package raidwalk

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/pjerra/turtle-agent/internal/planexport"
)

// suiteSum is a minimal summary of one walk: a single step carrying RunS, then the splits the metrics read.
func suiteSum(runS float64, deaths, injected int, end string) *Summary {
	return &Summary{
		Plan:         "onyxias_lair",
		Until:        "boss",
		Speed:        1,
		RowsSHA256:   "abcdef0123456789",
		EndReason:    end,
		StepsReached: 3,
		StepsTotal:   29,
		StepTimeline: []StepSummary{{Step: 1, Leg: "", Kind: "move", Seconds: runS}},
		Deaths:       DeathSummary{Total: deaths, Injected: injected},
	}
}

func withFights(s *Summary, n int) *Summary {
	for i := range n {
		s.Fights = append(s.Fights, FightSummary{ID: "f" + string(rune('a'+i))})
	}
	return s
}

// TestMetricsOf is spec raid-walk-addons 2.4: stops count the pauses nobody resumed plus an end that is not the
// walk's own goal, and the injected deaths do not count as deaths of the run.
func TestMetricsOf(t *testing.T) {
	s := &Summary{
		EndReason:    "stopped: stalled 40 s at step 5",
		StepsReached: 5,
		StepsTotal:   29,
		StepTimeline: []StepSummary{{Seconds: 100.5}, {Seconds: 201.5}},
		Deaths:       DeathSummary{Total: 3, Injected: 1},
		Pauses: []PauseSummary{
			{Step: 2, Resumed: true},
			{Step: 4},
			{Step: 5, Resumed: true},
		},
		Fights: []FightSummary{{}, {}},
	}
	m := MetricsOf(s)
	if m.RunS != 302 {
		t.Fatalf("RunS = %v, want the step_timeline sum 302", m.RunS)
	}
	if m.Deaths != 2 {
		t.Fatalf("Deaths = %d, want 2 (3 total - 1 injected)", m.Deaths)
	}
	if m.Pauses != 3 {
		t.Fatalf("Pauses = %d, want 3", m.Pauses)
	}
	if m.Stops != 2 {
		t.Fatalf("Stops = %d, want 2 (one pause not resumed + one end that is not until/plan done)", m.Stops)
	}
	if m.Fights != 2 || m.StepsReached != 5 || m.StepsTotal != 29 {
		t.Fatalf("metrics %+v, want fights 2, steps 5/29", m)
	}
	if got := MetricsOf(&Summary{EndReason: "until"}); got.Stops != 0 {
		t.Fatalf("Stops = %d for an until run, want 0", got.Stops)
	}
	if got := MetricsOf(&Summary{EndReason: "plan done"}); got.Stops != 0 {
		t.Fatalf("Stops = %d for a plan-done run, want 0", got.Stops)
	}
}

// TestLastGoodSkipsBadAndMismatched is spec raid-walk-addons 2.2: the baseline is a clean walk of the same tier and
// command against the same plan rows, and never the walk being compared.
func TestLastGoodSkipsBadAndMismatched(t *testing.T) {
	at := time.Date(2026, 10, 29, 20, 0, 0, 0, time.UTC)
	cur := suiteSum(300, 0, 0, "until")
	cur.Roster.Tier = 2
	cur.TestSpeed = false
	mk := func(name string, minute int, mut func(*Summary)) SummaryFile {
		s := suiteSum(300, 0, 0, "until")
		s.Roster.Tier = 2
		s.Started = at.Add(time.Duration(minute) * time.Minute)
		s.ExitCode = 0
		if mut != nil {
			mut(s)
		}
		return SummaryFile{Path: "/hist/onyxias_lair/" + name + "/summary.json", S: s}
	}
	hist := []SummaryFile{
		mk("cur", 60, nil),
		mk("exit19", 59, func(s *Summary) { s.ExitCode = 19 }),
		mk("stopped", 58, func(s *Summary) { s.EndReason = "stopped: stalled" }),
		mk("comparison", 57, func(s *Summary) { s.Roster.TierComparison = true }),
		mk("tier3", 56, func(s *Summary) { s.Roster.Tier = 3 }),
		mk("until-step", 55, func(s *Summary) { s.Until = "step 12" }),
		mk("speed2", 54, func(s *Summary) { s.Speed = 2 }),
		mk("testspeed", 53, func(s *Summary) { s.TestSpeed = true }),
		mk("injected", 52, func(s *Summary) { s.Injections = []InjectSummary{{Kind: "lag", Verdict: "pass"}} }),
		mk("good-old", 10, nil),
		mk("good-new", 20, func(s *Summary) { s.EndReason = "plan done" }),
	}
	got := LastGood(hist, cur, "/hist/onyxias_lair/cur/summary.json")
	if got == nil {
		t.Fatal("LastGood found no baseline among the good walks")
	}
	if got.Path != "/hist/onyxias_lair/good-new/summary.json" {
		t.Fatalf("baseline %s, want the newest good walk good-new", got.Path)
	}
	if got := LastGood(hist[:1], cur, "/hist/onyxias_lair/cur/summary.json"); got != nil {
		t.Fatalf("baseline %+v for a history of the current walk only, want none", got)
	}
	if got := LastGood(nil, cur, ""); got != nil {
		t.Fatalf("baseline %+v for an empty history, want none", got)
	}
}

