package raidwalk

import (
	"testing"
	"time"
)

// TestGMInstanceDataMarkHashResend: a changed command list resends. The
// mark carries the hash it was sent with, so a firing with another hash is
// not done — but the same hash stays silent.
func TestGMInstanceDataMarkHashResend(t *testing.T) {
	l := &CapLedger{}
	l.Record("m910q", CapEntry{At: capT0, Instance: 7}, capT0)
	l.NoteGMInstanceData("m910q", "7", "hakkar", capT0, "hash-one")
	if !l.GMInstanceDataDone("m910q", "7", "hakkar", "hash-one", capT0) {
		t.Fatal("the same command list must read back as done")
	}
	if l.GMInstanceDataDone("m910q", "7", "hakkar", "hash-two", capT0) {
		t.Fatal("a changed command list must resend, not read back as done")
	}
	// Recording the new list makes that one done, and the old one not.
	l.NoteGMInstanceData("m910q", "7", "hakkar", capT0, "hash-two")
	if !l.GMInstanceDataDone("m910q", "7", "hakkar", "hash-two", capT0) {
		t.Fatal("the re-sent list must read back as done")
	}
	if l.GMInstanceDataDone("m910q", "7", "hakkar", "hash-one", capT0) {
		t.Fatal("the superseded list must not read back as done")
	}
}

// TestGMInstanceDataMarkFirstSeen: the core reuses instance ids, so a mark
// only counts when it is not older than that instance's first-seen
// cap-ledger entry time (the instance's own At). A mark in instance 7, a
// server start noted, and a new walk in instance 7 resends; the reuse case
// (a mark newer than the entry) still skips.
func TestGMInstanceDataMarkFirstSeen(t *testing.T) {
	l := &CapLedger{}
	// The first instance 7, entered at capT0.
	l.Record("m910q", CapEntry{At: capT0, Instance: 7}, capT0)
	fired := capT0.Add(time.Minute)
	l.NoteGMInstanceData("m910q", "7", "hakkar", fired, "h")
	if !l.GMInstanceDataDone("m910q", "7", "hakkar", "h", capT0.Add(2*time.Minute)) {
		t.Fatal("a mark newer than the entry must skip the resend")
	}
	// The server restarts; the core hands out a NEW instance 7.
	start := capT0.Add(3 * time.Minute)
	l.NoteServerStart("m910q", start)
	if l.GMInstanceDataDone("m910q", "7", "hakkar", "h", start.Add(time.Minute)) {
		t.Fatal("a restart must drop the mark: the new instance 7 resends")
	}
	// The new instance 7 is entered and fired: it skips again.
	l.Record("m910q", CapEntry{At: start.Add(time.Minute), Instance: 7}, start.Add(2*time.Minute))
	l.NoteGMInstanceData("m910q", "7", "hakkar", start.Add(2*time.Minute), "h")
	if !l.GMInstanceDataDone("m910q", "7", "hakkar", "h", start.Add(3*time.Minute)) {
		t.Fatal("the new instance 7's own mark must skip")
	}
}

// TestGMInstanceDataMarkReset: Reset drops the marks with the entries —
// reused ids name new copies.
func TestGMInstanceDataMarkReset(t *testing.T) {
	l := &CapLedger{}
	l.Record("m910q", CapEntry{At: capT0, Instance: 7}, capT0)
	l.NoteGMInstanceData("m910q", "7", "hakkar", capT0, "h")
	l.Reset("m910q")
	if l.GMInstanceDataDone("m910q", "7", "hakkar", "h", capT0) {
		t.Fatal("Reset must drop the marks with the entries")
	}
}

// TestGMInstanceDataOldBareMarkDone: an old ledger's bare "<inst>/<leg>"
// mark (no time, no hash) reads back as done, as before.
func TestGMInstanceDataOldBareMarkDone(t *testing.T) {
	l := &CapLedger{}
	l.Record("m910q", CapEntry{At: capT0, Instance: 7}, capT0)
	s := l.server("m910q")
	s.GMInstanceData = append(s.GMInstanceData, GMInstanceMark{Mark: "7/hakkar"})
	if !l.GMInstanceDataDone("m910q", "7", "hakkar", "any-hash", capT0) {
		t.Fatal("an old bare mark must read back as done")
	}
}
