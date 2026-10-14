package dungeon

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"slices"
	"sort"
	"strings"
	"time"
)

// Live tuning (README "Live tuning"): the thresholds of the remote driver as named, bounded tunables that a tunables file (hot reloaded)
// or the `tune_set` RPC can change during a run, at a leg start or a pull-group start and never in the middle of a step. Every change is
// an event (`tune_change`) and a line of the report, so a fix made on the spot can become a PR afterwards (`turtle-agent tune-diff`).
//
// Rule 3 of the driver rules: no tunable can make a wait unbounded. Each has a hard Min and Max below; a value outside is refused, a file
// with one bad value is refused whole (the old values stay), and the watchdog's levels are not tunables.

// tuneExtra holds the thresholds that were constants and so have no field of Config or RemoteOptions. A RemoteDriver owns a copy; the
// code that used the constant reads the copy (the constants stay as the defaults).
type tuneExtra struct {
	LookStandoff   float32       // yards short of a pack's position the lead bot stops to look for it (confirmGone)
	LookArrive     float32       // how near its stand point the lead bot must get for the look
	AttackHoldYd   float32       // how near the mob the tank must be for its threat to count as holding it
	TankBusyRounds int           // waits of one pull for a tank that stays in combat
	AddOrders      int           // tank orders at one loose mob
	FallbackTries  int           // tries of the carrier's order before the agent takes an object step over
	GatherNear     float32       // careful walk: every living bot this near the lead bot counts as gathered
	GatherProgress float32       // careful walk: how much nearer a bot must have come during a gather's wait to be waited for again
	AggroGuard     float32       // careful walk: a hostile this near the next hop is pulled before it
	AheadMaxDZ     float32       // careful walk: how far above or below the hop a hostile may stand to be pulled
	MaxAheadPulls  int           // careful walk: pulls of hostiles near the way in one walk
	GatherClear    float32       // careful walk: a hop's end (a gather spot) stays this far from a hostile no step of this leg owns
	BigPack        int           // a pack of this many mobs gets one member crowd-controlled before the pull (remote_cc.go)
	PatrolAggro    float32       // a patrol member this near a pull (its target or the lead bot) is a danger to it (remote_patrol.go)
	PatrolWindow   time.Duration // ... within this long, along its path at its pace
	PatrolWait     time.Duration // the longest wait of one pull group for its patrol to walk away
	PatrolPullYd   float32       // a patrol this near the lead bot, and patrol_aggro clear of the pack, is pulled alone first
	MinMove        float32       // the shortest move the module carries out (a shorter one is sent as a step past)
	PartyDownDead  int           // dead bots that stop the leg
	RespawnSettle  time.Duration // pulltest: wait after `.respawn` for the pack to show
	RejoinTPDist   float32       // a living bot this far from the lead bot (or on another map) is summoned to it by the commander (0: off; remote_rejoin.go)
	RejoinGap      time.Duration // least time between two summons of the same bot
	RezReach       float32       // a dead bot is left to a living caster only when its body lies this near the caster (else it is released at once)
}

func defaultTuneExtra() tuneExtra {
	return tuneExtra{LookStandoff: LookStandoff, LookArrive: lookArrive, AttackHoldYd: attackHoldYd, TankBusyRounds: maxTankBusyRounds, AddOrders: maxAddOrders,
		FallbackTries: objectFallbackTries, GatherNear: GatherNear, GatherProgress: gatherProgress, AggroGuard: AggroGuard, AheadMaxDZ: aheadMaxDZ,
		MaxAheadPulls: maxAheadPulls, GatherClear: GatherClear, BigPack: BigPack, PatrolAggro: PatrolAggro, PatrolWindow: patrolWindow, PatrolWait: patrolWait, PatrolPullYd: patrolPullYd, MinMove: MinMove, PartyDownDead: partyDownDead, RespawnSettle: respawnSettle, RejoinTPDist: rejoinTPDist, RejoinGap: rejoinGap, RezReach: rezReach}
}

// tuneTarget is what a tunable reads and writes.
type tuneTarget struct {
	cfg *Config
	opt *RemoteOptions
	x   *tuneExtra
}

