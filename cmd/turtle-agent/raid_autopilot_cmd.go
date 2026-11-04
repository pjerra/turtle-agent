// raid-autopilot: keep the raids of a walk dir running without a worker watching them. The targets file names one
// goal per line; the autopilot launches raid-walk for each, watches the step and the bosses' HP, and on ANY end, a used
// -up restart budget or a stall tears the slot down and re-places: the same instance first (--reuse-instance), a fresh
// one only when the old bind is gone (a failed placement). A boss kill advances to the id's next goal. It never uses
// slot 3, never sends `.bot run stop` and never passes --instance-cap-unsafe; it runs detached (setsid) and stops
// cleanly on a YIELD file (tear down, release leases, YIELD-DONE). Walk 37 is the case set (EVENTS.txt, summary.md).
package main

import (
	"bufio"
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/pjerra/turtle-agent/internal/raidwalk"
	"github.com/pjerra/turtle-agent/internal/slots"
)

const raidAutopilotUsage = `usage: turtle-agent raid-autopilot [--foreground] [--binary PATH] [--poll D] [--stall D]
    [--settle D] [--status-every D] [--instance-cap-per-account N] [--dry-run] <walk-dir>`

// autoChildEnv marks the re-exec'd detached process.
const autoChildEnv = "TURTLE_RAID_AUTOPILOT_CHILD"

// The autopilot's files in the walk dir.
const (
	autoTargetsFile = "targets.txt"
	autoEventsFile  = "EVENTS.txt"
	autoStatusFile  = "status.txt"
	autoYieldFile   = "YIELD"
	autoYieldDone   = "YIELD-DONE"
	autoPIDFile     = "autopilot.pid"
	autoLogFile     = "autopilot.log"
)

const (
	autoDefaultPoll        = 10 * time.Second
	autoDefaultStall       = 15 * time.Minute
	autoDefaultSettle      = 20 * time.Second
	autoDefaultStatusEvery = time.Minute
	autoKillGrace          = 60 * time.Second
	autoTeardownTimeout    = 3 * time.Minute
	// autoSummaryGrace is how long an exited run may take to write summary.json before the autopilot acts without it.
	autoSummaryGrace = 10 * time.Second
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
	// flag was given (otherwise raid-walk resolves slots.json's per-server value and then the stock 5).
	instanceCapPerAccount int
	instanceCapSet        bool
	dryRun                bool
}

// autoGoalState is one of an id's goal lines.
type autoGoalState struct {
	target  raidwalk.AutoTarget
	attempt int // attempts of this goal so far
}

// autoRun is one live or finished raid-walk attempt.
type autoRun struct {
	seq     int
	out     string
	reuse   bool
	goal    raidwalk.AutoGoal
	cmd     *exec.Cmd
	wait    chan error
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
	fastFails    int
	tornDown     bool
}

// autoState is one raid id and its place in its goal lines.
type autoState struct {
	id        string
	goals     []autoGoalState
	current   int
	seq       int
	run       *autoRun
	last      *autoRun
	nextReuse bool
	nextAt    time.Time // no launch before this (the settle after a teardown, or a backoff)
	done      bool
}

