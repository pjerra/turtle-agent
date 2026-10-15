package dungeon

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/pjerra/turtle-agent/internal/packet"
	"github.com/pjerra/turtle-agent/internal/tbm"
)

// Area triggers and unit waits in remote mode (AreaTrigger, UnitWait), and the leg-wide corpse-loot mute (Leg.CompanionsLoot).
//
// An area_trigger step is taken by the lead bot first when By allows it: the bot walks to the step's Stand inside the zone, and the module
// (TortoiseBots AiPlayerbot.BotAreaTriggers, #1002) runs the trigger's script for a bot that enters a listed zone. When Expect does not
// hold within TriggerBotWait the agent's own character does what a client does: `.appear <lead bot>` when it is far (a GM assist, counted in
// run_gm_assists), a walk to Stand, CMSG_AREATRIGGER, and a walk back to where it came from. One attempt by each; then the step fails with
// the reason. It never loops.

// AreaTriggerGame is the optional part of a RemoteGame that lets the agent's own character send an area trigger (session.AreaTrigger).
type AreaTriggerGame interface {
	AreaTrigger(ctx context.Context, id uint32) error
}

// TriggerReport is what a run did for one area_trigger step.
type TriggerReport struct {
	Leg     int    `json:"leg"`
	Trigger uint32 `json:"trigger"`
	// By is who took it: "already" (Expect held when the step began), "lead bot" or "agent".
	By      string  `json:"by"`
	Result  string  `json:"result"` // "done" or "failed"
	Seconds float64 `json:"seconds"`
	Error   string  `json:"error,omitempty"`
}

func (r *RemoteReport) cloneTriggers() []TriggerReport {
	return append([]TriggerReport(nil), r.AreaTriggers...)
}

// Waits and limits of the step.
const (
	triggerWalkMax   = 60 * time.Second // ceiling of the agent's walk to the zone and of the walk back
	triggerBackMax   = 45 * time.Second
	triggerLeadSlack = 2.0 // yards: a lead bot this much inside the radius counts as in the zone
)

// UnitHolds makes the remote world a UnitWorld: the wait's state, read from the bots' feed and the agent's own object store.
func (w remoteWorld) UnitHolds(u *UnitWait) bool { return w.d.unitHolds(u) }

// unitSights are the creatures the run can see: the hostiles of the feed (alive or dead), then what the agent's object store lists that the
// feed does not (the friendly ones, a corpse the feed has dropped).
func (d *RemoteDriver) unitSights() []unitSight {
	var out []unitSight
	have := map[packet.GUID]bool{}
	for _, h := range d.fr.Hostiles {
		if h.Entry == 0 {
			continue
		}
		have[h.GUID] = true
		out = append(out, unitSight{guid: h.GUID, entry: h.Entry, at: hostilePoint(h), dead: h.HPPct <= 0, hostile: true})
	}
	if us, ok := d.g.(UnitSource); ok {
		out = append(out, storeSights(us.Units(), have)...)
	}
	return out
}

// unitHolds says whether the unit wait's state is true now. A death the run saw (the kill ledger) counts for "dead" after the feed has
// dropped the corpse.
func (d *RemoteDriver) unitHolds(u *UnitWait) bool {
	known := false
	for _, e := range u.Entries {
		if len(d.killedAt[e]) > 0 {
			known = true
		}
	}
	return u.holds(d.unitSights(), known || d.ledgerDead(u.Entries))
}

// waitUnit waits up to limit for the unit wait to hold, looking every poll. It reports whether it held.
func (d *RemoteDriver) waitUnit(ctx context.Context, u *UnitWait, limit time.Duration) (bool, error) {
	until := d.clk.Now().Add(limit)
	for {
		d.look()
		if d.unitHolds(u) {
			return true, nil
		}
		if !d.clk.Now().Before(until) {
			return false, nil
		}
		if err := d.tick(ctx, d.cfg.Poll); err != nil {
			return false, err
		}
	}
}

