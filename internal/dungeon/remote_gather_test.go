package dungeon

import (
	"strings"
	"testing"
	"time"

	"github.com/pjerra/turtle-agent/internal/packet"
	"github.com/pjerra/turtle-agent/internal/tbm"
)

// ZF-1 (2026-10-14): the hunter stood 35 yd back, drinking, when the party went at the pack. A pull now waits (bounded) for every living
// bot to stand within GatherNear yd of the lead bot.
func TestRemoteStrungOutPartyGathersBeforeThePull(t *testing.T) {
	g := newFakeRemote(t)
	g.aggro = 10
	m := g.addMob(299, Point{40, 0, 0})
	// Magey stands 30 yd behind (inside the rest gate's GatherRadius of 40) and only comes up 6 s into the run.
	g.party[2].X = -30
	released := g.now.Add(6 * time.Second)
	g.lagUntil = map[packet.GUID]time.Time{gMage: released}
	plan := remotePlan(Leg{Name: "first", To: Point{0, 0, 0}, PullGroups: []PullGroup{{Entries: []uint32{299}, Positions: []Point{m.pos}}}})
	rep, err := runRemote(t, g, plan, opts(), nil)
	if err != nil || rep.State != "done" {
		t.Fatalf("%v %s\n%s", err, rep.State, notesOf(rep))
	}
	i := g.indexOf("cmd pullback")
	if i < 0 {
		t.Fatalf("no pull: %v", g.orders)
	}
	if g.orderAt[i].Before(released) {
		t.Fatalf("the pull went out at %v, before the mage came up at %v", g.orderAt[i].Sub(released), released)
	}
	if r := rep.Remote; r == nil || r.Gathers < 1 || r.GatherTimeouts != 0 || r.WalkAggros != 0 {
		t.Fatalf("remote %+v", rep.Remote)
	}
}

// A bot that never comes up does not hold the pull forever: the gather wait has a ceiling, and the run goes on with a note.
func TestRemoteGatherWaitHasACeiling(t *testing.T) {
	g := newFakeRemote(t)
	m := g.addMob(299, Point{40, 0, 0})
	g.party[2].X = -30
	g.lagUntil = map[packet.GUID]time.Time{gMage: g.now.Add(time.Hour)}
	plan := remotePlan(Leg{Name: "first", To: Point{0, 0, 0}, PullGroups: []PullGroup{{Entries: []uint32{299}, Positions: []Point{m.pos}}}})
	began := g.now
	rep, err := runRemote(t, g, plan, opts(), nil)
	if err != nil || rep.State != "done" {
		t.Fatalf("%v %s\n%s", err, rep.State, notesOf(rep))
	}
	if r := rep.Remote; r == nil || r.GatherTimeouts < 1 || !strings.Contains(notesOf(rep), "did not gather") {
		t.Fatalf("remote %+v\n%s", rep.Remote, notesOf(rep))
	}
	if el := g.now.Sub(began); el > 5*time.Minute {
		t.Fatalf("the run took %v", el)
	}
}

// ZF-1: a walk went within aggro range of a pack the bots had not been sent at, and seven mobs came. A pack the bots see near the next
// hop is pulled to the gathered party before the hop, and the walk goes on after the fight: no aggro on the walk.
func TestRemotePackNearTheRouteIsPulledNotWalkedInto(t *testing.T) {
	g := newFakeRemote(t)
	g.aggro = 12
	m := g.addMob(77, Point{60, 8, 0}) // in no pull group: on the walk of the leg
	plan := remotePlan(Leg{Name: "first", To: Point{120, 0, 0}})
	rep, err := runRemote(t, g, plan, opts(), nil)
	if err != nil || rep.State != "done" {
		t.Fatalf("%v %s\n%s", err, rep.State, notesOf(rep))
	}
	if m.aggroed {
		t.Fatalf("the party walked into the pack: %v", g.orders)
	}
	if g.mobsKilled != 1 || g.indexOf("cmd pullback "+tbm.GUIDArg(m.guid)) < 0 {
		t.Fatalf("killed %d, orders %v", g.mobsKilled, g.orders)
	}
	if r := rep.Remote; r == nil || r.AheadPulls != 1 || r.WalkAggros != 0 {
		t.Fatalf("remote %+v", rep.Remote)
	}
	// Every move of the walk is a short hop.
	for i, to := range g.moveOrderDest {
		if d := dist3(g.moveOrderPos[i], to); d > float64(DefaultRemoteOptions().CarefulHop)+0.5 {
			t.Fatalf("move %d from %v to %v is %.0f yd", i, g.moveOrderPos[i], to, d)
		}
	}
}

