package main

import (
	"errors"
	"flag"
	"fmt"
	"io"
	"math"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/pjerra/turtle-agent/internal/dungeon"
	"github.com/pjerra/turtle-agent/internal/navmesh"
	"github.com/pjerra/turtle-agent/internal/planexport"
)

const planExportUsage = "usage: turtle-agent plan-export [--dir dungeons] [--allow-draft] [--drop-companions-loot] [--carrier-chests] [--gm-cross-direct] [--approach-hops] [--skip-legs NAME,...] [--with-optional NAME,...] [--mmaps DIR] [--rev SHA] [--stamp YYYYMMDDHHMMSS] [--force] --out FILE|DIR (<plan>... | --all-live)"

// migrationName is what a TortoiseBots world migration is called (data/sql/world/).
var migrationName = regexp.MustCompile(`^\d{14}_world\.sql$`)

// planExportCmd exports dungeon plans as one idempotent world migration for TortoiseBots' run leader (`.bot run`): the rows of
// ai_dungeon_plan, ai_dungeon_leg and ai_dungeon_step, from dungeons/<plan>.json exactly per TortoiseBots'
// docs/guides/dungeon-run-leader.md (README, "Exporting plans").
func planExportCmd(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("plan-export", flag.ContinueOnError)
	fs.SetOutput(stderr)
	dir := fs.String("dir", "dungeons", "the folder with the plan files")
	out := fs.String("out", "", "the migration file to write, or a folder: the file is then named <stamp>_world.sql")
	allLive := fs.Bool("all-live", false, "export every plan whose status is live-checked")
	allowDraft := fs.Bool("allow-draft", false, "export a named plan whose status is draft (refused without)")
	mmaps := fs.String("mmaps", "", "folder with the server's mmaps: a hop of more than 60 yd between two moves is cut into via points along the navmesh path (without it: only listed)")
	rev := fs.String("rev", "", "the turtle-agent commit named in ai_dungeon_plan.source (default: git rev-parse of the plan folder, if it is a checkout)")
	stamp := fs.String("stamp", "", "the migration's timestamp, YYYYMMDDHHMMSS (default: now, UTC)")
	dropLoot := fs.Bool("drop-companions-loot", false, "export a plan with a leg whose companions_loot is false anyway, without it (the rows cannot carry it: the run leader's bots loot; refused without)")
	carrierChests := fs.Bool("carrier-chests", false, "with --drop-companions-loot, export such a leg's loot_object steps as carrier rows: a move to its stand, the loot (gone-is-done flag 256), and — when the leg has leftover_entries — their sweep pull at the point; the bots' looting still has to be turned off for the leg in the setup, or a module's trapped-chest rule has to skip them; flag 256 needs a TortoiseBots module with the gone-is-done step flag: an older module ignores it and pauses on a despawned grave after a wipe (then a .bot run skip skips the row)")
	gmCrossDirect := fs.Bool("gm-cross-direct", false, "export a gm_cross waypoint as a move_direct (a straight walk to the landing point) instead of refusing the plan; only for a gap that is a drop the line crosses, and not tried live")
	approach := fs.Bool("approach-hops", false, "with --mmaps, also cut the hop to a pull, boss or hold that is more than 50 yd from the anchor before it into move rows along the navmesh path (the run leader's own hops are at most 8 of 40 yd)")
	skipLegs := fs.String("skip-legs", "", "leg names (comma list) left out of the rows, each with a warning; the later legs close up. An unknown name is refused")
	withOptional := fs.String("with-optional", "", "optional leg names (comma list) written into the rows anyway (Zul'Farrak's gahzrilla), each with a warning: the item the leg needs is the operator's to give to the lead bot before .bot run start (a GM .additem); a name that is no optional leg is refused")
	force := fs.Bool("force", false, "let --out replace an existing file (never edit a migration that is applied)")
	if err := fs.Parse(args); err != nil {
		return exitUsage
	}
	fail := func(format string, a ...any) int {
		fmt.Fprintf(stderr, "plan-export: "+format+"\n", a...)
		return exitUsage
	}
	names := fs.Args()
	switch {
	case *out == "":
		fmt.Fprintln(stderr, planExportUsage)
		return fail("--out is required")
	case *allLive && len(names) > 0:
		return fail("name the plans or pass --all-live, not both")
	case !*allLive && len(names) == 0:
		fmt.Fprintln(stderr, planExportUsage)
		return fail("name at least one plan, or pass --all-live")
	case *carrierChests && !*dropLoot:
		fmt.Fprintln(stderr, planExportUsage)
		return fail("--carrier-chests needs --drop-companions-loot (the companions_loot-false leg's chest rows are not exported without it)")
	}
	if *allLive {
		m, err := filepath.Glob(filepath.Join(*dir, "*.json"))
		if err != nil {
			return fail("%v", err)
		}
		for _, f := range m {
			base := strings.TrimSuffix(filepath.Base(f), ".json")
			if strings.Contains(base, ".") { // x.tune.json
				continue
			}
			p, err := dungeon.Load(f)
			if err != nil {
				return fail("%v", err)
			}
			if planexport.IsLive(p) {
				names = append(names, base)
			}
		}
		sort.Strings(names)
		if len(names) == 0 {
			return fail("no live-checked plan in %s", *dir)
		}
	}
	ts := time.Now().UTC()
	if *stamp != "" {
		t, err := time.Parse(planexport.StampFormat, *stamp)
		if err != nil {
			return fail("--stamp %q: want YYYYMMDDHHMMSS", *stamp)
		}
		ts = t
	}
	target := *out
	if st, err := os.Stat(target); (err == nil && st.IsDir()) || strings.HasSuffix(target, "/") {
		target = filepath.Join(target, planexport.FileName(ts))
	} else if !migrationName.MatchString(filepath.Base(target)) {
		fmt.Fprintf(stderr, "plan-export: note: %s is not named YYYYMMDDHHMMSS_world.sql, so TortoiseBots' updater will not take it\n", filepath.Base(target))
	}
	if _, err := os.Stat(target); err == nil && !*force {
		return fail("%s exists: a migration that is applied is never edited; export to a new name, or pass --force", target)
	}
	var planner *navmesh.Planner
	opt := planexport.Options{DropCompanionsLoot: *dropLoot, CarrierChests: *carrierChests, GMCrossDirect: *gmCrossDirect, ApproachHops: *approach}
	for _, n := range strings.Split(*skipLegs, ",") {
		if n = strings.TrimSpace(n); n != "" {
			opt.SkipLegs = append(opt.SkipLegs, n)
		}
	}
	for _, n := range strings.Split(*withOptional, ",") {
		if n = strings.TrimSpace(n); n != "" {
			opt.WithOptional = append(opt.WithOptional, n)
		}
	}
	var naive *navmesh.Planner
	if *mmaps != "" {
		planner = navmesh.NewPlanner(*mmaps)
		defer planner.Close()
		opt.Cut = cutWith(planner)
		// What the run leader's bots walk: the server's navmesh, which does not know a plan's avoid boxes.
		naive = navmesh.NewPlanner(*mmaps)
		defer naive.Close()
	}
	revision := *rev
	if revision == "" {
		revision = gitRev(*dir)
	}
	var all []planexport.Rows
	total := 0
	for _, n := range names {
		if !planName.MatchString(n) {
			return fail("%q is not a plan name (a-z, 0-9, _ and -)", n)
		}
		file := filepath.Join(*dir, n+".json")
		p, err := dungeon.Load(file)
		if err != nil {
			return fail("%v", err)
		}
		if msg := planexport.Refusal(p, *allowDraft); msg != "" {
			return fail("%s", msg)
		}
		o := opt
		if planner != nil {
			// The cutter keeps out of the plan's avoid boxes; the rows must keep the run leader's own path out of them as well.
			boxes := dungeon.NavBoxes(p.Avoid)
			planner.SetAvoid(p.Map, boxes)
			if len(boxes) > 0 {
				o.Route = routeAround(naive, planner, boxes)
			}
		}
		o.Source = "turtle-agent"
		if revision != "" {
			o.Source += " " + revision
		}
		o.Source += " dungeons/" + n + ".json"
		res, err := planexport.Build(p, o)
		if err != nil && *allLive && errors.Is(err, planexport.ErrCompanionsLoot) {
			// A batch lists the plans it cannot export and goes on with the rest; a plan named on the command line fails the command.
			fmt.Fprintf(stderr, "plan-export: %s: skipped: %v\n", n, err)
			continue
		}
		if err != nil {
			return fail("%v", err)
		}
		for _, w := range res.Warnings {
			fmt.Fprintf(stderr, "plan-export: %s: %s\n", n, w)
		}
		all = append(all, res.Rows)
		total += res.Rows.Count()
		fmt.Fprintf(stdout, "%s: map %d, %d legs, %d steps (%d rows)\n", n, p.Map, len(res.Rows.Legs), len(res.Rows.Steps), res.Rows.Count())
		if res.Cut != "" {
			fmt.Fprintf(stdout, "%s: CUT: %d of %d legs exported (see the note above)\n", n, len(res.Rows.Legs), len(p.Legs))
		}
	}
	if len(all) == 0 {
		return fail("no plan could be exported")
	}
	sql := planexport.WriteSQL(all)
	// The check: what was written reads back as the rows that were built.
	back, err := planexport.Parse(sql)
	if err != nil {
		return fail("the migration does not read back: %v", err)
	}
	if err := planexport.Equal(all, back); err != nil {
		return fail("the migration does not read back as the plans: %v", err)
	}
	if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
		return fail("%v", err)
	}
	if err := os.WriteFile(target, []byte(sql), 0o644); err != nil {
		return fail("%v", err)
	}
	fmt.Fprintf(stdout, "wrote %s: %d plans, %d rows\n", target, len(all), total)
	return exitOK
}

