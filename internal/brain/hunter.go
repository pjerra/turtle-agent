package brain

import (
	"context"
	"errors"
	"time"

	"github.com/pjerra/turtle-agent/internal/objects"
	"github.com/pjerra/turtle-agent/internal/packet"
)

// The hunter (issue #80 step 4b, preset marksman_hunter): a ranged damage dealer that shoots from 8 to 35 yards. The rotation is the
// ideas of mod-playerbots' hunter behaviour (GPL-2.0+; ideas and numbers only), the spells are checked against the core's spell data
// in TestHunterSpellData. No hunter spell has a stance or form in the core's spell_template.
//
//   - Auto Shot is an auto-repeat spell (spell 75), started with an ordinary cast at the mob like the mage's wand (Shoot) and left
//     running. It is not sent again while it runs: a second start is ignored by the server but restarts the ranged timer at 500 ms
//     (Unit.cpp m_AutoRepeatFirstCast), so each repeat would cost a shot's timing. The brain takes it as running while it saw a shot
//     (SMSG_SPELL_GO of spell 75 in the spell state) within autoShotLive, and sends it again after a walk, a refusal, a feint, a new
//     mob, or when no shot has been seen for that long. A shot has a minimum range of 8 yards (the dead zone) and a maximum of 35.
//   - Hunter's Mark, Serpent Sting (a mob with life left), Arcane Shot on cooldown and Multi-Shot with two or more mobs at the target are
//     spent only above hunterShotsManaPct of the mana; Auto Shot costs none. Multi-Shot is an area ability: the damage dealer's veto
//     (combo_dps) and a veto of its own (hits mobs round the target, not round the player) hold it back while a loose mob is near.
//   - Aspects: Aspect of the Hawk is the home aspect. Aspect of the Cheetah (and the Pack) is never cast and is shaken off in combat (it
//     dazes on every hit). Aspect of the Viper, when the spellbook has it (Turtle has it from level 56), takes over below
//     viperOnPct of the mana in combat and gives way to the Hawk from viperOffPct; without it the hunter drinks out of combat (rest_drink).
//   - A mob that runs at the hunter gets a Concussive Shot; with a tank in view and a mob on the hunter, Feign Death (the shots wait
//     feignWait, since any shot ends it). Without a tank, or with Feign Death not ready, a mob in melee is fought with the swing and
//     Raptor Strike.
//   - The dead zone is kept: hunter_position steps back to 12 to 29 yards from the fight's mob when it stands within 9, out of its
//     front, with sight of it.
//
// Not in this step: the pet. The agent has no pet control (see README "Roles and classes"): it does not know the pet's guid, health or
// happiness and has no CMSG_PET_ACTION call, so Call Pet, Revive Pet, Mend Pet, Feed Pet and `pet attack` are not driven; a hunter that
// is asked for the preset plays without them. The ammo in the quiver is not in the backpack list either: the brain reacts to the
// server's refusal (no_ammo, need_ammo) by holding the shots back for 30 s, and the decision log says so.

const (
	spellAutoShot uint32 = 75 // Auto Shot (the other spell of that name, 52636, is a test spell)

	hunterDeadZone   = 8.0  // shots reach no nearer than this
	hunterFrontMax   = 14.0 // within this a mob's front (cleave, breath) is kept out of
	hunterMeleeReach = 6.0  // a mob on the hunter this near is fought with the swing and Raptor Strike

	hunterShotsManaPct = 20 // Serpent Sting, Arcane Shot and Multi-Shot from this much mana
	markMinMobPct      = 50 // Hunter's Mark on a mob with at least this health
	stingMinMobPct     = 40 // Serpent Sting too
	viperOnPct         = 20 // Aspect of the Viper below this mana, in combat
	viperOffPct        = 60 // and back to the Hawk from here
	feignRange         = 15.0
	feignWait          = 6 * time.Second
	autoShotLive       = 6 * time.Second // a shot seen this recently: Auto Shot runs (the slowest bows shoot every 3.5 s)
	raptorHold         = 1500 * time.Millisecond
	multiShotRadius    = 10.0 // Multi-Shot hits mobs this near the target
	multiShotLooseRad  = 12.0 // and a mob this near the target that the party has not engaged vetoes it

	keyAutoShot = "autoshot_on" // action-hold keys of the hunter (not actions): who Auto Shot was last ordered at, and the feign wait
	keyFeign    = "feign_wait"
)

