// plan_scout_live.go: the scout's live pass (plan-scout spec 5.6). One GM character on a test server: lease and slot
// setup like raid-walk, login, GM mode, unbind, a fresh instance, the server's path query for every hop, `q units`/`q los`
// reads at the stands and pulls, and a real walk of the risky hops within the budget. No bot is hired, bound or ordered;
// every GM and toolkit line passes plan_scout_allow.go first. Nothing here writes the report: it returns the LiveData
// planscout.MergeLive merges and the exit code (0 done, 28 aborted, 18 refused, 15 slot, 26 instance cap).
package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"math"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync/atomic"
	"time"

	"github.com/pjerra/turtle-agent/internal/config"
	"github.com/pjerra/turtle-agent/internal/dungeon"
	"github.com/pjerra/turtle-agent/internal/events"
	"github.com/pjerra/turtle-agent/internal/objects"
	"github.com/pjerra/turtle-agent/internal/packet"
	"github.com/pjerra/turtle-agent/internal/planscout"
	"github.com/pjerra/turtle-agent/internal/raidwalk"
	"github.com/pjerra/turtle-agent/internal/session"
	"github.com/pjerra/turtle-agent/internal/slots"
	"github.com/pjerra/turtle-agent/internal/tbm"
	"github.com/pjerra/turtle-agent/internal/worldspawn"
)

const (
	// scoutLogName is the live log's file in the report folder.
	scoutLogName = "scout.log"
	// scoutPathWindow is how many path queries the scout keeps in flight (spec 5.6 step 7).
	scoutPathWindow = 8
	// scoutReplyWait is how long one path reply may take before the query is retried once.
	scoutReplyWait = 10 * time.Second
	// scoutPrewait is how long a hop's GM-mode, module and toolkit probes wait.
	scoutPrewait = 10 * time.Second
	// scoutLoginWait is the login wait raid-walk uses (phaseLogin).
	scoutLoginWait = 90 * time.Second
	// scoutMapWait is how long a `.go xyz` may take to show its map in the status.
	scoutMapWait = 30 * time.Second
	// scoutReadReachYd is how far (3D) the GM may stand from a read's point before the spot is reached first: the
	// toolkit refuses a read more than 150 yd from the GM (point_far), and a stop's spots can chain out past that.
	scoutReadReachYd = 120
	// scoutToolkitFar is the toolkit's refusal of a read out of reach (the rpc error names it).
	scoutToolkitFar = "point_far"
	// scoutLandYd is how close (x/y) a `.go xyz` must land to count as acked (raid-walk's reviveLandingYd).
	scoutLandYd = 10
	// scoutTeleportWait is how long a `.go xyz` may take to be acked and land.
	scoutTeleportWait = 10 * time.Second
	// scoutStopsPerInstanceCheck is how many stops pass between two `q inst` reads (spec 5.6 step 8).
	scoutStopsPerInstanceCheck = 25
	// scoutMaxLOSQueries is the toolkit's `q los` cap in one line.
	scoutMaxLOSQueries = 8
	// scoutWalkPoll is the `where` poll interval.
	scoutWalkPoll = time.Second
	// scoutWalkSettle is the rest before and after a walk.
	scoutWalkSettle = time.Second
	// scoutWalkStill is how long the GM must stand still to count as stopped (spec 5.6 step 9).
	scoutWalkStill = 2 * time.Second
	// scoutCapUnreadableWait is the one wait a scout gives an unreadable ledger (raid-walk's rule).
	scoutCapUnreadableWait = time.Minute
)

// scoutStop is a live pass ending with an exit code and its reason.
type scoutStop struct {
	code   int
	reason string
}

func (e *scoutStop) Error() string { return e.reason }

func scoutStopf(code int, format string, a ...any) error {
	return &scoutStop{code: code, reason: fmt.Sprintf(format, a...)}
}

// scoutStopInfo reads a live error: a *scoutStop carries its code; a plain error is an aborted pass (28).
func scoutStopInfo(err error) (int, string) {
	var s *scoutStop
	if errors.As(err, &s) {
		return s.code, s.reason
	}
	if err == nil {
		return exitOK, ""
	}
	return exitPlanScoutLiveAborted, err.Error()
}

// planScoutLiveEnv binds the live pass to the slot config; tests replace it with fakes.
var planScoutLiveEnv = func(o *planScoutOptions) (walkEnv, walkDeps, error) {
	return defaultWalkEnv(&raidWalkOptions{slotsPath: o.slots, mmaps: o.mmaps, dir: o.dir, binary: o.binary})
}

// scoutLive is one scout's live run.
type scoutLive struct {
	opts   *planScoutOptions
	server string
	plan   *dungeon.Plan
	in     planscout.Input
	rep    *planscout.Report
	world  *worldspawn.World
	nav    planscout.Nav
	outDir string

	stdout io.Writer
	stderr io.Writer
	env    walkEnv
	deps   walkDeps

	agent     walkAgent
	slotDir   string
	manifest  teardownSlotManifest
	lease     slots.Lease
	notes     []string
	logF      *os.File
	eventsAt  uint64
	nextID    uint32
	deadline  time.Time
	abortWhy  string
	leaseLost atomic.Bool
	cancel    context.CancelFunc

	data planscout.LiveData
	// preFixes are the offline fixes, for their move_stand candidate spots.
	preFixes []planscout.Fix
	// pathMissed are the path requests unanswered after their retry: the live pass is incomplete (28) and their hops go to the walk pass.
	pathMissed []*respReq

	capOwner    string
	capReserved bool
	instance    float64
	instKnown   bool
	tornDown    bool
	left        bool
}

// runScoutLive is the command's live entry; tests drive it through planScoutLiveEnv.
func runScoutLive(ctx context.Context, o *planScoutOptions, in planscout.Input, rep *planscout.Report, world *worldspawn.World,
	nav planscout.Nav, preFixes []planscout.Fix, outDir string, stdout, stderr io.Writer) (planscout.LiveData, int, string) {
	env, deps, err := planScoutLiveEnv(o)
	if err != nil {
		return planscout.LiveData{}, exitLivePhaseRefused, "the slot config: " + err.Error()
	}
	sl := &scoutLive{opts: o, server: o.live, plan: in.Plan, in: in, rep: rep, world: world, nav: nav,
		outDir: outDir, stdout: stdout, stderr: stderr, env: env, deps: deps, preFixes: preFixes}
	data, code, reason := sl.run(ctx, preFixes)
	data.Notes = append([]string{}, sl.notes...)
	return data, code, reason
}

