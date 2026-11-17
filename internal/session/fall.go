package session

import (
	"context"
	"math"
	"time"

	"github.com/TortoiseBots/turtle-agent/internal/events"
	"github.com/TortoiseBots/turtle-agent/internal/game"
	"github.com/TortoiseBots/turtle-agent/internal/navmesh"
	"github.com/TortoiseBots/turtle-agent/internal/opcode"
)

// FloorFinder lists the navmesh surfaces at a point, highest first (navmesh.Planner is one).
// A Planner that is one also lands falls and knockbacks on the ground.
type FloorFinder interface {
	Floors(mapID uint32, x, y, lo, hi float32) ([]navmesh.Floor, error)
}

var _ FloorFinder = (*navmesh.Planner)(nil)

// floorFunc is FloorFinder on the player's map.
type floorFunc func(x, y, lo, hi float32) ([]navmesh.Floor, error)

const (
	traceStep  = 0.05 // seconds between the samples a flight is checked against the floor at
	fallReach  = 600  // yards below a sample a floor is looked for: a 10 s flight drops less
	wallReach  = 50   // yards above a sample a floor counts as the top of a wall
	standSlack = 1.0  // a floor this far above the player's z is the one it stands on (*)
	startSlack = 2.0  // a floor this far above a flight's start is the ground it left (*)
	settleGap  = 0.05 // a floor this far below the player's z is the one it stands on
)

// (*) The mesh and the server's terrain height differ: live, a summon on a 45° slope put the
// player 0.55 yd under the mesh.

// floorFunc is the navmesh floor query on the player's map, or nil without one (no
// mmaps_dir, or a Planner that cannot answer).
func (s *Session) floorFunc() floorFunc {
	ff, ok := s.opts.Planner.(FloorFinder)
	if !ok {
		return nil
	}
	s.mu.Lock()
	m := s.mapID
	s.mu.Unlock()
	return func(x, y, lo, hi float32) ([]navmesh.Floor, error) { return ff.Floors(m, x, y, lo, hi) }
}

// highest is the highest floor from lo to hi in a list sorted highest first.
func highest(fl []navmesh.Floor, lo, hi float32) (navmesh.Floor, bool) {
	for _, f := range fl {
		if f.Z <= hi && f.Z >= lo {
			return f, true
		}
	}
	return navmesh.Floor{}, false
}

// trace flies the current arc against the navmesh, a sample every traceStep, up to limit
// seconds, and sets the landing. Between two samples the arc lands on a floor it came down
// onto, or one that rose under it by no more than a slope climbs in a step (rise) from the
// ground it was over. A surface that appears above the arc out of reach of that ground is
// an obstacle. Going up into it, or through it from below, with a floor under the arc, is a
// ceiling: the flight stops going across and drops from rest, as the client's does after a
// bump. Any other obstacle, and a surface above the arc with no floor under it, is a wall:
// the flight stops going across and flies on up and down, as the client slides along a wall. It says false when
// the arc meets no floor in time, or a query fails.
func (f *fallState) trace(limit float64) (float64, bool) {
	rise := float32(float64(f.kb.XYSpeed)*traceStep*1.5) + 0.5 // a 56° slope, and the mesh's error
	prev := f.pos(0)
	fl, err := f.floor(prev[0], prev[1], prev[2]-fallReach, prev[2]+startSlack)
	if err != nil {
		return 0, false
	}
	ground := float32(-math.MaxFloat32) // the highest floor under the last sample
	if g, ok := highest(fl, -math.MaxFloat32, prev[2]+startSlack); ok {
		ground = g.Z
	}
	for i := 1; float64(i-1)*traceStep < limit; {
		t, t0 := min(float64(i)*traceStep, limit), float64(i-1)*traceStep
		p := f.pos(t)
		top := max(prev[2], p[2]) + rise
		fl, err := f.floor(p[0], p[1], p[2]-fallReach, p[2]+wallReach)
		if err != nil {
			return 0, false
		}
		across := f.stopXY > t && f.kb.XYSpeed != 0
		up := p[2] > prev[2]
		obstacle := false
		if g, ok := highest(fl, p[2]-0.01, top); ok {
			if g.Z <= prev[2]+0.01 || g.Z <= ground+rise {
				f.landOn(g, t0, t, prev[2], rise)
				return f.land.Sub(f.start).Seconds(), true
			}
			obstacle = true
		}
		if g, ok := highest(fl, prev[2]+0.01, p[2]-0.01); ok && up && g.Z > ground+rise {
			obstacle = true // gone up through it from below
		}
		if !obstacle && across && len(fl) > 0 && fl[0].Z > top {
			_, under := highest(fl, -math.MaxFloat32, top)
			obstacle = !under
		}
		_, under := highest(fl, -math.MaxFloat32, p[2]-0.01)
		switch {
		case obstacle && up && under && f.bumpT > t:
			f.bumpT, f.stopXY = t0, min(f.stopXY, t0) // a ceiling: check this sample again
			continue
		case obstacle && across:
			f.stopXY = t0 // a wall: check this sample again
			continue
		}
		if g, ok := highest(fl, -math.MaxFloat32, p[2]+0.01); ok {
			ground = g.Z
		} else {
			ground = -math.MaxFloat32
		}
		prev = p
		i++
	}
	return 0, false
}

