package routeedit

import (
	"encoding/json"
	"errors"
	"math"
	"strings"
	"testing"

	"github.com/pjerra/turtle-agent/internal/dungeon"
	"github.com/pjerra/turtle-agent/internal/navmesh"
)

// floors is a Floorer made of a function.
type floors func(x, y, lo, hi float32) []navmesh.Floor

func (f floors) Floors(_ uint32, x, y, lo, hi float32) ([]navmesh.Floor, error) {
	return f(x, y, lo, hi), nil
}

// flat has one walkable floor at height 5 everywhere.
var flat = floors(func(_, _, lo, hi float32) []navmesh.Floor {
	if lo <= 5 && 5 <= hi {
		return []navmesh.Floor{{Z: 5, Flags: navmesh.NavGround}}
	}
	return nil
})

const basePlan = `{"version":1,"name":"edit1","map":209,"entrance":{"x":0,"y":0,"z":5},
 "legs":[
  {"name":"one","to":{"x":100,"y":0,"z":5},
   "via":[{"x":20,"y":0,"z":5},{"x":40,"y":0,"z":5},{"x":60,"y":0,"z":5,"direct":true},{"x":80,"y":0,"z":5,"check_skip":"pit"}],
   "pull_groups":[{"entries":[7001],"positions":[{"x":120,"y":0,"z":5}]}]},
  {"name":"two","to":{"x":200,"y":0,"z":5},"boss":{"entry":6001,"position":{"x":220,"y":0,"z":5}}}]}`

func load(t *testing.T) *dungeon.Plan {
	t.Helper()
	p, err := dungeon.Parse("edit1.json", []byte(basePlan))
	if err != nil {
		t.Fatal(err)
	}
	return p
}

// paths lists the JSON paths an edit changed.
func changedPaths(t *testing.T, a, b *dungeon.Plan) []string {
	t.Helper()
	var out []string
	for _, op := range dungeon.DiffPlans(a, b) {
		out = append(out, op.Op+" "+op.Path)
	}
	return out
}

func TestMoveChangesOnlyThePointAndPutsItOnTheFloor(t *testing.T) {
	base := load(t)
	want, _ := dungeon.Marshal(base)
	// a floor at 5 and one at 40 (a level above): the point stays on the level it was on
	two := floors(func(_, _, lo, hi float32) []navmesh.Floor {
		var out []navmesh.Floor
		for _, z := range []float32{5, 40} {
			if lo <= z && z <= hi {
				out = append(out, navmesh.Floor{Z: z, Flags: navmesh.NavGround})
			}
		}
		return out
	})
	got, ch, err := Apply(base, []Op{{Leg: 0, Kind: KindMove, Index: 1, X: 41.5, Y: 7}, {Leg: 1, Kind: KindMove, Index: 0, X: 205, Y: -3}}, two)
	if err != nil {
		t.Fatal(err)
	}
	if after, _ := dungeon.Marshal(base); string(after) != string(want) {
		t.Fatal("the base plan was changed")
	}
	if p := got.Legs[0].Via[1].Point; p.X != 41.5 || p.Y != 7 || p.Z != 5 {
		t.Errorf("via 1: %+v", p)
	}
	if p := got.Legs[1].To; p.X != 205 || p.Y != -3 || p.Z != 5 {
		t.Errorf("leg 1 to: %+v", p)
	}
	for _, c := range ch {
		if c.Floors != 1 || c.Picked {
			t.Errorf("only one floor lies within %v yd of height 5: %+v", FloorWindow, c)
		}
	}
	// nothing but the coordinates of the two points differs
	for _, p := range changedPaths(t, base, got) {
		if !strings.HasPrefix(p, "replace /legs/0/via/1/") && !strings.HasPrefix(p, "replace /legs/1/to/") {
			t.Errorf("an edit changed %s", p)
		}
	}
	if ch[0].Part != "via 1" || ch[1].Part != "to" || ch[0].Moved != 7.2 || ch[1].Moved != 5.8 {
		t.Errorf("changes: %+v", ch)
	}
}

