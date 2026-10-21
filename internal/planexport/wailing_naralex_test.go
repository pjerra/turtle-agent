package planexport

import (
	"strings"
	"testing"

	"github.com/pjerra/turtle-agent/internal/dungeon"
)

// TestWailingCavernsExportNeedsNoGMFlagAndNoCut: the mapped route (12 legs to Verdan) has no gm_cross and no step the run leader cannot
// do, so the export needs neither --gm-cross-direct nor a cut. Without a navmesh it is 107 steps (120 rows with the plan row and the
// 12 legs); with --mmaps and --approach-hops, as the applied mirror 20261018201557 was written, 226 steps (239 rows: checked by hand
// against the mirror, see the commit). The Naralex event lives in dungeons/wailing_caverns-extra.json.
func TestWailingCavernsExportNeedsNoGMFlagAndNoCut(t *testing.T) {
	p, err := dungeon.Load("../../dungeons/wailing_caverns.json")
	if err != nil {
		t.Fatal(err)
	}
	res, err := Build(p, Options{Source: "test", DropCompanionsLoot: true})
	if err != nil {
		t.Fatalf("the export without --gm-cross-direct: %v", err)
	}
	if res.Cut != "" {
		t.Fatalf("the export is cut: %s", res.Cut)
	}
	for _, w := range res.Warnings {
		if strings.Contains(w, "gm_cross") {
			t.Errorf("a gm_cross warning: %s", w)
		}
	}
	if n := len(res.Rows.Legs); n != 12 || res.Rows.Legs[n-1].Name != "verdan" {
		t.Fatalf("%d legs, want 12 with verdan last", n)
	}
	if len(res.Rows.Steps) != 107 || res.Rows.Count() != 120 {
		t.Fatalf("%d steps and %d rows, want 107 and 120 (no navmesh: the mapped route only)", len(res.Rows.Steps), res.Rows.Count())
	}
}

// TestWailingCavernsExtraPlanIsNotExported: the Naralex event holds unit waits and a gossip step the run leader has none of, so plan-export
// refuses the extra plan at its first leg (return_gossip: nothing before it could be exported either), with or without --gm-cross-direct.
// The return from Verdan is the hop pair now, not a gm_cross: the refusal is the unit wait of the leg's steps, and no gm_cross is named.
func TestWailingCavernsExtraPlanIsNotExported(t *testing.T) {
	p, err := dungeon.Load("../../dungeons/wailing_caverns-extra.json")
	if err != nil {
		t.Fatal(err)
	}
	for _, gmDirect := range []bool{false, true} {
		_, err = Build(p, Options{Source: "test", DropCompanionsLoot: true, GMCrossDirect: gmDirect})
		if err == nil || !strings.Contains(err.Error(), "leg 0 (return_gossip)") || !strings.Contains(err.Error(), "wait_for.unit") || strings.Contains(err.Error(), "gm_cross") {
			t.Fatalf("GMCrossDirect=%v: %v, want the unit wait of return_gossip refused and no gm_cross named", gmDirect, err)
		}
	}
}

// TestNaralexReturnHopRows: the hop pair of the event's first leg (the ramp lip, then the foot) is written as the same row kinds as the
// Serpentis hop of the base plan: an exact move (flags FlagExact) at the lip, the move_hop_exact at the foot, then the walk to the leg's
// target. The leg is cut to its via points and target (the unit wait after them is what the run leader lacks), and needs no
// --gm-cross-direct.
func TestNaralexReturnHopRows(t *testing.T) {
	p, err := dungeon.Load("../../dungeons/wailing_caverns-extra.json")
	if err != nil {
		t.Fatal(err)
	}
	leg := p.Legs[0]
	if leg.Name != "return_gossip" {
		t.Fatalf("leg 0 is %s", leg.Name)
	}
	leg.Steps = nil
	q := *p
	q.Legs = []dungeon.Leg{leg}
	res, err := Build(&q, Options{Source: "test", DropCompanionsLoot: true})
	if err != nil {
		t.Fatalf("the hop pair and the walk to the stand: %v", err)
	}
	for _, w := range res.Warnings {
		if strings.Contains(w, "gm_cross") {
			t.Errorf("a gm_cross warning: %s", w)
		}
	}
	s := res.Rows.Steps
	if len(s) != 3 {
		t.Fatalf("%d steps, want the exact move, the hop and the move to the stand: %+v", len(s), s)
	}
	if s[0].Kind != KindMove || s[0].Flags != FlagExact || s[0].X != -287.5 || s[0].Y != 5 || s[0].Z != -63.6 {
		t.Errorf("step 0 %+v, want an exact move to the lip -287.5 5 -63.6", s[0])
	}
	if s[1].Kind != KindMoveHopExact || s[1].Flags != 0 || s[1].X != -293.3 || s[1].Y != 0.5 || s[1].Z != -58 {
		t.Errorf("step 1 %+v, want the move_hop_exact to the foot -293.3 0.5 -58", s[1])
	}
	if s[2].Kind != KindMove || s[2].Flags != 0 || s[2].X != -134 || s[2].Y != 128.5 {
		t.Errorf("step 2 %+v, want the plain move to the gossip's stand -134 128.5", s[2])
	}
}
