//go:build cgo

package navmesh

import (
	"os"
	"path/filepath"
	"testing"
)

// TestLiquidAtZulGurubWaterPoints reads the server's own tiles: the Hakkar lake bed the walk-33 lead stalled on and the canal
// under its rim are liquid to the mmaps' NavWater floors, and the lake point's own floor is dry ground (a steep slope), so
// only the nearby surface finds it. It runs when the map 309 tiles are present (TURTLE_AGENT_MMAPS or ~/turtle-agent-mmaps).
func TestLiquidAtZulGurubWaterPoints(t *testing.T) {
	home, _ := os.UserHomeDir()
	dir := ""
	for _, d := range []string{os.Getenv("TURTLE_AGENT_MMAPS"), filepath.Join(home, "turtle-agent-mmaps")} {
		if d == "" {
			continue
		}
		if _, err := os.Stat(filepath.Join(d, "309.mmap")); err == nil {
			if tiles, _ := filepath.Glob(filepath.Join(d, "309*.mmtile")); len(tiles) > 0 {
				dir = d
				break
			}
		}
	}
	if dir == "" {
		t.Skip("no map 309 navmesh files: set TURTLE_AGENT_MMAPS to a folder holding 309.mmap and its tiles")
	}
	p := NewPlanner(dir)
	defer p.Close()
	for _, q := range []Vec3{{-11862.4, -1508.5, -8.1}, {-11944.3, -1353.8, 62.3}} {
		liq, err := LiquidAt(p, 309, q)
		if err != nil {
			t.Fatalf("%v: %v", q, err)
		}
		if !liq.Wet {
			t.Fatalf("%v: not liquid", q)
		}
	}
	// The lake bed's own floor is the generator's ground (steep): the nearby surface is what finds it.
	liq, err := LiquidAt(p, 309, Vec3{-11862.4, -1508.5, -8.1})
	if err != nil || liq.Own {
		t.Fatalf("lake bed: %+v, %v, want a nearby surface, not its own floor", liq, err)
	}
}
