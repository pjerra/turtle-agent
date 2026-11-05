// raid-autopilot: keep the raids of a walk dir running without a worker watching them. The targets file names one
// goal per line; the autopilot launches raid-walk for each, watches the step and the bosses' HP, and on ANY end, a used
// -up restart budget or a stall tears the slot down and re-places: the same instance first (--place --from
// --reuse-instance), a fresh one only when the old bind is gone (a failed placement). A boss kill advances to the id's
// next goal. It never uses slot 3, never sends `.bot run stop` and never passes --instance-cap-unsafe or --inject; it
// runs detached (setsid) and stops cleanly on a YIELD file (tear down, release leases, YIELD-DONE).
//
// A teardown that does not verify (the walk gone and the lease released) is not completion: the run stays put and is
// retried, and the third failed tick escalates the slot in EVENTS.txt/status.txt and leaves it alone (no relaunch, no
// YIELD-DONE). One supervisor per walk dir, by an flock on autopilot.lock.
//
// Every transition is written to autopilot-state.json (tmp+rename), and a start reconciles it with the attempt
// summaries and the slots' leases: a goal met before a crash is not walked again, and a walk that survived a crashed
// supervisor is adopted, not relaunched beside. A slot whose lease or live walk is not ours refuses its id with an
// ESCALATE line instead of launching. The stops and teardowns of the ids run concurrently, so one slow slot never
// delays the others and a YIELD/signal reaches every walk within seconds. Walk 37 is the case set (EVENTS.txt,
// summary.md).
package main

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/pjerra/turtle-agent/internal/dungeon"
	"github.com/pjerra/turtle-agent/internal/planexport"
	"github.com/pjerra/turtle-agent/internal/raidwalk"
	"github.com/pjerra/turtle-agent/internal/slots"
)

const raidAutopilotUsage = `usage: turtle-agent raid-autopilot [--foreground] [--binary PATH] [--poll D] [--stall D]
    [--settle D] [--status-every D] [--instance-cap-per-account N] [--dry-run] <walk-dir>`

// autoChildEnv marks the re-exec'd detached process.
const autoChildEnv = "TURTLE_RAID_AUTOPILOT_CHILD"

// exitAutoEscalated is the supervisor's exit when at least one id was escalated for a person: its goals were not met.
const exitAutoEscalated = 31

// The autopilot's files in the walk dir.
const (
	autoTargetsFile = "targets.txt"
	autoEventsFile  = "EVENTS.txt"
	autoStatusFile  = "status.txt"
	autoYieldFile   = "YIELD"
	autoYieldDone   = "YIELD-DONE"
	autoPIDFile     = "autopilot.pid"
	autoLockFile    = "autopilot.lock"
	autoLogFile     = "autopilot.log"
	// autoStateFile is the durable checkpoint (tmp+rename on every transition): per id its current goal, the goals
	// met, the last reached step, the reuse decision, the attempt number and the child's pid/out dir.
	autoStateFile    = "autopilot-state.json"
	autoStateFormat  = "tb-raid-autopilot-state"
	autoStateVersion = 1
)

const (
	autoDefaultPoll        = 10 * time.Second
	autoDefaultStall       = 15 * time.Minute
	autoDefaultSettle      = 20 * time.Second
	autoDefaultStatusEvery = time.Minute
	autoKillGrace          = 60 * time.Second
	autoKillWait           = 10 * time.Second
	autoTeardownTimeout    = 3 * time.Minute
	// autoSummaryGrace is how long an exited run may take to write summary.json before the autopilot acts without it.
	autoSummaryGrace = 10 * time.Second
	// autoTeardownTries is how many ticks a failed teardown is retried before the slot is escalated and left alone.
	autoTeardownTries = 3
	// autoYieldPoll is how often a YIELD file is looked for: a YIELD is handled within 5 s even with a long --poll.
	autoYieldPoll = time.Second
	// autoLaunchTries is how many consecutive launches may fail before the id is escalated and left for a person.
	autoLaunchTries = 3
	// autoDetachWait is how long the detached launcher waits for the child to hold the lock (or report its refusal).
	autoDetachWait = 10 * time.Second
	// autoLeaseTimeout bounds the `lease list` read the startup reconciliation and the teardown verification make.
	autoLeaseTimeout = 15 * time.Second
)

type autoOptions struct {
	walkDir     string
	binary      string
	foreground  bool
	poll        time.Duration
	stall       time.Duration
	settle      time.Duration
	statusEvery time.Duration
	// instanceCapPerAccount is the core's fresh instances per account and hour, forwarded to raid-walk only when the
	// flag was given (otherwise raid-walk resolves the per-server value of instance-cap-per-account.json and then the
	// stock 5).
	instanceCapPerAccount int
	instanceCapSet        bool
	dryRun                bool
}

// autoGoalState is one of an id's goal lines.
type autoGoalState struct {
	target  raidwalk.AutoTarget
	attempt int
	// bestStep is the furthest step this goal reached in any attempt: a restart starts there.
	bestStep int
	// noProgress counts the consecutive attempts of this goal that reached no further step: the backoff index.
	noProgress int
	// leaseFails counts the consecutive attempts that ended in exit 15 (the slot is held by someone else).
	leaseFails int
}

// autoRun is one live or finished raid-walk attempt. Its fields are guarded by its state's mutex.
type autoRun struct {
	seq     int
	out     string
	slot    string
	reuse   bool
	goal    raidwalk.AutoGoal
	cmd     *exec.Cmd
	pidN    int
	adopted bool
	started time.Time
	exited  bool
	// exitedAt is when the autopilot saw the exit (the summary may still be being written).
	exitedAt time.Time
	exitCode int
	// progress: the last step, the boss-HP signature, and when either changed.
	lastStep     int
	lastProgress time.Time
	frameOff     int64
	frameSig     string
	// pending is the finish the decision rule asked for; a failed teardown holds it back and the next tick retries
	// instead of deciding again. finishing says a finish goroutine is on it.
	pending       *raidwalk.AutoDecision
	finishing     bool
	stopped       bool
	tearing       bool
	tornDown      bool
	teardownFails int
	escalated     bool
}

// autoState is one raid id and its place in its goal lines.
type autoState struct {
	mu        sync.Mutex
	id        string
	goals     []autoGoalState
	current   int
	seq       int
	lastOut   string
	run       *autoRun
	last      *autoRun
	nextReuse bool
	// launchFails counts the consecutive launches that could not start (the attempt log or the child): the backoff
	// grows and the third escalates instead of recording a launch failure as completion.
	launchFails int
	// placeFails counts the consecutive attempts that ended in the place phase: the third escalates (a dead bind is
	// re-placed fresh, but a bind that keeps failing must not loop with minutes of backoff forever).
	placeFails int
	nextAt     time.Time // no launch before this (the settle after a teardown, or a backoff)
	done       bool
	// escalated is an id left for a person (a teardown that never verified, an exit 2/29, three exit 15s): it counts
	// as done for allDone, is never launched again and is kept in the checkpoint until a person clears it.
	escalated bool
	// refused is this start's refusal (H2: the slot is not ours). It counts as done for this run but is not kept in
	// the checkpoint: the next start re-reads the slot and decides again.
	refused bool
	// escalation is the "kind id slot: detail" tail status.txt prints after ESCALATE.
	escalation string
}

// autoSupervisor owns the processes and files.
type autoSupervisor struct {
	o        autoOptions
	states   []*autoState
	events   *os.File
	evMu     sync.Mutex
	stderr   io.Writer
	lastStat time.Time
	// wg tracks the finish/teardown goroutines; stateMu serializes the checkpoint writes.
	wg      sync.WaitGroup
	stateMu sync.Mutex
	// rowsMu guards rowsCache: one rows read per target's plan + --dir + --sql (they do not change under a walk).
	rowsMu    sync.Mutex
	rowsCache map[string]autoRows
}

// autoRows is one target's rows, read once: ok is false when neither its --sql migration nor its plan file can be
// read, and a resume start then keeps the step it had.
type autoRows struct {
	rows planexport.Rows
	ok   bool
}

