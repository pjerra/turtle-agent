package brain

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/pjerra/turtle-agent/internal/events"
	"github.com/pjerra/turtle-agent/internal/objects"
	"github.com/pjerra/turtle-agent/internal/packet"
	"github.com/pjerra/turtle-agent/internal/spells"
	"github.com/pjerra/turtle-agent/internal/spellstate"
)

// Member is a party member as the brain needs to know them.
type Member struct {
	Name   string
	GUID   packet.GUID
	Online bool
	Dead   bool
}

// Game is what the brain reads and does. The agent implements it over a session (cmd/turtle-agent/brain_methods.go);
// the tests use a scripted fake. Reads may be called from the brain's goroutine while the session loop runs; the
// calls that act go through the session like any RPC and never block its loop.
type Game interface {
	// Self is the player's own unit row (position, facing, health, power, auras, combat flag); false until known.
	Self() (objects.Unit, bool)
	// Units lists the units in view, nearest first, the player excluded; Distance is from the player.
	Units() []objects.Unit
	Party() []Member
	// Selected is the unit the player has targeted.
	Selected() packet.GUID
	Spells() *spellstate.State
	Items() []objects.Item

	Cast(ctx context.Context, spell uint32, target packet.GUID) error
	Face(ctx context.Context, g packet.GUID) error
	Target(ctx context.Context, g packet.GUID) error
	Attack(ctx context.Context) error
	StopAttack(ctx context.Context) error
	UseItem(ctx context.Context, entry uint32) error

	Emit(e events.Event)
}

// Params are the options of a run of the brain.
type Params struct {
	// Strategies are strategy or preset names (see Strategies); the base strategy is always added.
	Strategies []string `json:"strategies"`
	// Role is "dps", "tank" or "healer" (see roles.go): what the active strategies play. Empty takes the role of the strategies, "dps"
	// when none names one. A role must match the strategies: "tank" needs role_tank (preset tank_warrior), "healer" needs role_healer (preset holy_priest).
	Role string `json:"role,omitempty"`
	// Tank names the party member whose target the brain fights; empty finds the tank by class and target.
	Tank string `json:"tank,omitempty"`
	// OpeningDelay is how long the tank's mob must have been fought before a harmful action starts; 0 is 2 s.
	OpeningDelay time.Duration `json:"-"`
	// RestGate leaves the out-of-combat upkeep to Controller.Rest (the dungeon driver); false allows it always.
	RestGate bool `json:"-"`
	// NoMove keeps the brain from walking the player (positioning, stepping out of ground effects, running to the tank). A
	// dungeon run sets it until the driver holds the move lock (Controller.HoldMove) around its own walks. Blink, a spell, is not
	// a walk and is not affected.
	NoMove bool `json:"no_move,omitempty"`
}

// Config holds the engine's timings. A zero field takes the default of DefaultConfig.
type Config struct {
	Every     time.Duration // between two ticks
	DoTimeout time.Duration // longest an action's game calls may take
	Settle    time.Duration // after a cast was sent, the time the server needs to start it (no second cast meanwhile)
	ErrorHold time.Duration // a spell whose cast call failed is held this long
	IdleEvery time.Duration // the same idle reason is reported at most this often
	DecEvery  time.Duration // the same refused or vetoed decision is reported at most this often
	Recent    int           // decisions status keeps
	Now       func() time.Time
	// NewTicker makes the tick channel; the tests give a channel they feed by hand.
	NewTicker func(d time.Duration) (<-chan time.Time, func())
}

// DefaultConfig is the live engine: 200 ms ticks.
func DefaultConfig() Config {
	return Config{Every: 200 * time.Millisecond, DoTimeout: 3 * time.Second, Settle: 300 * time.Millisecond, ErrorHold: time.Second,
		IdleEvery: 10 * time.Second, DecEvery: 3 * time.Second, Recent: 20}
}

