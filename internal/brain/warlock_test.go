package brain

import (
	"testing"
	"time"

	"github.com/pjerra/turtle-agent/internal/objects"
	"github.com/pjerra/turtle-agent/internal/spells"
)

// Spells of the warlock tests (ids from internal/spells/meta.tsv.gz; TestWarlockSpellData checks the facts the rotation relies on).
const (
	spBolt       = 25307 // Shadow Bolt rank 10, level 60
	spCorruption = 25311 // rank 7, level 60
	spAgony      = 11713 // rank 6, level 58
	spElements   = 11722 // rank 3, level 60
	spDrainLife  = 11700 // rank 6, level 54
	spDrainSoul  = 51687 // rank 5, level 60
	spLifeTap    = 11689 // rank 6, level 56
	spWlFear     = 6215  // rank 3, level 56
	spCoil       = 17926 // rank 3, level 58
	spWard       = 28610 // Shadow Ward rank 4, level 60
	spDemonArmor = 11735 // rank 7, level 60
	spStoneMajor = 11730 // Create Healthstone (Major), level 58
	spVoidwalker = 697
	spImp        = 688
	itemShard    = 6265
	itemMajorHS  = 9421
	mobC2        = 103
)

var warlockSpells = []uint32{spBolt, spCorruption, spAgony, spElements, spDrainLife, spDrainSoul, spLifeTap, spWlFear, spCoil, spWard, spDemonArmor,
	spStoneMajor, spVoidwalker, spImp}

// newWarlock is a level 60 warlock with 3000 mana, Demon Armor up and a healthstone in the bags, on a fake game with a pet half.
func newWarlock(t *testing.T, p Params, known ...uint32) (*rig, *petFake) {
	t.Helper()
	if len(known) == 0 {
		known = warlockSpells
	}
	r, pf := newPetRig(t, "affliction_warlock", ClassWarlock, 60, 0, 3000, p, known, func(u *objects.Unit) {
		u.Auras = []objects.Aura{{Name: "Demon Armor", SpellID: spDemonArmor}}
	})
	r.g.items = []objects.Item{{Entry: itemMajorHS, Count: 1}}
	return r, pf
}

func setItems(g *fakeGame, shards uint32, entries ...uint32) {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.items = nil
	if shards > 0 {
		g.items = append(g.items, objects.Item{Entry: itemShard, Count: shards})
	}
	for _, e := range entries {
		g.items = append(g.items, objects.Item{Entry: e, Count: 1})
	}
}

// warlockFight is the standard fight: the tank holds a mob 24 yd east, the warlock stands inside its band (18 to 26).
func warlockFight(r *rig, pf *petFake) {
	pf.withPet()
	tankFights(r.g, 22, 24, 0)
	r.tick(100 * time.Millisecond)
}

func TestWarlockOpensWithDamageOverTimeThenFillsWithShadowBolt(t *testing.T) {
	r, pf := newWarlock(t, Params{})
	warlockFight(r, pf)
	pf.pmu.Lock()
	pf.orders = nil
	pf.pmu.Unlock()
	if d := r.tick(100 * time.Millisecond); d != nil || r.idle() != "vetoed:opening_delay" {
		t.Fatalf("it opened before the tank held the mob: %+v idle %q", d, r.idle())
	}
	d := open(r)
	if d == nil || d.Action != "pet_attack" {
		t.Fatalf("the pet goes first, it costs no cast: %+v idle %q", d, r.idle())
	}
	pf.setPet(func(p *PetState) { p.Target = mobA })
	d = r.tick(400 * time.Millisecond)
	if d == nil || d.Action != "corruption" || d.Spell != spCorruption || d.Rel != RelNormal+6 || d.Target != mobA {
		t.Fatalf("%+v idle %q", d, r.idle())
	}
	r.g.mutate(mobA, func(u *objects.Unit) {
		u.Auras = []objects.Aura{{Name: "Corruption", SpellID: spCorruption, Harmful: true}}
	})
	d = r.tick(1600 * time.Millisecond)
	if d == nil || d.Action != "curse" || d.Spell != spAgony || d.Rel != RelNormal+5 {
		t.Fatalf("%+v idle %q", d, r.idle())
	}
	r.g.mutate(mobA, func(u *objects.Unit) {
		u.Auras = append(u.Auras, objects.Aura{Name: "Curse of Agony", SpellID: spAgony, Harmful: true})
	})
	for i := 0; i < 2; i++ {
		if d := r.tick(1600 * time.Millisecond); d == nil || d.Action != "shadow_bolt" || d.Spell != spBolt {
			t.Fatalf("%+v idle %q", d, r.idle())
		}
	}
	if got := casts(r.g); !eq(got, []uint32{spCorruption, spAgony, spBolt, spBolt}) {
		t.Fatalf("casts %v", got)
	}
}