func raidAutopilotCmd(ctx context.Context, args []string, stdout, stderr io.Writer) int {
	o, code := parseAutoArgs(args, stderr)
	if code != exitOK {
		return code
	}
	targets, err := readAutoTargets(o.walkDir)
	if err != nil {
		fmt.Fprintf(stderr, "raid-autopilot: %v\n", err)
		return exitUsage
	}
	if o.dryRun {
		for _, t := range targets {
			out := filepath.Join(o.walkDir, fmt.Sprintf("%s-1", t.ID))
			fmt.Fprintf(stdout, "%s %s slot %s goal %s: %s\n", t.ID, t.Server, t.Slot, t.Goal, strings.Join(autoCommandArgs(o.binary, t, out, t.Start, false, o.forwardInstanceCap()), " "))
		}
		return exitOK
	}
	if !o.foreground && os.Getenv(autoChildEnv) == "" {
		return autoDetach(o, stdout, stderr)
	}
	if err := os.MkdirAll(o.walkDir, 0o700); err != nil {
		fmt.Fprintf(stderr, "raid-autopilot: %v\n", err)
		return exitUsage
	}
	// One supervisor per walk dir, held for this process's lifetime. The pid file is informational only: a start
	// that reads it and then races another one both find no live pid, so the lock is the guard.
	lock, err := autoLock(o.walkDir)
	if err != nil {
		fmt.Fprintf(stderr, "raid-autopilot: %v\n", err)
		return exitUsage
	}
	defer lock.Close()
	pidPath := filepath.Join(o.walkDir, autoPIDFile)
	pid := strconv.Itoa(os.Getpid())
	if err := os.WriteFile(pidPath, []byte(pid+"\n"), 0o600); err != nil {
		fmt.Fprintf(stderr, "raid-autopilot: pid file: %v\n", err)
		return exitUsage
	}
	defer autoRemovePIDFile(pidPath, pid)

	s := &autoSupervisor{o: o, stderr: stderr}
	if err := s.open(); err != nil {
		fmt.Fprintf(stderr, "raid-autopilot: %v\n", err)
		return exitUsage
	}
	defer s.close()
	s.states = groupAutoTargets(targets)
	s.initSeq()
	// A YIELD-DONE from an earlier YIELD is stale at this start (M6): this run writes its own only if a YIELD comes.
	_ = os.Remove(filepath.Join(o.walkDir, autoYieldDone))
	if err := s.reconcile(); err != nil {
		fmt.Fprintf(stderr, "raid-autopilot: %v\n", err)
		return exitUsage
	}
	s.writeStatus()
	for _, st := range s.states {
		st.mu.Lock()
		goal, n := st.goals[0].target.Goal.String(), len(st.goals)
		st.mu.Unlock()
		s.event(st.id, "supervise", fmt.Sprintf("goal %s (%d goal line(s))", goal, n))
	}
	return s.run(ctx)
}

func parseAutoArgs(args []string, stderr io.Writer) (autoOptions, int) {
	o := autoOptions{poll: autoDefaultPoll, stall: autoDefaultStall, settle: autoDefaultSettle, statusEvery: autoDefaultStatusEvery}
	fs := flag.NewFlagSet("raid-autopilot", flag.ContinueOnError)
	fs.SetOutput(stderr)
	fs.BoolVar(&o.foreground, "foreground", false, "run in this process (no setsid)")
	fs.StringVar(&o.binary, "binary", "", "the turtle-agent to launch raid-walk with (default: this executable)")
	fs.DurationVar(&o.poll, "poll", autoDefaultPoll, "how often the runs are read")
	fs.DurationVar(&o.stall, "stall", autoDefaultStall, "no step or boss-HP change for this long re-places the run")
	fs.DurationVar(&o.settle, "settle", autoDefaultSettle, "wait after a teardown before the next launch")
	fs.DurationVar(&o.statusEvery, "status-every", autoDefaultStatusEvery, "how often status.txt is written")
	fs.IntVar(&o.instanceCapPerAccount, "instance-cap-per-account", raidwalk.InstanceCapPerAccount, "the core's limit of fresh instance ids one account may enter in an hour, passed to every raid-walk (stock 5; without the flag each walk uses the server's entry in instance-cap-per-account.json)")
	fs.BoolVar(&o.dryRun, "dry-run", false, "validate the targets and print the first command, launch nothing")
	if err := fs.Parse(args); err != nil {
		return o, exitUsage
	}
	fs.Visit(func(f *flag.Flag) {
		if f.Name == "instance-cap-per-account" {
			o.instanceCapSet = true
		}
	})
	if o.instanceCapPerAccount < 1 {
		fmt.Fprintf(stderr, "raid-autopilot: --instance-cap-per-account must be 1 or more, not %d\n", o.instanceCapPerAccount)
		return o, exitUsage
	}
	if fs.NArg() != 1 {
		fmt.Fprintln(stderr, raidAutopilotUsage)
		return o, exitUsage
	}
	o.walkDir = slots.ExpandTilde(fs.Arg(0))
	if o.poll <= 0 || o.stall <= 0 || o.settle < 0 || o.statusEvery <= 0 {
		fmt.Fprintln(stderr, "raid-autopilot: --poll, --stall and --status-every must be positive; --settle not negative")
		return o, exitUsage
	}
	if o.binary == "" {
		exe, err := os.Executable()
		if err != nil {
			fmt.Fprintf(stderr, "raid-autopilot: %v\n", err)
			return o, exitUsage
		}
		o.binary = exe
	}
	return o, exitOK
}

func readAutoTargets(dir string) ([]raidwalk.AutoTarget, error) {
	f, err := os.Open(filepath.Join(dir, autoTargetsFile))
	if err != nil {
		return nil, err
	}
	defer f.Close()
	return raidwalk.ParseAutoTargets(f)
}

// groupAutoTargets keeps the file's id order and each id's goal order.
func groupAutoTargets(targets []raidwalk.AutoTarget) []*autoState {
	byID := map[string]*autoState{}
	var out []*autoState
	for _, t := range targets {
		st := byID[t.ID]
		if st == nil {
			st = &autoState{id: t.ID}
			byID[t.ID] = st
			out = append(out, st)
		}
		st.goals = append(st.goals, autoGoalState{target: t})
	}
	return out
}

// forwardInstanceCap is the --instance-cap-per-account to pass on, 0 when the flag was not given (raid-walk then
// resolves the per-server value of instance-cap-per-account.json itself).
func (o autoOptions) forwardInstanceCap() int {
	if o.instanceCapSet {
		return o.instanceCapPerAccount
	}
	return 0
}

// autoCommandArgs is the raid-walk command for one attempt. from is the start the attempt asks for (the furthest step
// a run reached, or the target's own --from); reuse asks for the same instance first. H1: --reuse-instance is never
// sent without --place/--from, because raid-walk refuses it there. instanceCap > 0 is passed on as
// --instance-cap-per-account.
func autoCommandArgs(binary string, t raidwalk.AutoTarget, out, from string, reuse bool, instanceCap int) []string {
	args := []string{"raid-walk"}
	args = append(args, t.Args...)
	args = append(args,
		"--server", t.Server,
		"--slot", t.Slot,
		"--holder", t.Holder,
		"--until", t.Goal.Until(),
		"--out", out,
	)
	if from != "" {
		args = append(args, "--from", from, "--place")
	}
	if reuse && from != "" {
		args = append(args, "--reuse-instance")
	}
	if instanceCap > 0 {
		args = append(args, "--instance-cap-per-account", strconv.Itoa(instanceCap))
	}
	return args
}

// autoFlag reads a target argument's value, bare or as name=value.
func autoFlag(args []string, name string) string {
	for i, a := range args {
		n, v, has := strings.Cut(a, "=")
		if n != name {
			continue
		}
		if has {
			return v
		}
		if i+1 < len(args) {
			return args[i+1]
		}
	}
	return ""
}

// autoReadRows reads the rows a target's raid-walk places against: the plan in its --sql migration (the deployed rows,
// which the walk itself reads), else the plan file's built rows. ok is false when neither can be read.
func autoReadRows(t raidwalk.AutoTarget) (planexport.Rows, bool) {
	var plan string
	if len(t.Args) > 0 {
		plan = t.Args[0]
	}
	dir := autoFlag(t.Args, "--dir")
	if dir == "" {
		dir = "dungeons"
	}
	if sql := autoFlag(t.Args, "--sql"); sql != "" {
		if raw, err := os.ReadFile(slots.ExpandTilde(sql)); err == nil {
			if plans, _, err := planexport.ParseMigration(string(raw)); err == nil {
				for _, r := range plans {
					if r.Plan.Plan == plan {
						return r, true
					}
				}
			}
		}
	}
	p, err := dungeon.Load(filepath.Join(slots.ExpandTilde(dir), plan+".json"))
	if err != nil {
		return planexport.Rows{}, false
	}
	res, err := planexport.Build(p, planexport.Options{})
	if err != nil {
		return planexport.Rows{}, false
	}
	return res.Rows, true
}

// targetRows is autoReadRows once per plan + --dir + --sql.
func (s *autoSupervisor) targetRows(t raidwalk.AutoTarget) (planexport.Rows, bool) {
	key := t.Args[0] + "\x00" + autoFlag(t.Args, "--dir") + "\x00" + autoFlag(t.Args, "--sql")
	s.rowsMu.Lock()
	if e, ok := s.rowsCache[key]; ok {
		s.rowsMu.Unlock()
		return e.rows, e.ok
	}
	s.rowsMu.Unlock()
	rows, ok := autoReadRows(t)
	s.rowsMu.Lock()
	if s.rowsCache == nil {
		s.rowsCache = map[string]autoRows{}
	}
	s.rowsCache[key] = autoRows{rows: rows, ok: ok}
	s.rowsMu.Unlock()
	return rows, ok
}

