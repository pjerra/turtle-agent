package tbm

import (
	"reflect"
	"testing"

	"github.com/TortoiseBots/turtle-agent/internal/events"
)

// A roster response as HandleRoster writes it (BotCommands.cpp:1880-2024 in TortoiseBots 18d4d14).
var roster = []string{
	"TBM:ROSTER_BEGIN|2",
	"TBM:ROSTER|7|Gorm|1|online|1|map:0,zone:12,area:87",
	"TBM:ROSTER|9|Lia|5|removing|0|-",
	"TBM:ROSTER_END",
	"TBM:CC_ASSIGN_BEGIN|0",
	"TBM:CC_ASSIGN_END",
	"TBM:CLAIMED_BEGIN|0",
	"TBM:CLAIMED_END",
	"TBM:BOTSTATE_BEGIN|1",
	"TBM:BOTSTATE|Gorm|move=follow,loot=on,aoe=off",
	"TBM:BOTSTATE_END",
	"TBM:VERSION|2026-10-11-v8",
	"TBM:CAPS|pull-seconds,flee,inventory,behavior,claimed-roster",
	"TBM:TRANSPORT|party",
}

func TestStatusFromRoster(t *testing.T) {
	var st Status
	for i, l := range roster {
		if done := st.Add(l); done != (i == len(roster)-1) {
			t.Fatalf("line %d %q: done=%v", i, l, done)
		}
	}
	want := Status{
		Version: "2026-10-11-v8", Transport: "party",
		Caps:     []string{"pull-seconds", "flee", "inventory", "behavior", "claimed-roster"},
		Roster:   []RosterRow{{GUID: 7, Name: "Gorm", Class: 1, State: "online", Grouped: true, Location: "map:0,zone:12,area:87"}, {GUID: 9, Name: "Lia", Class: 5, State: "removing", Location: "-"}},
		BotState: map[string]string{"Gorm": "move=follow,loot=on,aoe=off"},
		Lines:    roster,
		Complete: true,
	}
	if !reflect.DeepEqual(st, want) {
		t.Fatalf("got  %+v\nwant %+v", st, want)
	}
}

func TestRosterErrorEndsTheHandshake(t *testing.T) {
	var st Status
	if !st.Add("TBM:ROSTER_ERROR|not-in-game|You must be in-game.") || !st.Complete ||
		!reflect.DeepEqual(st.Errors, []string{"TBM:ROSTER_ERROR|not-in-game|You must be in-game."}) {
		t.Fatalf("%+v", st)
	}
}

func TestLineFromEvents(t *testing.T) {
	cases := []struct {
		e    events.Event
		line string
		ok   bool
	}{
		// Chat transport: a system line; the raw text wins over the colour-stripped one.
		{events.Event{Kind: events.KindSystem, Text: "TBM:ROSTER|9|Lia|5|emoving|0|-", Data: map[string]any{"raw": "TBM:ROSTER|9|Lia|5|removing|0|-"}}, "TBM:ROSTER|9|Lia|5|removing|0|-", true},
		{events.Event{Kind: events.KindSystem, Text: "TBM:VERSION|x"}, "TBM:VERSION|x", true},
		// Addon transport: prefix TBM, the text is the protocol line.
		{events.Event{Kind: events.KindAddon, Text: "TBM:TRANSPORT|party", Data: map[string]any{"prefix": "TBM"}}, "TBM:TRANSPORT|party", true},
		{events.Event{Kind: events.KindAddon, Text: "TBM:TRANSPORT|party", Data: map[string]any{"prefix": "BigWigs"}}, "", false},
		{events.Event{Kind: events.KindSystem, Text: "Bot Gorm logout requested."}, "", false},
		{events.Event{Kind: events.KindChat, Text: "TBM:VERSION|x"}, "", false},
	}
	for i, c := range cases {
		if line, ok := Line(c.e); line != c.line || ok != c.ok {
			t.Errorf("case %d: %q %v", i, line, ok)
		}
	}
}

func TestCommandBodies(t *testing.T) {
	if Chat("roster") != ".bot roster" || Prefix != "TBM" || !reflect.DeepEqual(Handshake, []string{"help", "roster"}) {
		t.Fatal("handshake")
	}
}

// Review nits: a roster row may carry a 7th field, the CC mark (Roster.lua:342-346 at
// 3384e15); CAPS splits on commas, spaces and pipes like the addon ([^,%s|]+, Comms.lua:623);
// BOTSTATE_BEGIN starts a fresh bot state set.
func TestRosterCCMarkCapsAndBotStateReset(t *testing.T) {
	var st Status
	for _, l := range []string{
		"TBM:BOTSTATE_BEGIN|1", "TBM:BOTSTATE|Old|move=stay", "TBM:BOTSTATE_END",
		"TBM:ROSTER_BEGIN|2",
		"TBM:ROSTER|7|Gorm|1|online|1|-|moon",
		"TBM:ROSTER|8|Lia|5|online|0|-|x|extra",
		"TBM:ROSTER|9|Bad|1|online",
		"TBM:ROSTER_END",
		"TBM:BOTSTATE_BEGIN|1", "TBM:BOTSTATE|Gorm|move=follow", "TBM:BOTSTATE_END",
		"TBM:CAPS|pull-seconds, flee|inventory,,behavior",
	} {
		st.Add(l)
	}
	want := []RosterRow{{GUID: 7, Name: "Gorm", Class: 1, State: "online", Grouped: true, Location: "-", CCMark: "moon"},
		{GUID: 8, Name: "Lia", Class: 5, State: "online", Location: "-", CCMark: "x"}}
	if !reflect.DeepEqual(st.Roster, want) {
		t.Fatalf("roster %+v", st.Roster)
	}
	if !reflect.DeepEqual(st.BotState, map[string]string{"Gorm": "move=follow"}) {
		t.Fatalf("bot state %+v", st.BotState)
	}
	if !reflect.DeepEqual(st.Caps, []string{"pull-seconds", "flee", "inventory", "behavior"}) {
		t.Fatalf("caps %q", st.Caps)
	}
}
