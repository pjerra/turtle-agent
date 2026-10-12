package session

import (
	"context"
	"errors"
	"fmt"
	"math"
	"time"

	"github.com/pjerra/turtle-agent/internal/navmesh"
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
// "walk" (one straight segment From -> To; Text the error that ended it early) or
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

// WalkPath plans a path on the navmesh and walks it, one straight walk per segment. A
// server move (fear, a scripted move, a same-map teleport) or a root ends the segment
// early: walk_path waits until the server lets go, plans again from where the player is
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
		for i := 1; i < len(path.Points) && !interrupted; i++ {
			b := path.Points[i]
			var g []navmesh.Vec3
			if i-1 < len(path.Ground) {
				g = path.Ground[i-1]
			}
			_, before := s.where()
			err := s.walkStraight(ctx, b, math.Inf(1), g)
			_, after := s.where()
			rep.Walked += dist3(before, after)
			st := TraceStep{Kind: "walk", From: &before, To: &b}
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
			case errors.Is(err, errServerMoved), errors.Is(err, errTeleported), errors.Is(err, errRooted), errors.Is(err, errRootedNow):
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

// interruption is the cause without walk_to's advice about waiting.
func interruption(err error) string {
	for _, e := range []error{errServerMoved, errTeleported, errRooted, errRootedNow} {
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