func onHunter(c *Ctx) bool { return onPower(c, powerMana) && c.Self.Class == ClassHunter }

// autoShotSpell is Auto Shot: spell 75 when the spellbook has it, else the highest of that name.
func autoShotSpell(c *Ctx) (uint32, bool) {
	if c.Spells.Known(spellAutoShot) {
		return spellAutoShot, true
	}
	return c.Spells.Highest("Auto Shot")
}

// shootingAt says Auto Shot was ordered at the mob a moment ago and is taken to run.
func shootingAt(c *Ctx, m *objects.Unit) bool {
	who, held := heldKey(c, keyAutoShot)
	return held && m != nil && who == guidStr(m.GUID)
}

// feigning says the hunter has just feigned death: its shots wait, since any of them ends the feint.
func feigning(c *Ctx) bool { _, held := heldKey(c, keyFeign); return held }

// hunterManaOK says the mana allows a shot that costs some.
func hunterManaOK(c *Ctx) bool { return PowerPct(c) >= hunterShotsManaPct }

// incomingMob is the mob that has picked the hunter and is running at it from out of the dead zone.
func incomingMob(c *Ctx) *objects.Unit {
	m := ValMobOnMe.Get(c)
	if m == nil || !m.Moving || m.Distance < hunterDeadZone+1 || m.Distance > 30 || HasAura(m, "Concussive Shot") {
		return nil
	}
	return m
}

func incomingMobGUID(c *Ctx) packet.GUID {
	if m := incomingMob(c); m != nil {
		return m.GUID
	}
	return 0
}

// hunterMelee is the mob on the hunter that is within the melee reach, nil when there is none.
func hunterMelee(c *Ctx) *objects.Unit {
	if feigning(c) {
		return nil // a swing would end the feint
	}
	if m := ValMeleeMob.Get(c); m != nil && m.Distance <= hunterMeleeReach {
		return m
	}
	return nil
}

// ---- aspects ----

var hunterAspects = []string{"Aspect of the Hawk", "Aspect of the Viper", "Aspect of the Cheetah", "Aspect of the Pack", "Aspect of the Monkey",
	"Aspect of the Wild", "Aspect of the Beast"}

const (
	aspectHawk   = "Aspect of the Hawk"
	aspectViper  = "Aspect of the Viper"
	aspectCheeta = "Aspect of the Cheetah"
	aspectPack   = "Aspect of the Pack"
)

// wantAspect is the aspect the hunter should cast now, "" for none: the Hawk when it has no aspect, the Viper when the mana is low in
// combat (and the spellbook has it), the Hawk again once the mana is back, the Hawk in place of a running aspect in combat. An aspect
// the brain does not manage (Monkey, Wild, Beast) is left alone, and so is a Cheetah or Pack out of combat.
func wantAspect(c *Ctx) string {
	cur := seenAura(&c.Self, hunterAspects...)
	mana := PowerPct(c)
	viper := knows(c, aspectViper) && c.Self.InCombat && mana < viperOnPct
	switch cur {
	case "":
		if viper {
			return aspectViper
		}
		if knows(c, aspectHawk) {
			return aspectHawk
		}
	case aspectHawk:
		if viper {
			return aspectViper
		}
	case aspectViper:
		if mana >= viperOffPct && knows(c, aspectHawk) {
			return aspectHawk
		}
	case aspectCheeta, aspectPack:
		if c.Self.InCombat && knows(c, aspectHawk) {
			return aspectHawk
		}
	}
	return ""
}

// ---- the rotation ----

