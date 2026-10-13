package brain

import (
	"context"
	"fmt"
	"math"
	"sort"
	"strings"
	"time"

	"github.com/pjerra/turtle-agent/internal/game"
	"github.com/pjerra/turtle-agent/internal/objects"
	"github.com/pjerra/turtle-agent/internal/packet"
)

// Strategies and presets. A preset is a list of strategies; a strategy is a Go literal built by a constructor, so
// adding a class is adding constructors and one preset line, not touching the engine.
var (
	strategyBuilders = map[string]func() *Strategy{
		"base":       baseStrategy,
		"combat_dps": combatDPS,
		"mage_frost": mageFrost,
		"mage_rest":  mageRest,
	}
	presets = map[string][]string{
		"frost_mage": {"combat_dps", "mage_frost", "mage_rest"},
	}
)

// Strategies lists the names brain_start accepts: presets, then strategies.
func Strategies() (presetNames, strategyNames []string) {
	for n := range presets {
		presetNames = append(presetNames, n)
	}
	for n := range strategyBuilders {
		if n != "base" {
			strategyNames = append(strategyNames, n)
		}
	}
	sort.Strings(presetNames)
	sort.Strings(strategyNames)
	return
}

// Resolve expands presets and builds the strategies, the base strategy first. names returns what is active.
func Resolve(want []string) (out []*Strategy, names []string, err error) {
	var flat []string
	for _, w := range want {
		w = strings.ToLower(strings.TrimSpace(w))
		if p, ok := presets[w]; ok {
			flat = append(flat, p...)
		} else {
			flat = append(flat, w)
		}
	}
	if len(flat) == 0 {
		return nil, nil, fmt.Errorf("strategies: name at least one")
	}
	flat = append([]string{"base"}, flat...)
	seen := map[string]bool{}
	for _, n := range flat {
		if seen[n] {
			continue
		}
		seen[n] = true
		mk, ok := strategyBuilders[n]
		if !ok {
			pn, sn := Strategies()
			return nil, nil, fmt.Errorf("strategy %q is unknown (presets: %s; strategies: %s)", n, strings.Join(pn, ", "), strings.Join(sn, ", "))
		}
		out = append(out, mk())
		names = append(names, n)
	}
	return out, names, nil
}

// ---- shared values ----

// Fight is what the brain is fighting: the mob, who made it the mob, and how long the fight has gone on.
type Fight struct {
	Mob    *objects.Unit
	Tank   *objects.Unit
	Source string        // "tank" (the tank's target), "self" (a mob that attacks the player) or "selected"
	Since  time.Duration // how long a mob has been there to fight, 0 when none
}

// quietGap is how long no mob may be there before the next one starts a new fight (and a new opening delay).
const quietGap = 3 * time.Second

// tankRank orders classes by how likely they are to be the tank: warrior, paladin, druid.
func tankRank(class uint8) int {
	switch class {
	case 1:
		return 0
	case 2:
		return 1
	case 11:
		return 2
	}
	return 3
}

func hostileMob(u *objects.Unit) bool { return u != nil && u.Type != "player" && u.Hostile && !u.Dead }

// ValFight finds the mob to fight. The tank's target comes first: the party member named by Params.Tank, else the
// first of warrior, paladin and druid among the party members in view whose target is a living hostile mob that is
// in combat. Without a tank, a mob that attacks the player is fought, and last the player's own selected target
// when it is in combat. It also keeps the fight's clock for the opening delay.
var ValFight = &Value[Fight]{Name: "fight", Calc: func(c *Ctx) Fight {
	f := Fight{}
	var tanks []*objects.Unit
	for _, m := range c.Party {
		if !m.Online || m.Dead {
			continue
		}
		u, ok := c.Unit(m.GUID)
		if !ok || u.Dead {
			continue
		}
		if c.P.Tank != "" && !strings.EqualFold(m.Name, c.P.Tank) {
			continue
		}
		tanks = append(tanks, u)
	}
	sort.SliceStable(tanks, func(i, j int) bool { return tankRank(tanks[i].Class) < tankRank(tanks[j].Class) })
	for _, t := range tanks {
		if mob, ok := c.Unit(t.Target); ok && hostileMob(mob) && mob.InCombat {
			f.Tank, f.Mob, f.Source = t, mob, "tank"
			break
		}
	}
	if f.Mob == nil {
		if u := ValMobOnMe.Get(c); u != nil {
			f.Mob, f.Source = u, "self"
		}
	}
	if f.Mob == nil {
		if mob, ok := c.Unit(c.G.Selected()); ok && hostileMob(mob) && mob.InCombat {
			f.Mob, f.Source = mob, "selected"
		}
	}
	m := c.mem
	m.mu.Lock()
	defer m.mu.Unlock()
	if f.Mob != nil {
		if m.fightStart.IsZero() || c.Now.Sub(m.lastMob) > quietGap {
			m.fightStart = c.Now
		}
		m.lastMob = c.Now
		f.Since = c.Now.Sub(m.fightStart)
	} else if !m.fightStart.IsZero() && c.Now.Sub(m.lastMob) > quietGap {
		m.fightStart = time.Time{}
	}
	return f
}}

// ValMobOnMe is the nearest living hostile mob in combat whose target is the player.
var ValMobOnMe = &Value[*objects.Unit]{Name: "mob_on_me", Calc: func(c *Ctx) *objects.Unit {
	for i := range c.Units { // nearest first
		u := &c.Units[i]
		if hostileMob(u) && u.InCombat && u.Target == c.Self.GUID {
			return u
		}
	}
	return nil
}}

// MeleeRange is how close a mob on the player is taken to be in melee with it.
const MeleeRange = 8

