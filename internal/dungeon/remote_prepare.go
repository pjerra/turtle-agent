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
	if len(d.opt.GiveItems) > 0 {
		d.opt.Prepare.GiveItems = slices.Clone(d.opt.GiveItems)
	}
	if !d.opt.Prepare.Active() {
		return nil
	}
	pg, ok := d.g.(Preparer)
	if !ok {
		return errors.New("prepare: this game cannot log bots in, teleport or send GM lines")
	}
	d.rep.setCurrent("prepare")
	env := &prepEnv{g: pg, plan: d.plan, spec: d.opt.Prepare, bots: append([]string{d.opt.Lead}, d.opt.Bots...), remote: true, memory: d.opt.HireMemory,
		now: d.clk.Now, sleep: d.clk.Sleep, note: d.rep.note, shortcut: d.prepShortcut,
		progress: func(s string) { d.rep.setCurrent(s); d.wd.progress(s) }, command: d.g.Command, settle: d.opt.BindSettle,
		tank: d.opt.Tank, healer: d.opt.Healer, carrier: d.opt.Carrier}
	if d.opt.Prepare.Fresh && !d.opt.IgnoreEntryLimit {
		env.entryOK = d.freshEntryOK
	}
	rep, err := runPrepare(ctx, env)
	d.rep.update(func(r *Report) { r.Prepare = rep.clone(); r.Ready = rep.Ready.clone() })
	if err != nil {
		if env.bound {
			// The step bound the party and then failed: the binding is not this run's yet, so nothing else would end it.
			uctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), d.opt.UnbindWait+5*time.Second)
			defer cancel()
			if _, uerr := d.g.Command(uctx, "commander unbind"); uerr != nil {
				d.rep.note("prepare: commander unbind after a failed step: " + uerr.Error())
			}
		}
		return err
	}
	if env.hired != nil {
		d.adoptHired(env.hired)
	}
	d.wd.progress("prepare done")
	return nil
}

// adoptHired takes the lead bot, the other bots, the tank and the healer from the hire step: the run was started without names.
func (d *RemoteDriver) adoptHired(h *HiredParty) {
	d.opt.Lead, d.opt.Bots, d.opt.Tank, d.opt.Healer = h.Lead, slices.Clone(h.Bots), h.Tank, h.Healer
	if h.CC != "" && d.opt.CCBot == "" {
		d.opt.CCBot = h.CC
	}
	d.remote(func(r *RemoteReport) { r.LeadBot, r.Bots, r.Tank = h.Lead, slices.Clone(h.Bots), h.Tank })
	d.rep.note(fmt.Sprintf("hired party: lead bot %s%s, tank %s, healer %s", h.Lead, botsSuffix(h.Bots), h.Tank, h.Healer))
	d.tr.Event("hired_party", map[string]any{"lead_bot": h.Lead, "bots": slices.Clone(h.Bots), "tank": h.Tank, "healer": h.Healer})
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
