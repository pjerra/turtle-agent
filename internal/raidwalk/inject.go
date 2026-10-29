package raidwalk

import (
	"errors"
	"fmt"
	"math"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/pjerra/turtle-agent/internal/dungeon"
	"github.com/pjerra/turtle-agent/internal/mapview"
	"github.com/pjerra/turtle-agent/internal/packet"
	"github.com/pjerra/turtle-agent/internal/planexport"
	"github.com/pjerra/turtle-agent/internal/tbm"
)

// Fault injection's pure side (spec raid-walk-addons 1.2-1.5): the injector never sleeps, never calls a clock and
// sends nothing itself. The command feeds it every watch tick and executes the actions it returns through the two
// allow-lists of inject_allow.go.
//
// The fault kinds of --inject (1.1).
type InjectKind string

const (
	InjectKillMT  InjectKind = "kill-mt"
	InjectRunaway InjectKind = "runaway"
	InjectAddPack InjectKind = "add-pack"
	InjectLag     InjectKind = "lag"
)

// The verdicts of an InjectResult (1.2).
const (
	VerdictPass         = "pass"
	VerdictFail         = "fail"
	VerdictInconclusive = "inconclusive"
	VerdictNotFired     = "not-fired"
)

// ActionKind is what the command has to do for one InjectAction.
type ActionKind string

const (
	ActGM         ActionKind = "gm"          // send Line through the walk's gm path
	ActSelectSelf ActionKind = "select_self" // target the commander's own GUID, then read status.target back
	ActSelect     ActionKind = "select"      // target GUID, then read status.target back
	ActOrder      ActionKind = "order"       // commander {op:"cmd", verb:Verb, args:Args}
	ActWait       ActionKind = "wait"        // the injector's own wait, already modeled; execute it or not
)

// InjectSpec is one parsed --inject SPEC.
type InjectSpec struct {
	Kind  InjectKind
	From  int // eligible at step >= From (1 by default)
	Dist  int // runaway: the placement distance in yards
	HoldS int // runaway: how long the victim is held out
	MaxYd int // add-pack: the candidate's farthest distance from the lead bot
	Secs  int // lag: how long the victim is frozen
	Raw   string
}

// ParseInjects parses the --inject specs (spec 1.1). Anything the grammar does not name is an error naming the spec.
func ParseInjects(specs []string, r *Roster) ([]InjectSpec, error) {
	if len(specs) > 4 {
		return nil, fmt.Errorf("--inject takes at most 4 faults, not %d", len(specs))
	}
	seen := map[InjectKind]bool{}
	out := make([]InjectSpec, 0, len(specs))
	for _, raw := range specs {
		sp, err := parseInject(raw)
		if err != nil {
			return nil, err
		}
		if seen[sp.Kind] {
			return nil, fmt.Errorf("--inject %q: %s is given twice", raw, sp.Kind)
		}
		if sp.Kind == InjectKillMT && !rosterHasOT(r) {
			return nil, errors.New("kill-mt needs an ot batch: nobody would take over")
		}
		seen[sp.Kind] = true
		out = append(out, sp)
	}
	return out, nil
}

func rosterHasOT(r *Roster) bool {
	if r == nil {
		return false
	}
	for _, b := range r.Batches {
		if b.Role == RoleOT {
			return true
		}
	}
	return false
}

func parseInject(raw string) (InjectSpec, error) {
	fail := func(format string, a ...any) (InjectSpec, error) {
		return InjectSpec{}, fmt.Errorf("--inject %q: %s", raw, fmt.Sprintf(format, a...))
	}
	parts := strings.Split(raw, ",")
	head := parts[0]
	kindWord, from := head, 0
	if i := strings.IndexByte(head, '@'); i >= 0 {
		kindWord = head[:i]
		rest := head[i+1:]
		if !strings.HasPrefix(rest, "from=") {
			return fail("@ wants from=<N>, not %q", rest)
		}
		n, err := strconv.Atoi(strings.TrimPrefix(rest, "from="))
		if err != nil || n < 1 {
			return fail("@from wants a step number from 1, not %q", strings.TrimPrefix(rest, "from="))
		}
		from = n
	}
	kind := InjectKind(kindWord)
	switch kind {
	case InjectKillMT, InjectRunaway, InjectAddPack, InjectLag:
	default:
		return fail("unknown fault kind %q", kindWord)
	}
	keys := map[string]int{}
	for _, kv := range parts[1:] {
		if strings.Contains(kv, "@") {
			return fail("@ goes right after the kind, not %q", kv)
		}
		k, v, ok := strings.Cut(kv, "=")
		if !ok {
			return fail("wants key=value, not %q", kv)
		}
		if _, dup := keys[k]; dup {
			return fail("%s is given twice", k)
		}
		n, err := strconv.Atoi(v)
		if err != nil {
			return fail("%s wants a whole number, not %q", k, v)
		}
		keys[k] = n
	}
	sp := InjectSpec{Kind: kind, From: 1, Raw: raw}
	if from > 0 {
		sp.From = from
	}
	allowed := map[string]bool{}
	switch kind {
	case InjectRunaway:
		allowed["dist"], allowed["hold"] = true, true
		sp.Dist, sp.HoldS = 150, 90
		if v, ok := keys["dist"]; ok {
			if v < 110 || v > 250 {
				return fail("dist is 110-250 yd, not %d", v)
			}
			sp.Dist = v
		}
		if v, ok := keys["hold"]; ok {
			if v < 30 || v > 180 {
				return fail("hold is 30-180 s, not %d", v)
			}
			sp.HoldS = v
		}
	case InjectAddPack:
		allowed["max"] = true
		sp.MaxYd = 60
		if v, ok := keys["max"]; ok {
			if v < 30 || v > 80 {
				return fail("max is 30-80 yd, not %d", v)
			}
			sp.MaxYd = v
		}
	case InjectLag:
		allowed["secs"] = true
		sp.Secs = 30
		if v, ok := keys["secs"]; ok {
			if v < 10 || v > 90 {
				return fail("secs is 10-90 s, not %d", v)
			}
			sp.Secs = v
		}
	}
	for k := range keys {
		if !allowed[k] {
			return fail("%s has no %s", kind, k)
		}
	}
	return sp, nil
}

// RosterNames are the walk's hires by role (spec 1.5).
type RosterNames struct {
	MT string
	OT []string
	// Rest is in hire order: the last one is the runaway's and lag's default victim.
	Rest []string
	// Tanks are the layout's tanks (TBM:RAID_LAYOUT), MT included.
	Tanks []string
}

// BotObs is one hire's merged state (newest frame naming it, <= 10 s old).
type BotObs struct {
	GUID     packet.GUID
	Pos      dungeon.Point
	Dead     bool
	InCombat bool
	At       time.Time
}

// HostileObs is one hostile's merged state (seen in the last 4 s).
type HostileObs struct {
	GUID     packet.GUID
	Entry    uint32
	Pos      dungeon.Point
	HPPct    int
	Target   packet.GUID
	Kind     string
	Reaction string
	Flags    string
	At       time.Time
}

// InjectObs is everything one watch tick knows (spec 1.5).
type InjectObs struct {
	Now         time.Time
	Run         tbm.RunStatus
	HaveRun     bool
	Notes       []string // the RUN notes of this plan read since the last tick, in order
	UntilStep   int
	BossEngaged bool
	Bots        map[string]BotObs // hires only
	Hostiles    []HostileObs      // seen in the last 4 s
	Lead        string            // the lead bot's name (the frame's party record)
	Wipe        bool              // the tracker fired IncidentWipe
}

// InjectAction is one thing the command has to do, in the order the injector returned it.
type InjectAction struct {
	Kind ActionKind
	Line string
	GUID packet.GUID
	Verb string
	Args []string
	For  time.Duration
	// Unit is the unit the lines that follow this selection act on (a reversible .freeze/.unfreeze victim): the
	// command proves the selection is safe for that unit before it sends them. Empty for a selection no line follows.
	Unit string
	// Release marks a selection that only precedes an .unfreeze: unlike a freeze, a wrong (non-frozen) unit loses
	// nothing, so a crowded target must not keep the injector from releasing its own bot.
	Release bool
}

