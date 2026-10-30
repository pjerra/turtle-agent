package main

import (
	"bytes"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/pjerra/turtle-agent/internal/dungeon"
	"github.com/pjerra/turtle-agent/internal/mapview"
	"github.com/pjerra/turtle-agent/internal/navmesh"
	"github.com/pjerra/turtle-agent/internal/planexport"
	"github.com/pjerra/turtle-agent/internal/planprobe"
)

const planProbeUsage = "usage: turtle-agent plan-probe [--dir dungeons] [--sql FILE [--plan NAME]] --mmaps DIR [--json] [--out DIR] [--from-step N] [<plan>|<path.json>]"

const (
	// exitPlanProbeBad: at least one segment is bad (a plan the run leader should not walk as it stands).
	exitPlanProbeBad = 20
	// exitPlanProbeFailed: the navmesh could not answer (a build without cgo, no tiles or a broken tile): the probe did not run.
	exitPlanProbeFailed = 1
)

// planProbeNavmesh is what plan-probe asks the navmesh: the probe's own queries, the polygons of the plan's box (the floor
// cuts) and the exporter's long-hop cut (cutWith, so a JSON input's rows are cut as plan-export cuts them).
type planProbeNavmesh struct {
	nav   planprobe.Nav
	polys func(mapID uint32, x0, y0, x1, y1 float32) ([]navmesh.Polygon, bool, error)
	cut   func(uint32, dungeon.Point, dungeon.Point) ([]dungeon.Point, error)
	close func()
}

// planProbeOpen opens the navmesh of one mmaps folder; the caller closes it. Tests replace it with a fake.
var planProbeOpen = func(dir string) *planProbeNavmesh {
	pl := navmesh.NewPlanner(dir)
	return &planProbeNavmesh{nav: pl, polys: pl.Polygons, cut: cutWith(pl), close: pl.Close}
}

// planProbeCmd measures the run leader's rows of one plan on the navmesh and prints where its hops are bad or worth a look
// (README, "Plan probe"). It reads plan files, a migration and the mmaps folder, and nothing else.
func planProbeCmd(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("plan-probe", flag.ContinueOnError)
	fs.SetOutput(stderr)
	dir := fs.String("dir", "dungeons", "the folder with the plan files")
	sqlFile := fs.String("sql", "", "a deployed world migration (the exact rows the server runs; recommended)")
	planFlag := fs.String("plan", "", "with --sql: the plan to probe (needed when the file holds several)")
	mmaps := fs.String("mmaps", "", "folder with the server's mmaps (<map>.mmap and <map>*.mmtile); required")
	asJSON := fs.Bool("json", false, "print the report as JSON")
	out := fs.String("out", "", "folder for probe-<plan>.json and probe-<plan>.txt")
	fromStep := fs.Int("from-step", 0, "only the segments that end at this run-leader step or later")
	if err := fs.Parse(args); err != nil {
		return exitUsage
	}
	given := map[string]bool{}
	fs.Visit(func(f *flag.Flag) { given[f.Name] = true })
	fail := func(format string, a ...any) int {
		fmt.Fprintf(stderr, "plan-probe: "+format+"\n", a...)
		return exitUsage
	}
	if *mmaps == "" {
		fmt.Fprintln(stderr, planProbeUsage)
		return fail("--mmaps is required")
	}
	if given["from-step"] && *fromStep < 1 {
		return fail("--from-step takes a step number from 1")
	}
	switch {
	case *planFlag != "" && *sqlFile == "":
		return fail("--plan picks a plan in the --sql file; name the plan as the argument otherwise")
	case *sqlFile != "" && fs.NArg() > 0:
		return fail("name the plan or pass --sql, not both")
	case *sqlFile == "" && fs.NArg() != 1:
		fmt.Fprintln(stderr, planProbeUsage)
		return fail("name one plan, or pass --sql FILE")
	}

	nv := planProbeOpen(*mmaps)
	defer nv.close()
	rows, source, notes, err := planProbeRows(*sqlFile, *planFlag, *dir, fs.Arg(0), nv.cut)
	if err != nil {
		return fail("%v", err)
	}
	cuts, err := planProbeCuts(nv, rows)
	if err != nil {
		return probeUnavailable(stderr, err)
	}
	rep, err := planprobe.Probe(rows, nv.nav, cuts, planprobe.Options{FromStep: *fromStep})
	if err != nil {
		return probeUnavailable(stderr, err)
	}
	rep.Source = source
	rep.Mmaps = *mmaps
	rep.Notes = append(append([]string{}, notes...), rep.Notes...)

	if *out != "" {
		if !planName.MatchString(rep.Plan) {
			return fail("the plan name %q cannot name a report file; probe without --out", rep.Plan)
		}
		if err := os.MkdirAll(*out, 0o755); err != nil {
			return fail("%v", err)
		}
		var jb bytes.Buffer
		enc := json.NewEncoder(&jb)
		enc.SetIndent("", "  ")
		if err := enc.Encode(rep); err != nil {
			return fail("%v", err)
		}
		var text bytes.Buffer
		rep.Text(&text)
		jsonFile := filepath.Join(*out, "probe-"+rep.Plan+".json")
		textFile := filepath.Join(*out, "probe-"+rep.Plan+".txt")
		if err := os.WriteFile(jsonFile, jb.Bytes(), 0o644); err != nil {
			return fail("%v", err)
		}
		if err := os.WriteFile(textFile, text.Bytes(), 0o644); err != nil {
			return fail("%v", err)
		}
		fmt.Fprintf(stdout, "wrote %s and %s\n", jsonFile, textFile)
	} else if *asJSON {
		enc := json.NewEncoder(stdout)
		enc.SetIndent("", "  ")
		if err := enc.Encode(rep); err != nil {
			return fail("%v", err)
		}
	} else {
		rep.Text(stdout)
	}
	if rep.Counts.Bad > 0 {
		return exitPlanProbeBad
	}
	return exitOK
}

