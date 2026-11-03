package planscout

import (
	"encoding/json"
	"math"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/pjerra/turtle-agent/internal/dungeon"
	"github.com/pjerra/turtle-agent/internal/navmesh"
	"github.com/pjerra/turtle-agent/internal/planexport"
	"github.com/pjerra/turtle-agent/internal/planlint"
	"github.com/pjerra/turtle-agent/internal/worldspawn"
)

// fakeNav answers the scout's queries. Defaults: no floors, the nearest point is the point itself, every ray is clear,
// the server path is a complete straight line that ends on the goal, and the offline path is that line.
type fakeNav struct {
	floors func(x, y, lo, hi float32) ([]navmesh.Floor, error)
	near   func(p navmesh.Vec3) (navmesh.Vec3, bool, error)
	server func(from, to navmesh.Vec3) (navmesh.ServerPath, error)
	plan   func(from, to navmesh.Vec3) (navmesh.Path, error)
}

func (f fakeNav) Floors(_ uint32, x, y, lo, hi float32) ([]navmesh.Floor, error) {
	if f.floors != nil {
		return f.floors(x, y, lo, hi)
	}
	return nil, nil
}

func (f fakeNav) Nearest(_ uint32, p navmesh.Vec3) (navmesh.Vec3, bool, error) {
	if f.near != nil {
		return f.near(p)
	}
	return p, true, nil
}

func (f fakeNav) Raycast(_ uint32, from, to navmesh.Vec3) (navmesh.Ray, error) {
	return navmesh.Ray{Known: true, Clear: true}, nil
}

func straight(from, to navmesh.Vec3) navmesh.Path {
	return navmesh.Path{Status: navmesh.Complete, Points: []navmesh.Vec3{from, to}, Length: f32(dist(point3(from), point3(to)))}
}

func (f fakeNav) Plan(_ uint32, from, to navmesh.Vec3) (navmesh.Path, error) {
	if f.plan != nil {
		return f.plan(from, to)
	}
	return straight(from, to), nil
}

func (f fakeNav) ServerPath(_ uint32, from, to navmesh.Vec3) (navmesh.ServerPath, error) {
	if f.server != nil {
		return f.server(from, to)
	}
	return navmesh.ServerPath{Type: navmesh.PathNormal, Points: []navmesh.Vec3{from, to}, ActualEnd: to,
		Length: f32(dist(point3(from), point3(to)))}, nil
}

func entriesHave(e uint32, es []uint32) bool {
	for _, x := range es {
		if x == e {
			return true
		}
	}
	return false
}

func point3(v navmesh.Vec3) dungeon.Point { return dungeon.Point{X: v[0], Y: v[1], Z: v[2]} }

func f32(v float64) float32 { return float32(v) }

func dist(a, b dungeon.Point) float64 {
	dx, dy, dz := float64(a.X-b.X), float64(a.Y-b.Y), float64(a.Z-b.Z)
	return math.Sqrt(dx*dx + dy*dy + dz*dz)
}

func rowsOf(steps ...planexport.StepRow) planexport.Rows {
	for i := range steps {
		steps[i].Leg, steps[i].Seq = 0, i
	}
	return planexport.Rows{Plan: planexport.PlanRow{Plan: "t", Map: 1}, Legs: []planexport.LegRow{{Plan: "t"}}, Steps: steps}
}

func move(x, y, z float32) planexport.StepRow {
	return planexport.StepRow{Kind: planexport.KindMove, X: x, Y: y, Z: z}
}

func mkPull(x, y, z, radius float32, entries ...uint32) planexport.StepRow {
	return planexport.StepRow{Kind: planexport.KindPull, X: x, Y: y, Z: z, Radius: radius, Entries: entries}
}

