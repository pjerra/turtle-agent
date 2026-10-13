// Package gametest builds server packet bodies for tests in game and its callers.
package gametest

import (
	"github.com/pjerra/turtle-agent/internal/game"
	"github.com/pjerra/turtle-agent/internal/packet"
)

// GroupList transcribes Group::SendUpdate (Group.cpp:1412-1443 on upstream/1181dev 5784fb0)
// for the receiving player of a party, in subgroup 0 and not an assistant.
func GroupList(leader packet.GUID, members ...game.GroupMember) []byte {
	return GroupListAs(game.GroupParty, 0, leader, members...)
}

// GroupListAs is GroupList with the group type (GROUPTYPE_NORMAL 0, GROUPTYPE_RAID 1,
// Group.h:100-104) and the receiver's own flags, subgroup | 0x80 when an assistant
// (Group.cpp:1414).
func GroupListAs(groupType, ownFlags uint8, leader packet.GUID, members ...game.GroupMember) []byte {
	w := packet.NewWriter().U8(groupType).U8(ownFlags).U32(uint32(len(members)))
	for _, m := range members {
		w.CString(m.Name).GUID(m.GUID).U8(m.Status).U8(m.Flags)
	}
	w.GUID(leader)
	if len(members) > 0 {
		w.U8(3).U64(0).U8(2).U8(0) // group loot, no looter, uncommon threshold, difficulty byte
	}
	return w.Bytes()
}

// GroupListLoot is GroupList with the group's loot method, master looter and threshold.
func GroupListLoot(leader packet.GUID, method uint8, looter packet.GUID, threshold uint8, members ...game.GroupMember) []byte {
	w := packet.NewWriter().U8(game.GroupParty).U8(0).U32(uint32(len(members)))
	for _, m := range members {
		w.CString(m.Name).GUID(m.GUID).U8(m.Status).U8(m.Flags)
	}
	w.GUID(leader)
	if len(members) > 0 {
		w.U8(method).U64(uint64(looter)).U8(threshold).U8(0)
	}
	return w.Bytes()
}
