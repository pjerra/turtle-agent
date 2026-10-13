package brain

import (
	"context"
	"errors"
	"math"
	"sync"
	"testing"
	"time"

	"github.com/pjerra/turtle-agent/internal/game"
	"github.com/pjerra/turtle-agent/internal/objects"
	"github.com/pjerra/turtle-agent/internal/packet"
)

// Tests for the fixes from the live class smoke test (issue #80): each one plays the symptom of the live run on the fake game.

// swingGame is the melee fake that also counts the swings the server refused, as the session's event log shows them. forget leaves the
// swing out of Swinging(), as in the live run where the brain never saw the attack_start of an attack it had not started itself: every
// tick then found the swing idle. An attack order at a mob the player does not face is refused with bad_facing, the swing stays on (the
// server keeps the auto attack and resumes it when the player faces the mob).
type swingGame struct {
	*meleeFake
	smu    sync.Mutex
	forget bool
	errs   uint64
}

func (s *swingGame) Attack(ctx context.Context) error {
	s.fakeGame.mu.Lock()
	s.attacks++
	sel := s.selected
	refused := false
	for _, u := range s.units {
		if u.GUID == sel && math.Abs(angleDiff(s.self.O, game.Facing(s.self.X, s.self.Y, u.X, u.Y))) > ConeHalfAngle {
			refused = true
		}
	}
	s.fakeGame.mu.Unlock()
	if !s.forget {
		s.setSwing(sel)
	}
	if refused {
		s.smu.Lock()
		s.errs++
		s.smu.Unlock()
	}
	return nil
}

func (s *swingGame) BadFacings() uint64 {
	s.smu.Lock()
	defer s.smu.Unlock()
	return s.errs
}

// swingRig is a preset's brain on a swingGame: the player as the class, at (0,0) facing east.
func swingRig(t *testing.T, preset string, class uint8, level uint32, power uint8, pool uint32, known []uint32, forget bool, tune func(*objects.Unit)) (*rig, *swingGame) {
	t.Helper()
	g := newFake(known...)
	g.self.Class, g.self.Level = class, level
	g.self.PowerType, g.self.Power, g.self.MaxPower = power, pool, pool
	g.self.Health, g.self.MaxHealth = 1200, 1200
	if tune != nil {
		tune(&g.self)
	}
	sg := &swingGame{meleeFake: &meleeFake{posFake: &posFake{fakeGame: g}, dodged: map[packet.GUID]time.Time{}}, forget: forget}
	return rigOn(t, sg, g, Params{Strategies: []string{preset}}), sg
}

func warriorSwingRig(t *testing.T, forget bool) (*rig, *swingGame) {
	return swingRig(t, "arms_warrior", ClassWarrior, 40, 1, 1000, warriorSpells, forget, func(u *objects.Unit) {
		u.Auras = []objects.Aura{stanceAura("Battle Shout", spBattleShout), stanceAura(stanceBattle, stanceSpells[stanceBattle])}
		u.Power = 600
	})
}

func TestSwingIsNotResentEveryTickWhenTheGameNeverShowsIt(t *testing.T) {
	// The live run (arms_warrior, 205 s): auto_attack ok x1015, no ability ever. The swing was never "on" for the brain.
	r, sg := warriorSwingRig(t, true)
	tankFights(r.g, 7, 4, 0)
	r.tick(100 * time.Millisecond)
	open(r)
	for i := 0; i < 24; i++ { // 4.8 s
		r.tick(200 * time.Millisecond)
	}
	if sg.attacks > 3 {
		t.Fatalf("the attack order went out %d times in 5 s", sg.attacks)
	}
	if got := casts(r.g); len(got) == 0 || got[0] != spMortal {
		t.Fatalf("the swing starved the abilities: casts %v", got)
	}
}

