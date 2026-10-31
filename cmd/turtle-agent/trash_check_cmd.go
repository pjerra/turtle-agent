package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/pjerra/turtle-agent/internal/dungeon"
	"github.com/pjerra/turtle-agent/internal/navmesh"
	"github.com/pjerra/turtle-agent/internal/patrols"
	"github.com/pjerra/turtle-agent/internal/planexport"
	"github.com/pjerra/turtle-agent/internal/trashcheck"
	"github.com/pjerra/turtle-agent/internal/worldspawn"
)

// exitTrashCheckFound: with --fail-on, the named class of finding exists (spec 1.2).
const exitTrashCheckFound = 21

const trashCheckUsage = "usage: turtle-agent trash-check [--dir dungeons] [--sql FILE [--plan NAME]] [--spawns DIR] [--patrols FILE]... [--mmaps DIR] [--level N] [--spread YD] [--aggro-rate R] [--assist-yd 10] [--json] [--out DIR] [--dash-out FILE] [--fail-on danger|patrol|any] [<plan>|<path.json>]"

// trashCheckCmd reports what one walk of a plan wakes: body pulls before the plan's own pulls, the family assist hop, links and
// groups, and the patrol windows at the route and the stands (README, "Trash check"). It reads the plan rows, a world export
// and (optionally) the navmesh and patrol recordings; no server, no login.
func trashCheckCmd(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("trash-check", flag.ContinueOnError)
	fs.SetOutput(stderr)
	dir := fs.String("dir", "dungeons", "the folder with the plan files (and where a recorded patrol's spawns are found)")
	sql := fs.String("sql", "", "a deployed migration (the rows the server runs); the recommended input")
	planFlag := fs.String("plan", "", "with --sql: the plan to report when the file holds several")
	spawns := fs.String("spawns", "", "the world export of the plan's map (default <--dir>/../tools/dungeongen/testdata/map<N>)")
	mmaps := fs.String("mmaps", "", "folder with the server's mmaps: the route is the server's own path (without it: straight lines)")
	level := fs.Int("level", 0, "the party's lowest level (default: the plan's level_band[0])")
	spread := fs.Float64("spread", 0, "how far the party spreads from the route, yd (default 10 for a raid plan, else 3)")
	rate := fs.Float64("aggro-rate", 1, "scales every aggro radius")
	assist := fs.Float64("assist-yd", 10, "the family assist hop's radius, yd")
	asJSON := fs.Bool("json", false, "print the report as JSON")
	out := fs.String("out", "", "write trash-<plan>.json and trash-<plan>.txt there")
	dashOut := fs.String("dash-out", "", "write the patrols as the dashboard's patrols.json data file")
	failOn := fs.String("fail-on", "", "exit 21 when a finding of this class exists: danger, patrol or any")
	var patrolFiles []string
	fs.Func("patrols", "a tb-patrols recording: it replaces the DB patrol it matches and is otherwise added (repeatable)", func(v string) error {
		patrolFiles = append(patrolFiles, v)
		return nil
	})
	if err := fs.Parse(args); err != nil {
		return exitUsage
	}
	given := map[string]bool{}
	fs.Visit(func(f *flag.Flag) { given[f.Name] = true })
	fail := func(format string, a ...any) int {
		fmt.Fprintf(stderr, "trash-check: "+format+"\n", a...)
		return exitUsage
	}
	switch *failOn {
	case "", "danger", "patrol", "any":
	default:
		return fail("--fail-on %q: danger, patrol or any", *failOn)
	}
	if *planFlag != "" && *sql == "" {
		return fail("--plan names a plan of --sql")
	}
	if *sql != "" && fs.NArg() > 0 {
		return fail("name a plan or pass --sql, not both")
	}
	if *sql == "" && fs.NArg() != 1 {
		fmt.Fprintln(stderr, trashCheckUsage)
		return fail("%d inputs, want one plan name, a .json path or --sql", fs.NArg())
	}

	// The rows: the deployed migration (B1's lenient reader) or the plan file rebuilt with the exporter.
	var rows planexport.Rows
	var source string
	var planNotes []string
	var meta *dungeon.Plan // the plan file, when it is there: level band, raid size
	if *sql != "" {
		data, err := os.ReadFile(*sql)
		if err != nil {
			return fail("%v", err)
		}
		parsed, notes, err := planexport.ParseMigration(string(data))
		if err != nil {
			return fail("%v", err)
		}
		planNotes = append(planNotes, notes...)
		switch {
		case *planFlag != "":
			found := false
			for _, r := range parsed {
				if r.Plan.Plan == *planFlag {
					rows, found = r, true
					break
				}
			}
			if !found {
				var names []string
				for _, r := range parsed {
					names = append(names, r.Plan.Plan)
				}
				return fail("%s holds no plan %q (%s)", *sql, *planFlag, strings.Join(names, ", "))
			}
		case len(parsed) == 1:
			rows = parsed[0]
		default:
			var names []string
			for _, r := range parsed {
				names = append(names, r.Plan.Plan)
			}
			return fail("%s holds %d plans: pick one with --plan (%s)", *sql, len(parsed), strings.Join(names, ", "))
		}
		source = "sql " + *sql
	} else {
		path := fs.Arg(0)
		if !strings.HasSuffix(path, ".json") {
			if !planName.MatchString(path) {
				return fail("%q is not a plan name (a-z, 0-9, _ and -)", path)
			}
			path = filepath.Join(*dir, path+".json")
		}
		p, err := dungeon.Load(path)
		if err != nil {
			return fail("%v", err)
		}
		meta = p
		res, err := planexport.Build(meta, planexport.Options{})
		if err != nil {
			return fail("%v", err)
		}
		rows = res.Rows
		source = "json " + path
		if len(res.Warnings) > 0 {
			planNotes = append(planNotes, res.Warnings...)
		}
		if res.Cut != "" {
			planNotes = append(planNotes, "rows cut: "+res.Cut)
		}
		planNotes = append(planNotes, "rows rebuilt from the plan JSON")
	}
	if !planName.MatchString(rows.Plan.Plan) {
		return fail("the plan name %q is not a plan name (a-z, 0-9, _ and -)", rows.Plan.Plan)
	}
	if meta == nil {
		// The sweep mode: the plan file is not given. Read it for the level band and the raid size when it is there.
		if p, err := dungeon.Load(filepath.Join(*dir, rows.Plan.Plan+".json")); err == nil && p.Map == rows.Plan.Map {
			meta = p
		}
	}

	if !given["level"] {
		if meta == nil || len(meta.LevelBand) != 2 {
			return fail("--level is required: the plan file %s is not there (or gives no level_band)", filepath.Join(*dir, rows.Plan.Plan+".json"))
		}
		*level = meta.LevelBand[0]
	}
	if *level < 1 || *level > 60 {
		return fail("--level %d: 1-60", *level)
	}
	if !given["spread"] {
		if meta != nil && meta.IsRaid() {
			*spread = 10
		} else {
			*spread = 3
		}
	}
	if *spread < 0 {
		return fail("--spread %g: not negative", *spread)
	}
	if *rate <= 0 {
		return fail("--aggro-rate %g: a positive scale", *rate)
	}

	spawnDir := *spawns
	if spawnDir == "" {
		spawnDir = filepath.Join(*dir, "..", "tools", "dungeongen", "testdata", fmt.Sprintf("map%d", rows.Plan.Map))
	}
	if _, err := os.Stat(filepath.Join(spawnDir, "creatures.tsv")); err != nil {
		return fail("no world export in %s: pass --spawns DIR (tools/dungeongen/export.sh writes it)", spawnDir)
	}
	world, worldNotes, err := worldspawn.Load(spawnDir, rows.Plan.Map)
	if err != nil {
		return fail("%v", err)
	}

	var recorded []patrols.Patrol
	for _, path := range patrolFiles {
		f, err := patrols.Read(path)
		if err != nil {
			return fail("%v", err)
		}
		if f.Map != rows.Plan.Map {
			return fail("%s is a recording of map %d, the plan is map %d", path, f.Map, rows.Plan.Map)
		}
		recorded = append(recorded, f.Patrols...)
	}

	opt := trashcheck.Options{
		Level: *level, Spread: float32(*spread), AggroRate: float32(*rate), AssistYd: float32(*assist),
		Recorded: recorded,
	}
	if *mmaps != "" {
		planner := navmesh.NewPlanner(*mmaps)
		defer planner.Close()
		// One query tells whether the navmesh can answer at all (no cgo, no files for the map): that is exit 1, as plan-check's
		// is, not a silent straight-line route.
		if p, ok := firstPoint(rows); ok {
			v := navmesh.Vec3{p.X, p.Y, p.Z}
			if _, err := planner.ServerPath(rows.Plan.Map, v, v); err != nil {
				if reason, unavailable := planCheckUnavailable(err); unavailable {
					return fail("%s", reason)
				}
				return fail("%v", err)
			}
		}
		opt.Nav = planner
	}
	rep := trashcheck.Check(rows, world, opt)
	rep.Source = source
	rep.SpawnsDir = spawnDir
	rep.Notes = append(rep.Notes, planNotes...)
	rep.Notes = append(rep.Notes, worldNotes...)

	if *out != "" {
		if err := writeTrashCheck(*out, rep); err != nil {
			return fail("%v", err)
		}
	}
	if *dashOut != "" {
		if err := writeDashPatrols(*dashOut, rep); err != nil {
			return fail("%v", err)
		}
	}
	if *asJSON {
		enc := json.NewEncoder(stdout)
		enc.SetIndent("", "  ")
		if err := enc.Encode(rep); err != nil {
			return fail("%v", err)
		}
	} else {
		fmt.Fprint(stdout, rep.Text())
	}

	if *failOn != "" && rep.Finds(*failOn) {
		return exitTrashCheckFound
	}
	return exitOK
}