// run is the live run's order (spec 5.6). Every exit tears the slot down and releases the ledger.
func (sl *scoutLive) run(ctx context.Context, preFixes []planscout.Fix) (planscout.LiveData, int, string) {
	sl.data.Paths = map[int]planscout.ScoutPath{}
	sl.data.ExtraPaths = map[int][]planscout.ScoutPath{}
	sl.data.Walks = map[int]planscout.WalkResult{}
	sl.data.Units = map[int][]objects.Unit{}
	sl.data.LOS = map[planscout.LOSKey]bool{}
	if err := sl.openLog(); err != nil {
		return sl.data, exitUsage, err.Error()
	}
	defer sl.closeLog()
	runCtx, cancel := context.WithCancel(ctx)
	sl.cancel = cancel
	defer cancel()
	minutes := sl.opts.maxMinutes
	if minutes <= 0 {
		minutes = 20
	}
	sl.deadline = sl.now().Add(time.Duration(minutes) * time.Minute)
	defer sl.leave()
	if code, reason := sl.instanceCap(runCtx); code != exitOK {
		return sl.data, code, reason
	}
	for _, step := range []func(context.Context) error{sl.leasePhase, sl.setupPhase, sl.loginPhase, sl.checkPhase,
		sl.gmPhase, sl.enterPhase} {
		if err := step(runCtx); err != nil {
			if sl.leaseLost.Load() {
				sl.data.Aborted = true
				sl.data.AbortReason = "the lease was lost"
				return sl.data, exitPlanScoutLiveAborted, "the lease was lost"
			}
			code, reason := scoutStopInfo(err)
			if code == exitPlanScoutLiveAborted {
				sl.data.Aborted = true
				sl.data.AbortReason = reason
			}
			return sl.data, code, reason
		}
	}
	// The passes: a stop ends the run with its code. An unanswered path request does not end it: its hop is queued for the walk
	// pass, and the live pass is still incomplete (28) with the request named in the reason.
	code, reason := exitOK, ""
	for _, pass := range []func(context.Context) error{sl.pathPass, sl.spotPass, sl.walkPass} {
		if err := pass(runCtx); err != nil {
			code, reason = scoutStopInfo(err)
			break
		}
	}
	if len(sl.pathMissed) > 0 {
		missed := sl.pathMissedReason()
		switch code {
		case exitOK:
			code, reason = exitPlanScoutLiveAborted, missed
		case exitPlanScoutLiveAborted:
			reason += "; " + missed
		}
	}
	if code == exitOK {
		sl.data.Aborted = false
		return sl.data, exitOK, ""
	}
	if code == exitPlanScoutLiveAborted {
		sl.data.Aborted = true
		sl.data.AbortReason = reason
	}
	return sl.data, code, reason
}

// now, sleep and expired use the env's clock, so tests drive a whole run.
func (sl *scoutLive) now() time.Time {
	if sl.env.now != nil {
		return sl.env.now()
	}
	return time.Now()
}

func (sl *scoutLive) sleep(ctx context.Context, d time.Duration) error {
	if d > 0 {
		if sl.env.sleep == nil {
			if err := sleepCtx(ctx, d); err != nil {
				return sl.ctxStop(ctx)
			}
		} else if err := sl.env.sleep(ctx, d); err != nil {
			return sl.ctxStop(ctx)
		}
	}
	if err := ctx.Err(); err != nil {
		return sl.ctxStop(ctx)
	}
	if !sl.now().Before(sl.deadline) {
		return scoutStopf(exitPlanScoutLiveAborted, "the time ceiling (%d min) is reached", sl.opts.maxMinutes)
	}
	return nil
}

// ctxStop turns an interrupted wait into the partial report's reason.
func (sl *scoutLive) ctxStop(ctx context.Context) error {
	if sl.leaseLost.Load() {
		return scoutStopf(exitPlanScoutLiveAborted, "the lease was lost")
	}
	reason := "interrupted"
	if errors.Is(ctx.Err(), context.DeadlineExceeded) {
		reason = "the time ceiling is reached"
	}
	if sl.abortWhy != "" {
		reason = sl.abortWhy
	}
	return scoutStopf(exitPlanScoutLiveAborted, "%s", reason)
}

// note records a report note (also written to scout.log).
func (sl *scoutLive) note(format string, a ...any) {
	msg := fmt.Sprintf(format, a...)
	sl.notes = append(sl.notes, msg)
	sl.logLine("note: " + msg)
}

// openLog opens <out>/scout.log (0600).
func (sl *scoutLive) openLog() error {
	if sl.outDir == "" {
		return nil
	}
	if err := os.MkdirAll(sl.outDir, 0o700); err != nil {
		return fmt.Errorf("the report folder: %w", err)
	}
	f, err := os.OpenFile(filepath.Join(sl.outDir, scoutLogName), os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		return fmt.Errorf("scout.log: %w", err)
	}
	sl.logF = f
	return nil
}

func (sl *scoutLive) closeLog() {
	if sl.logF != nil {
		_ = sl.logF.Close()
		sl.logF = nil
	}
}

// logLine writes one timestamped line to scout.log.
func (sl *scoutLive) logLine(text string) {
	if sl.logF == nil {
		return
	}
	fmt.Fprintf(sl.logF, "%s %s\n", sl.now().Format("15:04:05.000"), escapeCtl(text))
}

// logReply logs the replies the report needs (the scout lines and the toolkit's TTJSON), never a settings file or a token.
func (sl *scoutLive) logReply(line string) {
	if strings.HasPrefix(line, "TBM:SCOUT_") || strings.HasPrefix(line, "TTJSON ") {
		sl.logLine(line)
	}
}

// instanceCap is step 2: the raid-walk ledger's check and reservation.
func (sl *scoutLive) instanceCap(ctx context.Context) (int, string) {
	sl.capOwner = fmt.Sprintf("plan-scout-%s-%d", sl.plan.Name, os.Getpid())
	ledger := sl.env.capLedger
	if ledger == "" {
		return exitOK, ""
	}
	if sl.opts.instanceCapReset {
		if err := sl.capUpdate(func(l *raidwalk.CapLedger) { l.Reset(sl.server) }); err != nil {
			sl.note("instance cap: reset of the ledger failed: %v", err)
		} else {
			sl.note("instance cap: ledger for %s reset (--instance-cap-reset)", sl.server)
		}
	}
	started := sl.now()
	warned, unreadable := false, 0
	for {
		now := sl.now()
		var admitted bool
		var n int
		var wait time.Duration
		err := sl.capUpdate(func(l *raidwalk.CapLedger) { admitted, n, wait = l.TryReserve(sl.server, sl.capOwner, now) })
		if err != nil {
			msg := fmt.Sprintf("instance cap: the ledger cannot be read, so the count on %s is unknown: %v", sl.server, err)
			sl.note("%s", msg)
			if sl.opts.instanceCapFail {
				return exitRaidWalkInstanceCap, msg + " (--instance-cap-fail)"
			}
			if unreadable == 0 && sl.opts.capWaitD > 0 {
				unreadable++
				d := min(scoutCapUnreadableWait, sl.opts.capWaitD)
				if err := sl.sleep(ctx, d); err != nil {
					code, reason := scoutStopInfo(err)
					return code, reason
				}
				continue
			}
			sl.note("instance cap: going on without the ledger; no entry will be written over it")
			return exitOK, ""
		}
		if admitted {
			sl.capReserved = true
			if warned {
				sl.note("instance cap: %d fresh instance(s) in the last hour on %s; going on", n, sl.server)
			}
			return exitOK, ""
		}
		warn := fmt.Sprintf("instance cap: %d fresh instances in the last hour on %s; the 6th is refused by the core", n, sl.server)
		if !warned {
			sl.note("%s", warn)
			warned = true
		}
		if sl.opts.instanceCapFail {
			return exitRaidWalkInstanceCap, warn + " (--instance-cap-fail)"
		}
		left := sl.opts.capWaitD - now.Sub(started)
		if wait <= 0 {
			wait = 5 * time.Second
		}
		if left <= 0 {
			sl.note("instance cap: waited %s (--instance-cap-wait), still %d on %s; going on", sl.opts.capWaitD, n, sl.server)
			return exitOK, ""
		}
		if wait > left {
			wait = left
		} else {
			wait += 5 * time.Second
		}
		sl.note("instance cap: waiting %s for the oldest of %d to age out of the hour", wait.Round(time.Second), n)
		if err := sl.sleep(ctx, wait); err != nil {
			code, reason := scoutStopInfo(err)
			return code, reason
		}
	}
}

