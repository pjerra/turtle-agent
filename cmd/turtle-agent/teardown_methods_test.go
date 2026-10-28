package main

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/pjerra/turtle-agent/internal/session"
	"github.com/pjerra/turtle-agent/internal/tbm"
)

// tdFakeGame is a fake teardownGame recording every command it gets.
type tdFakeGame struct {
	mu         sync.Mutex
	char       string
	botCmds    []string
	gmCmds     []string
	replies    map[string]func() (tbm.Reply, error)
	gmReplies  map[string]func() (string, bool, error)
	party      session.PartyInfo
	visible    []string
	leaveErr   error
	left       bool
	logouts    []error
	logoutAt   int
	runOn      bool
	runState   string
	brainOn    bool
	brainRan   bool
	stopped    bool
	visibleSeq [][]string
	followOn   bool // a player follow loop runs
	followHang bool // ... and does not end within follow_stop's wait
	followStop int  // StopFollow calls
}

func tdAck(lines ...string) tbm.Reply {
	return tbm.Reply{Kind: tbm.KindAck, Ack: &tbm.Ack{Intent: "x", Scope: "party", Count: 1, Executor: "Lead"}, Lines: lines}
}

func (f *tdFakeGame) Character() string { return f.char }

func (f *tdFakeGame) BotCommand(ctx context.Context, cmd string, wait time.Duration) (tbm.Reply, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.botCmds = append(f.botCmds, cmd)
	if fn, ok := f.replies[cmd]; ok {
		return fn()
	}
	return tbm.Reply{}, fmt.Errorf("unexpected command %q", cmd)
}

func (f *tdFakeGame) GMReply(ctx context.Context, text string, match func(string) bool, wait time.Duration) (string, bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.gmCmds = append(f.gmCmds, text)
	if fn, ok := f.gmReplies[text]; ok {
		return fn()
	}
	return "", false, fmt.Errorf("unexpected gm %q", text)
}

func (f *tdFakeGame) Party() session.PartyInfo { f.mu.Lock(); defer f.mu.Unlock(); return f.party }

func (f *tdFakeGame) VisibleNames() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.visibleSeq) > 0 {
		v := f.visibleSeq[0]
		f.visibleSeq = f.visibleSeq[1:]
		return v
	}
	return append([]string(nil), f.visible...)
}

func (f *tdFakeGame) LeaveGroup(ctx context.Context) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.left = true
	return f.leaveErr
}

func (f *tdFakeGame) Logout(ctx context.Context) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	i := f.logoutAt
	if i >= len(f.logouts) {
		return "complete", nil
	}
	f.logoutAt++
	if err := f.logouts[i]; err != nil {
		return "", err
	}
	return "complete", nil
}

func (f *tdFakeGame) AgentRunActive() bool { f.mu.Lock(); defer f.mu.Unlock(); return f.runOn }

func (f *tdFakeGame) StopAgentRun() string {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.runOn = false
	return f.runState
}

func (f *tdFakeGame) BrainFree() bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.brainOn && !f.brainRan
}

func (f *tdFakeGame) StopBrain() { f.mu.Lock(); defer f.mu.Unlock(); f.brainOn = false }

func (f *tdFakeGame) FollowActive() bool { f.mu.Lock(); defer f.mu.Unlock(); return f.followOn }

func (f *tdFakeGame) StopFollow() (string, bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.followStop++
	if f.followHang {
		return "stopped by follow_stop", false
	}
	f.followOn = false
	return "stopped by follow_stop", true
}

func tdBaseFake() *tdFakeGame {
	statusLine := "You command Ragnar's group: Ragnar, Bo, Beardmeister."
	runLine := "TBM:RUN|zulfarrak|running|76/247|room|pull|gather|at the door"
	return &tdFakeGame{
		char: "Ragnar",
		replies: map[string]func() (tbm.Reply, error){
			"commander status": func() (tbm.Reply, error) { return tdAck(statusLine), nil },
			"run status":       func() (tbm.Reply, error) { return tdAck(runLine), nil },
			"run stop":         func() (tbm.Reply, error) { return tdAck("TBM:RUN|zulfarrak|done|247/247|-|-|-|-"), nil },
			"commander unbind": func() (tbm.Reply, error) {
				return tbm.Reply{Kind: tbm.KindAck, Ack: &tbm.Ack{Intent: "unbind", Scope: "party", Count: 2, Executor: "Ragnar"},
					Line: "ack", Lines: []string{"You no longer command Ragnar's group."}}, nil
			},
		},
		gmReplies: map[string]func() (string, bool, error){
			".bot remove Bo": func() (string, bool, error) { return "Hire ended for Bo; its character is being deleted.", true, nil },
			".bot remove Beardmeister": func() (string, bool, error) {
				return "Removal requested for bot Beardmeister; Headless cleanup completes asynchronously.", true, nil
			},
			".gm off": func() (string, bool, error) { return "GM mode is OFF", true, nil },
		},
		party: session.PartyInfo{InGroup: true, Members: []session.PartyMember{{Name: "Bo"}, {Name: "Beardmeister"}}},
		runOn: true, runState: "stopped", brainOn: true,
		logouts: []error{nil},
	}
}

