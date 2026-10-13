package dungeon

import (
	"context"
	"errors"
	"fmt"
	"math"
	"runtime/debug"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/pjerra/turtle-agent/internal/events"
	"github.com/pjerra/turtle-agent/internal/objects"
	"github.com/pjerra/turtle-agent/internal/packet"
	"github.com/pjerra/turtle-agent/internal/tbm"
)

// Remote-leader mode (issue #83). The agent leads a bot group it is not in, through the TortoiseBots remote commander
// (`.bot commander bind`, GUID-targeted `.bot cmd` orders, the TBM:VIEW perception feed). The RemoteDriver walks the same
// Plan with the same Stepper as the Driver, but everything it knows of the dungeon comes from the feed (where the lead bot
// is, who is hurt, which hostiles the bots see and who holds their threat), and everything it does is a `.bot cmd` order.
// The agent's own character is not part of it: it may be anywhere, even outside the instance.

// Runner is what the dungeon runner needs of a run, in group mode (Driver) and remote mode (RemoteDriver).
type Runner interface {
	Run(ctx context.Context, fromLeg int) error
	Report() Report
	Status() (Report, bool)
}

// RemoteGame is what the RemoteDriver does to the game: send commander commands, read the feed. The agent implements it over
// the session (cmd/turtle-agent/commander_methods.go); the tests use a fake with a fake clock. A RemoteGame may also implement
// PathPlanner.
type RemoteGame interface {
	// Command sends ".bot <command>" ("commander bind Tanky", "cmd move 1 2 3") and returns its ACK or ERR reply (Kind
	// timeout when none came in time). The error is for a command that could not be sent.
	Command(ctx context.Context, command string) (tbm.Reply, error)
	// Frame is the newest frame of the feed and the number of frames the feed has published; ok is false when none came yet.
	Frame() (fr tbm.ViewFrame, n uint64, ok bool)
	// ResetView forgets the frame held: a new binding counts its seq from 1.
	ResetView()
	// BindingEnds are the automatic ends of the binding the module announced.
	BindingEnds() []tbm.BindingEnd
	// ViewStats are the feed's counters.
	ViewStats() tbm.ViewStats
	// Emit appends an event of kind to the agent's event log.
	Emit(kind, text string, data map[string]any)
}

// PathPlanner is the optional part of a RemoteGame that plans a navmesh path between two points of a map, wherever the agent
// stands: the lead bot's walks over more than one leg (`move` takes a point within 150 yd) and the approach to a pack follow it.
type PathPlanner interface {
	PlanPath(ctx context.Context, mapID uint32, from, to Point) ([]Point, error)
}

// CCRule asks for a mob entry to be crowd-controlled in a fight: `.bot cmd cc <mark> <guid> [bot]` before the pull.
type CCRule struct {
	Entry uint32 `json:"entry"`
	Mark  string `json:"mark"`          // skull, cross, square, moon, triangle, diamond, circle or star
	Bot   string `json:"bot,omitempty"` // the bot that takes the mark; a capable one when empty
}

// RemoteOptions are the run's choices and limits in remote mode. A zero limit takes its default (DefaultRemoteOptions).
type RemoteOptions struct {
	Lead string   // the lead bot: it walks, the rest follow it
	Bots []string // the other bots to bind with it; empty: the lead bot's party as it is
	// Tank names the bot that holds threat. Empty: the bot that holds the first pulled mob's threat is taken for it.
	Tank string
	CC   []CCRule

	FeedWait      time.Duration // the first frame after the bind
	FeedStale     time.Duration // without a new frame this long the feed is "stalled": the loops wait instead of deciding
	FeedLost      time.Duration // without a new frame this long the run fails
	MoveLeg       float32       // yards: the longest single `move` (the server takes 150)
	MoveArrive    float32       // yards from the point at which the lead bot has arrived
	MoveStall     time.Duration // a lead bot that gained no ground in this long gets the move again
	MoveRetries   int           // stalls and refusals of one walk before it fails
	LeadWait      time.Duration // how long an order waits out lead-unavailable (the lead bot between maps, dead, on a taxi)
	ThreatWait    time.Duration // longest wait for the tank to hold a mob's threat before the party is sent in anyway
	ThreatWarnPct int           // a bot that is not the tank at this share of the top threat is a warning
	UnbindWait    time.Duration // the unbind at the end of the run
}

// DefaultRemoteOptions are the limits of a live run.
func DefaultRemoteOptions() RemoteOptions {
	return RemoteOptions{FeedWait: 20 * time.Second, FeedStale: 6 * time.Second, FeedLost: 45 * time.Second,
		MoveLeg: 120, MoveArrive: 5, MoveStall: 10 * time.Second, MoveRetries: 4, LeadWait: 30 * time.Second,
		ThreatWait: 6 * time.Second, ThreatWarnPct: 90, UnbindWait: 8 * time.Second}
}

func (o RemoteOptions) withDefaults() RemoteOptions {
	d := DefaultRemoteOptions()
	fill := func(dst *time.Duration, def time.Duration) {
		if *dst <= 0 {
			*dst = def
		}
	}
	fill(&o.FeedWait, d.FeedWait)
	fill(&o.FeedStale, d.FeedStale)
	fill(&o.FeedLost, d.FeedLost)
	fill(&o.MoveStall, d.MoveStall)
	fill(&o.LeadWait, d.LeadWait)
	fill(&o.ThreatWait, d.ThreatWait)
	fill(&o.UnbindWait, d.UnbindWait)
	if o.MoveLeg <= 0 {
		o.MoveLeg = d.MoveLeg
	}
	if o.MoveArrive <= 0 {
		o.MoveArrive = d.MoveArrive
	}
	if o.MoveRetries <= 0 {
		o.MoveRetries = d.MoveRetries
	}
	if o.ThreatWarnPct <= 0 {
		o.ThreatWarnPct = d.ThreatWarnPct
	}
	return o
}

