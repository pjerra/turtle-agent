package dungeon

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/pjerra/turtle-agent/internal/navmesh"
)

// offMeshNav is a fakeNav whose walk path crosses one link: the middle segment
// carries OffMesh, with no ground sampled on it.
type offMeshNav struct {
	fakeNav
}

func (o offMeshNav) Plan(_ uint32, from, to navmesh.Vec3) (navmesh.Path, error) {
	p, err := o.fakeNav.Plan(0, from, to)
	if err != nil {
		return p, err
	}
	mid := navmesh.Vec3{(from[0] + to[0]) / 2, (from[1] + to[1]) / 2, (from[2] + to[2]) / 2}
	p.Points = []navmesh.Vec3{from, mid, to}
	p.OffMesh = []bool{false, true, false}
	p.Ground = [][]navmesh.Vec3{{}, nil, {}}
	return p, nil
}

// TestPlanCheckWarnsOnOffMeshLink: a complete path that uses a link is not a
// failure: one warning with reason "off-mesh link" per link per hop.
func TestPlanCheckWarnsOnOffMeshLink(t *testing.T) {
	leg := Leg{Name: "a", To: Point{20, 0, -40}}
	res := checkLegs(t, offMeshNav{}, 0, leg)
	if !res.OK() {
		t.Fatalf("issues, want none:\n%s", res.Summary())
	}
	if got := issueReasons(res.Warnings); got != "0/to/0/off-mesh link" {
		t.Errorf("warnings %q, want %q", got, "0/to/0/off-mesh link")
	}
	var detail string
	for _, w := range res.Warnings {
		detail = w.Detail
	}
	if !strings.Contains(detail, "crosses an off-mesh link (swim or drop)") {
		t.Errorf("warning detail %q, want the link endpoints", detail)
	}
}

// TestLookPointNeverStopsOnALink: a walk-up point that falls strictly inside a
// link segment moves to that segment's end point.
func TestLookPointNeverStopsOnALink(t *testing.T) {
	path := []Point{{0, 0, 0}, {0, 10, 0}, {0, 50, -40}}
	off := []bool{false, true, false}
	pack := Point{0, 50, -40}
	// Without the link rule the standoff back from the end lands mid-link.
	stand, ok := StandoffPoint(path, 21)
	if !ok {
		t.Fatal("want a standoff point")
	}
	if !(stand.Y > 10 && stand.Y < 50) {
		t.Fatalf("standoff %v is not inside the link segment: pick another", stand)
	}
	got, _ := LookPoint(path, pack, 21, lookView(21, lookArrive), nil, off)
	if got != path[2] {
		t.Errorf("look point %v, want the link end %v", got, path[2])
	}
}

// karazhanMmaps finds the navmesh files of map 800: $TURTLE_AGENT_MMAPS or the
// VM's per-map folder. The rebuilt 8005334 with the two links is picked up
// wherever it is.
func karazhanMmaps(t *testing.T) string {
	t.Helper()
	home, _ := os.UserHomeDir()
	for _, d := range []string{os.Getenv("TURTLE_AGENT_MMAPS"), filepath.Join(home, "tb-ci", "mmaps-turtle800")} {
		if d == "" {
			continue
		}
		if _, err := os.Stat(filepath.Join(d, "800.mmap")); err != nil {
			continue
		}
		if tiles, _ := filepath.Glob(filepath.Join(d, "800*.mmtile")); len(tiles) > 0 {
			return d
		}
	}
	t.Skip("no map 800 navmesh files: set TURTLE_AGENT_MMAPS to a folder holding 800.mmap and the 800*.mmtile tiles")
	return ""
}

// TestKarazhanCryptLinks is the real tile: with the rebuilt 8005334 the
// walkway reaches Marrowspike over the dive link, and the swim link's own ends
// join over the link both ways. (The north ramp reaches Corpsemuncher over the
// ground mesh, no link needed.) Skipped when the tile is missing.
func TestKarazhanCryptLinks(t *testing.T) {
	dir := karazhanMmaps(t)
	pl := navmesh.NewPlanner(dir)
	defer pl.Close()
	for _, c := range []struct {
		name     string
		from, to Point
	}{
		{"walkway to marrowspike", Point{-11202.1, -1756.8, 4.3}, Point{-11214.6, -1800.2, -28.4}},
		{"south ramp to north ramp", Point{-11213.1, -1572.3, -1.8}, Point{-11207.3, -1510.4, -3.8}},
	} {
		path, err := pl.Plan(800, vec(c.from), vec(c.to))
		skipNoEngine(t, err)
		if path.Status != navmesh.Complete {
			t.Errorf("%s: %s, want complete: %s", c.name, path.Status, path.Reason)
			continue
		}
		found := false
		for _, b := range path.OffMesh {
			found = found || b
		}
		if !found {
			t.Errorf("%s: complete without a link segment (%v)", c.name, path.Points)
		}
	}
}
