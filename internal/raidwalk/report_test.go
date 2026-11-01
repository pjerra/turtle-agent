package raidwalk

import (
	"encoding/json"
	"strings"
	"testing"
	"time"
)

// fightJSON is a fight 100 s long that starts at 19:16:50 UTC on 2026-10-30, with a watched cast at 10 s and deaths at 12 s and 16 s.
const reportFight1 = `{"fight":{"id":"3-1","commander":"Cmd","plan":"zul_gurub","leg":"jeklik","step":2,"kind":"move","start":1793387810,"dur_ms":100000,"outcome":"end",
"engaged":[{"e":11350,"n":"Axe Thrower","c":2},{"e":11372,"n":"Adder","c":1}],
"deaths":[{"name":"Rosaline","t_ms":12000,"killer":"Adder"},{"name":"Hazzia","t_ms":16000},{"name":"Late","t_ms":15001}],
"log":[{"k":"cast","t":10000,"sid":24024,"s":"Unstable Concoction","e":14750,"n":"Bat Rider","tn":"Tank"},{"k":"cast","t":20000,"sid":999,"n":"Other"},{"k":"taunt","t":1}]}}`

// reportFight2 is a pull-type fight of step 5 that starts at 19:20:00 and lasts 60 s.
const reportFight2 = `{"id":"3-2","commander":"Cmd","plan":"zul_gurub","step":5,"kind":"pull","start":1793388000,"dur_ms":60000,"outcome":"end","engaged":[{"e":1,"n":"Bat","c":4}],"deaths":[],"log":[]}`

// reportLog is the run log of both fights in a UTC+1 clock (20:16:50 is 19:16:50 UTC).
const reportLog = `20:11:46 14 Hello
20:16:38 2456 TBM:RUN|zul_gurub|running|1/50|jeklik|move|begin|started|until boss (step 9)
20:16:46 2460 TBM:RUN|zul_gurub|running|2/50|jeklik|move|begin|arrived|until boss (step 9)
20:16:52 2465 TBM:RUN|zul_gurub|running|2/50|jeklik|move|walk|fighting: the tank engages 11372 on arrival (0.0 yd)|until boss (step 9)
20:17:30 2466 TBM:RUN|zul_gurub|running|2/50|jeklik|move|walk|fighting: the tank picks up 11351 off the healer (5 yd); a new unit joined: the orders start over|until boss (step 9)
20:18:00 2467 TBM:RUN|zul_gurub|running|3/50|jeklik|move|walk|walking|until boss (step 9)
20:19:00 2468 TBM:RUN|zul_gurub|running|5/50|jeklik|pull|begin|patrol pull: waiting at the stand for 11831 0xf130002e3700bff0 to come within 28.5 yd (now 40.1 yd)|until boss (step 9)
20:19:10 2469 TBM:RUN|zul_gurub|running|5/50|jeklik|pull|begin|patrol pull: waiting at the stand for 11831 0xf130002e3700bff0 to come within 28.5 yd (now 31.0 yd)|until boss (step 9)
20:19:30 2470 TBM:RUN|zul_gurub|running|5/50|jeklik|pull|begin|patrol pull: waiting at the stand for 11831 0xf130002e3700bff0 to come within 28.5 yd (now 29.2 yd)|until boss (step 9)
20:19:50 2471 TBM:RUN|zul_gurub|paused|5/50|jeklik|pull|begin|patrol pull: did not come: pull it, then .bot run resume|until boss (step 9)
20:19:58.250 > .bot run resume
20:19:59 2473 TBM:RUN|zul_gurub|running|5/50|jeklik|pull|pull|pulling 11368 0xf130002c6800bffa (order 1; shot from where it stands); joiners not read|until boss (step 9)
20:20:30 2474 TBM:RUN|zul_gurub|done|6/50|jeklik|move|walk|stopped before step 6 (until boss): the party stays where it stands|until boss (step 9)
`

