package brain

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/pjerra/turtle-agent/internal/objects"
	"github.com/pjerra/turtle-agent/internal/packet"
	"github.com/pjerra/turtle-agent/internal/spells"
)

// Spells of the warrior tests (ids from internal/spells/meta.tsv.gz; TestWarriorSpellData checks the facts the brain relies on).
const (
	spBattleSt    = 2457
	spDefSt       = 71
	spBerserkSt   = 2458
	spHeroic      = 78
	spBloodrage   = 2687
	spBattleShout = 6673
	spExecute     = 5308
	spOverpower   = 7384
	spMortal      = 12294
	spRend        = 772
	spSweeping    = 12292
	spCleave      = 845
	spCharge      = 100
	spHamstring   = 1715
	spBloodthirst = 23881
	spWhirlwind   = 1680
	spBerserkRage = 18499
	spIntercept   = 20252
	spPummel      = 6552
	spPummelT     = 13491 // the Turtle rank 1 of level 6, Battle Stance only
	spShieldBash  = 72
	spTaunt       = 355
	spSunder      = 7386
	spRevenge     = 6572
	spShieldBlock = 2565
	spThunder     = 6343
	spDemo        = 1160
	spFear        = 5782
	healG         = packet.GUID(3)
)

var warriorSpells = []uint32{spBattleSt, spDefSt, spBerserkSt, spHeroic, spBloodrage, spBattleShout, spExecute, spOverpower, spMortal, spRend,
	spSweeping, spCleave, spCharge, spHamstring, spBloodthirst, spWhirlwind, spBerserkRage, spIntercept, spPummel, spShieldBash, spTaunt,
	spSunder, spRevenge, spShieldBlock, spThunder, spDemo}

// meleeFake is the fake game with the melee half of the Game: the swings' outcomes the test scripts, and an auto attack that
// stays on its target.
type meleeFake struct {
	*posFake
	dodged  map[packet.GUID]time.Time
	avoided time.Time
	swing   packet.GUID
	mmu     sync.Mutex
}

func (f *meleeFake) LastDodge(t packet.GUID) time.Time {
	f.mmu.Lock()
	defer f.mmu.Unlock()
	return f.dodged[t]
}

func (f *meleeFake) LastAvoided() time.Time {
	f.mmu.Lock()
	defer f.mmu.Unlock()
	return f.avoided
}

func (f *meleeFake) Swinging() packet.GUID {
	f.mmu.Lock()
	defer f.mmu.Unlock()
	return f.swing
}

func (f *meleeFake) setSwing(g packet.GUID) {
	f.mmu.Lock()
	f.swing = g
	f.mmu.Unlock()
}

func (f *meleeFake) dodge(t packet.GUID, at time.Time) {
	f.mmu.Lock()
	f.dodged[t] = at
	f.mmu.Unlock()
}

func (f *meleeFake) Attack(ctx context.Context) error {
	f.fakeGame.mu.Lock()
	f.attacks++
	sel := f.selected
	f.fakeGame.mu.Unlock()
	f.setSwing(sel)
	return nil
}

func stanceAura(name string, id uint32) objects.Aura { return objects.Aura{Name: name, SpellID: id} }

// newWarriorRig is a warrior of level 40 in the stance, with Battle Shout up and no rage, on a fake game that can walk, see and
// follow the swings; it knows the spells given (all the warrior spells of the tests when none).
func newWarriorRig(t *testing.T, preset, stance string, p Params, known ...uint32) (*rig, *meleeFake) {
	t.Helper()
	if len(known) == 0 {
		known = warriorSpells
	}
	g := newFake(known...)
	g.self.Class, g.self.Level = ClassWarrior, 40
	g.self.PowerType, g.self.Power, g.self.MaxPower = 1, 0, 1000
	g.self.Health, g.self.MaxHealth = 1200, 1200
	g.self.Auras = []objects.Aura{stanceAura("Battle Shout", spBattleShout)}
	if stance != "" {
		g.self.Auras = append(g.self.Auras, stanceAura(stance, stanceSpells[stance]))
	}
	mf := &meleeFake{posFake: &posFake{fakeGame: g}, dodged: map[packet.GUID]time.Time{}}
	cfg := DefaultConfig()
	cfg.Now = g.clk.Now
	if p.Strategies == nil {
		p.Strategies = []string{preset}
	}
	b, err := New(mf, cfg, p)
	if err != nil {
		t.Fatal(err)
	}
	return &rig{t: t, g: g, b: b}, mf
}

func setRage(g *fakeGame, tenths uint32) { g.setSelf(func(u *objects.Unit) { u.Power = tenths }) }

func setMobHP(g *fakeGame, id packet.GUID, pct uint32) {
	g.mutate(id, func(u *objects.Unit) { u.Health = u.MaxHealth * pct / 100 })
}

// meleeFight is the standard fight: the tank holds a mob 4 yards east of the warrior, the mob faces away from the warrior (east),
// so the warrior stands behind it. The fight is registered on the first tick; the opening delay is over 2.1 s later.
func meleeFight(r *rig, mf *meleeFake) {
	tankFights(r.g, 7, 4, 0)
	mf.setSwing(mobA)
	r.tick(100 * time.Millisecond)
}