// The look at a group the bots do not see stops out of its aggro range (ZF-1: 15 yd short of it pulled it and two more packs), and the
// pack is pulled when it shows.
func TestRemoteLookStopsOutOfAggroRange(t *testing.T) {
	g := newFakeRemote(t)
	g.aggro = 18
	g.sight = 25 // the bots see the pack only from 25 yd
	m := g.addMob(299, Point{100, 0, 0})
	plan := remotePlan(Leg{Name: "first", To: Point{0, 0, 0}, PullGroups: []PullGroup{{Entries: []uint32{299}, Positions: []Point{m.pos}}}})
	rep, err := runRemote(t, g, plan, opts(), nil)
	if err != nil || rep.State != "done" {
		t.Fatalf("%v %s\n%s", err, rep.State, notesOf(rep))
	}
	if m.aggroed || g.mobsKilled != 1 {
		t.Fatalf("aggroed %v, killed %d\n%s", m.aggroed, g.mobsKilled, notesOf(rep))
	}
}

// ZF-1: after four of five bots died the driver walked the lone tank back into the pack. Three dead stop the leg as a wipe; nothing is
// sent at the pack with the survivors.
func TestRemoteHalfThePartyDeadStopsTheLeg(t *testing.T) {
	g := newFakeRemote(t)
	for i, n := range []string{"Roguey", "Huntey"} {
		g.party = append(g.party, &tbm.PartyRec{GUID: gMage + 1 + packet.GUID(i), Name: n, HP: 1000, MaxHP: 1000, Power: 100, MaxPower: 100, PowerType: 3, State: "n"})
	}
	g.killRate = 1 // a long fight
	m := g.addMob(299, Point{40, 0, 0})
	g.addMob(299, Point{140, 0, 0})
	var diedAt time.Time
	g.onTick = func(g *fakeRemote) {
		if m.engaged && diedAt.IsZero() && g.now.Sub(m.pullAt) > 6*time.Second {
			for _, p := range g.party[1:4] {
				p.Dead, p.HP = true, 0
			}
			diedAt = g.now
		}
	}
	plan := remotePlan(Leg{Name: "first", To: Point{0, 0, 0}, PullGroups: []PullGroup{{Entries: []uint32{299}, Positions: []Point{{40, 0, 0}}}, {Entries: []uint32{299}, Positions: []Point{{140, 0, 0}}}}})
	o := opts()
	o.Bots = append(o.Bots, "Roguey", "Huntey")
	rep, err := runRemote(t, g, plan, o, nil)
	if err == nil || !strings.Contains(err.Error(), "wipe") || rep.PartyDowns != 1 || rep.Wipes != 0 {
		t.Fatalf("%v wipes %d party downs %d\n%s", err, rep.Wipes, rep.PartyDowns, notesOf(rep))
	}
	if !strings.Contains(notesOf(rep), "3 of 5 bots are dead") {
		t.Fatalf("notes\n%s", notesOf(rep))
	}
	for i, o := range g.orders {
		if g.orderAt[i].After(diedAt) && (strings.HasPrefix(o, "cmd move") || strings.HasPrefix(o, "cmd pullback")) {
			t.Fatalf("order %q sent %v after the deaths", o, g.orderAt[i].Sub(diedAt))
		}
	}
}