// autoResumeStart makes a --from "step N" a start raid-walk takes: a restart resumes at the furthest step a run
// reached, and after a wipe at a boss that row is the boss row raid-walk refuses with exit 2 (walk 39: every wipe at
// a boss escalated the id). PlaceResumeStart steps back to the nearest accepted row of the same leg, else the leg's
// own start. Any other --from is returned unchanged.
func (s *autoSupervisor) autoResumeStart(t raidwalk.AutoTarget, from string) string {
	step, ok := strings.CutPrefix(from, "step ")
	if !ok {
		return from
	}
	n, err := strconv.Atoi(step)
	if err != nil {
		return from
	}
	rows, ok := s.targetRows(t)
	if !ok {
		return from
	}
	if words, ok := raidwalk.PlaceResumeStart(rows, n); ok {
		return words
	}
	return from
}

// initSeq continues the out numbering after the dirs an earlier autopilot made: the walk refuses an existing --out,
// and a relaunch must not think every attempt is the first. It also remembers each id's latest out dir.
func (s *autoSupervisor) initSeq() {
	entries, err := os.ReadDir(s.o.walkDir)
	if err != nil {
		return
	}
	byID := map[string]*autoState{}
	for _, st := range s.states {
		byID[st.id] = st
	}
	for _, e := range entries {
		name := e.Name()
		for id, st := range byID {
			prefix := id + "-"
			if !strings.HasPrefix(name, prefix) {
				continue
			}
			n, err := strconv.Atoi(strings.TrimSuffix(strings.TrimPrefix(name, prefix), ".out"))
			if err != nil {
				continue
			}
			if n > st.seq {
				st.seq = n
			}
			if n >= st.seq {
				st.lastOut = filepath.Join(s.o.walkDir, fmt.Sprintf("%s-%d", id, n))
			}
		}
	}
}

func (s *autoSupervisor) open() error {
	if err := os.MkdirAll(s.o.walkDir, 0o700); err != nil {
		return err
	}
	f, err := os.OpenFile(filepath.Join(s.o.walkDir, autoEventsFile), os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	s.events = f
	return nil
}

func (s *autoSupervisor) close() {
	if s.events != nil {
		s.events.Close()
	}
}

func (s *autoSupervisor) event(id, action, detail string) {
	if id == "" {
		id = "all"
	}
	s.eventLine(fmt.Sprintf("%s %s %s: %s", time.Now().UTC().Format(time.RFC3339), id, action, detail))
}

// eventLine writes an already built line to EVENTS.txt and the supervisor's stderr.
func (s *autoSupervisor) eventLine(line string) {
	s.evMu.Lock()
	defer s.evMu.Unlock()
	if s.events != nil {
		fmt.Fprintln(s.events, line)
	}
	fmt.Fprintln(s.stderr, "raid-autopilot: "+line)
}

// autoLock takes <dir>/autopilot.lock exclusively (LOCK_EX|LOCK_NB) and holds it as long as the returned file is
// open: the OS drops the lock when the process ends, so a crashed supervisor never leaves a stale guard. A second
// start exits with an error instead of running a second supervisor on the same targets, slots and out numbering.
func autoLock(dir string) (*os.File, error) {
	path := filepath.Join(dir, autoLockFile)
	f, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return nil, fmt.Errorf("lock: %v", err)
	}
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		f.Close()
		return nil, fmt.Errorf("another autopilot runs in %s (it holds %s)", dir, path)
	}
	return f, nil
}

// autoRemovePIDFile removes the informational pid file only while it still names this process: a supervisor that lost
// the lock (or was replaced) never deletes its successor's file.
func autoRemovePIDFile(path, pid string) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return
	}
	if strings.TrimSpace(string(raw)) == pid {
		_ = os.Remove(path)
	}
}

// autoDetach starts the detached supervisor and waits until it holds the lock (its pid file names it) or reports its
// refusal (it exits): a caller that gets exit 0 knows the supervisor is there, not merely forked (L2).
func autoDetach(o autoOptions, stdout, stderr io.Writer) int {
	exe, err := os.Executable()
	if err != nil {
		fmt.Fprintf(stderr, "raid-autopilot: %v\n", err)
		return exitUsage
	}
	// os.Args[0] is the command; [1] is raid-autopilot; pass the rest through. The child's env marker keeps it in
	// this process path (it writes its own pid file after its guard).
	dir := o.walkDir
	if err := os.MkdirAll(dir, 0o700); err != nil {
		fmt.Fprintf(stderr, "raid-autopilot: %v\n", err)
		return exitUsage
	}
	logPath := filepath.Join(dir, autoLogFile)
	f, err := os.OpenFile(logPath, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		fmt.Fprintf(stderr, "raid-autopilot: %v\n", err)
		return exitUsage
	}
	defer f.Close()
	args := append([]string{"raid-autopilot"}, os.Args[2:]...)
	cmd := exec.Command(exe, args...)
	cmd.Env = append(os.Environ(), autoChildEnv+"=1")
	cmd.Stdout, cmd.Stderr = f, f
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	if err := cmd.Start(); err != nil {
		fmt.Fprintf(stderr, "raid-autopilot: %v\n", err)
		return exitUsage
	}
	pid := cmd.Process.Pid
	pidPath := filepath.Join(dir, autoPIDFile)
	exited := make(chan struct{})
	go func() {
		_ = cmd.Wait()
		close(exited)
	}()
	deadline := time.Now().Add(autoDetachWait)
	for time.Now().Before(deadline) {
		if raw, err := os.ReadFile(pidPath); err == nil && strings.TrimSpace(string(raw)) == strconv.Itoa(pid) {
			fmt.Fprintf(stdout, "raid-autopilot: pid %d, log %s\n", pid, logPath)
			return exitOK
		}
		select {
		case <-exited:
			code := exitUsage
			if cmd.ProcessState != nil {
				code = cmd.ProcessState.ExitCode()
			}
			fmt.Fprintf(stderr, "raid-autopilot: the detached start exited (%d): %s\n", code, autoTail(logPath))
			return code
		case <-time.After(50 * time.Millisecond):
		}
	}
	fmt.Fprintf(stdout, "raid-autopilot: pid %d, log %s (the lock was not held yet)\n", pid, logPath)
	return exitOK
}

// run is the loop: YIELD, observe, decide, act, status every statusEvery.
func (s *autoSupervisor) run(ctx context.Context) int {
	ticker := time.NewTicker(s.o.poll)
	defer ticker.Stop()
	yield := time.NewTicker(autoYieldPoll)
	defer yield.Stop()
	if s.yieldNow() {
		return exitOK
	}
	s.tick(ctx)
	if s.allDone() {
		return s.finishAll()
	}
	for {
		select {
		case <-ctx.Done():
			s.shutdown("signal")
			return exitOK
		case <-yield.C:
			if s.yieldNow() {
				return exitOK
			}
		case <-ticker.C:
			if s.yieldNow() {
				return exitOK
			}
			s.tick(ctx)
			if time.Since(s.lastStat) >= s.o.statusEvery {
				s.writeStatus()
				s.lastStat = time.Now()
			}
			if s.allDone() {
				return s.finishAll()
			}
		}
	}
}

// yieldNow handles a YIELD file: the stale YIELD-DONE of an earlier yield goes first (M6), then the shutdown.
func (s *autoSupervisor) yieldNow() bool {
	if _, err := os.Stat(filepath.Join(s.o.walkDir, autoYieldFile)); err != nil {
		return false
	}
	_ = os.Remove(filepath.Join(s.o.walkDir, autoYieldDone))
	s.shutdown("YIELD")
	return true
}

// allDone says every id is finished or left for a person (L5: an escalated or refused id counts as done).
func (s *autoSupervisor) allDone() bool {
	for _, st := range s.states {
		st.mu.Lock()
		finished := st.done || st.escalated || st.refused
		st.mu.Unlock()
		if !finished {
			return false
		}
	}
	return true
}

// allGoalsMet says every id met its goals. An escalated or refused id did not, so the global done event is not for it.
func (s *autoSupervisor) allGoalsMet() bool {
	for _, st := range s.states {
		st.mu.Lock()
		met := st.done
		st.mu.Unlock()
		if !met {
			return false
		}
	}
	return true
}

// finishAll ends the supervisor: a finish goroutine may still be writing the checkpoint, so it waits for the
// goroutines, writes status.txt, and only says "every target's goals are met" when every id met them. An escalated
// id makes the exit non-zero (31), so a supervisor chain sees that the run ended for a person.
func (s *autoSupervisor) finishAll() int {
	s.wg.Wait()
	s.writeStatus()
	if s.allGoalsMet() {
		s.event("all", "done", "every target's goals are met")
		return exitOK
	}
	escalated, refused := 0, 0
	for _, st := range s.states {
		st.mu.Lock()
		if st.escalated {
			escalated++
		}
		if st.refused {
			refused++
		}
		st.mu.Unlock()
	}
	s.event("all", "finished", fmt.Sprintf("not every target's goals are met (%d escalated, %d refused)", escalated, refused))
	if escalated > 0 {
		return exitAutoEscalated
	}
	return exitOK
}

