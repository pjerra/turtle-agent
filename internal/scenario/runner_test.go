package scenario

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"strings"
	"testing"
	"time"

	"github.com/pjerra/turtle-agent/internal/events"
)

// runWith wires a Runner to fakes and runs f under a background context.
func runWith(t *testing.T, f *File, callers map[string]*fakeCaller, db *fakeDB, clk *fakeClock) (*Result, map[string]*fakeCaller) {
	t.Helper()
	r := &Runner{Sessions: map[string]Caller{}, Now: clk.Now, Sleep: clk.Sleep, Out: io.Discard}
	if db != nil {
		r.DB = db
	}
	for name, c := range callers {
		r.Sessions[name] = bindFake(c, clk)
		c.char = map[string]string{"A": "Tacmdaron", "B": "Tacmdb", "C": "Ragnar"}[name]
		if c.char == "" {
			c.char = name
		}
	}
	res, err := r.Run(context.Background(), f)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	return res, callers
}

func scenarioBase() string {
	return `{"scenario":"t","sessions":{"A":{"rpc":"127.0.0.1:8774"}},"steps":[`
}

// TestRunnerCapturesAndExpands pins capture + later use: an addon reply sets
// gid, the next send uses it, and the norm line keeps the template.
func TestRunnerCapturesAndExpands(t *testing.T) {
	f := parseFile(t, scenarioBase()+`
		{"do":"expect","session":"A","kind":"addon","prefix":"TW_LFG","match":"^S2C_GROUPS_LIST_UPDATE;\\{\"id\":(\\d+),\"creator\":\"Tacmdaron\"","capture":{"gid":1}},
		{"do":"addon_send","session":"A","channel":"guild","prefix":"TW_LFG","text":"C2S_GET_GROUP_DETAILS;${gid}"}
	]}`)
	a := &fakeCaller{}
	a.script = func(call int) {
		if call == 1 {
			a.add(addonEvent("noise", "TW_LFG", "guild"))
			a.add(addonEvent(`S2C_GROUPS_LIST_UPDATE;{"id":7,"creator":"Tacmdaron","title":"T1"}`, "TW_LFG", "guild"))
		}
	}
	clk := newFakeClock()
	res, _ := runWith(t, f, map[string]*fakeCaller{"A": a}, nil, clk)
	if res.Failed != 0 || res.Errors != 0 || res.Exit != 0 {
		t.Fatalf("%+v", res)
	}
	sends := a.called("addon_send")
	if len(sends) != 1 {
		t.Fatalf("sends %v", sends)
	}
	var p struct {
		Text string `json:"text"`
	}
	if err := json.Unmarshal(sends[0].Params, &p); err != nil || p.Text != "C2S_GET_GROUP_DETAILS;7" {
		t.Fatalf("params %s %v", sends[0].Params, err)
	}
	// The norm line keeps the template.
	found := false
	for _, rec := range res.Records {
		if rec.T == "action" && strings.Contains(rec.Norm, "C2S_GET_GROUP_DETAILS;${gid}") {
			found = true
		}
		if strings.Contains(rec.Norm, "C2S_GET_GROUP_DETAILS;7") {
			t.Fatalf("norm leaks the value: %q", rec.Norm)
		}
	}
	if !found {
		t.Fatal("no template norm line")
	}
}

// TestRunnerExpectIsOrderedPerSession pins ordered matching: expects see only
// events after the previous match.
func TestRunnerExpectIsOrderedPerSession(t *testing.T) {
	f := parseFile(t, scenarioBase()+`
		{"do":"mark"},
		{"do":"expect","session":"A","match":"^start$","timeout_s":1},
		{"do":"expect","session":"A","match":"^end$","timeout_s":1}
	]}`)
	a := &fakeCaller{}
	// The live agent appends end before start; matching is ordered, so the
	// first expect (start) matches seq 2 and the second (end) sees only
	// seq > 2 and fails.
	a.script = func(call int) {
		if call == 2 {
			a.add(events.Event{Kind: "chat", Text: "end"})
			a.add(events.Event{Kind: "chat", Text: "start"})
		}
	}
	clk := newFakeClock()
	res, _ := runWith(t, f, map[string]*fakeCaller{"A": a}, nil, clk)
	if res.Passed != 1 || res.Failed != 1 || res.Exit != 14 {
		t.Fatalf("%+v", res)
	}
}

