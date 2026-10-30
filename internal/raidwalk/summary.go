package raidwalk

import (
	"fmt"
	"math"
	"strings"
	"time"
)

// Summary is the tb-raid-walk v1 file written to <out>/summary.json.
type Summary struct {
	Format       string           `json:"format"`
	Version      int              `json:"version"`
	Plan         string           `json:"plan"`
	Map          uint32           `json:"map"`
	Server       string           `json:"server"`
	Slot         int              `json:"slot"`
	Character    string           `json:"character"`
	Until        string           `json:"until"`
	From         string           `json:"from"`
	Speed        float64          `json:"speed"`
	TestSpeed    bool             `json:"test_speed"`
	RowsSHA256   string           `json:"rows_sha256"`
	UntilStep    int              `json:"until_step"`
	Roster       RosterSummary    `json:"roster"`
	Layout       LayoutSummary    `json:"layout"`
	Started      time.Time        `json:"started"`
	Ended        time.Time        `json:"ended"`
	EndReason    string           `json:"end_reason"`
	StepsReached int              `json:"steps_reached"`
	StepsTotal   int              `json:"steps_total"`
	StepTimeline []StepSummary    `json:"step_timeline"`
	Legs         []LegSummary     `json:"legs"`
	Deaths       DeathSummary     `json:"deaths"`
	Injections   []InjectSummary  `json:"injections"`
	Incidents    []InjectIncident `json:"incidents"`
	Wipes        []int            `json:"wipes"`
	BossEngaged  *BossSummary     `json:"boss_engaged"`
	Pauses       []PauseSummary   `json:"pauses"`
	Fights       []FightSummary   `json:"fights"`
	Dashboard    string           `json:"dashboard"`
	Teardown     TeardownSummary  `json:"teardown"`
	ExitCode     int              `json:"exit_code"`
	Notes        []string         `json:"notes"`
}

// RosterSummary is the roster as hired.
type RosterSummary struct {
	MT    string   `json:"mt"`
	OT    []string `json:"ot"`
	Rest  []string `json:"rest"`
	Level int      `json:"level"`
	Tier  int      `json:"tier"`
	// TierSource is where Tier came from: "table", "roster", "--tier" or "--tier-override" (tiers.go).
	TierSource string `json:"tier_source"`
	// TierComparison is a run whose tier is not the raid's minimum: comparison, not a tier proof.
	TierComparison bool `json:"tier_comparison"`
}

// LayoutSummary is the TBM:RAID_LAYOUT check.
type LayoutSummary struct {
	MT       string   `json:"mt"`
	Tanks    []string `json:"tanks"`
	Warnings []string `json:"warnings"`
}

// StepSummary is one step of the timeline.
type StepSummary struct {
	Step    int     `json:"step"`
	Leg     string  `json:"leg"`
	Kind    string  `json:"kind"`
	Seconds float64 `json:"seconds"`
}

// LegSummary is the time spent in one leg.
type LegSummary struct {
	Leg     string  `json:"leg"`
	Seconds float64 `json:"seconds"`
}

// DeathSummary is the deaths total and the per-step and per-bot splits.
type DeathSummary struct {
	Total  int            `json:"total"`
	ByStep map[int]int    `json:"by_step"`
	ByBot  map[string]int `json:"by_bot"`
	// Injected is the deaths a fault caused (kill-mt's main tank): not a death of the run's own walk.
	Injected int `json:"injected"`
}

// InjectSummary is one fault of a walk's --inject run (spec raid-walk-addons 1.5).
type InjectSummary struct {
	Kind    string `json:"kind"`
	Verdict string `json:"verdict"`
	Reason  string `json:"reason"`
	Step    int    `json:"step"`
	Victim  string `json:"victim"`
}

// BossSummary is the first boss the frames showed engaged.
type BossSummary struct {
	Entry uint32 `json:"entry"`
	Step  int    `json:"step"`
}

// PauseSummary is one pause.
type PauseSummary struct {
	Step    int     `json:"step"`
	Note    string  `json:"note"`
	Seconds float64 `json:"seconds"`
	Resumed bool    `json:"resumed"`
}

// FightSummary is one dashboard fight.
type FightSummary struct {
	ID       string `json:"id"`
	Step     int    `json:"step"`
	Leg      string `json:"leg"`
	DurMS    int64  `json:"dur_ms"`
	Deaths   int    `json:"deaths"`
	Outcome  string `json:"outcome"`
	BossName string `json:"boss_name"`
}