// TestCompareRules is spec raid-walk-addons 2.2/2.4: every rule's boundary, exactly.
func TestCompareRules(t *testing.T) {
	run := func(baseS, newS float64) Comparison {
		b := suiteSum(baseS, 0, 0, "until")
		n := suiteSum(newS, 0, 0, "until")
		b.StepsTotal, n.StepsTotal = 29, 30
		return Compare(b, n)
	}
	cases := []struct {
		name           string
		baseS, newS    float64
		wantRegression bool
	}{
		{"time +19 percent", 300, 357, false},
		{"time +25 percent but +50 s", 200, 250, false},
		{"time +25 percent and +70 s", 280, 350, true},
	}
	for _, c := range cases {
		got := run(c.baseS, c.newS)
		has := false
		for _, r := range got.Regressions {
			if r.Metric == "time" {
				has = true
			}
		}
		if has != c.wantRegression {
			t.Fatalf("%s: time regression %v, want %v (%+v)", c.name, has, c.wantRegression, got.Regressions)
		}
	}
	if faster := Compare(suiteSum(300, 0, 0, "until"), suiteSum(239, 0, 0, "until")); len(faster.Regressions) != 0 || len(faster.Info) != 1 || faster.Info[0] != "faster" {
		t.Fatalf("239 s against 300 s: regressions %v info %v, want only the faster info", faster.Regressions, faster.Info)
	}

	deaths := func(baseN, newN int) bool {
		got := Compare(suiteSum(300, baseN, 0, "until"), suiteSum(300, newN, 0, "until"))
		for _, r := range got.Regressions {
			if r.Metric == "deaths" {
				return true
			}
		}
		return false
	}
	for _, c := range []struct {
		base, new int
		want      bool
	}{{0, 2, false}, {0, 3, true}, {12, 15, false}, {12, 16, true}} {
		if got := deaths(c.base, c.new); got != c.want {
			t.Fatalf("deaths %d -> %d: regression %v, want %v", c.base, c.new, got, c.want)
		}
	}

	pauses := func(baseN, newN int) bool {
		b, n := suiteSum(300, 0, 0, "until"), suiteSum(300, 0, 0, "until")
		for i := range baseN {
			b.Pauses = append(b.Pauses, PauseSummary{Step: i + 1, Resumed: true})
		}
		for i := range newN {
			n.Pauses = append(n.Pauses, PauseSummary{Step: i + 1, Resumed: true})
		}
		for _, r := range Compare(b, n).Regressions {
			if r.Metric == "pauses" {
				return true
			}
		}
		return false
	}
	if pauses(0, 0) || !pauses(0, 1) || !pauses(2, 4) {
		t.Fatal("pauses rule: want a regression for +1 and +2, none for equal")
	}

	fights := func(baseN, newN int) bool {
		for _, r := range Compare(withFights(suiteSum(300, 0, 0, "until"), baseN), withFights(suiteSum(300, 0, 0, "until"), newN)).Regressions {
			if r.Metric == "fights" {
				return true
			}
		}
		return false
	}
	if fights(1, 2) || !fights(1, 3) || fights(0, 1) {
		t.Fatal("fights rule: want none for +1, one for +2, one for 0 -> 1")
	}

	stops := func(mut func(*Summary)) bool {
		n := suiteSum(300, 0, 0, "until")
		mut(n)
		for _, r := range Compare(suiteSum(300, 0, 0, "until"), n).Regressions {
			if r.Metric == "stops" || r.Metric == "end" {
				return true
			}
		}
		return false
	}
	if stops(func(s *Summary) { s.Pauses = []PauseSummary{{Step: 1, Resumed: true}} }) {
		t.Fatal("stops rule: a resumed pause is no stop")
	}
	if !stops(func(s *Summary) { s.Pauses = []PauseSummary{{Step: 1}} }) {
		t.Fatal("stops rule: a pause nobody resumed is a stop")
	}
	if !stops(func(s *Summary) { s.EndReason = "stopped: stalled" }) {
		t.Fatal("end rule: a different end reason is a regression")
	}

	rows := Compare(suiteSum(300, 0, 0, "until"), func() *Summary {
		s := suiteSum(300, 0, 0, "until")
		s.RowsSHA256 = "9988776655443322"
		return s
	}())
	wantNote := "plan rows changed since the baseline (abcdef01 -> 99887766)"
	if len(rows.Notes) != 1 || rows.Notes[0] != wantNote {
		t.Fatalf("rows notes %q, want [%q]", rows.Notes, wantNote)
	}
	same := Compare(suiteSum(300, 0, 0, "until"), suiteSum(300, 0, 0, "until"))
	if len(same.Notes) != 0 {
		t.Fatalf("notes for the same rows: %q, want none", same.Notes)
	}
}

