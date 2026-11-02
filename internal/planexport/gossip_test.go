package planexport

import (
	"errors"
	"fmt"
	"slices"
	"strings"
	"testing"

	"github.com/pjerra/turtle-agent/internal/dungeon"
)

// gossipPlan is a one-leg plan with one gossip step (the fields of its JSON object) followed by the End Door wait.
func gossipPlan(t *testing.T, fields string) *dungeon.Plan {
	t.Helper()
	return parsePlan(t, `{"version":1,"name":"zf","status":"live-checked (test)","map":209,"entrance":{"x":0,"y":0,"z":0},"legs":[
 {"name":"end","to":{"x":10,"y":0,"z":0},"steps":[{"gossip":{"npc":7607,"near":{"x":12,"y":1,"z":2},`+fields+`}},
  {"wait_for":{"gameobject":{"entry":146084,"near":{"x":30,"y":0,"z":0},"state":"active"},"timeout_s":60}}]}]}`)
}

func gossipRow(t *testing.T, r Rows) StepRow {
	t.Helper()
	for _, s := range r.Steps {
		if s.Kind == KindGossip {
			return s
		}
	}
	t.Fatalf("no gossip row in %v", kinds(r, 0))
	return StepRow{}
}

// A gossip step with an option number and a bot talker is a gossip row: entry the NPC, x y z where it stands, param the option the way
// `.bot cmd gossip` counts it (from 1; the plan's option is the menu's own index from 0, so +1), no flags, timeout 0 (the module's 60 s).
func TestGossipStepIsARow(t *testing.T) {
	res, err := Build(gossipPlan(t, `"option":0,"by":"tank","stand":{"x":12,"y":4,"z":2},"retry_s":30,"fail_text":"no menu"`), Options{Source: "test"})
	if err != nil {
		t.Fatal(err)
	}
	if res.Cut != "" {
		t.Fatalf("cut: %s", res.Cut)
	}
	g := gossipRow(t, res.Rows)
	if got := fmt.Sprintf("%d %g %g %g r%g i%d p%d f%d t%d %v", g.Entry, g.X, g.Y, g.Z, g.Radius, g.Item, g.Param, g.Flags, g.TimeoutS, g.Entries); got != "7607 12 1 2 r0 i0 p1 f0 t0 []" {
		t.Errorf("row %s", got)
	}
	if !strings.Contains(g.Comment, "menu index 0 (cmd gossip option 1)") || !strings.Contains(g.Comment, "no menu") {
		t.Errorf("comment %q", g.Comment)
	}
	if got := strings.Join(kinds(res.Rows, 0), " "); got != "move gossip wait_object" {
		t.Errorf("kinds %s", got)
	}
	if w := strings.Join(res.Warnings, "\n"); !strings.Contains(w, "gossip stand/by/retry_s (leg 0 (end) steps[0]): not in the rows") {
		t.Errorf("no warning for what the row does not carry: %s", w)
	}
	// The option 1 is param 2; "carrier" talks as well; the SQL reads back.
	res, err = Build(gossipPlan(t, `"option":1,"by":"carrier"`), Options{Source: "test"})
	if err != nil {
		t.Fatal(err)
	}
	if g := gossipRow(t, res.Rows); g.Param != 2 {
		t.Errorf("param %d", g.Param)
	}
	back, err := Parse(WriteSQL([]Rows{res.Rows}))
	if err != nil || Equal([]Rows{res.Rows}, back) != nil {
		t.Fatalf("%v", err)
	}
}

// The highest row param is 15 (a menu holds 15 options), so the highest plan option is 14; 15 is an error of the plan, not a cut.
func TestGossipOptionAboveTheMenuIsRefused(t *testing.T) {
	if res, err := Build(gossipPlan(t, `"option":14,"by":"tank"`), Options{Source: "test"}); err != nil || gossipRow(t, res.Rows).Param != 15 {
		t.Fatalf("option 14: %v", err)
	}
	_, err := Build(gossipPlan(t, `"option":15,"by":"tank"`), Options{Source: "test"})
	if err == nil || errors.Is(err, ErrNoRunLeaderStep) || !strings.Contains(err.Error(), "holds at most 15") {
		t.Fatalf("%v", err)
	}
}

