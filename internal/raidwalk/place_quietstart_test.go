package raidwalk

import (
	"path/filepath"
	"strings"
	"testing"

	"github.com/pjerra/turtle-agent/internal/dungeon"
	"github.com/pjerra/turtle-agent/internal/planexport"
	"github.com/pjerra/turtle-agent/internal/worldspawn"
)

// quietStartRows is four move rows on +y (steps 1-4) and the pull that the run has not reached yet (step 5). The pull
// owns spawn 100 at (0,105,0): its reach is radius+5 = 15, so the 105 pack lies inside it.
func quietStartRows() planexport.Rows {
	return placeRows(nil,
		placeMove(0, 0, 0, 0),
		placeMove(0, 0, 40, 0),
		placeMove(0, 0, 80, 0),
		placeMove(0, 0, 100, 0),
		placeRow(planexport.KindPull, 0, 0, 110, 0, 10, 100),
	)
}

// quietStartWorld is one static hostile spawn at (0,105,0): level 60 against a level-60 party, detection 20, so the
// placement needs 20 + PlaceQuietGap = 28 yd.
func quietStartWorld(movementType int) *worldspawn.World {
	return &worldspawn.World{Map: 309, Spawns: []worldspawn.Spawn{{
		GUID: 1, Entry: 100, Name: "Hakkari Priest", LevelMax: 60, Rank: 1, Faction: 16,
		Pos: dungeon.Point{X: 0, Y: 105, Z: 0}, MovementType: movementType, DetectionYd: 20,
	}}}
}

func TestPlaceQuietStartWalksBackToAQuietMoveRow(t *testing.T) {
	rows := quietStartRows()
	got, note := PlaceQuietStart(rows, quietStartWorld(0), 4, 60)
	if got != 2 {
		t.Fatalf("start %d, want step 2 (the nearest move row 28 yd clear)", got)
	}
	if !strings.Contains(note, "step 4") || !strings.Contains(note, "step 2") || !strings.Contains(note, "Hakkari Priest") {
		t.Fatalf("note %q, want the refused row, the chosen row and the spawn", note)
	}
}

func TestPlaceQuietStartKeepsAClearStart(t *testing.T) {
	// The spawn is a patroller: it never counts as a static spawn of the pack, so the requested start stands.
	got, note := PlaceQuietStart(quietStartRows(), quietStartWorld(2), 4, 60)
	if got != 4 {
		t.Fatalf("start %d, want the requested step 4 (a patrolling spawn is not static)", got)
	}
	if !strings.Contains(note, "no static spawn") {
		t.Fatalf("note %q, want the no-static-spawn reason", note)
	}
}

func TestPlaceQuietStartRefusesWithoutAWorld(t *testing.T) {
	got, note := PlaceQuietStart(quietStartRows(), nil, 4, 60)
	if got != 4 || note != "" {
		t.Fatalf("start %d note %q, want the requested step and no note", got, note)
	}
}

func TestPlaceQuietStartSaysWhenNoEarlierMoveRowIsQuiet(t *testing.T) {
	// Every earlier move row stands at 100, 99, 98: all within 28 yd of the spawn at 105.
	rows := placeRows(nil,
		placeMove(0, 0, 100, 0),
		placeMove(0, 0, 99, 0),
		placeMove(0, 0, 98, 0),
		placeMove(0, 0, 100, 0),
		placeRow(planexport.KindPull, 0, 0, 110, 0, 10, 100),
	)
	got, note := PlaceQuietStart(rows, quietStartWorld(0), 4, 60)
	if got != 4 {
		t.Fatalf("start %d, want the requested step 4 when no earlier row is quiet", got)
	}
	if !strings.Contains(note, "no quiet move row") {
		t.Fatalf("note %q, want the no-quiet-row reason", note)
	}
}

func TestPlaceBackIsThePreviousMoveRow(t *testing.T) {
	rows := placeRows(nil,
		placeMove(0, 1, 0, 0),
		placeRow(planexport.KindPull, 0, 2, 0, 0, 1, 1),
		placeMove(0, 3, 0, 0),
		placeMove(0, 4, 0, 0),
	)
	got, ok := PlaceBack(rows, 4)
	if !ok || got != 3 {
		t.Fatalf("back %d %v, want 3", got, ok)
	}
	if _, ok := PlaceBack(rows, 1); ok {
		t.Fatal("step 1 has a move row before it")
	}
	// Step 2 is a pull row: the previous row is a move, the next one back is not.
	got, ok = PlaceBack(rows, 3)
	if !ok || got != 1 {
		t.Fatalf("back from 3: %d %v, want 1", got, ok)
	}
}

