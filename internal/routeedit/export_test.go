package routeedit

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/pjerra/turtle-agent/internal/dungeon"
)

func hexsum(b []byte) string { s := sha256.Sum256(b); return hex.EncodeToString(s[:]) }

// editedVersion saves an edit of basePlan (via 0 moved) as the next version of edit1, with the given status.
func editedVersion(t *testing.T, s *Store, status string) (v int, baseBytes, editBytes []byte) {
	t.Helper()
	base := load(t)
	baseBytes, _ = dungeon.Marshal(base)
	edited, ch, err := Apply(base, []Op{{Leg: 0, Kind: KindMove, Index: 0, X: 21, Y: 1}}, flat)
	if err != nil {
		t.Fatal(err)
	}
	editBytes, _ = dungeon.Marshal(edited)
	m := &Meta{Who: "owner", At: "2026-10-16T01:00:00Z", Status: status, Note: "n", Mmaps: "/mm", Base: BaseInfo{Source: "/src/edit1.json", SHA256: hexsum(baseBytes)},
		Check: CheckResult{Checked: true, OK: status != StatusRefused, Points: 9}, Changes: ch, Ops: []Op{{Leg: 0, Kind: KindMove, Index: 0, X: 21, Y: 1}}}
	var pj []byte
	if status != StatusRefused {
		pj = editBytes
	}
	v, err = s.Save("edit1", m, pj, "diff")
	if err != nil {
		t.Fatal(err)
	}
	return v, baseBytes, editBytes
}

func TestPickAndExportTheNewestCheckedVersion(t *testing.T) {
	s, _ := NewStore(t.TempDir())
	v1, _, edit1 := editedVersion(t, s, StatusChecked)
	editedVersion(t, s, StatusRefused) // v2: newer, but refused
	m, data, err := Pick(s, "edit1", 0)
	if err != nil || m.Version != v1 || string(data) != string(edit1) {
		t.Fatalf("latest: %v %+v", err, m)
	}
	if _, _, err := Pick(s, "edit1", 2); err == nil || !strings.Contains(err.Error(), "refused") {
		t.Errorf("a refused version: %v", err)
	}
	if _, _, err := Pick(s, "edit1", 9); err == nil {
		t.Error("no such version")
	}
	if _, _, err := Pick(s, "nope", 0); err == nil {
		t.Error("a plan with no versions")
	}
	if _, _, err := Pick(s, "../x", 0); err == nil {
		t.Error("a path")
	}
	dir := filepath.Join(t.TempDir(), "fresh", "dungeons")
	ex, err := Export(s, "edit1", 0, dir, nil)
	if err != nil {
		t.Fatal(err)
	}
	got, _ := os.ReadFile(filepath.Join(dir, "edit1.json"))
	if string(got) != string(edit1) || ex.Tag() != "edit1@v0001 sha256="+hexsum(edit1) || ex.Path != filepath.Join(dir, "edit1.json") {
		t.Errorf("%+v", ex)
	}
	if ents, _ := os.ReadDir(dir); len(ents) != 1 {
		t.Errorf("only the plan is written: %v", ents)
	}
	// the folder is used up: a second export into it, or into any folder with something in it, is refused
	if _, err := Export(s, "edit1", 0, dir, nil); err == nil || !strings.Contains(err.Error(), "not empty") {
		t.Errorf("second export: %v", err)
	}
	// an empty existing folder is fresh
	empty := t.TempDir()
	if _, err := Export(s, "edit1", 0, empty, nil); err != nil {
		t.Errorf("empty folder: %v", err)
	}
	// the guard refuses before anything is made
	nope := filepath.Join(t.TempDir(), "guarded")
	if _, err := Export(s, "edit1", 0, nope, func(string) error { return errors.New("a run reads it") }); err == nil {
		t.Error("the guard was ignored")
	}
	if _, err := os.Stat(nope); err == nil {
		t.Error("a refused export made the folder")
	}
	// a file is not a folder
	f := filepath.Join(t.TempDir(), "f")
	os.WriteFile(f, []byte("x"), 0o644)
	if _, err := Export(s, "edit1", 0, f, nil); err == nil {
		t.Error("exported over a file")
	}
}

func TestExportRefusesAVersionThatWasChangedAfterItWasWritten(t *testing.T) {
	s, _ := NewStore(t.TempDir())
	v, _, _ := editedVersion(t, s, StatusChecked)
	if err := os.WriteFile(filepath.Join(s.Root(), "edit1", Name(v)+".json"), []byte(basePlan), 0o640); err != nil {
		t.Fatal(err)
	}
	if _, err := Export(s, "edit1", v, t.TempDir(), nil); err == nil || !strings.Contains(err.Error(), "changed after it was written") {
		t.Errorf("%v", err)
	}
	// and one that is not a plan
	v2, _, _ := editedVersion(t, s, StatusChecked)
	junk := []byte("{}")
	os.WriteFile(filepath.Join(s.Root(), "edit1", Name(v2)+".json"), junk, 0o640)
	m, _ := s.Meta("edit1", v2)
	m.SHA256 = hexsum(junk)
	if _, _, err := Pick(s, "edit1", v2); err == nil {
		t.Error("a file that does not match its record")
	}
}

