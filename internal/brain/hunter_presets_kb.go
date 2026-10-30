package brain

import "slices"

// Presets that exist only as rotation files: the marksman hunter's list with the knowledge-base rotation swapped in. The
// survival list is a melee list, so its preset also stands in melee (hunter_position out, melee_position in).
//
// See warlock_presets_kb.go on the file name: presets["marksman_hunter"] is filled by hunter.go's init, and this init has to run
// after it.
func init() {
	bm := slices.Clone(presets["marksman_hunter"])
	bm[slices.Index(bm, "hunter_marksman")] = "rot:kb_beast_mastery_hunter"
	presets["beast_mastery_hunter"] = bm

	sv := slices.Clone(presets["marksman_hunter"])
	sv[slices.Index(sv, "hunter_marksman")] = "rot:kb_survival_hunter"
	sv[slices.Index(sv, "hunter_position")] = "melee_position"
	presets["survival_hunter"] = sv
}