func TestSwingResentAfterItStopped(t *testing.T) {
	r, sg := warriorSwingRig(t, false)
	tankFights(r.g, 7, 4, 0)
	r.tick(100 * time.Millisecond)
	if d := open(r); d == nil || d.Action != "auto_attack" || sg.attacks != 1 {
		t.Fatalf("%+v attacks %d", d, sg.attacks)
	}
	sg.setSwing(0) // the server stopped the swing (a stun, a target switch)
	for i := 0; i < 20; i++ {
		r.tick(200 * time.Millisecond)
	}
	if sg.attacks != 2 {
		t.Fatalf("attacks %d: a swing that stopped is ordered again, once", sg.attacks)
	}
}

func TestSwingRefusedForFacingTurnsToTheMobBeforeAnythingElse(t *testing.T) {
	// The live run: the warrior stands with its back to the mob; bad_facing at once, then auto_attack every 200 ms and no face_target.
	r, sg := warriorSwingRig(t, false)
	tankFights(r.g, 7, 4, 0)
	r.g.setSelf(func(u *objects.Unit) { u.O = math.Pi })
	r.tick(100 * time.Millisecond)
	if d := open(r); d == nil || d.Action != "auto_attack" {
		t.Fatalf("%+v idle %q", d, r.idle())
	}
	if n := sg.BadFacings(); n != 1 {
		t.Fatalf("the fake did not refuse the swing: %d", n)
	}
	d := r.tick(200 * time.Millisecond)
	if d == nil || d.Action != "face_target" || d.Trigger != "swing_refused_facing" {
		t.Fatalf("the player must turn to the mob next: %+v idle %q", d, r.idle())
	}
	if len(r.g.faces) != 1 || r.g.faces[0] != mobA {
		t.Fatalf("faces %v", r.g.faces)
	}
	for i := 0; i < 12; i++ {
		r.tick(200 * time.Millisecond)
	}
	if len(r.g.faces) != 1 || sg.attacks != 1 {
		t.Fatalf("faces %v attacks %d", r.g.faces, sg.attacks)
	}
	if got := casts(r.g); len(got) == 0 || got[0] != spMortal {
		t.Fatalf("no ability after the turn: %v", got)
	}
}

func TestSwingWaitsForTheTurnWhenTheGameDoesNotShowTheSwing(t *testing.T) {
	// Without a swing in view the order would be sent again at once; refused for facing, it waits and the turn comes first.
	r, sg := warriorSwingRig(t, true)
	tankFights(r.g, 7, 4, 0)
	r.g.setSelf(func(u *objects.Unit) { u.O = math.Pi; u.Power = 0 })
	r.tick(100 * time.Millisecond)
	open(r)
	for i := 0; i < 10; i++ {
		r.tick(200 * time.Millisecond)
	}
	if len(r.g.faces) != 1 || sg.attacks > 2 {
		t.Fatalf("faces %v attacks %d", r.g.faces, sg.attacks)
	}
}

func TestEveryMeleeClassKeepsItsSwingOrderToOnePerFewSeconds(t *testing.T) {
	for _, c := range []struct {
		name   string
		preset string
		class  uint8
		level  uint32
		power  uint8
		pool   uint32
		known  []uint32
		tune   func(*objects.Unit)
	}{
		{"rogue", "combat_rogue", ClassRogue, 40, 3, 100, rogueSpells, nil},
		{"hunter", "marksman_hunter", ClassHunter, 58, 0, 1000, hunterSpells, func(u *objects.Unit) {
			u.Auras = []objects.Aura{{Name: "Aspect of the Hawk", SpellID: spHawk}}
		}},
	} {
		r, sg := swingRig(t, c.preset, c.class, c.level, c.power, c.pool, c.known, true, c.tune)
		m := mob(mobA, selfG)
		r.g.addUnit(m, 4, 0)
		r.g.setSelf(func(u *objects.Unit) { u.InCombat = true })
		for i := 0; i < 25; i++ { // 5 s of ticks
			r.tick(200 * time.Millisecond)
		}
		if sg.attacks < 1 || sg.attacks > 2 {
			t.Errorf("%s: %d attack orders in 5 s", c.name, sg.attacks)
		}
	}
}

