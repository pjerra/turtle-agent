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

func cvec(v *Vec3) *C.float { return (*C.float)(unsafe.Pointer(&v[0])) }