func (c Config) withDefaults() Config {
	d := DefaultConfig()
	fill := func(dst *time.Duration, def time.Duration) {
		if *dst <= 0 {
			*dst = def
		}
	}
	fill(&c.Every, d.Every)
	fill(&c.DoTimeout, d.DoTimeout)
	fill(&c.Settle, d.Settle)
	fill(&c.ErrorHold, d.ErrorHold)
	fill(&c.IdleEvery, d.IdleEvery)
	fill(&c.DecEvery, d.DecEvery)
	if c.Recent <= 0 {
		c.Recent = d.Recent
	}
	if c.Now == nil {
		c.Now = time.Now
	}
	if c.NewTicker == nil {
		c.NewTicker = func(d time.Duration) (<-chan time.Time, func()) {
			t := time.NewTicker(d)
			return t.C, t.Stop
		}
	}
	return c
}

type hold struct {
	until  time.Time
	reason string
}

// memory is what the brain remembers between ticks; Status reads it from other goroutines.
type memory struct {
	mu          sync.Mutex
	holds       map[uint32]hold
	aholds      map[string]hold      // actions that are not casts, held back after a failed call or while the server answers
	eholds      map[uint32]time.Time // items used a moment ago
	failSeen    uint64
	settleUntil time.Time
	faceUntil   time.Time
	walkErrAt   map[string]time.Time // walk failures of missing navmesh data, and when each was last said
	unknownAt   map[string]time.Time // actions whose spell the spellbook lacks, and when that was last said (unknownEvery)
	fightStart  time.Time            // the fight began (the first mob to fight after a quiet spell)
	lastMob     time.Time            // a mob to fight was last seen
	ticks       uint64
	acted       uint64
	recent      []Decision
	lastKey     string
	lastKeyAt   time.Time
	idleKey     string
	idleAt      time.Time
	idle        string
	lastErr     string
	ext         memExt  // positioning, line of sight, interrupts, items (world.go)
	mel         melMem  // auto attack and stance changes (melee.go)
	heal        healMem // health history, shields, dispels and the last friendly cast (heal.go)
	sham        shamMem // the shaman's totems and weapon imbue (shaman.go)
	dru         druMem  // the druid's refused Mark of the Wild casts (druid.go)
	rez         rezMem  // resurrections sent and the moment one could be cast last (rez.go)

	// lastCastTarget is the target of the last cast sent (Ctx.cast); a refusal says which spell, not at whom.
	lastCastTarget packet.GUID
}

func (m *memory) hold(spell uint32, until time.Time, reason string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.holds[spell] = hold{until: until, reason: reason}
}

func (m *memory) held(spell uint32, now time.Time) (string, bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	h, ok := m.holds[spell]
	if !ok {
		return "", false
	}
	if !now.Before(h.until) {
		delete(m.holds, spell)
		return "", false
	}
	return h.reason, true
}

func (m *memory) holdAction(name string, until time.Time, reason string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.aholds[name] = hold{until: until, reason: reason}
}

func (m *memory) actionHeld(name string, now time.Time) (string, bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	h, ok := m.aholds[name]
	if ok && !now.Before(h.until) {
		delete(m.aholds, name)
		return "", false
	}
	return h.reason, ok
}

func (m *memory) holdEntry(entry uint32, until time.Time) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.eholds[entry] = until
}

func (m *memory) entryHeld(entry uint32, now time.Time) bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	until, ok := m.eholds[entry]
	if ok && !now.Before(until) {
		delete(m.eholds, entry)
		return false
	}
	return ok
}

func (m *memory) settle(until time.Time) {
	m.mu.Lock()
	m.settleUntil = until
	m.mu.Unlock()
}

func (m *memory) settling(now time.Time) bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	return now.Before(m.settleUntil)
}

// Brain is one run of the engine: the active strategies, what they remember, and the Tick that decides. It has no
// goroutine of its own; a Controller ticks it.
type Brain struct {
	g          Game
	cfg        Config
	p          Params
	strategies []*Strategy
	names      []string // the strategies as they were asked for, presets expanded
	actions    map[string]*Action
	mem        *memory
	vals       valueCache
	tick       uint64
	restOK     func() bool
	lock       *MoveLock // who moves the player; the controller gives the one the dungeon driver shares
}

// New builds a brain on the game for p. It refuses an unknown strategy or role, and strategies whose rules or
// chains name an action no active strategy defines.
func New(g Game, cfg Config, p Params) (*Brain, error) {
	strats, names, err := Resolve(p.Strategies)
	if err != nil {
		return nil, err
	}
	return assemble(g, cfg, p, strats, names)
}

