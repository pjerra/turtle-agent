package gametest

import "github.com/TortoiseBots/turtle-agent/internal/packet"

// Core cites in this file are tortoise-wow upstream/1181dev 5784fb0.

// LootStartRoll is Group::SendLootStartRoll (Group.cpp:751-772); the core writes 0 for the
// random suffix.
func LootStartRoll(looted packet.GUID, slot, item, randomProp, countdown uint32) []byte {
	return packet.NewWriter().GUID(looted).U32(slot).U32(item).U32(0).U32(randomProp).U32(countdown).Bytes()
}

// LootRoll is Group::SendLootRoll (Group.cpp:774-795). The core sends (0, 0) for a need
// choice, (128, 2) for greed, (128, 128) for pass (Group.cpp:954-973), and the dice
// (1..100, ROLL_NEED or ROLL_GREED) when the roll ends (Group.cpp:1160-1163, :1231-1234).
func LootRoll(looted packet.GUID, slot uint32, player packet.GUID, item, randomProp uint32, number, rollType uint8) []byte {
	return packet.NewWriter().GUID(looted).U32(slot).GUID(player).U32(item).U32(0).U32(randomProp).U8(number).U8(rollType).Bytes()
}

// LootRollWon is Group::SendLootRollWon (Group.cpp:797-818).
func LootRollWon(looted packet.GUID, slot, item, randomProp uint32, winner packet.GUID, number, rollType uint8) []byte {
	return packet.NewWriter().GUID(looted).U32(slot).U32(item).U32(0).U32(randomProp).GUID(winner).U8(number).U8(rollType).Bytes()
}

// LootAllPassed is Group::SendLootAllPassed (Group.cpp:820-838): here the random property
// comes before the (zero) suffix.
func LootAllPassed(looted packet.GUID, slot, item, randomProp uint32) []byte {
	return packet.NewWriter().GUID(looted).U32(slot).U32(item).U32(randomProp).U32(0).Bytes()
}

// LootSlot is one item of a LootResponse; Prop is the random property.
type LootSlot struct {
	Slot          uint8
	Item, Count   uint32
	Display, Prop uint32
	SlotType      uint8
}

// LootResponse is the window Player::SendLoot writes (Player.cpp:9679-9683, LootMgr.cpp:889-1055):
// guid, type, money, item count, then per item slot, item, count, display, a zero suffix,
// random property and slot type.
func LootResponse(guid packet.GUID, typ uint8, money uint32, items ...LootSlot) []byte {
	w := packet.NewWriter().GUID(guid).U8(typ).U32(money).U8(uint8(len(items)))
	for _, it := range items {
		w.U8(it.Slot).U32(it.Item).U32(it.Count).U32(it.Display).U32(0).U32(it.Prop).U8(it.SlotType)
	}
	return w.Bytes()
}

// LootRefused is Player::SendLootError (Player.cpp:9278-9284): guid, a zero type, the error.
func LootRefused(guid packet.GUID, loot uint8) []byte {
	return packet.NewWriter().GUID(guid).U8(0).U8(loot).Bytes()
}

// LootRelease is SMSG_LOOT_RELEASE_RESPONSE (Player.cpp:9270-9276).
func LootRelease(guid packet.GUID) []byte { return packet.NewWriter().GUID(guid).U8(1).Bytes() }

// LootRemoved is SMSG_LOOT_REMOVED (Player.cpp:9699-9704).
func LootRemoved(slot uint8) []byte { return []byte{slot} }
