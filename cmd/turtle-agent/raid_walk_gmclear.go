// raid_walk_gmclear.go: the `--gm-clear` test tool of raid-walk (owner-approved, test servers only, off by default). After the placement
// and before the raid moves, the commander GM-kills every live unit of the named pull groups (or a leg's boss) so a later leg can be
// tested while the trash before it is still being fixed. Every unit it kills goes into run.log, summary.json (gm_clear, assisted) and the
// report, and the walk's result line says ASSISTED: it is never a real clear.
//
// The kill is the core's `.die` on the SELECTED unit (Chat.cpp:885, SEC_DEVELOPER = account level 3; Commands.cpp:2795
// HandleDieCommand). The agent's gm_kill RPC selects the creature by guid, waits for the server to show the selection and sends the
// line (session.GMKill over GMOn): the walk never sends `.die` through its own allow-listed gm path.
package main

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/pjerra/turtle-agent/internal/objects"
	"github.com/pjerra/turtle-agent/internal/packet"
	"github.com/pjerra/turtle-agent/internal/raidwalk"
	"github.com/pjerra/turtle-agent/internal/slots"
	"github.com/pjerra/turtle-agent/internal/worldspawn"
)

const (
	// gmClearReadYd is how far around a pack's centroid the commander reads units: the object store holds what the client would see.
	gmClearReadYd = 150
	// gmClearReadLimit is the units RPC's row cap for that read (the default 100 would cut a crowded room).
	gmClearReadLimit = 2000
	// gmClearRounds is how many times a pack is read and cleared: a second read catches members that were out of view or came in.
	gmClearRounds = 2
	// gmClearDeathWait and gmClearDeathPoll are how long a kill is given to show in the unit's frames, and how often it is read.
	gmClearDeathWait = 5 * time.Second
	gmClearDeathPoll = 500 * time.Millisecond
	// gmClearTries is how many kill lines one unit gets: the first and one retry.
	gmClearTries = 2
)

// validateGMClear expands the specs into packs from the plan's rows and the world export, before any GM command.
func (w *walk) validateGMClear() error {
	specs, err := parseGMClearSpecs(w.opts.gmClear, w.opts.gmClearBoss)
	if err != nil {
		return stopf(exitUsage, "%v", err)
	}
	dir := w.opts.gmClearSpawns
	if dir == "" {
		dir = filepath.Join(slots.ExpandTilde(w.opts.dir), "..", "tools", "dungeongen", "testdata", fmt.Sprintf("map%d", w.plan.Map))
	}
	if _, err := os.Stat(filepath.Join(dir, "creatures.tsv")); err != nil {
		return stopf(exitUsage, "--gm-clear: no world export in %s: pass --gm-clear-spawns DIR (tools/dungeongen/export.sh writes it)", dir)
	}
	world, _, err := worldspawn.Load(dir, w.plan.Map)
	if err != nil {
		return stopf(exitUsage, "--gm-clear: %v", err)
	}
	packs, err := raidwalk.PlanGMClear(w.rows, world, specs)
	if err != nil {
		return stopf(exitUsage, "%v", err)
	}
	w.gmClear = packs
	w.note("--gm-clear: %d packs to kill by GM command (test tool; the walk is ASSISTED, not a real clear)", len(packs))
	return nil
}

// gmClearUnits reads the objects the commander's client holds around it (no hostile filter: the pack decides who matters).
func (w *walk) gmClearUnits(ctx context.Context) ([]objects.Unit, error) {
	var us []objects.Unit
	err := w.agent.Call(ctx, "units", map[string]any{"within": gmClearReadYd, "limit": gmClearReadLimit}, &us, 15*time.Second)
	return us, err
}

// gmClearDead says whether the unit's frames show it dead. A unit the client no longer holds is not confirmed dead.
func (w *walk) gmClearDead(ctx context.Context, g packet.GUID) bool {
	var u objects.Unit
	if err := w.agent.Call(ctx, "unit", map[string]string{"guid": g.String()}, &u, 10*time.Second); err != nil {
		return false
	}
	return u.Dead || (u.MaxHealth > 0 && u.Health == 0)
}

