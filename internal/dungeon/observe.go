package dungeon

import (
	"context"
	"errors"
	"fmt"
	"math"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/pjerra/turtle-agent/internal/events"
	"github.com/pjerra/turtle-agent/internal/packet"
	"github.com/pjerra/turtle-agent/internal/tbm"
)

// Observe mode (dungeon_run mode "observe"; README "Observe mode"). A TortoiseBots run leader (`.bot run`, TortoiseBots
// docs/guides/dungeon-run-leader.md) clears the dungeon plan by itself; the agent only prepares the party as in commander mode
// (hire, bind, enter), sends `.bot run start <plan>` and WATCHES: it reads the TBM:RUN status line (pushed on every change, and polled
// with `.bot run status` as a safety net) and feeds step, leg, kind, phase and note into the report, the trace, the events and the
// dashboard. After the start it sends no order, because any `.bot cmd` order but `state` and any `.bot action` pauses the run. The
// commands it does send are the run's own: pause, resume, skip and stop (RunControl, the dungeon_pause / dungeon_resume / dungeon_stop
// RPCs), `run status`, and a `run stop` when the watch ends with the run still alive on the server.

// ObserveSpec are the choices and ceilings of an observed run. A zero value takes its default.
type ObserveSpec struct {
	// Plan is the plan's name on the server (ai_dungeon_plan.plan); empty: the plan file's name, lower case.
	Plan string
	// Poll is how often `.bot run status` is asked when nothing came in (default 15 s).
	Poll time.Duration
	// MaxRun is the ceiling of the whole run, from `run start` (default 3 h). Past it the run is stopped and the watch fails.
	MaxRun time.Duration
	// Stall is how long a running run may show the very same status line (default 10 min; the module's own longest ceiling is a boss at
	// 7 min). Past it the run is stopped and the watch fails.
	Stall time.Duration
	// PausedWait is how long a paused run waits for a resume (by a person on the server, or dungeon_resume) before the watch stops it and
	// fails (default 10 min). The run leader pauses itself at 3 dead, an unsupported step or a ceiling and never goes on alone.
	PausedWait time.Duration
	// Maintenance sends `.bot maintenance <bot>` for every bot at the preflight, as a driven run does. Off by default: maintenance
	// walks each bot to a vendor, bank or mailbox, and a party scattered over the maps right after autogear pauses the run leader ("the
	// lead bot is not on the plan's map"). The inventory and talent reads still run.
	Maintenance bool
	// AllowFarStart lets a partway start go on although the lead bot stands more than farStartYd from the start step's point: the note
	// says so and the run leader walks there (or pauses) on its own. Off by default: a party in the wrong place is refused.
	AllowFarStart bool
	// Speed is the test walking speed of an observed run (`.bot run start <plan> [from ...] speed=<1.0-4.0>`, a GM-only
	// test option; owner request 2026-10-25): 0 or 1 is omitted from the start line (a normal run). Above 1.0 the server
	// walks the bots faster between fights and marks every status line with `|speed <x> (test)`; such a run never clears.
	Speed float64
	// KeepLootSetting leaves the bots' corpse looting as the operator set it: the start sends no `.bot behavior <bot> loot on` (the
	// run start's undo of a crashed run's boss `loot off`). Off by default. Observe only: a driven run always sends the undo, since
	// its legs turn looting off and on themselves and a crashed run may have left it off.
	KeepLootSetting bool
}

// Observe defaults.
const (
	DefaultObservePoll       = 15 * time.Second
	DefaultObserveMaxRun     = 3 * time.Hour
	DefaultObserveStall      = 10 * time.Minute
	DefaultObservePausedWait = 10 * time.Minute
	// observeGone is how long a "none" status (no run on the server) is tolerated right after the start before the run counts as gone.
	observeGone = 20 * time.Second
	// MaxRunChanges bounds the changes kept in the report.
	MaxRunChanges = 300
	// wipeDeadReArm is the dead-member count below which the wipe latch re-arms (TortoiseBots DungeonRunPolicy.kWipeDead, 3): a party
	// that is all dead, one member alive for a poll, all dead again, is one collapse, not two.
	wipeDeadReArm = 3
	// wipeWindow is a wipe counted from the feed's deaths: every member of the party dying within this long, even when the lead bot
	// revives in between (the RFC run: all five dead within 63 s, never in one frame).
	wipeWindow = 90 * time.Second
)

func (s ObserveSpec) withDefaults(plan *Plan) ObserveSpec {
	if s.Plan == "" {
		s.Plan = strings.ToLower(plan.Name)
	}
	if s.Poll <= 0 {
		s.Poll = DefaultObservePoll
	}
	if s.MaxRun <= 0 {
		s.MaxRun = DefaultObserveMaxRun
	}
	if s.Stall <= 0 {
		s.Stall = DefaultObserveStall
	}
	if s.PausedWait <= 0 {
		s.PausedWait = DefaultObservePausedWait
	}
	return s
}

// PartialStart is where an observed run's `.bot run start <plan> from ...` puts the run leader (#248).
type PartialStart struct {
	// Words are the `from ...` words: "from 4", "from leg serpentis", "from step 207"; "" for the first step.
	Words string
	// Step is the requested step in the run leader's own numbering when the mapping knows it (from_step, from_pull_group); 0 when only
	// the leg is known. It is the expected value of the start reply's step; the reply never changes it.
	Step int
	// Leg is the plan leg it starts at (-1 for a step start), LegNumber the leg's number in the exported rows and LegName the name sent
	// ("" when the number was sent because the name is not safe for the command line or is not unique). ExpectLeg is the plan leg's name
	// for a step start whose leg the mapping knows (it may be empty when the rows could not be reproduced).
	Leg       int
	LegNumber int
	LegName   string
	ExpectLeg string
	// Point is where the start step (or the leg's first point) stands on the plan's map; HavePoint says it is known. It is the party's
	// place: a partway start is refused when the lead bot stands far from it (ObserveSpec.AllowFarStart overrides).
	Point     Point
	HavePoint bool
}

