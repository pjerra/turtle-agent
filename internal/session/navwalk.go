package session

import (
	"context"
	"errors"
	"fmt"
	"math"
	"time"

	"github.com/pjerra/turtle-agent/internal/game"
	"github.com/pjerra/turtle-agent/internal/navmesh"
	"github.com/pjerra/turtle-agent/internal/opcode"
)

// Planner plans a path on the navmesh (navmesh.Planner; a fake in tests).
type Planner interface {
	Plan(mapID uint32, from, to navmesh.Vec3) (navmesh.Path, error)
}

var ErrNoNavmesh = errors.New(`path_to and walk_path need the server's mmaps: set "mmaps_dir" in the settings file`)

var errStillBlocked = errors.New("still rooted or moved by the server")

const (
	maxReplans = 8  // times walk_path plans again after a server move; the next one ends it
	maxHops    = 32 // times it plans on from the end of a partial path it made progress on
	minHop     = 10 // yards a partial path must have gained to plan on from its end
)

// rootWait is how long walk_path waits for a root to end (a var for the tests).
var rootWait = 15 * time.Second

// PathInfo is a planned path from the player's position.
type PathInfo struct {
	Map  uint32       `json:"map"`
	From navmesh.Vec3 `json:"from"`
	To   navmesh.Vec3 `json:"to"`
	navmesh.Path
}

// WalkReport is how a walk_path went. Result is "arrived", "partial" (it stopped short:
// the mesh has no route all the way, or the server kept moving the player, or took it to
// another map) or "no_path" (nothing walked).
type WalkReport struct {
	Result   string       `json:"result"`
	Reason   string       `json:"reason,omitempty"`
	Goal     navmesh.Vec3 `json:"goal"`
	Position navmesh.Vec3 `json:"position"`
	DistLeft float32      `json:"dist_left"`
	Walked   float32      `json:"walked"`
	Replans  int          `json:"replans"`
	Seconds  float64      `json:"seconds"`
	Trace    []TraceStep  `json:"trace"`
}

// TraceStep is one step of a walk_path: "plan" (Status, Points, Length, Text the reason),
// "walk" (the walk along a plan's path, From -> its end To; Text the error that ended it
// early) or
// "interrupted" (Text why, From where the server left the player).
type TraceStep struct {
	TMS    int64          `json:"t_ms"`
	Kind   string         `json:"kind"`
	Status string         `json:"status,omitempty"`
	From   *navmesh.Vec3  `json:"from,omitempty"`
	To     *navmesh.Vec3  `json:"to,omitempty"`
	Points []navmesh.Vec3 `json:"points,omitempty"`
	Length float32        `json:"length,omitempty"`
	Text   string         `json:"text,omitempty"`
}

// where is the player's map and position.
func (s *Session) where() (uint32, navmesh.Vec3) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.mapID, navmesh.Vec3{s.pos[0], s.pos[1], s.pos[2]}
}

// PathTo plans a path on the navmesh from the player's position, without moving.
func (s *Session) PathTo(ctx context.Context, x, y, z float32) (PathInfo, error) {
	if s.opts.Planner == nil {
		return PathInfo{}, ErrNoNavmesh
	}
	if err := s.inWorld(); err != nil {
		return PathInfo{}, err
	}
	m, from := s.where()
	to := navmesh.Vec3{x, y, z}
	p, err := s.opts.Planner.Plan(m, from, to)
	if err != nil {
		return PathInfo{}, err
	}
	return PathInfo{Map: m, From: from, To: to, Path: p}, nil
}