// shot is a ranged attack at the fight's mob: it waits for the opening delay (Harmful), faces the mob, and is not made while feigning.
func shot(name, spell string, threat ThreatKind, possible func(*Ctx) bool) *Action {
	return &Action{Name: name, Spell: spell, Harmful: true, Threat: threat, Target: fightTarget, Pre: []string{"face_target"},
		Possible: func(c *Ctx) bool { return onHunter(c) && !feigning(c) && (possible == nil || possible(c)) }}
}

func hunterMarksman() *Strategy {
	mobPct := func(c *Ctx) int { return mobHealthPct(fightMob(c)) }
	return &Strategy{
		Name: "hunter_marksman",
		// Auto Shot is taken as running while shots are seen; a refusal of it ends that at once (the spell is held back by the refusal).
		Before: func(c *Ctx) {
			cs := c.Spells.CastState()
			if f := cs.LastFailure; f != nil && f.SpellID == spellAutoShot && f.AgoMS < 1000 {
				setKey(c, keyAutoShot, 0, "")
				return
			}
			if l := cs.LastCast; l != nil && l.SpellID == spellAutoShot && l.AgoMS < 1500 {
				if who, held := heldKey(c, keyAutoShot); held {
					setKey(c, keyAutoShot, autoShotLive, who)
				}
			}
		},
		Mult: []Multiplier{{Name: "multishot_pulls_loose_mob", F: func(c *Ctx, a *Action, rel float32) float32 {
			// Multi-Shot hits the mobs round its target, which for a ranged class are far from the player: the damage dealer's veto
			// measures from the player, this one from the target. A mob there that the party has not engaged would be pulled.
			if a.Name != "multi_shot" {
				return 1
			}
			t := fightMob(c)
			tank := ValFight.Get(c).Tank
			if t == nil {
				return 1
			}
			for i := range c.Units {
				u := &c.Units[i]
				if !hostileMob(u) || u.GUID == t.GUID || dist2(unitPos(u), unitPos(t)) > multiShotLooseRad {
					continue
				}
				if u.InCombat && (u.Target == c.Self.GUID || (tank != nil && u.Target == tank.GUID)) {
					continue
				}
				return 0
			}
			return 1
		}}},
		Actions: []*Action{
			{Name: "auto_shot", Harmful: true, Threat: ThreatSingle, Target: fightTarget, Pre: []string{"face_target"}, Resolve: autoShotSpell,
				Possible: func(c *Ctx) bool { return onHunter(c) && !feigning(c) },
				Useful:   func(c *Ctx) bool { return !shootingAt(c, fightMob(c)) },
				Do: func(ctx context.Context, c *Ctx) error {
					id, ok := autoShotSpell(c)
					m := fightMob(c)
					if !ok || m == nil {
						return nil
					}
					setKey(c, keyAutoShot, autoShotLive, guidStr(m.GUID))
					return c.cast(ctx, id, m.GUID)
				}},
			shot("hunters_mark", "Hunter's Mark", ThreatNone, func(c *Ctx) bool {
				m := fightMob(c)
				return m != nil && !HasAura(m, "Hunter's Mark") && mobPct(c) >= markMinMobPct
			}),
			shot("serpent_sting", "Serpent Sting", ThreatSingle, func(c *Ctx) bool {
				m := fightMob(c)
				return m != nil && !HasAura(m, "Serpent Sting") && mobPct(c) >= stingMinMobPct && hunterManaOK(c)
			}),
			shot("arcane_shot", "Arcane Shot", ThreatSingle, hunterManaOK),
			shot("multi_shot", "Multi-Shot", ThreatAoE, func(c *Ctx) bool {
				return hunterManaOK(c) && mobsNear(c, fightMob(c), multiShotRadius) >= 2
			}),
			{Name: "concussive_shot", Spell: "Concussive Shot", Harmful: true, Threat: ThreatSingle, Target: incomingMobGUID, Pre: []string{"face_target"},
				Possible: func(c *Ctx) bool { return incomingMob(c) != nil }},
			{Name: "feign_death", Spell: "Feign Death",
				Possible: func(c *Ctx) bool {
					m := ValMobOnMe.Get(c)
					return m != nil && m.Distance >= 0 && m.Distance <= feignRange && tankInView(c) != nil
				},
				Useful: func(c *Ctx) bool { return !HasAura(&c.Self, "Feign Death") },
				Do: func(ctx context.Context, c *Ctx) error {
					id, ok := c.Spells.Highest("Feign Death")
					if !ok {
						return nil
					}
					// The server keeps the auto-repeat spell through the feint, and the next shot would end it: switch it off first (as a
					// client does when Auto Shot is toggled off). It starts again, with a new send, when the shots may go on.
					if cc, ok := c.G.(AutoRepeatCanceller); ok {
						if err := cc.CancelAutoRepeat(ctx); err != nil {
							return err
						}
					}
					setKey(c, keyFeign, feignWait, "feigned")
					setKey(c, keyAutoShot, 0, "")
					return c.cast(ctx, id, c.Self.GUID)
				}},
			swingAction("hunter_swing", hunterMelee),
			{Name: "raptor_strike", Spell: "Raptor Strike", Harmful: true, Threat: ThreatSingle, Pre: []string{"face_target"},
				Target: func(c *Ctx) packet.GUID {
					if m := hunterMelee(c); m != nil {
						return m.GUID
					}
					return 0
				},
				Possible: func(c *Ctx) bool { return hunterMelee(c) != nil },
				Do: func(ctx context.Context, c *Ctx) error {
					id, ok := c.Spells.Highest("Raptor Strike")
					m := hunterMelee(c)
					if !ok || m == nil {
						return nil
					}
					c.mem.holdAction("raptor_strike", c.Now.Add(raptorHold), "queued") // a next-swing ability: queued until the swing
					return c.cast(ctx, id, m.GUID)
				}},
			{Name: "aspect_hawk", Spell: aspectHawk, Possible: func(c *Ctx) bool { return onHunter(c) && wantAspect(c) == aspectHawk && aspectUpkeepOK(c) }},
			{Name: "aspect_viper", Spell: aspectViper, Possible: func(c *Ctx) bool { return onHunter(c) && wantAspect(c) == aspectViper }},
		},
		Rules: []Rule{
			{Trigger: Trigger{Name: "fight_target_ranged", Active: func(c *Ctx) bool { return onHunter(c) && fightMob(c) != nil }},
				Do: []Next{{"auto_shot", RelNormal + 9}, {"hunters_mark", RelNormal + 8}, {"serpent_sting", RelNormal + 6}, {"arcane_shot", RelNormal + 5}}},
			{Trigger: Trigger{Name: "mobs_round_target", Active: func(c *Ctx) bool {
				return onHunter(c) && mobsNear(c, fightMob(c), multiShotRadius) >= 2
			}}, Do: []Next{{"multi_shot", RelNormal + 7}}},
			{Trigger: Trigger{Name: "mob_running_at_hunter", Active: func(c *Ctx) bool { return onHunter(c) && incomingMob(c) != nil }},
				Do: []Next{{"concussive_shot", RelMove + 2}}},
			{Trigger: Trigger{Name: "mob_on_hunter_with_tank", Active: func(c *Ctx) bool {
				m := ValMobOnMe.Get(c)
				return onHunter(c) && m != nil && m.Distance >= 0 && m.Distance <= feignRange && tankInView(c) != nil
			}}, Do: []Next{{"feign_death", relEmergency - 2}}},
			{Trigger: Trigger{Name: "mob_in_melee_of_hunter", Active: func(c *Ctx) bool { return onHunter(c) && hunterMelee(c) != nil }},
				Do: []Next{{"hunter_swing", RelNormal + 9}, {"raptor_strike", RelNormal + 6}}},
			{Trigger: Trigger{Name: "low_mana_in_combat", Active: func(c *Ctx) bool { return onHunter(c) && wantAspect(c) == aspectViper }},
				Do: []Next{{"aspect_viper", RelHigh}}},
			{Trigger: Trigger{Name: "aspect_missing", Active: func(c *Ctx) bool { return onHunter(c) && wantAspect(c) == aspectHawk && aspectUpkeepOK(c) }},
				Do: []Next{{"aspect_hawk", RelNormal + 2}}},
		},
	}
}

