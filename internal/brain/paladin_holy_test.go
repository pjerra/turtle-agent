package brain

import (
	"testing"
	"time"

	"github.com/pjerra/turtle-agent/internal/objects"
	"github.com/pjerra/turtle-agent/internal/packet"
	"github.com/pjerra/turtle-agent/internal/spells"
)

// Spells of the holy paladin tests (ids from internal/spells/meta.tsv.gz; TestHolyPaladinSpellData checks the facts the brain relies on).
const (
	plHL4, plHL7, plHL9     = 1026, 10328, 25292 // Holy Light ranks 4 (level 22), 7 (46), 9 (60)
	plFoL3, plFoL5          = 19940, 19942       // Flash of Light ranks 3 (level 34) and 5 (50)
	plCleanse, plPurify     = 4987, 1152         // level 42 and 8
	plWisdom4               = 19853              // Blessing of Wisdom rank 4, level 44
	plSalvation             = 1038               // Blessing of Salvation, level 26
	plConcentration         = 19746              // Concentration Aura, level 22
	plBoP3                  = 10278              // Hand of Protection rank 3, level 38
	plShield2               = 1020               // Divine Shield rank 2, level 50
	plSoLight3, plSoWisdom4 = 20348, 51745       // Seals of Light (level 50) and Wisdom (48)
	plRedemption            = 20772              // Redemption rank 4, level 48
	plForbearance           = 25771              // the debuff of Divine Shield and Hand of Protection
	plMobC                  = packet.GUID(102)   // a third mob
)

var holyPalKnown = []uint32{plHL4, plHL7, plFoL3, plFoL5, plCleanse, plPurify, plWisdom4, spMight, spKings, plSalvation, plConcentration, spDevotionAur,
	spLayOnHands, plBoP3, plShield2, spHammerJ}

func nameOf(id uint32) string { return spells.Name(id) }

// holyPalRig is a level 50 holy paladin in a fight, with 3000 mana and 1000 health, its aura up, on a fake that cannot walk.
func holyPalRig(t *testing.T, known ...uint32) (*rig, *fakeGame) {
	t.Helper()
	if len(known) == 0 {
		known = holyPalKnown
	}
	g := newFake(known...)
	g.self.Class, g.self.Level = ClassPaladin, 50
	g.self.Health, g.self.MaxHealth = 1000, 1000
	g.self.PowerType, g.self.Power, g.self.MaxPower = 0, 3000, 3000
	g.self.InCombat = true
	g.self.Auras = []objects.Aura{{Name: "Concentration Aura", SpellID: plConcentration}}
	return newRig(t, g, Params{Strategies: []string{"holy_paladin"}}), g
}

// calm takes the fight out of the scene: the paladin and every member out of combat (a rest gate).
func calm(g *fakeGame) {
	g.setSelf(func(u *objects.Unit) { u.InCombat = false })
	g.mu.Lock()
	for i := range g.units {
		g.units[i].InCombat = false
	}
	g.mu.Unlock()
}

func TestHolyPaladinPresetPlaysTheHealerRole(t *testing.T) {
	b, err := New(newFake(), DefaultConfig(), Params{Strategies: []string{"holy_paladin"}})
	if err != nil {
		t.Fatal(err)
	}
	if b.p.Role != RoleHealer {
		t.Fatalf("role %q", b.p.Role)
	}
	want := "base,role_healer,paladin_holy,rez,healer_position,avoid_ground,class_interrupt,combat_consumables,rest_eat,rest_drink"
	if got := joinNames(b.Strategies()); got != want {
		t.Fatalf("%s", got)
	}
	if ClassOf(ClassPaladin).Heals != &paladinHeals {
		t.Fatal("the class table gives the paladin no heals")
	}
	// The optional judging strategy is not in the preset.
	if _, err := New(newFake(), DefaultConfig(), Params{Strategies: []string{"holy_paladin", "paladin_judge"}}); err != nil {
		t.Fatal(err)
	}
}

func joinNames(s []string) string {
	out := ""
	for i, n := range s {
		if i > 0 {
			out += ","
		}
		out += n
	}
	return out
}

// ---- heals and dispels ----

