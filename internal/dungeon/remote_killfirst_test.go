package dungeon

import (
	"context"
	"fmt"
	"strings"
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

func TestSummonedKillFirstComesFromThePlanField(t *testing.T) {
	d := ledgerDriver(t)
	d.plan.KillFirst = []uint32{8179, 5650}
	if d.summonedKillFirst(tbm.HostileRec{Entry: 8179}) {
		t.Fatal("kill_first alone does not make a unit summoned")
	}
	d.plan.SummonedKillFirst = []uint32{8179}
	if !d.summonedKillFirst(tbm.HostileRec{Entry: 8179}) || d.summonedKillFirst(tbm.HostileRec{Entry: 5650}) || d.summonedKillFirst(tbm.HostileRec{Entry: 299}) {
		t.Fatal("only 8179 is summoned")
	}
}

func TestPlanSummonedKillFirstMustBeInKillFirst(t *testing.T) {
	const head = `{"version":1,"name":"kf","map":1,"entrance":{"x":0,"y":0,"z":0},`
	const legs = `"legs":[{"name":"a","to":{"x":1,"y":0,"z":0},"boss":{"entry":5,"position":{"x":5,"y":0,"z":0}}}]}`
	if p, err := Parse("t.json", []byte(head+`"kill_first":[8179,5650],"summoned_kill_first":[8179],`+legs)); err != nil || fmt.Sprint(p.SummonedKillFirst) != "[8179]" {
		t.Fatalf("%v %v", p, err)
	}
	if _, err := Parse("t.json", []byte(head+`"kill_first":[5650],"summoned_kill_first":[8179],`+legs)); err == nil || !strings.Contains(err.Error(), "summoned_kill_first") {
		t.Fatalf("%v", err)
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

func wardDriver(t *testing.T, hs ...tbm.HostileRec) (*fakeRemote, *RemoteDriver) {
	t.Helper()
	g := newFakeRemote(t)
	d := NewRemoteDriver(remotePlan(Leg{Name: "a", To: Point{1, 0, 0}, PullGroups: []PullGroup{{Entries: []uint32{299, 5650}, Positions: []Point{{30, 0, 0}}}}}), g, DefaultConfig(), opts(), g.clock())
	d.plan.KillFirst = []uint32{8179, 5650, 7247}
	d.plan.SummonedKillFirst = []uint32{8179}
	d.tank = gTank
	d.fr = tbm.ViewFrame{Lead: gTank, Party: []tbm.PartyRec{{GUID: gTank, Name: "Tanky"}}, Hostiles: hs}
	d.haveFr = true
	d.engagedAt = map[packet.GUID]time.Time{}
	return g, d
}

// The ward hits nobody (no target, no threat list). It is part of the fight only with encounter evidence: it stands within 10 yd of a
// hostile that fights the party. A ward by a pack that is not fighting is ignored, and so is a ward the plan does not name as summoned.
func TestHealingWardNeedsEncounterEvidence(t *testing.T) {
	mob := tbm.HostileRec{GUID: 0x301, Entry: 299, X: 3, HPPct: 80, Target: gTank, Flags: "-"}
	ward := tbm.HostileRec{GUID: 0x302, Entry: 8179, X: 6, Y: 2, HPPct: 100, Flags: "-"}
	unrelated := tbm.HostileRec{GUID: 0x303, Entry: 8179, X: 20, Y: 0, HPPct: 100, Flags: "-"} // 17 yd from the fighting mob, 20 from the lead bot
	idlePack := tbm.HostileRec{GUID: 0x304, Entry: 5650, X: 22, Y: 0, HPPct: 100, Flags: "-"}  // a pack that does not fight
	idleWard := tbm.HostileRec{GUID: 0x305, Entry: 8179, X: 24, Y: 0, HPPct: 100, Flags: "-"}  // next to it: no encounter
	_, d := wardDriver(t, mob, ward, unrelated, idlePack, idleWard)
	if !d.inFight(ward) || !d.inFight(mob) {
		t.Fatalf("ward by the fight %v, mob %v", d.inFight(ward), d.inFight(mob))
	}
	if d.inFight(unrelated) || d.inFight(idleWard) || d.inFight(idlePack) {
		t.Fatalf("a ward near an unengaged pack is ignored: unrelated %v idle ward %v idle pack %v", d.inFight(unrelated), d.inFight(idleWard), d.inFight(idlePack))
	}
	d.plan.SummonedKillFirst = nil
	if d.inFight(ward) {
		t.Fatal("a unit the plan does not name as summoned is not inferred into the fight")
	}
}

// kill_first only orders what the fight has: the ward by the fight is sent at first, with no threat wait.
func TestHealingWardIsAttackedFirst(t *testing.T) {
	mob := tbm.HostileRec{GUID: 0x301, Entry: 299, X: 3, HPPct: 80, Target: gTank, Flags: "-"}
	ward := tbm.HostileRec{GUID: 0x302, Entry: 8179, X: 6, Y: 2, HPPct: 100, Flags: "-"}
	g, d := wardDriver(t, mob, ward)
	sent, err := d.assistSend(context.Background(), []tbm.HostileRec{mob, ward})
	if err != nil || !sent {
		t.Fatalf("sent %v %v", sent, err)
	}
	if want := "cmd attack " + tbm.GUIDArg(ward.GUID); g.orders[len(g.orders)-1] != want {
		t.Fatalf("orders %v, want the last to be %q", g.orders, want)
	}
}

// The no-threat shortcut is for summoned units only: a planned healer (5650, 7247) with no target waits for the tank's threat like any mob.
func TestTargetlessPlannedHealerStillWaitsForThreat(t *testing.T) {
	healer := tbm.HostileRec{GUID: 0x401, Entry: 5650, X: 6, HPPct: 100, Flags: "-"} // targets nobody, no tank threat on it
	soul := tbm.HostileRec{GUID: 0x402, Entry: 7247, X: 8, HPPct: 100, Flags: "-"}
	ward := tbm.HostileRec{GUID: 0x403, Entry: 8179, X: 7, HPPct: 100, Flags: "-"}
	_, d := wardDriver(t, healer, soul, ward)
	d.opt.ThreatWait = 30 * time.Second
	d.engagedAt[healer.GUID], d.engagedAt[soul.GUID] = d.clk.Now(), d.clk.Now()
	if d.threatAllows(healer) || d.threatAllows(soul) {
		t.Fatal("a targetless planned healer was sent at with no threat on it")
	}
	if !d.threatAllows(ward) {
		t.Fatal("the summoned ward needs no threat")
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
	plan.KillFirst, plan.SummonedKillFirst = []uint32{8179}, []uint32{8179}
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
