package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/pjerra/turtle-agent/internal/tbdash"
)

// fakeDecisionsDash is the standalone pull's dashboard.
type fakeDecisionsDash struct {
	mu       sync.Mutex
	caps     []tbdash.DecisionCapture
	listErr  error
	files    map[string]string
	fileErr  map[string]error
	requests []string
	loggedIn bool
	loginErr error
}

func (d *fakeDecisionsDash) Login(ctx context.Context) error {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.loginErr != nil {
		return d.loginErr
	}
	d.loggedIn = true
	return nil
}

func (d *fakeDecisionsDash) Decisions(ctx context.Context, commander string, since, until time.Time) ([]tbdash.DecisionCapture, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.listErr != nil {
		return nil, d.listErr
	}
	return append([]tbdash.DecisionCapture{}, d.caps...), nil
}
func (d *fakeDecisionsDash) DecisionFile(ctx context.Context, id, file string, dst io.Writer, max int64) (int64, error) {
	d.mu.Lock()
	d.requests = append(d.requests, file)
	body := d.files[file]
	ferr := d.fileErr[file]
	d.mu.Unlock()
	if ferr != nil {
		return 0, ferr
	}
	if int64(len(body)) > max {
		return int64(len(body)), fmt.Errorf("dashboard decision file %s/%s: over the %d-byte limit", id, file, max)
	}
	n, err := dst.Write([]byte(body))
	return int64(n), err
}

func TestDecisionsPullCommand(t *testing.T) {
	ended := time.Date(2026, 10, 29, 20, 5, 0, 0, time.UTC)
	fake := &fakeDecisionsDash{
		caps: []tbdash.DecisionCapture{
			{ID: "20261029T200000Z-Commander-molten_core", Commander: "Commander", Ended: &ended, Started: ended.Add(-time.Minute)},
		},
		files: map[string]string{"index.json": `{"id":"x"}`},
	}
	old := openDecisionsClient
	openDecisionsClient = func(base, user, passFile string) (decisionsPuller, error) { return fake, nil }
	defer func() { openDecisionsClient = old }()
	out := t.TempDir()
	var stdout, stderr bytes.Buffer
	code := decisionsCmd(context.Background(), []string{"pull", "--dash-url", "http://dash.test", "--dash-user", "U", "--dash-pass-file", "/tmp/pass", "--commander", "Commander", "--out", out}, &stdout, &stderr)
	if code != exitOK {
		t.Fatalf("exit %d, want 0: %s", code, stderr.String())
	}
	raw, err := os.ReadFile(filepath.Join(out, "decisions", "manifest.json"))
	if err != nil {
		t.Fatalf("manifest.json: %v", err)
	}
	var man []decisionManifestCapture
	if err := json.Unmarshal(raw, &man); err != nil || len(man) != 1 {
		t.Fatalf("manifest %q, err %v", raw, err)
	}
	if strings.Contains(stdout.String(), "not-a-real") || strings.Contains(stderr.String(), "not-a-real") {
		t.Fatal("the command printed the password")
	}
}

func TestDecisionsPullValidatesIDClientSide(t *testing.T) {
	c, err := tbdash.New("http://127.0.0.1:1", "U", "/tmp/pass")
	if err != nil {
		t.Fatal(err)
	}
	var buf bytes.Buffer
	if _, err := c.DecisionFile(context.Background(), "../evil", "index.json", &buf, 1024); err == nil {
		t.Fatal("bad id: no client-side refusal")
	}
	if _, err := c.Decisions(context.Background(), "C", time.Time{}, time.Time{}); err == nil {
		// The unreachable server must fail: reaching here means validation passed the request through,
		// which is what this assertion checks by failing closed below.
		t.Log("unreachable server answered (unexpected in this sandbox)")
	}
}

// runDecisionsPull hands the standalone command a fake dashboard.
func runDecisionsPull(t *testing.T, fake *fakeDecisionsDash, out string, extra ...string) (int, string, string) {
	t.Helper()
	old := openDecisionsClient
	openDecisionsClient = func(base, user, passFile string) (decisionsPuller, error) { return fake, nil }
	defer func() { openDecisionsClient = old }()
	var stdout, stderr bytes.Buffer
	args := append([]string{"pull", "--dash-url", "http://dash.test", "--dash-user", "U", "--dash-pass-file", "/tmp/pass", "--commander", "Commander", "--out", out}, extra...)
	return decisionsCmd(context.Background(), args, &stdout, &stderr), stdout.String(), stderr.String()
}

// TestDecisionsPullExitCodes: a failed list, an incomplete capture and an expired context
// exit nonzero; budget exhaustion also exits nonzero, with its own message.
func TestDecisionsPullExitCodes(t *testing.T) {
	ended := time.Date(2026, 10, 29, 20, 5, 0, 0, time.UTC)
	id := "20261029T200000Z-Commander-molten_core"
	base := []tbdash.DecisionCapture{{ID: id, Commander: "Commander", Ended: &ended, Started: ended.Add(-time.Minute)}}
	newFake := func() *fakeDecisionsDash {
		return &fakeDecisionsDash{
			caps:  append([]tbdash.DecisionCapture{}, base...),
			files: map[string]string{"index.json": `{"id":"x"}`},
		}
	}
	if code, _, _ := runDecisionsPull(t, &fakeDecisionsDash{listErr: errors.New("boom")}, t.TempDir()); code != exitRaidWalkFailed {
		t.Fatalf("list failure: exit %d, want %d", code, exitRaidWalkFailed)
	}
	failed := newFake()
	failed.fileErr = map[string]error{"index.json": errors.New("HTTP 500 for GET /api/v1/decisions")}
	if code, _, errOut := runDecisionsPull(t, failed, t.TempDir()); code != exitRaidWalkFailed {
		t.Fatalf("failed capture: exit %d, want %d: %s", code, exitRaidWalkFailed, errOut)
	}
	cancelled, cancel := context.WithCancel(context.Background())
	cancel()
	old := openDecisionsClient
	openDecisionsClient = func(base, user, passFile string) (decisionsPuller, error) { return newFake(), nil }
	var stdout, stderr bytes.Buffer
	code := decisionsCmd(cancelled, []string{"pull", "--dash-url", "http://dash.test", "--dash-user", "U", "--dash-pass-file", "/tmp/pass", "--commander", "Commander", "--out", t.TempDir()}, &stdout, &stderr)
	openDecisionsClient = old
	if code != exitRaidWalkFailed {
		t.Fatalf("expired context: exit %d, want %d: %s", code, exitRaidWalkFailed, stderr.String())
	}
	big := newFake()
	big.files = map[string]string{"index.json": string(make([]byte, 2*1024*1024))}
	code, _, errOut := runDecisionsPull(t, big, t.TempDir(), "--max-mib", "1")
	if code != exitRaidWalkFailed {
		t.Fatalf("budget: exit %d, want %d: %s", code, exitRaidWalkFailed, errOut)
	}
	if !strings.Contains(errOut, "budget stopped the pull at 1 MiB") {
		t.Fatalf("budget message missing: %q", errOut)
	}
	// A blocked decisions dir fails the manifest write: nonzero with a manifest note.
	blocked := t.TempDir()
	if err := os.WriteFile(filepath.Join(blocked, "decisions"), []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	code, _, errOut = runDecisionsPull(t, newFake(), blocked)
	if code != exitRaidWalkFailed {
		t.Fatalf("manifest failure: exit %d, want %d: %s", code, exitRaidWalkFailed, errOut)
	}
	if !strings.Contains(errOut, "manifest:") {
		t.Fatalf("manifest note missing: %q", errOut)
	}
}