// tick advances every id once.
func (s *autoSupervisor) tick(ctx context.Context) {
	for _, st := range s.states {
		s.tickState(st)
	}
	s.saveState()
}

// tickState observes one id and either launches its next attempt or hands a decided finish to its own goroutine: a
// stop or a teardown never blocks another id (M7).
func (s *autoSupervisor) tickState(st *autoState) {
	st.mu.Lock()
	if st.done || st.escalated || st.refused {
		st.mu.Unlock()
		return
	}
	if st.run == nil {
		due := !time.Now().Before(st.nextAt)
		st.mu.Unlock()
		if due {
			s.launch(st)
		}
		return
	}
	r := st.run
	s.pollRun(st)
	if r.escalated || r.finishing {
		st.mu.Unlock()
		return
	}
	if r.pending == nil {
		obs := s.observe(st)
		if obs.Exited && obs.ObserveErr != "" && time.Since(r.exitedAt) < autoSummaryGrace {
			st.mu.Unlock()
			return
		}
		g := st.goals[st.current]
		// The backoff counts the consecutive no-progress attempts that ended before this one: a run that reached a
		// further step resets the count even though its own finish has not run yet.
		fast := g.noProgress
		if obs.Exited {
			step := s.runLastStepLocked(r)
			r.lastStep = step
			if step > g.bestStep {
				fast = 0
			}
		}
		d := raidwalk.DecideAutoAction(obs, raidwalk.AutoRunState{
			Goal: r.goal, Attempt: g.attempt, Reuse: r.reuse, HasRun: g.attempt > 0,
			FastFailures: fast, LeaseFails: g.leaseFails, PlaceFails: st.placeFails,
		}, time.Now(), s.o.stall)
		if d.Action == raidwalk.AutoContinue {
			st.mu.Unlock()
			return
		}
		r.pending = &d
	}
	r.finishing = true
	st.mu.Unlock()
	s.startFinish(st)
}

func (s *autoSupervisor) startFinish(st *autoState) {
	s.wg.Add(1)
	go func() {
		defer s.wg.Done()
		s.finish(st)
	}()
}

// finish applies a decided finish: the walk is stopped, the slot is torn down and verified, and only then is the run
// cleared and the action (advance, stop or restart) taken. A teardown that does not verify keeps the run and the
// pending decision: the next tick retries it, and the third failure escalates and leaves the slot alone.
func (s *autoSupervisor) finish(st *autoState) {
	st.mu.Lock()
	r := st.run
	if r == nil || r.pending == nil || !r.finishing {
		st.mu.Unlock()
		return
	}
	d := *r.pending
	slot := r.slot
	st.mu.Unlock()

	s.stopRun(st, r)
	lastStep := s.runLastStep(st, r)
	if err := s.teardown(st, r); err != nil {
		st.mu.Lock()
		r.teardownFails++
		r.finishing = false
		fails := r.teardownFails
		if fails >= autoTeardownTries {
			r.escalated = true
		}
		st.mu.Unlock()
		if fails >= autoTeardownTries {
			s.escalate(st, "teardown", fmt.Sprintf("%s: %s", slot, err.Error()))
			return
		}
		s.event(st.id, "teardown-failed", fmt.Sprintf("%v; the run stays active, the teardown is retried next tick (fail %d of %d)", err, fails, autoTeardownTries))
		return
	}

	st.mu.Lock()
	st.run = nil
	r.tornDown = true
	r.finishing = false
	r.lastStep = lastStep
	st.last = r
	g := &st.goals[st.current]
	// Progress resets the backoff; a run that reached no further step grows it (M1).
	if lastStep > g.bestStep {
		g.bestStep = lastStep
		g.noProgress = 0
	} else {
		g.noProgress++
	}
	if r.exitCode == 15 {
		g.leaseFails++
	} else {
		g.leaseFails = 0
	}
	// A fresh-instance restart is a placement that failed on the old bind: count those in a row, and reset the count
	// on every other end (a run that placed, advanced or stopped broke the streak).
	if d.Action == raidwalk.AutoRestart && !d.Reuse {
		st.placeFails++
	} else {
		st.placeFails = 0
	}
	var esc string
	switch d.Action {
	case raidwalk.AutoAdvance:
		if st.current+1 < len(st.goals) {
			st.current++
			st.last = nil
			st.nextReuse = true
			st.goals[st.current] = autoGoalState{target: st.goals[st.current].target}
			esc = fmt.Sprintf("advance: %s; next goal %s", d.Reason, st.goals[st.current].target.Goal.String())
		} else {
			st.done = true
			esc = "done: the last goal is met (" + d.Reason + ")"
		}
		st.nextAt = time.Now().Add(s.o.settle)
	case raidwalk.AutoStop:
		esc = "stop: " + d.Reason
		if d.Escalate {
			st.escalated = true
			esc = "stop: " + d.Reason
		} else {
			st.done = true
		}
	case raidwalk.AutoRestart:
		st.last.reuse = d.Reuse
		wait := s.o.settle
		if d.Backoff > wait {
			wait = d.Backoff
		}
		st.nextAt = time.Now().Add(wait)
		esc = fmt.Sprintf("restart: %s; next %s after %s", d.Reason, s.nextPlacement(d.Reuse), wait.Round(time.Second))
	}
	st.mu.Unlock()

	action, _, _ := strings.Cut(esc, ":")
	s.event(st.id, action, strings.TrimSpace(strings.TrimPrefix(esc, action+":")))
	if d.Action == raidwalk.AutoStop && d.Escalate {
		s.escalate(st, "stop", fmt.Sprintf("%s: %s", slot, d.Reason))
		return
	}
	s.saveState()
	s.writeStatus()
}

// runLastStep is the furthest step the run reached: the live reading, else its log, else its summary.
func (s *autoSupervisor) runLastStep(st *autoState, r *autoRun) int {
	st.mu.Lock()
	defer st.mu.Unlock()
	return s.runLastStepLocked(r)
}

// runLastStepLocked is runLastStep with the state's mutex already held.
func (s *autoSupervisor) runLastStepLocked(r *autoRun) int {
	step := r.lastStep
	if step > 0 || r.out == "" {
		return step
	}
	if step = autoLastStep(filepath.Join(r.out, "run.log")); step > 0 {
		return step
	}
	if sum, err := autoReadSummary(filepath.Join(r.out, "summary.json")); err == nil {
		return sum.StepsReached
	}
	return 0
}

func (s *autoSupervisor) nextPlacement(reuse bool) string {
	if reuse {
		return "same instance (--reuse-instance) first"
	}
	return "fresh instance"
}

// launch starts the current goal's next attempt from the furthest step a run of it reached, else the target's own
// --from, else a fresh start (H1: no --reuse-instance without a --from/--place).
func (s *autoSupervisor) launch(st *autoState) {
	st.mu.Lock()
	if st.done || st.escalated || st.refused {
		st.mu.Unlock()
		return
	}
	if st.current >= len(st.goals) {
		st.done = true
		st.mu.Unlock()
		s.event(st.id, "done", "no goal line left")
		s.saveState()
		return
	}
	g := &st.goals[st.current]
	step, reuse, fresh := 0, st.nextReuse, false
	if st.last != nil {
		step = st.last.lastStep
		reuse = st.last.reuse
		// A fresh-instance decision (a placement that failed on the old bind) survives a reached step: the retry
		// keeps --from step N --place but must not ask for --reuse-instance, or it loops on the dead bind forever.
		fresh = !st.last.reuse
	}
	if g.bestStep > step {
		step = g.bestStep
	}
	from := g.target.Start
	if step > 0 {
		from = fmt.Sprintf("step %d", step)
		reuse = !fresh
	}
	resumeFrom := ""
	if from != "" {
		// A step raid-walk refuses as a start (the boss row a wipe died on) is not a reason to escalate: resume
		// from the nearest row of the same leg it takes.
		if adjusted := s.autoResumeStart(g.target, from); adjusted != from {
			resumeFrom = fmt.Sprintf("%q is not a place start: resuming from %q", from, adjusted)
			from = adjusted
		}
	}
	if from == "" {
		reuse = false
	}
	st.nextReuse = false
	g.attempt++
	st.seq++
	seq := st.seq
	out := filepath.Join(s.o.walkDir, fmt.Sprintf("%s-%d", st.id, seq))
	target := g.target
	attempt := g.attempt
	st.mu.Unlock()

	args := autoCommandArgs(s.o.binary, target, out, from, reuse, s.o.forwardInstanceCap())
	f, err := os.OpenFile(out+".out", os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		s.failLaunch(st, err)
		return
	}
	cmd := exec.Command(s.o.binary, args...)
	cmd.Stdout, cmd.Stderr = f, f
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	if err := cmd.Start(); err != nil {
		f.Close()
		s.failLaunch(st, err)
		return
	}
	run := &autoRun{
		seq: seq, out: out, slot: target.Slot, reuse: reuse, goal: target.Goal, cmd: cmd,
		pidN: cmd.Process.Pid, started: time.Now(), lastProgress: time.Now(),
	}
	go func() {
		_ = cmd.Wait()
		// cmd.Wait does not close a parent-supplied *os.File; the supervisor restarts indefinitely, so every attempt
		// must close its log or it runs out of descriptors.
		f.Close()
		st.mu.Lock()
		if !run.exited {
			run.exited = true
			run.exitedAt = time.Now()
			if cmd.ProcessState != nil {
				run.exitCode = cmd.ProcessState.ExitCode()
			}
		}
		st.mu.Unlock()
	}()
	st.mu.Lock()
	st.run = run
	st.lastOut = out
	st.launchFails = 0
	st.mu.Unlock()
	if resumeFrom != "" {
		s.event(st.id, "resume", resumeFrom)
	}
	s.event(st.id, "launch", fmt.Sprintf("attempt %d goal %s out %s from %q reuse %v", attempt, target.Goal, out, from, reuse))
	s.saveState()
}