// StartFromResolver maps an observed run's partial start (from_leg, from_pull_group, from_step) to how it starts once the server's step
// count for the plan is known. The mapping is the cmd layer's (it owns the plan exporter); the driver only asks. An error refuses the
// run before a bot is hired.
type StartFromResolver func(serverSteps int) (PartialStart, error)

// RunLeaderWatcher is the optional part of a RemoteGame that has read the TBM:RUN lines the module pushes: the newest one and how
// many have been read. Without it the observer relies on the replies to `.bot run start` and `.bot run status`.
type RunLeaderWatcher interface {
	RunStatus() (st tbm.RunStatus, n uint64, ok bool)
}

// RunChange is one change of the run leader's status as the observer saw it.
type RunChange struct {
	At    time.Time `json:"at"`
	State string    `json:"state"`
	Step  int       `json:"step"`
	Steps int       `json:"steps"`
	Leg   string    `json:"leg,omitempty"`
	Kind  string    `json:"kind,omitempty"`
	Phase string    `json:"phase,omitempty"`
	Note  string    `json:"note,omitempty"`
	Left  int       `json:"left,omitempty"`  // packs the run leader left alive
	Speed string    `json:"speed,omitempty"` // the test speed (`speed <x> (test)`); a run with it never clears
}

// ObserveReport is the report's part for an observed run (Report.Remote.Observe).
type ObserveReport struct {
	// Plan is the plan's name on the server.
	Plan string `json:"plan"`
	// The newest status: what the run leader is doing now.
	State string `json:"state,omitempty"` // running, paused, done or none
	Step  int    `json:"step"`
	Steps int    `json:"steps"`
	Leg   string `json:"leg,omitempty"`
	Kind  string `json:"kind,omitempty"`
	Phase string `json:"phase,omitempty"`
	Note  string `json:"note,omitempty"`
	Left  int    `json:"left,omitempty"`  // packs the run leader left alive
	Speed string `json:"speed,omitempty"` // the test speed (`speed <x> (test)`); a run with it never clears
	// Text is the status in plain words ("sentinel · pull 7/41 · pulling").
	Text string `json:"text,omitempty"`
	// Started: `.bot run start` was acknowledged (or an own run of the plan was adopted).
	Started bool `json:"started"`
	// Lines are the status lines taken in, Polls the `run status` asks, Pauses the times the run went to paused.
	Lines  int `json:"lines"`
	Polls  int `json:"polls"`
	Pauses int `json:"pauses"`
	// Controls are the pause, resume, skip and stop requests sent, in order, each with its answer.
	Controls []string `json:"controls,omitempty"`
	// PlayerOrders are the pauses the module made for a player's order (the observer's `cmd summon`, a move), each with the step and the order.
	PlayerOrders []string `json:"player_orders,omitempty"`
	// Ended is how the watch ended: done, or the reason of a failure or stop.
	Ended string `json:"ended,omitempty"`
	// Changes are the status changes, oldest first (the newest MaxRunChanges).
	Changes []RunChange `json:"changes"`
}

func (r *ObserveReport) clone() *ObserveReport {
	if r == nil {
		return nil
	}
	c := *r
	c.Controls = append([]string(nil), r.Controls...)
	c.PlayerOrders = append([]string(nil), r.PlayerOrders...)
	c.Changes = append([]RunChange{}, r.Changes...)
	return &c
}

// observeState is the observer's own state.
type observeState struct {
	spec       ObserveSpec
	started    bool
	startedAt  time.Time
	last       tbm.RunStatus
	haveLast   bool
	lastAt     time.Time // when the status last changed
	pausedAt   time.Time
	seenN      uint64
	baseN      uint64 // lines read before the start: older ones are another run's
	lastPoll   time.Time
	serverEnd  bool                      // the module said the run is done: no stop is needed
	wiped      bool                      // the feed showed the whole party dead: one rising edge of it is one wipe
	wasDead    map[packet.GUID]bool      // dead in the previous frame (nil until the first frame)
	deathAt    map[packet.GUID]time.Time // when each member last went dead
	leadDownAt time.Time                 // when the lead bot last went dead
	leadDown   bool
	lastWipeAt time.Time // when the last wipe was counted
	// The run leader's own rest (observe mode has no gate of the agent's): the phase rest of its status is the gate.
	restSince time.Time // when the run leader's current rest phase began
	restOpen  bool
	// playerPaused: the run is paused for a player's order (the module's pause for a `cmd`), so the status says why it waits.
	playerPaused bool

	mu   sync.Mutex
	reqs []string // pause, resume, skip requests from the RPC
}

// RunControl asks the observed run for a pause, a resume or a skip (the RPC's way to steer it); the watch sends it on its next look.
// The error is for a request that makes no sense: not an observed run, or a verb the run leader does not have.
func (d *RemoteDriver) RunControl(verb string) error {
	if d.opt.Observe == nil || d.obs == nil {
		return errors.New("the run is not an observed one (mode observe)")
	}
	verb = strings.ToLower(strings.TrimSpace(verb))
	switch verb {
	case "pause", "resume", "skip":
	default:
		return fmt.Errorf("run control %q: use pause, resume or skip (dungeon_stop stops the run)", verb)
	}
	d.obs.mu.Lock()
	defer d.obs.mu.Unlock()
	if len(d.obs.reqs) >= 8 {
		return errors.New("too many run control requests waiting")
	}
	d.obs.reqs = append(d.obs.reqs, verb)
	return nil
}

func (d *RemoteDriver) observing() bool { return d.opt.Observe != nil }

// Observing is whether the run is an observed one (mode observe): the RPC's dungeon_pause, dungeon_resume and dungeon_skip go to the run
// leader for it, and act on the commander's own run otherwise.
func (d *RemoteDriver) Observing() bool { return d.observing() }

