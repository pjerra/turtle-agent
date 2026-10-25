//go:build cgo

package navmesh

import (
	"math"
	"os"
	"path/filepath"
	"testing"

	"github.com/pjerra/turtle-agent/internal/navmesh/navfixture"
)

// linkTile plans on the two-squares-plus-link fixture: a walkable corridor at
// z 0 plus a lower square at z -10, joined by one link.
func linkTile(t *testing.T, polys []navfixture.Poly, links [][2][3]float32) *Planner {
	t.Helper()
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "001.mmap"), navfixture.MmapParams(), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, tileFile(fixtureMap, 12, 12)), navfixture.BuildTileOffMesh(polys, links, 1.5, 0, 0), 0o644); err != nil {
		t.Fatal(err)
	}
	p := NewPlanner(dir)
	t.Cleanup(p.Close)
	return p
}

// TestOffMeshLinkJoinsTwoFloors fails today: the far square is unreachable,
// the path ends on the near square. With the link flags fixed it is Complete,
// with OffMesh set on exactly the link segment and no ground sampled there.
func TestOffMeshLinkJoinsTwoFloors(t *testing.T) {
	polys := append(append([]navfixture.Poly{}, navfixture.LCorridor(0)...), navfixture.Square(46, 20, 50, 24, -10))
	links := [][2][3]float32{{{48, 14, 0}, {48, 22, -10}}}
	p := linkTile(t, polys, links)
	path, err := p.Plan(fixtureMap, Vec3{12, 12, 0}, Vec3{48, 22, -10})
	if err != nil {
		t.Fatal(err)
	}
	if path.Status != Complete {
		t.Fatalf("status %s, want complete: %s", path.Status, path.Reason)
	}
	if len(path.OffMesh) != len(path.Points) {
		t.Fatalf("OffMesh len %d, want %d", len(path.OffMesh), len(path.Points))
	}
	n := 0
	for i, b := range path.OffMesh {
		if b {
			n++
			if i == 0 || i >= len(path.Points)-1 {
				t.Errorf("OffMesh at %d, want a middle segment", i)
			}
			if i < len(path.Ground) && path.Ground[i] != nil {
				t.Errorf("Ground[%d] sampled on a link: %v", i, path.Ground[i])
			}
		}
	}
	if n != 1 {
		t.Errorf("OffMesh true on %d segments, want exactly 1 (%v)", n, path.Points)
	}
	var want float32
	for i := 1; i < len(path.Points); i++ {
		dx := float64(path.Points[i][0] - path.Points[i-1][0])
		dy := float64(path.Points[i][1] - path.Points[i-1][1])
		dz := float64(path.Points[i][2] - path.Points[i-1][2])
		want += float32(math.Sqrt(dx*dx + dy*dy + dz*dz))
	}
	if math.Abs(float64(path.Length-want)) > 0.01 {
		t.Errorf("length %.3f, want 3D segment length %.3f", path.Length, want)
	}
}
// offMeshSegments checks that exactly the wanted segments carry the off-mesh
// flag and that no ground is sampled on a link: with the straight-path flags
// read from an uninitialized buffer, ordinary segments randomly carried the
// off-mesh bit and real flags were lost.
func offMeshSegments(t *testing.T, path Path, want ...int) {
	t.Helper()
	if len(path.OffMesh) != len(path.Points) {
		t.Fatalf("OffMesh len %d, want %d", len(path.OffMesh), len(path.Points))
	}
	if path.Status != Complete {
		t.Fatalf("status %s, want complete: %s", path.Status, path.Reason)
	}
	got := map[int]bool{}
	for i, b := range path.OffMesh {
		if !b {
			continue
		}
		got[i] = true
		if i >= len(path.Points)-1 {
			t.Errorf("OffMesh at %d, want a segment start", i)
		}
		if i < len(path.Ground) && path.Ground[i] != nil {
			t.Errorf("Ground[%d] sampled on a link: %v", i, path.Ground[i])
		}
	}
	if len(got) != len(want) {
		t.Fatalf("OffMesh true on segments %v, want %v (%v)", got, want, path.Points)
	}
	for _, i := range want {
		if !got[i] {
			t.Errorf("segment %d misses the off-mesh flag (%v)", i, path.Points)
		}
	}
}

