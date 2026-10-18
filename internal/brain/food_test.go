package brain

import (
	"strings"
	"testing"
	"time"

	"github.com/pjerra/turtle-agent/internal/objects"
)

const (
	spConjureFood = 587 // Conjure Food rank 1
	itemBread     = 4540
	itemMuffin    = 5349
	itemCornbread = 4542 // Moist Cornbread, level 15
	itemPumper    = 1487 // Conjured Pumpernickel, level 25
	itemSweetRoll = 8076 // Conjured Sweet Roll, level 45
)

func TestLeaderEatsWhenHurtOutOfCombat(t *testing.T) {
	r := restRig(t)
	r.g.items = []objects.Item{{Entry: itemBread, Count: 5}}
	r.g.setSelf(func(u *objects.Unit) { u.Health = 600 }) // 75 %
	d := r.tick(time.Second)
	if d == nil || d.Action != "eat" || d.Trigger != "health_low" || len(r.g.used) != 1 || r.g.used[0] != itemBread {
		t.Fatalf("%+v used %v", d, r.g.used)
	}
	// Sitting and eating: nothing more to do, and no second piece goes out.
	r.g.setSelf(func(u *objects.Unit) { u.StandState = "sit"; u.Auras = append(u.Auras, objects.Aura{Name: "Food"}) })
	if d := r.tick(6 * time.Second); d != nil || r.idle() != "not_useful:eat" {
		t.Fatalf("%+v %q", d, r.idle())
	}
	// Healed up: no trigger.
	r.g.setSelf(func(u *objects.Unit) { u.Health = 800 })
	if d := r.tick(time.Second); d != nil || r.idle() != "no_trigger" {
		t.Fatalf("%+v %q", d, r.idle())
	}
}

func TestLeaderDoesNotEatAtTheEdgeOfTheBand(t *testing.T) {
	r := restRig(t)
	r.g.items = []objects.Item{{Entry: itemBread, Count: 5}}
	r.g.setSelf(func(u *objects.Unit) { u.Health = 680 }) // exactly 85 %
	if d := r.tick(time.Second); d != nil || r.idle() != "no_trigger" {
		t.Fatalf("%+v %q", d, r.idle())
	}
	r.g.setSelf(func(u *objects.Unit) { u.Health = 679 })
	if d := r.tick(time.Second); d == nil || d.Action != "eat" {
		t.Fatalf("%+v", d)
	}
}

func TestLeaderEatsTheBestFoodItsLevelAllows(t *testing.T) {
	r := restRig(t)
	// Level 30: Conjured Sweet Roll (45) is too high; Conjured Pumpernickel (25) beats Moist Cornbread (15) and bread.
	r.g.items = []objects.Item{{Entry: itemBread, Count: 5}, {Entry: itemCornbread, Count: 5}, {Entry: itemSweetRoll, Count: 5}, {Entry: itemPumper, Count: 5}}
	r.g.setSelf(func(u *objects.Unit) { u.Health = 300 })
	r.tick(time.Second)
	if len(r.g.used) != 1 || r.g.used[0] != itemPumper {
		t.Fatalf("%v", r.g.used)
	}
	// A level-50 leader takes the roll.
	r = restRig(t)
	r.g.items = []objects.Item{{Entry: itemPumper, Count: 5}, {Entry: itemSweetRoll, Count: 5}}
	r.g.setSelf(func(u *objects.Unit) { u.Health = 300; u.Level = 50 })
	r.tick(time.Second)
	if len(r.g.used) != 1 || r.g.used[0] != itemSweetRoll {
		t.Fatalf("%v", r.g.used)
	}
}

// A mana leader at a rest gate with an empty bag has nothing to drink: rest_drink's trigger is off, and the rest says "no drink" once,
// not on every tick. The next rest says it again, and a drink in the bag brings the drink back.
func TestRestDrinkSaysNoDrinkOncePerRest(t *testing.T) {
	g := newFake()
	g.self.Class, g.self.Power = ClassPaladin, 400 // 40% mana
	r := newRig(t, g, Params{Strategies: []string{"rest_drink"}})
	noDrink := func() int {
		n := 0
		for _, e := range g.events("brain_idle") {
			if e.Data["reason"] == "no_drink" {
				n++
			}
		}
		return n
	}
	for i := 0; i < 5; i++ {
		if d := r.tick(time.Second); d != nil {
			t.Fatalf("tick %d drank from an empty bag: %+v", i, d)
		}
	}
	if n := noDrink(); n != 1 {
		t.Fatalf("%d no-drink lines in five ticks of one rest, want 1", n)
	}
	if restDrink().Actions[0].Possible(r.b.newCtx(g.clk.Now())) {
		t.Fatal("drink possible with an empty bag")
	}
	// Full on mana the rest is over; the next low-mana rest says it again.
	g.setSelf(func(u *objects.Unit) { u.Power = manaFull })
	r.tick(time.Second)
	g.setSelf(func(u *objects.Unit) { u.Power = 400 })
	r.tick(time.Second)
	if n := noDrink(); n != 2 {
		t.Fatalf("%d no-drink lines after a second rest, want 2", n)
	}
	// A drink in the bag: it is possible, and the rule drinks it.
	g.items = []objects.Item{{Entry: 159, Count: 5}} // Refreshing Spring Water
	if !restDrink().Actions[0].Possible(r.b.newCtx(g.clk.Now())) {
		t.Fatal("drink not possible with a drink in the bag")
	}
	if d := r.tick(time.Second); d == nil || d.Action != "drink" || len(g.used) != 1 || g.used[0] != 159 {
		t.Fatalf("%+v used %v", d, g.used)
	}
}

