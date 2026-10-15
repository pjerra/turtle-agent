package main

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/pjerra/turtle-agent/internal/game"
	"github.com/pjerra/turtle-agent/internal/objects"
	"github.com/pjerra/turtle-agent/internal/objects/objtest"
	"github.com/pjerra/turtle-agent/internal/opcode"
	"github.com/pjerra/turtle-agent/internal/packet"
	"github.com/pjerra/turtle-agent/internal/session"
)

// selfBlock is the agent's own create block (guid 0x2A) with the selection the server shows, as the core writes it.
func selfBlock(target packet.GUID) []byte {
	w := objtest.Header(1)
	w.U8(uint8(objects.BlockCreate2)).PackedGUID(0x2A).U8(objects.TypePlayer)
	objtest.Living(w, 0x20, game.MoveInfo{X: 1, Y: 2, Z: 3}, nil)
	objtest.Values(w, 192, map[uint16]uint32{objects.UnitHealth: 800, objects.UnitMaxHealth: 800,
		objects.UnitTarget: uint32(target), objects.UnitTarget + 1: uint32(target >> 32)})
	return w.Bytes()
}

func selfValues(target packet.GUID) []byte {
	w := objtest.Header(1).U8(uint8(objects.BlockValues)).PackedGUID(0x2A)
	objtest.Values(w, 192, map[uint16]uint32{objects.UnitTarget: uint32(target), objects.UnitTarget + 1: uint32(target >> 32)})
	return w.Bytes()
}

// chats are the chat lines the server received within d.
func chats(r *agentRig, d time.Duration) []string {
	var out []string
	for {
		p, err := r.srv.Next(d)
		if err != nil {
			return out
		}
		if p.Op == opcode.CMSGMessagechat {
			out = append(out, string(p.Body))
		}
	}
}

// A line that acts on the selection goes out only once the server shows that selection. A selection that never lands, or a stale selection
// of another player that the server still shows, sends nothing (the old fixed wait would have sent the line, and it would have run on the
// other player: `.instance groupunbind all` disbands the group of whoever is selected).
func TestGMLineWaitsForTheServerToShowTheSelection(t *testing.T) {
	old := gmSelectConfirmWait
	gmSelectConfirmWait = 300 * time.Millisecond
	defer func() { gmSelectConfirmWait = old }()
	const lead, stranger = packet.GUID(0x23A), packet.GUID(0x4D2)

	// The server shows a stranger as selected and never changes it.
	r := newRig(t)
	r.srv.Send(opcode.SMSGUpdateObject, selfBlock(stranger))
	g := remoteGame{c: &commanderService{cur: func() *session.Session { return r.s }}}
	err := g.selectThenGM(context.Background(), lead, ".instance groupunbind all")
	if err == nil || !strings.Contains(err.Error(), "did not show") {
		t.Fatalf("%v", err)
	}
	if c := chats(r, 400*time.Millisecond); len(c) != 0 {
		t.Fatalf("a line went out without a confirmed selection: %q", c)
	}

	// The selection lands late (after 150 ms): the line goes out after it.
	r = newRig(t)
	r.srv.Send(opcode.SMSGUpdateObject, selfBlock(stranger))
	g = remoteGame{c: &commanderService{cur: func() *session.Session { return r.s }}}
	go func() {
		time.Sleep(150 * time.Millisecond)
		r.srv.Send(opcode.SMSGUpdateObject, selfValues(lead))
	}()
	if err := g.selectThenGM(context.Background(), lead, ".instance groupunbind all"); err != nil {
		t.Fatal(err)
	}
	if c := chats(r, 400*time.Millisecond); len(c) != 1 || !strings.Contains(c[0], ".instance groupunbind all") {
		t.Fatalf("chat lines %q", c)
	}
}
