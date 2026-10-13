package brain

import (
	"context"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/pjerra/turtle-agent/internal/objects"
	"github.com/pjerra/turtle-agent/internal/packet"
	"github.com/pjerra/turtle-agent/internal/spells"
)

// Spells and units of the pet tests (ids from internal/spells/meta.tsv.gz; TestPetSpellData checks the facts the rules rely on).
const (
	spCallPet   = 883
	spRevivePet = 982
	spMendPet   = 13544 // rank 7, level 60
	spGrowl     = 14921 // rank 7 (the pet's, level 60)
	spTorment   = 3716
	petG        = packet.GUID(0xF140000000000007)
)

// petFake is the fake game with the pet half of the Game: the test sets the pet, the brain's orders are written down.
type petFake struct {
	*meleeFake
	pmu    sync.Mutex
	pet    *PetState
	orders []string
}

func (f *petFake) Pet() (PetState, bool) {
	f.pmu.Lock()
	defer f.pmu.Unlock()
	if f.pet == nil {
		return PetState{}, false
	}
	p := *f.pet
	p.Spells = append([]PetSpell{}, p.Spells...)
	return p, true
}

func (f *petFake) note(s string) {
	f.pmu.Lock()
	f.orders = append(f.orders, s)
	f.pmu.Unlock()
}

func (f *petFake) PetAttack(ctx context.Context, target packet.GUID) error {
	f.note("attack:" + guidStr(target))
	return nil
}
func (f *petFake) PetFollow(ctx context.Context) error { f.note("follow"); return nil }
func (f *petFake) PetReact(ctx context.Context, react string) error {
	f.note("react:" + react)
	f.setPet(func(p *PetState) { p.React = react })
	return nil
}
func (f *petFake) PetAutocast(ctx context.Context, spell uint32, on bool) error {
	f.note(fmt.Sprintf("autocast:%d:%v", spell, on))
	f.setPet(func(p *PetState) {
		for i := range p.Spells {
			if p.Spells[i].ID == spell {
				p.Spells[i].Autocast = on
			}
		}
	})
	return nil
}

func (f *petFake) setPet(fn func(*PetState)) {
	f.pmu.Lock()
	defer f.pmu.Unlock()
	if f.pet != nil {
		fn(f.pet)
	}
}

func (f *petFake) orderList() []string {
	f.pmu.Lock()
	defer f.pmu.Unlock()
	return append([]string{}, f.orders...)
}

// withPet gives the player a pet out, alive, on defensive and following, with Bite on its bar, in view 4 yards away. addGrowl puts Growl,
// on autocast, on the bar too.
func (f *petFake) withPet() {
	f.pmu.Lock()
	f.pet = &PetState{GUID: petG, Health: 400, MaxHealth: 400, Happiness: 3, React: "defensive", Command: "follow", BarKnown: true, Enabled: true, Distance: 4,
		Spells: []PetSpell{{ID: 17253, Name: "Bite", Autocast: true, OnBar: true}}}
	f.pmu.Unlock()
	f.addUnit(objects.Unit{GUID: petG, Type: "creature", Name: "Pet", Health: 400, MaxHealth: 400}, 4, 0)
}

func (f *petFake) addGrowl() {
	f.setPet(func(p *PetState) {
		p.Spells = append(p.Spells, PetSpell{ID: spGrowl, Name: "Growl", Autocast: true, OnBar: true})
	})
}

func (f *petFake) noPet() {
	f.pmu.Lock()
	f.pet = nil
	f.pmu.Unlock()
}

// newPetRig is a brain of the preset on a fake game with a pet half, the player as the class on the power with its full pool.
func newPetRig(t *testing.T, preset string, class uint8, level uint32, power uint8, pool uint32, p Params, known []uint32, tune func(*objects.Unit)) (*rig, *petFake) {
	t.Helper()
	g := newFake(known...)
	g.self.Class, g.self.Level = class, level
	g.self.PowerType, g.self.Power, g.self.MaxPower = power, pool, pool
	g.self.Health, g.self.MaxHealth = 1000, 1000
	if tune != nil {
		tune(&g.self)
	}
	pf := &petFake{meleeFake: &meleeFake{posFake: &posFake{fakeGame: g}, dodged: map[packet.GUID]time.Time{}}}
	cfg := DefaultConfig()
	cfg.Now = g.clk.Now
	if p.Strategies == nil {
		p.Strategies = []string{preset}
	}
	b, err := New(pf, cfg, p)
	if err != nil {
		t.Fatal(err)
	}
	return &rig{t: t, g: g, b: b}, pf
}

