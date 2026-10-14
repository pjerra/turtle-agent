package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/pjerra/turtle-agent/internal/brain"
	"github.com/pjerra/turtle-agent/internal/dungeon"
	"github.com/pjerra/turtle-agent/internal/events"
	"github.com/pjerra/turtle-agent/internal/objects"
	"github.com/pjerra/turtle-agent/internal/packet"
	"github.com/pjerra/turtle-agent/internal/rpc"
	"github.com/pjerra/turtle-agent/internal/session"
	"github.com/pjerra/turtle-agent/internal/tbm"
)

// legParam is a leg of a plan given as its index or its name.
type legParam struct {
	index int
	name  string
}

func (l *legParam) UnmarshalJSON(b []byte) error {
	var n int
	if err := json.Unmarshal(b, &n); err == nil {
		l.index, l.name = n, ""
		return nil
	}
	var s string
	if err := json.Unmarshal(b, &s); err != nil || s == "" {
		return errors.New("from_leg must be a leg number (from 0) or a leg name")
	}
	l.name = s
	return nil
}

// pulltestParam is dungeon_run's pulltest: the leg (an index or a name), the pull group in it, the tries (default 3, at most 10) and how a pack
// is brought back between tries ("auto", "equivalent", "respawn" or "none").
type pulltestParam struct {
	Leg       *legParam `json:"leg"`
	PullGroup *int      `json:"pull_group"`
	Tries     int       `json:"tries,omitempty"`
	Reset     string    `json:"reset,omitempty"`
}

// legIndex resolves a leg given as an index or a name; -1 when the plan has no such leg. nil is leg 0.
func legIndex(plan *dungeon.Plan, l *legParam) int {
	if l == nil {
		return 0
	}
	if l.name == "" {
		if l.index < 0 || l.index >= len(plan.Legs) {
			return -1
		}
		return l.index
	}
	for i, leg := range plan.Legs {
		if leg.Name == l.name {
			return i
		}
	}
	return -1
}

// buffsParam is the buffs parameter: a preset name ("world"), a spell id, or a list mixing both.
type buffsParam struct {
	presets []string
	ids     []uint32
}

func (b *buffsParam) UnmarshalJSON(raw []byte) error {
	bad := errors.New(`buffs must be a preset name ("world"), a spell id, or a list of them`)
	var items []json.RawMessage
	if err := json.Unmarshal(raw, &items); err != nil {
		items = []json.RawMessage{raw}
	}
	b.presets, b.ids = nil, nil
	for _, it := range items {
		var name string
		var id uint32
		switch {
		case json.Unmarshal(it, &name) == nil && name != "":
			b.presets = append(b.presets, name)
		case json.Unmarshal(it, &id) == nil && id != 0:
			b.ids = append(b.ids, id)
		default:
			return bad
		}
	}
	return nil
}

