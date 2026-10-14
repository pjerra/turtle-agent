package dungeon

import (
	"context"
	"fmt"
	"slices"
	"sort"
	"strings"
	"time"

	"github.com/pjerra/turtle-agent/internal/packet"
	"github.com/pjerra/turtle-agent/internal/tbm"
)

// Pulls and fights in remote mode. The hostiles come from the TBM:VIEW feed (what the bots see, not what the agent sees), the
// orders are `.bot cmd pullback|attack|cc <guid>`, and the threat records decide when the party is sent in.

// doPull pulls each member group of the action: `.bot cmd pullback <guid>` at the nearest member, the tank walks up and pulls it,
// the fight ends when the pack is dead.
func (d *RemoteDriver) doPull(ctx context.Context, a Action) error {
	if skip, err := d.gatedLook(ctx, a); skip || err != nil {
		return err
	}
	sp := fightSpec{pick: a.Entries, track: a.Entries, skip: a.Skip, anchors: a.Positions, limit: d.cfg.FightTimeout, sneak: true, radius: a.Radius, passed: map[packet.GUID]bool{}}
	// A group whose members in view are all stealthed cannot be pulled (the bots never attack what they cannot see); with none in
	// view the plan's flag decides.
	if cs := d.candidates(sp, sp.pick); (len(cs) == 0 && a.Ambush) || (len(cs) > 0 && allHostilesStealthed(cs)) {
		return d.sneakPast(ctx, a.Label, sp)
	}
	return d.engage(ctx, a.Label, sp)
}

func allHostilesStealthed(hs []tbm.HostileRec) bool {
	for _, h := range hs {
		if !h.Stealthed() {
			return false
		}
	}
	return len(hs) > 0
}

// doKill kills Boss.First in order, then the boss.
func (d *RemoteDriver) doKill(ctx context.Context, a Action) error {
	b := a.Boss
	enc := append(slices.Clone(b.First), b.Entry)
	for _, e := range enc {
		spec := fightSpec{pick: []uint32{e}, track: enc, skip: a.Skip, anchors: []Point{b.Position}, limit: d.cfg.BossTimeout, required: e == b.Entry, wide: true}
		if err := d.engage(ctx, a.Label, spec); err != nil {
			return err
		}
	}
	return nil
}

func (d *RemoteDriver) groupRadius(sp fightSpec) float32 {
	if sp.radius > 0 {
		return sp.radius
	}
	return d.cfg.PackRadius
}

func (d *RemoteDriver) nearAnchor(sp fightSpec, h tbm.HostileRec) bool {
	for _, a := range sp.anchors {
		if dist3(hostilePoint(h), a) <= float64(d.groupRadius(sp)) {
			return true
		}
	}
	return false
}

// engagedWith says whether the hostile is fighting the party: it targets a bot, or a bot is on its threat list.
func (d *RemoteDriver) engagedWith(h tbm.HostileRec) bool {
	if d.fr.IsMember(h.Target) {
		return true
	}
	t, ok := d.fr.Threat(h.GUID)
	return ok && len(t.Top) > 0
}

// candidates are the living hostiles of entries (not skipped) the bots see, nearest to the lead bot first, with the same anchor and
// radius rules as Driver.candidates (the distances are the lead bot's, not the agent's).
func (d *RemoteDriver) candidates(sp fightSpec, entries []uint32) []tbm.HostileRec {
	var all, near, fighting []tbm.HostileRec
	for _, h := range d.fr.Hostiles {
		if h.Entry == 0 || h.HPPct <= 0 || !slices.Contains(entries, h.Entry) || slices.Contains(sp.skip, h.Entry) {
			continue
		}
		if _, refused := d.skipped[h.GUID]; refused || sp.passed[h.GUID] {
			continue
		}
		dl := distToLead(d, hostilePoint(h))
		if dl <= 80 {
			all = append(all, h)
		}
		isNear := d.nearAnchor(sp, h)
		if isNear {
			near = append(near, h)
		}
		if !isNear && dl <= 80 && d.engagedWith(h) {
			fighting = append(fighting, h)
		}
	}
	out := all
	if len(near) > 0 || (len(sp.anchors) > 0 && !sp.wide) {
		out = append(near, fighting...)
	}
	sort.SliceStable(out, func(i, j int) bool {
		return distToLead(d, hostilePoint(out[i])) < distToLead(d, hostilePoint(out[j]))
	})
	return out
}

// preferVisible puts the hostiles that are not stealthed first (a bot can be sent at those).
func preferVisibleHostile(hs []tbm.HostileRec) []tbm.HostileRec {
	out := make([]tbm.HostileRec, 0, len(hs))
	for _, h := range hs {
		if !h.Stealthed() {
			out = append(out, h)
		}
	}
	if len(out) == 0 {
		return hs
	}
	return out
}

// ccEntries are the entries a CC rule names.
func (d *RemoteDriver) ccRule(entry uint32) (CCRule, bool) {
	for _, r := range d.optCC() {
		if r.Entry == entry {
			return r, true
		}
	}
	return CCRule{}, false
}

func (d *RemoteDriver) optCC() []CCRule { return d.opt.CC }

