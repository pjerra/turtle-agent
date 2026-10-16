package dungeon

import (
	"context"
	"errors"
	"fmt"
	"math"
	"time"

	"github.com/pjerra/turtle-agent/internal/packet"
	"github.com/pjerra/turtle-agent/internal/tbm"
)

// The remote driver's side of the avoid boxes (avoid.go): the planner is told the plan's boxes, a hop never takes a straight line over
// one, a summon never lands on one, a body in one is not raised in place, and a hostile the party could only reach across one is not
// pulled on the way. Ground no box marks yet is caught from the deaths: a bot that dies out of combat at full health from one frame to the
// next (the feed carries no environmental damage: lava, slime and falls kill without a line in it) is a silent death, and two within 6 yd
// make a deadly spot that gets no summon for 5 min (rfc-live-1e r1: the lead bot stood in the lava and the summoned bots died there within
// 2 s, three times). A lead bot on deadly ground is moved off it before a summon, and walked to the plan's last safe stand when it cannot be.

// AvoidMarker is the optional part of a RemoteGame that marks a plan's avoid boxes on the navmesh its PathPlanner and FloorSnapper use, so
// that the paths and the floor snaps keep out of them (navmesh.Planner.SetAvoid). Without it the boxes still hold for the plan check, the
// hops, the summons and the resurrections, but a path may cross one.
type AvoidMarker interface {
	MarkAvoid(mapID uint32, boxes []AvoidBox)
}

const (
	// deadlyFullPct and deadlyFrameGap: a death is silent when the bot's last living frame, at most deadlyFrameGap before it (one feed
	// frame), showed it out of combat with deadlyFullPct of its health or more. Lava deals 605-610 a tick (rfc-live-1e: 532 of 532 to dead in
	// one frame).
	deadlyFullPct  = 80
	deadlyFrameGap = 2500 * time.Millisecond
	// deadlySpotYd and deadlySpotDZ: how near (across, and up or down) two silent deaths make a deadly spot, and how near a deadly spot a
	// summon is refused. One silent death is no spot: it takes a second within this distance, or the plan's avoid box.
	deadlySpotYd = 6.0
	deadlySpotDZ = 4.0
	// deadlySpotTTL: a deadly spot (and a lone silent death waiting for a second) is forgotten this long after its last death.
	deadlySpotTTL = 5 * time.Minute
	// deadlyProbeYd and deadlyProbe: a living member that stands within deadlyProbeYd of a deadly spot for deadlyProbe and lives clears it.
	deadlyProbeYd = 3.0
	deadlyProbe   = 5 * time.Second
	// leadOffYd is the farthest the lead bot is moved off deadly ground before a summon (to the nearest safe floor); leadOffTries such
	// moves are made before the fallback walk to the plan's last safe stand, which must lie fallbackClearYd or more outside every box and spot.
	leadOffYd       = 20.0
	leadOffTries    = 2
	fallbackClearYd = 10.0
)

// deadlyState is the driver's memory of the deadly spots and of the members' last living frames.
type deadlyState struct {
	last  map[packet.GUID]aliveFrame
	spots []deadlySpot // two silent deaths within deadlySpotYd: summons refused near it
	lone  []deadlySpot // a silent death with no second one yet
	// safeLead is the lead bot's last position on safe ground (not in an avoid box, not on a deadly spot), and its map.
	safeLead    Point
	safeLeadMap uint32
	haveSafe    bool
	// leadTries counts the moves off deadly ground since the lead bot last stood on safe ground; falling is set while the fallback walk runs.
	leadTries int
	falling   bool
	noted     map[string]bool
}

type aliveFrame struct {
	hp, max uint32
	combat  bool
	at      time.Time
}

