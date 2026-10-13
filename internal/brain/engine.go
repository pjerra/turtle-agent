// Package brain is the agent's bot brain: a small trigger -> action engine that plays the leader's character
// (issue #80). Triggers read only the agent's own state (internal/spellstate, the object store, the party list),
// strategies list which action a trigger asks for and how relevant it is, multipliers veto or damp actions, and
// every tick the most relevant action that is possible and useful runs. The design follows the idea of
// mod-playerbots' engine (relevance-ordered triggers, multipliers, prerequisite and alternative chains) and shares
// no code with it.
package brain

import (
	"context"
	"sort"
	"time"

	"github.com/pjerra/turtle-agent/internal/objects"
	"github.com/pjerra/turtle-agent/internal/packet"
	"github.com/pjerra/turtle-agent/internal/spellstate"
)

// Relevance bands. An action's relevance is the band of the rule that asks for it, which multipliers scale and
// which orders the candidates of a tick; the highest one that can run, runs.
const (
	RelIdle      float32 = 0
	RelDefault   float32 = 5
	RelNormal    float32 = 10
	RelHigh      float32 = 20
	RelMove      float32 = 30
	RelInterrupt float32 = 40
	RelDispel    float32 = 50
	RelRaid      float32 = 60

	// Steps of a chain: a prerequisite ranks just above its action's rank, an alternative just above that, so a
	// chain member beats nothing the action itself would not have beaten.
	relPre float32 = 0.002
	relAlt float32 = 0.003
)

// Result is how an action went.
type Result string

const (
	ResultOK          Result = "ok"           // the action ran
	ResultNotPossible Result = "not_possible" // it could not run now (cooldown, power, range, no target, a hold)
	ResultVetoed      Result = "vetoed"       // a multiplier zeroed its relevance
	ResultError       Result = "error"        // it ran and the game call failed
)

// ThreatKind says how an action adds threat; multipliers veto by it.
type ThreatKind uint8

const (
	ThreatNone   ThreatKind = iota
	ThreatSingle            // hits one target
	ThreatAoE               // hits everything near the target or the caster
)

// Action is something the brain can do. A cast action names its spell: the brain casts the highest known rank,
// and checks that the spell is known, off cooldown and the global cooldown, paid for, not held by a back-off,
// and that its target is alive, hostile when the action is harmful, and within the spell's range. Possible adds
// conditions of the action's own. Pre and Alt name other actions (of any active strategy): when this action cannot
// run now, a runnable prerequisite runs first, else a runnable alternative takes its place.
type Action struct {
	Name    string
	Spell   string // the spell's name; empty for an action that is not a cast
	Harmful bool   // aimed at an enemy: the opening delay holds it back
	Threat  ThreatKind
	// Target is the unit the action works on; nil means the player itself. Zero means there is none.
	Target   func(*Ctx) packet.GUID
	Possible func(*Ctx) bool
	Useful   func(*Ctx) bool
	// Do runs the action; nil for a cast action, which casts its spell on Target.
	Do       func(context.Context, *Ctx) error
	Pre, Alt []string
}

// Trigger is a named condition on the agent's state.
type Trigger struct {
	Name   string
	Active func(*Ctx) bool
}

// Next is an action a rule asks for, and how relevant it is.
type Next struct {
	Action string
	Rel    float32
}

// Rule asks for actions while its trigger is active.
type Rule struct {
	Trigger Trigger
	Do      []Next
}

// Multiplier scales the relevance of a candidate: 1 leaves it, a fraction damps it, 0 vetoes it. rel is the
// candidate's relevance before scaling, so a multiplier can leave urgent actions alone.
type Multiplier struct {
	Name string
	F    func(c *Ctx, a *Action, rel float32) float32
}

