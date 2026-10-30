package brain

import (
	"math"
	"slices"
	"testing"
	"time"

	"github.com/pjerra/turtle-agent/internal/objects"
	"github.com/pjerra/turtle-agent/internal/spells"
)

// The shaman rotations generated from the Turtle knowledge base (tools/kbrotgen/recipes/kb_*_shaman.json), one play
// test per spec: elemental-ranged-dps.yaml, enhancement-melee-dps.yaml, enhancement-tank.yaml and restoration-healer.yaml.

const (
	shChainHeal3      uint32 = 10623 // Chain Heal rank 3 (level 54), the KB's heavy-damage heal
	shElemMastery     uint32 = 16166 // Elemental Mastery, the KB's Flame Shock snapshot cooldown
	shLightningStrike uint32 = 52422 // Lightning Strike rank 3 (level 56), the KB's Rank 3
	shEarthshaker     uint32 = 51365 // Earthshaker Slam, the shaman tank's taunt
)

// The tank preset is a copy of enhancement_shaman with two strategies swapped (kb_presets_shaman.go writes it out
// because its init() runs before shaman.go's, which registers the sibling): this holds the two together.
func TestTankShamanPresetFollowsTheEnhancementOne(t *testing.T) {
	want := slices.Clone(presets["enhancement_shaman"])
	i := slices.Index(want, "combat_dps")
	j := slices.Index(want, "shaman_enhancement")
	if i < 0 || j < 0 {
		t.Fatalf("enhancement_shaman %v", want)
	}
	want[i] = "role_tank"
	want[j] = "rot:kb_enhancement_tank_shaman"
	if got := presets["tank_shaman"]; !slices.Equal(got, want) {
		t.Fatalf("tank_shaman:\n got %v\nwant %v", got, want)
	}
}

// kbElementalRig knows the spells the KB's list casts and no totems: the totem manager (shaman_totems_elemental)
// is the preset's and would take the first ticks with its own drops.
func kbElementalRig(t *testing.T) (*rig, *casterGame) {
	t.Helper()
	g := newCasterGame(shBolt1, shChainLightning1, shFlame1, shEarth1, 48306, shElemMastery, shLShield5)
	shamanSelf(g.fakeGame)
	g.self.Level = 60
	tankFights(g.fakeGame, 8, 12, 0)
	return rigOn(t, g, g.fakeGame, Params{Strategies: []string{"elemental_shaman"}, Rotation: "kb_elemental_shaman"}), g
}

func TestKBElementalShamanRotationPlays(t *testing.T) {
	r, g := kbElementalRig(t)
	// Elemental Mastery before a fresh Flame Shock (the KB's snapshot order), then the Flame Shock.
	if d := r.tick(100 * time.Millisecond); d == nil || d.Action != "elemental_mastery" || spells.Name(d.Spell) != "Elemental Mastery" {
		t.Fatalf("%+v idle %q", d, r.idle())
	}
	if d := open(r); d == nil || d.Action != "flame_shock" || spells.Name(d.Spell) != "Flame Shock" {
		t.Fatalf("%+v idle %q", d, r.idle())
	}
	mobAura(g.fakeGame, "Flame Shock", shFlame4)
	// Chain Lightning, then Earthquake at two mobs, then the Lightning Bolt filler.
	if d := r.tick(2500 * time.Millisecond); d == nil || d.Action != "chain_lightning" || spells.Name(d.Spell) != "Chain Lightning" {
		t.Fatalf("%+v idle %q", d, r.idle())
	}
	g.addUnit(mob(mobB, tankG), 14, 1)
	if d := r.tick(2500 * time.Millisecond); d == nil || d.Action != "earthquake" || spells.Name(d.Spell) != "Earthquake" {
		t.Fatalf("%+v idle %q", d, r.idle())
	}
	if d := r.tick(2500 * time.Millisecond); d == nil || d.Action != "lightning_bolt" || spells.Name(d.Spell) != "Lightning Bolt" {
		t.Fatalf("%+v idle %q", d, r.idle())
	}
}

