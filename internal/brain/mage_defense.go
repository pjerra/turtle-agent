package brain

import (
	"math"

	"github.com/pjerra/turtle-agent/internal/spells"
)

// The mage's answer to a mob that has picked it (issue #80 step 3b). The hard signal is the mob's target being the player: a 1.12
// client has no threat list. The order of the answers is by relevance: Ice Block at critical health (a talent: only if the
// spellbook has it), Blink out of melee when Frost Nova is not there, Frost Nova (mage_frost) when a mob is in melee, a step
// back out of the root's reach once the mob is rooted, and, before the mob arrives, a run toward the tank (ranged_position).

const (
	iceBlockHealthPct = 20 // Ice Block below this, with a mob on the player
	blinkHealthPct    = 50 // Blink below this, with a mob in melee and Frost Nova not available
	blinkReach        = 20.0
	blinkClearAhead   = 12.0 // the way ahead must be walkable this far for Blink (it lands up to 20 yards on)
	blinkGain         = 8.0  // and the landing must be this much farther from the mob
)

func healthPct(c *Ctx) int {
	if c.Self.MaxHealth == 0 {
		return 100
	}
	return int(uint64(c.Self.Health) * 100 / uint64(c.Self.MaxHealth))
}

// canCast reports whether the spell, by name, is known and can be cast now as far as the player's own state tells: off its
// cooldown and the global cooldown, paid for, and not backed off after a refusal.
func canCast(c *Ctx, name string) bool {
	id, ok := c.Spells.Highest(name)
	if !ok {
		return false
	}
	if _, held := c.mem.held(id, c.Now); held {
		return false
	}
	meta, ok := spells.Lookup(id)
	return ok && c.Spells.Ready(id) && Affordable(c, meta)
}

// blinkAhead reports whether Blink, which lands ahead of the way the player faces, would take it out of the mob's reach: the mob is
// behind, the way ahead is walkable, and the landing is farther from the mob by blinkGain.
func blinkAhead(c *Ctx, mob Vec3) bool {
	s := sightOf(c)
	if s == nil {
		return false
	}
	me := selfPos(c)
	if math.Abs(angleDiff(c.Self.O, float32(bearing(me, mob)))) < math.Pi/2 {
		return false // the mob is ahead: Blink would run into it, or past it
	}
	dir := [2]float64{math.Cos(float64(c.Self.O)), math.Sin(float64(c.Self.O))}
	ahead := Vec3{me[0] + float32(dir[0]*blinkClearAhead), me[1] + float32(dir[1]*blinkClearAhead), me[2]}
	if clear, known := s.LineClear(me, ahead); !clear || !known {
		return false
	}
	land := Vec3{me[0] + float32(dir[0]*blinkReach), me[1] + float32(dir[1]*blinkReach), me[2]}
	return dist2(land, mob) >= dist2(me, mob)+blinkGain
}

func mageDefense() *Strategy {
	return &Strategy{
		Name: "mage_defense",
		Actions: []*Action{
			{Name: "ice_block", Spell: "Ice Block", Threat: ThreatNone,
				Useful: func(c *Ctx) bool { return !HasAura(&c.Self, "Ice Block") }},
			{Name: "blink", Spell: "Blink",
				Possible: func(c *Ctx) bool {
					m := ValMeleeMob.Get(c)
					return !c.lock.Held() && m != nil && !HasAura(m, "Frost Nova") && !canCast(c, "Frost Nova") && blinkAhead(c, unitPos(m))
				}},
		},
		Rules: []Rule{
			{Trigger: Trigger{Name: "critical_health_mob_on_me", Active: func(c *Ctx) bool {
				return healthPct(c) < iceBlockHealthPct && ValMobOnMe.Get(c) != nil
			}}, Do: []Next{{"ice_block", RelDispel}}},
			{Trigger: Trigger{Name: "hurt_in_melee_no_nova", Active: func(c *Ctx) bool {
				m := ValMeleeMob.Get(c)
				return m != nil && healthPct(c) < blinkHealthPct && !HasAura(m, "Frost Nova") && !canCast(c, "Frost Nova")
			}}, Do: []Next{{"blink", RelInterrupt + 5}}},
			{Trigger: Trigger{Name: "rooted_mob_close", Active: func(c *Ctx) bool { return canMove(c) && ValRootedMob.Get(c) != nil }},
				Do: []Next{{"step_away", RelMove}}},
		},
	}
}
