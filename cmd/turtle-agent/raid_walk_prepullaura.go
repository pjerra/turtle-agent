// raid_walk_prepullaura.go: the `--prepull-aura` test tool of raid-walk (test servers only, off by default). Before
// each boss row the commander puts the flag's aura lists on every living party member with the same GM
// select-then-".aura" call dungeon_run's buff presets use (internal/dungeon's AuraPort and ApplyAuras), so a walk
// tests the preparation a real raid carries into the instance: a flask, elixirs, food and the world buffs, all as
// auras the walk can read back.
//
//	--prepull-aura role=tank:17626,17538 --prepull-aura role=healer:17627,24363 --prepull-aura all:world,23768
//
// The fire is the boss row's own gate (prepullGate): the step before a boss row in WHATEVER phase, as long as the run
// is still before the pull (no pulling/fighting note, no later pull line of the page, no party bot in combat), or the
// boss row itself when the run passed that step too fast to pause, still before the pull (no member in combat and the
// boss's arena read fresh out of combat). Walk 101b: the ZG Thekal plan's step before boss row 255 is a plain move, so
// a rest/gather-only gate never fired and the walk stopped with exit 18. The fire is once per boss row per walk, under
// a run pause that is always resumed. The pause is fail-closed: a refused or unconfirmed pause sends no aura, and the
// commander and every living party member are re-read for combat right before the first `.aura` and before every
// later one, so a pull that starts mid-batch stops the batch and fails the walk. PartyInfo.Members leaves the
// commander out, so it is read through the walk's own status and holds a read of its own. A unit the unit RPC
// cannot read counts as combat too: the walk never assumes an unreadable party is out of combat. A boss row that
// becomes current without a verified fire fails the walk too: the preparation the run was started to test never
// happened. The auras a bot already carries are not sent again, so a second boss row only tops up what a death or a
// buff strip took. Every fire is read back into prepull-auras.json (bot, role, ids sent/seen/missing). By default
// every requested aura must verify; --prepull-aura-tolerance N is the explicit opt-in that accepts up to N missing
// ids per bot. A missing aura beyond that bound fails the walk (exit 18), because a preparation the walk cannot see
// is not the test the run was started for. A boss row is written to prepull-auras.json only after its auras verified
// within the tolerance; a fire that failed is a separate `attempts` entry with its reason, never a boss row.
package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strings"
	"time"

	"github.com/pjerra/turtle-agent/internal/dungeon"
	"github.com/pjerra/turtle-agent/internal/objects"
	"github.com/pjerra/turtle-agent/internal/planexport"
	"github.com/pjerra/turtle-agent/internal/raidwalk"
	"github.com/pjerra/turtle-agent/internal/tbm"
)

// The walk's prepull-aura timings. The gap and the settle are dungeon_run's own buff timings, so one boss row's ~20
// bots and ~10 auras cost about a minute of the run pause; the verify window covers the last auras' arrival.
var (
	prepullAuraGap        = 300 * time.Millisecond
	prepullAuraSettle     = 1500 * time.Millisecond
	prepullAuraViewWait   = 10 * time.Second
	prepullAuraVerifyWait = 5 * time.Second
	prepullAuraVerifyPoll = time.Second
)

// prepullAuraFileName is the fire's evidence in the walk's out dir.
const prepullAuraFileName = "prepull-auras.json"

// prepullAuraFile is prepull-auras.json: the flag set, the tolerance, the merged lists, the boss rows whose fire
// verified, the fires that failed and every bot's line.
type prepullAuraFile struct {
	Flag      []string             `json:"flag"`
	Tolerance int                  `json:"tolerance"`
	Lists     []prepullAuraList    `json:"lists"`
	BossRows  []int                `json:"boss_rows"`
	Attempts  []prepullAuraAttempt `json:"attempts"`
	Bots      []prepullAuraRecord  `json:"bots"`
}

// prepullAuraAttempt is one fire that did not verify: the boss row it prepared and why it stopped. A fire that
// succeeded is a boss row instead, so the two lists never overlap.
type prepullAuraAttempt struct {
	BossRow int    `json:"boss_row"`
	Reason  string `json:"reason"`
}

// prepullAuraList is one role's merged list, as the walk resolved it.
type prepullAuraList struct {
	Role string   `json:"role"`
	IDs  []uint32 `json:"ids"`
}

