// Package testfixture loads the capture fixtures that the milestones' live steps record
// under testdata/captures, for tests only.
package testfixture

import (
	"path/filepath"
	"runtime"
	"testing"

	"github.com/TortoiseBots/turtle-agent/internal/capture"
)

// Load reads testdata/captures/name from the repository root and fails the test when
// the fixture is missing or unreadable.
func Load(t testing.TB, name string) []capture.Record {
	t.Helper()
	_, here, _, _ := runtime.Caller(0)
	path := filepath.Join(filepath.Dir(here), "..", "..", "testdata", "captures", name)
	recs, err := capture.ReadFile(path)
	if err != nil {
		t.Fatalf("fixture %s: %v (record it with the milestone's live step)", name, err)
	}
	return recs
}
