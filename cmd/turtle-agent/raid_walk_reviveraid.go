package main

import (
	"context"
	"fmt"
	"math"
	"sort"
	"strings"
	"time"

	"github.com/pjerra/turtle-agent/internal/dungeon"
	"github.com/pjerra/turtle-agent/internal/raidwalk"
)

// --revive-at-raid (walk 29, run-to-raid deaths). After a wipe TB releases every bot, the ghosts start their corpse
// run from the graveyard, and the self-regroup `.revive`s each hire wherever its ghost has got to: 300-650 yd from the
// raid, with uncleared packs in between. The runners died on the way (57 of 188 deaths in walk 29 runs a, b and c).
// With the flag on (the default) the recovery brings every hire revived more than reviveFarYd from the revive point
// back to it with the GM `.summon <hire>` the place phase already uses (the commander stands on the point, the hire
// lands on the commander; the allow-list takes `.summon` for a hire and nothing else), and the resume waits for the
// raid to gather before it goes on. The 3 s portal summon TB uses cancels on combat; the GM line does not.
const (
	reviveFarYd      = 100                 // a hire revived farther than this from the revive point is summoned to it
	reviveArriveYd   = 10                  // a summoned hire must stand within this of the point (x/y) ...
	reviveArriveZYd  = 30                  // ... and this in z (a landing may drop to the floor)
	reviveArriveWait = 15 * time.Second    // how long one summon gets to land before the retry
	reviveGatherWait = 60 * time.Second    // how long the resume waits for every hire to be in range and out of combat
	reviveReadStep   = 2 * time.Second     // the poll step of both waits
	reviveAliveWait  = 20 * time.Second    // how long a revive waits for every revived hire's frame to show it alive
	reviveAlivePoll  = time.Second         // the poll step of that wait
	reviveFrameFresh = 10 * time.Second    // a hire's frame older than this says nothing about where it is
	reviveNoDist     = -1.0                // reviveDist: no fresh frame of this hire
	reviveOffMap     = math.MaxFloat64 / 4 // reviveDist: the hire is on another map than the plan's
)

// reviveRead polls one frame into the walk's state, the way completeDeadView does.
func (w *walk) reviveRead(ctx context.Context) {
	if n, frame, err := w.view(ctx, w.afterN, 2000); err == nil && frame != nil {
		w.afterN = n
		now := w.env.now()
		w.trackFrame(now, frame)
		w.tracker.Frame(now, *frame)
		w.saveFrame(now, frame)
	}
}

// reviveDist is how far (x/y) the hire's freshest frame is from p: reviveNoDist without a fresh frame, reviveOffMap
// on another map.
func (w *walk) reviveDist(name string, p dungeon.Point, now time.Time) float64 {
	b, ok := w.botAt[name]
	if !ok || now.Sub(b.at) > reviveFrameFresh {
		return reviveNoDist
	}
	if b.mapID != w.plan.Map {
		return reviveOffMap
	}
	return math.Hypot(float64(b.x-p.X), float64(b.y-p.Y))
}

// reviveArrived says the hire stands on p: alive, fresh, on the plan's map, within reviveArriveYd (x/y) and
// reviveArriveZYd (z).
func (w *walk) reviveArrived(name string, p dungeon.Point, now time.Time) bool {
	b, ok := w.botAt[name]
	if !ok || now.Sub(b.at) > reviveFrameFresh || b.dead || b.mapID != w.plan.Map {
		return false
	}
	return math.Hypot(float64(b.x-p.X), float64(b.y-p.Y)) <= reviveArriveYd && math.Abs(float64(b.z-p.Z)) <= reviveArriveZYd
}

func reviveYdWords(d float64) string {
	switch {
	case d == reviveNoDist:
		return "position unknown"
	case d >= reviveOffMap:
		return "another map"
	}
	return fmt.Sprintf("%d yd", int(math.Round(d)))
}

func reviveAwayWords(d float64) string {
	if d >= reviveOffMap {
		return "on another map"
	}
	return reviveYdWords(d) + " away"
}

