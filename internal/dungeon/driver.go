package dungeon

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"sort"
	"strings"
	"time"

	"github.com/pjerra/turtle-agent/internal/events"
	"github.com/pjerra/turtle-agent/internal/objects"
	"github.com/pjerra/turtle-agent/internal/packet"
	"github.com/pjerra/turtle-agent/internal/tracerun"
)

// Game is what the Driver does to the game and what it reads of it. The agent implements it over a session
// (cmd/turtle-agent/dungeon_methods.go); the tests use a fake with a fake clock.
type Game interface {
	Position() Point
	Map() uint32
	GameObjects() []objects.GameObject
	// Units lists the units in view, players included; the Driver filters.
	Units() []objects.Unit
	Self() Self
	Party() []Member
	// WalkPath walks the navmesh to the point and reports how it went (the session's walk_path).
	WalkPath(ctx context.Context, to Point) (WalkResult, error)
	// PathPoints plans the walk to the point without moving: the corners of the path, the first being the player.
	PathPoints(ctx context.Context, to Point) ([]Point, error)
	UseObject(ctx context.Context, guid packet.GUID) error
	Target(ctx context.Context, guid packet.GUID) error
	// Chat says text in channel ("party"); the bot orders go as party chat, like /p .bot action attack.
	Chat(ctx context.Context, channel, text string) error
	// GM runs a GM command (".revive Name").
	GM(ctx context.Context, text string) error
	// GMOn selects the unit and runs a GM command that acts on the selected unit (".aura <id>") as one operation,
	// so that nothing else can change the selection between the two.
	GMOn(ctx context.Context, guid packet.GUID, text string) error
	Teleport(ctx context.Context, to Point, mapID uint32) error
	// ProbeAccount sends ".account" (SEC_PLAYER, Chat.cpp:77) and reads the account's security level from the server's reply
	// "Your account level is: N" (0 player, 1 observer, 2 moderator, 3 developer ...). With PlayerCommands = 0 the line is
	// said aloud instead, which counts as level 0. known is false when no such answer came: an unrelated system line is no answer.
	ProbeAccount(ctx context.Context) (level int, known bool, err error)

	// The leader's death (README "Death and the corpse run"): the state, the answer to a resurrection offer, the release of
	// the spirit, the corpse's position, the reclaim of the corpse, and the portal's area trigger a ghost sends to enter a
	// dungeon with its corpse. The resurrection sickness is read through the optional SelfAuraSource.
	Death() DeathState
	AcceptResurrect(ctx context.Context) error
	ReleaseSpirit(ctx context.Context) error
	CorpseQuery(ctx context.Context) (Corpse, error)
	ReclaimCorpse(ctx context.Context) error
	AreaTrigger(ctx context.Context, id uint32) error
	// Emit appends an event of kind (events.KindDungeon*) to the agent's event log.
	Emit(kind, text string, data map[string]any)
}

// Brain is the leader's bot brain (internal/brain). The Driver starts it at the first rest gate of a leg, so it buffs and
// fights from there on, tells it when out-of-combat upkeep (drinking, buffs) is allowed, and stops it at the end of
// the leg. The Driver keeps its own orders to the party; the brain only casts.
type Brain interface {
	// Start turns the brain on; it does nothing when it already runs.
	Start() error
	// Stop turns the brain off and waits for it to end.
	Stop()
	// Rest allows (true) or forbids upkeep that sits the leader down: the Driver allows it only inside a rest gate.
	Rest(on bool)
	// HoldMove takes the brain's move lock for one movement of the leader (a walk, a teleport, a portal): a step of the brain's
	// that is under way is cancelled and waited for first, and until release is called the brain starts no walk of its own. It
	// works whether or not the brain runs, and holds nest. The Driver holds it around every call that moves the leader, and only
	// from its own goroutine: the wait for a cancelled step needs the session's loop to run, so a call from inside that loop
	// would wait for itself. The error is the context's.
	HoldMove(ctx context.Context) (release func(), err error)
}

// Self is the leader as the session sees it.
type Self struct {
	GUID      packet.GUID
	Name      string
	Health    uint32
	MaxHealth uint32
	Ghost     bool // the spirit is released: health is 1, but the leader is dead
}

// Dead reports a leader with no health left, or a ghost.
func (s Self) Dead() bool { return s.Ghost || (s.MaxHealth > 0 && s.Health == 0) }

// Member is a party member as the session's party list shows it.
type Member struct {
	Name      string
	GUID      packet.GUID
	Online    bool
	Dead      bool
	Health    uint32
	MaxHealth uint32
	PowerType uint8 // 0 mana
	Power     uint32
	MaxPower  uint32
}

// WalkResult is how a walk_path went: Result is "arrived", "partial" or "no_path".
type WalkResult struct {
	Result   string
	Reason   string
	DistLeft float32
}

// Clock is the Driver's time: Sleep waits d or until ctx ends. Tests give a fake that moves Now by hand.
type Clock struct {
	Now   func() time.Time
	Sleep func(ctx context.Context, d time.Duration) error
}

// RealClock is the wall clock.
func RealClock() Clock {
	return Clock{Now: time.Now, Sleep: func(ctx context.Context, d time.Duration) error {
		t := time.NewTimer(d)
		defer t.Stop()
		select {
		case <-t.C:
			return nil
		case <-ctx.Done():
			return ctx.Err()
		}
	}}
}

// Config holds the Driver's limits. A zero field takes the default of DefaultConfig.
type Config struct {
	// Options of the run, not limits (set by the caller).
	Resume    bool // when the party stands further along the leg than its first point, skip what it has passed (see Resumed); off by default until a live check
	Teleport  bool // GM-teleport to the start of the first leg and bring the party along (flagged in the report)
	Shortcuts bool // allow the GM revive of a dead party member at a rest gate (flagged in the report)
	// GM says how the Driver learns whether the leader's account is a GM. Without one the driver sends no GM command: no
	// ".gm visible on", no teleport, no revive, no ".aura" (a Teleport or Buffs option then fails the run at once).
	GM GMMode
	// GMLevel is the account level a GMAssumed leader is taken to have (0: developer). It gates each GM command (gmCapLevels).
	GMLevel int
	// RecoverWipe lets the whole party, dead, do a corpse run and the run go on from the current step, instead of ending
	// with "wipe". Dead members at a rest gate with no GM revive are recovered the same way. Off by default.
	RecoverWipe bool
	// WaitSickness waits resurrection sickness out after a corpse run (up to SicknessMax); else it is only noted.
	WaitSickness bool
	// Setup lists the GM commands the caller used to set the run up; they are copied into the report (setup_shortcuts).
	Setup []string
	// Buffs are put on each companion by the GM leader (".aura <id>" on the selected member) as the run starts, after a
	// GM revive, and on a leg that finds one missing; every application is a "buff" entry in the report's gm_shortcuts.
	Buffs []Buff
	// Brain, when set, plays the leader's character during fights (the agent's bot brain). BrainName is shown in the report.
	Brain     Brain
	BrainName string
	// Trace says how the run is traced: its run id, the trace folder, whether there is a trace at all (internal/tracerun).
	Trace tracerun.Config

	WalkAttempts   int           // tries of one walk (each is a walk_path, which replans by itself)
	WalkRetryDelay time.Duration // wait between two tries
	UseSettle      time.Duration // wait after a use for the world to answer
	ItemsWait      time.Duration // how long the check of a required item waits for an empty bag list to fill (the inventory not yet sent)
	PullAttempts   int           // tries to start one fight before the run fails
	PullRange      float32       // yards: a target farther than this is approached first
	PullStandoff   float32       // yards from the target the leader stops at when it approaches
	PackRadius     float32       // yards around a pull position that a pack member may stand
	EngageTimeout  time.Duration // a pull that draws no fight in this long has failed
	EvadeGrace     time.Duration // a fight with nobody in combat for this long is over (evaded)
	AssistEvery    time.Duration // how often the leader re-orders the party onto the nearest pack member
	FightTimeout   time.Duration // one pull group
	BossTimeout    time.Duration // one boss fight
	PostFight      time.Duration // wait after the last pack member died (loot, last hits)
	AddsWait       time.Duration // longest wait for loose hostile units to be dealt with after a fight
	RestTimeout    time.Duration // longest rest gate (a leg's rest.max_s overrides it)
	RestPoll       time.Duration
	RestHealthPct  int // rested: every party member's health at least this, every healer's mana at least RestHealerManaPct, every other mana user's at least RestManaPct
	RestManaPct    int // the mana target of a member that is not a healer (a leg's rest.mana overrides it)
	// RestHealerManaPct is the healer's mana target (a leg's rest.healer_mana overrides it). In remote mode the feed carries no class:
	// the bot named by RemoteOptions.Healer has it, and without one every caster does.
	RestHealerManaPct int
	// DrinkNudge is how long a member short of mana may show no rise at all before it gets one drink order of its own.
	DrinkNudge    time.Duration
	RestMinHealth int           // after RestTimeout the run goes on only if everyone has at least this health
	GatherRadius  float32       // yards: rested also asks every living member to be this close
	WaitPoll      time.Duration // time between two looks at a wait step
	Poll          time.Duration // time between two looks at a fight
	ArriveTimeout time.Duration // the party's walk to the leader after a teleport
	ManaStall     time.Duration // a rest gate stops waiting for a caster whose mana has not risen this long (not drinking)
	ManaStallPct  int           // "has risen" means by at least this percent of the caster's maximum mana in ManaStall
	HealerManaMin int           // a healer is let pass for not drinking only with at least this percent mana (else it waits the gate out)
	ReviveWait    time.Duration // longest look for the dead flag to clear after a GM revive, before judging it
	AggroQuiet    time.Duration // a fight that came up on a walk is over when nothing has hit the party this long
	AmbushPause   time.Duration // how long the party stands at each position of a stealthed pack
	WalkWatch     time.Duration // real time between two looks for combat during a walk (it is stopped when one starts)
	BuffGap       time.Duration // wait between two .aura commands (the server throttles chat)
	BuffViewWait  time.Duration // longest wait for a companion out of the leader's view before its buffs go on (and to show them after)
	BuffMaxDist   float32       // yards: a companion farther than this from the leader is not selected for a buff

	// The leader's death.
	BrainRezWait       time.Duration // at a rest gate, how long the leader's brain is given to resurrect dead members before a GM revive or the corpse runs (RezBrain)
	ResWait            time.Duration // after the leader died, how long a living healer is given to resurrect it before the spirit is released
	ResAcceptWait      time.Duration // after accepting a resurrection, how long to wait for the leader to live
	ReleaseWait        time.Duration // after the release, how long to wait for the ghost
	CorpseRunTimeout   time.Duration // a corpse run that takes longer fails the run
	MaxLeaderDeaths    int           // deaths of the leader in one run before it gives up
	MaxWipeRecoveries  int           // recovered wipes in one run (RecoverWipe)
	WipeRecoverTimeout time.Duration // the members' corpse runs after a wipe
	SicknessMax        time.Duration // longest wait for resurrection sickness (WaitSickness)
}

