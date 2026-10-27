package main

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/pjerra/turtle-agent/internal/dungeon"
	"github.com/pjerra/turtle-agent/internal/rpc"
	"github.com/pjerra/turtle-agent/internal/session"
)

const followCmdPlan = `{"version":1,"name":"followtest","map":70,"entrance":{"x":0,"y":0,"z":0},"legs":[
 {"name":"a","to":{"x":10,"y":0,"z":0},"pull_groups":[{"entries":[1],"positions":[{"x":30,"y":0,"z":0}]}]}]}`

func followCmdRig(t *testing.T) (*agentRig, *followService) {
	t.Helper()
	r := dungeonRig(t)
	// dungeonRig's dir holds rig.json; the follow tests need their plan beside it.
	// Re-point a service at a dir with both plans.
	dir := t.TempDir()
	for name, doc := range map[string]string{"rig.json": rigPlan, "followtest.json": followCmdPlan} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(doc), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	svc := &followService{cur: func() *session.Session { return r.s }, dir: dir, nav: true}
	return r, svc
}

func followStartErr(t *testing.T, params string, svc *followService) error {
	t.Helper()
	var p followStartParams
	if err := json.Unmarshal([]byte(params), &p); err != nil {
		t.Fatal(err)
	}
	_, err := svc.Start(context.Background(), p)
	return err
}

func TestFollowStartParams(t *testing.T) {
	for _, c := range []struct{ params, want string }{
		{`{"lead":"Tank"}`, "plan"},
		{`{"plan":"../rig","lead":"Tank"}`, "plan must be a plan file name"},
		{`{"plan":"nosuchplan","lead":"Tank"}`, "dungeon plan:"},
		{`{"plan":"followtest"}`, "lead: name the lead bot"},
		{`{"plan":"followtest","lead":"Harnessb"}`, "cannot be the player itself"},
		{`{"plan":"followtest","lead":"Tank","near":2}`, "near must be 3-30 yd"},
		{`{"plan":"followtest","lead":"Tank","far":9}`, "far must be from near+2 to 45 yd"},
		{`{"plan":"followtest","lead":"Tank","far":46}`, "far must be from near+2 to 45 yd"},
		{`{"plan":"followtest","lead":"Tank","lost_s":5}`, "lost_s must be 10-120"},
	} {
		_, svc := followCmdRig(t)
		err := followStartErr(t, c.params, svc)
		var re *rpc.Error
		if !errors.As(err, &re) || re.Code != rpc.CodeInvalidParams || !strings.Contains(re.Message, c.want) {
			t.Errorf("%s: %v", c.params, err)
		}
	}
}

func TestFollowStartRefusedDuringDungeonRun(t *testing.T) {
	_, svc := followCmdRig(t)
	svc.moves = &moveMode{owner: moveOwnerRun}
	err := followStartErr(t, `{"plan":"followtest","lead":"Tank"}`, svc)
	var re *rpc.Error
	if !errors.As(err, &re) || re.Code != rpc.CodeGame || !strings.Contains(re.Message, "dungeon_stop it first") {
		t.Fatalf("%v", err)
	}
}

func TestDungeonRunRefusedDuringFollow(t *testing.T) {
	r := &dungeonRunner{moves: &moveMode{owner: moveOwnerFollow}}
	_, err := r.run(dungeonRunParams{})
	var re *rpc.Error
	if !errors.As(err, &re) || re.Code != rpc.CodeGame || !strings.Contains(re.Message, "follow_stop it first") {
		t.Fatalf("%v", err)
	}
}

func TestCheckFollowFlags(t *testing.T) {
	for _, c := range []struct {
		lead, plan string
		assist     bool
		want       string
	}{
		{"", "zulfarrak", true, "need --follow"},
		{"Tank", "", false, "needs --follow-plan"},
		{"Tank", "../x", false, "must be a plan file name"},
		{"Tank", "zulfarrak", false, ""},
		{"Tank", "zulfarrak", true, ""},
	} {
		err := checkFollowFlags(c.lead, c.plan, c.assist)
		if c.want == "" {
			if err != nil {
				t.Errorf("%+v: %v", c, err)
			}
		} else if err == nil || !strings.Contains(err.Error(), c.want) {
			t.Errorf("%+v: %v", c, err)
		}
	}
}

// Review round 2 (ta-follow-r2).

// sharedModeRig is a follow service and a dungeon runner on one session sharing one movement mode, with both workers replaced by
// a stand-in that blocks until its context ends (the run's driver and the follow loop): the mode is held as long as a real one.
func sharedModeRig(t *testing.T) (*followService, *dungeonRunner) {
	t.Helper()
	_, svc := followCmdRig(t)
	moves := &moveMode{}
	root, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	runner := &dungeonRunner{cur: svc.cur, dir: svc.dir, root: root, moves: moves, probeWindow: time.Hour}
	runner.testRun = func(ctx context.Context, _ dungeon.Runner, _ int) error {
		<-ctx.Done()
		return ctx.Err()
	}
	svc.runner, svc.moves, svc.root = runner, moves, context.Background()
	svc.testLoop = func(ctx context.Context, _ *dungeon.PlayerFollower) { <-ctx.Done() }
	t.Cleanup(func() {
		svc.Stop()
		runner.stop()
	})
	return svc, runner
}

func followDone(t *testing.T, svc *followService) <-chan struct{} {
	t.Helper()
	svc.mu.Lock()
	defer svc.mu.Unlock()
	if svc.done == nil {
		t.Fatal("no follow loop")
	}
	return svc.done
}

