package brain

import (
	"math"
	"testing"
	"time"

	"github.com/pjerra/turtle-agent/internal/objects"
	"github.com/pjerra/turtle-agent/internal/packet"
	"github.com/pjerra/turtle-agent/internal/spells"
)

// Spells of the hunter tests (ids from internal/spells/meta.tsv.gz; TestHunterSpellData checks the facts the rotation relies on).
const (
	spAutoShot   = 75
	spMark       = 14325 // rank 4, level 58
	spSerpent    = 13555 // rank 8, level 58
	spArcane     = 14286 // rank 7, level 52
	spMulti      = 14290 // rank 4, level 54
	spConcussive = 5116
	spHawk       = 14322 // rank 6, level 58
	spCheetah    = 5118
	spViper      = 45651 // Turtle, level 56
	spFeignDeath = 5384
	spRaptor     = 14266 // rank 8, level 56
	mobC         = packet.GUID(102)
)

var hunterSpells = []uint32{spAutoShot, spMark, spSerpent, spArcane, spMulti, spConcussive, spHawk, spCheetah, spViper, spFeignDeath, spRaptor}

// newHunterRig is a hunter of level 58 with 1000 mana and the Hawk up, on a fake game that can walk, see and follow the swings.
func newHunterRig(t *testing.T, p Params, known ...uint32) (*rig, *meleeFake) {
	t.Helper()
	if len(known) == 0 {
		known = hunterSpells
	}
	return newClassRig(t, "marksman_hunter", ClassHunter, 58, 0, 1000, p, known, func(u *objects.Unit) {
		u.Auras = []objects.Aura{{Name: "Aspect of the Hawk", SpellID: spHawk}}
	})
}

func setMana(g *fakeGame, n uint32) { g.setSelf(func(u *objects.Unit) { u.Power = n }) }

// rangedFight is the standard fight: the tank holds a mob 28 yards east of the hunter, the mob faces east, away from the hunter.
func rangedFight(r *rig) {
	tankFights(r.g, 25, 28, 0)
	r.tick(100 * time.Millisecond)
}

// markAndSting puts Hunter's Mark and Serpent Sting on the mob.
func markAndSting(g *fakeGame) {
	g.mutate(mobA, func(u *objects.Unit) {
		u.Auras = []objects.Aura{{Name: "Hunter's Mark", SpellID: spMark, Harmful: true}, {Name: "Serpent Sting", SpellID: spSerpent, Harmful: true}}
	})
}

func TestHunterShootsInOrderAndSendsAutoShotOnce(t *testing.T) {
	r, _ := newHunterRig(t, Params{})
	tankFights(r.g, 25, 28, 0)
	if d := r.tick(100 * time.Millisecond); d != nil || r.idle() != "vetoed:opening_delay" {
		t.Fatalf("it opened before the tank held the mob: %+v idle %q", d, r.idle())
	}
	d := open(r)
	if d == nil || d.Action != "auto_shot" || d.Spell != spAutoShot || d.Target != mobA || d.Rel != RelNormal+9 {
		t.Fatalf("%+v idle %q", d, r.idle())
	}
	// Mark first, then the sting, then the shot on its cooldown. Auto Shot is not sent again while it is taken to run.
	want := []struct {
		action string
		spell  uint32
		after  func()
	}{
		{"hunters_mark", spMark, func() {
			r.g.mutate(mobA, func(u *objects.Unit) {
				u.Auras = []objects.Aura{{Name: "Hunter's Mark", SpellID: spMark, Harmful: true}}
			})
		}},
		{"serpent_sting", spSerpent, func() { markAndSting(r.g) }},
		{"arcane_shot", spArcane, nil},
	}
	for _, w := range want {
		d := r.tick(1550 * time.Millisecond)
		if d == nil || d.Action != w.action || d.Spell != w.spell {
			t.Fatalf("want %s: %+v idle %q", w.action, d, r.idle())
		}
		if w.after != nil {
			w.after()
		}
	}
	if got := casts(r.g); !eq(got, []uint32{spAutoShot, spMark, spSerpent, spArcane}) {
		t.Fatalf("casts %v", got)
	}
}

