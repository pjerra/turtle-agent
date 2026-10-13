package objects

import (
	"math"
	"testing"

	"github.com/pjerra/turtle-agent/internal/packet"
)

func fbits(f float32) uint32 { return math.Float32bits(f) }

func TestDynamicObjectsListTheAreasNearestFirst(t *testing.T) {
	s, _ := newClockStore()
	near, far, noPos := packet.GUID(0xF100000000000001), packet.GUID(0xF100000000000002), packet.GUID(0xF100000000000003)
	caster := packet.GUID(0xF130000000000042)
	s.Apply([]Block{
		{Type: BlockCreate, GUID: far, ObjType: TypeDynamic, Move: &Movement{HasPosition: true, X: 30, Y: 40, Z: 1},
			Values: map[uint16]uint32{DynSpell: 5740, DynRadius: fbits(8), DynCaster: uint32(caster), DynCaster + 1: uint32(caster >> 32)}},
		{Type: BlockCreate, GUID: near, ObjType: TypeDynamic, Move: &Movement{HasPosition: true, X: 3, Y: 4, Z: 0},
			Values: map[uint16]uint32{DynSpell: 10, DynRadius: fbits(5)}},
		// Values before a create block: the position fields stand in for the movement block.
		{Type: BlockValues, GUID: noPos, Values: map[uint16]uint32{DynSpell: 7, DynRadius: fbits(2), DynPosX: fbits(6), DynPosX + 1: fbits(8)}},
	})
	got := s.DynamicObjects(0, 0)
	if len(got) != 3 || got[0].GUID != near || got[1].GUID != noPos || got[2].GUID != far {
		t.Fatalf("%+v", got)
	}
	if got[0].Distance != 5 || got[0].Spell != 10 || got[0].Radius != 5 {
		t.Fatalf("near: %+v", got[0])
	}
	if got[1].X != 6 || got[1].Y != 8 || got[1].Distance != 10 {
		t.Fatalf("by fields: %+v", got[1])
	}
	if got[2].Caster != caster || got[2].Distance != 50 || got[2].Spell != 5740 {
		t.Fatalf("far: %+v", got[2])
	}
	s.Destroy(near)
	if got = s.DynamicObjects(0, 0); len(got) != 2 {
		t.Fatalf("after destroy: %+v", got)
	}
	// Units and game objects are not dynamic objects.
	s.Apply([]Block{createBlock(wolf, TypeUnit, 1, 1, nil)})
	if got = s.DynamicObjects(0, 0); len(got) != 2 {
		t.Fatalf("a unit counted: %+v", got)
	}
}

func TestCastCarriesItsTarget(t *testing.T) {
	s, _ := newClockStore()
	s.Apply([]Block{createBlock(wolf, TypeUnit, 0, 0, nil)})
	s.StartCastAt(wolf, 116, 2500, 77)
	if u, _ := s.Get(wolf, 0, 0, 0); u.Casting == nil || u.Casting.Target != 77 {
		t.Fatalf("%+v", u.Casting)
	}
	s.StartCast(wolf, 116, 2500) // no target known
	if u, _ := s.Get(wolf, 0, 0, 0); u.Casting == nil || u.Casting.Target != 0 {
		t.Fatalf("%+v", u.Casting)
	}
}
