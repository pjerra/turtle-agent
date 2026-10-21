package dungeon

import (
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"testing"

	"github.com/pjerra/turtle-agent/internal/navmesh"
)

// The leave-clearance rule (zf-r6-19z24, PLAN-ZF-LESSONS section 3 F-a/F-c): a plan names the spawns a run leaves alone (a leg's `leave`
// list: Zul'Farrak's lone Shadowhunter 7246, spawn 81610, on a navmesh island at 1591 1047.7). No approach point, stand or move point of the
// plan may lie within LeaveClearYd of one: the run leader's hop pull takes every living hostile within 15 yd of a hop point that no later
// step owns, and 848693b's approach rows (7.4 and 12.9 yd from 81610) sent the tank to fight it, onto the island, where every move was
// refused. The agent's own fix of 15 Oct was plan data of the same kind (the group was dropped).

type leaveFinding struct {
	leg   string
	what  string // "pull group 4 approach 0", "pull group 4 stand", "to", "via 2"
	at    Point
	leave Leave
	dist  float64
}

func (f leaveFinding) String() string {
	return fmt.Sprintf("leg %s %s %.1f %.1f is %.1f yd from the left-alone %d (spawn %d) at %.1f %.1f (at least %.0f wanted)", f.leg, f.what, f.at.X, f.at.Y, f.dist, f.leave.Entry, f.leave.Spawn, f.leave.At.X, f.leave.At.Y, LeaveClearYd)
}

// leaveClearance returns the findings of the rule for a plan, in plan order: every approach point of a pull group or boss, every pull
// group's stand (its last approach point, or the point the run leader walks to 30 yd short of it), every leg's `to` and every via point
// that stands within LeaveClearYd (ground distance) of a Leave of the plan.
func leaveClearance(p *Plan) []leaveFinding {
	leaves := p.Leaves()
	if len(leaves) == 0 {
		return nil
	}
	var out []leaveFinding
	check := func(leg, what string, at Point) {
		for _, lv := range leaves {
			if d := groundDist(at, lv.At); d < LeaveClearYd {
				out = append(out, leaveFinding{leg, what, at, lv, d})
			}
		}
	}
	refs, _ := groupStands(p)
	for _, l := range p.Legs {
		if l.To != (Point{}) {
			check(l.Name, "to", l.To)
		}
		for i, w := range l.Via {
			check(l.Name, fmt.Sprintf("via %d", i), w.Point)
		}
		for i, g := range l.PullGroups {
			for j, a := range g.Approach {
				check(l.Name, fmt.Sprintf("pull group %d approach %d", i, j), a)
			}
		}
		if b := l.Boss; b != nil {
			for j, a := range b.Approach {
				check(l.Name, fmt.Sprintf("boss approach %d", j), a)
			}
		}
	}
	for _, r := range refs {
		if len(r.g.Approach) == 0 { // the last approach point is checked above
			check(r.leg, fmt.Sprintf("pull group %d stand", r.idx), r.stand)
		}
	}
	return out
}

// Every shipped plan passes the rule; the finding reads which point, and how far.
func TestPlansKeepClearOfLeave(t *testing.T) {
	files, _ := filepath.Glob(filepath.Join(plansDir, "*.json"))
	for _, f := range files {
		p, err := Load(f)
		if err != nil {
			continue // plans_all_test reports a plan that does not load
		}
		for _, x := range leaveClearance(p) {
			t.Errorf("%s: %s", p.Name, x)
		}
	}
}

// Zul'Farrak leaves the island Shadowhunter alone: spawn 81610 (entry 7246) at 1591 1047.7, named by the velratha leg.
func TestZulfarrakLeavesTheIslandShadowhunter(t *testing.T) {
	p := loadPlan(t, zulfarrakPlan)
	var island *Leave
	for _, lv := range p.Leaves() {
		if lv.Spawn == 81610 {
			island = &lv
		}
	}
	if island == nil || island.Entry != 7246 || groundDist(island.At, Point{1591, 1047.7, 9}) > 1 {
		t.Fatalf("the plan does not leave spawn 81610 (7246 at 1591 1047.7) alone: %+v", p.Leaves())
	}
	if island.Why == "" {
		t.Error("the leave has no reason")
	}
	if fs := leaveClearance(p); len(fs) != 0 {
		t.Fatalf("the plan has findings: %v", fs)
	}
}