func TestHolyPaladinHealsWithHolyLightAndKeepsFlashOfLightForLowHealth(t *testing.T) {
	r, g := holyPalRig(t)
	g.member(dpsG1, "Mage", 8, 700, 1000, 5, 0) // 300 short: a Holy Light covers it
	d := r.tick(100 * time.Millisecond)
	if d == nil || d.Action != "heal" || d.Target != dpsG1 || nameOf(d.Spell) != "Holy Light" {
		t.Fatalf("%+v idle %q", d, r.idle())
	}
	// Low (under 25%): the biggest quick heal, Flash of Light rank 5.
	g.setHP(dpsG1, 200)
	d = r.tick(3 * time.Second)
	if d == nil || d.Action != "heal" || d.Spell != plFoL5 || d.Rel != RelInterrupt+1 {
		t.Fatalf("%+v", d)
	}
	// Flash of Light heals through a script effect in this data: the amount is counted all the same.
	if m, _ := spells.Lookup(plFoL5); healAmount(m, 50) < 250 || !isHealSpell(m) || m.Flags.Has(spells.FlagHeal) {
		t.Fatalf("%+v %v", m, healAmount(m, 50))
	}
}

func TestHolyPaladinWithDivineFavorUpCastsTheBigHealInAnEmergency(t *testing.T) {
	r, g := holyPalRig(t)
	g.setSelf(func(u *objects.Unit) { u.Auras = append(u.Auras, objects.Aura{Name: "Divine Favor", SpellID: 51316}) })
	g.member(dpsG1, "Mage", 8, 200, 1000, 5, 0)
	d := r.tick(100 * time.Millisecond)
	if d == nil || d.Action != "heal" || nameOf(d.Spell) != "Holy Light" {
		t.Fatalf("%+v", d)
	}
}

func TestDivineFavorOfThisDataIsATalentPassiveAndIsNeverCast(t *testing.T) {
	// The five ranks 51312 to 51316 are passives (flag 16385 in the data): a spellbook with them casts nothing.
	r, g := holyPalRig(t, append([]uint32{51316}, holyPalKnown...)...)
	g.member(dpsG1, "Mage", 8, 200, 1000, 5, 0)
	for i := 0; i < 3; i++ {
		d := r.tick(3 * time.Second)
		if d == nil || d.Action == "divine_favor" {
			t.Fatalf("%+v", d)
		}
	}
	for _, c := range g.castList() {
		if c.spell == 51316 {
			t.Fatal("the passive was cast")
		}
	}
	if m, _ := spells.Lookup(51316); !m.Flags.Has(spells.FlagPassive) {
		t.Fatalf("%+v", m)
	}
}

func TestHolyPaladinDispelsWithCleanseElsePurify(t *testing.T) {
	poly := objects.Aura{SpellID: auraPolymorph, Name: "Polymorph", Harmful: true}
	poison := objects.Aura{SpellID: auraPoison, Name: "Deadly Poison", Harmful: true}
	curse := objects.Aura{SpellID: auraCurse, Name: "Curse of Agony", Harmful: true}
	try := func(known []uint32, a objects.Aura) *Decision {
		r, g := holyPalRig(t, known...)
		g.member(dpsG1, "Mage", 8, 1000, 1000, 5, 0)
		g.addAura(dpsG1, a)
		return r.tick(100 * time.Millisecond)
	}
	if d := try(holyPalKnown, poly); d == nil || d.Action != "dispel" || d.Spell != plCleanse || d.Target != dpsG1 {
		t.Errorf("magic with Cleanse: %+v", d)
	}
	if d := try(holyPalKnown, poison); d == nil || d.Spell != plCleanse {
		t.Errorf("poison with Cleanse: %+v", d)
	}
	onlyPurify := []uint32{plHL4, plPurify}
	if d := try(onlyPurify, poison); d == nil || d.Action != "dispel" || d.Spell != plPurify {
		t.Errorf("poison with Purify: %+v", d)
	}
	if d := try(onlyPurify, objects.Aura{SpellID: auraRabies, Name: "Rabies", Harmful: true}); d == nil || d.Spell != plPurify {
		t.Errorf("disease with Purify: %+v", d)
	}
	if d := try(onlyPurify, poly); d != nil {
		t.Errorf("Purify does not remove magic: %+v", d)
	}
	if d := try(holyPalKnown, curse); d != nil {
		t.Errorf("a curse is neither's: %+v", d)
	}
}

// ---- blessings ----

func blessed(g *fakeGame, guid packet.GUID, name string) {
	if guid == selfG {
		g.setSelf(func(u *objects.Unit) { u.Auras = append(u.Auras, objects.Aura{Name: name}) })
		return
	}
	g.addAura(guid, objects.Aura{Name: name})
}

