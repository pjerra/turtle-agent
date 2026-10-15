package dashboard

import (
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"

	"github.com/pjerra/turtle-agent/internal/minimap"
)

// MinimapLayer is one background image of a map, ready for the page: the rectangle it covers in the drawing's plane (the
// plane Project makes) and the URL it is served at. The images come from `turtle-agent minimap-extract` and stay local.
type MinimapLayer struct {
	Name   string  `json:"name"`   // what the page's picker shows: "worldmap", "minimap", "worldmap 2f", ...
	Source string  `json:"source"` // minimap, worldmap or navmesh
	URL    string  `json:"url"`
	X      float64 `json:"x"`
	Y      float64 `json:"y"`
	W      float64 `json:"w"`
	H      float64 `json:"h"`
}

// OverlayRect is where an image with the world edges of m lies in the drawing's plane. World x is north and world y is west;
// the drawing has north up and west left (Project), so the image's top-left corner (north, west) is Project(North, West) and
// the image spans West-East yards across and North-South yards down. It is the same transform the extractor writes the
// pictures for: pixel column c is world y West - c/PxPerYard, pixel row r is world x North - r/PxPerYard.
func OverlayRect(m minimap.Meta) (x, y, w, h float64) {
	x, y = Project(float32(m.North), float32(m.West))
	return x, y, m.West - m.East, m.North - m.South
}

var (
	minimapFile = regexp.MustCompile(`^(\d{1,9})(?:\.(minimap|worldmap|navmesh)(-\d+f)?)?\.(json|png)$`)
	layerName   = regexp.MustCompile(`^\d{1,9}\.(minimap|worldmap|navmesh)(-\d+f)?\.json$`)
)

// MinimapLayers lists the images the folder has for a map: the main one (<id>.png) first, then the others by name.
func MinimapLayers(dir string, id uint32) []MinimapLayer {
	if dir == "" {
		return nil
	}
	read := func(file string) (minimap.Meta, bool) {
		full, err := SafeJoin(dir, file)
		if err != nil {
			return minimap.Meta{}, false
		}
		st, err := os.Stat(full)
		if err != nil || st.Size() > 1<<16 {
			return minimap.Meta{}, false
		}
		b, err := os.ReadFile(full)
		if err != nil {
			return minimap.Meta{}, false
		}
		var m minimap.Meta
		if json.Unmarshal(b, &m) != nil || m.Width <= 0 || m.Height <= 0 || m.West <= m.East || m.North <= m.South {
			return minimap.Meta{}, false
		}
		return m, true
	}
	base := strconv.FormatUint(uint64(id), 10)
	layer := func(file, name string, m minimap.Meta) MinimapLayer {
		x, y, w, h := OverlayRect(m)
		return MinimapLayer{Name: name, Source: m.Source, URL: "/minimap/" + strings.TrimSuffix(file, ".json") + ".png", X: x, Y: y, W: w, H: h}
	}
	var out []MinimapLayer
	mainSrc := ""
	if m, ok := read(base + ".json"); ok {
		out = append(out, layer(base+".json", m.Source, m))
		mainSrc = m.Source
	}
	ents, _ := os.ReadDir(dir)
	var names []string
	for _, e := range ents {
		n := e.Name()
		if layerName.MatchString(n) && strings.HasPrefix(n, base+".") && n != base+"."+mainSrc+".json" {
			names = append(names, n)
		}
	}
	sort.Strings(names)
	for _, n := range names {
		if m, ok := read(n); ok {
			// "<id>.worldmap-2f.json" -> "worldmap 2f"
			name := strings.ReplaceAll(strings.TrimSuffix(strings.TrimPrefix(n, base+"."), ".json"), "-", " ")
			out = append(out, layer(n, name, m))
		}
	}
	return out
}

// serveMinimapLayers answers /api/minimap/<map id> with the layers, 404 for a map without images.
func serveMinimapLayers(w http.ResponseWriter, r *http.Request, dir string) {
	id, err := strconv.ParseUint(strings.TrimPrefix(r.URL.Path, "/api/minimap/"), 10, 32)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	ls := MinimapLayers(dir, uint32(id))
	if len(ls) == 0 {
		http.NotFound(w, r)
		return
	}
	b, _ := json.Marshal(ls)
	writeJSON(w, b)
}

// serveMinimapImage serves <id>[.<source>[-<n>f]].png from the minimap folder and nothing else.
func serveMinimapImage(w http.ResponseWriter, r *http.Request, dir string) {
	name := strings.TrimPrefix(r.URL.Path, "/minimap/")
	if m := minimapFile.FindStringSubmatch(name); dir == "" || m == nil || m[4] != "png" {
		http.NotFound(w, r)
		return
	}
	full, err := SafeJoin(dir, name)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	w.Header().Set("Content-Type", "image/png")
	w.Header().Set("Cache-Control", "no-cache")
	http.ServeFile(w, r, filepath.Clean(full))
}
