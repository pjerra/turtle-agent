package main

// The pre-placement boss-combat guard of a resumed or reused instance (walk 97 fight 9-1): after an exit 18 (the
// agent's RPC died mid-fight) the --reuse-instance restart resumes the bound instance while the boss and his arena adds
// are still engaged from the dead attempt. The quiet cannot see that: the adds stand beyond the 25 yd landing reach and
// their homes are not AHEAD-owned, so it dismisses them (BEHIND units never block), and the summon drops the raid into a
// live fight. Before the first `.summon`, the guard reads the plan boss's own combat state and every unit within the
// boss arena radius through the walk's existing `q units` read (the toolkit's `cb` field), waits for the arena to reset,
// and otherwise asks for a fresh instance through the existing exit 29 path (the autopilot's one fresh-instance retry).

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/pjerra/turtle-agent/internal/planexport"
)

const (
	// placeBossArenaYd is the radius of the boss-arena read around the plan boss's anchor: the boss's own aggro range
	// (20 yd plus the level difference) and his arena adds' spawns. Walk 97's Hakkar fight kept the boss and the altar
	// adds engaged; the adds stood beyond the quiet's landing reach, so the quiet let the summon through.
	placeBossArenaYd = 60
	// placeBossReachYd is how far (2D) a boss anchor may stand from the placement point and still be read: the toolkit
	// refuses a read more than 150 yd from the caller (point_far), and a boss whose arena is that far from the landing
	// cannot reach the raid the summon drops there.
	placeBossReachYd = 150
	// placeBossResetWait is how long the guard waits for a live fight in the arena to reset itself before it asks for a
	// fresh instance: a real client's boss evades and resets in well under a minute once nothing holds him.
	placeBossResetWait = 90 * time.Second
	// placeBossResetPoll is how often the arena is read again during that wait.
	placeBossResetPoll = 5 * time.Second
)

// placeBossAnchor is one plan boss row the guard reads: the row's step (1-based, for the notes), its entry and its
// position, which is the boss's anchor.
type placeBossAnchor struct {
	Step    int
	Entry   uint32
	X, Y, Z float32
}

// placeBossAnchors is every plan boss anchor near the placement point, in route order: a boss whose arena stands within
// placeBossReachYd (2D) of the landing. A boss behind the start step counts too: the autopilot may resume a later row
// (or another leg) while an earlier leg's boss is still engaged, and its arena is as near the landing either way.
func (w *walk) placeBossAnchors() []placeBossAnchor {
	if w.place == nil || w.place.Start < 1 || w.place.Start > len(w.rows.Steps) {
		return nil
	}
	p := w.place.Point
	var out []placeBossAnchor
	for i, s := range w.rows.Steps {
		if s.Kind != planexport.KindBoss || s.Entry == 0 {
			continue
		}
		if s.X == 0 && s.Y == 0 {
			// A boss row without its own point (a boss fought on the spot carries the hold row's anchor): nothing to
			// read around.
			continue
		}
		if placeDist2(s.X, s.Y, p.X, p.Y) > placeBossReachYd {
			continue
		}
		out = append(out, placeBossAnchor{Step: i + 1, Entry: s.Entry, X: s.X, Y: s.Y, Z: s.Z})
	}
	return out
}

// placeBossEngaged reads every anchor's arena (`q units <60> <anchor> all`) and reports whether any unit there is in
// combat, the boss included, with the words naming the fight for the notes. A read the toolkit refuses as out of reach
// (point_far) is no evidence but also no risk: the arena is farther than 150 yd from the commander, so nothing in it can
// reach the landing. Any other refusal is a failure: the guard never places on a read it could not make. A truncated
// reply (more units than one reply carries) fails closed as engaged: an arena that crowded is not a place to land.
func (w *walk) placeBossEngaged(ctx context.Context, anchors []placeBossAnchor) (bool, string, error) {
	var fighting []string
	for _, a := range anchors {
		cmd := fmt.Sprintf("q units %d %s %s %s all", placeBossArenaYd, num32(a.X), num32(a.Y), num32(a.Z))
		out, err := w.tt(ctx, cmd)
		if err != nil {
			if strings.Contains(err.Error(), "point_far") {
				w.note("place: the boss arena of step %d (%d) stands beyond the toolkit's read reach from the commander: a fight there cannot reach the landing and is not read", a.Step, a.Entry)
				continue
			}
			return false, "", stopf(exitRaidWalkFailed, "place: q units at the boss arena of step %d (%d): %v", a.Step, a.Entry, err)
		}
		body, ok := ttLast(out)
		if !ok {
			return false, "", stopf(exitRaidWalkFailed, "place: q units at the boss arena of step %d (%d) gave no reply", a.Step, a.Entry)
		}
		if body.flag("trunc") {
			fighting = append(fighting, fmt.Sprintf("step %d's arena lists more units than one reply carries (trunc)", a.Step))
			continue
		}
		for _, m := range body.list("units") {
			u := ttUnitOf(m)
			if !u.InCombat || u.Dead {
				continue
			}
			fighting = append(fighting, fmt.Sprintf("%d %s %.0f yd from the boss anchor of step %d", u.Entry, u.Name, u.Dist, a.Step))
		}
	}
	if len(fighting) == 0 {
		return false, "", nil
	}
	return true, "the boss arena is engaged: " + strings.Join(fighting, ", "), nil
}

// placeBossCombat is the guard itself: before a resumed or reused placement summons anything, wait for a live boss fight
// in the landing's arenas to reset, and when it does not, stop with exit 29 and a reason that asks for a fresh instance
// (the autopilot restarts the goal without --reuse-instance: the fresh placement path, which unbinds the stale bind).
// The decision is always logged: the clean read is silent (an ordinary placement), an engaged read is a note, and so are
// the reset and the give-up.
func (w *walk) placeBossCombat(ctx context.Context) error {
	anchors := w.placeBossAnchors()
	if len(anchors) == 0 {
		return nil
	}
	engaged, words, err := w.placeBossEngaged(ctx, anchors)
	if err != nil {
		return err
	}
	if !engaged {
		return nil
	}
	w.note("place: --reuse-instance: %s: no placement into a live boss fight; waiting up to %s for the reset",
		words, durWords(placeBossResetWait))
	start := w.env.now()
	deadline := start.Add(placeBossResetWait)
	for reads := 2; ; reads++ {
		if !w.env.now().Before(deadline) {
			return stopf(exitRaidWalkPlaceFailed,
				"place: --reuse-instance: %s; the arena did not reset in %s: fresh instance needed, no placement into a live boss fight",
				words, durWords(w.env.now().Sub(start)))
		}
		if err := w.placeSleep(ctx, placeBossResetPoll); err != nil {
			return err
		}
		engaged, words, err = w.placeBossEngaged(ctx, anchors)
		if err != nil {
			return err
		}
		if !engaged {
			w.note("place: --reuse-instance: the boss arena reset after %s (%d reads): placing into the instance",
				durWords(w.env.now().Sub(start)), reads)
			return nil
		}
	}
}
