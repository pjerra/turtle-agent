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
	if err := w.phaseBind(ctx); err != nil {
		return w.stop(err, true)
	}
	if err := w.phaseEnter(ctx); err != nil {
		return w.stop(err, true)
	}
	if err := w.phasePlace(ctx); err != nil {
		return w.stop(err, true)
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
	lines := w.roster.HireLines()
	for i, b := range w.roster.Batches {
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
	w.logLine(w.env.now(), fmt.Sprintf("waiting %s for the last hires' join summons", durWords(hireSettleWait)))
	if err := w.env.sleep(ctx, hireSettleWait); err != nil {
		return stopf(exitRaidWalkFailed, "interrupted")
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
// no help: it names a hire before its join, and its zone there stays stale after it.
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

// checkStartLine feeds the start line to the tracker and checks it: running, and the until label.
func (w *walk) checkStartLine(ctx context.Context, line string) error {
	st, ok := raidwalk.ParseRun(line)
	if !ok {
		return stopf(exitRaidWalkFailed, "run start: %s", line)
	}
	w.logLine(w.env.now(), line)
	if w.tracker != nil {
		w.tracker.Feed(w.env.now(), st)
	}
	w.lastStatus, w.haveStatus, w.statusSeq = st, true, w.statusSeq+1
	w.noteLabel(st)
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
		// round 1, item 2). bossNoKill notes the first tick without evidence once, not every tick.
		bossSkip   bool
		bossNoKill bool
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
				if ok, early := w.runFightsBoss(v.Entry); ok {
					if early > 0 {
						w.note("boss %d engaged at step %d, before its boss step %d in the same leg, and the until (%s) goes past it, so the run leader fights it", v.Entry, v.Step, early, w.untilLabel)
						w.logLine(now, fmt.Sprintf("boss %d engaged at step %d, before its boss step %d in the same leg, and the until (%s) goes past it, so the run leader fights it", v.Entry, v.Step, early, w.untilLabel))
					} else {
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
				ps.bossSkip, ps.bossNoKill = false, false
				w.logLine(now, "paused: "+w.lastStatus.Note)
				if w.dash != nil {
					w.saveCommanders("pause")
				}
			}
			regroup := strings.Contains(ps.note, "did not hold")
			nearWipe := nearWipeStop(ps.note)
			bossEntry, bossNotFound := w.bossNotFoundAtBossStep(ps.step, ps.note)
			if bossNotFound && !ps.bossSkip {
				// The run leader paused with "boss <N> not found" on that boss's own step. The frames are the
				// evidence: when they show a boss unit killed, the walk skips the step and the run goes on to its
				// until (walk 24 test2 r2). The evidence is re-read on every tick while the pause lasts (review
				// round 1, item 2: the kill can land after the pause arrived), and the skip is latched only once it
				// went out: a tick without evidence changes nothing (the pause path below still ends the walk), and
				// a failed send is retried once.
				if ok, ev := w.bossKilled(ctx, bossEntry, now); ok {
					ps.bossSkip = true
					w.bossSkipStep(ctx, bossEntry, ev, ps.step, now)
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
	lines, err := w.pump(ctx, 5000)
	if err != nil {
		return nil, err
	}
	for _, line := range lines {
		w.logLine(now, line)
		st, ok := raidwalk.ParseRun(line)
		if !ok || st.Plan != w.plan.Name {
			continue
		}
		w.tracker.Feed(now, st)
		w.lastStatus, w.haveStatus, w.statusSeq = st, true, w.statusSeq+1
		w.pullStarted(now, st)
		w.recordRunLine(st, now)
		w.noteLabel(st)
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

// pump reads one events page from the cursor and returns its TBM lines.
func (w *walk) pump(ctx context.Context, waitMS int) ([]string, error) {
	var page events.Page
	params := map[string]any{"since": w.eventsAt, "kinds": []string{"system", "addon"}, "wait_ms": waitMS}
	timeout := time.Duration(waitMS)*time.Millisecond + 15*time.Second
	if err := w.agent.Call(ctx, "events", params, &page, timeout); err != nil {
		return nil, stopf(exitRaidWalkFailed, "events: %v", err)
	}
	w.eventsAt = page.Next
	lines := make([]string, 0, len(page.Events))
	for _, e := range page.Events {
		if line, ok := tbm.Line(e); ok {
			lines = append(lines, line)
		}
	}
	return lines, nil
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
		lines, err := w.pump(ctx, 1000)
		if err != nil {
			return "", false, err
		}
		for _, line := range lines {
			w.logLine(w.env.now(), line)
			if match(line) || match(raidwalk.CleanLine(line)) {
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
// over a navmesh hole (reviveLanded's seq term). One retry, 1 s later, within the recovery deadline; have is false
// when no read answered (the near rule then falls back to the settled z, as before).
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
// After a NEAR teleport (fromMap == mapID) the server must also have sent a position: not ZUnknown, the session's
// guess until the floor drop or a server position settles it; or the session must have ACKED the teleport
// (TeleportSeq > beforeSeq), which is the landing signal when a navmesh hole under p leaves the z guess unsettled
// for good (walk 24 test2 r3). After a FAR teleport (fromMap != mapID) ZUnknown stays set for good, as the session
// lands it through SMSG_NEW_WORLD with no floor drop and the core never sends the player its own position (walk 23:
// 6 runs exit 19, the commander waited on map 0 at the portal); there the map change is the landing itself, since
// the session acks the worldport at once and the server places the player on reading that ack, before any later
// chat line. Falling still blocks: a dropToFloor fall must finish first.
func reviveLanded(p dungeon.Point, mapID, fromMap uint32, st session.StatusInfo, beforeSeq uint64, haveSeq bool) bool {
	if st.Map != mapID || st.Falling {
		return false
	}
	if st.ZUnknown && fromMap == mapID && !(haveSeq && st.TeleportSeq > beforeSeq) {
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
			w.hostiles[h.GUID.String()] = hostileState{entry: h.Entry, x: h.X, y: h.Y, z: h.Z, kind: h.Kind, reaction: h.Reaction, flags: h.Flags,
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
	// An entry the frame shows more than one LIVE unit of at once exists several at a time: it is an add, not a
	// boss reborn, and its corpse proves nothing (bossUnits). Thekal's two Zulian Guardians 15068 are summoned
	// together (boss_thekal.cpp:41/:413) while Thekal reborn 14509 stands alone.
	for i := range f.Hostiles {
		if f.Hostiles[i].HPPct <= 0 || w.bossAdds[f.Hostiles[i].Entry] {
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
	// showed a boss unit engaged, judged like the tracker's one-shot latch (checkBoss). bossKilled reads it to
	// keep a 0 % row an earlier attempt left behind out of the kill evidence (review round 3), so it is recorded
	// per unit entry and only the first sighting counts.
	for _, h := range f.Hostiles {
		if h.GUID == 0 || !w.bossUnit(h.Entry) || !bossAttemptEngaged(h.HPPct, uint64(h.Target), w.partyG) {
			continue
		}
		if w.bossAttemptAt[h.Entry].IsZero() {
			w.bossAttemptAt[h.Entry] = now
		}
	}
}

// bossAttemptEngaged is the tracker's one-shot boss latch judgement (checkBoss): a plan-boss row starts the
// attempt when it is hurt (below 100 %) or holds a party target. A 0 % row counts too, as it does there: the
// frames' own 0 % sighting of a fresh corpse is the kill bossKilled reads.
func bossAttemptEngaged(hppct int, target uint64, partyG map[uint64]bool) bool {
	return hppct < 100 || (target != 0 && partyG[target])
}

// pullStarted arms the pull-joiners window when a "pulling <entry> <guid>" status
// line goes by: 10 s in which frames count the engaged hostiles that are not the
// pulled pack. The pack is the pulled guid plus the plan pack's other members:
// every guid whose frame position stands within the row's radius of the pack
// centroid (w.pullRow).
func (w *walk) pullStarted(now time.Time, st tbm.RunStatus) {
	_, guid, ok := raidwalk.PullingTarget(raidwalk.CleanLine(st.Note))
	if !ok {
		return
	}
	w.pullPack = map[packet.GUID]bool{guid: true}
	w.pullSeed = guid
	w.pullRow = packRowOf(w.rows, st.Step)
	w.pullPackSeeded = false
	w.pullJoinsAt, w.pullJoinsStep = now, st.Step
	w.pullJoinsSeen = map[packet.GUID]bool{}
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
// outside the pulled pack's guids joins the seen set. 10 s after the pull
// the count is final: the record is written keyed by the pull step and the
// window closes. A newer pull re-arms first (pullStarted runs on the status
// lines of the same poll before the frame).
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
	// The first frame of the window sees the pack where it was pulled from
	// (trackFrame already recorded this frame's positions): its other members
	// are the guids within the pull row's radius, whatever their entry.
	if !w.pullPackSeeded {
		w.pullPackSeeded = true
		for _, g := range raidwalk.PullPackMembers(w.pullSeed, w.pullRow, w.packPositions()) {
			w.pullPack[g] = true
		}
	}
	for _, h := range f.EngagedHostiles() {
		if h.GUID == 0 || w.pullPack[h.GUID] {
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
	w.pullJoins = append(w.pullJoins, raidwalk.PullJoiners{Step: w.pullJoinsStep, At: w.pullJoinsAt, Count: len(rec), GUIDs: rec})
	if note := raidwalk.JoinersNote(rec); note != "" {
		w.note("step %d: %s", w.pullJoinsStep, note)
		w.logLine(w.env.now(), raidwalk.PullJoinersLine(w.pullJoinsStep, rec))
	}
	w.pullJoinsAt = time.Time{}
	w.pullSeed = 0
	w.pullRow = raidwalk.PackRow{}
	w.pullPackSeeded = false
	w.pullPack = nil
	w.pullJoinsSeen = nil
}

// savedFrame is one frames.jsonl line (compact: positions, hp, dead, hostiles).
type savedFrame struct {
	T        int64          `json:"t"`
	Seq      uint64         `json:"seq"`
	Tier     int            `json:"tier"` // the walk's hire gear tier (amendment G1: every dump's meta)
	Party    []savedBot     `json:"party"`
	Hostiles []savedHostile `json:"hostiles"`
}

type savedBot struct {
	GUID   string  `json:"guid"`
	Name   string  `json:"name"`
	X      float32 `json:"x"`
	Y      float32 `json:"y"`
	Z      float32 `json:"z"`
	Map    uint32  `json:"map"`
	HP     uint32  `json:"hp"`
	MaxHP  uint32  `json:"max_hp"`
	Dead   bool    `json:"dead"`
	State  string  `json:"state"`
	Target string  `json:"target,omitempty"` // the bot's target, else its victim (absent when the module sent none)
	Spell  uint32  `json:"spell,omitempty"`  // the spell it casts or channels
	Action string  `json:"action,omitempty"` // the last action its AI ran
	Result string  `json:"result,omitempty"` // the verdict on that action (O ok, F failed, I impossible, U useless, P queued, V dropped)
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
			HP: p.HP, MaxHP: p.MaxHP, Dead: p.Dead || p.State == "d", State: p.State,
			Spell: p.Spell, Action: p.Action, Result: p.Result}
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
			Deaths: r.Deaths, Outcome: r.Outcome, BossName: r.BossName, Warnings: r.Warnings}
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
	// lines of this step (selfRegroupSeen, which also takes a note whose own page already carried the
	// run past it) instead of the last status, and only the run's state still comes from that status:
	// a paused or done last line is no self-regroup.
	now := w.env.now()
	selfRegroup := w.haveStatus && w.lastStatus.State == tbm.RunRunning && w.selfRegroupSeen(step, now)
	// The observations explained this wipe: a later wipe needs its own line since the last recovery.
	w.runLines = nil
	if w.bossEngagedNow() {
		w.note("wipe resume skipped: %s engaged", w.engagedBossWords())
		return w.endWipe(ctx, step)
	}
	if w.opts.wipeResume > 0 && w.wipeResumes >= w.opts.wipeResume {
		w.note("wipe resume skipped: no recoveries left (%d/%d)", w.wipeResumes, w.opts.wipeResume)
		return w.endWipe(ctx, step)
	}
	if w.wipeResumes >= w.opts.wipeResume { // --wipe-resume 0: the old ending, no note
		return w.endWipe(ctx, step)
	}
	if err := w.recoverWipe(ctx, step, selfRegroup); err == errWipeResumeSkipped {
		return w.endWipe(ctx, step)
	} else if terr, ok := err.(*timeoutWalkStop); ok {
		w.note("%s", terr.note)
		return stopf(terr.code, "%s", terr.reason)
	} else {
		return err
	}
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
//     (bossAttemptStart), with no boss unit of the family seen alive after that row, or the toolkit sees a corpse
//     of a boss-unit entry within bossKillYd of the boss row (TB's own search reads the live view, so a boss that
//     died out of the party's sight leaves no row at all);
//   - no boss unit is seen alive: no frame row fresher than 15 s with hp left;
//   - a hire is alive (a wipe is no kill).
//
// A stale low-hp sighting alone is NEVER a kill: a boss that evades at 20 % and leaves the frames is not a corpse.
// w.hostiles is never pruned, so a dead row alone says nothing about when it died: it counts only inside the
// current attempt, and a live sighting after it (a reset or a respawn left the boss out of view for the pause)
// voids it — then the toolkit read decides (review round 3). The returned string is the evidence for the note.
func (w *walk) bossKilled(ctx context.Context, entry uint32, now time.Time) (bool, string) {
	units := w.bossUnits(entry)
	attemptAt := w.bossAttemptStart(units)
	var alive, dead hostileState
	haveAlive, haveDead := false, false
	for _, h := range w.hostiles {
		if !inEntries(units, h.entry) {
			continue
		}
		if h.hppct == 0 {
			if attemptAt.IsZero() || h.at.Before(attemptAt) {
				continue // an earlier attempt's corpse
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
	} else if ok, s := w.bossCorpse(ctx, entry, units); ok {
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

// bossCorpse asks the toolkit whether one of the boss units lies dead near the boss row. TB's boss step wants a
// dead creature of the step's entry within 30 yd of the spot and reads the live view only, so a boss killed while
// the party was dead or away leaves no frame row at all; the toolkit's
// `q units <r> <x> <y> <z> all dead e=<entry>` reads the map's creatures, corpses included (TortoiseBots
// tools/eluna/test-toolkit tt_commands.lua Q.units: the `dead` flag asks for both, and TT.unitSnap carries
// `dead`). A refused line, no reply or an error is no evidence: the walk waits as a pause, and the first failure
// turns the reads off for the rest of the walk (a server without the toolkit must not cost the watch loop a
// timeout on every tick).
func (w *walk) bossCorpse(ctx context.Context, boss uint32, units []uint32) (bool, string) {
	if w.bossToolkitOff {
		return false, ""
	}
	spot, ok := w.bossSpot(boss)
	if !ok {
		return false, ""
	}
	readCtx, cancel := context.WithTimeout(ctx, bossCorpseWait)
	defer cancel()
	for _, e := range units {
		out, err := w.tt(readCtx, fmt.Sprintf("q units %s %s %s %s all dead e=%d", num32(bossKillYd), num32(spot.X), num32(spot.Y), num32(spot.Z), e))
		if err != nil {
			if ctx.Err() != nil {
				return false, "" // the walk itself was interrupted: no verdict, and no latch on a cancelled read
			}
			// The read's own deadline (bossCorpseWait) is a toolkit failure like any other: latch it, so a toolkit
			// that never answers costs the watch loop one bounded wait, not one per tick.
			w.bossToolkitOff = true
			w.note("boss %d: the toolkit corpse read failed (%v): the frames alone decide from here", boss, err)
			return false, ""
		}
		body, ok := ttLast(out)
		if !ok {
			w.bossToolkitOff = true
			w.note("boss %d: the toolkit corpse read gave no reply: the frames alone decide from here", boss)
			return false, ""
		}
		for _, m := range body.list("units") {
			u := ttUnitOf(m)
			if u.Entry != e || !u.Dead {
				continue
			}
			return true, fmt.Sprintf("the toolkit sees %d dead %s yd from the boss row", e, num32(u.Dist))
		}
	}
	return false, ""
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
)

// runFightsBoss says whether the run leader itself fights the engaged boss: true when the run's
// own status kind is boss and its until goes past that step (walk 12: Jeklik engaged at step 40
// with --until "step 41", and raid-walk stopped the run from under the fight); or, for the walk-24
// early rule, when the run is on a step BEFORE its next boss step in the SAME leg, its until goes
// past that boss step, and the engaged entry belongs to that boss (its entry, phase entries,
// first adds or hold entries): plans whose approach point sits inside the pull (Thekal's
// boss.approach[0] 19.9 yd from him, walk 24) would otherwise stop the run under the fight.
//
// early is the boss step the early rule fired for (0: the status kind was boss). False when
// --until boss (a trash walk that wakes a boss never fights one), when the run is not running
// (a boss that engages while the run is paused on its boss step still stops the walk), when the
// rows are not the server's (step numbers mean nothing), when the next boss step is in another
// leg, when the until does not go past the boss step, when the run stands more than
// earlyEngageSteps steps before that boss step, or when the main tank is not within earlyEngageYd
// of the boss row (review round 1, item 3).
func (w *walk) runFightsBoss(entry uint32) (ok bool, early int) {
	if w.opts.until == "boss" || !w.haveStatus || w.lastStatus.State != tbm.RunRunning {
		return false, 0
	}
	if w.lastStatus.Kind == "boss" {
		return true, 0
	}
	if w.lastStatus.Steps != len(w.rows.Steps) {
		return false, 0 // rows not the server's: step numbers mean nothing
	}
	b := nextBossStep(w.rows, w.lastStatus.Step)
	if b == 0 {
		return false, 0
	}
	switch {
	case w.untilStep == 0 && len(w.untilWords) == 0:
		// --until none: the run walks to the plan's end, past every boss step.
	case w.untilStep > b:
		// the until goes past the boss step (the server stops BEFORE step N, so N must be > b)
	default:
		return false, 0
	}
	if w.lastStatus.Step < 1 || w.lastStatus.Step > len(w.rows.Steps) {
		return false, 0
	}
	if w.rows.Steps[w.lastStatus.Step-1].Leg != w.rows.Steps[b-1].Leg {
		return false, 0 // another leg's boss woken from this step still stops
	}
	if !w.bossFamilyWithFirst(w.rows.Steps[b-1].Entry)[entry] {
		return false, 0
	}
	// The hand-over is for a boss pulled from the raid's own approach: further back than earlyEngageSteps the
	// boss is woken by something else (another leg's pull, a patrol), and a main tank far from the boss row means
	// the party is not standing at the pull at all (review round 1, item 3: walk 24's Thekal approach point sits
	// inside the pull, so the step-back case is the common one).
	if b-w.lastStatus.Step > earlyEngageSteps {
		return false, 0
	}
	lead, ok := w.botAt[w.mt]
	if !ok || w.env.now().Sub(lead.at) > earlyEngageFresh {
		return false, 0
	}
	row := w.rows.Steps[b-1]
	if math.Hypot(float64(lead.x-row.X), float64(lead.y-row.Y)) > earlyEngageYd {
		return false, 0
	}
	return true, b
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
// 60 s is at 100 % or gone.
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

// resetReady is the reset condition, held for 20 s.
func (w *walk) resetReady(now time.Time) bool {
	clear := true
	for _, h := range w.hostiles {
		if now.Sub(h.at) > 60*time.Second {
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

// revive is the walk-4 recipe's ending: revive and always end the walk with exit 19.
func (w *walk) revive(ctx context.Context, step int) error {
	// false: this is the boss-wipe ending, with the walk ending here anyway and no run regrouping itself.
	if err := w.reviveHires(ctx, time.Time{}, false); err != nil {
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
func (w *walk) reviveHires(ctx context.Context, deadline time.Time, selfRegroup bool) error {
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
	ok, why, terr := w.quietCheck(ctx, p, deadline)
	if terr != nil {
		return terr
	}
	if !ok && w.place == nil {
		// A door-to-end walk falls back to the entrance; a placed one does not: the entrance is behind the legs the
		// placement skipped, and the run leader's walk back would cross them (spec 6).
		e := w.plan.Entrance
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
			if werr := w.waitRevivePoint(ctx, e, from, eSeq, eHave, deadline); werr == nil {
				ok, why, terr = w.quietCheck(ctx, e, deadline)
				if terr != nil {
					return terr
				}
			} else if timeout, isTimeout := werr.(*recoveryTimeout); isTimeout {
				return timeout
			}
		}
	}
	if !ok {
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
	if err := w.recoverySleep(ctx, 10*time.Second, deadline); err != nil {
		return err
	}
	for _, name := range w.hireList() {
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
	return nil
}

// recoverWipe revives the dead raid after a wipe, rests it out of combat and resumes the run instead of ending the
// walk (walk 13 s7: the hand-revived, rested run went on). It returns errWipeRecovered when the run is running
// again: the watch loop keeps going and the summary's end stays truthful. Any failure ends the walk with the
// failure's own reason, exactly as endWipe would. Review round 2: the boss evidence must be fresh, the rest
// needs a complete quiet view, and the whole recovery has an absolute deadline.
func (w *walk) recoverWipe(ctx context.Context, step int, selfRegroup bool) error {
	if err := w.bossUnknown(ctx); err != nil {
		return err
	}
	// The recovery ends the boss attempt that wiped: nothing the frames showed before it bounds the next
	// attempt's kill evidence (review round 3).
	clear(w.bossAttemptAt)
	from := w.env.now()
	deadline := recoveryDeadline(from, w.opts.wipeRest, w.opts.maxRunMinutes, w.started)
	if err := w.reviveHires(ctx, deadline, selfRegroup); err != nil {
		if timeout, ok := err.(*recoveryTimeout); ok {
			return timeout.end(step)
		}
		return err
	}
	revived := len(w.hireList())
	if selfRegroup {
		// The run is running and regroups by itself: it rests, gathers and walks back on its own.
		// No out-of-combat rest and no run resume (walk 17b: the rest never got quiet while the
		// run marched on). The post-revive boss re-check still runs; a boss it names ends the walk.
		if _, err := w.freshWipeRest(); err != nil {
			return err
		}
		w.resumeStatusSeq = w.statusSeq // recoveredBoss's run-leader exception needs a status after this point
		if err := w.recoveredBoss(ctx, step); err != nil {
			return err
		}
		w.wipeResumes++
		w.note("wipe-resume %d/%d at step %d: revived %d; the run regroups by itself (no rest, no resume)", w.wipeResumes, w.opts.wipeResume, step, revived)
		return errWipeRecovered
	}
	rest, err := w.freshWipeRest()
	if err != nil {
		return err
	}
	seq0 := w.statusSeq
	if err := w.restOutOfCombat(ctx, step, rest, deadline); err != nil {
		if timeout, ok := err.(*recoveryTimeout); ok {
			return timeout.end(step)
		}
		if err == errRunWentOn {
			// The run went on by itself while the walk rested (defence in depth: usually the
			// self-regroup note decided this before the rest). Skip the resume and count it.
			w.resumeStatusSeq = seq0
			if rerr := w.recoveredBoss(ctx, step); rerr != nil {
				return rerr
			}
			w.wipeResumes++
			w.note("wipe-resume %d/%d at step %d: revived %d; the run went on by itself during the rest (no resume)", w.wipeResumes, w.opts.wipeResume, step, revived)
			return errWipeRecovered
		}
		return err
	}
	if err := w.resumeRun(ctx, step, from, rest); err != nil {
		if timeout, ok := err.(*recoveryTimeout); ok {
			return timeout.end(step)
		}
		return err
	}
	if err := w.recoveredBoss(ctx, step); err != nil {
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
	from := w.env.now()
	deadline := recoveryDeadline(from, w.opts.wipeRest, w.opts.maxRunMinutes, w.started)
	revived, err := w.recoverNearWipeRevive(ctx, deadline)
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
			w.resumeStatusSeq = seq0
			if rerr := w.recoveredBoss(ctx, step); rerr != nil {
				return rerr
			}
			w.wipeResumes++
			w.note("wipe-resume %d/%d at step %d: revived %d; the run went on by itself during the rest (no resume)", w.wipeResumes, w.opts.wipeResume, step, revived)
			return errWipeRecovered
		}
		return err
	}
	if err := w.resumeRun(ctx, step, from, rest); err != nil {
		if timeout, ok := err.(*recoveryTimeout); ok {
			return timeout.pausedEnd(step)
		}
		return err
	}
	if err := w.recoveredBoss(ctx, step); err != nil {
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
func (w *walk) recoverNearWipeRevive(ctx context.Context, deadline time.Time) (int, error) {
	if w.allDead(w.env.now()) {
		// false: a near-wipe stop is a PAUSED run (never a self-regroup), so the old summon recipe stands.
		if err := w.reviveHires(ctx, deadline, false); err != nil {
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
	if !w.bossLatchAt.IsZero() && now.Sub(w.bossLatchAt) < 120*time.Second {
		w.note("wipe resume skipped: boss state unknown (%s)", w.latchWords(now))
		return errWipeResumeSkipped
	}
	if w.dash == nil {
		return nil
	}
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
// including its run-leader-fights-its-boss exception.
func (w *walk) recoveredBoss(ctx context.Context, step int) error {
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
	if w.statusSeq > w.resumeStatusSeq {
		if ok, early := w.runFightsBoss(entry); ok {
			if early > 0 {
				w.note("boss %d engaged at step %d, before its boss step %d in the same leg, and the until (%s) goes past it, so the run leader fights it", entry, step, early, w.untilLabel)
			} else {
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

// selfRegroupSeen says a running status line recorded since the last recovery reports the run
// regrouping by itself: its note arrived within selfRegroupWindow of now and either it is the line of
// this step — pollOnce keeps every running line of a page, so the note still counts when a later line
// of the same page ("regroup: waiting ... reviving", the run's own "regrouped") is the last status the
// wipe fires on (walk 18 r5) — or its own page carried the run on to this step: the note's page holds
// the note and the later line that moved the step, so the wipe explains the note (walk 19 r6).
func (w *walk) selfRegroupSeen(step int, now time.Time) bool {
	for i := len(w.runLines) - 1; i >= 0; i-- {
		o := w.runLines[i]
		if !selfRegroupNote(o.note) || now.Sub(o.at) > selfRegroupWindow {
			continue
		}
		if o.step == step || (o.seq > w.pollSeq0 && step > o.step) {
			return true
		}
	}
	return false
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

// quietCheck is the revive point's quiet check (spec 3.5): every 5 s up to 120 s, with a patrol exempt when one
// of its members is visible 80 yd out and its distance grew between two checks. 200 yd asks for the far members;
// raidwalk.QuietAt still enforces its own 45/80 yd rules. The deadline is the recovery's
// entry deadline (zero: the old unbounded path): every loop turn and sleep checks it, and
// past it the check fails with the timed-out recovery instead of true/false.
func (w *walk) quietCheck(ctx context.Context, p dungeon.Point, deadline time.Time) (bool, string, *recoveryTimeout) {
	prev := map[string]float64{}
	for i := range 24 {
		if timeout := w.recoveryExpired(deadline); timeout != nil {
			return false, "", timeout
		}
		us, err := w.unitsAt(ctx, 200)
		if err != nil {
			return false, "units: " + err.Error(), nil
		}
		patrols := make([]raidwalk.QuietPatrol, len(w.quietPatrols))
		copy(patrols, w.quietPatrols)
		for j := range patrols {
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
				delete(prev, patrols[j].ID)
				continue
			}
			if old, ok := prev[patrols[j].ID]; ok && best >= 80 && best > old {
				patrols[j].Exempt = true
			}
			prev[patrols[j].ID] = best
		}
		ok, why := raidwalk.QuietAt(us, patrols, p)
		if ok {
			return true, "", nil
		}
		if i == 23 {
			return false, why, nil
		}
		if err := w.recoverySleep(ctx, 5*time.Second, deadline); err != nil {
			if timeout, ok := err.(*recoveryTimeout); ok {
				return false, "", timeout
			}
			return false, "interrupted", nil
		}
	}
	return false, "the units never went quiet", nil
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
