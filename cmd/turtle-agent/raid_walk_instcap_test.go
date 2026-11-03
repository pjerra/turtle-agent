package main

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"sync"
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
		_, err := raidwalk.UpdateCapLedger(path, func(l *raidwalk.CapLedger) {
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
	// 3 entries left in the hour, plus the place this walk now holds.
	if n := l.Count("m910q", *now); n != 4 || !w.capReserved {
		t.Fatalf("after the wait the count is %d (reserved %v), want 3 entries + 1 reservation", n, w.capReserved)
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
	if n := l.Count("m910q", *now); n != 1 { // only this walk's own reservation
		t.Fatalf("count after the restart = %d, want 1 (the walk's own reservation)", n)
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

func TestInstanceCapThreeConcurrentWalksAtThreeAdmitOnlyOne(t *testing.T) {
	w1, now, _, _ := capWalk(t, raidWalkOptions{capFail: true, holder: "w1"}, 30*time.Minute, 20*time.Minute, 10*time.Minute)
	walks := []*walk{w1}
	for _, h := range []string{"w2", "w3"} {
		var se bytes.Buffer
		w := &walk{opts: raidWalkOptions{capFail: true, holder: h, server: "m910q"}, stderr: &se, plan: w1.plan}
		w.env = walkEnv{now: func() time.Time { return *now }, capLedger: w1.env.capLedger}
		walks = append(walks, w)
	}
	errs := make([]error, len(walks))
	var wg sync.WaitGroup
	for i, w := range walks {
		wg.Add(1)
		go func() {
			defer wg.Done()
			errs[i] = w.phaseInstanceCap(context.Background())
		}()
	}
	wg.Wait()
	admitted := 0
	for i, e := range errs {
		if e == nil {
			admitted++
			continue
		}
		if code, _ := stopInfo(e); code != exitRaidWalkInstanceCap {
			t.Fatalf("walk %d: code %d, want %d", i, code, exitRaidWalkInstanceCap)
		}
	}
	if admitted != 1 {
		t.Fatalf("%d walks admitted at a count of 3, want exactly 1", admitted)
	}
}

func TestInstanceCapReservationConfirmedOrReleased(t *testing.T) {
	w, now, _, _ := capWalk(t, raidWalkOptions{capWait: time.Minute, holder: "h"}, time.Minute)
	if err := w.phaseInstanceCap(context.Background()); err != nil {
		t.Fatal(err)
	}
	l, _ := raidwalk.LoadCapLedger(w.env.capLedger)
	if !w.capReserved || l.Count("m910q", *now) != 2 {
		t.Fatalf("reserved %v, count %d, want a held place (2)", w.capReserved, l.Count("m910q", *now))
	}
	// A failure before the raid entered gives the place back.
	w.releaseCapReservation()
	l, _ = raidwalk.LoadCapLedger(w.env.capLedger)
	if w.capReserved || l.Count("m910q", *now) != 1 {
		t.Fatalf("after release: reserved %v, count %d, want 1", w.capReserved, l.Count("m910q", *now))
	}
	// A walk that entered turns it into an entry, and a later release does nothing.
	if err := w.phaseInstanceCap(context.Background()); err != nil {
		t.Fatal(err)
	}
	w.recordFreshInstance()
	w.releaseCapReservation()
	l, _ = raidwalk.LoadCapLedger(w.env.capLedger)
	if n := l.Count("m910q", *now); n != 2 || len(l.Servers["m910q"].Reservations) != 0 || len(l.Servers["m910q"].Entries) != 2 {
		t.Fatalf("after confirm: count %d, %+v", n, l.Servers["m910q"])
	}
}

func TestInstanceCapUnreadableLedger(t *testing.T) {
	// A directory where the file should be: unreadable, not corrupt.
	mk := func(opts raidWalkOptions) (*walk, *[]time.Duration, *bytes.Buffer) {
		w, _, slept, stderr := capWalk(t, opts)
		if err := os.Mkdir(w.env.capLedger, 0o700); err != nil {
			t.Fatal(err)
		}
		return w, slept, stderr
	}
	w, slept, _ := mk(raidWalkOptions{capFail: true})
	code, reason := stopInfo(w.phaseInstanceCap(context.Background()))
	if code != exitRaidWalkInstanceCap || !strings.Contains(reason, "cannot be read") || len(*slept) != 0 {
		t.Fatalf("--instance-cap-fail: code %d %q slept %v", code, reason, *slept)
	}
	w, slept, stderr := mk(raidWalkOptions{capWait: 20 * time.Minute})
	if err := w.phaseInstanceCap(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(*slept) != 1 || (*slept)[0] != capUnreadableWait || w.capReserved || !strings.Contains(stderr.String(), "cannot be read") {
		t.Fatalf("wait one cycle: slept %v, reserved %v, stderr %q", *slept, w.capReserved, stderr.String())
	}
	if st, err := os.Stat(w.env.capLedger); err != nil || !st.IsDir() {
		t.Fatal("the unreadable ledger was written over")
	}
	w.recordFreshInstance() // must not overwrite either
	if st, err := os.Stat(w.env.capLedger); err != nil || !st.IsDir() {
		t.Fatal("recordFreshInstance wrote over the unreadable ledger")
	}
}

func TestInstanceCapCorruptLedgerIsMovedAsideAndLogged(t *testing.T) {
	w, _, _, stderr := capWalk(t, raidWalkOptions{capWait: time.Minute})
	if err := os.WriteFile(w.env.capLedger, []byte("garbage"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := w.phaseInstanceCap(context.Background()); err != nil {
		t.Fatal(err)
	}
	moved, _ := filepath.Glob(w.env.capLedger + ".corrupt-*")
	if len(moved) != 1 || !strings.Contains(stderr.String(), "ledger was corrupt") || !w.capReserved {
		t.Fatalf("moved %v, stderr %q, reserved %v", moved, stderr.String(), w.capReserved)
	}
	if len(w.notes) == 0 {
		t.Fatal("the history loss is not in the walk's notes")
	}
}