// gmClearKill sends the kill line for one unit (up to gmClearTries times) and reads its death from the frames after each. It returns the
// number of lines sent and whether the death was seen.
func (w *walk) gmClearKill(ctx context.Context, u objects.Unit) (tries int, dead bool, err error) {
	for tries < gmClearTries {
		tries++
		w.logLine(w.env.now(), fmt.Sprintf("> .die (gm_kill, selected %s)", u.GUID))
		if err := w.agent.Call(ctx, "gm_kill", map[string]string{"guid": u.GUID.String()}, nil, 15*time.Second); err != nil {
			w.note("gm-clear: gm_kill %s: %v", u.GUID, err)
			if ctx.Err() != nil {
				return tries, false, ctx.Err()
			}
			continue
		}
		for waited := time.Duration(0); waited < gmClearDeathWait; waited += gmClearDeathPoll {
			if err := w.env.sleep(ctx, gmClearDeathPoll); err != nil {
				return tries, false, err
			}
			if w.gmClearDead(ctx, u.GUID) {
				return tries, true, nil
			}
		}
	}
	return tries, false, nil
}

// placeGMClear is place step 6b: for every pack, the commander goes to the pack, reads the units around it, and kills every live
// member. A member that will not die is recorded and noted, and the walk goes on (the summary says so). The commander then returns to
// the start point, on its floor, before the quiet check and the raid's move.
func (w *walk) placeGMClear(ctx context.Context) error {
	if len(w.gmClear) == 0 {
		return nil
	}
	w.gmClearUsed = true
	p := w.place.Point
	for _, pk := range w.gmClear {
		label := raidwalk.GMClearLabel(pk.Leg, pk.Group)
		if err := w.goPlace(ctx, pk.Centroid.X, pk.Centroid.Y, pk.Centroid.Z+2); err != nil {
			return err
		}
		if err := w.placeSleep(ctx, 2*time.Second); err != nil {
			return err
		}
		if err := w.placeInstanceIs(ctx, w.placeInst); err != nil {
			return err
		}
		done := map[packet.GUID]int{} // unit -> index into gmClearLog
		for round := 0; round < gmClearRounds; round++ {
			units, err := w.gmClearUnits(ctx)
			if err != nil {
				return stopf(exitRaidWalkFailed, "gm-clear: units read at %s: %v", label, err)
			}
			live := pk.LiveMembers(units)
			if len(live) == 0 {
				break
			}
			for _, u := range live {
				if i, ok := done[u.GUID]; ok && !w.gmClearLog[i].Killed {
					continue // it would not die once already: a third line would not change that
				}
				tries, dead, err := w.gmClearKill(ctx, u)
				if err != nil {
					return stopf(exitRaidWalkFailed, "gm-clear: interrupted at %s: %v", label, err)
				}
				e := raidwalk.GMClearEntry{Leg: pk.Leg, Group: pk.Group, Entry: u.GUID.Entry(), GUID: u.GUID.String(), Killed: dead, Tries: tries}
				done[u.GUID] = len(w.gmClearLog)
				w.gmClearLog = append(w.gmClearLog, e)
				if dead {
					w.logLine(w.env.now(), raidwalk.GMClearLogLine(e))
				} else {
					w.note("gm-clear: unit %d %s (%s) would not die after %d .die lines", e.Entry, e.GUID, label, tries)
					w.logLine(w.env.now(), fmt.Sprintf("gm-clear: FAILED %d %s (%s) alive after %d tries", e.Entry, e.GUID, label, tries))
				}
			}
		}
	}
	killed, failed := raidwalk.GMClearCounts(w.gmClearLog)
	w.note("gm-clear: ASSISTED, %d units killed by GM command, %d would not die; this walk is not a real clear", killed, failed)
	// Back to the start point, on its floor, as the corridor clear's last position was.
	if err := w.goPlace(ctx, p.X, p.Y, p.Z+2); err != nil {
		return err
	}
	if err := w.placeSleep(ctx, 2*time.Second); err != nil {
		return err
	}
	if err := w.placeInstanceIs(ctx, w.placeInst); err != nil {
		return err
	}
	return w.placeFloor(ctx, p)
}
