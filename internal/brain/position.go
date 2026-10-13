package brain

import (
	"context"
	"errors"
	"strings"
	"time"

	"github.com/pjerra/turtle-agent/internal/objects"
	"github.com/pjerra/turtle-agent/internal/spells"
)

// Positioning (issue #80 step 3b). Ideas from mod-playerbots' ranged distance rules, tank facing and area avoidance (GPL-2.0+;
// numbers and ideas only, no code); the line of sight is a navmesh raycast because the agent has no vmaps. Every movement is a
// step through the game's walker under the move lock (movelock.go), so it never walks while the dungeon driver does.

// ---- values ----

// Position issues, in the order they are checked.
const (
	issueNoLOS    = "no_line_of_sight"
	issueFrontal  = "in_mobs_front"
	issueTooClose = "too_close"
	issueTooFar   = "too_far"
)

// positionIssue says what is wrong with where the player stands for the fight's mob, "" when nothing is. A mob that attacks the
// player is the aggro response's business, so there is no issue while one does.
func positionIssue(c *Ctx) string {
	f := ValFight.Get(c)
	m := f.Mob
	if m == nil || !m.InCombat || m.Distance < 0 || ValMobOnMe.Get(c) != nil {
		return ""
	}
	if clear, known := c.LineOfSight(m); known && !clear {
		return issueNoLOS
	}
	if m.Distance2D <= rangedMax+bandSlack && inFront(m, selfPos(c)) {
		return issueFrontal
	}
	switch d := float64(m.Distance2D); {
	case d < rangedMin-bandSlack:
		return issueTooClose
	case d > rangedMax+bandSlack:
		return issueTooFar
	}
	return ""
}

func fightMob(c *Ctx) *objects.Unit { return ValFight.Get(c).Mob }

// ValHazard is the ground effect the player stands in: a dynamic object of an enemy (or a harmful spell) that covers the player.
type Hazard struct {
	Center Vec3
	Radius float32
	Spell  uint32
}

// periodic damage auras: SPELL_AURA_PERIODIC_DAMAGE, _LEECH, _DAMAGE_PERCENT (SpellAuraDefines.h).
func periodicDamage(spell uint32) bool {
	m, ok := spells.Lookup(spell)
	if !ok {
		return false
	}
	for _, e := range m.Effects {
		if e.Aura == 3 || e.Aura == 53 || e.Aura == 89 {
			return true
		}
	}
	return false
}

// harmfulArea says whether a ground effect is the party's enemy: its caster is a hostile mob in view, or it is the source of a
// periodic damage aura the player carries, or (its caster out of view) its spell is a harmful or damaging one. The party's own
// areas (a bot's Blizzard, a paladin's Consecration) are not.
func harmfulArea(c *Ctx, d objects.DynamicObject) bool {
	if d.Caster == c.Self.GUID {
		return false
	}
	for _, a := range c.Self.Auras {
		if a.SpellID == d.Spell && a.Harmful && periodicDamage(a.SpellID) {
			return true // the player is being burnt by this very spell, whoever the object says cast it
		}
	}
	for _, m := range c.Party {
		if m.GUID == d.Caster {
			return false
		}
	}
	if u, ok := c.Unit(d.Caster); ok {
		return hostileMob(u)
	}
	if m, ok := spells.Lookup(d.Spell); ok {
		return m.Flags.Has(spells.FlagHarmful) || m.Flags.Has(spells.FlagDamage)
	}
	return false
}

// hazards lists the harmful ground effects in view with a position and a radius.
func hazards(c *Ctx) []Hazard {
	g := groundOf(c)
	if g == nil {
		return nil
	}
	var out []Hazard
	for _, d := range g.DynamicObjects() {
		if d.Distance < 0 || d.Radius <= 0 || !harmfulArea(c, d) {
			continue
		}
		out = append(out, Hazard{Center: Vec3{d.X, d.Y, d.Z}, Radius: d.Radius, Spell: d.Spell})
	}
	return out
}

