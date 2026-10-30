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

func TestQuietAt(t *testing.T) {
	p := dungeon.Point{}
	if ok, reason := QuietAt([]objects.Unit{hostileAt("Onyxian Warder", 40, false, false)}, nil, p); ok || !strings.Contains(reason, "40") {
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

func mustQuiet(t *testing.T, p QuietPatrol) bool {
	t.Helper()
	ok, reason := QuietAt(nil, []QuietPatrol{p}, dungeon.Point{})
	if !ok && reason == "" {
		t.Fatal("not quiet without a reason")
	}
	return ok
}
