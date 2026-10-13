package brain

import (
	"context"
	"errors"
	"math"
	"slices"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/pjerra/turtle-agent/internal/objects"
	"github.com/pjerra/turtle-agent/internal/packet"
	"github.com/pjerra/turtle-agent/internal/spells"
)

// The healer role (issue #80 step 4a). The role is the discipline a healing player shares whatever the class: who to heal first, with
// which spell, when not to heal, what to dispel, how to spend mana, where to stand. The class supplies the spells it heals and
// dispels with (ClassInfo.Heals, a HealKit), so another healing class is a table row, not a new engine. Ideas from mod-playerbots'
// healing behaviour (PartyMemberToHeal: the lowest of hp% + distance/10 wins; the heal ladder by health band; mana thresholds; dispel
// by type; GPL-2.0+, numbers and ideas only, no code).
//
//   - Triage (ValHealPlan): every member of the party in view, the player included, scores hp% + distance/10, minus a bonus for the
//     tank; the lowest wins. A member out of reach (38.5 yd), without sight (the navmesh ray, the server's refusal remembered) or
//     dead is not a candidate. The hp% counts the heals other casters have in flight on the member (a cast started by another party
//     member, read off its unit), so two healers do not heal the same hurt.
//   - The spell is chosen by the deficit: the smallest heal that covers it, a big heal for a big deficit, a fast one when the member is
//     low. No heal is started on a deficit under half of the smallest heal in play (overheal). Ranks more than 30 levels under the
//     player are left to the highest rank of each name. Amounts come from the spell metadata (internal/spells effects: base points,
//     dice, the level scaling), without the player's bonus healing, which the agent cannot read.
//   - Mana: below manaSavePct only the members who need it are healed, no fast heals unless low, no HoT, no dispel that is not urgent.
//   - Dispels: by the dispel type of the harmful aura's spell, in the order of the class's kit; a do-not-dispel list is honoured.
//   - Position (healer_position): 20 to 30 yd from the tank, with sight of the tank and of as many members as possible, out of the
//     fight's mob's front.

const (
	healReach        = 38.5 // heal distance of mod-playerbots' config; Heal reaches 40
	healBelowPct     = 85   // a member below this is a candidate in combat
	healTankBelowPct = 90   // the tank's is a little higher
	healRestBelowPct = 90   // at a rest gate every member is healed up to the driver's rested threshold
	healEmergencyPct = 25   // mod-playerbots' critical health band
	healTankEmergPct = 35
	healLowPct       = 45 // low
	healMediumPct    = 65 // medium
	healTankBonus    = 8.0
	manaSavePct      = 40 // below this the healer saves mana
	manaSaveDrop     = 15 // and the candidate thresholds fall by this much
	dispelManaMin    = 20 // an urgent dispel needs this much mana, any other twice as much
	healerDrinkPct   = 80 // a healer drinks below this at a rest gate (the driver's gate wants 75)
	healRankWindow   = 30 // levels under the player's within which a lower rank of a heal is a candidate
	overhealFrac     = 0.5
	healCoverFrac    = 0.9 // a heal covers a deficit when it heals this much of it
	castGuess        = 2.0 // seconds of damage a heal is chosen to outlast
	histKeep         = 4 * time.Second
	burstWindow      = 3 * time.Second
	burstLossPct     = 25 // a member who lost this much of the health within burstWindow is taking burst damage
	hotTick          = 3 * time.Second
	dispelHold       = 4 * time.Second

	// Healer positioning, yards from the tank.
	healerMin   = 20.0
	healerMax   = 30.0
	healerSlack = 3.0
)

// Dispel types of a spell's Dispel field (SpellDefines.h DispelType).
const (
	dispelMagic   uint8 = 1
	dispelCurse   uint8 = 2
	dispelDisease uint8 = 3
	dispelPoison  uint8 = 4
)

// DispelSpell is a spell of a class that removes harmful auras.
type DispelSpell struct {
	Spell string
	Types []uint8 // the dispel types it removes
	// Cures names the aura the spell leaves on its target to cure the type over time (Abolish Disease): a target that carries it is left
	// alone for that type.
	Cures string
}

// HealKit is what a healing class casts. Names are spell names; the brain picks the ranks.
type HealKit struct {
	// Direct lists the direct heals; every known rank (inside the rank window) is a candidate, chosen by amount.
	Direct []string
	// Fast is the part of Direct that is quick and dear, kept for low health and used sparingly.
	Fast []string
	// DirectOnly is the part of Direct whose heal over time is left out of the amount (Regrowth: a direct heal and a HoT in one spell):
	// the spell is chosen by what it heals at once.
	DirectOnly []string
	// Multi lists heals that hit several members (Chain Heal): a candidate only while two or more members in reach are hurt, since on a
	// single member they cost more than the plain heal for the same health.
	Multi []string
	// HoT is the heal over time the healer keeps on the tank.
	HoT string
	// Dispels, in the order of preference.
	Dispels []DispelSpell
}

