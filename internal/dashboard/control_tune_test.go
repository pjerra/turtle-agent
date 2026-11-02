package dashboard

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"
)

const tuneListJSON = `{"tunables":[
 {"key":"rest_health_pct","unit":"pct","min":0,"max":100,"default":55,"base":55,"value":55,"source":"base","doc":"health to rest at"},
 {"key":"pull_range","unit":"yd","min":5,"max":40,"default":20,"base":20,"value":20,"source":"base","doc":"pull reach"}]}`

func newTuneRig(t *testing.T) *controlRig {
	t.Helper()
	r := newControlRig(t)
	r.fake.set("dungeon_status", `{"running":true,"state":"running","report":{"state":"running","remote":{"mode":"remote"}}}`)
	r.fake.set("tune_list", tuneListJSON)
	r.fake.set("tune_set", `{"key":"rest_health_pct","old":55,"new":70,"applies":"at the next leg start or pull-group start"}`)
	return r
}

func (r *controlRig) tune(key string, value any) reply {
	r.t.Helper()
	return r.post("/api/control/"+r.agent.ID+"/tune", map[string]any{"key": key, "value": value}, r.token, nil)
}

func TestControlTuneSetSendsTuneSet(t *testing.T) {
	r := newTuneRig(t)
	rec := r.tune("rest_health_pct", 70)
	if rec.code != http.StatusOK {
		t.Fatalf("tune: %d %s", rec.code, rec.body)
	}
	calls := r.fake.methods()
	if len(calls) != 2 || calls[0] != "tune_list" || calls[1] != "tune_set" {
		t.Fatalf("the fake saw %v, want [tune_list tune_set]", calls)
	}
	p, ok := r.fake.paramsOf("tune_set")
	if !ok {
		t.Fatal("tune_set was not sent")
	}
	var got map[string]any
	must(t, json.Unmarshal(p, &got))
	if len(got) != 2 || got["key"] != "rest_health_pct" || got["value"] != float64(70) {
		t.Errorf("tune_set params are %s", p)
	}
	var rep struct {
		Status string         `json:"status"`
		Result map[string]any `json:"result"`
	}
	must(t, json.Unmarshal([]byte(rec.body), &rep))
	if rep.Status != "ok" || rep.Result["new"] != float64(70) || rep.Result["applies"] == "" {
		t.Errorf("the reply does not carry the ack: %s", rec.body)
	}
	lines := r.logRecords()
	if len(lines) != 2 || lines[0]["phase"] != "request" || lines[1]["phase"] != "result" || lines[0]["action"] != "tune" || lines[1]["ok"] != true {
		t.Errorf("the log lines are %v", lines)
	}
	if lines[0]["who"] != "owner" {
		t.Errorf("the log says who %v", lines[0]["who"])
	}
}

func TestControlTuneRefusals(t *testing.T) {
	cases := map[string]struct {
		key   string
		value any
	}{
		"an unknown key":       {"frobnicate", 0},
		"over the max":         {"rest_health_pct", 101},
		"under the min":        {"pull_range", 4.9},
		"a fraction of a pct":  {"rest_health_pct", 70.5},
		"a number in a string": {"rest_health_pct", "70"},
		"null":                 {"rest_health_pct", nil},
		"a true value":         {"rest_health_pct", true},
	}
	for name, c := range cases {
		r := newTuneRig(t)
		if rec := r.tune(c.key, c.value); rec.code != http.StatusBadRequest {
			t.Errorf("%s: %d %s, want 400", name, rec.code, rec.body)
		}
		if r.fake.sawWrite() {
			t.Errorf("%s: a tune_set reached the agent", name)
		}
	}
	// a tune_list the agent refuses (no remote run): its message comes back as a 409.
	r := newTuneRig(t)
	r.fake.fail("tune_list", `live tuning is for runs in mode "remote"`)
	rec := r.tune("rest_health_pct", 70)
	if rec.code != http.StatusConflict || !strings.Contains(rec.body, "live tuning is for runs in mode") {
		t.Errorf("a refused tune_list: %d %s", rec.code, rec.body)
	}
	if r.fake.sawWrite() {
		t.Error("a tune_set reached the agent without a tune_list")
	}
}

func TestControlTuneDefault(t *testing.T) {
	r := newTuneRig(t)
	r.fake.set("tune_set", `{"key":"rest_health_pct","old":70,"new":55,"applies":"at the next leg start or pull-group start"}`)
	if rec := r.tune("rest_health_pct", "default"); rec.code != http.StatusOK {
		t.Fatalf("default: %d %s", rec.code, rec.body)
	}
	p, ok := r.fake.paramsOf("tune_set")
	if !ok {
		t.Fatal("tune_set was not sent")
	}
	var got map[string]any
	must(t, json.Unmarshal(p, &got))
	if len(got) != 2 || got["key"] != "rest_health_pct" || got["value"] != "default" {
		t.Errorf("tune_set params are %s", p)
	}
}

func TestCardTuneAll(t *testing.T) {
	tune := json.RawMessage(`{"tunables":[
	 {"key":"rest_health_pct","unit":"pct","min":0,"max":100,"default":55,"base":55,"value":55,"source":"base","queued":70,"doc":"health to rest at"},
	 {"key":"pull_range","unit":"yd","min":5,"max":40,"default":20,"base":30,"value":20,"source":"rpc","doc":"pull reach"},
	 {"key":"big_pack","unit":"","min":0,"max":1,"default":1,"base":1,"value":1,"source":"base","doc":"big packs"}]}`)
	c := BuildCard(Inputs{Agent: Agent{ID: "1"}, Now: t0, Status: json.RawMessage(statusJSON), Dungeon: json.RawMessage(dungeonJSON("leg 0 (one): walk to 1 2 3", 0)), Tune: tune})
	if len(c.TuneAll) != 3 {
		t.Fatalf("tune_all: %+v", c.TuneAll)
	}
	if c.TuneAll[0].Key != "rest_health_pct" || c.TuneAll[0].Unit != "pct" || c.TuneAll[0].Max != 100 || c.TuneAll[0].Queued == nil || *c.TuneAll[0].Queued != 70 {
		t.Errorf("the first row is %+v", c.TuneAll[0])
	}
	if c.TuneAll[1].Base != 30 || c.TuneAll[1].Source != "rpc" || c.TuneAll[2].Queued != nil {
		t.Errorf("the rows are %+v", c.TuneAll)
	}
	if len(c.Tunables) != 1 || c.Tunables[0].Key != "pull_range" || c.Tunables[0].Source != "rpc" {
		t.Errorf("Tunables: %+v", c.Tunables)
	}
}

func TestControlTune(t *testing.T) { runCheck(t, "ctltune_check.js", "ctltune.js") }
