package dungeon

import (
	"context"
	"fmt"
	"math"
	"slices"
	"sort"
	"strings"
	"time"

	"github.com/pjerra/turtle-agent/internal/packet"
	"github.com/pjerra/turtle-agent/internal/tbm"
)

// Patrols and reactive CC (Zul'Farrak hires-3, 2026-10-14: every run that got to the road at x 1650-1690 wiped there). The pack of three
// at 1680 871 was pulled from 1638 869; 10 to 25 s later the patrolling pair of Sandfury Shadowhunters (spawn 155626 + 44170, a loop of
// 45 waypoints through 1643-1707 874-930) and the camp beside its loop (a Soul Eater, two Shadowcasters, three Witch Doctors) joined the
// fight: twelve mobs on a level-46 tank, dead in 34 s. The CC was decided once, before the pull, on the three the bots counted then, so
// the Shadowhunter (cc_always) never got its Polymorph. Two rules come out of it:
//   - reactive CC: during a fight, a mob that joins it gets the cc bot's Polymorph at once when it is a cc_always entry, or when the fight
//     has grown to big_pack mobs and nothing is crowd-controlled; a CC that lapses while the fight goes on is put on again. Never on the
//     tank's target or the mob the party is sent at, never on one nearly dead, one order per ccFresh, at most maxCCPerMob per mob;
//   - patrols as plan data (PullGroup.Patrol: entries, path, period_s): before the group is pulled the driver looks for the patrol's
//     members; one whose path brings it within patrol_aggro of the pull target or the lead bot within patrol_window is a danger. One
//     that passes within patrol_pull_yd of the lead bot and patrol_aggro clear of the pack is pulled alone first, as its own pull with
//     the pack CC (at most maxPatrolPulls per group); otherwise the party waits for it to walk away, at most patrol_wait per group over
//     all its pulls (rule 3), then pulls with the patrol counted in the pack (withPatrols).

const (
	// PatrolAggro is how near (yards) a patrol member may come to a pull before it is taken to join it.
	PatrolAggro = 25
	// patrolWindow is how far ahead (along its path, at its pace) a patrol is looked at: about one pull.
	patrolWindow = 40 * time.Second
	// patrolWait bounds the waits of one pull group for its patrol.
	patrolWait = 90 * time.Second
	// patrolLocate bounds the look of one pull group for a patrol none of whose members the bots see (ZF-2 run 3i: the Shadowhunters were
	// out of view when the pull of 1642 902 began and joined it 30 s later, with the camp beside it: nine mobs, wipe). The group's patrol
	// is looked for up to this long, and never longer than the patrol's own round plus 10 s: a patrol that walks past the pull is seen
	// once in a round.
	patrolLocate = 120 * time.Second
	// patrolKnown is how long after a patrol member was seen walking the patrol counts as located for the next group of the leg.
	patrolKnown = 45 * time.Second
	// patrolPullYd is how near the lead bot a patrol member must pass to be pulled alone.
	patrolPullYd = 40
	// patrolCorridor is how far (yards) from its path a member of a patrol may stand and still be taken as walking it.
	patrolCorridor = 15
	// maxPatrolPulls bounds the patrol pulls of one group.
	maxPatrolPulls = 2
	// maxCCPerMob bounds the reactive CC orders at one mob.
	maxCCPerMob = 3
	// ccLowHP is the health (percent) below which a mob is not worth a CC: it is about to die.
	ccLowHP = 30
	// patrolStaticAfter is how long a unit of a patrol's entries on its path must have been seen standing still (within 3 yd) to be
	// taken for a mob that stands there (ZF: a static Shadowhunter stands on the patrol's loop at 1662 930), not a member of the patrol.
	patrolStaticAfter = 6 * time.Second
	// MinMove is the shortest move the module carries out (ZF-1 run 4: a move of 5.2 yd was ignored five times and failed the walk).
	MinMove = 6
)

// patrolState is what the patrol gate of one pull group has done.
type patrolState struct {
	waitBegan time.Time
	waiting   bool
	spent     time.Duration // the group's waits so far
	timedOut  bool
	pulls     int
	passed    map[packet.GUID]bool // members a patrol pull did not win against: not pulled again by this group
	// the look for a patrol out of view: located says a member was seen on its path (or one was seen in the last patrolKnown),
	// locating that the party is waiting to see one, locateDone that the look ran out unrewarded.
	located    bool
	locating   bool
	locateDone bool
	locateAt   time.Time
}

// pathLen is the length of the patrol's round: its path and the way back from the last point to the first.
func pathLen(path []Point) float64 {
	l := 0.0
	for i := range path {
		l += dist3(path[i], path[(i+1)%len(path)])
	}
	return l
}

