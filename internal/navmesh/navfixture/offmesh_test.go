package navfixture

import (
	"crypto/sha256"
	"encoding/hex"
	"testing"
)

// The off-mesh tile bytes MoveMapGen would write: golden sha256, as for the
// existing fixtures.
func TestBuildTileOffMeshMatchesGolden(t *testing.T) {
	for name, c := range map[string]struct {
		polys []Poly
		links [][2][3]float32
		want  string
	}{
		"linked": {TwoFloorsLinked(40).Polys(), TwoFloorsLinked(40).Links(), "aa4d396e1e282eebeeb44f56cf27d6559c1dae5ee38efa8966e4158603cf14e6"},
	} {
		sum := sha256.Sum256(BuildTileOffMesh(c.polys, c.links, 1.5, 0, 0))
		if got := hex.EncodeToString(sum[:]); got != c.want {
			t.Errorf("%s: sha256 %s, want %s (record, then pin)", name, got, c.want)
		}
	}
}
