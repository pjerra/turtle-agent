package raidwalk

import (
	"encoding/json"
	"fmt"
	"strings"
	"time"
)

// JSON is the report as indented JSON.
func (r *Report) JSON() ([]byte, error) {
	b, err := json.MarshalIndent(r, "", "  ")
	if err != nil {
		return nil, err
	}
	return append(b, '\n'), nil
}

func clock(t time.Time) string {
	if t.IsZero() {
		return "-"
	}
	return t.UTC().Format("15:04:05")
}

func mdCell(s string) string {
	s = strings.ReplaceAll(s, "|", "/")
	return strings.ReplaceAll(s, "\n", " ")
}

func unitsText(us []ReportUnit) string {
	if len(us) == 0 {
		return "-"
	}
	parts := make([]string, len(us))
	for i, u := range us {
		parts[i] = u.Name
		if u.Count > 1 {
			parts[i] += fmt.Sprintf(" x%d", u.Count)
		}
	}
	return strings.Join(parts, ", ")
}

func deathsText(ds []ReportDeath) string {
	if len(ds) == 0 {
		return "0"
	}
	names := make([]string, 0, len(ds))
	for i, d := range ds {
		if i == 6 {
			names = append(names, fmt.Sprintf("+%d more", len(ds)-6))
			break
		}
		names = append(names, fmt.Sprintf("%s %.0fs", d.Name, d.TS))
	}
	return fmt.Sprintf("%d (%s)", len(ds), strings.Join(names, ", "))
}

// Markdown is the report as Markdown: the per-pull table, then the stand-pull waits, the pauses, stops and resumes and the watched casts.
func (r *Report) Markdown() string {
	var b strings.Builder
	title := "Raid walk report"
	if r.Plan != "" {
		title += ": " + r.Plan
	}
	fmt.Fprintf(&b, "# %s\n\n", title)
	fmt.Fprintf(&b, "Times are UTC (the run log's clock is read as UTC%+.0fh and converted). %d fights, %d deaths", r.clockHours(), r.Totals.Fights, r.Totals.Deaths)
	if r.HasRunLog {
		fmt.Fprintf(&b, ", %d pulls with contact before the pull order, %d with a neighbour joining, %d pauses, %d resumes, %d stops", r.Totals.ContactBeforePull, r.Totals.FightsWithJoins, r.Totals.Pauses, r.Totals.Resumes, r.Totals.Stops)
	}
	b.WriteString(".\n\n")
	for _, n := range r.Notes {
		fmt.Fprintf(&b, "- %s\n", n)
	}
	if len(r.Notes) > 0 {
		b.WriteString("\n")
	}

	b.WriteString("## Pulls\n\n")
	b.WriteString("| step | fight | start | dur | outcome | engaged | deaths | contact before pull | joined mid-fight | stand wait |\n")
	b.WriteString("| ---: | --- | --- | ---: | --- | --- | --- | --- | --- | ---: |\n")
	for _, p := range r.Pulls {
		contact := "-"
		if p.ContactBeforePull != nil {
			contact = "no"
			if *p.ContactBeforePull {
				contact = "YES: " + p.ContactText
			}
		}
		joins := "-"
		if len(p.Joins) > 0 {
			js := make([]string, len(p.Joins))
			for i, j := range p.Joins {
				js[i] = fmt.Sprintf("+%.0fs", j.TS)
			}
			joins = fmt.Sprintf("%d (%s)", len(p.Joins), strings.Join(js, ", "))
		}
		if p.JoinersExpected != "" {
			joins += "; " + p.JoinersExpected
		}
		stand := "-"
		if len(p.StandRuns) > 0 {
			stand = fmt.Sprintf("%.0fs", p.StandWaitS)
		}
		kind := p.Kind
		if p.Boss != "" {
			kind += " " + p.Boss
		}
		fmt.Fprintf(&b, "| %d | %s (%s) | %s | %.0fs | %s | %s | %s | %s | %s | %s |\n", p.Step, mdCell(p.Fight), mdCell(kind), clock(p.Start), p.DurS, mdCell(p.Outcome),
			mdCell(unitsText(p.Engaged)), mdCell(deathsText(p.Deaths)), mdCell(contact), mdCell(joins), stand)
	}

	if r.HasRunLog {
		b.WriteString("\n## Stand pulls\n\n")
		any := false
		for _, p := range r.Pulls {
			for _, s := range p.StandRuns {
				if !any {
					b.WriteString("| step | fight | first | last | span | lines (runs) | text |\n| ---: | --- | --- | --- | ---: | ---: | --- |\n")
					any = true
				}
				fmt.Fprintf(&b, "| %d | %s | %s | %s | %.0fs | %d (%d) | %s |\n", s.Step, mdCell(p.Fight), clock(s.From), clock(s.To), s.WaitS, s.Count, s.Runs, mdCell(s.Text))
			}
		}
		for _, s := range r.StandOther {
			if !any {
				b.WriteString("| step | fight | first | last | span | lines (runs) | text |\n| ---: | --- | --- | --- | ---: | ---: | --- |\n")
				any = true
			}
			fmt.Fprintf(&b, "| %d | - | %s | %s | %.0fs | %d (%d) | %s |\n", s.Step, clock(s.From), clock(s.To), s.WaitS, s.Count, s.Runs, mdCell(s.Text))
		}
		if !any {
			b.WriteString("No stand-pull status lines.\n")
		}

		b.WriteString("\n## Pauses, stops and resumes\n\n")
		if len(r.Control) == 0 {
			b.WriteString("None.\n")
		} else {
			b.WriteString("| at | step | event | note |\n| --- | ---: | --- | --- |\n")
			for _, c := range r.Control {
				ev := c.Kind
				if c.Kind == "resume" {
					ev = fmt.Sprintf("resume (paused %.0fs)", c.PausedS)
				}
				fmt.Fprintf(&b, "| %s | %d | %s | %s |\n", clock(c.At), c.Step, ev, mdCell(c.Note))
			}
		}
	}

	if len(r.Spells) > 0 {
		ids := make([]string, len(r.Spells))
		for i, s := range r.Spells {
			ids[i] = fmt.Sprint(s)
		}
		fmt.Fprintf(&b, "\n## Casts of spell %s (deaths within %.0fs)\n\n", strings.Join(ids, ", "), r.WindowS)
		if len(r.Casts) == 0 {
			b.WriteString("No cast.\n")
		} else {
			b.WriteString("| fight | step | at | into fight | caster | spell | deaths within window |\n| --- | ---: | --- | ---: | --- | --- | --- |\n")
			for _, c := range r.Casts {
				name := fmt.Sprint(c.Spell)
				if c.SpellName != "" {
					name = fmt.Sprintf("%s (%d)", c.SpellName, c.Spell)
				}
				d := "0"
				if len(c.DeathsWithin) > 0 {
					d = fmt.Sprintf("%d (%s)", len(c.DeathsWithin), strings.Join(c.DeathsWithin, ", "))
				}
				fmt.Fprintf(&b, "| %s | %d | %s | %.1fs | %s | %s | %s |\n", mdCell(c.Fight), c.Step, clock(c.At), c.TS, mdCell(c.Caster), mdCell(name), mdCell(d))
			}
			fmt.Fprintf(&b, "\n%d casts, %d deaths within the window.\n", r.Totals.WatchedSpellCasts, r.Totals.WatchedCastsDeaths)
		}
	}
	return b.String()
}

func (r *Report) clockHours() float64 {
	d, err := time.ParseDuration(r.ClockOffset)
	if err != nil {
		return 0
	}
	return d.Hours()
}