func run(t *testing.T, rows planexport.Rows, nav Nav, w *worldspawn.World, origins []planexport.RowOrigin) *Report {
	t.Helper()
	rep, err := Offline(Input{Rows: rows, Origins: origins, Plan: &dungeon.Plan{Name: "t", Map: 1, Kind: "raid"}}, nav, w, Options{})
	if err != nil {
		t.Fatal(err)
	}
	return rep
}

func checkAt(t *testing.T, rep *Report, step int, name string) *Check {
	t.Helper()
	for i := range rep.Steps {
		if rep.Steps[i].Step != step {
			continue
		}
		for k := range rep.Steps[i].Checks {
			if rep.Steps[i].Checks[k].Name == name {
				return &rep.Steps[i].Checks[k]
			}
		}
		return nil
	}
	t.Fatalf("no step %d", step)
	return nil
}

// 1. The walk-31 f loop: the stand 18.3 yd above its anchor fails climb_anchor; insert_via proposes three vias.
func TestGatherLedgeF23ClimbAnchorFails(t *testing.T) {
	rows := rowsOf(
		move(-11994.4, -1510.7, 57.3),
		move(-12028, -1510, 75.6),
		func() planexport.StepRow {
			s := mkPull(-12006.8, -1486.9, 80.5, 6.3, 11351, 11831)
			s.Flags = planexport.FlagStandPull
			return s
		}(),
	)
	origins := []planexport.RowOrigin{{Leg: 0, Part: "via", Group: -1, Index: 0}, {Leg: 0, Part: "to", Group: -1, Index: 0}, {Leg: 0, Part: "pull", Group: 0, Index: 0}}
	rep := run(t, rows, fakeNav{}, nil, origins)
	c := checkAt(t, rep, 2, planlint.LintClimb)
	if c == nil || c.Verdict != VerdictFail || !strings.Contains(c.Reason, "18.3") || !strings.Contains(c.Reason, "CheckAnchor") {
		t.Fatalf("step 2 climb_anchor: %+v", c)
	}
	// Three vias, each climb <= 6 yd.
	fixes := ProposeFixes(rep, Input{Rows: rows, Origins: origins, Plan: &dungeon.Plan{Name: "t", Map: 1, Kind: "raid"}}, fakeNav{}, nil)
	var via *Fix
	for i := range fixes {
		if fixes[i].Kind == FixInsertVia {
			via = &fixes[i]
		}
	}
	if via == nil || len(via.Vias) != 3 || !via.Apply {
		t.Fatalf("insert_via fix: %+v", via)
	}
	prev := dungeon.Point{X: -11994.4, Y: -1510.7, Z: 57.3}
	for _, v := range via.Vias {
		if dz := math.Abs(float64(v.Z - prev.Z)); dz > planlint.ClimbViaDzYd+0.01 {
			t.Errorf("via %+v climbs %.1f yd", v, dz)
		}
		prev = v
	}

	// Boundary: 7.9 ok, 8.1 fail.
	ok := rowsOf(move(1, 0, 0), move(11, 0, 7.9), func() planexport.StepRow {
		s := mkPull(21, 0, 7.9, 5, 1)
		s.Flags = planexport.FlagStandPull
		return s
	}())
	if c := checkAt(t, run(t, ok, fakeNav{}, nil, nil), 2, planlint.LintClimb); c != nil && c.Verdict == VerdictFail {
		t.Fatalf("dz 7.9 fails: %+v", c)
	}
	bad := rowsOf(move(1, 0, 0), move(11, 0, 8.1), func() planexport.StepRow {
		s := mkPull(21, 0, 8.1, 5, 1)
		s.Flags = planexport.FlagStandPull
		return s
	}())
	if c := checkAt(t, run(t, bad, fakeNav{}, nil, nil), 2, planlint.LintClimb); c == nil || c.Verdict != VerdictFail {
		t.Fatalf("dz 8.1 does not fail: %+v", c)
	}
}

