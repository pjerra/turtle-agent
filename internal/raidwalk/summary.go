package raidwalk

import (
	"fmt"
	"math"
	"strconv"
	"strings"
	"time"
)

// Summary is the tb-raid-walk v1 file written to <out>/summary.json.
type Summary struct {
	Format    string `json:"format"`
	Version   int    `json:"version"`
	Plan      string `json:"plan"`
	Map       uint32 `json:"map"`
	Server    string `json:"server"`
	Slot      int    `json:"slot"`
	Character string `json:"character"`
	Until     string `json:"until"`
	// UntilEffective is the until the --place route guard shortened to, when it shortened (empty otherwise): Until
	// stays what the user asked for.
	UntilEffective string  `json:"until_effective,omitempty"`
	From           string  `json:"from"`
	Speed          float64 `json:"speed"`
	TestSpeed      bool    `json:"test_speed"`
	RowsSHA256     string  `json:"rows_sha256"`
	UntilStep      int     `json:"until_step"`
	// RunLabel is the newest speed/until label the run's status lines carried ("speed 2.0 (test); until boss (step 41)");
	// SpeedFellBack says the server's test speed went back to 1.0 once a real player joined.
	RunLabel      string           `json:"run_label,omitempty"`
	SpeedFellBack bool             `json:"speed_fell_back,omitempty"`
	Roster        RosterSummary    `json:"roster"`
	Layout        LayoutSummary    `json:"layout"`
	Started       time.Time        `json:"started"`
	Ended         time.Time        `json:"ended"`
	EndReason     string           `json:"end_reason"`
	StepsReached  int              `json:"steps_reached"`
	StepsTotal    int              `json:"steps_total"`
	StepTimeline  []StepSummary    `json:"step_timeline"`
	Legs          []LegSummary     `json:"legs"`
	Deaths        DeathSummary     `json:"deaths"`
	Injections    []InjectSummary  `json:"injections"`
	Incidents     []InjectIncident `json:"incidents"`
	Wipes         []int            `json:"wipes"`
	WipeResumes   int              `json:"wipe_resumes"`
	// GMInstanceDataUnsent counts the --gm-instance-data legs whose lines were never sent (no out-of-combat rest/gather in the leg).
	GMInstanceDataUnsent int `json:"gm_instance_data_unsent,omitempty"`
	// The --revive-at-raid counts (omitted when the walk never needed it): hires summoned back to the revive point,
	// hires two summons did not bring within 10 yd, and hires still out of range or in combat when a resume went out.
	ReviveBrought      int          `json:"revive_brought,omitempty"`
	ReviveUnreached    int          `json:"revive_unreached,omitempty"`
	ReviveGatherMissed int          `json:"revive_gather_missed,omitempty"`
	BossEngaged        *BossSummary `json:"boss_engaged"`
	// BossKill is the `boss_kill` object of a walk whose frames engaged a boss (spec 6, D5): the run leader's own
	// kill report, the phase the boss unit was seen with and the evidence. Absent when no boss was engaged. It is the
	// LAST engaged boss's record; BossKills lists one record per boss step the run's statuses showed.
	BossKill *BossKillSummary `json:"boss_kill,omitempty"`
	// BossKills lists one record per boss step the run's statuses showed (spec 6, D5): each boss row's entry,
	// whether the encounter was engaged, killed or skipped, and the phase the boss's guid was seen with. Absent when
	// no boss row was seen.
	BossKills []BossKillSummary `json:"boss_kills,omitempty"`
	Pauses    []PauseSummary    `json:"pauses"`
	Fights    []FightSummary    `json:"fights"`
	Decisions *DecisionsSummary `json:"decisions,omitempty"`
	Dashboard string            `json:"dashboard"`
	Teardown  TeardownSummary   `json:"teardown"`
	// Place is the `place` object of a `--place` walk (spec 5.11): the start step the raid was put at, the instance, the
	// cleared circles and the result. Absent without --place.
	Place *PlaceSummary `json:"place,omitempty"`
	// Assisted is true for a walk that used --gm-clear (a test server's GM killed units for it): the result is NOT a real clear.
	// GMClear lists every unit the tool tried to kill. Both are absent without --gm-clear.
	Assisted bool           `json:"assisted,omitempty"`
	GMClear  []GMClearEntry `json:"gm_clear,omitempty"`
	// GMClearPartial is true when an expected pack member was not confirmed dead (not seen, or still alive): the clear is PARTIAL.
	GMClearPartial bool `json:"gm_clear_partial,omitempty"`
	// ReviveRefusal is what kept the revive point from going quiet when the walk gave up on it (D-D, walk 25). Absent
	// when the walk never refused a revive.
	ReviveRefusal *ReviveRefusal `json:"revive_refusal,omitempty"`
	ExitCode      int            `json:"exit_code"`
	Notes         []string       `json:"notes"`
}

