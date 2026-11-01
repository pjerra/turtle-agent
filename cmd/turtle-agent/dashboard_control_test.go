package main

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
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
	for name, args := range map[string][]string{
		"trust without the controls": {"--control-trust-local", "--config-dir", cfg, "--listen", "127.0.0.1:0"},
		"a wildcard listen":          {"--control", "--control-tokens", tokens, "--config-dir", cfg, "--listen", "0.0.0.0:8790"},
		"a LAN listen":               {"--control", "--control-tokens", tokens, "--config-dir", cfg, "--listen", "192.168.1.5:8790"},
		"a missing token file":       {"--control", "--control-tokens", filepath.Join(cfg, "none"), "--config-dir", cfg, "--listen", "127.0.0.1:0"},
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