func waitClosed(t *testing.T, done <-chan struct{}) {
	t.Helper()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("the worker did not exit")
	}
}

// Item 1: follow_start and dungeon_run released at once: exactly one gets the movement mode.
func TestFollowStartAndDungeonRunRaceForOneMode(t *testing.T) {
	svc, runner := sharedModeRig(t)
	gate := make(chan struct{})
	var wg sync.WaitGroup
	var followErr, runErr error
	var runRes dungeonRunResult
	wg.Add(2)
	go func() {
		defer wg.Done()
		<-gate
		_, followErr = svc.Start(context.Background(), followStartParams{Plan: "followtest", Lead: "Tank"})
	}()
	go func() {
		defer wg.Done()
		<-gate
		runRes, runErr = runner.run(dungeonRunParams{Name: "rig"})
	}()
	close(gate)
	wg.Wait()
	followOK, runOK := followErr == nil, runErr == nil && runRes.Started
	if followOK == runOK {
		t.Fatalf("follow_start err %v, dungeon_run started %v err %v: want exactly one", followErr, runRes.Started, runErr)
	}
	t.Logf("follow got the mode: %v", followOK)
	var re *rpc.Error
	switch {
	case followOK:
		if !errors.As(runErr, &re) || !strings.Contains(re.Message, "follow_stop it first") {
			t.Fatalf("dungeon_run refusal %v", runErr)
		}
	default:
		if !errors.As(followErr, &re) || !strings.Contains(re.Message, "dungeon_stop it first") {
			t.Fatalf("follow_start refusal %v", followErr)
		}
	}
}

// Item 1: a follow_stop that gives up waiting leaves the follow "ending": dungeon_run (and follow_start) stay refused until the
// loop has exited, then dungeon_run is allowed.
func TestFollowStopTimeoutKeepsTheModeUntilTheLoopExits(t *testing.T) {
	svc, runner := sharedModeRig(t)
	release := make(chan struct{})
	svc.testLoop = func(ctx context.Context, _ *dungeon.PlayerFollower) { <-release } // deaf to the cancel
	svc.stopWait = time.Millisecond
	if _, err := svc.Start(context.Background(), followStartParams{Plan: "followtest", Lead: "Tank"}); err != nil {
		t.Fatal(err)
	}
	done := followDone(t, svc)
	if st := svc.Stop(); st.State != "ending" {
		t.Fatalf("stop state %q, want ending", st.State)
	}
	var re *rpc.Error
	if _, err := runner.run(dungeonRunParams{Name: "rig"}); !errors.As(err, &re) || !strings.Contains(re.Message, "still ending") {
		t.Fatalf("dungeon_run while the follow is ending: %v", err)
	}
	if err := followStartErr(t, `{"plan":"followtest","lead":"Tank"}`, svc); !errors.As(err, &re) || !strings.Contains(re.Message, "still ending") {
		t.Fatalf("follow_start while the follow is ending: %v", err)
	}
	if !svc.Running() {
		t.Fatal("Running false while the loop has not exited")
	}
	close(release)
	waitClosed(t, done)
	if svc.Running() {
		t.Fatal("Running true after the loop exited")
	}
	res, err := runner.run(dungeonRunParams{Name: "rig"})
	if err != nil || !res.Started {
		t.Fatalf("dungeon_run after the loop exited: %+v %v", res, err)
	}
}

// Item 2: a loop that ended by itself (here at its first tick: the rig's player is in no group) is not running: dungeon_run is
// allowed, and follow_start with the same plan and lead starts a new loop.
func TestFollowLoopThatEndedByItself(t *testing.T) {
	svc, runner := sharedModeRig(t)
	svc.testLoop = nil // the real loop
	if _, err := svc.Start(context.Background(), followStartParams{Plan: "followtest", Lead: "Tank"}); err != nil {
		t.Fatal(err)
	}
	first := svc.f
	waitClosed(t, followDone(t, svc))
	if svc.Running() {
		t.Fatal("Running true after the loop stopped by itself")
	}
	if st := svc.Status(); st.Running || !strings.Contains(st.StoppedReason, "not in a group") {
		t.Fatalf("status %+v", st)
	}
	_, err := runner.run(dungeonRunParams{})
	var re *rpc.Error
	if !errors.As(err, &re) || strings.Contains(re.Message, "follow") {
		t.Fatalf("dungeon_run refused after the follow ended by itself: %v", err)
	}
	st, err := svc.Start(context.Background(), followStartParams{Plan: "followtest", Lead: "Tank"})
	if err != nil {
		t.Fatal(err)
	}
	if !st.Running || st.StoppedReason != "" || svc.f == first {
		t.Fatalf("follow_start after the end did not start a new loop: %+v", st)
	}
	waitClosed(t, followDone(t, svc))
}

// Item 3: follow_start Tank, Heal, Tank switches back to Tank (the compare is against the live follower's lead, not the first).
func TestFollowStartSwitchesTheLeadBack(t *testing.T) {
	svc, _ := sharedModeRig(t)
	for _, lead := range []string{"Tank", "Heal", "Tank"} {
		if _, err := svc.Start(context.Background(), followStartParams{Plan: "followtest", Lead: lead}); err != nil {
			t.Fatalf("%s: %v", lead, err)
		}
	}
	if got := svc.f.WantedLead(); got != "Tank" {
		t.Fatalf("lead %q after Tank, Heal, Tank", got)
	}
}
