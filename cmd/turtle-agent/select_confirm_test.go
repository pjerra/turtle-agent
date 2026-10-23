package main

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
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

// The core handles a selection on the map thread (it lands after a delay and its echo comes back later) and a GM chat line on the world
// thread (at once): this server models that, applying each CMSG_SET_SELECTION 12 ms after it arrived (less than the agent's 25 ms poll of the echo, so a selection can land between the confirmation and the line), in order, and recording the selection IN
// FORCE at the moment each chat line arrives. Somebody else keeps selecting a stranger meanwhile (the target RPC, the addon host). Whatever
// the interleaving, a line that goes out must find the server's selection on the unit it was meant for; the old way (a fresh, unconfirmed
// selection sent right before the line) let it run on the stranger.
func TestGMLineRunsOnTheConfirmedSelectionWhenSelectionAndChatAreOnSeparateThreads(t *testing.T) {
	const lead, stranger = packet.GUID(0x23A), packet.GUID(0x4D2)
	r := newRig(t)
	r.srv.Send(opcode.SMSGUpdateObject, selfBlock(0))
	var mu sync.Mutex
	var inForce packet.GUID
	var ranOn []packet.GUID
	mapThread := make(chan packet.GUID, 1000)
	stop := make(chan struct{})
	var wg sync.WaitGroup
	wg.Add(2)
	go func() { // the map thread: selections, in order, each 12 ms after it was read
		defer wg.Done()
		for {
			select {
			case g := <-mapThread:
				time.Sleep(12 * time.Millisecond)
				mu.Lock()
				inForce = g
				mu.Unlock()
				r.srv.Send(opcode.SMSGUpdateObject, selfValues(g)) // the echo the agent waits for
			case <-stop:
				return
			}
		}
	}()
	go func() { // the world thread: reads every packet; a chat line is run at once, on the selection in force then
		defer wg.Done()
		for {
			select {
			case <-stop:
				return
			default:
			}
			p, err := r.srv.Next(50 * time.Millisecond)
			if err != nil {
				continue
			}
			switch p.Op {
			case opcode.CMSGSetSelection:
				mapThread <- packet.NewReader(p.Body).GUID()
			case opcode.CMSGMessagechat:
				mu.Lock()
				ranOn = append(ranOn, inForce)
				mu.Unlock()
			}
		}
	}()
	stopSel := make(chan struct{})
	var swg sync.WaitGroup
	swg.Add(1)
	go func() { // somebody else keeps selecting the stranger
		defer swg.Done()
		for {
			select {
			case <-stopSel:
				return
			case <-time.After(40 * time.Millisecond):
				_, _ = r.s.Target(context.Background(), stranger, "")
			}
		}
	}()
	// And on every other attempt a competitor's selection lands right after the confirmation, and the server has applied it before the line comes.
	attempt := 0
	gmAfterConfirm = func() {
		attempt++
		if attempt%2 == 1 {
			_, _ = r.s.Target(context.Background(), stranger, "")
			waitUntil(t, func() bool { g, _ := r.s.ServerSelection(); return g == stranger })
		}
	}
	defer func() { gmAfterConfirm = nil }()
	g := remoteGame{c: &commanderService{cur: func() *session.Session { return r.s }}}
	sent := 0
	for i := 0; i < 40; i++ {
		if err := g.selectThenGM(context.Background(), lead, ".additem 9240"); err == nil {
			sent++
		}
	}
	close(stopSel)
	swg.Wait()
	time.Sleep(200 * time.Millisecond) // the world thread reads what is still on the wire
	close(stop)
	wg.Wait()
	mu.Lock()
	defer mu.Unlock()
	if len(ranOn) != sent {
		t.Fatalf("%d lines reported sent, the server ran %d", sent, len(ranOn))
	}
	if sent == 0 {
		t.Fatal("no line went out at all")
	}
	for i, sel := range ranOn {
		if sel != lead {
			t.Errorf("line %d ran on %#x, not on the unit it was meant for (%#x)", i, uint64(sel), uint64(lead))
		}
	}
}

