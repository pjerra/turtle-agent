package main

// `raid-walk --practice` is the owner-approved test tool of the boss-mechanic practice runs (2026-11-07): it keeps one
// boss fight alive and resets the boss with GM tools between attempts. Its options refuse every shape the safety rules
// forbid — a practice without a placement, and a practice on a server that is not a test server — before anything is
// sent.

import (
	"bytes"
	"strings"
	"testing"
	"time"
)

// TestPracticeNeedsPlace: a practice run starts the raid at its boss row, so --from and --place are not optional.
func TestPracticeNeedsPlace(t *testing.T) {
	for _, args := range [][]string{
		{"zul_gurub", "--practice", "14834", "--from", "step 700"},
		{"zul_gurub", "--practice", "14834", "--place"},
		{"zul_gurub", "--practice", "14834"},
	} {
		var stderr bytes.Buffer
		if _, code := parseRaidWalkArgs(args, &stderr); code != exitUsage {
			t.Fatalf("%v: exit %d, want %d (%q)", args, code, exitUsage, stderr.String())
		}
		// Without --place the --place rule refuses first (it needs its own --from); with --place but no --from the
		// practice rule must be the one that speaks.
		if len(args) == 3 && strings.Contains(stderr.String(), "--place") && !strings.Contains(stderr.String(), "needs --place and --from") {
			t.Fatalf("%v: the refusal does not name the rule: %q", args, stderr.String())
		}
	}
}

// TestPracticeRefusedOffTestServer: GM sustain and reset only run where --gm-clear already may, so an unknown server
// (or any server outside placeServers) is refused at parse time.
func TestPracticeRefusedOffTestServer(t *testing.T) {
	var stderr bytes.Buffer
	_, code := parseRaidWalkArgs([]string{"zul_gurub", "--server", "not-a-test-server", "--place", "--from", "step 700",
		"--sql", "/zg.sql", "--practice", "14834"}, &stderr)
	if code != exitUsage {
		t.Fatalf("exit %d, want %d (%q)", code, exitUsage, stderr.String())
	}
	if !strings.Contains(stderr.String(), "test servers only") {
		t.Fatalf("the refusal does not name the rule: %q", stderr.String())
	}
}

// A well-formed practice is accepted, and its defaults are the documented ones.
func TestPracticeAcceptedWithDefaults(t *testing.T) {
	var stderr bytes.Buffer
	o, code := parseRaidWalkArgs([]string{"zul_gurub", "--server", "m910q", "--place", "--from", "step 700",
		"--sql", "/zg.sql", "--practice", "14834"}, &stderr)
	if code != exitOK {
		t.Fatalf("exit %d, want 0 (%q)", code, stderr.String())
	}
	if o.practiceEntry != 14834 || o.practiceCycles != 10 || o.practiceMax != 20*time.Minute ||
		o.practiceRest != 30*time.Second || o.practiceBossFloor != 40 || !o.practiceSustain ||
		o.practiceWindow != 120*time.Second {
		t.Fatalf("practice options %+v are not the documented defaults", o)
	}
	// --practice-sustain=off is the only way to turn the GM hands off.
	if o, code := parseRaidWalkArgs([]string{"zul_gurub", "--server", "m910q", "--place", "--from", "step 700",
		"--sql", "/zg.sql", "--practice", "14834", "--practice-sustain=off"}, &stderr); code != exitOK || o.practiceSustain {
		t.Fatalf("--practice-sustain=off: exit %d, sustain %v", code, o.practiceSustain)
	}
}

// A bad entry, cycle count, floor or window is a usage error.
func TestPracticeBadValues(t *testing.T) {
	base := []string{"zul_gurub", "--server", "m910q", "--place", "--from", "step 700", "--sql", "/zg.sql",
		"--practice", "14834"}
	for _, extra := range [][]string{
		{"--practice-cycles", "0"},
		{"--practice-boss-floor", "0"},
		{"--practice-boss-floor", "101"},
		{"--practice-window", "0s"},
	} {
		var stderr bytes.Buffer
		if _, code := parseRaidWalkArgs(append(append([]string{}, base...), extra...), &stderr); code != exitUsage {
			t.Fatalf("%v: exit %d, want %d (%q)", extra, code, exitUsage, stderr.String())
		}
	}
	for _, bad := range []string{"", "zero", "0"} {
		var stderr bytes.Buffer
		args := append(append([]string{}, base...), "--practice="+bad)
		if _, code := parseRaidWalkArgs(args, &stderr); code != exitUsage {
			t.Fatalf("--practice=%q: exit %d, want %d (%q)", bad, code, exitUsage, stderr.String())
		}
	}
}
