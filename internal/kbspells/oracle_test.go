// Package kbspells judges what a bot cast against the Turtle WoW knowledge base (stevemcqueenz) spec lists: the core spells a
// spec presses every fight, the situational ones, and the ones a spec must never cast. The oracle data is generated from the KB
// rotations (internal/kbspells/oracle), the judge is a pure function of a set of casts.
package kbspells

import (
	"os"
	"slices"
	"strings"
	"testing"
	"testing/fstest"
)

// fixtureFS is the hand-written warrior and hunter fixture (internal/kbspells/testdata/oracle, derived by hand from
// ~/TortoiseBots-audit/turtle-kb/classes/yaml and credited to the Turtle WoW knowledge base (stevemcqueenz),
// https://stevemcqueenz.github.io/turtle-knowledge-base-/#/, plus each YAML's own sources) joined with the committed common data.
func fixtureFS(t *testing.T) fstest.MapFS {
	t.Helper()
	m := fstest.MapFS{}
	for _, f := range []struct{ from, to string }{
		{"oracle/_common.json", "_common.json"},
		{"testdata/oracle/warrior.json", "warrior.json"},
		{"testdata/oracle/hunter.json", "hunter.json"},
	} {
		b, err := os.ReadFile(f.from)
		if err != nil {
			t.Fatal(err)
		}
		m[f.to] = &fstest.MapFile{Data: b}
	}
	return m
}

func fixtureOracle(t *testing.T) *Oracle {
	t.Helper()
	o, err := Load(fixtureFS(t))
	if err != nil {
		t.Fatal(err)
	}
	return o
}

func TestLoadFixture(t *testing.T) {
	o := fixtureOracle(t)
	if len(o.Classes["warrior"]) != 4 || len(o.Classes["hunter"]) != 2 {
		t.Fatalf("classes: %+v", o.Classes)
	}
	if o.Common.Limits.MinFightMS != 10000 || o.Common.Limits.SunderOKPer10s != 1.5 || o.Common.Limits.SunderWarnPer10s != 0.8 {
		t.Fatalf("limits: %+v", o.Common.Limits)
	}
}

// TestCommonTauntsEqualDebugger pins the six taunts of the fight debugger (TortoiseBots tools/observability
// commander-fight-debugger.md §2: TAUNTS = ["Taunt","Growl","Hand of Reckoning","Mocking Blow","Challenging Shout",
// "Challenging Roar"]) in its order, so the oracle and the debugger count the same spells.
func TestCommonTauntsEqualDebugger(t *testing.T) {
	want := []string{"Taunt", "Growl", "Hand of Reckoning", "Mocking Blow", "Challenging Shout", "Challenging Roar"}
	got := fixtureOracle(t).Common.Taunts
	if len(got) != len(want) {
		t.Fatalf("taunts %v", got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("taunts %v, want %v", got, want)
		}
	}
}

// kbSpecs are the 31 PvE specs of the Turtle WoW knowledge base the generated oracle must carry, per class (the spec's table
// K2a-K2d; rogue combat-tank is not one: a rogue has no taunt in the core).
var kbSpecs = map[string][]string{
	"warrior": {"arms", "fury", "fury_tank", "protection"},
	"rogue":   {"assassination", "combat", "subtlety"},
	"mage":    {"arcane", "fire", "frost"},
	"warlock": {"affliction", "demonology", "destruction_fire", "destruction_shadow"},
	"hunter":  {"beast_mastery", "marksmanship", "survival"},
	"priest":  {"discipline", "holy", "shadow"},
	"paladin": {"holy", "protection", "retribution"},
	"druid":   {"balance", "feral_bear", "feral_cat", "restoration"},
	"shaman":  {"elemental", "enhancement", "enhancement_tank", "restoration"},
}

// TestEmbeddedOracleLoads: the shipped default is the generated per-class data of internal/kbspells/oracle, not the hand-written
// warrior/hunter fixture: every class is there, each carries exactly the 31 PvE specs, and no two specs of a class claim the same
// signature spell (the judge infers a dps spec from those casts).
func TestEmbeddedOracleLoads(t *testing.T) {
	o := Default()
	if len(o.Classes) != len(kbSpecs) {
		var got []string
		for c := range o.Classes {
			got = append(got, c)
		}
		slices.Sort(got)
		t.Fatalf("%d classes %v, want %d", len(o.Classes), got, len(kbSpecs))
	}
	total := 0
	for class, specs := range kbSpecs {
		have := o.Classes[class]
		if len(have) != len(specs) {
			var got []string
			for _, s := range have {
				got = append(got, s.Spec)
			}
			t.Fatalf("%s: specs %v, want %v", class, got, specs)
		}
		sig := map[string]string{}
		for _, s := range have {
			if _, ok := o.spec(class, s.Spec); !ok {
				t.Fatalf("%s: no spec %s", class, s.Spec)
			}
			for _, n := range s.Signature {
				k := strings.ToLower(n)
				if prev, dup := sig[k]; dup {
					t.Fatalf("%s: %s is a signature of both %s and %s", class, n, prev, s.Spec)
				}
				sig[k] = s.Spec
			}
		}
		total += len(have)
	}
	if total != 31 {
		t.Fatalf("%d specs, want the 31 of the KB", total)
	}
}

func TestLoadRejectsBadData(t *testing.T) {
	base := fixtureFS(t)
	bad := fstest.MapFS{}
	for k, v := range base {
		bad[k] = v
	}
	bad["warrior.json"] = &fstest.MapFile{Data: []byte(`{"class":"warrior","specs":[{"spec":"fury","role":"dps","rotation":"r","core":["Not A Spell"],"signature":["Bloodthirst"]},{"spec":"arms","role":"dps","rotation":"r","core":["Whirlwind"],"signature":["Bloodthirst"]}]}`)}
	if _, err := Load(bad); err == nil {
		t.Fatal("a name the spell data lacks and a doubled signature must be refused")
	}
}
