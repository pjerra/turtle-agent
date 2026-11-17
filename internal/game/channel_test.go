package game_test

import (
	"bytes"
	"testing"

	"github.com/TortoiseBots/turtle-agent/internal/game"
	"github.com/TortoiseBots/turtle-agent/internal/game/gametest"
	"github.com/TortoiseBots/turtle-agent/internal/packet"
)

// HandleJoinChannelOpcode reads the name, then the password (ChannelHandler.cpp:30-49);
// HandleLeaveChannelOpcode reads the name only (ChannelHandler.cpp:74-90).
func TestBuildJoinAndLeaveChannel(t *testing.T) {
	b, err := game.BuildJoinChannel("tbtest", "")
	if err != nil || !bytes.Equal(b, packet.NewWriter().CString("tbtest").CString("").Bytes()) {
		t.Fatalf("join: % X %v", b, err)
	}
	b, err = game.BuildJoinChannel("tbtest", "pw")
	if err != nil || !bytes.Equal(b, packet.NewWriter().CString("tbtest").CString("pw").Bytes()) {
		t.Fatalf("join pw: % X %v", b, err)
	}
	b, err = game.BuildLeaveChannel("tbtest")
	if err != nil || !bytes.Equal(b, packet.NewWriter().CString("tbtest").Bytes()) {
		t.Fatalf("leave: % X %v", b, err)
	}
	for name, fn := range map[string]func() error{
		"join empty":  func() error { _, err := game.BuildJoinChannel("", ""); return err },
		"leave empty": func() error { _, err := game.BuildLeaveChannel(""); return err },
		"join nul":    func() error { _, err := game.BuildJoinChannel("a\x00b", ""); return err },
	} {
		if fn() == nil {
			t.Errorf("%s: accepted", name)
		}
	}
}

// CHAT_MSG_CHANNEL reads the channel name before the message (ChatHandler.cpp:264-281 at 187af78).
func TestBuildChannelChat(t *testing.T) {
	if ct, err := game.ChatTypeByName("channel"); err != nil || ct != game.ChatChannel {
		t.Fatalf("%v %v", ct, err)
	}
	b, err := game.BuildMessageChat(game.ChatChannel, "tbtest", "hello")
	if err != nil || !bytes.Equal(b, packet.NewWriter().U32(0x0E).U32(0).CString("tbtest").CString("hello").Bytes()) {
		t.Fatalf("% X %v", b, err)
	}
	if _, err := game.BuildMessageChat(game.ChatChannel, "", "hello"); err == nil {
		t.Fatal("channel chat without a channel name accepted")
	}
}

func TestParseChannelNotify(t *testing.T) {
	cases := []struct {
		body []byte
		want game.ChannelNotify
	}{
		{gametest.ChannelYouJoined("tbtest", 0x01), game.ChannelNotify{Notice: game.ChannelYouJoinedNotice, Channel: "tbtest", Flags: 0x01}},
		{gametest.ChannelNotice(0x03, "tbtest"), game.ChannelNotify{Notice: game.ChannelYouLeftNotice, Channel: "tbtest"}},
		{gametest.ChannelPlayer(0x00, "tbtest", 0x99), game.ChannelNotify{Notice: game.ChannelJoinedNotice, Channel: "tbtest", Player: 0x99}},
		{gametest.ChannelPlayer(0x01, "tbtest", 0x99), game.ChannelNotify{Notice: game.ChannelLeftNotice, Channel: "tbtest", Player: 0x99}},
		{gametest.ChannelNotice(0x05, "nope"), game.ChannelNotify{Notice: game.ChannelNotMemberNotice, Channel: "nope"}},
		{gametest.ChannelName(0x09, "tbtest", "Nobody"), game.ChannelNotify{Notice: 0x09, Channel: "tbtest", Name: "Nobody"}},
		{gametest.ChannelName(0x0B, "tbtest", "Gorm"), game.ChannelNotify{Notice: 0x0B, Channel: "tbtest", Name: "Gorm"}},
		{gametest.ChannelModeChange("tbtest", 0x99, 0, 2), game.ChannelNotify{Notice: 0x0C, Channel: "tbtest", Player: 0x99, NewFlags: 2}},
		{gametest.ChannelPlayer(0x0F, "tbtest", 0x99), game.ChannelNotify{Notice: 0x0F, Channel: "tbtest", Player: 0x99}},
		{gametest.ChannelPlayerBy(0x12, "tbtest", 0x2A, 0x99), game.ChannelNotify{Notice: 0x12, Channel: "tbtest", Player: 0x2A, Source: 0x99}},
		{gametest.ChannelPlayerBy(0x15, "tbtest", 0x2A, 0x99), game.ChannelNotify{Notice: 0x15, Channel: "tbtest", Player: 0x2A, Source: 0x99}},
		{gametest.ChannelPlayer(0x17, "tbtest", 0x99), game.ChannelNotify{Notice: 0x17, Channel: "tbtest", Player: 0x99}},
		{gametest.ChannelPlayer(0x18, "tbtest", 0x99), game.ChannelNotify{Notice: 0x18, Channel: "tbtest", Player: 0x99}},
		{gametest.ChannelNotice(0x1B, "1bad"), game.ChannelNotify{Notice: game.ChannelInvalidNameNotice, Channel: "1bad"}},
		{gametest.ChannelName(0x1E, "tbtest", "Gorm"), game.ChannelNotify{Notice: 0x1E, Channel: "tbtest", Name: "Gorm"}},
		{gametest.ChannelNotice(0x1F, "World"), game.ChannelNotify{Notice: 0x1F, Channel: "World"}},
		// An unknown notice keeps its header and skips the rest.
		{append(gametest.ChannelNotice(0x40, "tbtest"), 1, 2, 3), game.ChannelNotify{Notice: 0x40, Channel: "tbtest"}},
	}
	for i, c := range cases {
		got, err := game.ParseChannelNotify(c.body)
		if err != nil || got != c.want {
			t.Errorf("case %d: got %+v %v, want %+v", i, got, err, c.want)
		}
	}
	if _, err := game.ParseChannelNotify(gametest.ChannelNotice(0x00, "tbtest")); err == nil {
		t.Fatal("joined notice without its GUID accepted")
	}
	if game.ChannelYouJoinedNotice.String() != "you_joined" || game.ChannelNotice(0x12).String() != "player_kicked" ||
		game.ChannelNotice(0x40).String() != "notice_0x40" {
		t.Fatal("names")
	}
}

// Text is the client's line for notices that need no player name resolved.
func TestChannelNotifyText(t *testing.T) {
	for _, c := range []struct {
		n    game.ChannelNotify
		want string
	}{
		{game.ChannelNotify{Notice: game.ChannelYouJoinedNotice, Channel: "tbtest"}, "Joined Channel: [tbtest]"},
		{game.ChannelNotify{Notice: game.ChannelYouLeftNotice, Channel: "tbtest"}, "Left Channel: [tbtest]"},
		{game.ChannelNotify{Notice: game.ChannelNotMemberNotice, Channel: "x"}, "Not on channel x."},
		{game.ChannelNotify{Notice: 0x09, Channel: "x", Name: "Nobody"}, "[x] Player Nobody was not found."},
		{game.ChannelNotify{Notice: game.ChannelInvalidNameNotice, Channel: "1bad"}, "Invalid channel name"},
		{game.ChannelNotify{Notice: game.ChannelJoinedNotice, Channel: "x", Player: 0x99}, ""},
	} {
		if got := c.n.Text(); got != c.want {
			t.Errorf("%v: %q, want %q", c.n.Notice, got, c.want)
		}
	}
}