// open lets the opening delay pass and runs the first tick after it.
func open(r *rig) *Decision { return r.tick(2200 * time.Millisecond) }

func casts(g *fakeGame) []uint32 { return spellSeq(g) }

func TestArmsWaitsForTheTankThenSwingsAndStrikes(t *testing.T) {
	r, mf := newWarriorRig(t, "arms_warrior", stanceBattle, Params{})
	setRage(r.g, 600)
	tankFights(r.g, 7, 4, 0)
	if d := r.tick(100 * time.Millisecond); d != nil || r.idle() != "vetoed:opening_delay" {
		t.Fatalf("it opened before the tank held the mob: %+v idle %q", d, r.idle())
	}
	d := open(r)
	if d == nil || d.Action != "auto_attack" || d.Rel != RelNormal+9 || len(mf.targets) != 1 || mf.targets[0] != mobA || mf.attacks != 1 {
		t.Fatalf("%+v targets %v attacks %d", d, mf.targets, mf.attacks)
	}
	if d := r.tick(1600 * time.Millisecond); d == nil || d.Action != "mortal_strike" || d.Spell != spMortal || d.Target != mobA {
		t.Fatalf("%+v", d)
	}
	if d := r.tick(1600 * time.Millisecond); d == nil || d.Action != "rend" || d.Spell != spRend {
		t.Fatalf("%+v", d)
	}
	r.g.mutate(mobA, func(u *objects.Unit) { u.Auras = []objects.Aura{{Name: "Rend", SpellID: spRend, Harmful: true}} })
	if d := r.tick(1600 * time.Millisecond); d == nil || d.Action != "heroic_strike" || d.Spell != spHeroic || d.Trigger != "rage_dump" {
		t.Fatalf("rage 60 and nothing else to spend it on: %+v idle %q", d, r.idle())
	}
	if d := r.tick(400 * time.Millisecond); d != nil {
		t.Fatalf("the queued Heroic Strike was sent twice: %+v", d)
	}
	if got := casts(r.g); !eq(got, []uint32{spMortal, spRend, spHeroic}) {
		t.Fatalf("casts %v", got)
	}
	if len(mf.walkList()) != 0 {
		t.Fatalf("it stands behind the mob and had no reason to walk: %v", mf.walkList())
	}
	if mf.attacks != 1 {
		t.Fatalf("the auto attack was sent again while swinging: %d", mf.attacks)
	}
}

func TestRageDecidesWhatTheWarriorCanDo(t *testing.T) {
	// 10 rage: Mortal Strike (30) is out of reach, Rend (10) is not.
	r, mf := newWarriorRig(t, "arms_warrior", stanceBattle, Params{})
	meleeFight(r, mf)
	setRage(r.g, 100)
	if d := open(r); d == nil || d.Action != "rend" {
		t.Fatalf("%+v idle %q", d, r.idle())
	}
	// No rage: nothing to cast, only the swing (Bloodrage is the one thing that costs none; it is not known here).
	var noBloodrage []uint32
	for _, id := range warriorSpells {
		if id != spBloodrage {
			noBloodrage = append(noBloodrage, id)
		}
	}
	r, _ = newWarriorRig(t, "arms_warrior", stanceBattle, Params{}, noBloodrage...)
	tankFights(r.g, 7, 4, 0)
	r.tick(100 * time.Millisecond)
	if d := open(r); d == nil || d.Action != "auto_attack" {
		t.Fatalf("%+v", d)
	}
	for i := 0; i < 3; i++ {
		if d := r.tick(1600 * time.Millisecond); d != nil {
			t.Fatalf("with no rage: %+v", d)
		}
	}
	if len(casts(r.g)) != 0 {
		t.Fatalf("casts %v", casts(r.g))
	}
	// The dump starts at 50 rage, not before.
	r, mf = newWarriorRig(t, "arms_warrior", stanceBattle, Params{}, spBattleSt, spHeroic)
	meleeFight(r, mf)
	setRage(r.g, 490)
	if d := open(r); d != nil {
		t.Fatalf("49 rage: %+v", d)
	}
	setRage(r.g, 500)
	if d := r.tick(300 * time.Millisecond); d == nil || d.Action != "heroic_strike" {
		t.Fatalf("50 rage: %+v", d)
	}
}

func TestExecutePhaseComesFirst(t *testing.T) {
	r, mf := newWarriorRig(t, "arms_warrior", stanceBattle, Params{})
	meleeFight(r, mf)
	setRage(r.g, 600)
	setMobHP(r.g, mobA, 50)
	if d := open(r); d == nil || d.Action != "mortal_strike" {
		t.Fatalf("at 50%%: %+v", d)
	}
	setMobHP(r.g, mobA, 15)
	if d := r.tick(1600 * time.Millisecond); d == nil || d.Action != "execute" || d.Rel != RelNormal+8 || d.Spell != spExecute || d.Trigger != "mob_low" {
		t.Fatalf("at 15%%: %+v idle %q", d, r.idle())
	}
	// 20 percent exactly is not the execute phase.
	r, mf = newWarriorRig(t, "arms_warrior", stanceBattle, Params{})
	meleeFight(r, mf)
	setRage(r.g, 600)
	setMobHP(r.g, mobA, 20)
	if d := open(r); d == nil || d.Action != "mortal_strike" {
		t.Fatalf("at 20%%: %+v", d)
	}
}