func TestHunterManaDecidesWhichShotsFire(t *testing.T) {
	r, _ := newHunterRig(t, Params{})
	rangedFight(r)
	setMana(r.g, 150) // 15%: the sting and the arcane shot wait, the free Auto Shot and the mark do not
	open(r)
	r.tick(1550 * time.Millisecond)
	markAndSting(r.g)
	r.g.mutate(mobA, func(u *objects.Unit) {
		u.Auras = []objects.Aura{{Name: "Hunter's Mark", SpellID: spMark, Harmful: true}}
	})
	for i := 0; i < 3; i++ {
		r.tick(1550 * time.Millisecond)
	}
	for _, id := range casts(r.g) {
		if id == spSerpent || id == spArcane {
			t.Fatalf("a shot at 15%% mana: %v", casts(r.g))
		}
	}
	// A mob that will die soon gets neither the mark nor the sting.
	r, _ = newHunterRig(t, Params{})
	rangedFight(r)
	setMobHP(r.g, mobA, 30)
	open(r)
	for i := 0; i < 3; i++ {
		r.tick(1550 * time.Millisecond)
	}
	for _, id := range casts(r.g) {
		if id == spMark || id == spSerpent {
			t.Fatalf("on a mob at 30%%: %v", casts(r.g))
		}
	}
}

func TestMultiShotNeedsTwoMobsAtTheTargetAndTheAreaVetoToAllowIt(t *testing.T) {
	r, _ := newHunterRig(t, Params{})
	rangedFight(r)
	markAndSting(r.g)
	pack := mob(mobB, tankG)
	r.g.addUnit(pack, 28, 5)
	open(r) // Auto Shot
	d := r.tick(1550 * time.Millisecond)
	if d == nil || d.Action != "multi_shot" || d.Spell != spMulti || d.Rel != RelNormal+7 {
		t.Fatalf("two mobs on the tank: %+v idle %q", d, r.idle())
	}
	// A mob near the target that has nobody: Multi-Shot would pull it.
	r, _ = newHunterRig(t, Params{})
	rangedFight(r)
	markAndSting(r.g)
	loose := mob(mobB, 999)
	r.g.addUnit(loose, 28, 5)
	open(r)
	for i := 0; i < 3; i++ {
		r.tick(1550 * time.Millisecond)
	}
	for _, id := range casts(r.g) {
		if id == spMulti {
			t.Fatalf("Multi-Shot into a loose mob: %v", casts(r.g))
		}
	}
	if d := r.tick(1550 * time.Millisecond); d != nil && d.Action == "multi_shot" {
		t.Fatalf("%+v", d)
	}
	// A loose mob beside the hunter is the damage dealer's veto.
	r, _ = newHunterRig(t, Params{})
	rangedFight(r)
	markAndSting(r.g)
	r.g.addUnit(mob(mobB, tankG), 28, 5)
	r.g.addUnit(mob(mobC, 999), 3, 3)
	open(r)
	for i := 0; i < 3; i++ {
		r.tick(1550 * time.Millisecond)
	}
	for _, id := range casts(r.g) {
		if id == spMulti {
			t.Fatalf("Multi-Shot with a loose mob at the hunter: %v", casts(r.g))
		}
	}
	// One mob only: the arcane shot.
	r, _ = newHunterRig(t, Params{})
	rangedFight(r)
	markAndSting(r.g)
	open(r)
	if d := r.tick(1550 * time.Millisecond); d == nil || d.Action != "arcane_shot" {
		t.Fatalf("%+v", d)
	}
}

