package main

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/pjerra/turtle-agent/internal/dungeon"
	"github.com/pjerra/turtle-agent/internal/worldspawn"
)

// mustWalkReadyPlan loads a plan file the test just wrote.
func mustWalkReadyPlan(t *testing.T, path string) *dungeon.Plan {
	t.Helper()
	p, err := dungeon.Load(path)
	if err != nil {
		t.Fatal(err)
	}
	return p
}

// wrRow is one creatures.tsv row (level 60, rank 0, the given faction and no
// flags): guid, entry, name, x, y, z, wander, movement type, detection yd.
func wrRow(guid uint64, entry uint32, name string, faction int, x, y, z, wander float32, movementType int, detection float32) string {
	return fmt.Sprintf("%d\t%d\t%s\t60\t60\t0\t%d\t0\t%v\t%v\t%v\t%v\t%d\t0\t0\t\t%v\t0\t1\t0\t0\n",
		guid, entry, name, faction, x, y, z, wander, movementType, detection)
}

// walkReadyDir writes a minimal world export (creatures, groups, links,
// movement) for the synthetic walk-ready tests and returns its folder.
func walkReadyDir(t *testing.T, creatures, movement string) string {
	t.Helper()
	dir := t.TempDir()
	header := "guid\tentry\tname\tlevel_min\tlevel_max\trank\tfaction\tnpc_flags\tx\ty\tz\twander\tmovement_type\tgroup_leader\tpatrol\tauras\tdetection_range\tflags_extra\tspeed_walk\tgroup_flags\tpool\n"
	files := map[string]string{
		"creatures.tsv":         header + creatures,
		"movement.tsv":          "guid\tpoint\tx\ty\tz\twaittime\n" + movement,
		"movement_template.tsv": "entry\tpoint\tx\ty\tz\twaittime\n",
		"links.tsv":             "guid\tmaster_guid\tflag\n",
		"link_templates.tsv":    "entry\tmap\tmaster_entry\tflag\tsearch_range\n",
		"groups.tsv":            "leader_guid\tmember_guid\tdist\tangle\tflags\n",
		"gameobjects.tsv":       "guid\tentry\tname\ttype\tx\ty\tz\tkey_item\tkey_name\n",
		"entrances.tsv":         "x\ty\tz\n0\t0\t0\n",
	}
	for name, body := range files {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	return dir
}

// walkReadyPlan writes the plan JSON and returns its path.
func walkReadyPlan(t *testing.T, js string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "p.json")
	if err := os.WriteFile(p, []byte(js), 0o600); err != nil {
		t.Fatal(err)
	}
	return p
}

func walkReadyLoad(t *testing.T, planPath, dir string) ([]creature, *worldspawn.World) {
	t.Helper()
	cs, err := readCreatures(filepath.Join(dir, "creatures.tsv"))
	if err != nil {
		t.Fatal(err)
	}
	w, _, err := worldspawn.Load(dir, 0)
	if err != nil {
		t.Fatal(err)
	}
	_ = planPath
	return cs, w
}

func walkReadyFilter() PullFilter {
	return PullFilter{Ignore: map[int]bool{35: true}}
}

func walkReadyRules(fs []WalkReadyFinding) map[string]int {
	out := map[string]int{}
	for _, f := range fs {
		out[f.Rule]++
	}
	return out
}

// Rule 1: a two-mob pack 15 yd from the stand joins through the ring and
// fails over its ceiling of 1.
func TestWalkReadyOverCeilingFails(t *testing.T) {
	dir := walkReadyDir(t,
		wrRow(1, 1, "One", 14, 30, 0, 0, 0, 0, 20)+
			wrRow(2, 1, "Two", 14, 45, 0, 0, 0, 0, 20), "")
	p := walkReadyPlan(t, `{"version":1,"name":"t","map":0,"entrance":{"x":0,"y":0,"z":0},"level_band":[60,60],"legs":[
 {"name":"a","to":{"x":0,"y":0,"z":0},"pull_groups":[{"entries":[1],"positions":[{"x":30,"y":0,"z":0}],"max_bodies":1}]}]}`)
	_ = p
	plan := mustWalkReadyPlan(t, p)
	cs, w := walkReadyLoad(t, p, dir)
	fs, err := WalkReady(plan, cs, w, walkReadyFilter(), "")
	if err != nil {
		t.Fatal(err)
	}
	if got := walkReadyRules(fs)[WalkReadyOverCeiling]; got != 1 {
		t.Fatalf("over-ceiling findings: %d, want 1 (%v)", got, fs)
	}
	// The mutation drops the ceiling past the estimate: the finding goes quiet.
	p2 := walkReadyPlan(t, `{"version":1,"name":"t","map":0,"entrance":{"x":0,"y":0,"z":0},"level_band":[60,60],"legs":[
 {"name":"a","to":{"x":0,"y":0,"z":0},"pull_groups":[{"entries":[1],"positions":[{"x":30,"y":0,"z":0}],"max_bodies":2}]}]}`)
	plan2 := mustWalkReadyPlan(t, p2)
	fs2, err := WalkReady(plan2, cs, w, walkReadyFilter(), "")
	if err != nil {
		t.Fatal(err)
	}
	if got := walkReadyRules(fs2)[WalkReadyOverCeiling]; got != 0 {
		t.Fatalf("over-ceiling findings after raising the ceiling: %d, want 0", got)
	}
}

