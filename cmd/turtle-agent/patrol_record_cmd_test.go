package main

// Tests for patrol-record (spec D2, section 6.3): the command records patrol
// loops through an already logged-in slot agent, and only ever sends the
// stance lines of the allow-list. Every test runs against a scripted slot
// client (slotClient.post), so no agent and no server are needed.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/pjerra/turtle-agent/internal/objects"
	"github.com/pjerra/turtle-agent/internal/packet"
	"github.com/pjerra/turtle-agent/internal/patrols"
	"github.com/pjerra/turtle-agent/internal/session"
)

// patrolRecordFake scripts one slot agent's answers; call counts start at 1.
type patrolRecordFake struct {
	mu sync.Mutex

	methods []string
	lines   []string

	statusCalls int
	unitsCalls  int

	status func(call int) session.StatusInfo
	units  func(call int) []objects.Unit
	party  session.PartyInfo
	own    objects.Unit
}

func (f *patrolRecordFake) client(t *testing.T) *slotClient {
	t.Helper()
	c := &slotClient{
		manifest: teardownSlotManifest{Server: "m910q", Slot: "1", Holder: "w-a", Character: "Gmtest"},
		token:    "patrol-record-test-token",
	}
	c.post = func(port int, token, method string, params json.RawMessage, timeout time.Duration) (json.RawMessage, *rpcError, error) {
		f.mu.Lock()
		f.methods = append(f.methods, method)
		switch method {
		case "status":
			f.statusCalls++
		case "units":
			f.unitsCalls++
		case "gm":
			var p struct {
				Text string `json:"text"`
			}
			if err := json.Unmarshal(params, &p); err != nil {
				t.Errorf("gm params %s: %v", params, err)
			}
			f.lines = append(f.lines, p.Text)
		}
		statusCall, unitsCall := f.statusCalls, f.unitsCalls
		f.mu.Unlock()

		switch method {
		case "party":
			return patrolRecordJSON(t, f.party)
		case "status":
			if f.status == nil {
				t.Errorf("unexpected status call")
				return nil, nil, errors.New("unexpected status call")
			}
			return patrolRecordJSON(t, f.status(statusCall))
		case "units":
			if f.units == nil {
				t.Errorf("unexpected units call")
				return nil, nil, errors.New("unexpected units call")
			}
			return patrolRecordJSON(t, f.units(unitsCall))
		case "unit":
			return patrolRecordJSON(t, f.own)
		case "gm":
			return json.RawMessage(`{}`), nil, nil
		}
		return nil, nil, fmt.Errorf("unexpected method %s", method)
	}
	return c
}

// patrolRecordJSON is a fake reply as the post hook returns it.
func patrolRecordJSON(t *testing.T, v any) (json.RawMessage, *rpcError, error) {
	t.Helper()
	raw, err := json.Marshal(v)
	if err != nil {
		t.Errorf("marshal reply %T: %v", v, err)
		return nil, nil, err
	}
	return raw, nil, nil
}

func (f *patrolRecordFake) snapshot() (methods, lines []string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.methods...), append([]string(nil), f.lines...)
}

// patrolRecordClock is the command's fake wall clock: sleep advances it.
type patrolRecordClock struct {
	mu  sync.Mutex
	now time.Time
}

func (c *patrolRecordClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.now
}

func (c *patrolRecordClock) Sleep(_ context.Context, d time.Duration) error {
	c.mu.Lock()
	c.now = c.now.Add(d)
	c.mu.Unlock()
	return nil
}

// patrolRecordEnvFor wires a scripted client and the fake clock.
func patrolRecordEnvFor(t *testing.T, f *patrolRecordFake, clock *patrolRecordClock) patrolRecordEnv {
	t.Helper()
	return patrolRecordEnv{
		now:   clock.Now,
		sleep: clock.Sleep,
		open:  func(string) (*slotClient, error) { return f.client(t), nil },
		floors: func(string, uint32, float32, float32) ([]float32, error) {
			t.Fatal("floors asked with --z given")
			return nil, nil
		},
	}
}

// patrolRecordArgs is the documented flag order: the slot dir first.
func patrolRecordArgs(dir, out string) []string {
	return []string{
		dir,
		"--map", "249",
		"--area", "-166.6,-196.0,120",
		"--z", "-66.26",
		"--minutes", "1",
		"--sample-ms", "500",
		"--out", out,
	}
}

// patrolRecordSquare is a 10-yd square walked at 2.5 yd/s from its (-5, -5)
// corner: a 16 s loop, three of them in a one-minute recording.
func patrolRecordSquare(t float64) (float32, float32) {
	d := math.Mod(t*2.5, 40)
	switch {
	case d < 10:
		return float32(-5 + d), -5
	case d < 20:
		return 5, float32(-5 + (d - 10))
	case d < 30:
		return float32(5 - (d - 20)), 5
	default:
		return -5, float32(5 - (d - 30))
	}
}

