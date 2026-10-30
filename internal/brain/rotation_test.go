package brain

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/pjerra/turtle-agent/internal/objects"
	"github.com/pjerra/turtle-agent/internal/packet"
	"github.com/pjerra/turtle-agent/internal/spells"
)

// ---- the shipped files ----

func TestEveryBuiltinRotationIsValidAndMatchesTheSpellData(t *testing.T) {
	names := RotationNames()
	for _, want := range []string{"frost_mage", "fire_mage", "arcane_mage", "shadow_priest", "arms_warrior", "fury_warrior", "tank_warrior"} {
		if !slices.Contains(names, want) {
			t.Errorf("no built-in rotation %q (have %v)", want, names)
		}
	}
	for _, n := range names {
		r, err := RotationNamed(n)
		if err != nil {
			t.Fatal(err)
		}
		if len(r.Sources) == 0 || r.Notes == "" {
			t.Errorf("%s: a rotation names its sources and says what it plays", n)
		}
		for _, p := range r.Priorities {
			name := p.Spell
			if p.SpellID != 0 {
				name = spells.Name(p.SpellID)
			}
			if name == "" || p.Talent || p.Level == 0 {
				continue
			}
			// The level of the first rank is the lowest level of a spell of that name.
			if got := FirstRankLevels(name); !slices.Contains(got, p.Level) {
				t.Errorf("%s/%s: %s is learned at level %v in the data, the file says %d", n, p.ID, name, got, p.Level)
			}
		}
	}
}

func TestRotationStancesAreTheCoresStances(t *testing.T) {
	for _, n := range []string{"arms_warrior", "fury_warrior", "tank_warrior"} {
		r, _ := RotationNamed(n)
		for _, p := range r.Priorities {
			want, gated := warriorStances[p.Spell]
			if !gated || p.Kind == "stance" {
				continue
			}
			if !slices.Equal(sortedCopy(p.When.Stance), sortedCopy(want)) {
				t.Errorf("%s/%s: stances %v, the core's data has %v (warriorStances)", n, p.ID, p.When.Stance, want)
			}
		}
	}
}

func sortedCopy(s []string) []string {
	out := slices.Clone(s)
	slices.Sort(out)
	return out
}

func TestEveryRotationActionIdIsFreeInItsPreset(t *testing.T) {
	// The engine keys actions by name: a rotation's id must not be another strategy's action in the presets the rotation is for.
	for preset, rot := range map[string]string{"frost_mage": "frost_mage", "fire_mage": "fire_mage", "arcane_mage": "arcane_mage", "shadow_priest": "shadow_priest",
		"arms_warrior": "arms_warrior", "fury_warrior": "fury_warrior", "tank_warrior": "tank_warrior"} {
		if err := CheckParams(Params{Strategies: []string{preset}, Rotation: rot}); err != nil {
			t.Errorf("%s with %s: %v", preset, rot, err)
		}
	}
}

func TestRotationReplacesTheClassStrategyAndNothingElse(t *testing.T) {
	_, names, err := ResolveFor([]string{"frost_mage"}, ResolveOpts{Rotation: "frost_mage"})
	if err != nil {
		t.Fatal(err)
	}
	if slices.Contains(names, "mage_frost") || !slices.Contains(names, "rot:frost_mage") || !slices.Contains(names, "mage_rest") || !slices.Contains(names, "mage_interrupt") {
		t.Fatalf("%v", names)
	}
	_, plain, _ := Resolve([]string{"frost_mage"})
	if !slices.Contains(plain, "mage_frost") {
		t.Fatalf("without a rotation the preset is as it was: %v", plain)
	}
	if _, _, err := ResolveFor([]string{"frost_mage"}, ResolveOpts{Rotation: "no_such"}); err == nil || !strings.Contains(err.Error(), "unknown") {
		t.Fatalf("%v", err)
	}
	// fire_mage and arcane_mage exist only as rotation files.
	for _, p := range []string{"fire_mage", "arcane_mage"} {
		if _, n, err := Resolve([]string{p}); err != nil || slices.Contains(n, "mage_frost") {
			t.Fatalf("%s: %v %v", p, n, err)
		}
	}
}

