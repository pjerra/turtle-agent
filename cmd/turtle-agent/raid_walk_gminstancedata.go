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
// the cap ledger reads it at entry (the caller's `q inst`), once per walk
// and kept. "unreadable" means the read failed: the walk already WARNed
// once and fires marker-only (the ledger keys on instances, so it is
// skipped). A read failure never fails the walk: ttInst would, so the
// toolkit is asked directly and any failure falls back.
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
	if err != nil || inst == 0 {
		w.gmInstanceDataEntryInst = "unreadable"
		if err != nil {
			w.note("gm-instance-data: WARN: the walk has no placement, and the instance id could not be read (%v): the lines fire once per out dir, not per instance", err)
		} else {
			w.note("gm-instance-data: WARN: the walk has no placement, and the toolkit gave no instance id: the lines fire once per out dir, not per instance")
		}
		return w.gmInstanceDataEntryInst
	}
	w.gmInstanceDataEntryInst = fmt.Sprintf("%.0f", inst)
	return w.gmInstanceDataEntryInst
}

// gmInstanceDataDone says the leg's lines already went out in this instance:
// the out dir's marker says so (keyed by the instance, or by "unplaced"
// when the id could not be read), or the ledger does (a --reuse-instance
// restart runs in a fresh out dir, but the ledger outlives the attempt). A
// changed command list is never done: the ledger's mark carries the hash it
// was sent with, and the marker carries the list itself. An "unreadable"
// instance is marker-only: the ledger keys on instances.
func (w *walk) gmInstanceDataDone(inst string, spec raidwalk.GMInstanceSpec) bool {
	key := inst
	if key == "unreadable" {
		key = "unplaced"
	}
	if key != "" && raidwalk.NewInstanceMarks(w.outDir).LegDone(key, spec.Leg, spec) {
		return true
	}
	if inst == "" || inst == "unreadable" || w.env.capLedger == "" {
		return false
	}
	l, err := raidwalk.LoadCapLedger(w.env.capLedger)
	if err != nil {
		return false
	}
	return l.GMInstanceDataDone(w.opts.server, inst, spec.Leg, raidwalk.GMInstanceDataHash(spec), w.env.now())
}

