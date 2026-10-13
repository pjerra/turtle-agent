package brain

import (
	"fmt"
	"strings"

	"github.com/pjerra/turtle-agent/internal/objects"
	"github.com/pjerra/turtle-agent/internal/packet"
)

// Roles (issue #80 step 3c). A role is a set of strategies that share a discipline: what to fight, when to start, where to stand.
//
//   - dps    (role_dps, the name of combat_dps): fight the tank's mob, wait out the opening delay, never pull loose mobs with an
//     area ability, never hold a mob the tank does not have.
//   - tank   (role_tank): fight the loose add first (a mob attacking a party member), then the mob on the player, hold it, face it
//     away from the group, taunt what is not on the player.
//   - healer (role_healer): not implemented; a request for it fails with a clear message instead of a brain that does nothing.
//
// A strategy names its role (Strategy.Role); Params.Role is checked against the active ones in resolveRole. A class adds its abilities
// as strategies next to the role (warrior_arms, warrior_tank, frost mage), so the same role discipline serves every class.

const (
	RoleDPS    = "dps"
	RoleTank   = "tank"
	RoleHealer = "healer"
)

// healerNotImplemented is the answer to a healer: the engine can hold the strategy (triage, rank by deficit, dispels, mana), but it is
// not written yet.
const healerNotImplemented = "the healer role is not implemented yet (step 4 of issue #80); use role dps or tank"

// resolveRole settles the role of a brain: the one asked for, checked against the roles of the active strategies, or (none asked) the
// role they play, "dps" when none names one.
func resolveRole(want string, strats []*Strategy, names []string) (string, error) {
	want = strings.ToLower(strings.TrimSpace(want))
	switch want {
	case "", RoleDPS, RoleTank, RoleHealer:
	default:
		return "", fmt.Errorf("role %q: want dps, tank or healer", want)
	}
	for _, s := range strats {
		if s.NotImplemented != "" {
			return "", fmt.Errorf("strategy %q is not implemented yet: %s", s.Name, s.NotImplemented)
		}
	}
	if want == RoleHealer {
		return "", fmt.Errorf("role %q: %s", want, healerNotImplemented)
	}
	var active []string
	seen := map[string]bool{}
	for _, s := range strats {
		if s.Role != "" && !seen[s.Role] {
			seen[s.Role] = true
			active = append(active, s.Role)
		}
	}
	switch {
	case len(active) > 1:
		return "", fmt.Errorf("strategies %v play more than one role (%s): name one role's strategies", names, strings.Join(active, ", "))
	case want == "" && len(active) == 1:
		return active[0], nil
	case want == "":
		return RoleDPS, nil
	case len(active) == 1 && active[0] != want:
		return "", fmt.Errorf("role %q does not match the strategies %v, which play %s", want, names, active[0])
	case len(active) == 0 && want == RoleTank:
		return "", fmt.Errorf("role %q needs the role_tank strategy (preset tank_warrior): %v do not play it", want, names)
	}
	return want, nil
}

func init() {
	strategyBuilders["role_tank"] = roleTank
	strategyBuilders["role_healer"] = roleHealer
	aliases["role_dps"] = "combat_dps"
}

// ---- role_tank ----

// tankPickupRange is how far from the tank a mob is taken as one to hold.
const tankPickupRange = 30.0

// tankMob is the mob a tanking player holds: first a loose add (a mob in combat that attacks a party member), then a mob that
// attacks the player, then the player's own selected target when it is in combat. Nearest first within each.
func tankMob(c *Ctx) (*objects.Unit, string) {
	party := map[packet.GUID]bool{}
	for _, m := range c.Party {
		if m.Online && !m.Dead {
			party[m.GUID] = true
		}
	}
	var onMe *objects.Unit
	for i := range c.Units { // nearest first
		u := &c.Units[i]
		if !hostileMob(u) || !u.InCombat || u.Distance < 0 || u.Distance > tankPickupRange {
			continue
		}
		switch {
		case u.Target == c.Self.GUID:
			if onMe == nil {
				onMe = u
			}
		case party[u.Target]:
			return u, "loose"
		}
	}
	if onMe != nil {
		return onMe, "self"
	}
	if sel, ok := c.Unit(c.G.Selected()); ok && hostileMob(sel) && sel.InCombat {
		return sel, "selected"
	}
	return nil, ""
}

// roleTank is the tank's discipline: hold the mob (the fight is chosen by tankMob, see ValFight), and answer a loose add with the
// class's taunt. Where to stand is melee_position's business (it faces the mob away from the group), the abilities are the class's
// (warrior_tank).
func roleTank() *Strategy {
	return &Strategy{
		Name: "role_tank",
		Role: RoleTank,
		Actions: []*Action{
			faceTargetAction(),
			{Name: "taunt", Harmful: true, Threat: ThreatSingle, Target: fightTarget, Pre: []string{"face_target"},
				Resolve: func(c *Ctx) (uint32, bool) {
					name := c.Class().Taunt
					if name == "" {
						return 0, false
					}
					return c.Spells.Highest(name)
				},
				Possible: func(c *Ctx) bool {
					f := ValFight.Get(c)
					if f.Mob == nil || f.Source != "loose" {
						return false
					}
					// A warrior's Taunt works in Defensive Stance only.
					return c.Class().ID != ClassWarrior || InStance(&c.Self, warriorStances["Taunt"]...)
				}},
		},
		Rules: []Rule{
			{Trigger: Trigger{Name: "loose_add", Active: func(c *Ctx) bool { return ValFight.Get(c).Source == "loose" }},
				Do: []Next{{"taunt", RelHigh + 5}}},
		},
	}
}

// ---- role_healer ----

// roleHealer is the healer's place in the strategy list, so that the role is named and asked for; it refuses to run (healers come
// with step 4).
func roleHealer() *Strategy {
	return &Strategy{Name: "role_healer", Role: RoleHealer, NotImplemented: healerNotImplemented}
}