// noDispel lists auras that hurt more when they are dispelled (the donor's do-not-dispel list; TortoiseBots has the same rule).
var noDispel = []string{"Mutating Injection"}

// ---- spell amounts ----

// effectAmount is the average value of one effect at a caster level: the core rolls base+1 to base+sides when the effect has more than
// one die side and gives base+1 otherwise (SpellMgr.cpp CalculateSimpleValue), so the average is base plus (sides+1)/2, or base+1; then
// the level scaling (internal/spells.Effect: PerLevel per caster level above the spell's base level, up to its max level).
func effectAmount(e spells.Effect, m spells.Meta, level uint32) float64 {
	v := float64(e.Base) + 1
	if e.Sides > 1 {
		v = float64(e.Base) + float64(e.Sides+1)/2
	}
	if e.PerLevel != 0 {
		lv := level
		if m.MaxLevel > 0 && lv > uint32(m.MaxLevel) {
			lv = uint32(m.MaxLevel)
		}
		if lv > uint32(m.BaseLevel) {
			v += float64(e.PerLevel) * float64(lv-uint32(m.BaseLevel))
		}
	}
	return v
}

// healAmount is what a heal spell heals in all, without bonus healing: a direct heal's value, or a periodic heal's value per tick
// over the aura's duration (the tick is 3 s).
func healAmount(m spells.Meta, level uint32) float64 {
	var total float64
	for _, e := range m.Effects {
		switch {
		case e.Effect == 10: // SPELL_EFFECT_HEAL
			total += effectAmount(e, m, level)
		case e.Effect == 6 && e.Aura == 8 && m.DurationMS > 0: // apply aura: periodic heal
			total += effectAmount(e, m, level) * float64(m.DurationMS) / float64(hotTick.Milliseconds())
		}
	}
	return total
}

// healDirectAmount is what a heal spell heals at once, without its heal over time.
func healDirectAmount(m spells.Meta, level uint32) float64 {
	var total float64
	for _, e := range m.Effects {
		if e.Effect == 10 { // SPELL_EFFECT_HEAL
			total += effectAmount(e, m, level)
		}
	}
	return total
}

// membersHurt counts the members in reach and in sight who are below the combat threshold: the party that a heal on several members is for.
func membersHurt(c *Ctx) int {
	n := 0
	for _, u := range healMembers(c) {
		hp, max := memberHealth(c, u)
		if max > 0 && hp/max*100 < healBelowPct && inHealReach(u) && sightOK(c, u) {
			n++
		}
	}
	return n
}

// healOption is a heal spell the player could cast now, as far as its own state tells.
type healOption struct {
	ID     uint32
	Name   string
	Level  uint8
	Amount float64
	Cost   float64
	CastMS uint32
	Fast   bool
	Multi  bool // hits several members (HealKit.Multi)
}

// seconds the heal takes to land (at least the global cooldown).
func (o healOption) seconds() float64 { return math.Max(float64(o.CastMS)/1000, 1.5) }

func (o healOption) hps() float64 { return o.Amount / o.seconds() }

func spellCost(c *Ctx, m spells.Meta) float64 {
	if m.Cost > 0 {
		return float64(m.Cost)
	}
	return float64(m.CostPct) * float64(c.Self.MaxPower) / 100
}

// healOptions lists the direct heals of the kit that the player knows, pays for and has not been refused lately: every rank within
// healRankWindow levels of the player, and the highest rank of each name whatever its level.
func healOptions(c *Ctx, kit *HealKit) []healOption {
	var out []healOption
	names := kit.Direct
	if len(kit.Multi) > 0 && membersHurt(c) >= 2 {
		names = append(slices.Clone(names), kit.Multi...)
	}
	for _, name := range names {
		ranks := c.Spells.Ranks(name) // lowest first
		for i, id := range ranks {
			m, ok := spells.Lookup(id)
			if !ok || !m.Flags.Has(spells.FlagHeal) {
				continue
			}
			if i != len(ranks)-1 && int(m.Level) < int(c.Self.Level)-healRankWindow {
				continue
			}
			if _, held := c.mem.held(id, c.Now); held || !Affordable(c, m) {
				continue
			}
			amount := healAmount(m, c.Self.Level)
			if slices.Contains(kit.DirectOnly, name) {
				amount = healDirectAmount(m, c.Self.Level)
			}
			out = append(out, healOption{ID: id, Name: name, Level: m.Level, Amount: amount, Cost: spellCost(c, m),
				CastMS: m.CastMS, Fast: slices.Contains(kit.Fast, name), Multi: slices.Contains(kit.Multi, name)})
		}
	}
	return out
}