// DefaultConfig is the Driver's limits for a live run.
func DefaultConfig() Config {
	return Config{
		WalkAttempts: 3, WalkRetryDelay: 2 * time.Second, UseSettle: 1500 * time.Millisecond, ItemsWait: 3 * time.Second,
		PullAttempts: 3, PullRange: 45, PullStandoff: 38, PackRadius: 25,
		EngageTimeout: 25 * time.Second, EvadeGrace: 12 * time.Second, AssistEvery: 10 * time.Second,
		FightTimeout: 300 * time.Second, BossTimeout: 600 * time.Second, PostFight: 4 * time.Second, AddsWait: 15 * time.Second,
		RestTimeout: 60 * time.Second, RestPoll: 3 * time.Second, RestHealthPct: 90, RestManaPct: 35, RestHealerManaPct: 50, DrinkNudge: 15 * time.Second, RestMinHealth: 50, GatherRadius: 40,
		WaitPoll: 2 * time.Second, Poll: time.Second, ArriveTimeout: 60 * time.Second,
		ManaStall: 30 * time.Second, ManaStallPct: 5, HealerManaMin: 30, ReviveWait: 5 * time.Second, AggroQuiet: 3 * time.Second,
		AmbushPause: 6 * time.Second, WalkWatch: 500 * time.Millisecond, BuffGap: 300 * time.Millisecond, BuffViewWait: 10 * time.Second, BuffMaxDist: 80,
		BrainRezWait: 120 * time.Second, ResWait: 60 * time.Second, ResAcceptWait: 10 * time.Second, ReleaseWait: 15 * time.Second, CorpseRunTimeout: 10 * time.Minute,
		MaxLeaderDeaths: 5, MaxWipeRecoveries: 2, WipeRecoverTimeout: 12 * time.Minute, SicknessMax: 11 * time.Minute,
	}
}

func (c Config) withDefaults() Config {
	d := DefaultConfig()
	fill := func(dst *time.Duration, def time.Duration) {
		if *dst <= 0 {
			*dst = def
		}
	}
	fillInt := func(dst *int, def int) {
		if *dst <= 0 {
			*dst = def
		}
	}
	fillF := func(dst *float32, def float32) {
		if *dst <= 0 {
			*dst = def
		}
	}
	fillInt(&c.WalkAttempts, d.WalkAttempts)
	fill(&c.WalkRetryDelay, d.WalkRetryDelay)
	fill(&c.UseSettle, d.UseSettle)
	fill(&c.ItemsWait, d.ItemsWait)
	fillInt(&c.PullAttempts, d.PullAttempts)
	fillF(&c.PullRange, d.PullRange)
	fillF(&c.PullStandoff, d.PullStandoff)
	fillF(&c.PackRadius, d.PackRadius)
	fill(&c.EngageTimeout, d.EngageTimeout)
	fill(&c.EvadeGrace, d.EvadeGrace)
	fill(&c.AssistEvery, d.AssistEvery)
	fill(&c.FightTimeout, d.FightTimeout)
	fill(&c.BossTimeout, d.BossTimeout)
	fill(&c.PostFight, d.PostFight)
	fill(&c.AddsWait, d.AddsWait)
	fill(&c.RestTimeout, d.RestTimeout)
	fill(&c.RestPoll, d.RestPoll)
	fillInt(&c.RestHealthPct, d.RestHealthPct)
	fillInt(&c.RestManaPct, d.RestManaPct)
	fillInt(&c.RestHealerManaPct, d.RestHealerManaPct)
	fill(&c.DrinkNudge, d.DrinkNudge)
	fillInt(&c.RestMinHealth, d.RestMinHealth)
	fillF(&c.GatherRadius, d.GatherRadius)
	fill(&c.WaitPoll, d.WaitPoll)
	fill(&c.Poll, d.Poll)
	fill(&c.ArriveTimeout, d.ArriveTimeout)
	fill(&c.ManaStall, d.ManaStall)
	fillInt(&c.ManaStallPct, d.ManaStallPct)
	fillInt(&c.HealerManaMin, d.HealerManaMin)
	fill(&c.ReviveWait, d.ReviveWait)
	fill(&c.AggroQuiet, d.AggroQuiet)
	fill(&c.AmbushPause, d.AmbushPause)
	fill(&c.WalkWatch, d.WalkWatch)
	fill(&c.BuffGap, d.BuffGap)
	fill(&c.BuffViewWait, d.BuffViewWait)
	fillF(&c.BuffMaxDist, d.BuffMaxDist)
	fill(&c.BrainRezWait, d.BrainRezWait)
	fill(&c.ResWait, d.ResWait)
	fill(&c.ResAcceptWait, d.ResAcceptWait)
	fill(&c.ReleaseWait, d.ReleaseWait)
	fill(&c.CorpseRunTimeout, d.CorpseRunTimeout)
	fillInt(&c.MaxLeaderDeaths, d.MaxLeaderDeaths)
	fillInt(&c.MaxWipeRecoveries, d.MaxWipeRecoveries)
	fill(&c.WipeRecoverTimeout, d.WipeRecoverTimeout)
	fill(&c.SicknessMax, d.SicknessMax)
	return c
}

// errWipe ends the run: every member of the party is dead (the leader may be too). RecoverWipe lets the party recover.
var errWipe = errors.New("wipe")

// Driver runs a Plan with the party: it asks a Stepper what to do, does it through the Game, and keeps the Report.
// Run blocks; Report can be read from another goroutine while it runs.
type Driver struct {
	plan *Plan
	g    Game
	cfg  Config
	clk  Clock
	rep  reporter

	st      *Stepper
	curLeg  int
	prevDea map[packet.GUID]bool // who was dead at the last look, to count each death once
	seen    bool
	party   map[packet.GUID]bool // guids of the leader and the members, for "who is the unit hitting"
	// buffedAt is the leg a member last had its buffs given or topped up at: a leg tops a member up once.
	buffedAt   map[packet.GUID]int
	brainNoted bool            // the brain failed to start once and was noted
	leaderBase map[uint32]bool // the leader's own auras before the buff batch under way
	lastOrder  string          // the intent of the last "/p .bot action ..." order, which a refusal whisper (no intent of its own) answers
	// skipped are the mobs the run gave up on (the tank could not reach them, they evaded, or they drew no fight twice):
	// no later pick or pull group looks at them again. The value says why, for the report.
	skipped    map[packet.GUID]string
	pendingRef []Refusal // refusals seen by a sleep and not yet looked at by a fight
	tg         groupTank // the tank-first state of the fights (driver_tank.go)

	gm             bool   // the leader's account is taken for a GM, level above 0 (decided in begin)
	level          int    // its account level: probed, asserted (gm_level, else developer) or 0
	caps           GMCaps // the GM commands that level may send (gmCapLevels)
	recovering     bool   // the leader's recovery is going on: its waits do not start another
	inWipe         bool   // a wipe is being recovered from: the waits do not report it again
	deathCounted   bool   // the leader's current death is in the report
	recoveries     int    // how often the leader came back; a walk does not count the interruption as a failed try
	leaderDeaths   int
	wipeRecoveries int

	tr          *tracerun.Recorder // the run id and the trace (nil-safe; no file when tracing is off)
	stopSampler func()
}

// NewDriver prepares a run of plan. A zero clock is the wall clock.
func NewDriver(plan *Plan, g Game, cfg Config, clk Clock) *Driver {
	if clk.Now == nil || clk.Sleep == nil {
		clk = RealClock()
	}
	d := &Driver{plan: plan, g: g, cfg: cfg.withDefaults(), clk: clk, curLeg: -1, prevDea: map[packet.GUID]bool{}, party: map[packet.GUID]bool{}, buffedAt: map[packet.GUID]int{}, skipped: map[packet.GUID]string{}}
	d.rep.init(plan, clk.Now)
	d.rep.r.SetupShortcuts = slices.Clone(cfg.Setup)
	d.rep.r.Buffs = append([]Buff{}, cfg.Buffs...)
	d.rep.r.Brain = cfg.BrainName
	d.initTrace()
	return d
}

// Report is a copy of the run report, usable while the run goes on.
func (d *Driver) Report() Report { return d.rep.snapshot() }

// Status is the report and whether the run has ended, read together under one lock. The run's end event is emitted before the
// report says it ended (finish), so ended true means dungeon_done or dungeon_failed is in the event log.
func (d *Driver) Status() (rep Report, ended bool) { return d.rep.snapshotEnded() }

// Run does the plan from leg fromLeg. It returns nil when the plan is done; the error says why it ended otherwise
// (also in the report): "wipe", a failed step, or the context's error for a stop.
func (d *Driver) Run(ctx context.Context, fromLeg int) error {
	if fromLeg < 0 || fromLeg >= len(d.plan.Legs) {
		return fmt.Errorf("from_leg %d: the plan has legs 0-%d", fromLeg, len(d.plan.Legs)-1)
	}
	d.rep.start(fromLeg)
	d.traceBegin(fromLeg)
	err := d.run(ctx, fromLeg)
	d.finish(err)
	return err
}

