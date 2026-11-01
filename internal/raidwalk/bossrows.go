package raidwalk

import (
	"fmt"
	"math"
	"strings"

	"github.com/pjerra/turtle-agent/internal/dungeon"
	"github.com/pjerra/turtle-agent/internal/mapview"
)

// FirstAddBossRadius is how near (yards, 2D) the plan position of a boss that lists it as a `first` add a frame row
// of an entry that is ONLY a first add must stand to be boss evidence. Farther out it is trash of the same entry.
//   - ZG walk 19 test2 s4: Zulian Tigers 11361 (Thekal's first adds) fought as trash at step 89 on Venoxis's leg,
//     ~330 yd from Thekal's position, latched the walk's boss gate and blocked a trash-wipe recovery.
//   - Thekal phase 2: Thekal 14599 dies in phase 1 and comes back as 14509, which no plan lists; TB marks the step
//     done (bossDeadNearSpot, TortoiseBots runtime/DungeonRunPolicy.h:1404) and the status moves on to arlokk while
//     the tigers still fight at Thekal's spot. A leg rule would fail open there; the distance rule does not.
//
// The nearest 11361 trash pull group in zul_gurub.json stands 56.5 yd from Thekal's position (on his own leg).
const FirstAddBossRadius = 40

// firstOf is one boss that lists an entry as a first add.
type firstOf struct {
	leg    string
	boss   uint32
	pos    dungeon.Point
	hasPos bool
}

// BossRows decides which hostile frame rows are boss evidence. The walk's gates, the tracker's one-shot boss latch
// and the injector's "never pick a boss" filter all use it, so one rule decides everywhere.
//
// A plan boss entry or hold entry always counts. An entry that is only a `first` add counts within
// FirstAddBossRadius of the position of any boss that lists it (on any leg), and always when such a boss has no
// position in the plan (fail closed).
type BossRows struct {
	set   map[uint32]bool
	core  map[uint32]bool
	first map[uint32][]firstOf
}

// NewBossRows builds the rule from the plan (nil: no entry is a boss row).
func NewBossRows(plan *dungeon.Plan) *BossRows {
	b := &BossRows{set: map[uint32]bool{}, core: map[uint32]bool{}, first: map[uint32][]firstOf{}}
	if plan == nil {
		return b
	}
	for _, e := range mapview.BossEntries(plan) {
		b.set[e] = true
	}
	for _, leg := range plan.Legs {
		if leg.Boss == nil {
			continue
		}
		b.core[leg.Boss.Entry] = true
		p := leg.Boss.Position
		for _, e := range leg.Boss.First {
			b.first[e] = append(b.first[e], firstOf{leg: leg.Name, boss: leg.Boss.Entry, pos: p, hasPos: p != (dungeon.Point{})})
		}
		if leg.Boss.Hold != nil {
			for _, e := range leg.Boss.Hold.Entries {
				b.set[e] = true
				b.core[e] = true
			}
		}
	}
	return b
}

// Listed says whether the plan names the entry as a boss, a first add or a hold entry (position not considered).
func (b *BossRows) Listed(entry uint32) bool { return b != nil && b.set[entry] }

// Counts says whether a row of this entry standing at x, y is boss evidence.
func (b *BossRows) Counts(entry uint32, x, y float32) bool {
	if !b.Listed(entry) {
		return false
	}
	if b.core[entry] {
		return true
	}
	bosses := b.first[entry]
	if len(bosses) == 0 {
		return true
	}
	for _, f := range bosses {
		if !f.hasPos {
			return true
		}
		if math.Hypot(float64(x-f.pos.X), float64(y-f.pos.Y)) <= FirstAddBossRadius {
			return true
		}
	}
	return false
}

// Words names a boss row for a note: "boss N" for a boss or hold entry, "boss row N (first add of <leg>)" for an
// entry that is only a first add.
func (b *BossRows) Words(entry uint32) string {
	if b == nil || b.core[entry] || len(b.first[entry]) == 0 {
		return fmt.Sprintf("boss %d", entry)
	}
	var legs []string
	for _, f := range b.first[entry] {
		name := f.leg
		if name == "" {
			name = fmt.Sprintf("boss %d", f.boss)
		}
		legs = append(legs, name)
	}
	return fmt.Sprintf("boss row %d (first add of %s)", entry, strings.Join(legs, ", "))
}
