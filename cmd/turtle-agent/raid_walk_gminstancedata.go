// raid_walk_gminstancedata.go: the `--gm-instance-data` test tool of
// raid-walk (owner-approved, test servers only, off by default). It replaces
// walk 39's hkgmall.sh shell loop: once a raid reached the Hakkar leg, that
// loop sent `.debug instancedata 1 3`, `.debug instancedata 6 3`,
// `.debug instancedata 0 0` as say chat over the slot's rpc, 3 s apart, and a
// glob bug in it cost ~4 h of Hakkar attempts in walk 39.
//
// A walk with `--gm-instance-data "<leg>:<a> <b>[;<a> <b>...]"` (repeatable,
// one leg per value) has the commander send each command as
// `.debug instancedata <a> <b>`, in order, 3 s apart, on every launch: a
// placed start inside the leg fires on the placement's leg BEFORE the run
// starts, and a walk that reaches the leg mid-run fires under a run pause
// (the next pull waits). Without --place the entry's instance is read the
// way the cap ledger reads it at entry.
//
// The lines go out through the walk's own gm path (w.gm, the commander's gm
// RPC allow-listed by raidwalk.AllowedLine): a refused or failed send breaks
// the walk instead of losing the pull gate silently. Every sent command is
// one event line in run.log (`gm-instance-data: <leg>: .debug instancedata
// <a> <b> -> <reply>`). A marker file per instance and leg in the walk's out
// dir remembers the sent commands, so ONE walk never fires the same leg twice.
// Every launch sends, whatever the reuse mode: a --reuse-instance restart after
// a wipe runs in a fresh out dir and resends, because the instance data does
// NOT survive a wipe (walk 44, raid hk7m attempt -3: no lines on the restart,
// and Hakkar cast Aspect of Jeklik/Venoxis again). The zulgurub boss scripts
// overwrite their own slot on Reset (boss_jeklik.cpp:105-106,
// boss_venoxis.cpp:152-153), see docs/okf/raids/raid-walk-reference.md.
// Resending into data that did survive is harmless (SetData is idempotent).
// After `run start` the lines go out ONLY at a rest or gather status that passes
// the pre-pull gate (gmInstanceDataMayFire): any other status defers a pending
// send. A walk also resends once per boss row, at that rest of the step before
// the row (a boss row with no such rest warns and skips): a GM kill does not stop the respawn, and a
// respawned priest's Reset() overwrites its data (walk 45 hk7m).
package main

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/pjerra/turtle-agent/internal/events"
	"github.com/pjerra/turtle-agent/internal/planexport"
	"github.com/pjerra/turtle-agent/internal/raidwalk"
	"github.com/pjerra/turtle-agent/internal/tbm"
)

// gmInstanceDataGap is the pause between two instance-data lines, 3 s like
// the shell loop's. A var so the tests run it at once.
var gmInstanceDataGap = 3 * time.Second

// validateGMInstanceData parses the flag values and resolves the legs against
// the rows: an unknown leg is a phase-1 refusal (exit 2), like --gm-clear's
// unknown groups. A leg named twice is refused.
func (w *walk) validateGMInstanceData() error {
	if len(w.opts.gmInstanceData) == 0 {
		return nil
	}
	var specs []raidwalk.GMInstanceSpec
	for _, v := range w.opts.gmInstanceData {
		sp, err := raidwalk.ParseGMInstanceData(v)
		if err != nil {
			return stopf(exitUsage, "%v", err)
		}
		specs = append(specs, sp)
	}
	planned, err := raidwalk.PlanGMInstanceData(w.rows, specs)
	if err != nil {
		return stopf(exitUsage, "%v", err)
	}
	w.gmInstanceData = planned
	return nil
}

// gmInstanceDataEntryInst is the walk's memory for firing outside its own
// placement (a field on walk): the instance id read the way the cap ledger
// reads it at entry ("" until read, "unreadable" when the read failed and
// the walk fell back to the per-out-dir marker only).