// landOn sets the landing on floor g, met between samples t0 and t1. Coming down onto it,
// the landing is where the arc meets the floor (by bisection, the floor followed near g's
// height as the arc goes across); a floor that rose above the arc (a slope going across) is
// landed on at t1.
func (f *fallState) landOn(g navmesh.Floor, t0, t1 float64, z0, rise float32) {
	at := func(t float64) ([3]float32, navmesh.Floor) {
		p := f.pos(t)
		fl, err := f.floor(p[0], p[1], g.Z-rise, g.Z+rise)
		best := g
		if err == nil {
			for i, c := range fl {
				if i == 0 || math.Abs(float64(c.Z-g.Z)) < math.Abs(float64(best.Z-g.Z)) {
					best = c
				}
			}
		}
		return p, best
	}
	p, floor := at(t1)
	t := t1
	if z0 >= g.Z {
		lo, hi := t0, t1
		for range 12 {
			mid := (lo + hi) / 2
			if q, fm := at(mid); q[2] > fm.Z {
				lo = mid
			} else {
				hi = mid
			}
		}
		t = hi
		p, floor = at(t)
	}
	f.land = f.start.Add(time.Duration(t * float64(time.Second)))
	p[2] = floor.Z
	f.landPos, f.onFloor, f.water = p, true, floor.Flags&navmesh.NavWater != 0
}

// floorKind says what the landing is on, for the landed event.
func (f *fallState) floorKind() string {
	switch {
	case !f.onFloor:
		return "guess"
	case f.water:
		return "water"
	}
	return "navmesh"
}

// describe is the event data of a flight that began at from at now.
func (f *fallState) describe(state string, from [3]float32, now time.Time) map[string]any {
	return map[string]any{"state": state, "x": from[0], "y": from[1], "z": from[2],
		"speed_xy": f.kb.XYSpeed, "speed_z": f.kb.ZSpeed, "flight_ms": f.land.Sub(now).Milliseconds(),
		"land_x": f.landPos[0], "land_y": f.landPos[1], "land_z": f.landPos[2], "floor": f.floorKind()}
}