// prepullAuraRecord is one bot's line: its aura role, every id this walk sent it, and what the last read showed.
type prepullAuraRecord struct {
	Bot     string   `json:"bot"`
	Role    string   `json:"role"`
	Sent    []uint32 `json:"sent"`
	Seen    []uint32 `json:"seen"`
	Missing []uint32 `json:"missing"`
}

// prepullAuraTarget is one bot of a fire: its record, its guid, the list it must carry and what it lacked at the send.
type prepullAuraTarget struct {
	rec     *prepullAuraRecord
	unit    dungeon.AuraTarget
	want    []dungeon.Buff
	missing []dungeon.Buff
}

// validatePrepullAura parses the flag values into the walk's lists before any GM line (phase 1: a bad role, an
// unknown preset, spell id 0, a role named twice or an over-full list is an exit-2 refusal).
func (w *walk) validatePrepullAura() error {
	set, err := raidwalk.PlanPrepullAuras(w.opts.prepullAura)
	if err != nil {
		return stopf(exitUsage, "%v", err)
	}
	w.prepullAuras = set
	return nil
}

// prepullGate is the --prepull-aura firing gate. It returns the boss row to prepare and whether a fire may go out
// now (0: nothing to do, and no failure). The step before a boss row fires in whatever phase, as long as the status
// is still before the pull (prePullOK: no pulling/fighting note, no later pull line of the same page, no party bot
// in combat in a recent frame). A status that is not before the pull defers to the boss row's own status, exactly as
// the rest/gather-only gate did. The boss row itself fires only while the pause can still land before the pull: the
// same pre-pull check AND no engaged boss, read FRESH from the server (prepullBossEngaged) rather than from the
// cached hostile frames bossEngagedNow reads: those can still show the boss unengaged while the pull has started and
// the next view frame has not arrived. A boss row that becomes current otherwise is (step, false), the caller's
// blocking failure: the preparation the run was started to test never happened. A failed boss read is an error too
// (the gate never fires on a read it could not make); the caller stops the walk with it, and no aura goes out.
func (w *walk) prepullGate(ctx context.Context, st tbm.RunStatus, later bool) (int, bool, error) {
	if st.State != tbm.RunRunning && st.State != tbm.RunPaused {
		return 0, false, nil
	}
	if st.Kind == planexport.KindBoss {
		if _, ok := w.bossRowAt(st.Step); !ok {
			return 0, false, nil
		}
		if w.prepullAuraSent[st.Step] {
			// The row was prepared already: its own status is no failure and needs no read. The caller's sent check
			// returns before it treats fire=false as one.
			return st.Step, false, nil
		}
		if !w.prePullOK(st, later, w.env.now()) {
			return st.Step, false, nil
		}
		engaged, err := w.prepullBossEngaged(ctx, st.Step)
		if err != nil {
			return 0, false, err
		}
		return st.Step, !engaged, nil
	}
	next := st.Step + 1
	if _, ok := w.bossRowAt(next); !ok {
		return 0, false, nil
	}
	if !w.prePullOK(st, later, w.env.now()) {
		return 0, false, nil
	}
	return next, true, nil
}

// prepullAuraSendPending is pollOnce's firing: at the step before a boss row (whatever its phase) or at the boss row
// itself when the run passed that step too fast to pause, once per boss row, the auras go out under a run pause that
// must hold. A boss row that became current without a verified fire fails the walk (a configured boss row the walk
// never prepared is not the test the run was started for). A boss row whose arena read fails is the gate's error and
// stops the walk too. Whenever the status was running before the pause attempt, the resume is attempted afterwards
// even when the hold or the fire failed: a run left paused stops the raid silently. A status that was already paused
// sends no resume. A row is recorded as done only after the auras verified within the tolerance. It reports whether a
// firing happened, so pollOnce breaks its line loop (the next poll re-reads from the cursor), like the instance-data
// firing.
func (w *walk) prepullAuraSendPending(ctx context.Context, st tbm.RunStatus, later bool) (bool, error) {
	if len(w.prepullAuras) == 0 {
		return false, nil
	}
	step, fire, err := w.prepullGate(ctx, st, later)
	if err != nil {
		return false, err
	}
	if step == 0 {
		return false, nil
	}
	if w.prepullAuraSent[step] {
		return false, nil
	}
	if !fire {
		return false, stopf(exitRaidWalkFailed,
			"prepull-aura: boss row %d became current with no verified pre-pull application: the auras were never sent", step)
	}
	pauseAttempted := st.State == tbm.RunRunning
	fireErr := w.prepullAuraHold(ctx, step, pauseAttempted)
	if fireErr == nil {
		fireErr = w.prepullAuraFire(ctx, step)
		if fireErr == nil {
			if w.prepullAuraSent == nil {
				w.prepullAuraSent = map[int]bool{}
			}
			w.prepullAuraSent[step] = true
		}
	}
	if pauseAttempted {
		if _, err := w.commander(ctx, commanderReq{Op: "run", Verb: "resume", WaitMS: 10000}); err != nil {
			if fireErr != nil {
				w.note("prepull-aura: WARN: the run resume after boss row %d also failed (%v)", step, err)
				return true, fireErr
			}
			return true, stopf(exitRaidWalkFailed, "prepull-aura: boss row %d: the run resume after the auras failed: %v", step, err)
		}
	}
	return true, fireErr
}

