package brain

import (
	"context"
	"testing"
	"time"

	"github.com/pjerra/turtle-agent/internal/objects"
	"github.com/pjerra/turtle-agent/internal/packet"
	"github.com/pjerra/turtle-agent/internal/spells"
)

// The resurrections of the tests (ids from internal/spells/meta.tsv.gz; TestRezSpellData checks them against the core's data).
const (
	rzResurrection1, rzResurrection2 = 2006, 2010   // priest, levels 10 and 22
	rzRedemption1                    = 7328         // paladin, level 12
	rzAncestral2, rzAncestral3       = 20609, 20610 // shaman, levels 24 and 36
	rzRebirth3                       = 20742        // druid, level 40, reagent 17036
	rzSeedOfLife                     = 17036

	deadG1, deadG2 = packet.GUID(40), packet.GUID(41)
)

// deadMember adds a dead party member, the body where it fell.
func (g *fakeGame) deadMember(guid packet.GUID, name string, class uint8, x, y float32) {
	g.party = append(g.party, Member{Name: name, GUID: guid, Online: true, Dead: true})
	g.addUnit(objects.Unit{GUID: guid, Type: "player", Name: name, Class: class, InParty: true, Health: 0, MaxHealth: 1000, Dead: true}, x, y)
}

// rezRig is a healer preset out of combat at a rest gate, with 1000 mana, that knows the spells and can walk when walk is set.
func rezRig(t *testing.T, preset string, class uint8, level uint32, known []uint32, walk bool) (*rig, *fakeGame, *posFake) {
	t.Helper()
	g := newFake(known...)
	g.self.Class, g.self.Level = class, level
	g.self.Health, g.self.MaxHealth = 1000, 1000
	g.self.PowerType, g.self.Power, g.self.MaxPower = 0, 1000, 1000
	var game Game = g
	var pf *posFake
	if walk {
		pf = &posFake{fakeGame: g}
		game = pf
	}
	return rigOn(t, game, g, Params{Strategies: []string{preset}}), g, pf
}

var (
	priestRezKnown = []uint32{rzResurrection1, rzResurrection2, prLesser1, prHeal1}
	paladinRez     = []uint32{rzRedemption1, plHL4, plConcentration}
)

func TestPriestResurrectsADeadMemberOutOfCombat(t *testing.T) {
	r, g, _ := rezRig(t, "holy_priest", ClassPriest, 30, priestRezKnown, false)
	g.member(tankG, "Tankard", 1, 1000, 1000, 6, 0)
	g.deadMember(deadG1, "Mage", 8, 10, 0)
	calm(g)
	d := r.tick(100 * time.Millisecond)
	if d == nil || d.Action != "rez" || d.Trigger != "dead_member" || d.Spell != rzResurrection2 || d.Target != deadG1 || d.Rel != RelNormal+4 {
		t.Fatalf("%+v idle %q", d, r.idle())
	}
	if c := g.lastCast(t); c.spell != rzResurrection2 || c.target != deadG1 {
		t.Fatalf("%+v", c)
	}
	// Redemption and Ancestral Spirit are the paladin's and the shaman's.
	rp, gp, _ := rezRig(t, "holy_paladin", ClassPaladin, 50, paladinRez, false)
	gp.deadMember(deadG1, "Mage", 8, 10, 0)
	if d := rp.tick(100 * time.Millisecond); d == nil || d.Action != "rez" || d.Spell != rzRedemption1 {
		t.Fatalf("paladin: %+v idle %q", d, rp.idle())
	}
	rs, gs := restoShamanRig(t, append([]uint32{rzAncestral2, rzAncestral3}, shHealKnown...)...)
	gs.setSelf(func(u *objects.Unit) { u.InCombat = false })
	gs.deadMember(deadG1, "Mage", 8, 10, 0)
	if d := rs.tick(100 * time.Millisecond); d == nil || d.Action != "rez" || d.Spell != rzAncestral3 || d.Target != deadG1 {
		t.Fatalf("shaman: %+v idle %q", d, rs.idle())
	}
}