type dungeonRunParams struct {
	Name string `json:"name"`
	// AllowDraft lets the run take a plan whose status is "draft": a skeleton from the world database that has not had its mapping walk
	// (AUTO-IMPROVE step 0), so its waypoints and pull order are unchecked. Without it such a plan is refused.
	AllowDraft bool      `json:"allow_draft,omitempty"`
	FromLeg    *legParam `json:"from_leg,omitempty"`
	// FromPullGroup starts the run inside from_leg, at that pull group (remote mode; README "Start anywhere"): the commander's own GM move
	// brings the party to the group's gather spot. StartHere keeps the old from_leg (no move: the run starts where the lead bot stands).
	// Pulltest runs one pull group as a scored scenario instead of the plan (README "Pull test").
	FromPullGroup *int `json:"from_pull_group,omitempty"`
	StartHere     bool `json:"start_here,omitempty"`
	// HaveItems are item entries the party already carries, for a start past the leg that makes them (remote mode; README "Start anywhere").
	HaveItems   []uint32       `json:"have_items,omitempty"`
	Pulltest    *pulltestParam `json:"pulltest,omitempty"`
	Teleport    bool           `json:"teleport,omitempty"`     // GM-teleport the leader to the start and summon the party (flagged in the report)
	NoShortcuts bool           `json:"no_shortcuts,omitempty"` // no GM revive of a dead companion at a rest gate
	// Setup lists the GM commands the caller ran to set the run up ("go entrance", "levelup -15"); they are copied
	// into the report's setup_shortcuts. The run sends none of them.
	Setup []string `json:"setup,omitempty"`
	// Buffs puts spells on every companion with the GM command ".aura <id>" (the leader must be a GM of developer
	// level) as the run starts and after any GM revive; each application is a "buff" entry in the report's
	// gm_shortcuts. Preset "world" is the classic world buffs.
	Buffs *buffsParam `json:"buffs,omitempty"`
	// Brain plays the leader's character during the run's fights with the agent's bot brain (README "Bot brain"):
	// a preset such as "frost_mage" or a strategy name. The run keeps leading and giving the party its orders; the
	// brain starts at a leg's first rest gate and stops at the leg's end. Off by default.
	Brain string `json:"brain,omitempty"`
	// GM says whether the leader's account is a GM. false: a player's account, the run sends no GM command (no
	// ".gm visible on", no teleport, no revive, no ".aura"; teleport and buffs are refused). true: a GM of level GMLevel
	// (developer when omitted), no probe. Omitted: the run sends ".account" once and reads the level from the reply; no
	// clear answer counts as a player (pass gm true to override). The level gates each GM command (dungeon.capsFor).
	GM *bool `json:"gm,omitempty"`
	// GMLevel is the account level a gm true leader has (1 observer: teleport; 2 moderator: also .gm visible on;
	// 3 developer and above: also revive and buffs). Only with gm true.
	GMLevel *int `json:"gm_level,omitempty"`
	// RecoverWipe lets the party, when it is all dead, release and run back to its corpses and the run go on from the
	// current step (else a wipe ends the run). Dead members at a rest gate with no GM revive are recovered the same way.
	RecoverWipe bool `json:"recover_wipe,omitempty"`
	// Resume true skips what the leader has already passed when it stands further along from_leg's route than its first point: the walk to
	// the leg's first point and the pull groups behind it that a party member stands near and sees empty. Off by default (not yet checked live).
	Resume *bool `json:"resume,omitempty"`
	// WaitSickness waits resurrection sickness out after the leader's corpse run (up to 11 min); else it is noted.
	WaitSickness bool `json:"wait_sickness,omitempty"`
	// Mode is "group" (the default: the agent's character leads the party it is in) or "remote": the agent leads a bot group
	// from outside it through the TortoiseBots remote commander (README "Remote leader"). Remote mode takes lead_bot, and
	// optionally bots, tank and cc; its only GM command is the enter phase's `.tele name`, and it refuses teleport, buffs, brain, gm and gm_level.
	Mode string `json:"mode,omitempty"`
	// LeadBot is the bot that walks in remote mode; bots are the other bots to bind with it (empty: its party as it is).
	LeadBot string   `json:"lead_bot,omitempty"`
	Bots    []string `json:"bots,omitempty"`
	// Tank names the bot that holds threat (remote mode); empty: the bot that holds the first pulled mob.
	Tank string `json:"tank,omitempty"`
	// Healer names the bot that gets the healer mana target at the rest gates (remote mode); empty: every caster does.
	Healer string `json:"healer,omitempty"`
	// Carrier names the bot that does the object steps in remote mode (loot_object, a boss's loot_item, use_item, use_object): it loots,
	// opens and uses items on `cmd` orders. Empty: the first living bot that is not the lead bot, the tank or the healer.
	Carrier string `json:"carrier,omitempty"`
	// LootRestore is the loot method put back after the carrier's free-for-all loot (remote mode): group (default), rr, master or nbg.
	LootRestore string `json:"loot_restore,omitempty"`
	// ObjectFallback is what an object step does when the carrier's order fails twice (remote mode): "agent" (the default) lets the
	// agent's own character open the object and trade the item to the carrier, "off" fails the step.
	ObjectFallback string `json:"object_fallback,omitempty"`
	// CC puts crowd-control marks on mobs of an entry before a pull (remote mode): [{"entry": 4863, "mark": "moon"}].
	CC []dungeon.CCRule `json:"cc,omitempty"`
	// Team is the party's team for the team gate (remote mode): "alliance" or "horde". Omitted: the agent's own (the owned bots share the
	// account's faction). Baelog, Eric and Olaf are friendly to the Alliance and never show in the bots' feed: without the team, the run
	// would wait for a boss it cannot see.
	Team string `json:"team,omitempty"`
	// Preflight is the kit check before the first step (remote mode): "warn" (the default: `.bot maintenance` and `.bot inv` for each bot,
	// what is missing is reported as preflight), "strict" (a warning fails the run) or "off".
	Preflight string `json:"preflight,omitempty"`
	// OnEnd is what happens to the bots at the end of a remote run: "stay" (the default: `cmd stay` before the unbind), "logout" (stay,
	// unbind, then `.bot logout` for each) or "none" (the unbind only: the bots go back to following the agent).
	OnEnd string `json:"on_end,omitempty"`
	// KeepOnFail (remote mode): a FAILED run keeps its binding and the party in its instance, so that a resume re-enters the same
	// instance. Off by default (the group is always unbound at the end); dungeon_stop with no run going unbinds a kept binding.
	KeepOnFail bool `json:"keep_on_fail,omitempty"`
	// Enter is how a remote run gets a party that is not inside the plan's map in (README "Remote leader"): "auto" (the default: the lead bot
	// is teleported with `.tele name` when the account is a developer-level GM and the plan names a tele, else walked into the portal),
	// "gm", "walk", or "off" (a party outside fails the run). IgnoreEntryLimit lets the run try a 6th new instance within the hour.
	Enter            string `json:"enter,omitempty"`
	IgnoreEntryLimit bool   `json:"ignore_entry_limit,omitempty"`
	// Prepare is the setup before the bind (remote mode; README "Remote leader", "Prepare"): online (`.bot add` for the bots the roster
	// shows offline), fresh (a new instance: the bots and the agent teleported out, the bots out of their group, `.instance unbind all` per
	// bot by name and for the agent; a GM) and gm_setup (`.gm visible off|on` and nothing else). The report's prepare has each step's seconds.
	// {"party": "hire"} instead hires the party (README "Hired party"): five hired bots at the plan's level, checked and bound with the
	// commander outside the group; lead_bot, bots, tank and healer are then left out, the step names them. The report's ready block has the bots.
	Prepare *dungeon.PrepareSpec `json:"prepare,omitempty"`
	// RunID names the run (letters, digits, . _ -; at most 64). Empty: the agent makes one (r-<utc yyyymmddThhmmss>-<4 hex>). It is
	// in the report, in every dungeon event, in the trace's header and file name; a caller that sets it can stamp the same id on
	// the server side (README "Run traces and checks").
	RunID string `json:"run_id,omitempty"`
	// Trace false turns the run's trace file off (on by default); the run id stays. TraceMeta is free text copied into the trace's
	// header (the server build, the TortoiseBots SHA, a scenario name): at most 16 entries.
	Trace     *bool             `json:"trace,omitempty"`
	TraceMeta map[string]string `json:"trace_meta,omitempty"`
	// Watchdog sets the progress watchdog's stall levels in seconds without progress (README "Run watchdog"): warn_s (a stall event and a
	// note), recover_s (one recovery attempt suited to the phase), fail_s (the run stops with "stalled: ..."). Defaults 120/240/420; a zero
	// field takes the plan's own or the default, a negative one turns that level off. It wins field by field over the plan's "watchdog".
	Watchdog *dungeon.WatchdogConfig `json:"watchdog,omitempty"`
	// Tune is the tunables file of a remote run (README "Live tuning"); empty: the login's --tune, else <plan>.tune.json beside the plan
	// file, which may be absent. A named file must exist at the start. LiveReload false stops the run from reading the plan file and the
	// tunables file again at leg and pull-group starts (tune_set still works).
	Tune       string `json:"tune,omitempty"`
	LiveReload *bool  `json:"live_reload,omitempty"`
}

