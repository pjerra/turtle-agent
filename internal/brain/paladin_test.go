package brain

import (
	"math"
	"testing"
	"time"

	"github.com/pjerra/turtle-agent/internal/objects"
	"github.com/pjerra/turtle-agent/internal/spells"
)

// Spells of the paladin tests (ids from internal/spells/meta.tsv.gz; TestPaladinSpellData checks the facts the rotation relies on).
const (
	spSoCommand   = 20919 // Seal of Command rank 4, level 50
	spSoRight     = 20292 // Seal of Righteousness rank 7, level 50
	spSoCrusader  = 20307 // Seal of the Crusader rank 5, level 42
	spSoWisdom    = 51745 // Seal of Wisdom rank 4, level 48
	spJudgement   = 20271
	spCrusStrike  = 10336 // Crusader Strike rank 4, level 46
	spConsecrate  = 20923 // Consecration rank 4, level 50
	spHammerJ     = 5589  // Hammer of Justice rank 3, level 40
	spLayOnHands  = 10310 // rank 3, level 50
	spDivineProt  = 5573  // Divine Protection rank 2
	spRetribAura  = 10300 // Retribution Aura rank 4, level 46
	spDevotionAur = 10292 // Devotion Aura rank 6, level 50
	spMight       = 19837 // Blessing of Might rank 5, level 42
	spKings       = 20217
)

var paladinSpells = []uint32{spSoCommand, spSoRight, spSoWisdom, spJudgement, spCrusStrike, spConsecrate, spHammerJ, spLayOnHands, spDivineProt,
	spRetribAura, spDevotionAur, spMight, spKings}

// allPaladin is the spellbook with Seal of the Crusader too (the plain one chooses Command on a fresh mob).
var allPaladin = append(append([]uint32{}, paladinSpells...), spSoCrusader)

var paladinBuffed = []objects.Aura{{Name: "Retribution Aura", SpellID: spRetribAura}, {Name: "Blessing of Might", SpellID: spMight}}

// newPaladinRig is a paladin of level 50 with 3000 mana, its aura and blessing up, on a fake game that can walk, see and follow the swings.
func newPaladinRig(t *testing.T, p Params, known ...uint32) (*rig, *meleeFake) {
	t.Helper()
	if len(known) == 0 {
		known = paladinSpells
	}
	return newClassRig(t, "ret_paladin", ClassPaladin, 50, 0, 3000, p, known, func(u *objects.Unit) {
		u.Auras = append([]objects.Aura{}, paladinBuffed...)
	})
}

func setSeal(g *fakeGame, name string, id uint32) {
	g.setSelf(func(u *objects.Unit) {
		keep := u.Auras[:0:0]
		for _, a := range u.Auras {
			if len(a.Name) < 8 || a.Name[:8] != "Seal of " {
				keep = append(keep, a)
			}
		}
		if name != "" {
			keep = append(keep, objects.Aura{Name: name, SpellID: id})
		}
		u.Auras = keep
	})
}

func TestPaladinSealsJudgesAndStrikes(t *testing.T) {
	r, mf := newPaladinRig(t, Params{})
	tankFights(r.g, 7, 4, 0)
	mf.setSwing(mobA)
	// The seal is a buff on the paladin: it goes up while the opening delay runs.
	d := r.tick(100 * time.Millisecond)
	if d == nil || d.Action != "seal" || d.Spell != spSoCommand || d.Trigger != "seal_down" || d.Rel != RelNormal+8 {
		t.Fatalf("%+v idle %q", d, r.idle())
	}
	setSeal(r.g, "Seal of Command", spSoCommand)
	if d := r.tick(1600 * time.Millisecond); d != nil {
		t.Fatalf("an attack before the tank held the mob: %+v", d)
	}
	d = r.tick(1600 * time.Millisecond)
	if d == nil || d.Action != "judgement" || d.Spell != spJudgement || d.Target != mobA || d.Rel != RelNormal+7 {
		t.Fatalf("%+v idle %q", d, r.idle())
	}
	setSeal(r.g, "", 0) // the judgement spent it
	if d := r.tick(1600 * time.Millisecond); d == nil || d.Action != "seal" {
		t.Fatalf("the seal again: %+v", d)
	}
	setSeal(r.g, "Seal of Command", spSoCommand)
	d = r.tick(1600 * time.Millisecond)
	if d == nil || d.Action != "crusader_strike" || d.Spell != spCrusStrike || d.Rel != RelNormal+6 {
		t.Fatalf("Judgement is on its 10 s cooldown: %+v idle %q", d, r.idle())
	}
	if got := casts(r.g); !eq(got, []uint32{spSoCommand, spJudgement, spSoCommand, spCrusStrike}) {
		t.Fatalf("casts %v", got)
	}
	if len(mf.walkList()) != 0 {
		t.Fatalf("it stands behind the mob and had no reason to walk: %v", mf.walkList())
	}
}

