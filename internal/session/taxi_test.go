package session

import (
	"bytes"
	"context"
	"errors"
	"net"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/pjerra/turtle-agent/internal/events"
	"github.com/pjerra/turtle-agent/internal/game"
	"github.com/pjerra/turtle-agent/internal/opcode"
	"github.com/pjerra/turtle-agent/internal/packet"
	"github.com/pjerra/turtle-agent/internal/worldconn"
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
	h.send(opcode.SMSGActivatetaxireply, packet.NewWriter().U32(0).Bytes())
	e := lastOfKind(t, h.s.Log(), events.KindTaxiReply)
	if e.Data["result"] != uint32(0) || e.Data["reason"] != "ok" {
		t.Fatalf("%+v", e)
	}
	if err := h.s.ActivateTaxi(ctx, flightMasterGUID, []uint32{2, 5, 6}); err != nil {
		t.Fatal(err)
	}
	if p := h.expect(opcode.CMSGActivatetaxiexpress); !bytes.Equal(p.Body, game.BuildActivateTaxiExpress(flightMasterGUID, []uint32{2, 5, 6})) {
		t.Fatalf("express % X", p.Body)
	}
	h.send(opcode.SMSGActivatetaxireply, packet.NewWriter().U32(3).Bytes())
	waitFor(t, func() bool { return len(ofKind(h.s.Log(), events.KindTaxiReply)) == 2 })
	e = ofKind(h.s.Log(), events.KindTaxiReply)[1]
	if e.Data["result"] != uint32(3) || e.Data["reason"] != "not_enough_money" {
		t.Fatalf("%+v", e)
	}
}

// A short reply body is a parse error, not an event.
func TestActivateTaxiReplyShortBodyEmitsNoEvent(t *testing.T) {
	h := start(t, Options{})
	h.enterWorld()
	ctx := context.Background()
	if err := h.s.ActivateTaxi(ctx, flightMasterGUID, []uint32{2, 6}); err != nil {
		t.Fatal(err)
	}
	h.expect(opcode.CMSGActivatetaxi)
	h.send(opcode.SMSGActivatetaxireply, []byte{0x03, 0x00})
	waitFor(t, func() bool { return len(ofKind(h.s.Log(), events.KindParseError)) > 0 })
	if n := len(ofKind(h.s.Log(), events.KindTaxiReply)); n != 0 {
		t.Fatalf("%d taxi_reply events", n)
	}
}