// capUpdate runs fn on the ledger under its lock, moving a corrupt ledger aside (raid-walk's message).
func (sl *scoutLive) capUpdate(fn func(*raidwalk.CapLedger)) error {
	moved, err := raidwalk.UpdateCapLedger(sl.env.capLedger, fn)
	if moved != "" {
		sl.note("instance cap: the ledger was corrupt; moved to %s and a new ledger started (the earlier history is lost)", moved)
	}
	return err
}

// confirmInstance turns the reservation into the ledger entry once the GM is inside the fresh copy.
func (sl *scoutLive) confirmInstance() {
	if sl.env.capLedger == "" {
		return
	}
	now := sl.now()
	err := sl.capUpdate(func(l *raidwalk.CapLedger) {
		if sl.capReserved {
			l.Confirm(sl.server, sl.capOwner, raidwalk.CapEntry{At: now.UTC(), Plan: sl.plan.Name}, now)
			return
		}
		// The scout went on past the wait cap (no reservation): a plain entry, as raid-walk records it.
		l.Record(sl.server, raidwalk.CapEntry{At: now.UTC(), Plan: sl.plan.Name}, now)
	})
	if err != nil {
		sl.note("instance cap: ledger entry not written: %v", err)
	}
	sl.capReserved = false
	sl.note("instance: one of the account's 5 fresh instance ids per hour was used")
}

// releaseCap gives the reservation back when the scout ends before its GM entered.
func (sl *scoutLive) releaseCap() {
	if !sl.capReserved || sl.env.capLedger == "" {
		return
	}
	sl.capReserved = false
	if err := sl.capUpdate(func(l *raidwalk.CapLedger) { l.Release(sl.server, sl.capOwner) }); err != nil {
		sl.note("instance cap: reservation not released (it expires in %s): %v", raidwalk.InstanceCapReserveFor, err)
	}
}

// leasePhase is step 3: never slot 3, a TTL with a renewal every 10 minutes.
func (sl *scoutLive) leasePhase(ctx context.Context) error {
	slot := ""
	if sl.opts.slot != 0 {
		slot = strconv.Itoa(sl.opts.slot)
	}
	minutes := sl.opts.maxMinutes
	if minutes <= 0 {
		minutes = 20
	}
	ttl := time.Duration(minutes+15) * time.Minute
	holder := sl.opts.holder
	if holder == "" {
		holder = "plan-scout-" + sl.plan.Name
	}
	acq, err := sl.env.acquire(sl.server, slot, slot == "", holder, ttl)
	if err != nil {
		return scoutStopf(exitSlotRefused, "lease %s/%s: %v", sl.server, slot, err)
	}
	if acq.Lease.Slot == "3" {
		if _, rerr := sl.env.release(acq.Lease.Server, acq.Lease.Slot, acq.Lease.Holder, acq.Lease.LeaseID, false); rerr != nil {
			sl.note("slot 3 lease release: %v", rerr)
		}
		return scoutStopf(exitSlotRefused, "the lease came back as slot 3: slot 3 is never used (released)")
	}
	sl.lease = acq.Lease
	sl.note("lease %s/%s (holder %s) until %s", sl.lease.Server, sl.lease.Slot, sl.lease.Holder, sl.lease.Expires.UTC().Format(time.RFC3339))
	go func() {
		t := time.NewTicker(10 * time.Minute)
		defer t.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-t.C:
				if _, err := sl.env.renew(sl.lease.Server, sl.lease.Slot, sl.lease.Holder, sl.lease.LeaseID, ttl); err != nil {
					sl.leaseLost.Store(true)
					sl.abortWhy = "the lease renew failed: " + err.Error()
					if sl.cancel != nil {
						sl.cancel()
					}
					return
				}
			}
		}
	}()
	return nil
}

// setupPhase is step 4a: the slot setup and its manifest.
func (sl *scoutLive) setupPhase(ctx context.Context) error {
	dir := filepath.Join(sl.outDir, "slot")
	args := []string{sl.server, sl.lease.Slot, "--holder", sl.holderName(), "--dir", dir, "--dungeons-from", sl.opts.dir}
	if sl.opts.binary != "" {
		args = append(args, "--binary", sl.opts.binary)
	}
	if sl.opts.mmaps != "" {
		args = append(args, "--mmaps-dir", sl.opts.mmaps)
	}
	if sl.opts.slots != "" {
		args = append(args, "--slots", sl.opts.slots)
	}
	var buf strings.Builder
	code := sl.env.setup(args, &buf, &buf)
	if err := os.MkdirAll(sl.outDir, 0o700); err == nil {
		_ = os.WriteFile(filepath.Join(sl.outDir, "slot-setup.txt"), []byte(buf.String()), 0o600)
	}
	if code != exitOK {
		return scoutStopf(exitLivePhaseRefused, "slot setup failed (exit %d): %s", code, firstOutLine(buf.String()))
	}
	sl.slotDir = dir
	m, err := readWalkManifest(dir)
	if err != nil {
		return scoutStopf(exitLivePhaseRefused, "slot setup: %v", err)
	}
	sl.manifest = m
	sl.logLine("slot ready in " + dir)
	return nil
}

// holderName is the lease holder.
func (sl *scoutLive) holderName() string {
	if sl.opts.holder != "" {
		return sl.opts.holder
	}
	return "plan-scout-" + sl.plan.Name
}

// loginPhase is step 4b: start the slot agent and wait for in_world.
func (sl *scoutLive) loginPhase(ctx context.Context) error {
	pid, err := sl.env.startAgent(sl.manifest)
	if err != nil {
		return scoutStopf(exitLivePhaseRefused, "login: %v", err)
	}
	sl.logLine(fmt.Sprintf("login started (pid %d)", pid))
	deadline := sl.now().Add(scoutLoginWait)
	for {
		agent, m, err := sl.deps.openAgent(sl.slotDir)
		if err == nil {
			sl.agent, sl.manifest = agent, m
			break
		}
		if !sl.now().Before(deadline) {
			return scoutStopf(exitLivePhaseRefused, "login: no agent in %s after 90 s (%v; see %s/agent.out)", sl.slotDir, err, sl.slotDir)
		}
		if err := sl.sleep(ctx, time.Second); err != nil {
			return err
		}
	}
	for {
		st, err := sl.status(ctx)
		if err == nil && st.State == session.StatusInWorld {
			break
		}
		if !sl.now().Before(deadline) {
			return scoutStopf(exitLivePhaseRefused, "login: not in world after 90 s (state %q; see %s/agent.out)", stateWord(st), sl.slotDir)
		}
		if err := sl.sleep(ctx, 2*time.Second); err != nil {
			return err
		}
	}
	return sl.drainEvents(ctx)
}