func TestPaladinSealChoice(t *testing.T) {
	first := func(known []uint32, mana uint32, tuneMob func(*objects.Unit)) uint32 {
		r, mf := newPaladinRig(t, Params{}, known...)
		r.g.setSelf(func(u *objects.Unit) { u.Power = mana })
		tankFights(r.g, 7, 4, 0)
		mf.setSwing(mobA)
		if tuneMob != nil {
			r.g.mutate(mobA, tuneMob)
		}
		d := r.tick(100 * time.Millisecond)
		if d == nil || d.Action != "seal" {
			return 0
		}
		return d.Spell
	}
	// The Crusader on a healthy mob that has no Judgement of the Crusader yet; not when a companion has put one on, not on a mob that
	// is nearly dead, not with the mana low.
	if got := first(allPaladin, 3000, nil); got != spSoCrusader {
		t.Errorf("a fresh mob: seal %d", got)
	}
	jotc := func(u *objects.Unit) {
		u.Auras = []objects.Aura{{Name: "Judgement of the Crusader", SpellID: 21183, Harmful: true}}
	}
	if got := first(allPaladin, 3000, jotc); got != spSoCommand {
		t.Errorf("a mob with Judgement of the Crusader: seal %d", got)
	}
	if got := first(allPaladin, 3000, func(u *objects.Unit) { u.Health = u.MaxHealth / 2 }); got != spSoCommand {
		t.Errorf("a mob at 50%%: seal %d", got)
	}
	if got := first(allPaladin, 1200, nil); got != spSoCommand {
		t.Errorf("at 40%% mana: seal %d", got)
	}
	// Wisdom when the mana is low, unless the mob carries Judgement of Wisdom already.
	if got := first(allPaladin, 600, nil); got != spSoWisdom {
		t.Errorf("at 20%% mana: seal %d", got)
	}
	jow := func(u *objects.Unit) {
		u.Auras = []objects.Aura{{Name: "Judgement of Wisdom", SpellID: 20186, Harmful: true}}
	}
	if got := first(allPaladin, 600, jow); got != spSoCommand {
		t.Errorf("at 20%% mana on a mob with Judgement of Wisdom: seal %d", got)
	}
	// Righteousness when there is no Command yet; nothing when the paladin has no seal.
	if got := first([]uint32{spSoRight, spJudgement}, 3000, nil); got != spSoRight {
		t.Errorf("no Command: seal %d", got)
	}
	if got := first([]uint32{spJudgement, spCrusStrike}, 3000, nil); got != 0 {
		t.Errorf("no seal known: seal %d", got)
	}
	// Judgement needs a seal.
	r, mf := newPaladinRig(t, Params{}, spJudgement, spCrusStrike)
	tankFights(r.g, 7, 4, 0)
	mf.setSwing(mobA)
	r.tick(100 * time.Millisecond)
	if d := open(r); d == nil || d.Action != "crusader_strike" {
		t.Fatalf("Judgement without a seal: %+v", d)
	}
}

