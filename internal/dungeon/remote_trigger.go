package dungeon

import (
	"context"
	"errors"
	"fmt"
	"slices"
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
	triggerBackMax   = 120 * time.Second
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
		out = append(out, unitSight{guid: h.GUID, entry: h.Entry, at: hostilePoint(h), dead: h.HPPct <= 0, hostile: h.Hostile() && !h.Bystander()})
	}
	if us, ok := d.g.(UnitSource); ok {
		out = append(out, storeSights(us.Units(), have)...)
	}
	return out
}

// unitHolds says whether the unit wait's state is true now. A death the run saw (the kill ledger, the places of won pulls) counts for "dead"
// after the feed has dropped the corpse; with `near`, only a death at a place within the wait's radius of it does, so that an earlier kill of
// the same entry elsewhere in the dungeon proves nothing.
func (d *RemoteDriver) unitHolds(u *UnitWait) bool {
	return u.holds(d.unitSights(), d.killSeen(u))
}

// killSeen says whether the run has seen a unit of the wait's entries die, within the wait's radius of Near when it has one.
func (d *RemoteDriver) killSeen(u *UnitWait) bool {
	near := func(p Point) bool { return u.Near == nil || dist3(p, *u.Near) <= float64(u.radius()) }
	for _, e := range u.Entries {
		for _, p := range d.killedAt[e] {
			if near(p) {
				return true
			}
		}
	}
	for _, m := range d.ledger {
		if m.dead && slices.Contains(u.Entries, m.last.Entry) && near(hostilePoint(m.last)) {
			return true
		}
	}
	return false
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

// agentHome is where the agent's own character stood before an area trigger brought it to the zone: its safe point. The step does not
// walk back (the boss it woke is fought by the boss step); agentGoHome does, when the fight is over or before the next walk.
type agentHome struct {
	at      Point
	known   bool     // on the plan's map: a place the agent can walk back to
	entries []uint32 // the entries the trigger's expect names: when one is dead, the boss is down and a failed return is no failure
}

// triggerByAgent is the agent's own character taking the trigger: near the party, into the zone, the packet. It ends as soon as `expect`
// holds, and the boss step owns the fight the trigger starts (under its own timeout, with kill credit); the agent stays in the zone, a GM
// that is invisible, until agentGoHome sends it back to the point it stood at before `.appear`. Only a GM that the setup made invisible
// (`gm visible off`) does it: the boss that the trigger wakes stands 9 yd from the stand point and takes a visible character for a target.
func (d *RemoteDriver) triggerByAgent(ctx context.Context, a Action) error {
	t := a.Trigger
	f, ok := d.g.(Follower)
	tg, ok2 := d.g.(AreaTriggerGame)
	if !ok || !ok2 {
		return errors.New("the agent has no hands for an area trigger in this game")
	}
	if !d.agentGM(ctx, f) {
		return errors.New("the agent's character may take an area trigger only as a GM (it is no GM, or its level is unknown): the boss the trigger wakes would see and attack it; use by \"bot\" with TortoiseBots #1002, or run with a GM account")
	}
	if !d.setupHas("gm visible off") {
		return errors.New("the agent's character may take an area trigger only while invisible: the run's setup does not list \"gm visible off\" (pass it in dungeon_run setup after running it)")
	}
	if err := d.fallbackGate(ctx, "take an area trigger"); err != nil {
		return err
	}
	// The safe point is where the agent stands just before this trigger's `.appear` (keepNearOpt) puts it next to the lead bot.
	var pre *agentHome
	if at, m, here := f.AgentPlace(); here {
		pre = &agentHome{at: at, known: m == d.plan.Map}
	}
	if _, err := d.keepNearOpt(ctx, true); err != nil {
		return err
	}
	if d.home == nil && pre != nil {
		pre.entries = t.ExpectUnit.Entries
		d.home = pre
	}
	dist, known := d.agentDistance(f)
	if !known {
		return errors.New("the agent's own character is not in the world")
	}
	if dist > keepNearFar {
		return fmt.Errorf("the agent's own character is %.0f yd from the lead bot (more than %d) and could not be brought up", dist, int(keepNearFar))
	}
	if ok, err := d.agentWalk(ctx, f, t.Stand, triggerWalkMax); err != nil {
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
		return fmt.Errorf("sending area trigger %d: %w", t.ID, sendErr)
	}
	d.rep.note(fmt.Sprintf("%s: the agent sent area trigger %d from %.1f %.1f %.1f", a.Label, t.ID, at.X, at.Y, at.Z))
	held, err := d.waitUnit(ctx, t.ExpectUnit, time.Duration(t.TimeoutS)*time.Second)
	if err != nil {
		return err
	}
	if !held {
		// Nothing was woken: no fight to wait for, so the agent goes home now (best effort; the step fails with the reason below).
		_ = d.agentGoHome(ctx, false)
		return fmt.Errorf("the agent sent area trigger %d from inside its zone and %s did not hold within %d s: the trigger's script did not run, or it changed something else", t.ID, t.ExpectUnit.describe(), t.TimeoutS)
	}
	return nil
}

// triggerFightWait is how long the agent waits for a fight to end before it walks home.
const triggerFightWait = 5 * time.Minute

// triggerReturnYd is how close to its safe point the agent must be for the return to count.
const triggerReturnYd = 6.0

// agentGoHome sends the agent back to the safe point an agent-taken area trigger recorded. It does nothing when there is no safe point.
// afterKill says the boss step has killed the boss (the same is true when the trigger's unit is dead): then a return that fails or is
// blocked, or a fight that outlasts the wait, is a note and a GM `.go xyz` back to the safe point (a GM assist during the run), never an
// error: a dead boss does not fail a run. Before the boss is dead a failed return is the error of the step it ran for.
func (d *RemoteDriver) agentGoHome(ctx context.Context, afterKill bool) error {
	h := d.home
	if h == nil {
		return nil
	}
	f, ok := d.g.(Follower)
	if !ok {
		d.home = nil
		return nil
	}
	if !h.known {
		d.rep.note("area trigger: the agent's safe point is not on the dungeon's map: it stays where it is")
		d.home = nil
		return nil
	}
	err := d.walkHome(ctx, f, h)
	if err == nil {
		d.home = nil
		d.rep.note(fmt.Sprintf("area trigger: the agent is back at its safe point %.1f %.1f %.1f", h.at.X, h.at.Y, h.at.Z))
		return nil
	}
	if runEnded(ctx, err) {
		return err
	}
	if afterKill || d.unitHolds(&UnitWait{Entries: h.entries, State: UnitDead}) {
		d.home = nil
		d.rep.note(fmt.Sprintf("area trigger: the boss is dead and the agent could not walk back (%v)", err))
		d.teleHome(ctx, f, h)
		return nil
	}
	return err
}

// teleHome puts the agent back at its safe point with `.go xyz` (a GM assist during the run, counted in run_gm_assists); a failure is noted.
func (d *RemoteDriver) teleHome(ctx context.Context, f Follower, h *agentHome) {
	sh, ok := f.(StartHands)
	if !ok {
		d.rep.note("area trigger: this game cannot teleport the agent: it stays where it is")
		return
	}
	if err := sh.GoXYZ(ctx, d.plan.Map, h.at); err != nil {
		d.rep.note(fmt.Sprintf("area trigger: .go xyz back to the safe point failed: %v", err))
		return
	}
	cmd := fmt.Sprintf(".go xyz %.2f %.2f %.2f %d", h.at.X, h.at.Y, h.at.Z, d.plan.Map)
	d.rep.update(func(r *Report) { r.SetupShortcuts = append(r.SetupShortcuts, cmd+" (GM assist during the run)") })
	d.remote(func(r *RemoteReport) { r.RunGMAssists++ })
	d.rep.note(fmt.Sprintf("area trigger: %s: the agent is back at its safe point (a GM assist during the run)", cmd))
}

// walkHome walks the agent to the safe point once the party is out of combat (up to triggerFightWait), never through a fight, and checks
// it got there.
func (d *RemoteDriver) walkHome(ctx context.Context, f Follower, h *agentHome) error {
	until := d.clk.Now().Add(triggerFightWait)
	for {
		if err := d.waitOutOfCombat(ctx, until.Sub(d.clk.Now())); err != nil {
			return fmt.Errorf("the agent could not walk back to its safe point %.1f %.1f %.1f: %w", h.at.X, h.at.Y, h.at.Z, err)
		}
		ok, err := d.agentWalk(ctx, f, h.at, triggerBackMax)
		if err != nil {
			return err
		}
		at, _, here := f.AgentPlace()
		if here && dist3(at, h.at) <= triggerReturnYd {
			return nil
		}
		if d.inCombat() && d.clk.Now().Before(until) {
			continue // a fight began on the way and stopped the walk: wait it out and go on
		}
		why := "the walk did not finish"
		if ok {
			why = "the walk ended short of it"
		}
		return fmt.Errorf("the agent did not get back to its safe point %.1f %.1f %.1f (%s)", h.at.X, h.at.Y, h.at.Z, why)
	}
}

// waitOutOfCombat waits until the party is out of combat, for at most bound. A fight in the feed is fought with the driver's orders (the
// tank first, kill_first, CC: fightEngaged), a party that is in combat with nothing to order at is waited out.
func (d *RemoteDriver) waitOutOfCombat(ctx context.Context, bound time.Duration) error {
	until := d.clk.Now().Add(bound)
	d.look()
	for d.inCombat() {
		if !d.clk.Now().Before(until) {
			return fmt.Errorf("the party was still in combat after %.0f s: the agent does not walk through a fight", bound.Seconds())
		}
		if len(d.fr.EngagedHostiles()) > 0 {
			if err := d.fightEngaged(ctx); err != nil {
				return err
			}
			continue
		}
		if err := d.tick(ctx, d.cfg.Poll); err != nil {
			return err
		}
	}
	return nil
}

// ---- Leg.CompanionsLoot: corpse looting off for the whole leg ----

// legLootOff turns the party's looting off for a leg whose plan says companions_loot false, and remembers whom, for legLootOn. A leg that
// needs the looting off does not start without it: a game that cannot send behavior orders, or a bot that refuses, fails the leg before it
// moves, naming the bots, and the bots already muted are turned back on.
func (d *RemoteDriver) legLootOff(ctx context.Context, leg int) error {
	l := d.plan.Legs[leg]
	if l.CompanionsLoot == nil || *l.CompanionsLoot {
		return nil
	}
	og, ok := d.orderGame()
	if !ok {
		return fmt.Errorf("leg %d (%s): companions_loot is false but this game cannot send behavior orders: the bots would loot the leg's chests (the graveyard's); the leg is not started", leg, l.Name)
	}
	if len(d.fr.Party) == 0 {
		return fmt.Errorf("leg %d (%s): companions_loot is false but the feed lists no party member to turn looting off for: the leg is not started", leg, l.Name)
	}
	d.legLoot = true
	var muted, failed []string
	for _, m := range d.fr.Party {
		if r, err := og.Behavior(ctx, m.Name, "loot", false); err != nil || r.Kind != tbm.KindAck {
			failed = append(failed, m.Name+" ("+behaviorWhy(r, err)+")")
			continue
		}
		muted = append(muted, m.Name)
	}
	d.legMuted = muted
	if len(failed) > 0 {
		d.legLootOn() // the ones that did turn off go back on
		return fmt.Errorf("leg %d (%s): companions_loot is false but looting could not be turned off for %s: the leg is not started (%d of %d bots were turned off and are back on)", leg, l.Name, strings.Join(failed, ", "), len(muted), len(muted)+len(failed))
	}
	d.rep.note(fmt.Sprintf("leg %d: corpse and object looting is off for %v for the whole leg (companions_loot false)", leg, muted))
	return nil
}

// legLootOn puts the looting of the bots legLootOff muted back on; it runs when the leg ends, with a context of its own (a stopped run has
// none left).
func (d *RemoteDriver) legLootOn() {
	d.legLoot = false
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

// legLootSync turns the looting off for a bot that joined the party after the leg began (a hire in the middle of the leg, a bot that came
// back into the group): the leg's companions_loot false covers every bot of the leg. It runs before each step; a bot it cannot turn off
// fails the step like one at the leg's start.
func (d *RemoteDriver) legLootSync(ctx context.Context) error {
	if !d.legLoot || !d.haveFr {
		return nil
	}
	og, ok := d.orderGame()
	if !ok {
		return nil
	}
	for _, m := range d.fr.Party {
		if slices.Contains(d.legMuted, m.Name) {
			continue
		}
		if r, err := og.Behavior(ctx, m.Name, "loot", false); err != nil || r.Kind != tbm.KindAck {
			return fmt.Errorf("leg %d: %s joined the party during the leg and looting could not be turned off for it (%s): companions_loot is false", d.curLeg, m.Name, behaviorWhy(r, err))
		}
		d.legMuted = append(d.legMuted, m.Name)
		d.rep.note(fmt.Sprintf("leg %d: %s joined the party during the leg: its looting is off too (companions_loot false)", d.curLeg, m.Name))
	}
	return nil
}
