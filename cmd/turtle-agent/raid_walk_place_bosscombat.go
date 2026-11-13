package main

// The pre-placement boss-combat guard of a resumed or reused instance (walk 97 fight 9-1): after an exit 18 (the
// agent's RPC died mid-fight) the --reuse-instance restart resumes the bound instance while the boss and his arena adds
// are still engaged from the dead attempt. The quiet cannot see that: the adds stand beyond the 25 yd landing reach and
// their homes are not AHEAD-owned, so it dismisses them (BEHIND units never block), and the summon drops the raid into a
// live fight. Before the first `.summon`, the guard reads every boss arena near the landing — the plan boss's own
// combat state and every unit within the arena radius — through the walk's existing `q units` read (the toolkit's `cb`
// field), waits for the arena to reset, and otherwise asks for a fresh instance through the existing exit 29 path (the
// autopilot's one fresh-instance retry). A boss fought on the spot has no boss row: the exporter writes his encounter as
// a hold row commented "boss hold" whose entries list the boss first, and that row's spot is his arena. The guard runs
// twice: before the GM clear and the quiet, and again as the final gate immediately before the first summon, because
// those run in between and a reused copy keeps live raid members that can start a fight meanwhile. Only the first read
// waits for a reset; the final gate does not, because a patrol can walk into the landing during that wait and no read
// after it would see the patrol before the summon (exit 29 at once).

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

// placeBossAnchor is one boss encounter row the guard reads: the row's step (1-based, for the notes), its boss entry,
// and its position, which is the boss's anchor (a boss row's own point, a boss hold's spot).
type placeBossAnchor struct {
	Step    int
	Entry   uint32
	X, Y, Z float32
}

// placeBossAnchors is every plan boss anchor near the placement point, in route order: a boss whose arena stands within
// placeBossReachYd (2D) of the landing. A boss behind the start step counts too: the autopilot may resume a later row
// (or another leg) while an earlier leg's boss is still engaged, and its arena is as near the landing either way.
//
// A boss fought on the spot has no KindBoss row: the exporter writes his encounter as a KindHold row whose comment
// starts "boss hold" (raidwalk.isBossRow), the row's spot the fight position and its entries the boss first, then his
// adds (Blackwing Lair's Vaelastrasz and Nefarian, Zul'Farrak's arena). Those rows are anchors too: the round-1 guard
// read only KindBoss rows, so an engaged boss hold went unseen and the summon dropped the raid into it.
func (w *walk) placeBossAnchors() []placeBossAnchor {
	if w.place == nil || w.place.Start < 1 || w.place.Start > len(w.rows.Steps) {
		return nil
	}
	p := w.place.Point
	var out []placeBossAnchor
	for i, s := range w.rows.Steps {
		entry := s.Entry
		switch {
		case s.Kind == planexport.KindBoss && s.Entry != 0:
		case s.Kind == planexport.KindHold && strings.HasPrefix(s.Comment, placeBossHoldComment) && len(s.Entries) > 0:
			// The hold row's entry is the instance data (or 0) that ends the event, not the boss: the entries carry him.
			entry = s.Entries[0]
		default:
			continue
		}
		if s.X == 0 && s.Y == 0 {
			// A boss row without its own point: nothing to read around.
			continue
		}
		if placeDist2(s.X, s.Y, p.X, p.Y) > placeBossReachYd {
			continue
		}
		out = append(out, placeBossAnchor{Step: i + 1, Entry: entry, X: s.X, Y: s.Y, Z: s.Z})
	}
	return out
}

// placeBossHoldComment starts the comment of a boss's hold row (planexport: b.hold(bs.Hold, ..., "boss hold")): a boss
// fought on the spot, which has no KindBoss row. It mirrors raidwalk.isBossRow's own rule.
const placeBossHoldComment = "boss hold"

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

// placeBossCombat is the guard's first half: before a resumed or reused placement clears anything or reads the quiet,
// wait for a live boss fight in the landing's arenas to reset, and when it does not, stop with exit 29 and a reason that
// asks for a fresh instance (the autopilot restarts the goal without --reuse-instance: the fresh placement path, which
// unbinds the stale bind). The decision is always logged: the clean read is silent (an ordinary placement), an engaged
// read is a note, and so are the reset and the give-up.
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
	return w.placeBossReset(ctx, anchors, words)
}

// placeBossCombatFinal is the guard's second half and the final gate: every arena is read again immediately before the
// first `.summon`, after the GM clear and the quiet. Those run for seconds, and the reused copy keeps its online raid
// members, so a fight can begin after the first read; the round-1 guard read once and let the summon through. An
// engaged read does not wait for the reset: the read is final, and a wait would re-open the race this gate closes (a
// patrol can walk into the landing during it, after the last read). It fails straight to the fresh instance (exit 29).
func (w *walk) placeBossCombatFinal(ctx context.Context) error {
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
	w.note("place: --reuse-instance: %s: the fight began after the first read: the final gate waits for no reset; fresh instance needed",
		words)
	return stopf(exitRaidWalkPlaceFailed,
		"place: --reuse-instance: %s: the fight began after the first read: fresh instance needed, no placement into a live boss fight",
		words)
}

// placeBossReset is the first half's wait: poll every placeBossResetPoll for up to placeBossResetWait from now, and when
// the arena clears, say so and let the placement go on. A fight still on at the deadline needs a fresh instance.
func (w *walk) placeBossReset(ctx context.Context, anchors []placeBossAnchor, words string) error {
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
		engaged, now, err := w.placeBossEngaged(ctx, anchors)
		if err != nil {
			return err
		}
		words = now
		if !engaged {
			w.note("place: --reuse-instance: the boss arena reset after %s (%d reads): placing into the instance",
				durWords(w.env.now().Sub(start)), reads)
			return nil
		}
	}
}