func kbEnhRig(t *testing.T) (*rig, *meleeGame) {
	t.Helper()
	g := newFake(append(append([]uint32{}, shEnhKnown...), append(shShields, shLightningStrike)...)...)
	shamanSelf(g)
	g.self.Level = 60
	mg := newMeleeGame(g)
	r := rigOn(t, mg, g, Params{Strategies: []string{"enhancement_shaman"}, Rotation: "kb_enhancement_shaman"})
	tankFights(g, 7, 4, 0)
	g.setSelf(func(u *objects.Unit) { u.InCombat = true })
	return r, mg
}

func TestKBEnhancementShamanRotationPlays(t *testing.T) {
	r, _ := kbEnhRig(t)
	r.tick(100 * time.Millisecond)
	// The swing starts before the first Stormstrike (the KB's opener rule), then Stormstrike on cooldown.
	if d := open(r); d == nil || d.Action != "auto_attack" {
		t.Fatalf("%+v idle %q", d, r.idle())
	}
	if d := r.tick(1600 * time.Millisecond); d == nil || d.Action != "stormstrike" || spells.Name(d.Spell) != "Stormstrike" {
		t.Fatalf("%+v idle %q", d, r.idle())
	}
	// Lightning Strike only with a Lightning Shield up, which the shaman has.
	if d := r.tick(1600 * time.Millisecond); d == nil || d.Action != "lightning_strike" || spells.Name(d.Spell) != "Lightning Strike" {
		t.Fatalf("%+v idle %q", d, r.idle())
	}
	// The shock loop: Flame Shock on a mob without it, Earth Shock after.
	if d := r.tick(1600 * time.Millisecond); d == nil || d.Action != "flame_shock" || spells.Name(d.Spell) != "Flame Shock" {
		t.Fatalf("%+v idle %q", d, r.idle())
	}
	mobAura(r.g, "Flame Shock", shFlame4)
	// The shocks share their category cooldown (6 s): Earth Shock takes the next one.
	saw := false
	for i := 0; i < 4 && !saw; i++ {
		if d := r.tick(1600 * time.Millisecond); d != nil && d.Action == "earth_shock" && spells.Name(d.Spell) == "Earth Shock" {
			saw = true
		}
	}
	if !saw {
		t.Fatalf("no Earth Shock after the shock cooldown: %v (idle %q)", r.g.castList(), r.idle())
	}
}

func kbEnhTankRig(t *testing.T) (*rig, *meleeGame) {
	t.Helper()
	g := newFake(append(append([]uint32{}, shEnhKnown...), append(shShields, shLightningStrike, shEarthshaker)...)...)
	shamanSelf(g)
	g.self.Level = 60
	g.self.Health, g.self.MaxHealth = 2000, 2000
	mg := newMeleeGame(g)
	r := rigOn(t, mg, g, Params{Strategies: []string{"tank_shaman"}})
	m := mob(mobA, selfG)
	m.O = math.Pi // the mob faces the shaman: the shaman is the tank
	g.addUnit(m, 3, 0)
	g.setSelf(func(u *objects.Unit) { u.InCombat = true })
	return r, mg
}