// autoSupervisor owns the processes and files.
type autoSupervisor struct {
	o        autoOptions
	states   []*autoState
	events   *os.File
	stderr   io.Writer
	lastStat time.Time
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
	if code := autoPIDGuard(o.walkDir, stderr); code != exitOK {
		return code
	}
	pidPath := filepath.Join(o.walkDir, autoPIDFile)
	if err := os.WriteFile(pidPath, []byte(strconv.Itoa(os.Getpid())+"\n"), 0o600); err != nil {
		fmt.Fprintf(stderr, "raid-autopilot: pid file: %v\n", err)
		return exitUsage
	}
	defer os.Remove(pidPath)

	s := &autoSupervisor{o: o, stderr: stderr}
	if err := s.open(); err != nil {
		fmt.Fprintf(stderr, "raid-autopilot: %v\n", err)
		return exitUsage
	}
	defer s.close()
	s.states = groupAutoTargets(targets)
	s.initSeq()
	for _, st := range s.states {
		s.event(st.id, "supervise", fmt.Sprintf("goal %s (%d goal line(s))", st.goals[0].target.Goal, len(st.goals)))
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
	fs.IntVar(&o.instanceCapPerAccount, "instance-cap-per-account", raidwalk.InstanceCapPerAccount, "the core's limit of fresh instance ids one account may enter in an hour, passed to every raid-walk (stock 5; without the flag each walk uses slots.json's instance_cap_per_account)")
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
// resolves slots.json's per-server value itself).
func (o autoOptions) forwardInstanceCap() int {
	if o.instanceCapSet {
		return o.instanceCapPerAccount
	}
	return 0
}

// autoCommandArgs is the raid-walk command for one attempt. from is the start the attempt asks for (a target's own
// --from, or the furthest step a restart reached); reuse asks for the same instance first; instanceCap > 0 is passed
// on as --instance-cap-per-account.
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
	if reuse {
		args = append(args, "--reuse-instance")
	}
	if instanceCap > 0 {
		args = append(args, "--instance-cap-per-account", strconv.Itoa(instanceCap))
	}
	return args
}

// initSeq continues the out numbering after the dirs an earlier autopilot made: the walk refuses an existing --out,
// and a relaunch must not think every attempt is the first.
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
			if err == nil && n > st.seq {
				st.seq = n
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
	line := fmt.Sprintf("%s %s %s: %s", time.Now().UTC().Format(time.RFC3339), id, action, detail)
	if s.events != nil {
		fmt.Fprintln(s.events, line)
	}
	fmt.Fprintln(s.stderr, "raid-autopilot: "+line)
}

func autoPIDGuard(dir string, stderr io.Writer) int {
	raw, err := os.ReadFile(filepath.Join(dir, autoPIDFile))
	if err != nil {
		return exitOK
	}
	pid, err := strconv.Atoi(strings.TrimSpace(string(raw)))
	if err != nil || pid <= 0 || pid == os.Getpid() {
		return exitOK
	}
	if err := syscall.Kill(pid, 0); err == nil || err == syscall.EPERM {
		fmt.Fprintf(stderr, "raid-autopilot: another autopilot runs in %s (pid %d)\n", dir, pid)
		return exitUsage
	}
	return exitOK
}

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
	fmt.Fprintf(stdout, "raid-autopilot: pid %d, log %s\n", cmd.Process.Pid, logPath)
	return exitOK
}

// run is the loop: YIELD, observe, decide, act, status every statusEvery.
func (s *autoSupervisor) run(ctx context.Context) int {
	ticker := time.NewTicker(s.o.poll)
	defer ticker.Stop()
	if _, err := os.Stat(filepath.Join(s.o.walkDir, autoYieldFile)); err == nil {
		s.shutdown("YIELD")
		return exitOK
	}
	s.tick(ctx)
	for {
		select {
		case <-ctx.Done():
			s.shutdown("signal")
			return exitOK
		case <-ticker.C:
		}
		if _, err := os.Stat(filepath.Join(s.o.walkDir, autoYieldFile)); err == nil {
			s.shutdown("YIELD")
			return exitOK
		}
		s.tick(ctx)
		if time.Since(s.lastStat) >= s.o.statusEvery {
			s.writeStatus()
			s.lastStat = time.Now()
		}
		if s.allDone() {
			s.event("all", "done", "every target's goals are met")
			return exitOK
		}
	}
}

func (s *autoSupervisor) allDone() bool {
	for _, st := range s.states {
		if !st.done {
			return false
		}
	}
	return true
}

// tick advances every id once.
func (s *autoSupervisor) tick(ctx context.Context) {
	for _, st := range s.states {
		if st.done {
			continue
		}
		if st.run == nil {
			if time.Now().Before(st.nextAt) {
				continue
			}
			s.launch(st)
			continue
		}
		s.pollRun(st)
		if st.run == nil {
			continue
		}
		obs := s.observe(st.run)
		if obs.Exited && obs.ObserveErr != "" && time.Since(st.run.exitedAt) < autoSummaryGrace {
			continue
		}
		d := raidwalk.DecideAutoAction(obs, raidwalk.AutoRunState{
			Goal: st.run.goal, Attempt: st.goals[st.current].attempt, Reuse: st.run.reuse,
			HasRun: st.goals[st.current].attempt > 0, FastFailures: st.run.fastFails,
		}, time.Now(), s.o.stall)
		switch d.Action {
		case raidwalk.AutoContinue:
			continue
		case raidwalk.AutoAdvance:
			s.event(st.id, "advance", d.Reason)
			s.finish(st, true)
			st.nextAt = time.Now().Add(s.o.settle)
		case raidwalk.AutoStop:
			s.event(st.id, "stop", d.Reason)
			s.finish(st, false)
			st.done = true
		case raidwalk.AutoRestart:
			s.event(st.id, "restart", fmt.Sprintf("%s; next %s", d.Reason, s.nextPlacement(d.Reuse)))
			s.finish(st, false)
			if st.last != nil {
				st.last.reuse = d.Reuse
			}
			wait := s.o.settle
			if d.Backoff > wait {
				wait = d.Backoff
				s.event(st.id, "backoff", d.Backoff.String())
			}
			st.nextAt = time.Now().Add(wait)
		}
	}
}

