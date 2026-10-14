package objects

import "testing"

func TestCCMechanics(t *testing.T) {
	// 22 is persuade, 23 turn (SpellDefines.h Mechanics).
	if ccMechanics[22] || !ccMechanics[23] {
		t.Fatalf("%v", ccMechanics)
	}
}

func TestCrowdControlled(t *testing.T) {
	for _, c := range []struct {
		name string
		a    []Aura
		want bool
	}{
		{"no auras", nil, false},
		{"polymorph by id", []Aura{{SpellID: 118}}, true},
		{"sap by id", []Aura{{SpellID: 6770}}, true},
		{"shackle undead by id", []Aura{{SpellID: 9484}}, true},
		{"hibernate by id", []Aura{{SpellID: 2637}}, true},
		{"banish by id", []Aura{{SpellID: 710}}, true},
		{"freezing trap by id", []Aura{{SpellID: 3355}}, true},
		{"polymorph variant by name", []Aura{{SpellID: 999999, Name: "Polymorph: Pig"}}, true},
		{"rank in the name", []Aura{{SpellID: 999999, Name: "Sap"}}, true},
		{"fireball", []Aura{{SpellID: 133}}, false},
		{"frost nova is a root, not a sheep", []Aura{{SpellID: 122}}, false},
		{"a name that only starts with sap", []Aura{{SpellID: 999999, Name: "Sapphire Ward"}}, false},
		{"gouge by id is no sheep", []Aura{{SpellID: 1776}}, false},
		{"gouge by name is no sheep", []Aura{{SpellID: 999999, Name: "Gouge"}}, false},
		{"turn undead by name", []Aura{{SpellID: 999999, Name: "Turn Undead"}}, true},
		{"cc among others", []Aura{{SpellID: 133}, {SpellID: 118}}, true},
	} {
		if got := (Unit{Auras: c.a}).CrowdControlled(); got != c.want {
			t.Errorf("%s: %v, want %v", c.name, got, c.want)
		}
	}
}