// finish ends the run: the end event goes out before the end state is published, so that a reader of the report (dungeon_status)
// that sees "done", "failed" or "stopped" finds dungeon_done or dungeon_failed in the event log, and a report that is still "running"
// is a run whose end has not been announced.
func (d *Driver) finish(err error) {
	leg := d.curLeg
	var state, errText, kind, text string
	switch {
	case err == nil:
		state, kind, text = "done", events.KindDungeonDone, "dungeon "+d.plan.Name+" done"
	case ctxDone(err):
		state, errText, kind, text = "stopped", "stopped", events.KindDungeonFailed, "dungeon "+d.plan.Name+" stopped"
	default:
		state, errText, kind, text = "failed", err.Error(), events.KindDungeonFailed, "dungeon "+d.plan.Name+" failed: "+err.Error()
	}
	d.endLeg(state)
	at := d.rep.now()
	data := d.rep.summaryEnded(state, errText, at)
	if err != nil {
		data["error"], data["leg"] = errText, leg
	}
	d.emit(kind, text, data)
	d.traceEnd(state, errText, at)
	d.rep.end(state, errText, at)
}

func ctxDone(err error) bool {
	return errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded)
}

func (d *Driver) run(ctx context.Context, fromLeg int) error {
	if err := d.begin(ctx, fromLeg); err != nil {
		return err
	}
	d.st = NewStepperAt(d.plan, stepWorld{d}, d.clk.Now, fromLeg)
	d.resume()
	waiting := [2]int{-1, -1} // the leg and step a wait event was sent for
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		a := d.st.Next()
		d.noteSkipped()
		d.rep.setCurrent(a.Label)
		if a.Kind != ActFailed && a.Leg != d.curLeg {
			d.endLeg("done")
			if a.Kind != ActDone {
				if err := d.startLeg(ctx, a.Leg); err != nil {
					d.st.Fail(err.Error())
					d.countWipe(err)
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
				d.emit(events.KindDungeonStep, a.Label, map[string]any{"leg": a.Leg, "step": a.Step, "action": string(a.Kind)})
			}
			if err := d.sleep(ctx, min(d.cfg.WaitPoll, a.Remaining)); err != nil {
				d.stepFailed(err)
			}
			continue
		}
		began := d.clk.Now()
		var err error
		switch a.Kind {
		case ActWalk:
			err = d.tracedWalk(ctx, a.To)
		case ActUse:
			err = d.doUse(ctx, a)
		case ActPull:
			err = d.doPull(ctx, a)
		case ActKill:
			err = d.doKillAndLoot(ctx, a)
		case ActLoot:
			err = d.doLoot(ctx, a)
		case ActItem:
			err = d.doUseItem(ctx, a)
		default:
			err = fmt.Errorf("unknown action %q", a.Kind)
		}
		data := map[string]any{"leg": a.Leg, "step": a.Step, "action": string(a.Kind), "ok": err == nil, "seconds": d.clk.Now().Sub(began).Seconds()}
		if err != nil {
			data["error"] = err.Error()
		}
		d.emit(events.KindDungeonStep, a.Label, data)
		if err != nil {
			d.stepFailed(err)
			continue
		}
		d.st.Complete()
	}
}

// stepFailed ends the plan with err (a wipe is counted and named as such); the loop then returns it.
func (d *Driver) stepFailed(err error) {
	d.countWipe(err)
	if errors.Is(err, errWipe) {
		d.st.Fail("wipe")
		return
	}
	d.st.Fail(err.Error())
}

func (d *Driver) countWipe(err error) {
	if errors.Is(err, errWipe) {
		d.rep.update(func(r *Report) { r.Wipes++ })
		d.tr.Event("wipe", map[string]any{"leg": d.curLeg, "error": err.Error()})
		d.rep.note(err.Error())
	}
}

// begin checks that the run can start: the leader is in a group and on the map (or, with Teleport, is put there).
func (d *Driver) begin(ctx context.Context, fromLeg int) error {
	if len(d.g.Party()) == 0 {
		return errors.New("the leader has no party: invite the companions first (invite, hire)")
	}
	d.observeInit()
	if err := d.setupGM(ctx); err != nil {
		return err
	}
	if err := d.checkRequiredItems(fromLeg); err != nil {
		return err
	}
	if err := d.checkLootRules(fromLeg); err != nil {
		return err
	}
	start, what := d.startPoint(fromLeg)
	if fromLeg > 0 && !d.cfg.Teleport {
		// Without teleport the party walks from where it stands to the leg: the packs of the earlier legs on that way are
		// fought as walk aggros (live run 2: from_leg keystone from the entrance was a 167 s walk and 7 fights).
		d.rep.note(fmt.Sprintf("from_leg %d without teleport: the run starts where the party stands and assumes the legs before it are cleared; packs of the earlier legs on the walk are fought as they aggro", fromLeg))
	}
	switch {
	case d.cfg.Teleport && (d.g.Map() != d.plan.Map || dist3(d.g.Position(), start) > 15):
		d.shortcut("teleport_to_"+what, fromLeg, fmt.Sprintf("GM teleport to %.1f %.1f %.1f on map %d", start.X, start.Y, start.Z, d.plan.Map))
		// The lock stays on across the wait for the arrival, so that no step of a brain started by hand carries the leader off meanwhile.
		if err := d.holdingMove(ctx, func() error {
			if err := d.g.Teleport(ctx, start, d.plan.Map); err != nil {
				return fmt.Errorf("teleport to the %s: %w", what, err)
			}
			return d.sleepHeld(ctx, 3*time.Second)
		}); err != nil {
			return err
		}
		if err := d.check(ctx); err != nil { // the look the wait would have made, now that the lock is let go
			return err
		}
		if err := d.gather(ctx); err != nil {
			return err
		}
	case d.g.Map() != d.plan.Map:
		return fmt.Errorf("the leader is on map %d and the plan needs map %d: enter the dungeon first, or run with teleport", d.g.Map(), d.plan.Map)
	}
	return d.startBuffs(ctx, fromLeg)
}

// setupGM settles the leader's account level and from it the GM capabilities of the run (gmCapLevels), then makes the
// leader visible when it may. The level comes from the option (Config.GM): none (0), the probe, or asserted (Config.GMLevel,
// developer when not given). A probe with no clear answer counts as a player: the server answers ".account" to any account
// (SEC_PLAYER, PlayerCommands = 1) or says it aloud (PlayerCommands = 0), so silence means the probe broke, not that the
// leader is a GM. An option that needs a capability the level lacks fails the run before anything is sent.
func (d *Driver) setupGM(ctx context.Context) error {
	unknown := ""
	switch d.cfg.GM {
	case GMNone:
		d.level = 0
	case GMProbe:
		level, known, err := d.g.ProbeAccount(ctx)
		switch {
		case err != nil:
			unknown = "could not tell the leader's account level (" + err.Error() + ")"
		case !known:
			unknown = "the server did not answer .account"
		default:
			d.level = max(level, 0)
			d.rep.update(func(r *Report) { l := level; r.AccountLevel = &l })
			if level > 0 {
				d.rep.note(fmt.Sprintf("the leader's account level is %d", level))
			} else {
				d.rep.note("the leader's account level is 0 (a player): the run sends no GM command")
			}
		}
		if unknown != "" {
			d.level = 0
			unknown += "; the leader is taken for a player and the run sends no GM command (pass gm: true, and gm_level, to override)"
			d.rep.note(unknown)
		}
	default:
		d.level = d.cfg.GMLevel
		if d.level <= 0 {
			d.level = DeveloperLevel
		}
		if d.cfg.GMLevel > 0 {
			d.rep.note(fmt.Sprintf("the leader is taken for a GM of account level %d (gm_level)", d.level))
		}
	}
	d.gm = d.level > 0
	d.caps = capsFor(d.level)
	d.rep.update(func(r *Report) { r.LeaderGM, r.GMCaps = d.gm, d.caps })
	if d.caps.Visible {
		d.rep.note("leader made visible with .gm visible on")
		if err := d.g.GM(ctx, ".gm visible on"); err != nil {
			d.rep.note("could not send .gm visible on: " + err.Error())
		}
	} else if d.gm {
		d.rep.note(fmt.Sprintf(".gm visible on needs account level %d; the leader's is %d: not sent", ModeratorLevel, d.level))
	}
	if !d.caps.Revive && d.cfg.Shortcuts {
		d.cfg.Shortcuts = false
		if d.gm {
			d.rep.note(fmt.Sprintf("GM revives need account level %d (.revive); the leader's is %d: dead members are not revived by GM (recover_wipe runs them back to their corpses)", DeveloperLevel, d.level))
		}
	}
	if d.cfg.Teleport && !d.caps.Teleport {
		return d.capError("teleport needs", ".go xyz", ObserverLevel, "stand at the entrance and run without teleport", unknown)
	}
	if len(d.cfg.Buffs) > 0 && !d.caps.Buffs {
		return d.capError("buffs need", ".aura", DeveloperLevel, "run without buffs", unknown)
	}
	return nil
}

// capError is the error of an option (what: "teleport needs") whose GM command (cmd) needs account level need, above the leader's.
func (d *Driver) capError(what, cmd string, need int, instead, unknown string) error {
	if d.level == 0 {
		msg := fmt.Sprintf("%s a GM account (%s, level %d); the leader is a player: %s", what, cmd, need, instead)
		if unknown != "" {
			msg += " (the account level is unknown: pass gm: true, and gm_level, if the leader is a GM)"
		}
		return errors.New(msg)
	}
	return fmt.Errorf("%s account level %d (%s); the leader's account level is %d: %s", what, need, cmd, d.level, instead)
}

// startPoint is where a run from leg i begins: the entrance, or where the leg before ended.
func (d *Driver) startPoint(i int) (Point, string) {
	if i == 0 {
		return d.plan.Entrance, "entrance"
	}
	l := d.plan.Legs[i-1]
	if l.Boss != nil {
		return l.Boss.Position, "leg_start"
	}
	return l.To, "leg_start"
}