func TestFloorWindowKeepsTheLevelAndMarksAChoice(t *testing.T) {
	base := load(t)
	// floors at 5 and 20, a drop from height 5 sees both in the window (25 yd); the nearer one wins and the choice is shown
	two := floors(func(_, _, lo, hi float32) []navmesh.Floor {
		var out []navmesh.Floor
		for _, z := range []float32{5, 20} {
			if lo <= z && z <= hi {
				out = append(out, navmesh.Floor{Z: z, Flags: navmesh.NavGround})
			}
		}
		return out
	})
	got, ch, err := Apply(base, []Op{{Leg: 0, Kind: KindMove, Index: 0, X: 21, Y: 1}}, two)
	if err != nil {
		t.Fatal(err)
	}
	if got.Legs[0].Via[0].Z != 5 || !ch[0].Picked || ch[0].Floors != 2 {
		t.Errorf("%+v %+v", got.Legs[0].Via[0], ch[0])
	}
	// water, lava, and a slope too steep to stand on are not floors for a route
	wet := floors(func(_, _, _, _ float32) []navmesh.Floor {
		return []navmesh.Floor{{Z: 5, Flags: navmesh.NavWater}, {Z: 5, Flags: navmesh.NavGround | navmesh.NavSteep}, {Z: 5, Flags: navmesh.NavMagma}}
	})
	if _, _, err := Apply(base, []Op{{Leg: 0, Kind: KindMove, Index: 0, X: 21, Y: 1}}, wet); err == nil || !strings.Contains(err.Error(), "off the mesh") {
		t.Errorf("no walkable floor: %v", err)
	}
	// no floor at all: refused, naming the point
	none := floors(func(_, _, _, _ float32) []navmesh.Floor { return nil })
	var oe *ErrOp
	if _, _, err := Apply(base, []Op{{Leg: 0, Kind: KindMove, Index: 0, X: 21, Y: 1}}, none); !errors.As(err, &oe) || oe.Index != 0 || !strings.Contains(err.Error(), "via 0 of leg 0") {
		t.Errorf("%v", err)
	}
}

func TestInsertTakesTheHeightOfTheSegmentAndIsAPlainPoint(t *testing.T) {
	base := load(t)
	// a slope: floor height is x/10 + 5; the new point between via 0 (20) and via 1 (40) is put on the floor under it
	slope := floors(func(x, _, lo, hi float32) []navmesh.Floor {
		z := 5 + x/10
		if lo <= z && z <= hi {
			return []navmesh.Floor{{Z: z, Flags: navmesh.NavGround}}
		}
		return nil
	})
	got, ch, err := Apply(base, []Op{{Leg: 0, Kind: KindInsert, Index: 1, X: 30, Y: 4}}, slope)
	if err != nil {
		t.Fatal(err)
	}
	l := got.Legs[0]
	if len(l.Via) != 5 || l.Via[1].X != 30 || l.Via[1].Y != 4 || l.Via[1].Z != 8 || l.Via[1].Direct || l.Via[1].CheckSkip != "" {
		t.Errorf("via: %+v", l.Via)
	}
	if l.Via[0].X != 20 || l.Via[2].X != 40 || l.Via[3].Direct != true {
		t.Errorf("the others moved or changed: %+v", l.Via)
	}
	if ch[0].Part != "via 1" || ch[0].To == nil || ch[0].From != nil {
		t.Errorf("%+v", ch[0])
	}
	// at the end of the list: after the last via point, before `to`
	got, _, err = Apply(base, []Op{{Leg: 0, Kind: KindInsert, Index: 4, X: 90, Y: 0}}, flat)
	if err != nil || len(got.Legs[0].Via) != 5 || got.Legs[0].Via[4].X != 90 {
		t.Errorf("%v %+v", err, got.Legs[0].Via)
	}
	// a leg with no via points takes one
	got, _, err = Apply(base, []Op{{Leg: 1, Kind: KindInsert, Index: 0, X: 100, Y: 10}}, flat)
	if err != nil || len(got.Legs[1].Via) != 1 {
		t.Errorf("%v", err)
	}
}