// Strategy is a set of rules, defaults, multipliers and the actions they use. Strategies add up: the brain runs
// the union of the active ones. Everything refers to actions by name, so a strategy can use actions another one
// defines (a class strategy uses the shared "face_target").
type Strategy struct {
	Name    string
	Rules   []Rule
	Default []Next // asked for every tick, at the given relevance
	Mult    []Multiplier
	Actions []*Action
	// Before runs at the start of every tick, for state that must be followed continuously.
	Before func(*Ctx)
}

// ---- values ----

// Value is a named computation of the agent's state, cached for its TTL (0: for the current tick only), so
// that several triggers can ask for the same thing without recomputing it.
type Value[T any] struct {
	Name string
	TTL  time.Duration
	Calc func(*Ctx) T
}

type cached struct {
	at   time.Time
	tick uint64
	v    any
}

// valueCache lives as long as the brain, so a TTL above one tick reaches over ticks.
type valueCache map[string]cached

// Get returns the value, computing it when the cache has none that is still fresh.
func (v *Value[T]) Get(c *Ctx) T {
	if e, ok := c.vals[v.Name]; ok {
		fresh := e.tick == c.Tick
		if v.TTL > 0 {
			fresh = c.Now.Sub(e.at) < v.TTL
		}
		if fresh {
			return e.v.(T)
		}
	}
	t := v.Calc(c)
	c.vals[v.Name] = cached{at: c.Now, tick: c.Tick, v: t}
	return t
}

// ---- the tick ----

// Ctx is what a tick looks at: one consistent snapshot of the game, taken at the start of the tick.
type Ctx struct {
	G      Game
	Now    time.Time
	Tick   uint64
	Spells *spellstate.State
	Self   objects.Unit // the player's own row; SelfOK false until the game has it
	SelfOK bool
	Units  []objects.Unit // in view, nearest first, the player excluded
	Party  []Member
	P      Params
	// RestOK says out-of-combat upkeep (buffs, drinking) may run now. A brain started by hand always allows it;
	// the dungeon driver allows it only at its rest gates, so the brain never sits down in the middle of a walk.
	RestOK bool

	vals  valueCache
	byID  map[packet.GUID]*objects.Unit
	mem   *memory
	trace []Decision
}

// Unit finds a unit by guid in the snapshot (the player included).
func (c *Ctx) Unit(g packet.GUID) (*objects.Unit, bool) {
	u, ok := c.byID[g]
	return u, ok
}

// Decision is one candidate action of a tick and what became of it.
type Decision struct {
	Trigger string      `json:"trigger"`
	Action  string      `json:"action"`
	Rel     float32     `json:"relevance"`
	Result  Result      `json:"result"`
	Why     string      `json:"why,omitempty"` // the veto's multiplier, or what made the action impossible
	Via     string      `json:"via,omitempty"` // "pre" or "alt" when the action ran as a chain member
	Spell   uint32      `json:"spell_id,omitempty"`
	Target  packet.GUID `json:"target,omitempty"`
	Error   string      `json:"error,omitempty"`
	AgoMS   int64       `json:"ago_ms,omitempty"` // set by Status
	at      time.Time
}

type candidate struct {
	trigger string
	a       *Action
	rel     float32 // after the multipliers
	raw     float32 // as the rule asked for it
	vetoBy  string
}

// candidates lists what the active rules and defaults ask for, scaled by the multipliers, most relevant first.
// A vetoed candidate stays in the list with vetoBy set, so that the tick can say why it did nothing.
func (b *Brain) candidates(c *Ctx) []candidate {
	best := map[string]*candidate{}
	var order []string
	add := func(trigger string, n Next) {
		a := b.actions[n.Action]
		if a == nil {
			return
		}
		if old, ok := best[n.Action]; ok {
			if n.Rel > old.rel {
				old.trigger, old.rel = trigger, n.Rel
			}
			return
		}
		best[n.Action] = &candidate{trigger: trigger, a: a, rel: n.Rel}
		order = append(order, n.Action)
	}
	for _, s := range b.strategies {
		for _, r := range s.Rules {
			if r.Trigger.Active(c) {
				for _, n := range r.Do {
					add(r.Trigger.Name, n)
				}
			}
		}
		for _, n := range s.Default {
			add("default", n)
		}
	}
	out := make([]candidate, 0, len(order))
	for _, name := range order {
		cd := *best[name]
		cd.raw = cd.rel
		cd.rel, cd.vetoBy = b.scale(c, cd.a, cd.rel)
		out = append(out, cd)
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].rel > out[j].rel })
	return out
}