// checkPhase is step 4c: only a test server, and only the one the session actually dialled.
func (sl *scoutLive) checkPhase(ctx context.Context) error {
	_ = ctx
	path := sl.opts.slots
	if path == "" {
		path = slots.DefaultPath()
	}
	cfg, err := slots.Load(path)
	if err != nil {
		return scoutStopf(exitLivePhaseRefused, "the slots table: %v", err)
	}
	st, err := config.Load(sl.manifest.Settings)
	if err != nil {
		return scoutStopf(exitLivePhaseRefused, "the slot settings: %v", err)
	}
	if st.WorldAddr == "" {
		return scoutStopf(exitLivePhaseRefused, "the slot settings name no world_addr: the dialled address cannot be checked")
	}
	if ok, why := gmKillAllowed(st, st.WorldAddr, cfg.Servers); !ok {
		if why == "" {
			why = fmt.Sprintf("settings server_name %q is not a test server", st.ServerName)
		}
		return scoutStopf(exitLivePhaseRefused, "%s", why)
	}
	sl.note("live server %s at %s (a test server; the session dialled %s)", sl.server, st.ServerName, st.WorldAddr)
	return nil
}

// gmPhase is step 5: GM mode, invisible, the module and the toolkit.
func (sl *scoutLive) gmPhase(ctx context.Context) error {
	if err := sl.gm(ctx, ".gm on"); err != nil {
		return err
	}
	deadline := sl.now().Add(placeGMTimeout)
	for {
		lines, err := sl.pump(ctx, 1000)
		if err != nil {
			return scoutStopf(exitLivePhaseRefused, "the GM-mode check: %v", err)
		}
		for _, line := range lines {
			switch {
			case strings.Contains(line, "GM mode is ON"):
				goto gmOn
			case strings.Contains(line, "GM mode is OFF"), strings.Contains(line, "There is no such command"):
				return scoutStopf(exitLivePhaseRefused, "the commander is not in GM mode (%s)", line)
			}
		}
		if !sl.now().Before(deadline) {
			return scoutStopf(exitLivePhaseRefused, "no answer to .gm on in %s: the commander's GM mode is unknown", placeGMTimeout)
		}
		if err := sl.sleep(ctx, 200*time.Millisecond); err != nil {
			return err
		}
	}
gmOn:
	if err := sl.gm(ctx, raidwalk.GMVisibleOff); err != nil {
		return err
	}
	// The module's own rules (spec 5.6 step 5); no SCOUT_CFG in 10 s is a module without .bot scout.
	id := sl.newID()
	if err := sl.gm(ctx, fmt.Sprintf(".bot scout cfg #%d", id)); err != nil {
		return err
	}
	line, ok, err := sl.waitLine(ctx, func(l string) bool {
		p, ok := tbm.ParseScoutCfg(l)
		return ok && p.ID == id
	}, scoutPrewait)
	if err != nil {
		return err
	}
	if !ok {
		return scoutStopf(exitLivePhaseRefused, "module without .bot scout: no SCOUT_CFG in %s", scoutPrewait)
	}
	if cfg, ok := tbm.ParseScoutCfg(line); ok {
		sl.data.Cfg = planscout.ScoutCfg{ReactDistance: float64(cfg.ReactDistance), RateCreatureAggro: float64(cfg.RateCreatureAggro),
			FamilyAssistRadius: float64(cfg.FamilyAssistRadius)}
		sl.data.CfgOK = true
		sl.note("module rules: ReactDistance %.0f, Rate.Creature.Aggro %.2f", cfg.ReactDistance, cfg.RateCreatureAggro)
	}
	// The Eluna test toolkit.
	if _, err := sl.ttInst(ctx, "the toolkit probe"); err != nil {
		return scoutStopf(exitLivePhaseRefused, "no Eluna toolkit: %v", stopReason(err))
	}
	return nil
}

// enterPhase is step 6: the portal, the unbind, the first row and the fresh instance's id.
func (sl *scoutLive) enterPhase(ctx context.Context) error {
	p := sl.plan.Portal
	if p == nil {
		return scoutStopf(exitLivePhaseRefused, "the plan has no portal: the live pass needs its place outside the entrance")
	}
	if err := sl.gm(ctx, fmt.Sprintf(".go xyz %s %s %s %d", num32(p.X), num32(p.Y), num32(p.Z), p.Map)); err != nil {
		return err
	}
	if err := sl.waitMap(ctx, p.Map, scoutMapWait); err != nil {
		return err
	}
	if err := sl.gm(ctx, fmt.Sprintf(".instance unbind %d", sl.plan.Map)); err != nil {
		return err
	}
	if len(sl.rep.Steps) == 0 {
		return scoutStopf(exitLivePhaseRefused, "the report has no step: the live pass has nothing to check")
	}
	first := sl.rep.Steps[0]
	if err := sl.gm(ctx, fmt.Sprintf(".go xyz %s %s %s %d", num32(first.X), num32(first.Y), num32(first.Z), sl.plan.Map)); err != nil {
		return err
	}
	if err := sl.waitMap(ctx, sl.plan.Map, scoutMapWait); err != nil {
		return err
	}
	inst, err := sl.ttInst(ctx, "the enter check")
	if err != nil {
		return scoutStopf(exitLivePhaseRefused, "the instance check: %v", stopReason(err))
	}
	sl.instance, sl.instKnown = inst, true
	sl.confirmInstance()
	return nil
}

// pathPass is step 7: `.bot scout path` for every hop origin, 8 in flight, matched by id, one retry.
func (sl *scoutLive) pathPass(ctx context.Context) error {
	hops := planscout.LiveHops(sl.rep, sl.in, sl.nav)
	if len(hops) == 0 {
		return nil
	}
	reqs := make([]*respReq, len(hops))
	for i, h := range hops {
		reqs[i] = &respReq{id: sl.newID(), hop: h}
	}
	for start := 0; start < len(reqs); start += scoutPathWindow {
		end := min(start+scoutPathWindow, len(reqs))
		win := reqs[start:end]
		for attempt := 0; attempt < 2; attempt++ {
			for _, r := range win {
				if r.done || (attempt == 1 && r.retried) {
					continue
				}
				if attempt == 0 && r.sent {
					continue
				}
				if attempt == 1 {
					r.retried = true
					sl.note("no answer for path #%d: one retry", r.id)
				}
				r.sent = true
				line := fmt.Sprintf(".bot scout path #%d %s %s %s from %s %s %s", r.id,
					num32(r.hop.Point.X), num32(r.hop.Point.Y), num32(r.hop.Point.Z),
					num32(r.hop.From.X), num32(r.hop.From.Y), num32(r.hop.From.Z))
				if err := sl.gm(ctx, line); err != nil {
					return err
				}
			}
			deadline := sl.now().Add(scoutReplyWait)
			for !allDone(win) && sl.now().Before(deadline) {
				lines, err := sl.pump(ctx, 500)
				if err != nil {
					return scoutStopf(exitPlanScoutLiveAborted, "the path pass: %v", err)
				}
				for _, line := range lines {
					sl.takePath(line, win)
				}
				if err := sl.sleep(ctx, 200*time.Millisecond); err != nil {
					return err
				}
			}
			if allDone(win) {
				break
			}
		}
		for _, r := range win {
			if !r.done {
				sl.pathMissed = append(sl.pathMissed, r)
				sl.note("path #%d (step %d) unanswered after the retry", r.id, r.hop.Step)
			}
		}
	}
	return nil
}

