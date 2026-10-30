package brain

import "slices"

// Presets that exist only as rotation files: the affliction warlock's list with the knowledge-base rotation swapped in.
//
// This file's name is not the kb_presets_<class>.go of the task spec on purpose: package init functions run in file-name order,
// and presets["affliction_warlock"] is filled by warlock.go's init, which a file named kb_presets_warlock.go would run before
// (k sorts before w). Like rotation_presets.go it clones a sibling preset, so a change to affliction_warlock reaches these too.
func init() {
	for _, spec := range []string{"demonology", "destruction_fire", "destruction_shadow"} {
		p := slices.Clone(presets["affliction_warlock"])
		p[slices.Index(p, "warlock_affliction")] = "rot:kb_" + spec + "_warlock"
		presets[spec+"_warlock"] = p
	}
}
