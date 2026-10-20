package main

import (
	"fmt"
	"math"
	"slices"
	"strconv"
	"strings"

	"github.com/pjerra/turtle-agent/internal/dungeon"
	"github.com/pjerra/turtle-agent/internal/navmesh"
	"github.com/pjerra/turtle-agent/internal/planexport"
	"github.com/pjerra/turtle-agent/internal/tbm"
)

// A partial start of an observed run (#248). The run leader counts the steps of its own SQL plan (`ai_dungeon_step` rows loaded from
// the exported migration), not the agent's plan legs: the exporter writes a move for every via point and a leg's To, every pull group's
// approach point, every boss approach, and — with the options the live migrations use — walk rows that cut long hops ("path"), the
// approach to a pack out of the run leader's reach ("approach path") and avoid-box routes ("path (keeps out of an avoid box)"). So:
//
//   - from_leg maps to the leg's name (`from leg <name>`) when the name is one the command line takes and is unique in the plan, else to
//     its number in the rows (`from <n>`): the number is not the plan's leg index when an optional leg was left out. The start message
//     names the leg the run leader really started in, which is checked against the plan file (drift between the file and the deployed
//     rows stops the run rather than watching the wrong plan).
//   - from_pull_group needs a step number (`from step <n>`). The rows are rebuilt with the exporter's option sets (plain; cut hops;
//     cut hops and approach paths; with the plan's avoid boxes routed around) and the one whose step count equals the count the server
//     listed (`.bot run list`, before anything is hired) is used. When no option set (or more than one, with different answers)
//     matches, the deployed rows were exported differently and the resolver refuses: observe.from_step takes the number the run
//     leader's status line shows.
//   - from_step is passed through as given and checked against the server's step count when it listed one.
//
// The start step's point comes along (PartialStart.Point) for the driver's place check: the party must stand there, not walk the plan
// from the entrance.

// observeStartFrom is the RemoteOptions.StartFrom of an observed run; nil when the run starts at the first step.
func observeStartFrom(plan *dungeon.Plan, leg int, group *int, step int, planner *navmesh.Planner) dungeon.StartFromResolver {
	if group == nil && step == 0 && leg == 0 {
		return nil
	}
	return func(serverSteps int) (dungeon.PartialStart, error) {
		switch {
		case step > 0:
			if serverSteps > 0 && step > serverSteps {
				return dungeon.PartialStart{}, fmt.Errorf("from_step %d: the run leader's plan %s has %d steps (from 1)", step, plan.Name, serverSteps)
			}
			ps := dungeon.PartialStart{Words: "from step " + strconv.Itoa(step), Step: step, Leg: -1}
			ps.Point, ps.HavePoint, ps.ExpectLeg = observeStepInfo(plan, planner, serverSteps, step)
			return ps, nil
		case group != nil:
			if leg < 0 || leg >= len(plan.Legs) {
				return dungeon.PartialStart{}, fmt.Errorf("from_pull_group: the plan has legs %v (numbers 0-%d)", legNames(plan), len(plan.Legs)-1)
			}
			if n := len(plan.Legs[leg].PullGroups); *group < 0 || *group >= n {
				return dungeon.PartialStart{}, fmt.Errorf("from_pull_group %d: leg %d (%s) has %d pull groups (numbers 0-%d)", *group, leg, plan.Legs[leg].Name, n, n-1)
			}
			if plan.Legs[leg].Optional != nil {
				return dungeon.PartialStart{}, fmt.Errorf("from_pull_group %d: leg %d (%s) is optional (it needs item %d) and the exporter leaves optional legs out of the run leader's rows", *group, leg, plan.Legs[leg].Name, plan.Legs[leg].Optional.Item)
			}
			n, at, ok, err := observeGroupStep(plan, leg, *group, planner, serverSteps)
			if err != nil {
				return dungeon.PartialStart{}, err
			}
			rowLeg, _ := planexport.LegNumber(plan, leg)
			return dungeon.PartialStart{Words: "from step " + strconv.Itoa(n), Step: n, Leg: leg, LegNumber: rowLeg, ExpectLeg: plan.Legs[leg].Name, Point: at, HavePoint: ok}, nil
		default:
			if leg < 0 || leg >= len(plan.Legs) {
				return dungeon.PartialStart{}, fmt.Errorf("from_leg %d: the plan has legs %v (numbers 0-%d)", leg, legNames(plan), len(plan.Legs)-1)
			}
			rowLeg, ok := planexport.LegNumber(plan, leg)
			if !ok {
				return dungeon.PartialStart{}, fmt.Errorf("from_leg %d: leg %q is optional (it needs item %d) and the exporter leaves optional legs out of the run leader's rows; start at a later leg, or pass observe.from_step", leg, plan.Legs[leg].Name, plan.Legs[leg].Optional.Item)
			}
			ps := dungeon.PartialStart{Leg: leg, LegNumber: rowLeg}
			if name, ok := legNameForCommand(plan, leg); ok {
				ps.Words, ps.LegName = "from leg "+name, name
			} else if rowLeg > 0 { // leg 0 is the first step: no `from` (the run leader's own beginning)
				ps.Words = "from " + strconv.Itoa(rowLeg)
			}
			ps.Point, ps.HavePoint = legStartPoint(plan, leg)
			return ps, nil
		}
	}
}

