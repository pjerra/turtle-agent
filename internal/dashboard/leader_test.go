package dashboard

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"github.com/pjerra/turtle-agent/internal/dungeon"
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

// A test-speed or until run says so on the card, from the status line's label.
func TestCardShowsTheRunLabel(t *testing.T) {
	d := strings.Replace(observedDungeon("running", 7, "sentinel", "pull", "pull", "pulling 4851", "running"), `"lines":9}`, `"label":"speed 2.0 (test); until boss (step 38)","lines":9}`, 1)
	c := BuildCard(Inputs{Agent: Agent{ID: "1"}, Now: t0, Status: json.RawMessage(statusJSON), Dungeon: json.RawMessage(d)})
	if l := c.Run.Leader; l == nil || l.Label != "speed 2.0 (test); until boss (step 38)" || l.Text != "Run leader: Sentinel · pull 7/41 · pulling (speed 2.0 (test); until boss (step 38))" {
		t.Fatalf("%+v", l)
	}
}

// A driven run's card has no run leader.
func TestCardWithoutRunLeader(t *testing.T) {
	c := BuildCard(Inputs{Agent: Agent{ID: "1"}, Now: t0, Status: json.RawMessage(statusJSON), Dungeon: json.RawMessage(dungeonJSON("leg 0 (one): walk to 1 2 3", 0))})
	if c.Run.Leader != nil || c.Run.Phase == "leader" {
		t.Fatalf("%+v", c.Run)
	}
}

// A raid run's card shows the ready block as subgroup-less rows (name, class, role, level, item level),
// the plan's raid size, and the dashboard link when the slot carries tb_dashboard_url.
func TestCardShowsARaidRun(t *testing.T) {
	raid := func() *dungeon.Plan {
		p, err := dungeon.Parse("lkh.json", []byte(`{"version":1,"name":"lower_karazhan_halls","kind":"raid","status":"live-checked","map":532,
 "entrance":{"x":0,"y":0,"z":0},"raid":{"size":10,"tanks":2,"healers":3,"dps":5},"legs":[{"to":{"x":0,"y":0,"z":0},
 "pull_groups":[{"entries":[1],"positions":[{"x":0,"y":0,"z":0}]}]}]}`))
		if err != nil {
			t.Fatal(err)
		}
		return p
	}
	dungeonBody := `{"running":true,"state":"running","last_progress_at":"2026-10-14T16:40:00Z","stall_level":"none",
 "report":{"plan":"lower_karazhan_halls","map":532,"state":"running","started":"2026-10-14T16:35:00Z","seconds":300,"current":"run leader: x",
  "legs":[{"leg":0,"name":"a","result":"running","pulls":0}],"run_id":"r-9",
  "remote":{"mode":"observe","lead_bot":"Rtank","bots":["Rheala","Rdpsa"],"tank":"Rtank",
   "observe":{"started":true,"state":"running","step":1,"steps":41,"leg":"a","kind":"pull","phase":"pull","note":"pulling","lines":9}},
  "ready":{"bots":[{"name":"Rtank","class":"warrior","role":"tank","level":60,"ilvl":63.5},{"name":"Rheala","class":"priest","role":"healer","level":60},{"name":"Rdpsa","class":"mage","role":"dps","level":60,"ilvl":61}]}}}`
	in := Inputs{Agent: Agent{ID: "1", TBDashboardURL: "http://tb:8095"}, Now: t0,
		Status: json.RawMessage(statusJSON), Dungeon: json.RawMessage(dungeonBody), Plan: raid()}
	c := BuildCard(in)
	if c.Run.Raid != 10 {
		t.Fatalf("raid size: %+v", c.Run)
	}
	if c.Run.RaidView != "http://tb:8095/#commanders" {
		t.Fatalf("raid view: %+v", c.Run)
	}
	if len(c.Bots) != 3 || c.Bots[0].Name != "Rtank" || !c.Bots[0].Lead || c.Bots[0].Level != 60 || c.Bots[0].ItemLevel != 63.5 {
		t.Fatalf("bots: %+v", c.Bots)
	}
	if c.Bots[1].Role != "healer" || c.Bots[1].ItemLevel != 0 || c.Bots[2].Class != "mage" || c.Bots[2].Level != 60 {
		t.Fatalf("bots: %+v", c.Bots)
	}
	// Without the settings key there is no link, and without a raid plan there is no raid size.
	in.Agent.TBDashboardURL = ""
	if c := BuildCard(in); c.Run.RaidView != "" || c.Run.Raid != 10 {
		t.Fatalf("link without the key: %+v", c.Run)
	}
	in.Plan = nil
	if c := BuildCard(in); c.Run.Raid != 0 {
		t.Fatalf("raid without a raid plan: %+v", c.Run)
	}
}