func TestWarlockCursesOncePerMobAndNeverOverAnotherWarlocks(t *testing.T) {
	for _, foreign := range []string{"Curse of Recklessness", "Curse of Doom", "Curse of Agony", "Curse of the Elements"} {
		r, pf := newWarlock(t, Params{})
		warlockFight(r, pf)
		pf.setPet(func(p *PetState) { p.Target = mobA })
		r.g.mutate(mobA, func(u *objects.Unit) {
			u.Auras = []objects.Aura{{Name: "Corruption", SpellID: spCorruption, Harmful: true}, {Name: foreign, SpellID: 1, Harmful: true}}
		})
		for i := 0; i < 3; i++ {
			r.tick(1600 * time.Millisecond)
		}
		for _, id := range casts(r.g) {
			if id == spAgony || id == spElements {
				t.Fatalf("a curse over %s: %v", foreign, casts(r.g))
			}
		}
		if got := casts(r.g); len(got) == 0 || got[0] != spBolt {
			t.Fatalf("with %s on the mob the filler goes on: %v", foreign, got)
		}
	}
	// A mob that is nearly dead is not worth one.
	r, pf := newWarlock(t, Params{})
	warlockFight(r, pf)
	setMobHP(r.g, mobA, 35)
	r.g.mutate(mobA, func(u *objects.Unit) {
		u.Auras = []objects.Aura{{Name: "Corruption", SpellID: spCorruption, Harmful: true}}
	})
	for i := 0; i < 3; i++ {
		r.tick(1600 * time.Millisecond)
	}
	for _, id := range casts(r.g) {
		if id == spAgony {
			t.Fatalf("%v", casts(r.g))
		}
	}
}

func TestWarlockPicksTheElementsCurseWhenOtherCastersAreInTheGroup(t *testing.T) {
	r, pf := newWarlock(t, Params{})
	warlockFight(r, pf)
	pf.setPet(func(p *PetState) { p.Target = mobA })
	r.g.mutate(mobA, func(u *objects.Unit) {
		u.Auras = []objects.Aura{{Name: "Corruption", SpellID: spCorruption, Harmful: true}}
	})
	r.g.party = append(r.g.party, Member{Name: "Mage", GUID: 5, Online: true}, Member{Name: "Priest", GUID: 6, Online: true})
	r.g.addUnit(objects.Unit{GUID: 5, Type: "player", Name: "Mage", Class: ClassMage, InParty: true, Health: 500, MaxHealth: 500}, 10, 3)
	r.g.addUnit(objects.Unit{GUID: 6, Type: "player", Name: "Priest", Class: ClassPriest, InParty: true, Health: 500, MaxHealth: 500}, 10, -3)
	if d := open(r); d == nil || d.Action != "curse" || d.Spell != spElements {
		t.Fatalf("%+v idle %q", d, r.idle())
	}
}

func TestWarlockStandsStillForCastsWithACastTime(t *testing.T) {
	r, pf := newWarlock(t, Params{})
	warlockFight(r, pf)
	open(r)
	pf.setPet(func(p *PetState) { p.Target = mobA })
	r.g.setSelf(func(u *objects.Unit) { u.Moving = true })
	for i := 0; i < 3; i++ {
		r.tick(1600 * time.Millisecond)
	}
	for _, id := range casts(r.g) {
		if id == spBolt || id == spCorruption || id == spDrainLife {
			t.Fatalf("a cast with a cast time on the move: %v", casts(r.g))
		}
	}
	r.g.setSelf(func(u *objects.Unit) { u.Moving = false })
	if d := r.tick(1600 * time.Millisecond); d == nil || d.Action != "corruption" {
		t.Fatalf("%+v idle %q", d, r.idle())
	}
}

