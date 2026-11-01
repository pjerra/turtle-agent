package raidwalk

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/pjerra/turtle-agent/internal/tbm"
)

// The walk report (`turtle-agent raid-walk report <dir>`): the per-pull table the raid walks keep making by hand. It reads what a walk
// saved (the dashboard's fights and the run log of TBM:RUN lines) and writes nothing to a server. This file is the pure part: parsing,
// the join of fights and status lines, and the Markdown and JSON texts. cmd/turtle-agent/raid_walk_report_cmd.go reads the folder.

// ReportFormat and ReportVersion name the JSON.
const (
	ReportFormat  = "tb-raid-walk-report"
	ReportVersion = 1
)

// DefaultCastWindow is how long after a watched cast a death still counts as its death.
const DefaultCastWindow = 5 * time.Second

// ReportFight is the part of a dashboard fight (GET /api/v1/combat/fight/<key>, the "fight" object) the report reads.
type ReportFight struct {
	ID        string  `json:"id"`
	Commander string  `json:"commander"`
	Plan      string  `json:"plan"`
	Leg       string  `json:"leg"`
	Step      int     `json:"step"`
	Kind      string  `json:"kind"`
	Start     float64 `json:"start"` // unix seconds, UTC
	DurMS     int64   `json:"dur_ms"`
	Outcome   string  `json:"outcome"`
	BossName  string  `json:"boss_name"`
	Engaged   []struct {
		Entry uint32 `json:"e"`
		Name  string `json:"n"`
		Count int    `json:"c"`
	} `json:"engaged"`
	Deaths []struct {
		Name   string `json:"name"`
		TMS    int64  `json:"t_ms"`
		Killer string `json:"killer"`
	} `json:"deaths"`
	Log []struct {
		Kind   string `json:"k"`
		TMS    int64  `json:"t"`
		SpellI uint32 `json:"sid"`
		Spell  string `json:"s"`
		Entry  uint32 `json:"e"`
		Caster string `json:"n"`
		Target string `json:"tn"`
	} `json:"log"`
}

// ParseFight reads one fight file: the dashboard's {"fight": {...}} reply, or the bare fight.
func ParseFight(raw []byte) (ReportFight, error) {
	var wrapped struct {
		Fight *ReportFight `json:"fight"`
	}
	if err := json.Unmarshal(raw, &wrapped); err != nil {
		return ReportFight{}, err
	}
	if wrapped.Fight != nil {
		return *wrapped.Fight, nil
	}
	var f ReportFight
	if err := json.Unmarshal(raw, &f); err != nil {
		return ReportFight{}, err
	}
	if f.ID == "" {
		return ReportFight{}, errors.New("not a fight (no id)")
	}
	return f, nil
}

// ParseFightSet reads a file that holds many fights: an object id -> fight (the hand-made fights-full.json) or a list of fights.
func ParseFightSet(raw []byte) ([]ReportFight, error) {
	var asMap map[string]json.RawMessage
	if err := json.Unmarshal(raw, &asMap); err == nil {
		var out []ReportFight
		for _, v := range asMap {
			f, err := ParseFight(v)
			if err != nil {
				return nil, err
			}
			out = append(out, f)
		}
		return out, nil
	}
	var list []json.RawMessage
	if err := json.Unmarshal(raw, &list); err != nil {
		return nil, err
	}
	out := make([]ReportFight, 0, len(list))
	for _, v := range list {
		f, err := ParseFight(v)
		if err != nil {
			return nil, err
		}
		out = append(out, f)
	}
	return out, nil
}

// LogLine is one line of the run log: its clock time of day, and the text after it.
type LogLine struct {
	// At is the line's time in UTC once ResolveLog has dated it.
	At time.Time
	// TOD is the time of day as written, in the log's own clock.
	TOD  time.Duration
	Text string
	// Status is the TBM:RUN status of the line, when it is one.
	Status *tbm.RunStatus
}