// WalkPath plans a path on the navmesh and walks it as one walk, turning at the corners
// without stopping, as a client does. A server move (fear, a scripted move, a same-map
// teleport) or a root ends the walk early: walk_path waits until the server lets go, plans again from where the player is
// and walks on. The error is for what ends the call itself (no navmesh, a planner error,
// a lost connection, a cancelled request); every other outcome is in the report.
func (s *Session) WalkPath(ctx context.Context, x, y, z float32) (WalkReport, error) {
	if s.opts.Planner == nil {
		return WalkReport{}, ErrNoNavmesh
	}
	if err := s.inWorld(); err != nil {
		return WalkReport{}, err
	}
	began := time.Now()
	goal := navmesh.Vec3{x, y, z}
	rep := WalkReport{Goal: goal, Trace: []TraceStep{}}
	add := func(st TraceStep) {
		st.TMS = time.Since(began).Milliseconds()
		rep.Trace = append(rep.Trace, st)
	}
	end := func(result, reason string) WalkReport {
		rep.Result, rep.Reason = result, reason
		_, rep.Position = s.where()
		rep.DistLeft = dist3(rep.Position, goal)
		rep.Seconds = time.Since(began).Seconds()
		return rep
	}
	startMap, _ := s.where()
	hops := 0
	for {
		if err := s.waitFree(ctx); errors.Is(err, errStillBlocked) {
			return end("partial", err.Error()), nil
		} else if err != nil {
			if ctx.Err() != nil {
				return end("partial", "cancelled"), ctx.Err()
			}
			return end("partial", err.Error()), err
		}
		m, from := s.where()
		if m != startMap {
			return end("partial", errNewMap.Error()), nil
		}
		path, err := s.opts.Planner.Plan(m, from, goal)
		if err != nil {
			return end("partial", err.Error()), err
		}
		add(TraceStep{Kind: "plan", Status: string(path.Status), From: &from, Points: path.Points, Length: path.Length, Text: path.Reason})
		if path.Status == navmesh.NoPath {
			if rep.Walked == 0 {
				return end("no_path", path.Reason), nil
			}
			return end("partial", path.Reason), nil
		}
		_, planned := s.where()
		interrupted := false
		if len(path.Points) > 1 {
			pts, corners := polyline(planned, path)
			s.mu.Lock()
			s.walkDist = 0
			s.mu.Unlock()
			err := s.walkStraight(ctx, pts[len(pts)-1], walkOpts{via: pts[1 : len(pts)-1], corners: corners, onMesh: true})
			_, after := s.where()
			// How far along the path the walk got, as stopWalk or the arrival recorded it: a server
			// move (fear, charge, teleport) is not walked past the point where it stopped the walk.
			s.mu.Lock()
			rep.Walked += float32(s.walkDist)
			s.mu.Unlock()
			st := TraceStep{Kind: "walk", From: &planned, To: &pts[len(pts)-1]}
			if err != nil {
				st.Text = err.Error()
			}
			add(st)
			switch {
			case err == nil:
			case ctx.Err() != nil:
				return end("partial", "cancelled"), ctx.Err()
			case errors.Is(err, errNewMap):
				return end("partial", errNewMap.Error()), nil
			case isInterruption(err):
				interrupted = true
				add(TraceStep{Kind: "interrupted", From: &after, Text: interruption(err)})
			default:
				return end("partial", err.Error()), err
			}
		}
		if !interrupted {
			if path.Status == navmesh.Complete {
				return end("arrived", ""), nil
			}
			// A partial path that got somewhere may end at the edge of the tiles loaded for
			// it: plan on from its end. One that got nowhere is as close as the mesh goes.
			if _, here := s.where(); dist3(planned, here) < minHop || hops >= maxHops {
				return end("partial", path.Reason), nil
			}
			hops++
			continue
		}
		if rep.Replans == maxReplans {
			return end("partial", fmt.Sprintf("the server moved the player %d times; gave up", maxReplans+1)), nil
		}
		rep.Replans++
	}
}

// interruptions are the server's doings a walk_path waits out and plans round.
var interruptions = []error{errServerMoved, errTeleported, errRooted, errRootedNow, errKnockedBack, errAirborne}

func isInterruption(err error) bool {
	for _, e := range interruptions {
		if errors.Is(err, e) {
			return true
		}
	}
	return false
}

// interruption is the cause without walk_to's advice about waiting.
func interruption(err error) string {
	for _, e := range interruptions {
		if errors.Is(err, e) {
			return e.Error()
		}
	}
	return err.Error()
}

// waitFree waits while a server spline moves the player, and up to rootWait for a root
// to end.
func (s *Session) waitFree(ctx context.Context) error {
	deadline := time.Now().Add(rootWait)
	for {
		v, err := s.do(ctx, func() (any, error) {
			if err := s.inWorld(); err != nil {
				return nil, err
			}
			if left := time.Until(s.splineUntil); left > 0 {
				return left + 20*time.Millisecond, nil
			}
			if f := s.fall; f != nil { // a knockback flight: wait for the landing
				return max(time.Until(f.land), 0) + 20*time.Millisecond, nil
			}
			if s.rooted {
				return 100 * time.Millisecond, nil
			}
			return time.Duration(0), nil
		})
		if err != nil {
			return err
		}
		wait := v.(time.Duration)
		if wait == 0 {
			return nil
		}
		if time.Now().Add(wait).After(deadline) {
			return fmt.Errorf("%w after %s", errStillBlocked, rootWait)
		}
		select {
		case <-time.After(wait):
		case <-ctx.Done():
			return ctx.Err()
		case <-s.done:
			return ErrClosed
		}
	}
}

