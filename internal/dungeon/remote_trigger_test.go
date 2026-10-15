package dungeon

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/pjerra/turtle-agent/internal/objects"
	"github.com/pjerra/turtle-agent/internal/packet"
	"github.com/pjerra/turtle-agent/internal/tbm"
)

// The area_trigger step, wait_for unit, a leg's companions_loot and the boss's kill_first in remote mode (remote_trigger.go).

// triggerGame is a nearGame whose agent can send area triggers, and a world where a trigger changes something: the module (moduleTakes)
// turns the boss hostile when the lead bot stands in the zone, and the agent's packet does when the agent stands in it.
type triggerGame struct {
	*nearGame
	center, boss Point
	radius       float32
	moduleTakes  bool // TortoiseBots' AiPlayerbot.BotAreaTriggers lists the trigger
	packetWorks  bool // the agent's CMSG_AREATRIGGER runs the script
	sent         []uint32
	sentFrom     []Point
	sentAt       time.Time
	teles        []Point
	teleErr      error // GoXYZ fails
	teleStays    bool  // GoXYZ is accepted and the agent does not move
	blockReturn  bool  // after the packet every walk of the agent's fails (it cannot get back)
	movesBefore  int   // the lead bot's move orders when the agent sent the packet
	fired        bool
	onFire       func(f *fakeRemote) // runs when the trigger takes effect
	friend       *rmob               // a unit of the boss's entry the feed lists friendly until the trigger: it turns hostile instead of a new one appearing
}

func newTriggerGame(t *testing.T, level int) *triggerGame {
	g := &triggerGame{nearGame: newNearGame(t, level), center: Point{110, 0, 0}, boss: Point{118, 0, 0}, radius: 10, packetWorks: true}
	g.agent = Point{-5, 0, 0}
	g.onTick = func(f *fakeRemote) {
		l := f.lead()
		if g.moduleTakes && !g.fired && dist3(Point{l.X, l.Y, l.Z}, g.center) <= float64(g.radius) {
			g.fire()
		}
	}
	return g
}

func (g *triggerGame) fire() {
	g.fired = true
	if g.friend != nil {
		g.friend.reaction = "h"
	} else {
		g.addMob(7271, g.boss)
	}
	if g.onFire != nil {
		g.onFire(g.fakeRemote)
	}
}

// GoXYZ is the GM `.go xyz` the agent's return falls back on after the boss is dead.
func (g *triggerGame) GoXYZ(ctx context.Context, mapID uint32, at Point) error {
	g.teles = append(g.teles, at)
	if g.teleErr != nil {
		return g.teleErr
	}
	if !g.teleStays {
		g.agent = at
	}
	return nil
}

func (g *triggerGame) AreaTrigger(ctx context.Context, id uint32) error {
	g.sent = append(g.sent, id)
	g.sentFrom = append(g.sentFrom, g.agent)
	g.sentAt, g.movesBefore = g.now, len(g.moveOrderDest)
	if g.blockReturn {
		g.walkErr = errors.New("path blocked")
	}
	if g.packetWorks && !g.fired && dist3(g.agent, g.center) <= float64(g.radius) {
		g.fire()
	}
	return nil
}

func zumrahLeg(by string, timeout int) Leg {
	t := &AreaTrigger{ID: 962, Center: Point{110, 0, 0}, Radius: 10, Stand: Point{104, 0, 0}, By: by, TimeoutS: timeout, Expect: "unit 7271 hostile",
		ExpectUnit: &UnitWait{Entries: []uint32{7271}, State: UnitHostile}}
	return Leg{Name: "zumrah", To: Point{80, 0, 0}, Steps: []Step{{AreaTrigger: t}}, Boss: &Boss{Entry: 7271, Position: Point{118, 0, 0}}}
}

func runTrigger(t *testing.T, g *triggerGame, legs ...Leg) (Report, error) {
	t.Helper()
	cfg := DefaultConfig()
	cfg.PostFight, cfg.AddsWait = 2*time.Second, 4*time.Second
	if !g.noVisibleOff {
		cfg.Setup = []string{"gm visible off"}
	}
	d := NewRemoteDriver(remotePlan(legs...), g, cfg, objOpts(), g.clock())
	err := d.Run(context.Background(), 0)
	rep, ended := d.Status()
	if !ended {
		t.Fatal("not ended")
	}
	return rep, err
}

// movedNear says whether one of the move orders went to within a yard of p.
func movedNear(dests []Point, p Point) bool {
	for _, d := range dests {
		if dist3(d, p) < 1 {
			return true
		}
	}
	return false
}

func triggerOf(t *testing.T, rep Report) TriggerReport {
	t.Helper()
	if rep.Remote == nil || len(rep.Remote.AreaTriggers) != 1 {
		t.Fatalf("area triggers: %+v", rep.Remote)
	}
	return rep.Remote.AreaTriggers[0]
}

// The lead bot walks into the zone and the module takes the trigger for it: the agent sends nothing, and the party then kills him.
func TestAreaTriggerTakenByTheLeadBot(t *testing.T) {
	g := newTriggerGame(t, 3)
	g.moduleTakes = true
	rep, err := runTrigger(t, g, zumrahLeg("auto", 15))
	if err != nil || rep.State != "done" {
		t.Fatalf("%v\n%s", err, notesOf(rep))
	}
	tr := triggerOf(t, rep)
	if tr.By != "lead bot" || tr.Result != "done" || tr.Trigger != 962 || len(g.sent) != 0 || rep.Remote.AgentTriggers != 0 {
		t.Fatalf("%+v sent %v", tr, g.sent)
	}
	// The lead bot was sent to the stand point inside the zone, and he died there.
	if !movedNear(g.moveOrderDest, Point{104, 0, 0}) {
		t.Errorf("no move to the stand: %v", g.moveOrderDest)
	}
	if g.mobsKilled != 1 {
		t.Errorf("killed %d", g.mobsKilled)
	}
}

