package main

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/pjerra/turtle-agent/internal/events"
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
