package game

import (
	"fmt"

	"github.com/TortoiseBots/turtle-agent/internal/packet"
)

// Core cites in this file are tortoise-wow upstream/1181dev 5784fb0.

// Loot window types, the u8 after the guid in SMSG_LOOT_RESPONSE (LootMgr.h:51-60). The
// core folds skinning, insignia, fishing holes and failed fishing into these before
// sending (Player.cpp:9660-9677).
const (
	LootCorpse       uint8 = 1
	LootPickpocket   uint8 = 2
	LootFishing      uint8 = 3
	LootDisenchant   uint8 = 4
	lootResponseFail uint8 = 0 // a refusal: the next u8 is a LootError
)

// LootTypeName is the window type as a word.
func LootTypeName(t uint8) string {
	switch t {
	case LootCorpse:
		return "corpse"
	case LootPickpocket:
		return "pickpocket"
	case LootFishing:
		return "fishing"
	case LootDisenchant:
		return "disenchant"
	}
	return fmt.Sprintf("type_%d", t)
}

// Loot slot types, the last u8 of each item in SMSG_LOOT_RESPONSE (LootMgr.h:75-83).
const (
	LootSlotAllow   uint8 = 0 // the player can take it
	LootSlotRolling uint8 = 1 // a group roll is running
	LootSlotMaster  uint8 = 2 // only the master looter hands it out
	LootSlotLocked  uint8 = 3 // shown in red, cannot be taken
	LootSlotOwner   uint8 = 4 // the player's own, no binding prompt
)

// LootSlotTypeName is the slot type as a word.
func LootSlotTypeName(t uint8) string {
	switch t {
	case LootSlotAllow:
		return "allow_loot"
	case LootSlotRolling:
		return "roll_ongoing"
	case LootSlotMaster:
		return "master"
	case LootSlotLocked:
		return "locked"
	case LootSlotOwner:
		return "owner"
	}
	return fmt.Sprintf("slot_type_%d", t)
}

// LootTakeable says a slot of this type can be sent to CMSG_AUTOSTORE_LOOT_ITEM.
func LootTakeable(slotType uint8) bool { return slotType == LootSlotAllow || slotType == LootSlotOwner }

// LootErrorName is a LootError (LootMgr.h:85-99), the reason of a refused SMSG_LOOT_RESPONSE.
func LootErrorName(e uint8) string {
	switch e {
	case 0:
		return "didnt_kill"
	case 4:
		return "too_far"
	case 5:
		return "bad_facing"
	case 6:
		return "locked"
	case 8:
		return "not_standing"
	case 9:
		return "stunned"
	case 10:
		return "player_not_found"
	case 11:
		return "play_time_exceeded"
	case 12:
		return "master_inv_full"
	case 13:
		return "master_unique_item"
	case 14:
		return "master_other"
	case 15:
		return "already_pickpocketed"
	case 16:
		return "not_while_shapeshifted"
	}
	return fmt.Sprintf("error_%d", e)
}

// LootErrorText is what the 1.12 client prints for a LootError.
func LootErrorText(e uint8) string {
	switch e {
	case 0:
		return "no permission to loot that"
	case 4:
		return "too far away to loot that"
	case 5:
		return "must be facing the corpse to loot it"
	case 6:
		return "someone is already looting that"
	case 8:
		return "must be standing to loot"
	case 9:
		return "cannot loot while stunned"
	case 15:
		return "its pockets were already picked"
	case 16:
		return "cannot do that while shapeshifted"
	}
	return LootErrorName(e)
}

// BuildLoot is CMSG_LOOT: u64 guid of the creature, player corpse or bones to open
// (LootHandler.cpp:414-420). Containers, chests and bags open through a use, not this.
func BuildLoot(g packet.GUID) []byte { return packet.NewWriter().GUID(g).Bytes() }

// BuildAutoStoreLootItem is CMSG_AUTOSTORE_LOOT_ITEM: u8 loot slot (LootHandler.cpp:41-51).
func BuildAutoStoreLootItem(slot uint8) []byte { return []byte{slot} }

// BuildLootRelease is CMSG_LOOT_RELEASE: a u64 guid that the core reads and ignores, using
// the guid it holds for the player instead (LootHandler.cpp:452-462). CMSG_LOOT_MONEY has no
// body (LootHandler.cpp:292-300).
func BuildLootRelease(g packet.GUID) []byte { return packet.NewWriter().GUID(g).Bytes() }

// LootItem is one slot of a loot window: the loot slot, the item and how many, and its
// slot type.
type LootItem struct {
	Slot       uint8
	Item       uint32
	Count      uint32
	DisplayID  uint32
	RandomProp uint32
	SlotType   uint8
}

// LootResponse is SMSG_LOOT_RESPONSE. Err is set (with Refused) when the core refused the
// loot: it writes the guid, a zero type and a LootError (Player.cpp:9278-9284).
type LootResponse struct {
	GUID    packet.GUID
	Type    uint8
	Refused bool
	Err     uint8
	Money   uint32
	Items   []LootItem
}

// ParseLootResponse decodes SMSG_LOOT_RESPONSE: guid, type, u32 money, u8 count, then per
// item u8 slot, u32 item, u32 count, u32 display id, u32 random suffix, u32 random property
// and u8 slot type (Player.cpp:9679-9683, LootMgr.cpp:889-1055). A refusal is the
// guid, a zero type and the error byte.
func ParseLootResponse(body []byte) (LootResponse, error) {
	r := packet.NewReader(body)
	l := LootResponse{GUID: r.GUID(), Type: r.U8()}
	if l.Type == lootResponseFail {
		l.Refused, l.Err = true, r.U8()
		return l, done(r, "SMSG_LOOT_RESPONSE")
	}
	l.Money = r.U32()
	n := int(r.U8())
	for i := 0; i < n && r.Err() == nil; i++ {
		it := LootItem{Slot: r.U8(), Item: r.U32(), Count: r.U32(), DisplayID: r.U32()}
		r.U32() // random suffix, always 0
		it.RandomProp, it.SlotType = r.U32(), r.U8()
		l.Items = append(l.Items, it)
	}
	return l, done(r, "SMSG_LOOT_RESPONSE")
}

// ParseLootRelease is SMSG_LOOT_RELEASE_RESPONSE: the guid of the loot that closed and a
// u8 that is 1 (Player.cpp:9270-9276).
func ParseLootRelease(body []byte) (packet.GUID, error) {
	r := packet.NewReader(body)
	g := r.GUID()
	r.U8()
	return g, done(r, "SMSG_LOOT_RELEASE_RESPONSE")
}

// ParseLootRemoved is SMSG_LOOT_REMOVED: the u8 slot taken (Player.cpp:9699-9704).
func ParseLootRemoved(body []byte) (uint8, error) {
	r := packet.NewReader(body)
	s := r.U8()
	return s, done(r, "SMSG_LOOT_REMOVED")
}

// ParseLootMoneyNotify is SMSG_LOOT_MONEY_NOTIFY: the u32 copper this player's share came
// to when the money was split in a group (LootHandler.cpp:382-386).
func ParseLootMoneyNotify(body []byte) (uint32, error) {
	r := packet.NewReader(body)
	m := r.U32()
	return m, done(r, "SMSG_LOOT_MONEY_NOTIFY")
}
