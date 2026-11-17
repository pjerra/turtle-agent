package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/TortoiseBots/turtle-agent/internal/events"
	"github.com/TortoiseBots/turtle-agent/internal/game"
	"github.com/TortoiseBots/turtle-agent/internal/game/gametest"
	"github.com/TortoiseBots/turtle-agent/internal/opcode"
	"github.com/TortoiseBots/turtle-agent/internal/packet"
	"github.com/TortoiseBots/turtle-agent/internal/session"
	"github.com/TortoiseBots/turtle-agent/internal/worldconn"
)

type capRec struct {
	dir  worldconn.Direction
	at   time.Time
	op   uint16
	body []byte
}

// writeCapture writes a TWCAP1 file with the given records, times and all.
func writeCapture(t *testing.T, recs ...capRec) string {
	t.Helper()
	w := packet.NewWriter().Append([]byte("TWCAP1\n"))
	for _, r := range recs {
		w.U8(uint8(r.dir)).U64(uint64(r.at.UnixNano())).U16(r.op).U32(uint32(len(r.body))).Append(r.body)
	}
	path := filepath.Join(t.TempDir(), "agent.cap")
	if err := os.WriteFile(path, w.Bytes(), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

// sessionCapture is a login, two monster yells ten seconds apart, and a logout.
func sessionCapture(t *testing.T) (string, time.Time) {
	t.Helper()
	t0 := time.Date(2026, 10, 12, 20, 0, 0, 0, time.UTC)
	enum := packet.NewWriter().U8(1).GUID(0x2A).CString("Harnessb").Append(make([]byte, session.CharEnumTailLen)).Bytes()
	yell := func(msg string) []byte {
		return gametest.ChatPacket(game.ChatMonsterYell, game.LangUniversal, 0xF130000001000001, "Hogger", 0, "", msg, 0)
	}
	verify := packet.NewWriter().U32(0).F32(1).F32(2).F32(3).F32(0).Bytes()
	return writeCapture(t,
		capRec{worldconn.ServerToClient, t0, opcode.SMSGCharEnum, enum},
		capRec{worldconn.ClientToServer, t0.Add(time.Millisecond), opcode.CMSGPlayerLogin, packet.NewWriter().GUID(0x2A).Bytes()},
		capRec{worldconn.ServerToClient, t0.Add(2 * time.Millisecond), opcode.SMSGLoginVerifyWorld, verify},
		capRec{worldconn.ServerToClient, t0.Add(10 * time.Second), opcode.SMSGMessagechat, yell("Grrr")},
		capRec{worldconn.ServerToClient, t0.Add(20 * time.Second), opcode.SMSGMessagechat, yell("Rawr")},
		capRec{worldconn.ServerToClient, t0.Add(21 * time.Second), opcode.SMSGLogoutComplete, nil},
	), t0
}

func decode(t *testing.T, out string) []events.Event {
	t.Helper()
	var evs []events.Event
	for _, line := range strings.Split(strings.TrimSpace(out), "\n") {
		var e events.Event
		if err := json.Unmarshal([]byte(line), &e); err != nil {
			t.Fatalf("%q: %v", line, err)
		}
		evs = append(evs, e)
	}
	return evs
}

func TestCaptureEventsPrintsOneJSONEventPerLine(t *testing.T) {
	path, t0 := sessionCapture(t)
	code, out, errOut := runCLI("capture", "events", path)
	if code != exitOK {
		t.Fatalf("%d %q", code, errOut)
	}
	evs := decode(t, out)
	if len(evs) != 4 || evs[0].Kind != events.KindSession || evs[1].Kind != events.KindChat || evs[3].Data["state"] != "logged_out" {
		t.Fatalf("%+v", evs)
	}
	for i, e := range evs {
		if e.Seq != uint64(i+1) {
			t.Fatalf("event %d has seq %d", i, e.Seq)
		}
	}
	if !evs[1].Time.Equal(t0.Add(10*time.Second)) || evs[1].Source != "Hogger" || evs[1].Text != "Grrr" {
		t.Fatalf("chat %+v", evs[1])
	}
	if !strings.Contains(errOut, "4 of 4 events for Harnessb") {
		t.Fatalf("summary %q", errOut)
	}
}

func TestCaptureEventsFilters(t *testing.T) {
	path, t0 := sessionCapture(t)
	_, out, _ := runCLI("capture", "events", path, "--kinds", "chat")
	if evs := decode(t, out); len(evs) != 2 || evs[0].Seq != 2 || evs[1].Seq != 3 {
		t.Fatalf("--kinds: %+v", evs)
	}
	// Flags may also come first; --from is inclusive, --to too.
	_, out, _ = runCLI("capture", "events", "--from", t0.Add(15*time.Second).Format(time.RFC3339), "--to", t0.Add(20*time.Second).Format(time.RFC3339), path)
	if evs := decode(t, out); len(evs) != 1 || evs[0].Text != "Rawr" {
		t.Fatalf("--from/--to: %+v", evs)
	}
	_, out, _ = runCLI("capture", "events", "--from", t0.Add(time.Hour).Format(time.RFC3339), path)
	if strings.TrimSpace(out) != "" {
		t.Fatalf("an empty window printed %q", out)
	}
}

func TestCaptureEventsOut(t *testing.T) {
	path, _ := sessionCapture(t)
	dest := filepath.Join(t.TempDir(), "events.jsonl")
	code, out, _ := runCLI("capture", "events", "--out", dest, path)
	if code != exitOK || out != "" {
		t.Fatalf("%d %q", code, out)
	}
	raw, err := os.ReadFile(dest)
	if err != nil {
		t.Fatal(err)
	}
	if evs := decode(t, string(raw)); len(evs) != 4 {
		t.Fatalf("%d events in the file", len(evs))
	}
	if fi, _ := os.Stat(dest); fi.Mode().Perm() != 0o600 {
		t.Fatalf("mode %v", fi.Mode())
	}
}

// --out never wipes a capture and never replaces a file without --force.
func TestCaptureEventsOutIsCareful(t *testing.T) {
	path, _ := sessionCapture(t)
	before, _ := os.ReadFile(path)
	other, _ := sessionCapture(t)
	dir := t.TempDir()
	plain := filepath.Join(dir, "events.jsonl")
	if err := os.WriteFile(plain, []byte("old\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	for _, out := range []string{path, other, plain} {
		if code, _, _ := runCLI("capture", "events", "--out", out, path); code != exitUsage {
			t.Errorf("--out %s: exit %d", out, code)
		}
		if code, _, _ := runCLI("capture", "events", "--out", out, "--force", path); out != plain && code != exitUsage {
			t.Errorf("--force over a capture, %s: exit %d", out, code)
		}
	}
	if after, _ := os.ReadFile(path); string(after) != string(before) {
		t.Fatal("the capture was changed")
	}
	// With --force a plain file is replaced, and ends up mode 0600.
	if code, _, errOut := runCLI("capture", "events", "--out", plain, "--force", path); code != exitOK {
		t.Fatalf("%d %q", code, errOut)
	}
	raw, _ := os.ReadFile(plain)
	if fi, _ := os.Stat(plain); len(decode(t, string(raw))) != 4 || fi.Mode().Perm() != 0o600 {
		t.Fatalf("%q %v", raw, fi.Mode())
	}
}

func TestCaptureEventsErrors(t *testing.T) {
	path, _ := sessionCapture(t)
	for _, args := range [][]string{
		{"capture"},
		{"capture", "events"},
		{"capture", "events", path, path},
		{"capture", "events", "--from", "yesterday", path},
		{"capture", "events", "--to", "2026-10-12", path},
		{"capture", "events", filepath.Join(t.TempDir(), "missing.cap")},
		{"capture", "events", writeCapture(t)}, // no login in it
	} {
		if code, _, _ := runCLI(args...); code != exitUsage {
			t.Errorf("%v: exit %d", args, code)
		}
	}
}

// A capture of an agent that was killed ends in a cut-off record: the records before it decode.
func TestCaptureEventsCutOffCapture(t *testing.T) {
	path, _ := sessionCapture(t)
	raw, _ := os.ReadFile(path)
	cut := filepath.Join(t.TempDir(), "cut.cap")
	if err := os.WriteFile(cut, raw[:len(raw)-5], 0o600); err != nil {
		t.Fatal(err)
	}
	code, out, errOut := runCLI("capture", "events", cut)
	if code != exitOK || len(decode(t, out)) != 3 || !strings.Contains(errOut, "warning") {
		t.Fatalf("%d %q %q", code, out, errOut)
	}
}
