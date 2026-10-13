package brain

import (
	"context"
	"time"
)

// The rogue (issue #80 step 4b, preset combat_rogue): a melee damage dealer that stands behind its mob (melee_position), builds combo
// points with Sinister Strike, keeps Slice and Dice up and finishes with Eviscerate. Energy is whole points (the update fields and
// the spell data have them so; rage is in tenths). The rotation is the ideas of mod-playerbots' rogue behaviour (GPL-2.0+; ideas and
// numbers only), the spells are checked against the core's spell data in TestRogueSpellData. Every ability works in any state: the
// core's spell_template gives none of them a stance or form (the rogue has none), and the ones that need Stealth are not used.
//
// Not in this step: Stealth openers (Cheap Shot, Garrote, Ambush need the rogue stealthed and a walk up to a sleeping mob; the
// dungeon driver or a tank pulls), Backstab (it needs a dagger, which the agent cannot tell from the equipment), and the poisons
// (applying one means choosing a weapon with the item: Game.UseItem takes no target item).

// Thresholds. Combo points are 0-5, energy whole points, health in percent.
const (
	comboMax         = 5  // Eviscerate from here
	comboLowMob      = 4  // and from here when the mob is nearly dead
	finisherMobPct   = 35 // "nearly dead"
	sndMinCombo      = 2  // Slice and Dice from here: 9 s at least
	sndMinMobPct     = 40 // and only on a mob that will live for it
	sndRefreshLeftMS = 1500
	evasionHealthPct = 50 // Evasion below this, with a mob on the rogue in melee
	vanishHealthPct  = 25 // Vanish below this, with a mob on the rogue
	sprintHealthPct  = 30 // Sprint below this, to run to a tank that stands more than pickupFrom away
)

func onRogue(c *Ctx) bool { return onPower(c, powerEnergy) }

// comboOnMob is the combo points the rogue holds on the fight's mob: the points are on one target, and a finisher needs it to be the
// one it is cast at.
func comboOnMob(c *Ctx) int {
	m := fightMob(c)
	if m == nil || c.Self.ComboPoints == 0 || (c.Self.ComboTarget != 0 && c.Self.ComboTarget != m.GUID) {
		return 0
	}
	return int(c.Self.ComboPoints)
}

// finisherDue says Eviscerate is worth its energy: five combo points, or four on a mob that is nearly dead.
func finisherDue(c *Ctx) bool {
	cp := comboOnMob(c)
	return cp >= comboMax || (cp >= comboLowMob && mobHealthPct(fightMob(c)) <= finisherMobPct)
}

// sndDue says Slice and Dice is down (or about to end), the rogue has the points for it and the mob will live for it.
func sndDue(c *Ctx) bool {
	if comboOnMob(c) < sndMinCombo || mobHealthPct(fightMob(c)) < sndMinMobPct {
		return false
	}
	for _, a := range c.Self.Auras {
		if a.Name == "Slice and Dice" {
			return a.RemainingMS != nil && *a.RemainingMS >= 0 && *a.RemainingMS < sndRefreshLeftMS
		}
	}
	return true
}

// rogueFleePoint is where the rogue runs in an emergency: toward a tank that stands too far to have the mob picked up, nil when
// there is none (or the health is not low enough).
func rogueFleePoint(c *Ctx) *Vec3 {
	if healthPct(c) >= sprintHealthPct || ValMobOnMe.Get(c) == nil {
		return nil
	}
	tank := tankInView(c)
	if tank == nil || float64(tank.Distance2D) <= pickupFrom {
		return nil
	}
	p := pointByTank(c, tank)
	return &p
}

