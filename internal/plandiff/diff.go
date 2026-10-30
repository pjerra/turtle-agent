// Package plandiff compares two exports of one dungeon or raid plan: the rows the TortoiseBots run leader reads
// (ai_dungeon_plan, ai_dungeon_leg, ai_dungeon_step), as a migration or as rows built from the plan file. It reports what a
// reader of the route sees: steps moved, changed, added, removed, renumbered, legs added or removed, and plan-row fields.
//
// The diff is pure (no clock, no files) so a command can print it as text, JSON (format tb-plan-diff v1) or as an SVG drawn
// over the map's picture.
package plandiff

import (
	"fmt"
	"io"
	"math"
	"sort"
	"strconv"
	"strings"

	"github.com/pjerra/turtle-agent/internal/planexport"
)

// The kinds of change.
const (
	TypeMoved      = "moved"
	TypeChanged    = "changed"
	TypeAdded      = "added"
	TypeRemoved    = "removed"
	TypeComment    = "comment"
	TypeLegAdded   = "leg added"
	TypeLegRemoved = "leg removed"
)

// moveYd is the smallest 3D distance that makes a paired step "moved" rather than "changed".
const moveYd = 0.05

// Change is one difference. A step change carries its global step numbers on the sides it exists (0: none) and its seq in the leg.
type Change struct {
	Type    string              `json:"type"`
	Leg     string              `json:"leg,omitempty"`
	OldStep int                 `json:"old_step,omitempty"`
	NewStep int                 `json:"new_step,omitempty"`
	OldSeq  int                 `json:"old_seq,omitempty"`
	NewSeq  int                 `json:"new_seq,omitempty"`
	Kind    string              `json:"kind,omitempty"`
	Old     *planexport.StepRow `json:"old,omitempty"`
	New     *planexport.StepRow `json:"new,omitempty"`
	DeltaYd float64             `json:"delta_yd,omitempty"`
	Fields  []string            `json:"fields,omitempty"`
}

// Diff is the comparison of two row sets of one plan.
type Diff struct {
	Plan       string         `json:"plan"`
	Map        uint32         `json:"map"`
	OldSteps   int            `json:"old_steps"`
	NewSteps   int            `json:"new_steps"`
	Changes    []Change       `json:"changes"`
	PlanFields []string       `json:"plan_fields"`
	Renumbered int            `json:"renumbered"`
	Notes      []string       `json:"notes"`
	Counts     map[string]int `json:"counts"`
}

// Changed reports whether anything differs besides a comment: the change --fail-on-change refuses.
func (d Diff) Changed() bool {
	if len(d.PlanFields) > 0 {
		return true
	}
	for _, c := range d.Changes {
		if c.Type != TypeComment {
			return true
		}
	}
	return false
}

type numbered struct {
	s      planexport.StepRow
	global int
}

func stepsByLeg(r planexport.Rows) []numbered {
	steps := append([]planexport.StepRow{}, r.Steps...)
	sort.SliceStable(steps, func(i, j int) bool {
		if steps[i].Leg != steps[j].Leg {
			return steps[i].Leg < steps[j].Leg
		}
		return steps[i].Seq < steps[j].Seq
	})
	out := make([]numbered, len(steps))
	for i, s := range steps {
		out[i] = numbered{s: s, global: i + 1}
	}
	return out
}

func inLeg(steps []numbered, leg int) []numbered {
	var out []numbered
	for _, s := range steps {
		if s.s.Leg == leg {
			out = append(out, s)
		}
	}
	return out
}

func legLabel(l planexport.LegRow) string {
	if l.Name != "" {
		return l.Name
	}
	return strconv.Itoa(l.Leg)
}

