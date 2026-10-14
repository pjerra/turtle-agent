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
	"github.com/pjerra/turtle-agent/internal/tracerun"
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
	// Healer names the bot the healer mana target applies to (the feed carries no class). Empty: every caster is held to the healer target.
	Healer string
	CC     []CCRule
	// Team is the party's team for the team gate (Boss.Team, PullGroup.Team): "alliance" or "horde". Empty: the game's (the agent's own
	// race, as the feed carries none), which is right when the bots share the agent's account faction.
	Team string
	// Preflight is the pre-run kit check: "warn" (the default; `.bot maintenance` and `.bot inv` for each bot, what is missing is reported),
	// "strict" (a warning fails the run) or "off".
	Preflight string
	// OnEnd is what the run does with the bots at its end: "stay" (the default: `cmd stay` before the unbind, so that they do not follow the
	// agent), "logout" (stay, unbind, then `.bot logout` for each) or "none".
	OnEnd string
	// Carrier names the bot that does the object steps (loot_object, a boss's loot_item, use_item, use_object): it walks to the chest or
	// corpse, loots, opens and uses items on `cmd open|loot|use` orders (remote_objects.go). Empty: the first living bot that is neither
	// the lead bot, the tank nor the healer.
	Carrier string
	// LootRestore is the loot method put back after a free-for-all loot ("group" by default; the module cannot be asked for the old one).
	LootRestore string
	// Enter says how the run gets a party that is not inside the plan's map in (remote_enter.go): "auto" (the default: `.tele name` when
	// the account is a developer-level GM and the plan names a tele, else walk the lead bot into the portal), "gm", "walk", or "off"
	// (a party outside fails the run). IgnoreEntryLimit lets the run try a 6th new instance within the hour.
	Enter            string
	IgnoreEntryLimit bool
	// RezWait is how long a dead bot waits for a living caster's resurrection, out of combat, before it is released with `cmd revive`.
	// 0: 30 s; negative: no wait.
	RezWait time.Duration

	EnterVerify time.Duration // after the entry every bot must be on the plan's map within this
	EnterGrace  time.Duration // the followers' time to come in after the lead bot on a walk, before they are summoned
	SummonGap   time.Duration // between two `cmd summon` orders
	// TankBusyWait is how long a pull the module refused as tank-busy (the only tank is in combat) waits for the tank to be free or the
	// mob to be engaged, before the pull is sent again.
	TankBusyWait time.Duration

	FeedWait   time.Duration // the first frame after the bind
	FeedStale  time.Duration // without a new frame this long the feed is "stalled": the loops wait instead of deciding
	FeedLost   time.Duration // without a new frame this long the run fails
	MoveLeg    float32       // yards: the longest single `move` (the server takes 150)
	MoveArrive float32       // yards from the point at which the lead bot has arrived
	MoveStall  time.Duration // a lead bot that gained no ground in this long gets the move again
	// LookAhead is how near (yards) the lead bot must be to its current waypoint for the next `cmd move` to go out, so that it never
	// comes to a stop between waypoints; LookAheadETA does the same by time (distance / the bot's speed). Negative LookAhead turns the
	// look-ahead off (each waypoint is waited for). MoveGap is the least time between two `cmd move` orders.
	LookAhead     float32
	LookAheadETA  time.Duration
	MoveGap       time.Duration
	MoveRetries   int           // stalls and refusals of one walk before it fails
	LeadWait      time.Duration // how long an order waits out lead-unavailable (the lead bot between maps, dead, on a taxi)
	ThreatWait    time.Duration // longest wait for the tank to hold a mob's threat before the party is sent in anyway
	ThreatWarnPct int           // a bot that is not the tank at this share of the top threat is a warning
	// OpenerDelay is how long the tank must have held a mob (top of its T record, the mob hitting it) before the party is first sent
	// at it: the tank's lead needs a moment to build. Negative: no delay. AddCooldown is the least time between two tank orders at
	// the same loose add (a mob hitting a bot that is not the tank).
	OpenerDelay time.Duration
	AddCooldown time.Duration
	UnbindWait  time.Duration // the unbind at the end of the run
}

// Values of RemoteOptions.OnEnd.
const (
	OnEndStay   = "stay"
	OnEndLogout = "logout"
	OnEndNone   = "none"
)

