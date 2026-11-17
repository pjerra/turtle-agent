package game_test

import (
	"bytes"
	"strings"
	"testing"

	"github.com/TortoiseBots/turtle-agent/internal/game"
	"github.com/TortoiseBots/turtle-agent/internal/game/gametest"
	"github.com/TortoiseBots/turtle-agent/internal/packet"
)

// HandleMessagechatOpcode reads type, lang, then the channel name for CHAT_MSG_CHANNEL and
// the message (ChatHandler.cpp:176-183, :264-307 at 187af78).
func TestBuildAddonMessage(t *testing.T) {
	b, err := game.BuildAddonMessage(game.ChatParty, "TBM", "roster", "")
	want := packet.NewWriter().U32(0x01).U32(0xFFFFFFFF).CString("TBM\troster").Bytes()
	if err != nil || !bytes.Equal(b, want) {
		t.Fatalf("party: % X %v", b, err)
	}
	b, err = game.BuildAddonMessage(game.ChatChannel, "LFT", "x", "World")
	want = packet.NewWriter().U32(0x0E).U32(0xFFFFFFFF).CString("World").CString("LFT\tx").Bytes()
	if err != nil || !bytes.Equal(b, want) {
		t.Fatalf("channel: % X %v", b, err)
	}
	// An empty text still sends "prefix\t", as the client's SendAddonMessage does.
	if b, err = game.BuildAddonMessage(game.ChatGuild, "TBM", "", ""); err != nil ||
		!bytes.Equal(b, packet.NewWriter().U32(0x03).U32(0xFFFFFFFF).CString("TBM\t").Bytes()) {
		t.Fatalf("empty text: % X %v", b, err)
	}
	for name, fn := range map[string]func() error{
		"no prefix":     func() error { _, err := game.BuildAddonMessage(game.ChatParty, "", "x", ""); return err },
		"tab in prefix": func() error { _, err := game.BuildAddonMessage(game.ChatParty, "A\tB", "x", ""); return err },
		"nul in text":   func() error { _, err := game.BuildAddonMessage(game.ChatParty, "A", "x\x00y", ""); return err },
		"too long": func() error {
			_, err := game.BuildAddonMessage(game.ChatParty, "TBM", strings.Repeat("x", 252), "")
			return err
		},
		"channel no name": func() error { _, err := game.BuildAddonMessage(game.ChatChannel, "A", "x", ""); return err },
		"say":             func() error { _, err := game.BuildAddonMessage(game.ChatSay, "A", "x", ""); return err },
		"whisper":         func() error { _, err := game.BuildAddonMessage(game.ChatWhisper, "A", "x", "Gorm"); return err },
	} {
		if fn() == nil {
			t.Errorf("%s: accepted", name)
		}
	}
	// 3 + 1 + 251 = 255 bytes is the longest message.
	if _, err := game.BuildAddonMessage(game.ChatParty, "TBM", strings.Repeat("x", 251), ""); err != nil {
		t.Fatal(err)
	}
}

// IsLanguageAllowedForChatType lets LANG_ADDON through on exactly these types
// (ChatHandler.cpp:98-125 at 187af78); a whisper is dropped before it is parsed further.
func TestAddonChannelByName(t *testing.T) {
	for name, want := range map[string]game.ChatType{
		"party": game.ChatParty, "RAID": game.ChatRaid, "guild": game.ChatGuild, "officer": game.ChatOfficer,
		"raid_leader": game.ChatRaidLeader, "raid_warning": game.ChatRaidWarning, "battleground": game.ChatBattleground,
		"battleground_leader": game.ChatBattlegroundLeader, "channel": game.ChatChannel,
	} {
		if got, err := game.AddonChannelByName(name); err != nil || got != want {
			t.Errorf("%s: %v %v", name, got, err)
		}
	}
	_, err := game.AddonChannelByName("whisper")
	if err == nil || !strings.Contains(err.Error(), "drops") {
		t.Fatalf("whisper: %v", err)
	}
	if _, err := game.AddonChannelByName("say"); err == nil {
		t.Fatal("say accepted")
	}
}

func TestParseAddonMessage(t *testing.T) {
	m, err := game.ParseMessageChat(gametest.AddonMessage(0x2A, "TBM", "TBM:ROSTER|7|Gorm|1|removing|1|map:0", 3))
	if err != nil {
		t.Fatal(err)
	}
	if m.Type != game.ChatGuild || m.Lang != game.LangAddon || m.Sender != 0x2A || m.Tag != 3 {
		t.Fatalf("%+v", m)
	}
	// No GM colour on addon text (Chat.cpp:2260), and "|r" in a protocol field survives.
	prefix, text, ok := game.SplitAddon(m.Raw)
	if !ok || prefix != "TBM" || text != "TBM:ROSTER|7|Gorm|1|removing|1|map:0" {
		t.Fatalf("%q %q %v", prefix, text, ok)
	}
	// The core turns say lines holding "MR:" or "TR:" into LANG_ADDON with no tab
	// (ChatHandler.cpp:358-360 at 187af78).
	if p, text, ok := game.SplitAddon("TR:hello"); ok || p != "" || text != "TR:hello" {
		t.Fatalf("no tab: %q %q %v", p, text, ok)
	}
	if p, text, ok := game.SplitAddon("A\tb\tc"); !ok || p != "A" || text != "b\tc" {
		t.Fatalf("two tabs: %q %q %v", p, text, ok)
	}
	m, err = game.ParseMessageChat(gametest.PartyAddonMessage(0x2B, "BigWigs", "sync"))
	if err != nil || m.Type != game.ChatParty || m.Sender != 0x2B || m.Lang != game.LangAddon || m.Raw != "BigWigs\tsync" {
		t.Fatalf("%+v %v", m, err)
	}
}
