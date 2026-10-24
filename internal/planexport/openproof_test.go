package planexport

import (
	"strings"
	"testing"

	"github.com/pjerra/turtle-agent/internal/dungeon"
)

const proofLeg = `{"version":1,"name":"Proof_Temple","status":"live-checked (test)","map":109,"entrance":{"x":0,"y":0,"z":0},"legs":[
 {"name":"statues","to":{"x":-510,"y":95,"z":-148},
  "steps":[{"use_object":{"entry":148830,"near":{"x":-515.553,"y":95.2582,"z":-148.74},"stand":{"x":-513,"y":95.3,"z":-148.74},"proof":{"gameobject":148883,"radius":10}}}],
  "boss":{"entry":8580,"position":{"x":-460,"y":96.8,"z":-189.58}}}]}}`

// A use_object with a proof is an open_proof row: the entry and near point, radius 10, entries [148883], no flags,
// param or timeout. Without a proof it stays a plain open row.
func TestUseObjectWithProofIsAnOpenProofRow(t *testing.T) {
	r := build(t, parsePlan(t, proofLeg))
	var rows []StepRow
	for _, s := range r.Steps {
		if s.Kind == KindOpen || s.Kind == KindOpenProof {
			rows = append(rows, s)
		}
	}
	if len(rows) != 1 {
		t.Fatalf("open rows %+v", rows)
	}
	got := rows[0]
	if got.Kind != KindOpenProof || got.Entry != 148830 || got.X != -515.553 || got.Radius != 10 ||
		got.Flags != 0 || got.Param != 0 || got.TimeoutS != 0 || entriesText(got.Entries) != "148883" {
		t.Fatalf("open_proof row %+v", got)
	}
	if !strings.Contains(got.Comment, "object 148883") {
		t.Fatalf("comment %q names no proof", got.Comment)
	}
	plain := build(t, parsePlan(t, small))
	for _, s := range plain.Steps {
		if s.Entry == 900 && s.Kind != KindOpen {
			t.Fatalf("a use_object without proof is no open_proof: %+v", s)
		}
	}
}

func TestUseObjectCreatureProofSetsFlag2048(t *testing.T) {
	js := `{"version":1,"name":"P","status":"live-checked (test)","map":109,"entrance":{"x":0,"y":0,"z":0},"legs":[` +
		`{"name":"s","to":{"x":-510,"y":95,"z":-148},` +
		`"steps":[{"use_object":{"entry":148830,"near":{"x":-515.553,"y":95.2582,"z":-148.74},"proof":{"creature":8580,"radius":10}}}],` +
		`"boss":{"entry":8580,"position":{"x":-460,"y":96.8,"z":-189.58}}}]}}`
	r := build(t, parsePlan(t, js))
	var got *StepRow
	for i, s := range r.Steps {
		if s.Kind == KindOpenProof {
			got = &r.Steps[i]
		}
	}
	if got == nil {
		t.Fatalf("no open_proof row in %+v", r.Steps)
	}
	if got.Flags != FlagProofCreature || entriesText(got.Entries) != "8580" {
		t.Fatalf("creature proof row %+v", got)
	}
	if FlagProofCreature != 2048 {
		t.Fatalf("FlagProofCreature is %d, not the module's free bit 2048", FlagProofCreature)
	}
	if !strings.Contains(got.Comment, "creature 8580") {
		t.Fatalf("comment %q names no creature proof", got.Comment)
	}
}

func TestValidateOpenProofRow(t *testing.T) {
	good := StepRow{Plan: "p", Leg: 0, Seq: 1, Kind: KindOpenProof, Entry: 148830, X: -515.553, Y: 95.2582, Z: -148.74,
		Radius: 10, Entries: []uint32{148883}}
	if err := Validate(Rows{Steps: []StepRow{good}}); err != nil {
		t.Fatalf("the good row is refused: %v", err)
	}
	bad := []struct {
		name string
		row  StepRow
		want string
	}{
		{"no entry", StepRow{Kind: KindOpenProof, Radius: 10, Entries: []uint32{148883}, X: 1}, "needs an entry"},
		{"no entries", StepRow{Kind: KindOpenProof, Entry: 148830, Radius: 10, X: 1}, "exactly one proof entry, not 0"},
		{"two entries", StepRow{Kind: KindOpenProof, Entry: 148830, Radius: 10, Entries: []uint32{148883, 148884}, X: 1}, "exactly one proof entry, not 2"},
		{"radius zero", StepRow{Kind: KindOpenProof, Entry: 148830, Entries: []uint32{148883}, X: 1}, "radius 0"},
		{"radius over", StepRow{Kind: KindOpenProof, Entry: 148830, Radius: 21, Entries: []uint32{148883}, X: 1}, "radius 21"},
		{"param", StepRow{Kind: KindOpenProof, Entry: 148830, Radius: 10, Param: 1, Entries: []uint32{148883}, X: 1}, "carries no param"},
		{"flags 256", StepRow{Kind: KindOpenProof, Entry: 148830, Radius: 10, Flags: 256, Entries: []uint32{148883}, X: 1}, "carries no flag but 2048"},
		{"timeout", StepRow{Kind: KindOpenProof, Entry: 148830, Radius: 10, TimeoutS: 30, Entries: []uint32{148883}, X: 1}, "carries no timeout_s"},
		{"no point", StepRow{Kind: KindOpenProof, Entry: 148830, Radius: 10, Entries: []uint32{148883}}, "needs the object's point"},
		{"proof is entry", StepRow{Kind: KindOpenProof, Entry: 148830, Radius: 10, Entries: []uint32{148830}, X: 1}, "the used object itself"},
		{"flag on open", StepRow{Kind: KindOpen, Entry: 900, Flags: FlagProofCreature, X: 1}, "is for an open_proof row"},
	}
	for _, tc := range bad {
		t.Run(tc.name, func(t *testing.T) {
			row := tc.row
			row.Plan = "p"
			if err := Validate(Rows{Steps: []StepRow{row}}); err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("error %v lacks %q", err, tc.want)
			}
		})
	}
}

var _ = dungeon.MaxProofRadius