// tuneSpec is one tunable: its key in the file, the unit and hard limits of its value, and how it reads and writes the driver.
type tuneSpec struct {
	Key  string
	Unit string // "yd", "s", "pct" or "count"
	Min  float64
	Max  float64
	Doc  string
	get  func(*tuneTarget) float64
	set  func(*tuneTarget, float64)
}

func (s tuneSpec) whole() bool { return s.Unit == "pct" || s.Unit == "count" }

func yds(key string, min, max float64, doc string, p func(*tuneTarget) *float32) tuneSpec {
	return tuneSpec{Key: key, Unit: "yd", Min: min, Max: max, Doc: doc,
		get: func(t *tuneTarget) float64 { return float64(*p(t)) },
		set: func(t *tuneTarget, v float64) { *p(t) = float32(v) }}
}

func secs(key string, min, max float64, doc string, p func(*tuneTarget) *time.Duration) tuneSpec {
	return tuneSpec{Key: key, Unit: "s", Min: min, Max: max, Doc: doc,
		get: func(t *tuneTarget) float64 { return p(t).Seconds() },
		set: func(t *tuneTarget, v float64) { *p(t) = time.Duration(math.Round(v*1000)) * time.Millisecond }}
}

func ints(key, unit string, min, max float64, doc string, p func(*tuneTarget) *int) tuneSpec {
	return tuneSpec{Key: key, Unit: unit, Min: min, Max: max, Doc: doc,
		get: func(t *tuneTarget) float64 { return float64(*p(t)) },
		set: func(t *tuneTarget, v float64) { *p(t) = int(v) }}
}