func TestWarlockLifeTapsInCombatOnlyWithHealthToSpare(t *testing.T) {
	r, pf := newWarlock(t, Params{})
	warlockFight(r, pf)
	open(r)
	pf.setPet(func(p *PetState) { p.Target = mobA })
	r.g.setSelf(func(u *objects.Unit) { u.InCombat = true; u.Power = 600 }) // 20%
	setHealth(r.g, 50)
	for i := 0; i < 2; i++ {
		r.tick(1600 * time.Millisecond)
	}
	for _, id := range casts(r.g) {
		if id == spLifeTap {
			t.Fatalf("a tap at 50%% health: %v", casts(r.g))
		}
	}
	setHealth(r.g, 90)
	d := r.tick(1600 * time.Millisecond)
	if d == nil || d.Action != "life_tap" || d.Spell != spLifeTap || d.Trigger != "mana_low" || d.Target != selfG {
		t.Fatalf("%+v idle %q", d, r.idle())
	}
	// With the mana back there is no tap.
	r, pf = newWarlock(t, Params{})
	warlockFight(r, pf)
	open(r)
	r.g.setSelf(func(u *objects.Unit) { u.InCombat = true })
	for i := 0; i < 3; i++ {
		r.tick(1600 * time.Millisecond)
	}
	for _, id := range casts(r.g) {
		if id == spLifeTap {
			t.Fatalf("%v", casts(r.g))
		}
	}
}

func TestWarlockLifeTapsBeforeTheDrinkOutOfCombat(t *testing.T) {
	r, pf := newWarlock(t, Params{})
	pf.withPet()
	r.g.setSelf(func(u *objects.Unit) { u.Power = 900 }) // 30%
	d := r.tick(100 * time.Millisecond)
	if d == nil || d.Action != "life_tap" || d.Spell != spLifeTap {
		t.Fatalf("%+v idle %q", d, r.idle())
	}
	// Not when health is short.
	r, pf = newWarlock(t, Params{})
	pf.withPet()
	r.g.setSelf(func(u *objects.Unit) { u.Power = 900 })
	setHealth(r.g, 70)
	for i := 0; i < 2; i++ {
		r.tick(2 * time.Second)
	}
	if got := casts(r.g); len(got) != 0 {
		t.Fatalf("%v", got)
	}
}

func TestWarlockDrainsLifeWhenHurtAndNotOnADyingMob(t *testing.T) {
	r, pf := newWarlock(t, Params{})
	pf.withPet()
	tankFights(r.g, 17, 19, 0) // Drain Life reaches 20 yd
	r.tick(100 * time.Millisecond)
	open(r)
	setHealth(r.g, 40)
	r.g.setSelf(func(u *objects.Unit) { u.InCombat = true })
	pf.setPet(func(p *PetState) { p.Target = mobA })
	d := r.tick(400 * time.Millisecond)
	if d == nil || d.Action != "drain_life" || d.Spell != spDrainLife || d.Trigger != "health_low" || d.Rel != RelNormal+8 {
		t.Fatalf("%+v idle %q", d, r.idle())
	}
	// A mob at 10% dies of its own: no drain on it.
	r, pf = newWarlock(t, Params{})
	pf.withPet()
	tankFights(r.g, 17, 19, 0)
	r.tick(100 * time.Millisecond)
	open(r)
	setHealth(r.g, 40)
	setMobHP(r.g, mobA, 10)
	for i := 0; i < 2; i++ {
		r.tick(1600 * time.Millisecond)
	}
	for _, id := range casts(r.g) {
		if id == spDrainLife {
			t.Fatalf("%v", casts(r.g))
		}
	}
}

