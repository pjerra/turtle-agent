package raidwalk

import (
	"fmt"
	"os"
	"path/filepath"
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

// ClassName is the name the hire data and the fire agree on: "" for an id no class has.
func TestClassName(t *testing.T) {
	for class, want := range map[uint8]string{
		1: "warrior", 2: "paladin", 3: "hunter", 4: "rogue", 5: "priest",
		7: "shaman", 8: "mage", 9: "warlock", 11: "druid",
		0: "", 6: "", 10: "",
	} {
		if got := ClassName(class); got != want {
			t.Errorf("class %d: %q, want %q", class, got, want)
		}
	}
}

// A --prepull-aura value that is not a role= list names a tb-prepull-auras file (dungeons/prepull/zg-preraid.json):
// one list per role under "roles", so a walk names the file instead of repeating six values. The file's roles join
// the set in PrepullRoles order, and a role named by both a file and a value is refused.
func TestPrepullAuraFile(t *testing.T) {
	const sample = "../../dungeons/prepull/zg-preraid.json"
	raw, err := os.ReadFile(sample)
	if err != nil {
		t.Fatalf("%s: %v", sample, err)
	}
	list, err := ParsePrepullAuraFile(sample, raw)
	if err != nil {
		t.Fatalf("%v", err)
	}
	if len(list) != len(PrepullRoles) {
		t.Fatalf("%d roles, want %d: %+v", len(list), len(PrepullRoles), list)
	}
	for i, role := range PrepullRoles {
		if list[i].Role != role {
			t.Fatalf("role %d is %q, want %q (PrepullRoles order)", i, list[i].Role, role)
		}
	}
	if got := PrepullIDs(list[0].Buffs); len(got) != 5 || got[0] != 17626 || got[4] != 25661 {
		t.Fatalf("tank ids %v", got)
	}
	if got := PrepullIDs(list[len(list)-1].Buffs); len(got) != 7 || got[0] != 22888 || got[6] != 23768 {
		t.Fatalf("all ids %v", got)
	}

	// The same file through the flag: its roles are the plan's lists, exactly as if each had been typed.
	plan, err := PlanPrepullAuras([]string{sample})
	if err != nil {
		t.Fatalf("plan: %v", err)
	}
	if len(plan) != len(PrepullRoles) || PrepullIDs(plan.For(PrepullTank))[0] != 17626 {
		t.Fatalf("plan %+v", plan)
	}
	// A file and a role= value for the same role are ambiguous.
	if _, err := PlanPrepullAuras([]string{sample, "role=tank:17626"}); err == nil || !strings.Contains(err.Error(), "named twice") {
		t.Fatalf("file plus its role: %v", err)
	}
	// A file and a value for a role the file does not name merge, with the file's lists first.
	dir := t.TempDir()
	tankOnly := filepath.Join(dir, "tank.json")
	if err := os.WriteFile(tankOnly, []byte(`{"format":"tb-prepull-auras","version":1,"roles":{"tank":{"ids":[17626]}}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	plan, err = PlanPrepullAuras([]string{"role=melee:10000", tankOnly})
	if err != nil {
		t.Fatalf("%v", err)
	}
	if got := PrepullIDs(plan.For(PrepullMelee)); len(got) != 1 || got[0] != 10000 {
		t.Fatalf("merged melee list %v", got)
	}
	if got := PrepullIDs(plan.For(PrepullTank)); len(got) != 1 || got[0] != 17626 {
		t.Fatalf("merged tank list %v", got)
	}

	write := func(name, body string) string {
		path := filepath.Join(dir, name)
		if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
		return path
	}
	doc := func(format string, version int, roles string) string {
		return fmt.Sprintf(`{"format":%q,"version":%d,"roles":%s}`, format, version, roles)
	}
	for _, tc := range []struct {
		name string
		body string
		want string
	}{
		{"bad format", doc("tb-raid-roster", 1, `{"tank":{"ids":[17626]}}`), "format"},
		{"bad version", doc(PrepullFileFormat, 2, `{"tank":{"ids":[17626]}}`), "version"},
		{"unknown role", doc(PrepullFileFormat, 1, `{"wizard":{"ids":[17626]}}`), "is not one of"},
		{"no ids", doc(PrepullFileFormat, 1, `{"tank":{"ids":[]}}`), "names no id"},
		{"id 0", doc(PrepullFileFormat, 1, `{"tank":{"ids":[0]}}`), "spell id 0"},
		{"no roles", doc(PrepullFileFormat, 1, `{}`), "names no role"},
		{"not json", `not json`, "prepull-aura"},
	} {
		path := write("f.json", tc.body)
		if _, err := PlanPrepullAuras([]string{path}); err == nil || !strings.Contains(err.Error(), tc.want) {
			t.Errorf("%s: err %v (want %q)", tc.name, err, tc.want)
		}
	}
	if _, err := PlanPrepullAuras([]string{filepath.Join(dir, "absent.json")}); err == nil || !strings.Contains(err.Error(), "no such file") {
		t.Fatalf("absent file: %v", err)
	}
}

// BotStateAuraRole reads the module's own per-bot `specs=` list: a mixed batch says nothing about which priest heals,
// but the module's kit does. A spec naming two roles or none fails closed, never a class guess.
func TestBotStateAuraRole(t *testing.T) {
	for _, tc := range []struct {
		name  string
		class string
		specs string
		want  string
	}{
		{"holy priest", "priest", "holy", PrepullHealer},
		{"shadow priest", "priest", "shadow", PrepullCaster},
		{"discipline priest with offdps", "priest", "discipline+offdps", PrepullHealer},
		{"holy paladin", "paladin", "holy", PrepullHealer},
		{"ret paladin", "paladin", "retribution", PrepullMelee},
		{"prot paladin", "paladin", "protection", PrepullTank},
		{"resto druid", "druid", "restoration", PrepullHealer},
		{"feral cat", "druid", "dps feral", PrepullMelee},
		{"feral bear", "druid", "bear", PrepullTank},
		{"balance druid", "druid", "balance+offdps", PrepullCaster},
		{"enh shaman", "shaman", "enhancement", PrepullMelee},
		{"ele shaman", "shaman", "elemental", PrepullCaster},
		{"resto shaman", "shaman", "restoration", PrepullHealer},
		{"tank shaman", "shaman", "tank shaman", PrepullTank},
		{"prot warrior", "warrior", "protection", PrepullTank},
		{"arms warrior", "warrior", "arms", PrepullMelee},
		{"marks hunter", "hunter", "marksmanship", PrepullRanged},
		{"combat rogue", "rogue", "combat", PrepullMelee},
		{"frost mage", "mage", "frost", PrepullCaster},
		{"demo warlock", "warlock", "demonology", PrepullCaster},
	} {
		if got, why := BotStateAuraRole(tc.class, tc.specs); got != tc.want {
			t.Errorf("%s: BotStateAuraRole(%q, %q) = %q (%s), want %q", tc.name, tc.class, tc.specs, got, why, tc.want)
		}
	}
	for _, tc := range []struct {
		name  string
		class string
		specs string
		want  string
	}{
		{"no kit", "priest", "", "no spec strategy"},
		{"two roles", "priest", "holy+shadow", "more than one role"},
		{"unknown strategy", "mage", "moonfire", "no aura role covers"},
		{"unknown class", "death knight", "frost", "no spec table"},
	} {
		got, why := BotStateAuraRole(tc.class, tc.specs)
		if got != "" || !strings.Contains(why, tc.want) {
			t.Errorf("%s: got %q (%s), want no role and %q", tc.name, got, why, tc.want)
		}
	}
}
