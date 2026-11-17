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

// The module answers its addon with Player::SendAddonMessage: guild-type LANG_ADDON from
// the player himself. Such lines are addon events, never chat.
func TestAddonMessagesAreAddonEvents(t *testing.T) {
	h := start(t, Options{})
	h.enterWorld()
	h.send(opcode.SMSGMessagechat, gametest.AddonMessage(me.GUID, "TBM", "TBM:TRANSPORT|party", 3))
	h.send(opcode.SMSGMessagechat, gametest.PartyAddonMessage(0x99, "BigWigs", "BWVQ"))
	if p := h.expect(opcode.CMSGNameQuery); packet.NewReader(p.Body).GUID() != 0x99 {
		t.Fatalf("query % X", p.Body)
	}
	h.send(opcode.SMSGNameQueryResponse, nameResponse(0x99, "Gorm"))
	h.send(opcode.SMSGMessagechat, gametest.ChatPacket(game.ChatSay, game.LangAddon, 0x99, "", 0, "", "TR:hello", 0))
	h.send(opcode.SMSGMessagechat, gametest.ChatPacket(game.ChatSystem, 0, 0, "", 0, "", "TBM:ROSTER|7|Gorm|1|removing|1|map:0", 0))
	h.logout()
	a := ofKind(h.s.Log(), events.KindAddon)
	if len(a) != 3 {
		t.Fatalf("%+v", a)
	}
	if a[0].Source != "Harnessb" || a[0].Text != "TBM:TRANSPORT|party" || a[0].Data["prefix"] != "TBM" || a[0].Data["type"] != "guild" {
		t.Fatalf("self: %+v", a[0])
	}
	if a[1].Source != "Gorm" || a[1].Text != "BWVQ" || a[1].Data["prefix"] != "BigWigs" || a[1].Data["type"] != "party" {
		t.Fatalf("party: %+v", a[1])
	}
	if a[2].Text != "TR:hello" || a[2].Data["prefix"] != "" || a[2].Data["type"] != "say" {
		t.Fatalf("no tab: %+v", a[2])
	}
	if c := ofKind(h.s.Log(), events.KindChat); len(c) != 0 {
		t.Fatalf("addon lines leaked into chat: %+v", c)
	}
	// StripColors eats "|r" out of "|removing"; the raw line keeps the protocol intact.
	sys := ofKind(h.s.Log(), events.KindSystem)
	if len(sys) != 1 || sys[0].Data["raw"] != "TBM:ROSTER|7|Gorm|1|removing|1|map:0" {
		t.Fatalf("system: %+v", sys)
	}
}

func TestAddonSend(t *testing.T) {
	h := start(t, Options{})
	h.enterWorld()
	ctx := context.Background()
	if err := h.s.AddonSend(ctx, "party", "TBM", "roster", ""); err != nil {
		t.Fatal(err)
	}
	if p := h.expect(opcode.CMSGMessagechat); !bytes.Equal(p.Body, packet.NewWriter().U32(1).U32(game.LangAddon).CString("TBM\troster").Bytes()) {
		t.Fatalf("% X", p.Body)
	}
	if err := h.s.AddonSend(ctx, "channel", "LFT", "x", "World"); err != nil {
		t.Fatal(err)
	}
	if p := h.expect(opcode.CMSGMessagechat); !bytes.Equal(p.Body, packet.NewWriter().U32(0x0E).U32(game.LangAddon).CString("World").CString("LFT\tx").Bytes()) {
		t.Fatalf("% X", p.Body)
	}
	for _, bad := range [][3]string{{"whisper", "TBM", "Gorm"}, {"say", "TBM", ""}, {"party", "", ""}} {
		if err := h.s.AddonSend(ctx, bad[0], bad[1], "x", bad[2]); err == nil {
			t.Errorf("%v accepted", bad)
		}
	}
	h.logout()
	for _, p := range h.srv.Skipped() {
		if p.Op == opcode.CMSGMessagechat {
			t.Fatalf("refused addon message was sent: % X", p.Body)
		}
	}
}
