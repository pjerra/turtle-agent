package brain

import "slices"

// Presets that exist only as rotation files: a mage's list with the Frost rotation swapped for the file.
func init() {
	for _, spec := range []string{"fire", "arcane"} {
		p := slices.Clone(presets["frost_mage"])
		i := slices.Index(p, "mage_frost")
		p[i] = "rot:" + spec + "_mage"
		presets[spec+"_mage"] = p
	}
}