// runLeaderRefusal explains why the module refused `.bot run <verb>` with this code.
func runLeaderRefusal(verb, plan string, e *tbm.Err) string {
	if e == nil {
		return "no reason given"
	}
	hint := map[string]string{
		"disabled":         "the server's run leader is off (AiPlayerbot.DungeonSelfClear and AiPlayerbot.RemoteCommander must both be 1)",
		"not-bound":        "the commander binding is gone",
		"no-plan":          fmt.Sprintf("the server has no dungeon plan %q: export it with `turtle-agent plan-export`, apply the migration and restart the server (`.bot run list` names what is loaded)", plan),
		"wrong-map":        "the lead bot is not on the plan's map: the party must be inside the dungeon first",
		"busy":             "a run is already going on the server",
		"lead-unavailable": "the lead bot is between maps",
		"no-run":           "no run is going",
		"state":            "the run is not in a state that allows it",
		"no-leg":           "the run leader's plan has no such leg: from_leg counts the exported rows (an optional leg takes no number); start at a later leg or pass observe.from_step",
		"no-step":          "the run leader's plan has no such step: observe.from_step is the number .bot run status shows, from 1",
		"not-in-instance":  "a partway start needs the party inside the instance: enter it first, or start from the beginning",
		"party-scattered":  "a partway start needs the whole party in the lead bot's instance (.bot cmd summon first)",
	}[e.Code]
	if hint == "" {
		return fmt.Sprintf("%s (%s)", e.Code, e.Message)
	}
	return fmt.Sprintf("%s: %s", e.Code, hint)
}

// probeRunLeader asks the server for its plans before anything is prepared, so that a server without the run leader (or without the plan)
// is refused before bots are hired. `.bot run list` needs no binding.
func (d *RemoteDriver) probeRunLeader(ctx context.Context) error {
	spec := d.opt.Observe.withDefaults(d.plan)
	r, err := d.g.Command(ctx, "run list")
	if err != nil {
		return fmt.Errorf("observe mode: .bot run list: %w", err)
	}
	if r.Err != nil {
		return fmt.Errorf("observe mode: the server refused .bot run list: %s", runLeaderRefusal("list", spec.Plan, r.Err))
	}
	list := ""
	for _, l := range r.Lines {
		if rest, ok := strings.CutPrefix(l, "Dungeon plans: "); ok {
			list = rest
		}
	}
	if r.Kind == tbm.KindTimeout && list == "" {
		return errors.New("observe mode needs the TortoiseBots run leader (`.bot run`, TortoiseBots PRs #962 and #963): this server did not answer .bot run list, so it has no run leader. Use mode \"commander\" (the agent leads the bots) or \"leader\" instead")
	}
	if list != "" && !strings.Contains(" "+strings.ReplaceAll(strings.ReplaceAll(list, ",", " "), "(", " ")+" ", " "+spec.Plan+" ") {
		return fmt.Errorf("observe mode: the server's run leader has no plan %q (it lists: %s): export the plan with `turtle-agent plan-export`, apply the migration and restart the server", spec.Plan, list)
	}
	d.planSteps = listedSteps(list, spec.Plan)
	if d.opt.StartFrom != nil {
		// A partial start is mapped here, before anything is hired: an unmappable from_pull_group must not pay for a party first.
		start, err := d.opt.StartFrom(d.planSteps)
		if err != nil {
			return fmt.Errorf("observe mode: %w", err)
		}
		d.start = &start
		if start.Words != "" {
			d.rep.note(fmt.Sprintf("observe mode: the run leader starts partway (`run start %s %s`; the server lists %d steps)", spec.Plan, start.Words, d.planSteps))
		}
	}
	d.rep.note("observe mode: the server's run leader answered; plans: " + nonEmpty(list, "(not listed)"))
	return nil
}

// listedSteps reads a plan's step count out of `.bot run list` ("wailing_caverns (map 43, 226 steps)"). 0: the plan is not in the
// list, or the list does not carry a count (an older module).
func listedSteps(list, plan string) int {
	// The plan name is followed by its map and count; a name is [a-z0-9_-] (planNameRe in the RPC layer).
	re := regexp.MustCompile(`(?:^|, )` + regexp.QuoteMeta(plan) + ` \(map \d+, (\d+) steps`)
	if m := re.FindStringSubmatch(list); m != nil {
		if n, err := strconv.Atoi(m[1]); err == nil {
			return n
		}
	}
	return 0
}

func nonEmpty(s, def string) string {
	if s == "" {
		return def
	}
	return s
}

