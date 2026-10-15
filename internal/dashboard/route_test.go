package dashboard

import (
	"math"
	"strings"
	"testing"
	"time"

	"github.com/pjerra/turtle-agent/internal/dungeon"
)

func TestLegStopsOrder(t *testing.T) {
	g := BuildGeometry(tinyPlan(t))
	stops := g.Legs[0].Stops
	var kinds []string
	for _, s := range stops {
		kinds = append(kinds, s.Kind)
	}
	// start, the two via points, the stage point (`to`), the first position of each pull group, the use_object step, the boss: the order the
	// driver does it (plan.go Leg, plancheck.go checkLeg), not the order of the fields
	if got, want := strings.Join(kinds, ","), "start,via,via,stage,pull,pull,step,boss"; got != want {
		t.Fatalf("stops %s, want %s", got, want)
	}
	for i, s := range stops {
		if s.N != i {
			t.Errorf("stop %d is numbered %d", i, s.N)
		}
	}
	if !stops[2].Direct || stops[1].Direct {
		t.Error("the second via point is the direct step")
	}
	if stops[4].Group != 0 || stops[5].Group != 1 || !strings.HasPrefix(stops[4].Label, "pack 1:") || stops[6].Label != "use 555" {
		t.Errorf("pull and step stops %+v %+v %+v", stops[4], stops[5], stops[6])
	}
	if g.Legs[0].Label != "One" || g.Legs[1].Label == "" {
		t.Errorf("leg labels %q %q", g.Legs[0].Label, g.Legs[1].Label)
	}
	// the second leg starts where the first ended: at its boss
	if a, b := stops[len(stops)-1], g.Legs[1].Stops[0]; a.X != b.X || a.Y != b.Y {
		t.Errorf("leg 1 starts at %v,%v, leg 0 ended at %v,%v", b.X, b.Y, a.X, a.Y)
	}
	// the drawn line is the start, the via points and the stage point: the packs, steps and boss are marks off it
	path := g.Legs[0].Path
	if len(path) != 4 {
		t.Errorf("path of %d points, want start, 2 via, stage", len(path))
	}
	if !strings.Contains(g.SVG, "step 4 of 7") {
		t.Errorf("the hover text names the step: %s", g.SVG)
	}
	if legLabel("sentinel_room", 2) != "Sentinel room" || legLabel("", 2) != "Section 3" {
		t.Error("legLabel")
	}
}

func TestTrailCap(t *testing.T) {
	var tr Trail
	// a long walk in a zigzag, far past the cap
	for i := 0; i < 6000; i++ {
		x := float64(i) * 5
		y := 0.0
		if i%2 == 1 {
			y = 40
		}
		tr.Add("r1", x, y)
	}
	pts := tr.Points()
	if len(pts) > TrailCap || len(pts) < TrailCap/2 {
		t.Fatalf("trail holds %d points, cap %d", len(pts), TrailCap)
	}
	if pts[0] != [2]float64{0, 0} {
		t.Errorf("the start of the run stays: %v", pts[0])
	}
	if last := pts[len(pts)-1]; last[0] != 5*5999 {
		t.Errorf("the newest point is last: %v", last)
	}
	for i := 1; i < len(pts); i++ {
		if pts[i][0] <= pts[i-1][0] {
			t.Fatalf("points are in walking order, %v then %v", pts[i-1], pts[i])
		}
	}
	// a new run empties it
	tr.Add("r2", 1, 1)
	if len(tr.Points()) != 1 {
		t.Errorf("a new run starts a new trail, got %d", len(tr.Points()))
	}
}

// Each point keeps the second the lead was last seen there, in step with the points, through merging, thinning and a new run.
func TestTrailTimes(t *testing.T) {
	var tr Trail
	t0 := time.Unix(1_800_000_000, 0)
	tr.AddAt("r", 0, 0, t0)
	tr.AddAt("r", 1, 0, t0.Add(time.Second)) // closer than the step: ignored
	tr.AddAt("r", 10, 0, t0.Add(5*time.Second))
	tr.AddAt("r", 20, 0, t0.Add(9*time.Second)) // on the line through its neighbours: merged, newer time
	tr.AddAt("r", 20, 30, t0.Add(20*time.Second))
	pts, ts := tr.Points(), tr.Times()
	if len(pts) != len(ts) || len(pts) != 3 {
		t.Fatalf("points %v times %v", pts, ts)
	}
	if ts[0] != t0.Unix() || ts[1] != t0.Add(9*time.Second).Unix() || ts[2] != t0.Add(20*time.Second).Unix() || pts[1] != [2]float64{20, 0} {
		t.Fatalf("points %v times %v", pts, ts)
	}
	for i := 0; i < 6000; i++ {
		y := 0.0
		if i%2 == 1 {
			y = 40
		}
		tr.AddAt("big", float64(i)*5, y, t0.Add(time.Duration(i)*time.Second))
	}
	pts, ts = tr.Points(), tr.Times()
	if len(pts) != len(ts) || len(pts) > TrailCap {
		t.Fatalf("after thinning: %d points, %d times", len(pts), len(ts))
	}
	for i := 1; i < len(ts); i++ {
		if ts[i] <= ts[i-1] {
			t.Fatalf("times run forward: %d then %d", ts[i-1], ts[i])
		}
	}
	tr.AddAt("next", 1, 1, t0)
	if len(tr.Times()) != 1 {
		t.Errorf("a new run starts new times: %v", tr.Times())
	}
	var empty Trail
	if empty.Times() != nil {
		t.Error("no points, no times")
	}
}

