package tbm

import "testing"

// The lines are RemoteCommanderPolicy.h's own output for one scout reply each (spec 4.2).
func TestParseScoutLines(t *testing.T) {
	cfg, ok := ParseScoutCfg("TBM:SCOUT_CFG|7|150.00|1.00|10.00")
	if !ok || cfg.ID != 7 || cfg.ReactDistance != 150 || cfg.RateCreatureAggro != 1 || cfg.FamilyAssistRadius != 10 {
		t.Fatalf("cfg: %+v %v", cfg, ok)
	}
	p, ok := ParseScoutPath("TBM:SCOUT_PATH|12|ok|0x81|42.50|-11945.70 -1393.50 68.10|0.00|68.10|1|1")
	if !ok || p.ID != 12 || p.Verdict != ScoutVerdictOK || p.PathType != 0x81 || p.Length != 42.5 ||
		p.End != [3]float32{-11945.7, -1393.5, 68.1} || !p.StartSwim || !p.EndSwim || !p.Underwater() {
		t.Fatalf("path: %+v %v", p, ok)
	}
	bad, ok := ParseScoutPath("TBM:SCOUT_PATH|3|off-mesh|0x1|7.25|1.00 2.00 3.00|5.10|3.00|0|1")
	if !ok || bad.Verdict != ScoutVerdictOffMesh || bad.EndOffset != 5.1 || bad.StartSwim || !bad.EndSwim || bad.Underwater() {
		t.Fatalf("off-mesh path: %+v %v", bad, ok)
	}
	w, ok := ParseScoutWalk("TBM:SCOUT_WALK|4|started|0x1")
	if !ok || w.ID != 4 || w.PathType != 1 {
		t.Fatalf("walk: %+v %v", w, ok)
	}
	pos, ok := ParseScoutPos("TBM:SCOUT_POS|9|-12007.50 -1510.40 64.70|1|0|1")
	if !ok || pos.ID != 9 || pos.X != -12007.5 || pos.Z != 64.7 || !pos.Moving || pos.InWater || !pos.Swimming {
		t.Fatalf("pos: %+v %v", pos, ok)
	}
}

func TestParseScoutRejects(t *testing.T) {
	paths := []string{
		"TBM:SCOUT_PATH|0|ok|0x1|1.00|1.00 2.00 3.00|0.00|3.00|0|0",      // id 0
		"TBM:SCOUT_PATH|1|maybe|0x1|1.00|1.00 2.00 3.00|0.00|3.00|0|0",   // unknown verdict
		"TBM:SCOUT_PATH|1|ok|1|1.00|1.00 2.00 3.00|0.00|3.00|0|0",        // type without 0x
		"TBM:SCOUT_PATH|1|ok|0x1|NaN|1.00 2.00 3.00|0.00|3.00|0|0",       // NaN
		"TBM:SCOUT_PATH|1|ok|0x1|1.00|1.00 2.00|0.00|3.00|0|0",           // two coordinates
		"TBM:SCOUT_PATH|1|ok|0x1|1.00|1.00 2.00 3.00 4.00|0.00|3.00|0|0", // four coordinates
		"TBM:SCOUT_PATH|1|ok|0x1|1.00|1.00 2.00 3.00|0.00|3.00|0",        // a field short
		"TBM:SCOUT_PATH|1|ok|0x1|1.00|1.00 2.00 3.00|0.00|3.00|2|0",      // flag 2
	}
	for _, line := range paths {
		if _, ok := ParseScoutPath(line); ok {
			t.Fatalf("accepted %q", line)
		}
	}
	for _, line := range []string{
		"TBM:SCOUT_CFG|1|150.00|1.00",
		"TBM:SCOUT_CFG|1234567890|150.00|1.00|10.00",
		"TBM:SCOUT_CFG|1|Inf|1.00|10.00",
	} {
		if _, ok := ParseScoutCfg(line); ok {
			t.Fatalf("accepted %q", line)
		}
	}
	for _, line := range []string{
		"TBM:SCOUT_WALK|1|done|0x1",
		"TBM:SCOUT_WALK|1|started|1",
	} {
		if _, ok := ParseScoutWalk(line); ok {
			t.Fatalf("accepted %q", line)
		}
	}
	for _, line := range []string{
		"TBM:SCOUT_POS|1|1.00 2.00 3.00|1|0",
		"TBM:SCOUT_POS|1|1.00 2.00 3.00|1|0|1|0",
	} {
		if _, ok := ParseScoutPos(line); ok {
			t.Fatalf("accepted %q", line)
		}
	}
}

// The verdict words must be the module's MoveVerdictCode words, byte for byte.
func TestScoutVerdictWords(t *testing.T) {
	words := []string{ScoutVerdictOK, ScoutVerdictTooFar, ScoutVerdictUnreachable, ScoutVerdictOffMesh}
	want := []string{"ok", "too-far", "unreachable", "off-mesh"}
	for i := range words {
		if words[i] != want[i] {
			t.Fatalf("verdict %d: %q, want %q", i, words[i], want[i])
		}
	}
}
