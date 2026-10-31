package trashcheck

import (
	"fmt"
	"strings"
)

// Finding is one line of the report's text form, with the class it belongs to.
type Finding struct {
	Class string // danger or patrol
	Text  string
}

// Findings lists the report's findings, one per danger pack and per patrol hit, in segment order. An own pull is not a
// finding; a patrol hit on an own segment is not either (the plan pulls it there on purpose).
func (r *Report) Findings() []Finding {
	var out []Finding
	for _, s := range r.Segments {
		for _, p := range s.Packs {
			if p.Class != ClassDanger {
				continue
			}
			out = append(out, Finding{Class: VerdictDanger, Text: dangerLine(s, p)})
		}
		for _, h := range s.Patrols {
			if h.own {
				continue
			}
			out = append(out, Finding{Class: VerdictPatrol, Text: patrolLine(s, h)})
		}
	}
	return out
}

// dangerLine is one body pull: the step, the nearest waker and the reach it woke at, and when (if ever) the plan clears it.
func dangerLine(s Segment, p SegmentPack) string {
	name, reach, min := "the pack", float32(0), float32(0)
	if len(p.Wakers) > 0 {
		w := p.Wakers[0]
		name, reach, min = w.Name, w.AggroYd, w.MinYd
	}
	target := "unplanned"
	if p.TargetedAt != nil {
		target = fmt.Sprintf("targeted at step %d", *p.TargetedAt)
	}
	return fmt.Sprintf("step %d->%d (%s '%s') DANGER: body pull: step %d walks within %.1f yd of %s (aggro %.1f, pack of %d, %s)",
		s.From.Step, s.To.Step, s.To.Kind, s.To.Comment, s.To.Step, min, name, reach, p.Brings, target)
}

// patrolLine is one patrol crossing: its id, name and windows.
func patrolLine(s Segment, h PatrolHit) string {
	parts := make([]string, 0, len(h.Windows))
	for _, w := range h.Windows {
		parts = append(parts, fmt.Sprintf("%.1f-%.1f s", w.FromS, w.ToS))
	}
	return fmt.Sprintf("step %d->%d (%s '%s') PATROL: patrol %s (%s): %.0f%% of %.1f s in %s, at %s",
		s.From.Step, s.To.Step, s.To.Kind, s.To.Comment, h.ID, h.Name, h.Share*100, h.PeriodS,
		strings.Join(parts, ", "), h.At)
}

// Finds says a finding of the class exists: danger, patrol or any (either). It reads the findings, not the segment verdicts:
// a segment that is a danger as a whole may still hold a patrol finding, and --fail-on counts that finding.
func (r *Report) Finds(class string) bool {
	for _, f := range r.Findings() {
		if class == "any" || f.Class == class {
			return true
		}
	}
	return false
}

// Text renders the report: one line per finding, then the counts line, then the notes.
func (r *Report) Text() string {
	var b strings.Builder
	for _, f := range r.Findings() {
		b.WriteString(f.Text)
		b.WriteByte('\n')
	}
	fmt.Fprintf(&b, "%s: %d steps, %d segments: %d clear, %d own, %d patrol, %d danger\n",
		r.Plan, r.Steps, len(r.Segments), r.Counts.Clear, r.Counts.Own, r.Counts.Patrol, r.Counts.Danger)
	for _, note := range r.Notes {
		fmt.Fprintf(&b, "note: %s\n", note)
	}
	return b.String()
}

// DashPatrol is one patrol in the dashboard data file's shape (the phase-A backlog's `patrols.json`, a that mirrors the
// plan layer's `wake.json` map): keyed by the patrol's id, the leader's spawn guid as a string.
type DashPatrol struct {
	Entries  []uint32     `json:"entries"`
	Path     [][3]float32 `json:"path"`
	PeriodS  float32      `json:"period_s"`
	Estimate bool         `json:"estimate"`
	Why      string       `json:"why"`
	Src      string       `json:"src"`
}

// DashOut renders the report's patrols as the dashboard data file: a JSON object keyed by patrol id, src naming the run.
func (r *Report) DashOut(src string) map[string]DashPatrol {
	out := make(map[string]DashPatrol, len(r.Patrols))
	for _, p := range r.Patrols {
		path := make([][3]float32, len(p.Path))
		for i, pt := range p.Path {
			path[i] = [3]float32{pt.X, pt.Y, pt.Z}
		}
		out[p.ID] = DashPatrol{
			Entries:  p.Entries,
			Path:     path,
			PeriodS:  p.PeriodS,
			Estimate: p.Estimate,
			Why:      fmt.Sprintf("%s, %.1f yd loop", p.Source, p.LengthYd),
			Src:      src,
		}
	}
	return out
}
