package worldspawn

import (
	"math"
	"testing"
)

// squareMovement is a 40 yd square loop (points 1-4, 160 yd around) with the given wait at each point.
func squareMovement(guid uint64, waits [4]int) string {
	pts := [4][2]int{{0, 0}, {40, 0}, {40, 40}, {0, 40}}
	body := "guid\tpoint\tx\ty\tz\twaittime\n"
	for i, p := range pts {
		body += join(guid, i+1, p[0], p[1], 0, waits[i])
	}
	return body
}

// TestPatrolPeriodFromMovement pins the estimate: the closed loop at 2.5 yd/s plus the waits at its points. A 40 yd square
// at speed 1 takes 64 s; two 2 s waits make 68 s.
func TestPatrolPeriodFromMovement(t *testing.T) {
	dir := writeExport(t, map[string]string{
		"creatures.tsv": newHeader +
			spawnRowOpts(500, 100, "Square", 0, 0, 0, 2, 0, 0, 20, 0, 1, 0, 0),
		"movement.tsv": squareMovement(500, [4]int{0, 2000, 0, 2000}),
	})
	w, _, err := Load(dir, 700)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if len(w.Patrols) != 1 {
		t.Fatalf("%d patrols, want 1", len(w.Patrols))
	}
	p := w.Patrols[0]
	if p.ID != "500" || p.Source != "creature_movement 500" {
		t.Errorf("patrol id %q source %q, want 500 from creature_movement", p.ID, p.Source)
	}
	if len(p.Path) != 4 || len(p.WaitsMS) != 4 {
		t.Fatalf("path %d points, waits %d, want 4 and 4", len(p.Path), len(p.WaitsMS))
	}
	if p.Path[1].X != 40 || p.Path[2].Y != 40 {
		t.Errorf("path = %+v, want the waypoints in point order", p.Path)
	}
	if want := []int{0, 2000, 0, 2000}; p.WaitsMS[0] != want[0] || p.WaitsMS[1] != want[1] || p.WaitsMS[2] != want[2] || p.WaitsMS[3] != want[3] {
		t.Errorf("waits = %v ms, want %v", p.WaitsMS, want)
	}
	if !p.PeriodEstimate {
		t.Errorf("period_estimate false, a DB period is an estimate")
	}
	if math.Abs(float64(p.PeriodS)-68) > 0.01 {
		t.Errorf("period = %g s, want 68 (64 yd/s at 2.5 yd/s + 4 s of waittime)", p.PeriodS)
	}
	if len(p.SpawnGUIDs) != 1 || p.SpawnGUIDs[0] != 500 || len(p.Entries) != 1 || p.Entries[0] != 100 {
		t.Errorf("spawn guids %v entries %v, want [500] [100]", p.SpawnGUIDs, p.Entries)
	}
}

// TestPatrolSpeedScalesTheLoop pins speed_walk: a creature walking at 2 yd over the 160 yd square takes 32 s.
func TestPatrolSpeedScalesTheLoop(t *testing.T) {
	dir := writeExport(t, map[string]string{
		"creatures.tsv": newHeader +
			spawnRowOpts(501, 100, "Fast", 0, 0, 0, 2, 0, 0, 20, 0, 2, 0, 0),
		"movement.tsv": squareMovement(501, [4]int{}),
	})
	w, _, err := Load(dir, 700)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if len(w.Patrols) != 1 || math.Abs(float64(w.Patrols[0].PeriodS)-32) > 0.01 {
		t.Fatalf("patrols = %+v, want one with a 32 s period", w.Patrols)
	}
}

// TestTemplateMovementUsedWhenNoGuidRows pins the fallback: a patrolling spawn with no creature_movement rows walks its entry's
// creature_movement_template, and all spawns of that entry share the one loop.
func TestTemplateMovementUsedWhenNoGuidRows(t *testing.T) {
	dir := writeExport(t, map[string]string{
		"creatures.tsv": newHeader +
			spawnRow(600, 200, "Template", 0, 0, 0, 2) + "\n" +
			spawnRow(601, 200, "Template", 0, 40, 0, 2),
		"movement_template.tsv": "entry\tpoint\tx\ty\tz\twaittime\n" +
			join(200, 1, 0, 0, 0, 0) +
			join(200, 2, 40, 0, 0, 0) +
			join(200, 3, 40, 40, 0, 0) +
			join(200, 4, 0, 40, 0, 0),
	})
	w, _, err := Load(dir, 700)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if len(w.Patrols) != 1 {
		t.Fatalf("%d patrols, want one loop per entry", len(w.Patrols))
	}
	p := w.Patrols[0]
	if p.Source != "creature_movement_template 200" {
		t.Errorf("source %q, want creature_movement_template 200", p.Source)
	}
	if len(p.SpawnGUIDs) != 2 || p.SpawnGUIDs[0] != 600 || p.SpawnGUIDs[1] != 601 {
		t.Errorf("spawn guids %v, want both spawns of the entry", p.SpawnGUIDs)
	}
	if math.Abs(float64(p.PeriodS)-64) > 0.01 {
		t.Errorf("period = %g s, want 64", p.PeriodS)
	}
}

// TestFormationFollowersJoinPatrol pins that a formation follower (groups flags 0x1) is listed in its leader's patrol, though it
// has no waypoints of its own.
func TestFormationFollowersJoinPatrol(t *testing.T) {
	dir := writeExport(t, map[string]string{
		"creatures.tsv": newHeader +
			spawnRowOpts(700, 300, "Leader", 0, 0, 0, 2, 0, 0, 20, 0, 1, 0, 0) + "\n" +
			spawnRowOpts(701, 301, "Follower", 3, 0, 0, 0, 700, 0, 20, 0, 1, FormationMove, 0),
		"groups.tsv": "leader_guid\tmember_guid\tdist\tangle\tflags\n" +
			"700\t701\t3\t0\t1\n",
		"movement.tsv": squareMovement(700, [4]int{}),
	})
	w, _, err := Load(dir, 700)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if len(w.Patrols) != 1 {
		t.Fatalf("%d patrols, want 1", len(w.Patrols))
	}
	p := w.Patrols[0]
	if p.ID != "700" {
		t.Errorf("id %q, want the leader's spawn guid", p.ID)
	}
	if len(p.SpawnGUIDs) != 2 || p.SpawnGUIDs[0] != 700 || p.SpawnGUIDs[1] != 701 {
		t.Errorf("spawn guids %v, want [700 701]", p.SpawnGUIDs)
	}
	wantGUIDs := []string{"0xF13000012C0002BC", "0xF13000012D0002BD"}
	if len(p.GUIDs) != 2 || p.GUIDs[0] != wantGUIDs[0] || p.GUIDs[1] != wantGUIDs[1] {
		t.Errorf("guids %v, want %v", p.GUIDs, wantGUIDs)
	}
	if len(p.Entries) != 2 || p.Entries[0] != 300 || p.Entries[1] != 301 {
		t.Errorf("entries %v, want [300 301]", p.Entries)
	}
}
