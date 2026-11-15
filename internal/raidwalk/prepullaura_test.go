package raidwalk

import (
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"

	"github.com/pjerra/turtle-agent/internal/dungeon"
	"github.com/pjerra/turtle-agent/internal/planexport"
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

// The sample tb-prepull-auras files (dungeons/prepull/zg-preraid.json and
// dungeons/prepull/onyxia-preraid.json): one non-empty id list per role in PrepullRoles order, every id > 0. The
// read fails when the file is missing, so deleting one breaks this test.
func TestPrepullAuraSampleFiles(t *testing.T) {
	for _, sample := range []struct {
		path    string
		tankLen int
		allLen  int
	}{
		{"../../dungeons/prepull/zg-preraid.json", 5, 7},
		{"../../dungeons/prepull/onyxia-preraid.json", 5, 8},
	} {
		raw, err := os.ReadFile(sample.path)
		if err != nil {
			t.Fatalf("%s: %v", sample.path, err)
		}
		list, err := ParsePrepullAuraFile(sample.path, raw)
		if err != nil {
			t.Fatalf("%v", err)
		}
		if len(list) != len(PrepullRoles) {
			t.Fatalf("%s: %d roles, want %d: %+v", sample.path, len(list), len(PrepullRoles), list)
		}
		for i, role := range PrepullRoles {
			if list[i].Role != role {
				t.Fatalf("%s: role %d is %q, want %q (PrepullRoles order)", sample.path, i, list[i].Role, role)
			}
			if len(PrepullIDs(list[i].Buffs)) == 0 {
				t.Fatalf("%s: role %q names no id", sample.path, role)
			}
			for _, id := range PrepullIDs(list[i].Buffs) {
				if id == 0 {
					t.Fatalf("%s: role %q names spell id 0", sample.path, role)
				}
			}
		}
		if got := PrepullIDs(list[0].Buffs); len(got) != sample.tankLen || got[0] != 17626 || got[4] != 25661 {
			t.Fatalf("%s: tank ids %v", sample.path, got)
		}
		if got := PrepullIDs(list[len(list)-1].Buffs); len(got) != sample.allLen || got[0] != 22888 || !slices.Contains(got, 23768) {
			t.Fatalf("%s: all ids %v", sample.path, got)
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

// BotStateAuraRoleUnclassed is the same read for a hire whose unit row the store does not hold, so no class keys the
// table: a strategy every class that names it agrees on gives that role ("protection" a tank, "holy" a healer), and
// everything else fails closed - a strategy no class names, a kit naming none or two roles, and a strategy a future
// table would split on roles.
func TestBotStateAuraRoleUnclassed(t *testing.T) {
	for _, tc := range []struct{ specs, want string }{
		{"protection", PrepullTank},
		{"holy", PrepullHealer},
		{"protection+offdps", PrepullTank},
		{"combat", PrepullMelee},
		{"restoration", PrepullHealer},
	} {
		if got, why := BotStateAuraRoleUnclassed(tc.specs); got != tc.want {
			t.Errorf("BotStateAuraRoleUnclassed(%q) = %q (%s), want %q", tc.specs, got, why, tc.want)
		}
	}
	for _, tc := range []struct{ name, specs, want string }{
		{"no kit", "", "no spec strategy"},
		{"two roles", "holy+shadow", "more than one role"},
		{"unknown strategy", "moonfire", "no aura role covers"},
	} {
		if got, why := BotStateAuraRoleUnclassed(tc.specs); got != "" || !strings.Contains(why, tc.want) {
			t.Errorf("%s: got %q (%s), want no role and %q", tc.name, got, why, tc.want)
		}
	}
	// A strategy two classes would split on roles is no role at all: the guard a future table edit meets.
	moduleSpecRoles["test class"] = map[string]string{"combat": PrepullCaster}
	defer delete(moduleSpecRoles, "test class")
	if got, why := BotStateAuraRoleUnclassed("combat"); got != "" || !strings.Contains(why, "different roles") {
		t.Errorf("a split strategy gave %q (%s), want no role", got, why)
	}
	if got, _ := BotStateAuraRoleUnclassed("protection"); got != PrepullTank {
		t.Errorf("protection with a split table gave %q, want %q", got, PrepullTank)
	}
}

// prepullKindRows is a one-leg plan of the given row kinds (1-based step == index+1).
func prepullKindRows(kinds ...string) planexport.Rows {
	rows := planexport.Rows{}
	for _, k := range kinds {
		rows.Steps = append(rows.Steps, planexport.StepRow{Plan: "p", Leg: 1, Kind: k, Entry: 1})
	}
	return rows
}

// Walk 104: the ZG gong (use_object 180526, an open row) summoned Arlokk and engaged the raid on the spot, so her
// boss row found the party already fighting and the prepull fire sent nothing (exit 18, "combat started"). The fire
// stands in front of the trigger when only the trigger, waits and the approach's moves stand between it and the boss
// row; a pull, another kind of row, another leg's rows or a trigger that is the leg's first row keep the boss row's
// own step (nothing about a plan this rule does not fit changes).
func TestPrepullFirePointBeforeASummoningTrigger(t *testing.T) {
	move, open, proof, gossip := planexport.KindMove, planexport.KindOpen, planexport.KindOpenProof, planexport.KindGossip
	for _, tc := range []struct {
		name  string
		kinds []string
		boss  int
		fire  int
		trig  int
		kind  string
	}{
		// Walk 104's own shape, narrowed to the leg's tail: the gong, her arena's approach move, her boss row.
		{"open then the approach move", []string{move, move, open, move, planexport.KindBoss}, 5, 3, 3, open},
		{"open_proof then the approach", []string{move, proof, move, planexport.KindBoss}, 4, 2, 2, proof},
		{"gossip then a wait", []string{move, move, gossip, planexport.KindWaitObject, planexport.KindBoss}, 5, 3, 3, gossip},
		{"the nearest trigger wins", []string{move, gossip, move, open, planexport.KindBoss}, 5, 4, 4, open},
		// A plain boss row keeps its own fire point.
		{"no trigger", []string{move, move, move, planexport.KindBoss}, 4, 4, 0, ""},
		// A pull between the trigger and the boss row means the party fights a pack there: the fire stays on the boss row.
		{"a pull after the trigger", []string{move, open, planexport.KindPull, move, planexport.KindBoss}, 5, 5, 0, ""},
		{"a loot after the trigger", []string{move, open, planexport.KindLoot, planexport.KindBoss}, 4, 4, 0, ""},
		{"a use_item after the trigger", []string{move, open, planexport.KindUseItem, planexport.KindBoss}, 4, 4, 0, ""},
		// The trigger is the leg's first row: the fire would stand on the previous leg's last row.
		{"the trigger opens the leg", []string{open, move, planexport.KindBoss}, 3, 3, 0, ""},
	} {
		rows := prepullKindRows(tc.kinds...)
		fire, trig, kind := PrepullFirePoint(rows, tc.boss)
		if fire != tc.fire || trig != tc.trig || kind != tc.kind {
			t.Errorf("%s: fire %d trigger %d kind %q, want %d %d %q", tc.name, fire, trig, kind, tc.fire, tc.trig, tc.kind)
		}
	}
	// A step that is not a boss row, and a step outside the rows, have no fire point.
	rows := prepullKindRows(move, open, planexport.KindBoss)
	if fire, trig, kind := PrepullFirePoint(rows, 2); fire != 2 || trig != 0 || kind != "" {
		t.Errorf("a move row: fire %d trigger %d kind %q", fire, trig, kind)
	}
	if fire, trig, kind := PrepullFirePoint(rows, 9); fire != 9 || trig != 0 || kind != "" {
		t.Errorf("out of range: fire %d trigger %d kind %q", fire, trig, kind)
	}
}