// InjectCtx is what the allow-lists check against (spec 1.4).
type InjectCtx struct {
	Hires        map[string]bool
	Map          uint32
	Lead         dungeon.Point
	HaveLead     bool
	SelfSelected bool
	SelectedHire string
	Fault        InjectKind
	Victim       string
	Candidate    packet.GUID
}

// InjectLine is one line the injector had the command send (for inject.json).
type InjectLine struct {
	T    string `json:"t"`
	Kind string `json:"kind"`
	Text string `json:"text"`
}

// InjectTarget is add-pack's chosen hostile.
type InjectTarget struct {
	GUID  string `json:"guid"`
	Entry uint32 `json:"entry"`
}

// InjectIncident is a fault run's blocking finding: something the walk could not undo (a bot still frozen after
// cleanup's recovery deadline). It goes to inject.json and the summary, with the exact manual command.
type InjectIncident struct {
	Severity string   `json:"severity"` // "blocking"
	Kind     string   `json:"kind"`     // "cleanup_incomplete"
	Bots     []string `json:"bots"`
	Reason   string   `json:"reason"`
	Manual   string   `json:"manual"`
}

// InjectResult is one fault's result (spec 1.5).
type InjectResult struct {
	Kind     InjectKind     `json:"kind"`
	Spec     string         `json:"spec"`
	Verdict  string         `json:"verdict"`
	Reason   string         `json:"reason"`
	FiredAt  *time.Time     `json:"fired_at"`
	Step     int            `json:"step"`
	Leg      string         `json:"leg"`
	Victim   string         `json:"victim"`
	Target   *InjectTarget  `json:"target"`
	Lines    []InjectLine   `json:"lines"`
	Measured map[string]any `json:"measured"`
	Notes    []string       `json:"notes"`
}

// The injector's own state machine (unexported).
const (
	injectPending = "pending"
	injectActive  = "active"
	injectDone    = "done"
)

const injectTimeLayout = "2006-01-02T15:04:05.000Z07:00"

// injectFault is one fault's state between ticks.
type injectFault struct {
	spec    InjectSpec
	state   string
	phase   string
	phaseAt time.Time
	verdict string
	reason  string

	lastReason string
	firedAt    time.Time
	step       int
	leg        string
	victim     string
	q          dungeon.Point
	mt         packet.GUID
	attackers  map[packet.GUID]bool
	candidate  packet.GUID
	candEntry  uint32
	pack       map[packet.GUID]bool
	puller     string
	measured   map[string]any
	notes      []string
	lines      []InjectLine
	f4Re       *regexp.Regexp

	frozen    bool
	unfrozen  bool
	t0        time.Time
	t1        time.Time
	tDie      time.Time
	lastTick  time.Time
	prevDead  map[string]bool
	pauseOpen bool
	pauseNote string
	pauses    []string

	// kill-mt
	killed       bool
	killedAt     time.Time
	pickup       bool
	pickupS      float64
	pickupBy     string
	loose        bool
	looseS       float64
	deathsAfter  int
	fightEnded   bool
	fightEndedAt time.Time
	fightEndS    float64
	fightEndDone bool
	mtWasDead    bool
	// runaway
	fightS     float64
	f4Seen     bool
	f4At       time.Time
	f4S        float64
	back       bool
	backS      float64
	backAt     time.Time
	straggler  string
	otherNotes []string
	triggered  bool
	bossSeen   bool
	// add-pack
	engaged       bool
	engagedS      float64
	stepAtEnd     int
	phaseAtEnd    string
	packDead      bool
	runContinued  bool
	runContinuedS float64
	// lag
	rejoined bool
	rejoinAt time.Time
	rejoinS  float64
	maxGap   float64
	ladder   []string
}

// Injector is one walk's faults.
type Injector struct {
	names       RosterNames
	hires       map[string]bool
	mapID       uint32
	rows        []planexport.StepRow
	bossEntries map[uint32]bool

	faults []*injectFault
	active *injectFault

	fightOn       bool
	fightStart    time.Time
	fightRaw      bool
	lastFightSign time.Time
	fightEndAt    time.Time
	noFightSince  time.Time

	lead     dungeon.Point
	haveLead bool
	wipe     bool

	selfSelected bool
	selectedHire string

	lastVerdict time.Time
	lastTick    time.Time
	lastObs     InjectObs
	haveObs     bool

	pending  []pendingInject
	out      []InjectAction
	finished bool
}

type pendingInject struct {
	act   InjectAction
	fault *injectFault
	at    time.Time
	// cleanup is one of Cleanup's actions: it carries no verdict and only its acknowledgement changes state.
	cleanup bool
}

// NewInjector builds the injector of one walk (spec 1.5).
func NewInjector(specs []InjectSpec, names RosterNames, rows planexport.Rows, plan *dungeon.Plan) *Injector {
	in := &Injector{names: names, hires: map[string]bool{}, bossEntries: map[uint32]bool{}}
	for _, n := range append(append(append([]string{names.MT}, names.OT...), names.Rest...), names.Tanks...) {
		if n != "" {
			in.hires[n] = true
		}
	}
	if plan != nil {
		in.mapID = plan.Map
		for _, e := range mapview.BossEntries(plan) {
			in.bossEntries[e] = true
		}
		for _, leg := range plan.Legs {
			if leg.Boss != nil && leg.Boss.Hold != nil {
				for _, e := range leg.Boss.Hold.Entries {
					in.bossEntries[e] = true
				}
			}
		}
	}
	steps := append([]planexport.StepRow(nil), rows.Steps...)
	sort.SliceStable(steps, func(i, j int) bool {
		if steps[i].Leg != steps[j].Leg {
			return steps[i].Leg < steps[j].Leg
		}
		return steps[i].Seq < steps[j].Seq
	})
	in.rows = steps
	for _, sp := range specs {
		in.faults = append(in.faults, newFault(sp))
	}
	return in
}

func newFault(sp InjectSpec) *injectFault {
	f := &injectFault{
		spec:       sp,
		state:      injectPending,
		measured:   map[string]any{},
		attackers:  map[packet.GUID]bool{},
		pack:       map[packet.GUID]bool{},
		prevDead:   map[string]bool{},
		lastReason: "the watch ended before the fault could fire",
	}
	var keys []string
	switch sp.Kind {
	case InjectKillMT:
		keys = []string{"killed_at", "attackers_at_kill", "pickup_s", "pickup_by", "loose_s", "deaths_after", "fight_end_s", "wipe", "mt_revived_s", "pauses_after"}
	case InjectRunaway:
		keys = []string{"placed_yd", "out_of_reach_s", "f4_at_s", "f4_note", "other_notes", "back_s", "fight_s"}
	case InjectAddPack:
		keys = []string{"engaged_s", "fight_end_s", "deaths", "wipe", "pack_dead", "run_continued_s"}
	case InjectLag:
		keys = []string{"max_gap_yd", "rejoin_s", "ladder_notes", "pauses"}
	}
	for _, k := range keys {
		f.measured[k] = nil
	}
	return f
}

// Ctx is the context the allow-lists check against.
func (in *Injector) Ctx() InjectCtx {
	c := InjectCtx{
		Hires:        in.hires,
		Map:          in.mapID,
		Lead:         in.lead,
		HaveLead:     in.haveLead,
		SelfSelected: in.selfSelected,
		SelectedHire: in.selectedHire,
	}
	if in.active != nil {
		c.Fault = in.active.spec.Kind
		c.Victim = in.active.victim
		c.Candidate = in.active.candidate
	}
	return c
}