func newPetHunter(t *testing.T, p Params, known ...uint32) (*rig, *petFake) {
	t.Helper()
	if len(known) == 0 {
		known = append(append([]uint32{}, hunterSpells...), spCallPet, spRevivePet, spMendPet)
	}
	return newPetRig(t, "marksman_hunter", ClassHunter, 60, 0, 1000, p, known, func(u *objects.Unit) {
		u.Auras = []objects.Aura{{Name: "Aspect of the Hawk", SpellID: spHawk}}
	})
}

// held puts the tank's mob on the tank (the standard ranged fight) and lets the opening delay pass.
func tankHolds(r *rig) {
	tankFights(r.g, 25, 28, 0)
	r.tick(100 * time.Millisecond)
}

func TestPetIsSentAtTheTanksMobOnlyAfterTheOpeningDelayAndTheTankHoldingIt(t *testing.T) {
	r, pf := newPetHunter(t, Params{})
	pf.withPet()
	tankHolds(r)
	// The opening delay is on: nothing, not the pet either.
	if d := r.tick(100 * time.Millisecond); d != nil || r.idle() != "vetoed:opening_delay" {
		t.Fatalf("%+v idle %q", d, r.idle())
	}
	if o := pf.orderList(); len(o) != 0 {
		t.Fatalf("the pet was sent in the opening delay: %v", o)
	}
	d := open(r)
	if d == nil || d.Action != "pet_attack" || d.Trigger != "mob_held_pet_idle" || d.Target != mobA || d.Rel != RelNormal+10 {
		t.Fatalf("%+v idle %q", d, r.idle())
	}
	if o := pf.orderList(); len(o) != 1 || o[0] != "attack:"+guidStr(mobA) {
		t.Fatalf("%v", o)
	}
	// The pet is on the mob (its target shows it): no second order, and the hunter's shot goes on.
	pf.setPet(func(p *PetState) { p.Target = mobA; p.InCombat = true })
	if d := r.tick(400 * time.Millisecond); d == nil || d.Action != "auto_shot" {
		t.Fatalf("%+v", d)
	}
	if o := pf.orderList(); len(o) != 1 {
		t.Fatalf("the order was repeated: %v", o)
	}
}

func TestPetWaitsWhileTheMobIsNotOnTheTank(t *testing.T) {
	r, pf := newPetHunter(t, Params{})
	pf.withPet()
	tankFights(r.g, 25, 28, 0)
	r.g.mutate(mobA, func(u *objects.Unit) { u.Target = 0 }) // the tank has not got it: the mob is on nobody
	r.tick(100 * time.Millisecond)
	for i := 0; i < 3; i++ {
		r.tick(1100 * time.Millisecond)
	}
	if o := pf.orderList(); len(o) != 0 {
		t.Fatalf("a mob nobody holds: %v", o)
	}
	r.g.mutate(mobA, func(u *objects.Unit) { u.Target = tankG })
	if d := r.tick(400 * time.Millisecond); d == nil || d.Action != "pet_attack" {
		t.Fatalf("the tank holds it now: %+v idle %q", d, r.idle())
	}
}

func TestPetNeverPulls(t *testing.T) {
	r, pf := newPetHunter(t, Params{})
	pf.withPet()
	tankFights(r.g, 25, 28, 0)
	r.g.mutate(mobA, func(u *objects.Unit) { u.InCombat = false }) // the tank targets a mob nobody has pulled
	r.tick(100 * time.Millisecond)
	for i := 0; i < 3; i++ {
		r.tick(1100 * time.Millisecond)
	}
	if o := pf.orderList(); len(o) != 0 {
		t.Fatalf("the pet was sent at a mob that is not in combat: %v", o)
	}
}