// TestRunnerExpectTimesOut pins the FAIL text and the 14 exit, and that the
// fake clock advanced by the timeout.
func TestRunnerExpectTimesOut(t *testing.T) {
	f := parseFile(t, scenarioBase()+`
		{"do":"expect","session":"A","match":"^never$","timeout_s":3}
	]}`)
	a := &fakeCaller{}
	clk := newFakeClock()
	res, _ := runWith(t, f, map[string]*fakeCaller{"A": a}, nil, clk)
	if res.Failed != 1 || res.Exit != 14 {
		t.Fatalf("%+v", res)
	}
	if !strings.Contains(res.Checks[0].Reason, "no matching line within 3 s") {
		t.Fatalf("%+v", res.Checks[0])
	}
	if clk.slept() < 3*time.Second {
		t.Fatalf("slept %v", clk.slept())
	}
}

// TestRunnerExpectNotScansFromTheWindow pins that expect_not scans from the
// window (section/mark), not from the match cursor: the FAIL case has X after
// the window but consumed before from.
func TestRunnerExpectNotScansFromTheWindow(t *testing.T) {
	mk := func(match string) *File {
		return parseFile(t, scenarioBase()+`
			{"do":"mark"},
			{"do":"expect","session":"A","match":"^keep$","timeout_s":1},
			{"do":"expect_not","session":"A","match":"`+match+`","within_s":0}
		]}`)
	}
	// X and keep arrive after the mark: the expect consumes keep (from=2)
	// while X (seq 1, after the window 0) still fails the expect_not.
	a := &fakeCaller{}
	a.script = func(call int) {
		if call == 2 {
			a.add(events.Event{Kind: "chat", Text: "X"})
			a.add(events.Event{Kind: "chat", Text: "keep"})
		}
	}
	clk := newFakeClock()
	res, _ := runWith(t, mk("^X$"), map[string]*fakeCaller{"A": a}, nil, clk)
	if res.Failed != 1 {
		t.Fatalf("want FAIL, %+v", res)
	}
	if !strings.Contains(res.Checks[1].Reason, "seen at seq 1") {
		t.Fatalf("%+v", res.Checks[1])
	}
	// No X: PASS.
	b := &fakeCaller{}
	b.script = func(call int) {
		if call == 2 {
			b.add(events.Event{Kind: "chat", Text: "keep"})
		}
	}
	clk2 := newFakeClock()
	res2, _ := runWith(t, mk("^X$"), map[string]*fakeCaller{"A": b}, nil, clk2)
	if res2.Failed != 0 || res2.Passed != 2 {
		t.Fatalf("%+v", res2)
	}
}

// TestRunnerStopSkipsTheRestAndRunsFinally pins on_fail stop: later main
// steps print SKIP, finally still runs, exit 14.
func TestRunnerStopSkipsTheRestAndRunsFinally(t *testing.T) {
	f := parseFile(t, `{"scenario":"t","sessions":{"A":{"rpc":"127.0.0.1:8774"}},"on_fail":"stop","steps":[
		{"do":"expect","session":"A","match":"^nope$","timeout_s":1},
		{"do":"rpc","session":"A","method":"leave_group"},
		{"do":"rpc","session":"A","method":"party"}
	],"finally":[{"do":"rpc","session":"A","method":"logout"}]}`)
	a := &fakeCaller{}
	clk := newFakeClock()
	res, _ := runWith(t, f, map[string]*fakeCaller{"A": a}, nil, clk)
	if res.Failed != 1 || res.Skipped != 2 || res.Exit != 14 {
		t.Fatalf("%+v", res)
	}
	if a.callCount("leave_group") != 0 || a.callCount("party") != 0 || a.callCount("logout") != 1 {
		t.Fatalf("calls %+v", a.calls)
	}
	haveSkip := false
	for _, rec := range res.Records {
		if rec.T == "skip" {
			haveSkip = true
		}
	}
	if !haveSkip {
		t.Fatal("no SKIP records")
	}
}