func TestWarlockDrainsSoulOnADyingMobWhenShardsAreLow(t *testing.T) {
	for _, c := range []struct {
		name   string
		shards uint32
		level  uint32
		pct    uint32
		want   bool
	}{
		{"low on shards, dying, a green mob", 2, 58, 20, true},
		{"enough shards", 6, 58, 20, false},
		{"a grey mob gives no shard", 2, 40, 20, false},
		{"not dying yet", 2, 58, 60, false},
	} {
		r, pf := newWarlock(t, Params{})
		setItems(r.g, c.shards, itemMajorHS)
		warlockFight(r, pf)
		open(r)
		pf.setPet(func(p *PetState) { p.Target = mobA })
		r.g.mutate(mobA, func(u *objects.Unit) {
			u.Level = c.level
			u.Auras = []objects.Aura{{Name: "Corruption", SpellID: spCorruption, Harmful: true}, {Name: "Curse of Agony", SpellID: spAgony, Harmful: true}}
		})
		setMobHP(r.g, mobA, c.pct)
		d := r.tick(1600 * time.Millisecond)
		got := d != nil && d.Action == "drain_soul" && d.Spell == spDrainSoul
		if got != c.want {
			t.Errorf("%s: %+v idle %q", c.name, d, r.idle())
		}
	}
}

func TestWarlockFearIsAnEmergencyNeverBesideATankOrANearbyPack(t *testing.T) {
	hurt := func(r *rig) {
		r.g.setSelf(func(u *objects.Unit) { u.InCombat = true })
		setHealth(r.g, 30)
	}
	// Alone with a mob on it: Death Coil first (it is the stronger answer), then Fear.
	r, _ := newWarlock(t, Params{})
	r.g.addUnit(mob(mobA, selfG), 8, 0)
	hurt(r)
	d := r.tick(100 * time.Millisecond)
	if d == nil || d.Action != "death_coil" || d.Spell != spCoil || d.Rel != relEmergency {
		t.Fatalf("%+v idle %q", d, r.idle())
	}
	d = r.tick(1600 * time.Millisecond) // the global cooldown of the coil is over
	if d == nil || d.Action != "fear" || d.Spell != spWlFear || d.Target != mobA || d.Trigger != "mob_on_warlock_alone" {
		t.Fatalf("%+v idle %q", d, r.idle())
	}
	// A tank in view: the warlock never feared a mob in a dungeon pack.
	r, _ = newWarlock(t, Params{})
	r.g.partyWithTank()
	r.g.addUnit(mob(mobA, selfG), 8, 0)
	hurt(r)
	for i := 0; i < 4; i++ {
		r.tick(2 * time.Second)
	}
	for _, id := range casts(r.g) {
		if id == spWlFear {
			t.Fatalf("fear beside a tank: %v", casts(r.g))
		}
	}
	// An unengaged mob within 30 yd would be run into.
	r, _ = newWarlock(t, Params{})
	r.g.addUnit(mob(mobA, selfG), 8, 0)
	sleeper := mob(mobC2, 0)
	sleeper.InCombat = false
	r.g.addUnit(sleeper, 25, 0)
	hurt(r)
	for i := 0; i < 4; i++ {
		r.tick(2 * time.Second)
	}
	for _, id := range casts(r.g) {
		if id == spWlFear {
			t.Fatalf("fear with a pack asleep near: %v", casts(r.g))
		}
	}
	// Healthy: neither.
	r, _ = newWarlock(t, Params{})
	r.g.addUnit(mob(mobA, selfG), 8, 0)
	r.g.setSelf(func(u *objects.Unit) { u.InCombat = true })
	for i := 0; i < 3; i++ {
		r.tick(2 * time.Second)
	}
	for _, id := range casts(r.g) {
		if id == spWlFear || id == spCoil {
			t.Fatalf("%v", casts(r.g))
		}
	}
}

