package main

import (
	"testing"

	"github.com/pjerra/turtle-agent/internal/dungeon"
	"github.com/pjerra/turtle-agent/internal/events"
)

// The driver reads the module's refusals out of the event log: TBM:ACTION_ERR replies (system messages or addon
// messages) and the "is evading" whisper; acks, other lines and chat are not refusals.
func TestRefusalCursorReadsTBMErrorsAndWhispers(t *testing.T) {
	l := events.New(100)
	l.Append(events.Event{Kind: events.KindSystem, Text: "TBM:ACTION_ERR|pullback|unreachable|before the run"})
	var c refusalCursor
	c.mark(l)
	l.Append(events.Event{Kind: events.KindSystem, Text: "TBM:ACTION_ACK|attack|party|4|-"})
	l.Append(events.Event{Kind: events.KindSystem, Text: "TBM:ACTION_ERR|pullback|unreachable|The tank could not start moving to a firing position for the pull."})
	l.Append(events.Event{Kind: events.KindAddon, Text: "ACTION_ERR|attack|no-target|Select a live non-bot target first.", Data: map[string]any{"prefix": "TBM"}})
	l.Append(events.Event{Kind: events.KindSystem, Text: "TBM:ACTION_ERR|pullback|no-tank|No live tank-capable owned party bot can pull this target.", Data: map[string]any{"raw": "TBM:ACTION_ERR|pullback|no-tank|No live tank-capable owned party bot can pull this target."}})
	l.Append(events.Event{Kind: events.KindWhisper, Source: "Gelpanbrad", Text: "Stonevault Ambusher is evading"})
	l.Append(events.Event{Kind: events.KindWhisper, Source: "Gelpanbrad", Text: "Stonevault Ambusher is dead"})
	l.Append(events.Event{Kind: events.KindWhisper, Source: "Gelpanbrad", Text: "hello there"})
	l.Append(events.Event{Kind: events.KindChat, Text: "Stonevault Ambusher is evading"})

	want := []dungeon.Refusal{
		{Intent: "pullback", Code: "unreachable", Message: "The tank could not start moving to a firing position for the pull."},
		{Intent: "attack", Code: "no-target", Message: "Select a live non-bot target first."},
		{Intent: "pullback", Code: "no-tank", Message: "No live tank-capable owned party bot can pull this target."},
		{Intent: "attack", Code: "evading", Message: "Stonevault Ambusher is evading"},
		{Intent: "attack", Code: "dead-target", Message: "Stonevault Ambusher is dead"},
	}
	got := c.next(l)
	if len(got) != len(want) {
		t.Fatalf("%d refusals %+v, want %d", len(got), got, len(want))
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("refusal %d: %+v, want %+v", i, got[i], want[i])
		}
	}
	if again := c.next(l); len(again) != 0 {
		t.Fatalf("read twice: %+v", again)
	}
}
