package navmesh

import "errors"

// ServerPath is the core's own answer for one hop: the server's exact query
// (the core's PathInfo: BuildPolyPath + findSmoothPath, PathFinder.cpp) and
// its verdict inputs. Points are Detour's smooth-path points, the first near
// the start and the last what the run leader walks to; ActualEnd is that
// last point (the asked goal when the path is normal); StartDist/EndDist are
// the asked point's distance to its walk poly (FindWalkPoly; 0 when none).
//
// The core's PathType bits (PathFinder.h).
const (
	PathNormal     = 0x1
	PathShortcut   = 0x2 // unused: no swimming modelled
	PathIncomplete = 0x4
	PathNoPath     = 0x8
)

type ServerPath struct {
	Type      uint32
	Points    []Vec3
	ActualEnd Vec3
	Length    float32
	StartDist float32 // asked start to its walk poly (FindWalkPoly); 0 when none
	EndDist   float32 // asked goal to its walk poly (FindWalkPoly); 0 when none
}

// ServerPath replays the server's own query from one point to another on a
// map. The error is for what a retry will not fix (no folder, no map, a
// broken tile, no engine); an unreachable goal is a ServerPath with type
// PathNoPath, not an error.
func (p *Planner) ServerPath(mapID uint32, from, to Vec3) (ServerPath, error) {
	if p.dir == "" {
		return ServerPath{}, errors.New(`navmesh: no tiles folder; set "mmaps_dir" in the settings file`)
	}
	p.mu.Lock()
	m, err := p.mesh(mapID)
	p.mu.Unlock()
	if err != nil {
		return ServerPath{}, err
	}
	names, err := p.list(mapID)
	if err != nil {
		return ServerPath{}, err
	}
	p.mu.Lock()
	if p.maps[mapID] != m {
		p.mu.Unlock()
		return ServerPath{}, errClosed
	}
	m.missing = nil
	want := wantTiles(names, mapID, from, to, 3, p.max)
	p.mu.Unlock()
	data, err := p.fetch(m, want)
	if err != nil {
		return ServerPath{}, err
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.maps[mapID] != m {
		return ServerPath{}, errClosed
	}
	if err := p.load(m, want, data); err != nil {
		return ServerPath{}, err
	}
	typ, pts, actual, sdist, edist := m.eng.serverPath(from, to)
	sp := ServerPath{Type: typ, Points: pts, ActualEnd: actual, StartDist: sdist, EndDist: edist}
	for i := 1; i < len(pts); i++ {
		sp.Length += dist3D(pts[i-1], pts[i])
	}
	return sp, nil
}

// ServerMoveVerdict is TortoiseBots RemoteCommander::CheckMove
// (RemoteCommanderPolicy.h:1596-1619) for a run-leader order
// (fromRunLeader=true): "ok", "unreachable", "off-mesh" or "too-far"
// (reactDistance > 0 only; 0 skips it).
const (
	// AtLeadYd is how near (3D yd) the lead bot must stand to the point for
	// it to count as reached (RemoteCommanderPolicy.h:43).
	AtLeadYd = 1.0
	// NoProgressYd is the most an accepted incomplete path may end from where
	// the lead bot stands before the move is refused as no progress.
	NoProgressYd = 2.0
	// MaxMoveEndOffset is how far (3D yd) the path's end may lie from the
	// ordered point before the move is refused off-mesh (:39).
	MaxMoveEndOffset = 5.0
)

func ServerMoveVerdict(sp ServerPath, from, to Vec3, reactDistance float32) string {
	if reactDistance > 0 && dist3D(from, to) > reactDistance {
		return "too-far"
	}
	if dist3D(from, to) <= AtLeadYd {
		return "ok"
	}
	if sp.Type&PathNoPath != 0 {
		return "unreachable"
	}
	if sp.Type&PathIncomplete != 0 {
		if dist3D(sp.ActualEnd, from) <= NoProgressYd {
			return "unreachable"
		}
		return "ok"
	}
	if dist3D(sp.ActualEnd, to) > MaxMoveEndOffset {
		return "off-mesh"
	}
	return "ok"
}