// ValHazards caches hazards for the tick.
var ValHazards = &Value[[]Hazard]{Name: "hazards", Calc: hazards}

// hazardSlack is the margin round a ground effect that the brain keeps: the server measures the radius from the unit's reach.
const hazardSlack = 1.0

// inGroundHazard reports whether a point lies in a hazard, margin yards wider than its radius.
func inGroundHazard(c *Ctx, p Vec3, margin float64) bool {
	for _, h := range ValHazards.Get(c) {
		if dist2(p, h.Center) < float64(h.Radius)+margin {
			return true
		}
	}
	return false
}

// ValStandingIn is the nearest hazard the player stands in.
var ValStandingIn = &Value[*Hazard]{Name: "standing_in", Calc: func(c *Ctx) *Hazard {
	var best *Hazard
	hs := ValHazards.Get(c)
	for i := range hs {
		h := &hs[i]
		if dist2(selfPos(c), h.Center) < float64(h.Radius)+hazardSlack && (best == nil || h.Radius > best.Radius) {
			best = h
		}
	}
	return best
}}

// findTank is the party member the brain sees as the tank, in view and alive: the one Params.Tank names, else the first of
// warrior, paladin and druid.
func findTank(c *Ctx) *objects.Unit {
	var best *objects.Unit
	for _, m := range c.Party {
		if !m.Online || m.Dead {
			continue
		}
		u, ok := c.Unit(m.GUID)
		if !ok || u.Dead || u.Distance < 0 {
			continue
		}
		if c.P.Tank != "" {
			if strings.EqualFold(m.Name, c.P.Tank) {
				return u
			}
			continue
		}
		if tankRank(u.Class) < 3 && (best == nil || tankRank(u.Class) < tankRank(best.Class)) {
			best = u
		}
	}
	return best
}

// ---- strategies ----

// ranged_position keeps the player where a ranged class belongs: 20 to 28 yards from the fight's mob, with sight of it and out of
// its front, and brings a mob that has picked the player toward the tank. All of it is movement, so it is off when the game cannot
// walk or see the mesh, when the run switched it off, and while the dungeon driver holds the move lock.
func rangedPosition() *Strategy {
	moving := func(c *Ctx) bool { return canMove(c) }
	return &Strategy{
		Name: "ranged_position",
		Actions: []*Action{
			{Name: "reposition", Settle: 700 * time.Millisecond,
				Possible: func(c *Ctx) bool {
					m := fightMob(c)
					if m == nil || !canMove(c) {
						return false
					}
					_, ok := bandSpot(c, "band", m)
					return ok
				},
				Useful: func(c *Ctx) bool { return positionIssue(c) != "" },
				Do: func(ctx context.Context, c *Ctx) error {
					m := fightMob(c)
					if m == nil {
						return nil
					}
					p, ok := bandSpot(c, "band", m)
					if !ok {
						return errors.New("no spot")
					}
					forgetSpot(c)
					return c.walk(ctx, p)
				}},
			{Name: "step_away", Settle: 700 * time.Millisecond,
				Possible: func(c *Ctx) bool {
					m := ValRootedMob.Get(c)
					if m == nil || !canMove(c) {
						return false
					}
					_, ok := bandSpot(c, "away", m)
					return ok
				},
				Do: func(ctx context.Context, c *Ctx) error {
					m := ValRootedMob.Get(c)
					if m == nil {
						return nil
					}
					p, ok := bandSpot(c, "away", m)
					if !ok {
						return errors.New("no spot")
					}
					forgetSpot(c)
					return c.walk(ctx, p)
				}},
			{Name: "run_to_tank", Settle: 300 * time.Millisecond,
				Possible: func(c *Ctx) bool { return canMove(c) && runToTankPoint(c) != nil },
				Do: func(ctx context.Context, c *Ctx) error {
					p := runToTankPoint(c)
					if p == nil {
						return nil
					}
					return c.walk(ctx, *p)
				}},
		},
		Rules: []Rule{
			{Trigger: Trigger{Name: "bad_position", Active: func(c *Ctx) bool { return moving(c) && positionIssue(c) != "" }},
				Do: []Next{{"reposition", RelMove}}},
			{Trigger: Trigger{Name: "mob_on_me_far", Active: func(c *Ctx) bool { return moving(c) && runToTankPoint(c) != nil }},
				Do: []Next{{"run_to_tank", RelMove}}},
		},
	}
}