// observe is the run of an observed plan, after begin has bound and entered the party. It returns nil when the run leader says the
// plan is done.
func (d *RemoteDriver) observe(ctx context.Context) (err error) {
	spec := d.opt.Observe.withDefaults(d.plan)
	d.obs = &observeState{spec: spec}
	o := d.obs
	d.remote(func(r *RemoteReport) {
		r.Observe = &ObserveReport{Plan: spec.Plan, Changes: []RunChange{}}
	})
	// Whatever way the watch ends, a run still alive on the server is stopped: the agent leaves the party idle, not half-led.
	defer func() {
		if o.restOpen {
			d.endObservedRest(d.clk.Now())
		}
		why, reason := "", ""
		var ceiling *runCeilingError
		switch {
		case errors.As(err, &ceiling):
			why, reason = err.Error(), ceiling.reason()
		case err == nil:
			why = "done"
		case ctxDone(err):
			why = "stopped"
		default:
			why = err.Error()
		}
		d.remote(func(r *RemoteReport) {
			if r.Observe != nil {
				r.Observe.Ended = why
			}
		})
		if o.started && !o.serverEnd {
			d.stopRunLeader(why, reason)
		}
	}()

	if w, ok := d.g.(RunLeaderWatcher); ok {
		_, o.baseN, _ = w.RunStatus()
	}
	if err := d.checkStartPlace(); err != nil {
		return err
	}
	if err := d.startRunLeader(ctx); err != nil {
		return err
	}
	o.startedAt, o.lastAt, o.lastPoll = d.clk.Now(), d.clk.Now(), d.clk.Now()
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		if err := d.clk.Sleep(ctx, d.cfg.Poll); err != nil {
			return err
		}
		d.look()
		if err := d.endedByModule(); err != nil {
			return err
		}
		d.resendViewIfSilent(ctx)
		// The wipe count of an observed run: the run leader owns the run (it stops and recovers on its own), the watch only counts. The
		// result of countDeaths is an edge, not a level: without the wiped flag three polls of a dead party would count three wipes. The
		// latch re-arms only once fewer than wipeDeadReArm members are dead (the module's kWipeDead): a single in-place revive (one member
		// alive for a poll, dead again the next) re-arms a plain "not all dead" check and double counts the collapse. The "wipe" event goes
		// to the trace, so the tracecheck deaths oracle sees the wipes of an observed run too (stepFailedRecover never runs in observe mode).
		// The window rule counts a wipe the frames never show: every member died within wipeWindow, each death newer than the last counted
		// wipe, so a lead bot that revives between their deaths does not hide the collapse. trackObservedDeaths reads the same frames for
		// the lead bot's deaths and revives.
		all := d.countDeaths()
		window := d.trackObservedDeaths(d.clk.Now())
		if (all || window) && !o.wiped {
			d.rep.update(func(r *Report) { r.Wipes++ })
			o.lastWipeAt = d.clk.Now()
			d.tr.Event("wipe", map[string]any{"leg": d.curLeg, "error": "the whole party is dead", "observe": true, "window": !all})
		}
		if all || window {
			o.wiped = true
		} else if deadCount(d.fr) < wipeDeadReArm {
			o.wiped = false
		}
		d.serveRunRequests(ctx)
		d.readRunLines()
		if d.clk.Now().Sub(o.lastPoll) >= spec.Poll {
			d.pollRunLeader(ctx)
		}
		if done, err := d.runVerdict(); done {
			return err
		}
	}
}

// startRunLeader sends `.bot run start <plan> [speed=<x>] [from ...]` and takes its answer. The `from ...` words are the
// probe's resolution of a partial start (from_leg, from_pull_group, from_step): the run leader begins at that step of its
// own plan (#248). The speed comes from ObserveSpec.Speed (a GM-only test option, owner request 2026-10-25): 0 or 1 is
// omitted, anything else goes as `speed=<x>` with one decimal, AFTER the plan and BEFORE any `from ...` words (the
// server's ParseStartArgs takes `from` last; TakeSpeed takes `speed=` out of the words anywhere).
func (d *RemoteDriver) startRunLeader(ctx context.Context) error {
	o := d.obs
	line := "run start " + o.spec.Plan
	if speed := o.spec.Speed; speed != 0 && speed != 1 {
		line += " speed=" + strconv.FormatFloat(speed, 'f', 1, 64)
	}
	if d.start != nil && d.start.Words != "" {
		line += " " + d.start.Words
	}
	r, err := d.g.Command(ctx, line)
	if err != nil {
		return fmt.Errorf("run start: %w", err)
	}
	d.takeLines(r.Lines)
	switch {
	case r.Kind == tbm.KindErr && r.Err != nil && r.Err.Code == "busy":
		// A run of this plan is already going (the agent restarted, or the run was started by hand): watch it, unless it is another plan's
		// or a partial start was asked for and the run going is at another step (A: adopting it would watch a run from the wrong place).
		sr, serr := d.g.Command(ctx, "run status")
		if serr != nil {
			return fmt.Errorf("run start: busy, and run status: %w", serr)
		}
		d.takeLines(sr.Lines)
		if !o.haveLast || o.last.Plan != o.spec.Plan || o.last.State == tbm.RunDone || o.last.State == tbm.RunNone {
			return fmt.Errorf("run start refused: %s", runLeaderRefusal("start", o.spec.Plan, r.Err))
		}
		if why := d.startMatchesStatus(o.last); why != "" {
			return fmt.Errorf("run start refused: %s", why)
		}
		d.rep.note(fmt.Sprintf("observe mode: a run of %s was already going (%s); watching it", o.spec.Plan, o.last.Text()))
	case r.Kind == tbm.KindErr:
		return fmt.Errorf("run start refused: %s", runLeaderRefusal("start", o.spec.Plan, r.Err))
	case r.Kind == tbm.KindTimeout && !o.haveLast:
		return errors.New("run start: no answer: this server has no run leader (`.bot run` needs TortoiseBots PRs #962 and #963), or it is not answering")
	default:
		if err := d.verifyStartReply(ctx, r.Lines); err != nil {
			return err
		}
	}
	o.started = true
	d.remote(func(rr *RemoteReport) { rr.Observe.Started = true })
	d.rep.note("observe mode: run leader started plan " + o.spec.Plan)
	return nil
}

// startMatchesStatus says why a run already going cannot stand for the requested partial start ("" when it can). A step start needs the
// status at that step; a leg start by name needs the status in that leg; a leg start by number cannot be compared with the status line's
// leg (a name) reliably, so it never adopts (A).
func (d *RemoteDriver) startMatchesStatus(st tbm.RunStatus) string {
	s := d.start
	if s == nil || s.Words == "" {
		return ""
	}
	switch {
	case s.Step > 0:
		if st.Step != s.Step {
			return fmt.Sprintf("busy: the run going is at step %d/%d, not the requested step %d: stop it (.bot run stop) or wait for it to end", st.Step, st.Steps, s.Step)
		}
	case s.LegName != "":
		if !strings.EqualFold(st.Leg, s.LegName) {
			return fmt.Sprintf("busy: the run going is in leg %q, not the requested leg %q: stop it (.bot run stop) or wait for it to end", st.Leg, s.LegName)
		}
	default:
		return "busy: a run going cannot be compared with a from_leg sent as a number (its status line names its leg): stop it (.bot run stop) and start again, or wait for it to end"
	}
	return ""
}

