package raidwalk

import (
	"strings"
	"testing"

	"github.com/pjerra/turtle-agent/internal/tbm"
)

func TestCleanLineAndParseRun(t *testing.T) {
	raw := "TBM:RUN|onyxias_lair\r|running|29/29|onyxia|boss|begin|arrived|speed 3.0 (test)"
	st, ok := ParseRun(raw)
	if !ok {
		t.Fatalf("the raw line did not parse: %q", CleanLine(raw))
	}
	if st.Plan != "onyxias_lair" || st.State != tbm.RunRunning || st.Step != 29 || st.Steps != 29 {
		t.Fatalf("parsed %+v", st)
	}

	// The walk-3 lesson (spec 0.1): the colour-stripped text is not a TBM:RUN line. The parser must not be
	// widened to accept it; the raw form in data.raw is the one to read.
	stripped := "TBM:RUN|onyxias_lairunning|29/29|onyxia|boss|begin|arrived|speed 3.0 (test)"
	if st, ok := ParseRun(stripped); ok {
		t.Fatalf("the stripped line parsed as %+v; it must not", st)
	}

	if got, want := CleanLine("a\x00b\x1fc\x7fd\u0080e\u009ff"), "abcdef"; got != want {
		t.Fatalf("CleanLine = %q, want %q", got, want)
	}
	if got := CleanLine("keep\tTAB?\nno"); strings.ContainsAny(got, "\t\n") {
		t.Fatalf("CleanLine kept a control byte: %q", got)
	}
}
