package tracecheck

import (
	"slices"
	"strings"
	"testing"

	"github.com/pjerra/turtle-agent/internal/trace"
)

const gPet = "0xf1400001"

// oracleTrace puts a warrior tank in combat for secs frames, alternating Sunder Armor and Shield Slam actions (instants, so they
// are only visible as acts), and a pet that casts on the side and is not a party member.
func oracleTrace(secs int) *build {
	b := newTrace(float64(secs))
	b.f.Party = []trace.Member{{G: gTank, Name: "Tank", Class: "warrior", Role: "tank", Level: 60}}
	b.f.Header.IntervalMS = 1000
	for s := range secs {
		act := "sunder armor"
		if s%2 == 1 {
			act = "shield slam"
		}
		tank := sb(gTank, act, "O")
		pet := sb(gPet, "bite", "O")
		b.frame(int64(s)*1000, "feed", tank, pet)
	}
	return b
}

// TestSpellOracleTrace: a protection warrior tank casts its core Shield Slam and Sunder Armor every fight, so the missing core
// spell Heroic Strike is a warning. 1 Hz frames do not see instants, which the detail says. A bot active for less than the
// threshold is not judged, and a pet is not a party member and is not judged either.
func TestSpellOracleTrace(t *testing.T) {
	cfg := Defaults()
	b := oracleTrace(70)
	c := get(t, Score(b.f, cfg), IDSpellOracle)
	expect(t, c, trace.Pass)
	if c.Count != 1 || !strings.Contains(c.Detail, "Heroic Strike") || !strings.Contains(c.Detail, "1 Hz") {
		t.Fatalf("%+v", c)
	}
	r := Spells(b.f, cfg)
	text := strings.Join(r.Lines(), "\n")
	if !strings.Contains(text, "Spell oracle") || !strings.Contains(text, "Heroic Strike") {
		t.Fatalf("lines:\n%s", text)
	}
	// Sunder Armor and Shield Slam were seen (the acts are lower case and the oracle's names are not), so only Heroic Strike is
	// missing.
	if len(r.Oracle) != 1 || r.Oracle[0].Spec != "protection" || !slices.Equal(r.Oracle[0].Missing, []string{"Heroic Strike"}) {
		t.Fatalf("%+v", r.Oracle)
	}
	for _, f := range r.Oracle {
		if f.Bot == "Pet" || f.Bot == gPet || strings.Contains(strings.ToLower(f.Bot), "pet") {
			t.Fatalf("a pet was judged: %+v", f)
		}
	}

	// 30 s of combat is below the 60 s threshold: nothing to judge yet.
	c = get(t, Score(oracleTrace(30).f, cfg), IDSpellOracle)
	expect(t, c, trace.NA)
	if !strings.Contains(c.Detail, "60") {
		t.Fatalf("%+v", c)
	}
}

// TestOOMInFight: a mana user under 5 % mana for 10 s or more in combat is one warning; 8 s is not.
func TestOOMInFight(t *testing.T) {
	run := func(secs int) *build {
		b := newTrace(float64(secs))
		b.f.Party = []trace.Member{{G: gHealer, Name: "Healer", Class: "priest", Role: "heal", Level: 60}}
		b.f.Header.IntervalMS = 1000
		for s := range secs {
			b.frame(int64(s)*1000, "feed", trace.Bot{G: gHealer, Alive: true, Combat: true, State: "c", HP: 500, MaxHP: 500, Power: 30, MaxPow: 1000, PType: 0})
		}
		return b
	}
	c := get(t, Score(run(12).f, Defaults()), IDOOMInFight)
	expect(t, c, trace.Pass)
	if c.Count != 1 || !strings.Contains(c.Detail, "Healer") || !strings.Contains(c.Detail, "5 %") {
		t.Fatalf("%+v", c)
	}
	c = get(t, Score(run(8).f, Defaults()), IDOOMInFight)
	expect(t, c, trace.Pass)
	if c.Count != 0 {
		t.Fatalf("%+v", c)
	}
	// The threshold is a --set value: 6 s trips a 12 s run.
	cfg := Defaults()
	if err := cfg.Set(IDOOMInFight, 6); err != nil {
		t.Fatal(err)
	}
	c = get(t, Score(run(8).f, cfg), IDOOMInFight)
	if c.Count != 1 {
		t.Fatalf("%+v", c)
	}
}