// The regression: 848693b's approach rows (1584 1050 and 1603 1043: 7.4 and 12.9 yd from 81610) must be found, on the first and on the last
// approach point; a plan with no leave is clean.
func TestLeaveClearanceFailsOnTheOldIslandApproach(t *testing.T) {
	p := loadPlan(t, zulfarrakPlan)
	for li := range p.Legs {
		l := &p.Legs[li]
		if l.Name != "velratha" {
			continue
		}
		for gi := range l.PullGroups {
			if len(l.PullGroups[gi].Approach) > 0 && l.PullGroups[gi].Entries[0] == 5650 && len(l.PullGroups[gi].Entries) == 5 {
				l.PullGroups[gi].Approach = []Point{{1584, 1050, 9}, {1603, 1043, 9.8}}
			}
		}
	}
	var hit []string
	for _, f := range leaveClearance(p) {
		hit = append(hit, f.String())
	}
	all := strings.Join(hit, "\n")
	if len(hit) != 2 || !strings.Contains(all, "approach 0 1584.0 1050.0 is 7.4 yd") || !strings.Contains(all, "approach 1 1603.0 1043.0 is 12.9 yd") {
		t.Fatalf("848693b's approach rows are not found on both points: %v", hit)
	}
	t.Logf("old approach findings: %s", strings.Join(hit, "; "))
	for li := range p.Legs {
		p.Legs[li].Leave = nil
	}
	if fs := leaveClearance(p); len(fs) != 0 {
		t.Errorf("a plan with no leave has findings: %v", fs)
	}
}

// The stand the run leader computes (no approach rows) is held to the rule too, and a boss approach point and a via point are found.
func TestLeaveClearanceChecksComputedStandsBossesAndVias(t *testing.T) {
	p := &Plan{Entrance: Point{0, 0, 0}, Legs: []Leg{{
		Name:  "a",
		To:    Point{100, 0, 0},
		Via:   []Waypoint{{Point: Point{100, 40, 0}}},
		Leave: []Leave{{Entry: 9, Spawn: 1, At: Point{100, 45, 0}, Why: "island"}},
		PullGroups: []PullGroup{
			{Entries: []uint32{1}, Positions: []Point{{100, 90, 0}}, Radius: 15},
		},
		Boss: &Boss{Entry: 2, Position: Point{100, 130, 0}, Approach: []Point{{100, 52, 0}}},
	}}}
	var got []string
	for _, f := range leaveClearance(p) {
		got = append(got, f.what)
	}
	// via 0 at 5 yd, the stand (60 yd short of... 30 yd from the group: 100 60) at 15.0 is NOT a finding, the boss approach at 7 yd is.
	want := "via 0,boss approach 0"
	if strings.Join(got, ",") != want {
		t.Errorf("findings %v, want %s", got, want)
	}
	p.Legs[0].PullGroups[0].Positions = []Point{{100, 70, 0}} // the stand is now (100, 40): 5 yd from the leave
	got = nil
	for _, f := range leaveClearance(p) {
		got = append(got, f.what)
	}
	if !strings.Contains(strings.Join(got, ","), "pull group 0 stand") {
		t.Errorf("the computed stand is not checked: %v", got)
	}
}

