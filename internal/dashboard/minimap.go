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
	"unicode/utf8"

	"github.com/pjerra/turtle-agent/internal/minimap"
)

// The page always gets the three picture slots of a map in the order Zul'Farrak has them: the world map (the main image, then
// a slot per floor), the minimap and the navmesh. A kind the folder has no image for is still listed, marked unavailable with
// one line of reason, so the picker shows "Minimap (none)" and why instead of silently dropping the entry.
const (
	maxStatus      = 1 << 16 // <id>.status.json is at most this big; real ones are a few hundred bytes
	maxReasonRunes = 160     // longer reason strings are cut, so a hand-written status file cannot bloat every page
)

var (
	kindOrder = []string{"worldmap", "minimap", "navmesh"}
	kindTitle = map[string]string{"worldmap": "World map", "minimap": "Minimap", "navmesh": "Navmesh"}
	kindWhy   = map[string]string{
		"worldmap": "No world map picture: the client has no WorldMapArea row for this map",
		"minimap":  "No minimap picture: indoor dungeon (the client draws it from WMO tiles, which minimap-extract does not compose yet)",
		"navmesh":  "No navmesh picture: minimap-extract ran without --mmaps, or the server has no mmaps for this map",
	}
)

// MinimapPicture is the rectangle a picture covers in the drawing's plane and the URL it is served at.
type MinimapPicture struct {
	URL string  `json:"url"`
	X   float64 `json:"x"`
	Y   float64 `json:"y"`
	W   float64 `json:"w"`
	H   float64 `json:"h"`
}

// MinimapLayer is one picture slot of a map, ready for the page: the rectangle it covers in the drawing's plane (the plane
// Project makes) and the URL it is served at. An unavailable slot has no picture fields (the embedded pointer is nil) and a
// reason instead. The images come from `turtle-agent minimap-extract` and stay local.
type MinimapLayer struct {
	Name      string `json:"name"`   // what the page's picker shows, the same as Label: "World map", "World map 2f", "Minimap", ...
	Source    string `json:"source"` // minimap, worldmap or navmesh
	Kind      string `json:"kind"`   // the slot: worldmap, minimap or navmesh
	Floor     string `json:"floor"`  // "" or the floor suffix of a world map slot: "2f", "3f", ...
	Label     string `json:"label"`  // the panel's text for the slot
	Available bool   `json:"available"`
	Reason    string `json:"reason,omitempty"` // one line, for a slot without a picture
	*MinimapPicture
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
	minimapFile = regexp.MustCompile(`^(\d{1,9})(?:\.(minimap|worldmap|navmesh|status)(-\d+f)?)?\.(json|png)$`)
	layerName   = regexp.MustCompile(`^\d{1,9}\.(minimap|worldmap|navmesh|status)(-\d+f)?\.json$`)
)

// readMeta reads the JSON beside a picture of the folder; the picture itself must be there too.
func readMeta(dir, file string) (minimap.Meta, bool) {
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
	if _, err := SafeJoin(dir, strings.TrimSuffix(file, ".json")+".png"); err != nil {
		return minimap.Meta{}, false
	}
	return m, true
}

// statusReasons reads <id>.status.json, written by minimap-extract and the operator: {"minimap": {"reason": "..."}, ...}. An
// unreadable, oversized or malformed file gives no reasons at all, so the built-in line of each kind stands. Names are held to
// the same allow-list as every other file of the folder.
func statusReasons(dir, base string) map[string]string {
	name := base + ".status.json"
	if !layerName.MatchString(name) {
		return nil
	}
	full, err := SafeJoin(dir, name)
	if err != nil {
		return nil
	}
	st, err := os.Stat(full)
	if err != nil || st.Size() > maxStatus {
		return nil
	}
	b, err := os.ReadFile(full)
	if err != nil {
		return nil
	}
	var raw map[string]struct {
		Reason string `json:"reason"`
	}
	if json.Unmarshal(b, &raw) != nil {
		return nil
	}
	out := map[string]string{}
	for kind, v := range raw {
		if _, ok := kindTitle[kind]; !ok {
			continue
		}
		if r := strings.TrimSpace(v.Reason); r != "" {
			out[kind] = shortReason(r)
		}
	}
	return out
}