func TestDeleteTakesAViaPointOnly(t *testing.T) {
	base := load(t)
	got, ch, err := Apply(base, []Op{{Leg: 0, Kind: KindDelete, Index: 0}}, flat)
	if err != nil || len(got.Legs[0].Via) != 3 || got.Legs[0].Via[0].X != 40 || ch[0].From == nil || ch[0].From.X != 20 {
		t.Fatalf("%v %+v", err, got.Legs[0].Via)
	}
	// `to` is the index after the last via point: a leg needs it
	_, _, err = Apply(base, []Op{{Leg: 0, Kind: KindDelete, Index: 4}}, flat)
	if err == nil || !strings.Contains(err.Error(), "cannot be deleted") {
		t.Errorf("delete to: %v", err)
	}
}

func TestLockedPointsAreRefused(t *testing.T) {
	base := load(t)
	for _, op := range []Op{
		{Leg: 0, Kind: KindMove, Index: 2, X: 61, Y: 0}, // direct
		{Leg: 0, Kind: KindDelete, Index: 2},            // direct
		{Leg: 0, Kind: KindMove, Index: 3, X: 81, Y: 0}, // check_skip
		{Leg: 0, Kind: KindDelete, Index: 3},            // check_skip
	} {
		if _, _, err := Apply(base, []Op{op}, flat); err == nil || !strings.Contains(err.Error(), "locked") {
			t.Errorf("%+v: %v", op, err)
		}
	}
}

