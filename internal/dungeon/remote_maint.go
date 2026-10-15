package dungeon

// Automatic `.bot maintenance` during a remote run. The preflight maintains every bot once at the start; this keeps the bots' reagents,
// ammo and drinks topped up on a long run (README "Automatic maintenance").
//
//   - Every maintenance_every_legs legs (0: off): at the start of legs N, 2N, ... (leg 0 is skipped: the preflight just did it).
//     When the party is in combat, or a bot is dead, at that moment, the order waits for the next calm moment of the same leg (the end
//     of a rest gate, which runs before every pull); it is given up when the leg ends.
//   - After every recovered wipe, once the party is calm and alive: once per recovery, apart from the per-leg slot.
//
// Never in combat, and never when the kit is off (maintenance is off on the server, or the game cannot send bot lines). It reuses
// maintain (preflight.go); the refills at rest gates (remote_rest.go) stay as they are.

import (
	"context"
	"fmt"
	"strings"
)

// maintState is what is due and what has been done.
type maintState struct {
	legWant bool // the per-leg slot of leg is due and has not run yet
	leg     int
	wipe    bool // a recovered wipe is waiting for its maintenance
}

// maintReasons says what is due, for the notes and the trace.
func (m maintState) reasons() []string {
	var out []string
	if m.legWant {
		out = append(out, "leg")
	}
	if m.wipe {
		out = append(out, "wipe")
	}
	return out
}

func (m maintState) pending() bool { return m.legWant || m.wipe }

// maintSkip counts a skipped maintenance by reason.
func (d *RemoteDriver) maintSkip(reason string) {
	d.remote(func(r *RemoteReport) {
		if r.MaintenanceSkips == nil {
			r.MaintenanceSkips = map[string]int{}
		}
		r.MaintenanceSkips[reason]++
	})
}

// maintLegStart is called when leg i starts: it makes the leg's slot due when the tunable says so, and tries it.
func (d *RemoteDriver) maintLegStart(ctx context.Context, i int) error {
	n := d.cfg.MaintenanceEveryLegs
	if n <= 0 || i <= 0 || i%n != 0 {
		return nil
	}
	d.maint.legWant, d.maint.leg = true, i
	return d.maintainIfDue(ctx)
}

// maintWipeRecovered is called after a wipe was recovered: the party's maintenance is due once it is calm.
func (d *RemoteDriver) maintWipeRecovered(ctx context.Context) error {
	if d.cfg.MaintenanceEveryLegs <= 0 {
		return nil // the tunable at 0 turns all of it off
	}
	d.maint.wipe = true
	return d.maintainIfDue(ctx)
}

// maintLegEnd is called when the leg ends: a per-leg slot that never found a calm moment is given up.
func (d *RemoteDriver) maintLegEnd() {
	if d.maint.legWant {
		d.maint.legWant = false
		d.maintSkip("combat")
		d.rep.note(fmt.Sprintf("leg %d: maintenance given up, the party was never calm and alive at a moment for it in this leg", d.maint.leg))
		d.tr.Event("maintenance", map[string]any{"leg": d.maint.leg, "reasons": []string{"leg"}, "skipped": "combat"})
	}
}

// maintainIfDue sends `.bot maintenance` to every bot when something is due and the party is calm. It returns only the context's error.
func (d *RemoteDriver) maintainIfDue(ctx context.Context) error {
	if !d.maint.pending() {
		return nil
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	reasons := d.maint.reasons()
	d.kit.init()
	if d.kit.off {
		for range reasons {
			d.maintSkip("disabled")
		}
		d.maint = maintState{}
		return nil
	}
	bl, ok := d.g.(BotLiner)
	if !ok {
		for range reasons {
			d.maintSkip("no bot lines")
		}
		d.maint = maintState{}
		return nil
	}
	d.look()
	if d.stale() || !d.maintCalm() {
		return nil // not now: the next gate tries again
	}
	var names []string
	for _, m := range d.fr.Party {
		names = append(names, m.Name)
	}
	verdicts := map[string]string{}
	for i, name := range names {
		if i > 0 {
			d.look()
			if !d.maintCalm() { // a fight began while the bots were being answered
				d.maintSkip("combat")
				d.tr.Event("maintenance", map[string]any{"leg": max(d.curLeg, 0), "reasons": reasons, "skipped": "combat", "verdicts": verdicts})
				return nil
			}
		}
		v := d.maintain(ctx, bl, name)
		verdicts[name] = v
		if err := ctx.Err(); err != nil {
			return err
		}
		if v == "disabled" {
			d.kit.off = true
			d.rep.note("maintenance: off on the server (AiPlayerbot.OwnedBotMaintenanceEnabled = 0); no more of it")
			for range reasons {
				d.maintSkip("disabled")
			}
			d.tr.Event("maintenance", map[string]any{"leg": max(d.curLeg, 0), "reasons": reasons, "skipped": "disabled", "verdicts": verdicts})
			d.maint = maintState{}
			return nil
		}
		delete(d.kit.readLeg, strings.ToLower(name)) // the bags changed: the next refill check reads them again
	}
	d.remote(func(r *RemoteReport) { r.MaintenanceRuns++ })
	d.tr.Event("maintenance", map[string]any{"leg": max(d.curLeg, 0), "reasons": reasons, "verdicts": verdicts})
	d.rep.note(fmt.Sprintf("leg %d: maintenance (%s): %s", max(d.curLeg, 0), strings.Join(reasons, "+"), verdictText(names, verdicts)))
	d.maint = maintState{}
	return nil
}

// maintCalm says whether the newest frame has nobody in combat and everybody alive.
func (d *RemoteDriver) maintCalm() bool {
	if len(d.fr.Party) == 0 || d.fr.PartyInCombat() || len(d.fr.EngagedHostiles()) > 0 {
		return false
	}
	for _, m := range d.fr.Party {
		if m.Dead {
			return false
		}
	}
	return true
}

func verdictText(names []string, v map[string]string) string {
	var parts []string
	for _, n := range names {
		if x, ok := v[n]; ok {
			parts = append(parts, n+" "+x)
		}
	}
	return strings.Join(parts, ", ")
}