// gather brings the party to the leader after a teleport: summon each companion, then follow.
func (d *Driver) gather(ctx context.Context) error {
	for _, m := range d.g.Party() {
		if m.Online && !m.Dead {
			if err := d.g.GM(ctx, ".bot summon "+m.Name); err != nil {
				return fmt.Errorf("summon %s: %w", m.Name, err)
			}
		}
	}
	deadline := d.clk.Now().Add(d.cfg.ArriveTimeout)
	for {
		if err := d.sleep(ctx, d.cfg.Poll*2); err != nil {
			return err
		}
		if missing := d.notNear(); len(missing) == 0 {
			break
		} else if d.clk.Now().After(deadline) {
			return fmt.Errorf("the party did not reach the leader in %.0f s: %v", d.cfg.ArriveTimeout.Seconds(), missing)
		}
	}
	return d.chat(ctx, ".bot action follow")
}

// notNear names the living online members who are not in view within GatherRadius of the leader.
func (d *Driver) notNear() []string {
	var out []string
	units := d.g.Units()
	for _, m := range d.g.Party() {
		if !m.Online || m.Dead {
			continue
		}
		near := false
		for _, u := range units {
			if u.Type == "player" && (u.GUID == m.GUID || u.Name == m.Name) && u.Distance >= 0 && u.Distance <= d.cfg.GatherRadius {
				near = true
			}
		}
		if !near {
			out = append(out, m.Name)
		}
	}
	return out
}

func (d *Driver) shortcut(kind string, leg int, detail string) {
	d.rep.update(func(r *Report) {
		r.Shortcuts = append(r.Shortcuts, Shortcut{Kind: kind, Leg: leg, Detail: detail, At: d.clk.Now()})
	})
	d.tr.Event("gm_shortcut", map[string]any{"kind": kind, "leg": leg, "detail": detail})
}

// startLeg opens a leg's report and event, and waits for the party to be rested.
func (d *Driver) startLeg(ctx context.Context, i int) error {
	d.curLeg = i
	name := d.plan.Legs[i].Name
	d.rep.update(func(r *Report) {
		r.Legs = append(r.Legs, LegReport{Leg: i, Name: name, Started: d.clk.Now(), Result: "running"})
	})
	d.emit(events.KindDungeonLeg, fmt.Sprintf("leg %d %s: start", i, name), map[string]any{"leg": i, "name": name, "phase": "start"})
	if err := d.topUpBuffs(ctx); err != nil {
		return err
	}
	return d.restGate(ctx)
}

// endLeg closes the current leg's report with result and sends its event; it does nothing when no leg is open.
func (d *Driver) endLeg(result string) {
	d.brainOff()
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
		"pulls": l.Pulls, "pull_retries": l.PullRetries, "rest_seconds": l.RestSeconds})
}

func (d *Driver) legStat(f func(*LegReport)) {
	d.rep.update(func(r *Report) {
		if n := len(r.Legs); n > 0 {
			f(&r.Legs[n-1])
		}
	})
}

// sleep waits d, then looks at the party (check): deaths are counted, a dead leader is brought back, and a wipe ends the
// wait with errWipe.
func (d *Driver) sleep(ctx context.Context, dur time.Duration) error {
	if err := d.clk.Sleep(ctx, dur); err != nil {
		return err
	}
	d.drainRefusals()
	return d.check(ctx)
}

// sleepHeld is sleep for a wait made under the brain's move lock: it only waits (and drains refusals), with no look at the party. A leader
// found dead would start its recovery (a corpse run, a rest gate) with the lock held for all of it; the caller lets the lock go and
// then calls check.
func (d *Driver) sleepHeld(ctx context.Context, dur time.Duration) error {
	if err := d.clk.Sleep(ctx, dur); err != nil {
		return err
	}
	d.drainRefusals()
	return nil
}

func (d *Driver) observeInit() {
	self := d.g.Self()
	d.party = map[packet.GUID]bool{self.GUID: true}
	d.prevDea[self.GUID] = self.Dead()
	for _, m := range d.g.Party() {
		d.party[m.GUID] = true
		d.prevDea[m.GUID] = m.Dead
	}
	d.seen = true
}

// observe counts each death once (leader or member) and returns errWipe when every member is dead.
func (d *Driver) observe() error {
	if d.countDeaths() {
		return fmt.Errorf("%w: the whole party is dead", errWipe)
	}
	return nil
}

// countDeaths counts each death once (leader or member) and reports whether every online member is dead.
func (d *Driver) countDeaths() (wholePartyDead bool) {
	d.traceSample()
	if !d.seen {
		d.observeInit()
	}
	self := d.g.Self()
	died := func(name string, g packet.GUID, dead bool) {
		if dead && !d.prevDea[g] {
			d.rep.update(func(r *Report) {
				r.Deaths = append(r.Deaths, Death{Name: name, Leg: d.curLeg, At: d.clk.Now()})
				if n := len(r.Legs); n > 0 && d.curLeg >= 0 {
					r.Legs[n-1].Deaths++
				}
			})
			d.tr.Event("death", map[string]any{"name": name, "a": guidText(g), "leg": d.curLeg})
		}
		d.prevDea[g] = dead
	}
	died(self.Name, self.GUID, self.Dead())
	members := d.g.Party()
	allDown, anyDead := len(members) > 0, false
	for _, m := range members {
		d.party[m.GUID] = true
		died(m.Name, m.GUID, m.Dead)
		if m.Dead {
			anyDead = true
		} else if m.Online {
			allDown = false
		}
	}
	return allDown && anyDead
}

// stepWorld is the Stepper's World over a Game.
type stepWorld struct{ d *Driver }

func (w stepWorld) Position() Point                   { return w.d.g.Position() }
func (w stepWorld) GameObjects() []objects.GameObject { return w.d.g.GameObjects() }
func (w stepWorld) PartyNear(c Point, r float32) bool {
	if dist3(w.d.g.Position(), c) <= float64(r) {
		return true
	}
	in := w.d.party
	for _, u := range w.d.g.Units() {
		if u.Type == "player" && in[u.GUID] && dist3(Point{u.X, u.Y, u.Z}, c) <= float64(r) {
			return true
		}
	}
	return false
}

func (w stepWorld) HostilesWithin(c Point, r float32) int {
	n := 0
	for _, u := range w.d.g.Units() {
		if u.Type != "player" && u.Hostile && !u.Dead && u.Distance >= 0 && dist3(Point{u.X, u.Y, u.Z}, c) <= float64(r) {
			n++
		}
	}
	return n
}

// doWalk walks to p, trying up to WalkAttempts times: walk_path replans by itself, a second try covers a walk it
// gave up on (a root that outlasted its wait, a server move too many). A pack that aggros on the way stops the walk:
// the party fights it until the fight is over (walkAggro), then the walk goes on. That does not use up a try.
func (d *Driver) doWalk(ctx context.Context, p Point) error {
	var last string
	aggros := 0
	for try := 1; try <= d.cfg.WalkAttempts; {
		if d.partyInCombat() {
			if aggros++; aggros > maxWalkAggros {
				return fmt.Errorf("walk to %.1f %.1f %.1f: the party was pulled into a fight %d times on the way", p.X, p.Y, p.Z, maxWalkAggros)
			}
			if err := d.walkAggro(ctx); err != nil {
				return err
			}
			continue
		}
		wctx, cancel := context.WithCancel(ctx)
		stop := d.watchCombat(wctx, cancel)
		recovered := d.recoveries
		r, err := d.walkPath(wctx, p)
		aggro := stop()
		cancel()
		if err := ctx.Err(); err != nil {
			return err
		}
		if oerr := d.check(ctx); oerr != nil {
			return oerr
		}
		if d.recoveries != recovered { // the leader died on the way and came back: the walk is made again, from where it stands
			continue
		}
		if aggro {
			if aggros++; aggros > maxWalkAggros {
				return fmt.Errorf("walk to %.1f %.1f %.1f: the party was pulled into a fight %d times on the way", p.X, p.Y, p.Z, maxWalkAggros)
			}
			if err := d.walkAggro(ctx); err != nil {
				return err
			}
			continue
		}
		switch {
		case err != nil:
			last = err.Error()
		case r.Result == "arrived" || dist3(d.g.Position(), p) <= ArriveRadius:
			return nil
		default:
			last = fmt.Sprintf("%s: %s, %.1f yd left", r.Result, r.Reason, r.DistLeft)
		}
		if try < d.cfg.WalkAttempts {
			if err := d.sleep(ctx, d.cfg.WalkRetryDelay); err != nil {
				return err
			}
		}
		try++
	}
	return fmt.Errorf("walk to %.1f %.1f %.1f failed after %d tries (%s)", p.X, p.Y, p.Z, d.cfg.WalkAttempts, last)
}

// holdTimeout bounds the wait for a step of the brain's to stop when the Driver takes the brain's move lock; a step that has not
// stopped by then fails the movement like a refused walk (a walk would be refused by the session anyway).
const holdTimeout = 5 * time.Second

// holdMove takes the brain's move lock for one movement of the leader; the caller defers the release it returns. Without a
// brain it holds nothing. It runs on the Driver's goroutine, never inside the session's do loop (see Brain.HoldMove).
func (d *Driver) holdMove(ctx context.Context) (func(), error) {
	if d.cfg.Brain == nil {
		return func() {}, nil
	}
	hctx, cancel := context.WithTimeout(ctx, holdTimeout)
	defer cancel()
	release, err := d.cfg.Brain.HoldMove(hctx)
	if err != nil {
		if ctx.Err() == nil {
			err = fmt.Errorf("the brain's own step did not stop within %s: %w", holdTimeout, err)
		}
		return func() {}, err
	}
	return release, nil
}

// The calls that move the leader, each under the brain's move lock, and the only places the Driver calls them: walk_path (doWalk
// for the plan's walks, approaches and stand points; ghostWalk for the corpse run), the GM teleport to a leg's start (held across the
// wait for the arrival), the release of the spirit (the graveyard teleport, held across the wait for the ghost) and the dungeon portal's
// area trigger. A use_object is held with its settle wait (doUse), since the leader must stay where the walk put it.

func (d *Driver) walkPath(ctx context.Context, to Point) (WalkResult, error) {
	release, err := d.holdMove(ctx)
	if err != nil {
		return WalkResult{}, err
	}
	defer release()
	return d.g.WalkPath(ctx, to)
}

// holdingMove runs fn under one hold of the move lock: for a movement together with the wait for its arrival.
func (d *Driver) holdingMove(ctx context.Context, fn func() error) error {
	release, err := d.holdMove(ctx)
	if err != nil {
		return err
	}
	defer release()
	return fn()
}

