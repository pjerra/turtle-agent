// raid-walk: hire a raid, bind it to the TortoiseBots run leader and walk its plan until the plan's `until` point
// (spec ~/TortoiseBots-audit/specs/turtle-agent-raid-tools.md task A3). The pure decisions live in
// internal/raidwalk (A2); this file holds the flags, the phase wiring and the output dir, raid_walk_phases.go the
// phases.
//
// Every GM line the walk sends passes raidwalk.AllowedLine first (spec 3.6); everything else goes through the
// `commander` RPC. The walk never reads a token file itself (S0's slotClient does) and never prints a secret.
package main

import (
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
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync/atomic"
	"syscall"
	"time"

	"github.com/pjerra/turtle-agent/internal/dungeon"
	"github.com/pjerra/turtle-agent/internal/mapview"
	"github.com/pjerra/turtle-agent/internal/objects"
	"github.com/pjerra/turtle-agent/internal/packet"
	"github.com/pjerra/turtle-agent/internal/patrols"
	"github.com/pjerra/turtle-agent/internal/planexport"
	"github.com/pjerra/turtle-agent/internal/privfile"
	"github.com/pjerra/turtle-agent/internal/raidwalk"
	"github.com/pjerra/turtle-agent/internal/session"
	"github.com/pjerra/turtle-agent/internal/slots"
	"github.com/pjerra/turtle-agent/internal/tbdash"
	"github.com/pjerra/turtle-agent/internal/tbm"
)

// The exit codes of a raid walk (spec 1.2).
const (
	exitRaidWalkFailed       = 18 // a phase failed or was refused
	exitRaidWalkIncident     = 19 // the walk ran and was torn down, but a wipe or a boss engagement happened, or hires were left
	exitRaidWalkInjectFailed = 23 // a walk that would exit 0 had at least one fault with verdict fail
)

const raidWalkUsage = `usage: turtle-agent raid-walk <plan> [--server m910q] [--slot N] [--holder NAME] [--dir DIR] [--sql FILE]
    [--until boss|none|step N|leg L] [--from leg L|step N] [--speed X] [--hires FILE] [--tier N|--tier-override N]
    [--enter auto|near|tele] [--hold-minutes M] [--max-run-minutes 90] [--paused-wait 10m] [--auto-resume K]
    [--reset-wait 5m] [--patrols FILE] [--out DIR] [--dash-url URL] [--dash-user TADASH] [--dash-pass-file PATH]
    [--mmaps DIR] [--slots FILE] [--binary PATH] [--inject SPEC] [--dry-run]`

// raidWalkPlanRe is the plan name as the server knows it (spec 3.1).
var raidWalkPlanRe = regexp.MustCompile(`^[a-z0-9_-]{1,32}$`)

// raidWalkSpeedRe is --speed: 1.0 to 3.0 with at most one decimal.
var raidWalkSpeedRe = regexp.MustCompile(`^[1-3](?:\.[0-9])?$`)

// raidWalkOptions are the command's flags.
type raidWalkOptions struct {
	plan          string
	server        string
	slot          string // "" leases any free slot
	holder        string
	dir           string
	sql           string
	until         string
	from          string
	speed         float64
	hires         string
	tier          int // --tier: the kit of a raid the committed table leaves unknown
	tierOverride  int // --tier-override: a comparison, not a tier proof
	enter         string
	holdMinutes   int
	maxRunMinutes int
	pausedWait    time.Duration
	autoResume    int
	resetWait     time.Duration
	patrols       string
	out           string
	dashURL       string
	dashUser      string
	dashPassFile  string
	mmaps         string
	slotsPath     string
	binary        string
	injects       stringList // --inject, repeatable
	dryRun        bool
}

// stringList is a repeatable string flag: --inject a --inject b, or --inject=a.
type stringList []string

func (s *stringList) String() string { return strings.Join(*s, ",") }

func (s *stringList) Set(v string) error {
	*s = append(*s, v)
	return nil
}

// raidWalkPairEntry is the two-raid mode's entry (spec raid-walk-addons 4.1); nil in this build (P1 sets it).
var raidWalkPairEntry func(ctx context.Context, args []string, stdout, stderr io.Writer) int

// raidWalkSlotList reports a --slot (or --slot=) value that lists slots, e.g. "1,2": the pair form.
func raidWalkSlotList(args []string) (string, bool) {
	for i, a := range args {
		var v string
		switch {
		case a == "--slot":
			if i+1 < len(args) {
				v = args[i+1]
			}
		case strings.HasPrefix(a, "--slot="):
			v = strings.TrimPrefix(a, "--slot=")
		default:
			continue
		}
		if strings.Contains(v, ",") {
			return v, true
		}
	}
	return "", false
}

// walkInjector is the fault injector's command side (spec raid-walk-addons 1.6): nil in this build (I2 sets it).
type walkInjector interface {
	Prepare(ctx context.Context) error
	Tick(ctx context.Context, now time.Time) error
	Cleanup(ctx context.Context)
	Results() ([]raidwalk.InjectSummary, int)
	Incidents() []raidwalk.InjectIncident
	Blocking() bool
	Failed() bool
	PrintPlan(io.Writer)
}

// newWalkInjector builds the injector from the --inject specs; nil in this build (I2 sets it).
var newWalkInjector func(w *walk, specs []string) (walkInjector, error)

// raidWalkCmd is the command line entry.
func raidWalkCmd(ctx context.Context, args []string, stdout, stderr io.Writer) int {
	// Two raids at once: a --slot list is the pair form, handled before anything is parsed (spec 1.7.5).
	if _, ok := raidWalkSlotList(args); ok {
		if raidWalkPairEntry == nil {
			fmt.Fprintln(stderr, "this build has no two-raid mode (--slot A,B)")
			return exitUsage
		}
		return raidWalkPairEntry(ctx, args, stdout, stderr)
	}
	o, code := parseRaidWalkArgs(args, stderr)
	if code != exitOK {
		return code
	}
	// The owner rule, before anything else: never slot 3, for every server (spec 3.1).
	if o.slot == "3" {
		fmt.Fprintln(stderr, "raid-walk never uses slot 3 (owner rule)")
		return exitSlotRefused
	}
	if !o.dryRun && !slotsSupported() {
		return slotsUnsupported("raid-walk", stderr)
	}
	env, deps := walkEnv{}, walkDeps{}
	if !o.dryRun {
		var err error
		if env, deps, err = defaultWalkEnv(&o); err != nil {
			fmt.Fprintf(stderr, "raid-walk: %v\n", err)
			return exitUsage
		}
	}
	return raidWalkRun(ctx, o, env, deps, stdout, stderr)
}