// Tick says what to do now, in order; an empty slice means nothing (spec 1.5). It never waits.
func (in *Injector) Tick(o InjectObs) []InjectAction {
	if in.finished {
		return nil
	}
	in.observe(o)
	if in.active != nil {
		in.stepFault(in.active, o)
	} else {
		in.tryFire(o)
	}
	out := in.out
	in.out = nil
	return out
}

// observe folds the global facts of one tick: the fight, the lead, the wipe flag and the boss.
func (in *Injector) observe(o InjectObs) {
	in.lastObs, in.haveObs = o, true
	in.lastTick = o.Now
	if b, ok := o.Bots[o.Lead]; ok && o.Lead != "" {
		in.lead, in.haveLead = b.Pos, true
	}
	if o.Wipe {
		in.wipe = true
	}
	// Historical boss exposure is per active fault, where it only affects that fault's verdict (runaway's F4):
	// the fire blocks read the current observation instead.
	if o.BossEngaged || (o.HaveRun && o.Run.Kind == "boss") {
		if in.active != nil {
			in.active.bossSeen = true
		}
	}
	raw := injectFightOn(o)
	if raw {
		in.fightRaw = true
		in.lastFightSign = o.Now
		in.noFightSince = time.Time{}
		if !in.fightOn {
			in.fightOn = true
			in.fightStart = o.Now
		}
		return
	}
	in.fightRaw = false
	if in.fightOn && o.Now.Sub(in.lastFightSign) >= 10*time.Second {
		in.fightOn = false
		in.fightEndAt = o.Now
		in.noFightSince = o.Now
		if in.active != nil {
			in.active.fightEnded = true
			in.active.fightEndedAt = o.Now
		}
	}
	if !in.fightOn && in.noFightSince.IsZero() {
		in.noFightSince = o.Now
	}
}

func injectFightOn(o InjectObs) bool {
	party := injectParty(o)
	for _, h := range o.Hostiles {
		if h.Target != 0 && party[h.Target] {
			return true
		}
	}
	for _, b := range o.Bots {
		if b.InCombat {
			return true
		}
	}
	return false
}

func injectParty(o InjectObs) map[packet.GUID]bool {
	p := make(map[packet.GUID]bool, len(o.Bots))
	for _, b := range o.Bots {
		if b.GUID != 0 {
			p[b.GUID] = true
		}
	}
	return p
}

// tryFire fires the first fault in command-line order whose rules hold (spec 1.2).
func (in *Injector) tryFire(o InjectObs) {
	if reason := in.globalBlock(o); reason != "" {
		for _, f := range in.faults {
			if f.state == injectPending {
				f.lastReason = reason
			}
		}
		return
	}
	for _, f := range in.faults {
		if f.state != injectPending {
			continue
		}
		reason := in.eligible(f, o)
		if reason != "" {
			f.lastReason = reason
			continue
		}
		in.fire(f, o)
		return
	}
}

func (in *Injector) globalBlock(o InjectObs) string {
	if !o.HaveRun {
		return "no run yet"
	}
	switch o.Run.State {
	case tbm.RunRunning:
	case tbm.RunPaused:
		return "the run is paused"
	default:
		return "the run ended"
	}
	if o.Run.Kind == "boss" {
		return "on a boss step"
	}
	if o.BossEngaged {
		return "a boss is engaged"
	}
	if in.wipe {
		return "a wipe is on"
	}
	if o.UntilStep > 0 && o.Run.Step >= o.UntilStep-2 {
		return fmt.Sprintf("within 2 steps of the until stop (step %d)", o.UntilStep)
	}
	if !in.lastVerdict.IsZero() {
		if o.Now.Sub(in.lastVerdict) < 60*time.Second {
			return "60 s after the previous fault"
		}
		if in.fightOn {
			return "a fight is on"
		}
	}
	return ""
}

// eligible checks one fault's own fire condition; the reason is the last unmet condition (spec 1.2, 1.3).
func (in *Injector) eligible(f *injectFault, o InjectObs) string {
	if o.Run.Step < f.spec.From {
		return fmt.Sprintf("step %d is before @from=%d", o.Run.Step, f.spec.From)
	}
	switch f.spec.Kind {
	case InjectKillMT:
		if o.Run.Kind != planexport.KindPull {
			return "not on a pull step"
		}
		if !in.fightOn || o.Now.Sub(in.fightStart) < 6*time.Second {
			return "no fight for 6 s"
		}
		party := injectParty(o)
		n, sum := 0, 0
		for _, h := range o.Hostiles {
			if party[h.Target] {
				n++
				sum += h.HPPct
			}
		}
		if n < 2 {
			return "fewer than 2 hostiles on the party"
		}
		mt, ok := o.Bots[in.names.MT]
		if !ok || mt.Dead {
			return "the main tank is not alive"
		}
		if !in.hostileTargets(o, mt.GUID) {
			return "no hostile targets the main tank"
		}
		if mean := float64(sum) / float64(n); mean < 20 || mean > 90 {
			return fmt.Sprintf("the hostiles' mean health is %.0f%%", mean)
		}
		if !in.anyOTALive(o) {
			return "no off tank is alive"
		}
		return ""
	case InjectRunaway:
		if o.Run.Kind != planexport.KindMove && o.Run.Kind != planexport.KindMoveDirect {
			return "not on a move step"
		}
		if !in.noFightFor(o, 10*time.Second) {
			return "a fight was on less than 10 s ago"
		}
		if in.runawayVictim(o) == "" {
			return "no rest hire is alive and out of combat"
		}
		if !in.haveLead {
			return "the lead bot's position is unknown"
		}
		if _, ok := in.placement(f, o); !ok {
			return fmt.Sprintf("no plan point behind the lead bot in the %d-%d yd band", maxInt(110, f.spec.Dist-30), f.spec.Dist+50)
		}
		return ""
	case InjectAddPack:
		if o.Run.Kind != planexport.KindPull {
			return "not on a pull step"
		}
		if !in.fightOn || o.Now.Sub(in.fightStart) < 3*time.Second {
			return "no fight for 3 s"
		}
		c, _ := in.addPackCandidate(f, o)
		if c == 0 {
			return "no idle hostile to pull"
		}
		if in.addPackPuller(o, entryPos(o, c)) == "" {
			return "no rest hire is alive to pull"
		}
		return ""
	case InjectLag:
		if o.Run.Kind != planexport.KindMove && o.Run.Kind != planexport.KindMoveDirect {
			return "not on a move step"
		}
		if !in.noFightFor(o, 10*time.Second) {
			return "a fight was on less than 10 s ago"
		}
		if in.lagVictim(o) == "" {
			return "no rest hire is alive, out of combat, within 30 yd of the lead bot"
		}
		return ""
	}
	return "unknown fault"
}

func (in *Injector) noFightFor(o InjectObs, d time.Duration) bool {
	return !in.fightOn && !in.noFightSince.IsZero() && o.Now.Sub(in.noFightSince) >= d
}

func (in *Injector) hostileTargets(o InjectObs, g packet.GUID) bool {
	for _, h := range o.Hostiles {
		if h.Target == g {
			return true
		}
	}
	return false
}

func (in *Injector) anyOTALive(o InjectObs) bool {
	for _, n := range in.names.OT {
		if b, ok := o.Bots[n]; ok && !b.Dead {
			return true
		}
	}
	return false
}

func (in *Injector) runawayVictim(o InjectObs) string {
	last := ""
	for _, n := range in.names.Rest {
		if b, ok := o.Bots[n]; ok && !b.Dead && !b.InCombat {
			last = n
		}
	}
	return last
}

// chosenRunawayVictim is the victim of a runaway fault that already fired (lag must not freeze the same bot).
func (in *Injector) chosenRunawayVictim() string {
	for _, f := range in.faults {
		if f.spec.Kind == InjectRunaway && f.victim != "" {
			return f.victim
		}
	}
	return ""
}

