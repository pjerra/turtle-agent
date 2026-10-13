package brain

import (
	"context"
	"slices"
	"strings"
	"time"

	"github.com/pjerra/turtle-agent/internal/objects"
	"github.com/pjerra/turtle-agent/internal/packet"
	"github.com/pjerra/turtle-agent/internal/spells"
)

// Pets (issue #80 step 4d): the hunter's pet and the warlock's demon. The strategy "pet" is shared by both classes; what differs is
// the class's row in petKits (the spells that bring the pet back and look after it).
//
// The game side is the optional PetGame half of the Game (the session's pet state and CMSG_PET_ACTION, cmd/turtle-agent/brain_methods.go);
// a game without it has no pet, and every rule here stays quiet.
//
//   - Attack. The pet is sent at the fight's mob only once the damage dealer's opening delay is over and the mob is held: it attacks the
//     tank (or the player, or the pet), or there is no tank to wait for. A mob that is not in combat is never ordered at, so the pet never
//     pulls. An order is sent once per mob (the pet's own target shows it); a pet that was told to stay is left alone, since an explicit
//     order beats automation.
//   - Recall. `follow` when the fight is over and the pet still has a target, or when the leader is fleeing: moving in combat with the pet
//     a long way from it, or with a mob on it below a quarter of its health.
//   - Defensive. In a group a pet on aggressive is put on defensive (an aggressive pet picks every fight in sight), once per pet. A pet on
//     passive or on defensive is not touched, and a solo player's pet is left as it is.
//   - Taunt. With a tank in the group the pet's taunt (Growl, Torment, Suffering) has its autocast switched off, once per pet and spell: a
//     pet that taunts takes the mob from the tank. Solo it stays on. It is not switched back on.
//   - Looking after it: the hunter's Call Pet (no pet out), Revive Pet (pet dead) and Mend Pet (hurt); the warlock's summon (no demon out),
//     the best of the class's list that the spellbook and the bags allow (a Voidwalker needs a soul shard, the Imp none). Feed Pet is not
//     driven: it needs food by the pet's diet, which the agent cannot tell (README "Pets").

// PetSpell is one spell of the pet's action bar.
type PetSpell struct {
	ID       uint32
	Name     string
	Autocast bool // the button is the autocast one
	OnBar    bool // the spell has a button (the pet can be ordered to cast it)
}

// PetState is the pet as the brain sees it: the session's copy of its unit row and action bar.
type PetState struct {
	GUID              packet.GUID
	Dead              bool
	Health, MaxHealth uint32
	// Happiness is 1 (unhappy), 2 (content) or 3 (happy) for a hunter's pet, 0 for a pet that has none.
	Happiness uint8
	// React is "passive", "defensive" or "aggressive", Command "stay" or "follow"; both are empty until the pet's bar is known.
	React, Command string
	BarKnown       bool
	Enabled        bool // the bar is on (it is off while the player is mounted)
	Target         packet.GUID
	InCombat       bool
	Distance       float32
	Spells         []PetSpell
}

// PetGame is what the brain needs of a game to drive a pet. Pet is false when the player has none. The orders are the pet bar's: attack
// the unit, follow, a reaction ("passive", "defensive", "aggressive") and the autocast of a spell.
type PetGame interface {
	Pet() (PetState, bool)
	PetAttack(ctx context.Context, target packet.GUID) error
	PetFollow(ctx context.Context) error
	PetReact(ctx context.Context, react string) error
	PetAutocast(ctx context.Context, spell uint32, on bool) error
}

// ValPet is the pet this tick, nil when the player has none (or the game cannot drive one).
var ValPet = &Value[*PetState]{Name: "pet", Calc: func(c *Ctx) *PetState {
	pg, ok := c.G.(PetGame)
	if !ok || !petClass(c) {
		return nil
	}
	st, ok := pg.Pet()
	if !ok {
		return nil
	}
	return &st
}}

// petKit is what a class does for its pet.
type petKit struct {
	Summons []string // the spells that bring a pet out, best first
	Revive  string   // the spell that raises a dead pet ("" for none: a demon is gone in 15 s)
	Mend    string   // the channel that heals it ("" for none)
}

var petKits = map[uint8]petKit{
	ClassHunter: {Summons: []string{"Call Pet"}, Revive: "Revive Pet", Mend: "Mend Pet"},
	// The Voidwalker is the default demon: it holds a mob and its health makes it the safest in a group. The Imp needs no shard.
	ClassWarlock: {Summons: []string{"Summon Voidwalker", "Summon Imp"}},
}