// chooseHeal picks the spell for a deficit. need is the health to make up; pct the member's health in percent; tank whether it is the
// tank. ok is false when no heal is worth casting: nothing castable, or the deficit is under half of the smallest heal (it would be
// mostly overheal). In an emergency (under 25%, the tank under 35%) the biggest fast heal goes; a member who is low (under 45%) gets
// the quickest heal that covers the deficit, else the one that heals most per second; anyone else the smallest heal that covers it, and,
// when none does, the biggest.
func chooseHeal(c *Ctx, kit *HealKit, need, pct float64, tank bool) (healOption, bool) {
	opts := healOptions(c, kit)
	if len(opts) == 0 || need <= 0 {
		return healOption{}, false
	}
	urgent := pct < healLowPct
	saving := PowerPct(c) < manaSavePct
	if !urgent || (saving && pct >= healEmergencyPct+10) {
		// A fast heal is dear: kept for a member who is low (and, when mana is short, for one who is close to dying).
		if p := withoutFast(opts); len(p) > 0 {
			opts = p
		}
	}
	smallest := opts[0].Amount
	for _, o := range opts {
		smallest = math.Min(smallest, o.Amount)
	}
	if need < overhealFrac*smallest {
		return healOption{}, false
	}
	var covering []healOption
	for _, o := range opts {
		if o.Amount >= healCoverFrac*need {
			covering = append(covering, o)
		}
	}
	if urgent {
		if pct < healEmergencyPct || (tank && pct < healTankEmergPct) {
			var fast []healOption
			for _, o := range opts {
				if o.Fast {
					fast = append(fast, o)
				}
			}
			if len(fast) > 0 {
				sort.SliceStable(fast, func(i, j int) bool { return fast[i].Amount > fast[j].Amount })
				return fast[0], true
			}
		}
		if len(covering) > 0 { // the quickest that covers; of equals the smaller
			sort.SliceStable(covering, func(i, j int) bool {
				if covering[i].CastMS != covering[j].CastMS {
					return covering[i].CastMS < covering[j].CastMS
				}
				return covering[i].Amount < covering[j].Amount
			})
			return covering[0], true
		}
		sort.SliceStable(opts, func(i, j int) bool { return opts[i].hps() > opts[j].hps() })
		return opts[0], true
	}
	if len(covering) > 0 { // the smallest that covers; a heal on several members first, since two or more are hurt
		sort.SliceStable(covering, func(i, j int) bool {
			if covering[i].Multi != covering[j].Multi {
				return covering[i].Multi
			}
			return covering[i].Amount < covering[j].Amount
		})
		return covering[0], true
	}
	sort.SliceStable(opts, func(i, j int) bool { return opts[i].Amount > opts[j].Amount })
	return opts[0], true
}

func withoutFast(opts []healOption) []healOption {
	var out []healOption
	for _, o := range opts {
		if !o.Fast {
			out = append(out, o)
		}
	}
	return out
}

// ---- what the healer remembers ----

type hpSample struct {
	at time.Time
	hp uint32
}

type healCastRec struct {
	spell  uint32
	target packet.GUID
	at     time.Time
}

type dispelKey struct {
	target packet.GUID
	aura   uint32
}

// healMem is the healer's memory between ticks: the health members had a moment ago (for burst and incoming damage), the shields it
// sent, the dispels it tried, and the last friendly cast (a refusal for line of sight names the spell, not the target).
type healMem struct {
	mu       sync.Mutex
	hist     map[packet.GUID][]hpSample
	shielded map[packet.GUID]time.Time
	dispels  map[dispelKey]time.Time
	last     healCastRec
	failSeen uint64
	failInit bool
}

func (h *healMem) observe(now time.Time, g packet.GUID, hp uint32) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.hist == nil {
		h.hist = map[packet.GUID][]hpSample{}
	}
	s := append(h.hist[g], hpSample{now, hp})
	for len(s) > 0 && now.Sub(s[0].at) > histKeep {
		s = s[1:]
	}
	h.hist[g] = s
	for k, v := range h.hist { // forget members not seen for a while
		if len(v) > 0 && now.Sub(v[len(v)-1].at) > 10*time.Second {
			delete(h.hist, k)
		}
	}
}

// lost is the health a member lost within burstWindow: the most it had in that time less what it has now.
func (h *healMem) lost(now time.Time, g packet.GUID, hp uint32) uint32 {
	h.mu.Lock()
	defer h.mu.Unlock()
	top := hp
	for _, s := range h.hist[g] {
		if now.Sub(s.at) <= burstWindow && s.hp > top {
			top = s.hp
		}
	}
	return top - hp
}

func (h *healMem) shieldedAt(g packet.GUID) time.Time {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.shielded[g]
}

func (h *healMem) noteShield(g packet.GUID, at time.Time) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.shielded == nil {
		h.shielded = map[packet.GUID]time.Time{}
	}
	h.shielded[g] = at
}

func (h *healMem) dispelTried(k dispelKey, now time.Time) bool {
	h.mu.Lock()
	defer h.mu.Unlock()
	return now.Before(h.dispels[k])
}

