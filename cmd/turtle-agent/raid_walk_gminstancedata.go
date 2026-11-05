// raid_walk_gminstancedata.go: the `--gm-instance-data` test tool of
// raid-walk (owner-approved, test servers only, off by default). It replaces
// walk 39's hkgmall.sh shell loop: once a raid reached the Hakkar leg, that
// loop sent `.debug instancedata 1 3`, `.debug instancedata 6 3`,
// `.debug instancedata 0 0` as say chat over the slot's rpc, 3 s apart, and a
// glob bug in it cost ~4 h of Hakkar attempts in walk 39.
//
// A walk with `--gm-instance-data "<leg>:<a> <b>[;<a> <b>...]"` (repeatable,
// one leg per value) has the commander send each command as
// `.debug instancedata <a> <b>`, in order, 3 s apart, once per instance: a
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
// dir, plus a mark (with the command list's hash) in the instance-cap ledger
// (the walk's existing state file, which outlives the attempt's out dir),
// remembers the sent commands, so a resumed walk in the same instance
// (--reuse-instance) never refires: resending is harmless but noisy.
package main

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/pjerra/turtle-agent/internal/events"
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
// marker-only (the ledger keys on instances, so it is skipped). A read of
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

// gmInstanceDataDone says the leg's lines already went out in this instance.
// The out dir's marker always counts (keyed by the instance, or by "unplaced"
// when the id could not be read). The ledger counts ONLY on a --reuse-instance
// walk: it continues the same instance in a fresh out dir, so the out dir's
// marker cannot tell it — but a fresh walk always sends, even when an old mark
// names the same instance id (the core reuses ids across copies). A changed
// command list is never done: the ledger's mark carries the hash it was sent
// with, and the marker carries the list itself. An "unreadable" instance is
// marker-only: the ledger keys on instances.
func (w *walk) gmInstanceDataDone(inst string, spec raidwalk.GMInstanceSpec) bool {
	key := inst
	if key == "unreadable" {
		key = "unplaced"
	}
	if key != "" && raidwalk.NewInstanceMarks(w.outDir).LegDone(key, spec.Leg, spec) {
		return true
	}
	if !w.opts.reuseInstance || inst == "" || inst == "unreadable" || w.env.capLedger == "" {
		return false
	}
	l, err := raidwalk.LoadCapLedger(w.env.capLedger)
	if err != nil {
		return false
	}
	return l.GMInstanceDataDone(w.opts.server, inst, spec.Leg, raidwalk.GMInstanceDataHash(spec))
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
// already-confirmed commands' sends. The ledger's mark goes out only then,
// so an interrupt or a refusal before the last command resends the rest on
// the next firing. A changed command list is never done: the ledger's mark
// carries the hash it was sent with, and the marker carries the list
// itself.
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
	if key != "" && marks.LegDone(key, spec.Leg, spec) {
		w.noteGMInstanceData(inst, spec.Leg, raidwalk.GMInstanceDataHash(spec))
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

// noteGMInstanceData marks the leg's lines sent in the instance-cap ledger,
// so a resumed walk in the same instance does not resend. Without a ledger
// (tests that do not exercise the cap) or without a readable instance id
// the out dir's marker is the memory.
func (w *walk) noteGMInstanceData(inst, leg, hash string) {
	if w.env.capLedger == "" || inst == "" || inst == "unreadable" {
		return
	}
	at := w.env.now()
	if err := w.capUpdate(func(l *raidwalk.CapLedger) {
		l.NoteGMInstanceData(w.opts.server, inst, leg, at, hash)
	}); err != nil {
		w.note("instance cap: the gm-instance-data mark not written: %v", err)
	}
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
// status names fires here, once per instance, when a walk reaches the leg
// mid-run. A firing here holds the run's next pull until the lines are
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
func (w *walk) gmInstanceDataSendPending(ctx context.Context, st tbm.RunStatus) (bool, error) {
	// Idle fast path: no specs, or a state with no leg to name — never
	// touch the toolkit. The instance read below is a real `q inst`.
	if len(w.gmInstanceData) == 0 {
		return false, nil
	}
	if st.State != tbm.RunRunning && st.State != tbm.RunPaused {
		return false, nil
	}
	inst := w.gmInstanceDataInst(ctx)
	for _, spec := range w.gmInstanceDataPending(inst, st) {
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
	return false, nil
}