// Walk 36 b: a start placed at leg high_priestess_arlokk refused the quiet circle at the leg's first row and the quiet
// retreat stepped one move row back, over Thekal's boss row, to his approach at step 271; --from was rewritten to
// "from step 271", the run started there and fought Thekal again (boss 14599 engaged at step 271, 2 deaths, 166 s).
// PlaceBackInLeg keeps a retreat inside the leg and never across a boss or hold row.
func TestPlaceBackInLegStopsAtABossRowAndAtTheLeg(t *testing.T) {
	rows := placeRows(nil,
		placeMove(3, 1, 10, 0),                           // leg 3: the earlier leg
		placeRow(planexport.KindBoss, 3, 2, 10, 0, 1, 1), // its boss row
		placeMove(4, 3, 10, 0),                           // leg 4: the start's own leg, first row
		placeMove(4, 4, 10, 0),                           // and the row behind the start
	)
	// A start at leg 4's first row has no retreat: step 3 is the boss row, and step 2 is another leg.
	if got, ok := PlaceBackInLeg(rows, 3, "4"); ok {
		t.Fatalf("a retreat from leg 4's first row went to %d: it would start the run at the boss row before it", got)
	}
	// Inside the leg the row behind is the retreat.
	if got, ok := PlaceBackInLeg(rows, 4, "4"); !ok || got != 3 {
		t.Fatalf("retreat from 4 in leg 4: %d %v, want 3", got, ok)
	}
	// An explicit-step start (--from step N) names no leg: the retreat stays in the row's own leg.
	if got, ok := PlaceBackInLeg(rows, 2, ""); ok {
		t.Fatalf("a retreat from the boss row (step 2) went to %d", got)
	}
	if got, ok := PlaceBackInLeg(rows, 4, ""); !ok || got != 3 {
		t.Fatalf("retreat from 4 with no leg name: %d %v, want 3 (leg 4's own row)", got, ok)
	}
	// The pure finder still crosses it (walk-super's restart is not the quiet retreat).
	if got, ok := PlaceBack(rows, 3); !ok || got != 1 {
		t.Fatalf("PlaceBack from 3: %d %v, want 1", got, ok)
	}
}

// Walk 46 Jin'do: the boss stands alone and no pack owns it, so the offline check never saw it; the autopilot's
// resume start (the last approach row) stood 25.2 yd from his spawn and pulled him. The leg's boss row is a hazard.
func quietStartBossRows() planexport.Rows {
	boss := placeRow(planexport.KindBoss, 0, 0, 125.2, 0, 0, 11380)
	boss.Entry = 11380
	return placeRows(nil,
		placeMove(0, 0, 10, 0),
		placeMove(0, 0, 40, 0),
		placeMove(0, 0, 100, 0),
		boss,
	)
}

func TestPlaceQuietStartMovesBackFromABossInReach(t *testing.T) {
	// The world holds no spawn of the boss: the row's own entry and position are the hazard (detection 20 + gap 8).
	rows := quietStartBossRows()
	got, note := PlaceQuietStart(rows, &worldspawn.World{Map: 309}, 3, 60)
	if got != 2 {
		t.Fatalf("start %d, want step 2 (step 3 stands 25.2 yd from the boss, step 2 85.2)", got)
	}
	if !strings.Contains(note, "11380") || !strings.Contains(note, "step 3") || !strings.Contains(note, "step 2") {
		t.Fatalf("note %q, want the boss entry, the refused row and the chosen row", note)
	}
}

func TestPlaceQuietStartKeepsAStartFarFromTheBoss(t *testing.T) {
	rows := quietStartBossRows()
	rows.Steps[3].Y = 169 // the boss is 69 yd from step 3
	got, note := PlaceQuietStart(rows, &worldspawn.World{Map: 309}, 3, 60)
	if got != 3 {
		t.Fatalf("start %d, want the requested step 3 (the boss is 69 yd away): %s", got, note)
	}
}