func (h *healMem) noteDispel(k dispelKey, until time.Time) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.dispels == nil {
		h.dispels = map[dispelKey]time.Time{}
	}
	h.dispels[k] = until
}

// healCast sends a cast at a friendly unit and remembers where it went: a refusal for line of sight (Before) is a wall to that unit.
func healCast(ctx context.Context, c *Ctx, spell uint32, target packet.GUID) error {
	h := &c.mem.heal
	h.mu.Lock()
	h.last = healCastRec{spell: spell, target: target, at: c.Now}
	h.mu.Unlock()
	return c.cast(ctx, spell, target)
}

// ---- values ----

// ValTank is the tank the healer looks after (findTank).
var ValTank = &Value[*objects.Unit]{Name: "heal_tank", Calc: findTank}

// healMembers lists the units the healer looks after: the player, then each online member of the party in view and alive.
func healMembers(c *Ctx) []*objects.Unit {
	if !c.SelfOK {
		return nil
	}
	out := []*objects.Unit{&c.Self}
	for _, m := range c.Party {
		if !m.Online || m.Dead || m.GUID == c.Self.GUID {
			continue
		}
		if u, ok := c.Unit(m.GUID); ok && !u.Dead && !u.Ghost && u.Distance >= 0 {
			out = append(out, u)
		}
	}
	return out
}

// healCombat says a fight is on: the player is in combat, there is a mob to fight, or a party member is in combat.
func healCombat(c *Ctx) bool {
	if c.Self.InCombat || ValFight.Get(c).Mob != nil {
		return true
	}
	for _, u := range healMembers(c) {
		if u.InCombat {
			return true
		}
	}
	return false
}

// healAllowed says the healer may cast now: in a fight always, otherwise where upkeep is allowed (the dungeon driver's rest gates).
func healAllowed(c *Ctx) bool { return c.SelfOK && (c.RestOK || healCombat(c)) }

func inHealReach(u *objects.Unit) bool { return u.Distance >= 0 && float64(u.Distance) <= healReach }

// sightOK is the line of sight to a member, taken as clear when the mesh cannot tell. The player itself is always in sight.
func sightOK(c *Ctx, u *objects.Unit) bool {
	if u.GUID == c.Self.GUID {
		return true
	}
	clear, known := c.LineOfSight(u)
	return clear || !known
}

// memberHealth reads a member's health and its maximum. A party member's health is absolute (the party stats the core sends), so
// the numbers are used as they are.
func memberHealth(c *Ctx, u *objects.Unit) (hp, max float64) {
	return float64(u.Health), float64(u.MaxHealth)
}

// incomingHeal is the healing other party members have in flight on a member (their casts read off their units), and the player's own.
func incomingHeal(c *Ctx, g packet.GUID) float64 {
	var sum float64
	add := func(spell uint32, target packet.GUID, endsIn *int64) {
		if target != g {
			return
		}
		m, ok := spells.Lookup(spell)
		if !ok || !m.Flags.Has(spells.FlagHeal) || m.Flags.Has(spells.FlagAura) {
			return
		}
		if endsIn != nil && *endsIn < -500 {
			return
		}
		sum += healAmount(m, c.Self.Level)
	}
	for _, m := range c.Party {
		if u, ok := c.Unit(m.GUID); ok && u.Casting != nil && m.GUID != c.Self.GUID {
			add(u.Casting.SpellID, u.Casting.Target, u.Casting.EndsInMS)
		}
	}
	if cs := c.Spells.CastState(); cs.Cast.State == "casting" {
		add(cs.Cast.SpellID, cs.Cast.Target, cs.Cast.EndsInMS)
	}
	return sum
}

// HealPlan is the healer's choice of the tick.
type HealPlan struct {
	Target *objects.Unit
	Pct    float64 // the target's health in percent, counting heals in flight
	Need   float64 // the health to make up
	Tank   bool
	Spell  healOption
	// Far is the most hurt member the player cannot reach or see, when no member in reach needs a heal: the healer walks toward it.
	Far *objects.Unit
}

// ValHealPlan is the triage of the tick (see the package comment): who to heal, and with which spell.
var ValHealPlan = &Value[HealPlan]{Name: "heal_plan", Calc: calcHealPlan}