// The module does not take the trigger: five seconds after the lead bot stands in the zone the agent does it itself: it comes up to
// the party with .appear (a GM assist), walks into the zone, sends the packet once and walks back.
func TestAreaTriggerFallsBackToTheAgent(t *testing.T) {
	g := newTriggerGame(t, 3)
	g.agent = Point{45, 0, 0} // 35 yd behind the lead bot after the leg's walk (no appear then), 59 once the lead bot is at the stand: this step brings it up
	var botStood time.Time
	g.onTick = func(f *fakeRemote) {
		if l := f.lead(); botStood.IsZero() && dist3(Point{l.X, l.Y, l.Z}, Point{104, 0, 0}) < 5 {
			botStood = f.now
		}
	}
	rep, err := runTrigger(t, g, zumrahLeg("auto", 15))
	if err != nil || rep.State != "done" {
		t.Fatalf("%v\n%s", err, notesOf(rep))
	}
	tr := triggerOf(t, rep)
	if wait := g.sentAt.Sub(botStood); botStood.IsZero() || wait < TriggerBotWait {
		t.Errorf("the agent sent %s after the lead bot stood in the zone: it must wait %s first", wait, TriggerBotWait)
	}
	if tr.By != "agent" || tr.Result != "done" || fmt.Sprint(g.sent) != "[962]" || rep.Remote.AgentTriggers != 1 || rep.Remote.RunGMAssists != 1 || fmt.Sprint(g.appears) != "[Tanky]" {
		t.Fatalf("%+v sent %v appears %v %+v", tr, g.sent, g.appears, rep.Remote)
	}
	// It sent from inside the zone (the stand point), and went back to where it had stood before walking in.
	if d := dist3(g.sentFrom[0], Point{104, 0, 0}); d > 0.1 {
		t.Errorf("sent from %v", g.sentFrom[0])
	}
	// The step ended at once; the boss step killed him; then the agent went back to where it stood just before THIS step's `.appear`.
	if n := len(g.walks); n < 2 || g.walks[n-2] != (Point{104, 0, 0}) || g.walks[n-1] != (Point{45, 0, 0}) || g.agent != (Point{45, 0, 0}) || len(g.teles) != 0 {
		t.Errorf("walks %v agent %v", g.walks, g.agent)
	}
	if !strings.Contains(notesOf(rep), "did not hold 5 s after the lead bot stood in the zone; the agent takes the trigger") {
		t.Errorf("%s", notesOf(rep))
	}
	if g.mobsKilled != 1 {
		t.Errorf("killed %d", g.mobsKilled)
	}
}

// by "bot" never calls the agent: a module without the trigger in its list fails the step, with the way to fix it.
func TestAreaTriggerByBotFailsWithoutTheModule(t *testing.T) {
	g := newTriggerGame(t, 3)
	rep, err := runTrigger(t, g, zumrahLeg("bot", 8))
	if err == nil || !strings.Contains(err.Error(), "did not hold: the module does not take area trigger 962 for a bot (TortoiseBots AiPlayerbot.BotAreaTriggers must list it, #1002)") {
		t.Fatalf("%v", err)
	}
	if len(g.sent) != 0 || triggerOf(t, rep).Result != "failed" || rep.State != "failed" {
		t.Fatalf("sent %v appears %v %+v", g.sent, g.appears, rep.Remote.AreaTriggers)
	}
}

// by "agent": the lead bot is not walked into the zone at all.
func TestAreaTriggerByAgentOnly(t *testing.T) {
	g := newTriggerGame(t, 3)
	g.moduleTakes = true // would take it, if the bot stood there
	rep, err := runTrigger(t, g, zumrahLeg("agent", 15))
	if err != nil || rep.State != "done" {
		t.Fatalf("%v\n%s", err, notesOf(rep))
	}
	if tr := triggerOf(t, rep); tr.By != "agent" || fmt.Sprint(g.sent) != "[962]" {
		t.Fatalf("%+v %v", tr, g.sent)
	}
	if movedNear(g.moveOrderDest[:g.movesBefore], Point{104, 0, 0}) {
		t.Errorf("the lead bot was sent to the stand before the agent's packet: %v", g.moveOrderDest[:g.movesBefore])
	}
}

// The agent sends the packet and nothing changes: the step fails, naming what did not hold, after one attempt: nothing is sent again.
func TestAreaTriggerThatChangesNothingFailsOnce(t *testing.T) {
	g := newTriggerGame(t, 3)
	g.packetWorks = false
	_, err := runTrigger(t, g, zumrahLeg("auto", 6))
	if err == nil || !strings.Contains(err.Error(), "the agent sent area trigger 962 from inside its zone and unit 7271 hostile did not hold within 6 s") {
		t.Fatalf("%v", err)
	}
	if len(g.sent) != 1 {
		t.Fatalf("sent %v: one attempt", g.sent)
	}
	// It walked out of the zone again even so.
	if n := len(g.walks); n < 2 || g.walks[n-1] == (Point{104, 0, 0}) {
		t.Errorf("walks %v", g.walks)
	}
}

// The agent's fallback is for a GM that is invisible: anything else is refused with the reason, and nothing is sent.
func TestAreaTriggerFallbackNeedsAnInvisibleGM(t *testing.T) {
	g := newTriggerGame(t, 0) // no GM
	_, err := runTrigger(t, g, zumrahLeg("agent", 15))
	if err == nil || !strings.Contains(err.Error(), "only as a GM") || len(g.sent) != 0 || movedNear(g.walks, Point{104, 0, 0}) {
		t.Fatalf("%v sent %v walks %v", err, g.sent, g.walks)
	}
	g = newTriggerGame(t, 3)
	g.noVisibleOff = true // a GM, but the setup did not make it invisible
	_, err = runTrigger(t, g, zumrahLeg("agent", 15))
	if err == nil || !strings.Contains(err.Error(), `the run's setup does not list "gm visible off"`) || len(g.sent) != 0 || movedNear(g.walks, Point{104, 0, 0}) {
		t.Fatalf("%v sent %v walks %v", err, g.sent, g.walks)
	}
	// By auto the lead bot gets its chance first, then the fallback is refused the same way.
	g = newTriggerGame(t, 0)
	_, err = runTrigger(t, g, zumrahLeg("auto", 15))
	if err == nil || !strings.Contains(err.Error(), "only as a GM") || len(g.sent) != 0 {
		t.Fatalf("%v sent %v", err, g.sent)
	}
}

