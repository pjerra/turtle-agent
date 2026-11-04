// autopilot.go: the raid autopilot's targets file and its decision rule, as pure functions. The command
// (cmd/turtle-agent/raid_autopilot_cmd.go) owns the processes, files and timers; this file answers one question: for
// this run and this goal, what should the autopilot do next? Walk 37 is the fixture set: an exit 0 after the walk's own
// `.bot run stop`, restarts used up, a 65-minute no-progress fight, and a failed placement that burned instances.
package raidwalk

import (
	"bufio"
	"fmt"
	"io"
	"strconv"
	"strings"
	"time"
)

// The autopilot's actions.
const (
	// AutoContinue: the run is alive and has shown progress inside the stall window.
	AutoContinue = "continue"
	// AutoRestart: tear the slot down and re-place the same goal.
	AutoRestart = "restart"
	// AutoAdvance: the goal is met; move to the id's next goal line.
	AutoAdvance = "advance"
	// AutoStop: the target cannot run (bad flags); stop supervising it instead of re-placing in a loop.
	AutoStop = "stop"
)

// AutoGoal is one target's goal: the plan walked whole, a leg stopped at, or one boss killed.
type AutoGoal struct {
	Kind string // "full", "leg" or "boss"
	Leg  string // Kind leg: the leg name or number
	Boss uint32 // Kind boss: the boss's creature entry
}

// ParseAutoGoal reads a goal token: full, leg:<L> or boss:<ENTRY>.
func ParseAutoGoal(s string) (AutoGoal, error) {
	switch {
	case s == "full":
		return AutoGoal{Kind: "full"}, nil
	case strings.HasPrefix(s, "leg:"):
		leg := strings.TrimPrefix(s, "leg:")
		if leg == "" || strings.ContainsAny(leg, " \t") {
			return AutoGoal{}, fmt.Errorf("goal %q: leg wants a leg name or number after the leg: prefix", s)
		}
		return AutoGoal{Kind: "leg", Leg: leg}, nil
	case strings.HasPrefix(s, "boss:"):
		n, err := strconv.ParseUint(strings.TrimPrefix(s, "boss:"), 10, 32)
		if err != nil || n == 0 {
			return AutoGoal{}, fmt.Errorf("goal %q: boss wants a creature entry after the boss: prefix", s)
		}
		return AutoGoal{Kind: "boss", Boss: uint32(n)}, nil
	}
	return AutoGoal{}, fmt.Errorf("goal %q: want full, leg:<L> or boss:<ENTRY>", s)
}

// String prints the goal as the targets file writes it.
func (g AutoGoal) String() string {
	switch g.Kind {
	case "leg":
		return "leg:" + g.Leg
	case "boss":
		return "boss:" + strconv.FormatUint(uint64(g.Boss), 10)
	}
	return "full"
}

// Until is the raid-walk --until clause the goal walks with.
func (g AutoGoal) Until() string {
	switch g.Kind {
	case "leg":
		return "leg " + g.Leg
	case "boss":
		// The boss goal is a kill: boss-done stops after it, not at its step.
		return "boss-done"
	}
	return "none"
}

// AutoTarget is one line of the targets file.
type AutoTarget struct {
	ID     string
	Server string
	Slot   string
	Holder string
	Goal   AutoGoal
	// Args are the raid-walk arguments after the plan, without the flags the autopilot manages (--server, --slot,
	// --holder, --until, --from, --place, --reuse-instance, --out).
	Args []string
	// Start is the target's own --from ("step N" or "leg L"), used for a goal's first placement; a restart inside the
	// goal starts at the furthest step the run reached instead.
	Start string
	Line  int
}