// prepullAuraHold puts the run on hold for the batch and confirms it. A refused, unacknowledged or unconfirmed pause
// is a blocking failure: no aura goes out on a run the walk cannot show is paused before the pull. The pause must
// stand on the step before the boss row; the one exception is a run that passed that step too fast to pause: then it
// may stand on the boss row itself, and only while the boss is not engaged — the boss's arena is read fresh from the
// server after the pause is confirmed (prepullBossEngaged), never from the cached frames, and the fire's own combat
// read still gates the commander and every member. A pause past the boss row is mid-pull and fails. The confirmation
// is a fresh `run status`, the run's own answer rather than the push feed's newest line.
func (w *walk) prepullAuraHold(ctx context.Context, step int, pause bool) error {
	if pause {
		rep, err := w.commander(ctx, commanderReq{Op: "run", Verb: "pause", WaitMS: 10000})
		if err != nil {
			return stopf(exitRaidWalkFailed, "prepull-aura: boss row %d: the run pause failed, no auras were sent: %v", step, err)
		}
		if rep.Reply.Err != nil {
			return stopf(exitRaidWalkFailed, "prepull-aura: boss row %d: the run pause was refused (%s), no auras were sent", step, rep.Reply.Err.Message)
		}
		if !rep.Reply.OK && rep.Reply.Kind != tbm.KindAck {
			return stopf(exitRaidWalkFailed, "prepull-aura: boss row %d: the run pause was not acknowledged (%s), no auras were sent", step, rep.Reply.Line)
		}
		w.logLine(w.env.now(), fmt.Sprintf("> .bot run pause (prepull-aura, boss row %d)", step))
	}
	rep, err := w.commander(ctx, commanderReq{Op: "run", Verb: "status", WaitMS: 5000})
	if err != nil {
		return stopf(exitRaidWalkFailed, "prepull-aura: boss row %d: the paused run could not be confirmed (%v), no auras were sent", step, err)
	}
	if rep.Reply.Err != nil {
		return stopf(exitRaidWalkFailed, "prepull-aura: boss row %d: the run status was refused (%s), no auras were sent", step, rep.Reply.Err.Message)
	}
	run, line, ok := lastRunLine(rep.Reply.Lines)
	if !ok {
		return stopf(exitRaidWalkFailed, "prepull-aura: boss row %d: the run status answer has no TBM:RUN line, no auras were sent", step)
	}
	if run.State != tbm.RunPaused {
		return stopf(exitRaidWalkFailed, "prepull-aura: boss row %d: the run is not paused (%s), no auras were sent", step, line)
	}
	if w.plan != nil && run.Plan != "" && run.Plan != w.plan.Name {
		return stopf(exitRaidWalkFailed, "prepull-aura: boss row %d: the paused run is %s, not %s, no auras were sent", step, run.Plan, w.plan.Name)
	}
	if run.Step != step-1 && run.Step != step {
		return stopf(exitRaidWalkFailed, "prepull-aura: boss row %d: the run paused at step %d, past the step before it (step %d), no auras were sent",
			step, run.Step, step-1)
	}
	if run.Step == step {
		// The run passed the step before the boss too fast for the pause. A pause on the boss row is still before the
		// pull only while the boss is not engaged: the arena is read fresh here, after the pause was confirmed, because
		// the cached hostile frames can lag the server by a frame and show a boss the commander already pulled. The fire
		// re-reads the commander and every living member for combat before its first aura, so a pull that started anyway
		// still stops the batch there.
		engaged, err := w.prepullBossEngaged(ctx, step)
		if err != nil {
			return err
		}
		if engaged {
			return stopf(exitRaidWalkFailed, "prepull-aura: boss row %d: the run paused on the boss row with the boss engaged, no auras were sent", step)
		}
		w.note("prepull-aura: boss row %d: the run reached the boss row before the pause; it paused there with the boss not engaged", step)
	}
	return nil
}