// gmInstanceDataInst is the instance id as the markers key it. With a
// placement it is the placement's copy ("" when unknown: no firing);
// without one (or a door start, which places nothing) it is read the way
// the cap ledger reads it at entry (the caller's `q inst`), kept once read.
// "unreadable" means the read errored: the walk WARNs once and fires
// marker-only. A read of
// 0 (the toolkit answered but named no instance) is NOT cached: the next
// firing retries the read, and the walk WARNs once. A read failure never
// fails the walk: ttInst would, so the toolkit is asked directly and any
// failure falls back.
func (w *walk) gmInstanceDataInst(ctx context.Context) string {
	if w.place != nil {
		if w.placeInst == 0 {
			return ""
		}
		return fmt.Sprintf("%.0f", w.placeInst)
	}
	if w.gmInstanceDataEntryInst != "" {
		return w.gmInstanceDataEntryInst
	}
	out, err := w.tt(ctx, "q inst")
	inst := 0.0
	if err == nil {
		if body, ok := ttLast(out); ok {
			inst, _ = body.num("inst")
		}
	}
	if err != nil {
		w.gmInstanceDataEntryInst = "unreadable"
		w.note("gm-instance-data: WARN: the walk has no placement, and the instance id could not be read (%v): the lines fire once per out dir, not per instance", err)
		return w.gmInstanceDataEntryInst
	}
	if inst == 0 {
		if !w.gmInstanceDataOZeroWarned {
			w.gmInstanceDataOZeroWarned = true
			w.note("gm-instance-data: WARN: the walk has no placement, and the toolkit gave no instance id: retrying the read on the next status")
		}
		return ""
	}
	w.gmInstanceDataEntryInst = fmt.Sprintf("%.0f", inst)
	return w.gmInstanceDataEntryInst
}

// gmInstanceDataDone says the leg's lines already went out in this walk's
// instance. Only the out dir's marker counts (keyed by the instance, or by
// "unplaced" when the id could not be read): it is per launch, so every new
// launch, a --reuse-instance restart included, sends again. A changed command
// list is never done: the marker carries the list itself.
func (w *walk) gmInstanceDataDone(inst string, spec raidwalk.GMInstanceSpec) bool {
	key := inst
	if key == "unreadable" {
		key = "unplaced"
	}
	return key != "" && raidwalk.NewInstanceMarks(w.outDir).LegDone(key, spec.Leg, spec)
}

// gmInstanceDataClear forgets what this walk sent, markers and retry counts,
// so the next status naming a leg with instance-data lines sends them again.
// A wipe recovery calls it: the revive is in place, the walk keeps its out
// dir, and the marker would otherwise call the lines done. Without
// --gm-instance-data it touches nothing.
func (w *walk) gmInstanceDataClear() {
	if len(w.gmInstanceData) == 0 {
		return
	}
	if err := raidwalk.NewInstanceMarks(w.outDir).Clear(); err != nil {
		w.note("gm-instance-data: WARN: the markers could not be cleared after the wipe recovery (%v): the lines may not go out again", err)
	}
	w.gmInstanceDataTries = nil
}

// gmInstanceDataMaxTries bounds the per-command retries: a command the
// server never confirms is tried at most 3 times per walk, then the walk
// logs `gm-instance-data: <leg>: <cmd> NOT confirmed after 3 tries` and
// continues with the next command (never failing the walk on this).
const gmInstanceDataMaxTries = 3