func TestPaladinASealThatIsUpIsNeverSwapped(t *testing.T) {
	r, mf := newPaladinRig(t, Params{})
	tankFights(r.g, 7, 4, 0)
	mf.setSwing(mobA)
	setSeal(r.g, "Seal of Righteousness", spSoRight)
	r.g.setSelf(func(u *objects.Unit) { u.Power = 500 }) // low mana: Wisdom would be wanted, but a seal is up
	for i := 0; i < 4; i++ {
		r.tick(1600 * time.Millisecond)
	}
	for _, id := range casts(r.g) {
		if id == spSoWisdom || id == spSoCommand || id == spSoCrusader {
			t.Fatalf("a seal swapped: %v", casts(r.g))
		}
	}
}

func TestConsecrationOnlyWithAPackAndWhenTheAreaVetoAllowsIt(t *testing.T) {
	known := []uint32{spSoCommand, spConsecrate, spRetribAura, spMight}
	pack := func(r *rig, mf *meleeFake) {
		tankFights(r.g, 7, 4, 0)
		r.g.addUnit(mob(mobB, tankG), 4, 3)
		mf.setSwing(mobA)
		setSeal(r.g, "Seal of Command", spSoCommand)
		r.tick(100 * time.Millisecond)
	}
	r, mf := newPaladinRig(t, Params{}, known...)
	pack(r, mf)
	if d := open(r); d == nil || d.Action != "consecration" || d.Spell != spConsecrate || d.Rel != RelNormal+4 || d.Target != selfG {
		t.Fatalf("two mobs on the tank: %+v idle %q", d, r.idle())
	}
	// A mob that nobody has, near: the damage dealer's veto.
	r, mf = newPaladinRig(t, Params{}, known...)
	pack(r, mf)
	r.g.addUnit(mob(mobC, 999), 6, -6)
	if d := open(r); d != nil {
		t.Fatalf("Consecration with a loose mob near: %+v", d)
	}
	c := r.b.newCtx(r.g.clk.Now())
	if rel, by := r.b.scale(c, r.b.actions["consecration"], RelNormal+4); rel != 0 || by != "aoe_pulls_loose_mob" {
		t.Fatalf("scaled to %v by %q", rel, by)
	}
	// One mob, or not enough mana: no.
	r, mf = newPaladinRig(t, Params{}, known...)
	tankFights(r.g, 7, 4, 0)
	mf.setSwing(mobA)
	setSeal(r.g, "Seal of Command", spSoCommand)
	r.tick(100 * time.Millisecond)
	if d := open(r); d != nil {
		t.Fatalf("one mob: %+v", d)
	}
	r, mf = newPaladinRig(t, Params{}, known...)
	pack(r, mf)
	r.g.setSelf(func(u *objects.Unit) { u.Power = 1000 }) // 33%
	if d := open(r); d != nil {
		t.Fatalf("at 33%% mana: %+v", d)
	}
}