// parseRaidWalkArgs reads the flags and checks their shape (no file or network access).
func parseRaidWalkArgs(args []string, stderr io.Writer) (raidWalkOptions, int) {
	o := raidWalkOptions{}
	fs := flag.NewFlagSet("raid-walk", flag.ContinueOnError)
	fs.SetOutput(stderr)
	fs.Usage = func() { fmt.Fprintln(stderr, raidWalkUsage) }
	speed := "1.0"
	fs.StringVar(&o.server, "server", "m910q", "the slot server")
	fs.StringVar(&o.slot, "slot", "", "the slot id (default: any free slot)")
	fs.StringVar(&o.holder, "holder", "", "the lease holder (default raid-walk-<plan>)")
	fs.StringVar(&o.dir, "dir", "dungeons", "the folder of plan files")
	fs.StringVar(&o.sql, "sql", "", "the deployed migration: step numbers and the first point come from it")
	fs.StringVar(&o.until, "until", "boss", "boss, none, \"step N\" or \"leg L\"")
	fs.StringVar(&o.from, "from", "", "\"leg L\" or \"step N\" for the run leader")
	fs.StringVar(&speed, "speed", "1.0", "the run's test speed, 1.0 to 3.0")
	fs.StringVar(&o.hires, "hires", "", "the tb-raid-roster v1 file (default: from the plan's raid block)")
	fs.IntVar(&o.tier, "tier", 0, "the hire gear tier (1-3) of a raid the committed table leaves unknown")
	fs.IntVar(&o.tierOverride, "tier-override", 0, "the hire gear tier (1-3) in place of the raid's minimum: a comparison, not a tier proof")
	fs.StringVar(&o.enter, "enter", "auto", "auto, near or tele")
	fs.IntVar(&o.holdMinutes, "hold-minutes", 0, "keep the raid bound for this many minutes after the run")
	fs.IntVar(&o.maxRunMinutes, "max-run-minutes", 90, "the ceiling from run start to the end")
	fs.DurationVar(&o.pausedWait, "paused-wait", 10*time.Minute, "how long a pause that is not a regroup may last")
	fs.IntVar(&o.autoResume, "auto-resume", 0, "resume a regroup pause this many times per step (0-3)")
	fs.DurationVar(&o.resetWait, "reset-wait", 5*time.Minute, "how long a wipe may wait for the instance to reset")
	fs.StringVar(&o.patrols, "patrols", "", "a tb-patrols file for the revive quiet check")
	fs.StringVar(&o.out, "out", "", "the output dir (default ./raid-walk-<plan>-<stamp>)")
	fs.StringVar(&o.dashURL, "dash-url", "", "the dashboard URL (or TBDASH_URL)")
	fs.StringVar(&o.dashUser, "dash-user", "", "the dashboard login (or TBDASH_USER; default TADASH)")
	fs.StringVar(&o.dashPassFile, "dash-pass-file", "", "the dashboard password file (or TBDASH_PASS_FILE)")
	fs.StringVar(&o.mmaps, "mmaps", "", "the navmesh dir for slot setup")
	fs.StringVar(&o.slotsPath, "slots", "", "the slots file (default ~/.config/turtle-agent/slots.json)")
	fs.StringVar(&o.binary, "binary", "", "the agent binary slot setup copies (default: this executable)")
	fs.Var(&o.injects, "inject", "a fault to inject, repeatable (at most 4): kill-mt, runaway, add-pack or lag, each with @from=N and its keys")
	fs.BoolVar(&o.dryRun, "dry-run", false, "validate and print the phase plan, send nothing")
	if err := fs.Parse(args); err != nil {
		return o, exitUsage
	}
	if fs.NArg() != 1 {
		fmt.Fprintln(stderr, raidWalkUsage)
		return o, exitUsage
	}
	o.plan = fs.Arg(0)
	if !raidWalkPlanRe.MatchString(o.plan) {
		fmt.Fprintf(stderr, "raid-walk: %q is not a plan name (a-z, 0-9, _ and -)\n", o.plan)
		return o, exitUsage
	}
	if o.holder == "" {
		o.holder = "raid-walk-" + o.plan
	}
	if !leaseHolderRe.MatchString(o.holder) {
		fmt.Fprintf(stderr, "raid-walk: --holder must be 1-64 letters, digits, . _ -\n")
		return o, exitUsage
	}
	if !raidWalkSpeedRe.MatchString(speed) {
		fmt.Fprintf(stderr, "raid-walk: --speed wants 1.0 to 3.0 with one decimal, not %q\n", speed)
		return o, exitUsage
	}
	v, err := strconv.ParseFloat(speed, 64)
	if err != nil || v < 1.0 || v > 3.0 {
		fmt.Fprintf(stderr, "raid-walk: --speed wants 1.0 to 3.0 with one decimal, not %q\n", speed)
		return o, exitUsage
	}
	o.speed = v
	for _, c := range []struct {
		name string
		v    int
	}{{"--tier", o.tier}, {"--tier-override", o.tierOverride}} {
		if c.v != 0 && (c.v < 1 || c.v > 3) {
			fmt.Fprintf(stderr, "raid-walk: %s wants 1, 2 or 3, not %d\n", c.name, c.v)
			return o, exitUsage
		}
	}
	if o.tier != 0 && o.tierOverride != 0 {
		fmt.Fprintf(stderr, "raid-walk: --tier fills a raid the table leaves unknown and --tier-override replaces a known one; not both\n")
		return o, exitUsage
	}
	switch o.enter {
	case "auto", "near", "tele":
	default:
		fmt.Fprintf(stderr, "raid-walk: --enter wants auto, near or tele, not %q\n", o.enter)
		return o, exitUsage
	}
	if o.holdMinutes < 0 {
		fmt.Fprintf(stderr, "raid-walk: --hold-minutes must be 0 or more\n")
		return o, exitUsage
	}
	if o.maxRunMinutes < 1 {
		fmt.Fprintf(stderr, "raid-walk: --max-run-minutes must be 1 or more\n")
		return o, exitUsage
	}
	if o.pausedWait <= 0 {
		fmt.Fprintf(stderr, "raid-walk: --paused-wait must be above 0\n")
		return o, exitUsage
	}
	if o.resetWait <= 0 {
		fmt.Fprintf(stderr, "raid-walk: --reset-wait must be above 0\n")
		return o, exitUsage
	}
	if o.autoResume < 0 || o.autoResume > 3 {
		fmt.Fprintf(stderr, "raid-walk: --auto-resume wants 0 to 3\n")
		return o, exitUsage
	}
	if ttl := o.ttlMinutes(); ttl > 720 {
		fmt.Fprintf(stderr, "raid-walk: the lease would last %d min (max-run + hold + 30); the limit is 720: lower --max-run-minutes or --hold-minutes\n", ttl)
		return o, exitUsage
	}
	return o, exitOK
}