// tuneSpecs is the list of tunables, in the order of the README table.
var tuneSpecs = []tuneSpec{
	// Walks.
	yds("move_leg", 10, 150, "longest single move of a walk (the server takes 150)", func(t *tuneTarget) *float32 { return &t.opt.MoveLeg }),
	yds("move_arrive", 1, 15, "how near a point the lead bot has arrived", func(t *tuneTarget) *float32 { return &t.opt.MoveArrive }),
	yds("look_ahead", 2, 40, "how near its waypoint the lead bot must be for the next move to go out", func(t *tuneTarget) *float32 { return &t.opt.LookAhead }),
	secs("look_ahead_eta_s", 0.5, 10, "the look-ahead by time", func(t *tuneTarget) *time.Duration { return &t.opt.LookAheadETA }),
	secs("move_gap_s", 0.2, 10, "least time between two move orders", func(t *tuneTarget) *time.Duration { return &t.opt.MoveGap }),
	secs("move_stall_s", 3, 60, "a lead bot that gained no ground this long gets the move again", func(t *tuneTarget) *time.Duration { return &t.opt.MoveStall }),
	ints("move_retries", "count", 1, 10, "stalls and refusals of one walk before it fails", func(t *tuneTarget) *int { return &t.opt.MoveRetries }),
	ints("walk_attempts", "count", 1, 8, "refusals of one walk (on top of move_retries) before it fails", func(t *tuneTarget) *int { return &t.cfg.WalkAttempts }),
	secs("lead_wait_s", 5, 120, "how long an order waits out lead-unavailable", func(t *tuneTarget) *time.Duration { return &t.opt.LeadWait }),
	// Pulls and fights.
	yds("pull_range", 10, 80, "a target farther than this from the lead bot is approached first", func(t *tuneTarget) *float32 { return &t.cfg.PullRange }),
	yds("pull_standoff", 5, 70, "how far from the target the lead bot stops when it approaches (at most pull_range)", func(t *tuneTarget) *float32 { return &t.cfg.PullStandoff }),
	yds("pack_radius", 5, 60, "how far from a pull position a pack member may stand (a group's radius wins)", func(t *tuneTarget) *float32 { return &t.cfg.PackRadius }),
	ints("pull_attempts", "count", 1, 6, "failed pulls of one group before the run fails", func(t *tuneTarget) *int { return &t.cfg.PullAttempts }),
	secs("engage_timeout_s", 5, 90, "a pull that draws no fight in this long has failed", func(t *tuneTarget) *time.Duration { return &t.cfg.EngageTimeout }),
	secs("evade_grace_s", 3, 60, "a fight with nobody in combat this long is over", func(t *tuneTarget) *time.Duration { return &t.cfg.EvadeGrace }),
	secs("assist_every_s", 3, 60, "how often the party is re-ordered onto the nearest mob", func(t *tuneTarget) *time.Duration { return &t.cfg.AssistEvery }),
	secs("aggro_quiet_s", 1, 15, "a fight on a walk is over when nothing has hit the party this long", func(t *tuneTarget) *time.Duration { return &t.cfg.AggroQuiet }),
	secs("post_fight_s", 1, 30, "wait after the last pack member died", func(t *tuneTarget) *time.Duration { return &t.cfg.PostFight }),
	secs("adds_wait_s", 1, 90, "longest wait for loose hostiles after a fight", func(t *tuneTarget) *time.Duration { return &t.cfg.AddsWait }),
	secs("fight_timeout_s", 30, 900, "one pull group", func(t *tuneTarget) *time.Duration { return &t.cfg.FightTimeout }),
	secs("boss_timeout_s", 60, 1800, "one boss fight", func(t *tuneTarget) *time.Duration { return &t.cfg.BossTimeout }),
	yds("look_standoff", 5, 24, "how far short of a pack's position the lead bot stops to look for it (under the 25 yd of the clear evidence)", func(t *tuneTarget) *float32 { return &t.x.LookStandoff }),
	yds("attack_hold_yd", 10, 80, "how near the mob the tank must be for its threat to count as holding it", func(t *tuneTarget) *float32 { return &t.x.AttackHoldYd }),
	ints("tank_busy_rounds", "count", 1, 6, "waits of one pull for a tank that stays in combat", func(t *tuneTarget) *int { return &t.x.TankBusyRounds }),
	ints("add_orders", "count", 1, 6, "tank orders at one loose mob", func(t *tuneTarget) *int { return &t.x.AddOrders }),
	ints("fallback_tries", "count", 1, 4, "tries of the carrier's order before the agent takes an object step over", func(t *tuneTarget) *int { return &t.x.FallbackTries }),
	secs("threat_wait_s", 1, 30, "longest wait for the tank to hold a mob's threat", func(t *tuneTarget) *time.Duration { return &t.opt.ThreatWait }),
	secs("opener_delay_s", 0, 15, "how long the tank must have held a mob before the party is sent at it", func(t *tuneTarget) *time.Duration { return &t.opt.OpenerDelay }),
	secs("add_cooldown_s", 2, 60, "least time between two tank orders at the same loose add", func(t *tuneTarget) *time.Duration { return &t.opt.AddCooldown }),
	secs("tank_busy_wait_s", 2, 60, "one wait of a pull the module refused as tank-busy", func(t *tuneTarget) *time.Duration { return &t.opt.TankBusyWait }),
	secs("rez_wait_s", 5, 120, "how long a dead bot waits for a resurrection before it is released", func(t *tuneTarget) *time.Duration { return &t.opt.RezWait }),
	// Rest gates.
	ints("rest_health_pct", "pct", 30, 100, "rested: every member's health at least this", func(t *tuneTarget) *int { return &t.cfg.RestHealthPct }),
	ints("rest_mana_pct", "pct", 10, 100, "rested: every other mana user's mana at least this", func(t *tuneTarget) *int { return &t.cfg.RestManaPct }),
	ints("rest_healer_mana_pct", "pct", 10, 100, "rested: every healer's mana at least this", func(t *tuneTarget) *int { return &t.cfg.RestHealerManaPct }),
	secs("rest_timeout_s", 5, 300, "longest rest gate", func(t *tuneTarget) *time.Duration { return &t.cfg.RestTimeout }),
	ints("rest_min_health_pct", "pct", 20, 100, "after the timeout the run goes on only with this health (at most rest_health_pct)", func(t *tuneTarget) *int { return &t.cfg.RestMinHealth }),
	yds("rest_walk_yd", 10, 500, "the walk length from which the start gate lets the party rest while it walks", func(t *tuneTarget) *float32 { return &t.cfg.RestWalkYd }),
	ints("rest_walk_health_pct", "pct", 10, 100, "health floor for resting on a walk", func(t *tuneTarget) *int { return &t.cfg.RestWalkHealthPct }),
	ints("rest_walk_healer_mana_pct", "pct", 5, 100, "healer mana floor for resting on a walk", func(t *tuneTarget) *int { return &t.cfg.RestWalkHealerManaPct }),
	ints("rest_walk_mana_pct", "pct", 5, 100, "mana floor for resting on a walk", func(t *tuneTarget) *int { return &t.cfg.RestWalkManaPct }),
	yds("gather_radius", 8, 80, "the rest gate before a pull also wants every living bot this near the lead bot", func(t *tuneTarget) *float32 { return &t.cfg.GatherRadius }),
	// The careful walk (remote_gather.go: gather before a hop or a pull, pull what stands near the way) and the look at a pack.
	yds("careful_hop", 5, 60, "the longest single move of a walk inside the dungeon", func(t *tuneTarget) *float32 { return &t.opt.CarefulHop }),
	secs("gather_wait_s", 1, 60, "how long the party is waited for to gather before a hop or a pull", func(t *tuneTarget) *time.Duration { return &t.opt.GatherWait }),
	yds("gather_near", 4, 40, "every living bot within this of the lead bot counts as gathered", func(t *tuneTarget) *float32 { return &t.x.GatherNear }),
	yds("gather_progress", 0.5, 10, "how much nearer a bot must have come during a gather's whole wait to be waited for again", func(t *tuneTarget) *float32 { return &t.x.GatherProgress }),
	yds("aggro_guard", 8, 40, "a hostile this near the next hop is pulled before the hop", func(t *tuneTarget) *float32 { return &t.x.AggroGuard }),
	yds("ahead_max_dz", 2, 30, "how far above or below the hop a hostile may stand to be pulled before it", func(t *tuneTarget) *float32 { return &t.x.AheadMaxDZ }),
	yds("gather_clear", 10, 60, "a hop's end (where the party gathers) stays this far from a hostile no step of this leg owns", func(t *tuneTarget) *float32 { return &t.x.GatherClear }),
	ints("big_pack", "count", 3, 20, "a pack of this many mobs gets one member crowd-controlled by the cc bot before the pull", func(t *tuneTarget) *int { return &t.x.BigPack }),
	yds("patrol_aggro", 10, 50, "a patrol member this near a pull (its target or the lead bot) is a danger to it", func(t *tuneTarget) *float32 { return &t.x.PatrolAggro }),
	secs("patrol_window_s", 5, 180, "a patrol is a danger to a pull when its path brings it within patrol_aggro this soon", func(t *tuneTarget) *time.Duration { return &t.x.PatrolWindow }),
	secs("patrol_wait_s", 0, 180, "the longest wait of one pull group for its patrol to walk away", func(t *tuneTarget) *time.Duration { return &t.x.PatrolWait }),
	yds("patrol_pull_yd", 10, 60, "a patrol this near the lead bot, and patrol_aggro clear of the pack, is pulled alone first", func(t *tuneTarget) *float32 { return &t.x.PatrolPullYd }),
	yds("min_move", 0, 10, "the shortest move the module carries out; a shorter last stretch is sent as a step to min_move+1 yd along the line", func(t *tuneTarget) *float32 { return &t.x.MinMove }),
	ints("max_ahead_pulls", "count", 1, 30, "pulls of hostiles near the way, at most, in one walk", func(t *tuneTarget) *int { return &t.x.MaxAheadPulls }),
	yds("look_arrive", 1, 10, "how near its stand point the lead bot must get to look at a pack", func(t *tuneTarget) *float32 { return &t.x.LookArrive }),
	ints("party_down_dead", "count", 1, 5, "dead bots that stop the leg", func(t *tuneTarget) *int { return &t.x.PartyDownDead }),
	yds("rez_reach_yd", 10, 100, "a dead bot is left to a living caster's resurrection only when its body lies this near the caster; a farther one is released at once", func(t *tuneTarget) *float32 { return &t.x.RezReach }),
	// Rejoin (remote_rejoin.go).
	yds("rejoin_tp_dist", 0, 500, "a living bot farther than this from the lead bot, or on another map, is brought to it by the commander (cmd summon, GM account); 0 turns it off", func(t *tuneTarget) *float32 { return &t.x.RejoinTPDist }),
	secs("rejoin_tp_gap_s", 10, 600, "least time between two summons of the same bot", func(t *tuneTarget) *time.Duration { return &t.x.RejoinGap }),
	// The pull test (pulltest.go).
	secs("respawn_settle_s", 1, 30, "pulltest: wait after .respawn for the pack to show", func(t *tuneTarget) *time.Duration { return &t.x.RespawnSettle }),
}

