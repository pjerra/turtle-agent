package dungeon

import (
	"errors"
	"fmt"
	"strings"
	"testing"
)

// gpsText is what the core prints for `.gps` (Commands.cpp HandleGPSCommand with mangos_string 101), as lines of the chat.
func gpsText(ground, floor float64) string {
	return strings.Join([]string{
		"You are OUTdoor",
		"Map: 70 (Uldaman) Zone: 1337 (Uldaman) Area: 1337 (Uldaman)",
		"X: 100.000000 Y: 0.000000 Z: 0.000000 Orientation: 1.570796",
		"grid[31,32]cell[5,6] InstanceID: 12",
		" ZoneX: 10.500000 ZoneY: 20.250000",
		fmt.Sprintf("GroundZ: %f FloorZ: %f Have height data (Map: 1 VMap: 1)", ground, floor),
	}, "\n")
}

func TestParseGPS(t *testing.T) {
	for _, c := range []struct {
		name          string
		text          string
		ground, floor float64
		ok            bool
	}{
		{"real wording", gpsText(-120.5, -131.25), -120.5, -131.25, true},
		{"one line", "GroundZ: 12.000000 FloorZ: 11.750000 Have height data (Map: 1 VMap: 1)", 12, 11.75, true},
		{"extra lines after", gpsText(1, 2) + "\nsomething else\n", 1, 2, true},
		{"other player's chat before", "[World] hi\n" + gpsText(3, 3), 3, 3, true},
		{"integers", "GroundZ: 5 FloorZ: 6", 5, 6, true},
		{"no height data (-100000)", gpsText(-100000, -100000), 0, 0, false},
		{"floor only invalid", gpsText(10, -100000), 0, 0, false},
		{"no FloorZ", "GroundZ: 5.0", 0, 0, false},
		{"empty", "", 0, 0, false},
		{"unrelated", "There is no such command", 0, 0, false},
		{"comma decimals are not read", "GroundZ: 5,5 FloorZ: 6,5", 0, 0, false},
	} {
		g, f, ok := ParseGPS(c.text)
		if ok != c.ok || (ok && (g != c.ground || f != c.floor)) {
			t.Errorf("%s: got %v %v %v, want %v %v %v", c.name, g, f, ok, c.ground, c.floor, c.ok)
		}
	}
}

func gpsConst(ground, floor float64) func(*startFake) (string, error) {
	return func(*startFake) (string, error) { return gpsText(ground, floor), nil }
}

// The commander ends 15 yd under the ground at the start point: `.go xyz` is sent once more at FloorZ+0.3, and when it still stands off
// the start is refused with the numbers, before the party is brought.
func TestStartRefusesACommanderUnderTheGround(t *testing.T) {
	g := newStartFake(t)
	g.addMob(300, Point{250, 5, 0})
	g.sink = 15
	g.gpsFn = gpsConst(0, 0)
	rep, err := runFrom(t, g, twoLegPlan(), startOpts(), 1)
	if err == nil || rep.State != "failed" {
		t.Fatalf("%v %s\n%s", err, rep.State, notesOf(rep))
	}
	for _, want := range []string{"target z 0.0", "GroundZ 0.0", "FloorZ 0.0", "commander z -14.7"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("the refusal lacks %q: %v", want, err)
		}
	}
	if len(g.goPts) != 2 || g.goPts[0] != (Point{100, 0, 0}) || g.goPts[1].X != 100 || g.goPts[1].Y != 0 || g.goPts[1].Z < 0.29 || g.goPts[1].Z > 0.31 {
		t.Fatalf("go xyz points %v", g.goPts)
	}
	if g.gpsCalls != 2 || len(g.botLines) != 0 || len(g.summonsLog) != 0 {
		t.Fatalf("gps %d, bot lines %v, summons %v", g.gpsCalls, g.botLines, g.summonsLog)
	}
}

// A commander that stands on the floor needs no second move.
func TestStartKeepsACommanderOnTheFloor(t *testing.T) {
	g := newStartFake(t)
	g.addMob(300, Point{250, 5, 0})
	g.gpsFn = gpsConst(0, 0.2)
	rep, err := runFrom(t, g, twoLegPlan(), startOpts(), 1)
	if err != nil || rep.State != "done" {
		t.Fatalf("%v %s\n%s", err, rep.State, notesOf(rep))
	}
	if len(g.goPts) != 1 || g.gpsCalls != 1 {
		t.Fatalf("go xyz %v, gps calls %d", g.goPts, g.gpsCalls)
	}
}

// A commander that hovers above the floor is put down on it by the one re-send.
func TestStartResendsOnceToTheFloor(t *testing.T) {
	g := newStartFake(t)
	g.addMob(300, Point{250, 5, 0})
	g.gpsFn = gpsConst(5, 5)
	rep, err := runFrom(t, g, twoLegPlan(), startOpts(), 1)
	if err != nil || rep.State != "done" {
		t.Fatalf("%v %s\n%s", err, rep.State, notesOf(rep))
	}
	if len(g.goPts) != 2 || g.goPts[1].Z < 5.29 || g.goPts[1].Z > 5.31 || g.gpsCalls != 2 {
		t.Fatalf("go xyz %v, gps calls %d", g.goPts, g.gpsCalls)
	}
}

// The floor under the commander is another layer than the point (GroundZ differs, FloorZ is 40 yd from the target z): refused even when
// the commander stands on it.
func TestStartRefusesAFloorFarFromTheTarget(t *testing.T) {
	g := newStartFake(t)
	g.addMob(300, Point{250, 5, 0})
	g.gpsFn = gpsConst(100, 40)
	_, err := runFrom(t, g, twoLegPlan(), startOpts(), 1)
	if err == nil || !strings.Contains(err.Error(), "FloorZ 40.0") || !strings.Contains(err.Error(), "GroundZ 100.0") {
		t.Fatalf("%v", err)
	}
	// GroundZ = FloorZ far from the target is not that refusal: the point is what the plan says.
	g = newStartFake(t)
	g.addMob(300, Point{250, 5, 0})
	g.gpsFn = gpsConst(40, 40)
	if rep, err := runFrom(t, g, twoLegPlan(), startOpts(), 1); err != nil || rep.State != "done" {
		t.Fatalf("%v %s", err, rep.State)
	}
}

// A .gps that fails or cannot be read never blocks the start (older servers, commanders without GM): one note says the check is best effort.
func TestStartGPSIsBestEffort(t *testing.T) {
	for name, fn := range map[string]func(*startFake) (string, error){
		"no reply":    func(*startFake) (string, error) { return "", errors.New("timeout") },
		"unparseable": func(*startFake) (string, error) { return "There is no such command", nil },
		"no map data": gpsConst(-100000, -100000),
		"empty reply": func(*startFake) (string, error) { return "", nil },
	} {
		g := newStartFake(t)
		g.addMob(300, Point{250, 5, 0})
		g.sink = 15
		g.gpsFn = fn
		rep, err := runFrom(t, g, twoLegPlan(), startOpts(), 1)
		if err != nil || rep.State != "done" {
			t.Fatalf("%s: %v %s\n%s", name, err, rep.State, notesOf(rep))
		}
		if n := strings.Count(notesOf(rep), "best effort"); n != 1 || len(g.goPts) != 1 {
			t.Fatalf("%s: %d best-effort notes, go xyz %v\n%s", name, n, g.goPts, notesOf(rep))
		}
	}
}
