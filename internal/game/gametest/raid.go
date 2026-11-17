package gametest

import "github.com/TortoiseBots/turtle-agent/internal/packet"

// Core cites in this file are tortoise-wow upstream/1181dev 5784fb0.

// RaidTargetDelta is Group::SetTargetIcon's broadcast (Group.cpp:1296-1314): u8 0 (delta),
// u8 icon, u64 guid; a zero guid clears the icon.
func RaidTargetDelta(icon uint8, g packet.GUID) []byte {
	return packet.NewWriter().U8(0).U8(icon).GUID(g).Bytes()
}

// RaidTargetList is Group::SendTargetIconList (Group.cpp:1364-1381) and the list
// Group::SendUpdate resends after a non-raid group list (Group.cpp:1383-1448): u8 1 (full
// list), then u8 icon + u64 guid for each set icon in icon order.
func RaidTargetList(icons [8]packet.GUID) []byte {
	w := packet.NewWriter().U8(1)
	for i, g := range icons {
		if g == 0 {
			continue
		}
		w.U8(uint8(i)).GUID(g)
	}
	return w.Bytes()
}
