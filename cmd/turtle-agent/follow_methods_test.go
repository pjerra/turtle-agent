package main

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

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
	svc.runner = &dungeonRunner{done: make(chan struct{})}
	err := followStartErr(t, `{"plan":"followtest","lead":"Tank"}`, svc)
	var re *rpc.Error
	if !errors.As(err, &re) || re.Code != rpc.CodeGame || !strings.Contains(re.Message, "dungeon_stop it first") {
		t.Fatalf("%v", err)
	}
}

func TestDungeonRunRefusedDuringFollow(t *testing.T) {
	r := &dungeonRunner{follow: stubFollow{running: true}}
	_, err := r.run(dungeonRunParams{})
	var re *rpc.Error
	if !errors.As(err, &re) || re.Code != rpc.CodeGame || !strings.Contains(re.Message, "follow_stop it first") {
		t.Fatalf("%v", err)
	}
}

type stubFollow struct{ running bool }

func (s stubFollow) Running() bool { return s.running }

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
