package dungeon

import (
	"context"
	"fmt"
	"github.com/pjerra/turtle-agent/internal/objects"
	"github.com/pjerra/turtle-agent/internal/packet"
	"github.com/pjerra/turtle-agent/internal/tbm"
	"strings"
	"testing"
	"time"
)

// Remote Uldaman run 2 fixes (issue #83): the end of a run with the party still fighting, team-gated bosses that are hostile, corpses
// the bots did not see, packs given up on, a binding that is already there.

// runRemoteCtx is runRemote with the caller's context (a run cancelled mid-way).
func runRemoteCtx(t *testing.T, ctx context.Context, g *fakeRemote, plan *Plan, o RemoteOptions) (Report, error) {
	t.Helper()
	cfg := DefaultConfig()
	cfg.PostFight = 2 * time.Second
	cfg.AddsWait = 4 * time.Second
	d := NewRemoteDriver(plan, g, cfg, o, g.clock())
	err := d.Run(ctx, 0)
	rep, _ := d.Status()
	return rep, err
}

// A stopped run with a pack on the party keeps the binding and sends the attack orders until it is dead, then stays and unbinds.
func TestRemoteEndFinishesTheFightBeforeUnbinding(t *testing.T) {
	g := newFakeRemote(t)
	plan := remotePlan(Leg{Name: "far", To: Point{400, 0, 0}})
	ctx, cancel := context.WithCancel(context.Background())
	started := false
	g.onTick = func(g *fakeRemote) {
		if !started && g.lead().X > 100 {
			started = true
			m := g.addMob(299, Point{g.lead().X + 5, 0, 0})
			m.pulled, m.engaged, m.pullAt = true, true, g.now.Add(-5*time.Second)
			cancel() // the user stops the run with the pack on the party
		}
	}
	rep, err := runRemoteCtx(t, ctx, g, plan, opts())
	if err == nil || rep.State != "stopped" {
		t.Fatalf("%v %s", err, rep.State)
	}
	n := len(g.orders)
	if g.orders[n-1] != "commander unbind" || g.orders[n-2] != "cmd stay" {
		t.Fatalf("the end: %v", g.orders[max(0, n-4):])
	}
	at := g.indexOf("cmd attack")
	if at < 0 || at > n-3 || g.mobsKilled != 1 {
		t.Fatalf("no attack order before the stay (attack %d of %d, killed %d): %v", at, n, g.mobsKilled, g.orders)
	}
	if !strings.HasPrefix(rep.Remote.EndFight, "out of combat") {
		t.Fatalf("end fight %q", rep.Remote.EndFight)
	}
	// the party was out of combat when the stay went out
	for _, p := range g.party {
		if p.State == "c" {
			t.Fatalf("%s still in combat at the unbind", p.Name)
		}
	}
}

// A fight that does not end is given up on after EndFightWait: the stay and the unbind still go.
func TestRemoteEndGivesUpOnAFightThatNeverEnds(t *testing.T) {
	g := newFakeRemote(t)
	g.killRate = 0
	plan := remotePlan(Leg{Name: "far", To: Point{400, 0, 0}})
	started := false
	o := opts()
	o.EndFightWait = 20 * time.Second
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	g.onTick = func(g *fakeRemote) {
		if !started && g.lead().X > 100 {
			started = true
			m := g.addMob(299, Point{g.lead().X + 5, 0, 0})
			m.pulled, m.engaged, m.pullAt = true, true, g.now.Add(-5*time.Second)
			cancel()
		}
	}
	rep, _ := runRemoteCtx(t, ctx, g, plan, o)
	n := len(g.orders)
	if g.orders[n-1] != "commander unbind" || g.orders[n-2] != "cmd stay" || !strings.Contains(rep.Remote.EndFight, "gave up") {
		t.Fatalf("%q %v", rep.Remote.EndFight, g.orders[max(0, n-4):])
	}
	if g.unbinds != 1 {
		t.Fatalf("unbinds %d", g.unbinds)
	}
}

