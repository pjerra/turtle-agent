package dungeon

import (
	"fmt"
	"math"
	"strings"
)

// Avoid regions (Plan.Avoid): ground the party must never stand on although the navmesh calls it plain ground.
//
// Ragefire Chasm, runs rfc-live-1d and 1e (2026-10-15/16): bots died within 2 s at full health with no damage in the feed on the floor of the
// trogg cave's lava basin (x -224 to -144, y -62 to 3, floor z -65.4). The client's WMO (lavadungeon.wmo, group 1) lays a magma surface
// at z -64.5 over that floor, under a yard deep; the core deals 605-610 fire damage a tick to a player in magma (EnvironmentalDamage.Min/Max,
// Player::OnMirrorTimerExpirationPulse), which is more than a level-16 bot's health. The mmap generator gives a ground span the liquid's area
// only where the liquid is at least the agent's height deep (TileBuilder.cpp filterWalkableLowHeightSpansWith, inWaterGround =
// walkableHeight), so the tiles flag the basin floor NAV_GROUND, not NAV_MAGMA, and neither the agent's planner nor the server's PathFinder
// keeps out of it: the agent planned the walk back to leg 3 straight across it (1e, cmd move -207.8 -56.1 -65.5), and the tank chased
// pulls across it. An avoid box marks that ground: the planner flags its polygons NAV_MAGMA (navmesh.Planner.SetAvoid), the plan refuses a
// point in one, and the remote driver summons no bot onto a lead bot standing in one and raises no body lying in one.

// AvoidBox is one avoid region: the points from Min to Max (inclusive) on every axis. A box is a floor band, not a column: its Max.Z
// is just above the deadly surface, so the ledges and ramps above it stay walkable.
type AvoidBox struct {
	// Name says what the region is ("lava basin of the trogg cave"), for the messages.
	Name string `json:"name"`
	Min  Point  `json:"min"`
	Max  Point  `json:"max"`
	// Why says how the region was found, for people.
	Why string `json:"why,omitempty"`
}

// Contains says whether p lies inside the box.
func (b AvoidBox) Contains(p Point) bool {
	return p.X >= b.Min.X && p.X <= b.Max.X && p.Y >= b.Min.Y && p.Y <= b.Max.Y && p.Z >= b.Min.Z && p.Z <= b.Max.Z
}

// InAvoid is the avoid box of the plan that holds p, if any.
func (p *Plan) InAvoid(pt Point) (AvoidBox, bool) {
	for _, b := range p.Avoid {
		if b.Contains(pt) {
			return b, true
		}
	}
	return AvoidBox{}, false
}

// maxAvoidBoxes bounds Plan.Avoid; maxAvoidSpan is the widest a box may be on any axis (yards).
const (
	maxAvoidBoxes = 32
	maxAvoidSpan  = 400
)

type rawAvoid struct {
	Name   string    `json:"name"`
	RawMin *rawPoint `json:"min"`
	RawMax *rawPoint `json:"max"`
	Why    string    `json:"why"`
}

// avoid checks and copies the avoid boxes.
func (c *checker) avoid(raw []rawAvoid) []AvoidBox {
	if len(raw) > maxAvoidBoxes {
		c.fail("avoid", "%d boxes: at most %d", len(raw), maxAvoidBoxes)
		return nil
	}
	var out []AvoidBox
	for i, r := range raw {
		path := fmt.Sprintf("avoid[%d]", i)
		if strings.TrimSpace(r.Name) == "" {
			c.fail(path+".name", "required (what the region is)")
		}
		lo, hi := c.point(path+".min", r.RawMin), c.point(path+".max", r.RawMax)
		if r.RawMin == nil || r.RawMax == nil {
			continue
		}
		bad := false
		for _, ax := range []struct {
			name   string
			lo, hi float32
		}{{"x", lo.X, hi.X}, {"y", lo.Y, hi.Y}, {"z", lo.Z, hi.Z}} {
			switch {
			case ax.lo >= ax.hi:
				c.fail(path, "min.%s %.2f is not below max.%s %.2f", ax.name, ax.lo, ax.name, ax.hi)
				bad = true
			case ax.hi-ax.lo > maxAvoidSpan:
				c.fail(path, "%.0f yd across in %s: at most %d (a box is one floor region, not the map)", ax.hi-ax.lo, ax.name, maxAvoidSpan)
				bad = true
			}
		}
		if !bad {
			out = append(out, AvoidBox{Name: r.Name, Min: lo, Max: hi, Why: r.Why})
		}
	}
	return out
}