// RemoteReport is the report's part for a run in remote mode (Report.Remote; absent in group mode).
type RemoteReport struct {
	Mode      string   `json:"mode"` // "remote"
	LeadBot   string   `json:"lead_bot"`
	Bots      []string `json:"bots"`
	Tank      string   `json:"tank,omitempty"` // the bot taken for the tank (named, or inferred from the first pull)
	Bound     bool     `json:"bound"`          // the bind was acknowledged (or unanswered)
	Unbound   bool     `json:"unbound"`        // the run's end sent the unbind and it was answered
	UnbindVia string   `json:"unbind_via,omitempty"`
	// EndedByModule is the reason the module gave when it ended the binding itself (lead bot gone, party disbanded, a player joined).
	EndedByModule string `json:"ended_by_module,omitempty"`
	// GMCommands is always 0: the remote run sends no GM command (no teleport, revive or aura).
	GMCommands int `json:"gm_commands"`

	// The feed during the run.
	FramesReceived  uint64 `json:"frames_received"`
	FramesMissed    uint64 `json:"frames_missed"`
	FramesInvalid   uint64 `json:"frames_invalid"`
	FramesStale     uint64 `json:"frames_stale"`
	FramesTruncated uint64 `json:"frames_truncated"` // frames the module marked as cut short: hostiles may be missing
	FeedStalls      int    `json:"feed_stalls"`      // times the feed went quiet for FeedStale and came back
	LastSeq         uint64 `json:"last_seq"`

	// The orders.
	OrdersSent       int            `json:"orders_sent"`
	OrdersRefused    int            `json:"orders_refused"`
	OrdersUnanswered int            `json:"orders_unanswered"`
	Orders           map[string]int `json:"orders"`  // sent, by verb
	Refused          map[string]int `json:"refused"` // by "<verb>:<code>"

	// Decisions made from the feed.
	MoveStalls       int `json:"move_stalls"`        // moves sent again because the lead bot gained no ground
	WalkAggros       int `json:"walk_aggros"`        // fights that interrupted a walk
	ThreatHolds      int `json:"threat_holds"`       // mobs the party was sent at once the tank held their threat
	ThreatWaits      int `json:"threat_waits"`       // mobs the party was held back from until the tank held their threat
	ThreatOverrides  int `json:"threat_overrides"`   // mobs the party was sent at after ThreatWait without the tank holding them
	ThreatWarnings   int `json:"threat_warnings"`    // bots that came within ThreatWarnPct of the tank's threat on a mob
	PullFallbacks    int `json:"pull_fallbacks"`     // pulls done as an attack order because the module found no tank
	CCOrders         int `json:"cc_orders"`          // crowd-control orders
	Revives          int `json:"revives"`            // `.bot cmd revive` orders
	ObjectStepsAhead int `json:"object_steps_ahead"` // use_object, loot_object, use_item and game-object waits the plan has from the first leg on: they fail the run
}

func (r *RemoteReport) clone() *RemoteReport {
	if r == nil {
		return nil
	}
	c := *r
	c.Bots = slices.Clone(r.Bots)
	c.Orders = cloneMap(r.Orders)
	c.Refused = cloneMap(r.Refused)
	return &c
}

func cloneMap(m map[string]int) map[string]int {
	out := make(map[string]int, len(m))
	for k, v := range m {
		out[k] = v
	}
	return out
}

// Errors of the remote run.
var (
	errBindingLost = errors.New("the commander binding ended")
	errFeedLost    = errors.New("the view feed stopped")
)

// RemoteDriver runs a Plan with a bot group it leads from outside (see the package comment above). Run blocks; Report can be
// read from another goroutine while it runs.
type RemoteDriver struct {
	plan *Plan
	g    RemoteGame
	cfg  Config
	opt  RemoteOptions
	clk  Clock
	rep  reporter

	st     *Stepper
	curLeg int

	// The feed as the driver last looked at it.
	fr      tbm.ViewFrame
	haveFr  bool
	n       uint64    // frames the feed had published at the last look
	nAt     time.Time // when the driver saw n change
	stalled bool

	bound       bool
	unbinding   bool
	endsAtBind  int
	stats0      tbm.ViewStats
	statsMu     sync.Mutex // guards stats0: Status reads it from another goroutine while begin writes it
	prevDead    map[packet.GUID]bool
	deadSeen    bool
	revived     map[packet.GUID]int
	lastRevive  map[packet.GUID]time.Time
	wipeRecover int
	recoveries  int

	tank       packet.GUID
	engagedAt  map[packet.GUID]time.Time // when a mob was first seen fighting the party
	attacked   map[packet.GUID]bool      // mobs the party was sent at
	heldNoted  map[packet.GUID]bool
	waitNoted  map[packet.GUID]bool
	warned     map[string]bool
	ccDone     map[packet.GUID]bool
	skipped    map[packet.GUID]string
	lastAssist time.Time
	// objectTouched is set when the Stepper asked for game objects, i.e. it is on an object step.
	objectTouched bool
}

// NewRemoteDriver prepares a run of plan in remote mode. A zero clock is the wall clock.
func NewRemoteDriver(plan *Plan, g RemoteGame, cfg Config, opt RemoteOptions, clk Clock) *RemoteDriver {
	if clk.Now == nil || clk.Sleep == nil {
		clk = RealClock()
	}
	d := &RemoteDriver{plan: plan, g: g, cfg: cfg.withDefaults(), opt: opt.withDefaults(), clk: clk, curLeg: -1,
		prevDead: map[packet.GUID]bool{}, revived: map[packet.GUID]int{}, lastRevive: map[packet.GUID]time.Time{},
		engagedAt: map[packet.GUID]time.Time{}, attacked: map[packet.GUID]bool{}, heldNoted: map[packet.GUID]bool{}, waitNoted: map[packet.GUID]bool{},
		warned: map[string]bool{}, ccDone: map[packet.GUID]bool{}, skipped: map[packet.GUID]string{}}
	d.rep.init(plan, clk.Now)
	d.rep.r.SetupShortcuts = slices.Clone(cfg.Setup)
	d.rep.r.Remote = &RemoteReport{Mode: "remote", LeadBot: opt.Lead, Bots: slices.Clone(opt.Bots), Tank: opt.Tank,
		Orders: map[string]int{}, Refused: map[string]int{}}
	return d
}

// Report is a copy of the run report, usable while the run goes on.
func (d *RemoteDriver) Report() Report { r, _ := d.Status(); return r }

// Status is the report and whether the run has ended, read together under one lock.
func (d *RemoteDriver) Status() (Report, bool) {
	r, ended := d.rep.snapshotEnded()
	if r.Remote != nil && !ended {
		d.fillFeed(r.Remote)
	}
	return r, ended
}