// startLineRe is the run leader's start message for a partway start (TortoiseBots, `.bot run start ... from ...`): "The run starts at
// step 207 of 226 (leg serpentis): the party is in the instance; ...". A trailing sentence says when the start was moved back into the
// step that opens a scripted event (G).
var startLineRe = regexp.MustCompile(`The run starts at step (\d+) of (\d+) \(leg ([^)]+)\)`)

// verifyStartReply proves where a partial start really began, and refuses the watch otherwise (D, fail closed). The first source is the
// run leader's own start message ("The run starts at step X of Y (leg NAME)"), which must name the plan leg asked for. A module that
// answers only with an ACK (or a message whose text changed, or a reply collection that dropped it) is asked with `run status` right
// after the start: the reported step (for a step start) and leg must be the requested ones. When neither source proves it, the newly
// started run is stopped and the watch is refused: no unverified run is watched, and the failed run's unbind leaves no hire bound.
func (d *RemoteDriver) verifyStartReply(ctx context.Context, lines []string) error {
	if d.start == nil || d.start.Words == "" {
		return nil
	}
	for _, line := range lines {
		m := startLineRe.FindStringSubmatch(line)
		if m == nil {
			continue
		}
		step, _ := strconv.Atoi(m[1])
		steps, _ := strconv.Atoi(m[2])
		leg := strings.TrimSpace(m[3])
		movedBack := strings.Contains(line, "moved back")
		// The reply's step is checked against the requested one; the reply never overwrites it. The one documented difference is the
		// module moving a start that fell inside a scripted event back to the step that opens it (the reply says "moved back").
		if d.start.Step > 0 && step != d.start.Step && !movedBack {
			d.stopRunLeader("the start line is not the requested start", "")
			return fmt.Errorf("observe mode: run start: the run leader started at step %d of %d (leg %s), not the requested step %d: the deployed start is not the plan's; the run was stopped and is not watched", step, steps, leg, d.start.Step)
		}
		if want, ok := d.expectedStartLeg(); ok && !movedBack && !strings.EqualFold(leg, want) {
			d.stopRunLeader("the start line is not the requested start", "")
			return fmt.Errorf("observe mode: run start: the run leader started in leg %q, not the requested leg %q: the deployed start is not the plan's; the run was stopped and is not watched", leg, want)
		}
		d.rep.note(fmt.Sprintf("observe mode: the run leader starts at step %d of %d (leg %s)", step, steps, leg))
		if movedBack {
			d.rep.note("observe mode: the run leader moved the start back to the step that opens the scripted event it fell in")
		}
		return nil
	}
	// No start message: the status line right after the start is the second source.
	why := "`run status` gave no answer"
	if sr, err := d.g.Command(ctx, "run status"); err != nil {
		why = "`run status`: " + err.Error()
	} else {
		d.takeLines(sr.Lines)
		if proof := d.startProof(); proof == "" {
			d.rep.note("observe mode: the start message was not read; `run status` proves the start (step " + strconv.Itoa(d.obs.last.Step) + "/" + strconv.Itoa(d.obs.last.Steps) + " leg " + d.obs.last.Leg + ")")
			return nil
		} else {
			why = "`run status` " + proof
		}
	}
	d.stopRunLeader("the start could not be proven", "")
	return fmt.Errorf("observe mode: run start: the run leader's start was not proven (no start message, and %s); the run was stopped and is not watched", why)
}

// startProof says why the newest status line does not prove the requested start ("" when it does). A step start needs that step; the
// leg too when the mapping knows it. A leg start needs the plan leg's name (or its number, for an unnamed leg). Without the start line
// there is no documented "moved back" sentence, so no step difference is accepted here.
func (d *RemoteDriver) startProof() string {
	o, s := d.obs, d.start
	if !o.haveLast || o.last.Plan != o.spec.Plan {
		return "does not show this run's status"
	}
	if s.Step > 0 && o.last.Step != s.Step {
		return fmt.Sprintf("shows step %d/%d, not the requested step %d", o.last.Step, o.last.Steps, s.Step)
	}
	if want, ok := d.expectedStartLeg(); ok && !strings.EqualFold(o.last.Leg, want) {
		return fmt.Sprintf("shows leg %q, not %q", o.last.Leg, want)
	}
	if s.Step == 0 && s.Leg < 0 {
		return "has no leg to compare"
	}
	return ""
}

// expectedStartLeg is the plan leg a partial start must report: the leg asked for, or the legacy of the requested step (ExpectLeg, from
// the plan rows). "" when the mapping could not resolve it (a from_step whose rows are not reproduced): then only the step is checked.
func (d *RemoteDriver) expectedStartLeg() (string, bool) {
	s := d.start
	if s.ExpectLeg != "" {
		return s.ExpectLeg, true
	}
	if s.Leg < 0 || s.Leg >= len(d.plan.Legs) {
		return "", false
	}
	if name := d.plan.Legs[s.Leg].Name; name != "" {
		return name, true
	}
	return strconv.Itoa(s.LegNumber), true
}

// farStartYd is how far from the start step's point a partway start tolerates the lead bot (ObserveSpec.AllowFarStart overrides).
const farStartYd = 80.0

