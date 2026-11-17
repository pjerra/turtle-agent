package game

import (
	"fmt"
	"strings"

	"github.com/TortoiseBots/turtle-agent/internal/packet"
)

// Core cites in this file are tortoise-wow upstream/1181dev 5784fb0.

// Group types (GroupType, Group.h:100-104) and the assistant bit of SMSG_GROUP_LIST's own
// flags and member flags (Group.cpp:1414, :1427).
const (
	GroupParty     uint8 = 0
	GroupRaid      uint8 = 1
	GroupAssistant uint8 = 0x80
)

// RaidIconCount is TARGET_ICON_COUNT (Group.h:49).
const RaidIconCount = 8

// raidIcons is the icon order the 1.12 client and the bots share: SetRaidTarget index
// minus one on the wire, and RtiTargetValue::GetRtiIndex in TortoiseBots (RtiTargetValue.h:20-31).
var raidIcons = [RaidIconCount]string{"star", "circle", "diamond", "triangle", "moon", "square", "cross", "skull"}

// RaidIconByName maps an icon name (case-insensitive) to its wire index.
func RaidIconByName(name string) (uint8, bool) {
	for i, n := range raidIcons {
		if strings.EqualFold(n, name) {
			return uint8(i), true
		}
	}
	return 0, false
}

// RaidIconName is the icon's name, "" past the last icon.
func RaidIconName(icon uint8) string {
	if icon >= RaidIconCount {
		return ""
	}
	return raidIcons[icon]
}

// raidTargetRequest asks the server for the full icon list (GroupHandler.cpp:491-492).
const raidTargetRequest = 0xFF

// BuildRaidTargetUpdate is the client's MSG_RAID_TARGET_UPDATE that sets icon on g, or
// clears the icon when g is 0: u8 icon, u64 guid (GroupHandler.cpp:476-502). The server
// takes it from the group leader or an assistant only, and moves an icon already on g
// (Group.cpp:1296-1314).
func BuildRaidTargetUpdate(icon uint8, g packet.GUID) []byte {
	return packet.NewWriter().U8(icon).GUID(g).Bytes()
}

// BuildRaidTargetRequest is MSG_RAID_TARGET_UPDATE asking for the full list (u8 0xFF).
func BuildRaidTargetRequest() []byte { return []byte{raidTargetRequest} }

// RaidMark is one icon slot: GUID 0 means the icon is clear.
type RaidMark struct {
	Icon uint8
	GUID packet.GUID
}

// RaidTargetUpdate is the server's MSG_RAID_TARGET_UPDATE: a delta with one icon, or the
// full list of set icons (Full), which replaces every icon.
type RaidTargetUpdate struct {
	Full  bool
	Marks []RaidMark
}

// ParseRaidTargetUpdate decodes the server's MSG_RAID_TARGET_UPDATE: u8 1 then (u8 icon,
// u64 guid) per set icon (Group.cpp:1364-1381, :1383-1448), or u8 0, u8 icon, u64 guid
// (Group.cpp:1296-1314).
func ParseRaidTargetUpdate(body []byte) (RaidTargetUpdate, error) {
	r := packet.NewReader(body)
	u := RaidTargetUpdate{Full: r.U8() == 1}
	if err := r.Err(); err != nil {
		return RaidTargetUpdate{}, fmt.Errorf("MSG_RAID_TARGET_UPDATE: %w", err)
	}
	// A delta holds one icon; a full list holds as many as follow, none when all are clear.
	for n := 0; (!u.Full && n == 0) || (u.Full && r.Remaining() > 0); n++ {
		m := RaidMark{Icon: r.U8(), GUID: r.GUID()}
		if err := r.Err(); err != nil {
			return RaidTargetUpdate{}, fmt.Errorf("MSG_RAID_TARGET_UPDATE: %w", err)
		}
		if m.Icon >= RaidIconCount {
			return RaidTargetUpdate{}, fmt.Errorf("MSG_RAID_TARGET_UPDATE: icon %d", m.Icon)
		}
		u.Marks = append(u.Marks, m)
	}
	if r.Remaining() != 0 {
		return RaidTargetUpdate{}, fmt.Errorf("MSG_RAID_TARGET_UPDATE: %d bytes unread", r.Remaining())
	}
	return u, nil
}
