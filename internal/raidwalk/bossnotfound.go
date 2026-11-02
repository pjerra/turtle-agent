package raidwalk

import "regexp"

// bossNotFoundRe matches the run leader's note when it cannot find a plan boss: TB's
// DungeonRunPolicy boss step needs a dead creature of the step's entry near the spot, and a
// boss the encounter turns into another entry (Zul'Gurub's Thekal 14599 -> 14509,
// boss_thekal.cpp:358) leaves no such corpse, so TB pauses with "boss <entry> not found: ..."
// instead of marking the step done. raid-walk reads this to skip the step when the frames show
// the boss dead (walk 24 test2 r2).
var bossNotFoundRe = regexp.MustCompile(`^boss ([0-9]+) not found:`)

// BossNotFound reads the entry of a "boss <N> not found:" note (CleanLine applied); false for any
// other note.
func BossNotFound(note string) (uint32, bool) {
	m := bossNotFoundRe.FindStringSubmatch(CleanLine(note))
	if m == nil {
		return 0, false
	}
	var n uint32
	for _, c := range m[1] {
		n = n*10 + uint32(c-'0')
	}
	return n, true
}