// The trigger step ends as soon as `expect` holds. The fight it starts is the boss step's: it kills him (counted as killed, under its own
// timeout) while the agent stays in the zone, and the agent walks back only after the fight, never during it.
func TestAreaTriggerStepEndsAtOnceAndTheBossStepOwnsTheFight(t *testing.T) {
	g := newTriggerGame(t, 3)
	g.agent = Point{45, 0, 0}
	var walkedInCombat []string
	var bossDied, backAt time.Time
	g.onWalk = func(n *nearGame, to Point) {
		if n.party[0].State == "c" {
			walkedInCombat = append(walkedInCombat, fmt.Sprint(to))
		}
		if to == (Point{45, 0, 0}) {
			backAt = n.now
		}
	}
	var boss *rmob
	g.onFire = func(f *fakeRemote) { // the boss aggroes the party the moment the trigger runs (the fight starts inside the trigger step)
		boss = f.mobs[len(f.mobs)-1]
		boss.pulled, boss.pullAt = true, f.now
	}
	prev := g.onTick
	g.onTick = func(f *fakeRemote) {
		prev(f)
		if boss != nil && bossDied.IsZero() && boss.hp <= 0 {
			bossDied = f.now
		}
	}
	leg := zumrahLeg("agent", 15)
	// The kill is credited: a wait for the unit dead near him passes after the boss step.
	leg.AfterBoss = []Step{{WaitFor: &WaitFor{Unit: &UnitWait{Entries: []uint32{7271}, State: UnitDead, Near: &Point{118, 0, 0}, Radius: 20}, TimeoutS: 5}}}
	rep, err := runTrigger(t, g, leg)
	if err != nil || rep.State != "done" {
		t.Fatalf("%v\n%s", err, notesOf(rep))
	}
	tr := triggerOf(t, rep)
	if tr.By != "agent" || tr.Seconds > 8 || g.mobsKilled != 1 || len(walkedInCombat) != 0 {
		t.Errorf("trigger step %+v killed %d walked in combat %v", tr, g.mobsKilled, walkedInCombat)
	}
	if bossDied.IsZero() || backAt.IsZero() || backAt.Before(bossDied) || g.agent != (Point{45, 0, 0}) {
		t.Errorf("the boss died at %v, the agent was back at %v (%v)", bossDied, backAt, g.agent)
	}
}

// A dead boss never fails a run: a fight that outlasts the wait, or a walk that is blocked, after the boss step has killed the boss is a
// note and a GM `.go xyz` back to the safe point (a GM assist), not an error.
func TestAreaTriggerReturnAfterTheKillFallsBackToATeleport(t *testing.T) {
	for name, setup := range map[string]func(g *triggerGame){
		"a fight that never ends": func(g *triggerGame) { g.onFire = func(f *fakeRemote) { f.combatUntil = f.now.Add(time.Hour) } },
		"a blocked walk":          func(g *triggerGame) { g.blockReturn = true },
	} {
		g := newTriggerGame(t, 3)
		g.agent = Point{45, 0, 0}
		setup(g)
		rep, err := runTrigger(t, g, zumrahLeg("agent", 15))
		if err != nil || rep.State != "done" {
			t.Fatalf("%s: %v\n%s", name, err, notesOf(rep))
		}
		if fmt.Sprint(g.teles) != "[{45 0 0}]" || g.agent != (Point{45, 0, 0}) || g.mobsKilled != 1 || len(g.sent) != 1 {
			t.Errorf("%s: teles %v agent %v killed %d sent %v", name, g.teles, g.agent, g.mobsKilled, g.sent)
		}
		if !strings.Contains(notesOf(rep), "the boss is dead and the agent could not walk back") || rep.Remote.RunGMAssists != 1 { // the .go xyz (the agent stood 35 yd from the lead bot: no .appear)
			t.Errorf("%s: gm assists %d\n%s", name, rep.Remote.RunGMAssists, notesOf(rep))
		}
	}
}

// A failed walk AND a failed, unsupported or ineffective teleport end the run "done (degraded: agent not back at its safe point)": the
// state stays done, the report lists it.
func TestAreaTriggerReturnThatCannotBeFixedDegradesTheRun(t *testing.T) {
	for name, setup := range map[string]func(g *triggerGame){
		"the teleport fails":            func(g *triggerGame) { g.teleErr = errors.New("no such command") },
		"the teleport does not move it": func(g *triggerGame) { g.teleStays = true },
	} {
		g := newTriggerGame(t, 3)
		g.agent = Point{45, 0, 0}
		g.blockReturn = true
		setup(g)
		rep, err := runTrigger(t, g, zumrahLeg("agent", 15))
		if err != nil || rep.State != "done" {
			t.Fatalf("%s: %v\n%s", name, err, notesOf(rep))
		}
		if len(rep.Degraded) != 1 || !strings.Contains(rep.Degraded[0], "agent not back at its safe point 45.0 0.0 0.0") {
			t.Errorf("%s: degraded %v", name, rep.Degraded)
		}
	}
	// A game with no teleport at all (no StartHands) is the same.
	g := &noTeleGame{newTriggerGame(t, 3)}
	g.agent = Point{45, 0, 0}
	g.blockReturn = true
	cfg := DefaultConfig()
	cfg.Setup = []string{"gm visible off"}
	d := NewRemoteDriver(remotePlan(zumrahLeg("agent", 15)), g, cfg, objOpts(), g.clock())
	if err := d.Run(context.Background(), 0); err != nil {
		t.Fatal(err)
	}
	if rep, _ := d.Status(); rep.State != "done" || len(rep.Degraded) != 1 {
		t.Fatalf("%s %v", rep.State, rep.Degraded)
	}
}

// noTeleGame hides the triggerGame's GoXYZ.
type noTeleGame struct{ *triggerGame }

func (noTeleGame) GoXYZ() {} // a different signature: the game is no StartHands

// Before the boss is dead a failed return still fails the step it ran for (here the next leg's walk: nothing was killed).
func TestAreaTriggerReturnBeforeTheKillFailsTheStep(t *testing.T) {
	g := newTriggerGame(t, 3)
	g.agent = Point{45, 0, 0}
	g.blockReturn = true
	leg := zumrahLeg("agent", 15)
	leg.Boss = nil // the trigger's unit stays alive
	_, err := runTrigger(t, g, leg, Leg{Name: "next", To: Point{140, 0, 0}})
	if err == nil || !strings.Contains(err.Error(), "did not get back to its safe point") || len(g.teles) != 0 {
		t.Fatalf("%v teles %v", err, g.teles)
	}
}

// The feed lists Zum'rah as a friendly unit before the trigger (reaction f): that is not "hostile", and the trigger is still sent.
func TestAreaTriggerFriendlyUnitInTheFeedIsNotHostile(t *testing.T) {
	g := newTriggerGame(t, 3)
	friend := g.addMob(7271, g.boss)
	friend.reaction = "f"
	g.friend = friend
	rep, err := runTrigger(t, g, zumrahLeg("agent", 15))
	if err != nil || rep.State != "done" {
		t.Fatalf("%v\n%s", err, notesOf(rep))
	}
	if tr := triggerOf(t, rep); tr.By != "agent" || fmt.Sprint(g.sent) != "[962]" {
		t.Fatalf("%+v %v", tr, g.sent)
	}
	// A bystander (a critter) of the entry is not hostile either.
	if (&UnitWait{Entries: []uint32{7271}, State: UnitHostile}).holds([]unitSight{{entry: 7271, hostile: false}}, false) {
		t.Fatal("not hostile")
	}
}

// A game with no hands for the packet fails the agent's part with that reason.
type noPacketGame struct{ *nearGame }