func TestLeaderEatsAfterDrinkingWhenLowOnBoth(t *testing.T) {
	r := restRig(t)
	r.g.items = []objects.Item{{Entry: itemBread, Count: 5}, {Entry: itemWater, Count: 2}}
	r.g.setSelf(func(u *objects.Unit) { u.Health = 300; u.Power = 300 })
	if d := r.tick(time.Second); d == nil || d.Action != "drink" {
		t.Fatalf("drink first: %+v", d)
	}
	r.g.setSelf(func(u *objects.Unit) { u.Auras = append(u.Auras, objects.Aura{Name: "Drink"}) })
	if d := r.tick(2 * time.Second); d == nil || d.Action != "eat" || len(r.g.used) != 2 || r.g.used[1] != itemBread {
		t.Fatalf("then eat: %+v used %v", d, r.g.used)
	}
}

func TestLeaderNeverEatsInCombatOrWithAMobToFight(t *testing.T) {
	r := restRig(t)
	r.g.items = []objects.Item{{Entry: itemBread, Count: 5}}
	r.g.setSelf(func(u *objects.Unit) { u.Health = 300; u.InCombat = true })
	if d := r.tick(time.Second); d != nil || len(r.g.used) != 0 {
		t.Fatalf("%+v %v", d, r.g.used)
	}
	r.g.setSelf(func(u *objects.Unit) { u.InCombat = false })
	r.g.partyWithTank()
	r.g.addUnit(mob(mobA, tankG), 28, 0)
	if d := r.tick(time.Second); d != nil && d.Action == "eat" {
		t.Fatalf("a mob is there to fight: %+v", d)
	}
}

func TestEatingWaitsForTheRestGate(t *testing.T) {
	g := newFake()
	buffUp(g)
	g.items = []objects.Item{{Entry: itemBread, Count: 5}}
	g.setSelf(func(u *objects.Unit) { u.Health = 300 })
	cfg := DefaultConfig()
	cfg.Now = g.clk.Now
	cfg.NewTicker = func(time.Duration) (<-chan time.Time, func()) { return make(chan time.Time), func() {} }
	ctl := NewController(t.Context(), g, cfg)
	if _, err := ctl.Start(Params{Strategies: []string{"frost_mage"}, RestGate: true}); err != nil {
		t.Fatal(err)
	}
	defer ctl.Stop()
	b := ctl.b
	g.clk.advance(time.Second)
	if d := b.Tick(t.Context()); d != nil || len(g.used) != 0 {
		t.Fatalf("outside a gate: %+v %v", d, g.used)
	}
	ctl.Rest(true)
	g.clk.advance(time.Second)
	if d := b.Tick(t.Context()); d == nil || d.Action != "eat" {
		t.Fatalf("inside a gate: %+v", d)
	}
}

func TestMageConjuresFoodWhenItHasNone(t *testing.T) {
	r := restRig(t, spConjureFood)
	r.g.setSelf(func(u *objects.Unit) { u.Health = 300 })
	d := r.tick(time.Second)
	if d == nil || d.Action != "conjure_food" || d.Via != "pre" || d.Spell != spConjureFood || len(r.g.used) != 0 {
		t.Fatalf("%+v", d)
	}
	// Without the spell and without food there is nothing to do, and no loop.
	r = restRig(t)
	r.g.setSelf(func(u *objects.Unit) { u.Health = 300 })
	if d := r.tick(time.Second); d != nil || !strings.HasPrefix(r.idle(), "not_possible:eat") {
		t.Fatalf("%+v %q", d, r.idle())
	}
}

func TestEatingWorksForAnyClassThatNamesTheStrategy(t *testing.T) {
	g := newFake()
	g.items = []objects.Item{{Entry: itemMuffin, Count: 5}}
	g.setSelf(func(u *objects.Unit) { u.Class = 1; u.PowerType = 1; u.Power = 0; u.MaxPower = 100; u.Health = 300 })
	r := newRig(t, g, Params{Strategies: []string{"rest_eat"}})
	if d := r.tick(time.Second); d == nil || d.Action != "eat" || len(g.used) != 1 || g.used[0] != itemMuffin {
		t.Fatalf("%+v %v", d, g.used)
	}
}

// The food table is sorted best first within a level band, has no duplicates, and keeps the conjured foods.
func TestFoodTableIsSane(t *testing.T) {
	seen := map[uint32]bool{}
	for _, f := range foodItems {
		if seen[f.entry] || f.entry == 0 || f.level > 60 {
			t.Fatalf("bad row %+v", f)
		}
		seen[f.entry] = true
	}
	for _, e := range []uint32{5349, 1113, 1114, 1487, 8075, 8076, 22895} {
		if !seen[e] {
			t.Errorf("conjured food %d missing", e)
		}
	}
}