func TestPaladinKeepsAnAuraAndABlessingAtRest(t *testing.T) {
	bare := func(p Params, auras ...objects.Aura) *rig {
		r, _ := newClassRig(t, "ret_paladin", ClassPaladin, 50, 0, 3000, p, paladinSpells, func(u *objects.Unit) { u.Auras = auras })
		return r
	}
	r := bare(Params{})
	d := r.tick(time.Second)
	if d == nil || d.Action != "paladin_aura" || d.Spell != spRetribAura || d.Rel != RelNormal+3 {
		t.Fatalf("%+v", d)
	}
	addAura(r.g, "Retribution Aura", spRetribAura, nil)
	d = r.tick(2 * time.Second)
	if d == nil || d.Action != "blessing" || d.Spell != spMight {
		t.Fatalf("%+v", d)
	}
	addAura(r.g, "Blessing of Might", spMight, nil)
	if d := r.tick(2 * time.Second); d != nil {
		t.Fatalf("everything up: %+v", d)
	}
	// A companion's Devotion Aura is a different aura: ours still goes up. Its Kings is a blessing: ours does not go over it.
	r = bare(Params{}, objects.Aura{Name: "Devotion Aura", SpellID: spDevotionAur}, objects.Aura{Name: "Blessing of Kings", SpellID: spKings})
	if d := r.tick(time.Second); d == nil || d.Spell != spRetribAura {
		t.Fatalf("%+v", d)
	}
	addAura(r.g, "Retribution Aura", spRetribAura, nil)
	if d := r.tick(2 * time.Second); d != nil {
		t.Fatalf("a blessing is up: %+v", d)
	}
	// The same blessing ending (under a minute left) is renewed.
	r = bare(Params{}, objects.Aura{Name: "Retribution Aura", SpellID: spRetribAura}, objects.Aura{Name: "Blessing of Might", SpellID: spMight, RemainingMS: ms(30000)})
	if d := r.tick(time.Second); d == nil || d.Action != "blessing" || d.Spell != spMight {
		t.Fatalf("%+v", d)
	}
	r = bare(Params{}, objects.Aura{Name: "Retribution Aura", SpellID: spRetribAura}, objects.Aura{Name: "Blessing of Kings", SpellID: spKings, RemainingMS: ms(30000)})
	if d := r.tick(time.Second); d == nil || d.Action != "blessing" || d.Spell != spKings {
		t.Fatalf("the blessing it has is renewed, not swapped: %+v", d)
	}
	// Only Devotion known: Devotion. No aura known: none.
	r, _ = newClassRig(t, "ret_paladin", ClassPaladin, 20, 0, 3000, Params{}, []uint32{spDevotionAur, spMight}, func(u *objects.Unit) { u.Auras = nil })
	if d := r.tick(time.Second); d == nil || d.Spell != spDevotionAur {
		t.Fatalf("%+v", d)
	}
	// Out of combat upkeep waits for a rest gate; an aura is also kept up in combat.
	r = bare(Params{})
	r.b.restOK = func() bool { return false }
	if d := r.tick(time.Second); d != nil {
		t.Fatalf("%+v", d)
	}
	r = bare(Params{})
	r.b.restOK = func() bool { return false }
	r.g.setSelf(func(u *objects.Unit) { u.InCombat = true })
	if d := r.tick(time.Second); d == nil || d.Action != "paladin_aura" {
		t.Fatalf("%+v", d)
	}
	for _, id := range casts(r.g) {
		if id == spMight {
			t.Fatalf("a blessing in combat: %v", casts(r.g))
		}
	}
}

func TestPaladinEmergencies(t *testing.T) {
	hurt := func(pct uint32, onPaladin bool, known ...uint32) (*rig, *meleeFake) {
		r, mf := newPaladinRig(t, Params{}, known...)
		setSeal(r.g, "Seal of Command", spSoCommand)
		meleeFight(r, mf)
		r.g.setSelf(func(u *objects.Unit) { u.InCombat = true })
		if onPaladin {
			onMe(r.g, mobA)
		}
		setHealth(r.g, pct)
		return r, mf
	}
	// 15%: Lay on Hands first, then (it is on cooldown now) Divine Protection, then the stun.
	r, _ := hurt(15, true)
	d := r.tick(100 * time.Millisecond)
	if d == nil || d.Action != "lay_on_hands" || d.Spell != spLayOnHands || d.Target != selfG || d.Rel != RelDispel {
		t.Fatalf("%+v idle %q", d, r.idle())
	}
	d = r.tick(1600 * time.Millisecond)
	if d == nil || d.Action != "divine_protection" || d.Spell != spDivineProt || d.Rel != relEmergency+1 {
		t.Fatalf("%+v idle %q", d, r.idle())
	}
	// Divine Protection pacifies the paladin: no attack, no stun, until it ends.
	addAura(r.g, "Divine Protection", spDivineProt, nil)
	before := len(r.g.castList())
	attacks := r.g.attacks
	if d := r.tick(1600 * time.Millisecond); d != nil {
		t.Fatalf("an attack while pacified: %+v", d)
	}
	if len(r.g.castList()) != before || r.g.attacks != attacks {
		t.Fatalf("casts %v", casts(r.g))
	}
	// 30%: Divine Protection; 40%: the stun; 60%: none of it. A mob on the tank: none of it either.
	r, _ = hurt(30, true)
	if d := r.tick(100 * time.Millisecond); d == nil || d.Action != "divine_protection" {
		t.Fatalf("%+v", d)
	}
	r, _ = hurt(40, true)
	d = r.tick(100 * time.Millisecond)
	if d == nil || d.Action != "hammer_of_justice" || d.Spell != spHammerJ || d.Target != mobA || d.Rel != relEmergency-1 {
		t.Fatalf("%+v idle %q", d, r.idle())
	}
	// A mob already stunned is not stunned again.
	r, _ = hurt(40, true)
	r.g.mutate(mobA, func(u *objects.Unit) {
		u.Auras = []objects.Aura{{Name: "Hammer of Justice", SpellID: spHammerJ, Harmful: true}}
	})
	if d := r.tick(100 * time.Millisecond); d != nil && d.Action == "hammer_of_justice" {
		t.Fatalf("%+v", d)
	}
	r, _ = hurt(60, true)
	r.tick(100 * time.Millisecond)
	for _, id := range casts(r.g) {
		if id == spHammerJ || id == spDivineProt || id == spLayOnHands {
			t.Fatalf("at 60%%: %v", casts(r.g))
		}
	}
	r, _ = hurt(30, false)
	r.tick(100 * time.Millisecond)
	for _, id := range casts(r.g) {
		if id == spHammerJ || id == spDivineProt {
			t.Fatalf("a mob on the tank: %v", casts(r.g))
		}
	}
}