// impKit is the warlock's list for a spec that wants the Imp (Firebolt, Blood Pact): a preset's choice, by naming the strategy pet_imp
// in place of pet (kitOf looks at the active strategies).
var impKit = petKit{Summons: []string{"Summon Imp"}}

func petClass(c *Ctx) bool {
	return c.SelfOK && (c.Self.Class == ClassHunter || c.Self.Class == ClassWarlock)
}

func kitOf(c *Ctx) petKit {
	if c.Self.Class == ClassWarlock && c.b != nil && slices.Contains(c.b.names, "pet_imp") {
		return impKit
	}
	return petKits[c.Self.Class]
}

// petUsable is the pet when it is out, alive and can be ordered (its bar is on, or not known yet).
func petUsable(c *Ctx) *PetState {
	p := ValPet.Get(c)
	if p == nil || p.Dead || (p.BarKnown && !p.Enabled) {
		return nil
	}
	return p
}

const (
	petAttackHold   = 2500 * time.Millisecond // an attack order is not repeated for the same mob within this long
	petRecallHold   = 3 * time.Second
	petOnce         = 20 * time.Minute // a one-shot setting (defensive, autocast off) is not made again for the same pet within this long
	petSummonHold   = 12 * time.Second // a summon or a revive is not cast again while the pet is on its way
	petLeash        = 40.0             // the leader is fleeing when it moves in combat with its pet's mob this far off
	petFleeHealth   = 25               // or has a mob on it below this health, moving
	petMendCombat   = 50               // Mend Pet below this pet health in combat
	petMendRest     = 70               // and out of combat
	petMendMaxRange = 20.0             // Mend Pet reaches 20 yd (spell 136...)

	keyPetAttack = "pet_attack_at" // who the pet was ordered at (reason: the mob's guid)
	keyPetRecall = "pet_recall"
	keyPetSummon = "pet_summon"
	keyPetRevive = "pet_revive"
)

// petTaunts are the pet spells that taunt: Growl (the hunter's pet), Torment and Suffering (the Voidwalker). Their autocast is switched off
// when a tank is in the group.
var petTaunts = []string{"Growl", "Torment", "Suffering", "Anguish"}

func isPetTaunt(name string) bool {
	for _, t := range petTaunts {
		if strings.EqualFold(name, t) {
			return true
		}
	}
	return false
}

// groupedWithOthers says another party member is online.
func groupedWithOthers(c *Ctx) bool {
	for _, m := range c.Party {
		if m.Online && m.GUID != c.Self.GUID {
			return true
		}
	}
	return false
}

// petMob is the mob the pet may be sent at now: the fight's mob, in combat, held by someone (the tank, the player, the pet itself), and
// the pet is out, alive, not told to stay. nil otherwise.
func petMob(c *Ctx) *objects.Unit {
	p := petUsable(c)
	if p == nil || p.Command == "stay" {
		return nil
	}
	f := ValFight.Get(c)
	m := f.Mob
	if m == nil || !m.InCombat || m.Dead || m.Distance < 0 {
		return nil
	}
	if f.Tank != nil && f.Source == "tank" && m.Target != f.Tank.GUID && m.Target != c.Self.GUID && m.Target != p.GUID {
		return nil // the tank has not got it yet (or it is on someone else): the pet waits like the damage dealers
	}
	return m
}

func petMobGUID(c *Ctx) packet.GUID {
	if m := petMob(c); m != nil {
		return m.GUID
	}
	return 0
}

// leaderFleeing says the leader is running from the fight: moving in combat while the pet's mob is a long way off, or with a mob on it
// and little health.
func leaderFleeing(c *Ctx, p *PetState) bool {
	if !c.SelfOK || !c.Self.InCombat || !c.Self.Moving {
		return false
	}
	if p.Target != 0 {
		if m, ok := c.Unit(p.Target); ok && m.Distance > petLeash {
			return true
		}
	}
	return healthPct(c) < petFleeHealth && ValMobOnMe.Get(c) != nil
}

// petRecallWhy says why the pet should be called back: "fight_over", "leader_fleeing" or "".
func petRecallWhy(c *Ctx) string {
	p := petUsable(c)
	if p == nil || p.Command == "stay" || p.Target == 0 {
		return ""
	}
	if leaderFleeing(c, p) {
		return "leader_fleeing"
	}
	if ValFight.Get(c).Mob == nil && !c.Self.InCombat {
		return "fight_over"
	}
	return ""
}