// gmInstanceDataFire sends one spec's commands in order, gmInstanceDataGap
// apart. Each command is recorded in the out dir's marker (keyed by the
// instance, or by "unplaced" when the id could not be read) ONLY when the
// server confirms it: a refused command is NOT recorded, so the next firing
// retries it, at most gmInstanceDataMaxTries times per walk. Silence counts
// as confirmed: the debug handler in the core
// (Commands.cpp:17778 HandleDebugSetInstanceDataCommand, Chat.cpp:172)
// prints nothing on success — it calls pInstData->SetData and returns
// true, and ExecuteCommand sends nothing on a true return — so no system
// line in 2 s is the success path (the shell loop it replaces never read a
// reply either). The marker always holds the confirmed commands of the spec
// in spec order (rewritten every pass), so a refire never duplicates and
// LegDone reads back once every command is confirmed. A refire skips the
// already-confirmed commands' sends. An interrupt or a refusal before the last
// command resends the rest on the next firing. A changed command list is
// never done: the marker carries the list itself.
func (w *walk) gmInstanceDataFire(ctx context.Context, inst string, spec raidwalk.GMInstanceSpec) error {
	key := inst
	if key == "unreadable" {
		key = "unplaced"
	}
	marks := raidwalk.NewInstanceMarks(w.outDir)
	confirmed := gmInstanceDataConfirmedSet(marks, key, spec.Leg, spec)
	for i, cmd := range spec.Cmds {
		line := raidwalk.GMInstanceDataLine(cmd)
		if confirmed[line] || w.gmInstanceDataTriedOut(spec.Leg, line) {
			continue
		}
		if err := w.gm(ctx, line); err != nil {
			return err
		}
		reply, ok := w.gmInstanceDataReply(ctx)
		if ok {
			confirmed[line] = true
			w.logLine(w.env.now(), raidwalk.GMInstanceDataLogLine(spec.Leg, cmd, reply))
			w.gmInstanceDataNoteTry(lineKey(spec.Leg, line), true)
			// Persist after every command: an interrupt before the last
			// command keeps the confirmed prefix, and the next firing
			// resends only the rest.
			if err := gmInstanceDataWriteConfirmed(marks, key, spec.Leg, spec, confirmed); err != nil {
				return stopf(exitRaidWalkFailed, "gm-instance-data: %s: %v", spec.Leg, err)
			}
		} else {
			w.logLine(w.env.now(), raidwalk.GMInstanceDataLogLine(spec.Leg, cmd, reply+" (REFUSED)"))
			w.note("gm-instance-data: WARN: %s: %s not confirmed (%s): retrying on the next status", spec.Leg, line, reply)
			if n := w.gmInstanceDataNoteTry(lineKey(spec.Leg, line), false); n >= gmInstanceDataMaxTries {
				w.logLine(w.env.now(), fmt.Sprintf("gm-instance-data: %s: %s NOT confirmed after %d tries", spec.Leg, line, gmInstanceDataMaxTries))
				w.note("gm-instance-data: %s: %s NOT confirmed after %d tries", spec.Leg, line, gmInstanceDataMaxTries)
			}
		}
		if i+1 < len(spec.Cmds) {
			if err := w.env.sleep(ctx, gmInstanceDataGap); err != nil {
				return stopf(exitRaidWalkFailed, "gm-instance-data: %s: interrupted", spec.Leg)
			}
		}
	}
	return nil
}

// lineKey identifies one command's retry count: the leg plus the exact line.
func lineKey(leg, line string) string { return leg + "\x00" + line }

// gmInstanceDataNoteTry records one command's attempt: confirmed resets its
// count, unconfirmed increments it and reports the new count.
func (w *walk) gmInstanceDataNoteTry(key string, confirmed bool) int {
	if confirmed {
		if w.gmInstanceDataTries != nil {
			delete(w.gmInstanceDataTries, key)
		}
		return 0
	}
	if w.gmInstanceDataTries == nil {
		w.gmInstanceDataTries = map[string]int{}
	}
	w.gmInstanceDataTries[key]++
	return w.gmInstanceDataTries[key]
}

// gmInstanceDataTriedOut says the command already used its tries: the
// firing skips it (its NOT-confirmed line is already in run.log).
func (w *walk) gmInstanceDataTriedOut(leg, line string) bool {
	return w.gmInstanceDataTries[lineKey(leg, line)] >= gmInstanceDataMaxTries
}

// gmInstanceDataConfirmedSet reads which commands of the spec the marker
// already holds as confirmed (a set of exact command lines).
func gmInstanceDataConfirmedSet(marks raidwalk.InstanceMarks, key, leg string, spec raidwalk.GMInstanceSpec) map[string]bool {
	out := map[string]bool{}
	have, err := marks.LegLog(key, leg)
	if err != nil {
		return out
	}
	for _, h := range have {
		h = strings.TrimSpace(h)
		for _, cmd := range spec.Cmds {
			if h == raidwalk.GMInstanceDataLine(cmd) {
				out[h] = true
			}
		}
	}
	return out
}

