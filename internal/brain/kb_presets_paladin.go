package brain

// Preset for the protection paladin, the one KB paladin spec without one: the retribution paladin's list with the tank
// role and the knowledge base's protection rotation in place of the Go retribution rotation
// (tools/kbrotgen/recipes/kb_protection_paladin.json).
//
// The list is written out rather than cloned from presets["ret_paladin"] in an init: this file's init runs before
// paladin.go's (files initialize in name order), so the sibling preset does not exist yet. TestKBPresetIsTheSiblingClone
// holds the two equal, so an edit of ret_paladin that forgets this list fails a test instead of drifting.
func init() {
	presets["protection_paladin"] = []string{"role_tank", "rot:kb_protection_paladin", "melee_position", "avoid_ground",
		"class_interrupt", "combat_consumables", "rest_drink", "rest_eat"}
}
