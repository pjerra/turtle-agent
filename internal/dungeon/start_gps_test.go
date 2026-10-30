package dungeon

import (
	"errors"
	"fmt"
	"slices"
	"strings"
	"testing"
)

// gpsText is what the core prints for `.gps` (Commands.cpp HandleGPSCommand with mangos_string 101), as lines of the chat, for the
// commander at (100, 0).
func gpsText(ground, floor float64) string { return gpsTextAt(100, 0, ground, floor) }

// gpsTextAt is the same reply about a unit at (x, y).
func gpsTextAt(x, y, ground, floor float64) string {
	return strings.Join([]string{
		"You are OUTdoor",
		"Map: 70 (Uldaman) Zone: 1337 (Uldaman) Area: 1337 (Uldaman)",
		fmt.Sprintf("X: %f Y: %f Z: 0.000000 Orientation: 1.570796", x, y),
		"grid[31,32]cell[5,6] InstanceID: 12",
		" ZoneX: 10.500000 ZoneY: 20.250000",
		fmt.Sprintf("GroundZ: %f FloorZ: %f Have height data (Map: 1 VMap: 1)", ground, floor),
	}, "\n")
}

func TestParseGPS(t *testing.T) {
	for _, c := range []struct {
		name string
		text string
		want GPSReply
		ok   bool
	}{
		{"real wording", gpsText(-120.5, -131.25), GPSReply{X: 100, Y: 0, GroundZ: -120.5, FloorZ: -131.25}, true},
		{"x and y with decimals", gpsTextAt(-12.5, 3.25, 1, 2), GPSReply{X: -12.5, Y: 3.25, GroundZ: 1, FloorZ: 2}, true},
		{"extra lines after", gpsText(1, 2) + "\nsomething else\n", GPSReply{X: 100, GroundZ: 1, FloorZ: 2}, true},
		{"other player's chat before", "[World] hi\n" + gpsText(3, 3), GPSReply{X: 100, GroundZ: 3, FloorZ: 3}, true},
		{"integers", "X: 5 Y: 6\nGroundZ: 5 FloorZ: 6", GPSReply{X: 5, Y: 6, GroundZ: 5, FloorZ: 6}, true},
		{"no height data (-200000)", gpsText(-200000, -200000), GPSReply{}, false},
		{"indoor GroundZ missing uses FloorZ", "X: 10.000000 Y: 20.000000 Z: 0.000000 Orientation: 0.000000\nGroundZ: -200000.000000 FloorZ: -62.260399 Have height data (Map: 1 VMap: 1)", GPSReply{X: 10, Y: 20, GroundZ: -62.260399, FloorZ: -62.260399}, true},
		{"GroundZ above the missing mark stays", "X: 10.000000 Y: 20.000000 Z: 0.000000 Orientation: 0.000000\nGroundZ: -50000.000000 FloorZ: -62.260399 Have height data (Map: 1 VMap: 1)", GPSReply{X: 10, Y: 20, GroundZ: -50000, FloorZ: -62.260399}, true},
		{"no X and Y", "GroundZ: 12.000000 FloorZ: 11.750000 Have height data (Map: 1 VMap: 1)", GPSReply{}, false},
		{"ZoneX and ZoneY are not X and Y", "ZoneX: 10.500000 ZoneY: 20.250000\nGroundZ: 1.0 FloorZ: 2.0", GPSReply{}, false},
		{"empty", "", GPSReply{}, false},
		{"unrelated", "There is no such command", GPSReply{}, false},
		{"comma decimals are not read", "X: 1,5 Y: 2,5\nGroundZ: 5,5 FloorZ: 6,5", GPSReply{}, false},
	} {
		got, ok := ParseGPS(c.text)
		if ok != c.ok || got != c.want {
			t.Errorf("%s: got %+v %v, want %+v %v", c.name, got, ok, c.want, c.ok)
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

// An indoor `.gps` (`GroundZ: -200000`, BRD) uses FloorZ: a commander sunk 15 yd is re-sent at FloorZ+0.3 and refused with FloorZ's
// numbers, exactly as outdoors.
func TestStartUsesFloorZWhenGroundZIsMissing(t *testing.T) {
	g := newStartFake(t)
	g.addMob(300, Point{250, 5, 0})
	g.sink = 15
	g.gpsFn = func(*startFake) (string, error) {
		return gpsTextAt(100, 0, -200000, 0), nil
	}
	_, err := runFrom(t, g, twoLegPlan(), startOpts(), 1)
	if err == nil || !strings.Contains(err.Error(), "GroundZ 0.0") || !strings.Contains(err.Error(), "FloorZ 0.0") {
		t.Fatalf("no indoor refusal with FloorZ numbers: %v", err)
	}
	if len(g.goPts) != 2 || g.goPts[1].Z < 0.29 || g.goPts[1].Z > 0.31 {
		t.Fatalf("go xyz points %v (want a re-send at z 0.3): the missing GroundZ was not replaced by FloorZ", g.goPts)
	}
}

// A .gps that fails or cannot be read never blocks the start (older servers, commanders without GM): one note says the check is best effort.
func TestStartGPSIsBestEffort(t *testing.T) {
	for name, fn := range map[string]func(*startFake) (string, error){
		"no reply":           func(*startFake) (string, error) { return "", errors.New("timeout") },
		"unparseable":        func(*startFake) (string, error) { return "There is no such command", nil },
		"no map data":        gpsConst(-200000, -200000),
		"empty reply":        func(*startFake) (string, error) { return "", nil },
		"floor only invalid": gpsConst(10, -200000),
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

// A reply about another unit (500 yd away, its floor 15 yd under the commander's z) is not the commander's: it is unreadable, so it
// neither moves the commander again nor refuses the start, and one note says the check could not be made.
func TestStartIgnoresAReplyAboutAnotherUnit(t *testing.T) {
	g := newStartFake(t)
	g.addMob(300, Point{250, 5, 0})
	g.sink = 15
	g.gpsFn = func(*startFake) (string, error) { return gpsTextAt(500, 500, 0, 0), nil }
	rep, err := runFrom(t, g, twoLegPlan(), startOpts(), 1)
	if err != nil || rep.State != "done" {
		t.Fatalf("%v %s\n%s", err, rep.State, notesOf(rep))
	}
	if len(g.goPts) != 1 || g.gpsCalls != 1 {
		t.Fatalf("go xyz %v, gps calls %d: a reply about another unit must not move the commander", g.goPts, g.gpsCalls)
	}
	if n := strings.Count(notesOf(rep), "best effort"); n != 1 {
		t.Fatalf("%d best-effort notes\n%s", n, notesOf(rep))
	}
}

// Every `.gps` the start sends is booked like the `.go xyz` lines: in setup_shortcuts (GM start point), in the start's gm_commands and in
// remote.gm_commands. It is not a gm_shortcut (the buffs and the other shortcuts).
func TestStartBooksEachGPSLine(t *testing.T) {
	g := newStartFake(t)
	g.addMob(300, Point{250, 5, 0})
	g.gpsFn = gpsConst(5, 5) // one re-send, so two `.gps` lines
	rep, err := runFrom(t, g, twoLegPlan(), startOpts(), 1)
	if err != nil || rep.State != "done" {
		t.Fatalf("%v %s\n%s", err, rep.State, notesOf(rep))
	}
	var setup []string
	for _, s := range rep.SetupShortcuts {
		if strings.HasPrefix(s, ".gps") {
			setup = append(setup, s)
		}
	}
	if want := []string{".gps (GM start point)", ".gps (GM start point)"}; !slices.Equal(setup, want) {
		t.Fatalf("setup_shortcuts %q, want %q", setup, want)
	}
	if rep.Start == nil || strings.Count(strings.Join(rep.Start.GMCommands, "\n"), ".gps") != 2 {
		t.Fatalf("start gm_commands %v", rep.Start)
	}
	for _, s := range rep.Shortcuts {
		if strings.Contains(s.Detail, ".gps") {
			t.Fatalf("gm_shortcuts lists %q", s.Detail)
		}
	}
	if rep.Remote == nil || rep.Remote.GMCommands != 5 {
		t.Fatalf("remote gm_commands %+v, want .appear, two .go xyz and two .gps", rep.Remote)
	}
}
