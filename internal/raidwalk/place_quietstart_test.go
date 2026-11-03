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
