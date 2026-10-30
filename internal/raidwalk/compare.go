package raidwalk

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"math"
	"os"
	"path/filepath"
	"sort"

	"github.com/pjerra/turtle-agent/internal/planexport"
)

// SuiteMetrics are the numbers raid-suite compares two walks of one plan by (spec raid-walk-addons 2.2).
type SuiteMetrics struct {
	RunS         float64 `json:"run_s"`
	Deaths       int     `json:"deaths"`
	Pauses       int     `json:"pauses"`
	Stops        int     `json:"stops"`
	Fights       int     `json:"fights"`
	StepsReached int     `json:"steps_reached"`
	StepsTotal   int     `json:"steps_total"`
}

// MetricsOf reads a walk's summary into the suite's numbers. RunS is the step_timeline's seconds (one decimal, as
// the summary's own times); deaths are the run's own (a fault's injected death is not one); stops count the pauses
// nobody resumed plus one when the walk ended on anything but its own goal ("until" or "plan done").
func MetricsOf(s *Summary) SuiteMetrics {
	m := SuiteMetrics{
		Pauses:       len(s.Pauses),
		Fights:       len(s.Fights),
		StepsReached: s.StepsReached,
		StepsTotal:   s.StepsTotal,
		Deaths:       s.Deaths.Total - s.Deaths.Injected,
	}
	var runS float64
	for _, st := range s.StepTimeline {
		runS += st.Seconds
	}
	m.RunS = round1(runS)
	for _, p := range s.Pauses {
		if !p.Resumed {
			m.Stops++
		}
	}
	if s.EndReason != string(EndUntil) && s.EndReason != string(EndPlanDone) {
		m.Stops++
	}
	return m
}

// SummaryFile is one walk of the history: its summary.json and where it was read from.
type SummaryFile struct {
	Path string
	S    *Summary
}

// ReadHistory reads <dir>/<plan>/*/summary.json, oldest walk first. A run whose summary.json is missing or cannot be
// read or parsed is a note (a walk killed before it wrote one), not an error; a plan with no history at all is an
// empty history. The only error is a plan directory that exists and cannot be listed.
func ReadHistory(dir, plan string) ([]SummaryFile, []string, error) {
	root := filepath.Join(dir, plan)
	entries, err := os.ReadDir(root)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return nil, nil, nil
		}
		return nil, nil, fmt.Errorf("read the history of %s: %w", plan, err)
	}
	var hist []SummaryFile
	var notes []string
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		path := filepath.Join(root, e.Name(), "summary.json")
		raw, err := os.ReadFile(path)
		if err != nil {
			if errors.Is(err, fs.ErrNotExist) {
				notes = append(notes, fmt.Sprintf("%s: no summary.json (a walk that did not finish)", path))
			} else {
				notes = append(notes, fmt.Sprintf("%s: %v", path, err))
			}
			continue
		}
		var s Summary
		if err := json.Unmarshal(raw, &s); err != nil {
			notes = append(notes, fmt.Sprintf("%s: %v", path, err))
			continue
		}
		hist = append(hist, SummaryFile{Path: path, S: &s})
	}
	sort.Slice(hist, func(i, j int) bool {
		if !hist[i].S.Started.Equal(hist[j].S.Started) {
			return hist[i].S.Started.Before(hist[j].S.Started)
		}
		return hist[i].Path < hist[j].Path
	})
	return hist, notes, nil
}

// LastGood picks the newest walk of the history worth comparing with cur: exit 0, ended on its own goal, no tier
// comparison, the same tier, until, speed and test speed, and no fault injection; never the walk at curPath. A nil
// return means cur is the first good walk (the suite calls it "new").
func LastGood(hist []SummaryFile, cur *Summary, curPath string) *SummaryFile {
	var best *SummaryFile
	for i := range hist {
		h := &hist[i]
		s := h.S
		if sameFile(h.Path, curPath) || s.ExitCode != 0 {
			continue
		}
		if s.EndReason != string(EndUntil) && s.EndReason != string(EndPlanDone) {
			continue
		}
		if s.Roster.TierComparison || s.Roster.Tier != cur.Roster.Tier {
			continue
		}
		if s.Until != cur.Until || s.Speed != cur.Speed || s.TestSpeed != cur.TestSpeed {
			continue
		}
		if len(s.Injections) != 0 {
			continue
		}
		if best == nil || s.Started.After(best.S.Started) ||
			(s.Started.Equal(best.S.Started) && h.Path > best.Path) {
			best = h
		}
	}
	return best
}

// sameFile compares two summary paths as far as the suite can: the walks' own <out> paths may be relative or
// absolute (`--history` and the walk's `--out` are written differently).
func sameFile(a, b string) bool {
	if a == b {
		return true
	}
	if a == "" || b == "" {
		return false
	}
	absA, errA := filepath.Abs(a)
	absB, errB := filepath.Abs(b)
	return errA == nil && errB == nil && absA == absB
}

// Regression is one rule that fired: which metric, the two values, and the rule in words. Metric "end" carries the
// two end reasons in Rule (its Base and New are 0): an end that differs from the baseline's is a regression of its
// own, whatever the stops count says.
type Regression struct {
	Metric string  `json:"metric"`
	Base   float64 `json:"base"`
	New    float64 `json:"new"`
	Rule   string  `json:"rule"`
}

// StepDelta is the time one step took in both walks.
type StepDelta struct {
	Step  int     `json:"step"`
	Kind  string  `json:"kind"`
	BaseS float64 `json:"base_s"`
	NewS  float64 `json:"new_s"`
}