func calcHealPlan(c *Ctx) HealPlan {
	kit := c.Class().Heals
	if kit == nil || !healAllowed(c) {
		return HealPlan{}
	}
	combat := healCombat(c)
	saving := PowerPct(c) < manaSavePct
	tank := ValTank.Get(c)
	var best HealPlan
	bestScore := math.MaxFloat64
	var far *objects.Unit
	farPct := 70.0
	for _, u := range healMembers(c) {
		hp, max := memberHealth(c, u)
		if max <= 0 {
			continue
		}
		in := incomingHeal(c, u.GUID)
		eff := math.Min(hp+in, max)
		pct := eff / max * 100
		isTank := tank != nil && tank.GUID == u.GUID
		below := float64(healBelowPct)
		switch {
		case !combat:
			below = healRestBelowPct
		case isTank:
			below = healTankBelowPct
		}
		if saving {
			below = math.Max(below-manaSaveDrop, healEmergencyPct)
		}
		if pct >= below {
			continue
		}
		if !inHealReach(u) || !sightOK(c, u) {
			if pct < farPct {
				far, farPct = u, pct
			}
			continue
		}
		score := pct + float64(u.Distance)/10
		if isTank {
			score -= healTankBonus
		}
		if score >= bestScore {
			continue
		}
		need := max - eff
		if lose := float64(c.mem.heal.lost(c.Now, u.GUID, u.Health)); lose > 0 {
			need += math.Min(lose/burstWindow.Seconds()*castGuess, 0.3*max) // the damage that goes on while the heal is cast
		}
		bestScore = score
		best = HealPlan{Target: u, Pct: pct, Need: need, Tank: isTank}
	}
	if best.Target == nil {
		return HealPlan{Far: far}
	}
	opt, ok := chooseHeal(c, kit, best.Need, best.Pct, best.Tank)
	if !ok {
		return HealPlan{}
	}
	best.Spell = opt
	return best
}

// DispelPlan is the dispel of the tick.
type DispelPlan struct {
	Target *objects.Unit
	Spell  uint32
	Name   string
	Aura   uint32
	Urgent bool
	// Far is a member with crowd control on it that the dispel does not reach (Dispel Magic reaches 30 yd, Heal 40), when no dispel is
	// in reach: the healer walks into range (healer_position).
	Far *objects.Unit
}

// dispelSlack is the margin kept inside the dispel's own range.
const dispelSlack = 1.0

// ValDispel finds a harmful aura on a member that the class's dispels remove: by the dispel type of the aura's spell, urgent when it
// is crowd control (charm, fear, sleep, stun, polymorph, horror), otherwise only with mana to spare.
var ValDispel = &Value[DispelPlan]{Name: "dispel_plan", Calc: calcDispel}

func calcDispel(c *Ctx) DispelPlan {
	kit := c.Class().Heals
	if kit == nil || len(kit.Dispels) == 0 || !healAllowed(c) || PowerPct(c) < dispelManaMin {
		return DispelPlan{}
	}
	tank := ValTank.Get(c)
	var best DispelPlan
	var far *objects.Unit
	bestPct, bestRank := 0.0, -1
	for _, u := range healMembers(c) {
		if u.Distance < 0 {
			continue
		}
		hp, max := memberHealth(c, u)
		seen, visible := false, false // the sight ray is asked for only when a dispellable aura is found
		for _, a := range u.Auras {
			if !a.Harmful || slices.ContainsFunc(noDispel, func(n string) bool { return strings.EqualFold(n, a.Name) }) {
				continue
			}
			m, ok := spells.Lookup(a.SpellID)
			if !ok || m.Dispel == 0 || c.mem.heal.dispelTried(dispelKey{u.GUID, a.SpellID}, c.Now) {
				continue
			}
			var spell uint32
			var name string
			handled := false
			for _, ds := range kit.Dispels {
				if !slices.Contains(ds.Types, m.Dispel) {
					continue
				}
				if ds.Cures != "" && HasAura(u, ds.Cures) {
					handled = true // the cure is already working on it
					break
				}
				if id, ok := c.Spells.Highest(ds.Spell); ok && spell == 0 {
					spell, name = id, ds.Spell
				}
			}
			if handled || spell == 0 {
				continue
			}
			urgent := ccMechanics[m.Mechanic]
			if !urgent && PowerPct(c) < 2*dispelManaMin {
				continue
			}
			// The dispel's own range, not the heal's: Dispel Magic, Cure Disease and Abolish Disease reach 30 yd.
			if sm, ok := spells.Lookup(spell); ok && sm.RangeMax > 0 && float64(u.Distance) > float64(sm.RangeMax)-dispelSlack {
				if urgent && far == nil && float64(u.Distance) <= healReach+float64(sm.RangeMax) {
					far = u
				}
				continue
			}
			if !seen {
				seen, visible = true, sightOK(c, u)
			}
			if !visible {
				break
			}
			rank := 0
			if urgent {
				rank = 2
			}
			if tank != nil && tank.GUID == u.GUID {
				rank++
			}
			pct := 100.0
			if max > 0 {
				pct = hp / max * 100
			}
			if best.Target == nil || rank > bestRank || (rank == bestRank && pct < bestPct) {
				best = DispelPlan{Target: u, Spell: spell, Name: name, Aura: a.SpellID, Urgent: urgent}
				bestRank, bestPct = rank, pct
			}
		}
	}
	if best.Target == nil {
		best.Far = far
	}
	return best
}

// hotPlan is the tank to keep a heal over time on: in combat, hurt or taking damage, without the HoT, with mana to spare.
type hotPlan struct {
	Target *objects.Unit
	Spell  uint32
}