func reportFixture(t *testing.T, opt ReportOptions) *Report {
	t.Helper()
	f1, err := ParseFight([]byte(reportFight1))
	if err != nil {
		t.Fatal(err)
	}
	f2, err := ParseFight([]byte(reportFight2))
	if err != nil {
		t.Fatal(err)
	}
	lines, err := ParseRunLog(strings.NewReader(reportLog))
	if err != nil {
		t.Fatal(err)
	}
	ResolveLog(lines, time.Hour, unixTime(1793387810))
	return BuildReport([]ReportFight{f2, f1}, lines, opt)
}

func TestParseRunLogBothShapes(t *testing.T) {
	in := "20:16:38.123 TBM:RUN|p|running|1/9|l|move|walk|walking\n20:16:39 77 TBM:RUN|p|running|2/9|l|move|walk|walking\n20:16:40 14 Hello\n20:16:41.500 > .bot run resume\n20:16:42.500 7 dead bots\nnot a line\n99:00:00 x\n"
	ls, err := ParseRunLog(strings.NewReader(in))
	if err != nil {
		t.Fatal(err)
	}
	if len(ls) != 5 {
		t.Fatalf("%d lines: %+v", len(ls), ls)
	}
	if ls[0].Status == nil || ls[0].Status.Step != 1 || ls[0].TOD != 20*time.Hour+16*time.Minute+38*time.Second+123*time.Millisecond {
		t.Errorf("raid-walk shape: %+v", ls[0])
	}
	if ls[1].Status == nil || ls[1].Status.Step != 2 {
		t.Errorf("event log shape (a sequence number first): %+v", ls[1])
	}
	if ls[2].Status != nil || ls[2].Text != "Hello" {
		t.Errorf("a chat line of the event log loses its sequence number: %+v", ls[2])
	}
	if ls[3].Text != "> .bot run resume" || ls[4].Text != "7 dead bots" {
		t.Errorf("a command line, and a note that starts with a number: %+v %+v", ls[3], ls[4])
	}
}

func TestResolveLogDatesAndWraps(t *testing.T) {
	// 23:50 and 00:10 in a UTC+1 clock are 22:50 and 23:10 UTC; the first fight is at 22:55 UTC on 2026-10-30.
	first := time.Date(2026, 10, 30, 22, 55, 0, 0, time.UTC)
	ls := []LogLine{{TOD: 23*time.Hour + 50*time.Minute}, {TOD: 10 * time.Minute}, {TOD: 30 * time.Minute}}
	ResolveLog(ls, time.Hour, first)
	want := []time.Time{time.Date(2026, 10, 30, 22, 50, 0, 0, time.UTC), time.Date(2026, 10, 30, 23, 10, 0, 0, time.UTC), time.Date(2026, 10, 30, 23, 30, 0, 0, time.UTC)}
	for i := range ls {
		if !ls[i].At.Equal(want[i]) {
			t.Errorf("line %d at %s, want %s", i, ls[i].At, want[i])
		}
	}
	// a log that starts the evening before the fight, in a clock behind midnight
	ls = []LogLine{{TOD: 23*time.Hour + 58*time.Minute}, {TOD: 5 * time.Minute}}
	ResolveLog(ls, 0, time.Date(2026, 10, 31, 0, 4, 0, 0, time.UTC))
	if !ls[0].At.Equal(time.Date(2026, 10, 30, 23, 58, 0, 0, time.UTC)) || !ls[1].At.Equal(time.Date(2026, 10, 31, 0, 5, 0, 0, time.UTC)) {
		t.Errorf("midnight: %s, %s", ls[0].At, ls[1].At)
	}
}

