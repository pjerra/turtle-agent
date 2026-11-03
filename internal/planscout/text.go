package planscout

import (
	"fmt"
	"io"
)

// Text writes one line per non-ok step, then the counts and the runtime.
func (r *Report) Text(w io.Writer) {
	for _, s := range r.Steps {
		if s.Verdict == VerdictOK || s.Verdict == VerdictSkip {
			continue
		}
		fmt.Fprintf(w, "step %d (kind '%s') %s:", s.Step, s.Kind, upper(s.Verdict))
		for _, c := range s.Checks {
			if c.Verdict == VerdictOK {
				continue
			}
			fmt.Fprintf(w, " %s: %s;", c.Name, c.Reason)
		}
		fmt.Fprintln(w)
	}
	fmt.Fprintf(w, "%d steps: %d ok, %d warn, %d fail, %d skip (%.1f s)\n", len(r.Steps), r.Counts.OK, r.Counts.Warn, r.Counts.Fail, r.Counts.Skip, r.Runtime)
}

func upper(v string) string {
	switch v {
	case VerdictFail:
		return "FAIL"
	case VerdictWarn:
		return "WARN"
	case VerdictSkip:
		return "SKIP"
	}
	return "OK"
}
