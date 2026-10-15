package dungeon

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/pjerra/turtle-agent/internal/packet"
	"github.com/pjerra/turtle-agent/internal/tbm"
)

// kill_first (issue #190): the plan's healers and the summoned healing ward are killed before the rest of the fight.

func entriesOf(hs []tbm.HostileRec) string {
	var out []uint32
	for _, h := range hs {
		out = append(out, h.Entry)
	}
	return fmt.Sprint(out)
}

func TestPlanReadsKillFirst(t *testing.T) {
	p, err := Parse("t.json", []byte(`{"version":1,"name":"kf","map":1,"entrance":{"x":0,"y":0,"z":0},"cc_priority":[5650],"kill_first":[8179,5650,7247],"legs":[{"name":"a","to":{"x":1,"y":0,"z":0},"boss":{"entry":5,"position":{"x":5,"y":0,"z":0}}}]}`))
	if err != nil {
		t.Fatal(err)
	}
	if fmt.Sprint(p.KillFirst) != "[8179 5650 7247]" {
		t.Fatalf("kill_first %v", p.KillFirst)
	}
}

func TestKillFirstEntriesComeFirstInTheListsOrder(t *testing.T) {
	d := ledgerDriver(t)
	d.plan.KillFirst = []uint32{8179, 5650, 7247}
	mk := func(guid uint64, entry uint32) tbm.HostileRec {
		return tbm.HostileRec{GUID: packet.GUID(0x100 + guid), Entry: entry, HPPct: 100, Flags: "-"}
	}
	// rankByTank's order: the tank's mob 299 first, then 7247, the ward 8179 last.
	in := []tbm.HostileRec{mk(1, 299), mk(2, 7247), mk(3, 300), mk(4, 5650), mk(5, 8179)}
	if got := entriesOf(d.killFirstFirst(in)); got != "[8179 5650 7247 299 300]" {
		t.Fatalf("order %s", got)
	}
	if got := entriesOf(in); got != "[299 7247 300 5650 8179]" {
		t.Fatalf("the input changed: %s", got)
	}
	d.plan.KillFirst = nil
	if got := entriesOf(d.killFirstFirst(in)); got != "[299 7247 300 5650 8179]" {
		t.Fatalf("no kill_first, no change: %s", got)
	}
}

func TestKillFirstLeavesAMobUnderCCAlone(t *testing.T) {
	d := ledgerDriver(t)
	d.plan.KillFirst = []uint32{5650, 7247}
	sheep := tbm.HostileRec{GUID: 0x201, Entry: 5650, HPPct: 100, Flags: "c"}
	d.tf.init()
	d.tf.ccHeld[sheep.GUID] = true // the cc order was accepted
	other := tbm.HostileRec{GUID: 0x202, Entry: 299, HPPct: 100, Flags: "-"}
	soul := tbm.HostileRec{GUID: 0x203, Entry: 7247, HPPct: 100, Flags: "-"}
	if got := entriesOf(d.killFirstFirst([]tbm.HostileRec{other, sheep, soul})); got != "[7247 299 5650]" {
		t.Fatalf("order %s", got)
	}
}

func TestSummonedKillFirstIsAWardNotAPlannedMob(t *testing.T) {
	d := ledgerDriver(t)
	d.plan.KillFirst = []uint32{8179, 5650}
	d.plan.Legs[0].PullGroups = []PullGroup{{Entries: []uint32{5650, 299}, Positions: []Point{{30, 0, 0}}}}
	if !d.summonedKillFirst(tbm.HostileRec{Entry: 8179}) || d.summonedKillFirst(tbm.HostileRec{Entry: 5650}) || d.summonedKillFirst(tbm.HostileRec{Entry: 299}) {
		t.Fatal("only 8179 is summoned")
	}
}

func killFirstFight(t *testing.T, kf []uint32, mod func(g *fakeRemote)) (*fakeRemote, []*rmob, Report) {
	t.Helper()
	g := newFakeRemote(t)
	a := g.addMob(299, Point{130, 0, 0})  // nearer to the lead bot
	b := g.addMob(300, Point{130, 40, 0}) // the healer
	g.sight = 20
	if mod != nil {
		mod(g)
	}
	inner := g.onTick
	g.onTick = func(g *fakeRemote) {
		if g.lead().X > 90 {
			for _, m := range g.mobs {
				if !m.engaged && !m.noTarget {
					m.pulled, m.pullAt, m.engaged = true, g.now, true
				}
			}
		}
		if inner != nil {
			inner(g)
		}
	}
	o := opts()
	o.Tank = "Tanky"
	plan := remotePlan(Leg{Name: "a", To: Point{300, 0, 0}})
	plan.KillFirst = kf
	rep, err := runRemoteTx(t, g, plan, o, nil)
	if err != nil {
		t.Fatalf("%v %v", err, rep.Notes)
	}
	return g, []*rmob{a, b}, rep
}