// cutWith cuts a hop along the navmesh path, leaving hops of at most planexport.MoveGapCutYd.
func cutWith(pl *navmesh.Planner) func(uint32, dungeon.Point, dungeon.Point) ([]dungeon.Point, error) {
	return func(mapID uint32, from, to dungeon.Point) ([]dungeon.Point, error) {
		path, err := pl.Plan(mapID, navmesh.Vec3{from.X, from.Y, from.Z}, navmesh.Vec3{to.X, to.Y, to.Z})
		if err != nil {
			return nil, err
		}
		if path.Status != navmesh.Complete || len(path.Points) < 2 {
			return nil, fmt.Errorf("no complete path (%s %s)", path.Status, path.Reason)
		}
		var via []dungeon.Point
		prev := path.Points[0]
		for i := 1; i < len(path.Points); i++ {
			p := path.Points[i]
			d := math.Sqrt(float64(sq32(p[0]-prev[0]) + sq32(p[1]-prev[1]) + sq32(p[2]-prev[2])))
			// A corner is a point of the route; a long stretch between two corners is split into equal hops.
			for k := 1; float64(k)*planexport.MoveGapCutYd < d; k++ {
				f := float32(float64(k) * planexport.MoveGapCutYd / d)
				via = append(via, dungeon.Point{X: prev[0] + (p[0]-prev[0])*f, Y: prev[1] + (p[1]-prev[1])*f, Z: prev[2] + (p[2]-prev[2])*f})
			}
			if i < len(path.Points)-1 {
				via = append(via, dungeon.Point{X: p[0], Y: p[1], Z: p[2]})
			}
			prev = p
		}
		return via, nil
	}
}