func TestPetHelpsAtOnceWithAMobOnTheOwnerAndRespectsStay(t *testing.T) {
	r, pf := newPetHunter(t, Params{})
	pf.withPet()
	r.g.addUnit(mob(mobA, selfG), 12, 0) // a mob on the hunter, no tank: no delay
	if d := r.tick(100 * time.Millisecond); d == nil || d.Action != "pet_attack" {
		t.Fatalf("%+v idle %q", d, r.idle())
	}
	// An explicit stay stays: no attack order, no recall.
	r, pf = newPetHunter(t, Params{})
	pf.withPet()
	pf.setPet(func(p *PetState) { p.Command = "stay" })
	r.g.addUnit(mob(mobA, selfG), 12, 0)
	r.tick(100 * time.Millisecond)
	r.tick(2200 * time.Millisecond)
	if o := pf.orderList(); len(o) != 0 {
		t.Fatalf("the pet was told to stay: %v", o)
	}
}

func TestPetIsCalledBackWhenTheFightEndsAndNotTwice(t *testing.T) {
	r, pf := newPetHunter(t, Params{})
	pf.withPet()
	tankHolds(r)
	open(r)
	pf.setPet(func(p *PetState) { p.Target = mobA; p.InCombat = true })
	// The mob dies: it is gone from the scene, the tank has no target, the player is out of combat; the pet still chases.
	r.g.mu.Lock()
	r.g.units = r.g.units[:0]
	r.g.mu.Unlock()
	r.g.partyWithTank()
	r.g.mutate(tankG, func(u *objects.Unit) { u.Target = 0 })
	r.g.addUnit(objects.Unit{GUID: petG, Type: "creature", Name: "Pet", Health: 400, MaxHealth: 400}, 4, 0)
	r.g.setSelf(func(u *objects.Unit) { u.InCombat = false })
	var follow *Decision
	for i := 0; i < 8 && follow == nil; i++ {
		if d := r.tick(1100 * time.Millisecond); d != nil && d.Action == "pet_recall" {
			follow = d
		}
	}
	if follow == nil || follow.Trigger != "fight_over_pet_out" {
		t.Fatalf("no recall: idle %q recent %+v", r.idle(), r.recent())
	}
	n := len(pf.orderList())
	r.tick(500 * time.Millisecond) // within the hold: not again, even though the pet's target has not cleared yet
	if len(pf.orderList()) != n {
		t.Fatalf("recalled twice: %v", pf.orderList())
	}
	pf.setPet(func(p *PetState) { p.Target = 0 })
	for i := 0; i < 5; i++ {
		r.tick(2 * time.Second)
	}
	if o := pf.orderList(); o[len(o)-1] != "follow" || countOf(o, "follow") != 1 {
		t.Fatalf("a pet that follows is left alone: %v", o)
	}
}

func countPrefix(l []string, prefix string) int {
	n := 0
	for _, x := range l {
		if len(x) >= len(prefix) && x[:len(prefix)] == prefix {
			n++
		}
	}
	return n
}

func countOf(l []string, s string) int {
	n := 0
	for _, x := range l {
		if x == s {
			n++
		}
	}
	return n
}