func TestHunterAspects(t *testing.T) {
	mk := func(inCombat bool, mana uint32, auras []objects.Aura, known ...uint32) *rig {
		r, _ := newClassRig(t, "marksman_hunter", ClassHunter, 58, 0, 1000, Params{}, known, func(u *objects.Unit) {
			u.Auras, u.Power, u.InCombat = auras, mana, inCombat
		})
		return r
	}
	// No aspect out of combat: the Hawk.
	if d := mk(false, 1000, nil, hunterSpells...).tick(time.Second); d == nil || d.Action != "aspect_hawk" || d.Spell != spHawk {
		t.Fatalf("%+v", d)
	}
	// Cheetah out of combat: left alone (the player runs); in combat: the Hawk.
	cheetah := []objects.Aura{{Name: "Aspect of the Cheetah", SpellID: spCheetah}}
	if d := mk(false, 1000, cheetah, hunterSpells...).tick(time.Second); d != nil {
		t.Fatalf("the Cheetah out of combat: %+v", d)
	}
	if d := mk(true, 1000, cheetah, hunterSpells...).tick(time.Second); d == nil || d.Action != "aspect_hawk" {
		t.Fatalf("the Cheetah in combat: %+v", d)
	}
	// The Cheetah is never cast by the brain.
	r := mk(false, 1000, nil, hunterSpells...)
	for i := 0; i < 3; i++ {
		r.tick(2 * time.Second)
	}
	for _, id := range casts(r.g) {
		if id == spCheetah {
			t.Fatalf("%v", casts(r.g))
		}
	}
	// Low mana in combat: the Viper. Mana back: the Hawk. Without the Viper in the spellbook: nothing (the drink is for rest).
	hawk := []objects.Aura{{Name: "Aspect of the Hawk", SpellID: spHawk}}
	viper := []objects.Aura{{Name: "Aspect of the Viper", SpellID: spViper}}
	if d := mk(true, 150, hawk, hunterSpells...).tick(time.Second); d == nil || d.Action != "aspect_viper" || d.Spell != spViper || d.Rel != RelHigh {
		t.Fatalf("%+v", d)
	}
	if d := mk(true, 500, viper, hunterSpells...).tick(time.Second); d != nil {
		t.Fatalf("the Viper stays until 60%%: %+v", d)
	}
	if d := mk(true, 700, viper, hunterSpells...).tick(time.Second); d == nil || d.Action != "aspect_hawk" {
		t.Fatalf("%+v", d)
	}
	noViper := []uint32{spAutoShot, spMark, spSerpent, spArcane, spHawk}
	if d := mk(true, 150, hawk, noViper...).tick(time.Second); d != nil {
		t.Fatalf("no Viper to cast: %+v", d)
	}
	// Out of combat upkeep waits for a rest gate.
	r = mk(false, 1000, nil, hunterSpells...)
	r.b.restOK = func() bool { return false }
	if d := r.tick(time.Second); d != nil {
		t.Fatalf("%+v", d)
	}
}

func TestHunterDrinksAtRestWhenTheViperIsNotThere(t *testing.T) {
	r, _ := newClassRig(t, "marksman_hunter", ClassHunter, 58, 0, 1000, Params{}, hunterSpells, func(u *objects.Unit) {
		u.Auras, u.Power = []objects.Aura{{Name: "Aspect of the Hawk", SpellID: spHawk}}, 400
	})
	r.g.items = []objects.Item{{Entry: 159, Count: 5}} // Refreshing Spring Water
	d := r.tick(time.Second)
	if d == nil || d.Action != "drink" || len(r.g.used) != 1 || r.g.used[0] != 159 {
		t.Fatalf("%+v used %v idle %q", d, r.g.used, r.idle())
	}
	// Not in combat only, and not while the Drink aura is there.
	r, _ = newClassRig(t, "marksman_hunter", ClassHunter, 58, 0, 1000, Params{}, hunterSpells, func(u *objects.Unit) {
		u.Auras, u.Power = []objects.Aura{{Name: "Aspect of the Hawk", SpellID: spHawk}, {Name: "Drink", SpellID: 430}}, 400
	})
	r.g.items = []objects.Item{{Entry: 159, Count: 5}}
	if d := r.tick(time.Second); d != nil {
		t.Fatalf("%+v", d)
	}
}