// gmInstanceDataWriteConfirmed rewrites the marker with the confirmed
// commands in spec order, so a refire never duplicates lines and LegDone
// reads back once every command is confirmed.
func gmInstanceDataWriteConfirmed(marks raidwalk.InstanceMarks, key, leg string, spec raidwalk.GMInstanceSpec, confirmed map[string]bool) error {
	var cmds [][2]int
	for _, cmd := range spec.Cmds {
		if confirmed[raidwalk.GMInstanceDataLine(cmd)] {
			cmds = append(cmds, cmd)
		}
	}
	return marks.Write(key, leg, cmds)
}

// gmInstanceDataReplyWait is the server reply's window: up to 2 s after
// each line. The reader polls one no-wait page at a time; each page keeps
// the walk's event cursor moving, so the run's own status lines behind a
// reply are never skipped unlogged. The 3 s gap before the next line is
// extra window: a reply that lands within it is the next poll's own read.
const gmInstanceDataReplyWait = 2 * time.Second

// gmInstanceDataRefusal says whether the system line refuses the command
// instead of confirming it. The debug handler in the core
// (Commands.cpp:17778 HandleDebugSetInstanceDataCommand, Chat.cpp:172)
// prints nothing on success: it calls pInstData->SetData only. What the
// walk may see instead is ExecuteCommand's failure text (the handler's
// false return sends the command's Help or LANG_CMD_SYNTAX
// "Incorrect syntax.", sql base entry 10), an unknown/low-security refusal
// ("There is no such command", "There is no such subcommand",
// "This command is not available to you.", "You can use the command"),
// or a GM-mode line ("GM mode is OFF"/"GM mode is ON" head).
func gmInstanceDataRefusal(text string) bool {
	l := strings.ToLower(text)
	for _, s := range []string{
		"there is no such command",
		"there is no such subcommand",
		"incorrect syntax.",
		"this command is not available to you.",
		"you can use the command",
		"no such command",
		"gm mode is off",
		"gm mode is on",
		"not a gm account",
	} {
		if strings.Contains(l, s) {
			return true
		}
	}
	return false
}

// gmInstanceDataReply waits up to gmInstanceDataReplyWait for the server's
// answer to the line just sent. It returns the first plain system line
// when one arrives (a refusal line means NOT confirmed), or "sent" when
// the window passes silently: the handler prints nothing on success, so
// silence IS the success path and counts as confirmed. An unconfirmed
// command is retried on the next status poll, at most
// gmInstanceDataMaxTries times per walk. It never fails the walk: a
// refusing reply is a REFUSED event line and a retry, not an error.
// The reader only advances the cursor and scans for the reply: TBM:RUN
// lines on its pages are NOT logged, fed or adopted here — the firing's
// caller (pollOnce) rewinds the cursor to the firing line, so the next
// poll re-reads the tail in order and adopts it exactly once. (Feeding
// newer lines here while the caller still processed its older page is what
// regressed lastStatus.)
func (w *walk) gmInstanceDataReply(ctx context.Context) (string, bool) {
	deadline := w.env.now().Add(gmInstanceDataReplyWait)
	for {
		var page events.Page
		params := map[string]any{"since": w.eventsAt, "kinds": []string{"system", "addon"}, "wait_ms": 0}
		if err := w.agent.Call(ctx, "events", params, &page, 15*time.Second); err != nil {
			return "sent", true
		}
		w.eventsAt = page.Next
		for _, e := range page.Events {
			if _, ok := tbm.Line(e); ok {
				continue
			}
			if e.Kind != events.KindSystem || e.Text == "" {
				continue
			}
			text := raidwalk.CleanLine(e.Text)
			if gmInstanceDataRefusal(text) {
				return text, false
			}
			return text, true
		}
		if !w.env.now().Before(deadline) {
			return "sent", true
		}
		if err := w.env.sleep(ctx, 200*time.Millisecond); err != nil {
			return "sent", true
		}
	}
}