// Rule 1b: accept_over_ceiling keeps the finding and rides in the report.
func TestWalkReadyAcceptOverCeilingKeepsFinding(t *testing.T) {
	dir := walkReadyDir(t,
		wrRow(1, 1, "One", 14, 30, 0, 0, 0, 0, 20)+
			wrRow(2, 1, "Two", 14, 45, 0, 0, 0, 0, 20), "")
	p := walkReadyPlan(t, `{"version":1,"name":"t","map":0,"entrance":{"x":0,"y":0,"z":0},"level_band":[60,60],"legs":[
 {"name":"a","to":{"x":0,"y":0,"z":0},"pull_groups":[{"entries":[1],"positions":[{"x":30,"y":0,"z":0}],"max_bodies":1,"accept_over_ceiling":"deliberate 2-body pull"}]}]}`)
	plan := mustWalkReadyPlan(t, p)
	cs, w := walkReadyLoad(t, p, dir)
	fs, err := WalkReady(plan, cs, w, walkReadyFilter(), "")
	if err != nil {
		t.Fatal(err)
	}
	var over []WalkReadyFinding
	for _, f := range fs {
		if f.Rule == WalkReadyOverCeiling {
			over = append(over, f)
		}
	}
	if len(over) != 1 {
		t.Fatalf("over-ceiling findings: %d, want 1 (accepted rows stay findings)", len(over))
	}
	if !strings.Contains(over[0].Note, "deliberate 2-body pull") {
		t.Fatalf("finding note %q does not carry the accept reason", over[0].Note)
	}
}

// Rule 2: a pull position below z 0 inside the Zul'Gurub lake basin is in
// the lake; above the surface it is not.
func TestWalkReadyPointInWater(t *testing.T) {
	dir := walkReadyDir(t, wrRow(1, 1, "One", 14, -11700, -1600, 30, 0, 0, 20), "")
	p := walkReadyPlan(t, `{"version":1,"name":"t","map":309,"entrance":{"x":0,"y":0,"z":0},"level_band":[58,60],"legs":[
 {"name":"a","to":{"x":-11700,"y":-1600,"z":30},"pull_groups":[{"entries":[1],"positions":[{"x":-11750,"y":-1600,"z":-5}]}]}]}`)
	plan := mustWalkReadyPlan(t, p)
	cs, w := walkReadyLoad(t, p, dir)
	fs, err := WalkReady(plan, cs, w, walkReadyFilter(), "")
	if err != nil {
		t.Fatal(err)
	}
	if got := walkReadyRules(fs)[WalkReadyPointInWater]; got < 1 {
		t.Fatalf("point-in-water findings: %d, want >= 1 (%v)", got, fs)
	}
	// The mutation lifts the point above the surface: the finding goes quiet.
	p2 := walkReadyPlan(t, `{"version":1,"name":"t","map":309,"entrance":{"x":0,"y":0,"z":0},"level_band":[58,60],"legs":[
 {"name":"a","to":{"x":-11700,"y":-1600,"z":30},"pull_groups":[{"entries":[1],"positions":[{"x":-11750,"y":-1600,"z":30}]}]}]}`)
	plan2 := mustWalkReadyPlan(t, p2)
	fs2, err := WalkReady(plan2, cs, w, walkReadyFilter(), "")
	if err != nil {
		t.Fatal(err)
	}
	if got := walkReadyRules(fs2)[WalkReadyPointInWater]; got != 0 {
		t.Fatalf("point-in-water findings after lifting the point: %d, want 0", got)
	}
}

// Rule 3: a patrol loop (movement_type 2) crossed by the route before its
// own pull row is a finding. The patroller owns the second row; the walk to
// the first crosses its loop.
func TestWalkReadyPatrolCrossing(t *testing.T) {
	dir := walkReadyDir(t,
		wrRow(1, 1, "Pack", 14, 100, 0, 0, 0, 0, 20)+
			wrRow(2, 2, "Patroller", 14, 10, 0, 0, 0, 2, 20),
		"2\t1\t10\t5\t0\t0\n2\t2\t10\t-5\t0\t0\n")
	p := walkReadyPlan(t, `{"version":1,"name":"t","map":0,"entrance":{"x":0,"y":0,"z":0},"level_band":[60,60],"legs":[
 {"name":"a","to":{"x":0,"y":0,"z":0},"pull_groups":[
  {"entries":[1],"positions":[{"x":100,"y":0,"z":0}]},
  {"entries":[2],"positions":[{"x":10,"y":0,"z":0}]}]}]}`)
	plan := mustWalkReadyPlan(t, p)
	cs, w := walkReadyLoad(t, p, dir)
	fs, err := WalkReady(plan, cs, w, walkReadyFilter(), "")
	if err != nil {
		t.Fatal(err)
	}
	if got := walkReadyRules(fs)[WalkReadyPatrolCrossing]; got != 1 {
		t.Fatalf("patrol-crossing findings: %d, want 1 (%v)", got, fs)
	}
}