// onPath is the segment of the round nearest to p (from path[i] to path[i+1], wrapping) and how far along it, in yards, and how far p is.
func onPath(path []Point, p Point) (seg int, along, off float64) {
	off = math.MaxFloat64
	for i := range path {
		a, b := path[i], path[(i+1)%len(path)]
		dd := distToSegment(p, a, b)
		if dd < off {
			l := dist3(a, b)
			t := 0.0
			if l > 0 {
				// the projection of p on the segment, in yards from a
				dot := float64(p.X-a.X)*float64(b.X-a.X) + float64(p.Y-a.Y)*float64(b.Y-a.Y) + float64(p.Z-a.Z)*float64(b.Z-a.Z)
				t = math.Max(0, math.Min(l, dot/l))
			}
			seg, along, off = i, t, dd
		}
	}
	return seg, along, off
}

// pathAhead are points every 2 yd along the round from (seg, along), for dist yards.
func pathAhead(path []Point, seg int, along, dist float64) []Point {
	var out []Point
	walked := 0.0
	for k := 0; k <= len(path) && walked <= dist; k++ {
		a, b := path[(seg+k)%len(path)], path[(seg+k+1)%len(path)]
		l := dist3(a, b)
		start := 0.0
		if k == 0 {
			start = along
		}
		for x := start; x <= l && walked <= dist; x += 2 {
			if l > 0 {
				out = append(out, lerp(a, b, x/l))
			}
			walked += 2
		}
	}
	return out
}

// patrolObs is what the driver has seen of one unit of a patrol's entries: where it was first seen, when, and whether it has moved since.
type patrolObs struct {
	at    Point
	since time.Time
	moved bool
}

// patrolMembers are the living hostiles of p's entries the bots see on or near its path, not fighting the party yet, that walk: a unit
// seen standing in one spot for patrolStaticAfter is a mob that stands on the path, not a member.
func (d *RemoteDriver) patrolMembers(p *Patrol) []tbm.HostileRec {
	if d.patrolSeen == nil {
		d.patrolSeen = map[packet.GUID]*patrolObs{}
	}
	now := d.clk.Now()
	var out []tbm.HostileRec
	for _, h := range d.fr.Hostiles {
		if h.Entry == 0 || h.HPPct <= 0 || !slices.Contains(p.Entries, h.Entry) || !h.Hostile() || h.Bystander() || h.Evading() || d.engagedWith(h) {
			continue
		}
		if _, refused := d.skipped[h.GUID]; refused {
			continue
		}
		if _, _, off := onPath(p.Path, hostilePoint(h)); off > patrolCorridor {
			continue
		}
		o := d.patrolSeen[h.GUID]
		if o == nil {
			o = &patrolObs{at: hostilePoint(h), since: now}
			d.patrolSeen[h.GUID] = o
		}
		if !o.moved && dist3(o.at, hostilePoint(h)) > 3 {
			o.moved = true
		}
		if !o.moved && now.Sub(o.since) >= patrolStaticAfter {
			continue
		}
		out = append(out, h)
	}
	return out
}

// patrolDanger says whether h, walking p, stands or will stand within PatrolAggro of one of at within PatrolWindow.
func (d *RemoteDriver) patrolDanger(p *Patrol, h tbm.HostileRec, at []Point) bool {
	near := func(q Point) bool {
		for _, a := range at {
			if dist3(q, a) <= float64(d.tx.PatrolAggro) {
				return true
			}
		}
		return false
	}
	if near(hostilePoint(h)) {
		return true
	}
	speed := pathLen(p.Path) / float64(p.PeriodS)
	seg, along, _ := onPath(p.Path, hostilePoint(h))
	for _, q := range pathAhead(p.Path, seg, along, speed*d.tx.PatrolWindow.Seconds()) {
		if near(q) {
			return true
		}
	}
	return false
}

// legPatrols are the patrols of the current leg's pull groups.
func (d *RemoteDriver) legPatrols() []*Patrol {
	if d.curLeg < 0 || d.curLeg >= len(d.plan.Legs) {
		return nil
	}
	var out []*Patrol
	for _, g := range d.plan.Legs[d.curLeg].PullGroups {
		if g.Patrol != nil && !slices.Contains(out, g.Patrol) {
			out = append(out, g.Patrol)
		}
	}
	return out
}