// checkStartPlace refuses a partway start whose step stands far from the party (E): the run leader walks from wherever the party is,
// so a party left at the entrance for a step deep in the dungeon would walk the plan from the wrong place. It fails closed: a start
// whose point or the lead bot's position is not known is refused too, since the place cannot be checked. observe.allow_far_start turns
// each refusal into a note.
func (d *RemoteDriver) checkStartPlace() error {
	if d.start == nil || d.start.Words == "" {
		return nil
	}
	allow := d.opt.Observe.withDefaults(d.plan).AllowFarStart
	if !d.start.HavePoint {
		msg := "the start step's point is not known (the deployed rows could not be reproduced from the plan file), so the party's place cannot be checked"
		if allow {
			d.rep.note("observe mode: " + msg + " (allow_far_start: going on)")
			return nil
		}
		return fmt.Errorf("observe mode: %s: put the party at the start step first, or pass observe.allow_far_start", msg)
	}
	lead, ok := d.leadPos()
	if !ok {
		msg := "the lead bot's position is not known, so the party's place at the start step cannot be checked"
		if allow {
			d.rep.note("observe mode: " + msg + " (allow_far_start: going on)")
			return nil
		}
		return fmt.Errorf("observe mode: %s: pass observe.allow_far_start to start anyway", msg)
	}
	dist := dist3(lead, d.start.Point)
	if dist <= farStartYd {
		return nil
	}
	at := fmt.Sprintf("the lead bot stands %.0f yd from the start step's point %.0f %.0f %.0f", dist, d.start.Point.X, d.start.Point.Y, d.start.Point.Z)
	if allow {
		d.rep.note("observe mode: " + at + " (allow_far_start: the run leader walks from there)")
		return nil
	}
	return fmt.Errorf("observe mode: %s: put the party there first, or pass observe.allow_far_start", at)
}

// runCeilingError is the watch ending at the run ceiling (ObserveSpec.MaxRun, observe.max_run_s).
type runCeilingError struct {
	max time.Duration
	msg string
}

func (e *runCeilingError) Error() string { return e.msg }

// reason is the text `.bot run stop` carries (TortoiseBots #1070 logs it as the stop reason instead of "stopped by .bot run stop"):
// "time limit reached (3h)", the hours with up to two decimals.
func (e *runCeilingError) reason() string {
	return "time limit reached (" + strconv.FormatFloat(math.Round(e.max.Hours()*100)/100, 'f', -1, 64) + "h)"
}

// stopRunLeader sends `.bot run stop` with a context of its own (the watch may have been cancelled). A non-empty reason goes with it as
// the stop's argument; the other endings send the plain line.
func (d *RemoteDriver) stopRunLeader(why, reason string) {
	ctx, cancel := context.WithTimeout(context.WithoutCancel(context.Background()), d.opt.UnbindWait+5*time.Second)
	defer cancel()
	line := "run stop"
	if reason != "" {
		line += " " + reason
	}
	r, err := d.g.Command(ctx, line)
	if reason != "" && err == nil && r.Err != nil && r.Err.Code == "invalid" {
		// A module from before TortoiseBots #1070 reads no argument: the plain line still stops the run.
		r, err = d.g.Command(ctx, "run stop")
	}
	ans := "no answer"
	switch {
	case err != nil:
		ans = "error: " + err.Error()
	case r.Kind == tbm.KindAck:
		ans = "ack"
	case r.Err != nil:
		ans = "refused: " + r.Err.Code
	}
	d.takeLines(r.Lines)
	d.control("stop (" + shortText(why, 60) + "): " + ans)
	d.rep.note("observe mode: run stop sent: " + ans)
}

func shortText(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "..."
}

// control records a control request in the report.
func (d *RemoteDriver) control(text string) {
	d.remote(func(r *RemoteReport) {
		if r.Observe != nil && len(r.Observe.Controls) < 50 {
			r.Observe.Controls = append(r.Observe.Controls, text)
		}
	})
}

// serveRunRequests sends the pause, resume and skip requests of the RPC.
func (d *RemoteDriver) serveRunRequests(ctx context.Context) {
	o := d.obs
	o.mu.Lock()
	reqs := o.reqs
	o.reqs = nil
	o.mu.Unlock()
	for _, verb := range reqs {
		r, err := d.g.Command(ctx, "run "+verb)
		ans := "no answer"
		switch {
		case err != nil:
			ans = "error: " + err.Error()
		case r.Kind == tbm.KindAck:
			ans = "ack"
		case r.Err != nil:
			ans = "refused: " + runLeaderRefusal(verb, o.spec.Plan, r.Err)
		}
		d.takeLines(r.Lines)
		d.control(verb + ": " + ans)
		d.rep.note("observe mode: run " + verb + ": " + ans)
	}
}

// readRunLines takes in the newest status line the feed has read, if it is new.
func (d *RemoteDriver) readRunLines() {
	w, ok := d.g.(RunLeaderWatcher)
	if !ok {
		return
	}
	o := d.obs
	if st, n, ok := w.RunStatus(); ok && n != o.seenN {
		o.seenN = n
		if n > o.baseN {
			d.takeStatus(st)
		}
	}
}

// pollRunLeader asks `.bot run status` and takes in the status lines of the answer.
func (d *RemoteDriver) pollRunLeader(ctx context.Context) {
	o := d.obs
	o.lastPoll = d.clk.Now()
	r, err := d.g.Command(ctx, "run status")
	d.remote(func(rr *RemoteReport) { rr.Observe.Polls++ })
	if err != nil {
		d.rep.note("observe mode: run status: " + err.Error())
		return
	}
	d.takeLines(r.Lines)
}

// takeLines takes in the TBM:RUN lines among the lines of a reply.
func (d *RemoteDriver) takeLines(lines []string) {
	for _, l := range lines {
		if st, ok := tbm.ParseRunLine(l); ok {
			d.takeStatus(st)
		}
	}
}

// playerOrderOf reads the order out of the note of a pause the module made for a player's order: "player order (<order>): <why>". A later
// status of the same pause appends to the note ("; X meanwhile: ..."), so the order ends at the first "): ". ok is false for any other note.
func playerOrderOf(note string) (order string, ok bool) {
	rest, ok := strings.CutPrefix(note, "player order (")
	if !ok {
		return "", false
	}
	if order, _, ok := strings.Cut(rest, "): "); ok {
		return order, true
	}
	if i := strings.LastIndex(rest, ")"); i >= 0 {
		return rest[:i], true
	}
	return "", false
}

