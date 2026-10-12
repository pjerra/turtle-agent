// Package navmesh plans walking paths on the server's own navmesh: the mmaps the core's
// PathFinder uses (MaNGOS mmap version 6 tiles holding Detour version 7 data with 64-bit
// poly refs), run through the core's own Detour sources, vendored in detour/ (zlib).
//
// Tiles are read on demand from a local folder holding copies of the server's data/mmaps
// files: NNN.mmap (the map's dtNavMeshParams) and NNNyyxx.mmtile.
package navmesh

import (
	"errors"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
)

// Vec3 is a WoW position (x, y, z).
type Vec3 = [3]float32

// MoveMapSharedDefines.h NavTerrain.
const (
	NavGround = 0x01
	NavMagma  = 0x02
	NavSlime  = 0x04
	NavWater  = 0x08
	NavSteep  = 0x10 // slopes above the player's climb angle
)

// IncludeFlags and ExcludeFlags are the polygon filter of a player on foot: ground and water
// as the server's PathFinder::createFilter gives a player, and never lava, slime or slopes
// too steep to climb.
const (
	IncludeFlags = NavGround | NavWater
	ExcludeFlags = NavMagma | NavSlime | NavSteep
)

// GroundStep is the spacing of Path.Ground samples, in yards.
const GroundStep = 4

// smallMap is the tile count up to which a map loads whole (a dungeon).
const smallMap = 16

// MaxTiles bounds the tiles a map keeps loaded (a tile is 0.5-3 MB); past it the least
// recently used ones are unloaded, and a box of tiles that would not fit is cut to the
// tiles nearest the straight line from start to goal.
const MaxTiles = 64

// ErrNoEngine says the binary was built without cgo, so it has no Detour.
var ErrNoEngine = errors.New("navmesh: this turtle-agent was built without cgo, so it has no Detour engine; " +
	"rebuild with CGO_ENABLED=1 and a C++ compiler")

type Status string

const (
	Complete Status = "complete"
	Partial  Status = "partial" // the path ends at the reachable point nearest the goal
	NoPath   Status = "no_path"
)

// Path is a planned route. Points are Detour's straight-path corners, the first the start
// and the last the goal (or, when partial, the nearest point to it the mesh reaches).
// Ground[i] holds surface points every GroundStep yards strictly between Points[i] and
// Points[i+1], so a walk keeps to the ground between corners.
type Path struct {
	Status Status   `json:"status"`
	Reason string   `json:"reason,omitempty"`
	Points []Vec3   `json:"points"`
	Length float32  `json:"length"`
	Engine string   `json:"engine"`
	Ground [][]Vec3 `json:"-"`
}

// Planner plans paths on the tiles under one folder. It is safe for concurrent use.
type Planner struct {
	dir  string
	mu   sync.Mutex
	maps map[uint32]*mapMesh
	max  int // MaxTiles; tests lower it
}

type mapMesh struct {
	eng   *engine
	tiles map[string]*loaded
	tick  uint64
}

type loaded struct {
	ref  uint64 // Detour tile ref
	used uint64 // mapMesh.tick when a plan last wanted it
}

func NewPlanner(dir string) *Planner {
	return &Planner{dir: dir, maps: map[uint32]*mapMesh{}, max: MaxTiles}
}

// Close frees the Detour meshes.
func (p *Planner) Close() {
	p.mu.Lock()
	defer p.mu.Unlock()
	for id, m := range p.maps {
		m.eng.close()
		delete(p.maps, id)
	}
}

// Tiles lists the tile files loaded for a map.
func (p *Planner) Tiles(mapID uint32) []string {
	p.mu.Lock()
	defer p.mu.Unlock()
	m := p.maps[mapID]
	if m == nil {
		return nil
	}
	var out []string
	for n := range m.tiles {
		out = append(out, n)
	}
	slices.Sort(out)
	return out
}

