package objects

import (
	"testing"

	"github.com/pjerra/turtle-agent/internal/opcode"
	"github.com/pjerra/turtle-agent/internal/packet"
	"github.com/pjerra/turtle-agent/internal/testfixture"
	"github.com/pjerra/turtle-agent/internal/worldconn"
)

// invSlot sets inventory slot i of a player to item g (two words per slot from
// PLAYER_FIELD_INV_SLOT_HEAD, UpdateFields.h:202-205).
func invSlot(vals map[uint16]uint32, i int, g packet.GUID) map[uint16]uint32 {
	vals[PlayerInvSlotHead+uint16(2*i)] = uint32(g)
	vals[PlayerInvSlotHead+uint16(2*i)+1] = uint32(g >> 32)
	return vals
}

func itemCreate(g packet.GUID, typ uint8, entry, count uint32) Block {
	return Block{Type: BlockCreate2, GUID: g, ObjType: typ, Move: &Movement{UpdateFlags: 0x10},
		Values: map[uint16]uint32{FieldEntry: entry, ItemStackCount: count}}
}

func TestBackpackFromOwnFields(t *testing.T) {
	const (
		me                    = packet.GUID(0x2A)
		helm, bag             = packet.GUID(0x4000000000000001), packet.GUID(0x4000000000000002)
		sword, potions, later = packet.GUID(0x4000000000000003), packet.GUID(0x4000000000000004), packet.GUID(0x4000000000000005)
	)
	s := NewStore()
	if got := s.Backpack(me); len(got) != 0 {
		t.Fatalf("unknown player %+v", got)
	}
	vals := map[uint16]uint32{UnitHealth: 100}
	invSlot(vals, 0, helm)     // equipped head
	invSlot(vals, 19, bag)     // first bag slot
	invSlot(vals, 30, potions) // backpack, out of order on purpose
	invSlot(vals, 23, sword)
	invSlot(vals, 38, later) // last backpack slot; its create block has not arrived
	s.Apply([]Block{
		itemCreate(helm, TypeItem, 12064, 1), itemCreate(bag, TypeContainer, 4500, 1),
		itemCreate(sword, TypeItem, 2589, 1), itemCreate(potions, TypeItem, 118, 5),
		createBlock(me, TypePlayer, 0, 0, vals),
	})
	want := []Item{
		{Bag: BackpackBag, Slot: 23, GUID: sword, Entry: 2589, Count: 1},
		{Bag: BackpackBag, Slot: 30, GUID: potions, Entry: 118, Count: 5},
		{Bag: BackpackBag, Slot: 38, GUID: later},
	}
	got := s.Backpack(me)
	if len(got) != len(want) {
		t.Fatalf("%+v", got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("item %d: %+v want %+v", i, got[i], want[i])
		}
	}
	// Equipping the sword empties slot 23 (a values update for the player).
	s.Apply([]Block{{Type: BlockValues, GUID: me, Values: invSlot(map[uint16]uint32{}, 23, 0)}})
	if got := s.Backpack(me); len(got) != 2 || got[0].Slot != 30 {
		t.Fatalf("after equip %+v", got)
	}
}

func TestItemAtBackpackAndBags(t *testing.T) {
	const (
		me         = packet.GUID(0x2A)
		bag, stone = packet.GUID(0x4000000000000002), packet.GUID(0x4000000000000006)
		sword      = packet.GUID(0x4000000000000003)
	)
	s := NewStore()
	if _, ok := s.ItemAt(me, BackpackBag, 23); ok {
		t.Fatal("unknown player")
	}
	bagBlock := itemCreate(bag, TypeContainer, 4500, 1)
	bagBlock.Values[ContainerNumSlots] = 6
	bagBlock.Values[ContainerSlot1+2*2], bagBlock.Values[ContainerSlot1+2*2+1] = uint32(stone&0xFFFFFFFF), uint32(stone>>32)
	s.Apply([]Block{bagBlock, itemCreate(stone, TypeItem, 6948, 1), itemCreate(sword, TypeItem, 2589, 1),
		createBlock(me, TypePlayer, 0, 0, invSlot(invSlot(map[uint16]uint32{}, 19, bag), 23, sword))})
	for _, c := range []struct {
		bag, slot uint8
		want      Item
		ok        bool
	}{
		{BackpackBag, 23, Item{Bag: BackpackBag, Slot: 23, GUID: sword, Entry: 2589, Count: 1}, true},
		{BackpackBag, 24, Item{}, false},
		{BackpackBag, 39, Item{}, false}, // past the backpack: bank slots are not carried
		{19, 2, Item{Bag: 19, Slot: 2, GUID: stone, Entry: 6948, Count: 1}, true},
		{19, 1, Item{}, false},
		{19, 6, Item{}, false}, // past the bag's 6 slots
		{20, 0, Item{}, false}, // no bag there
		{7, 0, Item{}, false},  // not a bag slot
	} {
		if got, ok := s.ItemAt(me, c.bag, c.slot); ok != c.ok || got != c.want {
			t.Errorf("(%d, %d): %+v %v", c.bag, c.slot, got, ok)
		}
	}
}

