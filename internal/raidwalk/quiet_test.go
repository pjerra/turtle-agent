package raidwalk

import (
	"strings"
	"testing"
	"time"

	"github.com/pjerra/turtle-agent/internal/dungeon"
	"github.com/pjerra/turtle-agent/internal/objects"
)

// quietNow is the read time the quiet tests use.
var quietNow = time.Unix(1700000000, 0)

// quietAt is QuietAt with a fresh read memory: one read, so D-A's two-read idle exemption exempts nothing.
func quietAt(units []objects.Unit, patrols []QuietPatrol, p dungeon.Point) (bool, string) {
	return QuietAt(units, patrols, p, NewQuietIdleReads(), quietNow)
}

// quietHome is QuietAt after the two reads D-A's exemption needs: the same units, quietIdleHold apart at the same spot.
func quietHome(units []objects.Unit, patrols []QuietPatrol, p dungeon.Point) (bool, string) {
	idle := NewQuietIdleReads()
	QuietAt(units, patrols, p, idle, quietNow)
	return QuietAt(units, patrols, p, idle, quietNow.Add(quietIdleHold))
}

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
	if ok, reason := quietAt([]objects.Unit{fightingAt("Onyxian Warder", 40, false)}, nil, p); ok || !strings.Contains(reason, "40") {
		t.Fatalf("40 yd: quiet %v, reason %q", ok, reason)
	}
	if ok, reason := quietAt([]objects.Unit{hostileAt("Onyxian Warder", 50, false, false)}, nil, p); !ok {
		t.Fatalf("50 yd standing: not quiet (%s)", reason)
	}
	if ok, reason := quietAt([]objects.Unit{hostileAt("Onyxian Warder", 70, true, false)}, nil, p); ok || !strings.Contains(reason, "70") {
		t.Fatalf("70 yd moving: quiet %v, reason %q", ok, reason)
	}
	if ok, reason := quietAt([]objects.Unit{hostileAt("Onyxian Warder", 10, false, true)}, nil, p); !ok {
		t.Fatalf("dead unit: not quiet (%s)", reason)
	}
	if ok, reason := quietAt([]objects.Unit{{Type: "player", Name: "Someone", Hostile: true, X: 10}}, nil, p); !ok {
		t.Fatalf("hostile player: not quiet (%s)", reason)
	}

	patrol := QuietPatrol{ID: "52050", Path: []dungeon.Point{{X: 30, Y: 0, Z: 0}, {X: 30, Y: 30, Z: 0}}}
	if ok, reason := quietAt(nil, []QuietPatrol{patrol}, p); ok || !strings.Contains(reason, "52050") {
		t.Fatalf("patrol near: quiet %v, reason %q", ok, reason)
	}
	patrol.Exempt = true
	if ok, reason := quietAt(nil, []QuietPatrol{patrol}, p); !ok {
		t.Fatalf("exempt patrol: not quiet (%s)", reason)
	}

	// The loop closes from the last point back to the first.
	patrol = QuietPatrol{ID: "52051", Path: []dungeon.Point{{X: 50, Y: -50, Z: 0}, {X: 50, Y: 50, Z: 0}, {X: -50, Y: 50, Z: 0}}}
	if ok, reason := quietAt(nil, []QuietPatrol{patrol}, p); ok {
		t.Fatalf("the closing leg of a loop was not seen: %s", reason)
	}
	patrol.Path = []dungeon.Point{{X: 60, Y: 0, Z: 0}, {X: 60, Y: 30, Z: 0}}
	if ok := mustQuiet(t, patrol); !ok {
		t.Fatal("a patrol 60 yd away was not quiet")
	}
}

// D-A (walk 25): the refusal was a mob standing at its own spawn 44.6 yd from the revive point, not fighting: an idle
// unit beyond the landing reach does not block the revive — once two reads quietIdleHold apart saw it idle at the same
// spot (review round 1, Opus M1: one read is no proof, the mob may start to move next).
func TestQuietAtIgnoresIdleUnitBeyondReach(t *testing.T) {
	p := dungeon.Point{}
	u := hostileAt("Razzashi Venombrood", 44.6, false, false)
	u.Entry = 14532
	if ok, reason := quietAt([]objects.Unit{u}, nil, p); ok {
		t.Fatalf("an idle unit 44.6 yd from the point is quiet on ONE read: %s", reason)
	}
	if ok, reason := quietHome([]objects.Unit{u}, nil, p); !ok {
		t.Fatalf("an idle unit 44.6 yd from the point still blocks after two reads: %s", reason)
	}
}

