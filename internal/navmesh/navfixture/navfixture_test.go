package navfixture

import (
	"crypto/sha256"
	"encoding/hex"
	"testing"
)

// The Go builder writes the same bytes as the harness's Python one (navfixture.py), whose
// tiles the core's Detour is known to load.
func TestBuildTileMatchesTheHarnessFixture(t *testing.T) {
	for name, c := range map[string]struct {
		polys []Poly
		want  string
	}{
		"l_corridor": {LCorridor(0), "479cbc70765f6d5d961f0142a3ba747b939c3c37a81c140cf9958032c273c8eb"},
		"ring 2.5":   {Ring(2.5), "56d84a4f2a69b99cc7f615f2c204beeb96ce50c5c6c87378efa9cc9833ba92d5"},
	} {
		sum := sha256.Sum256(BuildTile(c.polys, nil, 0, 0))
		if got := hex.EncodeToString(sum[:]); got != c.want {
			t.Errorf("%s: sha256 %s, want %s", name, got, c.want)
		}
	}
	if got := hex.EncodeToString(MmapParams()); got != "000000000000fac30000000055550544555505440400000000000100" {
		t.Errorf("mmap params %s", got)
	}
}

func TestTileNameFollowsTheCore(t *testing.T) {
	// Uldaman entrance: gx from y, gy from x (GridMap "reversed"), file (map, gy, gx).
	if got := tileName(70, -226.8, 49.1); got != "0703231.mmtile" {
		t.Fatal(got)
	}
	if got := mmapName(70); got != "070.mmap" {
		t.Fatal(got)
	}
}