type dungeonRunResult struct {
	Started bool   `json:"started"`
	Plan    string `json:"plan"`
	Map     uint32 `json:"map"`
	FromLeg int    `json:"from_leg"`
	// FromPullGroup is the pull group the run starts at (from_pull_group, or the pulltest's).
	FromPullGroup *int     `json:"from_pull_group,omitempty"`
	Legs          []string `json:"legs"`
	Buffs         []string `json:"buffs"`           // the spells the run will put on the companions (GM), empty for none
	Brain         string   `json:"brain,omitempty"` // the strategies that play the leader, empty for none
	// LeaderGM is "true" (the run takes the leader for a GM), "false" (it sends no GM command) or "probe" (it asks the
	// server at the start; the report's leader_gm has the answer).
	LeaderGM string `json:"leader_gm"`
	// Mode is "group" or "remote"; LeadBot is the remote run's lead bot.
	Mode    string `json:"mode"`
	LeadBot string `json:"lead_bot,omitempty"`
	// RunID names the run; TraceFile is where its trace goes (absent when tracing is off).
	RunID     string `json:"run_id"`
	TraceFile string `json:"trace_file,omitempty"`
}

type dungeonStatusResult struct {
	Running bool            `json:"running"`
	State   string          `json:"state"` // idle, running, done, failed or stopped
	Report  *dungeon.Report `json:"report,omitempty"`
	// The progress watchdog's view, lifted out of report.watchdog for a poller that wants to see at a glance whether a run hangs: when it
	// last made progress, the seconds since then (0 while it waits on something with a limit of its own) and the stall level (none, warn,
	// recover or fail).
	LastProgressAt *time.Time `json:"last_progress_at,omitempty"`
	StalledForS    float64    `json:"stalled_for_s"`
	StallLevel     string     `json:"stall_level"`
}

// planName limits a plan name to what a file name in the plans folder can be.
var planName = regexp.MustCompile(`^[a-z0-9][a-z0-9_-]{0,39}$`)

