package game

import (
	"fmt"

	"github.com/TortoiseBots/turtle-agent/internal/packet"
)

// BuildGroupInvite is CMSG_GROUP_INVITE: the name (GroupHandler.cpp:56-59).
func BuildGroupInvite(name string) []byte { return packet.NewWriter().CString(name).Bytes() }

// BuildRequestPartyMemberStats is CMSG_REQUEST_PARTY_MEMBER_STATS (GroupHandler.cpp:823-827).
func BuildRequestPartyMemberStats(g packet.GUID) []byte { return packet.NewWriter().GUID(g).Bytes() }

// Member status flags (Group/Group.h:87-95).
const (
	MemberOnline uint8 = 0x01
	MemberDead   uint8 = 0x04
	MemberGhost  uint8 = 0x08
)

// Group update flags (Group/Group.h:108-132).
const (
	GroupUpdateStatus           uint32 = 0x00000001
	GroupUpdateCurHP            uint32 = 0x00000002
	GroupUpdateMaxHP            uint32 = 0x00000004
	GroupUpdatePowerType        uint32 = 0x00000008
	GroupUpdateCurPower         uint32 = 0x00000010
	GroupUpdateMaxPower         uint32 = 0x00000020
	GroupUpdateLevel            uint32 = 0x00000040
	GroupUpdateZone             uint32 = 0x00000080
	GroupUpdatePosition         uint32 = 0x00000100
	GroupUpdateAuras            uint32 = 0x00000200
	GroupUpdateAurasNegative    uint32 = 0x00000400
	GroupUpdatePetGUID          uint32 = 0x00000800
	GroupUpdatePetName          uint32 = 0x00001000
	GroupUpdatePetModelID       uint32 = 0x00002000
	GroupUpdatePetCurHP         uint32 = 0x00004000
	GroupUpdatePetMaxHP         uint32 = 0x00008000
	GroupUpdatePetPowerType     uint32 = 0x00010000
	GroupUpdatePetCurPower      uint32 = 0x00020000
	GroupUpdatePetMaxPower      uint32 = 0x00040000
	GroupUpdatePetAuras         uint32 = 0x00080000
	GroupUpdatePetAurasNegative uint32 = 0x00100000
	GroupUpdateFull             uint32 = 0x001FFFFF
)

type PartyResult struct {
	Op     uint32 // PartyOperation: 0 invite, 2 leave (WorldSession.h:86-90)
	Member string
	Result uint32 // PartyResult (WorldSession.h:92-102)
}

// Text renders the result the way the client's strings do (WorldSession.h:94-102).
func (p PartyResult) Text() string {
	switch p.Result {
	case 0:
		return "OK"
	case 1:
		return fmt.Sprintf("Cannot find '%s'.", p.Member)
	case 2:
		return fmt.Sprintf("%s is not in your party.", p.Member)
	case 3:
		return "Your party is full."
	case 4:
		return fmt.Sprintf("%s is already in a group.", p.Member)
	case 5:
		return "You aren't in a party."
	case 6:
		return "You are not the party leader."
	case 7:
		return fmt.Sprintf("%s is of the wrong faction.", p.Member)
	case 8:
		return fmt.Sprintf("%s is ignoring you.", p.Member)
	}
	return fmt.Sprintf("party result %d for %s", p.Result, p.Member)
}

// ParsePartyCommandResult decodes SMSG_PARTY_COMMAND_RESULT (GroupHandler.cpp:46-54).
func ParsePartyCommandResult(body []byte) (PartyResult, error) {
	r := packet.NewReader(body)
	res := PartyResult{Op: r.U32(), Member: r.CString(), Result: r.U32()}
	if err := r.Err(); err != nil {
		return PartyResult{}, fmt.Errorf("SMSG_PARTY_COMMAND_RESULT: %w", err)
	}
	return res, nil
}

type GroupMember struct {
	Name   string
	GUID   packet.GUID
	Status uint8 // Member* flags
	Flags  uint8 // subgroup | 0x80 assistant
}

type GroupList struct {
	Type          uint8
	OwnFlags      uint8
	Members       []GroupMember // every member except the receiving player (Group.cpp:1411-1412)
	Leader        packet.GUID
	LootMethod    uint8
	Looter        packet.GUID
	LootThreshold uint8
	Extra         int // bytes after the fields read: the difficulty byte, or the empty list's zeros
}