func TestRezWaitsForAnEndToTheFightAndTheRestGate(t *testing.T) {
	setup := func() (*rig, *fakeGame) {
		r, g, _ := rezRig(t, "holy_priest", ClassPriest, 30, priestRezKnown, false)
		g.member(tankG, "Tankard", 1, 1000, 1000, 6, 0)
		g.deadMember(deadG1, "Mage", 8, 10, 0)
		calm(g)
		return r, g
	}
	noRez := func(name string, r *rig) {
		t.Helper()
		if d := r.tick(100 * time.Millisecond); d != nil && d.Action == "rez" {
			t.Errorf("%s: %+v", name, d)
		}
	}
	r, g := setup()
	g.setSelf(func(u *objects.Unit) { u.InCombat = true })
	noRez("the priest in combat", r)
	r, g = setup()
	g.mutate(tankG, func(u *objects.Unit) { u.InCombat = true })
	noRez("a member in combat", r)
	r, _ = setup()
	r.b.restOK = func() bool { return false }
	noRez("a walk, not a rest gate", r)
	r, g = setup()
	g.setSelf(func(u *objects.Unit) {
		u.Auras = append(u.Auras, objects.Aura{Name: "Shadowform", SpellID: spellShadowform})
	})
	noRez("Shadowform", r)
	r, g = setup()
	g.mutate(deadG1, func(u *objects.Unit) { u.Dead, u.Ghost, u.Health = false, true, 1 })
	noRez("a ghost", r)
	r, g = setup()
	g.mutate(deadG1, func(u *objects.Unit) { u.Dead = false })
	noRez("a member the unit row calls alive", r)
	// Without the spell nothing happens either.
	r2, g2, _ := rezRig(t, "holy_priest", ClassPriest, 30, []uint32{prLesser1}, false)
	g2.deadMember(deadG1, "Mage", 8, 10, 0)
	noRez("no Resurrection in the book", r2)
	// And the control: the setup does resurrect.
	r, _ = setup()
	if d := r.tick(100 * time.Millisecond); d == nil || d.Action != "rez" {
		t.Fatalf("%+v", d)
	}
}

func TestRezOneAtATimeHealersFirstAndNotTheSameTwice(t *testing.T) {
	r, g, _ := rezRig(t, "holy_priest", ClassPriest, 30, priestRezKnown, false)
	g.member(tankG, "Tankard", 1, 1000, 1000, 6, 0)
	g.deadMember(deadG1, "Mage", 8, 6, 0)     // nearer
	g.deadMember(deadG2, "Paladin", 2, 14, 0) // a healer: first
	g.slow = true                             // the 10 s cast stays in progress
	calm(g)
	d := r.tick(100 * time.Millisecond)
	if d == nil || d.Action != "rez" || d.Target != deadG2 {
		t.Fatalf("the healer first: %+v", d)
	}
	// While the cast goes on there is no second one.
	if d := r.tick(2 * time.Second); d != nil && d.Action == "rez" {
		t.Fatalf("a second cast during the first: %+v", d)
	}
	g.st.CastWent(rzResurrection2)
	g.slow = false
	d = r.tick(2 * time.Second)
	if d == nil || d.Action != "rez" || d.Target != deadG1 {
		t.Fatalf("the next member, not the one already sent: %+v", d)
	}
	if d := r.tick(3 * time.Second); d != nil && d.Action == "rez" {
		t.Fatalf("both are on their way: %+v", d)
	}
	// Past the hold a member who is still dead (the offer was declined or timed out) is offered again.
	if d := r.tick(rezSentHold); d == nil || d.Action != "rez" {
		t.Fatalf("%+v", d)
	}
}