// prepullBossEngaged reads the boss row's arena FRESH from the server and reports whether any unit there is in
// combat: `q units 60 <anchor> all`, the same read the reuse-instance guard (`placeBossEngaged`) makes, with `cb`
// the combat state. The walk's cached hostile frames (`bossEngagedNow`) are not used here: the push feed's statuses
// are handled before the next view frame is fetched, so a cached frame can still show the boss unengaged after the
// commander has pulled. It fails closed: a boss row without a point, a refused or unanswered read and a truncated
// reply are all errors, never a clean read, and the caller sends no aura on any of them.
func (w *walk) prepullBossEngaged(ctx context.Context, step int) (bool, error) {
	row, ok := w.bossRowAt(step)
	if !ok {
		return false, stopf(exitRaidWalkFailed, "prepull-aura: boss row %d is not one of the plan's rows", step)
	}
	if row.X == 0 && row.Y == 0 {
		return false, stopf(exitRaidWalkFailed,
			"prepull-aura: boss row %d has no point to read the boss's combat state at, so combat cannot be ruled out: no auras were sent", step)
	}
	cmd := fmt.Sprintf("q units %d %s %s %s all", placeBossArenaYd, num32(row.X), num32(row.Y), num32(row.Z))
	out, err := w.tt(ctx, cmd)
	if err != nil {
		return false, stopf(exitRaidWalkFailed,
			"prepull-aura: boss row %d: the arena read (%s) failed, so combat cannot be ruled out: no auras were sent: %v", step, cmd, err)
	}
	body, ok := ttLast(out)
	if !ok {
		return false, stopf(exitRaidWalkFailed,
			"prepull-aura: boss row %d: the arena read (%s) gave no reply, so combat cannot be ruled out: no auras were sent", step, cmd)
	}
	if body.flag("trunc") {
		return false, stopf(exitRaidWalkFailed,
			"prepull-aura: boss row %d: the arena read (%s) lists more units than one reply carries, so combat cannot be ruled out: no auras were sent", step, cmd)
	}
	for _, m := range body.list("units") {
		u := ttUnitOf(m)
		if u.InCombat && !u.Dead {
			w.note("prepull-aura: boss row %d: %s (%d) is in combat in the arena read at %s", step, u.Name, u.Entry, cmd)
			return true, nil
		}
	}
	return false, nil
}

// prepullAuraCombatClear re-reads the commander and every living party member and refuses the batch while any one of
// them is in combat: a pull that starts after the pause must stop the auras, not receive them after the encounter
// began. PartyInfo.Members leaves the commander out, so the watch carries it first; a pull the commander started is
// the one a member-only check misses. It fails closed on the read too: a unit the RPC cannot read may be fighting, so
// the batch stops there as well. The caller builds the watch from the commander and the whole living party, not only
// the members that carry an aura list.
func (w *walk) prepullAuraCombatClear(ctx context.Context, step int) error {
	port := walkAuras{w: w}
	for _, t := range w.prepullAuraWatch {
		u, seen := port.ViewUnit(ctx, t)
		if !seen {
			return stopf(exitRaidWalkFailed,
				"prepull-aura: boss row %d: the unit read of %s failed, so combat cannot be ruled out: no further auras were sent", step, t.Name)
		}
		if u.Dead || u.Ghost {
			continue
		}
		if u.InCombat {
			return stopf(exitRaidWalkFailed,
				"prepull-aura: boss row %d: combat started (%s is in combat): no further auras were sent", step, t.Name)
		}
	}
	return nil
}

