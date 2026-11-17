package main

import (
	"bytes"
	"context"
	"encoding/hex"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/TortoiseBots/turtle-agent/internal/auth"
	"github.com/TortoiseBots/turtle-agent/internal/capture"
	"github.com/TortoiseBots/turtle-agent/internal/opcode"
	"github.com/TortoiseBots/turtle-agent/internal/packet"
	"github.com/TortoiseBots/turtle-agent/internal/worldconn/worldtest"
)

// e2eChars builds SMSG_CHAR_ENUM with one character (Player::BuildEnumData layout).
func e2eChars(name string) []byte {
	w := packet.NewWriter().U8(1).GUID(0x2A).CString(name).U8(1).U8(1).U8(0)
	w.U8(1).U8(2).U8(3).U8(4).U8(5).U8(60).U32(12).U32(0).F32(-8913).F32(-136).F32(82).U32(0).U32(0).U8(0)
	w.U32(0).U32(0).U32(0)
	for slot := 0; slot < 20; slot++ {
		w.U32(uint32(1000 + slot)).U8(uint8(slot))
	}
	return w.Bytes()
}

// stubLogin replaces realmd with a ticket whose key has a zero high byte.
func stubLogin(t *testing.T, tk *auth.Ticket, calls *int) {
	old := authLogin
	t.Cleanup(func() { authLogin = old })
	authLogin = func(context.Context, string, string, string) (*auth.Ticket, error) {
		*calls++
		return tk, nil
	}
}

func TestLoginCaptureEndToEndLeaksNoSecret(t *testing.T) {
	const password = "pw-secret"
	tk := &auth.Ticket{Account: "HARNESS", Realms: []auth.Realm{{Name: "Tortoise", Address: "127.0.0.1:1"}}}
	for i := range tk.Key {
		tk.Key[i] = byte(0x21 + i)
	}
	tk.Key[39] = 0
	key := tk.KeyBytes()
	if len(key) != 39 {
		t.Fatalf("setup: key is %d bytes", len(key))
	}
	var calls int
	stubLogin(t, tk, &calls)

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { ln.Close() })
	served := make(chan error, 1)
	go func() {
		served <- func() error {
			nc, err := ln.Accept()
			if err != nil {
				return err
			}
			defer nc.Close()
			srv, err := worldtest.Accept(nc, key, worldtest.Options{})
			if err != nil {
				return err
			}
			defer srv.Close()
			step := func(op uint16) error { _, err := srv.Expect(op, 5*time.Second); return err }
			if err := step(opcode.CMSGCharEnum); err != nil {
				return err
			}
			srv.Send(opcode.SMSGCharEnum, e2eChars("Harnessb"))
			if err := step(opcode.CMSGPlayerLogin); err != nil {
				return err
			}
			srv.Send(opcode.SMSGLoginVerifyWorld, packet.NewWriter().U32(0).F32(-8913).F32(-136).F32(82).F32(1.5).Bytes())
			if err := step(opcode.CMSGSetActiveMover); err != nil {
				return err
			}
			// A server packet that carries every secret form, so the capture must redact it.
			leak := []byte("HARNESS harness " + password + " " + hex.EncodeToString(key))
			leak = append(leak, tk.Key[:]...)
			leak = append(leak, key...)
			srv.Send(opcode.SMSGMessagechat, leak)
			if err := step(opcode.CMSGLogoutRequest); err != nil {
				return err
			}
			srv.Send(opcode.SMSGLogoutResponse, packet.NewWriter().U32(0).U8(1).Bytes())
			srv.Send(opcode.SMSGLogoutComplete, nil)
			return nil
		}()
	}()

	dir := t.TempDir()
	cfg := settingsFile(t, `{"realmd":"127.0.0.1:1","world_addr":"`+ln.Addr().String()+`","rpc":"127.0.0.1:0","accounts":{"harness":"`+password+`"}}`)
	capPath := filepath.Join(dir, "e2e.twcap")
	code, _, errOut := runCLI("login", "--config", cfg, "--capture", capPath, "--stay", "200ms", "harness", "Harnessb")
	if code != exitOK || !strings.Contains(errOut, "in world: Harnessb") || !strings.Contains(errOut, "logout: complete") ||
		!strings.Contains(errOut, "rpc: listening on http://127.0.0.1:") {
		t.Fatalf("code %d stderr %q", code, errOut)
	}
	if err := <-served; err != nil {
		t.Fatal(err)
	}
	if strings.Contains(errOut, ", 0 redacted") {
		t.Fatalf("expected at least one redacted record: %q", errOut)
	}

	secrets := [][]byte{[]byte("HARNESS"), []byte("harness"), []byte(password), tk.Key[:], key, []byte(hex.EncodeToString(key))}
	for _, s := range secrets {
		if strings.Contains(errOut, string(s)) {
			t.Errorf("stderr leaks a secret form %q", s)
		}
	}
	recs, err := capture.ReadFile(capPath)
	if err != nil {
		t.Fatal(err)
	}
	found, err := capture.Scan(recs, secrets...)
	if err != nil || len(found) != 0 {
		t.Fatalf("scan: %v %v", found, err)
	}
	raw, err := os.ReadFile(capPath)
	if err != nil {
		t.Fatal(err)
	}
	for _, s := range secrets {
		for _, v := range capture.Variants(s) {
			if bytes.Contains(raw, v) {
				t.Errorf("capture file contains secret form %q", v)
			}
		}
	}
}