// engage pulls the group until it is gone. Its exits (README "Remote mode", G3):
//   - an error of the rest gate, the pick, a fight, an order or a wait: the run stops;
//   - nothing to pick, a required step (the boss) known dead (bossDead): done;
//   - nothing to pick, and confirmGone fails (a position cannot be looked at): error; finds a hostile on its look: pick again;
//   - nothing to pick, every position confirmed: with given-up mobs, leftOver; a required step not known dead: error ("no living ...
//     seen"); else done (with kills, or seen empty);
//   - a stealthed target of a sneak group: sneakPast;
//   - a won pull, or a lost target (up to maxRetargets): pick again;
//   - the pulls used up (PullAttempts failed pulls, or the loops cap): leftOver.
//
// leftOver passes only with progress (kills, or every mob left refused by the bots), every position seen, and a required step's boss
// known dead.
func (d *RemoteDriver) engage(ctx context.Context, label string, sp fightSpec) error {
	if sp.passed == nil {
		sp.passed = map[packet.GUID]bool{}
	}
	kills, retargets := 0, 0
	cl := newClearing()
	var gaveUp []passedOver
	for attempt, loops := 1, 0; attempt <= d.cfg.PullAttempts && loops < 4*d.cfg.PullAttempts+12; loops++ {
		if err := d.restGate(ctx, true); err != nil {
			return err
		}
		t, ok, err := d.pickTarget(ctx, sp)
		if err != nil {
			return err
		}
		if !ok {
			// A boss that died, in its own pull or in a First add's that it joined, is not in the feed any more (the feed lists no
			// corpses for long): the kill this run (or its corpse in view) is the evidence, as the group driver's deadInView.
			if sp.required {
				if err := d.nearForBoss(ctx, sp); err != nil {
					return err
				}
				if dead, why := d.bossDead(sp); dead {
					d.rep.note(fmt.Sprintf("%s: %v is dead (%s)", label, sp.pick, why))
					return nil
				}
			}
			// Nothing of the group in view: it counts as gone only with evidence, else the lead bot walks to its positions to look. This
			// holds after mobs were given up on too: a position the bots never saw is not cleared by a give-up at another.
			cleared, why, err := d.confirmGone(ctx, sp, cl)
			if err != nil {
				return err
			}
			if !cleared {
				continue // a hostile of the group showed itself on the way: pick it
			}
			// The look walk may itself have run into the boss: a pack aggroed on the way, the party fought, and the boss died in that
			// fight (remote Uldaman hires-2, Revelosh at 1 % in the ledger). The ledger knows it; the empty look is then the corpse's absence.
			if sp.required {
				if dead, why := d.bossDead(sp); dead {
					d.rep.note(fmt.Sprintf("%s: %v is dead (%s)", label, sp.pick, why))
					return nil
				}
			}
			if len(gaveUp) > 0 {
				return d.leftOver(label, sp, kills, gaveUp, cl)
			}
			if sp.required {
				return fmt.Errorf("%s: no living %v seen by the bots (%s)", label, sp.pick, why)
			}
			if kills > 0 {
				d.rep.note(fmt.Sprintf("%s: %d kills; nothing more of %v at its positions: %s", label, kills, sp.pick, why))
				return nil
			}
			d.rep.note(fmt.Sprintf("%s: no living %v seen by the bots; counted as cleared: %s", label, sp.pick, why))
			return nil
		}
		if sp.sneak && t.Stealthed() {
			return d.sneakPast(ctx, label, sp)
		}
		// The party gathers on the lead bot before the pull (remote_gather.go): a bot left behind is not the one the pack finds first.
		if err := d.gather(ctx, "the pull of "+d.hostileName(t)); err != nil {
			return err
		}
		d.legStat(func(l *LegReport) {
			l.Pulls++
			if attempt > 1 {
				l.PullRetries++
			}
		})
		res, err := d.tracedFight(ctx, t, sp)
		if err != nil {
			return err
		}
		kills += res.kills
		if res.out == won {
			// The fight tracked only the pulled mob and what joined it: the group's other positions, out of the bots' sight, are not
			// known to be clear (G3). Pick again: what is left in view is pulled, else confirmGone clears this position by its kill
			// (noteKilled) and walks to the others. A won pull has kills, so attempt does not grow; the loops cap bounds a group that
			// keeps showing mobs.
			d.noteKilled(res)
			continue
		}
		if res.out == retarget && retargets < maxRetargets {
			retargets++
			d.rep.note(fmt.Sprintf("%s: the bots could not take %s (%s); picking again", label, d.hostileName(t), res.why))
			continue
		}
		where := fmt.Sprintf("%s at %.0f %.0f", d.hostileName(t), t.X, t.Y)
		why := map[outcome]string{noEngage: "drew no fight", evaded: "evaded", refused: "was refused (" + res.why + ")", retarget: "was lost (" + res.why + ")"}[res.out]
		if res.out == refused && res.unreachable {
			// A living mob the party could not get at: its group is not cleared, and nothing else of it is counted either.
			return fmt.Errorf("%s: %s at %.0f %.0f %.0f is unreachable for the tank: %s; the group is not cleared (the plan's position for it needs correcting)", label, d.hostileName(t), t.X, t.Y, t.Z, res.why)
		}
		cl.given = append(cl.given, hostilePoint(t)) // the bots saw the spot; the note below names the mob
		switch {
		case res.out == refused:
			d.skipped[t.GUID] = res.why
			gaveUp = append(gaveUp, passedOver{text: where + ": " + res.why, refused: true})
		case res.kills > 0:
			why += fmt.Sprintf(", after %d kills", res.kills)
		case res.out == noEngage || res.out == evaded:
			if len(d.candidates(sp, sp.pick)) > 1 {
				sp.passed[t.GUID] = true
				gaveUp = append(gaveUp, passedOver{text: where + ": " + why})
			}
		}
		if res.kills == 0 {
			attempt++
		}
		d.rep.note(fmt.Sprintf("%s: pull of %s %s", label, d.hostileName(t), why))
		if _, err := d.order(ctx, "follow"); err != nil {
			return err
		}
		if err := d.tick(ctx, d.cfg.Poll*3); err != nil {
			return err
		}
	}
	return d.leftOver(label, sp, kills, gaveUp, cl)
}