// dungeonRunner holds the one dungeon run the agent has at a time.
type dungeonRunner struct {
	cur   func() *session.Session
	dir   string
	root  context.Context
	brain *brain.Controller
	lease *brainLease
	// probeWindow is how long a run listens for the server's answer to its ".account" probe (accountProbeWindow when 0).
	probeWindow time.Duration
	// remote is the commander service remote mode runs on; nil in tests that do not build it.
	remote *commanderService
	// tracesDir is where run traces go (settings traces_dir); empty means "traces".
	tracesDir string
	// tunePath is the login's --tune: the tunables file of runs that name none.
	tunePath string

	mu  sync.Mutex
	drv dungeon.Runner
	// kept is the hires of a binding the last run kept (keep_on_fail): dungeon_stop unbinds it, the next run's prepare takes their class
	// and level from it (a bound hire does not answer `who`).
	kept   map[string]tbm.Who
	cancel context.CancelFunc
	done   chan struct{}
}

// registerDungeonMethods wires dungeon_run, dungeon_status and dungeon_stop to the session cur returns at each
// call (see registerMethodsFor). The returned func stops a run still going; call it before the process ends. The
// runner is returned for the methods that read the report (map_snapshot).
func registerDungeonMethods(srv *rpc.Server, cur func() *session.Session, dir string, brainCtl *brain.Controller, lease *brainLease, probeWindow time.Duration, remote *commanderService) (func(), *dungeonRunner) {
	if dir == "" {
		dir = "dungeons"
	}
	root, cancel := context.WithCancel(context.Background())
	r := &dungeonRunner{cur: cur, dir: dir, root: root, brain: brainCtl, lease: lease, probeWindow: probeWindow, remote: remote}
	srv.Register("dungeon_run", rpc.Typed(func(ctx context.Context, p dungeonRunParams) (dungeonRunResult, error) {
		res, err := r.run(p)
		if err == nil {
			r.mu.Lock()
			rep := r.drv.Report()
			r.mu.Unlock()
			res.RunID, res.TraceFile = rep.RunID, rep.TraceFile
		}
		return res, err
	}))
	srv.Register("dungeon_status", rpc.Typed(func(ctx context.Context, _ empty) (dungeonStatusResult, error) {
		return r.status(), nil
	}))
	srv.Register("dungeon_stop", rpc.Typed(func(ctx context.Context, _ empty) (dungeonStatusResult, error) {
		return r.stop(), nil
	}))
	registerTuneMethods(srv, r)
	return func() {
		cancel()
		r.wait(10 * time.Second)
	}, r
}

// planRunRefusal says why dungeon_run will not take the plan, or "" when it will. A raid plan is data only (no raid driver yet); a
// draft plan has had no mapping walk, so it runs only when the caller says allow_draft.
func planRunRefusal(plan *dungeon.Plan, allowDraft bool) string {
	switch {
	case plan.IsRaid():
		return fmt.Sprintf("plan %s is a raid plan: dungeon_run leads a 5-man party and has no raid driver yet", plan.Name)
	case strings.HasPrefix(plan.Status, dungeon.StatusDraft) && !allowDraft:
		return fmt.Sprintf("plan %s is a draft (%s): its route is a skeleton from the world database that has not had its mapping walk; walk it first, or pass allow_draft to run it anyway", plan.Name, plan.Status)
	}
	return ""
}

func (r *dungeonRunner) running() bool {
	if r.done == nil {
		return false
	}
	select {
	case <-r.done:
		return false
	default:
		return true
	}
}

