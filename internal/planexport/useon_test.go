package planexport

import (
	"fmt"
	"slices"
	"strings"
	"testing"

	"github.com/pjerra/turtle-agent/internal/dungeon"
)

// A use_object with via_item is the run leader's use_on row (TortoiseBots kind use_on: entry = the object, x,y,z = its near point,
// item = requires_item), after the move to its stand. Its optional leg is written by default, with a mode-1 need row for its item;
// WithoutOptional leaves it out.
func TestViaItemIsAUseOnRowOnlyWithOptional(t *testing.T) {
	const js = `{"version":1,"name":"zf","status":"live-checked (test)","map":209,"entrance":{"x":0,"y":0,"z":0},"legs":[
 {"name":"a","to":{"x":5,"y":0,"z":0},"pull_groups":[{"entries":[5649],"positions":[{"x":8,"y":0,"z":0}]}]},
 {"name":"gong","to":{"x":10,"y":0,"z":0},"optional":{"item":9240,"name":"Mallet of Zul'Farrak"},
  "steps":[{"use_object":{"entry":141832,"near":{"x":12,"y":1,"z":2},"stand":{"x":11,"y":0,"z":0},"requires_item":{"entry":9240,"name":"Mallet of Zul'Farrak"},"via_item":true}},
   {"wait_for":{"unit":{"entries":[7273],"state":"seen"},"timeout_s":30,"agent_only":"the boss row waits for him"}}],
  "boss":{"entry":7273,"position":{"x":40,"y":0,"z":0}}},
 {"name":"b","to":{"x":50,"y":0,"z":0},"pull_groups":[{"entries":[5650],"positions":[{"x":55,"y":0,"z":0}]}]}]}`
	p := parsePlan(t, js)
	res, err := Build(p, Options{Source: "test", WithoutOptional: true})
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Rows.Legs) != 2 || res.Rows.Legs[1].Name != "b" || slices.Contains(kinds(res.Rows, 1), KindUseOn) {
		t.Fatalf("with WithoutOptional the optional leg is left out: %+v", res.Rows.Legs)
	}
	res, err = Build(p, Options{Source: "test", WithOptional: []string{"gong"}})
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Rows.Legs) != 3 || res.Rows.Legs[1].Name != "gong" || res.Rows.Legs[2].Name != "b" {
		t.Fatalf("legs %+v", res.Rows.Legs)
	}
	var got []string
	for _, s := range res.Rows.Steps {
		if s.Leg == 1 {
			got = append(got, fmt.Sprintf("%s %d %g %g %g i%d p%d f%d", s.Kind, s.Entry, s.X, s.Y, s.Z, s.Item, s.Param, s.Flags))
		}
	}
	want := []string{"move 0 10 0 0 i0 p0 f0", "move 0 11 0 0 i0 p0 f0", "use_on 141832 12 1 2 i9240 p0 f0", "boss 7273 40 0 0 i0 p0 f0"}
	if !slices.Equal(got, want) {
		t.Fatalf("gong rows:\n%s\nwant\n%s", strings.Join(got, "\n"), strings.Join(want, "\n"))
	}
	w := strings.Join(res.Warnings, "\n")
	for _, want := range []string{
		"leg 1 (gong): optional, in the rows by --with-optional: give Mallet of Zul'Farrak (item 9240) to the lead bot before .bot run start (a GM .additem 9240",
		`wait_for agent_only (leg 1 (gong) steps[1] "the boss row waits for him")`,
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
	// A name that is no optional leg of the plan is refused: a misspelling, or a leg that is in the rows anyway.
	for _, bad := range []string{"gongg", "b"} {
		if _, err := Build(p, Options{Source: "test", WithOptional: []string{bad}}); err == nil || !strings.Contains(err.Error(), "with-optional") {
			t.Errorf("%s: %v", bad, err)
		}
	}
}

// Validate refuses a use_on the TortoiseBots loader would skip: no object, no item, no point, a flag or a param.
func TestValidateUseOn(t *testing.T) {
	row := func(mod func(*StepRow)) Rows {
		s := StepRow{Plan: "p", Kind: KindUseOn, Entry: 141832, X: 1650.9, Y: 1171.9, Z: 10.9, Item: 9240}
		mod(&s)
		return Rows{Steps: []StepRow{s}}
	}
	if err := Validate(row(func(*StepRow) {})); err != nil {
		t.Fatal(err)
	}
	for name, mod := range map[string]func(*StepRow){
		"no object": func(s *StepRow) { s.Entry = 0 },
		"no item":   func(s *StepRow) { s.Item = 0 },
		"no point":  func(s *StepRow) { s.X, s.Y, s.Z = 0, 0, 0 },
		"a flag":    func(s *StepRow) { s.Flags = FlagGoneIsDone },
		"a param":   func(s *StepRow) { s.Param = 1 },
	} {
		if err := Validate(row(mod)); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
}

// The shipped Zul'Farrak plan by default: the gahzrilla leg is row leg 9 (move to the stand, the 8 pulls, the stand
// move, use_on 141832 with 9240 at the gong's point, boss 7273), the courtyard follows as leg 10, and the plan still exports without a
// cut through Ukorz.
func TestZulfarrakWithGahzrilla(t *testing.T) {
	p, err := dungeon.Load("../../dungeons/zulfarrak.json")
	if err != nil {
		t.Fatal(err)
	}
	res, err := Build(p, Options{Source: "test", DropCompanionsLoot: true})
	if err != nil {
		t.Fatal(err)
	}
	if res.Cut != "" {
		t.Fatalf("cut: %s", res.Cut)
	}
	legs := res.Rows.Legs
	if len(legs) != 18 || legs[9].Name != "gahzrilla" || legs[10].Name != "courtyard" || legs[16].Name != "bly" || legs[17].Name != "ukorz" {
		t.Fatalf("legs %+v", legs)
	}
	g := kinds(res.Rows, 9)
	wantKinds := []string{KindMove, KindPull, KindPull, KindPull, KindMove, KindPull, KindMove, KindPull, KindPull, KindPull, KindPull, KindMove, KindUseOn, KindBoss}
	if !slices.Equal(g, wantKinds) {
		t.Fatalf("gahzrilla kinds %v", g)
	}
	for _, s := range res.Rows.Steps {
		if s.Leg == 9 && s.Kind == KindUseOn && (s.Entry != 141832 || s.Item != 9240 || s.X != 1650.9 || s.Y != 1171.9 || s.Z != 10.9 || s.Flags != 0 || s.Param != 0) {
			t.Errorf("the gong row %+v", s)
		}
		if s.Leg == 9 && s.Kind == KindBoss && (s.Entry != 7273 || s.X != 1702.58) {
			t.Errorf("the boss row %+v", s)
		}
	}
}
