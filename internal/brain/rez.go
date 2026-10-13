package brain

import (
	"context"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/pjerra/turtle-agent/internal/objects"
	"github.com/pjerra/turtle-agent/internal/packet"
	"github.com/pjerra/turtle-agent/internal/spells"
)

// Resurrecting the party (issue #80 step 4e). The healer presets share one strategy, `rez`: the priest's Resurrection, the paladin's
// Redemption and the shaman's Ancestral Spirit are cast on a dead party member, out of combat only (the spells are barred in a fight:
// SPELL_ATTR_CANT_USED_IN_COMBAT); the druid's Rebirth works in combat, so it is the other way round for a druid: only in combat, only
// on a member who is key to the fight (the tank or a healer), only when the spellbook has it and the bags hold its reagent, and never
// out of combat (a druid cannot resurrect at a rest gate; TortoiseBots' druid bots do the same, DRU-1).
//
// A dead member is a candidate when it is a party member in view, dead with its body where it fell (a released spirit is not in view
// of the living and is left to its own corpse run: the module's own resurrection chooses the same, PartyMemberToResurrect), within the
// spell's reach and in sight; one that is out of reach but near (rezFarMax) makes the healer walk into reach (rez_approach). Healers
// come first, then the tank, then the rest. One member at a time: the brain's cast is the 10 s one, and no second starts before it ends.
// A member who has a resurrection in flight from another caster (the cast of a party member or anyone in view, read off its unit:
// SMSG_SPELL_START names the target) is skipped, and so is one the brain sent a resurrection to a moment ago (rezSentHold) unless
// the server refused the cast. What the dead companion does with the offer is the TortoiseBots module's (docs in README): a dungeon
// bot accepts at once and waits unreleased while a living resurrector is in the party.
//
// Spell facts (pinned by TestRezSpellData against the core's spell_template): Resurrection (level 10 to 58), Redemption (12 to 60),
// Ancestral Spirit (12 to 60) cost 75, 80 and 90 percent of the base mana and take 10 s; Rebirth (20 to 60) costs 85 percent, takes 2 s,
// has a 30 minute cooldown and a reagent (17034 to 17038). The priest's Resurrection is barred in Shadowform and Rebirth in Moonkin
// Form (stancesNot); the other two have no form at all. The cost is why a healer drinks to the top while a resurrection waits for
// mana (rezNeedsMana, restDrink).

const (
	rezReach    = 28.0             // the spells reach 30 yd; the brain casts from within 28
	rezFarMax   = 40.0             // a dead member farther than this is not walked to (one walk step is 24 yd and the band 14 to 28 yd from the body)
	rezSentHold = 14 * time.Second // after a resurrection went out: the 10 s cast, the offer, and the accept
	rezSeenFor  = 3 * time.Second  // CanRez answers true this long after the brain last saw a member it could resurrect
	rezMaxTries = 3                // resurrections sent to one member who stays dead (it keeps declining) ...
	rezCapSkip  = 5 * time.Minute  // ... and then it is left alone this long
)

// rezSpellNames is the resurrection of each class.
var rezSpellNames = map[uint8]string{
	ClassPriest:  "Resurrection",
	ClassPaladin: "Redemption",
	ClassShaman:  "Ancestral Spirit",
	ClassDruid:   "Rebirth",
}

// rezMem is the memory of the resurrecting: who it sent a resurrection to and when, and when the brain last saw a member it could
// resurrect (read by another goroutine through CanRez).
type rezMem struct {
	mu     sync.Mutex
	sent   map[packet.GUID]rezSent
	tries  map[packet.GUID]rezTries
	seenNS atomic.Int64 // unix nanoseconds of the last tick that had a candidate for an out-of-combat resurrection

	lastTank packet.GUID // the tank the brain saw last (a dead tank is not found by findTank)
	failSeen uint64
	failInit bool
}

// rezTries counts the resurrections sent to a member who stayed dead, and until when it is left alone once the count is used up.
type rezTries struct {
	n     int
	until time.Time
}

type rezSent struct {
	until time.Time
	spell uint32
	at    time.Time
}