func TestRezSkipsAMemberWhoseResurrectionIsAlreadyInFlight(t *testing.T) {
	r, g, _ := rezRig(t, "holy_priest", ClassPriest, 30, priestRezKnown, false)
	g.member(tankG, "Tankard", 1, 1000, 1000, 6, 0)
	g.member(dpsG1, "Paladin", 2, 1000, 1000, 8, 0)
	g.deadMember(deadG1, "Mage", 8, 6, 0)
	g.deadMember(deadG2, "Rogue", 4, 12, 0)
	calm(g)
	// The paladin is casting Redemption at the mage (SMSG_SPELL_START names the target).
	g.mutate(dpsG1, func(u *objects.Unit) {
		u.Casting = &objects.Cast{SpellID: rzRedemption1, Target: deadG1, EndsInMS: ms(7000)}
	})
	d := r.tick(100 * time.Millisecond)
	if d == nil || d.Action != "rez" || d.Target != deadG2 {
		t.Fatalf("the rogue, not the mage: %+v", d)
	}
	// With the cast over (a long past its end) the mage is a candidate again.
	g.mutate(dpsG1, func(u *objects.Unit) {
		u.Casting = &objects.Cast{SpellID: rzRedemption1, Target: deadG1, EndsInMS: ms(-3000)}
	})
	if d := r.tick(rezSentHold + time.Second); d == nil || d.Action != "rez" || d.Target != deadG1 {
		t.Fatalf("%+v", d)
	}
	// Anyone's resurrection counts, not only a party member's.
	r2, g2, _ := rezRig(t, "holy_priest", ClassPriest, 30, priestRezKnown, false)
	g2.deadMember(deadG1, "Mage", 8, 6, 0)
	g2.addUnit(objects.Unit{GUID: 77, Type: "player", Name: "Stranger", Class: 5, Health: 900, MaxHealth: 900,
		Casting: &objects.Cast{SpellID: rzResurrection2, Target: deadG1, EndsInMS: ms(5000)}}, 9, 0)
	if d := r2.tick(100 * time.Millisecond); d != nil && d.Action == "rez" {
		t.Fatalf("%+v", d)
	}
}

func TestRezIsTriedAgainAtOnceWhenTheServerRefusedIt(t *testing.T) {
	r, g, _ := rezRig(t, "holy_priest", ClassPriest, 30, priestRezKnown, false)
	g.deadMember(deadG1, "Mage", 8, 10, 0)
	calm(g)
	if d := r.tick(100 * time.Millisecond); d == nil || d.Action != "rez" {
		t.Fatalf("%+v", d)
	}
	g.st.CastRefused(rzResurrection2, codeOf(t, "out_of_range"))
	if d := r.tick(2 * time.Second); d != nil && d.Action == "rez" {
		t.Fatalf("a refused spell is held back for a moment first: %+v", d)
	}
	if d := r.tick(2 * time.Second); d == nil || d.Action != "rez" || d.Target != deadG1 {
		t.Fatalf("a refused resurrection is sent again: %+v idle %q", d, r.idle())
	}
	if n := len(g.castList()); n != 2 {
		t.Fatalf("casts %d", n)
	}
	// Went off: not again within the hold.
	if d := r.tick(3 * time.Second); d != nil && d.Action == "rez" {
		t.Fatalf("%+v", d)
	}
}

func TestRezReachAndSight(t *testing.T) {
	// A walker walks into reach, then resurrects.
	r, g, pf := rezRig(t, "holy_priest", ClassPriest, 30, priestRezKnown, true)
	g.deadMember(deadG1, "Mage", 8, 38, 0)
	calm(g)
	d := r.tick(100 * time.Millisecond)
	if d == nil || d.Action != "rez_approach" || d.Trigger != "dead_member_far" || d.Rel != RelNormal+3 || len(pf.walkList()) != 1 {
		t.Fatalf("%+v idle %q walks %v", d, r.idle(), pf.walkList())
	}
	d = r.tick(2 * time.Second)
	if d == nil || d.Action != "rez" || d.Target != deadG1 {
		t.Fatalf("%+v idle %q", d, r.idle())
	}
	// Past rezFarMax it is not walked to.
	r2, g2, pf2 := rezRig(t, "holy_priest", ClassPriest, 30, priestRezKnown, true)
	g2.deadMember(deadG1, "Mage", 8, 80, 0)
	if d := r2.tick(100 * time.Millisecond); d != nil || len(pf2.walkList()) != 0 {
		t.Fatalf("%+v walks %v", d, pf2.walkList())
	}
	// Without a walker a member out of reach is left.
	r3, g3, _ := rezRig(t, "holy_priest", ClassPriest, 30, priestRezKnown, false)
	g3.deadMember(deadG1, "Mage", 8, 29, 0)
	if d := r3.tick(100 * time.Millisecond); d != nil {
		t.Fatalf("%+v", d)
	}
	// A wall between: the priest walks to where it can see the body.
	r4, g4, pf4 := rezRig(t, "holy_priest", ClassPriest, 30, priestRezKnown, true)
	g4.deadMember(deadG1, "Mage", 8, 12, 0)
	pf4.blocked = wallRect(5, -6, 6, 6)
	if d := r4.tick(100 * time.Millisecond); d == nil || d.Action != "rez_approach" {
		t.Fatalf("%+v idle %q", d, r4.idle())
	}
}

