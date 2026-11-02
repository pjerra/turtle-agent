package main

import (
	"bytes"
	"context"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/pjerra/turtle-agent/internal/dungeon"
	"github.com/pjerra/turtle-agent/internal/raidwalk"
)

// capWalk builds a walk on a fake clock whose sleep advances it, with a ledger of n entries aged by ages.
func capWalk(t *testing.T, opts raidWalkOptions, ages ...time.Duration) (*walk, *time.Time, *[]time.Duration, *bytes.Buffer) {
	t.Helper()
	now := time.Date(2026, 11, 3, 12, 0, 0, 0, time.UTC)
	path := filepath.Join(t.TempDir(), raidwalk.InstanceCapFile)
	if len(ages) > 0 {
		err := raidwalk.UpdateCapLedger(path, func(l *raidwalk.CapLedger) {
			for _, a := range ages {
				l.Record("m910q", raidwalk.CapEntry{At: now.Add(-a)}, now)
			}
		})
		if err != nil {
			t.Fatal(err)
		}
	}
	var slept []time.Duration
	var stderr bytes.Buffer
	opts.server = "m910q"
	w := &walk{opts: opts, stderr: &stderr, plan: &dungeon.Plan{Name: "zg"}}
	w.env = walkEnv{
		now: func() time.Time { return now },
		sleep: func(_ context.Context, d time.Duration) error {
			slept = append(slept, d)
			now = now.Add(d)
			return nil
		},
		capLedger: path,
	}
	return w, &now, &slept, &stderr
}

