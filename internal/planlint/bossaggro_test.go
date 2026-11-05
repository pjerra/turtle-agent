package planlint

import (
	"testing"

	"github.com/pjerra/turtle-agent/internal/dungeon"
	"github.com/pjerra/turtle-agent/internal/planexport"
	"github.com/pjerra/turtle-agent/internal/worldspawn"
)

// The spec's §8 tests, FIRST: a path point 9 yd from Ossirian flags, a stand
// 55 yd from Moam flags on the script range (and is clean on the template
// range alone), the boss's own approach is exempt, and rows after the boss
// row are exempt. Every hop stays under the hop_len warning (120 yd) so only
// the boss_aggro finding shows.

func bossWorld() *worldspawn.World {
	return worldOf(
		worldspawn.Spawn{GUID: 1, Entry: 15339, Name: "Ossirian the Unscarred", LevelMax: 63, DetectionYd: 30, Faction: 16, Pos: dungeon.Point{X: 0, Y: 0, Z: 0}},
		worldspawn.Spawn{GUID: 2, Entry: 15340, Name: "Moam", LevelMax: 63, DetectionYd: 20, Faction: 16, Pos: dungeon.Point{X: 1000, Y: 0, Z: 0}},
	)
}

func bossStep(entry uint32) planexport.StepRow {
	return planexport.StepRow{Kind: planexport.KindBoss, Entry: entry, X: 0, Y: 0, Z: 0}
}

// A synthetic plan with a path point 9 yd from Ossirian is flagged.
func TestBossAggroFlagsPathPastOssirian(t *testing.T) {
	w := bossWorld()
	b := bossStep(15339)
	rows := rowsOf(move(65, 0, 0), move(9, 0, 0), move(60, 0, 0), b)
	fs := lintOne(t, rows, fakeNav{}, w)
	wantOnly(t, fs, LintBossAggro)
	if fs[0].Step != 2 {
		t.Fatalf("finding %v, want step 2", fs[0])
	}
	if fs[0].Severity != SeverityError {
		t.Fatalf("finding %v, want severity error", fs[0])
	}
}

// A stand 55 yd from Moam is flagged with the script range, and clean with
// the template range (spawns far from Ossirian so only Moam judges).
func TestBossAggroUsesScriptRangeForMoam(t *testing.T) {
	w := worldOf(
		worldspawn.Spawn{GUID: 2, Entry: 15340, Name: "Moam", LevelMax: 63, DetectionYd: 20, Faction: 16, Pos: dungeon.Point{X: 1000, Y: 0, Z: 0}},
	)
	b := bossStep(15340)
	b.X, b.Y, b.Z = 1000, 0, 0
	fs := lintOne(t, rowsOf(move(900, 0, 0), move(945, 0, 0), pull(950, 10, 0, 999), b), fakeNav{}, w)
	wantOnly(t, fs, LintBossAggro)

	// The template reach alone (20 yd vs level 60: 23 yd) is clean at 55 yd:
	// the same shape against an entry with no script row passes.
	noScript := worldOf(
		worldspawn.Spawn{GUID: 9, Entry: 99999, Name: "other", LevelMax: 63, DetectionYd: 20, Faction: 16, Pos: dungeon.Point{X: 1000, Y: 0, Z: 0}},
	)
	b2 := bossStep(99999)
	b2.X, b2.Y, b2.Z = 1000, 0, 0
	if fs := lintOne(t, rowsOf(move(900, 0, 0), move(945, 0, 0), pull(950, 10, 0, 999), b2), fakeNav{}, noScript); len(fs) != 0 {
		t.Fatalf("template-range stand findings %v, want none", fs)
	}
}

func countLint(t *testing.T, rows planexport.Rows, w *worldspawn.World, lint string) int {
	t.Helper()
	n := 0
	for _, f := range lintOne(t, rows, fakeNav{}, w) {
		if f.Lint == lint {
			n++
		}
	}
	return n
}

// The boss's own approach row (the move right before the boss row) is exempt.
func TestBossApproachExempt(t *testing.T) {
	w := bossWorld()
	b := bossStep(15339)
	if fs := lintOne(t, rowsOf(move(65, 0, 0), move(9, 0, 0), b), fakeNav{}, w); len(fs) != 0 {
		t.Fatalf("approach findings %v, want none", fs)
	}
}

// Rows at or after the boss row (after_boss) are exempt.
func TestAfterBossRowsExempt(t *testing.T) {
	w := bossWorld()
	b := bossStep(15339)
	rows := rowsOf(move(100, 0, 0), move(60, 0, 0), b, move(9, 0, 0))
	if fs := lintOne(t, rows, fakeNav{}, w); len(fs) != 0 {
		t.Fatalf("after-boss findings %v, want none", fs)
	}
}

// The stand of a pull row that lists the boss entry (a boss pulled with its
// adds) is exempt.
func TestBossPullStandExempt(t *testing.T) {
	w := bossWorld()
	b := bossStep(15339)
	rows := rowsOf(move(30, 0, 0), pull(35, 0, 0, 15339), b)
	if fs := lintOne(t, rows, fakeNav{}, w); len(fs) != 0 {
		t.Fatalf("boss-pull stand findings %v, want none", fs)
	}
}

// The committed walk into Moam (trailing moves no closer than his 22 yd
// approach) is exempt; a point closer than the stand still flags.
func TestBossWalkInExempt(t *testing.T) {
	w := worldOf(
		worldspawn.Spawn{GUID: 2, Entry: 15340, Name: "Moam", LevelMax: 63, DetectionYd: 20, Faction: 16, Pos: dungeon.Point{X: 1000, Y: 0, Z: 0}},
	)
	b := bossStep(15340)
	b.X, b.Y, b.Z = 1000, 0, 0
	// Walk-in from 90 yd to the 22 yd stand: the 55 yd tail point is exempt.
	// (Only boss_aggro is judged: the old stand aggro lint still sees the
	// boss spawn itself, as on every live boss approach.)
	rows := rowsOf(move(910, 0, 0), move(945, 0, 0), move(978, 0, 0), b)
	if n := countLint(t, rows, w, LintBossAggro); n != 0 {
		t.Fatalf("walk-in boss_aggro findings %d, want none", n)
	}
	// A point at 10 yd, closer than the 22 yd stand (a walk past him), flags.
	rows = rowsOf(move(910, 0, 0), move(990, 0, 0), move(978, 0, 0), b)
	if n := countLint(t, rows, w, LintBossAggro); n != 1 {
		t.Fatalf("walk-past boss_aggro findings %d, want one", n)
	}
}
