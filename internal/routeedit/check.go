package routeedit

import (
	"fmt"

	"github.com/pjerra/turtle-agent/internal/dungeon"
)

// Issue is one failing point of the edited plan. New is true when the base plan does not fail at that point for that reason: an edit is
// refused for the issues it makes, not for the ones the plan already had.
type Issue struct {
	dungeon.PlanIssue
	New  bool   `json:"new"`
	Text string `json:"text"`
}

// CheckResult is the plan check of an edit (dungeon.CheckPlan, the whole plan, the driver's default look), against the navmesh of the
// server the plan is run on.
type CheckResult struct {
	// Checked: the check ran. False when the navmesh could not answer (no engine, no tiles): the edit is then refused, nothing is proven.
	Checked bool   `json:"checked"`
	Skipped string `json:"skipped,omitempty"`
	// OK: checked and the edit makes no new issue.
	OK         bool                `json:"ok"`
	Points     int                 `json:"points"`
	BaseIssues int                 `json:"base_issues"`
	NewIssues  int                 `json:"new_issues"`
	Issues     []Issue             `json:"issues"`
	Warnings   []dungeon.PlanIssue `json:"warnings"`
	Exempt     []dungeon.PlanIssue `json:"exempt"`
}

// Evaluate checks the edited plan, and the base plan to tell the issues the edit makes from the ones the plan had.
func Evaluate(base, edited *dungeon.Plan, nav dungeon.Navmesh) CheckResult {
	res := CheckResult{Issues: []Issue{}, Warnings: []dungeon.PlanIssue{}, Exempt: []dungeon.PlanIssue{}}
	after, err := dungeon.CheckPlan(edited, nav, 0)
	if err != nil {
		res.Skipped = err.Error()
		return res
	}
	before, err := dungeon.CheckPlan(base, nav, 0)
	if err != nil {
		res.Skipped = err.Error()
		return res
	}
	had := map[string]bool{}
	for _, is := range before.Issues {
		had[issueKey(is)] = true
	}
	res.Checked, res.Points, res.BaseIssues = true, after.Points, len(before.Issues)
	for _, is := range after.Issues {
		n := !had[issueKey(is)]
		if n {
			res.NewIssues++
		}
		res.Issues = append(res.Issues, Issue{PlanIssue: is, New: n, Text: is.String()})
	}
	res.Warnings, res.Exempt = append(res.Warnings, after.Warnings...), append(res.Exempt, after.Exempt...)
	res.OK = res.NewIssues == 0
	return res
}

// issueKey names an issue by where it is and why, not by its number in the leg: inserting a via point renumbers the ones after it.
func issueKey(i dungeon.PlanIssue) string {
	return fmt.Sprintf("%d|%s|%.1f|%.1f|%.1f", i.Leg, i.Reason, i.Point.X, i.Point.Y, i.Point.Z)
}

// Refusal is the sentence a refused edit starts with.
func (c CheckResult) Refusal() string {
	if !c.Checked {
		return "the plan check could not run (" + c.Skipped + "): nothing is proven, the edit is refused"
	}
	return fmt.Sprintf("the edit makes %d new plan check issue(s)", c.NewIssues)
}
