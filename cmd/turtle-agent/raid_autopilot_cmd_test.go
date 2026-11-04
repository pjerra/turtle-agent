package main

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/pjerra/turtle-agent/internal/raidwalk"
)

// TestAutoCommandArgs pins the managed flags: the target's own flags, the goal's until, the attempt's out, and the
// restart's --from/--place/--reuse-instance. Nothing unsafe is ever added.
func TestAutoCommandArgs(t *testing.T) {
	target := raidwalk.AutoTarget{
		ID: "a", Server: "tortoise-test2", Slot: "1", Holder: "zg-a",
		Goal: raidwalk.AutoGoal{Kind: "boss", Boss: 14515},
		Args: []string{"zul_gurub", "--dir", "/plans", "--hires", "/roster.json"},
	}
	args := autoCommandArgs("/bin/ta", target, "/walk/a-1", "step 405", true, 0)
	got := strings.Join(args, " ")
	want := "raid-walk zul_gurub --dir /plans --hires /roster.json --server tortoise-test2 --slot 1 --holder zg-a --until boss-done --out /walk/a-1 --from step 405 --place --reuse-instance"
	if got != want {
		t.Fatalf("args:\n%s\nwant:\n%s", got, want)
	}
	if strings.Contains(got, "--instance-cap-unsafe") {
		t.Fatal("unsafe flag added")
	}
	// A first placement has no --from, no --place and no reuse.
	args = autoCommandArgs("/bin/ta", target, "/walk/a-1", "", false, 0)
	got = strings.Join(args, " ")
	if strings.Contains(got, "--from") || strings.Contains(got, "--place") || strings.Contains(got, "--reuse-instance") {
		t.Fatalf("first placement has managed restart flags: %s", got)
	}
	if strings.Contains(got, "--instance-cap-per-account") {
		t.Fatalf("an unset cap was forwarded: %s", got)
	}
	// An explicit cap is passed on for every walk.
	args = autoCommandArgs("/bin/ta", target, "/walk/a-1", "", false, 30)
	if got = strings.Join(args, " "); !strings.Contains(got, "--instance-cap-per-account 30") {
		t.Fatalf("the cap was not forwarded: %s", got)
	}
}

func TestAutoLastStepAndReason(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "run.log")
	os.WriteFile(path, []byte("19:46:05.696 TBM:RUN|zul_gurub|running|51/761|jeklik|boss|begin|fighting\n"+
		"19:46:56.999 TBM:RUN|zul_gurub|running|51/761|jeklik|boss|begin|fighting: 11368 is polymorphed\n"), 0o600)
	if n := autoLastStep(path); n != 51 {
		t.Fatalf("step = %d, want 51", n)
	}
	if r := autoLastReason(path); r != "fighting: 11368 is polymorphed" {
		t.Fatalf("reason = %q", r)
	}
	if n := autoLastStep(filepath.Join(dir, "none.log")); n != 0 {
		t.Fatalf("missing log step = %d", n)
	}
}

// TestAutoFrameSignature counts a boss-HP change and an empty hostile list as progress, but a repeated frame as not.
func TestAutoFrameSignature(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "frames.jsonl")
	os.WriteFile(path, []byte(`{"t":1,"hostiles":[{"guid":"0xA","entry":14515,"hp_pct":80}]}`+"\n"), 0o600)
	r := &autoRun{}
	if sig := autoFrameSignature(path, r); sig != "0xA=14515:80" {
		t.Fatalf("signature = %q", sig)
	}
	// The same frame again: no new bytes, no signature.
	if sig := autoFrameSignature(path, r); sig != "" {
		t.Fatalf("unchanged file returned %q", sig)
	}
	f, _ := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0o600)
	f.WriteString(`{"t":2,"hostiles":[{"guid":"0xA","entry":14515,"hp_pct":40}]}` + "\n")
	f.Close()
	if sig := autoFrameSignature(path, r); sig != "0xA=14515:40" {
		t.Fatalf("changed hp signature = %q", sig)
	}
	f, _ = os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0o600)
	f.WriteString(`{"t":3,"hostiles":[]}` + "\n")
	f.Close()
	if sig := autoFrameSignature(path, r); sig != "none" {
		t.Fatalf("empty hostiles signature = %q", sig)
	}
}