// Rows compares two row sets. The new set's plan name and map are the diff's.
func Rows(old, new planexport.Rows) Diff {
	d := Diff{
		Plan:     new.Plan.Plan,
		Map:      new.Plan.Map,
		OldSteps: len(old.Steps),
		NewSteps: len(new.Steps),
		Changes:  []Change{},
		Notes:    []string{},
		Counts:   map[string]int{},
	}
	d.PlanFields = planRowFields(old.Plan, new.Plan)
	o, n := stepsByLeg(old), stepsByLeg(new)
	for _, p := range matchLegs(old.Legs, new.Legs) {
		switch {
		case p.new < 0:
			l := old.Legs[p.old]
			d.add(Change{Type: TypeLegRemoved, Leg: legLabel(l), Kind: "leg"})
			for _, s := range inLeg(o, l.Leg) {
				d.add(removedChange(s))
			}
		case p.old < 0:
			l := new.Legs[p.new]
			d.add(Change{Type: TypeLegAdded, Leg: legLabel(l), Kind: "leg"})
			for _, s := range inLeg(n, l.Leg) {
				d.add(addedChange(s))
			}
		default:
			lold, lnew := old.Legs[p.old], new.Legs[p.new]
			if lold.Name != lnew.Name {
				d.Notes = append(d.Notes, fmt.Sprintf("leg %d name: %s -> %s", lnew.Leg, nameOrNumber(lold), nameOrNumber(lnew)))
			}
			d.diffLeg(lnew, inLeg(o, lold.Leg), inLeg(n, lnew.Leg))
		}
	}
	return d
}

func nameOrNumber(l planexport.LegRow) string {
	if l.Name == "" {
		return fmt.Sprintf("leg %d (no name)", l.Leg)
	}
	return strconv.Quote(l.Name)
}

type legPair struct{ old, new int } // -1: the leg exists on one side only

// matchLegs pairs the legs by name first (a leg inserted before another keeps the later legs together by name), then the
// remaining ones by leg number. The pairs are in the new order; removed legs follow in the old order.
func matchLegs(old, new []planexport.LegRow) []legPair {
	byName := map[string]int{}
	for i, l := range old {
		if l.Name == "" {
			continue
		}
		if _, ok := byName[l.Name]; !ok {
			byName[l.Name] = i
		}
	}
	used := make([]bool, len(old))
	newToOld := make([]int, len(new))
	for i := range newToOld {
		newToOld[i] = -1
	}
	for j, l := range new {
		if l.Name == "" {
			continue
		}
		if i, ok := byName[l.Name]; ok && !used[i] {
			used[i] = true
			newToOld[j] = i
		}
	}
	for j, l := range new {
		if newToOld[j] >= 0 {
			continue
		}
		for i, o := range old {
			if !used[i] && o.Leg == l.Leg {
				used[i] = true
				newToOld[j] = i
				break
			}
		}
	}
	out := make([]legPair, 0, len(new))
	for j, i := range newToOld {
		out = append(out, legPair{old: i, new: j})
	}
	for i := range old {
		if !used[i] {
			out = append(out, legPair{old: i, new: -1})
		}
	}
	return out
}

type anchor struct{ i, j int }

// lcsAnchors matches steps by their key (the fields a reader of the route sees, positions rounded to 0.1 yd) with a longest
// common subsequence. The pairs are key-equal and in order.
func lcsAnchors(old, new []numbered) []anchor {
	dp := make([][]int, len(old)+1)
	for i := range dp {
		dp[i] = make([]int, len(new)+1)
	}
	for i := len(old) - 1; i >= 0; i-- {
		for j := len(new) - 1; j >= 0; j-- {
			if stepKey(old[i].s) == stepKey(new[j].s) {
				dp[i][j] = dp[i+1][j+1] + 1
			} else if dp[i+1][j] >= dp[i][j+1] {
				dp[i][j] = dp[i+1][j]
			} else {
				dp[i][j] = dp[i][j+1]
			}
		}
	}
	var out []anchor
	for i, j := 0, 0; i < len(old) && j < len(new); {
		switch {
		case stepKey(old[i].s) == stepKey(new[j].s):
			out = append(out, anchor{i, j})
			i++
			j++
		case dp[i+1][j] >= dp[i][j+1]:
			i++
		default:
			j++
		}
	}
	return out
}