var valHot = &Value[hotPlan]{Name: "heal_hot", Calc: func(c *Ctx) hotPlan {
	kit := c.Class().Heals
	if kit == nil || kit.HoT == "" || !healAllowed(c) || PowerPct(c) < manaSavePct {
		return hotPlan{}
	}
	t := ValTank.Get(c)
	if t == nil || !t.InCombat || !inHealReach(t) || !sightOK(c, t) || HasAura(t, kit.HoT) {
		return hotPlan{}
	}
	hp, max := memberHealth(c, t)
	if max <= 0 || (hp/max >= 0.98 && c.mem.heal.lost(c.Now, t.GUID, t.Health) == 0) {
		return hotPlan{}
	}
	id, ok := c.Spells.Highest(kit.HoT)
	if !ok {
		return hotPlan{}
	}
	return hotPlan{Target: t, Spell: id}
}}

// ---- the strategy ----

func init() {
	strategyBuilders["healer_position"] = healerPosition
	strategyBuilders["rest_drink"] = restDrink
}

func targetGUID(u *objects.Unit) packet.GUID {
	if u == nil {
		return 0
	}
	return u.GUID
}

// roleHealer is the healer's discipline: triage, the choice of the heal, dispels, the HoT on the tank, mana. Where to stand is
// healer_position's business, the class's own abilities (shields, buffs, Fade) are the class's strategies.
func roleHealer() *Strategy {
	emergency := func(p HealPlan) bool {
		return p.Pct < healEmergencyPct || (p.Tank && p.Pct < healTankEmergPct)
	}
	plan := func(c *Ctx) HealPlan { return ValHealPlan.Get(c) }
	return &Strategy{
		Name: "role_healer",
		Role: RoleHealer,
		Before: func(c *Ctx) {
			for _, u := range healMembers(c) {
				c.mem.heal.observe(c.Now, u.GUID, u.Health)
			}
			noteHealRefusal(c)
		},
		Mult: []Multiplier{{Name: "finish_the_cast", F: func(c *Ctx, a *Action, rel float32) float32 {
			// An emergency heal may cut a walk short, but not the heal that is being cast: the second one would be refused.
			if a.Name == "heal" && rel >= RelInterrupt && c.Spells.Casting() {
				return 0
			}
			return 1
		}}},
		Actions: []*Action{
			faceTargetAction(),
			{Name: "heal",
				Resolve: func(c *Ctx) (uint32, bool) { p := plan(c); return p.Spell.ID, p.Target != nil && p.Spell.ID != 0 },
				Target:  func(c *Ctx) packet.GUID { return targetGUID(plan(c).Target) },
				Do: func(ctx context.Context, c *Ctx) error {
					p := plan(c)
					if p.Target == nil {
						return nil
					}
					return healCast(ctx, c, p.Spell.ID, p.Target.GUID)
				}},
			{Name: "hot_tank",
				Resolve: func(c *Ctx) (uint32, bool) { h := valHot.Get(c); return h.Spell, h.Target != nil },
				Target:  func(c *Ctx) packet.GUID { return targetGUID(valHot.Get(c).Target) },
				Do: func(ctx context.Context, c *Ctx) error {
					h := valHot.Get(c)
					if h.Target == nil {
						return nil
					}
					return healCast(ctx, c, h.Spell, h.Target.GUID)
				}},
			{Name: "dispel",
				Resolve: func(c *Ctx) (uint32, bool) { d := ValDispel.Get(c); return d.Spell, d.Target != nil },
				Target:  func(c *Ctx) packet.GUID { return targetGUID(ValDispel.Get(c).Target) },
				Do: func(ctx context.Context, c *Ctx) error {
					d := ValDispel.Get(c)
					if d.Target == nil {
						return nil
					}
					c.mem.heal.noteDispel(dispelKey{d.Target.GUID, d.Aura}, c.Now.Add(dispelHold)) // one try per aura: it may resist
					return healCast(ctx, c, d.Spell, d.Target.GUID)
				}},
		},
		Rules: []Rule{
			{Trigger: Trigger{Name: "heal_emergency", Active: func(c *Ctx) bool { p := plan(c); return p.Target != nil && emergency(p) }},
				Do: []Next{{"heal", RelInterrupt + 1}}},
			{Trigger: Trigger{Name: "heal_low", Active: func(c *Ctx) bool {
				p := plan(c)
				return p.Target != nil && !emergency(p) && p.Pct < healLowPct
			}}, Do: []Next{{"heal", RelHigh + 8}}},
			{Trigger: Trigger{Name: "heal_medium", Active: func(c *Ctx) bool {
				p := plan(c)
				return p.Target != nil && p.Pct >= healLowPct && p.Pct < healMediumPct
			}}, Do: []Next{{"heal", RelHigh}}},
			{Trigger: Trigger{Name: "heal_light", Active: func(c *Ctx) bool {
				p := plan(c)
				return p.Target != nil && p.Pct >= healMediumPct && healCombat(c)
			}}, Do: []Next{{"heal", RelNormal}}},
			// At a rest gate a top-up waits for the drink: drinking restores the party's mana and health too.
			{Trigger: Trigger{Name: "heal_rest", Active: func(c *Ctx) bool {
				p := plan(c)
				return p.Target != nil && p.Pct >= healMediumPct && !healCombat(c)
			}}, Do: []Next{{"heal", RelNormal - 2}}},
			{Trigger: Trigger{Name: "dispel_cc", Active: func(c *Ctx) bool { d := ValDispel.Get(c); return d.Target != nil && d.Urgent }},
				Do: []Next{{"dispel", RelHigh + 5}}},
			{Trigger: Trigger{Name: "dispel_debuff", Active: func(c *Ctx) bool { d := ValDispel.Get(c); return d.Target != nil && !d.Urgent }},
				Do: []Next{{"dispel", RelNormal + 1}}},
			{Trigger: Trigger{Name: "tank_hot", Active: func(c *Ctx) bool { return valHot.Get(c).Target != nil }},
				Do: []Next{{"hot_tank", RelNormal - 0.5}}},
		},
	}
}

