package dungeon

import (
	"context"
	"fmt"
	"math"
	"regexp"
	"strconv"
	"time"
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
	// gpsMissingGround is the most GroundZ may read when the core has no ground height there: indoors it prints
	// INVALID_HEIGHT_VALUE (-200000), e.g. `GroundZ: -200000.000000 FloorZ: -62.260399` (BRD). A GroundZ at or
	// below this is missing: FloorZ alone is used. A FloorZ below gpsNoHeight still fails the parse.
	gpsMissingGround = -100000.0
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
// which may carry other lines. ok is false when the X/Y line or FloorZ is not in the text or when FloorZ has no height data (the core
// prints -200000). A missing GroundZ (at or below -100000, indoors) falls back to FloorZ, so the floor under the commander is still known.
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
	if err1 != nil || err2 != nil || err3 != nil || err4 != nil || f < gpsNoHeight {
		return GPSReply{}, false
	}
	if g <= gpsMissingGround {
		g = f // no ground height (indoors): judge by FloorZ alone
	} else if g < gpsNoHeight {
		return GPSReply{}, false
	}
	return GPSReply{X: x, Y: y, GroundZ: g, FloorZ: f}, true
}

// floorHands is what the floor check needs of the game.
type floorHands interface {
	GoXYZ(ctx context.Context, mapID uint32, at Point) error
	GPS(ctx context.Context) (string, error)
	AgentPlace() (Point, uint32, bool)
}
type floorHooks struct {
	gm     func(line string)                                // a GM line was sent (".gps", ".go xyz ...")
	note   func(text string)                                // a note ("the commander stands at z ...", the best-effort warning)
	warned *bool                                            // the best-effort warning was given already
	wait   func(ctx context.Context, d time.Duration) error // the delay between stale `.gps` re-reads (nil: no wait)
}

// readGPSWith is readGPS's body: it sends `.gps` (booked as a GM line) and parses the reply. ok is false (with a nil error, and one note
// through the hooks) when the check cannot be made: the line failed, the reply has no X/Y, GroundZ and FloorZ, or it is about another unit
// (its X/Y is more than startNear from the commander, e.g. a mob the commander has selected). The `.go xyz` race (a reply about the old
// spot right after the move) is not filtered here: floorCheck's settle loop judges staleness against the target point. The error is the
// context's.
func readGPSWith(ctx context.Context, h floorHands, hk floorHooks) (reply GPSReply, ok bool, err error) {
	hk.gm(".gps")
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
	if hk.warned != nil && !*hk.warned {
		*hk.warned = true
		hk.note("warning: the commander's z could not be checked with .gps (" + gerr.Error() + "); the z check is best effort and the start goes on")
	}
	return GPSReply{}, false, nil
}

// readGPSRaw sends `.gps` (booked as a GM line) and parses the reply with no distance check: ok is false (with a nil error and no note)
// when the line failed or the reply has no usable X/Y and FloorZ. The settle loop below judges staleness against the target point.
func readGPSRaw(ctx context.Context, h floorHands, hk floorHooks) (reply GPSReply, ok bool, err error) {
	hk.gm(".gps")
	text, gerr := h.GPS(ctx)
	if err := ctx.Err(); err != nil {
		return GPSReply{}, false, err
	}
	if gerr != nil {
		return GPSReply{}, false, nil
	}
	reply, ok = ParseGPS(text)
	if !ok {
		return GPSReply{}, false, nil
	}
	return reply, true, nil
}

// readGPS sends `.gps` (booked as a GM start line) and parses the reply. ok is false (with a nil error, and one note per driver) when the
// check cannot be made: the line failed, the reply has no X/Y, GroundZ and FloorZ, or it is about another unit (its X/Y is more than startNear
// from the commander, e.g. a mob the commander has selected). The error is the context's.
func (d *RemoteDriver) readGPS(ctx context.Context, h StartHands) (reply GPSReply, ok bool, err error) {
	return readGPSWith(ctx, h, floorHooks{gm: d.startGM, note: d.startNote, warned: &d.gpsWarned})
}

// settleGPSDelayYd is how close a `.gps` X/Y must be to the point of the `.go xyz` to be about it: a stale reply (the race right
// after the move) describes the old spot, far from the point. settleGPSDelayTries re-reads that many times, settleGPSDelayWait apart,
// waiting through the floor hooks' wait (the stage passes its sleep; nil waits nothing and the check is one read, as before).
const (
	settleGPSDelayTries = 4
	settleGPSDelayWait  = 750 * time.Millisecond
	settleGPSDelayYd    = 5.0
)

