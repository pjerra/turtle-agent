package dungeon

import (
	"fmt"
	"os"
	"slices"
	"strings"
	"testing"
)

const validPlan = `{
  "version": 1, "name": "test", "map": 70,
  "entrance": {"x": 1, "y": 2, "z": 3},
  "legs": [
    {"name": "a", "to": {"x": 4, "y": 5, "z": 6},
     "pull_groups": [{"entries": [10, 10, 11], "positions": [{"x": 7, "y": 8, "z": 9}, {"x": 7, "y": 9, "z": 9}]}],
     "skip": [20],
     "boss": {"entry": 30, "position": {"x": 1, "y": 1, "z": 1}, "first": [31]}},
    {"to": {"x": 4, "y": 5, "z": 6},
     "steps": [
       {"use_object": {"entry": 100, "near": {"x": 1, "y": 1, "z": 1}, "range": 12, "stand": {"x": 2, "y": 2, "z": 2}}},
       {"wait_for": {"gameobject": {"entry": 101, "state": "active"}, "timeout_s": 30}},
       {"wait_for": {"hostiles_gone": {"radius": 25}}}
     ]}
  ]
}`

func TestParseValid(t *testing.T) {
	p, err := Parse("t.json", []byte(validPlan))
	if err != nil {
		t.Fatal(err)
	}
	if p.Name != "test" || p.Map != 70 || p.Entrance != (Point{1, 2, 3}) || len(p.Legs) != 2 {
		t.Fatalf("%+v", p)
	}
	l := p.Legs[0]
	if len(l.PullGroups) != 1 || len(l.PullGroups[0].Entries) != 3 || len(l.PullGroups[0].Positions) != 2 || l.Skip[0] != 20 ||
		l.Boss.Entry != 30 || l.Boss.First[0] != 31 || l.Boss.Position != (Point{1, 1, 1}) {
		t.Fatalf("%+v", l)
	}
	u := p.Legs[1].Steps[0].UseObject
	if u.Entry != 100 || *u.Near != (Point{1, 1, 1}) || u.Range != 12 || *u.Stand != (Point{2, 2, 2}) {
		t.Fatalf("%+v", u)
	}
	if w := p.Legs[1].Steps[1].WaitFor; w.TimeoutS != 30 || w.GameObject.State != "active" || w.GameObject.Entry != 101 {
		t.Fatalf("%+v", w)
	}
	if w := p.Legs[1].Steps[2].WaitFor; w.TimeoutS != DefaultTimeoutS || w.HostilesGone.Radius != 25 || w.HostilesGone.Center != nil {
		t.Fatalf("the timeout defaults: %+v", w)
	}
}

