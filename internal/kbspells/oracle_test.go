// Package kbspells judges what a bot cast against the Turtle WoW knowledge base (stevemcqueenz) spec lists: the core spells a
// spec presses every fight, the situational ones, and the ones a spec must never cast. The oracle data is generated from the KB
// rotations (internal/kbspells/oracle), the judge is a pure function of a set of casts.
package kbspells

import (
	"os"
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
