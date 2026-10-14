package dungeon

import (
	"math"
	"sync"
	"time"

	"github.com/pjerra/turtle-agent/internal/tracerun"
)

// Report is what a run did, kept while it goes and complete at the end. dungeon_status returns it as JSON.
type Report struct {
	Plan    string     `json:"plan"`
	Map     uint32     `json:"map"`
	State   string     `json:"state"` // running, done, failed or stopped
	FromLeg int        `json:"from_leg"`
	Started time.Time  `json:"started"`
	Ended   *time.Time `json:"ended,omitempty"`
	Seconds float64    `json:"seconds"`
	Current string     `json:"current,omitempty"` // the action in progress
	Error   string     `json:"error,omitempty"`
	Wipes   int        `json:"wipes"`
	// LeaderGM says whether the run took the leader's account for a GM (and so may have sent GM commands). A run with
	// leader_gm false sent none.
	LeaderGM bool `json:"leader_gm"`
	// AccountLevel is the leader's security level (0 player, 3 developer) when the probe read it from ".account"; absent otherwise.
	AccountLevel *int `json:"account_level,omitempty"`
	// GMCaps are the GM commands the leader's account level let the run use (visible, teleport, revive, buffs).
	GMCaps GMCaps `json:"gm_caps"`
	// The leader's own deaths (README "Death and the corpse run"): how often it died, came back by an accepted
	// resurrection or by a corpse run, and for how long it was dead in all.
	LeaderDeaths  int        `json:"leader_deaths"`
	Resurrections int        `json:"resurrections"`
	CorpseRuns    int        `json:"corpse_runs"`
	SecondsDead   float64    `json:"seconds_dead"`
	Deaths        []Death    `json:"deaths"`
	Shortcuts     []Shortcut `json:"gm_shortcuts"`
	// SetupShortcuts are the GM commands the caller used to set the run up (".go" to the entrance, ".levelup -15", a
	// summon), as dungeon_run's setup parameter gave them: the run did not make them, but a run that needed any is
	// not one a player could have started either.
	SetupShortcuts []string `json:"setup_shortcuts"`
	// Buffs are the spells the run was asked to put on the companions (GM .aura); each application is also a "buff"
	// entry in gm_shortcuts, so a buffed run is never mistaken for a player's.
	Buffs []Buff `json:"buffs"`
	// Brain names the strategy the agent's bot brain played the leader with (dungeon_run's brain), empty for none.
	Brain string      `json:"brain,omitempty"`
	Legs  []LegReport `json:"legs"`
	Notes []string    `json:"notes"`
	// RestSeconds is the time the run spent in rest gates in all; RestGates lists each gate: the seconds it waited, who was short
	// and whether a drink order went out.
	RestSeconds float64    `json:"rest_seconds"`
	RestGates   []RestGate `json:"rest_gates"`
	// RestGatesSkipped counts the gates that cost nothing (RestGate.Skipped), RestWalkPasses those among them that passed because a long
	// walk followed. RestShare is RestSeconds as a percent of the run's seconds (the trace's rest_share check measures the same).
	RestGatesSkipped int     `json:"rest_gates_skipped"`
	RestWalkPasses   int     `json:"rest_walk_passes"`
	RestShare        float64 `json:"rest_share"`
	// Resumed says what a run started inside a leg skipped (see Resumed); absent when it started at the leg's beginning.
	Resumed *Resumed `json:"resumed,omitempty"`
	// Refusals are the orders TortoiseBots turned down (TBM:ACTION_ERR replies to the driver's "/p .bot action ..."), by
	// intent, code and message, with how often each came in the leg.
	Refusals []RefusalReport `json:"refusals"`
	// Remote is set for a run in remote mode (dungeon_run mode "remote"): the commander binding, the feed, the orders and the
	// decisions taken from the feed. Absent in group mode.
	Remote *RemoteReport `json:"remote,omitempty"`
	// TankFirst is the counters of the tank-first rules in a group run (README "Tank first"); a remote run has them in Remote. Absent
	// until the first fight with a tank in view.
	TankFirst *TankFirstReport `json:"tank_first,omitempty"`
	// RunID names the run: it is in every dungeon event (data.run_id) and in the trace file's header.
	RunID string `json:"run_id"`
	// Trace says where the run's trace went and how it scored (README "Run traces and checks"); set when the run ends.
	Trace *tracerun.Info `json:"trace,omitempty"`
	// TraceFile is the trace's path while the run goes (empty when tracing is off or the file could not be made).
	TraceFile string `json:"trace_file,omitempty"`
	// Watchdog is the progress watchdog's view (README "Run watchdog"): when the run last made progress, for how long it has not, and the
	// stall level.
	Watchdog *WatchdogReport `json:"watchdog,omitempty"`
	// Prepare is the prepare block's steps with their seconds (remote mode; absent when the run had none). SetupS is the whole setup: the
	// seconds from the start of the run to its first step (prepare, bind, enter phase and preflight together).
	Prepare *PrepareReport `json:"prepare,omitempty"`
	SetupS  float64        `json:"setup_s"`
	// Ready is the hired party's ready block (prepare party "hire", hire.go): per bot the class, level, item level and quality, what a `.bot`
	// command fixed and what was seen, and the seconds the hire step and the whole setup took. Absent for named bots.
	Ready *ReadyReport `json:"ready,omitempty"`
	// Start is how a run that began inside the dungeon got there (from_leg past the first, from_pull_group; start.go): the start point, the
	// GM lines, what was skipped. Absent for a run from the first leg's start.
	Start *StartReport `json:"start,omitempty"`
	// Pulltest is the pull-test scenario's result (dungeon_run pulltest; pulltest.go): per try the time to kill, deaths, threat overrides, adds,
	// healer mana and rest, and the summary. Absent for a plan run.
	Pulltest *PulltestReport `json:"pulltest,omitempty"`
}

