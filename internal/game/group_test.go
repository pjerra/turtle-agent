package game_test

import (
	"bytes"
	"testing"

	"github.com/TortoiseBots/turtle-agent/internal/game"
	"github.com/TortoiseBots/turtle-agent/internal/game/gametest"
	"github.com/TortoiseBots/turtle-agent/internal/packet"
)

func TestGroupInviteAndResult(t *testing.T) {
	if !bytes.Equal(game.BuildGroupInvite("Gorm"), []byte("Gorm\x00")) {
		t.Fatal("invite body")
	}
	res, err := game.ParsePartyCommandResult(packet.NewWriter().U32(0).CString("Gorm").U32(4).Bytes())
	if err != nil || res.Result != 4 || res.Text() != "Gorm is already in a group." {
		t.Fatalf("%+v %q %v", res, res.Text(), err)
	}
}

func TestGroupListTwoMembers(t *testing.T) {
	body := gametest.GroupList(0x2A, game.GroupMember{Name: "Gorm", GUID: 0x30, Status: game.MemberOnline}, game.GroupMember{Name: "Lia", GUID: 0x31, Status: game.MemberOnline | game.MemberDead})
	g, err := game.ParseGroupList(body)
	if err != nil || len(g.Members) != 2 || g.Members[1].Name != "Lia" || g.Leader != 0x2A || g.LootThreshold != 2 || g.Extra != 1 {
		t.Fatalf("%+v %v", g, err)
	}
}

// Review Focus 3: bytes past the fields we read (difficulty, or the 24-byte empty list) are tolerated.
func TestGroupListTrailingBytes(t *testing.T) {
	g, err := game.ParseGroupList(make([]byte, 24)) // Group.cpp:493-494
	if err != nil || len(g.Members) != 0 || g.Extra != 10 {
		t.Fatalf("%+v %v", g, err)
	}
	g, err = game.ParseGroupList(append(gametest.GroupList(0x2A, game.GroupMember{Name: "Gorm", GUID: 0x30, Status: 1}), 0xEE, 0xEE))
	if err != nil || g.Extra != 3 {
		t.Fatalf("%+v %v", g, err)
	}
	if _, err := game.ParseGroupList([]byte{0, 0, 5, 0, 0, 0}); err == nil {
		t.Fatal("truncated list accepted")
	}
}

// memberStatsBody transcribes BuildPartyMemberStatsPacket (GroupHandler.cpp:657-801), no pet.
func memberStatsBody(g packet.GUID, mask uint32) []byte {
	w := packet.NewWriter().PackedGUID(g).U32(mask)
	if mask&game.GroupUpdateStatus != 0 {
		w.U8(game.MemberOnline)
	}
	if mask&game.GroupUpdateCurHP != 0 {
		w.U16(500)
	}
	if mask&game.GroupUpdateMaxHP != 0 {
		w.U16(600)
	}
	if mask&game.GroupUpdatePowerType != 0 {
		w.U8(0)
	}
	if mask&game.GroupUpdateCurPower != 0 {
		w.U16(200)
	}
	if mask&game.GroupUpdateMaxPower != 0 {
		w.U16(300)
	}
	if mask&game.GroupUpdateLevel != 0 {
		w.U16(60)
	}
	if mask&game.GroupUpdateZone != 0 {
		w.U16(12)
	}
	if mask&game.GroupUpdatePosition != 0 {
		w.U16(100).U16(200)
	}
	if mask&game.GroupUpdateAuras != 0 {
		w.U32(0b101).U16(1243).U16(1459) // slots 0 and 2
	}
	if mask&game.GroupUpdateAurasNegative != 0 {
		w.U16(0b1).U16(589) // slot 32
	}
	if mask&game.GroupUpdatePetGUID != 0 {
		w.U64(0)
	}
	if mask&game.GroupUpdatePetName != 0 {
		w.U8(0)
	}
	for _, f := range []uint32{game.GroupUpdatePetModelID, game.GroupUpdatePetCurHP, game.GroupUpdatePetMaxHP} {
		if mask&f != 0 {
			w.U16(0)
		}
	}
	if mask&game.GroupUpdatePetPowerType != 0 {
		w.U8(0)
	}
	for _, f := range []uint32{game.GroupUpdatePetCurPower, game.GroupUpdatePetMaxPower} {
		if mask&f != 0 {
			w.U16(0)
		}
	}
	if mask&game.GroupUpdatePetAuras != 0 {
		w.U32(0)
	}
	if mask&game.GroupUpdatePetAurasNegative != 0 {
		w.U16(0)
	}
	return w.Bytes()
}

