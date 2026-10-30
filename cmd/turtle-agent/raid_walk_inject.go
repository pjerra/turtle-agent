// raid_walk_inject.go: the command side of `raid-walk --inject` (spec ~/TortoiseBots-audit/specs/raid-walk-addons.md
// 1.6, task I2). The injector's decisions are pure (internal/raidwalk/inject.go, I1); this file feeds it the watch's
// observations and executes what it asks, through the fault allow-lists (inject_allow.go).
//
// Live facts from the P0 probe (2026-10-29, tests/raid-inject-probe-20261029-042517/probe.md):
//   - `.go xyz` puts the COMMANDER into another instance copy than the bots, so Prepare does not check the commander's
//     own units. Every line that acts on a bot goes by GUID selection, which works across copies, and `.summon <bot>`
//     lands the bot at the commander's coordinates inside the bot's own copy.
//   - `.freeze`/`.unfreeze` answer no chat line: the injector measures by the bots' positions in bot_view.
//   - a `target` RPC's read-back in `status.target` does not prove the server has the selection: after the target the
//     walk waits 2 s, reads `.gps` (the SELECTED unit's coordinates) and compares it with the target's known position
//     within 3 yd; a mismatch is retried once, then the fault ends inconclusive. Nothing that acts on the selection
//     goes out before that check.
//   - coordinates alone cannot prove a selection: a stale selection of a unit standing next to the intended one passes
//     the 3 yd check. `.die` (which acts on the selection) therefore also needs `.pinfo`'s identity (review ad-i2-r2),
//     and a `.freeze` goes out only for a target with no other unit within 6 yd of it.
package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/pjerra/turtle-agent/internal/dungeon"
	"github.com/pjerra/turtle-agent/internal/events"
	"github.com/pjerra/turtle-agent/internal/objects"
	"github.com/pjerra/turtle-agent/internal/packet"
	"github.com/pjerra/turtle-agent/internal/raidwalk"
	"github.com/pjerra/turtle-agent/internal/session"
	"github.com/pjerra/turtle-agent/internal/tbm"
)

func init() { newWalkInjector = newRaidInjector }

const (
	// injectSelectSettle is the wait between a target RPC and the `.gps` read-back (P0 finding 4: 2 s was enough; a
	// selection sent right before a GM line is not in force yet).
	injectSelectSettle = 2 * time.Second
	// injectPrepareWait is how long Prepare waits for the hires' first bot_view positions.
	injectPrepareWait = 20 * time.Second
	// injectGPSWait bounds one `.gps` reply.
	injectGPSWait = 3 * time.Second
	// injectPInfoWait bounds one `.pinfo` reply (the core answers it from asynchronous DB queries).
	injectPInfoWait = 10 * time.Second
	// injectCleanupDeadline is how long Cleanup keeps trying to release the bots it froze.
	injectCleanupDeadline = 60 * time.Second
	// injectCleanupRetry is the pause between cleanup attempts.
	injectCleanupRetry = 3 * time.Second
	// injectCleanupAttempt bounds one cleanup attempt; each attempt gets a fresh context.
	injectCleanupAttempt = 30 * time.Second
	// injectBotFresh and injectHostileFresh are the observation ages the injector's measurements take (spec 1.3).
	injectBotFresh     = 10 * time.Second
	injectHostileFresh = 4 * time.Second
)

// errInjectCleanupDeadline ends a wait at Cleanup's recovery deadline: the attempt has no time left.
var errInjectCleanupDeadline = errors.New("cleanup deadline reached")

// injectFile is <out>/inject.json, format tb-raid-inject v1 (spec 1.6).
type injectFile struct {
	Format    string                    `json:"format"`
	Version   int                       `json:"version"`
	Plan      string                    `json:"plan"`
	Map       uint32                    `json:"map"`
	Tier      int                       `json:"tier"`
	Faults    []raidwalk.InjectResult   `json:"faults"`
	Incidents []raidwalk.InjectIncident `json:"incidents,omitempty"`
}

// raidInjector is the walkInjector of one walk's --inject specs.
type raidInjector struct {
	w     *walk
	specs []raidwalk.InjectSpec

	in       *raidwalk.Injector // built in Prepare, when the roster's names and the layout are known
	prepFail string             // Prepare failed: every fault is inconclusive with this reason

	finalized   bool
	results     []raidwalk.InjectResult
	failed      bool
	incidents   []raidwalk.InjectIncident
	blocking    bool
	cleanupDone bool

	cleanupUntil time.Time // Cleanup's recovery deadline: its attempts and waits never run past it
}

