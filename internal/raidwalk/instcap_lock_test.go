//go:build unix

package raidwalk

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

// A holder that keeps the ledger lock past the old 10 s stale threshold still excludes a second
// writer. The lock file is backdated 30 s, an age the old code took as stale and unlinked, so the
// second writer must wait until the holder lets go.
func TestUpdateCapLedgerHolderPastStaleAgeStillExcludes(t *testing.T) {
	path := filepath.Join(t.TempDir(), InstanceCapFile)
	inside := make(chan struct{})
	release := make(chan struct{})
	holderDone := make(chan error, 1)
	go func() {
		_, err := UpdateCapLedger(path, func(*CapLedger) {
			close(inside)
			<-release
		})
		holderDone <- err
	}()
	<-inside
	old := time.Now().Add(-30 * time.Second)
	if err := os.Chtimes(path+".lock", old, old); err != nil {
		t.Fatal(err)
	}
	secondRan := make(chan struct{}, 1)
	secondDone := make(chan error, 1)
	go func() {
		_, err := UpdateCapLedger(path, func(*CapLedger) { secondRan <- struct{}{} })
		secondDone <- err
	}()
	select {
	case err := <-secondDone:
		t.Fatalf("second writer got in while the holder kept the lock (err %v)", err)
	case <-time.After(time.Second):
	}
	close(release)
	if err := <-holderDone; err != nil {
		t.Fatalf("holder: %v", err)
	}
	if err := <-secondDone; err != nil {
		t.Fatalf("second writer after the holder released: %v", err)
	}
	select {
	case <-secondRan:
	default:
		t.Fatal("second writer returned without running its update")
	}
}
