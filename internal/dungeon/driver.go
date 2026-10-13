package dungeon

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"sort"
	"time"

	"github.com/pjerra/turtle-agent/internal/events"
	"github.com/pjerra/turtle-agent/internal/objects"
	"github.com/pjerra/turtle-agent/internal/packet"
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
	Teleport(ctx context.Context, to Point, mapID uint32) error
	// Emit appends an event of kind (events.KindDungeon*) to the agent's event log.
	Emit(kind, text string, data map[string]any)
}

// Self is the leader as the session sees it.
type Self struct {
	GUID      packet.GUID
	Name      string
	Health    uint32
	MaxHealth uint32
}

// Dead reports a leader with no health left (a ghost shows 0 too).
func (s Self) Dead() bool { return s.MaxHealth > 0 && s.Health == 0 }

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
	Teleport  bool // GM-teleport to the start of the first leg and bring the party along (flagged in the report)
	Shortcuts bool // allow the GM revive of a dead party member at a rest gate (flagged in the report)

	WalkAttempts   int           // tries of one walk (each is a walk_path, which replans by itself)
	WalkRetryDelay time.Duration // wait between two tries
	UseSettle      time.Duration // wait after a use for the world to answer
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
	RestTimeout    time.Duration // longest rest gate
	RestPoll       time.Duration
	RestHealthPct  int // rested: every party member's health at least this, mana at least RestManaPct
	RestManaPct    int
	RestMinHealth  int           // after RestTimeout the run goes on only if everyone has at least this health
	GatherRadius   float32       // yards: rested also asks every living member to be this close
	WaitPoll       time.Duration // time between two looks at a wait step
	Poll           time.Duration // time between two looks at a fight
	ArriveTimeout  time.Duration // the party's walk to the leader after a teleport
}

// DefaultConfig is the Driver's limits for a live run.
func DefaultConfig() Config {
	return Config{
		WalkAttempts: 3, WalkRetryDelay: 2 * time.Second, UseSettle: 1500 * time.Millisecond,
		PullAttempts: 3, PullRange: 45, PullStandoff: 38, PackRadius: 25,
		EngageTimeout: 25 * time.Second, EvadeGrace: 12 * time.Second, AssistEvery: 10 * time.Second,
		FightTimeout: 300 * time.Second, BossTimeout: 600 * time.Second, PostFight: 4 * time.Second, AddsWait: 15 * time.Second,
		RestTimeout: 150 * time.Second, RestPoll: 3 * time.Second, RestHealthPct: 90, RestManaPct: 75, RestMinHealth: 50, GatherRadius: 40,
		WaitPoll: 2 * time.Second, Poll: time.Second, ArriveTimeout: 60 * time.Second,
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
	fillInt(&c.RestMinHealth, d.RestMinHealth)
	fillF(&c.GatherRadius, d.GatherRadius)
	fill(&c.WaitPoll, d.WaitPoll)
	fill(&c.Poll, d.Poll)
	fill(&c.ArriveTimeout, d.ArriveTimeout)
	return c
}

// errWipe ends the run: the leader is dead or the whole party is. The MVP does not retry.
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
}

// NewDriver prepares a run of plan. A zero clock is the wall clock.
func NewDriver(plan *Plan, g Game, cfg Config, clk Clock) *Driver {
	if clk.Now == nil || clk.Sleep == nil {
		clk = RealClock()
	}
	d := &Driver{plan: plan, g: g, cfg: cfg.withDefaults(), clk: clk, curLeg: -1, prevDea: map[packet.GUID]bool{}, party: map[packet.GUID]bool{}}
	d.rep.init(plan, clk.Now)
	return d
}

// Report is a copy of the run report, usable while the run goes on.
func (d *Driver) Report() Report { return d.rep.snapshot() }

// Run does the plan from leg fromLeg. It returns nil when the plan is done; the error says why it ended otherwise
// (also in the report): "wipe", a failed step, or the context's error for a stop.
func (d *Driver) Run(ctx context.Context, fromLeg int) error {
	if fromLeg < 0 || fromLeg >= len(d.plan.Legs) {
		return fmt.Errorf("from_leg %d: the plan has legs 0-%d", fromLeg, len(d.plan.Legs)-1)
	}
	d.rep.start(fromLeg)
	err := d.run(ctx, fromLeg)
	d.finish(err)
	return err
}

