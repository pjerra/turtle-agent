//go:build cgo

package navmesh

/*
#cgo CXXFLAGS: -std=c++17 -O2 -I${SRCDIR}/detour/Include
#include <stdlib.h>
#include "navdetour.h"
*/
import "C"

import (
	"errors"
	"fmt"
	"unsafe"
)

// engineName is what Path.Engine reports.
const engineName = "detour"

// detourBuild is nd_version() of the core's Detour: version 7 with 64-bit poly refs. Tiles
// built for those refs do not load into another build (dtLink is 16 bytes, not 12).
const detourBuild = 708

const maxPoints = 2048

// engine is one map's Detour navmesh and query. It is not safe for concurrent use; the
// Planner serialises calls.
type engine struct{ h unsafe.Pointer }

func newEngine(params []byte) (*engine, error) {
	if v := int(C.nd_version()); v != detourBuild {
		return nil, fmt.Errorf("navmesh: Detour build %d, want %d", v, detourBuild)
	}
	if len(params) == 0 {
		return nil, errors.New("navmesh: empty .mmap")
	}
	h := C.nd_create((*C.uchar)(unsafe.Pointer(&params[0])), C.int(len(params)))
	if h == nil {
		return nil, fmt.Errorf("navmesh: Detour refused the %d-byte .mmap params", len(params))
	}
	return &engine{h: h}, nil
}

func (e *engine) close() {
	if e.h != nil {
		C.nd_destroy(e.h)
		e.h = nil
	}
}

// addTile loads a whole .mmtile file and returns its tile ref.
func (e *engine) addTile(data []byte) (uint64, error) {
	if len(data) == 0 {
		return 0, tileError(-1)
	}
	var ref C.ulonglong
	if rc := C.nd_add_tile(e.h, (*C.uchar)(unsafe.Pointer(&data[0])), C.int(len(data)), &ref); rc != 0 {
		return 0, tileError(int(rc))
	}
	return uint64(ref), nil
}

func (e *engine) removeTile(ref uint64) error {
	if C.nd_remove_tile(e.h, C.ulonglong(ref)) != 0 {
		return errors.New("Detour refused to unload the tile")
	}
	return nil
}

// nearest is the closest walkable point to p within the half extents ext (x, y, height).
func (e *engine) nearest(p, ext Vec3) (Vec3, bool) {
	var out Vec3
	rc := C.nd_nearest(e.h, cvec(&p), cvec(&ext), C.ushort(IncludeFlags), C.ushort(ExcludeFlags), cvec(&out))
	return out, rc == 0
}

// floors lists the surface heights at (x, y) between lo and hi with their polygon flags.
func (e *engine) floors(x, y, lo, hi float32) ([]Floor, error) {
	const most = 64 // nd_floors queries at most 64 polygons, so none is cut
	p := Vec3{x, y, 0}
	var z [most]float32
	var fl [most]C.ushort
	n := int(C.nd_floors(e.h, cvec(&p), C.float(lo), C.float(hi), (*C.float)(unsafe.Pointer(&z[0])), &fl[0], most))
	if n < 0 {
		return nil, errors.New("navmesh: Detour failed to query the floors")
	}
	out := make([]Floor, n)
	for i := range out {
		out[i] = Floor{Z: z[i], Flags: uint16(fl[i])}
	}
	return out, nil
}

// polys is the polygons of the loaded tiles that overlap the box (x0, y0, x1, y1).
func (e *engine) polys(box [4]float32) ([]Polygon, error) {
	n := int(C.nd_polys(e.h, (*C.float)(unsafe.Pointer(&box[0])), nil, 0))
	if n < 0 {
		return nil, errors.New("navmesh: Detour failed to list the polygons")
	}
	if n == 0 {
		return nil, nil
	}
	buf := make([]float32, n)
	if got := int(C.nd_polys(e.h, (*C.float)(unsafe.Pointer(&box[0])), (*C.float)(unsafe.Pointer(&buf[0])), C.int(n))); got != n {
		return nil, errors.New("navmesh: the polygon list changed while it was read")
	}
	var out []Polygon
	for i := 0; i < len(buf); {
		nv := int(buf[i])
		p := Polygon{Flags: uint16(buf[i+1]), Verts: make([]Vec3, nv)}
		for k := range nv {
			p.Verts[k] = Vec3{buf[i+2+3*k], buf[i+3+3*k], buf[i+4+3*k]}
		}
		out = append(out, p)
		i += 2 + 3*nv
	}
	return out, nil
}

// markBox adds flag to the polygons of the loaded tiles whose part inside b covers at least share of their area; it returns how many
// it marked.
func (e *engine) markBox(b Box, flag uint16, share float32) int {
	box := [6]float32{b.Min[0], b.Min[1], b.Min[2], b.Max[0], b.Max[1], b.Max[2]}
	return int(C.nd_mark_box(e.h, (*C.float)(unsafe.Pointer(&box[0])), C.ushort(flag), C.float(share)))
}

// findPath returns Detour's verdict (0 complete, 1 partial, 2 partial with the goal over
// 7 yd from the mesh, -1 start off the mesh, -2 goal off the mesh, -3 failure), the
// straight path between the floor points, and the floor point found for the goal.
func (e *engine) findPath(from, to Vec3) (int, []Vec3, Vec3) {
	out := make([]Vec3, maxPoints)
	var n C.int
	var goal Vec3
	rc := C.nd_find_path(e.h, cvec(&from), cvec(&to), C.ushort(IncludeFlags), C.ushort(ExcludeFlags),
		cvec(&out[0]), C.int(maxPoints), &n, cvec(&goal))
	return int(rc), out[:int(n)], goal
}

// serverMaxPoints is the most smooth points the server's findSmoothPath returns
// (PathFinder.h MAX_POINT_PATH_LENGTH).
const serverMaxPoints = 256

// serverPath replays the server's own query (the core's PathInfo: BuildPolyPath +
// findSmoothPath): 2048 search nodes, at most 256 polys, ground|water included, nothing
// excluded, a smoothed path of at most 256 points. It returns the path type bits
// (PathNormal/PathIncomplete/PathNoPath), the smooth points, and the last smooth point
// (what the run leader walks to), with the asked point's distance to its walk poly for
// each end (0 when there is none).
func (e *engine) serverPath(from, to Vec3) (uint32, []Vec3, Vec3, float32, float32) {
	out := make([]Vec3, serverMaxPoints)
	var n C.int
	var actual Vec3
	var typ C.uint
	var sdist, edist C.float
	C.nd_server_path(e.h, cvec(&from), cvec(&to), cvec(&out[0]), C.int(serverMaxPoints), &n, cvec(&actual), &typ, &sdist, &edist)
	return uint32(typ), out[:int(n)], actual, float32(sdist), float32(edist)
}

// raycast casts a walkability ray over the mesh from one point to another: the fraction of the way at which it meets the mesh's
// edge (over 1 when it reaches the end), and 0 for done, -1 when the start is off the mesh, -3 on a Detour failure.
func (e *engine) raycast(from, to Vec3) (float32, int) {
	var t C.float
	rc := C.nd_raycast(e.h, cvec(&from), cvec(&to), C.ushort(IncludeFlags), C.ushort(ExcludeFlags), &t)
	return float32(t), int(rc)
}

func cvec(v *Vec3) *C.float { return (*C.float)(unsafe.Pointer(&v[0])) }
