package worldspawn

import (
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

// oldExport is a creatures.tsv from before export.sh wrote the raid columns (tools/dungeongen/testdata/map249 before C0).
const oldHeader = "guid\tentry\tname\tlevel_min\tlevel_max\trank\tfaction\tnpc_flags\tx\ty\tz\twander\tmovement_type\tgroup_leader\tpatrol\tauras\n"

// newHeader is the creatures.tsv C0 writes: the five raid columns appended at the end.
var newHeader = strings.TrimSuffix(oldHeader, "\n") + "\tdetection_range\tflags_extra\tspeed_walk\tgroup_flags\tpool\n"

// writeExport writes a synthetic export directory: each file's content after the file name.
func writeExport(t *testing.T, files map[string]string) string {
	t.Helper()
	dir := t.TempDir()
	for name, body := range files {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o600); err != nil {
			t.Fatalf("write %s: %v", name, err)
		}
	}
	return dir
}

// spawnRow is one new-format creatures.tsv row with the usual filler (level 60, rank 1, faction 103, no flags).
func spawnRow(guid uint64, entry uint32, name string, x, y, z float32, movementType int) string {
	return spawnRowOpts(guid, entry, name, x, y, z, movementType, 0, 0, 20, 0, 1, 0, 0)
}

func spawnRowOpts(guid uint64, entry uint32, name string, x, y, z float32, movementType int, groupLeader uint64, wander, detection float32, flagsExtra uint32, speedWalk float32, groupFlags uint32, pool uint32) string {
	return join(guid, entry, name, 60, 60, 1, 103, 0, x, y, z, wander, movementType, groupLeader, 1, "", detection, flagsExtra, speedWalk, groupFlags, pool)
}

// join formats the creatures.tsv columns as a tab-separated row.
func join(vals ...any) string {
	parts := make([]string, len(vals))
	for i, v := range vals {
		switch x := v.(type) {
		case string:
			parts[i] = x
		case float32:
			parts[i] = strconv.FormatFloat(float64(x), 'f', -1, 32)
		default:
			parts[i] = fmt.Sprint(x)
		}
	}
	return strings.Join(parts, "\t") + "\n"
}

// byGUID finds a spawn by its guid.
func byGUID(t *testing.T, w *World, guid uint64) Spawn {
	t.Helper()
	for _, s := range w.Spawns {
		if s.GUID == guid {
			return s
		}
	}
	t.Fatalf("no spawn with guid %d", guid)
	return Spawn{}
}

// TestLoadOldExportAssumesDefaults pins what Load does with an export from before C0 (testdata/map249-old: map 249 with the
// five raid columns cut and no movement.tsv; the shared dungeongen fixture is a new export now): the five raid columns are missing,
// so every spawn gets detection 20 yd, no extra flags, speed 1, no group flags and no pool, and a note says so.
func TestLoadOldExportAssumesDefaults(t *testing.T) {
	w, notes, err := Load(filepath.Join("testdata", "map249-old"), 249)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if w.Map != 249 {
		t.Errorf("map = %d, want 249", w.Map)
	}
	if len(w.Spawns) != 6 {
		t.Fatalf("%d spawns, want 6", len(w.Spawns))
	}
	for _, s := range w.Spawns {
		if s.DetectionYd != 20 {
			t.Errorf("spawn %d: detection %g, want the 20 yd default", s.GUID, s.DetectionYd)
		}
		if s.FlagsExtra != 0 || s.SpeedWalk != 1 || s.GroupFlags != 0 || s.Pool != 0 {
			t.Errorf("spawn %d: flags_extra %#x speed %g group_flags %#x pool %d, want the defaults", s.GUID, s.FlagsExtra, s.SpeedWalk, s.GroupFlags, s.Pool)
		}
	}
	joined := strings.Join(notes, "\n")
	if !strings.Contains(joined, "detection_range") || !strings.Contains(joined, "20") {
		t.Errorf("notes do not say the detection range default was assumed:\n%s", joined)
	}
	if !strings.Contains(joined, "movement.tsv") {
		t.Errorf("notes do not say movement.tsv is missing:\n%s", joined)
	}
	ony := byGUID(t, w, 47572)
	if ony.Entry != 10184 || ony.Name != "Onyxia" || ony.LevelMax != 63 || ony.Rank != 3 {
		t.Errorf("Onyxia row = %+v", ony)
	}
	warder := byGUID(t, w, 52048)
	if warder.Entry != 12129 || warder.MovementType != 2 || warder.Wander != 5 {
		t.Errorf("warder row = %+v", warder)
	}
}