// A negative EndFightWait unbinds at once, as before.
func TestRemoteEndFightWaitOff(t *testing.T) {
	g := newFakeRemote(t)
	plan := remotePlan(Leg{Name: "far", To: Point{400, 0, 0}})
	ctx, cancel := context.WithCancel(context.Background())
	started := false
	g.onTick = func(g *fakeRemote) {
		if !started && g.lead().X > 100 {
			started = true
			m := g.addMob(299, Point{g.lead().X + 5, 0, 0})
			m.pulled, m.engaged, m.pullAt = true, true, g.now.Add(-5*time.Second)
			cancel()
		}
	}
	o := opts()
	o.EndFightWait = -1
	rep, _ := runRemoteCtx(t, ctx, g, plan, o)
	if rep.Remote.EndFight != "" || g.indexOf("cmd attack") >= 0 {
		t.Fatalf("%q %v", rep.Remote.EndFight, g.orders)
	}
}

// Fix 2: a team-gated boss whose mobs are hostile in the feed is fought like a pull group; one never seen is skipped as before.
func TestRemoteTeamGateFightsHostileDwarves(t *testing.T) {
	plan := remotePlan(Leg{Name: "baelog", To: Point{20, 0, 0},
		PullGroups: []PullGroup{{Entries: []uint32{1}, Positions: []Point{{30, 0, 0}}, Team: TeamHorde}},
		Boss:       &Boss{Entry: 6906, Position: Point{60, 0, 0}, First: []uint32{6907}, Team: TeamHorde}})
	o := opts()
	o.Team = "alliance"
	// hostile and in view: the group and the boss (and its First add) are fought
	g := newFakeRemote(t)
	g.addMob(1, Point{30, 0, 0})
	g.addMob(6907, Point{58, 0, 0})
	g.addMob(6906, Point{60, 0, 0})
	rep, err := runRemote(t, g, plan, o, nil)
	if err != nil || rep.State != "done" {
		t.Fatalf("%v %s %v", err, rep.State, rep.Notes)
	}
	if g.mobsKilled != 3 || len(g.ordersOf("cmd pullback")) != 3 {
		t.Fatalf("killed %d, pulls %v", g.mobsKilled, g.ordersOf("cmd pullback"))
	}
	notes := notesOf(rep)
	if !strings.Contains(notes, "but its mobs are hostile to the party: fought") || strings.Contains(notes, "alliance: skipped") {
		t.Fatalf("%s", notes)
	}
	if strings.Count(notes, "boss 6906") < 1 || strings.Count(notes, "but its mobs are hostile") != 2 {
		t.Fatalf("one note per part, got:\n%s", notes)
	}
	// nothing of them in view: skipped, as before
	g = newFakeRemote(t)
	rep, err = runRemote(t, g, plan, o, nil)
	if err != nil || !strings.Contains(notesOf(rep), "boss 6906 is for team horde, the leader is alliance: skipped") || len(g.ordersOf("cmd pullback")) != 0 {
		t.Fatalf("%v\n%s", err, notesOf(rep))
	}
}

// A team-gated boss listed hostile earlier in the run and out of the feed now is not skipped as "not seen": the boss step runs.
func TestRemoteTeamGateRemembersHostileSightings(t *testing.T) {
	plan := remotePlan(Leg{Name: "baelog", To: Point{40, 0, 0}, Boss: &Boss{Entry: 6906, Position: Point{300, 0, 0}, Team: TeamHorde}})
	o := opts()
	o.Team = "alliance"
	g := newFakeRemote(t)
	m := g.addMob(6906, Point{300, 0, 0})
	g.sight = 50
	g.onTick = func(g *fakeRemote) {
		if x := g.lead().X; x >= 10 && x < 30 {
			m.pos = Point{60, 0, 0} // in sight for a moment while the party walks by
		} else {
			m.pos = Point{300, 0, 0}
		}
	}
	rep, err := runRemote(t, g, plan, o, nil)
	if err != nil || g.mobsKilled != 1 || !strings.Contains(notesOf(rep), "hostile to the party: fought") || strings.Contains(notesOf(rep), "alliance: skipped") {
		t.Fatalf("%v killed %d\n%s", err, g.mobsKilled, notesOf(rep))
	}
}