// ValMeleeMob is the mob on the player that stands within MeleeRange.
var ValMeleeMob = &Value[*objects.Unit]{Name: "melee_mob", Calc: func(c *Ctx) *objects.Unit {
	if u := ValMobOnMe.Get(c); u != nil && u.Distance >= 0 && u.Distance <= MeleeRange {
		return u
	}
	return nil
}}

// ConeRange and ConeHalfAngle size the frontal cone of Cone of Cold.
const (
	ConeRange     = 10
	ConeHalfAngle = math.Pi / 4
)

// ValConeMobs counts the living hostile mobs in combat within ConeRange in front of the player.
var ValConeMobs = &Value[int]{Name: "cone_mobs", Calc: func(c *Ctx) int {
	n := 0
	for i := range c.Units {
		u := &c.Units[i]
		if !hostileMob(u) || !u.InCombat || u.Distance < 0 || u.Distance > ConeRange {
			continue
		}
		if math.Abs(angleDiff(c.Self.O, game.Facing(c.Self.X, c.Self.Y, u.X, u.Y))) <= ConeHalfAngle {
			n++
		}
	}
	return n
}}

// angleDiff is the signed difference a-b between two bearings, in (-pi, pi].
func angleDiff(a, b float32) float64 {
	d := math.Mod(float64(a)-float64(b), 2*math.Pi)
	if d > math.Pi {
		d -= 2 * math.Pi
	} else if d <= -math.Pi {
		d += 2 * math.Pi
	}
	return d
}

// PowerPct is the player's primary power as a percent of its maximum (100 when it has no maximum).
func PowerPct(c *Ctx) int {
	if c.Self.MaxPower == 0 {
		return 100
	}
	return int(uint64(c.Self.Power) * 100 / uint64(c.Self.MaxPower))
}

// HasAura reports whether the unit carries an aura of one of the names.
func HasAura(u *objects.Unit, names ...string) bool {
	if u == nil {
		return false
	}
	for _, a := range u.Auras {
		for _, n := range names {
			if strings.EqualFold(a.Name, n) {
				return true
			}
		}
	}
	return false
}

// auraMissing reports a named aura on the player that is missing or, when the server told how long it has left,
// has under a minute left.
func auraMissing(u *objects.Unit, names ...string) bool {
	for _, a := range u.Auras {
		for _, n := range names {
			if strings.EqualFold(a.Name, n) {
				return a.RemainingMS != nil && *a.RemainingMS >= 0 && *a.RemainingMS < 60_000
			}
		}
	}
	return true
}

func fightTarget(c *Ctx) packet.GUID {
	if m := ValFight.Get(c).Mob; m != nil {
		return m.GUID
	}
	return 0
}

// ---- base ----

// baseStrategy is always on: it keeps the brain from starting a second cast while one is going or being sent.
func baseStrategy() *Strategy {
	return &Strategy{Name: "base", Mult: []Multiplier{{Name: "busy", F: func(c *Ctx, a *Action, rel float32) float32 {
		if rel >= RelInterrupt {
			return 1 // an interrupt or an emergency may break a cast
		}
		if c.Spells.Casting() || c.mem.settling(c.Now) {
			return 0
		}
		return 1
	}}}}
}

// ---- combat_dps ----

// combatDPS is the damage dealer's discipline, whatever the class: wait for the tank to hold the mob, do not
// open on a pack the tank has not engaged, and do not pull loose mobs with an area spell. A 1.12 client has no
// threat list; the opening delay and the mob's target stand in for it.
func combatDPS() *Strategy {
	return &Strategy{
		Name: "combat_dps",
		// The fight's clock must run every tick, also while no rule asks for the fight.
		Before: func(c *Ctx) { ValFight.Get(c) },
		Mult: []Multiplier{
			{Name: "opening_delay", F: func(c *Ctx, a *Action, rel float32) float32 {
				if !a.Harmful {
					return 1
				}
				f := ValFight.Get(c)
				if f.Mob == nil || f.Source == "self" || ValMobOnMe.Get(c) != nil {
					return 1 // a mob that attacks the player is fought at once
				}
				if f.Since < c.P.OpeningDelay {
					return 0
				}
				return 1
			}},
			{Name: "aoe_pulls_loose_mob", F: func(c *Ctx, a *Action, rel float32) float32 {
				// An area spell is only safe when every mob it can reach is already on the tank or on the player.
				if a.Threat != ThreatAoE {
					return 1
				}
				tank := ValFight.Get(c).Tank
				for i := range c.Units {
					u := &c.Units[i]
					if !hostileMob(u) || u.Distance < 0 || u.Distance > ConeRange+2 {
						continue
					}
					if u.Target == c.Self.GUID || (tank != nil && u.Target == tank.GUID) {
						continue
					}
					return 0
				}
				return 1
			}},
		},
		Actions: []*Action{{
			Name: "face_target", Target: fightTarget,
			Possible: func(c *Ctx) bool {
				c.mem.mu.Lock()
				defer c.mem.mu.Unlock()
				return c.Now.Before(c.mem.faceUntil)
			},
			Useful: func(c *Ctx) bool {
				m := ValFight.Get(c).Mob
				return m != nil && math.Abs(angleDiff(c.Self.O, game.Facing(c.Self.X, c.Self.Y, m.X, m.Y))) > ConeHalfAngle/2
			},
			Do: func(ctx context.Context, c *Ctx) error {
				m := ValFight.Get(c).Mob
				if m == nil {
					return nil
				}
				c.mem.mu.Lock()
				c.mem.faceUntil = time.Time{}
				c.mem.mu.Unlock()
				return c.G.Face(ctx, m.GUID)
			},
		}},
	}
}