func TestWarlockWardsShadowCastsAtIt(t *testing.T) {
	r, pf := newWarlock(t, Params{})
	warlockFight(r, pf)
	open(r)
	pf.setPet(func(p *PetState) { p.Target = mobA })
	r.g.mutate(mobA, func(u *objects.Unit) { u.Casting = &objects.Cast{SpellID: spShadowBolt, Target: selfG} })
	d := r.tick(400 * time.Millisecond)
	if d == nil || d.Action != "shadow_ward" || d.Spell != spWard || d.Rel != RelInterrupt+1 || d.Target != selfG {
		t.Fatalf("%+v idle %q", d, r.idle())
	}
	// A fire spell is not warded.
	r, pf = newWarlock(t, Params{})
	warlockFight(r, pf)
	open(r)
	r.g.mutate(mobA, func(u *objects.Unit) { u.Casting = &objects.Cast{SpellID: 133, Target: selfG} }) // Fireball
	for i := 0; i < 2; i++ {
		r.tick(1600 * time.Millisecond)
	}
	for _, id := range casts(r.g) {
		if id == spWard {
			t.Fatalf("%v", casts(r.g))
		}
	}
}

func TestWarlockUpkeepArmorAndHealthstone(t *testing.T) {
	// Armor down: Demon Armor, out of combat.
	r, pf := newWarlock(t, Params{})
	pf.withPet()
	r.g.setSelf(func(u *objects.Unit) { u.Auras = nil })
	if d := r.tick(100 * time.Millisecond); d == nil || d.Action != "demon_armor" || d.Spell != spDemonArmor {
		t.Fatalf("%+v idle %q", d, r.idle())
	}
	// No healthstone and two shards: make the best one.
	r, pf = newWarlock(t, Params{})
	pf.withPet()
	setItems(r.g, 2)
	if d := r.tick(100 * time.Millisecond); d == nil || d.Action != "create_healthstone" || d.Spell != spStoneMajor {
		t.Fatalf("%+v idle %q", d, r.idle())
	}
	for _, c := range []struct {
		name    string
		shards  uint32
		stones  []uint32
		pet     bool
		mana    uint32
		inFight bool
	}{
		{"one shard stays for the demon", 1, nil, false, 3000, false},
		{"a stone of the tier is in the bags", 2, []uint32{itemMajorHS}, true, 3000, false},
		{"a talent stone of the tier counts", 2, []uint32{19013}, true, 3000, false},
		{"low mana: drink first", 2, nil, true, 900, false},
		{"in a fight", 2, nil, true, 3000, true},
	} {
		r, pf = newWarlock(t, Params{})
		if c.pet {
			pf.withPet()
		}
		setItems(r.g, c.shards, c.stones...)
		r.g.setSelf(func(u *objects.Unit) { u.Power = c.mana })
		if c.inFight {
			tankFights(r.g, 22, 24, 0)
			r.g.setSelf(func(u *objects.Unit) { u.InCombat = true })
		}
		for i := 0; i < 3; i++ {
			r.tick(2 * time.Second)
		}
		for _, id := range casts(r.g) {
			if id == spStoneMajor {
				t.Errorf("%s: %v", c.name, casts(r.g))
			}
		}
	}
	// One shard with the demon out: the stone is made.
	r, pf = newWarlock(t, Params{})
	pf.withPet()
	setItems(r.g, 1)
	if d := r.tick(100 * time.Millisecond); d == nil || d.Action != "create_healthstone" {
		t.Fatalf("%+v idle %q", d, r.idle())
	}
	// A stone of the best known tier in the bags: none is made.
	r, pf = newWarlock(t, Params{}, spBolt, spStoneMajor, spDemonArmor)
	pf.withPet()
	setItems(r.g, 2, itemMajorHS)
	r.tick(100 * time.Millisecond)
	if got := casts(r.g); len(got) != 0 {
		t.Fatalf("%v", got)
	}
}

