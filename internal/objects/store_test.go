package objects

import (
	"math"
	"testing"

	"github.com/pjerra/turtle-agent/internal/packet"
)

func createBlock(g packet.GUID, typ uint8, x, y float32, vals map[uint16]uint32) Block {
	return Block{Type: BlockCreate2, GUID: g, ObjType: typ, Move: &Movement{Living: true, HasPosition: true, X: x, Y: y}, Values: vals}
}

func TestApplyCreateValuesAndUnits(t *testing.T) {
	s := NewStore()
	ch := s.Apply([]Block{
		createBlock(wolf, TypeUnit, 10, 0, map[uint16]uint32{FieldEntry: 299, UnitHealth: 100, UnitMaxHealth: 100, UnitLevel: 5, UnitAura + 33: 589}),
		createBlock(0x30, TypePlayer, 3, 4, map[uint16]uint32{UnitHealth: 500, UnitMaxHealth: 600, UnitBytes0: 1 << 24, UnitPower1 + 1: 40, UnitMaxPower1 + 1: 100}),
	})
	if len(ch) != 2 || ch[0].Kind != ChangeCreated || ch[1].Kind != ChangeCreated {
		t.Fatalf("created objects report only creation: %+v", ch)
	}
	us := s.Units(Filter{}, 0, 0, 0)
	if len(us) != 2 || us[0].GUID != 0x30 || us[0].Distance != 5 || us[0].Type != "player" || us[0].Power != 40 || us[0].PowerType != 1 {
		t.Fatalf("%+v", us)
	}
	w := us[1]
	if w.Entry != 299 || w.Type != "creature" || !w.Hostile || w.Dead || len(w.Auras) != 1 || w.Auras[0].SpellID != 589 || w.Auras[0].Slot != 33 || !w.Auras[0].Harmful || w.Auras[0].Stacks != 1 {
		t.Fatalf("%+v", w)
	}
	ch = s.Apply([]Block{{Type: BlockValues, GUID: wolf, Values: map[uint16]uint32{UnitHealth: 0, UnitAura + 33: 0, UnitAura + 2: 1243, UnitFlags: UnitFlagInCombat}}})
	want := map[ChangeKind]Change{
		ChangeHealth: {GUID: wolf, Kind: ChangeHealth, Old: 100, New: 0},
	}
	auras := 0
	for _, c := range ch {
		if c.Kind == ChangeAura {
			auras++
			if (c.Slot == 33 && (c.Old != 589 || c.New != 0)) || (c.Slot == 2 && c.New != 1243) {
				t.Fatalf("aura change %+v", c)
			}
			continue
		}
		if want[c.Kind] != c {
			t.Fatalf("change %+v", c)
		}
	}
	if auras != 2 || len(ch) != 3 {
		t.Fatalf("%+v", ch)
	}
	if u, ok := s.Get(wolf, 0, 0, 0); !ok || !u.Dead || u.Hostile || !u.InCombat {
		t.Fatalf("%+v", u)
	}
}

func TestOutOfRangeDestroyAndClear(t *testing.T) {
	s := NewStore()
	s.Apply([]Block{createBlock(wolf, TypeUnit, 1, 1, nil), createBlock(0x30, TypePlayer, 2, 2, nil)})
	ch := s.Apply([]Block{{Type: BlockOutOfRange, GUIDs: []packet.GUID{wolf, 0x99}}})
	if len(ch) != 0 {
		t.Fatalf("%+v", ch)
	}
	if _, ok := s.Get(wolf, 0, 0, 0); ok {
		t.Fatal("unit kept")
	}
	if !s.Destroy(0x30) || s.Destroy(0x30) {
		t.Fatal("destroy")
	}
	s.Apply([]Block{createBlock(wolf, TypeUnit, 1, 1, nil)})
	s.Clear()
	if len(s.Units(Filter{}, 0, 0, 0)) != 0 {
		t.Fatal("clear")
	}
}