// legNameForCommand says whether a leg's name can be sent in `.bot run start ... from leg <name>`: one word the commander command
// grammar takes (an apostrophe or a space cannot), at most the 32 characters the exporter keeps, and unique case-insensitively (the
// module matches names lower case). Else the leg's row number goes, which a busy run cannot be compared with (startMatchesStatus).
func legNameForCommand(p *dungeon.Plan, leg int) (string, bool) {
	name := p.Legs[leg].Name
	if name == "" || len(name) > planexport.MaxLegName || len(strings.Fields(name)) != 1 || tbm.BadOrderWords(name) != "" {
		return "", false
	}
	n := 0
	for _, l := range p.Legs {
		if strings.EqualFold(l.Name, name) {
			n++
		}
	}
	return name, n == 1
}

// legStartPoint is the plan's first point of a leg: its first via point, else its To (the exporter's first row of the leg).
func legStartPoint(p *dungeon.Plan, leg int) (dungeon.Point, bool) {
	if leg < 0 || leg >= len(p.Legs) {
		return dungeon.Point{}, false
	}
	l := p.Legs[leg]
	if len(l.Via) > 0 {
		return l.Via[0].Point, true
	}
	return l.To, true
}

// observeGroupStep maps a plan leg's pull group to the run leader's 1-based step number and that step's point.
func observeGroupStep(plan *dungeon.Plan, leg, group int, planner *navmesh.Planner, serverSteps int) (int, dungeon.Point, bool, error) {
	if serverSteps <= 0 {
		return 0, dungeon.Point{}, false, fmt.Errorf("from_pull_group %d (leg %d): the server did not list the plan's step count for %s (.bot run list), so the group's step cannot be mapped; pass observe.from_step (the step .bot run status shows)", group, leg, plan.Name)
	}
	cands, buildErrs := observePlanRows(plan, planner)
	if len(cands) == 0 {
		return 0, dungeon.Point{}, false, fmt.Errorf("from_pull_group %d (leg %d): the plan's rows cannot be rebuilt here (%s), so the group's step number is not known; pass observe.from_step (the step .bot run status shows)", group, leg, strings.Join(buildErrs, "; "))
	}
	var counts, steps []int
	var at dungeon.Point
	haveAt := false
	for _, r := range cands {
		counts = append(counts, r.StepCount())
		if r.StepCount() != serverSteps {
			continue
		}
		if n, ok := r.GroupStep(plan, leg, group); ok {
			steps = append(steps, n)
			if p, ok2 := r.StepPoint(n); ok2 {
				if !haveAt {
					at, haveAt = p, true
				} else if pointDist(at, p) > 1 {
					haveAt = false // the option sets put the same step number in different places: no point to compare the party with
				}
			}
		}
	}
	if len(steps) == 0 {
		return 0, dungeon.Point{}, false, fmt.Errorf("from_pull_group %d (leg %d): the server's plan %s has %d steps, and the plan file's exported rows give %s steps: the rows on the server were exported with other options (walk rows for cut hops, approach paths and avoid-box routes change the step numbers), so the group's step number cannot be computed offline. Pass observe.from_step (the step .bot run status shows) instead", group, leg, plan.Name, serverSteps, distinctSteps(counts))
	}
	slices.Sort(steps)
	if steps[0] != steps[len(steps)-1] {
		return 0, dungeon.Point{}, false, fmt.Errorf("from_pull_group %d (leg %d): the exporter's option sets that match the server's %d steps put the group at steps %v: too ambiguous to pick one; pass observe.from_step", group, leg, serverSteps, distinctSteps(steps))
	}
	return steps[0], at, haveAt, nil
}

