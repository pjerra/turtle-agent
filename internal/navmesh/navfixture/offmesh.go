package navfixture

import (
	"bytes"
	"math"
	"os"
	"path/filepath"
	"testing"
)

// Linked is two squares at different heights, joined by one off-mesh link: the
// lower square at z 0 (x 10..30, y 10..14), the upper at z -40 (x 10..30, y
// 60..64), with the link from the lower square's far edge to the upper one.
// MoveMapGen appends the link's two endpoint vertices after the ground verts,
// the link record pointing at them.
type Linked struct {
	Upper, Lower Poly
	From, To     [3]float32
	Gap          float32
}

func TwoFloorsLinked(gap float32) Linked {
	upper := Square(10, 10, 30, 14, 0)
	lower := Square(10, 60, 30, 64, -40)
	return Linked{
		Upper: upper,
		Lower: lower,
		From:  [3]float32{20, 14, 0},
		To:    [3]float32{20, 60, -40},
		Gap:   gap,
	}
}

func (l Linked) Polys() []Poly { return []Poly{l.Upper, l.Lower} }

func (l Linked) Links() [][2][3]float32 { return [][2][3]float32{{l.From, l.To}} }

// BuildTileOffMesh writes a whole .mmtile file holding polys plus links as
// dtOffMeshConnection records and their 2-vertex polys of type
// DT_POLYTYPE_OFFMESH_CONNECTION, with MoveMapGen's flags 0xFF and area 0xFF.
func BuildTileOffMesh(polys []Poly, links [][2][3]float32, rad float32, tx, ty int32) []byte {
	if len(links) == 0 {
		panic("BuildTileOffMesh: no links")
	}
	// Ground layout first: shared edges become neighbour links, as in BuildTile.
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
	nlinks := 0
	for pi, ids := range pidx {
		for k := range ids {
			var n uint16
			for _, other := range edges[norm(ids[k], ids[(k+1)%len(ids)])] {
				if other != pi {
					n = uint16(other + 1)
					nlinks++
					break
				}
			}
			neis[pi] = append(neis[pi], n)
		}
	}
	groundCount := len(polys)
	flags := make([]uint16, groundCount)
	for i := range flags {
		flags[i] = 1
	}
	// The link endpoints become new vertices (Detour order (y, z, x)) after the
	// ground verts; each link gets its own 2-vertex polygon.
	lidx := make([][2]int, len(links))
	for li, con := range links {
		for e, p := range con {
			d := [3]float32{p[1], p[2], p[0]}
			lidx[li][e] = len(verts)
			verts = append(verts, [3]float32{d[2], d[0], d[1]}) // back to WoW for the table
		}
	}
	totalPolys := groundCount + len(links)
	nlinks += 4 * totalPolys
	// Cross-tile neighbour slots for the link endpoints: Detour's addTile only
	// wires off-mesh ends to ground polys in the same tile, and both ends of
	// our links land on ground, so no extra external slots are needed.
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
	put(&h, []int32{int32(groundCount), int32(len(verts)), int32(nlinks), int32(groundCount), 0, int32(tris), 0, 0, 0})
	put(&h, []float32{1.5, 0.3, 1.8})
	put(&h, bmin)
	put(&h, bmax)
	put(&h, float32(0))
	// polyCount, offMeshConCount and offMeshBase are header offsets 24, 52 and 56, patched below.
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
	for li := range links {
		var vv, nn [maxPolyVts]uint16
		vv[0], vv[1] = uint16(lidx[li][0]), uint16(lidx[li][1])
		put(&h, uint32(0xFFFFFFFF))
		put(&h, vv)
		put(&h, nn)
		put(&h, uint16(0xFF)) // flags 0xFF as MoveMapGen writes them
		h.WriteByte(2)
		h.WriteByte(0x40 | 0x3F) // type off-mesh connection, area 0x3F
	}
	h.Write(make([]byte, linkSize*nlinks))
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
	tile := h.Bytes()
	// Patch the header: total polys, offMeshConCount, offMeshBase. Detour reads
	// the off-mesh records after the ground detail arrays, so detailMeshCount
	// covers ground polys only (MoveMapGen layout).
	setHeader := func(off int, v int32) {
		tile[off] = byte(v)
		tile[off+1] = byte(v >> 8)
		tile[off+2] = byte(v >> 16)
		tile[off+3] = byte(v >> 24)
	}
	_ = setHeader
	setHeader(24, int32(totalPolys))
	setHeader(52, int32(len(links)))
	setHeader(56, int32(groundCount))
	// Append the dtOffMeshConnection records: endpoints in Detour (y, z, x)
	// order, radius, poly index, BIDIR flag, side 0xff, user id 0.
	var cons bytes.Buffer
	for li, con := range links {
		for e := range 2 {
			p := con[e]
			put(&cons, []float32{p[1], p[2], p[0]})
		}
		put(&cons, rad)
		put(&cons, uint16(groundCount+li))
		cons.WriteByte(1) // DT_OFFMESH_CON_BIDIR
		cons.WriteByte(0xFF)
		put(&cons, uint32(0))
	}
	var out bytes.Buffer
	put(&out, []uint32{mmapMagic, dtVersion, mmapVer, uint32(len(tile) + cons.Len()), 0})
	out.Write(tile)
	out.Write(cons.Bytes())
	return out.Bytes()
}

// WriteLinkedDir writes MmapParams and one off-mesh tile into a new temporary
// directory, and returns it with the link spec.
func WriteLinkedDir(t testing.TB, mapID uint32, l Linked, tx, ty int32) string {
	t.Helper()
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, mmapName(mapID)), MmapParams(), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, tileName(mapID, 12, 12)), BuildTileOffMesh(l.Polys(), l.Links(), 1.5, tx, ty), 0o644); err != nil {
		t.Fatal(err)
	}
	return dir
}
