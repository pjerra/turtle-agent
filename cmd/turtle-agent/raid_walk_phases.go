// raid_walk_phases.go: phases 2-13 of `turtle-agent raid-walk` (spec 3.3). Every phase takes the walk and ends
// either nil or a *walkStop; the summary and teardown run in stop/done so no path skips them.
package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/pjerra/turtle-agent/internal/dungeon"
	"github.com/pjerra/turtle-agent/internal/events"
	"github.com/pjerra/turtle-agent/internal/packet"
	"github.com/pjerra/turtle-agent/internal/planexport"
	"github.com/pjerra/turtle-agent/internal/raidwalk"
	"github.com/pjerra/turtle-agent/internal/session"
	"github.com/pjerra/turtle-agent/internal/tbm"
)

// noUntilText is the refusal of a server whose run leader has no `until` (spec 3.3 phase 9).
const noUntilText = "the server's run leader has no `until` (TortoiseBots feat/run-until); raid-walk never walks without it"

// walkUntilStepRe is the step number right after the until label of a start line ("until boss (step 29)");
// anything before the label, e.g. a note's own "(step 5)", is not the until step (spec raid-walk-addons 1.7).
var walkUntilStepRe = regexp.MustCompile(`^ \(step ([0-9]+)\)`)

// phases runs the walk's phases in order (spec 3.3). It always ends in a summary and a teardown.
func (w *walk) phases(ctx context.Context) int {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	w.cancel = cancel
	// A panic anywhere in the walk still releases the injector's frozen bots before it propagates.
	defer func() {
		if rec := recover(); rec != nil {
			if w.inj != nil {
				w.inj.Cleanup(context.Background())
			}
			panic(rec)
		}
	}()

	// Any exit before the raid has entered gives the instance-cap reservation back (phaseEnter's success confirms it).
	defer w.releaseCapReservation()
	// --reuse-instance keeps the raid's instance: no fresh instance is entered, so nothing is counted or reserved.
	if !w.opts.reuseInstance {
		if err := w.phaseInstanceCap(ctx); err != nil {
			return w.stop(err, false)
		}
	}
	if err := w.phaseLease(ctx); err != nil {
		return w.stop(err, false)
	}
	if err := w.phaseSetup(ctx); err != nil {
		w.releaseLease()
		return w.stop(err, false)
	}
	if err := w.phaseLogin(ctx); err != nil {
		return w.stop(err, true)
	}
	if err := w.drainEvents(ctx); err != nil {
		return w.stop(err, true)
	}
	if err := w.phasePortal(ctx); err != nil {
		return w.stop(err, true)
	}
	if err := w.phaseUnbindSelf(ctx); err != nil {
		return w.stop(err, true)
	}
	if err := w.phaseHire(ctx); err != nil {
		return w.stop(err, true)
	}
	// The exact instance cap: the hire reply names no pool accounts, so read them now (before anything enters) and
	// make the per-account count the gate. A refused read leaves the list empty and the ledger keeps the raid count.
	w.hireAccounts = w.readHireAccounts(ctx)
	if err := w.waitHireAccountCap(ctx); err != nil {
		return w.stop(err, true)
	}
	if err := w.phaseBind(ctx); err != nil {
		return w.stop(err, true)
	}
	if w.opts.reuseInstance {
		// The raid is already inside: no entry order (which opens a fresh copy), only a check that it is alive in the
		// plan's map (walk 32: every restart re-entered at the leg start and burned a fresh instance). When the
		// previous run's party logged out but the commander still holds its bind and the instance has not expired, the
		// walk resumes that instance instead (walk 33); with no bind it says so and falls back to a fresh entry.
		ready, err := w.reuseReady(ctx)
		if err != nil {
			return w.stop(err, true)
		}
		switch {
		case ready:
			w.reuseMode = "alive"
		default:
			if inst, ttr, ok := w.reuseBind(ctx); ok {
				w.reuseMode, w.reuseBindInst, w.reuseBindTTR = "bind", inst, ttr
				w.note("place: --reuse-instance: the previous raid has logged out, but the commander is still bound to map %d instance %.0f (expires in %s): resuming it, no fresh instance",
					w.plan.Map, inst, ttr)
			} else {
				w.reuseMode = "fresh"
				w.note("place: --reuse-instance: the previous raid has logged out and no live bind for map %d is left: falling back to a fresh instance", w.plan.Map)
				// The fresh fallback is a real entry: the cap check skipped for --reuse-instance runs now, and the
				// commander's stale personal bind (if any) is dropped first, or .appear would land in an old copy.
				if err := w.phaseInstanceCap(ctx); err != nil {
					return w.stop(err, false)
				}
				if err := w.phaseUnbindSelf(ctx); err != nil {
					return w.stop(err, true)
				}
				if err := w.phaseEnter(ctx); err != nil {
					return w.stop(err, true)
				}
				w.recordFreshInstance()
			}
		}
	} else {
		if err := w.phaseEnter(ctx); err != nil {
			return w.stop(err, true)
		}
		w.recordFreshInstance()
	}
	if err := w.phasePlace(ctx); err != nil {
		return w.stop(err, true)
	}
	// The placement read the instance id: a fresh entry records it (a later
	// reuse's dedupe needs it), a reuse placement records the accounts it
	// re-hired into the raid's instance (M5).
	if w.opts.reuseInstance && w.reuseMode != "fresh" {
		w.recordReuseInstance()
	} else {
		w.stampInstance()
	}
	// --gm-instance-data: a placed start inside the leg fires BEFORE the run
	// starts, in every --until mode including --until none (instance data
	// is no until: it needs no label). It fires on the placement's leg
	// after place+enter fixed the instance, so `run start` (phaseStart
	// below) always follows the lines. An explicit --from step N inside the
	// leg fires the same way: PlaceLegName(w.rows, N) resolves the leg the
	// row belongs to (even a pull row: the gate is the leg's, not the
	// row's). (checkStartLine stays clean: the run's own label checks run
	// there, the lines go out here.)
	if len(w.gmInstanceData) > 0 {
		leg := ""
		if w.place != nil {
			leg = w.place.Leg
			if leg == "" {
				// An explicit --from step N names no leg (PlaceStart
				// returns "" for step starts): PlaceLegName(w.rows, N)
				// resolves the leg the staged row belongs to, even a
				// pull row — the gate is the leg's, not the row's.
				leg = raidwalk.PlaceLegName(w.rows, w.place.Start)
			}
		} else if n, err := raidwalk.FromStartStep(w.rows, w.fromWords); err == nil && n >= 1 && n <= len(w.rows.Steps) {
			leg = raidwalk.PlaceLegName(w.rows, n)
		}
		if leg != "" {
			if err := w.gmInstanceDataStart(ctx, w.gmInstanceDataInst(ctx), leg); err != nil {
				return w.stop(err, true)
			}
		}
	}
	if err := w.phaseStart(ctx); err != nil {
		return w.stop(err, true)
	}
	if w.inj != nil {
		if err := w.inj.Prepare(ctx); err != nil {
			return w.stop(err, true)
		}
	}
	if err := w.phaseWatch(ctx); err != nil {
		return w.stop(err, true)
	}
	w.endReason = w.tracker.End().Reason()
	w.ended = w.env.now()
	if w.inj != nil {
		w.inj.Cleanup(ctx)
	}
	_ = w.phaseHold(ctx)
	w.ended = w.env.now()
	w.phaseTeardown()
	if w.dash != nil {
		w.pullDecisions(ctx)
	}
	code := exitOK
	if len(w.hiresLeft) > 0 {
		code = exitRaidWalkIncident
	}
	w.finish(code)
	return w.exitCode
}

// stop ends a failed or incident walk: it stops the run, tears down (when a slot exists), writes the summary.
func (w *walk) stop(err error, teardown bool) int {
	code, reason := stopInfo(err)
	w.endReason = reason
	if w.inj != nil {
		w.inj.Cleanup(context.Background())
	}
	_ = w.stopRun(context.Background(), "")
	if teardown {
		w.phaseTeardown()
	}
	if code == exitOK && len(w.hiresLeft) > 0 {
		code = exitRaidWalkIncident
	}
	w.ended = w.env.now()
	if w.dash != nil {
		w.pullDecisions(context.Background())
	}
	w.finish(code)
	return w.exitCode
}

// phaseLease is phase 2: the slot lease, renewed every 30 minutes.
func (w *walk) phaseLease(ctx context.Context) error {
	ttl := time.Duration(w.opts.ttlMinutes()) * time.Minute
	anySlot := w.opts.slot == ""
	acq, err := w.env.acquire(w.opts.server, w.opts.slot, anySlot, w.opts.holder, ttl)
	if err != nil {
		return stopf(exitSlotRefused, "lease %s/%s: %v", w.opts.server, w.opts.slot, err)
	}
	w.lease = acq.Lease
	w.logLine(w.env.now(), fmt.Sprintf("lease %s/%s (holder %s) until %s", w.lease.Server, w.lease.Slot, w.lease.Holder, w.lease.Expires.UTC().Format(time.RFC3339)))
	go func() {
		t := time.NewTicker(30 * time.Minute)
		defer t.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-t.C:
				if _, err := w.env.renew(w.lease.Server, w.lease.Slot, w.lease.Holder, w.lease.LeaseID, ttl); err != nil {
					w.leaseLost.Store(true)
					if w.cancel != nil {
						w.cancel()
					}
					return
				}
			}
		}
	}()
	return nil
}

// releaseLease gives the lease back when the walk never reached teardown (teardown releases it itself).
func (w *walk) releaseLease() {
	if w.lease.Slot == "" || w.env.release == nil {
		return
	}
	if _, err := w.env.release(w.lease.Server, w.lease.Slot, w.lease.Holder, w.lease.LeaseID, false); err != nil {
		w.note("lease release: %v", err)
	}
}

// phaseSetup is phase 3: slot setup in process; its stdout is saved.
func (w *walk) phaseSetup(ctx context.Context) error {
	dir := filepath.Join(w.outDir, "slot")
	args := []string{w.opts.server, w.lease.Slot, "--holder", w.opts.holder, "--dir", dir, "--dungeons-from", w.opts.dir}
	if w.opts.binary != "" {
		args = append(args, "--binary", w.opts.binary)
	}
	if w.opts.mmaps != "" {
		args = append(args, "--mmaps-dir", w.opts.mmaps)
	}
	if w.opts.slotsPath != "" {
		args = append(args, "--slots", w.opts.slotsPath)
	}
	var buf bytes.Buffer
	code := w.env.setup(args, &buf, &buf)
	if err := w.writeOut("slot-setup.txt", buf.Bytes()); err != nil {
		return stopf(exitRaidWalkFailed, "slot-setup.txt: %v", err)
	}
	if code != exitOK {
		return stopf(exitRaidWalkFailed, "slot setup failed (exit %d): %s", code, firstLine(buf.String()))
	}
	w.slotDir = dir
	m, err := readWalkManifest(dir)
	if err != nil {
		return stopf(exitRaidWalkFailed, "slot setup: %v", err)
	}
	w.manifest = m
	w.logLine(w.env.now(), "slot ready in "+dir)
	return nil
}

// phaseLogin is phase 4: start the slot agent and wait for in_world.
func (w *walk) phaseLogin(ctx context.Context) error {
	pid, err := w.env.startAgent(w.manifest)
	if err != nil {
		return stopf(exitRaidWalkFailed, "login: %v", err)
	}
	w.logLine(w.env.now(), fmt.Sprintf("login started (pid %d)", pid))
	deadline := w.env.now().Add(90 * time.Second)
	for {
		agent, m, err := w.deps.openAgent(w.slotDir)
		if err == nil {
			w.agent, w.manifest = agent, m
			break
		}
		if !w.env.now().Before(deadline) {
			return stopf(exitRaidWalkFailed, "login: no agent in %s after 90 s (%v; see %s/agent.out)", w.slotDir, err, w.slotDir)
		}
		if err := w.env.sleep(ctx, time.Second); err != nil {
			return stopf(exitRaidWalkFailed, "interrupted")
		}
	}
	for {
		if err := ctx.Err(); err != nil {
			return stopf(exitRaidWalkFailed, "interrupted")
		}
		st, err := w.status(ctx)
		if err == nil && st.State == session.StatusInWorld {
			return nil
		}
		if !w.env.now().Before(deadline) {
			return stopf(exitRaidWalkFailed, "login: not in world after 90 s (state %q; see %s/agent.out)", stateWord(st), w.slotDir)
		}
		if err := w.env.sleep(ctx, 2*time.Second); err != nil {
			return stopf(exitRaidWalkFailed, "interrupted")
		}
	}
}

func stateWord(st session.StatusInfo) string {
	if st.State == "" {
		return "?"
	}
	return string(st.State)
}

// drainEvents moves the events cursor past what the agent logged before this walk, so a stale HIRE_RAID,
// TBM:RUN or TBM:RAID_LAYOUT line cannot answer a wait of this walk.
func (w *walk) drainEvents(ctx context.Context) error {
	for {
		var page events.Page
		params := map[string]any{"since": w.eventsAt, "wait_ms": 0, "limit": events.MaxLimit}
		if err := w.agent.Call(ctx, "events", params, &page, 20*time.Second); err != nil {
			return stopf(exitRaidWalkFailed, "events: %v", err)
		}
		w.eventsAt = page.Next
		if !page.More {
			return nil
		}
	}
}

// phasePortal is phase 5: GM to the entrance portal and wait for its map.
func (w *walk) phasePortal(ctx context.Context) error {
	p := w.plan.Portal
	line := fmt.Sprintf(".go xyz %s %s %s %d", num32(p.X), num32(p.Y), num32(p.Z), p.Map)
	if err := w.gm(ctx, line); err != nil {
		return err
	}
	return w.waitMap(ctx, p.Map, 30*time.Second, time.Time{})
}

// hireMaxRetries is how often a short t/h/d batch is retried before the walk fails (walk 12 s4: "no mercenary of
// that kind is available right now. Try again shortly").
const hireMaxRetries = 3

// hireRetryWait is the rest between a short batch reply and its retry.
const hireRetryWait = 30 * time.Second

// phaseHire is phase 6: the batches, the party diff and the gear preset.
func (w *walk) phaseHire(ctx context.Context) error {
	if f := w.roster.CommanderFaction; f != "" {
		// TortoiseBots' hire fills the commander's own faction only, so a roster of the other side cannot be
		// hired at all (a Horde commander gets no paladin, an Alliance one no shaman). Fail before the first
		// hire, not after a party of the wrong side has been bought.
		st, err := w.status(ctx)
		if err != nil {
			return stopf(exitRaidWalkFailed, "roster: commander faction %s: status: %v", f, err)
		}
		switch st.Team {
		case f:
			w.logLine(w.env.now(), fmt.Sprintf("roster: commander %s is %s, the roster wants %s", st.Character, st.Team, f))
		case "":
			return stopf(exitRaidWalkFailed, "roster: the roster wants a %s commander, but %s's faction is unknown (no race yet); hire from a %s commander", f, st.Character, f)
		default:
			return stopf(exitRaidWalkFailed, "roster: the roster wants a %s commander, but %s is %s: the module hires the commander's own faction, so hire from a %s commander",
				f, st.Character, st.Team, f)
		}
	}
	lines := w.roster.HireLines()
	for i, b := range w.roster.Batches {
		// The server hires at most HireCap bots: a roster that asks for more (the 40-man raids) has its last
		// batches skipped whole, so a one-bot last batch is not refused as "maximum companions" (v2 rosters
		// name every bot's class, so the last seat is often a batch of its own).
		if len(w.hires) >= raidwalk.HireCap {
			w.note("hire cap %d: batch %s skipped, the raid is full", raidwalk.HireCap, b.Hire)
			w.logLine(w.env.now(), fmt.Sprintf("hire cap %d: batch %s skipped", raidwalk.HireCap, b.Hire))
			continue
		}
		before, err := w.partyNames(ctx)
		if err != nil {
			return stopf(exitRaidWalkFailed, "party: %v", err)
		}
		if err := w.gm(ctx, lines[i]); err != nil {
			return err
		}
		line, ok, err := w.waitTBM(ctx, "TBM:HIRE_RAID|", 60*time.Second)
		if err != nil {
			return err
		}
		if !ok {
			return stopf(exitRaidWalkFailed, "hire %s: no TBM:HIRE_RAID line in 60 s", b.Hire)
		}
		reply := parseHireReply(line)
		lastLine := line
		// got counts the bots this batch hired so far, per role; missing is the batch's own t/h/d counts minus
		// got. Only t/h/d batches are retried: a class batch ("1 warrior:tank") names one exact bot and fails as
		// now.
		wantT, wantH, wantD, countable := raidwalk.RoleCounts(b.Hire)
		gotT, gotH, gotD := reply.tank, reply.healer, reply.dps
		hired := reply.hired
		retries := 0
		for countable && reply.retryable() && retries < hireMaxRetries {
			missing := hireShortfall(max(wantT-gotT, 0), max(wantH-gotH, 0), max(wantD-gotD, 0))
			if missing == "" {
				break
			}
			retries++
			w.note("hire %s: %d of %d hired (%s); retry %d in 30 s for %s", b.Hire, reply.hired, reply.planned, reply.status, retries, missing)
			if err := w.env.sleep(ctx, hireRetryWait); err != nil {
				return stopf(exitRaidWalkFailed, "interrupted")
			}
			if err := w.gm(ctx, w.roster.HireLine(missing)); err != nil {
				return err
			}
			line, ok, err = w.waitTBM(ctx, "TBM:HIRE_RAID|", 60*time.Second)
			if err != nil {
				return err
			}
			if !ok {
				return stopf(exitRaidWalkFailed, "hire %s: no TBM:HIRE_RAID line in 60 s", b.Hire)
			}
			lastLine = line
			reply = parseHireReply(line)
			hired += reply.hired
			gotT += reply.tank
			gotH += reply.healer
			gotD += reply.dps
		}
		// A partial that reaches the cap is answered ok by the happy-path rule below: the 40th-seat check
		// owns it (amendment G2), so a capped retry loop still counts as a full hire.
		if countable && hired > 0 && len(w.hires)+hired == raidwalk.HireCap {
			reply.status = "ok"
		}
		if reply.status != "ok" || hired == 0 {
			if hired == 0 {
				w.note("check the commander's gold")
			}
			if retries == 0 {
				return stopf(exitRaidWalkFailed, "hire %s: %s", b.Hire, lastLine)
			}
			return stopf(exitRaidWalkFailed, "hire %s: %s after %d retries", b.Hire, lastLine, retries)
		}
		// A capped retry's reply may count the requested order rather than the seats (its hired field can
		// overshoot what the server fit under the cap), so the join wait is capped exactly like the observed
		// growth check below: at most the seats the server can fill.
		want := min(len(before)+hired, raidwalk.HireCap)
		after, err := w.waitPartyNames(ctx, want, 30*time.Second)
		if err != nil {
			return err
		}
		added := newNames(before, after)
		// The 40th seat (amendment G2): the server's hire cap ends the last batch early, and its reply may count the
		// order rather than the seats. Any other shortfall is a failed hire.
		if len(added) != hired && !(len(w.hires)+len(added) == raidwalk.HireCap && len(added) < hired) {
			return stopf(exitRaidWalkFailed, "hire %s: the reply says %d hired, the party grew by %d", b.Hire, hired, len(added))
		}
		for _, n := range added {
			w.hires[n] = true
		}
		switch b.Role {
		case raidwalk.RoleMT:
			if len(added) != 1 {
				return stopf(exitRaidWalkFailed, "the mt batch added %d names, want exactly 1", len(added))
			}
			w.mt = added[0]
		case raidwalk.RoleOT:
			w.ots = append(w.ots, added...)
		case raidwalk.RoleRest:
			w.rest = append(w.rest, added...)
		}
		w.logLine(w.env.now(), fmt.Sprintf("hired %s: %s", b.Role, strings.Join(added, ", ")))
		if b.Spec != "" {
			// The roster's spec goes on every bot of the batch before the refit: the tier kit follows the
			// bot's own spec, so the spec has to be in place first.
			class := b.Class()
			for _, n := range added {
				if err := w.applySpec(ctx, n, b.Spec); err != nil {
					return err
				}
				w.logLine(w.env.now(), fmt.Sprintf("roster: %s %s %s", n, class, b.Spec))
			}
		}
	}
	// A roster asks for the raid's every seat; the server's hire cap fills all but the last one (amendment G2).
	requested := w.roster.Total()
	switch hired := len(w.hires); {
	case hired == requested:
	case hired == raidwalk.HireCap:
		w.note("hire cap %d: asked for %d bots, the server hired %d", raidwalk.HireCap, requested, hired)
		w.logLine(w.env.now(), fmt.Sprintf("hire cap %d: %d of %d hired", raidwalk.HireCap, hired, requested))
	default:
		return stopf(exitRaidWalkFailed, "hire: asked for %d bots, the party has %d", requested, hired)
	}
	if w.opts.hireSettle == 0 {
		w.logLine(w.env.now(), "no wait for the hires' join summons (--hire-settle 0)")
	} else {
		w.logLine(w.env.now(), fmt.Sprintf("waiting %s for the last hires' join summons", durWords(w.opts.hireSettle)))
		if err := w.env.sleep(ctx, w.opts.hireSettle); err != nil {
			return stopf(exitRaidWalkFailed, "interrupted")
		}
	}
	// The refit at the walk's tier (amendment G1): the hire wears the raid's minimum kit, and a comparison run its
	// override. A failed refit is a failed walk: the tier is what the run is judged against.
	line := fmt.Sprintf(".bot gear raid %s tier %d", w.manifest.Character, w.roster.Tier)
	if err := w.gm(ctx, line); err != nil {
		return err
	}
	gear, ok, err := w.waitTBM(ctx, "TBM:GEAR|", 60*time.Second)
	if err != nil {
		return err
	}
	if !ok {
		return stopf(exitRaidWalkFailed, "gear: no TBM:GEAR line in 60 s")
	}
	if !strings.HasPrefix(gear, "TBM:GEAR|ok|") {
		return stopf(exitRaidWalkFailed, "gear: %s", gear)
	}
	w.tracker = raidwalk.NewTracker(w.plan, w.hireList())
	return nil
}

// hireSettleWait follows the last hire batch before the refit and the bind. A big hire answers while its bots are
// still on their join summon (about one a second), and a hire whose join lands after the bind is removed from the
// group by the server. Walk 12 (ZG, 17-bot batches): the last join came 4-8 s after the answer. The party list is
// no help: it names a hire before its join, and its zone there stays stale after it. This is the default of
// --hire-settle; 0 skips the wait, which is how a walk proves TortoiseBots#1367's bind keeps its late joiners.
const hireSettleWait = 20 * time.Second

// hireReply is one TBM:HIRE_RAID line read field by field (TortoiseBots commands/Hire.cpp:124):
// TBM:HIRE_RAID|<ok|partial|refused>|<hired>|<planned>|<tank>|<healer>|<dps>|<tier>|<feeCopper>|<reason>.
// The tank, healer and dps fields are the counts hired by this order.
type hireReply struct {
	status  string
	hired   int
	planned int
	tank    int
	healer  int
	dps     int
	reason  string
}

// parseHireReply reads a TBM:HIRE_RAID line into its fields. A line that does not parse reports the zero reply
// with an empty status, which retries nothing and hires nothing.
func parseHireReply(line string) hireReply {
	var r hireReply
	rest, found := strings.CutPrefix(raidwalk.CleanLine(line), "TBM:HIRE_RAID|")
	if !found {
		return r
	}
	f := strings.Split(rest, "|")
	if len(f) < 9 {
		return r
	}
	n, err := strconv.Atoi(strings.TrimSpace(f[1]))
	if err != nil {
		return hireReply{}
	}
	p, _ := strconv.Atoi(strings.TrimSpace(f[2]))
	t, _ := strconv.Atoi(strings.TrimSpace(f[3]))
	h, _ := strconv.Atoi(strings.TrimSpace(f[4]))
	d, _ := strconv.Atoi(strings.TrimSpace(f[5]))
	return hireReply{status: f[0], hired: n, planned: p, tank: t, healer: h, dps: d, reason: strings.Join(f[8:], "|")}
}

// retryable says whether a short batch reply may be retried: "partial" always, "refused" only when the reason asks
// to try again shortly.
func (r hireReply) retryable() bool {
	switch r.status {
	case "partial":
		return true
	case "refused":
		return strings.Contains(r.reason, "Try again shortly")
	default:
		return false
	}
}

// hireShortfall builds the "<n>t <n>h <n>d" retry grammar for what is still missing, zero parts left out.
func hireShortfall(tanks, healers, dps int) string {
	var parts []string
	if tanks > 0 {
		parts = append(parts, fmt.Sprintf("%dt", tanks))
	}
	if healers > 0 {
		parts = append(parts, fmt.Sprintf("%dh", healers))
	}
	if dps > 0 {
		parts = append(parts, fmt.Sprintf("%dd", dps))
	}
	return strings.Join(parts, " ")
}

// partyNames is the party's bot names.
func (w *walk) partyNames(ctx context.Context) ([]string, error) {
	p, err := w.party(ctx)
	if err != nil {
		return nil, err
	}
	out := make([]string, 0, len(p.Members))
	for _, m := range p.Members {
		out = append(out, m.Name)
	}
	return out, nil
}

// waitPartyNames polls the party until it holds want members.
func (w *walk) waitPartyNames(ctx context.Context, want int, timeout time.Duration) ([]string, error) {
	deadline := w.env.now().Add(timeout)
	for {
		names, err := w.partyNames(ctx)
		if err != nil {
			return nil, stopf(exitRaidWalkFailed, "party: %v", err)
		}
		if len(names) >= want {
			return names, nil
		}
		if !w.env.now().Before(deadline) {
			return nil, stopf(exitRaidWalkFailed, "party: %d of %d hires joined in %s", len(names), want, durWords(timeout))
		}
		if err := w.env.sleep(ctx, time.Second); err != nil {
			return nil, stopf(exitRaidWalkFailed, "interrupted")
		}
	}
}

// newNames are the names of after that before did not hold, in after's order.
func newNames(before, after []string) []string {
	had := make(map[string]bool, len(before))
	for _, n := range before {
		had[n] = true
	}
	var out []string
	for _, n := range after {
		if !had[n] {
			out = append(out, n)
		}
	}
	return out
}

// phaseBind is phase 7: commander bind to the MT, the layout check and the view.
func (w *walk) phaseBind(ctx context.Context) error {
	reply, err := w.commander(ctx, commanderReq{Op: "bind", LeadBot: w.mt, Bots: []string{"raid"}, WaitMS: 10000})
	if err != nil {
		return stopf(exitRaidWalkFailed, "bind: %v", err)
	}
	if reply.Reply.Err != nil {
		return stopf(exitRaidWalkFailed, "bind: %s", reply.Reply.Err.Message)
	}
	if !reply.Reply.OK && reply.Reply.Kind != tbm.KindAck {
		return stopf(exitRaidWalkFailed, "bind: no ACK (%s)", reply.Reply.Line)
	}
	if err := w.waitLayout(ctx); err != nil {
		return err
	}
	on := true
	vr, err := w.commander(ctx, commanderReq{Op: "view", On: &on, WaitMS: 5000})
	if err != nil {
		w.note("commander view: %v", err)
	} else if vr.Reply.Err != nil {
		w.note("commander view: %s", vr.Reply.Err.Message)
	}
	return nil
}

// waitLayout waits for the first TBM:RAID_LAYOUT data line and checks the tanks against the roster. A warning
// line is kept as a summary warning and the data line is still awaited.
func (w *walk) waitLayout(ctx context.Context) error {
	deadline := w.env.now().Add(10 * time.Second)
	for {
		remaining := deadline.Sub(w.env.now())
		if remaining <= 0 {
			return stopf(exitRaidWalkFailed, "bind: no TBM:RAID_LAYOUT line in 10 s")
		}
		if remaining > time.Second {
			remaining = time.Second
		}
		line, ok, err := w.waitTBMMatch(ctx, remaining, func(l string) bool {
			return strings.HasPrefix(l, "TBM:RAID_LAYOUT|")
		})
		if err != nil {
			return err
		}
		if !ok {
			continue
		}
		clean := raidwalk.CleanLine(line)
		f := strings.Split(strings.TrimPrefix(clean, "TBM:RAID_LAYOUT|"), "|")
		if len(f) < 1 {
			return stopf(exitRaidWalkFailed, "bind: bad layout line %s", line)
		}
		if f[0] == "warning" {
			w.layout.Warnings = append(w.layout.Warnings, strings.Join(f[1:], "|"))
			w.note("raid layout warning: %s", strings.Join(f[1:], "|"))
			continue
		}
		if len(f) < 2 {
			return stopf(exitRaidWalkFailed, "bind: bad layout line %s", line)
		}
		tanks := strings.Split(f[1], ",")
		for i := range tanks {
			tanks[i] = strings.TrimSpace(tanks[i])
		}
		if len(tanks) == 0 || tanks[0] == "" {
			return stopf(exitRaidWalkFailed, "bind: the layout lists no main tank: %s", line)
		}
		if !strings.EqualFold(tanks[0], w.mt) {
			return stopf(exitRaidWalkFailed, "the layout's main tank is %s, not the roster's MT %s", tanks[0], w.mt)
		}
		w.layout.MT = tanks[0]
		w.layout.Tanks = tanks
		for _, ot := range w.ots {
			if !containsFold(tanks, ot) {
				w.layout.Warnings = append(w.layout.Warnings, "the layout does not list the off-tank "+ot)
				w.note("the layout does not list the off-tank %s", ot)
			}
		}
		return nil
	}
}

func containsFold(list []string, s string) bool {
	for _, v := range list {
		if strings.EqualFold(v, s) {
			return true
		}
	}
	return false
}

// phaseEnter is phase 8: near, tele or auto, then the straggler ladder.
func (w *walk) phaseEnter(ctx context.Context) error {
	tele := w.opts.enter == "tele"
	if !tele {
		code, err := w.enterNear(ctx)
		if err != nil {
			if w.opts.enter != "auto" || code != "requirement" {
				return err
			}
			if w.plan.EntranceTele == "" {
				return stopf(exitRaidWalkFailed, "cmd enter near was refused (%s) and plan %s has no entrance_tele: cannot teleport the hires", code, w.plan.Name)
			}
			w.note("cmd enter near was refused (%s): teleporting the hires instead", code)
			tele = true
		}
	}
	if tele {
		if err := w.teleHires(ctx); err != nil {
			return err
		}
	}
	// The first wait is best effort: the ladder below gives a straggler its one re-teleport or summon.
	if err := w.waitEntered(ctx, 60*time.Second); err != nil && ctx.Err() != nil {
		return err
	}
	missing := w.missingHires(w.env.now())
	if len(missing) == 0 {
		return nil
	}
	for _, name := range missing {
		if tele {
			if err := w.gm(ctx, fmt.Sprintf(".tele name %s %s", name, w.plan.EntranceTele)); err != nil {
				return err
			}
			_ = w.env.sleep(ctx, 100*time.Millisecond)
			continue
		}
		rep, err := w.commander(ctx, commanderReq{Op: "cmd", Verb: "summon", Args: []string{name}, WaitMS: 10000})
		if err != nil || rep.Reply.Err != nil {
			w.note("summon %s: %s", name, replyProblem(rep, err))
		}
	}
	if err := w.waitEntered(ctx, 30*time.Second); err != nil {
		missing = w.missingHires(w.env.now())
		return stopf(exitRaidWalkFailed, "not in the instance: %s", strings.Join(missing, ", "))
	}
	return nil
}

func replyProblem(rep commanderReply, err error) string {
	switch {
	case err != nil:
		return err.Error()
	case rep.Reply.Err != nil:
		return rep.Reply.Err.Message
	case rep.Reply.Line != "":
		return rep.Reply.Line
	}
	return "no reply"
}