// ttlMinutes is the lease's length: the run ceiling, the hold and 30 minutes (spec 3.3 phase 2).
func (o *raidWalkOptions) ttlMinutes() int { return o.maxRunMinutes + o.holdMinutes + 30 }

// walkStop ends a phase with an exit code and the reason the summary carries.
type walkStop struct {
	code   int
	reason string
}

func (e *walkStop) Error() string { return e.reason }

func stopf(code int, format string, a ...any) error {
	return &walkStop{code: code, reason: fmt.Sprintf(format, a...)}
}

// stopInfo reads a phase error: a *walkStop carries its own code and reason.
func stopInfo(err error) (int, string) {
	var s *walkStop
	if errors.As(err, &s) {
		return s.code, s.reason
	}
	if err == nil {
		return exitOK, ""
	}
	return exitRaidWalkFailed, err.Error()
}

// walkAgent is the slot agent's JSON-RPC (S0's *slotClient in production).
type walkAgent interface {
	Call(ctx context.Context, method string, params, out any, timeout time.Duration) error
}

// walkDash is the dashboard API (A1's *tbdash.Client in production).
type walkDash interface {
	Login(ctx context.Context) error
	Commanders(ctx context.Context) (json.RawMessage, error)
	Fights(ctx context.Context, commander string) (json.RawMessage, error)
	Fight(ctx context.Context, key string) (json.RawMessage, error)
}

// walkEnv is everything the phases reach outside the two clients: the clock, the wait, the agent
// process, the lease and the in-process slot setup and teardown. Every field is replaced in tests.
type walkEnv struct {
	now        func() time.Time
	sleep      func(ctx context.Context, d time.Duration) error
	startAgent func(m teardownSlotManifest) (int, error)
	acquire    func(server, slot string, any bool, holder string, ttl time.Duration) (slots.Acquired, error)
	renew      func(server, slot, holder, leaseID string, ttl time.Duration) (slots.Lease, error)
	release    func(server, slot, holder, leaseID string, force bool) (slots.Lease, error)
	setup      func(args []string, stdout, stderr io.Writer) int
	teardown   func(args []string, stdout, stderr io.Writer) int
}

// walkDeps are the two clients the phases use; tests hand in fakes.
type walkDeps struct {
	openAgent func(dir string) (walkAgent, teardownSlotManifest, error)
	openDash  func(url, user, passFile string) (walkDash, error)
}

// defaultWalkEnv binds the environment to the slot config.
func defaultWalkEnv(o *raidWalkOptions) (walkEnv, walkDeps, error) {
	path := o.slotsPath
	if path == "" {
		path = slots.DefaultPath()
	}
	cfg, err := slots.Load(path)
	if err != nil {
		return walkEnv{}, walkDeps{}, err
	}
	store := slots.NewStore(cfg, path)
	env := walkEnv{
		now:        time.Now,
		sleep:      sleepCtx,
		startAgent: startSlotAgent,
		acquire:    store.Acquire,
		renew:      store.Renew,
		release:    store.Release,
		setup:      slotSetupCmd,
		teardown:   teardownCmd,
	}
	deps := walkDeps{
		openAgent: func(dir string) (walkAgent, teardownSlotManifest, error) {
			c, err := openSlotClient(dir)
			if err != nil {
				return nil, teardownSlotManifest{}, err
			}
			return c, c.Manifest(), nil
		},
		openDash: func(url, user, passFile string) (walkDash, error) {
			return tbdash.New(url, user, passFile)
		},
	}
	return env, deps, nil
}

// fillDefaults lets a test override only the fields it needs.
func (e *walkEnv) fillDefaults() {
	if e.now == nil {
		e.now = time.Now
	}
	if e.sleep == nil {
		e.sleep = sleepCtx
	}
	if e.startAgent == nil {
		e.startAgent = startSlotAgent
	}
}

func (d *walkDeps) fillDefaults() {
	if d.openAgent == nil {
		d.openAgent = func(dir string) (walkAgent, teardownSlotManifest, error) {
			c, err := openSlotClient(dir)
			if err != nil {
				return nil, teardownSlotManifest{}, err
			}
			return c, c.Manifest(), nil
		}
	}
	if d.openDash == nil {
		d.openDash = func(url, user, passFile string) (walkDash, error) {
			return tbdash.New(url, user, passFile)
		}
	}
}

// sleepCtx waits d or until ctx ends.
func sleepCtx(ctx context.Context, d time.Duration) error {
	if d <= 0 {
		return ctx.Err()
	}
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-t.C:
		return nil
	}
}

