package session

import (
	"os"
	"testing"

	"github.com/pjerra/turtle-agent/internal/spells"
)

// The spell tables load once per process. Loading them in the middle of a test would stall the
// session's loop for the time of one parse (long under the race detector), which timing checks
// such as TestReplayEqualsLiveEvents notice.
func TestMain(m *testing.M) {
	spells.Warm()
	os.Exit(m.Run())
}