// Plan finds a path from one point to another on a map. The error is for what a retry
// will not fix (no folder, no map, a broken tile, no engine); an unreachable goal is a
// Path with Status NoPath or Partial and a Reason.
func (p *Planner) Plan(mapID uint32, from, to Vec3) (Path, error) {
	if p.dir == "" {
		return Path{}, errors.New(`navmesh: no tiles folder; set "mmaps_dir" in the settings file`)
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	m, err := p.mesh(mapID)
	if err != nil {
		return Path{}, err
	}
	names, err := p.list(mapID)
	if err != nil {
		return Path{}, err
	}
	var path Path
	for _, margin := range []int{1, 3} {
		if err := p.load(m, wantTiles(names, mapID, from, to, margin, p.max)); err != nil {
			return Path{}, err
		}
		path = plan(m.eng, from, to)
		if path.Status == Complete || len(names) <= smallMap {
			break
		}
	}
	return path, nil
}

func (p *Planner) mesh(mapID uint32) (*mapMesh, error) {
	if m := p.maps[mapID]; m != nil {
		return m, nil
	}
	name := fmt.Sprintf("%03d.mmap", mapID)
	params, err := os.ReadFile(filepath.Join(p.dir, name))
	if err != nil {
		return nil, fmt.Errorf("navmesh: map %d: %w (copy the server's data/mmaps/%s and %03d*.mmtile into mmaps_dir)",
			mapID, err, name, mapID)
	}
	eng, err := newEngine(params)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", name, err)
	}
	m := &mapMesh{eng: eng, tiles: map[string]*loaded{}}
	p.maps[mapID] = m
	return m, nil
}

// list is the map's tile files in the folder, read each time so tiles copied in later count.
func (p *Planner) list(mapID uint32) ([]string, error) {
	ents, err := os.ReadDir(p.dir)
	if err != nil {
		return nil, fmt.Errorf("navmesh: %w", err)
	}
	prefix := fmt.Sprintf("%03d", mapID)
	var out []string
	for _, e := range ents {
		n := e.Name()
		if len(n) == len("0000000.mmtile") && strings.HasPrefix(n, prefix) && strings.HasSuffix(n, ".mmtile") {
			out = append(out, n)
		}
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("navmesh: no %s*.mmtile files in %s", prefix, p.dir)
	}
	return out, nil
}

// load makes the wanted tiles the loaded ones, unloading the least recently wanted others
// first when they would not all fit.
func (p *Planner) load(m *mapMesh, want []string) error {
	m.tick++
	missing := 0
	for _, n := range want {
		if t := m.tiles[n]; t != nil {
			t.used = m.tick
		} else {
			missing++
		}
	}
	for len(m.tiles)+missing > p.max {
		var old string
		for n, t := range m.tiles {
			if t.used != m.tick && (old == "" || t.used < m.tiles[old].used) {
				old = n
			}
		}
		if old == "" {
			break
		}
		if err := m.eng.removeTile(m.tiles[old].ref); err != nil {
			return fmt.Errorf("navmesh: %s: %w", old, err)
		}
		delete(m.tiles, old)
	}
	for _, n := range want {
		if m.tiles[n] != nil {
			continue
		}
		data, err := os.ReadFile(filepath.Join(p.dir, n))
		if err != nil {
			return fmt.Errorf("navmesh: %w", err)
		}
		ref, err := m.eng.addTile(data)
		if err != nil {
			return fmt.Errorf("navmesh: %s: %w", n, err)
		}
		m.tiles[n] = &loaded{ref: ref, used: m.tick}
	}
	return nil
}

func plan(eng *engine, from, to Vec3) Path {
	rc, pts, goal := eng.findPath(from, to)
	path := Path{Engine: engineName, Points: pts}
	switch rc {
	case 0:
		path.Status = Complete
	case 1, 2:
		path.Status = Partial
	case -1:
		return Path{Status: NoPath, Engine: engineName, Points: []Vec3{}, Reason: fmt.Sprintf(
			"start %s is not on the navmesh (no walkable polygon within 5 yd, or tiles missing)", fmtVec(from))}
	case -2:
		return Path{Status: NoPath, Engine: engineName, Points: []Vec3{}, Reason: fmt.Sprintf(
			"goal %s is not on the navmesh (no walkable polygon within 5 yd, or tiles missing)", fmtVec(to))}
	default:
		return Path{Status: NoPath, Engine: engineName, Points: []Vec3{}, Reason: "Detour found no path"}
	}
	for i := 1; i < len(pts); i++ {
		path.Length += dist3D(pts[i-1], pts[i])
		path.Ground = append(path.Ground, ground(eng, pts[i-1], pts[i]))
	}
	end := pts[len(pts)-1]
	switch rc {
	case 1:
		path.Reason = fmt.Sprintf("no navmesh route to the goal: the nearest reachable point %s is %.1f yd from the "+
			"goal's floor point %s", fmtVec(end), dist3D(end, goal), fmtVec(goal))
	case 2:
		path.Reason = fmt.Sprintf("the goal %s is %.1f yd from the navmesh; the path ends on the floor nearest it, %s",
			fmtVec(to), dist3D(to, goal), fmtVec(end))
	}
	return path
}

