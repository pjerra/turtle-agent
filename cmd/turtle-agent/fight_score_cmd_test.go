package main

import (
	"bytes"
	"context"
	"strings"
	"testing"
)

func scoreLines(s string) []string { return strings.Split(strings.TrimSpace(s), "\n") }

// scoreLine finds the one output line that holds every part.
func scoreLine(t *testing.T, out string, parts ...string) string {
	t.Helper()
	for _, l := range scoreLines(out) {
		ok := true
		for _, p := range parts {
			if !strings.Contains(l, p) {
				ok = false
				break
			}
		}
		if ok {
			return l
		}
	}
	t.Fatalf("no line with %v in\n%s", parts, out)
	return ""
}

// TestFightScoreWalk2Parity reproduces the fight debugger's pinned walk-2 numbers (TortoiseBots commander-fight-debugger.md §2):
// 5-4 main tank Ragemeister cast Sunder Armor 8 times in 39 s (2.0 per 10 s, ok), off-tanks Brun 2x and Irelanthe 3x taunted
// (ok); 5-3 Sunder is 7 in 52 s (1.3 per 10 s, warn) and Irelanthe never taunted (warn). The files are copies of
// ~/TortoiseBots-audit/tests/raid-ony-walk2/combat-fight-5-4.json and -5-3.json.
func TestFightScoreWalk2Parity(t *testing.T) {
	var out, errOut bytes.Buffer
	code := run(context.Background(), []string{"fight-score", "--mt", "Ragemeister", "--ot", "Brun,Irelanthe", "testdata/fights/combat-fight-5-4.json"}, &out, &errOut)
	if code != exitOK {
		t.Fatalf("exit %d: %s", code, errOut.String())
	}
	if l := scoreLine(t, out.String(), "mt_sunder", "2.0/10 s"); !strings.Contains(l, "ok") || !strings.Contains(l, "Sunder Armor 8x") {
		t.Fatalf("mt_sunder: %q", l)
	}
	if l := scoreLine(t, out.String(), "ot_taunt", "Brun"); !strings.Contains(l, "ok") || !strings.Contains(l, "2x") || !strings.Contains(l, "Hand of Reckoning 2x") {
		t.Fatalf("ot_taunt Brun: %q", l)
	}
	if l := scoreLine(t, out.String(), "ot_taunt", "Irelanthe"); !strings.Contains(l, "ok") || !strings.Contains(l, "3x") || !strings.Contains(l, "Growl 3x") {
		t.Fatalf("ot_taunt Irelanthe: %q", l)
	}

	out.Reset()
	errOut.Reset()
	code = run(context.Background(), []string{"fight-score", "--mt", "Ragemeister", "--ot", "Brun,Irelanthe", "testdata/fights/combat-fight-5-3.json"}, &out, &errOut)
	if code != exitOK {
		t.Fatalf("exit %d: %s", code, errOut.String())
	}
	if l := scoreLine(t, out.String(), "mt_sunder", "1.3/10 s"); !strings.Contains(l, "warn") {
		t.Fatalf("mt_sunder: %q", l)
	}
	if l := scoreLine(t, out.String(), "ot_taunt", "Irelanthe"); !strings.Contains(l, "warn") || !strings.Contains(l, "0x") || !strings.Contains(l, "no taunt") {
		t.Fatalf("ot_taunt Irelanthe: %q", l)
	}

	// No argument is a usage error.
	if code := run(context.Background(), []string{"fight-score"}, &out, &errOut); code != exitUsage {
		t.Fatalf("no arguments: exit %d", code)
	}
}