// enterBusyTries bounds the wait for hires still busy with their join summon: a late joiner of a
// big hire is summoned to the commander a few seconds after the hire answers (walk 12, ZG).
const enterBusyTries = 12

// enterNear sends the module's own entry order; the ERR code is returned so auto can fall back.
// A "busy" refusal is retried every 5 s, at most enterBusyTries times.
func (w *walk) enterNear(ctx context.Context) (string, error) {
	var rep commanderReply
	var err error
	for try := 1; ; try++ {
		rep, err = w.commander(ctx, commanderReq{Op: "cmd", Verb: "enter", Args: []string{"near"}, WaitMS: 15000})
		if err != nil || rep.Reply.Err == nil || rep.Reply.Err.Code != "busy" || try >= enterBusyTries {
			break
		}
		if try == 1 {
			w.note("cmd enter near: a hire is busy (%s); retrying every 5 s", rep.Reply.Err.Message)
		}
		if err := w.env.sleep(ctx, 5*time.Second); err != nil {
			return "", err
		}
	}
	if err != nil {
		return "", stopf(exitRaidWalkFailed, "enter near: %v", err)
	}
	if rep.Reply.Err != nil {
		return rep.Reply.Err.Code, stopf(exitRaidWalkFailed, "enter near: %s", rep.Reply.Err.Message)
	}
	if !rep.Reply.OK && rep.Reply.Kind != tbm.KindAck {
		return "", stopf(exitRaidWalkFailed, "enter near: no ACK (%s)", rep.Reply.Line)
	}
	return "", nil
}

// teleHires GM-teleports every hire to the plan's entrance_tele.
func (w *walk) teleHires(ctx context.Context) error {
	for _, name := range w.hireList() {
		if err := w.gm(ctx, fmt.Sprintf(".tele name %s %s", name, w.plan.EntranceTele)); err != nil {
			return err
		}
		if err := w.env.sleep(ctx, 100*time.Millisecond); err != nil {
			return stopf(exitRaidWalkFailed, "interrupted")
		}
	}
	return nil
}

// waitEntered waits until every hire's freshest frame state is on the plan's map.
func (w *walk) waitEntered(ctx context.Context, timeout time.Duration) error {
	deadline := w.env.now().Add(timeout)
	for {
		if err := ctx.Err(); err != nil {
			return stopf(exitRaidWalkFailed, "interrupted")
		}
		if n, frame, err := w.view(ctx, w.afterN, 2000); err == nil && frame != nil {
			w.afterN = n
			w.trackFrame(w.env.now(), frame)
		}
		if len(w.missingHires(w.env.now())) == 0 {
			return nil
		}
		if !w.env.now().Before(deadline) {
			return stopf(exitRaidWalkFailed, "not in the instance: %s", strings.Join(w.missingHires(w.env.now()), ", "))
		}
		if err := w.env.sleep(ctx, 2*time.Second); err != nil {
			return stopf(exitRaidWalkFailed, "interrupted")
		}
	}
}

// missingHires are the hired bots whose freshest state (10 s) is not on the plan's map.
func (w *walk) missingHires(now time.Time) []string {
	var out []string
	for _, name := range w.hireList() {
		p, ok := w.botAt[name]
		if !ok || now.Sub(p.at) > 10*time.Second || p.mapID != w.plan.Map {
			out = append(out, name)
		}
	}
	return out
}

// phaseStart is phase 9: run start, and the until label the server must show.
func (w *walk) phaseStart(ctx context.Context) error {
	if w.place != nil {
		if err := w.placeMTReady(ctx); err != nil {
			return err
		}
	}
	args := []string{w.plan.Name}
	args = append(args, w.fromWords...)
	args = append(args, w.untilWords...)
	if w.opts.speed != 1.0 {
		args = append(args, fmt.Sprintf("speed=%.1f", w.opts.speed))
	}
	rep, err := w.commander(ctx, commanderReq{Op: "run", Verb: "start", Args: args, WaitMS: 10000})
	if err != nil {
		// A lost answer may still have started the run: a later stop is worth sending.
		w.runStarted = true
		return stopf(exitRaidWalkFailed, "run start: %v", err)
	}
	if rep.Reply.Err != nil {
		if len(w.untilWords) > 0 && untilRefusal(*rep.Reply.Err) {
			return stopf(exitRaidWalkFailed, "%s", noUntilText)
		}
		return stopf(exitRaidWalkFailed, "run start: %s", rep.Reply.Err.Message)
	}
	w.runStarted = true
	if !rep.Reply.OK && rep.Reply.Kind != tbm.KindAck {
		return stopf(exitRaidWalkFailed, "run start: no ACK (%s)", rep.Reply.Line)
	}
	// The server's own start text ("The run starts at step N of S (leg L)") is logged for the summary and the run.log,
	// but only for a placed walk: the note is labelled "place:" (review round 1, item 10).
	for _, l := range rep.Reply.Lines {
		if w.place != nil && strings.Contains(l, "run starts at step") {
			w.note("place: %s", strings.TrimSpace(raidwalk.CleanLine(l)))
		}
	}
	line, ok := firstRunLine(rep.Reply.Lines, w.plan.Name)
	if !ok {
		var werr error
		line, ok, werr = w.waitTBM(ctx, "TBM:RUN|"+w.plan.Name+"|", 15*time.Second)
		if werr != nil {
			return werr
		}
	}
	if !ok {
		return stopf(exitRaidWalkFailed, "run start: no TBM:RUN line for %s in 15 s", w.plan.Name)
	}
	return w.checkStartLine(ctx, line)
}

// untilRefusal says whether an ERR is the run leader refusing its until clause (spec phase 9).
func untilRefusal(e tbm.Err) bool {
	text := strings.ToLower(e.Code + " " + e.Message)
	return strings.Contains(text, "until") || strings.Contains(text, "invalid")
}

// firstRunLine is the first TBM:RUN line of plan in a reply's lines.
func firstRunLine(lines []string, plan string) (string, bool) {
	for _, l := range lines {
		st, ok := raidwalk.ParseRun(l)
		if ok && st.Plan == plan {
			return l, true
		}
	}
	return "", false
}

// adoptRunStatus makes st the walk's current run status: its log line, the tracker, the boss rule's previous status and the
// labels. A run start that is not a fresh run (a re-run of a leg's open row, rerunOpenStep) checks its line with it too.
func (w *walk) adoptRunStatus(st tbm.RunStatus, line string) {
	w.logLine(w.env.now(), line)
	if w.tracker != nil {
		w.tracker.Feed(w.env.now(), st)
	}
	// The boss rule reads the walk's previous status (a run ending right after the boss step), so it is observed
	// before this one replaces it.
	w.observeBossStatus(st, w.env.now())
	w.lastStatus, w.haveStatus, w.statusSeq = st, true, w.statusSeq+1
	w.noteLabel(st)
}

// checkStartLine feeds the start line to the tracker and checks it: running, and the until label.
func (w *walk) checkStartLine(ctx context.Context, line string) error {
	st, ok := raidwalk.ParseRun(line)
	if !ok {
		return stopf(exitRaidWalkFailed, "run start: %s", line)
	}
	w.adoptRunStatus(st, line)
	if st.State != tbm.RunRunning {
		return stopf(exitRaidWalkFailed, "run start: the run is %s: %s", st.State, line)
	}
	if w.place != nil && (st.Step != w.place.Start || st.Steps != len(w.rows.Steps)) {
		// The placement is only worth its hire when the server starts exactly the step it was placed for, in the rows
		// the place was computed from (spec 5.2).
		if err := w.gm(ctx, ".bot run stop"); err != nil {
			return err
		}
		w.runStopped = true
		return stopf(exitRaidWalkFailed, "place: placed for step %d of %d rows, the server starts step %d of %d", w.place.Start, len(w.rows.Steps), st.Step, st.Steps)
	}
	if w.bossDoneStep > 0 && st.Steps != len(w.rows.Steps) {
		// boss-done is "the step after the boss" in the rows' numbering: another step count means other rows on the server.
		if err := w.gm(ctx, ".bot run stop"); err != nil {
			return err
		}
		w.runStopped = true
		return stopf(exitRaidWalkFailed, "--until boss-done: the server's plan has %d steps, the rows the boss step was found in have %d (pass --sql with the deployed migration, or use --until leg L)", st.Steps, len(w.rows.Steps))
	}
	// (No --gm-instance-data here: it fires in phases after phaseStart
	// adopts this status, BEFORE the run starts, so the lines precede the
	// run start in every --until mode including --until none — and in
	// pollOnce, which holds the next pull while a mid-run firing sends.)
	if len(w.untilWords) == 0 {
		return nil
	}
	clean := raidwalk.CleanLine(line)
	if !strings.Contains(clean, w.untilLabel+" (step ") {
		// A run without the label is a run without until: stop it at once, no poll guard (spec phase 9).
		if err := w.gm(ctx, ".bot run stop"); err != nil {
			return err
		}
		w.runStopped = true
		return stopf(exitRaidWalkFailed, "%s", noUntilText)
	}
	if i := strings.Index(clean, w.untilLabel); i >= 0 {
		if m := walkUntilStepRe.FindStringSubmatch(clean[i+len(w.untilLabel):]); m != nil {
			w.untilStep, _ = strconv.Atoi(m[1])
		}
	}
	return nil
}

// noteLabel keeps the newest speed/until label of the run's status lines for the summary (the label drops once the run
// stopped at its until step, so the last line alone does not have it) and whether the test speed ever fell back.
func (w *walk) noteLabel(st tbm.RunStatus) {
	if st.Label != "" {
		w.runLabel = st.Label
	}
	if st.SpeedFellBack {
		w.speedFellBack = true
	}
}

// phaseWatch is phase 10: the run's status, the frames and the dashboard until the run ends or something stops it.
func (w *walk) phaseWatch(ctx context.Context) error {
	deadline := w.started.Add(time.Duration(w.opts.maxRunMinutes) * time.Minute)
	var ps struct {
		armed   bool
		at      time.Time
		note    string
		step    int
		lastTry time.Time
		skipped bool // a near-wipe recovery was skipped for this pause: the old ending, once
		// bossSkip says this pause's boss-not-found skip is settled: a `.bot run skip` went out, or its two tries
		// failed (then the pause path takes over). Until then the kill evidence is re-read on every tick (review
		// round 1, item 2). bossNoKill notes the first tick without evidence once, not every tick. gongStep is the
		// leg's open row this pause already re-ran to bring the boss back (0: none yet); a new pause re-arms it.
		bossSkip   bool
		bossNoKill bool
		gongStep   int
	}
	perStep := map[int]int{}
	autoTotal := 0
	for {
		if w.leaseLost.Load() {
			return stopf(exitRaidWalkFailed, "lease lost")
		}
		if err := ctx.Err(); err != nil {
			return stopf(exitRaidWalkFailed, "interrupted")
		}
		now := w.env.now()
		if now.After(deadline) {
			line := fmt.Sprintf(".bot run stop time limit reached (raid-walk %dm)", w.opts.maxRunMinutes)
			_ = w.stopRun(context.Background(), line)
			return stopf(exitRaidWalkFailed, "time limit reached (raid-walk %dm)", w.opts.maxRunMinutes)
		}
		incs, err := w.pollOnce(ctx, true)
		if err != nil {
			return err
		}
		for _, inc := range incs {
			switch v := inc.(type) {
			case raidwalk.IncidentStall:
				_ = w.stopRun(context.Background(), "")
				return stopf(exitRaidWalkFailed, "stalled at step %d (the same status line for 10 min)", v.Step)
			case raidwalk.IncidentWipe:
				if err := w.handleWipe(ctx, v.Step); err != errWipeRecovered {
					return err
				}
			case raidwalk.IncidentBossEngaged:
				ok, early, waitOut := w.runFightsBoss(ctx, v.Entry, true)
				// The resume's wait drains the tracker's incidents while it polls: feed every drained
				// incident to the normal handler exactly as phaseWatch would, in drain order, before
				// deciding the handoff. A wipe inside the wait runs the wipe recovery path (no handoff);
				// a stall inside the wait ends the walk stalled (no handoff).
				waitWipe := false
				for _, winc := range waitOut.incs {
					switch wv := winc.(type) {
					case raidwalk.IncidentStall:
						_ = w.stopRun(context.Background(), "")
						return stopf(exitRaidWalkFailed, "stalled at step %d (the same status line for 10 min)", wv.Step)
					case raidwalk.IncidentWipe:
						if err := w.handleWipe(ctx, wv.Step); err != errWipeRecovered {
							return err
						}
						waitWipe = true
					case raidwalk.IncidentBossEngaged:
						// The boss incident the wait drained is this one: the decision below owns it.
					}
				}
				// A wipe the wait drained that RECOVERED has already revived, rested and resumed the
				// run: normal watching resumes, so the stale pre-wipe incident v must never reach
				// handleBoss (it would stop the freshly recovered run and end the walk on the wrong
				// encounter, Codex HIGH round 3). Any other wipe-handler error ended the walk above.
				if waitWipe {
					continue
				}
				if ok {
					w.note("boss %d engaged at step %d while the run is paused on its boss row: resumed the run so the run leader fights it", v.Entry, v.Step)
					w.logLine(now, fmt.Sprintf("boss %d engaged at step %d while the run is paused on its boss row: resumed the run so the run leader fights it", v.Entry, v.Step))
					switch {
					case strings.TrimSpace(w.opts.until) == "none":
						w.note("boss %d engaged at step %d under --until none: the run leader fights it", v.Entry, v.Step)
						w.logLine(now, fmt.Sprintf("boss %d engaged at step %d under --until none: the run leader fights it", v.Entry, v.Step))
					case early > 0:
						w.note("boss %d engaged at step %d, before its boss step %d in the same leg, and the until (%s) goes past it, so the run leader fights it", v.Entry, v.Step, early, w.untilLabel)
						w.logLine(now, fmt.Sprintf("boss %d engaged at step %d, before its boss step %d in the same leg, and the until (%s) goes past it, so the run leader fights it", v.Entry, v.Step, early, w.untilLabel))
					default:
						w.note("boss %d engaged at step %d: the run is on its boss step and its until (%s) goes past it, so the run leader fights it", v.Entry, v.Step, w.untilLabel)
						w.logLine(now, fmt.Sprintf("boss %d engaged at step %d: the run is on its boss step and its until (%s) goes past it, so the run leader fights it", v.Entry, v.Step, w.untilLabel))
					}
					continue
				}
				return w.handleBoss(ctx, v)
			}
		}
		now = w.env.now()
		if w.inj != nil {
			if err := w.inj.Tick(ctx, now); err != nil {
				return err
			}
		}
		if w.haveStatus && w.lastStatus.State == tbm.RunPaused {
			if !ps.armed || ps.note != w.lastStatus.Note || ps.step != w.lastStatus.Step {
				ps.armed, ps.at, ps.note, ps.step, ps.lastTry, ps.skipped = true, now, w.lastStatus.Note, w.lastStatus.Step, now, false
				ps.bossSkip, ps.bossNoKill, ps.gongStep = false, false, 0
				w.logLine(now, "paused: "+w.lastStatus.Note)
				if w.dash != nil {
					w.saveCommanders("pause")
				}
			}
			regroup := strings.Contains(ps.note, "did not hold")
			nearWipe := nearWipeStop(ps.note)
			bossEntry, bossNotFound := w.bossNotFoundAtBossStep(ps.step, ps.note)
			if bossNotFound && !ps.bossSkip {
				// A re-run at the gong that did not bring him back: the frames saw no unit of his alive since it went out, so the
				// object did nothing. The core's gong script is a no-op while the instance counts the encounter IN_PROGRESS or
				// DONE (boss_arlokk.cpp GOHello_go_gong_of_bethekk), and only the boss reaching home resets it, so this instance
				// cannot summon him again: report it and end restartable, for walk-super to re-place in a fresh instance.
				if w.gongRerunBoss == bossEntry && !w.gongRerunAt.IsZero() && !w.bossSeenAliveSince(bossEntry, w.gongRerunAt) {
					line := fmt.Sprintf("boss %d not found: the re-run at step %d did not summon him again (no unit of his alive in the frames since, so the instance still counts him started and refuses the object): this instance cannot bring him back; a fresh instance is needed", bossEntry, w.gongRerunOpen)
					w.note("%s", line)
					w.logLine(now, line)
					return stopf(exitRaidWalkFailed, "%s", line)
				}
				// The run leader paused with "boss <N> not found" on that boss's own step. The frames are the
				// evidence: when they show a boss unit killed, the walk skips the step and the run goes on to its
				// until (walk 24 test2 r2). The evidence is re-read on every tick while the pause lasts (review
				// round 1, item 2: the kill can land after the pause arrived), and the skip is latched only once it
				// went out: a tick without evidence changes nothing (the pause path below still ends the walk), and
				// a failed send is retried once.
				if ok, ev := w.bossKilled(ctx, bossEntry, now); ok {
					ps.bossSkip = true
					w.bossSkipStep(ctx, bossEntry, ev, ps.step, now)
				} else if openStep, ok := raidwalk.SummonOpenStep(w.rows, w.plan, ps.step); ok && ps.gongStep != openStep && w.gongReruns < bossGongReruns {
					// The boss is gone from the instance and the plan says which open row is the object that summons it (walk 35: Arlokk
					// 14515 despawns after a wipe with no spawn row left, and the run pauses "boss 14515 not found" until the gong is
					// repeated by hand). Restart the run at that row instead of ending the walk.
					ps.gongStep = openStep
					w.gongReruns++
					if err := w.rerunOpenStep(ctx, openStep, bossEntry, ps.step, ev, now); err != nil {
						return err
					}
				} else if !ps.bossNoKill {
					ps.bossNoKill = true
					w.note("boss %d not found and no kill in the frames yet (%s): re-reading while the pause lasts", bossEntry, ev)
				}
			}
			switch {
			case regroup && perStep[ps.step] < w.opts.autoResume && autoTotal < 6 && now.Sub(ps.lastTry) >= 15*time.Second:
				if _, err := w.commander(ctx, commanderReq{Op: "run", Verb: "resume", WaitMS: 5000}); err != nil {
					return err
				}
				perStep[ps.step]++
				autoTotal++
				ps.lastTry = now
				w.note("auto-resumed the regroup at step %d (%d/%d)", ps.step, perStep[ps.step], w.opts.autoResume)
			case nearWipe && !ps.skipped && !w.bossEngagedNow() && w.wipeResumes < w.opts.wipeResume && now.Sub(ps.at) >= 15*time.Second:
				w.logLine(now, "near-wipe stop: recovering instead of ending the walk")
				if err := w.recoverNearWipe(ctx, ps.step); err == errWipeResumeSkipped {
					// Unknown boss state: the old ending, not a recovery, and not retried for this pause.
					ps.skipped = true
				} else if terr, ok := err.(*timeoutWalkStop); ok {
					w.note("%s", terr.note)
					return stopf(terr.code, "%s", terr.reason)
				} else if err != errWipeRecovered {
					return err
				} else {
					ps.armed = false
				}
			case now.Sub(ps.at) >= w.opts.pausedWait:
				return stopf(exitRaidWalkFailed, "paused: %s", ps.note)
			}
		} else {
			ps.armed = false
		}
		if end := w.tracker.End(); end.Kind != raidwalk.EndRunning {
			w.endReason = end.Reason()
			w.pullJoinersFlush(time.Time{})
			return nil
		}
		if err := w.env.sleep(ctx, 2*time.Second); err != nil {
			return stopf(exitRaidWalkFailed, "interrupted")
		}
	}
}

// pollOnce reads one events page, one bot_view frame and (on request) the dashboard, feeding the tracker.
func (w *walk) pollOnce(ctx context.Context, dash bool) ([]raidwalk.Incident, error) {
	now := w.env.now()
	// The page's lines get sequence numbers above pollSeq0: handleWipe's self-regroup rule uses the
	// watermark to tell a line of the wipe's own page from an older one (walk 19 r6).
	w.pollSeq0 = w.statusSeq
	evs, err := w.pumpEvents(ctx, 5000)
	if err != nil {
		return nil, err
	}
	for _, e := range evs {
		line, ok := tbm.Line(e)
		if !ok {
			continue
		}
		w.logLine(now, line)
		st, ok := raidwalk.ParseRun(line)
		if !ok || st.Plan != w.plan.Name {
			continue
		}
		if w.rerunStale {
			// The run the re-run stopped emits its own ending after the new run's start went out (walk 37 b: "no run" and
			// "done ... stopped by .bot run stop" at the paused step). That ending is the old run's, not the walk's: drop it
			// until the new run's own running line arrives. Only the stop's own line is dropped (a "stopped before step" or
			// "plan done" line is the new run's, whatever step it names), so a missed clear cannot lose the new run's end.
			switch {
			case !now.Before(w.rerunStaleUntil):
				w.rerunStale = false
			case st.State == tbm.RunRunning:
				w.rerunStale = false
			case (st.State == tbm.RunDone || st.State == tbm.RunNone) && (st.Step == 0 || st.Step == w.rerunStaleStep) &&
				(st.State == tbm.RunNone || strings.Contains(st.Note, ".bot run stop")):
				continue
			}
		}
		w.tracker.Feed(now, st)
		w.observeBossStatus(st, now) // before lastStatus is replaced: the end rule reads the previous status
		// pullStarted compares st with the status before it (the step-decrease latch reset): it runs before
		// lastStatus takes st.
		w.pullStarted(now, st)
		w.lastStatus, w.haveStatus, w.statusSeq = st, true, w.statusSeq+1
		w.recordRunLine(st, now)
		w.noteLabel(st)
		// --gm-instance-data: the first status that names the leg fires
		// here, once per instance, when a walk reaches the leg mid-run. A
		// failing send breaks the walk. The 3 s gaps between the lines run
		// inline in pollOnce and stall its polling for ~6 s once per
		// instance. A status that names the leg while the run is already past
		// the pull gate's first pull is too late to help it: the walk pauses
		// the run first (`.bot run pause`, resumed after the last line),
		// so the next pull waits for the lines. The walk fires at most one
		// spec per poll this way; the next poll fires the next. After a
		// firing the cursor rewinds to the firing line (the way
		// waitTBMMatch rewinds to its matched line) and the loop breaks:
		// the firing's reply read already advanced the cursor past newer
		// lines, but it only logged them — the next poll re-reads the
		// unfired tail in order, so no status after the firing ever
		// processes past an adopted newer one (a rewind to the page start
		// would re-feed the pre-fire statuses instead, regressing
		// lastStatus; the rerunStale filter above re-applies to the
		// re-read lines unchanged).
		fired, err := w.gmInstanceDataSendPending(ctx, st)
		if err != nil {
			return nil, err
		}
		if fired {
			w.eventsAt = e.Seq
			break
		}
		if st.Note != "" {
			w.newNotes = append(w.newNotes, st.Note)
		}
	}
	if n, frame, err := w.view(ctx, w.afterN, 2000); err == nil && frame != nil {
		w.afterN = n
		w.trackFrame(now, frame)
		w.tracker.Frame(now, *frame)
		w.pullFramed(now, frame)
		w.saveFrame(now, frame)
	}
	w.pullJoinersFlush(now)
	if dash && w.dash != nil {
		if w.dashAt.IsZero() || now.Sub(w.dashAt) >= 10*time.Second {
			w.saveCommanders("walk")
			w.dashAt = now
		}
		if w.fightAt.IsZero() || now.Sub(w.fightAt) >= 15*time.Second {
			w.saveFights()
			w.fightAt = now
		}
	}
	return w.tracker.Events(), nil
}

// pumpEvents reads one events page and advances the cursor past it.
func (w *walk) pumpEvents(ctx context.Context, waitMS int) ([]events.Event, error) {
	var page events.Page
	params := map[string]any{"since": w.eventsAt, "kinds": []string{"system", "addon"}, "wait_ms": waitMS}
	timeout := time.Duration(waitMS)*time.Millisecond + 15*time.Second
	if err := w.agent.Call(ctx, "events", params, &page, timeout); err != nil {
		return nil, stopf(exitRaidWalkFailed, "events: %v", err)
	}
	w.eventsAt = page.Next
	return page.Events, nil
}

// pump is pumpEvents as the lines the walk reads.
func (w *walk) pump(ctx context.Context, waitMS int) ([]string, error) {
	evs, err := w.pumpEvents(ctx, waitMS)
	if err != nil {
		return nil, err
	}
	lines := make([]string, 0, len(evs))
	for _, e := range evs {
		if line, ok := tbm.Line(e); ok {
			lines = append(lines, line)
		}
	}
	return lines, nil
}

// waitSystem waits for a plain system line (not a TBM line) that match accepts, logging every line it
// reads. The .bot role reply is such a line; its text is colour-stripped before the match.
func (w *walk) waitSystem(ctx context.Context, timeout time.Duration, match func(string) bool) (string, bool, error) {
	deadline := w.env.now().Add(timeout)
	for {
		if err := ctx.Err(); err != nil {
			return "", false, stopf(exitRaidWalkFailed, "interrupted")
		}
		var page events.Page
		params := map[string]any{"since": w.eventsAt, "kinds": []string{"system", "addon"}, "wait_ms": 1000}
		if err := w.agent.Call(ctx, "events", params, &page, 15*time.Second); err != nil {
			return "", false, stopf(exitRaidWalkFailed, "events: %v", err)
		}
		w.eventsAt = page.Next
		for _, e := range page.Events {
			if line, ok := tbm.Line(e); ok {
				w.logLine(w.env.now(), line)
				continue
			}
			if e.Kind != events.KindSystem || e.Text == "" {
				continue
			}
			text := raidwalk.CleanLine(e.Text)
			w.logLine(w.env.now(), text)
			if match(text) {
				return text, true, nil
			}
		}
		if !w.env.now().Before(deadline) {
			return "", false, nil
		}
		if err := w.env.sleep(ctx, time.Second); err != nil {
			return "", false, stopf(exitRaidWalkFailed, "interrupted")
		}
	}
}

// specWait bounds one `.bot role` reply: the command rebuilds the bot's strategies and saves them before
// it answers, so the answer is the spec applied.
const specWait = 30 * time.Second

// applySpec sets one hire's class spec with `.bot role` and waits for the server's answer: the "role set"
// line is the spec applied, any refusal is a failed walk.
func (w *walk) applySpec(ctx context.Context, name, spec string) error {
	line := fmt.Sprintf(".bot role %s %s", name, spec)
	if err := w.gm(ctx, line); err != nil {
		return err
	}
	okPrefix := fmt.Sprintf("Bot %s role set to %s", name, raidwalk.SpecRole(spec))
	bad := func(text string) bool {
		return strings.HasPrefix(text, fmt.Sprintf("Bot %s does not use that spec", name)) ||
			strings.HasPrefix(text, fmt.Sprintf("Bot %s has no AI yet", name)) ||
			strings.HasPrefix(text, "Usage: .bot role")
	}
	text, ok, err := w.waitSystem(ctx, specWait, func(s string) bool {
		return strings.HasPrefix(s, okPrefix) || bad(s)
	})
	if err != nil {
		return err
	}
	if !ok {
		return stopf(exitRaidWalkFailed, "roster: no answer to %s in %s", line, durWords(specWait))
	}
	if bad(text) {
		return stopf(exitRaidWalkFailed, "roster: %s", text)
	}
	return nil
}

// waitTBM waits for a line with prefix and logs the lines it reads on the way.
func (w *walk) waitTBM(ctx context.Context, prefix string, timeout time.Duration) (string, bool, error) {
	return w.waitTBMMatch(ctx, timeout, func(line string) bool {
		return strings.HasPrefix(line, prefix)
	})
}

func (w *walk) waitTBMMatch(ctx context.Context, timeout time.Duration, match func(string) bool) (string, bool, error) {
	deadline := w.env.now().Add(timeout)
	for {
		if err := ctx.Err(); err != nil {
			return "", false, stopf(exitRaidWalkFailed, "interrupted")
		}
		evs, err := w.pumpEvents(ctx, 1000)
		if err != nil {
			return "", false, err
		}
		for _, e := range evs {
			line, ok := tbm.Line(e)
			if !ok {
				continue
			}
			w.logLine(w.env.now(), line)
			if match(line) || match(raidwalk.CleanLine(line)) {
				// The page's later events are not read: put the cursor back after the matched line, so the walk's next poll
				// reads them. A run's done line right after its start line would otherwise be lost (a re-run then never sees
				// its new run end; review of walk 37 b).
				w.eventsAt = e.Seq
				return line, true, nil
			}
		}
		if !w.env.now().Before(deadline) {
			return "", false, nil
		}
		if err := w.env.sleep(ctx, time.Second); err != nil {
			return "", false, stopf(exitRaidWalkFailed, "interrupted")
		}
	}
}

// stopRun sends `.bot run stop` once, with its own context so an interrupt still stops the run.
func (w *walk) stopRun(ctx context.Context, line string) error {
	if !w.runStarted || w.runStopped || w.agent == nil {
		return nil
	}
	if line == "" {
		line = ".bot run stop"
	}
	callCtx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	if err := w.gm(callCtx, line); err != nil {
		w.note("run stop: %v", err)
		return nil
	}
	w.runStopped = true
	return nil
}

// waitMap waits until the agent reports mapID. The deadline is a recovery entry deadline
// (zero: no recovery bounds this wait): every loop turn and sleep checks it first, and
// past it the wait fails with the timed-out recovery the caller turns into the
// original incident's ending.
func (w *walk) waitMap(ctx context.Context, mapID uint32, timeout time.Duration, deadline time.Time) error {
	return w.waitStatus(ctx, timeout, deadline, fmt.Sprintf("the commander is not on map %d", mapID),
		func(st session.StatusInfo) bool { return st.Map == mapID })
}

// waitStatus polls the agent's status until ok accepts it, up to timeout. The deadline is a recovery entry
// deadline (zero: no recovery bounds this wait): every loop turn and sleep checks it first, and past it the wait
// fails with the timed-out recovery the caller turns into the original incident's ending.
func (w *walk) waitStatus(ctx context.Context, timeout time.Duration, deadline time.Time, what string, ok func(session.StatusInfo) bool) error {
	statusDue := w.env.now().Add(timeout)
	for {
		if err := ctx.Err(); err != nil {
			return stopf(exitRaidWalkFailed, "interrupted")
		}
		if timeout := w.recoveryExpired(deadline); timeout != nil {
			return timeout
		}
		st, err := w.status(ctx)
		if err == nil && ok(st) {
			return nil
		}
		if !w.env.now().Before(statusDue) {
			return stopf(exitRaidWalkFailed, "%s after %s", what, durWords(timeout))
		}
		if err := w.recoverySleep(ctx, time.Second, deadline); err != nil {
			return err
		}
	}
}

// waitRevivePoint waits until the commander has LANDED at p on the plan's map: the server has settled its position
// (reviveLanded). The map id alone is not enough: a `.go xyz` answers at once, but the server lands the character a
// moment later, and a summon that leaves before the landing drops the whole party at the OLD spot (walk 22 test2
// s4, Q3: the lead landed 560 yd back at the ZG entrance and its Voidwalker followed). The session also reports the
// `.go` destination itself at once, flagged ZUnknown until the server sends a position, so a position match alone
// is no landing either (Codex HIGH, F4 round 2). fromMap is the commander's map before the `.go` (see reviveLanded);
// beforeSeq is the session's acked-teleport count read just before the `.go` (haveSeq false when the read failed).
func (w *walk) waitRevivePoint(ctx context.Context, p dungeon.Point, fromMap uint32, beforeSeq uint64, haveSeq bool, deadline time.Time) error {
	return w.waitStatus(ctx, reviveTeleportWait, deadline,
		fmt.Sprintf("the commander is not within %d yd of the revive point on map %d", reviveLandingYd, w.plan.Map),
		func(st session.StatusInfo) bool { return reviveLanded(p, w.plan.Map, fromMap, st, beforeSeq, haveSeq) })
}

