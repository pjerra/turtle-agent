package routeedit

import (
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"testing"
)

func names(t *testing.T, dir string) []string {
	t.Helper()
	ents, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	var out []string
	for _, e := range ents {
		out = append(out, e.Name())
	}
	sort.Strings(out)
	return out
}

func TestSaveWritesOnlyTheServersFileNames(t *testing.T) {
	root := t.TempDir()
	s, err := NewStore(filepath.Join(root, "edits"))
	if err != nil {
		t.Fatal(err)
	}
	m := &Meta{Who: "owner", At: "2026-10-16T01:00:00Z", Status: StatusChecked, Note: "../../../etc/passwd", Base: BaseInfo{Source: "/x/p.json", SHA256: "ab"}}
	v, err := s.Save("zulfarrak", m, []byte("{}\n"), "diff text")
	if err != nil || v != 1 {
		t.Fatalf("%d %v", v, err)
	}
	// a note, a who or a source path can say anything: only the version number and the fixed suffixes make file names
	got := names(t, filepath.Join(s.Root(), "zulfarrak"))
	if strings.Join(got, ",") != "v0001.diff,v0001.json,v0001.meta.json" {
		t.Fatalf("files: %v", got)
	}
	if got := names(t, s.Root()); strings.Join(got, ",") != "zulfarrak" {
		t.Fatalf("root: %v", got)
	}
	// the second edit is the next number; a refused one (no plan) keeps a record and a diff only
	v2, err := s.Save("zulfarrak", &Meta{Who: "owner", Status: StatusRefused}, nil, "d2")
	if err != nil || v2 != 2 {
		t.Fatalf("%d %v", v2, err)
	}
	got = names(t, filepath.Join(s.Root(), "zulfarrak"))
	if strings.Join(got, ",") != "v0001.diff,v0001.json,v0001.meta.json,v0002.diff,v0002.meta.json" {
		t.Fatalf("files: %v", got)
	}
	if _, err := s.PlanFile("zulfarrak", 2); err == nil {
		t.Error("a refused version has no plan file")
	}
	ms, err := s.List("zulfarrak")
	if err != nil || len(ms) != 2 || ms[0].Version != 1 || ms[1].Version != 2 || ms[1].Status != StatusRefused || ms[0].SHA256 == "" || ms[1].SHA256 != "" {
		t.Fatalf("%+v %v", ms, err)
	}
	if l, _ := s.Latest("zulfarrak"); l != 1 {
		t.Errorf("latest checked is %d, want 1 (version 2 is refused)", l)
	}
	if b, err := s.PlanFile("zulfarrak", 1); err != nil || string(b) != "{}\n" {
		t.Errorf("%q %v", b, err)
	}
	if d, err := s.Diff("zulfarrak", 1); err != nil || d != "diff text" {
		t.Errorf("%q %v", d, err)
	}
}

func TestSaveRefusesBadPlanNames(t *testing.T) {
	s, _ := NewStore(t.TempDir())
	for _, bad := range []string{"", "..", "../x", "a/b", "A", ".hidden", "a b", "x\x00", strings.Repeat("a", 41), "a\\b"} {
		if _, err := s.Save(bad, &Meta{}, nil, ""); err == nil {
			t.Errorf("%q must not be a plan name", bad)
		}
		if _, err := s.List(bad); err == nil {
			t.Errorf("%q listed", bad)
		}
	}
	if got := names(t, s.Root()); len(got) != 0 {
		t.Errorf("a refused name left files: %v", got)
	}
}

func TestSaveFollowsNoLinks(t *testing.T) {
	root, outside := t.TempDir(), t.TempDir()
	s, _ := NewStore(root)
	// a plan folder that is a link out of the store
	if err := os.Symlink(outside, filepath.Join(s.Root(), "evil")); err != nil {
		t.Skip(err)
	}
	if _, err := s.Save("evil", &Meta{}, []byte("{}"), ""); err == nil {
		t.Error("saved through a linked plan folder")
	}
	if got := names(t, outside); len(got) != 0 {
		t.Errorf("wrote outside the store: %v", got)
	}
	// a version file that is a link to a file elsewhere is not overwritten
	d := filepath.Join(s.Root(), "good")
	if err := os.Mkdir(d, 0o750); err != nil {
		t.Fatal(err)
	}
	target := filepath.Join(outside, "precious")
	if err := os.WriteFile(target, []byte("keep"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, filepath.Join(d, "v0001.json")); err != nil {
		t.Fatal(err)
	}
	// v0001.meta.json is free, so number 1 is claimed, and the plan file's name is a link: the write must fail and undo what it made
	if _, err := s.Save("good", &Meta{}, []byte("{}"), ""); err == nil {
		t.Error("wrote through a link")
	}
	if b, _ := os.ReadFile(target); string(b) != "keep" {
		t.Errorf("the file behind the link was changed: %q", b)
	}
	if _, err := os.Lstat(filepath.Join(d, "v0001.meta.json")); err == nil {
		t.Error("a failed save left its record behind")
	}
}

func TestVersionsAreNeverOverwrittenAndConcurrentSavesGetOwnNumbers(t *testing.T) {
	s, _ := NewStore(t.TempDir())
	var wg sync.WaitGroup
	got := make(chan int, 20)
	for i := 0; i < 20; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			v, err := s.Save("p", &Meta{Who: "a"}, []byte("{}"), "d")
			if err != nil {
				t.Error(err)
			}
			got <- v
		}()
	}
	wg.Wait()
	close(got)
	seen := map[int]bool{}
	for v := range got {
		if seen[v] {
			t.Errorf("version %d written twice", v)
		}
		seen[v] = true
	}
	if len(seen) != 20 {
		t.Errorf("%d versions", len(seen))
	}
	// a second store on the same folder (another process) counts on from the files that are there
	s2, _ := NewStore(s.Root())
	if v, err := s2.Save("p", &Meta{}, []byte("{}"), "d"); err != nil || v != 21 {
		t.Errorf("%d %v", v, err)
	}
}