func TestPetIsCalledBackWhenTheLeaderFlees(t *testing.T) {
	r, pf := newPetHunter(t, Params{NoMove: true}) // the test moves the units; the hunter's own positioning would walk it back
	pf.withPet()
	tankHolds(r)
	open(r)
	// The pet chases a mob 50 yd from the leader, who is running in combat.
	pf.setPet(func(p *PetState) { p.Target = mobA })
	r.g.mutate(mobA, func(u *objects.Unit) { u.X = 50 })
	r.g.setSelf(func(u *objects.Unit) { u.InCombat = true; u.Moving = true })
	var d *Decision
	for i := 0; i < 4 && (d == nil || d.Action != "pet_recall"); i++ {
		d = r.tick(300 * time.Millisecond)
	}
	if d == nil || d.Action != "pet_recall" || d.Trigger != "leader_fleeing" || d.Rel != RelMove+2 {
		t.Fatalf("%+v idle %q", d, r.idle())
	}
	// The recall cleared the pet's target (AttackStop). Over several hold windows of the attack (2.5 s) and of the recall (3 s) the leader
	// keeps running: the pet is not sent back in, and not called back again.
	pf.setPet(func(p *PetState) { p.Target = 0 })
	attacks := countPrefix(pf.orderList(), "attack:")
	for i := 0; i < 12; i++ {
		r.tick(1500 * time.Millisecond)
	}
	if got := countPrefix(pf.orderList(), "attack:"); got != attacks || countOf(pf.orderList(), "follow") != 1 {
		t.Fatalf("ping-pong while the leader flees: %v", pf.orderList())
	}
	// The leader stops running: the tank still holds the mob, and the pet goes back in.
	r.g.mutate(mobA, func(u *objects.Unit) { u.X = 28 })
	r.g.setSelf(func(u *objects.Unit) { u.Moving = false })
	if d := r.tick(1500 * time.Millisecond); d == nil || d.Action != "pet_attack" {
		t.Fatalf("%+v idle %q", d, r.idle())
	}
	// Standing in the fight with the pet on its mob is no flight.
	r, pf = newPetHunter(t, Params{})
	pf.withPet()
	tankHolds(r)
	open(r)
	pf.setPet(func(p *PetState) { p.Target = mobA })
	r.g.setSelf(func(u *objects.Unit) { u.InCombat = true })
	for i := 0; i < 5; i++ {
		r.tick(1100 * time.Millisecond)
	}
	if countOf(pf.orderList(), "follow") != 0 {
		t.Fatalf("%v", pf.orderList())
	}
}

func TestPetStaysOnTheMobThatIsOnAHurtLeader(t *testing.T) {
	// A hunter below a quarter of its health, running, with a mob on it: the pet is not called off that mob, it is sent at it.
	r, pf := newPetHunter(t, Params{})
	pf.withPet()
	r.g.addUnit(mob(mobA, selfG), 8, 0)
	setHealth(r.g, 20)
	r.g.setSelf(func(u *objects.Unit) { u.InCombat = true; u.Moving = true })
	if d := r.tick(100 * time.Millisecond); d == nil || d.Action != "pet_attack" || d.Target != mobA {
		t.Fatalf("%+v idle %q", d, r.idle())
	}
	pf.setPet(func(p *PetState) { p.Target = mobA })
	for i := 0; i < 8; i++ {
		r.tick(1500 * time.Millisecond)
	}
	if countOf(pf.orderList(), "follow") != 0 || countPrefix(pf.orderList(), "attack:") != 1 {
		t.Fatalf("%v", pf.orderList())
	}
}

func TestPetIsKeptDefensiveInAGroupOnceAndLeftAloneSolo(t *testing.T) {
	r, pf := newPetHunter(t, Params{})
	pf.withPet()
	pf.setPet(func(p *PetState) { p.React = "aggressive" })
	r.g.partyWithTank()
	d := r.tick(100 * time.Millisecond)
	if d == nil || d.Action != "pet_defensive" {
		t.Fatalf("%+v idle %q", d, r.idle())
	}
	// The owner sets it aggressive again by hand: the brain does not fight that.
	pf.setPet(func(p *PetState) { p.React = "aggressive" })
	for i := 0; i < 5; i++ {
		r.tick(2 * time.Second)
	}
	if countOf(pf.orderList(), "react:defensive") != 1 {
		t.Fatalf("%v", pf.orderList())
	}
	// Solo: an aggressive pet is the player's choice. Passive is never touched.
	for _, react := range []string{"aggressive", "passive"} {
		r, pf = newPetHunter(t, Params{})
		pf.withPet()
		pf.setPet(func(p *PetState) { p.React = react })
		if react == "passive" {
			r.g.partyWithTank()
		}
		for i := 0; i < 3; i++ {
			r.tick(2 * time.Second)
		}
		if countOf(pf.orderList(), "react:defensive") != 0 {
			t.Fatalf("%s: %v", react, pf.orderList())
		}
	}
}