// TestCompareStepDeltas is spec raid-walk-addons 2.2: with the same step count, the five steps that lost or gained the
// most seconds (each at least 10 s) are reported.
func TestCompareStepDeltas(t *testing.T) {
	mk := func(secs ...float64) *Summary {
		s := suiteSum(0, 0, 0, "until")
		s.StepsTotal = len(secs)
		s.StepTimeline = nil
		for i, v := range secs {
			kind := "move"
			if i == 0 {
				kind = "pull"
			}
			s.StepTimeline = append(s.StepTimeline, StepSummary{Step: i + 1, Kind: kind, Seconds: v})
		}
		return s
	}
	base := mk(50, 40, 30, 20, 20, 10, 5)
	cur := mk(90, 40, 30, 20, 20, 25, 5) // +40, +15, and five differences under 10 s
	got := Compare(base, cur)
	if len(got.StepDeltas) != 2 {
		t.Fatalf("step deltas %+v, want the two steps over 10 s", got.StepDeltas)
	}
	if got.StepDeltas[0].Step != 1 || got.StepDeltas[0].Kind != "pull" || got.StepDeltas[0].BaseS != 50 || got.StepDeltas[0].NewS != 90 {
		t.Fatalf("first delta %+v, want step 1 pull 50 -> 90", got.StepDeltas[0])
	}
	if got.StepDeltas[1].Step != 6 || got.StepDeltas[1].BaseS != 10 || got.StepDeltas[1].NewS != 25 {
		t.Fatalf("second delta %+v, want step 6 10 -> 25", got.StepDeltas[1])
	}
	// Different step counts: no per-step comparison.
	cur.StepsTotal = 8
	if deltas := Compare(base, cur).StepDeltas; len(deltas) != 0 {
		t.Fatalf("step deltas %+v for unequal step counts, want none", deltas)
	}
	// More than five over 10 s: the five largest by absolute change.
	base = mk(100, 100, 100, 100, 100, 100, 100)
	cur = mk(105, 111, 118, 126, 130, 145, 151)
	deltas := Compare(base, cur).StepDeltas
	if len(deltas) != 5 {
		t.Fatalf("deltas %+v, want the five largest of the six over 10 s", deltas)
	}
	want := []int{7, 6, 5, 4, 3}
	for i, step := range want {
		if deltas[i].Step != step {
			t.Fatalf("deltas %+v, want step %d at %d", deltas, step, i)
		}
	}
}