// ParseGroupList decodes Group::SendUpdate (Group.cpp:1402-1432).
func ParseGroupList(body []byte) (GroupList, error) {
	r := packet.NewReader(body)
	var g GroupList
	g.Type, g.OwnFlags = r.U8(), r.U8()
	n := r.U32()
	if n > 40 {
		return GroupList{}, fmt.Errorf("SMSG_GROUP_LIST: %d members", n)
	}
	for i := uint32(0); i < n; i++ {
		var m GroupMember
		m.Name, m.GUID, m.Status, m.Flags = r.CString(), r.GUID(), r.U8(), r.U8()
		g.Members = append(g.Members, m)
	}
	g.Leader = r.GUID()
	if n > 0 {
		g.LootMethod, g.Looter, g.LootThreshold = r.U8(), r.GUID(), r.U8()
	}
	if err := r.Err(); err != nil {
		return GroupList{}, fmt.Errorf("SMSG_GROUP_LIST: %w", err)
	}
	g.Extra = r.Remaining()
	return g, nil
}

type MemberStats struct {
	GUID            packet.GUID
	Mask            uint32
	Status          uint8
	HP, MaxHP       uint16
	PowerType       uint8
	Power, MaxPower uint16
	Level, Zone     uint16
	X, Y            uint16
	Auras           []uint16 // spell ids, positive slots first
	PetGUID         packet.GUID
	PetName         string
	PetHP, PetMaxHP uint16
}

// ParsePartyMemberStats decodes BuildPartyMemberStatsPacket (GroupHandler.cpp:657-801),
// reading every flagged field in order so the pet fields stay aligned.
func ParsePartyMemberStats(body []byte) (MemberStats, error) {
	r := packet.NewReader(body)
	var m MemberStats
	m.GUID, m.Mask = r.PackedGUID(), r.U32()
	f := m.Mask
	if f&GroupUpdateStatus != 0 {
		m.Status = r.U8()
	}
	if f&GroupUpdateCurHP != 0 {
		m.HP = r.U16()
	}
	if f&GroupUpdateMaxHP != 0 {
		m.MaxHP = r.U16()
	}
	if f&GroupUpdatePowerType != 0 {
		m.PowerType = r.U8()
	}
	if f&GroupUpdateCurPower != 0 {
		m.Power = r.U16()
	}
	if f&GroupUpdateMaxPower != 0 {
		m.MaxPower = r.U16()
	}
	if f&GroupUpdateLevel != 0 {
		m.Level = r.U16()
	}
	if f&GroupUpdateZone != 0 {
		m.Zone = r.U16()
	}
	if f&GroupUpdatePosition != 0 {
		m.X, m.Y = r.U16(), r.U16()
	}
	if f&GroupUpdateAuras != 0 {
		mask := r.U32()
		for i := 0; i < 32; i++ {
			if mask&(1<<i) != 0 {
				m.Auras = append(m.Auras, r.U16())
			}
		}
	}
	if f&GroupUpdateAurasNegative != 0 {
		mask := r.U16() // aura mask >> 32: slots 32-47 (GroupHandler.cpp:700-708)
		for i := 0; i < 16; i++ {
			if mask&(1<<i) != 0 {
				m.Auras = append(m.Auras, r.U16())
			}
		}
	}
	if f&GroupUpdatePetGUID != 0 {
		m.PetGUID = r.GUID()
	}
	if f&GroupUpdatePetName != 0 {
		m.PetName = r.CString()
	}
	if f&GroupUpdatePetModelID != 0 {
		r.U16()
	}
	if f&GroupUpdatePetCurHP != 0 {
		m.PetHP = r.U16()
	}
	if f&GroupUpdatePetMaxHP != 0 {
		m.PetMaxHP = r.U16()
	}
	if f&GroupUpdatePetPowerType != 0 {
		r.U8()
	}
	if f&GroupUpdatePetCurPower != 0 {
		r.U16()
	}
	if f&GroupUpdatePetMaxPower != 0 {
		r.U16()
	}
	if f&GroupUpdatePetAuras != 0 {
		mask := r.U32()
		for i := 0; i < 32; i++ {
			if mask&(1<<i) != 0 {
				r.U16()
			}
		}
	}
	if f&GroupUpdatePetAurasNegative != 0 {
		mask := r.U16()
		for i := 0; i < 16; i++ {
			if mask&(1<<i) != 0 {
				r.U16()
			}
		}
	}
	if err := r.Err(); err != nil {
		return MemberStats{}, fmt.Errorf("party member stats: %w", err)
	}
	return m, nil
}
