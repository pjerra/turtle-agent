package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/pjerra/turtle-agent/internal/opcode"
	"github.com/pjerra/turtle-agent/internal/rpc"
	"github.com/pjerra/turtle-agent/internal/session"
)

const rigPlan = `{"version":1,"name":"rig","map":0,"entrance":{"x":1,"y":2,"z":3},"legs":[
 {"name":"first","to":{"x":1,"y":2,"z":3},"pull_groups":[{"entries":[299],"positions":[{"x":5,"y":2,"z":3}]}]},
 {"name":"second","to":{"x":9,"y":2,"z":3},"pull_groups":[{"entries":[299],"positions":[{"x":12,"y":2,"z":3}]}]}]}`

func dungeonRig(t *testing.T) *agentRig {
	t.Helper()
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "rig.json"), []byte(rigPlan), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "broken.json"), []byte(`{"version":1}`), 0o600); err != nil {
		t.Fatal(err)
	}
	return newRigWith(t, session.Options{}, [3]float32{1, 2, 3}, methodOptions{DungeonsDir: dir})
}

func TestDungeonStatusIsIdleAtFirst(t *testing.T) {
	r := dungeonRig(t)
	for _, m := range []string{"dungeon_status", "dungeon_stop"} {
		if res := r.call(m, "{}")["result"].(map[string]any); res["state"] != "idle" || res["running"] != false || res["report"] != nil {
			t.Fatalf("%s: %v", m, res)
		}
	}
}

func TestDungeonRunRefusals(t *testing.T) {
	r := dungeonRig(t)
	for _, c := range []struct{ params, want string }{
		{`{"name":"../rig"}`, "name must be a plan file name"},
		{`{"name":""}`, "name must be a plan file name"},
		{`{"name":"nowhere"}`, "dungeon plan: "},
		{`{"name":"broken"}`, "name: required"},
		{`{"name":"rig","from_leg":2}`, "from_leg: the plan has legs [first second] (numbers 0-1)"},
		{`{"name":"rig","from_leg":"third"}`, "from_leg: the plan has legs"},
		{`{"name":"rig","from_leg":true}`, "from_leg must be a leg number"},
		{`{"name":"rig","colour":1}`, "colour"},
	} {
		out := r.call("dungeon_run", c.params)
		msg, _ := out["error"].(map[string]any)["message"].(string)
		if errCode(out) != rpc.CodeInvalidParams || !strings.Contains(msg, c.want) {
			t.Errorf("%s: %v", c.params, out)
		}
	}
}

// A run starts in the background, ends in the report and the events, and a second one may follow.
func TestDungeonRunWithoutAPartyFailsInTheReport(t *testing.T) {
	r := dungeonRig(t)
	res := r.call("dungeon_run", `{"name":"rig","from_leg":"second"}`)["result"].(map[string]any)
	if res["started"] != true || res["plan"] != "rig" || res["from_leg"] != float64(1) || res["map"] != float64(0) {
		t.Fatalf("%v", res)
	}
	st := r.eventually("dungeon_status", "{}", func(m map[string]any) bool { return m["result"].(map[string]any)["state"] == "failed" })["result"].(map[string]any)
	rep := st["report"].(map[string]any)
	if st["running"] != false || rep["plan"] != "rig" || rep["from_leg"] != float64(1) || !strings.Contains(rep["error"].(string), "no party") ||
		len(rep["gm_shortcuts"].([]any)) != 0 || rep["wipes"] != float64(0) {
		t.Fatalf("%v", st)
	}
	evs := r.call("events", `{"since":0,"kinds":["dungeon_failed","dungeon_done","dungeon_leg","dungeon_step"]}`)["result"].(map[string]any)["events"].([]any)
	if len(evs) != 1 || evs[0].(map[string]any)["kind"] != "dungeon_failed" || !strings.Contains(evs[0].(map[string]any)["data"].(map[string]any)["error"].(string), "no party") {
		t.Fatalf("%v", evs)
	}
	// The finished run is replaced by the next.
	if out := r.call("dungeon_run", `{"name":"rig","teleport":true,"no_shortcuts":true}`); out["error"] != nil || out["result"].(map[string]any)["from_leg"] != float64(0) {
		t.Fatalf("%v", out)
	}
}

// A run that goes on shows as running, refuses a second, and dungeon_stop ends it with the report of what it did.
func TestDungeonStopEndsARun(t *testing.T) {
	r := dungeonRig(t)
	r.groupWith("Gorm") // a party, who is not in view: the first rest gate waits for him
	if out := r.call("dungeon_run", `{"name":"rig"}`); out["error"] != nil {
		t.Fatalf("%v", out)
	}
	r.expect(opcode.CMSGMessagechat) // .gm visible on
	st := r.eventually("dungeon_status", "{}", func(m map[string]any) bool {
		rep, _ := m["result"].(map[string]any)["report"].(map[string]any)
		return rep != nil && len(rep["legs"].([]any)) == 1
	})["result"].(map[string]any)
	if st["running"] != true || st["state"] != "running" {
		t.Fatalf("%v", st)
	}
	if out := r.call("dungeon_run", `{"name":"rig"}`); errCode(out) != rpc.CodeGame || !strings.Contains(out["error"].(map[string]any)["message"].(string), "dungeon_stop it first") {
		t.Fatalf("%v", out)
	}
	stopped := r.call("dungeon_stop", "{}")["result"].(map[string]any)
	rep := stopped["report"].(map[string]any)
	if stopped["running"] != false || stopped["state"] != "stopped" || rep["error"] != "stopped" || rep["legs"].([]any)[0].(map[string]any)["result"] != "stopped" {
		t.Fatalf("%v", stopped)
	}
	b, _ := json.Marshal(r.call("events", `{"since":0,"kinds":["dungeon_leg","dungeon_failed"]}`)["result"])
	if !strings.Contains(string(b), `"phase":"start"`) || !strings.Contains(string(b), `"kind":"dungeon_failed"`) || !strings.Contains(string(b), `"error":"stopped"`) {
		t.Fatalf("%s", b)
	}
}