func TestRezWaitsForManaAndTheHealerDrinksToTheTop(t *testing.T) {
	// A shaman needs 90% of the base mana for Ancestral Spirit: at 85% it drinks (a healer drinks only below 80% otherwise).
	r, g := restoShamanRig(t, append([]uint32{rzAncestral3}, shHealKnown...)...)
	g.setSelf(func(u *objects.Unit) { u.InCombat = false })
	g.items = append(g.items, objects.Item{Entry: 159})
	g.deadMember(deadG1, "Mage", 8, 10, 0)
	g.setMana(85)
	d := r.tick(100 * time.Millisecond)
	if d == nil || d.Action != "drink" {
		t.Fatalf("%+v idle %q", d, r.idle())
	}
	g.setMana(95)
	g.setSelf(func(u *objects.Unit) { u.Auras = append(u.Auras, objects.Aura{Name: "Drink"}) })
	g.stand()
	if d := r.tick(2 * time.Second); d == nil || d.Action != "rez" {
		t.Fatalf("%+v idle %q", d, r.idle())
	}
	// Once the dead member is up, the healer drinks as ever: not at 85%.
	r2, g2 := restoShamanRig(t, append([]uint32{rzAncestral3}, shHealKnown...)...)
	g2.setSelf(func(u *objects.Unit) { u.InCombat = false })
	g2.items = append(g2.items, objects.Item{Entry: 159})
	g2.setMana(85)
	if d := r2.tick(100 * time.Millisecond); d != nil && d.Action == "drink" {
		t.Fatalf("%+v", d)
	}
}

func (g *fakeGame) stand() {
	g.mu.Lock()
	defer g.mu.Unlock()
	var keep []objects.Aura
	for _, a := range g.self.Auras {
		if a.Name != "Drink" {
			keep = append(keep, a)
		}
	}
	g.self.Auras, g.self.StandState = keep, "stand"
}

// ---- Rebirth ----

func rebirthRig(t *testing.T, known ...uint32) (*rig, *casterGame) {
	t.Helper()
	if len(known) == 0 {
		known = append([]uint32{rzRebirth3}, drRestoKnown...)
	}
	r, g := restoRig(t, known...)
	g.items = []objects.Item{{Entry: rzSeedOfLife, Count: 1}}
	return r, g
}

func TestRebirthInACombatOnAKeyMemberOnly(t *testing.T) {
	r, g := rebirthRig(t)
	g.deadMember(deadG1, "Tankard", 1, 10, 0) // a warrior and no tank in view: the tank
	d := r.tick(100 * time.Millisecond)
	if d == nil || d.Action != "rez" || d.Trigger != "dead_key_member" || d.Spell != rzRebirth3 || d.Target != deadG1 || d.Rel != RelHigh+6 {
		t.Fatalf("%+v idle %q", d, r.idle())
	}
	// A healer is a key member too; a mage or a rogue is not.
	r2, g2 := rebirthRig(t)
	g2.deadMember(deadG1, "Priest", 5, 10, 0)
	if d := r2.tick(100 * time.Millisecond); d == nil || d.Action != "rez" || d.Target != deadG1 {
		t.Fatalf("%+v", d)
	}
	r3, g3 := rebirthRig(t)
	g3.deadMember(deadG1, "Mage", 8, 10, 0)
	g3.deadMember(deadG2, "Rogue", 4, 8, 0)
	if d := r3.tick(100 * time.Millisecond); d != nil && d.Action == "rez" {
		t.Fatalf("a damage dealer: %+v", d)
	}
	// With another tank alive a dead warrior is not the tank.
	r4, g4 := rebirthRig(t)
	g4.member(tankG, "Tankard", 1, 1000, 1000, 6, 0)
	g4.deadMember(deadG1, "Warrior", 1, 10, 0)
	if d := r4.tick(100 * time.Millisecond); d != nil && d.Action == "rez" {
		t.Fatalf("%+v", d)
	}
	// The tank that was seen alive and died is remembered.
	r5, g5 := rebirthRig(t)
	g5.member(tankG, "Tankard", 2, 1000, 1000, 6, 0) // a paladin tank
	r5.tick(100 * time.Millisecond)
	g5.mutate(tankG, func(u *objects.Unit) { u.Dead, u.Health = true, 0 })
	g5.party[0].Dead = true
	if d := r5.tick(2 * time.Second); d == nil || d.Action != "rez" || d.Target != tankG {
		t.Fatalf("%+v idle %q", d, r5.idle())
	}
}