var logLineRe = regexp.MustCompile(`^(\d{2}):(\d{2}):(\d{2})(?:\.(\d{3}))?\s+(?:(\d+)\s+)?(.*)$`)

// ParseRunLog reads a run log: `HH:MM:SS.mmm <text>` as raid-walk writes it, or `HH:MM:SS <seq> <text>` as a hand-made event log
// does (the sequence number is dropped). Lines that are neither are skipped. The lines are not dated yet (ResolveLog).
func ParseRunLog(r io.Reader) ([]LogLine, error) {
	var out []LogLine
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 0, 64*1024), 4*1024*1024)
	for sc.Scan() {
		m := logLineRe.FindStringSubmatch(sc.Text())
		if m == nil {
			continue
		}
		h, _ := strconv.Atoi(m[1])
		mi, _ := strconv.Atoi(m[2])
		s, _ := strconv.Atoi(m[3])
		ms := 0
		if m[4] != "" {
			ms, _ = strconv.Atoi(m[4])
		}
		if h > 23 || mi > 59 || s > 59 {
			continue
		}
		text := m[6]
		if m[5] != "" && m[4] != "" {
			// "HH:MM:SS.mmm <number> ...": raid-walk's own lines carry milliseconds and no sequence number, so the number is text
			text = m[5] + " " + text
		}
		l := LogLine{TOD: time.Duration(h)*time.Hour + time.Duration(mi)*time.Minute + time.Duration(s)*time.Second + time.Duration(ms)*time.Millisecond, Text: text}
		if st, ok := ParseRun(text); ok {
			l.Status = &st
		}
		out = append(out, l)
	}
	return out, sc.Err()
}

// ResolveLog dates the lines. The log carries a time of day in the clock of the machine or server that wrote it (clockOffset ahead of
// UTC, +1h on the servers) and no date: the day is the latest one that puts the first line no more than 5 minutes after the first
// fight, and a time of day that jumps back by more than 12 hours starts the next day. firstFight is the earliest fight start in UTC.
func ResolveLog(lines []LogLine, clockOffset time.Duration, firstFight time.Time) {
	if len(lines) == 0 {
		return
	}
	day := firstFight.UTC().Truncate(24 * time.Hour)
	at := func(d time.Time, tod time.Duration) time.Time { return d.Add(tod - clockOffset) }
	limit := firstFight.Add(5 * time.Minute)
	for at(day, lines[0].TOD).After(limit) {
		day = day.Add(-24 * time.Hour)
	}
	for !at(day.Add(24*time.Hour), lines[0].TOD).After(limit) {
		day = day.Add(24 * time.Hour)
	}
	prev := lines[0].TOD
	for i := range lines {
		if lines[i].TOD < prev-12*time.Hour {
			day = day.Add(24 * time.Hour)
		}
		prev = lines[i].TOD
		lines[i].At = at(day, lines[i].TOD)
	}
}

// ReportOptions are the choices of a report.
type ReportOptions struct {
	// Spells are the enemy spell ids whose casts get a row each, with the deaths within Window.
	Spells []uint32
	// Window is the death window of a watched cast (DefaultCastWindow when zero).
	Window time.Duration
	// ClockOffset is how far the run log's clock is ahead of UTC.
	ClockOffset time.Duration
}

// Report is the JSON of a walk report.
type Report struct {
	Format      string         `json:"format"`
	Version     int            `json:"version"`
	Source      string         `json:"source,omitempty"`
	Plan        string         `json:"plan,omitempty"`
	Commander   string         `json:"commander,omitempty"`
	ClockOffset string         `json:"log_clock_offset"`
	HasRunLog   bool           `json:"has_run_log"`
	Spells      []uint32       `json:"spells,omitempty"`
	WindowS     float64        `json:"cast_window_s,omitempty"`
	Pulls       []ReportPull   `json:"pulls"`
	Control     []ControlEvent `json:"control"`
	Casts       []ReportCast   `json:"casts,omitempty"`
	StandOther  []StandRun     `json:"stand_pull_unattached,omitempty"`
	Totals      ReportTotals   `json:"totals"`
	Notes       []string       `json:"notes,omitempty"`
}