// TestLoadReadsTheNewColumns pins the appended columns C0 writes, and that an old export's default is only a default.
func TestLoadReadsTheNewColumns(t *testing.T) {
	dir := writeExport(t, map[string]string{
		"creatures.tsv": newHeader +
			spawnRowOpts(1, 100, "Guard", 1, 2, 3, 0, 0, 4, 15, NoAssist, 1.6, 3, 7) + "\n" +
			join(2, 101, "Idle", 4, 5, 6, 60, 60, 1, 103, 0, 0, 0, 0, 1, "", 30, NoAggro, 0.8, 0, 0),
	})
	w, _, err := Load(dir, 700)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	g := byGUID(t, w, 1)
	if g.Pos.X != 1 || g.Pos.Y != 2 || g.Pos.Z != 3 || g.Wander != 4 || g.DetectionYd != 15 || g.FlagsExtra != NoAssist ||
		g.SpeedWalk != 1.6 || g.GroupFlags != 3 || g.Pool != 7 {
		t.Errorf("spawn 1 = %+v", g)
	}
	i := byGUID(t, w, 2)
	if i.DetectionYd != 30 || i.FlagsExtra != NoAggro || i.SpeedWalk != 0.8 {
		t.Errorf("spawn 2 = %+v", i)
	}
}

// TestAggroRadiusMatchesRunLeader pins AggroRadius against the run leader's ai::AggroRadius (TortoiseBots
// PullAggroEstimatePolicy.h), which is Creature::GetAttackDistance with the Rate.Creature.Aggro factor.
func TestAggroRadiusMatchesRunLeader(t *testing.T) {
	cases := []struct {
		name  string
		s     Spawn
		party int
		rate  float32
		want  float32
	}{
		{"two levels under", Spawn{LevelMax: 62, DetectionYd: 20}, 60, 1, 22},
		{"level difference floors at -25", Spawn{LevelMax: 60, DetectionYd: 20}, 30, 1, 45},
		{"small detection is its own floor", Spawn{LevelMax: 60, DetectionYd: 3}, 60, 1, 3},
		{"level bonus floors at min(detection, 5)", Spawn{LevelMax: 40, DetectionYd: 20}, 60, 1, 5},
		{"no aggro flag never wakes", Spawn{LevelMax: 60, DetectionYd: 20, FlagsExtra: NoAggro}, 60, 1, 0},
		{"detection under a yard never wakes", Spawn{LevelMax: 60, DetectionYd: 0.5}, 60, 1, 0},
		{"aggro rate multiplies", Spawn{LevelMax: 62, DetectionYd: 20}, 60, 0.5, 11},
	}
	for _, c := range cases {
		if got := AggroRadius(c.s, c.party, c.rate); got != c.want {
			t.Errorf("%s: AggroRadius(%+v, %d, %g) = %g, want %g", c.name, c.s, c.party, c.rate, got, c.want)
		}
	}
}

// TestHostileFilter pins the hostile rule (dungeongen's: faction 35 and npc_flags 0 are not hostile), and that NO_AGGRO
// spawns are still listed.
func TestHostileFilter(t *testing.T) {
	dir := writeExport(t, map[string]string{
		"creatures.tsv": newHeader +
			spawnRow(1, 100, "Hostile", 0, 0, 0, 0) + "\n" +
			join(2, 101, "Vendor", 60, 60, 0, 103, 1, 0, 0, 0, 0, 0, 0, 1, "", 20, 0, 1, 0, 0) + "\n" +
			join(3, 102, "Friendly", 60, 60, 0, 35, 0, 0, 0, 0, 0, 0, 0, 1, "", 20, 0, 1, 0, 0) + "\n" +
			join(4, 103, "Flat", 60, 60, 1, 103, 0, 0, 0, 0, 0, 0, 0, 1, "", 20, NoAggro, 1, 0, 0),
	})
	w, _, err := Load(dir, 700)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if len(w.Spawns) != 4 {
		t.Fatalf("%d spawns listed, want all 4", len(w.Spawns))
	}
	want := map[uint64]bool{1: true, 2: false, 3: false, 4: true}
	for guid, hostile := range want {
		if got := byGUID(t, w, guid).Hostile(); got != hostile {
			t.Errorf("spawn %d: Hostile() = %v, want %v", guid, got, hostile)
		}
	}
	if r := AggroRadius(byGUID(t, w, 4), 60, 1); r != 0 {
		t.Errorf("NO_AGGRO spawn radius %g, want 0", r)
	}
}
