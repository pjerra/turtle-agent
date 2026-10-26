package main

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/pjerra/turtle-agent/internal/events"
	"github.com/pjerra/turtle-agent/internal/opcode"
	"github.com/pjerra/turtle-agent/internal/packet"
	"github.com/pjerra/turtle-agent/internal/session"
)

func gpsSys(l *events.Log, text string) { l.Append(events.Event{Kind: events.KindSystem, Text: text}) }

// The reply is the system lines after the send, up to the one with FloorZ; earlier lines and other kinds are not part of it.
func TestReadGPSReplyCollectsTheLinesToFloorZ(t *testing.T) {
	log := events.New(100)
	gpsSys(log, "old line")
	since := log.Last()
	go func() {
		time.Sleep(20 * time.Millisecond)
		gpsSys(log, "Map: 70 (Uldaman)")
		log.Append(events.Event{Kind: events.KindChat, Text: "somebody says hi"})
		gpsSys(log, "X: 1 Y: 2 Z: 3")
		gpsSys(log, "GroundZ: 3.000000 FloorZ: 3.000000 Have height data (Map: 1 VMap: 1)")
	}()
	got, err := readGPSReply(context.Background(), log, since, 2*time.Second, 400*time.Millisecond)
	if err != nil || got != "Map: 70 (Uldaman)\nX: 1 Y: 2 Z: 3\nGroundZ: 3.000000 FloorZ: 3.000000 Have height data (Map: 1 VMap: 1)" {
		t.Fatalf("%q %v", got, err)
	}
}

// A reply without a FloorZ line ends on a quiet gap after its last line; no reply at all is an error after the total wait.
func TestReadGPSReplyQuietAndTimeout(t *testing.T) {
	log := events.New(100)
	since := log.Last()
	gpsSys(log, "There is no such command")
	start := time.Now()
	got, err := readGPSReply(context.Background(), log, since, 2*time.Second, 50*time.Millisecond)
	if err != nil || !strings.Contains(got, "no such command") || time.Since(start) > time.Second {
		t.Fatalf("%q %v after %s", got, err, time.Since(start))
	}
	if _, err := readGPSReply(context.Background(), log, log.Last(), 80*time.Millisecond, 50*time.Millisecond); err == nil {
		t.Fatal("no reply must be an error")
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := readGPSReply(ctx, log, log.Last(), time.Second, 50*time.Millisecond); err == nil {
		t.Fatal("a cancelled context must be an error")
	}
}

// `.gps` reports the unit the commander has selected, so the read selects the agent's own character (guid 0x2A) first and sends the line
// only once the server shows that selection. A stranger left selected (a mob, a bot) must not be read in its place.
func TestGPSSelectsTheCommanderBeforeTheLine(t *testing.T) {
	const stranger = packet.GUID(0x4D2)
	r := newRig(t)
	r.srv.Send(opcode.SMSGUpdateObject, selfBlock(stranger)) // the server shows a stranger selected
	g := remoteGame{c: &commanderService{cur: func() *session.Session { return r.s }}}
	type reply struct {
		text string
		err  error
	}
	done := make(chan reply, 1)
	go func() {
		text, err := g.GPS(context.Background())
		done <- reply{text, err}
	}()
	// The server applies each selection and echoes it. A neutral selection of nothing may come first (when GPS starts before the session
	// read the create block, the field is unknown: session.SelectConfirmed); anything else, or a line on anything but the commander, fails.
	var ops []uint16
	inForce := stranger
	for len(ops) == 0 || ops[len(ops)-1] != opcode.CMSGMessagechat {
		p, err := r.srv.Next(2 * time.Second)
		if err != nil {
			t.Fatalf("after %v: %v", ops, err)
		}
		switch p.Op {
		case opcode.CMSGSetSelection:
			guid := packet.NewReader(p.Body).GUID()
			if guid != 0x2A && (guid != 0 || len(ops) != 0) {
				t.Fatalf("selected %#x, not the commander (0x2A) or a first neutral selection of nothing", uint64(guid))
			}
			inForce = guid
			ops = append(ops, p.Op)
			r.srv.Send(opcode.SMSGUpdateObject, selfValues(guid)) // the server's echo of the selection
		case opcode.CMSGMessagechat:
			if inForce != 0x2A {
				t.Fatalf("the .gps line was sent with %#x selected, not the commander", uint64(inForce))
			}
			if !strings.Contains(string(p.Body), ".gps") {
				t.Fatalf("chat line %q", p.Body)
			}
			ops = append(ops, p.Op)
			r.s.Log().Append(events.Event{Kind: events.KindSystem, Text: "X: 1.000000 Y: 2.000000 Z: 3.000000 Orientation: 0.000000\n" +
				"GroundZ: 3.000000 FloorZ: 3.000000 Have height data (Map: 1 VMap: 1)"})
		}
	}
	select {
	case got := <-done:
		if got.err != nil || !strings.Contains(got.text, "FloorZ: 3.000000") {
			t.Fatalf("%q %v", got.text, got.err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("GPS did not return")
	}
}