// newRaidInjector parses the --inject specs (spec 1.1); the roster refuses kill-mt without an ot batch.
func newRaidInjector(w *walk, specs []string) (walkInjector, error) {
	parsed, err := raidwalk.ParseInjects(specs, w.roster)
	if err != nil {
		return nil, err
	}
	return &raidInjector{w: w, specs: parsed}, nil
}

// Prepare is the injector's one-time placement (spec 1.6): `.gm visible off`, the commander to step 1 in the raid,
// then the hires' positions from bot_view. P0 finding 1: the commander's own units do not prove the bots' copy, so the
// readiness check reads the walk's frames, not the commander's `units`; the lines that follow work across copies.
func (r *raidInjector) Prepare(ctx context.Context) error {
	w := r.w
	names := raidwalk.RosterNames{
		MT:    w.mt,
		OT:    append([]string{}, w.ots...),
		Rest:  append([]string{}, w.rest...),
		Tanks: append([]string{}, w.layout.Tanks...),
	}
	r.in = raidwalk.NewInjector(r.specs, names, w.rows, w.plan)
	c := r.in.Ctx()
	if !raidwalk.AllowedInjectLine(".gm visible off", c) {
		return stopf(exitRaidWalkFailed, "raid-walk refused to send a GM line outside the allow-list: .gm visible off")
	}
	if err := r.sendGM(ctx, ".gm visible off"); err != nil {
		r.prepFail = "the GM console did not take .gm visible off: " + err.Error()
		return nil // the walk goes on; every fault is inconclusive (spec 1.6)
	}
	if w.stepFirst == nil {
		r.prepFail = "the plan's rows give no positioned step 1 point"
		return nil
	}
	p := *w.stepFirst
	line := fmt.Sprintf(".go xyz %s %s %s %d", num32(p.X), num32(p.Y), num32(p.Z), w.plan.Map)
	if !raidwalk.AllowedInjectLine(line, c) {
		return stopf(exitRaidWalkFailed, "raid-walk refused to send a GM line outside the allow-list: %s", line)
	}
	if err := r.sendGM(ctx, line); err != nil {
		r.prepFail = "the commander did not move to the raid's map: " + err.Error()
		return nil
	}
	if err := w.waitMap(ctx, w.plan.Map, 30*time.Second); err != nil {
		if ctx.Err() != nil {
			return err
		}
		r.prepFail = "the commander did not reach the raid's map"
		return nil
	}
	start := w.env.now()
	deadline := start.Add(injectPrepareWait)
	for {
		if n, frame, err := w.view(ctx, w.afterN, 2000); err == nil && frame != nil {
			w.afterN = n
			w.trackFrame(w.env.now(), frame)
		}
		now := w.env.now()
		if b, ok := w.botAt[w.mt]; ok && b.guid != 0 && b.at.After(start) && now.Sub(b.at) <= injectBotFresh {
			return nil
		}
		if !now.Before(deadline) {
			r.prepFail = "the injector has no bot positions from bot_view (no party record for the lead bot)"
			return nil
		}
		if err := w.env.sleep(ctx, 2*time.Second); err != nil {
			return err
		}
	}
}

// Tick is one watch tick (spec 1.6): build the observation, ask the injector, execute its actions in order.
func (r *raidInjector) Tick(ctx context.Context, now time.Time) error {
	w := r.w
	notes := w.newNotes
	w.newNotes = nil
	if r.in == nil || r.prepFail != "" {
		return nil
	}
	return r.exec(ctx, r.in.Tick(r.observe(now, notes, false)))
}

// exec runs the actions in order. A refused allow-list line is a programming error (exit 18); any other failure ends
// its fault inconclusive (Ack with the error), never the walk.
func (r *raidInjector) exec(ctx context.Context, acts []raidwalk.InjectAction) error {
	for i, a := range acts {
		got, aerr, fatal := r.execOne(ctx, a)
		if fatal != nil {
			return fatal
		}
		if a.Kind == raidwalk.ActWait {
			continue
		}
		if aerr != nil {
			r.in.Ack(a, r.in.Ctx(), aerr)
			if a.Kind == raidwalk.ActSelect || a.Kind == raidwalk.ActSelectSelf {
				// The lines that need this selection are not sent: the rest of this fault's actions end with it.
				for _, b := range acts[i+1:] {
					if b.Kind != raidwalk.ActWait {
						r.in.Ack(b, r.in.Ctx(), aerr)
					}
				}
				return nil
			}
			continue
		}
		r.in.Ack(a, got, nil)
	}
	return nil
}