// fillFeed copies the feed's counters since the bind into the remote report.
func (d *RemoteDriver) fillFeed(r *RemoteReport) {
	s := d.g.ViewStats()
	d.statsMu.Lock()
	defer d.statsMu.Unlock()
	r.FramesReceived, r.FramesMissed, r.FramesInvalid, r.FramesStale = s.Frames-d.stats0.Frames, s.Missed-d.stats0.Missed, s.Invalid-d.stats0.Invalid, s.Stale-d.stats0.Stale
	r.FramesTruncated = s.Truncated - d.stats0.Truncated
	r.LastSeq = s.LastSeq
}

func (d *RemoteDriver) remote(f func(*RemoteReport)) {
	d.rep.update(func(r *Report) {
		if r.Remote != nil {
			f(r.Remote)
		}
	})
}

// Run does the plan from leg fromLeg. The commander binding is made first and always ended, whatever way the run ends (done,
// failed, stopped or a panic): the unbind is deferred. It returns nil when the plan is done.
func (d *RemoteDriver) Run(ctx context.Context, fromLeg int) error {
	if fromLeg < 0 || fromLeg >= len(d.plan.Legs) {
		return fmt.Errorf("from_leg %d: the plan has legs 0-%d", fromLeg, len(d.plan.Legs)-1)
	}
	d.rep.start(fromLeg)
	err := d.runRecovering(ctx, fromLeg)
	d.finish(err)
	return err
}

// runRecovering is run with a panic turned into a failed run: the deferred unbind has run by the time the panic gets here, and the
// report says what happened instead of the process going down with the binding half undone.
func (d *RemoteDriver) runRecovering(ctx context.Context, fromLeg int) (err error) {
	defer func() {
		if r := recover(); r != nil {
			err = fmt.Errorf("internal error: %v", r)
			d.rep.note(fmt.Sprintf("the remote run panicked: %v\n%s", r, debug.Stack()))
		}
	}()
	return d.run(ctx, fromLeg)
}

func (d *RemoteDriver) finish(err error) {
	leg := d.curLeg
	var state, errText, kind, text string
	switch {
	case err == nil:
		state, kind, text = "done", events.KindDungeonDone, "dungeon "+d.plan.Name+" done (remote)"
	case ctxDone(err):
		state, errText, kind, text = "stopped", "stopped", events.KindDungeonFailed, "dungeon "+d.plan.Name+" stopped (remote)"
	default:
		state, errText, kind, text = "failed", err.Error(), events.KindDungeonFailed, "dungeon "+d.plan.Name+" failed (remote): "+err.Error()
	}
	d.endLeg(state)
	at := d.rep.now()
	d.remote(d.fillFeed)
	data := d.rep.summaryEnded(state, errText, at)
	if err != nil {
		data["error"], data["leg"] = errText, leg
	}
	d.g.Emit(kind, text, data)
	d.rep.end(state, errText, at)
}

func (d *RemoteDriver) run(ctx context.Context, fromLeg int) (err error) {
	defer d.unbind() // every way out: done, failed, stopped, a panic
	if err := d.begin(ctx, fromLeg); err != nil {
		return err
	}
	d.st = NewStepperAt(d.plan, remoteWorld{d}, d.clk.Now, fromLeg)
	waiting := [2]int{-1, -1}
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		d.objectTouched = false
		a := d.st.Next()
		d.rep.setCurrent(a.Label)
		if d.objectTouched {
			// The step looked for a game object (the agent sees none): it is a step only the agent could do. Fail before any order.
			return d.objectStepError(a)
		}
		if a.Kind != ActFailed && a.Leg != d.curLeg {
			d.endLeg("done")
			if a.Kind != ActDone {
				if err := d.startLeg(ctx, a.Leg); err != nil {
					if d.stepFailedRecover(ctx, err) {
						continue
					}
					continue
				}
			}
		}
		switch a.Kind {
		case ActDone:
			return nil
		case ActFailed:
			return errors.New(a.Err)
		case ActWait:
			if waiting != [2]int{a.Leg, a.Step} {
				waiting = [2]int{a.Leg, a.Step}
				d.g.Emit(events.KindDungeonStep, a.Label, map[string]any{"leg": a.Leg, "step": a.Step, "action": string(a.Kind), "mode": "remote"})
			}
			if err := d.tick(ctx, min(d.cfg.WaitPoll, a.Remaining)); err != nil {
				d.stepFailedRecover(ctx, err)
			}
			continue
		}
		began := d.clk.Now()
		var err error
		switch a.Kind {
		case ActWalk:
			err = d.doWalk(ctx, a.To)
		case ActPull:
			err = d.doPull(ctx, a)
		case ActKill:
			err = d.doKill(ctx, a)
		default:
			err = fmt.Errorf("remote mode cannot do %q: %s", a.Kind, remoteObjectShort)
		}
		data := map[string]any{"leg": a.Leg, "step": a.Step, "action": string(a.Kind), "ok": err == nil, "seconds": d.clk.Now().Sub(began).Seconds(), "mode": "remote"}
		if err != nil {
			data["error"] = err.Error()
		}
		d.g.Emit(events.KindDungeonStep, a.Label, data)
		if err != nil {
			d.stepFailedRecover(ctx, err)
			continue
		}
		d.st.Complete()
	}
}

// remoteObjectShort is the failure of an object step in remote mode; remoteObjectMsg adds why.
const remoteObjectShort = "remote mode: object step needs the agent nearby"

// objectStepError is the failure of an object step: using or looting a game object, using an item, waiting for a game object's
// state. The leader of a remote run is a bot, and a bot does not open chests for the agent.
func (d *RemoteDriver) objectStepError(a Action) error {
	kind := "object step"
	if a.Leg >= 0 && a.Leg < len(d.plan.Legs) && a.Step >= 0 {
		l := d.plan.Legs[a.Leg]
		steps := l.Steps
		if strings.Contains(a.Label, "after_boss step") {
			steps = l.AfterBoss
		}
		if a.Step < len(steps) && objectStepKind(steps[a.Step]) != "" {
			kind = objectStepKind(steps[a.Step])
		}
	}
	label := a.Label
	if a.Kind == ActFailed {
		label = a.Err
	}
	return fmt.Errorf("%s: %s: %s (%s)", remoteObjectShort, kind, label, "the agent's character is not with the group; run that leg in group mode")
}

func objectStepKind(st Step) string {
	switch {
	case st.UseObject != nil:
		return "use_object"
	case st.LootObject != nil:
		return "loot_object"
	case st.UseItem != nil:
		return "use_item"
	case st.WaitFor != nil && st.WaitFor.GameObject != nil:
		return "wait_for game_object"
	}
	return ""
}

