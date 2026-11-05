package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/pjerra/turtle-agent/internal/raidwalk"
)

// autoStub is the fake turtle-agent the autopilot launches: `raid-walk` writes the attempt's summary, run.log and
// walk.pid from the fixture's spec and exits with its code, `teardown` is logged (and fails or sleeps on request),
// and `lease list` prints the fixture's JSON. Every invocation records its argv.
const autoStub = `#!/bin/sh
case "$1" in
teardown)
  printf 'start %s\n' "$2" >> "$AUTO_TEARDOWN_LOG"
  if [ -f "$AUTO_SLOW_TEARDOWN" ] && [ "$(cat "$AUTO_SLOW_TEARDOWN")" = "$2" ]; then sleep 2; fi
  if [ -f "$AUTO_FAIL_TEARDOWN" ]; then
    printf 'done %s fail\n' "$2" >> "$AUTO_TEARDOWN_LOG"
    echo "stub: teardown failed" >&2
    exit 14
  fi
  printf 'done %s ok\n' "$2" >> "$AUTO_TEARDOWN_LOG"
  echo "stub: teardown ok"
  exit 0
  ;;
lease)
  if [ -f "$AUTO_LEASE_JSON" ]; then cat "$AUTO_LEASE_JSON"; else echo '[]'; fi
  exit 0
  ;;
raid-walk)
  printf '%s\n' "$*" >> "$AUTO_ARGS_LOG"
  out=""
  while [ $# -gt 0 ]; do
    if [ "$1" = "--out" ]; then out=$2; shift 2; else shift; fi
  done
  mkdir -p "$out/slot"
  printf '{"server":"srv","slot":"1","holder":"zg-a","slots_path":"%s"}\n' "$AUTO_SLOTS" > "$out/slot/slot.json"
  printf '%s\n' "$$" > "$out/walk.pid"
  base=$(basename "$out")
  if [ -f "$AUTO_SPEC_DIR/$base.runlog" ]; then cp "$AUTO_SPEC_DIR/$base.runlog" "$out/run.log"; fi
  if [ -f "$AUTO_SPEC_DIR/$base.summary" ]; then cp "$AUTO_SPEC_DIR/$base.summary" "$out/summary.json"; fi
  if [ -f "$AUTO_SPEC_DIR/$base.sleep" ]; then sleep "$(cat "$AUTO_SPEC_DIR/$base.sleep")"; fi
  rm -f "$out/walk.pid"
  code=0
  if [ -f "$AUTO_SPEC_DIR/$base.exit" ]; then code=$(cat "$AUTO_SPEC_DIR/$base.exit"); fi
  exit "$code"
  ;;
esac
exit 0
`

// autoFixture is one walk dir with the stub, its logs and the per-attempt spec files.
type autoFixture struct {
	t         *testing.T
	dir, walk string
	stub      string
	specDir   string
	argsLog   string
	tearLog   string
	leaseJSON string
	failTear  string
	slowTear  string
	slotsPath string
	env       []string
}

