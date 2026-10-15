package game

import (
	"fmt"

	"github.com/pjerra/turtle-agent/internal/packet"
)

// BuildNameQuery is CMSG_NAME_QUERY (QueryHandler.cpp:121-125).
func BuildNameQuery(g packet.GUID) []byte { return packet.NewWriter().GUID(g).Bytes() }

type NameInfo struct {
	GUID                packet.GUID
	Name                string
	Race, Gender, Class uint32
}

// ParseNameQueryResponse decodes SMSG_NAME_QUERY_RESPONSE (QueryHandler.cpp:45-52).
func ParseNameQueryResponse(body []byte) (NameInfo, error) {
	r := packet.NewReader(body)
	var n NameInfo
	n.GUID, n.Name = r.GUID(), r.CString()
	r.CString() // realm name, empty
	n.Race, n.Gender, n.Class = r.U32(), r.U32(), r.U32()
	if err := r.Err(); err != nil {
		return NameInfo{}, fmt.Errorf("SMSG_NAME_QUERY_RESPONSE: %w", err)
	}
	return n, nil
}

// BuildCreatureQuery is CMSG_CREATURE_QUERY (QueryHandler.cpp:141-147).
func BuildCreatureQuery(entry uint32, g packet.GUID) []byte {
	return packet.NewWriter().U32(entry).GUID(g).Bytes()
}

type CreatureInfo struct {
	Entry   uint32
	Found   bool
	Name    string
	SubName string
	Rank    uint32
}

// ParseCreatureQueryResponse decodes SMSG_CREATURE_QUERY_RESPONSE (QueryHandler.cpp:186-209).
func ParseCreatureQueryResponse(body []byte) (CreatureInfo, error) {
	r := packet.NewReader(body)
	entry := r.U32()
	if entry&0x80000000 != 0 {
		return CreatureInfo{Entry: entry &^ 0x80000000}, r.Err()
	}
	c := CreatureInfo{Entry: entry, Found: true}
	c.Name = r.CString()
	r.CString()
	r.CString()
	r.CString()
	c.SubName = r.CString()
	r.U32() // type flags
	r.U32() // type
	r.U32() // family
	c.Rank = r.U32()
	if err := r.Err(); err != nil {
		return CreatureInfo{}, fmt.Errorf("SMSG_CREATURE_QUERY_RESPONSE: %w", err)
	}
	return c, nil
}

// BuildItemQuery is CMSG_ITEM_QUERY_SINGLE: the item entry and a u64 guid the core skips (ItemHandler.cpp:337-342); a client sends 0
// for an item it only knows by entry.
func BuildItemQuery(entry uint32) []byte { return packet.NewWriter().U32(entry).U64(0).Bytes() }

// ItemInfo is what SMSG_ITEM_QUERY_SINGLE_RESPONSE says of an item prototype that matters to a gear check.
type ItemInfo struct {
	Entry         uint32
	Found         bool
	Name          string
	Class         uint32
	SubClass      uint32
	Quality       uint32 // 0 poor, 1 common, 2 uncommon (green), 3 rare (blue), 4 epic, 5 legendary
	InventoryType uint32
	ItemLevel     uint32
	RequiredLevel uint32
}

// ParseItemQueryResponse decodes SMSG_ITEM_QUERY_SINGLE_RESPONSE up to the required level (ItemHandler.cpp:370-398). An unknown entry comes as
// the entry with bit 31 set and nothing else (:488); a core only answers for an item the account has seen unless it is a GM's.
func ParseItemQueryResponse(body []byte) (ItemInfo, error) {
	r := packet.NewReader(body)
	entry := r.U32()
	if entry&0x80000000 != 0 {
		return ItemInfo{Entry: entry &^ 0x80000000}, r.Err()
	}
	it := ItemInfo{Entry: entry, Found: true}
	it.Class, it.SubClass = r.U32(), r.U32()
	it.Name = r.CString()
	r.CString()
	r.CString()
	r.CString()
	r.U32() // display id
	it.Quality = r.U32()
	r.U32() // flags
	r.U32() // buy price
	r.U32() // sell price
	it.InventoryType = r.U32()
	r.U32() // allowable class
	r.U32() // allowable race
	it.ItemLevel = r.U32()
	it.RequiredLevel = r.U32()
	if err := r.Err(); err != nil {
		return ItemInfo{}, fmt.Errorf("SMSG_ITEM_QUERY_SINGLE_RESPONSE: %w", err)
	}
	return it, nil
}
