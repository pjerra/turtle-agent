package brain

import (
	"slices"
	"testing"
	"time"

	"github.com/pjerra/turtle-agent/internal/objects"
)

// The knowledge base paladin rotations (tools/kbrotgen): retribution replaces paladin_retribution, protection plays on
// the new protection_paladin preset, holy is an add-mode file after role_healer. Each plays its first runnable entry and
// a core spell of its oracle inside four ticks, and protection_paladin stays a clone of ret_paladin.

const (
	kbpSoR1          = 20154 // Seal of Righteousness rank 1, level 1
	kbpHolyStrike1   = 679   // Holy Strike rank 1, level 4
	kbpCrusStrike1   = 2537  // Crusader Strike rank 1, level 10
	kbpJudgement     = 20271 // Judgement, level 4
	kbpConsecrate1   = 26573 // Consecration rank 1, level 20
	kbpConsecrate5   = 20924 // Consecration rank 5, level 60
	kbpHammerWrath1  = 24275 // Hammer of Wrath rank 1, level 44
	kbpRepentance1   = 20066 // Repentance rank 1, level 40
	kbpDivineShield  = 642   // Divine Shield rank 1, level 34
	kbpRighteousFury = 25780 // Righteous Fury, level 16
	kbpSanctityAura  = 20218 // Sanctity Aura, level 30
	kbpHolyShield1   = 20169 // Holy Shield rank 1, level 30
	kbpHolyShock     = 20473 // Holy Shock rank 1, level 38
	kbpSoWisdom1     = 20166 // Seal of Wisdom rank 1, level 18
)

// TestKBPaladinPresetsAreTheSiblingClones holds protection_paladin to the clone it was written as: ret_paladin with the
// tank role and the KB file in place of the Go retribution strategy.
func TestKBPaladinPresetsAreTheSiblingClones(t *testing.T) {
	want := slices.Clone(presets["ret_paladin"])
	want[slices.Index(want, "combat_dps")] = "role_tank"
	want[slices.Index(want, "paladin_retribution")] = "rot:kb_protection_paladin"
	if got := presets["protection_paladin"]; !slices.Equal(got, want) {
		t.Fatalf("protection_paladin\n got %v\nwant %v", got, want)
	}
}

// kbPaladinRig is a level 60 paladin with a full mana pool, its aura and blessing up.
func kbPaladinRig(t *testing.T, preset, rotation string, known ...uint32) (*rig, *meleeFake) {
	t.Helper()
	return newClassRig(t, preset, ClassPaladin, 60, 0, 3000, Params{Strategies: []string{preset}, Rotation: rotation}, known,
		func(u *objects.Unit) { u.Auras = append([]objects.Aura{}, paladinBuffed...) })
}

// The retribution KB file: the seal first, then the strike that keeps Holy Might up.
func TestKBRetributionPaladinPlays(t *testing.T) {
	r, mf := kbPaladinRig(t, "ret_paladin", "kb_retribution_paladin", kbpSoR1, kbpHolyStrike1, kbpCrusStrike1, kbpJudgement,
		kbpConsecrate1, kbpHammerWrath1, kbpRepentance1, kbpDivineShield)
	meleeFight(r, mf)
	if d := r.tick(2200 * time.Millisecond); d == nil || d.Action != "seal_of_righteousness" {
		t.Fatalf("the seal first: %+v idle %q", d, r.idle())
	}
	r.g.setSelf(func(u *objects.Unit) {
		u.Auras = append(u.Auras, objects.Aura{Name: "Seal of Righteousness", SpellID: kbpSoR1})
	})
	if d := r.tick(1600 * time.Millisecond); d == nil || d.Action != "holy_strike" {
		t.Fatalf("a core spell after the seal: %+v idle %q", d, r.idle())
	}
}

// The protection KB file: Righteous Fury and Sanctity Aura before the pull, then the threat list.
func TestKBProtectionPaladinPlays(t *testing.T) {
	r, mf := kbPaladinRig(t, "protection_paladin", "kb_protection_paladin", kbpRighteousFury, kbpSanctityAura, kbpSoR1,
		kbpHolyStrike1, kbpJudgement, kbpHolyShield1, kbpConsecrate5, kbpSoWisdom1, kbpDivineShield)
	meleeFight(r, mf)
	if d := r.tick(2200 * time.Millisecond); d == nil || d.Action != "righteous_fury" {
		t.Fatalf("Righteous Fury first: %+v idle %q", d, r.idle())
	}
	r.g.setSelf(func(u *objects.Unit) {
		u.Auras = append(u.Auras, objects.Aura{Name: "Righteous Fury", SpellID: kbpRighteousFury})
	})
	if d := r.tick(1600 * time.Millisecond); d == nil || d.Action != "sanctity_aura" {
		t.Fatalf("Sanctity Aura second: %+v idle %q", d, r.idle())
	}
	r.g.setSelf(func(u *objects.Unit) {
		u.Auras = append(u.Auras, objects.Aura{Name: "Sanctity Aura", SpellID: kbpSanctityAura})
	})
	if d := r.tick(1600 * time.Millisecond); d == nil || d.Action != "seal_of_righteousness" {
		t.Fatalf("the seal third: %+v idle %q", d, r.idle())
	}
	r.g.setSelf(func(u *objects.Unit) {
		u.Auras = append(u.Auras, objects.Aura{Name: "Seal of Righteousness", SpellID: kbpSoR1})
	})
	if d := r.tick(1600 * time.Millisecond); d == nil || d.Action != "holy_strike" {
		t.Fatalf("a core spell after the buffs: %+v idle %q", d, r.idle())
	}
}

// The holy KB file is add mode: Seal of Wisdom up, then Holy Light on a tank that just dropped to 55%.
func TestKBHolyPaladinPlays(t *testing.T) {
	r, _ := kbPaladinRig(t, "holy_paladin", "kb_holy_paladin", plHL4, plFoL3, kbpHolyShock, kbpSoWisdom1, kbpJudgement,
		kbpHolyStrike1, kbpConsecrate1, kbpCrusStrike1)
	r.g.member(tankG, "Tankard", ClassWarrior, 550, 1000, 5, 0)
	if d := r.tick(100 * time.Millisecond); d == nil || d.Action != "seal_of_wisdom" || d.Spell != kbpSoWisdom1 {
		t.Fatalf("Seal of Wisdom first: %+v idle %q", d, r.idle())
	}
	r.g.setSelf(func(u *objects.Unit) {
		u.Auras = append(u.Auras, objects.Aura{Name: "Seal of Wisdom", SpellID: kbpSoWisdom1})
	})
	got := actions(r, 1600*time.Millisecond, 1600*time.Millisecond)
	if !slices.Contains(got, "holy_light") {
		t.Fatalf("a core spell within two ticks: %v (idle %q)", got, r.idle())
	}
}