// pathMissedReason names the unanswered path requests: the abort reason of an incomplete live pass.
func (sl *scoutLive) pathMissedReason() string {
	named := make([]string, 0, len(sl.pathMissed))
	for _, r := range sl.pathMissed {
		named = append(named, fmt.Sprintf("#%d (step %d)", r.id, r.hop.Step))
	}
	return fmt.Sprintf("%d path request(s) unanswered after the retry: %s (their hops are queued for the walk pass)",
		len(sl.pathMissed), strings.Join(named, ", "))
}

// takePath records a SCOUT_PATH answer for a request of the window.
func (sl *scoutLive) takePath(line string, win []*respReq) {
	p, ok := tbm.ParseScoutPath(line)
	if !ok {
		return
	}
	for i := range win {
		r := win[i]
		if r.id != p.ID || r.done {
			continue
		}
		sp := planscout.ScoutPath{Verdict: p.Verdict, PathType: p.PathType, Length: float64(p.Length),
			End:       dungeon.Point{X: p.End[0], Y: p.End[1], Z: p.End[2]},
			EndOffset: float64(p.EndOffset), SnappedZ: float64(p.SnappedZ), StartSwim: p.StartSwim, EndSwim: p.EndSwim}
		if r.hop.Primary {
			sl.data.Paths[r.hop.Step] = sp
		} else {
			sl.data.ExtraPaths[r.hop.Step] = append(sl.data.ExtraPaths[r.hop.Step], sp)
		}
		r.done = true
		return
	}
}

// respReq is one path pass request.
type respReq struct {
	id      uint32
	hop     planscout.LiveHop
	sent    bool
	retried bool
	done    bool
}

func allDone(win []*respReq) bool {
	for _, r := range win {
		if !r.done {
			return false
		}
	}
	return true
}

// spotPass is step 8: every stand, anchor and pull row, read by `q units` and `q los` from a stop.
func (sl *scoutLive) spotPass(ctx context.Context) error {
	spots := planscout.LiveSpots(sl.rep, sl.in, sl.world, sl.preFixes)
	if len(spots) == 0 {
		return nil
	}
	radius := planscout.LiveSpotRadius(sl.world, sl.options())
	groups := planscout.LiveStops(spots)
	for gi, group := range groups {
		if _, err := sl.standAt(ctx, group[0].Point, scoutLandYd); err != nil {
			return err
		}
		if err := sl.sleep(ctx, time.Second); err != nil {
			return err
		}
		for _, spot := range group {
			// A stop's spots chain (each within LiveSpotYd of an earlier one), so a later spot can stand past the
			// toolkit's read reach from the stop: the GM goes there first.
			if _, err := sl.standAt(ctx, spot.Point, scoutReadReachYd); err != nil {
				return err
			}
			units, err := sl.readUnits(ctx, spot.Point, radius)
			if err != nil {
				return err
			}
			sl.addUnits(spot.Step, units)
			var losUnits []planscout.LiveTarget
			for _, u := range units {
				if u.Dead {
					continue
				}
				ou := sl.objectUnit(u)
				if sl.withinReach(ou, spot.Point, radius) {
					losUnits = append(losUnits, planscout.LiveTarget{Guid: uint32(ou.GUID) & 0xffffff, Entry: ou.Entry})
				}
			}
			if err := sl.losAt(ctx, spot.LOSPoint, spot.Step, losUnits); err != nil {
				return err
			}
			if len(spot.TargetGuids) > 0 {
				if err := sl.losAt(ctx, spot.LOSPoint, spot.Step, spot.TargetGuids); err != nil {
					return err
				}
			}
		}
		if (gi+1)%scoutStopsPerInstanceCheck == 0 {
			inst, err := sl.ttInst(ctx, "the instance re-check")
			if err != nil {
				return scoutStopf(exitPlanScoutLiveAborted, "the instance re-check: %v", stopReason(err))
			}
			if !sl.instKnown || inst != sl.instance {
				return scoutStopf(exitPlanScoutLiveAborted, "the instance changed (%.0f before, %.0f now): the GM left the scout's copy", sl.instance, inst)
			}
		}
	}
	return nil
}

// readUnits reads `q units` at a point; a truncated answer is re-read at half the radius around four offsets.
func (sl *scoutLive) readUnits(ctx context.Context, p dungeon.Point, r float64) ([]ttUnit, error) {
	out, trunc, err := sl.unitsOnce(ctx, p, r)
	if err != nil {
		return nil, err
	}
	if !trunc {
		return out, nil
	}
	sl.note("q units at %.1f %.1f %.1f was truncated at %.0f yd: re-read at half the radius around four offsets", p.X, p.Y, p.Z, r)
	half := r / 2
	off := float32(half)
	seen := map[string]bool{}
	for _, u := range out {
		seen[u.GUID] = true
	}
	for _, d := range [][2]float32{{off, 0}, {-off, 0}, {0, off}, {0, -off}} {
		q := dungeon.Point{X: p.X + d[0], Y: p.Y + d[1], Z: p.Z}
		more, _, err := sl.unitsOnce(ctx, q, half)
		if err != nil {
			return nil, err
		}
		for _, u := range more {
			if seen[u.GUID] {
				continue
			}
			seen[u.GUID] = true
			out = append(out, u)
		}
	}
	return out, nil
}

// unitsOnce sends one `q units` read.
func (sl *scoutLive) unitsOnce(ctx context.Context, p dungeon.Point, r float64) ([]ttUnit, bool, error) {
	if r < 1 {
		r = 1
	}
	cmd := fmt.Sprintf("q units %.0f %s %s %s all", r, num32(p.X), num32(p.Y), num32(p.Z))
	res, err := sl.ttRead(ctx, p, cmd)
	if err != nil {
		return nil, false, scoutStopf(exitPlanScoutLiveAborted, "q units: %v", stopReason(err))
	}
	body, ok := ttLast(res)
	if !ok {
		return nil, false, scoutStopf(exitPlanScoutLiveAborted, "q units gave no reply")
	}
	var out []ttUnit
	for _, m := range body.list("units") {
		out = append(out, ttUnitOf(m))
	}
	return out, body.flag("trunc"), nil
}

// addUnits appends a spot's units to the step, one per GUID.
func (sl *scoutLive) addUnits(step int, units []ttUnit) {
	have := map[packet.GUID]bool{}
	for _, u := range sl.data.Units[step] {
		have[u.GUID] = true
	}
	for _, u := range units {
		ou := sl.objectUnit(u)
		if have[ou.GUID] {
			continue
		}
		have[ou.GUID] = true
		sl.data.Units[step] = append(sl.data.Units[step], ou)
	}
}

// objectUnit joins a toolkit unit to the world export (its level, faction and hostility) and gives it its full GUID.
func (sl *scoutLive) objectUnit(u ttUnit) objects.Unit {
	entry := u.Entry
	level, hostile, faction := uint32(0), false, uint32(0)
	if sl.world != nil {
		for _, sp := range sl.world.Spawns {
			if sp.GUID != uint64(u.Spawn) {
				continue
			}
			level, hostile, faction = uint32(sp.LevelMax), true, uint32(sp.Faction)
			if sp.Entry != 0 {
				entry = sp.Entry
			}
			break
		}
	}
	guid := packet.GUID(packet.HighUnit)<<48 | packet.GUID(entry)<<24 | packet.GUID(u.Spawn)
	return objects.Unit{GUID: guid, Type: "creature", Entry: entry, Name: u.Name, X: u.X, Y: u.Y, Z: u.Z,
		Health: uint32(u.MaxHP), MaxHealth: uint32(u.MaxHP), Level: level, Faction: faction, Dead: u.Dead, Hostile: hostile}
}