// startSlotAgent starts the slot's login in its own process group, its output going to <slot>/agent.out.
func startSlotAgent(m teardownSlotManifest) (int, error) {
	out, err := os.OpenFile(filepath.Join(m.Dir, "agent.out"), os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		return 0, err
	}
	cmd := exec.Command(m.Binary, "login", "--config", m.Settings, "--pid-file", m.PIDFile, m.Character)
	cmd.Stdout, cmd.Stderr = out, out
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	if err := cmd.Start(); err != nil {
		out.Close()
		return 0, err
	}
	go func() { _ = cmd.Wait(); out.Close() }()
	return cmd.Process.Pid, nil
}

// walk is one raid walk.
type walk struct {
	opts raidWalkOptions
	env  walkEnv
	deps walkDeps

	stdout io.Writer
	stderr io.Writer

	plan   *dungeon.Plan
	rows   planexport.Rows
	roster *raidwalk.Roster

	untilWords []string
	untilLabel string
	// runLabel and speedFellBack are the status lines' label facts for the summary (noteLabel).
	runLabel      string
	speedFellBack bool
	untilStep     int // the step of the start line's until label (0: unknown)
	fromWords     []string

	agent    walkAgent
	manifest teardownSlotManifest

	dash     walkDash
	dashNote string
	dashSeq  int

	outDir  string
	slotDir string
	logF    *os.File
	framesF *os.File

	tracker  *raidwalk.Tracker
	inj      walkInjector
	newNotes []string // the RUN notes read since the last injector Tick (it drains them)
	layout   raidwalk.LayoutSummary
	notes    []string
	fights   []raidwalk.FightSummary

	seenFights map[string]bool
	hostiles   map[string]hostileState
	botAt      map[string]botPos
	partyG     map[uint64]bool
	resetSince time.Time
	afterN     uint64
	eventsAt   uint64

	bossSet      map[uint32]bool
	bossRows     map[uint32]hostileState
	bossSeenAt   time.Time
	bossTargetAt time.Time

	stepFirst *dungeon.Point

	lastStatus    tbm.RunStatus
	haveStatus    bool
	dashAt        time.Time
	fightAt       time.Time
	lastFrameSave time.Time
	dashWarned    bool

	tierSource string // where the roster's tier came from (raidwalk.TierSource*)

	hires map[string]bool
	mt    string
	ots   []string
	rest  []string

	quietPatrols []raidwalk.QuietPatrol
	patrolGUIDS  []map[string]bool

	started    time.Time
	ended      time.Time
	runStarted bool
	runStopped bool
	lease      slots.Lease
	leaseLost  atomic.Bool
	cancel     context.CancelFunc

	endReason  string
	exitCode   int
	teardownWd raidwalk.TeardownSummary
	hiresLeft  []string
}

type hostileState struct {
	entry    uint32
	x, y, z  float32
	kind     string
	reaction string
	flags    string
	hppct    int
	target   uint64
	at       time.Time
}

type botPos struct {
	guid     packet.GUID
	x, y, z  float32
	inCombat bool
	mapID    uint32
	dead     bool
	at       time.Time
}

// raidWalkRun runs a parsed walk; tests call it directly with fakes.
func raidWalkRun(ctx context.Context, o raidWalkOptions, env walkEnv, deps walkDeps, stdout, stderr io.Writer) int {
	env.fillDefaults()
	deps.fillDefaults()
	w := &walk{opts: o, env: env, deps: deps, stdout: stdout, stderr: stderr,
		seenFights: map[string]bool{}, hostiles: map[string]hostileState{}, botAt: map[string]botPos{},
		partyG: map[uint64]bool{}, bossSet: map[uint32]bool{}, bossRows: map[uint32]hostileState{}, hires: map[string]bool{}}
	return w.main(ctx)
}

// main is phases 1-13 (spec 3.3).
func (w *walk) main(ctx context.Context) int {
	if err := w.validate(); err != nil {
		return w.reportBeforeOut(err)
	}
	if w.opts.dryRun {
		w.printPlan(w.stdout)
		return exitOK
	}
	if err := w.prepareOut(); err != nil {
		return w.reportBeforeOut(err)
	}
	defer w.closeOut()
	w.started = w.env.now()
	w.logLine(w.started, fmt.Sprintf("tier %d (%s) for %s", w.roster.Tier, w.tierSource, w.plan.Name))
	w.openDashboard(ctx)
	return w.phases(ctx)
}

// reportBeforeOut prints a phase-1 refusal (no output dir exists yet).
func (w *walk) reportBeforeOut(err error) int {
	code, reason := stopInfo(err)
	fmt.Fprintf(w.stderr, "raid-walk: %s\n", reason)
	return code
}