func TestAreaTriggerGameWithoutHands(t *testing.T) {
	g := &noPacketGame{newNearGame(t, 3)}
	g.agent = Point{-5, 0, 0}
	cfg := DefaultConfig()
	d := NewRemoteDriver(remotePlan(zumrahLeg("agent", 5)), g, cfg, objOpts(), g.clock())
	err := d.Run(context.Background(), 0)
	if err == nil || !strings.Contains(err.Error(), "the agent has no hands for an area trigger in this game") {
		t.Fatalf("%v", err)
	}
}

// Expect already true (a resumed run, the trigger taken before): nothing is walked or sent.
func TestAreaTriggerAlreadyDone(t *testing.T) {
	g := newTriggerGame(t, 3)
	g.fire() // the boss is hostile from the start
	rep, err := runTrigger(t, g, zumrahLeg("auto", 15))
	if err != nil || rep.State != "done" {
		t.Fatalf("%v\n%s", err, notesOf(rep))
	}
	if tr := triggerOf(t, rep); tr.By != "already" || len(g.sent) != 0 || !strings.Contains(notesOf(rep), "unit 7271 hostile already: the trigger is not sent") {
		t.Fatalf("%+v %v", tr, g.sent)
	}
}

// ---- wait_for unit ----

// A wait for a unit reads the agent's own store when the feed does not list it (a friendly one): the agent's store holds Zum'rah as friendly,
// then hostile.
func TestWaitForUnitReadsTheAgentsStore(t *testing.T) {
	g := newNearGame(t, 3)
	g.agent = Point{-5, 0, 0}
	me := objects.Unit{GUID: packet.GUID(0xF130000000001234), Type: "creature", Entry: 7271, X: 50, Y: 0, PosAgeMS: 100, Health: 100, MaxHealth: 100}
	g.units = []objects.Unit{me}
	flips := false
	g.onTick = func(f *fakeRemote) {
		if !flips && f.now.After(time.Unix(1_000_000, 0).Add(12*time.Second)) {
			flips = true
			g.units[0].Hostile = true
		}
	}
	w := func(state string) Step {
		return Step{WaitFor: &WaitFor{Unit: &UnitWait{Entries: []uint32{7271}, State: state}, TimeoutS: 60}}
	}
	plan := remotePlan(Leg{Name: "w", To: Point{20, 0, 0}, Steps: []Step{w(UnitSeen), w(UnitHostile)}})
	cfg := DefaultConfig()
	d := NewRemoteDriver(plan, g, cfg, objOpts(), g.clock())
	if err := d.Run(context.Background(), 0); err != nil {
		rep, _ := d.Status()
		t.Fatalf("%v\n%s", err, notesOf(rep))
	}
	if !flips {
		t.Fatal("the unit never turned hostile: the second wait did not wait")
	}
}

func TestWaitForUnitTimesOut(t *testing.T) {
	g := newNearGame(t, 3)
	plan := remotePlan(Leg{Name: "w", To: Point{20, 0, 0}, Steps: []Step{{WaitFor: &WaitFor{Unit: &UnitWait{Entries: []uint32{7271}, State: UnitHostile}, TimeoutS: 5, FailText: "the trigger did not run"}}}})
	d := NewRemoteDriver(plan, g, DefaultConfig(), objOpts(), g.clock())
	err := d.Run(context.Background(), 0)
	if err == nil || !strings.Contains(err.Error(), "waited 5 s for unit 7271 hostile: the trigger did not run") {
		t.Fatalf("%v", err)
	}
}

// The kill ledger counts for "dead" after the feed has dropped the corpse.
func TestWaitForUnitDeadAfterAKill(t *testing.T) {
	g := carrierGame(t)
	g.addMob(7271, Point{30, 0, 0})
	plan := remotePlan(Leg{Name: "w", To: Point{20, 0, 0}, Boss: &Boss{Entry: 7271, Position: Point{30, 0, 0}},
		AfterBoss: []Step{{WaitFor: &WaitFor{Unit: &UnitWait{Entries: []uint32{7271}, State: UnitDead}, TimeoutS: 5}}}})
	rep, err := runCarrier(t, g, plan, objOpts())
	if err != nil || rep.State != "done" {
		t.Fatalf("%v\n%s", err, notesOf(rep))
	}
}

// With near, the kill the wait counts is one that happened within its radius of it: an earlier kill of the same entry elsewhere says
// nothing about the unit that stands here.
func TestWaitForUnitDeadNearNeedsTheKillThere(t *testing.T) {
	for _, tc := range []struct {
		name string
		near Point
		ok   bool
	}{{"kill at the place", Point{30, 0, 0}, true}, {"kill elsewhere", Point{500, 0, 0}, false}} {
		g := carrierGame(t)
		g.addMob(7271, Point{30, 0, 0})
		plan := remotePlan(Leg{Name: "w", To: Point{20, 0, 0}, Boss: &Boss{Entry: 7271, Position: Point{30, 0, 0}},
			AfterBoss: []Step{{WaitFor: &WaitFor{Unit: &UnitWait{Entries: []uint32{7271}, State: UnitDead, Near: &tc.near, Radius: 20}, TimeoutS: 5}}}})
		rep, err := runCarrier(t, g, plan, objOpts())
		if tc.ok && (err != nil || rep.State != "done") {
			t.Errorf("%s: %v\n%s", tc.name, err, notesOf(rep))
		}
		if !tc.ok && (err == nil || !strings.Contains(err.Error(), "waited 5 s for unit 7271 dead within 20 yd of 500 0 0")) {
			t.Errorf("%s: %v", tc.name, err)
		}
	}
}

// ---- Leg.CompanionsLoot ----

func lootLegPlan(withBossOff bool) *Plan {
	off := false
	boss := &Boss{Entry: 9, Position: Point{40, 0, 0}}
	if withBossOff {
		boss.CompanionsLoot = &off
	}
	return remotePlan(Leg{Name: "grave", To: Point{20, 0, 0}, CompanionsLoot: &off, Boss: boss}, Leg{Name: "after", To: Point{60, 0, 0}})
}

// The leg turns every bot's looting off when it starts and back on when it ends, and the boss's own companions_loot adds nothing to it (it
// would turn the looting back on in the middle of the leg).
func TestLegCompanionsLootOffForTheWholeLeg(t *testing.T) {
	for _, bossOff := range []bool{false, true} {
		g := carrierGame(t)
		g.addMob(9, Point{40, 0, 0})
		rep, err := runCarrier(t, g, lootLegPlan(bossOff), objOpts())
		if err != nil || rep.State != "done" {
			t.Fatalf("boss off %v: %v\n%s", bossOff, err, notesOf(rep))
		}
		// The run's start turns looting on for everyone (an earlier run may have left it off), the first leg off for everyone, the end of the
		// leg on again; the second leg has no say.
		var seq []string
		for _, b := range g.behaviors {
			f := strings.Fields(b)
			seq = append(seq, f[1]+" "+f[2])
		}
		want := strings.Repeat("loot true,", 3) + strings.Repeat("loot false,", 3) + strings.Repeat("loot true,", 3)
		if got := strings.Join(seq, ",") + ","; got != want {
			t.Errorf("boss off %v: %s\nwant %s", bossOff, got, want)
		}
		if !strings.Contains(notesOf(rep), "corpse and object looting is off for") || !strings.Contains(notesOf(rep), "looting turned back on for") {
			t.Errorf("%s", notesOf(rep))
		}
	}
}

