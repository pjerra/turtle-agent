package raidwalk

import (
	"sort"
	"strings"
	"time"

	"github.com/pjerra/turtle-agent/internal/dungeon"
	"github.com/pjerra/turtle-agent/internal/mapview"
	"github.com/pjerra/turtle-agent/internal/packet"
	"github.com/pjerra/turtle-agent/internal/tbm"
)

// The end reasons of a run (spec 3.4).
type EndKind string

const (
	EndRunning      EndKind = ""
	EndUntil        EndKind = "until"
	EndPlanDone     EndKind = "plan done"
	EndStoppedOther EndKind = "stopped"
	EndNoRun        EndKind = "no run"
)

// End is how the run ended: its kind, the server's note and the step reached.
type End struct {
	Kind EndKind
	Note string
	Step int
}

// Reason is the end reason for the summary: "until", "plan done", "no run" or "stopped: <note>".
func (e End) Reason() string {
	switch e.Kind {
	case EndStoppedOther:
		if e.Note == "" {
			return "stopped"
		}
		return "stopped: " + e.Note
	}
	return string(e.Kind)
}

// Incident is something the command must act on while it watches a run.
type Incident interface {
	Kind() string
}

// IncidentWipe says every hire is dead.
type IncidentWipe struct {
	Step int
	At   time.Time
}

func (IncidentWipe) Kind() string { return "wipe" }

// IncidentBossEngaged says a boss of the plan was engaged.
type IncidentBossEngaged struct {
	Entry uint32
	Step  int
	At    time.Time
}

func (IncidentBossEngaged) Kind() string { return "boss" }

// IncidentStall says the same running status line stood for 10 minutes.
type IncidentStall struct {
	Step int
	At   time.Time
}

func (IncidentStall) Kind() string { return "stall" }

// DashBot is one bound bot of a GET /api/v1/commanders snapshot.
type DashBot struct {
	Name  string `json:"name"`
	Alive bool   `json:"alive"`
}

// StepSpan is one step of the run timeline.
type StepSpan struct {
	Step    int
	Leg     string
	Kind    string
	Began   time.Time
	Ended   time.Time
	Seconds float64
}

// LegSpan is the time the run spent in one leg.
type LegSpan struct {
	Leg     string
	Seconds float64
}

// Pause is one contiguous paused span.
type Pause struct {
	Step      int
	Note      string
	At        time.Time
	ResumedAt time.Time
	Seconds   float64
	Resumed   bool
}

type botState struct {
	Dead bool
	At   time.Time
}

const (
	wipeFresh  = 10 * time.Second
	stallAfter = 10 * time.Minute
	bossNoPct  = 100
)

// Tracker folds the run's status lines and the commander's frames into the timeline, deaths, pauses, end reason and
// incidents. It is pure: the command supplies the clock. hires are the names the walk hired: the wipe rule needs
// them, because a frame may be truncated and name only part of the party.
type Tracker struct {
	bossEntries map[uint32]bool

	hires    map[string]bool // names the walk hired
	expected int             // hire count when hires is nil: the plan's raid size - 1
	guidName map[packet.GUID]string
	state    map[string]botState
	lastNow  time.Time

	currentStep int
	currentLeg  string
	currentKind string
	timeline    []StepSpan
	stepOpen    int // index of the open span, -1: none

	lastStatus   tbm.RunStatus
	haveStatus   bool
	sameSince    time.Time
	stallFired   bool
	stepsTotal   int
	stepsReached int

	paused    bool
	pauses    []Pause
	pauseOpen int // index of the open pause, -1: none

	deathTotal  int
	deathByStep map[int]int
	deathByBot  map[string]int

	wipeFired bool
	wipes     []int

	bossFired bool
	bossEntry uint32
	bossStep  int

	pending []Incident
	end     End
}

// NewTracker builds a tracker for the plan; hires are the names the walk hired. With no names the wipe rule takes the
// plan's raid size - 1 as the hire count and waits until that many bots have been seen.
func NewTracker(plan *dungeon.Plan, hires []string) *Tracker {
	t := &Tracker{
		bossEntries: map[uint32]bool{},
		guidName:    map[packet.GUID]string{},
		state:       map[string]botState{},
		stepOpen:    -1,
		pauseOpen:   -1,
		deathByStep: map[int]int{},
		deathByBot:  map[string]int{},
	}
	if plan != nil {
		for _, e := range mapview.BossEntries(plan) {
			t.bossEntries[e] = true
		}
		for _, leg := range plan.Legs {
			if leg.Boss != nil && leg.Boss.Hold != nil {
				for _, e := range leg.Boss.Hold.Entries {
					t.bossEntries[e] = true
				}
			}
		}
	}
	if len(hires) > 0 {
		t.hires = make(map[string]bool, len(hires))
		for _, name := range hires {
			if name != "" {
				t.hires[name] = true
			}
		}
	} else if plan != nil && plan.Raid != nil && plan.Raid.Size > 1 {
		t.expected = plan.Raid.Size - 1
	}
	return t
}

