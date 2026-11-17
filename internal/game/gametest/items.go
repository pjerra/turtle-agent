package gametest

import "github.com/TortoiseBots/turtle-agent/internal/packet"

// Core cites in this file are tortoise-wow upstream/1181dev 5784fb0.

// InventoryChangeFailure is Player::SendEquipError (Player.cpp:13561-13579): u8 result,
// and for a failure the required level (EQUIP_ERR_CANT_EQUIP_LEVEL_I only), both item
// guids and a zero bag subclass byte.
func InventoryChangeFailure(result uint8, requiredLevel uint32, item1, item2 packet.GUID) []byte {
	w := packet.NewWriter().U8(result)
	if result == 0 {
		return w.Bytes()
	}
	if result == 1 {
		w.U32(requiredLevel)
	}
	return w.GUID(item1).GUID(item2).U8(0).Bytes()
}

// ResultCode is the u32 of SMSG_MOUNTRESULT and SMSG_DISMOUNTRESULT (Player.cpp:20213-20226).
func ResultCode(v uint32) []byte { return packet.NewWriter().U32(v).Bytes() }
