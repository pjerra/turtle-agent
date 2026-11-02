package main

import (
	"context"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/pjerra/turtle-agent/internal/tbdash"
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
	staging = decisionStagingPath(target)
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
	backup := decisionBackupPath(target)
	fsys := faultDecisionFS{failRename: func(o, n string) error {
		if o == staging {
			return errors.New("injected: staging rename")
		}
		if n == target && o == backup {
			return errors.New("injected: restore")
		}
		return nil
	}}
	err := swapDecisionDir(fsys, staging, target)
	if err == nil || !strings.Contains(err.Error(), "injected: restore") {
		t.Fatalf("err %v, want the injected restore failure", err)
	}
	if got := readIndex(t, backup); got != "old" {
		t.Fatalf("backup holds %q, want the old capture", got)
	}
	if !strings.Contains(err.Error(), backup) {
		t.Fatalf("error %q does not name backup %s", err, backup)
	}
	requireOnly(t, parent, "id1.bak-decisions")
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
		if p == decisionBackupPath(target) {
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

// crashCaptureID is a valid capture id (it passes tbdash.DecisionCaptureRe).
const crashCaptureID = "20261029T200000Z-Commander-molten_core"

// writeCaptureDir makes dir holding index.json with body.
func writeCaptureDir(t *testing.T, dir, body string) {
	t.Helper()
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "index.json"), []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
}

// crashFixture is a complete old capture at out/decisions/<id>, with a complete staged new one.
func crashFixture(t *testing.T) (out, parent, target string) {
	t.Helper()
	out = t.TempDir()
	parent = filepath.Join(out, "decisions")
	target = filepath.Join(parent, crashCaptureID)
	writeCaptureDir(t, target, "old")
	writeCaptureDir(t, decisionStagingPath(target), "new")
	return out, parent, target
}

// crashAfterFirstRename runs the swap until the process "dies" on the staging->target rename, after
// the target has been moved to its backup.
func crashAfterFirstRename(t *testing.T, target string) {
	t.Helper()
	crash := faultDecisionFS{failRename: func(_, n string) error {
		if n == target {
			panic("simulated crash")
		}
		return nil
	}}
	defer func() {
		if recover() == nil {
			t.Fatal("swap finished: no simulated crash")
		}
	}()
	_ = swapDecisionDir(crash, decisionStagingPath(target), target)
}

// A crash between the two renames leaves target missing and the backup present. The next pull
// restores the old capture before it does anything else: here the dashboard list fails, so only the
// recovery could have changed the disk.
func TestDecisionCrashAfterFirstRenameRestoredBeforeList(t *testing.T) {
	out, parent, target := crashFixture(t)
	crashAfterFirstRename(t, target)
	if _, err := os.Lstat(target); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("target present after crash: %v", err)
	}
	if got := readIndex(t, decisionBackupPath(target)); got != "old" {
		t.Fatalf("backup holds %q, want the old capture", got)
	}
	dash := &fakeDecisionsDash{listErr: errors.New("dashboard down")}
	res, notes := pullDecisionsInto(context.Background(), dash, "Commander", time.Time{}, time.Time{}, out, 1<<20)
	if !res.ListFailed {
		t.Fatalf("list not reached: %+v", res)
	}
	if len(notes) != 1 || !strings.Contains(notes[0], "dashboard down") {
		t.Fatalf("notes %q, want only the list failure", notes)
	}
	if got := readIndex(t, target); got != "old" {
		t.Fatalf("target not restored before the list: %q", got)
	}
	requireOnly(t, parent, crashCaptureID)
}

// After a crash, a pull that lists the capture restores it and then replaces it with the new pull.
func TestDecisionCrashThenPullReplacesWithNew(t *testing.T) {
	out, parent, target := crashFixture(t)
	crashAfterFirstRename(t, target)
	ended := time.Now()
	dash := &fakeDecisionsDash{
		caps:  []tbdash.DecisionCapture{{ID: crashCaptureID, Commander: "Commander", Ended: &ended, Started: ended.Add(-time.Minute)}},
		files: map[string]string{"index.json": "new"},
	}
	res, notes := pullDecisionsInto(context.Background(), dash, "Commander", time.Time{}, time.Time{}, out, 1<<20)
	if len(notes) != 0 || len(res.Captures) != 1 {
		t.Fatalf("pull: captures %+v notes %q", res.Captures, notes)
	}
	if got := readIndex(t, target); got != "new" {
		t.Fatalf("target holds %q, want the new capture", got)
	}
	requireOnly(t, parent, crashCaptureID, "manifest.json")
}

// Both target and backup present means the swap committed: the stale backup is removed.
func TestDecisionRecoverDropsStaleBackup(t *testing.T) {
	out, parent, target := crashFixture(t)
	if err := os.RemoveAll(decisionStagingPath(target)); err != nil {
		t.Fatal(err)
	}
	writeCaptureDir(t, target, "new")
	writeCaptureDir(t, decisionBackupPath(target), "old")
	failed, notes := recoverDecisionDir(osDecisionFS{}, out)
	if len(failed) != 0 || len(notes) != 0 {
		t.Fatalf("failed %v notes %q", failed, notes)
	}
	if got := readIndex(t, target); got != "new" {
		t.Fatalf("target holds %q, want the committed capture", got)
	}
	requireOnly(t, parent, crashCaptureID)
}

// A failed restore keeps the backup, flags the capture so the pull skips it, and reports the cause.
func TestDecisionRecoverRestoreFailureFlagsCapture(t *testing.T) {
	out, _, target := crashFixture(t)
	if err := os.RemoveAll(target); err != nil {
		t.Fatal(err)
	}
	writeCaptureDir(t, decisionBackupPath(target), "old")
	fsys := faultDecisionFS{failRename: func(_, n string) error {
		if n == target {
			return errors.New("injected: restore")
		}
		return nil
	}}
	failed, notes := recoverDecisionDir(fsys, out)
	if !failed[crashCaptureID] || len(failed) != 1 {
		t.Fatalf("failed %v, want only %s", failed, crashCaptureID)
	}
	if len(notes) != 1 || !strings.Contains(notes[0], "injected: restore") {
		t.Fatalf("notes %q, want the restore failure", notes)
	}
	if got := readIndex(t, decisionBackupPath(target)); got != "old" {
		t.Fatalf("backup holds %q, want the old capture kept", got)
	}
}