// TeardownSummary is what teardown reported.
type TeardownSummary struct {
	Failed    bool     `json:"failed"`
	HiresLeft []string `json:"hires_left"`
}

// SummaryMeta is what the command knows besides the tracker.
type SummaryMeta struct {
	Plan      string
	Map       uint32
	Server    string
	Slot      int
	Character string
	Until     string
	From      string
	Speed     float64
	TestSpeed bool
	// RowsSHA256 is the hex sha256 of planexport.WriteSQL of the walk's rows: the plan the run walked.
	RowsSHA256 string
	// UntilStep is the step of the start line's until label (0 when unknown or no until).
	UntilStep int
	Roster    *Roster
	// TierSource and TierComparison qualify the roster's tier (tiers.go).
	TierSource     string
	TierComparison bool
	Layout         LayoutSummary
	Started        time.Time
	Ended          time.Time
	EndReason      string
	Fights         []FightSummary
	Injections     []InjectSummary
	InjectedDeaths int
	Incidents      []InjectIncident
	Dashboard      string
	Teardown       TeardownSummary
	ExitCode       int
	Notes          []string
}

// BuildSummary fills the tb-raid-walk v1 summary from the tracker and the command's own facts.
func BuildSummary(t *Tracker, m SummaryMeta) *Summary {
	s := &Summary{
		Format:       "tb-raid-walk",
		Version:      1,
		Plan:         m.Plan,
		Map:          m.Map,
		Server:       m.Server,
		Slot:         m.Slot,
		Character:    m.Character,
		Until:        m.Until,
		From:         m.From,
		Speed:        m.Speed,
		TestSpeed:    m.TestSpeed,
		RowsSHA256:   m.RowsSHA256,
		UntilStep:    m.UntilStep,
		Layout:       m.Layout,
		Started:      m.Started,
		Ended:        m.Ended,
		EndReason:    m.EndReason,
		Fights:       nonNilFights(m.Fights),
		Injections:   nonNilInjections(m.Injections),
		Incidents:    nonNilIncidents(m.Incidents),
		Roster:       RosterSummary{OT: []string{}, Rest: []string{}},
		Dashboard:    m.Dashboard,
		Teardown:     m.Teardown,
		ExitCode:     m.ExitCode,
		Notes:        nonNilStrings(m.Notes),
		StepTimeline: []StepSummary{},
		Legs:         []LegSummary{},
		Wipes:        []int{},
		Pauses:       []PauseSummary{},
		Deaths:       DeathSummary{ByStep: map[int]int{}, ByBot: map[string]int{}},
	}
	if s.Layout.Tanks == nil {
		s.Layout.Tanks = []string{}
	}
	if s.Layout.Warnings == nil {
		s.Layout.Warnings = []string{}
	}
	if s.Teardown.HiresLeft == nil {
		s.Teardown.HiresLeft = []string{}
	}
	if m.Roster != nil {
		s.Roster = rosterSummary(m.Roster)
		s.Roster.TierSource = m.TierSource
		s.Roster.TierComparison = m.TierComparison
	}
	if t != nil {
		if s.EndReason == "" {
			s.EndReason = t.End().Reason()
		}
		s.StepsReached, s.StepsTotal = t.StepsReached(), t.StepsTotal()
		for _, sp := range t.Timeline() {
			s.StepTimeline = append(s.StepTimeline, StepSummary{Step: sp.Step, Leg: sp.Leg, Kind: sp.Kind, Seconds: round1(sp.Seconds)})
		}
		for _, l := range t.Legs() {
			s.Legs = append(s.Legs, LegSummary{Leg: l.Leg, Seconds: round1(l.Seconds)})
		}
		total, byStep, byBot := t.Deaths()
		s.Deaths = DeathSummary{Total: total, ByStep: byStep, ByBot: byBot}
		s.Wipes = t.Wipes()
		if s.Wipes == nil {
			s.Wipes = []int{}
		}
		if entry, step, ok := t.BossEngaged(); ok {
			s.BossEngaged = &BossSummary{Entry: entry, Step: step}
		}
		for _, p := range t.Pauses() {
			s.Pauses = append(s.Pauses, PauseSummary{Step: p.Step, Note: p.Note, Seconds: round1(p.Seconds), Resumed: p.Resumed})
		}
	}
	s.Deaths.Injected = m.InjectedDeaths
	return s
}

