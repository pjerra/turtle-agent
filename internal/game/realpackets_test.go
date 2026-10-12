package game_test

import (
	"encoding/hex"

	"github.com/pjerra/turtle-agent/internal/opcode"
)

// Server packet bodies copied byte for byte from live captures of a TortoiseBots server
// (Tortoise WoW 1.18.1) that `turtle-agent login --capture` wrote on the m910q test server, read
// back with the capture reader. They come from the run directories under ~/TortoiseBots-audit
// (t2, gate-b17, ta23, ta-16-17, 139, t1) and from agent2.cap in its scratchpad-backup folder.
// #N is the record index in that capture. They are literal so that the parsers are tested
// against what the real core sends, not what gametest writes. Checked before committing: a
// capture holding exactly these packets passes `turtle-agent scrub-check` against every
// account in the test settings files, and none of the packets carries a player name (the only
// text is NPC gossip and a vendor's goods).
//
// No capture holds a trainer list, a quest panel, an SMSG_LOOT_RESPONSE window or a force
// speed change (the runs never opened those), so those parsers are still tested only against
// gametest packets; see seeds_test.go.

// agent2.cap #1392: a class-choice menu of eight options
var realGossipClasses = mustHex("17093D2F730130F1FFFFFF000800000000000000000057617272696F72000100" +
	"0000000050616C6164696E0002000000000048756E7465720003000000000052" +
	"6F67756500040000000000507269657374000500000000004D61676500060000" +
	"0000005761726C6F636B0007000000000044727569640000000000")

// agent2.cap #1428: a submenu whose last option is "< Back"
var realGossipRaces = mustHex("17093D2F730130F1FFFFFF000500000000000000000048756D616E0001000000" +
	"00004477617266000200000000004E6967687420456C66000300000000004869" +
	"676820456C66000400000007003C204261636B0000000000")

// agent2.cap #1458: an innkeeper, a bind option and a vendor option
var realGossipInnkeeper = mustHex("190200971A0030F139030000020000000000000005004D616B65207468697320" +
	"696E6E20796F757220686F6D652E00010000000100492077616E7420746F2062" +
	"726F77736520796F757220676F6F64732E0001000000FBEA0000050000002700" +
	"0000536F75746820536561732044656C696361637900")

// agent2.cap #1489: the innkeeper's vendor window
var realVendorList = mustHex("190200971A0030F10C010000001303000079600000FFFFFFFF19000000000000" +
	"000500000002000000F01100007E600000FFFFFFFF1400000000000000050000" +
	"0003000000F111000086600000FFFFFFFFF40100000000000005000000040000" +
	"00F2110000D7120000FFFFFFFFE8030000000000000500000005000000305400" +
	"00CB120000FFFFFFFFD0070000000000000500000006000000FD2200008E6000" +
	"00FFFFFFFFA00F00000000000005000000070000009F000000A4460000FFFFFF" +
	"FF190000000000000005000000080000009B040000AA460000FFFFFFFF7D0000" +
	"00000000000500000009000000B50400009E460000FFFFFFFFF4010000000000" +
	"00050000000A000000AC060000C2460000FFFFFFFFE803000000000000050000" +
	"000B0000006D0600008C460000FFFFFFFFD007000000000000050000000C0000" +
	"003E220000D00B0100FFFFFFFFA00F00000000000005000000")

// t2: full list holding one icon (7 skull)
var realRaidFullSkull = mustHex("0107030A00EB0F0030F1")

// t2: delta setting icon 4 (moon)
var realRaidSetMoon = mustHex("0004150A0019060030F1")

// t2: delta clearing icon 4
var realRaidClearMoon = mustHex("00040000000000000000")

// gate-b17: full list holding icon 4 (moon)
var realRaidFullMoon = mustHex("0104F58200F4120030F1")

// ta23 player.cap #2838: root order, counter 4
var realForceRoot = mustHex("03080204000000")

// ta23 player.cap #1783
var realKnockBack1 = mustHex("030802010000000B3C59BFBD71073F00002041000020C1")

// ta23 player.cap #2154
var realKnockBack2 = mustHex("03080203000000B9FC7FBFABD223BC00002041000020C1")

// gate-b17: map 70
var realNewWorldOutland = mustHex("46000000CDCC62C3295C4442B81E38C285EBB13F")

// ta-16-17: map 1
var realNewWorldKalimdor = mustHex("0100000000E0A0C49AB914C56666B742D00F4940")

// 139 run0: map 0
var realNewWorldEK = mustHex("0000000000CC5DC60080D44300006041E6CB9640")

// gate-b17: map 70
var realTransferPending = mustHex("46000000")

// t1 #36778: 57 copper
var realLootMoney = mustHex("39000000")

func mustHex(s string) []byte {
	b, err := hex.DecodeString(s)
	if err != nil {
		panic(err)
	}
	return b
}

// realPackets is the lot by opcode, seeding the fuzz corpora.
var realPackets = map[uint16][][]byte{
	opcode.SMSGGossipMessage:   {realGossipClasses, realGossipRaces, realGossipInnkeeper},
	opcode.SMSGListInventory:   {realVendorList},
	opcode.MSGRaidTargetUpdate: {realRaidFullSkull, realRaidSetMoon, realRaidClearMoon, realRaidFullMoon},
	opcode.SMSGForceMoveRoot:   {realForceRoot},
	opcode.SMSGMoveKnockBack:   {realKnockBack1, realKnockBack2},
	opcode.SMSGNewWorld:        {realNewWorldOutland, realNewWorldKalimdor, realNewWorldEK},
	opcode.SMSGTransferPending: {realTransferPending},
	opcode.SMSGLootMoneyNotify: {realLootMoney},
}