// reviveSeq reads the session's acked-teleport count just before a revive `.go xyz`, the landing signal that works
// over a navmesh hole and for every far go (reviveLanded's seq term). One retry, 1 s later, within the recovery
// deadline; have is false when no read answered (a near go then falls back to the settled z, as before; a far one
// does not land).
func (w *walk) reviveSeq(ctx context.Context, deadline time.Time) (seq uint64, have bool) {
	if st, err := w.status(ctx); err == nil {
		return st.TeleportSeq, true
	}
	if err := w.recoverySleep(ctx, time.Second, deadline); err != nil {
		return 0, false
	}
	if st, err := w.status(ctx); err == nil {
		return st.TeleportSeq, true
	}
	return 0, false
}

// commanderMap reads the commander's map before a revive `.go xyz`: what tells a far teleport from a near one. A failed
// read is retried (commanderMapTries in all, 1 s apart, within the recovery deadline), never guessed: taking the plan's
// map for it picks the near rule, and a far go then never lands (review round 2, Codex MEDIUM). ok is false when no
// read answered.
func (w *walk) commanderMap(ctx context.Context, deadline time.Time) (m uint32, ok bool, err error) {
	for try := 0; try < commanderMapTries; try++ {
		if try > 0 {
			if err := w.recoverySleep(ctx, time.Second, deadline); err != nil {
				return 0, false, err
			}
		}
		if st, serr := w.status(ctx); serr == nil {
			return st.Map, true, nil
		}
	}
	return 0, false, nil
}

const commanderMapTries = 3

// farFromMap stands for "the commander was not on the plan's map" when its map could not be read: a self-regroup's
// commander starts at the portal (map 0 in every walk), so the far rule applies.
const farFromMap = math.MaxUint32

// reviveFrom is the fromMap a revive go's landing rule uses: the plan's map after a verified appear (its fresh landing
// was read on it), else commanderMap. With no readable status a normal revive stops (exit 19) and a self-regroup goes on
// under the far rule, noted.
func (w *walk) reviveFrom(ctx context.Context, joined, selfRegroup bool, deadline time.Time) (uint32, error) {
	if joined {
		return w.plan.Map, nil
	}
	m, ok, err := w.commanderMap(ctx, deadline)
	if err != nil {
		return 0, err
	}
	if ok {
		return m, nil
	}
	if !selfRegroup {
		return 0, stopf(exitRaidWalkIncident, "not revived: commander status unreadable")
	}
	w.note("revive: commander status unreadable; self-regroup: the far landing rule (the commander starts at the portal)")
	return farFromMap, nil
}

// hiresOffMap counts the hires the frames last saw on another map than the plan's (released to the graveyard).
func (w *walk) hiresOffMap() int {
	n := 0
	for _, name := range w.hireList() {
		if b, ok := w.botAt[name]; ok && b.mapID != w.plan.Map {
			n++
		}
	}
	return n
}

// hireUpOnMap says a hire was last seen alive on the plan's map.
func (w *walk) hireUpOnMap() bool {
	for _, name := range w.hireList() {
		if b, ok := w.botAt[name]; ok && b.mapID == w.plan.Map && !b.dead {
			return true
		}
	}
	return false
}

// appearHire is the hire the revive appears at before its `.go xyz` to p: among the hires the frames last saw on the
// plan's map, the living one nearest p (x/y), else the nearest dead one; "" when none is known there. Ties go to roster
// order.
func (w *walk) appearHire(p dungeon.Point) string {
	best, bestDead, bestD := "", true, math.Inf(1)
	for _, name := range w.hireList() {
		b, ok := w.botAt[name]
		if !ok || b.mapID != w.plan.Map {
			continue
		}
		d := math.Hypot(float64(b.x-p.X), float64(b.y-p.Y))
		if best == "" || (bestDead && !b.dead) || (bestDead == b.dead && d < bestD) {
			best, bestDead, bestD = name, b.dead, d
		}
	}
	return best
}

// joinHires puts the commander into the hires' instance copy before a revive `.go xyz` to p, and says why not when it
// cannot. A `.go xyz` from the portal's map opens a NEW copy (walk 23 mangosd: hires in inst 101, commander in 104;
// test2 106 vs 107), where the quiet check sees an empty copy. (A `.summon` from there would not drag the raid away:
// the core moves the summoned hire within its own copy, Player.cpp:2613.) `.appear <hire>` binds a GM to the target's
// copy (core Commands.cpp HandleGonameCommand: UnbindInstance, then BindToInstance(the target's
// DungeonPersistentState)), so the `.go xyz` after it is a near go, with the settled-z landing rule.
//
// The appear counts only on a FRESH landing (appearLanded): the commander may already stand on the plan's map in a copy
// of its own, so the map id alone proves nothing. reviveHires sends `.gm visible off` once before it.
func (w *walk) joinHires(ctx context.Context, p dungeon.Point, deadline time.Time) (joined bool, why string, err error) {
	name := w.appearHire(p)
	if name == "" {
		return false, fmt.Sprintf("no hire seen on map %d", w.plan.Map), nil
	}
	return w.appearAtHire(ctx, name, deadline)
}

// appearAtHire is joinHires' appear half for a named hire: `.appear <name>` and the wait for a fresh landing.
func (w *walk) appearAtHire(ctx context.Context, name string, deadline time.Time) (joined bool, why string, err error) {
	hire := w.botAt[name]
	before, serr := w.status(ctx)
	if serr != nil { // one retry (review round 2, L1): the freshness check needs the read
		if err := w.recoverySleep(ctx, time.Second, deadline); err != nil {
			return false, "", err
		}
		if before, serr = w.status(ctx); serr != nil {
			return false, "commander status unreadable before the appear", nil
		}
	}
	beforeSeq := before.TeleportSeq
	if err := w.gm(ctx, ".appear "+name); err != nil {
		if ctx.Err() != nil {
			return false, "", stopf(exitRaidWalkFailed, "interrupted")
		}
		return false, fmt.Sprintf(".appear %s failed: %v", name, err), nil
	}
	werr := w.waitStatus(ctx, appearWait, deadline, "the appear did not land",
		func(st session.StatusInfo) bool { return appearLanded(st, w.plan.Map, hire, before, beforeSeq) })
	if werr == nil {
		return true, "", nil
	}
	if timeout, ok := werr.(*recoveryTimeout); ok {
		return false, "", timeout
	}
	if ctx.Err() != nil {
		return false, "", stopf(exitRaidWalkFailed, "interrupted")
	}
	return false, fmt.Sprintf(".appear %s did not land within %d yd of it in %s", name, appearLandingYd, durWords(appearWait)), nil
}

// appearLanded says st is a fresh landing of an `.appear` at hire: on mapID, x/y within appearLandingYd of where the
// frames last saw the hire, and a fresh teleport since the read before the appear (an ack the session counted in
// TeleportSeq, or a moved position/map). The seq term covers an appear that lands the commander where it already
// stands: the destination is right and the position does not change, but the server teleport has landed (walk 24 C2).
func appearLanded(st session.StatusInfo, mapID uint32, hire botPos, before session.StatusInfo, beforeSeq uint64) bool {
	if st.Map != mapID || math.Hypot(float64(st.X-hire.x), float64(st.Y-hire.y)) > appearLandingYd {
		return false
	}
	if st.TeleportSeq > beforeSeq {
		return true
	}
	return st.Map != before.Map || st.X != before.X || st.Y != before.Y || st.Z != before.Z
}

// appearWait bounds the wait for a revive `.appear` to land; appearLandingYd is how close (x/y) to the hire's last
// frame position it must land (the hire may have moved a little since).
const (
	appearWait      = 10 * time.Second
	appearLandingYd = 15
)

// reviveLanded says the status is a SETTLED landing at p on mapID: the character is not falling, x/y are within
// reviveLandingYd of p and z within reviveLandingZYd (a landing may drop to the floor, so z gets more room than x/y).
// The session publishes a teleport's landing in one update that bumps TeleportSeq after the client's ack has gone
// out, so the seq past beforeSeq is the landing signal in both cases. After a NEAR teleport (fromMap == mapID) the
// other term is a position the server sent: not ZUnknown, the session's guess until the floor drop or a server
// position settles it; the seq alone lands over a navmesh hole that leaves the z guess unsettled for good (walk 24
// test2 r3). After a FAR teleport (fromMap != mapID) ZUnknown stays set for good, as the session lands it through
// SMSG_NEW_WORLD with no floor drop and the core never sends the player its own position (walk 23: 6 runs exit 19,
// the commander waited on map 0 at the portal); the map change alone is not the landing, only the worldport ack the
// session counts (Codex HIGH). An unread beforeSeq (haveSeq false) lands nothing: the wait times out rather than
// summon blind. Falling still blocks: a dropToFloor fall must finish first.
func reviveLanded(p dungeon.Point, mapID, fromMap uint32, st session.StatusInfo, beforeSeq uint64, haveSeq bool) bool {
	if st.Map != mapID || st.Falling {
		return false
	}
	seq := haveSeq && st.TeleportSeq > beforeSeq
	if fromMap != mapID {
		if !seq {
			return false
		}
	} else if st.ZUnknown && !seq {
		return false
	}
	dx, dy := float64(p.X-st.X), float64(p.Y-st.Y)
	return math.Hypot(dx, dy) <= reviveLandingYd && math.Abs(float64(p.Z-st.Z)) <= reviveLandingZYd
}

// bossRowEngaged says a plan-boss row is in a fight: alive and either holding a target or hurt.
// A row at full hp with no target is a boss (or a `first` add standing as trash) merely in view;
// a 0 % row is a corpse. Neither latches (walk 17 s4: a Zulian Tiger 11361 at 100 %, no target,
// 25 s before a trash wipe blocked the recovery).
func bossRowEngaged(hppct int, target uint64) bool {
	return hppct > 0 && (target != 0 || hppct < 100)
}

// trackFrame keeps the per-bot and per-hostile state the enter, reset and wipe rules read.
func (w *walk) trackFrame(now time.Time, f *tbm.ViewFrame) {
	for _, p := range f.Party {
		if p.GUID != 0 {
			w.partyG[uint64(p.GUID)] = true
		}
		if p.Name == "" {
			continue
		}
		w.botAt[p.Name] = botPos{guid: p.GUID, x: p.X, y: p.Y, z: p.Z, inCombat: p.InCombat(), mapID: p.Map, dead: p.Dead || p.State == "d", at: now}
	}
	for _, h := range f.Hostiles {
		if h.GUID != 0 {
			// first keeps the position the frames first saw the unit at: a later one never changes the patrol binding (round 4).
			first := dungeon.Point{X: h.X, Y: h.Y, Z: h.Z}
			if prev, ok := w.hostiles[h.GUID.String()]; ok {
				first = prev.first
			}
			w.hostiles[h.GUID.String()] = hostileState{entry: h.Entry, x: h.X, y: h.Y, z: h.Z, first: first, kind: h.Kind, reaction: h.Reaction, flags: h.Flags,
				hppct: h.HPPct, target: uint64(h.Target), at: now}
		}
		if w.bossRule.Counts(h.Entry, h.X, h.Y) {
			w.bossSeenAt = now
			if bossRowEngaged(h.HPPct, uint64(h.Target)) {
				w.bossLatchAt, w.bossLatchHit = now, h.Entry
			}
			w.bossRows[h.Entry] = hostileState{entry: h.Entry, x: h.X, y: h.Y, z: h.Z, kind: h.Kind, reaction: h.Reaction, flags: h.Flags,
				hppct: h.HPPct, target: uint64(h.Target), at: now}
			if h.Target != 0 {
				w.bossTargetAt = now
			}
		}
	}
	// The guid rule (spec 6, D5 b, #1453): the frames are keyed by guid, and a script that changes a creature's
	// entry (core UpdateEntry) keeps that guid. So a row whose guid the frames showed with a plan boss's OWN entry
	// is the boss whatever entry it now reads: bossGUIDs remembers the guid, bossForms the entries it was seen with,
	// one per change (the summary's phase is 1 + the changes).
	for _, h := range f.Hostiles {
		if h.GUID == 0 {
			continue
		}
		g := h.GUID.String()
		if w.bossRule.Owns(h.Entry) {
			if w.bossGUIDs == nil {
				w.bossGUIDs = map[string]uint32{}
			}
			w.bossGUIDs[g] = h.Entry
		}
		if _, boss := w.bossGUIDs[g]; !boss {
			continue
		}
		if l := w.bossForms[g]; len(l) == 0 || l[len(l)-1] != h.Entry {
			if w.bossForms == nil {
				w.bossForms = map[string][]uint32{}
			}
			w.bossForms[g] = append(l, h.Entry)
		}
	}
	// An entry the frame shows more than one LIVE unit of at once exists several at a time: it is an add, not a
	// boss reborn, and its corpse proves nothing (bossUnits). Thekal's two Zulian Guardians 15068 are summoned
	// together (boss_thekal.cpp:41/:413) while Thekal reborn 14509 stands alone.
	for i := range f.Hostiles {
		if f.Hostiles[i].HPPct <= 0 || w.bossAdds[f.Hostiles[i].Entry] {
			continue
		}
		// A boss unit is NEVER an add: the row of a guid the frames showed as the boss's own entry must not mark
		// its entry an add, so a boss that changes entry into a pair's entry is still the boss (bossKilled).
		if f.Hostiles[i].GUID != 0 && w.bossGUIDs[f.Hostiles[i].GUID.String()] != 0 {
			continue
		}
		for j := range f.Hostiles {
			if j != i && f.Hostiles[j].Entry == f.Hostiles[i].Entry && f.Hostiles[j].HPPct > 0 {
				if w.bossAdds == nil {
					w.bossAdds = map[uint32]bool{}
				}
				w.bossAdds[f.Hostiles[i].Entry] = true
				break
			}
		}
	}
	// The attempt start (after bossAdds, so an add several at a time is already out): the first time the frames
	// showed a boss unit alive and engaged (bossAttemptEngaged), judged like the tracker's one-shot latch
	// (checkBoss). bossKilled reads it to keep a 0 % row an earlier attempt left behind out of the kill evidence
	// (review round 3), so it is recorded per unit entry and only the first sighting counts. A dead row never
	// starts one: the first sighting of a corpse that was already there would otherwise time-stamp the very row
	// it then accepts (revive r7).
	for _, h := range f.Hostiles {
		if h.GUID == 0 || !w.bossUnit(h.Entry) || !bossAttemptEngaged(h.HPPct, uint64(h.Target), w.partyG) {
			continue
		}
		if w.bossAttemptAt[h.Entry].IsZero() {
			w.bossAttemptAt[h.Entry] = now
			w.resetBossAliveSeen(h.Entry)
		}
	}
	// The GUIDs the current attempt showed alive (revive r6): every live boss-unit row by its frame key,
	// latest wins, and a new attempt starts the map over (resetBossAliveSeen: an earlier attempt's sighting
	// vouches for nothing). A live sighting after the toolkit's first corpse report voids that wait (a fake
	// death raised the boss again), and a corpse the frames never showed alive is a stale or foreign one.
	// A 0 % frame row is no sighting, so it neither starts the link nor voids the wait.
	for _, h := range f.Hostiles {
		if h.GUID == 0 || h.HPPct <= 0 || !w.bossUnit(h.Entry) {
			continue
		}
		if w.bossAliveSeen == nil {
			w.bossAliveSeen = map[string]time.Time{}
		}
		w.bossAliveSeen[h.GUID.String()] = now
		if w.bossCorpseSeen != nil {
			delete(w.bossCorpseSeen, h.GUID.String())
		}
	}
}

// bossAttemptEngaged is the attempt-start judgement: a plan-boss row starts the attempt when it is alive
// (above 0 %) and hurt (below 100 %) or holding a party target. A 0 % row never starts one, not even with a
// party target: the frames' own 0 % sighting of a fresh corpse is the kill bossKilled reads, not a fight (fake
// deaths), and the wipe-resume kill (wipeBossFrame) needs the same rule — a dead row never starts an attempt.
func bossAttemptEngaged(hppct int, target uint64, partyG map[uint64]bool) bool {
	return hppct > 0 && (hppct < 100 || (target != 0 && partyG[target]))
}

// resetBossAliveSeen ends the previous attempt's live links for entry's boss units: a new attempt starts
// with no guid seen alive yet (the same frame's live rows refill the map after this), so a sighting from
// an earlier attempt can never vouch for this attempt's corpse (bossKilled, bossCorpse). The toolkit's
// first corpse sighting rests on that link, so it goes with it.
func (w *walk) resetBossAliveSeen(entry uint32) {
	units := w.bossUnits(entry)
	for key, h := range w.hostiles {
		if !inEntries(units, h.entry) {
			continue
		}
		delete(w.bossAliveSeen, key)
		delete(w.bossCorpseSeen, key)
	}
}

// pullStarted arms the pull-joiners window when the pull order's own
// "pulling <entry> <guid>" status line goes by: 10 s in which frames count
// the engaged hostiles that are not the pulled pack. The "waiting for the
// tank to stand still before pulling" line still waits and opens no window:
// PullingTarget only reads the actual pull line. The pack is the pulled guid
// plus the plan pack's other members: every guid whose frame position stands
// within the row's radius of the pack centroid (w.pullRow), the row found
// from the run's step or, on a move step, from the pulled unit's entry and
// position. bossPullStarted arms the same window for a boss, which has no
// pulling line. w.lastStatus must still be the status BEFORE st (the
// step-decrease check reads it): call this before storing st.
func (w *walk) pullStarted(now time.Time, st tbm.RunStatus) {
	if w.haveStatus && st.State == tbm.RunRunning && st.Step < w.lastStatus.Step {
		// The walk resumed at an earlier step: the boss latch belongs to the
		// abandoned walk on, so a later boss fight arms a fresh window.
		w.bossPullArmed = nil
	}
	if st.State != tbm.RunRunning {
		// A paused run pulls nothing: forget the boss step's latch so the
		// resumed attempt's fight start arms a fresh window.
		if st.Kind == planexport.KindBoss {
			delete(w.bossPullArmedOf(), st.Step)
		}
		return
	}
	if entry, guid, ok := raidwalk.PullingTarget(raidwalk.CleanLine(st.Note)); ok {
		w.pullPack = map[packet.GUID]bool{guid: true}
		w.pullSeed = guid
		w.pullEntry = entry
		w.pullRow = packRowOf(w.rows, st.Step)
		if w.pulledSteps == nil {
			w.pulledSteps = map[int]bool{}
		}
		w.pulledSteps[st.Step] = true
		w.pullPackSeeded = false
		w.pullSeedTries = 0
		w.pullNoRow = false
		w.pullBossAdds = w.bossAddsOf(st.Step)
		w.pullJoinsAt, w.pullJoinsStep = now, st.Step
		w.pullJoinsSeen = map[packet.GUID]bool{}
		w.pullAddsSeen = map[packet.GUID]bool{}
		if st.Kind == planexport.KindBoss {
			// The boss step's pull line (a stand pull of one of its adds) opened
			// the window: the fight-start line below must not open a second one.
			w.bossPullArmedOf()[st.Step] = true
		}
	}
	w.bossPullStarted(now, st)
}