func TestReportPullsContactJoinsAndDeaths(t *testing.T) {
	rep := reportFixture(t, ReportOptions{})
	if len(rep.Pulls) != 2 || rep.Pulls[0].Fight != "3-1" || rep.Pulls[1].Fight != "3-2" {
		t.Fatalf("pulls in start order: %+v", rep.Pulls)
	}
	p := rep.Pulls[0]
	if p.Start.Format("15:04:05") != "19:16:50" || p.DurS != 100 || p.Step != 2 {
		t.Errorf("start %s dur %v step %d", p.Start, p.DurS, p.Step)
	}
	if len(p.Engaged) != 2 || p.Engaged[0].Name != "Axe Thrower" || p.Engaged[0].Count != 2 {
		t.Errorf("engaged %+v", p.Engaged)
	}
	if len(p.Deaths) != 3 || p.Deaths[0].Name != "Rosaline" || p.Deaths[1].Name != "Late" || p.Deaths[0].Killer != "Adder" {
		t.Errorf("deaths sorted by time: %+v", p.Deaths)
	}
	if p.ContactBeforePull == nil || !*p.ContactBeforePull || !strings.Contains(p.ContactText, "engages 11372 on arrival") {
		t.Errorf("contact before pull: %v %q", p.ContactBeforePull, p.ContactText)
	}
	if len(p.Joins) != 1 || p.Joins[0].TS != 40 {
		t.Errorf("the neighbour joined 40 s into the fight (20:17:30 in UTC+1 against a 19:16:50 start): %+v", p.Joins)
	}
	if rep.Totals.Fights != 2 || rep.Totals.Deaths != 3 || rep.Totals.ContactBeforePull != 1 || rep.Totals.FightsWithJoins != 1 {
		t.Errorf("totals %+v", rep.Totals)
	}
	// a pull whose "pulling" line comes first is no contact before the pull order
	q := rep.Pulls[1]
	if q.ContactBeforePull == nil || *q.ContactBeforePull {
		t.Errorf("pull 3-2 contact: %v", q.ContactBeforePull)
	}
	if q.PullLineAt.Format("15:04:05") != "19:19:59" {
		t.Errorf("pull line at %s", q.PullLineAt)
	}
}

func TestReportStandPullWaits(t *testing.T) {
	rep := reportFixture(t, ReportOptions{})
	q := rep.Pulls[1]
	if len(q.StandRuns) != 3 { // the waiting text merged, the pause line, the pulling line
		t.Fatalf("stand runs: %+v", q.StandRuns)
	}
	w := q.StandRuns[0]
	if w.Count != 3 || w.Runs != 1 || w.WaitS != 30 || w.Step != 5 || !strings.Contains(w.Text, "waiting at the stand for # # to come within # yd (now # yd)") {
		t.Errorf("waiting row %+v", w)
	}
	// the wait is from the first waiting line (20:19:00) to the pulling line (20:19:59)
	if q.StandWaitS != 59 {
		t.Errorf("stand wait %v, want 59", q.StandWaitS)
	}
	if rep.Pulls[0].StandWaitS != 0 || len(rep.Pulls[0].StandRuns) != 0 {
		t.Errorf("pull 3-1 has no stand lines: %+v", rep.Pulls[0].StandRuns)
	}
}

func TestReportControlEvents(t *testing.T) {
	rep := reportFixture(t, ReportOptions{})
	kinds := []string{}
	for _, c := range rep.Control {
		kinds = append(kinds, c.Kind)
	}
	if strings.Join(kinds, " ") != "pause sent resume stop" {
		t.Fatalf("control %v", rep.Control)
	}
	if rep.Control[2].PausedS != 9 || rep.Control[1].Note != ".bot run resume" || !strings.HasPrefix(rep.Control[3].Note, "stopped before step 6") {
		t.Errorf("control details %+v", rep.Control)
	}
	if rep.Totals.Pauses != 1 || rep.Totals.Resumes != 1 || rep.Totals.Stops != 1 {
		t.Errorf("totals %+v", rep.Totals)
	}
}