// With recover_wipe the three dead are revived (all of them, before anything else is sent at a pack) and the step is tried again with the
// whole party.
func TestRemoteHalfThePartyDeadIsRecoveredWithRecoverWipe(t *testing.T) {
	g := newFakeRemote(t)
	for i, n := range []string{"Roguey", "Huntey"} {
		g.party = append(g.party, &tbm.PartyRec{GUID: gMage + 1 + packet.GUID(i), Name: n, HP: 1000, MaxHP: 1000, Power: 100, MaxPower: 100, PowerType: 3, State: "n"})
	}
	m := g.addMob(299, Point{40, 0, 0})
	died := false
	g.onTick = func(g *fakeRemote) {
		if m.engaged && !died && g.now.Sub(m.pullAt) > 3*time.Second {
			died = true
			for _, p := range g.party[1:4] {
				p.Dead, p.HP = true, 0
			}
		}
	}
	plan := remotePlan(Leg{Name: "first", To: Point{0, 0, 0}, PullGroups: []PullGroup{{Entries: []uint32{299}, Positions: []Point{{40, 0, 0}}}}})
	o := opts()
	o.Bots = append(o.Bots, "Roguey", "Huntey")
	o.RezWait = -1
	rep, err := runRemote(t, g, plan, o, func(c *Config) { c.RecoverWipe = true })
	if err != nil || rep.State != "done" || rep.PartyDowns != 1 || rep.Wipes != 0 || len(g.revived) != 3 {
		t.Fatalf("%v %s wipes %d revived %v\n%s", err, rep.State, rep.Wipes, g.revived, notesOf(rep))
	}
}

// A bot that cannot come (stuck, no path) costs one gather wait in its leg, not one at every hop and pull: after a wait in which it got
// no closer the gathers stop waiting for it, and wait for it again once it is back by itself.
func TestRemoteStuckBotCostsOneGatherWaitPerLeg(t *testing.T) {
	g := newFakeRemote(t)
	g.addMob(299, Point{40, 0, 0})
	g.addMob(299, Point{140, 0, 0})
	g.party[2].X = -30
	g.lagUntil = map[packet.GUID]time.Time{gMage: g.now.Add(25 * time.Second)}
	plan := remotePlan(Leg{Name: "first", To: Point{0, 0, 0}, PullGroups: []PullGroup{{Entries: []uint32{299}, Positions: []Point{{40, 0, 0}}}, {Entries: []uint32{299}, Positions: []Point{{140, 0, 0}}}}})
	rep, err := runRemote(t, g, plan, opts(), nil)
	if err != nil || rep.State != "done" {
		t.Fatalf("%v %s\n%s", err, rep.State, notesOf(rep))
	}
	if r := rep.Remote; r.GatherTimeouts != 1 || r.GatherGaveUp != 1 || r.GatherWaitS > 15 {
		t.Fatalf("gathers %d timeouts %d gave up %d waited %.0f s\n%s", r.Gathers, r.GatherTimeouts, r.GatherGaveUp, r.GatherWaitS, notesOf(rep))
	}
	if !strings.Contains(notesOf(rep), "Magey came no closer") || !strings.Contains(notesOf(rep), "Magey is back with the party") {
		t.Fatalf("%s", notesOf(rep))
	}
}

// A mob a pull on the way drew no fight from is not pulled again on the next walk of the leg (and nothing counts it cleared).
func TestRemoteNoFightMobIsPassedOverForTheLeg(t *testing.T) {
	g := newFakeRemote(t)
	g.sight = 50
	m := g.addMob(77, Point{60, 8, 0}) // in no pull group, and a pull at it draws nothing
	m.inert = true
	g.addMob(299, Point{0, 0, 0})
	plan := remotePlan(Leg{Name: "first", To: Point{100, 0, 0}, PullGroups: []PullGroup{{Entries: []uint32{299}, Positions: []Point{{0, 0, 0}}}}})
	rep, err := runRemote(t, g, plan, opts(), nil)
	if err != nil || rep.State != "done" {
		t.Fatalf("%v %s\n%s", err, rep.State, notesOf(rep))
	}
	if n := len(g.ordersOf("cmd pullback " + tbm.GUIDArg(m.guid))); n != 1 {
		t.Fatalf("%d pulls at the mob that drew no fight: %v", n, g.orders)
	}
	if g.lead().X > 45 {
		t.Fatalf("the second walk did not happen: lead at %v", g.lead().X)
	}
}

