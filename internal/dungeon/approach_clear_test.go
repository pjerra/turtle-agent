package dungeon

import (
	"fmt"
	"math"
	"path/filepath"
	"strings"
	"testing"
)

// The approach-clearance rule (r2-19z19, step 26 of Zul'Farrak; PLAN-ZF-STEP26): where the TortoiseBots run leader's party stands to pull a
// group is its last approach point, or else the point runLeaderApproachYd short of the group's centre on the way from where the party was
// before. That spot must keep clear of every group pulled LATER in the plan (their aggro reach at level 46 is under 20 yd) and of every
// patrol path (15 yd), or the walk to the pull starts a fight no pull order, crowd control or firing stand has planned. Today's step 26 stood
// 15.4 yd from a Shadowcaster of the pair that the plan pulled after it.
const (
	runLeaderApproachYd = 30 // DungeonRunPolicy.h kApproachYd
	laterGroupClearYd   = 20.0
	approachDriftYd     = 3.0 // added where the stand is computed, not written: the real path and the walk-in drift
	patrolPathClearYd   = 15.0
)

type standFinding struct {
	leg    string
	group  int
	stand  Point
	what   string
	dist   float64
	wanted float64
}

func (f standFinding) String() string {
	return fmt.Sprintf("leg %s pull group %d: the stand %.1f %.1f is %.1f yd from %s (at least %.0f wanted)", f.leg, f.group, f.stand.X, f.stand.Y, f.dist, f.what, f.wanted)
}

func groupCentre(g PullGroup) Point {
	var c Point
	for _, p := range g.Positions {
		c.X += p.X
		c.Y += p.Y
		c.Z += p.Z
	}
	n := float32(len(g.Positions))
	return Point{c.X / n, c.Y / n, c.Z / n}
}

// segDist is the ground distance from p to the segment a-b.
func pointToSegYd(p, a, b Point) float64 {
	dx, dy := float64(b.X-a.X), float64(b.Y-a.Y)
	l2 := dx*dx + dy*dy
	if l2 == 0 {
		return groundDist(p, a)
	}
	t := (float64(p.X-a.X)*dx + float64(p.Y-a.Y)*dy) / l2
	t = math.Max(0, math.Min(1, t))
	return math.Hypot(float64(p.X-a.X)-t*dx, float64(p.Y-a.Y)-t*dy)
}

// approachClearance returns the findings of the rule for a plan, in plan order. Groups the run leader does not walk up to and pull (ambush,
// hold, off the navmesh) are neither checked nor counted as a stand's anchor.
func approachClearance(p *Plan) []standFinding {
	return approachClearanceOf(p, nil)
}

// approachClearanceOf is approachClearance, limited to the groups whose first spawn is in only (nil: every group).
func approachClearanceOf(p *Plan, only map[Point]bool) []standFinding {
	type ref struct {
		leg   string
		idx   int
		g     PullGroup
		anchr Point
	}
	var refs []ref
	var patrols []Patrol
	anchor := p.Entrance
	var seen []Point // positions of the groups already counted: a later leg that lists the same pack again (velratha's pair) is a repeat, dead by then
	repeat := func(g PullGroup) bool {
		for _, pos := range g.Positions {
			found := false
			for _, q := range seen {
				if groundDist(pos, q) < 3 {
					found = true
				}
			}
			if !found {
				return false
			}
		}
		return true
	}
	for _, l := range p.Legs {
		if l.To != (Point{}) {
			anchor = l.To
		}
		for i, g := range l.PullGroups {
			dup := repeat(g)
			seen = append(seen, g.Positions...)
			if g.Patrol != nil && !dup {
				patrols = append(patrols, *g.Patrol)
			}
			if dup || g.Ambush || g.Hold != nil || g.NeedsWalk || len(g.Positions) == 0 {
				continue
			}
			refs = append(refs, ref{l.Name, i, g, anchor})
			anchor = groupCentre(g)
		}
	}
	var out []standFinding
	for k, r := range refs {
		if only != nil && !only[Point{X: r.g.Positions[0].X, Y: r.g.Positions[0].Y}] {
			continue
		}
		stand, wantLater := Point{}, laterGroupClearYd
		if n := len(r.g.Approach); n > 0 {
			stand = r.g.Approach[n-1]
		} else {
			c := groupCentre(r.g)
			d := groundDist(r.anchr, c)
			if d <= runLeaderApproachYd {
				stand = r.anchr
			} else {
				f := (d - runLeaderApproachYd) / d
				stand = Point{r.anchr.X + float32(f)*(c.X-r.anchr.X), r.anchr.Y + float32(f)*(c.Y-r.anchr.Y), c.Z}
			}
			wantLater += approachDriftYd
		}
		for _, later := range refs[k+1:] {
			for _, pos := range later.g.Positions {
				if d := groundDist(stand, pos); d < wantLater {
					out = append(out, standFinding{r.leg, r.idx, stand, fmt.Sprintf("a spawn of leg %s group %d at %.1f %.1f (pulled later)", later.leg, later.idx, pos.X, pos.Y), d, wantLater})
				}
			}
		}
		for _, pa := range patrols {
			for j := 0; j+1 < len(pa.Path); j++ {
				if d := pointToSegYd(stand, pa.Path[j], pa.Path[j+1]); d < patrolPathClearYd {
					out = append(out, standFinding{r.leg, r.idx, stand, fmt.Sprintf("the patrol path of %v (segment %d)", pa.Entries, j+1), d, patrolPathClearYd})
				}
			}
		}
	}
	return out
}

