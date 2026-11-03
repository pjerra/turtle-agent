// Package planscout checks a dungeon plan point by point before a walk: the offline pass runs plan-check's lints over the
// plan's exported rows (the rows TortoiseBots' run leader walks) and turns every finding into a step check; it writes a
// report and a fix file of proposed corrections. The live pass (a GM on a test server) is not in this build.
package planscout

import (
	"github.com/pjerra/turtle-agent/internal/planexport"
	"github.com/pjerra/turtle-agent/internal/planlint"
)

// Format and Version are the report's file format (tb-plan-scout v1).
const (
	Format  = "tb-plan-scout"
	Version = 1
)

// FixFormat and FixVersion are the fix file's format (tb-scout-fix v1).
const (
	FixFormat  = "tb-scout-fix"
	FixVersion = 1
)

// The verdicts, worst first.
const (
	VerdictOK   = "ok"
	VerdictWarn = "warn"
	VerdictFail = "fail"
	VerdictSkip = "skip"
)

// The checks' sources.
const (
	SourceOffline = "offline"
	SourceLive    = "live"
)

// Check is one rule applied to one step.
type Check struct {
	Name    string             `json:"name"`
	Source  string             `json:"source"`
	Verdict string             `json:"verdict"`
	Reason  string             `json:"reason"`
	Data    map[string]float64 `json:"data,omitempty"`
	// Bodies, Guids and Packs carry a joiner check's payload (the fix's split_pull).
	Bodies int                   `json:"bodies,omitempty"`
	Guids  []uint32              `json:"guids,omitempty"`
	Packs  []planlint.JoinerPack `json:"joiner_packs,omitempty"`
}

// StepResult is one exported row and what the checks made of it.
type StepResult struct {
	Step    int                   `json:"step"`
	Leg     int                   `json:"leg"`
	Seq     int                   `json:"seq"`
	Kind    string                `json:"kind"`
	Comment string                `json:"comment,omitempty"`
	X       float32               `json:"x"`
	Y       float32               `json:"y"`
	Z       float32               `json:"z"`
	Origin  *planexport.RowOrigin `json:"origin,omitempty"`
	Checks  []Check               `json:"checks"`
	Verdict string                `json:"verdict"`
}

// Counts are the steps by verdict.
type Counts struct {
	OK   int `json:"ok"`
	Warn int `json:"warn"`
	Fail int `json:"fail"`
	Skip int `json:"skip"`
}

// Report is one plan's scout run (tb-plan-scout v1).
type Report struct {
	Format  string `json:"format"`
	Version int    `json:"version"`
	Plan    string `json:"plan"`
	Map     uint32 `json:"map"`
	// Source says what the rows came from ("plan file", a migration path).
	Source string `json:"source,omitempty"`
	Mmaps  string `json:"mmaps,omitempty"`
	// Server is the live server's name ("" offline).
	Server string `json:"server,omitempty"`
	// From and Until are the legs checked, inclusive (0: from the first, through the last).
	From int `json:"from,omitempty"`
	// Until is 0 for the last leg.
	Until  int          `json:"until,omitempty"`
	Steps  []StepResult `json:"steps"`
	Counts Counts       `json:"counts"`
	Notes  []string     `json:"notes"`
	// Runtime is the offline pass's wall clock in seconds.
	Runtime float64 `json:"runtime"`
}

// Worst is the worse of two verdicts.
func Worst(a, b string) string {
	rank := map[string]int{VerdictOK: 0, VerdictSkip: 1, VerdictWarn: 2, VerdictFail: 3}
	if rank[b] > rank[a] {
		return b
	}
	return a
}