func TestOverpowerAnswersADodgeInBattleStanceOnly(t *testing.T) {
	r, mf := newWarriorRig(t, "arms_warrior", stanceBattle, Params{})
	meleeFight(r, mf)
	setRage(r.g, 600)
	mf.dodge(mobA, r.g.clk.Now().Add(1500*time.Millisecond)) // 0.7 s before the tick
	if d := open(r); d == nil || d.Action != "overpower" || d.Rel != RelNormal+7 || d.Spell != spOverpower {
		t.Fatalf("%+v", d)
	}
	// An old dodge is no window.
	r, mf = newWarriorRig(t, "arms_warrior", stanceBattle, Params{})
	meleeFight(r, mf)
	setRage(r.g, 600)
	mf.dodge(mobA, r.g.clk.Now().Add(-10*time.Second))
	if d := open(r); d == nil || d.Action != "mortal_strike" {
		t.Fatalf("%+v", d)
	}
	// In Berserker Stance there is no Overpower, however fresh the dodge.
	r, mf = newWarriorRig(t, "arms_warrior", stanceBerserker, Params{})
	meleeFight(r, mf)
	setRage(r.g, 100)
	mf.dodge(mobA, r.g.clk.Now().Add(1500*time.Millisecond))
	open(r)
	for i := 0; i < 3; i++ {
		r.tick(1600 * time.Millisecond)
	}
	for _, id := range casts(r.g) {
		if id == spOverpower {
			t.Fatal("Overpower was sent from Berserker Stance")
		}
	}
}

func TestWarriorKeepsItsHomeStanceAndChangesItOnlyWhenCheap(t *testing.T) {
	// Out of combat, in the wrong stance: back to Battle at once, but not again within the stance cooldown.
	r, _ := newWarriorRig(t, "arms_warrior", stanceBerserker, Params{})
	d := r.tick(time.Second)
	if d == nil || d.Action != "stance_arms" || d.Spell != spBattleSt {
		t.Fatalf("%+v", d)
	}
	if d := r.tick(time.Second); d != nil {
		t.Fatalf("the stance change repeated within its cooldown: %+v", d)
	}
	if d := r.tick(1500 * time.Millisecond); d == nil || d.Action != "stance_arms" {
		t.Fatalf("still in the wrong stance after 2 s: %+v", d)
	}
	// In combat with 40 rage a stance change would cost it: the warrior keeps the stance and fights in it.
	r, mf := newWarriorRig(t, "arms_warrior", stanceBerserker, Params{}, spBattleSt, spBerserkSt, spBattleShout, spMortal)
	r.g.setSelf(func(u *objects.Unit) { u.Power = 400; u.InCombat = true })
	meleeFight(r, mf)
	open(r)
	for _, id := range casts(r.g) {
		if id == spBattleSt {
			t.Fatal("it threw away 40 rage for a stance")
		}
	}
	// With 10 rage left it goes home.
	setRage(r.g, 100)
	var went bool
	for i := 0; i < 4 && !went; i++ {
		if d := r.tick(1600 * time.Millisecond); d != nil && d.Action == "stance_arms" {
			went = true
		}
	}
	if !went {
		t.Fatalf("it never went home, casts %v", casts(r.g))
	}
	// Fury goes to Berserker Stance when it has it, to Battle Stance when it has not.
	r, _ = newWarriorRig(t, "fury_warrior", stanceBattle, Params{})
	if d := r.tick(time.Second); d == nil || d.Action != "stance_fury" || d.Spell != spBerserkSt {
		t.Fatalf("%+v", d)
	}
	r, _ = newWarriorRig(t, "fury_warrior", stanceDefensive, Params{}, spBattleSt, spDefSt, spBattleShout, spHeroic)
	if d := r.tick(time.Second); d == nil || d.Action != "stance_fury" || d.Spell != spBattleSt {
		t.Fatalf("below level 30: %+v", d)
	}
}

func TestRendOnlyWhereItCanPayOff(t *testing.T) {
	r, mf := newWarriorRig(t, "arms_warrior", stanceBattle, Params{}, spBattleSt, spRend, spBattleShout)
	meleeFight(r, mf)
	setRage(r.g, 300)
	setMobHP(r.g, mobA, 30)
	if d := open(r); d != nil {
		t.Fatalf("Rend on a mob about to die: %+v", d)
	}
	setMobHP(r.g, mobA, 90)
	r.g.mutate(mobA, func(u *objects.Unit) { u.Auras = []objects.Aura{{Name: "Rend", SpellID: spRend, Harmful: true}} })
	if d := r.tick(1600 * time.Millisecond); d != nil {
		t.Fatalf("a second Rend on a bleeding mob: %+v", d)
	}
	r.g.mutate(mobA, func(u *objects.Unit) { u.Auras = nil })
	if d := r.tick(1600 * time.Millisecond); d == nil || d.Action != "rend" {
		t.Fatalf("%+v", d)
	}
	// A mob that cannot bleed: the server says immune, and Rend is left alone for a while.
	r.g.st.CastRefused(spRend, codeOf(t, "immune"))
	r.g.st.Clear(spRend)
	if d := r.tick(1600 * time.Millisecond); d != nil {
		t.Fatalf("Rend again right after 'immune': %+v", d)
	}
}

