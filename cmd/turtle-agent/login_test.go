package main

import (
	"bytes"
	"compress/zlib"
	"context"
	"io"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/TortoiseBots/turtle-agent/internal/auth"
	"github.com/TortoiseBots/turtle-agent/internal/capture"
	"github.com/TortoiseBots/turtle-agent/internal/config"
	"github.com/TortoiseBots/turtle-agent/internal/opcode"
	"github.com/TortoiseBots/turtle-agent/internal/packet"
	"github.com/TortoiseBots/turtle-agent/internal/worldconn"
	"github.com/TortoiseBots/turtle-agent/internal/worldconn/worldtest"
)

func settingsFile(t *testing.T, body string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "settings.json")
	if err := os.WriteFile(p, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	os.Chmod(p, 0o600)
	return p
}

// rpc port 0: the tests never collide with a live agent on the default 8765.
const unreachable = `{"realmd":"127.0.0.1:1","rpc":"127.0.0.1:0","accounts":{"harness":"pw-secret"}}`

func runCLI(args ...string) (int, string, string) {
	var out, errOut bytes.Buffer
	code := run(context.Background(), args, &out, &errOut)
	return code, out.String(), errOut.String()
}

func TestLoginUsage(t *testing.T) {
	if code, _, _ := runCLI("login"); code != exitUsage {
		t.Fatalf("code %d", code)
	}
}

func TestLoginAccountNotInSettings(t *testing.T) {
	code, _, errOut := runCLI("login", "--config", settingsFile(t, unreachable), "nobody", "Harnessb")
	if code != exitUsage || !strings.Contains(errOut, "nobody") || strings.Contains(errOut, "pw-secret") {
		t.Fatalf("code %d stderr %q", code, errOut)
	}
}

func TestLoginRealmdUnreachable(t *testing.T) {
	code, _, errOut := runCLI("login", "--config", settingsFile(t, unreachable), "harness", "Harnessb")
	if code != exitAuth || !strings.Contains(errOut, "realmd:") || strings.Contains(errOut, "pw-secret") {
		t.Fatalf("code %d stderr %q", code, errOut)
	}
}

// The world server hashes the unpadded key (39 bytes when K's high byte is zero), so the
// login path must hand worldconn.Dial Ticket.KeyBytes(), never Ticket.Key[:].
func TestDialWorldUsesKeyBytes(t *testing.T) {
	var tk auth.Ticket
	tk.Account = "HARNESS"
	for i := range tk.Key[:39] {
		tk.Key[i] = byte(i + 1)
	}
	tk.Key[39] = 0 // high byte of the little-endian K is zero
	if len(tk.KeyBytes()) != 39 {
		t.Fatalf("test setup: KeyBytes is %d bytes", len(tk.KeyBytes()))
	}
	old := dial
	t.Cleanup(func() { dial = old })
	var got []byte
	dial = func(_ context.Context, _, account string, key []byte, _ worldconn.Tap, _ ...worldconn.Option) (*worldconn.Conn, error) {
		got = append([]byte(nil), key...)
		return nil, context.Canceled
	}
	dialWorld(context.Background(), "x", &tk, nil)
	if !bytes.Equal(got, tk.KeyBytes()) || len(got) != 39 {
		t.Fatalf("dial got a %d byte key, want the 39 byte KeyBytes form", len(got))
	}
}

func TestScrubCheck(t *testing.T) {
	dir := t.TempDir()
	dirty, clean := filepath.Join(dir, "dirty.twcap"), filepath.Join(dir, "clean.twcap")
	w, _ := capture.Create(dirty)
	w.Packet(worldconn.ServerToClient, opcode.SMSGMessagechat, []byte("text HARNESS text"))
	w.Close()
	w, _ = capture.Create(clean)
	w.Packet(worldconn.ServerToClient, opcode.SMSGMessagechat, []byte("Harnessb says hi"))
	w.Close()
	cfg := settingsFile(t, unreachable)
	code, out, errOut := runCLI("scrub-check", "--config", cfg, dirty)
	if code != exitScrub || !strings.Contains(out, "record 0") || strings.Contains(out+errOut, "pw-secret") {
		t.Fatalf("dirty: %d %q", code, out)
	}
	if code, out, _ := runCLI("scrub-check", "--config", cfg, clean); code != exitOK || !strings.Contains(out, "clean, 1 records") {
		t.Fatalf("clean: %d %q", code, out)
	}
}

