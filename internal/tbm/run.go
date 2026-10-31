package tbm

import (
	"fmt"
	"regexp"
	"strconv"
	"strings"
)

// The run leader (TortoiseBots docs/guides/dungeon-run-leader.md, `.bot run`): a bound party clears a dungeon plan by itself, and
// the commander watches its status line. When packs were left alive the run leader appends an optional trailing field:
//
//	TBM:RUN|<plan>|<state>|<step>/<steps>|<leg>|<kind>|<phase>|<note>[|left <K>][|<label>]
//
// The `left` field is present only when K > 0. A GameMaster's test run above speed 1.0 and a run started with `until`
// add a last label field, after `left` (dungeon-run-leader.md, `.bot run start ... until ... speed=`): "speed 2.0 (test)",
// "speed 2.0 (test; 1.0 since a real player joined)", "until boss (step 38)", or both joined with "; ". The parser takes
// the label only as the LAST field made of such parts, then `left <digits>` only as the field before it (or the last one).
// Anything else stays note text, so old lines and fields it does not know parse exactly as before.
//
// During a scripted event (a `hold` step) the phase reads "hold <dead>/<seen> down" or "fight <dead>/<seen> down".
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
	Leg   string `json:"leg"`            // the leg's name, or its number; empty when the module sent "-"
	Kind  string `json:"kind"`           // the step kind; empty for "-"
	Phase string `json:"phase"`          // begin, gather, walk, pull, task, rest, wait, hold, or a hold step's "hold|fight <d>/<s> down"; empty for "-"
	Note  string `json:"note"`           // what it does, or why it paused; empty for "-"
	Left  int    `json:"left,omitempty"` // packs the run leader left alive; only the trailing `left <K>` field sets it
	// Label is the trailing speed/until label as sent ("speed 3.0 (test); until boss (step 38)"); empty without one.
	Label string `json:"label,omitempty"`
	// Speed is the label's test speed (0 without a speed part); SpeedFellBack is set once a real player joined and the
	// run went back to normal speed ("speed 2.0 (test; 1.0 since a real player joined)").
	Speed         float64 `json:"speed,omitempty"`
	SpeedFellBack bool    `json:"speed_fell_back,omitempty"`
	// Until is the label's until clause without its step ("until boss", "until leg gehennas"); UntilStep the step it
	// stops before (0 when the label names none).
	Until     string `json:"until,omitempty"`
	UntilStep int    `json:"until_step,omitempty"`
	// EventDown and EventSeen are a hold step's event count from the phase ("fight 1/4 down": 1 and 4); EventFight is
	// set for "fight", clear for "hold".
	EventDown  int    `json:"event_down,omitempty"`
	EventSeen  int    `json:"event_seen,omitempty"`
	EventFight bool   `json:"event_fight,omitempty"`
	Line       string `json:"line,omitempty"`
}

// ParseRunLine reads a TBM:RUN line. The note may hold anything but a line break; a trailing field matching exactly
// `left <digits>` is the packs-left count and is stripped from the note. A line with fewer than eight fields or a step
// that is not "n/m" is not a status line.
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
	// The note holds no '|' of its own: only a known trailing label and then a `left <K>` field are taken off its end;
	// anything else stays note text, so old lines parse exactly as before.
	note, left := f[6], 0
	var lab runLabel
	if i := strings.LastIndexByte(note, '|'); i >= 0 {
		if l, ok := parseRunLabel(note[i+1:]); ok {
			lab, note = l, note[:i]
		}
	}
	if i := strings.LastIndexByte(note, '|'); i >= 0 {
		if n, ok := leftField(note[i+1:]); ok {
			left, note = n, note[:i]
		}
	}
	st := RunStatus{Plan: dash(f[0]), State: f[1], Step: step, Steps: steps, Leg: dash(f[3]), Kind: dash(f[4]), Phase: dash(f[5]), Note: dash(note), Left: left,
		Label: lab.text, Speed: lab.speed, SpeedFellBack: lab.fellBack, Until: lab.until, UntilStep: lab.untilStep, Line: strings.TrimSpace(line)}
	st.EventDown, st.EventSeen, st.EventFight = parseEventPhase(st.Phase)
	return st, true
}