func TestPetTauntAutocastGoesOffWithATankAndStaysOnSolo(t *testing.T) {
	r, pf := newPetHunter(t, Params{})
	pf.withPet()
	pf.addGrowl()
	r.g.partyWithTank()
	d := r.tick(100 * time.Millisecond)
	if d == nil || d.Action != "pet_taunt_off" || d.Trigger != "pet_taunts_with_tank" {
		t.Fatalf("%+v idle %q", d, r.idle())
	}
	if o := pf.orderList(); len(o) != 1 || o[0] != fmt.Sprintf("autocast:%d:false", spGrowl) {
		t.Fatalf("only the taunt: %v", o)
	}
	for i := 0; i < 4; i++ {
		r.tick(2 * time.Second)
	}
	if o := pf.orderList(); len(o) != 1 {
		t.Fatalf("%v", o)
	}
	// Solo, and in a group without a tank: it stays on.
	r, pf = newPetHunter(t, Params{})
	pf.withPet()
	pf.addGrowl()
	for i := 0; i < 3; i++ {
		r.tick(2 * time.Second)
	}
	r.g.party = []Member{{Name: "Mage", GUID: 5, Online: true}}
	r.g.addUnit(objects.Unit{GUID: 5, Type: "player", Name: "Mage", Class: 8, InParty: true, Health: 500, MaxHealth: 500}, 6, 0)
	for i := 0; i < 3; i++ {
		r.tick(2 * time.Second)
	}
	if o := pf.orderList(); len(o) != 0 {
		t.Fatalf("%v", o)
	}
}

func TestHunterCallsReviveAndMendsThePet(t *testing.T) {
	// No pet out: Call Pet, once.
	r, pf := newPetHunter(t, Params{})
	d := r.tick(100 * time.Millisecond)
	if d == nil || d.Action != "pet_summon" || d.Spell != spCallPet || d.Target != selfG {
		t.Fatalf("%+v idle %q", d, r.idle())
	}
	for i := 0; i < 4; i++ {
		r.tick(2 * time.Second)
	}
	if got := casts(r.g); !eq(got, []uint32{spCallPet}) {
		t.Fatalf("casts %v", got)
	}
	// 14 s later with still no pet it tries again.
	r.tick(5 * time.Second)
	if got := casts(r.g); len(got) != 2 {
		t.Fatalf("casts %v", got)
	}
	// A dead pet: Revive Pet.
	r, pf = newPetHunter(t, Params{})
	pf.withPet()
	pf.setPet(func(p *PetState) { p.Dead, p.Health = true, 0 })
	d = r.tick(100 * time.Millisecond)
	if d == nil || d.Action != "pet_revive" || d.Spell != spRevivePet {
		t.Fatalf("%+v idle %q", d, r.idle())
	}
	// Not in a fight, though.
	r, pf = newPetHunter(t, Params{})
	pf.withPet()
	pf.setPet(func(p *PetState) { p.Dead, p.Health = true, 0 })
	tankHolds(r)
	r.g.setSelf(func(u *objects.Unit) { u.InCombat = true })
	for i := 0; i < 3; i++ {
		r.tick(2 * time.Second)
	}
	for _, id := range casts(r.g) {
		if id == spRevivePet || id == spCallPet {
			t.Fatalf("a 10 s cast in a fight: %v", casts(r.g))
		}
	}
	// A hurt pet in reach: Mend Pet, at the pet. In a fight only below half.
	r, pf = newPetHunter(t, Params{})
	pf.withPet()
	pf.setPet(func(p *PetState) { p.Health = 250 }) // 62%: out of combat Mend Pet starts below 70
	r.g.mutate(petG, func(u *objects.Unit) { u.Health = 250 })
	d = r.tick(100 * time.Millisecond)
	if d == nil || d.Action != "pet_mend" || d.Spell != spMendPet || d.Target != petG {
		t.Fatalf("%+v idle %q", d, r.idle())
	}
	r.g.mutate(petG, func(u *objects.Unit) { u.Auras = []objects.Aura{{Name: "Mend Pet", SpellID: spMendPet}} })
	if d := r.tick(6 * time.Second); d != nil && d.Action == "pet_mend" {
		t.Fatalf("mending already: %+v", d)
	}
	r, pf = newPetHunter(t, Params{})
	pf.withPet()
	pf.setPet(func(p *PetState) { p.Health = 250 })
	r.g.mutate(petG, func(u *objects.Unit) { u.Health = 250 })
	r.g.setSelf(func(u *objects.Unit) { u.InCombat = true })
	tankHolds(r)
	for i := 0; i < 3; i++ {
		r.tick(2 * time.Second)
	}
	for _, id := range casts(r.g) {
		if id == spMendPet {
			t.Fatalf("mended a pet at 62%% in a fight: %v", casts(r.g))
		}
	}
	// Far from the pet (beyond 20 yd): nothing to mend with.
	r, pf = newPetHunter(t, Params{})
	pf.withPet()
	pf.setPet(func(p *PetState) { p.Health = 100 })
	r.g.mutate(petG, func(u *objects.Unit) { u.Health = 100; u.X = 40 })
	for i := 0; i < 3; i++ {
		r.tick(2 * time.Second)
	}
	for _, id := range casts(r.g) {
		if id == spMendPet {
			t.Fatalf("mended out of reach: %v", casts(r.g))
		}
	}
	_ = pf
}