// Each mistake is reported with the leg and the field, and one pass reports all of them.
func TestParseErrors(t *testing.T) {
	step := func(s string) string {
		return `{"version":1,"name":"t","map":1,"entrance":{"x":0,"y":0,"z":0},"legs":[{"to":{"x":0,"y":0,"z":0},"steps":[` + s + `]}]}`
	}
	leg := func(s string) string {
		return `{"version":1,"name":"t","map":1,"entrance":{"x":0,"y":0,"z":0},"legs":[{"to":{"x":0,"y":0,"z":0},"pull_groups":[{"entries":[1],"positions":[{"x":0,"y":0,"z":0}]}]},` + s + `]}`
	}
	cases := []struct{ name, in, want string }{
		{"no version", `{"name":"t","map":1,"entrance":{"x":0,"y":0,"z":0},"legs":[]}`, "version: required"},
		{"future version", `{"version":2}`, "version: 2 is not supported"},
		{"no name", `{"version":1,"map":1}`, "name: required"},
		{"no map", `{"version":1,"name":"t"}`, "map: required"},
		{"no entrance", `{"version":1,"name":"t","map":1}`, "entrance: required"},
		{"entrance z", `{"version":1,"name":"t","map":1,"entrance":{"x":0,"y":0}}`, "entrance.z: required"},
		{"no legs", `{"version":1,"name":"t","map":1,"entrance":{"x":0,"y":0,"z":0},"legs":[]}`, "legs: needs at least one leg"},
		{"fail_text with a line break", step(`{"wait_for":{"hostiles_gone":{"radius":5},"fail_text":"a\nb"}}`), "wait_for.fail_text"},
		{"unknown top field", `{"version":1,"colour":1}`, `unknown field "colour"`},
		{"not json", `{`, "t.json:"},
		{"two documents", validPlan + validPlan, "more than one JSON value"},
		{"unknown leg field", leg(`{"to":{"x":0,"y":0,"z":0},"bosses":[]}`), `legs[1]: json: unknown field "bosses"`},
		{"leg without to", leg(`{"boss":{"entry":1,"position":{"x":0,"y":0,"z":0}}}`), "legs[1].to: required"},
		{"empty leg", leg(`{"to":{"x":0,"y":0,"z":0}}`), "legs[1]: has no pull_groups, steps, boss or after_boss"},
		{"huge coordinate", leg(`{"to":{"x":1e9,"y":0,"z":0},"steps":[{"wait_for":{"hostiles_gone":{"radius":5}}}]}`), "legs[1].to.x: 1e+09 is not a map coordinate"},
		{"group no entries", leg(`{"to":{"x":0,"y":0,"z":0},"pull_groups":[{"entries":[],"positions":[{"x":0,"y":0,"z":0}]}]}`), "legs[1].pull_groups[0].entries: needs at least one entry"},
		{"entry zero", leg(`{"to":{"x":0,"y":0,"z":0},"pull_groups":[{"entries":[5,0],"positions":[{"x":0,"y":0,"z":0}]}]}`), "legs[1].pull_groups[0].entries[1]: 0 is no creature entry"},
		{"group no positions", leg(`{"to":{"x":0,"y":0,"z":0},"pull_groups":[{"entries":[5]}]}`), "legs[1].pull_groups[0].positions: needs at least one position"},
		{"too many positions", leg(`{"to":{"x":0,"y":0,"z":0},"pull_groups":[{"entries":[5],"positions":[{"x":0,"y":0,"z":0},{"x":1,"y":0,"z":0}]}]}`), "legs[1].pull_groups[0].positions: 2 positions for 1 entries"},
		{"position y", leg(`{"to":{"x":0,"y":0,"z":0},"pull_groups":[{"entries":[5],"positions":[{"x":0,"z":0}]}]}`), "legs[1].pull_groups[0].positions[0].y: required"},
		{"boss no entry", leg(`{"to":{"x":0,"y":0,"z":0},"boss":{"position":{"x":0,"y":0,"z":0}}}`), "legs[1].boss.entry: required"},
		{"boss no position", leg(`{"to":{"x":0,"y":0,"z":0},"boss":{"entry":4}}`), "legs[1].boss.position: required"},
		{"first holds boss", leg(`{"to":{"x":0,"y":0,"z":0},"boss":{"entry":4,"first":[4],"position":{"x":0,"y":0,"z":0}}}`), "legs[1].boss.first[0]: the boss (4) is killed last"},
		{"skip holds boss", leg(`{"to":{"x":0,"y":0,"z":0},"skip":[4],"boss":{"entry":4,"position":{"x":0,"y":0,"z":0}}}`), "legs[1].skip[0]: skips the boss (4)"},
		{"skip also pulled", leg(`{"to":{"x":0,"y":0,"z":0},"skip":[1],"pull_groups":[{"entries":[1],"positions":[{"x":0,"y":0,"z":0}]}]}`), "legs[1].skip[0]: 1 is also in pull_groups[0].entries"},
		{"leave no entry", leg(`{"to":{"x":0,"y":0,"z":0},"leave":[{"at":{"x":5,"y":5,"z":0},"why":"island"}],"pull_groups":[{"entries":[5],"positions":[{"x":0,"y":0,"z":0}]}]}`), "legs[1].leave[0].entry: required"},
		{"leave no why", leg(`{"to":{"x":0,"y":0,"z":0},"leave":[{"entry":9,"at":{"x":5,"y":5,"z":0}}],"pull_groups":[{"entries":[5],"positions":[{"x":0,"y":0,"z":0}]}]}`), "legs[1].leave[0].why: required"},
		{"leave no point", leg(`{"to":{"x":0,"y":0,"z":0},"leave":[{"entry":9,"why":"island"}],"pull_groups":[{"entries":[5],"positions":[{"x":0,"y":0,"z":0}]}]}`), "legs[1].leave[0].at: required"},
		{"leave also pulled", leg(`{"to":{"x":0,"y":0,"z":0},"leave":[{"entry":5,"at":{"x":1,"y":1,"z":0},"why":"island"}],"pull_groups":[{"entries":[5],"positions":[{"x":0,"y":0,"z":0}]}]}`), "legs[1].leave[0]: pull_groups[0].positions[0] is this spawn"},
		{"leave unknown field", leg(`{"to":{"x":0,"y":0,"z":0},"leave":[{"entry":9,"at":{"x":5,"y":5,"z":0},"why":"x","radius":3}],"pull_groups":[{"entries":[5],"positions":[{"x":0,"y":0,"z":0}]}]}`), `unknown field "radius"`},
		{"skip also first", leg(`{"to":{"x":0,"y":0,"z":0},"skip":[3],"boss":{"entry":4,"first":[3],"position":{"x":0,"y":0,"z":0}}}`), "legs[1].skip[0]: 3 is also in boss.first"},
		{"empty step", step(`{}`), "legs[0].steps[0]: needs use_object, wait_for, loot_object, use_item, ritual, area_trigger or gossip"},
		{"two things", step(`{"use_object":{"entry":1},"wait_for":{"hostiles_gone":{"radius":5}}}`), "legs[0].steps[0]: has more than one of use_object, wait_for, loot_object, use_item, ritual, area_trigger and gossip"},
		{"unknown step field", step(`{"click":{"entry":1}}`), `legs[0].steps[0]: json: unknown field "click"`},
		{"use nothing", step(`{"use_object":{}}`), "legs[0].steps[0].use_object: needs an entry, a near position, or both"},
		{"range without near", step(`{"use_object":{"entry":1,"range":5}}`), "legs[0].steps[0].use_object.range: only means something with near"},
		{"range too big", step(`{"use_object":{"near":{"x":0,"y":0,"z":0},"range":5000}}`), "legs[0].steps[0].use_object.range: 5000 yards is outside"},
		{"bad stand", step(`{"use_object":{"entry":1,"stand":{"x":0,"y":0}}}`), "legs[0].steps[0].use_object.stand.z: required"},
		{"wait nothing", step(`{"wait_for":{}}`), "legs[0].steps[0].wait_for: needs gameobject, hostiles_gone or unit"},
		{"wait both", step(`{"wait_for":{"gameobject":{"entry":1,"state":"active"},"hostiles_gone":{"radius":5}}}`), "legs[0].steps[0].wait_for: has both gameobject and hostiles_gone"},
		{"gone_ok is a flag", step(`{"wait_for":{"gameobject":{"entry":1,"state":"active","gone_ok":"yes"}}}`), "gone_ok"},
		{"bad state", step(`{"wait_for":{"gameobject":{"entry":1,"state":"open"}}}`), `legs[0].steps[0].wait_for.gameobject.state: "open" is none of ready, active, active_alt`},
		{"no state", step(`{"wait_for":{"gameobject":{"entry":1}}}`), `wait_for.gameobject.state: "" is none of`},
		{"no radius", step(`{"wait_for":{"hostiles_gone":{}}}`), "legs[0].steps[0].wait_for.hostiles_gone.radius: required"},
		{"radius", step(`{"wait_for":{"hostiles_gone":{"radius":-1}}}`), "legs[0].steps[0].wait_for.hostiles_gone.radius: -1 yards is outside"},
		{"loot nothing", step(`{"loot_object":{}}`), "legs[0].steps[0].loot_object: needs an entry, a near position, or both"},
		{"loot expect no entry", step(`{"loot_object":{"entry":1,"expect_item":{"name":"x"}}}`), "legs[0].steps[0].loot_object.expect_item.entry: required"},
		{"loot bad stand", step(`{"loot_object":{"entry":1,"stand":{"x":0,"y":0}}}`), "legs[0].steps[0].loot_object.stand.z: required"},
		{"loot and use", step(`{"loot_object":{"entry":1},"use_object":{"entry":1}}`), "has more than one of"},
		{"use_item no entry", step(`{"use_item":{"name":"x"}}`), "legs[0].steps[0].use_item.entry: required"},
		{"use_item expect no entry", step(`{"use_item":{"entry":1,"expect_item":{}}}`), "legs[0].steps[0].use_item.expect_item.entry: required"},
		{"use_item unknown field", step(`{"use_item":{"entry":1,"slot":3}}`), `unknown field "slot"`},
		{"bad group team", leg(`{"to":{"x":0,"y":0,"z":0},"pull_groups":[{"entries":[5],"team":"orcs","positions":[{"x":0,"y":0,"z":0}]}]}`), `legs[1].pull_groups[0].team: "orcs" is neither "alliance" nor "horde"`},
		{"bad boss team", leg(`{"to":{"x":0,"y":0,"z":0},"boss":{"entry":4,"team":"Horde","position":{"x":0,"y":0,"z":0}}}`), `legs[1].boss.team: "Horde" is neither`},
		{"boss loot item no entry", leg(`{"to":{"x":0,"y":0,"z":0},"boss":{"entry":4,"loot_item":{"name":"x"},"position":{"x":0,"y":0,"z":0}}}`), "legs[1].boss.loot_item.entry: required"},
		{"timeout", step(`{"wait_for":{"timeout_s":9999,"hostiles_gone":{"radius":5}}}`), "legs[0].steps[0].wait_for.timeout_s: 9999 s is outside 1-600"},
	}
	for _, c := range cases {
		_, err := Parse("t.json", []byte(c.in))
		if err == nil || !strings.Contains(err.Error(), c.want) {
			t.Errorf("%s: want %q, got %v", c.name, c.want, err)
		}
	}

	// A top-level unknown field is listed with the other mistakes, not instead of them.
	_, err := Parse("top.json", []byte(`{"version":1,"colour":1,"map":1,"entrance":{"x":0,"y":0,"z":0},"legs":[]}`))
	if err == nil || !strings.Contains(err.Error(), `unknown field "colour"`) || !strings.Contains(err.Error(), "name: required") ||
		!strings.Contains(err.Error(), "legs: needs at least one leg") {
		t.Fatalf("%v", err)
	}

	// Everything wrong at once is reported at once, one per line, each with the file name.
	_, err = Parse("bad.json", []byte(`{"version":1,"name":"t","map":1,"entrance":{"x":0,"y":0,"z":0},"legs":[
	  {"to":{"x":0,"y":0,"z":0},"boss":{"entry":0,"position":{"x":0,"y":0,"z":0}}},
	  {"boss":{"entry":2,"position":{"x":0,"y":0,"z":0}}}]}`))
	if err == nil {
		t.Fatal("accepted")
	}
	lines := strings.Split(err.Error(), "\n")
	if len(lines) != 2 || !strings.HasPrefix(lines[0], "bad.json: legs[0].boss.entry: ") || !strings.HasPrefix(lines[1], "bad.json: legs[1].to: ") {
		t.Fatalf("%q", lines)
	}
}

