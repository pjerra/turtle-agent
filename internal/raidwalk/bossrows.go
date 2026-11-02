package raidwalk

import (
	"fmt"
	"math"
	"slices"
	"strings"

	"github.com/pjerra/turtle-agent/internal/dungeon"
	"github.com/pjerra/turtle-agent/internal/mapview"
)

// FirstAddBossRadius is how near (yards, 2D) the plan position of a boss that lists it as a `first` add a frame row
// of an AMBIGUOUS first entry must stand to be boss evidence; farther out it is trash of the same entry. An
// ambiguous first entry is one that is only a first add (no boss, phase or hold entry) and that the same plan also
// pulls as trash in some leg's pull_groups. Every other first entry is boss evidence wherever it stands: many are
// real encounter units that fight far from the boss's plan position (Naxx Four Horsemen 16065/16062/16063;
// Thaddius's Stalagg 15929 / Feugen 15930, summoned 69 yd off, boss_thaddius.cpp:125-129; AQ40 Yauj 15543 53 yd,
// Vek'nilash 15275 > 60 yd; ZF Bly's crew 7605/7606/7608 63-72 yd; Nekrum 7796 ~56 yd; Anub'Rekhan's crypt guard
// 16573 43 yd), and with a radius the walk would revive into a live encounter (Codex adversarial and the Opus cold
// review of f311f7e).
//   - ZG walk 19 test2 s4: Zulian Tigers 11361 (Thekal's first adds, also ZG trash) fought as trash at step 89 on
//     Venoxis's leg, ~330 yd from Thekal's position, latched the walk's boss gate and blocked a trash-wipe recovery.
//   - Thekal phase 2: Thekal 14599 dies in phase 1 and comes back as 14509 with Zulian Guardians 15068 (plan
//     boss.phase_entries); on THIS server no dead creature of the boss's own entry (14599) is left, so TB does not
//     mark the step done: it pauses with "boss 14599 not found: ..." (walk 24 test2 r2), and the walk skips the step
//     when the frames show the family killed (Part B). A leg rule would fail open there.
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
// A plan boss, phase or hold entry always counts, and so does a first entry the plan never pulls as trash. An
// ambiguous first entry (first-only, and in some pull group) counts within FirstAddBossRadius of the position of any
// boss that lists it (on any leg), and always when such a boss has no position in the plan (fail closed).
type BossRows struct {
	set       map[uint32]bool
	core      map[uint32]bool
	first     map[uint32][]firstOf
	ambiguous map[uint32]bool
}

// NewBossRows builds the rule from the plan (nil: no entry is a boss row).
func NewBossRows(plan *dungeon.Plan) *BossRows {
	b := &BossRows{set: map[uint32]bool{}, core: map[uint32]bool{}, first: map[uint32][]firstOf{}, ambiguous: map[uint32]bool{}}
	if plan == nil {
		return b
	}
	for _, e := range mapview.BossEntries(plan) {
		b.set[e] = true
	}
	trash := map[uint32]bool{}
	for _, leg := range plan.Legs {
		for _, g := range leg.PullGroups {
			for _, e := range g.Entries {
				trash[e] = true
			}
		}
		if leg.Boss == nil {
			continue
		}
		b.core[leg.Boss.Entry] = true
		for _, e := range leg.Boss.PhaseEntries {
			b.core[e] = true
		}
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
	for e := range b.first {
		if !b.core[e] && trash[e] {
			b.ambiguous[e] = true
		}
	}
	return b
}

// Ambiguous lists the plan's ambiguous first entries (first-only and pulled as trash somewhere), sorted.
func (b *BossRows) Ambiguous() []uint32 {
	var out []uint32
	for e := range b.ambiguous {
		out = append(out, e)
	}
	slices.Sort(out)
	return out
}

// Listed says whether the plan names the entry as a boss, a first add or a hold entry (position not considered).
func (b *BossRows) Listed(entry uint32) bool { return b != nil && b.set[entry] }

// Counts says whether a row of this entry standing at x, y is boss evidence.
func (b *BossRows) Counts(entry uint32, x, y float32) bool {
	if !b.Listed(entry) {
		return false
	}
	if !b.ambiguous[entry] {
		return true
	}
	for _, f := range b.first[entry] {
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