// DefaultRemoteOptions are the limits of a live run.
func DefaultRemoteOptions() RemoteOptions {
	return RemoteOptions{FeedWait: 20 * time.Second, FeedStale: 6 * time.Second, FeedLost: 45 * time.Second,
		MoveLeg: 120, MoveArrive: 5, LookAhead: 10, LookAheadETA: 2 * time.Second, MoveGap: time.Second, MoveStall: 10 * time.Second, MoveRetries: 4, LeadWait: 30 * time.Second,
		ThreatWait: 6 * time.Second, ThreatWarnPct: 90, OpenerDelay: 2 * time.Second, AddCooldown: 8 * time.Second, UnbindWait: 8 * time.Second, RezWait: 30 * time.Second,
		EnterVerify: 30 * time.Second, EnterGrace: 8 * time.Second, SummonGap: 6 * time.Second, TankBusyWait: 10 * time.Second}
}

func (o RemoteOptions) withDefaults() RemoteOptions {
	d := DefaultRemoteOptions()
	o.Team, o.Preflight, o.OnEnd, o.Enter = strings.ToLower(o.Team), strings.ToLower(o.Preflight), strings.ToLower(o.OnEnd), strings.ToLower(o.Enter)
	if o.Enter == "" {
		o.Enter = EnterAuto
	}
	if o.Preflight == "" {
		o.Preflight = PreflightWarn
	}
	if o.OnEnd == "" {
		o.OnEnd = OnEndStay
	}
	if o.RezWait == 0 {
		o.RezWait = d.RezWait
	}
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
	fill(&o.AddCooldown, d.AddCooldown)
	if o.OpenerDelay == 0 {
		o.OpenerDelay = d.OpenerDelay
	}
	fill(&o.UnbindWait, d.UnbindWait)
	fill(&o.EnterVerify, d.EnterVerify)
	fill(&o.EnterGrace, d.EnterGrace)
	fill(&o.SummonGap, d.SummonGap)
	fill(&o.TankBusyWait, d.TankBusyWait)
	fill(&o.LookAheadETA, d.LookAheadETA)
	fill(&o.MoveGap, d.MoveGap)
	if o.LookAhead == 0 {
		o.LookAhead = d.LookAhead
	}
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
	Team      string   `json:"team,omitempty"` // the team the team gate ran with, and where it came from
	Bound     bool     `json:"bound"`          // the bind was acknowledged (or unanswered)
	Unbound   bool     `json:"unbound"`        // the run's end sent the unbind and it was answered
	UnbindVia string   `json:"unbind_via,omitempty"`
	// EndStay is the answer to the `cmd stay` sent before the unbind ("ack", "refused: ...", "no answer"); LoggedOut the bots logged out
	// after it (OnEnd logout).
	EndStay   string   `json:"end_stay,omitempty"`
	LoggedOut []string `json:"logged_out,omitempty"`
	// Preflight is the pre-run kit check (absent with preflight off).
	Preflight *Preflight `json:"preflight,omitempty"`
	// EndedByModule is the reason the module gave when it ended the binding itself (lead bot gone, party disbanded, a player joined).
	EndedByModule string `json:"ended_by_module,omitempty"`
	// GMCommands is the GM commands the run sent: 0, or 1 when the enter phase teleported the lead bot (`.tele name`). The run sends no
	// other (no revive, aura or visibility).
	GMCommands int `json:"gm_commands"`
	// RunGMAssists is the GM `.appear <lead bot>` lines sent DURING the run to keep the agent's character near the party (a GM account;
	// remote_near.go). They are GM assists, not part of the party's play: gm_commands does not count them, so a target of "0 GM commands"
	// has to read this counter too. AgentWalks is the walks of the agent's own character behind the party (any other account).
	RunGMAssists int `json:"run_gm_assists"`
	AgentWalks   int `json:"agent_walks"`
	// Enter is the enter phase (absent when the party was already inside the plan's map).
	Enter *EnterReport `json:"enter,omitempty"`

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
	MoveStalls      int `json:"move_stalls"`      // moves sent again because the lead bot gained no ground
	WalkAggros      int `json:"walk_aggros"`      // fights that interrupted a walk
	ThreatHolds     int `json:"threat_holds"`     // mobs the party was sent at once the tank held their threat
	ThreatWaits     int `json:"threat_waits"`     // mobs the party was held back from until the tank held their threat
	ThreatOverrides int `json:"threat_overrides"` // mobs the party was sent at after ThreatWait without the tank holding them
	ThreatWarnings  int `json:"threat_warnings"`  // bots that came within ThreatWarnPct of the tank's threat on a mob
	PullFallbacks   int `json:"pull_fallbacks"`   // pulls done as an attack order because the module found no tank
	TankBusyWaits   int `json:"tank_busy_waits"`  // pulls the module refused as tank-busy that waited for the tank, then pulled again
	CCOrders        int `json:"cc_orders"`        // crowd-control orders
	Revives         int `json:"revives"`          // `.bot cmd revive` orders
	// Tank first (README "Tank first"): TankThreatShare is the mean share of the top threat the tank held on the mobs of the fight, in
	// percent (100: top of every list, 0: not among the top three), over TankThreatSamples frames of fighting; LooseAddOrders are the
	// tank orders at a mob that hit a bot that was not the tank; CCRespected the crowd-controlled mobs the party was not sent at
	// (counted once per mob); TargetSwitches the times the party was sent at another mob while the one before still lived.
	TankThreatShare   float64 `json:"tank_threat_share"`
	TankThreatSamples int     `json:"tank_threat_samples"`
	LooseAddOrders    int     `json:"loose_add_orders"`
	CCRespected       int     `json:"cc_respected"`
	TargetSwitches    int     `json:"target_switches"`
	RezWaits          int     `json:"rez_waits"`        // times dead bots were left to a living caster's resurrection before a release
	Rezzed            int     `json:"rezzed"`           // bots that got up during those waits
	PackLooks         int     `json:"pack_looks"`       // pack positions the lead bot walked to because the bots saw nothing of the pack from afar
	PacksSeenEmpty    int     `json:"packs_seen_empty"` // pull groups counted as cleared because the party stood at every position and saw no hostile
	// The walks: WalkStops counts the times the lead bot stood still in the middle of a walk (after it had moved, before the walk's end),
	// WalkStoppedS the seconds it stood, LookAheadMoves the `move` orders sent while the bot was still on its way to the last
	// one, and FightHolds the "stay here" moves sent when a fight ended.
	WalkStops      int     `json:"walk_stops"`
	WalkStoppedS   float64 `json:"walk_stopped_s"`
	LookAheadMoves int     `json:"look_ahead_moves"`
	FightHolds     int     `json:"fight_holds"`

	ObjectStepsAhead int `json:"object_steps_ahead"` // use_object, loot_object, use_item and game-object waits the plan has from the first leg on
	// Carrier is the bot that did (or will do) the object steps; ObjectSteps is what each did (remote_objects.go).
	Carrier     string             `json:"carrier,omitempty"`
	ObjectSteps []ObjectStepReport `json:"object_steps,omitempty"`
}