func TestConcussiveShotOnAMobRunningAtTheHunter(t *testing.T) {
	r, _ := newHunterRig(t, Params{})
	rangedFight(r)
	m := mob(mobB, selfG)
	m.Moving = true
	r.g.addUnit(m, 20, 0)
	d := r.tick(100 * time.Millisecond)
	if d == nil || d.Action != "concussive_shot" || d.Spell != spConcussive || d.Target != mobB || d.Rel != RelMove+2 {
		t.Fatalf("%+v idle %q", d, r.idle())
	}
	// Slowed already, or standing still: no.
	r, _ = newHunterRig(t, Params{})
	rangedFight(r)
	m = mob(mobB, selfG)
	m.Moving = true
	m.Auras = []objects.Aura{{Name: "Concussive Shot", Harmful: true}}
	r.g.addUnit(m, 20, 0)
	if d := r.tick(100 * time.Millisecond); d != nil && d.Action == "concussive_shot" {
		t.Fatalf("%+v", d)
	}
	r, _ = newHunterRig(t, Params{})
	rangedFight(r)
	r.g.addUnit(mob(mobB, selfG), 20, 0)
	if d := r.tick(100 * time.Millisecond); d != nil && d.Action == "concussive_shot" {
		t.Fatalf("a mob that stands: %+v", d)
	}
}

func TestFeignDeathWhenAMobHasTheHunterAndATankIsThere(t *testing.T) {
	r, mf := newHunterRig(t, Params{})
	tankFights(r.g, 12, 5, 0)
	onMe(r.g, mobA)
	d := r.tick(100 * time.Millisecond)
	if d == nil || d.Action != "feign_death" || d.Spell != spFeignDeath || d.Rel != relEmergency-2 {
		t.Fatalf("%+v idle %q", d, r.idle())
	}
	// The shots and the swing wait while it lasts (6 s), then go on.
	r.g.setSelf(func(u *objects.Unit) {
		u.Auras = append(u.Auras, objects.Aura{Name: "Feign Death", SpellID: spFeignDeath})
	})
	if d := r.tick(2 * time.Second); d != nil {
		t.Fatalf("shooting while feigning: %+v", d)
	}
	if len(mf.walkList()) != 0 {
		t.Fatalf("%v", mf.walkList())
	}
	for _, c := range casts(r.g) {
		if c == spAutoShot || c == spRaptor {
			t.Fatalf("%v", casts(r.g))
		}
	}
	// Without a tank: no feint; the mob is fought with the swing and Raptor Strike.
	r, mf = newHunterRig(t, Params{})
	m := mob(mobA, selfG)
	m.Auras = []objects.Aura{{Name: "Hunter's Mark", SpellID: spMark, Harmful: true}}
	r.g.addUnit(m, 4, 0)
	r.g.setSelf(func(u *objects.Unit) { u.InCombat = true })
	d = r.tick(100 * time.Millisecond)
	if d == nil || d.Action != "hunter_swing" || mf.attacks != 1 {
		t.Fatalf("%+v attacks %d idle %q", d, mf.attacks, r.idle())
	}
	d = r.tick(1600 * time.Millisecond)
	if d == nil || d.Action != "raptor_strike" || d.Spell != spRaptor || d.Target != mobA {
		t.Fatalf("%+v idle %q", d, r.idle())
	}
	for _, c := range casts(r.g) {
		if c == spFeignDeath || c == spAutoShot {
			t.Fatalf("%v", casts(r.g))
		}
	}
	// The Raptor Strike is queued for the swing: not sent twice.
	if d := r.tick(400 * time.Millisecond); d != nil && d.Action == "raptor_strike" {
		t.Fatalf("%+v", d)
	}
}