// validate is phase 1: no network, no lease, no files written.
func (w *walk) validate() error {
	path := filepath.Join(w.opts.dir, w.opts.plan+".json")
	p, err := dungeon.Load(path)
	if err != nil {
		return stopf(exitUsage, "%v", err)
	}
	if !p.IsRaid() {
		return stopf(exitUsage, "raid-walk is for raid plans; a 5-man uses dungeon_run observe")
	}
	if p.Raid == nil {
		return stopf(exitUsage, "plan %s has no raid block", p.Name)
	}
	w.plan = p

	if w.opts.hires != "" {
		raw, err := os.ReadFile(slots.ExpandTilde(w.opts.hires))
		if err != nil {
			return stopf(exitUsage, "%v", err)
		}
		r, err := raidwalk.ParseRoster(raw)
		if err != nil {
			return stopf(exitUsage, "%v", err)
		}
		if err := r.Validate(p); err != nil {
			return stopf(exitUsage, "%v", err)
		}
		w.roster = r
	} else {
		r, err := raidwalk.DefaultRoster(p)
		if err != nil {
			return stopf(exitUsage, "%v", err)
		}
		w.roster = r
	}

	tier, source, err := raidwalk.ResolveTier(p.Name, w.roster.Tier, w.opts.tier, w.opts.tierOverride)
	if err != nil {
		return stopf(exitUsage, "%v", err)
	}
	w.roster.Tier = tier
	w.tierSource = source
	if source == raidwalk.TierSourceOverride {
		if min, ok := raidwalk.MinimumTier(p.Name); ok {
			w.note("tier %d by --tier-override: a comparison, not a tier proof (raid %s's minimum is %d)", tier, p.Name, min)
		} else {
			w.note("tier %d by --tier-override: a comparison, not a tier proof (raid %s has no sourced tier)", tier, p.Name)
		}
	}

	words, label, err := raidwalk.ParseUntil(w.opts.until)
	if err != nil {
		return stopf(exitUsage, "%v", err)
	}
	w.untilWords, w.untilLabel = words, label
	if w.opts.from != "" {
		fw, err := raidwalk.ParseFrom(w.opts.from)
		if err != nil {
			return stopf(exitUsage, "%v", err)
		}
		w.fromWords = fw
	}

	if w.opts.enter == "tele" && w.plan.EntranceTele == "" {
		return stopf(exitUsage, "plan %s has no entrance_tele: --enter tele cannot place the hires (use auto or near)", w.plan.Name)
	}
	if w.plan.Portal == nil {
		return stopf(exitUsage, "plan %s has no portal: raid-walk cannot put the commander outside the entrance", w.plan.Name)
	}

	for _, e := range mapview.BossEntries(p) {
		w.bossSet[e] = true
	}
	for _, leg := range p.Legs {
		if leg.Boss != nil && leg.Boss.Hold != nil {
			for _, e := range leg.Boss.Hold.Entries {
				w.bossSet[e] = true
			}
		}
	}

	if w.opts.sql != "" {
		rows, notes, err := readRaidMigration(slots.ExpandTilde(w.opts.sql), w.plan.Name)
		if err != nil {
			return stopf(exitUsage, "%v", err)
		}
		w.rows = rows
		for _, n := range notes {
			w.note("%s", n)
		}
	} else {
		res, err := planexport.Build(p, planexport.Options{})
		if err != nil {
			return stopf(exitUsage, "plan %s: %v", p.Name, err)
		}
		w.rows = res.Rows
		w.note("step points from the plan JSON, not the deployed rows")
	}
	w.stepFirst = firstStepPoint(w.rows)

	if len(w.opts.injects) > 0 {
		if newWalkInjector == nil {
			return stopf(exitUsage, "this build has no fault injection (--inject)")
		}
		inj, err := newWalkInjector(w, w.opts.injects)
		if err != nil {
			return stopf(exitUsage, "%v", err)
		}
		w.inj = inj
	}

	if w.opts.patrols != "" {
		patrols, guids, mapID, err := readPatrolsFile(slots.ExpandTilde(w.opts.patrols))
		if err != nil {
			return stopf(exitUsage, "%v", err)
		}
		if mapID != 0 && mapID != w.plan.Map {
			return stopf(exitUsage, "--patrols is for map %d, plan %s is map %d", mapID, w.plan.Name, w.plan.Map)
		}
		w.quietPatrols, w.patrolGUIDS = patrols, guids
	}

	if w.opts.out != "" {
		out := slots.ExpandTilde(w.opts.out)
		if _, err := os.Lstat(out); err == nil {
			return stopf(exitUsage, "--out %s exists: pick a new dir", out)
		} else if !os.IsNotExist(err) {
			return stopf(exitUsage, "--out %s: %v", out, err)
		}
	}
	return nil
}

// prepareOut creates the output dir (0700) and its run.log (0600).
func (w *walk) prepareOut() error {
	dir := slots.ExpandTilde(w.opts.out)
	if dir == "" {
		dir = fmt.Sprintf("raid-walk-%s-%s", w.plan.Name, w.env.now().Format("20060102-150405"))
	}
	if _, err := os.Lstat(dir); err == nil {
		return stopf(exitUsage, "--out %s exists: pick a new dir", dir)
	} else if !os.IsNotExist(err) {
		return stopf(exitUsage, "--out %s: %v", dir, err)
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return stopf(exitUsage, "out dir: %v", err)
	}
	if err := os.Chmod(dir, 0o700); err != nil {
		return stopf(exitUsage, "out dir: %v", err)
	}
	w.outDir = dir
	f, err := os.OpenFile(filepath.Join(dir, "run.log"), os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		return stopf(exitUsage, "run.log: %v", err)
	}
	w.logF = f
	ff, err := os.OpenFile(filepath.Join(dir, "frames.jsonl"), os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		return stopf(exitUsage, "frames.jsonl: %v", err)
	}
	w.framesF = ff
	return nil
}

func (w *walk) closeOut() {
	if w.framesF != nil {
		_ = w.framesF.Close()
		w.framesF = nil
	}
	if w.logF != nil {
		_ = w.logF.Close()
		w.logF = nil
	}
}

// writeOut writes one output file (0600).
func (w *walk) writeOut(name string, data []byte) error {
	return os.WriteFile(filepath.Join(w.outDir, name), data, 0o600)
}

// note appends a note to the summary and run.log.
func (w *walk) note(format string, a ...any) {
	w.notes = append(w.notes, fmt.Sprintf(format, a...))
}

// logLine writes one line to run.log ("HH:MM:SS.mmm <text>"), control bytes escaped.
func (w *walk) logLine(now time.Time, text string) {
	if w.logF == nil {
		return
	}
	fmt.Fprintf(w.logF, "%s %s\n", now.Format("15:04:05.000"), escapeCtl(text))
}

// escapeCtl writes C0, DEL and C1 as \xNN so a raw protocol line stays one line.
func escapeCtl(s string) string {
	var b strings.Builder
	b.Grow(len(s))
	for i := 0; i < len(s); i++ {
		c := s[i]
		if c < 0x20 || c == 0x7f || (c >= 0x80 && c <= 0x9f) {
			fmt.Fprintf(&b, "\\x%02X", c)
			continue
		}
		b.WriteByte(c)
	}
	return b.String()
}

