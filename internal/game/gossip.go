package game

import (
	"fmt"

	"github.com/pjerra/turtle-agent/internal/packet"
)

// NPC gossip. Line numbers cite the core at 187af78, the checkout deployed on the test
// server; NPCHandler.cpp and GossipDef.cpp are the same at 5d79486.

// BuildGossipHello is CMSG_GOSSIP_HELLO: the NPC GUID (NPCHandler.cpp:368-373). The server
// answers nothing when the NPC is out of reach or cannot talk (NPCHandler.cpp:375-380).
func BuildGossipHello(npc packet.GUID) []byte { return packet.NewWriter().GUID(npc).Bytes() }

// BuildGossipSelectOption is CMSG_GOSSIP_SELECT_OPTION: GUID, option index, then the code
// only for an option the menu marks coded (NPCHandler.cpp:405-420).
func BuildGossipSelectOption(npc packet.GUID, option uint32, code string, coded bool) []byte {
	w := packet.NewWriter().GUID(npc).U32(option)
	if coded {
		w.CString(code)
	}
	return w.Bytes()
}

// GossipOption is one menu line (GossipMenuItem, GossipDef.h:143-151).
type GossipOption struct {
	Index uint32 `json:"index"`
	Icon  uint8  `json:"icon"`  // GOSSIP_ICON_* (GossipDef.h:59-83): 0 chat, 1 vendor, 2 taxi, 3 trainer, …
	Coded bool   `json:"coded"` // the client asks for a code before it sends the choice
	Text  string `json:"text"`
}

// GossipQuest is one quest line of the same menu.
type GossipQuest struct {
	ID    uint32 `json:"id"`
	Icon  uint32 `json:"icon"`
	Level uint32 `json:"level"`
	Title string `json:"title"`
}

type GossipMenu struct {
	NPC     packet.GUID
	TextID  uint32 // npc_text id; its text comes from CMSG_NPC_TEXT_QUERY
	Options []GossipOption
	Quests  []GossipQuest
}

// Option returns the option with that index.
func (m GossipMenu) Option(index uint32) (GossipOption, bool) {
	for _, o := range m.Options {
		if o.Index == index {
			return o, true
		}
	}
	return GossipOption{}, false
}

// ParseGossipMessage decodes SMSG_GOSSIP_MESSAGE (PlayerMenu::SendGossipMenu,
// GossipDef.cpp:156-221).
func ParseGossipMessage(body []byte) (GossipMenu, error) {
	r := packet.NewReader(body)
	m := GossipMenu{NPC: r.GUID(), TextID: r.U32()}
	for i, n := uint32(0), r.U32(); i < n && r.Err() == nil; i++ {
		o := GossipOption{Index: r.U32(), Icon: r.U8(), Coded: r.U8() != 0, Text: r.CString()}
		if r.Err() == nil {
			m.Options = append(m.Options, o)
		}
	}
	for i, n := uint32(0), r.U32(); i < n && r.Err() == nil; i++ {
		q := GossipQuest{ID: r.U32(), Icon: r.U32(), Level: r.U32(), Title: r.CString()}
		if r.Err() == nil {
			m.Quests = append(m.Quests, q)
		}
	}
	if err := r.Err(); err != nil {
		return GossipMenu{}, fmt.Errorf("SMSG_GOSSIP_MESSAGE: %w", err)
	}
	return m, nil
}
