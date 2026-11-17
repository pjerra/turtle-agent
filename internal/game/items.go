package game

import (
	"fmt"

	"github.com/TortoiseBots/turtle-agent/internal/packet"
)

// Core cites in this file are tortoise-wow upstream/1181dev 5784fb0.

// BuildAutoEquipItem is CMSG_AUTOEQUIP_ITEM: u8 bag, u8 slot of the item to equip, as a
// right-click in the bags sends it (ItemHandler.cpp:159-163). The server picks the
// equipment slot and swaps out what is there.
func BuildAutoEquipItem(bag, slot uint8) []byte { return []byte{bag, slot} }

// EquipErrCantEquipLevel is EQUIP_ERR_CANT_EQUIP_LEVEL_I (Item.h:48), the one result
// that carries the required level.
const EquipErrCantEquipLevel uint8 = 1

// InventoryChangeFailure is SMSG_INVENTORY_CHANGE_FAILURE: an InventoryResult (Item.h:45)
// and the items involved (Player.cpp:13561-13579). Result 0 is success, sent without more.
type InventoryChangeFailure struct {
	Result        uint8
	RequiredLevel uint32
	Item, Item2   packet.GUID
}

func ParseInventoryChangeFailure(body []byte) (InventoryChangeFailure, error) {
	r := packet.NewReader(body)
	f := InventoryChangeFailure{Result: r.U8()}
	if f.Result != 0 {
		if f.Result == EquipErrCantEquipLevel {
			f.RequiredLevel = r.U32()
		}
		f.Item, f.Item2 = r.GUID(), r.GUID()
		r.U8() // bag subclass
	}
	return f, done(r, "SMSG_INVENTORY_CHANGE_FAILURE")
}

// BuildUseItem is CMSG_USE_ITEM: u8 bag, u8 slot, u8 spell count (read and ignored,
// SpellHandler.cpp:37-42), then the spell's targets as SpellCastTargets::read takes them
// (Spell.cpp:180-228): a u16 mask and, for a unit, its packed guid. A zero target uses the
// self mask, which the core fills with the caster (Spell.cpp:182-190).
func BuildUseItem(bag, slot uint8, target packet.GUID) []byte {
	w := packet.NewWriter().U8(bag).U8(slot).U8(0)
	if target == 0 {
		return w.U16(TargetFlagSelf).Bytes()
	}
	return w.U16(TargetFlagUnit).PackedGUID(target).Bytes()
}

// BuildCancelAura is CMSG_CANCEL_AURA: u32 spell id of the aura to remove
// (SpellHandler.cpp:428-431). Dismounting a mount aura is this packet; 1.12 has no
// CMSG_CANCEL_MOUNT_AURA.
func BuildCancelAura(spell uint32) []byte { return packet.NewWriter().U32(spell).Bytes() }

// MountResultName names the u32 of SMSG_MOUNTRESULT (UnitMountResult, UnitDefines.h:579-592).
func MountResultName(v uint32) string {
	switch v {
	case 0:
		return "invalid_mountee"
	case 1:
		return "too_far_away"
	case 2:
		return "already_mounted"
	case 3:
		return "not_mountable"
	case 4:
		return "not_your_pet"
	case 5:
		return "other"
	case 6:
		return "looting"
	case 7:
		return "race_cant_mount"
	case 8:
		return "shapeshifted"
	case 9:
		return "forced_dismount"
	case 10:
		return "ok"
	}
	return fmt.Sprintf("unknown(%d)", v)
}

// DismountResultName names the u32 of SMSG_DISMOUNTRESULT (UnitDismountResult,
// UnitDefines.h:594-600).
func DismountResultName(v uint32) string {
	switch v {
	case 0:
		return "no_pet"
	case 1:
		return "not_mounted"
	case 2:
		return "not_your_pet"
	case 3:
		return "ok"
	}
	return fmt.Sprintf("unknown(%d)", v)
}

// ParseResultCode reads the one u32 of SMSG_MOUNTRESULT and SMSG_DISMOUNTRESULT
// (Player.cpp:20213-20226).
func ParseResultCode(body []byte, name string) (uint32, error) {
	r := packet.NewReader(body)
	v := r.U32()
	return v, done(r, name)
}

// StandStateName names the stand state, byte 0 of UNIT_FIELD_BYTES_1 (UnitDefines.h:74-85).
func StandStateName(v uint32) string {
	switch v {
	case 0:
		return "stand"
	case 1:
		return "sit"
	case 2, 4, 5, 6:
		return "sit_chair"
	case 3:
		return "sleep"
	case 7:
		return "dead"
	case 8:
		return "kneel"
	}
	return fmt.Sprintf("unknown(%d)", v)
}
