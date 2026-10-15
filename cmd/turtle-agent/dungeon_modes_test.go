package main

import (
	"strings"
	"testing"

	"github.com/pjerra/turtle-agent/internal/brain"
	"github.com/pjerra/turtle-agent/internal/dungeon"
	"github.com/pjerra/turtle-agent/internal/rpc"
)

func TestRunModeNames(t *testing.T) {
	for in, want := range map[string]string{"": modeLeader, "group": modeLeader, "Leader": modeLeader, "remote": modeCommander, "commander": modeCommander} {
		got, _, err := runMode(in)
		if err != nil || got != want {
			t.Errorf("%q: %q %v", in, got, err)
		}
	}
	if _, explicit, _ := runMode(""); explicit {
		t.Error("an omitted mode is the older group mode, not an explicit leader")
	}
	if got, explicit, err := runMode("observe"); err != nil || got != modeObserve || !explicit {
		t.Errorf("observe: %q %v %v", got, explicit, err)
	}
	if _, _, err := runMode("fly"); err == nil {
		t.Error("fly")
	}
}

func TestLeaderPresetPerClassAndRole(t *testing.T) {
	// Every default names a preset the brain has; the table covers all nine classes.
	for class, rows := range leaderPresets {
		for role, preset := range rows {
			if err := brain.CheckParams(brain.Params{Strategies: []string{preset}, Role: role}); err != nil {
				t.Errorf("class %d role %s: %s: %v", class, role, preset, err)
			}
		}
	}
	if len(leaderPresets) != 9 {
		t.Errorf("%d classes", len(leaderPresets))
	}
	for _, c := range []struct {
		class uint8
		role  string
		want  string
	}{{brain.ClassMage, "", "frost_mage"}, {brain.ClassWarrior, "tank", "tank_warrior"}, {brain.ClassWarrior, "", "arms_warrior"}, {brain.ClassPriest, "Healer", "holy_priest"}} {
		if got, err := leaderPreset(c.class, c.role); err != nil || got != c.want {
			t.Errorf("%+v: %q %v", c, got, err)
		}
	}
	if _, err := leaderPreset(brain.ClassMage, "tank"); err == nil || !strings.Contains(err.Error(), "no default preset for the role") {
		t.Errorf("a mage tank: %v", err)
	}
	if _, err := leaderPreset(0, ""); err == nil || !strings.Contains(err.Error(), "not known") {
		t.Errorf("class 0: %v", err)
	}
}

func TestDungeonRunModesAreRefusedClearly(t *testing.T) {
	r := dungeonRig(t)
	for _, c := range []struct{ params, want string }{
		{`{"name":"rig","mode":"observe"}`, "needs lead_bot"},
		{`{"name":"rig","mode":"observe","lead_bot":"Tanky","from_leg":1}`, "start inside the plan"},
		{`{"name":"rig","mode":"observe","lead_bot":"Tanky","tank":"Tanky"}`, "the agent sends no order"},
		{`{"name":"rig","mode":"commander"}`, "needs lead_bot"},
		{`{"name":"rig","mode":"leader","lead_bot":"Tanky"}`, `go with mode "remote"`},
		{`{"name":"rig","mode":"commander","lead_bot":"Tanky","rotation":"frost_mage"}`, "in remote (commander) mode the bots fight"},
		{`{"name":"rig","mode":"commander","lead_bot":"Tanky","role":"tank"}`, "in remote (commander) mode the bots fight"},
		{`{"name":"rig","rotation":"frost_mage"}`, "go with a brain"},
		{`{"name":"rig","role":"tank"}`, "go with a brain"},
		{`{"name":"rig","brain":"frost_mage","rotation":"no_such_rotation"}`, "unknown"},
		{`{"name":"rig","brain":"frost_mage","role":"healer"}`, "does not match the strategies"},
		{`{"name":"rig","brain":"frost_mage","rotation":"arms_warrior"}`, "brain:"},
	} {
		out := r.call("dungeon_run", c.params)
		if errCode(out) != rpc.CodeInvalidParams || !strings.Contains(out["error"].(map[string]any)["message"].(string), c.want) {
			t.Errorf("%s: %v", c.params, out["error"])
		}
	}
}