// ---- Slice and Dice ----

func TestSliceAndDiceIsCastAtTheComboMobNotAtThePlayer(t *testing.T) {
	r, mf := newRogueRig(t, Params{})
	meleeFight(r, mf)
	setEnergy(r.g, 100)
	setCombo(r.g, 2, mobA)
	d := open(r)
	if d == nil || d.Action != "slice_and_dice" || d.Spell != spSnD || d.Target != mobA {
		t.Fatalf("%+v idle %q", d, r.idle())
	}
	if c := r.g.lastCast(t); c.spell != spSnD || c.target != mobA {
		t.Fatalf("the cast aimed at %v", c)
	}
}

func TestBadTargetsOfACastAtThePlayerBacksOffForLong(t *testing.T) {
	r, _ := newRogueRig(t, Params{})
	r.tick(100 * time.Millisecond)
	bad := codeOf(t, "bad_targets")
	for _, c := range []struct {
		name   string
		target packet.GUID
		held   time.Duration
	}{{"a cast at the mob", mobA, 2 * time.Second}, {"a cast at the player", selfG, selfBadTargetsHold}} {
		r.g.clk.advance(time.Minute)
		r.b.mem.mu.Lock()
		r.b.mem.lastCastTarget = c.target
		r.b.mem.mu.Unlock()
		r.g.st.CastRefused(spSnD, bad)
		ctx := r.b.newCtx(r.g.clk.Now())
		r.b.observeFailures(ctx)
		if _, held := r.b.mem.held(spSnD, ctx.Now.Add(c.held-time.Second)); !held {
			t.Errorf("%s: not held for %v", c.name, c.held)
		}
		if _, held := r.b.mem.held(spSnD, ctx.Now.Add(c.held+time.Second)); held {
			t.Errorf("%s: held for longer than %v", c.name, c.held)
		}
	}
}

// ---- the hunter's pet ----

func TestPetAttackIsNotHeldBackByACastInProgressOrSettling(t *testing.T) {
	r, pf := newPetHunter(t, Params{})
	pf.withPet()
	tankHolds(r)
	// The tank holds the mob, the opening delay is over, and a cast has just been sent: the order to the pet is no cast.
	r.g.clk.advance(2200 * time.Millisecond)
	r.g.st.CastStarted(spSerpent, 3000, mobA)
	if !r.b.newCtx(r.g.clk.Now()).Spells.Casting() {
		t.Fatal("the fake is not casting")
	}
	d := r.b.Tick(context.Background())
	if d == nil || d.Action != "pet_attack" || d.Target != mobA {
		t.Fatalf("%+v idle %q", d, r.idle())
	}
	if o := pf.orderList(); len(o) != 1 || o[0] != "attack:"+guidStr(mobA) {
		t.Fatalf("%v", o)
	}
	// The same while the brain settles after its own cast.
	r, pf = newPetHunter(t, Params{})
	pf.withPet()
	tankHolds(r)
	r.g.clk.advance(2200 * time.Millisecond)
	r.b.mem.settle(r.g.clk.Now().Add(300 * time.Millisecond))
	if d := r.b.Tick(context.Background()); d == nil || d.Action != "pet_attack" {
		t.Fatalf("%+v idle %q", d, r.idle())
	}
}

func TestPetAttackWithinThreeSecondsOfTheTankHoldingTheMob(t *testing.T) {
	r, pf := newPetHunter(t, Params{})
	pf.withPet()
	tankHolds(r)
	var at time.Duration
	for at < 3*time.Second && len(pf.orderList()) == 0 {
		r.tick(200 * time.Millisecond)
		at += 200 * time.Millisecond
	}
	if o := pf.orderList(); len(o) != 1 {
		t.Fatalf("no order to the pet after %v: %v idle %q", at, o, r.idle())
	}
}