// ReviveRefusal is the summary's `revive_refusal` object (D-D): the walk's revive never found a quiet point, ended
// there (exit 19, "not revived: <why>"), and this says what blocked it and for how long.
type ReviveRefusal struct {
	WipeStep int `json:"wipe_step"`
	// StartStep is the step of the revive point the walk actually used: the placement's staged start with --place,
	// else the first positioned row of the plan (review round 1, Opus L3: a plain --from walk revives at the plan's
	// own first point, whatever step the run is started on).
	StartStep int `json:"start_step"`
	// TriedSteps are the revive points the walk read, in order: the revive point, then the fallback rows of its leg
	// and the previous leg's last move row (the entrance has no step: EntranceTried says it was reached).
	TriedSteps []int `json:"tried_steps"`
	// EntranceTried says the entrance fallback's `.go xyz` went out (review round 1, Opus L3): true while the walk
	// refused at the entrance, whether its go landed or not.
	EntranceTried bool `json:"entrance_tried"`
	// WaitedS is how long the revive waited for the point to go quiet: the 120 s check plus the D-C re-reads, in
	// seconds, from the first read.
	WaitedS  int             `json:"waited_s"`
	Blockers []ReviveBlocker `json:"blockers"`
	// AllIdle says every last blocker of the last read was idle (not in combat, no target, not moving): waiting would
	// not have helped.
	AllIdle bool `json:"all_idle"`
}

// ReviveBlocker is one blocker of a refusal, as the last read saw it.
type ReviveBlocker struct {
	Entry uint32  `json:"entry"`
	Name  string  `json:"name"`
	Dist  float64 `json:"dist"`
	Idle  bool    `json:"idle"`
	// InCombat is the unit's UNIT_FLAG_IN_COMBAT: the state that keeps the 45 yd rule on it.
	InCombat bool `json:"in_combat"`
	// Patrol marks a patrol rather than a unit (entry 0, no combat): a --patrols file's patrol or the plan's own
	// patrol block. Only its path blocks.
	Patrol bool `json:"patrol,omitempty"`
}

// Line is summary.txt's one-line refusal, under the walk's end.
func (r *ReviveRefusal) Line() string {
	steps := make([]string, 0, len(r.TriedSteps))
	for _, s := range r.TriedSteps {
		steps = append(steps, strconv.Itoa(s))
	}
	bs := make([]string, 0, len(r.Blockers))
	for _, b := range r.Blockers {
		part := fmt.Sprintf("%s %.1f yd", b.Name, b.Dist)
		if b.Entry != 0 {
			part = fmt.Sprintf("%d %s", b.Entry, part)
		}
		switch {
		case b.Patrol:
			part += " patrol"
		case b.Idle:
			part += " idle"
		case b.InCombat:
			part += " in combat"
		default:
			part += " not idle"
		}
		bs = append(bs, part)
	}
	if len(bs) == 0 {
		bs = append(bs, "none")
	}
	entrance := ""
	if r.EntranceTried {
		entrance = ", entrance tried"
	}
	return fmt.Sprintf("revive refusal: wipe step %d, tried %s%s, waited %d:%02d, blockers %s",
		r.WipeStep, strings.Join(steps, "/"), entrance, r.WaitedS/60, r.WaitedS%60, strings.Join(bs, "; "))
}