// TestQuietAtIdleNeedsTwoReads (review round 1, Opus M1): D-A's exemption is not an idle-ness test but a
// idle-at-home one — the same unit must be seen idle twice, quietIdleHold apart, within quietIdleMoveYd of the same
// position. A unit idle on read 1 and moving on read 2 was never idle at home, so read 1 does not exempt it.
func TestQuietAtIdleNeedsTwoReads(t *testing.T) {
	p := dungeon.Point{}
	u := hostileAt("Razzashi Venombrood", 44.6, false, false)
	idle := NewQuietIdleReads()
	if ok, _ := QuietAt([]objects.Unit{u}, nil, p, idle, quietNow); ok {
		t.Fatal("idle on read 1: quiet, want blocked (no earlier read saw it idle)")
	}
	moving := hostileAt("Razzashi Venombrood", 44.6, true, false)
	if ok, _ := QuietAt([]objects.Unit{moving}, nil, p, idle, quietNow.Add(quietIdleHold)); ok {
		t.Fatal("idle on read 1, moving on read 2: quiet, want blocked")
	}
	// Read 2 did not see it idle, so read 3 starts the pair over: a single idle read again.
	if ok, _ := QuietAt([]objects.Unit{u}, nil, p, idle, quietNow.Add(2*quietIdleHold)); ok {
		t.Fatal("an idle read after a moving one: quiet, want blocked (the moving read forgot the pair)")
	}
	// Read 4, quietIdleHold after read 3 at the same spot: the pair is complete.
	if ok, reason := QuietAt([]objects.Unit{u}, nil, p, idle, quietNow.Add(3*quietIdleHold)); !ok {
		t.Fatalf("two idle reads quietIdleHold apart at the same spot: not quiet (%s)", reason)
	}

	// The two reads must be quietIdleHold apart: a second read too soon is no pair.
	soon := NewQuietIdleReads()
	QuietAt([]objects.Unit{u}, nil, p, soon, quietNow)
	if ok, _ := QuietAt([]objects.Unit{u}, nil, p, soon, quietNow.Add(quietIdleHold-time.Second)); ok {
		t.Fatal("two idle reads 4 s apart: quiet, want blocked")
	}

	// And within quietIdleMoveYd of the SAME position: a unit idle at two spots is a wanderer, not at home.
	near := NewQuietIdleReads()
	QuietAt([]objects.Unit{u}, nil, p, near, quietNow)
	shifted := hostileAt("Razzashi Venombrood", 44.9, false, false)
	if ok, reason := QuietAt([]objects.Unit{shifted}, nil, p, near, quietNow.Add(quietIdleHold)); !ok {
		t.Fatalf("idle 0.3 yd apart: not quiet (%s)", reason)
	}
	far := NewQuietIdleReads()
	QuietAt([]objects.Unit{u}, nil, p, far, quietNow)
	moved := hostileAt("Razzashi Venombrood", 43.0, false, false)
	if ok, _ := QuietAt([]objects.Unit{moved}, nil, p, far, quietNow.Add(quietIdleHold)); ok {
		t.Fatal("idle at two spots over 1 yd apart: quiet, want blocked")
	}
	// The same spot twice again, quietIdleHold later: the pair completes there.
	if ok, reason := QuietAt([]objects.Unit{moved}, nil, p, far, quietNow.Add(2*quietIdleHold)); !ok {
		t.Fatalf("idle at the same spot on two reads: not quiet (%s)", reason)
	}
}

