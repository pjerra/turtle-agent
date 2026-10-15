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
