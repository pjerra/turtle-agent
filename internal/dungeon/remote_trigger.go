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
	if u.State == UnitGone {
		return d.unitGone(u)
	}
	return u.holds(d.unitSights(), d.killSeen(u))
}

// unitMemory is what a "gone" wait has seen so far: that a matching unit was alive in view, and for how many new frames in a row none was.
type unitMemory struct {
	seen   bool
	absent int
	lastN  uint64
}

// goneFrames is how many new frames in a row must show no living unit before a wait for "gone" holds.
const goneFrames = 2

// unitGone is the "gone" state of a unit wait: a unit that was never seen is not gone (nothing says it was ever there: an empty feed is
// unknown, not an answer), and one seen alive earlier in the step is gone only after goneFrames whole new frames without it, none of them
// truncated or stale, so that a feed that goes quiet for a moment does not pass the wait.
func (d *RemoteDriver) unitGone(u *UnitWait) bool {
	if d.unitMem == nil {
		d.unitMem = map[*UnitWait]*unitMemory{}
	}
	m := d.unitMem[u]
	if m == nil {
		m = &unitMemory{}
		d.unitMem[u] = m
	}
	alive := (&UnitWait{Entries: u.Entries, Near: u.Near, Radius: u.Radius, State: UnitSeen}).holds(d.unitSights(), false)
	switch {
	case alive:
		m.seen, m.absent = true, 0
	case d.n != m.lastN && d.haveFr && !d.fr.Truncated && !d.stale():
		m.absent++
	}
	m.lastN = d.n
	return m.seen && m.absent >= goneFrames
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
	// A resumed run whose unit is dead already (the kill ledger, or its corpse in view) has nothing to wake.
	if dead := (&UnitWait{Entries: exp.Entries, Near: exp.Near, Radius: exp.Radius, State: UnitDead}); d.unitHolds(dead) {
		d.rep.note(fmt.Sprintf("%s: %s is dead already: the trigger is not sent", a.Label, exp.String()))
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
		if !d.teleHome(ctx, f, h) {
			d.degrade(fmt.Sprintf("agent not back at its safe point %.1f %.1f %.1f", h.at.X, h.at.Y, h.at.Z))
		}
		return nil
	}
	return err
}