// ---- fix 3: the boss's corpse ----

// unitCarrier is a fakeCarrier whose agent has a unit store.
type unitCarrier struct {
	*fakeCarrier
	units []objects.Unit
}

func (u *unitCarrier) Units() []objects.Unit { return u.units }

func bossCorpse(age int64) objects.Unit {
	return objects.Unit{GUID: 0xF130001AFE000777, Type: "creature", Entry: 6910, X: 52, Y: 1, Z: 0, PosAgeMS: age, MaxHealth: 5000, Dead: true}
}

func runUnitCarrier(t *testing.T, g *unitCarrier, plan *Plan) (Report, error) {
	t.Helper()
	cfg := DefaultConfig()
	cfg.PostFight, cfg.AddsWait = 2*time.Second, 4*time.Second
	d := NewRemoteDriver(plan, g, cfg, objOpts(), g.clock())
	err := d.Run(context.Background(), 0)
	rep, _ := d.Status()
	return rep, err
}

// A boss killed out of the bots' sight is looted from the corpse in the agent's own unit store.
func TestBossLootUsesTheCorpseInTheAgentsStore(t *testing.T) {
	g := &unitCarrier{fakeCarrier: carrierGame(t), units: []objects.Unit{bossCorpse(2000)}}
	g.recipe = combineRecipe(g.fakeCarrier)
	g.bag("Magey")[itMedallion] = 1
	rep, err := runUnitCarrier(t, g, bossPlan(true))
	if err != nil {
		t.Fatalf("%v\n%s", err, notesOf(rep))
	}
	want := fmt.Sprintf("cmd loot %s Magey %d", tbm.GUIDArg(0xF130001AFE000777), itShaft)
	if g.indexOf(want) < 0 {
		t.Fatalf("want %q in %v", want, g.orders)
	}
}

// A store corpse far from the boss's position is not its corpse.
func TestBossLootIgnoresACorpseFarFromTheBoss(t *testing.T) {
	d := lootDriver(t, &unitCarrier{fakeCarrier: carrierGame(t), units: []objects.Unit{func() objects.Unit { u := bossCorpse(10); u.X = 500; return u }()}})
	if _, ok := d.corpseOf(6910, &Point{50, 0, 0}); ok {
		t.Fatal("a corpse 450 yd away counted")
	}
	if c, ok := d.corpseOf(6910, nil); !ok || c.ageMS != 10 {
		t.Fatalf("%+v %v", c, ok)
	}
}

// lootDriver is a driver that has seen one frame, for calling the loot step directly.
func lootDriver(t *testing.T, g *unitCarrier) *RemoteDriver {
	t.Helper()
	d := NewRemoteDriver(bossPlan(true), g, DefaultConfig(), objOpts(), g.clock())
	g.advance(time.Second)
	d.look()
	return d
}

func lootBoss(t *testing.T, g *unitCarrier) error {
	t.Helper()
	d := lootDriver(t, g)
	a := Action{Leg: 0, Label: "leg 0 (r): kill boss 6910", Boss: d.plan.Legs[0].Boss}
	_, err := d.lootBossItem(context.Background(), a, "Magey", d.newObjRec(0, a.Label, "boss loot_item", "Magey"))
	return err
}

// With no corpse GUID anywhere the boss step says the boss died out of sight.
func TestBossLootWithoutAnyCorpseSaysSo(t *testing.T) {
	err := lootBoss(t, &unitCarrier{fakeCarrier: carrierGame(t)})
	if err == nil || !strings.Contains(err.Error(), "died out of sight") || !strings.Contains(err.Error(), "its loot can't be reached") {
		t.Fatalf("%v", err)
	}
}

