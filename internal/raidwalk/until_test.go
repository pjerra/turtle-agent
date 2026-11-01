package raidwalk

import (
	"reflect"
	"strings"
	"testing"

	"github.com/pjerra/turtle-agent/internal/planexport"
)

func TestParseUntil(t *testing.T) {
	ok := []struct {
		in    string
		args  []string
		label string
	}{
		{"boss", []string{"until", "boss"}, "until boss"},
		{"none", nil, ""},
		{"step 29", []string{"until", "step", "29"}, "until step 29"},
		{"leg gehennas", []string{"until", "leg", "gehennas"}, "until leg gehennas"},
		{"leg 2", []string{"until", "leg", "2"}, "until leg 2"},
	}
	for _, c := range ok {
		args, label, err := ParseUntil(c.in)
		if err != nil {
			t.Errorf("ParseUntil(%q): %v", c.in, err)
			continue
		}
		if !reflect.DeepEqual(args, c.args) || label != c.label {
			t.Errorf("ParseUntil(%q) = %v, %q; want %v, %q", c.in, args, label, c.args, c.label)
		}
	}
	for _, in := range []string{"5", "step", "step x", "step 0", "leg", "leg Go", "boss now", "until boss", "boss  now"} {
		if _, _, err := ParseUntil(in); err == nil {
			t.Errorf("ParseUntil(%q) accepted", in)
		}
	}
}

func TestParseFrom(t *testing.T) {
	ok := []struct {
		in   string
		args []string
	}{
		{"leg gehennas", []string{"from", "leg", "gehennas"}},
		{"leg 2", []string{"from", "leg", "2"}},
		{"step 57", []string{"from", "step", "57"}},
	}
	for _, c := range ok {
		args, err := ParseFrom(c.in)
		if err != nil {
			t.Errorf("ParseFrom(%q): %v", c.in, err)
			continue
		}
		if !reflect.DeepEqual(args, c.args) {
			t.Errorf("ParseFrom(%q) = %v, want %v", c.in, args, c.args)
		}
	}
	for _, in := range []string{"", "5", "leg", "step", "step x", "step 0", "from step 5", "leg Go"} {
		if _, err := ParseFrom(in); err == nil {
			t.Errorf("ParseFrom(%q) accepted", in)
		}
	}
}

func TestBossDoneUntil(t *testing.T) {
	step := func(kind string) planexport.StepRow { return planexport.StepRow{Kind: kind} }
	cases := []struct {
		name  string
		steps []planexport.StepRow
		want  string
		boss  int
	}{
		{"boss then more", []planexport.StepRow{step("move"), step("pull"), step(planexport.KindBoss), step("move")}, "step 4", 3},
		{"first boss counts", []planexport.StepRow{step(planexport.KindBoss), step("move"), step(planexport.KindBoss)}, "step 2", 1},
		{"boss last", []planexport.StepRow{step("move"), step(planexport.KindBoss)}, "none", 2},
	}
	for _, c := range cases {
		got, boss, err := BossDoneUntil(planexport.Rows{Steps: c.steps}, 1)
		if err != nil || got != c.want || boss != c.boss {
			t.Errorf("%s: %q, %d, %v; want %q, %d", c.name, got, boss, err, c.want, c.boss)
		}
		// the clause is one ParseUntil takes
		if _, _, err := ParseUntil(got); err != nil {
			t.Errorf("%s: ParseUntil(%q): %v", c.name, got, err)
		}
	}
	if _, _, err := BossDoneUntil(planexport.Rows{Steps: []planexport.StepRow{step("move")}}, 1); err == nil || !strings.Contains(err.Error(), "no boss step") {
		t.Errorf("a plan without a boss: %v", err)
	}
	if _, _, err := ParseUntil("boss-done"); err == nil || !strings.Contains(err.Error(), "BossDoneUntil") {
		t.Errorf("ParseUntil takes boss-done unresolved: %v", err)
	}
	if !IsBossDone(" boss-done ") || IsBossDone("boss") {
		t.Error("IsBossDone")
	}
}

// --from past a boss must not make boss-done resolve to the boss behind the start (spec 5.2): the walk would be
// "until step <the start>" and the run would end at once.
func TestBossDoneUntilFromStart(t *testing.T) {
	step := func(kind string) planexport.StepRow { return planexport.StepRow{Kind: kind} }
	var steps []planexport.StepRow
	for range 40 {
		steps = append(steps, step(planexport.KindMove))
	}
	steps[4] = step(planexport.KindBoss)  // step 5
	steps[29] = step(planexport.KindBoss) // step 30
	rows := planexport.Rows{Steps: steps}
	got, boss, err := BossDoneUntil(rows, 10)
	if err != nil || got != "step 31" || boss != 30 {
		t.Fatalf("from 10: %q, %d, %v; want \"step 31\", 30", got, boss, err)
	}
	if got, boss, err := BossDoneUntil(rows, 1); err != nil || got != "step 6" || boss != 5 {
		t.Fatalf("from 1: %q, %d, %v; want \"step 6\", 5", got, boss, err)
	}
	if _, _, err := BossDoneUntil(rows, 31); err == nil || !strings.Contains(err.Error(), "no boss step at or after step 31") {
		t.Fatalf("no boss after the start: %v", err)
	}
	last := planexport.Rows{Steps: []planexport.StepRow{step(planexport.KindMove), step(planexport.KindBoss)}}
	if got, boss, err := BossDoneUntil(last, 1); err != nil || got != "none" || boss != 2 {
		t.Fatalf("the boss is the last step: %q, %d, %v", got, boss, err)
	}
}