func tdStepNames(res teardownResult) []string {
	var out []string
	for _, s := range res.Steps {
		out = append(out, s.Step)
	}
	return out
}

func TestTeardownOrder(t *testing.T) {
	f := tdBaseFake()
	res, err := runTeardown(context.Background(), f, teardownParams{WaitMS: 4000}, func(time.Duration) {})
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"snapshot", "follow", "agent_run", "brain", "run_stop", "unbind", "remove Bo", "remove Beardmeister", "leave_group", "gone", "gm", "logout"}
	if got := tdStepNames(res); strings.Join(got, ",") != strings.Join(want, ",") {
		t.Fatalf("steps %v", got)
	}
	wantCmds := []string{"commander status", "run status", "run stop", "commander unbind"}
	if strings.Join(f.botCmds, ",") != strings.Join(wantCmds, ",") {
		t.Fatalf("bot cmds %v", f.botCmds)
	}
	wantGM := []string{".bot remove Bo", ".bot remove Beardmeister", ".gm off"}
	if strings.Join(f.gmCmds, ",") != strings.Join(wantGM, ",") {
		t.Fatalf("gm cmds %v", f.gmCmds)
	}
	// agent_run stop after commander status but before run stop: agent_run ran (runOn false now).
	if f.runOn {
		t.Fatal("agent run not stopped")
	}
	if res.Listed == nil || len(res.Listed) != 2 || !res.Bound || res.Lead != "Lead" {
		t.Fatalf("result %+v", res)
	}
	if !strings.Contains(res.Steps[0].Detail, "bound to") || !strings.Contains(res.Steps[0].Detail, "in a group of") {
		t.Fatalf("snapshot detail %q", res.Steps[0].Detail)
	}
}

func TestTeardownNeverRemovesInAMixedGroup(t *testing.T) {
	f := tdBaseFake()
	f.party.Members = append(f.party.Members, session.PartyMember{Name: "Pipwren"})
	res, err := runTeardown(context.Background(), f, teardownParams{WaitMS: 4000}, func(time.Duration) {})
	if err != nil {
		t.Fatal(err)
	}
	for _, c := range f.gmCmds {
		if strings.HasPrefix(c, ".bot remove") {
			t.Fatalf("sent %q in a mixed group", c)
		}
	}
	found := false
	for _, s := range res.Steps {
		if s.Step == "remove" && strings.Contains(s.Detail, "Pipwren") && strings.Contains(s.Detail, "(not a bot of commander status)") {
			found = true
		}
	}
	if !found {
		t.Fatalf("no mixed-group skip: %+v", res.Steps)
	}
	if !f.left {
		t.Fatal("leave not sent")
	}
}

func TestTeardownRemovesOnlyListedNames(t *testing.T) {
	f := tdBaseFake()
	f.visible = []string{"Extra"}
	// status listing also names our own character: never removed, no step.
	f.replies["commander status"] = func() (tbm.Reply, error) {
		return tdAck("You command Ragnar's group: Ragnar, Bo."), nil
	}
	f.party.Members = []session.PartyMember{{Name: "Bo"}}
	delete(f.gmReplies, ".bot remove Beardmeister")
	res, err := runTeardown(context.Background(), f, teardownParams{WaitMS: 4000}, func(time.Duration) {})
	if err != nil {
		t.Fatal(err)
	}
	for _, s := range res.Steps {
		if s.Step == "remove Ragnar" || s.Step == "remove Extra" {
			t.Fatalf("bad remove step %q", s.Step)
		}
	}
}

func TestTeardownGoesOnAfterAFailure(t *testing.T) {
	f := tdBaseFake()
	f.replies["run stop"] = func() (tbm.Reply, error) { return tbm.Reply{}, fmt.Errorf("boom") }
	res, err := runTeardown(context.Background(), f, teardownParams{WaitMS: 4000}, func(time.Duration) {})
	if err != nil {
		t.Fatal(err)
	}
	if res.Failed != 1 {
		t.Fatalf("failed=%d", res.Failed)
	}
	if len(res.Steps) != 12 { // snapshot, follow, agent_run, brain, run_stop, ...: the follow step came with the player follow
		t.Fatalf("steps %d", len(res.Steps))
	}
	if res.Steps[4].Status != "fail" {
		t.Fatalf("run_stop %v", res.Steps[4])
	}
}

func TestTeardownDryRunSendsOnlyReads(t *testing.T) {
	f := tdBaseFake()
	res, err := runTeardown(context.Background(), f, teardownParams{WaitMS: 4000, DryRun: true}, func(time.Duration) {})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Join(f.botCmds, ",") != "commander status,run status" {
		t.Fatalf("bot cmds %v", f.botCmds)
	}
	if len(f.gmCmds) != 0 {
		t.Fatalf("gm cmds %v", f.gmCmds)
	}
	if !res.DryRun {
		t.Fatal("not dry")
	}
	for _, s := range res.Steps[1:] {
		if s.Status != "plan" && s.Status != "skip" {
			t.Fatalf("step %v not plan", s)
		}
	}
}

