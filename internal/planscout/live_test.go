package planscout

import (
	"strings"
	"testing"

	"github.com/pjerra/turtle-agent/internal/dungeon"
	"github.com/pjerra/turtle-agent/internal/navmesh"
	"github.com/pjerra/turtle-agent/internal/objects"
	"github.com/pjerra/turtle-agent/internal/packet"
	"github.com/pjerra/turtle-agent/internal/planexport"
	"github.com/pjerra/turtle-agent/internal/worldspawn"
)

// unitGUID builds the creature GUID the toolkit's `db` guid and entry encode (ObjectGuid: entry << 24 | counter).
func unitGUID(entry, low uint32) packet.GUID {
	return packet.GUID(0xF130)<<48 | packet.GUID(entry)<<24 | packet.GUID(low)
}

// zgF23Rows are walk-31 f steps 21-23 (the stand 18.3 yd above its anchor, spec test 1).
func zgF23Rows() planexport.Rows {
	pull := mkPull(-12006.8, -1486.9, 80.5, 6.3, 11351, 11831)
	pull.Flags = planexport.FlagStandPull
	return rowsOf(move(-11994.4, -1510.7, 57.3), move(-12028, -1510, 75.6), pull)
}

func zgF23Origins() []planexport.RowOrigin {
	return []planexport.RowOrigin{{Leg: 0, Part: "via", Group: -1, Index: 0}, {Leg: 0, Part: "to", Group: -1, Index: 0},
		{Leg: 0, Part: "pull", Group: 0, Index: 0}}
}

func mergeOn(t *testing.T, rows planexport.Rows, origins []planexport.RowOrigin, w *worldspawn.World, live LiveData) *Report {
	t.Helper()
	in := Input{Rows: rows, Origins: origins, Plan: &dungeon.Plan{Name: "t", Map: 1, Kind: "raid"}}
	rep := run(t, rows, fakeNav{}, w, origins)
	MergeLive(rep, in, w, live, Options{})
	return rep
}

// 2. The live walk of the f stand hop stalls on the slope: 23.2 yd short (the spec's rows put the stop 23.2 yd from the
// stand: 20.5 yd 2D and 10.9 yd below); a stop within 8 yd of the anchor whose way back is NOPATH is CheckAnchor Off.
func TestLiveWalkStallF23(t *testing.T) {
	rows, origins := zgF23Rows(), zgF23Origins()
	rep := mergeOn(t, rows, origins, nil, LiveData{Walks: map[int]WalkResult{2: {Stop: dungeon.Point{X: -12007.5, Y: -1510.4, Z: 64.7}}}})
	c := checkAt(t, rep, 2, CheckLiveWalk)
	if c == nil || c.Verdict != VerdictFail || !strings.Contains(c.Reason, "23.2 yd short") {
		t.Fatalf("step 2 live_walk: %+v", c)
	}
	// The stop 7.8 yd above the anchor, with the walk back NOPATH: CheckAnchor Off whatever the distance.
	back := &ScoutPath{Verdict: "unreachable", PathType: navmesh.PathNoPath}
	rep = mergeOn(t, rows, origins, nil, LiveData{Walks: map[int]WalkResult{2: {
		Stop: dungeon.Point{X: -12028, Y: -1510, Z: 57.3 + 7.8}, Back: back}}})
	c = checkAt(t, rep, 2, CheckLiveWalk)
	if c == nil || c.Verdict != VerdictFail || !strings.Contains(c.Reason, "CheckAnchor Off") {
		t.Fatalf("step 2 anchor: %+v", c)
	}
}

// 4 (live). A swim-line answer (0x81) on a stand is a FAIL; on a plain move only a WARN.
func TestLivePathBothEndsSwimmableA2(t *testing.T) {
	rows := rowsOf(move(-100, 0, 0), move(-50, 0, 0), move(-11930, -1340, 65),
		move(-11945.7, -1358.2, 64.6),
		func() planexport.StepRow {
			s := mkPull(-11940, -1400, 68, 6, 11372)
			s.Flags = planexport.FlagStandPull
			return s
		}())
	origins := []planexport.RowOrigin{{Part: "via", Group: -1}, {Part: "to", Group: -1}, {Part: "path", Group: -1},
		{Part: "approach", Group: 0, Index: 0}, {Part: "pull", Group: 0, Index: 0}}
	swim := ScoutPath{Verdict: "ok", PathType: 0x81, StartSwim: true, EndSwim: true}
	rep := mergeOn(t, rows, origins, nil, LiveData{Paths: map[int]ScoutPath{4: swim, 2: swim}})
	// Step 4 is the stand row: FAIL.
	c := checkAt(t, rep, 4, CheckLivePath)
	if c == nil || c.Verdict != VerdictFail || !strings.Contains(c.Reason, "0x81") {
		t.Fatalf("step 4 live_path: %+v", c)
	}
	// Step 2 is a plain move: WARN.
	c = checkAt(t, rep, 2, CheckLivePath)
	if c == nil || c.Verdict != VerdictWarn || !strings.Contains(c.Reason, "0x81") {
		t.Fatalf("step 2 live_path: %+v", c)
	}
	// An approach end with no pull after it (the last approach row of its group) is special too.
	plain := rowsOf(move(-100, 0, 0), move(-50, 0, 0), move(-11951.0, -1393.5, 68.1))
	pad := []planexport.RowOrigin{{Part: "via", Group: -1}, {Part: "to", Group: -1}, {Part: "approach", Group: 0, Index: 0}}
	rep = mergeOn(t, plain, pad, nil, LiveData{Paths: map[int]ScoutPath{3: swim}})
	if c := checkAt(t, rep, 3, CheckLivePath); c == nil || c.Verdict != VerdictFail {
		t.Fatalf("approach end: %+v", c)
	}
}