func TestRebirthNeedsTheSpellTheReagentAndAFightAndCasterForm(t *testing.T) {
	cast := func(mod func(r *rig, g *casterGame)) *Decision {
		r, g := rebirthRig(t)
		g.deadMember(deadG1, "Tankard", 1, 10, 0)
		mod(r, g)
		return r.tick(100 * time.Millisecond)
	}
	if d := cast(func(*rig, *casterGame) {}); d == nil || d.Action != "rez" {
		t.Fatalf("the control: %+v", d)
	}
	for name, mod := range map[string]func(r *rig, g *casterGame){
		"no reagent":   func(r *rig, g *casterGame) { g.items = nil },
		"out of fight": func(r *rig, g *casterGame) { calm(g.fakeGame) },
		"Moonkin Form": func(r *rig, g *casterGame) {
			g.setSelf(func(u *objects.Unit) { u.Auras = append(u.Auras, objects.Aura{Name: "Moonkin Form", SpellID: 24858}) })
		},
		"on cooldown": func(r *rig, g *casterGame) { g.st.ServerCooldown(rzRebirth3, 1_800_000) },
	} {
		if d := cast(mod); d != nil && d.Action == "rez" {
			t.Errorf("%s: %+v", name, d)
		}
	}
	// Without Rebirth in the book a druid resurrects nobody, at a rest gate or in a fight.
	r, g := restoRig(t, drRestoKnown...)
	g.deadMember(deadG1, "Tankard", 1, 10, 0)
	if d := r.tick(100 * time.Millisecond); d != nil && d.Action == "rez" {
		t.Fatalf("%+v", d)
	}
	// Other healers do not resurrect in a fight: the spells are barred in combat.
	rp, gp, _ := rezRig(t, "holy_priest", ClassPriest, 30, priestRezKnown, false)
	gp.setSelf(func(u *objects.Unit) { u.InCombat = true })
	gp.deadMember(deadG1, "Tankard", 1, 10, 0)
	if d := rp.tick(100 * time.Millisecond); d != nil && d.Action == "rez" {
		t.Fatalf("%+v", d)
	}
}

// ---- what the dungeon driver reads ----

func TestCanRezIsSetByATickThatSawAMemberItCouldResurrect(t *testing.T) {
	r, g, _ := rezRig(t, "holy_priest", ClassPriest, 30, priestRezKnown, false)
	if r.b.CanRez() {
		t.Fatal("nobody is dead")
	}
	g.deadMember(deadG1, "Mage", 8, 10, 0)
	calm(g)
	r.b.restOK = func() bool { return false } // not at a gate yet: the answer does not depend on it
	r.tick(100 * time.Millisecond)
	if !r.b.CanRez() {
		t.Fatal("a dead member in reach and a Resurrection in the book")
	}
	g.clk.advance(rezSeenFor + time.Second)
	if r.b.CanRez() {
		t.Fatal("stale")
	}
	// A druid at a rest gate cannot (Rebirth is for a fight), and a class without a resurrection cannot.
	rd, gd := restoRig(t, append([]uint32{rzRebirth3}, drRestoKnown...)...)
	gd.items = []objects.Item{{Entry: rzSeedOfLife, Count: 1}}
	calm(gd.fakeGame)
	gd.deadMember(deadG1, "Tankard", 1, 10, 0)
	rd.tick(100 * time.Millisecond)
	if rd.b.CanRez() {
		t.Fatal("a druid cannot resurrect out of combat")
	}
	// The controller has no brain, or a stopped one, to ask.
	ctl := NewController(context.Background(), g, DefaultConfig())
	if ctl.CanRez() {
		t.Fatal("no brain runs")
	}
}

// ---- the spell data ----