// execOne performs one action. got is the context to acknowledge it with (a verified selection for selects); aerr is
// an action failure (the fault turns inconclusive); fatal is a walk stop (a refused allow-list line or a ctx end).
func (r *raidInjector) execOne(ctx context.Context, a raidwalk.InjectAction) (raidwalk.InjectCtx, error, error) {
	w := r.w
	switch a.Kind {
	case raidwalk.ActWait:
		if a.For > 0 {
			if err := w.env.sleep(ctx, a.For); err != nil {
				return r.in.Ctx(), err, nil
			}
		}
		return r.in.Ctx(), nil, nil
	case raidwalk.ActGM:
		if !raidwalk.AllowedInjectLine(a.Line, r.in.Ctx()) {
			return r.in.Ctx(), nil, stopf(exitRaidWalkFailed, "raid-walk refused to send a GM line outside the allow-list: %s", a.Line)
		}
		return r.in.Ctx(), r.sendGM(ctx, a.Line), nil
	case raidwalk.ActOrder:
		if !raidwalk.AllowedInjectOrder(a.Verb, a.Args, r.in.Ctx()) {
			return r.in.Ctx(), nil, stopf(exitRaidWalkFailed, "raid-walk refused to send a commander order outside the allow-list: %s %s", a.Verb, strings.Join(a.Args, " "))
		}
		return r.in.Ctx(), r.sendOrder(ctx, a), nil
	case raidwalk.ActSelect, raidwalk.ActSelectSelf:
		return r.selectUnit(ctx, a)
	}
	return r.in.Ctx(), fmt.Errorf("unknown injector action %q", a.Kind), nil
}

// sendGM sends one injector line without raid-walk's own allow-list (the caller checked the fault allow-list).
func (r *raidInjector) sendGM(ctx context.Context, line string) error {
	w := r.w
	w.logLine(w.env.now(), "> "+line)
	if err := w.agent.Call(ctx, "gm", map[string]string{"text": line}, nil, 10*time.Second); err != nil {
		return fmt.Errorf("gm %s: %v", line, err)
	}
	return nil
}

// sendOrder is add-pack's commander order; an ACTION_ACK is required (spec 1.3).
func (r *raidInjector) sendOrder(ctx context.Context, a raidwalk.InjectAction) error {
	rep, err := r.w.commander(ctx, commanderReq{Op: "cmd", Verb: a.Verb, Args: a.Args, WaitMS: 5000})
	if err != nil {
		return fmt.Errorf("cmd %s: %v", a.Verb, err)
	}
	if rep.Reply.Err != nil {
		return fmt.Errorf("cmd %s: %s (%s)", a.Verb, rep.Reply.Err.Message, rep.Reply.Err.Code)
	}
	if !rep.Reply.OK && rep.Reply.Kind != tbm.KindAck {
		return fmt.Errorf("cmd %s: no ACK (%s)", a.Verb, rep.Reply.Line)
	}
	return nil
}

