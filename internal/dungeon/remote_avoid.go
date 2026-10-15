package dungeon

import (
	"context"
	"fmt"
	"math"
	"time"

	"github.com/pjerra/turtle-agent/internal/packet"
	"github.com/pjerra/turtle-agent/internal/tbm"
)

// The remote driver's side of the avoid boxes (avoid.go): the planner is told the plan's boxes, a hop never takes a straight line over
// one, a summon never lands on one, a body in one is not raised in place, and a hostile the party could only reach across one is not
// pulled on the way. A box has to be found first; a bot that dies at full health with no fight on it (the feed carries no environmental
// damage: lava, slime and falls kill without a line in it) marks its spot as deadly for the rest of the run, so that the rejoin (the wipe's
// recovery included) does not summon the party onto the spot that has just killed it (rfc-live-1e r1: the lead bot stood in the lava and the summoned bots
// died there within 2 s, three times).

// AvoidMarker is the optional part of a RemoteGame that marks a plan's avoid boxes on the navmesh its PathPlanner and FloorSnapper use, so
// that the paths and the floor snaps keep out of them (navmesh.Planner.SetAvoid). Without it the boxes still hold for the plan check, the
// hops, the summons and the resurrections, but a path may cross one.
type AvoidMarker interface {
	MarkAvoid(mapID uint32, boxes []AvoidBox)
}

const (
	// deadlyFullPct and deadlyFrameGap: a death is silent when the bot's last living frame, at most deadlyFrameGap before it, showed
	// deadlyFullPct of its health or more, or showed it out of combat. Lava deals 605-610 a tick (rfc-live-1e: 532 of 532 to dead in one frame).
	deadlyFullPct  = 80
	deadlyFrameGap = 3 * time.Second
	// deadlySpotYd and deadlySpotDZ: how near a silent death's spot (across, and up or down) a summon, a resurrection or a stand is refused.
	deadlySpotYd = 6.0
	deadlySpotDZ = 4.0
	// leadOffYd is the farthest the lead bot is moved off a deadly spot before a summon (to the nearest safe floor).
	leadOffYd = 20.0
)

// deadlyState is the driver's memory of the deadly spots and of the members' last living frames.
type deadlyState struct {
	last  map[packet.GUID]aliveFrame
	spots []deadlySpot
	// safeLead is the lead bot's last position on safe ground (not in an avoid box, not on a deadly spot), and its map.
	safeLead    Point
	safeLeadMap uint32
	haveSafe    bool
	noted       map[string]bool
}

type aliveFrame struct {
	hp, max uint32
	combat  bool
	at      time.Time
}

type deadlySpot struct {
	at   Point
	mapI uint32
	who  string
}

func (s *deadlyState) once(key string) bool {
	if s.noted == nil {
		s.noted = map[string]bool{}
	}
	if s.noted[key] {
		return false
	}
	s.noted[key] = true
	return true
}

// markAvoid tells the game's planner the plan's avoid boxes (none clears earlier ones).
func (d *RemoteDriver) markAvoid() {
	if m, ok := d.g.(AvoidMarker); ok {
		m.MarkAvoid(d.plan.Map, d.plan.Avoid)
	}
	if len(d.plan.Avoid) > 0 && d.deadly.once("avoid") {
		d.rep.note(fmt.Sprintf("avoid: %d region(s) of the map are kept out of (%s): no walk, stand, summon or resurrection in them", len(d.plan.Avoid), avoidNames(d.plan.Avoid)))
	}
}

func avoidNames(bs []AvoidBox) string {
	s := ""
	for i, b := range bs {
		if i > 0 {
			s += ", "
		}
		s += b.Name
	}
	return s
}

// stepAvoid is the avoid boxes of the map the lead bot walks on: the plan's inside the dungeon, none outside it.
func (d *RemoteDriver) stepAvoid() []AvoidBox {
	if d.outside != nil {
		return nil
	}
	return d.plan.Avoid
}