// aspectUpkeepOK says the home aspect may be cast now: in combat always (the Cheetah is shaken off at once), out of combat only where
// upkeep is allowed.
func aspectUpkeepOK(c *Ctx) bool { return c.Self.InCombat || restNow(c) }

// ---- position ----

// hunterIssue says what is wrong with where the hunter stands for the fight's mob, "" when nothing is: no sight of it, inside the dead
// zone, too far for the shots, or in its front within a few yards. A mob that has picked the hunter is the aggro response's business
// (Concussive Shot, Feign Death, the run to the tank, the melee kit).
func hunterIssue(c *Ctx) string {
	m := fightMob(c)
	if m == nil || !m.InCombat || m.Distance < 0 || ValMobOnMe.Get(c) != nil {
		return ""
	}
	if clear, known := c.LineOfSight(m); known && !clear {
		return issueNoLOS
	}
	lo, hi := rangedBand(c) // the class row's band: 10 to 30 yd, so the hunter steps back within 9 and forward past 31 (the shots reach 35)
	switch d := float64(m.Distance2D); {
	case d < lo-bandSlack:
		return issueTooClose
	case d > hi+bandSlack:
		return issueTooFar
	case d <= hunterFrontMax && inFront(m, selfPos(c)):
		return issueFrontal
	}
	return ""
}