// ---- noise ----

func TestNoHealthstoneNoLineAboutIt(t *testing.T) {
	// The live run: "healthstone not_possible condition" at relevance 47 every 3 s, in every fight, with none in the bags.
	r, mf := newWarriorRig(t, "arms_warrior", stanceBattle, Params{})
	meleeFight(r, mf)
	r.g.items = nil
	onMe(r.g, mobA)
	r.g.setSelf(func(u *objects.Unit) { u.Health = 150; u.InCombat = true }) // 12%
	setRage(r.g, 0)
	for i := 0; i < 8; i++ {
		r.tick(3 * time.Second)
		if got := r.idle(); got == "not_possible:healthstone:condition" || got == "not_possible:health_potion:condition" {
			t.Fatalf("a healthstone nobody has is reported: %q", got)
		}
	}
	for _, e := range r.g.events("brain_decision") {
		if a := e.Data["action"]; a == "healthstone" || a == "health_potion" {
			t.Fatalf("%v", e.Data)
		}
	}
	// With a potion in the bags it is drunk.
	r.g.items = []objects.Item{{Entry: 929, Count: 1}}
	if d := r.tick(3 * time.Second); d == nil || d.Action != "health_potion" {
		t.Fatalf("%+v idle %q", d, r.idle())
	}
}

func TestBattleShoutWithoutRageIsNotTried(t *testing.T) {
	r, _ := newWarriorRig(t, "arms_warrior", stanceBattle, Params{})
	r.g.setSelf(func(u *objects.Unit) { u.Auras = []objects.Aura{stanceAura("Battle Stance", spBattleSt)}; u.Power = 0 })
	for i := 0; i < 5; i++ {
		if d := r.tick(3 * time.Second); d != nil {
			t.Fatalf("%+v", d)
		}
		if got := r.idle(); got == "not_possible:battle_shout:no_power" {
			t.Fatalf("a shout without rage is reported: %q", got)
		}
	}
	r.g.setSelf(func(u *objects.Unit) { u.Power = 100 })
	if d := r.tick(3 * time.Second); d == nil || d.Action != "battle_shout" {
		t.Fatalf("with the rage it shouts: %+v", d)
	}
}

func TestSpellNotInTheBookIsNeverPossibleAndSaidOnceInAWhile(t *testing.T) {
	// A warrior who knows only the stance: nothing of the rotation is in the book (the talent spells of the live run were not either).
	r, mf := newWarriorRig(t, "arms_warrior", stanceBattle, Params{}, spBattleSt)
	setRage(r.g, 600)
	meleeFight(r, mf)
	open(r)
	for i := 0; i < 40; i++ { // two minutes at 3 s
		r.tick(3 * time.Second)
	}
	if got := casts(r.g); len(got) != 0 {
		t.Fatalf("casts %v", got)
	}
	said := map[any]int{}
	for _, e := range r.g.events("brain_decision") {
		if e.Data["why"] == "unknown_spell" {
			said[e.Data["action"]]++
		}
	}
	for a, n := range said {
		if n > 1 {
			t.Errorf("%v: unknown_spell said %d times in two minutes", a, n)
		}
	}
}

// ---- the warlock's percent costs ----

func TestPercentCostIsOfTheBaseManaNotTheMaximum(t *testing.T) {
	// Summon Voidwalker costs 100% of the base mana (the core: Spell::CalculatePowerCost, Unit::GetCreateMana).
	// A pool of 99999 (the live run's .modify mana) with 95000 left: the base is a few hundred.
	for _, c := range []struct {
		name            string
		power, max, bas uint32
		want            bool
	}{
		{"modified pool, 95k of 99999", 95000, 99999, 1100, true},
		{"natural pool, 1500 left of 2000, base 1400", 1500, 2000, 1400, true},
		{"natural pool, 1300 left of 2000, base 1400", 1300, 2000, 1400, false},
		{"no base shown: the maximum stands in", 1900, 2000, 0, false},
	} {
		r, _ := newWarlock(t, Params{})
		setItems(r.g, 1, itemMajorHS)
		r.g.setSelf(func(u *objects.Unit) { u.Power, u.MaxPower, u.BaseMana = c.power, c.max, c.bas })
		d := r.tick(100 * time.Millisecond)
		got := d != nil && d.Action == "pet_summon" && d.Spell == spVoidwalker
		if got != c.want {
			t.Errorf("%s: summon %v (%+v idle %q), want %v", c.name, got, d, r.idle(), c.want)
		}
	}
}