// gmInstanceDataPendingLeg is the firing check the pre-start call uses:
// the specs for the leg the walk stands on that are not done in this
// instance, in flag order. leg is the placement's leg ("" for an explicit
// --from step: the walk names no leg before the run starts, so nothing
// fires there — the run's first status line fires it in phaseStart's
// wake instead, still before the first pull).
func (w *walk) gmInstanceDataPendingLeg(inst, leg string) []raidwalk.GMInstanceSpec {
	if len(w.gmInstanceData) == 0 || inst == "" || inst == "unreadable" || leg == "" {
		return nil
	}
	var out []raidwalk.GMInstanceSpec
	for _, spec := range w.gmInstanceData {
		if !strings.EqualFold(spec.Leg, leg) {
			continue
		}
		if w.gmInstanceDataDone(inst, spec) {
			continue
		}
		out = append(out, spec)
	}
	return out
}

// gmInstanceDataPending is the firing check the running walk uses: the
// specs whose leg this status names that are not done in this instance, in
// flag order. inst "unreadable" still fires (marker-only under "unplaced");
// "" means unknown: no firing.
func (w *walk) gmInstanceDataPending(inst string, st tbm.RunStatus) []raidwalk.GMInstanceSpec {
	if len(w.gmInstanceData) == 0 {
		return nil
	}
	if st.State != tbm.RunRunning && st.State != tbm.RunPaused {
		return nil
	}
	if inst == "" {
		return nil
	}
	var out []raidwalk.GMInstanceSpec
	for _, spec := range w.gmInstanceData {
		if !raidwalk.LegMatchesStatus(spec.Leg, st.Leg) {
			continue
		}
		if w.gmInstanceDataDone(inst, spec) {
			continue
		}
		out = append(out, spec)
	}
	return out
}

// gmInstanceDataStart fires the specs for the leg the walk stands on,
// BEFORE the run starts: the placement's leg fires here, once per
// instance. leg "" (an explicit --from step) fires nothing: the run's
// first status line fires it in phaseStart's wake instead, still before
// the first pull. A failing send breaks the walk. inst "" means unknown:
// no firing; "unreadable" fires marker-only.
func (w *walk) gmInstanceDataStart(ctx context.Context, inst, leg string) error {
	for _, spec := range w.gmInstanceDataPendingLeg(inst, leg) {
		if err := w.gmInstanceDataFire(ctx, inst, spec); err != nil {
			return err
		}
	}
	return nil
}