// 3. The walk-31 d step 650: 154 yd from the lead; 125 warn, 119 ok, 141 fail; the far drift ring point fails too.
func TestHopTooFarD650(t *testing.T) {
	origin := dungeon.Point{X: -11763.3, Y: -1249.2, Z: 112.6}
	target := dungeon.Point{X: -11632.0, Y: -1322.0, Z: 78.1}
	d := dist(origin, target)
	if d < 150 {
		t.Fatalf("the fixture is %.1f yd, want 154", d)
	}
	rep := run(t, rowsOf(move(origin.X, origin.Y, origin.Z), move(target.X, target.Y, target.Z)), fakeNav{}, nil, nil)
	c := checkAt(t, rep, 2, planlint.LintHop)
	if c == nil || c.Verdict != VerdictFail {
		t.Fatalf("154 yd: %+v", c)
	}
	// Along -x: 119 ok, 125 and 141 warn/fail.
	for _, tc := range []struct {
		d    float32
		want string
	}{{119, ""}, {125, VerdictWarn}, {141, VerdictFail}} {
		r := run(t, rowsOf(move(1, 0, 0), move(1+tc.d, 0, 0)), fakeNav{}, nil, nil)
		c := checkAt(t, r, 2, planlint.LintHop)
		if tc.want == "" {
			if c != nil {
				t.Errorf("%v yd: %+v, want no finding", tc.d, c)
			}
			continue
		}
		if c == nil || c.Verdict != tc.want {
			t.Errorf("%v yd: %+v, want %s", tc.d, c, tc.want)
		}
	}
	// Drift: the pull's far ring point is 150 yd from the next move (the row itself is 119).
	dr := rowsOf(mkPull(1, 0, 0, 30, 1), move(120, 0, 0))
	c = checkAt(t, run(t, dr, fakeNav{}, nil, nil), 2, planlint.LintHop)
	if c == nil || c.Verdict != VerdictFail || !strings.Contains(c.Reason, "drift") {
		t.Fatalf("drift: %+v", c)
	}
}

// 4. Both ends swimmable: the water lint fails with type 0x81.
func TestBothEndsSwimmableA2(t *testing.T) {
	nav := fakeNav{floors: func(x, y, lo, hi float32) ([]navmesh.Floor, error) {
		// A water floor at the point itself (±5 window), and far below the "river" query.
		if hi-lo <= 11 {
			return []navmesh.Floor{{Z: lo + 5, Flags: navmesh.NavWater}}, nil
		}
		return []navmesh.Floor{{Z: lo + 1, Flags: navmesh.NavWater}}, nil
	}}
	rep := run(t, rowsOf(move(-11945.7, -1358.2, 64.6), move(-11951.0, -1393.5, 68.1)), nav, nil, nil)
	var c *Check
	for i := range rep.Steps[1].Checks {
		if rep.Steps[1].Checks[i].Name == planlint.LintWater && rep.Steps[1].Checks[i].Verdict == VerdictFail {
			c = &rep.Steps[1].Checks[i]
		}
	}
	if c == nil || !strings.Contains(c.Reason, "0x81") {
		t.Fatalf("water: %+v", rep.Steps[1].Checks)
	}
}

// 5. A river 13 yd below the path warns.
func TestRiverBelowWarns(t *testing.T) {
	nav := fakeNav{floors: func(x, y, lo, hi float32) ([]navmesh.Floor, error) {
		if hi <= -6 {
			return []navmesh.Floor{{Z: -13, Flags: navmesh.NavWater}}, nil
		}
		return nil, nil
	}}
	rep := run(t, rowsOf(move(1, 0, 0), move(61, 0, 0)), nav, nil, nil)
	c := checkAt(t, rep, 2, planlint.LintWater)
	if c == nil || c.Verdict != VerdictWarn || !strings.Contains(c.Reason, "river below") {
		t.Fatalf("river: %+v", c)
	}
}

