package brain

import (
	"context"
	"time"

	"github.com/pjerra/turtle-agent/internal/objects"
	"github.com/pjerra/turtle-agent/internal/spells"
)

// The warrior, the second class of the brain (issue #80 step 3c): Arms and Fury as damage dealers, a basic tank. The rotation is
// the harvest of mod-playerbots' warrior behaviour (docs: PLAYERBOTS-HARVEST, Warrior table; ideas and numbers only, GPL-2.0+),
// with the spell names, levels, costs and ranges checked against the core's spell data (internal/spells). Spells are named, and the
// brain casts the highest known rank, as for the mage. Rage and rage costs are in tenths (50 rage is 500), as the update fields and
// the spell data have them.
//
// Stances. A stance change costs a warrior its rage (without the Tactical Mastery talent), so the brain keeps a home stance and
// leaves it for nothing: Arms stays in Battle Stance, Fury in Berserker Stance (Battle below level 30, which is when the stance
// is learned), the tank in Defensive Stance. The abilities each name the stances they work in, and an ability the stance does not
// allow is simply not possible: Fury has no Overpower and Rend, Arms has no Pummel (Shield Bash if it carries a shield), and neither
// dances for a gap closer (Arms Charges out of combat from Battle Stance, Fury Intercepts in combat from Berserker Stance).

// The stance spells. They are listed by id: the spellbook also holds the stance's passive spells under the same names, and "the
// highest known rank by name" would find one of those (Battle Stance 7165, Berserker Stance 7366).
const (
	spellBattleStance    uint32 = 2457
	spellDefensiveStance uint32 = 71
	spellBerserkerStance uint32 = 2458
)

var stanceSpells = map[string]uint32{stanceBattle: spellBattleStance, stanceDefensive: spellDefensiveStance, stanceBerserker: spellBerserkerStance}

// Thresholds, in tenths of rage and in percent.
const (
	rageDump       = 500 // Heroic Strike and Cleave from here (mod-playerbots: rage >= 50 on next swing)
	rageBloodrage  = 200 // Bloodrage below this at the start of a fight
	rageStanceKeep = 250 // below this a stance change in combat is cheap enough
	bloodrageHP    = 50  // and only above this health
	executePct     = 20  // Execute below this health of the mob
	hamstringPct   = 25  // Hamstring a mob at or below this health that is running
	rendMinPct     = 35  // Rend a mob above this health: it must live to see the bleed
	sunderStacks   = 5   // Sunder Armor up to this many stacks
	hsHold         = 1500 * time.Millisecond
	dodgeWindow    = 4500 * time.Millisecond // Overpower after a dodge, a little under the five seconds the server allows
	avoidedWindow  = 3500 * time.Millisecond // Revenge after the player dodged, parried or blocked
)

// ---- helpers ----

// InStance reports whether the unit carries one of the stance auras.
func InStance(u *objects.Unit, names ...string) bool { return HasAura(u, names...) }

// stanceSpell is the stance's spell id when the player knows it.
func stanceSpell(c *Ctx, name string) (uint32, bool) {
	id, ok := stanceSpells[name]
	if !ok || !c.Spells.Known(id) {
		return 0, false
	}
	return id, true
}

// rageNow is the rage the player has, in tenths (0 for a player that plays on another power).
func rageNow(c *Ctx) uint32 {
	if c.Self.PowerType != powerRage {
		return 0
	}
	return c.Self.Power
}

// stanceChangeOK says changing stance now costs little: out of combat, or with little rage to lose.
func stanceChangeOK(c *Ctx) bool { return !c.Self.InCombat || rageNow(c) <= rageStanceKeep }

// dodgedRecently reports whether the fight's mob dodged an attack of the player's a moment ago (Overpower's window).
func dodgedRecently(c *Ctx) bool {
	m, ml := fightMob(c), meleeOf(c)
	if m == nil || ml == nil {
		return false
	}
	t := ml.LastDodge(m.GUID)
	return !t.IsZero() && c.Now.Sub(t) >= 0 && c.Now.Sub(t) < dodgeWindow
}