func (d *Driver) finish(err error) {
	leg := d.curLeg
	switch {
	case err == nil:
		d.endLeg("done")
		d.rep.end("done", "")
		d.g.Emit(events.KindDungeonDone, "dungeon "+d.plan.Name+" done", d.rep.summary())
	case ctxDone(err):
		d.endLeg("stopped")
		d.rep.end("stopped", "stopped")
		d.g.Emit(events.KindDungeonFailed, "dungeon "+d.plan.Name+" stopped", d.failData(leg, "stopped"))
	default:
		d.endLeg("failed")
		d.rep.end("failed", err.Error())
		d.g.Emit(events.KindDungeonFailed, "dungeon "+d.plan.Name+" failed: "+err.Error(), d.failData(leg, err.Error()))
	}
}

func (d *Driver) failData(leg int, why string) map[string]any {
	m := d.rep.summary()
	m["error"], m["leg"] = why, leg
	return m
}

func ctxDone(err error) bool {
	return errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded)
}

func (d *Driver) run(ctx context.Context, fromLeg int) error {
	if err := d.begin(ctx, fromLeg); err != nil {
		return err
	}
	d.st = NewStepperAt(d.plan, stepWorld{d}, d.clk.Now, fromLeg)
	waiting := [2]int{-1, -1} // the leg and step a wait event was sent for
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		a := d.st.Next()
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
				d.g.Emit(events.KindDungeonStep, a.Label, map[string]any{"leg": a.Leg, "step": a.Step, "action": string(a.Kind)})
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
			err = d.doWalk(ctx, a.To)
		case ActUse:
			err = d.doUse(ctx, a)
		case ActPull:
			err = d.doPull(ctx, a)
		case ActKill:
			err = d.doKill(ctx, a)
		default:
			err = fmt.Errorf("unknown action %q", a.Kind)
		}
		data := map[string]any{"leg": a.Leg, "step": a.Step, "action": string(a.Kind), "ok": err == nil, "seconds": d.clk.Now().Sub(began).Seconds()}
		if err != nil {
			data["error"] = err.Error()
		}
		d.g.Emit(events.KindDungeonStep, a.Label, data)
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
		d.rep.note(err.Error())
	}
}

// begin checks that the run can start: the leader is in a group and on the map (or, with Teleport, is put there).
func (d *Driver) begin(ctx context.Context, fromLeg int) error {
	if len(d.g.Party()) == 0 {
		return errors.New("the leader has no party: invite the companions first (invite, hire)")
	}
	d.observeInit()
	d.rep.note("leader made visible with .gm visible on")
	if err := d.g.GM(ctx, ".gm visible on"); err != nil {
		d.rep.note("could not send .gm visible on: " + err.Error())
	}
	start, what := d.startPoint(fromLeg)
	switch {
	case d.cfg.Teleport && (d.g.Map() != d.plan.Map || dist3(d.g.Position(), start) > 15):
		d.shortcut("teleport_to_"+what, fromLeg, fmt.Sprintf("GM teleport to %.1f %.1f %.1f on map %d", start.X, start.Y, start.Z, d.plan.Map))
		if err := d.g.Teleport(ctx, start, d.plan.Map); err != nil {
			return fmt.Errorf("teleport to the %s: %w", what, err)
		}
		if err := d.sleep(ctx, 3*time.Second); err != nil {
			return err
		}
		if err := d.gather(ctx); err != nil {
			return err
		}
	case d.g.Map() != d.plan.Map:
		return fmt.Errorf("the leader is on map %d and the plan needs map %d: enter the dungeon first, or run with teleport", d.g.Map(), d.plan.Map)
	}
	return nil
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
	return d.g.Chat(ctx, "party", ".bot action follow")
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
}

// startLeg opens a leg's report and event, and waits for the party to be rested.
func (d *Driver) startLeg(ctx context.Context, i int) error {
	d.curLeg = i
	name := d.plan.Legs[i].Name
	d.rep.update(func(r *Report) {
		r.Legs = append(r.Legs, LegReport{Leg: i, Name: name, Started: d.clk.Now(), Result: "running"})
	})
	d.g.Emit(events.KindDungeonLeg, fmt.Sprintf("leg %d %s: start", i, name), map[string]any{"leg": i, "name": name, "phase": "start"})
	return d.restGate(ctx)
}

