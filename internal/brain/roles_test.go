package brain

import (
	"math"
	"strings"
	"testing"
	"time"

	"github.com/pjerra/turtle-agent/internal/objects"
)

func TestRoleResolution(t *testing.T) {
	g := newFake()
	for _, c := range []struct {
		p    Params
		role string
		err  string
	}{
		{Params{Strategies: []string{"frost_mage"}}, "dps", ""},
		{Params{Strategies: []string{"arms_warrior"}}, "dps", ""},
		{Params{Strategies: []string{"fury_warrior"}, Role: "dps"}, "dps", ""},
		{Params{Strategies: []string{"tank_warrior"}}, "tank", ""},
		{Params{Strategies: []string{"tank_warrior"}, Role: "Tank"}, "tank", ""},
		{Params{Strategies: []string{"mage_rest"}}, "dps", ""}, // no strategy names a role: damage, as it always was
		{Params{Strategies: []string{"arms_warrior"}, Role: "tank"}, "", `role "tank" does not match the strategies`},
		{Params{Strategies: []string{"tank_warrior"}, Role: "dps"}, "", `role "dps" does not match the strategies`},
		{Params{Strategies: []string{"frost_mage"}, Role: "tank"}, "", `role "tank"`},
		{Params{Strategies: []string{"mage_rest"}, Role: "tank"}, "", `role "tank" needs the role_tank strategy`},
		{Params{Strategies: []string{"holy_priest"}}, "healer", ""},
		{Params{Strategies: []string{"holy_priest"}, Role: "Healer"}, "healer", ""},
		{Params{Strategies: []string{"shadow_priest"}}, "dps", ""},
		{Params{Strategies: []string{"role_healer"}}, "healer", ""},
		{Params{Strategies: []string{"frost_mage"}, Role: "healer"}, "", `role "healer" does not match the strategies`},
		{Params{Strategies: []string{"holy_priest"}, Role: "dps"}, "", `role "dps" does not match the strategies`},
		{Params{Strategies: []string{"mage_rest"}, Role: "healer"}, "", `role "healer" needs the role_healer strategy`},
		{Params{Strategies: []string{"role_tank", "role_dps"}}, "", "play more than one role"},
		{Params{Strategies: []string{"frost_mage"}, Role: "bard"}, "", `role "bard": want dps, tank or healer`},
	} {
		b, err := New(g, DefaultConfig(), c.p)
		if c.err != "" {
			if err == nil || !strings.Contains(err.Error(), c.err) {
				t.Errorf("%+v: %v, want %q", c.p, err, c.err)
			}
			continue
		}
		if err != nil || b.p.Role != c.role {
			t.Errorf("%+v: role %q err %v, want %q", c.p, b.p.Role, err, c.role)
		}
	}
	// role_dps is combat_dps under the name of its role.
	b, err := New(g, DefaultConfig(), Params{Strategies: []string{"role_dps", "mage_frost"}})
	if err != nil || strings.Join(b.Strategies(), ",") != "base,combat_dps,mage_frost" || b.p.Role != "dps" {
		t.Fatalf("%v %v", b, err)
	}
	if b, err = New(g, DefaultConfig(), Params{Strategies: []string{"role_dps", "combat_dps", "mage_frost"}}); err != nil || len(b.Strategies()) != 3 {
		t.Fatalf("the alias and its strategy are one: %v %v", b.Strategies(), err)
	}
}

// Resolve alone refuses what could never run (strategies of two roles), so that dungeon_run's brain option fails at once.
func TestResolveRefusesMixedRoles(t *testing.T) {
	for _, c := range []struct {
		names []string
		want  string
	}{
		{[]string{"tank_warrior", "frost_mage"}, "play more than one role"},
		{[]string{"holy_priest", "combat_dps"}, "play more than one role"},
	} {
		if _, _, err := Resolve(c.names); err == nil || !strings.Contains(err.Error(), c.want) {
			t.Errorf("%v: %v", c.names, err)
		}
	}
	for _, n := range []string{"tank_warrior", "arms_warrior", "fury_warrior", "frost_mage", "holy_priest", "shadow_priest", "role_healer"} {
		if _, _, err := Resolve([]string{n}); err != nil {
			t.Errorf("%s: %v", n, err)
		}
	}
}