// gmInstanceDataFire sends one spec's commands in order, gmInstanceDataGap
// apart, and remembers them in the out dir's marker (keyed by the instance,
// or by "unplaced" when the id could not be read) and the ledger only
// after EVERY command of the spec has been sent: an interrupt after the
// first command resends the rest on the next firing. The ledger's mark
// carries the hash of the exact command list sent, so a changed list is
// resent too.
//
// After each line the walk reads the chat/system events for up to
// gmInstanceDataReplyWait and logs the server's reply in the event line
// (`-> <reply>`); with no reply it logs `-> no reply (WARN)`. The walk
// never fails on a reply: the reply is evidence, not a gate.
func (w *walk) gmInstanceDataFire(ctx context.Context, inst string, spec raidwalk.GMInstanceSpec) error {
	key := inst
	if key == "unreadable" {
		key = "unplaced"
	}
	for i, cmd := range spec.Cmds {
		line := raidwalk.GMInstanceDataLine(cmd)
		if err := w.gm(ctx, line); err != nil {
			return err
		}
		reply := w.gmInstanceDataReply(ctx)
		if reply == "" {
			reply = "no reply (WARN)"
		}
		if err := raidwalk.NewInstanceMarks(w.outDir).Record(key, spec.Leg, cmd); err != nil {
			return stopf(exitRaidWalkFailed, "gm-instance-data: %s: %v", spec.Leg, err)
		}
		w.logLine(w.env.now(), raidwalk.GMInstanceDataLogLine(spec.Leg, cmd, reply))
		if i+1 < len(spec.Cmds) {
			if err := w.env.sleep(ctx, gmInstanceDataGap); err != nil {
				return stopf(exitRaidWalkFailed, "gm-instance-data: %s: interrupted", spec.Leg)
			}
		}
	}
	w.noteGMInstanceData(inst, spec.Leg, raidwalk.GMInstanceDataHash(spec))
	return nil
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
// each line. The reader takes one no-wait page per line and the 3 s gap
// before the next line is the window: a reply that lands within it is the
// next line's poll read back. Each line's own poll keeps the walk's event
// cursor moving, so the run's own status lines behind a reply are never
// skipped unlogged.
const gmInstanceDataReplyWait = 2 * time.Second

// gmInstanceDataReply returns the server's reply to the line just sent
// ("": none in 2 s). It polls one no-wait events page at a time until the
// first plain system line or the 2 s bound, sleeping 200 ms between polls;
// the 3 s gap before the next line is the rest of the window. It never fails
// the walk: a missing reply is a WARN in the event line, not an error.
// Every TBM:RUN line on a page goes through the walk's own pipeline the way
// pollOnce does (logged, fed to the tracker and the boss rule, the status
// adopted, pull windows armed, the frame read skipped: the page is the
// reply's, not the poll's): a run line behind the reply is not lost
// (page.Next already advanced past it).
func (w *walk) gmInstanceDataReply(ctx context.Context) string {
	deadline := w.env.now().Add(gmInstanceDataReplyWait)
	for {
		var page events.Page
		params := map[string]any{"since": w.eventsAt, "kinds": []string{"system", "addon"}, "wait_ms": 0}
		if err := w.agent.Call(ctx, "events", params, &page, 15*time.Second); err != nil {
			return ""
		}
		w.eventsAt = page.Next
		var reply string
		for _, e := range page.Events {
			if line, ok := tbm.Line(e); ok {
				now := w.env.now()
				w.logLine(now, line)
				if st, ok := raidwalk.ParseRun(line); ok && st.Plan == w.plan.Name && w.tracker != nil {
					w.tracker.Feed(now, st)
					w.observeBossStatus(st, now)
					w.pullStarted(now, st)
					w.lastStatus, w.haveStatus, w.statusSeq = st, true, w.statusSeq+1
					w.recordRunLine(st, now)
					w.noteLabel(st)
					if st.Note != "" {
						w.newNotes = append(w.newNotes, st.Note)
					}
				}
				continue
			}
			if reply != "" || e.Kind != events.KindSystem || e.Text == "" {
				continue
			}
			reply = raidwalk.CleanLine(e.Text)
		}
		if reply != "" {
			return reply
		}
		if !w.env.now().Before(deadline) {
			return ""
		}
		if err := w.env.sleep(ctx, 200*time.Millisecond); err != nil {
			return ""
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
// pull gate's lines land before the next pull. Pause and resume go through
// the same path the walk's other run controls use; a refused pause still
// fires the lines (late is better than never), and a refused resume is a
// failed walk (the raid would stand paused: that must be loud, like a
// failed send). At most one spec fires per poll: the next poll fires the
// next.
func (w *walk) gmInstanceDataSendPending(ctx context.Context, st tbm.RunStatus) error {
	inst := w.gmInstanceDataInst(ctx)
	for _, spec := range w.gmInstanceDataPending(inst, st) {
		paused := false
		if st.State == tbm.RunRunning {
			if _, err := w.commander(ctx, commanderReq{Op: "run", Verb: "pause", WaitMS: 10000}); err == nil {
				paused = true
			} else {
				w.note("gm-instance-data: WARN: the run pause before %s was refused (%v): the lines go out unheld", spec.Leg, err)
			}
		}
		if err := w.gmInstanceDataFire(ctx, inst, spec); err != nil {
			return err
		}
		if paused {
			if _, err := w.commander(ctx, commanderReq{Op: "run", Verb: "resume", WaitMS: 10000}); err != nil {
				return stopf(exitRaidWalkFailed, "gm-instance-data: %s: the run resume after the lines failed: %v", spec.Leg, err)
			}
		}
		return nil
	}
	return nil
}