// When the selection changes after the server showed the confirmed one, nothing is sent and the unit is selected and confirmed again.
func TestGMLineIsNotSentWhenTheConfirmedSelectionChanged(t *testing.T) {
	const lead, stranger = packet.GUID(0x23A), packet.GUID(0x4D2)
	r := newRig(t)
	r.srv.Send(opcode.SMSGUpdateObject, selfBlock(lead)) // the server shows the lead...
	waitUntil(t, func() bool { g, ok := r.s.ServerSelection(); return ok && g == lead })
	if _, err := r.s.Target(context.Background(), stranger, ""); err != nil { // ...but another request selected the stranger since
		t.Fatal(err)
	}
	if err := r.s.GMOnConfirmed(context.Background(), lead, ".additem 9240"); !errors.Is(err, session.ErrSelectionChanged) {
		t.Fatalf("%v", err)
	}
	if err := r.s.GMOnConfirmed(context.Background(), stranger, ".additem 9240"); !errors.Is(err, session.ErrSelectionChanged) {
		t.Fatalf("believed but not shown by the server: %v", err)
	}
	for _, c := range chats(r, 300*time.Millisecond) {
		t.Fatalf("a line was sent: %q", c)
	}
	// Selected again, but the server has not shown it: it still shows the lead from before the stranger was selected, and that stale echo
	// (sent again) does not confirm the new select of the lead (issue #296).
	if _, err := r.s.Target(context.Background(), lead, ""); err != nil {
		t.Fatal(err)
	}
	r.srv.Send(opcode.SMSGUpdateObject, selfValues(lead))
	time.Sleep(100 * time.Millisecond)
	if r.s.SelectionConfirmed(lead) {
		t.Fatal("a stale echo of the lead confirmed the new select")
	}
	if err := r.s.GMOnConfirmed(context.Background(), lead, ".additem 9240"); !errors.Is(err, session.ErrSelectionChanged) {
		t.Fatalf("stale echo: %v", err)
	}
	// The server shows the stranger, then the lead: now the line goes out, and GMOnConfirmed adds no selection packet of its own.
	r.srv.Send(opcode.SMSGUpdateObject, selfValues(stranger))
	r.srv.Send(opcode.SMSGUpdateObject, selfValues(lead))
	waitUntil(t, func() bool { return r.s.SelectionConfirmed(lead) })
	if err := r.s.GMOnConfirmed(context.Background(), lead, ".additem 9240"); err != nil {
		t.Fatal(err)
	}
	if ops := selOps(r, 300*time.Millisecond); len(ops) != 2 || ops[0] != "sel 0x23a" || ops[1] != "chat" {
		t.Fatalf("ops %v", ops)
	}
}

// selOps are the selections (as "sel 0x..") and chat lines (as "chat") the server received, in order, until nothing came for d.
func selOps(r *agentRig, d time.Duration) []string {
	var out []string
	for {
		p, err := r.srv.Next(d)
		if err != nil {
			return out
		}
		switch p.Op {
		case opcode.CMSGSetSelection:
			out = append(out, fmt.Sprintf("sel %#x", uint64(packet.NewReader(p.Body).GUID())))
		case opcode.CMSGMessagechat:
			out = append(out, "chat")
		}
	}
}

// The selection-log rule (session.SelectionConfirmed): an echo confirms a select only when it cannot answer an older one.
func TestSelectionEchoMustAnswerTheSelectItConfirms(t *testing.T) {
	const lead, stranger = packet.GUID(0x23A), packet.GUID(0x4D2)

	// A fresh echo: the server showed nothing selected, the lead is selected, the echo shows it.
	r := newRig(t)
	r.srv.Send(opcode.SMSGUpdateObject, selfBlock(0))
	waitUntil(t, func() bool { return r.s.SelectionConfirmed(0) })
	if _, err := r.s.Target(context.Background(), lead, ""); err != nil {
		t.Fatal(err)
	}
	if r.s.SelectionConfirmed(lead) {
		t.Fatal("confirmed before any echo")
	}
	r.srv.Send(opcode.SMSGUpdateObject, selfValues(lead))
	waitUntil(t, func() bool { return r.s.SelectionConfirmed(lead) })

	// A stale echo: lead, then stranger, then lead again are sent; an echo of the lead may answer the first, with the stranger still
	// pending, so it confirms nothing; the stranger's echo and then the lead's do.
	r = newRig(t)
	r.srv.Send(opcode.SMSGUpdateObject, selfBlock(0))
	for _, g := range []packet.GUID{lead, stranger, lead} {
		if _, err := r.s.Target(context.Background(), g, ""); err != nil {
			t.Fatal(err)
		}
	}
	r.srv.Send(opcode.SMSGUpdateObject, selfValues(lead))
	time.Sleep(100 * time.Millisecond)
	if r.s.SelectionConfirmed(lead) || r.s.SelectionConfirmed(stranger) {
		t.Fatal("a stale echo confirmed a selection")
	}
	if !r.s.SelectionAmbiguous(lead) {
		t.Fatal("lead before stranger, unresolved: a new select of the lead is ambiguous")
	}
	r.srv.Send(opcode.SMSGUpdateObject, selfValues(stranger))
	time.Sleep(100 * time.Millisecond)
	if r.s.SelectionConfirmed(lead) || r.s.SelectionConfirmed(stranger) {
		t.Fatal("the stranger's echo, with the lead sent after it, confirmed a selection")
	}
	r.srv.Send(opcode.SMSGUpdateObject, selfValues(lead))
	waitUntil(t, func() bool { return r.s.SelectionConfirmed(lead) })
	if r.s.SelectionAmbiguous(lead) {
		t.Fatal("ambiguous after the log was resolved")
	}
}

