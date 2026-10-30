package brain

// The shaman's knowledge-base enhancement tank (tools/kbrotgen/recipes/kb_enhancement_tank_shaman.json): the
// enhancement preset with the tank role in place of the damage dealer's and the KB rotation in place of the class
// strategy. Its file names shaman_enhancement in "replaces", so the preset carries the list and `rotation:
// kb_enhancement_tank_shaman` takes the same place: the two arms of a pull test are the same list.
//
// The list is written out instead of cloning presets["enhancement_shaman"] as rotation_presets.go clones the mage's:
// a file's init() runs in file-name order, and kb_presets_shaman.go sorts before shaman.go, whose init() registers the
// sibling (kb_presets_shaman.go's would find no enhancement_shaman to clone). TestTankShamanPresetFollowsTheEnhancementOne
// holds the two lists together.
func init() {
	presets["tank_shaman"] = []string{"role_tank", "rot:kb_enhancement_tank_shaman", "shaman_shield", "shaman_imbue",
		"shaman_totems_enhancement", "melee_position", "avoid_ground", "class_interrupt", "combat_consumables",
		"rest_eat", "rest_drink"}
}