// assemble builds a brain from strategies already in hand (the tests give their own).
func assemble(g Game, cfg Config, p Params, strats []*Strategy, names []string) (*Brain, error) {
	role, err := resolveRole(p.Role, strats, names)
	if err != nil {
		return nil, err
	}
	p.Role = role
	if p.OpeningDelay <= 0 {
		p.OpeningDelay = 2 * time.Second
	}
	b := &Brain{g: g, cfg: cfg.withDefaults(), p: p, strategies: strats, names: names, actions: map[string]*Action{},
		mem: &memory{holds: map[uint32]hold{}, aholds: map[string]hold{}, eholds: map[uint32]time.Time{}}, vals: valueCache{}, restOK: func() bool { return true }, lock: NewMoveLock()}
	for _, s := range strats {
		for _, a := range s.Actions {
			b.actions[a.Name] = a
		}
	}
	if err := b.validate(); err != nil {
		return nil, err
	}
	return b, nil
}

// validate checks every name the strategies use.
func (b *Brain) validate() error {
	var bad []string
	need := func(owner, name string) {
		if b.actions[name] == nil {
			bad = append(bad, fmt.Sprintf("%s -> %s", owner, name))
		}
	}
	for _, s := range b.strategies {
		for _, r := range s.Rules {
			for _, n := range r.Do {
				need(s.Name+"/"+r.Trigger.Name, n.Action)
			}
		}
		for _, n := range s.Default {
			need(s.Name+"/default", n.Action)
		}
	}
	for _, a := range b.actions {
		for _, n := range a.Pre {
			need("action "+a.Name+" pre", n)
		}
		for _, n := range a.Alt {
			need("action "+a.Name+" alt", n)
		}
	}
	if len(bad) > 0 {
		slices.Sort(bad)
		return fmt.Errorf("strategies %v use actions no active strategy defines: %s", b.names, strings.Join(bad, ", "))
	}
	return nil
}

// Strategies are the active strategy names, presets expanded.
func (b *Brain) Strategies() []string { return slices.Clone(b.names) }

// newCtx takes the snapshot of the game a tick decides on.
func (b *Brain) newCtx(now time.Time) *Ctx {
	b.tick++
	c := &Ctx{G: b.g, Now: now, Tick: b.tick, Spells: b.g.Spells(), P: b.p, vals: b.vals, mem: b.mem, RestOK: b.restOK(), lock: b.lock, b: b,
		byID: map[packet.GUID]*objects.Unit{}}
	c.Self, c.SelfOK = b.g.Self()
	c.Units = b.g.Units()
	c.Party = b.g.Party()
	for i := range c.Units {
		c.byID[c.Units[i].GUID] = &c.Units[i]
	}
	if c.SelfOK {
		c.byID[c.Self.GUID] = &c.Self
	}
	return c
}

// Tick decides and does one thing: it reads the game, ranks what the active rules ask for, and runs the first
// candidate that can run. It reports what it did (nil when nothing ran).
func (b *Brain) Tick(ctx context.Context) (done *Decision) {
	now := b.cfg.Now()
	defer func() {
		if r := recover(); r != nil {
			b.mem.mu.Lock()
			b.mem.lastErr = fmt.Sprint("panic: ", r)
			b.mem.mu.Unlock()
			b.idle(now, "panic", map[string]any{"panic": fmt.Sprint(r)})
			done = nil
		}
	}()
	c := b.newCtx(now)
	c.run = ctx
	b.mem.mu.Lock()
	b.mem.ticks++
	b.mem.mu.Unlock()
	if !c.SelfOK || c.Spells == nil || !c.Spells.Loaded() {
		b.idle(now, "not_ready", nil)
		return nil
	}
	if c.Self.Dead || c.Self.Ghost || (c.Self.MaxHealth > 0 && c.Self.Health == 0) { // a ghost has 1 health
		b.idle(now, "dead", nil)
		return nil
	}
	b.observeFailures(c)
	b.observeSwing(c)
	for _, s := range b.strategies {
		if s.Before != nil {
			s.Before(c)
		}
	}
	cands := b.candidates(c)
	for _, cd := range cands {
		if cd.vetoBy != "" {
			c.trace = append(c.trace, Decision{Trigger: cd.trigger, Action: cd.a.Name, Rel: cd.raw, Result: ResultVetoed, Why: cd.vetoBy, at: now})
			continue
		}
		if ok, d := b.try(ctx, c, cd.a, cd.trigger, cd.rel, "", map[string]bool{}, 0); ok {
			b.record(d)
			b.emitDecision(d)
			b.mem.mu.Lock()
			b.mem.acted++
			b.mem.idle = ""
			b.mem.mu.Unlock()
			return &d
		}
	}
	b.report(c, cands)
	return nil
}