func (r *RemoteReport) clone() *RemoteReport {
	if r == nil {
		return nil
	}
	c := *r
	c.Bots = slices.Clone(r.Bots)
	c.LoggedOut = slices.Clone(r.LoggedOut)
	c.Preflight = r.Preflight.clone()
	c.Enter = r.Enter.clone()
	c.ObjectSteps = cloneObjectSteps(r.ObjectSteps)
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

	bound     bool
	unbinding bool
	// groupLocked is set when the enter phase begins to send the party in: no leave, invite or bind may follow until the run ends.
	groupLocked   bool
	entryRecorded bool    // the instance the lead bot entered is in the ledger
	outside       *uint32 // the map a walk before the entry plans its paths on (nil: the plan's)
	endsAtBind    int
	stats0        tbm.ViewStats
	statsMu       sync.Mutex // guards stats0: Status reads it from another goroutine while begin writes it
	prevDead      map[packet.GUID]bool
	deadSeen      bool
	revived       map[packet.GUID]int
	lastRevive    map[packet.GUID]time.Time
	wipeRecover   int
	recoveries    int

	tank       packet.GUID
	engagedAt  map[packet.GUID]time.Time // when a mob was first seen fighting the party
	attacked   map[packet.GUID]bool      // mobs the party was sent at
	heldNoted  map[packet.GUID]bool
	waitNoted  map[packet.GUID]bool
	warned     map[string]bool
	ccDone     map[packet.GUID]bool
	skipped    map[packet.GUID]string
	lastAssist time.Time
	lastMove   time.Time // when the last `cmd move` went out (MoveGap keeps the orders apart)
	tf         tankFirst // the tank-first state of the fights (remote_tank.go)
	// objectTouched is set when the Stepper asked for game objects, i.e. it is on an object step.
	objectTouched bool
	obj           objState                   // the object steps' carrier, corpses and step reports (remote_objects.go)
	team          string                     // the party's team for the team gate ("" when unknown: everything runs)
	killedAt      map[uint32][]Point         // where mobs of an entry were killed in this run (evidence that a pack is gone)
	ledger        map[packet.GUID]*ledgerMob // every hostile seen fighting the party in this run (remote_ledger.go)
	near          nearState                  // the agent kept near the party (remote_near.go)

	tr        *tracerun.Recorder // the run id and the trace (nil-safe; no file when tracing is off)
	trSeen    uint64             // the feed frame count the trace last took
	trRoster  bool
	trStalled bool
}

