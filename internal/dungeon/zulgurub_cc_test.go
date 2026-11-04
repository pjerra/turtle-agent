package dungeon

import (
	"path/filepath"
	"slices"
	"testing"
)

// TestZulGurubNoCCOnSacrificedTrolls: Sacrificed Troll 14826 respawns every
// 10 s (tw_world_creature.sql 49078-49087), so a crowd-control order on one
// is wasted. research/underperf/ROUND2.md item 2 (lead decision) takes it out
// of zul_gurub's cc_priority.
func TestZulGurubNoCCOnSacrificedTrolls(t *testing.T) {
	p, err := Load(filepath.Join("..", "..", "dungeons", "zul_gurub.json"))
	if err != nil {
		t.Fatal(err)
	}
	if slices.Contains(p.CCPriority, 14826) {
		t.Fatalf("cc_priority %v holds 14826 (Sacrificed Troll, respawns every 10 s)", p.CCPriority)
	}
}
