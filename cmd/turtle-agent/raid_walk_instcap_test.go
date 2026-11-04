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

// Six fresh raids in the hour pass the walk's gate on a core patched to 30 per account, where the stock 5 would wait.
func TestInstanceCapThirtyGoesOnAtSixRaids(t *testing.T) {
	w, _, slept, stderr := capWalk(t, raidWalkOptions{capWait: 20 * time.Minute, capPerAccount: 30},
		5*time.Minute, 10*time.Minute, 15*time.Minute, 20*time.Minute, 25*time.Minute, 30*time.Minute)
	if err := w.phaseInstanceCap(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(*slept) != 0 || stderr.Len() != 0 || !w.capReserved {
		t.Fatalf("6 in the hour below a cap of 30: slept %v stderr %q reserved %v", *slept, stderr.String(), w.capReserved)
	}
}

func TestInstanceCapWaitsForTheOldestToAgeOut(t *testing.T) {
	w, now, slept, stderr := capWalk(t, raidWalkOptions{capWait: 20 * time.Minute},
		55*time.Minute, 30*time.Minute, 20*time.Minute, 10*time.Minute)
	if err := w.phaseInstanceCap(context.Background()); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(stderr.String(), "instance cap: 4 fresh instances in the last hour on m910q; the core's cap is 5 per account and hour") {
		t.Fatalf("warning missing: %q", stderr.String())
	}
	if len(*slept) != 1 || (*slept)[0] != 5*time.Minute+capWaitMargin {
		t.Fatalf("slept %v, want one wait of 5m + margin", *slept)
	}
	// The wait names when the oldest entry leaves the hour (55 min old at 12:00: 12:05), not only the duration.
	if !strings.Contains(stderr.String(), "earliest free time 2026-11-03T12:05:00Z") {
		t.Fatalf("the wait line lacks the earliest free time: %q", stderr.String())
	}
	l, _ := raidwalk.LoadCapLedger(w.env.capLedger)
	// 3 entries left in the hour, plus the place this walk now holds.
	if n := l.Count("m910q", *now); n != 4 || !w.capReserved {
		t.Fatalf("after the wait the count is %d (reserved %v), want 3 entries + 1 reservation", n, w.capReserved)
	}
}

func TestInstanceCapWaitIsCappedExitsWithoutAReservation(t *testing.T) {
	// The oldest entry needs 50 more minutes; the wait cap is 20 minutes, then the walk exits 26 and holds no place.
	w, now, slept, stderr := capWalk(t, raidWalkOptions{capWait: 20 * time.Minute},
		10*time.Minute, 8*time.Minute, 5*time.Minute, 2*time.Minute)
	code, reason := stopInfo(w.phaseInstanceCap(context.Background()))
	if code != exitRaidWalkInstanceCap {
		t.Fatalf("code %d (%s), want %d: a walk past the wait must not start without a reservation", code, reason, exitRaidWalkInstanceCap)
	}
	var total time.Duration
	for _, d := range *slept {
		total += d
	}
	if total != 20*time.Minute {
		t.Fatalf("waited %s, want exactly the 20m cap (slept %v)", total, *slept)
	}
	if w.capReserved || !strings.Contains(reason, "no fresh instance is started without a reservation") || len(w.notes) == 0 {
		t.Fatalf("reserved %v, reason %q, notes %v", w.capReserved, reason, w.notes)
	}
	if !strings.Contains(stderr.String(), "still 4 on m910q") {
		t.Fatalf("no wait-exhausted line: %q", stderr.String())
	}
	l, _ := raidwalk.LoadCapLedger(w.env.capLedger)
	if n := l.Count("m910q", *now); n != 4 { // the 4 entries only: no place was taken
		t.Fatalf("count %d, want 4 (no reservation)", n)
	}
}

func TestInstanceCapZeroWaitExitsAtTheCount(t *testing.T) {
	w, _, slept, stderr := capWalk(t, raidWalkOptions{}, time.Minute, 2*time.Minute, 3*time.Minute, 4*time.Minute)
	code, _ := stopInfo(w.phaseInstanceCap(context.Background()))
	if code != exitRaidWalkInstanceCap || w.capReserved || len(*slept) != 0 || !strings.Contains(stderr.String(), "instance cap: 4 fresh instances") {
		t.Fatalf("code %d, reserved %v, slept %v, stderr %q", code, w.capReserved, *slept, stderr.String())
	}
}

func TestInstanceCapUnsafeGoesOnWithoutAReservation(t *testing.T) {
	w, now, slept, stderr := capWalk(t, raidWalkOptions{capWait: 20 * time.Minute, capUnsafe: true},
		10*time.Minute, 8*time.Minute, 5*time.Minute, 2*time.Minute)
	if err := w.phaseInstanceCap(context.Background()); err != nil {
		t.Fatalf("--instance-cap-unsafe must go on at the wait's end: %v", err)
	}
	var total time.Duration
	for _, d := range *slept {
		total += d
	}
	if total != 20*time.Minute {
		t.Fatalf("waited %s, want the 20m cap (slept %v)", total, *slept)
	}
	if w.capReserved || !strings.Contains(stderr.String(), "--instance-cap-unsafe") || len(w.notes) == 0 {
		t.Fatalf("reserved %v, stderr %q, notes %v: the unsafe pass must be logged", w.capReserved, stderr.String(), w.notes)
	}
	// Past the cap the walk's entry is still recorded once it enters, so later walks count it.
	w.recordFreshInstance()
	l, _ := raidwalk.LoadCapLedger(w.env.capLedger)
	if n := l.Count("m910q", *now); n != 5 {
		t.Fatalf("count %d, want the 4 entries and the walk's own", n)
	}
}

func TestInstanceCapTimedOutWalkersNoneProceedsWithoutAReservation(t *testing.T) {
	// Four walkers start together at the threshold (4 entries, the oldest needs 56 more minutes, the wait is 2 minutes):
	// every one of them times out and exits 26, and none is admitted or holds a place.
	w0, now0, _, _ := capWalk(t, raidWalkOptions{capWait: 2 * time.Minute, holder: "w0"},
		time.Minute, 2*time.Minute, 3*time.Minute, 4*time.Minute)
	ledger := w0.env.capLedger
	start := *now0
	walks := []*walk{w0}
	for _, h := range []string{"w1", "w2", "w3"} {
		var se bytes.Buffer
		clock := start
		w := &walk{opts: raidWalkOptions{capWait: 2 * time.Minute, holder: h, server: "m910q"}, stderr: &se, plan: w0.plan}
		w.env = walkEnv{
			now:       func() time.Time { return clock },
			sleep:     func(_ context.Context, d time.Duration) error { clock = clock.Add(d); return nil },
			capLedger: ledger,
		}
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
	for i, e := range errs {
		if code, reason := stopInfo(e); code != exitRaidWalkInstanceCap {
			t.Fatalf("walk %d: code %d (%s), want %d and no admission", i, code, reason, exitRaidWalkInstanceCap)
		}
		if walks[i].capReserved {
			t.Fatalf("walk %d holds a reservation after timing out", i)
		}
	}
	l, _ := raidwalk.LoadCapLedger(ledger)
	if s := l.Servers["m910q"]; s == nil || len(s.Reservations) != 0 || len(s.Entries) != 4 {
		t.Fatalf("ledger after the timeouts: %+v, want 4 entries and no reservation", s)
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
	if !strings.Contains(stderr.String(), "the core's cap is 5") {
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
	if code != exitOK || o.capWait != 20*time.Minute || o.capFail || o.capReset || o.capUnsafe {
		t.Fatalf("defaults: %+v code %d", o, code)
	}
	o, code = parseRaidWalkArgs([]string{"zg", "--instance-cap-wait", "5m", "--instance-cap-fail", "--instance-cap-reset"}, &se)
	if code != exitOK || o.capWait != 5*time.Minute || !o.capFail || !o.capReset {
		t.Fatalf("set: %+v code %d", o, code)
	}
	o, code = parseRaidWalkArgs([]string{"zg", "--instance-cap-unsafe"}, &se)
	if code != exitOK || !o.capUnsafe || o.capFail {
		t.Fatalf("--instance-cap-unsafe: %+v code %d", o, code)
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
	untouched := func(w *walk) {
		t.Helper()
		if st, err := os.Stat(w.env.capLedger); err != nil || !st.IsDir() {
			t.Fatal("the unreadable ledger was written over")
		}
	}
	// --instance-cap-fail: exit 26 at once.
	w, slept, _ := mk(raidWalkOptions{capFail: true})
	code, reason := stopInfo(w.phaseInstanceCap(context.Background()))
	if code != exitRaidWalkInstanceCap || !strings.Contains(reason, "cannot be read") || len(*slept) != 0 {
		t.Fatalf("--instance-cap-fail: code %d %q slept %v", code, reason, *slept)
	}
	// Fail closed: one cycle's wait for a transient read error, then exit 26 with no reservation and no write.
	w, slept, stderr := mk(raidWalkOptions{capWait: 20 * time.Minute})
	code, reason = stopInfo(w.phaseInstanceCap(context.Background()))
	if code != exitRaidWalkInstanceCap || !strings.Contains(reason, "cannot be read") || !strings.Contains(reason, "--instance-cap-unsafe") {
		t.Fatalf("unreadable ledger must fail closed: code %d %q", code, reason)
	}
	if len(*slept) != 1 || (*slept)[0] != capUnreadableWait || w.capReserved || !strings.Contains(stderr.String(), "cannot be read") {
		t.Fatalf("wait one cycle: slept %v, reserved %v, stderr %q", *slept, w.capReserved, stderr.String())
	}
	untouched(w)
	w.recordFreshInstance() // must not overwrite either
	untouched(w)
	// No wait (--instance-cap-wait 0): exit 26 at once.
	w, slept, _ = mk(raidWalkOptions{})
	if code, _ := stopInfo(w.phaseInstanceCap(context.Background())); code != exitRaidWalkInstanceCap || len(*slept) != 0 {
		t.Fatalf("zero wait: code %d slept %v", code, *slept)
	}
	// --instance-cap-unsafe: the same wait, then go on without the ledger, logged, no reservation, nothing written.
	w, slept, stderr = mk(raidWalkOptions{capWait: 20 * time.Minute, capUnsafe: true})
	if err := w.phaseInstanceCap(context.Background()); err != nil {
		t.Fatalf("--instance-cap-unsafe must go on: %v", err)
	}
	if len(*slept) != 1 || w.capReserved || !strings.Contains(stderr.String(), "--instance-cap-unsafe") {
		t.Fatalf("unsafe: slept %v, reserved %v, stderr %q", *slept, w.capReserved, stderr.String())
	}
	untouched(w)
	w.recordFreshInstance()
	untouched(w)
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

// The instance cap resolves flag first, then instance-cap-per-account.json beside the slots file, then the stock 5.
func TestWalkInstanceCapFromFlagAndFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "slots.json")
	body := `{"version":1,"servers":[
		{"name":"m910q","realmd":"10.0.0.1:3724","slots":[
			{"id":"1","account":"A","character":"Gbmage","port":8771,"settings_from":"/s.json"}]},
		{"name":"tortoise-test2","realmd":"10.0.0.2:3724","slots":[
			{"id":"1","account":"A","character":"Gbmage","port":8772,"settings_from":"/s.json"}]}]}`
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "instance-cap-per-account.json"), []byte(`{"m910q":30}`), 0o600); err != nil {
		t.Fatal(err)
	}
	cases := []struct {
		name string
		o    raidWalkOptions
		want int
	}{
		{"the server's file entry", raidWalkOptions{slotsPath: path, server: "m910q"}, 30},
		{"an unset server is the stock 5", raidWalkOptions{slotsPath: path, server: "tortoise-test2"}, raidwalk.InstanceCapPerAccount},
		{"the flag wins", raidWalkOptions{slotsPath: path, server: "m910q", capPerAccount: 5, capGiven: true}, 5},
	}
	for _, c := range cases {
		o := c.o
		env, _, err := defaultWalkEnv(&o)
		if err != nil {
			t.Fatalf("%s: %v", c.name, err)
		}
		if o.capPerAccount != c.want || env.capPerAccount != c.want {
			t.Fatalf("%s: opts %d env %d, want %d", c.name, o.capPerAccount, env.capPerAccount, c.want)
		}
	}
}
