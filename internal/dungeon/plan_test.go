package dungeon

import (
	"os"
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
		{"unknown top field", `{"version":1,"colour":1}`, `unknown field "colour"`},
		{"not json", `{`, "t.json:"},
		{"two documents", validPlan + validPlan, "more than one JSON value"},
		{"unknown leg field", leg(`{"to":{"x":0,"y":0,"z":0},"bosses":[]}`), `legs[1]: json: unknown field "bosses"`},
		{"leg without to", leg(`{"boss":{"entry":1,"position":{"x":0,"y":0,"z":0}}}`), "legs[1].to: required"},
		{"empty leg", leg(`{"to":{"x":0,"y":0,"z":0}}`), "legs[1]: has no pull_groups, steps or boss"},
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
		{"skip also first", leg(`{"to":{"x":0,"y":0,"z":0},"skip":[3],"boss":{"entry":4,"first":[3],"position":{"x":0,"y":0,"z":0}}}`), "legs[1].skip[0]: 3 is also in boss.first"},
		{"empty step", step(`{}`), "legs[0].steps[0]: needs use_object or wait_for"},
		{"two things", step(`{"use_object":{"entry":1},"wait_for":{"hostiles_gone":{"radius":5}}}`), "legs[0].steps[0]: has both use_object and wait_for"},
		{"unknown step field", step(`{"click":{"entry":1}}`), `legs[0].steps[0]: json: unknown field "click"`},
		{"use nothing", step(`{"use_object":{}}`), "legs[0].steps[0].use_object: needs an entry, a near position, or both"},
		{"range without near", step(`{"use_object":{"entry":1,"range":5}}`), "legs[0].steps[0].use_object.range: only means something with near"},
		{"range too big", step(`{"use_object":{"near":{"x":0,"y":0,"z":0},"range":5000}}`), "legs[0].steps[0].use_object.range: 5000 yards is outside"},
		{"bad stand", step(`{"use_object":{"entry":1,"stand":{"x":0,"y":0}}}`), "legs[0].steps[0].use_object.stand.z: required"},
		{"wait nothing", step(`{"wait_for":{}}`), "legs[0].steps[0].wait_for: needs gameobject or hostiles_gone"},
		{"wait both", step(`{"wait_for":{"gameobject":{"entry":1,"state":"active"},"hostiles_gone":{"radius":5}}}`), "legs[0].steps[0].wait_for: has both gameobject and hostiles_gone"},
		{"bad state", step(`{"wait_for":{"gameobject":{"entry":1,"state":"open"}}}`), `legs[0].steps[0].wait_for.gameobject.state: "open" is none of ready, active, active_alt`},
		{"no state", step(`{"wait_for":{"gameobject":{"entry":1}}}`), `wait_for.gameobject.state: "" is none of`},
		{"no radius", step(`{"wait_for":{"hostiles_gone":{}}}`), "legs[0].steps[0].wait_for.hostiles_gone.radius: required"},
		{"radius", step(`{"wait_for":{"hostiles_gone":{"radius":-1}}}`), "legs[0].steps[0].wait_for.hostiles_gone.radius: -1 yards is outside"},
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