// partyOfFive is a calm party at a rest gate: the tank (a warrior), a mage, a rogue and a priest, all within reach.
func partyOfFive(g *fakeGame) {
	g.member(tankG, "Tankard", 1, 1000, 1000, 6, 0)
	g.member(dpsG1, "Mage", 8, 1000, 1000, 6, 3)
	g.member(dpsG2, "Rogue", 4, 1000, 1000, 6, -3)
	g.member(dpsG3, "Priest", 5, 1000, 1000, 9, 0)
	calm(g)
}

func TestBlessingsGoOnByRoleAndNeverSalvationOnTheTank(t *testing.T) {
	r, g := holyPalRig(t)
	partyOfFive(g)
	var got []struct {
		to   packet.GUID
		name string
	}
	for i := 0; i < 6; i++ {
		d := r.tick(2 * time.Second)
		if d == nil {
			break
		}
		if d.Action != "bless_member" || d.Rel != RelNormal+2 {
			t.Fatalf("%+v", d)
		}
		got = append(got, struct {
			to   packet.GUID
			name string
		}{d.Target, nameOf(d.Spell)})
		blessed(g, d.Target, nameOf(d.Spell))
	}
	want := []struct {
		to   packet.GUID
		name string
	}{{selfG, "Blessing of Wisdom"}, {tankG, "Blessing of Kings"}, {dpsG1, "Blessing of Wisdom"}, {dpsG2, "Blessing of Might"}, {dpsG3, "Blessing of Wisdom"}}
	if len(got) != len(want) {
		t.Fatalf("got %v", got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("blessing %d: %v, want %v", i, got[i], want[i])
		}
	}
	// With no Kings and no Might the tank gets nothing rather than Salvation, and a mage gets Salvation when it is all there is.
	r2, g2 := holyPalRig(t, plSalvation, plConcentration)
	g2.member(tankG, "Tankard", 1, 1000, 1000, 6, 0)
	g2.member(dpsG1, "Mage", 8, 1000, 1000, 6, 3)
	calm(g2)
	blessed(g2, selfG, "Blessing of Salvation")
	d := r2.tick(2 * time.Second)
	if d == nil || d.Target != dpsG1 || d.Spell != plSalvation {
		t.Fatalf("%+v", d)
	}
	blessed(g2, dpsG1, "Blessing of Salvation")
	if d := r2.tick(2 * time.Second); d != nil {
		t.Fatalf("the tank was blessed with Salvation: %+v", d)
	}
}

func TestBlessingsNeverOverwriteACompanionPaladinsBlessing(t *testing.T) {
	r, g := holyPalRig(t)
	partyOfFive(g)
	blessed(g, selfG, "Blessing of Kings")         // any blessing on the paladin itself counts
	blessed(g, tankG, "Blessing of Might")         // a companion's, not the one this paladin would give
	blessed(g, dpsG1, "Greater Blessing of Kings") // the group version counts too
	d := r.tick(2 * time.Second)
	if d == nil || d.Action != "bless_member" || d.Target != dpsG2 {
		t.Fatalf("only the rogue lacks a blessing: %+v", d)
	}
	blessed(g, dpsG2, "Blessing of Might")
	d = r.tick(2 * time.Second)
	if d == nil || d.Target != dpsG3 {
		t.Fatalf("%+v", d)
	}
	blessed(g, dpsG3, "Blessing of Light")
	if d := r.tick(2 * time.Second); d != nil {
		t.Fatalf("every member carries a blessing: %+v", d)
	}
}

func TestBlessingsOfThePaladinItselfAreRenewedUnderAMinute(t *testing.T) {
	r, g := holyPalRig(t)
	g.member(tankG, "Tankard", 1, 1000, 1000, 6, 0)
	calm(g)
	blessed(g, tankG, "Blessing of Kings")
	g.setSelf(func(u *objects.Unit) {
		u.Auras = append(u.Auras, objects.Aura{Name: "Blessing of Wisdom", SpellID: plWisdom4, RemainingMS: ms(400_000)})
	})
	if d := r.tick(2 * time.Second); d != nil {
		t.Fatalf("%+v", d)
	}
	g.setSelf(func(u *objects.Unit) {
		for i := range u.Auras {
			if u.Auras[i].Name == "Blessing of Wisdom" {
				u.Auras[i].RemainingMS = ms(30_000)
			}
		}
	})
	d := r.tick(2 * time.Second)
	if d == nil || d.Action != "bless_member" || d.Target != selfG || d.Spell != plWisdom4 {
		t.Fatalf("%+v", d)
	}
}