// reviveSeen is what a hire's freshest frame says about the revive. Only a frame read since the revive counts: a frame
// from before it shows the hire as the revive found it (dead, or not in the frame at all).
type reviveSeen int

const (
	reviveSeenUnknown reviveSeen = iota // no frame since the revive shows the hire: none read, a failed view, or absent
	reviveSeenAlive
	reviveSeenDead
)

func (w *walk) reviveStateSince(name string, since time.Time) reviveSeen {
	b, ok := w.botAt[name]
	switch {
	case !ok || b.at.Before(since):
		return reviveSeenUnknown
	case b.dead:
		return reviveSeenDead
	}
	return reviveSeenAlive
}

// reviveAwaitAlive reads the view one frame a second until every hire in names has a frame read since `since` that
// shows it alive, or reviveAliveWait passes, whichever is sooner and never past the recovery's deadline. The state
// it leaves is what reviveSummonList judges by. A failed read is not an error: the hire stays unknown until a frame
// says otherwise.
func (w *walk) reviveAwaitAlive(ctx context.Context, names []string, since, deadline time.Time) error {
	end := w.env.now().Add(reviveAliveWait)
	if !deadline.IsZero() && deadline.Add(-reviveAlivePoll).Before(end) {
		end = deadline.Add(-reviveAlivePoll)
	}
	for {
		w.reviveRead(ctx)
		waiting := false
		for _, name := range names {
			if w.reviveStateSince(name, since) != reviveSeenAlive {
				waiting = true
				break
			}
		}
		if !waiting || !w.env.now().Before(end) {
			return nil
		}
		if err := w.recoverySleep(ctx, reviveAlivePoll, deadline); err != nil {
			return err
		}
	}
}

// reviveSummonList is who the revive-at-raid summons go to, judged by each hire's frame since `since`: a hire shown
// alive within reviveFarYd of p stays where it is; any other hire goes, far or on another map, and also a hire still
// shown dead or not shown at all, which is summoned anyway (a summon of a living hire is harmless, a summon of a dead
// one fails visibly). The dead and unknown ones are named in a note.
func (w *walk) reviveSummonList(names []string, p dungeon.Point, since time.Time) []string {
	now := w.env.now()
	var out, unseen []string
	for _, name := range names {
		switch w.reviveStateSince(name, since) {
		case reviveSeenAlive:
			if d := w.reviveDist(name, p, now); d != reviveNoDist && d <= reviveFarYd {
				continue
			}
		case reviveSeenDead:
			unseen = append(unseen, name+" (shown dead)")
		default:
			unseen = append(unseen, name+" (state unknown)")
		}
		out = append(out, name)
	}
	if len(unseen) > 0 {
		w.note("revive-at-raid: %d hire(s) not shown alive %s after the revive, summoned to the point anyway: %s", len(unseen), durWords(reviveAliveWait), strings.Join(unseen, ", "))
	}
	return out
}

// reviveBringBack is the revive-at-raid half of a self-regroup revive: the hires are alive again (the `.revive` loop
// and its wait are done), and the commander stands on p. The frames are polled first (reviveAwaitAlive), then every
// hire reviveSummonList names is summoned to p, with a log line naming the distance it came from; a hire that is not
// within reviveArriveYd after reviveArriveWait gets one more summon; one that still does not come is named in a note
// and counted. The summons never end the walk: the gather wait that follows decides what the resume does about a
// hire that is still away.
func (w *walk) reviveBringBack(ctx context.Context, p dungeon.Point, deadline time.Time) error {
	since := w.env.now()
	names := w.hireList()
	if err := w.reviveAwaitAlive(ctx, names, since, deadline); err != nil {
		return err
	}
	far := w.reviveSummonList(names, p, since)
	if len(far) == 0 {
		return nil
	}
	return w.reviveSummonAll(ctx, far, p, deadline)
}

