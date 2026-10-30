package plandiff

import (
	"encoding/json"

	"github.com/pjerra/turtle-agent/internal/planexport"
)

// Format and Version are the tb-plan-diff file's header.
const (
	Format  = "tb-plan-diff"
	Version = 1
)

// JSON renders the tb-plan-diff v1 file: the diff with its flows at two decimals (what a reader compares, not the rows'
// float32 noise).
func JSON(d Diff) ([]byte, error) {
	type jStep struct {
		Plan     string   `json:"plan"`
		Leg      int      `json:"leg"`
		Seq      int      `json:"seq"`
		Kind     string   `json:"kind"`
		Entry    uint32   `json:"entry"`
		X        float64  `json:"x"`
		Y        float64  `json:"y"`
		Z        float64  `json:"z"`
		Radius   float64  `json:"radius"`
		Item     uint32   `json:"item"`
		Param    uint32   `json:"param"`
		Flags    uint32   `json:"flags"`
		TimeoutS int      `json:"timeout_s"`
		Entries  []uint32 `json:"entries"`
		Comment  string   `json:"comment"`
	}
	type jChange struct {
		Type    string   `json:"type"`
		Leg     string   `json:"leg,omitempty"`
		OldStep int      `json:"old_step"`
		NewStep int      `json:"new_step"`
		OldSeq  int      `json:"old_seq"`
		NewSeq  int      `json:"new_seq"`
		Kind    string   `json:"kind,omitempty"`
		Old     *jStep   `json:"old,omitempty"`
		New     *jStep   `json:"new,omitempty"`
		DeltaYd float64  `json:"delta_yd"`
		Fields  []string `json:"fields"`
	}
	type jFile struct {
		Format     string         `json:"format"`
		Version    int            `json:"version"`
		Plan       string         `json:"plan"`
		Map        uint32         `json:"map"`
		OldSteps   int            `json:"old_steps"`
		NewSteps   int            `json:"new_steps"`
		Changes    []jChange      `json:"changes"`
		PlanFields []string       `json:"plan_fields"`
		Renumbered int            `json:"renumbered"`
		Notes      []string       `json:"notes"`
		Counts     map[string]int `json:"counts"`
	}
	step := func(s *planexport.StepRow) *jStep {
		if s == nil {
			return nil
		}
		es := s.Entries
		if es == nil {
			es = []uint32{}
		}
		return &jStep{
			Plan: s.Plan, Leg: s.Leg, Seq: s.Seq, Kind: s.Kind, Entry: s.Entry,
			X: round2(float64(s.X)), Y: round2(float64(s.Y)), Z: round2(float64(s.Z)),
			Radius: round2(float64(s.Radius)), Item: s.Item, Param: s.Param, Flags: s.Flags,
			TimeoutS: s.TimeoutS, Entries: es, Comment: s.Comment,
		}
	}
	out := jFile{
		Format: Format, Version: Version,
		Plan: d.Plan, Map: d.Map, OldSteps: d.OldSteps, NewSteps: d.NewSteps,
		Changes:    make([]jChange, 0, len(d.Changes)),
		PlanFields: d.PlanFields, Renumbered: d.Renumbered, Notes: d.Notes, Counts: d.Counts,
	}
	if out.PlanFields == nil {
		out.PlanFields = []string{}
	}
	if out.Notes == nil {
		out.Notes = []string{}
	}
	if out.Counts == nil {
		out.Counts = map[string]int{}
	}
	for _, c := range d.Changes {
		fields := c.Fields
		if fields == nil {
			fields = []string{}
		}
		out.Changes = append(out.Changes, jChange{
			Type: c.Type, Leg: c.Leg, OldStep: c.OldStep, NewStep: c.NewStep,
			OldSeq: c.OldSeq, NewSeq: c.NewSeq, Kind: c.Kind,
			Old: step(c.Old), New: step(c.New), DeltaYd: round2(c.DeltaYd), Fields: fields,
		})
	}
	b, err := json.MarshalIndent(out, "", "  ")
	if err != nil {
		return nil, err
	}
	return append(b, '\n'), nil
}