// A taxi call outside the world fails naming the state, like any other send.
func TestActivateTaxiBeforeWorld(t *testing.T) {
	h := start(t, Options{})
	h.expect(opcode.CMSGCharEnum)
	if err := h.s.ActivateTaxi(context.Background(), flightMasterGUID, []uint32{2, 6}); !errors.Is(err, ErrNotInWorld) {
		t.Fatalf("err = %v", err)
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

// A second request while the first waits for its reply is refused with nothing sent;
// the reply frees the session for the next one.
func TestActivateTaxiWaitsForReply(t *testing.T) {
	h := start(t, Options{})
	h.enterWorld()
	ctx := context.Background()
	if err := h.s.ActivateTaxi(ctx, flightMasterGUID, []uint32{2, 6}); err != nil {
		t.Fatal(err)
	}
	h.expect(opcode.CMSGActivatetaxi)
	if err := h.s.ActivateTaxi(ctx, flightMasterGUID, []uint32{2, 6}); err == nil ||
		!strings.Contains(err.Error(), "taxi: previous request still unanswered") {
		t.Fatalf("%v", err)
	}
	drainTaxi(t, h)
	h.send(opcode.SMSGActivatetaxireply, packet.NewWriter().U32(0).Bytes())
	lastOfKind(t, h.s.Log(), events.KindTaxiReply)
	if err := h.s.ActivateTaxi(ctx, flightMasterGUID, []uint32{2, 6}); err != nil {
		t.Fatal(err)
	}
	h.expect(opcode.CMSGActivatetaxi)
}

// Some refusals get no reply, so the 10 s window is what frees the session: pinned to the
// session's clock, not the wall's.
func TestActivateTaxiTimeoutFreesTheSession(t *testing.T) {
	h := start(t, Options{})
	h.enterWorld()
	clock := &taxiClock{at: h.s.now()}
	ctx := context.Background()
	// The session's clock is read on the owner loop (and by the spell state), so the
	// test swaps it on the owner loop too: a plain assignment here would race Run.
	if _, err := h.s.do(ctx, func() (any, error) { h.s.clockFn = clock.now; return nil, nil }); err != nil {
		t.Fatal(err)
	}
	if err := h.s.ActivateTaxi(ctx, flightMasterGUID, []uint32{2, 6}); err != nil {
		t.Fatal(err)
	}
	h.expect(opcode.CMSGActivatetaxi)
	if err := h.s.ActivateTaxi(ctx, flightMasterGUID, []uint32{2, 6}); err == nil ||
		err.Error() != "taxi: previous request still unanswered (retry after 10 s)" {
		t.Fatalf("%v", err)
	}
	drainTaxi(t, h)
	clock.advance(taxiWait)
	if err := h.s.ActivateTaxi(ctx, flightMasterGUID, []uint32{2, 6}); err != nil {
		t.Fatal(err)
	}
	h.expect(opcode.CMSGActivatetaxi)
}

// taxiClock is a session clock the test moves by hand; the owner loop reads it too, so a
// lock guards the time.
type taxiClock struct {
	mu sync.Mutex
	at time.Time
}

func (c *taxiClock) now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.at
}

func (c *taxiClock) advance(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.at = c.at.Add(d)
}

// Two calls at once: the owner loop serializes them, so exactly one sends.
func TestActivateTaxiConcurrentSendsOnce(t *testing.T) {
	h := start(t, Options{})
	h.enterWorld()
	ctx := context.Background()
	res := make(chan error, 2)
	for range 2 {
		go func() { res <- h.s.ActivateTaxi(ctx, flightMasterGUID, []uint32{2, 6}) }()
	}
	var errs, oks int
	for range 2 {
		if err := <-res; err == nil {
			oks++
		} else if strings.Contains(err.Error(), "taxi: previous request still unanswered") {
			errs++
		} else {
			t.Fatalf("%v", err)
		}
	}
	if oks != 1 || errs != 1 {
		t.Fatalf("oks %d errs %d", oks, errs)
	}
	h.expect(opcode.CMSGActivatetaxi)
	drainTaxi(t, h)
}

// More than maxTaxiNodes nodes are refused before anything is built or sent, so the
// refusal sets no window: a valid call right after still sends.
func TestActivateTaxiRejectsTooManyNodes(t *testing.T) {
	h := start(t, Options{})
	h.enterWorld()
	ctx := context.Background()
	nodes := make([]uint32, maxTaxiNodes+1)
	for i := range nodes {
		nodes[i] = uint32(i + 2)
	}
	if err := h.s.ActivateTaxi(ctx, flightMasterGUID, nodes); err == nil ||
		!strings.Contains(err.Error(), "taxi: too many nodes (max 64)") {
		t.Fatalf("%v", err)
	}
	drainTaxi(t, h)
	if err := h.s.ActivateTaxi(ctx, flightMasterGUID, []uint32{2, 6}); err != nil {
		t.Fatal(err)
	}
	h.expect(opcode.CMSGActivatetaxi)
}

// A request the socket refuses to take sets no window: the next call fails on the dead
// socket, not as "still unanswered". (A failed write closes the conn, so no follow-up
// call can send; what matters is the refusal is the write error, not the window.)
func TestActivateTaxiWriteFailureSetsNoWindow(t *testing.T) {
	var bw *brokenWrites
	h := startWrapped(t, Options{}, testKey, testKey, func(c net.Conn) net.Conn {
		bw = &brokenWrites{Conn: c}
		return bw
	})
	h.enterWorld()
	ctx := context.Background()
	bw.broken.Store(true)
	if err := h.s.ActivateTaxi(ctx, flightMasterGUID, []uint32{2, 6}); !errors.Is(err, worldconn.ErrWrite) {
		t.Fatalf("%v", err)
	}
	if err := h.s.ActivateTaxi(ctx, flightMasterGUID, []uint32{2, 6}); err == nil ||
		strings.Contains(err.Error(), "still unanswered") {
		t.Fatalf("%v", err)
	}
}

// drainTaxi reads packets until 200 ms pass with nothing new, so a check for "nothing
// sent" does not trip on a request the session already queued.
func drainTaxi(t *testing.T, h *harness) {
	t.Helper()
	for {
		p, err := h.srv.Next(200 * time.Millisecond)
		if err != nil {
			return
		}
		if p.Op == opcode.CMSGActivatetaxi || p.Op == opcode.CMSGActivatetaxiexpress {
			t.Fatalf("a second request went out: % X", p.Body)
		}
	}
}
