// raid_walk_phases.go: phases 2-13 of `turtle-agent raid-walk` (spec 3.3). Every phase takes the walk and ends
// either nil or a *walkStop; the summary and teardown run in stop/done so no path skips them.
package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"math"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/pjerra/turtle-agent/internal/dungeon"
	"github.com/pjerra/turtle-agent/internal/events"
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
	if err := w.phaseHire(ctx); err != nil {
		return w.stop(err, true)
	}
	if err := w.phaseBind(ctx); err != nil {
		return w.stop(err, true)
	}
	if err := w.phaseEnter(ctx); err != nil {
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
	return w.waitMap(ctx, p.Map, 30*time.Second)
}

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
		hired, okLine := parseHireLine(line)
		if !okLine {
			if hired == 0 {
				w.note("check the commander's gold")
			}
			return stopf(exitRaidWalkFailed, "hire %s: %s", b.Hire, line)
		}
		want := len(before) + hired
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

// parseHireLine reads TBM:HIRE_RAID|<ok|partial|refused>|<hired>|...; ok is false unless the first field is ok
// and the hired count is above 0, and hired is always the count it read.
func parseHireLine(line string) (hired int, ok bool) {
	rest, found := strings.CutPrefix(raidwalk.CleanLine(line), "TBM:HIRE_RAID|")
	if !found {
		return 0, false
	}
	f := strings.Split(rest, "|")
	if len(f) < 2 {
		return 0, false
	}
	n, err := strconv.Atoi(strings.TrimSpace(f[1]))
	if err != nil {
		return 0, false
	}
	return n, f[0] == "ok" && n > 0
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

// enterNear sends the module's own entry order; the ERR code is returned so auto can fall back.
func (w *walk) enterNear(ctx context.Context) (string, error) {
	rep, err := w.commander(ctx, commanderReq{Op: "cmd", Verb: "enter", Args: []string{"near"}, WaitMS: 15000})
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
	w.lastStatus, w.haveStatus = st, true
	w.noteLabel(st)
	if st.State != tbm.RunRunning {
		return stopf(exitRaidWalkFailed, "run start: the run is %s: %s", st.State, line)
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
				return w.handleWipe(ctx, v.Step)
			case raidwalk.IncidentBossEngaged:
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
				ps.armed, ps.at, ps.note, ps.step, ps.lastTry = true, now, w.lastStatus.Note, w.lastStatus.Step, now
				w.logLine(now, "paused: "+w.lastStatus.Note)
				if w.dash != nil {
					w.saveCommanders("pause")
				}
			}
			regroup := strings.Contains(ps.note, "did not hold")
			switch {
			case regroup && perStep[ps.step] < w.opts.autoResume && autoTotal < 6 && now.Sub(ps.lastTry) >= 15*time.Second:
				if _, err := w.commander(ctx, commanderReq{Op: "run", Verb: "resume", WaitMS: 5000}); err != nil {
					return err
				}
				perStep[ps.step]++
				autoTotal++
				ps.lastTry = now
				w.note("auto-resumed the regroup at step %d (%d/%d)", ps.step, perStep[ps.step], w.opts.autoResume)
			case now.Sub(ps.at) >= w.opts.pausedWait:
				return stopf(exitRaidWalkFailed, "paused: %s", ps.note)
			}
		} else {
			ps.armed = false
		}
		if end := w.tracker.End(); end.Kind != raidwalk.EndRunning {
			w.endReason = end.Reason()
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
		w.lastStatus, w.haveStatus = st, true
		w.noteLabel(st)
		if st.Note != "" {
			w.newNotes = append(w.newNotes, st.Note)
		}
	}
	if n, frame, err := w.view(ctx, w.afterN, 2000); err == nil && frame != nil {
		w.afterN = n
		w.trackFrame(now, frame)
		w.tracker.Frame(now, *frame)
		w.saveFrame(now, frame)
	}
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

// waitMap waits until the agent reports mapID.
func (w *walk) waitMap(ctx context.Context, mapID uint32, timeout time.Duration) error {
	deadline := w.env.now().Add(timeout)
	for {
		if err := ctx.Err(); err != nil {
			return stopf(exitRaidWalkFailed, "interrupted")
		}
		st, err := w.status(ctx)
		if err == nil && st.Map == mapID {
			return nil
		}
		if !w.env.now().Before(deadline) {
			return stopf(exitRaidWalkFailed, "the commander is not on map %d after %s", mapID, durWords(timeout))
		}
		if err := w.env.sleep(ctx, time.Second); err != nil {
			return stopf(exitRaidWalkFailed, "interrupted")
		}
	}
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
		if w.bossSet[h.Entry] {
			w.bossSeenAt = now
			w.bossRows[h.Entry] = hostileState{entry: h.Entry, x: h.X, y: h.Y, z: h.Z, kind: h.Kind, reaction: h.Reaction, flags: h.Flags,
				hppct: h.HPPct, target: uint64(h.Target), at: now}
			if h.Target != 0 {
				w.bossTargetAt = now
			}
		}
	}
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
	GUID  string  `json:"guid"`
	Name  string  `json:"name"`
	X     float32 `json:"x"`
	Y     float32 `json:"y"`
	Z     float32 `json:"z"`
	Map   uint32  `json:"map"`
	HP    uint32  `json:"hp"`
	MaxHP uint32  `json:"max_hp"`
	Dead  bool    `json:"dead"`
	State string  `json:"state"`
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

// saveFrame writes one frame per 10 s.
func (w *walk) saveFrame(now time.Time, f *tbm.ViewFrame) {
	if w.framesF == nil {
		return
	}
	if !w.lastFrameSave.IsZero() && now.Sub(w.lastFrameSave) < 10*time.Second {
		return
	}
	w.lastFrameSave = now
	rec := savedFrame{T: now.UnixMilli(), Seq: f.Seq, Tier: w.roster.Tier}
	for _, p := range f.Party {
		rec.Party = append(rec.Party, savedBot{GUID: p.GUID.String(), Name: p.Name, X: p.X, Y: p.Y, Z: p.Z, Map: p.Map,
			HP: p.HP, MaxHP: p.MaxHP, Dead: p.Dead || p.State == "d", State: p.State})
	}
	for _, h := range f.Hostiles {
		rec.Hostiles = append(rec.Hostiles, savedHostile{GUID: h.GUID.String(), Entry: h.Entry, X: h.X, Y: h.Y, Z: h.Z,
			HPPct: h.HPPct, Target: h.Target.String(), Flags: h.Flags})
	}
	raw, err := json.Marshal(rec)
	if err != nil {
		return
	}
	_, _ = w.framesF.Write(append(raw, '\n'))
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

// saveFights saves the fights list and one detail file per new key.
func (w *walk) saveFights() {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	raw, err := w.dash.Fights(ctx, w.manifest.Character)
	if err != nil {
		if !w.dashWarned {
			w.note("dashboard: %v", err)
			w.dashWarned = true
		}
		return
	}
	_ = w.writeOut("combat-fights.json", raw)
	var rows []walkFightRow
	if json.Unmarshal(raw, &rows) != nil {
		return
	}
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

// wipe handling (spec 3.5): stop, wait for a reset, revive only when quiet.

// handleWipe is an all-dead party with no boss engaged.
func (w *walk) handleWipe(ctx context.Context, step int) error {
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

// revive is the walk-4 recipe, made safe (spec 3.5): the revive point, the quiet check (then the entrance once),
// summon and revive, and a look for bots attacked after the revive. It always ends the walk with exit 19.
func (w *walk) revive(ctx context.Context, step int) error {
	if w.stepFirst == nil {
		return stopf(exitRaidWalkIncident, "not revived: the plan's rows give no first point")
	}
	p := *w.stepFirst
	if err := w.gm(ctx, fmt.Sprintf(".go xyz %s %s %s %d", num32(p.X), num32(p.Y), num32(p.Z), w.plan.Map)); err != nil {
		return err
	}
	if err := w.waitMap(ctx, w.plan.Map, 30*time.Second); err != nil {
		return stopf(exitRaidWalkIncident, "not revived: the commander did not reach map %d", w.plan.Map)
	}
	ok, why := w.quietCheck(ctx, p)
	if !ok {
		e := w.plan.Entrance
		if err := w.gm(ctx, fmt.Sprintf(".go xyz %s %s %s %d", num32(e.X), num32(e.Y), num32(e.Z), w.plan.Map)); err == nil {
			if err := w.waitMap(ctx, w.plan.Map, 30*time.Second); err == nil {
				ok, why = w.quietCheck(ctx, e)
			}
		}
	}
	if !ok {
		return stopf(exitRaidWalkIncident, "not revived: %s", why)
	}
	for _, name := range w.hireList() {
		if err := w.gm(ctx, ".summon "+name); err != nil {
			return err
		}
		if err := w.env.sleep(ctx, 100*time.Millisecond); err != nil {
			return stopf(exitRaidWalkFailed, "interrupted")
		}
	}
	if err := w.env.sleep(ctx, 10*time.Second); err != nil {
		return stopf(exitRaidWalkFailed, "interrupted")
	}
	for _, name := range w.hireList() {
		if err := w.gm(ctx, ".revive "+name); err != nil {
			return err
		}
		if err := w.env.sleep(ctx, 100*time.Millisecond); err != nil {
			return stopf(exitRaidWalkFailed, "interrupted")
		}
	}
	if err := w.env.sleep(ctx, 15*time.Second); err != nil {
		return stopf(exitRaidWalkFailed, "interrupted")
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
	return stopf(exitRaidWalkIncident, "wipe at step %d", step)
}

// quietCheck is the revive point's quiet check (spec 3.5): every 5 s up to 120 s, with a patrol exempt when one
// of its members is visible 80 yd out and its distance grew between two checks. 200 yd asks for the far members;
// raidwalk.QuietAt still enforces its own 45/80 yd rules.
func (w *walk) quietCheck(ctx context.Context, p dungeon.Point) (bool, string) {
	prev := map[string]float64{}
	for i := range 24 {
		us, err := w.unitsAt(ctx, 200)
		if err != nil {
			return false, "units: " + err.Error()
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
			return true, ""
		}
		if i == 23 {
			return false, why
		}
		if err := w.env.sleep(ctx, 5*time.Second); err != nil {
			return false, "interrupted"
		}
	}
	return false, "the units never went quiet"
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
	var buf bytes.Buffer
	code := w.env.teardown([]string{"--json", w.slotDir}, &buf, &buf)
	if err := w.writeOut("teardown.json", buf.Bytes()); err != nil {
		w.note("teardown.json: %v", err)
	}
	t, ok := parseTeardownJSON(buf.Bytes())
	w.teardownWd.Failed = !ok || code != exitOK || t.Failed > 0
	if !ok {
		w.note("teardown: the report did not parse")
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