func (d *Diff) diffLeg(lnew planexport.LegRow, old, new []numbered) {
	leg := strconv.Itoa(lnew.Leg)
	prevI, prevJ := 0, 0
	renumbered := 0
	for _, a := range lcsAnchors(old, new) {
		d.pairRange(leg, old[prevI:a.i], new[prevJ:a.j])
		o, n := old[a.i], new[a.j]
		if o.s.Comment != n.s.Comment {
			oc, nc := o.s, n.s
			d.add(Change{
				Type: TypeComment, Leg: leg, OldStep: o.global, NewStep: n.global,
				OldSeq: o.s.Seq, NewSeq: n.s.Seq, Kind: o.s.Kind, Old: &oc, New: &nc, Fields: []string{"comment"},
			})
		}
		if o.global != n.global {
			d.Renumbered++
			if renumbered < 5 {
				d.Notes = append(d.Notes, fmt.Sprintf("renumbered step %d -> %d (leg %d seq %d)", o.global, n.global, lnew.Leg, n.s.Seq))
			}
			renumbered++
		}
		prevI, prevJ = a.i+1, a.j+1
	}
	d.pairRange(leg, old[prevI:], new[prevJ:])
}

// pairRange pairs the unmatched rows between two anchors in order while their kinds match; the rest are removed and added.
func (d *Diff) pairRange(leg string, old, new []numbered) {
	i, j := 0, 0
	for i < len(old) && j < len(new) && old[i].s.Kind == new[j].s.Kind {
		d.add(pairChange(leg, old[i], new[j]))
		i++
		j++
	}
	for ; i < len(old); i++ {
		d.add(removedChange(old[i]))
	}
	for ; j < len(new); j++ {
		d.add(addedChange(new[j]))
	}
}

func pairChange(leg string, o, n numbered) Change {
	oc, nc := o.s, n.s
	c := Change{Leg: leg, OldStep: o.global, NewStep: n.global, OldSeq: o.s.Seq, NewSeq: n.s.Seq, Kind: o.s.Kind, Old: &oc, New: &nc}
	c.DeltaYd = round2(dist3(o.s, n.s))
	if c.DeltaYd > moveYd {
		c.Type = TypeMoved
		c.Fields = differingFields(o.s, n.s, false)
	} else {
		c.Type = TypeChanged
		c.Fields = differingFields(o.s, n.s, true)
	}
	return c
}

func addedChange(s numbered) Change {
	sc := s.s
	return Change{Type: TypeAdded, Leg: strconv.Itoa(s.s.Leg), NewStep: s.global, NewSeq: s.s.Seq, Kind: s.s.Kind, New: &sc}
}

func removedChange(s numbered) Change {
	sc := s.s
	return Change{Type: TypeRemoved, Leg: strconv.Itoa(s.s.Leg), OldStep: s.global, OldSeq: s.s.Seq, Kind: s.s.Kind, Old: &sc}
}

func (d *Diff) add(c Change) {
	if c.Fields == nil {
		c.Fields = []string{}
	}
	d.Changes = append(d.Changes, c)
	d.Counts[c.Type]++
}

func dist3(a, b planexport.StepRow) float64 {
	dx := float64(a.X - b.X)
	dy := float64(a.Y - b.Y)
	dz := float64(a.Z - b.Z)
	return math.Sqrt(dx*dx + dy*dy + dz*dz)
}

func round2(v float64) float64 { return math.Round(v*100) / 100 }