func rosterSummary(r *Roster) RosterSummary {
	out := RosterSummary{OT: []string{}, Rest: []string{}, Level: r.Level, Tier: r.Tier}
	for _, b := range r.Batches {
		switch b.Role {
		case RoleMT:
			out.MT = b.Hire
		case RoleOT:
			out.OT = append(out.OT, b.Hire)
		case RoleRest:
			out.Rest = append(out.Rest, b.Hire)
		}
	}
	return out
}

func nonNilStrings(in []string) []string {
	if in == nil {
		return []string{}
	}
	return in
}

func nonNilFights(in []FightSummary) []FightSummary {
	if in == nil {
		return []FightSummary{}
	}
	return in
}

func nonNilInjections(in []InjectSummary) []InjectSummary {
	if in == nil {
		return []InjectSummary{}
	}
	return in
}

func nonNilIncidents(in []InjectIncident) []InjectIncident {
	if in == nil {
		return []InjectIncident{}
	}
	return in
}

func round1(v float64) float64 { return math.Round(v*10) / 10 }

// comparisonWord marks a run whose tier is not the raid's minimum.
func comparisonWord(comparison bool) string {
	if !comparison {
		return ""
	}
	return ": comparison, not a tier proof"
}

func bossLine(b *BossSummary) string {
	if b == nil {
		return "none"
	}
	return fmt.Sprintf("%d at step %d", b.Entry, b.Step)
}

// Text is summary.txt: the same facts in a few readable lines. Its first line is the walk's one-line result.
func (s *Summary) Text() string {
	d := s.Ended.Sub(s.Started)
	if s.Ended.IsZero() || s.Started.IsZero() {
		d = 0
	}
	secs := int(d / time.Second)
	var b strings.Builder
	fmt.Fprintf(&b, "raid-walk %s: %s; steps %d/%d in %02d:%02d; deaths %d; fights %d; pauses %d",
		s.Plan, s.EndReason, s.StepsReached, s.StepsTotal, secs/60, secs%60, s.Deaths.Total, len(s.Fights), len(s.Pauses))
	if s.Until == "none" {
		b.WriteString(" (full clear: no until)")
	}
	b.WriteString("\n")
	fmt.Fprintf(&b, "plan %s map %d; server %s slot %d as %s\n", s.Plan, s.Map, s.Server, s.Slot, s.Character)
	if s.From != "" {
		fmt.Fprintf(&b, "from %s; until %s\n", s.From, s.Until)
	} else {
		fmt.Fprintf(&b, "until %s\n", s.Until)
	}
	if s.TestSpeed {
		fmt.Fprintf(&b, "speed %.1f (test)\n", s.Speed)
	} else if s.Speed > 0 {
		fmt.Fprintf(&b, "speed %.1f\n", s.Speed)
	}
	if s.Roster.Tier > 0 {
		fmt.Fprintf(&b, "tier %d (%s)%s\n", s.Roster.Tier, s.Roster.TierSource, comparisonWord(s.Roster.TierComparison))
	}
	fmt.Fprintf(&b, "started %s; ended %s; exit %d\n",
		s.Started.Format(time.RFC3339), s.Ended.Format(time.RFC3339), s.ExitCode)
	fmt.Fprintf(&b, "wipes %d; boss engaged: %s\n", len(s.Wipes), bossLine(s.BossEngaged))
	if len(s.Injections) > 0 {
		parts := make([]string, 0, len(s.Injections))
		for _, f := range s.Injections {
			part := f.Kind + " " + f.Verdict
			if f.Reason != "" {
				part += ": " + f.Reason
			}
			parts = append(parts, fmt.Sprintf("%s (step %d)", part, f.Step))
		}
		fmt.Fprintf(&b, "faults: %s\n", strings.Join(parts, "; "))
	}
	for _, inc := range s.Incidents {
		fmt.Fprintf(&b, "%s incident: %s; frozen: %s; manual: %s\n",
			strings.ToUpper(inc.Severity), inc.Kind, strings.Join(inc.Bots, ", "), inc.Manual)
	}
	fmt.Fprintf(&b, "dashboard: %s\n", s.Dashboard)
	if s.Teardown.Failed {
		fmt.Fprintf(&b, "teardown: failed\n")
	} else {
		fmt.Fprintf(&b, "teardown: ok\n")
	}
	if len(s.Teardown.HiresLeft) > 0 {
		fmt.Fprintf(&b, "hires possibly left online: %s\n", strings.Join(s.Teardown.HiresLeft, ", "))
	}
	if len(s.Notes) > 0 {
		b.WriteString("notes:\n")
		for _, n := range s.Notes {
			fmt.Fprintf(&b, "  %s\n", n)
		}
	}
	return b.String()
}
