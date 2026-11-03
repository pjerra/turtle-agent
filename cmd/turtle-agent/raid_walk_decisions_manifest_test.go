package main

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/pjerra/turtle-agent/internal/tbdash"
)

const (
	manifestCapA = "20261029T200000Z-Commander-molten_core"
	manifestCapB = "20261029T210000Z-Commander-molten_core"
	manifestCapC = "20261029T220000Z-Commander-molten_core"
)

// manifestCap is one ended capture with index.json and one segment.
func manifestCap(id string) tbdash.DecisionCapture {
	ended := time.Date(2026, 10, 29, 20, 5, 0, 0, time.UTC)
	return tbdash.DecisionCapture{
		ID: id, Commander: "Commander", Ended: &ended, Started: ended.Add(-time.Minute),
		Segments: []tbdash.DecisionSegment{{File: "seg-0001.jsonl"}},
	}
}

// pullManifestRun runs one pull against fake into out and returns its result and notes.
func pullManifestRun(t *testing.T, fake *fakeDecisionsDash, out string, maxBytes int64) (decisionsPullResult, []string) {
	t.Helper()
	since := time.Date(2026, 10, 29, 0, 0, 0, 0, time.UTC)
	return pullDecisionsInto(context.Background(), fake, "Commander", since, since.Add(24*time.Hour), out, maxBytes)
}

func readManifestFile(t *testing.T, out string) []decisionManifestCapture {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join(out, "decisions", "manifest.json"))
	if err != nil {
		t.Fatalf("manifest.json: %v", err)
	}
	var m []decisionManifestCapture
	if err := json.Unmarshal(raw, &m); err != nil {
		t.Fatalf("manifest.json is not a list: %v: %q", err, raw)
	}
	return m
}

// firstTwoCaptures pulls A and B with the normal bodies and returns the manifest it wrote.
func firstTwoCaptures(t *testing.T, out string) []decisionManifestCapture {
	t.Helper()
	first := &fakeDecisionsDash{
		caps:  []tbdash.DecisionCapture{manifestCap(manifestCapA), manifestCap(manifestCapB)},
		files: map[string]string{"index.json": "idx-1", "seg-0001.jsonl": "seg-one"},
	}
	if res, notes := pullManifestRun(t, first, out, 1<<30); len(res.Captures) != 2 {
		t.Fatalf("first pull: %d captures, notes %v", len(res.Captures), notes)
	}
	before := readManifestFile(t, out)
	if len(before) != 2 {
		t.Fatalf("first manifest has %d entries, want 2", len(before))
	}
	return before
}

// A failed re-pull keeps the earlier complete capture and its manifest row.
func TestDecisionManifestFailedRePullKeepsOldEntry(t *testing.T) {
	out := t.TempDir()
	before := firstTwoCaptures(t, out)
	failed := &fakeDecisionsDash{
		caps:    []tbdash.DecisionCapture{manifestCap(manifestCapA)},
		files:   map[string]string{"index.json": "idx-1", "seg-0001.jsonl": "seg-one"},
		fileErr: map[string]error{"seg-0001.jsonl": errors.New("HTTP 500 for GET /api/v1/decisions")},
	}
	res, _ := pullManifestRun(t, failed, out, 1<<30)
	if res.Incomplete != 1 || len(res.Captures) != 0 {
		t.Fatalf("re-pull: incomplete %d, captures %d; want 1 and 0", res.Incomplete, len(res.Captures))
	}
	if after := readManifestFile(t, out); !reflect.DeepEqual(after, before) {
		t.Fatalf("failed re-pull changed the manifest:\nbefore %+v\nafter  %+v", before, after)
	}
}

// An empty window (nothing listed) keeps every valid capture.
func TestDecisionManifestEmptyWindowKeepsAll(t *testing.T) {
	out := t.TempDir()
	before := firstTwoCaptures(t, out)
	res, _ := pullManifestRun(t, &fakeDecisionsDash{}, out, 1<<30)
	if res.ManifestFailed || len(res.Captures) != 0 {
		t.Fatalf("empty window: manifestFailed %v, captures %d", res.ManifestFailed, len(res.Captures))
	}
	if after := readManifestFile(t, out); !reflect.DeepEqual(after, before) {
		t.Fatalf("empty window changed the manifest:\nbefore %+v\nafter  %+v", before, after)
	}
}

// Budget exhaustion replaces the capture pulled before it and keeps the untouched ones.
func TestDecisionManifestBudgetKeepsRest(t *testing.T) {
	out := t.TempDir()
	first := &fakeDecisionsDash{
		caps:  []tbdash.DecisionCapture{manifestCap(manifestCapA), manifestCap(manifestCapB), manifestCap(manifestCapC)},
		files: map[string]string{"index.json": "idx-1", "seg-0001.jsonl": "seg-one"},
	}
	if res, notes := pullManifestRun(t, first, out, 1<<30); len(res.Captures) != 3 {
		t.Fatalf("first pull: %d captures, notes %v", len(res.Captures), notes)
	}
	before := readManifestFile(t, out)
	if len(before) != 3 {
		t.Fatalf("first manifest has %d entries, want 3", len(before))
	}
	// Each capture is 12 bytes. The budget is exactly A's 12 bytes, so A re-pulls and B is never reached.
	second := &fakeDecisionsDash{
		caps:  []tbdash.DecisionCapture{manifestCap(manifestCapA), manifestCap(manifestCapB), manifestCap(manifestCapC)},
		files: map[string]string{"index.json": "idx-2", "seg-0001.jsonl": "seg-two"},
	}
	res, _ := pullManifestRun(t, second, out, 12)
	if !res.BudgetHit {
		t.Fatal("budget: BudgetHit not set")
	}
	after := readManifestFile(t, out)
	if len(after) != 3 {
		t.Fatalf("budget run: manifest has %d entries, want 3: %+v", len(after), after)
	}
	if after[0].Files[0].SHA256 == before[0].Files[0].SHA256 {
		t.Fatal("budget run: capture A was not replaced by its re-pull")
	}
	if !reflect.DeepEqual(after[1:], before[1:]) {
		t.Fatalf("budget run changed captures B and C:\nbefore %+v\nafter  %+v", before[1:], after[1:])
	}
}

// A kept entry whose directory no longer validates is dropped from the manifest, with a note.
func TestDecisionManifestDropsInvalidKeptEntry(t *testing.T) {
	out := t.TempDir()
	firstTwoCaptures(t, out)
	if err := os.WriteFile(filepath.Join(out, "decisions", manifestCapB, "seg-0001.jsonl"), []byte("tampered"), 0o600); err != nil {
		t.Fatal(err)
	}
	_, notes := pullManifestRun(t, &fakeDecisionsDash{}, out, 1<<30)
	after := readManifestFile(t, out)
	if len(after) != 1 || after[0].ID != manifestCapA {
		t.Fatalf("manifest after tamper: %+v, want only %s", after, manifestCapA)
	}
	if !strings.Contains(strings.Join(notes, "\n"), manifestCapB+": dropped from manifest") {
		t.Fatalf("no drop note for %s: %v", manifestCapB, notes)
	}
}