func TestHammerOfJusticeInterruptsACast(t *testing.T) {
	r, mf := newPaladinRig(t, Params{})
	setSeal(r.g, "Seal of Command", spSoCommand)
	meleeFight(r, mf)
	caster := mob(mobB, tankG)
	caster.Casting = &objects.Cast{SpellID: spShadowBolt, EndsInMS: ms(1500), Target: tankG}
	r.g.addUnit(caster, 6, 0)
	d := r.tick(100 * time.Millisecond)
	if d == nil || d.Action != "interrupt" || d.Spell != spHammerJ || d.Target != mobB || d.Rel != RelInterrupt {
		t.Fatalf("%+v idle %q", d, r.idle())
	}
	// Hammer of Justice reaches 10 yards.
	r, mf = newPaladinRig(t, Params{})
	setSeal(r.g, "Seal of Command", spSoCommand)
	meleeFight(r, mf)
	far := mob(mobB, tankG)
	far.Casting = &objects.Cast{SpellID: spShadowBolt, EndsInMS: ms(1500), Target: tankG}
	r.g.addUnit(far, 15, 0)
	if d := r.tick(100 * time.Millisecond); d != nil && d.Action == "interrupt" {
		t.Fatalf("%+v", d)
	}
}

func TestPaladinDrinksAtRestOnly(t *testing.T) {
	r, _ := newPaladinRig(t, Params{})
	r.g.setSelf(func(u *objects.Unit) { u.Power = 1200 })
	r.g.items = []objects.Item{{Entry: 1645, Count: 5}} // Moonberry Juice, level 35
	d := r.tick(time.Second)
	if d == nil || d.Action != "drink" || len(r.g.used) != 1 || r.g.used[0] != 1645 {
		t.Fatalf("%+v used %v idle %q", d, r.g.used, r.idle())
	}
	// A mob to fight, or a closed rest gate: no drink.
	r, mf := newPaladinRig(t, Params{})
	r.g.setSelf(func(u *objects.Unit) { u.Power = 1200 })
	r.g.items = []objects.Item{{Entry: 1645, Count: 5}}
	meleeFight(r, mf)
	r.g.setSelf(func(u *objects.Unit) { u.InCombat = true })
	r.tick(2200 * time.Millisecond)
	if len(r.g.used) != 0 {
		t.Fatalf("drank in a fight: %v", r.g.used)
	}
	r, _ = newPaladinRig(t, Params{})
	r.b.restOK = func() bool { return false }
	r.g.setSelf(func(u *objects.Unit) { u.Power = 1200 })
	r.g.items = []objects.Item{{Entry: 1645, Count: 5}}
	if d := r.tick(time.Second); d != nil {
		t.Fatalf("%+v", d)
	}
}

