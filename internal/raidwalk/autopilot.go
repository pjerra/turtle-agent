// autopilot.go: the raid autopilot's targets file and its decision rule, as pure functions. The command
// (cmd/turtle-agent/raid_autopilot_cmd.go) owns the processes, files and timers; this file answers one question: for
// this run and this goal, what should the autopilot do next? Walk 37 is the fixture set: an exit 0 after the walk's own
// `.bot run stop`, restarts used up, a 65-minute no-progress fight, and a failed placement that burned instances.
package raidwalk

import (
	"bufio"
	"fmt"
	"io"
	"sort"
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
	// AutoHirePoolWait: the server's shared hire pool refused this raid ("No mercenary of that kind is available right
	// now"); wait AutoHirePoolWaitBackoff and re-place the same instance. Walk 39: several big raids on one server
	// starved each other, and each refusal counted as a no-progress attempt (m5's rest grew to 10m, m1's hit 20s then
	// 1m, and the failures moved the ids toward ESCALATE).
	AutoHirePoolWait = "hire-pool-wait"
)

// AutoHirePoolWaitBackoff is how long a raid backs off after the shared hire pool refused its hires: enough for the
// other raids of the server to finish hiring, and no cross-raid scheduling beyond it.
const AutoHirePoolWaitBackoff = 5 * time.Minute

// AutoHirePoolWaitMax is how many consecutive hire-pool refusals (~30 min at AutoHirePoolWaitBackoff) an id waits
// before it is left for a person: a pool that refuses six times in a row needs a person, not another wait, and no
// backoff loop ends by itself otherwise.
const AutoHirePoolWaitMax = 6

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
// slot 3, --instance-cap-unsafe and --inject, and reads a --from in the arguments as the goal's start.
//
// --sql is required: every goal may be re-placed (a restart asks for --place --from ...), and raid-walk refuses
// --place without --sql. Two ids may not name the same server and slot: one slot runs one walk at a time.
//
// This is the strict read (the start): the first bad line fails the whole file.
func ParseAutoTargets(r io.Reader) ([]AutoTarget, error) {
	out, issues, err := ParseAutoTargetsLenient(r, nil)
	if err != nil {
		return nil, err
	}
	if len(issues) > 0 {
		return nil, issues[0].Err
	}
	return out, nil
}

// AutoTargetIssue is one line (or id) a lenient read skipped. Err already carries the "line N: " prefix.
type AutoTargetIssue struct {
	Line int
	ID   string
	Err  error
}