// reviveSummonAll summons the far hires to p (the commander stands there): the `revive: <bot> brought back from <dist>
// yd` line per hire, one retry for a hire not within reviveArriveYd after reviveArriveWait, a note naming the ones that
// still do not come.
func (w *walk) reviveSummonAll(ctx context.Context, far []string, p dungeon.Point, deadline time.Time) error {
	if w.reviveBrought == nil {
		w.reviveBrought = map[string]bool{}
	}
	now := w.env.now()
	for _, name := range far {
		w.logLine(now, fmt.Sprintf("revive: %s brought back from %s", name, reviveYdWords(w.reviveDist(name, p, now))))
		w.reviveBrought[name] = true
	}
	pending := far
	for attempt := 1; attempt <= 2 && len(pending) > 0; attempt++ {
		for _, name := range pending {
			if timeout := w.recoveryExpired(deadline); timeout != nil {
				return timeout
			}
			if err := w.gm(ctx, ".summon "+name); err != nil {
				return err
			}
			if err := w.recoverySleep(ctx, 100*time.Millisecond, deadline); err != nil {
				return err
			}
		}
		end := w.env.now().Add(reviveArriveWait)
		for {
			if err := w.recoverySleep(ctx, reviveReadStep, deadline); err != nil {
				return err
			}
			w.reviveRead(ctx)
			now = w.env.now()
			var still []string
			for _, name := range pending {
				if !w.reviveArrived(name, p, now) {
					still = append(still, name)
				}
			}
			pending = still
			if len(pending) == 0 || !now.Before(end) {
				break
			}
		}
		if len(pending) > 0 && attempt == 1 {
			for _, name := range pending {
				w.logLine(w.env.now(), fmt.Sprintf("revive: %s is still %s from the point after the summon: summoning once more", name, reviveYdWords(w.reviveDist(name, p, w.env.now()))))
			}
		}
	}
	if w.reviveUnreached == nil {
		w.reviveUnreached = map[string]bool{}
	}
	var gone []string
	for _, name := range pending {
		w.reviveUnreached[name] = true
		gone = append(gone, fmt.Sprintf("%s (%s)", name, reviveYdWords(w.reviveDist(name, p, w.env.now()))))
	}
	w.note("revive-at-raid: %d hire(s) brought back to the revive point from over %d yd away", len(far), reviveFarYd)
	if len(gone) > 0 {
		w.note("revive-at-raid: %d hire(s) did not come within %d yd after two summons: %s", len(gone), reviveArriveYd, strings.Join(gone, ", "))
	}
	return nil
}

// reviveStragglers are the hires keeping the raid from being gathered at now: no fresh frame, dead, out of range of
// the revive point p AND of the living lead bot (TB moves the lead and gathers the party itself, so the lead's
// position counts as the raid too), or in combat. Each entry says why.
func (w *walk) reviveStragglers(p dungeon.Point, now time.Time) []string {
	var lead *botPos
	if b, ok := w.botAt[w.mt]; ok && !b.dead && now.Sub(b.at) <= reviveFrameFresh && b.mapID == w.plan.Map {
		lead = &b
	}
	var out []string
	for _, name := range w.hireList() {
		b, ok := w.botAt[name]
		switch {
		case !ok || now.Sub(b.at) > reviveFrameFresh:
			out = append(out, name+" (no fresh position)")
			continue
		case b.dead:
			out = append(out, name+" (dead)")
			continue
		}
		d := w.reviveDist(name, p, now)
		near := d <= reviveFarYd
		if !near && lead != nil && name != w.mt && b.mapID == w.plan.Map {
			near = math.Hypot(float64(b.x-lead.x), float64(b.y-lead.y)) <= reviveFarYd
		}
		// The lead is where the raid is only while it is fresh, alive and on the plan's map (the switch above sent the
		// first two away); a lead on another map is a straggler like any hire and is named after the timeout.
		if !near && name == w.mt && b.mapID == w.plan.Map {
			near = true
		}
		switch {
		case !near && b.inCombat:
			out = append(out, fmt.Sprintf("%s (%s, in combat)", name, reviveAwayWords(d)))
		case !near:
			out = append(out, fmt.Sprintf("%s (%s)", name, reviveAwayWords(d)))
		case b.inCombat:
			out = append(out, name+" (in combat)")
		}
	}
	return out
}