func (r *dungeonRunner) run(p dungeonRunParams) (dungeonRunResult, error) {
	if !planName.MatchString(p.Name) {
		return dungeonRunResult{}, &rpc.Error{Code: rpc.CodeInvalidParams, Message: `name must be a plan file name such as "uldaman" (a-z, 0-9, _ and -)`}
	}
	planPath := filepath.Join(r.dir, p.Name+".json")
	plan, err := dungeon.Load(planPath)
	if err != nil {
		return dungeonRunResult{}, &rpc.Error{Code: rpc.CodeInvalidParams, Message: "dungeon plan: " + err.Error()}
	}
	if msg := planRunRefusal(plan, p.AllowDraft); msg != "" {
		return dungeonRunResult{}, &rpc.Error{Code: rpc.CodeInvalidParams, Message: msg}
	}
	setup, err := cleanSetup(p.Setup)
	if err != nil {
		return dungeonRunResult{}, &rpc.Error{Code: rpc.CodeInvalidParams, Message: err.Error()}
	}
	var buffs []dungeon.Buff
	if p.Buffs != nil {
		if buffs, err = dungeon.ResolveBuffs(p.Buffs.presets, p.Buffs.ids); err != nil {
			return dungeonRunResult{}, &rpc.Error{Code: rpc.CodeInvalidParams, Message: err.Error()}
		}
	}
	remote, err := checkMode(p)
	if err != nil {
		return dungeonRunResult{}, err
	}
	if err := watchdogParam(plan, p); err != nil {
		return dungeonRunResult{}, err
	}
	tcfg, err := r.traceConfig(p)
	if err != nil {
		return dungeonRunResult{}, err
	}
	if p.GMLevel != nil && !remote {
		switch {
		case p.GM == nil || !*p.GM:
			return dungeonRunResult{}, &rpc.Error{Code: rpc.CodeInvalidParams, Message: "gm_level goes with gm true (it states the GM account's level instead of probing it)"}
		case *p.GMLevel < 1 || *p.GMLevel > 6:
			return dungeonRunResult{}, &rpc.Error{Code: rpc.CodeInvalidParams, Message: "gm_level must be 1 (observer) to 6 (console)"}
		}
	}
	if p.GM != nil && !*p.GM && !remote {
		switch {
		case p.Teleport:
			return dungeonRunResult{}, &rpc.Error{Code: rpc.CodeInvalidParams, Message: "teleport needs a GM account (.go xyz): with gm false stand at the entrance and run without teleport"}
		case p.Buffs != nil:
			return dungeonRunResult{}, &rpc.Error{Code: rpc.CodeInvalidParams, Message: "buffs need a GM account (.aura, developer level): with gm false run without buffs"}
		}
	}
	var hook dungeon.Brain
	if p.Brain != "" && !remote {
		if r.brain == nil {
			return dungeonRunResult{}, &rpc.Error{Code: rpc.CodeGame, Message: "the bot brain is not available"}
		}
		if _, _, err := brain.Resolve([]string{p.Brain}); err != nil {
			return dungeonRunResult{}, &rpc.Error{Code: rpc.CodeInvalidParams, Message: "brain: " + err.Error()}
		}
		hook = &driverBrain{ctl: r.brain, strategies: []string{p.Brain}, runID: tcfg.RunID}
	}
	from := legIndex(plan, p.FromLeg)
	if pt := p.Pulltest; pt != nil && remote {
		if pt.Leg == nil || pt.PullGroup == nil {
			return dungeonRunResult{}, &rpc.Error{Code: rpc.CodeInvalidParams, Message: "pulltest needs leg (a number or a name) and pull_group"}
		}
		if from = legIndex(plan, pt.Leg); from < 0 {
			return dungeonRunResult{}, &rpc.Error{Code: rpc.CodeInvalidParams, Message: fmt.Sprintf("pulltest.leg: the plan has legs %v", legNames(plan))}
		}
	}
	names := make([]string, len(plan.Legs))
	for i, l := range plan.Legs {
		names[i] = l.Name
	}
	if from < 0 || from >= len(plan.Legs) {
		return dungeonRunResult{}, &rpc.Error{Code: rpc.CodeInvalidParams, Message: fmt.Sprintf("from_leg: the plan has legs %v (numbers 0-%d)", names, len(plan.Legs)-1)}
	}
	if remote {
		if err := checkStartParams(p, plan, from); err != nil {
			return dungeonRunResult{}, err
		}
	}
	if st := r.cur().Status().State; st != session.StatusInWorld {
		return dungeonRunResult{}, &rpc.Error{Code: rpc.CodeNotInWorld, Message: "dungeon_run needs the character in the world (state " + string(st) + ")"}
	}

	r.mu.Lock()
	defer r.mu.Unlock()
	if r.running() {
		return dungeonRunResult{}, &rpc.Error{Code: rpc.CodeGame, Message: "a dungeon run is going (" + r.drv.Report().Plan + "): dungeon_stop it first"}
	}
	if remote {
		tunePath, err := r.tuneFile(p, planPath)
		if err != nil {
			return dungeonRunResult{}, err
		}
		return r.startRemote(p, tcfg, plan, planPath, tunePath, setup, from, names)
	}
	cfg := dungeon.DefaultConfig()
	cfg.Trace = tcfg
	cfg.Teleport, cfg.Shortcuts, cfg.Setup, cfg.Buffs = p.Teleport, !p.NoShortcuts, setup, buffs
	cfg.RecoverWipe, cfg.WaitSickness = p.RecoverWipe, p.WaitSickness
	if p.Watchdog != nil {
		cfg.Watchdog = *p.Watchdog
	}
	cfg.Resume = p.Resume != nil && *p.Resume
	leaderGM := "probe"
	switch {
	case p.GM == nil:
		cfg.GM = dungeon.GMProbe
	case *p.GM:
		cfg.GM, leaderGM = dungeon.GMAssumed, "true"
		if p.GMLevel != nil {
			cfg.GMLevel = *p.GMLevel
		}
	default:
		cfg.GM, leaderGM = dungeon.GMNone, "false"
	}
	if hook != nil {
		// The lease comes first: checking Running() before it left a window in which a brain_start could slip in between.
		if !r.lease.acquire() {
			// A run that has not finished ending still owns the brain (r.running() above is already false once its context is done).
			return dungeonRunResult{}, &rpc.Error{Code: rpc.CodeGame, Message: "the previous dungeon run still owns the brain (it is ending): try again in a moment"}
		}
		if r.brain.Running() {
			r.lease.release()
			return dungeonRunResult{}, &rpc.Error{Code: rpc.CodeGame, Message: "a bot brain that was started by hand is running: brain_stop it first"}
		}
		cfg.Brain, cfg.BrainName = hook, p.Brain
	}
	game := &sessionGame{cur: r.cur, probeWindow: r.probeWindow}
	game.refusals.mark(r.cur().Log())
	drv := dungeon.NewDriver(plan, game, cfg, dungeon.RealClock())
	ctx, cancel := context.WithCancel(r.root)
	done := make(chan struct{})
	r.drv, r.cancel, r.done = drv, cancel, done
	go func() {
		defer close(done)
		defer cancel()
		if hook != nil {
			defer r.lease.release()
			defer hook.Stop() // whatever the run left on
		}
		_ = drv.Run(ctx, from) // the outcome is in the report and the events
	}()
	buffNames := []string{}
	for _, b := range buffs {
		buffNames = append(buffNames, b.String())
	}
	return dungeonRunResult{Started: true, Plan: plan.Name, Map: plan.Map, FromLeg: from, Legs: names, Buffs: buffNames, Brain: cfg.BrainName, LeaderGM: leaderGM, Mode: "group"}, nil
}