// The server no longer has a corpse the agent's store still lists: a copy older than 300 s is called stale, a young one just gone.
func TestBossLootCorpseGoneOnTheServer(t *testing.T) {
	for _, tc := range []struct {
		age   int64
		stale bool
	}{{400_000, true}, {5_000, false}} {
		g := &unitCarrier{fakeCarrier: carrierGame(t), units: []objects.Unit{bossCorpse(tc.age)}}
		g.script["loot"] = []tbm.OrderResult{errOrder("cmd loot", "invalid-target")}
		err := lootBoss(t, g)
		if err == nil || !strings.Contains(err.Error(), "gone on the server") || !strings.Contains(err.Error(), "can't be reached") || strings.Contains(err.Error(), "stale") != tc.stale {
			t.Fatalf("age %d: %v", tc.age, err)
		}
	}
}

// ---- fix 4: packs the tank cannot reach ----

// A mob the tank cannot reach from any approach point fails the step with the mob's position; its group is not counted cleared.
func TestRemoteUnreachablePullFailsTheStep(t *testing.T) {
	plan := remotePlan(Leg{Name: "a", To: Point{0, 0, 0}, PullGroups: []PullGroup{{Entries: []uint32{299}, Positions: []Point{{150, 0, 0}}}}})
	g := newFakeRemote(t)
	g.addMob(299, Point{150, 0, 0})
	g.cmdErr["pullback"] = []string{"unreachable", "unreachable", "unreachable", "unreachable"}
	rep, err := runRemote(t, g, plan, opts(), nil)
	if err == nil || !strings.Contains(err.Error(), "is unreachable for the tank") || !strings.Contains(err.Error(), "150 0 0") || !strings.Contains(err.Error(), "not cleared") {
		t.Fatalf("%v\n%s", err, notesOf(rep))
	}
	if n := len(g.ordersOf("cmd pullback")); n != 1+len(unreachableStandoffs) {
		t.Fatalf("pulls %d: %v", n, g.orders)
	}
	if strings.Contains(notesOf(rep), "counted as cleared") || rep.Remote.Unreachable != 1+len(unreachableStandoffs) {
		t.Fatalf("%+v\n%s", rep.Remote, notesOf(rep))
	}
	// the nearer approaches were walks of the lead bot toward the mob
	if g.lead().X < 120 {
		t.Fatalf("the party did not come nearer: lead at %v", g.lead().X)
	}
	if !strings.Contains(notesOf(rep), "unreachable pull to 150.0 0.0 0.0") {
		t.Fatalf("the unreachable pull is not logged with its point:\n%s", notesOf(rep))
	}
}

// Each unreachable move is logged with its target point.
func TestRemoteUnreachableMovesAreLogged(t *testing.T) {
	g := newFakeRemote(t)
	g.moveErrs = []string{"unreachable", "unreachable"}
	rep, err := runRemote(t, g, remotePlan(Leg{Name: "a", To: Point{60, 0, 0}}), opts(), nil)
	if err != nil {
		t.Fatalf("%v", err)
	}
	if rep.Remote.Unreachable != 2 || strings.Count(notesOf(rep), "unreachable move to 60.0 0.0 0.0") != 2 {
		t.Fatalf("%+v\n%s", rep.Remote, notesOf(rep))
	}
}

// ---- fix 5: a commander that is already bound ----

// The same lead bot and bots: the binding is reused, the run goes on and unbinds at its end.
func TestRemoteReusesTheSameBinding(t *testing.T) {
	g := newFakeRemote(t)
	g.bindErr, g.bound = "already-bound", true
	rep, err := runRemote(t, g, remotePlan(Leg{Name: "a", To: Point{30, 0, 0}}), opts(), nil)
	if err != nil || rep.State != "done" || !rep.Remote.Bound || !rep.Remote.Unbound {
		t.Fatalf("%v %+v\n%s", err, rep.Remote, notesOf(rep))
	}
	if !strings.Contains(notesOf(rep), "already bound to Tanky with 3 bots, the same group; reusing the binding") || g.unbinds != 1 || g.indexOf("commander status") != 1 {
		t.Fatalf("%v\n%s", g.orders, notesOf(rep))
	}
	if len(g.ordersOf("cmd move")) == 0 {
		t.Fatalf("the run did nothing: %v", g.orders)
	}
}