// LegReport is one leg of the run: how long it took and what it cost.
type LegReport struct {
	Leg         int       `json:"leg"`
	Name        string    `json:"name,omitempty"`
	Started     time.Time `json:"started"`
	Seconds     float64   `json:"seconds"`
	Result      string    `json:"result"` // running, done, failed or stopped
	Deaths      int       `json:"deaths"`
	Pulls       int       `json:"pulls"`        // fights started, retries included
	PullRetries int       `json:"pull_retries"` // pulls that drew no fight or evaded, and were tried again
	// Skipped says what of the leg the run did not do on purpose (from_pull_group); the legs before from_leg have Result "skipped (from_leg)".
	Skipped     string  `json:"skipped,omitempty"`
	RestSeconds float64 `json:"rest_seconds"` // time in the rest gates
	WalkAggros  int     `json:"walk_aggros"`  // packs that aggroed during a walk and were fought there (each also counts in pulls)
	Ambushes    int     `json:"ambushes"`     // stealthed pull groups walked through instead of pulled
	// The leader's deaths in this leg: how many, how it came back, and the time it was dead.
	LeaderDeaths  int     `json:"leader_deaths"`
	Resurrections int     `json:"resurrections"`
	CorpseRuns    int     `json:"corpse_runs"`
	SecondsDead   float64 `json:"seconds_dead"`
}

// Death is one death of the leader or a companion.
type Death struct {
	Name string    `json:"name"`
	Leg  int       `json:"leg"`
	At   time.Time `json:"at"`
}

// Shortcut is a GM action the run took instead of playing it: a run that used any does not count as a player's run.
// Kind is teleport_to_entrance, teleport_to_leg_start, revive or buff.
type Shortcut struct {
	Kind   string    `json:"kind"`
	Leg    int       `json:"leg"`
	Detail string    `json:"detail"`
	At     time.Time `json:"at"`
}

// reporter guards a Report for the run's goroutine and the RPC readers.
type reporter struct {
	mu  sync.Mutex
	r   Report
	now func() time.Time
	// onNote, when set, sees each note (the trace copies them as events).
	onNote func(string)
	// wd, when set, is the run's watchdog; each snapshot carries its state as of now.
	wd *watchdog
}

func (p *reporter) init(plan *Plan, now func() time.Time) {
	p.now = now
	p.r = Report{Plan: plan.Name, Map: plan.Map, State: "idle", Deaths: []Death{}, Shortcuts: []Shortcut{}, SetupShortcuts: []string{}, Buffs: []Buff{}, Legs: []LegReport{}, Notes: []string{}, Refusals: []RefusalReport{}, RestGates: []RestGate{}}
}

func (p *reporter) start(fromLeg int) {
	p.update(func(r *Report) {
		r.State, r.FromLeg, r.Started = "running", fromLeg, p.now()
	})
}

// end publishes the end of the run at t. The event that announces it goes out first (summaryEnded): a reader that
// sees a final state finds the event in the log.
func (p *reporter) end(state, errText string, t time.Time) {
	p.update(func(r *Report) { endReport(r, state, errText, t) })
}

func endReport(r *Report, state, errText string, t time.Time) {
	r.State, r.Error, r.Ended, r.Current = state, errText, &t, ""
	r.Seconds = t.Sub(r.Started).Seconds()
	r.setRestShare()
}