// stepFailedRecover ends the plan with err, or, for a wipe with RecoverWipe on, recovers the party and lets the step be tried again.
// It reports whether the run goes on.
func (d *RemoteDriver) stepFailedRecover(ctx context.Context, err error) bool {
	if errors.Is(err, errWipe) {
		d.rep.update(func(r *Report) { r.Wipes++ })
		d.rep.note(err.Error())
		if d.cfg.RecoverWipe && d.wipeRecover < d.cfg.MaxWipeRecoveries && ctx.Err() == nil {
			d.wipeRecover++
			if rerr := d.recoverWipe(ctx); rerr == nil {
				return true
			} else {
				d.st.Fail("wipe: " + rerr.Error())
				return false
			}
		}
		d.st.Fail("wipe")
		return false
	}
	d.st.Fail(err.Error())
	return false
}

// remoteWorld is the Stepper's World over the feed: the lead bot's position and the hostiles the bots see. It has no game
// objects: the agent does not see the dungeon.
type remoteWorld struct{ d *RemoteDriver }

func (w remoteWorld) Position() Point {
	p, _ := w.d.leadPos()
	return p
}
func (w remoteWorld) GameObjects() []objects.GameObject {
	w.d.objectTouched = true // only the object steps and game-object waits ask
	return nil
}
func (w remoteWorld) HostilesWithin(c Point, r float32) int {
	n := 0
	for _, h := range w.d.fr.Hostiles {
		if h.HPPct > 0 && dist3(hostilePoint(h), c) <= float64(r) {
			n++
		}
	}
	return n
}

func hostilePoint(h tbm.HostileRec) Point { return Point{h.X, h.Y, h.Z} }
func partyPoint(p tbm.PartyRec) Point     { return Point{p.X, p.Y, p.Z} }

// leadPos is where the lead bot is in the newest frame.
func (d *RemoteDriver) leadPos() (Point, bool) {
	if !d.haveFr {
		return Point{}, false
	}
	l, ok := d.fr.LeadRec()
	if !ok {
		return Point{}, false
	}
	return partyPoint(l), true
}

// begin binds the party, switches the feed on, waits for the first frame and checks the lead bot is where the plan starts.
func (d *RemoteDriver) begin(ctx context.Context, fromLeg int) error {
	if strings.TrimSpace(d.opt.Lead) == "" {
		return errors.New("remote mode needs a lead_bot")
	}
	d.countObjectSteps(fromLeg)
	d.g.ResetView()
	d.setStats0(d.g.ViewStats())
	d.endsAtBind = len(d.g.BindingEnds())
	args := append([]string{"commander", "bind", d.opt.Lead}, d.opt.Bots...)
	r, err := d.g.Command(ctx, strings.Join(args, " "))
	if err != nil {
		return fmt.Errorf("commander bind: %w", err)
	}
	switch r.Kind {
	case tbm.KindErr:
		return fmt.Errorf("commander bind refused: %s", describeBindErr(r.Err))
	case tbm.KindTimeout:
		// No answer: it may have gone through. The unbind at the end clears it either way.
		d.rep.note("commander bind: no ACK or ERR came; going on with the feed (the module may be an older build without the remote commander)")
	}
	d.bound = true
	d.remote(func(r *RemoteReport) { r.Bound = true })
	d.rep.note(fmt.Sprintf("remote mode: bound lead bot %s%s", d.opt.Lead, botsSuffix(d.opt.Bots)))
	// Every new binding starts with the view on; say so anyway, a binding that survived an earlier run may have it off.
	if vr, err := d.g.Command(ctx, "commander view on"); err != nil {
		d.rep.note("commander view on: " + err.Error())
	} else if vr.Kind == tbm.KindErr {
		d.rep.note("commander view on refused: " + describeBindErr(vr.Err))
	}
	if err := d.waitFirstFrame(ctx); err != nil {
		return err
	}
	if l, ok := d.fr.LeadRec(); !ok {
		return fmt.Errorf("the first frame does not list the lead bot %s (lead guid %s)", d.opt.Lead, d.fr.Lead)
	} else if d.plan.Map != l.Map {
		return fmt.Errorf("the lead bot %s is on map %d and the plan needs map %d: bring the group into the dungeon first", l.Name, l.Map, d.plan.Map)
	} else if !strings.EqualFold(l.Name, d.opt.Lead) {
		d.rep.note(fmt.Sprintf("the feed's lead bot is %s, not %s", l.Name, d.opt.Lead))
	}
	if len(d.fr.Party) == 0 {
		return errors.New("the first frame has an empty party")
	}
	d.observeInit()
	if fromLeg > 0 {
		d.rep.note(fmt.Sprintf("from_leg %d: the run starts where the lead bot stands and assumes the legs before it are cleared", fromLeg))
	}
	if d.opt.Tank != "" {
		if m, ok := d.memberByName(d.opt.Tank); ok {
			d.tank = m.GUID
		} else {
			d.rep.note(fmt.Sprintf("tank %s is not in the party of the feed; the tank will be taken from the first pull", d.opt.Tank))
		}
	}
	return nil
}

func botsSuffix(bots []string) string {
	if len(bots) == 0 {
		return ""
	}
	return " with " + strings.Join(bots, ", ")
}

// describeBindErr explains the refusals of `.bot commander bind` (player-controls.md section 1a).
func describeBindErr(e *tbm.Err) string {
	if e == nil {
		return "no reason"
	}
	hint := map[string]string{
		"disabled":         "AiPlayerbot.RemoteCommander is off on the server (needs a TortoiseBots build with the remote commander and the switch on)",
		"already-bound":    "this account already commands a party: unbind it first",
		"no-bot":           "not an online bot this account controls",
		"hired":            "a hired companion cannot be bound",
		"group-raid":       "the lead bot is in a raid",
		"group-has-player": "a player is in the lead bot's party",
		"group-bound":      "another commander holds that party",
		"no-members":       "a lead bot without a party needs at least one more bot",
		"group-too-large":  "more than five bots",
		"bot-bound":        "a bot is still in another commander's group",
		"member-grouped":   "a named bot is in some other group",
		"invite-failed":    "a named bot could not be invited",
	}[e.Code]
	if hint != "" {
		return fmt.Sprintf("%s (%s): %s", e.Code, e.Message, hint)
	}
	return fmt.Sprintf("%s (%s)", e.Code, e.Message)
}