func TestFiltersAndMoveTo(t *testing.T) {
	s := NewStore()
	s.Apply([]Block{
		createBlock(wolf, TypeUnit, 10, 0, map[uint16]uint32{FieldEntry: 299, UnitHealth: 1, UnitMaxHealth: 1}),
		createBlock(0xF130000064000002, TypeUnit, 50, 0, map[uint16]uint32{FieldEntry: 100, UnitHealth: 1, UnitMaxHealth: 1, UnitNPCFlags: 2}),
		createBlock(0xF110000000000003, TypeGameObject, 1, 0, nil),
	})
	no, yes := false, true
	if us := s.Units(Filter{Near: 20}, 0, 0, 0); len(us) != 1 || us[0].GUID != wolf {
		t.Fatalf("near: %+v", us)
	}
	if us := s.Units(Filter{Entry: 100}, 0, 0, 0); len(us) != 1 || us[0].NPCFlags != 2 {
		t.Fatalf("entry: %+v", us)
	}
	if us := s.Units(Filter{Hostile: &yes}, 0, 0, 0); len(us) != 1 || us[0].GUID != wolf {
		t.Fatalf("hostile: %+v", us)
	}
	if us := s.Units(Filter{Hostile: &no, Limit: 1}, 0, 0, 0); len(us) != 1 || us[0].Entry != 100 {
		t.Fatalf("friendly: %+v", us)
	}
	s.MoveTo(wolf, 100, 0, 0, 0)
	if u, _ := s.Get(wolf, 0, 0, 0); u.Distance != 100 {
		t.Fatalf("moved %+v", u)
	}
}

func TestValuesBeforeCreateAreKept(t *testing.T) {
	s := NewStore()
	s.Apply([]Block{{Type: BlockValues, GUID: wolf, Values: map[uint16]uint32{UnitHealth: 9, UnitMaxHealth: 10}}})
	if u, ok := s.Get(wolf, 0, 0, 0); !ok || u.Health != 9 || u.Type != "creature" {
		t.Fatalf("%+v", u)
	}
}

// With ShowHealthValues off the server sends health as a percent and max health 100
// (Object.cpp:727-748); the store keeps them as sent and Dead still follows health 0.
func TestPercentHealthPassesThrough(t *testing.T) {
	s := NewStore()
	s.Apply([]Block{createBlock(wolf, TypeUnit, 1, 1, map[uint16]uint32{UnitHealth: 1, UnitMaxHealth: 100})})
	u, _ := s.Get(wolf, 0, 0, 0)
	if u.Health != 1 || u.MaxHealth != 100 || u.Dead {
		t.Fatalf("%+v", u)
	}
	ch := s.Apply([]Block{{Type: BlockValues, GUID: wolf, Values: map[uint16]uint32{UnitHealth: 0}}})
	if u, _ = s.Get(wolf, 0, 0, 0); !u.Dead || len(ch) != 1 || ch[0].Old != 1 || ch[0].New != 0 {
		t.Fatalf("%+v %+v", u, ch)
	}
}

// A create block for a GUID the store already holds (a respawn, a unit back in range)
// starts the object afresh: old auras and values are gone and only ChangeCreated is
// reported (ruling T20 minor 1).
func TestCreateForExistingGUIDResets(t *testing.T) {
	s := NewStore()
	s.Apply([]Block{createBlock(wolf, TypeUnit, 1, 1, map[uint16]uint32{UnitHealth: 0, UnitMaxHealth: 50, UnitAura + 33: 589, UnitTarget: 0x2A})})
	ch := s.Apply([]Block{createBlock(wolf, TypeUnit, 2, 2, map[uint16]uint32{UnitHealth: 50, UnitMaxHealth: 50})})
	if len(ch) != 1 || ch[0] != (Change{GUID: wolf, Kind: ChangeCreated}) {
		t.Fatalf("%+v", ch)
	}
	if u, _ := s.Get(wolf, 0, 0, 0); u.Health != 50 || u.Dead || len(u.Auras) != 0 || u.Target != 0 || u.X != 2 {
		t.Fatalf("%+v", u)
	}
}

// Units at the same distance come back in GUID order, so a limit cuts the same set every
// call (ruling T20 minor 3).
func TestUnitsTieBreakOnGUID(t *testing.T) {
	s := NewStore()
	var blocks []Block
	for _, g := range []packet.GUID{0x35, 0x31, 0x34, 0x32, 0x33} {
		blocks = append(blocks, createBlock(g, TypePlayer, 3, 4, nil))
	}
	s.Apply(blocks)
	for i := 0; i < 20; i++ {
		us := s.Units(Filter{Limit: 3}, 0, 0, 0)
		if len(us) != 3 || us[0].GUID != 0x31 || us[1].GUID != 0x32 || us[2].GUID != 0x33 {
			t.Fatalf("%+v", us)
		}
	}
}