// noteHealRefusal turns the server's "no line of sight" for a friendly cast into a wall to its target, so that the triage leaves the
// member alone for a few seconds and the position rules look for another place.
func noteHealRefusal(c *Ctx) {
	cs := c.Spells.CastState()
	h := &c.mem.heal
	h.mu.Lock()
	first := !h.failInit
	h.failInit = true
	fresh := cs.Failures != h.failSeen
	h.failSeen = cs.Failures
	last := h.last
	h.mu.Unlock()
	if first || !fresh || cs.LastFailure == nil || cs.LastFailure.ReasonName != "line_of_sight" || cs.LastFailure.SpellID != last.spell {
		return
	}
	if u, ok := c.Unit(last.target); ok && last.target != c.Self.GUID {
		markLOSBlocked(c, u)
	}
}

// ---- position ----

// healerIssue says what is wrong with where the healer stands, "" when nothing is: there is a fight with a tank, no mob has picked the
// healer (that is run_to_tank's business), and the tank is out of sight, farther than 30 yd (plus a slack) or nearer than 20 (less
// the slack), or the healer stands in the front of the fight's mob.
func healerIssue(c *Ctx) string {
	tank, mob := ValTank.Get(c), fightMob(c)
	if tank == nil || mob == nil || !c.SelfOK || ValMobOnMe.Get(c) != nil || !mob.InCombat {
		return ""
	}
	if clear, known := c.LineOfSight(tank); known && !clear {
		return issueNoLOS
	}
	if mob.Distance >= 0 && float64(mob.Distance2D) <= healerMax+healerSlack && inFront(mob, selfPos(c)) {
		return issueFrontal
	}
	switch d := float64(tank.Distance2D); {
	case d < healerMin-healerSlack:
		return issueTooClose
	case d > healerMax+healerSlack:
		return issueTooFar
	}
	return ""
}

// healerSpot is a standing place 20 to 30 yd from the tank with sight of the tank, out of the mob's front and of ground effects, and,
// when there is one, with sight of every other member in view; else of the tank alone.
func healerSpot(c *Ctx, tank, mob *objects.Unit) (Vec3, bool) {
	blocked := losBlocked(c, tank)
	return memoSpot(c, "heal_band", tank.GUID, func() (Vec3, bool) {
		spec := func(all bool) spotSpec {
			return spotSpec{kind: "heal_band", center: unitPos(tank), radii: []float64{healerMin + 2, (healerMin + healerMax) / 2, healerMax - 1}, target: tank,
				base: bearing(unitPos(tank), selfPos(c)),
				extra: func(p Vec3) bool {
					if blocked && dist2(p, selfPos(c)) < losBlockMoved+1 {
						return false
					}
					if (mob != nil && inFront(mob, p)) || inGroundHazard(c, p, 2) {
						return false
					}
					if all {
						s := sightOf(c)
						for _, m := range healMembers(c) {
							if m.GUID == c.Self.GUID || m.GUID == tank.GUID {
								continue
							}
							if clear, known := s.LineClear(p, unitPos(m)); known && !clear {
								return false
							}
						}
					}
					return true
				}}
		}
		if p, ok := findSpot(c, spec(true)); ok {
			return p, true
		}
		return findSpot(c, spec(false))
	})
}

// approachTarget is the member the healer walks toward: a hurt one out of heal reach or sight, else one with crowd control out of
// dispel range.
func approachTarget(c *Ctx) *objects.Unit {
	if f := ValHealPlan.Get(c).Far; f != nil {
		return f
	}
	return ValDispel.Get(c).Far
}