func legNames(plan *dungeon.Plan) []string {
	names := make([]string, len(plan.Legs))
	for i, l := range plan.Legs {
		names[i] = l.Name
	}
	return names
}

// checkStartParams checks from_pull_group, start_here and pulltest against the plan and against each other (remote mode).
func checkStartParams(p dungeonRunParams, plan *dungeon.Plan, from int) error {
	bad := func(format string, a ...any) error {
		return &rpc.Error{Code: rpc.CodeInvalidParams, Message: fmt.Sprintf(format, a...)}
	}
	resume := p.Resume != nil && *p.Resume
	if len(p.HaveItems) > 20 || slices.Contains(p.HaveItems, 0) {
		return bad("have_items: at most 20 item entries, none 0")
	}
	if g := p.FromPullGroup; g != nil {
		if n := len(plan.Legs[from].PullGroups); *g < 0 || *g >= n {
			return bad("from_pull_group %d: leg %d (%s) has %d pull groups (numbers 0-%d)", *g, from, plan.Legs[from].Name, n, n-1)
		}
		if p.StartHere {
			return bad("from_pull_group and start_here exclude each other: start_here does not move the party")
		}
		if resume {
			return bad("from_pull_group and resume exclude each other: both say where the run starts in the leg")
		}
	}
	if pt := p.Pulltest; pt != nil {
		if p.FromLeg != nil || p.FromPullGroup != nil || p.StartHere || resume {
			return bad("pulltest names its own start (leg and pull_group): leave from_leg, from_pull_group, start_here and resume out")
		}
		if pt.Tries < 0 {
			return bad("pulltest.tries %d: use 1-%d", pt.Tries, dungeon.MaxPulltestTries)
		}
		if err := (dungeon.PulltestSpec{Leg: from, PullGroup: *pt.PullGroup, Tries: pt.Tries, Reset: pt.Reset}).Validate(plan); err != nil {
			return bad("%v", err)
		}
	}
	return nil
}

// Bounds on the setup list, so a report cannot be filled with a novel.
const (
	maxSetup     = 40
	maxSetupText = 400
)

// cleanSetup trims the setup entries and refuses empty or over-long ones.
func cleanSetup(in []string) ([]string, error) {
	if len(in) > maxSetup {
		return nil, fmt.Errorf("setup: %d entries, at most %d", len(in), maxSetup)
	}
	var out []string
	for i, s := range in {
		s = strings.TrimSpace(s)
		if s == "" || len(s) > maxSetupText {
			return nil, fmt.Errorf("setup[%d]: needs 1-%d characters", i, maxSetupText)
		}
		out = append(out, s)
	}
	return out, nil
}

func (r *dungeonRunner) status() dungeonStatusResult {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.drv == nil {
		return dungeonStatusResult{State: "idle", StallLevel: "none"}
	}
	// The run's goroutine is read first: it ends after the driver published the end of the run (and its event), so a goroutine seen
	// gone means a final report. The report and whether it ended are one snapshot; a state that is not final is a run that goes on.
	gone := !r.running()
	rep, ended := r.drv.Status()
	res := dungeonStatusResult{Running: !gone || !ended, State: rep.State, Report: &rep, StallLevel: "none"}
	if w := rep.Watchdog; w != nil {
		at := w.LastProgressAt
		res.LastProgressAt, res.StalledForS, res.StallLevel = &at, w.StalledForS, w.StallLevel
	}
	return res
}

// watchdogParam refuses a watchdog parameter that, on top of the plan's own, leaves the levels out of order (warn_s < recover_s < fail_s).
func watchdogParam(plan *dungeon.Plan, p dungeonRunParams) error {
	var c dungeon.WatchdogConfig
	if plan.Watchdog != nil {
		c = *plan.Watchdog
	}
	if p.Watchdog != nil {
		c = c.Merge(*p.Watchdog)
	}
	if err := c.Validate(); err != nil {
		return &rpc.Error{Code: rpc.CodeInvalidParams, Message: err.Error()}
	}
	return nil
}