func TestDungeonRunLeaderModeWithARotation(t *testing.T) {
	r := dungeonRig(t)
	r.groupWith("Gorm")
	out := r.call("dungeon_run", `{"name":"rig","mode":"leader","brain":"frost_mage","rotation":"frost_mage"}`)
	if out["error"] != nil {
		t.Fatalf("%v", out)
	}
	res := out["result"].(map[string]any)
	rot, _ := brain.RotationNamed("frost_mage")
	if res["run_mode"] != "leader" || res["mode"] != "group" || res["brain"] != "frost_mage+rot:frost_mage@"+rot.SHA() {
		t.Fatalf("%v", res)
	}
	r.eventually("dungeon_status", "{}", func(m map[string]any) bool {
		rep, _ := m["result"].(map[string]any)["report"].(map[string]any)
		return rep != nil && rep["brain"] == res["brain"]
	})
	// The rest gate starts the brain with the rotation in place of the preset's Frost strategy.
	st := r.eventually("brain_status", "{}", func(m map[string]any) bool { return m["result"].(map[string]any)["running"] == true })["result"].(map[string]any)
	var have []string
	for _, s := range st["strategies"].([]any) {
		have = append(have, s.(string))
	}
	joined := strings.Join(have, " ")
	if !strings.Contains(joined, "rot:frost_mage") || strings.Contains(joined, "mage_frost") {
		t.Fatalf("%v", have)
	}
	r.call("dungeon_stop", "{}")
}

func TestDungeonRunLeaderModeNeedsAClassToPickAPreset(t *testing.T) {
	r := dungeonRig(t)
	out := r.call("dungeon_run", `{"name":"rig","mode":"leader"}`)
	if errCode(out) != rpc.CodeInvalidParams || !strings.Contains(out["error"].(map[string]any)["message"].(string), "pass brain") {
		t.Fatalf("%v", out)
	}
}

func TestPlanEncountersCarryTheBossTactics(t *testing.T) {
	plan := `{"version":1,"name":"enc","map":0,"entrance":{"x":1,"y":2,"z":3},"legs":[
	 {"name":"a","to":{"x":1,"y":2,"z":3},"boss":{"entry":7023,"position":{"x":5,"y":5,"z":5},"first":[4847],"tactics":{"kill_first":[7209],"interrupt":["Chain Lightning",15801]}}},
	 {"name":"b","to":{"x":9,"y":2,"z":3},"boss":{"entry":6910,"position":{"x":9,"y":9,"z":9}}}]}`
	p, err := dungeon.Parse("enc", []byte(plan))
	if err != nil {
		t.Fatal(err)
	}
	enc := planEncounters(p)
	if len(enc) != 1 || enc[0].Name != "enc/a" || len(enc[0].Entries) != 2 || enc[0].Entries[0] != 7023 || enc[0].Entries[1] != 4847 ||
		enc[0].Tactics.KillFirst[0] != 7209 || len(enc[0].Tactics.Interrupt) != 2 {
		t.Fatalf("%+v", enc)
	}
	// With the encounters in, the brain runs the encounter strategy and avoid_ground.
	_, names, err := brain.ResolveFor([]string{"frost_mage"}, brain.ResolveOpts{Encounters: true})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(strings.Join(names, " "), "encounter") {
		t.Fatalf("%v", names)
	}
}