// NewRemoteDriver prepares a run of plan in remote mode. A zero clock is the wall clock.
func NewRemoteDriver(plan *Plan, g RemoteGame, cfg Config, opt RemoteOptions, clk Clock) *RemoteDriver {
	if clk.Now == nil || clk.Sleep == nil {
		clk = RealClock()
	}
	d := &RemoteDriver{plan: plan, g: g, cfg: cfg.withDefaults(), opt: opt.withDefaults(), clk: clk, curLeg: -1,
		prevDead: map[packet.GUID]bool{}, revived: map[packet.GUID]int{}, lastRevive: map[packet.GUID]time.Time{},
		engagedAt: map[packet.GUID]time.Time{}, attacked: map[packet.GUID]bool{}, heldNoted: map[packet.GUID]bool{}, waitNoted: map[packet.GUID]bool{},
		warned: map[string]bool{}, ccDone: map[packet.GUID]bool{}, skipped: map[packet.GUID]string{}, killedAt: map[uint32][]Point{}}
	d.rep.init(plan, clk.Now)
	d.rep.r.SetupShortcuts = slices.Clone(cfg.Setup)
	d.rep.r.Remote = &RemoteReport{Mode: "remote", LeadBot: opt.Lead, Bots: slices.Clone(opt.Bots), Tank: opt.Tank,
		Orders: map[string]int{}, Refused: map[string]int{}}
	d.initTrace()
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
	d.traceBegin(fromLeg)
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
	d.emit(kind, text, data)
	d.traceEnd(state, errText, at)
	d.rep.end(state, errText, at)
}

