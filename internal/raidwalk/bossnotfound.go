package raidwalk

import (
	"regexp"

	"github.com/pjerra/turtle-agent/internal/planexport"
)

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

// OpenStepBeforeBoss is the plan's open row that summons the boss of the boss row at bossStep (1-based), when there is one: the
// last non-move row of that leg before the boss row, and an open (KindOpen, or KindOpenProof: the same object, with a proof
// creature the run watches). Zul'Gurub's Arlokk is the case it exists for: the leg's only non-move row is `use_object 180526`
// (the gong) and the boss row 14515 follows it, and after a wipe she despawns with no spawn row left, so the run leader pauses
// "boss 14515 not found" until an operator repeats the gong (walk 35). A leg whose last non-move row before the boss is
// anything else (a pull, a hold, a loot, a gossip, a trigger) has no such step: false.
func OpenStepBeforeBoss(rows planexport.Rows, bossStep int) (int, bool) {
	if bossStep < 1 || bossStep > len(rows.Steps) {
		return 0, false
	}
	boss := rows.Steps[bossStep-1]
	if boss.Kind != planexport.KindBoss {
		return 0, false
	}
	for i := bossStep - 2; i >= 0; i-- {
		s := rows.Steps[i]
		if s.Leg != boss.Leg {
			return 0, false
		}
		switch s.Kind {
		case planexport.KindMove, planexport.KindMoveDirect, planexport.KindMoveHopExact, planexport.KindRest:
			continue
		case planexport.KindOpen, planexport.KindOpenProof:
			return i + 1, true
		default:
			return 0, false
		}
	}
	return 0, false
}
