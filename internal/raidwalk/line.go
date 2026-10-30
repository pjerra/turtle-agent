// Package raidwalk holds the pure parts of `turtle-agent raid-walk`: the roster grammar, the `until`/`from`
// clauses, the run-line reader, the run tracker, the GM allow-list, the revive-point quiet check and the summary.
// Nothing here talks to a server; cmd/turtle-agent/raid_walk_phases.go feeds it.
package raidwalk

import (
	"strings"

	"github.com/pjerra/turtle-agent/internal/tbm"
)

// CleanLine removes the control bytes a TBM line may carry (C0, DEL and C1) before it is parsed. The raw event text
// is read through tbm.Line, but a stray control byte must not break a field boundary (spec 0.1).
func CleanLine(s string) string {
	var b strings.Builder
	b.Grow(len(s))
	for _, r := range s {
		if r < 0x20 || r == 0x7f || (r >= 0x80 && r <= 0x9f) {
			continue
		}
		b.WriteRune(r)
	}
	return b.String()
}

// ParseRun reads one TBM:RUN line after CleanLine.
func ParseRun(line string) (tbm.RunStatus, bool) {
	return tbm.ParseRunLine(CleanLine(line))
}