// gmInstanceDataSendPending is pollOnce's firing: a spec whose leg this
// status names fires here, once per walk, when a walk reaches the leg
// mid-run, but only at an allowlisted status (gmInstanceDataMayFire: a
// pre-pull-gated rest or gather); every other status defers it. A firing here holds the run's next pull until the lines are
// sent: the walk pauses the run first (`.bot run pause`, through the
// commander's own run verbs) and resumes it after the last line, so the
// pull gate's lines land before the next pull. Whenever the status was
// running before the pause attempt, the walk always sends the resume
// afterwards — even when the pause call errored or timed out (a mid-run
// pause may still have landed server-side; leaving the run paused stops
// the raid silently). A pause that was never attempted (the status was
// already paused) sends no resume. A refused resume is a failed walk (the
// raid would stand paused: that must be loud, like a failed send). At most
// one spec fires per poll: the next poll fires the next. It reports whether
// a firing happened: pollOnce then breaks out of its line loop for this
// poll (the next poll re-reads from the cursor), so the rest of the old
// page's lines never process past the adopted statuses of the firing.
func (w *walk) gmInstanceDataSendPending(ctx context.Context, st tbm.RunStatus, later bool) (bool, error) {
	// Idle fast path: no specs, or a state with no leg to name — never
	// touch the toolkit. The instance read below is a real `q inst`.
	if len(w.gmInstanceData) == 0 {
		return false, nil
	}
	if st.State != tbm.RunRunning && st.State != tbm.RunPaused {
		return false, nil
	}
	inst := w.gmInstanceDataInst(ctx)
	pending := w.gmInstanceDataPending(inst, st)
	if bossStep, resend := w.gmInstanceDataBossRow(inst, st, later); bossStep > 0 {
		// Once per boss row per walk, whether the leg's lines were pending (they
		// go out now), resent at the rest before the row, or skipped (WARN).
		if w.gmInstanceDataBossSent == nil {
			w.gmInstanceDataBossSent = map[int]bool{}
		}
		w.gmInstanceDataBossSent[bossStep] = true
		if len(pending) == 0 && resend {
			pending = w.gmInstanceDataBossResend(inst, st)
		}
	}
	if !w.gmInstanceDataMayFire(st, later) {
		pending = nil
	}
	if len(pending) == 0 {
		return false, nil
	}
	// one spec per poll; the next poll fires the rest
	spec := pending[0]
	pauseAttempted := st.State == tbm.RunRunning
	if pauseAttempted {
		if _, err := w.commander(ctx, commanderReq{Op: "run", Verb: "pause", WaitMS: 10000}); err != nil {
			w.note("gm-instance-data: WARN: the run pause before %s was refused (%v): the lines go out unheld", spec.Leg, err)
		}
	}
	if err := w.gmInstanceDataFire(ctx, inst, spec); err != nil {
		return true, err
	}
	if pauseAttempted {
		if _, err := w.commander(ctx, commanderReq{Op: "run", Verb: "resume", WaitMS: 10000}); err != nil {
			return true, stopf(exitRaidWalkFailed, "gm-instance-data: %s: the run resume after the lines failed: %v", spec.Leg, err)
		}
	}
	return true, nil
}

// gmInstanceDataFrameFresh is how old a bot_view frame may be and still count
// for the "party is out of combat" check of the boss-row resend.
const gmInstanceDataFrameFresh = 15 * time.Second

// gmInstanceDataBossRow decides the pre-boss resend for a status. Walk 45
// hk7m: the launch send went out at 14:27, but a GM `.die` does not stop the
// respawn, and a respawned priest's Reset() overwrites its DONE data before the
// run reaches Hakkar's row. So the lines go out again, once per boss row per
// walk, but ONLY at a confirmed out-of-combat rest/gather status of the step
// before the boss row (the run is idle there, the pause costs nothing).
//
// The boss row's own status is never a place to send: it becomes current about
// 1 s before the run's FightStep can pull (DungeonRunPolicy.h:5661,
// Commander.cpp:6886), a pause during a fight drops the focus and holds ~7 s
// inside the boss (DungeonRunPolicy.h:3542-3567), and a quiet begin status can
// be followed by the pull on the next event page, which no look-ahead sees. A
// boss row that becomes current with no pre-boss resend logs one WARN and
// counts as handled: the walk never pauses at the boss.
//
// It returns the boss step to mark as handled (0: nothing to do) and whether
// the lines go out now.
func (w *walk) gmInstanceDataBossRow(inst string, st tbm.RunStatus, later bool) (int, bool) {
	if len(w.gmInstanceData) == 0 || inst == "" {
		return 0, false
	}
	if st.State != tbm.RunRunning && st.State != tbm.RunPaused {
		return 0, false
	}
	legSpec := false
	for _, spec := range w.gmInstanceData {
		if raidwalk.LegMatchesStatus(spec.Leg, st.Leg) {
			legSpec = true
		}
	}
	if !legSpec {
		return 0, false
	}
	if st.Kind == planexport.KindBoss {
		if _, ok := w.bossRowAt(st.Step); !ok || w.gmInstanceDataBossSent[st.Step] {
			return 0, false
		}
		w.note("gm-instance-data: no out-of-combat rest before boss row %d; resend skipped", st.Step)
		return st.Step, false
	}
	// The step before a boss row, resting or gathering out of combat.
	next := st.Step + 1
	if _, ok := w.bossRowAt(next); !ok || w.gmInstanceDataBossSent[next] {
		return 0, false
	}
	if p := strings.ToLower(st.Phase); p != "rest" && p != "gather" {
		return 0, false
	}
	if !w.gmInstanceDataPrePull(st, later, w.env.now()) {
		return 0, false
	}
	return next, true
}

