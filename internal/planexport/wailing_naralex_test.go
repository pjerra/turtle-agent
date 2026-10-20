package planexport

import (
	"strings"
	"testing"

	"github.com/pjerra/turtle-agent/internal/dungeon"
)

// TestWailingCavernsNaralexEventIsCut: the Naralex event's legs (12-20, after verdan) hold steps the run leader cannot
// do (the unit waits and the gossip that starts the escort), so the export keeps the mapped route (legs 0-11) and cuts
// before return_gossip; the hop's own facts stay as they were: the exact foot (flag 64), then move_hop_exact to the
// landing. The DB facts were read on 2026-10-20 through export.sh 43 and read-only SELECTs (menu 202 option 0, 32
// script_waypoint rows, EventAI 1720974-6, no ai_boss_tactic rows for 3654).
func TestWailingCavernsNaralexEventIsCut(t *testing.T) {
	p, err := dungeon.Load("../../dungeons/wailing_caverns.json")
	if err != nil {
		t.Fatal(err)
	}
	res, err := Build(p, Options{Source: "test", DropCompanionsLoot: true, GMCrossDirect: true})
	if err != nil {
		t.Fatal(err)
	}
	if res.Cut == "" {
		t.Fatalf("the plan's event legs are exported, want the cut: %+v", res.Rows.Count())
	}
	for _, want := range []string{"cut before leg 12 (return_gossip)", "wait_for.unit has no run leader step (unit 3678 seen)"} {
		if !strings.Contains(res.Cut, want) && !strings.Contains(strings.Join(res.Warnings, "\n"), want) {
			t.Fatalf("the cut lacks %q: %s", want, res.Cut)
		}
	}
	// The rows carry the mapped route only: 12 legs (0-11, verdan last), 107 steps (120 rows with the plan row), no
	// event leg and no move_direct for the way back to the Disciple.
	if n := len(res.Rows.Legs); n != 12 || res.Rows.Legs[n-1].Name != "verdan" {
		t.Fatalf("%d legs, want 12 with verdan last: %v", n, res.Rows.Legs)
	}
	if res.Rows.Count() != 120 {
		t.Fatalf("%d rows, want 120 (the plan row, 12 legs and 107 steps of legs 0-11)", res.Rows.Count())
	}
	for _, s := range res.Rows.Steps {
		if s.Leg >= 12 {
			t.Fatalf("a step of event leg %d (%s) is in the rows", s.Leg, s.Kind)
		}
	}
	for _, s := range res.Rows.Steps {
		if s.Kind == KindMoveDirect && s.Leg >= 0 {
			t.Fatalf("a move_direct row is in the legs 0-11 (the way back is cut with the leg): %+v", s)
		}
	}
}

// The event's own shape, for the record: the legs that are cut would export wave pulls, the hold of Mutanus (instance
// data 4, the event's done) and no boss tactics; deleting the unit wait of return_gossip moves the cut onto the gossip
// step, which the test catches by its message.
func TestWailingCavernsNaralexEventIsCutMutations(t *testing.T) {
	p, err := dungeon.Load("../../dungeons/wailing_caverns.json")
	if err != nil {
		t.Fatal(err)
	}
	// Delete the Disciple's presence wait: the cut is then named by the gossip step.
	p.Legs[12].Steps = []dungeon.Step{p.Legs[12].Steps[1]}
	res, err := Build(p, Options{Source: "test", DropCompanionsLoot: true, GMCrossDirect: true})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(res.Cut, "gossip (NPC 3678") || strings.Contains(res.Cut, "wait_for.unit") {
		t.Fatalf("after deleting the presence wait the cut names %q, want the gossip step", res.Cut)
	}
}