func TestScrubCheckFindsPasswordForms(t *testing.T) {
	path := filepath.Join(t.TempDir(), "p.twcap")
	w, _ := capture.Create(path)
	w.Packet(worldconn.ClientToServer, opcode.CMSGMessagechat, []byte("x PW-SECRET x"))
	w.Close()
	code, out, _ := runCLI("scrub-check", "--config", settingsFile(t, unreachable), path)
	if code != exitScrub || !strings.Contains(out, "a password") {
		t.Fatalf("%d %q", code, out)
	}
}

func TestScrubCheckShortSecretIsAnError(t *testing.T) {
	path := filepath.Join(t.TempDir(), "c.twcap")
	w, _ := capture.Create(path)
	w.Packet(worldconn.ServerToClient, opcode.SMSGMessagechat, []byte("hi"))
	w.Close()
	cfg := settingsFile(t, `{"realmd":"127.0.0.1:1","accounts":{"harness":"pw"}}`)
	code, out, errOut := runCLI("scrub-check", "--config", cfg, path)
	if code == exitOK || strings.Contains(out, "clean") || strings.Contains(out+errOut, "HARNESS") {
		t.Fatalf("%d %q %q", code, out, errOut)
	}
}

func TestCaptureStats(t *testing.T) {
	path := filepath.Join(t.TempDir(), "c.twcap")
	w, _ := capture.Create(path)
	w.Packet(worldconn.ServerToClient, opcode.SMSGCharEnum, []byte{0})
	w.Close()
	code, out, _ := runCLI("capture-stats", path)
	if code != exitOK || !strings.Contains(out, "SMSG_CHAR_ENUM") || !strings.Contains(out, "TOTAL") {
		t.Fatalf("%d %q", code, out)
	}
}

// The first login stores the fingerprint the server announces; the next one presents it.
func TestAddonOptionKeepsFingerprintBetweenLogins(t *testing.T) {
	st := config.Settings{FingerprintDir: t.TempDir()}
	f := &worldconn.FingerprintFile{Path: fingerprintPath(st, "harness")}
	if f.Load() != 0 {
		t.Fatal("fresh path holds a fingerprint")
	}
	if err := f.Save(0x7A6B5C4D); err != nil {
		t.Fatal(err)
	}
	var stderr bytes.Buffer
	opt := addonOption(st, "Harness", &stderr)
	// Apply the option to a Conn by authenticating against the scripted server.
	cli, srv := net.Pipe()
	defer cli.Close()
	defer srv.Close()
	ch := make(chan *worldtest.Server, 1)
	go func() {
		s, _ := worldtest.Accept(srv, bytes.Repeat([]byte{1, 2, 3, 4}, 10), worldtest.Options{})
		ch <- s
	}()
	if _, err := worldconn.Client(cli, "HARNESS", bytes.Repeat([]byte{1, 2, 3, 4}, 10), nil, opt); err != nil {
		t.Fatal(err)
	}
	s := <-ch
	r := packet.NewReader(s.AddonBlock)
	r.U32()
	zr, _ := zlib.NewReader(bytes.NewReader(r.Bytes(r.Remaining())))
	plain, _ := io.ReadAll(zr)
	if !bytes.Contains(plain, append([]byte("Blizzard_BindingUI\x00"), 0x4D)) {
		t.Fatalf("stored fingerprint not sent: %q", plain)
	}
}

// One account's fingerprint must not be presented by another: the core bans by a hash of
// fingerprint and IP, so a shared print would spread a ban. shared_fingerprint opts in.
func TestFingerprintFilePerAccount(t *testing.T) {
	st := config.Settings{FingerprintDir: "/cfg"}
	a, b := fingerprintPath(st, "alpha"), fingerprintPath(st, "beta")
	if a == b || a != fingerprintPath(st, "ALPHA") || strings.Contains(a, "alpha") {
		t.Fatalf("paths %q %q", a, b)
	}
	st.SharedFingerprint = true
	if got := fingerprintPath(st, "alpha"); got != "/cfg/addon-fingerprint" || got != fingerprintPath(st, "beta") {
		t.Fatalf("shared path %q", got)
	}
}