// failLaunch schedules the retry of a launch that could not start. A launch failure is not completion: the id keeps
// its goal and retries with the restart backoff. The third consecutive failure escalates the id, so a broken binary
// or an unwritable walk dir ends at a person instead of spinning or claiming the goal was met.
func (s *autoSupervisor) failLaunch(st *autoState, err error) {
	st.mu.Lock()
	st.launchFails++
	fails := st.launchFails
	wait := raidwalk.AutoBackoff(fails - 1)
	st.nextAt = time.Now().Add(wait)
	st.mu.Unlock()
	if fails >= autoLaunchTries {
		s.escalate(st, "launch", err.Error())
		return
	}
	s.event(st.id, "launch-failed", fmt.Sprintf("%v; retrying in %s (fail %d of %d)", err, wait.Round(time.Second), fails, autoLaunchTries))
	s.saveState()
}

// pollRun reads the child's exit and its progress (the state's mutex is held).
func (s *autoSupervisor) pollRun(st *autoState) {
	r := st.run
	if r == nil || r.exited {
		return
	}
	// An adopted walk has no wait channel: its pid (and its group) say whether it is still there.
	if r.adopted && !processAlive(r.pidN) {
		r.exited = true
		r.exitedAt = time.Now()
		if sum, err := autoReadSummary(filepath.Join(r.out, "summary.json")); err == nil {
			r.exitCode = sum.ExitCode
		}
		return
	}
	if step := autoLastStep(filepath.Join(r.out, "run.log")); step != r.lastStep {
		r.lastStep = step
		r.lastProgress = time.Now()
	}
	if sig := autoFrameSignature(filepath.Join(r.out, "frames.jsonl"), r); sig != "" && sig != r.frameSig {
		r.frameSig = sig
		r.lastProgress = time.Now()
	}
}

// observe builds the decision input from the run's files (the state's mutex is held).
func (s *autoSupervisor) observe(st *autoState) raidwalk.AutoObservation {
	r := st.run
	o := raidwalk.AutoObservation{
		Exited: r.exited, ExitCode: r.exitCode,
		KilledBosses: map[uint32]bool{}, LastProgress: r.lastProgress,
	}
	if !r.exited {
		o.StepsReached = r.lastStep
		o.EndReason = autoLastReason(filepath.Join(r.out, "run.log"))
		o.StallAfter = autoCapFreeTime(filepath.Join(r.out, "run.log"))
		return o
	}
	sum, err := autoReadSummary(filepath.Join(r.out, "summary.json"))
	if err != nil {
		o.ObserveErr = err.Error()
		o.EndReason = "no summary yet"
		return o
	}
	o.StepsReached, o.StepsTotal, o.EndReason = sum.StepsReached, sum.StepsTotal, sum.EndReason
	if sum.ExitCode != 0 {
		o.ExitCode = sum.ExitCode
	}
	for _, b := range sum.BossKills {
		if b.Killed {
			o.KilledBosses[b.Entry] = true
		}
	}
	if sum.BossKill != nil && sum.BossKill.Killed {
		o.KilledBosses[sum.BossKill.Entry] = true
	}
	return o
}

// autoSummary is the part of summary.json the autopilot reads.
type autoSummary struct {
	ExitCode     int    `json:"exit_code"`
	StepsReached int    `json:"steps_reached"`
	StepsTotal   int    `json:"steps_total"`
	EndReason    string `json:"end_reason"`
	BossKills    []struct {
		Entry  uint32 `json:"entry"`
		Killed bool   `json:"killed"`
	} `json:"boss_kills"`
	// BossKill is the last boss the walk engaged (older summaries only; boss_kills has all of them).
	BossKill *struct {
		Entry  uint32 `json:"entry"`
		Killed bool   `json:"killed"`
	} `json:"boss_kill"`
}

func autoReadSummary(path string) (autoSummary, error) {
	var s autoSummary
	raw, err := os.ReadFile(path)
	if err != nil {
		return s, err
	}
	if err := json.Unmarshal(raw, &s); err != nil {
		return s, err
	}
	return s, nil
}

// autoLastStep reads the step of the last TBM:RUN line in run.log (0: none).
func autoLastStep(path string) int {
	f := strings.Split(autoLastRunLine(path), "|")
	if len(f) < 4 {
		return 0
	}
	step, _, _ := strings.Cut(f[3], "/")
	n, _ := strconv.Atoi(step)
	return n
}

// autoLastReason is the last TBM:RUN note.
func autoLastReason(path string) string {
	f := strings.Split(autoLastRunLine(path), "|")
	if len(f) < 8 {
		return ""
	}
	return f[7]
}

func autoLastRunLine(path string) string {
	f, err := os.Open(path)
	if err != nil {
		return ""
	}
	defer f.Close()
	st, err := f.Stat()
	if err != nil || st.Size() == 0 {
		return ""
	}
	const tail = 256 * 1024
	start := st.Size() - tail
	if start < 0 {
		start = 0
	}
	buf := make([]byte, st.Size()-start)
	if _, err := f.ReadAt(buf, start); err != nil && err != io.EOF {
		return ""
	}
	lines := strings.Split(string(buf), "\n")
	for i := len(lines) - 1; i >= 0; i-- {
		if j := strings.Index(lines[i], "TBM:RUN|"); j >= 0 {
			return strings.TrimSpace(lines[i][j:])
		}
	}
	return ""
}

// autoCapFreeTime reads the "earliest free time" of a cap wait only while the walk's LAST line is that wait: a walk
// that waits on the instance cap (or on a hired account's cap) is legitimately idle until then, so the stall rule
// must not fire before it, and its clock starts when the wait ends (M4).
func autoCapFreeTime(path string) time.Time {
	raw := autoTail(path)
	lines := strings.Split(raw, "\n")
	var last string
	for i := len(lines) - 1; i >= 0; i-- {
		if strings.TrimSpace(lines[i]) != "" {
			last = lines[i]
			break
		}
	}
	if !strings.Contains(last, "instance cap: waiting") {
		return time.Time{}
	}
	i := strings.LastIndex(last, "earliest free time ")
	if i < 0 {
		return time.Time{}
	}
	word := last[i+len("earliest free time "):]
	if j := strings.IndexByte(word, ' '); j >= 0 {
		word = word[:j]
	}
	if t, err := time.Parse(time.RFC3339, strings.TrimRight(strings.TrimSpace(word), "),.;")); err == nil {
		return t
	}
	return time.Time{}
}

func autoTail(path string) string {
	f, err := os.Open(path)
	if err != nil {
		return ""
	}
	defer f.Close()
	st, err := f.Stat()
	if err != nil || st.Size() == 0 {
		return ""
	}
	const tail = 256 * 1024
	start := st.Size() - tail
	if start < 0 {
		start = 0
	}
	buf := make([]byte, st.Size()-start)
	if _, err := f.ReadAt(buf, start); err != nil && err != io.EOF {
		return ""
	}
	return string(buf)
}