// avoidedRecently reports whether the player dodged, parried or blocked an attack a moment ago (Revenge's window).
func avoidedRecently(c *Ctx) bool {
	ml := meleeOf(c)
	if ml == nil {
		return false
	}
	t := ml.LastAvoided()
	return !t.IsZero() && c.Now.Sub(t) >= 0 && c.Now.Sub(t) < avoidedWindow
}

// feared says the player carries a fear or a horror (the mechanic of the aura's spell).
func feared(c *Ctx) bool {
	for _, a := range c.Self.Auras {
		if !a.Harmful {
			continue
		}
		if m, ok := spells.Lookup(a.SpellID); ok && (m.Mechanic == 5 || m.Mechanic == 24) {
			return true
		}
	}
	return false
}

// fleeing is the stand-in for "the mob runs away at low health": a mob at low health that moves while within reach of a hamstring.
func fleeing(m *objects.Unit) bool {
	return m != nil && m.Moving && mobHealthPct(m) <= hamstringPct && m.Distance >= 0 && m.Distance <= 7 && !HasAura(m, "Hamstring")
}

// sunderStacksOn is how many Sunder Armor stacks the mob carries.
func sunderStacksOn(m *objects.Unit) int {
	if m == nil {
		return 0
	}
	for _, a := range m.Auras {
		if a.Name == "Sunder Armor" {
			return a.Stacks
		}
	}
	return 0
}

// mobsLacking counts the mobs in combat within reach that lack the aura (Thunder Clap and Demoralizing Shout want one that does).
func mobsLacking(c *Ctx, aura string) int {
	n := 0
	for i := range c.Units {
		u := &c.Units[i]
		if hostileMob(u) && u.InCombat && u.Distance >= 0 && float64(u.Distance2D) <= meleeReach && !HasAura(u, aura) {
			n++
		}
	}
	return n
}

// meleeAction is a melee ability aimed at the fight's mob, in the stances given (none: any) and when possible says so.
func meleeAction(name, spell string, threat ThreatKind, stances []string, possible func(*Ctx) bool) *Action {
	return &Action{Name: name, Spell: spell, Harmful: true, Threat: threat, Target: fightTarget, Pre: []string{"face_target"},
		Possible: func(c *Ctx) bool {
			if len(stances) > 0 && !InStance(&c.Self, stances...) {
				return false
			}
			return possible == nil || possible(c)
		}}
}

// selfAction is an ability that works on the player itself (a shout, a stance move, a rage gain).
func selfAction(name, spell string, stances []string, possible, useful func(*Ctx) bool) *Action {
	return &Action{Name: name, Spell: spell, Possible: func(c *Ctx) bool {
		if len(stances) > 0 && !InStance(&c.Self, stances...) {
			return false
		}
		return possible == nil || possible(c)
	}, Useful: useful}
}

// homeStanceAction returns the player to its stance: the first of the stances (in the order given) that the spellbook has, when
// the player is not in it and the change costs little.
func homeStanceAction(name string, homes ...string) *Action {
	pick := func(c *Ctx) string {
		for _, h := range homes {
			if _, ok := stanceSpell(c, h); ok {
				return h
			}
		}
		return ""
	}
	return &Action{Name: name,
		Resolve: func(c *Ctx) (uint32, bool) { return stanceSpell(c, pick(c)) },
		Possible: func(c *Ctx) bool {
			h := pick(c)
			if h == "" || InStance(&c.Self, h) || !stanceChangeOK(c) {
				return false
			}
			c.mem.mel.mu.Lock()
			defer c.mem.mel.mu.Unlock()
			return c.Now.Sub(c.mem.mel.stanceAt) >= 2*time.Second // the stance cooldown is a second; a refusal must not loop
		},
		Do: func(ctx context.Context, c *Ctx) error {
			id, ok := stanceSpell(c, pick(c))
			if !ok {
				return nil
			}
			c.mem.mel.mu.Lock()
			c.mem.mel.stanceAt = c.Now
			c.mem.mel.mu.Unlock()
			return c.G.Cast(ctx, id, c.Self.GUID)
		}}
}