func (d *RemoteDriver) countObjectSteps(fromLeg int) {
	n := 0
	for li := fromLeg; li < len(d.plan.Legs); li++ {
		l := d.plan.Legs[li]
		for _, st := range append(slices.Clone(l.Steps), l.AfterBoss...) {
			if objectStepKind(st) != "" {
				n++
			}
		}
	}
	d.remote(func(r *RemoteReport) { r.ObjectStepsAhead = n })
	if n > 0 {
		d.rep.note(fmt.Sprintf("remote mode: the plan has %d object steps (use_object, loot_object, use_item, game-object waits) from leg %d on; each fails the run when reached, because the agent is not with the group", n, fromLeg))
	}
}

func (d *RemoteDriver) waitFirstFrame(ctx context.Context) error {
	deadline := d.clk.Now().Add(d.opt.FeedWait)
	for {
		d.look()
		if d.haveFr {
			return nil
		}
		if err := d.endedByModule(); err != nil {
			return err
		}
		if d.clk.Now().After(deadline) {
			return fmt.Errorf("no TBM:VIEW frame came in %.0f s after the bind: the view is off, or the server build has no perception feed (TortoiseBots phase 2)", d.opt.FeedWait.Seconds())
		}
		if err := d.clk.Sleep(ctx, d.cfg.Poll); err != nil {
			return err
		}
	}
}

// unbind ends the binding: always, with its own context, so that a stopped run unbinds too.
func (d *RemoteDriver) unbind() {
	if !d.bound {
		return
	}
	d.unbinding = true
	ctx, cancel := context.WithTimeout(context.WithoutCancel(context.Background()), d.opt.UnbindWait+5*time.Second)
	defer cancel()
	r, err := d.g.Command(ctx, "commander unbind")
	via := ""
	switch {
	case err != nil:
		via = "error: " + err.Error()
	case r.Kind == tbm.KindAck:
		via = "ack"
	case r.Kind == tbm.KindErr && r.Err != nil && r.Err.Code == "not-bound":
		via = "already unbound"
	case r.Kind == tbm.KindErr && r.Err != nil:
		via = "refused: " + r.Err.Code
	default:
		via = "no answer"
	}
	d.remote(func(rr *RemoteReport) {
		rr.Unbound, rr.UnbindVia = err == nil && (r.Kind == tbm.KindAck || via == "already unbound"), via
	})
	d.rep.note("commander unbind: " + via)
	d.bound = false
}

func (d *RemoteDriver) setStats0(s tbm.ViewStats) {
	d.statsMu.Lock()
	d.stats0 = s
	d.statsMu.Unlock()
}

// look reads the feed. It never waits.
func (d *RemoteDriver) look() {
	fr, n, ok := d.g.Frame()
	if !ok {
		return
	}
	if !d.haveFr || n != d.n {
		d.fr, d.n, d.haveFr = fr, n, true
		if d.stalled {
			d.remote(func(r *RemoteReport) { r.FeedStalls++ })
		}
		d.nAt, d.stalled = d.clk.Now(), false
	} else if d.clk.Now().Sub(d.nAt) > d.opt.FeedStale {
		d.stalled = true
	}
}

// endedByModule is the error for a binding the module ended on its own.
func (d *RemoteDriver) endedByModule() error {
	if d.unbinding {
		return nil
	}
	if ends := d.g.BindingEnds(); len(ends) > d.endsAtBind {
		reason := ends[len(ends)-1].Reason
		d.remote(func(r *RemoteReport) { r.EndedByModule = reason })
		return fmt.Errorf("%w: %s", errBindingLost, reason)
	}
	return nil
}

// tick waits dur, then looks: the feed, the binding and the party's deaths (a wipe ends the wait with errWipe).
func (d *RemoteDriver) tick(ctx context.Context, dur time.Duration) error {
	if err := d.clk.Sleep(ctx, dur); err != nil {
		return err
	}
	return d.check()
}

func (d *RemoteDriver) check() error {
	d.look()
	if err := d.endedByModule(); err != nil {
		return err
	}
	if d.haveFr && d.clk.Now().Sub(d.nAt) > d.opt.FeedLost {
		return fmt.Errorf("%w: no new frame for %.0f s", errFeedLost, d.opt.FeedLost.Seconds())
	}
	if d.countDeaths() {
		return fmt.Errorf("%w: the whole party is dead", errWipe)
	}
	return nil
}

func (d *RemoteDriver) observeInit() {
	for _, p := range d.fr.Party {
		d.prevDead[p.GUID] = p.Dead
	}
	d.deadSeen = true
}

// countDeaths counts each death once and reports whether the whole party is dead.
func (d *RemoteDriver) countDeaths() bool {
	if !d.haveFr {
		return false
	}
	if !d.deadSeen {
		d.observeInit()
	}
	for _, p := range d.fr.Party {
		if p.Dead && !d.prevDead[p.GUID] {
			d.rep.update(func(r *Report) {
				r.Deaths = append(r.Deaths, Death{Name: p.Name, Leg: d.curLeg, At: d.clk.Now()})
				if n := len(r.Legs); n > 0 && d.curLeg >= 0 {
					r.Legs[n-1].Deaths++
				}
			})
		}
		d.prevDead[p.GUID] = p.Dead
	}
	return d.fr.AllDead()
}

func (d *RemoteDriver) memberByName(name string) (tbm.PartyRec, bool) {
	for _, p := range d.fr.Party {
		if strings.EqualFold(p.Name, name) {
			return p, true
		}
	}
	return tbm.PartyRec{}, false
}

func (d *RemoteDriver) startLeg(ctx context.Context, i int) error {
	d.curLeg = i
	name := d.plan.Legs[i].Name
	d.rep.update(func(r *Report) {
		r.Legs = append(r.Legs, LegReport{Leg: i, Name: name, Started: d.clk.Now(), Result: "running"})
	})
	d.g.Emit(events.KindDungeonLeg, fmt.Sprintf("leg %d %s: start", i, name), map[string]any{"leg": i, "name": name, "phase": "start", "mode": "remote"})
	return d.restGate(ctx)
}

func (d *RemoteDriver) endLeg(result string) {
	i := d.curLeg
	if i < 0 {
		return
	}
	d.curLeg = -1
	var l LegReport
	d.rep.update(func(r *Report) {
		if n := len(r.Legs); n > 0 && r.Legs[n-1].Leg == i {
			x := &r.Legs[n-1]
			x.Seconds, x.Result = d.clk.Now().Sub(x.Started).Seconds(), result
			l = *x
		}
	})
	d.g.Emit(events.KindDungeonLeg, fmt.Sprintf("leg %d %s: %s in %.0f s", i, l.Name, result, l.Seconds), map[string]any{
		"leg": i, "name": l.Name, "phase": "end", "result": result, "seconds": l.Seconds, "deaths": l.Deaths,
		"pulls": l.Pulls, "pull_retries": l.PullRetries, "rest_seconds": l.RestSeconds, "mode": "remote"})
}

