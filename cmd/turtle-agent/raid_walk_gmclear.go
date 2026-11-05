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
	"sort"
	"time"

	"github.com/pjerra/turtle-agent/internal/dungeon"
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
	// gmClearRounds is how many sweeps are made over the members still unseen: a member seen (alive or dead) in round one is not
	// visited again, so a sweep where every member showed costs one round, not two.
	gmClearRounds = 2
	// gmClearSettlePoll, gmClearSettleMax and gmClearSettleRetry replace the fixed 2 s the commander used to wait after every `.go`:
	// the units are read every gmClearSettlePoll and the read is used the moment every unseen member near the stop shows, or after
	// gmClearSettleMax (gmClearSettleRetry in round two) when one does not (a corpse that already despawned never shows).
	gmClearSettlePoll  = 250 * time.Millisecond
	gmClearSettleMax   = 2 * time.Second
	gmClearSettleRetry = time.Second
	// gmClearDeathWait and gmClearDeathPoll are how long the kills of one batch are given to show in the units' frames, and how
	// often they are read (one `units` read for the whole batch, not one `unit` read per kill).
	gmClearDeathWait = 5 * time.Second
	gmClearDeathPoll = 250 * time.Millisecond
	// gmClearTries is how many kill lines one unit gets: the first and three retries (walk 32: the two tries were over
	// before a refused gm_kill or a slow death could be told apart), gmClearRetryGap apart.
	gmClearTries = 4
	// gmClearRetryGap is the pause between a kill line that did not show a death and the next one: a unit that is
	// still falling, or a server still answering the selection, gets the time.
	gmClearRetryGap = 3 * time.Second
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

// gmClearUnitDead says whether a unit's frames show it dead.
func gmClearUnitDead(u objects.Unit) bool {
	return u.Dead || (u.MaxHealth > 0 && u.Health == 0)
}

// gmClearDead says whether the unit's frames show it dead (the `unit` RPC: for a unit the batched `units` read did not list). A unit the
// client no longer holds is not confirmed dead.
func (w *walk) gmClearDead(ctx context.Context, g packet.GUID) bool {
	var u objects.Unit
	if err := w.agent.Call(ctx, "unit", map[string]string{"guid": g.String()}, &u, 10*time.Second); err != nil {
		return false
	}
	return gmClearUnitDead(u)
}

// gmClearKill is gmClearKillBatch for one unit (the retreat's blockers): the lines sent and whether the death was seen.
func (w *walk) gmClearKill(ctx context.Context, u objects.Unit, deadline time.Time) (tries int, dead bool, err error) {
	ts, ds, err := w.gmClearKillBatch(ctx, []objects.Unit{u}, deadline)
	return ts[0], ds[0], err
}