// withinReach says a live unit stands within its aggro reach plus the stand spread of a spot.
func (sl *scoutLive) withinReach(u objects.Unit, p dungeon.Point, fallback float64) bool {
	spread := float64(planscout.StandPullMarginYd)
	if sl.options().Raid {
		spread = 10
	} else {
		spread = 3
	}
	reach := fallback
	if sl.world != nil {
		for _, sp := range sl.world.Spawns {
			if sp.GUID == uint64(uint32(u.GUID)&0xffffff) {
				if r := float64(worldspawn.AggroRadius(sp, sl.partyLevel(), sl.aggroRate())); r > 0 {
					reach = r
				}
				break
			}
		}
	}
	dx, dy, dz := float64(p.X-u.X), float64(p.Y-u.Y), float64(p.Z-u.Z)
	return dx*dx+dy*dy+dz*dz <= (reach+spread)*(reach+spread)
}

// losAt asks `q los` from p for the targets, 8 a line, and stores the answers under the step.
func (sl *scoutLive) losAt(ctx context.Context, p dungeon.Point, step int, targets []planscout.LiveTarget) error {
	for start := 0; start < len(targets); start += scoutMaxLOSQueries {
		end := min(start+scoutMaxLOSQueries, len(targets))
		cmd := fmt.Sprintf("q los %s %s %s", num32(p.X), num32(p.Y), num32(p.Z))
		for _, t := range targets[start:end] {
			cmd += fmt.Sprintf(" %s", scoutFullGUID(t))
		}
		res, err := sl.ttRead(ctx, p, cmd)
		if err != nil {
			return scoutStopf(exitPlanScoutLiveAborted, "q los: %v", stopReason(err))
		}
		body, ok := ttLast(res)
		if !ok {
			continue
		}
		for _, m := range body.list("los") {
			in, ok := numOf(m, "in")
			if !ok {
				continue
			}
			g := strOf(m, "g")
			low, ok := scoutLowGUID(g)
			if !ok {
				continue
			}
			sl.data.LOS[planscout.LOSKey{Step: step, Guid: low}] = in != 0
		}
	}
	return nil
}

// scoutFullGUID is a `q los` guid: the creature GUID the toolkit's findByHex parses (entry and counter).
func scoutFullGUID(t planscout.LiveTarget) string {
	return fmt.Sprintf("0x%X", uint64(packet.HighUnit)<<48|uint64(t.Entry)<<24|uint64(t.Guid))
}

// scoutLowGUID reads the counter (the low 24 bits) of a guid the toolkit answered.
func scoutLowGUID(s string) (uint32, bool) {
	rest, ok := strings.CutPrefix(s, "0x")
	if !ok {
		return 0, false
	}
	v, err := strconv.ParseUint(rest, 16, 64)
	if err != nil {
		return 0, false
	}
	return uint32(v) & 0xffffff, true
}

// walkPass is step 9: the ranked risky hops, walked for real within the budget.
func (sl *scoutLive) walkPass(ctx context.Context) error {
	hops := planscout.LiveHops(sl.rep, sl.in, sl.nav)
	primary := map[int]planscout.LiveHop{}
	for _, h := range hops {
		if h.Primary {
			primary[h.Step] = h
		}
	}
	type ranked struct {
		step  int
		score int
	}
	// A primary hop whose path request went unanswered is queued: it ranks above every checked hop.
	const queuedRank = 1000
	queued := map[int]bool{}
	for _, r := range sl.pathMissed {
		if r.hop.Primary {
			queued[r.hop.Step] = true
		}
	}
	var list []ranked
	for _, st := range sl.rep.Steps {
		if _, ok := primary[st.Step]; !ok {
			continue
		}
		s := scoutWalkRank(&st, sl.data.Paths[st.Step])
		if queued[st.Step] {
			s = queuedRank
		}
		if s > 0 {
			list = append(list, ranked{step: st.Step, score: s})
		}
	}
	sort.SliceStable(list, func(i, j int) bool {
		if list[i].score != list[j].score {
			return list[i].score > list[j].score
		}
		return list[i].step < list[j].step
	})
	budget := sl.opts.walkBudgetD
	if budget <= 0 {
		budget = 5 * time.Minute
	}
	ends := sl.now().Add(budget)
	for _, r := range list {
		hop := primary[r.step]
		if !sl.now().Before(ends) {
			sl.data.Walks[r.step] = planscout.WalkResult{Skipped: "walk budget"}
			continue
		}
		if err := sl.walkHop(ctx, hop); err != nil {
			return err
		}
	}
	return nil
}

// scoutWalkRank ranks a hop by the offline and live checks the walk pass names (spec 5.6 step 9).
func scoutWalkRank(st *planscout.StepResult, live planscout.ScoutPath) int {
	score := 0
	for _, c := range st.Checks {
		if c.Source != planscout.SourceOffline {
			continue
		}
		var base int
		switch c.Name {
		case "climb_anchor":
			base = 100
		case "water":
			base = 90
		case "arrival":
			base = 80
		case "drop":
			base = 70
		default:
			continue
		}
		switch c.Verdict {
		case planscout.VerdictFail:
			if base > score {
				score = base
			}
		case planscout.VerdictWarn:
			if base-10 > score {
				score = base - 10
			}
		}
	}
	switch {
	case live.Verdict != "" && live.Verdict != "ok":
		if score < 85 {
			score = 85
		}
	case live.Underwater():
		if score < 65 {
			score = 65
		}
	}
	return score
}