func (d *RemoteDriver) legStat(f func(*LegReport)) {
	d.rep.update(func(r *Report) {
		if n := len(r.Legs); n > 0 {
			f(&r.Legs[n-1])
		}
	})
}

// order sends `.bot cmd <verb> <args...>` and counts it. A refusal is recorded in the report; an answer that says the binding is
// gone (not-bound, disabled) ends the run. lead-unavailable is waited out up to LeadWait. The reply is returned for the caller to
// judge (an ERR is not an error here).
func (d *RemoteDriver) order(ctx context.Context, verb string, args ...string) (tbm.Reply, error) {
	cmd := strings.Join(append([]string{"cmd", verb}, args...), " ")
	deadline := d.clk.Now().Add(d.opt.LeadWait)
	for {
		r, err := d.g.Command(ctx, cmd)
		if err != nil {
			return r, fmt.Errorf("cmd %s: %w", verb, err)
		}
		d.remote(func(rr *RemoteReport) {
			rr.OrdersSent++
			rr.Orders[verb]++
			switch r.Kind {
			case tbm.KindErr:
				rr.OrdersRefused++
				if r.Err != nil {
					rr.Refused[verb+":"+r.Err.Code]++
				}
			case tbm.KindTimeout:
				rr.OrdersUnanswered++
			}
		})
		if r.Kind != tbm.KindErr || r.Err == nil {
			return r, nil
		}
		d.recordRefusal(Refusal{Intent: "cmd " + verb, Code: r.Err.Code, Message: r.Err.Message})
		switch r.Err.Code {
		case "not-bound", "disabled":
			return r, fmt.Errorf("%w: cmd %s answered %s (%s)", errBindingLost, verb, r.Err.Code, r.Err.Message)
		case "lead-unavailable":
			if d.clk.Now().Before(deadline) {
				if err := d.tick(ctx, d.cfg.Poll*2); err != nil {
					return r, err
				}
				continue
			}
			return r, fmt.Errorf("cmd %s: the lead bot was unavailable for %.0f s (%s)", verb, d.opt.LeadWait.Seconds(), r.Err.Message)
		}
		return r, nil
	}
}

func (d *RemoteDriver) recordRefusal(r Refusal) {
	d.rep.update(func(rep *Report) {
		for i := range rep.Refusals {
			x := &rep.Refusals[i]
			if x.Intent == r.Intent && x.Code == r.Code && x.Message == r.Message && x.Leg == d.curLeg {
				x.Count++
				return
			}
		}
		if len(rep.Refusals) < maxRefusalKinds {
			rep.Refusals = append(rep.Refusals, RefusalReport{Intent: r.Intent, Code: r.Code, Message: r.Message, Leg: d.curLeg, Count: 1, First: d.clk.Now()})
		}
	})
}

// refused turns an ERR reply into the Refusal the shared classifier reads.
func refusedOf(r tbm.Reply) (Refusal, bool) {
	if r.Kind != tbm.KindErr || r.Err == nil {
		return Refusal{}, false
	}
	return Refusal{Intent: r.Err.Intent, Code: r.Err.Code, Message: r.Err.Message}, true
}

// ---- walking ----------------------------------------------------------------------------------------------------------

// moveArg formats the coordinates of a `.bot cmd move`.
func moveArgs(p Point) []string {
	return []string{tbm.CoordArg(p.X), tbm.CoordArg(p.Y), tbm.CoordArg(p.Z)}
}

// waypoint is the point to send the lead bot to on its way from `from` to `to`: `to` itself when within MoveLeg, else a point on
// the navmesh path (when the game plans one), else on the straight line.
func (d *RemoteDriver) waypoint(ctx context.Context, from, to Point, leg float32) Point {
	if dist3(from, to) <= float64(leg) {
		return to
	}
	if pl, ok := d.g.(PathPlanner); ok {
		if pts, err := pl.PlanPath(ctx, d.plan.Map, from, to); err == nil && len(pts) >= 2 {
			if p, ok := alongPath(pts, from, leg); ok {
				return p
			}
		}
	}
	return alongLine(from, to, float64(leg))
}

// alongPath is the farthest point of the path within leg yards of from, following the corners in order.
func alongPath(pts []Point, from Point, leg float32) (Point, bool) {
	best := from
	found := false
	for i := 1; i < len(pts); i++ {
		a := pts[i-1]
		if i == 1 {
			a = from
		}
		b := pts[i]
		if dist3(from, b) <= float64(leg) {
			best, found = b, true
			continue
		}
		// The corner is beyond: cut the segment where it leaves the circle.
		seg := dist3(a, b)
		if seg == 0 {
			break
		}
		lo, hi := 0.0, 1.0
		for k := 0; k < 24; k++ {
			mid := (lo + hi) / 2
			p := lerp(a, b, mid)
			if dist3(from, p) <= float64(leg) {
				lo = mid
			} else {
				hi = mid
			}
		}
		if lo > 0 {
			best, found = lerp(a, b, lo), true
		}
		break
	}
	return best, found
}

func lerp(a, b Point, t float64) Point {
	return Point{X: float32(float64(a.X) + (float64(b.X)-float64(a.X))*t), Y: float32(float64(a.Y) + (float64(b.Y)-float64(a.Y))*t), Z: float32(float64(a.Z) + (float64(b.Z)-float64(a.Z))*t)}
}

func alongLine(from, to Point, leg float64) Point {
	d := dist3(from, to)
	if d <= leg || d == 0 {
		return to
	}
	return lerp(from, to, leg/d)
}

// moveTimeout bounds one leg of a walk: bots run about 7 yd/s, and a path is longer than the straight line.
func moveTimeout(dist float64) time.Duration {
	return time.Duration(30+dist/3) * time.Second
}

