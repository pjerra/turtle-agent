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