// walkHop walks one hop: origin, `.bot scout walk`, `where` until still, and the anchor's way back.
func (sl *scoutLive) walkHop(ctx context.Context, hop planscout.LiveHop) error {
	if err := sl.gm(ctx, fmt.Sprintf(".go xyz %s %s %s %d", num32(hop.From.X), num32(hop.From.Y), num32(hop.From.Z), sl.plan.Map)); err != nil {
		return err
	}
	if err := sl.sleep(ctx, scoutWalkSettle); err != nil {
		return err
	}
	id := sl.newID()
	if err := sl.gm(ctx, fmt.Sprintf(".bot scout walk #%d %s %s %s", id, num32(hop.Point.X), num32(hop.Point.Y), num32(hop.Point.Z))); err != nil {
		return err
	}
	line, ok, err := sl.waitLine(ctx, func(l string) bool {
		w, ok := tbm.ParseScoutWalk(l)
		return ok && w.ID == id
	}, scoutPrewait)
	if err != nil {
		return err
	}
	if !ok {
		sl.data.Walks[hop.Step] = planscout.WalkResult{Skipped: "no SCOUT_WALK answer: the walk did not start"}
		return nil
	}
	sl.logLine(line)
	limit := 60 * time.Second
	if p, ok := sl.data.Paths[hop.Step]; ok && p.Length > 0 {
		limit = time.Duration(p.Length/7+10) * time.Second
	}
	start := sl.now()
	var last tbm.ScoutPos
	stillSince := time.Time{}
	have := false
	for sl.now().Sub(start) < limit {
		if err := sl.gm(ctx, fmt.Sprintf(".bot scout where #%d", id)); err != nil {
			return err
		}
		pos, ok, err := sl.waitPos(ctx, id, scoutPrewait)
		if err != nil {
			return err
		}
		if ok {
			last, have = pos, true
			if pos.Moving {
				stillSince = time.Time{}
			} else if stillSince.IsZero() {
				stillSince = sl.now()
			}
			if !stillSince.IsZero() && sl.now().Sub(stillSince) >= scoutWalkStill {
				break
			}
		}
		if err := sl.sleep(ctx, scoutWalkPoll); err != nil {
			return err
		}
	}
	if !have {
		sl.data.Walks[hop.Step] = planscout.WalkResult{Skipped: "no SCOUT_POS answer: the walk's stop is unknown"}
		return nil
	}
	wk := planscout.WalkResult{Length: limit.Seconds() * 7, Stop: dungeon.Point{X: last.X, Y: last.Y, Z: last.Z}}
	if hop.StandRow {
		if back, ok, err := sl.pathBack(ctx, wk.Stop, hop.Anchor); err != nil {
			return err
		} else if ok {
			wk.Back = &back
		}
	}
	sl.data.Walks[hop.Step] = wk
	return nil
}

// pathBack asks the server path from a walk's stop back to its anchor.
func (sl *scoutLive) pathBack(ctx context.Context, from, to dungeon.Point) (planscout.ScoutPath, bool, error) {
	id := sl.newID()
	if err := sl.gm(ctx, fmt.Sprintf(".bot scout path #%d %s %s %s from %s %s %s", id, num32(to.X), num32(to.Y), num32(to.Z),
		num32(from.X), num32(from.Y), num32(from.Z))); err != nil {
		return planscout.ScoutPath{}, false, err
	}
	line, ok, err := sl.waitLine(ctx, func(l string) bool {
		p, ok := tbm.ParseScoutPath(l)
		return ok && p.ID == id
	}, scoutPrewait)
	if err != nil || !ok {
		return planscout.ScoutPath{}, false, err
	}
	p, ok := tbm.ParseScoutPath(line)
	if !ok {
		return planscout.ScoutPath{}, false, nil
	}
	return planscout.ScoutPath{Verdict: p.Verdict, PathType: p.PathType, Length: float64(p.Length),
		End: dungeon.Point{X: p.End[0], Y: p.End[1], Z: p.End[2]}, EndOffset: float64(p.EndOffset)}, true, nil
}

// waitPos waits for a SCOUT_POS answer of one id.
func (sl *scoutLive) waitPos(ctx context.Context, id uint32, timeout time.Duration) (tbm.ScoutPos, bool, error) {
	line, ok, err := sl.waitLine(ctx, func(l string) bool {
		p, ok := tbm.ParseScoutPos(l)
		return ok && p.ID == id
	}, timeout)
	if err != nil || !ok {
		return tbm.ScoutPos{}, false, err
	}
	p, ok := tbm.ParseScoutPos(line)
	return p, ok, nil
}

// waitLine pumps the events until a scout line matches, logging every line.
func (sl *scoutLive) waitLine(ctx context.Context, match func(string) bool, timeout time.Duration) (string, bool, error) {
	deadline := sl.now().Add(timeout)
	for {
		lines, err := sl.pump(ctx, 500)
		if err != nil {
			return "", false, scoutStopf(exitPlanScoutLiveAborted, "events: %v", err)
		}
		for _, line := range lines {
			if match(line) || match(raidwalk.CleanLine(line)) {
				return line, true, nil
			}
		}
		if !sl.now().Before(deadline) {
			return "", false, nil
		}
		if err := sl.sleep(ctx, 200*time.Millisecond); err != nil {
			return "", false, err
		}
	}
}

// leave is step 10 and every error path: out to the portal, the slot teardown, the reservation release.
func (sl *scoutLive) leave() {
	if sl.left {
		return
	}
	sl.left = true
	sl.data.Aborted = sl.data.Aborted || sl.abortWhy != ""
	if sl.abortWhy != "" {
		sl.data.AbortReason = sl.abortWhy
	}
	if sl.agent != nil && sl.plan != nil && sl.plan.Portal != nil {
		ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
		p := sl.plan.Portal
		if err := sl.gm(ctx, fmt.Sprintf(".go xyz %s %s %s %d", num32(p.X), num32(p.Y), num32(p.Z), p.Map)); err != nil {
			sl.note("leave: %v", stopReason(err))
		}
		cancel()
	}
	if sl.slotDir != "" && !sl.tornDown {
		sl.tornDown = true
		var buf strings.Builder
		code := sl.env.teardown([]string{"--json", sl.slotDir}, &buf, &buf)
		_ = os.WriteFile(filepath.Join(sl.outDir, "teardown.json"), []byte(buf.String()), 0o600)
		if code != exitOK {
			sl.note("teardown exit %d: %s", code, firstOutLine(buf.String()))
			sl.releaseLease()
		}
	}
	sl.releaseCap()
}

// releaseLease gives the lease back when the teardown did not.
func (sl *scoutLive) releaseLease() {
	if sl.lease.Slot == "" || sl.env.release == nil {
		return
	}
	if _, err := sl.env.release(sl.lease.Server, sl.lease.Slot, sl.lease.Holder, sl.lease.LeaseID, false); err != nil {
		sl.note("lease release: %v", err)
	}
}

// status is the agent's status RPC.
func (sl *scoutLive) status(ctx context.Context) (session.StatusInfo, error) {
	var st session.StatusInfo
	err := sl.agent.Call(ctx, "status", nil, &st, 10*time.Second)
	return st, err
}

// standAt makes sure the GM stands within reach yd (3D) of p on the plan's map: a farther GM teleports to p first. It says
// whether it teleported.
func (sl *scoutLive) standAt(ctx context.Context, p dungeon.Point, reach float64) (bool, error) {
	st, err := sl.status(ctx)
	if err != nil {
		return false, scoutStopf(exitPlanScoutLiveAborted, "the commander's position: %v", stopReason(err))
	}
	here := dungeon.Point{X: st.X, Y: st.Y, Z: st.Z}
	if st.Map == sl.plan.Map && scoutDist3(here, p) <= reach {
		return false, nil
	}
	if err := sl.teleportTo(ctx, p); err != nil {
		return false, err
	}
	return true, nil
}