// selectUnit selects the action's unit and proves the selection names the intended unit before anything acts on it
// (P0 finding 4, review ad-i2-r2, ad-i2-r3): target, status.target read-back, a 2 s settle, `.pinfo` with no name.
// The selected player must be the intended unit with its exact guid counter: the hire for a `select` (the victim
// `.die`, `.freeze` and `.unfreeze` act on), the commander for the self selection a `.freeze`/`.unfreeze`'s name
// applies to. Coordinates cannot prove a selection (a stale neighbour standing close passes them), so `.gps` is only
// an extra log line; a `.pinfo` mismatch is terminal: the fault ends "selection not proven" with no retry. The target
// and status.target read-backs keep their one retry for a lagging server.
func (r *raidInjector) selectUnit(ctx context.Context, a raidwalk.InjectAction) (raidwalk.InjectCtx, error, error) {
	w := r.w
	self := a.Kind == raidwalk.ActSelectSelf
	guid := a.GUID
	st, err := w.status(ctx)
	if err != nil {
		return r.in.Ctx(), fmt.Errorf("selection not confirmed: status: %v", err), nil
	}
	if self {
		guid = st.GUID
		if guid == 0 {
			return r.in.Ctx(), errors.New("selection not confirmed: the commander has no GUID"), nil
		}
	}
	label := r.hireName(guid)
	wantName := label
	if self {
		label = "the commander"
		wantName = st.Character
	}
	if label == "" {
		label = guid.String()
	}
	wantCounter := uint64(uint32(guid))
	var last error
	for range 2 {
		var u objects.Unit
		if err := w.agent.Call(ctx, "target", map[string]string{"guid": guid.String()}, &u, 10*time.Second); err != nil {
			last = fmt.Errorf("selection not confirmed: target %s: %v", guid, err)
			continue
		}
		st, err = w.status(ctx)
		if err != nil {
			last = fmt.Errorf("selection not confirmed: status: %v", err)
			continue
		}
		if st.Target != guid {
			last = fmt.Errorf("selection not confirmed: status.target is %s, not %s", st.Target, guid)
			continue
		}
		if err := r.sleep(ctx, injectSelectSettle); err != nil {
			return r.in.Ctx(), err, nil
		}
		// The coordinates are an observation only: they cannot tell a stale nearby selection from the intended unit.
		pos, have, err := r.gpsSelected(ctx)
		switch {
		case err != nil && (errors.Is(err, errInjectCleanupDeadline) || ctx.Err() != nil):
			return r.in.Ctx(), err, nil
		case err != nil:
			w.logLine(w.env.now(), fmt.Sprintf("note: .gps after selecting %s: %v", label, err))
		case !have:
			w.logLine(w.env.now(), "note: .gps after selecting "+label+": no coordinates")
		default:
			if want, known := r.knownPos(guid, self, st); known {
				w.logLine(w.env.now(), fmt.Sprintf("note: .gps after selecting %s: %.1f yd from its known position", label, injectDist3(pos, want)))
			} else {
				w.logLine(w.env.now(), "note: .gps after selecting "+label+": known position unknown")
			}
		}
		// The identity is the proof every line that acts on the selection needs.
		name, counter, ok, err := r.pinfoSelected(ctx)
		if err != nil {
			return r.in.Ctx(), fmt.Errorf("selection not proven: %v", err), nil
		}
		if !ok {
			return r.in.Ctx(), fmt.Errorf("selection not proven: .pinfo named no player, not %s", label), nil
		}
		if wantName == "" || name != wantName || counter != wantCounter {
			return r.in.Ctx(), fmt.Errorf("selection not proven: .pinfo names %q (guid %d), not %s (%d)", name, counter, label, wantCounter), nil
		}
		c := r.in.Ctx()
		c.SelfSelected = self
		c.SelectedHire = ""
		if !self {
			c.SelectedHire = r.hireName(guid)
		}
		return c, nil, nil
	}
	return r.in.Ctx(), last, nil
}

// knownPos is where the selected unit is known to stand: the commander's own status position, or the hire's newest
// frame position.
func (r *raidInjector) knownPos(g packet.GUID, self bool, st session.StatusInfo) (dungeon.Point, bool) {
	if self {
		return dungeon.Point{X: st.X, Y: st.Y, Z: st.Z}, true
	}
	for _, n := range r.w.hireList() {
		if b, ok := r.w.botAt[n]; ok && b.guid == g {
			return dungeon.Point{X: b.x, Y: b.y, Z: b.z}, true
		}
	}
	return dungeon.Point{}, false
}

// hireName is the hired name with that GUID ("" when the GUID is not a hire).
func (r *raidInjector) hireName(g packet.GUID) string {
	for _, n := range r.w.hireList() {
		if b, ok := r.w.botAt[n]; ok && b.guid == g {
			return n
		}
	}
	return ""
}

// sleep waits d, but never past Cleanup's recovery deadline: an attempt's waits must not spend time the deadline
// does not have (ad-i2-r3). Outside Cleanup it is the walk's own sleep.
func (r *raidInjector) sleep(ctx context.Context, d time.Duration) error {
	if !r.cleanupUntil.IsZero() {
		left := r.cleanupUntil.Sub(r.w.env.now())
		if left <= 0 {
			return errInjectCleanupDeadline
		}
		d = min(d, left)
	}
	return r.w.env.sleep(ctx, d)
}