// report says why a tick did nothing: the traced decisions that were refused (deduplicated, so a wait does not
// fill the log) and one idle reason.
func (b *Brain) report(c *Ctx, cands []candidate) {
	reason := "no_trigger"
	var top *Decision
	if len(c.trace) > 0 {
		sort2 := slices.Clone(c.trace)
		slices.SortStableFunc(sort2, func(x, y Decision) int {
			switch {
			case x.Rel > y.Rel:
				return -1
			case x.Rel < y.Rel:
				return 1
			}
			return 0
		})
		top = &sort2[0]
		if top.Result == ResultVetoed {
			reason = "vetoed:" + top.Why
		} else {
			reason = "not_possible:" + top.Action + ":" + top.Why
		}
	} else if len(cands) > 0 {
		reason = "not_useful:" + cands[0].a.Name
	}
	if top != nil {
		b.emitDecision(*top)
	}
	data := map[string]any{}
	if top != nil {
		data["trigger"], data["action"], data["relevance"] = top.Trigger, top.Action, top.Rel
	}
	b.idle(c.Now, reason, data)
}

func (b *Brain) record(d Decision) {
	b.mem.mu.Lock()
	defer b.mem.mu.Unlock()
	b.mem.recent = append(b.mem.recent, d)
	if n := len(b.mem.recent) - b.cfg.Recent; n > 0 {
		b.mem.recent = b.mem.recent[n:]
	}
}

// emitDecision logs a decision as brain_decision: always for an action that ran, and for a refused or vetoed one
// when it differs from the last or the last was DecEvery ago.
func (b *Brain) emitDecision(d Decision) {
	if d.Result != ResultOK && d.Result != ResultError {
		key := string(d.Result) + "|" + d.Trigger + "|" + d.Action + "|" + d.Why
		b.mem.mu.Lock()
		same := key == b.mem.lastKey && d.at.Sub(b.mem.lastKeyAt) < b.cfg.DecEvery
		if !same {
			b.mem.lastKey, b.mem.lastKeyAt = key, d.at
		}
		b.mem.mu.Unlock()
		if same {
			return
		}
		b.record(d)
	}
	data := map[string]any{"trigger": d.Trigger, "action": d.Action, "relevance": d.Rel, "result": string(d.Result)}
	if d.Why != "" {
		data["why"] = d.Why
	}
	if d.Via != "" {
		data["via"] = d.Via
	}
	if d.Error != "" {
		data["error"] = d.Error
	}
	b.g.Emit(events.Event{Kind: events.KindBrainDecision, Text: d.Action, SpellID: d.Spell, TargetGUID: d.Target, Data: data})
}

// idle logs brain_idle with the reason, when the reason is new or IdleEvery has passed.
func (b *Brain) idle(now time.Time, reason string, data map[string]any) {
	b.mem.mu.Lock()
	b.mem.idle = reason
	same := reason == b.mem.idleKey && now.Sub(b.mem.idleAt) < b.cfg.IdleEvery
	if !same {
		b.mem.idleKey, b.mem.idleAt = reason, now
	}
	b.mem.mu.Unlock()
	if same {
		return
	}
	if data == nil {
		data = map[string]any{}
	}
	data["reason"] = reason
	b.g.Emit(events.Event{Kind: events.KindBrainIdle, Text: reason, Data: data})
}

