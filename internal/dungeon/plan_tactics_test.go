package dungeon

import (
	"strings"
	"testing"
)

const tacticsPlan = `{"version":1,"name":"tac","map":0,"entrance":{"x":1,"y":2,"z":3},"legs":[
 {"name":"a","to":{"x":1,"y":2,"z":3},"boss":{"entry":7023,"position":{"x":5,"y":5,"z":5},"tactics":%s}}]}`

func tacticsLoad(tactics string) (*Plan, error) {
	return Parse("tac", []byte(strings.Replace(tacticsPlan, "%s", tactics, 1)))
}

func TestBossTacticsLoad(t *testing.T) {
	p, err := tacticsLoad(`{"kill_first":[7209],"never_target":[9001],"interrupt":["Chain Lightning",15801],"dispel":["Fevered Plague"],"no_dispel":["Mutating Injection"],"move_out":[{"cast":"Fire Nova","yards":12},{"ground":10132}]}`)
	if err != nil {
		t.Fatal(err)
	}
	x := p.Legs[0].Boss.Tactics
	if x == nil || x.KillFirst[0] != 7209 || len(x.Interrupt) != 2 || x.MoveOut[0].Yards != 12 || x.MoveOut[1].Ground.ID != 10132 {
		t.Fatalf("%+v", x)
	}
	// An empty block is no block; a boss without the field has none.
	if p, err = tacticsLoad(`{}`); err != nil || p.Legs[0].Boss.Tactics != nil {
		t.Fatalf("%v %+v", err, p.Legs[0].Boss.Tactics)
	}
}

func TestBossTacticsAreChecked(t *testing.T) {
	for tactics, want := range map[string]string{
		`{"interrupt":["Chain Lightening"]}`:    `legs[0].boss.tactics: interrupt: no spell is named "Chain Lightening"`,
		`{"kill_first":[5],"never_target":[5]}`: "both kill_first and never_target",
		`{"move_out":[{}]}`:                     "exactly one of cast and ground",
		`{"colour":1}`:                          "colour",
		`{"interrupt":[0]}`:                     "spell",
	} {
		if _, err := tacticsLoad(tactics); err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("%s: %v, want %q", tactics, err, want)
		}
	}
}

func TestShippedPlansCarryTheirBossTactics(t *testing.T) {
	// The ids come from the core's creature_spells and EventAI tables (Revelosh 69100: Chain Lightning 16006, Lightning Bolt 15801;
	// the Obsidian Shard 7209 is what the Sentinel's EventAI summons; Grimlok 48540; Antu'sul 81270 and EventAI 812701-812709; Velratha 77950).
	want := map[string]map[string]string{
		"uldaman":   {"revelosh": "interrupt", "sentinel": "kill_first", "grimlok": "interrupt"},
		"zulfarrak": {"antusul": "interrupt", "velratha": "interrupt"},
	}
	for name, legs := range want {
		p, err := Load("../../dungeons/" + name + ".json")
		if err != nil {
			t.Fatal(err)
		}
		for _, l := range p.Legs {
			kind, ok := legs[l.Name]
			if !ok {
				continue
			}
			delete(legs, l.Name)
			x := l.Boss.Tactics
			if x == nil || (kind == "interrupt" && len(x.Interrupt) == 0) || (kind == "kill_first" && (len(x.KillFirst) != 1 || x.KillFirst[0] != 7209)) {
				t.Errorf("%s/%s: %+v", name, l.Name, x)
			}
		}
		if len(legs) != 0 {
			t.Errorf("%s: legs not found: %v", name, legs)
		}
	}
}
