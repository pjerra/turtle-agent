package dungeon

import (
	"sync"
	"time"
)

// Report is what a run did, kept while it goes and complete at the end. dungeon_status returns it as JSON.
type Report struct {
	Plan      string     `json:"plan"`
	Map       uint32     `json:"map"`
	State     string     `json:"state"` // running, done, failed or stopped
	FromLeg   int        `json:"from_leg"`
	Started   time.Time  `json:"started"`
	Ended     *time.Time `json:"ended,omitempty"`
	Seconds   float64    `json:"seconds"`
	Current   string     `json:"current,omitempty"` // the action in progress
	Error     string     `json:"error,omitempty"`
	Wipes     int        `json:"wipes"`
	Deaths    []Death    `json:"deaths"`
	Shortcuts []Shortcut `json:"gm_shortcuts"`
	// SetupShortcuts are the GM commands the caller used to set the run up (".go" to the entrance, ".levelup -15", a
	// summon), as dungeon_run's setup parameter gave them: the run did not make them, but a run that needed any is
	// not one a player could have started either.
	SetupShortcuts []string `json:"setup_shortcuts"`
	// Buffs are the spells the run was asked to put on the companions (GM .aura); each application is also a "buff"
	// entry in gm_shortcuts, so a buffed run is never mistaken for a player's.
	Buffs []Buff      `json:"buffs"`
	Legs  []LegReport `json:"legs"`
	Notes []string    `json:"notes"`
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
	RestSeconds float64   `json:"rest_seconds"` // time in the rest gates
	WalkAggros  int       `json:"walk_aggros"`  // packs that aggroed during a walk and were fought there (each also counts in pulls)
	Ambushes    int       `json:"ambushes"`     // stealthed pull groups walked through instead of pulled
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
}

func (p *reporter) init(plan *Plan, now func() time.Time) {
	p.now = now
	p.r = Report{Plan: plan.Name, Map: plan.Map, State: "idle", Deaths: []Death{}, Shortcuts: []Shortcut{}, SetupShortcuts: []string{}, Buffs: []Buff{}, Legs: []LegReport{}, Notes: []string{}}
}

func (p *reporter) start(fromLeg int) {
	p.update(func(r *Report) {
		r.State, r.FromLeg, r.Started = "running", fromLeg, p.now()
	})
}

func (p *reporter) end(state, errText string) {
	p.update(func(r *Report) {
		t := p.now()
		r.State, r.Error, r.Ended, r.Current = state, errText, &t, ""
		r.Seconds = t.Sub(r.Started).Seconds()
	})
}

func (p *reporter) setCurrent(label string) { p.update(func(r *Report) { r.Current = label }) }

func (p *reporter) note(s string) { p.update(func(r *Report) { r.Notes = append(r.Notes, s) }) }

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
	if r.State == "running" {
		now := p.now()
		r.Seconds = now.Sub(r.Started).Seconds()
		if n := len(r.Legs); n > 0 && r.Legs[n-1].Result == "running" {
			r.Legs[n-1].Seconds = now.Sub(r.Legs[n-1].Started).Seconds()
		}
	}
	return r
}

// summary is the report's totals for the dungeon_done and dungeon_failed events.
func (p *reporter) summary() map[string]any {
	r := p.snapshot()
	return map[string]any{"plan": r.Plan, "state": r.State, "seconds": r.Seconds, "legs": len(r.Legs), "deaths": len(r.Deaths),
		"wipes": r.Wipes, "gm_shortcuts": len(r.Shortcuts), "setup_shortcuts": len(r.SetupShortcuts), "buffs": buffNames(r.Buffs)}
}
