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

// WMOTile is one minimap tile of one group of a WMO: the group index from the WMO's MOGI, the column A along the group's
// local x and the row B along its local y, counted up from the group bounding box minimum. The client names such a tile
// WMO\...\<base>_<group>_<A>_<B>.blp.
type WMOTile struct{ Group, A, B int }

// Translate is md5translate.trs: the terrain tiles of every map folder and the WMO tiles of every WMO, each keyed by the
// lower-case base name of the WMO file.
type Translate struct {
	Terrain map[string]map[Tile]string
	WMO     map[string]map[WMOTile]string
}

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
	// From says what a minimap picture is made of: "terrain" (the map's terrain tiles), "wmo" (the client's indoor
	// minimap tiles of the WMO groups) or "terrain+wmo" (the WMO tiles drawn over the terrain). Empty for the other
	// sources.
	From string `json:"from,omitempty"`
	// Note is a one-line known limit of the picture (a client-data gap), for humans; readers ignore unknown keys.
	Note string `json:"note,omitempty"`
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

// ParseTranslate reads textures\Minimap\md5translate.trs: lines "<MapDir>\map<R>_<C>.blp<TAB><hash>.blp" for the terrain
// tiles, and "<WMO dir>\<base>_<group>_<A>_<B>.blp<TAB><hash>.blp" for the WMO minimap tiles (a "dir:" line starts each
// map's block in some builds; it is skipped). Terrain tiles map a map's folder (lower case) to its tiles' file names in
// Textures\Minimap; WMO tiles map a WMO's lower-case base file name to its tiles.
func ParseTranslate(b []byte) Translate {
	out := Translate{Terrain: map[string]map[Tile]string{}, WMO: map[string]map[WMOTile]string{}}
	sc := bufio.NewScanner(bytes.NewReader(b))
	sc.Buffer(make([]byte, 1<<20), 1<<20)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		k, v, ok := strings.Cut(line, "\t")
		if !ok || strings.HasPrefix(strings.ToLower(k), "dir:") {
			continue
		}
		k, v = strings.TrimSpace(k), strings.TrimSpace(v)
		if v == "" {
			continue
		}
		i := strings.LastIndexAny(k, `\/`)
		if i < 0 {
			continue
		}
		dir, file := strings.ToLower(k[:i]), strings.ToLower(k[i+1:])
		if lk := strings.ToLower(k); strings.HasPrefix(lk, `wmo\`) || strings.HasPrefix(lk, "wmo/") {
			base, t, ok := parseWMOTile(file)
			if !ok {
				continue
			}
			if out.WMO[base] == nil {
				out.WMO[base] = map[WMOTile]string{}
			}
			out.WMO[base][t] = v
			continue
		}
		var c, r int // map<A>_<B>: A counts along world y, B along world x (checked against the plans' coordinates)
		if _, err := fmt.Sscanf(file, "map%d_%d.blp", &c, &r); err != nil {
			continue
		}
		if out.Terrain[dir] == nil {
			out.Terrain[dir] = map[Tile]string{}
		}
		out.Terrain[dir][Tile{r, c}] = v
	}
	return out
}

// parseWMOTile splits <base>_<group>_<A>_<B>.blp. The base may hold underscores itself, so the last three fields are the
// numbers, each with its fixed width (3, 2 and 2 digits, as the client writes them).
func parseWMOTile(file string) (string, WMOTile, bool) {
	if !strings.HasSuffix(file, ".blp") {
		return "", WMOTile{}, false
	}
	parts := strings.Split(strings.TrimSuffix(file, ".blp"), "_")
	if len(parts) < 4 {
		return "", WMOTile{}, false
	}
	n := len(parts)
	if len(parts[n-3]) != 3 || len(parts[n-2]) != 2 || len(parts[n-1]) != 2 ||
		!allDigits(parts[n-3]) || !allDigits(parts[n-2]) || !allDigits(parts[n-1]) {
		return "", WMOTile{}, false
	}
	g, err1 := strconv.Atoi(parts[n-3])
	a, err2 := strconv.Atoi(parts[n-2])
	bb, err3 := strconv.Atoi(parts[n-1])
	if err1 != nil || err2 != nil || err3 != nil {
		return "", WMOTile{}, false
	}
	base := strings.Join(parts[:n-3], "_")
	if base == "" || g < 0 || a < 0 || bb < 0 {
		return "", WMOTile{}, false
	}
	return base, WMOTile{Group: g, A: a, B: bb}, true
}

// allDigits reports whether s is a non-empty run of ASCII digits (Atoi alone accepts a sign).
func allDigits(s string) bool {
	for i := range len(s) {
		if s[i] < '0' || s[i] > '9' {
			return false
		}
	}
	return s != ""
}

func itoa(n int) string { return strconv.Itoa(n) }