func rogueCombat() *Strategy {
	meleeMob := func(c *Ctx) bool { return ValMeleeMob.Get(c) != nil }
	return &Strategy{
		Name: "rogue_combat",
		Actions: []*Action{
			swingAction("auto_attack", fightMob),
			meleeAction("sinister_strike", "Sinister Strike", ThreatSingle, nil, func(c *Ctx) bool { return comboOnMob(c) < comboMax }),
			meleeAction("eviscerate", "Eviscerate", ThreatSingle, nil, finisherDue),
			// Slice and Dice is a buff on the rogue, but the core's spell asks for a selected enemy (effect 1 targets TARGET_UNIT_ENEMY, the combo
			// points sit on that mob) and the aura goes on the caster by the second effect: it is cast at the mob, not at the rogue itself
			// (the server refuses a cast at the own guid with bad_targets).
			{Name: "slice_and_dice", Spell: "Slice and Dice", Target: fightTarget, Useful: sndDue},
			selfAction("evasion", "Evasion", nil, meleeMob, func(c *Ctx) bool { return !HasAura(&c.Self, "Evasion") }),
			// Vanish drops the rogue out of combat and threat (a threat wipe, so the mob goes back to the tank) and leaves it stealthed; the next
			// swing or Sinister Strike breaks the stealth, so there is no opener to follow it up with: it is the emergency's last resort, not a rotation.
			selfAction("vanish", "Vanish", nil, nil, func(c *Ctx) bool { return !HasAura(&c.Self, "Vanish", "Stealth") }),
			selfAction("sprint", "Sprint", nil, nil, func(c *Ctx) bool { return !HasAura(&c.Self, "Sprint") }),
			// Feint takes threat off the rogue: cast at the mob that has the rogue while a tank can take it. Its own cooldown (10 s, the spell's
			// category) spaces it; the server's refusal backs it off.
			{Name: "feint", Spell: "Feint", Harmful: true, Threat: ThreatNone, Target: meleeMobGUID, Pre: []string{"face_target"},
				Possible: func(c *Ctx) bool { return tankInView(c) != nil }},
			{Name: "flee_to_tank", Settle: 500 * time.Millisecond,
				Possible: func(c *Ctx) bool { return canWalk(c) && rogueFleePoint(c) != nil },
				Do: func(ctx context.Context, c *Ctx) error {
					p := rogueFleePoint(c)
					if p == nil {
						return nil
					}
					return c.walk(ctx, *p)
				}},
		},
		Rules: []Rule{
			{Trigger: Trigger{Name: "mob_in_reach_rogue", Active: mobInReach}, Do: []Next{
				{"auto_attack", RelNormal + 9}, {"sinister_strike", RelNormal + 5}}},
			{Trigger: Trigger{Name: "snd_down", Active: func(c *Ctx) bool { return mobInReach(c) && sndDue(c) }}, Do: []Next{{"slice_and_dice", RelNormal + 8}}},
			{Trigger: Trigger{Name: "finisher_due", Active: func(c *Ctx) bool { return mobInReach(c) && finisherDue(c) }}, Do: []Next{{"eviscerate", RelNormal + 7}}},
			{Trigger: Trigger{Name: "mob_on_rogue_with_tank", Active: func(c *Ctx) bool {
				return onRogue(c) && meleeMob(c) && tankInView(c) != nil
			}}, Do: []Next{{"feint", RelHigh}}},
			{Trigger: Trigger{Name: "hurt_in_melee", Active: func(c *Ctx) bool { return onRogue(c) && meleeMob(c) && healthPct(c) < evasionHealthPct }},
				Do: []Next{{"evasion", relEmergency}}},
			{Trigger: Trigger{Name: "critical_mob_on_rogue", Active: func(c *Ctx) bool {
				return onRogue(c) && ValMobOnMe.Get(c) != nil && healthPct(c) < vanishHealthPct
			}}, Do: []Next{{"vanish", relEmergency + 1}}},
			{Trigger: Trigger{Name: "fleeing_to_tank", Active: func(c *Ctx) bool { return onRogue(c) && rogueFleePoint(c) != nil }},
				Do: []Next{{"sprint", relEmergency - 1}, {"flee_to_tank", RelMove + 3}}},
		},
	}
}

func init() {
	strategyBuilders["rogue_combat"] = rogueCombat
	presets["combat_rogue"] = []string{"combat_dps", "rogue_combat", "melee_position", "avoid_ground", "class_interrupt", "combat_consumables", "rest_eat"}
}
