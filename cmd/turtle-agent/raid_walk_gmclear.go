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
	"math"
	"os"
	"path/filepath"
	"sort"
	"time"

	"github.com/pjerra/turtle-agent/internal/objects"
	"github.com/pjerra/turtle-agent/internal/packet"
	"github.com/pjerra/turtle-agent/internal/raidwalk"
	"github.com/pjerra/turtle-agent/internal/session"
	"github.com/pjerra/turtle-agent/internal/slots"
	"github.com/pjerra/turtle-agent/internal/worldspawn"
)

const (
	// gmClearReadYd is how far around a visit stop the commander reads units: the object store holds what the client would see. It is
	// wider than raidwalk.GMClearStopYd, the distance a stop covers.
	gmClearReadYd = 150
	// gmClearReadLimit is the units RPC's row cap for that read (the default 100 would cut a crowded room).
	gmClearReadLimit = 2000
	// gmClearRounds is how many times a pack's stops are visited and read: a first empty read ends nothing, so a pack
	// with a member still alive or unseen gets the second round. A pack whose every member is dead (or has had its
	// gmClearTries lines) after the first round is not walked again: the second round would read the same dead pack at
	// every stop and kill nothing (walk 71: 52 packs, 57 stops, two rounds each, 114 .go xyz and ~230 s of the 322 s
	// pre-clear; the kills it adds are only ever a member the first round did not see).
	gmClearRounds = 2
	// gmClearDeathWait and gmClearDeathPoll are how long a kill is given to show in the unit's frames, and how often it is read.
	gmClearDeathWait = 5 * time.Second
	gmClearDeathPoll = 500 * time.Millisecond
	// gmClearTries is how many kill lines one unit gets: the first and three retries (walk 32: the two tries were over
	// before a refused gm_kill or a slow death could be told apart), gmClearRetryGap apart.
	gmClearTries = 4
	// gmClearRetryGap is the pause between a kill line that did not show a death and the next one: a unit that is
	// still falling, or a server still answering the selection, gets the time.
	gmClearRetryGap = 3 * time.Second
	// gmClearLandPoll and gmClearLandWait are a stop's landing wait: the commander's status is read this often after
	// its `.go xyz`, up to this ceiling. Walk 74 paid a fixed 2 s at each of its 53 stops (~106 s of the 157 s clear).
	// The landing needs a teleport the session has ACKED (TeleportSeq past the read before the go, reviveLanded's
	// signal): the status reports the go's destination at once and a stop the commander already stands on is within
	// the slack before any teleport lands, so the position alone would pass on the previous stop's status (Codex
	// HIGH). A stop the status never settles within this ceiling falls back to the old fixed gmClearFallbackWait, so
	// the units read after it is never earlier than before.
	gmClearLandPoll     = 250 * time.Millisecond
	gmClearLandWait     = 3 * time.Second
	gmClearFallbackWait = 2 * time.Second
	// gmClearSettle is the least time a landed stop settles before the units are read, and gmClearUnitPoll and
	// gmClearUnitWait bound the poll of the pack in that read: the landing arrives before the client's object store
	// holds the pack, so the read right after the landing can still be empty.
	gmClearSettle   = 1 * time.Second
	gmClearUnitPoll = 250 * time.Millisecond
	gmClearUnitWait = 2 * time.Second
)

// gmClearBudget bounds the whole --gm-clear: past it no more lines go out, the members left are listed as not tried and the walk goes
// on. A member that cannot die costs gmClearTries * (gmClearDeathWait + gmClearRetryGap) = 32 s, so an unbounded clear of a big hall
// runs for half an hour (walk 35: Jin'do groups 5-13, 52 members, 15-25 min per placement). The despawned-member skip handles the known
// cause; this is the ceiling for an unknown one. A var so a test can shorten it.
var gmClearBudget = 5 * time.Minute