func (d *Driver) areaTrigger(ctx context.Context, id uint32) error {
	release, err := d.holdMove(ctx)
	if err != nil {
		return err
	}
	defer release()
	return d.g.AreaTrigger(ctx, id)
}

// brainOn starts the leader's brain (a no-op when there is none or it runs); a failure to start is noted once.
func (d *Driver) brainOn() {
	if d.cfg.Brain == nil {
		return
	}
	if err := d.cfg.Brain.Start(); err != nil && !d.brainNoted {
		d.brainNoted = true
		d.rep.note("the leader's bot brain did not start: " + err.Error())
	}
}

// brainOff stops the leader's brain: legs end with it off.
func (d *Driver) brainOff() {
	if d.cfg.Brain != nil {
		d.cfg.Brain.Stop()
	}
}

// maxWalkAggros bounds the fights that stop one walk, so a walk that keeps running into packs ends the run.
const maxWalkAggros = 12

// attackers are the living hostile units in combat with the party within 60 yards: what "the party is in combat"
// means to the walk and the rest gate.
func (d *Driver) attackers() []objects.Unit {
	return attackersOf(d.g.Units(), d.party)
}

func attackersOf(units []objects.Unit, party map[packet.GUID]bool) []objects.Unit {
	var out []objects.Unit
	for _, u := range units {
		if u.Type != "player" && u.Hostile && !u.Dead && u.InCombat && u.Distance >= 0 && u.Distance <= 60 && party[u.Target] {
			out = append(out, u)
		}
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].Distance < out[j].Distance })
	return out
}

func (d *Driver) partyInCombat() bool { return len(d.attackers()) > 0 }

// watchCombat looks, in real time every WalkWatch, for the party being in combat while a walk goes on and calls
// cancel when it is (that stops the walk). The returned func ends the look and says whether it cancelled. It reads
// the game from another goroutine, which the Game must allow (the session does).
func (d *Driver) watchCombat(ctx context.Context, cancel context.CancelFunc) func() bool {
	party := make(map[packet.GUID]bool, len(d.party))
	for g := range d.party {
		party[g] = true
	}
	done, finished := make(chan struct{}), make(chan struct{})
	fired := false
	go func() {
		defer close(finished)
		t := time.NewTicker(d.cfg.WalkWatch)
		defer t.Stop()
		for {
			select {
			case <-done:
				return
			case <-ctx.Done():
				return
			case <-t.C:
				if len(attackersOf(d.g.Units(), party)) > 0 {
					fired = true
					cancel()
					return
				}
				if d.g.Self().Dead() { // not a fight: the walk ends and the caller finds the leader dead
					cancel()
					return
				}
			}
		}
	}()
	return func() bool {
		close(done)
		<-finished
		return fired
	}
}

// walkAggro fights what came at the party during a walk (or was still on it when a walk was due): the leader orders
// the party onto the nearest attacker, again every AssistEvery, until nothing has hit the party for AggroQuiet.
// It counts as an extra pull of the leg and is noted in the report.
func (d *Driver) walkAggro(ctx context.Context) error {
	d.brainOn()
	first := d.attackers()
	name := "a pack"
	if len(first) > 0 {
		name = first[0].Name
	}
	d.legStat(func(l *LegReport) {
		l.Pulls++
		l.WalkAggros++
	})
	d.rep.note(fmt.Sprintf("leg %d: %s aggroed during a walk; the walk was stopped and the party fought it", d.curLeg, name))
	if err := d.fightAttackers(ctx); err != nil {
		return err
	}
	return d.afterFight(ctx)
}

// fightAttackers orders the party onto the nearest attacker until none has hit it for AggroQuiet (or FightTimeout).
func (d *Driver) fightAttackers(ctx context.Context) error {
	began := d.clk.Now()
	lastSeen := began
	var lastAssist time.Time
	for {
		now := d.clk.Now()
		att := d.attackers()
		if len(att) > 0 {
			lastSeen = now
			if err := d.tankFirstGroup(ctx, att); err != nil {
				return err
			}
			if lastAssist.IsZero() || now.Sub(lastAssist) >= d.cfg.AssistEvery || d.focusGoneIn(att) {
				if t, ok := d.pickAssist(att); ok {
					lastAssist = now
					if _, err := d.assistOrder(ctx, t, att); err != nil {
						return err
					}
				}
			}
		} else if now.Sub(lastSeen) >= d.cfg.AggroQuiet {
			return nil
		}
		if now.Sub(began) > d.cfg.FightTimeout {
			return fmt.Errorf("the fight on the way lasted more than %.0f s", d.cfg.FightTimeout.Seconds())
		}
		if err := d.sleep(ctx, d.cfg.Poll); err != nil {
			return err
		}
	}
}

// order says ".bot action <intent>" in party chat and remembers the intent: a bot's whisper refusing it carries none.
func (d *Driver) order(ctx context.Context, intent string) error {
	d.lastOrder = intent
	return d.chat(ctx, ".bot action "+intent)
}

// doUse uses the object under the move lock, the settle wait included: a brain step must not carry the leader out of the object's reach
// between the use and the world's answer. The waits under the lock do not look at the party (sleepHeld); the look (check) is made after
// the lock is let go, so a leader that died meanwhile is brought back without the lock held.
func (d *Driver) doUse(ctx context.Context, a Action) error {
	if err := d.checkUseItem(ctx, a.Requires); err != nil {
		return err
	}
	uerr := d.holdingMove(ctx, func() error { return d.use(ctx, a) })
	if err := d.check(ctx); err != nil {
		return err
	}
	return uerr
}

func (d *Driver) use(ctx context.Context, a Action) error {
	var err error
	for try := 0; try < 2; try++ {
		if err = d.g.UseObject(ctx, a.GUID); err == nil {
			break
		}
		if cerr := ctx.Err(); cerr != nil {
			return cerr
		}
		if serr := d.sleepHeld(ctx, time.Second); serr != nil {
			return serr
		}
	}
	if err != nil {
		return fmt.Errorf("use game object %s: %w", a.GUID, err)
	}
	return d.sleepHeld(ctx, d.cfg.UseSettle)
}

// restGate waits until every living online member is rested (health and mana at the thresholds, near the leader),
// ordering the bots to eat and drink. It starts only once the party is out of combat: while it is fighting the gate
// waits for the fight to end (helping with the attack order), and its timeout counts from the end of the fight.
// A dead member is revived by GM when shortcuts are on (flagged), and given ReviveWait to show alive before it is
// judged. A caster whose mana has not risen ManaStallPct of its maximum for ManaStall (it does not drink) is let
// pass when everything else is rested, with a note; a healer (HealerClasses) below HealerManaMin percent is not. After RestTimeout the run goes on if everyone has at least
// RestMinHealth percent, else it fails.
func (d *Driver) restGate(ctx context.Context) (err error) {
	defer d.traceRest(d.clk.Now())
	if b := d.cfg.Brain; b != nil {
		d.brainOn()
		b.Rest(true)
		defer b.Rest(false)
	}
	began := d.clk.Now()
	tgt := restTargetFor(d.cfg, d.plan, d.curLeg)
	rec := newGateRec(max(d.curLeg, 0), tgt, began)
	defer func() { rec.finish(&d.rep, d.clk.Now(), err) }()
	ordered, refusedSeen := false, false
	calm := began // the rest timeout counts from here: the start of the gate, or the end of the last fight
	var fightBegan, lastAssist time.Time
	revived := map[packet.GUID]int{}
	rebuff := map[packet.GUID]bool{} // revived members still to get their buffs back
	var rez rezWatch                 // the wait for the leader's brain to resurrect the dead members
	rw := newRestWatch(tgt)
	// Whatever resets the other clocks (an attacker that flickers across the 60 yd line, a mob that evades but keeps
	// its combat flag), the gate as a whole is bounded.
	limit := tgt.max + 2*d.cfg.FightTimeout
	for {
		if el := d.clk.Now().Sub(began); el > limit {
			return fmt.Errorf("the rest gate has gone on for more than %.0f s (fights keep interrupting it): the party is not rested", limit.Seconds())
		}
		if att := d.attackers(); len(att) > 0 {
			now := d.clk.Now()
			if fightBegan.IsZero() {
				fightBegan = now
			}
			if now.Sub(fightBegan) > d.cfg.FightTimeout {
				return fmt.Errorf("the party is still in combat after %.0f s at a rest gate", d.cfg.FightTimeout.Seconds())
			}
			if err := d.tankFirstGroup(ctx, att); err != nil {
				return err
			}
			if lastAssist.IsZero() || now.Sub(lastAssist) >= d.cfg.AssistEvery || d.focusGoneIn(att) {
				if t, ok := d.pickAssist(att); ok {
					lastAssist = now
					if _, err := d.assistOrder(ctx, t, att); err != nil {
						return err
					}
				}
			}
			ordered, calm = false, now
			clear(rw.mana)
			if err := d.sleep(ctx, d.cfg.Poll); err != nil {
				return err
			}
			continue
		}
		fightBegan, lastAssist = time.Time{}, time.Time{}
		st := d.restState(rw)
		rec.g.Short = st.short
		if len(st.dead) == 0 {
			rez = rezWatch{}
		} else if d.brainRezWaiting(&rez) {
			for _, m := range st.dead {
				rebuff[m.GUID] = true
			}
			calm = d.clk.Now()
			if err := d.sleep(ctx, d.cfg.RestPoll); err != nil {
				return err
			}
			continue
		}
		if len(st.dead) > 0 && !d.cfg.Shortcuts && d.cfg.RecoverWipe {
			if err := d.recoverDeadMembers(ctx); err != nil {
				// The leader died while the members' recovery went on and nobody is left alive: that is a wipe, which RecoverWipe (on
				// here) recovers from instead of ending the run.
				if errors.Is(err, errWipe) {
					return d.recoverWipe(ctx, err)
				}
				return err
			}
			calm = d.clk.Now()
			continue
		}
		for _, m := range st.dead {
			if !d.cfg.Shortcuts {
				return fmt.Errorf("%s is dead and GM shortcuts are off (recover_wipe lets the party run back to its corpses)", m.Name)
			}
			if revived[m.GUID] >= 2 {
				return fmt.Errorf("%s is still dead after two GM revives", m.Name)
			}
			revived[m.GUID]++
			d.shortcut("revive", max(d.curLeg, 0), "GM .revive "+m.Name)
			if err := d.g.GM(ctx, ".revive "+m.Name); err != nil {
				return fmt.Errorf("revive %s: %w", m.Name, err)
			}
			// The party list follows the revive a moment later: look for the dead flag to clear before judging, and
			// never send the second revive before then.
			if err := d.waitRevived(ctx, m.GUID); err != nil {
				return err
			}
			calm = d.clk.Now()
			rebuff[m.GUID] = true
		}
		if len(st.dead) > 0 {
			continue
		}
		for g := range rebuff {
			if !slices.ContainsFunc(d.g.Party(), func(m Member) bool { return m.GUID == g && m.Dead }) {
				delete(rebuff, g)
				if err := d.rebuff(ctx, g); err != nil {
					return err
				}
				calm = d.clk.Now()
			}
		}
		if st.ok {
			if len(st.stalled) > 0 {
				rec.g.Result = "stalled"
				d.rep.note(fmt.Sprintf("leg %d: rest gate passed with %s; mana did not rise in %.0f s", d.curLeg, strings.Join(st.stalled, ", "), d.cfg.ManaStall.Seconds()))
			}
			return nil
		}
		// The party rest order was refused (TB #916: "No scoped bot accepted the mature action"): the bots are asked one by one.
		if ordered && !refusedSeen && d.restRefused() {
			refusedSeen, rec.g.PartyRestRefused = true, true
			if err := d.orderEach(ctx, rw, rec, st, "the party rest order was refused"); err != nil {
				return err
			}
		}
		for _, m := range st.nudge {
			if rw.nudged[m.GUID] {
				continue
			}
			rw.nudged[m.GUID] = true
			rec.nudged(m.Name)
			if err := d.chat(ctx, drinkOrder(m.Name, "drink")); err != nil {
				return err
			}
			d.rep.note(fmt.Sprintf("leg %d: %s mana %d%% did not rise in %.0f s; sent %q", d.curLeg, m.Name, m.Pct, d.cfg.DrinkNudge.Seconds(), drinkOrder(m.Name, "drink")))
		}
		if d.clk.Now().Sub(calm) > tgt.max {
			if st.minHealth >= d.cfg.RestMinHealth {
				rec.g.Result = "gave_up"
				d.rep.note(fmt.Sprintf("leg %d: rest gate gave up after %.0f s (%s); went on with at least %d%% health", d.curLeg, tgt.max.Seconds(), st.why, st.minHealth))
				return nil
			}
			return fmt.Errorf("the party is not rested after %.0f s: %s", tgt.max.Seconds(), st.why)
		}
		if !ordered {
			ordered = true
			d.restRefused() // an old refusal is not an answer to this order
			if err := d.chat(ctx, ".bot action rest"); err != nil {
				return err
			}
		}
		if err := d.sleep(ctx, d.cfg.RestPoll); err != nil {
			return err
		}
	}
}

