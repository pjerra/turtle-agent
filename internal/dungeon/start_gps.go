package dungeon

import (
	"context"
	"fmt"
	"math"
	"regexp"
	"strconv"
)

// The z check of the start (issue #292): `.go xyz X Y Z` on a point whose z is a few yards off puts the commander under the ground or in
// the air, and the lead bot and the party are then brought to a commander nobody can reach. After the move `.gps` is read, the commander is
// put on the floor once if it stands off it, and the start is refused when it still does. The read is best effort: a server that answers
// no `.gps` (or in another wording) or a commander that may not send it never blocks the start.
const (
	startZTol      = 0.5 // yards: the commander stands on the floor within this of FloorZ
	startFloorLift = 0.3 // yards above FloorZ the one re-send aims at
	startLayerTol  = 3.0 // yards: FloorZ below the ground is the target's own layer within this of the target z
	gpsNoHeight    = -90000.0
)

// gpsZ reads "GroundZ: <f> FloorZ: <f>" of the core's `.gps` reply (Commands.cpp HandleGPSCommand: mangos_string 101 ends with
// "GroundZ: %f FloorZ: %f Have height data (Map: %u VMap: %u)"). The core prints with '.' as the decimal point whatever the locale.
var gpsZ = regexp.MustCompile(`GroundZ:\s*(-?\d+(?:\.\d+)?)\s+FloorZ:\s*(-?\d+(?:\.\d+)?)`)

// ParseGPS reads GroundZ (the highest surface over the commander) and FloorZ (the surface under it) out of a `.gps` reply, which may carry
// other lines. ok is false when they are not in the text or when the core had no height data there (it prints -100000).
func ParseGPS(text string) (groundZ, floorZ float64, ok bool) {
	m := gpsZ.FindStringSubmatch(text)
	if m == nil {
		return 0, 0, false
	}
	g, err1 := strconv.ParseFloat(m[1], 64)
	f, err2 := strconv.ParseFloat(m[2], 64)
	if err1 != nil || err2 != nil || g < gpsNoHeight || f < gpsNoHeight {
		return 0, 0, false
	}
	return g, f, true
}

// readGPS sends `.gps` and parses the reply. ok is false (with a nil error, and one note per driver) when the check cannot be made; the
// error is the context's.
func (d *RemoteDriver) readGPS(ctx context.Context, h StartHands) (ground, floor float64, ok bool, err error) {
	text, gerr := h.GPS(ctx)
	if err := ctx.Err(); err != nil {
		return 0, 0, false, err
	}
	if gerr == nil {
		if ground, floor, ok = ParseGPS(text); ok {
			return ground, floor, true, nil
		}
		gerr = fmt.Errorf("no GroundZ/FloorZ in the reply %q", text)
	}
	if !d.gpsWarned {
		d.gpsWarned = true
		d.startNote("warning: the commander's z could not be checked with .gps (" + gerr.Error() + "); the z check is best effort and the start goes on")
	}
	return 0, 0, false, nil
}

// checkStartZ is the z check after the `.go xyz` to at (see above).
func (d *RemoteDriver) checkStartZ(ctx context.Context, h StartHands, at Point) error {
	ground, floor, ok, err := d.readGPS(ctx, h)
	if err != nil || !ok {
		return err
	}
	here, _, _ := h.AgentPlace()
	if math.Abs(float64(here.Z)-floor) > startZTol {
		fix := Point{X: at.X, Y: at.Y, Z: float32(floor + startFloorLift)}
		d.startNote(fmt.Sprintf("the commander stands at z %.1f, FloorZ is %.1f: .go xyz again at z %.1f", here.Z, floor, fix.Z))
		if err := h.GoXYZ(ctx, d.plan.Map, fix); err != nil {
			return fmt.Errorf(".go xyz %.1f %.1f %.1f (z re-send): %w", fix.X, fix.Y, fix.Z, err)
		}
		d.startGM(fmt.Sprintf(".go xyz %.2f %.2f %.2f %d", fix.X, fix.Y, fix.Z, d.plan.Map))
		if ground, floor, ok, err = d.readGPS(ctx, h); err != nil || !ok {
			return err
		}
		here, _, _ = h.AgentPlace()
	}
	offFloor := math.Abs(float64(here.Z)-floor) > startZTol
	otherLayer := math.Abs(ground-floor) > startZTol && math.Abs(floor-float64(at.Z)) > startLayerTol
	if offFloor || otherLayer {
		why := "the commander does not stand on the floor"
		if !offFloor {
			why = "the floor under the commander is another layer than the target point"
		}
		return fmt.Errorf("%s after .go xyz: target z %.1f, GroundZ %.1f, FloorZ %.1f, commander z %.1f; the start is refused (the party would be brought to a commander that is under the ground or in the air)",
			why, at.Z, ground, floor, here.Z)
	}
	return nil
}
