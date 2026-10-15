package main

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/pjerra/turtle-agent/internal/dungeon"
)

func writePulltest(t *testing.T, dir, name string, mod func(*dungeon.PulltestReport)) string {
	t.Helper()
	r := &dungeon.PulltestReport{Version: dungeon.PulltestVersion, Plan: "uldaman", Leg: 2, LegName: "ironaya", PullGroup: 1, Entries: []uint32{7}, TriesAsked: 3, Reset: "auto", Healer: "all mana users", GMCommands: []string{},
		Tries: []dungeon.PulltestTry{
			{Try: 1, PullGroup: 1, Reset: "first spawn", Outcome: dungeon.TryKilled, TimeToKillS: 30, RestS: 10, HealerMinManaPct: 60},
			{Try: 2, PullGroup: 1, Reset: "respawn", Outcome: dungeon.TryKilled, TimeToKillS: 40, RestS: 20, HealerMinManaPct: 50, Deaths: 1},
			{Try: 3, PullGroup: 1, Reset: "respawn", Outcome: dungeon.TryKilled, TimeToKillS: 35, RestS: 12, HealerMinManaPct: 55, ThreatOverrides: 1}}}
	if mod != nil {
		mod(r)
	}
	r.Summarize()
	b, err := json.MarshalIndent(r, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, name)
	if err := os.WriteFile(path, b, 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestPulltestCommand(t *testing.T) {
	dir := t.TempDir()
	a := writePulltest(t, dir, "a.json", nil)
	b := writePulltest(t, dir, "b.json", func(r *dungeon.PulltestReport) {
		r.Tries[0].TimeToKillS, r.Tries[1].TimeToKillS, r.Tries[2].TimeToKillS = 20, 22, 21
		r.Tries[1].Deaths = 0
	})
	var out, errOut bytes.Buffer
	if code := run(context.Background(), []string{"pulltest", a}, &out, &errOut); code != exitOK {
		t.Fatalf("exit %d: %s", code, errOut.String())
	}
	for _, want := range []string{"pulltest uldaman leg 2 (ironaya) pull group 1 [7]", "first spawn", "respawn", "sum  killed 3/3", "ttk mean 35.0 max 40.0"} {
		if !strings.Contains(out.String(), want) {
			t.Fatalf("lacks %q:\n%s", want, out.String())
		}
	}
	out.Reset()
	if code := run(context.Background(), []string{"pulltest", "--compare", a, b}, &out, &errOut); code != exitOK {
		t.Fatalf("exit %d: %s", code, errOut.String())
	}
	for _, want := range []string{"time to kill, mean s", "-14.0", "better", "deaths", "-1.0"} {
		if !strings.Contains(out.String(), want) {
			t.Fatalf("compare lacks %q:\n%s", want, out.String())
		}
	}
	// Usage errors: no file, one file with --compare, a file that is not a result, a file that is not there.
	junk := filepath.Join(dir, "junk.json")
	if err := os.WriteFile(junk, []byte(`{"state":"done"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	for _, args := range [][]string{{"pulltest"}, {"pulltest", "--compare", a}, {"pulltest", junk}, {"pulltest", filepath.Join(dir, "none.json")}} {
		if code := run(context.Background(), args, &out, &errOut); code != exitUsage {
			t.Errorf("%v: exit %d", args, code)
		}
	}
}
