package main

import (
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/pjerra/turtle-agent/internal/dungeon"
	"github.com/pjerra/turtle-agent/internal/navmesh"
	"github.com/pjerra/turtle-agent/internal/rpc"
)

// exitPlanCheck: plan-check found a point the navmesh cannot take (a failing plan is not a usage error).
const exitPlanCheck = 11

const planCheckUsage = "usage: turtle-agent plan-check [--dir dungeons] --mmaps DIR [--from-leg N [--from-pull-group N]] [--team alliance|horde] [--json] <plan>..."

// planCheckCmd walks the named plans on the navmesh (README, "Plan check") and prints each failing point. It reads the plan files and
// the mmaps folder and nothing else.
func planCheckCmd(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("plan-check", flag.ContinueOnError)
	fs.SetOutput(stderr)
	dir := fs.String("dir", "dungeons", "the folder with the plan files")
	mmaps := fs.String("mmaps", "", "folder with the server's mmaps (<map>.mmap and <map>*.mmtile); required")
	from := fs.Int("from-leg", 0, "check from this leg on (the legs before it only place the party)")
	fromGroup := fs.Int("from-pull-group", -1, "with --from-leg: check from this pull group (0-based) of that leg on (default: the leg's own walk too)")
	team := fs.String("team", "", "alliance or horde: leave out the parts of the other team, as a run does (default: check all)")
	asJSON := fs.Bool("json", false, "print the results as JSON")
	if err := fs.Parse(args); err != nil {
		return exitUsage
	}
	if fs.NArg() == 0 || *mmaps == "" {
		fmt.Fprintln(stderr, planCheckUsage)
		return exitUsage
	}
	if t := strings.ToLower(*team); t != "" && t != dungeon.TeamAlliance && t != dungeon.TeamHorde {
		fmt.Fprintf(stderr, "plan-check: --team %q is neither %q nor %q\n", *team, dungeon.TeamAlliance, dungeon.TeamHorde)
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
		opt := dungeon.CheckOptions{FromLeg: *from, Team: strings.ToLower(*team)}
		if *fromGroup >= 0 {
			opt.FromPullGroup = fromGroup
		}
		res, err := dungeon.CheckPlanWith(p, planner, opt)
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
		if len(res.Exempt) > 0 {
			fmt.Fprintf(stdout, "%s: %d hops exempted by the plan (check_skip)\n%s\n", n, len(res.Exempt), res.ExemptSummary())
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
	// FromPullGroup (0-based, with from_leg) checks from that pull group of the leg on.
	FromPullGroup *int `json:"from_pull_group,omitempty"`
	// Team ("alliance" or "horde") leaves out the pull groups and bosses of the other team, as the run does; omitted: all are checked and
	// the team-only ones are labelled.
	Team string `json:"team,omitempty"`
}

// planCheckResult is plan_check's answer: the check, or the reason it could not run (Checked false).
type planCheckResult struct {
	dungeon.PlanCheck
	Checked bool   `json:"checked"`
	Skipped string `json:"skipped,omitempty"`
}

// maxRefusedIssues is how many failing points a refusal names; plan_check lists all of them.
const maxRefusedIssues = 12

// checkOptions is where the run starts and (when tuned: the commander driver) its tuned look: look_standoff and look_arrive of the tunables file it reads (the one named, else
// the login's --tune, else <plan>.tune.json beside the plan; a missing or bad file leaves the defaults, the run itself reports a bad one).
func (r *dungeonRunner) checkOptions(planPath, tune string, tuned bool, team string, from int, group *int) dungeon.CheckOptions {
	opt := dungeon.CheckOptions{FromLeg: from, FromPullGroup: group, Team: strings.ToLower(team)}
	if !tuned { // only the commander driver reads a tunables file
		return opt
	}
	path, err := r.tuneFile(dungeonRunParams{Tune: tune}, planPath)
	if err != nil {
		return opt
	}
	if path == "" {
		path = dungeon.DefaultTunePath(planPath)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return opt
	}
	if vals, err := dungeon.ParseTuneFile(data); err == nil {
		opt.LookStandoff, opt.LookArrive = float32(vals["look_standoff"]), float32(vals["look_arrive"])
	}
	return opt
}

// check runs the plan check on a scratch planner (its own tile cache: a run going at the same time keeps its tiles) over the same mmaps_dir
// the run plans on. A nil planner or missing navmesh files leave the plan unchecked, with the reason in skipped.
func (r *dungeonRunner) check(plan *dungeon.Plan, opt dungeon.CheckOptions) (planCheckResult, error) {
	if err := opt.Validate(plan); err != nil {
		return planCheckResult{}, err
	}
	if r.remote == nil || r.remote.planner == nil {
		return planCheckResult{Skipped: `no navmesh planner (set "mmaps_dir" in the settings file)`}, nil
	}
	pl := r.remote.planner.Scratch()
	defer pl.Close()
	res, err := dungeon.CheckPlanWith(plan, pl, opt)
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
	if t := strings.ToLower(p.Team); t != "" && t != dungeon.TeamAlliance && t != dungeon.TeamHorde {
		return planCheckResult{}, &rpc.Error{Code: rpc.CodeInvalidParams, Message: fmt.Sprintf("team: %q is neither %q nor %q", p.Team, dungeon.TeamAlliance, dungeon.TeamHorde)}
	}
	planPath := filepath.Join(r.dir, p.Name+".json")
	plan, err := dungeon.Load(planPath)
	if err != nil {
		return planCheckResult{}, &rpc.Error{Code: rpc.CodeInvalidParams, Message: "dungeon plan: " + err.Error()}
	}
	from := 0
	if p.FromLeg != nil {
		if from = legIndex(plan, p.FromLeg); from < 0 {
			return planCheckResult{}, &rpc.Error{Code: rpc.CodeInvalidParams, Message: fmt.Sprintf("from_leg: the plan has legs %v (numbers 0-%d)", legNames(plan), len(plan.Legs)-1)}
		}
	}
	res, err := r.check(plan, r.checkOptions(planPath, "", true, p.Team, from, p.FromPullGroup))
	if errors.Is(err, dungeon.ErrCheckStart) {
		return planCheckResult{}, &rpc.Error{Code: rpc.CodeInvalidParams, Message: err.Error()}
	}
	if err != nil {
		return planCheckResult{}, &rpc.Error{Code: rpc.CodeGame, Message: "plan check: " + err.Error()}
	}
	if res.Issues == nil {
		res.Issues = []dungeon.PlanIssue{}
	}
	if res.Warnings == nil {
		res.Warnings = []dungeon.PlanIssue{}
	}
	if res.Exempt == nil {
		res.Exempt = []dungeon.PlanIssue{}
	}
	return res, nil
}

// checkForRun is dungeon_run's plan check: it refuses a plan with a failing point, or one it cannot check (no planner, no navmesh files),
// unless force is set, and returns the line for the result.
// With force an error inside the check (a corrupt tile, a failed ray) is passed too, and noted.
func (r *dungeonRunner) checkForRun(plan *dungeon.Plan, opt dungeon.CheckOptions, force bool) (string, error) {
	res, err := r.check(plan, opt)
	switch {
	case errors.Is(err, dungeon.ErrCheckStart):
		return "", &rpc.Error{Code: rpc.CodeInvalidParams, Message: err.Error()}
	case err != nil && force:
		return "forced past check error: " + err.Error(), nil
	case err != nil:
		return "", &rpc.Error{Code: rpc.CodeGame, Message: "plan check: " + err.Error() + "\npass force to run the plan unchecked"}
	case !res.Checked && force:
		return "forced past: plan check not run (" + res.Skipped + ")", nil
	case !res.Checked:
		return "", &rpc.Error{Code: rpc.CodeInvalidParams, Message: "plan check could not run: " + res.Skipped + "; pass force to run anyway"}
	case res.OK():
		return fmt.Sprintf("passed: %d points on the navmesh, %d warnings, %d hops exempted by the plan (plan_check lists them)", res.Points, len(res.Warnings), len(res.Exempt)), nil
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