// gmClearKillBatch sends the kill line to every unit (one gm_kill each: it must select the unit, so those stay one after the other) and
// then reads the deaths of the whole batch together: one `units` read per gmClearDeathPoll, a unit that read does not list asked for by
// guid, instead of a sleep and a read after every single kill. A unit still alive after gmClearDeathWait gets the next line with the
// others still alive, gmClearRetryGap after the last (gmClearTries lines at most). The result is the lines sent and the confirmed death
// per unit. No line goes out at or after deadline: the budget is checked before every line.
func (w *walk) gmClearKillBatch(ctx context.Context, units []objects.Unit, deadline time.Time) (tries []int, dead []bool, err error) {
	tries, dead = make([]int, len(units)), make([]bool, len(units))
	for attempt := 0; attempt < gmClearTries; attempt++ {
		var sent []int
		cut := false
		for i, u := range units {
			if dead[i] {
				continue
			}
			if !w.env.now().Before(deadline) {
				cut = true
				break
			}
			tries[i]++
			w.logLine(w.env.now(), fmt.Sprintf("> .die (gm_kill, selected %s)", u.GUID))
			if err := w.agent.Call(ctx, "gm_kill", map[string]string{"guid": u.GUID.String()}, nil, 15*time.Second); err != nil {
				w.note("gm-clear: gm_kill %s: %v", u.GUID, err)
				if ctx.Err() != nil {
					return tries, dead, ctx.Err()
				}
				continue
			}
			sent = append(sent, i)
		}
		for waited := time.Duration(0); len(sent) > 0 && waited < gmClearDeathWait; waited += gmClearDeathPoll {
			if err := w.env.sleep(ctx, gmClearDeathPoll); err != nil {
				return tries, dead, err
			}
			listed := map[packet.GUID]bool{}
			dying := map[packet.GUID]bool{}
			if us, err := w.gmClearUnits(ctx); err == nil {
				for _, u := range us {
					listed[u.GUID] = true
					if gmClearUnitDead(u) {
						dying[u.GUID] = true
					}
				}
			}
			left := sent[:0:0]
			for _, i := range sent {
				g := units[i].GUID
				if dying[g] || (!listed[g] && w.gmClearDead(ctx, g)) {
					dead[i] = true
				} else {
					left = append(left, i)
				}
			}
			sent = left
		}
		alive := false
		for i := range units {
			alive = alive || !dead[i]
		}
		if !alive || cut || attempt+1 == gmClearTries {
			break
		}
		if err := w.placeSleep(ctx, gmClearRetryGap); err != nil {
			return tries, dead, err
		}
	}
	return tries, dead, nil
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

// gmClearPackState is one pack and what the walk knows about each of its expected members while it clears.
type gmClearPackState struct {
	pk      raidwalk.GMClearPack
	label   string
	guids   []uint64 // the members' spawn guids, sorted
	members map[uint64]*gmClearMember
}

// gmClearCovers says whether a stop at point `at` covers a member standing at pt (raidwalk.GMClearStopYd, 2D).
func gmClearCovers(at, pt dungeon.Point) bool {
	dx, dy := float64(pt.X-at.X), float64(pt.Y-at.Y)
	return dx*dx+dy*dy <= raidwalk.GMClearStopYd*raidwalk.GMClearStopYd
}

// gmClearWanted says the sweep still has to see this member: not dead, not given up on, never in a read.
func (m *gmClearMember) wanted() bool { return !m.dead && !m.gaveUp && m.guid == 0 }

// gmClearSettle reads the units around the commander, again every gmClearSettlePoll, until every member the stop covers that no read
// has shown yet is in the read, or max has passed: the object update after a `.go` takes a moment, and a member that is gone (a corpse
// that already despawned) never shows. It replaces the fixed 2 s the sweep used to wait after every teleport.
func (w *walk) gmClearSettle(ctx context.Context, states []*gmClearPackState, visited map[*gmClearMember]bool, at dungeon.Point, max time.Duration) ([]objects.Unit, error) {
	for waited := time.Duration(0); ; {
		if err := w.placeSleep(ctx, gmClearSettlePoll); err != nil {
			return nil, err
		}
		waited += gmClearSettlePoll
		units, err := w.gmClearUnits(ctx)
		if err != nil {
			return nil, stopf(exitRaidWalkFailed, "gm-clear: units read at %.0f %.0f: %v", at.X, at.Y, err)
		}
		missing := false
		for _, st := range states {
			seen := map[uint64]bool{}
			for _, u := range st.pk.SeenMembers(units) {
				seen[raidwalk.SpawnGUID(u.GUID)] = true
			}
			for _, g := range st.guids {
				m := st.members[g]
				if m.wanted() && !visited[m] && gmClearCovers(at, st.pk.Positions[g]) && !seen[g] {
					missing = true
				}
			}
		}
		if !missing || waited >= max {
			return units, nil
		}
	}
}

// placeGMClear is place step 6b. The members of every named pack are cleared in one sweep over the whole set (not pack by pack): the
// commander goes to the first member nobody has seen yet, reads the units its client holds around it, and kills EVERY live member of
// ANY named pack that read shows (a read holds 150 yd, so one stop serves the packs of a whole room); the members that stop covers are
// then done for the round. A member seen dead is never sent a line (a re-place in the same instance finds most of them dead), a
// member seen alive is killed in one batch with the others of that read (gmClearKillBatch: one death read for the batch, not a sleep
// per kill), and a member that is seen once is never visited again. Round two (gmClearRounds) revisits only members no read has shown
// yet, so a first empty read ends nothing. A member the walk's own corridor clear already despawned is never sent a line: the server
// no longer has it (walk 35), so it is recorded as despawned at once. Every expected member then gets one entry: killed (a death
// confirmed from the frames), despawned, already dead, alive (seen, would not die) or not seen. Anything but the first three leaves
// the clear PARTIAL, noted and in the summary; the walk goes on. A unit that belongs to no named pack is never touched. gmClearBudget
// bounds the whole tool: past it no more lines go out. The commander returns to the start point, on its floor, before the quiet
// check and the raid's move.
func (w *walk) placeGMClear(ctx context.Context) error {
	if len(w.gmClear) == 0 {
		return nil
	}
	w.gmClearUsed = true
	p := w.place.Point
	deadline := w.env.now().Add(gmClearBudget)
	var states []*gmClearPackState
	for _, pk := range w.gmClear {
		st := &gmClearPackState{pk: pk, label: raidwalk.GMClearLabel(pk.Leg, pk.Group), guids: pk.SpawnGUIDs(), members: map[uint64]*gmClearMember{}}
		for _, g := range st.guids {
			m := &gmClearMember{}
			if w.placeCleared[g] {
				// The corridor clear or the quiet's extra round removed it in this instance: `.die` on its guid does nothing
				// (the client's store still lists it), so four tries would burn 32 s and report it alive.
				m.dead, m.despawned = true, true
			}
			st.members[g] = m
		}
		states = append(states, st)
	}
	budgetHit, err := w.gmClearSweep(ctx, states, deadline)
	if err != nil {
		return err
	}
	for _, st := range states {
		pk, label, members := st.pk, st.label, st.members
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

// gmClearSweep is the visit-read-kill loop of placeGMClear (see there). It returns whether the budget cut it short.
func (w *walk) gmClearSweep(ctx context.Context, states []*gmClearPackState, deadline time.Time) (budgetHit bool, err error) {
	for round := 0; round < gmClearRounds; round++ {
		max := gmClearSettleMax
		if round > 0 {
			max = gmClearSettleRetry
		}
		visited := map[*gmClearMember]bool{}
		for {
			// The next stop: the first member (pack order, then spawn guid) that no read has shown and no stop of this round covered.
			var at dungeon.Point
			found := false
			for _, st := range states {
				for _, g := range st.guids {
					if m := st.members[g]; m.wanted() && !visited[m] {
						at, found = st.pk.Positions[g], true
						break
					}
				}
				if found {
					break
				}
			}
			if !found {
				break
			}
			if !w.env.now().Before(deadline) {
				return true, nil
			}
			if err := w.goPlace(ctx, at.X, at.Y, at.Z+2); err != nil {
				return false, err
			}
			units, err := w.gmClearSettle(ctx, states, visited, at, max)
			if err != nil {
				return false, err
			}
			if err := w.placeInstanceIs(ctx, w.placeInst); err != nil {
				return false, err
			}
			// Every live member of any named pack in the read goes into one batch; a corpse is only recorded.
			type owner struct {
				st *gmClearPackState
				m  *gmClearMember
			}
			var todo []objects.Unit
			owners := map[packet.GUID][]owner{}
			for _, st := range states {
				for _, u := range st.pk.SeenMembers(units) {
					m := st.members[raidwalk.SpawnGUID(u.GUID)]
					if m == nil || m.dead || m.gaveUp {
						continue
					}
					m.guid = u.GUID
					if gmClearUnitDead(u) {
						m.dead = true
						continue
					}
					if len(owners[u.GUID]) == 0 {
						todo = append(todo, u)
					}
					owners[u.GUID] = append(owners[u.GUID], owner{st, m})
				}
			}
			for _, st := range states {
				for _, g := range st.guids {
					if m := st.members[g]; m.wanted() && gmClearCovers(at, st.pk.Positions[g]) {
						visited[m] = true
					}
				}
			}
			if len(todo) == 0 {
				continue
			}
			tries, dead, err := w.gmClearKillBatch(ctx, todo, deadline)
			if err != nil {
				return false, stopf(exitRaidWalkFailed, "gm-clear: interrupted at %.0f %.0f: %v", at.X, at.Y, err)
			}
			for i, u := range todo {
				if tries[i] == 0 {
					budgetHit = true // the budget ran out before this unit's first line
					continue
				}
				for _, o := range owners[u.GUID] {
					o.m.tries += tries[i]
					o.m.dead, o.m.gaveUp = dead[i], !dead[i]
					if dead[i] {
						w.logLine(w.env.now(), raidwalk.GMClearLogLine(raidwalk.GMClearEntry{Leg: o.st.pk.Leg, Group: o.st.pk.Group, Entry: u.GUID.Entry(), GUID: u.GUID.String()}))
					} else {
						w.logLine(w.env.now(), fmt.Sprintf("gm-clear: FAILED %d %s (%s) alive after %d tries", u.GUID.Entry(), u.GUID, o.st.label, o.m.tries))
					}
				}
			}
			if budgetHit {
				return true, nil
			}
		}
	}
	return false, nil
}