// withPatrols adds to pack the members of the leg's patrols that stand within PatrolAggro of at: they join the fight.
func (d *RemoteDriver) withPatrols(pack []tbm.HostileRec, at Point) []tbm.HostileRec {
	for _, p := range d.legPatrols() {
		for _, h := range d.patrolMembers(p) {
			if dist3(hostilePoint(h), at) <= float64(d.tx.PatrolAggro) && !slices.ContainsFunc(pack, func(o tbm.HostileRec) bool { return o.GUID == h.GUID }) {
				pack = append(pack, h)
			}
		}
	}
	return pack
}

// patrolGate runs before a pull of a group with a patrol (t is the pull target). It returns pulled when it fought (a patrol pulled alone,
// or the patrol came to the party during the wait): the caller picks its target again. It ends without a fight when no member of the
// patrol is a danger to the pull, or when the group's patrol wait ran out (noted; the pull goes on with the patrol counted in its pack).
func (d *RemoteDriver) patrolGate(ctx context.Context, t tbm.HostileRec, sp fightSpec) (pulled bool, err error) {
	p, st := sp.patrol, sp.patrolSt
	if p == nil || st == nil || st.timedOut {
		return false, nil
	}
	if st.passed == nil {
		st.passed = map[packet.GUID]bool{}
	}
	stop := func() {
		if st.waiting {
			el := d.clk.Now().Sub(st.waitBegan)
			st.spent += el
			st.waiting = false
			d.remote(func(r *RemoteReport) { r.PatrolWaitS += el.Seconds() })
		}
	}
	for {
		if len(d.fr.EngagedHostiles()) > 0 && d.fr.PartyInCombat() {
			stop()
			return true, d.walkAggro(ctx) // the patrol (or something else) came to the party while it waited
		}
		if d.stale() {
			if err := d.tick(ctx, d.cfg.Poll); err != nil {
				return false, err
			}
			continue
		}
		here, ok := d.leadPos()
		r := float64(d.groupRadius(sp))
		pack := d.packOf(t, r)
		at := []Point{hostilePoint(t)}
		if ok {
			at = append(at, here)
		}
		var danger []tbm.HostileRec
		members := d.patrolMembers(p)
		for _, h := range members {
			if st.passed[h.GUID] || h.GUID == t.GUID {
				continue
			}
			if d.patrolDanger(p, h, at) {
				danger = append(danger, h)
			}
		}
		if len(members) > 0 {
			st.located = true
			d.patrolWalkedAt[p] = d.clk.Now()
		}
		if len(danger) == 0 {
			// No member of the patrol in view is no patrol: it may be one corner away, on a round that brings it to the pull in 30 s.
			// The party looks for it for up to a round before it pulls (remote_patrol.go, patrolLocate).
			if wait, err := d.locatePatrol(ctx, p, st, t, len(members) > 0); err != nil {
				return false, err
			} else if wait {
				continue
			}
			stop()
			return false, nil
		}
		d.endLocate(st)
		if ok && st.pulls < maxPatrolPulls {
			sort.SliceStable(danger, func(i, j int) bool {
				return dist3(hostilePoint(danger[i]), here) < dist3(hostilePoint(danger[j]), here)
			})
			for _, h := range danger {
				if dist3(hostilePoint(h), here) > float64(d.tx.PatrolPullYd) {
					break
				}
				clear := !slices.ContainsFunc(pack, func(o tbm.HostileRec) bool {
					return dist3(hostilePoint(o), hostilePoint(h)) <= float64(d.tx.PatrolAggro)
				})
				if !clear {
					continue
				}
				stop()
				st.pulls++
				return true, d.pullPatrol(ctx, h, sp)
			}
		}
		now := d.clk.Now()
		if !st.waiting {
			st.waiting, st.waitBegan = true, now
			d.remote(func(r *RemoteReport) { r.PatrolWaits++ })
			d.rep.note(fmt.Sprintf("leg %d: a patrol (%s) walks within %.0f yd of the pull of %s; the party waits for it to pass (at most %.0f s for this group)", d.curLeg, patrolText(d, danger), float64(d.tx.PatrolAggro), d.hostileName(t), (d.tx.PatrolWait - st.spent).Seconds()))
		}
		if st.spent+now.Sub(st.waitBegan) >= d.tx.PatrolWait {
			stop()
			st.timedOut = true
			d.remote(func(r *RemoteReport) { r.PatrolTimeouts++ })
			d.rep.note(fmt.Sprintf("leg %d: the patrol (%s) did not walk away from the pull of %s in %.0f s; pulling with it counted in the pack", d.curLeg, patrolText(d, danger), d.hostileName(t), d.tx.PatrolWait.Seconds()))
			return false, nil
		}
		if err := d.tick(ctx, d.cfg.Poll); err != nil {
			return false, err
		}
	}
}

