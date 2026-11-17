package game

import (
	"bytes"
	"strings"
	"testing"

	"github.com/TortoiseBots/turtle-agent/internal/packet"
)

// buildChat transcribes ChatHandler::BuildChatPacket (Chat.cpp:2251-2314) for tests.
func buildChat(t ChatType, sender packet.GUID, name string, target packet.GUID, channel, msg string, tag uint8) []byte {
	w := packet.NewWriter().U8(uint8(t)).U32(LangUniversal)
	switch t {
	case ChatMonsterWhisper, ChatRaidBossWhisper, ChatRaidBossEmote, ChatMonsterEmote:
		w.U32(uint32(len(name) + 1)).CString(name).GUID(target)
	case ChatSay, ChatParty, ChatYell:
		w.GUID(sender).GUID(sender)
	case ChatMonsterSay, ChatMonsterYell:
		w.GUID(sender).U32(uint32(len(name) + 1)).CString(name).GUID(target)
	case ChatChannel:
		w.CString(channel).U32(0).GUID(sender)
	default:
		w.GUID(sender)
	}
	if msg == "" {
		return w.Bytes()
	}
	if tag == 3 { // CHAT_TAG_GM (Chat.h:88)
		msg = "|c1049e6ff" + msg + "|r"
	}
	return w.U32(uint32(len(msg) + 1)).CString(msg).U8(tag).Bytes()
}

func TestParseMessageChatEveryBranch(t *testing.T) {
	cases := []struct {
		body []byte
		want ChatMessage
	}{
		{buildChat(ChatSay, 0x2A, "", 0, "", "hello", 0), ChatMessage{Type: ChatSay, Sender: 0x2A, Text: "hello"}},
		{buildChat(ChatWhisper, 0x2B, "", 0, "", "attack!", 0), ChatMessage{Type: ChatWhisper, Sender: 0x2B, Text: "attack!"}},
		{buildChat(ChatSystem, 0, "", 0, "", "TBM:VERSION|2026-10-10-v3", 0), ChatMessage{Type: ChatSystem, Text: "TBM:VERSION|2026-10-10-v3"}},
		{buildChat(ChatMonsterSay, 0xF130000000001234, "Hogger", 0x2A, "", "Grr", 0), ChatMessage{Type: ChatMonsterSay, Sender: 0xF130000000001234, SenderName: "Hogger", Target: 0x2A, Text: "Grr"}},
		{buildChat(ChatMonsterEmote, 0, "Hogger", 0x2A, "", "growls", 0), ChatMessage{Type: ChatMonsterEmote, SenderName: "Hogger", Target: 0x2A, Text: "growls"}},
		{buildChat(ChatChannel, 0x2C, "", 0, "world", "lfg", 0), ChatMessage{Type: ChatChannel, Sender: 0x2C, Channel: "world", Text: "lfg"}},
		{buildChat(ChatHardcore, 0x2D, "", 0, "", "hc", 0), ChatMessage{Type: ChatHardcore, Sender: 0x2D, Text: "hc"}},
		{buildChat(ChatSay, 0x2A, "", 0, "", "gm text", 3), ChatMessage{Type: ChatSay, Sender: 0x2A, Text: "gm text", Tag: 3}},
		{buildChat(ChatSystem, 0, "", 0, "", "", 0), ChatMessage{Type: ChatSystem}},
	}
	for i, c := range cases {
		got, err := ParseMessageChat(c.body)
		if err != nil {
			t.Fatalf("case %d: %v", i, err)
		}
		got.Raw = ""
		if got != c.want {
			t.Errorf("case %d: got %+v want %+v", i, got, c.want)
		}
	}
	if _, err := ParseMessageChat([]byte{0, 0, 0}); err == nil {
		t.Fatal("truncated packet accepted")
	}
}

func TestBuildMessageChat(t *testing.T) {
	b, err := BuildMessageChat(ChatWhisper, "Gorm", "hi")
	want := packet.NewWriter().U32(6).U32(0).CString("Gorm").CString("hi").Bytes()
	if err != nil || !bytes.Equal(b, want) {
		t.Fatalf("% X %v", b, err)
	}
	b, _ = BuildMessageChat(ChatSay, "", ".gm visible on")
	if !bytes.Equal(b, packet.NewWriter().U32(0).U32(0).CString(".gm visible on").Bytes()) {
		t.Fatalf("% X", b)
	}
	for name, fn := range map[string]func() error{
		"empty":       func() error { _, err := BuildMessageChat(ChatSay, "", ""); return err },
		"too long":    func() error { _, err := BuildMessageChat(ChatSay, "", strings.Repeat("x", 256)); return err },
		"whisper, no": func() error { _, err := BuildMessageChat(ChatWhisper, "", "x"); return err },
		"system":      func() error { _, err := BuildMessageChat(ChatSystem, "", "x"); return err },
	} {
		if fn() == nil {
			t.Errorf("%s: accepted", name)
		}
	}
}

func TestChatTypeByName(t *testing.T) {
	if ct, err := ChatTypeByName("Party"); err != nil || ct != ChatParty {
		t.Fatalf("%v %v", ct, err)
	}
	if _, err := ChatTypeByName("system"); err == nil {
		t.Fatal("system is not sendable")
	}
	if ChatHardcore.String() != "hardcore" || ChatType(0x42).String() != "chat_0x42" {
		t.Fatal("names")
	}
}

func TestStripColorsAndLinks(t *testing.T) {
	in := "|cffff0000Red|r and |cff71d5ff|Hspell:133|h[Fireball]|h|r"
	if got := StripColors(in); got != "Red and [Fireball]" {
		t.Fatalf("%q", got)
	}
}