// 12. A live hostile within reach + spread out of sight only warns; in sight it fails.
func TestLiveAggroNeedsLOS(t *testing.T) {
	rows := rowsOf(move(-20, 0, 0), move(0, 0, 0), mkPull(10, 0, 0, 5, 100))
	w := &worldspawn.World{Spawns: []worldspawn.Spawn{
		{GUID: 500, Entry: 100, LevelMax: 60, DetectionYd: 20, Faction: 14},
		{GUID: 600, Entry: 200, LevelMax: 60, DetectionYd: 20, Faction: 14},
	}}
	u := objects.Unit{GUID: unitGUID(200, 600), Entry: 200, Name: "Stranger", X: 3, Y: 0, Z: 0, Level: 60, Hostile: true}
	rep := mergeOn(t, rows, nil, w, LiveData{
		Units: map[int][]objects.Unit{2: {u}},
		LOS:   map[LOSKey]bool{{Step: 2, Guid: 600}: false},
	})
	c := checkAt(t, rep, 2, CheckLiveAggro)
	if c == nil || c.Verdict != VerdictWarn || !strings.Contains(c.Reason, "out of sight") {
		t.Fatalf("out of sight: %+v", c)
	}
	rep = mergeOn(t, rows, nil, w, LiveData{
		Units: map[int][]objects.Unit{2: {u}},
		LOS:   map[LOSKey]bool{{Step: 2, Guid: 600}: true},
	})
	c = checkAt(t, rep, 2, CheckLiveAggro)
	if c == nil || c.Verdict != VerdictFail || !strings.Contains(c.Reason, "in sight") {
		t.Fatalf("in sight: %+v", c)
	}
}

// A unit the pull itself owns is not an aggro finding (it is the pack being pulled).
func TestLiveAggroSkipsOwnedPack(t *testing.T) {
	rows := rowsOf(move(-20, 0, 0), move(0, 0, 0), mkPull(10, 0, 0, 5, 100))
	w := &worldspawn.World{Spawns: []worldspawn.Spawn{
		{GUID: 500, Entry: 100, LevelMax: 60, DetectionYd: 20, Faction: 14, Pos: dungeon.Point{X: 10}},
	}}
	u := objects.Unit{GUID: unitGUID(100, 500), Entry: 100, X: 10, Y: 0, Z: 0, Level: 60, Hostile: true}
	rep := mergeOn(t, rows, nil, w, LiveData{Units: map[int][]objects.Unit{2: {u}},
		LOS: map[LOSKey]bool{{Step: 2, Guid: 500}: true}})
	c := checkAt(t, rep, 2, CheckLiveAggro)
	if c == nil || c.Verdict != VerdictOK {
		t.Fatalf("owned pack: %+v", c)
	}
}

// The spot list is every stand, gather anchor and pull row; the stops group them within 100 yd.
func TestLiveSpotsAndStops(t *testing.T) {
	rows := zgF23Rows()
	rep := run(t, rows, fakeNav{}, nil, zgF23Origins())
	spots := LiveSpots(rep, Input{Rows: rows, Plan: &dungeon.Plan{Name: "t", Map: 1, Kind: "raid"}}, nil, nil)
	kinds := map[string]int{}
	for _, s := range spots {
		kinds[s.Kind]++
	}
	if kinds["stand"] != 1 || kinds["anchor"] != 1 || kinds["pull"] != 1 {
		t.Fatalf("spots: %+v", spots)
	}
	groups := LiveStops(spots)
	if len(groups) != 1 {
		t.Fatalf("one stop: %d groups", len(groups))
	}
	// A spot 150 yd on starts a second stop.
	groups = LiveStops([]LiveSpot{{Step: 1, Point: dungeon.Point{}}, {Step: 2, Point: dungeon.Point{X: 150}}})
	if len(groups) != 2 {
		t.Fatalf("two stops: %d groups", len(groups))
	}
}

// Every hop of 3.2 is asked, the plan hop first; the walk-31 stand row names its anchor.
func TestLiveHopsCoverOrigins(t *testing.T) {
	rows := zgF23Rows()
	rep := run(t, rows, fakeNav{}, nil, zgF23Origins())
	hops := LiveHops(rep, Input{Rows: rows, Plan: &dungeon.Plan{Name: "t", Map: 1, Kind: "raid"}}, fakeNav{})
	var stand *LiveHop
	for i := range hops {
		if hops[i].Step == 2 && hops[i].Primary {
			stand = &hops[i]
		}
	}
	if stand == nil || !stand.StandRow || stand.Anchor.Z != 57.3 || stand.From.Z != 57.3 {
		t.Fatalf("stand hop: %+v", hops)
	}
}