func TestRotationDescribeShowsTheRankAtALevel(t *testing.T) {
	r, _ := RotationNamed("frost_mage")
	out := r.Describe(20)
	if !strings.Contains(out, "frostbolt") || !strings.Contains(out, "level 20: id 7322 rank 4") || !strings.Contains(out, "level 20: not learned yet (level 26)") {
		t.Fatalf("%s", out)
	}
	if id := RankAt("Frostbolt", 4); id != 116 {
		t.Fatalf("rank at 4: %d", id)
	}
	if id := RankAt("Frostbolt", 3); id != 0 {
		t.Fatalf("rank at 3: %d", id)
	}
}

// ---- the file format ----

const minimalRotation = `{"version":1,"name":"%s","class":"mage","replaces":["mage_frost"],"priorities":[%s]}`

func rot(name, prio string) []byte {
	return []byte(strings.Replace(strings.Replace(minimalRotation, "%s", name, 1), "%s", prio, 1))
}

func TestRotationFileIsChecked(t *testing.T) {
	good := `{"id":"bolt","spell":"Frostbolt","level":4}`
	if _, err := ParseRotation(rot("ok_one", good)); err != nil {
		t.Fatal(err)
	}
	for name, c := range map[string]struct{ data, want string }{
		"unknown field":    {`{"version":1,"name":"x","replaces":["mage_frost"],"priorities":[{"id":"a","spell":"Frostbolt","level":4}],"extra":1}`, "unknown field"},
		"bad version":      {`{"version":2,"name":"x","replaces":["mage_frost"],"priorities":[{"id":"a","spell":"Frostbolt","level":4}]}`, "version 2"},
		"no replaces":      {`{"version":1,"name":"x","replaces":[],"priorities":[{"id":"a","spell":"Frostbolt","level":4}]}`, "replaces"},
		"bad replaces":     {`{"version":1,"name":"x","replaces":["nope"],"priorities":[{"id":"a","spell":"Frostbolt","level":4}]}`, "not a strategy"},
		"bad name":         {`{"version":1,"name":"X Y","replaces":["mage_frost"],"priorities":[{"id":"a","spell":"Frostbolt","level":4}]}`, "rotation name"},
		"unknown spell":    {string(rot("x", `{"id":"a","spell":"Frostbolt Of Doom","level":4}`)), "no spell is named"},
		"wrong level":      {string(rot("x", `{"id":"a","spell":"Frostbolt","level":5}`)), "file says 5"},
		"no level":         {string(rot("x", `{"id":"a","spell":"Frostbolt"}`)), "level of its first rank"},
		"duplicate id":     {string(rot("x", good+","+good)), "used twice"},
		"bad target":       {string(rot("x", `{"id":"a","spell":"Frostbolt","level":4,"target":"moon"}`)), "target"},
		"bad band":         {string(rot("x", `{"id":"a","spell":"Frostbolt","level":4,"band":"loud"}`)), "band"},
		"bad alt":          {string(rot("x", `{"id":"a","spell":"Frostbolt","level":4,"alt":["b"]}`)), "alt"},
		"bad aura":         {string(rot("x", `{"id":"a","spell":"Frostbolt","level":4,"when":{"no_mob_aura":["Not An Aura"]}}`)), "no aura is named"},
		"bad percent":      {string(rot("x", `{"id":"a","spell":"Frostbolt","level":4,"when":{"self_hp_below":150}}`)), "percent"},
		"bad condition":    {string(rot("x", `{"id":"a","spell":"Frostbolt","level":4,"when":{"flies":true}}`)), "unknown field"},
		"attack w spell":   {string(rot("x", `{"id":"a","kind":"attack","spell":"Frostbolt","level":4}`)), "no spell"},
		"stance not set":   {string(rot("x", `{"id":"a","kind":"stance"}`)), "stances"},
		"no priorities":    {string(rot("x", ``)), "priorities"},
		"both spell forms": {string(rot("x", `{"id":"a","spell":"Frostbolt","spell_id":116,"level":4}`)), "by name or by spell_id"},
	} {
		_, err := ParseRotation([]byte(c.data))
		if err == nil || !strings.Contains(err.Error(), c.want) {
			t.Errorf("%s: %v, want %q", name, err, c.want)
		}
	}
}

