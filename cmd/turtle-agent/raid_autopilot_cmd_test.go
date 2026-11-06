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

// stateByID is the supervisor's state of one id (nil once a removed target is dropped).
func (f *autoFixture) stateByID(s *autoSupervisor, id string) *autoState {
	s.statesMu.Lock()
	defer s.statesMu.Unlock()
	for _, st := range s.states {
		if st.id == id {
			return st
		}
	}
	return nil
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

// Review round 1: the rows cache is keyed by the migration's path, mtime and size, so a redeployed migration is read
// again (the resume step is then adjusted against the rows a walk now places against).
func TestAutoRowsCacheRereadsAChangedMigration(t *testing.T) {
	f := newAutoFixture(t)
	fallback, err := os.ReadFile("testdata/zg-place-fallback-20000000.sql")
	if err != nil {
		t.Fatal(err)
	}
	deployed, err := os.ReadFile("testdata/zg-20261101093000.sql")
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "walk.sql")
	if err := os.WriteFile(path, fallback, 0o600); err != nil {
		t.Fatal(err)
	}
	s := &autoSupervisor{o: autoOptions{walkDir: f.walk}}
	target := raidwalk.AutoTarget{ID: "a", Args: []string{"zul_gurub", "--sql", path}}
	first, ok := s.targetRows(target)
	if !ok || len(first.Steps) == 0 {
		t.Fatalf("the fixture migration was not read (ok=%v)", ok)
	}
	if again, ok := s.targetRows(target); !ok || len(again.Steps) != len(first.Steps) {
		t.Fatalf("a cache hit changed the rows: %d then %d", len(first.Steps), len(again.Steps))
	}
	if err := os.WriteFile(path, deployed, 0o600); err != nil {
		t.Fatal(err)
	}
	second, ok := s.targetRows(target)
	if !ok {
		t.Fatal("the redeployed migration was not read")
	}
	if len(second.Steps) == len(first.Steps) {
		t.Fatalf("the redeployed migration was served from the cache (%d steps)", len(second.Steps))
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

// Walk 39 t7-1: the summary credited Thekal's kill to entry 14599 with phase_entry 14509 (the reborn form). Both
// entries count for a goal, and a boss that was not killed (a guardian row) counts for neither.
func TestAutoBossKillCountsBothEntriesOfTheBoss(t *testing.T) {
	m := map[uint32]bool{}
	autoBossKill{Entry: 14599, Killed: true, PhaseEntry: 14509}.deadEntries(m)
	if !m[14599] || !m[14509] {
		t.Fatalf("a killed 14599/14509 record left %v", m)
	}
	plain := map[uint32]bool{}
	autoBossKill{Entry: 15068, Killed: true}.deadEntries(plain)
	if len(plain) != 1 || !plain[15068] {
		t.Fatalf("a plain record left %v", plain)
	}
	unkilled := map[uint32]bool{}
	autoBossKill{Entry: 14599, Killed: false, PhaseEntry: 14509}.deadEntries(unkilled)
	if len(unkilled) != 0 {
		t.Fatalf("an unkilled record counted as dead: %v", unkilled)
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

// Walk 39: every wipe at a boss resumed at the boss row the wipe died on. raid-walk refused that start (exit 2:
// "--place --from step N: step N is a boss row") and the autopilot escalated the id. The restart steps back to the
// nearest row of the same leg the walk takes (the fixture migration's Arlokk boss row is step 397, the move row 396).
func TestAutoRestartFromABossRowStepsBack(t *testing.T) {
	f := newAutoFixture(t)
	f.target("a srv 1 zg-a boss:14515 -- zul_gurub --sql testdata/zg-20261101093000.sql")
	f.attempt("a-1", 18, 397, "paused: 15 dead: 21")
	f.attempt("a-2", 2, 0, "bad --sql")
	s := f.supervisor()
	tickUntil(t, s, "the first launch", func() bool { return len(f.args()) >= 1 })
	finishAttempt(t, s)
	tickUntil(t, s, "the restart", func() bool { return len(f.args()) >= 2 })
	args := f.args()
	if len(args) != 2 {
		t.Fatalf("%d launches, want 2:\n%s", len(args), strings.Join(args, "\n"))
	}
	if !strings.Contains(args[1], "--from step 396 --place --reuse-instance") {
		t.Fatalf("the restart did not step back from the boss row 397: %s", args[1])
	}
	if !strings.Contains(f.events(), "a resume: \"step 397\" is not a place start: resuming from \"step 396\"") {
		t.Fatalf("no resume event:\n%s", f.events())
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

// Exit 29 on a same-instance (--reuse-instance) placement: the autopilot retries ONCE in a fresh instance (no
// --reuse-instance, same --from) and logs it; a second exit 29 stops the id with an ESCALATE line (walk 40: 45 ids
// stopped on the first one and a person re-added each).
func TestAutoExit29RetriesOnceInAFreshInstance(t *testing.T) {
	f := newAutoFixture(t)
	f.target("a srv 1 zg-a boss:14515 -- zul_gurub --sql /zg.sql")
	f.attempt("a-1", 5, 51, "server closed the connection")
	f.attempt("a-2", 29, 0, "place: 4 tries in the same instance, start step 51")
	f.attempt("a-3", 29, 0, "place: 4 tries in the same instance, start step 51")
	s := f.supervisor()
	tickUntil(t, s, "the first launch", func() bool { return len(f.args()) >= 1 })
	finishAttempt(t, s)
	tickUntil(t, s, "the reuse re-place", func() bool { return len(f.args()) >= 2 })
	finishAttempt(t, s)
	tickUntil(t, s, "the fresh-instance retry", func() bool { return len(f.args()) >= 3 })
	args := f.args()
	if !strings.Contains(args[1], "--from step 51 --place --reuse-instance") {
		t.Fatalf("the re-place did not reuse the instance: %s", args[1])
	}
	if !strings.Contains(args[2], "--from step 51 --place") || strings.Contains(args[2], "--reuse-instance") {
		t.Fatalf("the retry after exit 29 was not a fresh-instance placement: %s", args[2])
	}
	if !strings.Contains(f.events(), "fresh-instance retry after exit 29") {
		t.Fatalf("the retry is not logged:\n%s", f.events())
	}
	if done, escalated := runState(s.states[0]); done || escalated {
		t.Fatalf("one exit 29 stopped the id: done=%v escalated=%v", done, escalated)
	}
	// The fresh attempt fails with exit 29 too: only now the id stops.
	finishAttempt(t, s)
	if done, escalated := runState(s.states[0]); done || !escalated {
		t.Fatalf("second exit 29: done=%v escalated=%v, want escalated only:\n%s", done, escalated, f.events())
	}
	if args := f.args(); len(args) != 3 {
		t.Fatalf("the id was retried again after the fresh attempt failed: %d launches", len(args))
	}
	if !strings.Contains(f.events(), "ESCALATE stop a 1") || !strings.Contains(f.events(), "exit 29") {
		t.Fatalf("no ESCALATE stop for the second exit 29:\n%s", f.events())
	}
}

// The exit-29 fresh-instance retry keeps the target's --gm-instance-data: the
// new copy needs the lines, so the relaunch args carry the flag through (as
// does the same-instance re-place before it).
func TestAutoExit29RetryCarriesGMInstanceData(t *testing.T) {
	f := newAutoFixture(t)
	f.target("a srv 1 zg-a boss:14515 -- zul_gurub --sql /zg.sql --gm-instance-data hakkar:1 3;6 3;0 0")
	f.attempt("a-1", 5, 51, "server closed the connection")
	f.attempt("a-2", 29, 0, "place: 4 tries in the same instance, start step 51")
	f.attempt("a-3", 29, 0, "place: 4 tries in the same instance, start step 51")
	s := f.supervisor()
	tickUntil(t, s, "the first launch", func() bool { return len(f.args()) >= 1 })
	finishAttempt(t, s)
	tickUntil(t, s, "the reuse re-place", func() bool { return len(f.args()) >= 2 })
	finishAttempt(t, s)
	tickUntil(t, s, "the fresh-instance retry", func() bool { return len(f.args()) >= 3 })
	args := f.args()
	const want = "--gm-instance-data=hakkar:1 3;6 3;0 0"
	if !strings.Contains(args[1], want) {
		t.Fatalf("the reuse re-place dropped the flag: %s", args[1])
	}
	if !strings.Contains(args[2], want) {
		t.Fatalf("the fresh-instance retry dropped the flag: %s", args[2])
	}
	finishAttempt(t, s)
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

// Walk 39: a raid re-added after its goal boss was already dead kept running (t6 kept placing a Mar'li another raid
// had already killed; t7 restarted Thekal 9 s after the kill). A goal the checkpoint records as dead is DONE at start:
// nothing is launched and the slot stays free, instead of a re-place whose start the walk refuses (exit 2, ESCALATE).
func TestAutoStartSkipsAGoalWhoseBossIsDead(t *testing.T) {
	f := newAutoFixture(t)
	f.target("a srv 1 zg-a boss:14509 -- zul_gurub --sql /zg.sql")
	saved := autoSavedState{
		Format: autoStateFormat, Version: autoStateVersion, Updated: "2026-11-05T04:06:20Z",
		IDs: []autoSavedID{{ID: "a", BossDead: true}},
	}
	raw, err := json.Marshal(saved)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(f.walk, autoStateFile), raw, 0o600); err != nil {
		t.Fatal(err)
	}
	s := f.supervisor()
	tickUntil(t, s, "a to finish", func() bool {
		done, escalated := runState(s.states[0])
		return done || escalated
	})
	if done, escalated := runState(s.states[0]); !done || escalated {
		t.Fatalf("done %v escalated %v, want done and no escalate", done, escalated)
	}
	if launches := f.args(); len(launches) != 0 {
		t.Fatalf("a dead goal launched %d walk(s):\n%s", len(launches), strings.Join(launches, "\n"))
	}
	if st := s.states[0]; st.run != nil {
		t.Fatalf("a dead goal kept a run: %+v", st.run)
	}
	if !strings.Contains(f.events(), "a already dead: goal boss:14509 is dead") {
		t.Fatalf("no dead-boss event:\n%s", f.events())
	}
}

// Walk 39: the only per-dir stop was YIELD, which stops every raid. STOP-<raid id> stops just that raid cleanly
// (the run is stopped and its slot torn down) and never relaunches it; the other id keeps running.
func TestAutoStopFileStopsOnlyItsRaid(t *testing.T) {
	f := newAutoFixture(t)
	f.target("a srv 1 zg-a full -- zul_gurub --sql /zg.sql")
	f.target("b srv 2 zg-b full -- zul_gurub --sql /zg.sql")
	f.running("a-1", 30)
	f.running("b-1", 30)
	s := f.supervisor()
	tickUntil(t, s, "both raids launched", func() bool { return len(f.args()) >= 2 })
	if err := os.WriteFile(filepath.Join(f.walk, "STOP-a"), nil, 0o600); err != nil {
		t.Fatal(err)
	}
	tickUntil(t, s, "a stopped", func() bool {
		done, _ := runState(s.states[0])
		return done
	})
	if _, escalated := runState(s.states[0]); escalated {
		t.Fatalf("STOP-a escalated a:\n%s", f.events())
	}
	if done, _ := runState(s.states[1]); done {
		t.Fatalf("STOP-a stopped b too:\n%s", f.events())
	}
	if !strings.Contains(f.events(), "a stop: STOP-a") {
		t.Fatalf("no STOP-a event:\n%s", f.events())
	}
	before := len(f.args())
	waitForN(t, time.Second, "a's teardown", func() bool { return len(f.teardowns()) >= 1 })
	tickUntil(t, s, "b to keep running", func() bool {
		done, _ := runState(s.states[1])
		return !done
	})
	if after := len(f.args()); after != before {
		t.Fatalf("a was relaunched after its STOP file (%d -> %d):\n%s", before, after, strings.Join(f.args(), "\n"))
	}
}

// A stopped id is done, but its goals were not met: when it is the last id the supervisor does not claim "every
// target's goals are met", and it still exits 0 (a person asked for the stop; nothing needs one).
func TestAutoStopFileIsNotAMetGoal(t *testing.T) {
	f := newAutoFixture(t)
	f.target("a srv 1 zg-a full -- zul_gurub --sql /zg.sql")
	f.running("a-1", 30)
	s := f.supervisor()
	tickUntil(t, s, "a launched", func() bool { return len(f.args()) >= 1 })
	if err := os.WriteFile(filepath.Join(f.walk, "STOP-a"), nil, 0o600); err != nil {
		t.Fatal(err)
	}
	tickUntil(t, s, "a stopped", func() bool {
		done, _ := runState(s.states[0])
		return done
	})
	if code := s.finishAll(); code != exitOK {
		t.Fatalf("exit %d after a clean stop, want 0", code)
	}
	if strings.Contains(f.events(), "all done: every target's goals are met") {
		t.Fatalf("a stopped id claimed every goal was met:\n%s", f.events())
	}
	if !strings.Contains(f.events(), "1 stopped") {
		t.Fatalf("the finished line does not count the stop:\n%s", f.events())
	}
	if status, _ := os.ReadFile(filepath.Join(f.walk, autoStatusFile)); !strings.Contains(string(status), "stopped (STOP-a)") {
		t.Fatalf("status.txt does not name the stop:\n%s", status)
	}
}

// Review round 1: the checkpoint keeps the stop. A restarted supervisor of a STOP-completed id still reports it as
// stopped (not as a met goal), never relaunches it, and its finish line does not claim every goal was met.
func TestAutoStoppedStateSurvivesARestart(t *testing.T) {
	f := newAutoFixture(t)
	f.target("a srv 1 zg-a full -- zul_gurub --sql /zg.sql")
	f.running("a-1", 30)
	s := f.supervisor()
	tickUntil(t, s, "a launched", func() bool { return len(f.args()) >= 1 })
	if err := os.WriteFile(filepath.Join(f.walk, "STOP-a"), nil, 0o600); err != nil {
		t.Fatal(err)
	}
	tickUntil(t, s, "a stopped", func() bool {
		done, _ := runState(s.states[0])
		return done
	})
	s.saveState()
	// A fresh supervisor reads the checkpoint as a start does.
	s2 := f.supervisor()
	st := f.stateByID(s2, "a")
	if st == nil {
		t.Fatalf("the restart dropped a:\n%s", f.events())
	}
	st.mu.Lock()
	stopped, why := st.stopped, st.stopWhy
	st.mu.Unlock()
	if !stopped || why != "STOP-a" {
		t.Fatalf("the restart lost the stop (stopped=%v why=%q):\n%s", stopped, why, f.events())
	}
	if s2.allGoalsMet() {
		t.Fatalf("a stopped id counted as a met goal after the restart:\n%s", f.events())
	}
	s2.writeStatus()
	if status := f.statusFile(); !strings.Contains(status, "a goal=full stopped (STOP-a)") {
		t.Fatalf("status.txt does not report the restored stop:\n%s", status)
	}
	if n := len(f.args()); n != 1 {
		t.Fatalf("the restarted supervisor relaunched a (%d launches):\n%s", n, strings.Join(f.args(), "\n"))
	}
}

// Review round 1: a STOP file an id acted on is consumed (renamed STOP-<id>.done, one line in EVENTS.txt), so a
// re-added id of the same name starts instead of being stopped again by an order that already ran.
func TestAutoConsumedStopFileDoesNotStopAReAddedID(t *testing.T) {
	f := newAutoFixture(t)
	f.target("a srv 1 zg-a full -- zul_gurub --sql /zg.sql")
	f.target("z srv 2 zg-z full -- zul_gurub --sql /zg.sql")
	f.running("a-1", 30)
	f.running("a-2", 30)
	f.attempt("z-1", 2, 0, "bad --sql")
	launched := func(name string) bool {
		want := "--out " + filepath.Join(f.walk, name)
		for _, a := range f.args() {
			if strings.Contains(a, want) {
				return true
			}
		}
		return false
	}
	s := f.supervisor()
	tickUntil(t, s, "a launched", func() bool { return launched("a-1") })
	tickUntil(t, s, "z escalated", func() bool {
		st := f.stateByID(s, "z")
		if st == nil {
			return false
		}
		_, escalated := runState(st)
		return escalated
	})
	if err := os.WriteFile(filepath.Join(f.walk, "STOP-a"), nil, 0o600); err != nil {
		t.Fatal(err)
	}
	tickUntil(t, s, "a stopped", func() bool {
		st := f.stateByID(s, "a")
		if st == nil {
			return false
		}
		done, _ := runState(st)
		return done
	})
	if _, err := os.Stat(filepath.Join(f.walk, "STOP-a.done")); err != nil {
		t.Fatalf("STOP-a was not consumed: %v\n%s", err, f.events())
	}
	if n := strings.Count(f.events(), "STOP-a consumed"); n != 1 {
		t.Fatalf("%d consumed lines, want 1:\n%s", n, f.events())
	}
	// Remove a's line (the stopped state goes; z keeps the file valid) and add it again: the re-added id must run.
	path := filepath.Join(f.walk, autoTargetsFile)
	if err := os.WriteFile(path, []byte("z srv 2 zg-z full -- zul_gurub --sql /zg.sql\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	tickUntil(t, s, "the stopped a to be dropped", func() bool { return f.stateByID(s, "a") == nil })
	if err := os.WriteFile(path, []byte("a srv 1 zg-a full -- zul_gurub --sql /zg.sql\nz srv 2 zg-z full -- zul_gurub --sql /zg.sql\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	tickUntil(t, s, "the re-added a to launch", func() bool { return launched("a-2") })
	st := f.stateByID(s, "a")
	if st == nil {
		t.Fatalf("the re-added a is not supervised:\n%s", f.events())
	}
	if done, _ := runState(st); done {
		t.Fatalf("the re-added a was stopped before it ran:\n%s", f.events())
	}
}

// Walk 39: targets.txt was read once at start, so every added raid needed another autopilot dir (the ramp-* dirs,
// one per re-added raid). A line appended while the supervisor runs starts that raid without a restart, and a removed
// line stops its raid cleanly (its slot torn down), with no ESCALATE and no re-read of the goals of an unchanged id.
func TestAutoTargetsFileIsReread(t *testing.T) {
	f := newAutoFixture(t)
	f.target("a srv 1 zg-a full -- zul_gurub --sql /zg.sql")
	f.running("a-1", 30)
	f.running("b-1", 30)
	s := f.supervisor()
	tickUntil(t, s, "a launched", func() bool { return len(f.args()) >= 1 })
	f.target("b srv 2 zg-b full -- zul_gurub --sql /zg.sql")
	tickUntil(t, s, "b launched", func() bool { return len(f.args()) >= 2 })
	// Remove a's line: the id stops like STOP-a (its run stopped, its slot torn down), and b keeps running.
	path := filepath.Join(f.walk, autoTargetsFile)
	if err := os.WriteFile(path, []byte("b srv 2 zg-b full -- zul_gurub --sql /zg.sql\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	tickUntil(t, s, "a to stop", func() bool {
		st := f.stateByID(s, "a")
		if st == nil {
			return true
		}
		done, _ := runState(st)
		return done
	})
	if done, _ := runState(f.stateByID(s, "b")); done {
		t.Fatalf("removing a stopped b too:\n%s", f.events())
	}
	if strings.Contains(f.events(), "ESCALATE") {
		t.Fatalf("the targets re-read escalated an id:\n%s", f.events())
	}
	before := len(f.args())
	waitForN(t, time.Second, "a's teardown", func() bool { return len(f.teardowns()) >= 1 })
	for range 20 {
		s.tick(context.Background())
		time.Sleep(2 * time.Millisecond)
	}
	if after := len(f.args()); after != before {
		t.Fatalf("a was relaunched after its line was removed:\n%s", strings.Join(f.args(), "\n"))
	}
}

// Review round 1: a changed id was numbered from a state added after initSeq, so it launched <id>-1 again and read
// the old attempt's summary.json (a kill of the same boss) as its own: the changed goal looked met. The new run is
// numbered past the out dirs on disk.
func TestAutoChangedIDNeverRereadsAnOldOut(t *testing.T) {
	f := newAutoFixture(t)
	f.target("a srv 1 zg-a boss:14515 -- zul_gurub --sql /zg.sql")
	f.attempt("a-1", 0, 417, "boss 14515 down")
	f.attempt("a-2", 5, 0, "server closed the connection")
	s := f.supervisor()
	tickUntil(t, s, "a's first launch", func() bool { return len(f.args()) >= 1 })
	tickUntil(t, s, "a done", func() bool {
		done, _ := runState(s.states[0])
		return done
	})
	// The line changes (a new --from): the id stops, is dropped, and starts again in a new out dir.
	path := filepath.Join(f.walk, autoTargetsFile)
	if err := os.WriteFile(path, []byte("a srv 1 zg-a boss:14515 -- zul_gurub --sql /zg.sql --from step 100\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	tickUntil(t, s, "the changed a to launch", func() bool { return len(f.args()) >= 2 })
	args := f.args()
	if !strings.Contains(args[1], filepath.Join(f.walk, "a-2")) {
		t.Fatalf("the changed goal reused an old out dir: %s", args[1])
	}
	st := f.stateByID(s, "a")
	tickUntil(t, s, "the new attempt to end", func() bool {
		st.mu.Lock()
		defer st.mu.Unlock()
		return st.run == nil
	})
	if done, _ := runState(st); done {
		t.Fatalf("the changed goal read a-1's old summary and called itself met:\n%s", f.events())
	}
	if got := strings.Count(f.events(), "done: the last goal is met"); got != 1 {
		t.Fatalf("%d met-goal lines, want only the first goal's:\n%s", got, f.events())
	}
}

// An unparseable targets.txt keeps the targets the supervisor has (a half-written file is not a stop order) and logs
// one warning; the next valid write is applied.
func TestAutoInvalidTargetsKeepTheOldOnes(t *testing.T) {
	f := newAutoFixture(t)
	f.target("a srv 1 zg-a full -- zul_gurub --sql /zg.sql")
	f.running("a-1", 30)
	s := f.supervisor()
	tickUntil(t, s, "a launched", func() bool { return len(f.args()) >= 1 })
	if err := os.WriteFile(filepath.Join(f.walk, autoTargetsFile), []byte("not a target line\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	for range 20 {
		s.tick(context.Background())
		time.Sleep(2 * time.Millisecond)
	}
	if got := strings.Count(f.events(), "targets"); strings.Count(f.events(), "invalid targets file") != 1 {
		t.Fatalf("a invalid targets.txt warned %d times (want once):\n%s", got, f.events())
	}
	if done, _ := runState(f.stateByID(s, "a")); done {
		t.Fatalf("an invalid targets.txt stopped a:\n%s", f.events())
	}
}

// Walk 40: a finished id kept its slot, so a new line naming that slot made the whole file invalid and every edit was
// ignored. A done id with no run and no lease holds nothing: the new id starts on its slot.
func TestAutoNewIDTakesADoneIDsSlot(t *testing.T) {
	f := newAutoFixture(t)
	f.target("a srv 1 zg-a boss:14515 -- zul_gurub --sql /zg.sql")
	f.attempt("a-1", 0, 417, "boss 14515 down")
	f.running("b-1", 30)
	s := f.supervisor()
	tickUntil(t, s, "a done", func() bool {
		done, _ := runState(s.states[0])
		return done
	})
	f.target("b srv 1 zg-b full -- zul_gurub --sql /zg.sql")
	tickUntil(t, s, "b launched on a's slot", func() bool { return len(f.args()) >= 2 })
	if strings.Contains(f.events(), "invalid targets") {
		t.Fatalf("a done id's slot made the file invalid:\n%s", f.events())
	}
	if f.stateByID(s, "b") == nil {
		t.Fatalf("b is not supervised:\n%s", f.events())
	}
}

// A slot still held by a live run keeps the one-walk-per-slot rule: the new line is skipped with a warning naming
// it, the running id is untouched, and the line starts once the run is over.
func TestAutoNewIDOnARunningIDsSlotWaits(t *testing.T) {
	f := newAutoFixture(t)
	f.target("a srv 1 zg-a boss:14515 -- zul_gurub --sql /zg.sql")
	f.running("a-1", 30)
	f.running("b-1", 30)
	s := f.supervisor()
	tickUntil(t, s, "a launched", func() bool { return len(f.args()) >= 1 })
	f.target("b srv 1 zg-b full -- zul_gurub --sql /zg.sql")
	for range 20 {
		s.tick(context.Background())
		time.Sleep(2 * time.Millisecond)
	}
	if len(f.args()) != 1 || f.stateByID(s, "b") != nil {
		t.Fatalf("b started on a running id's slot:\n%s", f.events())
	}
	if got := strings.Count(f.events(), "invalid targets line skipped"); got != 1 || !strings.Contains(f.events(), "line 2") {
		t.Fatalf("%d warnings, want one naming line 2:\n%s", got, f.events())
	}
	if done, _ := runState(f.stateByID(s, "a")); done {
		t.Fatalf("the skipped line stopped a:\n%s", f.events())
	}
}

// Walk 40: one bad line must not block the other edits. The bad line is skipped with a warning naming it (once),
// the other new lines start, and the bad line's id keeps its old state.
func TestAutoOneBadLineDoesNotBlockTheOtherEdits(t *testing.T) {
	f := newAutoFixture(t)
	f.target("a srv 1 zg-a full -- zul_gurub --sql /zg.sql")
	f.running("a-1", 30)
	f.running("b-1", 30)
	f.running("c-1", 30)
	s := f.supervisor()
	tickUntil(t, s, "a launched", func() bool { return len(f.args()) >= 1 })
	// a's line is edited into a bad one (no --sql), b is new and valid, line 3 is garbage, c is new and valid.
	path := filepath.Join(f.walk, autoTargetsFile)
	text := "a srv 1 zg-a full -- zul_gurub\nb srv 2 zg-b full -- zul_gurub --sql /zg.sql\nnot a target line\nc srv 4 zg-c full -- zul_gurub --sql /zg.sql\n"
	if err := os.WriteFile(path, []byte(text), 0o600); err != nil {
		t.Fatal(err)
	}
	tickUntil(t, s, "b and c launched", func() bool { return len(f.args()) >= 3 })
	for range 10 {
		s.tick(context.Background())
		time.Sleep(2 * time.Millisecond)
	}
	if got := strings.Count(f.events(), "invalid targets line skipped"); got != 2 {
		t.Fatalf("%d warnings, want 2 (lines 1 and 3), once each:\n%s", got, f.events())
	}
	for _, want := range []string{"line 1", "line 3"} {
		if !strings.Contains(f.events(), want) {
			t.Fatalf("no warning naming %s:\n%s", want, f.events())
		}
	}
	if st := f.stateByID(s, "a"); st == nil {
		t.Fatalf("a lost its state to its bad line:\n%s", f.events())
	} else if done, esc := runState(st); done || esc {
		t.Fatalf("a was stopped by its bad line:\n%s", f.events())
	}
	if len(f.args()) != 3 {
		t.Fatalf("%d launches, want 3 (a, b, c):\n%s", len(f.args()), strings.Join(f.args(), "\n"))
	}
}

// Opus round 1 (HIGH): the live re-read accepts done a + new b on a's slot, so the restart that follows must too. The
// start marks the ids the checkpoint holds as done or stopped as inactive, like the re-read does.
func TestAutoRestartAcceptsADoneIDsSlotTaken(t *testing.T) {
	f := newAutoFixture(t)
	f.target("a srv 1 zg-a boss:14515 -- zul_gurub --sql /zg.sql")
	f.attempt("a-1", 0, 417, "boss 14515 down")
	s := f.supervisor()
	tickUntil(t, s, "a done", func() bool {
		done, _ := runState(s.states[0])
		return done
	})
	s.saveState()
	f.target("b srv 1 zg-b full -- zul_gurub --sql /zg.sql")
	s2 := f.supervisor() // fails the test when the start refuses the file
	if f.stateByID(s2, "b") == nil || f.stateByID(s2, "a") == nil {
		t.Fatalf("the restart lost a or b:\n%s", f.events())
	}
	if done, _ := runState(f.stateByID(s2, "a")); !done {
		t.Fatalf("the restart did not restore a as done:\n%s", f.events())
	}
}

// Opus round 1 (HIGH): the first line in file order must not win a slot over a live id. A new line inserted ABOVE the
// running id's line is the skipped one; the running id keeps its slot and never gets a second launch beside it.
func TestAutoNewLineAboveARunningIDDoesNotTakeItsSlot(t *testing.T) {
	f := newAutoFixture(t)
	f.target("a srv 1 zg-a full -- zul_gurub --sql /zg.sql")
	f.running("a-1", 30)
	f.running("b-1", 30)
	s := f.supervisor()
	tickUntil(t, s, "a launched", func() bool { return len(f.args()) >= 1 })
	path := filepath.Join(f.walk, autoTargetsFile)
	text := "b srv 1 zg-b full -- zul_gurub --sql /zg.sql\na srv 1 zg-a full -- zul_gurub --sql /zg.sql\n"
	if err := os.WriteFile(path, []byte(text), 0o600); err != nil {
		t.Fatal(err)
	}
	for range 20 {
		s.tick(context.Background())
		time.Sleep(2 * time.Millisecond)
	}
	if len(f.args()) != 1 || f.stateByID(s, "b") != nil {
		t.Fatalf("b took a running id's slot:\n%s", f.events())
	}
	if done, esc := runState(f.stateByID(s, "a")); done || esc {
		t.Fatalf("a was stopped or escalated by the insert:\n%s", f.events())
	}
	if !strings.Contains(f.events(), "id b: line 1") {
		t.Fatalf("the skipped line is not b's line 1:\n%s", f.events())
	}
}

// Opus round 1 (MEDIUM): a done id is inactive whoever holds its slot's lease now. b runs on a's slot (lease held by
// b), and an unrelated new line must not make a count as active again and warn about b.
func TestAutoDoneIDStaysInactiveWhileANewIDHoldsItsLease(t *testing.T) {
	f := newAutoFixture(t)
	f.target("a srv 1 zg-a boss:14515 -- zul_gurub --sql /zg.sql")
	f.attempt("a-1", 0, 417, "boss 14515 down")
	f.running("b-1", 30)
	f.running("c-1", 30)
	s := f.supervisor()
	tickUntil(t, s, "a done", func() bool {
		done, _ := runState(s.states[0])
		return done
	})
	f.target("b srv 1 zg-b full -- zul_gurub --sql /zg.sql")
	tickUntil(t, s, "b launched", func() bool { return len(f.args()) >= 2 })
	f.lease("zg-b", "held")
	f.target("c srv 2 zg-c full -- zul_gurub --sql /zg.sql")
	tickUntil(t, s, "c launched", func() bool { return len(f.args()) >= 3 })
	if strings.Contains(f.events(), "invalid targets") {
		t.Fatalf("a done id counted as active while b held its lease:\n%s", f.events())
	}
}

// Opus round 1 (MEDIUM): a skipped line is re-read every tick only while some id's state changed; an idle supervisor
// with a skipped line does not re-parse the file (and so does not take a lease check per tick).
func TestAutoSkippedLineIsNotRereadWhileNothingChanges(t *testing.T) {
	f := newAutoFixture(t)
	f.target("a srv 1 zg-a full -- zul_gurub --sql /zg.sql")
	f.running("a-1", 30)
	s := f.supervisor()
	tickUntil(t, s, "a launched", func() bool { return len(f.args()) >= 1 })
	f.target("b srv 1 zg-b full -- zul_gurub --sql /zg.sql")
	s.tick(context.Background())
	if s.targetsReads == 0 {
		t.Fatal("the edited file was not read")
	}
	before := s.targetsReads
	for range 20 {
		s.tick(context.Background())
	}
	if s.targetsReads != before {
		t.Fatalf("the file was re-read %d times while no id changed", s.targetsReads-before)
	}
}

// Opus round 1 (MEDIUM): the fresh-instance retry after exit 29 is once per GOAL, not once per streak: a goal whose
// fresh retry placed and ran, then hit exit 29 again on a later reuse placement, stops the id.
func TestAutoExit29RetryIsOncePerGoal(t *testing.T) {
	f := newAutoFixture(t)
	f.target("a srv 1 zg-a boss:14515 -- zul_gurub --sql /zg.sql")
	f.attempt("a-1", 5, 51, "server closed the connection")
	f.attempt("a-2", 29, 0, "place: 4 tries in the same instance, start step 51")
	f.attempt("a-3", 5, 60, "server closed the connection")
	f.attempt("a-4", 29, 0, "place: 4 tries in the same instance, start step 60")
	s := f.supervisor()
	for i := 1; i <= 3; i++ {
		tickUntil(t, s, fmt.Sprintf("launch %d", i), func() bool { return len(f.args()) >= i })
		finishAttempt(t, s)
	}
	tickUntil(t, s, "launch 4", func() bool { return len(f.args()) >= 4 })
	finishAttempt(t, s)
	if done, esc := runState(s.states[0]); done || !esc {
		t.Fatalf("second exit 29 of the goal: done=%v escalated=%v, want escalated:\n%s", done, esc, f.events())
	}
	if n := len(f.args()); n != 4 {
		t.Fatalf("%d launches, want 4 (one fresh retry per goal):\n%s", n, f.events())
	}
}

// Walk 39: the shared hire pool refused one raid while others hired ("No mercenary of that kind is available right
// now"; m5 and m1 in EVENTS.txt). The raid backs off 5 minutes and the refusal is not a failed attempt: refusals
// never grow the backoff toward 10 minutes, and the first five never escalate the id (the sixth does, its own test).
func TestAutoHirePoolWaitBacksOffWithoutCountingAFailure(t *testing.T) {
	f := newAutoFixture(t)
	f.target("a srv 1 zg-a boss:11380 -- zul_gurub --sql /zg.sql")
	reason := "hire 2 priest:healer: TBM:HIRE_RAID|partial|1|2|0|1|0|2|0|No mercenary of that kind is available right now. Try again"
	for n := 1; n <= 4; n++ {
		f.attempt(fmt.Sprintf("a-%d", n), 18, 0, reason)
	}
	s := f.supervisor()
	for n := 1; n <= 4; n++ {
		tickUntil(t, s, fmt.Sprintf("launch %d", n), func() bool { return len(f.args()) >= n })
		tickUntil(t, s, fmt.Sprintf("attempt %d to finish", n), func() bool {
			st := s.states[0]
			st.mu.Lock()
			defer st.mu.Unlock()
			return st.run == nil
		})
		st := s.states[0]
		st.mu.Lock()
		wait := time.Until(st.nextAt)
		escalated := st.escalated
		st.mu.Unlock()
		if escalated || strings.Contains(f.events(), "ESCALATE") {
			t.Fatalf("refusal %d escalated the id:\n%s", n, f.events())
		}
		if wait < 4*time.Minute || wait > 6*time.Minute {
			t.Fatalf("refusal %d backed off %s, want 5m", n, wait.Round(time.Second))
		}
		clearWait(st)
	}
	if got := strings.Count(f.events(), "hire-pool-wait: "); got != 4 {
		t.Fatalf("%d hire-pool-wait lines, want 4:\n%s", got, f.events())
	}
}

// Review round 1: the wait loop has an end. Six consecutive hire-pool refusals (~30 min of 5-minute waits) leave the id
// for a person instead of waiting forever.
func TestAutoHirePoolWaitEscalatesAfterSixWaits(t *testing.T) {
	f := newAutoFixture(t)
	f.target("a srv 1 zg-a boss:11380 -- zul_gurub --sql /zg.sql")
	reason := "hire 2 priest:healer: TBM:HIRE_RAID|partial|1|2|0|1|0|2|0|No mercenary of that kind is available right now. Try again"
	for n := 1; n <= 6; n++ {
		f.attempt(fmt.Sprintf("a-%d", n), 18, 0, reason)
	}
	s := f.supervisor()
	for n := 1; n <= 6; n++ {
		tickUntil(t, s, fmt.Sprintf("launch %d", n), func() bool { return len(f.args()) >= n })
		tickUntil(t, s, fmt.Sprintf("attempt %d to finish", n), func() bool {
			st := s.states[0]
			st.mu.Lock()
			defer st.mu.Unlock()
			return st.run == nil || st.escalated
		})
		clearWait(s.states[0])
	}
	if _, escalated := runState(s.states[0]); !escalated {
		t.Fatalf("six hire-pool waits did not escalate the id:\n%s", f.events())
	}
	if !strings.Contains(f.events(), "ESCALATE stop a 1") || !strings.Contains(f.events(), "hire pool: 6 waits") {
		t.Fatalf("the ESCALATE line does not name the sixth wait:\n%s", f.events())
	}
	if code := s.finishAll(); code != exitAutoEscalated {
		t.Fatalf("finishAll = %d, want %d", code, exitAutoEscalated)
	}
}

// Review round 1: a hire-pool wait is no attempt at all — it leaves noProgress and placeFails as they were (it used to
// wipe both) and moves bestStep only when the refused run had already walked past it.
func TestAutoHirePoolWaitKeepsTheFailureCounts(t *testing.T) {
	f := newAutoFixture(t)
	f.target("a srv 1 zg-a boss:11380 -- zul_gurub --sql /zg.sql")
	reason := "hire 2 priest:healer: TBM:HIRE_RAID|partial|1|2|0|1|0|2|0|No mercenary of that kind is available right now. Try again"
	f.attempt("a-1", 5, 0, "place: the corridor is not clear")
	f.attempt("a-2", 18, 0, reason)
	f.attempt("a-3", 5, 90, "server closed the connection")
	f.attempt("a-4", 18, 95, reason)
	s := f.supervisor()
	counts := func() (noProgress, bestStep, placeFails int) {
		st := s.states[0]
		st.mu.Lock()
		defer st.mu.Unlock()
		return st.goals[0].noProgress, st.goals[0].bestStep, st.placeFails
	}
	finish := func(n int, wantNoProgress, wantBest, wantPlaces int) {
		t.Helper()
		tickUntil(t, s, fmt.Sprintf("launch %d", n), func() bool { return len(f.args()) >= n })
		tickUntil(t, s, fmt.Sprintf("attempt %d to finish", n), func() bool {
			st := s.states[0]
			st.mu.Lock()
			defer st.mu.Unlock()
			return st.run == nil
		})
		clearWait(s.states[0])
		if np, best, pf := counts(); np != wantNoProgress || best != wantBest || pf != wantPlaces {
			t.Fatalf("after attempt %d: noProgress=%d bestStep=%d placeFails=%d, want %d/%d/%d:\n%s",
				n, np, best, pf, wantNoProgress, wantBest, wantPlaces, f.events())
		}
	}
	finish(1, 1, 0, 1)
	finish(2, 1, 0, 1)  // the wait leaves both counts alone
	finish(3, 0, 90, 0) // progress resets them
	finish(4, 0, 95, 0) // the refused run had progressed: bestStep moves, the counts stand
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
	// The checkpoint is written on the launch tick (Goal 1, step 0), but a-2's canned 51/761 progress line is
	// already on disk: the supervisor learns step 51 on a later tick and rewrites the checkpoint with LastStep 51.
	// A kill in that window restarts goal 2 from step 51 instead of its --from step 200. Wait until the checkpoint
	// itself records the advanced goal AND the observed progress, so the kill cannot land in that window.
	waitForN(t, 10*time.Second, "the checkpoint to record the advanced goal and its progress", func() bool {
		raw, err := os.ReadFile(filepath.Join(f.walk, autoStateFile))
		if err != nil {
			return false
		}
		var saved autoSavedState
		if json.Unmarshal(raw, &saved) != nil || len(saved.IDs) != 1 {
			return false
		}
		return saved.IDs[0].Goal == 1 && saved.IDs[0].LastStep == 51
	})
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
		if !strings.Contains(a, "--from step 200") || !strings.Contains(a, "--place") || !strings.Contains(a, "--reuse-instance") {
			t.Fatalf("the restarted supervisor did not resume goal 2 in its instance: %s", a)
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

// Review round 1: a removed targets line must not drop the state of an escalated id. Its ESCALATE line stays in
// status.txt, finishAll still exits for a person, and a YIELD over that slot writes no YIELD-DONE.
func TestAutoRemovedLineKeepsAnEscalatedID(t *testing.T) {
	f := newAutoFixture(t)
	f.target("a srv 1 zg-a full -- zul_gurub --sql /zg.sql")
	f.target("z srv 2 zg-z full -- zul_gurub --sql /zg.sql")
	f.attempt("a-1", 2, 0, "bad --sql")
	f.running("z-1", 30)
	s := f.supervisor()
	tickUntil(t, s, "a escalated", func() bool {
		st := f.stateByID(s, "a")
		if st == nil {
			return false
		}
		_, escalated := runState(st)
		return escalated
	})
	// a's line goes; z's keeps the file valid (an empty targets.txt is refused as unreadable).
	path := filepath.Join(f.walk, autoTargetsFile)
	if err := os.WriteFile(path, []byte("z srv 2 zg-z full -- zul_gurub --sql /zg.sql\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	for range 5 {
		s.tick(context.Background())
		time.Sleep(2 * time.Millisecond)
	}
	if st := f.stateByID(s, "a"); st == nil {
		t.Fatalf("the removed line dropped an escalated id:\n%s", f.events())
	}
	s.writeStatus()
	if status := f.statusFile(); !strings.Contains(status, "ESCALATE") {
		t.Fatalf("status.txt dropped the ESCALATE line:\n%s", status)
	}
	if code := s.finishAll(); code != exitAutoEscalated {
		t.Fatalf("finishAll = %d, want %d", code, exitAutoEscalated)
	}
	s.shutdown("YIELD")
	if _, err := os.Stat(filepath.Join(f.walk, autoYieldDone)); err == nil {
		t.Fatalf("a YIELD wrote YIELD-DONE over an escalated slot:\n%s", f.events())
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
	// The stub creates its slot dir after the launch event; a YIELD before that leaves no slot dir to fail on, and
	// the teardown is then (correctly) clean. Wait for the dir so the test exercises the failed teardown.
	waitForN(t, 10*time.Second, "the walk's slot dir", func() bool {
		_, err := os.Stat(filepath.Join(f.walk, "a-1", "slot"))
		return err == nil
	})
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
	// teardownFails is set before the event line is written (and the tick may return in between): poll the line.
	waitForN(t, 5*time.Second, "the teardown-failed event", func() bool {
		return strings.Contains(f.events(), "teardown-failed")
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

// M1: an adopted run has no Wait goroutine, so its exit comes from the pid: a YIELD after the walk died is clean and
// writes YIELD-DONE instead of waiting out the 60 s grace, SIGKILLing a dead pid and calling the teardown unclean.
func TestAutoAdoptedRunYieldIsClean(t *testing.T) {
	f := newAutoFixture(t)
	f.target("a srv 1 zg-a boss:14515 -- zul_gurub --sql /zg.sql")
	f.lease("zg-a", "held")
	out := filepath.Join(f.walk, "a-1")
	if err := os.MkdirAll(filepath.Join(out, "slot"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(out, "slot", "slot.json"), []byte(fmt.Sprintf(`{"server":"srv","slot":"1","holder":"zg-a","slots_path":%q}`, f.slotsPath)), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(out, "run.log"), []byte("19:46:05.696 TBM:RUN|zul_gurub|running|51/761|jeklik|boss|begin|fighting\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	live := exec.Command("sleep", "30")
	live.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	if err := live.Start(); err != nil {
		t.Fatal(err)
	}
	defer func() { _ = live.Process.Kill(); _ = live.Wait() }()
	if err := os.WriteFile(filepath.Join(out, "walk.pid"), []byte(strconv.Itoa(live.Process.Pid)+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	saved := autoSavedState{Format: autoStateFormat, Version: autoStateVersion, IDs: []autoSavedID{{
		ID: "a", Goal: 0, LastStep: 51, Reuse: true, Attempt: 1, ChildPID: live.Process.Pid, OutDir: out,
	}}}
	raw, _ := json.Marshal(saved)
	if err := os.WriteFile(filepath.Join(f.walk, autoStateFile), raw, 0o600); err != nil {
		t.Fatal(err)
	}
	s := f.supervisor()
	st := s.states[0]
	st.mu.Lock()
	adopted := st.run != nil && st.run.adopted
	st.mu.Unlock()
	if !adopted {
		t.Fatalf("the live walk was not adopted:\n%s", f.events())
	}
	if err := live.Process.Kill(); err != nil {
		t.Fatal(err)
	}
	_ = live.Wait()
	f.lease("", "")
	closed := make(chan struct{})
	go func() {
		s.shutdown("YIELD")
		close(closed)
	}()
	select {
	case <-closed:
	case <-time.After(10 * time.Second):
		t.Fatalf("the YIELD waited for the kill grace on an adopted walk that already died:\n%s", f.events())
	}
	if _, err := os.Stat(filepath.Join(f.walk, autoYieldDone)); err != nil {
		t.Fatalf("no YIELD-DONE after a clean adopted shutdown:\n%s", f.events())
	}
	if strings.Contains(f.events(), "SIGKILL") {
		t.Fatalf("a dead adopted walk was SIGKILLed:\n%s", f.events())
	}
}

// M2: a YIELD does not tear down the stale slot dirs of refused and done ids, and the synthetic last attempt from
// reconcile is torn down already.
func TestAutoYieldSkipsRefusedAndDoneIDs(t *testing.T) {
	f := newAutoFixture(t)
	f.target("a srv 1 zg-a boss:14515 -- zul_gurub --sql /zg.sql")
	f.target("b srv 2 zg-b boss:14515 -- zul_gurub --sql /zg.sql")
	f.lease("someone-else", "held")
	for _, id := range []string{"a-1", "b-1"} {
		out := filepath.Join(f.walk, id)
		if err := os.MkdirAll(filepath.Join(out, "slot"), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(out, "slot", "slot.json"), []byte(fmt.Sprintf(`{"server":"srv","slot":"1","holder":"someone-else","slots_path":%q}`, f.slotsPath)), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	saved := autoSavedState{Format: autoStateFormat, Version: autoStateVersion, IDs: []autoSavedID{
		{ID: "a", Goal: 0, OutDir: filepath.Join(f.walk, "a-1")},
		{ID: "b", Goal: 0, Done: true, OutDir: filepath.Join(f.walk, "b-1")},
	}}
	raw, _ := json.Marshal(saved)
	if err := os.WriteFile(filepath.Join(f.walk, autoStateFile), raw, 0o600); err != nil {
		t.Fatal(err)
	}
	s := f.supervisor()
	for _, st := range s.states {
		st.mu.Lock()
		tornDown := st.last != nil && st.last.tornDown
		st.mu.Unlock()
		if !tornDown {
			t.Fatalf("the synthetic last attempt of %s is not torn down", st.id)
		}
	}
	s.states[0].mu.Lock()
	refused := s.states[0].refused
	s.states[0].mu.Unlock()
	if !refused {
		t.Fatalf("the foreign slot did not refuse a:\n%s", f.events())
	}
	s.shutdown("YIELD")
	if got := len(f.teardowns()); got != 0 {
		t.Fatalf("a YIELD tore down the stale slots of refused/done ids (%d teardown lines)", got)
	}
	if _, err := os.Stat(filepath.Join(f.walk, autoYieldDone)); err != nil {
		t.Fatalf("no YIELD-DONE although nothing needed a teardown:\n%s", f.events())
	}
}

// The state file is complete and atomic after saveState, and no temp file is left behind (the fsync path).
func TestAutoSaveStateLeavesNoTempAndReadsBack(t *testing.T) {
	f := newAutoFixture(t)
	f.target("a srv 1 zg-a boss:14515 -- zul_gurub --sql /zg.sql")
	s := f.supervisor()
	s.saveState()
	if _, err := os.Stat(filepath.Join(f.walk, autoStateFile) + ".tmp"); err == nil {
		t.Fatal("saveState left its temp file behind")
	}
	saved, err := s.loadState()
	if err != nil {
		t.Fatalf("the checkpoint it wrote does not read back: %v", err)
	}
	if len(saved.IDs) != 1 || saved.IDs[0].ID != "a" {
		t.Fatalf("state = %+v", saved.IDs)
	}
}

// autoSavedHireWaits reads the checkpoint's hire_waits through a shape of its own: it compiles without the field the
// fix adds, so this test fails on the behavior (an empty checkpoint value), not on a build.
func autoSavedHireWaits(t *testing.T, walk string) int {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join(walk, autoStateFile))
	if err != nil {
		t.Fatal(err)
	}
	var saved struct {
		IDs []struct {
			HireWaits int `json:"hire_waits"`
		} `json:"ids"`
	}
	if err := json.Unmarshal(raw, &saved); err != nil {
		t.Fatal(err)
	}
	if len(saved.IDs) != 1 {
		t.Fatalf("the checkpoint holds %d id(s), want 1", len(saved.IDs))
	}
	return saved.IDs[0].HireWaits
}

// Review round 2: the checkpoint's goal and step belong to the goal lines it was written for. A person edits the
// lines under a crashed supervisor; the old done/boss-dead state must not mark the new lines done, it must not be
// restored: the new goals run.
func TestAutoEditedGoalsRunDespiteADoneCheckpoint(t *testing.T) {
	f := newAutoFixture(t)
	f.target("a srv 1 zg-a boss:14515 -- zul_gurub --sql /zg.sql")
	f.target("a srv 1 zg-a boss:14507 -- zul_gurub --sql /zg.sql")
	s := f.supervisor()
	st := s.states[0]
	st.mu.Lock()
	st.current = 1
	st.goals[1].dead = true
	st.mu.Unlock()
	s.saveState()
	// The person rewrites both lines into two other goals before the restart: the saved goal 1 (boss dead) is about
	// lines targets.txt no longer holds, and its index 1 still exists in the new file.
	path := filepath.Join(f.walk, autoTargetsFile)
	if err := os.WriteFile(path, []byte("a srv 1 zg-a boss:14509 -- zul_gurub --sql /zg.sql\na srv 1 zg-a boss:14506 -- zul_gurub --sql /zg.sql\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	f.attempt("a-1", 2, 0, "bad --sql")
	s2 := f.supervisor()
	tickUntil(t, s2, "the edited goal to run", func() bool { return len(f.args()) >= 1 })
	if args := f.args(); len(args) != 1 || !strings.Contains(args[0], filepath.Join(f.walk, "a-1")) {
		t.Fatalf("the edited goal did not run fresh:\n%s", strings.Join(args, "\n"))
	}
	if strings.Contains(f.events(), "already dead") {
		t.Fatalf("the checkpoint's boss-dead state was applied to the edited goals:\n%s", f.events())
	}
	if !strings.Contains(f.events(), "the goal lines changed since the checkpoint; its progress is not restored") {
		t.Fatalf("the start did not say the checkpoint was stale:\n%s", f.events())
	}
}

// Review round 2: the other half of the fingerprint rule: unchanged lines still restore their done/boss-dead
// progress across a restart, and the checkpoint carries the lines' fingerprint.
func TestAutoUnchangedGoalsRestoreTheDoneCheckpoint(t *testing.T) {
	f := newAutoFixture(t)
	f.target("a srv 1 zg-a boss:14515 -- zul_gurub --sql /zg.sql")
	f.target("a srv 1 zg-a boss:14507 -- zul_gurub --sql /zg.sql")
	s := f.supervisor()
	st := s.states[0]
	st.mu.Lock()
	st.current = 1
	st.goals[1].dead = true
	st.mu.Unlock()
	s.saveState()
	var saved struct {
		IDs []struct {
			Fingerprint string `json:"fingerprint"`
		} `json:"ids"`
	}
	raw, err := os.ReadFile(filepath.Join(f.walk, autoStateFile))
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(raw, &saved); err != nil {
		t.Fatal(err)
	}
	if len(saved.IDs) != 1 || saved.IDs[0].Fingerprint == "" {
		t.Fatalf("the checkpoint does not fingerprint the id's goal lines: %s", raw)
	}
	if len(f.args()) != 0 {
		t.Fatalf("the first supervisor launched a walk:\n%s", strings.Join(f.args(), "\n"))
	}
	s2 := f.supervisor()
	tickUntil(t, s2, "the restored dead goal to be done", func() bool {
		done, _ := runState(f.stateByID(s2, "a"))
		return done
	})
	if len(f.args()) != 0 {
		t.Fatalf("an unchanged line's boss-dead goal was walked again:\n%s", strings.Join(f.args(), "\n"))
	}
	if !strings.Contains(f.events(), "already dead: goal boss:14507 is dead; the last goal is done") {
		t.Fatalf("the restart did not restore the boss-dead goal:\n%s", f.events())
	}
}

// Review round 2: a supervisor stopped between a STOP file's request and its finish (killed, or its teardown still
// running) must finish the stop after a restart: the checkpoint's stopping state keeps the id stopped, never relaunched,
// and the order file is consumed only then.
func TestAutoRestoredStopFinishesTheStop(t *testing.T) {
	f := newAutoFixture(t)
	f.target("a srv 1 zg-a full -- zul_gurub --sql /zg.sql")
	// The crashed supervisor requested the stop but died before its finish: the slot lease is still the stopped
	// walk's (the teardown never ran) and nothing is launching on it.
	f.lease("zg-a", "held")
	out := filepath.Join(f.walk, "a-1")
	if err := os.MkdirAll(filepath.Join(out, "slot"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(out, "slot", "slot.json"), []byte(fmt.Sprintf(`{"server":"srv","slot":"1","holder":"zg-a","slots_path":%q}`, f.slotsPath)), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(out, "run.log"), []byte("19:46:05.696 TBM:RUN|zul_gurub|running|51/761|jeklik|boss|begin|fighting\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	// The checkpoint of a supervisor that was killed after it read STOP-a and before its finish: stopping, no run.
	raw := fmt.Sprintf(`{"format":%q,"version":1,"ids":[{"id":"a","goal":0,"last_step":51,"attempt":1,"out_dir":%q,"stopping":true,"stop_why":"STOP-a"}]}`,
		autoStateFormat, out)
	if err := os.WriteFile(filepath.Join(f.walk, autoStateFile), []byte(raw), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(f.walk, "STOP-a"), nil, 0o600); err != nil {
		t.Fatal(err)
	}
	s := f.supervisor()
	tickUntil(t, s, "the restored stop to complete", func() bool {
		done, _ := runState(s.states[0])
		return done
	})
	st := s.states[0]
	st.mu.Lock()
	stopped := st.stopped
	st.mu.Unlock()
	if !stopped {
		t.Fatalf("the restored stop did not mark the id stopped:\n%s", f.events())
	}
	if _, err := os.Stat(filepath.Join(f.walk, "STOP-a.done")); err != nil {
		t.Fatalf("the completed stop did not consume STOP-a: %v\n%s", err, f.events())
	}
	if _, err := os.Stat(filepath.Join(f.walk, "STOP-a")); err == nil {
		t.Fatalf("STOP-a stayed beside its .done copy:\n%s", f.events())
	}
	if launches := f.args(); len(launches) != 0 {
		t.Fatalf("the restart relaunched a stopped id:\n%s", strings.Join(launches, "\n"))
	}
	if strings.Contains(f.events(), "ESCALATE") {
		t.Fatalf("a stop in progress was refused:\n%s", f.events())
	}
	if status := f.statusFile(); !strings.Contains(status, "a goal=full stopped (STOP-a)") {
		t.Fatalf("status.txt does not report the restored stop:\n%s", status)
	}
}

// Review round 2: a STOP file is consumed when the stop completes, not when it is read. For an escalated id a person
// owns, the order is refused: the file stays and one line says so (a stop the id cannot act on needs a person).
func TestAutoStopFileOnAnEscalatedIDIsKept(t *testing.T) {
	f := newAutoFixture(t)
	f.target("a srv 1 zg-a full -- zul_gurub --sql /zg.sql")
	f.attempt("a-1", 2, 0, "bad --sql")
	s := f.supervisor()
	tickUntil(t, s, "a escalated", func() bool {
		_, escalated := runState(s.states[0])
		return escalated
	})
	if err := os.WriteFile(filepath.Join(f.walk, "STOP-a"), nil, 0o600); err != nil {
		t.Fatal(err)
	}
	for range 5 {
		s.tick(context.Background())
		time.Sleep(2 * time.Millisecond)
	}
	if _, err := os.Stat(filepath.Join(f.walk, "STOP-a")); err != nil {
		t.Fatalf("the refused STOP-a was consumed: %v\n%s", err, f.events())
	}
	if _, err := os.Stat(filepath.Join(f.walk, "STOP-a.done")); err == nil {
		t.Fatalf("the refused STOP-a was renamed to .done:\n%s", f.events())
	}
	if got := strings.Count(f.events(), "STOP-a not applied: escalated, needs a person"); got != 1 {
		t.Fatalf("%d refusal lines for STOP-a, want 1:\n%s", got, f.events())
	}
	if strings.Contains(f.events(), "STOP-a consumed") {
		t.Fatalf("a refused STOP-a was logged as consumed:\n%s", f.events())
	}
}

// Review round 2: an edited line could not replace an escalated id (it stayed escalated forever and the new goals
// never ran). With no live run and a free slot, the new lines replace the old state and start in a fresh out dir.
func TestAutoChangedLineReplacesAnEscalatedID(t *testing.T) {
	f := newAutoFixture(t)
	f.target("a srv 1 zg-a full -- zul_gurub --sql /zg.sql")
	f.target("z srv 2 zg-z full -- zul_gurub --sql /zg.sql")
	f.attempt("a-1", 2, 0, "bad --sql")
	f.running("z-1", 30)
	launched := func(name string) bool {
		want := "--out " + filepath.Join(f.walk, name)
		for _, a := range f.args() {
			if strings.Contains(a, want) {
				return true
			}
		}
		return false
	}
	s := f.supervisor()
	tickUntil(t, s, "a escalated", func() bool {
		_, escalated := runState(f.stateByID(s, "a"))
		return escalated
	})
	// The person edits a's line (a new --from): the new lines must replace the escalated state, not wait forever.
	path := filepath.Join(f.walk, autoTargetsFile)
	if err := os.WriteFile(path, []byte("a srv 1 zg-a full -- zul_gurub --sql /zg.sql --from step 100\nz srv 2 zg-z full -- zul_gurub --sql /zg.sql\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	tickUntil(t, s, "the new lines of a to launch", func() bool { return launched("a-2") })
	if !strings.Contains(f.events(), "the new goal lines replace the escalated state") {
		t.Fatalf("no replacement line:\n%s", f.events())
	}
	s.writeStatus()
	if status := f.statusFile(); strings.Contains(status, "ESCALATE") {
		t.Fatalf("status.txt kept the replaced id's ESCALATE line:\n%s", status)
	}
}

// Review round 2: while the replaced id's slot lease is still held, the new lines wait; the wait is logged once, and
// the lines start when the lease is released (not before).
func TestAutoChangedLineWaitsForTheSlotLease(t *testing.T) {
	f := newAutoFixture(t)
	f.target("a srv 1 zg-a full -- zul_gurub --sql /zg.sql")
	f.target("z srv 2 zg-z full -- zul_gurub --sql /zg.sql")
	f.attempt("a-1", 2, 0, "bad --sql")
	f.running("z-1", 30)
	launched := func(name string) bool {
		want := "--out " + filepath.Join(f.walk, name)
		for _, a := range f.args() {
			if strings.Contains(a, want) {
				return true
			}
		}
		return false
	}
	s := f.supervisor()
	tickUntil(t, s, "a escalated", func() bool {
		_, escalated := runState(f.stateByID(s, "a"))
		return escalated
	})
	f.lease("zg-a", "held")
	path := filepath.Join(f.walk, autoTargetsFile)
	if err := os.WriteFile(path, []byte("a srv 1 zg-a full -- zul_gurub --sql /zg.sql --from step 100\nz srv 2 zg-z full -- zul_gurub --sql /zg.sql\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	for range 5 {
		s.tick(context.Background())
		time.Sleep(2 * time.Millisecond)
	}
	if launched("a-2") {
		t.Fatalf("the new lines launched on a held lease:\n%s", strings.Join(f.args(), "\n"))
	}
	if got := strings.Count(f.events(), "id a: new lines wait for the slot lease"); got != 1 {
		t.Fatalf("%d wait lines, want 1:\n%s", got, f.events())
	}
	f.lease("", "")
	tickUntil(t, s, "the new lines to launch after the release", func() bool { return launched("a-2") })
}

// Review round 2: the hire-pool waits are part of the checkpoint: the sixth refusal still leaves the id for a person
// when the first five happened before a restart.
func TestAutoHireWaitsSurviveARestart(t *testing.T) {
	f := newAutoFixture(t)
	f.target("a srv 1 zg-a boss:11380 -- zul_gurub --sql /zg.sql")
	reason := "hire 2 priest:healer: TBM:HIRE_RAID|partial|1|2|0|1|0|2|0|No mercenary of that kind is available right now. Try again"
	s := f.supervisor()
	st := s.states[0]
	st.mu.Lock()
	st.goals[0].hireWaits = raidwalk.AutoHirePoolWaitMax - 1
	st.mu.Unlock()
	s.saveState()
	if got := autoSavedHireWaits(t, f.walk); got != raidwalk.AutoHirePoolWaitMax-1 {
		t.Fatalf("the checkpoint saved hire_waits=%d, want %d", got, raidwalk.AutoHirePoolWaitMax-1)
	}
	f.attempt("a-1", 18, 0, reason)
	s2 := f.supervisor()
	tickUntil(t, s2, "the refusal after the restart to escalate", func() bool {
		_, escalated := runState(s2.states[0])
		return escalated
	})
	if !strings.Contains(f.events(), "hire pool: 6 waits") {
		t.Fatalf("the restored wait count did not reach the sixth wait:\n%s", f.events())
	}
}

// rewriteTargets replaces targets.txt whole.
func (f *autoFixture) rewriteTargets(text string) {
	f.t.Helper()
	if err := os.WriteFile(filepath.Join(f.walk, autoTargetsFile), []byte(text), 0o600); err != nil {
		f.t.Fatal(err)
	}
}

// checkpoint writes autopilot-state.json with these ids.
func (f *autoFixture) checkpoint(ids ...autoSavedID) {
	f.t.Helper()
	raw, err := json.Marshal(autoSavedState{Format: autoStateFormat, Version: autoStateVersion, IDs: ids})
	if err != nil {
		f.t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(f.walk, autoStateFile), raw, 0o600); err != nil {
		f.t.Fatal(err)
	}
}

// tickN ticks the supervisor n times.
func tickN(s *autoSupervisor, n int) {
	for range n {
		s.tick(context.Background())
		time.Sleep(2 * time.Millisecond)
	}
}

// Codex round 2 (HIGH): a running a gains one malformed line, and a new b is added on a's slot. a's bad line keeps
// its live state, and that state keeps its slot: b is skipped, never launched beside a (only c, on its own slot, is).
func TestAutoBadLineIDKeepsItsSlotFromANewID(t *testing.T) {
	f := newAutoFixture(t)
	f.target("a srv 1 zg-a full -- zul_gurub --sql /zg.sql")
	f.running("a-1", 30)
	f.running("b-1", 30)
	f.running("c-1", 30)
	s := f.supervisor()
	tickUntil(t, s, "a launched", func() bool { return len(f.args()) >= 1 })
	f.rewriteTargets("a srv 1 zg-a full -- zul_gurub --sql /zg.sql\n" +
		"a srv 1 zg-a leg:2 -- zul_gurub\n" + // no --sql
		"b srv 1 zg-b full -- zul_gurub --sql /zg.sql\n" +
		"c srv 2 zg-c full -- zul_gurub --sql /zg.sql\n") // a valid line, so the file applies
	tickUntil(t, s, "c launched", func() bool { return len(f.args()) >= 2 })
	tickN(s, 20)
	if len(f.args()) != 2 || f.stateByID(s, "b") != nil {
		t.Fatalf("b launched on the slot of a's live run (%d launches):\n%s", len(f.args()), f.events())
	}
	if done, esc := runState(f.stateByID(s, "a")); done || esc {
		t.Fatalf("a was stopped or escalated:\n%s", f.events())
	}
	if !strings.Contains(f.events(), "already a's") {
		t.Fatalf("b's skip does not name a as the slot's owner:\n%s", f.events())
	}
}

// Opus round 2 (HIGH-1): a is done, b runs on a's slot, and a's goal line is edited. The edited done id is a new id
// for the slot check: its line is skipped as b's slot, and it is never queued to launch beside b.
func TestAutoEditedDoneIDDoesNotTakeARunningIDsSlot(t *testing.T) {
	f := newAutoFixture(t)
	f.target("a srv 1 zg-a boss:14515 -- zul_gurub --sql /zg.sql")
	f.attempt("a-1", 0, 417, "boss 14515 down")
	f.running("b-1", 30)
	f.running("a-2", 30)
	s := f.supervisor()
	tickUntil(t, s, "a done", func() bool {
		done, _ := runState(s.states[0])
		return done
	})
	f.target("b srv 1 zg-b full -- zul_gurub --sql /zg.sql")
	tickUntil(t, s, "b launched", func() bool { return len(f.args()) >= 2 })
	f.rewriteTargets("a srv 1 zg-a boss:14516 -- zul_gurub --sql /zg.sql\nb srv 1 zg-b full -- zul_gurub --sql /zg.sql\n")
	tickN(s, 20)
	if len(f.args()) != 2 {
		t.Fatalf("%d launches, want 2 (a's edited line launched beside b):\n%s", len(f.args()), strings.Join(f.args(), "\n"))
	}
	if !strings.Contains(f.events(), "id a: line 1") || !strings.Contains(f.events(), "already b's") {
		t.Fatalf("a's edited line is not skipped as b's slot:\n%s", f.events())
	}
	if done, esc := runState(f.stateByID(s, "b")); done || esc || strings.Contains(f.events(), "exit 15") {
		t.Fatalf("b was stopped:\n%s", f.events())
	}
}

// Opus round 2 (MEDIUM-1): an edited active id moving onto a slot another live id runs on is skipped, whatever the
// file order; both runs go on and nothing is launched.
func TestAutoEditedActiveIDMovingOntoAnOccupiedSlotIsSkipped(t *testing.T) {
	f := newAutoFixture(t)
	f.target("a srv 1 zg-a full -- zul_gurub --sql /zg.sql")
	f.target("c srv 2 zg-c full -- zul_gurub --sql /zg.sql")
	f.running("a-1", 30)
	f.running("c-1", 30)
	f.running("a-2", 30)
	s := f.supervisor()
	tickUntil(t, s, "a and c launched", func() bool { return len(f.args()) >= 2 })
	f.rewriteTargets("a srv 2 zg-a full -- zul_gurub --sql /zg.sql\nc srv 2 zg-c full -- zul_gurub --sql /zg.sql\n")
	tickN(s, 20)
	if len(f.args()) != 2 {
		t.Fatalf("%d launches, want 2 (a moved onto c's running slot):\n%s", len(f.args()), strings.Join(f.args(), "\n"))
	}
	for _, id := range []string{"a", "c"} {
		st := f.stateByID(s, id)
		st.mu.Lock()
		stopping := st.stopping
		st.mu.Unlock()
		if done, esc := runState(st); done || esc || stopping {
			t.Fatalf("%s was stopped by the move:\n%s", id, f.events())
		}
	}
	if !strings.Contains(f.events(), "id a: line 1") || !strings.Contains(f.events(), "already c's") {
		t.Fatalf("a's move is not skipped as c's slot:\n%s", f.events())
	}
}

// Opus round 2 (LOW-2): a checkpointed id whose last goal's boss is dead (not yet marked done) holds no slot at a
// start: a new id on its slot is accepted.
func TestAutoStartBossDeadLastGoalIsInactive(t *testing.T) {
	f := newAutoFixture(t)
	f.target("a srv 1 zg-a boss:14515 -- zul_gurub --sql /zg.sql")
	f.target("b srv 1 zg-b full -- zul_gurub --sql /zg.sql")
	f.checkpoint(autoSavedID{ID: "a", Goal: 0, BossDead: true})
	targets, err := readAutoTargets(f.walk)
	if err != nil || len(targets) != 2 {
		t.Fatalf("targets %v err %v, want a and b accepted", targets, err)
	}
}

// A restart where a done id's lines changed and an active id holds its slot refuses: the changed id is a new id.
func TestAutoStartChangedDoneIDOnATakenSlotRefuses(t *testing.T) {
	f := newAutoFixture(t)
	f.target("a srv 1 zg-a boss:14516 -- zul_gurub --sql /zg.sql")
	f.target("b srv 1 zg-b full -- zul_gurub --sql /zg.sql")
	f.checkpoint(autoSavedID{ID: "a", Done: true, Fingerprint: "old-lines"}, autoSavedID{ID: "b", Attempt: 1})
	if _, err := readAutoTargets(f.walk); err == nil || !strings.Contains(err.Error(), "line 1") || !strings.Contains(err.Error(), "already b's") {
		t.Fatalf("err %v, want a refusal naming a's line 1 and b", err)
	}
}

// A restart where an active checkpointed id sits below a new line on its slot: the checkpointed id wins the slot, the
// new line is the one refused.
func TestAutoStartActiveIDBelowANewLineWinsItsSlot(t *testing.T) {
	f := newAutoFixture(t)
	f.target("n srv 1 zg-n full -- zul_gurub --sql /zg.sql")
	f.target("b srv 1 zg-b full -- zul_gurub --sql /zg.sql")
	f.checkpoint(autoSavedID{ID: "b", Attempt: 1})
	if _, err := readAutoTargets(f.walk); err == nil || !strings.Contains(err.Error(), "line 1") || !strings.Contains(err.Error(), "already b's") {
		t.Fatalf("err %v, want a refusal naming n's line 1 and b", err)
	}
}
