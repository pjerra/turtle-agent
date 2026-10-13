package brain

import (
	"strings"
	"testing"
	"time"

	"github.com/pjerra/turtle-agent/internal/objects"
	"github.com/pjerra/turtle-agent/internal/spells"
)

func allConjureRanks() []uint32 {
	var ids []uint32
	for _, r := range append(append([]conjureRank{}, conjureWater...), conjureFood...) {
		ids = append(ids, r.spell)
	}
	return ids
}

// The tables hold what the spell table says: the ranks 1-7 of the two spells, creating better items rank by rank, each item in the
// drink or food table of its level.
func TestConjureTablesMatchTheSpellTable(t *testing.T) {
	for _, tc := range []struct {
		name    string
		ranks   []conjureRank
		levelOf func(uint32) (uint8, bool)
	}{{"Conjure Water", conjureWater, drinkLevel}, {"Conjure Food", conjureFood, foodLevel}} {
		var prevSpell, prevItem uint8
		for i, r := range tc.ranks {
			m, ok := spells.Lookup(r.spell)
			if !ok || !strings.EqualFold(spells.Name(r.spell), tc.name) || int(m.Rank) != i+1 || m.Effects[0].Effect != 24 {
				t.Errorf("%s rank %d: spell %d is %q %+v", tc.name, i+1, r.spell, spells.Name(r.spell), m)
			}
			lvl, ok := tc.levelOf(r.item)
			if !ok {
				t.Errorf("%s rank %d: item %d is in no table", tc.name, i+1, r.item)
			}
			if m.Level < prevSpell || lvl < prevItem {
				t.Errorf("%s rank %d: levels fall (spell %d, item %d)", tc.name, i+1, m.Level, lvl)
			}
			prevSpell, prevItem = m.Level, lvl
		}
	}
}

func TestConjurePicksTheHighestRankTheLeaderCanUse(t *testing.T) {
	g := newFake(allConjureRanks()...)
	c := &Ctx{Spells: g.st}
	for _, tc := range []struct {
		level       uint32
		water, food uint32
	}{
		{1, 5504, 587},
		{4, 5504, 587},
		{5, 5505, 597},
		{34, 6127, 6129}, // Conjured Spring Water 25, Conjured Pumpernickel 25
		{35, 10138, 10144},
		{44, 10138, 10144}, // level 45 is Mineral Water's successor: rank 7 (55) and Sparkling (45) are out at 44
		{45, 10139, 10145},
		{54, 10139, 10145},
		{55, 10140, 28612},
		{60, 10140, 28612},
	} {
		c.Self.Level = tc.level
		if w, ok := conjureWaterSpell(c); !ok || w != tc.water {
			t.Errorf("level %d: water %d %v, want %d", tc.level, w, ok, tc.water)
		}
		if f, ok := conjureFoodSpell(c); !ok || f != tc.food {
			t.Errorf("level %d: food %d %v, want %d", tc.level, f, ok, tc.food)
		}
	}
}

// Only the ranks the character knows count, and a known spell that is no player rank (an NPC's copy) is never picked.
func TestConjureOnlyConsidersKnownRanks(t *testing.T) {
	g := newFake(5504, 5505, 10143) // 10143: Conjure Water rank 7 of an NPC (effect 36)
	c := &Ctx{Spells: g.st}
	c.Self.Level = 60
	if w, ok := conjureWaterSpell(c); !ok || w != 5505 {
		t.Fatalf("%d %v", w, ok)
	}
	g = newFake(10143)
	c = &Ctx{Spells: g.st}
	c.Self.Level = 60
	if w, ok := conjureWaterSpell(c); ok {
		t.Fatalf("an unknown spell was picked: %d", w)
	}
}

// Live run 3: a level 45 mage that knew every rank conjured Conjured Crystal Water (level 55) with rank 7 and could not drink it.
func TestMageConjuresAWaterItsLevelCanDrink(t *testing.T) {
	r := restRig(t, allConjureRanks()...)
	r.g.setSelf(func(u *objects.Unit) { u.Power = 600; u.Level = 45 })
	d := r.tick(time.Second)
	if d == nil || d.Action != "conjure_water" || d.Spell != 10139 {
		t.Fatalf("%+v", d)
	}
	r = restRig(t, allConjureRanks()...)
	r.g.setSelf(func(u *objects.Unit) { u.Health = 300; u.Level = 45 })
	if d := r.tick(time.Second); d == nil || d.Action != "conjure_food" || d.Spell != 10145 {
		t.Fatalf("%+v", d)
	}
}