// prepullAuraFire puts the flag's lists on every living party member, reads them back, writes prepull-auras.json and
// fails the walk when a bot misses more auras than --prepull-aura-tolerance (0 by default: every requested aura must
// verify). A failed fire is recorded as an attempt with its reason; only a fire whose auras verified within the
// tolerance appends the boss row, so the file never reports a preparation that did not happen. The file is written on
// every fire, failures included, so the attempt's reason and the missing list are always on disk.
func (w *walk) prepullAuraFire(ctx context.Context, step int) (err error) {
	defer func() {
		if err != nil {
			w.prepullAuraAttempts = append(w.prepullAuraAttempts, prepullAuraAttempt{BossRow: step, Reason: err.Error()})
		}
		w.prepullAuraWatch, w.prepullAuraRow = nil, 0
		if werr := w.prepullAuraWrite(); werr != nil {
			w.note("prepull-aura: WARN: %s could not be written (%v)", prepullAuraFileName, werr)
		}
	}()
	party, err := w.party(ctx)
	if err != nil {
		return stopf(exitRaidWalkFailed, "prepull-aura: party: %v", err)
	}
	port := walkAuras{w: w}
	cfg := dungeon.AuraCfg{Gap: prepullAuraGap, Settle: prepullAuraSettle, ViewWait: prepullAuraViewWait}
	// The combat watch is the commander and every living party member, list or no list: a pull may come from the
	// commander (which PartyInfo.Members leaves out) or from a member the flag does not cover. A unit the unit read
	// cannot confirm is living stays in the watch, and its unreadable row fails the recheck: an unproven party is not
	// an out-of-combat party.
	st, err := w.status(ctx)
	if err != nil {
		return stopf(exitRaidWalkFailed, "prepull-aura: the commander's status: %v", err)
	}
	if st.Character == "" || st.GUID == 0 {
		return stopf(exitRaidWalkFailed, "prepull-aura: the status names no commander (character %q, guid %s), so its combat state cannot be read", st.Character, st.GUID)
	}
	watch := []dungeon.AuraTarget{{GUID: st.GUID, Name: st.Character}}
	var targets []prepullAuraTarget
	for _, m := range party.Members {
		if !m.Online || m.Dead || m.GUID == 0 {
			continue
		}
		t := dungeon.AuraTarget{GUID: m.GUID, Name: m.Name}
		u, seen := port.ViewUnit(ctx, t)
		if seen && (u.Dead || u.Ghost) {
			continue
		}
		watch = append(watch, t)
		role := w.prepullRole(m.Name, u.Class)
		list := w.prepullAuras.For(role)
		if len(list) == 0 {
			continue
		}
		rec := w.prepullAuraRecordFor(m.Name, role)
		missing := list
		if seen {
			missing, _ = dungeon.AurasMissing(ctx, port, t, list)
		}
		// Until the read-back below proves otherwise (or the batch aborts early), the auras that did not land are
		// what the bot lacks: the file never claims a preparation the walk has not seen.
		rec.Missing = raidwalk.PrepullIDs(missing)
		rec.Seen = subtractIDs(raidwalk.PrepullIDs(list), rec.Missing)
		targets = append(targets, prepullAuraTarget{rec: rec, unit: t, want: list, missing: missing})
	}
	w.prepullAuraWatch, w.prepullAuraRow = watch, step
	if err := w.prepullAuraCombatClear(ctx, step); err != nil {
		return err
	}
	for i := range targets {
		tg := &targets[i]
		if len(tg.missing) == 0 {
			continue
		}
		if oc, err := dungeon.ApplyAuras(ctx, port, tg.unit, tg.missing, cfg, w.env.now); err != nil {
			return err
		} else if oc != dungeon.AuraTaken {
			w.note("prepull-aura: %s: the first .aura (%s) did not land on the bot", tg.rec.Bot, tg.missing[0].Name)
		}
	}
	// One read-back with a short retry window: the auras sent last need a moment to show in the unit store, and a
	// bot the store does not hold tells nothing (an unproven aura counts as missing).
	tol := w.opts.prepullAuraTolerance
	var over, within []string
	for deadline := w.env.now().Add(prepullAuraVerifyWait); ; {
		over, within = over[:0], within[:0]
		for i := range targets {
			tg := &targets[i]
			missing, seen := dungeon.AurasMissing(ctx, port, tg.unit, tg.want)
			if !seen {
				missing = tg.want
			}
			tg.rec.Missing = raidwalk.PrepullIDs(missing)
			tg.rec.Seen = subtractIDs(raidwalk.PrepullIDs(tg.want), tg.rec.Missing)
			switch {
			case len(missing) > tol:
				over = append(over, tg.rec.Bot)
			case len(missing) > 0:
				within = append(within, tg.rec.Bot)
			}
		}
		if len(over) == 0 || !w.env.now().Before(deadline) {
			break
		}
		if err := w.env.sleep(ctx, prepullAuraVerifyPoll); err != nil {
			return err
		}
	}
	if len(over) > 0 {
		return stopf(exitRaidWalkFailed, "prepull-aura: boss row %d: %d bot(s) miss auras beyond the tolerance of %d (%s): %s",
			step, len(over), tol, w.prepullAuraMissSummary(over), prepullAuraFileName)
	}
	if len(within) > 0 {
		w.logLine(w.env.now(), fmt.Sprintf("prepull-aura: boss row %d: %d bot(s) carry their lists; %d bot(s) miss auras within the tolerance of %d: %s",
			step, len(targets)-len(within), len(within), tol, w.prepullAuraMissSummary(within)))
	} else {
		w.logLine(w.env.now(), fmt.Sprintf("prepull-aura: boss row %d: %d bot(s) carry their lists", step, len(targets)))
	}
	// The row is a verified preparation now, and only now: every failure above left it out (its attempt carries the
	// reason), so a consumer of the file reads boss_rows as applications that happened.
	if w.prepullAuraRows == nil || !slices.Contains(w.prepullAuraRows, step) {
		w.prepullAuraRows = append(w.prepullAuraRows, step)
	}
	return nil
}