// TestOffMeshFlagsOnlyOnTheLink plans the same link 50 times: a walk of
// several normal segments round one link must flag only the link segment,
// every time. Reading the flags from an uninitialized buffer made ordinary
// segments randomly carry the off-mesh bit.
func TestOffMeshFlagsOnlyOnTheLink(t *testing.T) {
	polys := append(append([]navfixture.Poly{}, navfixture.LCorridor(0)...), navfixture.Square(46, 20, 50, 24, -10))
	links := [][2][3]float32{{{48, 14, 0}, {48, 22, -10}}}
	p := linkTile(t, polys, links)
	for range 50 {
		path, err := p.Plan(fixtureMap, Vec3{12, 12, 0}, Vec3{48, 22, -10})
		if err != nil {
			t.Fatal(err)
		}
		link := -1
		for i := 1; i+1 < len(path.Points); i++ {
			if near(path.Points[i], Vec3{48, 14, 0}, 0.6) && near(path.Points[i+1], Vec3{48, 22, -10}, 0.6) {
				link = i
			}
		}
		if link < 0 {
			t.Fatalf("no link segment in %v", path.Points)
		}
		offMeshSegments(t, path, link)
	}
}

// TestOffMeshFlagsTwoLinks: two links in a row flag exactly the two link
// segments, 50 times. The second link drops from the middle square to a far
// one, so every segment between the links stays ordinary ground.
func TestOffMeshFlagsTwoLinks(t *testing.T) {
	polys := append(append([]navfixture.Poly{}, navfixture.LCorridor(0)...), navfixture.Square(46, 20, 50, 24, -10), navfixture.Square(46, 30, 50, 34, -20))
	links := [][2][3]float32{{{48, 14, 0}, {48, 22, -10}}, {{48, 24, -10}, {48, 32, -20}}}
	p := linkTile(t, polys, links)
	for range 50 {
		path, err := p.Plan(fixtureMap, Vec3{12, 12, 0}, Vec3{48, 32, -20})
		if err != nil {
			t.Fatal(err)
		}
		var segs []int
		for i := 0; i+1 < len(path.Points); i++ {
			a, b := path.Points[i], path.Points[i+1]
			if near(a, Vec3{48, 14, 0}, 0.6) && near(b, Vec3{48, 22, -10}, 0.6) {
				segs = append(segs, i)
			}
			if near(a, Vec3{48, 24, -10}, 0.6) && near(b, Vec3{48, 32, -20}, 0.6) {
				segs = append(segs, i)
			}
		}
		if len(segs) != 2 {
			t.Fatalf("want 2 link segments, got %d in %v", len(segs), path.Points)
		}
		offMeshSegments(t, path, segs...)
	}
}

// TestOffMeshLinkIgnoredWithoutLink: the same floors without the link stay
// Partial. This proves the test above sees the link, not a seam.
func TestOffMeshLinkIgnoredWithoutLink(t *testing.T) {
	polys := append(append([]navfixture.Poly{}, navfixture.LCorridor(0)...), navfixture.Square(46, 20, 50, 24, -10))
	p := planner(t, polys)
	path, err := p.Plan(fixtureMap, Vec3{12, 12, 0}, Vec3{48, 22, -10})
	if err != nil {
		t.Fatal(err)
	}
	if path.Status != Partial {
		t.Fatalf("status %s, want partial without the link", path.Status)
	}
}

// TestAvoidBoxDoesNotMarkALink: an avoid box over the link's middle keeps the
// path Complete: nd_mark_box never marks a link.
func TestAvoidBoxDoesNotMarkALink(t *testing.T) {
	polys := append(append([]navfixture.Poly{}, navfixture.LCorridor(0)...), navfixture.Square(46, 20, 50, 24, -10))
	links := [][2][3]float32{{{48, 14, 0}, {48, 22, -10}}}
	p := linkTile(t, polys, links)
	// Over the link's middle only: the landing square (y 20..24) stays out,
	// so only a marked link could cut the route.
	p.SetAvoid(fixtureMap, []Box{{Min: Vec3{47, 16, -8}, Max: Vec3{49, 19, -2}}})
	path, err := p.Plan(fixtureMap, Vec3{12, 12, 0}, Vec3{48, 22, -10})
	if err != nil {
		t.Fatal(err)
	}
	if path.Status != Complete {
		t.Fatalf("status %s with an avoid box over the link, want complete: %s", path.Status, path.Reason)
	}
}

// TestFloorsSkipsLinks: Floors under the link's midpoint lists no floor between
// the two squares' heights.
func TestFloorsSkipsLinks(t *testing.T) {
	polys := append(append([]navfixture.Poly{}, navfixture.LCorridor(0)...), navfixture.Square(46, 20, 50, 24, -10))
	links := [][2][3]float32{{{48, 14, 0}, {48, 22, -10}}}
	p := linkTile(t, polys, links)
	fl, err := p.Floors(fixtureMap, 48, 18, -50, 50)
	if err != nil {
		t.Fatal(err)
	}
	for _, f := range fl {
		if f.Z < -0.5 && f.Z > -9.5 {
			t.Errorf("floor on a link at %.2f: %v", f.Z, fl)
		}
	}
}