func onWarrior(c *Ctx) bool { return c.SelfOK && c.Self.PowerType == powerRage }

// offHome says the player is not in its home stance (the first of the stances that its spellbook has) and a change would cost little.
func offHome(homes ...string) func(*Ctx) bool {
	return func(c *Ctx) bool {
		if !onWarrior(c) {
			return false
		}
		for _, h := range homes {
			if _, ok := stanceSpell(c, h); ok {
				return !InStance(&c.Self, h) && stanceChangeOK(c)
			}
		}
		return false
	}
}

func mobInReach(c *Ctx) bool {
	m := fightMob(c)
	return m != nil && m.Distance >= 0 && float64(m.Distance2D) <= meleeReach
}

func mobGap(c *Ctx) bool {
	m := fightMob(c)
	return m != nil && m.Distance >= 8 && m.Distance <= 25
}

// ---- warrior_melee: what every warrior does ----

// warriorMelee is the swing and the rage: auto attack, Execute, Heroic Strike or Cleave as the rage dump, Bloodrage, Battle Shout,
// Hamstring on a runner, and Charge out of combat. The specs add their own abilities on top (warrior_arms, warrior_fury,
// warrior_tank).
func warriorMelee() *Strategy {
	return &Strategy{
		Name: "warrior_melee",
		Mult: []Multiplier{{Name: "gap_closer_needs_tank", F: func(c *Ctx, a *Action, rel float32) float32 {
			// No Charge or Intercept into a pull the tank has not engaged (mod-playerbots: the leader waits for the tank).
			if (a.Name != "charge" && a.Name != "intercept") || c.P.Role == RoleTank {
				return 1
			}
			if ValFight.Get(c).Tank == nil {
				return 0
			}
			return 1
		}}},
		Actions: []*Action{
			{Name: "auto_attack", Harmful: true, Threat: ThreatSingle, Target: fightTarget, Pre: []string{"face_target"},
				Possible: func(c *Ctx) bool { return mobInReach(c) },
				Useful:   func(c *Ctx) bool { return !swingingAt(c, fightMob(c)) },
				Do: func(ctx context.Context, c *Ctx) error {
					m := fightMob(c)
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
				}},
			meleeAction("execute", "Execute", ThreatSingle, []string{stanceBattle, stanceBerserker}, func(c *Ctx) bool {
				return mobHealthPct(fightMob(c)) < executePct
			}),
			meleeAction("hamstring", "Hamstring", ThreatSingle, []string{stanceBattle, stanceBerserker}, func(c *Ctx) bool { return fleeing(fightMob(c)) }),
			nextSwing("heroic_strike", "Heroic Strike", ThreatSingle),
			nextSwing("cleave", "Cleave", ThreatAoE),
			selfAction("battle_shout", "Battle Shout", nil, nil, func(c *Ctx) bool { return auraMissing(&c.Self, "Battle Shout") }),
			selfAction("bloodrage", "Bloodrage", nil, nil, func(c *Ctx) bool { return !HasAura(&c.Self, "Bloodrage") }),
			meleeAction("charge", "Charge", ThreatSingle, []string{stanceBattle}, func(c *Ctx) bool { return !c.Self.InCombat }),
		},
		Rules: []Rule{
			{Trigger: Trigger{Name: "mob_in_reach", Active: mobInReach}, Do: []Next{{"auto_attack", RelNormal + 9}}},
			{Trigger: Trigger{Name: "mob_low", Active: func(c *Ctx) bool { return mobInReach(c) && mobHealthPct(fightMob(c)) < executePct }},
				Do: []Next{{"execute", RelNormal + 8}}},
			{Trigger: Trigger{Name: "mob_fleeing", Active: func(c *Ctx) bool { return fleeing(fightMob(c)) }}, Do: []Next{{"hamstring", RelNormal + 2}}},
			{Trigger: Trigger{Name: "rage_dump", Active: func(c *Ctx) bool { return mobInReach(c) && rageNow(c) >= rageDump }},
				Do: []Next{{"heroic_strike", RelNormal}}},
			{Trigger: Trigger{Name: "rage_dump_pack", Active: func(c *Ctx) bool {
				return mobInReach(c) && rageNow(c) >= rageDump && ValMeleeMobs.Get(c) >= 2
			}}, Do: []Next{{"cleave", RelNormal + 1}}},
			{Trigger: Trigger{Name: "shout_missing_ooc", Active: func(c *Ctx) bool {
				return c.RestOK && onWarrior(c) && !c.Self.InCombat && ValFight.Get(c).Mob == nil && auraMissing(&c.Self, "Battle Shout")
			}}, Do: []Next{{"battle_shout", RelNormal + 2}}},
			{Trigger: Trigger{Name: "shout_missing_in_combat", Active: func(c *Ctx) bool {
				return onWarrior(c) && c.Self.InCombat && auraMissing(&c.Self, "Battle Shout")
			}}, Do: []Next{{"battle_shout", RelNormal - 2}}},
			{Trigger: Trigger{Name: "fight_start_low_rage", Active: func(c *Ctx) bool {
				m := fightMob(c)
				return onWarrior(c) && m != nil && m.Distance >= 0 && m.Distance <= 12 && rageNow(c) < rageBloodrage && healthPct(c) > bloodrageHP
			}}, Do: []Next{{"bloodrage", RelNormal + 3}}},
			{Trigger: Trigger{Name: "mob_far_ooc", Active: func(c *Ctx) bool {
				return onWarrior(c) && !c.Self.InCombat && InStance(&c.Self, stanceBattle) && mobGap(c)
			}}, Do: []Next{{"charge", RelMove + 2}}},
		},
	}
}