// doAreaTrigger is the area_trigger step.
func (d *RemoteDriver) doAreaTrigger(ctx context.Context, a Action) error {
	t := a.Trigger
	began := d.clk.Now()
	rep := TriggerReport{Leg: a.Leg, Trigger: t.ID}
	by, err := d.takeTrigger(ctx, a)
	rep.By, rep.Result, rep.Seconds = by, "done", d.clk.Now().Sub(began).Seconds()
	if err != nil {
		rep.Result, rep.Error = "failed", err.Error()
	}
	d.remote(func(r *RemoteReport) { r.AreaTriggers = append(r.AreaTriggers, rep) })
	d.tr.Event("area_trigger", map[string]any{"leg": a.Leg, "trigger": t.ID, "by": by, "ok": err == nil, "seconds": rep.Seconds})
	return err
}

// takeTrigger does the step and says who took it.
func (d *RemoteDriver) takeTrigger(ctx context.Context, a Action) (string, error) {
	t, exp := a.Trigger, a.Trigger.ExpectUnit
	d.look()
	if d.unitHolds(exp) {
		d.rep.note(fmt.Sprintf("%s: %s already: the trigger is not sent", a.Label, exp.describe()))
		return "already", nil
	}
	var botErr error
	if t.By != TriggerAgent {
		wait := TriggerBotWait
		if t.By == TriggerBot {
			wait = time.Duration(t.TimeoutS) * time.Second
		}
		ok, err := d.triggerByLead(ctx, a, wait)
		switch {
		case ok:
			return "lead bot", nil
		case err != nil && (runEnded(ctx, err) || t.By == TriggerBot):
			return "lead bot", fmt.Errorf("%s: %w", a.Label, err)
		case err != nil:
			botErr = err
			d.rep.note(fmt.Sprintf("%s: the lead bot could not walk into the zone (%v); the agent takes the trigger", a.Label, err))
		case t.By == TriggerBot:
			return "lead bot", fmt.Errorf("%s: the lead bot stood in the zone %d s and %s did not hold: the module does not take area trigger %d for a bot (TortoiseBots AiPlayerbot.BotAreaTriggers must list it, #1002); use by \"auto\" to let the agent take it", a.Label, t.TimeoutS, exp.describe(), t.ID)
		default:
			d.rep.note(fmt.Sprintf("%s: %s did not hold %.0f s after the lead bot stood in the zone; the agent takes the trigger", a.Label, exp.describe(), wait.Seconds()))
		}
	}
	err := d.triggerByAgent(ctx, a)
	if err != nil {
		if botErr != nil {
			err = fmt.Errorf("%w (the lead bot's walk failed too: %v)", err, botErr)
		}
		return "agent", fmt.Errorf("%s: %w", a.Label, err)
	}
	return "agent", nil
}

// triggerByLead walks the lead bot (the party follows) into the zone and waits up to wait for Expect. It reports whether it held; the error
// is the walk's.
func (d *RemoteDriver) triggerByLead(ctx context.Context, a Action, wait time.Duration) (bool, error) {
	t := a.Trigger
	if lead, ok := d.leadPos(); !ok || dist3(lead, t.Center) > float64(t.Radius)-triggerLeadSlack {
		if err := d.tracedWalkAs(ctx, t.Stand, false); err != nil {
			return false, err
		}
		d.rep.note(fmt.Sprintf("%s: the lead bot walked to %.1f %.1f %.1f in the zone of trigger %d", a.Label, t.Stand.X, t.Stand.Y, t.Stand.Z, t.ID))
	}
	return d.waitUnit(ctx, t.ExpectUnit, wait)
}