func TestHunterStepsOutOfTheDeadZone(t *testing.T) {
	r, mf := newHunterRig(t, Params{})
	tankFights(r.g, 7, 6, 0) // the tank's mob 6 yards away, facing away
	d := r.tick(100 * time.Millisecond)
	if d == nil || d.Action != "hunter_reposition" || d.Rel != RelMove {
		t.Fatalf("%+v idle %q", d, r.idle())
	}
	w := mf.walkList()
	if len(w) != 1 {
		t.Fatalf("walks %v", w)
	}
	if dd := dist2(w[0], Vec3{6, 0, 0}); dd < 11 || dd > 30 {
		t.Fatalf("walked to %v, %.1f yards from the mob", w[0], dd)
	}
	// From the middle of the band it stays.
	r, mf = newHunterRig(t, Params{})
	rangedFight(r)
	for i := 0; i < 3; i++ {
		r.tick(1550 * time.Millisecond)
	}
	if len(mf.walkList()) != 0 {
		t.Fatalf("walked %v", mf.walkList())
	}
	// Too far for the shots (34 yards): closer. In a mob's front within 14 yards: out of it.
	r, mf = newHunterRig(t, Params{})
	tankFights(r.g, 30, 36, 0)
	if d := r.tick(100 * time.Millisecond); d == nil || d.Action != "hunter_reposition" {
		t.Fatalf("a mob at 36 yards: %+v idle %q", d, r.idle())
	}
	r, mf = newHunterRig(t, Params{})
	tankFights(r.g, 20, 12, math.Pi) // 12 yards away and facing the hunter
	if d := r.tick(100 * time.Millisecond); d == nil || d.Action != "hunter_reposition" {
		t.Fatalf("in the mob's front: %+v idle %q", d, r.idle())
	}
	if w := mf.walkList(); len(w) != 1 || inFront(&objects.Unit{X: 12, Y: 0, O: math.Pi}, w[0]) {
		t.Fatalf("walked to %v, in front of the mob", w)
	}
	// No walking when the run switched movement off: the hunter still shoots from where it stands.
	r, mf = newHunterRig(t, Params{NoMove: true})
	tankFights(r.g, 7, 6, 0)
	r.tick(100 * time.Millisecond)
	if len(mf.walkList()) != 0 {
		t.Fatalf("%v", mf.walkList())
	}
}

func TestHunterWalkingEndsTheShotsSoTheyStartAgain(t *testing.T) {
	r, mf := newHunterRig(t, Params{})
	rangedFight(r)
	open(r)
	if shots := casts(r.g); len(shots) != 1 || shots[0] != spAutoShot {
		t.Fatalf("%v", shots)
	}
	// The mob moves into the dead zone: the hunter walks away, and sends Auto Shot again at once.
	r.g.mutate(mobA, func(u *objects.Unit) { u.X = 5 })
	if d := r.tick(300 * time.Millisecond); d == nil || d.Action != "hunter_reposition" {
		t.Fatalf("%+v idle %q", d, r.idle())
	}
	markAndSting(r.g)
	r.g.mutate(mobA, func(u *objects.Unit) { u.X = 15 })
	d := r.tick(1550 * time.Millisecond)
	if d == nil || d.Action != "auto_shot" {
		t.Fatalf("%+v walks %v", d, mf.walkList())
	}
}

func TestHunterStopsShootingWithoutAmmo(t *testing.T) {
	r, _ := newHunterRig(t, Params{})
	rangedFight(r)
	open(r)
	r.g.st.CastRefused(spAutoShot, codeOf(t, "no_ammo"))
	r.g.st.Clear(spAutoShot)
	markAndSting(r.g)
	// Held for 30 s; the ticks go on every 1.5 s, as the fight's clock needs, and the shots resume when the hold ends.
	resumed := time.Duration(0)
	for i := 1; i <= 24 && resumed == 0; i++ {
		d := r.tick(1500 * time.Millisecond)
		if d != nil && d.Action == "auto_shot" {
			resumed = time.Duration(i) * 1500 * time.Millisecond
		}
		if i == 4 {
			c := r.b.newCtx(r.g.clk.Now())
			if ok, why := r.b.possible(c, r.b.actions["auto_shot"]); ok || why != "held:no_ammo" {
				t.Fatalf("the reason is on the action, for brain_status: %v %q", ok, why)
			}
		}
	}
	if resumed < 28*time.Second || resumed > 34*time.Second {
		t.Fatalf("Auto Shot resumed after %v, want about 30 s", resumed)
	}
}

