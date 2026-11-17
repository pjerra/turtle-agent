package objects

import (
	"testing"

	"github.com/TortoiseBots/turtle-agent/internal/packet"
)

func TestBagsListsEveryBag(t *testing.T) {
	const (
		me           = packet.GUID(0x2A)
		bag1, bag2   = packet.GUID(0x4000000000000002), packet.GUID(0x4000000000000007)
		stone, bread = packet.GUID(0x4000000000000006), packet.GUID(0x4000000000000008)
	)
	s := NewStore()
	if got := s.Bags(me); len(got) != 0 {
		t.Fatalf("unknown player %+v", got)
	}
	mk := func(g packet.GUID, slot int, item packet.GUID) Block {
		b := itemCreate(g, TypeContainer, 4500, 1)
		b.Values[ContainerNumSlots] = 8
		b.Values[ContainerSlot1+2*uint16(slot)], b.Values[ContainerSlot1+2*uint16(slot)+1] = uint32(item), uint32(item>>32)
		return b
	}
	s.Apply([]Block{mk(bag1, 2, stone), mk(bag2, 5, bread), itemCreate(stone, TypeItem, 6948, 1), itemCreate(bread, TypeItem, 4540, 4),
		createBlock(me, TypePlayer, 0, 0, invSlot(invSlot(map[uint16]uint32{}, 19, bag1), 21, bag2))})
	want := []Item{{Bag: 19, Slot: 2, GUID: stone, Entry: 6948, Count: 1}, {Bag: 21, Slot: 5, GUID: bread, Entry: 4540, Count: 4}}
	got := s.Bags(me)
	if len(got) != 2 || got[0] != want[0] || got[1] != want[1] {
		t.Fatalf("%+v", got)
	}
}

// A mount shows as the mount display field and the stand state as byte 0 of bytes 1; both
// report changes after the create block, not in it.
func TestMountAndStandChanges(t *testing.T) {
	const me = packet.GUID(0x2A)
	s := NewStore()
	if ch := s.Apply([]Block{createBlock(me, TypePlayer, 0, 0, map[uint16]uint32{UnitBytes1: 0x00000100})}); len(ch) != 1 || ch[0].Kind != ChangeCreated {
		t.Fatalf("create %+v", ch)
	}
	ch := s.Apply([]Block{{Type: BlockValues, GUID: me, Values: map[uint16]uint32{UnitMountDisplay: 14337, UnitBytes1: 0x00000101, UnitAura + 4: 458}}})
	want := []Change{{GUID: me, Kind: ChangeAura, Slot: 4, New: 458}, {GUID: me, Kind: ChangeMount, New: 14337}, {GUID: me, Kind: ChangeStand, Old: 0, New: 1}}
	if len(ch) != len(want) {
		t.Fatalf("%+v", ch)
	}
	for i := range want {
		if ch[i] != want[i] {
			t.Errorf("change %d: %+v want %+v", i, ch[i], want[i])
		}
	}
	if s.Field(me, UnitMountDisplay) != 14337 || s.Field(0x99, UnitMountDisplay) != 0 {
		t.Fatal("Field")
	}
	// Only byte 0 counts: the other bytes of UNIT_FIELD_BYTES_1 moving is no stand change.
	if ch := s.Apply([]Block{{Type: BlockValues, GUID: me, Values: map[uint16]uint32{UnitBytes1: 0x00030101}}}); len(ch) != 0 {
		t.Fatalf("other byte %+v", ch)
	}
	if ch := s.Apply([]Block{{Type: BlockValues, GUID: me, Values: map[uint16]uint32{UnitMountDisplay: 0}}}); len(ch) != 1 || ch[0].New != 0 || ch[0].Old != 14337 {
		t.Fatalf("dismount %+v", ch)
	}
}
