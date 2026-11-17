package session

import (
	"bytes"
	"context"
	"testing"
	"time"

	"github.com/TortoiseBots/turtle-agent/internal/events"
	"github.com/TortoiseBots/turtle-agent/internal/game"
	"github.com/TortoiseBots/turtle-agent/internal/game/gametest"
	"github.com/TortoiseBots/turtle-agent/internal/opcode"
	"github.com/TortoiseBots/turtle-agent/internal/packet"
)

// recruiterGUID is a creature of entry 0x1F4A (8010).
const recruiterGUID packet.GUID = 0xF130001F4A000123

var recruiterMenu = []game.GossipOption{{Text: "Hire a companion"}, {Icon: 6, Coded: true, Text: "Enter a code"}}

func TestGossipHelloSelectComplete(t *testing.T) {
	h := start(t, Options{})
	h.enterWorld()
	ctx := context.Background()
	if err := h.s.GossipSelect(ctx, recruiterGUID, 0, ""); err == nil {
		t.Fatal("select without a menu accepted")
	}
	if err := h.s.GossipHello(ctx, recruiterGUID); err != nil {
		t.Fatal(err)
	}
	if p := h.expect(opcode.CMSGGossipHello); !bytes.Equal(p.Body, packet.NewWriter().GUID(recruiterGUID).Bytes()) {
		t.Fatalf("hello % X", p.Body)
	}
	h.send(opcode.SMSGGossipMessage, gametest.GossipMessage(recruiterGUID, 907, recruiterMenu,
		[]game.GossipQuest{{ID: 7, Icon: 2, Level: 10, Title: "Kobold Camp Cleanup"}}))
	if p := h.expect(opcode.CMSGCreatureQuery); packet.NewReader(p.Body).U32() != 0x1F4A {
		t.Fatalf("query % X", p.Body)
	}
	h.send(opcode.SMSGCreatureQueryResponse, creatureResponse(0x1F4A, "Bot Recruiter"))
	for _, bad := range []struct {
		g      packet.GUID
		option uint32
		code   string
	}{{recruiterGUID + 1, 0, ""}, {recruiterGUID, 2, ""}, {recruiterGUID, 0, "1234"}} {
		if err := h.s.GossipSelect(ctx, bad.g, bad.option, bad.code); err == nil {
			t.Errorf("%+v accepted", bad)
		}
	}
	if err := h.s.GossipSelect(ctx, recruiterGUID, 1, ""); err != nil {
		t.Fatal(err)
	}
	// A coded option carries its code string even when empty.
	if p := h.expect(opcode.CMSGGossipSelectOption); !bytes.Equal(p.Body, packet.NewWriter().GUID(recruiterGUID).U32(1).CString("").Bytes()) {
		t.Fatalf("coded select % X", p.Body)
	}
	if err := h.s.GossipSelect(ctx, recruiterGUID, 0, ""); err != nil {
		t.Fatal(err)
	}
	if p := h.expect(opcode.CMSGGossipSelectOption); !bytes.Equal(p.Body, packet.NewWriter().GUID(recruiterGUID).U32(0).Bytes()) {
		t.Fatalf("select % X", p.Body)
	}
	h.send(opcode.SMSGGossipComplete, nil)
	h.logout()
	if err := h.s.GossipSelect(ctx, recruiterGUID, 0, ""); err == nil {
		t.Fatal("select after logout accepted")
	}

	g := ofKind(h.s.Log(), events.KindGossip)
	if len(g) != 1 || g[0].Source != "Bot Recruiter" || g[0].SourceGUID != recruiterGUID || g[0].Data["text_id"] != uint32(907) {
		t.Fatalf("%+v", g)
	}
	opts := g[0].Data["options"].([]map[string]any)
	if len(opts) != 2 || opts[0]["text"] != "Hire a companion" || opts[0]["index"] != uint32(0) || opts[1]["coded"] != true || opts[1]["icon"] != uint8(6) {
		t.Fatalf("options %+v", opts)
	}
	if q := g[0].Data["quests"].([]map[string]any); len(q) != 1 || q[0]["title"] != "Kobold Camp Cleanup" || q[0]["id"] != uint32(7) {
		t.Fatalf("quests %+v", q)
	}
	if c := ofKind(h.s.Log(), events.KindGossipComplete); len(c) != 1 {
		t.Fatalf("complete %+v", c)
	}
}