// wait blocks until the run's goroutine has ended or d has passed.
func (r *dungeonRunner) wait(d time.Duration) {
	r.mu.Lock()
	done := r.done
	r.mu.Unlock()
	if done == nil {
		return
	}
	select {
	case <-done:
	case <-time.After(d):
	}
}

func (r *dungeonRunner) stop() dungeonStatusResult {
	r.mu.Lock()
	cancel := r.cancel
	r.mu.Unlock()
	if cancel != nil {
		cancel()
		r.wait(10 * time.Second)
	}
	r.unbindKept()
	return r.status()
}

// keptFrom notes the hires of the binding a finished run kept (keep_on_fail), from its report; nothing when it unbound.
func (r *dungeonRunner) keptFrom(rep dungeon.Report) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.kept = nil
	if rep.Remote == nil || !strings.HasPrefix(rep.Remote.UnbindVia, "kept") {
		return
	}
	r.kept = map[string]tbm.Who{}
	if rep.Ready == nil {
		return
	}
	for _, b := range rep.Ready.Bots {
		w := tbm.Who{Class: b.Class, Level: b.Level}
		fmt.Sscanf(b.Talents, "%d/%d/%d", &w.Trees[0], &w.Trees[1], &w.Trees[2])
		r.kept[strings.ToLower(b.Name)] = w
	}
}

// unbindKept ends a binding the last run kept, when no run is going: `dungeon_stop` is how a kept binding is let go.
func (r *dungeonRunner) unbindKept() {
	r.mu.Lock()
	kept, running := r.kept, r.running()
	r.mu.Unlock()
	if kept == nil || running || r.remote == nil {
		return
	}
	ctx, cancel := context.WithTimeout(r.root, 15*time.Second)
	defer cancel()
	if _, err := (remoteGame{r.remote}).Command(ctx, "commander unbind"); err == nil {
		r.mu.Lock()
		r.kept = nil
		r.mu.Unlock()
	}
}

// sessionGame is the dungeon driver's Game over the agent's session. It asks cur at each call, so a reconnect that
// swaps the session does not strand a run.
type sessionGame struct {
	cur         func() *session.Session
	refusals    refusalCursor
	probeWindow time.Duration // ProbeAccount's listening time; accountProbeWindow when 0
}

func (g *sessionGame) Position() dungeon.Point {
	st := g.cur().Status()
	return dungeon.Point{X: st.X, Y: st.Y, Z: st.Z}
}

func (g *sessionGame) Map() uint32 { return g.cur().Status().Map }

func (g *sessionGame) GameObjects() []objects.GameObject {
	return g.cur().GameObjects(objects.GOFilter{Limit: 5000})
}

func (g *sessionGame) Units() []objects.Unit { return g.cur().Units(objects.Filter{Limit: 5000}) }

func (g *sessionGame) Self() dungeon.Self {
	st := g.cur().Status()
	return dungeon.Self{GUID: st.GUID, Name: st.Character, Health: st.Health, MaxHealth: st.MaxHealth, Ghost: st.Ghost}
}

func (g *sessionGame) Death() dungeon.DeathState {
	d := g.cur().Death()
	out := dungeon.DeathState{Dead: d.Dead, Ghost: d.Ghost, ReclaimIn: time.Duration(d.ReclaimInMS) * time.Millisecond}
	if d.Resurrect != nil {
		out.Offer = &dungeon.Offer{Name: d.Resurrect.Name, Sickness: d.Resurrect.Sickness}
	}
	return out
}

func (g *sessionGame) AcceptResurrect(ctx context.Context) error {
	_, err := g.cur().AcceptResurrect(ctx)
	return err
}

func (g *sessionGame) ReleaseSpirit(ctx context.Context) error { return g.cur().ReleaseSpirit(ctx) }

func (g *sessionGame) CorpseQuery(ctx context.Context) (dungeon.Corpse, error) {
	c, err := g.cur().CorpseQuery(ctx)
	if err != nil || !c.Found {
		return dungeon.Corpse{}, err
	}
	return dungeon.Corpse{Found: true, Map: uint32(max(c.Map, 0)), CorpseMap: c.CorpseMap, Pos: dungeon.Point{X: c.X, Y: c.Y, Z: c.Z}, Inside: c.Inside()}, nil
}

func (g *sessionGame) ReclaimCorpse(ctx context.Context) error { return g.cur().ReclaimCorpse(ctx) }

func (g *sessionGame) AreaTrigger(ctx context.Context, id uint32) error {
	return g.cur().AreaTrigger(ctx, id)
}

// accountProbeWindow is how long ProbeAccount listens for the server's answer to ".account".
const accountProbeWindow = 2 * time.Second

