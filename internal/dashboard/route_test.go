package dashboard

import (
	"math"
	"strings"
	"testing"
)

func TestLegStopsOrder(t *testing.T) {
	g := BuildGeometry(tinyPlan(t))
	stops := g.Legs[0].Stops
	var kinds []string
	for _, s := range stops {
		kinds = append(kinds, s.Kind)
	}
	// start, the two via points, the first position of each pull group, the boss, the end: the order the plan lists them
	if got, want := strings.Join(kinds, ","), "start,via,via,pull,pull,boss,end"; got != want {
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
	if stops[3].Group != 0 || stops[4].Group != 1 || !strings.HasPrefix(stops[3].Label, "pack 1:") {
		t.Errorf("pull stops %+v %+v", stops[3], stops[4])
	}
	if g.Legs[0].Label != "One" || g.Legs[1].Label == "" {
		t.Errorf("leg labels %q %q", g.Legs[0].Label, g.Legs[1].Label)
	}
	// the second leg starts where the first ended
	if a, b := stops[len(stops)-1], g.Legs[1].Stops[0]; a.X != b.X || a.Y != b.Y {
		t.Errorf("leg 1 starts at %v,%v, leg 0 ended at %v,%v", b.X, b.Y, a.X, a.Y)
	}
	// the drawn line runs through all of them, and a boss on the spot of the end is one point of the line
	path := g.Legs[0].Path
	if len(path) != len(stops)-1 {
		t.Errorf("path of %d points for %d stops", len(path), len(stops))
	}
	if !strings.Contains(g.SVG, "step 3 of 6") {
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