// installedWorldDir is the per-user data dir an installed binary reads the world export from (export.sh's out dir on
// a machine without a checkout): ~/.local/share/turtle-agent/world/map<N>.
func installedWorldDir(mapID uint32) string {
	home, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	return filepath.Join(home, ".local", "share", "turtle-agent", "world", fmt.Sprintf("map%d", mapID))
}

// repoWorldDir is the world export of a checkout beside the plan folder: <dir>/../tools/dungeongen/testdata/map<N>.
func (w *walk) repoWorldDir() string {
	return filepath.Join(slots.ExpandTilde(w.opts.dir), "..", "tools", "dungeongen", "testdata", fmt.Sprintf("map%d", w.plan.Map))
}

// worldExportDir is the world export the placement and --gm-clear read: --gm-clear-spawns, else the installed data
// dir when it holds creatures.tsv, else the checkout's tools/dungeongen/testdata/map<N>. With neither, the installed
// dir is named: it is the path an installed binary can have (the checkout path only exists beside a source tree).
func (w *walk) worldExportDir() string {
	if dir := w.opts.gmClearSpawns; dir != "" {
		return slots.ExpandTilde(dir)
	}
	installed := installedWorldDir(w.plan.Map)
	if installed != "" {
		if _, err := os.Stat(filepath.Join(installed, "creatures.tsv")); err == nil {
			return installed
		}
	}
	repo := w.repoWorldDir()
	if _, err := os.Stat(filepath.Join(repo, "creatures.tsv")); err == nil {
		return repo
	}
	if installed != "" {
		return installed
	}
	return repo
}

// worldExportWhere names every place the walk looks, for a note or a refusal.
func (w *walk) worldExportWhere() string {
	return fmt.Sprintf("%s (installed) or %s (checkout)", installedWorldDir(w.plan.Map), w.repoWorldDir())
}

// loadPlaceWorld reads the world export the offline quiet start judges (once per walk). A missing export is a note,
// never a refusal: the placement still works, it just cannot walk the start back.
func (w *walk) loadPlaceWorld() *worldspawn.World {
	if w.placeWorld != nil {
		return w.placeWorld
	}
	dir := w.worldExportDir()
	if _, err := os.Stat(filepath.Join(dir, "creatures.tsv")); err != nil {
		w.note("place: no world export in %s: the offline quiet start is skipped (pass --gm-clear-spawns DIR)", w.worldExportWhere())
		return nil
	}
	world, _, err := worldspawn.Load(dir, w.plan.Map)
	if err != nil {
		w.note("place: the world export in %s: %v; the offline quiet start is skipped", dir, err)
		return nil
	}
	w.placeWorld = world
	return world
}