// autoFrameSignature reads the frames.jsonl lines added since r.frameOff and returns a signature of the hostile HP
// percentages. An unchanged signature is no boss-HP progress; a change is progress.
func autoFrameSignature(path string, r *autoRun) string {
	f, err := os.Open(path)
	if err != nil {
		return ""
	}
	defer f.Close()
	st, err := f.Stat()
	if err != nil {
		return ""
	}
	if st.Size() < r.frameOff {
		r.frameOff = 0
	}
	if st.Size() == r.frameOff {
		return ""
	}
	if _, err := f.Seek(r.frameOff, io.SeekStart); err != nil {
		return ""
	}
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 0, 64*1024), 4*1024*1024)
	sig := r.frameSig
	for sc.Scan() {
		var frame struct {
			Hostiles []struct {
				GUID  string `json:"guid"`
				Entry uint32 `json:"entry"`
				HPPct int    `json:"hp_pct"`
			} `json:"hostiles"`
		}
		if json.Unmarshal(sc.Bytes(), &frame) != nil {
			continue
		}
		parts := make([]string, 0, len(frame.Hostiles))
		for _, h := range frame.Hostiles {
			parts = append(parts, fmt.Sprintf("%s=%d:%d", h.GUID, h.Entry, h.HPPct))
		}
		sort.Strings(parts)
		sig = "none"
		if len(parts) > 0 {
			sig = strings.Join(parts, ",")
		}
	}
	r.frameOff = st.Size()
	return sig
}

// processAlive says the pid answers, or any process of its group does: a walk is its own group leader (Setpgid), so
// the group check catches the login child a stopped walk left behind (L1). A pid that answers with EPERM exists.
func processAlive(pid int) bool {
	if pid <= 0 {
		return false
	}
	for _, p := range []int{pid, -pid} {
		err := syscall.Kill(p, 0)
		if err == nil || err == syscall.EPERM {
			return true
		}
	}
	return false
}

// stopRun ends the child gracefully: SIGTERM the walk's process group (its own signal handler stops the run and tears
// down), then SIGKILL after the grace. The autopilot never sends `.bot run stop`.
func (s *autoSupervisor) stopRun(st *autoState, r *autoRun) {
	st.mu.Lock()
	if runExitedLocked(r) || r.stopped || r.pidN <= 0 {
		r.stopped = true
		st.mu.Unlock()
		return
	}
	r.stopped = true
	pid := r.pidN
	st.mu.Unlock()
	_ = syscall.Kill(-pid, syscall.SIGTERM)
	if !s.waitPidGone(st, r, pid, autoKillGrace) {
		s.event(st.id, "kill", fmt.Sprintf("pid %d did not stop in %s; SIGKILL", pid, autoKillGrace))
		_ = syscall.Kill(-pid, syscall.SIGKILL)
		s.waitPidGone(st, r, pid, autoKillWait)
	}
}

// runExitedLocked says the run's walk ended (the state's mutex is held). An adopted run has no Wait goroutine, so
// its pid (and its group) is the source: the exit is not observed by anyone else, and treating it as running would
// SIGKILL a dead pid and call the teardown unclean on a YIELD.
func runExitedLocked(r *autoRun) bool {
	if r.exited {
		return true
	}
	return r.adopted && !processAlive(r.pidN)
}

// waitPidGone waits until the run's pid (and its group) is gone, bounded by d.
func (s *autoSupervisor) waitPidGone(st *autoState, r *autoRun, pid int, d time.Duration) bool {
	deadline := time.Now().Add(d)
	for {
		st.mu.Lock()
		exited := runExitedLocked(r)
		st.mu.Unlock()
		if exited && !processAlive(pid) {
			return true
		}
		if !time.Now().Before(deadline) {
			return false
		}
		time.Sleep(200 * time.Millisecond)
	}
}

// processGone says the attempt's walk process (and its group) ended: the wait saw it exit and no process of its group
// answers any more.
func processGone(pid int, exited bool) bool {
	if !exited {
		return false
	}
	if pid <= 0 {
		return true
	}
	return !processAlive(pid)
}

// teardown tears the run's slot down and verifies it: the walk process is gone and the slot lease is released. The
// first attempt is a plain teardown; when it failed or the lease is kept, a second one runs with --release-anyway
// (the brief's "twice if the lease is kept"). A non-nil error means the slot is not clean: the caller keeps the run
// and never relaunches on that slot. A run already torn down, or a slot dir that no longer exists, is clean.
func (s *autoSupervisor) teardown(st *autoState, r *autoRun) error {
	if r.out == "" {
		return nil
	}
	st.mu.Lock()
	if r.tornDown {
		st.mu.Unlock()
		return nil
	}
	if r.escalated {
		st.mu.Unlock()
		return fmt.Errorf("teardown %s: the slot is escalated and left alone", r.out)
	}
	if r.tearing {
		// Another goroutine is tearing this run down (the shutdown and a finish race): its verdict is the run's.
		st.mu.Unlock()
		return nil
	}
	r.tearing = true
	pid, exited := r.pidN, runExitedLocked(r)
	st.mu.Unlock()

	slotDir := filepath.Join(r.out, "slot")
	if _, err := os.Stat(slotDir); err != nil {
		if !processGone(pid, exited) {
			st.mu.Lock()
			r.tearing = false
			st.mu.Unlock()
			return fmt.Errorf("teardown %s: the walk process %d is still alive", r.out, pid)
		}
		st.mu.Lock()
		r.tearing = false
		r.tornDown = true
		st.mu.Unlock()
		return nil
	}
	ok, detail := s.teardownOnce(slotDir, false)
	unclean := s.teardownUnclean(slotDir, pid, exited, ok)
	if unclean == "" {
		st.mu.Lock()
		r.tearing = false
		r.tornDown = true
		st.mu.Unlock()
		s.event(st.id, "teardown", detail)
		return nil
	}
	s.event(st.id, "teardown", detail+"; not clean ("+unclean+"), trying again with --release-anyway")
	ok, detail = s.teardownOnce(slotDir, true)
	unclean = s.teardownUnclean(slotDir, pid, exited, ok)
	st.mu.Lock()
	r.tearing = false
	st.mu.Unlock()
	if unclean != "" {
		return fmt.Errorf("%s: %s", detail, unclean)
	}
	st.mu.Lock()
	r.tornDown = true
	st.mu.Unlock()
	s.event(st.id, "teardown", detail)
	return nil
}

// teardownUnclean names what is still unclean after a teardown ("" when the slot is verified clean): the command
// failed, the walk process is alive, or the lease is held or cannot be verified. An unverifiable lease is not called
// released: a second supervisor must never place on a slot whose lease state is unknown.
func (s *autoSupervisor) teardownUnclean(slotDir string, pid int, exited bool, teardownOK bool) string {
	var left []string
	if !teardownOK {
		left = append(left, "the teardown command failed")
	}
	if !processGone(pid, exited) {
		left = append(left, fmt.Sprintf("the walk process %d is still alive", pid))
	}
	held, err := s.leaseHeld(slotDir)
	switch {
	case err != nil:
		left = append(left, "the lease cannot be verified: "+err.Error())
	case held:
		left = append(left, "the slot lease is still held")
	}
	return strings.Join(left, "; ")
}

// escalate writes the ESCALATE line (EVENTS.txt and status.txt) and marks the id: it is left alone until a person
// clears it (no relaunch, no further teardown, no YIELD-DONE).
func (s *autoSupervisor) escalate(st *autoState, kind, detail string) {
	st.mu.Lock()
	slot := st.goals[0].target.Slot
	if st.current < len(st.goals) {
		slot = st.goals[st.current].target.Slot
	}
	st.escalated = true
	st.escalation = fmt.Sprintf("%s %s %s: %s", kind, st.id, slot, detail)
	line := st.escalation
	st.mu.Unlock()
	s.eventLine(fmt.Sprintf("%s ESCALATE %s", time.Now().UTC().Format(time.RFC3339), line))
	s.writeStatus()
	s.saveState()
}

// refuse leaves an id for a person at startup: its slot is not ours to launch on (H2). The refusal is not written
// into the checkpoint, so a restart after the operator fixed the slot decides again.
func (s *autoSupervisor) refuse(st *autoState, why string) {
	st.mu.Lock()
	slot := st.goals[0].target.Slot
	if st.current < len(st.goals) {
		slot = st.goals[st.current].target.Slot
	}
	st.refused = true
	st.escalation = fmt.Sprintf("start %s %s: %s", st.id, slot, why)
	line := st.escalation
	st.mu.Unlock()
	s.eventLine(fmt.Sprintf("%s ESCALATE %s", time.Now().UTC().Format(time.RFC3339), line))
	s.writeStatus()
	s.saveState()
}

func (s *autoSupervisor) teardownOnce(slotDir string, releaseAnyway bool) (bool, string) {
	ctx, cancel := context.WithTimeout(context.Background(), autoTeardownTimeout)
	defer cancel()
	args := []string{"teardown", slotDir}
	if releaseAnyway {
		args = append(args, "--release-anyway")
	}
	out, err := exec.CommandContext(ctx, s.o.binary, args...).CombinedOutput()
	detail := strings.TrimSpace(string(out))
	if len(detail) > 300 {
		detail = detail[:297] + "..."
	}
	if err != nil {
		return false, fmt.Sprintf("teardown %s: %v: %s", slotDir, err, detail)
	}
	return true, fmt.Sprintf("teardown %s: %s", slotDir, detail)
}