// waitRevived polls the party list for up to ReviveWait until the member's dead flag is clear (the sleep also counts
// deaths and notices a wipe). A member still dead afterwards is left to the caller.
func (d *Driver) waitRevived(ctx context.Context, g packet.GUID) error {
	deadline := d.clk.Now().Add(d.cfg.ReviveWait)
	for {
		if err := d.sleep(ctx, d.cfg.Poll); err != nil {
			return err
		}
		still := false
		for _, m := range d.g.Party() {
			if m.GUID == g && m.Dead {
				still = true
			}
		}
		if !still || !d.clk.Now().Before(deadline) {
			return nil
		}
	}
}

// HealerClasses are the class ids (UNIT_FIELD_BYTES_0) taken for the party's healer: paladin, priest, shaman, druid.
// The party list carries no class; the unit rows of the members in view do.
var HealerClasses = map[uint8]bool{2: true, 5: true, 7: true, 11: true}

// manaWatch is the window a rest gate keeps for a caster below its mana target: its mana when the window began (power, since: the
// ManaStall rule), and the highest mana seen with when it was reached (base, baseAt: the DrinkNudge rule).
type manaWatch struct {
	power  uint32
	since  time.Time
	base   uint32
	baseAt time.Time
}

type restReport struct {
	ok        bool
	why       string   // what is missing, for the report
	short     []string // the same as a list ("Name mana 29%"), for the gate record
	minHealth int      // lowest health percent among the living online members and the leader
	dead      []Member
	stalled   []string    // casters let pass because their mana does not rise ("Name 49%")
	shortMana []shortMana // members below their mana target and not let pass
	shortHP   []string    // names of members below the health target
	nudge     []shortMana // members whose mana has not risen at all in DrinkNudge and who have had no drink order yet
}

// restState looks at the party. rw holds a window per caster below its mana target: when it has run for ManaStall
// and the caster gained less than ManaStallPct of its maximum, the caster does not hold the gate (it is not drinking);
// else the window starts again. A healer is held to the healer target, anyone else to the other one.
func (d *Driver) restState(rw *restWatch) restReport {
	r := restReport{ok: true, minHealth: 100}
	var low []string
	now := d.clk.Now()
	tgt := rw.tgt
	classes := map[packet.GUID]uint8{}
	for _, u := range d.g.Units() {
		if u.Type == "player" && u.Class != 0 {
			classes[u.GUID] = u.Class
		}
	}
	pct := func(v, max uint32) int {
		if max == 0 {
			return 100
		}
		return int(v * 100 / max)
	}
	if s := d.g.Self(); s.MaxHealth > 0 {
		h := pct(s.Health, s.MaxHealth)
		r.minHealth = min(r.minHealth, h)
		if h < tgt.health {
			r.ok = false
			low = append(low, fmt.Sprintf("leader %d%%", h))
		}
	}
	for _, m := range d.g.Party() {
		if !m.Online {
			continue
		}
		if m.Dead {
			r.dead = append(r.dead, m)
			r.ok = false
			continue
		}
		h := pct(m.Health, m.MaxHealth)
		r.minHealth = min(r.minHealth, h)
		if h < tgt.health {
			r.ok = false
			low = append(low, fmt.Sprintf("%s health %d%%", m.Name, h))
			r.shortHP = append(r.shortHP, m.Name)
		}
		if m.PowerType == 0 && m.MaxPower > 0 {
			healer := HealerClasses[classes[m.GUID]]
			p := pct(m.Power, m.MaxPower)
			if p >= tgt.manaNeed(healer) {
				delete(rw.mana, m.GUID)
				continue
			}
			w, nudge := rw.look(d.cfg, now, m.GUID, m.Power)
			sm := shortMana{GUID: m.GUID, Name: m.Name, Pct: p}
			if nudge {
				r.nudge = append(r.nudge, sm)
			}
			// A healer that low is waited for like before: the party would go on without its heals.
			healerLow := healer && p < d.cfg.HealerManaMin
			if !healerLow && rw.stalled(d.cfg, now, m.GUID, w, m.Power, m.MaxPower) {
				r.stalled = append(r.stalled, fmt.Sprintf("%s not drinking (mana %d%%)", m.Name, p))
			} else {
				r.ok = false
				low = append(low, sm.String())
				r.shortMana = append(r.shortMana, sm)
			}
		}
	}
	if far := d.notNear(); len(far) > 0 {
		r.ok = false
		low = append(low, fmt.Sprintf("not near the leader: %v", far))
	}
	low = append(low, r.stalled...)
	r.short = low
	r.why = fmt.Sprint(low)
	return r
}

// restRefused says whether TortoiseBots refused a rest order since the last look, and forgets those refusals.
func (d *Driver) restRefused() bool {
	found := false
	d.pendingRef = slices.DeleteFunc(d.pendingRef, func(r Refusal) bool {
		if r.Intent == "rest" || r.Intent == "drink" || r.Intent == "eat" {
			found = true
			return true
		}
		return false
	})
	return found
}

// orderEach sends the members that are short a drink (mana) or eat (health) order of their own, one each per gate.
func (d *Driver) orderEach(ctx context.Context, rw *restWatch, rec *gateRec, st restReport, why string) error {
	for _, m := range st.shortMana {
		if rw.nudged[m.GUID] {
			continue
		}
		rw.nudged[m.GUID] = true
		rec.nudged(m.Name)
		if err := d.chat(ctx, drinkOrder(m.Name, "drink")); err != nil {
			return err
		}
		d.rep.note(fmt.Sprintf("leg %d: %s: sent %q (%s mana %d%%)", d.curLeg, why, drinkOrder(m.Name, "drink"), m.Name, m.Pct))
	}
	for _, name := range st.shortHP {
		if err := d.chat(ctx, drinkOrder(name, "eat")); err != nil {
			return err
		}
		d.rep.note(fmt.Sprintf("leg %d: %s: sent %q", d.curLeg, why, drinkOrder(name, "eat")))
	}
	return nil
}

// doPull pulls each member group of the action: the leader targets the nearest member and the tank walks up and
// pulls it (.bot action pullback); the fight ends when the pack is dead.
func (d *Driver) doPull(ctx context.Context, a Action) error {
	sp := fightSpec{pick: a.Entries, track: a.Entries, skip: a.Skip, anchors: a.Positions, limit: d.cfg.FightTimeout, sneak: true, radius: a.Radius, passed: map[packet.GUID]bool{}}
	// A group whose members in view all carry a stealth aura cannot be pulled (the companions never attack what they
	// cannot see); with none in view the plan's flag decides. Members in view without the aura are pulled as usual.
	if cs := d.candidates(sp, sp.pick); (len(cs) == 0 && a.Ambush) || (len(cs) > 0 && allStealthed(cs)) {
		return d.sneakPast(ctx, a.Label, sp)
	}
	return d.engage(ctx, a.Label, sp)
}

func allStealthed(us []objects.Unit) bool {
	for _, u := range us {
		if !Stealthed(u) {
			return false
		}
	}
	return len(us) > 0
}