func TestRemotePartyKillsAKillFirstMobBeforeTheNearerOne(t *testing.T) {
	g, mobs, _ := killFirstFight(t, nil, func(g *fakeRemote) { g.ignorePull = true })
	if got := attackGUIDs(g); len(got) == 0 || got[0] != tbm.GUIDArg(mobs[0].guid) {
		t.Fatalf("without kill_first the nearer mob goes first: %v", got)
	}
	g, mobs, _ = killFirstFight(t, []uint32{300}, func(g *fakeRemote) { g.ignorePull = true })
	if got := attackGUIDs(g); len(got) == 0 || got[0] != tbm.GUIDArg(mobs[1].guid) {
		t.Fatalf("with kill_first [300] the healer goes first: %v (a %s b %s)", got, tbm.GUIDArg(mobs[0].guid), tbm.GUIDArg(mobs[1].guid))
	}
}

// The ward hits nobody (no target, no threat list); it is still part of the fight when the plan lists it in kill_first, within wardRangeYd
// of the lead bot, and the party is sent at it, with no threat wait, ahead of the pack the tank holds.
func TestHealingWardJoinsTheFightAndIsAttackedFirst(t *testing.T) {
	g := newFakeRemote(t)
	d := NewRemoteDriver(remotePlan(Leg{Name: "a", To: Point{1, 0, 0}, PullGroups: []PullGroup{{Entries: []uint32{299, 5650}, Positions: []Point{{30, 0, 0}}}}}), g, DefaultConfig(), opts(), g.clock())
	d.plan.KillFirst = []uint32{8179, 5650}
	d.tank = gTank
	mob := tbm.HostileRec{GUID: 0x301, Entry: 299, X: 3, HPPct: 80, Target: gTank, Flags: "-"}
	ward := tbm.HostileRec{GUID: 0x302, Entry: 8179, X: 6, Y: 2, HPPct: 100, Flags: "-"}
	far := tbm.HostileRec{GUID: 0x303, Entry: 8179, X: 90, HPPct: 100, Flags: "-"}
	d.fr = tbm.ViewFrame{Lead: gTank, Party: []tbm.PartyRec{{GUID: gTank, Name: "Tanky"}}, Hostiles: []tbm.HostileRec{mob, ward, far}}
	d.haveFr = true
	d.engagedAt = map[packet.GUID]time.Time{}
	if !d.inFight(ward) || d.inFight(far) || !d.inFight(mob) {
		t.Fatalf("inFight: ward %v far %v mob %v", d.inFight(ward), d.inFight(far), d.inFight(mob))
	}
	d.plan.KillFirst = nil
	if d.inFight(ward) {
		t.Fatal("without kill_first a unit that fights nobody is not part of the fight")
	}
	d.plan.KillFirst = []uint32{8179, 5650}
	sent, err := d.assistSend(context.Background(), []tbm.HostileRec{mob, ward})
	if err != nil || !sent {
		t.Fatalf("sent %v %v", sent, err)
	}
	if want := "cmd attack " + tbm.GUIDArg(ward.GUID); g.orders[len(g.orders)-1] != want {
		t.Fatalf("orders %v, want the last to be %q", g.orders, want)
	}
}

// reactiveCC: the summoned ward is never the CC target, a planned kill_first entry that is in cc_always still is.
func TestRemoteReactiveCCSkipsTheWard(t *testing.T) {
	g := newFakeRemote(t)
	a := g.addMob(299, Point{30, 0, 0})
	ward := g.addMob(8179, Point{90, 0, 0})
	g.killRate = 5
	g.onTick = func(g *fakeRemote) {
		if a.engaged && !ward.pulled {
			ward.pulled, ward.pullAt = true, g.now // it comes in with the fight
		}
	}
	plan := remotePlan(Leg{Name: "a", To: Point{0, 0, 0}, PullGroups: []PullGroup{{Entries: []uint32{299}, Positions: []Point{{30, 0, 0}}}}})
	plan.CCAlways = []uint32{8179} // would be sheeped at once, were it not a summoned kill target
	plan.KillFirst = []uint32{8179}
	o := opts()
	o.CCBot = "Magey"
	rep, err := runRemote(t, g, plan, o, nil)
	if err != nil {
		t.Fatalf("%v %v", err, rep.Notes)
	}
	if rep.Remote.ReactiveCC != 0 || g.indexOf("cmd cc moon "+tbm.GUIDArg(ward.guid)) >= 0 {
		t.Fatalf("the ward was CCed: %+v %v", rep.Remote, g.orders)
	}
}