// TestAutoCapFreeTime pins the instance-cap wait as "not a stall": the line's earliest free time is the floor.
func TestAutoCapFreeTime(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "run.log")
	os.WriteFile(path, []byte("10:00:00.000 instance cap: waiting 16m5s (earliest free time 2026-11-03T12:34:56Z) for the oldest of 4 to age out of the hour\n"), 0o600)
	at := autoCapFreeTime(path)
	if at.IsZero() || at.Format("2006-01-02T15:04:05Z") != "2026-11-03T12:34:56Z" {
		t.Fatalf("free time = %s", at)
	}
}

// TestAutoInitSeq continues the out numbering after an earlier autopilot's dirs (the walk refuses an existing --out).
func TestAutoInitSeq(t *testing.T) {
	dir := t.TempDir()
	for _, name := range []string{"a-1", "a-2.out", "a-10", "b-3", "other-9"} {
		if err := os.MkdirAll(filepath.Join(dir, name), 0o700); err != nil {
			t.Fatal(err)
		}
	}
	s := &autoSupervisor{o: autoOptions{walkDir: dir}, states: []*autoState{{id: "a"}, {id: "b"}}}
	s.initSeq()
	if s.states[0].seq != 10 || s.states[1].seq != 3 {
		t.Fatalf("seq a=%d b=%d, want 10 and 3", s.states[0].seq, s.states[1].seq)
	}
}

func TestAutoSummaryReadsTheSingularBossKill(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "summary.json")
	os.WriteFile(path, []byte(`{"exit_code":0,"steps_reached":51,"steps_total":761,"end_reason":"until","boss_kill":{"entry":14517,"killed":true},"boss_kills":[{"entry":14517,"killed":true}]}`), 0o600)
	sum, err := autoReadSummary(path)
	if err != nil {
		t.Fatal(err)
	}
	if sum.BossKill == nil || !sum.BossKill.Killed || sum.BossKill.Entry != 14517 {
		t.Fatalf("boss_kill = %+v", sum.BossKill)
	}
}