// spotKeepsReach says a standing spot does not take the healer out of reach of the member it has work for: the one to heal, else the one it
// walks toward, else the one to dispel. Without it the healer ping-pongs: it walks to a hurt member, and the position rules (the band round
// the tank) walk it back before the heal is cast.
func spotKeepsReach(c *Ctx, spot Vec3) bool {
	need := ValHealPlan.Get(c).Target
	if need == nil {
		need = approachTarget(c)
	}
	if need == nil {
		need = ValDispel.Get(c).Target
	}
	return need == nil || dist2(spot, unitPos(need)) <= healReach-healReachMargin
}

// healReachMargin is the margin inside the heal reach that a standing spot must keep from the member to be healed.
const healReachMargin = 4.0

// healerPosition keeps the healer where it can heal from: behind the tank at range, in sight of the party; it walks toward a hurt member
// it cannot reach (or one with crowd control beyond the dispel's range); and, when a mob has picked it, it runs to the tank with it. All of
// it is movement, so it is off while the run switched movement off and while the dungeon driver holds the move lock. It ranks under an
// emergency heal (rule relevance 24 against 41) so that a dying tank is healed first, and it does not walk to the band where that
// takes the healer out of reach of the member it has work for (spotKeepsReach).
func healerPosition() *Strategy {
	return &Strategy{
		Name: "healer_position",
		Actions: []*Action{
			{Name: "heal_reposition", Settle: 700 * time.Millisecond,
				Possible: func(c *Ctx) bool {
					tank, mob := ValTank.Get(c), fightMob(c)
					if tank == nil || mob == nil || !canMove(c) {
						return false
					}
					p, ok := healerSpot(c, tank, mob)
					return ok && spotKeepsReach(c, p)
				},
				Useful: func(c *Ctx) bool { return healerIssue(c) != "" },
				Do: func(ctx context.Context, c *Ctx) error {
					tank, mob := ValTank.Get(c), fightMob(c)
					if tank == nil {
						return nil
					}
					p, ok := healerSpot(c, tank, mob)
					if !ok {
						return errors.New("no spot")
					}
					forgetSpot(c)
					return c.walk(ctx, p)
				}},
			{Name: "heal_approach", Settle: 700 * time.Millisecond,
				Possible: func(c *Ctx) bool {
					f := approachTarget(c)
					if f == nil || !canMove(c) {
						return false
					}
					_, ok := bandSpot(c, "heal_reach", f)
					return ok
				},
				Do: func(ctx context.Context, c *Ctx) error {
					f := approachTarget(c)
					if f == nil {
						return nil
					}
					p, ok := bandSpot(c, "heal_reach", f)
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
			{Trigger: Trigger{Name: "healer_bad_position", Active: func(c *Ctx) bool { return canMove(c) && healerIssue(c) != "" }},
				Do: []Next{{"heal_reposition", RelHigh + 4}}},
			{Trigger: Trigger{Name: "member_out_of_reach", Active: func(c *Ctx) bool { return canMove(c) && approachTarget(c) != nil }},
				Do: []Next{{"heal_approach", RelHigh + 4}}},
			{Trigger: Trigger{Name: "mob_on_healer_far", Active: func(c *Ctx) bool { return canMove(c) && runToTankPoint(c) != nil }},
				Do: []Next{{"run_to_tank", RelMove}}},
		},
	}
}

// ---- rest: drinking ----

// restDrink is a mana class's drinking at a rest gate, for a class that conjures no water: out of combat, with no mob to fight, below
// 80% mana for a healer (the driver's gate wants 75) and below 65% otherwise, the best drink in the bags that the level allows (the
// vendor table of drinksFor). A drink just used is not used again for 5 s and nothing is sent while the Drink aura is awaited.
func restDrink() *Strategy {
	below := func(c *Ctx) int {
		if c.P.Role == RoleHealer {
			return healerDrinkPct
		}
		return manaDrinkPct
	}
	return &Strategy{
		Name: "rest_drink",
		Actions: []*Action{{Name: "drink", Settle: drinkSettle,
			Possible: func(c *Ctx) bool { return !inCombat(c) && c.Class().Drinks && drinkEntry(c, false) != 0 },
			Useful:   func(c *Ctx) bool { return !HasAura(&c.Self, "Drink") },
			Do: func(ctx context.Context, c *Ctx) error {
				e := drinkEntry(c, false)
				if e == 0 {
					return errors.New("no drink in the bags")
				}
				if err := c.G.UseItem(ctx, e); err != nil {
					return err
				}
				c.mem.holdEntry(e, c.Now.Add(drinkUseHold))
				return nil
			}}},
		Rules: []Rule{{Trigger: Trigger{Name: "mana_low", Active: func(c *Ctx) bool {
			return c.RestOK && onSelf(c) && !inCombat(c) && ValFight.Get(c).Mob == nil && PowerPct(c) < below(c)
		}}, Do: []Next{{"drink", RelNormal}}}},
	}
}