// PlaceSummary is the summary's `place` object (spec 5.11).
type PlaceSummary struct {
	Start      int                  `json:"start_step"`
	Leg        string               `json:"leg,omitempty"`
	Point      []float32            `json:"point"`
	Stop       int                  `json:"until_step"`
	Instance   float64              `json:"instance"`
	Circles    []PlaceCircleSummary `json:"circles"`
	NotCleared []string             `json:"not_cleared"`
	Quiet      bool                 `json:"quiet"`
	Placed     int                  `json:"placed"`
	Hires      int                  `json:"hires"`
	Summoned   []string             `json:"summoned,omitempty"`
	Notes      []string             `json:"notes,omitempty"`
}

// PlaceCircleSummary is one cleared circle of the summary's `place` object: the BEHIND row it came from and what the
// toolkit removed there.
type PlaceCircleSummary struct {
	Step    int      `json:"step"`
	Leg     int      `json:"leg"`
	X       float32  `json:"x"`
	Y       float32  `json:"y"`
	Z       float32  `json:"z"`
	Radius  float32  `json:"radius"`
	Removed int      `json:"removed"`
	Skipped []string `json:"skipped,omitempty"`
	PerUnit bool     `json:"per_unit,omitempty"`
	Unit    uint32   `json:"unit,omitempty"`
	Sweep   bool     `json:"sweep,omitempty"`
	Moved   bool     `json:"moved,omitempty"`
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
	// HireSettleS is the --hire-settle wait in seconds, written by every summary that has the field (0 for
	// --hire-settle 0). A pointer: a summary written before the field existed decodes it as nil, absent, not 0.
	// SettleShort is a run whose wait is below the default: its refit may land before a late hire's join, so it is
	// no baseline for the tier proof (LastGood skips it).
	HireSettleS *float64 `json:"hire_settle_s,omitempty"`
	SettleShort bool     `json:"settle_short"`
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
	// Assisted marks a leg the walk's --gm-clear killed units of: its result is not a real clear.
	Assisted bool `json:"assisted,omitempty"`
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

// BossKillSummary is the summary's `boss_kill` object (spec 6, D5): the boss a walk's frames engaged, whether the
// run reported the kill, and the phase the boss unit was seen with. `phase` is 1 + the entry changes the frames saw
// on the boss's guid (a script that changes a creature's entry keeps its guid); `phase_entry` is the entry that guid
// read last. `evidence` is the run leader's own `boss down` note (or the walk's skip evidence) when the kill holds,
// and the reason when it does not.
type BossKillSummary struct {
	Entry   uint32 `json:"entry"`
	Step    int    `json:"step"`
	Engaged bool   `json:"engaged,omitempty"`
	Killed  bool   `json:"killed"`
	// Skipped is the walk's own `.bot run skip` for the step (the old-module path, where no `boss down` note
	// arrives): it is kill evidence too.
	Skipped    bool   `json:"skipped,omitempty"`
	Phase      int    `json:"phase"`
	PhaseEntry uint32 `json:"phase_entry"`
	Evidence   string `json:"evidence"`
}

// BossKillLine is the summary.txt line of the boss kill (spec 6): the entry and step, the verdict with its reason
// when the kill does not hold, the phase and the evidence.
func (k *BossKillSummary) Line() string {
	if !k.Engaged && !k.Killed && !k.Skipped {
		return fmt.Sprintf("boss step: %d at step %d, not engaged", k.Entry, k.Step)
	}
	if !k.Killed {
		return fmt.Sprintf("boss kill: %d at step %d, not killed (%s), phase %d (%d)", k.Entry, k.Step, k.Evidence, k.Phase, k.PhaseEntry)
	}
	line := fmt.Sprintf("boss kill: %d at step %d, killed, phase %d (%d)", k.Entry, k.Step, k.Phase, k.PhaseEntry)
	if k.Evidence != "" {
		line += ": " + k.Evidence
	}
	return line
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
	// BossPhase and BossPhaseEntry are the boss's phase in this fight when TortoiseBots sends them (spec 6, D5):
	// 1 + the entry changes of the boss unit (1 = the boss never changed entry), and the entry he read last.
	BossPhase      int    `json:"boss_phase,omitempty"`
	BossPhaseEntry uint32 `json:"boss_phase_entry,omitempty"`
	// Warnings is the dashboard's rule warning count of the fight (the list row's).
	Warnings int `json:"warnings,omitempty"`
	// Detail is the digest of the fight's detail file (fights.go); nil when it could not be read.
	Detail *FightDetail `json:"detail,omitempty"`
	// History is true when the dashboard already listed the fight before this walk filed one: an earlier
	// walk's or instance's record. The walk keeps it for the kill credit and the summary but never copies
	// its detail file into this walk's dir (fights-run.json names the at-start set).
	History bool `json:"history,omitempty"`
}

// DecisionsFileSummary is one file of the decisions pull: its name, bytes and sha256 hex.
type DecisionsFileSummary struct {
	Name   string `json:"name"`
	Bytes  int64  `json:"bytes"`
	SHA256 string `json:"sha256"`
}

// DecisionsCaptureSummary is one pulled decision capture: its id, file count and bytes.
type DecisionsCaptureSummary struct {
	ID    string `json:"id"`
	Files int    `json:"files"`
	Bytes int64  `json:"bytes"`
}

// DecisionsSummary is the walk's decisions pull: a note plus one row per pulled capture.
type DecisionsSummary struct {
	Note     string                    `json:"note"`
	Captures []DecisionsCaptureSummary `json:"captures"`
	Bytes    int64                     `json:"bytes"`
}

// TeardownSummary is what teardown reported. Reason is the first failing step
// ("gone", "unbind", "remove X", "logout", "rpc", "lease", ...) with its detail;
// empty when nothing failed.
type TeardownSummary struct {
	Failed    bool     `json:"failed"`
	HiresLeft []string `json:"hires_left"`
	Reason    string   `json:"reason,omitempty"`
}

// SummaryMeta is what the command knows besides the tracker.
type SummaryMeta struct {
	Plan      string
	Map       uint32
	Server    string
	Slot      int
	Character string
	Until     string
	// UntilEffective is the until a --place walk's route guard shortened to (F5, D8): empty when it did not shorten.
	UntilEffective string
	From           string
	Speed          float64
	TestSpeed      bool
	// RowsSHA256 is the hex sha256 of planexport.WriteSQL of the walk's rows: the plan the run walked.
	RowsSHA256 string
	// UntilStep is the step of the start line's until label (0 when unknown or no until).
	UntilStep int
	// RunLabel and SpeedFellBack are the status lines' label facts (Summary).
	RunLabel      string
	SpeedFellBack bool
	Roster        *Roster
	// TierSource and TierComparison qualify the roster's tier (tiers.go).
	TierSource     string
	TierComparison bool
	// HireSettle is the --hire-settle wait; SettleShort marks it below the default (RosterSummary).
	HireSettle  time.Duration
	SettleShort bool
	// WipeResumes is the walk's recovered wipes and near-wipe stops (--wipe-resume).
	WipeResumes                                        int
	GMInstanceDataUnsent                               int
	ReviveBrought, ReviveUnreached, ReviveGatherMissed int
	Layout                                             LayoutSummary
	Started                                            time.Time
	Ended                                              time.Time
	EndReason                                          string
	Fights                                             []FightSummary
	Decisions                                          *DecisionsSummary
	Injections                                         []InjectSummary
	InjectedDeaths                                     int
	Incidents                                          []InjectIncident
	Dashboard                                          string
	Teardown                                           TeardownSummary
	// Place is the --place walk's place object (nil without --place).
	Place *PlaceSummary
	// GMClearUsed says the walk ran --gm-clear (also when no unit was found); GMClear is what it tried to kill and GMClearLegs the legs
	// it came from.
	GMClearUsed bool
	GMClear     []GMClearEntry
	GMClearLegs []string
	// ReviveRefusal is the walk's revive_refusal (nil when no revive was refused).
	ReviveRefusal *ReviveRefusal
	// BossKill is the walk's boss_kill (nil when no boss was engaged).
	BossKill *BossKillSummary
	// BossKills lists one record per boss step the run's statuses showed (nil when no boss row was seen).
	BossKills []BossKillSummary
	ExitCode  int
	Notes     []string
}

// BuildSummary fills the tb-raid-walk v1 summary from the tracker and the command's own facts.
func BuildSummary(t *Tracker, m SummaryMeta) *Summary {
	s := &Summary{
		Format:               "tb-raid-walk",
		Version:              1,
		Plan:                 m.Plan,
		Map:                  m.Map,
		Server:               m.Server,
		Slot:                 m.Slot,
		Character:            m.Character,
		Until:                m.Until,
		UntilEffective:       m.UntilEffective,
		From:                 m.From,
		Speed:                m.Speed,
		TestSpeed:            m.TestSpeed,
		RowsSHA256:           m.RowsSHA256,
		UntilStep:            m.UntilStep,
		RunLabel:             m.RunLabel,
		SpeedFellBack:        m.SpeedFellBack,
		Layout:               m.Layout,
		Started:              m.Started,
		Ended:                m.Ended,
		EndReason:            m.EndReason,
		Fights:               nonNilFights(m.Fights),
		Decisions:            m.Decisions,
		Injections:           nonNilInjections(m.Injections),
		Incidents:            nonNilIncidents(m.Incidents),
		Roster:               RosterSummary{OT: []string{}, Rest: []string{}},
		Dashboard:            m.Dashboard,
		Teardown:             m.Teardown,
		Place:                m.Place,
		Assisted:             m.GMClearUsed,
		GMClear:              m.GMClear,
		GMClearPartial:       GMClearPartial(m.GMClear),
		BossKill:             m.BossKill,
		BossKills:            m.BossKills,
		ReviveRefusal:        m.ReviveRefusal,
		ExitCode:             m.ExitCode,
		Notes:                nonNilStrings(m.Notes),
		WipeResumes:          m.WipeResumes,
		GMInstanceDataUnsent: m.GMInstanceDataUnsent,
		ReviveBrought:        m.ReviveBrought, ReviveUnreached: m.ReviveUnreached, ReviveGatherMissed: m.ReviveGatherMissed,
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
	hireSettle := m.HireSettle.Seconds()
	s.Roster.HireSettleS = &hireSettle
	s.Roster.SettleShort = m.SettleShort
	if t != nil {
		if s.EndReason == "" {
			s.EndReason = t.End().Reason()
		}
		s.StepsReached, s.StepsTotal = t.StepsReached(), t.StepsTotal()
		for _, sp := range t.Timeline() {
			s.StepTimeline = append(s.StepTimeline, StepSummary{Step: sp.Step, Leg: sp.Leg, Kind: sp.Kind, Seconds: round1(sp.Seconds)})
		}
		assisted := map[string]bool{}
		for _, l := range m.GMClearLegs {
			assisted[l] = true
		}
		for _, l := range t.Legs() {
			s.Legs = append(s.Legs, LegSummary{Leg: l.Leg, Seconds: round1(l.Seconds), Assisted: assisted[l.Leg]})
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
	if s.Assisted {
		b.WriteString(" ASSISTED")
		if s.GMClearPartial {
			b.WriteString(" PARTIAL")
		}
	}
	b.WriteString("\n")
	if s.Assisted {
		b.WriteString(GMClearSummaryLine(s.GMClear, assistedLegs(s)) + "\n")
	}
	fmt.Fprintf(&b, "plan %s map %d; server %s slot %d as %s\n", s.Plan, s.Map, s.Server, s.Slot, s.Character)
	// A --place walk whose route guard shortened the until shows both: what the user asked for and what the run
	// actually walked (F5, D8).
	shortened := ""
	if s.UntilEffective != "" {
		shortened = fmt.Sprintf(" (the place shortens it to %s)", s.UntilEffective)
	}
	if s.From != "" {
		fmt.Fprintf(&b, "from %s; until %s%s\n", s.From, s.Until, shortened)
	} else {
		fmt.Fprintf(&b, "until %s%s\n", s.Until, shortened)
	}
	if s.TestSpeed {
		fmt.Fprintf(&b, "speed %.1f (test)", s.Speed)
		if s.SpeedFellBack {
			b.WriteString("; the server went back to 1.0 once a real player joined")
		}
		b.WriteString("\n")
	} else if s.Speed > 0 {
		fmt.Fprintf(&b, "speed %.1f\n", s.Speed)
	}
	if s.RunLabel != "" {
		fmt.Fprintf(&b, "server label: %s\n", s.RunLabel)
	}
	if s.Roster.Tier > 0 {
		fmt.Fprintf(&b, "tier %d (%s)%s\n", s.Roster.Tier, s.Roster.TierSource, comparisonWord(s.Roster.TierComparison))
	}
	fmt.Fprintf(&b, "started %s; ended %s; exit %d\n",
		s.Started.Format(time.RFC3339), s.Ended.Format(time.RFC3339), s.ExitCode)
	if s.ReviveRefusal != nil {
		b.WriteString(s.ReviveRefusal.Line() + "\n")
	}
	fmt.Fprintf(&b, "wipes %d; boss engaged: %s\n", len(s.Wipes), bossLine(s.BossEngaged))
	if s.GMInstanceDataUnsent > 0 {
		fmt.Fprintf(&b, "gm_instance_data_unsent=%d\n", s.GMInstanceDataUnsent)
	}
	if s.BossKill != nil {
		b.WriteString(s.BossKill.Line() + "\n")
	}
	if len(s.BossKills) > 1 {
		b.WriteString("boss steps:\n")
		for i := range s.BossKills {
			b.WriteString("  " + s.BossKills[i].Line() + "\n")
		}
	}
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
	var fl []string
	for _, f := range s.Fights {
		if l := fightLine(f); l != "" {
			fl = append(fl, l)
		}
	}
	if len(fl) > 0 {
		b.WriteString("fights with deaths or red warnings:\n")
		for _, l := range fl {
			fmt.Fprintf(&b, "  %s\n", l)
		}
	}
	fmt.Fprintf(&b, "dashboard: %s\n", s.Dashboard)
	if s.Teardown.Failed {
		if s.Teardown.Reason != "" {
			fmt.Fprintf(&b, "teardown: failed (%s)\n", s.Teardown.Reason)
		} else {
			fmt.Fprintf(&b, "teardown: failed\n")
		}
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

// assistedLegs are the legs of the walk's gm_clear entries, in first-use order (the Legs marks when the tracker saw them).
func assistedLegs(s *Summary) []string {
	var out []string
	seen := map[string]bool{}
	for _, e := range s.GMClear {
		if !seen[e.Leg] {
			seen[e.Leg] = true
			out = append(out, e.Leg)
		}
	}
	if len(out) == 0 {
		out = append(out, "no live unit found")
	}
	return out
}