// The preset of the first class is as the earlier steps shipped it: the warrior and the roles added presets, they did not change it.
func TestFrostMagePresetIsAsBefore(t *testing.T) {
	want := []string{"combat_dps", "mage_frost", "mage_rest", "ranged_position", "avoid_ground", "mage_defense", "mage_interrupt", "combat_consumables", "wand", "rest_eat"}
	if got := presets["frost_mage"]; strings.Join(got, ",") != strings.Join(want, ",") {
		t.Fatalf("%v", got)
	}
}

func TestWarriorPresetsListTheirStrategies(t *testing.T) {
	for preset, want := range map[string]string{
		"arms_warrior": "base,combat_dps,warrior_arms,warrior_melee,melee_position,avoid_ground,class_interrupt,combat_consumables,rest_eat",
		"fury_warrior": "base,combat_dps,warrior_fury,warrior_melee,melee_position,avoid_ground,class_interrupt,combat_consumables,rest_eat",
		"tank_warrior": "base,role_tank,warrior_tank,warrior_melee,melee_position,avoid_ground,class_interrupt,combat_consumables,rest_eat",
	} {
		b, err := New(newFake(), DefaultConfig(), Params{Strategies: []string{preset}})
		if err != nil || strings.Join(b.Strategies(), ",") != want {
			t.Errorf("%s: %v %v", preset, b.Strategies(), err)
		}
	}
}

// ---- the tank ----

func addHealer(g *fakeGame, x, y float32) {
	g.party = append(g.party, Member{Name: "Hilda", GUID: healG, Online: true})
	g.addUnit(objects.Unit{GUID: healG, Type: "player", Name: "Hilda", Class: 5, InParty: true, Health: 700, MaxHealth: 700}, x, y)
}

func tankRig(t *testing.T, known ...uint32) (*rig, *meleeFake) {
	r, mf := newWarriorRig(t, "tank_warrior", stanceDefensive, Params{}, known...)
	r.g.setSelf(func(u *objects.Unit) { u.InCombat = true })
	return r, mf
}

func TestTankTurnsTheMobAwayFromTheGroup(t *testing.T) {
	r, mf := tankRig(t)
	m := mob(mobA, selfG)
	m.O = math.Pi // the mob faces the tank, and so the healer behind the tank
	r.g.addUnit(m, 3, 0)
	addHealer(r.g, -6, 0)
	d := r.tick(100 * time.Millisecond)
	if d == nil || d.Action != "melee_reposition" {
		t.Fatalf("%+v idle %q", d, r.idle())
	}
	w := mf.walkList()
	mp := Vec3{3, 0, 0}
	if len(w) != 1 || w[0][0] <= 3 || dist2(w[0], mp) < 2.5 || dist2(w[0], mp) > 4.1 {
		t.Fatalf("the tank walked to %v, which is not across the mob from the healer", w)
	}
	// The mob turns to the tank's new place: the group is behind it and the tank holds it, no more walking, and no opening delay.
	r.g.mutate(mobA, func(u *objects.Unit) { u.O = float32(bearing(Vec3{u.X, u.Y, u.Z}, here(r.g))) })
	d = r.tick(3 * time.Second)
	if d == nil || d.Action != "auto_attack" || len(mf.walkList()) != 1 {
		t.Fatalf("%+v walks %v", d, mf.walkList())
	}
}