func TestLoginBadCaptureNeverSpendsARealmdAttempt(t *testing.T) {
	var calls int
	stubLogin(t, &auth.Ticket{}, &calls)
	existing := filepath.Join(t.TempDir(), "old.twcap")
	if err := os.WriteFile(existing, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg := settingsFile(t, unreachable)
	if code, _, errOut := runCLI("login", "--config", cfg, "--capture", existing, "harness", "Harnessb"); code != exitUsage || !strings.Contains(errOut, "already exists") {
		t.Fatalf("existing: %d %q", code, errOut)
	}
	short := settingsFile(t, `{"realmd":"127.0.0.1:1","rpc":"127.0.0.1:0","accounts":{"harness":"pw"}}`)
	if code, _, errOut := runCLI("login", "--config", short, "--capture", filepath.Join(t.TempDir(), "n.twcap"), "harness", "Harnessb"); code != exitUsage || strings.Contains(errOut, "pw\n") {
		t.Fatalf("short: %d %q", code, errOut)
	}
	if calls != 0 {
		t.Fatalf("realmd login ran %d times", calls)
	}
}

func TestLoginErrorsHaveOnePrefix(t *testing.T) {
	_, _, errOut := runCLI("login", "--config", settingsFile(t, unreachable), "harness", "Harnessb")
	if strings.Contains(errOut, "realmd: realmd:") {
		t.Fatalf("doubled prefix: %q", errOut)
	}
}

func TestLoginUsageAdvertisesRPC(t *testing.T) {
	_, _, errOut := runCLI()
	if !strings.Contains(errOut, "[--rpc ADDR]") {
		t.Fatalf("usage lacks --rpc: %q", errOut)
	}
}

func TestLoginBadRPCAddressNeverSpendsARealmdAttempt(t *testing.T) {
	var calls int
	stubLogin(t, &auth.Ticket{}, &calls)
	cfg := settingsFile(t, unreachable)
	busy, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer busy.Close()
	for _, addr := range []string{"0.0.0.0:8765", "192.168.1.10:8765", "example.com:8765", "8765", "127.0.0.1:65536", busy.Addr().String()} {
		if code, _, errOut := runCLI("login", "--config", cfg, "--rpc", addr, "harness", "Harnessb"); code != exitUsage || !strings.Contains(errOut, "rpc: ") {
			t.Errorf("%s: %d %q", addr, code, errOut)
		}
	}
	if calls != 0 {
		t.Fatalf("realmd login ran %d times", calls)
	}
}

// login <character> takes the account from a settings file that holds exactly one, so the
// account name need not appear on the command line; with several it is a usage error.
func TestLoginAccountFromSettings(t *testing.T) {
	var calls int
	stubLogin(t, &auth.Ticket{}, &calls)
	if code, _, errOut := runCLI("login", "--config", settingsFile(t, unreachable), "Harnessb"); strings.Contains(errOut, "usage:") {
		t.Fatalf("one account, one argument: %d %q", code, errOut)
	}
	two := settingsFile(t, `{"realmd":"127.0.0.1:1","rpc":"127.0.0.1:0","accounts":{"harness":"pw-secret","other":"pw-two"}}`)
	code, _, errOut := runCLI("login", "--config", two, "Harnessb")
	if code != exitUsage || strings.Contains(errOut, "harness") || strings.Contains(errOut, "other") {
		t.Fatalf("two accounts: %d %q", code, errOut)
	}
	if calls != 1 { // only the one-account call reached realmd
		t.Fatalf("realmd login ran %d times", calls)
	}
}