// injectGPSRe matches `.gps`'s coordinates line ("X: -158.440994 Y: 131.600998 Z: -74.255203 Orientation: ...").
var injectGPSRe = regexp.MustCompile(`^X: (-?[0-9]+(?:\.[0-9]+)?) Y: (-?[0-9]+(?:\.[0-9]+)?) Z: (-?[0-9]+(?:\.[0-9]+)?)`)

// gpsSelected sends `.gps` and reads the reply from the event log: the reply reports the SELECTED unit. It consumes
// the events like pollOnce does, so no TBM:RUN line is lost.
func (r *raidInjector) gpsSelected(ctx context.Context) (dungeon.Point, bool, error) {
	w := r.w
	if r.in != nil && !raidwalk.AllowedInjectLine(".gps", r.in.Ctx()) {
		return dungeon.Point{}, false, errors.New("the fault allow-list refuses .gps")
	}
	if err := r.sendGM(ctx, ".gps"); err != nil {
		return dungeon.Point{}, false, err
	}
	deadline := w.env.now().Add(injectGPSWait)
	var pos dungeon.Point
	have := false
	for {
		var page events.Page
		params := map[string]any{"since": w.eventsAt, "kinds": []string{"system", "addon"}, "wait_ms": 1000}
		if err := w.agent.Call(ctx, "events", params, &page, 15*time.Second); err != nil {
			return dungeon.Point{}, false, fmt.Errorf(".gps events: %v", err)
		}
		w.eventsAt = page.Next
		for _, e := range page.Events {
			if line, ok := tbm.Line(e); ok {
				w.logLine(w.env.now(), line)
				if st, ok := raidwalk.ParseRun(line); ok && st.Plan == w.plan.Name {
					if w.tracker != nil {
						w.tracker.Feed(w.env.now(), st)
					}
					w.lastStatus, w.haveStatus = st, true
					if st.Note != "" {
						w.newNotes = append(w.newNotes, st.Note)
					}
				}
				continue
			}
			if e.Kind != events.KindSystem || e.Text == "" {
				continue
			}
			if p, ok := parseGPSPoint(e.Text); ok {
				pos, have = p, true
			}
			if strings.Contains(e.Text, "FloorZ") {
				return pos, have, nil
			}
		}
		if !w.env.now().Before(deadline) {
			return dungeon.Point{}, false, fmt.Errorf("no .gps reply in %s", injectGPSWait)
		}
		if err := r.sleep(ctx, 200*time.Millisecond); err != nil {
			return dungeon.Point{}, false, err
		}
	}
}

// injectPInfoGUIDRe matches the guid counter in the core's LANG_PINFO_ACCOUNT line: "%s %s%s %s (guid: %u) ...".
var injectPInfoGUIDRe = regexp.MustCompile(`\(guid: ([0-9]+)\)`)

// injectPInfoNameRe matches the character's player link in that line (ChatHandler::playerLink).
var injectPInfoNameRe = regexp.MustCompile(`\|Hplayer:([^|]+)\|h\[([^\]]+)\]\|h`)

