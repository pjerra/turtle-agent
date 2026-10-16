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
	// maxLeadFallbacks is how many fallback walks a leg may make; historyStep and historyMax shape the lead bot's history.
	maxLeadFallbacks = 1
	historyStep      = 5.0
	historyMax       = 400
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
	// history is where the lead bot stood alive on safe ground, oldest first, a point every historyStep yards (at most historyMax);
	// fallbacks counts the fallback walks per leg.
	history   []histPoint
	fallbacks map[int]int
	// progress is the run's place in the plan: leg*progressLeg plus the count of distinct actions taken in the leg (setProgress). progLeg
	// and progLabel are the action it was last set from.
	progress  int
	progLeg   int
	progLabel string
	noted     map[string]bool
}

type histPoint struct {
	at       Point
	m        uint32
	progress int // the run's progress when the lead bot stood there (setProgress)
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
				if n := len(st.history); n == 0 || st.history[n-1].m != p.Map || dist3(st.history[n-1].at, partyPoint(p)) >= historyStep {
					st.history = append(st.history, histPoint{at: partyPoint(p), m: p.Map, progress: st.progress})
					if len(st.history) > historyMax {
						st.history = st.history[len(st.history)-historyMax:]
					}
				}
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
// leadOffTries times; after that it walks back to where it last stood and lived (leadFallback, once per leg), and when that cannot be
// done the step fails with errLeadStuck: the recovery never loops on a lead bot that cannot get out. It reports whether the lead bot stands on safe ground; false means no
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

// progressLeg separates the legs in deadlyState.progress.
const progressLeg = 1_000_000

// setProgress records the run's place from the action the run loop takes: a new leg starts at its own count, a new action of the leg
// counts one more, the same action again (a retry) does not. A place behind the last one (the run was put back) drops the history ahead of it.
func (d *RemoteDriver) setProgress(a Action) {
	st := &d.deadly
	if a.Kind == ActFailed || a.Kind == ActWait || a.Kind == ActDone {
		return
	}
	n := 0
	switch {
	case a.Leg != st.progLeg || st.progLabel == "":
		n = 0
	case a.Label != st.progLabel:
		n = st.progress%progressLeg + 1
	default:
		return
	}
	p := a.Leg*progressLeg + n
	st.progLeg, st.progLabel = a.Leg, a.Label
	if p < st.progress {
		d.dropHistory(func(h histPoint) bool { return h.progress > p }, "the run went back")
	}
	st.progress = p
}

// rollbackHistory drops the places the lead bot stood on during the step now under way: the step ended in a wipe, so the ground it reached
// in it is not known safe (the pack that killed the party may stand there).
func (d *RemoteDriver) rollbackHistory() {
	p := d.deadly.progress
	d.dropHistory(func(h histPoint) bool { return h.progress >= p }, "the step under way ended in a wipe")
}

func (d *RemoteDriver) dropHistory(drop func(histPoint) bool, why string) {
	st := &d.deadly
	keep := st.history[:0]
	n := 0
	for _, h := range st.history {
		if drop(h) {
			n++
			continue
		}
		keep = append(keep, h)
	}
	st.history = keep
	if n > 0 {
		st.haveSafe = false // the last safe position may be one of them
		d.rep.note(fmt.Sprintf("leg %d: %d place(s) of the lead bot's safe history dropped: %s", d.curLeg, n, why))
	}
}

// errLeadStuck is the end of a recovery whose lead bot stands on deadly ground and cannot be brought off it: no safe stand with a safe
// route, or the one fallback walk of the leg failed. The step fails with it (the run's normal failure path) instead of looping.
var errLeadStuck = errors.New("the lead bot is stuck on deadly ground")

// leadFallback walks the lead bot to the last place it stood and lived (fallbackStand: fallbackClearYd clear of every box and spot, by a
// route that crosses none), once per leg. A second need in the same leg, no such place, or a walk that does not end on safe ground fails
// the step with errLeadStuck.
func (d *RemoteDriver) leadFallback(ctx context.Context, here Point, why string) (bool, error) {
	st := &d.deadly
	if st.fallbacks == nil {
		st.fallbacks = map[int]int{}
	}
	if st.fallbacks[d.curLeg] >= maxLeadFallbacks {
		return false, fmt.Errorf("%w: at %.1f %.1f %.1f (%s), and leg %d's walk to a safe stand was used already", errLeadStuck, here.X, here.Y, here.Z, why, d.curLeg)
	}
	st.fallbacks[d.curLeg]++
	target, reason, ok := d.fallbackStand(ctx, here)
	if !ok {
		return false, fmt.Errorf("%w: at %.1f %.1f %.1f (%s), and %s", errLeadStuck, here.X, here.Y, here.Z, why, reason)
	}
	d.rep.note(fmt.Sprintf("leg %d: rejoin: the lead bot did not get off deadly ground (%s) in %d tries; it walks back to %.1f %.1f %.1f, where it stood and lived, by a route clear of every avoid box and deadly spot, then the party is summoned there", d.curLeg, why, leadOffTries, target.X, target.Y, target.Z))
	d.tr.Event("lead_off_deadly", map[string]any{"leg": d.curLeg, "x": here.X, "y": here.Y, "z": here.Z, "to_x": target.X, "to_y": target.Y, "to_z": target.Z, "why": why, "fallback": true})
	st.falling = true
	err := d.walkArrive(ctx, target, d.opt.MoveArrive)
	st.falling = false
	if err != nil {
		if ctx.Err() != nil || errors.Is(err, errWipe) || errors.Is(err, errBindingLost) || errors.Is(err, errFeedLost) {
			return false, err
		}
		return false, fmt.Errorf("%w: the walk back to %.1f %.1f %.1f failed: %v", errLeadStuck, target.X, target.Y, target.Z, err)
	}
	d.look()
	if l, ok := d.fr.LeadRec(); ok {
		if _, bad := d.deadlyAt(partyPoint(l), l.Map); !bad {
			st.leadTries = 0
			return true, nil
		}
	}
	return false, fmt.Errorf("%w: the walk back to %.1f %.1f %.1f did not leave the lead bot on safe ground", errLeadStuck, target.X, target.Y, target.Z)
}

// fallbackStand is where the stuck lead bot goes: the most recent place in its history (positions it stood on alive, safe at the time,
// behind the run's progress: never a point ahead past packs not yet cleared) that lies fallbackClearYd or more clear of every avoid box and
// deadly spot, and to which the planner's route (which keeps out of the boxes) is complete and passes no deadly spot and no box. Without a
// planner the straight line must. reason says why there is none.
func (d *RemoteDriver) fallbackStand(ctx context.Context, from Point) (Point, string, bool) {
	hist := d.deadly.history
	if len(hist) == 0 {
		return Point{}, "it has stood on no safe ground in this run", false
	}
	clear := 0
	for i := len(hist) - 1; i >= 0; i-- {
		c := hist[i]
		if c.progress > d.deadly.progress {
			continue // ahead of where the run is now (it was put back): the ground past it is not known safe
		}
		if c.m != d.plan.Map || !d.clearOfDeadly(c.at, fallbackClearYd) {
			continue
		}
		clear++
		route := []Point{from, c.at}
		if pl, ok := d.g.(PathPlanner); ok {
			pts, err := pl.PlanPath(ctx, d.plan.Map, from, c.at)
			if err != nil || len(pts) < 2 || dist3(pts[len(pts)-1], c.at) > 5 {
				continue
			}
			route = pts
		}
		if d.routeSafe(route) {
			return c.at, "", true
		}
	}
	if clear == 0 {
		return Point{}, fmt.Sprintf("none of the %d places it stood on is %.0f yd clear of the avoid boxes and deadly spots", len(hist), fallbackClearYd), false
	}
	return Point{}, fmt.Sprintf("no safe route leads to any of the %d places it stood on that are clear (each crosses an avoid box or a deadly spot, or has no path)", clear), false
}

// routeSafe says whether a route keeps out of every avoid box and every deadly spot, exactly: each segment is tested against each box
// (segmentBox, the slab test) and against each spot (its distance to the spot's centre, deadlySpotYd). The route's first segment may start
// on the deadly ground the lead bot stands on, but only to leave it: out of a box without going deeper into it (leavesBox), away from a
// spot's centre, and never into another box or spot.
func (d *RemoteDriver) routeSafe(route []Point) bool {
	for i := 1; i < len(route); i++ {
		a, b := route[i-1], route[i]
		for _, s := range d.deadly.spots {
			if s.mapI != d.plan.Map || distToSegment(s.at, a, b) >= deadlySpotYd {
				continue
			}
			// Starting on the spot: allowed only straight away from its centre (the distance then only grows).
			if i == 1 && hdist(s.at, a) < deadlySpotYd && math.Abs(float64(s.at.Z-a.Z)) <= deadlySpotDZ &&
				float64(b.X-a.X)*float64(a.X-s.at.X)+float64(b.Y-a.Y)*float64(a.Y-s.at.Y) >= 0 {
				continue
			}
			return false
		}
		for _, bx := range d.plan.Avoid {
			if _, _, hit := segmentBox(a, b, bx); !hit {
				continue
			}
			if i == 1 && bx.Contains(a) && leavesBox(a, b, bx) {
				continue
			}
			return false
		}
	}
	return true
}

// segmentBox is the slab test of the segment a-b against the box: whether it meets it, and the fractions of the way where it enters and
// leaves (clipped to 0..1).
func segmentBox(a, b Point, bx AvoidBox) (enter, exit float64, hit bool) {
	enter, exit = 0, 1
	for _, ax := range [3]struct{ a, b, lo, hi float32 }{{a.X, b.X, bx.Min.X, bx.Max.X}, {a.Y, b.Y, bx.Min.Y, bx.Max.Y}, {a.Z, b.Z, bx.Min.Z, bx.Max.Z}} {
		d := float64(ax.b - ax.a)
		if d == 0 {
			if ax.a < ax.lo || ax.a > ax.hi {
				return 0, 0, false
			}
			continue
		}
		t1, t2 := float64(ax.lo-ax.a)/d, float64(ax.hi-ax.a)/d
		if t1 > t2 {
			t1, t2 = t2, t1
		}
		enter, exit = max(enter, t1), min(exit, t2)
		if enter > exit {
			return 0, 0, false
		}
	}
	return enter, exit, true
}

// leavesBox says whether a segment that starts inside the box only leaves it: it gets out within leadOffYd, and its depth across the box
// (the distance to the nearest side, in x and y) never grows on the way out. The depth is the least of four linear functions along the
// segment, so it never grows once it does not grow at the start.
func leavesBox(a, b Point, bx AvoidBox) bool {
	_, exit, hit := segmentBox(a, b, bx)
	if !hit || exit >= 1 || exit*dist3(a, b) > leadOffYd {
		return false // the route ends in the box, or stays in it too long
	}
	dx, dy := float64(b.X-a.X), float64(b.Y-a.Y)
	sides := [4]struct{ dist, rate float64 }{
		{float64(a.X - bx.Min.X), dx}, {float64(bx.Max.X - a.X), -dx}, {float64(a.Y - bx.Min.Y), dy}, {float64(bx.Max.Y - a.Y), -dy},
	}
	least := math.Inf(1)
	for _, s := range sides {
		least = math.Min(least, s.dist)
	}
	rate := math.Inf(1) // the depth's rate at the start: the least rate among the nearest sides
	for _, s := range sides {
		if s.dist <= least+1e-3 {
			rate = math.Min(rate, s.rate)
		}
	}
	return rate < 0
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
