package main

import (
	"testing"

	"github.com/pjerra/turtle-agent/internal/dungeon"
	"github.com/pjerra/turtle-agent/internal/events"
	"github.com/pjerra/turtle-agent/internal/packet"
)

// The remote driver finds what the gossip step needs by asserting these interfaces on remoteGame (d.g.(X)): a missing method is no compile
// error and would turn the agent's step into "no hands" (see the area_trigger fallback, live 2026-10-15).
var (
	_ dungeon.GossipTalker = remoteGame{}
	_ dungeon.Follower     = remoteGame{}
	_ dungeon.UnitSource   = remoteGame{}
	_ dungeon.OrderGame    = remoteGame{}
	_ dungeon.PathPlanner  = remoteGame{}
	_ dungeon.GossipTalker = (*sessionGame)(nil)
)

func TestGossipAnswerOf(t *testing.T) {
	menu := &events.Event{Kind: events.KindGossip, SourceGUID: packet.GUID(7), Data: map[string]any{
		"text_id": uint32(1513),
		"options": []map[string]any{
			{"index": uint32(0), "icon": uint8(0), "coded": false, "text": "Weegli, blow the door!"},
			{"index": uint32(2), "icon": uint8(0), "coded": true, "text": "Code"},
		},
	}}
	got := gossipAnswerOf(menu)
	want := dungeon.GossipAnswer{Kind: dungeon.GossipAnswerMenu, TextID: 1513, Options: []dungeon.GossipLine{{Index: 0, Text: "Weegli, blow the door!"}, {Index: 2, Text: "Code", Coded: true}}}
	if got.Kind != want.Kind || got.TextID != want.TextID || len(got.Options) != 2 || got.Options[0] != want.Options[0] || got.Options[1] != want.Options[1] {
		t.Fatalf("%+v", got)
	}
	// After a JSON round trip the numbers are float64 and the list is a []any.
	rt := &events.Event{Kind: events.KindGossip, Data: map[string]any{"text_id": float64(1511), "options": []any{map[string]any{"index": float64(1), "text": "x", "coded": false}}}}
	if g := gossipAnswerOf(rt); g.TextID != 1511 || len(g.Options) != 1 || g.Options[0].Index != 1 {
		t.Fatalf("%+v", g)
	}
	// An empty menu (Weegli before the event) is a menu without options, not nothing.
	if g := gossipAnswerOf(&events.Event{Kind: events.KindGossip, Data: map[string]any{"text_id": uint32(1511), "options": []map[string]any{}}}); g.Kind != dungeon.GossipAnswerMenu || g.TextID != 1511 || len(g.Options) != 0 {
		t.Fatalf("%+v", g)
	}
	if g := gossipAnswerOf(&events.Event{Kind: events.KindGossipComplete}); g.Kind != dungeon.GossipAnswerComplete {
		t.Fatalf("%+v", g)
	}
	if g := gossipAnswerOf(&events.Event{Kind: events.KindVendorList}); g.Kind != dungeon.GossipAnswerOther || g.Other != events.KindVendorList {
		t.Fatalf("%+v", g)
	}
	if g := gossipAnswerOf(nil); g.Kind != dungeon.GossipAnswerNone {
		t.Fatalf("%+v", g)
	}
}
