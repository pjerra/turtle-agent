package planexport

import (
	"fmt"
	"strings"
	"testing"
)

// A wait_for that carries a hold is exported as that hold row (the wait itself, a unit wait, has no run leader step), and a hold with a
// value is done by that instance data value read as a script value: flag 8 alone (no encounter flag 32), param = the value. A pull group's
// hold (no value: the plan refuses one there) keeps the encounter DONE (param 3, flags 40). Zul'Farrak's pyramid phase (instance data 1) is the case.
func TestWaitHoldIsTheRowAndValueIsAScriptValue(t *testing.T) {
	const js = `{"version":1,"name":"zf","status":"live-checked (test)","map":209,"entrance":{"x":0,"y":0,"z":0},"legs":[
 {"name":"cage","to":{"x":10,"y":0,"z":0},
  "steps":[{"use_object":{"entry":141073,"near":{"x":12,"y":0,"z":0},"requires_item":{"entry":8444,"name":"Key"}}},
   {"wait_for":{"unit":{"entries":[7607],"near":{"x":20,"y":0,"z":0},"radius":15,"state":"seen"},"timeout_s":60,
    "hold":{"instance_data":1,"value":3,"spot":{"x":20,"y":0,"z":0},"radius":30,"timeout_s":90,"entries":[7787,7788,7787]}}}]},
 {"name":"top","to":{"x":20,"y":0,"z":0},
  "steps":[{"wait_for":{"unit":{"entries":[7796],"state":"seen"},"timeout_s":600,
   "hold":{"instance_data":1,"value":7,"spot":{"x":20,"y":0,"z":0},"radius":30,"timeout_s":600}}}]},
 {"name":"group","to":{"x":40,"y":0,"z":0},
  "pull_groups":[{"entries":[4858],"positions":[{"x":50,"y":0,"z":0}],"hold":{"instance_data":2,"spot":{"x":50,"y":0,"z":0},"radius":20,"timeout_s":300}}]}]}`
	res, err := Build(parsePlan(t, js), Options{Source: "test"})
	if err != nil {
		t.Fatal(err)
	}
	if res.Cut != "" {
		t.Fatalf("a unit wait with a hold must not cut the plan: %s", res.Cut)
	}
	r := res.Rows
	if got := strings.Join(kinds(r, 0), " "); got != "move open hold" {
		t.Fatalf("leg 0: %s", got)
	}
	// The move to the hold's own spot (leg 1's to) is left out: the hold walks there.
	if got := strings.Join(kinds(r, 1), " "); got != "hold" {
		t.Fatalf("leg 1: %s", got)
	}
	row := func(leg, seq int) string {
		for _, s := range r.Steps {
			if s.Leg == leg && s.Seq == seq {
				return fmt.Sprintf("%s %d %g %g %g r%g p%d f%d t%d %v", s.Kind, s.Entry, s.X, s.Y, s.Z, s.Radius, s.Param, s.Flags, s.TimeoutS, s.Entries)
			}
		}
		return "none"
	}
	for _, tc := range []struct {
		leg, seq int
		want     string
	}{
		{0, 2, "hold 1 20 0 0 r30 p3 f8 t90 [7787 7788]"},
		{1, 0, "hold 1 20 0 0 r30 p7 f8 t600 []"},
		{2, 1, "hold 2 50 0 0 r20 p3 f40 t300 [4858]"},
	} {
		if got := row(tc.leg, tc.seq); got != tc.want {
			t.Errorf("leg %d step %d: %s, want %s", tc.leg, tc.seq, got, tc.want)
		}
	}
	for _, s := range r.Steps {
		if s.Kind == KindHold && s.Flags == FlagDoneData && !strings.Contains(s.Comment, "(a script value, not an encounter state)") {
			t.Errorf("a value hold's comment does not say so: %q", s.Comment)
		}
	}
	if w := strings.Join(res.Warnings, "\n"); !strings.Contains(w, "wait_for with a hold (leg 0 (cage) steps[1]; leg 1 (top) steps[0]): not in the rows") {
		t.Errorf("no warning that the waits' own conditions are the agent's only: %s", w)
	}
	back, err := Parse(WriteSQL([]Rows{r}))
	if err != nil {
		t.Fatal(err)
	}
	if err := Equal([]Rows{r}, back); err != nil {
		t.Fatal(err)
	}
}