// validateGMClear expands the specs into packs from the plan's rows and the world export, before any GM command.
func (w *walk) validateGMClear() error {
	specs, err := parseGMClearSpecs(w.opts.gmClear, w.opts.gmClearBoss)
	if err != nil {
		return stopf(exitUsage, "%v", err)
	}
	dir := w.worldExportDir()
	if _, err := os.Stat(filepath.Join(dir, "creatures.tsv")); err != nil {
		return stopf(exitUsage, "--gm-clear: no world export in %s: pass --gm-clear-spawns DIR (tools/dungeongen/export.sh writes it)", w.worldExportWhere())
	}
	world, _, err := worldspawn.Load(dir, w.plan.Map)
	if err != nil {
		return stopf(exitUsage, "--gm-clear: %v", err)
	}
	w.placeWorld = world
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

// gmClearKill sends the kill line for one unit (up to gmClearTries times, gmClearRetryGap apart) and reads its death from the frames
// after each. It returns the number of lines sent and whether the death was seen. No line goes out at or after deadline: the tool's
// budget is checked before every line, so a unit that cannot die stops the tool on its current try.
func (w *walk) gmClearKill(ctx context.Context, u objects.Unit, deadline time.Time) (tries int, dead bool, err error) {
	for tries < gmClearTries {
		if !w.env.now().Before(deadline) {
			return tries, false, nil
		}
		tries++
		w.logLine(w.env.now(), fmt.Sprintf("> .die (gm_kill, selected %s)", u.GUID))
		if err := w.agent.Call(ctx, "gm_kill", map[string]string{"guid": u.GUID.String()}, nil, 15*time.Second); err != nil {
			w.note("gm-clear: gm_kill %s: %v", u.GUID, err)
			if ctx.Err() != nil {
				return tries, false, ctx.Err()
			}
		} else {
			for waited := time.Duration(0); waited < gmClearDeathWait; waited += gmClearDeathPoll {
				if err := w.env.sleep(ctx, gmClearDeathPoll); err != nil {
					return tries, false, err
				}
				if w.gmClearDead(ctx, u.GUID) {
					return tries, true, nil
				}
			}
		}
		if tries < gmClearTries {
			if err := w.placeSleep(ctx, gmClearRetryGap); err != nil {
				return tries, false, err
			}
		}
	}
	return tries, false, nil
}

// appendSkipEntries adds entries to a skip list, keeping its order and dropping duplicates.
func appendSkipEntries(skips, entries []uint32) []uint32 {
	seen := make(map[uint32]bool, len(skips)+len(entries))
	for _, e := range skips {
		seen[e] = true
	}
	for _, e := range entries {
		if seen[e] {
			continue
		}
		seen[e] = true
		skips = append(skips, e)
	}
	return skips
}

// gmClearMember is what the walk knows about one expected pack member while it clears the pack.
type gmClearMember struct {
	guid      packet.GUID // the unit's world guid once a read showed it (0: never seen)
	dead      bool        // the frames showed it dead, or this walk's own clear despawned it
	tries     int         // kill lines sent
	gaveUp    bool        // it survived gmClearTries lines: no more are sent
	despawned bool        // the corridor clear or the quiet's extra round removed it before the tool ran
}

// gmClearAllSettled says every expected member of a pack has nothing left for this clear: its death is confirmed, or its
// gmClearTries lines are used up (a member that would not die gets no more). A member never seen, or seen alive and still
// to be tried, keeps the pack unsettled, so its next round still runs.
func gmClearAllSettled(members map[uint64]*gmClearMember, guids []uint64) bool {
	for _, g := range guids {
		if m := members[g]; m != nil && !m.dead && !m.gaveUp {
			return false
		}
	}
	return true
}

// gmClearLand sends a GM-clear stop's `.go xyz` and waits for the commander to have LANDED there: the plan's map, not
// falling, the position within the revive-landing slack of the stop (a landing may drop the character to the floor, so
// z gets reviveLandingZYd of room), and a teleport the session counted since the read before the go (gmClearLanded).
// A position match alone is no landing: the status reports the go's destination at once, and the previous stop's
// status can already be within the slack, so the first read would carry on while the server is still processing the
// teleport (Codex HIGH). A landed stop settles gmClearSettle before its units are read (gmClearStopUnits after the
// instance check): the landing arrives before the client's object store holds the pack. The wait is an absolute
// deadline (gmClearLandWait from here, on the walk's clock), not a sum of sleeps: each status RPC gets a context
// capped at the time left and the deadline is checked after every read, so a slow RPC cannot stretch the ceiling
// (Codex round 2). A landing the status never shows by the deadline falls back to the old fixed gmClearFallbackWait:
// the units read after it is then exactly as late as before.
func (w *walk) gmClearLand(ctx context.Context, x, y, z float32) error {
	beforeSeq, haveSeq, err := w.gmClearSeq(ctx)
	if err != nil {
		return err
	}
	if err := w.goPlace(ctx, x, y, z); err != nil {
		return err
	}
	landed := false
	due := w.env.now().Add(gmClearLandWait)
	for w.env.now().Before(due) {
		readCtx, cancel := context.WithTimeout(ctx, due.Sub(w.env.now()))
		st, serr := w.status(readCtx)
		cancel()
		if serr == nil && gmClearLanded(st, w.plan.Map, x, y, z, beforeSeq, haveSeq) {
			landed = true
			break
		}
		if !w.env.now().Before(due) {
			break
		}
		wait := due.Sub(w.env.now())
		if wait > gmClearLandPoll {
			wait = gmClearLandPoll
		}
		if err := w.placeSleep(ctx, wait); err != nil {
			return err
		}
	}
	settle := gmClearSettle
	if !landed {
		settle = gmClearFallbackWait
	}
	return w.placeSleep(ctx, settle)
}

// gmClearSeq reads the session's acked-teleport count before a stop's `.go xyz`: the seq a landing must pass. One retry
// a poll later; have is false when neither read answered, and no landing passes then (the wait falls back).
func (w *walk) gmClearSeq(ctx context.Context) (seq uint64, have bool, err error) {
	if st, serr := w.status(ctx); serr == nil {
		return st.TeleportSeq, true, nil
	}
	if err := w.placeSleep(ctx, gmClearLandPoll); err != nil {
		return 0, false, err
	}
	if st, serr := w.status(ctx); serr == nil {
		return st.TeleportSeq, true, nil
	}
	return 0, false, nil
}

// gmClearLanded says the commander's status is a fresh landing at a GM-clear stop: on the plan's map, not falling, a
// teleport the session counted since before the `.go` (TeleportSeq, the landing signal reviveLanded reads), and the
// position within the revive-landing slack of the stop. The seq is required: it is the only term that ties the status
// to THIS go (the session reports the go's destination itself at once, flagged ZUnknown or not). Over a navmesh hole
// the z guess never settles, and the seq is the landing there, exactly as for a near revive teleport.
func gmClearLanded(st session.StatusInfo, mapID uint32, x, y, z float32, beforeSeq uint64, haveSeq bool) bool {
	if st.Map != mapID || st.Falling || !haveSeq || st.TeleportSeq <= beforeSeq {
		return false
	}
	dx, dy := float64(x-st.X), float64(y-st.Y)
	return math.Hypot(dx, dy) <= reviveLandingYd && math.Abs(float64(z-st.Z)) <= reviveLandingZYd
}

// gmClearStopUnits reads the units at a landed stop until at least one of the pack's spawn guids shows, up to
// gmClearUnitWait, and returns the last read: the landing arrives before the client's object store holds the pack, so
// the read right after it can still be empty and the members would be recorded not seen. The caller has re-read its
// instance first, so this poll never runs on a stop that landed in another copy. The ceiling is an absolute deadline
// on the walk's clock, and each units RPC gets a context capped at the time left (Codex round 2): a read that the cap
// cuts ends the poll with the last units instead of stretching it, while a read that fails on its own still stops the
// walk.
func (w *walk) gmClearStopUnits(ctx context.Context, label string, guids []uint64) ([]objects.Unit, error) {
	due := w.env.now().Add(gmClearUnitWait)
	var last []objects.Unit
	for {
		readCtx := ctx
		cancel := func() {}
		timed := false
		if left := due.Sub(w.env.now()); left > 0 {
			readCtx, cancel = context.WithTimeout(ctx, left)
			timed = true
		}
		units, err := w.gmClearUnits(readCtx)
		capped := timed && ctx.Err() == nil && readCtx.Err() != nil
		cancel()
		if err != nil {
			if capped {
				return last, nil
			}
			return nil, stopf(exitRaidWalkFailed, "gm-clear: units read at %s: %v", label, err)
		}
		last = units
		if gmClearUnitsShown(units, guids) || !w.env.now().Before(due) {
			return units, nil
		}
		wait := due.Sub(w.env.now())
		if wait > gmClearUnitPoll {
			wait = gmClearUnitPoll
		}
		if err := w.placeSleep(ctx, wait); err != nil {
			return nil, err
		}
	}
}

// gmClearUnitsShown says the units read holds at least one of the pack's spawn guids: the client's object store has the
// pack the stop was walked to, so the read can name its members.
func gmClearUnitsShown(units []objects.Unit, guids []uint64) bool {
	want := make(map[uint64]bool, len(guids))
	for _, g := range guids {
		want[g] = true
	}
	for _, u := range units {
		if want[raidwalk.SpawnGUID(u.GUID)] {
			return true
		}
	}
	return false
}

// placeGMClear is place step 6b. Every pack is cleared in gmClearRounds rounds; each round the commander visits the pack's stops
// (a member's spawn point, so a pack wider than the commander's view is seen whole), reads the units around it, and kills every live
// member it sees. A first empty read ends nothing: the second round still runs while a member is alive or unseen, and is skipped once
// every member is dead (nothing there to kill: walk 71 paid 230 s of the 322 s pre-clear for those empty second rounds). After every
// `.go xyz` the commander re-reads its instance before the units read or any kill: the same map does not prove the same copy, and a
// stop that landed elsewhere stops the clear on the existing error path with no kill sent. The end of the clear reads it again. A member
// the walk's own corridor clear already despawned is never sent a line: the server no longer has it (walk 35), so it is recorded as
// despawned at once. Every expected member then gets one entry: killed (a death confirmed from the frames), despawned, already dead,
// alive (seen, would not die) or not seen. Anything but the first three leaves the clear PARTIAL, noted and in the summary; the walk
// goes on. gmClearBudget bounds the whole tool: past it no more lines go out. The commander returns to the start point, on its floor,
// before the quiet check and the raid's move.
func (w *walk) placeGMClear(ctx context.Context) error {
	if len(w.gmClear) == 0 {
		return nil
	}
	w.gmClearUsed = true
	p := w.place.Point
	deadline := w.env.now().Add(gmClearBudget)
	budgetHit := false
	for _, pk := range w.gmClear {
		label := raidwalk.GMClearLabel(pk.Leg, pk.Group)
		guids := pk.SpawnGUIDs()
		members := map[uint64]*gmClearMember{}
		for _, g := range guids {
			m := &gmClearMember{}
			if w.placeCleared[g] {
				// The corridor clear or the quiet's extra round removed it in this instance: `.die` on its guid does nothing
				// (the client's store still lists it), so four tries would burn 32 s and report it alive.
				m.dead, m.despawned = true, true
			}
			members[g] = m
		}
		stops := pk.Stops()
		for range gmClearRounds {
			if gmClearAllSettled(members, guids) {
				// Nothing is left to read for: a pack the corridor sweep already despawned whole is marked settled
				// before its first visit, so round 1 skips it exactly as the later rounds do (walk 74: 28 of 53
				// stops went out only to print `nothing to clear`, 28 empty stops x ~2.1 s = 59 s).
				break
			}
			for _, stop := range stops {
				if err := w.gmClearLand(ctx, stop.X, stop.Y, stop.Z+2); err != nil {
					return err
				}
				if err := w.placeInstanceIs(ctx, w.placeInst); err != nil {
					return err
				}
				units, err := w.gmClearStopUnits(ctx, label, guids)
				if err != nil {
					return err
				}
				for _, u := range pk.SeenMembers(units) {
					m := members[raidwalk.SpawnGUID(u.GUID)]
					if m == nil || m.dead || m.gaveUp {
						continue
					}
					if !w.env.now().Before(deadline) {
						budgetHit = true
						continue
					}
					m.guid = u.GUID
					if u.Dead {
						m.dead = true
						continue
					}
					tries, dead, err := w.gmClearKill(ctx, u, deadline)
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
				if gmClearAllSettled(members, guids) {
					break // nothing alive left to read this stop for; the next stop has no kill for it either
				}
			}
		}
		left, gone, dead := 0, 0, 0
		for _, g := range pk.SpawnGUIDs() {
			m := members[g]
			e := raidwalk.GMClearEntry{Leg: pk.Leg, Group: pk.Group, Entry: pk.Members[g], Tries: m.tries}
			untried := m.tries == 0 && !w.env.now().Before(deadline)
			switch {
			case m.guid == 0:
				e.GUID = fmt.Sprintf("spawn %d", g)
				switch {
				case m.despawned:
					e.Killed, e.Reason = true, raidwalk.GMClearDespawned
				case untried:
					e.Reason = raidwalk.GMClearGaveUp
				default:
					e.Reason = raidwalk.GMClearNotSeen
				}
			case m.dead:
				e.GUID, e.Killed = m.guid.String(), true
				if m.tries == 0 {
					if m.despawned {
						e.Reason = raidwalk.GMClearDespawned
					} else {
						e.Reason = raidwalk.GMClearAlreadyDead
					}
				}
			case untried:
				e.GUID, e.Reason = m.guid.String(), raidwalk.GMClearGaveUp
			default:
				e.GUID, e.Reason = m.guid.String(), raidwalk.GMClearAlive
			}
			switch {
			case e.Reason == raidwalk.GMClearDespawned:
				gone++
			case !e.Killed:
				left++
				w.note("gm-clear: member %d %s (%s) %s after %d .die lines", e.Entry, e.GUID, label, e.Reason, e.Tries)
			case e.Tries > 0:
				dead++
			}
			w.gmClearLog = append(w.gmClearLog, e)
		}
		why := "nothing to clear"
		if dead > 0 || left > 0 {
			why = fmt.Sprintf("%d killed by .die, %d already gone, %d left", dead, gone, left)
		}
		w.logLine(w.env.now(), fmt.Sprintf("gm-clear: %s: %s", label, why))
		if gone > 0 {
			// The frames still show them alive in the client's store, so this is not a partial clear. The per-group
			// line above already says they were gone; the note keeps the count, because --gm-clear's count then
			// reads lower than the pack size.
			w.note("gm-clear: %s: %d of %d members were already despawned by this walk's own clear (no line sent)", label, gone, len(pk.SpawnGUIDs()))
		}
		if left > 0 {
			// The group stays alive (walk 32: every gm_kill was refused for the slots table's missing world_addr, so
			// the units never died): the placement must NOT fail for it. WARN, and let the quiet and the landing read
			// ignore the pack's entries, so an alive member cannot make the area "not quiet" and burn an instance.
			entries := make([]uint32, 0, len(pk.Members))
			seen := map[uint32]bool{}
			for _, e := range pk.Members {
				if !seen[e] {
					seen[e] = true
					entries = append(entries, e)
				}
			}
			sort.Slice(entries, func(i, j int) bool { return entries[i] < entries[j] })
			w.place.Skips = appendSkipEntries(w.place.Skips, entries)
			warn := fmt.Sprintf("gm-clear: WARN: %s not cleared (%d members left): the placement goes on without it; the quiet and the landing read ignore entries %s", label, left, raidwalk.SkipWords(entries))
			w.note("%s", warn)
			w.logLine(w.env.now(), warn)
		}
	}
	if budgetHit {
		untried := 0
		for _, e := range w.gmClearLog {
			if e.Reason == raidwalk.GMClearGaveUp {
				untried++
			}
		}
		warn := fmt.Sprintf("gm-clear: WARN: gave up after %s (the tool's budget): %d expected members were not tried and count as not cleared; the placement goes on (raise the budget only to debug a kill that does not land)", gmClearBudget, untried)
		w.note("%s", warn)
		w.logLine(w.env.now(), warn)
	}
	w.note("%s", raidwalk.GMClearSummaryLine(w.gmClearLog, raidwalk.GMClearLegs(w.gmClear)))
	// Back to the start point, on its floor, as the corridor clear's last position was.
	if err := w.gmClearLand(ctx, p.X, p.Y, p.Z+2); err != nil {
		return err
	}
	if err := w.placeInstanceIs(ctx, w.placeInst); err != nil {
		return err
	}
	return w.placeFloor(ctx, p)
}