func (in *Injector) lagVictim(o InjectObs) string {
	lead, ok := o.Bots[o.Lead]
	if !ok {
		return ""
	}
	skip := in.chosenRunawayVictim()
	last := ""
	for _, n := range in.names.Rest {
		if n == skip {
			continue
		}
		b, ok := o.Bots[n]
		if !ok || b.Dead || b.InCombat {
			continue
		}
		if dist3(b.Pos, lead.Pos) <= 30 {
			last = n
		}
	}
	return last
}

// placement is Q: the positioned row of a step <= the run's step in the runaway's band, closest to dist (ties:
// the earlier step), spec 1.3.
func (in *Injector) placement(f *injectFault, o InjectObs) (dungeon.Point, bool) {
	lead, ok := o.Bots[o.Lead]
	if !ok {
		return dungeon.Point{}, false
	}
	lo := float64(maxInt(110, f.spec.Dist-30))
	hi := float64(f.spec.Dist + 50)
	var best dungeon.Point
	bestScore := math.Inf(1)
	found := false
	for i, s := range in.rows {
		if i+1 > o.Run.Step {
			continue
		}
		if s.X == 0 && s.Y == 0 && s.Z == 0 {
			continue
		}
		p := dungeon.Point{X: s.X, Y: s.Y, Z: s.Z}
		d := dist3(p, lead.Pos)
		if d < lo || d > hi {
			continue
		}
		score := math.Abs(d - float64(f.spec.Dist))
		if !found || score < bestScore {
			best, bestScore, found = p, score, true
		}
	}
	return best, found
}

// addPackCandidate is the nearest idle hostile in add-pack's band (spec 1.3); 0 when none.
func (in *Injector) addPackCandidate(f *injectFault, o InjectObs) (packet.GUID, uint32) {
	lead, ok := o.Bots[o.Lead]
	if !ok {
		return 0, 0
	}
	party := injectParty(o)
	var targeted []dungeon.Point
	for _, h := range o.Hostiles {
		if party[h.Target] {
			targeted = append(targeted, h.Pos)
		}
	}
	var best packet.GUID
	var bestEntry uint32
	bestD := math.Inf(1)
	for _, h := range o.Hostiles {
		if h.Target != 0 || h.HPPct != 100 {
			continue
		}
		if !(h.Reaction == "" || h.Reaction == "h" || h.Reaction == "-") {
			continue
		}
		if strings.ContainsAny(h.Kind, "ukvg") || strings.Contains(h.Flags, "e") {
			continue
		}
		if in.bossEntries[h.Entry] {
			continue
		}
		tooClose := false
		for _, p := range targeted {
			if dist3(h.Pos, p) < 20 {
				tooClose = true
				break
			}
		}
		if tooClose {
			continue
		}
		d := dist3(h.Pos, lead.Pos)
		if d < 20 || d > float64(f.spec.MaxYd) {
			continue
		}
		if best == 0 || d < bestD {
			best, bestEntry, bestD = h.GUID, h.Entry, d
		}
	}
	return best, bestEntry
}

func (in *Injector) extraPack(o InjectObs, c packet.GUID) map[packet.GUID]bool {
	pack := map[packet.GUID]bool{c: true}
	var at dungeon.Point
	for _, h := range o.Hostiles {
		if h.GUID == c {
			at = h.Pos
		}
	}
	for _, h := range o.Hostiles {
		if dist3(h.Pos, at) <= 10 {
			pack[h.GUID] = true
		}
	}
	return pack
}

func (in *Injector) addPackPuller(o InjectObs, pos dungeon.Point) string {
	best := ""
	bestD := math.Inf(1)
	for _, n := range in.names.Rest {
		b, ok := o.Bots[n]
		if !ok || b.Dead {
			continue
		}
		if d := dist3(b.Pos, pos); d < bestD {
			best, bestD = n, d
		}
	}
	return best
}

// fire starts a fault and emits its first action (spec 1.3).
func (in *Injector) fire(f *injectFault, o InjectObs) {
	f.state = injectActive
	f.firedAt = o.Now
	f.step = o.Run.Step
	f.leg = o.Run.Leg
	in.active = f
	switch f.spec.Kind {
	case InjectKillMT:
		f.victim = in.names.MT
		f.mt = o.Bots[in.names.MT].GUID
		f.attackers = in.attackersAt(o, f.mt, nil)
		setAttackers(f, o, f.attackers)
		f.prevDead = injectDead(o)
		f.phase = "select_mt"
		in.emitSelect(f, f.mt, o.Now)
	case InjectRunaway:
		f.victim = in.runawayVictim(o)
		f.q, _ = in.placement(f, o)
		f.f4Re = injectF4Re(f.victim)
		f.phase = "go"
		f.phaseAt = o.Now
		f.lastTick = o.Now
		in.emitGM(f, injectGoLine(f.q, in.mapID), o.Now)
		in.emit(f, InjectAction{Kind: ActWait, For: 2 * time.Second}, o.Now)
	case InjectAddPack:
		c, entry := in.addPackCandidate(f, o)
		f.candidate, f.candEntry = c, entry
		f.pack = in.extraPack(o, c)
		f.puller = in.addPackPuller(o, entryPos(o, c))
		f.prevDead = injectDead(o)
		f.phase = "order"
		in.emitOrder(f, "attack", []string{c.String(), f.puller}, o.Now)
	case InjectLag:
		f.victim = in.lagVictim(o)
		f.phase = "select_freeze"
		in.emitSelectSelf(f, f.victim, false, o.Now)
	}
}

// stepFault advances the active fault and emits what it needs (spec 1.3).
func (in *Injector) stepFault(f *injectFault, o InjectObs) {
	if f.state != injectActive {
		return
	}
	if in.wipe {
		in.wipeOutcome(f, o, o.Now)
		return
	}
	switch f.spec.Kind {
	case InjectKillMT:
		in.stepKill(f, o)
	case InjectRunaway:
		in.stepRunaway(f, o)
	case InjectAddPack:
		in.stepAddPack(f, o)
	case InjectLag:
		in.stepLag(f, o)
	}
}

// faultTookEffect says whether a fault's disruptive action was acknowledged or observed: the MT died, the extra
// pack engaged, the victim froze. A wipe before that point says nothing about the fault.
func (in *Injector) faultTookEffect(f *injectFault, o InjectObs) bool {
	switch f.spec.Kind {
	case InjectKillMT:
		if f.killed {
			return true
		}
		if f.phase == "dying" {
			mt, ok := o.Bots[in.names.MT]
			return ok && mt.Dead
		}
	case InjectAddPack:
		// The order has to be acknowledged first: an idle pack's engagement before that is not this fault's pull.
		return f.engaged || (f.phase != "order" && in.packEngaged(o, f.pack))
	case InjectRunaway, InjectLag:
		// t0 is set when the `.freeze` is acknowledged.
		return !f.t0.IsZero()
	}
	return false
}

// wipeOutcome ends an active fault once the wipe is latched (spec 1.2): only a fault whose effect came first can be
// blamed for it; one still waiting for its effect ends inconclusive.
func (in *Injector) wipeOutcome(f *injectFault, o InjectObs, at time.Time) {
	if !in.faultTookEffect(f, o) {
		in.endFault(f, VerdictInconclusive, "wipe before the fault took effect", at)
		return
	}
	reason := "wipe while the victim was out of reach"
	switch f.spec.Kind {
	case InjectKillMT:
		reason = "wipe after the MT died"
	case InjectAddPack:
		reason = "wipe after the extra pack was pulled"
	case InjectLag:
		reason = "wipe while the victim lagged"
	}
	in.endFault(f, VerdictFail, reason, at)
}