// nextSwing is Heroic Strike or Cleave: an ability that replaces the next auto attack. It is sent once per swing: the server queues it
// and the rage is spent at the swing, so the rage does not drop while it waits and the same order would go out again.
func nextSwing(name, spell string, threat ThreatKind) *Action {
	a := meleeAction(name, spell, threat, nil, nil)
	a.Do = func(ctx context.Context, c *Ctx) error {
		id, ok := c.Spells.Highest(spell)
		if !ok {
			return nil
		}
		c.mem.holdAction(name, c.Now.Add(hsHold), "queued")
		// Heroic Strike and Cleave replace each other on the queue: the one just sent holds the other back for the same time.
		other := "cleave"
		if name == "cleave" {
			other = "heroic_strike"
		}
		c.mem.holdAction(other, c.Now.Add(hsHold), "queued")
		return c.G.Cast(ctx, id, fightTarget(c))
	}
	return a
}

// swingingAt says the player's auto attack is on the mob. A game that knows (Melee) is asked; otherwise the brain remembers its last
// attack order, and repeats it every swingRefresh.
func swingingAt(c *Ctx, m *objects.Unit) bool {
	if m == nil {
		return true
	}
	if ml := meleeOf(c); ml != nil {
		return ml.Swinging() == m.GUID
	}
	c.mem.mel.mu.Lock()
	defer c.mem.mel.mu.Unlock()
	return c.mem.mel.swingTarget == m.GUID && c.Now.Sub(c.mem.mel.swingAt) < swingRefresh
}

// ---- warrior_arms ----

