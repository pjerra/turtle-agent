package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// Path-check usage is anything but flags plus exactly 6 positional numbers:
// exit 2, on flags, counts and non-numbers alike.
func TestPathCheckCmdUsage(t *testing.T) {
	for _, args := range [][]string{
		{},
		{"--mmaps", "x"},
		{"--mmaps", "x", "--map", "1"},
		{"--mmaps", "x", "--map", "1", "--", "0", "0", "0", "1", "1"},           // 5 numbers
		{"--mmaps", "x", "--map", "1", "--", "0", "0", "0", "1", "1", "1", "2"}, // 7 numbers
		{"--mmaps", "x", "--map", "1", "--", "0", "0", "0", "1", "1", "far"},    // not a number
		{"--mmaps", "x", "--map", "1", "-1", "0", "0", "0", "1", "1"},           // no --: -1 parses as a flag
		{"--nope", "1"},
	} {
		var out, errOut bytes.Buffer
		if code := pathCheckCmd(args, &out, &errOut); code != exitUsage {
			t.Errorf("%v: exit %d, want %d (%s)", args, code, exitUsage, errOut.String())
		}
		if out.String() != "" {
			t.Errorf("%v: stdout %q on a usage error", args, out.String())
		}
	}
}

// A query the navmesh cannot answer (here: a folder with no tiles) is exit 1,
// not a usage error.
func TestPathCheckCmdQueryError(t *testing.T) {
	var out, errOut bytes.Buffer
	args := []string{"--mmaps", t.TempDir(), "--map", "1", "--", "0", "0", "0", "1", "1", "1"}
	if code := pathCheckCmd(args, &out, &errOut); code != exitCheckFailed {
		t.Fatalf("exit %d, want %d (%s)", code, exitCheckFailed, errOut.String())
	}
	if !strings.Contains(errOut.String(), "path-check: ") {
		t.Errorf("no query error on stderr: %q", errOut.String())
	}
}

// The server's NOPATH prints NOPATH and no "yd short" line (the pocket-to-boss
// hop on map 808: the server's query has no path from the steep pocket).
func TestPathCheckCmdNopath(t *testing.T) {
	home, _ := os.UserHomeDir()
	dir := ""
	for _, d := range []string{os.Getenv("TURTLE_AGENT_MMAPS"), filepath.Join(home, "tortoise-wow-server", "data", "mmaps")} {
		if d == "" {
			continue
		}
		if _, err := os.Stat(filepath.Join(d, "808.mmap")); err == nil {
			dir = d
			break
		}
	}
	if dir == "" {
		t.Skip("no map 808 navmesh files: set TURTLE_AGENT_MMAPS to a folder holding 808.mmap and the tiles")
	}
	var out, errOut bytes.Buffer
	args := []string{"--mmaps", dir, "--map", "808", "--",
		"-8312.5", "-3474.8", "176.1", "-8309.45", "-3460.42", "194.43"}
	if code := pathCheckCmd(args, &out, &errOut); code != exitOK {
		t.Fatalf("exit %d (%s)", code, errOut.String())
	}
	if !strings.Contains(out.String(), "NOPATH") {
		t.Fatalf("no NOPATH in %q", out.String())
	}
	if strings.Contains(out.String(), "yd short") {
		t.Fatalf("a NOPATH prints no yd-short line: %q", out.String())
	}
}

// A hop the server takes prints the type, the verdict and the shortfall.
func TestPathCheckCmdOk(t *testing.T) {
	home, _ := os.UserHomeDir()
	dir := ""
	for _, d := range []string{os.Getenv("TURTLE_AGENT_MMAPS"), filepath.Join(home, "tortoise-wow-server", "data", "mmaps")} {
		if d == "" {
			continue
		}
		if _, err := os.Stat(filepath.Join(d, "808.mmap")); err == nil {
			dir = d
			break
		}
	}
	if dir == "" {
		t.Skip("no map 808 navmesh files: set TURTLE_AGENT_MMAPS to a folder holding 808.mmap and the tiles")
	}
	var out, errOut bytes.Buffer
	args := []string{"--mmaps", dir, "--map", "808", "--",
		"-8321.5", "-3447.5", "181.0", "-8309.45", "-3460.42", "194.43"}
	if code := pathCheckCmd(args, &out, &errOut); code != exitOK {
		t.Fatalf("exit %d (%s)", code, errOut.String())
	}
	for _, want := range []string{"offline: complete", "server: type 0x1 ok", "yd short"} {
		if !strings.Contains(out.String(), want) {
			t.Errorf("no %q in %q", want, out.String())
		}
	}
}