// observeStepInfo is the point of the module's step step and the plan leg it belongs to, when an exporter option set reproduces the
// server's step count (else neither; the start still goes). The leg name is the expectation for the run leader's start reply.
func observeStepInfo(plan *dungeon.Plan, planner *navmesh.Planner, serverSteps, step int) (dungeon.Point, bool, string) {
	if serverSteps <= 0 || step < 1 {
		return dungeon.Point{}, false, ""
	}
	cands, _ := observePlanRows(plan, planner)
	var at dungeon.Point
	have, haveLeg := false, false
	legName := ""
	for _, r := range cands {
		if r.StepCount() != serverSteps || step > len(r.Steps) {
			continue
		}
		s := r.Steps[step-1]
		p := dungeon.Point{X: s.X, Y: s.Y, Z: s.Z}
		if !have {
			at, have = p, true
		} else if pointDist(at, p) > 1 {
			at, have = dungeon.Point{}, false
		}
		name := ""
		if pi, ok := planexport.PlanLeg(plan, s.Leg); ok {
			name = plan.Legs[pi].Name
		}
		if !haveLeg {
			legName, haveLeg = name, name != ""
		} else if name != legName {
			legName, haveLeg = "", false
		}
	}
	if !haveLeg {
		legName = ""
	}
	return at, have, legName
}

// observePlanRows rebuilds the plan's rows with the exporter option sets an applied migration may have used. The step count the server
// lists picks the one that was deployed. Errors are per option set (a plan the exporter refuses one way may export another way).
func observePlanRows(p *dungeon.Plan, planner *navmesh.Planner) ([]planexport.Rows, []string) {
	base := planexport.Options{Source: "observe mode step mapping", GMCrossDirect: true, DropCompanionsLoot: true}
	var out []planexport.Rows
	var errs []string
	add := func(o planexport.Options, what string) {
		res, err := planexport.Build(p, o)
		if err != nil {
			errs = append(errs, what+": "+err.Error())
			return
		}
		out = append(out, res.Rows)
	}
	add(base, "plain")
	if planner == nil {
		return out, errs
	}
	// The same rows the plan-export command writes with --mmaps (it cuts long hops), with and without --approach-hops; the avoid boxes
	// of the plan are marked on the cutter and routed around, as the exporter does.
	aware := planner.Scratch()
	defer aware.Close()
	boxes := dungeon.NavBoxes(p.Avoid)
	if len(boxes) > 0 {
		aware.SetAvoid(p.Map, boxes)
	}
	cut := cutWith(aware)
	o := base
	o.Cut = cut
	add(o, "cut hops")
	o.ApproachHops = true
	add(o, "cut hops and approach paths")
	if len(boxes) > 0 {
		naive := planner.Scratch()
		defer naive.Close()
		route := routeAround(naive, aware, boxes)
		ro := base
		ro.Cut, ro.Route = cut, route
		add(ro, "cut hops and avoid-box routes")
		ro.ApproachHops = true
		add(ro, "cut hops, approach paths and avoid-box routes")
	}
	return out, errs
}

// pointDist is the distance between two points of a map, in yards.
func pointDist(a, b dungeon.Point) float64 {
	dx, dy, dz := float64(a.X-b.X), float64(a.Y-b.Y), float64(a.Z-b.Z)
	return math.Sqrt(dx*dx + dy*dy + dz*dz)
}

// distinctSteps lists the numbers once each, in the order they came, as a comma list.
func distinctSteps(ns []int) string {
	var out []string
	for _, n := range ns {
		s := strconv.Itoa(n)
		if !slices.Contains(out, s) {
			out = append(out, s)
		}
	}
	return strings.Join(out, ", ")
}