func TestDungeonRunLeaderPulltestRefusals(t *testing.T) {
	r := dungeonRig(t)
	for _, c := range []struct{ params, want string }{
		{`{"name":"rig","mode":"leader","brain":"frost_mage","pulltest":{"leg":0,"pull_group":0},"teleport":true}`, "leave teleport and resume out"},
		{`{"name":"rig","mode":"leader","brain":"frost_mage","pulltest":{"leg":0,"pull_group":0},"from_leg":1}`, "names its own start"},
		{`{"name":"rig","mode":"leader","brain":"frost_mage","pulltest":{"leg":0,"pull_group":7}}`, "pull_group"},
		{`{"name":"rig","mode":"leader","brain":"frost_mage","pulltest":{"leg":0}}`, "needs leg"},
		{`{"name":"rig","mode":"leader","brain":"frost_mage","pulltest":{"leg":0,"pull_group":0,"tries":11}}`, "use 1-10"},
		{`{"name":"rig","mode":"leader","brain":"frost_mage","pulltest":{"leg":0,"pull_group":0},"from_pull_group":0}`, "go with mode"},
	} {
		out := r.call("dungeon_run", c.params)
		if errCode(out) != rpc.CodeInvalidParams || !strings.Contains(out["error"].(map[string]any)["message"].(string), c.want) {
			t.Errorf("%s: %v", c.params, out["error"])
		}
	}
}

func TestDungeonRunLeaderPulltestStartsAndReportsItsMode(t *testing.T) {
	r := dungeonRig(t)
	r.groupWith("Gorm")
	out := r.call("dungeon_run", `{"name":"rig","mode":"leader","brain":"frost_mage","rotation":"frost_mage","pulltest":{"leg":0,"pull_group":0,"tries":2,"reset":"none"}}`)
	if out["error"] != nil {
		t.Fatalf("%v", out)
	}
	rot, _ := brain.RotationNamed("frost_mage")
	st := r.eventually("dungeon_status", "{}", func(m map[string]any) bool {
		rep, _ := m["result"].(map[string]any)["report"].(map[string]any)
		return rep != nil && rep["pulltest"] != nil
	})["result"].(map[string]any)
	pt := st["report"].(map[string]any)["pulltest"].(map[string]any)
	if pt["mode"] != "leader" || pt["brain"] != "frost_mage+rot:frost_mage@"+rot.SHA() || pt["tries_asked"] != float64(2) || pt["reset"] != "none" {
		t.Fatalf("%v", pt)
	}
	r.call("dungeon_stop", "{}")
}

// give_items is a remote-mode option: a commander run takes it (checked), a leader or observe run refuses it, and a bad entry is refused.
func TestCheckModeGiveItems(t *testing.T) {
	give := []dungeon.GiveItem{{Bot: "lead", Item: 9240}}
	p := dungeonRunParams{Mode: "remote", LeadBot: "Lcwarra", Bots: []string{"Lcpriest"}, GiveItems: give}
	if remote, err := checkMode(p); !remote || err != nil {
		t.Fatalf("remote: %v %v", remote, err)
	}
	hire := dungeonRunParams{Mode: "remote", Prepare: &dungeon.PrepareSpec{Party: "hire"}, GiveItems: []dungeon.GiveItem{{Bot: "carrier", Item: 9240}}}
	if remote, err := checkMode(hire); !remote || err != nil {
		t.Fatalf("hired party: %v %v", remote, err)
	}
	for _, bad := range []dungeonRunParams{
		{Mode: "leader", GiveItems: give},
		{GiveItems: give},
		{Mode: "observe", LeadBot: "Lcwarra", Observe: &observeParam{}, GiveItems: give},
		{Mode: "remote", LeadBot: "Lcwarra", Bots: []string{"Lcpriest"}, GiveItems: []dungeon.GiveItem{{Bot: "lead"}}},
		{Mode: "remote", LeadBot: "Lcwarra", Bots: []string{"Lcpriest"}, GiveItems: []dungeon.GiveItem{{Bot: "not a name", Item: 1}}},
	} {
		if _, err := checkMode(bad); err == nil {
			t.Errorf("%+v was accepted", bad)
		}
	}
}