func TestWarlockSummonsTheVoidwalkerWithAShardElseTheImp(t *testing.T) {
	r, _ := newWarlock(t, Params{})
	setItems(r.g, 1, itemMajorHS)
	d := r.tick(100 * time.Millisecond)
	if d == nil || d.Action != "pet_summon" || d.Spell != spVoidwalker || d.Target != selfG {
		t.Fatalf("%+v idle %q", d, r.idle())
	}
	for i := 0; i < 3; i++ {
		r.tick(2 * time.Second) // within the hold: the 10 s cast is not sent again
	}
	if got := casts(r.g); !eq(got, []uint32{spVoidwalker}) {
		t.Fatalf("%v", got)
	}
	// No shard: the Imp, which needs none.
	r, _ = newWarlock(t, Params{})
	setItems(r.g, 0, itemMajorHS)
	if d := r.tick(100 * time.Millisecond); d == nil || d.Action != "pet_summon" || d.Spell != spImp {
		t.Fatalf("%+v idle %q", d, r.idle())
	}
	// pet_imp asks for the Imp even with a shard.
	r, _ = newWarlock(t, Params{Strategies: []string{"combat_dps", "warlock_affliction", "warlock_upkeep", "pet_imp", "ranged_position", "avoid_ground",
		"combat_consumables", "rest_eat", "rest_drink"}})
	setItems(r.g, 3, itemMajorHS)
	if d := r.tick(100 * time.Millisecond); d == nil || d.Spell != spImp {
		t.Fatalf("%+v idle %q", d, r.idle())
	}
	// In a fight nothing is summoned (a 10 s cast).
	r, _ = newWarlock(t, Params{})
	setItems(r.g, 1, itemMajorHS)
	tankFights(r.g, 22, 24, 0)
	r.g.setSelf(func(u *objects.Unit) { u.InCombat = true })
	for i := 0; i < 3; i++ {
		r.tick(2 * time.Second)
	}
	for _, id := range casts(r.g) {
		if id == spVoidwalker || id == spImp {
			t.Fatalf("%v", casts(r.g))
		}
	}
	// A dead demon is not replaced and not revived: it has no spell for that, and it is gone in 15 s.
	r, pf := newWarlock(t, Params{})
	setItems(r.g, 1, itemMajorHS)
	pf.withPet()
	pf.setPet(func(p *PetState) { p.Dead = true })
	for i := 0; i < 3; i++ {
		r.tick(2 * time.Second)
	}
	if got := casts(r.g); len(got) != 0 {
		t.Fatalf("nothing to revive a demon with: %v", got)
	}
}