func TestCleaveForAPackHeroicStrikeForOneAndNoCleaveOnALooseMob(t *testing.T) {
	setup := func(loose bool) (*rig, *meleeFake) {
		r, mf := newWarriorRig(t, "arms_warrior", stanceBattle, Params{}, spBattleSt, spHeroic, spCleave, spBattleShout)
		meleeFight(r, mf)
		setRage(r.g, 600)
		b := mob(mobB, tankG)
		if loose {
			b.Target = healG // attacks somebody the tank is not holding
		}
		b.O = 0
		r.g.addUnit(b, 4, 2)
		return r, mf
	}
	r, _ := setup(false)
	if d := open(r); d == nil || d.Action != "cleave" || d.Rel != RelNormal+1 || d.Spell != spCleave {
		t.Fatalf("two mobs on the tank: %+v", d)
	}
	r, _ = setup(true)
	if d := open(r); d == nil || d.Action != "heroic_strike" {
		t.Fatalf("a loose mob must veto the area ability: %+v", d)
	}
	// One mob: Heroic Strike.
	r, mf := newWarriorRig(t, "arms_warrior", stanceBattle, Params{}, spBattleSt, spHeroic, spCleave, spBattleShout)
	meleeFight(r, mf)
	setRage(r.g, 600)
	if d := open(r); d == nil || d.Action != "heroic_strike" {
		t.Fatalf("%+v", d)
	}
}

func TestSweepingStrikesWithTwoMobs(t *testing.T) {
	r, mf := newWarriorRig(t, "arms_warrior", stanceBattle, Params{}, spBattleSt, spSweeping, spBattleShout)
	meleeFight(r, mf)
	setRage(r.g, 600)
	r.g.addUnit(mob(mobB, tankG), 4, 2)
	if d := open(r); d == nil || d.Action != "sweeping_strikes" || d.Spell != spSweeping {
		t.Fatalf("%+v", d)
	}
	r.g.setSelf(func(u *objects.Unit) { u.Auras = append(u.Auras, stanceAura("Sweeping Strikes", spSweeping)) })
	if d := r.tick(1600 * time.Millisecond); d != nil {
		t.Fatalf("again while up: %+v", d)
	}
	// One mob is no reason for it.
	r, mf = newWarriorRig(t, "arms_warrior", stanceBattle, Params{}, spBattleSt, spSweeping, spBattleShout)
	meleeFight(r, mf)
	setRage(r.g, 600)
	if d := open(r); d != nil {
		t.Fatalf("%+v", d)
	}
}

func TestFuryRotationBloodthirstWhirlwindAndRageDump(t *testing.T) {
	r, mf := newWarriorRig(t, "fury_warrior", stanceBerserker, Params{})
	meleeFight(r, mf)
	setRage(r.g, 900)
	var seq []uint32
	if d := open(r); d != nil {
		seq = append(seq, d.Spell)
	}
	for i := 0; i < 2; i++ {
		if d := r.tick(1600 * time.Millisecond); d != nil {
			seq = append(seq, d.Spell)
		}
	}
	if !eq(seq, []uint32{spBloodthirst, spWhirlwind, spHeroic}) {
		t.Fatalf("sequence %v", seq)
	}
	// No Overpower, no Rend in Berserker Stance, whatever the mob does.
	mf.dodge(mobA, r.g.clk.Now())
	for i := 0; i < 3; i++ {
		r.tick(1600 * time.Millisecond)
	}
	for _, id := range casts(r.g) {
		if id == spOverpower || id == spRend {
			t.Fatalf("a Battle Stance ability from Berserker Stance: %v", casts(r.g))
		}
	}
}

func TestBerserkerRageBreaksAFear(t *testing.T) {
	r, mf := newWarriorRig(t, "fury_warrior", stanceBerserker, Params{})
	meleeFight(r, mf)
	setRage(r.g, 0)
	r.g.setSelf(func(u *objects.Unit) {
		u.Auras = append(u.Auras, objects.Aura{Name: "Fear", SpellID: spFear, Harmful: true})
	})
	d := r.tick(100 * time.Millisecond) // before the opening delay: it is no attack
	if d == nil || d.Action != "berserker_rage" || d.Spell != spBerserkRage || d.Rel != RelDispel-5 || d.Trigger != "feared" {
		t.Fatalf("%+v", d)
	}
	// Arms has no Berserker Rage: it is not in the stance.
	r, mf = newWarriorRig(t, "arms_warrior", stanceBattle, Params{})
	meleeFight(r, mf)
	r.g.setSelf(func(u *objects.Unit) {
		u.Auras = append(u.Auras, objects.Aura{Name: "Fear", SpellID: spFear, Harmful: true})
	})
	if d := r.tick(time.Second); d != nil && d.Spell == spBerserkRage {
		t.Fatalf("%+v", d)
	}
}

