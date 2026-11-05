// raid_walk_gminstancedata.go: the `--gm-instance-data` test tool of
// raid-walk (owner-approved, test servers only, off by default). It replaces
// walk 39's hkgmall.sh shell loop: once a raid reached the Hakkar leg, that
// loop sent `.debug instancedata 1 3`, `.debug instancedata 6 3`,
// `.debug instancedata 0 0` as say chat over the slot's rpc, 3 s apart, and a
// glob bug in it cost ~4 h of Hakkar attempts in walk 39.
//
// A walk with `--gm-instance-data "<leg>:<a> <b>[;<a> <b>...]"` (repeatable,
// one leg per value) has the commander send each command as
// `.debug instancedata <a> <b>`, in order, 3 s apart, once per instance, when
// the walk first starts the named leg: the first run-status line that names
// the leg (a placed start inside the leg fires on the start line itself,
// before the first pull).
//
// The lines go out through the walk's own gm path (w.gm, the commander's gm
// RPC allow-listed by raidwalk.AllowedLine): a refused or failed send breaks
// the walk instead of losing the pull gate silently. Every sent command is
// one event line in run.log (`gm-instance-data: <leg>: .debug instancedata
// <a> <b> -> sent`). A marker file per instance and leg in the walk's out
// dir, plus a mark in the instance-cap ledger (the walk's existing state
// file, which outlives the attempt's out dir), remembers the sent commands,
// so a resumed walk in the same instance (--reuse-instance) never refires:
// resending is harmless but noisy.
package main

import (
	"context"
	"fmt"
	"time"

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

// gmInstanceDataInst is the placement's instance id as the markers key it
// ("": no placement, no firing).
func (w *walk) gmInstanceDataInst() string {
	if w.place == nil || w.placeInst == 0 {
		return ""
	}
	return fmt.Sprintf("%.0f", w.placeInst)
}

// gmInstanceDataDone says the leg's lines already went out in this instance:
// the out dir's marker says so, or the ledger does (a --reuse-instance
// restart runs in a fresh out dir, but the ledger outlives the attempt).
func (w *walk) gmInstanceDataDone(inst string, spec raidwalk.GMInstanceSpec) bool {
	if raidwalk.NewInstanceMarks(w.outDir).LegDone(inst, spec.Leg, spec) {
		return true
	}
	if w.env.capLedger == "" {
		return false
	}
	l, err := raidwalk.LoadCapLedger(w.env.capLedger)
	if err != nil {
		return false
	}
	return l.GMInstanceDataDone(w.opts.server, inst, spec.Leg)
}

// gmInstanceDataFire sends one spec's commands in order, gmInstanceDataGap
// apart, and remembers them in the out dir's marker and the ledger.
func (w *walk) gmInstanceDataFire(ctx context.Context, inst string, spec raidwalk.GMInstanceSpec) error {
	for i, cmd := range spec.Cmds {
		line := raidwalk.GMInstanceDataLine(cmd)
		if err := w.gm(ctx, line); err != nil {
			return err
		}
		if err := raidwalk.NewInstanceMarks(w.outDir).Record(inst, spec.Leg, cmd); err != nil {
			return stopf(exitRaidWalkFailed, "gm-instance-data: %s: %v", spec.Leg, err)
		}
		w.noteGMInstanceData(inst, spec.Leg)
		w.logLine(w.env.now(), raidwalk.GMInstanceDataLogLine(spec.Leg, cmd, "sent"))
		if i+1 < len(spec.Cmds) {
			if err := w.env.sleep(ctx, gmInstanceDataGap); err != nil {
				return stopf(exitRaidWalkFailed, "gm-instance-data: %s: interrupted", spec.Leg)
			}
		}
	}
	return nil
}

// noteGMInstanceData marks the leg's lines sent in the instance-cap ledger,
// so a resumed walk in the same instance does not resend. Without a ledger
// (tests that do not exercise the cap) the out dir's marker is the memory.
func (w *walk) noteGMInstanceData(inst, leg string) {
	if w.env.capLedger == "" {
		return
	}
	if err := w.capUpdate(func(l *raidwalk.CapLedger) {
		l.NoteGMInstanceData(w.opts.server, inst, leg)
	}); err != nil {
		w.note("instance cap: the gm-instance-data mark not written: %v", err)
	}
}

// gmInstanceDataStart fires the specs whose leg the run just reached: the
// first status line that names the leg, running or paused (a done or none
// line names no leg the raid stands on). Done legs stay silent: the marker
// (or the ledger) says they fired already in this instance.
func (w *walk) gmInstanceDataStart(ctx context.Context, st tbm.RunStatus) error {
	if len(w.gmInstanceData) == 0 {
		return nil
	}
	if st.State != tbm.RunRunning && st.State != tbm.RunPaused {
		return nil
	}
	inst := w.gmInstanceDataInst()
	if inst == "" {
		return nil
	}
	for _, spec := range w.gmInstanceData {
		if !raidwalk.LegMatchesStatus(spec.Leg, st.Leg) {
			continue
		}
		if w.gmInstanceDataDone(inst, spec) {
			continue
		}
		if err := w.gmInstanceDataFire(ctx, inst, spec); err != nil {
			return err
		}
	}
	return nil
}