func TestPlaceQuietStartUsesTheBossSpawnsOwnStats(t *testing.T) {
	// A boss spawn in the export keeps its own detection range: 30 yd at the party's level needs 30 + 8 yd.
	rows := quietStartBossRows()
	rows.Steps[3].Y = 135 // 35 yd from step 3: clear at 28, inside 38
	world := &worldspawn.World{Map: 309, Spawns: []worldspawn.Spawn{{
		GUID: 9, Entry: 11380, Name: "Jin'do the Hexxer", LevelMax: 60, Rank: 3, Faction: 16,
		Pos: dungeon.Point{X: 0, Y: 135, Z: 0}, DetectionYd: 30,
	}}}
	got, _ := PlaceQuietStart(rows, world, 3, 60)
	if got != 2 {
		t.Fatalf("start %d, want step 2 (the spawn's detection range is 30)", got)
	}
}

// The boss hazard is the NEXT boss row of the start's own leg, not every later boss of the plan: a row that stands
// near a later boss (LBRS, Scholomance, MC, UBRS, Windhorn) is a fine start for the leg it belongs to.
func TestPlaceQuietStartJudgesOnlyTheNextBossOfItsLeg(t *testing.T) {
	boss := func(leg int, y float32, entry uint32) planexport.StepRow {
		b := placeRow(planexport.KindBoss, leg, 0, y, 0, 0, entry)
		b.Entry = entry
		return b
	}
	legs := []planexport.LegRow{{Plan: "t", Leg: 0, Name: "one"}, {Plan: "t", Leg: 1, Name: "two"}}
	rows := placeRows(legs,
		placeMove(0, 0, 10, 0),
		placeMove(0, 0, 100, 0), // 2: 20 yd from the later boss of leg 1, 100 from its own boss
		boss(0, 200, 111),       // 3
		placeMove(1, 0, 300, 0), // 4
		boss(1, 120, 222),       // 5: a later leg's boss that stands near step 2
	)
	got, note := PlaceQuietStart(rows, &worldspawn.World{Map: 309}, 2, 60)
	if got != 2 {
		t.Fatalf("start %d, want 2 (boss 222 belongs to a later leg): %s", got, note)
	}
	if strings.Contains(note, "222") {
		t.Fatalf("note %q names the later leg's boss", note)
	}
}

// The walk-back stops at a boss or hold row, and at the leg's start: a start moved behind one would have the raid
// fight it again (PlaceBackInLeg's rule).
func TestPlaceQuietStartDoesNotCrossABossOrHoldRow(t *testing.T) {
	boss := placeRow(planexport.KindBoss, 0, 0, 60, 0, 0, 111)
	boss.Entry = 111
	hold := placeRow(planexport.KindHold, 0, 0, 60, 0, 0, 333)
	hold.Entry = 333
	next := placeRow(planexport.KindBoss, 0, 0, 125.2, 0, 0, 222)
	next.Entry = 222
	for _, behind := range []planexport.StepRow{boss, hold} {
		rows := placeRows([]planexport.LegRow{{Plan: "t", Leg: 0, Name: "one"}},
			placeMove(0, 0, 10, 0),
			behind,
			placeMove(0, 0, 80, 0),
			placeMove(0, 0, 100, 0), // 4: 25.2 yd from boss 222
			next,
		)
		got, note := PlaceQuietStart(rows, &worldspawn.World{Map: 309}, 4, 60)
		// Step 3 (45 yd from the boss) is quiet and stands after the crossed row.
		if got != 3 {
			t.Fatalf("%s: start %d, want 3: %s", behind.Kind, got, note)
		}
		// Now every row after the crossed one is inside the boss's reach.
		rows.Steps[2].Y = 110
		got, note = PlaceQuietStart(rows, &worldspawn.World{Map: 309}, 4, 60)
		if got != 4 || !strings.Contains(note, "no quiet move row") {
			t.Fatalf("%s: start %d note %q, want the start kept with the no-quiet-row note", behind.Kind, got, note)
		}
		if behind.Kind == planexport.KindBoss && !strings.Contains(note, "boss 111") {
			t.Fatalf("note %q does not name the boss that stops the walk-back", note)
		}
	}
	// A different leg stops it too.
	rows := placeRows([]planexport.LegRow{{Plan: "t", Leg: 0, Name: "one"}, {Plan: "t", Leg: 1, Name: "two"}},
		placeMove(0, 0, 10, 0),
		placeMove(1, 0, 100, 0),
		next,
	)
	next2 := rows.Steps[2]
	next2.Leg = 1
	rows.Steps[2] = next2
	if got, note := PlaceQuietStart(rows, &worldspawn.World{Map: 309}, 2, 60); got != 2 {
		t.Fatalf("start %d, want 2 (step 1 is another leg): %s", got, note)
	}
}