// dropToFloor looks for the navmesh floor under where a GM teleport put the player. On it
// (within settleGap below or standSlack above), the z is known; above it, the player falls
// to it from rest as the client does. The fall begins with a heartbeat at the teleport's
// position: the server keeps the player's movement flags across a teleport
// (Unit::TeleportPositionRelocation changes only the position, Unit.cpp:10555-10568) and
// the anticheat has no grace for the first packet after one (Movement::HandleTeleport has
// no caller), so a first packet that had already dropped would break the z bound of a
// player standing still (Movement.cpp:863-867, :593-595). Without a navmesh, or with no
// floor under the point, the z stays a guess.
//
// The search, and the fall's trace with it, runs off the loop: it may read a tile or wait
// behind a path_to that holds the planner, and the loop must keep its heartbeats, acks and
// pings on time (#42). The player stands still at the teleport's position meanwhile; the
// answer is dropped when the player has been moved since (a teleport, a walk, a knockback).
func (s *Session) dropToFloor() {
	floor := s.floorFunc()
	if floor == nil {
		return
	}
	s.mu.Lock()
	p, o := [3]float32{s.pos[0], s.pos[1], s.pos[2]}, s.pos[3]
	s.mu.Unlock()
	placed, term := s.placed, fallTerm(s.toggles)
	began := s.now() // the loop's clock, read on the loop; startDrop moves the flight to its start
	go func() {
		fl, err := floor(p[0], p[1], p[2]-fallReach, p[2]+standSlack)
		if err != nil {
			return
		}
		g, ok := highest(fl, -math.MaxFloat32, p[2]+standSlack)
		if !ok {
			return
		}
		var f *fallState
		if p[2]-g.Z > settleGap {
			kb := game.KnockBack{Cos: float32(math.Cos(float64(o))), Sin: float32(math.Sin(float64(o)))}
			f = &fallState{kb: kb, began: began, startZ: p[2], floor: floor}
			f.arc(p, began, 0, term, false)
		}
		_, _ = s.do(context.Background(), func() (any, error) {
			if s.placed != placed || s.fall != nil || s.walk != nil || s.last.pos != p {
				return nil, nil // moved since
			}
			if f == nil {
				s.mu.Lock()
				s.zUnknown = g.Flags&navmesh.NavWater != 0
				s.mu.Unlock()
				return nil, nil
			}
			// A failed write closes the socket, and the loop ends on the read error.
			return nil, s.startDrop(f, p, s.now())
		})
	}()
}

// startDrop starts a traced fall from rest at p, moved on to begin at now.
func (s *Session) startDrop(f *fallState, p [3]float32, now time.Time) error {
	d := now.Sub(f.began)
	f.began, f.start, f.land = now, f.start.Add(d), f.land.Add(d)
	f.nextBeat = now.Add(500 * time.Millisecond)
	s.fall = f
	if term := fallTerm(s.toggles); term != f.term { // feather fall came or went meanwhile
		f.restart(now, term)
		s.traceFall(now)
	}
	s.mu.Lock()
	s.falling = true
	mi := s.moveInfoAt(s.currentFlags(), now)
	s.mu.Unlock()
	if err := s.sendMove(opcode.MSGMoveHeartbeat, mi, game.BuildMovement(mi)); err != nil {
		return err
	}
	s.emit(events.Event{Kind: events.KindSession, Data: f.describe("falling", p, now)})
	return nil
}

// traceWait bounds how long a flight waits for its trace before it keeps the guess landing.
// No heartbeat goes out meanwhile, as the trace may still bend the arc (a wall, a ceiling);
// with the 100 ms tick the next one is at most 600 ms after the packet before it, inside the
// anticheat's 1 s (Movement.cpp:600-604).
const traceWait = 300 * time.Millisecond

// traceFall traces the current arc against the navmesh off the loop, on a copy of the
// flight, and hands the landing back to the loop (s.do), which stays the flight's only
// owner. A trace that answers after traceWait, after the arc was restarted, or after the
// flight ended is dropped (#42).
func (s *Session) traceFall(now time.Time) {
	f := s.fall
	if f == nil || f.floor == nil {
		s.resolveFall()
		return
	}
	s.traceSeq++
	seq := s.traceSeq
	f.tracing, f.traceBy = seq, now.Add(traceWait)
	c := *f
	go func() {
		c.traced()
		_, _ = s.do(context.Background(), func() (any, error) {
			if s.fall != f || f.tracing != seq {
				return nil, nil
			}
			f.stopXY, f.bumpT, f.land, f.landPos, f.onFloor, f.water = c.stopXY, c.bumpT, c.land, c.landPos, c.onFloor, c.water
			s.resolveFall()
			return nil, nil
		})
	}()
}

// resolveFall ends the wait for a trace and emits the flight's announcement, now that its
// landing is known.
func (s *Session) resolveFall() {
	f := s.fall
	if f == nil {
		return
	}
	f.tracing = 0
	if f.announce != "" {
		s.emit(events.Event{Kind: events.KindSession, Data: f.describe(f.announce, f.annFrom, f.annAt)})
		f.announce = ""
	}
}
