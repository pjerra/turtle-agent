//go:build cgo

package navmesh

import (
	"math"
	"os"
	"path/filepath"
	"testing"
)

// serverMmaps finds the navmesh files of maps 808, 818 and 820 (808.mmap and the
// 808*.mmtile tiles, and the same for the others): $TURTLE_AGENT_MMAPS or
// ~/turtle-agent-mmaps, as in zulfarrakMmaps
// (internal/dungeon/zulfarrak_test.go:410-425).
func serverMmaps() string {
	home, _ := os.UserHomeDir()
	for _, d := range []string{os.Getenv("TURTLE_AGENT_MMAPS"), filepath.Join(home, "turtle-agent-mmaps")} {
		if d == "" {
			continue
		}
		ok := true
		for _, n := range []string{"808", "818", "820"} {
			if _, err := os.Stat(filepath.Join(d, n+".mmap")); err != nil {
				ok = false
				break
			}
			if tiles, _ := filepath.Glob(filepath.Join(d, n+"*.mmtile")); len(tiles) == 0 {
				ok = false
				break
			}
		}
		if ok {
			return d
		}
	}
	return ""
}

func serverPlanner(t *testing.T) *Planner {
	t.Helper()
	dir := serverMmaps()
	if dir == "" {
		t.Skip("no map 808/818/820 navmesh files: set TURTLE_AGENT_MMAPS to a folder holding 808.mmap, 818.mmap and 820.mmap and the tiles")
	}
	p := NewPlanner(dir)
	t.Cleanup(p.Close)
	return p
}

// TestServerPathWindhornStep641RunsOutOfNodes is case 1: the 986 yd climb the
// offline query takes with a complete path, but the server's 2048-node query
// cannot search. The server answers NOPATH, so the run leader's CheckMove
// refuses the hop "unreachable".
func TestServerPathWindhornStep641RunsOutOfNodes(t *testing.T) {
	p := serverPlanner(t)
	from := Vec3{-7640.0, -3636.0, 197.3}
	to := Vec3{-7639.95, -3636.02, 224.08}
	path, err := p.Plan(820, from, to)
	if err != nil {
		t.Fatal(err)
	}
	if path.Status != Complete || path.Length <= 900 {
		t.Fatalf("offline: %+v (want complete, length > 900)", path)
	}
	sp, err := p.ServerPath(820, from, to)
	if err != nil {
		t.Fatal(err)
	}
	if sp.Type&PathNoPath == 0 {
		t.Errorf("server type 0x%x, want NOPATH (0x8)", sp.Type)
	}
	if v := ServerMoveVerdict(sp, from, to, 0); v != "unreachable" {
		t.Errorf("verdict %q, want unreachable", v)
	}
}

// TestServerPathHateforgeTerraceStartIsOK is the control: from the terrace
// itself (where plan-check used to start) both queries take the hop.
func TestServerPathHateforgeTerraceStartIsOK(t *testing.T) {
	p := serverPlanner(t)
	from := Vec3{-8321.5, -3447.5, 181.0}
	to := Vec3{-8309.45, -3460.42, 194.43}
	sp, err := p.ServerPath(808, from, to)
	if err != nil {
		t.Fatal(err)
	}
	if sp.Type != PathNormal {
		t.Errorf("server type 0x%x, want NORMAL (0x1)", sp.Type)
	}
	if v := ServerMoveVerdict(sp, from, to, 0); v != "ok" {
		t.Errorf("verdict %q, want ok", v)
	}
}

// TestServerPathHateforgeLowerStartWalksIntoTheSteepPocket is case 2: from the
// lower poly where the lead really stood, the server's filter (steep included)
// walks down the 0x11 slope and ends INCOMPLETE in the pocket, which CheckMove
// accepts. The offline query (steep excluded) sees only a 1-poly island.
func TestServerPathHateforgeLowerStartWalksIntoTheSteepPocket(t *testing.T) {
	p := serverPlanner(t)
	from := Vec3{-8320.0, -3447.5, 180.1}
	to := Vec3{-8309.45, -3460.42, 194.43}
	want := Vec3{-8312.5, -3474.8, 176.1}
	sp, err := p.ServerPath(808, from, to)
	if err != nil {
		t.Fatal(err)
	}
	if sp.Type&PathIncomplete == 0 {
		t.Errorf("server type 0x%x, want INCOMPLETE (0x4)", sp.Type)
	}
	if !near(sp.ActualEnd, want, 1.5) {
		t.Errorf("actual end %v, want %v (±1.5)", sp.ActualEnd, want)
	}
	if v := ServerMoveVerdict(sp, from, to, 0); v != "ok" {
		t.Errorf("verdict %q, want ok", v)
	}
	fl, err := p.Floors(808, want[0], want[1], want[2]-8, want[2]+8)
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, f := range fl {
		if math.Abs(float64(f.Z-want[2])) <= 1.5 && f.Flags&NavSteep != 0 {
			found = true
		}
	}
	if !found {
		t.Errorf("no steep floor at the pocket end %+v", fl)
	}
}