// Rule 4: the leg start (its `to`) within aggro reach of a live spawn is
// exposed.
func TestWalkReadyLegExposed(t *testing.T) {
	dir := walkReadyDir(t, wrRow(1, 1, "Lurker", 14, 12, 0, 0, 0, 0, 20), "")
	p := walkReadyPlan(t, `{"version":1,"name":"t","map":0,"entrance":{"x":0,"y":0,"z":0},"level_band":[60,60],"legs":[
 {"name":"a","to":{"x":0,"y":0,"z":0},"pull_groups":[{"entries":[2],"positions":[{"x":200,"y":0,"z":0}]}]}]}`)
	plan := mustWalkReadyPlan(t, p)
	cs, w := walkReadyLoad(t, p, dir)
	fs, err := WalkReady(plan, cs, w, walkReadyFilter(), "")
	if err != nil {
		t.Fatal(err)
	}
	if got := walkReadyRules(fs)[WalkReadyLegExposed]; got < 1 {
		t.Fatalf("leg-exposed findings: %d, want >= 1 (%v)", got, fs)
	}
}

// Rule 5: a hostile spawn within reach of the route that no row of this
// leg owns (owned by a later leg) is unowned near the leg.
func TestWalkReadyUnownedNearLeg(t *testing.T) {
	dir := walkReadyDir(t,
		wrRow(1, 1, "Early", 14, 0, 0, 0, 0, 0, 20)+
			wrRow(2, 2, "Later", 14, 10, 0, 0, 5, 0, 20), "")
	p := walkReadyPlan(t, `{"version":1,"name":"t","map":0,"entrance":{"x":0,"y":0,"z":0},"level_band":[60,60],"legs":[
 {"name":"a","to":{"x":-50,"y":0,"z":0},"pull_groups":[{"entries":[1],"positions":[{"x":0,"y":0,"z":0}]}]},
 {"name":"b","to":{"x":0,"y":0,"z":0},"pull_groups":[{"entries":[2],"positions":[{"x":100,"y":0,"z":0}]}]}]}`)
	plan := mustWalkReadyPlan(t, p)
	cs, w := walkReadyLoad(t, p, dir)
	fs, err := WalkReady(plan, cs, w, walkReadyFilter(), "")
	if err != nil {
		t.Fatal(err)
	}
	if got := walkReadyRules(fs)[WalkReadyUnownedNearLeg]; got < 1 {
		t.Fatalf("unowned findings: %d, want >= 1 (%v)", got, fs)
	}
}

// Rule 6: no quiet placement point at the leg start fails; a hot `to` with
// a quiet corner passes, and place_at is checked too.
func TestWalkReadyNoQuietStart(t *testing.T) {
	// The lurker is entry 99 (in no pull group): the quiet rule treats it
	// as an earlier leg's pack (30-yd limit) while rules 4-5 skip the plan's
	// own entries only when a row owns them. The hot `to` at 10 yd fails the
	// 30-yd limit; the quiet `to` at 100 yd clears every candidate.
	dir := walkReadyDir(t, wrRow(1, 99, "Lurker", 14, 0, 0, 0, 0, 0, 20), "")
	hot := walkReadyPlan(t, `{"version":1,"name":"t","map":0,"entrance":{"x":0,"y":0,"z":0},"level_band":[60,60],"legs":[
 {"name":"a","to":{"x":10,"y":0,"z":0},"pull_groups":[{"entries":[1],"positions":[{"x":12,"y":0,"z":0}]}]}]}`)
	plan := mustWalkReadyPlan(t, hot)
	cs, w := walkReadyLoad(t, hot, dir)
	fs, err := WalkReady(plan, cs, w, walkReadyFilter(), "")
	if err != nil {
		t.Fatal(err)
	}
	if got := walkReadyRules(fs)[WalkReadyNoQuietStart]; got != 1 {
		t.Fatalf("no-quiet findings for a hot leg start: %d, want 1 (%v)", got, fs)
	}
	// The mutation moves the `to` 100 yd off: the start is quiet.
	quiet := walkReadyPlan(t, `{"version":1,"name":"t","map":0,"entrance":{"x":0,"y":0,"z":0},"level_band":[60,60],"legs":[
 {"name":"a","to":{"x":100,"y":0,"z":0},"pull_groups":[{"entries":[1],"positions":[{"x":200,"y":0,"z":0}]}]}]}`)
	plan2 := mustWalkReadyPlan(t, quiet)
	fs2, err := WalkReady(plan2, cs, w, walkReadyFilter(), "")
	if err != nil {
		t.Fatal(err)
	}
	if got := walkReadyRules(fs2)[WalkReadyNoQuietStart]; got != 0 {
		t.Fatalf("no-quiet findings after moving the start: %d, want 0", got)
	}
}