// autoSlotJSON is the server, slot and slots path a slot dir's slot.json names.
type autoSlotJSON struct {
	Server    string `json:"server"`
	Slot      string `json:"slot"`
	SlotsPath string `json:"slots_path"`
}

func autoReadSlotJSON(slotDir string) (autoSlotJSON, error) {
	var m autoSlotJSON
	raw, err := os.ReadFile(filepath.Join(slotDir, "slot.json"))
	if err != nil {
		return m, fmt.Errorf("slot.json: %v", err)
	}
	if err := json.Unmarshal(raw, &m); err != nil || m.Server == "" || m.Slot == "" {
		return m, fmt.Errorf("slot.json does not name the server and the slot")
	}
	return m, nil
}

// leaseHeld asks the slots store whether the slot's lease is still there. An error means the lease state cannot be
// verified (no readable slot.json, a failed lease list, bad JSON): that is not "released".
func (s *autoSupervisor) leaseHeld(slotDir string) (bool, error) {
	m, err := autoReadSlotJSON(slotDir)
	if err != nil {
		return false, err
	}
	_, held, err := s.slotLease(m.Server, m.Slot, m.SlotsPath)
	if err != nil {
		return false, err
	}
	return held, nil
}

// slotLease reads the slot's lease from the slots store: the holder ("" when none) and whether it is held (a live
// lease, not a stale one). An error means the state cannot be verified.
func (s *autoSupervisor) slotLease(server, slot, slotsPath string) (holder string, held bool, err error) {
	args := []string{"lease", "list", server, "--json"}
	if slotsPath != "" {
		args = append(args, "--slots", slotsPath)
	}
	ctx, cancel := context.WithTimeout(context.Background(), autoLeaseTimeout)
	defer cancel()
	out, err := exec.CommandContext(ctx, s.o.binary, args...).Output()
	if err != nil {
		return "", false, fmt.Errorf("lease list %s: %v", server, err)
	}
	var list []struct {
		Slot  struct{ ID string } `json:"slot"`
		State string              `json:"state"`
		Lease *struct {
			Holder string `json:"holder"`
		} `json:"lease"`
	}
	if err := json.Unmarshal(out, &list); err != nil {
		return "", false, fmt.Errorf("lease list %s: %v", server, err)
	}
	for _, e := range list {
		if e.Slot.ID != slot {
			continue
		}
		if e.Lease != nil {
			holder = e.Lease.Holder
		}
		held = e.State == "held"
	}
	return holder, held, nil
}

// autoReadPID reads a pid file (0: missing or not a number).
func autoReadPID(path string) int {
	raw, err := os.ReadFile(path)
	if err != nil {
		return 0
	}
	n, err := strconv.Atoi(strings.TrimSpace(string(raw)))
	if err != nil || n <= 0 {
		return 0
	}
	return n
}

// autoSavedID is one id's checkpoint.
type autoSavedID struct {
	ID        string `json:"id"`
	Goal      int    `json:"goal"`
	GoalsDone int    `json:"goals_done"`
	LastStep  int    `json:"last_step"`
	Reuse     bool   `json:"reuse"`
	Attempt   int    `json:"attempt"`
	ChildPID  int    `json:"child_pid,omitempty"`
	OutDir    string `json:"out_dir,omitempty"`
	Done      bool   `json:"done,omitempty"`
	Escalated bool   `json:"escalated,omitempty"`
	NextAt    string `json:"next_at,omitempty"`
}

// autoSavedState is <walk-dir>/autopilot-state.json.
type autoSavedState struct {
	Format  string        `json:"format"`
	Version int           `json:"version"`
	Updated string        `json:"updated"`
	IDs     []autoSavedID `json:"ids"`
}

// saveState writes the checkpoint through a temp file and a rename (atomic: a crash leaves the previous file).
func (s *autoSupervisor) saveState() {
	s.stateMu.Lock()
	defer s.stateMu.Unlock()
	saved := autoSavedState{Format: autoStateFormat, Version: autoStateVersion, Updated: time.Now().UTC().Format(time.RFC3339)}
	for _, st := range s.states {
		st.mu.Lock()
		e := autoSavedID{
			ID: st.id, Goal: st.current, GoalsDone: st.current,
			Done: st.done, Escalated: st.escalated, Reuse: st.nextReuse,
		}
		if st.current < len(st.goals) {
			g := st.goals[st.current]
			e.Attempt, e.LastStep = g.attempt, g.bestStep
		}
		if st.last != nil {
			e.OutDir, e.Reuse = st.last.out, st.last.reuse
			if st.last.lastStep > e.LastStep {
				e.LastStep = st.last.lastStep
			}
		}
		if st.run != nil {
			e.OutDir, e.Reuse, e.ChildPID = st.run.out, st.run.reuse, st.run.pidN
			if st.run.lastStep > e.LastStep {
				e.LastStep = st.run.lastStep
			}
		}
		if !st.nextAt.IsZero() {
			e.NextAt = st.nextAt.UTC().Format(time.RFC3339)
		}
		st.mu.Unlock()
		saved.IDs = append(saved.IDs, e)
	}
	raw, err := json.MarshalIndent(saved, "", "  ")
	if err != nil {
		s.event("", "state", err.Error())
		return
	}
	path := filepath.Join(s.o.walkDir, autoStateFile)
	tmp := path + ".tmp"
	if err := writeFileSync(tmp, append(raw, '\n')); err != nil {
		s.event("", "state", err.Error())
		return
	}
	if err := os.Rename(tmp, path); err != nil {
		s.event("", "state", err.Error())
		return
	}
	// The rename is durable only once the directory entry is on disk: without this a WSL/VM crash can leave no
	// checkpoint (or the old one) although the write returned.
	if err := syncDir(s.o.walkDir); err != nil {
		s.event("", "state", err.Error())
	}
}

// writeFileSync writes a file, fsyncs it and closes it: the tmp file of an atomic replace must be on disk before the
// rename, or a crash can leave an empty or torn checkpoint.
func writeFileSync(path string, data []byte) error {
	f, err := os.OpenFile(path, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	if _, err := f.Write(data); err != nil {
		f.Close()
		return err
	}
	if err := f.Sync(); err != nil {
		f.Close()
		return err
	}
	return f.Close()
}

// syncDir fsyncs a directory, making a rename inside it durable.
func syncDir(dir string) error {
	d, err := os.Open(dir)
	if err != nil {
		return err
	}
	if err := d.Sync(); err != nil {
		d.Close()
		return err
	}
	return d.Close()
}

// loadState reads the checkpoint: a missing file is no state, and a corrupt or truncated one is an error (the start
// refuses instead of replacing it with empty progress, which would walk already-killed bosses again).
func (s *autoSupervisor) loadState() (autoSavedState, error) {
	var saved autoSavedState
	path := filepath.Join(s.o.walkDir, autoStateFile)
	raw, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return saved, nil
	}
	if err != nil {
		return saved, err
	}
	if err := json.Unmarshal(raw, &saved); err != nil || saved.Format != autoStateFormat {
		return autoSavedState{}, fmt.Errorf("%s is not a readable checkpoint: fix or remove it (a start would otherwise re-walk met goals)", path)
	}
	return saved, nil
}

// reconcile loads the checkpoint and makes it agree with what is on disk: a goal the last run met before a crash is
// advanced (not walked again), a live walk on the id's slot is adopted, and a slot that is not ours refuses the id.
// An unreadable checkpoint is returned as an error and is never overwritten.
func (s *autoSupervisor) reconcile() error {
	saved, err := s.loadState()
	if err != nil {
		return err
	}
	byID := map[string]*autoSavedID{}
	for i := range saved.IDs {
		byID[saved.IDs[i].ID] = &saved.IDs[i]
	}
	for _, st := range s.states {
		st.mu.Lock()
		t := st.goals[0].target
		sv := byID[st.id]
		if sv != nil {
			if sv.Goal >= 0 && sv.Goal < len(st.goals) {
				st.current = sv.Goal
				st.goals[st.current].attempt = sv.Attempt
				st.goals[st.current].bestStep = sv.LastStep
			}
			st.done, st.escalated = sv.Done, sv.Escalated
			st.nextReuse = sv.Reuse
			if at, err := time.Parse(time.RFC3339, sv.NextAt); err == nil {
				st.nextAt = at
			}
			if sv.OutDir != "" {
				st.lastOut = sv.OutDir
			}
			if sv.LastStep > 0 || sv.OutDir != "" {
				// The last attempt, as the checkpoint saw it: its step is where a restart starts. It is torn
				// down already (the checkpoint writes only after a verified teardown), so a YIELD must not tear
				// the stale slot dir down again.
				st.last = &autoRun{out: sv.OutDir, slot: t.Slot, reuse: sv.Reuse, lastStep: sv.LastStep, exited: true, adopted: true, pidN: sv.ChildPID, tornDown: true}
			}
		}
		done := st.done || st.escalated
		st.mu.Unlock()
		if done {
			continue
		}
		s.reconcileSlot(st, sv)
	}
	s.saveState()
	return nil
}

