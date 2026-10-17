package routeedit

import (
	"os"
	"path/filepath"
	"testing"
)

func TestCheckFoldersKeepsEditsOutOfASlotsPlanFolder(t *testing.T) {
	root := t.TempDir()
	slot := filepath.Join(root, "ta-r4-s1", "dungeons")
	other := filepath.Join(root, "ta-r4-s4", "dungeons")
	edits := filepath.Join(root, "turtle-agent-route-edits")
	src := filepath.Join(root, "turtle-agent", "dungeons")
	for _, d := range []string{slot, other, edits, src} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	if err := CheckFolders(edits, src, []string{slot, other, ""}); err != nil {
		t.Fatalf("apart: %v", err)
	}
	if err := CheckFolders(edits, src, nil); err != nil {
		t.Fatalf("no slots: %v", err)
	}
	bad := map[string]struct{ edits, src string }{
		"edits is a slot's folder":      {slot, src},
		"edits inside a slot's folder":  {filepath.Join(slot, "versions"), src},
		"edits not made yet, inside":    {filepath.Join(slot, "a", "b", "c"), src},
		"slot folder inside the edits":  {root, src},
		"source is a slot's folder":     {edits, slot},
		"source inside a slot's folder": {edits, filepath.Join(slot, "golden")},
		"dot-dot spelling":              {filepath.Join(edits, "..", "ta-r4-s1", "dungeons", "x"), src},
	}
	for name, c := range bad {
		if err := CheckFolders(c.edits, c.src, []string{slot}); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
	// a link does not hide the folder
	link := filepath.Join(root, "innocent")
	if err := os.Symlink(slot, link); err == nil {
		if err := CheckFolders(link, src, []string{slot}); err == nil {
			t.Error("edits behind a link to a slot's folder: accepted")
		}
		if err := CheckFolders(edits, link, []string{slot}); err == nil {
			t.Error("source behind a link to a slot's folder: accepted")
		}
		if err := CheckFolders(edits, src, []string{link}); err != nil {
			t.Errorf("a slot folder named by a link to somewhere else is no conflict: %v", err)
		}
	}
	// a name that only starts the same is not inside
	if err := CheckFolders(slot+"-edits", src, []string{slot}); err != nil {
		t.Errorf("sibling with a longer name: %v", err)
	}
}