// hostileName names a hostile for the report: the feed has no names, only entries.
func (d *RemoteDriver) hostileName(h tbm.HostileRec) string {
	return fmt.Sprintf("entry %d (%s)", h.Entry, h.GUID)
}

// leftOver ends a group whose pulls are used up or whose mobs left in view were all given up on. As in group mode it counts as cleared
// (noted) with progress: kills, or every mob left refused by the bots. Unlike group mode, progress is not enough (G3): every position of
// the group needs evidence (positionSure), since a kill at one says nothing about another out of sight; and a required step (the boss)
// needs the boss known dead, not just kills of its adds, unless the bots refused it outright.
func (d *RemoteDriver) leftOver(label string, sp fightSpec, kills int, gaveUp []passedOver, cl *clearing) error {
	allRefused := len(gaveUp) > 0
	var texts []string
	for _, g := range gaveUp {
		allRefused = allRefused && g.refused
		texts = append(texts, g.text)
	}
	if kills == 0 && !allRefused {
		return fmt.Errorf("%s: %d pulls of %v failed (no fight, or the mobs evaded)", label, d.cfg.PullAttempts, sp.pick)
	}
	rest := strings.Join(texts, "; ")
	if rest == "" {
		rest = "the last pulls drew no fight"
	}
	if miss := d.unseenPositions(sp, cl); len(miss) > 0 {
		return fmt.Errorf("%s: gave up with %d kills (%s), and the group's positions %s were never seen: not counting it as cleared", label, kills, rest, strings.Join(miss, ", "))
	}
	if sp.required && !allRefused {
		if dead, _ := d.bossDead(sp); !dead {
			return fmt.Errorf("%s: gave up with %d kills (%s), and %v was not seen dead: not counting the boss as killed", label, kills, rest, sp.pick)
		}
	}
	d.rep.note(fmt.Sprintf("%s: gave up with %d kills (%s); counted as cleared", label, kills, rest))
	return nil
}

// pickTarget finds the hostile to pull, walking the lead bot up to PullStandoff yards from it when it is out of PullRange (or from
// the anchor when the bots see nothing of the group yet). A hostile with a CC rule, or one that is crowd-controlled, is not picked while
// another candidate is.
func (d *RemoteDriver) pickTarget(ctx context.Context, sp fightSpec) (tbm.HostileRec, bool, error) {
	const looks = 3
	for look := 1; look <= looks; look++ {
		if cs := d.candidates(sp, sp.pick); len(cs) > 0 {
			cs = preferVisibleHostile(cs)
			if rest := slices.DeleteFunc(slices.Clone(cs), func(h tbm.HostileRec) bool { _, cc := d.ccRule(h.Entry); return cc || d.ccd(h) }); len(rest) > 0 {
				cs = rest
			}
			if c := cs[0]; distToLead(d, hostilePoint(c)) <= float64(d.cfg.PullRange) || look == looks {
				return c, true, nil
			} else if err := d.approach(ctx, hostilePoint(c), d.cfg.PullStandoff); err != nil {
				return tbm.HostileRec{}, false, err
			}
			continue
		}
		if len(sp.anchors) == 0 {
			break
		}
		if err := d.approach(ctx, sp.anchors[0], d.cfg.PullStandoff); err != nil {
			return tbm.HostileRec{}, false, err
		}
		if err := d.tick(ctx, d.cfg.Poll); err != nil {
			return tbm.HostileRec{}, false, err
		}
	}
	return tbm.HostileRec{}, false, nil
}

// approach walks the lead bot to a point standoff yards short of p along the navmesh path (the straight line without a planner).
func (d *RemoteDriver) approach(ctx context.Context, p Point, standoff float32) error {
	return d.approachArrive(ctx, p, standoff, d.opt.MoveArrive)
}

// approachArrive is approach with its own arrival distance.
func (d *RemoteDriver) approachArrive(ctx context.Context, p Point, standoff, arrive float32) error {
	here, ok := d.leadPos()
	if !ok {
		return nil
	}
	var stand Point
	if pl, ok := d.g.(PathPlanner); ok {
		if pts, err := pl.PlanPath(ctx, d.plan.Map, here, p); err == nil {
			if s, ok := StandoffPoint(pts, standoff); ok {
				stand = s
			} else {
				return nil
			}
		}
	}
	if stand == (Point{}) {
		dist := dist3(here, p)
		if dist <= float64(standoff) {
			return nil
		}
		stand = lerp(here, p, (dist-float64(standoff))/dist)
	}
	if dist3(here, stand) <= float64(arrive) {
		return nil
	}
	return d.walkArrive(ctx, stand, arrive)
}

// ccFor puts the CC marks on the hostiles around the target that a rule names, before the pull.
func (d *RemoteDriver) ccFor(ctx context.Context, t tbm.HostileRec, sp fightSpec) error {
	for _, h := range d.candidates(sp, sp.track) {
		rule, ok := d.ccRule(h.Entry)
		if !ok || d.ccDone[h.GUID] || dist3(hostilePoint(h), hostilePoint(t)) > float64(d.groupRadius(sp)) {
			continue
		}
		d.ccDone[h.GUID] = true
		args := []string{rule.Mark, tbm.GUIDArg(h.GUID)}
		if rule.Bot != "" {
			args = append(args, rule.Bot)
		}
		d.remote(func(r *RemoteReport) { r.CCOrders++ })
		r, err := d.order(ctx, "cc", args...)
		if err != nil {
			return err
		}
		if ref, bad := refusedOf(r); bad {
			d.rep.note(fmt.Sprintf("cc %s on %s was refused: %s (%s); it is fought like the rest", rule.Mark, d.hostileName(h), ref.Code, ref.Message))
		} else {
			d.tf.init()
			d.tf.ccHeld[h.GUID] = true // the party leaves it to the CC bot (tank-first rules)
		}
	}
	return nil
}