// A leg that needs the looting off does not start without it. Partial: the bots that did turn off are turned back on and the run
// fails before the lead bot moves, naming the one that refused. None: all named. No behavior orders at all: the same.
func TestLegCompanionsLootRefusedFailsTheLegBeforeItMoves(t *testing.T) {
	for name, tc := range map[string]struct {
		refuse   []string
		wantErr  []string
		wantBack []string // bots turned off and back on
	}{
		"partial": {[]string{"Healy"}, []string{"Healy (refused"}, []string{"Tanky", "Magey"}},
		"none":    {[]string{"Tanky", "Healy", "Magey"}, []string{"Tanky (", "Healy (", "Magey ("}, nil},
	} {
		g := carrierGame(t)
		g.noMute = map[string]bool{}
		for _, b := range tc.refuse {
			g.noMute[b] = true
		}
		g.addMob(9, Point{40, 0, 0})
		rep, err := runCarrier(t, g, lootLegPlan(false), objOpts())
		if err == nil || rep.State != "failed" || !strings.Contains(err.Error(), "companions_loot is false but looting could not be turned off for") || !strings.Contains(err.Error(), "the leg is not started") {
			t.Fatalf("%s: %v", name, err)
		}
		for _, w := range tc.wantErr {
			if !strings.Contains(err.Error(), w) {
				t.Errorf("%s: %q is not named: %v", name, w, err)
			}
		}
		if g.indexOf("cmd move") >= 0 || g.indexOf("cmd pull") >= 0 {
			t.Errorf("%s: the leg moved before it failed: %v", name, g.orders)
		}
		// Every bot that turned off is turned on again (after the run's own start-up `on` for all three).
		tail := g.behaviors
		for _, bot := range tc.wantBack {
			offAt, onAfter := -1, false
			for i, b := range tail {
				if b == bot+" loot false" {
					offAt = i
				}
				if offAt >= 0 && b == bot+" loot true" {
					onAfter = true
				}
			}
			if offAt < 0 || !onAfter {
				t.Errorf("%s: %s was not turned off and back on: %v", name, bot, tail)
			}
		}
	}
}

// A bot that joins the party during the leg (a hire in the middle of it) has its looting turned off too, and back on at the leg's end.
func TestLegCompanionsLootCoversABotThatJoinsMidLeg(t *testing.T) {
	g := carrierGame(t)
	g.addMob(9, Point{40, 0, 0})
	g.left = map[string]bool{"Magey": true}
	g.lateJoin = map[string]time.Time{"Magey": g.now.Add(3 * time.Second)}
	o := objOpts()
	o.Bots = []string{"Healy"}
	rep, err := runCarrier(t, g, lootLegPlan(false), o)
	if err != nil || rep.State != "done" {
		t.Fatalf("%v\n%s", err, notesOf(rep))
	}
	off, on := -1, -1
	for i, b := range g.behaviors {
		if b == "Magey loot false" {
			off = i
		}
		if b == "Magey loot true" {
			on = i
		}
	}
	if off < 0 || on < off || !strings.Contains(notesOf(rep), "Magey joined the party during the leg") {
		t.Fatalf("off %d on %d: %v\n%s", off, on, g.behaviors, notesOf(rep))
	}
}

// An empty party list is a failure for companions_loot false: nothing could be turned off.
func TestLegCompanionsLootEmptyPartyFails(t *testing.T) {
	g := carrierGame(t)
	g.addMob(9, Point{40, 0, 0})
	d := NewRemoteDriver(lootLegPlan(false), g, DefaultConfig(), objOpts(), g.clock())
	d.fr.Party = nil
	if err := d.legLootOff(context.Background(), 0); err == nil || !strings.Contains(err.Error(), "the feed lists no party member") {
		t.Fatalf("%v", err)
	}
}

// The order that turns looting back on is tried three times; a bot that still refuses ends the run "done (degraded: looting left off on
// <bots>)", with the list in the report; a bot that gives in on the third try is no degradation.
func TestLegCompanionsLootRestoreRetriesThenDegrades(t *testing.T) {
	g := carrierGame(t)
	g.addMob(9, Point{40, 0, 0})
	g.noUnmute = map[string]int{"Healy": 2}
	rep, err := runCarrier(t, g, lootLegPlan(false), objOpts())
	if err != nil || rep.State != "done" || len(rep.Degraded) != 0 {
		t.Fatalf("two refusals then ok: %v %s %v", err, rep.State, rep.Degraded)
	}
	ons := 0
	for _, b := range g.behaviors {
		if b == "Healy loot true" {
			ons++
		}
	}
	if ons != 1+3 { // the run's start, then three tries at the leg's end
		t.Errorf("healy on orders %d: %v", ons, g.behaviors)
	}
	g = carrierGame(t)
	g.addMob(9, Point{40, 0, 0})
	g.noUnmute = map[string]int{"Healy": -1, "Magey": -1}
	rep, err = runCarrier(t, g, lootLegPlan(false), objOpts())
	if err != nil || rep.State != "done" || len(rep.Degraded) != 1 || rep.Degraded[0] != "looting left off on Healy, Magey" {
		t.Fatalf("%v %s %v\n%s", err, rep.State, rep.Degraded, notesOf(rep))
	}
}

// A game that cannot send the order at the leg's end is noted and degrades the run, not silent.
func TestLegLootOnWithoutBehaviorOrdersDegrades(t *testing.T) {
	g := newFakeRemote(t)
	d := NewRemoteDriver(lootLegPlan(false), g, DefaultConfig(), opts(), g.clock())
	d.legMuted = []string{"Tanky"}
	d.legLootOn()
	if rep, _ := d.Status(); len(rep.Degraded) != 1 || !strings.Contains(rep.Degraded[0], "looting left off on Tanky") {
		t.Fatalf("%v", rep.Degraded)
	}
}

func TestLegCompanionsLootNeedsBehaviorOrders(t *testing.T) {
	g := newFakeRemote(t) // no OrderGame: it cannot send behavior orders
	_, err := runRemote(t, g, lootLegPlan(false), opts(), nil)
	if err == nil || !strings.Contains(err.Error(), "this game cannot send behavior orders") || g.indexOf("cmd move") >= 0 {
		t.Fatalf("%v %v", err, g.orders)
	}
}

