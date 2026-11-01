package raidwalk

import (
	"os"
	"strings"
	"testing"
	"time"
)

// TestFightDetailOfReadsTheRecap reads a saved dashboard fight detail (Onyxia walk, step 22: two deaths to the
// Onyxian Warders) into the summary digest.
func TestFightDetailOfReadsTheRecap(t *testing.T) {
	raw, err := os.ReadFile("../../cmd/turtle-agent/testdata/fights/combat-fight-5-4.json")
	if err != nil {
		t.Fatal(err)
	}
	d, ok := FightDetailOf(raw)
	if !ok {
		t.Fatal("not read")
	}
	if d.Kind != "move" || len(d.DeathRecap) != 2 || d.DeathRecap[0].Name != "Irelanthe" || d.DeathRecap[0].Killer != "Onyxian Warder" || d.DeathRecap[0].LastHit != 1369 {
		t.Fatalf("%+v", d)
	}
	if d.DeathRecap[0].Burst != 92+1270+1085+2606+1369 {
		t.Errorf("burst %d", d.DeathRecap[0].Burst)
	}
	if d.TopTaken == "" || d.TopTakenAmt == 0 || d.Damage == 0 {
		t.Errorf("bots: %+v", d)
	}
	// coe 24 of 42 sampled seconds
	if d.DebuffPct["coe"] != 57.1 || d.DebuffPct["faerie_fire"] != 61.9 {
		t.Errorf("debuffs %v", d.DebuffPct)
	}
}

func TestFightDetailOfIsTolerant(t *testing.T) {
	for _, raw := range []string{"", "[]", `{"error":"no such fight"}`, `{"fight":null}`, "not json"} {
		if _, ok := FightDetailOf([]byte(raw)); ok {
			t.Errorf("%q read as a fight", raw)
		}
	}
	// the walk test's stub detail, and keys a newer daemon adds: what is there is read, the rest stays zero
	d, ok := FightDetailOf([]byte(`{"fight":{"id":"4-5","new_key":{"x":1},"deaths":[{"name":"A","hits":[{"a":"Razorgore","d":900}]}]},"warnings":[{"level":"red","bot":"B","text":"no heals"},{"level":"yellow","bot":"C","text":"x"}],"hist":true}`))
	if !ok || len(d.DeathRecap) != 1 || d.DeathRecap[0].Killer != "Razorgore" || d.DeathRecap[0].LastHit != 900 || d.Warnings != 2 || len(d.Red) != 1 || d.Red[0] != "B: no heals" || d.DebuffPct != nil {
		t.Fatalf("%+v %v", d, ok)
	}
}

func TestSummaryTextListsDeadlyFights(t *testing.T) {
	sum := BuildSummary(nil, SummaryMeta{Plan: "zul_gurub", Started: time.Unix(0, 0), Ended: time.Unix(60, 0),
		RunLabel: "speed 2.0 (test; 1.0 since a real player joined); until boss (step 41)", SpeedFellBack: true, Speed: 2, TestSpeed: true,
		Fights: []FightSummary{
			{ID: "1", Step: 20, Leg: "high_priestess_jeklik", DurMS: 54300, Deaths: 2, Outcome: "wipe",
				Detail: &FightDetail{Kind: "pull", DeathRecap: []FightDeath{{Name: "Tumble", Killer: "Gurubashi Berserker", LastHit: 2104}, {Name: "Belia"}}, Red: []string{"Belia: no heals"}}},
			{ID: "2", Step: 21, DurMS: 9000, Outcome: "win", Detail: &FightDetail{Kind: "pull"}},
			{ID: "3", Step: 22, DurMS: 9000, Outcome: "win"},
		}})
	text := sum.Text()
	for _, want := range []string{
		"speed 2.0 (test); the server went back to 1.0 once a real player joined\n",
		"server label: speed 2.0 (test; 1.0 since a real player joined); until boss (step 41)\n",
		"fights with deaths or red warnings:\n  step 20 high_priestess_jeklik (pull, 54.3 s, wipe): 2 deaths: Tumble by Gurubashi Berserker (last hit 2104), Belia; red: Belia: no heals\n",
	} {
		if !strings.Contains(text, want) {
			t.Errorf("no %q in:\n%s", want, text)
		}
	}
	if strings.Contains(text, "step 21") || strings.Contains(text, "step 22") {
		t.Errorf("a quiet fight is listed:\n%s", text)
	}
}