// sendPull sends the pull order for t and reads the answer. done is true when the pull is over with res (refused, lost); false when
// the order went (or fell back to attack) and the fight is on.
func (d *RemoteDriver) sendPull(ctx context.Context, t tbm.HostileRec) (res fightResult, done bool, err error) {
	busy, approaches := 0, 0
	var tried []string
	for try := 0; try < 3; try++ {
		r, err := d.order(ctx, "pullback", tbm.GUIDArg(t.GUID))
		if err != nil {
			return fightResult{}, true, err
		}
		ref, bad := refusedOf(r)
		if !bad {
			return fightResult{}, false, nil
		}
		why := ref.Code + ": " + ref.Message
		switch c := classify(ref); {
		case ref.Code == "unreachable":
			// The tank found no firing position: the mob is alive and the party has not got at it, so the group must not be counted
			// cleared on it. Pull again from nearer, from other approach points, a bounded number of times.
			d.noteUnreachable("pull", hostilePoint(t), d.hostileName(t)+": "+ref.Message)
			if approaches >= len(unreachableStandoffs) {
				return fightResult{out: refused, unreachable: true, why: fmt.Sprintf("%s (pulled %d times, from %s)", why, approaches+1, strings.Join(tried, ", "))}, true, nil
			}
			st := unreachableStandoffs[approaches]
			approaches++
			here, _ := d.leadPos()
			tried = append(tried, fmt.Sprintf("%.0f %.0f %.0f", here.X, here.Y, here.Z))
			d.rep.note(fmt.Sprintf("pull of %s: the tank could not reach a firing position (unreachable); walking the party to %.0f yd of it and pulling again (approach %d of %d)", d.hostileName(t), st, approaches, len(unreachableStandoffs)))
			if err := d.approach(ctx, hostilePoint(t), st); err != nil {
				return fightResult{}, true, err
			}
			try-- // an approach is not a failed pull
			continue
		case c == refUnreachable:
			return fightResult{out: refused, why: why}, true, nil
		case c == refDead || c == refNoTarget:
			return fightResult{out: retarget, why: why}, true, nil
		case ref.Code == "too-far":
			if err := d.approach(ctx, hostilePoint(t), 20); err != nil {
				return fightResult{}, true, err
			}
			continue
		case ref.Code == "tank-busy":
			// The only tank is in combat (it cannot start a pull while it fights): wait for the fight to end or the mob to come, then
			// pull again. Never an attack order in its place: the party would go in with no tank holding the mob.
			if busy++; busy > d.tx.TankBusyRounds {
				return fightResult{out: noEngage, why: fmt.Sprintf("tank-busy: the tank was still fighting after %d waits of %.0f s", d.tx.TankBusyRounds, d.opt.TankBusyWait.Seconds())}, true, nil
			}
			engaged, err := d.waitTankFree(ctx, t)
			if err != nil {
				return fightResult{}, true, err
			}
			if engaged {
				return fightResult{}, false, nil // the mob came to the party by itself: the fight is on
			}
			try-- // a wait is not a failed pull
			continue
		case ref.Code == "no-tank":
			// No bot of the party is a tank: the party goes in as it is.
			d.remote(func(r *RemoteReport) { r.PullFallbacks++ })
			d.rep.note(fmt.Sprintf("pull of %s: the module found no tank (%s); sending the party in with an attack order", d.hostileName(t), ref.Message))
			if _, err := d.order(ctx, "attack", tbm.GUIDArg(t.GUID)); err != nil {
				return fightResult{}, true, err
			}
			d.attacked[t.GUID] = true
			return fightResult{}, false, nil
		default:
			return fightResult{out: refused, why: why}, true, nil
		}
	}
	return fightResult{out: refused, why: "too-far: the pull was out of range after two approaches"}, true, nil
}