// ---- the ward of Zum'rah: plan kill_first and summoned_kill_first (the design of #190) ----

// zumrahFightPlan is a boss leg whose plan names the ward 7785 as a summoned kill_first unit.
func zumrahFightPlan(killFirst bool) *Plan {
	p := remotePlan(Leg{Name: "boss", To: Point{20, 0, 0}, Boss: &Boss{Entry: 7271, Position: Point{30, 0, 0}}})
	if killFirst {
		p.KillFirst, p.SummonedKillFirst = []uint32{7785}, []uint32{7785}
	}
	return p
}

// Zum'rah's ward joins the fight and heals without ever fighting the party. It stands by the boss, which fights the party: that is the
// encounter evidence, and the party is sent at it ahead of the boss and without the tank's threat wait. Without kill_first it stays up and
// is never attacked. A unit of the same entry that is not in the encounter (it stands far from every hostile that fights the party, or was
// there before the boss was engaged) is left alone however near the lead bot it stands.
func TestKillFirstSendsThePartyAtTheWardFirst(t *testing.T) {
	for _, withKillFirst := range []bool{true, false} {
		g := newFakeRemote(t)
		boss := g.addMob(7271, Point{30, 0, 0})
		// A ward of someone else's, up by the lead bot (10 yd) and 20 yd from the boss: not in this encounter.
		var stray, ward *rmob
		var bossDied time.Time
		g.onTick = func(f *fakeRemote) {
			if ward == nil && boss.attacked { // summoned once the party is on the boss, as Zum'rah's is
				ward = f.addMob(7785, Point{38, 0, 0})
				ward.passive = true
				stray = f.addMob(7785, Point{10, -3, 0})
				stray.passive = true
			}
			if bossDied.IsZero() && boss.hp <= 0 {
				bossDied = f.now
			}
		}
		rep, err := runRemote(t, g, zumrahFightPlan(withKillFirst), opts(), nil)
		if err != nil || rep.State != "done" {
			t.Fatalf("kill_first %v: %v\n%s", withKillFirst, err, notesOf(rep))
		}
		if ward == nil {
			t.Fatal("the ward never appeared")
		}
		wardAttack := g.indexOf("cmd attack " + tbm.GUIDArg(ward.guid))
		if g.indexOf("cmd attack "+tbm.GUIDArg(stray.guid)) >= 0 || stray.hp <= 0 {
			t.Errorf("kill_first %v: the unrelated ward was attacked: hp %.0f\n%v", withKillFirst, stray.hp, g.orders)
		}
		if !withKillFirst {
			if wardAttack >= 0 || ward.hp <= 0 {
				t.Errorf("without kill_first the ward is left alone: attack at %d, hp %.0f", wardAttack, ward.hp)
			}
			continue
		}
		if wardAttack < 0 || ward.hp > 0 {
			t.Fatalf("the ward was not attacked and killed: attack at %d, hp %.0f\n%v", wardAttack, ward.hp, g.orders)
		}
		// The party was sent at the ward while the boss still lived: it is the first order after it joined, not a leftover once the boss is dead.
		if at := g.orderAt[wardAttack]; bossDied.IsZero() || !at.Before(bossDied) {
			t.Errorf("the ward was attacked at %v, the boss died at %v: not first\n%v", at, bossDied, g.orders)
		}
	}
}

// A same-entry unit within 40 yd of the lead bot but far from the encounter is not part of it.
func TestKillFirstIgnoresAWardAwayFromTheEncounter(t *testing.T) {
	g := newFakeRemote(t)
	boss := g.addMob(7271, Point{30, 0, 0})
	var ward *rmob
	g.onTick = func(f *fakeRemote) {
		if ward == nil && boss.attacked {
			ward = f.addMob(7785, Point{20, 30, 0}) // 30 yd from the lead bot, 31.6 from the boss
			ward.passive = true
		}
	}
	rep, err := runRemote(t, g, zumrahFightPlan(true), opts(), nil)
	if err != nil || rep.State != "done" || ward == nil || g.indexOf("cmd attack "+tbm.GUIDArg(ward.guid)) >= 0 || ward.hp <= 0 {
		t.Fatalf("%v %s\n%v", err, notesOf(rep), g.orders)
	}
}

// "gone" needs a unit that was seen alive first, and then whole new frames without it: an empty feed is unknown, a blink does not pass.
func TestWaitForUnitGoneNeedsASightingAndSteadyAbsence(t *testing.T) {
	goneStep := Step{WaitFor: &WaitFor{Unit: &UnitWait{Entries: []uint32{7271}, State: UnitGone}, TimeoutS: 20}}
	// Never seen: not gone, and the wait times out.
	g := newNearGame(t, 3)
	plan := remotePlan(Leg{Name: "w", To: Point{20, 0, 0}, Steps: []Step{goneStep}})
	err := NewRemoteDriver(plan, g, DefaultConfig(), objOpts(), g.clock()).Run(context.Background(), 0)
	if err == nil || !strings.Contains(err.Error(), "waited 20 s for unit 7271 gone") {
		t.Fatalf("never seen: %v", err)
	}
	// Seen, then gone for good: the wait passes (after the steady absence).
	g = newNearGame(t, 3)
	m := g.addMob(7271, Point{150, 100, 0}) // far off the walk: the wait sees it alive, nothing pulls it
	g.onTick = func(f *fakeRemote) {
		if f.now.After(time.Unix(1_000_000, 0).Add(10 * time.Second)) {
			m.hp = 0
			f.mobs = nil
		}
	}
	if err := NewRemoteDriver(remotePlan(Leg{Name: "w", To: Point{20, 0, 0}, Steps: []Step{goneStep}}), g, DefaultConfig(), objOpts(), g.clock()).Run(context.Background(), 0); err != nil {
		t.Fatalf("seen then gone: %v", err)
	}
	// Unit unit test: one empty frame does not pass; two do; a truncated frame counts for nothing.
	d := NewRemoteDriver(remotePlan(Leg{Name: "w", To: Point{20, 0, 0}}), newFakeRemote(t), DefaultConfig(), objOpts(), Clock{})
	u := &UnitWait{Entries: []uint32{7271}, State: UnitGone}
	d.haveFr, d.n = true, 1
	d.fr.Hostiles = []tbm.HostileRec{{GUID: 1, Entry: 7271, HPPct: 100}}
	if d.unitGone(u) {
		t.Fatal("alive")
	}
	d.fr.Hostiles, d.n = nil, 2
	if d.unitGone(u) {
		t.Fatal("one empty frame passed")
	}
	d.fr.Truncated, d.n = true, 3
	if d.unitGone(u) {
		t.Fatal("a truncated frame counted")
	}
	d.fr.Truncated, d.n = false, 4
	if !d.unitGone(u) {
		t.Fatal("two whole empty frames after a sighting")
	}
}

