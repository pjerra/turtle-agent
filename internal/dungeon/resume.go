package dungeon

import (
	"fmt"
	"math"
)

// Resume: a run started with the party already inside a leg (a restart halfway through it) must not run back to the leg's first
// waypoint. The leg's route is the straight lines from where the leg starts (the entrance, or the end of the leg before) through
// its `to`, the first position of each pull group, and the boss. When the leader stands close to that route, everything it has
// already passed is skipped: the walk to `to`, and the pull groups behind it that are surely gone. A group counts as gone only when
// a party member stands within ResumeSeeRadius of its positions and no hostile is listed there: the feed and the unit list hold only
// what someone sees (about 60 yd, and a frame may be cut short), so a pack out of everyone's sight proves nothing. Any other group
// is pulled (an empty pull passes quickly); the groups from the first unsure one on are all kept.

// ResumeSeeRadius is how near a party member must stand to a pull group's positions for an empty view of them to be believed.
const ResumeSeeRadius = 40

// ResumeMaxOff is how far (yards) from the leg's route the leader may stand for the run to take it for inside the leg.
const ResumeMaxOff = 80.0

// Resumed is what a resume skipped, for the report.
type Resumed struct {
	Leg           int   `json:"leg"`
	At            Point `json:"at"`             // where the leader stood
	SkippedWalk   bool  `json:"skipped_walk"`   // the walk to the leg's `to`
	SkippedGroups int   `json:"skipped_groups"` // pull groups, from the first, passed over
}

// legStart is where leg i begins: the plan's entrance for the first, else the end of the leg before it.
func legStart(p *Plan, i int) Point {
	if i <= 0 {
		return p.Entrance
	}
	prev := p.Legs[i-1]
	switch {
	case prev.Boss != nil:
		return prev.Boss.Position
	case len(prev.PullGroups) > 0 && len(prev.PullGroups[len(prev.PullGroups)-1].Positions) > 0:
		g := prev.PullGroups[len(prev.PullGroups)-1]
		return g.Positions[0]
	}
	return prev.To
}

// segDist is the distance from p to the segment a-b (3D) and where on it (0-1) the nearest point lies.
func segDist(p, a, b Point) (dist, t float64) {
	ab := [3]float64{float64(b.X - a.X), float64(b.Y - a.Y), float64(b.Z - a.Z)}
	ap := [3]float64{float64(p.X - a.X), float64(p.Y - a.Y), float64(p.Z - a.Z)}
	l2 := ab[0]*ab[0] + ab[1]*ab[1] + ab[2]*ab[2]
	if l2 > 0 {
		t = (ap[0]*ab[0] + ap[1]*ab[1] + ap[2]*ab[2]) / l2
		t = math.Max(0, math.Min(1, t))
	}
	c := Point{X: float32(float64(a.X) + ab[0]*t), Y: float32(float64(a.Y) + ab[1]*t), Z: float32(float64(a.Z) + ab[2]*t)}
	return dist3(p, c), t
}

// ResumeAt moves a Stepper that has just entered its leg past what the leader stands beyond. It reports what it skipped; ok is false
// when it skipped nothing (the leader is not inside the leg's route, or still before its first stop).
func (s *Stepper) ResumeAt() (r Resumed, ok bool) {
	if s.leg >= len(s.plan.Legs) || s.ph != phWalk || s.idx != 0 {
		return r, false
	}
	l := s.plan.Legs[s.leg]
	pos := s.w.Position()
	route := []Point{legStart(s.plan, s.leg), l.To}
	for _, g := range l.PullGroups {
		if len(g.Positions) > 0 {
			route = append(route, g.Positions[0])
		}
	}
	if l.Boss != nil {
		route = append(route, l.Boss.Position)
	}
	// The segment nearest to the leader; the later one wins a tie (the leader stands on a stop).
	k, best := -1, math.MaxFloat64
	for i := 0; i+1 < len(route); i++ {
		if d, _ := segDist(pos, route[i], route[i+1]); d <= best {
			k, best = i, d
		}
	}
	if k < 1 || best > ResumeMaxOff { // before the leg's `to` (segment 0) or nowhere near
		return r, false
	}
	// Segment k leads from route[k] to route[k+1]: route[1] (`to`) and the groups at route[2:] up to route[k] are behind.
	r = Resumed{Leg: s.leg, At: pos, SkippedWalk: true}
	idx := 0
	for idx < len(l.PullGroups) && len(l.PullGroups[idx].Positions) > 0 && k >= 2+idx {
		g := l.PullGroups[idx]
		radius := g.Radius
		if radius <= 0 {
			radius = 25
		}
		pw, known := s.w.(PartyWorld)
		seen, alive := false, 0
		for _, p := range g.Positions {
			alive += s.w.HostilesWithin(p, radius)
			seen = seen || (known && pw.PartyNear(p, ResumeSeeRadius))
		}
		if alive > 0 || !seen {
			break // a pack the party passed but did not kill, or one nobody has looked at: it is still to be pulled
		}
		idx++
	}
	r.SkippedGroups = idx
	s.ph, s.idx = phPulls, idx
	return r, true
}

// String says what a resume did, for the report's notes.
func (r Resumed) String() string {
	return fmt.Sprintf("resumed leg %d at %.0f %.0f %.0f: skipped the walk to the leg's first point and %d pull group(s) behind the leader", r.Leg, r.At.X, r.At.Y, r.At.Z, r.SkippedGroups)
}
