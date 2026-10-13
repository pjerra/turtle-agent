package brain

import (
	"context"
	"time"

	"github.com/pjerra/turtle-agent/internal/objects"
	"github.com/pjerra/turtle-agent/internal/packet"
	"github.com/pjerra/turtle-agent/internal/spells"
)

// Interrupts (issue #80 step 3b). An enemy's SMSG_SPELL_START shows on its unit as Casting (spell, end, the unit it is aimed at).
// The brain interrupts a cast that matters: a heal, a crowd-control spell (fear, stun, sleep, polymorph, charm), or a damage
// spell aimed at a party member or at the player. The relevance is the interrupt band (40), which also lets it break the
// player's own cast. Behaviour after mod-playerbots' interrupt triggers (GPL-2.0+; ideas only).

const (
	// interruptMinLeft is the least time a cast must have left: a Counterspell sent later lands after the spell went off.
	interruptMinLeft = 350 * time.Millisecond
	// interruptedHold is how long the brain leaves a cast alone that it already tried to interrupt.
	interruptedHold = 3 * time.Second
	// interruptedKeep is how long an entry of that memory stays before it is pruned.
	interruptedKeep = 10 * time.Second
)

// crowd-control mechanics (SpellDefines.h Mechanics): charm, fear, sleep, stun, knockout, polymorph, horror.
var ccMechanics = map[uint8]bool{1: true, 5: true, 10: true, 12: true, 14: true, 17: true, 24: true}

// castPriority rates an enemy cast for interruption: 0 leaves it, 3 heals and crowd control, 2 damage on the party.
func castPriority(c *Ctx, caster *objects.Unit, cast *objects.Cast) int {
	meta, known := spells.Lookup(cast.SpellID)
	if known && !cast.Channel && !meta.Flags.Has(spells.FlagInterruptible) {
		return 0
	}
	if cast.EndsInMS != nil && time.Duration(*cast.EndsInMS)*time.Millisecond < interruptMinLeft {
		return 0
	}
	if known {
		if meta.Flags.Has(spells.FlagHeal) || ccMechanics[meta.Mechanic] {
			return 3
		}
	}
	onParty := cast.Target == c.Self.GUID
	for _, m := range c.Party {
		if m.GUID == cast.Target {
			onParty = true
		}
	}
	if onParty && (!known || meta.Flags.Has(spells.FlagDamage) || meta.Flags.Has(spells.FlagHarmful)) {
		return 2
	}
	return 0
}

// interruptTarget finds the enemy cast to interrupt with the named spell: in the spell's range, the highest priority, the soonest
// to end among those.
func interruptTarget(c *Ctx, spell string) *objects.Unit {
	id, ok := c.Spells.Highest(spell)
	if !ok {
		return nil
	}
	meta, ok := spells.Lookup(id)
	if !ok {
		return nil
	}
	var best *objects.Unit
	bestPri := 0
	e := &c.mem.ext
	for i := range c.Units {
		u := &c.Units[i]
		if !hostileMob(u) || u.Casting == nil || u.Distance < 0 {
			continue
		}
		if meta.RangeMax > 0 && u.Distance > meta.RangeMax {
			continue
		}
		e.mu.Lock()
		t, tried := e.interrupted[interruptKey{u.GUID, u.Casting.SpellID}]
		e.mu.Unlock()
		if tried && c.Now.Sub(t) < interruptedHold {
			continue
		}
		pri := castPriority(c, u, u.Casting)
		if pri == 0 {
			continue
		}
		if best == nil || pri > bestPri || (pri == bestPri && endsIn(u.Casting) < endsIn(best.Casting)) {
			best, bestPri = u, pri
		}
	}
	return best
}

func endsIn(c *objects.Cast) int64 {
	if c.EndsInMS == nil {
		return 1 << 40
	}
	return *c.EndsInMS
}

// interruptStrategy interrupts with the named spell (the class's Counterspell, Pummel, Kick...).
func interruptStrategy(name, spell string) *Strategy {
	val := &Value[*objects.Unit]{Name: "interrupt_target:" + name, Calc: func(c *Ctx) *objects.Unit { return interruptTarget(c, spell) }}
	return &Strategy{
		Name: name,
		Actions: []*Action{{Name: "interrupt", Spell: spell,
			Target: func(c *Ctx) packet.GUID {
				if u := val.Get(c); u != nil {
					return u.GUID
				}
				return 0
			},
			Do: func(ctx context.Context, c *Ctx) error {
				u := val.Get(c)
				id, ok := c.Spells.Highest(spell)
				if u == nil || !ok || u.Casting == nil {
					return nil
				}
				if err := c.G.Cast(ctx, id, u.GUID); err != nil {
					return err
				}
				e := &c.mem.ext
				e.mu.Lock()
				if e.interrupted == nil {
					e.interrupted = map[interruptKey]time.Time{}
				}
				e.interrupted[interruptKey{u.GUID, u.Casting.SpellID}] = c.Now
				for k, t := range e.interrupted { // forget casts long gone
					if c.Now.Sub(t) > interruptedKeep {
						delete(e.interrupted, k)
					}
				}
				e.mu.Unlock()
				return nil
			}}},
		Rules: []Rule{{Trigger: Trigger{Name: "enemy_casting", Active: func(c *Ctx) bool { return val.Get(c) != nil }},
			Do: []Next{{"interrupt", RelInterrupt}}}},
	}
}

// mageInterrupt is Counterspell.
func mageInterrupt() *Strategy { return interruptStrategy("mage_interrupt", "Counterspell") }