func TestTankLeavesTheMobAloneWhenTheGroupIsToTheSide(t *testing.T) {
	r, mf := tankRig(t)
	m := mob(mobA, selfG)
	m.O = math.Pi
	r.g.addUnit(m, 3, 0)
	addHealer(r.g, 0, 8) // 70 degrees round from the way the mob faces
	d := r.tick(100 * time.Millisecond)
	if d == nil || d.Action != "auto_attack" || len(mf.walkList()) != 0 {
		t.Fatalf("%+v walks %v", d, mf.walkList())
	}
}

func TestTankTauntsALooseAddAndOnlyInDefensiveStance(t *testing.T) {
	setup := func(stance string) (*rig, *meleeFake) {
		r, mf := newWarriorRig(t, "tank_warrior", stance, Params{})
		r.g.setSelf(func(u *objects.Unit) { u.InCombat = true })
		held := mob(mobA, selfG)
		held.O = 0
		r.g.addUnit(held, 3, 0)
		loose := mob(mobB, healG) // hits the healer
		loose.O = 0
		r.g.addUnit(loose, 0, 4)
		addHealer(r.g, -6, 4)
		mf.setSwing(mobB)
		return r, mf
	}
	r, _ := setup(stanceDefensive)
	d := r.tick(100 * time.Millisecond)
	if d == nil || d.Action != "taunt" || d.Trigger != "loose_add" || d.Spell != spTaunt || d.Target != mobB || d.Rel != RelHigh+5 {
		t.Fatalf("%+v idle %q", d, r.idle())
	}
	r, _ = setup(stanceBattle)
	r.tick(100 * time.Millisecond)
	for _, id := range casts(r.g) {
		if id == spTaunt {
			t.Fatal("Taunt outside Defensive Stance")
		}
	}
}

func TestTankKit(t *testing.T) {
	// Sunder Armor up to five stacks, then the rage goes into Heroic Strike.
	r, mf := tankRig(t)
	r.g.addUnit(mob(mobA, selfG), 3, 0)
	mf.setSwing(mobA)
	setRage(r.g, 600)
	if d := r.tick(100 * time.Millisecond); d == nil || d.Action != "sunder" || d.Spell != spSunder {
		t.Fatalf("no opening delay for a tank: %+v idle %q", d, r.idle())
	}
	r.g.mutate(mobA, func(u *objects.Unit) {
		u.Auras = []objects.Aura{{Name: "Sunder Armor", SpellID: spSunder, Stacks: 5, Harmful: true}}
	})
	if d := r.tick(1600 * time.Millisecond); d == nil || d.Action != "heroic_strike" {
		t.Fatalf("five stacks are enough: %+v", d)
	}

	// Revenge after the tank avoided an attack, before Sunder.
	r, mf = tankRig(t)
	r.g.addUnit(mob(mobA, selfG), 3, 0)
	mf.setSwing(mobA)
	setRage(r.g, 600)
	mf.avoided = r.g.clk.Now().Add(80 * time.Millisecond)
	if d := r.tick(100 * time.Millisecond); d == nil || d.Action != "revenge" || d.Spell != spRevenge {
		t.Fatalf("%+v", d)
	}
	r, mf = tankRig(t)
	r.g.addUnit(mob(mobA, selfG), 3, 0)
	mf.setSwing(mobA)
	setRage(r.g, 600)
	mf.avoided = r.g.clk.Now().Add(-time.Minute)
	if d := r.tick(100 * time.Millisecond); d == nil || d.Action == "revenge" {
		t.Fatalf("an old avoid is no window: %+v", d)
	}

	// Thunder Clap, then Demoralizing Shout, with two mobs on the tank; neither again once the mobs carry it.
	r, mf = tankRig(t)
	sundered := []objects.Aura{{Name: "Sunder Armor", SpellID: spSunder, Stacks: 5, Harmful: true}}
	a, b := mob(mobA, selfG), mob(mobB, selfG)
	a.Auras, b.Auras = sundered, sundered
	r.g.addUnit(a, 3, 0)
	r.g.addUnit(b, 3, 2)
	mf.setSwing(mobA)
	setRage(r.g, 450)
	if d := r.tick(100 * time.Millisecond); d == nil || d.Action != "thunder_clap" || d.Spell != spThunder {
		t.Fatalf("%+v", d)
	}
	if d := r.tick(1600 * time.Millisecond); d == nil || d.Action != "demoralizing_shout" || d.Spell != spDemo {
		t.Fatalf("%+v", d)
	}
	both := append([]objects.Aura{{Name: "Thunder Clap", Harmful: true}, {Name: "Demoralizing Shout", Harmful: true}}, sundered...)
	r.g.mutate(mobA, func(u *objects.Unit) { u.Auras = both })
	r.g.mutate(mobB, func(u *objects.Unit) { u.Auras = both })
	r.tick(7 * time.Second)
	if got := casts(r.g); !eq(got, []uint32{spThunder, spDemo}) {
		t.Fatalf("it kept clapping: %v", got)
	}

	// Shield Block when hurt and held.
	r, mf = tankRig(t, spDefSt, spShieldBlock, spBattleShout)
	r.g.addUnit(mob(mobA, selfG), 3, 0)
	mf.setSwing(mobA)
	setRage(r.g, 300)
	r.g.setSelf(func(u *objects.Unit) { u.Health = 800 })
	if d := r.tick(100 * time.Millisecond); d == nil || d.Action != "shield_block" || d.Spell != spShieldBlock {
		t.Fatalf("%+v", d)
	}
}