// TestServerPathHateforgePocketIsUnreachable is case 2's second half: from the
// pocket the server's own answer is NOPATH (or an incomplete leg that makes no
// progress), 22-25 yd from the goal.
func TestServerPathHateforgePocketIsUnreachable(t *testing.T) {
	p := serverPlanner(t)
	from := Vec3{-8312.5, -3474.8, 176.1}
	to := Vec3{-8309.45, -3460.42, 194.43}
	sp, err := p.ServerPath(808, from, to)
	if err != nil {
		t.Fatal(err)
	}
	if v := ServerMoveVerdict(sp, from, to, 0); v != "unreachable" {
		t.Errorf("verdict %q, want unreachable (type 0x%x)", v, sp.Type)
	}
	d := dist3D(sp.ActualEnd, to)
	if sp.Type&PathNoPath != 0 {
		d = dist3D(from, to)
	}
	if d < 22 || d > 25 {
		t.Errorf("distance %.1f, want 22-25", d)
	}
}

// TestServerPathStormwroughtIslandAfterTeleport is case 3's second half: from
// the real floor after the rescue teleport, the ledge poly is 3.6 yd above the
// lead, so the server refuses the hop.
func TestServerPathStormwroughtIslandAfterTeleport(t *testing.T) {
	p := serverPlanner(t)
	from := Vec3{-6333.4, -3947.2, 215.8}
	to := Vec3{-6332.8, -3946.7, 219.4}
	sp, err := p.ServerPath(818, from, to)
	if err != nil {
		t.Fatal(err)
	}
	if v := ServerMoveVerdict(sp, from, to, 0); v != "unreachable" {
		t.Errorf("verdict %q, want unreachable (type 0x%x)", v, sp.Type)
	}
}

// TestServerPathStormwroughtStartPolyCutoff is case 3's first half: the ledge
// poly 2.8 yd above the lead still answers, 3.6 yd above it does not.
func TestServerPathStormwroughtStartPolyCutoff(t *testing.T) {
	p := serverPlanner(t)
	to := Vec3{-6333.1, -3947.2, 219.3}
	ok, err := p.ServerPath(818, Vec3{-6317.6, -3945.9, 215.8}, to)
	if err != nil {
		t.Fatal(err)
	}
	if v := ServerMoveVerdict(ok, Vec3{-6317.6, -3945.9, 215.8}, to, 0); v != "ok" {
		t.Errorf("z=215.8: verdict %q, want ok (type 0x%x)", v, ok.Type)
	}
	if math.Abs(float64(ok.StartDist)-2.8) > 1.5 {
		t.Errorf("z=215.8: startDist %.1f, want ~2.8", ok.StartDist)
	}
	bad, err := p.ServerPath(818, Vec3{-6317.6, -3945.9, 215.5}, to)
	if err != nil {
		t.Fatal(err)
	}
	if bad.Type&PathNoPath == 0 || bad.StartDist != 0 {
		t.Errorf("z=215.5: type 0x%x startDist %.1f, want NOPATH and 0", bad.Type, bad.StartDist)
	}
}

// TestServerMoveVerdictBranches covers every branch of ServerMoveVerdict in
// order, with synthetic ServerPath values (no tiles).
func TestServerMoveVerdictBranches(t *testing.T) {
	from := Vec3{0, 0, 0}
	far := Vec3{100, 0, 0}
	near := Vec3{0.5, 0, 0}
	mid := Vec3{30, 0, 0}
	// 1. reactDistance first: too far wins over everything.
	sp := ServerPath{Type: PathNoPath, ActualEnd: from}
	if v := ServerMoveVerdict(sp, from, far, 60); v != "too-far" {
		t.Errorf("react: %q, want too-far", v)
	}
	// 2. within AtLeadYd: ok even when far from the react distance is 0.
	if v := ServerMoveVerdict(sp, from, near, 0); v != "ok" {
		t.Errorf("at lead: %q, want ok", v)
	}
	// 3. NoPath gives unreachable.
	if v := ServerMoveVerdict(sp, from, mid, 0); v != "unreachable" {
		t.Errorf("nopath: %q, want unreachable", v)
	}
	// 4. Incomplete with no progress (end within NoProgressYd of from): unreachable.
	pocket := ServerPath{Type: PathIncomplete, ActualEnd: Vec3{1.9, 0, 0}}
	if v := ServerMoveVerdict(pocket, from, mid, 0); v != "unreachable" {
		t.Errorf("incomplete no-progress: %q, want unreachable", v)
	}
	// 5. Incomplete with progress: ok (the run leader walks into the pocket).
	pocket.ActualEnd = Vec3{2.1, 0, 0}
	if v := ServerMoveVerdict(pocket, from, mid, 0); v != "ok" {
		t.Errorf("incomplete progress: %q, want ok", v)
	}
	// 6. End more than MaxMoveEndOffset from the goal: off-mesh.
	off := ServerPath{Type: PathNormal, ActualEnd: Vec3{24, 0, 0}}
	if v := ServerMoveVerdict(off, from, mid, 0); v != "off-mesh" {
		t.Errorf("off-mesh: %q, want off-mesh", v)
	}
	// 7. Otherwise ok.
	ok := ServerPath{Type: PathNormal, ActualEnd: Vec3{29, 0, 0}}
	if v := ServerMoveVerdict(ok, from, mid, 0); v != "ok" {
		t.Errorf("ok: %q, want ok", v)
	}
}