func TestInstanceCapBelowFourGoesOn(t *testing.T) {
	w, _, slept, stderr := capWalk(t, raidWalkOptions{capWait: 20 * time.Minute}, 5*time.Minute, 10*time.Minute, 15*time.Minute)
	if err := w.phaseInstanceCap(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(*slept) != 0 || stderr.Len() != 0 {
		t.Fatalf("3 in the hour must not wait or warn: slept %v, stderr %q", *slept, stderr.String())
	}
}

func TestInstanceCapWaitsForTheOldestToAgeOut(t *testing.T) {
	w, now, slept, stderr := capWalk(t, raidWalkOptions{capWait: 20 * time.Minute},
		55*time.Minute, 30*time.Minute, 20*time.Minute, 10*time.Minute)
	if err := w.phaseInstanceCap(context.Background()); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(stderr.String(), "instance cap: 4 fresh instances in the last hour on m910q; the 6th is refused by the core") {
		t.Fatalf("warning missing: %q", stderr.String())
	}
	if len(*slept) != 1 || (*slept)[0] != 5*time.Minute+capWaitMargin {
		t.Fatalf("slept %v, want one wait of 5m + margin", *slept)
	}
	l, _ := raidwalk.LoadCapLedger(w.env.capLedger)
	if n := l.Count("m910q", *now); n != 3 {
		t.Fatalf("after the wait the count is %d, want 3", n)
	}
}

func TestInstanceCapWaitIsCapped(t *testing.T) {
	// The oldest entry needs 50 more minutes; the wait cap is 20 minutes, then the walk goes on with a note.
	w, _, slept, stderr := capWalk(t, raidWalkOptions{capWait: 20 * time.Minute},
		10*time.Minute, 8*time.Minute, 5*time.Minute, 2*time.Minute)
	if err := w.phaseInstanceCap(context.Background()); err != nil {
		t.Fatal(err)
	}
	var total time.Duration
	for _, d := range *slept {
		total += d
	}
	if total != 20*time.Minute {
		t.Fatalf("waited %s, want exactly the 20m cap (slept %v)", total, *slept)
	}
	if !strings.Contains(stderr.String(), "going on") || len(w.notes) == 0 {
		t.Fatalf("no going-on note: %q / %v", stderr.String(), w.notes)
	}
}

func TestInstanceCapZeroWaitWarnsAndGoesOn(t *testing.T) {
	w, _, slept, stderr := capWalk(t, raidWalkOptions{}, time.Minute, 2*time.Minute, 3*time.Minute, 4*time.Minute)
	if err := w.phaseInstanceCap(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(*slept) != 0 || !strings.Contains(stderr.String(), "instance cap: 4 fresh instances") {
		t.Fatalf("slept %v, stderr %q", *slept, stderr.String())
	}
}

func TestInstanceCapFailExitsWithItsOwnCode(t *testing.T) {
	w, _, slept, stderr := capWalk(t, raidWalkOptions{capWait: 20 * time.Minute, capFail: true},
		50*time.Minute, 30*time.Minute, 20*time.Minute, 10*time.Minute)
	err := w.phaseInstanceCap(context.Background())
	code, reason := stopInfo(err)
	if code != exitRaidWalkInstanceCap || code == exitRaidWalkFailed || code == exitRaidWalkIncident {
		t.Fatalf("code = %d (%s), want the distinct %d", code, reason, exitRaidWalkInstanceCap)
	}
	if len(*slept) != 0 {
		t.Fatalf("--instance-cap-fail must not wait: %v", *slept)
	}
	if !strings.Contains(stderr.String(), "the 6th is refused by the core") {
		t.Fatalf("warning missing: %q", stderr.String())
	}
}

func TestInstanceCapRestartResetsTheLedger(t *testing.T) {
	w, now, slept, _ := capWalk(t, raidWalkOptions{capWait: 20 * time.Minute, capFail: true},
		50*time.Minute, 30*time.Minute, 20*time.Minute, 10*time.Minute)
	start := now.Add(-5 * time.Minute) // the worldserver restarted 5 minutes ago
	w.env.serverStart = func(context.Context, string) (time.Time, bool) { return start, true }
	if err := w.phaseInstanceCap(context.Background()); err != nil {
		t.Fatalf("a restarted server must not trip the cap: %v", err)
	}
	if len(*slept) != 0 {
		t.Fatalf("slept %v", *slept)
	}
	l, _ := raidwalk.LoadCapLedger(w.env.capLedger)
	if n := l.Count("m910q", *now); n != 0 {
		t.Fatalf("count after the restart = %d, want 0", n)
	}
}

func TestInstanceCapResetFlag(t *testing.T) {
	w, _, _, _ := capWalk(t, raidWalkOptions{capFail: true, capReset: true},
		50*time.Minute, 30*time.Minute, 20*time.Minute, 10*time.Minute)
	if err := w.phaseInstanceCap(context.Background()); err != nil {
		t.Fatalf("--instance-cap-reset must clear the ledger first: %v", err)
	}
}

func TestInstanceCapRecordsTheEntryAndOffWithoutALedger(t *testing.T) {
	w, now, _, _ := capWalk(t, raidWalkOptions{})
	w.recordFreshInstance()
	w.recordFreshInstance()
	l, _ := raidwalk.LoadCapLedger(w.env.capLedger)
	if n := l.Count("m910q", *now); n != 2 {
		t.Fatalf("recorded %d, want 2", n)
	}
	w.env.capLedger = ""
	w.recordFreshInstance() // no file, no panic
	if err := w.phaseInstanceCap(context.Background()); err != nil {
		t.Fatal(err)
	}
}

func TestInstanceCapFlags(t *testing.T) {
	var se bytes.Buffer
	o, code := parseRaidWalkArgs([]string{"zg"}, &se)
	if code != exitOK || o.capWait != 20*time.Minute || o.capFail || o.capReset {
		t.Fatalf("defaults: %+v code %d", o, code)
	}
	o, code = parseRaidWalkArgs([]string{"zg", "--instance-cap-wait", "5m", "--instance-cap-fail", "--instance-cap-reset"}, &se)
	if code != exitOK || o.capWait != 5*time.Minute || !o.capFail || !o.capReset {
		t.Fatalf("set: %+v code %d", o, code)
	}
	if _, code = parseRaidWalkArgs([]string{"zg", "--instance-cap-wait", "-1m"}, &se); code != exitUsage {
		t.Fatalf("negative wait: code %d", code)
	}
}