func (s *autoSupervisor) nextPlacement(reuse bool) string {
	if reuse {
		return "same instance (--reuse-instance) first"
	}
	return "fresh instance"
}

// launch starts the current goal's next attempt from its own start (first attempt) or the last run's furthest step.
func (s *autoSupervisor) launch(st *autoState) {
	if st.current >= len(st.goals) {
		st.done = true
		s.event(st.id, "done", "no goal line left")
		return
	}
	g := &st.goals[st.current]
	from := g.target.Start
	reuse := st.nextReuse
	if st.last != nil {
		reuse = st.last.reuse
		if st.last.lastStep > 0 {
			from = fmt.Sprintf("step %d", st.last.lastStep)
		}
	}
	st.nextReuse = false
	g.attempt++
	s.launchAttempt(st, g, from, reuse)
}

func (s *autoSupervisor) launchAttempt(st *autoState, g *autoGoalState, from string, reuse bool) {
	st.seq++
	out := filepath.Join(s.o.walkDir, fmt.Sprintf("%s-%d", st.id, st.seq))
	args := autoCommandArgs(s.o.binary, g.target, out, from, reuse, s.o.forwardInstanceCap())
	f, err := os.OpenFile(out+".out", os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		s.event(st.id, "launch-failed", err.Error())
		st.done = true
		return
	}
	cmd := exec.Command(s.o.binary, args...)
	cmd.Stdout, cmd.Stderr = f, f
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	if err := cmd.Start(); err != nil {
		f.Close()
		s.event(st.id, "launch-failed", err.Error())
		st.done = true
		return
	}
	run := &autoRun{
		seq: st.seq, out: out, reuse: reuse, goal: g.target.Goal, cmd: cmd,
		wait: make(chan error, 1), started: time.Now(), lastProgress: time.Now(),
	}
	if st.last != nil {
		run.fastFails = st.last.fastFails
	}
	go func() {
		err := cmd.Wait()
		// cmd.Wait does not close a parent-supplied *os.File; the supervisor restarts indefinitely, so every attempt
		// must close its log or it runs out of descriptors.
		f.Close()
		run.wait <- err
		close(run.wait)
	}()
	st.run = run
	s.event(st.id, "launch", fmt.Sprintf("attempt %d goal %s out %s from %q reuse %v", g.attempt, g.target.Goal, out, from, reuse))
}