func (in *Injector) stepKill(f *injectFault, o InjectObs) {
	switch f.phase {
	case "select_mt":
		// waiting for the read-back
	case "die_ready":
		f.tDie = o.Now
		in.emitGM(f, ".die", o.Now)
		in.emitSelectSelf(f, "", false, o.Now)
		f.phase = "dying"
	case "dying":
		if mt, ok := o.Bots[in.names.MT]; ok && mt.Dead {
			f.killed = true
			f.killedAt = o.Now
			setNum(f, "killed_at", o.Now.Sub(f.firedAt).Seconds())
			f.attackers = in.attackersAt(o, f.mt, f.attackers)
			setAttackers(f, o, f.attackers)
			f.prevDead = injectDead(o)
			f.phase = "measuring"
			in.killMeasure(f, o)
		} else if o.Now.Sub(f.tDie) >= 6*time.Second {
			in.endFault(f, VerdictInconclusive, "the MT did not die", o.Now)
		}
	case "measuring":
		in.killMeasure(f, o)
	}
}

func (in *Injector) killMeasure(f *injectFault, o InjectObs) {
	now := o.Now
	if !f.pickup {
		if name := in.pickupTank(o, f.mt, f.attackers); name != "" {
			f.pickup = true
			f.pickupS = now.Sub(f.firedAt).Seconds()
			f.pickupBy = name
			setNum(f, "pickup_s", f.pickupS)
			setStr(f, "pickup_by", f.pickupBy)
		}
	}
	if !f.loose && in.attackersTargetNonTank(o, f.mt, f.attackers) {
		f.loose = true
		f.looseS = now.Sub(f.firedAt).Seconds()
		setNum(f, "loose_s", f.looseS)
	}
	for name, b := range o.Bots {
		if name == in.names.MT {
			continue
		}
		if b.Dead && !f.prevDead[name] && (f.fightEndedAt.IsZero() || !now.After(f.fightEndedAt)) {
			f.deathsAfter++
		}
		f.prevDead[name] = b.Dead
	}
	setNum(f, "deaths_after", float64(f.deathsAfter))
	if mt, ok := o.Bots[in.names.MT]; ok {
		if f.mtWasDead && !mt.Dead {
			setNum(f, "mt_revived_s", now.Sub(f.firedAt).Seconds())
		}
		f.mtWasDead = mt.Dead
	}
	setBool(f, "wipe", in.wipe)
	if now.Sub(f.firedAt) <= 120*time.Second {
		in.trackPauses(f, o)
	}
	setStrings(f, "pauses_after", f.pauses)
	if !f.pickup && now.Sub(f.firedAt) >= 10*time.Second {
		in.endFault(f, VerdictFail, "no tank picked up the MT's attackers in 10 s", now)
		return
	}
	if f.fightEnded {
		if !f.fightEndDone {
			f.fightEndDone = true
			f.fightEndS = f.fightEndedAt.Sub(f.firedAt).Seconds()
			setNum(f, "fight_end_s", f.fightEndS)
			if f.fightEndS > 180 {
				in.endFault(f, VerdictFail, "the fight was not over in 180 s", now)
				return
			}
		}
		if now.Sub(f.fightEndedAt) >= 60*time.Second {
			in.endFault(f, VerdictPass, "", now)
		}
		return
	}
	if now.Sub(f.firedAt) > 180*time.Second {
		in.endFault(f, VerdictFail, "the fight was not over in 180 s", now)
	}
}

func (in *Injector) stepRunaway(f *injectFault, o InjectObs) {
	switch f.phase {
	case "go":
		if o.Now.Sub(f.phaseAt) >= 2*time.Second {
			in.emitGM(f, ".summon "+f.victim, o.Now)
			f.phase = "summon"
			f.phaseAt = o.Now
		}
	case "summon":
		v, ok := o.Bots[f.victim]
		if ok && dist3(v.Pos, f.q) <= 10 {
			in.emitSelectSelf(f, f.victim, false, o.Now)
			f.phase = "select_freeze"
		} else if o.Now.Sub(f.phaseAt) >= 10*time.Second {
			in.endFault(f, VerdictInconclusive, "the victim did not arrive at Q", o.Now)
		}
	case "select_freeze", "freezing", "unfreezing":
		// waiting for the read-back
	case "freeze_ready":
		in.emitGM(f, ".freeze "+f.victim, o.Now)
		f.phase = "freezing"
	case "frozen":
		in.runawayFrozen(f, o)
	case "select_unfreeze":
		// waiting for the read-back
	case "unfreeze_ready":
		in.emitGM(f, ".unfreeze "+f.victim, o.Now)
		f.phase = "unfreezing"
	}
}

func (in *Injector) runawayFrozen(f *injectFault, o InjectObs) {
	now := o.Now
	if in.fightRaw && !f.lastTick.IsZero() && !f.triggered {
		f.fightS += now.Sub(f.lastTick).Seconds()
	}
	f.lastTick = now
	for _, n := range o.Notes {
		if f.f4Re != nil && f.f4Re.MatchString(n) {
			if !f.f4Seen {
				f.f4Seen = true
				f.f4At = now
				f.f4S = now.Sub(f.firedAt).Seconds()
				setNum(f, "f4_at_s", f.f4S)
				setStr(f, "f4_note", n)
			}
			continue
		}
		if strings.Contains(n, f.victim) {
			f.otherNotes = append(f.otherNotes, n)
			if !f.f4Seen && strings.Contains(strings.ToLower(n), "summon") {
				f.straggler = n
			}
		}
	}
	setStrings(f, "other_notes", f.otherNotes)
	if o.HaveRun && o.Run.State == tbm.RunPaused && strings.Contains(o.Run.Note, f.victim) {
		if len(f.notes) == 0 || f.notes[len(f.notes)-1] != o.Run.Note {
			f.notes = append(f.notes, o.Run.Note)
		}
	}
	setNum(f, "fight_s", f.fightS)
	if v, vok := o.Bots[f.victim]; vok {
		if lead, lok := o.Bots[o.Lead]; lok {
			d := dist3(v.Pos, lead.Pos)
			if !f.back && d <= 30 {
				f.back = true
				f.backAt = now
				f.backS = now.Sub(f.firedAt).Seconds()
				setNum(f, "back_s", f.backS)
				setNum(f, "out_of_reach_s", f.backS)
			}
			if !f.back {
				setNum(f, "out_of_reach_s", now.Sub(f.t0).Seconds())
			}
		}
	}
	if f.triggered {
		return
	}
	trigger := false
	switch {
	case f.back:
		trigger = true
	case f.f4Seen && now.Sub(f.f4At) >= 15*time.Second:
		trigger = true
	case f.straggler != "":
		trigger = true
	case !f.t0.IsZero() && now.Sub(f.t0) >= time.Duration(f.spec.HoldS)*time.Second:
		trigger = true
	}
	if trigger {
		f.triggered = true
		f.phase = "select_unfreeze"
		in.emitSelectSelf(f, f.victim, true, now)
	}
}

func (in *Injector) runawayFinalize(f *injectFault, at time.Time) {
	switch {
	case f.straggler != "" && !f.f4Seen:
		in.endFault(f, VerdictFail, "summoned by the straggler ladder, not F4", at)
	case f.f4Seen:
		if f.back && f.backS <= f.f4S+15 {
			in.endFault(f, VerdictPass, "", at)
			return
		}
		back := "never"
		if f.back {
			back = fmt.Sprintf("%.1f s", f.backS)
		}
		in.endFault(f, VerdictFail, fmt.Sprintf("the victim was back %s after the runaway summon", back), at)
	case f.fightS > float64(f.spec.HoldS)/2 || f.bossSeen:
		in.endFault(f, VerdictInconclusive, "F4 waits for no fight / never on a boss step", at)
	default:
		placed := f.measured["placed_yd"]
		in.endFault(f, VerdictFail, fmt.Sprintf("no runaway summon in %d s with %s %v yd out", f.spec.HoldS, f.victim, placed), at)
	}
}

