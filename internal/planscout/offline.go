package planscout

import (
	"sort"
	"time"

	"github.com/pjerra/turtle-agent/internal/dungeon"
	"github.com/pjerra/turtle-agent/internal/planexport"
	"github.com/pjerra/turtle-agent/internal/planlint"
	"github.com/pjerra/turtle-agent/internal/worldspawn"
)

// Nav is what the offline pass asks the navmesh: the placement lints' queries and the server's own path.
type Nav interface {
	planlint.Nav
	planlint.PathNav
}

// Input is the scout's material: the exported rows, their plan origins (nil for a migration) and the plan (nil when a
// migration is checked without its plan file).
type Input struct {
	Rows    planexport.Rows
	Origins []planexport.RowOrigin
	Plan    *dungeon.Plan
}

// Offline runs plan-check's lints over the rows and turns every finding into a step check. It is pure over nav and w.
func Offline(in Input, nav Nav, w *worldspawn.World, opt Options) (*Report, error) {
	start := time.Now()
	level := opt.PartyLevel
	if level == 0 {
		if in.Plan != nil {
			_, level = in.Plan.Band()
		}
		if level == 0 {
			level = 60
		}
	}
	lopt := planlint.Options{Map: in.Rows.Plan.Map, Level: level, World: w}
	if in.Plan != nil {
		lopt.Raid, lopt.KillFirst, lopt.PullCeiling = in.Plan.IsRaid(), in.Plan.KillFirst, in.Plan.PullCeiling
	}
	findings, err := planlint.Lint(in.Rows, nav, lopt)
	if err != nil {
		return nil, err
	}
	byStep := map[int][]planlint.Finding{}
	for _, f := range findings {
		byStep[f.Step] = append(byStep[f.Step], f)
	}
	rep := &Report{Format: Format, Version: Version, Plan: in.Rows.Plan.Plan, Map: in.Rows.Plan.Map,
		From: opt.FromLeg, Until: opt.UntilLeg, Notes: []string{
			"sight is assumed offline for the aggro and joiner estimates (the mesh has no vmap collision): the high side",
			"patrols count at their spawn point offline",
		}}
	for i, s := range in.Rows.Steps {
		if opt.Filter && opt.FromLeg >= 0 && s.Leg < opt.FromLeg {
			continue
		}
		if opt.Filter && opt.UntilLeg >= 0 && s.Leg > opt.UntilLeg {
			continue
		}
		sr := StepResult{Step: i + 1, Leg: s.Leg, Seq: s.Seq, Kind: s.Kind, Comment: s.Comment, X: s.X, Y: s.Y, Z: s.Z, Checks: []Check{}, Verdict: VerdictOK}
		if i < len(in.Origins) {
			o := in.Origins[i]
			sr.Origin = &o
		}
		for _, f := range byStep[i+1] {
			v := VerdictWarn
			if f.Severity == planlint.SeverityError {
				v = VerdictFail
			}
			data := map[string]float64{}
			if f.Bodies != 0 {
				data["bodies"] = float64(f.Bodies)
			}
			if f.Ceiling != 0 {
				data["ceiling"] = float64(f.Ceiling)
			}
			c := Check{Name: f.Lint, Source: SourceOffline, Verdict: v, Reason: f.Detail, Data: data}
			if f.Lint == planlint.LintJoiners {
				c.Bodies, c.Guids, c.Packs = f.Bodies, f.Guids, f.JoinerPacks
			}
			sr.Checks = append(sr.Checks, c)
			sr.Verdict = Worst(sr.Verdict, v)
		}
		sort.SliceStable(sr.Checks, func(a, b int) bool { return sr.Checks[a].Name < sr.Checks[b].Name })
		switch sr.Verdict {
		case VerdictFail:
			rep.Counts.Fail++
		case VerdictWarn:
			rep.Counts.Warn++
		default:
			rep.Counts.OK++
		}
		rep.Steps = append(rep.Steps, sr)
	}
	rep.Runtime = time.Since(start).Seconds()
	if rep.Plan == "" {
		rep.Plan = in.Rows.Plan.Plan
	}
	return rep, nil
}