// leftField reads `left <digits>` (K > 0, no leading zero).
func leftField(s string) (int, bool) {
	digits, ok := strings.CutPrefix(s, "left ")
	if !ok || digits == "" {
		return 0, false
	}
	n, err := strconv.Atoi(digits)
	if err != nil || n <= 0 || strconv.Itoa(n) != digits {
		return 0, false
	}
	return n, true
}

// runLabel is the trailing label of a run line, read.
type runLabel struct {
	text      string
	speed     float64
	fellBack  bool
	until     string
	untilStep int
}

var (
	runSpeedRe = regexp.MustCompile(`^speed ([0-9]+(?:\.[0-9]+)?) \(test(; [0-9.]+ since a real player joined)?\)$`)
	runUntilRe = regexp.MustCompile(`^(until (?:boss|step [0-9]+|leg \S+))(?: \(step ([0-9]+)\))?$`)
	runEventRe = regexp.MustCompile(`^(hold|fight) ([0-9]+)/([0-9]+) down$`)
)

// parseRunLabel reads a label field: one or more "; "-joined parts, each a speed part or an until part. A field with
// any other part is no label (it stays note text).
func parseRunLabel(s string) (runLabel, bool) {
	if s == "" {
		return runLabel{}, false
	}
	l := runLabel{text: s}
	for _, part := range splitLabel(s) {
		if m := runSpeedRe.FindStringSubmatch(part); m != nil && l.speed == 0 {
			v, err := strconv.ParseFloat(m[1], 64)
			if err != nil || v <= 0 {
				return runLabel{}, false
			}
			l.speed, l.fellBack = v, m[2] != ""
			continue
		}
		if m := runUntilRe.FindStringSubmatch(part); m != nil && l.until == "" {
			l.until = m[1]
			if m[2] != "" {
				l.untilStep, _ = strconv.Atoi(m[2])
			}
			continue
		}
		return runLabel{}, false
	}
	return l, true
}

// splitLabel splits a label at the "; " that joins its parts, but not inside the speed part's parentheses
// ("speed 2.0 (test; 1.0 since a real player joined)").
func splitLabel(s string) []string {
	var parts []string
	depth, start := 0, 0
	for i := 0; i < len(s); i++ {
		switch s[i] {
		case '(':
			depth++
		case ')':
			if depth > 0 {
				depth--
			}
		case ';':
			if depth == 0 && i+1 < len(s) && s[i+1] == ' ' {
				parts = append(parts, s[start:i])
				start = i + 2
				i++
			}
		}
	}
	return append(parts, s[start:])
}

// parseEventPhase reads a hold step's phase "hold <dead>/<seen> down" or "fight <dead>/<seen> down".
func parseEventPhase(phase string) (down, seen int, fight bool) {
	m := runEventRe.FindStringSubmatch(phase)
	if m == nil {
		return 0, 0, false
	}
	down, _ = strconv.Atoi(m[2])
	seen, _ = strconv.Atoi(m[3])
	return down, seen, m[1] == "fight"
}

// EventPhase reports whether the phase is a hold step's event count.
func (s RunStatus) EventPhase() bool { return s.EventSeen > 0 || runEventRe.MatchString(s.Phase) }

// Same reports whether two statuses say the same thing (the line may differ in nothing else).
func (s RunStatus) Same(o RunStatus) bool {
	return s.Plan == o.Plan && s.State == o.State && s.Step == o.Step && s.Steps == o.Steps && s.Leg == o.Leg && s.Kind == o.Kind && s.Phase == o.Phase && s.Note == o.Note && s.Left == o.Left && s.Label == o.Label
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
	if s.EventPhase() && s.State == RunRunning {
		word := "holding for the event"
		if s.EventFight {
			word = "event fight"
		}
		parts = append(parts, fmt.Sprintf("%s (%d of %d down)", word, s.EventDown, s.EventSeen))
	} else if w := RunPhaseWord(s.Phase); w != "" && s.State == RunRunning {
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