func (in *Injector) stepAddPack(f *injectFault, o InjectObs) {
	switch f.phase {
	case "order":
		// waiting for ACTION_ACK
	case "engaging":
		if in.packEngaged(o, f.pack) {
			f.engaged = true
			f.engagedS = o.Now.Sub(f.firedAt).Seconds()
			setNum(f, "engaged_s", f.engagedS)
			f.phase = "measuring"
			in.addPackMeasure(f, o)
		} else if o.Now.Sub(f.firedAt) >= 10*time.Second {
			in.endFault(f, VerdictInconclusive, "the extra pack did not engage", o.Now)
		}
	case "measuring":
		in.addPackMeasure(f, o)
	}
}

func (in *Injector) addPackMeasure(f *injectFault, o InjectObs) {
	now := o.Now
	in.trackPauses(f, o)
	setStrings(f, "pauses", f.pauses)
	for name, b := range o.Bots {
		if b.Dead && !f.prevDead[name] && (f.fightEndedAt.IsZero() || now.Sub(f.fightEndedAt) <= 30*time.Second) {
			f.deathsAfter++
		}
		f.prevDead[name] = b.Dead
	}
	setNum(f, "deaths", float64(f.deathsAfter))
	setBool(f, "wipe", in.wipe)
	if f.fightEnded {
		if !f.fightEndDone {
			f.fightEndDone = true
			f.fightEndS = f.fightEndedAt.Sub(f.firedAt).Seconds()
			setNum(f, "fight_end_s", f.fightEndS)
			f.packDead = in.packDeadAt(o, f.pack)
			setBool(f, "pack_dead", f.packDead)
			f.stepAtEnd = o.Run.Step
			f.phaseAtEnd = o.Run.Phase
			if f.fightEndS > 240 {
				in.endFault(f, VerdictFail, "the fight was not over in 240 s", now)
				return
			}
		}
		if !f.runContinued {
			if o.Run.Step > f.stepAtEnd || (o.Run.Step == f.stepAtEnd && injectPhaseRank(o.Run.Phase) > injectPhaseRank(f.phaseAtEnd)) {
				f.runContinued = true
				f.runContinuedS = now.Sub(f.fightEndedAt).Seconds()
				setNum(f, "run_continued_s", f.runContinuedS)
			}
		}
		if now.Sub(f.fightEndedAt) >= 120*time.Second {
			if f.pauseOpen {
				in.endFault(f, VerdictFail, "the walk stopped by a pause: "+f.pauseNote, now)
				return
			}
			in.endFault(f, VerdictPass, "", now)
		}
		return
	}
	if now.Sub(f.firedAt) > 240*time.Second {
		in.endFault(f, VerdictFail, "the fight was not over in 240 s", now)
	}
}

func (in *Injector) stepLag(f *injectFault, o InjectObs) {
	switch f.phase {
	case "select_freeze", "freezing", "unfreezing":
		// waiting for the read-back
	case "freeze_ready":
		in.emitGM(f, ".freeze "+f.victim, o.Now)
		f.phase = "freezing"
	case "frozen":
		in.lagMeasure(f, o)
		if o.Now.Sub(f.t0) >= time.Duration(f.spec.Secs)*time.Second && !f.triggered {
			f.triggered = true
			f.phase = "select_unfreeze"
			in.emitSelectSelf(f, f.victim, true, o.Now)
		}
	case "select_unfreeze":
		// waiting for the read-back
	case "unfreeze_ready":
		f.t1 = o.Now
		in.emitGM(f, ".unfreeze "+f.victim, o.Now)
		f.phase = "unfreezing"
	case "waiting_rejoin":
		in.lagMeasure(f, o)
		if f.rejoined && o.Now.Sub(f.rejoinAt) >= 30*time.Second {
			if f.pauseOpen {
				in.endFault(f, VerdictFail, "the walk stopped by a pause: "+f.pauseNote, o.Now)
				return
			}
			in.endFault(f, VerdictPass, "", o.Now)
		}
	}
}

func (in *Injector) lagMeasure(f *injectFault, o InjectObs) {
	now := o.Now
	in.trackPauses(f, o)
	for _, n := range o.Notes {
		if strings.Contains(n, f.victim) {
			f.ladder = append(f.ladder, n)
		}
	}
	setStrings(f, "ladder_notes", f.ladder)
	setStrings(f, "pauses", f.pauses)
	v, vok := o.Bots[f.victim]
	lead, lok := o.Bots[o.Lead]
	if !vok || !lok {
		return
	}
	d := dist3(v.Pos, lead.Pos)
	if !f.rejoined {
		if d > f.maxGap {
			f.maxGap = d
			setNum(f, "max_gap_yd", f.maxGap)
		}
		if f.phase == "waiting_rejoin" && d <= 30 {
			f.rejoined = true
			f.rejoinAt = now
			f.rejoinS = now.Sub(f.t1).Seconds()
			if f.rejoinS < 0 {
				f.rejoinS = 0
			}
			setNum(f, "rejoin_s", f.rejoinS)
		}
	}
	if f.phase == "waiting_rejoin" && !f.rejoined && now.Sub(f.t1) > 120*time.Second {
		in.endFault(f, VerdictFail, "the victim did not rejoin within 120 s", now)
	}
}

// trackPauses records one pause per contiguous paused span (spec 1.3's pauses fields).
func (in *Injector) trackPauses(f *injectFault, o InjectObs) {
	paused := o.HaveRun && o.Run.State == tbm.RunPaused
	if paused && !f.pauseOpen {
		f.pauseOpen = true
		f.pauseNote = o.Run.Note
		f.pauses = append(f.pauses, o.Run.Note)
		return
	}
	if !paused && f.pauseOpen {
		f.pauseOpen = false
	}
}

func (in *Injector) packEngaged(o InjectObs, pack map[packet.GUID]bool) bool {
	party := injectParty(o)
	for _, h := range o.Hostiles {
		if pack[h.GUID] && h.Target != 0 && party[h.Target] {
			return true
		}
	}
	return false
}

func (in *Injector) packDeadAt(o InjectObs, pack map[packet.GUID]bool) bool {
	for g := range pack {
		for _, h := range o.Hostiles {
			if h.GUID == g && h.HPPct > 0 {
				return false
			}
		}
	}
	return true
}

func (in *Injector) pickupTank(o InjectObs, mt packet.GUID, attackers map[packet.GUID]bool) string {
	tanks := in.tankGUIDs(o)
	delete(tanks, mt)
	for _, h := range o.Hostiles {
		if attackers[h.GUID] && tanks[h.Target] {
			return injectName(o, h.Target)
		}
	}
	return ""
}

func (in *Injector) attackersTargetNonTank(o InjectObs, mt packet.GUID, attackers map[packet.GUID]bool) bool {
	tanks := in.tankGUIDs(o)
	party := injectParty(o)
	for _, h := range o.Hostiles {
		if !attackers[h.GUID] || !party[h.Target] {
			continue
		}
		if !tanks[h.Target] {
			return true
		}
	}
	return false
}

func (in *Injector) tankGUIDs(o InjectObs) map[packet.GUID]bool {
	tanks := map[packet.GUID]bool{}
	for _, n := range append(append([]string{}, in.names.Tanks...), in.names.OT...) {
		if b, ok := o.Bots[n]; ok {
			tanks[b.GUID] = true
		}
	}
	return tanks
}

func (in *Injector) attackersAt(o InjectObs, mt packet.GUID, have map[packet.GUID]bool) map[packet.GUID]bool {
	out := map[packet.GUID]bool{}
	for g := range have {
		out[g] = true
	}
	for _, h := range o.Hostiles {
		if h.Target == mt {
			out[h.GUID] = true
		}
	}
	return out
}

func setAttackers(f *injectFault, o InjectObs, attackers map[packet.GUID]bool) {
	names := make([]string, 0, len(attackers))
	for g := range attackers {
		names = append(names, g.String())
	}
	sort.Strings(names)
	setStrings(f, "attackers_at_kill", names)
}