func TestHunterSpellData(t *testing.T) {
	for _, c := range []struct {
		id       uint32
		name     string
		level    uint8
		cost     uint32 // flat mana
		rangeMin float32
		rangeMax float32
	}{
		{spAutoShot, "Auto Shot", 1, 0, 8, 35}, {spMark, "Hunter's Mark", 58, 60, 0, 100}, {spSerpent, "Serpent Sting", 58, 150, 8, 35},
		{spArcane, "Arcane Shot", 52, 105, 8, 35}, {spMulti, "Multi-Shot", 54, 210, 8, 35}, {spConcussive, "Concussive Shot", 8, 0, 8, 35},
		{spHawk, "Aspect of the Hawk", 58, 110, 0, 0}, {spCheetah, "Aspect of the Cheetah", 20, 40, 0, 0}, {spViper, "Aspect of the Viper", 56, 0, 0, 0},
		{spFeignDeath, "Feign Death", 30, 80, 0, 0}, {spRaptor, "Raptor Strike", 56, 100, 0, 5},
	} {
		if got := spells.Name(c.id); got != c.name {
			t.Errorf("%d is %q, want %q", c.id, got, c.name)
			continue
		}
		m, ok := spells.Lookup(c.id)
		if !ok || m.Level != c.level || m.Cost != c.cost || m.RangeMin != c.rangeMin || m.RangeMax != c.rangeMax || m.Power != spells.PowerMana {
			t.Errorf("%s: %+v", c.name, m)
		}
		if m.Flags.Has(spells.FlagStealthOnly) || m.Flags.Has(spells.FlagNotInCombat) {
			t.Errorf("%s: %v", c.name, m.Flags.Names())
		}
	}
	// The shots are ranged weapon spells; Auto Shot repeats by itself and has no cost; the dead zone is the minimum range of 8.
	if m, _ := spells.Lookup(spAutoShot); !m.Flags.Has(spells.FlagAutoRepeat) || !m.Flags.Has(spells.FlagRanged) || m.Cost != 0 || m.CostPct != 0 {
		t.Errorf("Auto Shot: %+v", m)
	}
	for _, id := range []uint32{spArcane, spMulti, spSerpent, spConcussive} {
		if m, _ := spells.Lookup(id); m.RangeMin != hunterDeadZone {
			t.Errorf("%s: minimum range %.0f, the brain's dead zone is %.0f", spells.Name(id), m.RangeMin, hunterDeadZone)
		}
	}
	// Concussive Shot slows (a daze mechanic 11), 12 s; Feign Death's cooldown starts when it ends and is 30 s; Arcane Shot 6 s, Multi-Shot 10 s.
	if m, _ := spells.Lookup(spConcussive); m.Mechanic != 11 || m.CooldownMS != 12000 {
		t.Errorf("Concussive Shot: %+v", m)
	}
	if m, _ := spells.Lookup(spFeignDeath); !m.Flags.Has(spells.FlagCooldownOnFade) || m.CooldownMS != 30000 {
		t.Errorf("Feign Death: %+v", m)
	}
	if m, _ := spells.Lookup(spArcane); m.CategoryCDMS != 6000 {
		t.Errorf("Arcane Shot: %+v", m)
	}
	if m, _ := spells.Lookup(spMulti); m.CategoryCDMS != 10000 || !m.Flags.Has(spells.FlagDamage) {
		t.Errorf("Multi-Shot: %+v", m)
	}
	// Aspect of the Viper: Turtle has it (an aura of mana regeneration, SPELL_AURA_OBS_MOD_MANA, 21) and the brain switches to it.
	if m, _ := spells.Lookup(spViper); m.Effects[0].Aura != 21 || m.DurationMS != -1 {
		t.Errorf("Aspect of the Viper: %+v", m)
	}
	// The class table row, and no stance anywhere.
	if ci := ClassOf(ClassHunter); ci.Power != powerMana || !ci.Drinks || !ci.ManaPotions || len(ci.Interrupts) != 0 {
		t.Errorf("%+v", ci)
	}
	assertNoStances(t, "hunter", []uint32{75, 1130, 14323, 14324, 14325, 1978, 13549, 13550, 13551, 13552, 13553, 13554, 13555, 25295, 3044, 14281, 14282, 14283,
		14284, 14285, 14286, 14287, 2643, 14288, 14289, 14290, 25294, 13165, 14318, 14319, 14320, 14321, 14322, 25296, 5118, 45651, 5116, 5384, 2973, 14260,
		14261, 14262, 14263, 14264, 14265, 14266})
}