// The core sends no update when a selection does not change the target field: selecting the unit already confirmed is confirmed at once,
// with no echo, and the line goes out without a wait.
func TestGMLineOnTheUnchangedSelectionNeedsNoEcho(t *testing.T) {
	const lead = packet.GUID(0x23A)
	r := newRig(t)
	r.srv.Send(opcode.SMSGUpdateObject, selfBlock(lead))
	waitUntil(t, func() bool { return r.s.SelectionConfirmed(lead) })
	g := remoteGame{c: &commanderService{cur: func() *session.Session { return r.s }}}
	start := time.Now()
	for i := 0; i < 3; i++ {
		if err := g.selectThenGM(context.Background(), lead, ".levelup 2"); err != nil {
			t.Fatal(err)
		}
	}
	if d := time.Since(start); d > time.Second {
		t.Fatalf("three lines on the unchanged selection took %s", d)
	}
	if ops := selOps(r, 300*time.Millisecond); len(ops) != 6 || ops[1] != "chat" || ops[3] != "chat" || ops[5] != "chat" {
		t.Fatalf("ops %v", ops)
	}
}

// A core that applies several selections in one update echoes only the last value, and only when it changed. With the lead confirmed and a
// stranger's select pending, an echo of a new select of the lead could not be told from the old one: selectConfirmed selects none first,
// waits for that echo, then selects the lead, and the line runs on the lead.
func TestGMLineSelectsNeutralFirstWhenAnEchoWouldBeAmbiguous(t *testing.T) {
	const lead, stranger = packet.GUID(0x23A), packet.GUID(0x4D2)
	r := newRig(t)
	r.srv.Send(opcode.SMSGUpdateObject, selfBlock(lead))
	waitUntil(t, func() bool { return r.s.SelectionConfirmed(lead) })
	if _, err := r.s.Target(context.Background(), stranger, ""); err != nil { // pending: the server has not shown it
		t.Fatal(err)
	}
	if ops := selOps(r, 100*time.Millisecond); len(ops) != 1 || ops[0] != "sel 0x4d2" {
		t.Fatalf("ops %v", ops)
	}
	g := remoteGame{c: &commanderService{cur: func() *session.Session { return r.s }}}
	done := make(chan error, 1)
	go func() { done <- g.selectThenGM(context.Background(), lead, ".levelup 2") }()
	inForce, shown := lead, lead
	var ops []string
	for len(ops) < 3 {
		p, err := r.srv.Next(2 * time.Second)
		if err != nil {
			t.Fatalf("ops %v: %v", ops, err)
		}
		switch p.Op {
		case opcode.CMSGSetSelection:
			// The stranger's select and this one are applied in one update; the echo shows the last value, if it changed.
			inForce = packet.NewReader(p.Body).GUID()
			ops = append(ops, fmt.Sprintf("sel %#x", uint64(inForce)))
			if inForce != shown {
				shown = inForce
				r.srv.Send(opcode.SMSGUpdateObject, selfValues(inForce))
			}
		case opcode.CMSGMessagechat:
			ops = append(ops, fmt.Sprintf("chat on %#x", uint64(inForce)))
		}
	}
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	if ops[0] != "sel 0x0" || ops[1] != "sel 0x23a" || ops[2] != "chat on 0x23a" {
		t.Fatalf("ops %v", ops)
	}
}

func waitUntil(t *testing.T, ok func() bool) {
	t.Helper()
	for i := 0; i < 200; i++ {
		if ok() {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("condition never held")
}