// ground samples the mesh surface every GroundStep yards between two corners. Each sample
// looks for the floor near the last one's height, so a hill is followed up and over and a
// bridge does not drop to the floor under it; a miss keeps the straight line's height.
func ground(eng *engine, a, b Vec3) []Vec3 {
	n := int(dist2D(a, b) / GroundStep)
	if n < 1 {
		return nil
	}
	out := make([]Vec3, 0, n)
	prev := a[2]
	for k := 1; k <= n; k++ {
		f := float32(k) * GroundStep / dist2D(a, b)
		if f >= 1 {
			break
		}
		q := Vec3{a[0] + (b[0]-a[0])*f, a[1] + (b[1]-a[1])*f, prev + (b[2]-a[2])/float32(n+1)}
		if c, ok := eng.nearest(q, Vec3{0.25, 0.25, 6}); ok && dist2D(c, q) < 0.05 {
			q[2] = c[2]
		} else {
			q[2] = a[2] + (b[2]-a[2])*f
		}
		prev = q[2]
		out = append(out, q)
	}
	return out
}

// wantTiles is the tiles to load for a path from one point to another: a small map whole,
// else the box of tiles round the two, margin tiles wider on each side, cut to the limit
// tiles nearest the straight line between them.
func wantTiles(names []string, mapID uint32, from, to Vec3, margin, limit int) []string {
	type cand struct {
		name string
		d    float64
	}
	r0, c0 := tileRC(from[0], from[1])
	r1, c1 := tileRC(to[0], to[1])
	var cs []cand
	for _, n := range names {
		var m, r, c int
		if _, err := fmt.Sscanf(n, "%03d%02d%02d.mmtile", &m, &r, &c); err != nil || uint32(m) != mapID {
			continue
		}
		if len(names) > smallMap && (r < min(r0, r1)-margin || r > max(r0, r1)+margin || c < min(c0, c1)-margin ||
			c > max(c0, c1)+margin) {
			continue
		}
		cs = append(cs, cand{n, segDist(float64(r), float64(c), float64(r0), float64(c0), float64(r1), float64(c1))})
	}
	slices.SortStableFunc(cs, func(a, b cand) int {
		if a.d != b.d {
			if a.d < b.d {
				return -1
			}
			return 1
		}
		return strings.Compare(a.name, b.name)
	})
	out := make([]string, 0, min(len(cs), limit))
	for _, c := range cs[:min(len(cs), limit)] {
		out = append(out, c.name)
	}
	return out
}

// segDist is the distance from (x, y) to the segment (ax, ay)-(bx, by).
func segDist(x, y, ax, ay, bx, by float64) float64 {
	dx, dy := bx-ax, by-ay
	t := 0.0
	if l := dx*dx + dy*dy; l > 0 {
		t = min(max(((x-ax)*dx+(y-ay)*dy)/l, 0), 1)
	}
	return math.Hypot(x-ax-t*dx, y-ay-t*dy)
}

// tileRC is the (gy, gx) of the grid holding a position: the core names a tile
// %03i%02i%02i.mmtile from (map, gy, gx) with gx from y and gy from x ("it's reversed",
// GridMap.cpp, MoveMap.cpp).
func tileRC(x, y float32) (int, int) {
	const tile = 533.3333333
	return int(32 - x/tile), int(32 - y/tile)
}

func tileFile(mapID uint32, x, y float32) string {
	r, c := tileRC(x, y)
	return fmt.Sprintf("%03d%02d%02d.mmtile", mapID, r, c)
}

var tileErrors = map[int]string{-1: "not an mmtile (bad header)", -2: "not mmap version 6 / Detour version 7",
	-3: "truncated (the header claims more data than the file holds)", -4: "Detour refused the tile"}

func tileError(rc int) error {
	if msg, ok := tileErrors[rc]; ok {
		return errors.New(msg)
	}
	return fmt.Errorf("unknown tile error %d", rc)
}

func dist2D(a, b Vec3) float32 {
	return float32(math.Hypot(float64(a[0]-b[0]), float64(a[1]-b[1])))
}

func dist3D(a, b Vec3) float32 {
	dx, dy, dz := float64(a[0]-b[0]), float64(a[1]-b[1]), float64(a[2]-b[2])
	return float32(math.Sqrt(dx*dx + dy*dy + dz*dz))
}

func fmtVec(v Vec3) string { return fmt.Sprintf("(%.1f, %.1f, %.1f)", v[0], v[1], v[2]) }
