package raidwalk

import (
	"encoding/json"
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
	if !l.GMInstanceDataDone("m910q", "7", "hakkar", "hash-one") {
		t.Fatal("the same command list must read back as done")
	}
	if l.GMInstanceDataDone("m910q", "7", "hakkar", "hash-two") {
		t.Fatal("a changed command list must resend, not read back as done")
	}
	// Recording the new list makes that one done, and the old one not.
	l.NoteGMInstanceData("m910q", "7", "hakkar", capT0, "hash-two")
	if !l.GMInstanceDataDone("m910q", "7", "hakkar", "hash-two") {
		t.Fatal("the re-sent list must read back as done")
	}
	if l.GMInstanceDataDone("m910q", "7", "hakkar", "hash-one") {
		t.Fatal("the superseded list must not read back as done")
	}
}

// TestGMInstanceDataMarkRestartDrops: the core reuses instance ids across
// copies, so a restart drops the marks (NoteServerStart, Reset) and the new
// instance resends.
func TestGMInstanceDataMarkRestartDrops(t *testing.T) {
	l := &CapLedger{}
	l.Record("m910q", CapEntry{At: capT0, Instance: 7}, capT0)
	fired := capT0.Add(time.Minute)
	l.NoteGMInstanceData("m910q", "7", "hakkar", fired, "h")
	if !l.GMInstanceDataDone("m910q", "7", "hakkar", "h") {
		t.Fatal("a mark in the same copy must skip the resend")
	}
	// The server restarts; the core may hand out a NEW instance 7.
	start := capT0.Add(3 * time.Minute)
	l.NoteServerStart("m910q", start)
	if l.GMInstanceDataDone("m910q", "7", "hakkar", "h") {
		t.Fatal("a restart must drop the mark: the new instance 7 resends")
	}
	// The new instance 7 is entered and fired: it skips again.
	l.Record("m910q", CapEntry{At: start.Add(time.Minute), Instance: 7}, start.Add(2*time.Minute))
	l.NoteGMInstanceData("m910q", "7", "hakkar", start.Add(2*time.Minute), "h")
	if !l.GMInstanceDataDone("m910q", "7", "hakkar", "h") {
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
	if l.GMInstanceDataDone("m910q", "7", "hakkar", "h") {
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
	if !l.GMInstanceDataDone("m910q", "7", "hakkar", "any-hash") {
		t.Fatal("an old bare mark must read back as done")
	}
}

// TestGMInstanceDataOldStringLedgerLoads: an old ledger file stores the marks
// as bare "<inst>/<leg>" strings. It must decode (not move aside as
// corrupt) and the bare mark must read back as done.
func TestGMInstanceDataOldStringLedgerLoads(t *testing.T) {
	raw := `{"servers":{"m910q":{"entries":[{"at":"2026-11-04T12:00:00Z","instance":7}],"gm_instance_data":["7/hakkar"]}}}`
	var l CapLedger
	if err := json.Unmarshal([]byte(raw), &l); err != nil {
		t.Fatalf("an old string ledger must decode: %v", err)
	}
	if !l.GMInstanceDataDone("m910q", "7", "hakkar", "any-hash") {
		t.Fatal("an old string mark must read back as done")
	}
	// A mixed file decodes too.
	raw = `{"servers":{"m910q":{"gm_instance_data":["7/hakkar",{"inst":"8","leg":"jeklik","hash":"h"}]}}}`
	l = CapLedger{}
	if err := json.Unmarshal([]byte(raw), &l); err != nil {
		t.Fatalf("a mixed ledger must decode: %v", err)
	}
	if !l.GMInstanceDataDone("m910q", "7", "hakkar", "x") || !l.GMInstanceDataDone("m910q", "8", "jeklik", "h") {
		t.Fatal("both mark forms must read back as done")
	}
}