// A resumed run whose Zum'rah is dead already (his corpse in the agent's view) does not send the trigger.
func TestAreaTriggerSkippedWhenTheUnitIsDeadAlready(t *testing.T) {
	g := newTriggerGame(t, 3)
	g.units = []objects.Unit{corpseOf(7271, Point{118, 0, 0})}
	rep, err := runTrigger(t, g, zumrahLeg("agent", 15))
	if err != nil || len(g.sent) != 0 || triggerOf(t, rep).By != "already" || !strings.Contains(notesOf(rep), "is dead already: the trigger is not sent") {
		t.Fatalf("%v sent %v\n%s", err, g.sent, notesOf(rep))
	}
}

// ---- group mode: the agent leads, so the agent takes the trigger ----

// zumrahGame is the group driver's fake: the agent's packet turns Zum'rah hostile when it stands in the zone.
type zumrahGame struct {
	*fakeGame
	center  Point
	radius  float32
	friend  *fakeUnit // Zum'rah before the trigger: friendly
	works   bool
	sentPos []Point
}

func newZumrahGame(works bool) *zumrahGame {
	g := &zumrahGame{fakeGame: newFake(), center: Point{110, 0, 0}, radius: 10, works: works}
	g.friend = g.add(77, 7271, Point{118, 0, 0})
	g.friend.Hostile = false
	return g
}

func (g *zumrahGame) AreaTrigger(ctx context.Context, id uint32) error {
	g.sentPos = append(g.sentPos, g.pos)
	if g.works && id == 962 && dist3(g.pos, g.center) <= float64(g.radius) {
		g.friend.Hostile = true
	}
	return nil
}

func zumrahGroupPlan(by string, timeout int) *Plan {
	l := zumrahLeg(by, timeout)
	l.Boss = nil
	return zumrahMapPlan(l)
}

// zumrahMapPlan is a plan on the group fake's map.
func zumrahMapPlan(legs ...Leg) *Plan {
	p := remotePlan(legs...)
	p.Map = 70
	return p
}

func TestGroupDriverTakesTheAreaTriggerWithTheAgent(t *testing.T) {
	for _, by := range []string{"auto", "agent"} {
		g := newZumrahGame(true)
		d := NewDriver(zumrahGroupPlan(by, 15), g, Config{}, g.clock())
		if err := d.Run(context.Background(), 0); err != nil {
			t.Fatalf("%s: %v\n%+v", by, err, d.Report())
		}
		// It walked to the stand point in the zone, sent the packet once from there, and Zum'rah turned hostile.
		if len(g.sentPos) != 1 || dist3(g.sentPos[0], Point{104, 0, 0}) > 0.5 || !g.friend.Hostile {
			t.Fatalf("%s: sent from %v", by, g.sentPos)
		}
	}
}

func TestGroupDriverAreaTriggerByBotNeedsALeadBot(t *testing.T) {
	g := newZumrahGame(true)
	d := NewDriver(zumrahGroupPlan("bot", 15), g, Config{}, g.clock())
	err := d.Run(context.Background(), 0)
	if err == nil || !strings.Contains(err.Error(), `by "bot" needs a lead bot: it runs in remote mode only`) || len(g.sentPos) != 0 {
		t.Fatalf("%v sent %v", err, g.sentPos)
	}
}

func TestGroupDriverAreaTriggerThatDoesNothingFailsOnce(t *testing.T) {
	g := newZumrahGame(false)
	d := NewDriver(zumrahGroupPlan("auto", 6), g, Config{}, g.clock())
	err := d.Run(context.Background(), 0)
	if err == nil || !strings.Contains(err.Error(), "area trigger 962 was sent and unit 7271 hostile did not hold within 6 s") || len(g.sentPos) != 1 {
		t.Fatalf("%v sent %v", err, g.sentPos)
	}
}

func TestGroupDriverAreaTriggerAlreadyDone(t *testing.T) {
	g := newZumrahGame(true)
	g.friend.Hostile = true
	d := NewDriver(zumrahGroupPlan("auto", 15), g, Config{}, g.clock())
	if err := d.Run(context.Background(), 0); err != nil || len(g.sentPos) != 0 {
		t.Fatalf("%v sent %v", err, g.sentPos)
	}
}

// A wait for a unit in group mode reads the agent's own store.
func TestGroupDriverWaitsForAUnit(t *testing.T) {
	g := newZumrahGame(true)
	w := Step{WaitFor: &WaitFor{Unit: &UnitWait{Entries: []uint32{7271}, State: UnitHostile}, TimeoutS: 30}}
	g.onTick = func(f *fakeGame) {
		if f.now.After(time.Unix(1_000_000, 0).Add(8 * time.Second)) {
			g.friend.Hostile = true
		}
	}
	d := NewDriver(zumrahMapPlan(Leg{Name: "w", To: Point{10, 0, 0}, Steps: []Step{w}}), g, Config{}, g.clock())
	if err := d.Run(context.Background(), 0); err != nil || !g.friend.Hostile {
		t.Fatalf("%v", err)
	}
}

func TestGroupDriverAreaTriggerSkippedWhenDeadAlready(t *testing.T) {
	g := newZumrahGame(true)
	g.friend.Dead, g.friend.Health = true, 0
	d := NewDriver(zumrahGroupPlan("auto", 15), g, Config{}, g.clock())
	if err := d.Run(context.Background(), 0); err != nil || len(g.sentPos) != 0 {
		t.Fatalf("%v sent %v", err, g.sentPos)
	}
}

// The group driver cannot keep its companions from looting a whole leg: a leg with companions_loot false is not started.
func TestGroupDriverRefusesALegWithCompanionsLootFalse(t *testing.T) {
	g := newFake()
	off := false
	d := NewDriver(zumrahMapPlan(Leg{Name: "grave", To: Point{10, 0, 0}, CompanionsLoot: &off, Steps: []Step{{WaitFor: &WaitFor{HostilesGone: &HostilesGone{Radius: 5}}}}}), g, Config{}, g.clock())
	err := d.Run(context.Background(), 0)
	if err == nil || !strings.Contains(err.Error(), "leg 0 (grave): companions_loot is false, which only a remote (commander) run can enforce") || len(g.walks) != 0 {
		t.Fatalf("%v walks %v", err, g.walks)
	}
}

// ---- recovery of a bot that is dead outside the dungeon, and a wipe after the boss kill ----

// reviveGame is a nearGame whose GM account can `.revive` a bot.
type reviveGame struct {
	*nearGame
	revives []string
}