// 6. The walk-31 f step 16: no floor within 5 yd fails the floor lint; snap_point proposes the nearest floor.
func TestRefusedPointNoFloorF16(t *testing.T) {
	p := dungeon.Point{X: -11931.9, Y: -1491.8, Z: 41.5}
	far := navmesh.Vec3{p.X + 8.8, p.Y, p.Z}
	nav := fakeNav{near: func(q navmesh.Vec3) (navmesh.Vec3, bool, error) { return far, true, nil }}
	rows := rowsOf(move(-11918.1, -1443.7, 42.4), move(p.X, p.Y, p.Z))
	rep := run(t, rows, nav, nil, []planexport.RowOrigin{{Leg: 0, Part: "via", Group: -1, Index: 0}, {Leg: 0, Part: "cut", Group: -1, Index: 1}})
	c := checkAt(t, rep, 2, planlint.LintOffFloor)
	if c == nil || c.Verdict != VerdictFail {
		t.Fatalf("floor: %+v", c)
	}
	fixes := ProposeFixes(rep, Input{Rows: rows, Origins: []planexport.RowOrigin{{Leg: 0, Part: "via", Group: -1, Index: 0}, {Leg: 0, Part: "cut", Group: -1, Index: 1}}, Plan: &dungeon.Plan{Name: "t", Map: 1}}, nav, nil)
	var snap *Fix
	for i := range fixes {
		if fixes[i].Kind == FixSnapPoint {
			snap = &fixes[i]
		}
	}
	if snap == nil || snap.Point == nil || !snap.Apply {
		t.Fatalf("snap_point: %+v", snap)
	}
}

// 7. Reach verdicts: NOPATH, unreachable and off-mesh (5.1 yd) fail; 4.9 yd is ok.
func TestRefusedPointsVerdicts(t *testing.T) {
	for _, tc := range []struct {
		name  string
		end   float32
		typ   uint32
		fails bool
	}{
		{"nopath", 0, navmesh.PathNoPath, true},
		{"unreachable", 0, navmesh.PathIncomplete, true}, // ends where it started: no progress
		{"off-mesh 5.1", 5.1, navmesh.PathNormal, true},
		{"off-mesh 4.9", 4.9, navmesh.PathNormal, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			nav := fakeNav{server: func(from, to navmesh.Vec3) (navmesh.ServerPath, error) {
				end := to
				if tc.typ == navmesh.PathIncomplete {
					end = from
				} else if tc.end != 0 {
					end = navmesh.Vec3{to[0] - tc.end, to[1], to[2]}
				}
				return navmesh.ServerPath{Type: tc.typ, Points: []navmesh.Vec3{from, end}, ActualEnd: end,
					Length: f32(dist(point3(from), point3(end)))}, nil
			}}
			rep := run(t, rowsOf(move(1, 0, 0), move(51, 0, 0)), nav, nil, nil)
			c := checkAt(t, rep, 2, planlint.LintReach)
			if tc.fails && (c == nil || c.Verdict != VerdictFail) {
				t.Fatalf("want fail: %+v", c)
			}
			if !tc.fails && c != nil && c.Verdict == VerdictFail {
				t.Fatalf("want ok: %+v", c)
			}
		})
	}
}

// 8. Arrival radii: 2.9 ok, 5.9 warn, 6.1 fail.
func TestArrivalRadii(t *testing.T) {
	for _, tc := range []struct {
		end  float32
		want string
	}{{2.9, ""}, {5.9, VerdictWarn}, {6.1, VerdictFail}} {
		nav := fakeNav{server: func(from, to navmesh.Vec3) (navmesh.ServerPath, error) {
			end := navmesh.Vec3{to[0] - tc.end, to[1], to[2]}
			return navmesh.ServerPath{Type: navmesh.PathNormal, Points: []navmesh.Vec3{from, end}, ActualEnd: end,
				Length: f32(dist(point3(from), point3(end)))}, nil
		}}
		rep := run(t, rowsOf(move(1, 0, 0), move(51, 0, 0)), nav, nil, nil)
		c := checkAt(t, rep, 2, planlint.LintArrival)
		if tc.want == "" {
			if c != nil {
				t.Errorf("%v yd: %+v", tc.end, c)
			}
			continue
		}
		if c == nil || c.Verdict != tc.want {
			t.Errorf("%v yd: %+v, want %s", tc.end, c, tc.want)
		}
	}
}