// openDashboard is the optional dashboard capture (spec 3.1): off without a URL and a password file,
// and a failed login is a warning only.
func (w *walk) openDashboard(ctx context.Context) {
	url := w.opts.dashURL
	if url == "" {
		url = os.Getenv("TBDASH_URL")
	}
	pass := w.opts.dashPassFile
	if pass == "" {
		pass = os.Getenv("TBDASH_PASS_FILE")
	}
	user := w.opts.dashUser
	if user == "" {
		user = os.Getenv("TBDASH_USER")
	}
	if user == "" {
		user = "TADASH"
	}
	if url == "" || pass == "" {
		w.dashNote = "off (no --dash-url/--dash-pass-file)"
		return
	}
	c, err := w.deps.openDash(url, user, pass)
	if err != nil {
		w.dashNote = "off (" + err.Error() + ")"
		return
	}
	if err := c.Login(ctx); err != nil {
		w.dashNote = dashboardWarning(err)
		return
	}
	w.dash = c
	w.dashNote = "on"
}

// dashboardWarning is the summary's dashboard note for a failed login; the password never reaches it.
func dashboardWarning(err error) string {
	var he *tbdash.HTTPError
	if errors.As(err, &he) {
		return fmt.Sprintf("login failed (HTTP %d)", he.Code)
	}
	return "login failed (" + err.Error() + ")"
}

// gm sends one GM line, but only after raidwalk.AllowedLine passes it: a refusal is a programming
// error that stops the walk and prints the line (spec 3.6).
func (w *walk) gm(ctx context.Context, line string) error {
	if !raidwalk.AllowedLine(line, w.hires) {
		return stopf(exitRaidWalkFailed, "raid-walk refused to send a GM line outside the allow-list: %s", line)
	}
	w.logLine(w.env.now(), "> "+line)
	if err := w.agent.Call(ctx, "gm", map[string]string{"text": line}, nil, 10*time.Second); err != nil {
		return stopf(exitRaidWalkFailed, "gm %s: %v", line, err)
	}
	return nil
}

// status is the agent's status RPC.
func (w *walk) status(ctx context.Context) (session.StatusInfo, error) {
	var st session.StatusInfo
	err := w.agent.Call(ctx, "status", nil, &st, 10*time.Second)
	return st, err
}

// party is the agent's party RPC.
func (w *walk) party(ctx context.Context) (session.PartyInfo, error) {
	var p session.PartyInfo
	err := w.agent.Call(ctx, "party", nil, &p, 10*time.Second)
	return p, err
}

// unitsAt asks for the hostile units within d yards of the agent.
func (w *walk) unitsAt(ctx context.Context, d float32) ([]objects.Unit, error) {
	var us []objects.Unit
	err := w.agent.Call(ctx, "units", map[string]any{"within": d, "hostile": true}, &us, 15*time.Second)
	if us == nil {
		us = []objects.Unit{}
	}
	return us, err
}

// commanderReq is the commander RPC's params (commander_methods.go).
type commanderReq struct {
	Op      string   `json:"op"`
	LeadBot string   `json:"lead_bot,omitempty"`
	Bots    []string `json:"bots,omitempty"`
	On      *bool    `json:"on,omitempty"`
	Verb    string   `json:"verb,omitempty"`
	Args    []string `json:"args,omitempty"`
	WaitMS  int      `json:"wait_ms,omitempty"`
}

type commanderReply struct {
	Sent  string    `json:"sent"`
	Reply tbm.Reply `json:"reply"`
}

// commander makes one commander RPC call.
func (w *walk) commander(ctx context.Context, p commanderReq) (commanderReply, error) {
	var out commanderReply
	timeout := 20 * time.Second
	if p.WaitMS > 0 {
		timeout = time.Duration(p.WaitMS)*time.Millisecond + 15*time.Second
	}
	err := w.agent.Call(ctx, "commander", p, &out, timeout)
	return out, err
}

// view polls the perception feed.
func (w *walk) view(ctx context.Context, afterN uint64, waitMS int) (uint64, *tbm.ViewFrame, error) {
	var out struct {
		Have  bool           `json:"have"`
		N     uint64         `json:"n"`
		Frame *tbm.ViewFrame `json:"frame"`
	}
	timeout := time.Duration(waitMS)*time.Millisecond + 10*time.Second
	err := w.agent.Call(ctx, "bot_view", map[string]any{"after_n": afterN, "wait_ms": waitMS}, &out, timeout)
	return out.N, out.Frame, err
}

// hireList is the hired names in hire order: MT, OT, rest.
func (w *walk) hireList() []string {
	var out []string
	if w.mt != "" {
		out = append(out, w.mt)
	}
	out = append(out, w.ots...)
	out = append(out, w.rest...)
	return out
}

// slotNumber is the leased slot as a number (0 when the id is not one).
func (w *walk) slotNumber() int {
	n, err := strconv.Atoi(w.lease.Slot)
	if err != nil {
		return 0
	}
	return n
}

// readRaidMigration reads the plan's rows from a deployed migration with planexport.ParseMigration (B1): it reads the
// three ai_dungeon tables and skips the hand-edited statements around them (a prep's ai_dungeon_boss block, REPLACE
// INTO for the edited rows), naming each skipped table once for the summary.
func readRaidMigration(path, plan string) (planexport.Rows, []string, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return planexport.Rows{}, nil, err
	}
	plans, notes, err := planexport.ParseMigration(string(raw))
	if err != nil {
		return planexport.Rows{}, nil, fmt.Errorf("%s: %v", path, err)
	}
	for _, r := range plans {
		if r.Plan.Plan == plan {
			return r, notes, nil
		}
	}
	return planexport.Rows{}, nil, fmt.Errorf("%s holds no plan %q", path, plan)
}