// ParseAutoTargetsLenient is the re-read of a running autopilot (walk 40: one bad line made the whole file invalid
// and every other edit was ignored for ~60 minutes). A bad line is returned as an issue and every other line still
// parses; all lines of the bad line's id are left out of the result too, so the caller keeps that id's old state
// whole instead of acting on half its lines. inactive says an id holds no slot any more (done or stopped, no live
// run, no lease): its lines do not count in the one-walk-per-slot check, so a new id may take its slot. nil: every
// id holds its slot. A file with no valid line left is an error (a half-written file is not a stop order).
func ParseAutoTargetsLenient(r io.Reader, inactive func(id string) bool) ([]AutoTarget, []AutoTargetIssue, error) {
	var parsed []AutoTarget
	var issues []AutoTargetIssue
	bad := map[string]bool{}
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	for n := 1; sc.Scan(); n++ {
		line := strings.TrimSpace(sc.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		t, err := parseAutoTargetLine(n, line)
		if err != nil {
			id := strings.Fields(line)[0]
			issues = append(issues, AutoTargetIssue{Line: n, ID: id, Err: err})
			bad[id] = true
			continue
		}
		parsed = append(parsed, t)
	}
	if err := sc.Err(); err != nil {
		return nil, nil, err
	}
	// The one-walk-per-slot check, in file order: the first id that holds a slot keeps it, a later id naming it is
	// the bad one. An inactive id holds nothing.
	slotOwner := map[string]string{}
	for _, t := range parsed {
		if bad[t.ID] || (inactive != nil && inactive(t.ID)) {
			continue
		}
		key := t.Server + "/" + t.Slot
		if owner, ok := slotOwner[key]; ok && owner != t.ID {
			issues = append(issues, AutoTargetIssue{Line: t.Line, ID: t.ID, Err: fmt.Errorf("line %d: %s slot %s is already %s's: one slot runs one walk at a time", t.Line, t.Server, t.Slot, owner)})
			bad[t.ID] = true
			continue
		}
		slotOwner[key] = t.ID
	}
	var out []AutoTarget
	for _, t := range parsed {
		if !bad[t.ID] {
			out = append(out, t)
		}
	}
	sort.SliceStable(issues, func(i, j int) bool { return issues[i].Line < issues[j].Line })
	if len(out) == 0 {
		if len(issues) > 0 {
			return nil, issues, issues[0].Err
		}
		return nil, nil, fmt.Errorf("no targets")
	}
	return out, issues, nil
}

// parseAutoTargetLine parses one non-blank, non-comment line (n is its 1-based number).
func parseAutoTargetLine(n int, line string) (AutoTarget, error) {
	fields := strings.Fields(line)
	sep := -1
	for i, f := range fields {
		if f == "--" {
			sep = i
			break
		}
	}
	if sep < 0 {
		return AutoTarget{}, fmt.Errorf("line %d: no `--` between the header (id server slot holder goal) and the raid-walk args", n)
	}
	if sep != 5 {
		return AutoTarget{}, fmt.Errorf("line %d: the header is `id server slot holder goal --`, got %d words before `--`", n, sep)
	}
	id, server, slot, holder := fields[0], fields[1], fields[2], fields[3]
	if id == "" || strings.ContainsAny(id, " \t/") {
		return AutoTarget{}, fmt.Errorf("line %d: bad raid id %q", n, id)
	}
	if server == "" {
		return AutoTarget{}, fmt.Errorf("line %d: no server", n)
	}
	if slotN, err := strconv.Atoi(slot); err != nil {
		return AutoTarget{}, fmt.Errorf("line %d: bad slot %q", n, slot)
	} else if slotN == 3 {
		return AutoTarget{}, fmt.Errorf("line %d: the owner rule: slot 3 is never used", n)
	}
	if holder == "" {
		return AutoTarget{}, fmt.Errorf("line %d: no holder", n)
	}
	goal, err := ParseAutoGoal(fields[4])
	if err != nil {
		return AutoTarget{}, fmt.Errorf("line %d: %v", n, err)
	}
	t := AutoTarget{ID: id, Server: server, Slot: slot, Holder: holder, Goal: goal, Line: n}
	args, start, err := parseAutoArgs(fields[sep+1:])
	if err != nil {
		return AutoTarget{}, fmt.Errorf("line %d: %v", n, err)
	}
	if len(args) == 0 || strings.HasPrefix(args[0], "-") {
		return AutoTarget{}, fmt.Errorf("line %d: the raid-walk args must start with the plan name", n)
	}
	if !hasAutoFlag(args, "--sql") {
		return AutoTarget{}, fmt.Errorf("line %d: --sql FILE is required: every goal may be re-placed, and raid-walk refuses --place without --sql", n)
	}
	t.Args, t.Start = args, start
	return t, nil
}

// parseAutoArgs drops the flags the autopilot manages and returns the rest, plus the target's --from.
func parseAutoArgs(in []string) (args []string, start string, err error) {
	for i := 0; i < len(in); i++ {
		a := in[i]
		name, _, hasEq := strings.Cut(a, "=")
		switch name {
		case "--instance-cap-unsafe":
			return nil, "", fmt.Errorf("--instance-cap-unsafe is never used by the autopilot")
		case "--inject":
			return nil, "", fmt.Errorf("--inject is never used by the autopilot: the walk's own faults are not a supervised run")
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
		case "--gm-instance-data":
			// The flag's value holds spaces (`<leg>:<a> <b>[;<a> <b>...]`), but targets.txt is split on
			// whitespace with no quoting: the words after the flag (up to the next flag) are its value, joined
			// back with single spaces. One argv (`--gm-instance-data=<value>`) reaches the walk whole, since the
			// supervisor launches it directly (no shell re-split).
			val := ""
			if hasEq {
				_, val, _ = strings.Cut(a, "=")
			}
			var words []string
			if val != "" {
				words = append(words, val)
			}
			for i+1 < len(in) && !strings.HasPrefix(in[i+1], "-") {
				i++
				words = append(words, in[i])
			}
			val = strings.Join(words, " ")
			if strings.TrimSpace(val) == "" {
				return nil, "", fmt.Errorf("--gm-instance-data needs a value (\"<leg>:<a> <b>[;<a> <b>...]\")")
			}
			args = append(args, "--gm-instance-data="+val)
			continue
		}
		args = append(args, a)
	}
	return args, start, nil
}

// hasAutoFlag reports whether name is among args, bare or as name=value.
func hasAutoFlag(args []string, name string) bool {
	for _, a := range args {
		if n, _, _ := strings.Cut(a, "="); n == name {
			return true
		}
	}
	return false
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
	// HirePoolWait says the end was the shared hire pool's refusal (no mercenary of that kind): a wait, not a failure.
	HirePoolWait bool
}

// PlacementFailed says the run never started: it ended in the place phase.
func (o AutoObservation) PlacementFailed() bool {
	return o.StepsReached == 0 && strings.HasPrefix(strings.TrimSpace(o.EndReason), "place:")
}

// HirePoolRefused says a run's end reason is the server's shared hire pool refusing a hire ("No mercenary of that
// kind is available right now", a partial hire's TBM:HIRE_RAID line; walk 39: m5 03:58 and 04:12, m1 04:20).
func HirePoolRefused(reason string) bool {
	return strings.Contains(strings.ToLower(reason), "no mercenary of that kind")
}

// RunStopped says the walk's own run stop ended the run (walk 37's b3-r1: exit 0, no boss kill).
func RunStopped(reason string) bool {
	r := strings.TrimSpace(reason)
	return strings.Contains(r, ".bot run stop") || strings.HasPrefix(r, "stopped:")
}

// autoBossIDs is the id set of a boss the walk reports under another entry than a goal may name. Zul'Gurub's Thekal
// is the case walk 39 found: his boss row and the summary's kill are 14599 (the plan's boss entry, phase 2), and a
// goal of boss:14509 (his reborn form) never matched, so t7 restarted a raid whose boss was already dead (t7-1's kill
// at 04:06:06Z was followed by a restart launch at 04:06:42Z, and t7 escalated at 04:47:56Z). The plan's boss.phase_entries is
// not the set: it also lists Thekal's Zulian Guardians 15068, which exist several at once and are never the boss's own
// unit (the walk's bossUnits excludes them). Only entries the evidence shows as the same boss go here.
var autoBossIDs = map[uint32][]uint32{
	14509: {14509, 14599}, // Thekal: the row is 14599, the reborn form 14509
	14599: {14599, 14509},
}

// GoalBossIDs is the entries a kill of which meets a goal boss: the goal's own entry (always) and the entries of the
// boss the walk reports it as.
func GoalBossIDs(boss uint32) []uint32 {
	if ids := autoBossIDs[boss]; len(ids) > 0 {
		return ids
	}
	return []uint32{boss}
}

// GoalBossDead says the run's summary reported the goal boss dead (a kill of any entry of it, see GoalBossIDs).
func GoalBossDead(g AutoGoal, o AutoObservation) bool {
	if g.Kind != "boss" {
		return false
	}
	for _, e := range GoalBossIDs(g.Boss) {
		if o.KilledBosses[e] {
			return true
		}
	}
	return false
}

// GoalAchieved says the run met its goal. A boss the summary's boss_kills says was killed counts whatever the exit
// code and whatever the end reason: an incident after the kill (19) or a "stopped: ..." ending (the 90-minute limit
// firing after the kill) still met the goal. A kill of any entry of the boss counts (GoalBossIDs): Thekal's 14509 and
// 14599 are one boss. A full clear and a leg goal still need a run that was not stopped by the walk's own run stop
// and did not fail in the place phase; a leg goal needs exit 0.
func GoalAchieved(g AutoGoal, o AutoObservation) bool {
	if !o.Exited {
		return false
	}
	if GoalBossDead(g, o) {
		return true
	}
	if RunStopped(o.EndReason) || o.PlacementFailed() {
		return false
	}
	switch g.Kind {
	case "boss":
		return false
	case "full":
		return o.StepsTotal > 0 && o.StepsReached >= o.StepsTotal
	case "leg":
		return o.ExitCode == 0 && o.StepsReached > 0
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
	// FastFailures counts the consecutive attempts of this goal that reached no further step: the backoff grows.
	FastFailures int
	// LeaseFails counts the consecutive attempts that ended in exit 15 (the slot's lease is held by another holder):
	// the third stops the id for a person.
	LeaseFails int
	// PlaceFails counts the consecutive attempts that ended in the place phase: the third stops the id for a person
	// (a dead bind is re-placed fresh, but a placement that keeps failing must not loop forever with a backoff).
	PlaceFails int
	// HireWaits counts the consecutive hire-pool waits of this goal: the sixth stops the id for a person.
	HireWaits int
}

// AutoDecision is the next step.
type AutoDecision struct {
	Action  string
	Reuse   bool
	Backoff time.Duration
	Reason  string
	// Escalate says the id stops and needs a person: an exit 2 (the walk refused its arguments), an exit 29 (the
	// placement failed every try in the fresh instance of its automatic retry), or
	// three consecutive exit 15s (the slot's lease is held by someone else).
	Escalate bool
}

// AutoBackoff is the wait before a restart after FastFailures consecutive no-progress attempts: 20 s, 1 m, 2 m, 5 m,
// then 10 minutes for every further one. The count resets when an attempt reaches a further step.
func AutoBackoff(fast int) time.Duration {
	steps := [...]time.Duration{20 * time.Second, time.Minute, 2 * time.Minute, 5 * time.Minute, 10 * time.Minute}
	if fast < 0 {
		fast = 0
	}
	if fast >= len(steps) {
		fast = len(steps) - 1
	}
	return steps[fast]
}

// DecideAutoAction is the whole rule: any end, a used-up restart budget, or a stall tears down and re-places; a met
// goal advances; a placement that failed on the old bind asks for a fresh instance next. A stall is no step and no
// boss-HP change for stall, counted from the end of a legitimate instance-cap wait. Exit 2 stops the id for a
// person, exit 29 retries once in a fresh instance (a second one stops it), and so does the third consecutive exit 15
// or the sixth consecutive hire-pool wait. Every restart carries the backoff of its no-progress count.
func DecideAutoAction(o AutoObservation, r AutoRunState, now time.Time, stall time.Duration) AutoDecision {
	if !o.Exited {
		// A run waiting on the instance cap is not stalled: its clock starts when the wait ends.
		progress := o.LastProgress
		if !o.StallAfter.IsZero() {
			if now.Before(o.StallAfter) {
				return AutoDecision{Action: AutoContinue, Reason: "waiting on the instance cap until " + o.StallAfter.UTC().Format(time.RFC3339)}
			}
			if o.StallAfter.After(progress) {
				progress = o.StallAfter
			}
		}
		if !progress.IsZero() && now.Sub(progress) >= stall {
			return AutoDecision{
				Action:  AutoRestart,
				Reuse:   true,
				Backoff: AutoBackoff(r.FastFailures),
				Reason:  fmt.Sprintf("stalled: no step or boss-HP change for %s (step %d)", now.Sub(progress).Round(time.Second), o.StepsReached),
			}
		}
		return AutoDecision{Action: AutoContinue, Reason: fmt.Sprintf("running, step %d", o.StepsReached)}
	}
	if GoalAchieved(r.Goal, o) {
		return AutoDecision{Action: AutoAdvance, Reason: "goal met: " + r.Goal.String()}
	}
	if o.HirePoolWait {
		if r.HireWaits+1 >= AutoHirePoolWaitMax {
			return AutoDecision{
				Action: AutoStop, Escalate: true,
				Reason: fmt.Sprintf("hire pool: %d waits", r.HireWaits+1),
			}
		}
		return AutoDecision{
			Action: AutoHirePoolWait, Reuse: true, Backoff: AutoHirePoolWaitBackoff,
			Reason: "the shared hire pool has no mercenary of that kind: another raid of the server holds them",
		}
	}
	if o.ExitCode == 2 {
		return AutoDecision{Action: AutoStop, Escalate: true, Reason: "exit 2: the walk refused its arguments (" + shortReason(o.EndReason) + ")"}
	}
	if o.ExitCode == 29 {
		// Walk 40: 45 ids stopped here after a wipe and a person re-added each one (a changed line starts a fresh
		// instance). A same-instance placement that failed every try gets ONE automatic fresh-instance retry; a
		// fresh one that also ended in 29 stops the id.
		if r.Reuse {
			return AutoDecision{Action: AutoRestart, Reuse: false, Backoff: AutoBackoff(r.FastFailures), Reason: "fresh-instance retry after exit 29: the placement failed every try in the same instance (" + shortReason(o.EndReason) + ")"}
		}
		return AutoDecision{Action: AutoStop, Escalate: true, Reason: "exit 29: the placement failed in a fresh instance too; no further retry (" + shortReason(o.EndReason) + ")"}
	}
	if o.ExitCode == 15 && r.LeaseFails+1 >= 3 {
		return AutoDecision{Action: AutoStop, Escalate: true, Reason: "exit 15 three times: the slot's lease is held by another holder (" + shortReason(o.EndReason) + ")"}
	}
	backoff := AutoBackoff(r.FastFailures)
	if o.PlacementFailed() {
		if r.PlaceFails+1 >= 3 {
			return AutoDecision{Action: AutoStop, Escalate: true, Reason: fmt.Sprintf("place failed %d times in a row: %s", r.PlaceFails+1, shortReason(o.EndReason))}
		}
		if r.Reuse {
			return AutoDecision{Action: AutoRestart, Reuse: false, Backoff: backoff, Reason: "place failed on the old bind: a fresh instance next (" + shortReason(o.EndReason) + ")"}
		}
		return AutoDecision{Action: AutoRestart, Reuse: false, Backoff: backoff, Reason: "place failed again: " + shortReason(o.EndReason)}
	}
	reason := fmt.Sprintf("exit %d: %s", o.ExitCode, shortReason(o.EndReason))
	if RunStopped(o.EndReason) {
		reason = "the run stopped by itself (exit 0), the goal is not met"
	}
	return AutoDecision{Action: AutoRestart, Reuse: true, Backoff: backoff, Reason: reason}
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
