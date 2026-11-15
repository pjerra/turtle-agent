// The navmesh checks' skip rule: locally a test whose server map files (or the cgo navmesh engine that reads them)
// are absent skips, as before. The navmesh CI job (.github/workflows/navmesh.yml) provisions those files and sets
// mmapsRequiredEnv, and there the same absence must fail the test instead: a skip would hide a route check that never
// ran, which is how the tools/dungeongen route tests stayed outside the gate (route batch B6a round 3).
package main

import (
	"os"
	"testing"
)

// mmapsRequiredEnv, set only by the navmesh CI job, says the server map files are provisioned.
const mmapsRequiredEnv = "TURTLE_AGENT_MMAPS_REQUIRED"

// missingMmapsFatal fails the test when the navmesh job promised the map files and this test cannot find them;
// without the variable it returns and the caller keeps its local fallback (a skip, or a log and the checks that run
// without the mesh).
func missingMmapsFatal(t *testing.T, msg string) {
	t.Helper()
	if os.Getenv(mmapsRequiredEnv) != "" {
		t.Fatalf("%s (%s is set: the navmesh job provisions the map files)", msg, mmapsRequiredEnv)
	}
}

// skipMissingMmaps skips a test that needs the server map files when they are absent, and fails it in the navmesh job.
func skipMissingMmaps(t *testing.T, msg string) {
	t.Helper()
	missingMmapsFatal(t, msg)
	t.Skip(msg)
}