func TestBlessingsOnlyAtARestGateInReachAndWithMana(t *testing.T) {
	r, g := holyPalRig(t)
	g.member(tankG, "Tankard", 1, 1000, 1000, 6, 0)
	g.member(dpsG1, "Mage", 8, 1000, 1000, 40, 0) // out of reach
	calm(g)
	blessed(g, selfG, "Blessing of Wisdom")
	blessed(g, tankG, "Blessing of Kings")
	if d := r.tick(2 * time.Second); d != nil {
		t.Fatalf("a member out of reach was blessed: %+v", d)
	}
	g.mutate(dpsG1, func(u *objects.Unit) { u.X = 10 })
	r.b.restOK = func() bool { return false }
	if d := r.tick(2 * time.Second); d != nil {
		t.Fatalf("blessed on the walk: %+v", d)
	}
	r.b.restOK = func() bool { return true }
	g.setMana(20)
	if d := r.tick(2 * time.Second); d != nil && d.Action == "bless_member" {
		t.Fatalf("blessed on low mana: %+v", d)
	}
	g.setMana(100)
	g.setSelf(func(u *objects.Unit) { u.InCombat = true })
	if d := r.tick(2 * time.Second); d != nil && d.Action == "bless_member" {
		t.Fatalf("blessed in combat: %+v", d)
	}
}

// ---- the aura ----

func TestHolyPaladinKeepsOneAuraAndDoesNotFightACompanions(t *testing.T) {
	cast := func(known []uint32, auras ...string) *Decision {
		r, g := holyPalRig(t, known...)
		calm(g)
		g.setSelf(func(u *objects.Unit) {
			u.Auras = nil
			for _, a := range auras {
				u.Auras = append(u.Auras, objects.Aura{Name: a})
			}
		})
		return r.tick(100 * time.Millisecond)
	}
	if d := cast([]uint32{plConcentration, spDevotionAur}); d == nil || d.Action != "holy_aura" || d.Spell != plConcentration || d.Rel != RelNormal+3 {
		t.Errorf("Concentration first: %+v", d)
	}
	if d := cast([]uint32{spDevotionAur}); d == nil || d.Spell != spDevotionAur {
		t.Errorf("Devotion when there is no Concentration: %+v", d)
	}
	// An aura of a companion on the paladin (it can be any of them) is the aura: nothing is cast over it.
	for _, have := range []string{"Concentration Aura", "Devotion Aura", "Retribution Aura"} {
		if d := cast([]uint32{plConcentration, spDevotionAur}, have); d != nil && d.Action == "holy_aura" {
			t.Errorf("fought a %s: %+v", have, d)
		}
	}
	if d := cast([]uint32{spLayOnHands}); d != nil {
		t.Errorf("no aura known: %+v", d)
	}
}

// ---- the emergencies ----

func mobsOn(g *fakeGame, target packet.GUID, x, y float32, guids ...packet.GUID) {
	for i, m := range guids {
		g.addUnit(mob(m, target), x, y+float32(i))
	}
}

func TestLayOnHandsOnTheMostCriticalBelowFifteenPercentInAFight(t *testing.T) {
	r, g := holyPalRig(t)
	g.member(tankG, "Tankard", 1, 1000, 1000, 6, 0)
	g.member(dpsG1, "Mage", 8, 120, 1000, 5, 0) // 12%
	d := r.tick(100 * time.Millisecond)
	if d == nil || d.Action != "lay_on_hands" || d.Target != dpsG1 || d.Spell != spLayOnHands || d.Rel != relEmergency+2 {
		t.Fatalf("%+v idle %q", d, r.idle())
	}
	// 16% is a heal's business.
	r2, g2 := holyPalRig(t)
	g2.member(dpsG1, "Mage", 8, 160, 1000, 5, 0)
	if d := r2.tick(100 * time.Millisecond); d == nil || d.Action != "heal" {
		t.Fatalf("%+v", d)
	}
	// The paladin itself counts, and the one with the least health wins.
	r3, g3 := holyPalRig(t)
	g3.member(dpsG1, "Mage", 8, 140, 1000, 5, 0)
	g3.setSelf(func(u *objects.Unit) { u.Health = 90 })
	if d := r3.tick(100 * time.Millisecond); d == nil || d.Action != "lay_on_hands" || d.Target != selfG {
		t.Fatalf("%+v", d)
	}
}

