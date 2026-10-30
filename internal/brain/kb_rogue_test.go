package brain

import (
	"slices"
	"testing"
	"time"
)

// The KB rogue rotations play: one fake rogue of level 60 per spec, full energy, one mob in reach, pinned on the file's first
// entry that can run, on a core spell right after it, and on the preset each spec's recipe names.

// Spells of the KB rogue tests not already named in rogue_test.go (ids from internal/spells/meta.tsv.gz).
const (
	spKBEnvenom    uint32 = 52531 // Envenom (Turtle talent)
	spKBSurprise   uint32 = 52511 // Surprise Attack (Turtle talent)
	spKBFlurry     uint32 = 13877 // Blade Flurry
	spKBNoxious    uint32 = 52714 // Noxious Assault rank 1 (Turtle talent)
	spKBHemorrhage uint32 = 16511 // Hemorrhage (talent)
	spKBExpose     uint32 = 8647  // Expose Armor rank 1
	spKBRupture    uint32 = 1943  // Rupture rank 1
	spKBColdBlood  uint32 = 14177 // Cold Blood (talent)
)

// kbFullRogue is a level 60 rogue of the preset with the given spells known, on full energy, opening the standard fight.
func kbFullRogue(t *testing.T, preset, rotation string, known ...uint32) *rig {
	t.Helper()
	r, mf := newClassRig(t, preset, ClassRogue, 60, 3, 100, Params{Rotation: rotation}, known, nil)
	meleeFight(r, mf)
	return r
}

func TestKBCombatRoguePlays(t *testing.T) {
	r := kbFullRogue(t, "combat_rogue", "kb_combat_rogue", spSinister, spEvisc, spSnD, spKick, spEvasion, spFeint, spVanish, spSprint,
		spKBEnvenom, spKBSurprise, spKBFlurry)
	actions(r, 100*time.Millisecond, 2200*time.Millisecond)
	casts := r.g.castList()
	if len(casts) == 0 || casts[0].spell != spSinister {
		t.Fatalf("casts %v, want Sinister Strike first: Envenom, Slice and Dice and the finishers need combo points (idle %q)", casts, r.idle())
	}
	setCombo(r.g, 1, mobA)
	actions(r, 1100*time.Millisecond)
	casts = r.g.castList()
	if len(casts) < 2 || casts[1].spell != spKBEnvenom {
		t.Fatalf("casts %v, want Envenom at one combo point (the file's first entry)", casts)
	}
}

func TestKBAssassinationRoguePlays(t *testing.T) {
	r := kbFullRogue(t, "assassination_rogue", "kb_assassination_rogue", spKBNoxious, spKBEnvenom, spEvisc, spSnD, spKBHemorrhage,
		spSinister, spKBColdBlood)
	actions(r, 100*time.Millisecond, 2200*time.Millisecond)
	casts := r.g.castList()
	if len(casts) == 0 || casts[0].spell != spKBNoxious {
		t.Fatalf("casts %v, want Noxious Assault first (the only builder, a core spell; idle %q)", casts, r.idle())
	}
	setCombo(r.g, 1, mobA)
	actions(r, 1100*time.Millisecond)
	casts = r.g.castList()
	if len(casts) < 2 || casts[1].spell != spKBEnvenom {
		t.Fatalf("casts %v, want Envenom at one combo point (the file's first entry)", casts)
	}
}

func TestKBSubtletyRoguePlays(t *testing.T) {
	r := kbFullRogue(t, "subtlety_rogue", "kb_subtlety_rogue", spKBHemorrhage, spKBExpose, spKBRupture, spSnD, spEvisc, spSinister)
	actions(r, 100*time.Millisecond, 2200*time.Millisecond)
	casts := r.g.castList()
	if len(casts) == 0 || casts[0].spell != spKBHemorrhage {
		t.Fatalf("casts %v, want Hemorrhage first (the only builder, a core spell; idle %q)", casts, r.idle())
	}
	setCombo(r.g, 5, mobA)
	actions(r, 1100*time.Millisecond)
	casts = r.g.castList()
	if len(casts) < 2 || casts[1].spell != spKBExpose {
		t.Fatalf("casts %v, want Expose Armor at five combo points (the file's first finisher)", casts)
	}
}

// TestKBPresetsTrackCombatRogue holds the two KB presets to the combat rogue's list they differ from in one strategy only:
// kb_presets_rogue.go writes the list out (it sorts before rogue.go, whose init registers combat_rogue), so a change to the
// combat preset must be made there too.
func TestKBPresetsTrackCombatRogue(t *testing.T) {
	for _, spec := range []string{"assassination", "subtlety"} {
		want := slices.Clone(presets["combat_rogue"])
		i := slices.Index(want, "rogue_combat")
		if i < 0 {
			t.Fatalf("combat_rogue has no rogue_combat: %v", want)
		}
		want[i] = "rot:kb_" + spec + "_rogue"
		if got := presets[spec+"_rogue"]; !slices.Equal(got, want) {
			t.Errorf("%s_rogue:\n got %v\nwant %v", spec, got, want)
		}
	}
}