// A tank holds the mob that is on it, the loose add first, and with nothing on it the one it has selected.
func TestTankChoosesItsMob(t *testing.T) {
	r, _ := tankRig(t)
	r.g.addUnit(mob(mobA, selfG), 3, 0)
	r.g.addUnit(mob(mobB, healG), 0, 5)
	addHealer(r.g, -6, 5)
	c := r.b.newCtx(r.g.clk.Now())
	if f := ValFight.Get(c); f.Mob == nil || f.Mob.GUID != mobB || f.Source != "loose" || f.Tank == nil || f.Tank.GUID != selfG {
		t.Fatalf("%+v", f)
	}
	r, _ = tankRig(t)
	r.g.addUnit(mob(mobA, selfG), 3, 0)
	c = r.b.newCtx(r.g.clk.Now())
	if f := ValFight.Get(c); f.Mob == nil || f.Mob.GUID != mobA || f.Source != "self" {
		t.Fatalf("%+v", f)
	}
	r, _ = tankRig(t)
	r.g.addUnit(mob(mobA, 0), 3, 0)
	r.g.selected = mobA
	c = r.b.newCtx(r.g.clk.Now())
	if f := ValFight.Get(c); f.Mob == nil || f.Mob.GUID != mobA || f.Source != "selected" {
		t.Fatalf("%+v", f)
	}
}

// The mob's facing in the update fields may not follow it: the tank turns it away from the group twice at most, six seconds apart,
// and goes on with Sunder and the rest between.
func TestTankDoesNotWalkForeverToTurnAMobThatStaysPut(t *testing.T) {
	r, mf := tankRig(t)
	m := mob(mobA, selfG)
	m.O = math.Pi // never changes
	r.g.addUnit(m, 3, 0)
	addHealer(r.g, -6, 0)
	setRage(r.g, 100)
	mf.setSwing(mobA)
	var walkTicks, sunders int
	for i := 0; i < 24; i++ { // 19 s
		before := len(mf.walkList())
		d := r.tick(800 * time.Millisecond)
		if len(mf.walkList()) > before {
			walkTicks++
		}
		if d != nil && d.Action == "sunder" {
			sunders++
		}
		setRage(r.g, 100)
		r.g.mutate(mobA, func(u *objects.Unit) { u.Auras = nil })
	}
	if walkTicks != 2 || len(mf.walkList()) != 2 {
		t.Fatalf("walked %d times: %v", walkTicks, mf.walkList())
	}
	if sunders < 2 {
		t.Fatalf("the tank never got to Sunder: %d", sunders)
	}
}