// A gossip the run leader's row cannot carry is a step it cannot do (the export is cut at its leg, as before): one that names its option by
// text (the row has a number), and one the agent's own character talks (a bound party has none).
func TestGossipByTextOrByAgentIsStillCut(t *testing.T) {
	for _, tc := range []struct{ fields, want string }{
		{`"text":"blow","by":"tank"`, "names its option by text"},
		{`"option":0`, `by "agent"`},
		{`"option":0,"by":"agent"`, `by "agent"`},
	} {
		_, err := Build(gossipPlan(t, tc.fields), Options{Source: "test"})
		if err == nil || !errors.Is(err, ErrNoRunLeaderStep) || !strings.Contains(err.Error(), "gossip (NPC 7607") || !strings.Contains(err.Error(), tc.want) {
			t.Errorf("%s: %v", tc.fields, err)
		}
	}
}

// Validate is the module loader's rule for a gossip row: an entry, a param of 1-15, no flag.
func TestValidateGossipRow(t *testing.T) {
	row := func(entry, param, flags uint32) Rows {
		return Rows{Steps: []StepRow{{Plan: "p", Kind: KindGossip, Entry: entry, Param: param, Flags: flags}}}
	}
	for _, tc := range []struct {
		entry, param, flags uint32
		ok                  bool
	}{{7607, 1, 0, true}, {7607, 15, 0, true}, {0, 1, 0, false}, {7607, 0, 0, false}, {7607, 16, 0, false}, {7607, 1, FlagAlliance, false}} {
		if err := Validate(row(tc.entry, tc.param, tc.flags)); (err == nil) != tc.ok {
			t.Errorf("%+v: %v", tc, err)
		}
	}
}

// "dead_is_done": true on a gossip step writes the module's kPlanFlagDeadNpcIsDone (512) on the row and nothing else changes; without it
// (or with false) the row's flags stay 0, so a dead NPC pauses the run (Weegli: the End Door cannot be opened without him).
func TestGossipDeadIsDoneIsExported(t *testing.T) {
	for _, tc := range []struct {
		fields string
		flags  uint32
	}{
		{`"option":0,"by":"tank","dead_is_done":true`, FlagDeadNpcIsDone},
		{`"option":0,"by":"tank","dead_is_done":false`, 0},
		{`"option":0,"by":"tank"`, 0},
	} {
		res, err := Build(gossipPlan(t, tc.fields), Options{Source: "test"})
		if err != nil {
			t.Fatalf("%s: %v", tc.fields, err)
		}
		g := gossipRow(t, res.Rows)
		if g.Flags != tc.flags || g.Param != 1 || g.Entry != 7607 {
			t.Errorf("%s: row %+v", tc.fields, g)
		}
		back, err := Parse(WriteSQL([]Rows{res.Rows}))
		if err != nil || Equal([]Rows{res.Rows}, back) != nil {
			t.Fatalf("%s: SQL does not read back: %v", tc.fields, err)
		}
		if err := Validate(res.Rows); err != nil {
			t.Errorf("%s: %v", tc.fields, err)
		}
	}
	if FlagDeadNpcIsDone != 512 {
		t.Errorf("the module's kPlanFlagDeadNpcIsDone is 512, not %d", FlagDeadNpcIsDone)
	}
}