func TestRezSpellData(t *testing.T) {
	for _, c := range []struct {
		id         uint32
		name       string
		level      uint8
		pct        uint32
		castMS     uint32
		notInFight bool
	}{
		{rzResurrection1, "Resurrection", 10, 75, 10000, true}, {rzResurrection2, "Resurrection", 22, 75, 10000, true}, {10881, "Resurrection", 46, 75, 10000, true},
		{20770, "Resurrection", 58, 75, 10000, true},
		{rzRedemption1, "Redemption", 12, 80, 10000, true}, {plRedemption, "Redemption", 48, 80, 10000, true}, {20773, "Redemption", 60, 80, 10000, true},
		{2008, "Ancestral Spirit", 12, 90, 10000, true}, {rzAncestral2, "Ancestral Spirit", 24, 90, 10000, true}, {rzAncestral3, "Ancestral Spirit", 36, 90, 10000, true},
		{20777, "Ancestral Spirit", 60, 90, 10000, true},
		{20484, "Rebirth", 20, 85, 2000, false}, {20739, "Rebirth", 30, 85, 2000, false}, {rzRebirth3, "Rebirth", 40, 85, 2000, false},
		{20748, "Rebirth", 60, 85, 2000, false},
	} {
		if got := spells.Name(c.id); got != c.name {
			t.Errorf("%d is %q, want %q", c.id, got, c.name)
			continue
		}
		m, ok := spells.Lookup(c.id)
		if !ok || m.Level != c.level || m.CostPct != c.pct || m.CastMS != c.castMS || m.RangeMax != 30 || !m.Flags.Has(spells.FlagResurrect) ||
			m.Flags.Has(spells.FlagNotInCombat) != c.notInFight || m.Power != spells.PowerMana {
			t.Errorf("%s %d: %+v", c.name, c.id, m)
		}
	}
	// Rebirth takes a reagent (a seed per rank) and has a 30 minute cooldown of its own.
	for id, item := range map[uint32]uint32{20484: 17034, 20739: 17035, rzRebirth3: 17036, 20747: 17037, 20748: 17038} {
		m, _ := spells.Lookup(id)
		if len(m.Reagents) != 1 || m.Reagents[0].Item != item || m.Reagents[0].Count != 1 || m.CategoryCDMS != 1800000 {
			t.Errorf("Rebirth %d: %+v", id, m)
		}
	}
	// Forms (stances and stancesNot of the core's spell_template, tortoise-wow 5d79486, sql/base/tw_world_spell_template.sql; no file of
	// sql/database_updates/world sets them for these spells): no rank of any of the four has `stances`; every rank of Resurrection (2006,
	// 2010, 10880, 10881, 20770) has `stancesNot` 1 << 27 (Shadowform, shapeshift form 28) and every rank of Rebirth (20484, 20739,
	// 20742, 20747, 20748) 1 << 30 (Moonkin Form, form 31); Redemption and Ancestral Spirit have none. The brain's table of barred forms
	// says the same, and Moonkin Form's bit is the druid table's.
	coreNot := map[uint32]uint32{2006: 1 << 27, 2010: 1 << 27, 10880: 1 << 27, 10881: 1 << 27, 20770: 1 << 27, 20484: 1 << 30, 20739: 1 << 30,
		20742: 1 << 30, 20747: 1 << 30, 20748: 1 << 30, 7328: 0, 10322: 0, 10324: 0, 20772: 0, 20773: 0, 2008: 0, 20609: 0, 20610: 0, 20776: 0, 20777: 0}
	for id, not := range coreNot {
		if _, ok := SpellStances[id]; ok {
			t.Errorf("%d: the brain gates it by a stance, the core's data has none", id)
		}
		f, barred := rezBarredForms[spells.Name(id)]
		if barred != (not != 0) || (barred && f.Mask != not) {
			t.Errorf("%s %d: the brain bars it in %+v, the core's data says %#x", spells.Name(id), id, f, not)
		}
	}
	if coreFormMasks[24858] != 1<<30 || rezBarredForms["Rebirth"].Mask != coreFormMasks[24858] || rezBarredForms["Rebirth"].Aura != formMoonkin {
		t.Errorf("Moonkin Form: %#x", coreFormMasks[24858])
	}
	if spellShadowform != 15473 || spells.Name(spellShadowform) != "Shadowform" {
		t.Errorf("Shadowform %d", spellShadowform)
	}
}

