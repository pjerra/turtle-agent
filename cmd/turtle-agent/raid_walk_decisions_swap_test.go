package main

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// faultDecisionFS is the real filesystem with optional injected failures per call.
type faultDecisionFS struct {
	failRename func(oldpath, newpath string) error
	failRemove func(path string) error
}

func (f faultDecisionFS) Rename(oldpath, newpath string) error {
	if f.failRename != nil {
		if err := f.failRename(oldpath, newpath); err != nil {
			return err
		}
	}
	return os.Rename(oldpath, newpath)
}

func (f faultDecisionFS) RemoveAll(path string) error {
	if f.failRemove != nil {
		if err := f.failRemove(path); err != nil {
			return err
		}
	}
	return os.RemoveAll(path)
}

func (faultDecisionFS) Lstat(path string) (fs.FileInfo, error) { return os.Lstat(path) }

// swapFixture makes <parent>/decisions/<id> holding "old" and a sibling staging dir holding "new".
func swapFixture(t *testing.T) (parent, staging, target string) {
	t.Helper()
	parent = filepath.Join(t.TempDir(), "decisions")
	target = filepath.Join(parent, "id1")
	staging = filepath.Join(parent, ".id1.staging-1")
	for dir, body := range map[string]string{target: "old", staging: "new"} {
		if err := os.MkdirAll(dir, 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, "index.json"), []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	return parent, staging, target
}

func readIndex(t *testing.T, dir string) string {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join(dir, "index.json"))
	if err != nil {
		t.Fatalf("read %s: %v", dir, err)
	}
	return string(raw)
}

func entryNames(t *testing.T, parent string) []string {
	t.Helper()
	ents, err := os.ReadDir(parent)
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, e := range ents {
		names = append(names, e.Name())
	}
	return names
}

func requireOnly(t *testing.T, parent string, want ...string) {
	t.Helper()
	got := entryNames(t, parent)
	if len(got) != len(want) {
		t.Fatalf("entries %q, want %q", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("entries %q, want %q", got, want)
		}
	}
}

// Old target kept and staging removed when moving the old capture aside fails.
func TestDecisionSwapBackupRenameFailsKeepsOld(t *testing.T) {
	parent, staging, target := swapFixture(t)
	fsys := faultDecisionFS{failRename: func(o, _ string) error {
		if o == target {
			return errors.New("injected: target rename")
		}
		return nil
	}}
	err := swapDecisionDir(fsys, staging, target)
	if err == nil || !strings.Contains(err.Error(), "injected: target rename") {
		t.Fatalf("err %v, want the injected target rename failure", err)
	}
	if errors.Is(err, errDecisionCommitted) {
		t.Fatalf("failure before commit reported as committed: %v", err)
	}
	if got := readIndex(t, target); got != "old" {
		t.Fatalf("old capture changed: %q", got)
	}
	requireOnly(t, parent, "id1")
}

// Old capture restored from its backup when staging cannot be renamed into place; staging removed.
func TestDecisionSwapStagingRenameFailsRestoresOld(t *testing.T) {
	parent, staging, target := swapFixture(t)
	fsys := faultDecisionFS{failRename: func(o, _ string) error {
		if o == staging {
			return errors.New("injected: staging rename")
		}
		return nil
	}}
	err := swapDecisionDir(fsys, staging, target)
	if err == nil || !strings.Contains(err.Error(), "injected: staging rename") {
		t.Fatalf("err %v, want the injected staging rename failure", err)
	}
	if got := readIndex(t, target); got != "old" {
		t.Fatalf("old capture not restored: %q", got)
	}
	requireOnly(t, parent, "id1")
}

