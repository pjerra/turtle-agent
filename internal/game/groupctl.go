package game

import (
	"strconv"

	"github.com/pjerra/turtle-agent/internal/packet"
)

// Core cites in this file are tortoise-wow upstream/1181dev 5784fb0; the 1.12 client
// sends the leader and assistant commands with a full u64 guid, not a packed one.

// Raid layout (Group.h:46-48): eight subgroups of five.
const (
	GroupSize     = 5
	RaidSubgroups = 8
	subgroupMask  = 0x7F // SMSG_GROUP_LIST flags: the subgroup, with GroupAssistant on top
)

// FlagsSubgroup is the 0-based subgroup in a SMSG_GROUP_LIST flags byte (Group.cpp:1414, :1427).
func FlagsSubgroup(flags uint8) uint8 { return flags & subgroupMask }

// BuildGroupSetLeader is CMSG_GROUP_SET_LEADER: u64 guid of the member to promote
// (GroupHandler.cpp:334-352). The server answers with SMSG_GROUP_SET_LEADER and a new list.
func BuildGroupSetLeader(g packet.GUID) []byte { return packet.NewWriter().GUID(g).Bytes() }

// BuildGroupAssistant is CMSG_GROUP_ASSISTANT_LEADER: u64 guid, u8 flag (non-zero sets the
// raid assistant bit, 0 clears it). The server takes it from the leader of a raid only
// (GroupHandler.cpp:598-616, Group.h:287).
func BuildGroupAssistant(g packet.GUID, on bool) []byte {
	w := packet.NewWriter().GUID(g)
	if on {
		return w.U8(1).Bytes()
	}
	return w.U8(0).Bytes()
}

// BuildGroupRaidConvert is CMSG_GROUP_RAID_CONVERT, which has no body; the leader of a group
// with at least two members converts it (GroupHandler.cpp:505-522).
func BuildGroupRaidConvert() []byte { return nil }

// BuildGroupChangeSubGroup is CMSG_GROUP_CHANGE_SUB_GROUP: the member's name, then the
// 0-based target subgroup (0-7). The leader or an assistant of a raid may send it; the
// server ignores it when the target subgroup holds five already (GroupHandler.cpp:524-557).
func BuildGroupChangeSubGroup(name string, subgroup uint8) []byte {
	return packet.NewWriter().CString(name).U8(subgroup).Bytes()
}

// BuildGroupSwapSubGroup is CMSG_GROUP_SWAP_SUB_GROUP: the names of two members, who trade
// subgroups (GroupHandler.cpp:559-596). It works when the target subgroup is full.
func BuildGroupSwapSubGroup(name, with string) []byte {
	return packet.NewWriter().CString(name).CString(with).Bytes()
}

// BuildGroupUninviteGUID is CMSG_GROUP_UNINVITE_GUID: u64 guid of the member to remove
// (GroupHandler.cpp:249-285). The leader or an assistant may send it, never for the
// leader; removing a member from a group of two disbands it (Group.cpp, Group::RemoveMember).
func BuildGroupUninviteGUID(g packet.GUID) []byte { return packet.NewWriter().GUID(g).Bytes() }

// Loot methods of CMSG_LOOT_METHOD and SMSG_GROUP_LIST (Group.h:51-58).
const (
	LootFreeForAll      uint8 = 0
	LootRoundRobin      uint8 = 1
	LootMaster          uint8 = 2
	LootGroup           uint8 = 3
	LootNeedBeforeGreed uint8 = 4
)

var lootMethodNames = [...]string{"free_for_all", "round_robin", "master_loot", "group_loot", "need_before_greed"}

// LootMethodName is the RPC name of a loot method ("group_loot"); an unknown value is "method_<n>".
func LootMethodName(m uint8) string {
	if int(m) < len(lootMethodNames) {
		return lootMethodNames[m]
	}
	return "method_" + strconv.Itoa(int(m))
}

// LootMethodByName is the loot method an RPC name stands for.
func LootMethodByName(name string) (uint8, bool) {
	for i, n := range lootMethodNames {
		if n == name {
			return uint8(i), true
		}
	}
	return 0, false
}

// LootMethodNames lists the names LootMethodByName takes.
func LootMethodNames() []string { return append([]string{}, lootMethodNames[:]...) }

// BuildLootMethod is CMSG_LOOT_METHOD: u32 method, u64 master looter, u32 threshold (an item quality, 1 common to 5
// legendary). Only the group leader may send it; the server drops anything else, and a method above 4 or a threshold
// outside 1-5, without a reply, and answers a change with a new SMSG_GROUP_LIST (GroupHandler.cpp:372-403).
func BuildLootMethod(method uint8, master packet.GUID, threshold uint8) []byte {
	return packet.NewWriter().U32(uint32(method)).GUID(master).U32(uint32(threshold)).Bytes()
}