// pinfoSelected sends `.pinfo` with no name (the core uses the selected player) and reads the first reply line of its
// LANG_PINFO_ACCOUNT message: the character's name and guid counter. It consumes events like gpsSelected does.
func (r *raidInjector) pinfoSelected(ctx context.Context) (string, uint64, bool, error) {
	w := r.w
	if r.in != nil && !raidwalk.AllowedInjectLine(".pinfo", r.in.Ctx()) {
		return "", 0, false, errors.New("the fault allow-list refuses .pinfo")
	}
	if err := r.sendGM(ctx, ".pinfo"); err != nil {
		return "", 0, false, err
	}
	deadline := w.env.now().Add(injectPInfoWait)
	for {
		var page events.Page
		params := map[string]any{"since": w.eventsAt, "kinds": []string{"system", "addon"}, "wait_ms": 1000}
		if err := w.agent.Call(ctx, "events", params, &page, 15*time.Second); err != nil {
			return "", 0, false, fmt.Errorf(".pinfo events: %v", err)
		}
		w.eventsAt = page.Next
		for _, e := range page.Events {
			if line, ok := tbm.Line(e); ok {
				w.logLine(w.env.now(), line)
				if st, ok := raidwalk.ParseRun(line); ok && st.Plan == w.plan.Name {
					if w.tracker != nil {
						w.tracker.Feed(w.env.now(), st)
					}
					w.lastStatus, w.haveStatus = st, true
					if st.Note != "" {
						w.newNotes = append(w.newNotes, st.Note)
					}
				}
				continue
			}
			if e.Kind != events.KindSystem || e.Text == "" {
				continue
			}
			// LANG_PINFO_ACCOUNT is localized, but its player link and "(guid: N)" are not: require both.
			text := raidwalk.CleanLine(e.Text)
			gm, nm := injectPInfoGUIDRe.FindStringSubmatch(text), injectPInfoNameRe.FindStringSubmatch(text)
			if gm == nil || nm == nil {
				continue
			}
			counter, err := strconv.ParseUint(gm[1], 10, 64)
			if err != nil {
				continue
			}
			return nm[2], counter, true, nil
		}
		if !w.env.now().Before(deadline) {
			return "", 0, false, fmt.Errorf("no .pinfo reply in %s", injectPInfoWait)
		}
		if err := r.sleep(ctx, 200*time.Millisecond); err != nil {
			return "", 0, false, err
		}
	}
}

// parseGPSPoint reads `.gps`'s "X: <f> Y: <f> Z: <f>" line.
func parseGPSPoint(s string) (dungeon.Point, bool) {
	m := injectGPSRe.FindStringSubmatch(raidwalk.CleanLine(s))
	if m == nil {
		return dungeon.Point{}, false
	}
	x, e1 := strconv.ParseFloat(m[1], 64)
	y, e2 := strconv.ParseFloat(m[2], 64)
	z, e3 := strconv.ParseFloat(m[3], 64)
	if e1 != nil || e2 != nil || e3 != nil {
		return dungeon.Point{}, false
	}
	return dungeon.Point{X: float32(x), Y: float32(y), Z: float32(z)}, true
}

// injectDist3 is the 3D distance the injector's measurements use.
func injectDist3(a, b dungeon.Point) float64 {
	dx, dy, dz := float64(a.X-b.X), float64(a.Y-b.Y), float64(a.Z-b.Z)
	return math.Sqrt(dx*dx + dy*dy + dz*dz)
}

// observe builds one tick's InjectObs (spec 1.5): fresh hires, fresh hostiles, the run status, the notes, the until
// step, the boss and the wipe flag.
func (r *raidInjector) observe(now time.Time, notes []string, wipe bool) raidwalk.InjectObs {
	w := r.w
	o := raidwalk.InjectObs{Now: now, Notes: notes, UntilStep: w.untilStep, Lead: w.mt, Wipe: wipe}
	o.Run, o.HaveRun = w.lastStatus, w.haveStatus
	if w.tracker != nil {
		if _, _, ok := w.tracker.BossEngaged(); ok {
			o.BossEngaged = true
		}
	}
	o.Bots = map[string]raidwalk.BotObs{}
	for _, n := range w.hireList() {
		b, ok := w.botAt[n]
		if !ok || now.Sub(b.at) > injectBotFresh {
			continue
		}
		o.Bots[n] = raidwalk.BotObs{GUID: b.guid, Pos: dungeon.Point{X: b.x, Y: b.y, Z: b.z}, Dead: b.dead, InCombat: b.inCombat, At: b.at}
	}
	o.Hostiles = []raidwalk.HostileObs{}
	for key, h := range w.hostiles {
		if now.Sub(h.at) > injectHostileFresh {
			continue
		}
		g, err := packet.ParseGUID(key)
		if err != nil {
			continue
		}
		o.Hostiles = append(o.Hostiles, raidwalk.HostileObs{GUID: g, Entry: h.entry, Pos: dungeon.Point{X: h.x, Y: h.y, Z: h.z},
			HPPct: h.hppct, Target: packet.GUID(h.target), Kind: h.kind, Reaction: h.reaction, Flags: h.flags, At: h.at})
	}
	return o
}