func shortReason(r string) string {
	if utf8.RuneCountInString(r) <= maxReasonRunes {
		return r
	}
	rs := []rune(r)
	return string(rs[:maxReasonRunes])
}

// MinimapSlots lists the three picture slots of a map in Zul'Farrak's order. The folder's own <id>.<source>[-<n>f].json is the
// slot of that source; the main <id>.json is used only for a source the folder has no file of (an old folder), so a picture is
// never listed twice. A slot with no picture is unavailable, with the reason from <id>.status.json or the built-in line. An
// empty folder gives three unavailable slots; no folder at all gives nil (the route then answers 404, as before).
func MinimapSlots(dir string, id uint32) []MinimapLayer {
	if dir == "" {
		return nil
	}
	base := strconv.FormatUint(uint64(id), 10)
	type found struct {
		m     minimap.Meta
		stem  string // the file without .json: "<id>.worldmap-2f" or, for the stand-in main picture, "<id>"
		floor string
	}
	byKind := map[string][]found{}
	ents, _ := os.ReadDir(dir)
	for _, e := range ents {
		n := e.Name()
		if !strings.HasPrefix(n, base+".") || !layerName.MatchString(n) {
			continue
		}
		// "<id>.worldmap-2f.json" -> source worldmap, floor 2f
		rest := strings.TrimSuffix(strings.TrimPrefix(n, base+"."), ".json")
		kind, floor := rest, ""
		if i := strings.IndexByte(rest, '-'); i >= 0 {
			kind, floor = rest[:i], rest[i+1:]
		}
		if kind == "status" {
			continue
		}
		if m, ok := readMeta(dir, n); ok {
			byKind[kind] = append(byKind[kind], found{m, strings.TrimSuffix(n, ".json"), floor})
		}
	}
	main, mainOK := readMeta(dir, base+".json")
	reasons := statusReasons(dir, base)

	slot := func(kind, floor, stem string, m *minimap.Meta) MinimapLayer {
		l := MinimapLayer{Kind: kind, Floor: floor, Source: kind, Label: kindTitle[kind]}
		if floor != "" {
			l.Label += " " + floor
		}
		l.Name = l.Label
		if m == nil {
			l.Reason = reasons[kind]
			if l.Reason == "" {
				l.Reason = kindWhy[kind]
			}
			return l
		}
		x, y, w, h := OverlayRect(*m)
		l.Source, l.Available = m.Source, true
		l.MinimapPicture = &MinimapPicture{URL: "/minimap/" + stem + ".png", X: x, Y: y, W: w, H: h}
		return l
	}
	pic := func(kind, floor string) (found, bool) {
		for _, f := range byKind[kind] {
			if f.floor == floor {
				return f, true
			}
		}
		// no file of its own: an old folder's main picture stands in for its one source
		if floor == "" && mainOK && main.Source == kind {
			return found{main, base, ""}, true
		}
		return found{}, false
	}

	out := make([]MinimapLayer, 0, len(kindOrder)+2)
	var floors []string
	for _, f := range byKind["worldmap"] {
		if f.floor != "" {
			floors = append(floors, f.floor)
		}
	}
	sort.Slice(floors, func(i, j int) bool {
		a, _ := strconv.Atoi(strings.TrimSuffix(floors[i], "f"))
		b, _ := strconv.Atoi(strings.TrimSuffix(floors[j], "f"))
		return a < b
	})
	for _, kind := range kindOrder {
		if f, ok := pic(kind, ""); ok {
			out = append(out, slot(kind, "", f.stem, &f.m))
		} else {
			out = append(out, slot(kind, "", "", nil))
		}
		if kind == "worldmap" {
			for _, fl := range floors {
				if f, ok := pic("worldmap", fl); ok {
					out = append(out, slot("worldmap", fl, f.stem, &f.m))
				}
			}
		}
	}
	return out
}

// serveMinimapLayers answers /api/minimap/<map id> with the three slots, 404 only when no folder is configured.
func serveMinimapLayers(w http.ResponseWriter, r *http.Request, dir string) {
	id, err := strconv.ParseUint(strings.TrimPrefix(r.URL.Path, "/api/minimap/"), 10, 32)
	if err != nil || dir == "" {
		http.NotFound(w, r)
		return
	}
	b, _ := json.Marshal(MinimapSlots(dir, uint32(id)))
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