// locatePatrol holds the gate while no member of the group's patrol is in view and none was seen walking lately (patrolKnown): the plan
// knows the patrol's path and round but not where it is on it. It waits at most min(patrol_locate_s, the patrol's round + 10 s) per group.
// It reports whether the gate waited (the caller loops); a patrol seen at any time ends it, and a look that ran out is noted once.
func (d *RemoteDriver) locatePatrol(ctx context.Context, p *Patrol, st *patrolState, t tbm.HostileRec, inView bool) (waited bool, err error) {
	if inView || st.located || st.locateDone || d.tx.PatrolLocate <= 0 {
		d.endLocate(st)
		return false, nil
	}
	if at, ok := d.patrolWalkedAt[p]; ok && d.clk.Now().Sub(at) < patrolKnown {
		st.located = true
		return false, nil
	}
	limit := min(d.tx.PatrolLocate, time.Duration(p.PeriodS+10)*time.Second)
	now := d.clk.Now()
	if !st.locating {
		st.locating, st.locateAt = true, now
		d.remote(func(r *RemoteReport) { r.PatrolLocates++ })
		d.rep.note(fmt.Sprintf("leg %d: no member of the patrol (entries %v) is in view of the pull of %s; the party watches for it up to %.0f s before the pull", d.curLeg, p.Entries, d.hostileName(t), limit.Seconds()))
	}
	if now.Sub(st.locateAt) >= limit {
		st.locateDone = true
		d.endLocate(st)
		d.rep.note(fmt.Sprintf("leg %d: the patrol (entries %v) did not come into view in %.0f s; pulling %s without it counted", d.curLeg, p.Entries, limit.Seconds(), d.hostileName(t)))
		return false, nil
	}
	d.wd.progress("looking for the patrol") // a bounded look: the watchdog's clock starts again (ZF-1 run 6: a stall warning after 120 s)
	if err := d.tick(ctx, d.cfg.Poll); err != nil {
		return false, err
	}
	return true, nil
}

// endLocate closes the look for a patrol (its seconds go in the report).
func (d *RemoteDriver) endLocate(st *patrolState) {
	if !st.locating {
		return
	}
	st.locating = false
	el := d.clk.Now().Sub(st.locateAt)
	d.remote(func(r *RemoteReport) { r.PatrolLocateS += el.Seconds() })
}

func patrolText(d *RemoteDriver, hs []tbm.HostileRec) string {
	var out []string
	for _, h := range hs {
		out = append(out, fmt.Sprintf("%s at %.0f %.0f", d.hostileName(h), h.X, h.Y))
	}
	return strings.Join(out, ", ")
}

// pullPatrol pulls h, a patrol member passing the party away from the pack, alone and fights it to the end. Its pull target is a member
// of the patrol that is not a cc_always entry when there is one; the pack CC (bigPackCC) takes a cc_always one.
func (d *RemoteDriver) pullPatrol(ctx context.Context, h tbm.HostileRec, sp fightSpec) error {
	d.remote(func(r *RemoteReport) { r.PatrolPulls++ })
	near := d.packOf(h, float64(d.cfg.PackRadius))
	target := h
	for _, o := range near {
		if slices.Contains(sp.patrol.Entries, o.Entry) && !slices.Contains(d.plan.CCAlways, o.Entry) {
			target = o
			break
		}
	}
	d.rep.note(fmt.Sprintf("leg %d: the patrol (%d mobs) passes %.0f yd from the party and clear of the pack; it is pulled alone first (%s)", d.curLeg, len(near), distToLead(d, hostilePoint(h)), d.hostileName(target)))
	if err := d.gather(ctx, "the pull of a patrol"); err != nil {
		return err
	}
	track := slices.Clone(sp.patrol.Entries)
	for _, o := range near {
		if !slices.Contains(track, o.Entry) {
			track = append(track, o.Entry)
		}
	}
	psp := fightSpec{pick: []uint32{target.Entry}, track: track, skip: sp.skip, anchors: []Point{hostilePoint(target)}, limit: d.cfg.FightTimeout, passed: map[packet.GUID]bool{}}
	d.legStat(func(l *LegReport) { l.Pulls++ })
	res, err := d.tracedFight(ctx, target, psp)
	if err != nil {
		return err
	}
	if res.out == won {
		d.noteKilled(res)
		return nil
	}
	sp.patrolSt.passed[h.GUID], sp.patrolSt.passed[target.GUID] = true, true
	if res.out == refused {
		d.skipped[target.GUID] = res.why
	}
	d.rep.note(fmt.Sprintf("leg %d: the pull of the patrol %s (%s); it is not pulled alone again for this group", d.curLeg, outcomeName(res.out), res.why))
	return nil
}

