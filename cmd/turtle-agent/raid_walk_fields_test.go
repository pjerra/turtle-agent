package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/pjerra/turtle-agent/internal/raidwalk"
	"github.com/pjerra/turtle-agent/internal/tbm"
)

// settleSeconds names a summary's hire_settle_s in a failure message: absent when the summary has no field.
func settleSeconds(p *float64) string {
	if p == nil {
		return "absent"
	}
	return fmt.Sprint(*p)
}

// TestWalkFramePowerTypeNeedsAPool: a bot with no power pool (max_power 0) has no power_type key in its frame line,
// so the reader never takes the absent key for mana (0). A bot with a pool keeps power_type even at 0 (mana).
func TestWalkFramePowerTypeNeedsAPool(t *testing.T) {
	ff, err := os.Create(filepath.Join(t.TempDir(), "frames.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	defer ff.Close()
	w := &walk{framesF: ff, roster: &raidwalk.Roster{}}
	w.saveFrame(time.Unix(1000, 0), &tbm.ViewFrame{Party: []tbm.PartyRec{
		{Name: "NoPool", MaxPower: 0, PowerType: 0},
		{Name: "Mana", Power: 0, MaxPower: 2400, PowerType: 0},
	}})
	raw, err := os.ReadFile(ff.Name())
	if err != nil {
		t.Fatal(err)
	}
	var rec struct {
		Party []map[string]any `json:"party"`
	}
	if err := json.Unmarshal(raw, &rec); err != nil {
		t.Fatalf("frames.jsonl: %v: %s", err, raw)
	}
	if len(rec.Party) != 2 {
		t.Fatalf("%d bots in the frame, want 2: %s", len(rec.Party), raw)
	}
	for _, p := range rec.Party {
		_, has := p["power_type"]
		switch p["name"] {
		case "NoPool":
			if has {
				t.Fatalf("NoPool (max_power 0) carries power_type %v, want no key: %s", p["power_type"], raw)
			}
		case "Mana":
			if v, ok := p["power_type"].(float64); !ok || v != 0 {
				t.Fatalf("Mana (max_power 2400) power_type %v (present %v), want 0: %s", p["power_type"], has, raw)
			}
		default:
			t.Fatalf("unexpected bot %v", p["name"])
		}
	}
}