// patrolRecordMoving is the warder sample at one 500 ms tick.
func patrolRecordMoving(tick int) objects.Unit {
	x, y := patrolRecordSquare(float64(tick) * 0.5)
	return objects.Unit{
		GUID:   packet.GUID(0xF130002F61000ABC),
		Type:   "creature",
		Entry:  12129,
		Name:   "Onyxian Warder",
		X:      -166.623 + x,
		Y:      -196.003 + y,
		Z:      -66.2619,
		Moving: true,
	}
}

// TestPatrolRecordHappyPath: a scripted loop becomes one patrol in the file,
// matched to its world-export spawn; the run exits 0 and prints the patrol.
func TestPatrolRecordHappyPath(t *testing.T) {
	f := &patrolRecordFake{
		status: func(int) session.StatusInfo {
			return session.StatusInfo{State: session.StatusInWorld, Map: 249, GUID: packet.GUID(0x00000000000000AA)}
		},
		units: func(call int) []objects.Unit { return []objects.Unit{patrolRecordMoving(call - 1)} },
	}
	clock := &patrolRecordClock{now: time.Date(2026, 10, 29, 10, 0, 0, 0, time.UTC)}

	// The world export: one warder spawn at the loop's first corner.
	spawnDir := t.TempDir()
	tsv := "guid\tentry\tname\tlevel_min\tlevel_max\trank\tfaction\tnpc_flags\tx\ty\tz\twander\tmovement_type\tgroup_leader\tpatrol\tauras\n" +
		"52048\t12129\tOnyxian Warder\t62\t62\t1\t103\t0\t-171.623\t-201.003\t-66.2619\t5\t2\t0\t1\t\n"
	if err := os.WriteFile(filepath.Join(spawnDir, "creatures.tsv"), []byte(tsv), 0o600); err != nil {
		t.Fatal(err)
	}
	out := filepath.Join(t.TempDir(), "patrols.json")
	args := append(patrolRecordArgs(filepath.Join(t.TempDir(), "slot"), out), "--spawns", spawnDir)

	var stdout, stderr strings.Builder
	code := patrolRecordCmdEnv(context.Background(), args, &stdout, &stderr, patrolRecordEnvFor(t, f, clock))
	if code != exitOK {
		t.Fatalf("code %d, want 0; stderr %q", code, stderr.String())
	}

	file, err := patrols.Read(out)
	if err != nil {
		t.Fatalf("read the recording: %v", err)
	}
	if len(file.Patrols) != 1 {
		t.Fatalf("%d patrols, want 1: %+v", len(file.Patrols), file.Patrols)
	}
	p := file.Patrols[0]
	if p.PeriodS < 15 || p.PeriodS > 17 {
		t.Fatalf("period %g s, want 16 +-1", p.PeriodS)
	}
	if len(p.Path) < 4 || len(p.Path) > 8 {
		t.Fatalf("path %d points, want the square's 4-6", len(p.Path))
	}
	if p.ID != "52048" || len(p.SpawnGUIDs) != 1 || p.SpawnGUIDs[0] != 52048 {
		t.Fatalf("patrol id %q spawn_guids %v, want 52048", p.ID, p.SpawnGUIDs)
	}
	if !strings.Contains(stdout.String(), "matches spawn 52048") {
		t.Fatalf("stdout %q has no spawn match line", stdout.String())
	}
	if file.Stopped != "" {
		t.Fatalf("stopped %q on a complete recording", file.Stopped)
	}
}

// TestPatrolRecordRefusesGroupedAgent: a grouped agent is refused before any
// stance line; the recording would land in a bound raid's instance.
func TestPatrolRecordRefusesGroupedAgent(t *testing.T) {
	f := &patrolRecordFake{party: session.PartyInfo{InGroup: true}}
	clock := &patrolRecordClock{now: time.Date(2026, 10, 29, 10, 0, 0, 0, time.UTC)}
	out := filepath.Join(t.TempDir(), "patrols.json")

	var stdout, stderr strings.Builder
	code := patrolRecordCmdEnv(context.Background(), patrolRecordArgs(filepath.Join(t.TempDir(), "slot"), out),
		&stdout, &stderr, patrolRecordEnvFor(t, f, clock))
	if code != exitSlotRefused {
		t.Fatalf("code %d, want %d; stderr %q", code, exitSlotRefused, stderr.String())
	}
	methods, lines := f.snapshot()
	if len(lines) != 0 {
		t.Fatalf("sent GM lines to a grouped agent: %v", lines)
	}
	if len(methods) != 1 || methods[0] != "party" {
		t.Fatalf("methods %v, want only party", methods)
	}
	if _, err := os.Stat(out); !os.IsNotExist(err) {
		t.Fatalf("a refused recording wrote %s", out)
	}
}