// The example in the README is a plan the loader accepts.
func TestReadmeExampleParses(t *testing.T) {
	b, err := os.ReadFile("../../README.md")
	if err != nil {
		t.Fatal(err)
	}
	s := string(b)
	i := strings.Index(s, "## Dungeon plans")
	if i < 0 {
		t.Fatal("no Dungeon plans section")
	}
	start := strings.Index(s[i:], "```json\n")
	end := strings.Index(s[i+start+8:], "```")
	if start < 0 || end < 0 {
		t.Fatal("no json example")
	}
	if _, err := Parse("README.md", []byte(s[i+start+8:i+start+8+end])); err != nil {
		t.Fatal(err)
	}
}

func TestPlanEntranceTrigger(t *testing.T) {
	p := mustPlan(t, `{"version":1,"name":"x","map":70,"entrance":{"x":1,"y":2,"z":3},"entrance_trigger":286,"legs":[{"to":{"x":1,"y":2,"z":3},"pull_groups":[{"entries":[1],"positions":[{"x":2,"y":2,"z":3}]}]}]}`)
	if p.EntranceTrigger != 286 {
		t.Fatalf("%d", p.EntranceTrigger)
	}
}

// The steps and fields of the loot, item and team parts read, and Marshal writes them back on the lines a hand-kept plan uses.
func TestParseLootUseAndTeam(t *testing.T) {
	const src = `{"version":1,"name":"t","map":70,"entrance":{"x":0,"y":0,"z":0},"legs":[
 {"to":{"x":1,"y":0,"z":0},
  "pull_groups":[{"entries":[5],"positions":[{"x":2,"y":0,"z":0}],"team":"horde"}],
  "steps":[
   {"loot_object":{"entry":123329,"near":{"x":3,"y":0,"z":0},"stand":{"x":4,"y":0,"z":0},"expect_item":{"entry":7740,"name":"Gni'kiv Medallion"}}},
   {"use_item":{"entry":7740,"name":"Gni'kiv Medallion","requires_item":{"entry":7741},"expect_item":{"entry":7733}}}],
  "boss":{"entry":6906,"position":{"x":5,"y":0,"z":0},"first":[6907],"team":"horde","loot_item":{"entry":7741,"name":"The Shaft of Tsol"},"companions_loot":false}}]}`
	p, err := Parse("t.json", []byte(src))
	if err != nil {
		t.Fatal(err)
	}
	l := p.Legs[0]
	lo, ui := l.Steps[0].LootObject, l.Steps[1].UseItem
	if l.PullGroups[0].Team != TeamHorde || l.Boss.Team != TeamHorde || l.Boss.LootItem.Entry != 7741 || l.Boss.LootItem.Name != "The Shaft of Tsol" ||
		lo.Entry != 123329 || *lo.Near != (Point{3, 0, 0}) || *lo.Stand != (Point{4, 0, 0}) || lo.ExpectItem.Entry != 7740 ||
		ui.Entry != 7740 || ui.Name != "Gni'kiv Medallion" || ui.RequiresItem.Entry != 7741 || ui.ExpectItem.Entry != 7733 {
		t.Fatalf("%+v %+v %+v", l.Boss, lo, ui)
	}
	if cl := l.Boss.CompanionsLoot; cl == nil || *cl {
		t.Fatalf("%v", cl)
	}
	b, err := Marshal(p)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{`"companions_loot": false`, `"expect_item": {"entry": 7740, "name": "Gni'kiv Medallion"}`, `"requires_item": {"entry": 7741}`, `"expect_item": {"entry": 7733}`,
		`"loot_item": {"entry": 7741, "name": "The Shaft of Tsol"}`, `"team": "horde"`} {
		if !strings.Contains(string(b), want) {
			t.Errorf("Marshal lacks %s:\n%s", want, b)
		}
	}
	q, err := Parse("t.json", b)
	if err != nil {
		t.Fatal(err)
	}
	b2, _ := Marshal(q)
	if string(b) != string(b2) {
		t.Fatalf("not stable:\n%s\n%s", b, b2)
	}
}

