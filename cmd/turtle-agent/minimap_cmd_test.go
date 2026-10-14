package main

import (
	"bytes"
	"strings"
	"testing"
)

func TestMinimapExtractFlags(t *testing.T) {
	for name, args := range map[string][]string{
		"no client":  {"minimap-extract", "--out", t.TempDir()},
		"no out":     {"minimap-extract", "--client", t.TempDir()},
		"bad maps":   {"minimap-extract", "--client", t.TempDir(), "--out", t.TempDir(), "--maps", "x"},
		"bad prefer": {"minimap-extract", "--client", t.TempDir(), "--out", t.TempDir(), "--prefer", "paper"},
		"stray arg":  {"minimap-extract", "--client", t.TempDir(), "--out", t.TempDir(), "extra"},
	} {
		var out, errb bytes.Buffer
		if code := run(t.Context(), args, &out, &errb); code != exitUsage {
			t.Errorf("%s: exit %d, want %d (%s)", name, code, exitUsage, errb.String())
		}
	}
}

func TestMinimapExtractWithoutArchives(t *testing.T) {
	var out, errb bytes.Buffer
	if code := run(t.Context(), []string{"minimap-extract", "--client", t.TempDir(), "--list"}, &out, &errb); code != 1 || !strings.Contains(errb.String(), "none of the archives exist") {
		t.Errorf("exit %d, stderr %q", code, errb.String())
	}
}