var tuneIndex = func() map[string]*tuneSpec {
	m := make(map[string]*tuneSpec, len(tuneSpecs))
	for i := range tuneSpecs {
		m[tuneSpecs[i].Key] = &tuneSpecs[i]
	}
	return m
}()

// TuneKeys lists the tunable keys in table order.
func TuneKeys() []string {
	out := make([]string, len(tuneSpecs))
	for i, s := range tuneSpecs {
		out[i] = s.Key
	}
	return out
}

// TuneError is a refused tunable value or file; the RPC answers it as invalid params.
type TuneError struct{ Msg string }

func (e *TuneError) Error() string { return e.Msg }

func tuneErr(format string, a ...any) error { return &TuneError{Msg: fmt.Sprintf(format, a...)} }

// check validates one value against the hard limits of its tunable.
func (s tuneSpec) check(v float64) error {
	switch {
	case math.IsNaN(v) || math.IsInf(v, 0):
		return fmt.Errorf("%s: %v is not a number", s.Key, v)
	case v < s.Min || v > s.Max:
		return fmt.Errorf("%s: %v %s is outside the limits %v to %v", s.Key, v, s.Unit, s.Min, s.Max)
	case s.whole() && v != math.Trunc(v):
		return fmt.Errorf("%s: %v is not a whole number", s.Key, v)
	}
	return nil
}

