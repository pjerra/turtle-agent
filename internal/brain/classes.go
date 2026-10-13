package brain

import (
	"context"
	"math"
	"strconv"
	"strings"
	"time"

	"github.com/pjerra/turtle-agent/internal/game"
	"github.com/pjerra/turtle-agent/internal/objects"
	"github.com/pjerra/turtle-agent/internal/packet"
)

// What the class presets of step 4b share (issue #80): the rogue, the hunter and the retribution paladin (rogue.go, hunter.go,
// paladin.go). The numbers and the order of the rotations are ideas from mod-playerbots' rotation tables (GPL-2.0+; ideas only, no
// code), with every spell name, level, cost and range checked against the core's spell data in the tests.

// relEmergency is the relevance of an emergency answer (Evasion, Vanish, Divine Protection, Feign Death...): just above the interrupt
// band, so that it also cancels a walk and may break a cast.
const relEmergency = RelInterrupt + 3

// swingResend is the least time between two attack orders at the same mob. Auto attack is a toggle the server keeps on its own (a
// swing refused for facing or range resumes when the player is in place), so an order that is sent again every tick only starves
// the abilities and the turn to the mob; a swing that stopped is ordered again after this long.
const swingResend = 3 * time.Second

// swingFaceBlocked says the server refused the swing for facing (observeSwing) and the player still has its back to the mob: the
// swing waits and face_target, its prerequisite, turns the player first.
func swingFaceBlocked(c *Ctx, m *objects.Unit) bool {
	if m == nil {
		return false
	}
	c.mem.mel.mu.Lock()
	until := c.mem.mel.swingFaceUntil
	c.mem.mel.mu.Unlock()
	return c.Now.Before(until) && math.Abs(angleDiff(c.Self.O, game.Facing(c.Self.X, c.Self.Y, m.X, m.Y))) > ConeHalfAngle/2
}

// swingSentRecently says the attack order at the mob went out less than swingResend ago.
func swingSentRecently(c *Ctx, m *objects.Unit) bool {
	c.mem.mel.mu.Lock()
	defer c.mem.mel.mu.Unlock()
	return m != nil && c.mem.mel.swingTarget == m.GUID && c.Now.Sub(c.mem.mel.swingAt) < swingResend
}

// swingAction is the auto attack at the mob mobOf picks, shared by every melee class: `target` the mob and `attack` when it is within
// reach, the swing is not on it already (see swingingAt for how the swing is known) and the order was not sent a moment ago. A swing
// the server refused for facing is not ordered again; the player turns to the mob first (swingFaceBlocked), so that the swing does not
// shut out face_target and the abilities.
func swingAction(name string, mobOf func(*Ctx) *objects.Unit) *Action {
	inReach := func(c *Ctx) *objects.Unit {
		m := mobOf(c)
		if m == nil || m.Distance < 0 || float64(m.Distance2D) > meleeReach {
			return nil
		}
		return m
	}
	return &Action{Name: name, Harmful: true, Threat: ThreatSingle, Pre: []string{"face_target"},
		Target: func(c *Ctx) packet.GUID {
			if m := mobOf(c); m != nil {
				return m.GUID
			}
			return 0
		},
		Possible: func(c *Ctx) bool {
			m := inReach(c)
			return m != nil && !swingFaceBlocked(c, m)
		},
		Useful: func(c *Ctx) bool {
			m := mobOf(c)
			return !swingingAt(c, m) && !swingSentRecently(c, m)
		},
		Do: func(ctx context.Context, c *Ctx) error {
			m := mobOf(c)
			if m == nil {
				return nil
			}
			if c.G.Selected() != m.GUID {
				if err := c.G.Target(ctx, m.GUID); err != nil {
					return err
				}
			}
			if err := c.G.Attack(ctx); err != nil {
				return err
			}
			c.mem.mel.mu.Lock()
			c.mem.mel.swingTarget, c.mem.mel.swingAt = m.GUID, c.Now
			c.mem.mel.mu.Unlock()
			return nil
		}}
}

// onPower says the player plays on the power (a class that plays on another one has no use for these rules).
func onPower(c *Ctx, power uint8) bool { return c.SelfOK && c.Self.PowerType == power }

// restNow says out-of-combat upkeep may run now: the brain may rest (a rest gate, or a brain started by hand), the player is not in
// combat and there is no mob to fight.
func restNow(c *Ctx) bool { return c.RestOK && c.SelfOK && !inCombat(c) && ValFight.Get(c).Mob == nil }

// seenAura is the first of the aura names the unit carries, "" when it carries none.
func seenAura(u *objects.Unit, names ...string) string {
	for _, n := range names {
		if HasAura(u, n) {
			return n
		}
	}
	return ""
}

// auraWithPrefix is the first aura of the unit whose name starts with prefix ("Seal of "), "" when there is none.
func auraWithPrefix(u *objects.Unit, prefix string) string {
	for _, a := range u.Auras {
		if strings.HasPrefix(strings.ToLower(a.Name), strings.ToLower(prefix)) {
			return a.Name
		}
	}
	return ""
}

// knows reports whether the spellbook has a spell of that name.
func knows(c *Ctx, name string) bool {
	_, ok := c.Spells.Highest(name)
	return ok
}

// spellByName is a Resolve that finds the highest known rank of the spell the function names ("" for none).
func spellByName(name func(*Ctx) string) func(*Ctx) (uint32, bool) {
	return func(c *Ctx) (uint32, bool) {
		n := name(c)
		if n == "" {
			return 0, false
		}
		return c.Spells.Highest(n)
	}
}

// tankInView is the tank the brain sees, nil without one in view (findTank).
func tankInView(c *Ctx) *objects.Unit { return findTank(c) }

// ---- small helpers over the units in view ----

// mobsNear counts the living hostile mobs in combat within r yards of the unit (the unit included when it is one).
func mobsNear(c *Ctx, around *objects.Unit, r float64) int {
	if around == nil {
		return 0
	}
	n := 0
	for i := range c.Units {
		u := &c.Units[i]
		if hostileMob(u) && u.InCombat && dist2(unitPos(u), unitPos(around)) <= r {
			n++
		}
	}
	return n
}

// heldKey reads a timer the class strategies keep in the action holds (a hold name that is not an action): the reason it was set
// with, and whether it still stands.
func heldKey(c *Ctx, key string) (string, bool) { return c.mem.actionHeld(key, c.Now) }

// setKey starts a timer until now+d with a reason; a zero d clears it.
func setKey(c *Ctx, key string, d time.Duration, reason string) {
	if d <= 0 {
		c.mem.holdAction(key, time.Time{}, "")
		return
	}
	c.mem.holdAction(key, c.Now.Add(d), reason)
}

func guidStr(g packet.GUID) string { return strconv.FormatUint(uint64(g), 10) }
