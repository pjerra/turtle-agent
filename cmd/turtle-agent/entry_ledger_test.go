package main

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestEntryLedgerKeepsAnHourInAFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "instance-entries-abc.json")
	now := time.Unix(2_000_000, 0)
	l := &entryLedger{path: path}
	for _, ago := range []time.Duration{90 * time.Minute, 50 * time.Minute, 20 * time.Minute} {
		if err := l.Record(now.Add(-ago)); err != nil {
			t.Fatal(err)
		}
	}
	// The entry older than the hour is dropped when the next one is recorded.
	if err := l.Record(now); err != nil {
		t.Fatal(err)
	}
	if got := l.Recent(now); len(got) != 3 || !got[0].Equal(now.Add(-50*time.Minute)) {
		t.Fatalf("%v", got)
	}
	// A new process reads the file back; entries age out as time passes.
	l2 := &entryLedger{path: path}
	if got := l2.Recent(now); len(got) != 3 {
		t.Fatalf("%v", got)
	}
	if got := l2.Recent(now.Add(45 * time.Minute)); len(got) != 1 {
		t.Fatalf("%v", got)
	}
	info, err := os.Stat(path)
	if err != nil || info.Mode().Perm()&0o077 != 0 {
		t.Fatalf("%v %v", info, err)
	}
}

func TestEntryLedgerInMemoryAndUnreadableFile(t *testing.T) {
	l := &entryLedger{}
	now := time.Now()
	if err := l.Record(now); err != nil || len(l.Recent(now)) != 1 {
		t.Fatal("in-memory ledger")
	}
	bad := filepath.Join(t.TempDir(), "x.json")
	if err := os.WriteFile(bad, []byte("not json"), 0o600); err != nil {
		t.Fatal(err)
	}
	if got := (&entryLedger{path: bad}).Recent(now); len(got) != 0 {
		t.Fatalf("%v", got)
	}
}