func (d *RemoteDriver) run(ctx context.Context, fromLeg int) (err error) {
	defer d.unbind() // every way out: done, failed, stopped, a panic
	if err := d.begin(ctx, fromLeg); err != nil {
		return err
	}
	d.resolveTeam()
	d.st = NewStepperAt(d.plan, remoteWorld{d}, d.clk.Now, fromLeg)
	if d.cfg.Resume {
		if r, ok := d.st.ResumeAt(); ok {
			d.rep.update(func(rp *Report) { rp.Resumed = &r })
			d.rep.note(r.String())
		}
	}
	waiting := [2]int{-1, -1}
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		if err := d.nearBeforeObjectStep(ctx); err != nil {
			d.stepFailedRecover(ctx, err)
			continue
		}
		d.objectTouched = false
		a := d.st.Next()
		d.noteSkipped()
		d.rep.setCurrent(a.Label)
		if d.objectTouched {
			if err, handled := d.objectLook(ctx, a); handled {
				if err != nil {
					return err
				}
				continue
			}
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
				d.emit(events.KindDungeonStep, a.Label, map[string]any{"leg": a.Leg, "step": a.Step, "action": string(a.Kind), "mode": "remote"})
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
			if err = d.tracedWalk(ctx, a.To); err == nil {
				_, err = d.keepNear(ctx) // the leg's walk is over: bring the agent up
			}
		case ActPull:
			err = d.doPull(ctx, a)
		case ActKill:
			err = d.doBoss(ctx, a)
		case ActLoot, ActUse, ActItem:
			err = d.doObjectStep(ctx, a)
		default:
			err = fmt.Errorf("remote mode cannot do %q", a.Kind)
		}
		data := map[string]any{"leg": a.Leg, "step": a.Step, "action": string(a.Kind), "ok": err == nil, "seconds": d.clk.Now().Sub(began).Seconds(), "mode": "remote"}
		if err != nil {
			data["error"] = err.Error()
		}
		d.emit(events.KindDungeonStep, a.Label, data)
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
		d.ledgerClearAbsences()
		d.rep.update(func(r *Report) { r.Wipes++ })
		d.tr.Event("wipe", map[string]any{"leg": d.curLeg, "error": err.Error()})
		d.rep.note(err.Error())
		if d.cfg.RecoverWipe && d.wipeRecover < d.cfg.MaxWipeRecoveries && ctx.Err() == nil {
			d.wipeRecover++
			if rerr := d.recoverWipe(ctx); rerr == nil {
				d.obj.waitAt = time.Time{} // the step comes back to its game-object wait: the settle time starts again (remote_objects.go)
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
	return w.d.objectsInView()
}

// Team makes the world a TeamWorld: the team gate skips what is for the other team (a boss friendly to the party never shows in the feed).
func (w remoteWorld) Team() string { return w.d.team }

func (w remoteWorld) PartyNear(c Point, r float32) bool {
	for _, m := range w.d.fr.Party {
		if !m.Dead && dist3(partyPoint(m), c) <= float64(r) {
			return true
		}
	}
	return false
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

// resolveTeam takes the party's team for the team gate: the run's team option, else the game's (the agent's own race; the feed carries
// no race, and the owned bots share the account's faction). Without either, nothing is gated and the report says so.
func (d *RemoteDriver) resolveTeam() {
	src := ""
	switch {
	case d.opt.Team != "":
		d.team, src = d.opt.Team, "the team option"
	default:
		if ts, ok := d.g.(TeamSource); ok {
			if t := ts.Team(); t != "" {
				d.team, src = t, "the agent's own race (the bots are taken to share it; pass team to override)"
			}
		}
	}
	if d.team == "" {
		d.rep.note("team gate: the party's team is unknown (no team option, the agent's race is not known); every team-gated step runs")
		return
	}
	d.remote(func(r *RemoteReport) { r.Team = d.team })
	d.rep.note(fmt.Sprintf("team gate: %s, from %s", d.team, src))
}

// noteSkipped reports what the team gate passed over since the last look, in the report and as dungeon_step events.
func (d *RemoteDriver) noteSkipped() {
	for _, text := range d.st.Skipped() {
		d.rep.note(text)
		d.emit(events.KindDungeonStep, text, map[string]any{"action": "skip_team", "ok": true, "mode": "remote"})
	}
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
	if _, ok := d.fr.LeadRec(); !ok {
		return fmt.Errorf("the first frame does not list the lead bot %s (lead guid %s)", d.opt.Lead, d.fr.Lead)
	}
	if d.opt.Enter != EnterOff && d.needsEntry() {
		if err := d.enter(ctx); err != nil {
			return err
		}
	}
	if l, ok := d.fr.LeadRec(); !ok {
		return fmt.Errorf("the lead bot %s is not in the feed any more (lead guid %s)", d.opt.Lead, d.fr.Lead)
	} else if d.plan.Map != l.Map {
		return fmt.Errorf("the lead bot %s is on map %d and the plan needs map %d: bring the group into the dungeon first (or let the run enter it: enter \"auto\")", l.Name, l.Map, d.plan.Map)
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
	d.lootOnAtStart(ctx)
	return d.preflight(ctx)
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
		"party-unnamed":    "the party holds a bot the bind did not name: name every bot in it (bots), or the bind would drop the party's instance bind",
		"lead-bound":       "the lead bot holds a saved instance of a different instance than the group's",
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
		d.rep.note(d.objectStepsNote(n, fromLeg))
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
	if d.opt.OnEnd != OnEndNone {
		// Once the commander lets go the bots follow their master, the agent, wherever it is: stand them still first.
		d.endStay(ctx)
	}
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
	if d.opt.OnEnd == OnEndLogout {
		lctx, lcancel := context.WithTimeout(context.WithoutCancel(context.Background()), 30*time.Second)
		defer lcancel()
		d.endLogout(lctx)
	}
}

func (d *RemoteDriver) setStats0(s tbm.ViewStats) {
	d.statsMu.Lock()
	d.stats0 = s
	d.statsMu.Unlock()
}

// look reads the feed. It never waits.
func (d *RemoteDriver) look() {
	defer d.traceLook()
	fr, n, ok := d.g.Frame()
	if !ok {
		return
	}
	if !d.haveFr || n != d.n {
		d.fr, d.n, d.haveFr = fr, n, true
		d.noteLedger()
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
			d.tr.Event("death", map[string]any{"name": p.Name, "a": guidText(p.GUID), "leg": d.curLeg})
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
	d.emit(events.KindDungeonLeg, fmt.Sprintf("leg %d %s: start", i, name), map[string]any{"leg": i, "name": name, "phase": "start", "mode": "remote"})
	// The gate at a leg's start waits for health and mana but not for the followers: they catch up on the walk, and the gate before a
	// pull gathers them.
	return d.restGate(ctx, false)
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
	d.emit(events.KindDungeonLeg, fmt.Sprintf("leg %d %s: %s in %.0f s", i, l.Name, result, l.Seconds), map[string]any{
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
	if verb == "move" {
		defer func() { d.lastMove = d.clk.Now() }()
	}
	deadline := d.clk.Now().Add(d.opt.LeadWait)
	for {
		sentAt := d.clk.Now()
		r, err := d.g.Command(ctx, cmd)
		if err != nil {
			return r, fmt.Errorf("cmd %s: %w", verb, err)
		}
		d.traceOrder(verb, cmd, r, d.clk.Now().Sub(sentAt).Milliseconds())
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
		mapID := d.plan.Map
		if d.outside != nil {
			mapID = *d.outside
		}
		if pts, err := pl.PlanPath(ctx, mapID, from, to); err == nil && len(pts) >= 2 {
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
	var meter walkMeter
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
			meter = walkMeter{} // the fight is not a stop of the walk
			continue
		}
		wp := d.waypoint(ctx, here, p, leg)
		if err := d.moveGap(ctx); err != nil {
			return err
		}
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
		ahead := wp != p && d.opt.LookAhead > 0
		res, err := d.waitArrival(ctx, wp, arrive, ahead, &meter)
		if err != nil {
			return err
		}
		switch res {
		case nearAt:
			// The lead bot is about to reach the waypoint: the next order goes out from here, while it is still running.
			d.remote(func(r *RemoteReport) { r.LookAheadMoves++ })
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
	nearAt // with look-ahead: close enough to the waypoint for the next order
)

// walkSpeed is the speed (yd/s) a lead bot is taken to run at until the feed has shown it.
const walkSpeed = 6.6

// walkMeter watches the lead bot during a walk: how fast it runs, and the times it stood still after it had started (a stop in the
// middle of the walk), for the report.
type walkMeter struct {
	prev    Point
	prevAt  time.Time
	have    bool
	moved   bool    // the bot has been seen moving in this walk
	stopped bool    // the last look found it standing: one stop, however long
	speed   float64 // yd/s, as of the last look that found it moving
}

// look takes the bot's position at now.
func (m *walkMeter) look(d *RemoteDriver, here Point, now time.Time) {
	if !m.have {
		m.prev, m.prevAt, m.have = here, now, true
		return
	}
	dt := now.Sub(m.prevAt).Seconds()
	if dt <= 0 {
		return
	}
	v := dist3(here, m.prev) / dt
	m.prev, m.prevAt = here, now
	if v > 0.5 {
		m.moved, m.stopped = true, false
		m.speed = min(max(v, 3), 12)
		return
	}
	if !m.moved {
		return
	}
	d.remote(func(r *RemoteReport) {
		r.WalkStoppedS += dt
		if !m.stopped {
			r.WalkStops++
		}
	})
	m.stopped = true
}

// near says whether the bot at here is close enough to wp for the next order: within LookAhead yards or LookAheadETA of running.
func (m *walkMeter) near(d *RemoteDriver, here, wp Point) bool {
	dist := dist3(here, wp)
	if dist <= float64(d.opt.LookAhead) {
		return true
	}
	v := m.speed
	if v <= 0 {
		v = walkSpeed
	}
	return dist/v <= d.opt.LookAheadETA.Seconds()
}

// moveGap keeps two `cmd move` orders MoveGap apart.
func (d *RemoteDriver) moveGap(ctx context.Context) error {
	if d.lastMove.IsZero() {
		return nil
	}
	if left := d.opt.MoveGap - d.clk.Now().Sub(d.lastMove); left > 0 {
		return d.tick(ctx, left)
	}
	return nil
}

// waitArrival waits for the lead bot to come within arrive yards of wp. It ends early when the party is in a fight, when the
// lead bot gained no ground in MoveStall, or at the leg's timeout (reported as a stall). With ahead it also ends as soon as the bot
// is near wp (nearAt), so that the caller can send the next order before the bot stops.
func (d *RemoteDriver) waitArrival(ctx context.Context, wp Point, arrive float32, ahead bool, meter *walkMeter) (arrival, error) {
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
		if ok && !d.stalled {
			meter.look(d, here, now)
		}
		if ok && dist3(here, wp) <= float64(arrive) {
			return arrivedAt, nil
		}
		if len(d.fr.EngagedHostiles()) > 0 && d.fr.PartyInCombat() {
			return fightAt, nil
		}
		if ahead && ok && !d.stalled && meter.near(d, here, wp) {
			return nearAt, nil
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
	if err := d.waitForRez(ctx, why); err != nil {
		return err
	}
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

// resurrectors are the living bots out of combat that may raise a dead one: the named healer, else any bot that uses mana (the feed
// carries no class, so a mage counts; the wait is bounded and a mage's absence of a resurrection costs RezWait at most).
func (d *RemoteDriver) resurrectors() []tbm.PartyRec {
	var out []tbm.PartyRec
	for _, p := range d.fr.Party {
		if p.Dead || !p.UsesMana() || p.InCombat() {
			continue
		}
		if d.opt.Healer != "" && !strings.EqualFold(d.opt.Healer, p.Name) {
			continue
		}
		out = append(out, p)
	}
	return out
}

// waitForRez leaves dead bots to a living healer's resurrection before they are released: `cmd revive` makes a bot release its spirit
// and run back (or revives it at once for a GM), and a released bot cannot be raised in place any more. It waits while a resurrector
// is alive out of combat, up to RezWait of calm (fights in the meantime are helped and do not count), and ends at once when none is
// left: a wipe goes straight to the revive orders. It returns when nobody is dead or the wait is over.
func (d *RemoteDriver) waitForRez(ctx context.Context, why string) error {
	if d.opt.RezWait <= 0 {
		return nil
	}
	began := d.clk.Now()
	last := began
	var calm time.Duration
	noted := false
	seenDead := map[packet.GUID]bool{} // every bot seen dead during the wait, including ones that fell meanwhile
	defer func() {
		raised := 0
		for g := range seenDead {
			if m, ok := d.fr.Member(g); ok && !m.Dead {
				raised++
			}
		}
		if raised > 0 {
			d.remote(func(r *RemoteReport) { r.Rezzed += raised })
			d.rep.note(fmt.Sprintf("leg %d: %d dead bots were raised within %.0f s of waiting", max(d.curLeg, 0), raised, d.clk.Now().Sub(began).Seconds()))
		}
	}()
	for {
		dead := d.deadMembers()
		for _, m := range dead {
			seenDead[m.GUID] = true
		}
		if len(dead) == 0 {
			return nil
		}
		now := d.clk.Now()
		if len(d.resurrectors()) == 0 {
			if noted {
				d.rep.note(fmt.Sprintf("leg %d: no living caster out of combat to raise %d dead bots (%s); releasing them", max(d.curLeg, 0), len(dead), why))
			}
			return nil
		}
		if calm >= d.opt.RezWait || now.Sub(began) > d.opt.RezWait+d.cfg.FightTimeout {
			d.rep.note(fmt.Sprintf("leg %d: %d dead bots were not raised in %.0f s (%s); releasing them", max(d.curLeg, 0), len(dead), d.opt.RezWait.Seconds(), why))
			return nil
		}
		if !noted {
			noted = true
			d.remote(func(r *RemoteReport) { r.RezWaits++ })
			d.rep.note(fmt.Sprintf("leg %d: %d dead bots (%s); waiting up to %.0f s for a living caster to raise them before releasing", max(d.curLeg, 0), len(dead), why, d.opt.RezWait.Seconds()))
		}
		if eng := d.fr.EngagedHostiles(); len(eng) > 0 {
			if err := d.assist(ctx, eng); err != nil {
				return err
			}
		} else {
			calm += now.Sub(last)
		}
		last = now
		if err := d.tick(ctx, d.cfg.Poll*2); err != nil && !errors.Is(err, errWipe) {
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
	short     []string
	minHealth int
	dead      []tbm.PartyRec
	stalled   []string
	nudge     []shortMana // members whose mana has not risen at all in DrinkNudge and have had no order of their own yet
}

// restState looks at the party in the newest frame, like Driver.restState: health and mana at the thresholds and everyone
// near the lead bot. A caster whose mana did not rise ManaStallPct in ManaStall (it does not drink) is let pass, unless it is
// below HealerManaMin (the feed carries no class, so every caster that low is waited for). The healer is the bot RemoteOptions.Healer
// names, held to the healer mana target; without one every caster is.
func (d *RemoteDriver) restState(rw *restWatch) remoteRest {
	r := remoteRest{ok: true, minHealth: 100}
	var low []string
	now := d.clk.Now()
	tgt := rw.tgt
	lead, haveLead := d.leadPos()
	for _, m := range d.fr.Party {
		if m.Dead {
			r.dead = append(r.dead, m)
			r.ok = false
			continue
		}
		h := m.HPPct()
		r.minHealth = min(r.minHealth, h)
		if h < tgt.health {
			r.ok = false
			low = append(low, fmt.Sprintf("%s health %d%%", m.Name, h))
		}
		if rw.gather && haveLead && m.GUID != d.fr.Lead && dist3(partyPoint(m), lead) > float64(d.cfg.GatherRadius) {
			r.ok = false
			low = append(low, fmt.Sprintf("%s not near the lead bot", m.Name))
		}
		if !m.UsesMana() {
			continue
		}
		p := m.PowerPct()
		healer := d.opt.Healer == "" || strings.EqualFold(d.opt.Healer, m.Name)
		if p >= tgt.manaNeed(healer) {
			delete(rw.mana, m.GUID)
			continue
		}
		w, nudge := rw.look(d.cfg, now, m.GUID, m.Power)
		sm := shortMana{GUID: m.GUID, Name: m.Name, Pct: p}
		if nudge {
			r.nudge = append(r.nudge, sm)
		}
		if p >= d.cfg.HealerManaMin && rw.stalled(d.cfg, now, m.GUID, w, m.Power, m.MaxPower) {
			r.stalled = append(r.stalled, fmt.Sprintf("%s not drinking (mana %d%%)", m.Name, p))
		} else {
			r.ok = false
			low = append(low, sm.String())
		}
	}
	low = append(low, r.stalled...)
	r.short = low
	r.why = fmt.Sprint(low)
	return r
}

// restGate waits until every living bot is rested (`.bot cmd rest` makes them eat and drink). It starts only once the party is out
// of combat: a fight is helped with attack orders, and the timeout counts from its end. A dead bot is revived with `.bot cmd revive`
// (owner or GM) and the wait for it; with RecoverWipe off a revive that is refused fails the run, like a dead member without GM
// shortcuts in group mode.
func (d *RemoteDriver) restGate(ctx context.Context, gather bool) (err error) {
	defer d.traceRest(d.clk.Now())
	began := d.clk.Now()
	tgt := restTargetFor(d.cfg, d.plan, d.curLeg)
	rec := newGateRec(max(d.curLeg, 0), tgt, began)
	defer func() { rec.finish(&d.rep, d.clk.Now(), err) }()
	ordered := false
	var lastRest time.Time
	calm := began
	var fightBegan time.Time
	rw := newRestWatch(tgt)
	rw.gather = gather
	limit := tgt.max + 2*d.cfg.FightTimeout
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
			clear(rw.mana)
			if err := d.tick(ctx, d.cfg.Poll); err != nil {
				return err
			}
			continue
		}
		fightBegan = time.Time{}
		st := d.restState(rw)
		rec.g.Short = st.short
		if len(st.dead) > 0 {
			if err := d.reviveDead(ctx, "dead bots at a rest gate"); err != nil {
				return err
			}
			calm = d.clk.Now()
			continue
		}
		if st.ok {
			if len(st.stalled) > 0 {
				rec.g.Result = "stalled"
				d.rep.note(fmt.Sprintf("leg %d: rest gate passed with %s; mana did not rise in %.0f s", d.curLeg, strings.Join(st.stalled, ", "), d.cfg.ManaStall.Seconds()))
			}
			return nil
		}
		// The commander has no order for one bot (`.bot cmd rest` is the whole party's): a member whose mana does not rise at all gets
		// the rest order once more, and a refused one is told apart in the record.
		if fresh := slices.DeleteFunc(slices.Clone(st.nudge), func(m shortMana) bool { return rw.nudged[m.GUID] }); len(fresh) > 0 {
			for _, m := range fresh {
				rw.nudged[m.GUID] = true
				rec.nudged(m.Name)
			}
			ordered, lastRest = true, d.clk.Now()
			r, err := d.order(ctx, "rest")
			if err != nil {
				return err
			}
			if r.Kind == tbm.KindErr {
				rec.g.PartyRestRefused = true
			}
			d.rep.note(fmt.Sprintf("leg %d: %s mana %d%% did not rise in %.0f s; sent cmd rest again (%s)", d.curLeg, fresh[0].Name, fresh[0].Pct, d.cfg.DrinkNudge.Seconds(), replyText(r)))
		}
		if d.clk.Now().Sub(calm) > tgt.max {
			if st.minHealth >= d.cfg.RestMinHealth {
				rec.g.Result = "gave_up"
				d.rep.note(fmt.Sprintf("leg %d: rest gate gave up after %.0f s (%s); went on with at least %d%% health", d.curLeg, tgt.max.Seconds(), st.why, st.minHealth))
				return nil
			}
			return fmt.Errorf("the party is not rested after %.0f s: %s", tgt.max.Seconds(), st.why)
		}
		if !ordered || d.clk.Now().Sub(lastRest) > 45*time.Second {
			ordered, lastRest = true, d.clk.Now()
			r, err := d.order(ctx, "rest")
			if err != nil {
				return err
			}
			if r.Kind == tbm.KindErr {
				rec.g.PartyRestRefused = true
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