// prepullAuraClear forgets which boss rows fired, so the rest before the next boss row (the one the wipe retries)
// sends the lists again: a death stripped them. The bots' records keep their sent ids; the next fire's read-back
// rewrites seen and missing.
func (w *walk) prepullAuraClear() {
	if len(w.prepullAuras) == 0 {
		return
	}
	w.prepullAuraSent = nil
}

// prepullAuraRecordFor is the bot's accumulating record, created on its first fire.
func (w *walk) prepullAuraRecordFor(name, role string) *prepullAuraRecord {
	if w.prepullAuraBots == nil {
		w.prepullAuraBots = map[string]*prepullAuraRecord{}
	}
	rec, ok := w.prepullAuraBots[name]
	if !ok {
		rec = &prepullAuraRecord{Bot: name, Role: role, Sent: []uint32{}, Seen: []uint32{}, Missing: []uint32{}}
		w.prepullAuraBots[name] = rec
	}
	if rec.Role == "" {
		rec.Role = role
	}
	return rec
}

// prepullRole is the aura role a bot plays: the walk's own tanks (mt and ot) are "tank", and every other bot takes its
// class's role (raidwalk.ClassAuraRole). "" means only the "all" lists apply.
func (w *walk) prepullRole(name string, class uint8) string {
	if w.mt != "" && strings.EqualFold(name, w.mt) {
		return raidwalk.PrepullTank
	}
	for _, ot := range w.ots {
		if strings.EqualFold(name, ot) {
			return raidwalk.PrepullTank
		}
	}
	return raidwalk.ClassAuraRole(class)
}

// prepullAuraMissSummary names bots and what each of them lacks.
func (w *walk) prepullAuraMissSummary(bots []string) string {
	var parts []string
	for _, name := range bots {
		rec := w.prepullAuraBots[name]
		if rec == nil {
			parts = append(parts, name)
			continue
		}
		parts = append(parts, fmt.Sprintf("%s (missing %s)", name, idWords(rec.Missing)))
	}
	return strings.Join(parts, "; ")
}

// prepullAuraWrite rewrites prepull-auras.json with the walk's whole prepull state.
func (w *walk) prepullAuraWrite() error {
	f := prepullAuraFile{Flag: w.opts.prepullAura, Tolerance: w.opts.prepullAuraTolerance, BossRows: w.prepullAuraRows,
		Attempts: append([]prepullAuraAttempt{}, w.prepullAuraAttempts...), Lists: []prepullAuraList{}, Bots: []prepullAuraRecord{}}
	for _, role := range raidwalk.PrepullRoles {
		if ids := raidwalk.PrepullIDs(w.prepullAuras.For(role)); len(ids) > 0 {
			f.Lists = append(f.Lists, prepullAuraList{Role: role, IDs: ids})
		}
	}
	names := make([]string, 0, len(w.prepullAuraBots))
	for name := range w.prepullAuraBots {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		f.Bots = append(f.Bots, *w.prepullAuraBots[name])
	}
	data, err := json.MarshalIndent(f, "", " ")
	if err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(w.outDir, prepullAuraFileName), append(data, '\n'), 0600)
}