// SMSG_GOSSIP_COMPLETE closes the menu: a select after it is refused, as the client has
// nothing left to click.
func TestGossipCompleteClosesTheMenu(t *testing.T) {
	h := start(t, Options{})
	h.enterWorld()
	h.send(opcode.SMSGGossipMessage, gametest.GossipMessage(recruiterGUID, 1, recruiterMenu, nil))
	h.expect(opcode.CMSGCreatureQuery)
	h.send(opcode.SMSGCreatureQueryResponse, creatureResponse(0x1F4A, "Bot Recruiter"))
	h.send(opcode.SMSGGossipComplete, nil)
	h.send(opcode.SMSGNotification, []byte("sync\x00")) // handled after the packets above
	if _, ok := waitText(h, "sync", 2*time.Second); !ok {
		t.Fatal("notification not logged")
	}
	if err := h.s.GossipSelect(context.Background(), recruiterGUID, 0, ""); err == nil {
		t.Fatal("select after gossip complete accepted")
	}
	h.logout()
}

// Gossip text the server echoes (an option naming the account) is scrubbed like any other.
func TestGossipOptionsAreRedacted(t *testing.T) {
	h := start(t, Options{Redact: []string{"TESTACC"}})
	h.enterWorld()
	h.send(opcode.SMSGGossipMessage, gametest.GossipMessage(recruiterGUID, 1,
		[]game.GossipOption{{Text: "Account TESTACC"}}, []game.GossipQuest{{ID: 1, Title: "testacc quest"}}))
	h.logout()
	g := ofKind(h.s.Log(), events.KindGossip)
	if len(g) != 1 {
		t.Fatalf("%+v", g)
	}
	if o := g[0].Data["options"].([]map[string]any); o[0]["text"] != "Account <redacted>" {
		t.Fatalf("%+v", o)
	}
	if q := g[0].Data["quests"].([]map[string]any); q[0]["title"] != "<redacted> quest" {
		t.Fatalf("%+v", q)
	}
}

// A teleport, a new map or a new gossip_hello leaves the old menu behind, as in the client:
// a select afterwards is refused instead of clicking a menu the server no longer offers.
func TestGossipMenuClosesOnMoveAndNewHello(t *testing.T) {
	h := start(t, Options{NamesWait: 50 * time.Millisecond})
	h.enterWorld()
	ctx := context.Background()
	open := func() {
		h.t.Helper()
		h.send(opcode.SMSGGossipMessage, gametest.GossipMessage(recruiterGUID, 1, recruiterMenu, nil))
		h.send(opcode.SMSGNotification, []byte("menu\x00"))
		if _, ok := waitText(h, "menu", 2*time.Second); !ok {
			h.t.Fatal("menu not handled")
		}
		if err := h.s.GossipSelect(ctx, recruiterGUID, 0, ""); err != nil {
			h.t.Fatal(err)
		}
		h.expect(opcode.CMSGGossipSelectOption)
	}
	refused := func(why string) {
		h.t.Helper()
		if err := h.s.GossipSelect(ctx, recruiterGUID, 0, ""); err == nil {
			h.t.Fatalf("select after %s accepted", why)
		}
	}

	open()
	w := packet.NewWriter().PackedGUID(me.GUID).U32(7)
	game.WriteMoveInfo(w, game.MoveInfo{X: 1, Y: 2, Z: 3})
	h.send(opcode.MSGMoveTeleportAck, w.Bytes())
	h.expect(opcode.MSGMoveTeleportAck)
	refused("a teleport")

	open()
	h.send(opcode.SMSGNewWorld, packet.NewWriter().U32(1).F32(10).F32(20).F32(30).F32(0).Bytes())
	h.expect(opcode.MSGMoveWorldportAck)
	h.expect(opcode.CMSGSetActiveMover)
	refused("a new map")

	open()
	if err := h.s.GossipHello(ctx, recruiterGUID+1); err != nil {
		t.Fatal(err)
	}
	h.expect(opcode.CMSGGossipHello)
	refused("a new gossip_hello")
	h.logout()
}
