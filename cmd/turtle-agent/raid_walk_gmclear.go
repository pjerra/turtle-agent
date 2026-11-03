// raid_walk_gmclear.go: the `--gm-clear` test tool of raid-walk (owner-approved, test servers only, off by default). After the placement
// and before the raid moves, the commander GM-kills every live unit of the named pull groups (or a leg's boss) so a later leg can be
// tested while the trash before it is still being fixed. Every unit it kills goes into run.log, summary.json (gm_clear, assisted) and the
// report, and the walk's result line says ASSISTED: it is never a real clear. A group whose row holds no hostile pack (a boss's
// or an event's units) is skipped with a gm-clear: WARN note; the other groups still run and the walk never aborts for it.
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
	// gmClearReadYd is how far around a visit stop the commander reads units: the object store holds what the client would see. It is
	// wider than raidwalk.GMClearStopYd, the distance a stop covers.
	gmClearReadYd = 150
	// gmClearReadLimit is the units RPC's row cap for that read (the default 100 would cut a crowded room).
	gmClearReadLimit = 2000
	// gmClearRounds is how many times a pack's stops are visited and read: both always run, so a first empty read ends nothing.
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
	packs, warnings, err := raidwalk.PlanGMClear(w.rows, world, specs)
	if err != nil {
		return stopf(exitUsage, "%v", err)
	}
	w.gmClearWarn = warnings
	for _, warn := range warnings {
		w.note("%s", warn)
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

// gmClearMember is what the walk knows about one expected pack member while it clears the pack.
type gmClearMember struct {
	guid   packet.GUID // the unit's world guid once a read showed it (0: never seen)
	dead   bool        // the frames showed it dead
	tries  int         // kill lines sent
	gaveUp bool        // it survived gmClearTries lines: no more are sent
}

// placeGMClear is place step 6b. Every pack is cleared in gmClearRounds rounds; each round the commander visits every stop of the pack
// (a member's spawn point, so a pack wider than the commander's view is seen whole), reads the units around it, and kills every live
// member it sees. An empty read ends nothing: both rounds always run. Every expected member then gets one entry: killed (a death
// confirmed from the frames), already dead, alive (seen, would not die) or not seen. Anything but the first two leaves the clear
// PARTIAL, noted and in the summary; the walk goes on. The commander returns to the start point, on its floor, before the quiet check
// and the raid's move.
func (w *walk) placeGMClear(ctx context.Context) error {
	if len(w.gmClear) == 0 {
		return nil
	}
	w.gmClearUsed = true
	p := w.place.Point
	for _, pk := range w.gmClear {
		label := raidwalk.GMClearLabel(pk.Leg, pk.Group)
		members := map[uint64]*gmClearMember{}
		for _, g := range pk.SpawnGUIDs() {
			members[g] = &gmClearMember{}
		}
		stops := pk.Stops()
		for round := 0; round < gmClearRounds; round++ {
			for _, stop := range stops {
				if err := w.goPlace(ctx, stop.X, stop.Y, stop.Z+2); err != nil {
					return err
				}
				if err := w.placeSleep(ctx, 2*time.Second); err != nil {
					return err
				}
				if err := w.placeInstanceIs(ctx, w.placeInst); err != nil {
					return err
				}
				units, err := w.gmClearUnits(ctx)
				if err != nil {
					return stopf(exitRaidWalkFailed, "gm-clear: units read at %s: %v", label, err)
				}
				for _, u := range pk.SeenMembers(units) {
					m := members[raidwalk.SpawnGUID(u.GUID)]
					if m == nil || m.dead || m.gaveUp {
						continue
					}
					m.guid = u.GUID
					if u.Dead {
						m.dead = true
						continue
					}
					tries, dead, err := w.gmClearKill(ctx, u)
					if err != nil {
						return stopf(exitRaidWalkFailed, "gm-clear: interrupted at %s: %v", label, err)
					}
					m.tries += tries
					m.dead, m.gaveUp = dead, !dead
					if dead {
						w.logLine(w.env.now(), raidwalk.GMClearLogLine(raidwalk.GMClearEntry{Leg: pk.Leg, Group: pk.Group, Entry: u.GUID.Entry(), GUID: u.GUID.String()}))
					} else {
						w.logLine(w.env.now(), fmt.Sprintf("gm-clear: FAILED %d %s (%s) alive after %d tries", u.GUID.Entry(), u.GUID, label, m.tries))
					}
				}
			}
		}
		for _, g := range pk.SpawnGUIDs() {
			m := members[g]
			e := raidwalk.GMClearEntry{Leg: pk.Leg, Group: pk.Group, Entry: pk.Members[g], Tries: m.tries}
			switch {
			case m.guid == 0:
				e.GUID, e.Reason = fmt.Sprintf("spawn %d", g), raidwalk.GMClearNotSeen
			case m.dead:
				e.GUID, e.Killed = m.guid.String(), true
				if m.tries == 0 {
					e.Reason = raidwalk.GMClearAlreadyDead
				}
			default:
				e.GUID, e.Reason = m.guid.String(), raidwalk.GMClearAlive
			}
			if !e.Killed {
				w.note("gm-clear: member %d %s (%s) %s after %d .die lines", e.Entry, e.GUID, label, e.Reason, e.Tries)
			}
			w.gmClearLog = append(w.gmClearLog, e)
		}
	}
	w.note("%s", raidwalk.GMClearSummaryLine(w.gmClearLog, raidwalk.GMClearLegs(w.gmClear)))
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