// worktree makes a fake TA worktree with dungeons/edit1.json holding the given text.
func worktree(t *testing.T, plan string) string {
	t.Helper()
	wt := t.TempDir()
	os.Mkdir(filepath.Join(wt, "dungeons"), 0o755)
	os.WriteFile(filepath.Join(wt, ".git"), []byte("gitdir: x"), 0o644)
	os.WriteFile(filepath.Join(wt, "dungeons", "edit1.json"), []byte(plan), 0o644)
	return wt
}

func TestPromoteWritesTheCanonicalFileOnItsBaseOnly(t *testing.T) {
	s, _ := NewStore(t.TempDir())
	v, baseBytes, edit := editedVersion(t, s, StatusChecked)
	wt := worktree(t, string(baseBytes))
	pr, err := Promote(s, "edit1", v, wt)
	if err != nil {
		t.Fatal(err)
	}
	got, _ := os.ReadFile(filepath.Join(wt, "dungeons", "edit1.json"))
	if string(got) != string(edit) {
		t.Error("not the version's bytes")
	}
	if ents, _ := os.ReadDir(filepath.Join(wt, "dungeons")); len(ents) != 1 {
		t.Errorf("a temp file was left: %v", ents)
	}
	for _, want := range []string{"dungeons: edit1 route edit v0001 (owner, 2026-10-16T01:00:00Z)", "leg 1 (one): via 0 moved", "checked with the plan check on the navmesh in /mm", "Note: n", hexsum(baseBytes), hexsum(edit)} {
		if !strings.Contains(pr.Message, want) {
			t.Errorf("the commit message lacks %q:\n%s", want, pr.Message)
		}
	}
	// the plan moved on in the worktree: refused, nothing written
	moved := worktree(t, strings.Replace(string(baseBytes), `"x": 20,`, `"x": 22,`, 1))
	if _, err := Promote(s, "edit1", v, moved); err == nil || !strings.Contains(err.Error(), "not the file this edit started from") {
		t.Errorf("another base: %v", err)
	}
	if b, _ := os.ReadFile(filepath.Join(moved, "dungeons", "edit1.json")); strings.Contains(string(b), `"x": 21`) {
		t.Error("written over another base")
	}
	// not a worktree, no dungeons folder, no plan, a link, a refused version, no version
	if _, err := Promote(s, "edit1", v, t.TempDir()); err == nil {
		t.Error("not a worktree")
	}
	nod := t.TempDir()
	os.WriteFile(filepath.Join(nod, ".git"), nil, 0o644)
	if _, err := Promote(s, "edit1", v, nod); err == nil {
		t.Error("no dungeons folder")
	}
	nofile := worktree(t, "")
	os.Remove(filepath.Join(nofile, "dungeons", "edit1.json"))
	if _, err := Promote(s, "edit1", v, nofile); err == nil {
		t.Error("no plan file")
	}
	linked := worktree(t, "")
	os.Remove(filepath.Join(linked, "dungeons", "edit1.json"))
	outside := filepath.Join(t.TempDir(), "o.json")
	os.WriteFile(outside, baseBytes, 0o644)
	if os.Symlink(outside, filepath.Join(linked, "dungeons", "edit1.json")) == nil {
		if _, err := Promote(s, "edit1", v, linked); err == nil {
			t.Error("promoted over a link")
		}
		if b, _ := os.ReadFile(outside); string(b) != string(baseBytes) {
			t.Error("wrote through the link")
		}
	}
	vr, _, _ := editedVersion(t, s, StatusRefused)
	if _, err := Promote(s, "edit1", vr, worktree(t, string(baseBytes))); err == nil {
		t.Error("promoted a refused version")
	}
	if _, err := Promote(s, "edit1", 0, worktree(t, string(baseBytes))); err == nil {
		t.Error("promote without a version")
	}
}

func TestOpenStoreDoesNotMakeAFolder(t *testing.T) {
	missing := filepath.Join(t.TempDir(), "none")
	if _, err := OpenStore(missing); err == nil {
		t.Error("opened a folder that is not there")
	}
	if _, err := os.Stat(missing); err == nil {
		t.Error("OpenStore made it")
	}
	f := filepath.Join(t.TempDir(), "f")
	os.WriteFile(f, nil, 0o644)
	if _, err := OpenStore(f); err == nil {
		t.Error("a file")
	}
}

func TestOverlaps(t *testing.T) {
	a := t.TempDir()
	b := filepath.Join(a, "b")
	os.Mkdir(b, 0o755)
	if !Overlaps(a, b) || !Overlaps(b, a) || !Overlaps(a, a) || Overlaps(b, filepath.Join(a, "bb")) || Overlaps(a, t.TempDir()) {
		t.Error("overlaps")
	}
}