// probeUnavailable is the exit of a query the navmesh cannot answer; planCheckUnavailable names the reason.
func probeUnavailable(stderr io.Writer, err error) int {
	if why, ok := planCheckUnavailable(err); ok {
		fmt.Fprintf(stderr, "plan-probe: the navmesh cannot answer: %s\n", why)
	} else {
		fmt.Fprintf(stderr, "plan-probe: %v\n", err)
	}
	return exitPlanProbeFailed
}

// planProbeRows loads the input: one plan of a deployed migration (the recommended source: the rows the server runs) or a plan
// JSON, whose rows are rebuilt and cut as plan-export cuts them.
func planProbeRows(sqlFile, planFlag, dir, arg string, cut func(uint32, dungeon.Point, dungeon.Point) ([]dungeon.Point, error)) (planexport.Rows, string, []string, error) {
	if sqlFile != "" {
		b, err := os.ReadFile(sqlFile)
		if err != nil {
			return planexport.Rows{}, "", nil, err
		}
		all, notes, err := planexport.ParseMigration(string(b))
		if err != nil {
			return planexport.Rows{}, "", nil, fmt.Errorf("%s: %w", sqlFile, err)
		}
		var rows []planexport.Rows
		for _, r := range all {
			if planFlag == "" || r.Plan.Plan == planFlag {
				rows = append(rows, r)
			}
		}
		switch {
		case len(rows) == 0 && planFlag != "":
			return planexport.Rows{}, "", nil, fmt.Errorf("%s holds no plan %q (it holds %s)", sqlFile, planFlag, planProbePlanList(all))
		case len(rows) == 0:
			return planexport.Rows{}, "", nil, fmt.Errorf("%s holds no plan rows", sqlFile)
		case len(rows) > 1:
			return planexport.Rows{}, "", nil, fmt.Errorf("%s holds %d plans (%s): pass --plan NAME", sqlFile, len(rows), planProbePlanList(rows))
		}
		return rows[0], "sql " + sqlFile, notes, nil
	}
	file := arg
	if !strings.HasSuffix(file, ".json") {
		if !planName.MatchString(arg) {
			return planexport.Rows{}, "", nil, fmt.Errorf("%q is not a plan name (a-z, 0-9, _ and -) nor a .json path", arg)
		}
		file = filepath.Join(dir, arg+".json")
	}
	p, err := dungeon.Load(file)
	if err != nil {
		return planexport.Rows{}, "", nil, err
	}
	res, err := planexport.Build(p, planexport.Options{Cut: cut})
	if err != nil {
		return planexport.Rows{}, "", nil, err
	}
	return res.Rows, "json " + file + " (rebuilt, cut)", []string{"rows rebuilt from the plan JSON (cut like plan-export --mmaps)"}, nil
}

// planProbePlanList names the plans of a migration for an error.
func planProbePlanList(all []planexport.Rows) string {
	names := make([]string, len(all))
	for i, r := range all {
		names[i] = r.Plan.Plan
	}
	return strings.Join(names, ", ")
}

// planProbeCuts are the map's floor cuts over the plan's bounding box (the rows' x and y): mapview.SplitHeights of the
// navmesh polygons there. A plan with no positioned row has none.
func planProbeCuts(nv *planProbeNavmesh, rows planexport.Rows) ([]float32, error) {
	x0, y0 := float32(0), float32(0)
	x1, y1 := float32(0), float32(0)
	first := true
	for _, s := range rows.Steps {
		if s.X == 0 && s.Y == 0 && s.Z == 0 {
			continue
		}
		if first {
			x0, y0, x1, y1, first = s.X, s.Y, s.X, s.Y, false
			continue
		}
		x0, x1 = min(x0, s.X), max(x1, s.X)
		y0, y1 = min(y0, s.Y), max(y1, s.Y)
	}
	if first {
		return nil, nil
	}
	polys, _, err := nv.polys(rows.Plan.Map, x0, y0, x1, y1)
	if err != nil {
		return nil, err
	}
	return mapview.SplitHeights(polys, mapview.DefaultFloorOptions()), nil
}