// endLeg closes the current leg's report with result and sends its event; it does nothing when no leg is open.
func (d *Driver) endLeg(result string) {
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
		"pulls": l.Pulls, "pull_retries": l.PullRetries, "rest_seconds": l.RestSeconds})
}

func (d *Driver) legStat(f func(*LegReport)) {
	d.rep.update(func(r *Report) {
		if n := len(r.Legs); n > 0 {
			f(&r.Legs[n-1])
		}
	})
}

// sleep waits d, then looks at the party: deaths are counted, and a wipe ends the wait with errWipe.
func (d *Driver) sleep(ctx context.Context, dur time.Duration) error {
	if err := d.clk.Sleep(ctx, dur); err != nil {
		return err
	}
	return d.observe()
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

// observe counts each death once (leader or member) and returns errWipe when the leader is dead or every member is.
func (d *Driver) observe() error {
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
	if self.Dead() {
		return fmt.Errorf("%w: the leader is dead", errWipe)
	}
	if allDown && anyDead {
		return fmt.Errorf("%w: the whole party is dead", errWipe)
	}
	return nil
}

// stepWorld is the Stepper's World over a Game.
type stepWorld struct{ d *Driver }

func (w stepWorld) Position() Point                   { return w.d.g.Position() }
func (w stepWorld) GameObjects() []objects.GameObject { return w.d.g.GameObjects() }
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
// gave up on (a root that outlasted its wait, a server move too many).
func (d *Driver) doWalk(ctx context.Context, p Point) error {
	var last string
	for try := 1; try <= d.cfg.WalkAttempts; try++ {
		r, err := d.g.WalkPath(ctx, p)
		if err := ctx.Err(); err != nil {
			return err
		}
		if oerr := d.observe(); oerr != nil {
			return oerr
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
	}
	return fmt.Errorf("walk to %.1f %.1f %.1f failed after %d tries (%s)", p.X, p.Y, p.Z, d.cfg.WalkAttempts, last)
}

func (d *Driver) doUse(ctx context.Context, a Action) error {
	var err error
	for try := 0; try < 2; try++ {
		if err = d.g.UseObject(ctx, a.GUID); err == nil {
			break
		}
		if cerr := ctx.Err(); cerr != nil {
			return cerr
		}
		if serr := d.sleep(ctx, time.Second); serr != nil {
			return serr
		}
	}
	if err != nil {
		return fmt.Errorf("use game object %s: %w", a.GUID, err)
	}
	return d.sleep(ctx, d.cfg.UseSettle)
}

// restGate waits until every living online member is rested (health and mana at the thresholds, near the leader),
// ordering the bots to eat and drink. A dead member is revived by GM when shortcuts are on (flagged). After
// RestTimeout the run goes on if everyone has at least RestMinHealth percent, else it fails.
func (d *Driver) restGate(ctx context.Context) error {
	began := d.clk.Now()
	defer func() {
		if s := d.clk.Now().Sub(began).Seconds(); s > 0 {
			d.legStat(func(l *LegReport) { l.RestSeconds += s })
		}
	}()
	ordered := false
	revived := map[packet.GUID]int{}
	for {
		st := d.restState()
		for _, m := range st.dead {
			if !d.cfg.Shortcuts {
				return fmt.Errorf("%s is dead and GM shortcuts are off", m.Name)
			}
			if revived[m.GUID] >= 2 {
				return fmt.Errorf("%s is still dead after two GM revives", m.Name)
			}
			revived[m.GUID]++
			d.shortcut("revive", max(d.curLeg, 0), "GM .revive "+m.Name)
			if err := d.g.GM(ctx, ".revive "+m.Name); err != nil {
				return fmt.Errorf("revive %s: %w", m.Name, err)
			}
		}
		if len(st.dead) == 0 && st.ok {
			return nil
		}
		if len(st.dead) == 0 && d.clk.Now().Sub(began) > d.cfg.RestTimeout {
			if st.minHealth >= d.cfg.RestMinHealth {
				d.rep.note(fmt.Sprintf("leg %d: rest gate gave up after %.0f s (%s); went on with at least %d%% health", d.curLeg, d.cfg.RestTimeout.Seconds(), st.why, st.minHealth))
				return nil
			}
			return fmt.Errorf("the party is not rested after %.0f s: %s", d.cfg.RestTimeout.Seconds(), st.why)
		}
		if !ordered && len(st.dead) == 0 {
			ordered = true
			if err := d.g.Chat(ctx, "party", ".bot action rest"); err != nil {
				return err
			}
		}
		if err := d.sleep(ctx, d.cfg.RestPoll); err != nil {
			return err
		}
	}
}

type restReport struct {
	ok        bool
	why       string // what is missing, for the report
	minHealth int    // lowest health percent among the living online members and the leader
	dead      []Member
}

func (d *Driver) restState() restReport {
	r := restReport{ok: true, minHealth: 100}
	var low []string
	pct := func(v, max uint32) int {
		if max == 0 {
			return 100
		}
		return int(v * 100 / max)
	}
	if s := d.g.Self(); s.MaxHealth > 0 {
		h := pct(s.Health, s.MaxHealth)
		r.minHealth = min(r.minHealth, h)
		if h < d.cfg.RestHealthPct {
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
		if h < d.cfg.RestHealthPct {
			r.ok = false
			low = append(low, fmt.Sprintf("%s health %d%%", m.Name, h))
		}
		if m.PowerType == 0 && m.MaxPower > 0 {
			if p := pct(m.Power, m.MaxPower); p < d.cfg.RestManaPct {
				r.ok = false
				low = append(low, fmt.Sprintf("%s mana %d%%", m.Name, p))
			}
		}
	}
	if far := d.notNear(); len(far) > 0 {
		r.ok = false
		low = append(low, fmt.Sprintf("not near the leader: %v", far))
	}
	r.why = fmt.Sprint(low)
	return r
}

// doPull pulls each member group of the action: the leader targets the nearest member and the tank walks up and
// pulls it (.bot action pullback); the fight ends when the pack is dead.
func (d *Driver) doPull(ctx context.Context, a Action) error {
	return d.engage(ctx, a.Label, fightSpec{pick: a.Entries, track: a.Entries, skip: a.Skip, anchors: a.Positions, limit: d.cfg.FightTimeout})
}

// doKill kills Boss.First in order, then the boss; the whole encounter is tracked so that its brothers dying with
// the boss, or joining the fight, count.
func (d *Driver) doKill(ctx context.Context, a Action) error {
	b := a.Boss
	enc := append(slices.Clone(b.First), b.Entry)
	for _, e := range enc {
		spec := fightSpec{pick: []uint32{e}, track: enc, skip: a.Skip, anchors: []Point{b.Position}, limit: d.cfg.BossTimeout, required: e == b.Entry}
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
	required bool // not finding the target fails the run (a boss), else the fight is taken as already won
}

type outcome int

const (
	won outcome = iota
	noEngage
	evaded
)

func (d *Driver) engage(ctx context.Context, label string, sp fightSpec) error {
	for attempt := 1; attempt <= d.cfg.PullAttempts; attempt++ {
		if err := d.restGate(ctx); err != nil {
			return err
		}
		t, ok, err := d.pickTarget(ctx, sp)
		if err != nil {
			return err
		}
		if !ok {
			if sp.required && !d.deadInView(sp) {
				return fmt.Errorf("%s: no living %v in view", label, sp.pick)
			}
			d.rep.note(fmt.Sprintf("%s: no living %v in view; taken as already dead", label, sp.pick))
			return nil
		}
		d.legStat(func(l *LegReport) {
			l.Pulls++
			if attempt > 1 {
				l.PullRetries++
			}
		})
		res, err := d.fight(ctx, t, sp)
		if err != nil {
			return err
		}
		if res == won {
			return nil
		}
		why := map[outcome]string{noEngage: "drew no fight", evaded: "evaded"}[res]
		d.rep.note(fmt.Sprintf("%s: pull %d of %s %s", label, attempt, t.Name, why))
		if err := d.g.Chat(ctx, "party", ".bot action follow"); err != nil {
			return err
		}
		if err := d.sleep(ctx, d.cfg.Poll*3); err != nil {
			return err
		}
	}
	return fmt.Errorf("%s: %d pulls of %v failed (no fight, or the mobs evaded)", label, d.cfg.PullAttempts, sp.pick)
}

// deadInView reports a corpse of the fight's target entries within 80 yards or PackRadius of an anchor: the unit
// was killed in the fight before (a boss that joined the fight of its brother).
func (d *Driver) deadInView(sp fightSpec) bool {
	for _, u := range d.g.Units() {
		if u.Type == "player" || !u.Dead || !slices.Contains(sp.pick, u.Entry) {
			continue
		}
		if u.Distance >= 0 && u.Distance <= 80 {
			return true
		}
	}
	return false
}

// candidates are the living hostile units of entries (not skipped) in view, nearest to the leader first; of those
// within PackRadius of an anchor when any are, else all within 80 yards (they wander and patrol).
func (d *Driver) candidates(entries, skip []uint32, anchors []Point) []objects.Unit {
	var all, near []objects.Unit
	for _, u := range d.g.Units() {
		if u.Type == "player" || u.Dead || !u.Hostile || u.Distance < 0 || !slices.Contains(entries, u.Entry) || slices.Contains(skip, u.Entry) {
			continue
		}
		if u.Distance <= 80 {
			all = append(all, u)
		}
		for _, a := range anchors {
			if dist3(Point{u.X, u.Y, u.Z}, a) <= float64(d.cfg.PackRadius) {
				near = append(near, u)
				break
			}
		}
	}
	out := all
	if len(near) > 0 {
		out = near
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].Distance < out[j].Distance })
	return out
}

// pickTarget finds the unit to pull, walking up to PullStandoff yards from it when it is out of PullRange (or from
// the anchor when nothing is in view yet).
func (d *Driver) pickTarget(ctx context.Context, sp fightSpec) (objects.Unit, bool, error) {
	const looks = 3
	for look := 1; look <= looks; look++ {
		if cs := d.candidates(sp.pick, sp.skip, sp.anchors); len(cs) > 0 {
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

func (d *Driver) fight(ctx context.Context, t objects.Unit, sp fightSpec) (outcome, error) {
	if err := d.g.Target(ctx, t.GUID); err != nil {
		return 0, fmt.Errorf("target %s: %w", t.Name, err)
	}
	if err := d.g.Chat(ctx, "party", ".bot action pullback"); err != nil {
		return 0, err
	}
	began := d.clk.Now()
	tracked := map[packet.GUID]bool{t.GUID: true}
	for _, u := range d.candidates(sp.track, sp.skip, sp.anchors) {
		if dist3(Point{u.X, u.Y, u.Z}, Point{t.X, t.Y, t.Z}) <= float64(d.cfg.PackRadius) {
			tracked[u.GUID] = true
		}
	}
	engaged := false
	lastCombat, lastAssist := began, began
	for {
		if err := d.sleep(ctx, d.cfg.Poll); err != nil {
			return 0, err
		}
		now := d.clk.Now()
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
		var nearest *objects.Unit
		for g := range tracked {
			u, ok := byGUID[g]
			if !ok { // out of view: gone
				delete(tracked, g)
				continue
			}
			if u.Dead {
				continue
			}
			alive++
			if u.InCombat || u.Health < u.MaxHealth {
				fighting = true
				if nearest == nil || u.Distance < nearest.Distance {
					c := u
					nearest = &c
				}
			}
		}
		if fighting {
			engaged, lastCombat = true, now
		}
		switch {
		case alive == 0:
			return won, d.afterFight(ctx)
		case !engaged && now.Sub(began) > d.cfg.EngageTimeout:
			return noEngage, nil
		case engaged && now.Sub(lastCombat) > d.cfg.EvadeGrace:
			return evaded, nil
		case now.Sub(began) > sp.limit:
			return 0, fmt.Errorf("the fight with %v lasted more than %.0f s", sp.pick, sp.limit.Seconds())
		}
		if engaged && nearest != nil && now.Sub(lastAssist) >= d.cfg.AssistEvery {
			lastAssist = now
			if err := d.g.Target(ctx, nearest.GUID); err == nil {
				if err := d.g.Chat(ctx, "party", ".bot action attack"); err != nil {
					return 0, err
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
			if err := d.g.Chat(ctx, "party", ".bot action attack"); err != nil {
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
