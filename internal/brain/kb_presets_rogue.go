package brain

// Presets that exist only as rotation files: the combat rogue's list with rogue_combat swapped for the KB's assassination or
// subtlety rotation (tools/kbrotgen). Which one a run plays is the rotation name (brain_start's rotation, a dungeon's), as for the
// mage's fire and arcane files.
//
// The list is written out instead of cloning presets["combat_rogue"]: this file sorts before rogue.go, whose init registers that
// preset, so a clone here would read a nil slice (rotation_presets.go can clone because frost_mage is in the presets literal).
// TestKBPresetsTrackCombatRogue holds the two lists together.
func init() {
	for _, spec := range []string{"assassination", "subtlety"} {
		presets[spec+"_rogue"] = []string{"combat_dps", "rot:kb_" + spec + "_rogue", "melee_position", "avoid_ground", "class_interrupt",
			"combat_consumables", "rest_eat"}
	}
}