func TestTeardownRetriesARefusedLogout(t *testing.T) {
	f := tdBaseFake()
	f.logouts = []error{&session.LogoutError{Reason: 1}, &session.LogoutError{Reason: 1}, nil}
	var slept []time.Duration
	res, err := runTeardown(context.Background(), f, teardownParams{WaitMS: 4000}, func(d time.Duration) { slept = append(slept, d) })
	if err != nil {
		t.Fatal(err)
	}
	last := res.Steps[len(res.Steps)-1]
	if last.Step != "logout" || last.Status != "ok" {
		t.Fatalf("logout %v", last)
	}
	if len(slept) != 2 || slept[0] != logoutRetryEvery {
		t.Fatalf("sleeps %v", slept)
	}
}

func TestTeardownRemoveReplies(t *testing.T) {
	for _, c := range []struct {
		line, name, status, detail string
	}{
		{"Hire ended for Bo; its character is being deleted.", "Bo", "ok", "Hire ended for Bo; its character is being deleted."},
		{"Removal requested for bot Bo; Headless cleanup completes asynchronously.", "Bo", "ok", "Removal requested for bot Bo; Headless cleanup completes asynchronously."},
		{"Character 'Bo' not found.", "Bo", "ok", "already gone"},
		{"'Bo' is not a module-owned bot.", "Bo", "fail", "'Bo' is not a module-owned bot."},
		{"You may only control characters on your account.", "Bo", "fail", "You may only control characters on your account."},
		{"Bot Bo not found or not removable.", "Bo", "fail", "Bot Bo not found or not removable."},
	} {
		st, detail := tdClassifyRemove(c.line, c.name)
		if st != c.status || detail != c.detail {
			t.Fatalf("%q -> %q %q", c.line, st, detail)
		}
	}
}

// Teardown stops a running player follow first of the stop steps (before agent_run and the brain), waiting for its loop; a
// loop that does not end in time is reported "ending" and counts as failed.
func TestTeardownStopsThePlayerFollow(t *testing.T) {
	stepOf := func(res teardownResult) teardownStep {
		for _, st := range res.Steps {
			if st.Step == "follow" {
				return st
			}
		}
		t.Fatalf("no follow step: %v", tdStepNames(res))
		return teardownStep{}
	}
	f := tdBaseFake()
	f.followOn = true
	res, err := runTeardown(context.Background(), f, teardownParams{WaitMS: 4000}, func(time.Duration) {})
	if err != nil {
		t.Fatal(err)
	}
	if st := stepOf(res); st.Status != "ok" || f.followStop != 1 || f.followOn {
		t.Fatalf("follow step %+v, stops %d, still on %v", st, f.followStop, f.followOn)
	}
	if names := tdStepNames(res); names[1] != "follow" || names[2] != "agent_run" {
		t.Fatalf("follow is not the first stop step: %v", names)
	}

	f = tdBaseFake()
	res, _ = runTeardown(context.Background(), f, teardownParams{WaitMS: 4000}, func(time.Duration) {})
	if st := stepOf(res); st.Status != "skip" || f.followStop != 0 {
		t.Fatalf("no follow: step %+v, stops %d", st, f.followStop)
	}

	f = tdBaseFake()
	f.followOn, f.followHang = true, true
	res, _ = runTeardown(context.Background(), f, teardownParams{WaitMS: 4000}, func(time.Duration) {})
	if st := stepOf(res); st.Status != "ending" || res.Failed == 0 {
		t.Fatalf("hanging follow: step %+v, failed %d", st, res.Failed)
	}

	f = tdBaseFake()
	f.followOn = true
	res, _ = runTeardown(context.Background(), f, teardownParams{WaitMS: 4000, DryRun: true}, func(time.Duration) {})
	if st := stepOf(res); st.Status != "plan" || f.followStop != 0 {
		t.Fatalf("dry run: step %+v, stops %d", st, f.followStop)
	}
}

// The production teardown game stops the agent's real follow service and waits for its loop (the shared movement mode is
// free afterwards).
func TestSessionTeardownGameStopsTheFollow(t *testing.T) {
	svc, runner := sharedModeRig(t)
	if _, err := svc.Start(context.Background(), followStartParams{Plan: "followtest", Lead: "Tank"}); err != nil {
		t.Fatal(err)
	}
	g := sessionTeardownGame{cur: svc.cur, runner: runner, follow: svc}
	if !g.FollowActive() {
		t.Fatal("FollowActive false with a loop going")
	}
	if _, ended := g.StopFollow(); !ended {
		t.Fatal("StopFollow did not see the loop end")
	}
	if g.FollowActive() || svc.moves.refusal(moveOwnerRun) != nil {
		t.Fatalf("after StopFollow: active %v, mode refusal %v", g.FollowActive(), svc.moves.refusal(moveOwnerRun))
	}
}
