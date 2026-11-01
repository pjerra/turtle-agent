package main

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/pjerra/turtle-agent/internal/dungeon"
	"github.com/pjerra/turtle-agent/internal/planexport"
	"github.com/pjerra/turtle-agent/internal/trashcheck"
)

const trashPlan = `{"version":1,"name":"tc","map":70,"entrance":{"x":1,"y":2,"z":3},"level_band":[40,45],
 "legs":[{"name":"a","via":[{"x":1000,"y":0,"z":1},{"x":1100,"y":0,"z":1}],"to":{"x":1200,"y":0,"z":1},
          "pull_groups":[{"entries":[100],"positions":[{"x":1150,"y":15,"z":1}]}]}]}`

const trashCalmPlan = `{"version":1,"name":"calm","map":70,"entrance":{"x":1,"y":2,"z":3},"level_band":[40,45],
 "legs":[{"name":"a","via":[{"x":5000,"y":0,"z":1}],"to":{"x":5100,"y":0,"z":1},
          "pull_groups":[{"entries":[999],"positions":[{"x":5090,"y":0,"z":1}]}]}]}`

// trashFixture writes a two-plan dungeon folder and a world export: a pack 15 yd from the route the walk uses at x 1100-1200,
// and a warder walking a loop 20 yd from it.
func trashFixture(t *testing.T) (dir, spawns string) {
	t.Helper()
	dir = t.TempDir()
	plans := filepath.Join(dir, "dungeons")
	if err := os.MkdirAll(plans, 0o755); err != nil {
		t.Fatal(err)
	}
	for name, body := range map[string]string{"tc": trashPlan, "calm": trashCalmPlan} {
		if err := os.WriteFile(filepath.Join(plans, name+".json"), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	spawns = filepath.Join(dir, "spawns")
	if err := os.MkdirAll(spawns, 0o755); err != nil {
		t.Fatal(err)
	}
	creatures := "guid\tentry\tname\tlevel_min\tlevel_max\trank\tfaction\tnpc_flags\tx\ty\tz\twander\tmovement_type\tgroup_leader\tpatrol\tauras\tdetection_range\tflags_extra\tspeed_walk\tgroup_flags\tpool\n" +
		"900\t100\tTrash\t40\t40\t1\t103\t0\t1050\t15\t1\t0\t0\t0\t0\t\t20\t0\t1\t0\t0\n" +
		"52050\t12129\tWarder\t40\t40\t1\t103\t0\t1000\t20\t1\t5\t2\t0\t1\t\t20\t0\t1\t0\t0\n"
	if err := os.WriteFile(filepath.Join(spawns, "creatures.tsv"), []byte(creatures), 0o644); err != nil {
		t.Fatal(err)
	}
	movement := "guid\tpoint\tx\ty\tz\twaittime\n" +
		"52050\t1\t1000\t20\t1\t0\n52050\t2\t1000\t24\t1\t0\n"
	if err := os.WriteFile(filepath.Join(spawns, "movement.tsv"), []byte(movement), 0o644); err != nil {
		t.Fatal(err)
	}
	return dir, spawns
}

func runTrashCheck(t *testing.T, args ...string) (int, string, string) {
	t.Helper()
	var out, errb bytes.Buffer
	code := run(t.Context(), append([]string{"trash-check"}, args...), &out, &errb)
	return code, out.String(), errb.String()
}

// One input: a plan name, a .json path or --sql, never two.
func TestTrashCheckNeedsOneInput(t *testing.T) {
	for _, args := range [][]string{
		{},
		{"a", "b"},
		{"--sql", "x.sql", "tc"},
		{"--plan", "tc", "--sql", "x.sql"},
		{"--fail-on", "bogus", "tc"},
	} {
		code, _, stderr := runTrashCheck(t, args...)
		if code != exitUsage {
			t.Errorf("%v: exit %d, want %d (%s)", args, code, exitUsage, stderr)
		}
		if !strings.Contains(stderr, "usage: turtle-agent trash-check") && !strings.Contains(stderr, "trash-check:") {
			t.Errorf("%v: stderr does not say what is wrong: %q", args, stderr)
		}
	}
}

// The walk to the pull passes 15 yd from a pack: a danger in the text and the JSON, exit 21 with --fail-on danger, and the plan
// that passes nothing is clean.
func TestTrashCheckReportsDangerAndFailOn(t *testing.T) {
	dir, spawns := trashFixture(t)
	code, stdout, stderr := runTrashCheck(t, "--dir", filepath.Join(dir, "dungeons"), "--spawns", spawns, "tc")
	if code != exitOK {
		t.Fatalf("exit %d, want 0 (%s%s)", code, stdout, stderr)
	}
	for _, want := range []string{"DANGER: body pull", "Trash", "tc: ", "danger"} {
		if !strings.Contains(stdout, want) {
			t.Errorf("text lacks %q:\n%s", want, stdout)
		}
	}
	if !strings.Contains(stdout, "note: no LOS: through-wall aggro is over-counted") {
		t.Errorf("the report does not state its LOS limit:\n%s", stdout)
	}

	if code, _, _ := runTrashCheck(t, "--dir", filepath.Join(dir, "dungeons"), "--spawns", spawns, "--fail-on", "danger", "tc"); code != exitTrashCheckFound {
		t.Errorf("--fail-on danger: exit %d, want %d", code, exitTrashCheckFound)
	}
	if code, _, _ := runTrashCheck(t, "--dir", filepath.Join(dir, "dungeons"), "--spawns", spawns, "--fail-on", "patrol", "tc"); code != exitTrashCheckFound {
		t.Errorf("--fail-on patrol: exit %d, want %d (the warder's loop crosses the route)", code, exitTrashCheckFound)
	}
	if code, _, _ := runTrashCheck(t, "--dir", filepath.Join(dir, "dungeons"), "--spawns", spawns, "--fail-on", "any", "calm"); code != exitOK {
		t.Errorf("--fail-on any on the clean plan: exit %d, want 0", code)
	}

	code, stdout, _ = runTrashCheck(t, "--dir", filepath.Join(dir, "dungeons"), "--spawns", spawns, "--json", "tc")
	if code != exitOK {
		t.Fatalf("--json: exit %d", code)
	}
	var rep trashcheck.Report
	if err := json.Unmarshal([]byte(stdout), &rep); err != nil {
		t.Fatalf("--json output: %v\n%s", err, stdout)
	}
	if rep.Format != trashcheck.Format || rep.Plan != "tc" || rep.Map != 70 {
		t.Errorf("report %q %q %d, want %q, tc, map 70", rep.Format, rep.Plan, rep.Map, trashcheck.Format)
	}
	if rep.Counts.Danger == 0 {
		t.Errorf("no danger in counts %+v", rep.Counts)
	}
	if rep.Level != 40 || rep.Spread != 3 {
		t.Errorf("level %d spread %g, want the plan's 40 and the non-raid 3", rep.Level, rep.Spread)
	}
	if rep.Source != "json "+filepath.Join(dir, "dungeons", "tc.json") {
		t.Errorf("source %q, want the plan path", rep.Source)
	}
	if rep.SpawnsDir != spawns {
		t.Errorf("spawns_dir %q, want %q", rep.SpawnsDir, spawns)
	}
}

// --out writes both files, and --dash-out the dashboard's patrol map keyed by the leader's spawn guid.
func TestTrashCheckOutAndDashOut(t *testing.T) {
	dir, spawns := trashFixture(t)
	outDir := filepath.Join(dir, "out")
	dash := filepath.Join(dir, "patrols.json")
	code, _, stderr := runTrashCheck(t, "--dir", filepath.Join(dir, "dungeons"), "--spawns", spawns,
		"--out", outDir, "--dash-out", dash, "--json", "tc")
	if code != exitOK {
		t.Fatalf("exit %d (%s)", code, stderr)
	}
	data, err := os.ReadFile(filepath.Join(outDir, "trash-tc.json"))
	if err != nil {
		t.Fatal(err)
	}
	var rep trashcheck.Report
	if err := json.Unmarshal(data, &rep); err != nil {
		t.Fatalf("%v", err)
	}
	if txt, err := os.ReadFile(filepath.Join(outDir, "trash-tc.txt")); err != nil || !strings.Contains(string(txt), "tc: ") {
		t.Errorf("trash-tc.txt: %v %q", err, txt)
	}

	data, err = os.ReadFile(dash)
	if err != nil {
		t.Fatal(err)
	}
	var patrols map[string]trashcheck.DashPatrol
	if err := json.Unmarshal(data, &patrols); err != nil {
		t.Fatalf("%v\n%s", err, data)
	}
	p, ok := patrols["52050"]
	if !ok {
		t.Fatalf("dash-out keys %v, want 52050", patrols)
	}
	if len(p.Path) != 2 || p.PeriodS < 3 || p.PeriodS > 3.5 || !strings.Contains(p.Why, "creature_movement 52050") || !strings.HasPrefix(p.Src, "turtle-agent trash-check ") {
		t.Errorf("dash-out patrol %+v", p)
	}
	if len(p.Entries) != 1 || p.Entries[0] != 12129 {
		t.Errorf("dash-out entries %v, want [12129]", p.Entries)
	}
}

// --sql reads the rows the server runs (B1's lenient reader) and takes the level band from the plan file beside it.
func TestTrashCheckSQLInput(t *testing.T) {
	dir, spawns := trashFixture(t)
	var all []planexport.Rows
	for _, name := range []string{"tc", "calm"} {
		p, err := dungeon.Load(filepath.Join(dir, "dungeons", name+".json"))
		if err != nil {
			t.Fatal(err)
		}
		res, err := planexport.Build(p, planexport.Options{})
		if err != nil {
			t.Fatal(err)
		}
		all = append(all, res.Rows)
	}
	sqlPath := filepath.Join(dir, "migration.sql")
	if err := os.WriteFile(sqlPath, []byte(planexport.WriteSQL(all)), 0o644); err != nil {
		t.Fatal(err)
	}

	if code, _, stderr := runTrashCheck(t, "--dir", filepath.Join(dir, "dungeons"), "--spawns", spawns, "--sql", sqlPath); code != exitUsage {
		t.Fatalf("two plans without --plan: exit %d, want %d (%s)", code, exitUsage, stderr)
	}
	code, stdout, stderr := runTrashCheck(t, "--dir", filepath.Join(dir, "dungeons"), "--spawns", spawns, "--sql", sqlPath, "--plan", "tc", "--json")
	if code != exitOK {
		t.Fatalf("exit %d (%s)", code, stderr)
	}
	var rep trashcheck.Report
	if err := json.Unmarshal([]byte(stdout), &rep); err != nil {
		t.Fatal(err)
	}
	if rep.Plan != "tc" || rep.Level != 40 || rep.Source != "sql "+sqlPath {
		t.Errorf("plan %q level %d source %q", rep.Plan, rep.Level, rep.Source)
	}
}

// A missing world export and a missing level band are usage errors that say what to do.
func TestTrashCheckRefusesMissingData(t *testing.T) {
	dir, spawns := trashFixture(t)
	code, _, stderr := runTrashCheck(t, "--dir", filepath.Join(dir, "dungeons"), "--spawns", filepath.Join(dir, "nope"), "tc")
	if code != exitUsage || !strings.Contains(stderr, "tools/dungeongen/export.sh") {
		t.Errorf("missing export: exit %d, %q", code, stderr)
	}

	// A migration without the plan file beside it cannot know the party's level.
	p, err := dungeon.Load(filepath.Join(dir, "dungeons", "tc.json"))
	if err != nil {
		t.Fatal(err)
	}
	res, err := planexport.Build(p, planexport.Options{})
	if err != nil {
		t.Fatal(err)
	}
	sqlPath := filepath.Join(dir, "one.sql")
	if err := os.WriteFile(sqlPath, []byte(planexport.WriteSQL([]planexport.Rows{res.Rows})), 0o644); err != nil {
		t.Fatal(err)
	}
	empty := filepath.Join(dir, "empty")
	if err := os.MkdirAll(empty, 0o755); err != nil {
		t.Fatal(err)
	}
	code, _, stderr = runTrashCheck(t, "--dir", empty, "--spawns", spawns, "--sql", sqlPath)
	if code != exitUsage || !strings.Contains(stderr, "--level is required") {
		t.Errorf("no level: exit %d, %q", code, stderr)
	}
	code, _, stderr = runTrashCheck(t, "--dir", empty, "--spawns", spawns, "--sql", sqlPath, "--level", "40")
	if code != exitOK {
		t.Errorf("explicit --level: exit %d, %q", code, stderr)
	}
}