type deadlySpot struct {
	at    Point
	mapI  uint32
	who   string
	last  time.Time // the last death there
	probe time.Time // since when a living member stands on it (zero: none)
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

// observeDeadly records a member's frame: its last living one, and, when it has just died (died), whether the death was silent (out of
// combat, at deadlyFullPct of its health or more one frame before). A silent death inside a plan's avoid box is only noted (the box holds
// already); one outside waits for a second within deadlySpotYd, which makes the spot deadly for summons. It also keeps the lead bot's last
// safe position.
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
				st.safeLead, st.safeLeadMap, st.haveSafe, st.leadTries = partyPoint(p), p.Map, true, 0
			}
		}
		return
	}
	if !died {
		return
	}
	lf, ok := st.last[p.GUID]
	if !ok || now.Sub(lf.at) > deadlyFrameGap || lf.combat || lf.max == 0 || uint64(lf.hp)*100 < uint64(lf.max)*deadlyFullPct {
		return // a death in a fight, or from low health: the feed shows what killed it, or nothing says the ground did
	}
	at := partyPoint(p)
	ev := map[string]any{"name": p.Name, "leg": d.curLeg, "x": at.X, "y": at.Y, "z": at.Z, "map": p.Map, "hp_pct": pctOf32(lf.hp, lf.max)}
	head := fmt.Sprintf("leg %d: %s died at %.1f %.1f %.1f out of combat, at %d%% health one frame before (the feed has no line for lava, slime or a fall)", d.curLeg, p.Name, at.X, at.Y, at.Z, pctOf32(lf.hp, lf.max))
	if b, in := d.plan.InAvoid(at); in && p.Map == d.plan.Map {
		ev["spot"] = "avoid_box"
		d.tr.Event("silent_death", ev)
		d.rep.note(fmt.Sprintf("%s, in the plan's avoid box %q", head, b.Name))
		return
	}
	for i, l := range st.lone {
		if l.mapI == p.Map && hdist(l.at, at) <= deadlySpotYd && math.Abs(float64(l.at.Z-at.Z)) <= deadlySpotDZ {
			st.lone = append(st.lone[:i], st.lone[i+1:]...)
			st.spots = append(st.spots, deadlySpot{at: at, mapI: p.Map, who: l.who + " and " + p.Name, last: now})
			ev["spot"] = "deadly"
			d.tr.Event("silent_death", ev)
			d.rep.note(fmt.Sprintf("%s, %.0f yd from where %s died the same way: a deadly spot; no bot is summoned to the lead bot within %.0f yd of it for %.0f min (or until a bot stands on it %.0f s and lives)",
				head, hdist(l.at, at), l.who, deadlySpotYd, deadlySpotTTL.Minutes(), deadlyProbe.Seconds()))
			return
		}
	}
	st.lone = append(st.lone, deadlySpot{at: at, mapI: p.Map, who: p.Name, last: now})
	ev["spot"] = "lone"
	d.tr.Event("silent_death", ev)
	d.rep.note(head + "; one such death is no evidence yet: a second within 6 yd makes the spot deadly for summons")
}