func TestFreeSlots(t *testing.T) {
	const (
		me         = packet.GUID(0x2A)
		bag, stone = packet.GUID(0x4000000000000002), packet.GUID(0x4000000000000006)
		sword      = packet.GUID(0x4000000000000003)
	)
	s := NewStore()
	if got := s.FreeSlots(me); got != nil {
		t.Fatalf("unknown player: %v", got)
	}
	bagBlock := itemCreate(bag, TypeContainer, 4500, 1)
	bagBlock.Values[ContainerNumSlots] = 3
	bagBlock.Values[ContainerSlot1+2*1], bagBlock.Values[ContainerSlot1+2*1+1] = uint32(stone&0xFFFFFFFF), uint32(stone>>32)
	s.Apply([]Block{bagBlock, itemCreate(stone, TypeItem, 6948, 1), itemCreate(sword, TypeItem, 2589, 1),
		createBlock(me, TypePlayer, 0, 0, invSlot(invSlot(map[uint16]uint32{}, 19, bag), 24, sword))})
	got := s.FreeSlots(me)
	// 16 backpack slots less slot 24, then bag 19's slots 0 and 2 (slot 1 holds the stone).
	if len(got) != 15+2 || got[0] != [2]uint8{BackpackBag, 23} || got[1] != [2]uint8{BackpackBag, 25} ||
		got[15] != [2]uint8{19, 0} || got[16] != [2]uint8{19, 2} {
		t.Fatalf("%v", got)
	}
}

// An imbued weapon shows its temporary enchantment (ITEM_FIELD_ENCHANTMENT slot 1, the id word) on the item row.
func TestItemAtReadsTheTemporaryEnchantment(t *testing.T) {
	const (
		me            = packet.GUID(0x2A)
		sword, dagger = packet.GUID(0x4000000000000003), packet.GUID(0x4000000000000007)
	)
	s := NewStore()
	imbued := itemCreate(sword, TypeItem, 2589, 1)
	imbued.Values[ItemTempEnchantID] = 3345 // Windfury Weapon
	imbued.Values[ItemTempEnchantID+1] = 3600000
	s.Apply([]Block{imbued, itemCreate(dagger, TypeItem, 2092, 1),
		createBlock(me, TypePlayer, 0, 0, invSlot(invSlot(map[uint16]uint32{}, 15, sword), 16, dagger))})
	if it, ok := s.ItemAt(me, BackpackBag, 15); !ok || it.TempEnchant != 3345 || it.Entry != 2589 {
		t.Fatalf("%+v %v", it, ok)
	}
	if it, ok := s.ItemAt(me, BackpackBag, 16); !ok || it.TempEnchant != 0 {
		t.Fatalf("%+v %v", it, ok)
	}
	// The imbue running out is a values update for the item.
	s.Apply([]Block{{Type: BlockValues, GUID: sword, Values: map[uint16]uint32{ItemTempEnchantID: 0}}})
	if it, _ := s.ItemAt(me, BackpackBag, 15); it.TempEnchant != 0 {
		t.Fatalf("%+v", it)
	}
}

// The m1 login capture: Tagentone carries one item in the backpack, entry 80817 in slot 23,
// next to three equipped items and four bags.
func TestCapturedBackpack(t *testing.T) {
	recs := testfixture.Load(t, "m1-login.twcap")
	s := NewStore()
	var self packet.GUID
	apply := func(blocks []Block, err error) {
		if err != nil {
			t.Fatal(err)
		}
		for _, b := range blocks {
			if (b.Type == BlockCreate || b.Type == BlockCreate2) && b.Move != nil && b.Move.UpdateFlags&updateFlagSelf != 0 {
				self = b.GUID
			}
		}
		s.Apply(blocks)
	}
	for _, r := range recs { // in arrival order, as the session applies them
		switch {
		case r.Dir != worldconn.ServerToClient || r.Redacted:
		case r.Op == opcode.SMSGUpdateObject:
			apply(ParseUpdate(r.Body))
		case r.Op == opcode.SMSGCompressedUpdateObject:
			apply(ParseCompressedUpdate(r.Body))
		}
	}
	got := s.Backpack(self)
	if self == 0 || len(got) != 1 || got[0] != (Item{Bag: BackpackBag, Slot: 23, GUID: 0x4000000000028E4A, Entry: 80817, Count: 1}) {
		t.Fatalf("self %s backpack %+v", self, got)
	}
}
