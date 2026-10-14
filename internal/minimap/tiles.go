package minimap

import (
	"bufio"
	"bytes"
	"fmt"
	"math"
	"strconv"
	"strings"
)

// TileSize is the side of one map tile (an ADT) in yards; a map is a grid of 64 x 64 of them.
const TileSize = 533.33333333

// TilePx is the side of a minimap tile image in pixels.
const TilePx = 256

// Tile is a tile's grid position: Row from the world x axis (north), Col from the world y axis (west). The server names its
// mmtiles <map><Row><Col>; the client names the minimap tile map<Col>_<Row>.blp.
type Tile struct{ Row, Col int }

// TileOf is the tile that holds a world position (x north, y west).
func TileOf(x, y float64) Tile {
	return Tile{int(math.Floor(32 - x/TileSize)), int(math.Floor(32 - y/TileSize))}
}

// Meta says where an image lies in world coordinates. The image is north up and west left, as the in-game map is:
// pixel column c is world y = West - c/PxPerYard, pixel row r is world x = North - r/PxPerYard.
type Meta struct {
	Map       uint32  `json:"map"`
	Name      string  `json:"name,omitempty"`
	Source    string  `json:"source"` // "minimap" (the client's tiles) or "navmesh" (a height-shaded floor render)
	Width     int     `json:"width"`
	Height    int     `json:"height"`
	PxPerYard float64 `json:"px_per_yard"`
	North     float64 `json:"north"` // world x of the top edge
	South     float64 `json:"south"` // world x of the bottom edge
	West      float64 `json:"west"`  // world y of the left edge
	East      float64 `json:"east"`  // world y of the right edge
	Tiles     int     `json:"tiles,omitempty"`
}

// Bounds fills the world edges for tiles rows r0..r1 and columns c0..c1 (inclusive) drawn at px pixels per tile.
func Bounds(r0, c0, r1, c1, px int) (m Meta) {
	m.PxPerYard = float64(px) / TileSize
	m.Width, m.Height = (c1-c0+1)*px, (r1-r0+1)*px
	m.North = float64(32-r0) * TileSize
	m.South = float64(32-(r1+1)) * TileSize
	m.West = float64(32-c0) * TileSize
	m.East = float64(32-(c1+1)) * TileSize
	return m
}

// ToPixel is the pixel (column, row, fractional) of a world position (x north, y west).
func (m Meta) ToPixel(x, y float64) (px, py float64) {
	return (m.West - y) * m.PxPerYard, (m.North - x) * m.PxPerYard
}

// ToWorld is the inverse of ToPixel.
func (m Meta) ToWorld(px, py float64) (x, y float64) {
	return m.North - py/m.PxPerYard, m.West - px/m.PxPerYard
}

// ParseTranslate reads textures\Minimap\md5translate.trs: lines "<MapDir>\map<R>_<C>.blp<TAB><hash>.blp" (a "dir:" line
// starts each map's block in some builds; it is skipped). The result maps a map's folder (lower case) to its tiles' file
// names in Textures\Minimap.
func ParseTranslate(b []byte) map[string]map[Tile]string {
	out := map[string]map[Tile]string{}
	sc := bufio.NewScanner(bytes.NewReader(b))
	sc.Buffer(make([]byte, 1<<20), 1<<20)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		k, v, ok := strings.Cut(line, "\t")
		if !ok || strings.HasPrefix(strings.ToLower(k), "dir:") {
			continue
		}
		k, v = strings.TrimSpace(k), strings.TrimSpace(v)
		i := strings.LastIndexAny(k, `\/`)
		if i < 0 {
			continue
		}
		dir, file := strings.ToLower(k[:i]), strings.ToLower(k[i+1:])
		var c, r int // map<A>_<B>: A counts along world y, B along world x (checked against the plans' coordinates)
		if _, err := fmt.Sscanf(file, "map%d_%d.blp", &c, &r); err != nil {
			continue
		}
		if out[dir] == nil {
			out[dir] = map[Tile]string{}
		}
		out[dir][Tile{r, c}] = v
	}
	return out
}

func itoa(n int) string { return strconv.Itoa(n) }