// The boss is judged from where it stands: a world-export spawn within 30 yd of the plan's boss row replaces the
// row's position as well as its stats. The row is 30 yd from the start (clear), the spawn 20 yd (inside 28).
func TestPlaceQuietStartJudgesTheBossFromItsSpawnNotTheRow(t *testing.T) {
	rows := quietStartBossRows()
	rows.Steps[3].Y = 130
	world := &worldspawn.World{Map: 309, Spawns: []worldspawn.Spawn{{
		GUID: 9, Entry: 11380, Name: "Jin'do the Hexxer", LevelMax: 60, Rank: 3, Faction: 16,
		Pos: dungeon.Point{X: 0, Y: 120, Z: 0}, DetectionYd: 20,
	}}}
	got, note := PlaceQuietStart(rows, world, 3, 60)
	if got != 2 {
		t.Fatalf("start %d, want step 2 (the spawn stands 20 yd from step 3): %s", got, note)
	}
	// With no spawn in the export the row's position is all there is: 30 yd is clear.
	if got, note := PlaceQuietStart(rows, &worldspawn.World{Map: 309}, 3, 60); got != 3 {
		t.Fatalf("start %d, want 3 without a spawn: %s", got, note)
	}
}

// Every plan, every move row as a start, an empty world (the boss rows are the only hazards): no result crosses a boss
// or hold row, none leaves its leg, and a result never lies after the start.
func TestPlaceQuietStartScanOfAllPlans(t *testing.T) {
	files, err := filepath.Glob("../../dungeons/*.json")
	if err != nil || len(files) == 0 {
		t.Fatalf("no plans: %v", err)
	}
	plans, starts, moved := 0, 0, 0
	for _, f := range files {
		plan, err := dungeon.Load(f)
		if err != nil {
			t.Logf("%s: %v", f, err)
			continue
		}
		res, err := planexport.Build(plan, planexport.Options{DropCompanionsLoot: true, GMCrossDirect: true})
		if err != nil {
			t.Logf("%s: %v", f, err)
			continue
		}
		plans++
		rows := res.Rows
		for s := 1; s <= len(rows.Steps); s++ {
			if r := rows.Steps[s-1]; !moveRow(r) || !placedRow(r) {
				continue
			}
			starts++
			got, note := PlaceQuietStart(rows, &worldspawn.World{Map: plan.Map}, s, 60)
			if got == s {
				continue
			}
			moved++
			if got > s || got < 1 {
				t.Errorf("%s: start %d became %d", f, s, got)
				continue
			}
			if rows.Steps[got-1].Leg != rows.Steps[s-1].Leg {
				t.Errorf("%s: start %d moved to step %d of another leg: %s", f, s, got, note)
			}
			for i := got; i < s; i++ {
				if k := rows.Steps[i].Kind; k == planexport.KindBoss || k == planexport.KindHold {
					t.Errorf("%s: start %d moved to %d across the %s row at step %d: %s", f, s, got, k, i+1, note)
				}
			}
		}
	}
	t.Logf("scan: %d plans, %d move-row starts, %d moved", plans, starts, moved)
	if plans < 30 {
		t.Fatalf("only %d plans scanned", plans)
	}
}

// Walk 46 Jin'do on the real plan: the last boss-approach row stands 25.2 yd from his spawn and the row before it
// 69 yd, so a start at the approach row moves back by one row (517 to 516 in the deployed rows).
func TestPlaceQuietStartMovesJindoBackOneRow(t *testing.T) {
	plan, err := dungeon.Load("../../dungeons/zul_gurub.json")
	if err != nil {
		t.Fatal(err)
	}
	res, err := planexport.Build(plan, planexport.Options{DropCompanionsLoot: true, GMCrossDirect: true})
	if err != nil {
		t.Fatal(err)
	}
	rows := res.Rows
	boss := 0
	for i, s := range rows.Steps {
		if s.Kind == planexport.KindBoss && s.Entry == 11380 {
			boss = i + 1
		}
	}
	if boss < 3 {
		t.Fatal("no Jin'do boss row")
	}
	got, note := PlaceQuietStart(rows, &worldspawn.World{Map: 309}, boss-1, 60)
	if got != boss-2 {
		t.Fatalf("start %d, want %d (one row back from the last approach row): %s", got, boss-2, note)
	}
	t.Logf("jin'do boss row %d: %s", boss, note)
}
