package session

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/TortoiseBots/turtle-agent/internal/events"
	"github.com/TortoiseBots/turtle-agent/internal/game"
	"github.com/TortoiseBots/turtle-agent/internal/game/gametest"
	"github.com/TortoiseBots/turtle-agent/internal/objects"
	"github.com/TortoiseBots/turtle-agent/internal/objects/objtest"
	"github.com/TortoiseBots/turtle-agent/internal/opcode"
	"github.com/TortoiseBots/turtle-agent/internal/packet"
)

const (
	swordGUID  = packet.GUID(0x4000000000000003)
	potionGUID = packet.GUID(0x4000000000000004)
	playerEnd  = 0x502 // PLAYER_END (UpdateFields.h:281)
)

// ownInventory is the player's own create block carrying backpack slots, then the items' create
// blocks (non-living, UPDATEFLAG_ALL: Item.cpp:241, Object.cpp:474-484), as the server
// sends them to their owner.
func ownInventory(items map[uint8]packet.GUID, entries map[packet.GUID][2]uint32) []byte {
	w := objtest.Header(uint32(1 + len(entries)))
	for g, ec := range entries {
		w.U8(uint8(objects.BlockCreate2)).PackedGUID(g).U8(objects.TypeItem).U8(0x10).U32(1)
		objtest.Values(w, 0x30, map[uint16]uint32{objects.FieldEntry: ec[0], objects.ItemStackCount: ec[1]})
	}
	vals := map[uint16]uint32{objects.UnitHealth: 100, objects.UnitMaxHealth: 100}
	for slot, g := range items {
		vals[objects.PlayerInvSlotHead+2*uint16(slot)] = uint32(g)
		vals[objects.PlayerInvSlotHead+2*uint16(slot)+1] = uint32(g >> 32)
	}
	w.U8(uint8(objects.BlockCreate2)).PackedGUID(me.GUID).U8(objects.TypePlayer)
	objtest.Living(w, 0x21, game.MoveInfo{X: me.X, Y: me.Y, Z: me.Z}, nil)
	objtest.Values(w, playerEnd, vals)
	return w.Bytes()
}

func TestInventoryAndEquip(t *testing.T) {
	ctx := context.Background()
	h := start(t, Options{})
	h.enterWorld()
	if inv := h.s.Inventory(); len(inv) != 0 {
		t.Fatalf("before the create block %+v", inv)
	}
	h.send(opcode.SMSGUpdateObject, ownInventory(map[uint8]packet.GUID{23: swordGUID, 30: potionGUID},
		map[packet.GUID][2]uint32{swordGUID: {2589, 1}, potionGUID: {118, 5}}))
	waitFor(t, func() bool { return len(h.s.Inventory()) == 2 })
	if inv := h.s.Inventory(); inv[0] != (objects.Item{Bag: 255, Slot: 23, GUID: swordGUID, Entry: 2589, Count: 1}) || inv[1].Entry != 118 || inv[1].Count != 5 {
		t.Fatalf("%+v", inv)
	}

	it, err := h.s.Equip(ctx, EquipRef{Entry: 2589})
	if err != nil || it.Slot != 23 || it.GUID != swordGUID {
		t.Fatalf("%+v %v", it, err)
	}
	if p := h.expect(opcode.CMSGAutoequipItem); string(p.Body) != string(game.BuildAutoEquipItem(255, 23)) {
		t.Fatalf("% X", p.Body)
	}
	slot := uint8(30)
	if it, err := h.s.Equip(ctx, EquipRef{Slot: &slot}); err != nil || it.Entry != 118 {
		t.Fatalf("by slot %+v %v", it, err)
	}
	if p := h.expect(opcode.CMSGAutoequipItem); string(p.Body) != string(game.BuildAutoEquipItem(255, 30)) {
		t.Fatalf("% X", p.Body)
	}
	// Empty slots fail without a packet: a backpack slot, and a bag that is not there.
	empty, bag, bagSlot := uint8(24), uint8(19), uint8(0)
	for _, ref := range []EquipRef{{Slot: &empty}, {Bag: &bag, Slot: &bagSlot}} {
		if it, err := h.s.Equip(ctx, ref); err == nil || !strings.Contains(err.Error(), "no item at bag") {
			t.Fatalf("empty slot: %+v %v", it, err)
		}
	}
	if p, err := h.srv.Expect(opcode.CMSGAutoequipItem, 100*time.Millisecond); err == nil {
		t.Fatalf("sent % X", p.Body)
	}
	if _, err := h.s.Equip(ctx, EquipRef{Entry: 4537}); err == nil || !strings.Contains(err.Error(), "not in the backpack") {
		t.Fatalf("missing item: %v", err)
	}
	if _, err := h.s.Equip(ctx, EquipRef{}); err == nil || !strings.Contains(err.Error(), "item_entry or a slot") {
		t.Fatalf("no item: %v", err)
	}

	// The server takes the sword out of the backpack, or says why not.
	h.send(opcode.SMSGInventoryChangeFailure, gametest.InventoryChangeFailure(1, 40, potionGUID, 0))
	w := objtest.Header(1).U8(uint8(objects.BlockValues)).PackedGUID(me.GUID)
	objtest.Values(w, playerEnd, map[uint16]uint32{objects.PlayerInvSlotHead + 46: 0, objects.PlayerInvSlotHead + 47: 0})
	h.send(opcode.SMSGUpdateObject, w.Bytes())
	waitFor(t, func() bool { return len(h.s.Inventory()) == 1 })
	h.logout()
	e := ofKind(h.s.Log(), events.KindInventoryError)
	if len(e) != 1 || e[0].Data["result"] != uint8(1) || e[0].Data["required_level"] != uint32(40) || e[0].Data["item_entry"] != uint32(118) || e[0].Data["item_guid"] != potionGUID {
		t.Fatalf("%+v", e)
	}
}