// autoTeardownTest builds one walk dir with one exited attempt (exit 5: a restart) and a stub turtle-agent. The stub's
// `teardown` fails while the fail file exists, its `lease list` reports a held lease while the leased file exists, and
// its `raid-walk` sleeps like a real walk. It returns the supervisor, the id's state and its run, and the stub's flags.
func autoTeardownTest(t *testing.T) (s *autoSupervisor, st *autoState, r *autoRun, fail, teardowns, leased string) {
	t.Helper()
	dir := t.TempDir()
	fail = filepath.Join(dir, "fail")
	leased = filepath.Join(dir, "leased")
	teardowns = filepath.Join(dir, "teardowns")
	stub := filepath.Join(dir, "stub.sh")
	script := fmt.Sprintf(`#!/bin/sh
case "$1" in
teardown)
  echo "$2 $3" >> '%s'
  if [ -f '%s' ]; then echo "stub: teardown failed" >&2; exit 14; fi
  echo "stub: teardown ok"
  ;;
lease)
  if [ -f '%s' ]; then echo '[{"slot":{"id":"1"},"lease":{"holder":"zg37-a"}}]'; else echo '[]'; fi
  ;;
*)
  sleep 30
  ;;
esac
`, teardowns, fail, leased)
	if err := os.WriteFile(stub, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	walk := filepath.Join(dir, "walk")
	out := filepath.Join(walk, "rd1-1")
	if err := os.MkdirAll(filepath.Join(out, "slot"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(out, "slot", "slot.json"), []byte(`{"server":"srv","slot":"1","holder":"zg37-a"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(out, "summary.json"), []byte(`{"exit_code":5,"steps_reached":51,"steps_total":761,"end_reason":"server closed the connection"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	s = &autoSupervisor{
		o:      autoOptions{walkDir: walk, binary: stub, poll: time.Millisecond, stall: time.Hour, settle: 0, statusEvery: time.Hour},
		stderr: io.Discard,
	}
	if err := s.open(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(s.close)
	g := raidwalk.AutoTarget{
		ID: "rd1", Server: "srv", Slot: "1", Holder: "zg37-a",
		Goal: raidwalk.AutoGoal{Kind: "boss", Boss: 14515}, Args: []string{"zul_gurub"},
	}
	st = &autoState{id: "rd1", goals: []autoGoalState{{target: g, attempt: 1}}}
	r = &autoRun{
		seq: 1, out: out, slot: "1", goal: g.Goal, exited: true, exitCode: 5,
		lastStep: 51, started: time.Now(), lastProgress: time.Now(), wait: make(chan error),
	}
	st.run = r
	s.states = []*autoState{st}
	return s, st, r, fail, teardowns, leased
}

func autoReadFile(t *testing.T, path string) string {
	t.Helper()
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return string(raw)
}

// A failed teardown is not completion: the run stays active, nothing is relaunched, and YIELD writes no YIELD-DONE.
func TestAutoTeardownFailureKeepsTheRunActive(t *testing.T) {
	s, st, r, fail, _, _ := autoTeardownTest(t)
	if err := os.WriteFile(fail, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	s.tick(context.Background())
	if st.run != r || r.tornDown {
		t.Fatal("a failed teardown cleared the run")
	}
	events := autoReadFile(t, filepath.Join(s.o.walkDir, autoEventsFile))
	if !strings.Contains(events, "teardown-failed") {
		t.Fatalf("no teardown-failed event: %s", events)
	}
	for _, unwanted := range []string{" rd1 restart:", " rd1 launch:", " rd1 advance:"} {
		if strings.Contains(events, unwanted) {
			t.Fatalf("the supervisor moved on after a failed teardown (%q): %s", unwanted, events)
		}
	}
	s.shutdown("YIELD")
	if _, err := os.Stat(filepath.Join(s.o.walkDir, autoYieldDone)); err == nil {
		t.Fatal("YIELD-DONE written although the teardown failed")
	}
	if !strings.Contains(autoReadFile(t, filepath.Join(s.o.walkDir, autoEventsFile)), "no YIELD-DONE") {
		t.Fatal("the failed YIELD did not say why it left YIELD-DONE alone")
	}
}

// The clean retry finishes the held-back restart and the run is relaunched.
func TestAutoTeardownRetryRelaunches(t *testing.T) {
	s, st, r, fail, _, _ := autoTeardownTest(t)
	if err := os.WriteFile(fail, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	s.tick(context.Background())
	if st.run != r {
		t.Fatal("a failed teardown cleared the run")
	}
	if err := os.Remove(fail); err != nil {
		t.Fatal(err)
	}
	s.tick(context.Background())
	if st.run != nil || !r.tornDown {
		t.Fatalf("the clean retry did not finish the run (run %v, tornDown %v)", st.run != nil, r.tornDown)
	}
	events := autoReadFile(t, filepath.Join(s.o.walkDir, autoEventsFile))
	if !strings.Contains(events, " rd1 restart:") {
		t.Fatalf("the held-back restart was not applied: %s", events)
	}
	// The restart's 20 s backoff is not what this test measures: the next tick is due now.
	st.nextAt = time.Time{}
	s.tick(context.Background())
	if st.run == nil || st.run == r {
		t.Fatal("no relaunch after the clean retry")
	}
	if !strings.Contains(autoReadFile(t, filepath.Join(s.o.walkDir, autoEventsFile)), " rd1 launch:") {
		t.Fatal("no launch event after the clean retry")
	}
	s.stopRun(st, st.run)
}

// The third failed teardown tick escalates: the line goes to EVENTS.txt and status.txt, and the slot is left alone.
func TestAutoTeardownEscalatesAfterThreeFailedTicks(t *testing.T) {
	s, st, r, fail, teardowns, _ := autoTeardownTest(t)
	if err := os.WriteFile(fail, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < autoTeardownTries; i++ {
		s.tick(context.Background())
	}
	if !r.escalated || st.run != r {
		t.Fatalf("not escalated after %d failed ticks (escalated %v, run kept %v)", autoTeardownTries, r.escalated, st.run == r)
	}
	if got := strings.Count(autoReadFile(t, teardowns), "\n"); got != 2*autoTeardownTries {
		t.Fatalf("teardown runs = %d, want %d (two per failed tick)", got, 2*autoTeardownTries)
	}
	for _, path := range []string{autoEventsFile, autoStatusFile} {
		if got := autoReadFile(t, filepath.Join(s.o.walkDir, path)); !strings.Contains(got, "ESCALATE teardown rd1 1") {
			t.Fatalf("%s has no ESCALATE teardown rd1 1 line: %s", path, got)
		}
	}
	// Left alone: no more teardown, no relaunch, no YIELD-DONE.
	s.tick(context.Background())
	if got := strings.Count(autoReadFile(t, teardowns), "\n"); got != 2*autoTeardownTries {
		t.Fatalf("the escalated slot was torn down again (%d teardown runs)", got)
	}
	s.shutdown("YIELD")
	if _, err := os.Stat(filepath.Join(s.o.walkDir, autoYieldDone)); err == nil {
		t.Fatal("YIELD-DONE written for an escalated slot")
	}
}

// A teardown that exits 0 is still a failure while the walk process is alive or the lease is held.
func TestAutoTeardownVerifiesTheWalkAndTheLease(t *testing.T) {
	s, _, r, _, _, leased := autoTeardownTest(t)
	live := exec.Command("sleep", "30")
	if err := live.Start(); err != nil {
		t.Fatal(err)
	}
	r.cmd = live
	if err := s.teardown("rd1", r); err == nil || !strings.Contains(err.Error(), "still alive") {
		t.Fatalf("a live walk passed the teardown: %v", err)
	}
	_ = live.Process.Kill()
	_ = live.Wait()
	r.exited = true
	if err := os.WriteFile(leased, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := s.teardown("rd1", r); err == nil || !strings.Contains(err.Error(), "lease is still held") {
		t.Fatalf("a held lease passed the teardown: %v", err)
	}
	if err := os.Remove(leased); err != nil {
		t.Fatal(err)
	}
	if err := s.teardown("rd1", r); err != nil {
		t.Fatalf("a clean slot failed the teardown: %v", err)
	}
}

// Two starts on one walk dir: exactly one acquires the lock; the other refuses with a clear error.
func TestAutoLockOneSupervisorPerWalkDir(t *testing.T) {
	dir := t.TempDir()
	const n = 8
	ready := make(chan struct{})
	locks := make(chan *os.File, n)
	errs := make(chan error, n)
	var wg sync.WaitGroup
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-ready
			f, err := autoLock(dir)
			if err != nil {
				errs <- err
				return
			}
			locks <- f
		}()
	}
	close(ready)
	wg.Wait()
	close(locks)
	close(errs)
	held := 0
	for f := range locks {
		held++
		f.Close()
	}
	if held != 1 {
		t.Fatalf("%d supervisors acquired the lock, want exactly 1", held)
	}
	refused := 0
	for err := range errs {
		refused++
		if !strings.Contains(err.Error(), "another autopilot runs") {
			t.Fatalf("refusal error = %v", err)
		}
	}
	if refused != n-1 {
		t.Fatalf("%d starts were refused, want %d", refused, n-1)
	}
	f, err := autoLock(dir)
	if err != nil {
		t.Fatalf("the released lock was not re-acquirable: %v", err)
	}
	f.Close()
}

// Two concurrent --foreground starts on one walk dir: one supervises, the other exits with the lock error.
func TestAutoTwoForegroundStarts(t *testing.T) {
	dir := t.TempDir()
	walk := filepath.Join(dir, "walk")
	if err := os.MkdirAll(walk, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(walk, autoTargetsFile),
		[]byte("rd1 srv 1 zg37-a full -- zul_gurub\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	stub := filepath.Join(dir, "stub.sh")
	if err := os.WriteFile(stub, []byte("#!/bin/sh\nsleep 30\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 300*time.Millisecond)
	defer cancel()
	codes := make(chan int, 2)
	var wg sync.WaitGroup
	ready := make(chan struct{})
	var mu sync.Mutex
	stderrs := map[int]string{}
	for i := 0; i < 2; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			var out, errOut bytes.Buffer
			<-ready
			code := raidAutopilotCmd(ctx, []string{"--foreground", "--binary", stub, walk}, &out, &errOut)
			mu.Lock()
			stderrs[i] = errOut.String()
			mu.Unlock()
			codes <- code
		}(i)
	}
	close(ready)
	wg.Wait()
	close(codes)
	var got []int
	for code := range codes {
		got = append(got, code)
	}
	ok, refused := 0, 0
	for _, code := range got {
		switch code {
		case exitOK:
			ok++
		case exitUsage:
			refused++
		default:
			t.Fatalf("unexpected exit code %d (all: %v)", code, got)
		}
	}
	if ok != 1 || refused != 1 {
		t.Fatalf("exits = %v, want one 0 and one %d", got, exitUsage)
	}
	mu.Lock()
	defer mu.Unlock()
	found := false
	for _, s := range stderrs {
		if strings.Contains(s, "another autopilot runs") {
			found = true
		}
	}
	if !found {
		t.Fatalf("no refusal named the running autopilot: %v", stderrs)
	}
}
