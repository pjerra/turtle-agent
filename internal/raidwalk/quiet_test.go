package raidwalk

import (
	"strings"
	"testing"

	"github.com/pjerra/turtle-agent/internal/dungeon"
	"github.com/pjerra/turtle-agent/internal/objects"
)

func hostileAt(name string, x float32, moving, dead bool) objects.Unit {
	return objects.Unit{Type: "creature", Name: name, Hostile: true, X: x, Z: 0, Moving: moving, Dead: dead}
}

// fightingAt is hostileAt for a unit in combat: D-A's idle exemption (not in combat, no target, not moving) leaves it
// alone, so the 45/80 yd rules still hold for it.
func fightingAt(name string, x float32, moving bool) objects.Unit {
	u := hostileAt(name, x, moving, false)
	u.InCombat = true
	return u
}

func TestQuietAt(t *testing.T) {
	p := dungeon.Point{}
	// 40 yd and in combat: D-A's idle exemption does not touch it, so the 45 yd still rule blocks (an idle unit at
	// 40 yd is quiet now: TestQuietAtIgnoresIdleUnitBeyondReach).
	if ok, reason := QuietAt([]objects.Unit{fightingAt("Onyxian Warder", 40, false)}, nil, p); ok || !strings.Contains(reason, "40") {
		t.Fatalf("40 yd: quiet %v, reason %q", ok, reason)
	}
	if ok, reason := QuietAt([]objects.Unit{hostileAt("Onyxian Warder", 50, false, false)}, nil, p); !ok {
		t.Fatalf("50 yd standing: not quiet (%s)", reason)
	}
	if ok, reason := QuietAt([]objects.Unit{hostileAt("Onyxian Warder", 70, true, false)}, nil, p); ok || !strings.Contains(reason, "70") {
		t.Fatalf("70 yd moving: quiet %v, reason %q", ok, reason)
	}
	if ok, reason := QuietAt([]objects.Unit{hostileAt("Onyxian Warder", 10, false, true)}, nil, p); !ok {
		t.Fatalf("dead unit: not quiet (%s)", reason)
	}
	if ok, reason := QuietAt([]objects.Unit{{Type: "player", Name: "Someone", Hostile: true, X: 10}}, nil, p); !ok {
		t.Fatalf("hostile player: not quiet (%s)", reason)
	}

	patrol := QuietPatrol{ID: "52050", Path: []dungeon.Point{{X: 30, Y: 0, Z: 0}, {X: 30, Y: 30, Z: 0}}}
	if ok, reason := QuietAt(nil, []QuietPatrol{patrol}, p); ok || !strings.Contains(reason, "52050") {
		t.Fatalf("patrol near: quiet %v, reason %q", ok, reason)
	}
	patrol.Exempt = true
	if ok, reason := QuietAt(nil, []QuietPatrol{patrol}, p); !ok {
		t.Fatalf("exempt patrol: not quiet (%s)", reason)
	}

	// The loop closes from the last point back to the first.
	patrol = QuietPatrol{ID: "52051", Path: []dungeon.Point{{X: 50, Y: -50, Z: 0}, {X: 50, Y: 50, Z: 0}, {X: -50, Y: 50, Z: 0}}}
	if ok, reason := QuietAt(nil, []QuietPatrol{patrol}, p); ok {
		t.Fatalf("the closing leg of a loop was not seen: %s", reason)
	}
	patrol.Path = []dungeon.Point{{X: 60, Y: 0, Z: 0}, {X: 60, Y: 30, Z: 0}}
	if ok := mustQuiet(t, patrol); !ok {
		t.Fatal("a patrol 60 yd away was not quiet")
	}
}

// D-A (walk 25): the refusal was a mob standing at its own spawn 44.6 yd from the revive point, not fighting: an idle
// unit beyond the placement's landing reach does not block the revive.
func TestQuietAtIgnoresIdleUnitBeyondReach(t *testing.T) {
	p := dungeon.Point{}
	u := hostileAt("Razzashi Venombrood", 44.6, false, false)
	u.Entry = 14532
	if ok, reason := QuietAt([]objects.Unit{u}, nil, p); !ok {
		t.Fatalf("an idle unit 44.6 yd from the point blocks the revive: %s", reason)
	}
}

// An idle unit inside the landing reach still blocks: the revived raid lands right there. The reach itself counts as
// inside (placeReachYd's own cut).
func TestQuietAtIdleWithinReachBlocks(t *testing.T) {
	p := dungeon.Point{}
	for _, d := range []float32{10, 24.9, 25} {
		ok, reason := QuietAt([]objects.Unit{hostileAt("Razzashi Venombrood", d, false, false)}, nil, p)
		if ok {
			t.Fatalf("an idle unit at %g yd: quiet, want blocked", d)
		}
		if !strings.Contains(reason, "yd from the revive point") {
			t.Fatalf("an idle unit at %g yd: reason %q", d, reason)
		}
	}
}

// A unit in combat, or one holding a target, is not idle: D-A leaves it to the old 45/80 yd rules.
func TestQuietAtInCombatOrTargetingStillBlocks(t *testing.T) {
	p := dungeon.Point{}
	if ok, reason := QuietAt([]objects.Unit{fightingAt("Onyxian Warder", 40, false)}, nil, p); ok {
		t.Fatalf("an in-combat unit 40 yd from the point: quiet (%s)", reason)
	}
	targeting := hostileAt("Onyxian Warder", 40, false, false)
	targeting.Target = 1000
	if ok, reason := QuietAt([]objects.Unit{targeting}, nil, p); ok {
		t.Fatalf("a targeting unit 40 yd from the point: quiet (%s)", reason)
	}
	// Two blockers: the reason names the nearest one (QuietBlockers' order; the summary's own and the run.log line's
	// use of the whole list is checked in the walk command's own tests).
	close := fightingAt("Razzashi Skitterer", 30, false)
	if ok, reason := QuietAt([]objects.Unit{targeting, close}, nil, p); ok || !strings.Contains(reason, "Razzashi Skitterer") {
		t.Fatalf("two blockers: quiet %v, reason %q, want the nearest named", ok, reason)
	}
}

// A moving unit is not idle either: it keeps the 80 yd moving rule (the Onyxian Warder patrol case).
func TestQuietAtIdleMovingStillBlocks(t *testing.T) {
	p := dungeon.Point{}
	if ok, reason := QuietAt([]objects.Unit{hostileAt("Onyxian Warder", 60, true, false)}, nil, p); ok {
		t.Fatalf("a moving unit 60 yd from the point: quiet (%s)", reason)
	}
}

func mustQuiet(t *testing.T, p QuietPatrol) bool {
	t.Helper()
	ok, reason := QuietAt(nil, []QuietPatrol{p}, dungeon.Point{})
	if !ok && reason == "" {
		t.Fatal("not quiet without a reason")
	}
	return ok
}