// worldOf builds a world export from spawns, with no groups or links (each spawn its own pack).
func worldOf(spawns ...worldspawn.Spawn) *worldspawn.World {
	return &worldspawn.World{Map: 1, Spawns: spawns}
}

func spawn(guid uint64, entry uint32, x, y, z float32) worldspawn.Spawn {
	return worldspawn.Spawn{GUID: guid, Entry: entry, LevelMax: 60, Faction: 14, Pos: dungeon.Point{X: x, Y: y, Z: z}, DetectionYd: 20}
}

// 10. Joiner boundaries: 0 ok, 2 warn, 3 fail; a row sized at 4 bodies with 5 fails.
func TestJoinerBoundaries(t *testing.T) {
	for _, tc := range []struct {
		joiners int
		item    uint32
		want    string
	}{
		{0, 0, ""}, {2, 0, VerdictWarn}, {3, 0, VerdictFail}, {4, 4, VerdictFail},
	} {
		spawns := []worldspawn.Spawn{spawn(1, 100, 5, 0, 0)}
		for i := 0; i < tc.joiners; i++ {
			spawns = append(spawns, spawn(uint64(10+i), uint32(200+i), 10+float32(i), 0, 0))
		}
		row := mkPull(1, 0, 0, 20, 100)
		row.Item = tc.item
		rep := run(t, rowsOf(row), fakeNav{}, worldOf(spawns...), nil)
		c := checkAt(t, rep, 1, planlint.LintJoiners)
		if tc.want == "" {
			if c != nil {
				t.Errorf("%d joiners: %+v", tc.joiners, c)
			}
			continue
		}
		if c == nil || c.Verdict != tc.want {
			t.Errorf("%d joiners: %+v, want %s", tc.joiners, c, tc.want)
		}
	}
}

// 11. A pack an earlier pull owns is dead; the same pack owned by a later row wakes.
func TestStandAggroIgnoresDeadPacks(t *testing.T) {
	// A is at 45: pull 1 at 40 owns it (radius 20); the stand at 55 is inside its aggro (20+8).
	w := worldOf(spawn(7, 100, 45, 0, 0))
	dead := func() planexport.StepRow {
		s := mkPull(70, 0, 0, 20, 200)
		s.Flags = planexport.FlagStandPull
		return s
	}()
	rows := rowsOf(mkPull(40, 0, 0, 20, 100), move(55, 0, 0), dead)
	rep := run(t, rows, fakeNav{}, w, nil)
	if c := checkAt(t, rep, 2, planlint.LintAggro); c != nil {
		t.Fatalf("an earlier pull's pack wakes: %+v", c)
	}
	// The same spawn owned by a later row (a pull behind the stand).
	later := mkPull(45, 0, 0, 20, 100)
	rows = rowsOf(move(55, 0, 0), dead, later)
	rows.Steps[1].Kind = planexport.KindPull
	rep = run(t, rows, fakeNav{}, w, nil)
	if c := checkAt(t, rep, 1, planlint.LintAggro); c == nil || c.Verdict != VerdictFail {
		t.Fatalf("a later pull's pack does not wake: %+v", c)
	}
}

