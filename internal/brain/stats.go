package brain

import (
	"sort"
	"sync"
	"time"

	"github.com/pjerra/turtle-agent/internal/packet"
)

// What the brain counts about the character's own play (leader-mode pull test, README "Pull test in leader mode"): the numbers an A/B of
// two rotations is read by. They are counted from the agent's own view and the server's answers, nothing more:
//
//   - Actions: the actions that ran, by id (a cast that was sent; the server may still refuse it);
//   - Errors: actions whose game call failed;
//   - CastFailed: casts the server refused, by SpellCastResult name (out_of_range, no_power, line_of_sight, not_infront...);
//   - CombatS: seconds the character was in combat while the brain ticked; IdleCombatS: of those, the seconds in which the brain ran
//     nothing and the character neither cast, channelled, settled a cast nor walked: waiting for a cooldown, power or the tank;
//   - MobOnMeS: seconds with at least one mob in combat that had the character as its target; MobsOnMe: how many different mobs did;
//   - PowerSpent: power the character lost in combat, in the power's own unit (mana, energy, rage in tenths): the sum of the drops
//     between two ticks, so that regeneration between them hides a little of it. Damage or healing per power uses it.
//
// ResetStats starts a new window (the pull test resets before every try).

// Stats is a snapshot of the counters.
type Stats struct {
	Actions     map[string]int `json:"actions"`
	Errors      int            `json:"errors"`
	CastFailed  map[string]int `json:"cast_failed"`
	CombatS     float64        `json:"combat_s"`
	IdleCombatS float64        `json:"idle_combat_s"`
	MobOnMeS    float64        `json:"mob_on_me_s"`
	MobsOnMe    int            `json:"mobs_on_me"`
	PowerSpent  uint64         `json:"power_spent"`
	MinPowerPct int            `json:"min_power_pct"` // the lowest power percent seen in combat; -1: none
}

// ActionsTotal is the number of actions that ran.
func (s Stats) ActionsTotal() int {
	n := 0
	for _, v := range s.Actions {
		n += v
	}
	return n
}

// CastFailedTotal is the number of casts the server refused.
func (s Stats) CastFailedTotal() int {
	n := 0
	for _, v := range s.CastFailed {
		n += v
	}
	return n
}

// ReasonsSorted lists the refusal reasons, most frequent first.
func (s Stats) ReasonsSorted() []string {
	var out []string
	for k := range s.CastFailed {
		out = append(out, k)
	}
	sort.Slice(out, func(i, j int) bool {
		if s.CastFailed[out[i]] != s.CastFailed[out[j]] {
			return s.CastFailed[out[i]] > s.CastFailed[out[j]]
		}
		return out[i] < out[j]
	})
	return out
}

// maxStatGap bounds the time one tick adds: a tick that follows a long pause (a dead character, a stopped brain) counts one second at most.
const maxStatGap = time.Second

type statsMem struct {
	mu         sync.Mutex
	s          Stats
	last       time.Time
	mobs       map[packet.GUID]bool
	prevPower  uint32
	havePrev   bool
	prevPowerT uint8
}

func (m *statsMem) reset() {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.s = Stats{Actions: map[string]int{}, CastFailed: map[string]int{}, MinPowerPct: -1}
	m.mobs = map[packet.GUID]bool{}
	m.havePrev = false
	m.last = time.Time{}
}

func (m *statsMem) snapshot() Stats {
	m.mu.Lock()
	defer m.mu.Unlock()
	s := m.s
	s.Actions, s.CastFailed = map[string]int{}, map[string]int{}
	for k, v := range m.s.Actions {
		s.Actions[k] = v
	}
	for k, v := range m.s.CastFailed {
		s.CastFailed[k] = v
	}
	s.MobsOnMe = len(m.mobs)
	s.CombatS, s.IdleCombatS, s.MobOnMeS = round2(s.CombatS), round2(s.IdleCombatS), round2(s.MobOnMeS)
	return s
}

func round2(v float64) float64 { return float64(int64(v*100+0.5)) / 100 }

// statDecision counts an action that ran (or failed).
func (b *Brain) statDecision(d Decision) {
	m := &b.mem.stat
	m.mu.Lock()
	defer m.mu.Unlock()
	switch d.Result {
	case ResultOK:
		m.s.Actions[d.Action]++
	case ResultError:
		m.s.Errors++
	}
}

// statRefused counts a cast the server refused.
func (b *Brain) statRefused(reason string, n uint64) {
	m := &b.mem.stat
	m.mu.Lock()
	defer m.mu.Unlock()
	if reason == "" {
		reason = "unknown"
	}
	m.s.CastFailed[reason] += int(n)
}

// statTick accounts the time of one tick: combat, idle, mobs on the character, power spent. acted says the tick ran an action.
func (b *Brain) statTick(c *Ctx, acted bool) {
	busy := c.Spells.Casting() || b.mem.settling(c.Now) || c.lock.stepping()
	m := &b.mem.stat
	m.mu.Lock()
	defer m.mu.Unlock()
	dt := time.Duration(0)
	if !m.last.IsZero() {
		dt = min(max(c.Now.Sub(m.last), 0), maxStatGap)
	}
	m.last = c.Now
	if !c.Self.InCombat {
		m.havePrev = false
		return
	}
	sec := dt.Seconds()
	m.s.CombatS += sec
	if !acted && !busy {
		m.s.IdleCombatS += sec
	}
	onMe := false
	for i := range c.Units {
		u := &c.Units[i]
		if hostileMob(u) && u.InCombat && u.Target == c.Self.GUID {
			onMe = true
			m.mobs[u.GUID] = true
		}
	}
	if onMe {
		m.s.MobOnMeS += sec
	}
	if m.havePrev && m.prevPowerT == c.Self.PowerType && c.Self.Power < m.prevPower {
		m.s.PowerSpent += uint64(m.prevPower - c.Self.Power)
	}
	m.prevPower, m.prevPowerT, m.havePrev = c.Self.Power, c.Self.PowerType, true
	if p := PowerPct(c); m.s.MinPowerPct < 0 || p < m.s.MinPowerPct {
		m.s.MinPowerPct = p
	}
}