func TestOwnersReadsTheThreeOwnerFields(t *testing.T) {
	const pet, totem, charmed = packet.GUID(0xF140000001000001), packet.GUID(0xF130000F8E000002), packet.GUID(0xF130000001000003)
	owner := packet.GUID(0x0000000100000030) // both words set, so the high word is read too
	s := NewStore()
	s.Apply([]Block{
		createBlock(pet, TypeUnit, 0, 0, map[uint16]uint32{UnitCreatedBy: 0x30, UnitCreatedBy + 1: 1}),
		createBlock(totem, TypeUnit, 0, 0, map[uint16]uint32{UnitSummonedBy: 0x30, UnitSummonedBy + 1: 1}),
		createBlock(charmed, TypeUnit, 0, 0, nil),
	})
	s.Apply([]Block{{Type: BlockValues, GUID: charmed, Values: map[uint16]uint32{UnitCharmedBy: 0x30, UnitCharmedBy + 1: 1}}})
	for _, c := range []struct {
		g    packet.GUID
		want [3]packet.GUID
	}{
		{pet, [3]packet.GUID{0, 0, owner}},
		{totem, [3]packet.GUID{0, owner, 0}},
		{charmed, [3]packet.GUID{owner, 0, 0}},
		{wolf, [3]packet.GUID{}}, // not in the store
	} {
		if got := s.Owners(c.g); got != c.want {
			t.Fatalf("%v: %v, want %v", c.g, got, c.want)
		}
	}
}

// A spline sets the unit's facing from its travel direction; a stop keeps the old one.
// These splines take no time, so the unit is at once where they end.
func TestMoveSplineFacesTravelDirection(t *testing.T) {
	s := NewStore()
	s.Apply([]Block{createBlock(wolf, TypeUnit, 0, 0, nil)})
	jump := func(from, to [3]float32, stop bool) {
		s.MoveSpline(MonsterMove{GUID: wolf, Start: from, Dest: to, Stop: stop})
	}
	jump([3]float32{0, 0, 0}, [3]float32{0, 10, 0}, false)
	if u, _ := s.Get(wolf, 0, 0, 0); u.Y != 10 || math.Abs(float64(u.O)-math.Pi/2) > 1e-5 {
		t.Fatalf("north: %+v", u)
	}
	jump([3]float32{0, 10, 0}, [3]float32{0, -10, 0}, false)
	if u, _ := s.Get(wolf, 0, 0, 0); math.Abs(float64(u.O)-1.5*math.Pi) > 1e-5 {
		t.Fatalf("south must wrap into [0, 2pi): %+v", u)
	}
	jump([3]float32{5, 5, 0}, [3]float32{5, 5, 0}, true)
	if u, _ := s.Get(wolf, 0, 0, 0); math.Abs(float64(u.O)-1.5*math.Pi) > 1e-5 || u.X != 5 {
		t.Fatalf("a stop keeps the facing: %+v", u)
	}
	jump([3]float32{5, 5, 0}, [3]float32{5, 5, 3}, false)
	if u, _ := s.Get(wolf, 0, 0, 0); math.Abs(float64(u.O)-1.5*math.Pi) > 1e-5 || u.Z != 3 {
		t.Fatalf("a vertical move keeps the facing: %+v", u)
	}
}

// A unit that plays on another power than mana keeps its mana in UNIT_FIELD_POWER1: a druid in Cat Form (energy, power type 3) or Bear Form
// (rage, 1) shows the form's power in Power and its mana in Mana.
func TestManaOfAShapeshiftedUnit(t *testing.T) {
	s := NewStore()
	s.Apply([]Block{
		createBlock(0x30, TypePlayer, 1, 1, map[uint16]uint32{UnitHealth: 500, UnitMaxHealth: 600, UnitBytes0: 3 << 24, UnitPower1: 700, UnitMaxPower1: 1000,
			UnitPower1 + 3: 80, UnitMaxPower1 + 3: 100}),
		createBlock(0x31, TypePlayer, 2, 2, map[uint16]uint32{UnitHealth: 500, UnitMaxHealth: 600, UnitBytes0: 0, UnitPower1: 300, UnitMaxPower1: 900}),
		createBlock(0x32, TypePlayer, 3, 3, map[uint16]uint32{UnitHealth: 500, UnitMaxHealth: 600, UnitBytes0: 1 << 24, UnitPower1 + 1: 40, UnitMaxPower1 + 1: 100}),
	})
	byGUID := map[packet.GUID]Unit{}
	for _, u := range s.Units(Filter{}, 0, 0, 0) {
		byGUID[u.GUID] = u
	}
	if u := byGUID[0x30]; u.PowerType != 3 || u.Power != 80 || u.MaxPower != 100 || u.Mana != 700 || u.MaxMana != 1000 {
		t.Fatalf("a cat: %+v", u)
	}
	if u := byGUID[0x31]; u.PowerType != 0 || u.Power != 300 || u.MaxPower != 900 || u.Mana != 0 || u.MaxMana != 0 {
		t.Fatalf("a unit on mana has its mana in Power: %+v", u)
	}
	if u := byGUID[0x32]; u.PowerType != 1 || u.Power != 40 || u.Mana != 0 || u.MaxMana != 0 {
		t.Fatalf("a warrior has no mana pool to show: %+v", u)
	}
}
