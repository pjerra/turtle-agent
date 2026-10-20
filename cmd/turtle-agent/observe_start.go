package main

import (
	"fmt"
	"slices"
	"strconv"
	"strings"

	"github.com/pjerra/turtle-agent/internal/dungeon"
	"github.com/pjerra/turtle-agent/internal/navmesh"
	"github.com/pjerra/turtle-agent/internal/planexport"
)

// A partial start of an observed run (#248). The run leader counts the steps of its own SQL plan (`ai_dungeon_step` rows loaded from
// the exported migration), not the agent's plan legs: the exporter writes a move for every via point and a leg's To, every pull group's
// approach point, every boss approach, and — with the options the live migrations use — path rows that cut long hops and "approach
// path" rows between a pack and the anchor before it. So:
//
//   - from_leg maps to the leg's number in the rows (`from <n>`), which is not the plan's leg index when an optional leg was left out;
//     no step count is needed (the run leader resolves the leg itself).
//   - from_pull_group needs a step number (`from step <n>`). The rows are rebuilt with the exporter's option sets (plain; cut hops;
//     cut hops and approach paths; with the plan's avoid boxes routed around) and the one whose step count equals the count the server
//     listed (`.bot run list`, before anything is hired) is used. When no option set (or more than one, with different answers)
//     matches, the deployed rows were exported differently and the resolver refuses: observe.from_step takes the number the run
//     leader's status line shows.
//   - from_step is passed through as given and checked against the server's step count when it listed one.

// observeStartFrom is the RemoteOptions.StartFrom of an observed run; nil when the run starts at the first step.
func observeStartFrom(plan *dungeon.Plan, leg int, group *int, step int, planner *navmesh.Planner) dungeon.StartFromResolver {
	if group == nil && step == 0 && leg == 0 {
		return nil
	}
	return func(serverSteps int) (string, error) {
		switch {
		case step > 0:
			if serverSteps > 0 && step > serverSteps {
				return "", fmt.Errorf("from_step %d: the run leader's plan %s has %d steps (from 1)", step, plan.Name, serverSteps)
			}
			return "from step " + strconv.Itoa(step), nil
		case group != nil:
			n, err := observeGroupStep(plan, leg, *group, planner, serverSteps)
			if err != nil {
				return "", err
			}
			return "from step " + strconv.Itoa(n), nil
		default:
			if leg < 0 || leg >= len(plan.Legs) {
				return "", fmt.Errorf("from_leg %d: the plan has legs %v (numbers 0-%d)", leg, legNames(plan), len(plan.Legs)-1)
			}
			n, ok := planexport.LegNumber(plan, leg)
			if !ok {
				return "", fmt.Errorf("from_leg %d: leg %q is optional (it needs item %d) and the exporter leaves optional legs out of the run leader's rows; start at a later leg, or pass observe.from_step", leg, plan.Legs[leg].Name, plan.Legs[leg].Optional.Item)
			}
			if n == 0 { // leg 0 is the first step: no `from` (the run leader's own beginning)
				return "", nil
			}
			return "from " + strconv.Itoa(n), nil
		}
	}
}

// observeGroupStep maps a plan leg's pull group to the run leader's 1-based step number.
func observeGroupStep(plan *dungeon.Plan, leg, group int, planner *navmesh.Planner, serverSteps int) (int, error) {
	if leg < 0 || leg >= len(plan.Legs) {
		return 0, fmt.Errorf("from_pull_group: the plan has legs %v (numbers 0-%d)", legNames(plan), len(plan.Legs)-1)
	}
	if n := len(plan.Legs[leg].PullGroups); group < 0 || group >= n {
		return 0, fmt.Errorf("from_pull_group %d: leg %d (%s) has %d pull groups (numbers 0-%d)", group, leg, plan.Legs[leg].Name, n, n-1)
	}
	if plan.Legs[leg].Optional != nil {
		return 0, fmt.Errorf("from_pull_group %d: leg %d (%s) is optional (it needs item %d) and the exporter leaves optional legs out of the run leader's rows", group, leg, plan.Legs[leg].Name, plan.Legs[leg].Optional.Item)
	}
	if serverSteps <= 0 {
		return 0, fmt.Errorf("from_pull_group %d (leg %d): the server did not list the plan's step count for %s (.bot run list), so the group's step cannot be mapped; pass observe.from_step (the step .bot run status shows)", group, leg, plan.Name)
	}
	cands, buildErrs := observePlanRows(plan, planner)
	if len(cands) == 0 {
		return 0, fmt.Errorf("from_pull_group %d (leg %d): the plan's rows cannot be rebuilt here (%s), so the group's step number is not known; pass observe.from_step (the step .bot run status shows)", group, leg, strings.Join(buildErrs, "; "))
	}
	var counts, steps []int
	for _, r := range cands {
		counts = append(counts, r.StepCount())
		if r.StepCount() != serverSteps {
			continue
		}
		if n, ok := r.GroupStep(plan, leg, group); ok {
			steps = append(steps, n)
		}
	}
	if len(steps) == 0 {
		return 0, fmt.Errorf("from_pull_group %d (leg %d): the server's plan %s has %d steps, and the plan file's exported rows give %s steps: the rows on the server were exported with other options (cut hops, approach paths and avoid-box routes add rows), so the group's step number cannot be computed offline. Pass observe.from_step (the step .bot run status shows) instead", group, leg, plan.Name, serverSteps, distinctSteps(counts))
	}
	slices.Sort(steps)
	if steps[0] != steps[len(steps)-1] {
		return 0, fmt.Errorf("from_pull_group %d (leg %d): the exporter's option sets that match the server's %d steps put the group at steps %v: too ambiguous to pick one; pass observe.from_step", group, leg, serverSteps, distinctSteps(steps))
	}
	return steps[0], nil
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