// zfGuarded are the Zul'Farrak groups held to the rule (key: the first spawn): the Antu'sul leg's Shadowcaster / Soul Eater pair and the
// Shadowcaster camp (r2-19z19 step 26). The other Zul'Farrak groups were written before the rule and have findings of their own, listed below
// as known and not fixed here (the rule is stricter than the walks they were live-checked with; each needs a stand measured on the navmesh).
var zfGuarded = map[Point]bool{{X: 1642.1, Y: 902.1}: true, {X: 1660.7, Y: 901.2}: true}

// The guarded groups of Zul'Farrak must pass the rule; every other finding, in any plan, is logged (report-only).
func TestPlansApproachClearance(t *testing.T) {
	files, _ := filepath.Glob(filepath.Join(plansDir, "*.json"))
	for _, f := range files {
		p, err := Load(f)
		if err != nil {
			continue // plans_all_test reports a plan that does not load
		}
		fail := map[string]bool{}
		if p.Name == "zulfarrak" {
			for _, x := range approachClearanceOf(p, zfGuarded) {
				t.Errorf("%s: %s", p.Name, x)
				fail[x.String()] = true
			}
		}
		n := 0
		for _, x := range approachClearance(p) {
			if !fail[x.String()] {
				n++
				if p.Name == "zulfarrak" { // the other plans: the count only (thousands of lines)
					t.Logf("report-only %s: %s", p.Name, x)
				}
			}
		}
		t.Logf("%s: %d report-only approach-clearance findings", p.Name, n)
	}
}

// The regression: r2-19z19's order and no approach rows (the camp before the pair, no stands) must be found; the edited plan is clean.
func TestApproachClearanceFailsOnTheOldAntusulOrder(t *testing.T) {
	p := loadPlan(t, zulfarrakPlan)
	if fs := approachClearanceOf(p, zfGuarded); len(fs) != 0 {
		t.Fatalf("the plan itself has findings: %v", fs)
	}
	for li := range p.Legs {
		l := &p.Legs[li]
		if l.Name != "antusul" {
			continue
		}
		var camp, pair = -1, -1
		for i, g := range l.PullGroups {
			if len(g.Entries) == 4 && g.Entries[0] == 5648 && g.Entries[1] == 5650 {
				camp = i
			}
			if len(g.Entries) == 2 && g.Entries[0] == 5648 && g.Entries[1] == 7247 {
				pair = i
			}
		}
		if camp < 0 || pair < 0 || pair >= camp {
			t.Fatalf("antusul: pair at %d, camp at %d (the pair is pulled first)", pair, camp)
		}
		// Old order: the camp and the north pack before the pair, no approach rows.
		gs := l.PullGroups
		old := append([]PullGroup{}, gs[:pair]...)
		old = append(old, gs[camp:]...)
		old = append(old, gs[pair])
		for i := range old {
			old[i].Approach = nil
		}
		l.PullGroups = old
	}
	var hit []string
	for _, f := range approachClearanceOf(p, zfGuarded) {
		hit = append(hit, f.String())
	}
	if len(hit) == 0 {
		t.Fatal("the old order passes the approach-clearance rule")
	}
	if !strings.Contains(strings.Join(hit, "\n"), "1642.1 902.1") {
		t.Errorf("the old order is not caught on the pair's Shadowcaster: %v", hit)
	}
	t.Logf("old order findings: %s", strings.Join(hit, "; "))
}