// fight pulls t and follows the fight in the feed until the pack is dead, evaded, never came, or the limit passed.
func (d *RemoteDriver) fight(ctx context.Context, t tbm.HostileRec, sp fightSpec) (fightResult, error) {
	if err := d.ccFor(ctx, t, sp); err != nil {
		return fightResult{}, err
	}
	if res, done, err := d.sendPull(ctx, t); err != nil || done {
		return res, err
	}
	d.ptPulled()
	began := d.clk.Now()
	limit := sp.limit
	tracked := map[packet.GUID]bool{t.GUID: true}
	last := map[packet.GUID]tbm.HostileRec{t.GUID: t} // each tracked mob as the feed last showed it: where a kill happened
	for _, h := range d.candidates(sp, sp.track) {
		if dist3(hostilePoint(h), hostilePoint(t)) <= float64(d.groupRadius(sp)) {
			tracked[h.GUID], last[h.GUID] = true, h
		}
	}
	engaged := false
	lastCombat := began
	var lastAssist time.Time
	killed := map[packet.GUID]bool{}
	// A mob is counted gone only when it is absent from two frames in a row, and never from a frame the module cut short (Truncated:
	// records were dropped to fit, so its absence says nothing). A mob counts as killed only when the feed showed it fighting (or
	// hurt) before it went; a mob that vanishes without that is no kill, and a pull that ends with no kill and no fight drew none.
	fought := map[packet.GUID]bool{}
	absent := map[packet.GUID]int{}
	for g := range tracked {
		fought[g] = false
	}
	lastN := d.n
	sawEvade := false
	deadOf := func() []deadAt {
		var out []deadAt
		for g := range killed {
			out = append(out, deadAt{entry: last[g].Entry, at: hostilePoint(last[g]), guid: g})
		}
		return out
	}
	for {
		if err := d.tick(ctx, d.cfg.Poll); err != nil {
			return fightResult{}, err
		}
		if d.stale() {
			lastCombat = d.clk.Now() // a quiet feed proves nothing about the fight
			continue
		}
		now := d.clk.Now()
		for _, h := range d.fr.Hostiles {
			// Anything that joins the fight is part of it, the step's own entries or not: a boss that comes into a group's fight and
			// dies there is that fight's kill (a unit of another entry used to be left untracked, and its death went unrecorded).
			if h.HPPct > 0 && h.Entry != 0 && d.engagedWith(h) && !slices.Contains(sp.skip, h.Entry) {
				if !tracked[h.GUID] && !slices.Contains(sp.track, h.Entry) {
					limit = max(limit, d.cfg.BossTimeout) // a boss's fight can be longer than a pack's
				}
				tracked[h.GUID], last[h.GUID] = true, h
			}
		}
		newFrame := d.n != lastN
		lastN = d.n
		alive, fighting := 0, false
		var fightingMobs []tbm.HostileRec
		for g := range tracked {
			h, ok := d.fr.Hostile(g)
			if !ok {
				if newFrame && !d.fr.Truncated {
					absent[g]++
				}
				if absent[g] >= 2 { // out of the bots' sight for two whole frames: dead if it was in the fight, else gone
					// The pulled mob and its group's entries: gone after fighting is dead. A joiner of another entry (a boss) is dead only
					// when it was nearly dead with a bot by (the ledger's rule): one chased out of view alive is no kill.
					if fought[g] && (slices.Contains(sp.track, last[g].Entry) || d.goneMeansDead(last[g])) {
						killed[g] = true
					}
					delete(tracked, g)
				} else {
					alive++ // not known to be gone
				}
				continue
			}
			absent[g], last[g] = 0, h
			if h.HPPct <= 0 {
				killed[g] = true
				delete(tracked, g)
				continue
			}
			if d.engagedWith(h) || h.HPPct < 100 {
				fought[g] = true
			}
			if h.Evading() && !d.engagedWith(h) {
				sawEvade = true
				continue // walking home: not part of the fight any more
			}
			if sp.sneak && h.Stealthed() && !d.engagedWith(h) && h.HPPct >= 100 {
				continue // a stealthed member that has not joined is not fought (it aggros on the walk after)
			}
			alive++
			if d.engagedWith(h) || h.HPPct < 100 {
				fighting = true
				fightingMobs = append(fightingMobs, h)
			}
		}
		if fighting {
			engaged, lastCombat = true, now
			d.watchThreat(fightingMobs)
			if err := d.tankFirstTick(ctx, fightingMobs); err != nil {
				return fightResult{}, err
			}
		}
		d.wd.fightSignal(len(killed), hostilesHP(fightingMobs))
		if d.wdRepick.take() {
			return fightResult{out: retarget, kills: len(killed), dead: deadOf(), why: "the watchdog ended a stalled fight"}, nil
		}
		switch {
		case alive == 0 && len(killed) > 0:
			d.ptKilled()
			return fightResult{out: won, kills: len(killed), dead: deadOf()}, d.afterFight(ctx)
		case alive == 0 && sawEvade:
			return fightResult{out: evaded}, nil
		case alive == 0:
			// Everything tracked went without the feed ever showing it fight: nothing was killed.
			return fightResult{out: noEngage}, nil
		case !engaged && now.Sub(began) > d.cfg.EngageTimeout:
			return fightResult{out: noEngage, kills: len(killed), dead: deadOf()}, nil
		case engaged && now.Sub(lastCombat) > d.cfg.EvadeGrace:
			return fightResult{out: evaded, kills: len(killed), dead: deadOf()}, nil
		case now.Sub(began) > limit:
			return fightResult{}, fmt.Errorf("the fight with %v lasted more than %.0f s", sp.pick, limit.Seconds())
		}
		if engaged && len(fightingMobs) > 0 {
			if err := d.assistEvery(ctx, fightingMobs, &lastAssist); err != nil {
				return fightResult{}, err
			}
		}
	}
}

// assistEvery sends the party at the nearest of mobs, the first time at once (when the threat allows) and then every AssistEvery.
func (d *RemoteDriver) assistEvery(ctx context.Context, mobs []tbm.HostileRec, last *time.Time) error {
	now := d.clk.Now()
	if !last.IsZero() && now.Sub(*last) < d.cfg.AssistEvery {
		// A mob not yet attacked is looked at every tick: the threat gate may open. So is a fight whose mob died: the party is sent at
		// the next one the tank holds at once, not left to pick its own.
		fresh := slices.ContainsFunc(mobs, func(h tbm.HostileRec) bool { return !d.attacked[h.GUID] })
		if !fresh && !d.focusGone(mobs) {
			return nil
		}
	}
	sent, err := d.assistSend(ctx, mobs)
	if sent {
		*last = now
	}
	return err
}

// assist is the one-shot form for the rest gate and the walks: send the party at the nearest mob, once per AssistEvery.
func (d *RemoteDriver) assist(ctx context.Context, mobs []tbm.HostileRec) error {
	if d.lastAssist.IsZero() || d.clk.Now().Sub(d.lastAssist) >= d.cfg.AssistEvery || slices.ContainsFunc(mobs, func(h tbm.HostileRec) bool { return !d.attacked[h.GUID] }) || d.focusGone(mobs) {
		sent, err := d.assistSend(ctx, mobs)
		if sent {
			d.lastAssist = d.clk.Now()
		}
		return err
	}
	return nil
}

// focusGone says whether the mob the party was sent at is no longer among mobs (it died, or left the fight).
func (d *RemoteDriver) focusGone(mobs []tbm.HostileRec) bool {
	f := d.tf.focus
	return f != 0 && !slices.ContainsFunc(mobs, func(h tbm.HostileRec) bool { return h.GUID == f })
}