// readPatrolsFile reads a tb-patrols file (D1's internal/patrols) for the revive quiet check: each patrol's path, and
// the guid keys a member's unit row may carry (the recorded hex guids and the matched spawn guids, decimal and hex).
func readPatrolsFile(path string) ([]raidwalk.QuietPatrol, []map[string]bool, uint32, error) {
	f, err := patrols.Read(path)
	if err != nil {
		return nil, nil, 0, fmt.Errorf("--patrols: %v", err)
	}
	quiet := make([]raidwalk.QuietPatrol, 0, len(f.Patrols))
	guids := make([]map[string]bool, 0, len(f.Patrols))
	for _, p := range f.Patrols {
		quiet = append(quiet, raidwalk.QuietPatrol{ID: p.ID, Path: p.Path})
		set := map[string]bool{}
		for _, g := range p.GUIDs {
			set[strings.ToUpper(g)] = true
		}
		for _, g := range p.SpawnGUIDs {
			set[strconv.FormatUint(uint64(g), 10)] = true
			set[fmt.Sprintf("0X%016X", uint64(g))] = true
		}
		guids = append(guids, set)
	}
	return quiet, guids, f.Map, nil
}

// printPlan is --dry-run: every line the walk would send, with <hire> placeholders.
func (w *walk) printPlan(out io.Writer) {
	fmt.Fprintf(out, "raid-walk %s: dry run, nothing is sent\n", w.plan.Name)
	src := filepath.Join(w.opts.dir, w.plan.Name+".json")
	fmt.Fprintf(out, "plan %s, map %d, %d legs, %d steps, raid %d\n", src, w.plan.Map, len(w.plan.Legs), len(w.rows.Steps), w.plan.Raid.Size)
	ot := "-"
	if len(w.roster.Batches) > 1 && w.roster.Batches[1].Role == raidwalk.RoleOT {
		ot = w.roster.Batches[1].Hire
	}
	var rest []string
	for _, b := range w.roster.Batches {
		if b.Role == raidwalk.RoleRest {
			rest = append(rest, b.Hire)
		}
	}
	tierNote := ""
	if w.tierSource == raidwalk.TierSourceOverride {
		tierNote = "; comparison, not a tier proof"
	}
	fmt.Fprintf(out, "roster %d bots: mt %s, ot %s, rest %s, level %d, tier %d (%s)%s\n", w.roster.Total(),
		w.roster.Batches[0].Hire, ot, strings.Join(rest, "; "), w.roster.Level, w.roster.Tier, w.tierSource, tierNote)
	from := w.opts.from
	if from == "" {
		from = "-"
	}
	slot := w.opts.slot
	if slot == "" {
		slot = "any"
	}
	fmt.Fprintf(out, "until %s; from %s; enter %s; server %s slot %s holder %s\n", w.opts.until, from, w.opts.enter, w.opts.server, slot, w.opts.holder)
	outDir := slots.ExpandTilde(w.opts.out)
	if outDir == "" {
		outDir = "<out>"
	}
	fmt.Fprintf(out, "out %s\n", outDir)
	fmt.Fprintf(out, "phases:\n")
	fmt.Fprintf(out, "  1 validate: plan, roster, until, %d steps\n", len(w.rows.Steps))
	fmt.Fprintf(out, "  2 lease: %s %s --holder %s --ttl %dm\n", w.opts.server, slot, w.opts.holder, w.opts.ttlMinutes())
	bin := w.opts.binary
	if bin == "" {
		bin = "<bin>"
	}
	fmt.Fprintf(out, "  3 slot setup: slot setup %s <slot> --holder %s --dir %s/slot --binary %s --dungeons-from %s\n",
		w.opts.server, w.opts.holder, outDir, bin, w.opts.dir)
	fmt.Fprintf(out, "  4 login: %s/slot/turtle-agent login --config %s/slot/settings.json --pid-file %s/slot/agent.pid <commander>\n", outDir, outDir, outDir)
	fmt.Fprintf(out, "  5 gm: .go xyz %s %s %s %d\n", num32(w.plan.Portal.X), num32(w.plan.Portal.Y), num32(w.plan.Portal.Z), w.plan.Portal.Map)
	for _, line := range w.roster.HireLines() {
		fmt.Fprintf(out, "  6 gm: %s\n", line)
	}
	fmt.Fprintf(out, "  6 party: before and after every batch (the new names are the mt, ot and rest)\n")
	if w.roster.Total() > raidwalk.HireCap {
		fmt.Fprintf(out, "  6 hire cap: %d (the server hires %d of the %d asked; the summary notes it)\n",
			raidwalk.HireCap, raidwalk.HireCap, w.roster.Total())
	}
	fmt.Fprintf(out, "  6 gm: .bot gear raid <commander> tier %d\n", w.roster.Tier)
	fmt.Fprintf(out, "  7 commander: bind <hire> raid\n")
	fmt.Fprintf(out, "  7 commander: view on\n")
	switch w.opts.enter {
	case "tele":
		fmt.Fprintf(out, "  8 gm: .tele name <hire> %s (each hire)\n", w.plan.EntranceTele)
	default:
		fmt.Fprintf(out, "  8 commander: cmd enter near\n")
	}
	start := []string{w.plan.Name}
	start = append(start, w.fromWords...)
	start = append(start, w.untilWords...)
	if w.opts.speed != 1.0 {
		start = append(start, fmt.Sprintf("speed=%.1f", w.opts.speed))
	}
	fmt.Fprintf(out, "  9 commander: run start %s\n", strings.Join(start, " "))
	fmt.Fprintf(out, " 10 watch: events 5s, bot_view 2s, dashboard %s, ceiling %dm, paused wait %s, auto-resume %d\n",
		w.dashNoteOrOff(), w.opts.maxRunMinutes, w.opts.pausedWait, w.opts.autoResume)
	fmt.Fprintf(out, " 11 hold: %dm\n", w.opts.holdMinutes)
	fmt.Fprintf(out, " 12 commander: status; teardown --json %s/slot\n", outDir)
	fmt.Fprintf(out, " 13 summary: %s/summary.json, %s/summary.txt\n", outDir, outDir)
	if w.inj != nil {
		w.inj.PrintPlan(out)
	}
}

// dashNoteOrOff is the dry run's dashboard word (nothing was opened).
func (w *walk) dashNoteOrOff() string {
	if w.opts.dashURL == "" && os.Getenv("TBDASH_URL") == "" {
		return "off (no --dash-url/--dash-pass-file)"
	}
	return "on (login not tried in a dry run)"
}