// subtractIDs is want without the ids of missing, in want's order.
func subtractIDs(want, missing []uint32) []uint32 {
	out := make([]uint32, 0, len(want))
	for _, id := range want {
		if !slices.Contains(missing, id) {
			out = append(out, id)
		}
	}
	return out
}

// prepullNames writes an aura list for the dry run ("Elixir of the Mongoose (17538), ...").
func prepullNames(list []dungeon.Buff) string {
	words := make([]string, len(list))
	for i, b := range list {
		words[i] = b.String()
	}
	return strings.Join(words, ", ")
}

// idWords writes ids for a note ("17538, 11405").
func idWords(ids []uint32) string {
	words := make([]string, len(ids))
	for i, id := range ids {
		words[i] = fmt.Sprintf("%d", id)
	}
	return strings.Join(words, ", ")
}

// walkAuras is the walk's dungeon.AuraPort: the agent's gm_aura RPC (a confirmed select then ".aura <id>"), the unit
// RPC for the target's auras and the commander's own, the walk's clock and its notes.
type walkAuras struct{ w *walk }

// SendAura puts one buff on the target with the gm_aura RPC, which selects the bot by guid, waits for the server to
// show that selection and sends the line only while it stands (session.GMAura). It first re-reads the party: a pull
// that started after the pause stops the batch before this line goes out. A refused or failed call breaks the walk: a
// preparation the walk could not send is not a preparation it may report.
func (p walkAuras) SendAura(ctx context.Context, t dungeon.AuraTarget, b dungeon.Buff) error {
	if len(p.w.prepullAuraWatch) > 0 {
		if err := p.w.prepullAuraCombatClear(ctx, p.w.prepullAuraRow); err != nil {
			return err
		}
	}
	line := fmt.Sprintf(".aura %d", b.ID)
	p.w.logLine(p.w.env.now(), fmt.Sprintf("> %s (gm_aura, selected %s)", line, t.Name))
	params := map[string]any{"guid": t.GUID.String(), "spell_id": b.ID}
	if err := p.w.agent.Call(ctx, "gm_aura", params, nil, 15*time.Second); err != nil {
		return stopf(exitRaidWalkFailed, "prepull-aura: %s on %s: %v", line, t.Name, err)
	}
	if rec := p.w.prepullAuraBots[t.Name]; rec != nil && !slices.Contains(rec.Sent, b.ID) {
		rec.Sent = append(rec.Sent, b.ID)
	}
	return nil
}

// ViewUnit reads the target's unit row from the agent's store (auras included); false when the store does not hold it.
func (p walkAuras) ViewUnit(ctx context.Context, t dungeon.AuraTarget) (objects.Unit, bool) {
	var u objects.Unit
	if err := p.w.agent.Call(ctx, "unit", map[string]any{"guid": t.GUID.String()}, &u, 10*time.Second); err != nil {
		return objects.Unit{}, false
	}
	if u.GUID != t.GUID {
		return objects.Unit{}, false
	}
	return u, true
}

// CasterAuras is the commander's own auras: an aura that lands on it is a miss (the selection did not hold, or the
// account is below developer level and the core puts every aura on the sender).
func (p walkAuras) CasterAuras(ctx context.Context) ([]objects.Aura, bool) {
	st, err := p.w.status(ctx)
	if err != nil || st.Character == "" {
		return nil, false
	}
	u, seen := p.ViewUnit(ctx, dungeon.AuraTarget{GUID: st.GUID, Name: st.Character})
	if !seen {
		return nil, false
	}
	return u.Auras, true
}

func (p walkAuras) Sleep(ctx context.Context, d time.Duration) error { return p.w.env.sleep(ctx, d) }

func (p walkAuras) Note(msg string) {
	p.w.note("prepull-aura: %s", msg)
	p.w.logLine(p.w.env.now(), "prepull-aura: "+msg)
}