// ReportTotals sums the table.
type ReportTotals struct {
	Fights             int `json:"fights"`
	Deaths             int `json:"deaths"`
	ContactBeforePull  int `json:"contact_before_pull"`
	FightsWithJoins    int `json:"fights_with_joins"`
	Pauses             int `json:"pauses"`
	Resumes            int `json:"resumes"`
	Stops              int `json:"stops"`
	WatchedSpellCasts  int `json:"watched_spell_casts"`
	WatchedCastsDeaths int `json:"watched_cast_deaths"`
}

// ReportUnit is one engaged enemy kind.
type ReportUnit struct {
	Entry uint32 `json:"entry"`
	Name  string `json:"name"`
	Count int    `json:"count"`
}

// ReportDeath is one death of a fight.
type ReportDeath struct {
	Name   string  `json:"name"`
	TS     float64 `json:"t_s"`
	Killer string  `json:"killer,omitempty"`
}

// ReportJoin is a "a new unit joined" status line inside a fight.
type ReportJoin struct {
	TS   float64   `json:"t_s"` // seconds into the fight
	At   time.Time `json:"at"`
	Text string    `json:"text"`
}

// StandRun is the stand-pull status line of one text (digits and guids normalised) in a pull's window: the first and last time it was
// shown, how many status lines that was, in how many separate runs, and the span between the first and the last (the wait).
type StandRun struct {
	Step  int       `json:"step"`
	Text  string    `json:"text"`
	From  time.Time `json:"from"`
	To    time.Time `json:"to"`
	Count int       `json:"count"`
	Runs  int       `json:"runs"`
	WaitS float64   `json:"wait_s"`
}

// mergeStand folds runs of the same step and text into one row (first from, last to, summed lines and runs), in order of first sight.
func mergeStand(runs []StandRun) []StandRun {
	var out []StandRun
	at := map[string]int{}
	for _, r := range runs {
		key := fmt.Sprintf("%d|%s", r.Step, r.Text)
		i, ok := at[key]
		if !ok {
			at[key] = len(out)
			out = append(out, r)
			continue
		}
		m := &out[i]
		if r.From.Before(m.From) {
			m.From = r.From
		}
		if r.To.After(m.To) {
			m.To = r.To
		}
		m.Count += r.Count
		m.Runs += r.Runs
		m.WaitS = m.To.Sub(m.From).Seconds()
	}
	return out
}

// ReportPull is one row of the per-pull table.
type ReportPull struct {
	Fight   string        `json:"fight"`
	Step    int           `json:"step"`
	Kind    string        `json:"kind,omitempty"`
	Leg     string        `json:"leg,omitempty"`
	Start   time.Time     `json:"start"`
	DurS    float64       `json:"dur_s"`
	Outcome string        `json:"outcome,omitempty"`
	Boss    string        `json:"boss,omitempty"`
	Engaged []ReportUnit  `json:"engaged"`
	Deaths  []ReportDeath `json:"deaths"`
	// ContactBeforePull: the status said the tank engaged on arrival before any "pulling" line of the step (nil: no run log).
	ContactBeforePull *bool        `json:"contact_before_pull"`
	ContactText       string       `json:"contact_text,omitempty"`
	ContactAt         time.Time    `json:"contact_at,omitempty"`
	PullLineAt        time.Time    `json:"pull_line_at,omitempty"`
	Joins             []ReportJoin `json:"neighbour_joins"`
	JoinersExpected   string       `json:"joiners_expected,omitempty"`
	// StandWaitS is the time from the first stand-pull status line of the pull to its "pulling" line (to the fight's start without one).
	StandWaitS float64      `json:"stand_wait_s"`
	StandRuns  []StandRun   `json:"stand_pull"`
	Casts      []ReportCast `json:"casts,omitempty"`
}

