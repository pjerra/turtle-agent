package planexport

import (
	"fmt"
	"strings"
	"testing"
)

// A boss's hold without instance data is the run leader's hold done by its units: entry 0, param 0, flags 0, the boss's entry first (it
// counts while still friendly), then its first and the hold's entries. A boss's hold with instance data stays an encounter hold (flags 40).
// A unit wait marked agent_only has no row and does not cut the plan; the warning quotes the reason.
func TestBossUnitsHoldAndAgentOnlyWait(t *testing.T) {
	const js = `{"version":1,"name":"zf","status":"live-checked (test)","map":209,"entrance":{"x":0,"y":0,"z":0},"legs":[
 {"name":"bly","to":{"x":10,"y":0,"z":0},
  "steps":[{"wait_for":{"unit":{"entries":[7607],"state":"gone"},"timeout_s":120,"agent_only":"the door wait covers it"}},
   {"gossip":{"npc":7604,"near":{"x":12,"y":0,"z":0},"option":0,"by":"tank"}}],
  "boss":{"entry":7604,"position":{"x":12,"y":0,"z":0},"first":[7608,7606],"hold":{"spot":{"x":10,"y":0,"z":0},"radius":25,"timeout_s":300,"entries":[7605,7608]}}},
 {"name":"keepers","to":{"x":40,"y":0,"z":0},
  "boss":{"entry":2748,"position":{"x":50,"y":0,"z":0},"hold":{"instance_data":2,"spot":{"x":50,"y":0,"z":0},"radius":20,"timeout_s":300}}}]}`
	res, err := Build(parsePlan(t, js), Options{Source: "test"})
	if err != nil {
		t.Fatal(err)
	}
	if res.Cut != "" {
		t.Fatalf("an agent_only unit wait must not cut the plan: %s", res.Cut)
	}
	var got []string
	for _, s := range res.Rows.Steps {
		got = append(got, fmt.Sprintf("%d %s %d %g r%g p%d f%d t%d %v", s.Leg, s.Kind, s.Entry, s.X, s.Radius, s.Param, s.Flags, s.TimeoutS, s.Entries))
	}
	want := "0 move 0 10 r0 p0 f0 t0 []\n" +
		"0 gossip 7604 12 r0 p1 f0 t0 []\n" +
		"0 hold 0 10 r25 p0 f0 t300 [7604 7608 7606 7605]\n" +
		"1 move 0 40 r0 p0 f0 t0 []\n" +
		"1 hold 2 50 r20 p3 f40 t300 [2748]"
	if strings.Join(got, "\n") != want {
		t.Fatalf("rows:\n%s\nwant\n%s", strings.Join(got, "\n"), want)
	}
	w := strings.Join(res.Warnings, "\n")
	for _, want := range []string{
		`wait_for agent_only (leg 0 (bly) steps[0] "the door wait covers it"): not in the rows`,
		"boss.first under a hold (leg 0 (bly) boss 7604 first [7608 7606]): not in the rows",
	} {
		if !strings.Contains(w, want) {
			t.Errorf("the warnings lack %q:\n%s", want, w)
		}
	}
	back, err := Parse(WriteSQL([]Rows{res.Rows}))
	if err != nil {
		t.Fatal(err)
	}
	if err := Equal([]Rows{res.Rows}, back); err != nil {
		t.Fatal(err)
	}
}

// Validate takes the loader's two hold forms the exporter writes: done by instance data (flag 8, the data's index in entry) and done by its
// units (no done flag, entries, no entry or param). The encounter flag without the data flag is the loader's BadParam.
func TestValidateHoldForms(t *testing.T) {
	hold := func(entry, param, flags uint32, entries []uint32) Rows {
		return Rows{Steps: []StepRow{{Plan: "p", Kind: KindHold, Entry: entry, Param: param, Flags: flags, Entries: entries}}}
	}
	for _, tc := range []struct {
		name string
		r    Rows
		ok   bool
	}{
		{"data", hold(1, 3, FlagDoneData|FlagEncounter, nil), true},
		{"data value", hold(1, 8, FlagDoneData, nil), true},
		{"units", hold(0, 0, 0, []uint32{7604, 7608}), true},
		{"data without its index", hold(0, 3, FlagDoneData, nil), false},
		{"units without entries", hold(0, 0, 0, nil), false},
		{"units with an entry", hold(1, 0, 0, []uint32{7604}), false},
		{"units with a param", hold(0, 3, 0, []uint32{7604}), false},
		{"encounter without data", hold(1, 3, FlagEncounter, []uint32{7604}), false},
	} {
		if err := Validate(tc.r); (err == nil) != tc.ok {
			t.Errorf("%s: %v", tc.name, err)
		}
	}
}