func newAutoFixture(t *testing.T) *autoFixture {
	t.Helper()
	dir := t.TempDir()
	f := &autoFixture{
		t: t, dir: dir, walk: filepath.Join(dir, "walk"),
		specDir:   filepath.Join(dir, "spec"),
		argsLog:   filepath.Join(dir, "args.log"),
		tearLog:   filepath.Join(dir, "teardown.log"),
		leaseJSON: filepath.Join(dir, "lease.json"),
		failTear:  filepath.Join(dir, "fail-teardown"),
		slowTear:  filepath.Join(dir, "slow-teardown"),
		slotsPath: filepath.Join(dir, "slots.json"),
	}
	for _, d := range []string{f.walk, f.specDir} {
		if err := os.MkdirAll(d, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	f.stub = filepath.Join(dir, "stub.sh")
	if err := os.WriteFile(f.stub, []byte(autoStub), 0o755); err != nil {
		t.Fatal(err)
	}
	f.env = append(os.Environ(),
		"AUTO_SPEC_DIR="+f.specDir,
		"AUTO_ARGS_LOG="+f.argsLog,
		"AUTO_TEARDOWN_LOG="+f.tearLog,
		"AUTO_LEASE_JSON="+f.leaseJSON,
		"AUTO_FAIL_TEARDOWN="+f.failTear,
		"AUTO_SLOW_TEARDOWN="+f.slowTear,
		"AUTO_SLOTS="+f.slotsPath,
	)
	// The in-process launches (and the detached child) inherit the test process's env: the stub reads these.
	for k, v := range map[string]string{
		"AUTO_SPEC_DIR": f.specDir, "AUTO_ARGS_LOG": f.argsLog, "AUTO_TEARDOWN_LOG": f.tearLog,
		"AUTO_LEASE_JSON": f.leaseJSON, "AUTO_FAIL_TEARDOWN": f.failTear, "AUTO_SLOW_TEARDOWN": f.slowTear,
		"AUTO_SLOTS": f.slotsPath,
	} {
		t.Setenv(k, v)
	}
	t.Cleanup(func() { _ = os.Remove(f.failTear) })
	return f
}

// target appends one targets.txt line.
func (f *autoFixture) target(line string) {
	f.t.Helper()
	path := filepath.Join(f.walk, autoTargetsFile)
	fh, err := os.OpenFile(path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		f.t.Fatal(err)
	}
	defer fh.Close()
	if _, err := fmt.Fprintln(fh, line); err != nil {
		f.t.Fatal(err)
	}
}

// attempt plans one attempt (out dir base, e.g. "a-1"): its exit code and summary; steps > 0 also writes a run.log
// line, sleep > 0 keeps the walk running that long (the test kills it).
func (f *autoFixture) attempt(base string, exit, steps int, endReason string) {
	f.t.Helper()
	f.writeSpec(base+".exit", strconv.Itoa(exit))
	sum := map[string]any{
		"exit_code": exit, "steps_reached": steps, "steps_total": 761, "end_reason": endReason,
	}
	if entry, ok := bossKillOf(endReason); ok {
		sum["boss_kills"] = []map[string]any{{"entry": entry, "killed": true}}
	}
	raw, err := json.Marshal(sum)
	if err != nil {
		f.t.Fatal(err)
	}
	f.writeSpec(base+".summary", string(raw))
	if steps > 0 {
		f.writeSpec(base+".runlog", fmt.Sprintf("19:46:05.696 TBM:RUN|zul_gurub|running|%d/761|jeklik|boss|begin|fighting\n", steps))
	}
}

// running plans an attempt that stays up until the test stops it.
func (f *autoFixture) running(base string, seconds int) {
	f.t.Helper()
	f.writeSpec(base+".sleep", strconv.Itoa(seconds))
	f.writeSpec(base+".runlog", "19:46:05.696 TBM:RUN|zul_gurub|running|51/761|jeklik|boss|begin|fighting\n")
	f.writeSpec(base+".exit", "0")
}

func (f *autoFixture) writeSpec(name, body string) {
	f.t.Helper()
	if err := os.WriteFile(filepath.Join(f.specDir, name), []byte(body), 0o600); err != nil {
		f.t.Fatal(err)
	}
}

// bossKillOf reads the boss entry out of a "boss <entry> down" end reason the tests use.
func bossKillOf(reason string) (uint32, bool) {
	var entry uint32
	if _, err := fmt.Sscanf(reason, "boss %d down", &entry); err == nil && entry != 0 {
		return entry, true
	}
	return 0, false
}

// lease makes the stub's `lease list` report the slot state.
func (f *autoFixture) lease(holder, state string) {
	f.t.Helper()
	var body string
	if state == "" {
		body = "[]"
	} else {
		body = fmt.Sprintf(`[{"server":"srv","slot":{"id":"1"},"state":%q,"lease":{"holder":%q}}]`, state, holder)
	}
	if err := os.WriteFile(f.leaseJSON, []byte(body), 0o600); err != nil {
		f.t.Fatal(err)
	}
}

func (f *autoFixture) args() []string {
	raw, err := os.ReadFile(f.argsLog)
	if err != nil {
		return nil
	}
	return strings.Split(strings.TrimRight(string(raw), "\n"), "\n")
}

func (f *autoFixture) teardowns() []string {
	raw, err := os.ReadFile(f.tearLog)
	if err != nil {
		return nil
	}
	return strings.Split(strings.TrimRight(string(raw), "\n"), "\n")
}

func (f *autoFixture) events() string {
	raw, _ := os.ReadFile(filepath.Join(f.walk, autoEventsFile))
	return string(raw)
}

func (f *autoFixture) opts(extra ...string) autoOptions {
	o, code := parseAutoArgs(append([]string{"--foreground", "--binary", f.stub, "--poll", "5ms", "--settle", "0", "--status-every", "1h", f.walk}, extra...), io.Discard)
	if code != exitOK {
		f.t.Fatalf("parseAutoArgs = %d", code)
	}
	return o
}

// supervisor builds the startup path without the lock/pid/YIELD machinery (the state file and the reconciliation run
// exactly as a real start does).
func (f *autoFixture) supervisor(extra ...string) *autoSupervisor {
	f.t.Helper()
	return f.supervisorOpts(f.opts(extra...))
}

// supervisorOpts is supervisor with the options chosen by the test (a broken --binary, for one).
func (f *autoFixture) supervisorOpts(o autoOptions) *autoSupervisor {
	f.t.Helper()
	targets, err := readAutoTargets(f.walk)
	if err != nil {
		f.t.Fatal(err)
	}
	s := &autoSupervisor{o: o, stderr: io.Discard}
	if err := s.open(); err != nil {
		f.t.Fatal(err)
	}
	f.t.Cleanup(s.close)
	s.states = groupAutoTargets(targets)
	s.initSeq()
	if err := s.reconcile(); err != nil {
		f.t.Fatal(err)
	}
	// Leave no walk or finish goroutine writing into the temp dir when the test's cleanup removes it.
	f.t.Cleanup(func() { s.shutdown("signal") })
	return s
}

// run calls the real command in this process until every id is done/escalated or the deadline passes.
func (f *autoFixture) run(extra ...string) int {
	f.t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	var out, errOut bytes.Buffer
	args := append([]string{"--foreground", "--binary", f.stub, "--poll", "5ms", "--settle", "0", "--status-every", "1h", f.walk}, extra...)
	done := make(chan int, 1)
	go func() { done <- raidAutopilotCmd(ctx, args, &out, &errOut) }()
	select {
	case code := <-done:
		return code
	case <-time.After(20 * time.Second):
		cancel()
		f.t.Fatalf("the autopilot did not finish; events:\n%s\nstderr:\n%s", f.events(), errOut.String())
		return -1
	}
}

// waitForN polls until cond holds, bounded by d.
func waitForN(t *testing.T, d time.Duration, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(d)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %s", what)
}

// tickUntil advances the supervisor until cond holds (the launch, exit and next-args phases are separate ticks).
func tickUntil(t *testing.T, s *autoSupervisor, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		s.tick(context.Background())
		time.Sleep(2 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %s; events:\n%s", what, s.eventsText())
}

// eventsText is the supervisor's EVENTS.txt.
func (s *autoSupervisor) eventsText() string {
	raw, _ := os.ReadFile(filepath.Join(s.o.walkDir, autoEventsFile))
	return string(raw)
}

// finishAttempt advances the supervisor until the id's current attempt has finished and its decision is applied,
// then skips the restart backoff (the backoff itself is pinned by its own test).
func finishAttempt(t *testing.T, s *autoSupervisor) {
	t.Helper()
	tickUntil(t, s, "the attempt to finish", func() bool {
		st := s.states[0]
		st.mu.Lock()
		defer st.mu.Unlock()
		return st.run == nil || st.escalated
	})
	clearWait(s.states[0])
}

// clearWait skips the restart backoff of one id.
func clearWait(st *autoState) {
	st.mu.Lock()
	st.nextAt = time.Time{}
	st.mu.Unlock()
}

// runState is one id's done/escalated flags.
func runState(st *autoState) (done, escalated bool) {
	st.mu.Lock()
	defer st.mu.Unlock()
	return st.done, st.escalated
}

// TestAutoCommandArgs pins the managed flags: the target's own flags, the goal's until, the attempt's out, and the
// restart's --from/--place/--reuse-instance. H1: no --reuse-instance without --from/--place.
func TestAutoCommandArgs(t *testing.T) {
	target := raidwalk.AutoTarget{
		ID: "a", Server: "tortoise-test2", Slot: "1", Holder: "zg-a",
		Goal: raidwalk.AutoGoal{Kind: "boss", Boss: 14515},
		Args: []string{"zul_gurub", "--dir", "/plans", "--hires", "/roster.json", "--sql", "/zg.sql"},
	}
	args := autoCommandArgs("/bin/ta", target, "/walk/a-1", "step 405", true, 0)
	got := strings.Join(args, " ")
	want := "raid-walk zul_gurub --dir /plans --hires /roster.json --sql /zg.sql --server tortoise-test2 --slot 1 --holder zg-a --until boss-done --out /walk/a-1 --from step 405 --place --reuse-instance"
	if got != want {
		t.Fatalf("args:\n%s\nwant:\n%s", got, want)
	}
	if strings.Contains(got, "--instance-cap-unsafe") || strings.Contains(got, "--inject") {
		t.Fatal("an unsafe flag was added")
	}
	// A first placement has no --from, no --place and no reuse.
	args = autoCommandArgs("/bin/ta", target, "/walk/a-1", "", false, 0)
	got = strings.Join(args, " ")
	if strings.Contains(got, "--from") || strings.Contains(got, "--place") || strings.Contains(got, "--reuse-instance") {
		t.Fatalf("first placement has managed restart flags: %s", got)
	}
	// H1: reuse without a start would make raid-walk refuse the command: it is never sent.
	args = autoCommandArgs("/bin/ta", target, "/walk/a-1", "", true, 0)
	if got = strings.Join(args, " "); strings.Contains(got, "--reuse-instance") || strings.Contains(got, "--place") {
		t.Fatalf("reuse without --from was sent: %s", got)
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

// TestAutoCapFreeTime pins the instance-cap wait as "not a stall", and only while it is the run's last line (M4): a
// wait the walk has moved past no longer holds the stall clock back.
func TestAutoCapFreeTime(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "run.log")
	os.WriteFile(path, []byte("10:00:00.000 instance cap: waiting 16m5s (earliest free time 2026-11-03T12:34:56Z) for the oldest of 4 to age out of the hour\n"), 0o600)
	at := autoCapFreeTime(path)
	if at.IsZero() || at.Format("2006-01-02T15:04:05Z") != "2026-11-03T12:34:56Z" {
		t.Fatalf("free time = %s", at)
	}
	// The hire-cap wait carries the same line (M4).
	os.WriteFile(path, []byte("10:00:00.000 instance cap: waiting 4m0s (earliest free time 2026-11-03T12:05:00Z) for a hired account's oldest instance to age out of the hour\n"), 0o600)
	if at := autoCapFreeTime(path); at.Format("2006-01-02T15:04:05Z") != "2026-11-03T12:05:00Z" {
		t.Fatalf("hire-cap free time = %s", at)
	}
	// A line after the wait means the walk moved on: the wait no longer delays the stall rule.
	os.WriteFile(path, []byte("10:00:00.000 instance cap: waiting 16m5s (earliest free time 2026-11-03T12:34:56Z) for the oldest of 4 to age out of the hour\n10:16:00.000 TBM:RUN|zul_gurub|running|51/761|jeklik|boss|begin|fighting\n"), 0o600)
	if at := autoCapFreeTime(path); !at.IsZero() {
		t.Fatalf("a wait the walk moved past still held the stall clock: %s", at)
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
	if s.states[0].lastOut != filepath.Join(dir, "a-10") {
		t.Fatalf("lastOut = %q, want a-10", s.states[0].lastOut)
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

// H1: a restart with no step and no target --from is a fresh start; --reuse-instance without --place/--from would be
// refused by raid-walk, so it is never sent. The real launch -> exit -> next-args path runs the stub.
func TestAutoRestartWithoutFromNeverReuses(t *testing.T) {
	f := newAutoFixture(t)
	f.target("a srv 1 zg-a boss:14515 -- zul_gurub --sql /zg.sql")
	f.attempt("a-1", 5, 0, "server closed the connection")
	f.attempt("a-2", 5, 0, "server closed the connection")
	f.attempt("a-3", 2, 0, "bad --sql")
	s := f.supervisor()
	for i := 1; i <= 3; i++ {
		tickUntil(t, s, fmt.Sprintf("launch %d", i), func() bool { return len(f.args()) >= i })
		finishAttempt(t, s)
	}
	args := f.args()
	if len(args) != 3 {
		t.Fatalf("%d launches, want 3:\n%s", len(args), strings.Join(args, "\n"))
	}
	for i, a := range args[1:] {
		if strings.Contains(a, "--from") || strings.Contains(a, "--place") || strings.Contains(a, "--reuse-instance") {
			t.Fatalf("attempt %d reuses without a start: %s", i+2, a)
		}
	}
}

// H1: a restart starts at the furthest step the run reached, with --place and --reuse-instance.
func TestAutoRestartFromTheLastReachedStep(t *testing.T) {
	f := newAutoFixture(t)
	f.target("a srv 1 zg-a boss:14515 -- zul_gurub --sql /zg.sql")
	f.attempt("a-1", 5, 77, "server closed the connection")
	f.attempt("a-2", 2, 0, "bad --sql")
	s := f.supervisor()
	tickUntil(t, s, "the first launch", func() bool { return len(f.args()) >= 1 })
	finishAttempt(t, s)
	tickUntil(t, s, "the restart", func() bool { return len(f.args()) >= 2 })
	args := f.args()
	if len(args) != 2 {
		t.Fatalf("%d launches, want 2:\n%s", len(args), strings.Join(args, "\n"))
	}
	if !strings.Contains(args[1], "--from step 77 --place --reuse-instance") {
		t.Fatalf("restart did not start at step 77: %s", args[1])
	}
}

// H1: with no step reached, a restart starts at the target's own --from, still in the same instance.
func TestAutoRestartFromTheTargetFrom(t *testing.T) {
	f := newAutoFixture(t)
	f.target("a srv 1 zg-a boss:14515 -- zul_gurub --sql /zg.sql --from step 405")
	f.attempt("a-1", 5, 0, "server closed the connection")
	f.attempt("a-2", 2, 0, "bad --sql")
	s := f.supervisor()
	tickUntil(t, s, "the first launch", func() bool { return len(f.args()) >= 1 })
	finishAttempt(t, s)
	tickUntil(t, s, "the restart", func() bool { return len(f.args()) >= 2 })
	args := f.args()
	if len(args) != 2 || !strings.Contains(args[1], "--from step 405 --place --reuse-instance") {
		t.Fatalf("restart did not use the target's --from:\n%s", strings.Join(args, "\n"))
	}
}

// H1 goal advance: the next goal's own --from is placed in the same instance; without one it is a fresh start.
func TestAutoGoalAdvanceUsesTheNewGoalsStart(t *testing.T) {
	f := newAutoFixture(t)
	f.target("a srv 1 zg-a boss:14515 -- zul_gurub --sql /zg.sql")
	f.target("a srv 1 zg-a boss:14507 -- zul_gurub --sql /zg.sql --from step 200")
	f.attempt("a-1", 0, 417, "boss 14515 down")
	f.attempt("a-2", 0, 300, "boss 14507 down")
	code := f.run()
	args := f.args()
	if len(args) != 2 {
		t.Fatalf("%d launches, want 2:\n%s", len(args), strings.Join(args, "\n"))
	}
	if !strings.Contains(args[1], "--from step 200 --place --reuse-instance") {
		t.Fatalf("the advance did not place at the new goal's start: %s", args[1])
	}
	if !strings.Contains(f.events(), "a advance: goal met: boss:14515") {
		t.Fatalf("no advance event:\n%s", f.events())
	}
	// Every id met its goals: the global done event and exit 0.
	if code != exitOK {
		t.Fatalf("exit %d after every goal was met, want 0", code)
	}
	if !strings.Contains(f.events(), "all done: every target's goals are met") {
		t.Fatalf("no global done event:\n%s", f.events())
	}
}

// H1: exit 2 is the walk refusing its arguments: the id stops with an ESCALATE line, not a silent done.
func TestAutoExitTwoEscalates(t *testing.T) {
	f := newAutoFixture(t)
	f.target("a srv 1 zg-a boss:14515 -- zul_gurub --sql /zg.sql")
	f.attempt("a-1", 2, 0, "bad --sql")
	if code := f.run(); code != exitAutoEscalated {
		t.Fatalf("exit %d, want %d (an escalated id did not meet its goal)", code, exitAutoEscalated)
	}
	if args := f.args(); len(args) != 1 {
		t.Fatalf("exit 2 was retried: %d launches", len(args))
	}
	for _, path := range []string{f.events(), f.statusFile()} {
		if !strings.Contains(path, "ESCALATE stop a 1") || !strings.Contains(path, "exit 2") {
			t.Fatalf("%s has no ESCALATE stop line:\n%s", path, path)
		}
	}
	if strings.Contains(f.events(), "all done: every target's goals are met") {
		t.Fatalf("an escalated id produced the global done event:\n%s", f.events())
	}
	if !strings.Contains(f.events(), "not every target's goals are met (1 escalated, 0 refused)") {
		t.Fatalf("the final line does not explain the non-zero exit:\n%s", f.events())
	}
}

// Exit 29 is the placement failing every try in the same instance: the id stops with an ESCALATE line and no
// fresh-instance retry (walk 37 burned four fresh instances an hour this way).
func TestAutoExit29EscalatesWithoutRetry(t *testing.T) {
	f := newAutoFixture(t)
	f.target("a srv 1 zg-a boss:14515 -- zul_gurub --sql /zg.sql --from step 582")
	f.attempt("a-1", 29, 0, "place: 4 tries in the same instance, start step 582")
	f.run()
	if args := f.args(); len(args) != 1 {
		t.Fatalf("exit 29 was retried into a fresh instance: %d launches", len(args))
	}
	if !strings.Contains(f.events(), "ESCALATE stop a 1") || !strings.Contains(f.events(), "no fresh-instance retry") {
		t.Fatalf("no ESCALATE stop for exit 29:\n%s", f.events())
	}
}

func (f *autoFixture) statusFile() string {
	raw, _ := os.ReadFile(filepath.Join(f.walk, autoStatusFile))
	return string(raw)
}

// M1: exit 15 three times stops the id with an ESCALATE line (the slot's lease is held by another holder).
func TestAutoLeaseHeldThreeTimesEscalates(t *testing.T) {
	f := newAutoFixture(t)
	f.target("a srv 1 zg-a boss:14515 -- zul_gurub --sql /zg.sql")
	for i := 1; i <= 3; i++ {
		f.attempt(fmt.Sprintf("a-%d", i), 15, 0, "lease: srv/1 is not leased by zg-a")
	}
	s := f.supervisor()
	for i := 1; i <= 3; i++ {
		tickUntil(t, s, fmt.Sprintf("launch %d", i), func() bool { return len(f.args()) >= i })
		tickUntil(t, s, fmt.Sprintf("attempt %d to finish", i), func() bool {
			st := s.states[0]
			st.mu.Lock()
			defer st.mu.Unlock()
			return st.run == nil
		})
		// Skip the backoff: this test is about the three strikes, not the wait.
		clearWait(s.states[0])
	}
	if args := f.args(); len(args) != 3 {
		t.Fatalf("%d launches, want 3 (the third stops the id)", len(args))
	}
	if _, escalated := runState(s.states[0]); !escalated {
		t.Fatalf("three exit 15s did not escalate:\n%s", f.events())
	}
	if !strings.Contains(f.events(), "ESCALATE stop a 1") || !strings.Contains(f.events(), "exit 15 three times") {
		t.Fatalf("no ESCALATE stop for the lease:\n%s", f.events())
	}
}

// M1: the restart backoff follows the consecutive no-progress attempts and resets when an attempt reaches a further
// step (the event line carries the wait).
func TestAutoBackoffGrowsAndResetsOnProgress(t *testing.T) {
	f := newAutoFixture(t)
	f.target("a srv 1 zg-a boss:14515 -- zul_gurub --sql /zg.sql")
	f.attempt("a-1", 5, 0, "server closed the connection")
	f.attempt("a-2", 5, 0, "server closed the connection")
	f.attempt("a-3", 5, 40, "server closed the connection")
	f.attempt("a-4", 2, 40, "bad --sql")
	s := f.supervisor()
	st := s.states[0]
	wants := []time.Duration{20 * time.Second, time.Minute, 20 * time.Second}
	for i := 1; i <= 3; i++ {
		tickUntil(t, s, fmt.Sprintf("launch %d", i), func() bool { return len(f.args()) >= i })
		tickUntil(t, s, fmt.Sprintf("attempt %d to finish", i), func() bool {
			st.mu.Lock()
			defer st.mu.Unlock()
			return st.run == nil
		})
		st.mu.Lock()
		wait := time.Until(st.nextAt)
		st.nextAt = time.Time{}
		st.mu.Unlock()
		if wait < wants[i-1]-5*time.Second || wait > wants[i-1]+time.Second {
			t.Fatalf("attempt %d restarts after %s, want %s", i, wait.Round(time.Second), wants[i-1])
		}
	}
	// The fourth attempt exits 2: the id stops with an ESCALATE line instead of restarting.
	tickUntil(t, s, "the fourth launch", func() bool { return len(f.args()) >= 4 })
	tickUntil(t, s, "the escalation", func() bool { _, esc := runState(st); return esc })
}

// M3: a boss the summary says went down advances whatever the exit code (19 included).
func TestAutoBossKillWithIncidentAdvances(t *testing.T) {
	f := newAutoFixture(t)
	f.target("a srv 1 zg-a boss:14515 -- zul_gurub --sql /zg.sql")
	f.target("a srv 1 zg-a boss:14507 -- zul_gurub --sql /zg.sql")
	f.attempt("a-1", 19, 417, "boss 14515 down")
	f.attempt("a-2", 2, 0, "bad --sql")
	f.run()
	args := f.args()
	if len(args) != 2 {
		t.Fatalf("%d launches, want 2 (the kill advanced):\n%s", len(args), strings.Join(args, "\n"))
	}
	if !strings.Contains(f.events(), "advance: goal met: boss:14515") {
		t.Fatalf("no advance after the exit-19 kill:\n%s", f.events())
	}
}

// The state file is written on every transition; a supervisor killed after goal 1 was met does not walk it again.
func TestAutoStateSurvivesASupervisorKill(t *testing.T) {
	f := newAutoFixture(t)
	f.target("a srv 1 zg-a boss:14515 -- zul_gurub --sql /zg.sql")
	f.target("a srv 1 zg-a boss:14507 -- zul_gurub --sql /zg.sql --from step 200")
	f.attempt("a-1", 0, 417, "boss 14515 down")
	f.running("a-2", 60)
	f.attempt("a-3", 2, 200, "bad --sql")
	f.lease("", "")

	cmd := exec.Command(os.Args[0], "raid-autopilot", "--foreground", "--binary", f.stub, "--poll", "5ms", "--settle", "0", "--status-every", "1h", f.walk)
	cmd.Env = append(f.env, "AUTO_TEST_CHILD=1")
	var log bytes.Buffer
	cmd.Stdout, cmd.Stderr = &log, &log
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	defer func() {
		_ = cmd.Process.Kill()
		_ = cmd.Wait()
	}()
	waitForN(t, 10*time.Second, "the second attempt to launch", func() bool { return len(f.args()) >= 2 })
	// Kill the supervisor while a-2 runs; its walk is killed too, as a crash would leave it dead on the slot.
	if err := cmd.Process.Kill(); err != nil {
		t.Fatal(err)
	}
	_ = cmd.Wait()
	waitForN(t, 10*time.Second, "a-2's walk pid", func() bool { return autoReadPID(filepath.Join(f.walk, "a-2", "walk.pid")) > 0 })
	if pid := autoReadPID(filepath.Join(f.walk, "a-2", "walk.pid")); pid > 0 {
		_ = syscall.Kill(-pid, syscall.SIGKILL)
		waitForN(t, 10*time.Second, "a-2's walk to end", func() bool { return !processAlive(pid) })
	}
	if len(f.args()) < 2 {
		t.Fatalf("only %d launches before the kill:\n%s", len(f.args()), strings.Join(f.args(), "\n"))
	}
	raw, err := os.ReadFile(filepath.Join(f.walk, autoStateFile))
	if err != nil {
		t.Fatalf("no state file after the kill: %v", err)
	}
	var saved autoSavedState
	if err := json.Unmarshal(raw, &saved); err != nil {
		t.Fatal(err)
	}
	if len(saved.IDs) != 1 || saved.IDs[0].Goal != 1 || saved.IDs[0].GoalsDone != 1 {
		t.Fatalf("state after the kill = %+v, want goal 1 and 1 goal done", saved.IDs)
	}
	before := len(f.args())
	// Restart: the reconciled state starts goal 2, never goal 1 again.
	f.run()
	after := f.args()[before:]
	if len(after) == 0 {
		t.Fatalf("the restarted supervisor launched nothing:\n%s", f.events())
	}
	for _, a := range after {
		if !strings.Contains(a, "--from step 200") {
			t.Fatalf("the restarted supervisor walked goal 1 again: %s", a)
		}
	}
	if got := strings.Count(f.events(), "advance: goal met: boss:14515"); got != 1 {
		t.Fatalf("goal 1 was met %d times (want once):\n%s", got, f.events())
	}
}

// A crash between a met goal's summary and the checkpoint: the start reconciles the summary and advances instead of
// walking the goal again.
func TestAutoStartReconcilesAMetGoal(t *testing.T) {
	f := newAutoFixture(t)
	f.target("a srv 1 zg-a boss:14515 -- zul_gurub --sql /zg.sql")
	f.target("a srv 1 zg-a boss:14507 -- zul_gurub --sql /zg.sql --from step 200")
	f.attempt("a-1", 0, 417, "boss 14515 down")
	f.attempt("a-10", 2, 200, "bad --sql")
	// The saved attempt met goal 1, but the checkpoint still says goal 0 (the crash came before its write).
	f.attempt("a-9", 0, 417, "boss 14515 down")
	out := filepath.Join(f.walk, "a-9")
	if err := os.MkdirAll(filepath.Join(out, "slot"), 0o700); err != nil {
		t.Fatal(err)
	}
	raw, _ := os.ReadFile(filepath.Join(f.specDir, "a-9.summary"))
	os.WriteFile(filepath.Join(out, "summary.json"), raw, 0o600)
	os.WriteFile(filepath.Join(out, "run.log"), []byte("19:46:05.696 TBM:RUN|zul_gurub|running|417/761|jeklik|boss|begin|fighting\n"), 0o600)
	saved := autoSavedState{Format: autoStateFormat, Version: autoStateVersion, IDs: []autoSavedID{{
		ID: "a", Goal: 0, GoalsDone: 0, LastStep: 417, Reuse: true, Attempt: 1, OutDir: out,
	}}}
	raw, _ = json.Marshal(saved)
	os.WriteFile(filepath.Join(f.walk, autoStateFile), raw, 0o600)

	f.run()
	args := f.args()
	if len(args) == 0 || !strings.Contains(args[0], "--from step 200") {
		t.Fatalf("the reconciled start walked goal 1 again:\n%s", strings.Join(args, "\n"))
	}
	if !strings.Contains(f.events(), "reconcile: goal boss:14515 was met before the restart") {
		t.Fatalf("no reconcile event:\n%s", f.events())
	}
}

// H2: a live walk of ours on the slot is adopted, not relaunched beside; when it exits, the supervisor takes over.
func TestAutoStartAdoptsALiveWalk(t *testing.T) {
	f := newAutoFixture(t)
	f.target("a srv 1 zg-a boss:14515 -- zul_gurub --sql /zg.sql")
	f.lease("zg-a", "held")
	// A live walk in a-1, as a crashed supervisor would have left it.
	out := filepath.Join(f.walk, "a-1")
	os.MkdirAll(filepath.Join(out, "slot"), 0o700)
	os.WriteFile(filepath.Join(out, "slot", "slot.json"), []byte(fmt.Sprintf(`{"server":"srv","slot":"1","holder":"zg-a","slots_path":%q}`, f.slotsPath)), 0o600)
	os.WriteFile(filepath.Join(out, "run.log"), []byte("19:46:05.696 TBM:RUN|zul_gurub|running|51/761|jeklik|boss|begin|fighting\n"), 0o600)
	os.WriteFile(filepath.Join(out, "summary.json"), []byte(`{"exit_code":5,"steps_reached":51,"steps_total":761,"end_reason":"server closed the connection"}`), 0o600)
	live := exec.Command("sleep", "30")
	live.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	if err := live.Start(); err != nil {
		t.Fatal(err)
	}
	defer func() { _ = live.Process.Kill(); _ = live.Wait() }()
	os.WriteFile(filepath.Join(out, "walk.pid"), []byte(strconv.Itoa(live.Process.Pid)+"\n"), 0o600)
	saved := autoSavedState{Format: autoStateFormat, Version: autoStateVersion, IDs: []autoSavedID{{
		ID: "a", Goal: 0, LastStep: 51, Reuse: true, Attempt: 1, ChildPID: live.Process.Pid, OutDir: out,
	}}}
	raw, _ := json.Marshal(saved)
	os.WriteFile(filepath.Join(f.walk, autoStateFile), raw, 0o600)

	s := f.supervisor()
	if len(f.args()) != 0 {
		t.Fatalf("a walk was launched beside the live one:\n%s", strings.Join(f.args(), "\n"))
	}
	st := s.states[0]
	st.mu.Lock()
	adopted := st.run != nil && st.run.adopted && st.run.pidN == live.Process.Pid
	st.mu.Unlock()
	if !adopted {
		t.Fatalf("the live walk was not adopted:\n%s", f.events())
	}
	if !strings.Contains(f.events(), "a adopt: watching the live walk pid") {
		t.Fatalf("no adopt event:\n%s", f.events())
	}
	// The walk ends (its own teardown releases the lease): the adopted run is seen as exited, the goal is not met,
	// so it is torn down and re-placed from the step it reached.
	f.attempt("a-2", 2, 51, "bad --sql")
	_ = live.Process.Kill()
	_ = live.Wait()
	f.lease("", "")
	finishAttempt(t, s)
	tickUntil(t, s, "the adopted run's re-place", func() bool { return len(f.args()) >= 1 })
	args := f.args()
	if len(args) != 1 || !strings.Contains(args[0], "a-2") {
		t.Fatalf("the adopted exit did not re-place:\n%s", strings.Join(args, "\n"))
	}
	if !strings.Contains(args[0], "--from step 51 --place --reuse-instance") {
		t.Fatalf("the re-place did not start at the adopted step: %s", args[0])
	}
}

// H2: a slot whose lease is held by another holder refuses its id with an ESCALATE line and launches nothing.
func TestAutoStartRefusesAForeignSlot(t *testing.T) {
	f := newAutoFixture(t)
	f.target("a srv 1 zg-a boss:14515 -- zul_gurub --sql /zg.sql")
	f.lease("someone-else", "held")
	if code := f.run(); code != exitOK {
		t.Fatalf("exit %d, want 0", code)
	}
	if args := f.args(); len(args) != 0 {
		t.Fatalf("a walk was launched on a foreign lease:\n%s", strings.Join(args, "\n"))
	}
	if !strings.Contains(f.events(), "ESCALATE start a 1") || !strings.Contains(f.events(), "held by someone-else") {
		t.Fatalf("no ESCALATE start line:\n%s", f.events())
	}
}

// L5: an escalated id counts as done (the loop ends instead of spinning), and status.txt exists from the start.
func TestAutoEscalatedCountsAsDoneAndFirstStatus(t *testing.T) {
	f := newAutoFixture(t)
	f.target("a srv 1 zg-a boss:14515 -- zul_gurub --sql /zg.sql")
	f.attempt("a-1", 2, 0, "bad --sql")
	start := time.Now()
	if code := f.run(); code != exitAutoEscalated {
		t.Fatalf("exit %d, want %d", code, exitAutoEscalated)
	}
	if time.Since(start) > 10*time.Second {
		t.Fatal("the escalated id kept the supervisor spinning")
	}
	if status := f.statusFile(); !strings.Contains(status, "ESCALATE stop a 1") {
		t.Fatalf("status.txt has no ESCALATE line:\n%s", status)
	}
}

// M6: a YIELD-DONE from an earlier YIELD is removed at start.
func TestAutoStaleYieldDoneRemovedAtStart(t *testing.T) {
	f := newAutoFixture(t)
	f.target("a srv 1 zg-a boss:14515 -- zul_gurub --sql /zg.sql")
	f.attempt("a-1", 2, 0, "bad --sql")
	yieldDone := filepath.Join(f.walk, autoYieldDone)
	if err := os.WriteFile(yieldDone, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	f.run()
	if _, err := os.Stat(yieldDone); err == nil {
		t.Fatal("the stale YIELD-DONE survived the start")
	}
}

// M6: a YIELD removes the stale YIELD-DONE first; a YIELD whose teardown fails writes none, so no stale one can be
// mistaken for this run's completion.
func TestAutoYieldFailedTeardownWritesNoYieldDone(t *testing.T) {
	f := newAutoFixture(t)
	f.target("a srv 1 zg-a boss:14515 -- zul_gurub --sql /zg.sql")
	f.running("a-1", 60)
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	done := make(chan int, 1)
	go func() {
		done <- raidAutopilotCmd(ctx, []string{"--foreground", "--binary", f.stub, "--poll", "50ms", "--settle", "0", "--status-every", "1h", f.walk}, io.Discard, io.Discard)
	}()
	waitForN(t, 10*time.Second, "the walk to launch", func() bool { return len(f.args()) >= 1 })
	yieldDone := filepath.Join(f.walk, autoYieldDone)
	if err := os.WriteFile(yieldDone, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(f.failTear, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(f.walk, autoYieldFile), nil, 0o600); err != nil {
		t.Fatal(err)
	}
	select {
	case <-done:
	case <-time.After(10 * time.Second):
		t.Fatal("the supervisor did not exit after the YIELD")
	}
	if _, err := os.Stat(yieldDone); err == nil {
		t.Fatalf("a YIELD-DONE was left after a failed teardown:\n%s", f.events())
	}
	if !strings.Contains(f.events(), "no YIELD-DONE") {
		t.Fatalf("no explanation for the missing YIELD-DONE:\n%s", f.events())
	}
}

// M7: a YIELD reaches every walk and the supervisor exits within 5 s, even while another slot's teardown is slow.
func TestAutoYieldWithinFiveSeconds(t *testing.T) {
	f := newAutoFixture(t)
	f.target("a srv 1 zg-a boss:14515 -- zul_gurub --sql /zg.sql")
	f.target("b srv 2 zg-b boss:14515 -- zul_gurub --sql /zg.sql")
	f.running("a-1", 60)
	f.running("b-1", 60)
	// a's teardown is slow; b's is not: the YIELD must still finish a and b together, within 5 s of the file.
	if err := os.WriteFile(f.slowTear, []byte(filepath.Join(f.walk, "a-1", "slot")), 0o600); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	done := make(chan int, 1)
	go func() {
		done <- raidAutopilotCmd(ctx, []string{"--foreground", "--binary", f.stub, "--poll", "50ms", "--settle", "0", "--status-every", "1h", f.walk}, io.Discard, io.Discard)
	}()
	waitForN(t, 10*time.Second, "both walks to launch", func() bool { return len(f.args()) >= 2 })
	pids := make([]int, 2)
	for i, id := range []string{"a-1", "b-1"} {
		waitForN(t, 10*time.Second, id+"'s walk pid", func() bool {
			pids[i] = autoReadPID(filepath.Join(f.walk, id, "walk.pid"))
			return pids[i] > 0
		})
	}
	if err := os.WriteFile(filepath.Join(f.walk, autoYieldFile), nil, 0o600); err != nil {
		t.Fatal(err)
	}
	// The requirement is that the YIELD reaches every walk within 5 s: both pids are gone, even while one slot's
	// teardown is slow. The teardowns and YIELD-DONE follow and are bounded by the slow teardown instead.
	waitForN(t, 5*time.Second, "both walks to stop", func() bool {
		for _, pid := range pids {
			if processAlive(pid) {
				return false
			}
		}
		return true
	})
	waitForN(t, 20*time.Second, "the YIELD-DONE", func() bool {
		_, err := os.Stat(filepath.Join(f.walk, autoYieldDone))
		return err == nil
	})
	select {
	case code := <-done:
		if code != exitOK {
			t.Fatalf("exit %d after YIELD", code)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("the supervisor did not exit after YIELD-DONE")
	}
}

// M7: one slot's slow teardown does not hold another id's teardown back.
func TestAutoSlowTeardownDoesNotBlockAnotherID(t *testing.T) {
	f := newAutoFixture(t)
	f.target("a srv 1 zg-a boss:14515 -- zul_gurub --sql /zg.sql")
	f.target("b srv 2 zg-b boss:14515 -- zul_gurub --sql /zg.sql")
	f.attempt("a-1", 5, 0, "server closed the connection")
	f.attempt("b-1", 5, 0, "server closed the connection")
	f.attempt("a-2", 2, 0, "bad --sql")
	f.attempt("b-2", 2, 0, "bad --sql")
	if err := os.WriteFile(f.slowTear, []byte(filepath.Join(f.walk, "a-1", "slot")), 0o600); err != nil {
		t.Fatal(err)
	}
	s := f.supervisor()
	tickUntil(t, s, "both launches", func() bool { return len(f.args()) >= 2 })
	waitForN(t, 10*time.Second, "both exits", func() bool {
		for _, st := range s.states {
			st.mu.Lock()
			exited := st.run != nil && st.run.exited
			st.mu.Unlock()
			if !exited {
				return false
			}
		}
		return true
	})
	s.tick(context.Background())
	waitForN(t, 10*time.Second, "b's teardown", func() bool {
		for _, line := range f.teardowns() {
			if strings.Contains(line, "b-1/slot") && strings.HasPrefix(line, "done") {
				return true
			}
		}
		return false
	})
	// b's teardown finished while a's is still sleeping.
	aDone := false
	for _, line := range f.teardowns() {
		if strings.HasPrefix(line, "done") && strings.Contains(line, "a-1/slot") {
			aDone = true
		}
	}
	if aDone {
		t.Fatal("a's slow teardown finished before b's: the test did not exercise the overlap")
	}
	s.wg.Wait()
}

// L1: a walk's group still has a live child after the leader exits, so processGone stays false until the group is gone.
func TestAutoProcessGoneChecksTheGroup(t *testing.T) {
	cmd := exec.Command("sh", "-c", "sleep 30 & exit 0")
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	pid := cmd.Process.Pid
	if err := cmd.Wait(); err != nil {
		t.Fatalf("the leader did not exit: %v", err)
	}
	if processGone(pid, true) {
		t.Fatal("processGone called the group gone while a child still runs")
	}
	_ = syscall.Kill(-pid, syscall.SIGKILL)
	waitForN(t, 10*time.Second, "the group to end", func() bool { return processGone(pid, true) })
}

// L2: the detached launcher waits until the child holds the lock (its pid file names it) and exits 0; when another
// supervisor holds the lock, the child's refusal is what the caller gets.
func TestAutoDetachWaitsForTheLock(t *testing.T) {
	f := newAutoFixture(t)
	f.target("a srv 1 zg-a boss:14515 -- zul_gurub --sql /zg.sql")
	f.running("a-1", 30)
	saved := os.Args
	defer func() { os.Args = saved }()
	os.Args = []string{os.Args[0], "raid-autopilot", "--binary", f.stub, "--poll", "20ms", "--settle", "0", "--status-every", "1h", f.walk}
	oldEnv := os.Getenv("AUTO_TEST_CHILD")
	os.Setenv("AUTO_TEST_CHILD", "1")
	defer os.Setenv("AUTO_TEST_CHILD", oldEnv)
	// The launcher's child needs the fixture env; the test process has it, so autoDetach's os.Environ() carries it.

	var out, errOut bytes.Buffer
	code := autoDetach(f.opts(), &out, &errOut)
	if code != exitOK {
		t.Fatalf("detach exit %d: %s", code, errOut.String())
	}
	pidRaw, err := os.ReadFile(filepath.Join(f.walk, autoPIDFile))
	if err != nil {
		t.Fatalf("the launcher returned before the pid file: %v", err)
	}
	pid, _ := strconv.Atoi(strings.TrimSpace(string(pidRaw)))
	if pid <= 0 || !processAlive(pid) {
		t.Fatalf("pid file names %d, not a live supervisor", pid)
	}
	defer func() { _ = syscall.Kill(-pid, syscall.SIGKILL) }()
	// The refusal path: the first supervisor ends, then this process holds the lock and the next child reports it.
	_ = syscall.Kill(-pid, syscall.SIGKILL)
	waitForN(t, 10*time.Second, "the first supervisor to end", func() bool { return !processAlive(pid) })
	lock, err := autoLock(f.walk)
	if err != nil {
		t.Fatal(err)
	}
	defer lock.Close()
	out.Reset()
	errOut.Reset()
	if code := autoDetach(f.opts(), &out, &errOut); code == exitOK {
		t.Fatalf("the launcher returned 0 although the child was refused:\n%s", errOut.String())
	}
	if !strings.Contains(errOut.String(), "another autopilot runs") {
		t.Fatalf("the refusal was not reported:\n%s", errOut.String())
	}
}

// A failed teardown is not completion: the run stays active, nothing is relaunched, and YIELD writes no YIELD-DONE.
func TestAutoTeardownFailureKeepsTheRunActive(t *testing.T) {
	f := newAutoFixture(t)
	f.target("a srv 1 zg-a boss:14515 -- zul_gurub --sql /zg.sql")
	f.attempt("a-1", 5, 51, "server closed the connection")
	if err := os.WriteFile(f.failTear, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	s := f.supervisor()
	tickUntil(t, s, "the first launch", func() bool { return len(f.args()) >= 1 })
	tickUntil(t, s, "the failed teardown", func() bool {
		st := s.states[0]
		st.mu.Lock()
		defer st.mu.Unlock()
		return st.run != nil && st.run.teardownFails > 0
	})
	st := s.states[0]
	st.mu.Lock()
	kept := st.run != nil && !st.run.tornDown
	st.mu.Unlock()
	if !kept {
		t.Fatal("a failed teardown cleared the run")
	}
	if args := f.args(); len(args) != 1 {
		t.Fatalf("the supervisor relaunched after a failed teardown: %d launches", len(args))
	}
	if !strings.Contains(f.events(), "teardown-failed") {
		t.Fatalf("no teardown-failed event:\n%s", f.events())
	}
	s.shutdown("YIELD")
	if _, err := os.Stat(filepath.Join(f.walk, autoYieldDone)); err == nil {
		t.Fatal("YIELD-DONE written although the teardown failed")
	}
	if !strings.Contains(f.events(), "no YIELD-DONE") {
		t.Fatalf("the failed YIELD did not say why it left YIELD-DONE alone:\n%s", f.events())
	}
}

// The clean retry finishes the held-back restart and the run is relaunched.
func TestAutoTeardownRetryRelaunches(t *testing.T) {
	f := newAutoFixture(t)
	f.target("a srv 1 zg-a boss:14515 -- zul_gurub --sql /zg.sql")
	f.attempt("a-1", 5, 51, "server closed the connection")
	f.attempt("a-2", 2, 51, "bad --sql")
	if err := os.WriteFile(f.failTear, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	s := f.supervisor()
	tickUntil(t, s, "the first launch", func() bool { return len(f.args()) >= 1 })
	tickUntil(t, s, "the failed teardown", func() bool {
		st := s.states[0]
		st.mu.Lock()
		defer st.mu.Unlock()
		return st.run != nil && st.run.teardownFails > 0
	})
	if err := os.Remove(f.failTear); err != nil {
		t.Fatal(err)
	}
	finishAttempt(t, s)
	tickUntil(t, s, "the relaunch", func() bool { return len(f.args()) >= 2 })
	if !strings.Contains(f.events(), "a restart:") {
		t.Fatalf("the held-back restart was not applied:\n%s", f.events())
	}
	if !strings.Contains(f.args()[1], "--from step 51 --place --reuse-instance") {
		t.Fatalf("the relaunch did not continue from the reached step: %s", f.args()[1])
	}
}

// The third failed teardown tick escalates: the line goes to EVENTS.txt and status.txt, and the slot is left alone.
func TestAutoTeardownEscalatesAfterThreeFailedTicks(t *testing.T) {
	f := newAutoFixture(t)
	f.target("a srv 1 zg-a boss:14515 -- zul_gurub --sql /zg.sql")
	f.attempt("a-1", 5, 51, "server closed the connection")
	if err := os.WriteFile(f.failTear, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	s := f.supervisor()
	tickUntil(t, s, "the first launch", func() bool { return len(f.args()) >= 1 })
	tickUntil(t, s, "the escalation", func() bool { _, esc := runState(s.states[0]); return esc })
	// Each teardown call logs a start and a done line; every failed tick runs two calls (plain, then --release-anyway).
	if got := len(f.teardowns()); got != 4*autoTeardownTries {
		t.Fatalf("teardown log lines = %d, want %d (two calls per failed tick)", got, 4*autoTeardownTries)
	}
	for _, text := range []string{f.events(), f.statusFile()} {
		if !strings.Contains(text, "ESCALATE teardown a 1") {
			t.Fatalf("no ESCALATE teardown a 1 line:\n%s", text)
		}
	}
	// Left alone: no more teardown, no relaunch, no YIELD-DONE.
	s.tick(context.Background())
	if got := len(f.teardowns()); got != 4*autoTeardownTries {
		t.Fatalf("the escalated slot was torn down again (%d teardown log lines)", got)
	}
	if args := f.args(); len(args) != 1 {
		t.Fatalf("the escalated id was relaunched: %d launches", len(args))
	}
	s.shutdown("YIELD")
	if _, err := os.Stat(filepath.Join(f.walk, autoYieldDone)); err == nil {
		t.Fatal("YIELD-DONE written for an escalated slot")
	}
}

// A teardown that exits 0 is still a failure while the walk (or its group) is alive or the lease is held.
func TestAutoTeardownVerifiesTheWalkAndTheLease(t *testing.T) {
	f := newAutoFixture(t)
	f.target("a srv 1 zg-a boss:14515 -- zul_gurub --sql /zg.sql")
	s := f.supervisor()
	out := filepath.Join(f.walk, "a-1")
	if err := os.MkdirAll(filepath.Join(out, "slot"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(out, "slot", "slot.json"), []byte(`{"server":"srv","slot":"1","holder":"zg-a"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	live := exec.Command("sleep", "30")
	live.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	if err := live.Start(); err != nil {
		t.Fatal(err)
	}
	defer func() { _ = syscall.Kill(-live.Process.Pid, syscall.SIGKILL); _ = live.Wait() }()
	st := s.states[0]
	st.mu.Lock()
	r := &autoRun{out: out, slot: "1", pidN: live.Process.Pid}
	st.run = r
	st.mu.Unlock()
	if err := s.teardown(st, r); err == nil || !strings.Contains(err.Error(), "still alive") {
		t.Fatalf("a live walk passed the teardown: %v", err)
	}
	_ = syscall.Kill(-live.Process.Pid, syscall.SIGKILL)
	_ = live.Wait()
	st.mu.Lock()
	r.exited = true
	st.mu.Unlock()
	f.lease("zg-a", "held")
	if err := s.teardown(st, r); err == nil || !strings.Contains(err.Error(), "lease is still held") {
		t.Fatalf("a held lease passed the teardown: %v", err)
	}
	f.lease("", "")
	if err := s.teardown(st, r); err != nil {
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
	for range n {
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
	f := newAutoFixture(t)
	f.target("a srv 1 zg-a boss:14515 -- zul_gurub --sql /zg.sql")
	f.running("a-1", 30)
	ctx, cancel := context.WithTimeout(context.Background(), 300*time.Millisecond)
	defer cancel()
	codes := make(chan int, 2)
	var wg sync.WaitGroup
	ready := make(chan struct{})
	var mu sync.Mutex
	stderrs := map[int]string{}
	for i := range 2 {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			var out, errOut bytes.Buffer
			<-ready
			code := raidAutopilotCmd(ctx, []string{"--foreground", "--binary", f.stub, f.walk}, &out, &errOut)
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

// H1 launch: a launch that cannot start is not completion. It retries with the restart backoff and the third
// consecutive failure escalates the id (no done, no relaunch, exit 31 from the supervisor).
func TestAutoLaunchFailureRetriesThenEscalates(t *testing.T) {
	f := newAutoFixture(t)
	f.target("a srv 1 zg-a boss:14515 -- zul_gurub --sql /zg.sql")
	s := f.supervisor()
	// The startup reconciliation verifies the slot's lease with the stub; only the launches get the broken binary.
	s.o.binary = filepath.Join(f.dir, "missing-turtle-agent")
	st := s.states[0]
	for i := 1; i <= autoLaunchTries; i++ {
		tickUntil(t, s, fmt.Sprintf("launch failure %d", i), func() bool {
			st.mu.Lock()
			defer st.mu.Unlock()
			return st.launchFails >= i || st.escalated
		})
		st.mu.Lock()
		wait := time.Until(st.nextAt)
		escalated := st.escalated
		st.mu.Unlock()
		if i < autoLaunchTries {
			want := raidwalk.AutoBackoff(i - 1)
			if wait < want-5*time.Second || wait > want+time.Second {
				t.Fatalf("launch failure %d retries after %s, want %s", i, wait.Round(time.Second), want)
			}
		}
		if escalated && i < autoLaunchTries {
			t.Fatalf("launch failure %d escalated before %d failures:\n%s", i, autoLaunchTries, f.events())
		}
		clearWait(st)
	}
	done, escalated := runState(st)
	if done {
		t.Fatal("a launch failure was recorded as a met goal")
	}
	if !escalated {
		t.Fatalf("%d launch failures did not escalate:\n%s", autoLaunchTries, f.events())
	}
	events := f.events()
	if !strings.Contains(events, "a launch-failed:") || !strings.Contains(events, "retrying in 20s (fail 1 of 3)") {
		t.Fatalf("no launch-failed retry line:\n%s", events)
	}
	if !strings.Contains(events, "ESCALATE launch a 1") {
		t.Fatalf("no ESCALATE launch line:\n%s", events)
	}
	if got := len(f.args()); got != 0 {
		t.Fatalf("%d walks started although every launch failed", got)
	}
	if !strings.Contains(f.statusFile(), "ESCALATE launch a 1") {
		t.Fatalf("status.txt has no launch escalation:\n%s", f.statusFile())
	}
}

// A corrupt (truncated) checkpoint fails closed: the start refuses with exit 2, names the file and says to fix or
// remove it, and leaves the file untouched (a start must not replace it with empty progress).
func TestAutoCorruptCheckpointRefusesToStart(t *testing.T) {
	f := newAutoFixture(t)
	f.target("a srv 1 zg-a boss:14515 -- zul_gurub --sql /zg.sql")
	path := filepath.Join(f.walk, autoStateFile)
	truncated := []byte(`{"format":"tb-raid-autopilot-state","version":1,"ids":[{"id":"a","goal":1`)
	if err := os.WriteFile(path, truncated, 0o600); err != nil {
		t.Fatal(err)
	}
	var out, errOut bytes.Buffer
	code := raidAutopilotCmd(context.Background(), []string{"--foreground", "--binary", f.stub, "--poll", "5ms", f.walk}, &out, &errOut)
	if code != exitUsage {
		t.Fatalf("exit %d, want %d (refuse the corrupt checkpoint)", code, exitUsage)
	}
	for _, want := range []string{path, "fix or remove it"} {
		if !strings.Contains(errOut.String(), want) {
			t.Fatalf("the refusal does not name %q:\n%s", want, errOut.String())
		}
	}
	after, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("the corrupt checkpoint was removed: %v", err)
	}
	if !bytes.Equal(after, truncated) {
		t.Fatalf("the corrupt checkpoint was overwritten:\n%s", after)
	}
	if args := f.args(); len(args) != 0 {
		t.Fatalf("a walk was launched over a corrupt checkpoint:\n%s", strings.Join(args, "\n"))
	}
}

// M3: a fresh-instance decision is not lost when the goal has reached a step: the retry keeps --from step N --place
// but no --reuse-instance (the old bind is gone, and reuse would loop on it with minutes of backoff).
func TestAutoFreshInstanceAfterAFailedPlacementKeepsTheFrom(t *testing.T) {
	f := newAutoFixture(t)
	f.target("a srv 1 zg-a boss:14515 -- zul_gurub --sql /zg.sql")
	f.attempt("a-1", 5, 51, "server closed the connection")
	f.attempt("a-2", 1, 0, "place: --reuse-instance: the old bind is gone")
	f.attempt("a-3", 2, 51, "bad --sql")
	s := f.supervisor()
	tickUntil(t, s, "the first launch", func() bool { return len(f.args()) >= 1 })
	finishAttempt(t, s)
	tickUntil(t, s, "the reuse re-place", func() bool { return len(f.args()) >= 2 })
	finishAttempt(t, s)
	tickUntil(t, s, "the fresh re-place", func() bool { return len(f.args()) >= 3 })
	args := f.args()
	if !strings.Contains(args[1], "--from step 51 --place --reuse-instance") {
		t.Fatalf("the first re-place did not reuse: %s", args[1])
	}
	if !strings.Contains(args[2], "--from step 51 --place") || strings.Contains(args[2], "--reuse-instance") {
		t.Fatalf("the fresh-instance decision was lost: %s", args[2])
	}
}

// M3: three consecutive placement failures on one id escalate it and stop the id instead of looping forever.
func TestAutoPlacementFailuresEscalateAfterThree(t *testing.T) {
	f := newAutoFixture(t)
	f.target("a srv 1 zg-a boss:14515 -- zul_gurub --sql /zg.sql --from step 405")
	for i := 1; i <= 3; i++ {
		f.attempt(fmt.Sprintf("a-%d", i), 1, 0, "place: the old bind is gone")
	}
	s := f.supervisor()
	st := s.states[0]
	for i := 1; i <= 3; i++ {
		tickUntil(t, s, fmt.Sprintf("launch %d", i), func() bool { return len(f.args()) >= i })
		finishAttempt(t, s)
	}
	if args := f.args(); len(args) != 3 {
		t.Fatalf("%d launches, want 3:\n%s", len(args), strings.Join(args, "\n"))
	}
	if done, escalated := runState(st); done || !escalated {
		t.Fatalf("three placement failures: done=%v escalated=%v, want escalated only:\n%s", done, escalated, f.events())
	}
	if !strings.Contains(f.events(), "ESCALATE stop a 1") || !strings.Contains(f.events(), "place failed 3 times in a row") {
		t.Fatalf("no ESCALATE stop for the placements:\n%s", f.events())
	}
}