// offAvoid refuses every point of the plan that lies in an avoid box: the entrance, each leg's via points and `to`, the pull groups'
// positions (the party walks to them and fights there), the boss and the stands of the steps. Patrol paths are left alone: a patrol is a mob,
// and a mob takes no damage from the lava.
func (c *checker) offAvoid(p *Plan) {
	if len(p.Avoid) == 0 {
		return
	}
	check := func(path string, pt Point) {
		if b, ok := p.InAvoid(pt); ok {
			c.fail(path, "%.1f %.1f %.1f lies in avoid box %q (%s): no point of the plan may stand on it", pt.X, pt.Y, pt.Z, b.Name, boxText(b))
		}
	}
	check("entrance", p.Entrance)
	for li, l := range p.Legs {
		lp := fmt.Sprintf("legs[%d]", li)
		for vi, w := range l.Via {
			check(fmt.Sprintf("%s.via[%d]", lp, vi), w.Point)
		}
		check(lp+".to", l.To)
		for gi, g := range l.PullGroups {
			for pi, pt := range g.Positions {
				check(fmt.Sprintf("%s.pull_groups[%d].positions[%d]", lp, gi, pi), pt)
			}
		}
		if l.Boss != nil {
			check(lp+".boss.position", l.Boss.Position)
		}
		for _, list := range []struct {
			name  string
			steps []Step
		}{{"steps", l.Steps}, {"after_boss", l.AfterBoss}} {
			for si, st := range list.steps {
				if at, label, ok := stepStand(st); ok {
					check(fmt.Sprintf("%s.%s[%d].%s", lp, list.name, si, label), at)
				}
			}
		}
	}
}

// stepStand is the stand point of a step that has one, with its field's name.
func stepStand(st Step) (Point, string, bool) {
	switch {
	case st.UseObject != nil && st.UseObject.Stand != nil:
		return *st.UseObject.Stand, "use_object.stand", true
	case st.LootObject != nil && st.LootObject.Stand != nil:
		return *st.LootObject.Stand, "loot_object.stand", true
	case st.Ritual != nil && st.Ritual.Stand != nil:
		return *st.Ritual.Stand, "ritual.stand", true
	case st.AreaTrigger != nil:
		return st.AreaTrigger.Stand, "area_trigger.stand", true
	case st.Gossip != nil && st.Gossip.Stand != nil:
		return *st.Gossip.Stand, "gossip.stand", true
	}
	return Point{}, "", false
}

func boxText(b AvoidBox) string {
	return fmt.Sprintf("x %.1f to %.1f, y %.1f to %.1f, z %.1f to %.1f", b.Min.X, b.Max.X, b.Min.Y, b.Max.Y, b.Min.Z, b.Max.Z)
}

// chordNearAvoid says whether the straight line from a to b passes over an avoid box near the box's height: within avoidChordDZ yards
// of it, at the line's own height there. The server walks a `move` on its own navmesh, which does not know the box: a hop whose straight
// line crosses the region may be walked through it when that is shorter than the way round, so such a hop is cut at a corner of the
// planned path instead (alongPathAvoid).
func chordNearAvoid(a, b Point, boxes []AvoidBox) bool {
	if len(boxes) == 0 {
		return false
	}
	n := int(math.Ceil(dist3(a, b))) + 1
	for i := 0; i <= n; i++ {
		t := float64(i) / float64(n)
		p := lerp(a, b, t)
		for _, bx := range boxes {
			if p.X >= bx.Min.X && p.X <= bx.Max.X && p.Y >= bx.Min.Y && p.Y <= bx.Max.Y && p.Z >= bx.Min.Z-avoidChordDZ && p.Z <= bx.Max.Z+avoidChordDZ {
				return true
			}
		}
	}
	return false
}

// avoidChordDZ is how far above (or below) an avoid box a straight hop over it still counts as crossing it (yards): the lava basin's
// strip and rim are 3 to 5 yd above its floor, and the server's path down to the floor is shorter than the way round.
const avoidChordDZ = 6

// alongPathAvoid is alongPath for a plan with avoid boxes: the farthest point of the path within leg yards of from whose straight line
// from `from` does not pass over an avoid box (chordNearAvoid), so that the server, which walks each hop on its own navmesh, cannot take
// a shorter way through the box than the planned path. The candidates are the path's corners within leg and the cut point on the segment
// that leaves the circle, in order; when no candidate's line is clear the first one is taken (the next corner, or the cut on the first
// segment): a segment of the planned path is itself straight over walkable ground of the server's navmesh, so the server's shortest path
// along it is the segment.
func alongPathAvoid(pts []Point, from Point, leg float32, boxes []AvoidBox) (Point, bool) {
	var cands []Point
	for i := 1; i < len(pts); i++ {
		if dist3(from, pts[i]) <= float64(leg) {
			cands = append(cands, pts[i])
			continue
		}
		a := pts[i-1]
		if i == 1 {
			a = from
		}
		if p, ok := cutSegment(a, pts[i], from, leg); ok {
			cands = append(cands, p)
		}
		break
	}
	if len(cands) == 0 {
		return from, false
	}
	for k := len(cands) - 1; k >= 1; k-- {
		if !chordNearAvoid(from, cands[k], boxes) {
			return cands[k], true
		}
	}
	return cands[0], true
}

// cutSegment is the point of the segment a-b farthest along it that is still within leg yards of from (a is within it).
func cutSegment(a, b, from Point, leg float32) (Point, bool) {
	if dist3(a, b) == 0 {
		return a, false
	}
	lo, hi := 0.0, 1.0
	for range 24 {
		mid := (lo + hi) / 2
		if dist3(from, lerp(a, b, mid)) <= float64(leg) {
			lo = mid
		} else {
			hi = mid
		}
	}
	if lo == 0 {
		return a, false
	}
	return lerp(a, b, lo), true
}