// 13. The constants mirror the run leader's.
func TestConstantsMirrorTheRunLeader(t *testing.T) {
	for _, c := range []struct {
		name string
		got  float64
		want float64
	}{
		{"ReactDistanceYd", float64(ReactDistanceYd), 150},
		{"TooFarLegYd", float64(TooFarLegYd), 120},
		{"DriftMarginYd", float64(DriftMarginYd), 10},
		{"ArriveYd", float64(ArriveYd), 3},
		{"StalledArriveYd", float64(StalledArriveYd), 6},
		{"ExactArriveYd", float64(ExactArriveYd), 1.5},
		{"StragglerDzYd", float64(StragglerDzYd), 8},
		{"ClimbViaDzYd", float64(ClimbViaDzYd), 6},
		{"StandZTolYd", float64(StandZTolYd), 4.5},
		{"SeeYd", float64(SeeYd), 60},
		{"FightDriftYd", float64(FightDriftYd), 30},
		{"DetourMaxYd", float64(DetourMaxYd), 15},
		{"StandMinYd", float64(StandMinYd), 15},
		{"StandHostileYd", float64(StandHostileYd), 20},
		{"StandPullMarginYd", float64(StandPullMarginYd), 5},
		{"MaxMoveEndOffset", float64(MaxMoveEndOffset), 5},
		{"JoinerWarnBodies", float64(JoinerWarnBodies), 1},
		{"JoinerFailBodies", float64(JoinerFailBodies), 3},
	} {
		if c.got != c.want {
			t.Errorf("%s = %v, want %v", c.name, c.got, c.want)
		}
	}
}

// 16. The fix file round-trips and ApplyFixes inserts the highest index first.
func TestFixFileRoundTrip(t *testing.T) {
	ff := FixFile{Format: FixFormat, Version: FixVersion, Plan: "t", Map: 1, RowsSHA256: "x", Scout: "scout-t.json",
		Fixes: []Fix{{ID: 1, Step: 22, Check: planlint.LintClimb, Kind: FixInsertVia, Apply: true,
			Ref:  &planexport.RowOrigin{Leg: 0, Part: "approach", Group: 0, Index: 0},
			Vias: []dungeon.Point{{X: 1, Y: 2, Z: 3}}, Reason: "r"}}}
	b, err := json.Marshal(ff)
	if err != nil {
		t.Fatal(err)
	}
	var back FixFile
	if err := json.Unmarshal(b, &back); err != nil {
		t.Fatal(err)
	}
	if back.Format != ff.Format || len(back.Fixes) != 1 || back.Fixes[0].Vias[0].Z != 3 || back.Fixes[0].Ref.Group != 0 {
		t.Fatalf("round trip: %+v", back)
	}
}

func TestApplyInsertsHighIndexFirst(t *testing.T) {
	p := &dungeon.Plan{Version: 1, Name: "t", Map: 1, Entrance: dungeon.Point{}, Legs: []dungeon.Leg{{
		Name: "one",
		Via:  []dungeon.Waypoint{{Point: dungeon.Point{X: 1, Y: 0, Z: 0}}, {Point: dungeon.Point{X: 2, Y: 0, Z: 0}}},
		To:   dungeon.Point{X: 10, Y: 0, Z: 0},
		PullGroups: []dungeon.PullGroup{{Entries: []uint32{1}, Positions: []dungeon.Point{{X: 12, Y: 0, Z: 0}},
			Approach: []dungeon.Point{{X: 10, Y: 0, Z: 0}}}},
	}}}
	fixes := []Fix{
		{ID: 1, Step: 1, Kind: FixInsertVia, Apply: true, Ref: &planexport.RowOrigin{Leg: 0, Part: "via", Group: -1, Index: 0},
			Vias: []dungeon.Point{{X: 0.5, Y: 0, Z: 0}}},
		{ID: 2, Step: 3, Kind: FixInsertVia, Apply: true, Ref: &planexport.RowOrigin{Leg: 0, Part: "via", Group: -1, Index: 1},
			Vias: []dungeon.Point{{X: 1.5, Y: 0, Z: 0}}},
	}
	edited, res, err := ApplyFixes(p, fixes, fakePlanNet{})
	if err != nil {
		t.Fatal(err)
	}
	if !res.Checked {
		t.Fatalf("check: %+v", res)
	}
	got := []float32{}
	for _, v := range edited.Legs[0].Via {
		got = append(got, v.X)
	}
	want := []float32{0.5, 1, 1.5, 2}
	if len(got) != len(want) {
		t.Fatalf("vias %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("vias %v, want %v", got, want)
		}
	}
	if len(p.Legs[0].Via) != 2 {
		t.Fatalf("the base plan changed: %d vias", len(p.Legs[0].Via))
	}
}