func injectDead(o InjectObs) map[string]bool {
	out := make(map[string]bool, len(o.Bots))
	for n, b := range o.Bots {
		out[n] = b.Dead
	}
	return out
}

func injectName(o InjectObs, g packet.GUID) string {
	for n, b := range o.Bots {
		if b.GUID == g {
			return n
		}
	}
	return ""
}

func entryPos(o InjectObs, g packet.GUID) dungeon.Point {
	for _, h := range o.Hostiles {
		if h.GUID == g {
			return h.Pos
		}
	}
	return dungeon.Point{}
}

func maxInt(a, b int) int {
	if a > b {
		return a
	}
	return b
}

func injectGoLine(p dungeon.Point, mapID uint32) string {
	return fmt.Sprintf(".go xyz %s %s %s %d", num32f(p.X), num32f(p.Y), num32f(p.Z), mapID)
}

func setNum(f *injectFault, k string, v float64) { f.measured[k] = round1(v) }
func setStr(f *injectFault, k, v string)         { f.measured[k] = v }
func setBool(f *injectFault, k string, v bool)   { f.measured[k] = v }
func setStrings(f *injectFault, k string, v []string) {
	if v == nil {
		f.measured[k] = nil
		return
	}
	f.measured[k] = append([]string(nil), v...)
}

// emit records one action for the command and, for the lines the command sends, for inject.json.
func (in *Injector) emit(f *injectFault, a InjectAction, at time.Time) {
	if a.Kind != ActWait {
		f.lines = append(f.lines, InjectLine{T: at.Format(injectTimeLayout), Kind: injectLineKind(a.Kind), Text: injectActionText(a)})
		in.pending = append(in.pending, pendingInject{act: a, fault: f, at: at})
	}
	in.out = append(in.out, a)
}

func (in *Injector) emitGM(f *injectFault, line string, at time.Time) {
	in.emit(f, InjectAction{Kind: ActGM, Line: line}, at)
}

func (in *Injector) emitSelect(f *injectFault, g packet.GUID, at time.Time) {
	in.emit(f, InjectAction{Kind: ActSelect, GUID: g}, at)
}

func (in *Injector) emitSelectSelf(f *injectFault, unit string, release bool, at time.Time) {
	in.emit(f, InjectAction{Kind: ActSelectSelf, Unit: unit, Release: release}, at)
}

func (in *Injector) emitOrder(f *injectFault, verb string, args []string, at time.Time) {
	in.emit(f, InjectAction{Kind: ActOrder, Verb: verb, Args: append([]string(nil), args...)}, at)
}

func injectLineKind(k ActionKind) string {
	switch k {
	case ActSelect, ActSelectSelf:
		return "select"
	case ActOrder:
		return "order"
	default:
		return "gm"
	}
}

func injectActionText(a InjectAction) string {
	switch a.Kind {
	case ActSelect:
		return "select " + a.GUID.String()
	case ActSelectSelf:
		return "select self"
	case ActOrder:
		return a.Verb + " " + strings.Join(a.Args, " ")
	default:
		return a.Line
	}
}

func sameInjectAction(a, b InjectAction) bool {
	if a.Kind != b.Kind || a.Line != b.Line || a.GUID != b.GUID || a.Verb != b.Verb || a.Unit != b.Unit || a.Release != b.Release || len(a.Args) != len(b.Args) {
		return false
	}
	for i := range a.Args {
		if a.Args[i] != b.Args[i] {
			return false
		}
	}
	return true
}

// Ack takes the command's result of one action (spec 1.5). A failed action ends its fault inconclusive, never the
// walk; a refused allow-list line is the command's own programming error and is checked before it gets here. A
// cleanup action carries no verdict: a failed release is left for the next Cleanup call.
func (in *Injector) Ack(a InjectAction, ctx InjectCtx, err error) {
	idx := -1
	for i, p := range in.pending {
		if sameInjectAction(p.act, a) {
			idx = i
			break
		}
	}
	if idx < 0 {
		return
	}
	p := in.pending[idx]
	in.pending = append(in.pending[:idx], in.pending[idx+1:]...)
	if a.Kind == ActSelect || a.Kind == ActSelectSelf {
		if err != nil {
			in.selfSelected, in.selectedHire = false, ""
		} else {
			in.selfSelected, in.selectedHire = ctx.SelfSelected, ctx.SelectedHire
		}
	}
	if p.cleanup {
		in.ackCleanup(p.fault, a, err)
		return
	}
	f := p.fault
	if f == nil || f.state != injectActive {
		return
	}
	if err != nil {
		in.endFault(f, VerdictInconclusive, err.Error(), p.at)
		return
	}
	switch f.spec.Kind {
	case InjectKillMT:
		in.ackKill(f, a, ctx, p.at)
	case InjectRunaway:
		in.ackRunaway(f, a, ctx, p.at)
	case InjectAddPack:
		if a.Kind == ActOrder && f.phase == "order" {
			f.phase = "engaging"
		}
	case InjectLag:
		in.ackLag(f, a, ctx, p.at)
	}
}

func (in *Injector) ackKill(f *injectFault, a InjectAction, ctx InjectCtx, at time.Time) {
	switch f.phase {
	case "select_mt":
		if a.Kind != ActSelect {
			return
		}
		// The MT is a hire, not the commander: a realizable read-back has SelectedHire == MT and SelfSelected
		// false, and never both (SelfSelected is the ActSelectSelf read-back of the commander's own GUID).
		if ctx.SelfSelected || ctx.SelectedHire != in.names.MT {
			in.endFault(f, VerdictInconclusive, "selection not confirmed", at)
			return
		}
		f.phase = "die_ready"
	}
}

func (in *Injector) ackRunaway(f *injectFault, a InjectAction, ctx InjectCtx, at time.Time) {
	switch f.phase {
	case "select_freeze":
		if a.Kind != ActSelectSelf {
			return
		}
		if !ctx.SelfSelected {
			in.endFault(f, VerdictInconclusive, "selection not confirmed", at)
			return
		}
		f.phase = "freeze_ready"
	case "freezing":
		if a.Kind != ActGM {
			return
		}
		// The freeze time is when the command sent it: the read-back follows within the same watch tick.
		f.t0 = at
		f.frozen = true
		f.phase = "frozen"
		f.lastTick = at
		in.placedAt(f)
	case "select_unfreeze":
		if a.Kind != ActSelectSelf {
			return
		}
		if !ctx.SelfSelected {
			in.endFault(f, VerdictInconclusive, "selection not confirmed", at)
			return
		}
		f.phase = "unfreeze_ready"
	case "unfreezing":
		if a.Kind != ActGM {
			return
		}
		f.frozen, f.unfrozen = false, true
		in.runawayFinalize(f, at)
	}
}

func (in *Injector) placedAt(f *injectFault) {
	if !in.haveObs {
		return
	}
	v, vok := in.lastObs.Bots[f.victim]
	lead, lok := in.lastObs.Bots[in.lastObs.Lead]
	if vok && lok {
		setNum(f, "placed_yd", dist3(v.Pos, lead.Pos))
	}
}

func (in *Injector) ackLag(f *injectFault, a InjectAction, ctx InjectCtx, at time.Time) {
	switch f.phase {
	case "select_freeze":
		if a.Kind != ActSelectSelf {
			return
		}
		if !ctx.SelfSelected {
			in.endFault(f, VerdictInconclusive, "selection not confirmed", at)
			return
		}
		f.phase = "freeze_ready"
	case "freezing":
		if a.Kind != ActGM {
			return
		}
		f.t0 = at
		f.frozen = true
		f.phase = "frozen"
		f.lastTick = at
	case "select_unfreeze":
		if a.Kind != ActSelectSelf {
			return
		}
		if !ctx.SelfSelected {
			in.endFault(f, VerdictInconclusive, "selection not confirmed", at)
			return
		}
		f.phase = "unfreeze_ready"
	case "unfreezing":
		if a.Kind != ActGM {
			return
		}
		f.frozen, f.unfrozen = false, true
		f.phase = "waiting_rejoin"
	}
}

