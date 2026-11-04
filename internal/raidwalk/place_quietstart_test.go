package raidwalk

import (
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
		placeMove(3, 1, 10, 0),                          // leg 3: the earlier leg
		placeRow(planexport.KindBoss, 3, 2, 10, 0, 1, 1), // its boss row
		placeMove(4, 3, 10, 0),                          // leg 4: the start's own leg, first row
		placeMove(4, 4, 10, 0),                          // and the row behind the start
	)
	// A start at leg 4's first row has no retreat: step 3 is the boss row, and step 2 is another leg.
	if got, ok := PlaceBackInLeg(rows, 3, "4"); ok {
		t.Fatalf("a retreat from leg 4's first row went to %d: it would start the run at the boss row before it", got)
	}
	// Inside the leg the row behind is the retreat.
	if got, ok := PlaceBackInLeg(rows, 4, "4"); !ok || got != 3 {
		t.Fatalf("retreat from 4 in leg 4: %d %v, want 3", got, ok)
	}
	// The pure finder still crosses it (walk-super's restart is not the quiet retreat).
	if got, ok := PlaceBack(rows, 3); !ok || got != 1 {
		t.Fatalf("PlaceBack from 3: %d %v, want 1", got, ok)
	}
}