func TestPartyMemberStatsFullAndPartial(t *testing.T) {
	m, err := game.ParsePartyMemberStats(memberStatsBody(0x30, game.GroupUpdateFull))
	if err != nil || m.GUID != 0x30 || m.Status != game.MemberOnline || m.HP != 500 || m.MaxHP != 600 || m.Power != 200 ||
		m.Level != 60 || len(m.Auras) != 3 || m.Auras[2] != 589 {
		t.Fatalf("%+v %v", m, err)
	}
	m, err = game.ParsePartyMemberStats(memberStatsBody(0x30, game.GroupUpdateCurHP|game.GroupUpdateLevel))
	if err != nil || m.HP != 500 || m.Level != 60 || m.MaxHP != 0 {
		t.Fatalf("%+v %v", m, err)
	}
	offline := packet.NewWriter().PackedGUID(0x30).U32(game.GroupUpdateStatus).U8(0).Bytes() // GroupHandler.cpp:831-837
	if m, err := game.ParsePartyMemberStats(offline); err != nil || m.Status != 0 {
		t.Fatalf("%+v %v", m, err)
	}
}

// A present pet keeps the pet fields aligned (GroupHandler.cpp:722-791); truncation is an error.
func TestPartyMemberStatsPetAndTruncation(t *testing.T) {
	mask := game.GroupUpdatePetGUID | game.GroupUpdatePetName | game.GroupUpdatePetModelID | game.GroupUpdatePetCurHP | game.GroupUpdatePetMaxHP | game.GroupUpdateLevel
	body := packet.NewWriter().PackedGUID(0x30).U32(mask).
		U16(60).U64(0xF14000001).CString("Rex").U16(7).U16(90).U16(100).Bytes()
	m, err := game.ParsePartyMemberStats(body)
	if err != nil || m.PetName != "Rex" || m.PetHP != 90 || m.PetMaxHP != 100 || m.Level != 60 || m.PetGUID != 0xF14000001 {
		t.Fatalf("%+v %v", m, err)
	}
	if _, err := game.ParsePartyMemberStats(body[:len(body)-1]); err == nil {
		t.Fatal("truncated stats accepted")
	}
}

// The pet's positive and negative aura masks are consumed with one U16 per set bit, so a
// body that ends inside either list is an error (GroupHandler.cpp:780-791).
func TestPartyMemberStatsPetAuraMasks(t *testing.T) {
	mask := game.GroupUpdatePetAuras | game.GroupUpdatePetAurasNegative | game.GroupUpdateLevel
	body := packet.NewWriter().PackedGUID(0x30).U32(mask).U16(60).
		U32(0b11).U16(1243).U16(1459). // pet slots 0 and 1
		U16(0b101).U16(589).U16(702).  // pet slots 32 and 34
		Bytes()
	m, err := game.ParsePartyMemberStats(body)
	if err != nil || m.Level != 60 || len(m.Auras) != 0 {
		t.Fatalf("pet auras leaked into the member's: %+v %v", m, err)
	}
	for _, cut := range []int{1, 2, 3} { // inside the negative list, then the mask
		if _, err := game.ParsePartyMemberStats(body[:len(body)-cut]); err == nil {
			t.Fatalf("body cut by %d accepted", cut)
		}
	}
}

