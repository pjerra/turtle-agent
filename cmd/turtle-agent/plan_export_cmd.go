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

const planExportUsage = "usage: turtle-agent plan-export [--dir dungeons] [--allow-draft] [--drop-companions-loot] [--mmaps DIR] [--rev SHA] [--stamp YYYYMMDDHHMMSS] [--force] --out FILE|DIR (<plan>... | --all-live)"

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
	opt := planexport.Options{DropCompanionsLoot: *dropLoot}
	if *mmaps != "" {
		planner = navmesh.NewPlanner(*mmaps)
		defer planner.Close()
		opt.Cut = cutWith(planner)
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