// TestRunnerContinueRunsEverything pins on_fail continue: the step after a
// FAIL still runs.
func TestRunnerContinueRunsEverything(t *testing.T) {
	f := parseFile(t, `{"scenario":"t","sessions":{"A":{"rpc":"127.0.0.1:8774"}},"steps":[
		{"do":"expect","session":"A","match":"^nope$","timeout_s":1},
		{"do":"rpc","session":"A","method":"leave_group"}
	]}`)
	a := &fakeCaller{}
	clk := newFakeClock()
	res, _ := runWith(t, f, map[string]*fakeCaller{"A": a}, nil, clk)
	if res.Failed != 1 || res.Exit != 14 {
		t.Fatalf("%+v", res)
	}
	if a.callCount("leave_group") != 1 {
		t.Fatalf("calls %+v", a.calls)
	}
}

// TestRunnerRPCErrorAborts pins that an RPC error (without ignore_error) is
// an ERROR: rest SKIP, finally ran, exit 15. With ignore_error it continues.
func TestRunnerRPCErrorAborts(t *testing.T) {
	mk := func(ie bool) *File {
		ieS := ""
		if ie {
			ieS = `,"ignore_error":true`
		}
		return parseFile(t, `{"scenario":"t","sessions":{"A":{"rpc":"127.0.0.1:8774"}},"steps":[
			{"do":"rpc","session":"A","method":"invite","params":{"name":"X"}`+ieS+`},
			{"do":"rpc","session":"A","method":"party"}
		],"finally":[{"do":"rpc","session":"A","method":"logout"}]}`)
	}
	a := &fakeCaller{results: map[string]fakeResult{"invite": {Err: &RPCError{Code: -32003, Message: "nope"}}}}
	clk := newFakeClock()
	res, _ := runWith(t, mk(false), map[string]*fakeCaller{"A": a}, nil, clk)
	if res.Errors != 1 || res.Exit != 15 || a.callCount("party") != 0 || a.callCount("logout") != 1 {
		t.Fatalf("%+v calls %+v", res, a.calls)
	}
	b := &fakeCaller{results: map[string]fakeResult{"invite": {Err: &RPCError{Code: -32003, Message: "nope"}}}}
	clk2 := newFakeClock()
	res2, _ := runWith(t, mk(true), map[string]*fakeCaller{"A": b}, nil, clk2)
	if res2.Errors != 0 || res2.Exit != 0 || b.callCount("party") != 1 {
		t.Fatalf("%+v calls %+v", res2, b.calls)
	}
}

// TestRunnerDBErrorIsAnErrorNotAFail pins that a failing DB query is an ERROR
// (exit 15), never a FAIL: a check that cannot be evaluated claims nothing
// about the server.
func TestRunnerDBErrorIsAnErrorNotAFail(t *testing.T) {
	f := parseFile(t, `{"scenario":"t","sessions":{"A":{"rpc":"127.0.0.1:8774"}},
		"db":{"host":"m910q","container":"tortoise-db"},
		"steps":[{"do":"check_db","table":"tw_world.lft_user_groups","rows":0}]}`)
	a := &fakeCaller{}
	clk := newFakeClock()
	db := &fakeDB{errs: []error{errors.New("ssh: boom")}}
	res, _ := runWith(t, f, map[string]*fakeCaller{"A": a}, db, clk)
	if res.Errors != 1 || res.Failed != 0 || res.Exit != 15 {
		t.Fatalf("%+v", res)
	}
}

// TestRunnerDBWithinPolls pins the 5 s poll loop: two 5s then 0 with
// rows 0 within 15 s passes on the third query; an always-5 fails got 5.
func TestRunnerDBWithinPolls(t *testing.T) {
	mk := func() *File {
		return parseFile(t, `{"scenario":"t","sessions":{"A":{"rpc":"127.0.0.1:8774"}},
			"db":{"host":"m910q","container":"tortoise-db"},
			"steps":[{"do":"check_db","table":"tw_world.lft_user_groups","rows":0,"within_s":15}]}`)
	}
	a := &fakeCaller{}
	clk := newFakeClock()
	db := &fakeDB{counts: []int64{5, 5, 0}}
	res, _ := runWith(t, mk(), map[string]*fakeCaller{"A": a}, db, clk)
	if res.Passed != 1 || res.Errors != 0 {
		t.Fatalf("%+v", res)
	}
	if db.calls != 3 {
		t.Fatalf("queries %d", db.calls)
	}
	if clk.slept() < 10*time.Second {
		t.Fatalf("slept %v", clk.slept())
	}
	b := &fakeCaller{}
	clk2 := newFakeClock()
	db2 := &fakeDB{counts: []int64{5}}
	res2, _ := runWith(t, mk(), map[string]*fakeCaller{"A": b}, db2, clk2)
	if res2.Failed != 1 || !strings.Contains(res2.Checks[0].Reason, "got 5") {
		t.Fatalf("%+v", res2)
	}
}