// assistSend picks the mob the tank holds best and sends `.bot cmd attack` at it (README "Tank first"): the crowd-controlled mobs are
// left out unless they are all that is left, and the rest are taken by the tank's share of their threat, the tank's own target first,
// the nearest after. A mob the party was not sent at yet is gated by threat: the order goes once the tank holds its threat (and has held
// it OpenerDelay), or ThreatWait after the mob first fought the party. It reports whether an order went.
func (d *RemoteDriver) assistSend(ctx context.Context, mobs []tbm.HostileRec) (bool, error) {
	if len(mobs) == 0 {
		return false, nil
	}
	d.tf.init()
	cands := d.respectCC(mobs)
	sorted := d.rankByTank(cands)
	var pick *tbm.HostileRec
	for i := range sorted {
		h := sorted[i]
		if !d.attacked[h.GUID] {
			if !d.threatAllows(h) {
				continue
			}
			d.attacked[h.GUID] = true
		}
		pick = &sorted[i]
		break
	}
	if pick == nil {
		return false, nil
	}
	if !d.attackAllowed(*pick) {
		return false, nil
	}
	r, err := d.order(ctx, "attack", tbm.GUIDArg(pick.GUID))
	if err != nil {
		return false, err
	}
	if _, bad := refusedOf(r); bad {
		d.tf.sentGUID = 0 // refused: the next look may send it again
	} else {
		d.tf.sentGUID, d.tf.sentAt = pick.GUID, d.clk.Now()
	}
	d.noteFocus(pick.GUID, cands)
	return true, nil
}

// attackHoldYd is how near the mob the tank must be for its threat to count as holding it.
const attackHoldYd = 45

// attackAllowed is the cadence of the attack order at one mob. After a pull the bots' own pull is running (the tank is walking to its
// firing position, the others hold): an attack order for the same mob every second made the tank drop it for "return to pull position"
// and back (Uldaman group B, tank wipes). So an order goes once per mob, and again only when the mob changed, the last order was refused
// (sentGUID forgotten), or AssistEvery (at least 5 s) has passed with the tank still not holding the mob; the reason of a repeat is
// noted. Every wait here is the clock's, so the repeat cannot be pushed out by anything the bots do (rule 3). "The tank holds it" means
// the named tank, or, before one is known, a tank-like bot (a healer on the mob is no tank); alive, and within attackHoldYd of the mob.
func (d *RemoteDriver) attackAllowed(h tbm.HostileRec) bool {
	if d.tf.sentGUID != h.GUID {
		return true
	}
	wait := max(d.cfg.AssistEvery, 5*time.Second)
	now := d.clk.Now()
	if now.Sub(d.tf.sentAt) < wait {
		return false
	}
	if holder, ok := d.fr.ThreatHolder(h.GUID); ok && d.fr.IsMember(holder) && ((d.tank == 0 && d.tankLike(holder)) || (d.tank != 0 && holder == d.tank)) {
		// The tank has it: the order did its work, unless the tank is dead, or not at the mob (it follows the commander, or is running back).
		if m, found := d.memberByGUID(holder); !found || (!m.Dead && dist3(partyPoint(m), hostilePoint(h)) <= float64(d.tx.AttackHoldYd)) {
			return false
		}
	}
	n := 0
	d.remote(func(r *RemoteReport) { r.AttackResends++; n = r.AttackResends })
	if n <= 5 {
		d.rep.note(fmt.Sprintf("leg %d: attack order for %s sent again %.0f s after the first: the tank still does not hold it", d.curLeg, d.hostileName(h), now.Sub(d.tf.sentAt).Seconds()))
	}
	return true
}

// threatAllows is the threat gate of the first attack order at a mob: the party is sent in when the tank holds the mob (the top of
// its T record, and the mob is hitting it), or when ThreatWait has passed since the mob first fought the party. The tank is the
// named one, else the first bot seen holding a pulled mob. The decisions are counted in the report.
func (d *RemoteDriver) threatAllows(h tbm.HostileRec) bool {
	now := d.clk.Now()
	if _, ok := d.engagedAt[h.GUID]; !ok {
		d.engagedAt[h.GUID] = now
	}
	holder, ok := d.fr.ThreatHolder(h.GUID)
	held := ok && d.fr.IsMember(holder) && (h.Target == 0 || h.Target == holder)
	if held && d.tank != 0 && holder != d.tank {
		held = false // some other bot has it: not the tank
	}
	if held && d.tank == 0 && !d.tankLike(holder) {
		held = false // nobody is named tank yet, and this one is not tank-like
	}
	if held {
		if d.tank == 0 {
			d.setTank(holder)
		}
		// The opener: the tank has held the mob for OpenerDelay before the party goes in, so its first hits have built a lead.
		d.tf.init()
		if _, ok := d.tf.heldAt[h.GUID]; !ok {
			d.tf.heldAt[h.GUID] = now
		}
		if now.Sub(d.tf.heldAt[h.GUID]) < d.opt.OpenerDelay {
			if !d.waitNoted[h.GUID] {
				d.waitNoted[h.GUID] = true
				d.remote(func(r *RemoteReport) { r.ThreatWaits++ })
				d.rep.note(fmt.Sprintf("leg %d: the tank has just got %s; the party is held back %.0f s for the opener", d.curLeg, d.hostileName(h), d.opt.OpenerDelay.Seconds()))
			}
			return false
		}
		if !d.heldNoted[h.GUID] {
			d.heldNoted[h.GUID] = true
			d.remote(func(r *RemoteReport) { r.ThreatHolds++ })
		}
		return true
	}
	d.tf.init()
	delete(d.tf.heldAt, h.GUID) // the tank lost it: the opener starts again when it has it back
	if now.Sub(d.engagedAt[h.GUID]) < d.opt.ThreatWait {
		if !d.waitNoted[h.GUID] {
			d.waitNoted[h.GUID] = true
			d.remote(func(r *RemoteReport) { r.ThreatWaits++ })
			d.rep.note(fmt.Sprintf("leg %d: %s is not on the tank yet; the party is held back (up to %.0f s)", d.curLeg, d.hostileName(h), d.opt.ThreatWait.Seconds()))
		}
		return false
	}
	d.remote(func(r *RemoteReport) { r.ThreatOverrides++ })
	d.rep.note(fmt.Sprintf("leg %d: %s still not on the tank after %.0f s; the party is sent in anyway", d.curLeg, d.hostileName(h), d.opt.ThreatWait.Seconds()))
	return true
}