// reviveGather is the resume gate of --revive-at-raid: it waits up to reviveGatherWait (and never past the recovery's
// deadline) for every hire to be in range of the raid and out of combat, then returns. A hire still missing at the
// end does not stop the resume; it is named in a note and counted.
func (w *walk) reviveGather(ctx context.Context, deadline time.Time) error {
	if !w.opts.reviveAtRaid || w.reviveSpot == nil {
		return nil
	}
	p := *w.reviveSpot
	end := w.env.now().Add(reviveGatherWait)
	// Never past the recovery's deadline: a sleep that crosses it would end the walk, a note does not.
	if cap := deadline.Add(-2 * reviveReadStep); !deadline.IsZero() && cap.Before(end) {
		end = cap
	}
	var missing []string
	for {
		w.reviveRead(ctx)
		missing = w.reviveStragglers(p, w.env.now())
		if len(missing) == 0 {
			return nil
		}
		if !w.env.now().Before(end) {
			break
		}
		if err := w.recoverySleep(ctx, reviveReadStep, deadline); err != nil {
			return err
		}
	}
	if w.reviveGatherMissed == nil {
		w.reviveGatherMissed = map[string]bool{}
	}
	for _, m := range missing {
		w.reviveGatherMissed[strings.Fields(m)[0]] = true
	}
	sort.Strings(missing)
	w.note("revive-at-raid: resuming with %d hire(s) not gathered after %s: %s", len(missing), durWords(reviveGatherWait), strings.Join(missing, ", "))
	return nil
}

// reviveRaidAnchor is the hire the near-wipe revive treats as the raid's place: a living hire the walk did not just
// revive, fresh, on the plan's map; the lead first, else the first in roster order.
func (w *walk) reviveRaidAnchor(revived map[string]bool, now time.Time) (string, botPos) {
	for _, name := range w.hireList() {
		b, ok := w.botAt[name]
		if !ok || revived[name] || b.dead || b.mapID != w.plan.Map || now.Sub(b.at) > reviveFrameFresh {
			continue
		}
		return name, b
	}
	return "", botPos{}
}

// reviveBringBackNear is --revive-at-raid for a near-wipe stop: someone is alive, so the raid is where the living
// hires stand, and the GM is nowhere near it. The hires this recovery revived (dead lists them) are revived where
// their corpses lie, which may be far from the raid; each one more than reviveFarYd from the anchor (or on another
// map, or not shown alive by a frame since the revive) is brought to it. The commander first `.appear`s at the anchor, so the `.summon`s land at the raid and not at
// the GM's last spot (walk 22 F4, walk 29 S5). It records the anchor's position as the point the resume gate measures.
func (w *walk) reviveBringBackNear(ctx context.Context, dead []string, deadline time.Time) error {
	if !w.opts.reviveAtRaid || len(dead) == 0 {
		return nil
	}
	since := w.env.now()
	w.reviveRead(ctx)
	revived := map[string]bool{}
	for _, n := range dead {
		revived[n] = true
	}
	// The anchor is a living hire the revive did not touch, so it is picked before the poll: a view that fails during
	// the poll must not cost the walk its place to bring the hires to.
	name, anchor := w.reviveRaidAnchor(revived, w.env.now())
	if name == "" {
		w.note("revive-at-raid: no living hire on map %d to bring the revived hires to", w.plan.Map)
		return nil
	}
	spot := dungeon.Point{X: anchor.x, Y: anchor.y, Z: anchor.z}
	w.reviveSpot = &spot
	// The revived hires' frames are polled (a hire whose alive frame comes late is still brought back); the summons
	// then go to every hire that is not shown alive within reviveFarYd of the anchor.
	if err := w.reviveAwaitAlive(ctx, dead, since, deadline); err != nil {
		return err
	}
	far := w.reviveSummonList(dead, spot, since)
	if len(far) == 0 {
		return nil
	}
	if err := w.gm(ctx, raidwalk.GMVisibleOff); err != nil {
		return err
	}
	if ok, why, err := w.appearAtHire(ctx, name, deadline); err != nil {
		return err
	} else if !ok {
		w.note("revive-at-raid: could not appear at %s to bring %d revived hire(s) back (%s)", name, len(far), why)
		return nil
	}
	return w.reviveSummonAll(ctx, far, spot, deadline)
}
