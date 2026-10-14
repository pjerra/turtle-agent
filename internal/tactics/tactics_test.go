package tactics

import (
	"encoding/json"
	"strings"
	"testing"
)

func parse(t *testing.T, s string) (Tactics, error) {
	t.Helper()
	var x Tactics
	return x, json.Unmarshal([]byte(s), &x)
}

func TestSpellsAreIdsOrNames(t *testing.T) {
	x, err := parse(t, `{"interrupt":[16006,"Lightning Bolt"],"dispel":["Fevered Plague"],"move_out":[{"cast":"Fire Nova","yards":12},{"ground":10132}]}`)
	if err != nil {
		t.Fatal(err)
	}
	if x.Interrupt[0].ID != 16006 || x.Interrupt[1].Name != "Lightning Bolt" || x.MoveOut[0].Cast.Name != "Fire Nova" || x.MoveOut[1].Ground.ID != 10132 {
		t.Fatalf("%+v", x)
	}
	back, _ := json.Marshal(x.Interrupt)
	if string(back) != `[16006,"Lightning Bolt"]` {
		t.Fatalf("%s", back)
	}
	for _, bad := range []string{`{"interrupt":[0]}`, `{"interrupt":[""]}`, `{"interrupt":[-3]}`, `{"interrupt":[1.5]}`, `{"interrupt":[true]}`} {
		if _, err := parse(t, bad); err == nil {
			t.Errorf("%s was read", bad)
		}
	}
}

func TestSpellMatching(t *testing.T) {
	names := map[uint32]string{16006: "Chain Lightning", 8600: "Fevered Plague"}
	nameOf := func(id uint32) string { return names[id] }
	if !(SpellRef{ID: 16006}).Matches(16006, nameOf) || (SpellRef{ID: 16006}).Matches(8600, nameOf) {
		t.Error("by id")
	}
	if !(SpellRef{Name: "chain lightning"}).Matches(16006, nameOf) || (SpellRef{Name: "chain lightning"}).Matches(8600, nameOf) {
		t.Error("by name, any case")
	}
	if !(SpellRef{Name: "fevered plague"}).MatchesAura(1, "Fevered Plague", nameOf) || !(SpellRef{Name: "fevered plague"}).MatchesAura(8600, "", nameOf) {
		t.Error("an aura by its own name or its spell's")
	}
}

func TestValidate(t *testing.T) {
	has := func(n string) bool { return n != "Nothing" }
	ok := func(s string) {
		t.Helper()
		x, err := parse(t, s)
		if err == nil {
			err = x.Validate(has)
		}
		if err != nil {
			t.Errorf("%s: %v", s, err)
		}
	}
	bad := func(s, want string) {
		t.Helper()
		x, err := parse(t, s)
		if err == nil {
			err = x.Validate(has)
		}
		if err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("%s: %v, want %q", s, err, want)
		}
	}
	ok(`{}`)
	ok(`{"kill_first":[7209],"never_target":[9001],"interrupt":["Chain Lightning"],"move_out":[{"cast":"Fire Nova"},{"ground":10}]}`)
	bad(`{"kill_first":[0]}`, "entry 0")
	bad(`{"kill_first":[5],"never_target":[5]}`, "both")
	bad(`{"interrupt":["Nothing"]}`, `no spell is named "Nothing"`)
	bad(`{"dispel":["Nothing"]}`, "no spell is named")
	bad(`{"move_out":[{}]}`, "exactly one")
	bad(`{"move_out":[{"cast":1,"ground":2}]}`, "exactly one")
	bad(`{"move_out":[{"ground":2,"yards":5}]}`, "yards goes with cast")
	bad(`{"move_out":[{"cast":2,"yards":99}]}`, "yards")
	bad(`{"move_out":[{"cast":"Nothing"}]}`, "no spell is named")
	long := `{"kill_first":[` + strings.TrimSuffix(strings.Repeat("1,", MaxList+1), ",") + `]}`
	bad(long, "at most")
}

func TestEmptyAndYards(t *testing.T) {
	var nilT *Tactics
	if !nilT.Empty() || !(&Tactics{}).Empty() || (&Tactics{KillFirst: []uint32{1}}).Empty() {
		t.Error("Empty")
	}
	if (MoveOut{}).YardsOf() != DefaultYard || (MoveOut{Yards: 7}).YardsOf() != 7 {
		t.Error("YardsOf")
	}
}
