package brain

import (
	"slices"
	"testing"
)

// The warlock knowledge-base play tests (task K2b). kbScene and kbPlays live in kb_mage_test.go.

// The warlock KB play tests' spellbooks: the rotation's spells and the preset's upkeep, at their highest rank of level 60.
var (
	kbAfflictionSpells = []uint32{686, 695, 705, 172, 6222, 980, 1014, 18265, 18879, 52550, 52551, 1120, 8288,
		1454, 1455, 6789, 17925, 711, 11683, 5740, 6219, 1490, 11721}
	kbDemonologySpells = []uint32{686, 695, 705, 172, 6222, 704, 7658, 5676, 17919, 17877, 18867, 711, 11683,
		2941, 11735, 51714, 18708}
	kbDestructionFireSpells = []uint32{6353, 17924, 348, 707, 17962, 18930, 17877, 18867, 5676, 17919, 172, 6222,
		1454, 711, 11683, 5740, 6219}
	kbDestructionShadowSpells = []uint32{686, 695, 705, 172, 6222, 17877, 18867, 18265, 18879, 1120, 8288, 711,
		11683, 5740, 6219}
)

// TestKBAfflictionWarlockPlays: the mob carries no Curse of Agony, so the KB's first curse entry (the damage curse) is the
// first action that can run; Corruption, the DoTs' backbone, follows.
func TestKBAfflictionWarlockPlays(t *testing.T) {
	r := kbScene(t, ClassWarlock, "affliction_warlock", "kb_affliction_warlock", kbAfflictionSpells, false, nil)
	kbPlays(t, r, "curse_of_agony", "Corruption", "Curse of Agony", "Drain Soul")
}

// TestKBDemonologyWarlockPlays: the demonology list's only curse is Curse of Recklessness ("if free choice": the mob carries
// no other curse), so that is the first action; the Shadow Bolt filler comes after it.
func TestKBDemonologyWarlockPlays(t *testing.T) {
	r := kbScene(t, ClassWarlock, "demonology_warlock", "kb_demonology_warlock", kbDemonologySpells, false, nil)
	kbPlays(t, r, "curse_of_recklessness", "Shadow Bolt", "Corruption", "Curse of Recklessness")
}

// TestKBDestructionFireWarlockPlays: Soul Fire is the KB's first cooldown row (band high) and the Improved Soul Fire buff is
// missing, so it opens; Immolate follows (the Conflagrate trigger).
func TestKBDestructionFireWarlockPlays(t *testing.T) {
	r := kbScene(t, ClassWarlock, "destruction_fire_warlock", "kb_destruction_fire_warlock", kbDestructionFireSpells, false, nil)
	got := kbPlays(t, r, "soul_fire_cd", "Soul Fire", "Immolate", "Conflagrate", "Searing Pain")
	if got[1] != "soul_fire_cd" && got[1] != "immolate" {
		t.Fatalf("after Soul Fire: %v", got)
	}
}

// TestKBDestructionShadowWarlockPlays: Corruption (the KB's first non-assigned spell) goes up first; Shadow Bolt is the filler.
func TestKBDestructionShadowWarlockPlays(t *testing.T) {
	r := kbScene(t, ClassWarlock, "destruction_shadow_warlock", "kb_destruction_shadow_warlock", kbDestructionShadowSpells, false, nil)
	kbPlays(t, r, "corruption", "Shadow Bolt", "Corruption")
}

// TestKBWarlockPresetsSwapOnlyTheAfflictionList pins what a KB warlock preset is made of: the affliction preset's own
// strategies, with warlock_affliction out and the KB rotation in; the upkeep and the pet stay.
func TestKBWarlockPresetsSwapOnlyTheAfflictionList(t *testing.T) {
	for _, tc := range []struct{ preset, rotation string }{
		{"demonology_warlock", "kb_demonology_warlock"},
		{"destruction_fire_warlock", "kb_destruction_fire_warlock"},
		{"destruction_shadow_warlock", "kb_destruction_shadow_warlock"},
	} {
		rot, err := RotationNamed(tc.rotation)
		if err != nil {
			t.Fatal(err)
		}
		if rot.KB == nil || rot.KB.Preset != tc.preset {
			t.Errorf("%s: the rotation names preset %v, want %s", tc.rotation, rot.KB, tc.preset)
		}
		strats, _, err := ResolveFor([]string{tc.preset}, ResolveOpts{Rotation: tc.rotation})
		if err != nil {
			t.Fatalf("%s: %v", tc.preset, err)
		}
		var names []string
		for _, s := range strats {
			names = append(names, s.Name)
		}
		if !slices.Contains(names, "rot:"+tc.rotation) || slices.Contains(names, "warlock_affliction") {
			t.Errorf("%s plays %v, want rot:%s and no warlock_affliction", tc.preset, names, tc.rotation)
		}
		for _, keep := range []string{"warlock_upkeep", "pet"} {
			if !slices.Contains(names, keep) {
				t.Errorf("%s dropped %s: %v", tc.preset, keep, names)
			}
		}
	}
}