// observeDeadly records a member's frame: its last living one, and, when it has just died (died), whether the death was silent, in which
// case its spot is deadly from now on. It also keeps the lead bot's last safe position.
func (d *RemoteDriver) observeDeadly(p tbm.PartyRec, died bool) {
	st := &d.deadly
	if st.last == nil {
		st.last = map[packet.GUID]aliveFrame{}
	}
	now := d.clk.Now()
	if !p.Dead {
		st.last[p.GUID] = aliveFrame{hp: p.HP, max: p.MaxHP, combat: p.InCombat(), at: now}
		if p.GUID == d.fr.Lead {
			if _, bad := d.deadlyAt(partyPoint(p), p.Map); !bad {
				st.safeLead, st.safeLeadMap, st.haveSafe = partyPoint(p), p.Map, true
			}
		}
		return
	}
	if !died {
		return
	}
	lf, ok := st.last[p.GUID]
	if !ok || now.Sub(lf.at) > deadlyFrameGap {
		return
	}
	full := lf.max > 0 && uint64(lf.hp)*100 >= uint64(lf.max)*deadlyFullPct
	if !full && lf.combat {
		return // a death in a fight that had worn the bot down: the fight killed it
	}
	at := partyPoint(p)
	st.spots = append(st.spots, deadlySpot{at: at, mapI: p.Map, who: p.Name})
	why := "out of combat"
	if full {
		why = fmt.Sprintf("at %d%% health one frame before", pctOf32(lf.hp, lf.max))
	}
	inBox := ""
	if b, ok := d.plan.InAvoid(at); ok && p.Map == d.plan.Map {
		inBox = fmt.Sprintf(" (in the plan's avoid box %q)", b.Name)
	}
	d.tr.Event("silent_death", map[string]any{"name": p.Name, "leg": d.curLeg, "x": at.X, "y": at.Y, "z": at.Z, "map": p.Map, "full": full, "combat": lf.combat})
	d.rep.note(fmt.Sprintf("leg %d: %s died at %.1f %.1f %.1f %s with no fight wearing it down%s: ground that kills without a line in the feed (lava, slime, a fall); no bot is summoned to the lead bot within %.0f yd of it for the rest of the run",
		d.curLeg, p.Name, at.X, at.Y, at.Z, why, inBox, deadlySpotYd))
}

func pctOf32(v, m uint32) int {
	if m == 0 {
		return 100
	}
	return int(uint64(v) * 100 / uint64(m))
}

// deadlyAt says whether a point of map m is deadly ground: in one of the plan's avoid boxes (on the plan's map) or near a silent death's spot.
func (d *RemoteDriver) deadlyAt(pt Point, m uint32) (string, bool) {
	if m == d.plan.Map {
		if b, ok := d.plan.InAvoid(pt); ok {
			return fmt.Sprintf("in the avoid box %q", b.Name), true
		}
	}
	for _, s := range d.deadly.spots {
		if s.mapI == m && hdist(s.at, pt) <= deadlySpotYd && math.Abs(float64(s.at.Z-pt.Z)) <= deadlySpotDZ {
			return fmt.Sprintf("%.0f yd from where %s died with no wound", hdist(s.at, pt), s.who), true
		}
	}
	return "", false
}

// overshootDeadly says whether a tiny move's overshoot (min_move+1 yd along the line from here through wp) would end on deadly ground: the
// move is then sent to wp itself.
func (d *RemoteDriver) overshootDeadly(ctx context.Context, here, wp Point, dd float64) bool {
	if len(d.stepAvoid()) == 0 && len(d.deadly.spots) == 0 {
		return false
	}
	past := d.onFloor(ctx, lerp(here, wp, (float64(d.tx.MinMove)+1)/dd), min(here.Z, wp.Z)-floorBand, max(here.Z, wp.Z))
	_, bad := d.deadlyAt(past, d.stepMap())
	return bad
}