// fakePlanNet is a navmesh for ApplyFixes: every point is on the floor and every path completes.
type fakePlanNet struct{}

func (fakePlanNet) Plan(_ uint32, from, to navmesh.Vec3) (navmesh.Path, error) {
	return straight(from, to), nil
}
func (fakePlanNet) Raycast(_ uint32, from, to navmesh.Vec3) (navmesh.Ray, error) {
	return navmesh.Ray{Known: true, Clear: true}, nil
}
func (fakePlanNet) Nearest(_ uint32, p navmesh.Vec3) (navmesh.Vec3, bool, error) { return p, true, nil }

// 9. The walk-31 Arlokk pull: the real rows and world export give joiner bodies outside pull group 12, and the split_pull
// fix lists them with apply:false.
func TestArlokk366Joiners(t *testing.T) {
	b, err := os.ReadFile(filepath.Join("testdata", "zg-20261103050000.sql"))
	if err != nil {
		t.Fatal(err)
	}
	all, _, err := planexport.ParseMigration(string(b))
	if err != nil {
		t.Fatal(err)
	}
	if len(all) != 1 || all[0].Plan.Plan != "zul_gurub" {
		t.Fatalf("%d plans", len(all))
	}
	rows := all[0]
	w, _, err := worldspawn.Load(filepath.Join("..", "..", "tools", "dungeongen", "testdata", "map309"), 309)
	if err != nil {
		t.Fatal(err)
	}
	plan := &dungeon.Plan{Version: 1, Name: "zul_gurub", Kind: "raid", Map: 309, LevelBand: []int{58, 60}}
	rep, err := Offline(Input{Rows: rows, Plan: plan}, fakeNav{}, w, Options{PartyLevel: 60})
	if err != nil {
		t.Fatal(err)
	}
	c := checkAt(t, rep, 366, planlint.LintJoiners)
	if c == nil || c.Verdict != VerdictFail || c.Bodies < 3 {
		t.Fatalf("step 366 joiners: %+v", c)
	}
	t.Logf("step 366: %s", c.Reason)
	fixes := ProposeFixes(rep, Input{Rows: rows, Plan: plan}, fakeNav{}, w)
	var split *Fix
	for i := range fixes {
		if fixes[i].Step == 366 && fixes[i].Kind == FixSplitPull {
			split = &fixes[i]
		}
	}
	if split == nil || split.Apply || len(split.Joiners) == 0 || len(split.PullFirst) != len(split.Joiners) {
		t.Fatalf("split_pull: %+v", split)
	}
	for _, p := range split.Joiners {
		t.Logf("joiner pack %s: %d bodies %v", p.Pack, p.Bodies, p.Guids)
	}
	if split.Stand == nil {
		t.Fatalf("split_pull without a stand")
	}
	// The stand is clear of every joiner unit: farther than reach + 10 + 5.
	for _, sp := range w.Spawns {
		if sp.LevelMax <= 1 || !sp.Hostile() {
			continue
		}
		if entriesHave(sp.Entry, rows.Steps[365].Entries) {
			continue
		}
		reach := float64(worldspawn.AggroRadius(sp, 60, 1))
		if reach == 0 {
			continue
		}
		at := dungeon.Point{X: split.Stand.X, Y: split.Stand.Y, Z: split.Stand.Z}
		if d := dist(at, sp.Pos); d <= reach+10+StandPullMarginYd {
			t.Errorf("stand %.1f %.1f %.1f is %.1f yd from %d (reach %.1f)", at.X, at.Y, at.Z, d, sp.Entry, reach)
		}
	}
}
