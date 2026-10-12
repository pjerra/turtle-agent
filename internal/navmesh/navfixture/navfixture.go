// Package navfixture builds synthetic .mmap and .mmtile files in the server's format
// (MmapTileHeader version 6 + a Detour version 7 tile with 64-bit poly refs), so the
// navmesh tests run without server data. Polygons are given in WoW (x, y, z),
// counter-clockwise in (x, y) like the real tiles.
//
// Ported from wow-play-harness tests/navfixture.py.
package navfixture

import (
	"bytes"
	"encoding/binary"
	"math"
	"os"
	"path/filepath"
	"testing"
)

// Tile is the size of a grid tile in yards (GridMap.h SIZE_OF_GRIDS).
const Tile = 533.3333333

const (
	dtMagic    = 'D'<<24 | 'N'<<16 | 'A'<<8 | 'V'
	dtVersion  = 7
	mmapMagic  = 0x4D4D4150
	mmapVer    = 6
	linkSize   = 16 // dtLink with 64-bit poly refs
	maxPolyVts = 6
)

// Poly is one convex polygon, vertices in WoW (x, y, z).
type Poly [][3]float32

// MmapParams is a map's NNN.mmap file: a raw dtNavMeshParams (orig in Detour y, z, x
// order, tile width and height, max tiles, max polys per tile).
func MmapParams() []byte {
	var b bytes.Buffer
	put(&b, []float32{0, -500, 0, Tile, Tile})
	put(&b, []int32{4, 1 << 16})
	return b.Bytes()
}

// BuildTile is a whole .mmtile file holding polys at tile (tx, ty), every polygon with the
// given flags (nil: all NAV_GROUND). Shared edges become neighbour links.
func BuildTile(polys []Poly, flags []uint16, tx, ty int32) []byte {
	if flags == nil {
		flags = make([]uint16, len(polys))
		for i := range flags {
			flags[i] = 1
		}
	}
	type key [3]float64
	var verts [][3]float32
	index := map[key]int{}
	pidx := make([][]int, len(polys))
	for pi, poly := range polys {
		for _, v := range poly {
			k := key{round4(v[0]), round4(v[1]), round4(v[2])}
			i, ok := index[k]
			if !ok {
				i = len(verts)
				index[k] = i
				verts = append(verts, [3]float32{float32(k[0]), float32(k[1]), float32(k[2])})
			}
			pidx[pi] = append(pidx[pi], i)
		}
	}
	type edge [2]int
	norm := func(a, b int) edge {
		if a > b {
			a, b = b, a
		}
		return edge{a, b}
	}
	edges := map[edge][]int{}
	for pi, ids := range pidx {
		for k := range ids {
			e := norm(ids[k], ids[(k+1)%len(ids)])
			edges[e] = append(edges[e], pi)
		}
	}
	neis := make([][]uint16, len(pidx))
	links := 0
	for pi, ids := range pidx {
		for k := range ids {
			var n uint16
			for _, other := range edges[norm(ids[k], ids[(k+1)%len(ids)])] {
				if other != pi {
					n = uint16(other + 1)
					links++
					break
				}
			}
			neis[pi] = append(neis[pi], n)
		}
	}
	links += 4 * len(polys)
	tris := 0
	for _, ids := range pidx {
		tris += len(ids) - 2
	}
	dv := make([][3]float32, len(verts)) // WoW (x, y, z) -> Detour (y, z, x)
	bmin := [3]float32{math.MaxFloat32, math.MaxFloat32, math.MaxFloat32}
	bmax := [3]float32{-math.MaxFloat32, -math.MaxFloat32, -math.MaxFloat32}
	for i, v := range verts {
		dv[i] = [3]float32{v[1], v[2], v[0]}
		for k := range 3 {
			bmin[k] = min(bmin[k], dv[i][k])
			bmax[k] = max(bmax[k], dv[i][k])
		}
	}
	var h bytes.Buffer
	put(&h, []int32{dtMagic, dtVersion, tx, ty, 0})
	put(&h, uint32(0))
	put(&h, []int32{int32(len(polys)), int32(len(verts)), int32(links), int32(len(polys)), 0, int32(tris), 0, 0, 0})
	put(&h, []float32{1.5, 0.3, 1.8})
	put(&h, bmin)
	put(&h, bmax)
	put(&h, float32(0))
	if h.Len() != 100 {
		panic("dtMeshHeader is not 100 bytes")
	}
	for _, v := range dv {
		put(&h, v)
	}
	for pi, ids := range pidx {
		var vv, nn [maxPolyVts]uint16
		for k, id := range ids {
			vv[k], nn[k] = uint16(id), neis[pi][k]
		}
		put(&h, uint32(0xFFFFFFFF))
		put(&h, vv)
		put(&h, nn)
		put(&h, flags[pi])
		h.WriteByte(byte(len(ids)))
		h.WriteByte(1) // area 1, polygon type ground
	}
	h.Write(make([]byte, linkSize*links))
	base := uint32(0)
	for _, ids := range pidx { // dtPolyDetail: no detail verts, a fan of the polygon's own
		put(&h, []uint32{0, base})
		h.Write([]byte{0, byte(len(ids) - 2), 0, 0})
		base += uint32(len(ids) - 2)
	}
	for _, ids := range pidx { // edge flags mark the polygon's own edges (2 bits per edge)
		n := len(ids)
		for k := 1; k < n-1; k++ {
			e := byte(1 << 2)
			if k == 1 {
				e |= 1
			}
			if k+1 == n-1 {
				e |= 1 << 4
			}
			h.Write([]byte{0, byte(k), byte(k + 1), e})
		}
	}
	var out bytes.Buffer
	put(&out, []uint32{mmapMagic, dtVersion, mmapVer, uint32(h.Len()), 0})
	out.Write(h.Bytes())
	return out.Bytes()
}