func TestRotationRegistryRefusesATakenName(t *testing.T) {
	dir := t.TempDir()
	write := func(file string, data []byte) {
		if err := os.WriteFile(filepath.Join(dir, file), data, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	write("mine.json", rot("test_mine_ab", `{"id":"bolt","spell":"Frostbolt","level":4}`))
	write("notes.txt", []byte("not json, not read"))
	names, err := LoadRotationDir(dir)
	if err != nil || !slices.Equal(names, []string{"test_mine_ab"}) {
		t.Fatalf("%v %v", names, err)
	}
	if _, err := LoadRotationDir(dir); err == nil || !strings.Contains(err.Error(), "exists already") {
		t.Fatalf("a second load of the same name: %v", err)
	}
	write("clash.json", rot("frost_mage", `{"id":"bolt","spell":"Frostbolt","level":4}`))
	if _, err := LoadRotationFile(filepath.Join(dir, "clash.json")); err == nil || !strings.Contains(err.Error(), "exists already") {
		t.Fatalf("a built-in name: %v", err)
	}
	write("bad.json", []byte(`{"version":1`))
	if _, err := LoadRotationFile(filepath.Join(dir, "bad.json")); err == nil {
		t.Fatal("a bad file was taken")
	}
}

func TestTwoRotationFilesAreToldApartByTheirDigest(t *testing.T) {
	a, err := ParseRotation(rot("arm_a", `{"id":"bolt","spell":"Frostbolt","level":4}`))
	if err != nil {
		t.Fatal(err)
	}
	b, _ := ParseRotation(rot("arm_a", `{"id":"bolt","spell":"Frostbolt","level":4,"band":"high"}`))
	if a.SHA() == "" || a.SHA() == b.SHA() {
		t.Fatalf("%q %q", a.SHA(), b.SHA())
	}
}

// ---- the format additions of the KB work (task K0) ----

// testRotationFile parses and registers a file of these tests; the registry keeps it for the rest of the package's run.
func testRotationFile(t *testing.T, body string) *Rotation {
	t.Helper()
	r, err := ParseRotation([]byte(body))
	if err != nil {
		t.Fatalf("test rotation: %v", err)
	}
	if err := RegisterRotation(r); err != nil {
		t.Fatalf("test rotation: %v", err)
	}
	return r
}

const testLevelBand = `{"version":1,"name":"test_level_band","class":"mage","replaces":["mage_frost"],
 "sources":["rotation_test.go"],"notes":"the level band of an entry",
 "priorities":[{"id":"max_29","spell":"Frostbolt","level":4,"when":{"level_max":29}},
  {"id":"min_30","spell":"Frostbolt","level":4,"when":{"level_min":30}}]}`

func TestLevelBand(t *testing.T) {
	r := testRotationFile(t, testLevelBand)
	g := newFake(spFrostbolt)
	buffUp(g)
	g.partyWithTank()
	g.addUnit(mob(mobA, tankG), 28, 0)
	rig := newRig(t, g, Params{Strategies: []string{"frost_mage"}, Rotation: r.Name})
	got := actions(rig, 100*time.Millisecond, 2200*time.Millisecond)
	if slices.Contains(got, "max_29") {
		t.Fatalf("level_max 29 fired at level 30: %v (%s)", got, rig.idle())
	}
	if !slices.Contains(got, "min_30") {
		t.Fatalf("level_min 30 did not fire at level 30: %v (%s)", got, rig.idle())
	}
}

const testMobsNearMob = `{"version":1,"name":"test_mobs_near_mob","class":"mage","replaces":["mage_frost"],
 "sources":["rotation_test.go"],"notes":"the mobs near the fight's mob",
 "priorities":[{"id":"near_three","spell":"Frostbolt","level":4,"when":{"mobs_near_mob_min":3}}]}`

func TestMobsNearMob(t *testing.T) {
	r := testRotationFile(t, testMobsNearMob)
	g := newFake(spFrostbolt)
	buffUp(g)
	tankFights(g, 5, 8, 0)                         // the tank at 5 yd, its mob at 8
	g.addUnit(mob(packet.GUID(101), tankG), 11, 0) // 3 yd off the fight's mob
	g.addUnit(mob(packet.GUID(102), tankG), 17, 0) // 9 yd off it: out of the 8 yd count
	rig := newRig(t, g, Params{Strategies: []string{"frost_mage"}, Rotation: r.Name})
	if got := actions(rig, 100*time.Millisecond, 2200*time.Millisecond); slices.Contains(got, "near_three") {
		t.Fatalf("two mobs within 8 yd and one at 9 yd fired it: %v", got)
	}
	if c := g.castList(); len(c) != 0 {
		t.Fatalf("casts: %v", c)
	}
	g.addUnit(mob(packet.GUID(103), tankG), 6, 0) // 2 yd off the fight's mob: three within 8
	if got := actions(rig, 2200*time.Millisecond); !slices.Contains(got, "near_three") {
		t.Fatalf("three mobs within 8 yd did not fire it: %v", got)
	}
}

const testHealAdd = `{"version":1,"name":"test_heal_add","class":"priest","mode":"add","replaces":[],
 "sources":["rotation_test.go"],"notes":"a heal-target entry of the add mode",
 "priorities":[{"id":"renew_tank","spell":"Renew","level":8,"target":"heal","band":"high",
  "when":{"heal_is_tank":true,"heal_no_aura":["Renew"]}}]}`

func TestHealTargetEntry(t *testing.T) {
	file := testRotationFile(t, testHealAdd)
	g := priestFake(holyKnown...)
	rig := newRig(t, g, Params{Strategies: []string{"holy_priest"}, Rotation: file.Name})
	g.member(tankG, "Tankard", 1, 700, 1000, 5, 0) // the tank at 70%
	if got := actions(rig, 100*time.Millisecond, 2200*time.Millisecond); !slices.Contains(got, "renew_tank") {
		t.Fatalf("the tank without Renew did not get it: %v (%s)", got, rig.idle())
	}
	want := castCall{prRenew1, tankG}
	if c := g.castList(); len(c) == 0 || c[0] != want {
		t.Fatalf("casts %v, want %v first", c, want)
	}
	g.addAura(tankG, objects.Aura{Name: "Renew", SpellID: prRenew1})
	if got := actions(rig, 2200*time.Millisecond, 2200*time.Millisecond); slices.Contains(got, "renew_tank") {
		t.Fatalf("the tank carries Renew and got it again: %v", got)
	}
}

const testRankRenew = `{"version":1,"name":"test_rank_renew","class":"priest","replaces":["priest_holy"],
 "sources":["rotation_test.go"],"notes":"a rank-1 entry",
 "priorities":[{"id":"renew_one","spell":"Renew","level":8,"rank":1,"target":"self"}]}`

func TestRankEntry(t *testing.T) {
	file := testRotationFile(t, testRankRenew)
	g := priestFake(prRenew1, 6075, prInnerFire, prFort1) // Renew ranks 1 and 3
	rig := newRig(t, g, Params{Strategies: []string{"holy_priest"}, Rotation: file.Name})
	if got := actions(rig, 100*time.Millisecond, 2200*time.Millisecond); !slices.Contains(got, "renew_one") {
		t.Fatalf("rank 1 did not fire: %v", got)
	}
	if c := g.castList(); len(c) == 0 || c[0].spell != prRenew1 {
		t.Fatalf("casts %v, want Renew rank 1 (%d)", c, prRenew1)
	}
	g3 := priestFake(6075, prInnerFire, prFort1) // rank 3 only
	rig3 := newRig(t, g3, Params{Strategies: []string{"holy_priest"}, Rotation: file.Name})
	if got := actions(rig3, 100*time.Millisecond, 2200*time.Millisecond, 2200*time.Millisecond); slices.Contains(got, "renew_one") {
		t.Fatalf("rank 1 is not known and the entry ran anyway: %v", got)
	}
	for _, c := range g3.castList() {
		if c.spell == 6075 {
			t.Fatalf("the rank-3 id was cast as a fallback: %v", c)
		}
	}
}

func TestAddModeInsertsAfterRoleHealer(t *testing.T) {
	file := testRotationFile(t, strings.Replace(testHealAdd, `"test_heal_add"`, `"test_add_order"`, 1))
	_, names, err := ResolveFor([]string{"holy_priest"}, ResolveOpts{Rotation: file.Name})
	if err != nil {
		t.Fatal(err)
	}
	i := slices.Index(names, "role_healer")
	if i < 0 || i+1 >= len(names) || names[i+1] != "rot:"+file.Name {
		t.Fatalf("the add-mode file is not right after role_healer: %v", names)
	}
}

func TestAddModeRefusedWithoutRoleHealer(t *testing.T) {
	file := testRotationFile(t, strings.Replace(testHealAdd, `"test_heal_add"`, `"test_add_no_healer"`, 1))
	if _, _, err := ResolveFor([]string{"frost_mage"}, ResolveOpts{Rotation: file.Name}); err == nil || !strings.Contains(err.Error(), "role_healer") {
		t.Fatalf("an add-mode file in a preset without role_healer: %v", err)
	}
}

func TestHealConditionsNeedHealTarget(t *testing.T) {
	for _, cond := range []string{`{"heal_hp_below":50}`, `{"heal_is_tank":true}`, `{"heal_no_aura":["Renew"]}`} {
		body := `{"version":1,"name":"test_heal_wrong_target","class":"priest","replaces":["priest_holy"],
		 "sources":["rotation_test.go"],"notes":"t",
		 "priorities":[{"id":"x","spell":"Renew","level":8,"target":"fight","when":` + cond + `}]}`
		if _, err := ParseRotation([]byte(body)); err == nil || !strings.Contains(err.Error(), "target heal") {
			t.Errorf("%s with target fight: %v", cond, err)
		}
	}
}

const testUnknownSpell = `{"version":1,"name":"test_unknown_spell","class":"mage","replaces":["mage_frost"],
 "sources":["rotation_test.go"],"notes":"a spell the spellbook lacks",
 "priorities":[{"id":"blizzard_missing","spell":"Blizzard","level":20}]}`

func TestUnknownSpellNeverCast(t *testing.T) {
	file := testRotationFile(t, testUnknownSpell)
	g := newFake(spFrostbolt) // Blizzard is not in the spellbook
	buffUp(g)
	g.partyWithTank()
	g.addUnit(mob(mobA, tankG), 28, 0)
	rig := newRig(t, g, Params{Strategies: []string{"frost_mage"}, Rotation: file.Name})
	if got := actions(rig, 100*time.Millisecond, 2200*time.Millisecond, 2200*time.Millisecond); slices.Contains(got, "blizzard_missing") {
		t.Fatalf("a spell the spellbook lacks was cast: %v", got)
	}
	if c := g.castList(); len(c) != 0 {
		t.Fatalf("casts: %v", c)
	}
}

// ---- the rotations play ----

// actions runs one tick per step and lists the actions that ran ("-" for a tick that did nothing).
func actions(r *rig, steps ...time.Duration) []string {
	var out []string
	for _, d := range steps {
		if dec := r.tick(d); dec != nil {
			out = append(out, dec.Action)
		} else {
			out = append(out, "-")
		}
	}
	return out
}

// frostRigs are the Go frost mage and its data twin on the standard fight.
func frostRigs(t *testing.T, scene func(g *fakeGame)) (goRig, dataRig *rig) {
	t.Helper()
	mk := func(p Params) *rig {
		g := newFake(spFrostbolt, spFireBlast, spFrostNova, spCone, spFrostArmor, spIntellect)
		g.partyWithTank()
		buffUp(g)
		scene(g)
		return newRig(t, g, p)
	}
	return mk(Params{Strategies: []string{"frost_mage"}}), mk(Params{Strategies: []string{"frost_mage"}, Rotation: "frost_mage"})
}

func TestFrostMageDataTwinPlaysLikeTheGoStrategy(t *testing.T) {
	steps := []time.Duration{100 * time.Millisecond, 2200 * time.Millisecond, 1600 * time.Millisecond, 1600 * time.Millisecond}
	for name, scene := range map[string]func(g *fakeGame){
		"a mob at range": func(g *fakeGame) { g.addUnit(mob(mobA, tankG), 28, 0) },
		"a mob in melee": func(g *fakeGame) {
			g.addUnit(mob(mobA, selfG), 4, 0)
		},
		"a rooted mob in melee": func(g *fakeGame) {
			m := mob(mobA, selfG)
			m.Auras = []objects.Aura{{Name: "Frost Nova", SpellID: spFrostNova, Harmful: true}}
			g.addUnit(m, 4, 0)
		},
		"two mobs in front": func(g *fakeGame) {
			g.addUnit(mob(mobA, tankG), 6, 0)
			g.addUnit(mob(mobB, tankG), 8, 1)
		},
	} {
		goRig, dataRig := frostRigs(t, scene)
		want, got := actions(goRig, steps...), actions(dataRig, steps...)
		if !slices.Equal(want, got) {
			t.Errorf("%s: the Go strategy plays %v, the file plays %v", name, want, got)
		}
		if name == "a mob at range" && (len(want) < 3 || want[2] != "frostbolt") {
			t.Errorf("the scene does not play the rotation: %v", want)
		}
	}
}

func TestArmsWarriorDataTwinPlaysLikeTheGoStrategies(t *testing.T) {
	play := func(p Params) []string {
		r, mf := newWarriorRig(t, "arms_warrior", stanceBattle, p)
		setRage(r.g, 600)
		tankFights(r.g, 7, 4, 0)
		var out []string
		out = append(out, actions(r, 100*time.Millisecond, 2200*time.Millisecond, 1600*time.Millisecond, 1600*time.Millisecond)...)
		r.g.mutate(mobA, func(u *objects.Unit) { u.Auras = []objects.Aura{{Name: "Rend", SpellID: spRend, Harmful: true}} })
		out = append(out, actions(r, 1600*time.Millisecond, 400*time.Millisecond)...)
		mf.dodge(mobA, r.g.clk.Now())
		out = append(out, actions(r, 100*time.Millisecond, 1600*time.Millisecond)...)
		setMobHP(r.g, mobA, 15)
		out = append(out, actions(r, 1600*time.Millisecond, 1600*time.Millisecond)...)
		return out
	}
	want, got := play(Params{}), play(Params{Rotation: "arms_warrior"})
	if !slices.Equal(want, got) {
		t.Fatalf("the Go strategies play\n  %v\nthe file plays\n  %v", want, got)
	}
	if !slices.Contains(want, "mortal_strike") || !slices.Contains(want, "execute") || !slices.Contains(want, "rend") {
		t.Fatalf("the scene does not play the rotation: %v", want)
	}
}

func TestShadowPriestFileCastsTheRotation(t *testing.T) {
	g := newFake(spShadowWordPain, spMindBlast, spSmite, spPWShield)
	g.self.Class, g.self.Level = ClassPriest, 30
	g.partyWithTank()
	g.addUnit(mob(mobA, tankG), 22, 0)
	r := newRig(t, g, Params{Strategies: []string{"shadow_priest"}, Rotation: "shadow_priest"})
	got := actions(r, 100*time.Millisecond, 2200*time.Millisecond)
	// Shadow Word: Pain first (the mob carries none), then Mind Blast; Smite is the filler as there is no Mind Flay or Shadowform.
	g.mutate(mobA, func(u *objects.Unit) {
		u.Auras = []objects.Aura{{Name: "Shadow Word: Pain", SpellID: spShadowWordPain, Harmful: true}}
	})
	got = append(got, actions(r, 1600*time.Millisecond)...)
	if got[1] != "shadow_word_pain" || got[2] != "mind_blast" {
		t.Fatalf("%v", got)
	}
}

func TestFireMageFallsBackToFrostboltOnAFireImmuneMob(t *testing.T) {
	g := newFake(spFireball, spFrostbolt, spFireBlast, spFrostNova, spScorch)
	g.partyWithTank()
	buffUp(g)
	g.addUnit(mob(mobA, tankG), 26, 0)
	r := newRig(t, g, Params{Strategies: []string{"fire_mage"}})
	got := actions(r, 100*time.Millisecond, 2200*time.Millisecond)
	if got[1] != "scorch" {
		t.Fatalf("a full-health mob with no Fire Vulnerability starts with Scorch: %v", got)
	}
	g.mutate(mobA, func(u *objects.Unit) {
		u.Auras = []objects.Aura{{Name: "Fire Vulnerability", SpellID: 22959, Stacks: 5, Harmful: true}}
	})
	if d := r.tick(1700 * time.Millisecond); d == nil || d.Action != "fireball" {
		t.Fatalf("%+v", d)
	}
	// The server says the mob is immune: the spell is held back and Frostbolt takes over.
	r.b.mem.hold(spFireball, g.clk.Now().Add(5*time.Second), "immune")
	if d := r.tick(1700 * time.Millisecond); d == nil || d.Action != "frostbolt" {
		t.Fatalf("%+v", d)
	}
}

func TestFireMageMovesAndStillCasts(t *testing.T) {
	g := newFake(spFireball, spFrostbolt, spFireBlast, spFrostNova)
	g.partyWithTank()
	buffUp(g)
	g.addUnit(mob(mobA, tankG), 18, 0)
	g.setSelf(func(u *objects.Unit) { u.Moving = true })
	r := newRig(t, g, Params{Strategies: []string{"fire_mage"}})
	got := actions(r, 100*time.Millisecond, 2200*time.Millisecond)
	if got[1] != "fire_blast_moving" {
		t.Fatalf("a moving mage casts the instant spell: %v", got)
	}
}

// Spells of these tests (internal/spells/meta.tsv.gz).
const (
	spShadowWordPain = 589
	spMindBlast      = 8092
	spSmite          = 585
	spPWShield       = 17
	spFireball       = 133
	spScorch         = 2948
)

var _ = packet.GUID(0)