// teleHome puts the agent back at its safe point with `.go xyz` (a GM assist during the run, counted in run_gm_assists) and checks where it
// stands afterwards. It reports whether the agent is back; the reason it is not is noted.
func (d *RemoteDriver) teleHome(ctx context.Context, f Follower, h *agentHome) bool {
	sh, ok := f.(StartHands)
	if !ok {
		d.rep.note("area trigger: this game cannot teleport the agent: it stays where it is")
		return false
	}
	if err := sh.GoXYZ(ctx, d.plan.Map, h.at); err != nil {
		d.rep.note(fmt.Sprintf("area trigger: .go xyz back to the safe point failed: %v", err))
		return false
	}
	cmd := fmt.Sprintf(".go xyz %.2f %.2f %.2f %d", h.at.X, h.at.Y, h.at.Z, d.plan.Map)
	d.rep.update(func(r *Report) { r.SetupShortcuts = append(r.SetupShortcuts, cmd+" (GM assist during the run)") })
	d.remote(func(r *RemoteReport) { r.RunGMAssists++ })
	if at, m, here := f.AgentPlace(); !here || m != d.plan.Map || dist3(at, h.at) > triggerReturnYd {
		d.rep.note(fmt.Sprintf("area trigger: %s sent, but the agent does not stand at its safe point after it", cmd))
		return false
	}
	d.rep.note(fmt.Sprintf("area trigger: %s: the agent is back at its safe point (a GM assist during the run)", cmd))
	return true
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

// degrade records what a run could not put right at its end: the run still ends "done", with "(degraded: ...)" and the list in the report.
func (d *RemoteDriver) degrade(what string) {
	d.rep.update(func(r *Report) { r.Degraded = append(r.Degraded, what) })
	d.rep.note("degraded: " + what)
}

// lootRestoreTries is how often the order that turns a bot's looting back on is sent before the bot is given up on.
const lootRestoreTries = 3

// legLootOn puts the looting of the bots legLootOff muted back on; it runs when the leg ends, with a context of its own (a stopped run has
// none left). Each bot's order is tried lootRestoreTries times; a bot that still refuses, or a game that cannot send the order, makes the
// run "done (degraded: looting left off on ...)": the next run's start turns looting on for everyone (lootOnAtStart).
func (d *RemoteDriver) legLootOn() {
	d.legLoot = false
	if len(d.legMuted) == 0 {
		return
	}
	muted := d.legMuted
	d.legMuted = nil
	og, ok := d.orderGame()
	if !ok {
		d.degrade(fmt.Sprintf("looting left off on %s: this game cannot send behavior orders to turn it back on", strings.Join(muted, ", ")))
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	var back, left []string
	for _, n := range muted {
		var why string
		okOn := false
		for try := 1; try <= lootRestoreTries && !okOn; try++ {
			r, err := og.Behavior(ctx, n, "loot", true)
			if okOn = err == nil && r.Kind == tbm.KindAck; !okOn {
				why = behaviorWhy(r, err)
			}
		}
		if okOn {
			back = append(back, n)
			continue
		}
		left = append(left, n)
		d.rep.note(fmt.Sprintf("could not turn %s's looting back on after the leg (%d tries): %s", n, lootRestoreTries, why))
	}
	if len(back) > 0 {
		d.rep.note(fmt.Sprintf("looting turned back on for %v after the leg", back))
	}
	if len(left) > 0 {
		d.degrade(fmt.Sprintf("looting left off on %s", strings.Join(left, ", ")))
	}
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

// BotReviver is the optional part of a RemoteGame that revives one of the commander's bots with the GM command `.revive <bot>`: the way back
// for a bot that is dead outside the dungeon (reviveOffMap). Only a GM account has it.
type BotReviver interface {
	ReviveBot(ctx context.Context, name string) error
}

// offMapWait is how long a bot dead outside the dungeon is left to run back through the entrance when no GM account can revive it there.
const offMapWait = 120 * time.Second

// BotSummoner is the optional part of a RemoteGame that brings one of the commander's bots to the agent with the GM command `.summon <bot>`:
// the lead bot, when it stands outside the dungeon (bringPartyIn).
type BotSummoner interface {
	SummonBot(ctx context.Context, name string) error
}

// offMapMembers are the living members of the feed that stand on another map than the plan's.
func (d *RemoteDriver) offMapMembers() []tbm.PartyRec {
	var out []tbm.PartyRec
	for _, m := range d.fr.Party {
		if !m.Dead && m.Map != d.plan.Map {
			out = append(out, m)
		}
	}
	return out
}

// leadOutside says whether the lead bot is known to stand on another map than the plan's.
func (d *RemoteDriver) leadOutside() bool {
	l, ok := d.fr.LeadRec()
	return ok && d.haveFr && l.Map != d.plan.Map
}

// bringPartyIn puts every living member of the party on the plan's map after a recovery. The lead bot first: when it stands outside the
// dungeon (it was revived at a graveyard there) a GM account summons it to the agent, which stands in the instance (`.summon <lead>`); the
// others are then summoned to the lead (`cmd summon`, the rejoin), never the party out to a lead outside. Without a GM account the bots
// that are alive outside are given offMapWait to come in on their own. A member still outside at the end fails the recovery with the reason.
func (d *RemoteDriver) bringPartyIn(ctx context.Context) error {
	d.look()
	if len(d.offMapMembers()) == 0 {
		return nil
	}
	f, isF := d.g.(Follower)
	gm := isF && d.agentGM(ctx, f)
	if d.leadOutside() {
		l, _ := d.fr.LeadRec()
		sb, canSummon := d.g.(BotSummoner)
		if !gm || !canSummon {
			return d.waitPartyIn(ctx, fmt.Sprintf("the lead bot %s stands on map %d, outside the dungeon, and no GM account can summon it in", l.Name, l.Map))
		}
		if at, m, here := f.AgentPlace(); !here || m != d.plan.Map {
			return fmt.Errorf("the lead bot %s stands on map %d, outside the dungeon, and the agent's own character is not in the instance (%v), so it cannot summon the lead in: the party cannot be brought back", l.Name, l.Map, at)
		}
		if err := sb.SummonBot(ctx, l.Name); err != nil {
			return fmt.Errorf("the lead bot %s stands on map %d, outside the dungeon, and .summon %s failed: %w", l.Name, l.Map, l.Name, err)
		}
		cmd := ".summon " + l.Name
		d.rep.update(func(r *Report) { r.SetupShortcuts = append(r.SetupShortcuts, cmd+" (GM assist during the run)") })
		d.remote(func(r *RemoteReport) { r.RunGMAssists++ })
		d.rep.note(fmt.Sprintf("recovery: the lead bot %s was on map %d, outside the dungeon: %s (a GM assist during the run); the others are summoned to it after", l.Name, l.Map, cmd))
		until := d.clk.Now().Add(30 * time.Second)
		for d.leadOutside() {
			if !d.clk.Now().Before(until) {
				return fmt.Errorf("the lead bot %s is still on map %d 30 s after .summon %s: the party cannot be brought back", l.Name, l.Map, l.Name)
			}
			if err := d.tick(ctx, d.cfg.Poll); err != nil {
				return err
			}
		}
	}
	if err := d.rejoinFar(ctx); err != nil { // the others to the lead (a GM account; a no-op when nobody stands far)
		return err
	}
	return d.waitPartyIn(ctx, "bots are alive outside the dungeon and could not be summoned in")
}

// waitPartyIn waits up to offMapWait for every living member to stand on the plan's map; what is left outside fails with why.
func (d *RemoteDriver) waitPartyIn(ctx context.Context, why string) error {
	until := d.clk.Now().Add(offMapWait)
	for {
		d.look()
		off := d.offMapMembers()
		if len(off) == 0 {
			return nil
		}
		if !d.clk.Now().Before(until) {
			var names []string
			for _, m := range off {
				names = append(names, fmt.Sprintf("%s (map %d)", m.Name, m.Map))
			}
			return fmt.Errorf("recovery: %s still outside the dungeon after %.0f s: %s", strings.Join(names, ", "), offMapWait.Seconds(), why)
		}
		if err := d.tick(ctx, d.cfg.Poll*3); err != nil {
			return err
		}
	}
}

// maxGMRevives is how many `.revive` lines one bot gets before the run gives up on it.
const maxGMRevives = 3

// reviveOffMap deals with a dead bot that stands on another map than the plan's (a ghost at a graveyard outside the dungeon) after a
// `cmd revive` did not bring it back. A GM account revives it where it stands (a GM assist: run_gm_assists, setup_shortcuts), and the
// rejoin (`cmd summon`) takes it into the instance once the party is calm; the revive is repeated up to maxGMRevives times, 20 s apart.
// Without a GM account or the hands, handled is true and nothing is sent: the ghost runs back through the entrance on its own and the
// recovery's own time limit decides. handled false: out of options, the caller fails the bot.
func (d *RemoteDriver) reviveOffMap(ctx context.Context, m tbm.PartyRec) (handled bool, err error) {
	f, ok := d.g.(Follower)
	br, ok2 := d.g.(BotReviver)
	if !ok || !ok2 || !d.agentGM(ctx, f) {
		if d.offSince == nil {
			d.offSince = map[packet.GUID]time.Time{}
		}
		since, seen := d.offSince[m.GUID]
		if !seen {
			d.offSince[m.GUID] = d.clk.Now()
			since = d.clk.Now()
			d.rep.note(fmt.Sprintf("recovery: %s is dead on map %d, outside the dungeon, and `cmd revive` did not bring it back; no GM account to revive it there: it is left to run back through the entrance for up to %.0f s", m.Name, m.Map, offMapWait.Seconds()))
		}
		if d.clk.Now().Sub(since) > offMapWait {
			return false, fmt.Errorf("%s has been dead on map %d, outside the dungeon, for %.0f s: `cmd revive` did not bring it back and there is no GM account to revive it there (run with a GM account, or bring it in by hand)", m.Name, m.Map, offMapWait.Seconds())
		}
		return true, nil
	}
	if d.gmRevived == nil {
		d.gmRevived = map[packet.GUID]int{}
	}
	if d.gmRevived[m.GUID] >= maxGMRevives {
		return false, nil
	}
	if last, ok := d.lastGMRevive[m.GUID]; ok && d.clk.Now().Sub(last) < 20*time.Second {
		return true, nil
	}
	if d.lastGMRevive == nil {
		d.lastGMRevive = map[packet.GUID]time.Time{}
	}
	d.gmRevived[m.GUID]++
	d.lastGMRevive[m.GUID] = d.clk.Now()
	if err := br.ReviveBot(ctx, m.Name); err != nil {
		d.rep.note(fmt.Sprintf("recovery: .revive %s failed: %v", m.Name, err))
		return true, nil
	}
	cmd := ".revive " + m.Name
	d.rep.update(func(r *Report) { r.SetupShortcuts = append(r.SetupShortcuts, cmd+" (GM assist during the run)") })
	d.remote(func(r *RemoteReport) { r.RunGMAssists++ })
	d.rep.note(fmt.Sprintf("recovery: %s was dead on map %d, outside the dungeon: %s (a GM assist during the run); the rejoin summons it into the instance", m.Name, m.Map, cmd))
	return true, nil
}

// ---- loot_object steps on exact spawns (the graves) ----

// spawnOpened asks the module (`cmd state <guid>`) about the exact spawn a loot_object step names (ObjectRef.Spawn). skip says the step has
// nothing to open: the object is gone (not-found: opened and despawned) or not at rest any more (its state is not "ready": opened or
// looted); why says which. sure is false when the answer was inconclusive (too far from the bots, no answer, an older module): the step
// goes on and looks again after the approach. A step with no spawn, or a run without entry targets, is never skipped.
func (d *RemoteDriver) spawnOpened(ctx context.Context, a Action) (skip bool, why string, sure bool) {
	og, ok := d.orderGame()
	if !ok || a.SpawnGUID == 0 || !d.entry.ok {
		return false, "", true
	}
	cmd := "cmd state " + tbm.GUIDArg(a.SpawnGUID)
	res, err := og.Order(ctx, cmd)
	d.remote(func(r *RemoteReport) { r.OrdersSent++; r.Orders["state"]++; r.StatePolls++ })
	switch {
	case err != nil:
		return false, "", false
	case res.Kind == tbm.KindAck && res.State != nil:
		if st := strings.ToLower(res.State.State); st != "ready" {
			return true, fmt.Sprintf("the object %s is opened already (state %s)", tbm.GUIDArg(a.SpawnGUID), st), true
		}
		return false, "", true
	case res.Code() == "not-found":
		return true, fmt.Sprintf("the object %s is gone (opened and despawned, or not spawned)", tbm.GUIDArg(a.SpawnGUID)), true
	}
	return false, "", false
}

// fightOutRounds bounds the loop of fightOut.
const fightOutRounds = 6

// fightOutSettle is how long fightOut waits after a chest was opened for what it summoned to show and come at the party.
const fightOutSettle = 3 * time.Second

// fightEngagedOut fights out what the party is in combat with right now, with no settle and no rest gate: the engaged hostiles until none
// has fought the party for AggroQuiet, then the loot wait (afterFight). It does nothing when the party is not in combat. It runs before an
// object step and after a boss dies (the adds he left: live, zum2, the Zombies and Dead Heroes killed 2-4 bots every time after the kill).
func (d *RemoteDriver) fightEngagedOut(ctx context.Context, label string) error {
	for round := 0; round < fightOutRounds; round++ {
		d.look()
		if !d.inCombat() && len(d.fr.EngagedHostiles()) == 0 {
			return nil
		}
		if round == 0 {
			d.rep.note(fmt.Sprintf("%s: the party is in a fight: fighting it out first", label))
		}
		if err := d.fightEngaged(ctx); err != nil {
			return err
		}
		if err := d.afterFight(ctx); err != nil {
			return err
		}
	}
	return nil
}

// fightOut fights out what the party is in combat with (the mobs a grave summoned, a pack that came by): the engaged hostiles are fought
// until none has fought the party for AggroQuiet, then the loot wait and the stay spot (afterFight), and the rest gate runs when a rest
// line is crossed (no fixed wait per chest). It looks first after fightOutSettle, so that the mobs a chest has just summoned are in the feed.
func (d *RemoteDriver) fightOut(ctx context.Context, label string) error {
	if err := d.fightOutOnly(ctx, label); err != nil {
		return err
	}
	return d.restGate(ctx, false)
}

// fightOutOnly is fightOut without the rest gate at the end.
func (d *RemoteDriver) fightOutOnly(ctx context.Context, label string) error {
	if err := d.tick(ctx, fightOutSettle); err != nil {
		return err
	}
	for round := 0; round < fightOutRounds; round++ {
		d.look()
		if !d.inCombat() && len(d.fr.EngagedHostiles()) == 0 {
			break
		}
		if round == 0 {
			d.rep.note(fmt.Sprintf("%s: the party is in a fight (what the chest brought): fighting it out before the next step", label))
		}
		if err := d.fightEngaged(ctx); err != nil {
			return err
		}
		if err := d.afterFight(ctx); err != nil {
			return err
		}
	}
	return nil
}