// tankLike says whether a holder may be taken for the tank when none is named: when the party has a rage user (a warrior, a bear
// druid) only a rage user can be; else any bot. Naming the tank (dungeon_run tank) is the reliable way.
func (d *RemoteDriver) tankLike(g packet.GUID) bool {
	rage := false
	for _, p := range d.fr.Party {
		rage = rage || p.PowerType == 1
	}
	m, ok := d.fr.Member(g)
	return ok && (!rage || m.PowerType == 1)
}

func (d *RemoteDriver) setTank(g packet.GUID) {
	d.tank = g
	name := g.String()
	if m, ok := d.fr.Member(g); ok {
		name = m.Name
	}
	d.remote(func(r *RemoteReport) { r.Tank = name })
}

// watchThreat notes a bot (not the tank) that has come within ThreatWarnPct of the top threat on a mob, once per mob and bot.
func (d *RemoteDriver) watchThreat(mobs []tbm.HostileRec) {
	if d.tank == 0 {
		return
	}
	for _, h := range mobs {
		t, ok := d.fr.Threat(h.GUID)
		if !ok {
			continue
		}
		for _, e := range t.Top {
			if e.GUID == d.tank || e.Pct < d.opt.ThreatWarnPct {
				continue
			}
			key := h.GUID.String() + "/" + e.GUID.String()
			if d.warned[key] {
				continue
			}
			d.warned[key] = true
			name := e.GUID.String()
			if m, ok := d.fr.Member(e.GUID); ok {
				name = m.Name
			}
			d.remote(func(r *RemoteReport) { r.ThreatWarnings++ })
			d.rep.note(fmt.Sprintf("leg %d: %s has %d%% of the top threat on %s", d.curLeg, name, e.Pct, d.hostileName(h)))
		}
	}
}

// walkAggro fights what came at the party during a walk: attack orders at the engaged mobs until nothing has hit the party for
// AggroQuiet.
func (d *RemoteDriver) walkAggro(ctx context.Context) error {
	d.legStat(func(l *LegReport) {
		l.Pulls++
		l.WalkAggros++
	})
	d.remote(func(r *RemoteReport) { r.WalkAggros++ })
	d.rep.note(fmt.Sprintf("leg %d: a pack aggroed during a walk; the walk was stopped and the party fought it", d.curLeg))
	if err := d.fightEngaged(ctx); err != nil {
		return err
	}
	return d.afterFight(ctx)
}

// fightEngaged sends the party at the engaged mobs until none has fought it for AggroQuiet (or FightTimeout).
func (d *RemoteDriver) fightEngaged(ctx context.Context) error {
	defer d.wd.enter(wdFightPhase, "on the way", wdWaitFight)()
	began := d.clk.Now()
	lastSeen := began
	for {
		now := d.clk.Now()
		if eng := d.fr.EngagedHostiles(); len(eng) > 0 {
			lastSeen = now
			d.wd.mobHealth(hostilesHP(eng))
			d.watchThreat(eng)
			if err := d.tankFirstTick(ctx, eng); err != nil {
				return err
			}
			if err := d.assist(ctx, eng); err != nil {
				return err
			}
		} else if now.Sub(lastSeen) >= d.cfg.AggroQuiet {
			return nil
		}
		if now.Sub(began) > d.cfg.FightTimeout {
			return fmt.Errorf("the fight on the way lasted more than %.0f s", d.cfg.FightTimeout.Seconds())
		}
		if err := d.tick(ctx, d.cfg.Poll); err != nil {
			return err
		}
	}
}

// afterFight gives the bots a moment to loot, then deals with loose hostiles still hitting the party (adds).
func (d *RemoteDriver) afterFight(ctx context.Context) error {
	if err := d.afterFightWait(ctx); err != nil {
		return err
	}
	// The stay spot is set once the fight is over for good: the loot wait is past and nothing is hitting the party. A move sent earlier
	// would reset the lead bot's movement (and the followers' formation) in the middle of the loot or an add's fight.
	if len(d.fr.EngagedHostiles()) == 0 {
		d.holdHere(ctx)
	}
	return nil
}

func (d *RemoteDriver) afterFightWait(ctx context.Context) error {
	if err := d.tick(ctx, d.cfg.PostFight); err != nil {
		return err
	}
	deadline := d.clk.Now().Add(d.cfg.AddsWait)
	for d.clk.Now().Before(deadline) {
		var adds []tbm.HostileRec
		for _, h := range d.fr.EngagedHostiles() {
			if distToLead(d, hostilePoint(h)) <= 40 {
				adds = append(adds, h)
			}
		}
		if len(adds) == 0 {
			return nil
		}
		if err := d.assist(ctx, adds); err != nil {
			return err
		}
		if err := d.tick(ctx, d.cfg.Poll*2); err != nil {
			return err
		}
	}
	d.rep.note("loose hostile units were still hitting the party after " + d.cfg.AddsWait.String())
	return nil
}

