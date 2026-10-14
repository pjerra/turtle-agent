package minimap

import (
	"encoding/binary"
	"errors"
	"fmt"
	"image"
	"image/color"
	"math"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// A navmesh floor render: the walkable polygons of a map's .mmtile files drawn top down, shaded by height. It is the
// fallback for a map the client has no minimap tiles for (indoor dungeons are WMOs).

const (
	mmapMagic  = 0x4d4d4150 // "MMAP", the server's tile header
	dtMagic    = 0x444e4156 // "DNAV"
	mmapHdrLen = 20
	dtHdrLen   = 100
	renderPad  = 12.0 // yards of margin round the polygons
)

// NavPoly is one convex walkable polygon in world coordinates (x north, y west, z up).
type NavPoly struct {
	V [][3]float32
}

// ParseMMTile reads the polygons of one .mmtile file.
func ParseMMTile(b []byte) ([]NavPoly, error) {
	le := binary.LittleEndian
	if len(b) < mmapHdrLen+dtHdrLen || le.Uint32(b) != mmapMagic {
		return nil, errors.New("not an mmtile")
	}
	h := b[mmapHdrLen:]
	if le.Uint32(h) != dtMagic || le.Uint32(h[4:]) != 7 {
		return nil, errors.New("not a Detour v7 tile")
	}
	polyCount, vertCount := int(le.Uint32(h[24:])), int(le.Uint32(h[28:]))
	vo := mmapHdrLen + dtHdrLen
	po := vo + vertCount*12
	if polyCount < 0 || vertCount < 0 || po+polyCount*32 > len(b) {
		return nil, errors.New("truncated tile")
	}
	f := func(off int) float32 { return math.Float32frombits(le.Uint32(b[off:])) }
	var out []NavPoly
	for i := 0; i < polyCount; i++ {
		p := b[po+i*32:]
		n := int(p[30])
		if p[31]>>6 == 1 { // DT_POLYTYPE_OFFMESH_CONNECTION
			continue
		}
		if n < 3 || n > 6 {
			continue
		}
		var np NavPoly
		for k := 0; k < n; k++ {
			vi := int(le.Uint16(p[4+k*2:]))
			if vi >= vertCount {
				return nil, errors.New("polygon vertex out of range")
			}
			o := vo + vi*12
			dx, dy, dz := f(o), f(o+4), f(o+8)
			np.V = append(np.V, [3]float32{dz, dx, dy}) // Detour (y, z, x) is the world (x, y, z) rotated
		}
		out = append(out, np)
	}
	return out, nil
}

// LoadMapPolys reads every tile of map id in the folders.
func LoadMapPolys(dirs []string, id uint32) ([]NavPoly, int, error) {
	var names []string
	for _, dir := range dirs {
		ns, err := filepath.Glob(filepath.Join(dir, fmt.Sprintf("%03d*.mmtile", id)))
		if err != nil {
			return nil, 0, err
		}
		names = append(names, ns...)
	}
	var all []NavPoly
	tiles := 0
	for _, n := range names {
		base := strings.TrimSuffix(filepath.Base(n), ".mmtile")
		if len(base) != 7 { // %03d%02d%02d only
			continue
		}
		b, err := os.ReadFile(n)
		if err != nil {
			return nil, 0, err
		}
		ps, err := ParseMMTile(b)
		if err != nil {
			return nil, 0, fmt.Errorf("%s: %w", n, err)
		}
		all = append(all, ps...)
		tiles++
	}
	return all, tiles, nil
}

// RenderNavmesh draws the polygons north up, west left, at about pxPerYard pixels per yard (lowered to keep the image
// within maxSide pixels), shaded from dark (low) to light (high); the background stays transparent.
func RenderNavmesh(polys []NavPoly, pxPerYard float64, maxSide int) (*image.NRGBA, Meta, error) {
	if len(polys) == 0 {
		return nil, Meta{}, errors.New("no polygons")
	}
	minX, maxX, minY, maxY := math.Inf(1), math.Inf(-1), math.Inf(1), math.Inf(-1)
	minZ, maxZ := float32(math.Inf(1)), float32(math.Inf(-1))
	for _, p := range polys {
		for _, v := range p.V {
			minX, maxX = math.Min(minX, float64(v[0])), math.Max(maxX, float64(v[0]))
			minY, maxY = math.Min(minY, float64(v[1])), math.Max(maxY, float64(v[1]))
			minZ, maxZ = min(minZ, v[2]), max(maxZ, v[2])
		}
	}
	north, south, west, east := maxX+renderPad, minX-renderPad, maxY+renderPad, minY-renderPad
	if side := max(north-south, west-east) * pxPerYard; side > float64(maxSide) {
		pxPerYard *= float64(maxSide) / side
	}
	m := Meta{Source: "navmesh", PxPerYard: pxPerYard, North: north, South: south, West: west, East: east,
		Width: int(math.Ceil((west - east) * pxPerYard)), Height: int(math.Ceil((north - south) * pxPerYard))}
	// Painter's order, low to high: an upper floor covers the hall under it.
	order := make([]int, len(polys))
	zs := make([]float32, len(polys))
	for i, p := range polys {
		order[i] = i
		for _, v := range p.V {
			zs[i] += v[2] / float32(len(p.V))
		}
	}
	sort.SliceStable(order, func(a, b int) bool { return zs[order[a]] < zs[order[b]] })
	img := image.NewNRGBA(image.Rect(0, 0, m.Width, m.Height))
	span := maxZ - minZ
	for _, i := range order {
		p := polys[i]
		var px, py, pz [6]float64
		for k, v := range p.V {
			px[k], py[k] = m.ToPixel(float64(v[0]), float64(v[1]))
			pz[k] = float64(v[2])
		}
		for k := 1; k+1 < len(p.V); k++ {
			fillTri(img, px[0], py[0], pz[0], px[k], py[k], pz[k], px[k+1], py[k+1], pz[k+1], float64(minZ), float64(span))
		}
	}
	return img, m, nil
}

// heightColor shades t in [0,1] from a dark blue-grey to a light sand.
func heightColor(t float64) color.NRGBA {
	t = math.Min(1, math.Max(0, t))
	lerp := func(a, b float64) uint8 { return uint8(a + (b-a)*t) }
	return color.NRGBA{lerp(48, 232), lerp(66, 214), lerp(96, 168), 255}
}

func fillTri(img *image.NRGBA, x0, y0, z0, x1, y1, z1, x2, y2, z2, minZ, span float64) {
	den := (y1-y2)*(x0-x2) + (x2-x1)*(y0-y2)
	if math.Abs(den) < 1e-9 {
		return
	}
	b := img.Bounds()
	lx, hx := int(math.Floor(math.Min(x0, math.Min(x1, x2)))), int(math.Ceil(math.Max(x0, math.Max(x1, x2))))
	ly, hy := int(math.Floor(math.Min(y0, math.Min(y1, y2)))), int(math.Ceil(math.Max(y0, math.Max(y1, y2))))
	for y := max(ly, b.Min.Y); y <= min(hy, b.Max.Y-1); y++ {
		for x := max(lx, b.Min.X); x <= min(hx, b.Max.X-1); x++ {
			cx, cy := float64(x)+0.5, float64(y)+0.5
			w0 := ((y1-y2)*(cx-x2) + (x2-x1)*(cy-y2)) / den
			w1 := ((y2-y0)*(cx-x2) + (x0-x2)*(cy-y2)) / den
			w2 := 1 - w0 - w1
			const e = -0.02 // a hair outside, so that neighbours leave no seam
			if w0 < e || w1 < e || w2 < e {
				continue
			}
			z := w0*z0 + w1*z1 + w2*z2
			t := 0.5
			if span > 0 {
				t = (z - minZ) / span
			}
			img.SetNRGBA(x, y, heightColor(t))
		}
	}
}
