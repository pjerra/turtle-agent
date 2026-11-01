package main

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/pjerra/turtle-agent/internal/objects"
	"github.com/pjerra/turtle-agent/internal/packet"
	"github.com/pjerra/turtle-agent/internal/tbm"
)

// TestUnitsMarkTheBoundBots: after a raid bind the commander is out of the raid, so no bot is in_party; the view
// frame's party still names the bound bots, and `units` marks them (and keeps only them with "bound").
func TestUnitsMarkTheBoundBots(t *testing.T) {
	now := time.Unix(1_800_000_000, 0)
	fr := tbm.ViewFrame{At: now.Add(-2 * time.Second), Party: []tbm.PartyRec{{GUID: 0x10, Name: "Tumble"}, {GUID: 0x11, Name: "Moonface"}}}
	us := []objects.Unit{
		{GUID: 0x10, Type: "player", Name: "Tumble"},
		{GUID: 0x99, Type: "player", Name: "Stranger"},
		{GUID: 0x11, Type: "player", Name: "Moonface"},
	}
	bound := boundGUIDs(fr, true, now)
	rows := unitRows(us, bound, false)
	if len(rows) != 3 || !rows[0].Bound || rows[1].Bound || !rows[2].Bound || rows[0].InParty {
		t.Fatalf("%+v", rows)
	}
	rows = unitRows(us, bound, true)
	if len(rows) != 2 || rows[0].Name != "Tumble" || rows[1].Name != "Moonface" {
		t.Fatalf("bound only: %+v", rows)
	}
	raw, err := json.Marshal(rows[0])
	if err != nil {
		t.Fatal(err)
	}
	// the unit's own fields stay flat, so callers that read []objects.Unit still decode a row
	if s := string(raw); !strings.Contains(s, `"bound":true`) || !strings.Contains(s, `"name":"Tumble"`) || strings.Contains(s, `"Unit"`) {
		t.Fatalf("row json %s", s)
	}
	var back objects.Unit
	if err := json.Unmarshal(raw, &back); err != nil || back.GUID != packet.GUID(0x10) {
		t.Fatalf("decode %+v %v", back, err)
	}
}

func TestUnitsBoundNeedsAFreshFrame(t *testing.T) {
	now := time.Unix(1_800_000_000, 0)
	fr := tbm.ViewFrame{At: now.Add(-2 * time.Minute), Party: []tbm.PartyRec{{GUID: 0x10}}}
	if b := boundGUIDs(fr, true, now); b != nil {
		t.Fatalf("a 2 min old frame marked %v", b)
	}
	if b := boundGUIDs(tbm.ViewFrame{}, false, now); b != nil {
		t.Fatalf("no frame marked %v", b)
	}
	if rows := unitRows([]objects.Unit{{GUID: 0x10}}, nil, true); len(rows) != 0 {
		t.Fatalf("bound only without a frame: %+v", rows)
	}
	if rows := unitRows(nil, nil, false); rows == nil {
		t.Fatal("an empty answer must be a list, not null")
	}
}