// ReportCast is one cast of a watched spell.
type ReportCast struct {
	Fight        string    `json:"fight"`
	Step         int       `json:"step"`
	Spell        uint32    `json:"spell"`
	SpellName    string    `json:"spell_name,omitempty"`
	TS           float64   `json:"t_s"`
	At           time.Time `json:"at"`
	Caster       string    `json:"caster"`
	Entry        uint32    `json:"entry,omitempty"`
	Target       string    `json:"target,omitempty"`
	DeathsWithin []string  `json:"deaths_within"`
}

// ControlEvent is a pause, resume, stop or end of the run, from the status lines (and the commands the walk logged).
type ControlEvent struct {
	At   time.Time `json:"at"`
	Step int       `json:"step"`
	Kind string    `json:"kind"` // pause, resume, stop, done, sent
	Note string    `json:"note,omitempty"`
	// PausedS is, on a resume, how long the pause lasted.
	PausedS float64 `json:"paused_s,omitempty"`
}

var (
	onArrivalRe = regexp.MustCompile(`(?i)\bengages\b.*\bon arrival\b`)
	pullingRe   = regexp.MustCompile(`(?:^|;\s+)pulling\b`)
	joinedRe    = regexp.MustCompile(`(?i)a new unit joined`)
	joinersRe   = regexp.MustCompile(`(?i)\b\d+ joiners expected`)
	standRe     = regexp.MustCompile(`(?i)(waiting at the stand|waiting for the patroller|stand still before pulling|patrol pull|back to the stand|joiners|shot from (where it stands|firing stand))`)
	normRe      = regexp.MustCompile(`0x[0-9a-fA-F]+|\d+(\.\d+)?`)
	untilTail   = regexp.MustCompile(`\|until .*$`)
	sentCmdRe   = regexp.MustCompile(`^> \.bot run (stop|pause|resume)\b(.*)$`)
)

// statusNote is a status line's note without the run leader's trailing `|until ...` label.
func statusNote(st *tbm.RunStatus) string { return untilTail.ReplaceAllString(st.Note, "") }