// settleGPSDelay re-reads `.gps` until its X/Y is within settleGPSDelayYd of at (the race: right after the `.go xyz` the reply may
// describe the old spot). The first read decides the shape: unreadable (a failed line, an unparseable reply, one about another unit
// far from both the commander and the target) stays best effort with one read, as before; a parsed reply far from at re-reads up to
// settleGPSDelayTries times, and refuses when it never settles: the commander did not arrive at the point `.gps` can check.
func settleGPSDelay(ctx context.Context, h floorHands, at Point, hk floorHooks, wait func(context.Context, time.Duration) error) (GPSReply, bool, error) {
	first, ok, err := readGPSRaw(ctx, h, hk)
	if err != nil || !ok {
		return GPSReply{}, false, err
	}
	for i := range settleGPSDelayTries {
		if i > 0 {
			if err := wait(ctx, settleGPSDelayWait); err != nil {
				return GPSReply{}, false, err
			}
			var rok bool
			var rerr error
			first, rok, rerr = readGPSRaw(ctx, h, hk)
			if rerr != nil {
				return GPSReply{}, false, rerr
			}
			if !rok {
				continue
			}
		}
		here, _, _ := h.AgentPlace()
		farFromHere := hdist(here, Point{X: float32(first.X), Y: float32(first.Y)}) > startNear
		farFromAt := hdist(Point{X: float32(first.X), Y: float32(first.Y)}, at) > settleGPSDelayYd
		switch {
		case !farFromAt:
			return first, true, nil // at the point (or the old spot is the point): the race is over
		case farFromHere:
			continue // stale (or about another unit): re-read
		default:
			return first, true, nil // about the commander but not the point: floorCheck judges it
		}
	}
	return GPSReply{}, false, fmt.Errorf("the commander did not arrive at the stage point: .gps shows X %.0f Y %.0f, the stage point is %.0f %.0f", first.X, first.Y, at.X, at.Y)
}

// floorCheck is checkStartZ's body: the z check after the `.go xyz` to at (see above). floor/known: the last FloorZ read (known false
// when .gps gave nothing usable).
func floorCheck(ctx context.Context, h floorHands, mapID uint32, at Point, hk floorHooks) (floor float64, known bool, err error) {
	var r GPSReply
	var ok bool
	if hk.wait != nil {
		var serr error
		r, ok, serr = settleGPSDelay(ctx, h, at, hk, hk.wait)
		if serr != nil {
			return 0, false, serr
		}
	} else if r, ok, err = readGPSWith(ctx, h, hk); err != nil || !ok {
		return 0, false, err
	}
	ground, floor := r.GroundZ, r.FloorZ
	here, _, _ := h.AgentPlace()
	if math.Abs(float64(here.Z)-floor) > startZTol {
		fix := Point{X: at.X, Y: at.Y, Z: float32(floor + startFloorLift)}
		hk.note(fmt.Sprintf("the commander stands at z %.1f, FloorZ is %.1f: .go xyz again at z %.1f", here.Z, floor, fix.Z))
		if err := h.GoXYZ(ctx, mapID, fix); err != nil {
			return 0, false, fmt.Errorf(".go xyz %.1f %.1f %.1f (z re-send): %w", fix.X, fix.Y, fix.Z, err)
		}
		hk.gm(fmt.Sprintf(".go xyz %.2f %.2f %.2f %d", fix.X, fix.Y, fix.Z, mapID))
		if r, ok, err = readGPSWith(ctx, h, hk); err != nil || !ok {
			return 0, false, err
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
		return 0, false, fmt.Errorf("%s after .go xyz: target z %.1f, GroundZ %.1f, FloorZ %.1f, commander z %.1f; the start is refused (the party would be brought to a commander that is under the ground or in the air)",
			why, at.Z, ground, floor, here.Z)
	}
	return floor, true, nil
}

// checkStartZ is the z check after the `.go xyz` to at (see above).
func (d *RemoteDriver) checkStartZ(ctx context.Context, h StartHands, at Point) error {
	_, _, err := floorCheck(ctx, h, d.plan.Map, at, floorHooks{gm: d.startGM, note: d.startNote, warned: &d.gpsWarned})
	return err
}