// takeStatus folds one status into the observer, the report, the trace, the events and the current-step label.
func (d *RemoteDriver) takeStatus(st tbm.RunStatus) {
	o := d.obs
	now := d.clk.Now()
	if o.haveLast && o.last.Same(st) {
		return
	}
	// A status of another plan is not this run's (a leftover line, or another commander's run on a shared log).
	if st.State != tbm.RunNone && st.Plan != "" && st.Plan != o.spec.Plan {
		d.rep.note(fmt.Sprintf("observe mode: a status line of plan %s is not this run's (%s); ignored", st.Plan, o.spec.Plan))
		return
	}
	prev, had := o.last, o.haveLast
	o.last, o.haveLast, o.lastAt = st, true, now
	// The run leader's rest phase is the observed run's rest: a running status still in phase rest keeps it open (one rest), any
	// other phase or state closes it before the leg switch below, so the rest lands in the leg it happened in. A paused status keeps
	// the phase it paused in, but the pause is waiting for a person, not resting: it closes the rest, and the resume opens a new one.
	if st.Phase == "rest" && st.State == tbm.RunRunning {
		if !o.restOpen {
			o.restSince, o.restOpen = now, true
		}
	} else if o.restOpen {
		d.endObservedRest(now)
	}
	if st.State == tbm.RunPaused && (!had || prev.State != tbm.RunPaused) {
		o.pausedAt = now
	}
	if st.State != tbm.RunPaused {
		o.pausedAt = time.Time{}
	}
	if st.State == tbm.RunDone {
		o.serverEnd = true
	}
	ch := RunChange{At: now, State: st.State, Step: st.Step, Steps: st.Steps, Leg: st.Leg, Kind: st.Kind, Phase: st.Phase, Note: st.Note, Left: st.Left, Speed: st.Speed}
	d.remote(func(r *RemoteReport) {
		ob := r.Observe
		ob.State, ob.Step, ob.Steps, ob.Leg, ob.Kind, ob.Phase, ob.Note, ob.Left, ob.Speed, ob.Text = st.State, st.Step, st.Steps, st.Leg, st.Kind, st.Phase, st.Note, st.Left, st.Speed, st.Text()
		ob.Lines++
		if st.State == tbm.RunPaused && (!had || prev.State != tbm.RunPaused) {
			ob.Pauses++
		}
		ob.Changes = append(ob.Changes, ch)
		if n := len(ob.Changes); n > MaxRunChanges {
			ob.Changes = append([]RunChange{}, ob.Changes[n-MaxRunChanges:]...)
		}
	})
	// A pause the module made for a player's order (the observer's own `cmd summon`, a move) is the observer's doing: the report names the
	// order so a person sees why the run waited. Only the first status of that pause counts: the module appends to the note of a pause it
	// already made. The run going on again is the resume after the order, whether the agent or a person sent it.
	if order, ok := playerOrderOf(st.Note); st.State == tbm.RunPaused && ok && !o.playerPaused {
		o.playerPaused = true
		d.remote(func(r *RemoteReport) {
			if ob := r.Observe; len(ob.PlayerOrders) < 50 {
				ob.PlayerOrders = append(ob.PlayerOrders, fmt.Sprintf("step %d/%d (%s): %s", st.Step, st.Steps, st.Leg, order))
			}
		})
		d.control("player order: " + order)
		d.tr.Event("player_order", map[string]any{"order": order, "step": st.Step, "leg_name": st.Leg})
		d.rep.note("observe mode: the run paused for a player order (" + order + ")")
	}
	if o.playerPaused {
		switch st.State {
		case tbm.RunRunning:
			o.playerPaused = false
			d.control(fmt.Sprintf("resumed after the player order at step %d", st.Step))
			d.rep.note("observe mode: the run went on after the player order")
		case tbm.RunDone:
			// The run stopped instead of going on: the pause is over, but there was no resume to record (#270).
			o.playerPaused = false
		}
	}
	label := "run leader: " + st.Text()
	if st.State != tbm.RunRunning {
		label = "run leader " + st.State + ": " + st.Text()
	}
	d.rep.setCurrent(label)
	d.wd.progress("run leader: " + st.Text())
	data := map[string]any{"mode": "observe", "state": st.State, "step": st.Step, "steps": st.Steps, "leg_name": st.Leg, "kind": st.Kind, "phase": st.Phase, "note": st.Note, "left": st.Left, "speed": st.Speed}
	d.tr.Event("run_leader", data)
	// The legs: a new leg name ends the one before it and starts the next, so the report has the per-leg times.
	if li := d.legOf(st.Leg); li >= 0 && li != d.curLeg && st.State != tbm.RunNone {
		d.endLeg("done")
		d.curLeg = li
		name := d.plan.Legs[li].Name
		d.rep.update(func(r *Report) {
			r.Legs = append(r.Legs, LegReport{Leg: li, Name: name, Started: now, Result: "running"})
		})
		d.emit(events.KindDungeonLeg, fmt.Sprintf("leg %d %s: start", li, name), map[string]any{"leg": li, "name": name, "phase": "start", "mode": "observe"})
	}
	if !had || prev.Step != st.Step || prev.Kind != st.Kind {
		d.emit(events.KindDungeonStep, "run leader: "+st.Text(), map[string]any{"leg": d.curLeg, "step": st.Step, "steps": st.Steps, "action": st.Kind, "ok": true, "mode": "observe", "state": st.State, "phase": st.Phase, "note": st.Note, "left": st.Left, "speed": st.Speed})
	}
}

