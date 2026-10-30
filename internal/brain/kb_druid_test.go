package brain

import (
	"math"
	"testing"
	"time"

	"github.com/pjerra/turtle-agent/internal/objects"
	"github.com/pjerra/turtle-agent/internal/spells"
)

// The druid rotations generated from the Turtle knowledge base (tools/kbrotgen/recipes/kb_*_druid.json): each spec's
// list plays on a fake of the class at level 60 with the spec's spells known. One test per spec; the KB rows these pin
// are balance-ranged-dps.yaml, feral-melee-dps.yaml, feral-tank.yaml and restoration-healer.yaml.

// kbBalanceRig is the balance druid with the KB file.
func kbBalanceRig(t *testing.T) (*rig, *casterGame) {
	t.Helper()
	g := newCasterGame(drBalanceKnown...)
	druidSelf(g.fakeGame)
	g.self.Level = 60
	tankFights(g.fakeGame, 25, 30, 0)
	return rigOn(t, g, g.fakeGame, Params{Strategies: []string{"balance_druid"}, Rotation: "kb_balance_druid"}), g
}

func TestKBBalanceDruidRotationPlays(t *testing.T) {
	r, g := kbBalanceRig(t)
	r.tick(100 * time.Millisecond)
	// The KB's upkeep first: Moonfire on a mob that has none, then Insect Swarm.
	if d := open(r); d == nil || d.Action != "moonfire" || spells.Name(d.Spell) != "Moonfire" || d.Target != mobA {
		t.Fatalf("%+v idle %q", d, r.idle())
	}
	mobAura(g.fakeGame, "Moonfire", drMoonfire)
	if d := r.tick(1600 * time.Millisecond); d == nil || d.Action != "insect_swarm" || spells.Name(d.Spell) != "Insect Swarm" {
		t.Fatalf("%+v idle %q", d, r.idle())
	}
	mobAura(g.fakeGame, "Insect Swarm", drSwarm)
	// No Eclipse yet: the KB's "fish" Starfire is the filler.
	if d := r.tick(2500 * time.Millisecond); d == nil || d.Action != "starfire_fish" || spells.Name(d.Spell) != "Starfire" {
		t.Fatalf("%+v idle %q", d, r.idle())
	}
	// Arcane Eclipse: the KB's Starfire entry takes over; Nature Eclipse gives Wrath.
	selfAura(g.fakeGame, "Arcane Eclipse", 51443)
	if d := r.tick(2500 * time.Millisecond); d == nil || d.Action != "starfire" {
		t.Fatalf("Arcane Eclipse: %+v idle %q", d, r.idle())
	}
	g.setSelf(func(u *objects.Unit) { u.Auras = []objects.Aura{{Name: "Nature Eclipse", SpellID: 51442}} })
	if d := r.tick(2500 * time.Millisecond); d == nil || d.Action != "wrath" || spells.Name(d.Spell) != "Wrath" {
		t.Fatalf("Nature Eclipse: %+v idle %q", d, r.idle())
	}
}

func kbCatRig(t *testing.T) (*rig, *meleeGame) {
	t.Helper()
	g := newFake(drCatKnown...)
	druidSelf(g)
	g.self.Level = 60
	shiftTo(g, formCat, drCat)
	mg := newMeleeGame(g)
	r := rigOn(t, mg, g, Params{Strategies: []string{"feral_cat_druid"}, Rotation: "kb_feral_cat_druid"})
	tankFights(g, 7, 4, 0)
	mg.setSwing(mobA)
	return r, mg
}

func TestKBFeralCatDruidRotationPlays(t *testing.T) {
	r, _ := kbCatRig(t)
	r.tick(100 * time.Millisecond)
	// The KB's opener without stealth: Faerie Fire (Feral) on a mob that has none, then Tiger's Fury.
	if d := open(r); d == nil || d.Action != "faerie_fire_feral" || spells.Name(d.Spell) != "Faerie Fire (Feral)" {
		t.Fatalf("%+v idle %q", d, r.idle())
	}
	mobAura(r.g, "Faerie Fire (Feral)", drFFF)
	if d := r.tick(1600 * time.Millisecond); d == nil || d.Action != "tigers_fury" || spells.Name(d.Spell) != "Tiger's Fury" {
		t.Fatalf("%+v idle %q", d, r.idle())
	}
	selfAura(r.g, "Tiger's Fury", drFury)
	// With the buff up: Rake while the mob carries none, then Shred only on Clearcasting, Claw as the filler.
	if d := r.tick(1600 * time.Millisecond); d == nil || d.Action != "rake" || spells.Name(d.Spell) != "Rake" {
		t.Fatalf("%+v idle %q", d, r.idle())
	}
	mobAura(r.g, "Rake", drRake)
	if d := r.tick(1600 * time.Millisecond); d == nil || d.Action != "claw" || spells.Name(d.Spell) != "Claw" {
		t.Fatalf("%+v idle %q", d, r.idle())
	}
	selfAura(r.g, "Clearcasting", 16870)
	if d := r.tick(1600 * time.Millisecond); d == nil || d.Action != "shred" || spells.Name(d.Spell) != "Shred" {
		t.Fatalf("Clearcasting: %+v idle %q", d, r.idle())
	}
	// Rip at five combo points, Ferocious Bite once Rip is up.
	setCP(r.g, 5, mobA)
	if d := r.tick(1600 * time.Millisecond); d == nil || d.Action != "rip" || spells.Name(d.Spell) != "Rip" {
		t.Fatalf("five points: %+v idle %q", d, r.idle())
	}
	mobAura(r.g, "Rip", drRip)
	if d := r.tick(1600 * time.Millisecond); d == nil || d.Action != "ferocious_bite" || spells.Name(d.Spell) != "Ferocious Bite" {
		t.Fatalf("%+v idle %q", d, r.idle())
	}
}