// accountLevelReply is LANG_ACCOUNT_LEVEL, "Your account level is: %u" (mangos_string 11), the answer of ".account"
// (HandleAccountCommand, Commands.cpp:6283-6291; SEC_PLAYER, Chat.cpp:77, so any account may ask).
var accountLevelReply = regexp.MustCompile(`^\s*Your account level is: (\d+)\s*$`)

// ProbeAccount sends ".account" and reads the account's security level from the reply. With PlayerCommands = 0 (not the
// shipped default) the server says the line aloud instead (Chat.cpp:1773-1774), which is read as level 0: the account may
// not run commands. Any other system line in the window is no answer.
func (g *sessionGame) ProbeAccount(ctx context.Context) (level int, known bool, err error) {
	s := g.cur()
	const cmd = ".account"
	since := s.Log().Last()
	self := s.Status().GUID
	if err := s.GM(ctx, cmd); err != nil {
		return 0, false, err
	}
	window := g.probeWindow
	if window <= 0 {
		window = accountProbeWindow
	}
	wctx, cancel := context.WithTimeout(ctx, window)
	defer cancel()
	var seen []events.Event
	for wctx.Err() == nil {
		_ = s.Log().Wait(wctx, since) // wakes on each new event; the window ends it
		page := s.Log().Read(events.Query{Since: since, Kinds: []string{events.KindChat, events.KindSystem}})
		since = page.Next
		seen = append(seen, page.Events...)
		if level, known := classifyAccountProbe(seen, self, cmd); known {
			return level, true, nil
		}
	}
	return 0, false, ctx.Err()
}

// classifyAccountProbe reads the events after the line cmd was sent: the reply "Your account level is: N" gives N; the
// player's own line coming back in "say" means the server did not run it, level 0. Anything else, system lines
// included, is ignored. known is false when neither came.
func classifyAccountProbe(evs []events.Event, self packet.GUID, cmd string) (level int, known bool) {
	for _, e := range evs {
		switch e.Kind {
		case events.KindSystem:
			if m := accountLevelReply.FindStringSubmatch(e.Text); m != nil {
				n, err := strconv.Atoi(m[1])
				if err == nil {
					return n, true
				}
			}
		case events.KindChat:
			if e.SourceGUID == self && strings.TrimSpace(e.Text) == cmd {
				return 0, true
			}
		}
	}
	return 0, false
}

func (g *sessionGame) Party() []dungeon.Member {
	var out []dungeon.Member
	for _, m := range g.cur().Party().Members {
		out = append(out, dungeon.Member{Name: m.Name, GUID: m.GUID, Online: m.Online, Dead: m.Dead, Health: uint32(m.Health), MaxHealth: uint32(m.MaxHealth),
			PowerType: m.PowerType, Power: uint32(m.Power), MaxPower: uint32(m.MaxPower)})
	}
	return out
}

func (g *sessionGame) WalkPath(ctx context.Context, to dungeon.Point) (dungeon.WalkResult, error) {
	r, err := g.cur().WalkPath(ctx, to.X, to.Y, to.Z)
	return dungeon.WalkResult{Result: r.Result, Reason: r.Reason, DistLeft: r.DistLeft}, err
}

func (g *sessionGame) PathPoints(ctx context.Context, to dungeon.Point) ([]dungeon.Point, error) {
	info, err := g.cur().PathTo(ctx, to.X, to.Y, to.Z)
	if err != nil {
		return nil, err
	}
	if len(info.Points) < 2 {
		return nil, fmt.Errorf("no path (%s): %s", info.Status, info.Reason)
	}
	pts := make([]dungeon.Point, len(info.Points))
	for i, p := range info.Points {
		pts[i] = dungeon.Point{X: p[0], Y: p[1], Z: p[2]}
	}
	return pts, nil
}

func (g *sessionGame) UseObject(ctx context.Context, guid packet.GUID) error {
	_, err := g.cur().UseObject(ctx, guid)
	return err
}

func (g *sessionGame) Target(ctx context.Context, guid packet.GUID) error {
	_, err := g.cur().Target(ctx, guid, "")
	return err
}

func (g *sessionGame) Chat(ctx context.Context, channel, text string) error {
	return g.cur().Chat(ctx, channel, text, "")
}

func (g *sessionGame) GM(ctx context.Context, text string) error { return g.cur().GM(ctx, text) }

func (g *sessionGame) GMOn(ctx context.Context, guid packet.GUID, text string) error {
	return g.cur().GMOn(ctx, guid, text)
}

func (g *sessionGame) Teleport(ctx context.Context, to dungeon.Point, mapID uint32) error {
	return g.cur().Teleport(ctx, to.X, to.Y, to.Z, int(mapID))
}

func (g *sessionGame) Emit(kind, text string, data map[string]any) {
	g.cur().Log().Append(events.Event{Kind: kind, Text: text, Data: data})
}
