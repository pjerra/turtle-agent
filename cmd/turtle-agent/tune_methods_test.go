package main

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/pjerra/turtle-agent/internal/opcode"
	"github.com/pjerra/turtle-agent/internal/rpc"
	"github.com/pjerra/turtle-agent/internal/session"
)

func TestTuneRefusals(t *testing.T) {
	r := dungeonRig(t)
	for _, c := range []struct{ params, want string }{
		{`{"name":"rig","force":true,"tune":"x.json"}`, `go with mode "remote"`},
		{`{"name":"rig","force":true,"live_reload":false}`, `go with mode "remote"`},
		{`{"name":"rig","force":true,"mode":"remote","lead_bot":"Tanky","tune":"/no/such/tunables.json"}`, "tune: "},
	} {
		out := r.call("dungeon_run", c.params)
		if errCode(out) != rpc.CodeInvalidParams || !strings.Contains(out["error"].(map[string]any)["message"].(string), c.want) {
			t.Errorf("%s: %v", c.params, out["error"])
		}
	}
	// No run: nothing to tune.
	for m, params := range map[string]string{"tune_set": `{"key":"pull_range","value":40}`, "tune_list": `{}`} {
		out := r.call(m, params)
		if errCode(out) != rpc.CodeGame || !strings.Contains(out["error"].(map[string]any)["message"].(string), "no dungeon run is going") {
			t.Errorf("%s: %v", m, out)
		}
	}
}