// gone_ok on a wait_for gameobject is read, written back and refused on a wait that has no object.
func TestParseWaitGoneOK(t *testing.T) {
	const src = `{"version":1,"name":"t","map":33,"entrance":{"x":0,"y":0,"z":0},"legs":[
 {"to":{"x":1,"y":0,"z":0},"steps":[{"wait_for":{"gameobject":{"entry":18895,"near":{"x":-242.58,"y":2159.05,"z":90.62},"state":"active","gone_ok":true},"timeout_s":25}},
  {"wait_for":{"gameobject":{"entry":18972,"state":"active"},"timeout_s":60}}]}]}`
	p, err := Parse("t.json", []byte(src))
	if err != nil {
		t.Fatal(err)
	}
	a, b := p.Legs[0].Steps[0].WaitFor.GameObject, p.Legs[0].Steps[1].WaitFor.GameObject
	if !a.GoneOK || b.GoneOK {
		t.Fatalf("%+v %+v", a, b)
	}
	out, err := Marshal(p)
	if err != nil || strings.Count(string(out), `"gone_ok": true`) != 1 {
		t.Fatalf("%v\n%s", err, out)
	}
	q, err := Parse("t.json", out)
	if err != nil || !q.Legs[0].Steps[0].WaitFor.GameObject.GoneOK {
		t.Fatalf("%v", err)
	}
	// hostiles_gone has no object to be gone: the key is unknown there.
	bad := strings.Replace(src, `"gameobject":{"entry":18972,"state":"active"}`, `"hostiles_gone":{"radius":5,"gone_ok":true}`, 1)
	if _, err := Parse("t.json", []byte(bad)); err == nil || !strings.Contains(err.Error(), "gone_ok") {
		t.Fatalf("%v", err)
	}
}

