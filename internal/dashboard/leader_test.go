package dashboard

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"
)

func observedDungeon(state string, step int, leg, kind, phase, note, runState string) string {
	return fmt.Sprintf(`{"running":%t,"state":%q,"last_progress_at":%q,"stall_level":"none",
 "report":{"plan":"uldaman","map":70,"state":%q,"started":%q,"seconds":300,"current":"run leader: x",
  "legs":[{"leg":5,"name":"sentinel","result":"running","pulls":0}],"run_id":"r-9",
  "remote":{"mode":"observe","lead_bot":"Forthur","bots":["Puvur"],"orders_sent":0,
   "observe":{"plan":"uldaman","started":true,"state":%q,"step":%d,"steps":41,"leg":%q,"kind":%q,"phase":%q,"note":%q,"lines":9}}}}`,
		runState == "running", runState, t0.Format("2006-01-02T15:04:05Z"), runState, t0.Add(-300*1e9).Format("2006-01-02T15:04:05Z"), state, step, leg, kind, phase, note)
}

// An observed run's card carries the run leader's status in plain words.
func TestCardShowsTheRunLeader(t *testing.T) {
	for _, tc := range []struct {
		state, leg, kind, phase, note, runState, want string
		step                                          int
	}{
		{"running", "sentinel", "pull", "pull", "pulling 4851", "running", "Run leader: Sentinel · pull 7/41 · pulling", 7},
		{"running", "ancient_stone_keeper", "wait_object", "wait", "waiting", "running", "Run leader: Ancient stone keeper · wait object 9/41 · waiting", 9},
		{"paused", "sentinel", "boss", "begin", "3 dead: regroup (cmd revive), then .bot run resume", "running", "Run leader paused: Sentinel · boss 12/41 · 3 dead: regroup", 12},
		{"done", "archaedas", "boss", "pull", "plan done", "done", "Run leader: finished all 41 steps", 41},
		{"done", "sentinel", "pull", "pull", "stopped by .bot run stop", "stopped", "Run leader stopped: Sentinel · pull 7/41", 7},
		{"none", "", "", "", "no run", "failed", "Run leader: no run", 0},
	} {
		c := BuildCard(Inputs{Agent: Agent{ID: "1"}, Now: t0, Status: json.RawMessage(statusJSON), Dungeon: json.RawMessage(observedDungeon(tc.state, tc.step, tc.leg, tc.kind, tc.phase, tc.note, tc.runState))})
		l := c.Run.Leader
		if l == nil || !strings.HasPrefix(l.Text, tc.want) {
			t.Errorf("%s/%s: %+v, want %q", tc.state, tc.kind, l, tc.want)
			continue
		}
		if c.Run.Mode != "observe" {
			t.Errorf("mode %q", c.Run.Mode)
		}
		if tc.runState == "running" && c.Run.Phase != "leader" {
			t.Errorf("phase %q", c.Run.Phase)
		}
	}
}

// A run leader that left packs alive says so on the card.
func TestCardShowsPacksLeftByTheRunLeader(t *testing.T) {
	d := observedDungeon("done", 41, "archaedas", "boss", "pull", "plan done", "done")
	d = strings.Replace(d, `"lines":9}`, `"left":2,"lines":9}`, 1)
	c := BuildCard(Inputs{Agent: Agent{ID: "1"}, Now: t0, Status: json.RawMessage(statusJSON), Dungeon: json.RawMessage(d)})
	l := c.Run.Leader
	if l == nil || l.Left != 2 || l.Text != "Run leader: finished all 41 steps, 2 packs left" {
		t.Fatalf("%+v", l)
	}
	d = strings.Replace(observedDungeon("running", 7, "sentinel", "pull", "pull", "pulling 4851", "running"), `"lines":9}`, `"left":1,"lines":9}`, 1)
	c = BuildCard(Inputs{Agent: Agent{ID: "1"}, Now: t0, Status: json.RawMessage(statusJSON), Dungeon: json.RawMessage(d)})
	if l = c.Run.Leader; l == nil || l.Text != "Run leader: Sentinel · pull 7/41 · pulling, 1 pack left" {
		t.Fatalf("%+v", l)
	}
}

// A test-speed run is never a clear: the card reports it as done (test speed x).
func TestCardShowsTestSpeedRunAsDoneNotClear(t *testing.T) {
	d := observedDungeon("done", 41, "archaedas", "boss", "pull", "plan done (speed 2.0 (test))", "done")
	d = strings.Replace(d, `"lines":9}`, `"left":2,"speed":"speed 2.0 (test)","lines":9}`, 1)
	c := BuildCard(Inputs{Agent: Agent{ID: "1"}, Now: t0, Status: json.RawMessage(statusJSON), Dungeon: json.RawMessage(d)})
	l := c.Run.Leader
	if l == nil || l.Speed != "speed 2.0 (test)" || l.Left != 2 {
		t.Fatalf("%+v", l)
	}
	if l.Text != "Run leader: done (test speed speed 2.0 (test)), 2 packs left" {
		t.Fatalf("%q", l.Text)
	}
}

// A driven run's card has no run leader.
func TestCardWithoutRunLeader(t *testing.T) {
	c := BuildCard(Inputs{Agent: Agent{ID: "1"}, Now: t0, Status: json.RawMessage(statusJSON), Dungeon: json.RawMessage(dungeonJSON("leg 0 (one): walk to 1 2 3", 0))})
	if c.Run.Leader != nil || c.Run.Phase == "leader" {
		t.Fatalf("%+v", c.Run)
	}
}