func TestInterruptPummelInBattleOrBerserkerShieldBashInBattleOrDefensive(t *testing.T) {
	cast := func(r *rig) {
		r.g.mutate(mobA, func(u *objects.Unit) {
			u.Casting = &objects.Cast{SpellID: spShadowBolt, EndsInMS: ms(1500), Target: tankG}
		})
	}
	try := func(preset, stance string, known ...uint32) (*rig, *Decision) {
		r, mf := newWarriorRig(t, preset, stance, Params{}, known...)
		meleeFight(r, mf)
		setRage(r.g, 500)
		cast(r)
		return r, r.tick(100 * time.Millisecond)
	}
	// Pummel (6552) works in Battle and Berserker Stance, by the core's data: fury in Berserker and arms in Battle both use it, first.
	for _, c := range []struct{ preset, stance string }{{"fury_warrior", stanceBerserker}, {"arms_warrior", stanceBattle}} {
		r, d := try(c.preset, c.stance)
		if d == nil || d.Action != "interrupt" || d.Spell != spPummel || d.Target != mobA || d.Rel != RelInterrupt {
			t.Fatalf("%s: %+v idle %q", c.stance, d, r.idle())
		}
	}
	// Shield Bash (Battle, Defensive) when Pummel is not there; not from Berserker Stance.
	if _, d := try("arms_warrior", stanceBattle, spBattleSt, spShieldBash, spBattleShout); d == nil || d.Spell != spShieldBash {
		t.Fatalf("%+v", d)
	}
	if r, d := try("tank_warrior", stanceDefensive, spDefSt, spShieldBash, spPummel, spBattleShout); d == nil || d.Spell != spShieldBash {
		t.Fatalf("a tank has Shield Bash, not Pummel: %+v idle %q", d, r.idle())
	}
	r, d := try("fury_warrior", stanceBerserker, spBerserkSt, spShieldBash, spBattleShout)
	if d != nil && d.Action == "interrupt" {
		t.Fatalf("Shield Bash from Berserker Stance: %+v", d)
	}
	for _, id := range casts(r.g) {
		if id == spShieldBash || id == spBattleSt {
			t.Fatalf("%v", casts(r.g))
		}
	}
	// The Turtle rank 13491 is Battle Stance only: used there, left alone in Berserker Stance.
	if _, d := try("arms_warrior", stanceBattle, spBattleSt, spPummelT, spBattleShout); d == nil || d.Spell != spPummelT {
		t.Fatalf("%+v", d)
	}
	r, d = try("fury_warrior", stanceBerserker, spBerserkSt, spPummelT, spBattleShout)
	if d != nil && d.Action == "interrupt" {
		t.Fatalf("%+v", d)
	}
	for _, id := range casts(r.g) {
		if id == spPummelT || id == spBattleSt {
			t.Fatalf("%v", casts(r.g))
		}
	}
	// A cast out of Pummel's reach is left to others.
	r, mf := newWarriorRig(t, "fury_warrior", stanceBerserker, Params{})
	meleeFight(r, mf)
	caster := mob(mobB, tankG)
	caster.Casting = &objects.Cast{SpellID: spShadowBolt, EndsInMS: ms(1500), Target: tankG}
	r.g.addUnit(caster, 12, 0)
	setRage(r.g, 500)
	if d := r.tick(100 * time.Millisecond); d != nil && d.Action == "interrupt" {
		t.Fatalf("Pummel from 12 yards: %+v", d)
	}
}

func TestChargeOutOfCombatAfterTheOpeningDelayAndOnlyIntoATankedPull(t *testing.T) {
	far := func(r *rig, tank bool) {
		if tank {
			tankFights(r.g, 17, 15, 0)
		} else {
			r.g.addUnit(mob(mobA, packet.GUID(0)), 15, 0)
			r.g.selected = mobA
		}
	}
	r, mf := newWarriorRig(t, "arms_warrior", stanceBattle, Params{})
	far(r, true)
	mf.setSwing(mobA)
	if d := r.tick(100 * time.Millisecond); d != nil || len(mf.walkList()) != 0 {
		t.Fatalf("it ran in on foot with Charge about to be allowed: %+v walks %v", d, mf.walkList())
	}
	d := open(r)
	if d == nil || d.Action != "charge" || d.Spell != spCharge || d.Target != mobA || d.Rel != RelMove+2 || d.Trigger != "mob_far_ooc" {
		t.Fatalf("%+v idle %q", d, r.idle())
	}
	if len(mf.walkList()) != 0 {
		t.Fatalf("walked as well: %v", mf.walkList())
	}
	// Charge is on its cooldown now: the warrior runs the rest on foot, to a spot behind the mob.
	d = r.tick(1600 * time.Millisecond)
	if d == nil || d.Action != "melee_reposition" || len(mf.walkList()) != 1 {
		t.Fatalf("%+v walks %v", d, mf.walkList())
	}

	// Never into a pull the tank has not engaged.
	r, mf = newWarriorRig(t, "arms_warrior", stanceBattle, Params{NoMove: true})
	far(r, false)
	mf.setSwing(mobA)
	r.tick(100 * time.Millisecond)
	r.tick(2200 * time.Millisecond)
	for _, id := range casts(r.g) {
		if id == spCharge {
			t.Fatal("Charge into an untanked mob")
		}
	}
	// Not in combat only: in combat Charge is refused by the game, so the warrior does not try.
	r, mf = newWarriorRig(t, "arms_warrior", stanceBattle, Params{NoMove: true})
	far(r, true)
	mf.setSwing(mobA)
	r.g.setSelf(func(u *objects.Unit) { u.InCombat = true })
	r.tick(100 * time.Millisecond)
	r.tick(2200 * time.Millisecond)
	for _, id := range casts(r.g) {
		if id == spCharge {
			t.Fatal("Charge in combat")
		}
	}
}

