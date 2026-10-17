package routeedit

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// realPath is path made absolute with the symlinks of its longest existing part resolved, so that a folder that is not made yet is
// still compared by where it would be.
func realPath(path string) string {
	abs, err := filepath.Abs(path)
	if err != nil {
		return filepath.Clean(path)
	}
	rest := ""
	for p := abs; ; p = filepath.Dir(p) {
		if r, err := filepath.EvalSymlinks(p); err == nil {
			return filepath.Join(r, rest)
		}
		rest = filepath.Join(filepath.Base(p), rest)
		if filepath.Dir(p) == p {
			return abs
		}
	}
}

// within reports whether a is b or lies under b.
func within(a, b string) bool {
	if a == b {
		return true
	}
	return strings.HasPrefix(a, strings.TrimSuffix(b, string(filepath.Separator))+string(filepath.Separator))
}

// CheckFolders refuses an editor whose folders touch a slot's plan folder. The commander run reloads its plan file at every leg and pull
// group (live_reload), so anything written inside a running slot's dungeons_dir is taken by that run unchecked: the edits folder must not
// be, contain or lie inside any slot's dungeons_dir, and the folder the edits start from (source) must not lie inside one.
func CheckFolders(editDir, source string, slotDirs []string) error {
	ed, src := realPath(editDir), realPath(source)
	for _, s := range slotDirs {
		if s == "" {
			continue
		}
		sd := realPath(s)
		switch {
		case within(ed, sd):
			return fmt.Errorf("the edits folder %s lies inside the plan folder %s of a slot: a running commander would take what is written there", ed, sd)
		case within(sd, ed):
			return fmt.Errorf("the plan folder %s of a slot lies inside the edits folder %s", sd, ed)
		case within(src, sd):
			return fmt.Errorf("the editable source %s lies inside the plan folder %s of a slot", src, sd)
		}
	}
	return nil
}

// ensureDir is for tests and the command: a folder must exist.
func ensureDir(p string) error {
	st, err := os.Stat(p)
	if err != nil {
		return err
	}
	if !st.IsDir() {
		return fmt.Errorf("%s is not a folder", p)
	}
	return nil
}

// CheckSource says the folder the edits start from exists.
func CheckSource(source string) error { return ensureDir(source) }

// Overlaps reports whether two folders are the same, or one lies inside the other (symlinks resolved).
func Overlaps(a, b string) bool {
	ra, rb := realPath(a), realPath(b)
	return within(ra, rb) || within(rb, ra)
}