// leaveWalkClearance walks every pull group with approach rows on the navmesh, from where the party stands after the group before it (that
// group's stand) through the approach points in order, and returns a finding for each stretch of the walk that passes within LeaveClearYd of
// a Leave. The points alone are not enough: zulfarrak's first velratha rows 1584 1050 -> 1603 1043 are the navmesh's short way past the
// island, 1.1 yd from the Shadowhunter, whichever side the points are on.
func leaveWalkClearance(t *testing.T, p *Plan, pl *navmesh.Planner) []leaveFinding {
	t.Helper()
	leaves := p.Leaves()
	refs, _ := groupStands(p)
	var out []leaveFinding
	for k, r := range refs {
		if k == 0 || len(r.g.Approach) == 0 {
			continue
		}
		from := refs[k-1].stand
		for j, a := range r.g.Approach {
			path, err := pl.Plan(p.Map, navmesh.Vec3{from.X, from.Y, from.Z}, navmesh.Vec3{a.X, a.Y, a.Z})
			if errors.Is(err, navmesh.ErrNoEngine) {
				t.Skip("built without cgo: no navmesh engine")
			}
			if err != nil {
				t.Fatalf("leg %s pull group %d approach %d: %v", r.leg, r.idx, j, err)
			}
			if path.Status != navmesh.Complete {
				t.Errorf("leg %s pull group %d approach %d %v from %v: %s %s", r.leg, r.idx, j, a, from, path.Status, path.Reason)
				continue
			}
			for _, lv := range leaves {
				for i := 0; i+1 < len(path.Points); i++ {
					x, y := path.Points[i], path.Points[i+1]
					if d := pointToSegYd(lv.At, Point{x[0], x[1], x[2]}, Point{y[0], y[1], y[2]}); d < LeaveClearYd {
						out = append(out, leaveFinding{r.leg, fmt.Sprintf("pull group %d walk to approach %d (from %.0f %.0f)", r.idx, j, from.X, from.Y), a, lv, d})
						break
					}
				}
			}
			from = a
		}
	}
	return out
}

// Zul'Farrak's walks to its approach points keep 15 yd from the island Shadowhunter; the old rows, whose points were too close anyway, are
// found by the walk too (their short way passes 1.1 yd from it).
func TestZulfarrakApproachWalkKeepsClearOfLeave(t *testing.T) {
	dir := zulfarrakMmaps()
	if dir == "" {
		t.Skip("no map 209 navmesh files: set TURTLE_AGENT_MMAPS to a folder holding 209.mmap and the 209*.mmtile tiles")
	}
	pl := navmesh.NewPlanner(dir)
	defer pl.Close()
	p := loadPlan(t, zulfarrakPlan)
	// Known and not fixable by points: the only way north from the east side to the next pack (pull group 5, 1601.7 1104.4) is the west lane,
	// which passes the island Shadowhunter at 9.3 yd. It is passive at that range (zf-r6: 4 passes at 100 % out of combat; the agent walks
	// it every time); what keeps the run leader's hop pull from taking it is the module (PLAN-ZF-LESSONS #2), not the plan. The entry must
	// stay true: if the walk stops passing it, drop the entry.
	const knownWalk = "pull group 5 walk to approach 0"
	known := false
	for _, f := range leaveWalkClearance(t, p, pl) {
		if f.leg == "velratha" && strings.HasPrefix(f.what, knownWalk) {
			known = true
			t.Logf("known: %s", f)
			continue
		}
		t.Errorf("%s", f)
	}
	if !known {
		t.Errorf("the known walk (%s) is no longer within %.0f yd of the island Shadowhunter: remove it from the test", knownWalk, LeaveClearYd)
	}
	for li := range p.Legs {
		l := &p.Legs[li]
		for gi := range l.PullGroups {
			if l.Name == "velratha" && len(l.PullGroups[gi].Approach) == 3 {
				l.PullGroups[gi].Approach = []Point{{1584, 1050, 9}, {1603, 1043, 9.8}}
			}
		}
	}
	found := false
	for _, f := range leaveWalkClearance(t, p, pl) {
		if f.leg == "velratha" && strings.HasPrefix(f.what, "pull group 4 walk to approach 1") && f.dist < 3 {
			found = true
			t.Logf("old rows' walk: %s", f)
		}
	}
	if !found {
		t.Fatal("the walk of 848693b's rows (1584 1050 -> 1603 1043, 1.3 yd from the Shadowhunter) is not found")
	}
}