// reconcileSlot reconciles one id's slot: a goal the saved run met is advanced, a live walk that is ours is adopted,
// anything else on the slot refuses the id (H2: never launch on a slot with a live walk or a lease).
func (s *autoSupervisor) reconcileSlot(st *autoState, sv *autoSavedID) {
	st.mu.Lock()
	t := st.goals[st.current].target
	out := st.lastOut
	reuse := st.nextReuse
	if st.last != nil && st.last.out != "" {
		out = st.last.out
	}
	st.mu.Unlock()
	if sv != nil && sv.OutDir != "" {
		out = sv.OutDir
	}
	var slotsPath string
	if out != "" {
		if m, err := autoReadSlotJSON(filepath.Join(out, "slot")); err == nil {
			slotsPath = m.SlotsPath
		}
	}
	holder, held, err := s.slotLease(t.Server, t.Slot, slotsPath)
	if err != nil {
		s.refuse(st, "the slot's lease cannot be verified: "+err.Error())
		return
	}
	pid := 0
	if out != "" {
		pid = autoReadPID(filepath.Join(out, walkPIDFile))
	}
	live := pid > 0 && processAlive(pid)
	if !held && !live {
		// Nothing runs on the slot. A saved attempt whose summary shows the goal met is advanced instead of walked
		// again (a crash between the summary and the checkpoint).
		s.reconcileSummary(st, sv)
		return
	}
	if live && (holder == t.Holder || holder == "") && sv != nil {
		st.mu.Lock()
		r := &autoRun{
			seq: st.seq, out: out, slot: t.Slot, reuse: reuse, goal: t.Goal, pidN: pid, adopted: true,
			started: time.Now(), lastProgress: time.Now(), lastStep: sv.LastStep,
		}
		st.run = r
		st.last = nil
		st.mu.Unlock()
		s.event(st.id, "adopt", fmt.Sprintf("watching the live walk pid %d in %s (goal %s)", pid, out, t.Goal))
		return
	}
	why := fmt.Sprintf("the slot is not free: lease %s", autoLeaseWords(holder, held))
	if live {
		why = fmt.Sprintf("the slot is not free: lease %s, walk pid %d is live in %s", autoLeaseWords(holder, held), pid, out)
	}
	s.refuse(st, why)
}

func autoLeaseWords(holder string, held bool) string {
	switch {
	case held && holder != "":
		return "held by " + holder
	case held:
		return "held"
	case holder != "":
		return "stale (was " + holder + ")"
	}
	return "none"
}

// reconcileSummary advances a goal the saved attempt's summary says was met (the checkpoint lagged a crash).
func (s *autoSupervisor) reconcileSummary(st *autoState, sv *autoSavedID) {
	if sv == nil || sv.OutDir == "" || sv.LastStep == 0 && sv.Attempt == 0 {
		return
	}
	sum, err := autoReadSummary(filepath.Join(sv.OutDir, "summary.json"))
	if err != nil {
		return
	}
	o := raidwalk.AutoObservation{Exited: true, ExitCode: sum.ExitCode, StepsReached: sum.StepsReached, StepsTotal: sum.StepsTotal, EndReason: sum.EndReason, KilledBosses: map[uint32]bool{}}
	for _, b := range sum.BossKills {
		if b.Killed {
			o.KilledBosses[b.Entry] = true
		}
	}
	if sum.BossKill != nil && sum.BossKill.Killed {
		o.KilledBosses[sum.BossKill.Entry] = true
	}
	st.mu.Lock()
	if st.current >= len(st.goals) || !raidwalk.GoalAchieved(st.goals[st.current].target.Goal, o) {
		st.mu.Unlock()
		return
	}
	met := st.goals[st.current].target.Goal.String()
	if st.current+1 < len(st.goals) {
		st.current++
		st.last = nil
		st.nextReuse = true
		st.goals[st.current] = autoGoalState{target: st.goals[st.current].target}
		st.mu.Unlock()
		s.event(st.id, "reconcile", fmt.Sprintf("goal %s was met before the restart; next goal %s", met, st.goals[st.current].target.Goal))
		return
	}
	st.done = true
	st.mu.Unlock()
	s.event(st.id, "reconcile", fmt.Sprintf("goal %s was met before the restart; the last goal is done", met))
}

// shutdown tears every run down (YIELD or a signal) and writes YIELD-DONE for a YIELD, but only when every slot was
// verified clean: a failed teardown, or a slot escalated and left alone, keeps YIELD-DONE away. The stops and the
// teardowns run per id at the same time, so one slow slot never delays the others (M7).
func (s *autoSupervisor) shutdown(why string) {
	var wg sync.WaitGroup
	for _, st := range s.states {
		st.mu.Lock()
		r := st.run
		escalated, refused, done := st.escalated, st.refused, st.done
		st.mu.Unlock()
		if escalated {
			s.event(st.id, "escalated", "the slot is left alone: no teardown, no relaunch")
			continue
		}
		// A refused id never launched, and a done id's slot was torn down when its goal was met: neither has a run
		// of this supervisor to stop, and tearing their stale slot dir down would use --release-anyway on a slot
		// another agent may hold (M2).
		if refused || done || r == nil {
			continue
		}
		s.event(st.id, "shutdown", why)
		wg.Add(1)
		go func(st *autoState, r *autoRun) {
			defer wg.Done()
			s.stopRun(st, r)
		}(st, r)
	}
	wg.Wait()
	// A finish goroutine may hold a teardown; let it finish before the rest is torn down.
	s.wg.Wait()
	clean := true
	var mu sync.Mutex
	for _, st := range s.states {
		st.mu.Lock()
		target := st.run
		if target == nil {
			target = st.last
		}
		escalated, refused, done := st.escalated, st.refused, st.done
		st.mu.Unlock()
		if escalated {
			// An escalated slot is left alone: it is not clean, so no YIELD-DONE may claim the run ended.
			clean = false
			continue
		}
		// Only a run this supervisor still owns is torn down: a refused id never launched, and a done id's last
		// attempt was torn down when its goal was met (M2).
		if refused || done || target == nil {
			continue
		}
		wg.Add(1)
		go func(st *autoState, r *autoRun) {
			defer wg.Done()
			if err := s.teardown(st, r); err != nil {
				mu.Lock()
				clean = false
				mu.Unlock()
				s.event(st.id, "teardown-failed", err.Error())
			}
		}(st, target)
	}
	wg.Wait()
	if why != "YIELD" {
		return
	}
	if !clean {
		s.event("", "yield", "no YIELD-DONE: a teardown did not finish cleanly (the slot is left alone)")
		return
	}
	if err := os.WriteFile(filepath.Join(s.o.walkDir, autoYieldDone), nil, 0o600); err != nil {
		s.event("", "yield", "YIELD-DONE: "+err.Error())
		return
	}
	s.event("", "yield", "YIELD-DONE")
}

// writeStatus writes status.txt through a temp file and a rename.
func (s *autoSupervisor) writeStatus() {
	var b strings.Builder
	fmt.Fprintf(&b, "raid-autopilot %s\n", time.Now().UTC().Format(time.RFC3339))
	for _, st := range s.states {
		st.mu.Lock()
		g := "none"
		if st.current < len(st.goals) {
			g = st.goals[st.current].target.Goal.String()
		}
		switch {
		case st.escalated || st.refused:
			fmt.Fprintf(&b, "ESCALATE %s\n", st.escalation)
		case st.done:
			fmt.Fprintf(&b, "%s goal=%s done\n", st.id, g)
		case st.run == nil:
			fmt.Fprintf(&b, "%s goal=%s waiting for the next launch\n", st.id, g)
		default:
			r := st.run
			state := "running"
			if r.exited {
				state = fmt.Sprintf("exited %d", r.exitCode)
			}
			if r.adopted {
				state += " adopted"
			}
			if r.teardownFails > 0 {
				state += fmt.Sprintf(" teardown-failed=%d/%d", r.teardownFails, autoTeardownTries)
			}
			fmt.Fprintf(&b, "%s goal=%s attempt=%d out=%s %s step=%d progress=%s ago reuse=%v\n",
				st.id, g, st.goals[st.current].attempt, filepath.Base(r.out), state,
				r.lastStep, time.Since(r.lastProgress).Round(time.Second), r.reuse)
		}
		st.mu.Unlock()
	}
	path := filepath.Join(s.o.walkDir, autoStatusFile)
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, []byte(b.String()), 0o600); err == nil {
		_ = os.Rename(tmp, path)
	}
}