func TestLayOnHandsNotOutOfAFightOrOutOfSightOrReach(t *testing.T) {
	r, g := holyPalRig(t)
	g.member(dpsG1, "Mage", 8, 100, 1000, 5, 0)
	calm(g)
	if d := r.tick(100 * time.Millisecond); d != nil && d.Action == "lay_on_hands" {
		t.Fatalf("in no fight: %+v", d)
	}
	r2, g2 := holyPalRig(t)
	g2.member(dpsG1, "Mage", 8, 100, 1000, 45, 0) // 38.5 yd is the reach
	if d := r2.tick(100 * time.Millisecond); d != nil && d.Action == "lay_on_hands" {
		t.Fatalf("out of reach: %+v", d)
	}
	// The spell is on its hour cooldown after it went off.
	r3, g3 := holyPalRig(t)
	g3.member(dpsG1, "Mage", 8, 100, 1000, 5, 0)
	if d := r3.tick(100 * time.Millisecond); d == nil || d.Action != "lay_on_hands" {
		t.Fatalf("%+v", d)
	}
	g3.setHP(dpsG1, 100)
	if d := r3.tick(3 * time.Second); d == nil || d.Action == "lay_on_hands" {
		t.Fatalf("a second Lay on Hands: %+v", d)
	}
}

func TestHandOfProtectionOnAMemberBeatenInMeleeNeverTheTank(t *testing.T) {
	setup := func() (*rig, *fakeGame) {
		r, g := holyPalRig(t)
		g.member(tankG, "Tankard", 1, 1000, 1000, 12, 0)
		g.member(dpsG1, "Mage", 8, 300, 1000, 5, 0) // 30%
		return r, g
	}
	// Two mobs on the mage: Hand of Protection (rank 3), above the emergency heal.
	r, g := setup()
	mobsOn(g, dpsG1, 6, 0, mobA, mobB)
	d := r.tick(100 * time.Millisecond)
	if d == nil || d.Action != "hand_of_protection" || d.Target != dpsG1 || d.Spell != plBoP3 || d.Rel != relEmergency+1 {
		t.Fatalf("%+v idle %q", d, r.idle())
	}
	// One mob and no burst: a heal.
	r, g = setup()
	mobsOn(g, dpsG1, 6, 0, mobA)
	if d := r.tick(100 * time.Millisecond); d == nil || d.Action != "heal" {
		t.Fatalf("one mob: %+v", d)
	}
	// One mob and a burst (a quarter of the health lost within three seconds): Hand of Protection.
	r, g = setup()
	g.setHP(dpsG1, 1000)
	mobsOn(g, dpsG1, 6, 0, mobA)
	r.tick(100 * time.Millisecond)
	g.setHP(dpsG1, 300)
	if d := r.tick(500 * time.Millisecond); d == nil || d.Action != "hand_of_protection" {
		t.Fatalf("a burst: %+v", d)
	}
	// Never the tank, however hard it is hit.
	r, g = holyPalRig(t)
	g.member(tankG, "Tankard", 1, 300, 1000, 5, 0)
	mobsOn(g, tankG, 6, 0, mobA, mobB, plMobC)
	if d := r.tick(100 * time.Millisecond); d == nil || d.Action == "hand_of_protection" || d.Target != tankG {
		t.Fatalf("the tank was protected: %+v", d)
	}
	// Not a member with Forbearance, nor one that is above 40%.
	r, g = setup()
	mobsOn(g, dpsG1, 6, 0, mobA, mobB)
	g.addAura(dpsG1, objects.Aura{Name: "Forbearance", SpellID: plForbearance, Harmful: true})
	if d := r.tick(100 * time.Millisecond); d == nil || d.Action == "hand_of_protection" {
		t.Fatalf("Forbearance: %+v", d)
	}
	r, g = setup()
	mobsOn(g, dpsG1, 6, 0, mobA, mobB)
	g.setHP(dpsG1, 450)
	if d := r.tick(100 * time.Millisecond); d != nil && d.Action == "hand_of_protection" {
		t.Fatalf("at 45%%: %+v", d)
	}
}

