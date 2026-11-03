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
    [--until boss|boss-done|none|step N|leg L] [--from leg L|step N] [--place] [--speed X] [--hires FILE] [--tier N|--tier-override N]
    [--enter auto|near|tele] [--hold-minutes M] [--max-run-minutes 90] [--paused-wait 10m] [--auto-resume K]
    [--reset-wait 5m] [--wipe-resume K] [--wipe-rest 10m] [--revive-at-raid=false] [--hire-settle 20s] [--patrols FILE] [--out DIR] [--dash-url URL] [--dash-user TADASH] [--dash-pass-file PATH]
    [--mmaps DIR] [--slots FILE] [--binary PATH] [--inject SPEC] [--dry-run]
       turtle-agent raid-walk report <dir> [--spell ID] [--clock-offset 1h] [--md FILE] [--json FILE] [--out DIR]`

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
	place         bool // --place: start the raid partway into its instance, at the --from start (spec 3)
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
	wipeResume    int
	wipeRest      time.Duration
	reviveAtRaid  bool          // --revive-at-raid (default on): a wipe recovery brings far hires to the revive point and waits for the raid to gather
	hireSettle    time.Duration // --hire-settle: the wait after the last hire; 0 skips it (the #1367 live proof)
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
	// `raid-walk report <dir>` reads a finished walk's folder; it sends nothing.
	if len(args) > 0 && args[0] == "report" {
		return raidWalkReportCmd(args[1:], stdout, stderr)
	}
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
	// The single-slot form takes the plan before its flags too (walks 12/13: Go's flag package stops
	// at the first non-flag, so the plan would strand every flag after it as a positional). A leading
	// non-flag moves to the end before parsing; flags-first callers (raid-suite, pair) start with a
	// flag and parse as before.
	var planFirst string
	if len(args) > 0 && !strings.HasPrefix(args[0], "-") {
		planFirst = args[0]
		moved := make([]string, 0, len(args))
		moved = append(moved, args[1:]...)
		moved = append(moved, args[0])
		args = moved
	}
	fs := flag.NewFlagSet("raid-walk", flag.ContinueOnError)
	fs.SetOutput(stderr)
	fs.Usage = func() { fmt.Fprintln(stderr, raidWalkUsage) }
	speed := "1.0"
	fs.StringVar(&o.server, "server", "m910q", "the slot server")
	fs.StringVar(&o.slot, "slot", "", "the slot id (default: any free slot)")
	fs.StringVar(&o.holder, "holder", "", "the lease holder (default raid-walk-<plan>)")
	fs.StringVar(&o.dir, "dir", "dungeons", "the folder of plan files")
	fs.StringVar(&o.sql, "sql", "", "the deployed migration: step numbers and the first point come from it")
	fs.StringVar(&o.until, "until", "boss", "boss (stops before it), boss-done (after the boss step), none, \"step N\" or \"leg L\"")
	fs.StringVar(&o.from, "from", "", "\"leg L\" or \"step N\" for the run leader")
	fs.Var(onOffFlag{&o.place}, "place", "with --from: put the raid at that start inside the instance, clearing the legs it skips (Eluna test toolkit: m910q or tortoise-test2 only)")
	fs.StringVar(&speed, "speed", "1.0", "the run's test speed, 1.0 to 3.0")
	fs.StringVar(&o.hires, "hires", "", "the tb-raid-roster v1 file (default: from the plan's raid block)")
	fs.IntVar(&o.tier, "tier", 0, "the hire gear tier (1-3) of a raid the committed table leaves unknown")
	fs.IntVar(&o.tierOverride, "tier-override", 0, "the hire gear tier (1-3) in place of the raid's minimum: a comparison, not a tier proof")
	fs.StringVar(&o.enter, "enter", "auto", "auto, near or tele")
	fs.IntVar(&o.holdMinutes, "hold-minutes", 0, "keep the raid bound for this many minutes after the run")
	fs.IntVar(&o.maxRunMinutes, "max-run-minutes", 90, "the ceiling from run start to the end")
	fs.DurationVar(&o.pausedWait, "paused-wait", 10*time.Minute, "how long a pause that is not a regroup may last")
	fs.IntVar(&o.wipeResume, "wipe-resume", 0, "revive, rest and resume a wipe or near-wipe stop this many times per walk (0-3; 0 ends the walk as before)")
	fs.IntVar(&o.autoResume, "auto-resume", 0, "resume a regroup pause this many times per step (0-3)")
	fs.DurationVar(&o.resetWait, "reset-wait", 5*time.Minute, "how long a wipe may wait for the instance to reset")
	fs.DurationVar(&o.wipeRest, "wipe-rest", 10*time.Minute, "how long a revived raid rests out of combat before the run resumes")
	fs.BoolVar(&o.reviveAtRaid, "revive-at-raid", true, "a wipe recovery revives the raid AT the revive point: a hire revived more than 100 yd away is summoned to it, and the resume waits up to 60s for every hire to be in range and out of combat (false: the old revive in place)")
	fs.DurationVar(&o.hireSettle, "hire-settle", hireSettleWait, "how long to wait after the last hire for its join summons before the refit and the bind (0 skips the wait)")
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
	if fs.NArg() == 0 {
		fmt.Fprintln(stderr, raidWalkUsage)
		return o, exitUsage
	}
	if fs.NArg() > 1 {
		extra := fs.Arg(1)
		if planFirst != "" {
			// The plan moved to the end, so parsing stopped at the first extra: it is Arg(0).
			extra = fs.Arg(0)
		}
		fmt.Fprintf(stderr, "raid-walk: unexpected argument %q\n", extra)
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
	if o.wipeResume < 0 || o.wipeResume > 3 {
		fmt.Fprintf(stderr, "raid-walk: --wipe-resume wants 0 to 3\n")
		return o, exitUsage
	}
	if o.wipeRest < 30*time.Second {
		fmt.Fprintf(stderr, "raid-walk: --wipe-rest must be 30s or more\n")
		return o, exitUsage
	}
	if o.autoResume < 0 || o.autoResume > 3 {
		fmt.Fprintf(stderr, "raid-walk: --auto-resume wants 0 to 3\n")
		return o, exitUsage
	}
	if o.hireSettle < 0 {
		fmt.Fprintf(stderr, "raid-walk: --hire-settle must be 0 or more\n")
		return o, exitUsage
	}
	if ttl := o.ttlMinutes(); ttl > 720 {
		fmt.Fprintf(stderr, "raid-walk: the lease would last %d min (max-run + hold + 30); the limit is 720: lower --max-run-minutes or --hold-minutes\n", ttl)
		return o, exitUsage
	}
	// --place (spec 3): it needs a start, it is not combined with the fault injector, and it is for the test servers
	// whose Eluna toolkit can move a raid.
	if o.place {
		if o.from == "" {
			fmt.Fprintf(stderr, "raid-walk: --place needs --from (\"leg L\" or \"step N\"): it places the raid at that start\n")
			return o, exitUsage
		}
		if len(o.injects) > 0 {
			fmt.Fprintf(stderr, "raid-walk: --place and --inject are not combined yet (the injector assumes the commander in another copy)\n")
			return o, exitUsage
		}
		if !placeServers[o.server] {
			fmt.Fprintf(stderr, "raid-walk: --place uses the Eluna test toolkit: test servers only (%s)\n", strings.Join(placeServerList(), ", "))
			return o, exitUsage
		}
		if o.sql == "" {
			// Q1 changed by the lead (review round 1, item 9): the placed start and the corridor must be the deployed
			// rows, or a step-count mismatch fails the walk after a placement has cost a hire.
			fmt.Fprintf(stderr, "raid-walk: --place needs --sql (the deployed migration): the start step and the corridor are checked against those rows\n")
			return o, exitUsage
		}
	}
	return o, exitOK
}

// placeServers are the servers whose Eluna test toolkit can move a raid (spec 3). --place refuses every other one.
var placeServers = map[string]bool{"m910q": true, "tortoise-test2": true}

// placeServerList is the place servers, sorted, for the refusal.
func placeServerList() []string {
	out := make([]string, 0, len(placeServers))
	for s := range placeServers {
		out = append(out, s)
	}
	sort.Strings(out)
	return out
}

// onOffFlag is --place: a bare `--place` turns it on, `--place=off` turns it off. The pair form writes the word after
// a slot prefix (--place 2=on), which the half then receives as --place=on.
type onOffFlag struct{ v *bool }

func (f onOffFlag) String() string {
	if f.v != nil && *f.v {
		return "on"
	}
	return "off"
}

func (f onOffFlag) Set(s string) error {
	switch strings.ToLower(s) {
	case "", "on", "true", "1":
		*f.v = true
	case "off", "false", "0":
		*f.v = false
	default:
		return fmt.Errorf("--place wants on or off, not %q", s)
	}
	return nil
}

// IsBoolFlag lets a bare `--place` stand without a value.
func (f onOffFlag) IsBoolFlag() bool { return true }

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
	// bossDoneStep is the boss step --until boss-done resolved to (0: not asked). The server must show the step count of w.rows, else the
	// step numbers are not the ones the clause was computed from.
	bossDoneStep int
	// placeShortened says the --place route guard shortened the until (F5, D8): untilLabel is then the effective
	// clause and opts.until the one the user asked for, and the dry run and the summary print both.
	placeShortened bool
	fromWords      []string

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
	// pendingFights are fights listed but whose detail is not loaded yet: fight id to its index in fights.
	pendingFights map[string]int
	hostiles      map[string]hostileState
	botAt         map[string]botPos
	partyG        map[uint64]bool
	resetSince    time.Time
	afterN        uint64
	eventsAt      uint64

	bossSet map[uint32]bool
	// bossRule decides which frame rows are boss evidence (raidwalk.BossRows: a `first`-only entry counts within
	// raidwalk.FirstAddBossRadius of its boss's plan position; walk 19 test2 s4: trash Zulian Tigers 11361 on
	// Venoxis's leg latched bossLatchAt and blocked the wipe recovery).
	bossRule     *raidwalk.BossRows
	bossRows     map[uint32]hostileState
	bossSeenAt   time.Time
	bossTargetAt time.Time
	// bossToolkitOff says the toolkit corpse read (bossCorpse) failed once: the walk stops asking, so a server
	// without the toolkit costs one bounded read and not a timeout on every pause tick.
	bossToolkitOff bool
	// bossAdds are the entries the frames have shown more than one live unit of at once (trackFrame): adds that
	// exist several at a time, never the boss reborn, so bossUnits leaves them out of the kill evidence.
	bossAdds map[uint32]bool
	// bossAttemptAt is when the current boss attempt began, per unit entry: the first time the frames showed
	// that unit engaged in this attempt (trackFrame, the tracker's own latch judgement). bossKilled accepts a
	// dead frame row of the family only at or after it, so a 0 % row an EARLIER attempt left in w.hostiles
	// (never pruned: a reset or a respawn leaves it there) is no evidence on a later pause (review round 3).
	// A wipe recovery clears it; a new attempt's first sighting sets it.
	bossAttemptAt map[uint32]time.Time
	// bossAliveSeen is when the frames last showed the boss unit GUID (key of w.hostiles) alive in the
	// current attempt: trackFrame records every live boss-unit row here (revive r6). bossKilled's frame
	// corpse and toolkit corpse both count only for a guid the frames showed alive in THIS attempt before
	// its death, and the toolkit's wait needs a second read at least 15 s later still dead with no alive
	// frame for that guid in between (Thekal's fake death: phase 1 dies and is raised again). A new
	// attempt (resetBossAliveSeen) and a wipe recovery clear it with the attempt.
	bossAliveSeen map[string]time.Time
	// bossCorpseSeen is when the toolkit first reported the boss unit GUID (key of w.hostiles) dead in
	// the current attempt: the wait's first sighting (revive r6). A live frame for that guid clears it,
	// as does a wipe recovery.
	bossCorpseSeen map[string]time.Time
	// bossGUIDs is the guid -> plan boss entry of the units the frames showed with a plan boss's OWN entry
	// (trackFrame): a script that changes a creature's entry (core UpdateEntry) keeps its guid, so a unit whose
	// guid the frames showed as the boss entry is the boss whatever entry it now reads (spec 6, D5 b, #1453).
	// A guid in it is never an add; bossForms keeps the entries that guid was seen with, one per change.
	bossGUIDs map[string]uint32
	bossForms map[string][]uint32
	// bossTracks is one record per boss step the run's own statuses showed (observeBossStatus): the last note seen
	// while the run stood there, the `boss down` note that leaves it, and the skip the walk sent for that step
	// (bossSkipStep). Keyed by the step, or by the boss's entry when the step is unknown.
	bossTracks map[bossKey]*bossStepTrack
	// bossMismatch is the steps whose boss-kind status found no boss row at that step: the rows the walk reads are
	// not the deployed ones (without --sql they are the plan JSON's), so the step's kill record is skipped and the
	// reason noted once.
	bossMismatch map[int]bool

	stepFirst *dungeon.Point
	// startStep is the step the run starts at (the placement's staged start, else the --from step): the revive's own
	// step, and the start_step of a revive refusal (D-D).
	startStep int
	// reviveRefusal is the refused revive (D-D): set when the revive point never went quiet and the walk ends with
	// "not revived"; nil otherwise.
	reviveRefusal *raidwalk.ReviveRefusal
	// wipeAt is when the last wipe (or near-wipe stop) was seen: the anchor of a self-regroup's D-C cap (review round
	// 1, Opus M2).
	wipeAt time.Time

	lastStatus tbm.RunStatus
	haveStatus bool
	// pullJoins tracks the pull the walk is counting joiners for: the pull line's
	// step and time, the pulled pack's guids and the engaged guids seen since.
	// The pack is the pulled guid plus the plan pack's other members: the guids
	// whose frame position stands within the pull row's radius of its centroid
	// (pullRow), seeded once on the first frame of the window (pullPackSeeded).
	// Counted once, 10 s after the pull: pullJoinsAt is zero afterwards.
	pullJoinsAt    time.Time
	pullJoinsStep  int
	pullSeed       packet.GUID
	pullRow        raidwalk.PackRow
	pullPackSeeded bool
	pullPack       map[packet.GUID]bool
	pullJoinsSeen  map[packet.GUID]bool
	// pullJoins is the per-pull joiner record the report reads: one per pull
	// step, in first-sight order. Written when the 10 s pull window closes.
	pullJoins []raidwalk.PullJoiners
	// Review round 3: statusSeq counts every received run status; resumeRun
	// captures it before issuing the resume (resumeStatusSeq) so recoveredBoss
	// can tell a fresh post-resume status from a retained pre-resume one.
	statusSeq       uint64
	resumeStatusSeq uint64
	// runLines are the recent running status lines pollOnce read (newest last). Walk 18 r5: a
	// poll can carry several status lines of the same incident (live TB sends the "regrouping by
	// itself" note and then "regroup: waiting ... reviving" for the same step in one page), so the
	// wipe's self-regroup rule and the rest's went-on rule must see every line, not only the last.
	runLines []runObs
	// pollSeq0 is statusSeq before the page pollOnce is reading (it sets it). Walk 19 r6: handleWipe's
	// self-regroup rule reads it to tell a line of the wipe's own events page from an older one.
	pollSeq0      uint64
	dashAt        time.Time
	fightAt       time.Time
	lastFrameSave time.Time
	dashWarned    bool
	framesWarned  bool // the first frames.jsonl write error is noted once
	// Review round 2: the boss evidence the recovery needs. bossReadAt is when the last
	// dashboard fights/boss read succeeded (zero: never), bossEngagedAt when it last
	// showed an engaged boss, bossLatchAt when the frames last showed an ENGAGED plan-boss row (alive, and a target or below 100 %), bossCheckErr
	// the last read's error (nil: the read worked). fightRows is the last good read.
	bossReadAt    time.Time
	bossEngagedAt time.Time
	bossLatchAt   time.Time
	bossLatchHit  uint32 // the entry of the row that last set bossLatchAt (for the skip note)
	bossCheckErr  error
	fightRows     []walkFightRow

	tierSource string // where the roster's tier came from (raidwalk.TierSource*)

	hires map[string]bool
	mt    string
	ots   []string
	rest  []string

	quietPatrols []raidwalk.QuietPatrol
	patrolGUIDS  []map[string]bool

	// The --place start (spec 4/5): the plan from validate, the raid's instance id read at the place, the commander's
	// last `.go xyz` in the copy (the clear's distance rule), the quiet read's near list, and what the phase reports.
	place         *raidwalk.PlacePlan
	placeInst     float64
	placePos      [3]float32
	placeHavePos  bool
	placeStayed   bool
	placeSum      *raidwalk.PlaceSummary
	placeRemoved  int
	placeSkipped  []raidwalk.ClearCircle // the corridor circles placeClear refused (the extra round keeps away from them)
	placeSummoned []string

	started     time.Time
	ended       time.Time
	runStarted  bool
	runStopped  bool
	wipeResumes int // wipes and near-wipe stops recovered so far (--wipe-resume bounds them)
	// --revive-at-raid bookkeeping (raid_walk_reviveraid.go): the point the commander stood at for the last revive, and
	// the distinct hires brought back to it, not reached by two summons, and still missing when a resume went out.
	reviveSpot                                         *dungeon.Point
	reviveBrought, reviveUnreached, reviveGatherMissed map[string]bool
	lease                                              slots.Lease
	leaseLost                                          atomic.Bool
	cancel                                             context.CancelFunc

	endReason  string
	exitCode   int
	teardownWd raidwalk.TeardownSummary
	hiresLeft  []string
}

// bossKey identifies one boss step's record: the step when the run's status gave one (the row the run stood on),
// else the boss's entry (a skip the pause path sent for a step the rows do not name).
type bossKey struct {
	step  int
	entry uint32
}

// bossStepTrack is one boss step's record (spec 6, D5): the boss's entry, whether the run reported the encounter
// engaged (its status kind was boss), whether the kill holds (`boss down` note or the walk's own skip), the guid
// the frames showed the boss with and the entries that guid read while this step was tracked. The summary lists one
// record per boss step; the last engaged one also fills the summary's single boss_kill field.
type bossStepTrack struct {
	entry    uint32
	step     int
	engaged  bool
	killed   bool
	skipped  bool
	note     string
	evidence string
	guid     string
	forms    []uint32
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

// runObs is one running status line the walk read: its step, what it said and the status sequence
// it arrived at (walk 18 r5).
type runObs struct {
	seq  uint64
	step int
	note string
	at   time.Time
}

// raidWalkRun runs a parsed walk; tests call it directly with fakes.
func raidWalkRun(ctx context.Context, o raidWalkOptions, env walkEnv, deps walkDeps, stdout, stderr io.Writer) int {
	env.fillDefaults()
	deps.fillDefaults()
	w := &walk{opts: o, env: env, deps: deps, stdout: stdout, stderr: stderr,
		seenFights: map[string]bool{}, pendingFights: map[string]int{}, hostiles: map[string]hostileState{}, botAt: map[string]botPos{},
		partyG: map[uint64]bool{}, bossSet: map[uint32]bool{}, bossRows: map[uint32]hostileState{}, hires: map[string]bool{},
		bossAttemptAt: map[uint32]time.Time{}, bossAliveSeen: map[string]time.Time{}, bossCorpseSeen: map[string]time.Time{},
		bossGUIDs: map[string]uint32{}, bossForms: map[string][]uint32{},
		bossTracks: map[bossKey]*bossStepTrack{}}
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

	// The rows come before the until clause: `boss-done` and `--place` are resolved against them (spec 4/5.2).
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
	w.bossRule = raidwalk.NewBossRows(p)

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
	// The step the run starts at: with --place the staged start (spec 4.2), else the --from step. It is what
	// `--until boss-done` is resolved from (spec 5.2). A plain --from (no --place, no boss-done) is NOT resolved
	// against the rows: it goes to the server exactly as before, so a step past the plan's own export is not refused
	// here (review round 1, item 3).
	startStep := 1
	// untilBase is the step boss and boss-done untils resolve from: the start step, except when the place start
	// fell back to an earlier leg (H2: the target leg's first row, so the until answers the target leg's boss).
	untilBase := 1
	if w.opts.from != "" && (w.opts.place || raidwalk.IsBossDone(w.opts.until)) {
		n, err := raidwalk.FromStartStep(w.rows, w.fromWords)
		if err != nil {
			return stopf(exitUsage, "%v", err)
		}
		startStep = n
		untilBase = n
	}
	if w.opts.place {
		step, leg, fallbackFrom, door, err := raidwalk.PlaceStart(w.rows, w.fromWords)
		if err != nil {
			return stopf(exitUsage, "%v", err)
		}
		if door {
			w.note("place: leg %s is the first leg; starting from the door", leg)
		} else {
			if fallbackFrom != "" {
				w.note("place: leg %s has no quiet row; placing at step %d (leg %s)", fallbackFrom, step, leg)
			}
			p, ok := raidwalk.PlacePoint(w.rows, step)
			if !ok {
				return stopf(exitUsage, "--place: step %d of %s has no point", step, w.plan.Name)
			}
			w.fromWords = []string{"from", "step", strconv.Itoa(step)}
			startStep = step
			w.place = &raidwalk.PlacePlan{Start: step, Leg: leg, Point: p, Skips: raidwalk.PlaceSkips(w.plan)}
		}
		// H2: a fallback start stands before the target leg, so boss and boss-done untils resolve from the
		// target leg's first row, never from the fallback start (the raid meets the earlier rows on its way in).
		if fallbackFrom != "" {
			untilBase = raidwalk.PlaceUntilBase(w.rows, fallbackFrom, startStep)
		}
	}
	w.startStep = startStep
	if raidwalk.IsBossDone(w.opts.until) {
		clause, boss, err := raidwalk.BossDoneUntil(w.rows, untilBase)
		if err != nil {
			return stopf(exitUsage, "%v", err)
		}
		words, label, err := raidwalk.ParseUntil(clause)
		if err != nil {
			return stopf(exitUsage, "%v", err)
		}
		w.untilWords, w.untilLabel, w.bossDoneStep = words, label, boss
		w.note("--until boss-done: the boss is step %d of %d, the walk goes with %q", boss, len(w.rows.Steps), clause)
	} else {
		words, label, err := raidwalk.ParseUntil(w.opts.until)
		if err != nil {
			return stopf(exitUsage, "%v", err)
		}
		w.untilWords, w.untilLabel = words, label
	}
	if w.place != nil {
		// H2: the corridor runs from the TARGET leg's first row, so a boss until stops at the target
		// leg's boss: resolve it from the until base, never from the fallback start. Other untils name
		// their own row, so they resolve as usual.
		corridorStart := startStep
		if len(w.untilWords) == 2 && w.untilWords[0] == "until" && w.untilWords[1] == "boss" {
			corridorStart = untilBase
		}
		stop, err := raidwalk.PlaceStop(w.rows, w.untilWords, corridorStart)
		if err != nil {
			return stopf(exitUsage, "%v", err)
		}
		// A BEHIND boss or hold row of an earlier leg on the route is alive in the raid's fresh copy: when the route
		// passes one in a later leg, the run shortens its until to stop before that segment (D8). In the start step's
		// own leg the placement is refused, as PlaceClear's own guard does. A hit on the last segment leaves until at
		// the stop, so hit says the run must shorten anyway (an `--until none` run, or a boss-done that became none:
		// its walk would otherwise walk into that segment).
		until, routeEnd, gnote, refusal, hit := raidwalk.PlaceBossGuard(w.rows, startStep, stop)
		if refusal != "" {
			return stopf(exitUsage, "%v", refusal)
		}
		if hit && (until != stop || len(w.untilWords) == 0) {
			if w.bossDoneStep != 0 && until <= w.bossDoneStep {
				return stopf(exitUsage, "--place: %s; that is before the boss-done step %d", gnote, w.bossDoneStep)
			}
			words, label, err := raidwalk.ParseUntil(fmt.Sprintf("step %d", until))
			if err != nil {
				return stopf(exitUsage, "%v", err)
			}
			w.untilWords, w.untilLabel = words, label
			w.placeShortened = true
			w.note("place: %s", gnote)
		}
		clear := raidwalk.PlaceClear(w.rows, w.plan, startStep, routeEnd)
		if clear.Refusal != "" {
			return stopf(exitUsage, "%v", clear.Refusal)
		}
		// Stop is the until the dry run and the summary print; the corridor ended at routeEnd.
		w.place.Stop, w.place.Circles, w.place.Notes, w.place.Ahead, w.place.Behind = until, clear.Circles, clear.Notes, clear.Ahead, clear.Behind
		for _, n := range clear.Notes {
			w.note("place: %s", n)
		}
		// Before any GM command: the toolkit's skip= cap and its 200-char line cap (review round 1, item 11). A plan
		// that overflows either would be refused by AllowedTT at the first clearto, deep inside the place phase.
		if len(w.place.Skips) > raidwalk.MaxSkipEntries {
			return stopf(exitUsage, "--place: the plan's legs' skip lists hold %d entries; the toolkit's skip= takes at most %d", len(w.place.Skips), raidwalk.MaxSkipEntries)
		}
		if long, n := placeTTMax(w.place, w.plan.Map); n > raidwalk.AllowedTTMax {
			return stopf(exitUsage, "--place: the longest toolkit line is %d chars, over the toolkit's %d: %s", n, raidwalk.AllowedTTMax, long)
		}
	}

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
	if !raidwalk.AllowedLine(line, w.hires, w.plan.Map) {
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
	untilLine := w.opts.until
	if w.placeShortened {
		untilLine = fmt.Sprintf("%s (the place shortens it to %s)", untilLine, w.untilLabel)
	}
	fmt.Fprintf(out, "until %s; from %s; enter %s; server %s slot %s holder %s\n", untilLine, from, w.opts.enter, w.opts.server, slot, w.opts.holder)
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
	if w.place != nil {
		fmt.Fprintf(out, "  5b gm: .instance unbind %d\n", w.plan.Map)
	}
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
	if w.place != nil {
		p := w.place.Point
		fmt.Fprintf(out, "  8b place: start step %d%s at %s %s %s; until step %d; %d circles; skip %s\n",
			w.place.Start, legWords(w.place.Leg), num32(p.X), num32(p.Y), num32(p.Z), w.place.Stop,
			len(w.place.Circles), raidwalk.SkipWords(w.place.Skips))
		for _, c := range w.place.Circles {
			unit := ""
			if c.PerUnit {
				unit = fmt.Sprintf(" unit %d", c.Unit)
			}
			sweep := ""
			if c.Sweep {
				sweep = " (sweep)"
			}
			fmt.Fprintf(out, "  8b clear: row %d at %s %s %s r %s%s%s\n", c.Step, num32(c.X), num32(c.Y), num32(c.Z), num32(c.Radius), unit, sweep)
		}
		for _, n := range w.place.Notes {
			fmt.Fprintf(out, "  8b note: %s\n", n)
		}
		fmt.Fprintf(out, "  8b commander: .gm on; .appear <mt>; q inst, q group, q floor, q units, clearto, quiet, place <x> <y> <z> spread=5 of=<mt>; frames; cmd stay; cmd follow\n")
	}
	start := []string{w.plan.Name}
	start = append(start, w.fromWords...)
	start = append(start, w.untilWords...)
	if w.opts.speed != 1.0 {
		start = append(start, fmt.Sprintf("speed=%.1f", w.opts.speed))
	}
	fmt.Fprintf(out, "  9 commander: run start %s\n", strings.Join(start, " "))
	fmt.Fprintf(out, " 10 watch: events 5s, bot_view 2s, dashboard %s, ceiling %dm, paused wait %s, auto-resume %d, wipe-resume %d, wipe rest %s\n",
		w.dashNoteOrOff(), w.opts.maxRunMinutes, w.opts.pausedWait, w.opts.autoResume, w.opts.wipeResume, w.opts.wipeRest)
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

// revivePoint is where a revive puts the raid: the place point with --place (spec 4.4), else the plan's first point.
// The entrance fallback is dropped with --place: it is behind the legs the placement skipped.
func (w *walk) revivePoint() *dungeon.Point {
	if w.place != nil {
		p := w.place.Point
		return &p
	}
	return w.stepFirst
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
	// The tracker's one-shot latch is the fallback when the run's statuses never named a boss step's kind as boss.
	// It is inserted before the summary reads the tracks, so the single boss_kill field and the boss_kills list both
	// carry it; a boss entry none of the rows is a boss row of (bossStepOf 0) is not recorded (L4).
	if w.tracker != nil {
		if entry, _, ok := w.tracker.BossEngaged(); ok {
			if s := w.bossStepOf(entry); s > 0 {
				w.bossTrackAt(s, entry).engaged = true
			}
		}
	}
	untilEffective := ""
	if w.placeShortened {
		untilEffective = w.untilLabel
	}
	meta := raidwalk.SummaryMeta{
		Plan: w.plan.Name, Map: w.plan.Map, Server: w.opts.server, Slot: w.slotNumber(), Character: w.manifest.Character,
		Until: w.opts.until, UntilEffective: untilEffective, From: w.opts.from, Speed: w.opts.speed, TestSpeed: w.opts.speed != 1.0,
		RowsSHA256: rowsSHA256(w.rows), UntilStep: w.untilStep, RunLabel: w.runLabel, SpeedFellBack: w.speedFellBack,
		Started: w.started, Ended: w.ended, EndReason: w.endReason, Fights: w.fights,
		Dashboard: w.dashNote, ExitCode: code, Notes: w.notes,
		Place:         w.placeSum,
		BossKill:      w.bossKillSummary(),
		BossKills:     w.bossKillSummaries(),
		ReviveRefusal: w.reviveRefusal,
		TierSource:    w.tierSource, TierComparison: w.tierSource == raidwalk.TierSourceOverride,
		HireSettle: w.opts.hireSettle, SettleShort: w.opts.hireSettle < hireSettleWait,
		WipeResumes:   w.wipeResumes,
		ReviveBrought: len(w.reviveBrought), ReviveUnreached: len(w.reviveUnreached), ReviveGatherMissed: len(w.reviveGatherMissed),
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
	if raw, err := json.MarshalIndent(w.pullJoinsOrEmpty(), "", "  "); err == nil {
		_ = w.writeOut(raidwalk.PullJoinersFName, append(raw, '\n'))
	}
	text := sum.Text()
	_ = w.writeOut("summary.txt", []byte(text))
	line := strings.SplitN(text, "\n", 2)[0]
	fmt.Fprintf(w.stdout, "%s; out %s\n", line, w.outDir)
}

// pullJoinsOrEmpty is the walk's per-pull joiner record: never nil, so the
// file the report reads is always a list.
func (w *walk) pullJoinsOrEmpty() []raidwalk.PullJoiners {
	if w.pullJoins == nil {
		return []raidwalk.PullJoiners{}
	}
	return w.pullJoins
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

// teardownReason is the first failing teardown step with its detail ("gone: still in
// view after 10 s: ..."), so summary.json says WHICH step failed instead of only that
// one did. The derived lease-kept line is skipped: it only repeats the game-side fail.
func teardownReason(t teardownJSON) string {
	for _, s := range t.Steps {
		if s.Status != "fail" && s.Status != "ending" {
			continue
		}
		if s.Step == "lease" && strings.HasPrefix(s.Detail, "kept: game-side teardown failed") {
			continue
		}
		if s.Detail != "" {
			return s.Step + ": " + s.Detail
		}
		return s.Step + ": failed"
	}
	return "teardown failed"
}