// ---- missing navmesh data ----

type noMesh struct{}

func (noMesh) Error() string     { return "navmesh: map 1: no such file" }
func (noMesh) Unavailable() bool { return true }

func TestWalkWithoutNavmeshDataIsTriedAndSaidOnce(t *testing.T) {
	r, mf := newWarriorRig(t, "arms_warrior", stanceBattle, Params{})
	mf.walkErr = noMesh{}
	tankFights(r.g, 8, 4, math.Pi) // the mob faces the warrior: melee_reposition walks
	mf.setSwing(mobA)
	for i := 0; i < 60; i++ { // a minute and a half
		r.tick(1500 * time.Millisecond)
	}
	n := 0
	for _, e := range r.g.events("brain_decision") {
		if e.Data["result"] == "error" {
			n++
		}
	}
	if n != 1 {
		t.Fatalf("the missing map was reported %d times", n)
	}
	// Any other failure of a walk is still held for ErrorHold only and reported at every try.
	r, mf = newWarriorRig(t, "arms_warrior", stanceBattle, Params{})
	mf.walkErr = errors.New("no route")
	tankFights(r.g, 8, 4, math.Pi)
	mf.setSwing(mobA)
	for i := 0; i < 20; i++ {
		r.tick(1500 * time.Millisecond)
	}
	if n := len(r.g.events("brain_decision")); n < 2 {
		t.Fatalf("an ordinary walk failure was swallowed: %d decisions", n)
	}
}

func TestPetOrderDuringAWalkLeavesTheWalkRunning(t *testing.T) {
	r, pf := newPetHunter(t, Params{})
	pf.withPet()
	tankHolds(r)
	r.g.clk.advance(2200 * time.Millisecond)
	sctx, done, ok := r.b.lock.begin(context.Background())
	if !ok {
		t.Fatal("no step")
	}
	defer done()
	d := r.b.Tick(context.Background())
	if d == nil || d.Action != "pet_attack" {
		t.Fatalf("%+v idle %q", d, r.idle())
	}
	if sctx.Err() != nil || !r.b.lock.stepping() {
		t.Fatal("the pet order cut the walk in progress")
	}
}

func TestSwingRefusalCountThatFellBackIsNewsAgain(t *testing.T) {
	// A new session starts its count over: the refusal after a reconnect is answered like the first.
	r, sg := warriorSwingRig(t, true)
	tankFights(r.g, 7, 4, 0)
	r.g.setSelf(func(u *objects.Unit) { u.O = math.Pi })
	r.tick(100 * time.Millisecond)
	open(r)
	if d := r.tick(200 * time.Millisecond); d == nil || d.Action != "face_target" {
		t.Fatalf("%+v", d)
	}
	r.g.setSelf(func(u *objects.Unit) { u.O = math.Pi })
	sg.smu.Lock()
	sg.errs = 0
	sg.smu.Unlock()
	if d := r.tick(200 * time.Millisecond); d != nil && d.Action == "face_target" {
		t.Fatalf("the reset itself is no refusal: %+v", d)
	}
	sg.smu.Lock()
	sg.errs = 1
	sg.smu.Unlock()
	if d := r.tick(500 * time.Millisecond); d == nil || d.Action != "face_target" {
		t.Fatalf("%+v idle %q", d, r.idle())
	}
}
