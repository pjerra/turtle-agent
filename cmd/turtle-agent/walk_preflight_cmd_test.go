package main

import (
	"bytes"
	"strings"
	"testing"
)

// walk-preflight --binary reaches the options, and it is optional: without it the preflight checks this executable,
// as raid-autopilot launches. The live proof's fix hint named a --binary flag that did not exist (round 2).
func TestWalkPreflightArgs(t *testing.T) {
	var errBuf bytes.Buffer
	opts, asJSON, code := parsePreflightArgs([]string{"--binary", "/opt/ta/bin/turtle-agent", "--server", "m910q,other", "--json", "/walks/w"}, &errBuf)
	if code != exitOK {
		t.Fatalf("exit %d: %s", code, errBuf.String())
	}
	if opts.Binary != "/opt/ta/bin/turtle-agent" || opts.Dir != "/walks/w" || len(opts.Servers) != 2 ||
		opts.Servers[0] != "m910q" || opts.Servers[1] != "other" || !asJSON {
		t.Fatalf("options %+v, json %v", opts, asJSON)
	}
	// Without --binary the option stays empty: Run fills it with this executable.
	opts, asJSON, code = parsePreflightArgs([]string{"/walks/w"}, &errBuf)
	if code != exitOK || opts.Binary != "" || asJSON {
		t.Fatalf("no --binary: %+v, json %v, exit %d", opts, asJSON, code)
	}
	// One walk directory, exactly.
	if _, _, code = parsePreflightArgs([]string{"/a", "/b"}, &errBuf); code != exitUsage {
		t.Fatalf("two directories exited %d, want usage", code)
	}
	if _, _, code = parsePreflightArgs(nil, &errBuf); code != exitUsage || !strings.Contains(errBuf.String(), walkPreflightUsage) {
		t.Fatalf("no directory exited %d: %s", code, errBuf.String())
	}
}