// triggerByAgent is the agent's own character taking the trigger: near the party, into the zone, the packet, back.
func (d *RemoteDriver) triggerByAgent(ctx context.Context, a Action) error {
	t := a.Trigger
	f, ok := d.g.(Follower)
	tg, ok2 := d.g.(AreaTriggerGame)
	if !ok || !ok2 {
		return errors.New("the agent has no hands for an area trigger in this game")
	}
	if err := d.fallbackGate(ctx, "take an area trigger"); err != nil {
		return err
	}
	// Near the party first: `.appear <lead bot>` for a GM (a GM assist, and only when the setup made the agent invisible), else a walk.
	if _, err := d.keepNearOpt(ctx, true); err != nil {
		return err
	}
	dist, known := d.agentDistance(f)
	if !known {
		return errors.New("the agent's own character is not in the world")
	}
	if dist > keepNearFar {
		return fmt.Errorf("the agent's own character is %.0f yd from the lead bot (more than %d) and could not be brought up: it needs a GM account (and \"gm visible off\" in the setup) to .appear, or to be on the map to walk", dist, int(keepNearFar))
	}
	home, _, _ := f.AgentPlace()
	if ok, err := d.agentWalkOpt(ctx, f, t.Stand, triggerWalkMax, true); err != nil {
		return err
	} else if !ok {
		return fmt.Errorf("the agent could not walk to %.1f %.1f %.1f in the zone of trigger %d", t.Stand.X, t.Stand.Y, t.Stand.Z, t.ID)
	}
	at, _, _ := f.AgentPlace()
	if d := dist3(at, t.Center); d > float64(t.Radius) {
		return fmt.Errorf("the agent stands %.1f yd from the centre of trigger %d after its walk (the radius is %g): not in the zone", d, t.ID, t.Radius)
	}
	sendErr := tg.AreaTrigger(ctx, t.ID)
	d.remote(func(r *RemoteReport) { r.AgentTriggers++ })
	if sendErr != nil {
		d.triggerHome(ctx, f, home)
		return fmt.Errorf("sending area trigger %d: %w", t.ID, sendErr)
	}
	d.rep.note(fmt.Sprintf("%s: the agent sent area trigger %d from %.1f %.1f %.1f", a.Label, t.ID, at.X, at.Y, at.Z))
	held, err := d.waitUnit(ctx, t.ExpectUnit, time.Duration(t.TimeoutS)*time.Second)
	// Out of the zone either way, a fight or not: the agent is no part of the party's play.
	d.triggerHome(ctx, f, home)
	if err != nil {
		return err
	}
	if !held {
		return fmt.Errorf("the agent sent area trigger %d from inside its zone and %s did not hold within %d s: the trigger's script did not run, or it changed something else", t.ID, t.ExpectUnit.describe(), t.TimeoutS)
	}
	return nil
}

// triggerHome walks the agent back to where it stood before the step. A failure is noted: the step's result does not hang on it.
func (d *RemoteDriver) triggerHome(ctx context.Context, f Follower, home Point) {
	if ctx.Err() != nil {
		return
	}
	if ok, err := d.agentWalkOpt(ctx, f, home, triggerBackMax, false); !ok && err == nil {
		d.rep.note(fmt.Sprintf("area trigger: the agent did not get back to %.1f %.1f %.1f", home.X, home.Y, home.Z))
	}
}

// ---- Leg.CompanionsLoot: corpse looting off for the whole leg ----

// legLootOff turns the party's looting off for a leg whose plan says companions_loot false, and remembers whom, for legLootOn.
func (d *RemoteDriver) legLootOff(ctx context.Context, leg int) {
	l := d.plan.Legs[leg]
	if l.CompanionsLoot == nil || *l.CompanionsLoot {
		return
	}
	og, ok := d.orderGame()
	if !ok {
		d.rep.note(fmt.Sprintf("leg %d: companions_loot is false but the game cannot send behavior orders: the bots keep looting", leg))
		return
	}
	var muted, failed []string
	for _, m := range d.fr.Party {
		if r, err := og.Behavior(ctx, m.Name, "loot", false); err != nil || r.Kind != tbm.KindAck {
			failed = append(failed, m.Name+": "+behaviorWhy(r, err))
			continue
		}
		muted = append(muted, m.Name)
	}
	d.legMuted = muted
	d.rep.note(fmt.Sprintf("leg %d: corpse and object looting is off for %v for the whole leg (companions_loot false)", leg, muted))
	if len(failed) > 0 {
		d.rep.note(fmt.Sprintf("leg %d: could not turn looting off for %s", leg, strings.Join(failed, "; ")))
	}
}

// legLootOn puts the looting of the bots legLootOff muted back on; it runs when the leg ends, with a context of its own (a stopped run has
// none left).
func (d *RemoteDriver) legLootOn() {
	if len(d.legMuted) == 0 {
		return
	}
	muted := d.legMuted
	d.legMuted = nil
	og, ok := d.orderGame()
	if !ok {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	for _, n := range muted {
		if r, err := og.Behavior(ctx, n, "loot", true); err != nil || r.Kind != tbm.KindAck {
			d.rep.note(fmt.Sprintf("could not turn %s's looting back on after the leg: %s", n, behaviorWhy(r, err)))
		}
	}
	d.rep.note(fmt.Sprintf("looting turned back on for %v after the leg", muted))
}