func TestTrailSimplifies(t *testing.T) {
	var tr Trail
	// standing still adds nothing; a straight walk is one segment; a turn keeps its corner
	for i := 0; i < 5; i++ {
		tr.Add("r", 10, 10)
	}
	if len(tr.Points()) != 1 {
		t.Errorf("standing still: %d points", len(tr.Points()))
	}
	for i := 1; i <= 40; i++ {
		tr.Add("r", 10+float64(i)*4, 10)
	}
	if n := len(tr.Points()); n != 2 {
		t.Errorf("a straight walk is %d points, want 2", n)
	}
	for i := 1; i <= 20; i++ {
		tr.Add("r", 170, 10+float64(i)*4)
	}
	pts := tr.Points()
	if len(pts) != 3 || math.Abs(pts[1][0]-170) > 4.1 {
		t.Errorf("a turn keeps its corner: %v", pts)
	}
}

func TestLeadPosition(t *testing.T) {
	c := Card{Bots: []BotView{{Name: "a", Known: true, X: 1, Y: 2}, {Name: "b", Lead: true, Known: true, X: 5, Y: 6}}}
	if x, y, ok := c.LeadPosition(); !ok || x != 5 || y != 6 {
		t.Errorf("lead %v %v %v", x, y, ok)
	}
	c = Card{Commander: &Dot{X: 7, Y: 8}}
	if x, y, ok := c.LeadPosition(); !ok || x != 7 || y != 8 {
		t.Errorf("commander %v %v %v", x, y, ok)
	}
	if _, _, ok := (Card{}).LeadPosition(); ok {
		t.Error("no position")
	}
}

// A leg ends where the party ends it: at the boss, else at the last pull group, else at `to`; the next leg starts there. The line from the
// stage point to the pull positions and back to `to` that an earlier drawing made (every leg 1.2 to 2.6 times too long) is gone.
func TestLegExitAndNextLegStart(t *testing.T) {
	p, err := dungeon.Parse("x.json", []byte(`{"version":1,"name":"x","map":209,"entrance":{"x":0,"y":0,"z":0},"legs":[
 {"name":"a","to":{"x":100,"y":0,"z":0},"via":[{"x":50,"y":0,"z":0}],"pull_groups":[{"entries":[1],"positions":[{"x":300,"y":0,"z":0}]},{"entries":[2,3],"positions":[{"x":400,"y":0,"z":0},{"x":450,"y":0,"z":0}]}]},
 {"name":"b","to":{"x":500,"y":0,"z":0},"boss":{"entry":9,"position":{"x":900,"y":0,"z":0}}},
 {"name":"c","to":{"x":600,"y":0,"z":0},"steps":[{"use_object":{"entry":5,"near":{"x":650,"y":0,"z":0}}}]},
 {"name":"d","to":{"x":700,"y":0,"z":0},"steps":[{"use_object":{"entry":6,"near":{"x":750,"y":0,"z":0}}}]}]}`))
	if err != nil {
		t.Fatal(err)
	}
	g := BuildGeometry(p)
	startOf := func(i int) float64 { return g.Legs[i].Stops[0].X }
	// drawing x is -y: all on the same y here, so the drawing's y is -x of the game
	yOf := func(i int) float64 { return g.Legs[i].Stops[0].Y }
	if yOf(1) != -450 { // leg a has no boss: its last pull group's last position
		t.Errorf("leg b starts at %v, want the last position of the last pull group (-450)", yOf(1))
	}
	if yOf(2) != -900 { // leg b's boss
		t.Errorf("leg c starts at %v, want the boss (-900)", yOf(2))
	}
	if yOf(3) != -600 { // leg c has neither: its `to`
		t.Errorf("leg d starts at %v, want its `to` (-600)", yOf(3))
	}
	_ = startOf
	// leg a's line ends at its stage point (100), never runs on to the packs at 300 to 450
	if path := g.Legs[0].Path; len(path) != 3 || path[2].Y != -100 {
		t.Errorf("leg a's line: %+v", path)
	}
	for _, pg := range g.Legs[0].Packs {
		if pg.Y > -300 {
			t.Errorf("pack %+v not at its place", pg)
		}
	}
}