// TestRunnerLogoutClosesTheSession pins that after an rpc logout the session
// is skipped by later drains, and a later gm on it is an ERROR.
func TestRunnerLogoutClosesTheSession(t *testing.T) {
	f := parseFile(t, `{"scenario":"t","sessions":{"A":{"rpc":"127.0.0.1:8774"}},"steps":[
		{"do":"rpc","session":"A","method":"logout"},
		{"do":"gm","session":"A","text":".go xyz 1 2 3 0"}
	]}`)
	a := &fakeCaller{}
	clk := newFakeClock()
	before := a.callCount("events")
	res, _ := runWith(t, f, map[string]*fakeCaller{"A": a}, nil, clk)
	if a.callCount("events") != before {
		t.Fatalf("drained a closed session: %+v", a.calls)
	}
	if res.Errors != 1 || res.Exit != 15 {
		t.Fatalf("%+v", res)
	}
	if !strings.Contains(res.Checks[0].Reason, "session A is closed (it logged out at step 1)") {
		t.Fatalf("%+v", res.Checks[0])
	}
}

// TestRunnerNeverWritesTheToken pins redaction: a reply echoing the token
// leaves <token> in every stored record.
func TestRunnerNeverWritesTheToken(t *testing.T) {
	f := parseFile(t, scenarioBase()+`
		{"do":"rpc","session":"A","method":"party","record_result":true}
	]}`)
	a := &fakeCaller{token: "sekret-token-123"}
	a.char = "Tacmdaron"
	a.results = map[string]fakeResult{"party": {Result: map[string]any{"tok": "sekret-token-123"}}}
	clk := newFakeClock()
	res, _ := runWith(t, f, map[string]*fakeCaller{"A": a}, nil, clk)
	if res.Errors != 0 {
		t.Fatalf("%+v", res)
	}
	for _, rec := range res.Records {
		if strings.Contains(rec.Raw, "sekret-token-123") || strings.Contains(rec.Norm, "sekret-token-123") {
			t.Fatalf("token leaked: %+v", rec)
		}
	}
	found := false
	for _, rec := range res.Records {
		if rec.T == "result" && strings.Contains(rec.Raw, "<token>") {
			found = true
		}
	}
	if !found {
		t.Fatal("no redacted result line")
	}
}

// TestRunnerWaitTimeoutAborts pins that a wait timeout is an ERROR, exit 15.
func TestRunnerWaitTimeoutAborts(t *testing.T) {
	f := parseFile(t, scenarioBase()+`
		{"do":"wait","session":"A","match":"^never$","timeout_s":2}
	]}`)
	a := &fakeCaller{}
	clk := newFakeClock()
	res, _ := runWith(t, f, map[string]*fakeCaller{"A": a}, nil, clk)
	if res.Errors != 1 || res.Exit != 15 {
		t.Fatalf("%+v", res)
	}
	if !strings.Contains(res.Checks[0].Reason, "no matching line within 2 s") {
		t.Fatalf("%+v", res.Checks[0])
	}
}