func TestDivineShieldWhenAboutToDieAndWithoutForbearance(t *testing.T) {
	noLoH := []uint32{plHL4, plFoL5, plShield2, spHammerJ}
	setup := func(hp uint32, known []uint32) (*rig, *fakeGame) {
		r, g := holyPalRig(t, known...)
		g.setSelf(func(u *objects.Unit) { u.Health = hp })
		g.addUnit(mob(mobA, selfG), 3, 0)
		return r, g
	}
	r, g := setup(120, noLoH)
	d := r.tick(100 * time.Millisecond)
	if d == nil || d.Action != "divine_shield" || d.Spell != plShield2 || d.Rel != relEmergency {
		t.Fatalf("%+v idle %q", d, r.idle())
	}
	// The healing goes on under the shield: in this data it only cuts the damage the paladin does (no pacify, no silence).
	g.setSelf(func(u *objects.Unit) {
		u.Auras = append(u.Auras, objects.Aura{Name: "Divine Shield", SpellID: plShield2})
	})
	g.member(dpsG1, "Mage", 8, 400, 1000, 5, 0)
	g.mutate(mobA, func(u *objects.Unit) { u.Target = dpsG1 })
	if d := r.tick(3 * time.Second); d == nil || d.Action != "heal" {
		t.Fatalf("no heal under Divine Shield: %+v", d)
	}
	// Lay on Hands on itself comes first when the paladin has it.
	r2, _ := setup(120, holyPalKnown)
	if d := r2.tick(100 * time.Millisecond); d == nil || d.Action != "lay_on_hands" || d.Target != selfG {
		t.Fatalf("%+v", d)
	}
	// Not with Forbearance, not above 15%, not with nothing hitting it.
	r3, g3 := setup(120, noLoH)
	g3.setSelf(func(u *objects.Unit) {
		u.Auras = append(u.Auras, objects.Aura{Name: "Forbearance", SpellID: plForbearance, Harmful: true})
	})
	if d := r3.tick(100 * time.Millisecond); d == nil || d.Action == "divine_shield" {
		t.Fatalf("Forbearance: %+v", d)
	}
	r4, _ := setup(300, noLoH)
	if d := r4.tick(100 * time.Millisecond); d != nil && d.Action == "divine_shield" {
		t.Fatalf("at 30%%: %+v", d)
	}
	r5, g5 := holyPalRig(t, noLoH...)
	g5.setSelf(func(u *objects.Unit) { u.Health = 120 })
	if d := r5.tick(100 * time.Millisecond); d != nil && d.Action == "divine_shield" {
		t.Fatalf("nothing on it, no burst: %+v", d)
	}
}

func TestHammerOfJusticeStunsACastThatHurtsTheParty(t *testing.T) {
	r, g := holyPalRig(t)
	g.member(tankG, "Tankard", 1, 1000, 1000, 12, 0)
	g.addUnit(mob(mobA, tankG), 8, 0)
	g.mutate(mobA, func(u *objects.Unit) {
		u.Casting = &objects.Cast{SpellID: spShadowBolt, EndsInMS: ms(1500), Target: tankG}
	})
	d := r.tick(100 * time.Millisecond)
	if d == nil || d.Action != "interrupt" || d.Spell != spHammerJ || d.Target != mobA || d.Rel != RelInterrupt {
		t.Fatalf("%+v idle %q", d, r.idle())
	}
}

// ---- paladin_judge ----

func judgeRig(t *testing.T, strategies []string, known ...uint32) *rig {
	t.Helper()
	g := newFake(known...)
	g.self.Class, g.self.Level = ClassPaladin, 50
	g.self.Health, g.self.MaxHealth = 1000, 1000
	g.self.PowerType, g.self.Power, g.self.MaxPower = 0, 3000, 3000
	g.self.InCombat = true
	g.self.Auras = []objects.Aura{{Name: "Concentration Aura", SpellID: plConcentration}}
	tankFights(g, 7, 4, 0)
	return newRig(t, g, Params{Strategies: strategies})
}