// Another lead bot, another number of bots, or another party in the frame: refused with a message that says so, the binding is left alone.
func TestRemoteRefusesAnotherBinding(t *testing.T) {
	plan := remotePlan(Leg{Name: "a", To: Point{30, 0, 0}})
	for name, tc := range map[string]struct {
		status *tbm.Ack
		bots   []string
		want   string
	}{
		"other lead": {&tbm.Ack{Intent: "commander status", Scope: "party", Count: 3, Executor: "Someone"}, []string{"Healy", "Magey"}, "lead bot Someone with 3 bots, not Tanky with Healy, Magey"},
		"other size": {&tbm.Ack{Intent: "commander status", Scope: "party", Count: 5, Executor: "Tanky"}, []string{"Healy", "Magey"}, "lead bot Tanky with 5 bots"},
		"other bots": {&tbm.Ack{Intent: "commander status", Scope: "party", Count: 3, Executor: "Tanky"}, []string{"Healy", "Priesty"}, "does not hold Priesty"},
	} {
		g := newFakeRemote(t)
		g.bindErr, g.bound, g.status = "already-bound", true, tc.status
		o := opts()
		o.Bots = tc.bots
		rep, err := runRemote(t, g, plan, o, nil)
		if err == nil || !strings.Contains(err.Error(), "already bound to a") || !strings.Contains(err.Error(), tc.want) || !strings.Contains(err.Error(), "unbind it first") {
			t.Fatalf("%s: %v", name, err)
		}
		if g.unbinds != 0 || !g.bound || rep.Remote.Bound || len(g.ordersOf("cmd")) != 0 {
			t.Fatalf("%s: the other binding was touched: unbinds %d bound %v %v", name, g.unbinds, g.bound, g.orders)
		}
	}
}

// ---- the look before a team-gated part is skipped ----

// A gated boss that stands out of the bots' sight, hostile, is not skipped for not being in the feed: the lead bot walks up to look, the
// dwarves show themselves, and they are fought.
func TestRemoteTeamGateLooksBeforeSkippingAnOutOfViewHostile(t *testing.T) {
	plan := remotePlan(Leg{Name: "baelog", To: Point{20, 0, 0}, Boss: &Boss{Entry: 6906, Position: Point{200, 0, 0}, Team: TeamHorde}})
	o := opts()
	o.Team = "alliance"
	g := newFakeRemote(t)
	g.addMob(6906, Point{200, 0, 0})
	g.sight = 50
	rep, err := runRemote(t, g, plan, o, nil)
	if err != nil {
		t.Fatalf("%v\n%s", err, notesOf(rep))
	}
	notes := notesOf(rep)
	if g.mobsKilled != 1 || !strings.Contains(notes, "hostile to the party (seen on the look): fought") || strings.Contains(notes, "alliance: skipped") {
		t.Fatalf("killed %d\n%s", g.mobsKilled, notes)
	}
	// the same for a gated pull group
	plan = remotePlan(Leg{Name: "p", To: Point{20, 0, 0}, PullGroups: []PullGroup{{Entries: []uint32{1}, Positions: []Point{{200, 0, 0}}, Team: TeamHorde}}})
	g = newFakeRemote(t)
	g.addMob(1, Point{200, 0, 0})
	g.sight = 50
	rep, err = runRemote(t, g, plan, o, nil)
	if err != nil || g.mobsKilled != 1 || !strings.Contains(notesOf(rep), "fought") {
		t.Fatalf("%v killed %d\n%s", err, g.mobsKilled, notesOf(rep))
	}
}

// With nothing there, the gated part is skipped only after the look: the lead bot went to its position and saw none.
func TestRemoteTeamGateSkipsAfterTheLook(t *testing.T) {
	plan := remotePlan(Leg{Name: "baelog", To: Point{20, 0, 0}, Boss: &Boss{Entry: 6906, Position: Point{200, 0, 0}, Team: TeamHorde}})
	o := opts()
	o.Team = "alliance"
	g := newFakeRemote(t)
	rep, err := runRemote(t, g, plan, o, nil)
	if err != nil || len(g.ordersOf("cmd pullback")) != 0 {
		t.Fatalf("%v %v", err, g.orders)
	}
	if !strings.Contains(notesOf(rep), "boss 6906 is for team horde, the leader is alliance: skipped (looked:") || g.lead().X < 170 {
		t.Fatalf("lead at %v\n%s", g.lead().X, notesOf(rep))
	}
}