func TestCanRezHoldsThroughTheCastAndWhileAnotherHealersIsInFlight(t *testing.T) {
	r, g, _ := rezRig(t, "holy_priest", ClassPriest, 30, priestRezKnown, false)
	g.deadMember(deadG1, "Mage", 8, 10, 0)
	calm(g)
	if d := r.tick(100 * time.Millisecond); d == nil || d.Action != "rez" {
		t.Fatalf("%+v", d)
	}
	// The member is held for rezSentHold, the plan has no target: the rest gate must still be told the brain is on it.
	for at := 2; at < 14; at += 2 {
		r.tick(2 * time.Second)
		if !r.b.CanRez() {
			t.Fatalf("CanRez is false %d s after the cast, with the member still dead", at)
		}
	}
	// Another healer's resurrection in flight counts the same.
	r2, g2, _ := rezRig(t, "holy_priest", ClassPriest, 30, priestRezKnown, false)
	g2.member(dpsG1, "Paladin", 2, 1000, 1000, 8, 0)
	g2.deadMember(deadG1, "Mage", 8, 6, 0)
	calm(g2)
	g2.mutate(dpsG1, func(u *objects.Unit) {
		u.Casting = &objects.Cast{SpellID: rzRedemption1, Target: deadG1, EndsInMS: ms(30000)}
	})
	for i := 0; i < 5; i++ {
		r2.tick(2 * time.Second)
		g2.mutate(dpsG1, func(u *objects.Unit) { u.Casting.EndsInMS = ms(30000) })
		if !r2.b.CanRez() {
			t.Fatalf("tick %d: CanRez false while another healer casts", i)
		}
	}
	// Once the member is up there is nothing to wait for.
	g2.mutate(deadG1, func(u *objects.Unit) { u.Dead, u.Health = false, 500 })
	r2.tick(2 * time.Second)
	g2.clk.advance(rezSeenFor + time.Second)
	if r2.b.CanRez() {
		t.Fatal("CanRez with nobody dead")
	}
}

func TestAMemberWhoStaysDeadIsOfferedThreeResurrectionsThenLeftAloneFiveMinutes(t *testing.T) {
	r, g, _ := rezRig(t, "holy_priest", ClassPriest, 30, priestRezKnown, false)
	g.deadMember(deadG1, "Mage", 8, 10, 0)
	calm(g)
	rezCasts := func() int {
		n := 0
		for _, c := range g.castList() {
			if c.spell == rzResurrection2 {
				n++
			}
		}
		return n
	}
	r.tick(100 * time.Millisecond)
	r.tick(rezSentHold + time.Second)
	r.tick(rezSentHold + time.Second)
	if n := rezCasts(); n != 3 {
		t.Fatalf("%d casts in three offers", n)
	}
	if d := r.tick(rezSentHold + time.Second); d != nil && d.Action == "rez" {
		t.Fatalf("a fourth offer: %+v", d)
	}
	r.tick(rezSeenFor + time.Second)
	if r.b.CanRez() {
		t.Fatal("the rest gate is told to wait for a member the brain gave up on")
	}
	if d := r.tick(4 * time.Minute); d != nil && d.Action == "rez" {
		t.Fatalf("within the five minutes: %+v", d)
	}
	if d := r.tick(90 * time.Second); d == nil || d.Action != "rez" || rezCasts() != 4 {
		t.Fatalf("after the skip: %+v casts %d", d, rezCasts())
	}
	// A member who got up in between starts again from nothing.
	r2, g2, _ := rezRig(t, "holy_priest", ClassPriest, 30, priestRezKnown, false)
	g2.deadMember(deadG1, "Mage", 8, 10, 0)
	calm(g2)
	r2.tick(100 * time.Millisecond)
	r2.tick(rezSentHold + time.Second)
	g2.mutate(deadG1, func(u *objects.Unit) { u.Dead, u.Health = false, 500 })
	r2.tick(2 * time.Second)
	g2.mutate(deadG1, func(u *objects.Unit) { u.Dead, u.Health = true, 0 })
	for i := 0; i < 3; i++ {
		if d := r2.tick(rezSentHold + time.Second); d == nil || d.Action != "rez" {
			t.Fatalf("offer %d after the member died again: %+v", i, d)
		}
	}
}