// BuildReport joins the fights with the run log (nil or empty: the table has no status columns).
func BuildReport(fights []ReportFight, log []LogLine, opt ReportOptions) *Report {
	if opt.Window <= 0 {
		opt.Window = DefaultCastWindow
	}
	sort.SliceStable(fights, func(i, j int) bool {
		if fights[i].Start != fights[j].Start {
			return fights[i].Start < fights[j].Start
		}
		return fights[i].ID < fights[j].ID
	})
	rep := &Report{Format: ReportFormat, Version: ReportVersion, ClockOffset: opt.ClockOffset.String(), HasRunLog: len(log) > 0,
		Spells: opt.Spells, Pulls: []ReportPull{}, Control: []ControlEvent{}}
	if len(opt.Spells) > 0 {
		rep.WindowS = opt.Window.Seconds()
	}
	if len(fights) > 0 {
		rep.Plan, rep.Commander = fights[0].Plan, fights[0].Commander
	}
	spells := map[uint32]bool{}
	for _, s := range opt.Spells {
		spells[s] = true
	}

	// The fights of one step, in order: each one's status window starts where the one before ended.
	wins := make([]fightWindow, len(fights))
	lastEnd := map[int]time.Time{}
	for i, f := range fights {
		start := unixTime(f.Start)
		end := start.Add(time.Duration(f.DurMS) * time.Millisecond)
		from := time.Time{}
		if p, ok := lastEnd[f.Step]; ok {
			from = p
		}
		wins[i] = fightWindow{from, end}
		lastEnd[f.Step] = end
	}

	for i, f := range fights {
		start := unixTime(f.Start)
		p := ReportPull{Fight: f.ID, Step: f.Step, Kind: f.Kind, Leg: f.Leg, Start: start, DurS: float64(f.DurMS) / 1000, Outcome: f.Outcome, Boss: f.BossName,
			Engaged: []ReportUnit{}, Deaths: []ReportDeath{}, Joins: []ReportJoin{}, StandRuns: []StandRun{}}
		for _, e := range f.Engaged {
			p.Engaged = append(p.Engaged, ReportUnit{Entry: e.Entry, Name: e.Name, Count: e.Count})
		}
		deaths := make([]ReportDeath, 0, len(f.Deaths))
		for _, d := range f.Deaths {
			deaths = append(deaths, ReportDeath{Name: d.Name, TS: float64(d.TMS) / 1000, Killer: d.Killer})
		}
		sort.SliceStable(deaths, func(a, b int) bool { return deaths[a].TS < deaths[b].TS })
		p.Deaths = deaths
		rep.Totals.Fights++
		rep.Totals.Deaths += len(deaths)

		for _, e := range f.Log {
			if e.Kind != "cast" || !spells[e.SpellI] {
				continue
			}
			c := ReportCast{Fight: f.ID, Step: f.Step, Spell: e.SpellI, SpellName: e.Spell, TS: float64(e.TMS) / 1000,
				At: start.Add(time.Duration(e.TMS) * time.Millisecond), Caster: e.Caster, Entry: e.Entry, Target: e.Target, DeathsWithin: []string{}}
			for _, d := range f.Deaths {
				if d.TMS >= e.TMS && d.TMS <= e.TMS+opt.Window.Milliseconds() {
					c.DeathsWithin = append(c.DeathsWithin, d.Name)
				}
			}
			p.Casts = append(p.Casts, c)
			rep.Casts = append(rep.Casts, c)
			rep.Totals.WatchedSpellCasts++
			rep.Totals.WatchedCastsDeaths += len(c.DeathsWithin)
		}

		if len(log) > 0 {
			fillFromLog(&p, log, wins[i].from, start, wins[i].to)
			if p.ContactBeforePull != nil && *p.ContactBeforePull {
				rep.Totals.ContactBeforePull++
			}
			if len(p.Joins) > 0 {
				rep.Totals.FightsWithJoins++
			}
		}
		rep.Pulls = append(rep.Pulls, p)
	}

	if len(log) > 0 {
		attachStand(rep, fights, log, wins)
		rep.Control = controlEvents(log)
		for _, c := range rep.Control {
			switch c.Kind {
			case "pause":
				rep.Totals.Pauses++
			case "resume":
				rep.Totals.Resumes++
			case "stop":
				rep.Totals.Stops++
			}
		}
	} else {
		rep.Notes = append(rep.Notes, "no run log: the contact, join, stand-pull and control columns are empty")
	}
	return rep
}

// fightWindow is the span of status lines a pull owns: after the end of the step's earlier fight (zero: from the start) up to its own end.
type fightWindow struct{ from, to time.Time }

func unixTime(s float64) time.Time {
	sec := int64(s)
	return time.Unix(sec, int64((s-float64(sec))*1e9)).UTC()
}

// fillFromLog sets the contact, join and joiners columns of one pull from the status lines of its step in the window (from, end].
func fillFromLog(p *ReportPull, log []LogLine, from, start, end time.Time) {
	var arrival, pull *LogLine
	contactAt := time.Time{}
	for i := range log {
		l := &log[i]
		if l.Status == nil || l.Status.Step != p.Step {
			continue
		}
		if !from.IsZero() && !l.At.After(from) {
			continue
		}
		if l.At.After(end.Add(time.Second)) {
			break
		}
		note := statusNote(l.Status)
		switch {
		case onArrivalRe.MatchString(note):
			if arrival == nil {
				arrival, contactAt = l, l.At
			}
		case pullingRe.MatchString(note):
			if pull == nil {
				pull = l
			}
		}
		if !l.At.Before(start.Add(-time.Second)) {
			if joinedRe.MatchString(note) {
				p.Joins = append(p.Joins, ReportJoin{TS: l.At.Sub(start).Seconds(), At: l.At, Text: note})
			}
			if joinersRe.MatchString(note) && p.JoinersExpected == "" {
				p.JoinersExpected = joinersRe.FindString(note)
			}
		}
	}
	if arrival != nil || pull != nil {
		b := arrival != nil && (pull == nil || !pull.At.Before(contactAt))
		p.ContactBeforePull = &b
		if arrival != nil {
			p.ContactText, p.ContactAt = statusNote(arrival.Status), arrival.At
		}
		if pull != nil {
			p.PullLineAt = pull.At
		}
	} else {
		b := false
		p.ContactBeforePull = &b
	}
}

