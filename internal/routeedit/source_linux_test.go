//go:build linux

package routeedit

import (
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
)

func TestSourceReadsOnlyRegularFilesOfItsOwn(t *testing.T) {
	dir, outside := t.TempDir(), t.TempDir()
	must := func(err error) {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
	}
	must(os.WriteFile(filepath.Join(dir, "good.json"), []byte(`{}`), 0o644))
	must(os.WriteFile(filepath.Join(outside, "x.json"), []byte(`{}`), 0o644))
	must(os.Symlink(filepath.Join(outside, "x.json"), filepath.Join(dir, "link.json")))
	must(os.Mkdir(filepath.Join(dir, "dir.json"), 0o755))
	must(syscall.Mkfifo(filepath.Join(dir, "fifo.json"), 0o644))
	must(os.WriteFile(filepath.Join(dir, "big.json"), []byte(strings.Repeat(" ", 100)), 0o644))
	s, err := OpenSource(dir)
	must(err)
	defer s.Close()
	if b, err := s.ReadPlan("good", 64); err != nil || string(b) != `{}` {
		t.Errorf("good: %q %v", b, err)
	}
	for _, p := range []string{"link", "dir", "fifo", "big", "missing", "../x"} {
		if _, err := s.ReadPlan(p, 64); err == nil {
			t.Errorf("%s: read", p)
		}
	}
	// the folder held is read even after its path names another one, and Verify says so
	must(os.Rename(dir, dir+".moved"))
	must(os.Symlink(outside, dir))
	if _, err := s.ReadPlan("x", 64); err == nil {
		t.Error("read through the new path")
	}
	if err := s.Verify(); err == nil {
		t.Error("Verify missed the swap")
	}
	if _, err := OpenSource(filepath.Join(outside, "x.json")); err == nil {
		t.Error("a file opened as the source")
	}
}

func TestCheckHeldKeepsEditsOutOfASlotsPlanFolder(t *testing.T) {
	root := t.TempDir()
	slot := filepath.Join(root, "ta-r4-s1", "dungeons")
	edits := filepath.Join(root, "edits")
	src := filepath.Join(root, "turtle-agent", "dungeons")
	for _, d := range []string{slot, src} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	open := func(e, s string) (*Store, *Source) {
		st, err := NewStore(e)
		if err != nil {
			t.Fatal(err)
		}
		so, err := OpenSource(s)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { st.Close(); so.Close() })
		return st, so
	}
	st, so := open(edits, src)
	if err := CheckHeld(st, so, []string{slot, "", filepath.Join(root, "not", "made")}); err != nil {
		t.Fatalf("apart: %v", err)
	}
	link := filepath.Join(root, "innocent")
	if err := os.Symlink(slot, link); err != nil {
		t.Fatal(err)
	}
	bad := map[string]struct {
		edits, src string
		slots      []string
	}{
		"edits is a slot's folder":        {slot, src, []string{slot}},
		"edits inside a slot's folder":    {filepath.Join(slot, "versions"), src, []string{slot}},
		"slot folder inside the edits":    {edits, src, []string{filepath.Join(edits, "p")}},
		"slot not made, inside the edits": {edits, src, []string{filepath.Join(edits, "a", "b")}},
		"slot folder holds the edits":     {edits, src, []string{root}},
		"source is a slot's folder":       {edits, slot, []string{slot}},
		"source inside a slot's folder":   {edits, src, []string{filepath.Dir(src)}},
		"edits behind a link":             {link, src, []string{slot}},
		"slot named by a link":            {slot, src, []string{link}},
	}
	for name, c := range bad {
		st, so := open(c.edits, c.src)
		if err := CheckHeld(st, so, c.slots); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
	// a held folder moved into a slot's plan folder after it was opened is refused (its path no longer names it)
	st, so = open(edits, src)
	if err := os.Rename(src, filepath.Join(slot, "moved")); err != nil {
		t.Fatal(err)
	}
	if err := CheckHeld(st, so, []string{slot}); err == nil {
		t.Error("source moved into a slot's folder: accepted")
	}
}