// sneakPast is the pull of a stealthed group: nothing can be pulled, so the party is walked through the group's
// positions (follow on, a pause of AmbushPause at each) and fights whatever shows itself. The group counts as cleared
// when none of its members is left alive in view, or when the party has passed it with no fight (noted in the
// report): the leg goes on, and what is still hidden aggros on the walks after (walkAggro).
func (d *Driver) sneakPast(ctx context.Context, label string, sp fightSpec) error {
	if err := d.restGate(ctx); err != nil {
		return err
	}
	d.legStat(func(l *LegReport) { l.Ambushes++ })
	if err := d.chat(ctx, ".bot action follow"); err != nil {
		return err
	}
	fights := 0
	for _, at := range sp.anchors {
		if err := d.doWalk(ctx, at); err != nil {
			return err
		}
		pause := d.clk.Now().Add(d.cfg.AmbushPause)
		for d.clk.Now().Before(pause) {
			if err := d.sleep(ctx, d.cfg.Poll); err != nil {
				return err
			}
			if d.partyInCombat() {
				fights++
				d.legStat(func(l *LegReport) { l.Pulls++ })
				if err := d.fightAttackers(ctx); err != nil {
					return err
				}
				if err := d.afterFight(ctx); err != nil {
					return err
				}
				break
			}
		}
	}
	left := 0
	for _, u := range d.candidates(sp, sp.pick) {
		if u.Distance <= 80 {
			left++
		}
	}
	switch {
	case fights > 0 && left == 0:
		d.rep.note(fmt.Sprintf("%s: stealthed group %v revealed itself and was killed (%d fights)", label, sp.pick, fights))
	case fights > 0:
		d.rep.note(fmt.Sprintf("%s: stealthed group %v: %d fights, %d members still alive in view; went on", label, sp.pick, fights, left))
	default:
		d.rep.note(fmt.Sprintf("%s: stealthed group %v passed with no fight; counted as cleared", label, sp.pick))
	}
	return nil
}

// doKill kills Boss.First in order, then the boss; the whole encounter is tracked so that its brothers dying with
// the boss, or joining the fight, count.
func (d *Driver) doKill(ctx context.Context, a Action) error {
	b := a.Boss
	enc := append(slices.Clone(b.First), b.Entry)
	for _, e := range enc {
		spec := fightSpec{pick: []uint32{e}, track: enc, skip: a.Skip, anchors: []Point{b.Position}, limit: d.cfg.BossTimeout, required: e == b.Entry, wide: true}
		if err := d.engage(ctx, a.Label, spec); err != nil {
			return err
		}
	}
	return nil
}

type fightSpec struct {
	pick     []uint32 // entries the target is picked from
	track    []uint32 // entries that make up the fight: it is won when all of them nearby are dead
	skip     []uint32
	anchors  []Point // where the pack stands
	limit    time.Duration
	required bool    // not finding the target fails the run (a boss), else the fight is taken as already won
	sneak    bool    // a target that turns out stealthed is walked past (sneakPast) instead of pulled
	wide     bool    // a boss encounter: with none of the entries near the anchors, any within 80 yd counts
	radius   float32 // yards a member may stand from an anchor (PullGroup.Radius); 0: PackRadius
	// passed are the mobs this group's pulls passed over (they drew no fight or evaded, nothing died): not picked again
	// by this group, and not by another one (unlike the refused ones, which the whole run leaves alone).
	passed map[packet.GUID]bool
}

type outcome int

const (
	won outcome = iota
	noEngage
	evaded
	refused  // the bots refused the pull: the mob is unreachable or evading
	retarget // the target died or was lost before the order arrived: pick again
)

// fightResult is how one pull went: the outcome and how many members of the fight died in it (progress, even when the
// rest of the pack stood idle and the pull was given up).
type fightResult struct {
	out   outcome
	kills int
	why   string   // the refusal that ended the pull
	dead  []deadAt // remote: the units that died, where the feed last showed them
}

// maxRetargets bounds the picks of one group that the bots answered with dead-target or no-target.
const maxRetargets = 3

// passedOver is a mob a group's pulls gave up on.
type passedOver struct {
	text    string
	refused bool // the bots refused to pull it (unreachable, evading), not just: it drew no fight
}

func (d *Driver) engage(ctx context.Context, label string, sp fightSpec) error {
	if sp.passed == nil {
		sp.passed = map[packet.GUID]bool{}
	}
	kills, retargets := 0, 0
	var gaveUp []passedOver
	// An attempt that killed something is progress and does not use up a PullAttempt: a pack of stationary mobs that
	// do not link comes one or two at a time, and each partial pull used to count as a failed one (live run 2, 7175 x3).
	for attempt, loops := 1, 0; attempt <= d.cfg.PullAttempts && loops < 4*d.cfg.PullAttempts+12; loops++ {
		if err := d.restGate(ctx); err != nil {
			return err
		}
		t, ok, err := d.pickTarget(ctx, sp)
		if err != nil {
			return err
		}
		if !ok {
			if len(gaveUp) > 0 {
				return d.leftOver(label, sp, kills, gaveUp)
			}
			// A boss (required) is never taken as dead unseen: pickTarget has walked to its position, and only its corpse in view
			// counts. Live run 4 read the note of a boss that the rest gate's fight had killed as "taken as dead while alive"; the
			// boss's note now says what was seen.
			if sp.required {
				if !d.deadInView(sp) {
					return fmt.Errorf("%s: no living %v in view at the boss's position, and no corpse of it: the boss is not where the plan puts it", label, sp.pick)
				}
				d.rep.note(fmt.Sprintf("%s: %v is dead already: its corpse is in view (it died in an earlier fight or during the rest gate)", label, sp.pick))
				return nil
			}
			d.rep.note(fmt.Sprintf("%s: no living %v in view; taken as already dead", label, sp.pick))
			return nil
		}
		if sp.sneak && Stealthed(t) {
			return d.sneakPast(ctx, label, sp)
		}
		d.legStat(func(l *LegReport) {
			l.Pulls++
			if attempt > 1 {
				l.PullRetries++
			}
		})
		res, err := d.tracedFight(ctx, t, sp)
		if err != nil {
			return err
		}
		kills += res.kills
		if res.out == won {
			return nil
		}
		if res.out == retarget && retargets < maxRetargets {
			retargets++
			d.rep.note(fmt.Sprintf("%s: the bots could not take %s (%s); picking again", label, t.Name, res.why))
			continue
		}
		where := fmt.Sprintf("%s at %.0f %.0f", t.Name, t.X, t.Y)
		why := map[outcome]string{noEngage: "drew no fight", evaded: "evaded", refused: "was refused (" + res.why + ")", retarget: "was lost (" + res.why + ")"}[res.out]
		switch {
		case res.out == refused:
			// The bots said the mob cannot be reached or is evading: no group of this run tries it again.
			d.skipped[t.GUID] = res.why
			gaveUp = append(gaveUp, passedOver{text: where + ": " + res.why, refused: true})
		case res.kills > 0:
			why += fmt.Sprintf(", after %d kills", res.kills)
		case res.out == noEngage || res.out == evaded:
			// A pull in which nothing died and nothing was refused: the mob drew no fight or evaded. The next try takes
			// another member when the group has one. This is this group's alone (sp.passed), not the run's: a mob that sits in
			// the area of the next group is still that group's to pull.
			if len(d.candidates(sp, sp.pick)) > 1 {
				sp.passed[t.GUID] = true
				gaveUp = append(gaveUp, passedOver{text: where + ": " + why})
			}
		}
		if res.kills == 0 {
			attempt++
		}
		d.rep.note(fmt.Sprintf("%s: pull of %s %s", label, t.Name, why))
		if err := d.chat(ctx, ".bot action follow"); err != nil {
			return err
		}
		if err := d.sleep(ctx, d.cfg.Poll*3); err != nil {
			return err
		}
	}
	return d.leftOver(label, sp, kills, gaveUp)
}

// leftOver ends a group whose pulls are used up or whose members are all dead or given up on. It counts as cleared
// (noted) when the group made progress or the bots refused every mob left (unreachable, evading): the party goes on and
// what is still standing aggros on the walks after. Else the run fails: nothing came of the pulls.
func (d *Driver) leftOver(label string, sp fightSpec, kills int, gaveUp []passedOver) error {
	allRefused := len(gaveUp) > 0
	var texts []string
	for _, g := range gaveUp {
		allRefused = allRefused && g.refused
		texts = append(texts, g.text)
	}
	if kills > 0 || allRefused {
		rest := strings.Join(texts, "; ")
		if rest == "" {
			rest = "the last pulls drew no fight"
		}
		d.rep.note(fmt.Sprintf("%s: gave up with %d kills (%s); counted as cleared", label, kills, rest))
		return nil
	}
	return fmt.Errorf("%s: %d pulls of %v failed (no fight, or the mobs evaded)", label, d.cfg.PullAttempts, sp.pick)
}

// groupRadius is how far from an anchor a member of the group may stand.
func (d *Driver) groupRadius(sp fightSpec) float32 {
	if sp.radius > 0 {
		return sp.radius
	}
	return d.cfg.PackRadius
}

// nearAnchor says whether the unit stands within the group's radius of one of the anchors.
func (d *Driver) nearAnchor(sp fightSpec, u objects.Unit) bool {
	for _, a := range sp.anchors {
		if dist3(Point{u.X, u.Y, u.Z}, a) <= float64(d.groupRadius(sp)) {
			return true
		}
	}
	return false
}

// deadInView reports a corpse of the fight's target entries that belongs to the group: within the group's radius of an
// anchor (the group was killed before, say by a walk aggro). A boss encounter (wide) takes any corpse within 80 yards of
// the leader: a brother that joined the fight of the boss's. A corpse elsewhere says nothing about this group.
func (d *Driver) deadInView(sp fightSpec) bool {
	for _, u := range d.g.Units() {
		if u.Type == "player" || !u.Dead || !slices.Contains(sp.pick, u.Entry) {
			continue
		}
		if sp.wide || len(sp.anchors) == 0 {
			if u.Distance >= 0 && u.Distance <= 80 {
				return true
			}
		} else if d.nearAnchor(sp, u) {
			return true
		}
	}
	return false
}