func TestJudgeIsOffByDefaultAndSealsAndJudgesWhenAskedFor(t *testing.T) {
	known := []uint32{plHL4, plConcentration, plSoLight3, plSoWisdom4, spJudgement}
	if d := judgeRig(t, []string{"holy_paladin"}, known...).tick(100 * time.Millisecond); d != nil {
		t.Fatalf("the preset alone judged: %+v", d)
	}
	r := judgeRig(t, []string{"holy_paladin", "paladin_judge"}, known...)
	d := r.tick(100 * time.Millisecond)
	if d == nil || d.Action != "judge_seal" || d.Spell != plSoLight3 {
		t.Fatalf("%+v idle %q", d, r.idle())
	}
	setSeal(r.g, "Seal of Light", plSoLight3)
	d = r.tick(3 * time.Second)
	if d == nil || d.Action != "judge" || d.Spell != spJudgement || d.Target != mobA {
		t.Fatalf("%+v idle %q", d, r.idle())
	}
	// With the mana under 80% the seal is Wisdom; under 60% nothing at all; a hurt member comes first; a mob the tank does not hold waits.
	r2 := judgeRig(t, []string{"holy_paladin", "paladin_judge"}, known...)
	r2.g.setMana(70)
	if d := r2.tick(100 * time.Millisecond); d == nil || d.Spell != plSoWisdom4 {
		t.Fatalf("%+v", d)
	}
	r3 := judgeRig(t, []string{"holy_paladin", "paladin_judge"}, known...)
	r3.g.setMana(50)
	if d := r3.tick(100 * time.Millisecond); d != nil {
		t.Fatalf("%+v", d)
	}
	r4 := judgeRig(t, []string{"holy_paladin", "paladin_judge"}, known...)
	r4.g.setHP(tankG, 400)
	if d := r4.tick(100 * time.Millisecond); d == nil || d.Action != "heal" {
		t.Fatalf("%+v", d)
	}
	r5 := judgeRig(t, []string{"holy_paladin", "paladin_judge"}, known...)
	r5.g.mutate(mobA, func(u *objects.Unit) { u.Target = selfG })
	if d := r5.tick(100 * time.Millisecond); d != nil && (d.Action == "judge_seal" || d.Action == "judge") {
		t.Fatalf("%+v", d)
	}
}

// ---- the spell data the holy paladin relies on ----