// warriorArms is the Arms rotation in Battle Stance: Execute, Overpower on a dodge, Mortal Strike on cooldown, Rend, Sweeping Strikes
// with two mobs, Heroic Strike with the spare rage (warrior_melee).
func warriorArms() *Strategy {
	return &Strategy{
		Name: "warrior_arms",
		Actions: []*Action{
			meleeAction("mortal_strike", "Mortal Strike", ThreatSingle, nil, nil),
			meleeAction("overpower", "Overpower", ThreatSingle, []string{stanceBattle}, dodgedRecently),
			// Rend is a bleed: the server refuses it on a mob that cannot bleed (undead, elementals, mechanicals) and the refusal backs it
			// off. A 1.12 client sees no creature type.
			meleeAction("rend", "Rend", ThreatSingle, []string{stanceBattle, stanceDefensive}, func(c *Ctx) bool {
				m := fightMob(c)
				return m != nil && !HasAura(m, "Rend") && mobHealthPct(m) > rendMinPct
			}),
			// Sweeping Strikes makes the next hits hit a second mob: an area ability for the veto, a buff on the player.
			{Name: "sweeping_strikes", Spell: "Sweeping Strikes", Harmful: true, Threat: ThreatAoE,
				Possible: func(c *Ctx) bool { return InStance(&c.Self, stanceBattle) && ValMeleeMobs.Get(c) >= 2 },
				Useful:   func(c *Ctx) bool { return !HasAura(&c.Self, "Sweeping Strikes") }},
			homeStanceAction("stance_arms", stanceBattle),
		},
		Rules: []Rule{
			{Trigger: Trigger{Name: "mob_in_reach_arms", Active: mobInReach}, Do: []Next{{"mortal_strike", RelNormal + 6}, {"rend", RelNormal + 4}}},
			{Trigger: Trigger{Name: "mob_dodged", Active: func(c *Ctx) bool { return mobInReach(c) && dodgedRecently(c) }},
				Do: []Next{{"overpower", RelNormal + 7}}},
			{Trigger: Trigger{Name: "pack_in_reach", Active: func(c *Ctx) bool { return mobInReach(c) && ValMeleeMobs.Get(c) >= 2 }},
				Do: []Next{{"sweeping_strikes", RelNormal + 3}}},
			{Trigger: Trigger{Name: "off_battle_stance", Active: offHome(stanceBattle)}, Do: []Next{{"stance_arms", RelNormal + 2}}},
		},
	}
}

// ---- warrior_fury ----

// warriorFury is the Fury rotation in Berserker Stance: Execute, Bloodthirst on cooldown, Whirlwind, Berserker Rage when feared,
// Intercept to close the distance in combat, Heroic Strike with the spare rage. Below level 30 the home stance is Battle Stance
// and the stance-bound abilities wait for it.
func warriorFury() *Strategy {
	return &Strategy{
		Name: "warrior_fury",
		Actions: []*Action{
			meleeAction("bloodthirst", "Bloodthirst", ThreatSingle, nil, nil),
			meleeAction("whirlwind", "Whirlwind", ThreatAoE, []string{stanceBerserker}, func(c *Ctx) bool { return ValMeleeMobs.Get(c) >= 1 }),
			selfAction("berserker_rage", "Berserker Rage", []string{stanceBerserker}, nil, func(c *Ctx) bool { return feared(c) }),
			meleeAction("intercept", "Intercept", ThreatSingle, []string{stanceBerserker}, func(c *Ctx) bool { return c.Self.InCombat }),
			homeStanceAction("stance_fury", stanceBerserker, stanceBattle),
		},
		Rules: []Rule{
			{Trigger: Trigger{Name: "mob_in_reach_fury", Active: mobInReach}, Do: []Next{{"bloodthirst", RelNormal + 6}, {"whirlwind", RelNormal + 5}}},
			{Trigger: Trigger{Name: "feared", Active: func(c *Ctx) bool { return onWarrior(c) && feared(c) }}, Do: []Next{{"berserker_rage", RelDispel - 5}}},
			{Trigger: Trigger{Name: "mob_far_in_combat", Active: func(c *Ctx) bool {
				return onWarrior(c) && c.Self.InCombat && InStance(&c.Self, stanceBerserker) && mobGap(c)
			}}, Do: []Next{{"intercept", RelMove + 2}}},
			{Trigger: Trigger{Name: "off_berserker_stance", Active: offHome(stanceBerserker, stanceBattle)}, Do: []Next{{"stance_fury", RelNormal + 2}}},
		},
	}
}

