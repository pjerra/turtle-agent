package raidwalk

import (
	"path/filepath"
	"strings"
	"testing"
)

// The raid-walk --gm-instance-data test tool: one instance-data command per
// leg of the run, sent as `.debug instancedata` by the commander's GM chat
// path (the same gm RPC raid-walk's own gm() uses), in order, 3 s apart, once
// per launch (and again after a recovered wipe), when the walk first starts
// the named leg.
func TestParseGMInstanceData(t *testing.T) {
	sp, err := ParseGMInstanceData("hakkar:1 3;6 3;0 0")
	if err != nil || sp.Leg != "hakkar" || len(sp.Cmds) != 3 {
		t.Fatalf("%+v, %v", sp, err)
	}
	for i, want := range [][2]int{{1, 3}, {6, 3}, {0, 0}} {
		if sp.Cmds[i] != want {
			t.Fatalf("cmd %d = %v, want %v", i, sp.Cmds[i], want)
		}
	}
	// Whitespace around the flag value's separators is tolerated.
	sp, err = ParseGMInstanceData("  hakkar : 1 3 ; 6 3 ")
	if err != nil || sp.Leg != "hakkar" || len(sp.Cmds) != 2 {
		t.Fatalf("%+v, %v", sp, err)
	}
	for _, bad := range []string{"", "hakkar", "hakkar:", ":1 3", "hakkar:1", "hakkar:1 2 3", "hakkar:a b", "hakkar:1.5 3", "hakkar:1 x", "hakkar:;", "hakkar:1 3;;6 3"} {
		if _, err := ParseGMInstanceData(bad); err == nil {
			t.Errorf("ParseGMInstanceData(%q) accepted", bad)
		}
	}
}

func TestParseGMInstanceDataRefusals(t *testing.T) {
	// Unknown leg names are refused at validate against the rows, with exit 2
	// and a clear message; a malformed command (not two integers) is refused
	// the same way. Both are pinned at the command level in
	// cmd/turtle-agent (TestParseGMInstanceDataFlags).
	for _, bad := range []string{"hakkar:1", "hakkar:1 2 3"} {
		if _, err := ParseGMInstanceData(bad); err == nil {
			t.Errorf("ParseGMInstanceData(%q) accepted", bad)
		}
	}
	if _, err := ParseGMInstanceData("1 3"); err == nil {
		t.Errorf("ParseGMInstanceData without a leg accepted")
	}
}

func TestGMInstanceDataLabel(t *testing.T) {
	if got := GMInstanceDataLogLine("hakkar", [2]int{1, 3}, "sent"); got != "gm-instance-data: hakkar: .debug instancedata 1 3 -> sent" {
		t.Fatalf("log line %q", got)
	}
}

// LegMatch reports whether a run-status leg label starts the named leg: the
// server sends the full plan leg name (high_priest_thekal), the statuses the
// tests write shorten it (thekal). The walk matches the run's own lines, not
// operator words, so both spellings must agree.
func TestLegMatchesStatus(t *testing.T) {
	for _, tc := range []struct {
		leg, label string
		want       bool
	}{
		{"hakkar", "hakkar", true},
		{"high_priest_thekal", "high_priest_thekal", true},
		{"HAKKAR", "hakkar", true},
		{"hakkar", "hakkar_outer", false}, // a leg prefix is not its leg
		{"hakkar_outer", "hakkar", false},
		{"thekal", "high_priest_thekal", false}, // unknown leg names never validate
	} {
		if got := LegMatchesStatus(tc.leg, tc.label); got != tc.want {
			t.Errorf("LegMatchesStatus(%q, %q) = %v, want %v", tc.leg, tc.label, got, tc.want)
		}
	}
}

// InstanceMarks remembers which legs already fired in an instance: a marker
// per leg in the walk's out dir, one line per sent command. A --reuse-instance
// restart resumes the same out dir's markers, so a leg fired there never fires
// again; a leg whose marker has fewer lines than commands takes no new pass.
func TestInstanceMarksOncePerLeg(t *testing.T) {
	dir := t.TempDir()
	m := NewInstanceMarks(dir)
	sp := GMInstanceSpec{Leg: "hakkar", Cmds: [][2]int{{1, 3}, {6, 3}}}
	if m.LegDone("inst-7", "hakkar", sp) {
		t.Fatal("a fresh instance reports a leg done")
	}
	if err := m.Record("inst-7", "hakkar", [2]int{1, 3}); err != nil {
		t.Fatal(err)
	}
	if m.LegDone("inst-7", "hakkar", sp) {
		t.Fatal("a half-sent leg reports done")
	}
	if err := m.Record("inst-7", "hakkar", [2]int{6, 3}); err != nil {
		t.Fatal(err)
	}
	if !m.LegDone("inst-7", "hakkar", sp) {
		t.Fatal("a fully sent leg does not report done")
	}
	// Another instance starts clean, and another leg in the same one too.
	if m.LegDone("inst-8", "hakkar", sp) {
		t.Fatal("another instance reports the leg done")
	}
	if m.LegDone("inst-7", "jeklik", GMInstanceSpec{Leg: "jeklik", Cmds: [][2]int{{0, 0}}}) {
		t.Fatal("another leg reports done")
	}
	// The markers survive a process restart: the walk re-reads the out dir.
	m2 := NewInstanceMarks(dir)
	if !m2.LegDone("inst-7", "hakkar", sp) {
		t.Fatal("a restarted walk would resend a leg it already sent")
	}
	lines, err := m2.LegLog("inst-7", "hakkar")
	if err != nil || len(lines) != 2 || !strings.Contains(lines[0], ".debug instancedata 1 3") {
		t.Fatalf("leg log %+v, %v", lines, err)
	}
}

func TestInstanceMarksClear(t *testing.T) {
	dir := t.TempDir()
	m := NewInstanceMarks(dir)
	spec := GMInstanceSpec{Leg: "hakkar", Cmds: [][2]int{{1, 3}}}
	if err := m.Write("7", "hakkar", spec.Cmds); err != nil {
		t.Fatal(err)
	}
	if !m.LegDone("7", "hakkar", spec) {
		t.Fatal("the written marker must read back as done")
	}
	if err := m.Clear(); err != nil {
		t.Fatal(err)
	}
	if m.LegDone("7", "hakkar", spec) {
		t.Fatal("a cleared marker must not read back as done")
	}
	if err := NewInstanceMarks(filepath.Join(dir, "missing")).Clear(); err != nil {
		t.Fatalf("Clear on a missing dir: %v", err)
	}
}