func (r *rezMem) note(g packet.GUID, spell uint32, at, until time.Time) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.sent == nil {
		r.sent = map[packet.GUID]rezSent{}
	}
	r.sent[g] = rezSent{until: until, spell: spell, at: at}
	if r.tries == nil {
		r.tries = map[packet.GUID]rezTries{}
	}
	t := r.tries[g]
	t.n++
	if t.n >= rezMaxTries {
		t.until = until.Add(rezCapSkip)
	}
	r.tries[g] = t
}

// capped says the member has had its resurrections and is left alone (it stays dead and keeps declining).
func (r *rezMem) capped(g packet.GUID, now time.Time) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	t, ok := r.tries[g]
	if ok && t.n >= rezMaxTries && !now.Before(t.until) {
		delete(r.tries, g) // the skip is over: the count starts again
		return false
	}
	return ok && t.n >= rezMaxTries
}

// forget drops the count of a member who is alive.
func (r *rezMem) forget(g packet.GUID) {
	r.mu.Lock()
	delete(r.tries, g)
	r.mu.Unlock()
}

func (r *rezMem) held(g packet.GUID, now time.Time) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	s, ok := r.sent[g]
	if ok && !now.Before(s.until) {
		delete(r.sent, g)
		return false
	}
	return ok
}

// refused forgets the resurrection of a spell the server refused after it was sent, so that the member is tried again at once.
func (r *rezMem) refused(spell uint32, since time.Time) {
	r.mu.Lock()
	defer r.mu.Unlock()
	for g, s := range r.sent {
		if s.spell == spell && !s.at.Before(since) {
			delete(r.sent, g)
			if t := r.tries[g]; t.n > 0 { // a refused cast is not a try
				t.n--
				t.until = time.Time{}
				r.tries[g] = t
			}
		}
	}
}

// rezBarredForms are the shapeshift forms a resurrection is barred in (`stancesNot` of every rank in the core's spell_template, as a bit
// of the form id minus one; pinned by TestRezSpellData): the priest's Resurrection in Shadowform, form 28, Rebirth in Moonkin Form, form 31.
var rezBarredForms = map[string]struct {
	Aura string
	Mask uint32
}{
	"Resurrection": {"Shadowform", 1 << 27},
	"Rebirth":      {formMoonkin, 1 << 30},
}

// rezSpell is the highest rank of the player's class's resurrection that the spellbook has.
func rezSpell(c *Ctx) (uint32, bool) {
	name, ok := rezSpellNames[c.Self.Class]
	if !ok {
		return 0, false
	}
	return c.Spells.Highest(name)
}

// rezPending says a resurrection is in flight on the unit: a cast of a resurrection spell aimed at it by anyone in view.
func rezPending(c *Ctx, g packet.GUID) bool {
	for i := range c.Units {
		u := &c.Units[i]
		if u.Casting == nil || u.Casting.Target != g {
			continue
		}
		if u.Casting.EndsInMS != nil && *u.Casting.EndsInMS < -500 {
			continue
		}
		if m, ok := spells.Lookup(u.Casting.SpellID); ok && m.Flags.Has(spells.FlagResurrect) {
			return true
		}
	}
	return false
}

// haveReagents says the bags hold the reagents a spell uses up.
func haveReagents(c *Ctx, m spells.Meta) bool {
	for _, r := range m.Reagents {
		var n uint32
		for _, it := range c.G.Items() {
			if it.Entry == r.Item {
				n += it.Count
			}
		}
		if n < r.Count {
			return false
		}
	}
	return true
}

// healerClass says the class heals in a party (paladin, priest, shaman, druid), for the order dead members are resurrected in.
func healerClass(class uint8) bool {
	return class == ClassPaladin || class == ClassPriest || class == ClassShaman || class == ClassDruid
}

// rezPlan is the resurrection of the tick.
type rezPlan struct {
	Target *objects.Unit // a member to cast on now
	Spell  uint32
	// Far is the nearest dead member in view that the player cannot reach or see from here, when no member is in reach.
	Far *objects.Unit
	// Combat says the plan is Rebirth's (in a fight); false is the out-of-combat resurrection.
	Combat bool
	// Waiting says a dead member in view has a resurrection on its way (the brain's own, in its hold, or another caster's in flight): the
	// dungeon driver's rest gate keeps waiting for it (CanRez).
	Waiting bool
	// NeedMana says a member is waiting and the spell is not affordable yet.
	NeedMana bool
}