// snapshotEnded is snapshot and whether the run has ended, read together under the lock: a state that is not final
// is always a run that is going.
func (p *reporter) snapshotEnded() (Report, bool) {
	r := p.snapshot()
	return r, r.Ended != nil
}

// summaryEnded is summary as it will read once end(state, errText, t) is published, for the event that goes out before.
func (p *reporter) summaryEnded(state, errText string, t time.Time) map[string]any {
	r := p.snapshot()
	endReport(&r, state, errText, t)
	return summaryOf(r)
}

func (p *reporter) setCurrent(label string) { p.update(func(r *Report) { r.Current = label }) }

func (p *reporter) note(s string) {
	p.update(func(r *Report) { r.Notes = append(r.Notes, s) })
	if p.onNote != nil {
		p.onNote(s)
	}
}

func (p *reporter) update(f func(*Report)) {
	p.mu.Lock()
	defer p.mu.Unlock()
	f(&p.r)
}

// snapshot copies the report; the time of a running leg and run is as of now.
func (p *reporter) snapshot() Report {
	p.mu.Lock()
	defer p.mu.Unlock()
	r := p.r
	r.Deaths = append([]Death{}, p.r.Deaths...)
	r.Shortcuts = append([]Shortcut{}, p.r.Shortcuts...)
	r.SetupShortcuts = append([]string{}, p.r.SetupShortcuts...)
	r.Buffs = append([]Buff{}, p.r.Buffs...)
	r.Legs = append([]LegReport{}, p.r.Legs...)
	r.Notes = append([]string{}, p.r.Notes...)
	r.Refusals = append([]RefusalReport{}, p.r.Refusals...)
	r.RestGates = append([]RestGate{}, p.r.RestGates...)
	r.Remote = p.r.Remote.clone()
	r.Prepare = p.r.Prepare.clone()
	r.Ready = p.r.Ready.clone()
	r.Start = p.r.Start.clone()
	r.Pulltest = p.r.Pulltest.clone()
	if p.r.TankFirst != nil {
		tf := *p.r.TankFirst
		r.TankFirst = &tf
	}
	if r.State == "running" {
		now := p.now()
		r.Seconds = now.Sub(r.Started).Seconds()
		if n := len(r.Legs); n > 0 && r.Legs[n-1].Result == "running" {
			r.Legs[n-1].Seconds = now.Sub(r.Legs[n-1].Started).Seconds()
		}
	}
	r.setRestShare()
	r.Watchdog = p.wd.report()
	return r
}

// runLegs counts the legs the run walked: the legs before from_leg are in Legs as skipped, not as part of the run.
func (r Report) runLegs() int {
	n := 0
	for _, l := range r.Legs {
		if l.Result != SkippedFromLeg {
			n++
		}
	}
	return n
}

// setRestShare puts the rest gates' share of the run's seconds, in percent with one decimal, in RestShare.
func (r *Report) setRestShare() {
	if r.Seconds > 0 {
		r.RestShare = math.Round(r.RestSeconds/r.Seconds*1000) / 10
	}
}

// summary is the report's totals for the dungeon_done and dungeon_failed events.
func (p *reporter) summary() map[string]any { return summaryOf(p.snapshot()) }

func summaryOf(r Report) map[string]any {
	m := summaryBase(r)
	m["run_id"] = r.RunID
	if r.Remote != nil {
		m["remote"], m["lead_bot"], m["frames_received"] = true, r.Remote.LeadBot, r.Remote.FramesReceived
		m["orders_sent"], m["orders_refused"], m["refills"] = r.Remote.OrdersSent, r.Remote.OrdersRefused, len(r.Remote.Refills)
	}
	return m
}

func summaryBase(r Report) map[string]any {
	return map[string]any{"plan": r.Plan, "state": r.State, "seconds": r.Seconds, "legs": r.runLegs(), "deaths": len(r.Deaths),
		"wipes": r.Wipes, "gm_shortcuts": len(r.Shortcuts), "setup_shortcuts": len(r.SetupShortcuts), "buffs": buffNames(r.Buffs),
		"leader_gm": r.LeaderGM, "gm_caps": r.GMCaps, "leader_deaths": r.LeaderDeaths, "resurrections": r.Resurrections, "corpse_runs": r.CorpseRuns, "seconds_dead": r.SecondsDead, "rest_seconds": r.RestSeconds,
		"rest_share": r.RestShare, "rest_gates_skipped": r.RestGatesSkipped}
}