func TestInterceptInCombatFromBerserkerStance(t *testing.T) {
	r, mf := newWarriorRig(t, "fury_warrior", stanceBerserker, Params{})
	tankFights(r.g, 17, 15, 0)
	mf.setSwing(mobA)
	setRage(r.g, 300)
	r.g.setSelf(func(u *objects.Unit) { u.InCombat = true })
	r.tick(100 * time.Millisecond)
	d := open(r)
	if d == nil || d.Action != "intercept" || d.Spell != spIntercept || d.Trigger != "mob_far_in_combat" {
		t.Fatalf("%+v idle %q", d, r.idle())
	}
	if len(mf.walkList()) != 0 {
		t.Fatalf("%v", mf.walkList())
	}
}

func TestHamstringOnlyAMobThatRuns(t *testing.T) {
	r, mf := newWarriorRig(t, "arms_warrior", stanceBattle, Params{}, spBattleSt, spHamstring, spBattleShout)
	meleeFight(r, mf)
	setRage(r.g, 300)
	setMobHP(r.g, mobA, 24)
	if d := open(r); d != nil {
		t.Fatalf("a mob that stands: %+v", d)
	}
	r.g.mutate(mobA, func(u *objects.Unit) { u.Moving = true })
	if d := r.tick(1600 * time.Millisecond); d == nil || d.Action != "hamstring" || d.Spell != spHamstring {
		t.Fatalf("%+v", d)
	}
	// Healthy mobs walk about in a fight: no hamstring.
	r, mf = newWarriorRig(t, "arms_warrior", stanceBattle, Params{}, spBattleSt, spHamstring, spBattleShout)
	meleeFight(r, mf)
	setRage(r.g, 300)
	r.g.mutate(mobA, func(u *objects.Unit) { u.Moving = true })
	if d := open(r); d != nil {
		t.Fatalf("%+v", d)
	}
}

func TestBattleShoutAtRestBloodrageAtTheStart(t *testing.T) {
	r, _ := newWarriorRig(t, "arms_warrior", stanceBattle, Params{})
	r.g.setSelf(func(u *objects.Unit) {
		u.Auras = []objects.Aura{stanceAura("Battle Stance", spBattleSt)}
		u.Power = 100
	}) // no shout, 10 rage
	d := r.tick(time.Second)
	if d == nil || d.Action != "battle_shout" || d.Spell != spBattleShout || d.Target != selfG || d.Trigger != "shout_missing_ooc" {
		t.Fatalf("%+v", d)
	}
	// In a dungeon run upkeep waits for a rest gate.
	r, _ = newWarriorRig(t, "arms_warrior", stanceBattle, Params{RestGate: true})
	r.b.restOK = func() bool { return false } // the controller's gate: no rest gate open
	r.g.setSelf(func(u *objects.Unit) {
		u.Auras = []objects.Aura{stanceAura("Battle Stance", spBattleSt)}
		u.Power = 100
	})
	if d := r.tick(time.Second); d != nil {
		t.Fatalf("%+v", d)
	}
	// Bloodrage when a fight starts with no rage and health to spare; not when hurt.
	r, mf := newWarriorRig(t, "arms_warrior", stanceBattle, Params{})
	tankFights(r.g, 7, 4, 0)
	mf.setSwing(mobA)
	d = r.tick(100 * time.Millisecond)
	if d == nil || d.Action != "bloodrage" || d.Spell != spBloodrage || d.Rel != RelNormal+3 {
		t.Fatalf("%+v", d)
	}
	r, mf = newWarriorRig(t, "arms_warrior", stanceBattle, Params{})
	tankFights(r.g, 7, 4, 0)
	mf.setSwing(mobA)
	r.g.setSelf(func(u *objects.Unit) { u.Health = 500 })
	if d := r.tick(100 * time.Millisecond); d != nil {
		t.Fatalf("Bloodrage at 40%% health: %+v", d)
	}
}

// ---- the class tables and the facts the rotation relies on ----