// TestReadHistoryReadsSummaries is spec raid-walk-addons 2.2: <dir>/<plan>/*/summary.json, oldest first, and a run
// nobody can read is a note.
func TestReadHistoryReadsSummaries(t *testing.T) {
	dir := t.TempDir()
	root := filepath.Join(dir, "onyxias_lair")
	at := time.Date(2026, 10, 29, 20, 0, 0, 0, time.UTC)
	write := func(run string, s *Summary) {
		if err := os.MkdirAll(filepath.Join(root, run), 0o755); err != nil {
			t.Fatal(err)
		}
		raw, err := json.Marshal(s)
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(root, run, "summary.json"), raw, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	late := suiteSum(300, 0, 0, "until")
	late.Started = at.Add(2 * time.Hour)
	early := suiteSum(200, 0, 0, "plan done")
	early.Started = at
	write("run-b-late", late)
	write("run-a-early", early)
	if err := os.MkdirAll(filepath.Join(root, "run-c-broken"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "run-c-broken", "summary.json"), []byte("{"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(root, "run-d-nosummary"), 0o755); err != nil {
		t.Fatal(err)
	}
	hist, notes, err := ReadHistory(dir, "onyxias_lair")
	if err != nil {
		t.Fatal(err)
	}
	if len(hist) != 2 {
		t.Fatalf("history %+v, want the two readable walks", hist)
	}
	if !strings.HasSuffix(hist[0].Path, "/run-a-early/summary.json") || !strings.HasSuffix(hist[1].Path, "/run-b-late/summary.json") {
		t.Fatalf("history order %s, %s: want oldest first", hist[0].Path, hist[1].Path)
	}
	if hist[0].S.Deaths.Total != early.Deaths.Total || hist[1].S.Plan != "onyxias_lair" {
		t.Fatalf("history payload %+v, want the parsed summaries", hist)
	}
	if len(notes) != 2 {
		t.Fatalf("notes %q, want one for the broken file and one for the run without a summary", notes)
	}
	for _, n := range notes {
		if !strings.Contains(n, "run-c-broken") && !strings.Contains(n, "run-d-nosummary") {
			t.Fatalf("note %q names neither unreadable run", n)
		}
	}
	if hist, notes, err := ReadHistory(dir, "no_such_plan"); err != nil || len(hist) != 0 || len(notes) != 0 {
		t.Fatalf("missing plan dir: hist %+v notes %q err %v, want an empty history and no error", hist, notes, err)
	}
}

// TestFirstLegUntil is spec raid-walk-addons 2.2: a boss in the first leg ends the walk there; otherwise the walk runs
// the first leg only, named for the run leader's `until leg <name>`; a plan with one leg runs to its end.
func TestFirstLegUntil(t *testing.T) {
	rows := func(legs []planexport.LegRow, steps ...planexport.StepRow) planexport.Rows {
		return planexport.Rows{Plan: planexport.PlanRow{Plan: "p"}, Legs: legs, Steps: steps}
	}
	bossAt := func(leg, seq int) planexport.StepRow {
		return planexport.StepRow{Leg: leg, Seq: seq, Kind: planexport.KindBoss, Entry: 1}
	}
	move := func(leg, seq int) planexport.StepRow {
		return planexport.StepRow{Leg: leg, Seq: seq, Kind: "move"}
	}
	if got, err := FirstLegUntil(rows([]planexport.LegRow{{Leg: 0, Name: "trash"}, {Leg: 1, Name: "gehennas"}}, move(1, 0), bossAt(0, 7), move(1, 1))); err != nil || got != "boss" {
		t.Fatalf("boss in leg 0: %q, %v; want boss", got, err)
	}
	if got, err := FirstLegUntil(rows([]planexport.LegRow{{Leg: 0, Name: "trash"}, {Leg: 1, Name: "gehennas"}}, move(0, 0), move(1, 0), bossAt(1, 4))); err != nil || got != "leg gehennas" {
		t.Fatalf("boss only in leg 1: %q, %v; want leg gehennas (the first leg, not the last boss)", got, err)
	}
	if got, err := FirstLegUntil(rows([]planexport.LegRow{{Leg: 0, Name: "trash"}}, move(0, 0), move(0, 1))); err != nil || got != "none" {
		t.Fatalf("one leg without a boss: %q, %v; want none", got, err)
	}
	if got, err := FirstLegUntil(rows([]planexport.LegRow{{Leg: 0, Name: "trash"}, {Leg: 1, Name: "Gehennas-2"}}, move(0, 0), move(1, 0))); err == nil || got != "" {
		t.Fatalf("bad leg name: %q, %v; want an error", got, err)
	} else if !strings.Contains(err.Error(), "leg 2 has no name the run leader's until can take") {
		t.Fatalf("bad leg name error %q, want the run leader's until wording", err)
	}
	if got, err := FirstLegUntil(rows([]planexport.LegRow{{Leg: 0, Name: "trash"}, {Leg: 1}}, move(0, 0))); err == nil || got != "" {
		t.Fatalf("leg 1 without a name: %q, %v; want an error", got, err)
	}
	if got, err := FirstLegUntil(planexport.Rows{}); err != nil || got != "none" {
		t.Fatalf("empty rows: %q, %v; want none", got, err)
	}
}
