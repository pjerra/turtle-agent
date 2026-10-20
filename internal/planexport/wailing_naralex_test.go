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

// TestWailingCavernsExtraPlanIsNotExported: the Naralex event needs a GM for its return (gm_cross) and holds unit waits and a gossip
// step the run leader has none of, so plan-export refuses the extra plan, with or without --gm-cross-direct, naming the gm_cross
// of the return_gossip leg (the first leg: nothing before it could be exported either).
func TestWailingCavernsExtraPlanIsNotExported(t *testing.T) {
	p, err := dungeon.Load("../../dungeons/wailing_caverns-extra.json")
	if err != nil {
		t.Fatal(err)
	}
	_, err = Build(p, Options{Source: "test", DropCompanionsLoot: true})
	if err == nil || !strings.Contains(err.Error(), "leg 0 (return_gossip)") || !strings.Contains(err.Error(), "gm_cross") {
		t.Fatalf("without --gm-cross-direct: %v, want the gm_cross of return_gossip refused", err)
	}
	_, err = Build(p, Options{Source: "test", DropCompanionsLoot: true, GMCrossDirect: true})
	if err == nil || !strings.Contains(err.Error(), "return_gossip") {
		t.Fatalf("with --gm-cross-direct: %v, want the first leg refused (nothing of the event is a run leader step)", err)
	}
}