// A hostile on another floor (20 yd below the walk) is not in its way: it is not pulled.
func TestRemoteMobOnAnotherFloorIsNotPulled(t *testing.T) {
	g := newFakeRemote(t)
	m := g.addMob(77, Point{60, 0, -20})
	rep, err := runRemote(t, g, remotePlan(Leg{Name: "first", To: Point{120, 0, 0}}), opts(), nil)
	if err != nil || rep.State != "done" || g.indexOf("cmd pullback "+tbm.GUIDArg(m.guid)) >= 0 || rep.Remote.AheadPulls != 0 {
		t.Fatalf("%v %s ahead %d %v", err, rep.State, rep.Remote.AheadPulls, g.orders)
	}
}

// A later leg's boss (Archaedas, a Stone Keeper) standing near this leg's walk is the plan's, not the walk's: it is not pulled early.
func TestRemoteLaterLegsBossNearTheRouteIsNotPulled(t *testing.T) {
	g := newFakeRemote(t)
	g.addMob(900, Point{60, 5, 0})
	plan := remotePlan(Leg{Name: "first", To: Point{120, 0, 0}}, Leg{Name: "second", To: Point{100, 0, 0}, Boss: &Boss{Entry: 900, Position: Point{60, 5, 0}}})
	rep, err := runRemote(t, g, plan, opts(), nil)
	if err != nil || rep.State != "done" || g.mobsKilled != 1 {
		t.Fatalf("%v %s\n%s", err, rep.State, notesOf(rep))
	}
	if rep.Remote.AheadPulls != 0 || strings.Contains(notesOf(rep), "next hop") {
		t.Fatalf("pulled on the way: %+v\n%s", rep.Remote, notesOf(rep))
	}
	if i := g.indexOf("cmd pullback"); i < 0 || g.moveOrderPos[0].X > 1 || g.trace[len(g.trace)-1].X < 0 {
		t.Fatalf("orders %v", g.orders)
	}
}

// The rest gate (40 yd) does not wait its full time for a bot the leg's gathers gave up on: ZF-1's stuck-bot case cost 60 s at the gate.
func TestRemoteRestGateSkipsABotTheGathersGaveUpOn(t *testing.T) {
	g := newFakeRemote(t)
	g.addMob(299, Point{40, 0, 0})
	g.addMob(299, Point{140, 0, 0})
	g.party[2].X = -30
	g.lagUntil = map[packet.GUID]time.Time{gMage: g.now.Add(time.Hour)}
	plan := remotePlan(Leg{Name: "first", To: Point{0, 0, 0}, PullGroups: []PullGroup{{Entries: []uint32{299}, Positions: []Point{{40, 0, 0}}}, {Entries: []uint32{299}, Positions: []Point{{140, 0, 0}}}}})
	rep, err := runRemote(t, g, plan, opts(), nil)
	if err != nil || rep.State != "done" {
		t.Fatalf("%v %s\n%s", err, rep.State, notesOf(rep))
	}
	notes := notesOf(rep)
	if strings.Contains(notes, "rest gate gave up") || !strings.Contains(notes, "rest gate: Magey is not near the lead bot, but the gathers of this leg gave up on it") {
		t.Fatalf("%s", notes)
	}
	if rep.Seconds > 90 {
		t.Fatalf("the run took %.0f s", rep.Seconds)
	}
}

// A direct via point bridges a gap the navmesh does not: it stays one `cmd move ... direct`, never split into the careful walk's hops.
func TestRemoteDirectStepIsNotHopped(t *testing.T) {
	g := newFakeRemote(t)
	o := opts()
	o.CarefulHop = 5 // shorter than the 15 yd step
	rep, err := runRemote(t, g, viaRemotePlan(), o, nil)
	if err != nil {
		t.Fatalf("%v %+v", err, rep.Notes)
	}
	var direct []string
	for _, m := range g.ordersOf("cmd move") {
		if strings.HasSuffix(m, "direct") {
			direct = append(direct, m)
		}
	}
	if len(direct) != 1 || direct[0] != "cmd move 55.00 0.00 4.00 direct" {
		t.Fatalf("direct moves %v (all %v)", direct, g.ordersOf("cmd move"))
	}
	if len(g.ordersOf("cmd move")) < 6 { // 40 yd to the lip in hops of 5
		t.Fatalf("the plain walk to the lip was not hopped: %v", g.ordersOf("cmd move"))
	}
}

