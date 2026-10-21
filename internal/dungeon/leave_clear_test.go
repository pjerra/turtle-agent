package dungeon

import (
	"errors"
	"fmt"
	"math"
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

// walkMinYd is the shortest ground distance from at to any segment of the path (all segments, not the first that is close).
func walkMinYd(at Point, path navmesh.Path) float64 {
	min := math.Inf(1)
	for i := 0; i+1 < len(path.Points); i++ {
		x, y := path.Points[i], path.Points[i+1]
		if d := pointToSegYd(at, Point{x[0], x[1], x[2]}, Point{y[0], y[1], y[2]}); d < min {
			min = d
		}
	}
	if len(path.Points) == 1 {
		min = groundDist(at, Point{path.Points[0][0], path.Points[0][1], path.Points[0][2]})
	}
	return min
}

// leaveWalkClearance walks every pull group with approach rows on the navmesh, from where the party stands after the group before it (that
// group's stand) through the approach points in order, and returns a finding for each walk whose shortest distance to a Leave, over all its
// segments, is under LeaveClearYd; the finding carries that shortest distance. The points alone are not enough: zulfarrak's first velratha
// rows 1584 1050 -> 1603 1043 are the navmesh's short way past the island, 0.8 yd from the Shadowhunter, whichever side the points are on.
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
			path := planWalk(t, pl, p.Map, from, a)
			if path.Status != navmesh.Complete {
				t.Errorf("leg %s pull group %d approach %d %v from %v: %s %s", r.leg, r.idx, j, a, from, path.Status, path.Reason)
				continue
			}
			for _, lv := range leaves {
				if d := walkMinYd(lv.At, path); d < LeaveClearYd {
					out = append(out, leaveFinding{r.leg, fmt.Sprintf("pull group %d walk to approach %d (from %.0f %.0f)", r.idx, j, from.X, from.Y), a, lv, d})
				}
			}
			from = a
		}
	}
	return out
}

func planWalk(t *testing.T, pl *navmesh.Planner, mapID uint32, from, to Point) navmesh.Path {
	t.Helper()
	path, err := pl.Plan(mapID, navmesh.Vec3{from.X, from.Y, from.Z}, navmesh.Vec3{to.X, to.Y, to.Z})
	if errors.Is(err, navmesh.ErrNoEngine) {
		t.Skip("built without cgo: no navmesh engine")
	}
	if err != nil {
		t.Fatalf("walk %v -> %v: %v", from, to, err)
	}
	return path
}

// leavePullWalkClearance is the module's own walk after the approach rows: from each pull group's stand toward the pack (its centre and each
// member's position: the tank walks to its firing stand, about 30 yd short of the pack, and the pull fetches the pack back along the same
// ground), and the pack's way back to the stand. Both are navmesh paths of the stand and the pack, in both directions; a finding carries
// the shortest distance to a Leave over every segment of every one of them. Unlike the approach rows these walks are not plan data: a
// finding here is a risk for the module to meet, not a row to move.
func leavePullWalkClearance(t *testing.T, p *Plan, pl *navmesh.Planner) []leaveFinding {
	t.Helper()
	leaves := p.Leaves()
	refs, _ := groupStands(p)
	var out []leaveFinding
	for _, r := range refs {
		targets := append([]Point{groupCentre(r.g)}, r.g.Positions...)
		for _, lv := range leaves {
			min, which := math.Inf(1), ""
			for ti, to := range targets {
				for dir, walk := range [2][2]Point{{r.stand, to}, {to, r.stand}} {
					path := planWalk(t, pl, p.Map, walk[0], walk[1])
					if len(path.Points) == 0 {
						continue
					}
					if d := walkMinYd(lv.At, path); d < min {
						min = d
						which = fmt.Sprintf("stand to pack position %d", ti-1)
						if ti == 0 {
							which = "stand to pack centre"
						}
						if dir == 1 {
							which = strings.Replace(which, "stand to pack", "pack to stand", 1)
						}
					}
				}
			}
			if min < LeaveClearYd {
				out = append(out, leaveFinding{r.leg, fmt.Sprintf("pull group %d %s (stand %.0f %.0f)", r.idx, which, r.stand.X, r.stand.Y), r.stand, lv, min})
			}
		}
	}
	return out
}

// zfKnownLeaveWalks are the walks of Zul'Farrak that pass the island Shadowhunter closer than LeaveClearYd and that plan data cannot move
// (measured on the 209 navmesh, 2026-10-21; the value is the shortest distance over every segment of the walk, held to +-0.5 yd so a change
// of the plan or of the mesh that makes one worse, or better, has to be looked at):
//   - velratha group 5, the approach walk 1606 1040 -> 1588 1080: 5.0 yd. The only way north from the east side to the next pack is the west
//     lane, which the island's mesh patch sits beside.
//   - velratha group 4, the pull walk from the stand 1606 1040 toward the pack at 1583 1087 (the tank walks to its firing stand about 30 yd
//     short of it, around y 1060, and the pull fetches the pack back along the same lane): 5.0 yd. The pack holds Shadowhunters (7246) like
//     the island mob, so a social-link aggro of 81610 by the dragged pack is possible too.
//   - velratha group 3, the lone Blood Drinker at 1585.1 1035.5: 13.6 yd, the pack's own position.
//
// The Shadowhunter is passive at such range (zf-r6: 4 passes at 100 % out of combat; the agent walks it every time), so what keeps the run
// leader's hop pull and melee chase from taking it is the module (PLAN-ZF-LESSONS #2), not the plan.
var zfKnownLeaveWalks = []struct {
	what string
	yd   float64
}{
	{"pull group 5 walk to approach 0", 5.0},
	{"pull group 4 stand to pack", 5.0},
	{"pull group 3 stand to pack", 13.6},
}

// Zul'Farrak's walks to its approach points keep 15 yd from the island Shadowhunter except the known ones; the old rows, whose points were
// too close anyway, are found by the walk too (their short way passes 0.8 yd from it).
func TestZulfarrakApproachWalkKeepsClearOfLeave(t *testing.T) {
	dir := zulfarrakMmaps()
	if dir == "" {
		t.Skip("no map 209 navmesh files: set TURTLE_AGENT_MMAPS to a folder holding 209.mmap and the 209*.mmtile tiles")
	}
	pl := navmesh.NewPlanner(dir)
	defer pl.Close()
	p := loadPlan(t, zulfarrakPlan)
	seen := map[string]bool{}
	for _, f := range append(leaveWalkClearance(t, p, pl), leavePullWalkClearance(t, p, pl)...) {
		known := false
		for _, k := range zfKnownLeaveWalks {
			if f.leg == "velratha" && strings.HasPrefix(f.what, k.what) {
				known = true
				seen[k.what] = true
				t.Logf("known: %s", f)
				if f.dist < k.yd-0.5 || f.dist > k.yd+0.5 {
					t.Errorf("the known walk %q is %.1f yd from the Shadowhunter, recorded %.1f: look at it and record the new figure", k.what, f.dist, k.yd)
				}
			}
		}
		if !known {
			t.Errorf("%s", f)
		}
	}
	for _, k := range zfKnownLeaveWalks {
		if !seen[k.what] {
			t.Errorf("the known walk %q is no longer within %.0f yd of the island Shadowhunter: remove it from zfKnownLeaveWalks", k.what, LeaveClearYd)
		}
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
		t.Fatal("the walk of 848693b's rows (1584 1050 -> 1603 1043, 0.8 yd from the Shadowhunter) is not found")
	}
}