func dist3(a, b navmesh.Vec3) float32 {
	dx, dy, dz := float64(a[0]-b[0]), float64(a[1]-b[1]), float64(a[2]-b[2])
	return float32(math.Sqrt(dx*dx + dy*dy + dz*dz))
}

// polyline is a planned path as one walk from the player's position: each leg's ground
// samples, then its end. corners are the indexes in the points between the ends (the walk's
// via) where a leg ends and the next begins.
func polyline(from navmesh.Vec3, path navmesh.Path) (pts [][3]float32, corners []int) {
	pts = [][3]float32{from}
	for i := 1; i < len(path.Points); i++ {
		if i-1 < len(path.Ground) {
			pts = append(pts, path.Ground[i-1]...)
		}
		pts = append(pts, path.Points[i])
		if i < len(path.Points)-1 {
			corners = append(corners, len(pts)-2)
		}
	}
	return pts, corners
}

// minTurn (radians) is the least change of facing at a corner worth a MSG_MOVE_SET_FACING.
const minTurn = 0.01

// setCorners splits the walk into legs that end at the given indexes in its via (pts[1:])
// and at its end, and faces it along the first.
func (w *walkState) setCorners(corners []int) {
	w.ends = w.ends[:0]
	for _, c := range corners {
		if e := c + 1; e > 0 && e < len(w.pts)-1 && (len(w.ends) == 0 || e > w.ends[len(w.ends)-1]) {
			w.ends = append(w.ends, e)
		}
	}
	w.ends = append(w.ends, len(w.pts)-1)
	w.leg = 0
	w.o = game.Facing(w.pts[0][0], w.pts[0][1], w.to[0], w.to[1]) // kept by a first leg straight up
	w.o = w.facing(0)
}

// legAt is the leg the walk is on after d yards.
func (w *walkState) legAt(d float64) int {
	for k, e := range w.ends {
		if d < w.cum[e] {
			return k
		}
	}
	return len(w.ends) - 1
}

// facing is the direction of leg k, corner to corner; w.o for a leg straight up or down.
func (w *walkState) facing(k int) float32 {
	a := w.pts[0]
	if k > 0 {
		a = w.pts[w.ends[k-1]]
	}
	b := w.pts[w.ends[k]]
	if math.Hypot(float64(b[0]-a[0]), float64(b[1]-a[1])) < 1e-3 {
		return w.o
	}
	return game.Facing(a[0], a[1], b[0], b[1])
}

// tickTurn turns a walk that has passed a corner since its last packet, as a client running
// a path does: MSG_MOVE_SET_FACING with MoveForward still set, from where the walk has got
// to on the new leg, no stop and start. The server holds it to the bounds of any movement
// packet (Movement.cpp:521-605: the run speed times the client-time gap since the last
// packet, checked by checkStep; no other check for a facing), which a point reached along
// the path at the run speed keeps, as the straight line to it is no longer than the path.
// A turn under minTurn from the facing last sent is not sent, and the next turn is measured
// from that same facing, so small corners add up to one. The next heartbeat comes 500 ms after
// a turn it sends. It reports whether it sent (or aborted the walk).
func (s *Session) tickTurn(w *walkState, now time.Time) (bool, error) {
	ax := s.walkAxis(now)
	leg := w.legAt(w.walked(ax))
	if leg == w.leg {
		return false, nil
	}
	o := w.facing(leg)
	w.leg = leg
	w.o = o
	s.mu.Lock()
	sent := s.pos[3] // the facing the server has from the last packet
	s.mu.Unlock()
	d := math.Abs(float64(o - sent))
	if d > math.Pi {
		d = 2*math.Pi - d
	}
	if w.arrived(ax) || d < minTurn { // the stop carries the last leg's facing
		return false, nil
	}
	w.nextBeat = now.Add(500 * time.Millisecond)
	s.syncWalk(now)
	s.mu.Lock()
	s.pos[3] = o
	mi := s.moveInfoAt(s.currentFlags(), now)
	s.mu.Unlock()
	if err := s.checkStep(mi); err != nil {
		s.walk = nil
		return true, s.abortWalk(w, err)
	}
	return true, s.sendWalkMove(opcode.MSGMoveSetFacing, mi, game.BuildMovement(mi), now)
}