// Feed takes one TBM:RUN status of the walk's plan.
func (t *Tracker) Feed(now time.Time, st tbm.RunStatus) {
	t.lastNow = now
	if st.Steps > t.stepsTotal {
		t.stepsTotal = st.Steps
	}
	if st.Step > t.stepsReached {
		t.stepsReached = st.Step
	}

	switch st.State {
	case tbm.RunDone:
		t.closeStep(now)
		t.closePause(now)
		t.sameSince = time.Time{}
		switch {
		case strings.HasPrefix(st.Note, "stopped before step"):
			t.end = End{Kind: EndUntil, Note: st.Note, Step: st.Step}
		case strings.HasPrefix(st.Note, "plan done"):
			t.end = End{Kind: EndPlanDone, Note: st.Note, Step: st.Step}
		default:
			t.end = End{Kind: EndStoppedOther, Note: st.Note, Step: st.Step}
		}
		t.lastStatus, t.haveStatus = st, true
		return
	case tbm.RunNone:
		t.closeStep(now)
		t.closePause(now)
		t.end = End{Kind: EndNoRun, Step: st.Step}
		t.sameSince = time.Time{}
		t.lastStatus, t.haveStatus = st, true
		return
	}

	if st.State == tbm.RunPaused {
		if !t.paused {
			t.paused = true
			t.pauses = append(t.pauses, Pause{Step: st.Step, Note: st.Note, At: now})
			t.pauseOpen = len(t.pauses) - 1
		}
	} else {
		t.closePause(now)
	}

	if st.Step > 0 && (t.stepOpen < 0 || st.Step != t.currentStep || st.Leg != t.currentLeg || st.Kind != t.currentKind) {
		t.closeStep(now)
		t.currentStep, t.currentLeg, t.currentKind = st.Step, st.Leg, st.Kind
		t.timeline = append(t.timeline, StepSpan{Step: st.Step, Leg: st.Leg, Kind: st.Kind, Began: now})
		t.stepOpen = len(t.timeline) - 1
	}

	if st.State == tbm.RunRunning {
		if !t.haveStatus || !st.Same(t.lastStatus) {
			t.sameSince = now
			t.stallFired = false
		} else if !t.stallFired && !t.sameSince.IsZero() && now.Sub(t.sameSince) >= stallAfter {
			t.pending = append(t.pending, IncidentStall{Step: st.Step, At: now})
			t.stallFired = true
		}
	} else {
		t.sameSince = time.Time{}
		t.stallFired = false
	}
	t.lastStatus, t.haveStatus = st, true
}

// Frame takes one bot_view frame. A frame that does not name a bot changes nothing for it.
func (t *Tracker) Frame(now time.Time, f tbm.ViewFrame) {
	t.lastNow = now
	for _, p := range f.Party {
		name := p.Name
		if name == "" {
			name = p.GUID.String()
		}
		dead := p.Dead || p.State == "d"
		if p.GUID != 0 {
			t.guidName[p.GUID] = name
		}
		if prev, ok := t.state[name]; ok && !prev.Dead && dead {
			t.deathTotal++
			t.deathByStep[t.currentStep]++
			t.deathByBot[name]++
		}
		t.state[name] = botState{Dead: dead, At: now}
	}
	t.checkBoss(f)
	t.checkWipe(now)
}

// Snapshot takes the newest /api/v1/commanders snapshot. A snapshot where every hire is not alive is a wipe.
func (t *Tracker) Snapshot(now time.Time, bots []DashBot) {
	t.lastNow = now
	alive := make(map[string]bool, len(bots))
	for _, b := range bots {
		alive[b.Name] = b.Alive
	}
	var allDead bool
	switch {
	case len(t.hires) > 0:
		allDead = true
		for name := range t.hires {
			if a, ok := alive[name]; !ok || a {
				allDead = false
				break
			}
		}
	case t.expected > 0:
		allDead = len(bots) == t.expected
		for _, b := range bots {
			if b.Alive {
				allDead = false
				break
			}
		}
	default:
		return
	}
	if allDead {
		t.fireWipe(now)
	} else if t.wipeFired {
		t.wipeFired = false
	}
}

func (t *Tracker) checkBoss(f tbm.ViewFrame) {
	if t.bossFired {
		return
	}
	party := make(map[packet.GUID]bool, len(f.Party))
	for _, p := range f.Party {
		party[p.GUID] = true
	}
	for _, h := range f.Hostiles {
		if !t.bossEntries[h.Entry] {
			continue
		}
		targeted := h.Target != 0 && (party[h.Target] || t.guidName[h.Target] != "")
		if !targeted && h.HPPct >= bossNoPct {
			continue
		}
		t.bossFired, t.bossEntry, t.bossStep = true, h.Entry, t.currentStep
		t.pending = append(t.pending, IncidentBossEngaged{Entry: h.Entry, Step: t.currentStep, At: t.lastNow})
		return
	}
}

