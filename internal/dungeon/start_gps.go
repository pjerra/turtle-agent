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
	// the core prints INVALID_HEIGHT_VALUE (-200000, GridMap.h:36) where it has no height data; anything below this is that
	gpsNoHeight = -90000.0
)

// gpsXY reads the "X: <f> Y: <f> Z: <f> Orientation" line of the core's `.gps` reply (mangos_string 101: "X: %f Y: %f Z: %f Orientation: %f"),
// the unit's own position. The line starts the line: "ZoneX:" and "ZoneY:" are not it.
var gpsXY = regexp.MustCompile(`(?m)^\s*X:\s*(-?\d+(?:\.\d+)?)\s+Y:\s*(-?\d+(?:\.\d+)?)`)

// gpsZ reads "GroundZ: <f> FloorZ: <f>" of the same reply ("GroundZ: %f FloorZ: %f Have height data (Map: %u VMap: %u)"). The core prints
// with '.' as the decimal point whatever the locale.
var gpsZ = regexp.MustCompile(`GroundZ:\s*(-?\d+(?:\.\d+)?)\s+FloorZ:\s*(-?\d+(?:\.\d+)?)`)

// GPSReply is what a `.gps` reply says: the position it is about (X, Y) and the GroundZ and FloorZ under it.
type GPSReply struct {
	X, Y, GroundZ, FloorZ float64
}

// ParseGPS reads the X/Y line and GroundZ (the highest surface over the commander) and FloorZ (the surface under it) out of a `.gps` reply,
// which may carry other lines. ok is false when any of them is not in the text or when the core had no height data there (it prints -200000).
func ParseGPS(text string) (r GPSReply, ok bool) {
	xy := gpsXY.FindStringSubmatch(text)
	z := gpsZ.FindStringSubmatch(text)
	if xy == nil || z == nil {
		return GPSReply{}, false
	}
	x, err1 := strconv.ParseFloat(xy[1], 64)
	y, err2 := strconv.ParseFloat(xy[2], 64)
	g, err3 := strconv.ParseFloat(z[1], 64)
	f, err4 := strconv.ParseFloat(z[2], 64)
	if err1 != nil || err2 != nil || err3 != nil || err4 != nil || g < gpsNoHeight || f < gpsNoHeight {
		return GPSReply{}, false
	}
	return GPSReply{X: x, Y: y, GroundZ: g, FloorZ: f}, true
}

// readGPS sends `.gps` (booked as a GM start line) and parses the reply. ok is false (with a nil error, and one note per driver) when the
// check cannot be made: the line failed, the reply has no X/Y, GroundZ and FloorZ, or it is about another unit (its X/Y is more than startNear
// from the commander, e.g. a mob the commander has selected). The error is the context's.
func (d *RemoteDriver) readGPS(ctx context.Context, h StartHands) (reply GPSReply, ok bool, err error) {
	d.startGM(".gps")
	text, gerr := h.GPS(ctx)
	if err := ctx.Err(); err != nil {
		return GPSReply{}, false, err
	}
	if gerr == nil {
		reply, ok = ParseGPS(text)
		if !ok {
			gerr = fmt.Errorf("no X/Y, GroundZ or FloorZ in the reply %q", text)
		} else if here, _, _ := h.AgentPlace(); hdist(here, Point{X: float32(reply.X), Y: float32(reply.Y)}) > startNear {
			gerr = fmt.Errorf("the reply is about (%.0f, %.0f), not the commander at (%.0f, %.0f)", reply.X, reply.Y, here.X, here.Y)
			ok = false
		} else {
			return reply, true, nil
		}
	}
	if !d.gpsWarned {
		d.gpsWarned = true
		d.startNote("warning: the commander's z could not be checked with .gps (" + gerr.Error() + "); the z check is best effort and the start goes on")
	}
	return GPSReply{}, false, nil
}

// checkStartZ is the z check after the `.go xyz` to at (see above).
func (d *RemoteDriver) checkStartZ(ctx context.Context, h StartHands, at Point) error {
	r, ok, err := d.readGPS(ctx, h)
	if err != nil || !ok {
		return err
	}
	ground, floor := r.GroundZ, r.FloorZ
	here, _, _ := h.AgentPlace()
	if math.Abs(float64(here.Z)-floor) > startZTol {
		fix := Point{X: at.X, Y: at.Y, Z: float32(floor + startFloorLift)}
		d.startNote(fmt.Sprintf("the commander stands at z %.1f, FloorZ is %.1f: .go xyz again at z %.1f", here.Z, floor, fix.Z))
		if err := h.GoXYZ(ctx, d.plan.Map, fix); err != nil {
			return fmt.Errorf(".go xyz %.1f %.1f %.1f (z re-send): %w", fix.X, fix.Y, fix.Z, err)
		}
		d.startGM(fmt.Sprintf(".go xyz %.2f %.2f %.2f %d", fix.X, fix.Y, fix.Z, d.plan.Map))
		if r, ok, err = d.readGPS(ctx, h); err != nil || !ok {
			return err
		}
		ground, floor = r.GroundZ, r.FloorZ
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