// Square is the axis-aligned cell x0..x1, y0..y1 at height z, counter-clockwise.
func Square(x0, y0, x1, y1, z float32) Poly {
	return Poly{{x0, y0, z}, {x1, y0, z}, {x1, y1, z}, {x0, y1, z}}
}

// LCorridor is 4-yd square cells: a row along x from 10 to 50 at y 10..14, then a column
// up y to 50 at x 46..50. Start (12, 12), goal (48, 48); the inner corner is (46, 14).
func LCorridor(z float32) []Poly {
	var ps []Poly
	for x0 := float32(10); x0 < 50; x0 += 4 {
		ps = append(ps, Square(x0, 10, x0+4, 14, z))
	}
	for y0 := float32(14); y0 < 50; y0 += 4 {
		ps = append(ps, Square(46, y0, 50, y0+4, z))
	}
	return ps
}

// Ring is 4-yd cells round a 40x40 square: bottom row y 10..14, right column x 46..50,
// top row y 46..50, left column x 10..14. From (12, 12) to (30, 48) the short way is up
// the left column (54 yd), the long way round by the right (90 yd).
func Ring(z float32) []Poly {
	var ps []Poly
	for x0 := float32(10); x0 < 50; x0 += 4 {
		ps = append(ps, Square(x0, 10, x0+4, 14, z))
	}
	for y0 := float32(14); y0 < 50; y0 += 4 {
		ps = append(ps, Square(46, y0, 50, y0+4, z))
	}
	for x0 := float32(10); x0 < 46; x0 += 4 {
		ps = append(ps, Square(x0, 46, x0+4, 50, z))
	}
	for y0 := float32(14); y0 < 46; y0 += 4 {
		ps = append(ps, Square(10, y0, 14, y0+4, z))
	}
	return ps
}

// Ramp is a 4-yd wide strip along x from 10 to 90 at y 10..14 whose height rises from 0 at
// x 10 to 20 at x 50 and falls back to 0 at x 90: a hill a straight line would cut through.
func Ramp() []Poly {
	h := func(x float32) float32 { return 20 - float32(math.Abs(float64(x-50)))/2 }
	var ps []Poly
	for x0 := float32(10); x0 < 90; x0 += 4 {
		ps = append(ps, Poly{{x0, 10, h(x0)}, {x0 + 4, 10, h(x0 + 4)}, {x0 + 4, 14, h(x0 + 4)}, {x0, 14, h(x0)}})
	}
	return ps
}

// WriteDir writes NNN.mmap and one tile, named for the map and the tile holding (12, 12),
// into a new temporary directory, and returns it.
func WriteDir(t testing.TB, mapID uint32, polys []Poly) string {
	t.Helper()
	dir := t.TempDir()
	write := func(name string, data []byte) {
		if err := os.WriteFile(filepath.Join(dir, name), data, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write(mmapName(mapID), MmapParams())
	write(tileName(mapID, 12, 12), BuildTile(polys, nil, 0, 0))
	return dir
}

func mmapName(mapID uint32) string { return pad(mapID, 3) + ".mmap" }

// tileName follows the core: the file is (map, gy, gx) with gx from y and gy from x.
func tileName(mapID uint32, x, y float32) string {
	gx, gy := int(32-y/Tile), int(32-x/Tile)
	return pad(mapID, 3) + pad(uint32(gy), 2) + pad(uint32(gx), 2) + ".mmtile"
}

func pad(v uint32, n int) string {
	s := []byte{}
	for range n {
		s = append([]byte{byte('0' + v%10)}, s...)
		v /= 10
	}
	return string(s)
}

func round4(v float32) float64 { return math.Round(float64(v)*1e4) / 1e4 }

func put(b *bytes.Buffer, v any) {
	if err := binary.Write(b, binary.LittleEndian, v); err != nil {
		panic(err)
	}
}