// petTauntSpell is the pet's taunt that has its autocast on, 0 when there is none (or the pet's bar is not known).
func petTauntOn(c *Ctx) (PetSpell, bool) {
	p := petUsable(c)
	if p == nil || !groupedWithOthers(c) || tankInView(c) == nil {
		return PetSpell{}, false
	}
	for _, sp := range p.Spells {
		if sp.OnBar && sp.Autocast && isPetTaunt(sp.Name) && !petOnceHeld(c, "taunt", p.GUID, sp.ID) {
			return sp, true
		}
	}
	return PetSpell{}, false
}

func onceKey(what string, g packet.GUID, spell uint32) string {
	return "pet_once:" + what + ":" + guidStr(g) + ":" + guidStr(packet.GUID(spell))
}

func petOnceHeld(c *Ctx, what string, g packet.GUID, spell uint32) bool {
	_, held := heldKey(c, onceKey(what, g, spell))
	return held
}

// ValBag counts the items in the bags by entry, once a tick.
var ValBag = &Value[map[uint32]uint32]{Name: "bag_counts", Calc: func(c *Ctx) map[uint32]uint32 {
	have := map[uint32]uint32{}
	for _, it := range c.G.Items() {
		have[it.Entry] += it.Count
	}
	return have
}}

// itemSoulShard is the soul shard, the reagent of Create Healthstone and of the demons that need one (item 6265).
const itemSoulShard uint32 = 6265

func soulShards(c *Ctx) int { return int(ValBag.Get(c)[itemSoulShard]) }

// reagentsOK says the bags hold what the spell's reagents need.
func reagentsOK(c *Ctx, spell uint32) bool {
	m, ok := spells.Lookup(spell)
	if !ok || len(m.Reagents) == 0 {
		return true
	}
	have := ValBag.Get(c)
	for _, r := range m.Reagents {
		if have[r.Item] < r.Count {
			return false
		}
	}
	return true
}

// summonSpell is the first spell of the class's list that the spellbook has and the bags can pay for.
func summonSpell(c *Ctx) (uint32, bool) {
	for _, name := range kitOf(c).Summons {
		if id, ok := c.Spells.Highest(name); ok && reagentsOK(c, id) {
			return id, true
		}
	}
	return 0, false
}

// petMissing says the player has no pet out and may bring one: out of combat with nothing to fight.
func petMissing(c *Ctx) bool {
	return petClass(c) && restNow(c) && ValPet.Get(c) == nil && petCapable(c)
}

func selfTarget(c *Ctx) packet.GUID { return c.Self.GUID }

func petCapable(c *Ctx) bool { _, ok := c.G.(PetGame); return ok }

