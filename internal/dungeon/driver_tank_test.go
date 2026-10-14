package dungeon

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/pjerra/turtle-agent/internal/objects"
	"github.com/pjerra/turtle-agent/internal/packet"
)

// Group mode, tank first: the fake's two mobs (201 at 20 yd, 202 at 35 yd) are pulled together and fight for dieIn; an attack order
// at one of them kills it 3 s later. The tank's unit row shows the target the test gives it.

const tankPlan = `{"version":1,"name":"t","map":70,"entrance":{"x":0,"y":0,"z":0},"legs":[
 {"to":{"x":0,"y":0,"z":0},"pull_groups":[{"entries":[1,2],"positions":[{"x":20,"y":0,"z":0},{"x":35,"y":0,"z":0}]}]}]}`

// tankFixture is two mobs in one pack, fighting for 60 s unless attacked. It returns the units and a driver.
func tankFixture(t *testing.T) (*fakeGame, *fakeUnit, *fakeUnit) {
	t.Helper()
	g := newFake()
	g.dieIn = 60 * time.Second
	g.partyTarget = map[packet.GUID]packet.GUID{}
	m1 := g.add(201, 1, Point{20, 0, 0})
	m2 := g.add(202, 2, Point{35, 0, 0})
	g.onChat = func(g *fakeGame, text string) {
		if text == ".bot action attack" {
			for _, u := range g.units {
				if u.GUID == g.targeted && !u.Dead {
					u.dieAt = g.now.Add(3 * time.Second)
				}
			}
		}
	}
	return g, m1, m2
}

func runTank(t *testing.T, g *fakeGame) Report {
	t.Helper()
	d := NewDriver(mustPlan(t, tankPlan), g, Config{}, g.clock())
	if err := d.Run(context.Background(), 0); err != nil {
		t.Fatalf("%v\n%+v", err, d.Report())
	}
	return d.Report()
}

func TestGroupTargetsTheMobTheTankHolds(t *testing.T) {
	g, _, _ := tankFixture(t)
	g.partyTarget[tank] = 202 // both mobs hit the tank; the tank's own target is the farther one
	rep := runTank(t, g)
	// The pull targets the nearest (201); the assist goes to 202, not the nearest; once it is dead the party is sent at 201 at once.
	if fmt.Sprint(g.targets) != fmt.Sprint([]packet.GUID{201, 202, 201}) {
		t.Fatalf("targets %v chats %q", g.targets, g.chats)
	}
	if tf := rep.TankFirst; tf == nil || tf.TankThreatShare != 100 || tf.TankThreatSamples == 0 || tf.LooseAddOrders != 0 || tf.TargetSwitches != 0 || tf.Tank != "Tank" {
		t.Fatalf("%+v", tf)
	}
}

func TestGroupSendsTheTankAtALooseAdd(t *testing.T) {
	g, m1, m2 := tankFixture(t)
	g.partyTarget[tank] = 202
	var pullAt []time.Time
	var pullTargets []packet.GUID
	g.onChat = func(g *fakeGame, text string) { // attack orders kill nothing here: the fight lasts its 60 s
		switch text {
		case ".bot action pullback":
			m1.Target, m2.Target = healer, tank // the first mob is on the healer
		case ".bot action pull 0":
			pullAt, pullTargets = append(pullAt, g.now), append(pullTargets, g.targeted)
		}
	}
	rep := runTank(t, g)
	// The mob stays on the healer (the fake tank never takes it): the tank is sent at it three times, never twice within the cooldown.
	if len(pullAt) != maxAddOrders || rep.TankFirst.LooseAddOrders != maxAddOrders {
		t.Fatalf("pull orders at %v: %+v", pullAt, rep.TankFirst)
	}
	for i, at := range pullAt {
		if pullTargets[i] != 201 {
			t.Fatalf("the tank was sent at %v", pullTargets)
		}
		if i > 0 && at.Sub(pullAt[i-1]) < groupAddCooldown {
			t.Fatalf("orders %v apart: %v", at.Sub(pullAt[i-1]), pullAt)
		}
	}
	if sh := rep.TankFirst.TankThreatShare; sh <= 0 || sh >= 100 {
		t.Fatalf("share %v", sh)
	}
	// A refusal of that pull is not the fight's pull being refused: the fight goes on.
	g2, n1, n2 := tankFixture(t)
	g2.partyTarget[tank] = 202
	g2.onChat = func(g *fakeGame, text string) {
		if text == ".bot action pullback" {
			n1.Target, n2.Target = healer, tank
		}
		if text == ".bot action pull 0" {
			g.refusals = append(g.refusals, Refusal{Intent: "pull", Code: "unreachable", Message: "no path"})
		}
	}
	rep2 := runTank(t, g2)
	if rep2.TankFirst.LooseAddOrders == 0 || g2.count(".bot action pullback") != 1 || rep2.Legs[0].Pulls != 1 {
		t.Fatalf("%+v chats %q notes %q", rep2.TankFirst, g2.chats, rep2.Notes)
	}
}