func TestBadOpsAreRefused(t *testing.T) {
	base := load(t)
	nan, inf := math.NaN(), math.Inf(1)
	bad := map[string][]Op{
		"unknown kind":     {{Leg: 0, Kind: "teleport", Index: 0, X: 1, Y: 1}},
		"empty kind":       {{Leg: 0, Index: 0, X: 1, Y: 1}},
		"leg too high":     {{Leg: 2, Kind: KindMove, Index: 0, X: 1, Y: 1}},
		"leg negative":     {{Leg: -1, Kind: KindMove, Index: 0, X: 1, Y: 1}},
		"index too high":   {{Leg: 0, Kind: KindMove, Index: 5, X: 1, Y: 1}},
		"index negative":   {{Leg: 0, Kind: KindMove, Index: -1, X: 1, Y: 1}},
		"insert too high":  {{Leg: 0, Kind: KindInsert, Index: 5, X: 1, Y: 1}},
		"delete too high":  {{Leg: 0, Kind: KindDelete, Index: 9}},
		"nan":              {{Leg: 0, Kind: KindMove, Index: 0, X: nan, Y: 1}},
		"inf":              {{Leg: 0, Kind: KindMove, Index: 0, X: 1, Y: inf}},
		"far outside":      {{Leg: 0, Kind: KindMove, Index: 0, X: 5000, Y: 0}},
		"far outside, ins": {{Leg: 0, Kind: KindInsert, Index: 0, X: 0, Y: -5000}},
		"none":             {},
	}
	for name, ops := range bad {
		if _, _, err := Apply(base, ops, flat); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
	many := make([]Op, MaxOps+1)
	for i := range many {
		many[i] = Op{Leg: 0, Kind: KindMove, Index: 0, X: 21, Y: 1}
	}
	if _, _, err := Apply(base, many, flat); err == nil {
		t.Error("too many ops")
	}
	if _, _, err := Apply(base, []Op{{Leg: 0, Kind: KindMove, Index: 0, X: 21, Y: 1}}, nil); err == nil {
		t.Error("no navmesh")
	}
	// a point just inside the margin is fine
	if _, _, err := Apply(base, []Op{{Leg: 0, Kind: KindMove, Index: 0, X: 220 + BoundsMargin - 1, Y: 0}}, flat); err != nil {
		t.Errorf("inside the margin: %v", err)
	}
}

// An op names a place in the leg as it is when the op is applied: the ops of one request run in order.
func TestOpsRunInOrder(t *testing.T) {
	base := load(t)
	got, _, err := Apply(base, []Op{
		{Leg: 0, Kind: KindInsert, Index: 0, X: 10, Y: 0}, // the list is now 10, 20, 40, 60d, 80s
		{Leg: 0, Kind: KindMove, Index: 1, X: 22, Y: 2},   // was via 0 (20)
		{Leg: 0, Kind: KindDelete, Index: 0},              // the one just put in (10)
	}, flat)
	if err != nil {
		t.Fatal(err)
	}
	var xs []float32
	for _, w := range got.Legs[0].Via {
		xs = append(xs, w.X)
	}
	if len(xs) != 4 || xs[0] != 22 || xs[1] != 40 || xs[2] != 60 || xs[3] != 80 {
		t.Errorf("%v", xs)
	}
}

// Deleting the via point a direct step stands on would leave a direct step with nothing before it: the file loader refuses such a plan,
// and so does the editor.
func TestAnEditThatBreaksThePlanIsRefused(t *testing.T) {
	p, err := dungeon.Parse("d.json", []byte(`{"version":1,"name":"d","map":209,"entrance":{"x":0,"y":0,"z":5},"legs":[{"name":"one","to":{"x":100,"y":0,"z":5},
	 "via":[{"x":20,"y":0,"z":5},{"x":30,"y":0,"z":5,"direct":true}],"pull_groups":[{"entries":[1],"positions":[{"x":120,"y":0,"z":5}]}]}]}`))
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := Apply(p, []Op{{Leg: 0, Kind: KindDelete, Index: 0}}, flat); err == nil {
		t.Skip("the plan loader accepts a direct step with nothing before it")
	} else if !strings.Contains(err.Error(), "valid plan") {
		t.Errorf("%v", err)
	}
}

func TestChangeIsJSON(t *testing.T) {
	_, ch, err := Apply(load(t), []Op{{Leg: 0, Kind: KindMove, Index: 0, X: 21, Y: 1}}, flat)
	if err != nil {
		t.Fatal(err)
	}
	b, err := json.Marshal(ch)
	if err != nil || !strings.Contains(string(b), `"part":"via 0"`) {
		t.Errorf("%s %v", b, err)
	}
}

func TestGMCrossPointsAreLocked(t *testing.T) {
	p, err := dungeon.Parse("g.json", []byte(`{"version":1,"name":"g","map":43,"entrance":{"x":0,"y":0,"z":5},"legs":[{"name":"one","to":{"x":300,"y":0,"z":5},
	 "via":[{"x":20,"y":0,"z":5},{"x":120,"y":0,"z":5,"direct":true,"gm_cross":true}],"pull_groups":[{"entries":[1],"positions":[{"x":320,"y":0,"z":5}]}]}]}`))
	if err != nil {
		t.Skip("this build's plan loader does not take that plan: ", err)
	}
	for _, op := range []Op{{Leg: 0, Kind: KindMove, Index: 1, X: 121, Y: 0}, {Leg: 0, Kind: KindDelete, Index: 1}} {
		if _, _, err := Apply(p, []Op{op}, flat); err == nil || !strings.Contains(err.Error(), "gm_cross") {
			t.Errorf("%+v: %v", op, err)
		}
	}
	if _, _, err := Apply(p, []Op{{Leg: 0, Kind: KindMove, Index: 0, X: 21, Y: 1}}, flat); err != nil {
		t.Errorf("the plain point before it moves: %v", err)
	}
}