func TestWarriorSpellData(t *testing.T) {
	// name, id, level, power cost in tenths, range max: read off the core's spell_template when the table was made.
	for _, c := range []struct {
		id    uint32
		name  string
		level uint8
		cost  uint32
		rng   float32
	}{
		{spBattleSt, "Battle Stance", 1, 0, 0}, {spDefSt, "Defensive Stance", 10, 0, 0}, {spBerserkSt, "Berserker Stance", 30, 0, 0},
		{spHeroic, "Heroic Strike", 1, 150, 5}, {spCleave, "Cleave", 20, 200, 5}, {spRend, "Rend", 4, 100, 5}, {spOverpower, "Overpower", 12, 50, 5},
		{spMortal, "Mortal Strike", 40, 300, 5}, {spBloodthirst, "Bloodthirst", 40, 300, 5}, {spWhirlwind, "Whirlwind", 36, 250, 0},
		{spExecute, "Execute", 24, 150, 5}, {spHamstring, "Hamstring", 8, 100, 5}, {spCharge, "Charge", 4, 0, 25}, {spIntercept, "Intercept", 30, 100, 25},
		{spBerserkRage, "Berserker Rage", 32, 0, 0}, {spPummel, "Pummel", 38, 100, 5}, {spShieldBash, "Shield Bash", 12, 100, 5},
		{spTaunt, "Taunt", 10, 0, 5}, {spSunder, "Sunder Armor", 10, 100, 5}, {spRevenge, "Revenge", 14, 50, 5}, {spShieldBlock, "Shield Block", 16, 100, 0},
		{spThunder, "Thunder Clap", 6, 200, 0}, {spDemo, "Demoralizing Shout", 14, 100, 0}, {spBattleShout, "Battle Shout", 1, 100, 0},
		{spBloodrage, "Bloodrage", 10, 0, 0}, {spSweeping, "Sweeping Strikes", 1, 200, 0},
	} {
		if got := spells.Name(c.id); got != c.name {
			t.Errorf("%d is %q, want %q", c.id, got, c.name)
			continue
		}
		m, ok := spells.Lookup(c.id)
		if !ok || m.Level != c.level || m.Cost != c.cost || m.RangeMax != c.rng {
			t.Errorf("%s: %+v", c.name, m)
		}
		if c.cost > 0 && m.Power != spells.PowerRage {
			t.Errorf("%s is not paid in rage: %+v", c.name, m)
		}
	}
	// The ranges the gap closers and the interrupts rely on.
	if m, _ := spells.Lookup(spCharge); m.RangeMin != 8 || !m.Flags.Has(spells.FlagNotInCombat) {
		t.Errorf("Charge: %+v", m)
	}
	if m, _ := spells.Lookup(spIntercept); m.RangeMin != 8 || m.RangeMax != 25 {
		t.Errorf("Intercept: %+v", m)
	}
	// Fear's mechanic is what feared() looks for.
	if m, _ := spells.Lookup(spFear); m.Mechanic != 5 {
		t.Errorf("Fear: %+v", m)
	}
	// The stance table has the casting spell of each stance, whatever else is called by its name.
	for name, id := range stanceSpells {
		if spells.Name(id) != name {
			t.Errorf("stance %q: id %d is %q", name, id, spells.Name(id))
		}
	}
}

func TestClassTables(t *testing.T) {
	if ci := ClassOf(0); ci.ID != ClassMage {
		t.Fatalf("an unread class must act as the mage the brain started with: %+v", ci)
	}
	for id, ci := range classTable {
		if ci.ID != id || ci.Name == "" {
			t.Errorf("%d: %+v", id, ci)
		}
		for _, is := range ci.Interrupts {
			found := false
			for sid := uint32(1); sid < 60000 && !found; sid++ {
				found = spells.Name(sid) == is.Spell
			}
			if !found {
				t.Errorf("%s interrupts with %q, which no spell is called", ci.Name, is.Spell)
			}
		}
	}
	if ClassOf(ClassWarrior).Power != powerRage || ClassOf(ClassMage).Power != powerMana || ClassOf(ClassRogue).Power != powerEnergy {
		t.Fatal("power types")
	}
	if len(drinksFor(ClassOf(ClassWarrior))) != 0 || len(drinksFor(ClassOf(ClassRogue))) != 0 {
		t.Fatal("rage and energy classes do not drink")
	}
	if n := len(drinksFor(ClassOf(ClassPriest))); n != len(vendorDrinks) {
		t.Fatalf("a priest drinks the vendor drinks only: %d", n)
	}
	if n := len(drinksFor(ClassOf(ClassMage))); n != len(conjuredDrinks)+len(vendorDrinks) {
		t.Fatalf("a mage drinks its own water first: %d", n)
	}
	if got := drinksFor(ClassOf(ClassMage))[0].entry; got != conjuredDrinks[0].entry {
		t.Fatalf("the best drink of a mage is the conjured one: %d", got)
	}
}

// The drink a class picks from the same bags: the mage takes its conjured water, the priest only what a vendor sells.
func TestDrinkTableByClass(t *testing.T) {
	g := newFake()
	g.items = []objects.Item{{Entry: 8077, Count: 5}, {Entry: 1645, Count: 5}} // Conjured Mineral Water (35), Moonberry Juice (35)
	g.self.Level = 40
	r := newRig(t, g, Params{Strategies: []string{"frost_mage"}})
	c := r.b.newCtx(g.clk.Now())
	if e := drinkEntry(c, true); e != 8077 {
		t.Fatalf("mage: %d", e)
	}
	g.self.Class = ClassPriest
	c = r.b.newCtx(g.clk.Now())
	if e := drinkEntry(c, true); e != 1645 {
		t.Fatalf("priest: %d", e)
	}
	g.self.Class = ClassWarrior
	c = r.b.newCtx(g.clk.Now())
	if e := drinkEntry(c, true); e != 0 {
		t.Fatalf("warrior: %d", e)
	}
}

