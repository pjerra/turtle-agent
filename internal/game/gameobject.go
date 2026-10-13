package game

import (
	"fmt"

	"github.com/pjerra/turtle-agent/internal/packet"
)

// Game object packets. Line numbers cite the core at 5d79486 (tortoise-wow upstream).

// BuildGameObjectQuery is CMSG_GAMEOBJECT_QUERY: entry, then the object's GUID (QueryHandler.cpp:214-219).
func BuildGameObjectQuery(entry uint32, g packet.GUID) []byte {
	return packet.NewWriter().U32(entry).GUID(g).Bytes()
}

// BuildGameObjectUse is CMSG_GAMEOBJ_USE: the object's GUID, in full. The core drops it without an
// answer for an object out of reach, a generic one, one flagged no-interact, or one the player
// cannot use (SpellHandler.cpp:286-318).
func BuildGameObjectUse(g packet.GUID) []byte { return packet.NewWriter().GUID(g).Bytes() }

// GameObjectInfo is the part of SMSG_GAMEOBJECT_QUERY_RESPONSE the agent keeps.
type GameObjectInfo struct {
	Entry     uint32
	Found     bool
	Type      uint32
	DisplayID uint32
	Name      string
	// Data is the template's 24 type-specific words (GameObjectInfo::raw, GameObject.h:41-): for a chest
	// the lock id and loot id, for a door whether it starts open, for a goober its spell and page.
	Data [24]uint32
}

// ParseGameObjectQueryResponse decodes SMSG_GAMEOBJECT_QUERY_RESPONSE: entry (with bit 31 set for
// an unknown entry and nothing after it), type, display id, the name and four more empty names,
// then 24 data words (QueryHandler.cpp:236-259 and :261-268).
func ParseGameObjectQueryResponse(body []byte) (GameObjectInfo, error) {
	r := packet.NewReader(body)
	entry := r.U32()
	if entry&0x80000000 != 0 {
		return GameObjectInfo{Entry: entry &^ 0x80000000}, r.Err()
	}
	g := GameObjectInfo{Entry: entry, Found: true}
	g.Type, g.DisplayID = r.U32(), r.U32()
	g.Name = r.CString()
	r.CString()
	r.CString()
	r.CString()
	r.CString()
	for i := range g.Data {
		g.Data[i] = r.U32()
	}
	if err := r.Err(); err != nil {
		return GameObjectInfo{}, fmt.Errorf("SMSG_GAMEOBJECT_QUERY_RESPONSE: %w", err)
	}
	return g, nil
}

// GameObjectAnim is the body of SMSG_GAMEOBJECT_CUSTOM_ANIM (GUID, animation id), and of
// SMSG_GAMEOBJECT_RESET_STATE, _SPAWN_ANIM and _DESPAWN_ANIM (GUID only; Anim is 0)
// (GameObject.cpp:2478-2491, Object.cpp:1992-2005).
type GameObjectAnim struct {
	GUID packet.GUID
	Anim uint32
}

// ParseGameObjectAnim decodes the packets above; custom is true for SMSG_GAMEOBJECT_CUSTOM_ANIM,
// which carries the animation id after the GUID.
func ParseGameObjectAnim(body []byte, custom bool) (GameObjectAnim, error) {
	r := packet.NewReader(body)
	a := GameObjectAnim{GUID: r.GUID()}
	if custom {
		a.Anim = r.U32()
	}
	if err := r.Err(); err != nil {
		return GameObjectAnim{}, fmt.Errorf("game object animation: %w", err)
	}
	return a, nil
}