// Cleanup releases every bot the injector still holds frozen (spec 1.2): it loops until none is left or the 60 s
// recovery deadline expires, with a fresh context per try and 3 s between tries (reviews ad-i2-r2, ad-i2-r3).
// Every attempt and every retry sleep is bounded by what is left of the deadline, so no attempt can run past it.
// Exhaustion records a BLOCKING incident with the manual command; the walk exits 23. The walk may be interrupted,
// so it never uses the walk's own context.
func (r *raidInjector) Cleanup(_ context.Context) {
	if r.cleanupDone || r.in == nil || r.prepFail != "" {
		return
	}
	w := r.w
	// The walk's own context may be the interrupted one: every attempt gets a fresh context of its own.
	deadline := w.env.now().Add(injectCleanupDeadline)
	r.cleanupUntil = deadline
	defer func() { r.cleanupUntil = time.Time{} }()
	for {
		r.in.DropCleanup() // a cut-off attempt must not hide its bots behind stale pending actions
		remaining := deadline.Sub(w.env.now())
		if remaining <= 0 {
			r.finishCleanup()
			return
		}
		if acts := r.in.Cleanup(); len(acts) > 0 {
			cctx, cancel := context.WithTimeout(context.Background(), min(injectCleanupAttempt, remaining))
			_ = r.exec(cctx, acts)
			cancel()
		}
		frozen := r.in.FrozenVictims()
		if len(frozen) == 0 {
			r.cleanupDone = true
			return
		}
		if remaining = deadline.Sub(w.env.now()); remaining <= 0 {
			r.finishCleanup()
			return
		}
		_ = r.sleep(context.Background(), min(injectCleanupRetry, remaining))
	}
}

// finishCleanup records the bots still frozen at the recovery deadline (nothing when an attempt released them all).
func (r *raidInjector) finishCleanup() {
	if frozen := r.in.FrozenVictims(); len(frozen) > 0 {
		r.recordStuck(frozen)
	}
	r.cleanupDone = true
}

// recordStuck is the cleanup's exhausted recovery: the bots still frozen, and the exact GM command that frees each.
func (r *raidInjector) recordStuck(frozen []string) {
	bots := append([]string(nil), frozen...)
	sort.Strings(bots)
	cmds := make([]string, 0, len(bots))
	for _, b := range bots {
		cmds = append(cmds, ".unfreeze "+b)
	}
	manual := strings.Join(cmds, "; ")
	reason := fmt.Sprintf("cleanup could not free %s within %.0f s", strings.Join(bots, ", "), injectCleanupDeadline.Seconds())
	r.incidents = append(r.incidents, raidwalk.InjectIncident{
		Severity: "blocking", Kind: "cleanup_incomplete", Bots: bots, Reason: reason, Manual: manual,
	})
	r.blocking = true
	r.w.logLine(r.w.env.now(), "BLOCKING: "+reason+"; manual: "+manual)
}

// finalize ends the injector with the walk's last state: the watch's end, or a wipe. Idempotent: Failed and Results both
// call it.
func (r *raidInjector) finalize() {
	if r.finalized {
		return
	}
	r.finalized = true
	w := r.w
	notes := w.newNotes
	w.newNotes = nil
	switch {
	case r.prepFail != "":
		r.results = make([]raidwalk.InjectResult, 0, len(r.specs))
		for _, sp := range r.specs {
			r.results = append(r.results, raidwalk.InjectResult{Kind: sp.Kind, Spec: sp.Raw, Verdict: raidwalk.VerdictInconclusive,
				Reason: r.prepFail, Lines: []raidwalk.InjectLine{}, Measured: map[string]any{}, Notes: []string{}})
		}
	case r.in == nil:
		r.results = make([]raidwalk.InjectResult, 0, len(r.specs))
		for _, sp := range r.specs {
			r.results = append(r.results, raidwalk.InjectResult{Kind: sp.Kind, Spec: sp.Raw, Verdict: raidwalk.VerdictNotFired,
				Reason: "the walk ended before the watch", Lines: []raidwalk.InjectLine{}, Measured: map[string]any{}, Notes: []string{}})
		}
	default:
		r.in.Finish(r.observe(w.env.now(), notes, r.wiped()))
		r.results = r.in.Results()
	}
	for _, res := range r.results {
		if res.Verdict == raidwalk.VerdictFail {
			r.failed = true
		}
	}
}