// checkTuneSet checks the limits that tie values together (a standoff within the pull range, say) on a full set of values.
func checkTuneSet(v map[string]float64) error {
	var errs []error
	if v["pull_standoff"] > v["pull_range"] {
		errs = append(errs, fmt.Errorf("pull_standoff %v is more than pull_range %v", v["pull_standoff"], v["pull_range"]))
	}
	if v["rest_min_health_pct"] > v["rest_health_pct"] {
		errs = append(errs, fmt.Errorf("rest_min_health_pct %v is more than rest_health_pct %v", v["rest_min_health_pct"], v["rest_health_pct"]))
	}
	if v["move_arrive"] > v["move_leg"] || v["look_ahead"] > v["move_leg"] {
		errs = append(errs, fmt.Errorf("move_arrive %v and look_ahead %v must not exceed move_leg %v", v["move_arrive"], v["look_ahead"], v["move_leg"]))
	}
	return errors.Join(errs...)
}

// readTarget reads every tunable of t.
func readTarget(t *tuneTarget) map[string]float64 {
	m := make(map[string]float64, len(tuneSpecs))
	for _, s := range tuneSpecs {
		m[s.Key] = s.get(t)
	}
	return m
}

// TuneDefaults are the tunables' default values: DefaultConfig, DefaultRemoteOptions and the former constants.
func TuneDefaults() map[string]float64 {
	cfg, opt, x := DefaultConfig().withDefaults(), DefaultRemoteOptions().withDefaults(), defaultTuneExtra()
	return readTarget(&tuneTarget{cfg: &cfg, opt: &opt, x: &x})
}

// ValidateTuneFile checks the contents of a tunables file as the run would: it must parse, keep every value inside its limits and
// keep the rules between values, against the defaults. dungeon_run uses it on a file it is named, so that a typo is an error at the
// call and not a note in the middle of a run.
func ValidateTuneFile(data []byte) error {
	vals, err := ParseTuneFile(data)
	if err != nil {
		return err
	}
	return checkTuneSet(layered(TuneDefaults(), vals, nil))
}

