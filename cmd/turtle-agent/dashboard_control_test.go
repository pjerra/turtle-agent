package main

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/pjerra/turtle-agent/internal/routeedit"
)

// TestDashboardControlFlags: the flags that must stop the dashboard before it serves, and that no token is ever printed.
func TestDashboardControlFlags(t *testing.T) {
	root := t.TempDir()
	cfg := filepath.Join(root, "config")
	if err := os.MkdirAll(cfg, 0o755); err != nil {
		t.Fatal(err)
	}
	token := strings.Repeat("k", 40)
	tokens := filepath.Join(root, "control-tokens")
	if err := os.WriteFile(tokens, []byte("owner "+token+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	// the route editor's file shares the control token (L2): a token that may edit routes must never press run buttons
	sharedEdit := filepath.Join(root, "edit-tokens")
	if err := os.WriteFile(sharedEdit, []byte("owner "+token+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	for name, args := range map[string][]string{
		"trust without the controls": {"--control-trust-local", "--config-dir", cfg, "--listen", "127.0.0.1:0"},
		"a wildcard listen":          {"--control", "--control-tokens", tokens, "--config-dir", cfg, "--listen", "0.0.0.0:8790"},
		"a LAN listen":               {"--control", "--control-tokens", tokens, "--config-dir", cfg, "--listen", "192.168.1.5:8790"},
		"a missing token file":       {"--control", "--control-tokens", filepath.Join(cfg, "none"), "--config-dir", cfg, "--listen", "127.0.0.1:0"},
		"the editor's token file":    {"--control", "--control-tokens", tokens, "--edit-tokens", tokens, "--config-dir", cfg, "--listen", "127.0.0.1:0"},
		"a token in both files":      {"--control", "--control-tokens", tokens, "--edit-tokens", sharedEdit, "--config-dir", cfg, "--listen", "127.0.0.1:0"},
	} {
		var out, errb bytes.Buffer
		if code := dashboardCmd(context.Background(), args, &out, &errb); code != exitUsage {
			t.Errorf("%s: exit %d: %s", name, code, errb.String())
		}
		if !strings.Contains(errb.String(), "--control") {
			t.Errorf("%s: the error does not name the flag: %s", name, errb.String())
		}
		if strings.Contains(errb.String(), token) || strings.Contains(out.String(), token) {
			t.Errorf("%s: a token was printed", name)
		}
	}
}

// L2: the controls refuse the editor's token file by path and by a shared token, and take two files of their own.
func TestCheckControlTokens(t *testing.T) {
	root := t.TempDir()
	token := strings.Repeat("c", 40)
	control := filepath.Join(root, "control-tokens")
	edit := filepath.Join(root, "edit-tokens")
	for p, body := range map[string]string{control: "owner " + token + "\n", edit: "owner " + strings.Repeat("e", 40) + "\n"} {
		if err := os.WriteFile(p, []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	ctl, err := routeedit.LoadTokens(control)
	if err != nil {
		t.Fatal(err)
	}
	if err := checkControlTokens(control, ctl, edit); err != nil {
		t.Fatalf("two files of their own: %v", err)
	}
	if err := checkControlTokens(control, ctl, filepath.Join(root, "absent")); err != nil {
		t.Fatalf("no editor token file at all: %v", err)
	}
	if err := checkControlTokens(control, ctl, control); err == nil || !strings.Contains(err.Error(), "file of their own") {
		t.Fatalf("the editor's own file: %v", err)
	}
	if err := os.WriteFile(edit, []byte("editor "+token+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := checkControlTokens(control, ctl, edit); err == nil || !strings.Contains(err.Error(), "must not press run buttons") {
		t.Fatalf("a token in both files: %v", err)
	}
}
