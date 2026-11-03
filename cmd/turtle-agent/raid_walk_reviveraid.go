package main

import (
	"context"
	"fmt"
	"math"
	"sort"
	"strings"
	"time"

	"github.com/pjerra/turtle-agent/internal/dungeon"
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

// reviveBringBack is the revive-at-raid half of a self-regroup revive: the hires are alive again (the `.revive` loop
// and its wait are done), and the commander stands on p. Every living hire whose freshest frame is more than
// reviveFarYd from p is summoned to it, with a log line naming the distance it came from; a hire that is not within
// reviveArriveYd after reviveArriveWait gets one more summon; one that still does not come is named in a note and
// counted. The summons never end the walk: the gather wait that follows decides what the resume does about a hire
// that is still away.
func (w *walk) reviveBringBack(ctx context.Context, p dungeon.Point, deadline time.Time) error {
	w.reviveRead(ctx)
	now := w.env.now()
	var far []string
	from := map[string]float64{}
	for _, name := range w.hireList() {
		b, ok := w.botAt[name]
		if !ok || b.dead {
			continue
		}
		d := w.reviveDist(name, p, now)
		if d == reviveNoDist || d <= reviveFarYd {
			continue
		}
		far = append(far, name)
		from[name] = d
	}
	if len(far) == 0 {
		return nil
	}
	if w.reviveBrought == nil {
		w.reviveBrought = map[string]bool{}
	}
	for _, name := range far {
		w.logLine(w.env.now(), fmt.Sprintf("revive: %s brought back from %s", name, reviveYdWords(from[name])))
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
		if !near && name == w.mt {
			near = true // the lead is where the raid is, by definition
		}
		switch {
		case !near && b.inCombat:
			out = append(out, fmt.Sprintf("%s (%s away, in combat)", name, reviveYdWords(d)))
		case !near:
			out = append(out, fmt.Sprintf("%s (%s away)", name, reviveYdWords(d)))
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
