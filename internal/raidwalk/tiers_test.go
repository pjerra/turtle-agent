package raidwalk

import (
	"path/filepath"
	"strings"
	"testing"

	"github.com/pjerra/turtle-agent/internal/dungeon"
)

func TestResolveTier(t *testing.T) {
	// Onyxia's row of the committed table (spec amendment G1, Sagiroth's rule): the tier-3 unlock needs an Onyxia
	// kill, so a player who fights her holds tier 2.
	if got, source, err := ResolveTier("onyxias_lair", 0, 0, 0); err != nil || got != 2 || source != TierSourceTable {
		t.Fatalf("onyxias_lair with no flag = %d %q %v, want tier 2 from the table", got, source, err)
	}
	// A raid the table leaves unknown refuses to run without --tier: the tier is what a wipe is judged against.
	if _, _, err := ResolveTier("molten_core", 0, 0, 0); err == nil || !strings.Contains(err.Error(), "--tier") {
		t.Fatalf("molten_core without --tier: %v, want a refusal that names --tier", err)
	}
	if got, source, err := ResolveTier("molten_core", 0, 2, 0); err != nil || got != 2 || source != TierSourceFlag {
		t.Fatalf("molten_core --tier 2 = %d %q %v", got, source, err)
	}
	// --tier fills an unknown raid only; it never overrides a row (that would be an unmarked comparison).
	if _, _, err := ResolveTier("onyxias_lair", 0, 3, 0); err == nil || !strings.Contains(err.Error(), "--tier-override") {
		t.Fatalf("onyxias_lair --tier 3: %v, want a refusal that names --tier-override", err)
	}
	// --tier-override runs a comparison, which the summary marks.
	if got, source, err := ResolveTier("onyxias_lair", 0, 0, 3); err != nil || got != 3 || source != TierSourceOverride {
		t.Fatalf("onyxias_lair --tier-override 3 = %d %q %v", got, source, err)
	}
	// A roster's tier is checked against the table too: the row's own value reads as the table's.
	if got, source, err := ResolveTier("onyxias_lair", 2, 0, 0); err != nil || got != 2 || source != TierSourceTable {
		t.Fatalf("roster tier 2 on onyxia = %d %q %v", got, source, err)
	}
	if _, _, err := ResolveTier("onyxias_lair", 3, 0, 0); err == nil || !strings.Contains(err.Error(), "--tier-override") {
		t.Fatalf("roster tier 3 on onyxia: %v, want the comparison rule", err)
	}
	// The file is the operator's declaration for a raid the table leaves unknown.
	if got, source, err := ResolveTier("molten_core", 1, 0, 0); err != nil || got != 1 || source != TierSourceRoster {
		t.Fatalf("roster tier 1 on molten_core = %d %q %v", got, source, err)
	}

	for _, c := range []struct {
		name string
		call func() (int, string, error)
	}{
		{"--tier 4", func() (int, string, error) { return ResolveTier("molten_core", 0, 4, 0) }},
		{"--tier -1", func() (int, string, error) { return ResolveTier("molten_core", 0, -1, 0) }},
		{"--tier-override 0 is absent", func() (int, string, error) { return ResolveTier("molten_core", 0, 0, 0) }},
		{"--tier-override 4", func() (int, string, error) { return ResolveTier("onyxias_lair", 0, 0, 4) }},
		{"both flags", func() (int, string, error) { return ResolveTier("onyxias_lair", 0, 2, 3) }},
		{"roster tier 4", func() (int, string, error) { return ResolveTier("molten_core", 4, 0, 0) }},
	} {
		got, source, err := c.call()
		if err == nil || got != 0 || source != "" {
			t.Errorf("%s: %d %q %v, want a refusal", c.name, got, source, err)
		}
	}
}

// TestMinimumHireTierTable: one row per raid plan (so a new raid cannot be forgotten), 0-3, and Onyxia's 2.
func TestMinimumHireTierTable(t *testing.T) {
	if got := MinimumHireTier["onyxias_lair"]; got != 2 {
		t.Fatalf("onyxias_lair = %d, want 2 (onyxia-kill-plan.md (c))", got)
	}
	plans, err := filepath.Glob("../../dungeons/*.json")
	if err != nil || len(plans) == 0 {
		t.Fatalf("dungeons/*.json: %v (%d files)", err, len(plans))
	}
	raids := 0
	for _, path := range plans {
		p, err := dungeon.Load(path)
		if err != nil {
			t.Fatal(err)
		}
		if !p.IsRaid() {
			continue
		}
		raids++
		tier, ok := MinimumHireTier[p.Name]
		if !ok {
			t.Errorf("raid %s has no tier row (add it, 0 until its unlock quest is sourced)", p.Name)
			continue
		}
		if tier < 0 || tier > maxHireTier {
			t.Errorf("%s: tier %d, want 0-%d", p.Name, tier, maxHireTier)
		}
	}
	if raids == 0 {
		t.Fatal("no raid plan found")
	}
}