// The control packets carry full u64 guids and 0-based subgroups (GroupHandler.cpp:334-352,
// :524-557, :598-616).
func TestGroupControlPackets(t *testing.T) {
	guid := packet.GUID(0x1122334455667788)
	cases := []struct {
		name string
		got  []byte
		want []byte
	}{
		{"set leader", game.BuildGroupSetLeader(guid), []byte{0x88, 0x77, 0x66, 0x55, 0x44, 0x33, 0x22, 0x11}},
		{"assistant on", game.BuildGroupAssistant(guid, true), []byte{0x88, 0x77, 0x66, 0x55, 0x44, 0x33, 0x22, 0x11, 1}},
		{"assistant off", game.BuildGroupAssistant(guid, false), []byte{0x88, 0x77, 0x66, 0x55, 0x44, 0x33, 0x22, 0x11, 0}},
		{"raid convert", game.BuildGroupRaidConvert(), nil},
		{"change subgroup", game.BuildGroupChangeSubGroup("Gorm", 1), []byte("Gorm\x00\x01")},
		{"swap subgroup", game.BuildGroupSwapSubGroup("Gorm", "Lia"), []byte("Gorm\x00Lia\x00")},
		{"uninvite guid", game.BuildGroupUninviteGUID(0x30), []byte{0x30, 0, 0, 0, 0, 0, 0, 0}},
	}
	for _, c := range cases {
		if !bytes.Equal(c.got, c.want) {
			t.Errorf("%s: % X, want % X", c.name, c.got, c.want)
		}
	}
}

func TestGroupListRaidFlags(t *testing.T) {
	body := gametest.GroupListAs(game.GroupRaid, game.GroupAssistant|2, 0x2A,
		game.GroupMember{Name: "Gorm", GUID: 0x30, Status: game.MemberOnline, Flags: 1},
		game.GroupMember{Name: "Lia", GUID: 0x31, Status: game.MemberOnline, Flags: game.GroupAssistant | 7})
	g, err := game.ParseGroupList(body)
	if err != nil || g.Type != game.GroupRaid || g.OwnFlags != 0x82 || len(g.Members) != 2 {
		t.Fatalf("%+v %v", g, err)
	}
	if game.FlagsSubgroup(g.OwnFlags) != 2 || game.FlagsSubgroup(g.Members[0].Flags) != 1 ||
		game.FlagsSubgroup(g.Members[1].Flags) != 7 || g.Members[1].Flags&game.GroupAssistant == 0 {
		t.Fatalf("flags %+v", g)
	}
}

// A hand-written SMSG_GROUP_LIST of a raid, not built with the test helper: type 1, own flags
// 0x82 (assistant, subgroup 2), two members, leader, group loot, no looter, threshold 2,
// difficulty byte (Group.cpp, Group::SendUpdate).
func TestGroupListRaidBytes(t *testing.T) {
	body := []byte{
		0x01, 0x82, // raid; own flags
		0x02, 0x00, 0x00, 0x00, // two other members
		'G', 'o', 'r', 'm', 0, 0x30, 0, 0, 0, 0, 0, 0, 0, 0x01, 0x01, // name, guid, status online, subgroup 1
		'L', 'i', 'a', 0, 0x31, 0, 0, 0, 0, 0, 0, 0, 0x01, 0x87, // assistant in subgroup 8
		0x2A, 0, 0, 0, 0, 0, 0, 0, // leader
		0x03, 0, 0, 0, 0, 0, 0, 0, 0, 0x02, 0x00, // loot method, looter, threshold, difficulty
	}
	g, err := game.ParseGroupList(body)
	if err != nil || g.Type != game.GroupRaid || g.OwnFlags != 0x82 || g.Leader != 0x2A || g.LootThreshold != 2 || g.Extra != 1 {
		t.Fatalf("%+v %v", g, err)
	}
	if len(g.Members) != 2 || g.Members[0].Name != "Gorm" || g.Members[0].GUID != 0x30 || g.Members[0].Flags != 0x01 ||
		g.Members[1].Name != "Lia" || g.Members[1].GUID != 0x31 || g.Members[1].Flags != 0x87 {
		t.Fatalf("members %+v", g.Members)
	}
}
