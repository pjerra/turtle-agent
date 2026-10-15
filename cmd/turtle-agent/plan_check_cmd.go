package main

import (
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"path/filepath"

	"github.com/pjerra/turtle-agent/internal/dungeon"
	"github.com/pjerra/turtle-agent/internal/navmesh"
	"github.com/pjerra/turtle-agent/internal/rpc"
)

// exitPlanCheck: plan-check found a point the navmesh cannot take (a failing plan is not a usage error).
const exitPlanCheck = 11

const planCheckUsage = "usage: turtle-agent plan-check [--dir dungeons] --mmaps DIR [--from-leg N] [--json] <plan>..."

// planCheckCmd walks the named plans on the navmesh (README, "Plan check") and prints each failing point. It reads the plan files and
// the mmaps folder and nothing else.
func planCheckCmd(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("plan-check", flag.ContinueOnError)
	fs.SetOutput(stderr)
	dir := fs.String("dir", "dungeons", "the folder with the plan files")
	mmaps := fs.String("mmaps", "", "folder with the server's mmaps (<map>.mmap and <map>*.mmtile); required")
	from := fs.Int("from-leg", 0, "check from this leg on (the legs before it only place the party)")
	asJSON := fs.Bool("json", false, "print the results as JSON")
	if err := fs.Parse(args); err != nil {
		return exitUsage
	}
	if fs.NArg() == 0 || *mmaps == "" {
		fmt.Fprintln(stderr, planCheckUsage)
		return exitUsage
	}
	planner := navmesh.NewPlanner(*mmaps)
	defer planner.Close()
	var results []*dungeon.PlanCheck
	failed := false
	for _, n := range fs.Args() {
		if !planName.MatchString(n) {
			fmt.Fprintf(stderr, "plan-check: %q is not a plan name (a-z, 0-9, _ and -)\n", n)
			return exitUsage
		}
		p, err := dungeon.Load(filepath.Join(*dir, n+".json"))
		if err != nil {
			fmt.Fprintf(stderr, "plan-check: %v\n", err)
			return exitUsage
		}
		res, err := dungeon.CheckPlan(p, planner, *from)
		if err != nil {
			fmt.Fprintf(stderr, "plan-check: %s: %v\n", n, err)
			return exitUsage
		}
		results = append(results, res)
		failed = failed || !res.OK()
		if *asJSON {
			continue
		}
		if res.OK() {
			fmt.Fprintf(stdout, "%s: ok, %d points on the map %d navmesh\n", n, res.Points, res.Map)
		} else {
			fmt.Fprintf(stdout, "%s: %d of %d points fail\n%s\n", n, len(res.Issues), res.Points, res.Summary())
		}
		if len(res.Warnings) > 0 {
			fmt.Fprintf(stdout, "%s: %d warnings\n%s\n", n, len(res.Warnings), res.WarningSummary())
		}
	}
	if *asJSON {
		_ = json.NewEncoder(stdout).Encode(results)
	}
	if failed {
		return exitPlanCheck
	}
	return exitOK
}

// planCheckSkip says the plan check was not run, and why, when the navmesh cannot answer: a build without cgo, or no files for the map.
// Any other error is the check's own.
func planCheckSkip(err error) (string, bool) {
	var missing *navmesh.DataMissingError
	switch {
	case errors.Is(err, navmesh.ErrNoEngine), errors.As(err, &missing):
		return err.Error(), true
	}
	return "", false
}

// planCheckParams is plan_check's: the plan's name and, optionally, the leg to check from (as dungeon_run's from_leg).
type planCheckParams struct {
	Name    string    `json:"name"`
	FromLeg *legParam `json:"from_leg,omitempty"`
}

// planCheckResult is plan_check's answer: the check, or the reason it could not run (Checked false).
type planCheckResult struct {
	dungeon.PlanCheck
	Checked bool   `json:"checked"`
	Skipped string `json:"skipped,omitempty"`
}

// maxRefusedIssues is how many failing points a refusal names; plan_check lists all of them.
const maxRefusedIssues = 12

// check runs the plan check on the planner of the commander service (the one the run plans on, so the same mmaps_dir). A nil planner
// or missing navmesh files leave the plan unchecked, with the reason in skipped.
func (r *dungeonRunner) check(plan *dungeon.Plan, from int) (planCheckResult, error) {
	if r.remote == nil || r.remote.planner == nil {
		return planCheckResult{Skipped: `no navmesh planner (set "mmaps_dir" in the settings file)`}, nil
	}
	res, err := dungeon.CheckPlan(plan, r.remote.planner, from)
	if why, skip := planCheckSkip(err); skip {
		return planCheckResult{Skipped: why}, nil
	}
	if err != nil {
		return planCheckResult{}, err
	}
	return planCheckResult{PlanCheck: *res, Checked: true}, nil
}

// planCheck is the plan_check method.
func (r *dungeonRunner) planCheck(p planCheckParams) (planCheckResult, error) {
	if !planName.MatchString(p.Name) {
		return planCheckResult{}, &rpc.Error{Code: rpc.CodeInvalidParams, Message: `name must be a plan file name such as "zulfarrak" (a-z, 0-9, _ and -)`}
	}
	plan, err := dungeon.Load(filepath.Join(r.dir, p.Name+".json"))
	if err != nil {
		return planCheckResult{}, &rpc.Error{Code: rpc.CodeInvalidParams, Message: "dungeon plan: " + err.Error()}
	}
	from := 0
	if p.FromLeg != nil {
		if from = legIndex(plan, p.FromLeg); from < 0 {
			return planCheckResult{}, &rpc.Error{Code: rpc.CodeInvalidParams, Message: fmt.Sprintf("from_leg: the plan has legs %v (numbers 0-%d)", legNames(plan), len(plan.Legs)-1)}
		}
	}
	res, err := r.check(plan, from)
	if err != nil {
		return planCheckResult{}, &rpc.Error{Code: rpc.CodeGame, Message: "plan check: " + err.Error()}
	}
	if res.Issues == nil {
		res.Issues = []dungeon.PlanIssue{}
	}
	if res.Warnings == nil {
		res.Warnings = []dungeon.PlanIssue{}
	}
	return res, nil
}

// checkForRun is dungeon_run's plan check: it refuses a plan with a failing point unless force is set, and returns the line for the result.
func (r *dungeonRunner) checkForRun(plan *dungeon.Plan, from int, force bool) (string, error) {
	res, err := r.check(plan, from)
	switch {
	case err != nil:
		return "", &rpc.Error{Code: rpc.CodeGame, Message: "plan check: " + err.Error()}
	case !res.Checked:
		return "not run: " + res.Skipped, nil
	case res.OK():
		return fmt.Sprintf("passed: %d points on the navmesh, %d warnings (plan_check lists them)", res.Points, len(res.Warnings)), nil
	}
	shown := res.PlanCheck
	if len(shown.Issues) > maxRefusedIssues {
		shown.Issues = shown.Issues[:maxRefusedIssues]
	}
	more := ""
	if n := len(res.Issues) - len(shown.Issues); n > 0 {
		more = fmt.Sprintf("\n... and %d more (plan_check lists all)", n)
	}
	text := fmt.Sprintf("%d of %d points fail:\n%s%s", len(res.Issues), res.Points, shown.Summary(), more)
	if force {
		return "forced past " + text, nil
	}
	return "", &rpc.Error{Code: rpc.CodeInvalidParams, Message: "plan check failed, " + text + "\nfix the plan, or pass force to run it anyway"}
}