// ParseTuneFile reads a tunables file: one JSON object of key to number. Keys that start with "_" are comments. An unknown key, a
// value that is not a number or one outside its limits refuses the whole file, with every problem listed.
func ParseTuneFile(data []byte) (map[string]float64, error) {
	var raw map[string]json.RawMessage
	dec := json.NewDecoder(bytes.NewReader(data))
	if err := dec.Decode(&raw); err != nil {
		return nil, fmt.Errorf("not a JSON object: %w", err)
	}
	if dec.More() {
		return nil, errors.New("not a JSON object: trailing data")
	}
	keys := make([]string, 0, len(raw))
	for k := range raw {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	out := map[string]float64{}
	var errs []error
	for _, k := range keys {
		if strings.HasPrefix(k, "_") {
			continue
		}
		s, ok := tuneIndex[k]
		if !ok {
			errs = append(errs, fmt.Errorf("unknown key %q", k))
			continue
		}
		var v float64
		if err := json.Unmarshal(raw[k], &v); err != nil {
			errs = append(errs, fmt.Errorf("%s: %s is not a number", k, strings.TrimSpace(string(raw[k]))))
			continue
		}
		if err := s.check(v); err != nil {
			errs = append(errs, err)
			continue
		}
		out[k] = v
	}
	if len(errs) > 0 {
		return nil, errors.Join(errs...)
	}
	return out, nil
}

// TuneChange is one tunable changed during a run (the report's tune_changes and the trace's tune_change event).
type TuneChange struct {
	Key      string    `json:"key"`
	Old      float64   `json:"old"`
	New      float64   `json:"new"`
	Source   string    `json:"source"` // "file", "rpc", or "reset" (the file lost the key, or an rpc put the default back)
	At       time.Time `json:"at"`
	Leg      int       `json:"leg"`
	Boundary string    `json:"boundary"` // "leg_start" or "pull_group_start"
}

// PatchOp is one operation of a JSON patch (RFC 6902); Old is not part of it: it says what the value was.
type PatchOp struct {
	Op    string `json:"op"`
	Path  string `json:"path"`
	Value any    `json:"value,omitempty"`
	Old   any    `json:"old,omitempty"`
}

// PlanReload is one reload of the plan file that changed something, or was refused (the report's plan_reloads and the trace's
// plan_reload event).
type PlanReload struct {
	OK       bool      `json:"ok"`
	At       time.Time `json:"at"`
	Leg      int       `json:"leg"`
	Boundary string    `json:"boundary"`
	File     string    `json:"file,omitempty"`
	Error    string    `json:"error,omitempty"`
	Ops      []PatchOp `json:"ops,omitempty"`
	// Ignored are the paths of the file that differ from the run's plan and were not taken: legs done, groups already cleared.
	Ignored []string `json:"ignored,omitempty"`
}

func jsonPtr(parts ...string) string {
	var b strings.Builder
	for _, p := range parts {
		b.WriteByte('/')
		b.WriteString(strings.NewReplacer("~", "~0", "/", "~1").Replace(p))
	}
	return b.String()
}

// jsonDiff appends the operations that turn a into b (both decoded JSON values) to ops. Arrays are compared by index; a longer array
// gets adds at its end, a shorter one removes from its end backwards, so that the operations apply in order.
func jsonDiff(path []string, a, b any, ops *[]PatchOp) {
	switch av := a.(type) {
	case map[string]any:
		bv, ok := b.(map[string]any)
		if !ok {
			break
		}
		keys := make([]string, 0, len(av)+len(bv))
		for k := range av {
			keys = append(keys, k)
		}
		for k := range bv {
			if _, in := av[k]; !in {
				keys = append(keys, k)
			}
		}
		sort.Strings(keys)
		for _, k := range keys {
			p := append(slices.Clone(path), k)
			x, inA := av[k]
			y, inB := bv[k]
			switch {
			case inA && !inB:
				*ops = append(*ops, PatchOp{Op: "remove", Path: jsonPtr(p...), Old: x})
			case !inA && inB:
				*ops = append(*ops, PatchOp{Op: "add", Path: jsonPtr(p...), Value: y})
			default:
				jsonDiff(p, x, y, ops)
			}
		}
		return
	case []any:
		bv, ok := b.([]any)
		if !ok {
			break
		}
		for i := 0; i < len(av) && i < len(bv); i++ {
			jsonDiff(append(slices.Clone(path), fmt.Sprint(i)), av[i], bv[i], ops)
		}
		for i := len(av); i < len(bv); i++ {
			*ops = append(*ops, PatchOp{Op: "add", Path: jsonPtr(append(slices.Clone(path), fmt.Sprint(i))...), Value: bv[i]})
		}
		for i := len(av) - 1; i >= len(bv); i-- {
			*ops = append(*ops, PatchOp{Op: "remove", Path: jsonPtr(append(slices.Clone(path), fmt.Sprint(i))...), Old: av[i]})
		}
		return
	}
	if !jsonEqual(a, b) {
		*ops = append(*ops, PatchOp{Op: "replace", Path: jsonPtr(path...), Value: b, Old: a})
	}
}

func jsonEqual(a, b any) bool {
	x, _ := json.Marshal(a)
	y, _ := json.Marshal(b)
	return bytes.Equal(x, y)
}

// planDiff is the JSON patch that turns plan a into plan b.
func planDiff(a, b *Plan) []PatchOp {
	var ja, jb any
	ab, _ := json.Marshal(a)
	bb, _ := json.Marshal(b)
	_ = json.Unmarshal(ab, &ja)
	_ = json.Unmarshal(bb, &jb)
	var ops []PatchOp
	jsonDiff(nil, ja, jb, &ops)
	return ops
}

// mergeReload says which parts of next the run takes over cur (the plan it runs). leg is the leg the stepper is on; started says
// that leg has begun (a pull-group start inside it: from group k on), else it has not (a leg start: all of it). Legs before it are
// done; the legs after it are whole. The result is the plan to run, built from cur and next; the paths where it still differs from
// next are the ignored ones (rule 1: the groups the leg cleared already stay as they were).
func mergeReload(cur, next *Plan, leg int, started bool, group int) (*Plan, []string, error) {
	switch {
	case next.Name != cur.Name:
		return nil, nil, fmt.Errorf("the plan is called %q, the run's is %q", next.Name, cur.Name)
	case next.Map != cur.Map:
		return nil, nil, fmt.Errorf("the plan's map is %d, the run's is %d", next.Map, cur.Map)
	case len(next.Legs) != len(cur.Legs):
		return nil, nil, fmt.Errorf("the plan has %d legs, the run's has %d: legs cannot be added or removed during a run", len(next.Legs), len(cur.Legs))
	}
	merged := *cur
	merged.Notes = next.Notes
	merged.Legs = slices.Clone(cur.Legs)
	for i := range merged.Legs {
		switch {
		case i < leg:
		case i == leg && started:
			l, n := cur.Legs[i], next.Legs[i]
			keep := min(group, len(l.PullGroups))
			l.PullGroups = append(slices.Clone(l.PullGroups[:keep]), n.PullGroups[min(keep, len(n.PullGroups)):]...)
			l.Skip, l.Steps, l.Boss, l.AfterBoss, l.Rest, l.Notes = n.Skip, n.Steps, n.Boss, n.AfterBoss, n.Rest, n.Notes
			merged.Legs[i] = l
		default:
			merged.Legs[i] = next.Legs[i]
		}
	}
	var ignored []string
	seen := map[string]bool{}
	for _, op := range planDiff(&merged, next) {
		p := op.Path
		if parts := strings.Split(p, "/"); len(parts) > 4 {
			keep := 4
			if slices.Contains([]string{"pull_groups", "steps", "after_boss", "via"}, parts[3]) {
				keep = 5 // name the element of the list too
			}
			p = strings.Join(parts[:min(keep, len(parts))], "/")
		}
		if !seen[p] {
			seen[p] = true
			ignored = append(ignored, p)
		}
	}
	return &merged, ignored, nil
}