// ValRez is the resurrection to cast now, if any (see the comment at the top of the file). It does not look at the rest gate: the rules
// do, and CanRez reads it outside one.
var ValRez = &Value[rezPlan]{Name: "rez_plan", Calc: calcRez}

func calcRez(c *Ctx) rezPlan {
	id, ok := rezSpell(c)
	if !ok || !c.SelfOK || len(c.Party) == 0 {
		return rezPlan{}
	}
	meta, ok := spells.Lookup(id)
	if !ok || !meta.Flags.Has(spells.FlagResurrect) {
		return rezPlan{}
	}
	combat := healCombat(c)
	rebirth := c.Self.Class == ClassDruid
	if combat != rebirth { // Rebirth in a fight, everything else out of it
		return rezPlan{}
	}
	if f, barred := rezBarredForms[spells.Name(id)]; (barred && HasAura(&c.Self, f.Aura)) || !haveReagents(c, meta) {
		return rezPlan{}
	}
	tank := ValTank.Get(c)
	c.mem.rez.mu.Lock()
	lastTank := c.mem.rez.lastTank
	c.mem.rez.mu.Unlock()
	// The tank is dead and so not found: the one seen last, the one named, or, with no tank in view, a warrior.
	isTank := func(m Member, u *objects.Unit) bool {
		switch {
		case tank != nil:
			return tank.GUID == u.GUID
		case lastTank == u.GUID:
			return true
		case c.P.Tank != "":
			return strings.EqualFold(m.Name, c.P.Tank)
		}
		return u.Class == ClassWarrior
	}
	var best, far *objects.Unit
	waiting := false
	bestRank := 99
	for _, m := range c.Party {
		if !m.Online || m.GUID == c.Self.GUID {
			continue
		}
		u, ok := c.Unit(m.GUID)
		if ok && !u.Dead {
			c.mem.rez.forget(u.GUID)
		}
		if !ok || !u.Dead || u.Ghost || u.Distance < 0 {
			continue
		}
		key := isTank(m, u) || healerClass(u.Class)
		if rebirth && !key {
			continue // Rebirth is for the one the fight cannot do without
		}
		if float64(u.Distance) > rezFarMax {
			continue
		}
		if rezPending(c, u.GUID) || c.mem.rez.held(u.GUID, c.Now) {
			waiting = true
			continue
		}
		if c.mem.rez.capped(u.GUID, c.Now) {
			continue
		}
		if float64(u.Distance) > rezReach || !sightOK(c, u) {
			if far == nil || u.Distance < far.Distance {
				far = u
			}
			continue
		}
		rank := 2
		switch {
		case healerClass(u.Class):
			rank = 0
		case isTank(m, u):
			rank = 1
		}
		if best == nil || rank < bestRank || (rank == bestRank && u.Distance < best.Distance) {
			best, bestRank = u, rank
		}
	}
	p := rezPlan{Spell: id, Combat: combat, Waiting: waiting}
	switch {
	case best != nil:
		p.NeedMana = !Affordable(c, meta)
		if !c.Self.Moving {
			p.Target = best
		}
	case far != nil:
		p.Far = far
		p.NeedMana = !Affordable(c, meta)
	}
	return p
}

// rezNeedsMana says a dead member waits for a resurrection the player cannot pay for yet (restDrink drinks to the top).
func rezNeedsMana(c *Ctx) bool {
	if c.P.Role != RoleHealer || !c.Class().Drinks || c.Class().Heals == nil {
		return false
	}
	p := ValRez.Get(c)
	return !p.Combat && p.NeedMana
}