func TestPaladinStaysBehindTheMob(t *testing.T) {
	r, mf := newPaladinRig(t, Params{})
	tankFights(r.g, 8, 4, math.Pi)
	mf.setSwing(mobA)
	setSeal(r.g, "Seal of Command", spSoCommand)
	d := r.tick(100 * time.Millisecond)
	if d == nil || d.Action != "melee_reposition" || d.Rel != RelMove {
		t.Fatalf("%+v idle %q", d, r.idle())
	}
	m := r.g.units[1]
	if m.GUID != mobA {
		m = r.g.units[0]
	}
	if w := mf.walkList(); len(w) != 1 || !behind(m, w[0]) {
		t.Fatalf("walked to %v, not behind the mob at (%.0f,%.0f)", w, m.X, m.Y)
	}
}

func TestPaladinSpellData(t *testing.T) {
	for _, c := range []struct {
		id     uint32
		name   string
		level  uint8
		cost   uint32 // flat mana
		rngMax float32
	}{
		{spSoCommand, "Seal of Command", 50, 180, 0}, {spSoRight, "Seal of Righteousness", 50, 170, 0}, {spSoCrusader, "Seal of the Crusader", 42, 125, 0},
		{spSoWisdom, "Seal of Wisdom", 48, 170, 0}, {spJudgement, "Judgement", 4, 0, 10}, {spCrusStrike, "Crusader Strike", 46, 100, 5},
		{spConsecrate, "Consecration", 50, 390, 0}, {spHammerJ, "Hammer of Justice", 40, 75, 10}, {spLayOnHands, "Lay on Hands", 50, 0, 40},
		{spDivineProt, "Divine Protection", 18, 35, 0}, {spRetribAura, "Retribution Aura", 46, 0, 0}, {spDevotionAur, "Devotion Aura", 50, 0, 0},
		{spMight, "Blessing of Might", 42, 85, 30}, {spKings, "Blessing of Kings", 20, 0, 30},
	} {
		if got := spells.Name(c.id); got != c.name {
			t.Errorf("%d is %q, want %q", c.id, got, c.name)
			continue
		}
		m, ok := spells.Lookup(c.id)
		if !ok || m.Level != c.level || m.Cost != c.cost || m.RangeMax != c.rngMax || m.Power != spells.PowerMana {
			t.Errorf("%s: %+v", c.name, m)
		}
		if m.Flags.Has(spells.FlagStealthOnly) || m.Flags.Has(spells.FlagNotInCombat) {
			t.Errorf("%s: %v", c.name, m.Flags.Names())
		}
	}
	// Judgement and Kings cost a percent of the base mana (6 and 8); Judgement has a 10 s cooldown of its own, Hammer of Justice a
	// 60 s category one, Consecration 8 s, Crusader Strike 6 s, Lay on Hands an hour.
	if m, _ := spells.Lookup(spJudgement); m.CostPct != 6 || m.CooldownMS != 10000 {
		t.Errorf("Judgement: %+v", m)
	}
	if m, _ := spells.Lookup(spKings); m.CostPct != 8 {
		t.Errorf("Blessing of Kings: %+v", m)
	}
	for id, cd := range map[uint32]uint32{spHammerJ: 60000, spConsecrate: 8000, spCrusStrike: 6000, spLayOnHands: 3600000, spDivineProt: 300000} {
		if m, _ := spells.Lookup(id); m.CategoryCDMS != cd {
			t.Errorf("%s: category cooldown %d, want %d", spells.Name(id), m.CategoryCDMS, cd)
		}
	}
	// Hammer of Justice is a stun (mechanic 12, SPELL_AURA_MOD_STUN 12): it breaks a cast and is what the class table interrupts with.
	if m, _ := spells.Lookup(spHammerJ); m.Mechanic != 12 || m.Effects[0].Aura != 12 {
		t.Errorf("Hammer of Justice: %+v", m)
	}
	if ci := ClassOf(ClassPaladin); len(ci.Interrupts) != 1 || ci.Interrupts[0].Spell != "Hammer of Justice" || len(ci.Interrupts[0].Stances) != 0 || !ci.Drinks {
		t.Errorf("%+v", ci)
	}
	// Divine Protection pacifies (SPELL_AURA_MOD_PACIFY, 25): the brain holds its attacks while it lasts.
	if m, _ := spells.Lookup(spDivineProt); m.Effects[0].Aura != 25 {
		t.Errorf("Divine Protection: %+v", m)
	}
	// The debuffs and buffs the brain looks for by name are called that in the data.
	for id, name := range map[uint32]string{21183: "Judgement of the Crusader", 20188: "Judgement of the Crusader", 20186: "Judgement of Wisdom",
		498: "Divine Protection", 642: "Divine Shield", 853: "Hammer of Justice", 20375: "Seal of Command", 20154: "Seal of Righteousness"} {
		if spells.Name(id) != name {
			t.Errorf("%d is %q, want %q", id, spells.Name(id), name)
		}
	}
	// A seal is a 30 s buff; a blessing 10 minutes.
	for _, id := range []uint32{spSoCommand, spSoRight, spSoCrusader, spSoWisdom} {
		if m, _ := spells.Lookup(id); m.DurationMS != 30000 || !m.Flags.Has(spells.FlagAura) {
			t.Errorf("%s: %+v", spells.Name(id), m)
		}
	}
	for _, id := range []uint32{spMight, spKings} {
		if m, _ := spells.Lookup(id); m.DurationMS != 600000 {
			t.Errorf("%s: %+v", spells.Name(id), m)
		}
	}
	assertNoStances(t, "paladin", []uint32{20154, 20287, 20288, 20289, 20290, 20291, 20292, 20293, 20375, 20915, 20918, 20919, 20920, 20162, 20305, 20306, 20307,
		20308, 21082, 20166, 20356, 20357, 51745, 51746, 20271, 2537, 8823, 8824, 10336, 10337, 20922, 20923, 20924, 26573, 20116, 853, 5588, 5589, 10308,
		633, 2800, 10310, 498, 5573, 7294, 10298, 10299, 10300, 10301, 465, 10290, 643, 10291, 1032, 10292, 10293, 19740, 19834, 19835, 19836, 19837, 19838,
		25291, 20217})
}