// reactiveCC runs every look of a fight (mobs are the mobs fighting the party; pulled is the pull's target, 0 for a fight on the way). It
// puts the cc bot's Polymorph on a mob that joined: a cc_always entry at once, or any by cc_priority once the fight has big_pack mobs. The
// cc bot holds one CC at a time: a CC that holds stays, unless it is on a mob that is not cc_always and a cc_always one is free.
func (d *RemoteDriver) reactiveCC(ctx context.Context, pulled packet.GUID, mobs []tbm.HostileRec) error {
	if d.tx.BigPack <= 0 || len(mobs) < 2 {
		return nil
	}
	d.ensureCCBot()
	bot := strings.TrimSpace(d.opt.CCBot)
	if bot == "" {
		return nil
	}
	if m, ok := d.memberByName(bot); !ok || m.Dead {
		return nil
	}
	now := d.clk.Now()
	if !d.rccLast.IsZero() && now.Sub(d.rccLast) < ccFresh {
		return nil
	}
	isAlways := func(h tbm.HostileRec) bool { return slices.Contains(d.plan.CCAlways, h.Entry) }
	if !slices.ContainsFunc(mobs, isAlways) && len(mobs) < d.tx.BigPack {
		return nil
	}
	d.tf.init()
	if d.rccN == nil {
		d.rccN = map[packet.GUID]int{}
	}
	var tankTarget packet.GUID
	var tankAt *Point
	if m, ok := d.fr.Member(d.tank); ok && d.tank != 0 {
		tankTarget = m.Target
		p := partyPoint(m)
		tankAt = &p
	}
	var held *tbm.HostileRec
	var cands []tbm.HostileRec
	for i, h := range mobs {
		if d.tf.ccHeld[h.GUID] && d.ccStillOn(h) {
			held = &mobs[i]
			continue
		}
		if h.CrowdControlled() || h.GUID == tankTarget || h.GUID == d.tf.focus || h.HPPct < ccLowHP || d.rccN[h.GUID] >= maxCCPerMob {
			continue
		}
		if h.GUID == pulled && d.tank == 0 {
			continue // with no tank known, the pull target is the one the tank went for
		}
		cands = append(cands, h)
	}
	rank := func(h tbm.HostileRec) int {
		if isAlways(h) {
			return -1
		}
		if i := slices.Index(d.plan.CCPriority, h.Entry); i >= 0 {
			return i
		}
		return len(d.plan.CCPriority)
	}
	far := func(h tbm.HostileRec) float64 {
		if tankAt == nil {
			return 0
		}
		return dist3(hostilePoint(h), *tankAt)
	}
	sort.SliceStable(cands, func(i, j int) bool {
		if ri, rj := rank(cands[i]), rank(cands[j]); ri != rj {
			return ri < rj
		}
		return far(cands[i]) > far(cands[j])
	})
	if len(cands) == 0 {
		return nil
	}
	pick := cands[0]
	if len(mobs) < d.tx.BigPack && !isAlways(pick) {
		return nil // a small fight is CCed for its cc_always member only
	}
	if held != nil && (isAlways(*held) || !isAlways(pick)) {
		return nil // the cc bot's one CC holds where it should
	}
	renew := d.tf.ccHeld[pick.GUID]
	d.rccLast = now
	d.rccN[pick.GUID]++
	d.ccDone[pick.GUID] = true
	d.ccOrdered[pick.GUID] = now
	d.remote(func(r *RemoteReport) { r.CCOrders++ })
	r, err := d.order(ctx, "cc", "moon", tbm.GUIDArg(pick.GUID), bot)
	if err != nil {
		return err
	}
	if ref, bad := refusedOf(r); bad {
		delete(d.ccOrdered, pick.GUID)
		d.rep.note(fmt.Sprintf("leg %d: the cc of %s during the fight by %s was refused: %s (%s)", d.curLeg, d.hostileName(pick), bot, ref.Code, ref.Message))
		return nil
	}
	if held != nil {
		delete(d.tf.ccHeld, held.GUID) // Polymorph moves: the old one is fought again
	}
	d.tf.ccHeld[pick.GUID] = true
	why := "it joined the fight"
	switch {
	case renew:
		why = "its CC lapsed during the fight"
		d.remote(func(r *RemoteReport) { r.CCRenews++ })
	default:
		d.remote(func(r *RemoteReport) { r.ReactiveCC++ })
	}
	d.rep.note(fmt.Sprintf("leg %d: %s takes %s with its CC (%s; %d mobs fighting)", d.curLeg, bot, d.hostileName(pick), why, len(mobs)))
	return nil
}