// gmInstanceDataPrePull says the status is still before the pull: phase begin,
// gather, walk or rest, no pulling or fighting note, no later line of the same
// event page showing the pull (later), and no live bot of the party in combat in
// a recent frame.
func (w *walk) gmInstanceDataPrePull(st tbm.RunStatus, later bool, now time.Time) bool {
	if later {
		return false
	}
	switch strings.ToLower(st.Phase) {
	case "begin", "gather", "walk", "rest":
	default:
		return false
	}
	if gmInstanceDataPullNote(st.Note) {
		return false
	}
	for _, b := range w.botAt {
		if b.inCombat && !b.dead && now.Sub(b.at) <= gmInstanceDataFrameFresh {
			return false
		}
	}
	return true
}

// gmInstanceDataPullNote says a run note names a pull or a fight.
func gmInstanceDataPullNote(note string) bool {
	n := strings.ToLower(note)
	return strings.Contains(n, "pulling") || strings.Contains(n, "fighting")
}

// gmInstanceDataLaterPull says a later line of the same event page shows the
// pull or the fight of the same step (a pull phase or a pulling/fighting note):
// the status being handled is older than the pull.
func (w *walk) gmInstanceDataLaterPull(rest []events.Event, step int) bool {
	for _, e := range rest {
		line, ok := tbm.Line(e)
		if !ok {
			continue
		}
		st, ok := raidwalk.ParseRun(line)
		if !ok || st.Plan != w.plan.Name || st.Step != step {
			continue
		}
		if strings.EqualFold(st.Phase, "pull") || gmInstanceDataPullNote(st.Note) {
			return true
		}
	}
	return false
}

// gmInstanceDataBossResend forgets the marker and the retry counts of the
// status leg's specs (which the launch send marked done) and returns them for
// the firing, so gmInstanceDataFire sends every line again.
func (w *walk) gmInstanceDataBossResend(inst string, st tbm.RunStatus) []raidwalk.GMInstanceSpec {
	key := inst
	if key == "unreadable" {
		key = "unplaced"
	}
	marks := raidwalk.NewInstanceMarks(w.outDir)
	var out []raidwalk.GMInstanceSpec
	for _, spec := range w.gmInstanceData {
		if !raidwalk.LegMatchesStatus(spec.Leg, st.Leg) {
			continue
		}
		if err := marks.Write(key, spec.Leg, nil); err != nil {
			w.note("gm-instance-data: WARN: the %s marker could not be cleared for the boss-row resend (%v): the lines may not go out again", spec.Leg, err)
			continue
		}
		for _, cmd := range spec.Cmds {
			delete(w.gmInstanceDataTries, lineKey(spec.Leg, raidwalk.GMInstanceDataLine(cmd)))
		}
		out = append(out, spec)
	}
	return out
}

// gmInstanceDataMayFire is the allowlist for every send after `run start`: the
// lines may go out ONLY at a rest or gather status that passes the pre-pull gate
// (no pulling or fighting note, no later pull line in the page, no bot of the
// party in combat in a frame under gmInstanceDataFrameFresh old). The launch send
// before `run start` (phases) is the only other place. At any other status a
// pending send is deferred and never fires: walking, begin, pull, fight, hold,
// boss rows and phases the walk does not know a pause there could land
// mid-pull. A deferred send stays pending (its marker is not done) and goes out
// at the next allowed status of the leg; the wipe recovery's cleared markers wait
// the same way.
func (w *walk) gmInstanceDataMayFire(st tbm.RunStatus, later bool) bool {
	switch strings.ToLower(st.Phase) {
	case "rest", "gather":
		return w.gmInstanceDataPrePull(st, later, w.env.now())
	}
	return false
}