func kbBearRig(t *testing.T) (*rig, *meleeGame) {
	t.Helper()
	g := newFake(append([]uint32{45736}, drBearKnown...)...) // Savage Bite (level 58) is one of the KB's core casts
	druidSelf(g)
	g.self.Level = 60
	g.self.Health, g.self.MaxHealth = 2000, 2000
	shiftTo(g, formDireBear, drDire)
	mg := newMeleeGame(g)
	r := rigOn(t, mg, g, Params{Strategies: []string{"bear_tank_druid"}, Rotation: "kb_feral_bear_druid"})
	m := mob(mobA, selfG)
	m.O = math.Pi
	g.addUnit(m, 3, 0)
	g.setSelf(func(u *objects.Unit) { u.InCombat = true; u.Power = 300 })
	mg.setSwing(mobA)
	return r, mg
}

func TestKBFeralBearDruidRotationPlays(t *testing.T) {
	r, _ := kbBearRig(t)
	// Maul is queued on every swing (the KB's "always queued"); the swing itself is already on the mob.
	if d := r.tick(1600 * time.Millisecond); d == nil || d.Action != "maul" || spells.Name(d.Spell) != "Maul" {
		t.Fatalf("%+v idle %q", d, r.idle())
	}
	// The rage dump at 50+ rage: Savage Bite, ahead of the queued Maul.
	setRageB(r.g, 600)
	if d := r.tick(1600 * time.Millisecond); d == nil || d.Action != "savage_bite" || spells.Name(d.Spell) != "Savage Bite" {
		t.Fatalf("%+v idle %q", d, r.idle())
	}
	// Two mobs: Swipe (the AoE row's threshold), ahead of Maul; one mob and 30 rage: Maul again.
	setRageB(r.g, 300)
	r.g.addUnit(mob(mobB, selfG), 4, 1)
	if d := r.tick(1600 * time.Millisecond); d == nil || d.Action != "swipe" || spells.Name(d.Spell) != "Swipe" {
		t.Fatalf("%+v idle %q", d, r.idle())
	}
}

func kbRestorationRig(t *testing.T) (*rig, *casterGame) {
	t.Helper()
	g := newCasterGame(drRestoKnown...)
	druidSelf(g.fakeGame)
	g.self.Level = 60
	g.self.InCombat = true
	return rigOn(t, g, g.fakeGame, Params{Strategies: []string{"resto_druid"}, Rotation: "kb_restoration_druid"}), g
}

func TestKBRestorationDruidRotationPlays(t *testing.T) {
	r, g := kbRestorationRig(t)
	// The KB's Rejuvenation upkeep on a hurt tank (55%: the triage picks him, role_healer still calls it a light
	// heal); then Regrowth on the tank once the HoT is up.
	g.member(tankG, "Tankard", ClassWarrior, 550, 1000, 6, 0)
	if d := r.tick(100 * time.Millisecond); d == nil || d.Action != "rejuvenation" || d.Target != tankG || spells.Name(d.Spell) != "Rejuvenation" {
		t.Fatalf("%+v idle %q", d, r.idle())
	}
	g.addAura(tankG, objects.Aura{Name: "Rejuvenation", SpellID: drRejuv7})
	if d := r.tick(2 * time.Second); d == nil || d.Action != "regrowth" || spells.Name(d.Spell) != "Regrowth" {
		t.Fatalf("%+v idle %q", d, r.idle())
	}
	// The KB's emergency: the max-rank Healing Touch on the tank below 30%.
	g.setHP(tankG, 200)
	if d := r.tick(2 * time.Second); d == nil || d.Action != "nature_healing_touch" || spells.Name(d.Spell) != "Healing Touch" {
		t.Fatalf("emergency: %+v idle %q", d, r.idle())
	}
}
