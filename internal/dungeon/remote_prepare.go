package dungeon

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"
)

// prepare runs the run's prepare block, if it has one, before the bind (prepare.go). The report gets the steps with their seconds; the GM
// lines the steps sent are setup shortcuts.
func (d *RemoteDriver) prepare(ctx context.Context) error {
	if !d.opt.Prepare.Active() {
		return nil
	}
	pg, ok := d.g.(Preparer)
	if !ok {
		return errors.New("prepare: this game cannot log bots in, teleport or send GM lines")
	}
	d.rep.setCurrent("prepare")
	env := &prepEnv{g: pg, plan: d.plan, spec: d.opt.Prepare, bots: append([]string{d.opt.Lead}, d.opt.Bots...), remote: true,
		now: d.clk.Now, sleep: d.clk.Sleep, note: d.rep.note, shortcut: d.prepShortcut,
		progress: func(s string) { d.rep.setCurrent(s); d.wd.progress(s) }}
	if d.opt.Prepare.Fresh && !d.opt.IgnoreEntryLimit {
		env.entryOK = d.freshEntryOK
	}
	rep, err := runPrepare(ctx, env)
	d.rep.update(func(r *Report) { r.Prepare = rep.clone() })
	if err != nil {
		return err
	}
	d.wd.progress("prepare done")
	return nil
}

// freshEntryOK refuses a fresh run when the account's ledger already holds the hour's limit of new instances: it is the same check the
// enter phase makes, made before anything is teleported or unbound.
func (d *RemoteDriver) freshEntryOK() error {
	l, ok := d.g.(EntryLedger)
	if !ok {
		d.rep.note("prepare fresh: warning: entries are not tracked in this build, the account's limit of new instances per hour is not checked")
		return nil
	}
	recent := l.Recent(d.clk.Now())
	if len(recent) >= MaxInstancesPerHour {
		return fmt.Errorf("the account entered %d new instances in the last hour (limit %d); the oldest was at %s, so a new entry is free after %s (a mangosd restart clears the list; ignore_entry_limit tries anyway)",
			len(recent), MaxInstancesPerHour, recent[0].Local().Format("15:04:05"), recent[0].Add(time.Hour).Local().Format("15:04:05"))
	}
	return nil
}

// prepShortcut books a GM line of the prepare step: a setup shortcut of the run (once per text), a "gm_shortcut" event of kind "setup" in
// the trace, and, for a `gm visible` line, the run's setup list (keep-near reads it).
func (d *RemoteDriver) prepShortcut(line string) {
	d.rep.update(func(r *Report) {
		if !slices.Contains(r.SetupShortcuts, line) {
			r.SetupShortcuts = append(r.SetupShortcuts, line)
		}
	})
	if strings.HasPrefix(line, "gm visible ") {
		d.cfg.Setup = append(slices.Clone(d.cfg.Setup), line)
	}
	d.tr.Event("gm_shortcut", map[string]any{"kind": "setup", "detail": line})
}