// stepKey is what makes two steps the same stop of the route: the position rounded to 0.1 yd, so the export's float32 noise
// does not count, and the fields the run leader acts on.
func stepKey(s planexport.StepRow) string {
	return strings.Join([]string{
		s.Kind,
		strconv.FormatUint(uint64(s.Entry), 10),
		entriesKey(s.Entries),
		strconv.FormatFloat(float64(s.Radius), 'f', -1, 32),
		strconv.FormatUint(uint64(s.Item), 10),
		strconv.FormatUint(uint64(s.Param), 10),
		strconv.FormatUint(uint64(s.Flags), 10),
		strconv.Itoa(s.TimeoutS),
		tenth(s.X), tenth(s.Y), tenth(s.Z),
	}, "|")
}

func entriesKey(es []uint32) string {
	parts := make([]string, len(es))
	for i, e := range es {
		parts[i] = strconv.FormatUint(uint64(e), 10)
	}
	return strings.Join(parts, ",")
}

func tenth(f float32) string {
	return strconv.FormatFloat(math.Round(float64(f)*10)/10, 'f', -1, 64)
}

// differingFields names the fields that differ, in a fixed order: the position axes (when withPosition) and the comment last.
func differingFields(a, b planexport.StepRow, withPosition bool) []string {
	var f []string
	if a.Entry != b.Entry {
		f = append(f, "entry")
	}
	if entriesKey(a.Entries) != entriesKey(b.Entries) {
		f = append(f, "entries")
	}
	if a.Radius != b.Radius {
		f = append(f, "radius")
	}
	if a.Item != b.Item {
		f = append(f, "item")
	}
	if a.Param != b.Param {
		f = append(f, "param")
	}
	if a.Flags != b.Flags {
		f = append(f, "flags")
	}
	if a.TimeoutS != b.TimeoutS {
		f = append(f, "timeout_s")
	}
	if withPosition {
		if tenth(a.X) != tenth(b.X) {
			f = append(f, "x")
		}
		if tenth(a.Y) != tenth(b.Y) {
			f = append(f, "y")
		}
		if tenth(a.Z) != tenth(b.Z) {
			f = append(f, "z")
		}
	}
	if a.Comment != b.Comment {
		f = append(f, "comment")
	}
	if f == nil {
		return []string{}
	}
	return f
}

func planRowFields(a, b planexport.PlanRow) []string {
	var f []string
	for _, p := range []struct {
		name string
		same bool
	}{
		{"cc_priority", entriesKey(a.CCPriority) == entriesKey(b.CCPriority)},
		{"cc_always", entriesKey(a.CCAlways) == entriesKey(b.CCAlways)},
		{"kill_first", entriesKey(a.KillFirst) == entriesKey(b.KillFirst)},
		{"source", a.Source == b.Source},
		{"comment", a.Comment == b.Comment},
	} {
		if !p.same {
			f = append(f, p.name)
		}
	}
	if f == nil {
		return []string{}
	}
	return f
}

// Text writes one line per change, the plan's summary line, then the notes.
func (d Diff) Text(w io.Writer) {
	for _, c := range d.Changes {
		fmt.Fprintln(w, changeText(c))
	}
	plan := "none"
	if len(d.PlanFields) > 0 {
		plan = strings.Join(d.PlanFields, ", ")
	}
	fmt.Fprintf(w, "%s: %d -> %d steps: %d moved, %d changed, %d added, %d removed, %d comment; %d renumbered; plan: %s\n",
		d.Plan, d.OldSteps, d.NewSteps,
		d.Counts[TypeMoved], d.Counts[TypeChanged], d.Counts[TypeAdded], d.Counts[TypeRemoved], d.Counts[TypeComment],
		d.Renumbered, plan)
	for _, n := range d.Notes {
		fmt.Fprintf(w, "note: %s\n", n)
	}
}

