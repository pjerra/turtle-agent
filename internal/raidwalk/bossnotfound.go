package raidwalk

import (
	"regexp"

	"github.com/pjerra/turtle-agent/internal/dungeon"
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

// SummonOpenStep is the run leader's step number (1-based) of the leg's open row that summons the boss of the boss row at bossStep
// (1-based), when the plan annotates one (dungeon.Boss.SummonOpenStep, the 1-based place of that use_object step in the leg's steps):
// the object whose use brings the boss back. Zul'Gurub's Arlokk is the case it exists for: after a wipe she despawns with no spawn row
// left, the run leader pauses "boss 14515 not found" on her own step, and only the gong 180526 (the leg's use_object step) starts her
// script again (walk 35). The step a walk restarts at is the open (KindOpen) or open_proof (KindOpenProof: the same object, with a
// proof creature the run watches) row the export wrote for the annotated step, in the same leg and before the boss row.
//
// The link is the plan's own annotation: an open row that merely stands before or near a boss row is no evidence that it summons the
// boss, so a boss with no SummonOpenStep gets no re-run (false), whatever stands around it. A plan whose rows do not carry the annotated
// step's open row (rows deployed from another export of the plan) is no re-run either: false.
func SummonOpenStep(rows planexport.Rows, p *dungeon.Plan, bossStep int) (int, bool) {
	if p == nil || bossStep < 1 || bossStep > len(rows.Steps) {
		return 0, false
	}
	boss := rows.Steps[bossStep-1]
	if boss.Kind != planexport.KindBoss {
		return 0, false
	}
	for li := range p.Legs {
		l := &p.Legs[li]
		if l.Boss == nil || l.Boss.Entry != boss.Entry || l.Boss.SummonOpenStep == 0 {
			continue
		}
		rowLeg, ok := rows.LegNumber(p, li)
		if !ok || rowLeg != boss.Leg {
			continue
		}
		step := l.Boss.SummonOpenStep
		if step < 1 || step > len(l.Steps) || l.Steps[step-1].UseObject == nil || l.Steps[step-1].UseObject.ViaItem {
			continue // the plan loader refuses these; a plan file newer than this build gets no re-run
		}
		entry := l.Steps[step-1].UseObject.Entry
		// The row that step wrote: an open row of the same leg with the object's entry, before the boss row (the step's own stand move
		// and the leg's cut points may stand between them). The last such row is the one that summons it.
		for i := bossStep - 2; i >= 0; i-- {
			s := rows.Steps[i]
			if s.Leg != boss.Leg {
				continue
			}
			if (s.Kind == planexport.KindOpen || s.Kind == planexport.KindOpenProof) && s.Entry == entry {
				return i + 1, true
			}
		}
		return 0, false
	}
	return 0, false
}