// When the restore fails too, the error names the backup holding the old capture and that backup
// is left in place (nothing is deleted).
func TestDecisionSwapRestoreFailsKeepsBackup(t *testing.T) {
	parent, staging, target := swapFixture(t)
	fsys := faultDecisionFS{failRename: func(o, n string) error {
		if o == staging {
			return errors.New("injected: staging rename")
		}
		if n == target && strings.Contains(filepath.Base(o), ".id1.backup-") {
			return errors.New("injected: restore")
		}
		return nil
	}}
	err := swapDecisionDir(fsys, staging, target)
	if err == nil || !strings.Contains(err.Error(), "injected: restore") {
		t.Fatalf("err %v, want the injected restore failure", err)
	}
	var backup string
	for _, name := range entryNames(t, parent) {
		if strings.HasPrefix(name, ".id1.backup-") {
			backup = filepath.Join(parent, name)
		}
	}
	if backup == "" {
		t.Fatalf("backup removed although restore failed: %q", entryNames(t, parent))
	}
	if !strings.Contains(err.Error(), backup) {
		t.Fatalf("error %q does not name backup %s", err, backup)
	}
	if got := readIndex(t, backup); got != "old" {
		t.Fatalf("backup holds %q, want the old capture", got)
	}
}

// A failed staging removal on the failure path is returned, not swallowed.
func TestDecisionSwapStagingRemoveFailureIsReturned(t *testing.T) {
	_, staging, target := swapFixture(t)
	fsys := faultDecisionFS{
		failRename: func(o, _ string) error {
			if o == target {
				return errors.New("injected: target rename")
			}
			return nil
		},
		failRemove: func(p string) error {
			if p == staging {
				return errors.New("injected: remove staging")
			}
			return nil
		},
	}
	err := swapDecisionDir(fsys, staging, target)
	if err == nil || !strings.Contains(err.Error(), "injected: remove staging") ||
		!strings.Contains(err.Error(), "injected: target rename") {
		t.Fatalf("err %v, want both the rename and the staging removal failures", err)
	}
}

// Success: the new capture is in place and the backup is gone.
func TestDecisionSwapSuccessRemovesBackup(t *testing.T) {
	parent, staging, target := swapFixture(t)
	if err := swapDecisionDir(faultDecisionFS{}, staging, target); err != nil {
		t.Fatalf("swap: %v", err)
	}
	if got := readIndex(t, target); got != "new" {
		t.Fatalf("target holds %q, want the new capture", got)
	}
	requireOnly(t, parent, "id1")
}

// No earlier capture: the staging dir just becomes the target.
func TestDecisionSwapFreshTarget(t *testing.T) {
	parent, staging, target := swapFixture(t)
	if err := os.RemoveAll(target); err != nil {
		t.Fatal(err)
	}
	if err := swapDecisionDir(faultDecisionFS{}, staging, target); err != nil {
		t.Fatalf("swap: %v", err)
	}
	if got := readIndex(t, target); got != "new" {
		t.Fatalf("target holds %q, want the new capture", got)
	}
	requireOnly(t, parent, "id1")
}

// The new capture is committed; a failed backup removal is returned as committed and the new
// capture stays.
func TestDecisionSwapBackupRemoveFailureIsCommitted(t *testing.T) {
	parent, staging, target := swapFixture(t)
	fsys := faultDecisionFS{failRemove: func(p string) error {
		if strings.HasPrefix(filepath.Base(p), ".id1.backup-") {
			return errors.New("injected: remove backup")
		}
		return nil
	}}
	err := swapDecisionDir(fsys, staging, target)
	if !errors.Is(err, errDecisionCommitted) {
		t.Fatalf("err %v, want errDecisionCommitted", err)
	}
	if !strings.Contains(err.Error(), "injected: remove backup") {
		t.Fatalf("err %v does not carry the removal failure", err)
	}
	if got := readIndex(t, target); got != "new" {
		t.Fatalf("target holds %q, want the new capture", got)
	}
	if len(entryNames(t, parent)) != 2 {
		t.Fatalf("want target plus the leftover backup, got %q", entryNames(t, parent))
	}
}
