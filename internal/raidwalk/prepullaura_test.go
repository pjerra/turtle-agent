package raidwalk

import (
	"strconv"
	"strings"
	"testing"

	"github.com/pjerra/turtle-agent/internal/dungeon"
)

// A --prepull-aura value is "role=<role>:<ids>": spell ids and buff preset names, with "world" the world-raid preset.
func TestParsePrepullAura(t *testing.T) {
	a, err := ParsePrepullAura("role=tank:17626,17538,11348")
	if err != nil {
		t.Fatalf("%v", err)
	}
	if a.Role != PrepullTank {
		t.Fatalf("role %q", a.Role)
	}
	if got := PrepullIDs(a.Buffs); len(got) != 3 || got[0] != 17626 || got[1] != 17538 || got[2] != 11348 {
		t.Fatalf("ids %v", got)
	}
	// The ids are named from the spell table when it knows them, "spell <id>" otherwise.
	if a.Buffs[0].Name != "Flask of the Titans" {
		t.Fatalf("name %q", a.Buffs[0].Name)
	}

	// "world" is the world-raid preset: the classic world buffs without Warchief's Blessing (Horde-only), then the
	// explicit id in flag order.
	w, err := ParsePrepullAura("role=all:world,23768")
	if err != nil {
		t.Fatalf("%v", err)
	}
	got := PrepullIDs(w.Buffs)
	want := []uint32{22888, 15366, 24425, 22817, 22818, 22820, 23768}
	if len(got) != len(want) {
		t.Fatalf("world ids %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("world ids %v, want %v", got, want)
		}
	}
	if strings.Contains(strings.Join(func() []string {
		var n []string
		for _, b := range w.Buffs {
			n = append(n, b.String())
		}
		return n
	}(), ","), "Warchief") {
		t.Fatal("Warchief's Blessing is Horde-only and must not be in the world preset")
	}

	// Case and spaces are tolerated, and the role words are the flag's own.
	if a, err := ParsePrepullAura(" ROLE=Healer : 17627 , 24363 "); err != nil || a.Role != PrepullHealer || len(a.Buffs) != 2 {
		t.Fatalf("%+v %v", a, err)
	}
}

// The refusals: a bad role, an unknown preset, spell id 0, a missing role or list, and an empty id.
func TestParsePrepullAuraRefusals(t *testing.T) {
	for _, tc := range []struct {
		in   string
		want string
	}{
		{"role=wizard:17626", "role \"wizard\" is not one of"},
		{"role=tank:no-such-preset", "unknown preset"},
		{"role=tank:0", "spell id 0"},
		{"tank:17626", "want \"role=<role>:<ids>\""},
		{"role=tank", "want \"role=<role>:<ids>\" after the role"},
		{"role=:17626", "role \"\" is not one of"},
		{"role=tank:17626,,17538", "an empty id"},
		{"role=all:", "an empty id"},
	} {
		_, err := ParsePrepullAura(tc.in)
		if err == nil || !strings.Contains(err.Error(), tc.want) {
			t.Errorf("%q: %v (want %q)", tc.in, err, tc.want)
		}
	}
}

// PlanPrepullAuras refuses a role named twice (one list per role wins) and a role over the aura slots.
func TestPlanPrepullAuras(t *testing.T) {
	set, err := PlanPrepullAuras([]string{"role=tank:17626", "role=all:world"})
	if err != nil {
		t.Fatalf("%v", err)
	}
	// A tank gets its own list and the "all" list, in flag order, without duplicates.
	if got := PrepullIDs(set.For(PrepullTank)); len(got) != 7 || got[0] != 17626 || got[1] != 22888 || got[6] != 22820 {
		t.Fatalf("tank list %v", got)
	}
	// A healer gets the "all" list only here.
	if got := PrepullIDs(set.For(PrepullHealer)); len(got) != 6 {
		t.Fatalf("healer list %v", got)
	}
	// A bot of unknown role gets the "all" list, never a role's.
	if got := PrepullIDs(set.For("")); len(got) != 6 || got[0] != 22888 {
		t.Fatalf("unknown role list %v", got)
	}

	if _, err := PlanPrepullAuras([]string{"role=tank:17626", "role=tank:17538"}); err == nil || !strings.Contains(err.Error(), "named twice") {
		t.Fatalf("duplicate role: %v", err)
	}
	// One list is bounded by dungeon.MaxBuffs at the parse; the union of "all" and a role's own list is what can pass
	// the aura slots a bot has, and the plan refuses that.
	var ids []string
	for i := 1; i <= dungeon.MaxBuffs-1; i++ {
		ids = append(ids, strconv.Itoa(10000+i))
	}
	if _, err := PlanPrepullAuras([]string{"role=all:" + strings.Join(ids, ","), "role=melee:17626,17538"}); err == nil ||
		!strings.Contains(err.Error(), "aura slots") {
		t.Fatalf("over-full union: %v", err)
	}
	// A single list over the bound is the resolver's own refusal.
	if _, err := PlanPrepullAuras([]string{"role=all:" + strings.Join(ids, ",") + ",17626,17538"}); err == nil ||
		!strings.Contains(err.Error(), "at most") {
		t.Fatalf("over-full list: %v", err)
	}
}

// ClassAuraRole is the class's aura role: melee, ranged, caster or healer, "" for a class the table does not know.
func TestClassAuraRole(t *testing.T) {
	for class, want := range map[uint8]string{
		1: PrepullMelee, 4: PrepullMelee, // warrior, rogue
		3: PrepullRanged,                   // hunter
		8: PrepullCaster, 9: PrepullCaster, // mage, warlock
		2: PrepullHealer, 5: PrepullHealer, 7: PrepullHealer, 11: PrepullHealer, // paladin, priest, shaman, druid
		6: "", 10: "", 0: "",
	} {
		if got := ClassAuraRole(class); got != want {
			t.Errorf("class %d: %q, want %q", class, got, want)
		}
	}
}