func TestHolyPaladinSpellData(t *testing.T) {
	for _, c := range []struct {
		id        uint32
		name      string
		level     uint8
		cost, pct uint32
		rng       float32
		castMS    uint32
	}{
		{635, "Holy Light", 1, 35, 0, 40, 2500}, {plHL4, "Holy Light", 22, 190, 0, 40, 2500}, {plHL7, "Holy Light", 46, 465, 0, 40, 2500},
		{plHL9, "Holy Light", 60, 660, 0, 40, 2500},
		{19750, "Flash of Light", 20, 35, 0, 40, 1500}, {plFoL3, "Flash of Light", 34, 70, 0, 40, 1500}, {plFoL5, "Flash of Light", 50, 115, 0, 40, 1500},
		{51743, "Flash of Light", 60, 180, 0, 40, 1500},
		{plCleanse, "Cleanse", 42, 0, 8, 30, 0}, {plPurify, "Purify", 8, 0, 8, 30, 0},
		{plWisdom4, "Blessing of Wisdom", 44, 90, 0, 30, 0}, {plSalvation, "Blessing of Salvation", 26, 0, 8, 30, 0},
		{spKings, "Blessing of Kings", 20, 0, 8, 30, 0}, {spMight, "Blessing of Might", 42, 85, 0, 30, 0},
		{plConcentration, "Concentration Aura", 22, 0, 0, 0, 0}, {spDevotionAur, "Devotion Aura", 50, 0, 0, 0, 0},
		{spLayOnHands, "Lay on Hands", 50, 0, 0, 40, 0}, {plBoP3, "Hand of Protection", 38, 0, 7, 30, 0},
		{plShield2, "Divine Shield", 50, 110, 0, 0, 0}, {spHammerJ, "Hammer of Justice", 40, 75, 0, 10, 0},
		{plSoLight3, "Seal of Light", 50, 180, 0, 0, 0}, {plSoWisdom4, "Seal of Wisdom", 48, 170, 0, 0, 0}, {spJudgement, "Judgement", 4, 0, 6, 10, 0},
		{plRedemption, "Redemption", 48, 0, 80, 30, 10000},
	} {
		if got := spells.Name(c.id); got != c.name {
			t.Errorf("%d is %q, want %q", c.id, got, c.name)
			continue
		}
		m, ok := spells.Lookup(c.id)
		if !ok || m.Level != c.level || m.Cost != c.cost || m.CostPct != c.pct || m.RangeMax != c.rng || m.CastMS != c.castMS {
			t.Errorf("%s: %+v", c.name, m)
		}
	}
	// Holy Light heals (effect 10); Flash of Light heals through the script effect (77) with the Holy Light's kind of numbers.
	for _, id := range []uint32{plHL4, plHL7, plHL9} {
		if m, _ := spells.Lookup(id); !m.Flags.Has(spells.FlagHeal) || m.Effects[0].Effect != 10 {
			t.Errorf("Holy Light %d: %+v", id, m)
		}
	}
	for _, id := range []uint32{19750, plFoL3, plFoL5, 51743} {
		m, _ := spells.Lookup(id)
		if m.Flags.Has(spells.FlagHeal) || m.Effects[0].Effect != 77 || !isHealSpell(m) {
			t.Errorf("Flash of Light %d: %+v", id, m)
		}
	}
	// Cleanse removes three kinds, Purify two (dispel effects, 38); both are instant and not barred in a fight.
	for id, n := range map[uint32]int{plCleanse: 3, plPurify: 2} {
		m, _ := spells.Lookup(id)
		got := 0
		for _, e := range m.Effects {
			if e.Effect == 38 {
				got++
			}
		}
		if got != n || m.Flags.Has(spells.FlagNotInCombat) {
			t.Errorf("%s: %d dispel effects, %v", spells.Name(id), got, m.Flags.Names())
		}
	}
	// The blessings are 10 minute auras that other paladins' blessings do not replace; the auras have no end.
	for _, id := range []uint32{plWisdom4, plSalvation, spKings, spMight} {
		if m, _ := spells.Lookup(id); m.DurationMS != 600000 || !m.Flags.Has(spells.FlagAura) {
			t.Errorf("%s: %+v", spells.Name(id), m)
		}
	}
	for _, id := range []uint32{plConcentration, spDevotionAur} {
		if m, _ := spells.Lookup(id); m.DurationMS != -1 || !m.Flags.Has(spells.FlagAura) {
			t.Errorf("%s: %+v", spells.Name(id), m)
		}
	}
	// Lay on Hands: an hour. Hand of Protection (the data's name of the Blessing of Protection): five minutes, school immunity (39);
	// Divine Shield: twelve seconds of school immunity that cuts the damage done (79) and does not pacify (25) or silence (27).
	if m, _ := spells.Lookup(spLayOnHands); m.CategoryCDMS != 3600000 {
		t.Errorf("Lay on Hands: %+v", m)
	}
	if m, _ := spells.Lookup(plBoP3); m.CategoryCDMS != 300000 || m.DurationMS != 10000 || m.Effects[0].Aura != 39 {
		t.Errorf("Hand of Protection: %+v", m)
	}
	m, _ := spells.Lookup(plShield2)
	if m.DurationMS != 12000 || m.CategoryCDMS != 300000 || m.Effects[0].Aura != 39 || m.Effects[1].Aura != 79 {
		t.Errorf("Divine Shield: %+v", m)
	}
	for _, e := range m.Effects {
		if e.Aura == 25 || e.Aura == 27 {
			t.Errorf("Divine Shield pacifies or silences in this data: %+v", m)
		}
	}
	// The names the brain looks for on units.
	for id, name := range map[uint32]string{plForbearance: "Forbearance", 51316: "Divine Favor", 25898: "Greater Blessing of Kings",
		25895: "Greater Blessing of Salvation", 19977: "Blessing of Light", 1022: "Hand of Protection", 20186: "Judgement of Wisdom"} {
		if spells.Name(id) != name {
			t.Errorf("%d is %q, want %q", id, spells.Name(id), name)
		}
	}
	// Divine Favor: the five ranks of this data are talent passives (flag 1), which is why the brain never casts it.
	for id := uint32(51312); id <= 51316; id++ {
		if m, _ := spells.Lookup(id); !m.Flags.Has(spells.FlagPassive) || spells.Name(id) != "Divine Favor" {
			t.Errorf("Divine Favor %d: %+v", id, m)
		}
	}
	// No spell of the holy paladin has a stance or form in the core's spell_template (stances and stancesNot are 0 for every rank the
	// brain casts; tortoise-wow 5d79486, sql/base/tw_world_spell_template.sql; the database_updates change none of them).
	assertNoStances(t, "holy paladin", []uint32{635, plHL4, 1042, 3472, plHL7, 10329, plHL9, 19750, 19939, plFoL3, 19941, plFoL5, 19943, 51743,
		plCleanse, plPurify, 19742, 19850, 19852, plWisdom4, 19854, 25290, plSalvation, spKings, spMight, plConcentration, spDevotionAur,
		spLayOnHands, 633, 2800, plBoP3, 1022, 5599, plShield2, 642, spHammerJ, plSoLight3, plSoWisdom4, 20165, spJudgement, plRedemption, 7328})
}