func TestGroupLeavesACrowdControlledMobAlone(t *testing.T) {
	for _, how := range []string{"aura", "name", "moon"} {
		t.Run(how, func(t *testing.T) {
			g, m1, _ := tankFixture(t)
			g.partyTarget[tank] = 202
			switch how {
			case "aura":
				m1.Auras = []objects.Aura{{SpellID: 118}} // Polymorph (mechanic 17)
			case "name":
				m1.Auras = []objects.Aura{{SpellID: 999999, Name: "Sap"}}
			case "moon":
				g.marks = map[string]packet.GUID{"moon": 201}
			}
			rep := runTank(t, g)
			// 201 is nearer and would be the first, but it is spared; it is attacked when it is the last one.
			if fmt.Sprint(g.targets) != fmt.Sprint([]packet.GUID{201, 202, 201}) {
				t.Fatalf("targets %v", g.targets)
			}
			if rep.TankFirst == nil || rep.TankFirst.CCRespected != 1 {
				t.Fatalf("%+v", rep.TankFirst)
			}
			// The first attack order was not at 201: the one targeted before it is 202.
			if g.targets[1] != 202 {
				t.Fatalf("targets %v", g.targets)
			}
		})
	}
}

func TestGroupSwitchesToTheMobTheTankHoldsBetter(t *testing.T) {
	g, _, m2 := tankFixture(t)
	g.partyTarget[tank] = 202
	g.onTick = func(g *fakeGame) {
		// 12 s into the fight the second mob turns on the healer and the tank picks the first.
		if m2.InCombat && g.now.After(time.Unix(1_000_000, 0).Add(12*time.Second)) && m2.Target != healer && !m2.Dead {
			m2.Target = healer
			g.partyTarget[tank] = 201
		}
	}
	// attacks must not kill: the fake's attack hook shortens a mob's life, so here it only records
	g.onChat = func(g *fakeGame, text string) {}
	rep := runTank(t, g)
	if rep.TankFirst == nil || rep.TankFirst.TargetSwitches < 1 {
		t.Fatalf("%+v targets %v", rep.TankFirst, g.targets)
	}
}

func TestGroupOpenerWaitsForTheTank(t *testing.T) {
	g := newFake()
	g.partyTarget = map[packet.GUID]packet.GUID{tank: 201}
	m := g.add(201, 1, Point{20, 0, 0})
	m.InCombat, m.Target = true, tank
	d := NewDriver(mustPlan(t, tankPlan), g, Config{}, g.clock())
	d.observeInit()
	mobs := []objects.Unit{m.Unit}
	if _, ok := d.pickAssist(mobs); ok {
		t.Fatal("the party was sent at once")
	}
	g.now = g.now.Add(groupOpenerDelay - time.Second)
	if _, ok := d.pickAssist(mobs); ok {
		t.Fatal("the party was sent before the opener delay")
	}
	g.now = g.now.Add(time.Second)
	if u, ok := d.pickAssist(mobs); !ok || u.GUID != 201 {
		t.Fatalf("%v %v", u, ok)
	}
	// A mob that is not on the tank is not held back for it.
	d2 := NewDriver(mustPlan(t, tankPlan), g, Config{}, g.clock())
	d2.observeInit()
	m2 := m.Unit
	m2.Target = healer
	if _, ok := d2.pickAssist([]objects.Unit{m2}); !ok {
		t.Fatal("a mob on the healer was held back")
	}
}

func TestGroupSendsTheTankAtAMobHittingTheLeader(t *testing.T) {
	// The leader (the agent's own character) is a bot that is not the tank too.
	g, m1, m2 := tankFixture(t)
	g.partyTarget[tank] = 202
	var pullTargets []packet.GUID
	g.onChat = func(g *fakeGame, text string) {
		switch text {
		case ".bot action pullback":
			m1.Target, m2.Target = leader, tank
		case ".bot action pull 0":
			pullTargets = append(pullTargets, g.targeted)
			m1.Target = tank // the tank takes it
		}
	}
	rep := runTank(t, g)
	if len(pullTargets) != 1 || pullTargets[0] != 201 || rep.TankFirst.LooseAddOrders != 1 {
		t.Fatalf("pull targets %v %+v", pullTargets, rep.TankFirst)
	}
}

func TestGroupLooseAnswerWindow(t *testing.T) {
	var tg groupTank
	t0 := time.Unix(1_000_000, 0)
	if tg.takeLooseAnswer(t0) {
		t.Fatal("a refusal with no loose-add order out was taken for its answer")
	}
	tg.looseSent = append(tg.looseSent, t0)
	if !tg.takeLooseAnswer(t0.Add(2 * time.Second)) {
		t.Fatal("the answer to a loose-add order was not taken")
	}
	tg.looseSent = append(tg.looseSent, t0)
	if tg.takeLooseAnswer(t0.Add(looseAnswerWindow + time.Second)) {
		t.Fatal("a late refusal was taken for the answer to an old order")
	}
}