func TestKBEnhancementTankShamanRotationPlays(t *testing.T) {
	r, _ := kbEnhTankRig(t)
	// The tank's list plays from the preset itself (tank_shaman carries rot:kb_enhancement_tank_shaman): the swing
	// first, then Earth Shock on cooldown, then Stormstrike.
	if d := r.tick(1600 * time.Millisecond); d == nil || d.Action != "auto_attack" {
		t.Fatalf("%+v idle %q", d, r.idle())
	}
	if d := r.tick(1600 * time.Millisecond); d == nil || d.Action != "earth_shock" || spells.Name(d.Spell) != "Earth Shock" {
		t.Fatalf("%+v idle %q", d, r.idle())
	}
	if d := r.tick(1600 * time.Millisecond); d == nil || d.Action != "stormstrike" || spells.Name(d.Spell) != "Stormstrike" {
		t.Fatalf("%+v idle %q", d, r.idle())
	}
	// Water Shield below ~66% mana: the KB's swap away from the Lightning Shield.
	r.g.setSelf(func(u *objects.Unit) { u.Power = u.MaxPower * 50 / 100 })
	if d := r.tick(1600 * time.Millisecond); d == nil || d.Action != "water_shield" || spells.Name(d.Spell) != "Water Shield" {
		t.Fatalf("%+v idle %q", d, r.idle())
	}
}

// A shaman tank taunts with Earthshaker Slam (the class row's Taunt): role_tank answers a loose add with it.
func TestKBEnhancementTankShamanTauntsWithEarthshakerSlam(t *testing.T) {
	r, _ := kbEnhTankRig(t)
	r.g.party = []Member{{Name: "Mage", GUID: dpsG1, Online: true}}
	r.g.addUnit(objects.Unit{GUID: dpsG1, Type: "player", Name: "Mage", Class: ClassMage, InParty: true, Health: 500, MaxHealth: 500}, 6, 3)
	r.g.addUnit(mob(mobB, dpsG1), 4, 3) // loose: it attacks the mage
	if d := r.tick(1600 * time.Millisecond); d == nil || d.Action != "taunt" || d.Spell != shEarthshaker || d.Target != mobB {
		t.Fatalf("%+v idle %q", d, r.idle())
	}
}

func kbRestoShamanRig(t *testing.T) (*rig, *casterGame) {
	t.Helper()
	known := append(append([]uint32{}, shHealKnown...), shShields...)
	known = append(known, shChainHeal3)
	g := newCasterGame(known...)
	shamanSelf(g.fakeGame)
	g.self.Level = 60
	g.self.InCombat = true
	g.self.Auras = []objects.Aura{{Name: "Water Shield", SpellID: shWShield2}}
	g.items = append([]objects.Item{}, allTools...)
	return rigOn(t, g, g.fakeGame, Params{Strategies: []string{"resto_shaman"}, Rotation: "kb_restoration_shaman"}), g
}

func TestKBRestorationShamanRotationPlays(t *testing.T) {
	r, g := kbRestoShamanRig(t)
	// Heavy raid damage: three members at 40%, the triage target under 50%: Chain Heal rank 3, the KB's heavy-damage heal.
	g.member(dpsG1, "Mage", ClassMage, 400, 1000, 5, 0)
	g.member(dpsG2, "Rogue", ClassRogue, 400, 1000, 5, 3)
	g.member(dpsG3, "Hunter", ClassHunter, 400, 1000, 5, -3)
	if d := r.tick(100 * time.Millisecond); d == nil || d.Action != "chain_heal_3" || d.Spell != shChainHeal3 {
		t.Fatalf("%+v idle %q", d, r.idle())
	}
	// Light damage: the rank-1 workhorse.
	g.setHP(dpsG1, 800)
	g.setHP(dpsG2, 800)
	g.setHP(dpsG3, 800)
	if d := r.tick(2 * time.Second); d == nil || d.Action != "chain_heal" || d.Spell != shChainHeal {
		t.Fatalf("%+v idle %q", d, r.idle())
	}
	// The KB's emergency: the max-rank Healing Wave on a member under 35%.
	g.setHP(dpsG1, 200)
	g.setHP(dpsG2, 1000)
	g.setHP(dpsG3, 1000)
	if d := r.tick(2 * time.Second); d == nil || d.Action != "swiftness_healing_wave" || spells.Name(d.Spell) != "Healing Wave" {
		t.Fatalf("%+v idle %q", d, r.idle())
	}
}