// Comparison is what Compare found between a baseline walk and a new one.
type Comparison struct {
	Base        SuiteMetrics `json:"base"`
	New         SuiteMetrics `json:"new"`
	Regressions []Regression `json:"regressions"`
	Info        []string     `json:"info"`
	Notes       []string     `json:"notes"`
	StepDeltas  []StepDelta  `json:"step_deltas"`
}

// Compare applies the suite's rules (spec raid-walk-addons 2.2). Only the listed rules make a regression; a faster
// run and fewer fights are info. Rows that changed since the baseline are a note: the rules still apply, but the
// baseline is no longer the same route.
func Compare(base, cur *Summary) Comparison {
	b, n := MetricsOf(base), MetricsOf(cur)
	c := Comparison{Base: b, New: n}

	if n.RunS > b.RunS*1.20 && n.RunS-b.RunS > 60 {
		c.Regressions = append(c.Regressions, Regression{Metric: "time", Base: b.RunS, New: n.RunS,
			Rule: "new > base * 1.20 and new - base > 60 s"})
	}
	if n.RunS < b.RunS*0.80 {
		c.Info = append(c.Info, "faster")
	}
	if n.Deaths > b.Deaths+max(2, int(math.Ceil(0.25*float64(b.Deaths)))) {
		c.Regressions = append(c.Regressions, Regression{Metric: "deaths", Base: float64(b.Deaths), New: float64(n.Deaths),
			Rule: "new > base + max(2, 25% of base)"})
	}
	if n.Pauses > b.Pauses {
		c.Regressions = append(c.Regressions, Regression{Metric: "pauses", Base: float64(b.Pauses), New: float64(n.Pauses),
			Rule: "new > base"})
	}
	if n.Stops > b.Stops {
		c.Regressions = append(c.Regressions, Regression{Metric: "stops", Base: float64(b.Stops), New: float64(n.Stops),
			Rule: "new > base"})
	}
	if cur.EndReason != base.EndReason {
		c.Regressions = append(c.Regressions, Regression{Metric: "end",
			Rule: fmt.Sprintf("end reason %q -> %q", base.EndReason, cur.EndReason)})
	}
	if n.Fights > b.Fights+1 {
		c.Regressions = append(c.Regressions, Regression{Metric: "fights", Base: float64(b.Fights), New: float64(n.Fights),
			Rule: "new > base + 1"})
	}
	if n.Fights < b.Fights-1 {
		c.Info = append(c.Info, "fewer fights")
	}

	if base.RowsSHA256 != cur.RowsSHA256 && (base.RowsSHA256 != "" || cur.RowsSHA256 != "") {
		c.Notes = append(c.Notes, fmt.Sprintf("plan rows changed since the baseline (%s -> %s)",
			shortSHA(base.RowsSHA256), shortSHA(cur.RowsSHA256)))
	}
	if b.StepsTotal == n.StepsTotal {
		c.StepDeltas = stepDeltas(base.StepTimeline, cur.StepTimeline)
	}
	return c
}

// shortSHA is the first 8 hex digits of a rows sha; a walk without one shows as "none".
func shortSHA(sha string) string {
	if sha == "" {
		return "none"
	}
	if len(sha) > 8 {
		return sha[:8]
	}
	return sha
}

// stepDeltas lists the five steps whose time changed the most between the two runs, each by at least 10 s (a step
// match runs with the same step count). A step only one side has counts the other side as 0 s.
func stepDeltas(base, cur []StepSummary) []StepDelta {
	type side struct {
		kind  string
		baseS float64
		newS  float64
	}
	steps := map[int]*side{}
	for _, st := range base {
		v := steps[st.Step]
		if v == nil {
			v = &side{}
			steps[st.Step] = v
		}
		v.baseS, v.kind = st.Seconds, st.Kind
	}
	for _, st := range cur {
		v := steps[st.Step]
		if v == nil {
			v = &side{}
			steps[st.Step] = v
		}
		v.newS, v.kind = st.Seconds, st.Kind
	}
	var deltas []StepDelta
	for step, v := range steps {
		if d := math.Abs(v.newS - v.baseS); d >= 10 {
			deltas = append(deltas, StepDelta{Step: step, Kind: v.kind, BaseS: round1(v.baseS), NewS: round1(v.newS)})
		}
	}
	sort.Slice(deltas, func(i, j int) bool {
		di := math.Abs(deltas[i].NewS - deltas[i].BaseS)
		dj := math.Abs(deltas[j].NewS - deltas[j].BaseS)
		if di != dj {
			return di > dj
		}
		return deltas[i].Step < deltas[j].Step
	})
	if len(deltas) > 5 {
		deltas = deltas[:5]
	}
	return deltas
}

// FirstLegUntil is the `--until` raid-suite walks a plan with: a boss in the first leg (leg 0) is the walk's goal
// ("boss"); else the walk stops when the second leg starts ("leg <name of leg 1>", which the run leader's until
// takes as a leg name); a plan of one leg runs to its end ("none").
func FirstLegUntil(rows planexport.Rows) (string, error) {
	steps := append([]planexport.StepRow(nil), rows.Steps...)
	sort.SliceStable(steps, func(i, j int) bool {
		if steps[i].Leg != steps[j].Leg {
			return steps[i].Leg < steps[j].Leg
		}
		return steps[i].Seq < steps[j].Seq
	})
	for _, st := range steps {
		if st.Leg == 0 && st.Kind == planexport.KindBoss {
			return "boss", nil
		}
	}
	if len(rows.Legs) >= 2 {
		var name string
		for _, leg := range rows.Legs {
			if leg.Leg == 1 {
				name = leg.Name
				break
			}
		}
		if !untilLegRe.MatchString(name) {
			return "", errors.New("leg 2 has no name the run leader's until can take")
		}
		return "leg " + name, nil
	}
	return "none", nil
}