// TortoiseBots #943: a walk pulls only hostile units. A neutral mob and a critter by the route are left alone; a neutral that comes at
// the party (it targets the tank) is fought all the same.
func TestRemoteWalkPullsOnlyHostiles(t *testing.T) {
	g := newFakeRemote(t)
	neutral := g.addMob(77, Point{50, 8, 0})
	neutral.reaction, neutral.kind = "n", "-"
	critter := g.addMob(78, Point{70, -6, 0})
	critter.reaction, critter.kind = "h", "k"
	hostile := g.addMob(79, Point{100, 6, 0})
	hostile.reaction, hostile.kind = "h", "-"
	rep, err := runRemote(t, g, remotePlan(Leg{Name: "first", To: Point{120, 0, 0}}), opts(), nil)
	if err != nil || rep.State != "done" {
		t.Fatalf("%v %s\n%s", err, rep.State, notesOf(rep))
	}
	for _, m := range []*rmob{neutral, critter} {
		if g.indexOf("cmd pullback "+tbm.GUIDArg(m.guid)) >= 0 {
			t.Fatalf("pulled %d (%s,%s): %v", m.entry, m.reaction, m.kind, g.orders)
		}
	}
	if g.indexOf("cmd pullback "+tbm.GUIDArg(hostile.guid)) < 0 || rep.Remote.AheadPulls != 1 {
		t.Fatalf("the hostile was not pulled: ahead %d %v", rep.Remote.AheadPulls, g.orders)
	}

	g = newFakeRemote(t)
	angry := g.addMob(77, Point{50, 8, 0})
	angry.reaction, angry.kind = "n", "-"
	g.aggro = 15 // it comes at the party when the walk passes
	rep, err = runRemote(t, g, remotePlan(Leg{Name: "first", To: Point{120, 0, 0}}), opts(), nil)
	if err != nil || rep.State != "done" || g.mobsKilled != 1 || rep.Remote.WalkAggros != 1 {
		t.Fatalf("a neutral attacking the party was not fought: %v killed %d walk aggros %d\n%s", err, g.mobsKilled, rep.Remote.WalkAggros, notesOf(rep))
	}
}

// ZF hires-2 run 6: the walk of leg 0 stopped (and gathered) 15 yd from a room of Velratha's packs of leg 2, which the walk took for the
// plan's and left alone; ten mobs came. A pack of another leg standing in this leg's way is pulled before the hop like an unlisted one.
func TestRemoteLaterLegsPackNearTheRouteIsPulled(t *testing.T) {
	g := newFakeRemote(t)
	g.aggro = 12
	m := g.addMob(77, Point{60, 8, 0})
	plan := remotePlan(Leg{Name: "first", To: Point{120, 0, 0}},
		Leg{Name: "second", To: Point{100, 0, 0}, PullGroups: []PullGroup{{Entries: []uint32{77}, Positions: []Point{{60, 8, 0}}}}})
	rep, err := runRemote(t, g, plan, opts(), nil)
	if err != nil || rep.State != "done" {
		t.Fatalf("%v %s\n%s", err, rep.State, notesOf(rep))
	}
	if m.aggroed || g.indexOf("cmd pullback "+tbm.GUIDArg(m.guid)) < 0 || rep.Remote.AheadPulls != 1 {
		t.Fatalf("aggroed %v ahead %d orders %v", m.aggroed, rep.Remote.AheadPulls, g.orders)
	}
}

// A hostile 27 yd off the route is out of AggroGuard (22) of the hops but within GatherClear (30) of a hop's end, where the party
// would stop and gather: it is pulled first.
func TestRemoteGatherSpotKeepsClearOfAPack(t *testing.T) {
	g := newFakeRemote(t)
	g.aggro = 10
	m := g.addMob(77, Point{50, 27, 0})
	rep, err := runRemote(t, g, remotePlan(Leg{Name: "first", To: Point{120, 0, 0}}), opts(), nil)
	if err != nil || rep.State != "done" {
		t.Fatalf("%v %s\n%s", err, rep.State, notesOf(rep))
	}
	if g.indexOf("cmd pullback "+tbm.GUIDArg(m.guid)) < 0 || rep.Remote.AheadPulls != 1 {
		t.Fatalf("ahead %d orders %v", rep.Remote.AheadPulls, g.orders)
	}
}
