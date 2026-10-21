package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/pjerra/turtle-agent/internal/navmesh/navfixture"
)

// outFixture is a one-leg plan on the fixture mesh: the plans folder and the mmaps folder.
func outFixture(t *testing.T) (plans, mmaps string) {
	t.Helper()
	plans = t.TempDir()
	plan := `{"version":1,"name":"corridor","map":0,"entrance":{"x":12,"y":12,"z":0},"legs":[
	 {"name":"hall","to":{"x":46,"y":12,"z":0},"pull_groups":[{"entries":[100],"positions":[{"x":44,"y":12,"z":0}]}]}]}`
	if err := os.WriteFile(filepath.Join(plans, "corridor.json"), []byte(plan), 0o644); err != nil {
		t.Fatal(err)
	}
	return plans, navfixture.WriteDir(t, 0, navfixture.LCorridor(0))
}

func runOracle(plans, mmaps, out, mode string) (int, string) {
	var stdout, stderr bytes.Buffer
	code := routeOracleCmd([]string{"--plans", plans, "--mmaps", mmaps, "--out", out, "--plan", "corridor", "--mode", mode}, &stdout, &stderr)
	return code, stderr.String()
}

func readOr(t *testing.T, p string) string {
	t.Helper()
	b, err := os.ReadFile(p)
	if err != nil {
		return "<" + err.Error() + ">"
	}
	return string(b)
}

// A report file that already exists as a symlink to somewhere outside --out is never written
// through: the target keeps its content (or stays absent when the link dangles), nothing at all
// is written, and the command fails. With the check and the rename removed, os.WriteFile followed
// the link and truncated the target.
func TestRouteOracleDoesNotWriteThroughASymlinkedReport(t *testing.T) {
	plans, mmaps := outFixture(t)
	for _, mode := range []string{"walk", "bosses"} {
		stem := "corridor"
		if mode == "bosses" {
			stem = "corridor-bosses"
		}
		for _, ext := range []string{".txt", ".json"} {
			for _, dangling := range []bool{false, true} {
				outside := t.TempDir()
				victim := filepath.Join(outside, "victim")
				if !dangling {
					if err := os.WriteFile(victim, []byte("KEEP"), 0o644); err != nil {
						t.Fatal(err)
					}
				}
				out := t.TempDir()
				if err := os.Symlink(victim, filepath.Join(out, stem+ext)); err != nil {
					t.Fatal(err)
				}
				code, stderr := runOracle(plans, mmaps, out, mode)
				name := mode + " " + ext + " dangling=" + map[bool]string{false: "no", true: "yes"}[dangling]
				if code != exitRouteOracle || !strings.Contains(stderr, "not a regular file") {
					t.Errorf("%s: exit %d stderr %q, want the refusal", name, code, stderr)
				}
				if dangling {
					if _, err := os.Lstat(victim); err == nil {
						t.Errorf("%s: the dangling link's target was created", name)
					}
				} else if got := readOr(t, victim); got != "KEEP" {
					t.Errorf("%s: the file outside --out now holds %q", name, got)
				}
				// Nothing else was written: the other report is absent, and no temp file is left.
				es, _ := os.ReadDir(out)
				if len(es) != 1 {
					t.Errorf("%s: --out holds %d entries, want only the planted link", name, len(es))
				}
			}
		}
	}
}

// A destination that is a folder is refused too.
func TestRouteOracleRefusesAFolderAsAReport(t *testing.T) {
	plans, mmaps := outFixture(t)
	out := t.TempDir()
	if err := os.Mkdir(filepath.Join(out, "corridor.json"), 0o755); err != nil {
		t.Fatal(err)
	}
	code, stderr := runOracle(plans, mmaps, out, "walk")
	if code != exitRouteOracle || !strings.Contains(stderr, "not a regular file") {
		t.Errorf("exit %d stderr %q", code, stderr)
	}
	if _, err := os.Stat(filepath.Join(out, "corridor.txt")); err == nil {
		t.Error("the other report was written")
	}
}

// --out that is a symlink, or has a symlink component, is refused before anything is made or
// written, and nothing lands in the link's target.
func TestRouteOracleRefusesASymlinkedOut(t *testing.T) {
	plans, mmaps := outFixture(t)
	base := t.TempDir()
	real := filepath.Join(base, "real")
	if err := os.Mkdir(real, 0o755); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(base, "link")
	if err := os.Symlink(real, link); err != nil {
		t.Fatal(err)
	}
	for _, out := range []string{link, filepath.Join(link, "sub"), filepath.Join(link, "a", "b")} {
		code, stderr := runOracle(plans, mmaps, out, "walk")
		if code != exitUsage || !strings.Contains(stderr, "contains a symlink") {
			t.Errorf("--out %s: exit %d stderr %q, want a usage refusal", out, code, stderr)
		}
	}
	if es, _ := os.ReadDir(real); len(es) != 0 {
		t.Errorf("the link's target holds %v: something was made through the link", es)
	}
	// A real folder still works.
	if code, stderr := runOracle(plans, mmaps, filepath.Join(base, "fresh", "deep"), "walk"); code != exitOK {
		t.Errorf("real --out: exit %d stderr %q", code, stderr)
	}
}

// The write itself does not follow a link planted after the check: the rename replaces the
// link and the file it pointed at is untouched.
func TestWriteThroughTempReplacesALinkWithoutFollowingIt(t *testing.T) {
	dir := t.TempDir()
	victim := filepath.Join(t.TempDir(), "victim")
	if err := os.WriteFile(victim, []byte("KEEP"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(victim, filepath.Join(dir, "r.txt")); err != nil {
		t.Fatal(err)
	}
	root, err := os.OpenRoot(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer root.Close()
	if err := writeThroughTemp(root, "r.txt", []byte("report")); err != nil {
		t.Fatal(err)
	}
	if got := readOr(t, victim); got != "KEEP" {
		t.Errorf("the file behind the link holds %q", got)
	}
	fi, err := os.Lstat(filepath.Join(dir, "r.txt"))
	if err != nil || !fi.Mode().IsRegular() || readOr(t, filepath.Join(dir, "r.txt")) != "report" {
		t.Errorf("r.txt: %v %v", fi, err)
	}
	if es, _ := os.ReadDir(dir); len(es) != 1 {
		t.Errorf("temp file left behind: %v", es)
	}
}