// holdFor is how long a refused cast keeps its spell from being tried again, by the server's reason.
var holdFor = map[string]time.Duration{
	"out_of_range":         1500 * time.Millisecond,
	"too_close":            time.Second,
	"no_power":             2 * time.Second,
	"line_of_sight":        3 * time.Second,
	"equipped_item":        30 * time.Second, // Shoot without a wand equipped
	"equipped_item_class":  30 * time.Second,
	"no_pet":               time.Minute,      // Call Pet of a hunter who has no pet to call (pet.go)
	"no_ammo":              30 * time.Second, // a hunter's shots without ammo in the quiver (hunter.go)
	"need_ammo":            30 * time.Second,
	"not_ready":            500 * time.Millisecond,
	"not_standing":         time.Second, // sitting: the engine stands the player up first (stand.go)
	"not_infront":          400 * time.Millisecond,
	"unit_not_infront":     400 * time.Millisecond,
	"interrupted":          300 * time.Millisecond,
	"moving":               500 * time.Millisecond,
	"spell_in_progress":    400 * time.Millisecond,
	"targets_dead":         2 * time.Second,
	"bad_targets":          2 * time.Second,
	"bad_implicit_targets": 2 * time.Second,
	"silenced":             2 * time.Second,
	"immune":               5 * time.Second,
	"target_enemy":         2 * time.Second,
	"target_friendly":      2 * time.Second,
}

const defaultHold = time.Second

// selfBadTargetsHold is how long a cast at the player itself stays off after the server named its target wrong.
const selfBadTargetsHold = 10 * time.Second

// observeFailures reads the newest refused cast off the spell state and holds that spell back for a moment, so the
// brain does not repeat a cast the server just refused (out of range, no power, no line of sight, not ready).
func (b *Brain) observeFailures(c *Ctx) {
	cs := c.Spells.CastState()
	b.mem.mu.Lock()
	seen := b.mem.failSeen
	b.mem.failSeen = cs.Failures
	b.mem.mu.Unlock()
	if cs.Failures == seen || cs.LastFailure == nil {
		return
	}
	f := cs.LastFailure
	d, ok := holdFor[f.ReasonName]
	if !ok {
		d = defaultHold
	}
	if f.ReasonName == "bad_targets" {
		// A refusal of the target for a cast at the player itself is a wrong target, not a moment's matter: it will be the same next
		// time, so the cast stays off for long.
		b.mem.mu.Lock()
		last := b.mem.lastCastTarget
		b.mem.mu.Unlock()
		if last == c.Self.GUID {
			d = selfBadTargetsHold
		}
	}
	b.mem.hold(f.SpellID, c.Now.Add(d), f.ReasonName)
	if f.ReasonName == "line_of_sight" {
		// The mesh cannot see every wall; the server just did. Let the position rules know, so the player moves instead of
		// casting into the wall again when the hold ends.
		// The refusal does not say whom the cast aimed at: only a cast at the fight's mob is a wall to it (not a heal or a buff, or
		// a spell at another mob).
		b.mem.mu.Lock()
		last := b.mem.lastCastTarget
		b.mem.mu.Unlock()
		if mob := ValFight.Get(c).Mob; mob != nil && mob.GUID == last {
			markLOSBlocked(c, mob)
		}
	}
	if f.ReasonName == "not_infront" || f.ReasonName == "unit_not_infront" {
		b.mem.mu.Lock()
		b.mem.faceUntil = c.Now.Add(3 * time.Second)
		b.mem.mu.Unlock()
	}
}

// walkFailed reports a step of the player that the game refused: the action is held back for ErrorHold and the failure shows as an
// error decision. It runs on the step's goroutine, a moment after the action that started the step was recorded as ok.
//
// A walk that fails because the navmesh data is not there (the folder lacks the map's file or tiles) will fail the same way every time:
// the action is held back for navMissingHold instead, and the error is said once per navMissingHold, not at every try.
func (b *Brain) walkFailed(action string, err error) {
	now := b.cfg.Now()
	hold := b.cfg.ErrorHold
	var missing interface{ Unavailable() bool }
	if errors.As(err, &missing) && missing.Unavailable() {
		hold = navMissingHold
		b.mem.mu.Lock()
		at, said := b.mem.walkErrAt[err.Error()]
		if !said || now.Sub(at) >= navMissingHold {
			if b.mem.walkErrAt == nil {
				b.mem.walkErrAt = map[string]time.Time{}
			}
			b.mem.walkErrAt[err.Error()] = now
			said = false
		}
		b.mem.mu.Unlock()
		if action != "" {
			b.mem.holdAction(action, now.Add(hold), "navmesh_missing")
		}
		if said {
			return
		}
	} else if action != "" {
		b.mem.holdAction(action, now.Add(hold), "error")
	}
	d := Decision{Trigger: "walk", Action: action, Result: ResultError, Error: err.Error(), at: now}
	b.record(d)
	b.emitDecision(d)
}