// firstPoint is the plan's first positioned row, the probe the navmesh availability check queries.
func firstPoint(rows planexport.Rows) (dungeon.Point, bool) {
	for _, s := range rows.Steps {
		if s.X != 0 || s.Y != 0 || s.Z != 0 {
			return dungeon.Point{X: s.X, Y: s.Y, Z: s.Z}, true
		}
	}
	return dungeon.Point{}, false
}

// writeTrashCheck writes trash-<plan>.json and trash-<plan>.txt into dir.
func writeTrashCheck(dir string, rep *trashcheck.Report) error {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	data, err := json.MarshalIndent(rep, "", "  ")
	if err != nil {
		return err
	}
	data = append(data, '\n')
	if err := os.WriteFile(filepath.Join(dir, "trash-"+rep.Plan+".json"), data, 0o644); err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(dir, "trash-"+rep.Plan+".txt"), []byte(rep.Text()), 0o644)
}

// writeDashPatrols writes the report's patrols as the dashboard's patrol data file (the plan layer's wake.json shape, keyed by
// patrol id), for the phase-A backlog's patrol-aware rings.
func writeDashPatrols(path string, rep *trashcheck.Report) error {
	src := "turtle-agent trash-check " + time.Now().Format("2006-01-02")
	data, err := json.MarshalIndent(rep.DashOut(src), "", "  ")
	if err != nil {
		return err
	}
	data = append(data, '\n')
	return os.WriteFile(path, data, 0o600)
}