// candidates are the living hostile units of entries (not skipped) in view, nearest to the leader first. With anchors
// only the units within the group's radius of an anchor count (plus any that is fighting the party: a pulled mob leaves
// its spawn to come at the tank): a pack whose own members are dead is cleared, and a mob of the same entry in another
// pack, 40 yards off, is no member of it (the first live run pulled a Rockchewer that stood in the next pack and the
// tank answered "unreachable"). Without anchors, or with wide (a boss encounter, whose brothers may stand off the boss's
// spot) and none near, every unit within 80 yards counts. Mobs the run was refused (d.skipped) and mobs this group passed
// over (sp.passed) are left out.
func (d *Driver) candidates(sp fightSpec, entries []uint32) []objects.Unit {
	var all, near, fighting []objects.Unit
	for _, u := range d.g.Units() {
		if u.Type == "player" || u.Dead || !u.Hostile || u.Distance < 0 || !slices.Contains(entries, u.Entry) || slices.Contains(sp.skip, u.Entry) {
			continue
		}
		if _, refused := d.skipped[u.GUID]; refused || sp.passed[u.GUID] {
			continue
		}
		if u.Distance <= 80 {
			all = append(all, u)
		}
		isNear := d.nearAnchor(sp, u)
		if isNear {
			near = append(near, u)
		}
		if !isNear && u.InCombat && u.Distance <= 80 && d.party[u.Target] {
			fighting = append(fighting, u)
		}
	}
	out := all
	if len(near) > 0 || (len(sp.anchors) > 0 && !sp.wide) {
		out = append(near, fighting...)
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].Distance < out[j].Distance })
	return out
}

// pickTarget finds the unit to pull, walking up to PullStandoff yards from it when it is out of PullRange (or from
// the anchor when nothing is in view yet).
func (d *Driver) pickTarget(ctx context.Context, sp fightSpec) (objects.Unit, bool, error) {
	const looks = 3
	for look := 1; look <= looks; look++ {
		if cs := d.candidates(sp, sp.pick); len(cs) > 0 {
			cs = preferVisible(cs)
			if c := cs[0]; c.Distance <= d.cfg.PullRange || look == looks {
				return c, true, nil
			} else if err := d.approach(ctx, Point{c.X, c.Y, c.Z}); err != nil {
				return objects.Unit{}, false, err
			}
			continue
		}
		if len(sp.anchors) == 0 || d.deadInView(sp) {
			break
		}
		if err := d.approach(ctx, sp.anchors[0]); err != nil {
			return objects.Unit{}, false, err
		}
		if err := d.sleep(ctx, d.cfg.Poll); err != nil {
			return objects.Unit{}, false, err
		}
	}
	return objects.Unit{}, false, nil
}

// preferVisible puts the units without a stealth aura first (a companion can be sent at those), keeping the order.
func preferVisible(cs []objects.Unit) []objects.Unit {
	out := make([]objects.Unit, 0, len(cs))
	for _, u := range cs {
		if !Stealthed(u) {
			out = append(out, u)
		}
	}
	if len(out) == 0 {
		return cs
	}
	return out
}

// approach walks to a point PullStandoff yards short of p along the planned path (nothing when already that close).
func (d *Driver) approach(ctx context.Context, p Point) error {
	pts, err := d.g.PathPoints(ctx, p)
	if err != nil {
		return fmt.Errorf("plan the approach to %.0f %.0f %.0f: %w", p.X, p.Y, p.Z, err)
	}
	stand, ok := StandoffPoint(pts, d.cfg.PullStandoff)
	if !ok || dist3(d.g.Position(), stand) <= ArriveRadius {
		return nil
	}
	return d.doWalk(ctx, stand)
}

func (d *Driver) fight(ctx context.Context, t objects.Unit, sp fightSpec) (fightResult, error) {
	if err := d.g.Target(ctx, t.GUID); err != nil {
		return fightResult{}, fmt.Errorf("target %s: %w", t.Name, err)
	}
	d.pendingRef = nil
	if err := d.order(ctx, "pullback"); err != nil {
		return fightResult{}, err
	}
	began := d.clk.Now()
	tracked := map[packet.GUID]bool{t.GUID: true}
	for _, u := range d.candidates(sp, sp.track) {
		if dist3(Point{u.X, u.Y, u.Z}, Point{t.X, t.Y, t.Z}) <= float64(d.groupRadius(sp)) {
			tracked[u.GUID] = true
		}
	}
	engaged := false
	lastCombat, lastAssist := began, began
	killed := map[packet.GUID]bool{} // members seen dead: the pull's progress
	assistRefusals := 0
	d.tg.looseSent, d.tg.focus = nil, 0 // the fight's first attack order comes at AssistEvery, after the pull's own join delay
	for {
		if err := d.sleep(ctx, d.cfg.Poll); err != nil {
			return fightResult{}, err
		}
		now := d.clk.Now()
		// What the bots answered to the orders: a pull refused as unreachable or evading ends this pull (the mob is skipped),
		// a target that died or was never selected is picked again; a refused assist order is sent again at once.
		for _, r := range d.takeRefusals() {
			switch c := classify(r); {
			case pullIntent(r) && d.tg.takeLooseAnswer(now):
				// the answer to a tank order at a loose add, not to this fight's pull
			case pullIntent(r) && c == refUnreachable:
				return fightResult{out: refused, kills: len(killed), why: r.Code + ": " + r.Message}, nil
			case pullIntent(r) && (c == refDead || c == refNoTarget):
				return fightResult{out: retarget, kills: len(killed), why: r.Code + ": " + r.Message}, nil
			case c == refDead || c == refNoTarget || c == refUnreachable:
				// The assist order is sent again at once, twice; a bot that keeps refusing it (the mob evades, it is
				// out of reach) is not asked every poll: the usual cadence goes on.
				if assistRefusals++; assistRefusals <= 2 {
					lastAssist = now.Add(-d.cfg.AssistEvery)
				}
			}
		}
		units := d.g.Units()
		byGUID := make(map[packet.GUID]objects.Unit, len(units))
		for _, u := range units {
			byGUID[u.GUID] = u
			// A member of the encounter that joins is part of it.
			if u.InCombat && !u.Dead && u.Hostile && u.Type != "player" && slices.Contains(sp.track, u.Entry) && !slices.Contains(sp.skip, u.Entry) {
				tracked[u.GUID] = true
			}
		}
		alive, fighting := 0, false
		var fighters []objects.Unit
		for g := range tracked {
			u, ok := byGUID[g]
			if !ok { // out of view: gone
				delete(tracked, g)
				continue
			}
			if u.Dead {
				killed[g] = true
				continue
			}
			if sp.sneak && Stealthed(u) && !u.InCombat && u.Health >= u.MaxHealth {
				continue // a stealthed member that has not joined is not fought (it aggros on the walk after)
			}
			alive++
			if u.InCombat || u.Health < u.MaxHealth {
				fighting = true
				fighters = append(fighters, u)
			}
		}
		if fighting {
			engaged, lastCombat = true, now
		}
		switch {
		case alive == 0:
			return fightResult{out: won, kills: len(killed)}, d.afterFight(ctx)
		case !engaged && now.Sub(began) > d.cfg.EngageTimeout:
			return fightResult{out: noEngage, kills: len(killed)}, nil
		case engaged && now.Sub(lastCombat) > d.cfg.EvadeGrace:
			return fightResult{out: evaded, kills: len(killed)}, nil
		case now.Sub(began) > sp.limit:
			return fightResult{}, fmt.Errorf("the fight with %v lasted more than %.0f s", sp.pick, sp.limit.Seconds())
		}
		if engaged && len(fighters) > 0 {
			if err := d.tankFirstGroup(ctx, fighters); err != nil {
				return fightResult{}, err
			}
			// The leader targets the mob the tank holds best, not the nearest; a dead mob is replaced at once.
			if now.Sub(lastAssist) >= d.cfg.AssistEvery || d.focusGoneIn(fighters) {
				if t, ok := d.pickAssist(fighters); ok {
					lastAssist = now
					if _, err := d.assistOrder(ctx, t, fighters); err != nil {
						return fightResult{}, err
					}
				}
			}
		}
	}
}

// afterFight gives the bots a moment to loot, then deals with loose hostile units that are hitting the party (adds).
func (d *Driver) afterFight(ctx context.Context) error {
	if err := d.sleep(ctx, d.cfg.PostFight); err != nil {
		return err
	}
	deadline := d.clk.Now().Add(d.cfg.AddsWait)
	for d.clk.Now().Before(deadline) {
		var add *objects.Unit
		for _, u := range d.g.Units() {
			if u.Type != "player" && u.Hostile && !u.Dead && u.InCombat && u.Distance >= 0 && u.Distance <= 40 && d.party[u.Target] {
				c := u
				add = &c
				break
			}
		}
		if add == nil {
			return nil
		}
		if err := d.g.Target(ctx, add.GUID); err == nil {
			if err := d.order(ctx, "attack"); err != nil {
				return err
			}
		}
		if err := d.sleep(ctx, d.cfg.Poll*2); err != nil {
			return err
		}
	}
	d.rep.note("loose hostile units were still hitting the party after " + d.cfg.AddsWait.String())
	return nil
}

// StandoffPoint is the point on a path (corners from the start to the target) standoff yards before its end, so that
// a leader can stop where it sees the target without walking up to it. ok is false when the path is no longer than
// standoff: there is nothing to walk.
func StandoffPoint(path []Point, standoff float32) (Point, bool) {
	if len(path) < 2 {
		return Point{}, false
	}
	left := float64(standoff)
	for i := len(path) - 1; i > 0; i-- {
		a, b := path[i], path[i-1]
		seg := dist3(a, b)
		if seg > left {
			t := left / seg
			if seg == 0 {
				t = 0
			}
			return Point{
				X: float32(float64(a.X) + (float64(b.X)-float64(a.X))*t),
				Y: float32(float64(a.Y) + (float64(b.Y)-float64(a.Y))*t),
				Z: float32(float64(a.Z) + (float64(b.Z)-float64(a.Z))*t),
			}, true
		}
		left -= seg
	}
	return Point{}, false
}
