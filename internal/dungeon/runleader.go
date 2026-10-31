package dungeon

import "fmt"

// The TortoiseBots run leader (`.bot run`, the rows plan-export writes) walks a plan differently from the agent's driver: its party stands
// on the last move row (a via, a `to`, a step's stand, an approach point, a hold's spot) and pulls a pack or a boss from there when it
// sees it, within RunLeaderSeeYd of its lead bot. A pack or boss farther away is walked toward in navmesh hops the run leader plans
// itself (DungeonRunPolicy.h ApproachHop): those hops are where the run stopped in Uldaman (live 2026-10-16: a hop point under a low
// ceiling refused off-mesh, and long paths with no path at all), so a plan gives such a pack approach points (PullGroup.Approach).
// RunLeaderParty and RunLeaderReach model that: no navmesh, only the rows' geometry.

// RunLeaderPoint is one point the run leader's party is walked to, in the order of the rows.
type RunLeaderPoint struct {
	Leg int
	// Part names the point the way PlanIssue.Part does ("via 1", "to", "pull group 3 approach 0", "boss hold spot").
	Part  string
	Point Point
}

// WarnRunLeaderHop is the reason of the warning on a pack or boss that stands farther than RunLeaderReachYd from the run leader's
// last move row: the run leader walks toward it in navmesh hops of its own.
const WarnRunLeaderHop = "run leader hop"

// PackCentre is where plan-export puts a pull group's row: the centre of its positions.
func PackCentre(g PullGroup) Point {
	var x, y, z float64
	for _, p := range g.Positions {
		x, y, z = x+float64(p.X), y+float64(p.Y), z+float64(p.Z)
	}
	n := float64(len(g.Positions))
	if n == 0 {
		return Point{}
	}
	return Point{X: float32(x / n), Y: float32(y / n), Z: float32(z / n)}
}

// runLeaderWalk calls move for every point the run leader's rows walk the party to and fight for every pull group and boss, in the order
// of the rows (plan-export's: via, to, per pull group its approach points then the pull or hold, the steps' stands, the boss's approach
// then the boss or hold, the after_boss steps' stands), from leg `from` on. An optional leg is left out, as plan-export leaves it out.
func (p *Plan) runLeaderWalk(from int, move func(RunLeaderPoint), fight func(leg int, part string, at Point, group int, team string)) {
	stands := func(li int, kind string, steps []Step) {
		for si, st := range steps {
			switch {
			case st.UseObject != nil && st.UseObject.Stand != nil && !st.UseObject.ViaItem:
				move(RunLeaderPoint{li, fmt.Sprintf("%s[%d].use_object.stand", kind, si), *st.UseObject.Stand})
			case st.AreaTrigger != nil && st.AreaTrigger.By != TriggerAgent:
				move(RunLeaderPoint{li, fmt.Sprintf("%s[%d].area_trigger.stand", kind, si), st.AreaTrigger.Stand})
			case st.WaitFor != nil && st.WaitFor.Hold != nil:
				// plan-export writes such a wait as its hold row: the run leader's party stands on the hold's spot.
				move(RunLeaderPoint{li, fmt.Sprintf("%s[%d].wait_for.hold spot", kind, si), st.WaitFor.Hold.Spot})
			}
		}
	}
	for li := from; li < len(p.Legs); li++ {
		l := p.Legs[li]
		if l.Optional != nil {
			continue
		}
		for vi, w := range l.Via {
			move(RunLeaderPoint{li, fmt.Sprintf("via %d", vi), w.Point})
		}
		move(RunLeaderPoint{li, "to", l.To})
		for gi, g := range l.PullGroups {
			for ai, a := range g.Approach {
				move(RunLeaderPoint{li, fmt.Sprintf("pull group %d approach %d", gi+1, ai), a})
			}
			if g.Hold != nil {
				move(RunLeaderPoint{li, fmt.Sprintf("pull group %d hold spot", gi+1), g.Hold.Spot})
				continue
			}
			fight(li, "pull group", PackCentre(g), gi+1, g.Team)
		}
		stands(li, "steps", l.Steps)
		if b := l.Boss; b != nil {
			for ai, a := range b.Approach {
				move(RunLeaderPoint{li, fmt.Sprintf("boss approach %d", ai), a})
			}
			if b.Hold != nil {
				move(RunLeaderPoint{li, "boss hold spot", b.Hold.Spot})
			} else {
				fight(li, "boss", b.Position, 0, b.Team)
			}
		}
		stands(li, "after_boss", l.AfterBoss)
	}
}

// RunLeaderReach lists, as warnings (reason WarnRunLeaderHop), every pack (its row's centre) and boss of the plan from leg `from` on that
// stands more than RunLeaderReachYd from the run leader's last move row before it.
func (p *Plan) RunLeaderReach(from int) []PlanIssue {
	var out []PlanIssue
	last, have := p.Entrance, from == 0
	if from > 0 {
		last, _ = p.LegStart(from)
		have = true
	}
	p.runLeaderWalk(from, func(m RunLeaderPoint) { last, have = m.Point, true }, func(li int, part string, at Point, group int, team string) {
		if !have {
			return
		}
		d := dist3(last, at)
		if d <= RunLeaderReachYd {
			return
		}
		if group > 0 && group <= len(p.Legs[li].PullGroups) && p.Legs[li].PullGroups[group-1].StandPull && d <= RunLeaderSeeYd {
			return // a stand_pull group is pulled from its stand: the run leader holds there and needs only to see the row's point
		}
		is := PlanIssue{Leg: li, LegName: p.Legs[li].Name, Part: part, Point: at, From: last, Reason: WarnRunLeaderHop, Team: team,
			Detail: fmt.Sprintf("%.0f yd from the run leader's last move row (it sees %d yd; at most %d wanted): it walks there in navmesh hops of its own; give the %s approach points", d, RunLeaderSeeYd, RunLeaderReachYd, part)}
		if group > 0 {
			is.PullGroup, is.PullGroups = group, len(p.Legs[li].PullGroups)
		}
		out = append(out, is)
	})
	return out
}