func TestAutoShotIsNotSentAgainWhileShotsAreSeen(t *testing.T) {
	r, _ := newHunterRig(t, Params{})
	rangedFight(r)
	open(r)
	markAndSting(r.g)
	// The server's SMSG_SPELL_GO of each shot reaches the spell state: the brain takes Auto Shot as running and never repeats it.
	for i := 0; i < 21; i++ {
		r.g.st.CastWent(spAutoShot)
		r.tick(time.Second)
	}
	shots := 0
	for _, id := range casts(r.g) {
		if id == spAutoShot {
			shots++
		}
	}
	if shots != 1 {
		t.Fatalf("Auto Shot sent %d times in 21 s of shooting: %v", shots, casts(r.g))
	}
	// No shot seen for over 6 s (a walk, a stun, a toggle): it is sent again, once.
	for i := 0; i < 8; i++ {
		r.tick(time.Second)
	}
	shots = 0
	for _, id := range casts(r.g) {
		if id == spAutoShot {
			shots++
		}
	}
	if shots != 2 {
		t.Fatalf("Auto Shot sent %d times after the shots stopped: %v", shots, casts(r.g))
	}
	// A new mob needs a new send.
	r.g.mutate(mobA, func(u *objects.Unit) { u.Dead = true })
	r.g.addUnit(func() objects.Unit { m := mob(mobB, tankG); return m }(), 28, 0)
	r.g.mutate(tankG, func(u *objects.Unit) { u.Target = mobB })
	r.g.st.CastWent(spAutoShot)
	d := r.tick(time.Second)
	if d == nil || d.Action != "auto_shot" || d.Target != mobB {
		t.Fatalf("%+v idle %q", d, r.idle())
	}
}

func TestFeignDeathSwitchesAutoShotOffAndItStartsAgainAfterTheWait(t *testing.T) {
	r, _ := newHunterRig(t, Params{})
	tankFights(r.g, 6, 12, 0) // the tank near, the mob 12 yd off
	r.tick(100 * time.Millisecond)
	open(r) // Auto Shot at the tank's mob
	onMe(r.g, mobA)
	d := r.tick(300 * time.Millisecond)
	if d == nil || d.Action != "feign_death" || r.g.cancels != 1 {
		t.Fatalf("%+v cancels %d idle %q", d, r.g.cancels, r.idle())
	}
	r.g.setSelf(func(u *objects.Unit) {
		u.Auras = append(u.Auras, objects.Aura{Name: "Feign Death", SpellID: spFeignDeath})
	})
	before := len(r.g.castList())
	var resumedAt time.Duration
	for i := 1; i <= 5 && resumedAt == 0; i++ {
		d := r.tick(2 * time.Second)
		if d != nil && d.Action == "auto_shot" {
			resumedAt = time.Duration(i) * 2 * time.Second
		}
	}
	if resumedAt < 6*time.Second || resumedAt > 8*time.Second {
		t.Fatalf("Auto Shot started again after %v, want after the 6 s wait; casts %v", resumedAt, casts(r.g)[before:])
	}
	if r.g.cancels != 1 {
		t.Fatalf("cancels %d", r.g.cancels)
	}
}
