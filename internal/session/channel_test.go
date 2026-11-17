package session

import (
	"bytes"
	"context"
	"testing"

	"github.com/TortoiseBots/turtle-agent/internal/events"
	"github.com/TortoiseBots/turtle-agent/internal/game"
	"github.com/TortoiseBots/turtle-agent/internal/game/gametest"
	"github.com/TortoiseBots/turtle-agent/internal/opcode"
	"github.com/TortoiseBots/turtle-agent/internal/packet"
)

func TestJoinChatLeaveChannel(t *testing.T) {
	h := start(t, Options{})
	h.enterWorld()
	ctx := context.Background()
	if err := h.s.JoinChannel(ctx, "tbtest", ""); err != nil {
		t.Fatal(err)
	}
	if p := h.expect(opcode.CMSGJoinChannel); !bytes.Equal(p.Body, packet.NewWriter().CString("tbtest").CString("").Bytes()) {
		t.Fatalf("join % X", p.Body)
	}
	h.send(opcode.SMSGChannelNotify, gametest.ChannelYouJoined("tbtest", 0x01))
	if err := h.s.Chat(ctx, "channel", "hello", "tbtest"); err != nil {
		t.Fatal(err)
	}
	if p := h.expect(opcode.CMSGMessagechat); !bytes.Equal(p.Body, packet.NewWriter().U32(0x0E).U32(0).CString("tbtest").CString("hello").Bytes()) {
		t.Fatalf("chat % X", p.Body)
	}
	// The channel echoes the line to every member, the sender included.
	h.send(opcode.SMSGMessagechat, gametest.ChatPacket(game.ChatChannel, 0, me.GUID, "", 0, "tbtest", "hello", 0))
	h.send(opcode.SMSGChannelNotify, gametest.ChannelPlayer(0x00, "tbtest", 0x99))
	h.expect(opcode.CMSGNameQuery)
	h.send(opcode.SMSGNameQueryResponse, nameResponse(0x99, "Gorm"))
	h.send(opcode.SMSGChannelNotify, gametest.ChannelPlayerBy(0x12, "tbtest", 0x99, me.GUID))
	if err := h.s.LeaveChannel(ctx, "tbtest"); err != nil {
		t.Fatal(err)
	}
	if p := h.expect(opcode.CMSGLeaveChannel); !bytes.Equal(p.Body, packet.NewWriter().CString("tbtest").Bytes()) {
		t.Fatalf("leave % X", p.Body)
	}
	h.send(opcode.SMSGChannelNotify, gametest.ChannelNotice(0x03, "tbtest"))
	h.send(opcode.SMSGChannelNotify, gametest.ChannelName(0x09, "tbtest", "Nobody"))
	h.logout()

	n := ofKind(h.s.Log(), events.KindChannelNotify)
	if len(n) != 5 {
		t.Fatalf("%+v", n)
	}
	if n[0].Text != "Joined Channel: [tbtest]" || n[0].Data["notice"] != "you_joined" || n[0].Data["channel"] != "tbtest" || n[0].Data["flags"] != uint32(1) {
		t.Fatalf("you_joined: %+v", n[0])
	}
	if n[1].Source != "Gorm" || n[1].Data["notice"] != "joined" {
		t.Fatalf("joined: %+v", n[1])
	}
	if n[2].Source != "Harnessb" || n[2].Target != "Gorm" || n[2].Data["notice"] != "player_kicked" {
		t.Fatalf("kicked: %+v", n[2])
	}
	if n[3].Text != "Left Channel: [tbtest]" || n[4].Data["name"] != "Nobody" || n[4].Text != "[tbtest] Player Nobody was not found." {
		t.Fatalf("left/not found: %+v %+v", n[3], n[4])
	}
	c := ofKind(h.s.Log(), events.KindChat)
	if len(c) != 1 || c[0].Data["channel"] != "tbtest" || c[0].Data["type"] != "channel" || c[0].Source != "Harnessb" {
		t.Fatalf("chat: %+v", c)
	}
	for _, bad := range []error{h.s.JoinChannel(ctx, "", ""), h.s.LeaveChannel(ctx, "")} {
		if bad == nil {
			t.Error("empty channel name accepted")
		}
	}
}