func changeText(c Change) string {
	switch c.Type {
	case TypeLegAdded:
		return fmt.Sprintf("leg %s: leg added", c.Leg)
	case TypeLegRemoved:
		return fmt.Sprintf("leg %s: leg removed", c.Leg)
	}
	if c.Old != nil && c.New != nil {
		head := fmt.Sprintf("step %d (leg %s seq %d) %s: ", c.NewStep, c.Leg, c.NewSeq, c.Kind)
		var b strings.Builder
		switch c.Type {
		case TypeMoved:
			fmt.Fprintf(&b, "moved %.2f yd (%.2f %.2f %.2f -> %.2f %.2f %.2f)", c.DeltaYd,
				c.Old.X, c.Old.Y, c.Old.Z, c.New.X, c.New.Y, c.New.Z)
		case TypeChanged:
			b.WriteString("changed")
			if len(c.Fields) > 0 {
				b.WriteString(" ")
				b.WriteString(strings.Join(fieldText(c), ", "))
			}
		case TypeComment:
			b.WriteString(commentText(c.Old.Comment, c.New.Comment))
		default:
			b.WriteString(c.Type)
		}
		if c.Type == TypeMoved && len(c.Fields) > 0 {
			b.WriteString("; " + strings.Join(fieldText(c), ", "))
		}
		return head + b.String()
	}
	if c.Old != nil {
		line := fmt.Sprintf("step %d (leg %s seq %d) %s: removed", c.OldStep, c.Leg, c.OldSeq, c.Kind)
		if c.Old.Comment != "" {
			line += fmt.Sprintf("; comment %s", quoteComment(c.Old.Comment))
		}
		return line
	}
	s := c.New
	line := fmt.Sprintf("step %d (leg %s seq %d) %s: added", c.NewStep, c.Leg, c.NewSeq, c.Kind)
	if s != nil && s.Comment != "" {
		line += fmt.Sprintf("; comment %s", quoteComment(s.Comment))
	}
	return line
}

func commentText(old, new string) string {
	return fmt.Sprintf("comment %s -> %s", quoteComment(old), quoteComment(new))
}

// quoteComment quotes a comment the way the rows' SQL does, with single quotes.
func quoteComment(s string) string { return "'" + strings.ReplaceAll(s, "'", `\'`) + "'" }

// fieldText renders each differing field as "name old -> new", the comment through commentText.
func fieldText(c Change) []string {
	out := make([]string, 0, len(c.Fields))
	for _, f := range c.Fields {
		switch f {
		case "comment":
			if c.Old != nil && c.New != nil {
				out = append(out, commentText(c.Old.Comment, c.New.Comment))
			}
		case "x", "y", "z":
			if c.Old != nil && c.New != nil {
				out = append(out, fmt.Sprintf("%s %.2f -> %.2f", f, axis(c.Old, f), axis(c.New, f)))
			}
		case "radius", "item", "param", "flags", "timeout_s":
			if c.Old != nil && c.New != nil {
				out = append(out, fmt.Sprintf("%s %s -> %s", f, value(c.Old, f), value(c.New, f)))
			}
		case "entry":
			if c.Old != nil && c.New != nil {
				out = append(out, fmt.Sprintf("entry %d -> %d", c.Old.Entry, c.New.Entry))
			}
		case "entries":
			if c.Old != nil && c.New != nil {
				out = append(out, fmt.Sprintf("entries [%s] -> [%s]", entriesKey(c.Old.Entries), entriesKey(c.New.Entries)))
			}
		default:
			out = append(out, f)
		}
	}
	return out
}

func axis(s *planexport.StepRow, f string) float32 {
	switch f {
	case "x":
		return s.X
	case "y":
		return s.Y
	}
	return s.Z
}

func value(s *planexport.StepRow, f string) string {
	switch f {
	case "radius":
		return strconv.FormatFloat(float64(s.Radius), 'f', -1, 32)
	case "item":
		return strconv.FormatUint(uint64(s.Item), 10)
	case "param":
		return strconv.FormatUint(uint64(s.Param), 10)
	case "flags":
		return strconv.FormatUint(uint64(s.Flags), 10)
	}
	return strconv.Itoa(s.TimeoutS)
}