func TestReportWatchedCasts(t *testing.T) {
	rep := reportFixture(t, ReportOptions{Spells: []uint32{24024}})
	if len(rep.Casts) != 1 {
		t.Fatalf("casts %+v", rep.Casts)
	}
	c := rep.Casts[0]
	// the cast is at 10 s: the deaths at 12 s and 15.001 s are within the default 5 s window (to 15 s) only for the first
	if c.Caster != "Bat Rider" || c.SpellName != "Unstable Concoction" || c.TS != 10 || len(c.DeathsWithin) != 1 || c.DeathsWithin[0] != "Rosaline" {
		t.Errorf("cast %+v", c)
	}
	if c.At.Format("15:04:05") != "19:17:00" {
		t.Errorf("cast at %s", c.At)
	}
	wide := reportFixture(t, ReportOptions{Spells: []uint32{24024}, Window: 6 * time.Second})
	if got := wide.Casts[0].DeathsWithin; len(got) != 3 {
		t.Errorf("a 6 s window holds all three deaths: %v", got)
	}
	if rep := reportFixture(t, ReportOptions{}); len(rep.Casts) != 0 {
		t.Errorf("no spell asked, no cast row: %+v", rep.Casts)
	}
}

func TestReportWithoutRunLog(t *testing.T) {
	f1, _ := ParseFight([]byte(reportFight1))
	rep := BuildReport([]ReportFight{f1}, nil, ReportOptions{})
	if rep.HasRunLog || rep.Pulls[0].ContactBeforePull != nil || len(rep.Notes) != 1 {
		t.Fatalf("%+v", rep)
	}
	if md := rep.Markdown(); !strings.Contains(md, "| 2 | 3-1 (move) |") || !strings.Contains(md, "no run log") {
		t.Errorf("markdown:\n%s", md)
	}
}

func TestReportMarkdownAndJSON(t *testing.T) {
	rep := reportFixture(t, ReportOptions{Spells: []uint32{24024}, ClockOffset: time.Hour})
	md := rep.Markdown()
	for _, want := range []string{
		"# Raid walk report: zul_gurub",
		"| 2 | 3-1 (move) | 19:16:50 | 100s | end | Axe Thrower x2, Adder | 3 (Rosaline 12s, Late 15s, Hazzia 16s) | YES: fighting: the tank engages 11372 on arrival (0.0 yd) | 1 (+40s) | - |",
		"| 5 | 3-2 (pull) | 19:20:00 |",
		"## Stand pulls",
		"## Pauses, stops and resumes",
		"| 19:19:50 | 5 | pause |",
		"| 19:19:59 | 5 | resume (paused 9s) |",
		"## Casts of spell 24024 (deaths within 5s)",
		"| 3-1 | 2 | 19:17:00 | 10.0s | Bat Rider | Unstable Concoction (24024) | 1 (Rosaline) |",
	} {
		if !strings.Contains(md, want) {
			t.Errorf("markdown lacks %q:\n%s", want, md)
		}
	}
	raw, err := rep.JSON()
	if err != nil {
		t.Fatal(err)
	}
	var back Report
	if err := json.Unmarshal(raw, &back); err != nil {
		t.Fatal(err)
	}
	if back.Format != ReportFormat || back.Version != 1 || len(back.Pulls) != 2 || len(back.Casts) != 1 || back.ClockOffset != "1h0m0s" {
		t.Errorf("json round trip: %+v", back)
	}
}

func TestParseFightSet(t *testing.T) {
	m, err := ParseFightSet([]byte(`{"3-2":` + reportFight2 + `,"3-1":` + reportFight1 + `}`))
	if err != nil || len(m) != 2 {
		t.Fatalf("map: %v %d", err, len(m))
	}
	l, err := ParseFightSet([]byte(`[` + reportFight2 + `,` + reportFight1 + `]`))
	if err != nil || len(l) != 2 {
		t.Fatalf("list: %v %d", err, len(l))
	}
	if _, err := ParseFight([]byte(`{"nothing":1}`)); err == nil {
		t.Error("a file without an id was taken as a fight")
	}
}