// ---- warrior_tank ----

// warriorTank is the basic tank kit in Defensive Stance: Sunder Armor on the main mob up to five stacks, Revenge after an avoided
// attack, Shield Block when hurt, Thunder Clap and Demoralizing Shout with two or more mobs on it. The taunt and the mob choice are
// role_tank's, the stance of the facing melee_position's.
func warriorTank() *Strategy {
	return &Strategy{
		Name: "warrior_tank",
		Actions: []*Action{
			meleeAction("sunder", "Sunder Armor", ThreatSingle, nil, func(c *Ctx) bool { return sunderStacksOn(fightMob(c)) < sunderStacks }),
			meleeAction("revenge", "Revenge", ThreatSingle, []string{stanceDefensive}, avoidedRecently),
			selfAction("shield_block", "Shield Block", []string{stanceDefensive}, nil, func(c *Ctx) bool { return !HasAura(&c.Self, "Shield Block") }),
			{Name: "thunder_clap", Spell: "Thunder Clap", Harmful: true, Threat: ThreatAoE,
				Possible: func(c *Ctx) bool {
					return InStance(&c.Self, stanceBattle, stanceDefensive) && ValMeleeMobs.Get(c) >= 2 && mobsLacking(c, "Thunder Clap") > 0
				}},
			{Name: "demoralizing_shout", Spell: "Demoralizing Shout", Harmful: true, Threat: ThreatAoE,
				Possible: func(c *Ctx) bool { return ValMeleeMobs.Get(c) >= 2 && mobsLacking(c, "Demoralizing Shout") > 0 }},
			homeStanceAction("stance_tank", stanceDefensive),
		},
		Rules: []Rule{
			{Trigger: Trigger{Name: "mob_in_reach_tank", Active: mobInReach}, Do: []Next{{"sunder", RelNormal + 4}}},
			{Trigger: Trigger{Name: "attack_avoided", Active: func(c *Ctx) bool { return mobInReach(c) && avoidedRecently(c) }},
				Do: []Next{{"revenge", RelNormal + 6}}},
			{Trigger: Trigger{Name: "mob_on_tank_hurt", Active: func(c *Ctx) bool {
				return onWarrior(c) && ValMobOnMe.Get(c) != nil && healthPct(c) < 90 && mobInReach(c)
			}}, Do: []Next{{"shield_block", RelNormal + 2}}},
			{Trigger: Trigger{Name: "pack_on_tank", Active: func(c *Ctx) bool { return mobInReach(c) && ValMeleeMobs.Get(c) >= 2 }},
				Do: []Next{{"thunder_clap", RelNormal + 5}, {"demoralizing_shout", RelNormal + 3}}},
			{Trigger: Trigger{Name: "off_defensive_stance", Active: offHome(stanceDefensive)}, Do: []Next{{"stance_tank", RelNormal + 2}}},
		},
	}
}

func init() {
	strategyBuilders["warrior_melee"] = warriorMelee
	strategyBuilders["warrior_arms"] = warriorArms
	strategyBuilders["warrior_fury"] = warriorFury
	strategyBuilders["warrior_tank"] = warriorTank
	strategyBuilders["melee_position"] = meleePosition
	strategyBuilders["class_interrupt"] = classInterrupt
	common := []string{"warrior_melee", "melee_position", "avoid_ground", "class_interrupt", "combat_consumables", "rest_eat"}
	presets["arms_warrior"] = append([]string{"combat_dps", "warrior_arms"}, common...)
	presets["fury_warrior"] = append([]string{"combat_dps", "warrior_fury"}, common...)
	presets["tank_warrior"] = append([]string{"role_tank", "warrior_tank"}, common...)
}