// pollRun reads the child's exit and its progress.
func (s *autoSupervisor) pollRun(st *autoState) {
	r := st.run
	select {
	case _, ok := <-r.wait:
		if !r.exited {
			r.exited = true
			r.exitedAt = time.Now()
			if r.cmd.ProcessState != nil {
				r.exitCode = r.cmd.ProcessState.ExitCode()
			}
		}
		_ = ok
	default:
	}
	if r.exited {
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

// observe builds the decision input from the run's files.
func (s *autoSupervisor) observe(r *autoRun) raidwalk.AutoObservation {
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

// autoCapFreeTime reads the last "earliest free time" the walk printed while it waits on the instance cap: the run is
// legitimately idle until then, so the stall rule must not fire before it.
func autoCapFreeTime(path string) time.Time {
	raw := autoTail(path)
	var at time.Time
	sc := bufio.NewScanner(strings.NewReader(raw))
	for sc.Scan() {
		line := sc.Text()
		i := strings.LastIndex(line, "earliest free time ")
		if i < 0 {
			continue
		}
		word := line[i+len("earliest free time "):]
		if j := strings.IndexByte(word, ' '); j >= 0 {
			word = word[:j]
		}
		if t, err := time.Parse(time.RFC3339, strings.TrimRight(strings.TrimSpace(word), "),.;")); err == nil {
			at = t
		}
	}
	return at
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

// finish tears the run's slot down and advances the goal (advance) or leaves the next attempt to the loop.
func (s *autoSupervisor) finish(st *autoState, advance bool) {
	r := st.run
	if r == nil {
		return
	}
	s.stopRun(st, r)
	// An attempt that ended before the poll ran still has a step in its log (and a restart must start there, not at
	// the goal's own start again).
	if r.lastStep == 0 {
		r.lastStep = autoLastStep(filepath.Join(r.out, "run.log"))
		if r.lastStep == 0 {
			if sum, err := autoReadSummary(filepath.Join(r.out, "summary.json")); err == nil {
				r.lastStep = sum.StepsReached
			}
		}
	}
	s.teardown(st.id, r)
	st.run = nil
	st.last = r
	// A fast failure (under two minutes, no step) doubles the next backoff.
	if time.Since(r.started) < 2*time.Minute && r.lastStep == 0 {
		r.fastFails++
	} else {
		r.fastFails = 0
	}
	if advance {
		if st.current+1 < len(st.goals) {
			st.current++
			st.last = nil
			st.nextReuse = true
			st.goals[st.current].attempt = 0
			s.event(st.id, "goal", "next: "+st.goals[st.current].target.Goal.String())
		} else {
			st.done = true
			s.event(st.id, "done", "the last goal is met")
		}
		return
	}
}

// stopRun ends the child gracefully: SIGTERM the walk's process group (its own signal handler stops the run and tears
// down), then SIGKILL after the grace. The autopilot never sends `.bot run stop`.
func (s *autoSupervisor) stopRun(st *autoState, r *autoRun) {
	if r.cmd == nil || r.cmd.Process == nil || r.exited {
		return
	}
	pid := r.cmd.Process.Pid
	_ = syscall.Kill(-pid, syscall.SIGTERM)
	select {
	case <-r.wait:
		r.exited = true
		if r.cmd.ProcessState != nil {
			r.exitCode = r.cmd.ProcessState.ExitCode()
		}
		return
	case <-time.After(autoKillGrace):
	}
	s.event(st.id, "kill", fmt.Sprintf("pid %d did not stop in %s; SIGKILL", pid, autoKillGrace))
	_ = syscall.Kill(-pid, syscall.SIGKILL)
	<-r.wait
	r.exited = true
}

// teardown runs the slot teardown once; when the lease is still held or the teardown failed, it runs again with
// --release-anyway (the brief's "twice if the lease is kept").
func (s *autoSupervisor) teardown(id string, r *autoRun) {
	if r.tornDown {
		return
	}
	r.tornDown = true
	slotDir := filepath.Join(r.out, "slot")
	if _, err := os.Stat(slotDir); err != nil {
		return
	}
	ok, detail := s.teardownOnce(slotDir, false)
	if ok && !s.leaseHeld(slotDir) {
		s.event(id, "teardown", detail)
		return
	}
	s.event(id, "teardown", detail+"; the lease is kept or the teardown failed, trying again with --release-anyway")
	ok, detail = s.teardownOnce(slotDir, true)
	verb := "teardown"
	if !ok {
		verb = "teardown-failed"
	}
	s.event(id, verb, detail)
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

// leaseHeld asks the slots store whether the slot's lease is still there.
func (s *autoSupervisor) leaseHeld(slotDir string) bool {
	raw, err := os.ReadFile(filepath.Join(slotDir, "slot.json"))
	if err != nil {
		return false
	}
	var m struct {
		Server    string `json:"server"`
		Slot      string `json:"slot"`
		SlotsPath string `json:"slots_path"`
	}
	if json.Unmarshal(raw, &m) != nil || m.Server == "" || m.Slot == "" {
		return false
	}
	args := []string{"lease", "list", m.Server, "--json"}
	if m.SlotsPath != "" {
		args = append(args, "--slots", m.SlotsPath)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	out, err := exec.CommandContext(ctx, s.o.binary, args...).Output()
	if err != nil {
		return false
	}
	var list []struct {
		Slot  struct{ ID string } `json:"slot"`
		Lease *json.RawMessage    `json:"lease"`
	}
	if json.Unmarshal(out, &list) != nil {
		return false
	}
	for _, e := range list {
		if e.Slot.ID == m.Slot && e.Lease != nil {
			return true
		}
	}
	return false
}

// shutdown tears every run down (YIELD or a signal) and writes YIELD-DONE for a YIELD.
func (s *autoSupervisor) shutdown(why string) {
	for _, st := range s.states {
		switch {
		case st.run != nil:
			s.stopRun(st, st.run)
			s.teardown(st.id, st.run)
			st.run = nil
		case st.last != nil:
			s.teardown(st.id, st.last)
		}
		s.event(st.id, "shutdown", why)
	}
	if why != "YIELD" {
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
		g := "none"
		if st.current < len(st.goals) {
			g = st.goals[st.current].target.Goal.String()
		}
		if st.done {
			fmt.Fprintf(&b, "%s goal=%s done\n", st.id, g)
			continue
		}
		if st.run == nil {
			fmt.Fprintf(&b, "%s goal=%s waiting for the next launch\n", st.id, g)
			continue
		}
		r := st.run
		state := "running"
		if r.exited {
			state = fmt.Sprintf("exited %d", r.exitCode)
		}
		fmt.Fprintf(&b, "%s goal=%s attempt=%d out=%s %s step=%d progress=%s ago reuse=%v\n",
			st.id, g, st.goals[st.current].attempt, filepath.Base(r.out), state,
			autoLastStep(filepath.Join(r.out, "run.log")), time.Since(r.lastProgress).Round(time.Second), r.reuse)
	}
	path := filepath.Join(s.o.walkDir, autoStatusFile)
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, []byte(b.String()), 0o600); err == nil {
		_ = os.Rename(tmp, path)
	}
}