func TestHunterWithoutAPetGameStillPlays(t *testing.T) {
	// The plain fake has no pet half: the pet rules stay quiet and the shots go on, as before the pet existed.
	r, _ := newHunterRig(t, Params{})
	rangedFight(r)
	d := open(r)
	if d == nil || d.Action != "auto_shot" {
		t.Fatalf("%+v idle %q", d, r.idle())
	}
	for _, rec := range r.recent() {
		if len(rec.Action) > 4 && rec.Action[:4] == "pet_" {
			t.Fatalf("a pet action without a pet game: %+v", rec)
		}
	}
}

func TestPetSpellData(t *testing.T) {
	for _, c := range []struct {
		id    uint32
		name  string
		level uint8
	}{{spCallPet, "Call Pet", 10}, {spRevivePet, "Revive Pet", 10}, {spMendPet, "Mend Pet", 60}, {spGrowl, "Growl", 60}, {spTorment, "Torment", 10}} {
		if got := spells.Name(c.id); got != c.name {
			t.Errorf("%d is %q, want %q", c.id, got, c.name)
			continue
		}
		if m, ok := spells.Lookup(c.id); !ok || m.Level != c.level {
			t.Errorf("%s: %+v", c.name, m)
		}
	}
	// Call Pet is instant and costs nothing; Revive Pet is a 10 s cast that costs the whole base mana (the rule keeps it out of a fight);
	// Mend Pet is a 5 s channel that reaches 20 yd (petMendMaxRange), 480 mana at rank 7.
	if m, _ := spells.Lookup(spCallPet); m.CastMS != 0 || m.Cost != 0 || m.CostPct != 0 {
		t.Errorf("Call Pet: %+v", m)
	}
	if m, _ := spells.Lookup(spRevivePet); m.CastMS != 10000 || m.CostPct != 100 {
		t.Errorf("Revive Pet: %+v", m)
	}
	if m, _ := spells.Lookup(spMendPet); !m.Flags.Has(spells.FlagChanneled) || m.RangeMax != petMendMaxRange || m.Cost != 480 || m.DurationMS != 5000 {
		t.Errorf("Mend Pet: %+v", m)
	}
	// The pet's taunts are the ones the rule names; the pet pays Growl in focus.
	if m, _ := spells.Lookup(spGrowl); m.Power != spells.PowerFocus || m.RangeMax != 5 {
		t.Errorf("Growl: %+v", m)
	}
	for _, id := range []uint32{spCallPet, spRevivePet, spMendPet, 883, 2649, spGrowl, spTorment, 17735, 17752} {
		if !isPetTaunt(spells.Name(id)) && (spells.Name(id) == "Growl" || spells.Name(id) == "Torment" || spells.Name(id) == "Suffering") {
			t.Errorf("%s is a taunt", spells.Name(id))
		}
	}
	assertNoStances(t, "pet", []uint32{spCallPet, spRevivePet, spMendPet, 982, 136, 3111, 3661, 3662, 13542, 13543, 688, 697, 712, 691})
	ci := ClassOf(ClassHunter)
	if _, ok := petKits[ClassHunter]; !ok || ci.Power != powerMana {
		t.Errorf("%+v", ci)
	}
}