func petStrategy(name string) *Strategy {
	return &Strategy{
		Name: name,
		Actions: []*Action{
			{Name: "pet_attack", Harmful: true, Target: petMobGUID,
				Possible: func(c *Ctx) bool { return petMob(c) != nil },
				Useful: func(c *Ctx) bool {
					m, p := petMob(c), ValPet.Get(c)
					if m == nil || p == nil || p.Target == m.GUID {
						return false
					}
					who, held := heldKey(c, keyPetAttack)
					return !(held && who == guidStr(m.GUID))
				},
				Do: func(ctx context.Context, c *Ctx) error {
					m := petMob(c)
					if m == nil {
						return nil
					}
					setKey(c, keyPetAttack, petAttackHold, guidStr(m.GUID))
					return c.G.(PetGame).PetAttack(ctx, m.GUID)
				}},
			{Name: "pet_recall", Target: func(c *Ctx) packet.GUID {
				if p := ValPet.Get(c); p != nil {
					return p.GUID
				}
				return 0
			},
				Possible: func(c *Ctx) bool { return petRecallWhy(c) != "" },
				Useful:   func(c *Ctx) bool { _, held := heldKey(c, keyPetRecall); return !held },
				Do: func(ctx context.Context, c *Ctx) error {
					setKey(c, keyPetRecall, petRecallHold, petRecallWhy(c))
					return c.G.(PetGame).PetFollow(ctx)
				}},
			{Name: "pet_defensive",
				Possible: func(c *Ctx) bool {
					p := petUsable(c)
					return p != nil && p.BarKnown && p.React == "aggressive" && groupedWithOthers(c) && !petOnceHeld(c, "defensive", p.GUID, 0)
				},
				Do: func(ctx context.Context, c *Ctx) error {
					p := ValPet.Get(c)
					if p == nil {
						return nil
					}
					setKey(c, onceKey("defensive", p.GUID, 0), petOnce, "set")
					return c.G.(PetGame).PetReact(ctx, "defensive")
				}},
			{Name: "pet_taunt_off",
				Possible: func(c *Ctx) bool { _, ok := petTauntOn(c); return ok },
				Do: func(ctx context.Context, c *Ctx) error {
					sp, ok := petTauntOn(c)
					p := ValPet.Get(c)
					if !ok || p == nil {
						return nil
					}
					setKey(c, onceKey("taunt", p.GUID, sp.ID), petOnce, "off")
					return c.G.(PetGame).PetAutocast(ctx, sp.ID, false)
				}},
			{Name: "pet_summon", Resolve: summonSpell, Target: selfTarget,
				Possible: func(c *Ctx) bool { _, held := heldKey(c, keyPetSummon); return petMissing(c) && !held },
				Do: func(ctx context.Context, c *Ctx) error {
					id, ok := summonSpell(c)
					if !ok {
						return nil
					}
					setKey(c, keyPetSummon, petSummonHold, "cast")
					return c.cast(ctx, id, c.Self.GUID)
				}},
			{Name: "pet_revive", Target: selfTarget,
				Resolve: spellByName(func(c *Ctx) string { return kitOf(c).Revive }),
				Possible: func(c *Ctx) bool {
					p := ValPet.Get(c)
					_, held := heldKey(c, keyPetRevive)
					return p != nil && p.Dead && kitOf(c).Revive != "" && restNow(c) && !held
				},
				Do: func(ctx context.Context, c *Ctx) error {
					id, ok := c.Spells.Highest(kitOf(c).Revive)
					if !ok {
						return nil
					}
					setKey(c, keyPetRevive, petSummonHold, "cast")
					return c.cast(ctx, id, c.Self.GUID)
				}},
			{Name: "pet_mend",
				Resolve: spellByName(func(c *Ctx) string { return kitOf(c).Mend }),
				Target: func(c *Ctx) packet.GUID {
					if p := ValPet.Get(c); p != nil {
						return p.GUID
					}
					return 0
				},
				Possible: func(c *Ctx) bool { return petNeedsMend(c) },
			},
		},
		Rules: []Rule{
			{Trigger: Trigger{Name: "mob_held_pet_idle", Active: func(c *Ctx) bool { return petMob(c) != nil }},
				Do: []Next{{"pet_attack", RelNormal + 10}}},
			{Trigger: Trigger{Name: "fight_over_pet_out", Active: func(c *Ctx) bool { return petRecallWhy(c) == "fight_over" }},
				Do: []Next{{"pet_recall", RelNormal + 6}}},
			{Trigger: Trigger{Name: "leader_fleeing", Active: func(c *Ctx) bool { return petRecallWhy(c) == "leader_fleeing" }},
				Do: []Next{{"pet_recall", RelMove + 2}}},
			{Trigger: Trigger{Name: "pet_aggressive_in_group", Active: func(c *Ctx) bool {
				p := petUsable(c)
				return p != nil && p.BarKnown && p.React == "aggressive" && groupedWithOthers(c)
			}}, Do: []Next{{"pet_defensive", RelNormal + 3}}},
			{Trigger: Trigger{Name: "pet_taunts_with_tank", Active: func(c *Ctx) bool { _, ok := petTauntOn(c); return ok }},
				Do: []Next{{"pet_taunt_off", RelNormal + 3}}},
			{Trigger: Trigger{Name: "pet_missing", Active: petMissing}, Do: []Next{{"pet_summon", RelNormal + 4}}},
			{Trigger: Trigger{Name: "pet_dead", Active: func(c *Ctx) bool { p := ValPet.Get(c); return p != nil && p.Dead && restNow(c) }},
				Do: []Next{{"pet_revive", RelNormal + 5}}},
			{Trigger: Trigger{Name: "pet_hurt", Active: petNeedsMend}, Do: []Next{{"pet_mend", RelHigh + 2}}},
		},
	}
}

// petNeedsMend says the pet is hurt enough for Mend Pet, within its reach, and not mending already.
func petNeedsMend(c *Ctx) bool {
	p := petUsable(c)
	if p == nil || kitOf(c).Mend == "" || p.MaxHealth == 0 {
		return false
	}
	u, ok := c.Unit(p.GUID)
	if !ok || u.Distance < 0 || u.Distance > petMendMaxRange || HasAura(u, "Mend Pet") {
		return false
	}
	pct := int(uint64(p.Health) * 100 / uint64(p.MaxHealth))
	if c.Self.InCombat {
		return pct < petMendCombat
	}
	return pct < petMendRest
}

func init() {
	strategyBuilders["pet"] = func() *Strategy { return petStrategy("pet") }
	strategyBuilders["pet_imp"] = func() *Strategy { return petStrategy("pet_imp") }
}