// scale runs the multipliers over a candidate. A veto returns relevance 0 and the vetoing multiplier's name.
func (b *Brain) scale(c *Ctx, a *Action, rel float32) (float32, string) {
	r := rel
	for _, s := range b.strategies {
		for _, m := range s.Mult {
			f := m.F(c, a, rel)
			if f <= 0 {
				return 0, m.Name
			}
			r *= f
		}
	}
	return r, ""
}

// maxChain bounds how deep prerequisites and alternatives nest.
const maxChain = 4

// try runs a if it can, else its prerequisites, else its alternatives. It reports whether something ran and how
// it went. Candidates that could not run are traced for the tick's report.
func (b *Brain) try(ctx context.Context, c *Ctx, a *Action, trigger string, rel float32, via string, seen map[string]bool, depth int) (bool, Decision) {
	if seen[a.Name] || depth > maxChain {
		return false, Decision{}
	}
	seen[a.Name] = true
	ok, why := b.possible(c, a)
	if ok {
		if a.Useful != nil && !a.Useful(c) {
			return false, Decision{}
		}
		return true, b.run(ctx, c, a, trigger, rel, via)
	}
	if via == "" { // a chain member that cannot run either is not worth a line: the action's own reason says more
		c.trace = append(c.trace, Decision{Trigger: trigger, Action: a.Name, Rel: rel, Result: ResultNotPossible, Why: why, at: c.Now})
	}
	chain := func(names []string, step float32, tag string) (bool, Decision) {
		for _, n := range names {
			next := b.actions[n]
			if next == nil {
				continue
			}
			r, by := b.scale(c, next, rel+step)
			if by != "" {
				c.trace = append(c.trace, Decision{Trigger: trigger, Action: next.Name, Rel: rel + step, Result: ResultVetoed, Why: by, Via: tag, at: c.Now})
				continue
			}
			if done, d := b.try(ctx, c, next, trigger, r, tag, seen, depth+1); done {
				return true, d
			}
		}
		return false, Decision{}
	}
	if done, d := chain(a.Pre, relPre, "pre"); done {
		return true, d
	}
	return chain(a.Alt, relAlt, "alt")
}

// run does the action and records how it went; a failed game call holds the spell back for a moment.
func (b *Brain) run(ctx context.Context, c *Ctx, a *Action, trigger string, rel float32, via string) Decision {
	d := Decision{Trigger: trigger, Action: a.Name, Rel: rel, Via: via, at: c.Now}
	if a.Spell != "" {
		d.Spell, _ = c.Spells.Highest(a.Spell)
	}
	if a.Target != nil {
		d.Target = a.Target(c)
	}
	cctx, cancel := context.WithTimeout(ctx, b.cfg.DoTimeout)
	defer cancel()
	var err error
	if a.Do != nil {
		err = a.Do(cctx, c)
	} else {
		t := d.Target
		if a.Target == nil {
			t, d.Target = c.Self.GUID, c.Self.GUID
		}
		err = c.G.Cast(cctx, d.Spell, t)
	}
	if err != nil {
		d.Result, d.Error = ResultError, err.Error()
		if d.Spell != 0 {
			b.mem.hold(d.Spell, c.Now.Add(b.cfg.ErrorHold), "error")
		} else {
			b.mem.holdAction(a.Name, c.Now.Add(b.cfg.ErrorHold))
		}
		return d
	}
	d.Result = ResultOK
	if d.Spell != 0 {
		b.mem.settle(c.Now.Add(b.cfg.Settle))
	}
	return d
}