// endObservedRest closes the run leader's rest phase: it counts as a rest gate (Result "run_leader"), in the report, the leg and the trace.
func (d *RemoteDriver) endObservedRest(now time.Time) {
	o := d.obs
	o.restOpen = false
	secs := max(now.Sub(o.restSince).Seconds(), 0)
	leg := max(d.curLeg, 0)
	d.rep.update(func(r *Report) {
		r.RestSeconds += secs
		if n := len(r.Legs); n > 0 {
			r.Legs[n-1].RestSeconds += secs
		}
		if len(r.RestGates) < maxRestGates {
			r.RestGates = append(r.RestGates, RestGate{Leg: leg, Seconds: secs, Result: "run_leader"})
		}
	})
	d.tr.Event("rest", map[string]any{"leg": d.curLeg, "seconds": secs, "source": "run_leader"})
}

// deadCount is how many members of the frame's party are dead (the latch of the wipe count compares it with wipeDeadReArm; tbm.ViewFrame
// counts it nowhere: AllDead is the all-rounded edge, not the level).
func deadCount(fr tbm.ViewFrame) int {
	n := 0
	for _, p := range fr.Party {
		if p.Dead {
			n++
		}
	}
	return n
}

// trackObservedDeaths follows the party's deaths in the newest frame: the lead bot's deaths and revives go into the report
// (LeaderDeaths, Resurrections, SecondsDead, the leg's too) and the trace (event leader_death), and the result says that every
// member has died within wipeWindow, each death newer than the last counted wipe.
func (d *RemoteDriver) trackObservedDeaths(now time.Time) bool {
	o := d.obs
	if !d.haveFr {
		return false
	}
	if o.wasDead == nil { // the first frame is the baseline: a bot already dead in it is not a death
		o.wasDead, o.deathAt = map[packet.GUID]bool{}, map[packet.GUID]time.Time{}
		for _, p := range d.fr.Party {
			o.wasDead[p.GUID] = p.Dead
		}
		return false
	}
	for _, p := range d.fr.Party {
		isLead := d.fr.Lead != 0 && p.GUID == d.fr.Lead
		if p.Dead && !o.wasDead[p.GUID] {
			o.deathAt[p.GUID] = now
			if isLead {
				o.leadDown, o.leadDownAt = true, now
				d.rep.update(func(r *Report) { r.LeaderDeaths++ })
				d.legStat(func(l *LegReport) { l.LeaderDeaths++ })
				d.tr.Event("leader_death", map[string]any{"name": p.Name, "a": guidText(p.GUID), "leg": d.curLeg})
			}
		}
		if !p.Dead && o.wasDead[p.GUID] && isLead && o.leadDown {
			o.leadDown = false
			secs := now.Sub(o.leadDownAt).Seconds()
			d.rep.update(func(r *Report) { r.Resurrections++; r.SecondsDead += secs })
			d.legStat(func(l *LegReport) { l.Resurrections++; l.SecondsDead += secs })
		}
		o.wasDead[p.GUID] = p.Dead
	}
	if len(d.fr.Party) < 2 {
		return false
	}
	for _, p := range d.fr.Party {
		at, ok := o.deathAt[p.GUID]
		if !ok || !at.After(o.lastWipeAt) || now.Sub(at) > wipeWindow {
			return false
		}
	}
	return true
}

// legOf finds the plan leg a status line names (by name, or by number when the module had no name); -1 when none.
func (d *RemoteDriver) legOf(name string) int {
	if name == "" {
		return -1
	}
	for i, l := range d.plan.Legs {
		if strings.EqualFold(l.Name, name) {
			return i
		}
	}
	var n int
	if _, err := fmt.Sscanf(name, "%d", &n); err == nil && n >= 0 && n < len(d.plan.Legs) && fmt.Sprint(n) == name {
		return n
	}
	return -1
}

// runVerdict decides whether the watch is over: the run is done, gone, or has been quiet, paused or going too long.
func (d *RemoteDriver) runVerdict() (done bool, err error) {
	o := d.obs
	now := d.clk.Now()
	st := o.last
	switch {
	case o.haveLast && st.State == tbm.RunDone:
		d.endLeg("done")
		if st.Steps > 0 && st.Step >= st.Steps && !strings.HasPrefix(st.Note, "stopped") {
			d.rep.note(fmt.Sprintf("observe mode: the run leader finished plan %s (%d steps)", o.spec.Plan, st.Steps))
			return true, nil
		}
		return true, fmt.Errorf("the run leader ended the run at step %d/%d (%s): %s", st.Step, st.Steps, st.Leg, nonEmpty(st.Note, "no reason given"))
	case o.haveLast && st.State == tbm.RunNone && now.Sub(o.startedAt) > observeGone:
		return true, errors.New("the run is gone: the server's run leader has no run (the binding ended, or someone stopped it)")
	case now.Sub(o.startedAt) > o.spec.MaxRun:
		return true, &runCeilingError{max: o.spec.MaxRun, msg: fmt.Sprintf("the run leader took longer than %s (at step %d/%d, %s)", o.spec.MaxRun, st.Step, st.Steps, st.Text())}
	case o.haveLast && st.State == tbm.RunPaused && !o.pausedAt.IsZero() && now.Sub(o.pausedAt) > o.spec.PausedWait:
		return true, fmt.Errorf("the run leader has been paused for %s without a resume at step %d/%d (%s): %s", o.spec.PausedWait, st.Step, st.Steps, st.Leg, nonEmpty(st.Note, "no reason given"))
	case o.haveLast && st.State == tbm.RunRunning && now.Sub(o.lastAt) > o.spec.Stall:
		return true, fmt.Errorf("the run leader shows the same status for %s (step %d/%d, %s): stalled", o.spec.Stall, st.Step, st.Steps, st.Text())
	case !o.haveLast && now.Sub(o.startedAt) > observeGone:
		return true, errors.New("the run leader sent no status line since the start: no TBM:RUN line came")
	}
	return false, nil
}