// after_boss holds the same kinds of steps, is checked like steps and counts as something to do for a leg.
func TestParseAfterBoss(t *testing.T) {
	const src = `{"version":1,"name":"t","map":1,"entrance":{"x":0,"y":0,"z":0},"legs":[
 {"to":{"x":1,"y":0,"z":0},"after_boss":[{"use_item":{"entry":7740}},{"wait_for":{"hostiles_gone":{"radius":5}}}]}]}`
	p, err := Parse("t.json", []byte(src))
	if err != nil || len(p.Legs[0].AfterBoss) != 2 || p.Legs[0].AfterBoss[0].UseItem.Entry != 7740 || p.Legs[0].AfterBoss[1].WaitFor == nil {
		t.Fatalf("%+v %v", p, err)
	}
	bad := strings.Replace(src, `{"use_item":{"entry":7740}}`, `{"use_item":{}}`, 1)
	if _, err := Parse("t.json", []byte(bad)); err == nil || !strings.Contains(err.Error(), "legs[0].after_boss[0].use_item.entry: required") {
		t.Fatalf("%v", err)
	}
	b, _ := Marshal(p)
	if q, err := Parse("t.json", b); err != nil || len(q.Legs[0].AfterBoss) != 2 {
		t.Fatalf("%v", err)
	}
}

