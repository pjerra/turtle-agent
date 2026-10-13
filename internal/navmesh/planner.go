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
	eng     *engine
	tiles   map[string]*loaded
	missing map[string]bool // tile files Floors found missing; Plan forgets them
	tick    uint64
}

type loaded struct {
	ref  uint64 // Detour tile ref
	used uint64 // mapMesh.tick when a plan last wanted it
}

func NewPlanner(dir string) *Planner {
	return &Planner{dir: dir, maps: map[uint32]*mapMesh{}, max: MaxTiles}
}

// Scratch returns a planner on the same folder with a tile cache of its own. Work that must not unload this planner's
// tiles (a map image's background, read with Polygons, while a dungeon run plans on this one) draws with a scratch
// planner and closes it when done.
func (p *Planner) Scratch() *Planner { return NewPlanner(p.dir) }

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
	m, err := p.mesh(mapID)
	p.mu.Unlock()
	if err != nil {
		return Path{}, err
	}
	names, err := p.list(mapID)
	if err != nil {
		return Path{}, err
	}
	var path Path
	for _, margin := range []int{1, 3} {
		want := wantTiles(names, mapID, from, to, margin, p.max)
		data, err := p.fetch(m, want)
		if err != nil {
			return Path{}, err
		}
		p.mu.Lock()
		if p.maps[mapID] != m {
			p.mu.Unlock()
			return Path{}, errClosed
		}
		m.missing = nil
		if err := p.load(m, want, data); err != nil {
			p.mu.Unlock()
			return Path{}, err
		}
		path = plan(m.eng, from, to)
		p.mu.Unlock()
		if path.Status == Complete || len(names) <= smallMap {
			break
		}
	}
	return path, nil
}

// Floor is a navmesh surface at a point: its height and its polygon's flags (NavGround,
// NavWater, ...).
type Floor struct {
	Z     float32
	Flags uint16
}