// Codex round 3: the two reads must be consecutive evidence. A unit missing from a read, a read too long ago, or a
// failed read in between starts the pair over.
func TestQuietAtIdlePairNeedsFreshConsecutiveReads(t *testing.T) {
	p := dungeon.Point{}
	u := hostileAt("Razzashi Venombrood", 44.6, false, false)

	gone := NewQuietIdleReads()
	QuietAt([]objects.Unit{u}, nil, p, gone, quietNow)
	QuietAt(nil, nil, p, gone, quietNow.Add(quietIdleHold)) // a complete read without the unit
	if ok, _ := QuietAt([]objects.Unit{u}, nil, p, gone, quietNow.Add(2*quietIdleHold)); ok {
		t.Fatal("idle, absent, idle: quiet, want blocked (the absent read forgot the first sighting)")
	}

	dead := NewQuietIdleReads()
	QuietAt([]objects.Unit{u}, nil, p, dead, quietNow)
	corpse := u
	corpse.Dead = true
	QuietAt([]objects.Unit{corpse}, nil, p, dead, quietNow.Add(quietIdleHold))
	if ok, _ := QuietAt([]objects.Unit{u}, nil, p, dead, quietNow.Add(2*quietIdleHold)); ok {
		t.Fatal("idle, dead, idle again: quiet, want blocked")
	}

	old := NewQuietIdleReads()
	QuietAt([]objects.Unit{u}, nil, p, old, quietNow)
	if ok, _ := QuietAt([]objects.Unit{u}, nil, p, old, quietNow.Add(quietIdleMaxGap+time.Second)); ok {
		t.Fatal("two idle reads over quietIdleMaxGap apart: quiet, want blocked")
	}

	failed := NewQuietIdleReads()
	QuietAt([]objects.Unit{u}, nil, p, failed, quietNow)
	failed.Reset() // what quietOnce does on a failed units read
	if ok, _ := QuietAt([]objects.Unit{u}, nil, p, failed, quietNow.Add(quietIdleHold)); ok {
		t.Fatal("idle, failed read, idle: quiet, want blocked")
	}
}

// An idle unit inside the landing reach still blocks, however many reads saw it idle: the revived raid lands right
// there. The cut itself (30 yd since review round 1, L1) counts as inside, and just beyond it needs the two reads.
func TestQuietAtIdleWithinReachBlocks(t *testing.T) {
	p := dungeon.Point{}
	for _, d := range []float32{10, 29.9, 30} {
		ok, reason := quietHome([]objects.Unit{hostileAt("Razzashi Venombrood", d, false, false)}, nil, p)
		if ok {
			t.Fatalf("an idle unit at %g yd: quiet, want blocked", d)
		}
		if !strings.Contains(reason, "yd from the revive point") {
			t.Fatalf("an idle unit at %g yd: reason %q", d, reason)
		}
	}
	if ok, reason := quietHome([]objects.Unit{hostileAt("Razzashi Venombrood", 30.1, false, false)}, nil, p); !ok {
		t.Fatalf("an idle unit at 30.1 yd is not quiet after two reads: %s", reason)
	}
}

// A unit in combat, or one holding a target, is not idle: D-A leaves it to the old 45/80 yd rules.
func TestQuietAtInCombatOrTargetingStillBlocks(t *testing.T) {
	p := dungeon.Point{}
	if ok, reason := quietAt([]objects.Unit{fightingAt("Onyxian Warder", 40, false)}, nil, p); ok {
		t.Fatalf("an in-combat unit 40 yd from the point: quiet (%s)", reason)
	}
	targeting := hostileAt("Onyxian Warder", 40, false, false)
	targeting.Target = 1000
	if ok, reason := quietAt([]objects.Unit{targeting}, nil, p); ok {
		t.Fatalf("a targeting unit 40 yd from the point: quiet (%s)", reason)
	}
	// Two blockers: the reason names the nearest one (QuietBlockers' order; the summary's own and the run.log line's
	// use of the whole list is checked in the walk command's own tests).
	close := fightingAt("Razzashi Skitterer", 30, false)
	if ok, reason := quietAt([]objects.Unit{targeting, close}, nil, p); ok || !strings.Contains(reason, "Razzashi Skitterer") {
		t.Fatalf("two blockers: quiet %v, reason %q, want the nearest named", ok, reason)
	}
}

// A moving unit is not idle either: it keeps the 80 yd moving rule (the Onyxian Warder patrol case).
func TestQuietAtIdleMovingStillBlocks(t *testing.T) {
	p := dungeon.Point{}
	if ok, reason := quietAt([]objects.Unit{hostileAt("Onyxian Warder", 60, true, false)}, nil, p); ok {
		t.Fatalf("a moving unit 60 yd from the point: quiet (%s)", reason)
	}
}

func mustQuiet(t *testing.T, p QuietPatrol) bool {
	t.Helper()
	ok, reason := quietAt(nil, []QuietPatrol{p}, dungeon.Point{})
	if !ok && reason == "" {
		t.Fatal("not quiet without a reason")
	}
	return ok
}
