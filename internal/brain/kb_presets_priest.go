package brain

// Preset for the disciple priest, the one KB priest spec without one: the shadow priest's list with the knowledge base's
// discipline rotation in place of the Go shadow rotation (tools/kbrotgen/recipes/kb_discipline_priest.json).
//
// The list is written out rather than cloned from presets["shadow_priest"] in an init: this file's init runs before
// priest.go's (files initialize in name order), so the sibling preset does not exist yet. TestKBPresetIsTheSiblingClone
// holds the two equal, so an edit of shadow_priest that forgets this list fails a test instead of drifting.
func init() {
	presets["discipline_priest"] = []string{"combat_dps", "rot:kb_discipline_priest", "priest_defense", "priest_buffs", "ranged_position",
		"avoid_ground", "class_interrupt", "combat_consumables", "wand", "rest_eat", "rest_drink"}
}