// `fleers` lists creature entries that run at low health; an entry of 0 is refused like any other.
func TestParseFleers(t *testing.T) {
	base := func(f string) string {
		return `{"version":1,"name":"t","map":1,"entrance":{"x":0,"y":0,"z":0},` + f + `"legs":[{"to":{"x":0,"y":0,"z":0},"pull_groups":[{"entries":[1],"positions":[{"x":0,"y":0,"z":0}]}]}]}`
	}
	p, err := Parse("t.json", []byte(base(`"fleers":[4853,7321],`)))
	if err != nil || fmt.Sprint(p.Fleers) != "[4853 7321]" {
		t.Fatalf("%v %v", err, p)
	}
	if _, err := Parse("t.json", []byte(base(`"fleers":[4853,0],`))); err == nil || !strings.Contains(err.Error(), "fleers[1]: 0 is no creature entry") {
		t.Fatalf("%v", err)
	}
	if p, err := Parse("t.json", []byte(base(``))); err != nil || len(p.Fleers) != 0 {
		t.Fatalf("%v %v", err, p)
	}
}

// TestApproachAndHoldAreRead: approach points and a hold are read, and their limits refused.
func TestApproachAndHoldAreRead(t *testing.T) {
	base := `{"version":1,"name":"t","map":70,"entrance":{"x":0,"y":0,"z":0},"legs":[{"name":"a","to":{"x":0,"y":0,"z":0},
	 "pull_groups":[{"entries":[5],"positions":[{"x":50,"y":0,"z":0}]%s}]}]}`
	p, err := Parse("t.json", []byte(fmt.Sprintf(base, `,"approach":[{"x":20,"y":0,"z":0}],"hold":{"instance_data":1,"spot":{"x":40,"y":0,"z":0},"radius":30,"timeout_s":600,"entries":[6]}`)))
	if err != nil {
		t.Fatal(err)
	}
	g := p.Legs[0].PullGroups[0]
	if len(g.Approach) != 1 || g.Approach[0].X != 20 || g.Hold == nil || g.Hold.Data != 1 || g.Hold.Spot.X != 40 || g.Hold.Radius != 30 || g.Hold.TimeoutS != 600 || len(g.Hold.Entries) != 1 {
		t.Fatalf("%+v %+v", g, g.Hold)
	}
	for _, bad := range []string{
		`,"approach":[{"x":1,"y":0,"z":0},{"x":2,"y":0,"z":0},{"x":3,"y":0,"z":0},{"x":4,"y":0,"z":0},{"x":5,"y":0,"z":0}]`,
		`,"approach":[{"x":1,"y":0}]`,
		`,"hold":{"spot":{"x":40,"y":0,"z":0},"radius":30,"timeout_s":600}`,
		`,"hold":{"instance_data":1,"spot":{"x":40,"y":0,"z":0},"radius":0,"timeout_s":600}`,
		`,"hold":{"instance_data":1,"spot":{"x":40,"y":0,"z":0},"radius":30,"timeout_s":601}`,
		`,"hold":{"instance_data":1,"radius":30,"timeout_s":600}`,
	} {
		if _, err := Parse("t.json", []byte(fmt.Sprintf(base, bad))); err == nil {
			t.Errorf("%s: accepted", bad)
		}
	}
}

// TestUldamanRunLeaderReach: from the scorpid cave on (legs 4-13, the part the run leader's live test of 2026-10-16 stopped in) every
// pack and boss of Uldaman is within RunLeaderReachYd of the run leader's last move row, so the run leader needs none of its own hops.
// Legs 0-3 ran live with the hops and keep them.
func TestUldamanRunLeaderReach(t *testing.T) {
	p, err := Load("../../dungeons/uldaman.json")
	if err != nil {
		t.Fatal(err)
	}
	for _, w := range p.RunLeaderReach(0) {
		if w.Leg >= 4 {
			t.Errorf("%s", w)
		}
	}
	// Both Jadespine Basilisks of the route that the first export left out are pulled (world guids 30084 and 30089).
	for _, want := range []Point{{X: -179.0, Y: 321.7, Z: -52.7}, {X: -125.9, Y: 266.9, Z: -45.7}} {
		found := false
		for _, l := range p.Legs {
			for _, g := range l.PullGroups {
				for _, at := range g.Positions {
					found = found || (dist3(at, want) < 0.5 && slices.Contains(g.Entries, 4863))
				}
			}
		}
		if !found {
			t.Errorf("no pull group of the basilisk at %v", want)
		}
	}
}