// firstStepPoint is the revive point: step 1's point from the rows (spec 3.5). A step 1 without a position
// (a rest row at 0,0,0) falls back to the first positioned row, and no positioned row gives nil.
func firstStepPoint(rows planexport.Rows) *dungeon.Point {
	for _, s := range rows.Steps {
		if s.X == 0 && s.Y == 0 && s.Z == 0 {
			continue
		}
		p := dungeon.Point{X: s.X, Y: s.Y, Z: s.Z}
		return &p
	}
	return nil
}

// num32 prints a plan coordinate the way planexport does.
func num32(v float32) string { return strconv.FormatFloat(float64(v), 'f', -1, 32) }

// rowsSHA256 is the walk's rows as the deployed SQL, sha256 hex: the plan the run actually walked (spec 1.7).
func rowsSHA256(rows planexport.Rows) string {
	h := sha256.Sum256([]byte(planexport.WriteSQL([]planexport.Rows{rows})))
	return hex.EncodeToString(h[:])
}

// finish writes the summary (phase 13) and the one stdout line.
func (w *walk) finish(code int) {
	// A fault with verdict fail makes an otherwise clean walk exit 23 (spec 0.5); 18 and 19 keep their meaning.
	if code == exitOK && w.inj != nil && w.inj.Failed() {
		code = exitRaidWalkInjectFailed
	}
	// A bot the cleanup could not free is a blocking incident: 23 at least, whatever the walk's own reason.
	if w.inj != nil && w.inj.Blocking() && code < exitRaidWalkInjectFailed {
		code = exitRaidWalkInjectFailed
	}
	w.exitCode = code
	meta := raidwalk.SummaryMeta{
		Plan: w.plan.Name, Map: w.plan.Map, Server: w.opts.server, Slot: w.slotNumber(), Character: w.manifest.Character,
		Until: w.opts.until, From: w.opts.from, Speed: w.opts.speed, TestSpeed: w.opts.speed != 1.0,
		RowsSHA256: rowsSHA256(w.rows), UntilStep: w.untilStep, RunLabel: w.runLabel, SpeedFellBack: w.speedFellBack,
		Started: w.started, Ended: w.ended, EndReason: w.endReason, Fights: w.fights,
		Dashboard: w.dashNote, ExitCode: code, Notes: w.notes,
		TierSource: w.tierSource, TierComparison: w.tierSource == raidwalk.TierSourceOverride,
	}
	if w.inj != nil {
		meta.Injections, meta.InjectedDeaths = w.inj.Results()
		meta.Incidents = w.inj.Incidents()
	}
	if w.roster != nil {
		r := *w.roster
		meta.Roster = &r
	}
	meta.Layout = w.layout
	meta.Teardown = w.teardownWd
	sum := raidwalk.BuildSummary(w.tracker, meta)
	if raw, err := json.MarshalIndent(sum, "", "  "); err == nil {
		_ = w.writeOut("summary.json", append(raw, '\n'))
	}
	text := sum.Text()
	_ = w.writeOut("summary.txt", []byte(text))
	line := strings.SplitN(text, "\n", 2)[0]
	fmt.Fprintf(w.stdout, "%s; out %s\n", line, w.outDir)
}

// sortedNames sorts a name list, for stable output.
func sortedNames(in []string) []string {
	out := append([]string{}, in...)
	sort.Strings(out)
	return out
}

// commanderListedNames reads the bot names out of a `.bot commander status` reply: "You command <lead>'s group: A, B
// (dead), C." A server may split that sentence over several system lines, so a line after it is read too. ok is false
// when the reply carries no such line (then the names are unknown, not missing).
func commanderListedNames(reply tbm.Reply) (map[string]bool, bool) {
	names := map[string]bool{}
	found := false
	for _, line := range reply.Lines {
		if strings.HasPrefix(line, "TBM:") {
			continue
		}
		txt := line
		if i := strings.Index(txt, "group: "); i >= 0 {
			found = true
			txt = txt[i+len("group: "):]
		} else if !found {
			continue
		}
		for _, part := range strings.Split(txt, ",") {
			name := strings.TrimSpace(part)
			name = strings.TrimSuffix(name, ".")
			name = strings.TrimSpace(strings.TrimSuffix(name, "(dead)"))
			if name != "" {
				names[strings.ToLower(name)] = true
			}
		}
	}
	if !found {
		// An ACK with no bots is a group of none.
		if reply.Ack != nil && reply.Ack.Count == 0 {
			return names, true
		}
		return nil, false
	}
	return names, true
}

// readWalkManifest loads the slot dir's manifest with teardown's assertions, before the agent (and its token
// file) exists.
func readWalkManifest(dir string) (teardownSlotManifest, error) {
	raw, err := privfile.Read(filepath.Join(dir, "slot.json"), 64*1024)
	if err != nil {
		return teardownSlotManifest{}, fmt.Errorf("%s/slot.json missing or unreadable: run slot setup first", dir)
	}
	var m teardownSlotManifest
	if err := json.Unmarshal(raw, &m); err != nil || m.Server == "" || m.Slot == "" || m.Holder == "" || m.Port == 0 {
		return teardownSlotManifest{}, fmt.Errorf("%s/slot.json missing or unreadable: run slot setup first", dir)
	}
	if err := teardownCheckManifest(dir, &m); err != nil {
		return teardownSlotManifest{}, err
	}
	return m, nil
}

// teardownJSON is what `teardown --json` prints.
type teardownJSON struct {
	Server string `json:"server"`
	Slot   string `json:"slot"`
	Steps  []struct {
		Step   string `json:"step"`
		Status string `json:"status"`
		Detail string `json:"detail"`
	} `json:"steps"`
	Failed int `json:"failed"`
}

// parseTeardownJSON reads the teardown report; a report that does not parse is a failed teardown.
func parseTeardownJSON(raw []byte) (teardownJSON, bool) {
	var t teardownJSON
	if err := json.Unmarshal(bytes.TrimSpace(raw), &t); err != nil {
		return teardownJSON{Failed: 1}, false
	}
	return t, true
}