// sneakPast is the pull of a stealthed group: nothing can be pulled, so the lead bot is walked through the group's positions (the
// followers come along) with a pause of AmbushPause at each, and what shows itself is fought.
func (d *RemoteDriver) sneakPast(ctx context.Context, label string, sp fightSpec) error {
	if err := d.restGate(ctx, true); err != nil {
		return err
	}
	d.legStat(func(l *LegReport) { l.Ambushes++ })
	if _, err := d.order(ctx, "follow"); err != nil {
		return err
	}
	fights := 0
	for _, at := range sp.anchors {
		if err := d.doWalk(ctx, at); err != nil {
			return err
		}
		pause := d.clk.Now().Add(d.cfg.AmbushPause)
		for d.clk.Now().Before(pause) {
			if err := d.tick(ctx, d.cfg.Poll); err != nil {
				return err
			}
			if len(d.fr.EngagedHostiles()) > 0 {
				fights++
				d.legStat(func(l *LegReport) { l.Pulls++ })
				if err := d.fightEngaged(ctx); err != nil {
					return err
				}
				if err := d.afterFight(ctx); err != nil {
					return err
				}
				break
			}
		}
	}
	left := 0
	for _, h := range d.candidates(sp, sp.pick) {
		if distToLead(d, hostilePoint(h)) <= 80 {
			left++
		}
	}
	switch {
	case fights > 0 && left == 0:
		d.rep.note(fmt.Sprintf("%s: stealthed group %v revealed itself and was killed (%d fights)", label, sp.pick, fights))
	case fights > 0:
		d.rep.note(fmt.Sprintf("%s: stealthed group %v: %d fights, %d members still alive in view; went on", label, sp.pick, fights, left))
	default:
		d.rep.note(fmt.Sprintf("%s: stealthed group %v passed with no fight; counted as cleared", label, sp.pick))
	}
	return nil
}

// holdSpotYd is how near the lead bot must stand to the point of its last move for holdHere to find its stay spot already here.
const holdSpotYd = 3.0

// holdHere makes the lead bot's "stay" spot the place it stands, once a fight is over (see afterFight). `cmd move` sets that spot to its point
// (BotCommands.cpp, CommanderMove), and after a fight the bot's "return to stay position" walks it back to the last point it was sent
// to: the party would run back, rest there, and run forward again. A move to its own position costs nothing and ends that. A refusal
// (the module may not take a move to where the bot already stands) is counted by order() and otherwise ignored.
//
// A lead bot that stands within holdSpotYd of the point of its last move already has its stay spot here (the module's return trigger
// fires only beyond the follow range), and a move to where a bot stands is one the server cannot path (`unreachable`: about 23
// refusals in a Uldaman run, hires-1 2026-10-14): none is sent then. (A run that has sent no move yet still sends it.)
func (d *RemoteDriver) holdHere(ctx context.Context) {
	here, ok := d.leadPos()
	if !ok || d.stale() {
		return
	}
	if d.haveMoveTo && dist3(here, d.moveTo) <= holdSpotYd {
		return
	}
	if d.moveGap(ctx) != nil {
		return
	}
	r, err := d.order(ctx, "move", moveArgs(here)...)
	if err != nil {
		return
	}
	if _, bad := refusedOf(r); !bad {
		d.remote(func(rr *RemoteReport) { rr.FightHolds++ })
	}
}

// unreachableStandoffs are the distances (yards from the mob) the party is walked to, one after the other, when the tank answers a pull
// with "unreachable"; the pull is sent again from each. Run 2 gave up on such packs and counted them cleared with their mobs alive.
var unreachableStandoffs = []float32{20, 8}

// noteUnreachable logs an "unreachable" answer (a pull, a move) with the point it was about, for the plan to be corrected: in the
// notes, the trace, and the report's counter.
func (d *RemoteDriver) noteUnreachable(kind string, at Point, what string) {
	d.remote(func(r *RemoteReport) { r.Unreachable++ })
	d.tr.Event("unreachable", map[string]any{"leg": d.curLeg, "kind": kind, "x": at.X, "y": at.Y, "z": at.Z, "what": what})
	d.rep.note(fmt.Sprintf("leg %d: unreachable %s to %.1f %.1f %.1f (%s)", d.curLeg, kind, at.X, at.Y, at.Z, what))
}

// maxTankBusyRounds bounds the waits of one pull for a tank that stays in combat.
const maxTankBusyRounds = 3

// waitTankFree waits up to TankBusyWait, polling the feed, until the tank is out of combat or t is engaged with the party. It reports
// whether t is engaged. The tank is the named (or inferred) one; with none known, the whole party stands for it.
func (d *RemoteDriver) waitTankFree(ctx context.Context, t tbm.HostileRec) (engaged bool, err error) {
	d.remote(func(r *RemoteReport) { r.TankBusyWaits++ })
	d.rep.note(fmt.Sprintf("pull of %s: the module answered tank-busy; waiting up to %.0f s for the tank to leave its fight", d.hostileName(t), d.opt.TankBusyWait.Seconds()))
	deadline := d.clk.Now().Add(d.opt.TankBusyWait)
	for {
		if err := d.tick(ctx, d.cfg.Poll); err != nil {
			return false, err
		}
		if h, ok := d.fr.Hostile(t.GUID); ok && d.engagedWith(h) {
			return true, nil
		}
		if !d.stale() && !d.tankFighting() {
			return false, nil
		}
		if !d.clk.Now().Before(deadline) {
			return false, nil
		}
	}
}

// tankFighting says whether the tank is in combat in the newest frame: the named one if it is in the party, else any bot.
func (d *RemoteDriver) tankFighting() bool {
	if d.tank != 0 {
		if m, ok := d.fr.Member(d.tank); ok {
			return m.InCombat()
		}
	}
	for _, p := range d.fr.Party {
		if p.InCombat() {
			return true
		}
	}
	return false
}
