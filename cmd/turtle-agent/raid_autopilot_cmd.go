// raid-autopilot: keep the raids of a walk dir running without a worker watching them. The targets file names one
// goal per line; the autopilot launches raid-walk for each, watches the step and the bosses' HP, and on ANY end, a used
// -up restart budget or a stall tears the slot down and re-places: the same instance first (--place --from
// --reuse-instance), a fresh one only when the old bind is gone (a failed placement) or after an exit 29 (once), and at
// once after a wipe at a boss for a --gm-instance-data goal or a --boss-wipe-fresh goal (up to the goal's --boss-wipe-max, default 3). An exit 26
// (no fresh instance place) relaunches a fresh instance at once: raid-walk waited out its own --instance-cap-wait
// inside the run, so the autopilot has no second wait, and the fourth refusal in a row escalates the id for a person.
// A boss kill advances to the id's next goal. It never uses slot 3, never sends `.bot run stop` and never passes --instance-cap-unsafe or --inject; it
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
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
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
	// hireWaits counts the consecutive hire-pool waits of this goal: the sixth stops the id for a person (a pool that
	// refuses six times in a row needs a person, not another 5-minute wait).
	hireWaits int
	// exit29Retries counts the fresh-instance retries after an exit 29 this goal already made (one per goal; a new goal
	// line starts a new goalState, so an advance resets it).
	exit29Retries int
	// bossWipes counts the fresh-instance relaunches after a wipe at a boss since this goal last made progress (a
	// step beyond bestStep); a goal advance starts a new goalState.
	bossWipes int
	// freshPending is the pending fresh-after-wipe mode (--boss-wipe-fresh): set when a FreshAfterWipe restart is
	// decided and cleared only when a launch gets past placement. While set, every launch uses the goal's --from
	// in a fresh instance, across exit-26 retries, other pre-placement failures and checkpoint save/restore.
	freshPending bool
	// dead says an attempt's summary reported this goal's boss dead: the goal is met and DONE, never placed again
	// (walk 39: a re-added raid kept walking a boss another raid had already killed).
	dead bool
	// practiceCycles is the practice goal's summed cycles over its finished runs: the goal is met when the sum reaches
	// the target, across relaunches. A practice walk that dies (exit 18, a boss death) restarts with what it collected.
	practiceCycles int
	// practiceRelaunches counts this practice goal's exit-18 relaunches (the third exit 18 gives up); the checkpoint
	// keeps it across a supervisor restart.
	practiceRelaunches int
	// practiceKilledRetry says the practice goal already took its one fresh-instance relaunch after the boss died
	// during the practice before the cycle target.
	practiceKilledRetry bool
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
	// placed says the run got past the place phase (it placed and started walking, or ended after a placement): a
	// cap refusal and a place-phase end are neither, and only a placement breaks an id's streak of cap refusals.
	placed   bool
	frameOff int64
	frameSig string
	// practiceCycles is the last observation's practice cycles (this run's own): finish adds them to the goal's sum.
	practiceCycles int
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
	// capWaits counts the consecutive instance-cap refusals (exit 26) of this id: the fourth escalates. A launch that
	// got past placement breaks the streak.
	capWaits int
	nextAt   time.Time // no launch before this (the settle after a teardown, or a backoff)
	done     bool
	// escalated is an id left for a person (a teardown that never verified, an exit 2, a second exit 29, three exit 15s): it counts
	// as done for allDone, is never launched again and is kept in the checkpoint until a person clears it.
	escalated bool
	// refused is this start's refusal (H2: the slot is not ours). It counts as done for this run but is not kept in
	// the checkpoint: the next start re-reads the slot and decides again.
	refused bool
	// escalation is the "kind id slot: detail" tail status.txt prints after ESCALATE.
	escalation string
	// stopping says a STOP-<id> file (or a removed target line) asked this id to stop: its run gets an AutoStop
	// finish (the slot is torn down as a YIELD would) and it is never launched again.
	stopping bool
	stopWhy  string
	// stopped says a STOP-<id> file (or a removed line) ended this id: it is done, but not because its goals were met.
	stopped bool
	// remove says the id's targets.txt lines are gone (or changed): once the stop is done the state is dropped.
	remove bool
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
	// statesMu guards states and pending: targets.txt is re-read while the loop runs, so ids come and go.
	statesMu sync.Mutex
	// pending holds the states of target lines added (or changed) by a re-read: they are adopted once the old state
	// of the same id is gone (its slot torn down).
	pending []*autoState
	// stopMu guards stopNoted and waitNoted: the tick goroutine reads the STOP files and adopts the pending lines,
	// while a finish goroutine consumes the file of a completed stop.
	// stopNoted is the refused STOP files already logged: the person's file stays, so a tick must not log it again.
	// waitNoted is the ids whose new lines were logged once as waiting for their old run or lease.
	stopMu    sync.Mutex
	stopNoted map[string]bool
	waitNoted map[string]bool
	// targetsMod/targetsSize are the targets.txt the loaded targets came from; targetsWarn is the last read error
	// (logged once, and the loaded targets are kept meanwhile).
	targetsMod  time.Time
	targetsSize int64
	targetsWarn string
	// targetsReads counts the parses of targets.txt (a test pins that a skipped line is not re-read while idle).
	targetsReads int
	// targetsSkipped says the last read skipped a line; targetsSig is the ids' claims at that read (see reloadTargets).
	targetsSkipped bool
	targetsSig     string
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
	s.noteTargetsStat()
	s.initSeq()
	// A walk whose worker died (a laptop restart, walk 47) leaves its lease behind: free it before the reconciliation
	// reads the slots, so a dead holder cannot refuse an id. Leases of another host stay until their expiry.
	s.reapLeases(targets)
	// The resume rows are read before each launch, outside the id's state mutex (preReadRows): an id a targets
	// re-read adds has no cache entry yet, and a YIELD stops every id through that mutex.
	// A YIELD-DONE from an earlier YIELD is stale at this start (M6): this run writes its own only if a YIELD comes.
	_ = os.Remove(filepath.Join(o.walkDir, autoYieldDone))
	if err := s.reconcile(); err != nil {
		fmt.Fprintf(stderr, "raid-autopilot: %v\n", err)
		return exitUsage
	}
	s.writeStatus()
	for _, st := range s.stateList() {
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

// readAutoTargets is the strict read of a start: any bad line refuses it. The slot check goes by the checkpoint like
// the live re-read does: an id the checkpoint holds as done, stopped, or escalated with no live child holds no slot (so
// a file the running autopilot accepted, a done id plus a new id on its slot, is accepted after a restart too), and so
// does an id whose last goal's boss is dead (reconcile never places it again); every other checkpointed id claims its
// slot before a new line does. An escalated id whose checkpoint's child still answers keeps its slot: the escalation
// left that run in place, and a new line on the slot would launch beside a live walk. An id whose goal lines changed
// since the checkpoint is not restored (reconcile starts it fresh), so it is a new id here.
func readAutoTargets(dir string) ([]raidwalk.AutoTarget, error) {
	read := func(claim func(string) raidwalk.AutoSlotClaim) ([]raidwalk.AutoTarget, []raidwalk.AutoTargetIssue, error) {
		f, err := os.Open(filepath.Join(dir, autoTargetsFile))
		if err != nil {
			return nil, nil, err
		}
		defer f.Close()
		return raidwalk.ParseAutoTargetsLenient(f, claim)
	}
	saved, _ := autoLoadCheckpoint(dir) // an unreadable checkpoint is reconcile's refusal, not this read's
	kinds := map[string]raidwalk.AutoSlotClaim{}
	byID := map[string]autoSavedID{}
	for _, sv := range saved.IDs {
		kinds[sv.ID] = raidwalk.AutoSlotActive
		if sv.Done || sv.Stopped || (sv.Escalated && !savedChildAlive(sv)) {
			// An escalated id holds its slot while the walk it escalated on still answers (the same pid check
			// reconcile makes): the escalation left the run in place, and a new line on that slot would launch beside
			// a live walk. Once the child is gone the slot is free.
			kinds[sv.ID] = raidwalk.AutoSlotInactive
		}
		byID[sv.ID] = sv
	}
	claim := func(id string) raidwalk.AutoSlotClaim { return kinds[id] }
	targets, issues, err := read(claim)
	if err == nil && len(kinds) > 0 {
		changed := false
		for _, st := range groupAutoTargets(targets) {
			sv, ok := byID[st.id]
			if !ok || kinds[st.id] == raidwalk.AutoSlotNew {
				continue
			}
			switch {
			case sv.Fingerprint != "" && sv.Fingerprint != autoTargetFingerprint(st):
				delete(kinds, st.id)
				changed = true
			case kinds[st.id] == raidwalk.AutoSlotActive && sv.BossDead && sv.Goal == len(st.goals)-1:
				// The last goal's boss is dead (review round 3): reconcile never places it again, so it holds no slot,
				// as a done id does.
				kinds[st.id] = raidwalk.AutoSlotInactive
				changed = true
			}
		}
		if changed {
			targets, issues, err = read(claim)
		}
	}
	if err != nil {
		return nil, err
	}
	if len(issues) > 0 {
		return nil, issues[0].Err
	}
	return targets, nil
}

// savedChildAlive says the walk a checkpointed id last launched still answers: its pid or a process of its group, the
// same check reconcile's slot decision makes. A checkpoint from before ChildPID existed says no, as a released run does.
func savedChildAlive(sv autoSavedID) bool { return processAlive(sv.ChildPID) }

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

// autoTargetFingerprint is the sha256 of a state's ordered, normalised goal lines: id, server, slot, holder, goal,
// start and the raid-walk args, one line per goal (the line numbers of targets.txt are not part of it). The
// checkpoint stores it per id, so a start can tell whether a saved goal and step belongs to the lines targets.txt
// holds now: a person may edit the goals under a crashed supervisor, and a checkpoint written for the old lines must
// not mark the new ones done or place them from an old step.
func autoTargetFingerprint(st *autoState) string {
	var b strings.Builder
	for _, g := range st.goals {
		t := g.target
		fmt.Fprintf(&b, "%q %q %q %q %q %q", t.ID, t.Server, t.Slot, t.Holder, t.Goal.String(), t.Start)
		for _, a := range t.Args {
			fmt.Fprintf(&b, " %q", a)
		}
		b.WriteByte('\n')
	}
	sum := sha256.Sum256([]byte(b.String()))
	return hex.EncodeToString(sum[:])
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
	if t.Goal.Kind == "practice" {
		// The practice loop ends the walk itself: the run carries the mechanic entry and its cycle budget.
		args = append(args, "--practice", strconv.FormatUint(uint64(t.Goal.Boss), 10),
			"--practice-cycles", strconv.Itoa(t.Goal.Cycles))
	}
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

// autoHasGMInstanceData says the target line carries --gm-instance-data: the goals that re-apply the boss
// prerequisites in a fresh copy (ZG Hakkar today), the only ones a boss wipe may relaunch in a fresh instance.
func autoHasGMInstanceData(args []string) bool {
	for _, a := range args {
		if a == "--gm-instance-data" || strings.HasPrefix(a, "--gm-instance-data=") {
			return true
		}
	}
	return false
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

// autoReadRows reads the rows a target's raid-walk places against: the plan in its --sql migration. The plan file is
// not a fallback: its export numbers the steps differently (ZG: 401 rows against the deployed 733), and a resume step
// adjusted against those numbers would place the raid somewhere else. ok is false when the migration cannot be read
// or does not hold the plan, and the resume then keeps the step it had.
func autoReadRows(t raidwalk.AutoTarget) (planexport.Rows, bool) {
	var plan string
	if len(t.Args) > 0 {
		plan = t.Args[0]
	}
	sql := autoFlag(t.Args, "--sql")
	if sql == "" {
		return planexport.Rows{}, false
	}
	raw, err := os.ReadFile(slots.ExpandTilde(sql))
	if err != nil {
		return planexport.Rows{}, false
	}
	plans, _, err := planexport.ParseMigration(string(raw))
	if err != nil {
		return planexport.Rows{}, false
	}
	for _, r := range plans {
		if r.Plan.Plan == plan {
			return r, true
		}
	}
	return planexport.Rows{}, false
}

// targetRows is autoReadRows once per plan + --dir + --sql. The cache key carries the migration's mtime and size, so a
// redeployed migration is read again instead of keeping the rows of the file it replaced (the resume step is then
// adjusted against the rows a walk now places against). A --sql that cannot be stat'ed is not cached: it may appear.
func (s *autoSupervisor) targetRows(t raidwalk.AutoTarget) (planexport.Rows, bool) {
	sql := autoFlag(t.Args, "--sql")
	path := slots.ExpandTilde(sql)
	key := t.Args[0] + "\x00" + autoFlag(t.Args, "--dir") + "\x00" + sql
	if path == "" {
		return autoReadRows(t)
	}
	st, err := os.Stat(path)
	if err != nil {
		return autoReadRows(t)
	}
	key += fmt.Sprintf("\x00%s\x00%d", st.ModTime().UTC().Format(time.RFC3339Nano), st.Size())
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

// preReadRows reads one id's rows before its launch, outside the id's state mutex: an id added or changed by a
// targets re-read has no cache entry, and parsing its migration while st.mu is held would keep a YIELD's stop of that
// id waiting on the parse.
func (s *autoSupervisor) preReadRows(st *autoState) {
	st.mu.Lock()
	var t raidwalk.AutoTarget
	if st.current < len(st.goals) {
		t = st.goals[st.current].target
	}
	st.mu.Unlock()
	if t.ID != "" {
		s.targetRows(t)
	}
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

// autoGoalResumeStart is the goal's own --from when the reached step is its boss row or one of the boss's approach
// rows (raidwalk.PlaceResumeGoalStart); "" for any other step, so the caller resumes from the reached step.
func (s *autoSupervisor) autoGoalResumeStart(t raidwalk.AutoTarget, step int) string {
	if step < 1 || t.Start == "" {
		return ""
	}
	rows, ok := s.targetRows(t)
	if !ok {
		return ""
	}
	words, _ := raidwalk.PlaceResumeGoalStart(rows, step, t.Start)
	return words
}

// initSeq continues the out numbering after the dirs an earlier autopilot made: the walk refuses an existing --out,
// and a relaunch must not think every attempt is the first. It also remembers each id's latest out dir.
func (s *autoSupervisor) initSeq() {
	entries, err := os.ReadDir(s.o.walkDir)
	if err != nil {
		return
	}
	byID := map[string]*autoState{}
	for _, st := range s.stateList() {
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
	s.statesMu.Lock()
	pending := len(s.pending)
	s.statesMu.Unlock()
	if pending > 0 {
		return false
	}
	for _, st := range s.stateList() {
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
	for _, st := range s.stateList() {
		st.mu.Lock()
		met := st.done && !st.stopped
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
	escalated, refused, stopped := 0, 0, 0
	for _, st := range s.stateList() {
		st.mu.Lock()
		if st.escalated {
			escalated++
		}
		if st.refused {
			refused++
		}
		if st.stopped {
			stopped++
		}
		st.mu.Unlock()
	}
	counts := fmt.Sprintf("%d escalated, %d refused", escalated, refused)
	if stopped > 0 {
		counts += fmt.Sprintf(", %d stopped", stopped)
	}
	s.event("all", "finished", "not every target's goals are met ("+counts+")")
	if escalated > 0 {
		return exitAutoEscalated
	}
	return exitOK
}

// stateList is a snapshot of the ids: the loop, the checkpoint and the status writer must not walk the slice while a
// targets re-read adds or drops an id.
func (s *autoSupervisor) stateList() []*autoState {
	s.statesMu.Lock()
	defer s.statesMu.Unlock()
	return append([]*autoState(nil), s.states...)
}

// addState appends one id (under statesMu).
func (s *autoSupervisor) addState(st *autoState) {
	s.statesMu.Lock()
	s.states = append(s.states, st)
	s.statesMu.Unlock()
}

// dropRemoved removes the states whose targets.txt lines are gone and whose stop has finished. Only an idle, done
// state goes: an escalated or refused id stays in status.txt (and in finishAll's exit code and shutdown's slot check),
// and so does a state whose slot is still leased — dropping it would lose the ESCALATE line and let a YIELD claim a
// clean end over a slot that is not ours. A stopped id of a STOP file is kept too (it owns a done line in status.txt);
// a removed line is not.
func (s *autoSupervisor) dropRemoved() {
	ids := s.stateList()
	kept := ids[:0]
	for _, st := range ids {
		st.mu.Lock()
		drop := st.remove && st.done && !st.escalated && !st.refused && st.run == nil
		st.mu.Unlock()
		if drop && !s.stateSlotHeld(st) {
			continue
		}
		kept = append(kept, st)
	}
	s.statesMu.Lock()
	// Only the tick goroutine adds states (adoptPending), so the snapshot is the whole list.
	s.states = kept
	s.statesMu.Unlock()
}

// stateSlotHeld says the state's slot dir is still leased. true when it cannot be verified either: a removed state is
// never dropped on a lease whose state is unknown (its slot may be in use by a walk we no longer watch).
func (s *autoSupervisor) stateSlotHeld(st *autoState) bool {
	st.mu.Lock()
	out := st.lastOut
	if st.last != nil && st.last.out != "" {
		out = st.last.out
	}
	if st.run != nil && st.run.out != "" {
		out = st.run.out
	}
	st.mu.Unlock()
	if out == "" {
		return false
	}
	slotDir := filepath.Join(out, "slot")
	if _, err := os.Stat(slotDir); errors.Is(err, os.ErrNotExist) {
		return false
	}
	held, err := s.leaseHeld(slotDir)
	if err != nil {
		return true
	}
	return held
}

// noteTargetsStat remembers the targets.txt the loaded targets came from.
func (s *autoSupervisor) noteTargetsStat() {
	st, err := os.Stat(filepath.Join(s.o.walkDir, autoTargetsFile))
	if err != nil {
		return
	}
	s.targetsMod, s.targetsSize = st.ModTime(), st.Size()
}

// slotClaims classifies every known id for the duplicate-slot check of a re-read: done, stopped or escalated with no
// live run is inactive whoever holds its slot's lease now (walk 40: a finished id kept its slot, and a new id running
// on it must not make it active again; an escalated id kept its slot until its line was edited, so the next goal could
// not start there); every other state, including a queued id, is active. held is the slot each live state
// holds now (its current goal's server and slot): that slot stays the id's in the check even when the id's own lines
// are skipped or changed (review round 3). sig is the claims and holds as one string, so a caller can tell whether any
// id changed state since the last read.
func (s *autoSupervisor) slotClaims() (claims map[string]raidwalk.AutoSlotClaim, held map[string]string, sig string) {
	claims = map[string]raidwalk.AutoSlotClaim{}
	held = map[string]string{}
	for _, st := range s.stateList() {
		st.mu.Lock()
		idle := (st.done || st.stopped || st.escalated) && !st.refused && st.run == nil
		var key string
		if !idle && st.current < len(st.goals) {
			t := st.goals[st.current].target
			key = raidwalk.AutoSlotKey(t.Server, t.Slot)
		}
		st.mu.Unlock()
		if idle {
			claims[st.id] = raidwalk.AutoSlotInactive
			continue
		}
		claims[st.id] = raidwalk.AutoSlotActive
		if _, taken := held[key]; key != "" && !taken {
			held[key] = st.id
		}
	}
	s.statesMu.Lock()
	for _, p := range s.pending {
		claims[p.id] = raidwalk.AutoSlotActive
	}
	s.statesMu.Unlock()
	var parts []string
	for id, c := range claims {
		parts = append(parts, fmt.Sprintf("%s=%d", id, c))
	}
	for k, id := range held {
		parts = append(parts, fmt.Sprintf("%s@%s", id, k))
	}
	sort.Strings(parts)
	return claims, held, strings.Join(parts, ";")
}

// autoCurrentStates is each id's current lines for the changed-id check of a re-read: a queued id's lines (the
// ones it will run next), else its state's.
func (s *autoSupervisor) autoCurrentStates() map[string]*autoState {
	cur := map[string]*autoState{}
	for _, st := range s.stateList() {
		cur[st.id] = st
	}
	s.statesMu.Lock()
	for _, p := range s.pending {
		cur[p.id] = p
	}
	s.statesMu.Unlock()
	return cur
}

// reloadTargets re-reads targets.txt when it changed (mtime or size): new ids start, removed ids stop cleanly, and an
// id whose goal lines changed is stopped and re-added from the new lines. An unreadable file, or one with no valid
// line, keeps the targets already loaded and logs one warning (a half-written file is not a stop order). A bad line
// is skipped with a warning naming it, the other lines apply and the bad line's id keeps its old state. The brief's
// walk 39 added every raid in its own autopilot dir because the file was read once.
func (s *autoSupervisor) reloadTargets() {
	path := filepath.Join(s.o.walkDir, autoTargetsFile)
	st, err := os.Stat(path)
	if err != nil {
		if s.targetsWarn != err.Error() {
			s.targetsWarn = err.Error()
			s.event("all", "targets", fmt.Sprintf("targets.txt is unreadable, keeping the %d loaded target(s): %v", len(s.stateList()), err))
		}
		return
	}
	claims, held, sig := s.slotClaims()
	if st.ModTime().Equal(s.targetsMod) && st.Size() == s.targetsSize {
		// Unchanged file. A skipped line is read again only when some id's state changed since the last read: a slot
		// conflict clears when the id holding the slot finishes, with no further edit.
		if !s.targetsSkipped || sig == s.targetsSig {
			return
		}
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		return
	}
	s.targetsReads++
	// Two phases, as the start's read: the first read classifies by state; an id whose new lines differ from its
	// current ones is then a new id for the check (review round 3: an edited done id claimed nothing and was queued
	// onto a running id's slot; an edited active id moving onto an occupied slot won by file order), and keeps only
	// the slot its state holds now (held), and the file is read again.
	read := func() ([]raidwalk.AutoTarget, []raidwalk.AutoTargetIssue, error) {
		return raidwalk.ParseAutoTargetsHeld(bytes.NewReader(raw), func(id string) raidwalk.AutoSlotClaim { return claims[id] }, held)
	}
	targets, issues, err := read()
	if err == nil {
		cur := s.autoCurrentStates()
		changed := false
		for _, next := range groupAutoTargets(targets) {
			if old := cur[next.id]; old != nil && claims[next.id] != raidwalk.AutoSlotNew && !autoSameTargets(old, next) {
				claims[next.id] = raidwalk.AutoSlotNew
				changed = true
			}
		}
		if changed {
			targets, issues, err = read()
		}
	}
	if err != nil {
		if s.targetsWarn != err.Error() {
			s.targetsWarn = err.Error()
			s.event("all", "targets", fmt.Sprintf("invalid targets file, keeping the %d loaded target(s): %v", len(s.stateList()), err))
		}
		return
	}
	// A bad line is skipped with a warning naming it; the other lines apply, and the bad line's id keeps its old
	// state (walk 40: one bad line made the whole file invalid and every edit was ignored for ~60 minutes).
	skip := map[string]bool{}
	var warn []string
	for _, is := range issues {
		skip[is.ID] = true
		warn = append(warn, fmt.Sprintf("id %s: %v", is.ID, is.Err))
	}
	if key := strings.Join(warn, "\n"); key != s.targetsWarn {
		s.targetsWarn = key
		for _, w := range warn {
			s.event("all", "targets", fmt.Sprintf("invalid targets line skipped, the other lines apply and the id keeps its old state: %s", w))
		}
	}
	s.targetsMod, s.targetsSize = st.ModTime(), st.Size()
	s.targetsSkipped, s.targetsSig = len(issues) > 0, sig
	s.applyTargets(targets, skip)
}

// autoSameTargets says two states walk the same goal lines with the same arguments: a re-read leaves such an id alone.
func autoSameTargets(a, b *autoState) bool {
	if len(a.goals) != len(b.goals) {
		return false
	}
	for i := range a.goals {
		x, y := a.goals[i].target, b.goals[i].target
		// The boss-wipe budget is a goal option (--boss-wipe-max is taken out of Args), so an edit of it alone is a
		// changed goal line too.
		if x.ID != y.ID || x.Server != y.Server || x.Slot != y.Slot || x.Holder != y.Holder || x.Goal != y.Goal || x.Start != y.Start || x.BossWipeMax != y.BossWipeMax {
			return false
		}
		if len(x.Args) != len(y.Args) {
			return false
		}
		for j := range x.Args {
			if x.Args[j] != y.Args[j] {
				return false
			}
		}
	}
	return true
}

// applyTargets reconciles the loaded ids with a new read: an unchanged id keeps its state (its goal, attempt, backoff
// and run); a removed id is stopped like STOP-<id>; a new or changed id waits in pending until its old state is gone,
// then starts (a changed id's slot is still the old run's until that teardown is verified). An id in skip had a bad
// line: its old state is left alone and nothing of it is queued.
func (s *autoSupervisor) applyTargets(targets []raidwalk.AutoTarget, skip map[string]bool) {
	fresh := groupAutoTargets(targets)
	newByID := map[string]*autoState{}
	for _, st := range fresh {
		newByID[st.id] = st
	}
	oldByID := map[string]*autoState{}
	for _, st := range s.stateList() {
		oldByID[st.id] = st
		if skip[st.id] {
			// A bad line of this id: its old state runs on untouched.
			continue
		}
		if next, ok := newByID[st.id]; ok {
			if autoSameTargets(st, next) {
				continue
			}
			s.requestStop(st, "targets.txt changed the id's goals")
			st.mu.Lock()
			st.remove = true
			st.mu.Unlock()
			s.queueState(next)
			continue
		}
		s.requestStop(st, "removed from targets.txt")
		st.mu.Lock()
		st.remove = true
		st.mu.Unlock()
	}
	for _, st := range fresh {
		if _, ok := oldByID[st.id]; !ok {
			s.queueState(st)
		}
	}
}

// queueState holds a new (or changed) id's state until its id is free.
func (s *autoSupervisor) queueState(st *autoState) {
	s.statesMu.Lock()
	for i, p := range s.pending {
		if p.id == st.id {
			if autoSameTargets(p, st) {
				// A re-read every tick (a skipped line) queues the same lines again: nothing to replace or log.
				s.statesMu.Unlock()
				return
			}
			s.pending[i] = st
			s.statesMu.Unlock()
			s.event(st.id, "targets", "queued: the new lines replace an id still stopping")
			return
		}
	}
	s.pending = append(s.pending, st)
	s.statesMu.Unlock()
	s.event(st.id, "targets", "queued: the id starts when its slot is free")
}

// adoptPending adds the queued ids whose id is no longer running: the old state was dropped after its clean stop.
// An escalated or refused id is never dropped, so a changed line's new lines replace its state once it has no live
// run and its slot is free (review round 2: an edited line could not replace a refused id); while the old run or
// lease keeps the slot, the lines wait and the wait is logged once.
func (s *autoSupervisor) adoptPending() {
	s.statesMu.Lock()
	queued := s.pending
	s.pending = nil
	s.statesMu.Unlock()
	if len(queued) == 0 {
		return
	}
	byID := map[string]*autoState{}
	for _, st := range s.stateList() {
		byID[st.id] = st
	}
	for _, st := range queued {
		if other := s.slotHeldByOther(st); other != "" {
			// The re-read already refuses such lines; this is the launch-side check of the same rule: a queued id never
			// starts on a slot another id's live state holds.
			s.noteLinesWait(st.id, fmt.Sprintf("new lines wait: a slot is held by %s", other))
			s.statesMu.Lock()
			s.pending = append(s.pending, st)
			s.statesMu.Unlock()
			continue
		}
		if old := byID[st.id]; old != nil {
			if !s.replaceEscalated(old) {
				s.statesMu.Lock()
				s.pending = append(s.pending, st)
				s.statesMu.Unlock()
				continue
			}
			// The old state is dropped before the new one is numbered: initSeq counts the dirs on disk, so the new
			// lines start past the old run's outs.
			s.dropState(old)
			delete(byID, old.id)
		}
		s.addState(st)
		// The id is in the list before initSeq numbers it: a state added after the numbering would keep seq 0 and
		// launch <id>-1, whose out dir may already hold an earlier run's summary.json (a changed goal would read it
		// and call itself met). initSeq counts the dirs on disk, so the new run starts past them.
		s.initSeq()
		s.reconcileSlot(st, nil)
		s.event(st.id, "targets", "added from targets.txt")
	}
}

// slotHeldByOther names another id whose live (not idle) state holds a slot one of st's lines names, "" when none
// does.
func (s *autoSupervisor) slotHeldByOther(st *autoState) string {
	want := map[string]bool{}
	for _, g := range st.goals {
		want[raidwalk.AutoSlotKey(g.target.Server, g.target.Slot)] = true
	}
	for _, o := range s.stateList() {
		if o.id == st.id {
			continue
		}
		o.mu.Lock()
		idle := (o.done || o.stopped || o.escalated) && !o.refused && o.run == nil
		var key string
		if o.current < len(o.goals) {
			key = raidwalk.AutoSlotKey(o.goals[o.current].target.Server, o.goals[o.current].target.Slot)
		}
		o.mu.Unlock()
		if !idle && want[key] {
			return o.id
		}
	}
	return ""
}

// replaceEscalated says a queued id's new lines may replace its escalated or refused state: the id has no live run
// and the slot of its old lines is free. An id still stopping, or whose run or lease still holds the slot, keeps the
// old state (the lines stay queued and the wait is logged once).
func (s *autoSupervisor) replaceEscalated(old *autoState) bool {
	old.mu.Lock()
	escalated, refused, stopping, run := old.escalated, old.refused, old.stopping, old.run
	old.mu.Unlock()
	if !escalated && !refused {
		return false
	}
	switch {
	case stopping || run != nil:
		s.noteLinesWait(old.id, "new lines wait for the run to end")
		return false
	case s.stateSlotHeld(old):
		s.noteLinesWait(old.id, "new lines wait for the slot lease")
		return false
	}
	s.stopMu.Lock()
	delete(s.waitNoted, old.id)
	s.stopMu.Unlock()
	s.event(old.id, "targets", "the new goal lines replace the escalated state")
	return true
}

// noteLinesWait logs one id's waiting new lines once: the replacement is retried every tick, and the person needs
// the line once, not every poll.
func (s *autoSupervisor) noteLinesWait(id, why string) {
	s.stopMu.Lock()
	if s.waitNoted[id] {
		s.stopMu.Unlock()
		return
	}
	if s.waitNoted == nil {
		s.waitNoted = map[string]bool{}
	}
	s.waitNoted[id] = true
	s.stopMu.Unlock()
	s.event(id, "targets", fmt.Sprintf("id %s: %s", id, why))
}

// dropState removes one state from the list (adoptPending's replaced escalated id).
func (s *autoSupervisor) dropState(st *autoState) {
	s.statesMu.Lock()
	kept := s.states[:0]
	for _, x := range s.states {
		if x != st {
			kept = append(kept, x)
		}
	}
	s.states = kept
	s.statesMu.Unlock()
}

// tick advances every id once.
func (s *autoSupervisor) tick(ctx context.Context) {
	s.reloadTargets()
	s.checkStopFiles()
	for _, st := range s.stateList() {
		s.tickState(st)
	}
	s.dropRemoved()
	s.adoptPending()
	s.saveState()
}

// checkStopFiles handles the per-raid stop files: <walk-dir>/STOP-<id> stops just that raid (its run stopped, its
// slot torn down as a YIELD would, the id done and never relaunched). YIELD stays the whole-dir stop. The file stays
// until the stop completes (finish, after the teardown; or at once for an id with no run), then it is renamed
// STOP-<id>.done, so a re-added id of the same name is not stopped by the order a person already used (walk 39's
// re-added raid would have been stopped by its old file at the first tick). A STOP for a person's id (escalated,
// refused or done) is not applied: the file stays and one line says so.
func (s *autoSupervisor) checkStopFiles() {
	entries, err := os.ReadDir(s.o.walkDir)
	if err != nil {
		return
	}
	byID := map[string]*autoState{}
	for _, st := range s.stateList() {
		byID[st.id] = st
	}
	seen := map[string]bool{}
	for _, e := range entries {
		id, ok := strings.CutPrefix(e.Name(), "STOP-")
		if !ok || id == "" || strings.HasSuffix(id, ".done") {
			continue
		}
		seen[e.Name()] = true
		st := byID[id]
		if st == nil {
			continue
		}
		if s.requestStop(st, "STOP-"+id) {
			continue
		}
		s.noteRefusedStop(e.Name(), st)
	}
	s.stopMu.Lock()
	for name := range s.stopNoted {
		if !seen[name] {
			delete(s.stopNoted, name)
		}
	}
	s.stopMu.Unlock()
}

// noteRefusedStop logs one refused STOP file once (the person's file stays across the ticks, and a tick must not
// repeat the line). A file a finish goroutine consumed after the directory was read is gone by now: it is not
// refused, it completed.
func (s *autoSupervisor) noteRefusedStop(name string, st *autoState) {
	if _, err := os.Stat(filepath.Join(s.o.walkDir, name)); err != nil {
		return
	}
	s.stopMu.Lock()
	if s.stopNoted[name] {
		s.stopMu.Unlock()
		return
	}
	if s.stopNoted == nil {
		s.stopNoted = map[string]bool{}
	}
	s.stopNoted[name] = true
	s.stopMu.Unlock()
	s.event(st.id, "stop", fmt.Sprintf("%s not applied: %s, needs a person", name, autoStateWord(st)))
}

// autoStateWord names the state a refused STOP found (the id is a person's, or its goals were already met).
func autoStateWord(st *autoState) string {
	st.mu.Lock()
	defer st.mu.Unlock()
	switch {
	case st.escalated:
		return "escalated"
	case st.refused:
		return "refused"
	case st.done:
		return "done"
	}
	return "not stopping"
}

// consumeStopFile renames an applied STOP-<id> to STOP-<id>.done. It is a no-op when there is no such file (a removed
// targets line, or a stop the rule itself decided): only an order a person wrote is consumed.
func (s *autoSupervisor) consumeStopFile(id string) {
	path := filepath.Join(s.o.walkDir, "STOP-"+id)
	if _, err := os.Stat(path); err != nil {
		return
	}
	if err := os.Rename(path, path+".done"); err != nil {
		s.event(id, "stop", fmt.Sprintf("STOP-%s could not be consumed: %v", id, err))
		return
	}
	s.stopMu.Lock()
	delete(s.stopNoted, "STOP-"+id)
	s.stopMu.Unlock()
	s.event(id, "stop", fmt.Sprintf("STOP-%s consumed (renamed STOP-%s.done)", id, id))
}

// requestStop stops one id cleanly: a run gets an AutoStop finish (the walk is stopped and the slot is torn down,
// verified, exactly as a met goal's teardown), and an id with no run is done at once. An escalated or refused id is
// left alone: a person owns it, so the order is refused (false) and the caller keeps its file. An id already stopping
// is accepted: its finish consumes the file.
func (s *autoSupervisor) requestStop(st *autoState, why string) bool {
	st.mu.Lock()
	if st.done || st.escalated || st.refused {
		st.mu.Unlock()
		return false
	}
	if st.stopping {
		// The stop is already in progress (the file is consumed when it completes) or was restored from the
		// checkpoint: nothing to request twice.
		st.mu.Unlock()
		return true
	}
	st.stopping, st.stopWhy = true, why
	r := st.run
	if r != nil && r.pending == nil {
		r.pending = &raidwalk.AutoDecision{Action: raidwalk.AutoStop, Reason: why}
	} else if r == nil {
		st.done, st.stopped = true, true
	}
	st.mu.Unlock()
	if r == nil {
		s.event(st.id, "stop", why+": the id is done")
		s.consumeStopFile(st.id)
		s.saveState()
		s.writeStatus()
	}
	return true
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
		dead := st.current < len(st.goals) && st.goals[st.current].dead
		stopping := st.stopping
		due := !time.Now().Before(st.nextAt)
		st.mu.Unlock()
		switch {
		case stopping:
			// requestStop already marked it done; a state whose run was gone (a stop restored from the checkpoint,
			// or a finish that ran before the order was seen) keeps the stop, and the order file is consumed now.
			st.mu.Lock()
			st.done, st.stopped = true, true
			st.mu.Unlock()
			s.consumeStopFile(st.id)
			s.saveState()
			s.writeStatus()
		case dead:
			s.deadGoal(st)
		case due:
			s.preReadRows(st)
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
	// A stop restored from the checkpoint (the supervisor died between the order and the finish) whose run survived
	// or was adopted still needs its finish: the run is stopped and the slot torn down now, not when the walk ends.
	if st.stopping && r.pending == nil {
		r.pending = &raidwalk.AutoDecision{Action: raidwalk.AutoStop, Reason: st.stopWhy}
	}
	if r.pending == nil {
		obs := s.observe(st)
		if obs.Exited && obs.ObserveErr != "" && time.Since(r.exitedAt) < autoSummaryGrace {
			st.mu.Unlock()
			return
		}
		g := st.goals[st.current]
		// A goal boss the summary reports dead is the goal met, whatever this rule would do with the end: the goal is
		// never placed again (the checkpoint keeps the flag across a restart).
		if raidwalk.GoalBossDead(g.target.Goal, obs) {
			st.goals[st.current].dead = true
		}
		// The backoff counts the consecutive no-progress attempts that ended before this one: a run that reached a
		// further step resets the count even though its own finish has not run yet.
		fast := g.noProgress
		// ONE effective step for an ended run: the larger of the live/log reading and the summary's steps_reached.
		// The wipe-at-boss check, the progress test, the fast-failure reset and the finish bookkeeping all use it.
		step, progressed := 0, false
		if obs.Exited {
			step = s.runLastStepLocked(r)
			if obs.StepsReached > step {
				step = obs.StepsReached
			}
			r.lastStep = step
			// Past the place phase: this run breaks the id's streak of instance-cap refusals. A cap refusal and a
			// place-phase end never got that far.
			r.placed = !obs.InstanceCapWait && !obs.PlacementFailed()
			if step > g.bestStep {
				fast = 0
				progressed = true
			}
		}
		if obs.Exited && obs.ExitCode == 19 && (autoHasGMInstanceData(g.target.Args) || g.target.BossWipeFresh) {
			// A wipe whose last step is a boss row, or the row before it: the next launch takes a fresh instance.
			// The rows are read outside the id's mutex (a first parse must not hold up a YIELD's stop).
			target := g.target
			st.mu.Unlock()
			rows, ok := s.targetRows(target)
			st.mu.Lock()
			if st.run != r || r.pending != nil || r.finishing {
				st.mu.Unlock()
				return
			}
			obs.WipeAtBoss = ok && raidwalk.PlaceStepAtBoss(rows, step)
		}
		// A practice goal's cycles are summed over its runs: the goal test and the relaunch rules read the sum, so
		// the prior runs' cycles are folded into this run's observation.
		if r.goal.Kind == "practice" {
			obs.PracticeCycles += g.practiceCycles
		}
		d := raidwalk.DecideAutoAction(obs, raidwalk.AutoRunState{
			Goal: r.goal, Attempt: g.attempt, Reuse: r.reuse, HasRun: g.attempt > 0,
			FastFailures: fast, LeaseFails: g.leaseFails, PlaceFails: st.placeFails, HireWaits: g.hireWaits, Exit29Retries: g.exit29Retries,
			BossWipes: g.bossWipes, BossWipeMax: g.target.BossWipeMax, Progressed: progressed, CapWaits: st.capWaits,
			PracticeRelaunches: g.practiceRelaunches, PracticeKilledRetry: g.practiceKilledRetry,
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
	// Progress resets the backoff; a run that reached no further step grows it (M1). A hire-pool wait is neither: the
	// pool refused the hires before the run started, so it leaves the counts as they were and only moves bestStep when
	// the refused run had already walked past it (walk 39).
	prevBest := g.bestStep
	if lastStep > g.bestStep {
		g.bestStep = lastStep
	}
	switch d.Action {
	case raidwalk.AutoHirePoolWait:
		g.hireWaits++
	case raidwalk.AutoCapWait:
		// Waiting for an instance place is no attempt: neither a failure nor progress, as the hire-pool wait.
	default:
		g.hireWaits = 0
		if lastStep > prevBest {
			g.noProgress = 0
			g.bossWipes = 0
		} else {
			g.noProgress++
		}
	}
	// The cap-refusal streak counts per id, not per goal, and only a launch that got past placement breaks it: a
	// refused place, a place-phase end or a hire-pool wait leaves it as it is (the escalation is in the decision rule).
	switch {
	case d.Action == raidwalk.AutoCapWait:
		st.capWaits++
	case r.placed:
		st.capWaits = 0
	}
	if d.Exit29Retry {
		g.exit29Retries++
	}
	if d.FreshAfterWipe {
		g.bossWipes++
		// The fresh-after-wipe mode stays pending across exit-26 retries, other pre-placement failures and
		// checkpoint save/restore: every launch uses the goal's --from in a fresh instance until one gets past
		// placement (round 4: an exit-26 retry used bestStep, the boss row, instead).
		g.freshPending = true
	} else {
		// Placed runs clear it (a fresh copy is now the instance); hire-pool waits can arrive after a placement,
		// and a hire-pool wait never clears it (no placement happened yet).
		if r.placed && d.Action != raidwalk.AutoHirePoolWait {
			g.freshPending = false
		}
	}
	if g.target.Goal.Kind == "practice" {
		// The practice goal's cycles accumulate over its runs, and its relaunch budget is kept with the goal: the
		// decision reads both on the next end.
		g.practiceCycles += r.practiceCycles
		if d.PracticeRelaunch {
			g.practiceRelaunches++
		}
		if d.PracticeKilledRetry {
			g.practiceKilledRetry = true
		}
	}
	if r.exitCode == 15 {
		g.leaseFails++
	} else {
		g.leaseFails = 0
	}
	// A fresh-instance restart is a placement that failed on the old bind: count those in a row, and reset the count
	// on every other end (a run that placed, advanced or stopped broke the streak). A hire-pool wait is no attempt at
	// all, so it leaves the count alone.
	switch {
	case d.Action == raidwalk.AutoHirePoolWait:
	case d.Action == raidwalk.AutoCapWait:
		// The cap refused a place before anything was placed: no failed placement, no broken streak.
	case d.FreshAfterWipe:
		// A fresh instance after a boss wipe is no failed placement: it neither adds to the count nor breaks a streak.
	case d.Action == raidwalk.AutoRestart && !d.Reuse:
		st.placeFails++
	default:
		st.placeFails = 0
	}
	var esc string
	stopping := st.stopping
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
			if st.stopping {
				st.stopped = true
			}
		}
	case raidwalk.AutoRestart:
		st.last.reuse = d.Reuse
		wait := s.o.settle
		if d.Backoff > wait {
			wait = d.Backoff
		}
		st.nextAt = time.Now().Add(wait)
		esc = fmt.Sprintf("restart: %s; next %s after %s", d.Reason, s.nextPlacement(d.Reuse), wait.Round(time.Second))
	case raidwalk.AutoHirePoolWait:
		st.last.reuse = true
		hw := d.Backoff
		if s.o.settle > hw {
			hw = s.o.settle
		}
		st.nextAt = time.Now().Add(hw)
		esc = fmt.Sprintf("hire-pool-wait: %s; next same instance after %s", d.Reason, hw.Round(time.Second))
	case raidwalk.AutoCapWait:
		// A fresh instance is what the cap refused, and the retry asks for one again at once: raid-walk waited out its
		// own --instance-cap-wait before the exit, so a second wait here would double it (and with a 0 there would be
		// a 20 m wait that was meant to be none).
		st.last.reuse = false
		st.nextAt = time.Now()
		esc = fmt.Sprintf("instance-cap-wait: %s; next fresh instance at once (refusal %d of %d)", d.Reason, st.capWaits, raidwalk.AutoCapRefuseMax)
	}
	st.mu.Unlock()

	action, _, _ := strings.Cut(esc, ":")
	s.event(st.id, action, strings.TrimSpace(strings.TrimPrefix(esc, action+":")))
	if d.Action == raidwalk.AutoStop && d.Escalate {
		s.escalate(st, "stop", fmt.Sprintf("%s: %s", slot, d.Reason))
		return
	}
	// The stop is complete only now, after the verified teardown: the order file is consumed then (not when it is
	// read), so a restart mid-stop still finds the order.
	if d.Action == raidwalk.AutoStop && stopping {
		s.consumeStopFile(st.id)
	}
	s.saveState()
	s.writeStatus()
}

// deadGoal ends the current goal whose boss is already dead: the id's next goal line, else DONE. It is a DONE state,
// never an ESCALATE, and the slot is free (the run that saw the death was torn down before this). The checkpoint's
// boss_dead keeps it across a supervisor restart.
func (s *autoSupervisor) deadGoal(st *autoState) {
	st.mu.Lock()
	if st.done || st.escalated || st.refused || st.stopping || st.current >= len(st.goals) || !st.goals[st.current].dead {
		st.mu.Unlock()
		return
	}
	goal := st.goals[st.current].target.Goal.String()
	if st.current+1 < len(st.goals) {
		next := st.goals[st.current+1].target.Goal.String()
		st.current++
		st.last = nil
		st.nextReuse = false
		st.goals[st.current] = autoGoalState{target: st.goals[st.current].target}
		st.nextAt = time.Now().Add(s.o.settle)
		st.mu.Unlock()
		s.event(st.id, "already dead", fmt.Sprintf("goal %s is dead; next goal %s", goal, next))
	} else {
		st.done = true
		st.mu.Unlock()
		s.event(st.id, "already dead", fmt.Sprintf("goal %s is dead; the last goal is done", goal))
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
	if st.stopping || st.goals[st.current].dead {
		stopping, dead := st.stopping, st.goals[st.current].dead
		st.mu.Unlock()
		if dead && !stopping {
			s.deadGoal(st)
		}
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
		// A reached step below the goal's own step start belongs to an earlier goal's walk, not this one (a crash
		// between the advance and the progress it observed): the goal starts at its --from, never behind it. Only
		// the contaminated step state is cleared (step, st.last, g.bestStep); the reuse decision survives: a goal
		// advance means the next goal starts at its own --from in the existing instance. A leg start has no step
		// number, so only a step start clamps.
		if start, ok := strings.CutPrefix(g.target.Start, "step "); ok {
			if want, err := strconv.Atoi(start); err == nil && step < want {
				s.event(st.id, "resume", fmt.Sprintf("ignoring step %d below the goal's start %q", step, g.target.Start))
				st.last = nil
				g.bestStep = 0
				step = 0
			}
		}
	}
	if step > 0 {
		from = fmt.Sprintf("step %d", step)
		reuse = !fresh
	}
	if g.target.BossWipeFresh && g.freshPending {
		// A boss wipe with --boss-wipe-fresh: the goal starts again from its own start in a fresh instance, never a
		// step resume into the old copy (a same-instance relaunch keeps the respawned tunnel trash that caused exit 19).
		// bestStep survives: only a run past the OLD best counts as progress, so a fresh run that wipes at the same
		// boss again does not reset the wipe budget. The mode stays pending until a launch gets past placement, so an
		// exit-26 retry between the wipe and placement still starts from the goal's --from, never from bestStep.
		from = g.target.Start
		reuse = false
		step = 0
		s.event(st.id, "resume", fmt.Sprintf("fresh instance after a boss wipe: from the goal's start %q", from))
	}
	resumeFrom := ""
	if from != "" {
		// A step raid-walk refuses as a start (the boss row a wipe died on) is not a reason to escalate: resume
		// from the nearest row of the same leg it takes.
		if goalFrom := s.autoGoalResumeStart(g.target, step); step > 0 && goalFrom != "" {
			// A wipe at the goal's boss (or on its approach rows) resumes at the goal's own start: the row before
			// the boss row stands inside his reach and the placement pulls him (walk 46 Jin'do).
			resumeFrom = fmt.Sprintf("step %d is the boss or its approach: resuming from the goal's start %q", step, goalFrom)
			from = goalFrom
		} else if adjusted := s.autoResumeStart(g.target, from); adjusted != from {
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
	o.PracticeCycles = sum.PracticeCycles
	r.practiceCycles = sum.PracticeCycles
	o.HirePoolWait = raidwalk.HirePoolRefused(sum.EndReason)
	if sum.ExitCode != 0 {
		o.ExitCode = sum.ExitCode
	}
	// Exit 26 is the cap refusing a fresh place, except when the ledger could not be read at all: that is no cap to
	// wait out, and the walk has already waited its minute for it.
	o.InstanceCapWait = o.ExitCode == exitRaidWalkInstanceCap && !strings.Contains(sum.EndReason, "cannot be read")
	for _, b := range sum.BossKills {
		b.deadEntries(o.KilledBosses)
	}
	if sum.BossKill != nil {
		sum.BossKill.deadEntries(o.KilledBosses)
	}
	return o
}

// autoSummary is the part of summary.json the autopilot reads.
type autoSummary struct {
	ExitCode     int            `json:"exit_code"`
	StepsReached int            `json:"steps_reached"`
	StepsTotal   int            `json:"steps_total"`
	EndReason    string         `json:"end_reason"`
	BossKills    []autoBossKill `json:"boss_kills"`
	// BossKill is the last boss the walk engaged (older summaries only; boss_kills has all of them).
	BossKill *autoBossKill `json:"boss_kill"`
	// Practice is the practice run's cycle object; PracticeCycles is its cycles (a practice goal's measure).
	Practice       *autoPractice `json:"practice,omitempty"`
	PracticeCycles int           `json:"-"`
}

// autoPractice is the summary practice object the autopilot reads.
type autoPractice struct {
	Entry    uint32 `json:"entry"`
	Attempts int    `json:"attempts"`
	Cycles   int    `json:"cycles"`
	Ended    string `json:"ended"`
	Rows     string `json:"rows,omitempty"`
}

// autoBossKill is one summary boss record. phase_entry is the entry the boss's guid read last (Thekal's row is
// 14599, his reborn form 14509): a kill counts for a goal that names either entry.
type autoBossKill struct {
	Entry      uint32 `json:"entry"`
	Killed     bool   `json:"killed"`
	PhaseEntry uint32 `json:"phase_entry,omitempty"`
}

// deadEntries adds one summary record's entries to a kill map.
func (b autoBossKill) deadEntries(m map[uint32]bool) {
	if !b.Killed {
		return
	}
	m[b.Entry] = true
	if b.PhaseEntry != 0 && b.PhaseEntry != b.Entry {
		m[b.PhaseEntry] = true
	}
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
	if s.Practice != nil {
		s.PracticeCycles = s.Practice.Cycles
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

// reapLeases frees the slots of holders that died on this host, once per lease ledger the targets can use: the
// default ledger, plus a --slots file a target names. A dead holder is a walk whose process is gone (its pid dead or
// reused): its lease is removed at once instead of blocking the slot until the TTL, which is how walk 47's 12 slots
// stayed taken for two hours. A lease of another host is left to its expiry. A reap error is logged and never blocks
// the start: an unreadable ledger is a person's problem, not a reason to leave every raid unwatched.
func (s *autoSupervisor) reapLeases(targets []raidwalk.AutoTarget) {
	dirs := map[string]bool{}
	if def := slots.DefaultPath(); def != "" {
		dirs[slots.LeasesDir(def)] = true
	}
	for _, t := range targets {
		if p := autoFlag(t.Args, "--slots"); p != "" {
			dirs[slots.LeasesDir(slots.ExpandTilde(p))] = true
		}
	}
	names := make([]string, 0, len(dirs))
	for d := range dirs {
		names = append(names, d)
	}
	sort.Strings(names)
	for _, dir := range names {
		got, err := slots.ReapDeadLeases(dir, false)
		if err != nil {
			s.event("all", "reap", fmt.Sprintf("%s: %v (leaving every lease it names)", dir, err))
			continue
		}
		for _, l := range got {
			s.event("all", "reap", fmt.Sprintf("freed %s/%s (holder %s pid %d is gone or reused)",
				l.Server, l.Slot, l.Holder, l.HolderPID))
		}
	}
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
	// FreshAfterWipe is the stale fresh-after-wipe run marker from rounds 1-3 (carried on a finished st.last). It is
	// unused now: the pending fresh-after-wipe mode lives on the goal and the checkpoint's fresh_pending entry. It
	// stays on disk so an older checkpoint still reads back.
	FreshAfterWipe bool   `json:"fresh_after_wipe,omitempty"`
	Attempt        int    `json:"attempt"`
	ChildPID       int    `json:"child_pid,omitempty"`
	OutDir         string `json:"out_dir,omitempty"`
	Done           bool   `json:"done,omitempty"`
	Escalated      bool   `json:"escalated,omitempty"`
	// Stopped and StopWhy say a STOP file (or a removed targets line) ended this id: it is done, but not because its
	// goals were met. The checkpoint keeps them so a restart still reports the id as stopped (not as a met goal).
	Stopped bool   `json:"stopped,omitempty"`
	StopWhy string `json:"stop_why,omitempty"`
	// Stopping says a STOP file (or a removed line) asked this id to stop and the stop is not complete: a restart
	// finishes it instead of relaunching the run it was stopping.
	Stopping bool `json:"stopping,omitempty"`
	// Fingerprint is the sha256 of the id's ordered goal lines this checkpoint was written for (empty in a checkpoint
	// from before it, which is taken as the same lines). A start restores the goal and step progress only when it
	// matches targets.txt now.
	Fingerprint string `json:"fingerprint,omitempty"`
	// HireWaits is the current goal's consecutive hire-pool waits: the sixth stops the id for a person, across a
	// restart too.
	HireWaits int `json:"hire_waits,omitempty"`
	// Exit29Retries is the current goal's fresh-instance retries after an exit 29 (one per goal), across a restart too.
	Exit29Retries int `json:"exit29_retries,omitempty"`
	// BossWipes is the current goal's fresh-instance relaunches after a boss wipe since its last progress, across a
	// restart too (a checkpoint from before it reads 0).
	BossWipes int `json:"boss_wipes,omitempty"`
	// FreshPending is the current goal's pending fresh-after-wipe mode (--boss-wipe-fresh): while set, every launch
	// uses the goal's --from in a fresh instance, across a supervisor restart too.
	FreshPending bool `json:"fresh_pending,omitempty"`
	// BossDead says the current goal's boss is dead in the instance: the goal is DONE at a start, never placed again.
	BossDead bool `json:"boss_dead,omitempty"`
	// PracticeCycles, PracticeRelaunches and PracticeKilledRetry are the practice goal's progress: its summed cycles
	// over its runs, its exit-18 relaunches and whether its one fresh-instance relaunch after a boss death was taken.
	PracticeCycles      int  `json:"practice_cycles,omitempty"`
	PracticeRelaunches  int  `json:"practice_relaunches,omitempty"`
	PracticeKilledRetry bool `json:"practice_killed_retry,omitempty"`
	// CapWaits is the id's consecutive instance-cap refusals (exit 26): the fourth stops it for a person, across a
	// restart too.
	CapWaits int    `json:"cap_waits,omitempty"`
	NextAt   string `json:"next_at,omitempty"`
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
	for _, st := range s.stateList() {
		st.mu.Lock()
		e := autoSavedID{
			ID: st.id, Goal: st.current, GoalsDone: st.current,
			Done: st.done, Escalated: st.escalated, Reuse: st.nextReuse,
			Stopped: st.stopped, StopWhy: st.stopWhy, Stopping: st.stopping,
			Fingerprint: autoTargetFingerprint(st), CapWaits: st.capWaits,
		}
		if st.current < len(st.goals) {
			g := st.goals[st.current]
			e.Attempt, e.LastStep, e.BossDead, e.HireWaits = g.attempt, g.bestStep, g.dead, g.hireWaits
			e.Exit29Retries = g.exit29Retries
			e.BossWipes = g.bossWipes
			e.FreshPending = g.freshPending
			e.PracticeCycles, e.PracticeRelaunches, e.PracticeKilledRetry = g.practiceCycles, g.practiceRelaunches, g.practiceKilledRetry
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
	return autoLoadCheckpoint(s.o.walkDir)
}

// autoLoadCheckpoint reads <dir>/autopilot-state.json (no supervisor needed: the start's targets read uses it too).
func autoLoadCheckpoint(dir string) (autoSavedState, error) {
	var saved autoSavedState
	path := filepath.Join(dir, autoStateFile)
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
	for _, st := range s.stateList() {
		st.mu.Lock()
		t := st.goals[0].target
		sv := byID[st.id]
		if sv != nil && sv.Fingerprint != "" && sv.Fingerprint != autoTargetFingerprint(st) {
			// The lines changed under the checkpoint (a person edited targets.txt while no supervisor ran): the
			// saved goal, step and stop belong to other goal lines. Only the slot is reconciled (a live walk or a
			// held lease still refuses the id); the new lines start fresh.
			st.mu.Unlock()
			s.event(st.id, "checkpoint", "the goal lines changed since the checkpoint; its progress is not restored")
			s.reconcileSlot(st, nil)
			continue
		}
		if sv != nil {
			if sv.Goal >= 0 && sv.Goal < len(st.goals) {
				st.current = sv.Goal
				st.goals[st.current].attempt = sv.Attempt
				st.goals[st.current].bestStep = sv.LastStep
				st.goals[st.current].dead = sv.BossDead
				st.goals[st.current].hireWaits = sv.HireWaits
				st.goals[st.current].exit29Retries = sv.Exit29Retries
				st.goals[st.current].bossWipes = sv.BossWipes
				st.goals[st.current].freshPending = sv.FreshPending || sv.FreshAfterWipe
				st.goals[st.current].practiceCycles = sv.PracticeCycles
				st.goals[st.current].practiceRelaunches = sv.PracticeRelaunches
				st.goals[st.current].practiceKilledRetry = sv.PracticeKilledRetry
			}
			st.done, st.escalated = sv.Done, sv.Escalated
			st.stopped, st.stopWhy = sv.Stopped, sv.StopWhy
			st.stopping = sv.Stopping
			st.capWaits = sv.CapWaits
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
		done := st.done || st.escalated || (st.current < len(st.goals) && st.goals[st.current].dead)
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
// anything else on the slot refuses the id (H2: never launch on a slot with a live walk or a lease). An id whose
// checkpoint says a stop is in progress is never launched again: its live walk (if any) is still adopted so the stop
// tears it down, but a lease left by the crashed stop or a foreign holder does not refuse it (a person's stop is not
// a launch decision).
func (s *autoSupervisor) reconcileSlot(st *autoState, sv *autoSavedID) {
	st.mu.Lock()
	t := st.goals[st.current].target
	out := st.lastOut
	reuse := st.nextReuse
	stopping := st.stopping
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
		if stopping {
			return
		}
		s.refuse(st, "the slot's lease cannot be verified: "+err.Error())
		return
	}
	pid := 0
	if out != "" {
		pid = autoReadPID(filepath.Join(out, walkPIDFile))
	}
	live := pid > 0 && processAlive(pid)
	if !held && !live {
		if stopping {
			// Nothing runs: the stop completes at once (tickState marks it stopped and consumes the order).
			return
		}
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
	if stopping {
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
		b.deadEntries(o.KilledBosses)
	}
	if sum.BossKill != nil {
		sum.BossKill.deadEntries(o.KilledBosses)
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
	for _, st := range s.stateList() {
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
	for _, st := range s.stateList() {
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
	for _, st := range s.stateList() {
		st.mu.Lock()
		g := "none"
		if st.current < len(st.goals) {
			g = st.goals[st.current].target.Goal.String()
		}
		switch {
		case st.escalated || st.refused:
			fmt.Fprintf(&b, "ESCALATE %s\n", st.escalation)
		case st.stopped:
			fmt.Fprintf(&b, "%s goal=%s stopped (%s)\n", st.id, g, st.stopWhy)
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