// noteRezSeen stamps the moment the brain saw a member it could resurrect out of combat (CanRez), and forgets a resurrection the
// server refused.
func noteRezSeen(c *Ctx) {
	if t := ValTank.Get(c); t != nil {
		c.mem.rez.mu.Lock()
		c.mem.rez.lastTank = t.GUID
		c.mem.rez.mu.Unlock()
	}
	if p := ValRez.Get(c); !p.Combat && (p.Target != nil || p.Far != nil || p.Waiting) {
		c.mem.rez.seenNS.Store(c.Now.UnixNano())
	}
	cs := c.Spells.CastState()
	r := &c.mem.rez
	r.mu.Lock()
	first := !r.failInit
	r.failInit = true
	fresh := cs.Failures != r.failSeen
	r.failSeen = cs.Failures
	r.mu.Unlock()
	if first || !fresh || cs.LastFailure == nil {
		return
	}
	if m, ok := spells.Lookup(cs.LastFailure.SpellID); ok && m.Flags.Has(spells.FlagResurrect) {
		r.refused(cs.LastFailure.SpellID, c.Now.Add(-30*time.Second))
	}
}

// rezStrategy is the shared resurrection of the healer presets.
func rezStrategy() *Strategy {
	plan := func(c *Ctx) rezPlan { return ValRez.Get(c) }
	canWalk := func(c *Ctx) bool {
		p := plan(c)
		if p.Far == nil || p.Combat || !c.RestOK || !canMove(c) { // a walk is for a rest gate; in a fight the healer stays where the tank needs it
			return false
		}
		_, ok := bandSpot(c, "rez_reach", p.Far)
		return ok
	}
	return &Strategy{
		Name:   "rez",
		Before: noteRezSeen,
		Actions: []*Action{
			{Name: "rez", Dead: true,
				Resolve: func(c *Ctx) (uint32, bool) { p := plan(c); return p.Spell, p.Target != nil },
				Target:  func(c *Ctx) packet.GUID { return targetGUID(plan(c).Target) },
				Do: func(ctx context.Context, c *Ctx) error {
					p := plan(c)
					if p.Target == nil {
						return nil
					}
					c.mem.rez.note(p.Target.GUID, p.Spell, c.Now, c.Now.Add(rezSentHold))
					return healCast(ctx, c, p.Spell, p.Target.GUID)
				}},
			{Name: "rez_approach", Settle: 700 * time.Millisecond, Possible: canWalk,
				Do: func(ctx context.Context, c *Ctx) error {
					p := plan(c)
					if p.Far == nil {
						return nil
					}
					spot, ok := bandSpot(c, "rez_reach", p.Far)
					if !ok {
						return nil
					}
					forgetSpot(c)
					return c.walk(ctx, spot)
				}},
		},
		Rules: []Rule{
			{Trigger: Trigger{Name: "dead_member", Active: func(c *Ctx) bool {
				p := plan(c)
				return p.Target != nil && !p.Combat && c.RestOK
			}}, Do: []Next{{"rez", RelNormal + 4}}},
			{Trigger: Trigger{Name: "dead_key_member", Active: func(c *Ctx) bool {
				p := plan(c)
				return p.Target != nil && p.Combat
			}}, Do: []Next{{"rez", RelHigh + 6}}},
			{Trigger: Trigger{Name: "dead_member_far", Active: canWalk}, Do: []Next{{"rez_approach", RelNormal + 3}}},
		},
	}
}

func init() {
	strategyBuilders["rez"] = rezStrategy
}

// CanRez says the brain can resurrect a dead member of the party out of combat: its class has a resurrection in the spellbook and it saw a
// dead member in view a moment ago. It is read by the dungeon driver at a rest gate (internal/dungeon.Config.BrainRezWait), from another
// goroutine than the brain's, so it reads a stamp the brain sets each tick.
func (b *Brain) CanRez() bool {
	ns := b.mem.rez.seenNS.Load()
	return ns != 0 && b.cfg.Now().UnixNano()-ns < int64(rezSeenFor)
}

// CanRez says the running brain can resurrect a dead party member now (see Brain.CanRez); false when no brain runs.
func (c *Controller) CanRez() bool {
	c.mu.Lock()
	b := c.b
	run := c.running()
	c.mu.Unlock()
	return run && b != nil && b.CanRez()
}
