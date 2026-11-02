package dashboard

import (
	"bytes"
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

// M1: a tune_set that ran and only failed to be logged is 200 with executed and log_failed (never a 500 that invites a second tune_set), and
// the result line goes to stderr.
func TestControlTuneLogFailsAfterSuccessfulRPC(t *testing.T) {
	r := newTuneRig(t)
	var stderr bytes.Buffer
	saved := controlStderr
	controlStderr = &stderr
	t.Cleanup(func() { controlStderr = saved })
	r.ctl.log.w = &failAfter{w: r.ctl.log.w, left: 1} // the request line gets through, the result line does not
	rec := r.tune("rest_health_pct", 70)
	if rec.code != http.StatusOK {
		t.Fatalf("tune with a failing result log: %d %s", rec.code, rec.body)
	}
	var got map[string]any
	must(t, json.Unmarshal([]byte(rec.body), &got))
	if got["executed"] != true || got["log_failed"] != true {
		t.Fatalf("the reply is %s, want executed true and log_failed true", rec.body)
	}
	n := 0
	for _, m := range r.fake.methods() {
		if m == "tune_set" {
			n++
		}
	}
	if n != 1 {
		t.Fatalf("tune_set was sent %d times, want once", n)
	}
	if !strings.Contains(stderr.String(), `"phase":"result"`) || !strings.Contains(stderr.String(), `"verb":"rest_health_pct"`) {
		t.Errorf("stderr has no result line: %q", stderr.String())
	}
}

// M1: a tune_set whose reply is not an answer to its request is 502 "unknown: check the tunable's value", and its log line says indeterminate,
// with no ok.
func TestControlTuneIndeterminateReplyIsNotOK(t *testing.T) {
	r := newTuneRig(t)
	r.fake.raw("tune_set", `{}`)
	rec := r.tune("rest_health_pct", 70)
	if rec.code != http.StatusBadGateway || !strings.Contains(rec.body, "unknown: check the tunable's value") {
		t.Fatalf("tune with a bare {} reply: %d %s", rec.code, rec.body)
	}
	lines := r.logRecords()
	if len(lines) != 2 || lines[1]["outcome"] != "indeterminate" {
		t.Fatalf("the log is %v", lines)
	}
	if _, ok := lines[1]["ok"]; ok {
		t.Errorf("an indeterminate result line carries ok: %v", lines[1])
	}
}

// L6: an older agent's tune_list reports no min/max; a number is refused by name (never as "outside 0..0"), while "default" needs no bounds.
func TestControlTuneWithoutBounds(t *testing.T) {
	r := newTuneRig(t)
	r.fake.set("tune_list", `{"tunables":[{"key":"rest_health_pct","unit":"pct","doc":"health to rest at"}]}`)
	rec := r.tune("rest_health_pct", 70)
	if rec.code != http.StatusBadRequest || !strings.Contains(rec.body, "this agent does not report bounds for rest_health_pct") {
		t.Fatalf("a number without reported bounds: %d %s", rec.code, rec.body)
	}
	if strings.Contains(rec.body, "outside") {
		t.Errorf("the refusal reads as a 0..0 range: %s", rec.body)
	}
	if r.fake.sawWrite() {
		t.Fatal("a tune_set reached the agent although the bounds are unknown")
	}
	if rec := r.tune("rest_health_pct", "default"); rec.code != http.StatusOK {
		t.Fatalf("default without reported bounds: %d %s", rec.code, rec.body)
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