// Validate takes a hold's param above 4 only as a script value (flag 8 without 32), the module loader's rule (NormalizePlanStep): an
// encounter state is 0-4.
func TestValidateHoldParamByFlags(t *testing.T) {
	hold := func(param, flags uint32) Rows {
		return Rows{Steps: []StepRow{{Plan: "p", Kind: KindHold, Entry: 1, Param: param, Flags: flags}}}
	}
	if err := Validate(hold(7, FlagDoneData)); err != nil {
		t.Errorf("a script value 7: %v", err)
	}
	if err := Validate(hold(7, FlagDoneData|FlagEncounter)); err == nil || !strings.Contains(err.Error(), "no encounter state") {
		t.Errorf("an encounter state 7 must be refused: %v", err)
	}
	if err := Validate(hold(3, FlagDoneData|FlagEncounter)); err != nil {
		t.Errorf("DONE: %v", err)
	}
}

// The shipped Zul'Farrak plan's stairs event in the rows: the Executioner, his key, the cage with the key, then the two holds on the stairs
// top (pyramid phase 3, wave 1 under way; phase 7, wave 2 dead and wave 3 up), then Nekrum and Sezz'ziz at the foot and the wait until the
// foot is clear. Instance data 1 is the pyramid phase (tortoise-wow zulfarrak.h EVENT_PYRAMID, instance_zulfarrak.cpp GetData).
func TestZulfarrakStairsEventRows(t *testing.T) {
	res, err := Build(zulfarrakThrough(t, "stairs_base"), Options{Source: "test", DropCompanionsLoot: true})
	if err != nil {
		t.Fatal(err)
	}
	leg := map[string]int{}
	for _, l := range res.Rows.Legs {
		leg[l.Name] = l.Leg
	}
	var got []string
	for _, s := range res.Rows.Steps {
		if s.Leg < leg["stairs"] || s.Kind == KindMove && (s.Comment == "path" || s.Comment == "via") {
			continue
		}
		got = append(got, fmt.Sprintf("%d %s %d %g %g %g i%d p%d f%d t%d %v", s.Leg-leg["stairs"], s.Kind, s.Entry, s.X, s.Y, s.Z, s.Item, s.Param, s.Flags, s.TimeoutS, s.Entries))
	}
	want := []string{
		"0 move 0 1887 1274 42 i0 p0 f0 t0 []",
		"0 boss 7274 1886.8 1286.8 44.2 i0 p0 f0 t0 []",
		"0 loot 7274 1886.8 1286.8 44.2 i8444 p0 f0 t0 []",
		"0 move 0 1886.9 1295.4 48.3 i0 p0 f0 t0 []",
		"0 open 141073 1886.9 1298.9 48.2 i8444 p0 f0 t0 []",
		"0 hold 1 1887 1274 42 i0 p3 f8 t90 [7787 7788 7789 8876 8877]",
		"1 hold 1 1887 1274 42 i0 p7 f8 t600 [7787 7788 7789 8876 8877]",
		"2 move 0 1885 1232 12.6 i0 p0 f0 t0 []",
		"2 boss 7275 1889.23 1207.72 8.87 i0 p0 f0 t0 [7796]",
		"2 wait_clear 0 1886 1215 9 i0 p0 f0 t180 []",
	}
	if strings.Join(got, "\n") != strings.Join(want, "\n") {
		t.Fatalf("stairs rows:\n%s\nwant:\n%s", strings.Join(got, "\n"), strings.Join(want, "\n"))
	}
}