// The flag is for a gossip row: Validate refuses it on every other kind (the module's loader refuses it there too), and a gossip row may
// carry it alone, no other flag.
func TestValidateDeadNpcIsDoneOnlyOnGossip(t *testing.T) {
	gossip := Rows{Steps: []StepRow{{Plan: "p", Kind: KindGossip, Entry: 7604, Param: 1, Flags: FlagDeadNpcIsDone}}}
	if err := Validate(gossip); err != nil {
		t.Fatalf("gossip with the flag: %v", err)
	}
	gossip.Steps[0].Flags = FlagDeadNpcIsDone | FlagAlliance
	if err := Validate(gossip); err == nil {
		t.Errorf("a gossip row with another flag besides it must be refused")
	}
	for _, r := range []StepRow{
		{Kind: KindMove, X: 1},
		{Kind: KindPull, Entries: []uint32{1}},
		{Kind: KindBoss, Entry: 1},
		{Kind: KindLoot, Entry: 1},
		{Kind: KindOpen, Entry: 1},
		{Kind: KindHold, Entries: []uint32{1}},
		{Kind: KindWaitObject, Entry: 1},
		{Kind: KindWaitClear, Radius: 5},
		{Kind: KindRitual, Entry: 1},
	} {
		r.Plan, r.Flags = "p", r.Flags|FlagDeadNpcIsDone
		if err := Validate(Rows{Steps: []StepRow{r}}); err == nil || !strings.Contains(err.Error(), "dead-npc-is-done") {
			t.Errorf("%s row with the flag: %v", r.Kind, err)
		}
	}
}

// In a plan file the key belongs to a gossip step only: on any other step the loader refuses an unknown field.
func TestDeadIsDoneKeyIsForGossipSteps(t *testing.T) {
	const js = `{"version":1,"name":"zf","status":"live-checked (test)","map":209,"entrance":{"x":0,"y":0,"z":0},"legs":[
 {"name":"end","to":{"x":10,"y":0,"z":0},"steps":[{"wait_for":{"gameobject":{"entry":146084,"near":{"x":30,"y":0,"z":0},"state":"active"},"timeout_s":60,"dead_is_done":true}}]}]}`
	if _, err := dungeon.Parse("test.json", []byte(js)); err == nil || !strings.Contains(err.Error(), "dead_is_done") {
		t.Errorf("a wait_for with dead_is_done: %v", err)
	}
}

// SkipLegs leaves a named leg out with a warning, and the later legs close up; a name that is no leg is an error.
func TestSkipLegsClosesUp(t *testing.T) {
	const js = `{"version":1,"name":"skip","status":"live-checked (test)","map":209,"entrance":{"x":0,"y":0,"z":0},"legs":[
 {"name":"a","to":{"x":1,"y":0,"z":0},"pull_groups":[{"entries":[1],"positions":[{"x":5,"y":0,"z":0}]}]},
 {"name":"b","to":{"x":2,"y":0,"z":0},"steps":[{"wait_for":{"unit":{"entries":[7604],"state":"seen"},"timeout_s":60}}]},
 {"name":"c","to":{"x":3,"y":0,"z":0},"pull_groups":[{"entries":[2],"positions":[{"x":6,"y":0,"z":0}]}]}]}`
	// Without it the plan is cut before b; with it, a and c are the rows (legs 0 and 1).
	if res, err := Build(parsePlan(t, js), Options{Source: "test"}); err != nil || len(res.Rows.Legs) != 1 || res.Cut == "" {
		t.Fatalf("no skip: %v %+v", err, res.Rows.Legs)
	}
	res, err := Build(parsePlan(t, js), Options{Source: "test", SkipLegs: []string{"b"}})
	if err != nil || res.Cut != "" {
		t.Fatalf("%v %s", err, res.Cut)
	}
	if len(res.Rows.Legs) != 2 || res.Rows.Legs[0].Name != "a" || res.Rows.Legs[1].Name != "c" || res.Rows.Legs[1].Leg != 1 {
		t.Errorf("legs %+v", res.Rows.Legs)
	}
	if !slices.ContainsFunc(res.Warnings, func(w string) bool {
		return strings.Contains(w, "leg 1 (b): left out of the export by the exporter's skip-legs option")
	}) {
		t.Errorf("no warning: %v", res.Warnings)
	}
	if _, err := Build(parsePlan(t, js), Options{Source: "test", SkipLegs: []string{"bly"}}); err == nil || !strings.Contains(err.Error(), `skip-legs names "bly"`) {
		t.Errorf("an unknown leg name: %v", err)
	}
}