// leadSafeForSummon makes sure the lead bot does not stand on deadly ground before a summon brings a bot to it: a lead bot on such ground is
// sent to the nearest safe floor (the planner's floor snap, which keeps out of the avoid boxes) or back to its last safe position, and the summon
// waits until it stands there. It reports whether the lead bot stands on safe ground; false means no summon is to be sent now.
func (d *RemoteDriver) leadSafeForSummon(ctx context.Context) (bool, error) {
	l, ok := d.fr.LeadRec()
	if !ok {
		return false, nil
	}
	here := partyPoint(l)
	why, bad := d.deadlyAt(here, l.Map)
	if !bad {
		return true, nil
	}
	target, found := d.safeFloorNear(ctx, here, l.Map)
	key := fmt.Sprintf("lead-%d-%.0f-%.0f", d.curLeg, here.X, here.Y)
	if !found {
		if d.deadly.once(key) {
			d.rep.note(fmt.Sprintf("leg %d: rejoin: the lead bot stands at %.1f %.1f %.1f, %s, and no safe floor is known near it: no bot is summoned onto it", d.curLeg, here.X, here.Y, here.Z, why))
		}
		return false, nil
	}
	if d.deadly.once(key) {
		d.rep.note(fmt.Sprintf("leg %d: rejoin: the lead bot stands at %.1f %.1f %.1f, %s; it is moved to %.1f %.1f %.1f before any bot is summoned to it", d.curLeg, here.X, here.Y, here.Z, why, target.X, target.Y, target.Z))
	}
	d.tr.Event("lead_off_deadly", map[string]any{"leg": d.curLeg, "x": here.X, "y": here.Y, "z": here.Z, "to_x": target.X, "to_y": target.Y, "to_z": target.Z, "why": why})
	if _, err := d.order(ctx, "move", moveArgs(target)...); err != nil {
		return false, err
	}
	until := d.clk.Now().Add(rejoinLand)
	for d.clk.Now().Before(until) {
		if err := d.tick(ctx, d.cfg.Poll); err != nil {
			return false, err
		}
		if l, ok := d.fr.LeadRec(); ok {
			if _, bad := d.deadlyAt(partyPoint(l), l.Map); !bad {
				return true, nil
			}
		}
	}
	return false, nil
}

// safeFloorNear is a safe point near p for the lead bot: the planner's nearest floor (which keeps out of the avoid boxes it was told) when it
// is not deadly and within leadOffYd, else the lead bot's last safe position on the same map within leadOffYd.
func (d *RemoteDriver) safeFloorNear(ctx context.Context, p Point, m uint32) (Point, bool) {
	if fs, ok := d.g.(FloorSnapper); ok {
		if q, ok := fs.SnapToFloor(ctx, m, p); ok && dist3(p, q) <= leadOffYd {
			if _, bad := d.deadlyAt(q, m); !bad {
				return q, true
			}
		}
	}
	if st := &d.deadly; st.haveSafe && st.safeLeadMap == m && dist3(p, st.safeLead) <= leadOffYd {
		if _, bad := d.deadlyAt(st.safeLead, m); !bad {
			return st.safeLead, true
		}
	}
	return Point{}, false
}

// corpsesOnDeadly is the dead members whose bodies lie in one of the plan's avoid boxes: a resurrection in place would raise them into what
// killed them. A silent death's spot alone does not stop a resurrection (a bot can drop dead out of a fight for other reasons, and a raise
// that fails costs one more death, which the revive caps bound); it stops summons only.
func (d *RemoteDriver) corpsesOnDeadly(dead []tbm.PartyRec) []string {
	var out []string
	for _, m := range dead {
		if m.Map != d.plan.Map {
			continue
		}
		if b, in := d.plan.InAvoid(partyPoint(m)); in {
			out = append(out, fmt.Sprintf("%s in the avoid box %q", m.Name, b.Name))
		}
	}
	return out
}

// aheadAcrossAvoid says whether a hostile near the walk's next hop could only be reached across an avoid box: the planner's path from the
// lead bot to it (which keeps out of the boxes) is missing, or a detour (longer than pullDetourFactor times the straight line plus
// pullDetourSlack), or the straight line itself crosses a box near its height when the game plans no path. The tank does not walk the
// planner's path to a pull: the server's own, which knows no box, takes it straight across (rfc-live-1e: the terrace pair under the ramp was
// pulled on the way and the tank ran through the lava basin to it, the party after it). Only for a plan with avoid boxes.
func (d *RemoteDriver) aheadAcrossAvoid(here, at Point) bool {
	boxes := d.stepAvoid()
	if len(boxes) == 0 {
		return false
	}
	straight := dist3(here, at)
	if pl, ok := d.g.(PathPlanner); ok {
		pts, err := pl.PlanPath(context.Background(), d.stepMap(), here, at)
		if err != nil || len(pts) < 2 {
			return true
		}
		if dist3(pts[len(pts)-1], at) > 5 {
			return true
		}
		length := 0.0
		for i := 1; i < len(pts); i++ {
			length += dist3(pts[i-1], pts[i])
		}
		return length > pullDetourFactor*straight+pullDetourSlack
	}
	return chordNearAvoid(here, at, boxes)
}