func sq32(v float32) float32 { return v * v }

// gitRev is the short commit of the checkout holding dir, "+local" when the folder has uncommitted changes; "" when it is no checkout.
func gitRev(dir string) string {
	o, err := exec.Command("git", "-C", dir, "rev-parse", "--short=8", "HEAD").Output()
	if err != nil {
		return ""
	}
	rev := strings.TrimSpace(string(o))
	if s, err := exec.Command("git", "-C", dir, "status", "--porcelain", "--", ".").Output(); err == nil && strings.TrimSpace(string(s)) != "" {
		rev += "+local"
	}
	return rev
}

// routeAround returns the via points that keep the run leader's own path from a to b out of the avoid boxes: none when the path on the
// plain navmesh (naive: the server's) stays clear of them, else the corners of the avoid-aware path (aware), cut to hops of at most
// routeGapYd, refined until the plain path of every hop is clear too.
func routeAround(naive, aware *navmesh.Planner, boxes []navmesh.Box) func(uint32, dungeon.Point, dungeon.Point) ([]dungeon.Point, error) {
	vec := func(p dungeon.Point) navmesh.Vec3 { return navmesh.Vec3{p.X, p.Y, p.Z} }
	pt := func(v navmesh.Vec3) dungeon.Point { return dungeon.Point{X: v[0], Y: v[1], Z: v[2]} }
	plain := func(m uint32, a, b dungeon.Point) []navmesh.Vec3 {
		path, err := naive.Plan(m, vec(a), vec(b))
		if err != nil || path.Status != navmesh.Complete || len(path.Points) < 2 {
			return nil // no server path either: the plan check reports it, a via point would not help
		}
		return path.Points
	}
	var fix func(m uint32, a, b dungeon.Point, gap float64, depth int) ([]dungeon.Point, error)
	fix = func(m uint32, a, b dungeon.Point, gap float64, depth int) ([]dungeon.Point, error) {
		if !pathHitsBox(plain(m, a, b), boxes) {
			return nil, nil
		}
		if depth > 3 {
			return nil, fmt.Errorf("the run leader's path from %.1f %.1f %.1f to %.1f %.1f %.1f crosses an avoid box and no via point makes it safe", a.X, a.Y, a.Z, b.X, b.Y, b.Z)
		}
		ap, err := aware.Plan(m, vec(a), vec(b))
		if err != nil || ap.Status != navmesh.Complete || len(ap.Points) < 2 {
			return nil, fmt.Errorf("the run leader's path from %.1f %.1f %.1f to %.1f %.1f %.1f crosses an avoid box and the avoid-aware navmesh has no path (%v)", a.X, a.Y, a.Z, b.X, b.Y, b.Z, err)
		}
		// The chain a, <aware path cut to gap yd hops>, b.
		chain := []dungeon.Point{a}
		prev := ap.Points[0]
		for i := 1; i < len(ap.Points); i++ {
			p := ap.Points[i]
			d := math.Sqrt(float64(sq32(p[0]-prev[0]) + sq32(p[1]-prev[1]) + sq32(p[2]-prev[2])))
			for k := 1; float64(k)*gap < d; k++ {
				f := float32(float64(k) * gap / d)
				chain = append(chain, dungeon.Point{X: prev[0] + (p[0]-prev[0])*f, Y: prev[1] + (p[1]-prev[1])*f, Z: prev[2] + (p[2]-prev[2])*f})
			}
			if i < len(ap.Points)-1 {
				chain = append(chain, pt(p))
			}
			prev = p
		}
		chain = append(chain, b)
		var via []dungeon.Point
		for i := 0; i+1 < len(chain); i++ {
			sub, err := fix(m, chain[i], chain[i+1], gap/2, depth+1)
			if err != nil {
				return nil, err
			}
			if i > 0 {
				via = append(via, chain[i])
			}
			via = append(via, sub...)
		}
		return via, nil
	}
	return func(m uint32, a, b dungeon.Point) ([]dungeon.Point, error) {
		return fix(m, a, b, routeGapYd, 0)
	}
}

// routeGapYd is the longest hop between the via points routeAround puts in (the run leader's own hops are at most 40 yd).
const routeGapYd = 25

// pathHitsBox says whether the polyline passes through a box (sampled every yard, heights interpolated).
func pathHitsBox(pts []navmesh.Vec3, boxes []navmesh.Box) bool {
	for i := 0; i+1 < len(pts); i++ {
		a, b := pts[i], pts[i+1]
		d := math.Sqrt(float64(sq32(b[0]-a[0]) + sq32(b[1]-a[1]) + sq32(b[2]-a[2])))
		n := int(math.Ceil(d))
		for k := 0; k <= n; k++ {
			f := float32(0)
			if n > 0 {
				f = float32(k) / float32(n)
			}
			p := navmesh.Vec3{a[0] + (b[0]-a[0])*f, a[1] + (b[1]-a[1])*f, a[2] + (b[2]-a[2])*f}
			for _, bx := range boxes {
				if bx.Contains(p) {
					return true
				}
			}
		}
	}
	return false
}