func (g *reviveGame) ReviveBot(ctx context.Context, name string) error {
	g.revives = append(g.revives, name)
	for _, p := range g.party {
		if p.Name == name {
			delete(g.reviveStuck, name)
			p.Dead, p.HP, p.State = false, p.MaxHP/2, "n"
		}
	}
	return nil
}

// deadOffMapPlan: the lead bot dies on its walk and Healy is a ghost on map 1 that `cmd revive` does nothing for.
func deadOffMapRun(t *testing.T, g interface {
	RemoteGame
	clock() Clock
}, f *fakeRemote) (Report, error) {
	t.Helper()
	f.reviveStuck = map[string]bool{"Healy": true}
	died := false
	f.onTick = func(fr *fakeRemote) {
		if !died && fr.lead().X > 5 {
			died = true
			fr.lead().Dead, fr.lead().HP = true, 0
			fr.party[1].Dead, fr.party[1].HP = true, 0
			fr.memberMap = map[packet.GUID]uint32{gHealer: 1}
		}
	}
	plan := remotePlan(Leg{Name: "a", To: Point{60, 0, 0}})
	cfg := DefaultConfig()
	cfg.Setup = []string{"gm visible off"}
	d := NewRemoteDriver(plan, g, cfg, objOpts(), g.clock())
	err := d.Run(context.Background(), 0)
	rep, _ := d.Status()
	return rep, err
}

// A GM account revives the ghost where it is (`.revive`) instead of failing after three `cmd revive` orders, and the rejoin summons it in.
func TestRecoveryRevivesABotDeadOutsideTheDungeonWithTheGM(t *testing.T) {
	g := &reviveGame{nearGame: newNearGame(t, 3)}
	g.agent = Point{5, 0, 0}
	rep, err := deadOffMapRun(t, g, g.fakeRemote)
	if err != nil || rep.State != "done" {
		t.Fatalf("%v\n%s", err, notesOf(rep))
	}
	if fmt.Sprint(g.revives) != "[Healy]" || g.indexOf("cmd summon Healy") < 0 || !strings.Contains(notesOf(rep), "was dead on map 1, outside the dungeon: .revive Healy") {
		t.Fatalf("revives %v orders %v\n%s", g.revives, g.orders, notesOf(rep))
	}
}

// Without a GM account the ghost is left to run back through the entrance, for the recovery's own time: no failure after three orders.
func TestRecoveryLeavesAGhostOutsideTheDungeonToRunBackWithoutAGM(t *testing.T) {
	g := &reviveGame{nearGame: newNearGame(t, 0)}
	g.agent = Point{5, 0, 0}
	var back time.Time
	prev := g.onTick
	_ = prev
	rep, err := func() (Report, error) {
		g.reviveStuck = map[string]bool{"Healy": true}
		died := false
		g.onTick = func(fr *fakeRemote) {
			if !died && fr.lead().X > 5 {
				died = true
				back = fr.now.Add(70 * time.Second) // longer than three revive orders (20 s apart)
				fr.lead().Dead, fr.lead().HP = true, 0
				fr.party[1].Dead, fr.party[1].HP = true, 0
				fr.memberMap = map[packet.GUID]uint32{gHealer: 1}
			}
			if died && !back.IsZero() && !fr.now.Before(back) && fr.party[1].Dead {
				fr.party[1].Dead, fr.party[1].HP = false, fr.party[1].MaxHP
				delete(fr.memberMap, gHealer)
			}
		}
		cfg := DefaultConfig()
		d := NewRemoteDriver(remotePlan(Leg{Name: "a", To: Point{60, 0, 0}}), g, cfg, objOpts(), g.clock())
		err := d.Run(context.Background(), 0)
		rep, _ := d.Status()
		return rep, err
	}()
	if err != nil || rep.State != "done" || len(g.revives) != 0 || !strings.Contains(notesOf(rep), "no GM account to revive it there") {
		t.Fatalf("%v %v\n%s", err, g.revives, notesOf(rep))
	}
}

// A wipe in a leg whose boss is dead is no failure of the boss step: it is recovered (also with recover_wipe off), counted apart as a wipe
// after the kill, and the leg finishes.
func TestWipeAfterTheBossKillIsRecoveredAndCountedApart(t *testing.T) {
	g := newFakeRemote(t)
	boss := g.addMob(9, Point{30, 0, 0})
	wiped := false
	var diedAt time.Time
	g.onTick = func(f *fakeRemote) {
		if diedAt.IsZero() && boss.hp <= 0 {
			diedAt = f.now
		}
		if !wiped && !diedAt.IsZero() && f.now.Sub(diedAt) >= 3*time.Second {
			wiped = true // the adds the boss left kill the party, a few seconds after the kill
			for _, p := range f.party {
				p.Dead, p.HP = true, 0
			}
		}
	}
	plan := remotePlan(Leg{Name: "boss", To: Point{20, 0, 0}, Boss: &Boss{Entry: 9, Position: Point{30, 0, 0}}})
	rep, err := runRemote(t, g, plan, opts(), func(c *Config) { c.RecoverWipe = false })
	if err != nil || rep.State != "done" {
		t.Fatalf("%v\n%s", err, notesOf(rep))
	}
	r := rep.Remote
	if !wiped || r.WipesAfterKill != 1 || rep.Wipes != 0 || rep.PartyDowns != 0 || len(g.revived) != 3 || !strings.Contains(notesOf(rep), "wipe after the kill: the boss of leg 0 is dead") {
		t.Fatalf("wiped %v after_kill %d wipes %d downs %d revived %v\n%s", wiped, r.WipesAfterKill, rep.Wipes, rep.PartyDowns, g.revived, notesOf(rep))
	}
}

// A wipe before the boss is dead stays a wipe (and, with recover_wipe off, a failure).
func TestWipeBeforeTheBossKillIsStillAWipe(t *testing.T) {
	g := newFakeRemote(t)
	g.addMob(9, Point{30, 0, 0})
	killed := false
	g.onTick = func(f *fakeRemote) {
		if !killed && len(f.mobs) > 0 && f.mobs[0].engaged {
			killed = true
			for _, p := range f.party {
				p.Dead, p.HP = true, 0
			}
		}
	}
	plan := remotePlan(Leg{Name: "boss", To: Point{20, 0, 0}, Boss: &Boss{Entry: 9, Position: Point{30, 0, 0}}})
	rep, err := runRemote(t, g, plan, opts(), func(c *Config) { c.RecoverWipe = false })
	if err == nil || rep.State != "failed" || rep.Remote.WipesAfterKill != 0 {
		t.Fatalf("%v %s %d", err, rep.State, rep.Remote.WipesAfterKill)
	}
}
