package main

import (
	"bytes"
	"context"
	"strings"
	"testing"
)

// Item 1 (R2): every command named in usage must have a dispatcher case.
// A dropped case (route-oracle / route-edits in f5c05b7) falls through to
// usage/exit 2; this test runs the dispatcher with failing args for each
// command and asserts none falls through to the global usage (which starts
// with "usage:\n  turtle-agent login"). Commands print their own usage line.
// Keep in step with usage() in main.go when adding a command.
func TestRunDispatchesEveryUsageCommand(t *testing.T) {
	globalPrefix := "usage:\n  turtle-agent login"
	for cmd, args := range map[string][]string{
		"login":           {},
		"fleet":           {},
		"scrub-check":     {},
		"capture":         {"bogus-subcommand"},
		"map":             {},
		"tune-diff":       {},
		"capture-stats":   {},
		"trace-score":     {},
		"pulltest":        {},
		"rotation":        {"bogus-subcommand"},
		"dashboard":       {"--help"},
		"minimap-extract": {},
		"plan-export":     {},
		"plan-check":      {},
		"path-check":      {},
		"stage-check":     {"--mmaps", "bogus-mmaps", "bogus-plan!", "0"},
		"route-oracle":    {},
		"route-edits":     {"bogus-subcommand"},
		"lease":           {"bogus-subcommand"},
		"slot":            {"setup"},
		"teardown":        {},
		"rpc":             {},
	} {
		var out, errOut bytes.Buffer
		code := run(context.Background(), append([]string{cmd}, args...), &out, &errOut)
		if code != exitUsage {
			t.Fatalf("%s: exit %d, want %d", cmd, code, exitUsage)
		}
		if strings.HasPrefix(errOut.String(), globalPrefix) {
			t.Fatalf("%s fell through to the default usage branch", cmd)
		}
	}
	// Unknown words still fall through to the global usage.
	var out, errOut bytes.Buffer
	code := run(context.Background(), []string{"no-such-cmd"}, &out, &errOut)
	if code != exitUsage || !strings.HasPrefix(errOut.String(), globalPrefix) {
		t.Fatalf("unknown command: exit %d stderr %q", code, errOut.String())
	}
}