// TestRunnerCheckRPC pins equals, regexp match and len over a result path.
func TestRunnerCheckRPC(t *testing.T) {
	f := parseFile(t, scenarioBase()+`
		{"do":"check_rpc","session":"A","method":"party","path":"in_group","equals":true},
		{"do":"check_rpc","session":"A","method":"party","path":"members.0.name","match":"^Tacmd"},
		{"do":"check_rpc","session":"A","method":"party","path":"members","len":2}
	]}`)
	a := &fakeCaller{}
	a.results = map[string]fakeResult{"party": {Result: map[string]any{
		"in_group": true,
		"members":  []any{map[string]any{"name": "Tacmdaron"}},
	}}}
	clk := newFakeClock()
	res, _ := runWith(t, f, map[string]*fakeCaller{"A": a}, nil, clk)
	if res.Passed != 2 || res.Failed != 1 || res.Exit != 14 {
		t.Fatalf("%+v", res)
	}
	if !strings.Contains(res.Checks[2].Reason, "got 1") {
		t.Fatalf("%+v", res.Checks[2])
	}
	// A missing path is a FAIL, not a crash.
	f2 := parseFile(t, scenarioBase()+`
		{"do":"check_rpc","session":"A","method":"party","path":"nope.nothing","equals":1}
	]}`)
	b := &fakeCaller{}
	b.results = a.results
	clk2 := newFakeClock()
	res2, _ := runWith(t, f2, map[string]*fakeCaller{"A": b}, nil, clk2)
	if res2.Failed != 1 {
		t.Fatalf("%+v", res2)
	}
}

// TestRunnerInterruptRunsFinally pins that cancelling the run context during
// a sleep aborts with an interrupted ERROR while finally still runs under
// its own context, exit 15.
func TestRunnerInterruptRunsFinally(t *testing.T) {
	f := parseFile(t, `{"scenario":"t","sessions":{"A":{"rpc":"127.0.0.1:8774"}},"steps":[
		{"do":"sleep","seconds":60}
	],"finally":[{"do":"rpc","session":"A","method":"logout"}]}`)
	a := &fakeCaller{}
	a.char = "Tacmdaron"
	clk := newFakeClock()
	r := &Runner{Sessions: map[string]Caller{"A": a}, Now: clk.Now, Sleep: clk.Sleep, Out: io.Discard}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	res, err := r.Run(ctx, f)
	if err != nil {
		t.Fatal(err)
	}
	if res.Errors != 1 || res.Exit != 15 || a.callCount("logout") != 1 {
		t.Fatalf("%+v calls %+v", res, a.calls)
	}
	if !strings.Contains(res.Checks[0].Reason, "interrupted") {
		t.Fatalf("%+v", res.Checks[0])
	}
}

// TestRunnerStatusProbeFails pins that a session not answering status aborts
// before any step is sent: no finally, exit 15.
func TestRunnerStatusProbeFails(t *testing.T) {
	f := parseFile(t, `{"scenario":"t","sessions":{"A":{"rpc":"127.0.0.1:8774"}},"steps":[
		{"do":"rpc","session":"A","method":"party"}
	],"finally":[{"do":"rpc","session":"A","method":"logout"}]}`)
	a := &fakeCaller{statusErr: errors.New("connection refused")}
	clk := newFakeClock()
	r := &Runner{Sessions: map[string]Caller{"A": a}, Now: clk.Now, Sleep: clk.Sleep, Out: io.Discard}
	res, err := r.Run(context.Background(), f)
	if err != nil {
		t.Fatal(err)
	}
	if res.Errors != 1 || res.Exit != 15 {
		t.Fatalf("%+v", res)
	}
	if a.callCount("party") != 0 || a.callCount("logout") != 0 {
		t.Fatalf("sent steps: %+v", a.calls)
	}
}

// TestRunnerGMDenyAtRunTime pins the GM deny list after var expansion: a var
// smuggling "reload all" into a gm step is an ERROR and nothing is sent.
func TestRunnerGMDenyAtRunTime(t *testing.T) {
	f := parseFile(t, `{"scenario":"t","sessions":{"A":{"rpc":"127.0.0.1:8774"}},
		"vars":{"cmd":"x"},"steps":[{"do":"gm","session":"A","text":".${cmd}"}]}`)
	f.Vars["cmd"] = "reload all"
	a := &fakeCaller{}
	clk := newFakeClock()
	res, _ := runWith(t, f, map[string]*fakeCaller{"A": a}, nil, clk)
	if res.Errors != 1 || res.Exit != 15 {
		t.Fatalf("%+v", res)
	}
	if a.callCount("gm") != 0 {
		t.Fatalf("sent: %+v", a.calls)
	}
}
