package main

import (
	"bytes"
	"context"
	"encoding/json"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/pjerra/turtle-agent/internal/dashreplay"
)

// walk2Dumps is the API dumps R1 pinned: no replay.jsonl, so the command has
// to convert them.
const walk2Dumps = "../../internal/dashreplay/testdata/walk2"

// A replay never leaves the machine by accident: a non-loopback target is
// refused with exit 17 and the README's text, before anything is sent, and
// --dry-run sends nothing at all even to a loopback target.
func TestCommandRefusesNonLoopbackExit17(t *testing.T) {
	var stdout, stderr bytes.Buffer
	code := run(context.Background(), []string{"dashboard-replay", walk2Dumps, "--to", "10.0.0.5:9195"}, &stdout, &stderr)
	if code != 17 {
		t.Fatalf("exit code = %d, want 17; stderr: %s", code, stderr.String())
	}
	want := "refusing to replay to 10.0.0.5:9195: not a loopback address; pass --i-know if this daemon is a test dashboard"
	if !strings.Contains(stderr.String(), want) {
		t.Errorf("stderr = %q, want %q", stderr.String(), want)
	}

	pc, err := net.ListenPacket("udp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	defer pc.Close()
	stdout.Reset()
	stderr.Reset()
	code = run(context.Background(), []string{"dashboard-replay", walk2Dumps, "--to", pc.LocalAddr().String(), "--speed", "1000", "--dry-run"}, &stdout, &stderr)
	if code != 0 {
		t.Fatalf("dry run exit code = %d, want 0; stderr: %s", code, stderr.String())
	}
	pc.SetReadDeadline(time.Now().Add(150 * time.Millisecond))
	buf := make([]byte, 65536)
	if n, _, err := pc.ReadFrom(buf); err == nil {
		t.Fatalf("--dry-run sent a %d-byte datagram", n)
	}
}

// A folder of API dumps with no replay.jsonl is converted in memory, and the
// command says so.
func TestCommandConvertsDumpDir(t *testing.T) {
	var stdout, stderr bytes.Buffer
	code := run(context.Background(), []string{"dashboard-replay", walk2Dumps, "--to", "127.0.0.1:9195", "--dry-run"}, &stdout, &stderr)
	if code != 0 {
		t.Fatalf("exit code = %d, want 0; stderr: %s", code, stderr.String())
	}
	if !strings.Contains(stderr.String(), "converted from API dumps") {
		t.Errorf("stderr = %q, want the converted-from-API-dumps line", stderr.String())
	}
	out := stdout.String()
	if !strings.Contains(out, "datagrams over") || !strings.Contains(out, "COMMANDERS") {
		t.Errorf("stdout = %q, want the datagram count and the per-type tally", out)
	}
}

// convert writes <dir>/replay.jsonl, readable as a replay file, mode 0600.
func TestCommandConvertWritesFile(t *testing.T) {
	dir := t.TempDir()
	for _, name := range []string{"commanders-pre.json", "commanders-after-stop.json", "combat-fight-5-4.json", "combat-fights.json"} {
		b, err := os.ReadFile(filepath.Join(walk2Dumps, name))
		if err != nil {
			t.Fatalf("read %s: %v", name, err)
		}
		if err := os.WriteFile(filepath.Join(dir, name), b, 0o644); err != nil {
			t.Fatalf("write %s: %v", name, err)
		}
	}
	var stdout, stderr bytes.Buffer
	code := run(context.Background(), []string{"dashboard-replay", "convert", dir}, &stdout, &stderr)
	if code != 0 {
		t.Fatalf("exit code = %d, want 0; stderr: %s", code, stderr.String())
	}
	path := filepath.Join(dir, "replay.jsonl")
	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat %s: %v", path, err)
	}
	if perm := info.Mode().Perm(); perm != 0o600 {
		t.Errorf("mode = %04o, want 0600", perm)
	}
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	header, lines, err := dashreplay.Read(bytes.NewReader(b))
	if err != nil {
		t.Fatalf("the written file does not read as a replay: %v", err)
	}
	if header.Source != "convert" || header.Datagrams != len(lines) || len(lines) == 0 {
		t.Errorf("header = %+v with %d lines, want a convert header counting them", header, len(lines))
	}
	// The same file replays without converting again.
	if !strings.Contains(stdout.String(), "replay.jsonl") {
		t.Errorf("stdout = %q, want the written path", stdout.String())
	}
	var playOut, playErr bytes.Buffer
	if code := run(context.Background(), []string{"dashboard-replay", path, "--to", "127.0.0.1:9195", "--dry-run"}, &playOut, &playErr); code != 0 {
		t.Fatalf("replaying the written file: exit %d, stderr %s", code, playErr.String())
	}
	if strings.Contains(playErr.String(), "converted from API dumps") {
		t.Errorf("a written replay.jsonl was converted again: %s", playErr.String())
	}
}

