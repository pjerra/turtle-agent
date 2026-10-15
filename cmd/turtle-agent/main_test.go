package main

import (
	"bytes"
	"context"
	"os"
	"strings"
	"testing"
)

// TestMain keeps the run traces that dungeon_run writes by default out of the source tree.
func TestMain(m *testing.M) {
	dir, err := os.MkdirTemp("", "turtle-agent-traces-")
	if err != nil {
		os.Exit(1)
	}
	os.Setenv(tracesDirEnv, dir)
	code := m.Run()
	os.RemoveAll(dir)
	os.Exit(code)
}

func TestRunWithoutArgsPrintsUsage(t *testing.T) {
	var out, errOut bytes.Buffer
	if code := run(context.Background(), nil, &out, &errOut); code != exitUsage {
		t.Fatalf("exit code %d, want %d", code, exitUsage)
	}
	if !strings.Contains(errOut.String(), "turtle-agent login") {
		t.Fatalf("usage missing: %q", errOut.String())
	}
}

func TestRunVersion(t *testing.T) {
	var out, errOut bytes.Buffer
	if code := run(context.Background(), []string{"version"}, &out, &errOut); code != exitOK {
		t.Fatalf("exit code %d", code)
	}
	if got := strings.TrimSpace(out.String()); got != "turtle-agent 0.1.0" {
		t.Fatalf("got %q", got)
	}
}