// hunterPosition keeps the hunter within the class row's band (10 to 30 yd, rangedBand) of the fight's mob with sight of it, out of its front, and brings a
// mob that has picked the hunter toward the tank. It is all movement, so it is off when the game cannot walk or see the mesh, when the
// run switched it off, and while the dungeon driver holds the move lock.
func hunterPosition() *Strategy {
	spot := func(c *Ctx, m *objects.Unit) (Vec3, bool) { return bandSpot(c, "hunter", m) }
	return &Strategy{
		Name: "hunter_position",
		Actions: []*Action{
			{Name: "hunter_reposition", Settle: 700 * time.Millisecond,
				Possible: func(c *Ctx) bool {
					m := fightMob(c)
					if m == nil || !canMove(c) {
						return false
					}
					_, ok := spot(c, m)
					return ok
				},
				Useful: func(c *Ctx) bool { return hunterIssue(c) != "" },
				Do: func(ctx context.Context, c *Ctx) error {
					m := fightMob(c)
					if m == nil {
						return nil
					}
					p, ok := spot(c, m)
					if !ok {
						return errors.New("no spot")
					}
					forgetSpot(c)
					setKey(c, keyAutoShot, 0, "") // a walk ends the shots: start them again at the new place
					return c.walk(ctx, p)
				}},
			{Name: "run_to_tank", Settle: 300 * time.Millisecond,
				Possible: func(c *Ctx) bool { return canMove(c) && runToTankPoint(c) != nil },
				Do: func(ctx context.Context, c *Ctx) error {
					p := runToTankPoint(c)
					if p == nil {
						return nil
					}
					setKey(c, keyAutoShot, 0, "")
					return c.walk(ctx, *p)
				}},
		},
		Rules: []Rule{
			{Trigger: Trigger{Name: "bad_hunter_position", Active: func(c *Ctx) bool { return canMove(c) && hunterIssue(c) != "" }},
				Do: []Next{{"hunter_reposition", RelMove}}},
			{Trigger: Trigger{Name: "mob_on_me_far", Active: func(c *Ctx) bool { return canMove(c) && runToTankPoint(c) != nil }},
				Do: []Next{{"run_to_tank", RelMove}}},
		},
	}
}

func init() {
	strategyBuilders["hunter_marksman"] = hunterMarksman
	strategyBuilders["hunter_position"] = hunterPosition
	presets["marksman_hunter"] = []string{"combat_dps", "hunter_marksman", "hunter_position", "avoid_ground", "combat_consumables", "rest_drink", "rest_eat"}
}