// tankMeet is how far from the tank the brain stops when it brings a mob over, and pickupFrom the distance to the tank from
// which it bothers.
const (
	tankMeet   = 4.0
	pickupFrom = 8.0
)

// runToTankPoint is where the player runs to bring the mob that has picked it to the tank: a point tankMeet yards from the tank on
// the way from the tank to the player. nil when no mob is on the player, or it is already in melee (running then costs free hits;
// the class has its answer for that), or it is rooted, or the tank is out of view or already near.
func runToTankPoint(c *Ctx) *Vec3 {
	mob := ValMobOnMe.Get(c)
	if mob == nil || mob.Distance < 0 || mob.Distance <= MeleeRange+1 || HasAura(mob, "Frost Nova") {
		return nil
	}
	tank := findTank(c)
	if tank == nil || float64(tank.Distance2D) <= pickupFrom {
		return nil
	}
	me, tp := selfPos(c), unitPos(tank)
	d := dist2(me, tp)
	f := float32(tankMeet / d)
	p := Vec3{tp[0] + (me[0]-tp[0])*f, tp[1] + (me[1]-tp[1])*f, tp[2]}
	return &p
}

// ValRootedMob is the nearest mob on the player within 12 yards that carries a root of the mage's (Frost Nova).
var ValRootedMob = &Value[*objects.Unit]{Name: "rooted_mob", Calc: func(c *Ctx) *objects.Unit {
	for i := range c.Units {
		u := &c.Units[i]
		if hostileMob(u) && u.InCombat && u.Target == c.Self.GUID && u.Distance >= 0 && u.Distance <= 12 && HasAura(u, "Frost Nova") {
			return u
		}
	}
	return nil
}}

// avoid_ground leaves a ground effect of the enemy's (a dynamic object covering the player, or the source of a periodic damage
// aura on the player) along the navmesh. It outranks a cast in progress: the effect hurts every second.
func avoidGround() *Strategy {
	return &Strategy{
		Name: "avoid_ground",
		Actions: []*Action{
			{Name: "leave_ground_aoe", Settle: time.Second,
				Possible: func(c *Ctx) bool {
					if !canMove(c) || ValStandingIn.Get(c) == nil {
						return false
					}
					_, ok := escapeSpot(c)
					return ok
				},
				Do: func(ctx context.Context, c *Ctx) error {
					p, ok := escapeSpot(c)
					if !ok {
						return errors.New("no spot")
					}
					forgetSpot(c)
					return c.walk(ctx, p)
				}},
		},
		Rules: []Rule{
			{Trigger: Trigger{Name: "in_ground_aoe", Active: func(c *Ctx) bool { return canMove(c) && ValStandingIn.Get(c) != nil }},
				Do: []Next{{"leave_ground_aoe", RelInterrupt + 2}}},
		},
	}
}

// escapeSpot is the nearest point outside every hazard (and the margin), away from the one the player stands in.
func escapeSpot(c *Ctx) (Vec3, bool) {
	h := ValStandingIn.Get(c)
	if h == nil {
		return Vec3{}, false
	}
	return memoSpot(c, "escape", 0, func() (Vec3, bool) {
		me := selfPos(c)
		sp := spotSpec{kind: "escape", center: me, radii: []float64{5, 8, 11, 14}, base: bearing(h.Center, me),
			extra: func(p Vec3) bool { return !inGroundHazard(c, p, 2) }}
		return findSpot(c, sp)
	})
}
