package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"regexp"
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
	Name        string    `json:"name"`
	FromLeg     *legParam `json:"from_leg,omitempty"`
	Teleport    bool      `json:"teleport,omitempty"`     // GM-teleport the leader to the start and summon the party (flagged in the report)
	NoShortcuts bool      `json:"no_shortcuts,omitempty"` // no GM revive of a dead companion at a rest gate
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
}

type dungeonRunResult struct {
	Started bool     `json:"started"`
	Plan    string   `json:"plan"`
	Map     uint32   `json:"map"`
	FromLeg int      `json:"from_leg"`
	Legs    []string `json:"legs"`
	Buffs   []string `json:"buffs"`           // the spells the run will put on the companions (GM), empty for none
	Brain   string   `json:"brain,omitempty"` // the strategies that play the leader, empty for none
}

type dungeonStatusResult struct {
	Running bool            `json:"running"`
	State   string          `json:"state"` // idle, running, done, failed or stopped
	Report  *dungeon.Report `json:"report,omitempty"`
}

// planName limits a plan name to what a file name in the plans folder can be.
var planName = regexp.MustCompile(`^[a-z0-9][a-z0-9_-]{0,39}$`)

// dungeonRunner holds the one dungeon run the agent has at a time.
type dungeonRunner struct {
	cur   func() *session.Session
	dir   string
	root  context.Context
	brain *brain.Controller

	mu     sync.Mutex
	drv    *dungeon.Driver
	cancel context.CancelFunc
	done   chan struct{}
}

// registerDungeonMethods wires dungeon_run, dungeon_status and dungeon_stop to the session cur returns at each
// call (see registerMethodsFor). The returned func stops a run still going; call it before the process ends.
func registerDungeonMethods(srv *rpc.Server, cur func() *session.Session, dir string, brainCtl *brain.Controller) func() {
	if dir == "" {
		dir = "dungeons"
	}
	root, cancel := context.WithCancel(context.Background())
	r := &dungeonRunner{cur: cur, dir: dir, root: root, brain: brainCtl}
	srv.Register("dungeon_run", rpc.Typed(func(ctx context.Context, p dungeonRunParams) (dungeonRunResult, error) {
		return r.run(p)
	}))
	srv.Register("dungeon_status", rpc.Typed(func(ctx context.Context, _ empty) (dungeonStatusResult, error) {
		return r.status(), nil
	}))
	srv.Register("dungeon_stop", rpc.Typed(func(ctx context.Context, _ empty) (dungeonStatusResult, error) {
		return r.stop(), nil
	}))
	return func() {
		cancel()
		r.wait(10 * time.Second)
	}
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
	plan, err := dungeon.Load(filepath.Join(r.dir, p.Name+".json"))
	if err != nil {
		return dungeonRunResult{}, &rpc.Error{Code: rpc.CodeInvalidParams, Message: "dungeon plan: " + err.Error()}
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
	var hook dungeon.Brain
	if p.Brain != "" {
		if r.brain == nil {
			return dungeonRunResult{}, &rpc.Error{Code: rpc.CodeGame, Message: "the bot brain is not available"}
		}
		if _, _, err := brain.Resolve([]string{p.Brain}); err != nil {
			return dungeonRunResult{}, &rpc.Error{Code: rpc.CodeInvalidParams, Message: "brain: " + err.Error()}
		}
		hook = &driverBrain{ctl: r.brain, strategies: []string{p.Brain}}
	}
	from := 0
	if l := p.FromLeg; l != nil {
		from = l.index
		if l.name != "" {
			from = -1
			for i, leg := range plan.Legs {
				if leg.Name == l.name {
					from = i
				}
			}
		}
	}
	names := make([]string, len(plan.Legs))
	for i, l := range plan.Legs {
		names[i] = l.Name
	}
	if from < 0 || from >= len(plan.Legs) {
		return dungeonRunResult{}, &rpc.Error{Code: rpc.CodeInvalidParams, Message: fmt.Sprintf("from_leg: the plan has legs %v (numbers 0-%d)", names, len(plan.Legs)-1)}
	}
	if st := r.cur().Status().State; st != session.StatusInWorld {
		return dungeonRunResult{}, &rpc.Error{Code: rpc.CodeNotInWorld, Message: "dungeon_run needs the character in the world (state " + string(st) + ")"}
	}

	r.mu.Lock()
	defer r.mu.Unlock()
	if r.running() {
		return dungeonRunResult{}, &rpc.Error{Code: rpc.CodeGame, Message: "a dungeon run is going (" + r.drv.Report().Plan + "): dungeon_stop it first"}
	}
	cfg := dungeon.DefaultConfig()
	cfg.Teleport, cfg.Shortcuts, cfg.Setup, cfg.Buffs = p.Teleport, !p.NoShortcuts, setup, buffs
	if hook != nil {
		if r.brain.Running() {
			return dungeonRunResult{}, &rpc.Error{Code: rpc.CodeGame, Message: "a bot brain is running: brain_stop it first"}
		}
		cfg.Brain, cfg.BrainName = hook, p.Brain
	}
	drv := dungeon.NewDriver(plan, &sessionGame{cur: r.cur}, cfg, dungeon.RealClock())
	ctx, cancel := context.WithCancel(r.root)
	done := make(chan struct{})
	r.drv, r.cancel, r.done = drv, cancel, done
	go func() {
		defer close(done)
		defer cancel()
		_ = drv.Run(ctx, from) // the outcome is in the report and the events
	}()
	buffNames := []string{}
	for _, b := range buffs {
		buffNames = append(buffNames, b.String())
	}
	return dungeonRunResult{Started: true, Plan: plan.Name, Map: plan.Map, FromLeg: from, Legs: names, Buffs: buffNames, Brain: cfg.BrainName}, nil
}

// Bounds on the setup list, so a report cannot be filled with a novel.
const (
	maxSetup     = 40
	maxSetupText = 200
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
		return dungeonStatusResult{State: "idle"}
	}
	rep := r.drv.Report()
	return dungeonStatusResult{Running: r.running(), State: rep.State, Report: &rep}
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
	return r.status()
}

// sessionGame is the dungeon driver's Game over the agent's session. It asks cur at each call, so a reconnect that
// swaps the session does not strand a run.
type sessionGame struct{ cur func() *session.Session }

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
	return dungeon.Self{GUID: st.GUID, Name: st.Character, Health: st.Health, MaxHealth: st.MaxHealth}
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
