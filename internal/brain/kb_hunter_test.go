package brain

import (
	"slices"
	"testing"
)

// The hunter knowledge-base play tests (task K2b). kbScene and kbPlays live in kb_mage_test.go.

// The hunter KB play tests' spellbooks: the rotation's spells and the preset's, at their highest rank of level 60.
var (
	kbMarksmanshipSpells = []uint32{75, 19434, 20900, 3035, 3044, 14281, 2643, 14288, 1978, 13549, 1510, 14294,
		3045, 5384, 14318, 14322, 45651}
	kbBeastMasterySpells = []uint32{75, 19574, 3035, 3044, 14281, 2643, 14288, 1510, 14294, 3045, 5384, 14318,
		45651}
	kbSurvivalSpells = []uint32{2973, 14266, 13795, 14302, 1495, 14269, 48049, 24118, 51575, 52416, 2974,
		13813, 14316, 3045, 19263, 5384, 14318, 45651}
)

// TestKB MarksmanshipHunterPlays: Rapid Fire is the KB's first cooldown row (band high) and goes up at the pull; Aimed Shot, the
// first entry of the list that can run, follows (no Lock and Load proc in the scene).
func TestKBMarksmanshipHunterPlays(t *testing.T) {
	r := kbScene(t, ClassHunter, "marksman_hunter", "kb_marksmanship_hunter", kbMarksmanshipSpells, false, nil)
	kbPlays(t, r, "rapid_fire", "Aimed Shot", "Steady Shot", "Arcane Shot")
}

// TestKBBeastMasteryHunterPlays: Bestial Wrath is the KB's first cooldown row (band high) and goes up first; Arcane Shot, the
// first entry of the loop that can run, follows. Kill Command is the KB's core but is pet state, so it is not in the rotation.
func TestKBBeastMasteryHunterPlays(t *testing.T) {
	r := kbScene(t, ClassHunter, "beast_mastery_hunter", "kb_beast_mastery_hunter", kbBeastMasterySpells, false, nil)
	got := kbPlays(t, r, "bestial_wrath", "Kill Command", "Steady Shot", "Arcane Shot")
	if got[1] != "arcane_shot" {
		t.Fatalf("after Bestial Wrath: %v", got)
	}
}

// TestKBSurvivalHunterPlays: the melee list opens on the swing (auto attack) and its first strike, Raptor Strike; Rapid Fire,
// the KB's cooldown row, goes up first because it is band high.
func TestKBSurvivalHunterPlays(t *testing.T) {
	r := kbScene(t, ClassHunter, "survival_hunter", "kb_survival_hunter", kbSurvivalSpells, true, nil)
	got := kbPlays(t, r, "rapid_fire", "Raptor Strike", "Mongoose Bite", "Lacerate")
	if got[1] != "auto_attack" || got[2] != "raptor_strike" {
		t.Fatalf("the swing and Raptor Strike: %v", got)
	}
}

// TestKBHunterPresets pins what the two new hunter presets are made of: Marksman's strategies with the KB rotation in place of
// hunter_marksman, and for survival the melee position in place of the ranged one.
func TestKBHunterPresets(t *testing.T) {
	names := func(preset, rotation string) []string {
		t.Helper()
		rot, err := RotationNamed(rotation)
		if err != nil {
			t.Fatal(err)
		}
		if rot.KB == nil || rot.KB.Preset != preset {
			t.Errorf("%s: the rotation names preset %v, want %s", rotation, rot.KB, preset)
		}
		strats, _, err := ResolveFor([]string{preset}, ResolveOpts{Rotation: rotation})
		if err != nil {
			t.Fatalf("%s: %v", preset, err)
		}
		var out []string
		for _, s := range strats {
			out = append(out, s.Name)
		}
		if !slices.Contains(out, "rot:"+rotation) || slices.Contains(out, "hunter_marksman") {
			t.Errorf("%s plays %v, want rot:%s and no hunter_marksman", preset, out, rotation)
		}
		return out
	}
	if got := names("beast_mastery_hunter", "kb_beast_mastery_hunter"); !slices.Contains(got, "hunter_position") {
		t.Errorf("beast_mastery_hunter is not at range: %v", got)
	}
	got := names("survival_hunter", "kb_survival_hunter")
	if !slices.Contains(got, "melee_position") || slices.Contains(got, "hunter_position") {
		t.Errorf("survival_hunter does not stand in melee: %v", got)
	}
}
