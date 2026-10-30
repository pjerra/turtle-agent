package brain

import (
	"slices"
	"testing"
	"time"

	"github.com/pjerra/turtle-agent/internal/objects"
	"github.com/pjerra/turtle-agent/internal/spells"
)

// The knowledge base priest rotations (tools/kbrotgen): shadow and discipline are replacements of priest_shadow,
// holy is an add-mode file after role_healer. Each plays its first runnable entry and a core spell of its oracle inside
// three ticks, and the two new presets stay clones of the sibling preset they were made from.

const (
	spirHolyFire   = 14914 // Holy Fire rank 1, level 20
	spirFlashHeal7 = 10917 // Flash Heal rank 7, level 56
	spirEnlighten  = 51476 // Enlighten, level 60
)

// kbPriest is a level 60 priest with a full mana pool on the preset and rotation named.
func kbPriest(t *testing.T, preset, rotation string, known ...uint32) (*rig, *fakeGame) {
	t.Helper()
	g := priestFake(known...)
	g.self.Level = 60
	g.self.PowerType, g.self.Power, g.self.MaxPower = 0, 3000, 3000
	return newRig(t, g, Params{Strategies: []string{preset}, Rotation: rotation}), g
}

// TestKBPriestPresetsAreTheSiblingClones holds discipline_priest to the clone it was written as: shadow_priest with the
// Go rotation swapped for the KB file. A change to shadow_priest must reach both lists.
func TestKBPriestPresetsAreTheSiblingClones(t *testing.T) {
	want := slices.Clone(presets["shadow_priest"])
	i := slices.Index(want, "priest_shadow")
	want[i] = "rot:kb_discipline_priest"
	if got := presets["discipline_priest"]; !slices.Equal(got, want) {
		t.Fatalf("discipline_priest\n got %v\nwant %v", got, want)
	}
}

// The shadow KB file: Shadowform first, then the single-target list, and Vampiric Embrace waits for hurt members.
func TestKBShadowPriestPlays(t *testing.T) {
	r, g := kbPriest(t, "shadow_priest", "kb_shadow_priest", prShadowform, prSWP, prMindBlast, prMindFlay, prVampiric,
		prInnerFire, prFort1, spShoot)
	tankFights(g, 25, 18, 0)
	if d := r.tick(100 * time.Millisecond); d == nil || d.Action != "shadowform" || d.Spell != prShadowform {
		t.Fatalf("Shadowform first: %+v idle %q", d, r.idle())
	}
	g.setSelf(func(u *objects.Unit) {
		u.Auras = append(u.Auras, objects.Aura{Name: "Shadowform", SpellID: prShadowform})
	})
	got := actions(r, 2200*time.Millisecond, 1600*time.Millisecond, 1600*time.Millisecond)
	if !slices.Contains(got, "shadow_word_pain") {
		t.Fatalf("a core spell within three ticks: %v (idle %q)", got, r.idle())
	}
	if slices.Contains(got, "vampiric_embrace") {
		t.Fatalf("Vampiric Embrace without a hurt member: %v", got)
	}
}

// The discipline KB file: the Enlighten upkeep, then the Holy Fire cycle; Smite is the filler.
func TestKBDisciplinePriestPlays(t *testing.T) {
	r, g := kbPriest(t, "discipline_priest", "kb_discipline_priest", spirHolyFire, prSmite, prSWP, spirEnlighten,
		prInnerFire, prFort1, spShoot)
	tankFights(g, 25, 18, 0)
	if d := r.tick(100 * time.Millisecond); d == nil || d.Action != "enlighten" || d.Spell != spirEnlighten {
		t.Fatalf("Enlighten first: %+v idle %q", d, r.idle())
	}
	g.setSelf(func(u *objects.Unit) {
		u.Auras = append(u.Auras, objects.Aura{Name: "Enlighten", SpellID: spirEnlighten})
	})
	got := actions(r, 2200*time.Millisecond, 1600*time.Millisecond, 1600*time.Millisecond)
	if !slices.Contains(got, "holy_fire") {
		t.Fatalf("a core spell within three ticks: %v (idle %q)", got, r.idle())
	}
}

// The holy KB file is add mode: its Renew and Greater Heal entries pre-empt the generic heal at a medium hurt tank.
func TestKBHolyPriestPlays(t *testing.T) {
	r, g := kbPriest(t, "holy_priest", "kb_holy_priest", prRenew1, prFlash1, spirFlashHeal7, prGreater1, prShield1,
		prInnerFire, prFort1, spShoot)
	g.member(tankG, "Tankard", ClassWarrior, 500, 1000, 5, 0)
	d := r.tick(100 * time.Millisecond)
	if d == nil || d.Action != "greater_heal_1" || d.Spell != prGreater1 || d.Target != tankG {
		t.Fatalf("Greater Heal rank 1 on the tank: %+v idle %q", d, r.idle())
	}
	if spells.Name(d.Spell) != "Greater Heal" {
		t.Fatalf("spell %d %q", d.Spell, spells.Name(d.Spell))
	}
}