// ParseAutoTargets reads the targets file:
//
//	id server slot holder goal -- <raid-walk args...>
//
// One line per goal; an id may repeat, and its lines are walked in file order. Blank lines and lines starting with #
// are skipped. The goal is a single token (full, leg:<L>, boss:<ENTRY>); the `--` separates it from the raid-walk
// arguments. The autopilot manages --server/--slot/--holder/--from/--until/--place/--reuse-instance/--out, refuses
// slot 3 and --instance-cap-unsafe, and reads a --from in the arguments as the goal's start.
func ParseAutoTargets(r io.Reader) ([]AutoTarget, error) {
	var out []AutoTarget
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	for n := 1; sc.Scan(); n++ {
		line := strings.TrimSpace(sc.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		fields := strings.Fields(line)
		sep := -1
		for i, f := range fields {
			if f == "--" {
				sep = i
				break
			}
		}
		if sep < 0 {
			return nil, fmt.Errorf("line %d: no `--` between the header (id server slot holder goal) and the raid-walk args", n)
		}
		if sep != 5 {
			return nil, fmt.Errorf("line %d: the header is `id server slot holder goal --`, got %d words before `--`", n, sep)
		}
		id, server, slot, holder := fields[0], fields[1], fields[2], fields[3]
		if id == "" || strings.ContainsAny(id, " \t/") {
			return nil, fmt.Errorf("line %d: bad raid id %q", n, id)
		}
		if server == "" {
			return nil, fmt.Errorf("line %d: no server", n)
		}
		if slotN, err := strconv.Atoi(slot); err != nil {
			return nil, fmt.Errorf("line %d: bad slot %q", n, slot)
		} else if slotN == 3 {
			return nil, fmt.Errorf("line %d: the owner rule: slot 3 is never used", n)
		}
		if holder == "" {
			return nil, fmt.Errorf("line %d: no holder", n)
		}
		goal, err := ParseAutoGoal(fields[4])
		if err != nil {
			return nil, fmt.Errorf("line %d: %v", n, err)
		}
		t := AutoTarget{ID: id, Server: server, Slot: slot, Holder: holder, Goal: goal, Line: n}
		args, start, err := parseAutoArgs(fields[sep+1:])
		if err != nil {
			return nil, fmt.Errorf("line %d: %v", n, err)
		}
		if len(args) == 0 || strings.HasPrefix(args[0], "-") {
			return nil, fmt.Errorf("line %d: the raid-walk args must start with the plan name", n)
		}
		t.Args, t.Start = args, start
		out = append(out, t)
	}
	if err := sc.Err(); err != nil {
		return nil, err
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("no targets")
	}
	return out, nil
}

// parseAutoArgs drops the flags the autopilot manages and returns the rest, plus the target's --from.
func parseAutoArgs(in []string) (args []string, start string, err error) {
	for i := 0; i < len(in); i++ {
		a := in[i]
		name, _, hasEq := strings.Cut(a, "=")
		switch name {
		case "--instance-cap-unsafe":
			return nil, "", fmt.Errorf("--instance-cap-unsafe is never used by the autopilot")
		case "--server", "--slot", "--holder", "--until", "--out":
			if hasEq {
				continue
			}
			if i+1 >= len(in) {
				return nil, "", fmt.Errorf("%s needs a value", a)
			}
			i++
			continue
		case "--from":
			if hasEq {
				start = strings.Trim(strings.TrimPrefix(a, name+"="), `"`)
				continue
			}
			if i+2 >= len(in) || (in[i+1] != "step" && in[i+1] != "leg") {
				return nil, "", fmt.Errorf("--from wants `leg L` or `step N` (two words: --from leg L)")
			}
			start = in[i+1] + " " + in[i+2]
			i += 2
			continue
		case "--place", "--reuse-instance":
			continue
		}
		args = append(args, a)
	}
	return args, start, nil
}

// AutoObservation is what the autopilot read of one run.
type AutoObservation struct {
	Exited       bool
	ExitCode     int
	StepsReached int
	StepsTotal   int
	EndReason    string
	// KilledBosses is the entries the run's summary says went down.
	KilledBosses map[uint32]bool
	// LastProgress is the last time the run's step or a boss's HP changed (the run's own start when neither has).
	LastProgress time.Time
	// ObserveErr is a read failure (no summary yet, a truncated file): it is never a restart reason by itself.
	ObserveErr string
	// StallAfter is the earliest the stall rule may fire: a run that logged a legitimate wait (the instance cap's
	// "earliest free time") is not stalled before it. Zero: the usual rule.
	StallAfter time.Time
}

// PlacementFailed says the run never started: it ended in the place phase.
func (o AutoObservation) PlacementFailed() bool {
	return o.StepsReached == 0 && strings.HasPrefix(strings.TrimSpace(o.EndReason), "place:")
}

// RunStopped says the walk's own run stop ended the run (walk 37's b3-r1: exit 0, no boss kill).
func RunStopped(reason string) bool {
	r := strings.TrimSpace(reason)
	return strings.Contains(r, ".bot run stop") || strings.HasPrefix(r, "stopped:")
}

// GoalAchieved says the run met its goal.
func GoalAchieved(g AutoGoal, o AutoObservation) bool {
	if !o.Exited || o.ExitCode != 0 || RunStopped(o.EndReason) || o.PlacementFailed() {
		return false
	}
	switch g.Kind {
	case "boss":
		return o.KilledBosses[g.Boss]
	case "full":
		return o.StepsTotal > 0 && o.StepsReached >= o.StepsTotal
	case "leg":
		return o.StepsReached > 0
	}
	return false
}

// AutoRunState is what the autopilot knows about the current goal's run(s).
type AutoRunState struct {
	Goal    AutoGoal
	Attempt int
	// Reuse says the last attempt asked for --reuse-instance (the same copy) and was not a first placement.
	Reuse bool
	// HasRun says an earlier attempt of this goal exists: a restart is a re-place, not a first placement.
	HasRun bool
	// FastFailures counts the consecutive ends under two minutes with no step progress: the backoff doubles.
	FastFailures int
}

// AutoDecision is the next step.
type AutoDecision struct {
	Action  string
	Reuse   bool
	Backoff time.Duration
	Reason  string
}

// AutoBackoff is the wait before a restart after FastFailures fast failures: 20 s doubling to 10 minutes.
func AutoBackoff(fast int) time.Duration {
	d := 20 * time.Second
	for i := 0; i < fast && d < 10*time.Minute; i++ {
		d *= 2
	}
	if d > 10*time.Minute {
		d = 10 * time.Minute
	}
	return d
}

// DecideAutoAction is the whole rule: any end, a used-up restart budget, or a stall tears down and re-places; a met
// goal advances; a placement that failed on the old bind asks for a fresh instance next. A stall is no step and no
// boss-HP change for stall.
func DecideAutoAction(o AutoObservation, r AutoRunState, now time.Time, stall time.Duration) AutoDecision {
	if !o.Exited {
		if !o.StallAfter.IsZero() && now.Before(o.StallAfter) {
			return AutoDecision{Action: AutoContinue, Reason: "waiting on the instance cap until " + o.StallAfter.UTC().Format(time.RFC3339)}
		}
		if !o.LastProgress.IsZero() && now.Sub(o.LastProgress) >= stall {
			return AutoDecision{
				Action: AutoRestart,
				Reuse:  true,
				Reason: fmt.Sprintf("stalled: no step or boss-HP change for %s (step %d)", now.Sub(o.LastProgress).Round(time.Second), o.StepsReached),
			}
		}
		return AutoDecision{Action: AutoContinue, Reason: fmt.Sprintf("running, step %d", o.StepsReached)}
	}
	if GoalAchieved(r.Goal, o) {
		return AutoDecision{Action: AutoAdvance, Reason: "goal met: " + r.Goal.String()}
	}
	if o.ExitCode == 2 {
		return AutoDecision{Action: AutoStop, Reason: "exit 2: the walk refused its arguments"}
	}
	if o.PlacementFailed() {
		if r.Reuse {
			return AutoDecision{Action: AutoRestart, Reuse: false, Reason: "place failed on the old bind: a fresh instance next (" + shortReason(o.EndReason) + ")"}
		}
		return AutoDecision{Action: AutoRestart, Reuse: false, Backoff: AutoBackoff(r.FastFailures), Reason: "place failed again: " + shortReason(o.EndReason)}
	}
	switch o.ExitCode {
	case 3, 4, 5, 6, 26:
		return AutoDecision{Action: AutoRestart, Reuse: true, Backoff: AutoBackoff(r.FastFailures), Reason: fmt.Sprintf("exit %d: %s", o.ExitCode, shortReason(o.EndReason))}
	}
	reason := fmt.Sprintf("exit %d: %s", o.ExitCode, shortReason(o.EndReason))
	if RunStopped(o.EndReason) {
		reason = "the run stopped by itself (exit 0), the goal is not met"
	}
	return AutoDecision{Action: AutoRestart, Reuse: true, Reason: reason}
}

// shortReason clips a run's end reason for an EVENT line.
func shortReason(s string) string {
	s = strings.TrimSpace(strings.ReplaceAll(s, "\n", " "))
	if len(s) > 120 {
		s = s[:117] + "..."
	}
	if s == "" {
		return "(no reason)"
	}
	return s
}
