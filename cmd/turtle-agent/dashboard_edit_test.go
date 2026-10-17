package main

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/pjerra/turtle-agent/internal/dashboard"
	"github.com/pjerra/turtle-agent/internal/navmesh/navfixture"
)

func editSetup(t *testing.T) (p *dashboard.Poller, cfg, src, edits, tokens, mmaps string) {
	t.Helper()
	root := t.TempDir()
	cfg, src, edits, tokens = filepath.Join(root, "config"), filepath.Join(root, "src"), filepath.Join(root, "edits"), filepath.Join(root, "tokens")
	for _, d := range []string{cfg, src} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(tokens, []byte("owner "+strings.Repeat("k", 40)+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	mmaps = navfixture.WriteDir(t, 209, navfixture.LCorridor(0))
	return dashboard.NewPoller(dashboard.Options{ConfigDir: cfg}), cfg, src, edits, tokens, mmaps
}

func TestDashboardEditSetupRefusesWhatIsNotSafe(t *testing.T) {
	p, cfg, src, edits, tokens, mmaps := editSetup(t)
	ed, planner, err := newEditor(p, src, edits, tokens, mmaps, nil)
	if err != nil {
		t.Fatal(err)
	}
	planner.Close()
	if ed == nil {
		t.Fatal("no editor")
	}
	cases := map[string]func() error{
		"no mmaps folder": func() error { _, _, err := newEditor(p, src, edits, tokens, "", nil); return err },
		"mmaps is a file": func() error { _, _, err := newEditor(p, src, edits, tokens, tokens, nil); return err },
		"no source":       func() error { _, _, err := newEditor(p, "", edits, tokens, mmaps, nil); return err },
		"missing tokens":  func() error { _, _, err := newEditor(p, src, edits, tokens+"x", mmaps, nil); return err },
		"open tokens": func() error {
			if err := os.Chmod(tokens, 0o644); err != nil {
				t.Fatal(err)
			}
			defer os.Chmod(tokens, 0o600)
			_, _, err := newEditor(p, src, edits, tokens, mmaps, nil)
			return err
		},
		"edits in a slot's folder": func() error {
			if err := os.WriteFile(filepath.Join(cfg, "settings-1.json"), []byte(`{"rpc":"127.0.0.1:8765","dungeons_dir":`+`"`+filepath.Dir(edits)+`"}`), 0o600); err != nil {
				t.Fatal(err)
			}
			defer os.Remove(filepath.Join(cfg, "settings-1.json"))
			_, _, err := newEditor(p, src, edits, tokens, mmaps, nil)
			return err
		},
	}
	for name, f := range cases {
		if err := f(); err == nil {
			t.Errorf("%s: the editor started", name)
		}
	}
}

func TestDashboardEditFlagNeedsItsParts(t *testing.T) {
	_, cfg, _, _, tokens, _ := editSetup(t)
	for name, args := range map[string][]string{
		"no mmaps":  {"--edit", "--config-dir", cfg, "--edit-tokens", tokens, "--listen", "127.0.0.1:0"},
		"no tokens": {"--edit", "--config-dir", cfg, "--edit-tokens", filepath.Join(cfg, "none"), "--mmaps-dir", cfg, "--listen", "127.0.0.1:0"},
	} {
		var out, errb bytes.Buffer
		if code := dashboardCmd(context.Background(), args, &out, &errb); code != exitUsage || !strings.Contains(errb.String(), "--edit") {
			t.Errorf("%s: exit %d: %s", name, code, errb.String())
		}
		if strings.Contains(errb.String(), strings.Repeat("k", 40)) || strings.Contains(out.String(), strings.Repeat("k", 40)) {
			t.Errorf("%s: a token was printed", name)
		}
	}
}