func TestTuneFileChoice(t *testing.T) {
	dir := t.TempDir()
	named := filepath.Join(dir, "mine.json")
	if err := os.WriteFile(named, []byte(`{}`), 0o600); err != nil {
		t.Fatal(err)
	}
	r := &dungeonRunner{}
	if p, err := r.tuneFile(dungeonRunParams{}, "dungeons/x.json"); p != "" || err != nil {
		t.Fatalf("%q %v", p, err) // the driver's default, beside the plan
	}
	r.tunePath = "login.tune.json" // --tune: need not exist yet
	if p, err := r.tuneFile(dungeonRunParams{}, "dungeons/x.json"); p != "login.tune.json" || err != nil {
		t.Fatalf("%q %v", p, err)
	}
	if p, err := r.tuneFile(dungeonRunParams{Tune: named}, "dungeons/x.json"); p != named || err != nil {
		t.Fatalf("%q %v", p, err) // the run's own wins and must exist
	}
	// The contents are checked at the call: a typo, a value over a limit and a broken rule are refused with the problems listed.
	bad := filepath.Join(dir, "bad.json")
	for _, content := range []string{`{"pul_range": 40}`, `{"pull_range": 500}`, `{"pull_standoff": 70}`, `{`} {
		if err := os.WriteFile(bad, []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
		_, err := r.tuneFile(dungeonRunParams{Tune: bad}, "dungeons/x.json")
		if e, ok := err.(*rpc.Error); !ok || e.Code != rpc.CodeInvalidParams || !strings.Contains(e.Message, "tune: "+bad) {
			t.Errorf("%s: %v", content, err)
		}
		r.tunePath = bad // the login's file is checked when it is there
		if _, err := r.tuneFile(dungeonRunParams{}, "dungeons/x.json"); err == nil {
			t.Errorf("login --tune %s accepted", content)
		}
		r.tunePath = "login.tune.json"
	}
}

func TestTuneDiffCommand(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "r-x.jsonl")
	lines := []string{
		`{"v":1,"k":"header","run":"r-x","src":"dungeon_run","t":0,"schema":"wowtrace","plan":"rig","mode":"remote"}`,
		`{"v":1,"k":"event","run":"r-x","src":"dungeon_run","t":100,"ev":"tune_change","ok":true,"key":"pull_range","old":45,"new":40,"source":"file","leg":1,"boundary":"leg_start"}`,
		`{"v":1,"k":"event","run":"r-x","src":"dungeon_run","t":200,"ev":"tune_change","ok":true,"key":"pull_range","old":40,"new":39,"source":"rpc","leg":1,"boundary":"pull_group_start"}`,
		`{"v":1,"k":"event","run":"r-x","src":"dungeon_run","t":300,"ev":"tune_change","ok":false,"source":"file","error":"unknown key"}`,
		`{"v":1,"k":"event","run":"r-x","src":"dungeon_run","t":400,"ev":"plan_reload","ok":true,"leg":1,"boundary":"leg_start","ops":[{"op":"replace","path":"/legs/1/pull_groups/1/radius","value":18,"old":0}]}`,
	}
	if err := os.WriteFile(path, []byte(strings.Join(lines, "\n")+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	var out, errOut bytes.Buffer
	if code := run(context.Background(), []string{"tune-diff", path}, &out, &errOut); code != exitOK {
		t.Fatalf("%d %s", code, errOut.String())
	}
	var got struct {
		Run       string
		Plan      string
		TunePatch []map[string]any `json:"tune_patch"`
		PlanPatch []map[string]any `json:"plan_patch"`
		Refused   []string
		Changes   int
	}
	if err := json.Unmarshal(out.Bytes(), &got); err != nil {
		t.Fatalf("%v\n%s", err, out.String())
	}
	if got.Run != "r-x" || got.Plan != "rig" || got.Changes != 3 || len(got.Refused) != 1 || len(got.TunePatch) != 1 || len(got.PlanPatch) != 1 {
		t.Fatalf("%s", out.String())
	}
	if p := got.TunePatch[0]; p["op"] != "add" || p["path"] != "/pull_range" || p["value"] != 39.0 {
		t.Fatalf("%v", p)
	}
	if p := got.PlanPatch[0]; p["op"] != "replace" || p["path"] != "/legs/1/pull_groups/1/radius" || p["value"] != 18.0 || p["old"] != nil {
		t.Fatalf("%v", p)
	}
	for _, args := range [][]string{{"tune-diff"}, {"tune-diff", "-x"}, {"tune-diff", filepath.Join(dir, "none.jsonl")}} {
		out.Reset()
		if code := run(context.Background(), args, &out, &errOut); code != exitUsage {
			t.Fatalf("%v: %d", args, code)
		}
	}
}

// A remote run end to end with live tuning: the tunables file beside the plan is read at the leg start, tune_set is checked at once and
// taken at the same place, tune_list shows the queued value, and the report lists both changes.
func TestTuneSetAndFileInARemoteRun(t *testing.T) {
	dir := t.TempDir()
	plan := `{"version":1,"name":"rem","map":0,"entrance":{"x":5,"y":5,"z":5},"legs":[{"name":"here","to":{"x":5,"y":5,"z":5},"steps":[{"wait_for":{"hostiles_gone":{"radius":30},"timeout_s":10}}]}]}`
	if err := os.WriteFile(filepath.Join(dir, "rem.json"), []byte(plan), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "rem.tune.json"), []byte(`{"assist_every_s": 15}`), 0o600); err != nil {
		t.Fatal(err)
	}
	r := newRigWith(t, session.Options{}, [3]float32{1, 2, 3}, methodOptions{DungeonsDir: dir})
	resultOf(t, r.call("dungeon_run", `{"name":"rem","force":true,"mode":"remote","lead_bot":"Tanky","preflight":"off"}`))
	r.expectBody(opcode.CMSGMessagechat, say(".bot commander bind Tanky"))
	r.system("TBM:ACTION_ACK|commander bind|party|1|Tanky")
	r.expectBody(opcode.CMSGMessagechat, say(".bot commander view on"))
	r.system("TBM:ACTION_ACK|commander view|party|1|on")
	r.system(viewFrame(1, true)...) // a hostile in view: the leg's wait holds, so the leg starts
	if out := r.call("tune_set", `{"key":"pull_range","value":500}`); errCode(out) != rpc.CodeInvalidParams || !strings.Contains(out["error"].(map[string]any)["message"].(string), "outside the limits") {
		t.Fatalf("%v", out)
	}
	ack := resultOf(t, r.call("tune_set", `{"key":"pull_range","value":41}`))
	if ack["key"] != "pull_range" || ack["old"].(float64) != 45 || ack["new"].(float64) != 41 {
		t.Fatalf("%v", ack)
	}
	listed := 0
	for _, in := range resultOf(t, r.call("tune_list", `{}`))["tunables"].([]any) {
		if m := in.(map[string]any); m["key"] == "pull_range" && m["min"].(float64) == 10 && m["max"].(float64) == 80 && m["default"].(float64) == 45 {
			listed++
		}
	}
	if listed != 1 {
		t.Fatal("tune_list does not show pull_range with its limits")
	}
	// The run reads the roster for its cc bot (resolveCCBot) before anything else is sent.
	r.expectBody(opcode.CMSGMessagechat, say(".bot roster"))
	r.system("TBM:ROSTER_BEGIN|1", "TBM:ROSTER|42|Tanky|1|online|1|Dun Morogh", "TBM:ROSTER_END")
	r.expectBody(opcode.CMSGMessagechat, say(".bot behavior Tanky loot on"))
	r.system("TBM:ACTION_ACK|behavior loot|bot:Tanky|1|on")
	// Both changes are taken at the leg's start, where the run stands on its wait; then the run is stopped.
	var changes []any
	for deadline := time.Now().Add(5 * time.Second); time.Now().Before(deadline) && len(changes) < 2; time.Sleep(20 * time.Millisecond) {
		rem := resultOf(t, r.call("dungeon_status", `{}`))["report"].(map[string]any)["remote"].(map[string]any)
		changes, _ = rem["tune_changes"].([]any)
	}
	r.system(viewFrame(2, false)...) // out of combat, so that the end of the run does not wait for a fight
	stopped := make(chan map[string]any, 1)
	go func() { stopped <- r.call("dungeon_stop", `{}`) }()
	r.expectBody(opcode.CMSGMessagechat, say(".bot cmd stay"))
	r.system("TBM:ACTION_ACK|cmd stay|party|1|Tanky")
	r.expectBody(opcode.CMSGMessagechat, say(".bot commander unbind"))
	r.system("TBM:ACTION_ACK|commander unbind|party|1|0")
	st := resultOf(t, <-stopped)
	ch := st["report"].(map[string]any)["remote"].(map[string]any)["tune_changes"].([]any)
	src := map[string]any{}
	for _, c := range ch {
		src[c.(map[string]any)["key"].(string)] = c.(map[string]any)["source"]
	}
	if len(ch) != 2 || src["assist_every_s"] != "file" || src["pull_range"] != "rpc" {
		t.Fatalf("%v", ch)
	}
}