// teleportTo sends `.go xyz` to p on the plan's map and waits for the server's ack: the session's TeleportSeq moves past
// the value read before the teleport, and the GM stands within scoutLandYd of p (x/y).
func (sl *scoutLive) teleportTo(ctx context.Context, p dungeon.Point) error {
	before, err := sl.status(ctx)
	if err != nil {
		return scoutStopf(exitPlanScoutLiveAborted, "the commander's position: %v", stopReason(err))
	}
	if err := sl.gm(ctx, fmt.Sprintf(".go xyz %s %s %s %d", num32(p.X), num32(p.Y), num32(p.Z), sl.plan.Map)); err != nil {
		return err
	}
	deadline := sl.now().Add(scoutTeleportWait)
	for {
		st, err := sl.status(ctx)
		if err == nil && st.Map == sl.plan.Map && st.TeleportSeq > before.TeleportSeq && !st.Falling &&
			math.Hypot(float64(st.X-p.X), float64(st.Y-p.Y)) <= scoutLandYd {
			return nil
		}
		if !sl.now().Before(deadline) {
			return scoutStopf(exitPlanScoutLiveAborted, "the commander did not land at %s %s %s on map %d within %s (no teleport ack)",
				num32(p.X), num32(p.Y), num32(p.Z), sl.plan.Map, scoutTeleportWait)
		}
		if err := sl.sleep(ctx, 200*time.Millisecond); err != nil {
			return err
		}
	}
}

// ttRead sends one toolkit read at p. A read the toolkit refuses as out of reach (point_far) is sent once more after the GM
// teleports to p; a second refusal, or any other error, is returned.
func (sl *scoutLive) ttRead(ctx context.Context, p dungeon.Point, cmd string) (ttResult, error) {
	res, err := sl.tt(ctx, cmd)
	if err == nil || !strings.Contains(err.Error(), scoutToolkitFar) {
		return res, err
	}
	sl.note("%s refused as %s at %.1f %.1f %.1f: the commander teleports there and the read is sent once more",
		strings.Join(strings.Fields(cmd)[:2], " "), scoutToolkitFar, p.X, p.Y, p.Z)
	if err := sl.teleportTo(ctx, p); err != nil {
		return ttResult{}, err
	}
	return sl.tt(ctx, cmd)
}

// scoutDist3 is the 3D distance between two points.
func scoutDist3(a, b dungeon.Point) float64 {
	dx, dy, dz := float64(a.X-b.X), float64(a.Y-b.Y), float64(a.Z-b.Z)
	return math.Sqrt(dx*dx + dy*dy + dz*dz)
}

// gm sends one GM line after the scout's allow-list passes it.
func (sl *scoutLive) gm(ctx context.Context, line string) error {
	if !scoutAllowedLine(line, sl.plan.Map, sl.portalMap()) {
		return scoutStopf(exitLivePhaseRefused, "plan-scout refused to send a GM line outside its allow-list: %s", line)
	}
	sl.logLine("> " + line)
	if err := sl.agent.Call(ctx, "gm", map[string]string{"text": line}, nil, 10*time.Second); err != nil {
		return scoutStopf(exitPlanScoutLiveAborted, "gm %s: %v", line, err)
	}
	return nil
}

// tt sends one toolkit line after the scout's allow-list passes it.
func (sl *scoutLive) tt(ctx context.Context, cmd string) (ttResult, error) {
	if !scoutAllowedTT(cmd, sl.plan.Map) {
		return ttResult{}, fmt.Errorf("plan-scout refused to send a toolkit line outside its allow-list: %s", cmd)
	}
	sl.logLine("> .tt " + cmd)
	var out ttResult
	err := sl.agent.Call(ctx, "tt", ttParams{Cmd: cmd}, &out, ttCallTimeout)
	for _, raw := range out.Replies {
		sl.logReply("TTJSON " + string(raw))
	}
	return out, err
}

// ttInst reads the caller's instance id from `q inst`.
func (sl *scoutLive) ttInst(ctx context.Context, why string) (float64, error) {
	out, err := sl.tt(ctx, "q inst")
	if err != nil {
		return 0, fmt.Errorf("%s: q inst: %v", why, err)
	}
	body, ok := ttLast(out)
	if !ok {
		return 0, fmt.Errorf("%s: q inst gave no reply", why)
	}
	inst, ok := body.num("inst")
	if !ok {
		return 0, fmt.Errorf("%s: q inst gave no instance id", why)
	}
	return inst, nil
}

// pump reads the event log once.
func (sl *scoutLive) pump(ctx context.Context, waitMS int) ([]string, error) {
	var page events.Page
	params := map[string]any{"since": sl.eventsAt, "kinds": []string{"system", "addon"}, "wait_ms": waitMS}
	if err := sl.agent.Call(ctx, "events", params, &page, time.Duration(waitMS)*time.Millisecond+15*time.Second); err != nil {
		return nil, err
	}
	sl.eventsAt = page.Next
	lines := make([]string, 0, len(page.Events))
	for _, e := range page.Events {
		if line, ok := tbm.Line(e); ok {
			sl.logReply(line)
			lines = append(lines, line)
			continue
		}
		if e.Kind == events.KindSystem && e.Text != "" {
			lines = append(lines, e.Text)
		}
	}
	return lines, nil
}

// drainEvents moves the cursor past what the agent logged before this run.
func (sl *scoutLive) drainEvents(ctx context.Context) error {
	for {
		var page events.Page
		params := map[string]any{"since": sl.eventsAt, "wait_ms": 0, "limit": events.MaxLimit}
		if err := sl.agent.Call(ctx, "events", params, &page, 20*time.Second); err != nil {
			return scoutStopf(exitLivePhaseRefused, "events: %v", err)
		}
		sl.eventsAt = page.Next
		if !page.More {
			return nil
		}
	}
}

// waitMap waits until the agent reports mapID.
func (sl *scoutLive) waitMap(ctx context.Context, mapID uint32, timeout time.Duration) error {
	deadline := sl.now().Add(timeout)
	for {
		st, err := sl.status(ctx)
		if err == nil && st.Map == mapID {
			return nil
		}
		if !sl.now().Before(deadline) {
			return scoutStopf(exitLivePhaseRefused, "the commander is not on map %d after %s", mapID, timeout)
		}
		if err := sl.sleep(ctx, time.Second); err != nil {
			return err
		}
	}
}

// portalMap is the portal's map (0 without one).
func (sl *scoutLive) portalMap() uint32 {
	if sl.plan != nil && sl.plan.Portal != nil {
		return sl.plan.Portal.Map
	}
	return 0
}

// newID is the next request id (1-9 digits, wrapping inside the range).
func (sl *scoutLive) newID() uint32 {
	sl.nextID++
	if sl.nextID == 0 || sl.nextID > 999999999 {
		sl.nextID = 1
	}
	return sl.nextID
}

// options is the plan's rule set for the live merge and its reads.
func (sl *scoutLive) options() planscout.Options {
	o := planscout.Options{PartyLevel: sl.opts.partyLevel}
	if sl.plan != nil {
		o.Raid = sl.plan.IsRaid()
	}
	return o
}

func (sl *scoutLive) partyLevel() int {
	if sl.opts.partyLevel != 0 {
		return sl.opts.partyLevel
	}
	if sl.plan != nil {
		if _, hi := sl.plan.Band(); hi != 0 {
			return hi
		}
	}
	return 60
}

func (sl *scoutLive) aggroRate() float32 {
	if sl.data.CfgOK && sl.data.Cfg.RateCreatureAggro > 0 {
		return float32(sl.data.Cfg.RateCreatureAggro)
	}
	return 1
}

// stopReason is an error's text without the stop type.
func stopReason(err error) string {
	if err == nil {
		return ""
	}
	return err.Error()
}

// firstOutLine is a subprocess report's first line.
func firstOutLine(s string) string {
	s = strings.TrimSpace(s)
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		s = s[:i]
	}
	if s == "" {
		return "no output"
	}
	return s
}