// wiped says whether the walk lost the party: the wipe a fault at that moment is blamed for (spec 1.2).
func (r *raidInjector) wiped() bool {
	for _, n := range r.w.notes {
		if strings.HasPrefix(n, "wipe at step ") {
			return true
		}
	}
	return strings.HasPrefix(r.w.endReason, "wipe at step ")
}

// Failed is true when one fault's verdict is fail (exit 23, spec 0.5).
func (r *raidInjector) Failed() bool {
	r.finalize()
	return r.failed
}

// Incidents is the run's blocking findings, for the summary and inject.json.
func (r *raidInjector) Incidents() []raidwalk.InjectIncident {
	return append([]raidwalk.InjectIncident(nil), r.incidents...)
}

// Blocking is true when a cleanup left a bot frozen past the recovery deadline.
func (r *raidInjector) Blocking() bool { return r.blocking }

// Results is the walk's injections for the summary, and the injected deaths; it writes <out>/inject.json.
func (r *raidInjector) Results() ([]raidwalk.InjectSummary, int) {
	r.finalize()
	r.writeInjectJSON()
	out := make([]raidwalk.InjectSummary, 0, len(r.results))
	for _, res := range r.results {
		out = append(out, raidwalk.InjectSummary{Kind: string(res.Kind), Verdict: res.Verdict, Reason: res.Reason, Step: res.Step, Victim: res.Victim})
	}
	injected := 0
	if r.in != nil && r.prepFail == "" {
		injected = r.in.InjectedDeaths()
	}
	return out, injected
}

// writeInjectJSON writes the faults' rich results (format tb-raid-inject v1, spec 1.6), 0600.
func (r *raidInjector) writeInjectJSON() {
	w := r.w
	if w.outDir == "" || w.plan == nil || w.roster == nil {
		return
	}
	faults := r.results
	if faults == nil {
		faults = []raidwalk.InjectResult{}
	}
	raw, err := json.MarshalIndent(injectFile{Format: "tb-raid-inject", Version: 1, Plan: w.plan.Name, Map: w.plan.Map,
		Tier: w.roster.Tier, Faults: faults, Incidents: r.incidents}, "", "  ")
	if err != nil {
		w.note("inject.json: %v", err)
		return
	}
	if err := w.writeOut("inject.json", append(raw, '\n')); err != nil {
		w.note("inject.json: %v", err)
	}
}

// PrintPlan is --dry-run: one block per fault, its fire condition and the lines it would send (spec 1.6).
func (r *raidInjector) PrintPlan(out io.Writer) {
	fmt.Fprintln(out, "faults:")
	fmt.Fprintf(out, "  prepare: .gm visible off; .go xyz <step-1> <map>; wait for the commander's map 30 s; wait for the hires' bot_view positions 20 s\n")
	for _, sp := range r.specs {
		from := fmt.Sprintf("from step %d", sp.From)
		switch sp.Kind {
		case raidwalk.InjectKillMT:
			fmt.Fprintf(out, "  kill-mt (%s): on a pull step, a fight on for 6 s, 2+ hostiles on the party, the MT targeted, their mean health 20-90%%, an OT alive\n", from)
			fmt.Fprintf(out, "    .die\n")
		case raidwalk.InjectRunaway:
			fmt.Fprintf(out, "  runaway (%s, dist %d yd, hold %d s): on a move step, no fight for 10 s, a rest hire alive and out of combat, a plan point %d-%d yd from the lead bot\n",
				from, sp.Dist, sp.HoldS, max(110, sp.Dist-30), sp.Dist+50)
			fmt.Fprintf(out, "    .go xyz <Q> <map>; .summon <victim>; .freeze <victim>; .unfreeze <victim>\n")
		case raidwalk.InjectAddPack:
			fmt.Fprintf(out, "  add-pack (%s, max %d yd): on a pull step, a fight on for 3 s, an idle hostile 20-%d yd from the lead bot\n",
				from, sp.MaxYd, sp.MaxYd)
			fmt.Fprintf(out, "    attack <pack-guid> <puller>\n")
		case raidwalk.InjectLag:
			fmt.Fprintf(out, "  lag (%s, secs %d): on a move step, no fight for 10 s, a rest hire within 30 yd of the lead bot\n",
				from, sp.Secs)
			fmt.Fprintf(out, "    .freeze <victim>; .unfreeze <victim>\n")
		}
	}
}