func (t *Tracker) checkWipe(now time.Time) {
	keys := t.wipeNames()
	if len(keys) == 0 {
		return
	}
	all := true
	for _, name := range keys {
		s, ok := t.state[name]
		if !ok || now.Sub(s.At) > wipeFresh || !s.Dead {
			all = false
			break
		}
	}
	if all {
		t.fireWipe(now)
	} else if t.wipeFired {
		t.wipeFired = false
	}
}

func (t *Tracker) fireWipe(now time.Time) {
	if t.wipeFired {
		return
	}
	t.wipeFired = true
	t.wipes = append(t.wipes, t.currentStep)
	t.pending = append(t.pending, IncidentWipe{Step: t.currentStep, At: now})
}

// wipeNames is the set the wipe rule must find dead: the hires, or when the caller gave none, every bot seen once
// the plan's raid size says all of them have been seen.
func (t *Tracker) wipeNames() []string {
	set := t.hires
	if len(set) == 0 {
		if t.expected == 0 || len(t.state) != t.expected {
			return nil
		}
		set = make(map[string]bool, len(t.state))
		for name := range t.state {
			set[name] = true
		}
	}
	out := make([]string, 0, len(set))
	for name := range set {
		out = append(out, name)
	}
	sort.Strings(out)
	return out
}

func (t *Tracker) closeStep(now time.Time) {
	if t.stepOpen < 0 {
		return
	}
	sp := &t.timeline[t.stepOpen]
	sp.Ended = now
	sp.Seconds = now.Sub(sp.Began).Seconds()
	t.stepOpen = -1
}

func (t *Tracker) closePause(now time.Time) {
	if t.pauseOpen < 0 {
		return
	}
	p := &t.pauses[t.pauseOpen]
	p.ResumedAt = now
	p.Seconds = now.Sub(p.At).Seconds()
	p.Resumed = true
	t.pauseOpen = -1
	t.paused = false
}

// Events returns the incidents since the last call and clears them.
func (t *Tracker) Events() []Incident {
	out := t.pending
	t.pending = nil
	return out
}

// End is how the run ended; EndRunning while it still goes.
func (t *Tracker) End() End { return t.end }

// Timeline is the step timeline; an open step's seconds run to the last Feed or Frame.
func (t *Tracker) Timeline() []StepSpan {
	out := make([]StepSpan, len(t.timeline))
	copy(out, t.timeline)
	if t.stepOpen >= 0 {
		sp := &out[t.stepOpen]
		sp.Seconds = t.lastNow.Sub(sp.Began).Seconds()
	}
	return out
}

// Legs is the time spent in each leg, in first-seen order.
func (t *Tracker) Legs() []LegSpan {
	var out []LegSpan
	idx := map[string]int{}
	for _, sp := range t.Timeline() {
		if sp.Leg == "" {
			continue
		}
		i, ok := idx[sp.Leg]
		if !ok {
			i = len(out)
			idx[sp.Leg] = i
			out = append(out, LegSpan{Leg: sp.Leg})
		}
		out[i].Seconds += sp.Seconds
	}
	return out
}

// Deaths is the total, the deaths by step and the deaths by bot name.
func (t *Tracker) Deaths() (int, map[int]int, map[string]int) {
	byStep := make(map[int]int, len(t.deathByStep))
	for k, v := range t.deathByStep {
		byStep[k] = v
	}
	byBot := make(map[string]int, len(t.deathByBot))
	for k, v := range t.deathByBot {
		byBot[k] = v
	}
	return t.deathTotal, byStep, byBot
}

// Wipes is the step of each wipe incident.
func (t *Tracker) Wipes() []int {
	out := make([]int, len(t.wipes))
	copy(out, t.wipes)
	return out
}

// BossEngaged is the first boss the frames showed engaged.
func (t *Tracker) BossEngaged() (entry uint32, step int, ok bool) {
	return t.bossEntry, t.bossStep, t.bossFired
}

// Pauses is the pauses so far; an open pause's seconds run to the last Feed or Frame.
func (t *Tracker) Pauses() []Pause {
	out := make([]Pause, len(t.pauses))
	copy(out, t.pauses)
	if t.pauseOpen >= 0 {
		p := &out[t.pauseOpen]
		p.Seconds = t.lastNow.Sub(p.At).Seconds()
	}
	return out
}

// StepsReached and StepsTotal are the numbers the run's status line gave.
func (t *Tracker) StepsReached() int { return t.stepsReached }
func (t *Tracker) StepsTotal() int   { return t.stepsTotal }