func TestWarlockSpellData(t *testing.T) {
	for _, c := range []struct {
		id       uint32
		name     string
		level    uint8
		cost     uint32 // flat mana
		rangeMax float32
	}{
		{spBolt, "Shadow Bolt", 60, 380, 30}, {spCorruption, "Corruption", 60, 340, 30}, {spAgony, "Curse of Agony", 58, 215, 30},
		{spElements, "Curse of the Elements", 60, 200, 30}, {spDrainLife, "Drain Life", 54, 300, 20}, {spDrainSoul, "Drain Soul", 60, 135, 30},
		{spCoil, "Death Coil", 58, 565, 30}, {spWard, "Shadow Ward", 60, 320, 0}, {spDemonArmor, "Demon Armor", 60, 948, 0},
		{spStoneMajor, "Create Healthstone (Major)", 58, 1120, 0},
	} {
		if got := spells.Name(c.id); got != c.name {
			t.Errorf("%d is %q, want %q", c.id, got, c.name)
			continue
		}
		m, ok := spells.Lookup(c.id)
		if !ok || m.Level != c.level || m.Cost != c.cost || m.RangeMax != c.rangeMax || m.Power != spells.PowerMana {
			t.Errorf("%s: %+v", c.name, m)
		}
		if m.Flags.Has(spells.FlagStealthOnly) || m.Flags.Has(spells.FlagNotInCombat) {
			t.Errorf("%s: %v", c.name, m.Flags.Names())
		}
	}
	// Life Tap is paid in health (the cost is in the effect); Fear costs a percent of the base mana; the demons cost a percent too.
	if m, _ := spells.Lookup(spLifeTap); spells.Name(spLifeTap) != "Life Tap" || m.Power != spells.PowerHealth || m.Level != 56 || m.CastMS != 0 {
		t.Errorf("Life Tap: %+v", m)
	}
	if m, _ := spells.Lookup(spWlFear); spells.Name(spWlFear) != "Fear" || m.Level != 56 || m.CostPct != 15 || m.RangeMax != 20 || m.CastMS != 1500 || m.Mechanic != 5 {
		t.Errorf("Fear: %+v", m)
	}
	// The drains are channels; Drain Life and Fear reach 20 yd, which the class row's band (18 to 26) keeps in mind.
	for _, id := range []uint32{spDrainLife, spDrainSoul} {
		if m, _ := spells.Lookup(id); !m.Flags.Has(spells.FlagChanneled) || m.School != 5 {
			t.Errorf("%s: %+v", spells.Name(id), m)
		}
	}
	// Shadow Bolt and Corruption have a cast time (Turtle: Corruption 1.5 s), the curses and Death Coil are instant, Death Coil waits 2 minutes.
	if m, _ := spells.Lookup(spBolt); m.CastMS != 3000 {
		t.Errorf("Shadow Bolt: %+v", m)
	}
	if m, _ := spells.Lookup(spCorruption); m.CastMS != 1500 {
		t.Errorf("Corruption: %+v", m)
	}
	for _, id := range []uint32{spAgony, spElements, spCoil, spWard} {
		if m, _ := spells.Lookup(id); m.CastMS != 0 {
			t.Errorf("%s is instant: %+v", spells.Name(id), m)
		}
	}
	if m, _ := spells.Lookup(spCoil); m.CategoryCDMS != 120000 {
		t.Errorf("Death Coil: %+v", m)
	}
	// The demons: the Voidwalker costs a soul shard (6265) and 100% of the base mana, the Imp 80% and no shard; Create Healthstone takes a shard.
	if m, _ := spells.Lookup(spVoidwalker); spells.Name(spVoidwalker) != "Summon Voidwalker" || m.Level != 10 || m.CostPct != 100 || m.CastMS != 10000 ||
		len(m.Reagents) != 1 || m.Reagents[0].Item != itemShard || m.Reagents[0].Count != 1 {
		t.Errorf("Summon Voidwalker: %+v", m)
	}
	if m, _ := spells.Lookup(spImp); spells.Name(spImp) != "Summon Imp" || m.Level != 1 || m.CostPct != 80 || len(m.Reagents) != 0 {
		t.Errorf("Summon Imp: %+v", m)
	}
	if m, _ := spells.Lookup(spStoneMajor); len(m.Reagents) != 1 || m.Reagents[0].Item != itemShard {
		t.Errorf("Create Healthstone (Major): %+v", m)
	}
	// The class row: mana, a drink, the potion, no interrupt, no taunt, and the band for 20 yd drains.
	if ci := ClassOf(ClassWarlock); ci.Power != powerMana || !ci.Drinks || !ci.ManaPotions || len(ci.Interrupts) != 0 || ci.Taunt != "" || ci.Heals != nil || ci.Band != [2]float64{18, 26} {
		t.Errorf("%+v", ci)
	}
	// The core's spell_template rows carry no stance or form (`stances` and `stancesNot` are 0) for all of them, the pet spells included.
	assertNoStances(t, "warlock", []uint32{spBolt, spCorruption, spAgony, spElements, spDrainLife, spDrainSoul, spLifeTap, spWlFear, spCoil, spWard, spDemonArmor,
		spStoneMajor, spVoidwalker, spImp, 686, 172, 980, 1490, 687, 696, 706, 1086, 11733, 11734, 6202, 6201, 11729, 5699, 689, 1454, 1120, 11659})
	if got := healthstoneMakers[0].spell; got != spells.Name(spStoneMajor) {
		t.Errorf("the best maker is %q", got)
	}
}

func TestGrayLevelAndPresetsWithPets(t *testing.T) {
	for lvl, want := range map[uint32]uint32{5: 0, 10: 4, 30: 22, 39: 31, 40: 31, 45: 35, 59: 47, 60: 51} {
		if got := grayLevel(lvl); got != want {
			t.Errorf("grayLevel(%d) = %d, want %d", lvl, got, want)
		}
	}
	for _, p := range []string{"marksman_hunter", "affliction_warlock"} {
		_, names, err := Resolve([]string{p})
		if err != nil {
			t.Fatal(err)
		}
		has := false
		for _, n := range names {
			has = has || n == "pet"
		}
		if !has {
			t.Errorf("%s: %v has no pet strategy", p, names)
		}
	}
}