// TestPatrolRecordStopsOnCombat: a unit targeting the GM stops the recording
// at once, restores visibility, writes the file with the reason and exits 22.
// Mutation: skipping the target check records to the end and exits 0.
func TestPatrolRecordStopsOnCombat(t *testing.T) {
	const own = packet.GUID(0x00000000000000AA)
	f := &patrolRecordFake{
		status: func(int) session.StatusInfo {
			return session.StatusInfo{State: session.StatusInWorld, Map: 249, GUID: own}
		},
		units: func(call int) []objects.Unit {
			if call == 5 {
				return []objects.Unit{{
					GUID:   packet.GUID(0xF130002F61000ABC),
					Type:   "creature",
					Entry:  12129,
					Name:   "Onyxian Warder",
					Target: own,
				}}
			}
			return nil
		},
	}
	clock := &patrolRecordClock{now: time.Date(2026, 10, 29, 10, 0, 0, 0, time.UTC)}
	out := filepath.Join(t.TempDir(), "patrols.json")

	var stdout, stderr strings.Builder
	code := patrolRecordCmdEnv(context.Background(), patrolRecordArgs(filepath.Join(t.TempDir(), "slot"), out),
		&stdout, &stderr, patrolRecordEnvFor(t, f, clock))
	if code != exitPatrolRecordStopped {
		t.Fatalf("code %d, want %d; stderr %q", code, exitPatrolRecordStopped, stderr.String())
	}
	_, lines := f.snapshot()
	if len(lines) == 0 || lines[len(lines)-1] != ".gm visible on" {
		t.Fatalf("GM lines %v do not end with .gm visible on", lines)
	}
	file, err := patrols.Read(out)
	if err != nil {
		t.Fatalf("read the partial recording: %v", err)
	}
	if file.Stopped != "targeted by Onyxian Warder" {
		t.Fatalf("stopped %q, want targeted by Onyxian Warder", file.Stopped)
	}
}

// TestPatrolRecordSendsOnlyAllowedLines pins the stance: .gm on, .gm visible
// off, .go xyz, and .gm visible on last, and no attack/cast/target/movement
// RPC ever.
// Mutation: sending .gm visible on first fails the order assert.
func TestPatrolRecordSendsOnlyAllowedLines(t *testing.T) {
	f := &patrolRecordFake{
		status: func(int) session.StatusInfo {
			return session.StatusInfo{State: session.StatusInWorld, Map: 249, GUID: packet.GUID(0x00000000000000AA)}
		},
		units: func(call int) []objects.Unit { return []objects.Unit{patrolRecordMoving(call - 1)} },
	}
	clock := &patrolRecordClock{now: time.Date(2026, 10, 29, 10, 0, 0, 0, time.UTC)}
	out := filepath.Join(t.TempDir(), "patrols.json")

	var stdout, stderr strings.Builder
	code := patrolRecordCmdEnv(context.Background(), patrolRecordArgs(filepath.Join(t.TempDir(), "slot"), out),
		&stdout, &stderr, patrolRecordEnvFor(t, f, clock))
	if code != exitOK {
		t.Fatalf("code %d, want 0; stderr %q", code, stderr.String())
	}
	methods, lines := f.snapshot()
	want := []string{".gm on", ".gm visible off"}
	if len(lines) != 4 {
		t.Fatalf("GM lines %v, want 4 (on, visible off, go, visible on)", lines)
	}
	for i, w := range want {
		if lines[i] != w {
			t.Fatalf("line %d %q, want %q (order %v)", i, lines[i], w, lines)
		}
	}
	if !strings.HasPrefix(lines[2], ".go xyz ") {
		t.Fatalf("line 2 %q is not the teleport", lines[2])
	}
	if lines[3] != ".gm visible on" {
		t.Fatalf("last line %q, want .gm visible on", lines[3])
	}
	forbidden := map[string]bool{"attack": true, "cast": true, "target": true, "walk_to": true, "path_to": true, "walk_path": true}
	for _, m := range methods {
		if forbidden[m] {
			t.Fatalf("called forbidden method %q (methods %v)", m, methods)
		}
		switch m {
		case "status", "party", "unit", "units", "gm":
		default:
			t.Fatalf("called method %q, not in the stance (methods %v)", m, methods)
		}
	}
}

// TestPatrolRecordNeedsZOnMultiFloor: two floors at (x, y) without --z is a
// usage error that names both, before the agent is touched.
func TestPatrolRecordNeedsZOnMultiFloor(t *testing.T) {
	f := &patrolRecordFake{}
	clock := &patrolRecordClock{now: time.Date(2026, 10, 29, 10, 0, 0, 0, time.UTC)}
	env := patrolRecordEnvFor(t, f, clock)
	env.floors = func(string, uint32, float32, float32) ([]float32, error) {
		return []float32{-66.7, -24.3}, nil
	}
	env.open = func(string) (*slotClient, error) {
		t.Fatal("opened the slot before the z question was settled")
		return nil, nil
	}
	args := []string{
		filepath.Join(t.TempDir(), "slot"),
		"--map", "249",
		"--area", "49.8,-114.6,120",
		"--minutes", "1",
		"--mmaps", t.TempDir(),
		"--out", filepath.Join(t.TempDir(), "patrols.json"),
	}
	var stdout, stderr strings.Builder
	code := patrolRecordCmdEnv(context.Background(), args, &stdout, &stderr, env)
	if code != exitUsage {
		t.Fatalf("code %d, want %d; stderr %q", code, exitUsage, stderr.String())
	}
	for _, z := range []string{"-66.7", "-24.3"} {
		if !strings.Contains(stderr.String(), z) {
			t.Fatalf("stderr %q does not name floor %s", stderr.String(), z)
		}
	}
}