func TestPaladinManaFloor(t *testing.T) {
	// Below 30% mana a Seal of Command is not judged (Judgement and the next seal cost mana); a Seal of Wisdom is.
	run := func(seal string, id uint32, mana uint32, known ...uint32) (*rig, *meleeFake) {
		r, mf := newPaladinRig(t, Params{}, known...)
		r.g.setSelf(func(u *objects.Unit) { u.Power = mana })
		setSeal(r.g, seal, id)
		tankFights(r.g, 7, 4, 0)
		mf.setSwing(mobA)
		r.tick(100 * time.Millisecond)
		return r, mf
	}
	r, _ := run("Seal of Command", spSoCommand, 700) // 23%
	if d := open(r); d != nil && d.Action == "judgement" {
		t.Fatalf("Judgement of Command at 23%% mana: %+v", d)
	}
	r, _ = run("Seal of Wisdom", spSoWisdom, 700)
	if d := open(r); d == nil || d.Action != "judgement" {
		t.Fatalf("Judgement of Wisdom at 23%% mana: %+v idle %q", d, r.idle())
	}
	// Crusader Strike waits below 15%; a seal other than Wisdom is not cast there, Wisdom is.
	r, _ = run("Seal of Command", spSoCommand, 300)
	if d := open(r); d != nil && d.Action == "crusader_strike" {
		t.Fatalf("Crusader Strike at 10%% mana: %+v", d)
	}
	r, _ = run("", 0, 300, spSoCommand, spSoRight, spJudgement, spCrusStrike)
	if d := open(r); d != nil && d.Action == "seal" {
		t.Fatalf("a Seal of Command at 10%% mana: %+v", d)
	}
	r, _ = run("", 0, 300, spSoCommand, spSoWisdom, spJudgement)
	if d := r.tick(1600 * time.Millisecond); d == nil || d.Action != "seal" || d.Spell != spSoWisdom {
		t.Fatalf("%+v idle %q", d, r.idle())
	}
	// Above the thresholds nothing changes.
	r, _ = run("Seal of Command", spSoCommand, 2000)
	if d := open(r); d == nil || d.Action != "judgement" {
		t.Fatalf("%+v", d)
	}
}
