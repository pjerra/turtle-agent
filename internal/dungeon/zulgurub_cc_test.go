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

// TestZulGurubTrashCcLists pins the WALK 39 trash CC lists (research/zg-trash/TRASH-TACTICS.md
// section 4, decided data change): cc_priority is the exact 16-entry list, cc_always stays empty,
// and kill_first drops the Blood Drinker 11353 to cc_priority. The list is the WALK 39 19-entry
// order cut to the TortoiseBots plan-list cap (kPlanMaxEntries 16, DungeonPlanPolicy.h:197): the
// 20261106030100 export first shipped all 19, the loader dropped all four lists and ZG ran with no
// kill_first and no CC from t1105c on.
func TestZulGurubTrashCcLists(t *testing.T) {
	p, err := Load(filepath.Join("..", "..", "dungeons", "zul_gurub.json"))
	if err != nil {
		t.Fatal(err)
	}
	wantCC := []uint32{14532, 14882, 14825, 11353, 11338, 11357, 11356, 11350, 14821, 11351, 11831, 11340, 11339, 11359, 15111, 15043}
	wantKill := []uint32{14987, 15112, 14750, 15146, 11830, 14883}
	if !slices.Equal(p.CCPriority, wantCC) {
		t.Errorf("cc_priority is %v, want %v (TRASH-TACTICS.md section 4)", p.CCPriority, wantCC)
	}
	if len(p.CCAlways) != 0 {
		t.Errorf("cc_always is %v, want empty (it only duplicates the head of cc_priority)", p.CCAlways)
	}
	if !slices.Equal(p.KillFirst, wantKill) {
		t.Errorf("kill_first is %v, want %v (11353 left kill_first for cc_priority)", p.KillFirst, wantKill)
	}
	if len(p.CCPriority) == 0 || p.CCPriority[0] != 14532 {
		t.Errorf("cc_priority[0] is %v, want 14532 first (Mar'li's pre-pull Polymorphs depend on it)", p.CCPriority)
	}
	if !slices.Contains(p.CCPriority, 11350) {
		t.Errorf("cc_priority %v lacks 11350 (Axe Thrower: Polymorph stops its Axe Flurry)", p.CCPriority)
	}
	for _, e := range p.CCPriority {
		if slices.Contains(p.KillFirst, e) {
			t.Errorf("entry %d is in both cc_priority and kill_first (kill_first wins, so the CC entry is dead data)", e)
		}
	}
	for _, e := range []uint32{14826, 11368, 11352, 14750} {
		if slices.Contains(p.CCPriority, e) {
			t.Errorf("cc_priority %v holds %d (never CC: 14826 respawns, 11368 is AoE fodder, 11352/14750 are Polymorph-immune)", p.CCPriority, e)
		}
	}
	// coreTmpl is {entry: (creature_template.type, creature_template.mechanic_immune_mask)} copied from
	// tortoise-wow sql/base/tw_world_creature_template.sql @5d79486 (commit "mangosd: build gsoap from source, ...",
	// 2026-09-16; the file predates it: last content change e1a48b4 "Initial 1.18.1-7272 spell implimentation").
	// Re-check: `git show 5d79486:sql/base/tw_world_creature_template.sql | grep -o '(<entry>,[^)]*)'`
	// and read the `type` and `mechanic_immune_mask` columns of the CREATE TABLE header.
	// type 1 Beast, 3 Demon, 6 Undead, 7 Humanoid; mask bit (1 << (mechanic-1)).
	coreTmpl := []struct {
		entry uint32
		typ   uint32
		mask  uint32
	}{
		{14532, 1, 0}, {14882, 7, 0}, {14825, 6, 8388624},
		{11353, 7, 4160}, {11338, 7, 4160}, {11357, 1, 0},
		{11356, 7, 608834276}, {11350, 7, 4160}, {14821, 1, 4160},
		{11351, 7, 4160}, {11831, 7, 4160}, {11340, 7, 4160},
		{11339, 7, 4160}, {11359, 1, 0}, {15111, 3, 4160},
		{15043, 1, 1024},
	}
	bit := func(mechanic uint32) uint32 { return 1 << (mechanic - 1) }
	// Auto CC families and their mechanics: Polymorph 17 (humanoid, beast),
	// Shackle 20 (undead), Banish 18 (demon), Hibernate 10 (beast), Fear 5
	// (humanoid, beast; the risky tier, still better than nothing).
	fits := func(typ, mask uint32) bool {
		switch typ {
		case 7: // Humanoid
			return mask&bit(17) == 0 || mask&bit(5) == 0
		case 1: // Beast
			return mask&bit(17) == 0 || mask&bit(10) == 0 || mask&bit(5) == 0
		case 6: // Undead
			return mask&bit(20) == 0
		case 3: // Demon
			return mask&bit(18) == 0
		}
		return false
	}
	if len(coreTmpl) != len(wantCC) {
		t.Errorf("core table holds %d entries, want %d (one per cc_priority entry)", len(coreTmpl), len(wantCC))
	}
	for _, c := range coreTmpl {
		if !slices.Contains(p.CCPriority, c.entry) {
			t.Errorf("cc_priority %v lacks table entry %d", p.CCPriority, c.entry)
		}
		if !fits(c.typ, c.mask) {
			t.Errorf("entry %d (type %d, immune mask %d): no auto CC family fits its type unblocked", c.entry, c.typ, c.mask)
		}
	}
}
