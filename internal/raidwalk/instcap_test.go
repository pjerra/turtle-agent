package raidwalk

import (
	"path/filepath"
	"testing"
	"time"
)

var capT0 = time.Date(2026, 11, 3, 12, 0, 0, 0, time.UTC)

func capLedgerWith(server string, ages ...time.Duration) *CapLedger {
	l := &CapLedger{}
	for _, a := range ages {
		l.Record(server, CapEntry{At: capT0.Add(-a), Plan: "zg"}, capT0)
	}
	return l
}

func TestCapCountsPerServerInTheLastHour(t *testing.T) {
	l := capLedgerWith("m910q", 5*time.Minute, 20*time.Minute, 59*time.Minute)
	l.Record("tortoise-test2", CapEntry{At: capT0}, capT0)
	if n := l.Count("m910q", capT0); n != 3 {
		t.Fatalf("m910q count = %d, want 3", n)
	}
	if n := l.Count("tortoise-test2", capT0); n != 1 {
		t.Fatalf("test2 count = %d, want 1", n)
	}
	if n := l.Count("nowhere", capT0); n != 0 {
		t.Fatalf("unknown server count = %d", n)
	}
}

func TestCapWindowAges(t *testing.T) {
	l := capLedgerWith("m910q", 59*time.Minute, 30*time.Minute, 10*time.Minute, 1*time.Minute)
	if n := l.Count("m910q", capT0); n != 4 {
		t.Fatalf("count = %d, want 4", n)
	}
	// The oldest entry (59 min old) leaves the window in 1 minute.
	if w := l.WaitFor("m910q", capT0); w != time.Minute {
		t.Fatalf("wait = %s, want 1m", w)
	}
	if n := l.Count("m910q", capT0.Add(61*time.Second)); n != 3 {
		t.Fatalf("count after the oldest aged out = %d, want 3", n)
	}
	if w := l.WaitFor("m910q", capT0.Add(61*time.Second)); w != 0 {
		t.Fatalf("wait below the threshold = %s, want 0", w)
	}
	// At exactly 60 minutes the entry is out.
	if n := l.Count("m910q", capT0.Add(time.Minute)); n != 3 {
		t.Fatalf("count at the 60 min mark = %d, want 3", n)
	}
}

func TestCapWaitAtFiveWaitsForTheSecondOldest(t *testing.T) {
	l := capLedgerWith("m910q", 50*time.Minute, 40*time.Minute, 30*time.Minute, 20*time.Minute, 10*time.Minute)
	// Dropping to 3 needs the second entry out, at +20 min, not the oldest at +10.
	if w := l.WaitFor("m910q", capT0); w != 20*time.Minute {
		t.Fatalf("wait = %s, want 20m", w)
	}
}

func TestCapRecordPrunesOldEntries(t *testing.T) {
	l := capLedgerWith("m910q", 90*time.Minute, 5*time.Minute)
	l.Record("m910q", CapEntry{At: capT0}, capT0)
	if got := len(l.Servers["m910q"].Entries); got != 2 {
		t.Fatalf("entries kept = %d, want 2", got)
	}
}

func TestCapServerRestartResets(t *testing.T) {
	l := capLedgerWith("m910q", 50*time.Minute, 40*time.Minute, 5*time.Minute, 2*time.Minute)
	start := capT0.Add(-10 * time.Minute) // the worldserver started 10 minutes ago
	if !l.NoteServerStart("m910q", start) {
		t.Fatal("NoteServerStart did not report the restart")
	}
	if n := l.Count("m910q", capT0); n != 2 {
		t.Fatalf("count after the restart = %d, want 2 (only the entries since the start)", n)
	}
	// The same start time again changes nothing.
	if l.NoteServerStart("m910q", start) {
		t.Fatal("same start time reported as a restart")
	}
	// A later start (another restart) drops everything before it.
	if !l.NoteServerStart("m910q", capT0) {
		t.Fatal("a later start was not a restart")
	}
	if n := l.Count("m910q", capT0); n != 0 {
		t.Fatalf("count after the second restart = %d, want 0", n)
	}
	// A zero start (no read) leaves the ledger alone.
	l.Record("m910q", CapEntry{At: capT0}, capT0)
	if l.NoteServerStart("m910q", time.Time{}) || l.Count("m910q", capT0) != 1 {
		t.Fatal("a zero start time changed the ledger")
	}
}

func TestCapReset(t *testing.T) {
	l := capLedgerWith("m910q", time.Minute, 2*time.Minute)
	l.Record("tortoise-test2", CapEntry{At: capT0}, capT0)
	l.Reset("m910q")
	if l.Count("m910q", capT0) != 0 || l.Count("tortoise-test2", capT0) != 1 {
		t.Fatal("Reset must clear that server only")
	}
}

func TestCapCountForAccounts(t *testing.T) {
	l := &CapLedger{}
	l.Record("s", CapEntry{At: capT0.Add(-3 * time.Minute), Accounts: []string{"a1", "a2"}}, capT0)
	l.Record("s", CapEntry{At: capT0.Add(-2 * time.Minute), Accounts: []string{"a2", "a3"}}, capT0)
	if n := l.CountFor("s", []string{"a1"}, capT0); n != 1 {
		t.Fatalf("a1 = %d, want 1", n)
	}
	if n := l.CountFor("s", []string{"a1", "a2"}, capT0); n != 2 {
		t.Fatalf("a1+a2 = %d, want 2 (the busiest)", n)
	}
	// An entry without accounts falls back to counting raids.
	l.Record("s", CapEntry{At: capT0.Add(-time.Minute)}, capT0)
	if n := l.CountFor("s", []string{"a1"}, capT0); n != 3 {
		t.Fatalf("with an unnamed entry = %d, want 3 raids", n)
	}
	if n := l.CountFor("s", nil, capT0); n != 3 {
		t.Fatalf("no accounts = %d, want 3", n)
	}
}

func TestCapFileRoundTrip(t *testing.T) {
	path := filepath.Join(t.TempDir(), "sub", InstanceCapFile)
	if l, err := LoadCapLedger(path); err != nil || l.Count("s", capT0) != 0 {
		t.Fatalf("missing file: %v, count %d", err, l.Count("s", capT0))
	}
	for i := 0; i < 2; i++ {
		if err := UpdateCapLedger(path, func(l *CapLedger) { l.Record("s", CapEntry{At: capT0, Plan: "zg"}, capT0) }); err != nil {
			t.Fatal(err)
		}
	}
	l, err := LoadCapLedger(path)
	if err != nil || l.Count("s", capT0) != 2 {
		t.Fatalf("reload: %v, count %d", err, l.Count("s", capT0))
	}
}