// doWalk sends the lead bot to p and waits for it to get there, in legs of at most MoveLeg yards. The lead bot's position comes
// from the feed. A pack that aggroes on the way is fought (walkAggro) and the walk goes on; a lead bot that gains no ground in
// MoveStall is told again; a refused move is tried again with a shorter leg. WalkAttempts bounds the refusals of one walk.
func (d *RemoteDriver) doWalk(ctx context.Context, p Point) error {
	refusals, stalls, aggros := 0, 0, 0
	leg := d.opt.MoveLeg
	var last string
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		if err := d.check(); err != nil {
			return err
		}
		if err := d.ensureAlive(ctx); err != nil {
			return err
		}
		here, ok := d.leadPos()
		if !ok {
			if err := d.tick(ctx, d.cfg.Poll); err != nil {
				return err
			}
			continue
		}
		if dist3(here, p) <= float64(d.opt.MoveArrive) {
			return nil
		}
		if d.fr.PartyInCombat() && len(d.fr.EngagedHostiles()) > 0 {
			if aggros++; aggros > maxWalkAggros {
				return fmt.Errorf("walk to %.1f %.1f %.1f: the party was pulled into a fight %d times on the way", p.X, p.Y, p.Z, maxWalkAggros)
			}
			if err := d.walkAggro(ctx); err != nil {
				return err
			}
			continue
		}
		wp := d.waypoint(ctx, here, p, leg)
		r, err := d.order(ctx, "move", moveArgs(wp)...)
		if err != nil {
			return err
		}
		if ref, bad := refusedOf(r); bad {
			last = ref.Code + ": " + ref.Message
			if refusals++; refusals > d.cfg.WalkAttempts+d.opt.MoveRetries {
				return fmt.Errorf("walk to %.1f %.1f %.1f: the lead bot was refused %d times (%s)", p.X, p.Y, p.Z, refusals, last)
			}
			if ref.Code == "too-far" || ref.Code == "off-mesh" || ref.Code == "unreachable" {
				leg = max(leg/2, 15) // a shorter leg: closer, and nearer the mesh
			}
			if err := d.tick(ctx, d.cfg.WalkRetryDelay); err != nil {
				return err
			}
			continue
		}
		arrive := d.opt.MoveArrive
		if wp != p {
			arrive = max(arrive, 8)
		}
		res, err := d.waitArrival(ctx, wp, arrive)
		if err != nil {
			return err
		}
		switch res {
		case arrivedAt:
			if wp == p {
				return nil
			}
		case stalledAt:
			if stalls++; stalls > d.opt.MoveRetries {
				return fmt.Errorf("walk to %.1f %.1f %.1f: the lead bot gained no ground %d times (stuck at %.1f %.1f %.1f)", p.X, p.Y, p.Z, stalls, here.X, here.Y, here.Z)
			}
			d.remote(func(r *RemoteReport) { r.MoveStalls++ })
			leg = max(leg/2, 30)
		case fightAt:
			// the loop's combat branch takes it
		}
	}
}

type arrival int

const (
	arrivedAt arrival = iota
	stalledAt
	fightAt
)

// waitArrival waits for the lead bot to come within arrive yards of wp. It ends early when the party is in a fight, when the
// lead bot gained no ground in MoveStall, or at the leg's timeout (reported as a stall).
func (d *RemoteDriver) waitArrival(ctx context.Context, wp Point, arrive float32) (arrival, error) {
	start, _ := d.leadPos()
	began := d.clk.Now()
	limit := moveTimeout(dist3(start, wp))
	winAt, winPos := began, start
	for {
		if err := d.tick(ctx, d.cfg.Poll); err != nil {
			return 0, err
		}
		now := d.clk.Now()
		here, ok := d.leadPos()
		if ok && dist3(here, wp) <= float64(arrive) {
			return arrivedAt, nil
		}
		if len(d.fr.EngagedHostiles()) > 0 && d.fr.PartyInCombat() {
			return fightAt, nil
		}
		if d.stalled {
			winAt = now // a quiet feed proves nothing about the lead bot
			continue
		}
		if ok && dist3(here, winPos) > 2 {
			winAt, winPos = now, here
		}
		if now.Sub(winAt) >= d.opt.MoveStall || now.Sub(began) > limit {
			return stalledAt, nil
		}
	}
}

// ensureAlive brings a dead lead bot back before it is asked to walk: a revive order (owner or GM), then the wait for it to run
// back to its corpse and revive.
func (d *RemoteDriver) ensureAlive(ctx context.Context) error {
	l, ok := d.fr.LeadRec()
	if !ok || !l.Dead {
		return nil
	}
	return d.reviveDead(ctx, "the lead bot is dead")
}

// reviveDead sends a revive order for every dead bot (the lead bot first) and waits for them to show alive. A member that stays
// dead after two orders fails the run.
func (d *RemoteDriver) reviveDead(ctx context.Context, why string) error {
	deadline := d.clk.Now().Add(d.cfg.WipeRecoverTimeout)
	for {
		dead := d.deadMembers()
		if len(dead) == 0 {
			return nil
		}
		if d.clk.Now().After(deadline) {
			return fmt.Errorf("%s and still %d bots dead after %.0f s", why, len(dead), d.cfg.WipeRecoverTimeout.Seconds())
		}
		for _, m := range dead {
			if last, ok := d.lastRevive[m.GUID]; ok && d.clk.Now().Sub(last) < 20*time.Second {
				continue
			}
			if d.revived[m.GUID] >= 3 {
				return fmt.Errorf("%s is still dead after %d revive orders", m.Name, d.revived[m.GUID])
			}
			d.revived[m.GUID]++
			d.lastRevive[m.GUID] = d.clk.Now()
			d.remote(func(r *RemoteReport) { r.Revives++ })
			r, err := d.order(ctx, "revive", m.Name)
			if err != nil {
				return err
			}
			if ref, bad := refusedOf(r); bad && ref.Code != "not-dead" {
				return fmt.Errorf("%s is dead and the revive order was refused: %s (%s)", m.Name, ref.Code, ref.Message)
			}
		}
		if err := d.tick(ctx, d.cfg.Poll*3); err != nil && !errors.Is(err, errWipe) {
			return err
		}
	}
}

func (d *RemoteDriver) deadMembers() []tbm.PartyRec {
	var out []tbm.PartyRec
	for _, p := range d.fr.Party {
		if p.Dead {
			out = append(out, p)
		}
	}
	// The lead bot first: nothing walks without it.
	slices.SortStableFunc(out, func(a, b tbm.PartyRec) int {
		switch {
		case a.GUID == d.fr.Lead && b.GUID != d.fr.Lead:
			return -1
		case b.GUID == d.fr.Lead && a.GUID != d.fr.Lead:
			return 1
		}
		return 0
	})
	return out
}