func TestParseVersion(t *testing.T) {
	for in, want := range map[string]int{"v0001": 1, "v0042": 42, "v9999": 9999} {
		if v, ok := ParseVersion(in); !ok || v != want || Name(v) != in {
			t.Errorf("%s: %d %v", in, v, ok)
		}
	}
	for _, bad := range []string{"", "v0000", "v1", "v00001", "V0001", "v0001.json", "../v0001", "v000a", "0001"} {
		if _, ok := ParseVersion(bad); ok {
			t.Errorf("%q", bad)
		}
	}
}

func TestListLeavesOutACutOffRecord(t *testing.T) {
	s, _ := NewStore(t.TempDir())
	if _, err := s.Save("p", &Meta{Who: "a", Status: StatusChecked}, []byte("{}"), "d"); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(s.Root(), "p", "v0002.meta.json"), nil, 0o640); err != nil {
		t.Fatal(err)
	}
	ms, err := s.List("p")
	if err != nil || len(ms) != 1 {
		t.Fatalf("%+v %v", ms, err)
	}
	if v, _ := s.Save("p", &Meta{}, nil, ""); v != 3 {
		t.Errorf("the empty record still holds its number: got %d", v)
	}
	if ps, _ := s.Plans(); len(ps) != 1 || ps[0] != "p" {
		t.Errorf("%v", ps)
	}
	if ms, err := s.List("never-edited"); err != nil || len(ms) != 0 {
		t.Errorf("%v %v", ms, err)
	}
}

// A plan folder swapped for a link after the store is in use gets no write: the folder is opened without following links at the moment of use.
func TestAPlanFolderSwappedForALinkGetsNoWrite(t *testing.T) {
	s, _ := NewStore(t.TempDir())
	outside := t.TempDir()
	if _, err := s.Save("p", &Meta{Status: StatusChecked}, []byte("{}"), "d"); err != nil {
		t.Fatal(err)
	}
	moved := filepath.Join(s.Root(), "p.moved")
	if err := os.Rename(filepath.Join(s.Root(), "p"), moved); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(s.Root(), "p")); err != nil {
		t.Skip(err)
	}
	if _, err := s.Save("p", &Meta{}, []byte("{}"), "d"); err == nil {
		t.Error("saved through the swapped folder")
	}
	if _, err := s.List("p"); err == nil {
		t.Error("listed through the swapped folder")
	}
	if _, err := s.Meta("p", 1); err == nil {
		t.Error("read through the swapped folder")
	}
	if got := names(t, outside); len(got) != 0 {
		t.Errorf("wrote outside the store: %v", got)
	}
	if got := names(t, moved); len(got) != 3 {
		t.Errorf("the moved folder was written to: %v", got)
	}
	// the same for a version file swapped for a link to a file elsewhere
	target := filepath.Join(outside, "t")
	os.WriteFile(target, []byte("keep"), 0o600)
	d2 := filepath.Join(s.Root(), "q")
	s.Save("q", &Meta{}, []byte("{}"), "d")
	os.Remove(filepath.Join(d2, "v0001.diff"))
	os.Symlink(target, filepath.Join(d2, "v0001.diff"))
	if _, err := s.Diff("q", 1); err == nil {
		t.Error("read a link as a version file")
	}
}

// The edits folder itself replaced by a link after the store opened it: the store goes on writing into the folder it opened, not through the link.
func TestTheRootSwappedForALinkIsNotFollowed(t *testing.T) {
	base := t.TempDir()
	root := filepath.Join(base, "edits")
	s, err := NewStore(root)
	if err != nil {
		t.Fatal(err)
	}
	outside := t.TempDir()
	if err := os.Rename(root, filepath.Join(base, "edits.moved")); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, root); err != nil {
		t.Skip(err)
	}
	if _, err := s.Save("p", &Meta{}, []byte("{}"), "d"); err != nil {
		t.Fatal(err)
	}
	if got := names(t, outside); len(got) != 0 {
		t.Errorf("wrote through the link: %v", got)
	}
	if got := names(t, filepath.Join(base, "edits.moved", "p")); len(got) != 3 {
		t.Errorf("not in the folder it opened: %v", got)
	}
}

func TestLatestSkipsRefusedAndUnchecked(t *testing.T) {
	s, _ := NewStore(t.TempDir())
	s.Save("p", &Meta{Status: StatusChecked}, []byte("{}"), "d")
	s.Save("p", &Meta{Status: StatusUnchecked}, []byte("{}"), "d")
	s.Save("p", &Meta{Status: StatusRefused}, nil, "d")
	if v, _ := s.Latest("p"); v != 1 {
		t.Errorf("latest %d, want 1: an unchecked or refused version is never the newest to use", v)
	}
	s.Save("p", &Meta{Status: StatusAccepted}, []byte("{}"), "d")
	if v, _ := s.Latest("p"); v != 4 {
		t.Errorf("latest %d, want the accepted one", v)
	}
}