// Floors lists the navmesh surfaces at (x, y) from height lo to hi, highest first: ground,
// water, lava, slime and too-steep slopes alike, as a falling body lands on any of them. It
// loads the one tile under the point when it is not loaded yet; a point whose tile file is
// missing has no floors (remembered until the next Plan, which reads the folder again). The
// mesh is the server's, so a floor is where the server's pathing has the ground (within the
// mesh's detail error); it holds no game objects.
func (p *Planner) Floors(mapID uint32, x, y, lo, hi float32) ([]Floor, error) {
	if p.dir == "" {
		return nil, errors.New(`navmesh: no tiles folder; set "mmaps_dir" in the settings file`)
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	m, err := p.mesh(mapID)
	if err != nil {
		return nil, err
	}
	if ok, err := p.ensureTile(mapID, m, x, y); err != nil || !ok {
		return nil, err
	}
	fl, err := m.eng.floors(x, y, lo, hi)
	slices.SortFunc(fl, func(a, b Floor) int {
		switch {
		case a.Z > b.Z:
			return -1
		case a.Z < b.Z:
			return 1
		}
		return 0
	})
	return fl, err
}

// ensureTile makes the one tile under (x, y) loaded (or counted as used), reading its file with the lock let go. It reports
// false for a point whose tile file is missing (remembered until the next Plan). The caller holds p.mu, which is held on return.
func (p *Planner) ensureTile(mapID uint32, m *mapMesh, x, y float32) (bool, error) {
	name := tileFile(mapID, x, y)
	if m.missing[name] {
		return false, nil
	}
	var data map[string][]byte
	if m.tiles[name] == nil {
		// Read the tile with the lock let go, as fetch does for Plan.
		p.mu.Unlock()
		b, err := os.ReadFile(filepath.Join(p.dir, name))
		p.mu.Lock()
		if p.maps[mapID] != m {
			return false, errClosed
		}
		switch {
		case errors.Is(err, os.ErrNotExist):
			if m.missing == nil {
				m.missing = map[string]bool{}
			}
			m.missing[name] = true
			return false, nil
		case err != nil:
			return false, fmt.Errorf("navmesh: %w", err)
		}
		data = map[string][]byte{name: b}
	}
	if err := p.load(m, []string{name}, data); err != nil {
		return false, err
	}
	return true, nil
}

// Ray is the answer of Raycast.
type Ray struct {
	// Known is false when the mesh cannot say: the start is off the mesh, or a tile under an end is missing.
	Known bool `json:"known"`
	// Clear says the straight line from the start to the end stays on walkable ground of the mesh.
	Clear bool `json:"clear"`
	// Hit is how far from the start the line meets the mesh's edge (0 when Clear).
	Hit float32 `json:"hit,omitempty"`
}

// Raycast casts a walkability ray over the mesh from one point to the other (Detour's raycast, over the polygons a player
// on foot may stand on). Clear means the straight line never leaves them, so a straight walk is possible. It is also the
// agent's stand-in for line of sight: the server tests sight against the world's collision geometry (vmaps), which the agent
// does not have, and the mesh edge is where a wall, a pillar or a drop begins. It misses what is above the ground (a bridge,
// a ceiling) and calls a gap or a cliff a block. It loads the tiles under both ends when they are not loaded (a ray of up to a
// tile's width crosses at most two; a longer one over an unloaded tile reads as blocked there), and gives Known false for an end whose
// tile file is missing.
func (p *Planner) Raycast(mapID uint32, from, to Vec3) (Ray, error) {
	if p.dir == "" {
		return Ray{}, errors.New(`navmesh: no tiles folder; set "mmaps_dir" in the settings file`)
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	m, err := p.mesh(mapID)
	if err != nil {
		return Ray{}, err
	}
	for _, pt := range []Vec3{from, to} {
		if ok, err := p.ensureTile(mapID, m, pt[0], pt[1]); err != nil {
			return Ray{}, err
		} else if !ok {
			return Ray{}, nil
		}
	}
	t, rc := m.eng.raycast(from, to)
	switch {
	case rc == -1:
		return Ray{}, nil
	case rc != 0:
		return Ray{}, errors.New("navmesh: Detour failed to cast the ray")
	}
	if t >= 1 {
		return Ray{Known: true, Clear: true}, nil
	}
	return Ray{Known: true, Hit: t * dist2D(from, to)}, nil
}

// Nearest is the walkable point of the mesh nearest to p within 4 yd round and 6 yd up and down (ok false when there is none or
// the tile is missing).
func (p *Planner) Nearest(mapID uint32, pt Vec3) (Vec3, bool, error) {
	if p.dir == "" {
		return Vec3{}, false, errors.New(`navmesh: no tiles folder; set "mmaps_dir" in the settings file`)
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	m, err := p.mesh(mapID)
	if err != nil {
		return Vec3{}, false, err
	}
	if ok, err := p.ensureTile(mapID, m, pt[0], pt[1]); err != nil || !ok {
		return Vec3{}, false, err
	}
	out, ok := m.eng.nearest(pt, Vec3{4, 4, 6})
	return out, ok, nil
}

// Polygon is one convex navmesh polygon for drawing: its corners in WoW (x, y, z), in the order the tile holds
// them, and its flags (NavGround, NavWater, ...).
type Polygon struct {
	Flags uint16
	Verts []Vec3
}

// Polygons lists the polygons of the map's tiles that overlap the box x0..x1, y0..y1 (WoW x and y; the corners may
// come in any order), whole polygons, not clipped to the box. It loads the tiles under the box that are not loaded
// yet, at most MaxTiles of them, the ones nearest the box's centre: a larger box is cut to those, and truncated
// says so. A box over no tile file gives no polygons and no error. Ground, water, lava, slime and slopes too steep
// to climb are all listed; the flags tell them apart. The tiles load into this planner's cache, which unloads the least
// recently wanted ones to make room: a caller that must not unload a plan's tiles uses a Scratch planner.
func (p *Planner) Polygons(mapID uint32, x0, y0, x1, y1 float32) (polys []Polygon, truncated bool, err error) {
	if p.dir == "" {
		return nil, false, errors.New(`navmesh: no tiles folder; set "mmaps_dir" in the settings file`)
	}
	if x0 > x1 {
		x0, x1 = x1, x0
	}
	if y0 > y1 {
		y0, y1 = y1, y0
	}
	p.mu.Lock()
	m, err := p.mesh(mapID)
	p.mu.Unlock()
	if err != nil {
		return nil, false, err
	}
	names, err := p.list(mapID)
	if err != nil {
		return nil, false, err
	}
	want, truncated := boxTiles(names, mapID, x0, y0, x1, y1, p.max)
	if len(want) == 0 {
		return nil, false, nil
	}
	data, err := p.fetch(m, want)
	if err != nil {
		return nil, false, err
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.maps[mapID] != m {
		return nil, false, errClosed
	}
	m.missing = nil
	if err := p.load(m, want, data); err != nil {
		return nil, false, err
	}
	polys, err = m.eng.polys([4]float32{x0, y0, x1, y1})
	return polys, truncated, err
}

// boxTiles is the map's tile files that overlap the box, nearest the box's centre first and cut to limit.
func boxTiles(names []string, mapID uint32, x0, y0, x1, y1 float32, limit int) (out []string, truncated bool) {
	rLo, cLo := tileRC(x1, y1) // x and y fall as the grid row and column rise
	rHi, cHi := tileRC(x0, y0)
	cr, cc := tileRC((x0+x1)/2, (y0+y1)/2)
	type cand struct {
		name string
		d    int
	}
	var cs []cand
	for _, n := range names {
		var m, r, c int
		if _, err := fmt.Sscanf(n, "%03d%02d%02d.mmtile", &m, &r, &c); err != nil || uint32(m) != mapID {
			continue
		}
		if r < rLo || r > rHi || c < cLo || c > cHi {
			continue
		}
		cs = append(cs, cand{n, (r-cr)*(r-cr) + (c-cc)*(c-cc)})
	}
	slices.SortStableFunc(cs, func(a, b cand) int {
		if a.d != b.d {
			return a.d - b.d
		}
		return strings.Compare(a.name, b.name)
	})
	truncated = len(cs) > limit
	for _, c := range cs[:min(len(cs), limit)] {
		out = append(out, c.name)
	}
	return out, truncated
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

// errClosed says Close ran while a query had the lock let go.
var errClosed = errors.New("navmesh: planner closed")

// fetch reads the wanted tiles that are not loaded yet, with the lock let go: a cold read
// (64 tiles of 0.5-3 MB, perhaps from a slow disk) must not hold back a Floors query of the
// session loop's fall and knockback traces (#42), which waits only for Detour itself.
func (p *Planner) fetch(m *mapMesh, want []string) (map[string][]byte, error) {
	p.mu.Lock()
	var cold []string
	for _, n := range want {
		if m.tiles[n] == nil {
			cold = append(cold, n)
		}
	}
	p.mu.Unlock()
	data := make(map[string][]byte, len(cold))
	for _, n := range cold {
		b, err := os.ReadFile(filepath.Join(p.dir, n))
		if err != nil {
			return nil, fmt.Errorf("navmesh: %w", err)
		}
		data[n] = b
	}
	return data, nil
}

// load makes the wanted tiles the loaded ones, unloading the least recently wanted others
// first when they would not all fit. data holds tiles fetch read; one it lacks (unloaded
// since by another query) is read here.
func (p *Planner) load(m *mapMesh, want []string, data map[string][]byte) error {
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
		b, ok := data[n]
		if !ok {
			var err error
			if b, err = os.ReadFile(filepath.Join(p.dir, n)); err != nil {
				return fmt.Errorf("navmesh: %w", err)
			}
		}
		ref, err := m.eng.addTile(b)
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