// recoverWipe brings a dead party back: revive orders for every bot (the bots release and run to their corpses), then the rest gate
// runs when the step goes on. It needs RecoverWipe.
func (d *RemoteDriver) recoverWipe(ctx context.Context) error {
	d.rep.note(fmt.Sprintf("wipe %d: reviving the party with .bot cmd revive (recover_wipe)", d.wipeRecover))
	// The wipe is being recovered from: the deaths already counted stay counted.
	if err := d.reviveDead(ctx, "the party wiped"); err != nil {
		return err
	}
	return nil
}

// ---- resting ----------------------------------------------------------------------------------------------------------

type remoteRest struct {
	ok        bool
	why       string
	minHealth int
	dead      []tbm.PartyRec
	stalled   []string
}

// restState looks at the party in the newest frame, like Driver.restState: health and mana at the thresholds and everyone
// near the lead bot. A caster whose mana did not rise ManaStallPct in ManaStall (it does not drink) is let pass, unless it is
// below HealerManaMin (the feed carries no class, so every caster that low is waited for).
func (d *RemoteDriver) restState(mana map[packet.GUID]manaWatch) remoteRest {
	r := remoteRest{ok: true, minHealth: 100}
	var low []string
	now := d.clk.Now()
	lead, haveLead := d.leadPos()
	for _, m := range d.fr.Party {
		if m.Dead {
			r.dead = append(r.dead, m)
			r.ok = false
			continue
		}
		h := m.HPPct()
		r.minHealth = min(r.minHealth, h)
		if h < d.cfg.RestHealthPct {
			r.ok = false
			low = append(low, fmt.Sprintf("%s health %d%%", m.Name, h))
		}
		if haveLead && m.GUID != d.fr.Lead && dist3(partyPoint(m), lead) > float64(d.cfg.GatherRadius) {
			r.ok = false
			low = append(low, fmt.Sprintf("%s not near the lead bot", m.Name))
		}
		if !m.UsesMana() {
			continue
		}
		p := m.PowerPct()
		if p >= d.cfg.RestManaPct {
			delete(mana, m.GUID)
			continue
		}
		w, ok := mana[m.GUID]
		if !ok {
			w = manaWatch{power: m.Power, since: now}
			mana[m.GUID] = w
		}
		stalled := false
		if now.Sub(w.since) >= d.cfg.ManaStall && p >= d.cfg.HealerManaMin {
			if int(m.Power)-int(w.power) >= int(m.MaxPower)*d.cfg.ManaStallPct/100 {
				mana[m.GUID] = manaWatch{power: m.Power, since: now}
			} else {
				stalled = true
			}
		}
		if stalled {
			r.stalled = append(r.stalled, fmt.Sprintf("%s not drinking (mana %d%%)", m.Name, p))
		} else {
			r.ok = false
			low = append(low, fmt.Sprintf("%s mana %d%%", m.Name, p))
		}
	}
	low = append(low, r.stalled...)
	r.why = fmt.Sprint(low)
	return r
}

// restGate waits until every living bot is rested (`.bot cmd rest` makes them eat and drink). It starts only once the party is out
// of combat: a fight is helped with attack orders, and the timeout counts from its end. A dead bot is revived with `.bot cmd revive`
// (owner or GM) and the wait for it; with RecoverWipe off a revive that is refused fails the run, like a dead member without GM
// shortcuts in group mode.
func (d *RemoteDriver) restGate(ctx context.Context) error {
	began := d.clk.Now()
	defer func() {
		if s := d.clk.Now().Sub(began).Seconds(); s > 0 {
			d.legStat(func(l *LegReport) { l.RestSeconds += s })
		}
	}()
	ordered := false
	var lastRest time.Time
	calm := began
	var fightBegan time.Time
	mana := map[packet.GUID]manaWatch{}
	limit := d.cfg.RestTimeout + 2*d.cfg.FightTimeout
	d.look()
	for {
		if el := d.clk.Now().Sub(began); el > limit {
			return fmt.Errorf("the rest gate has gone on for more than %.0f s (fights keep interrupting it): the party is not rested", limit.Seconds())
		}
		if err := d.check(); err != nil {
			return err
		}
		if eng := d.fr.EngagedHostiles(); len(eng) > 0 {
			now := d.clk.Now()
			if fightBegan.IsZero() {
				fightBegan = now
			}
			if now.Sub(fightBegan) > d.cfg.FightTimeout {
				return fmt.Errorf("the party is still in combat after %.0f s at a rest gate", d.cfg.FightTimeout.Seconds())
			}
			if err := d.assist(ctx, eng); err != nil {
				return err
			}
			ordered, calm = false, now
			clear(mana)
			if err := d.tick(ctx, d.cfg.Poll); err != nil {
				return err
			}
			continue
		}
		fightBegan = time.Time{}
		st := d.restState(mana)
		if len(st.dead) > 0 {
			if err := d.reviveDead(ctx, "dead bots at a rest gate"); err != nil {
				return err
			}
			calm = d.clk.Now()
			continue
		}
		if st.ok {
			if len(st.stalled) > 0 {
				d.rep.note(fmt.Sprintf("leg %d: rest gate passed with %s; mana did not rise in %.0f s", d.curLeg, strings.Join(st.stalled, ", "), d.cfg.ManaStall.Seconds()))
			}
			return nil
		}
		if d.clk.Now().Sub(calm) > d.cfg.RestTimeout {
			if st.minHealth >= d.cfg.RestMinHealth {
				d.rep.note(fmt.Sprintf("leg %d: rest gate gave up after %.0f s (%s); went on with at least %d%% health", d.curLeg, d.cfg.RestTimeout.Seconds(), st.why, st.minHealth))
				return nil
			}
			return fmt.Errorf("the party is not rested after %.0f s: %s", d.cfg.RestTimeout.Seconds(), st.why)
		}
		if !ordered || d.clk.Now().Sub(lastRest) > 45*time.Second {
			ordered, lastRest = true, d.clk.Now()
			if _, err := d.order(ctx, "rest"); err != nil {
				return err
			}
		}
		if err := d.tick(ctx, d.cfg.RestPoll); err != nil {
			return err
		}
	}
}

// stale says whether the feed has gone quiet: the loops that decide from it wait instead.
func (d *RemoteDriver) stale() bool { return d.stalled }

func distToLead(d *RemoteDriver, p Point) float64 {
	l, ok := d.leadPos()
	if !ok {
		return math.MaxFloat64
	}
	return dist3(l, p)
}