// coreStances is what the core's spell_template says about the stances of every player rank of the gated warrior abilities: the
// `stances` bitmask (0x10000 Battle, 0x20000 Defensive, 0x40000 Berserker) of the rows in tortoise-wow 5d79486,
// sql/base/tw_world_spell_template.sql; no file of sql/database_updates/world sets `stances` for these spells. B Battle, D Defensive,
// Z Berserker. Thunder Clap works in Battle and Defensive Stance in this data, all six ranks.
var coreStances = map[string]struct {
	stances string
	ids     []uint32
}{
	"Rend":             {"BD", []uint32{772, 6546, 6547, 6548, 11572, 11573, 11574}},
	"Execute":          {"BZ", []uint32{5308, 20658, 20660, 20661, 20662}},
	"Hamstring":        {"BZ", []uint32{1715, 7372, 7373}},
	"Overpower":        {"B", []uint32{7384, 7887, 11584, 11585}},
	"Revenge":          {"D", []uint32{6572, 6574, 7379, 11600, 11601, 25288}},
	"Charge":           {"B", []uint32{100, 6178, 11578}},
	"Intercept":        {"Z", []uint32{20252, 20616, 20617}},
	"Taunt":            {"D", []uint32{355}},
	"Thunder Clap":     {"BD", []uint32{6343, 8198, 8204, 8205, 11580, 11581}},
	"Sweeping Strikes": {"B", []uint32{12292}},
	"Shield Block":     {"D", []uint32{2565}},
	"Berserker Rage":   {"Z", []uint32{18499}},
	"Whirlwind":        {"Z", []uint32{1680}},
	"Shield Bash":      {"BD", []uint32{72, 1671, 1672}},
	"Pummel":           {"BZ", []uint32{6552}},
}

func stanceLetters(names []string) string {
	out := ""
	for _, c := range []struct{ name, letter string }{{stanceBattle, "B"}, {stanceDefensive, "D"}, {stanceBerserker, "Z"}} {
		for _, n := range names {
			if n == c.name {
				out += c.letter
			}
		}
	}
	return out
}

func TestStanceGatesFollowTheCoreData(t *testing.T) {
	for name, c := range coreStances {
		for _, id := range c.ids {
			if spells.Name(id) != name {
				t.Errorf("%d is %q, want %q", id, spells.Name(id), name)
			}
		}
		var have []string
		switch name {
		case "Shield Bash", "Pummel":
			for _, is := range classTable[ClassWarrior].Interrupts {
				if is.Spell == name {
					have = is.Stances
				}
			}
		default:
			have = warriorStances[name]
		}
		if got := stanceLetters(have); got != c.stances {
			t.Errorf("%s: the brain gates it to %q, the core's data says %q", name, got, c.stances)
		}
	}
	// The Turtle rank of Pummel is Battle Stance only.
	if got := stanceLetters(SpellStances[spPummelT]); got != "B" || spells.Name(spPummelT) != "Pummel" {
		t.Errorf("Pummel %d: %q", spPummelT, got)
	}
	// The abilities that work in every stance are not gated.
	for _, n := range []string{"Mortal Strike", "Bloodthirst", "Heroic Strike", "Cleave", "Sunder Armor", "Demoralizing Shout", "Battle Shout", "Bloodrage"} {
		if _, gated := warriorStances[n]; gated {
			t.Errorf("%s is gated, the data gates it to no stance", n)
		}
	}
}

// A mob that attacks the leader from afar (a caster, an archer) is walked up to, not stood in front of: the too-far check comes
// before "a mob on the player is the tank's business".
func TestMeleeWalksUpToAMobOnItThatKeepsItsDistance(t *testing.T) {
	r, mf := newWarriorRig(t, "arms_warrior", stanceBattle, Params{})
	m := mob(mobA, selfG)
	r.g.addUnit(m, 20, 0)
	r.g.setSelf(func(u *objects.Unit) { u.InCombat = true })
	d := r.tick(100 * time.Millisecond)
	if d == nil || d.Action != "melee_reposition" || len(mf.walkList()) != 1 {
		t.Fatalf("%+v walks %v idle %q", d, mf.walkList(), r.idle())
	}
	if w := mf.walkList()[0]; dist2(w, Vec3{20, 0, 0}) > 4.7 {
		t.Fatalf("walked to %v, not up to the mob", w)
	}
}

// ... and a fury warrior in combat intercepts it at once: no tank holds this mob, but it has pulled the leader already.
func TestInterceptAMobOnTheLeaderWithoutATank(t *testing.T) {
	r, mf := newWarriorRig(t, "fury_warrior", stanceBerserker, Params{})
	r.g.addUnit(mob(mobA, selfG), 20, 0)
	mf.setSwing(mobA)
	setRage(r.g, 300)
	r.g.setSelf(func(u *objects.Unit) { u.InCombat = true })
	d := r.tick(100 * time.Millisecond)
	if d == nil || d.Action != "intercept" || d.Spell != spIntercept {
		t.Fatalf("%+v idle %q", d, r.idle())
	}
}