// deadlyFrame runs once per frame after the members are observed: it forgets the spots and lone deaths older than deadlySpotTTL, and clears a
// spot a living member has stood on for deadlyProbe.
func (d *RemoteDriver) deadlyFrame() {
	st := &d.deadly
	now := d.clk.Now()
	keep := func(ss []deadlySpot) []deadlySpot {
		out := ss[:0]
		for _, s := range ss {
			if now.Sub(s.last) < deadlySpotTTL {
				out = append(out, s)
			}
		}
		return out
	}
	st.lone = keep(st.lone)
	n := len(st.spots)
	st.spots = keep(st.spots)
	if len(st.spots) < n {
		d.rep.note(fmt.Sprintf("leg %d: %d deadly spot(s) forgotten %.0f min after their last death", d.curLeg, n-len(st.spots), deadlySpotTTL.Minutes()))
	}
	out := st.spots[:0]
	for _, s := range st.spots {
		on := ""
		for _, p := range d.fr.Party {
			if !p.Dead && p.Map == s.mapI && hdist(partyPoint(p), s.at) <= deadlyProbeYd && math.Abs(float64(p.Z-s.at.Z)) <= deadlySpotDZ/2 {
				on = p.Name
				break
			}
		}
		switch {
		case on == "":
			s.probe = time.Time{}
		case s.probe.IsZero():
			s.probe = now
		case now.Sub(s.probe) >= deadlyProbe:
			d.rep.note(fmt.Sprintf("leg %d: %s has stood on the deadly spot at %.1f %.1f %.1f for %.0f s and lives: the spot is cleared", d.curLeg, on, s.at.X, s.at.Y, s.at.Z, now.Sub(s.probe).Seconds()))
			continue
		}
		out = append(out, s)
	}
	st.spots = out
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
			return fmt.Sprintf("%.0f yd from the deadly spot where %s died with no wound", hdist(s.at, pt), s.who), true
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

// leadSafeForSummon makes sure the lead bot does not stand on deadly ground before a summon brings a bot to it. A lead bot on such ground is
// sent to the nearest safe floor (the planner's floor snap, which keeps out of the avoid boxes, or its last safe position), up to
// leadOffTries times; after that it walks to the plan's last safe stand (fallbackStand), fallbackClearYd or more outside every box and spot,
// so the recovery never waits on a lead bot that cannot get out. It reports whether the lead bot stands on safe ground; false means no
// summon now (the call after tries again).
func (d *RemoteDriver) leadSafeForSummon(ctx context.Context) (bool, error) {
	l, ok := d.fr.LeadRec()
	if !ok {
		return false, nil
	}
	st := &d.deadly
	here := partyPoint(l)
	why, bad := d.deadlyAt(here, l.Map)
	if !bad {
		st.leadTries = 0
		return true, nil
	}
	if st.leadTries >= leadOffTries {
		return d.leadFallback(ctx, here, why)
	}
	st.leadTries++
	target, found := d.safeFloorNear(ctx, here, l.Map)
	if !found {
		d.rep.note(fmt.Sprintf("leg %d: rejoin: the lead bot stands at %.1f %.1f %.1f, %s, and no safe floor is known near it: no bot is summoned onto it (try %d of %d before the walk to the last safe stand)", d.curLeg, here.X, here.Y, here.Z, why, st.leadTries, leadOffTries))
		return false, nil
	}
	d.rep.note(fmt.Sprintf("leg %d: rejoin: the lead bot stands at %.1f %.1f %.1f, %s; it is moved to %.1f %.1f %.1f before any bot is summoned to it (try %d of %d)", d.curLeg, here.X, here.Y, here.Z, why, target.X, target.Y, target.Z, st.leadTries, leadOffTries))
	d.tr.Event("lead_off_deadly", map[string]any{"leg": d.curLeg, "x": here.X, "y": here.Y, "z": here.Z, "to_x": target.X, "to_y": target.Y, "to_z": target.Z, "why": why, "try": st.leadTries})
	if _, err := d.order(ctx, "move", moveArgs(target)...); err != nil {
		return false, err
	}
	return d.waitLeadSafe(ctx, rejoinLand)
}

// waitLeadSafe waits up to dur for the lead bot to stand on safe ground.
func (d *RemoteDriver) waitLeadSafe(ctx context.Context, dur time.Duration) (bool, error) {
	until := d.clk.Now().Add(dur)
	for d.clk.Now().Before(until) {
		if err := d.tick(ctx, d.cfg.Poll); err != nil {
			return false, err
		}
		if l, ok := d.fr.LeadRec(); ok {
			if _, bad := d.deadlyAt(partyPoint(l), l.Map); !bad {
				d.deadly.leadTries = 0
				return true, nil
			}
		}
	}
	return false, nil
}

// leadFallback walks the lead bot to the plan's last safe stand after the moves off deadly ground failed. The walk is the run's own (the
// planner's path, which keeps out of the boxes); the rejoin is held while it runs. A walk that fails for another reason than the run's end
// is noted and the tries start over.
func (d *RemoteDriver) leadFallback(ctx context.Context, here Point, why string) (bool, error) {
	st := &d.deadly
	st.leadTries = 0
	target, ok := d.fallbackStand()
	if !ok {
		d.rep.note(fmt.Sprintf("leg %d: rejoin: the lead bot stands at %.1f %.1f %.1f, %s, and the plan has no stand %.0f yd clear of every avoid box and deadly spot: no summon", d.curLeg, here.X, here.Y, here.Z, why, fallbackClearYd))
		return false, nil
	}
	d.rep.note(fmt.Sprintf("leg %d: rejoin: the lead bot did not get off deadly ground (%s) in %d tries; it walks to the plan's last safe stand %.1f %.1f %.1f, then the party is summoned there", d.curLeg, why, leadOffTries, target.X, target.Y, target.Z))
	d.tr.Event("lead_off_deadly", map[string]any{"leg": d.curLeg, "x": here.X, "y": here.Y, "z": here.Z, "to_x": target.X, "to_y": target.Y, "to_z": target.Z, "why": why, "fallback": true})
	st.falling = true
	err := d.walkArrive(ctx, target, d.opt.MoveArrive)
	st.falling = false
	if err != nil {
		if ctx.Err() != nil || errors.Is(err, errWipe) || errors.Is(err, errBindingLost) || errors.Is(err, errFeedLost) {
			return false, err
		}
		d.rep.note(fmt.Sprintf("leg %d: rejoin: the walk to the last safe stand failed: %v", d.curLeg, err))
		return false, nil
	}
	d.look()
	if l, ok := d.fr.LeadRec(); ok {
		if _, bad := d.deadlyAt(partyPoint(l), l.Map); !bad {
			return true, nil
		}
	}
	return false, nil
}

// fallbackStand is the plan's last safe stand for the lead bot: the current leg's `to` and via points, then those of the legs before it,
// back to the entrance, the first that lies fallbackClearYd or more outside every avoid box and deadly spot.
func (d *RemoteDriver) fallbackStand() (Point, bool) {
	var cands []Point
	for i := min(max(d.curLeg, 0), len(d.plan.Legs)-1); i >= 0; i-- {
		l := d.plan.Legs[i]
		cands = append(cands, l.To)
		for k := len(l.Via) - 1; k >= 0; k-- {
			if !l.Via[k].GMCross {
				cands = append(cands, l.Via[k].Point)
			}
		}
	}
	cands = append(cands, d.plan.Entrance)
	for _, c := range cands {
		if d.clearOfDeadly(c, fallbackClearYd) {
			return c, true
		}
	}
	return Point{}, false
}

// clearOfDeadly says whether p lies yd or more outside every avoid box (across; within its height band widened by yd) and every deadly spot.
func (d *RemoteDriver) clearOfDeadly(p Point, yd float64) bool {
	m := float32(yd)
	for _, b := range d.plan.Avoid {
		if p.X >= b.Min.X-m && p.X <= b.Max.X+m && p.Y >= b.Min.Y-m && p.Y <= b.Max.Y+m && p.Z >= b.Min.Z-m && p.Z <= b.Max.Z+m {
			return false
		}
	}
	for _, s := range d.deadly.spots {
		if s.mapI == d.plan.Map && hdist(s.at, p) < deadlySpotYd+yd && math.Abs(float64(s.at.Z-p.Z)) <= deadlySpotDZ+yd {
			return false
		}
	}
	return true
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
