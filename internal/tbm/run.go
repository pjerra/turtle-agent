package tbm

import (
	"fmt"
	"strconv"
	"strings"
)

// The run leader (TortoiseBots docs/guides/dungeon-run-leader.md, `.bot run`): a bound party clears a dungeon plan by itself, and
// the commander watches its status line:
//
//	TBM:RUN|<plan>|<state>|<step>/<steps>|<leg>|<kind>|<phase>|<note>
//	TBM:RUN|uldaman|running|7/41|baelog|pull|pull|pulling 4851
//	TBM:RUN|-|none|0/0|-|-|-|no run
//
// The module sends it whenever the step, phase or note changes, and on start, status, pause, resume, skip and stop.

// RunPrefix starts a run status line.
const RunPrefix = "TBM:RUN|"

// RunVerbs are the `.bot run` verbs.
var RunVerbs = []string{"start", "pause", "resume", "skip", "stop", "status", "list"}

// The states of a run status line.
const (
	RunRunning = "running"
	RunPaused  = "paused"
	RunDone    = "done"
	RunNone    = "none" // no run exists (the answer of `run status` with nothing going)
)

// RunStatus is one TBM:RUN line.
type RunStatus struct {
	Plan  string `json:"plan"`
	State string `json:"state"` // running, paused, done or none
	Step  int    `json:"step"`  // counts from 1; 0 before the first
	Steps int    `json:"steps"`
	Leg   string `json:"leg"`   // the leg's name, or its number; empty when the module sent "-"
	Kind  string `json:"kind"`  // the step kind; empty for "-"
	Phase string `json:"phase"` // begin, gather, walk, pull, task, rest or wait; empty for "-"
	Note  string `json:"note"`  // what it does, or why it paused; empty for "-"
	Line  string `json:"line,omitempty"`
}

// ParseRunLine reads a TBM:RUN line. The note is the last field and may hold anything but a line break; a line with fewer than
// eight fields or a step that is not "n/m" is not a status line.
func ParseRunLine(line string) (RunStatus, bool) {
	rest, ok := strings.CutPrefix(strings.TrimSpace(line), RunPrefix)
	if !ok {
		return RunStatus{}, false
	}
	f := strings.SplitN(rest, "|", 7)
	if len(f) != 7 {
		return RunStatus{}, false
	}
	a, b, ok := strings.Cut(f[2], "/")
	if !ok {
		return RunStatus{}, false
	}
	step, err1 := strconv.Atoi(a)
	steps, err2 := strconv.Atoi(b)
	if err1 != nil || err2 != nil || step < 0 || steps < 0 {
		return RunStatus{}, false
	}
	dash := func(s string) string {
		if s == "-" {
			return ""
		}
		return s
	}
	return RunStatus{Plan: dash(f[0]), State: f[1], Step: step, Steps: steps, Leg: dash(f[3]), Kind: dash(f[4]), Phase: dash(f[5]), Note: dash(f[6]), Line: strings.TrimSpace(line)}, true
}

// Same reports whether two statuses say the same thing (the line may differ in nothing else).
func (s RunStatus) Same(o RunStatus) bool {
	return s.Plan == o.Plan && s.State == o.State && s.Step == o.Step && s.Steps == o.Steps && s.Leg == o.Leg && s.Kind == o.Kind && s.Phase == o.Phase && s.Note == o.Note
}

// Text is the status in a few plain words for a report or a dashboard: "Sentinel room · pull 7/41 · pulling".
func (s RunStatus) Text() string {
	switch s.State {
	case RunNone:
		return "no run"
	}
	var parts []string
	if s.Leg != "" {
		parts = append(parts, strings.ReplaceAll(s.Leg, "_", " "))
	}
	if s.Kind != "" {
		parts = append(parts, fmt.Sprintf("%s %d/%d", strings.ReplaceAll(s.Kind, "_", " "), s.Step, s.Steps))
	} else if s.Steps > 0 {
		parts = append(parts, fmt.Sprintf("step %d/%d", s.Step, s.Steps))
	}
	if w := RunPhaseWord(s.Phase); w != "" && s.State == RunRunning {
		parts = append(parts, w)
	}
	if s.Note != "" && (s.State != RunRunning || s.Phase == "") {
		parts = append(parts, s.Note)
	}
	return strings.Join(parts, " · ")
}

// RunPhaseWord is a run phase in plain words.
func RunPhaseWord(phase string) string {
	switch phase {
	case "begin":
		return "starting the step"
	case "gather":
		return "gathering the party"
	case "walk":
		return "walking"
	case "pull":
		return "pulling"
	case "task":
		return "doing a task"
	case "rest":
		return "resting"
	case "wait":
		return "waiting"
	}
	return phase
}