// The daemon's payloads, mirrored with only the fields a replay carries
// (turtle-agent cannot import tools/observability).
type replayCommanders struct {
	V          int               `json:"v"`
	Session    uint64            `json:"session"`
	Seq        uint64            `json:"seq"`
	TS         int64             `json:"ts"`
	Type       string            `json:"type"`
	Commanders []json.RawMessage `json:"commanders"`
}

type replayFight struct {
	V       int             `json:"v"`
	Session uint64          `json:"session"`
	Seq     uint64          `json:"seq"`
	TS      int64           `json:"ts"`
	Type    string          `json:"type"`
	Fight   json.RawMessage `json:"fight"`
}

// The datagrams a replay sends are the daemon's shapes: COMMANDERS and
// COMBAT_FIGHT decode into the payload types (mirrored, the module cannot be
// imported here).
func TestCommandPlaysDaemonShapes(t *testing.T) {
	pc, err := net.ListenPacket("udp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	defer pc.Close()
	// The replay pushes megabytes of snapshots at once: read beside it (well
	// beyond the kernel's default socket buffer) or most datagrams are
	// dropped before the test looks at them.
	if uc, ok := pc.(*net.UDPConn); ok {
		if err := uc.SetReadBuffer(16 << 20); err != nil {
			t.Logf("read buffer: %v", err)
		}
	}
	recv := make(chan []byte, 16384)
	go func() {
		buf := make([]byte, 65536)
		for {
			n, _, err := pc.ReadFrom(buf)
			if err != nil {
				return
			}
			recv <- append([]byte(nil), buf[:n]...)
		}
	}()

	var stdout, stderr bytes.Buffer
	code := run(context.Background(), []string{"dashboard-replay", walk2Dumps, "--to", pc.LocalAddr().String(), "--speed", "1000", "--max-gap", "50ms"}, &stdout, &stderr)
	if code != 0 {
		t.Fatalf("exit code = %d, want 0; stderr: %s", code, stderr.String())
	}
	// Let the reader take what the kernel still holds.
	time.Sleep(200 * time.Millisecond)

	var commanders, fights int
drain:
	for {
		select {
		case dg := <-recv:
			var obj map[string]json.RawMessage
			if err := json.Unmarshal(dg, &obj); err != nil {
				t.Fatalf("a replayed datagram is not a JSON object: %v", err)
			}
			var replay bool
			if err := json.Unmarshal(obj["replay"], &replay); err != nil || !replay {
				t.Fatalf("a replayed datagram has no replay:true (%s)", obj["replay"])
			}
			var typ string
			if err := json.Unmarshal(obj["type"], &typ); err != nil {
				t.Fatalf("type: %v", err)
			}
			switch typ {
			case "COMMANDERS":
				var p replayCommanders
				if err := json.Unmarshal(dg, &p); err != nil {
					t.Fatalf("COMMANDERS does not decode as the daemon's payload: %v", err)
				}
				if p.V != 12 || p.Session == 0 || p.Seq == 0 || p.TS == 0 || len(p.Commanders) == 0 {
					t.Errorf("COMMANDERS = v %d, session %d, seq %d, ts %d, %d commanders", p.V, p.Session, p.Seq, p.TS, len(p.Commanders))
				}
				commanders++
			case "COMBAT_FIGHT":
				var p replayFight
				if err := json.Unmarshal(dg, &p); err != nil {
					t.Fatalf("COMBAT_FIGHT does not decode as the daemon's payload: %v", err)
				}
				if p.V != 12 || p.Session == 0 || p.Seq == 0 || p.TS == 0 || len(p.Fight) == 0 {
					t.Errorf("COMBAT_FIGHT = v %d, session %d, seq %d, ts %d, %d fight bytes", p.V, p.Session, p.Seq, p.TS, len(p.Fight))
				}
				fights++
			}
		default:
			break drain
		}
	}
	if commanders == 0 || fights == 0 {
		t.Fatalf("received %d COMMANDERS and %d COMBAT_FIGHT, want at least one of each", commanders, fights)
	}
}