// endFault sets the verdict, ends the active fault and starts the 60 s gap (spec 1.2).
func (in *Injector) endFault(f *injectFault, verdict, reason string, at time.Time) {
	if f.state != injectActive {
		return
	}
	f.state = injectDone
	f.verdict = verdict
	f.reason = reason
	if in.active == f {
		in.active = nil
	}
	if at.IsZero() {
		at = in.lastTick
	}
	in.lastVerdict = at
}

// Finish is the watch's end (spec 1.2): unfired faults are not-fired with the last unmet condition; an active fault
// gets the verdict its rules allow (a wipe fails it) or is inconclusive.
func (in *Injector) Finish(o InjectObs) {
	if in.finished {
		return
	}
	in.observe(o)
	for _, f := range in.faults {
		switch f.state {
		case injectDone:
			continue
		case injectPending:
			f.state = injectDone
			f.verdict = VerdictNotFired
			reason := in.globalBlock(o)
			if reason == "" {
				reason = in.eligible(f, o)
			}
			if reason == "" {
				reason = f.lastReason
			}
			if reason == "" {
				reason = "the watch ended before the fault could fire"
			}
			f.reason = reason
			continue
		}
		in.finishActive(f, o)
	}
	in.active = nil
	in.finished = true
}

func (in *Injector) finishActive(f *injectFault, o InjectObs) {
	if in.wipe {
		in.wipeOutcome(f, o, o.Now)
		return
	}
	switch f.spec.Kind {
	case InjectRunaway:
		switch {
		case f.straggler != "" && !f.f4Seen:
			in.endFault(f, VerdictFail, "summoned by the straggler ladder, not F4", o.Now)
		case f.f4Seen && f.back && f.backS <= f.f4S+15:
			in.endFault(f, VerdictPass, "", o.Now)
		case f.f4Seen && f.back:
			in.endFault(f, VerdictFail, "the victim was back too late after the runaway summon", o.Now)
		default:
			in.endFault(f, VerdictInconclusive, "the run ended before the fault resolved", o.Now)
		}
	case InjectLag:
		if f.rejoined && f.rejoinS <= 120 && !f.pauseOpen {
			in.endFault(f, VerdictPass, "", o.Now)
			return
		}
		if !f.t1.IsZero() && !f.rejoined && o.Now.Sub(f.t1) > 120*time.Second {
			in.endFault(f, VerdictFail, "the victim did not rejoin within 120 s", o.Now)
			return
		}
		in.endFault(f, VerdictInconclusive, "the run ended before the fault resolved", o.Now)
	default:
		in.endFault(f, VerdictInconclusive, "the run ended before the fault resolved", o.Now)
	}
}

// Cleanup is the watch's end (spec 1.2): select self, `.unfreeze` every bot still frozen, then select self once.
// It only asks: a bot is released when the command acknowledges its `.unfreeze` through Ack, so a failed or
// unattempted release leaves the bot frozen and the next Cleanup call asks for it again. The command acks every
// action it is handed, with an error for the ones it did not send.
func (in *Injector) Cleanup() []InjectAction {
	at := in.lastTick
	var out []InjectAction
	ask := func(f *injectFault, a InjectAction) {
		if f != nil {
			f.lines = append(f.lines, InjectLine{T: at.Format(injectTimeLayout), Kind: injectLineKind(a.Kind), Text: injectActionText(a)})
		}
		in.pending = append(in.pending, pendingInject{act: a, fault: f, at: at, cleanup: true})
		out = append(out, a)
	}
	for _, f := range in.faults {
		if !f.frozen || f.unfrozen || in.cleanupInFlight(f) {
			continue
		}
		ask(f, InjectAction{Kind: ActSelectSelf, Unit: f.victim, Release: true})
		ask(f, InjectAction{Kind: ActGM, Line: ".unfreeze " + f.victim})
	}
	if len(out) > 0 {
		ask(nil, InjectAction{Kind: ActSelectSelf})
	}
	return out
}

// FrozenVictims is every bot the injector froze and has not released, in fault order: the cleanup's remaining work.
func (in *Injector) FrozenVictims() []string {
	var out []string
	for _, f := range in.faults {
		if f.frozen && !f.unfrozen {
			out = append(out, f.victim)
		}
	}
	return out
}

// DropCleanup forgets every pending cleanup action: a cleanup attempt that was cut off (its context ended, or its
// selection failed) must not keep the next attempt from asking again.
func (in *Injector) DropCleanup() {
	kept := in.pending[:0]
	for _, p := range in.pending {
		if !p.cleanup {
			kept = append(kept, p)
		}
	}
	in.pending = kept
}

// cleanupInFlight says whether a cleanup action of this fault waits for its acknowledgement.
func (in *Injector) cleanupInFlight(f *injectFault) bool {
	for _, p := range in.pending {
		if p.cleanup && p.fault == f {
			return true
		}
	}
	return false
}

// dropCleanup forgets a fault's unacknowledged cleanup actions: once the selection failed, the `.unfreeze` that
// follows it is not allowed and is asked for again with the selection.
func (in *Injector) dropCleanup(f *injectFault) {
	kept := in.pending[:0]
	for _, p := range in.pending {
		if p.cleanup && p.fault == f {
			continue
		}
		kept = append(kept, p)
	}
	in.pending = kept
}

// ackCleanup folds one cleanup action: only a `.unfreeze` the command sent releases its bot; the selection state
// comes from the select's read-back in Ack. The trailing select belongs to no fault.
func (in *Injector) ackCleanup(f *injectFault, a InjectAction, err error) {
	if f == nil {
		return
	}
	if err != nil {
		in.dropCleanup(f)
		return
	}
	if a.Kind == ActGM {
		f.frozen, f.unfrozen = false, true
	}
}

// Results is the faults' final state in command-line order.
func (in *Injector) Results() []InjectResult {
	out := make([]InjectResult, 0, len(in.faults))
	for _, f := range in.faults {
		r := InjectResult{
			Kind:     f.spec.Kind,
			Spec:     f.spec.Raw,
			Verdict:  f.verdict,
			Reason:   f.reason,
			Step:     f.step,
			Leg:      f.leg,
			Victim:   f.victim,
			Lines:    append([]InjectLine(nil), f.lines...),
			Measured: map[string]any{},
			Notes:    append([]string(nil), f.notes...),
		}
		for k, v := range f.measured {
			r.Measured[k] = v
		}
		if !f.firedAt.IsZero() {
			t := f.firedAt
			r.FiredAt = &t
		}
		if f.spec.Kind == InjectAddPack && f.candidate != 0 {
			r.Target = &InjectTarget{GUID: f.candidate.String(), Entry: f.candEntry}
		}
		out = append(out, r)
	}
	return out
}

// InjectedDeaths is kill-mt's main-tank death, which the summary counts as injected, not as a run death (spec 1.5).
func (in *Injector) InjectedDeaths() int {
	n := 0
	for _, f := range in.faults {
		if f.spec.Kind == InjectKillMT && f.killed {
			n++
		}
	}
	return n
}

// injectF4Re is the run leader's runaway note (spec 0.1, DungeonRunPolicy.h:1906) with the victim's name.
func injectF4Re(victim string) *regexp.Regexp {
	return regexp.MustCompile("^" + regexp.QuoteMeta(victim) + ` is (?:on another map|[0-9]+\.[0-9] yd from the lead bot) \([0-9]+ s out of reach\): summoned$`)
}

var injectPhaseOrder = map[string]int{"": 0, "begin": 1, "gather": 2, "walk": 3, "pull": 4, "task": 5, "rest": 6, "wait": 7}

func injectPhaseRank(p string) int { return injectPhaseOrder[p] }

func num32f(v float32) string { return strconv.FormatFloat(float64(v), 'f', -1, 32) }