// navMissingHold is how long a walk that failed for lack of navmesh data stays off, and how seldom the failure is reported again.
const navMissingHold = 5 * time.Minute

// possible reports whether a can run now, and when it cannot, why.
func (b *Brain) possible(c *Ctx, a *Action) (bool, string) {
	if why, held := b.mem.actionHeld(a.Name, c.Now); held {
		return false, "held:" + why
	}
	if a.isCast() {
		if ok, why := castable(c, a, b.mem); !ok {
			return false, why
		}
	}
	if a.Possible != nil && !a.Possible(c) {
		return false, "condition"
	}
	return true, ""
}

// rangeSlack is the margin on a spell's range: the server measures between the units' combat reaches.
const rangeSlack = 1.5

// castable checks a cast action against the spell state, the spell's metadata and the target.
func castable(c *Ctx, a *Action, m *memory) (bool, string) {
	id, ok := a.spell(c)
	if !ok {
		return false, "unknown_spell"
	}
	if why, held := m.held(id, c.Now); held {
		return false, "held:" + why
	}
	meta, ok := spells.Lookup(id)
	if !ok {
		return false, "no_meta"
	}
	if !c.Spells.Ready(id) {
		return false, "not_ready"
	}
	if !Affordable(c, meta) {
		return false, "no_power"
	}
	tg := c.Self.GUID
	if a.Target != nil {
		tg = a.Target(c)
	}
	if tg == 0 {
		return false, "no_target"
	}
	if tg == c.Self.GUID {
		return true, ""
	}
	u, ok := c.Unit(tg)
	if !ok {
		return false, "target_not_in_view"
	}
	if u.Dead && !a.Dead {
		return false, "target_dead"
	}
	if a.Harmful && !u.Hostile {
		return false, "target_not_hostile"
	}
	if meta.RangeMax > 0 {
		if u.Distance < 0 {
			return false, "no_position"
		}
		if u.Distance > meta.RangeMax+rangeSlack {
			return false, "out_of_range"
		}
		if meta.RangeMin > 0 && u.Distance < meta.RangeMin {
			return false, "too_close"
		}
	}
	return true, ""
}

// Affordable reports whether the player holds the cost of a spell in the power it is paid in. A cost given as a percent is taken of
// the base mana (the base health for a spell paid in health), as the core does (Spell::CalculatePowerCost): the update fields have it
// (Unit.BaseMana); a game that does not show it falls back to the maximum pool, a little more than the base, and the server's refusal
// (no_power) backs the spell off when that is too high.
func Affordable(c *Ctx, m spells.Meta) bool {
	cost := spellCostOf(c, m)
	if cost == 0 {
		return true
	}
	if m.Power == spells.PowerHealth {
		return c.Self.Health > cost
	}
	if m.Power < spells.PowerMana || m.Power > spells.PowerEnergy {
		return false
	}
	return int8(c.Self.PowerType) == m.Power && c.Self.Power >= cost
}

// spellCostOf is what the core takes for the spell now: the flat cost plus the percent cost of the base mana or base health (of the
// maximum pool for rage, focus and energy).
func spellCostOf(c *Ctx, m spells.Meta) uint32 {
	cost := m.Cost
	if m.CostPct > 0 {
		base := c.Self.MaxPower
		switch {
		case m.Power == spells.PowerHealth && c.Self.BaseHealth > 0:
			base = c.Self.BaseHealth
		case m.Power == spells.PowerMana && c.Self.BaseMana > 0:
			base = c.Self.BaseMana
		}
		cost += m.CostPct * base / 100
	}
	return cost
}
