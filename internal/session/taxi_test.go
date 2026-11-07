package session

import (
	"bytes"
	"context"
	"strings"
	"testing"
	"time"

	"github.com/pjerra/turtle-agent/internal/events"
	"github.com/pjerra/turtle-agent/internal/game"
	"github.com/pjerra/turtle-agent/internal/opcode"
	"github.com/pjerra/turtle-agent/internal/packet"
)

// flightMasterGUID is a creature of entry 0x8C1.
const flightMasterGUID packet.GUID = 0xF1300008C1000042

// The route's nodes go out as the client sends them: two nodes direct, more as an
// express route. A guid of 0 or a single node is refused with nothing sent.
func TestActivateTaxi(t *testing.T) {
	h := start(t, Options{})
	h.enterWorld()
	ctx := context.Background()
	if err := h.s.ActivateTaxi(ctx, 0, []uint32{2, 6}); err == nil || !strings.Contains(err.Error(), "no flight master guid") {
		t.Fatalf("%v", err)
	}
	if err := h.s.ActivateTaxi(ctx, flightMasterGUID, []uint32{2}); err == nil ||
		!strings.Contains(err.Error(), "at least a source and a destination") {
		t.Fatalf("%v", err)
	}
	if err := h.s.ActivateTaxi(ctx, flightMasterGUID, nil); err == nil {
		t.Fatal("empty route accepted")
	}
	if err := h.s.ActivateTaxi(ctx, flightMasterGUID, []uint32{2, 6}); err != nil {
		t.Fatal(err)
	}
	if p := h.expect(opcode.CMSGActivatetaxi); !bytes.Equal(p.Body, game.BuildActivateTaxi(flightMasterGUID, 2, 6)) {
		t.Fatalf("direct % X", p.Body)
	}
	if err := h.s.ActivateTaxi(ctx, flightMasterGUID, []uint32{2, 5, 6}); err != nil {
		t.Fatal(err)
	}
	if p := h.expect(opcode.CMSGActivatetaxiexpress); !bytes.Equal(p.Body, game.BuildActivateTaxiExpress(flightMasterGUID, []uint32{2, 5, 6})) {
		t.Fatalf("express % X", p.Body)
	}
	h.send(opcode.SMSGActivatetaxireply, packet.NewWriter().U32(0).Bytes())
	e := lastOfKind(t, h.s.Log(), events.KindTaxiReply)
	if e.Data["result"] != uint32(0) || e.Data["reason"] != "ok" {
		t.Fatalf("%+v", e)
	}
	h.send(opcode.SMSGActivatetaxireply, packet.NewWriter().U32(3).Bytes())
	waitFor(t, func() bool { return len(ofKind(h.s.Log(), events.KindTaxiReply)) == 2 })
	if e := ofKind(h.s.Log(), events.KindTaxiReply)[1]; e.Data["result"] != uint32(3) || e.Data["reason"] != "not_enough_money" {
		t.Fatalf("%+v", e)
	}
}

// The guards above send nothing: a refused call leaves no packet on the wire.
func TestActivateTaxiSendsNothingWhenRefused(t *testing.T) {
	h := start(t, Options{})
	h.enterWorld()
	ctx := context.Background()
	if err := h.s.ActivateTaxi(ctx, 0, []uint32{2, 6}); err == nil {
		t.Fatal("zero guid accepted")
	}
	if err := h.s.ActivateTaxi(ctx, flightMasterGUID, []uint32{2}); err == nil {
		t.Fatal("single node accepted")
	}
	deadline := time.Now().Add(200 * time.Millisecond)
	for time.Now().Before(deadline) {
		p, err := h.srv.Next(200 * time.Millisecond)
		if err != nil {
			return
		}
		if p.Op == opcode.CMSGActivatetaxi || p.Op == opcode.CMSGActivatetaxiexpress {
			t.Fatalf("a request went out: % X", p.Body)
		}
	}
}