// attachStand groups the stand-pull status lines into runs and hangs each on the pull of its step whose window holds it.
func attachStand(rep *Report, fights []ReportFight, log []LogLine, wins []fightWindow) {
	var runs []StandRun
	var cur *StandRun
	curNorm := ""
	flush := func() {
		if cur != nil {
			cur.WaitS = cur.To.Sub(cur.From).Seconds()
			cur.Runs = 1
			runs = append(runs, *cur)
			cur = nil
		}
	}
	for i := range log {
		st := log[i].Status
		if st == nil {
			continue
		}
		note := statusNote(st)
		norm := normRe.ReplaceAllString(note, "#")
		if cur != nil && cur.Step == st.Step && curNorm == norm {
			cur.To = log[i].At
			cur.Count++
			continue
		}
		flush()
		if standRe.MatchString(note) {
			cur = &StandRun{Step: st.Step, Text: norm, From: log[i].At, To: log[i].At, Count: 1}
			curNorm = norm
		}
	}
	flush()
	for _, r := range runs {
		idx := -1
		for i, f := range fights {
			if f.Step != r.Step {
				continue
			}
			w := wins[i]
			if (w.from.IsZero() || r.From.After(w.from)) && !r.From.After(w.to) {
				idx = i
				break
			}
		}
		if idx < 0 {
			rep.StandOther = append(rep.StandOther, r)
			continue
		}
		rep.Pulls[idx].StandRuns = append(rep.Pulls[idx].StandRuns, r)
	}
	rep.StandOther = mergeStand(rep.StandOther)
	for i := range rep.Pulls {
		p := &rep.Pulls[i]
		if len(p.StandRuns) == 0 {
			continue
		}
		first := p.StandRuns[0].From
		for _, r := range p.StandRuns {
			if r.From.Before(first) {
				first = r.From
			}
		}
		p.StandRuns = mergeStand(p.StandRuns)
		end := p.Start
		if !p.PullLineAt.IsZero() && p.PullLineAt.After(first) {
			end = p.PullLineAt
		}
		if w := end.Sub(first).Seconds(); w > 0 {
			p.StandWaitS = w
		}
	}
}

// controlEvents lists the pauses, resumes, stops and the end of the run, in log order.
func controlEvents(log []LogLine) []ControlEvent {
	out := []ControlEvent{}
	state := ""
	var pausedAt time.Time
	for i := range log {
		l := &log[i]
		if m := sentCmdRe.FindStringSubmatch(l.Text); m != nil {
			out = append(out, ControlEvent{At: l.At, Kind: "sent", Note: strings.TrimSpace(".bot run " + m[1] + m[2])})
			continue
		}
		st := l.Status
		if st == nil || st.State == state {
			continue
		}
		note := statusNote(st)
		switch st.State {
		case tbm.RunPaused:
			out = append(out, ControlEvent{At: l.At, Step: st.Step, Kind: "pause", Note: note})
			pausedAt = l.At
		case tbm.RunRunning:
			if state == tbm.RunPaused {
				out = append(out, ControlEvent{At: l.At, Step: st.Step, Kind: "resume", Note: note, PausedS: l.At.Sub(pausedAt).Seconds()})
			}
		case tbm.RunDone:
			kind := "done"
			if strings.HasPrefix(st.Note, "stopped") {
				kind = "stop"
			}
			out = append(out, ControlEvent{At: l.At, Step: st.Step, Kind: kind, Note: note})
		}
		state = st.State
	}
	return out
}