// bossAddsOf is the boss-encounter add set of the leg the step's boss row
// belongs to: the row's own entries and the plan's hold/add entries for that
// leg (its hold entries and its first adds). Summoned and arriving adds of
// those entries join the fight as it runs: they are no other pack joining
// the pull, so the boss window keeps them out of the joiner count and marks
// them "boss add" in the record instead. With no plan or no boss row for the
// step (a trash pull): nothing is a boss add.
func (w *walk) bossAddsOf(step int) map[uint32]bool {
	if w.plan == nil || step < 1 || step > len(w.rows.Steps) {
		return nil
	}
	s := w.rows.Steps[step-1]
	if s.Kind != planexport.KindBoss {
		return nil
	}
	out := map[uint32]bool{}
	for _, e := range s.Entries {
		out[e] = true
	}
	for i := range w.plan.Legs {
		l := &w.plan.Legs[i]
		if l.Boss == nil || l.Boss.Entry != s.Entry {
			continue
		}
		if l.Boss.Hold != nil {
			for _, e := range l.Boss.Hold.Entries {
				out[e] = true
			}
		}
		for _, e := range l.Boss.First {
			out[e] = true
		}
		break
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

// bossPullStarted arms the joiner window of a boss pull: a boss has no
// "pulling <entry> <guid>" line (the run leader walks to the firing stand and
// the tank engages on arrival), so the first fighting line of its boss step is
// its pull order. One window per boss step: the later fighting lines of the
// same fight are not further pulls.
func (w *walk) bossPullStarted(now time.Time, st tbm.RunStatus) {
	if st.Kind != planexport.KindBoss || !strings.HasPrefix(strings.ToLower(st.Note), "fighting") {
		return
	}
	if w.bossPullArmedOf()[st.Step] {
		return
	}
	row := packRowOf(w.rows, st.Step)
	if !row.HasRow {
		return
	}
	if !w.pullJoinsAt.IsZero() && w.pullJoinsStep == st.Step {
		// A stand pull of one of the boss's adds already counts this fight.
		w.bossPullArmedOf()[st.Step] = true
		return
	}
	// Another pull's window is still open (under 10 s old): write its record now,
	// as its window end does, so the boss window can open.
	if !w.pullJoinsAt.IsZero() {
		w.pullJoinersFlush(time.Time{})
	}
	w.bossPullArmedOf()[st.Step] = true
	w.pullPack = map[packet.GUID]bool{}
	w.pullSeed = 0 // the row owns the boss and its adds by position
	w.pullEntry = 0
	w.pullRow = row
	w.pullPackSeeded = false
	w.pullSeedTries = 0
	w.pullNoRow = false
	w.pullBossAdds = w.bossAddsOf(st.Step)
	w.pullJoinsAt, w.pullJoinsStep = now, st.Step
	w.pullJoinsSeen = map[packet.GUID]bool{}
	w.pullAddsSeen = map[packet.GUID]bool{}
}

func (w *walk) bossPullArmedOf() map[int]bool {
	if w.bossPullArmed == nil {
		w.bossPullArmed = map[int]bool{}
	}
	return w.bossPullArmed
}

// packRowOf is the plan pack of the pull step: the centroid and radius of its
// pull/boss row, for ownership of the pack's other members.
func packRowOf(rows planexport.Rows, step int) raidwalk.PackRow {
	if step < 1 || step > len(rows.Steps) {
		return raidwalk.PackRow{}
	}
	s := rows.Steps[step-1]
	if s.Kind != planexport.KindPull && s.Kind != planexport.KindBoss {
		return raidwalk.PackRow{}
	}
	return raidwalk.PackRow{HasRow: true, CX: s.X, CY: s.Y, Radius: raidwalk.RowRadius(s.Kind, s.Radius)}
}

// packRowFor finds the plan row that owns a pulled unit when the run's current
// step is not a pull/boss row: a stand pull is ordered while the run still
// stands on its approach move row (walk 27: c-r3 step 262 pulled 11360, whose
// pull row is 265), and the module also pulls a mob found on the way. The row
// is the nearest pull/boss row whose entries hold the pulled entry within the
// row radius + 15 yd (3D) of the seed; the pulled unit's position breaks a tie
// between the packs of one entry (a leg repeats an entry in several pull
// groups). No row in range: no row, and the pack is the seed's aggro-together
// mates seen in the first frame within 15 yd (the record says "no row").
func packRowFor(rows planexport.Rows, entry uint32, seed [3]float32) raidwalk.PackRow {
	if entry == 0 {
		return raidwalk.PackRow{}
	}
	best, bestD := -1, float64(0)
	for i, s := range rows.Steps {
		if s.Kind != planexport.KindPull && s.Kind != planexport.KindBoss {
			continue
		}
		if !rowHasEntry(s, entry) {
			continue
		}
		dx, dy, dz := float64(s.X)-float64(seed[0]), float64(s.Y)-float64(seed[1]), float64(s.Z)-float64(seed[2])
		d := math.Sqrt(dx*dx + dy*dy + dz*dz)
		if d > float64(raidwalk.RowRadius(s.Kind, s.Radius)+raidwalk.PackRowSlackYd) {
			continue
		}
		if best < 0 || d < bestD {
			best, bestD = i, d
		}
	}
	if best < 0 {
		return raidwalk.PackRow{}
	}
	s := rows.Steps[best]
	return raidwalk.PackRow{HasRow: true, CX: s.X, CY: s.Y, Radius: raidwalk.RowRadius(s.Kind, s.Radius)}
}

// rowHasEntry says a pull/boss row owns entry: the boss row's own entry, or
// one of the row's listed pack entries.
func rowHasEntry(s planexport.StepRow, entry uint32) bool {
	if s.Entry == entry {
		return true
	}
	for _, e := range s.Entries {
		if e == entry {
			return true
		}
	}
	return false
}

func (w *walk) packPositions() map[packet.GUID][3]float32 {
	out := make(map[packet.GUID][3]float32, len(w.hostiles))
	for gs, h := range w.hostiles {
		g, err := packet.ParseGUID(gs)
		if err != nil || g == 0 {
			continue
		}
		out[g] = [3]float32{h.x, h.y, h.z}
	}
	return out
}

// pullFramed counts one frame toward the armed pull: every engaged hostile
// outside the pulled pack's guids joins the seen set, except units of the
// boss window's boss-add entries (they join the fight as it runs, and land in
// the record's "boss add" set instead). 10 s after the pull the count is
// final: the record is written keyed by the pull step and the window closes.
// A newer pull re-arms first (pullStarted runs on the status lines of the
// same poll before the frame).
func (w *walk) pullFramed(now time.Time, f *tbm.ViewFrame) {
	if w.pullJoinsAt.IsZero() {
		return
	}
	if now.Sub(w.pullJoinsAt) >= raidwalk.JoinWindow {
		w.pullJoinersFlush(now)
		return
	}
	if w.pullJoinsSeen == nil {
		w.pullJoinsSeen = map[packet.GUID]bool{}
	}
	if w.pullAddsSeen == nil {
		w.pullAddsSeen = map[packet.GUID]bool{}
	}
	// The first frame of the window sees the pack where it was pulled from
	// (trackFrame already recorded this frame's positions): its other members
	// are the guids within the pull row's radius, whatever their entry. A stand
	// pull ordered on a move step has no row from the step: find the pulled
	// unit's own plan row here, where its position is known. A seed missing
	// from the first frame retries on the next 3 frames before giving up (a
	// frame may arrive before the pulled unit's first position row does).
	if !w.pullPackSeeded {
		pos := w.packPositions()
		if !w.pullRow.HasRow {
			if seedPos, ok := pos[w.pullSeed]; ok {
				w.pullRow = packRowFor(w.rows, w.pullEntry, seedPos)
				w.pullPackSeeded = true
				if !w.pullRow.HasRow {
					w.pullNoRow = true
				}
			} else if w.pullSeedTries >= 3 {
				w.pullPackSeeded = true
				w.pullNoRow = true
			} else {
				w.pullSeedTries++
			}
		} else {
			w.pullPackSeeded = true
		}
		if w.pullPackSeeded {
			if w.pullNoRow {
				for _, g := range raidwalk.SeedMates(w.pullSeed, pos) {
					w.pullPack[g] = true
				}
			} else {
				for _, g := range raidwalk.PullPackMembers(w.pullSeed, w.pullRow, pos) {
					w.pullPack[g] = true
				}
			}
		}
	}
	for _, h := range f.EngagedHostiles() {
		if h.GUID == 0 || w.pullPack[h.GUID] {
			continue
		}
		if w.pullBossAdds[h.Entry] {
			w.pullAddsSeen[h.GUID] = true
			continue
		}
		w.pullJoinsSeen[h.GUID] = true
	}
}

// pullJoinersFlush closes the pull window once 10 s passed (or the watch ends:
// force at the run's end): the joiners record is written keyed by the pull
// step, where the report reads it.
func (w *walk) pullJoinersFlush(now time.Time) {
	if w.pullJoinsAt.IsZero() {
		return
	}
	if !now.IsZero() && now.Sub(w.pullJoinsAt) < raidwalk.JoinWindow {
		return
	}
	// pullFramed never adds a pack guid to the seen set: what it holds is the joiners.
	guids := make([]packet.GUID, 0, len(w.pullJoinsSeen))
	for g := range w.pullJoinsSeen {
		guids = append(guids, g)
	}
	rec := raidwalk.SortJoinerGUIDs(guids)
	adds := make([]packet.GUID, 0, len(w.pullAddsSeen))
	for g := range w.pullAddsSeen {
		adds = append(adds, g)
	}
	adds = raidwalk.SortJoinerGUIDs(adds)
	w.pullJoins = append(w.pullJoins, raidwalk.PullJoiners{Step: w.pullJoinsStep, At: w.pullJoinsAt, Count: len(rec), GUIDs: rec, BossAdds: adds, NoRow: w.pullNoRow})
	if note := raidwalk.JoinersNote(rec); note != "" {
		w.note("step %d: %s", w.pullJoinsStep, note)
		w.logLine(w.env.now(), raidwalk.PullJoinersLine(w.pullJoinsStep, rec))
	}
	if len(adds) > 0 {
		w.note("step %d: boss add %d (%s)", w.pullJoinsStep, len(adds), joinerGUIDs(adds))
		w.logLine(w.env.now(), fmt.Sprintf("step %d: boss add %d (%s)", w.pullJoinsStep, len(adds), joinerGUIDs(adds)))
	}
	if w.pullNoRow {
		w.note("step %d: no row (mates within 15 yd owned)", w.pullJoinsStep)
	}
	w.pullJoinsAt = time.Time{}
	w.pullSeed = 0
	w.pullEntry = 0
	w.pullRow = raidwalk.PackRow{}
	w.pullPackSeeded = false
	w.pullSeedTries = 0
	w.pullNoRow = false
	w.pullBossAdds = nil
	w.pullPack = nil
	w.pullJoinsSeen = nil
	w.pullAddsSeen = nil
}

// joinerGUIDs formats guids for the "boss add" note the way JoinersNote does its own.
func joinerGUIDs(guids []packet.GUID) string {
	var b strings.Builder
	for i, g := range guids {
		if i > 0 {
			b.WriteByte(' ')
		}
		b.WriteString(g.String())
	}
	return b.String()
}

// savedFrame is one frames.jsonl line (compact: positions, hp, power, dead, hostiles).
type savedFrame struct {
	T        int64          `json:"t"`
	Seq      uint64         `json:"seq"`
	Tier     int            `json:"tier"` // the walk's hire gear tier (amendment G1: every dump's meta)
	Party    []savedBot     `json:"party"`
	Hostiles []savedHostile `json:"hostiles"`
}

type savedBot struct {
	GUID      string  `json:"guid"`
	Name      string  `json:"name"`
	X         float32 `json:"x"`
	Y         float32 `json:"y"`
	Z         float32 `json:"z"`
	Map       uint32  `json:"map"`
	HP        uint32  `json:"hp"`
	MaxHP     uint32  `json:"max_hp"`
	Power     uint32  `json:"power"`
	MaxPower  uint32  `json:"max_power"`
	PowerType *uint8  `json:"power_type,omitempty"` // 0 mana, 1 rage, 2 focus, 3 energy; written even at 0 (mana); absent when the bot has no power pool (max_power 0)
	Dead      bool    `json:"dead"`
	State     string  `json:"state"`
	Target    string  `json:"target,omitempty"` // the bot's target, else its victim (absent when the module sent none)
	Spell     uint32  `json:"spell,omitempty"`  // the spell it casts or channels
	Action    string  `json:"action,omitempty"` // the last action its AI ran
	Result    string  `json:"result,omitempty"` // the verdict on that action (O ok, F failed, I impossible, U useless, P queued, V dropped)
}

type savedHostile struct {
	GUID   string  `json:"guid"`
	Entry  uint32  `json:"entry"`
	X      float32 `json:"x"`
	Y      float32 `json:"y"`
	Z      float32 `json:"z"`
	HPPct  int     `json:"hp_pct"`
	Target string  `json:"target,omitempty"`
	Flags  string  `json:"flags"`
}

const (
	// frameEvery is the frame cadence the walk keeps out of combat.
	frameEvery = 10 * time.Second
	// frameEveryCombat is the frame cadence while any party member is in combat: the Servants of one pile died
	// 11 s apart, so the old 10 s frames could hold nothing but their corpses (F3, walk 22 Q1).
	frameEveryCombat = 2 * time.Second
)

// saveFrame writes one frame per frameEvery, and one per frameEveryCombat while a party member is in combat (a
// bot's combat or reaction state, or a hostile engaged with the party). Each bot carries the target, spell, action
// and result of its P record (internal/tbm/view.go), so the frames show which bot hit which Servant with which
// spell — the evidence the hold needs, at no module cost.
func (w *walk) saveFrame(now time.Time, f *tbm.ViewFrame) {
	if w.framesF == nil {
		return
	}
	every := frameEvery
	if f.PartyInCombat() {
		every = frameEveryCombat
	}
	if !w.lastFrameSave.IsZero() && now.Sub(w.lastFrameSave) < every {
		return
	}
	w.lastFrameSave = now
	rec := savedFrame{T: now.UnixMilli(), Seq: f.Seq, Tier: w.roster.Tier}
	for _, p := range f.Party {
		bot := savedBot{GUID: p.GUID.String(), Name: p.Name, X: p.X, Y: p.Y, Z: p.Z, Map: p.Map,
			HP: p.HP, MaxHP: p.MaxHP, Power: p.Power, MaxPower: p.MaxPower,
			Dead: p.Dead || p.State == "d", State: p.State,
			Spell: p.Spell, Action: p.Action, Result: p.Result}
		if p.MaxPower > 0 { // no power pool, no power_type: an absent key is not mana (0)
			pt := p.PowerType
			bot.PowerType = &pt
		}
		if p.Target != 0 {
			bot.Target = p.Target.String()
		}
		rec.Party = append(rec.Party, bot)
	}
	for _, h := range f.Hostiles {
		rec.Hostiles = append(rec.Hostiles, savedHostile{GUID: h.GUID.String(), Entry: h.Entry, X: h.X, Y: h.Y, Z: h.Z,
			HPPct: h.HPPct, Target: h.Target.String(), Flags: h.Flags})
	}
	raw, err := json.Marshal(rec)
	if err != nil {
		return
	}
	if _, err := w.framesF.Write(append(raw, '\n')); err != nil && !w.framesWarned {
		w.note("frames.jsonl: %v", err)
		w.framesWarned = true
	}
}

// saveCommanders GETs /api/v1/commanders, saves it and feeds the tracker's wipe rule.
func (w *walk) saveCommanders(reason string) {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	raw, err := w.dash.Commanders(ctx)
	if err != nil {
		if !w.dashWarned {
			w.note("dashboard: %v", err)
			w.dashWarned = true
		}
		return
	}
	w.dashSeq++
	name := fmt.Sprintf("commanders-%04d-%s.json", w.dashSeq, reason)
	_ = w.writeOut(name, raw)
	if bots, ok := commandersBots(raw, w.manifest.Character); ok && w.tracker != nil {
		w.tracker.Snapshot(w.env.now(), bots)
	}
}

// commandersBots finds the walk's commander in a /api/v1/commanders answer.
func commandersBots(raw json.RawMessage, character string) ([]raidwalk.DashBot, bool) {
	var v struct {
		Commanders []struct {
			Commander string `json:"commander"`
			Bots      []struct {
				Name  string `json:"name"`
				Alive bool   `json:"alive"`
			} `json:"bots"`
		} `json:"commanders"`
	}
	if err := json.Unmarshal(raw, &v); err != nil {
		return nil, false
	}
	for _, c := range v.Commanders {
		if !strings.EqualFold(c.Commander, character) {
			continue
		}
		out := make([]raidwalk.DashBot, 0, len(c.Bots))
		for _, b := range c.Bots {
			out = append(out, raidwalk.DashBot{Name: b.Name, Alive: b.Alive})
		}
		return out, true
	}
	return nil, false
}

// walkFightRow is one row of GET /api/v1/combat/fights.
type walkFightRow struct {
	ID       string `json:"id"`
	Key      string `json:"key"`
	Dur      uint32 `json:"dur"`
	Outcome  string `json:"outcome"`
	BossName string `json:"boss_name"`
	Leg      string `json:"leg"`
	Step     uint32 `json:"step"`
	Deaths   int    `json:"deaths"`
	Warnings int    `json:"warnings"`
	// BossPhase and BossPhaseEntry are the boss's phase in the fight (spec 6, D5): TortoiseBots' COMBAT_FIGHT
	// payload carries them after boss_name when the boss changed entry, and the summary passes them through.
	BossPhase      int    `json:"boss_phase,omitempty"`
	BossPhaseEntry uint32 `json:"boss_phase_entry,omitempty"`
	// Live marks a fight still running (COMBAT_EVENTS opened it, no COMBAT_FIGHT yet): it has no detail to load, and
	// the finished fight comes back under the same id.
	Live bool `json:"live"`
}

// bossLive says whether the row is a boss fight still in progress. The fights list is the
// run's history: a finished boss fight (win, wipe) stays in it and must not block recovery
// from a later trash wipe. The dashboard files a running fight with outcome "live".
func (r walkFightRow) bossLive() bool {
	return r.BossName != "" && (r.Outcome == "" || r.Outcome == "live")
}

// saveFights saves the fights list and one detail file per new key. It also keeps
// the review-1 boss evidence fresh (the same fields freshWipeRest and the
// bossUnknown gate read): every attempt records its fetch/parse error (or nil),
// and a success records the read time and the parsed rows.
func (w *walk) saveFights() {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	raw, err := w.dash.Fights(ctx, w.manifest.Character)
	if err != nil {
		w.bossCheckErr = err
		if !w.dashWarned {
			w.note("dashboard: %v", err)
			w.dashWarned = true
		}
		return
	}
	_ = w.writeOut("combat-fights.json", raw)
	var rows []walkFightRow
	if uerr := json.Unmarshal(raw, &rows); uerr != nil {
		w.bossCheckErr = uerr
		return
	}
	now := w.env.now()
	w.bossCheckErr = nil
	w.bossReadAt = now
	w.fightRows = rows
	for _, r := range rows {
		if r.ID == "" || r.Live || w.seenFights[r.ID] {
			continue
		}
		fs := raidwalk.FightSummary{ID: r.ID, Step: int(r.Step), Leg: r.Leg, DurMS: int64(r.Dur),
			Deaths: r.Deaths, Outcome: r.Outcome, BossName: r.BossName, Warnings: r.Warnings,
			BossPhase: r.BossPhase, BossPhaseEntry: r.BossPhaseEntry}
		idx, pending := w.pendingFights[r.ID] // the row's fields are read fresh on every poll
		// A fight whose detail could not be loaded or read is kept without it and fetched again on the next poll (and at teardown),
		// so one dashboard timeout does not lose its death recap for good.
		got := false
		if detail, derr := w.dash.Fight(ctx, r.Key); derr == nil {
			_ = w.writeOut("combat-fight-"+fightFileID(r.ID)+".json", detail)
			if d, ok := raidwalk.FightDetailOf(detail); ok {
				fs.Detail = &d
				got = true
			}
		}
		if pending {
			w.fights[idx] = fs
		} else {
			w.fights = append(w.fights, fs)
			idx = len(w.fights) - 1
		}
		if got {
			w.seenFights[r.ID] = true
			delete(w.pendingFights, r.ID)
		} else {
			w.pendingFights[r.ID] = idx
		}
	}
}

// fightFileID keeps a fight id safe for a file name.
func fightFileID(id string) string {
	var b strings.Builder
	for _, r := range id {
		if r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || r == '-' || r == '_' || r == '.' {
			b.WriteRune(r)
			continue
		}
		b.WriteByte('_')
	}
	return b.String()
}

// wipe handling (spec 3.5): stop, wait for a reset, revive only when quiet. A wipe or a near-wipe stop outside a
// boss fight recovers while recoveries are left (--wipe-resume): the dead raid is revived, rested out of combat
// and the run is resumed, and the watch keeps going (errWipeRecovered tells the two callers to carry on).
//
// errWipeRecovered is the recovery's success: handleWipe and the watch loop's near-wipe branch swallow it and keep
// watching. Anything else out of the recovery ends the walk with its own reason.
var errWipeRecovered = errors.New("wipe recovered")

// errRunWentOn is the rest's defence-in-depth signal: a status after the rest began shows the run
// running again and either "regrouped" or on a later step, so the rest and the resume are skipped.
var errRunWentOn = errors.New("the run went on by itself")

// handleWipe is an all-dead party with no boss engaged. Inside a boss fight, or with no recovery left, it ends the
// walk as before (walk 13 s7: a near-wipe stop ended the walk after the raid was already revived and rested by
// hand). Otherwise it revives, rests and resumes and the watch goes on.
func (w *walk) handleWipe(ctx context.Context, step int) error {
	// The run may already be regrouping by itself after the wipe (TB "regrouping by itself"):
	// it stays running, rests, gathers and walks back on its own, so the recovery must not rest
	// or resume it (walk 17b m910q s4, test2 s2). The note need not be the wipe's last status line:
	// live TB sends "regrouping by itself" and then "regroup: waiting for X (dead), ... reviving" for
	// the same step in one page, and it may even go on to its own "regrouped" or a line of the next
	// step, which also moves the step the wipe fires on. So the decision reads the recorded running
	// lines of this step (selfRegroupLine, which also takes a note whose own page already carried the
	// run past it) instead of the last status, and only the run's state still comes from that status:
	// a paused or done last line is no self-regroup.
	now := w.env.now()
	w.wipeAt = now
	selfRegroup := false
	w.regroupAt = time.Time{}
	if w.haveStatus && w.lastStatus.State == tbm.RunRunning {
		if o, ok := w.selfRegroupLine(step, now); ok {
			selfRegroup, w.regroupAt = true, o.at
		}
	}
	// The observations explained this wipe: a later wipe needs its own line since the last recovery.
	w.runLines = nil
	// A boss row engaged by the walk's own boss path (handleBoss) still ends the wipe as before: the walk owns that
	// fight and never revives under it. When the run LEADER fights the boss (runFightsBoss: --until none, or an until
	// that goes past the boss step), the wipe is the run's own and follows the normal wipe-resume flow. Walk 32: a
	// Mar'li boss-step wipe under --until none was read as a boss wipe and `.bot run stop` went out 0.7 s after the
	// "20 dead ... regrouping by itself" line (a-r1), and a Hakkar one 42 ms after it (e-r3), so no boss retry could
	// ever happen.
	runLeaderFights := false
	if w.bossEngagedNow() {
		runLeaderFights = w.runLeaderFightsEngagedBoss(ctx, step)
		if !runLeaderFights {
			w.note("wipe resume skipped: %s engaged", w.engagedBossWords())
			return w.endWipe(ctx, step)
		}
		w.note("wipe on a boss step: the run leader fights %s %s, so the wipe recovers like any other", w.engagedBossWords(), w.untilNoteWords())
	}
	if w.opts.wipeResume > 0 && w.wipeResumes >= w.opts.wipeResume {
		w.note("wipe resume skipped: no recoveries left (%d/%d)", w.wipeResumes, w.opts.wipeResume)
		return w.endWipe(ctx, step)
	}
	if w.wipeResumes >= w.opts.wipeResume { // --wipe-resume 0: the old ending, no note
		return w.endWipe(ctx, step)
	}
	if err := w.recoverWipe(ctx, step, selfRegroup, runLeaderFights); err == errWipeResumeSkipped {
		return w.endWipe(ctx, step)
	} else if terr, ok := err.(*timeoutWalkStop); ok {
		w.note("%s", terr.note)
		return stopf(terr.code, "%s", terr.reason)
	} else {
		return err
	}
}

// runLeaderFightsEngagedBoss says a wipe ON THE BOSS'S OWN STEP is the run's own fight: runFightsBoss hands that
// encounter to the run leader (a boss step under --until none or a boss-done until, or an until that goes past the
// step), so the walk owns no boss attempt and the wipe is an ordinary wipe (walk 32's Mar'li and Hakkar wipes). A boss
// row the frames still hold while the run wipes on TRASH keeps the old gate: that fight is not the step's, and reviving
// under it is not this rule's business. False for an unknown step (rows not the plan's) or no named engagement.
func (w *walk) runLeaderFightsEngagedBoss(ctx context.Context, step int) bool {
	entry, ok := w.engagedBossEntry()
	if !ok {
		return false
	}
	if b := w.bossStepOf(entry); b == 0 || b != step {
		return false
	}
	ok, _, _ = w.runFightsBoss(ctx, entry, false)
	return ok
}

// untilWords names the run's until for a note ("under --until none", "under the until (until step 248)").
func (w *walk) untilNoteWords() string {
	if strings.TrimSpace(w.opts.until) == "none" {
		return "under --until none"
	}
	if w.untilLabel == "" {
		return "under its until"
	}
	return "under the until (" + w.untilLabel + ")"
}

// bossEngagedNow is the "not a boss wipe" gate: handleBoss owns the engaged path, so a boss row the frames still
// hold (seen in the last 15 s, hp left, a target, or gone from the frames less than 10 s ago) ends the wipe as
// before. A row gone 10 s or lost 15 s with no live target is a reset, and the recovery may run.
func (w *walk) bossEngagedNow() bool {
	now := w.env.now()
	if w.bossSeenAt.IsZero() {
		return false
	}
	if now.Sub(w.bossSeenAt) >= 10*time.Second {
		return !w.bossTargetAt.IsZero() && now.Sub(w.bossTargetAt) < 30*time.Second
	}
	for _, row := range w.bossRows {
		if now.Sub(row.at) > 15*time.Second {
			continue
		}
		if row.hppct == 0 {
			continue
		}
		if row.target == 0 && row.hppct == 100 && (w.bossTargetAt.IsZero() || now.Sub(w.bossTargetAt) >= 30*time.Second) {
			continue
		}
		return true
	}
	return false
}

// endWipe is the walk-4 recipe's ending: stop, wait for a reset, revive only when quiet, exit 19.
func (w *walk) endWipe(ctx context.Context, step int) error {
	_ = w.stopRun(context.Background(), "")
	w.note("wipe at step %d", step)
	if w.dash != nil {
		w.saveCommanders("wipe")
		w.saveFights()
	}
	if err := w.waitReset(ctx, w.opts.resetWait); err != nil {
		return err
	}
	return w.revive(ctx, step)
}

// bossPhase is what the frames say about an engaged boss.
type bossPhase int

const (
	bossWaiting bossPhase = iota
	bossReset
	bossGone
)

// wipeBossFrame judges the latched boss from the frames before the 120 s
// unknown wait (walk 26 test2-ven-door: Jeklik at 0 % while the raid died).
// kill: the frames' last view of a boss unit is HP 0. The dead row has no
// freshness cap, so a boss that died and then despawned still counts (the
// frames' last view of that guid was dead) — but only inside the current
// attempt: a live row of the family must have been seen since the last wipe
// recovery (bossAttemptStart, the same "a dead row never starts an attempt"
// rule bossAttemptEngaged uses), the dead row at or after it, and no live
// unit of the family seen alive after it (a reset or a respawn left the boss
// out of view, so the dead row is another attempt's). Otherwise a stale
// corpse — or a fake death with no live row in this attempt — is no kill.
// alive: a fresh (<= 15 s) row is alive (HP > 0) with no target and no live
// target — the reset shape — so the boss is alive and the resume is safe.
// Otherwise no usable frame evidence: keep the unknown wait. A 1-5 % alive
// row is never a kill.
func (w *walk) wipeBossFrame(now time.Time) (kill bool, alive bool, words string) {
	var haveDead, haveLive bool
	var deadAt, liveAt time.Time
	var deadEntry, liveEntry uint32
	for entry, row := range w.bossRows {
		if !w.bossSet[entry] {
			continue
		}
		if row.hppct == 0 {
			if !haveDead || row.at.After(deadAt) || (row.at.Equal(deadAt) && entry < deadEntry) {
				haveDead, deadAt, deadEntry = true, row.at, entry
			}
			continue
		}
		if now.Sub(row.at) > 15*time.Second {
			continue
		}
		if row.target != 0 {
			continue
		}
		if row.hppct == 100 && (w.bossTargetAt.IsZero() || now.Sub(w.bossTargetAt) >= 30*time.Second) {
			if !haveLive || row.at.After(liveAt) || (row.at.Equal(liveAt) && entry < liveEntry) {
				haveLive, liveAt, liveEntry = true, row.at, entry
			}
		}
	}
	if haveDead && (!haveLive || !deadAt.Before(liveAt)) {
		if attemptAt := w.bossAttemptStart(w.bossUnits(deadEntry)); !attemptAt.IsZero() && !deadAt.Before(attemptAt) {
			return true, false, w.bossRule.Words(deadEntry)
		}
	}
	if haveLive {
		return false, true, w.bossRule.Words(liveEntry)
	}
	return false, false, ""
}

// bossState reads the engaged boss's row across frames (freshness 15 s). A row gone from the frames for 10 s
// is a kill only when a hire is alive; with the party dead it is a reset once the last target is 30 s old (a
// revive is never risked on a guess).
func (w *walk) bossState(now time.Time, entry uint32) bossPhase {
	row, seen := w.bossRows[entry]
	if !seen || now.Sub(row.at) > 15*time.Second {
		if w.bossSeenAt.IsZero() || now.Sub(w.bossSeenAt) < 10*time.Second {
			return bossWaiting
		}
		if w.anyAlive(now) {
			return bossGone
		}
		if now.Sub(w.bossTargetAt) >= 30*time.Second {
			return bossReset
		}
		return bossWaiting
	}
	if row.hppct == 0 {
		return bossGone
	}
	if row.target != 0 {
		return bossWaiting
	}
	if row.hppct == 100 && now.Sub(w.bossTargetAt) >= 30*time.Second {
		return bossReset
	}
	return bossWaiting
}

// nextBossStep is the first 1-based step index at or after from whose row is a plan boss step
// (planexport.KindBoss); 0 when the rows hold none there. It is what the boss-done hand-over
// (runFightsBoss's early rule) compares the run's step against.
func nextBossStep(rows planexport.Rows, from int) int {
	for i := from; i >= 1 && i <= len(rows.Steps); i++ {
		if rows.Steps[i-1].Kind == planexport.KindBoss {
			return i
		}
	}
	return 0
}

const (
	// bossKillYd is the radius around the boss row the toolkit's corpse read searches: TB's own boss step wants a
	// dead creature of the step's entry within 30 yd of the spot, so a corpse a little further out is still the
	// boss that died there.
	bossKillYd = 40
)

// bossCorpseWait bounds one toolkit corpse read. The toolkit answers at once; a server without the toolkit
// answers nothing at all, so the read is bounded rather than waited out (the first failure, the deadline
// included, turns the reads off for the rest of the walk). A var so a test can shorten it.
var bossCorpseWait = 10 * time.Second

// bossNotFoundAtBossStep says the pause's note names a boss that is not found and the pause stands on that
// boss's OWN row of the server's rows (walk 24 B, decision B-D4): only then may the walk consider the skip.
// A "not found" on another step, or rows that are not the server's, is left to the old pause path.
func (w *walk) bossNotFoundAtBossStep(step int, note string) (uint32, bool) {
	entry, ok := raidwalk.BossNotFound(note)
	if !ok {
		return 0, false
	}
	if w.lastStatus.Steps != len(w.rows.Steps) || step < 1 || step > len(w.rows.Steps) {
		return 0, false
	}
	row := w.rows.Steps[step-1]
	if row.Kind != planexport.KindBoss || row.Entry != entry {
		return 0, false
	}
	return entry, true
}

// bossKilled says the frames show a boss unit of this step killed in this walk: the evidence that the run
// leader's "boss <N> not found" is a kill whose corpse it cannot see (walk 24 test2 r2: Thekal 14599 comes back as
// 14509, and TB needs a dead creature of the boss entry itself, so it pauses).
//
// It judges per UNIT (w.hostiles is keyed by GUID): an entry's newest row can be one unit's while another unit of
// the same entry still stands, so a per-entry last sighting reads a live boss's row off an add's corpse (review
// round 1, item 1). The boss units are the step's boss entry and the phase entries that are the boss reborn; an
// add that exists several at a time (Thekal's Zulian Guardians 15068), a hold entry and a `first` add are not boss
// units (bossUnits).
//
// The evidence holds when ALL of:
//   - a boss unit is affirmatively dead: a frame row at 0 % for its GUID seen at or after this attempt's start
//     (bossAttemptStart) and only after the frames saw that same GUID alive (above 0 %) in this attempt
//     (bossAliveSeen, reset when an attempt starts and by a wipe recovery), with no boss unit of the family
//     seen alive after that row, or the toolkit proves the kill (bossCorpse): TB's own search reads the live
//     view, so a boss that died out of the party's sight leaves no row at all;
//   - no boss unit is seen alive: no frame row fresher than 15 s with hp left;
//   - a hire is alive (a wipe is no kill).
//
// A stale low-hp sighting alone is NEVER a kill: a boss that evades at 20 % and leaves the frames is not a corpse.
// w.hostiles is never pruned, so a dead row alone says nothing about when it died: it counts only inside the
// current attempt, only for a guid this attempt saw alive before its death, and a live sighting after it (a reset
// or a respawn left the boss out of view for the pause) voids it — then the toolkit read decides (review round 3,
// revive r7). The returned string is the evidence for the note.
func (w *walk) bossKilled(ctx context.Context, entry uint32, now time.Time) (bool, string) {
	units := w.bossUnits(entry)
	attemptAt := w.bossAttemptStart(units)
	var alive, dead hostileState
	haveAlive, haveDead := false, false
	for g, h := range w.hostiles {
		// The guid rule first (spec 6, D5 b, #1453): a row whose guid the frames showed as this boss's own entry is
		// the boss whatever entry it now reads, so the add rule never applies to it. Every other row must be a boss
		// unit of this boss (its own entry or a phase entry that is not an add).
		if w.bossGUIDs[g] != entry {
			if w.bossAdds[h.entry] || !inEntries(units, h.entry) {
				continue
			}
		}
		if h.hppct == 0 {
			if attemptAt.IsZero() || h.at.Before(attemptAt) {
				continue // an earlier attempt's corpse
			}
			if seen, ok := w.bossAliveSeen[g]; !ok || seen.After(h.at) {
				continue // the frames never showed this guid alive before its death: a stale or foreign corpse
			}
			if !haveDead || dead.at.Before(h.at) {
				dead, haveDead = h, true
			}
			continue
		}
		if !haveAlive || alive.at.Before(h.at) {
			alive, haveAlive = h, true
		}
	}
	if haveAlive && now.Sub(alive.at) <= 15*time.Second {
		return false, fmt.Sprintf("%d is alive in the frames (%d %%)", alive.entry, alive.hppct)
	}
	if haveDead && haveAlive && alive.at.After(dead.at) {
		// A boss unit was seen alive after that corpse: the encounter reset or the boss respawned, and the row is
		// another attempt's (the live unit is out of view for the pause, so nothing later supersedes it).
		haveDead = false
	}
	ev := ""
	if haveDead {
		ev = fmt.Sprintf("%d seen dead in the frames (0 %%, %s ago)", dead.entry, durWords(now.Sub(dead.at)))
	} else if ok, s := w.bossCorpse(ctx, entry, units, now); ok {
		ev = s
	}
	if ev == "" {
		return false, "no boss unit dead in the frames and no corpse at the boss row"
	}
	if !w.anyAlive(now) {
		return false, "no hire alive (a wipe, not a kill)"
	}
	return true, fmt.Sprintf("%s; %d hires alive", ev, len(w.hireList()))
}

// bossDownNote is what the run leader's status note starts with once the boss step's boss is down (TortoiseBots
// FightStepEnd, spec 5): `boss down`, or `boss down: he died as <entry>` when the boss changed entry.
const bossDownNote = "boss down"

// bossRowAt is the plan's boss row at a step of the run's own rows (a step outside them is no boss row): safe
// against a 0/1-based mix-up, and what observeBossStatus tracks the boss step with.
func (w *walk) bossRowAt(step int) (planexport.StepRow, bool) {
	if step < 1 || step > len(w.rows.Steps) {
		return planexport.StepRow{}, false
	}
	if row := w.rows.Steps[step-1]; row.Kind == planexport.KindBoss {
		return row, true
	}
	return planexport.StepRow{}, false
}

// bossStepOf is the step of the plan's boss row whose entry is entry (0: none of the rows is his own boss row).
func (w *walk) bossStepOf(entry uint32) int {
	for i, s := range w.rows.Steps {
		if s.Kind == planexport.KindBoss && s.Entry == entry {
			return i + 1
		}
	}
	return 0
}

// bossTrackAt is the record of a boss step (or of the boss's entry when the step is unknown), created on first
// sight. A known step is keyed by the step, so each boss row the run's statuses show keeps its own record.
func (w *walk) bossTrackAt(step int, entry uint32) *bossStepTrack {
	if w.bossTracks == nil {
		w.bossTracks = map[bossKey]*bossStepTrack{}
	}
	k := bossKey{step: step}
	if step <= 0 {
		k = bossKey{entry: entry}
	}
	t, ok := w.bossTracks[k]
	if !ok {
		t = &bossStepTrack{entry: entry, step: step}
		w.bossTracks[k] = t
	}
	return t
}

// observeBossStatus records the run's own boss-step notes per boss step (spec 6, D5): the kill is the run leader's
// `boss down` note, and it fires when the run LEAVES the boss step (a later step, or the same step reporting another
// kind). Either the last note the run reported on the boss step or the first status after it must start with
// `boss down` (both are read: one poll may carry both lines, so the last status alone would miss the note). Each
// boss row's step gets its own record, so a walk past several bosses lists each one; a skip the walk sent is stored
// on the step it was sent for (bossSkipStep). A run that ends right after its boss step never sends that note at
// all (bossEndStatus reads the end), and a boss-kind status that matches no boss row at its step records nothing
// (the rows the walk reads are not the deployed ones then).
func (w *walk) observeBossStatus(st tbm.RunStatus, now time.Time) {
	// The run ending right after the boss step (see bossEndStatus) is read first: a plan whose last row is the boss
	// reports the end ON the boss step, so the boss-row branch below would swallow it.
	if t := w.bossEndStatus(st, now); t != nil {
		return
	}
	if row, ok := w.bossRowAt(st.Step); ok && st.Kind == planexport.KindBoss {
		t := w.bossTrackAt(st.Step, row.Entry)
		t.engaged = true
		t.note = st.Note
		return
	}
	if st.Kind == planexport.KindBoss {
		// The run's boss step is no boss row in the rows the walk reads (without --sql they are the plan JSON's, not
		// the deployed ones): a kill recorded by step would land on another step's row. Skip the record once per step.
		if w.bossMismatch == nil {
			w.bossMismatch = map[int]bool{}
		}
		if !w.bossMismatch[st.Step] {
			w.bossMismatch[st.Step] = true
			w.note("boss record skipped: plan rows are not the deployed rows")
			w.logLine(now, fmt.Sprintf("boss record skipped at step %d (%s): plan rows are not the deployed rows", st.Step, st.Note))
		}
		return
	}
	// A leaving status (a step that is not a boss row, or the boss step reporting another kind) reports the kill
	// of the nearest boss row at or below the status's step: its track is created when the run never stood on it,
	// so a boss row no status named still takes its leaving status's `boss down` note. A backward move (a wipe)
	// reads the earlier row.
	step, row := w.nearestBossRow(st.Step)
	if step == 0 {
		return
	}
	t := w.bossTrackAt(step, row.Entry)
	if t.killed {
		return
	}
	ev := t.note
	if strings.HasPrefix(st.Note, bossDownNote) {
		ev = st.Note
	}
	if !strings.HasPrefix(ev, bossDownNote) {
		return
	}
	t.killed, t.evidence = true, ev
	w.logLine(now, fmt.Sprintf("boss %d down at step %d: %s", t.entry, t.step, ev))
}

// nearestBossRow is the plan's boss row nearest at or below step: its 1-based step and the row. (0, zero) when no
// boss row is at or below. The leaving rule credits THIS row, not the highest track: a boss row the run's statuses
// never named still takes the note its leaving status carries (#1453 walk side).
func (w *walk) nearestBossRow(step int) (int, planexport.StepRow) {
	for i := min(step, len(w.rows.Steps)) - 1; i >= 0; i-- {
		if w.rows.Steps[i].Kind == planexport.KindBoss {
			return i + 1, w.rows.Steps[i]
		}
	}
	return 0, planexport.StepRow{}
}

// bossSkippedInNote says the run's end note names this boss in its "boss skipped, not seen killed: ..." list
// (TortoiseBots DoneNote, UnclearedBosses: every boss step the run ended without clearing). TB then ended the run
// without the kill, so the end rule must not credit it.
func bossSkippedInNote(note string, entry uint32, step int) bool {
	const marker = "boss skipped, not seen killed: "
	i := strings.Index(note, marker)
	if i < 0 {
		return false
	}
	return strings.Contains(note[i+len(marker):], fmt.Sprintf("boss %d (step %d)", entry, step))
}

// bossFramesDead says the frames saw a guid of this boss (or a script-changed form of it, bossGUIDs) dead in the
// current attempt: a 0 % row at or after the attempt's start (bossAttemptStart), the frames-only half of
// bossKilled's evidence. The end rule reads it when the run's own statuses never showed the step engaged.
func (w *walk) bossFramesDead(entry uint32) bool {
	units := w.bossUnits(entry)
	attemptAt := w.bossAttemptStart(units)
	if attemptAt.IsZero() {
		return false
	}
	for g, h := range w.hostiles {
		if w.bossGUIDs[g] != entry {
			if w.bossAdds[h.entry] || !inEntries(units, h.entry) {
				continue
			}
		}
		if h.hppct == 0 && !h.at.Before(attemptAt) {
			return true
		}
	}
	return false
}

// bossEndStatus credits the boss kill of a run that ENDED right after its boss step (#1453 walk side): TB's
// NextStep overwrites the boss step's "boss down" note in the same call, with the until stop's
// "stopped before step N (...)" (StopAtUntil/EndRunEarly) or DoneNote's "plan done (...)" (runtime/DungeonRunPolicy.h
// NextStep around line 3841), so a plan ending on a boss (43 of 46) never sends `boss down` at all. The end is
// credited when all of these hold:
//   - the status is the run's end (state done, a `stopped before step` or `plan done` note) and its step is the boss
//     step + 1, or the plan's own end while the run stood on its LAST boss row (the status line clamps the step
//     there, StatusParts: shown = min(step+1, total));
//   - the walk's LAST status was on that boss step (the run ended right after it);
//   - the note's "boss skipped, not seen killed: ..." list does not name this boss (TB's own verdict: it ended
//     without clearing the step, so no kill), and
//   - the frames saw the boss's guid (or a script-changed form of it) dead in this attempt, or the run's earlier
//     status on the boss step was engaged.
//
// The evidence says which. nil when the rule does not apply; a track already killed or skipped is left alone.
func (w *walk) bossEndStatus(st tbm.RunStatus, now time.Time) *bossStepTrack {
	if !w.haveStatus || st.State != tbm.RunDone {
		return nil
	}
	if !strings.HasPrefix(st.Note, "stopped before step") && !strings.HasPrefix(st.Note, "plan done") {
		return nil
	}
	b := w.lastStatus.Step
	row, ok := w.bossRowAt(b)
	if !ok {
		return nil
	}
	atPlanEnd := st.Steps > 0 && st.Step == st.Steps
	if st.Step != b+1 && !(atPlanEnd && st.Step == b) {
		return nil
	}
	t := w.bossTrackAt(b, row.Entry)
	if t.killed || t.skipped || bossSkippedInNote(st.Note, row.Entry, b) {
		return t
	}
	switch {
	case w.bossFramesDead(row.Entry):
		t.killed, t.evidence = true, "run ended right after the boss step; frames"
	case t.engaged:
		t.killed, t.evidence = true, "run ended right after the boss step; engaged"
	default:
		return t
	}
	w.logLine(now, fmt.Sprintf("boss %d down at step %d: %s", t.entry, t.step, t.evidence))
	return t
}

// bossGUIDFor returns the guid the frames showed with a plan boss's own entry (the one with the most forms when
// several rows carry the same entry), or "" when no guid was recorded. It is what bossFormsForTrack reads the
// phase from.
func (w *walk) bossGUIDFor(entry uint32) string {
	best, bestG := 0, ""
	for g, boss := range w.bossGUIDs {
		if boss != entry {
			continue
		}
		if forms := w.bossForms[g]; len(forms) > best {
			best, bestG = len(forms), g
		}
	}
	return bestG
}

// bossFormsFor is the phase the frames saw the boss's guid with (spec 6, D5): the number of entries recorded on it
// (one per change, so 1 = never changed) and the last entry it read. Falls back to (1, entry) when no guid of the
// boss was ever recorded (a boss whose kill came from the run's own note alone).
func (w *walk) bossFormsFor(entry uint32) (phase int, phaseEntry uint32) {
	best, last := 0, entry
	for g, boss := range w.bossGUIDs {
		if boss != entry {
			continue
		}
		forms := w.bossForms[g]
		if len(forms) > best {
			best, last = len(forms), forms[len(forms)-1]
		}
	}
	if best == 0 {
		return 1, entry
	}
	return best, last
}

// bossFormsForTrack is bossFormsFor for one tracked boss step: the forms of the guid that step's boss row was seen
// with (the record's guid, else the entry's best guid), so a walk past several bosses reads each boss's own phase.
func (w *walk) bossFormsForTrack(t *bossStepTrack) (phase int, phaseEntry uint32) {
	if t.guid == "" {
		t.guid = w.bossGUIDFor(t.entry)
	}
	if t.guid != "" {
		if forms := w.bossForms[t.guid]; len(forms) > 0 {
			t.forms = forms
			return len(forms), forms[len(forms)-1]
		}
	}
	return w.bossFormsFor(t.entry)
}

// resetBossForms clears each boss guid's form history back to its identity entry when a retry starts (spec 6, D5,
// #1453): the phase counts the entries the retry's own frames saw, not the wiped attempt's. It MUST come after the
// recovery's last frame poll (the reset wait, the revive, the rest all read the frames and the boss's reset lands in
// them) and before the retry's first one: clearing first let those polls re-add the failed attempt's forms, and the
// record showed a phase the retry never saw (review round 4).
func (w *walk) resetBossForms() {
	for g, entry := range w.bossGUIDs {
		w.bossForms[g] = []uint32{entry}
	}
}

// bossKillSummaryFor is one boss step's summary record: its entry and step, whether the encounter was engaged,
// killed or skipped, and the phase its guid was seen with. A boss that was engaged but never reported dead says so,
// with the reason, instead of staying silent.
func (w *walk) bossKillSummaryFor(t *bossStepTrack) raidwalk.BossKillSummary {
	phase, phaseEntry := w.bossFormsForTrack(t)
	k := raidwalk.BossKillSummary{
		Entry: t.entry, Step: t.step, Engaged: t.engaged,
		Killed: t.killed || t.skipped, Skipped: t.skipped,
		Phase: phase, PhaseEntry: phaseEntry,
	}
	switch {
	case t.killed:
		k.Evidence = t.evidence
	case t.skipped:
		k.Evidence = t.evidence
	default:
		k.Evidence = "the run leader never reported boss down"
	}
	return k
}

// goalBossStep is the boss row the run walks to, 1-based (0: no goal boss): the first boss row from the run's start
// step (--from or --place, else step 1) that the until still walks to. The server stops BEFORE the until's step, so
// the pass must test the rows from startStep to one below the stop. --until boss stops before its boss and has no
// goal boss; --until none walks to the end, so every row is in range (the first boss row is the run's next goal).
func (w *walk) goalBossStep() int {
	limit := len(w.rows.Steps) + 1
	switch {
	case w.untilStep > 0:
		limit = w.untilStep
	case w.bossDoneStep > 0:
		limit = w.bossDoneStep + 1
	case len(w.untilWords) > 0:
		return 0 // until boss (or an until whose step is not known yet): no goal boss
	}
	from := w.startStep
	if from < 1 {
		from = 1
	}
	for i := from - 1; i < len(w.rows.Steps) && i+1 < limit; i++ {
		if w.rows.Steps[i].Kind == planexport.KindBoss {
			return i + 1
		}
	}
	return 0
}

// bossRowForFight is the 1-based boss row a dashboard fight record names: its own step when that step is a boss row
// of the rows the walk reads and the record's leg is that row's leg, else the leg's boss row. 0 when the record
// names no plan leg or no boss row (a record without a leg cannot be tied to these rows).
func bossRowForFight(rows planexport.Rows, f raidwalk.FightSummary) int {
	if f.Leg == "" {
		return 0
	}
	legName := func(leg int) string {
		if leg >= 0 && leg < len(rows.Legs) {
			return rows.Legs[leg].Name
		}
		return ""
	}
	if f.Step >= 1 && f.Step <= len(rows.Steps) {
		s := rows.Steps[f.Step-1]
		if s.Kind == planexport.KindBoss && legName(s.Leg) == f.Leg {
			return f.Step
		}
	}
	for i, s := range rows.Steps {
		if s.Kind == planexport.KindBoss && legName(s.Leg) == f.Leg {
			return i + 1
		}
	}
	return 0
}

// creditFightKills credits the dashboard's own boss fight records (combat-fight-*.json, one per fight the walk's
// polls wrote) to the boss tracks before the summary reads them: an outcome "kill" record of the goal boss's own
// row means the goal boss is dead even when the walk never saw it — its frames stopped with the run (walk 41: tj-8
// ended exit 19 "boss 14517 engaged at step 50" two seconds before the tank's late pull landed, and the raid killed
// Jeklik 153.7 s later in fight 68-1; the next walk's summary carried that fight yet said boss_kill null, so the
// autopilot relaunched walks for a dead boss). A record whose phase entry is not the row's boss family is not this
// row's kill (Thekal's reborn 14509 is a phase entry of 14599).
func (w *walk) creditFightKills() {
	if len(w.fights) == 0 || len(w.rows.Steps) == 0 {
		return
	}
	goal := w.goalBossStep()
	for _, f := range w.fights {
		if !strings.EqualFold(strings.TrimSpace(f.Outcome), "kill") {
			continue
		}
		step := bossRowForFight(w.rows, f)
		if step == 0 || goal == 0 || step != goal {
			continue
		}
		row := w.rows.Steps[step-1]
		if f.BossPhaseEntry != 0 && !w.bossFamily(row.Entry)[f.BossPhaseEntry] {
			continue
		}
		t := w.bossTrackAt(step, row.Entry)
		if t.killed {
			continue
		}
		who := f.BossName
		if who == "" {
			who = strconv.Itoa(int(row.Entry))
		}
		t.engaged = true
		t.killed = true
		t.evidence = fmt.Sprintf("fight %s: %s killed (outcome kill)", f.ID, who)
		w.logLine(w.env.now(), fmt.Sprintf("boss %d killed at step %d: fight %s %s (outcome kill)", row.Entry, step, f.ID, who))
	}
}

// bossKillSummaries is the summary's boss_kills list: one record per boss step the run's statuses showed, ordered by
// step (unknown steps by entry). nil when no boss row was seen.
func (w *walk) bossKillSummaries() []raidwalk.BossKillSummary {
	if len(w.bossTracks) == 0 {
		return nil
	}
	out := make([]raidwalk.BossKillSummary, 0, len(w.bossTracks))
	for _, t := range w.bossTracks {
		out = append(out, w.bossKillSummaryFor(t))
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Step != out[j].Step {
			return out[i].Step < out[j].Step
		}
		return out[i].Entry < out[j].Entry
	})
	return out
}

// bossKillSummary is the summary's single boss_kill object (spec 6, D5): the LAST engaged boss's record. nil when no
// boss was engaged. The tracker's one-shot latch is inserted as a record in finish (L4), before the summary reads
// the tracks, so this reads only the tracks.
func (w *walk) bossKillSummary() *raidwalk.BossKillSummary {
	var best *bossStepTrack
	for _, t := range w.bossTracks {
		if !t.engaged {
			continue
		}
		if best == nil || t.step > best.step || (t.step == best.step && t.entry > best.entry) {
			best = t
		}
	}
	if best == nil {
		return nil
	}
	k := w.bossKillSummaryFor(best)
	return &k
}

// bossUnits is the boss's units for the kill evidence: the step's boss entry and the phase entries that are the
// boss reborn (plan.Legs, the leg whose Boss.Entry is boss; Zul'Gurub's Thekal 14599 -> 14509). The `first` adds
// and the hold entries are not boss units, and neither is a phase entry the frames have shown more than one live
// unit of at once (w.bossAdds, trackFrame): an add that exists several at a time, such as Thekal's two Zulian
// Guardians 15068, leaves corpses that prove nothing (review round 1, item 1). Looked up by the boss entry, not by
// a StepRow.Leg index, so a 0/1-based mix-up cannot bite.
func (w *walk) bossUnits(boss uint32) []uint32 {
	units := []uint32{boss}
	if w.plan == nil {
		return units
	}
	for i := range w.plan.Legs {
		l := &w.plan.Legs[i]
		if l.Boss == nil || l.Boss.Entry != boss {
			continue
		}
		for _, e := range l.Boss.PhaseEntries {
			if w.bossPhaseUnit(l.Boss, e) {
				units = append(units, e)
			}
		}
		return units
	}
	return units
}

// bossPhaseUnit says the entry is a boss unit among b's phase entries: the boss's own entry is not a phase
// unit, and a phase entry that is a `first` add, a hold entry or an add the frames showed several live units
// of at once (w.bossAdds, trackFrame) is not one either (bossUnits).
func (w *walk) bossPhaseUnit(b *dungeon.Boss, entry uint32) bool {
	if b == nil || entry == b.Entry {
		return false
	}
	for _, e := range b.PhaseEntries {
		if e == entry {
			return !inEntries(b.First, e) && !inHold(b.Hold, e) && !w.bossAdds[e]
		}
	}
	return false
}

// bossUnit says the entry can be a boss unit of a plan boss: a leg's boss entry or one of its phase units
// (bossUnits, bossPhaseUnit). It is what trackFrame records the attempt start for, so an add that exists
// several at a time never starts or bounds an attempt.
func (w *walk) bossUnit(entry uint32) bool {
	if w.plan == nil {
		return false
	}
	for i := range w.plan.Legs {
		b := w.plan.Legs[i].Boss
		if b == nil {
			continue
		}
		if entry == b.Entry || w.bossPhaseUnit(b, entry) {
			return true
		}
	}
	return false
}

// bossAttemptStart is when the current attempt of these units began: the earliest engagement the frames
// recorded (w.bossAttemptAt, trackFrame). Zero when no unit was seen engaged since the last wipe recovery or
// the watch began, and then bossKilled accepts no dead row at all.
func (w *walk) bossAttemptStart(units []uint32) time.Time {
	var first time.Time
	for _, u := range units {
		at := w.bossAttemptAt[u]
		if at.IsZero() {
			continue
		}
		if first.IsZero() || at.Before(first) {
			first = at
		}
	}
	return first
}

// inHold says the entry is one of the hold's own entries (nil hold: none).
func inHold(h *dungeon.Hold, e uint32) bool { return h != nil && inEntries(h.Entries, e) }

// bossCorpseWaitS is how long the toolkit must keep reporting the corpse dead before it proves the kill:
// the second read at least this far after the first still shows it dead, with no alive frame for that
// guid in between (revive r6: Thekal's fake death dies and is raised again within seconds).
const bossCorpseWaitS = 15

// bossCorpse says the toolkit proves the kill: a corpse of a boss unit within bossKillYd of the boss row
// whose guid the frames showed ALIVE in this attempt (w.bossAliveSeen, trackFrame), confirmed by a second
// read at least 15 s after the first with no alive frame for that guid in between. TB's boss step reads
// the live view only, so a boss killed while the party was dead or away leaves no frame row at all; the
// toolkit's `q units <r> <x> <y> <z> all dead e=<entry>` reads the map's creatures, corpses included
// (TortoiseBots tools/eluna/test-toolkit tt_commands.lua Q.units: the `dead` flag asks for both, and
// TT.unitSnap carries `dead`). A refused line, no reply or an error is no evidence: the walk waits as a
// pause, and the first failure turns the reads off for the rest of the walk (a server without the toolkit
// must not cost the watch loop a timeout on every tick).
//
// The wait is per GUID (key of w.hostiles, the frame key): the first sighting starts it and returns no
// evidence yet; a live frame for that guid in between voids it (trackFrame drops the wait); only the
// second sighting 15 s later proves the kill. Without the frames' alive link the corpse is a stale or
// foreign one and proves nothing (fail closed).
//
// Known limits: the commander is never pulled into the hires' copy first (no `.appear` here), so with no shared
// copy the read stays refused and the frames alone decide; a refused re-read after the move latches as a new
// toolkit failure instead of moving a second time.
func (w *walk) bossCorpse(ctx context.Context, boss uint32, units []uint32, now time.Time) (bool, string) {
	if w.bossToolkitOff {
		return false, ""
	}
	spot, ok := w.bossSpot(boss)
	if !ok {
		return false, ""
	}
	read := func(e uint32) (ttResult, error) {
		readCtx, cancel := context.WithTimeout(ctx, bossCorpseWait)
		defer cancel()
		return w.tt(readCtx, fmt.Sprintf("q units %s %s %s %s all dead e=%d", num32(bossKillYd), num32(spot.X), num32(spot.Y), num32(spot.Z), e))
	}
	fail := func(format string, a ...any) (bool, string) {
		w.bossToolkitOff = true
		w.note("boss %d: "+format+": the frames alone decide from here", append([]any{boss}, a...)...)
		return false, ""
	}
	moved := false
	for _, e := range units {
		for {
			out, err := read(e)
			if err != nil {
				if ctx.Err() != nil {
					return false, "" // the walk itself was interrupted: no verdict, and no latch on a cancelled read
				}
				// The read's own deadline (bossCorpseWait) is a toolkit failure like any other: latch it, so a
				// toolkit that never answers costs the watch loop one bounded wait, not one per tick.
				// The toolkit's corpse read is range-limited to 150 yd: a refused point_far read fails only
				// because the commander stands too far away. Move it to the boss row once, with the same go
				// the revive needs before its own `.go xyz` (GMVisibleOff), wait for the landing with the
				// revive's own wait (waitRevivePoint: the `.go xyz` answers at once but the server lands the
				// character a moment later, and a re-read at once is refused again and latches the reads off),
				// and only then note the move and re-read the same entry once.
				if !moved && strings.Contains(err.Error(), "point_far") {
					ready, ok, note := w.corpseMoveReady(ctx, boss)
					if note != "" {
						w.note("%s", note)
					}
					if !ok {
						return false, ""
					}
					if gerr := w.gm(ctx, raidwalk.GMVisibleOff); gerr != nil {
						if ctx.Err() != nil {
							return false, ""
						}
						return fail("the toolkit corpse read failed (point_far, and the visible-off before the move to the boss row failed: %v)", gerr)
					}
					moved = true
					if gerr := w.goPlace(ctx, spot.X, spot.Y, spot.Z+2); gerr != nil {
						if ctx.Err() != nil {
							return false, ""
						}
						return fail("the toolkit corpse read failed (point_far, and the move to the boss row failed: %v)", gerr)
					}
					waitStart := w.env.now()
					if gerr := w.waitRevivePoint(ctx, spot, ready.from, ready.seq, true, time.Time{}); gerr != nil {
						if ctx.Err() != nil {
							return false, ""
						}
						return fail("the toolkit corpse read failed (point_far, and the move to the boss row did not land: %v after %s)", gerr, durWords(w.env.now().Sub(waitStart)))
					}
					w.note("boss %d: the commander moved to the boss row for the corpse read (the toolkit read is limited to 150 yd)", boss)
					continue
				}
				// Only a failure after the move, or any non-point_far error, latches the reads off, as today.
				return fail("the toolkit corpse read failed (%v)", err)
			}
			body, ok := ttLast(out)
			if !ok {
				return fail("the toolkit corpse read gave no reply")
			}
			for _, m := range body.list("units") {
				u := ttUnitOf(m)
				if u.Entry != e || !u.Dead || u.GUID == "" {
					continue
				}
				key, err := bossCorpseKey(u.GUID)
				if err != nil {
					continue // a guid the frames could never name is no link to this attempt
				}
				seen, ok := w.bossAliveSeen[key]
				if !ok {
					continue // never seen alive in this attempt: a stale or foreign corpse
				}
				if dead, ok := w.hostiles[key]; ok && dead.hppct == 0 && seen.After(dead.at) {
					continue // the frames' only live sighting is after the death: a raised boss, not this corpse
				}
				if alive, ok := w.hostiles[key]; ok && alive.hppct > 0 && alive.at.After(seen) {
					seen = alive.at // a live row the frame maps hold but trackFrame never filed
				}
				first, waiting := w.bossCorpseSeen[key]
				if !waiting {
					if w.bossCorpseSeen == nil {
						w.bossCorpseSeen = map[string]time.Time{}
					}
					w.bossCorpseSeen[key] = now
					continue // the first sighting starts the wait; the kill needs the second read
				}
				if seen.After(first) {
					// The guid was alive again after the first corpse sighting (a fake death raised
					// the boss): the wait is void, not confirmed. Start over from this read.
					w.bossCorpseSeen[key] = now
					continue
				}
				if now.Sub(first) < bossCorpseWaitS*time.Second {
					continue
				}
				return true, fmt.Sprintf("the toolkit sees %d dead %s yd from the boss row (guid %s, still dead %s later)", e, num32(u.Dist), u.GUID, durWords(now.Sub(first)))
			}
			break
		}
	}
	return false, ""
}

// bossCorpseKey is the frame-map key of a toolkit corpse guid: the frames key w.hostiles by
// packet.GUID.String ("0x%016X"), the toolkit sends the same hex (TT.guidHex). The key matches
// only when the hex parses, so a malformed guid never links to the attempt.
func bossCorpseKey(guid string) (string, error) {
	g, err := packet.ParseGUID(guid)
	if err != nil || g == 0 {
		return "", fmt.Errorf("bad guid %q", guid)
	}
	return g.String(), nil
}

// corpseMoveReadiness is what corpseMoveReady proved before the boss-row move: the commander's map (the landing
// rule's fromMap) and the teleport sequence read just before the move.
type corpseMoveReadiness struct {
	from uint32
	seq  uint64
}

// corpseMoveReady reads the commander's status before the corpse read's boss-row move: its map is the landing
// rule's fromMap (see reviveLanded), and it must be the plan's map. From the portal map (0 in live walks, 1 in the
// walk-25 tests whose fake never left the spawn map) a `.go xyz ... 309` opens a NEW copy instead of the hires' one
// (walk 23 mangosd: hires in inst 101, commander in 104): no move goes out, and the reads latch with the "not on
// map" note. For placed walks the toolkit instance must also still match w.placeInst (as placeStillJoined compares):
// another copy is no move either, latched with the instance note. ok is false on a ctx cancel or a latched refusal
// (note non-empty on the latter): no verdict, and no move in either case.
func (w *walk) corpseMoveReady(ctx context.Context, boss uint32) (corpseMoveReadiness, bool, string) {
	before, serr := w.status(ctx)
	if serr != nil {
		if ctx.Err() != nil {
			return corpseMoveReadiness{}, false, ""
		}
		w.bossToolkitOff = true
		return corpseMoveReadiness{}, false, fmt.Sprintf("boss %d: the toolkit corpse read failed (point_far, and the status read before the move to the boss row failed: %v): the frames alone decide from here", boss, serr)
	}
	if before.Map != w.plan.Map {
		w.bossToolkitOff = true
		return corpseMoveReadiness{}, false, fmt.Sprintf("boss %d: the toolkit corpse read failed (point_far, and the commander is not on map %d): the frames alone decide from here", boss, w.plan.Map)
	}
	if w.place != nil {
		inst, terr := w.ttInst(ctx, "the corpse read's copy check")
		if terr != nil {
			if ctx.Err() != nil {
				return corpseMoveReadiness{}, false, ""
			}
			w.bossToolkitOff = true
			return corpseMoveReadiness{}, false, fmt.Sprintf("boss %d: the toolkit corpse read failed (point_far, and the instance check before the move to the boss row failed: %v): the frames alone decide from here", boss, terr)
		}
		if inst != w.placeInst {
			w.bossToolkitOff = true
			return corpseMoveReadiness{}, false, fmt.Sprintf("boss %d: the toolkit corpse read failed (point_far, and the commander is in instance %.0f, not the raid's %.0f): the frames alone decide from here", boss, inst, w.placeInst)
		}
	}
	return corpseMoveReadiness{from: before.Map, seq: before.TeleportSeq}, true, ""
}

// bossSkipStep sends the pause's `.bot run skip` for the boss step whose kill the frames show. The commander RPC
// can be busy, so a failed send is retried once; after that the pause path takes over (the walk waits out
// pausedWait and ends 18, as before). It returns whether the skip went out.
func (w *walk) bossSkipStep(ctx context.Context, bossEntry uint32, ev string, step int, now time.Time) bool {
	for try := range 2 {
		rep, err := w.commander(ctx, commanderReq{Op: "run", Verb: "skip", WaitMS: 5000})
		var why string
		switch {
		case err != nil:
			why = err.Error()
		case rep.Reply.Err != nil:
			why = rep.Reply.Err.Message
		default:
			t := w.bossTrackAt(step, bossEntry)
			t.skipped = true
			t.evidence = ev
			w.note("boss %d killed (%s); the run leader did not find his corpse: .bot run skip of step %d", bossEntry, ev, step)
			w.logLine(now, fmt.Sprintf("boss %d killed (%s); the run leader did not find his corpse: .bot run skip of step %d", bossEntry, ev, step))
			return true
		}
		if try == 0 {
			w.note("boss %d killed (%s), but the run skip failed (%s): retrying once", bossEntry, ev, why)
		} else {
			w.note("boss %d killed (%s), but the run skip failed (%s): waiting as a pause", bossEntry, ev, why)
		}
	}
	return false
}

// bossGongReruns bounds how many times one walk restarts its run at a leg's summoning open row. A boss that despawns after a
// wipe and comes back with the object (Zul'Gurub's Arlokk and the gong 180526) needs one per lost boss; the bound keeps a boss
// that never comes back from turning the pause into a restart loop. Past it the pause ends the walk as before.
const bossGongReruns = 3

// stopAfterRerun sends the cleanup `.bot run stop` of a re-run whose `run start` may have been accepted: the start's own RPC can time
// out with the run live (the server runs the plan whether or not the reply arrives), and no status line, a malformed one or a wrong
// one says nothing about what the server did. The stop has its own context, so an interrupted walk still stops the run; when the stop
// itself fails, runStopped stays false and the walk's own cleanup sends another.
func (w *walk) stopAfterRerun() {
	callCtx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	if err := w.gm(callCtx, ".bot run stop"); err != nil {
		w.note("the run stop after the re-run: %v (the walk's cleanup sends another)", err)
		return
	}
	w.runStopped = true
}

// runStopConfirmWait bounds the re-run's wait for the run's own status to show it stopped. The `.bot run stop` returning is its
// chat line reaching the commander, not the server stopping the run: a refused, dropped or late stop leaves the old run live.
const runStopConfirmWait = 15 * time.Second

// lastRunLine is the newest TBM:RUN status line of a reply's lines.
func lastRunLine(lines []string) (tbm.RunStatus, string, bool) {
	for i := len(lines) - 1; i >= 0; i-- {
		if st, ok := raidwalk.ParseRun(lines[i]); ok {
			return st, lines[i], true
		}
	}
	return tbm.RunStatus{}, "", false
}

// waitRunStopped polls `.bot run status` until it answers with no active run: state none, or a done line of this walk's plan (the
// run's own stop). A failed ask, an answer without a run line, and a status still running or paused are retried until timeout,
// which returns the last answer for the note. The question is put to the server through the commander RPC, so the answer is the
// run's own status, not the push feed's newest line.
func (w *walk) waitRunStopped(ctx context.Context, timeout time.Duration) (bool, string) {
	deadline := w.env.now().Add(timeout)
	why := "the run status was never read"
	for {
		if err := ctx.Err(); err != nil {
			return false, "interrupted"
		}
		remaining := deadline.Sub(w.env.now())
		if remaining <= 0 {
			return false, why
		}
		callCtx, cancel := context.WithTimeout(ctx, remaining)
		rep, err := w.commander(callCtx, commanderReq{Op: "run", Verb: "status", WaitMS: 5000})
		cancel()
		switch {
		case err != nil:
			why = "run status: " + err.Error()
		case rep.Reply.Err != nil:
			why = "run status: " + rep.Reply.Err.Message
		default:
			st, line, ok := lastRunLine(rep.Reply.Lines)
			switch {
			case !ok:
				why = "the run status answer has no TBM:RUN line"
			case st.State == tbm.RunNone, st.State == tbm.RunDone && (st.Plan == "" || st.Plan == w.plan.Name):
				w.logLine(w.env.now(), line)
				return true, ""
			case st.State == tbm.RunDone:
				why = fmt.Sprintf("the run is done, but for %s: %s", st.Plan, line)
			default:
				why = fmt.Sprintf("the run is still %s at step %d/%d: %s", st.State, st.Step, st.Steps, line)
			}
		}
		if err := w.env.sleep(ctx, time.Second); err != nil {
			return false, "interrupted"
		}
	}
}

// rerunOpenStep restarts the walk's run at the leg's open row (raidwalk.SummonOpenStep): `.bot run stop`, then, once the run's own
// status shows it stopped, `.bot run start <plan> from step <N>` with the walk's own until and speed, so the run leader walks back
// to the object, opens it (the gong) and meets the boss it summons. A stop that its own status does not confirm is never followed
// by a start. The new run's own status line is adopted and checked (running at the open row, the walk's row count, the until
// label), as a placed walk checks its start.
//
// Every path that ends the walk here ends it with the pause path's exit code (18, restartable), never 0: the walk has stopped its
// own run, so the tracker's own "stopped" ending would otherwise report a clean end while the raid is down (walk 37 b: the walk's
// stop of its own failed re-run's run made the run end 0, and walk-super restarts only 18/19). A failed or refused stop, a stop its
// status does not confirm, and a start that does not check out are all noted and end the walk restartable; walk-super re-places it.
//
// From the moment `run start` goes out the run counts as possibly live (runStopped is cleared), so every failure after it stops the
// run again (stopAfterRerun) and the walk's own cleanup is not skipped by a stop that no longer holds.
func (w *walk) rerunOpenStep(ctx context.Context, openStep int, bossEntry uint32, bossStep int, ev string, now time.Time) error {
	line := fmt.Sprintf("boss %d not found (%s): re-running step %d (the open summons the boss) with .bot run stop and .bot run start from step %d", bossEntry, ev, openStep, openStep)
	w.note("%s", line)
	w.logLine(now, line)
	// The stop is sent here, not through stopRun: its send has to be seen, since a second run start over a live run would leave
	// two run leaders on the party. `.bot run stop` alone is what the allow-list accepts.
	if !w.runStarted || w.runStopped {
		line := fmt.Sprintf("boss %d not found: no run to stop before the re-run at step %d", bossEntry, openStep)
		w.note("%s", line)
		return stopf(exitRaidWalkFailed, "%s", line)
	}
	if err := w.gm(ctx, ".bot run stop"); err != nil {
		line := fmt.Sprintf("boss %d not found: the run stop before the re-run failed (%v)", bossEntry, err)
		w.note("%s", line)
		return stopf(exitRaidWalkFailed, "%s", line)
	}
	// The stop's send is not the run stopping: a refused, dropped or late stop leaves the old run live, and a start over it would
	// leave two run leaders on the party. Only the run's own status decides, so the start waits for it.
	stopped, stopWhy := w.waitRunStopped(ctx, runStopConfirmWait)
	if !stopped {
		if ctx.Err() != nil {
			return nil // interrupted: the phase loop's own ctx check ends the walk; runStopped stays false, so the cleanup stops the run
		}
		w.note("boss %d not found: the run stop before the re-run is not confirmed (%s)", bossEntry, stopWhy)
		w.logLine(w.env.now(), fmt.Sprintf("boss %d not found: the run stop before the re-run is not confirmed (%s)", bossEntry, stopWhy))
		return stopf(exitRaidWalkFailed, "boss %d not found: the run stop before the re-run is not confirmed (%s)", bossEntry, stopWhy)
	}
	w.runStopped = true
	// The stopped run's own ending ("no run", or "done ... stopped by .bot run stop" at the paused step) reaches the feed after
	// this start goes out; it is no end of the walk (walk 37 b). The new run's own running line ends the drop.
	w.rerunStale, w.rerunStaleStep, w.rerunStaleUntil = true, bossStep, w.env.now().Add(time.Minute)
	if err := w.env.sleep(ctx, 2*time.Second); err != nil {
		return nil // interrupted: the phase loop's own ctx check ends the walk; the run is stopped
	}
	args := []string{w.plan.Name, "from", "step", strconv.Itoa(openStep)}
	args = append(args, w.untilWords...)
	if w.opts.speed != 1.0 {
		args = append(args, fmt.Sprintf("speed=%.1f", w.opts.speed))
	}
	// The start is sent from here: whatever its reply says, the server may have the plan running (a timed-out RPC is the clear case).
	w.runStopped = false
	rep, err := w.commander(ctx, commanderReq{Op: "run", Verb: "start", Args: args, WaitMS: 10000})
	var why string
	switch {
	case err != nil:
		why = err.Error()
	case rep.Reply.Err != nil:
		why = rep.Reply.Err.Message
	case !rep.Reply.OK && rep.Reply.Kind != tbm.KindAck:
		why = "no ACK (" + rep.Reply.Line + ")"
	}
	var st tbm.RunStatus
	var startLine string
	if why == "" {
		startLine, st, why = w.rerunStartLine(rep.Reply.Lines, openStep)
		if why == "" && startLine == "" {
			// The reply carried no running line of the new run (the old run's final line can land in it: its stop reaches the
			// server after the start goes out, walk 37 b), so wait for the new run's own status.
			startLine, st, why, err = w.waitRerunStart(ctx, openStep)
			if err != nil {
				w.stopAfterRerun()
				return err
			}
			if startLine != "" {
				// The wait read the feed up to the new run's own line: the stopped run's ending is behind it.
				w.rerunStale = false
			}
		}
		if why == "" && startLine == "" {
			why = fmt.Sprintf("no TBM:RUN status for the new run: no running line at step %d in the reply or the next 15 s", openStep)
		}
	}
	switch {
	case why == "" && st.Steps != len(w.rows.Steps):
		why = fmt.Sprintf("it started at step %d of %d, not step %d of the walk's %d rows", st.Step, st.Steps, openStep, len(w.rows.Steps))
	case why == "" && len(w.untilWords) > 0 && !strings.Contains(raidwalk.CleanLine(startLine), w.untilLabel+" (step "):
		why = "a run without the walk's until (" + w.untilLabel + ")"
	}
	if why != "" {
		w.note("boss %d not found: the re-run at step %d failed (%s)", bossEntry, openStep, why)
		w.logLine(now, fmt.Sprintf("boss %d not found: the re-run at step %d failed (%s)", bossEntry, openStep, why))
		w.stopAfterRerun()
		return stopf(exitRaidWalkFailed, "boss %d not found: the re-run at step %d failed (%s)", bossEntry, openStep, why)
	}
	w.adoptRunStatus(st, startLine)
	w.runStarted, w.runStopped = true, false
	w.gongRerunAt, w.gongRerunBoss, w.gongRerunOpen = w.env.now(), bossEntry, openStep
	w.note("the run restarted at step %d (leg %s) for boss %d", openStep, raidwalk.PlaceLegName(w.rows, openStep), bossEntry)
	w.logLine(w.env.now(), fmt.Sprintf("the run restarted at step %d for boss %d (its row is step %d)", openStep, bossEntry, bossStep))
	return nil
}

// rerunStartLine reads the re-run's own start line from a reply: a TBM:RUN line of the plan that is running at openStep. The
// reply's lines can also carry the old run's final status (its `.bot run stop` reaches the server after the start goes out, walk
// 37 b), and a done or none line says nothing about the new run, so those are skipped. A running line at another step is the new
// run at the wrong place and comes back as why, as does a line with the plan's prefix that is no run line. An empty line with an
// empty why means the reply has no line of the new run yet.
func (w *walk) rerunStartLine(lines []string, openStep int) (string, tbm.RunStatus, string) {
	var wrongLine string
	var wrong tbm.RunStatus
	for _, l := range lines {
		clean := raidwalk.CleanLine(l)
		if !strings.HasPrefix(clean, "TBM:RUN|"+w.plan.Name+"|") {
			continue
		}
		st, ok := raidwalk.ParseRun(clean)
		if !ok {
			return "", tbm.RunStatus{}, "its status line is not a run line: " + l
		}
		if st.State != tbm.RunRunning {
			continue // the old run's ending, not the new run's start
		}
		if st.Step == openStep {
			return l, st, ""
		}
		if wrongLine == "" {
			wrongLine, wrong = l, st
		}
	}
	if wrongLine != "" {
		return "", wrong, fmt.Sprintf("it started at step %d of %d, not step %d of the walk's %d rows", wrong.Step, wrong.Steps, openStep, len(w.rows.Steps))
	}
	return "", tbm.RunStatus{}, ""
}

// waitRerunStart waits up to 15 s for the new run's own status: a running TBM:RUN line of the plan (a running line at another
// step or a malformed one ends the wait with that why). The old run's done and none lines are logged and skipped.
func (w *walk) waitRerunStart(ctx context.Context, openStep int) (string, tbm.RunStatus, string, error) {
	line, ok, err := w.waitTBMMatch(ctx, 15*time.Second, func(l string) bool {
		clean := raidwalk.CleanLine(l)
		if !strings.HasPrefix(clean, "TBM:RUN|"+w.plan.Name+"|") {
			return false
		}
		st, ok := raidwalk.ParseRun(clean)
		return !ok || st.State == tbm.RunRunning
	})
	if err != nil || !ok {
		return "", tbm.RunStatus{}, "", err
	}
	startLine, st, why := w.rerunStartLine([]string{line}, openStep)
	return startLine, st, why, nil
}

// bossSeenAliveSince says the frames showed a unit of the boss's family alive at or after t (w.hostiles keeps each unit's last
// sighting). It tells a gong that summoned the boss again from one the instance refused: no live sighting after the re-run means
// the object did nothing.
func (w *walk) bossSeenAliveSince(boss uint32, t time.Time) bool {
	units := w.bossUnits(boss)
	for _, h := range w.hostiles {
		if h.hppct > 0 && !h.at.Before(t) && inEntries(units, h.entry) {
			return true
		}
	}
	return false
}

// bossSpot is the plan boss row's position: the spot the run leader's own boss step searches around (30 yd) and
// the point the toolkit's corpse read is made at.
func (w *walk) bossSpot(boss uint32) (dungeon.Point, bool) {
	for _, s := range w.rows.Steps {
		if s.Kind == planexport.KindBoss && s.Entry == boss {
			return dungeon.Point{X: s.X, Y: s.Y, Z: s.Z}, true
		}
	}
	return dungeon.Point{}, false
}

// bossFamily is the plan's entries that belong to a boss: its own entry, its phase entries and its hold entries
// (plan.Legs, the leg whose Boss.Entry is boss). The `first` adds are NOT part of it here: runFightsBoss adds them
// for the early rule (they can wake the encounter early), while bossKilled reads bossUnits instead (the boss's own
// entry and its phase entries: a hold add's or a first add's corpse is no boss kill). No such leg -> just {boss}.
// Looked up by the boss entry, not by a StepRow.Leg index, so a 0/1-based mix-up cannot bite.
func (w *walk) bossFamily(boss uint32) map[uint32]bool {
	fam := map[uint32]bool{boss: true}
	if w.plan == nil {
		return fam
	}
	for i := range w.plan.Legs {
		l := &w.plan.Legs[i]
		if l.Boss == nil || l.Boss.Entry != boss {
			continue
		}
		for _, e := range l.Boss.PhaseEntries {
			fam[e] = true
		}
		if l.Boss.Hold != nil {
			for _, e := range l.Boss.Hold.Entries {
				fam[e] = true
			}
		}
		return fam
	}
	return fam
}

// bossFamilyWithFirst is bossFamily plus the boss's `first` adds: the entries that count as the
// encounter being pulled (a first add waking the boss is the encounter starting).
func (w *walk) bossFamilyWithFirst(boss uint32) map[uint32]bool {
	fam := w.bossFamily(boss)
	if w.plan == nil {
		return fam
	}
	for i := range w.plan.Legs {
		l := &w.plan.Legs[i]
		if l.Boss != nil && l.Boss.Entry == boss {
			for _, e := range l.Boss.First {
				fam[e] = true
			}
			break
		}
	}
	return fam
}
const (
	// earlyEngageSteps is how many steps before its boss step the run may stand for the hand-over: the boss's own
	// approach point sits inside the pull, one or two steps back (walk 24).
	earlyEngageSteps = 2
	// earlyEngageYd is how close the main tank must stand to the boss row for the hand-over: the party has to be
	// at the pull, not merely on a nearby step.
	earlyEngageYd = 60
	// earlyEngageFresh is how old the main tank's frame may be for the hand-over: a stale position says nothing.
	earlyEngageFresh = 10 * time.Second
	// pausedBossResumeWait is how long the walk waits, after the ACK of its own `.bot run resume`, for a FRESH
	// RunRunning status of the boss row before it trusts the resume: an ACK only proves the command arrived, and
	// the run can stay paused or pause again at once on the same pullback failure. No named constant of this
	// meaning exists (resumeRun waits 60 s for the recovery's resume; the other 10 s names are frame, corpse and
	// freshness windows), so this is its own.
	pausedBossResumeWait = 10 * time.Second
)

// pausedOnBossOwnRow says the run is paused on the engaged boss's own row: the step of the run's last status is a
// boss row of the rows the walk reads, and the engaged entry is that row's boss (its own entry or a phase/hold form
// of it, never a `first` add). Walk 41 tj-8: the run paused at step 50 "cmd pullback ... refused ... no safe stand
// 35.0-25.0 yd from it" on Jeklik's own row.
func (w *walk) pausedOnBossOwnRow(entry uint32) bool {
	if w.lastStatus.Steps != len(w.rows.Steps) {
		return false // rows not the server's: step numbers mean nothing
	}
	row, ok := w.bossRowAt(w.lastStatus.Step)
	if !ok {
		return false
	}
	return w.bossFamily(row.Entry)[entry]
}

// pausedBossResumeWaitResult is what the resume's wait drained while it polled: the tracker's
// incidents in drain order (a wipe, a stall), and whether a FRESH RunRunning status of the boss
// row arrived. The wait cannot act on a wipe or a stall itself (it returns bool, not error), so
// it hands every drained incident to its caller, which feeds them to the normal handler exactly
// as phaseWatch would, before deciding the handoff. Any wipe or stall during the wait is a failed
// handoff, and its normal handler runs.
type pausedBossResumeWaitResult struct {
	running bool
	incs    []raidwalk.Incident
}

// resumePausedBossRun sends `.bot run resume` for a boss that engaged while the run paused on the boss's own row,
// then waits on the status poll for a FRESH RunRunning status of the boss row: an ACK only proves the command
// arrived, and the run can stay paused or pause again at once on the same pullback failure. True only when that
// fresh running line arrives within pausedBossResumeWait; a refused or dropped resume, a re-pause inside the wait,
// or no running line by the deadline keeps the old ending (the caller's handleBoss stops the run: the boss-engaged
// incident is one-shot, so without this the engaged boss would be left with no run-leader control). The boss row's
// step is the key, so the same row never sends a second resume however many ticks or pauses see it.
func (w *walk) resumePausedBossRun(ctx context.Context, entry uint32) (bool, pausedBossResumeWaitResult) {
	if w.agent == nil {
		return false, pausedBossResumeWaitResult{}
	}
	step := w.lastStatus.Step
	key := strconv.Itoa(step)
	if w.pausedBossResume == key {
		return false, pausedBossResumeWaitResult{} // this row already tried its resume: no retry, the boss path owns it
	}
	rep, err := w.commander(ctx, commanderReq{Op: "run", Verb: "resume", WaitMS: 5000})
	if err != nil {
		w.note("boss %d engaged at step %d while the run is paused on its boss row: the run resume failed (%v)", entry, step, err)
		return false, pausedBossResumeWaitResult{}
	}
	if rep.Reply.Err != nil || (!rep.Reply.OK && rep.Reply.Kind != tbm.KindAck) {
		line := rep.Reply.Line
		if rep.Reply.Err != nil {
			line = rep.Reply.Err.Message
		}
		w.note("boss %d engaged at step %d while the run is paused on its boss row: the run resume was refused (%s)", entry, step, line)
		return false, pausedBossResumeWaitResult{}
	}
	w.pausedBossResume = key
	w.logLine(w.env.now(), "> .bot run resume")
	seq0 := w.statusSeq
	deadline := w.env.now().Add(pausedBossResumeWait)
	var drained []raidwalk.Incident
	for {
		if w.leaseLost.Load() {
			return false, pausedBossResumeWaitResult{incs: drained}
		}
		if err := ctx.Err(); err != nil {
			return false, pausedBossResumeWaitResult{incs: drained}
		}
		incs, err := w.pollOnce(ctx, false)
		if err != nil {
			return false, pausedBossResumeWaitResult{incs: drained}
		}
		drained = append(drained, incs...)
		if w.statusSeq > seq0 && w.haveStatus && w.lastStatus.State == tbm.RunPaused {
			w.note("boss %d engaged at step %d while the run is paused on its boss row: the run paused again after the resume, so the walk fights it", entry, step)
			return false, pausedBossResumeWaitResult{incs: drained} // re-paused inside the wait: at once through handleBoss, no retry for this row
		}
		if w.haveStatus && w.lastStatus.State == tbm.RunRunning && w.lastStatus.Plan == w.plan.Name && w.lastStatus.Step == step && w.statusSeq > seq0 {
			// The handoff note fires in the caller, after it feeds the drained incidents to the
			// normal handler: a wipe or stall in the same wait fails the handoff, and its handler
			// runs without a stale handoff note claiming the run leader fights it.
			return true, pausedBossResumeWaitResult{running: true, incs: drained}
		}
		if !w.env.now().Before(deadline) {
			w.note("boss %d engaged at step %d while the run is paused on its boss row: the run is not running %s after the resume, so the walk fights it", entry, step, durWords(pausedBossResumeWait))
			return false, pausedBossResumeWaitResult{incs: drained}
		}
		if err := w.env.sleep(ctx, 2*time.Second); err != nil {
			return false, pausedBossResumeWaitResult{incs: drained}
		}
	}
}

// runFightsBoss says whether the run leader itself fights the engaged boss: true when the run's
// own status kind is boss and its until goes past that step (walk 12: Jeklik engaged at step 40
// with --until "step 41", and raid-walk stopped the run from under the fight); or, for the walk-24
// early rule, when the run is on a step BEFORE its next boss step in the SAME leg, its until goes
// past that boss step, and the engaged entry belongs to that boss (its entry, phase entries,
// first adds or hold entries): plans whose approach point sits inside the pull (Thekal's
// boss.approach[0] 19.9 yd from him, walk 24) would otherwise stop the run under the fight.
//
// early is the boss step the early rule fired for (0: the status kind was boss). False when
// --until boss (a trash walk that wakes a boss never fights one), when the run is paused anywhere
// but on that boss's own row, when the rows are not the server's (step numbers mean nothing), when the
// next boss step is in another leg, when the until does not go past the boss step, when the run stands
// more than earlyEngageSteps steps before that boss step (not under boss-done, which never stops on its
// own boss's engage), or when the main tank is not within earlyEngageYd of the boss row (review round 1,
// item 3). --until none is true from the first check: a full clear never stops the run on a boss.
//
// A run PAUSED on that boss's own row is the walk-41 case (tj-6/7/8, mj-4..8): the run leader paused there
// because its pullback had no firing position, the tank's own pull went out seconds later and woke the boss
// under the pause. With resumePaused the walk resumes the run (`.bot run resume`) and, once a FRESH RunRunning
// status of the boss row arrives within pausedBossResumeWait, the run leader fights it; with the old
// `.bot run stop` the raid killed Jeklik without the run, the summary said not killed and the autopilot relaunched
// walks for a dead boss. An ACK alone is no handoff: a run that stays paused or pauses again inside the wait goes
// through handleBoss at once (Codex HIGH). Any wipe or stall the wait drained runs its normal handler first: a
// recovered wipe returns the walk to its watch loop (the drained boss incident is stale), any other wipe failure
// ends the walk there, a stall ends it stalled. Dead raid must not be resumed, and a recovery sends its own resume.
func (w *walk) runFightsBoss(ctx context.Context, entry uint32, resumePaused bool) (ok bool, early int, waitOut pausedBossResumeWaitResult) {
	if w.opts.until == "boss" {
		return false, 0, pausedBossResumeWaitResult{}
	}
	// --until none walks the plan to its end: no boss or until rule may stop the run. The run leader fights every
	// boss it meets, however many steps before the boss row the engage came from (walk 31: Mandokir woke five steps
	// before his row, and the auto-shortened until made the walk send `.bot run stop` ~10 s into the fight).
	if strings.TrimSpace(w.opts.until) == "none" {
		return true, 0, pausedBossResumeWaitResult{}
	}
	if !w.haveStatus {
		return false, 0, pausedBossResumeWaitResult{}
	}
	if w.lastStatus.State == tbm.RunPaused {
		if resumePaused && w.pausedOnBossOwnRow(entry) {
			ok, waitOut = w.resumePausedBossRun(ctx, entry)
			if ok {
				return true, 0, waitOut
			}
			return false, 0, waitOut
		}
		return false, 0, pausedBossResumeWaitResult{}
	}
	if w.lastStatus.State != tbm.RunRunning {
		return false, 0, pausedBossResumeWaitResult{}
	}
	if w.lastStatus.Kind == "boss" {
		return true, 0, pausedBossResumeWaitResult{}
	}
	if w.lastStatus.Steps != len(w.rows.Steps) {
		return false, 0, pausedBossResumeWaitResult{} // rows not the server's: step numbers mean nothing
	}
	b := nextBossStep(w.rows, w.lastStatus.Step)
	if b == 0 {
		return false, 0, pausedBossResumeWaitResult{}
	}
	switch {
	case w.untilStep == 0 && len(w.untilWords) == 0:
		// --until none: the run walks to the plan's end, past every boss step.
	case w.untilStep > b:
		// the until goes past the boss step (the server stops BEFORE step N, so N must be > b)
	default:
		return false, 0, pausedBossResumeWaitResult{}
	}
	if w.lastStatus.Step < 1 || w.lastStatus.Step > len(w.rows.Steps) {
		return false, 0, pausedBossResumeWaitResult{}
	}
	if w.rows.Steps[w.lastStatus.Step-1].Leg != w.rows.Steps[b-1].Leg {
		return false, 0, pausedBossResumeWaitResult{} // another leg's boss woken from this step still stops
	}
	if !w.bossFamilyWithFirst(w.rows.Steps[b-1].Entry)[entry] {
		return false, 0, pausedBossResumeWaitResult{}
	}
	// The hand-over is for a boss pulled from the raid's own approach: further back than earlyEngageSteps the
	// boss is woken by something else (another leg's pull, a patrol), and a main tank far from the boss row means
	// the party is not standing at the pull at all (review round 1, item 3: walk 24's Thekal approach point sits
	// inside the pull, so the step-back case is the common one). Under boss-done the step window does not apply:
	// boss-done never stops the run on an engage of its own boss (walk 29b/29c: Mandokir woke 5 steps before his
	// boss row when the party pulled the pack beside him). The leg, the family and the main tank's distance still do.
	if b-w.lastStatus.Step > earlyEngageSteps && w.bossDoneStep == 0 {
		return false, 0, pausedBossResumeWaitResult{}
	}
	lead, ok := w.botAt[w.mt]
	if !ok || w.env.now().Sub(lead.at) > earlyEngageFresh {
		return false, 0, pausedBossResumeWaitResult{}
	}
	row := w.rows.Steps[b-1]
	if math.Hypot(float64(lead.x-row.X), float64(lead.y-row.Y)) > earlyEngageYd {
		return false, 0, pausedBossResumeWaitResult{}
	}
	return true, b, pausedBossResumeWaitResult{}
}

// handleBoss stops the run at once and waits for the fight to end: a wipe (then the revive), a kill or a reset.
// It never revives while a boss row has a target.
func (w *walk) handleBoss(ctx context.Context, inc raidwalk.IncidentBossEngaged) error {
	_ = w.stopRun(context.Background(), "")
	w.note("boss %d engaged at step %d", inc.Entry, inc.Step)
	if w.dash != nil {
		w.saveCommanders("boss")
		w.saveFights()
	}
	deadline := w.env.now().Add(15 * time.Minute)
	wipe := false
	for {
		if w.leaseLost.Load() {
			return stopf(exitRaidWalkFailed, "lease lost")
		}
		if err := ctx.Err(); err != nil {
			return stopf(exitRaidWalkFailed, "interrupted")
		}
		incs, err := w.pollOnce(ctx, false)
		if err != nil {
			return err
		}
		for _, i := range incs {
			if _, ok := i.(raidwalk.IncidentWipe); ok {
				wipe = true
			}
		}
		now := w.env.now()
		switch w.bossState(now, inc.Entry) {
		case bossGone:
			return stopf(exitRaidWalkIncident, "boss %d engaged at step %d", inc.Entry, inc.Step)
		case bossReset:
			if !wipe && !w.allDead(now) {
				return stopf(exitRaidWalkIncident, "boss %d engaged at step %d", inc.Entry, inc.Step)
			}
			if err := w.waitReset(ctx, w.opts.resetWait); err != nil {
				return err
			}
			// The reset wait polls the frames and the reset itself lands in them (the wiped attempt's last
			// form, then the boss's original one). Clearing the form history only now keeps those frames out
			// of the retry's phase: clearing first re-added them, and the record showed the failed attempt's
			// changes as the retry's (review round 4, #1453).
			w.resetBossForms()
			return w.revive(ctx, inc.Step)
		}
		if !now.Before(deadline) {
			return stopf(exitRaidWalkIncident, "boss %d engaged at step %d", inc.Entry, inc.Step)
		}
		if err := w.env.sleep(ctx, 2*time.Second); err != nil {
			return stopf(exitRaidWalkFailed, "interrupted")
		}
	}
}

// anyAlive says whether one hire's freshest state (10 s) is alive.
func (w *walk) anyAlive(now time.Time) bool {
	for _, n := range w.hireList() {
		if p, ok := w.botAt[n]; ok && now.Sub(p.at) <= 10*time.Second && !p.dead {
			return true
		}
	}
	return false
}

// allDead says whether every hire's freshest state (10 s) is dead.
func (w *walk) allDead(now time.Time) bool {
	names := w.hireList()
	if len(names) == 0 {
		return false
	}
	for _, n := range names {
		p, ok := w.botAt[n]
		if !ok || now.Sub(p.at) > 10*time.Second || !p.dead {
			return false
		}
	}
	return true
}

// waitReset waits until no hostile with a party target was seen for 20 s and every hostile seen in the last
// 60 s is at 100 % or gone. Critters never count: the frame's own critter flag (hostile kind holding 'k'), so a
// hurt snake cannot hold the reset open.
func (w *walk) waitReset(ctx context.Context, limit time.Duration) error {
	w.resetSince = time.Time{}
	deadline := w.env.now().Add(limit)
	for {
		if w.leaseLost.Load() {
			return stopf(exitRaidWalkFailed, "lease lost")
		}
		if err := ctx.Err(); err != nil {
			return stopf(exitRaidWalkFailed, "interrupted")
		}
		if _, err := w.pollOnce(ctx, false); err != nil {
			return err
		}
		w.tracker.Events()
		now := w.env.now()
		if w.resetReady(now) {
			return nil
		}
		if !now.Before(deadline) {
			return stopf(exitRaidWalkIncident, "not revived: no reset in %s", durWords(limit))
		}
		if err := w.env.sleep(ctx, 2*time.Second); err != nil {
			return stopf(exitRaidWalkFailed, "interrupted")
		}
	}
}

// resetReady is the reset condition, held for 20 s. A critter never counts (turtle-agent#379): the frame's own
// critter flag (kind holding 'k'), which is what the bystander rows of the module carry at any level. The frame
// carries no max health, so the absolute-vs-percent band test lives on the units read instead (raidwalk's critter).
func (w *walk) resetReady(now time.Time) bool {
	clear := true
	for _, h := range w.hostiles {
		if now.Sub(h.at) > 60*time.Second {
			continue
		}
		if resetCritter(h) {
			continue
		}
		if h.target != 0 && w.partyG[h.target] {
			clear = false
			break
		}
		if h.hppct != 100 {
			clear = false
			break
		}
	}
	if !clear {
		w.resetSince = time.Time{}
		return false
	}
	if w.resetSince.IsZero() {
		w.resetSince = now
	}
	return now.Sub(w.resetSince) >= 20*time.Second
}

// resetCritter says the frame row is a critter the reset wait never counts (turtle-agent#379): primarily the
// frame's own critter flag — the hostile kind holding 'k' (tbm.HostileRec Bystander) — whatever its level; a
// level-1-by-level row without the flag is not one here, because the frame carries no max health for the fallback
// band test (that band test lives on the units read, raidwalk's critter).
func resetCritter(h hostileState) bool {
	return strings.Contains(h.kind, "k")
}

// revive is the walk-4 recipe's ending: revive and always end the walk with exit 19.
func (w *walk) revive(ctx context.Context, step int) error {
	// false: this is the boss-wipe ending, with the walk ending here anyway and no run regrouping itself.
	if err := w.reviveHires(ctx, step, time.Time{}, false); err != nil {
		return err
	}
	return stopf(exitRaidWalkIncident, "wipe at step %d", step)
}

// reviveLandingYd is how close (x/y) the commander's own position must come to a revive teleport's target before
// the summons may leave (F4: a `.go xyz` answers at once, the server lands the character a moment later);
// reviveLandingZYd is the z room, because a landing may drop to the floor.
const (
	reviveLandingYd  = 10
	reviveLandingZYd = 30
)

// reviveTeleportWait is how long a revive teleport gets to land on its point.
const reviveTeleportWait = 30 * time.Second

// reviveHires is the walk-4 recipe's revive half, made safe (spec 3.5): the revive point, the quiet check (then the
// entrance once), summon and revive, and a look for bots attacked after the revive. revive ends the walk with it;
// recoverWipe rests and resumes with it. The deadline is the recovery's entry deadline (zero: the old
// unbounded path outside a recovery): every loop turn and sleep checks it, and past it the
// revive fails with the timed-out recovery the caller turns into the original incident's ending.
//
// Every `.go xyz` must have landed (waitRevivePoint) before a summon leaves: waitMap alone saw only the map id,
// which is already right when the agent stands in the instance, so the summons dropped the party at the OLD spot
// (walk 22 test2 s4, Q3). selfRegroup says the run is regrouping by itself: TB then moves the lead and the party
// itself, so the revive sends no summons at all (the lead, living and dead hires alike): every hire is `.revive`d
// where it lies and TB's regroup gathers them.
func (w *walk) reviveHires(ctx context.Context, step int, deadline time.Time, selfRegroup bool) error {
	revive := w.revivePoint()
	if revive == nil {
		return stopf(exitRaidWalkIncident, "not revived: the plan's rows give no revive point")
	}
	p := *revive
	if err := w.gm(ctx, raidwalk.GMVisibleOff); err != nil {
		if ctx.Err() != nil {
			return stopf(exitRaidWalkFailed, "interrupted")
		}
		w.note("revive: %s failed (%v)", raidwalk.GMVisibleOff, err)
	}
	// Hires released to the graveyard stand on another map: nobody to appear at. Their summons resolve to their own
	// bound copy, so the revive keeps the far go and the summons, with a quiet check of the commander's own copy.
	var joined bool
	why := ""
	placed := w.place != nil && w.placeStillJoined(ctx)
	ghosts := !placed && w.appearHire(p) == "" && w.hiresOffMap() > 0
	if placed {
		// A placed walk's commander joined the raid's copy at the placement (phasePlace: `.appear <mt>`, the toolkit's
		// instance read), and placeReviveInstance checks that copy again before the summons; a second appear could not
		// prove a fresh landing when the commander still stands where the first one put it. placeStillJoined above
		// proves the copy still holds: an off-map or other-copy commander takes the appear path instead (walk 24 C2).
		joined = true
	} else if ghosts {
		w.note("revive: no hire on map %d, %d seen on another map (released): no appear, the far go and summons; the quiet check was blind (the commander's own copy)", w.plan.Map, w.hiresOffMap())
	} else {
		var err error
		if joined, why, err = w.joinHires(ctx, p, deadline); err != nil {
			return err
		}
	}
	if !joined && !ghosts {
		// A normal revive quiet-checks and summons from where the commander stands: blind in a copy of its own. A
		// self-regroup summons nobody and `.revive` works by name, so it keeps the far go (reviveLanded's far rule).
		if !selfRegroup {
			return stopf(exitRaidWalkIncident, "not revived: could not join the hires' instance (%s)", why)
		}
		w.note("revive: could not join the hires' instance (%s); self-regroup: the go opens its own copy, the hires are revived by name", why)
	}
	from, err := w.reviveFrom(ctx, joined, selfRegroup, deadline)
	if err != nil {
		return err
	}
	beforeSeq, haveSeq := w.reviveSeq(ctx, deadline)
	if st, serr := w.status(ctx); serr == nil {
		w.logLine(w.env.now(), fmt.Sprintf("revive go from map %d (%.1f %.1f %.1f, z unknown %v, teleports %d)", st.Map, st.X, st.Y, st.Z, st.ZUnknown, st.TeleportSeq))
	}
	if err := w.gm(ctx, fmt.Sprintf(".go xyz %s %s %s %d", num32(p.X), num32(p.Y), num32(p.Z), w.plan.Map)); err != nil {
		return err
	}
	if err := w.waitRevivePoint(ctx, p, from, beforeSeq, haveSeq, deadline); err != nil {
		if timeout, ok := err.(*recoveryTimeout); ok {
			return timeout
		}
		st, _ := w.status(ctx)
		return stopf(exitRaidWalkIncident, "not revived: the commander did not land at the revive point on map %d (last read: map %d %.1f %.1f %.1f, z unknown %v, falling %v, teleports %d -> %d)", w.plan.Map, st.Map, st.X, st.Y, st.Z, st.ZUnknown, st.Falling, beforeSeq, st.TeleportSeq)
	}
	qs := w.newQuietStateFor()
	waitFrom := w.env.now()
	q := w.quietCheck(ctx, qs, p, deadline)
	if q.timeout != nil {
		return q.timeout
	}
	if q.err != nil {
		return q.err
	}
	ok, why, blockers := q.ok, q.why, q.blockers
	// cur is the point the commander stands at: p, a fallback row, or the entrance once its go has LANDED. The D-C
	// wait re-reads that one. entranceTried says the entrance fallback's `.go xyz` went out at all (the refusal's
	// entrance_tried); tried is every point the revive tried, in order (the refusal's tried_steps).
	cur := p
	curStep := w.reviveStep()
	tried := []int{curStep}
	entranceTried, entranceLanded := false, true
	if !ok {
		// The point stays refused for its 120 s check: try the earlier move rows of its own leg, newest first, then
		// the previous leg's last move row. Each is quiet-checked in turn; the first quiet one revives the raid there.
		for _, cand := range w.reviveFallbackRows() {
			if timeout := w.recoveryExpired(deadline); timeout != nil {
				return timeout
			}
			w.note("revive: point %d refused (%s); trying %d", curStep, why, cand.step)
			w.logLine(w.env.now(), fmt.Sprintf("revive: point %d refused (%s); trying %d", curStep, why, cand.step))
			tried = append(tried, cand.step)
			cq, landed, err := w.reviveGoPoint(ctx, cand.point, joined, selfRegroup, deadline)
			if err != nil {
				return err
			}
			if cq.timeout != nil {
				return cq.timeout
			}
			if cq.err != nil {
				return cq.err
			}
			// A point switch needs a fresh quiet state (Codex H2): the old point's patrol distances must not exempt
			// a stationary patrol at the new one whose distance grew only because the point moved.
			cur, curStep, qs = cand.point, cand.step, w.newQuietStateFor()
			if !landed {
				why = fmt.Sprintf("the commander did not land at step %d", cand.step)
				continue
			}
			ok, why, blockers = cq.ok, cq.why, cq.blockers
			if ok {
				break
			}
		}
	}
	if !ok {
		// The instance entrance is the last fallback: the old entrance fallback, and it now also runs for a placed
		// walk whose own point and rows all stayed refused (the 392 run: the placed walk never reached it).
		e := w.plan.Entrance
		w.note("revive: point %d refused (%s); trying the entrance", curStep, why)
		w.logLine(w.env.now(), fmt.Sprintf("revive: point %d refused (%s); trying the entrance", curStep, why))
		if !joined && !ghosts { // only a self-regroup gets here unjoined: one more try, and the far go again when it fails
			var jerr error
			if joined, why, jerr = w.joinHires(ctx, e, deadline); jerr != nil {
				return jerr
			}
			if !joined {
				w.note("revive: could not join the hires' instance at the entrance either (%s)", why)
			}
		}
		from, ferr := w.reviveFrom(ctx, joined, selfRegroup, deadline)
		if ferr != nil {
			return ferr
		}
		eSeq, eHave := w.reviveSeq(ctx, deadline)
		if st, serr := w.status(ctx); serr == nil {
			w.logLine(w.env.now(), fmt.Sprintf("revive go from map %d (%.1f %.1f %.1f, z unknown %v, teleports %d)", st.Map, st.X, st.Y, st.Z, st.ZUnknown, st.TeleportSeq))
		}
		if err := w.gm(ctx, fmt.Sprintf(".go xyz %s %s %s %d", num32(e.X), num32(e.Y), num32(e.Z), w.plan.Map)); err == nil {
			entranceTried = true
			werr := w.waitRevivePoint(ctx, e, from, eSeq, eHave, deadline)
			if timeout, isTimeout := werr.(*recoveryTimeout); isTimeout {
				return timeout
			}
			if werr != nil {
				// The go was accepted but never landed: the commander still stands at the last point, so a quiet read
				// of e would judge a point its own character is nowhere near (quietOnce's own reach rule) and D-C
				// would wait on nothing. Refuse (review round 1, Codex/Opus H1).
				entranceLanded = false
			} else {
				cur, qs, curStep = e, w.newQuietStateFor(), 0
				eq := w.quietCheck(ctx, qs, e, deadline)
				if eq.timeout != nil {
					return eq.timeout
				}
				if eq.err != nil {
					return eq.err
				}
				ok, why, blockers = eq.ok, eq.why, eq.blockers
			}
		}
	}
	if !entranceLanded {
		w.reviveRefusal = w.refusal(step, blockers, w.env.now().Sub(waitFrom), entranceTried, tried...)
		return stopf(exitRaidWalkIncident, "not revived: the entrance go never landed (the commander is not within %d yd of the revive point on map %d)", reviveLandingYd, w.plan.Map)
	}
	if !ok {
		// D-C (walk 25): the point may still go quiet, so re-read every 10 s while the rest and resume that follow still
		// fit before the recovery's deadline. The boss-wipe ending has no deadline and does not wait.
		if end := w.quietWaitEnd(deadline, selfRegroup); !end.IsZero() && w.env.now().Before(end) {
			wq := w.quietWait(ctx, qs, cur, quietRead{why: why, blockers: blockers}, end, deadline)
			if wq.timeout != nil {
				return wq.timeout
			}
			if wq.err != nil {
				return wq.err
			}
			ok, why, blockers = wq.ok, wq.why, wq.blockers
			// The wait is long, so the hires may have been released to the graveyard meanwhile: re-read their maps
			// and take the ghosts path when any is off the plan's map, so the late join is skipped (review round 1,
			// Opus M2).
			if ok && !ghosts && w.hiresOffMap() > 0 {
				ghosts = true
				w.note("revive: %d hire(s) are on another map after the wait (released): no late join, the summons resolve to each hire's own copy", w.hiresOffMap())
			}
		}
	}
	if !ok {
		w.reviveRefusal = w.refusal(step, blockers, w.env.now().Sub(waitFrom), entranceTried, tried...)
		return stopf(exitRaidWalkIncident, "not revived: %s", why)
	}
	if w.place != nil {
		if err := w.placeReviveInstance(ctx); err != nil {
			return err
		}
	}
	// Walk 22 F4 (lead ruling, round 2): during a self-regroup the agent summons NOBODY, not the lead and not a dead
	// hire. The run regroups by itself (TB moves the lead and gathers the party), and any `.summon` from where the
	// agent stands would drag a hire back there (walk 22 test2 s4, Q3: `.summon <lead>` 2 ms after the agent's own
	// `.go xyz` dropped the lead 560 yd back). Every hire is `.revive`d where it lies, below.
	if selfRegroup {
		w.note("self-regroup: no summons, revived in place")
	} else {
		for _, name := range w.hireList() {
			if timeout := w.recoveryExpired(deadline); timeout != nil {
				return timeout
			}
			if err := w.gm(ctx, ".summon "+name); err != nil {
				return err
			}
			if err := w.recoverySleep(ctx, 100*time.Millisecond, deadline); err != nil {
				return err
			}
		}
	}
	// --revive-at-raid: the commander stands on cur from here on, the point the hires are brought back to.
	atRaid := selfRegroup && w.opts.reviveAtRaid
	if w.opts.reviveAtRaid {
		spot := cur
		w.reviveSpot = &spot
	}
	// A self-regroup: TB released the party at its regroup line, and a released bot's graveyard repop fires only once
	// its pending movement changes are acked. A `.revive` inside that window makes the bot alive with the repop still
	// pending, and the repop then far-teleports it out of the fight (CRASH-2026-11-03). So no revive goes out until
	// selfRegroupReviveWait after the release, however long the checks above took, and then only the hires that are
	// still dead are revived.
	revives := w.hireList()
	if selfRegroup {
		if err := w.waitPastRelease(ctx, deadline); err != nil {
			return err
		}
		var err error
		if revives, err = w.stillDeadHires(ctx, deadline); err != nil {
			return err
		}
	} else {
		// Nothing was summoned in a self-regroup, so the 10 s the summons need is dead time while the ghosts run:
		// revive at once (the earlier the revive, the nearer the corpse run's start).
		if !atRaid {
			if err := w.recoverySleep(ctx, 10*time.Second, deadline); err != nil {
				return err
			}
		}
	}
	for _, name := range revives {
		if timeout := w.recoveryExpired(deadline); timeout != nil {
			return timeout
		}
		if err := w.gm(ctx, ".revive "+name); err != nil {
			return err
		}
		if err := w.recoverySleep(ctx, 100*time.Millisecond, deadline); err != nil {
			return err
		}
	}
	if err := w.recoverySleep(ctx, 15*time.Second, deadline); err != nil {
		return err
	}
	if n, frame, err := w.view(ctx, w.afterN, 2000); err == nil && frame != nil {
		w.afterN = n
		now := w.env.now()
		w.trackFrame(now, frame)
		inCombat := 0
		hires := w.hires
		for _, p := range frame.Party {
			if hires[p.Name] && p.InCombat() {
				inCombat++
			}
		}
		if inCombat > 0 {
			w.note("attacked after revive: %d bots in combat", inCombat)
		}
	}
	// An unjoined self-regroup tries the join once more when its revives have a hire up on the plan's map (review
	// round 2, L4): the next recovery then starts from the hires' copy.
	if selfRegroup && !joined && !ghosts && w.hireUpOnMap() {
		ok, why, err := w.joinHires(ctx, p, deadline)
		if err != nil {
			return err
		}
		if ok {
			w.note("revive: joined the hires' instance after the self-regroup revives")
		} else {
			w.note("revive: still could not join the hires' instance after the self-regroup revives (%s)", why)
		}
	}
	// --revive-at-raid: the hires the ghosts' corpse run had carried far away are brought to the point.
	if atRaid {
		return w.reviveBringBack(ctx, cur, deadline)
	}
	return nil
}

// recoverWipe revives the dead raid after a wipe, rests it out of combat and resumes the run instead of ending the
// walk (walk 13 s7: the hand-revived, rested run went on). It returns errWipeRecovered when the run is running
// again: the watch loop keeps going and the summary's end stays truthful. Any failure ends the walk with the
// failure's own reason, exactly as endWipe would. Review round 2: the boss evidence must be fresh, the rest
// needs a complete quiet view, and the whole recovery has an absolute deadline.
func (w *walk) recoverWipe(ctx context.Context, step int, selfRegroup, runLeaderFights bool) error {
	// The boss-unknown gate guards a revival under a boss the WALK owns. When the run leader fights the boss, the walk
	// owns no attempt to be unsure about: the run's own fight carries on after the revive.
	if !runLeaderFights {
		if err := w.bossUnknown(ctx); err != nil {
			return err
		}
	}
	// The recovery ends the boss attempt that wiped: nothing the frames showed before it bounds the next
	// attempt's kill evidence (review round 3). The corpse wait's alive link and first sighting end with
	// it too (revive r6).
	clear(w.bossAttemptAt)
	clear(w.bossAliveSeen)
	clear(w.bossCorpseSeen)
	from := w.env.now()
	deadline := recoveryDeadline(from, w.opts.wipeRest, w.opts.maxRunMinutes, w.started)
	if err := w.reviveHires(ctx, step, deadline, selfRegroup); err != nil {
		if timeout, ok := err.(*recoveryTimeout); ok {
			return timeout.end(step)
		}
		return err
	}
	revived := len(w.hireList())
	// --revive-at-raid: no resume while a hire is far or fighting away from the raid (up to 60 s, then it goes on
	// with a note naming them).
	if selfRegroup {
		if err := w.reviveGather(ctx, deadline); err != nil {
			if timeout, ok := err.(*recoveryTimeout); ok {
				return timeout.end(step)
			}
			return err
		}
	}
	if selfRegroup {
		// The run is running and regroups by itself: it rests, gathers and walks back on its own.
		// No out-of-combat rest and no run resume (walk 17b: the rest never got quiet while the
		// run marched on). The post-revive boss re-check still runs; a boss it names ends the walk.
		//
		// When the run leader fights the boss, the boss gate below has nothing to decide: the live boss is the
		// run's own fight and recoveredBoss hands that encounter back to it (walk 32's boss-step wipes).
		if !runLeaderFights {
			if _, err := w.freshWipeRest(); err != nil {
				return err
			}
		}
		// The retry starts here: every frame polled above (the revive's, the rest's) is the wiped attempt's, so
		// the record's phase counts only the entries the retry's own polls see (spec 6, D5, #1453).
		w.resetBossForms()
		w.resumeStatusSeq = w.statusSeq // recoveredBoss's run-leader exception needs a status after this point
		if err := w.recoveredBoss(ctx, step, runLeaderFights); err != nil {
			return err
		}
		w.wipeResumes++
		w.note("wipe-resume %d/%d at step %d: revived %d; the run regroups by itself (no rest, no resume)", w.wipeResumes, w.opts.wipeResume, step, revived)
		return errWipeRecovered
	}
	rest := w.opts.wipeRest
	if !runLeaderFights {
		var err error
		if rest, err = w.freshWipeRest(); err != nil {
			return err
		}
	}
	seq0 := w.statusSeq
	if err := w.restOutOfCombat(ctx, step, rest, deadline); err != nil {
		if timeout, ok := err.(*recoveryTimeout); ok {
			return timeout.end(step)
		}
		if err == errRunWentOn {
			// The run went on by itself while the walk rested (defence in depth: usually the
			// self-regroup note decided this before the rest). Skip the resume and count it.
			// The retry already began: the rest's polls above are the wiped attempt's frames (spec 6, D5, #1453).
			w.resetBossForms()
			w.resumeStatusSeq = seq0
			if rerr := w.recoveredBoss(ctx, step, runLeaderFights); rerr != nil {
				return rerr
			}
			w.wipeResumes++
			w.note("wipe-resume %d/%d at step %d: revived %d; the run went on by itself during the rest (no resume)", w.wipeResumes, w.opts.wipeResume, step, revived)
			return errWipeRecovered
		}
		return err
	}
	// --revive-at-raid: the resume waits for the raid to gather (the rest's own polls are the same wiped attempt's).
	if err := w.reviveGather(ctx, deadline); err != nil {
		if timeout, ok := err.(*recoveryTimeout); ok {
			return timeout.end(step)
		}
		return err
	}
	// The retry starts here: the revive's and the rest's frames are the wiped attempt's last, so the record's
	// phase counts only the entries the retry's own polls see (spec 6, D5, #1453).
	w.resetBossForms()
	if err := w.resumeRun(ctx, step, from, rest); err != nil {
		if timeout, ok := err.(*recoveryTimeout); ok {
			return timeout.end(step)
		}
		return err
	}
	if err := w.recoveredBoss(ctx, step, runLeaderFights); err != nil {
		return err
	}
	w.wipeResumes++
	w.note("wipe-resume %d/%d at step %d: revived %d, rested %s, resumed", w.wipeResumes, w.opts.wipeResume, step, revived, durWords(w.opts.wipeRest))
	return errWipeRecovered
}

// recoverNearWipe resumes after a near-wipe stop: someone is still alive (or the raid was revived by hand, walk
// 11), so there is no reset wait and no revive — only the out-of-combat rest and the resume. Dead hires found on
// the way are revived with the same recipe; revived counts only the hires this walk revived.
func (w *walk) recoverNearWipe(ctx context.Context, step int) error {
	if err := w.bossUnknown(ctx); err != nil {
		return err
	}
	// As in recoverWipe: the recovery ends the attempt whose frames it woke on.
	clear(w.bossAttemptAt)
	clear(w.bossAliveSeen)
	clear(w.bossCorpseSeen)
	from := w.env.now()
	w.wipeAt = from
	deadline := recoveryDeadline(from, w.opts.wipeRest, w.opts.maxRunMinutes, w.started)
	w.reviveSpot = nil
	revived, err := w.recoverNearWipeRevive(ctx, step, deadline)
	if err != nil {
		if timeout, ok := err.(*recoveryTimeout); ok {
			return timeout.pausedEnd(step)
		}
		return err
	}
	rest, err := w.freshWipeRest()
	if err != nil {
		return err
	}
	seq0 := w.statusSeq
	if err := w.restOutOfCombat(ctx, step, rest, deadline); err != nil {
		if timeout, ok := err.(*recoveryTimeout); ok {
			return timeout.pausedEnd(step)
		}
		if err == errRunWentOn {
			// The run went on by itself while the walk rested (a hand resume, or a regroup the
			// status line reported): skip the resume and count it.
			// The rest's polls above are the wiped attempt's frames, the retry's follow (spec 6, D5, #1453).
			w.resetBossForms()
			w.resumeStatusSeq = seq0
			if rerr := w.recoveredBoss(ctx, step, false); rerr != nil {
				return rerr
			}
			w.wipeResumes++
			w.note("wipe-resume %d/%d at step %d: revived %d; the run went on by itself during the rest (no resume)", w.wipeResumes, w.opts.wipeResume, step, revived)
			return errWipeRecovered
		}
		return err
	}
	// --revive-at-raid: no resume while a hire is far or fighting away from the raid.
	if err := w.reviveGather(ctx, deadline); err != nil {
		if timeout, ok := err.(*recoveryTimeout); ok {
			return timeout.pausedEnd(step)
		}
		return err
	}
	// As in recoverWipe: the revive's and the rest's frames are the attempt's last, the retry's follow.
	w.resetBossForms()
	if err := w.resumeRun(ctx, step, from, rest); err != nil {
		if timeout, ok := err.(*recoveryTimeout); ok {
			return timeout.pausedEnd(step)
		}
		return err
	}
	if err := w.recoveredBoss(ctx, step, false); err != nil {
		return err
	}
	w.wipeResumes++
	w.note("wipe-resume %d/%d at step %d: revived %d, rested %s, resumed", w.wipeResumes, w.opts.wipeResume, step, revived, durWords(w.opts.wipeRest))
	return errWipeRecovered
}

// recoverNearWipeRevive revives what the near-wipe path revives: every hire when all are dead,
// else only the dead hires of a complete fresh view (review 2: no complete view within 60 s is
// no recovery, the old ending with the skipped note). It returns the revived count. The
// deadline is the recovery's entry deadline: every loop turn and sleep checks it.
func (w *walk) recoverNearWipeRevive(ctx context.Context, step int, deadline time.Time) (int, error) {
	if w.allDead(w.env.now()) {
		// false: a near-wipe stop is a PAUSED run (never a self-regroup), so the old summon recipe stands.
		if err := w.reviveHires(ctx, step, deadline, false); err != nil {
			return 0, err
		}
		return len(w.hireList()), nil
	}
	if timeout := w.recoveryExpired(deadline); timeout != nil {
		return 0, timeout
	}
	dead, ok := w.completeDeadView(ctx)
	if !ok {
		w.note("wipe resume skipped: no complete view of the hires")
		return 0, errWipeResumeSkipped
	}
	for _, name := range dead {
		if timeout := w.recoveryExpired(deadline); timeout != nil {
			return 0, timeout
		}
		if err := w.gm(ctx, ".revive "+name); err != nil {
			return 0, err
		}
		if err := w.recoverySleep(ctx, 100*time.Millisecond, deadline); err != nil {
			return 0, err
		}
	}
	// --revive-at-raid: the corpses may lie far from the raid; bring those hires to the living ones.
	if err := w.reviveBringBackNear(ctx, dead, deadline); err != nil {
		return 0, err
	}
	return len(dead), nil
}

// freshWipeRest refreshes the dashboard boss evidence at recovery time (review 1: the gate reads
// FRESH evidence) and returns the rest length. A failed or missing read is unknown boss state:
// no recovery, like before K (endWipe for a wipe, the old paused ending for a near-wipe, told
// apart by errWipeResumeSkipped).
func (w *walk) freshWipeRest() (time.Duration, error) {
	if w.dash == nil {
		now := w.env.now()
		if w.bossEngagedNow() {
			w.note("wipe resume skipped: boss state unknown (after the revive: %s engaged in the frames)", w.engagedBossWords())
			return 0, errWipeResumeSkipped
		}
		if !w.bossLatchAt.IsZero() && now.Sub(w.bossLatchAt) < 120*time.Second {
			if kill, _, words := w.wipeBossFrame(now); kill {
				w.note("boss %s killed (frames show 0 %%, latched %ds ago): advancing as a kill", words, int(now.Sub(w.bossLatchAt)/time.Second))
				return w.opts.wipeRest, nil
			}
			w.note("wipe resume skipped: boss state unknown (after the revive: %s)", w.latchWords(now))
			return 0, errWipeResumeSkipped
		}
		return w.opts.wipeRest, nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	raw, err := w.dash.Fights(ctx, w.manifest.Character)
	now := w.env.now()
	if err != nil {
		w.bossCheckErr = err
		w.note("wipe resume skipped: boss state unknown (after the revive: dashboard fights read failed: %v)", err)
		return 0, errWipeResumeSkipped
	}
	var rows []walkFightRow
	if jerr := json.Unmarshal(raw, &rows); jerr != nil {
		w.bossCheckErr = jerr
		w.note("wipe resume skipped: boss state unknown (after the revive: dashboard fights unreadable: %v)", jerr)
		return 0, errWipeResumeSkipped
	}
	w.bossCheckErr = nil
	w.bossReadAt = now
	w.fightRows = rows
	_ = w.writeOut("combat-fights.json", raw)
	w.bossEngagedAt = time.Time{}
	for _, r := range rows {
		if r.bossLive() {
			w.bossEngagedAt = now
			break
		}
	}
	if !w.bossEngagedAt.IsZero() {
		w.note("wipe resume skipped: boss state unknown (after the revive: live boss fight row on the dashboard)")
		return 0, errWipeResumeSkipped
	}
	return w.opts.wipeRest, nil
}

// bossUnknown is the review-1 fail-closed gate: recovery starts only when the boss evidence is
// FRESH — the last successful fights/boss read is <= 30 s old and shows no engaged boss, and
// no boss was latched in the last 120 s. Otherwise the walk ends exactly as before K (endWipe
// for a wipe, the old paused ending for a near-wipe).
func (w *walk) bossUnknown(ctx context.Context) error {
	now := w.env.now()
	if w.bossEngagedNow() {
		w.note("wipe resume skipped: boss state unknown (%s engaged in the frames)", w.engagedBossWords())
		return errWipeResumeSkipped
	}
	// The dashboard first, exactly as before: a live boss fight row blocks the recovery, and a
	// missing, stale or errored read keeps the unknown wait. The frames only resolve the latch
	// once the dashboard does not block, so a kill (or reset) the frames show can never walk a
	// live dashboard fight or an unreadable dashboard into the revive.
	if w.dash != nil {
		switch {
		case w.bossCheckErr != nil:
			w.note("wipe resume skipped: boss state unknown (dashboard fights read failed: %v)", w.bossCheckErr)
			return errWipeResumeSkipped
		case w.bossReadAt.IsZero():
			w.note("wipe resume skipped: boss state unknown (no dashboard fights read yet)")
			return errWipeResumeSkipped
		case now.Sub(w.bossReadAt) > 30*time.Second:
			w.note("wipe resume skipped: boss state unknown (dashboard fights read stale %ds)", int(now.Sub(w.bossReadAt)/time.Second))
			return errWipeResumeSkipped
		}
		// A boss fight still live on the dashboard blocks recovery before anything is revived.
		for _, r := range w.fightRows {
			if r.bossLive() {
				w.note("wipe resume skipped: boss state unknown (live boss fight row on the dashboard)")
				return errWipeResumeSkipped
			}
		}
	}
	if !w.bossLatchAt.IsZero() && now.Sub(w.bossLatchAt) < 120*time.Second {
		if kill, alive, words := w.wipeBossFrame(now); kill || alive {
			what := "killed (frames show 0 %%)"
			if alive {
				what = "alive (frames show the reset: full HP, no target)"
			}
			w.note("boss %s %s, latched %ds ago: advancing, not unknown", words, what, int(now.Sub(w.bossLatchAt)/time.Second))
			return nil
		}
		w.note("wipe resume skipped: boss state unknown (%s)", w.latchWords(now))
		return errWipeResumeSkipped
	}
	return nil
}

// latchWords names the bossLatchAt skip: which boss row latched and how long ago.
func (w *walk) latchWords(now time.Time) string {
	who := "a boss row"
	if w.bossLatchHit != 0 {
		who = w.bossRule.Words(w.bossLatchHit)
	}
	return fmt.Sprintf("%s latched %ds ago, under 120 s", who, int(now.Sub(w.bossLatchAt)/time.Second))
}

// recoveryDeadline is the absolute deadline of a recovery starting at from: the walk's max-run
// ceiling, or start + 3 x --wipe-rest, whichever is sooner. Combat restarts the quiet window
// but never moves it.
func recoveryDeadline(from time.Time, rest time.Duration, maxRunMinutes int, started time.Time) time.Time {
	ceiling := started.Add(time.Duration(maxRunMinutes) * time.Minute)
	byRest := from.Add(3 * rest)
	if byRest.Before(ceiling) {
		return byRest
	}
	return ceiling
}

// recoveryTimeout says the absolute deadline passed before the run was back: the walk ends with
// the ORIGINAL incident's exit (19 for a wipe, the old paused code for a near-wipe).
type recoveryTimeout struct {
	dur    time.Duration
	paused bool
}

func (e *recoveryTimeout) Error() string {
	return fmt.Sprintf("wipe recovery timed out after %s", durWords(e.dur))
}

// end is the wipe path's ending: exit 19 with the timed-out note.
func (e *recoveryTimeout) end(step int) error {
	return &timeoutWalkStop{code: exitRaidWalkIncident, reason: fmt.Sprintf("wipe at step %d", step), note: fmt.Sprintf("wipe recovery timed out after %s", durWords(e.dur))}
}

// pausedEnd is the near-wipe path's ending: the old paused code with the timed-out note.
func (e *recoveryTimeout) pausedEnd(step int) error {
	return &timeoutWalkStop{code: exitRaidWalkFailed, reason: fmt.Sprintf("paused at step %d", step), note: fmt.Sprintf("wipe recovery timed out after %s", durWords(e.dur))}
}

// timeoutWalkStop carries a timeout ending through the recovery: the reason ends the walk and
// the note explains it in the summary.
type timeoutWalkStop struct {
	code   int
	reason string
	note   string
}

func (e *timeoutWalkStop) Error() string { return e.reason }

// errWipeResumeSkipped is not a failure: the boss state is unknown, so the walk ends exactly as
// before K existed (endWipe for a wipe, the old paused ending for a near-wipe).
var errWipeResumeSkipped = errors.New("wipe resume skipped: boss state unknown")

// completeDeadView takes one complete fresh bot_view (review 2: counts only when the call
// succeeded AND every roster hire is present with a row <= 10 s old) and returns the dead
// hires. No complete view means no recovery.
func (w *walk) completeDeadView(ctx context.Context) ([]string, bool) {
	n, frame, err := w.view(ctx, w.afterN, 2000)
	if err != nil || frame == nil {
		return nil, false
	}
	w.afterN = n
	now := w.env.now()
	w.trackFrame(now, frame)
	w.tracker.Frame(now, *frame)
	w.saveFrame(now, frame)
	byName := map[string]tbm.PartyRec{}
	for _, p := range frame.Party {
		byName[p.Name] = p
	}
	at := map[string]time.Time{}
	for name, pos := range w.botAt {
		at[name] = pos.at
	}
	var dead []string
	for _, name := range w.hireList() {
		p, ok := byName[name]
		if !ok {
			return nil, false
		}
		when, ok := at[name]
		if !ok || now.Sub(when) > 10*time.Second {
			return nil, false
		}
		if p.Dead || p.State == "d" {
			dead = append(dead, name)
		}
	}
	return dead, true
}

// recoveredBoss routes a boss engaged during the recovery to the boss path, exactly as the
// watch loop does: the rest and the resume wait drain the tracker's incidents, so without this
// a boss the revived raid woke would never reach handleBoss. The tracker's latch cannot tell a
// new boss from one engaged before the recovery (it fires once for life), so this decides from
// what the frames say now — the same bossEngagedNow the non-recovery path gates on, with the
// row's plan-boss identity from the frames — and reuses the watch loop's handleBoss call,
// including its run-leader-fights-its-boss exception. knownFight says the wipe handler already decided the engaged
// boss is the run leader's own fight (a boss-step wipe under an until that goes past the step): no new status line is
// needed then, because the boss was not newly engaged during the recovery.
func (w *walk) recoveredBoss(ctx context.Context, step int, knownFight bool) error {
	if !w.bossEngagedNow() {
		return nil
	}
	entry, ok := w.engagedBossEntry()
	if !ok {
		// The gate holds on a fresh target whose rows left the frames: name the
		// last plan-boss row seen so the boss path watches it settle.
		entry, ok = w.lastBossRow()
		if !ok {
			return nil
		}
	}
	// Review round 3: the exception needs a status received after the resume
	// went out — pollRunRunning accepts the retained pre-resume line, and a
	// stale boss-kind status must not hide a newly engaged boss. A fresh
	// post-resume boss-kind status keeps the old behaviour: the run leader
	// fights its boss step.
	if knownFight || w.statusSeq > w.resumeStatusSeq {
		if ok, early, _ := w.runFightsBoss(ctx, entry, false); ok {
			switch {
			case strings.TrimSpace(w.opts.until) == "none":
				w.note("boss %d engaged at step %d under --until none: the run leader fights it", entry, step)
			case early > 0:
				w.note("boss %d engaged at step %d, before its boss step %d in the same leg, and the until (%s) goes past it, so the run leader fights it", entry, step, early, w.untilLabel)
			default:
				w.note("boss %d engaged at step %d: the run is on its boss step and its until (%s) goes past it, so the run leader fights it", entry, step, w.untilLabel)
			}
			return nil
		}
	}
	return w.handleBoss(ctx, raidwalk.IncidentBossEngaged{Entry: entry, Step: step, At: w.env.now()})
}

// engagedBossEntry names the engaged plan boss the frames hold now: the freshest row (<= 15 s
// old) with hp left that is not a reset (the same row filters bossEngagedNow gates on), so an
// earlier boss that died, reset or left the frames is never named. Zero when no live row names
// one — the rows-gone-but-target-fresh case the gate still calls engaged falls to lastBossRow.
func (w *walk) engagedBossEntry() (uint32, bool) {
	now := w.env.now()
	var best uint32
	var bestRow hostileState
	bestAt := time.Time{}
	for entry, row := range w.bossRows {
		if !w.bossSet[entry] || now.Sub(row.at) > 15*time.Second {
			continue
		}
		if row.hppct == 0 {
			continue
		}
		if row.target == 0 && row.hppct == 100 && (w.bossTargetAt.IsZero() || now.Sub(w.bossTargetAt) >= 30*time.Second) {
			continue
		}
		if best == 0 || (row.target != 0 && bestRow.target == 0) ||
			(row.target != 0) == (bestRow.target != 0) && (row.at.After(bestAt) ||
				row.at.Equal(bestAt) && entry < best) {
			best, bestRow, bestAt = entry, row, row.at
		}
	}
	if best == 0 {
		return 0, false
	}
	return best, true
}

// lastBossRow names the freshest plan-boss row the frames ever held (rows are never pruned).
func (w *walk) lastBossRow() (uint32, bool) {
	bestAt := time.Time{}
	var best uint32
	for entry, row := range w.bossRows {
		if !w.bossSet[entry] {
			continue
		}
		if best == 0 || row.at.After(bestAt) {
			bestAt, best = row.at, entry
		}
	}
	if best == 0 {
		return 0, false
	}
	return best, true
}

// engagedBossWords names the engaged boss for a skip note: the engaged row the frames hold now,
// falling back to the freshest plan-boss row ever seen (the rows-gone target case), then "a boss".
func (w *walk) engagedBossWords() string {
	if entry, ok := w.engagedBossEntry(); ok {
		return w.bossRule.Words(entry)
	}
	if entry, ok := w.lastBossRow(); ok {
		return w.bossRule.Words(entry)
	}
	return "a boss"
}

// restOutOfCombat polls bot_view every 5 s and waits for d of continuous quiet (review 2: a poll
// counts as quiet only when the call succeeded AND every roster hire is present with a row <= 10 s
// old AND none is in combat AND none is dead; any failed call, missing/stale hire, combat or dead
// restarts the window from zero). A second wipe (every hire dead with fresh states) fails the
// recovery exit 19, exactly like a second wipe during the next leg. The deadline bounds the whole
// recovery: combat restarts the quiet window but never moves the deadline.
func (w *walk) restOutOfCombat(ctx context.Context, step int, d time.Duration, deadline time.Time) error {
	seq0 := w.statusSeq
	var quietSince, lastPoll time.Time
	for {
		if w.leaseLost.Load() {
			return stopf(exitRaidWalkFailed, "lease lost")
		}
		if err := ctx.Err(); err != nil {
			return stopf(exitRaidWalkFailed, "interrupted")
		}
		if !w.env.now().Before(deadline) {
			return &recoveryTimeout{dur: deadlineDuration(deadline, d), paused: false}
		}
		incs, err := w.pollOnce(ctx, false)
		if err != nil {
			return err
		}
		for _, inc := range incs {
			if wipe, ok := inc.(raidwalk.IncidentWipe); ok {
				w.note("second wipe at step %d during the wipe-resume of step %d (rest or resume not finished)", wipe.Step, step)
				return stopf(exitRaidWalkIncident, "wipe at step %d", wipe.Step)
			}
		}
		// Defence in depth for a run that goes on without the walk: a status after the rest began
		// that is running again and either says "regrouped" (TB's note when its own regroup
		// finishes) or stands on a later step means the rest and the resume are pointless. Every
		// line of the poll counts, not only the last (walk 18 r5).
		if w.runWentOnAfter(seq0, step) {
			return errRunWentOn
		}
		if w.completeQuietView(ctx) {
			now := w.env.now()
			if quietSince.IsZero() {
				// The quiet began between the previous poll (which was not quiet) and this one:
				// start the window there, so a poll every 5 s closes it after d, not d + one poll
				// (the old `window += 5 s` credited that interval too).
				quietSince = lastPoll
				if quietSince.IsZero() {
					quietSince = now
				}
			}
			if now.Sub(quietSince) >= d {
				return nil
			}
			lastPoll = now
		} else {
			quietSince = time.Time{}
			lastPoll = w.env.now()
		}
		if err := w.env.sleep(ctx, 5*time.Second); err != nil {
			return stopf(exitRaidWalkFailed, "interrupted")
		}
	}
}

// deadlineDuration says how long the recovery ran before the deadline hit, in the timeout note.
func deadlineDuration(deadline time.Time, d time.Duration) time.Duration {
	return 3 * d
}

// completeQuietView is one review-2 quiet poll: the bot_view call succeeded AND every roster hire
// is present with a row <= 10 s old AND none is in combat AND none is dead. Anything else is not
// quiet (the window restarts from zero). A second wipe during the rest still ends 19 through the
// tracker's own wipe rule in pollOnce; the dead row here only breaks quiet.
func (w *walk) completeQuietView(ctx context.Context) bool {
	n, frame, err := w.view(ctx, w.afterN, 2000)
	if err != nil || frame == nil {
		return false
	}
	w.afterN = n
	now := w.env.now()
	w.trackFrame(now, frame)
	w.tracker.Frame(now, *frame)
	w.saveFrame(now, frame)
	byName := map[string]tbm.PartyRec{}
	for _, p := range frame.Party {
		byName[p.Name] = p
	}
	for _, name := range w.hireList() {
		p, ok := byName[name]
		if !ok {
			return false
		}
		pos, ok := w.botAt[name]
		if !ok || now.Sub(pos.at) > 10*time.Second {
			return false
		}
		if p.InCombat() || p.Dead || p.State == "d" {
			return false
		}
	}
	return true
}

// resumeRun sends `run resume` through the commander RPC and waits up to 60 s for the server to show the run
// running again. Anything else fails the recovery: no resume ACK, or no running line in 60 s, ends the walk the
// way a failed phase does. A wipe during the wait ends 19 like a second wipe. The wait stays inside the
// recovery deadline: past it the walk ends with the original incident's exit and the timed-out note.
func (w *walk) resumeRun(ctx context.Context, step int, from time.Time, rest time.Duration) error {
	rep, err := w.commander(ctx, commanderReq{Op: "run", Verb: "resume", WaitMS: 5000})
	if err != nil {
		return err
	}
	if rep.Reply.Err != nil {
		return stopf(exitRaidWalkFailed, "run resume: %s", rep.Reply.Err.Message)
	}
	if !rep.Reply.OK && rep.Reply.Kind != tbm.KindAck {
		return stopf(exitRaidWalkFailed, "run resume: no ACK (%s)", rep.Reply.Line)
	}
	w.logLine(w.env.now(), "> .bot run resume")
	// Review round 3: only a status received after this point may route the
	// frames' boss through the run-leader exception; a retained pre-resume
	// boss-kind line must not hide a newly engaged boss.
	w.resumeStatusSeq = w.statusSeq
	deadline := recoveryDeadline(from, rest, w.opts.maxRunMinutes, w.started)
	waitEnd := w.env.now().Add(60 * time.Second)
	if waitEnd.After(deadline) {
		waitEnd = deadline
	}
	for {
		if w.leaseLost.Load() {
			return stopf(exitRaidWalkFailed, "lease lost")
		}
		if err := ctx.Err(); err != nil {
			return stopf(exitRaidWalkFailed, "interrupted")
		}
		running, err := w.pollRunRunning(ctx, step)
		if err != nil {
			return err
		}
		if running {
			return nil
		}
		now := w.env.now()
		if !now.Before(waitEnd) {
			if !now.Before(deadline) {
				return &recoveryTimeout{dur: deadlineDuration(deadline, rest), paused: false}
			}
			return stopf(exitRaidWalkFailed, "run resume: the run is not running 60 s after the resume")
		}
		if err := w.env.sleep(ctx, 2*time.Second); err != nil {
			return stopf(exitRaidWalkFailed, "interrupted")
		}
	}
}

// pollRunRunning reads one events page and the frames, feeding the tracker like pollOnce but without the dashboard:
// true once the walk's plan shows running again. A wipe during the wait is the second wipe and ends 19.
func (w *walk) pollRunRunning(ctx context.Context, step int) (bool, error) {
	incs, err := w.pollOnce(ctx, false)
	if err != nil {
		return false, err
	}
	for _, inc := range incs {
		if wipe, ok := inc.(raidwalk.IncidentWipe); ok {
			w.note("second wipe at step %d during the wipe-resume of step %d (rest or resume not finished)", wipe.Step, step)
			return false, stopf(exitRaidWalkIncident, "wipe at step %d", wipe.Step)
		}
	}
	return w.haveStatus && w.lastStatus.State == tbm.RunRunning && w.lastStatus.Plan == w.plan.Name, nil
}

// nearWipeStop says whether a paused note is the run stopping after a wipe or a near wipe (walk 13 s7: "19 dead:
// 3 near wipes within 10 min (...): the run stops here. Regroup (c..."). Only that stop recovers; a regroup pause
// ("did not hold") keeps its --auto-resume path and everything else stops as now.
func nearWipeStop(note string) bool {
	lower := strings.ToLower(note)
	return strings.Contains(lower, "near wipe") || strings.Contains(lower, "the run stops here")
}

// selfRegroupNote says the run is regrouping by itself after a wipe (TB DungeonRunPolicy: "19 dead (...):
// regrouping by itself (1 of 2 at this step)", also "... dead and the fight went past N s (...): regrouping by
// itself ..."). The run stays running and rests, gathers and walks back on its own: a recovery must not rest or
// resume it (walk 17b m910q s4, test2 s2).
func selfRegroupNote(note string) bool {
	return strings.Contains(strings.ToLower(note), "regrouping by itself")
}

// selfRegroupWindow is how close a "regrouping by itself" status line must be to the wipe it
// explains. Live TB sends the note and then "regroup: waiting for X (dead), ... reviving" for the
// same step in one poll (walk 18 r5: m910q s4, test2 s2), so the note is at most one poll old.
const selfRegroupWindow = 30 * time.Second

// selfRegroupReviveWait is how long after TB's release (the regroup line the walk read) a self-regroup waits before its
// first `.revive`: the released bots' graveyard repop must fire (or be cleared by TB) before a bot is made alive again.
const selfRegroupReviveWait = 10 * time.Second

// waitPastRelease sleeps until selfRegroupReviveWait after the regroup line (regroupAt), or not at all when that has
// passed already. With no regroup time it waits the full wait.
func (w *walk) waitPastRelease(ctx context.Context, deadline time.Time) error {
	d := selfRegroupReviveWait
	if !w.regroupAt.IsZero() {
		d = w.regroupAt.Add(selfRegroupReviveWait).Sub(w.env.now())
	}
	if d <= 0 {
		return nil
	}
	return w.recoverySleep(ctx, d, deadline)
}

// stillDeadHires reads a fresh frame and returns the hires it shows dead, in hire order. It logs one line with the
// counts: alive hires (TB already brought them back) and hires missing from the frame are never revived.
func (w *walk) stillDeadHires(ctx context.Context, deadline time.Time) ([]string, error) {
	now := w.env.now()
	n, frame, err := w.view(ctx, w.afterN, 2000)
	if ctx.Err() != nil {
		return nil, stopf(exitRaidWalkFailed, "interrupted")
	}
	if err != nil || frame == nil {
		return nil, stopf(exitRaidWalkIncident, "not revived: could not read which hires are still dead after the regroup (%v)", err)
	}
	w.afterN = n
	w.trackFrame(now, frame)
	if timeout := w.recoveryExpired(deadline); timeout != nil {
		return nil, timeout
	}
	dead, alive, unread := stillDead(w.hireList(), frame.Party)
	msg := fmt.Sprintf("revive after the regroup: %d hire(s) still dead (revived), %d alive (TB brought them back: not revived), %d not in the frame (not revived)", len(dead), alive, unread)
	w.note("%s", msg)
	w.logLine(w.env.now(), msg)
	return dead, nil
}

// stillDead splits the hires by the party read of one frame: dead when the read shows them dead, alive when it shows
// them up, unread when the party list does not have them. Hire order is kept.
func stillDead(hires []string, party []tbm.PartyRec) (dead []string, alive, unread int) {
	byName := make(map[string]tbm.PartyRec, len(party))
	for _, p := range party {
		byName[p.Name] = p
	}
	for _, name := range hires {
		p, ok := byName[name]
		switch {
		case !ok:
			unread++
		case p.Dead || p.State == "d":
			dead = append(dead, name)
		default:
			alive++
		}
	}
	return dead, alive, unread
}

// runObsKeep bounds w.runLines: a running status stays for the self-regroup window and well past a
// rest turn, while a long watch still cannot grow the list without bound.
const runObsKeep = 5 * time.Minute

// recordRunLine keeps a running status line for the wipe's self-regroup rule and the rest's
// went-on rule, so both see every line of a poll and not only the last one (walk 18 r5). Older
// lines are dropped: nothing reads past runObsKeep.
func (w *walk) recordRunLine(st tbm.RunStatus, now time.Time) {
	if st.State != tbm.RunRunning {
		return
	}
	w.runLines = append(w.runLines, runObs{seq: w.statusSeq, step: st.Step, note: st.Note, at: now})
	cut := 0
	for cut < len(w.runLines) && now.Sub(w.runLines[cut].at) > runObsKeep {
		cut++
	}
	if cut > 0 {
		w.runLines = w.runLines[cut:]
	}
}

// selfRegroupLine returns the running status line recorded since the last recovery that reports the run
// regrouping by itself: its note arrived within selfRegroupWindow of now and either it is the line of
// this step — pollOnce keeps every running line of a page, so the note still counts when a later line
// of the same page ("regroup: waiting ... reviving", the run's own "regrouped") is the last status the
// wipe fires on (walk 18 r5) — or its own page carried the run on to this step: the note's page holds
// the note and the later line that moved the step, so the wipe explains the note (walk 19 r6).
func (w *walk) selfRegroupLine(step int, now time.Time) (runObs, bool) {
	for i := len(w.runLines) - 1; i >= 0; i-- {
		o := w.runLines[i]
		if !selfRegroupNote(o.note) || now.Sub(o.at) > selfRegroupWindow {
			continue
		}
		if o.step == step || (o.seq > w.pollSeq0 && step > o.step) {
			return o, true
		}
	}
	return runObs{}, false
}

// runWentOnAfter says a running status line the rest read after it began (seq0) shows the run going
// on by itself: it says "regrouped" (TB's note when its own regroup finishes) or stands on a later
// step. Every line of a poll counts, not only the last (walk 18 r5).
func (w *walk) runWentOnAfter(seq0 uint64, step int) bool {
	for i := len(w.runLines) - 1; i >= 0; i-- {
		o := w.runLines[i]
		if o.seq <= seq0 {
			break
		}
		if strings.HasPrefix(o.note, "regrouped") || o.step > step {
			return true
		}
	}
	return false
}

// quietState is what one revive's run of quiet reads remembers: the patrols' last distances (the exemption needs two
// reads), the units' last idle-at-home reads (D-A's two-read rule, review round 1 Opus M1), and the blocker set the
// last failed read logged (D-E logs a change of blockers, not the same ones every 5 s). One state belongs to ONE
// checked point: a fresh one whenever the point changes (Codex H2).
type quietState struct {
	prev    map[string]float64
	idle    *raidwalk.QuietIdleReads
	homes   raidwalk.QuietHomeSpawns
	patrols []raidwalk.QuietPatrol
	logged  string
}

func newQuietState() *quietState {
	return &quietState{prev: map[string]float64{}, idle: raidwalk.NewQuietIdleReads()}
}

// newQuietStateFor builds the quiet state for a revive of this plan: the
// proven-home spawn list the idle exemption reads. The plan may be nil in
// tests; then nothing is exempt (fail closed).
func (w *walk) newQuietStateFor() *quietState {
	st := newQuietState()
	st.homes = raidwalk.PlanHomeSpawns(w.plan)
	st.patrols = raidwalk.PlanQuietPatrols(w.plan)
	return st
}

// quietRead is one quiet pass's outcome: the point is quiet (ok), or the last failed read's reason and blockers. A
// timeout says the recovery's deadline passed mid-pass: the caller returns it as the timed-out ending, as before. An
// err is the interrupt a sleep hit: the caller ends the walk with it (exit 18), never as a refusal.
type quietRead struct {
	ok       bool
	why      string
	blockers []raidwalk.QuietBlocker
	timeout  *recoveryTimeout
	err      error
}

// quietOnce is one quiet read of p: the commander's own position (H1: a read of a point its character is nowhere near
// proves nothing), the hostile units within 200 yd, the patrol exemptions, raidwalk's own quiet rules, and (D-E) one
// run.log line naming every blocker when the blocker set changed since the last failed read.
func (w *walk) quietOnce(ctx context.Context, st *quietState, p dungeon.Point) (bool, string, []raidwalk.QuietBlocker) {
	// The commander must stand at the point this read judges (review round 1, Codex/Opus H1): a `.go xyz` the server
	// accepted but never landed leaves the commander where it was, and a read of a point its own character is nowhere
	// near judges a copy it cannot see. placeReachYd is the reach the placement accepts the same point within.
	if stx, serr := w.status(ctx); serr != nil {
		return false, "status: " + serr.Error(), nil
	} else if stx.Map != w.plan.Map || stx.Falling {
		// Codex round 2: the long quiet wait gives the commander time to be moved (a teleport, a fall, another map);
		// a read from anywhere but the point's own map and floor judges surroundings the revive will not have.
		return false, fmt.Sprintf("the commander is not standing on map %d (map %d, falling %v)", w.plan.Map, stx.Map, stx.Falling), nil
	} else if d := math.Hypot(float64(stx.X-p.X), float64(stx.Y-p.Y)); d > placeReachYd {
		return false, fmt.Sprintf("the commander stands %.1f yd from the read point (over the %d yd reach)", d, placeReachYd), nil
	} else if !stx.ZUnknown && math.Abs(float64(stx.Z-p.Z)) > reviveLandingZYd {
		return false, fmt.Sprintf("the commander stands %.1f yd above or below the read point", math.Abs(float64(stx.Z-p.Z))), nil
	}
	us, err := w.unitsAt(ctx, 200)
	if err != nil {
		st.idle.Reset() // a gap in the reads breaks the two-read idle rule (Codex round 3)
		return false, "units: " + err.Error(), nil
	}
	patrols := make([]raidwalk.QuietPatrol, 0, len(w.quietPatrols)+len(st.patrols))
	patrols = append(patrols, w.quietPatrols...)
	patrols = append(patrols, st.patrols...)
	w.applyPlanPatrolState(patrols)
	for j := range patrols {
		if j >= len(w.patrolGUIDS) {
			// A plan patrol has no member guids: the run's state decides it (applyPlanPatrolState), never the units' distances.
			continue
		}
		best := -1.0
		for _, u := range us {
			key := strings.ToUpper(u.GUID.String())
			if !w.patrolGUIDS[j][key] && !w.patrolGUIDS[j][fmt.Sprintf("0X%016X", uint64(u.GUID))] {
				continue
			}
			if d := raidDist3(p, u.X, u.Y, u.Z); d > best {
				best = d
			}
		}
		if best < 0 {
			delete(st.prev, patrols[j].ID)
			continue
		}
		if old, ok := st.prev[patrols[j].ID]; ok && best >= 80 && best > old {
			patrols[j].Exempt = true
		}
		st.prev[patrols[j].ID] = best
	}
	bs := raidwalk.QuietBlockers(us, patrols, p, st.idle, st.homes, w.env.now())
	if len(bs) == 0 {
		st.logged = ""
		return true, "", nil
	}
	// D-E: the next walk then shows whether the blockers leash or leave.
	if key := blockerKey(bs); key != st.logged {
		st.logged = key
		w.logLine(w.env.now(), fmt.Sprintf("quiet read at (%s %s %s): %s", num32(p.X), num32(p.Y), num32(p.Z), blockerWords(bs)))
	}
	return false, bs[0].Reason(), bs
}

// applyPlanPatrolState sets each plan patrol's exemption and remark for this read. A plan patrol's pack is done
// (raidwalk.PatrolPackDone: its pull step was pulled and the run is past it, or its bound members are all dead, see
// packMembersHP), and then its path stops blocking. A patrol whose pack cannot be tied to its units, whose pull step is
// not in the rows, or whose members do not show it done keeps blocking and says so in its reason (fail safe).
func (w *walk) applyPlanPatrolState(patrols []raidwalk.QuietPatrol) {
	progress := 0
	if w.haveStatus {
		progress = w.lastStatus.Step
	}
	for j := len(w.quietPatrols); j < len(patrols); j++ {
		p := &patrols[j]
		p.Exempt, p.Note = false, ""
		if p.Pack == nil {
			p.Note = "its pull group cannot be tied to its units: blocks by path"
			continue
		}
		step := raidwalk.PullStepOf(w.rows, *p.Pack)
		pulled := step > 0 && w.pulledSteps[step]
		hp, why := w.packMembersHP(p.Pack, p.Path)
		p.Exempt = raidwalk.PatrolPackDone(step, pulled, progress, hp)
		if p.Exempt {
			continue
		}
		var notes []string
		if step == 0 {
			notes = append(notes, "its pull row is not in the plan's rows: blocks by path")
		}
		if why != "" {
			notes = append(notes, why)
		}
		p.Note = strings.Join(notes, "; ")
	}
}

// patrolCorridorYd is how close a pack unit's first sighting must stand to a plan patrol's path to be that patrol's member.
const patrolCorridorYd = 15

// patrolSpawnYd is how close a pack unit's first sighting must stand to one of the pack's spawns (its pull group's
// positions) to be that pack's member: the raidwalk home-spawn tolerance (quietHomeYd).
const patrolSpawnYd = 3

// packMembersHP binds a plan patrol's members by guid (Codex HIGH, round 4). A hostile of the pack's members joins the
// patrol only when the frames FIRST saw it within patrolCorridorYd of a path point or within patrolSpawnYd of a pack spawn,
// so a same-entry mob first seen elsewhere never joins, dead or alive. It returns each bound member's last frame health.
// The patrol can be retired only when at least the pack's unit count is bound (the set is then non-empty); with fewer,
// it returns no health and a reason, so the caller's PatrolPackDone is false and the path keeps blocking (fail safe).
func (w *walk) packMembersHP(pack *raidwalk.PatrolPack, path []dungeon.Point) ([]int, string) {
	var hp []int
	for _, h := range w.hostiles {
		if !inEntries(pack.Members, h.entry) || !patrolBinds(h.first, path, pack.Spawns) {
			continue
		}
		hp = append(hp, h.hppct)
	}
	if want := len(pack.Entries); len(hp) < want {
		return nil, fmt.Sprintf("%d of its %d pack units are bound to the path: blocks by path", len(hp), want)
	}
	return hp, ""
}

// patrolBinds says a unit first seen at first is a member of the patrol with this path and pack spawns (packMembersHP).
func patrolBinds(first dungeon.Point, path, spawns []dungeon.Point) bool {
	for _, pt := range path {
		if raidDist3(pt, first.X, first.Y, first.Z) <= patrolCorridorYd {
			return true
		}
	}
	for _, s := range spawns {
		if raidDist3(s, first.X, first.Y, first.Z) <= patrolSpawnYd {
			return true
		}
	}
	return false
}

// blockerKey identifies a blocker set for D-E's log-on-a-change rule: the units and patrols by identity and state,
// without their distances, so a mob moving about at home does not write a line on every read.
func blockerKey(bs []raidwalk.QuietBlocker) string {
	parts := make([]string, 0, len(bs))
	for _, b := range bs {
		if b.Patrol {
			parts = append(parts, "patrol:"+b.Name)
			continue
		}
		parts = append(parts, fmt.Sprintf("unit:%s:%v:%v:%d", b.GUID, b.Moving, b.InCombat, b.Target))
	}
	return strings.Join(parts, "|")
}

// blockerWords is the D-E run.log line's blocker list: each blocker's entry, GUID, name, distance and state.
func blockerWords(bs []raidwalk.QuietBlocker) string {
	parts := make([]string, 0, len(bs))
	for _, b := range bs {
		if b.Patrol {
			parts = append(parts, fmt.Sprintf("patrol %s %.1f yd", b.Name, b.Dist))
			continue
		}
		parts = append(parts, fmt.Sprintf("%s entry %d guid %s %.1f yd moving=%v in_combat=%v target=%d",
			b.Name, b.Entry, b.GUID, b.Dist, b.Moving, b.InCombat, b.Target))
	}
	return strings.Join(parts, "; ")
}

// quietCheck is the revive point's quiet check (spec 3.5): every 5 s up to 120 s, with a patrol exempt when one
// of its members is visible 80 yd out and its distance grew between two checks. 200 yd asks for the far members;
// raidwalk.QuietBlockers still enforces its own 45/80 yd rules and D-A's idle exemption. The deadline is the
// recovery's entry deadline (zero: the old unbounded path): every loop turn and sleep checks it, and
// past it the check fails with the timed-out recovery instead of true/false.
func (w *walk) quietCheck(ctx context.Context, st *quietState, p dungeon.Point, deadline time.Time) quietRead {
	for i := range 24 {
		if timeout := w.recoveryExpired(deadline); timeout != nil {
			return quietRead{timeout: timeout}
		}
		ok, why, bs := w.quietOnce(ctx, st, p)
		if ok {
			return quietRead{ok: true}
		}
		if i == 23 {
			return quietRead{why: why, blockers: bs}
		}
		if err := w.recoverySleep(ctx, 5*time.Second, deadline); err != nil {
			if timeout, ok := err.(*recoveryTimeout); ok {
				return quietRead{timeout: timeout}
			}
			// An interrupt is not a refusal (review round 1, Opus L2): the caller ends the walk with it.
			return quietRead{err: err}
		}
	}
	return quietRead{why: "the units never went quiet"}
}

// quietWait is D-C (walk 25): the 120 s check found the point blocked, so re-read it every 10 s until end, until it
// goes quiet, or until the recovery's deadline ends the walk. A blocker that is only passing (a real chaser, a summoned
// add that despawns) turns it quiet here; an idle mob at home does not (D-A already let those through). end is
// quietWaitEnd, always before deadline, and the last sleep is cut short so the wait ends on end itself.
func (w *walk) quietWait(ctx context.Context, st *quietState, p dungeon.Point, from quietRead, end, deadline time.Time) quietRead {
	out := from
	for {
		// The check just read the point: the wait sleeps first.
		d := 10 * time.Second
		if rem := end.Sub(w.env.now()); rem < d {
			if rem <= 0 {
				return out
			}
			d = rem
		}
		if err := w.recoverySleep(ctx, d, deadline); err != nil {
			if timeout, ok := err.(*recoveryTimeout); ok {
				return quietRead{timeout: timeout}
			}
			// An interrupt during D-C is the interrupted ending (review round 1, Opus L2), not a "not revived" refusal.
			return quietRead{err: err}
		}
		ok, why, bs := w.quietOnce(ctx, st, p)
		if ok {
			return quietRead{ok: true}
		}
		out = quietRead{why: why, blockers: bs}
		if !w.env.now().Before(end) {
			return out
		}
	}
}

// quietWaitEnd is D-C's wait end: the recovery's deadline less the rest and the 30 s that follow the wait, so the rest
// and the resume still fit before it. Zero when there is no deadline (the boss-wipe ending, where nothing follows the
// revive): that revive does not wait. A self-regroup gets a second cap (review round 1, Opus M2): the run regroups by
// itself, so a wait longer than selfRegroupQuietCap past the wipe is a wait on a regroup that has failed.
func (w *walk) quietWaitEnd(deadline time.Time, selfRegroup bool) time.Time {
	if deadline.IsZero() {
		return time.Time{}
	}
	end := deadline.Add(-30 * time.Second)
	if selfRegroup {
		if !w.wipeAt.IsZero() {
			if cap := w.wipeAt.Add(selfRegroupQuietCap); cap.Before(end) {
				end = cap
			}
		}
	} else {
		end = end.Add(-w.opts.wipeRest)
	}
	return end
}

// selfRegroupQuietCap is how long a self-regroup's D-C wait may run past the wipe (review round 1, Opus M2): TB's own
// regroup takes a minute or two, so a point still blocked five minutes on is not going to be freed by it.
const selfRegroupQuietCap = 5 * time.Minute

// refusal is the summary's revive_refusal (D-D, walk 25): waited is how long the revive waited for its point to go
// quiet, from its first read. all_idle says every blocker of the last read was idle, which is what makes waiting
// pointless; a read that named no unit blocker at all (a failed RPC, say) does not claim that. entrance_tried says the
// entrance fallback's go went out (review round 1, Opus L3).
// tried is every point the revive tried, in order (the revive point and its fallback rows): a call without one
// records the revive point alone. The entrance has no step; entranceTried says it was reached.
func (w *walk) refusal(step int, blockers []raidwalk.QuietBlocker, waited time.Duration, entranceTried bool, tried ...int) *raidwalk.ReviveRefusal {
	at := w.reviveStep()
	if len(tried) == 0 {
		tried = []int{at}
	}
	r := &raidwalk.ReviveRefusal{
		WipeStep: step, StartStep: at, TriedSteps: append([]int{}, tried...), EntranceTried: entranceTried,
		WaitedS: int(waited / time.Second), Blockers: make([]raidwalk.ReviveBlocker, 0, len(blockers)),
		AllIdle: len(blockers) > 0,
	}
	for _, b := range blockers {
		idle := !b.Patrol && !b.InCombat && b.Target == 0 && !b.Moving
		r.Blockers = append(r.Blockers, raidwalk.ReviveBlocker{Entry: b.Entry, Name: b.Name, Dist: b.Dist,
			Idle: idle, InCombat: b.InCombat, Patrol: b.Patrol})
		if !idle {
			r.AllIdle = false
		}
	}
	return r
}

// reviveStep is the step of the revive point the walk actually uses (the refusal's start_step, review round 1, Opus
// L3): the placement's staged start with --place, else the first positioned row (firstStepPoint) — step 1 unless step
// 1 is a point-less rest row. It is not the resolved --from step: a plain --from walk revives at the plan's own first
// point, whatever step the run is started on.
func (w *walk) reviveStep() int {
	if w.place != nil {
		return w.place.Start
	}
	for i, s := range w.rows.Steps {
		if s.X == 0 && s.Y == 0 && s.Z == 0 {
			continue
		}
		return i + 1
	}
	return 1
}

// reviveCandidate is one fallback point of a refused revive: the row it comes from and its point.
type reviveCandidate struct {
	step  int
	point dungeon.Point
}

// reviveFallbackRows lists the row fallbacks after the revive point, in the order the revive tries them: the earlier
// move rows of the revive point's own leg, newest first, then the previous leg's last move row. The instance entrance
// is the caller's last fallback. A point-less row (a rest at 0,0,0) is not a candidate.
func (w *walk) reviveFallbackRows() []reviveCandidate {
	return reviveFallbackRows(w.rows, w.reviveStep())
}

// reviveFallbackRows is the candidate list for a step in rows (the walk's own revive point uses it).
func reviveFallbackRows(rows planexport.Rows, at int) []reviveCandidate {
	if at < 1 || at > len(rows.Steps) {
		return nil
	}
	leg := rows.Steps[at-1].Leg
	var out []reviveCandidate
	for i := at - 2; i >= 0; i-- {
		s := rows.Steps[i]
		if s.Leg != leg || !reviveMoveRow(s) {
			continue
		}
		out = append(out, reviveCandidate{step: i + 1, point: dungeon.Point{X: s.X, Y: s.Y, Z: s.Z}})
	}
	for i := at - 2; i >= 0; i-- {
		s := rows.Steps[i]
		if s.Leg != leg-1 {
			continue
		}
		if reviveMoveRow(s) {
			out = append(out, reviveCandidate{step: i + 1, point: dungeon.Point{X: s.X, Y: s.Y, Z: s.Z}})
			break
		}
	}
	return out
}

// reviveMoveRow says the row is a positioned move the revive may stand on.
func reviveMoveRow(s planexport.StepRow) bool {
	if s.X == 0 && s.Y == 0 && s.Z == 0 {
		return false
	}
	return s.Kind == planexport.KindMove || s.Kind == planexport.KindMoveDirect
}

// reviveGoPoint teleports the commander to p, waits for the landing, and quiet-checks the point once (a fallback row
// gets one read: a blocked one is left for the next candidate, it does not get the 120 s wait the revive point does).
// landed is false when the go never landed: the commander still stands at the previous point, so the caller refuses
// that candidate without reading a point its character is nowhere near (quietOnce's own reach rule). A timeout or a
// walk-ending error is returned for the caller to pass on.
func (w *walk) reviveGoPoint(ctx context.Context, p dungeon.Point, joined, selfRegroup bool, deadline time.Time) (quietRead, bool, error) {
	from, err := w.reviveFrom(ctx, joined, selfRegroup, deadline)
	if err != nil {
		return quietRead{}, false, err
	}
	seq, have := w.reviveSeq(ctx, deadline)
	if st, serr := w.status(ctx); serr == nil {
		w.logLine(w.env.now(), fmt.Sprintf("revive go from map %d (%.1f %.1f %.1f, z unknown %v, teleports %d)", st.Map, st.X, st.Y, st.Z, st.ZUnknown, st.TeleportSeq))
	}
	if err := w.gm(ctx, fmt.Sprintf(".go xyz %s %s %s %d", num32(p.X), num32(p.Y), num32(p.Z), w.plan.Map)); err != nil {
		return quietRead{}, false, err
	}
	if err := w.waitRevivePoint(ctx, p, from, seq, have, deadline); err != nil {
		if timeout, isTimeout := err.(*recoveryTimeout); isTimeout {
			return quietRead{timeout: timeout}, false, nil
		}
		return quietRead{why: fmt.Sprintf("the commander did not land within %d yd of the point on map %d", reviveLandingYd, w.plan.Map)}, false, nil
	}
	qs := w.newQuietStateFor()
	ok, why, bs := w.quietOnce(ctx, qs, p)
	return quietRead{ok: ok, why: why, blockers: bs}, true, nil
}

// recoveryExpired reports the recovery's timed-out ending when the entry deadline
// passed (nil while time is left or when there is no deadline). The duration is
// the same 3 x rest the rest and resume waits report (deadline minus start).
func (w *walk) recoveryExpired(deadline time.Time) *recoveryTimeout {
	if deadline.IsZero() || w.env.now().Before(deadline) {
		return nil
	}
	return &recoveryTimeout{dur: deadlineDuration(deadline, w.opts.wipeRest), paused: false}
}

// recoverySleep sleeps but fails with the timed-out recovery when the entry
// deadline passes first (an interrupt keeps the old interrupted ending).
func (w *walk) recoverySleep(ctx context.Context, d time.Duration, deadline time.Time) error {
	if err := w.env.sleep(ctx, d); err != nil {
		return stopf(exitRaidWalkFailed, "interrupted")
	}
	if timeout := w.recoveryExpired(deadline); timeout != nil {
		return timeout
	}
	return nil
}

func raidDist3(p dungeon.Point, x, y, z float32) float64 {
	dx, dy, dz := float64(p.X-x), float64(p.Y-y), float64(p.Z-z)
	return math.Sqrt(dx*dx + dy*dy + dz*dz)
}

// phaseHold is phase 11: keep the raid bound where it stands, with a commander snapshot every 60 s. SIGINT
// ends it early and it is not a failure.
func (w *walk) phaseHold(ctx context.Context) error {
	hold := time.Duration(w.opts.holdMinutes) * time.Minute
	if hold <= 0 {
		return nil
	}
	end := w.ended.Add(hold)
	var last time.Time
	for w.env.now().Before(end) {
		if ctx.Err() != nil {
			return nil
		}
		now := w.env.now()
		if w.dash != nil && (last.IsZero() || now.Sub(last) >= 60*time.Second) {
			w.saveCommanders("hold")
			last = now
		}
		left := end.Sub(now)
		if left > 10*time.Second {
			left = 10 * time.Second
		}
		if err := w.env.sleep(ctx, left); err != nil {
			return nil
		}
	}
	return nil
}

// phaseTeardown is phase 12: the commander status, then teardown --json; a hire the status did not list is
// possibly left online.
func (w *walk) phaseTeardown() {
	if w.slotDir == "" {
		return
	}
	if w.dash != nil {
		w.saveCommanders("end")
		w.saveFights()
	}
	listedOK := false
	if w.agent != nil {
		ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
		if rep, err := w.commander(ctx, commanderReq{Op: "status", WaitMS: 5000}); err == nil {
			var listed map[string]bool
			listed, listedOK = commanderListedNames(rep.Reply)
			if listedOK {
				var missing []string
				for _, name := range w.hireList() {
					if !listed[strings.ToLower(name)] {
						missing = append(missing, name)
					}
				}
				if len(missing) > 0 {
					w.note("commander status did not list: %s", strings.Join(sortedNames(missing), ", "))
				}
				w.hiresLeft = sortedNames(missing)
			}
		}
		cancel()
	}
	if w.place != nil && w.agent != nil {
		// Out to the portal and `.instance unbind <plan map>` (spec 5.1). The external teardown ends the commander's
		// agent, so these lines go out before it, not after.
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		w.placeLeave(ctx)
		cancel()
	}
	var buf bytes.Buffer
	code := w.env.teardown([]string{"--json", w.slotDir}, &buf, &buf)
	if err := w.writeOut("teardown.json", buf.Bytes()); err != nil {
		w.note("teardown.json: %v", err)
	}
	t, ok := parseTeardownJSON(buf.Bytes())
	w.teardownWd.Failed = !ok || code != exitOK || t.Failed > 0
	if !ok {
		w.teardownWd.Reason = "teardown: the report did not parse"
		w.note("teardown: the report did not parse")
	} else if w.teardownWd.Failed {
		w.teardownWd.Reason = teardownReason(t)
		w.note("teardown: %s", w.teardownWd.Reason)
	}
	if w.hiresLeft != nil {
		w.teardownWd.HiresLeft = w.hiresLeft
	} else {
		w.teardownWd.HiresLeft = []string{}
	}
}

// firstLine is a report's first line, for an error text.
func firstLine(s string) string {
	s = strings.TrimSpace(s)
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		s = s[:i]
	}
	if s == "" {
		return "no output"
	}
	return s
}

// durWords is a duration as the spec writes it ("5m", "120s").
func durWords(d time.Duration) string {
	if d%time.Minute == 0 {
		return fmt.Sprintf("%dm", int(d/time.Minute))
	}
	return fmt.Sprintf("%ds", int(d/time.Second))
}