// "Seen alive and not killed" is per unit: one of two spawns of an entry killed does not clear the other.
func TestHostileSeenIsPerGUID(t *testing.T) {
	d := NewRemoteDriver(remotePlan(), newFakeRemote(t), DefaultConfig(), opts(), RealClock())
	a, b := packet.GUID(0xF130000000000001), packet.GUID(0xF130000000000002)
	d.seenHostile = map[packet.GUID]uint32{a: 6907, b: 6907}
	if !d.hostileSeen([]uint32{6907}) || d.hostileSeen([]uint32{6908}) {
		t.Fatal("seen units not counted")
	}
	d.killedGUID = map[packet.GUID]bool{a: true}
	if !d.hostileSeen([]uint32{6907}) {
		t.Fatal("one spawn killed cleared the other")
	}
	d.ledger = map[packet.GUID]*ledgerMob{b: {dead: true}}
	if d.hostileSeen([]uint32{6907}) {
		t.Fatal("both dead still counted")
	}
}

// A binding that turns out not to be the run's has the view that the run switched on put back off; one refused on status never got it on.
func TestRemoteMismatchTurnsTheViewBackOff(t *testing.T) {
	plan := remotePlan(Leg{Name: "a", To: Point{30, 0, 0}})
	g := newFakeRemote(t)
	g.bindErr, g.bound = "already-bound", true
	o := opts()
	o.Bots = []string{"Healy", "Priesty"}
	if _, err := runRemote(t, g, plan, o, nil); err == nil {
		t.Fatal("run went on")
	}
	if on, off := g.ordersOf("commander view on"), g.ordersOf("commander view off"); len(on) != 1 || len(off) != 1 || g.indexOf("commander view off") < g.indexOf("commander view on") {
		t.Fatalf("%v", g.orders)
	}
	g = newFakeRemote(t)
	g.bindErr, g.bound = "already-bound", true
	g.status = &tbm.Ack{Intent: "commander status", Scope: "party", Count: 3, Executor: "Someone"}
	if _, err := runRemote(t, g, plan, opts(), nil); err == nil || len(g.ordersOf("commander view")) != 0 {
		t.Fatalf("%v %v", err, g.orders)
	}
}

// The party wipes while the end of a run fights on: the fight loop gives up on the wipe, and the stay and the unbind still go.
func TestRemoteEndFightWipe(t *testing.T) {
	g := newFakeRemote(t)
	plan := remotePlan(Leg{Name: "far", To: Point{400, 0, 0}})
	ctx, cancel := context.WithCancel(context.Background())
	started, ticks := false, 0
	g.killRate = 0
	g.onTick = func(g *fakeRemote) {
		if !started && g.lead().X > 100 {
			started = true
			m := g.addMob(299, Point{g.lead().X + 5, 0, 0})
			m.pulled, m.engaged, m.pullAt = true, true, g.now.Add(-5*time.Second)
			cancel()
			return
		}
		if started {
			if ticks++; ticks == 3 {
				for _, p := range g.party {
					p.Dead, p.HP, p.State = true, 0, "d"
				}
			}
		}
	}
	rep, _ := runRemoteCtx(t, ctx, g, plan, opts())
	n := len(g.orders)
	if g.orders[n-1] != "commander unbind" || g.orders[n-2] != "cmd stay" || g.unbinds != 1 {
		t.Fatalf("%v", g.orders[max(0, n-4):])
	}
	if !strings.HasPrefix(rep.Remote.EndFight, "interrupted") || !strings.Contains(rep.Remote.EndFight, "dead") {
		t.Fatalf("end fight %q", rep.Remote.EndFight)
	}
}
