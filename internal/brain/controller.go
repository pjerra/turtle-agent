package brain

import (
	"context"
	"errors"
	"slices"
	"sort"
	"sync"
	"sync/atomic"

	"github.com/pjerra/turtle-agent/internal/events"
	"github.com/pjerra/turtle-agent/internal/spells"
	"github.com/pjerra/turtle-agent/internal/trace"
)

// ErrRunning is returned by Start while a brain runs.
var ErrRunning = errors.New("the brain is running: brain_stop it first")

// HoldInfo is a spell the brain keeps from casting for a moment after the server refused it.
type HoldInfo struct {
	SpellID uint32 `json:"spell_id"`
	Name    string `json:"name,omitempty"`
	Reason  string `json:"reason"`
	LeftMS  int64  `json:"left_ms"`
}

// Status is what brain_status reports.
type Status struct {
	Running    bool       `json:"running"`
	Strategies []string   `json:"strategies"`
	Role       string     `json:"role,omitempty"`
	Tank       string     `json:"tank,omitempty"`
	RestGate   bool       `json:"rest_gate,omitempty"` // upkeep (buffs, drinking) waits for the dungeon driver's rest gates
	NoMove     bool       `json:"no_move,omitempty"`   // the brain never walks the player (a dungeon run until the driver holds the move lock)
	MoveHeld   bool       `json:"move_held,omitempty"` // the dungeon driver holds the move lock now
	RestOK     bool       `json:"rest_ok"`
	Ticks      uint64     `json:"ticks"`
	Actions    uint64     `json:"actions"`        // ticks on which an action ran
	Idle       string     `json:"idle,omitempty"` // why the newest tick did nothing; empty when it did something
	Last       []Decision `json:"last_decisions"`
	Holds      []HoldInfo `json:"holds"`
	Error      string     `json:"error,omitempty"`
	RunID      string     `json:"run_id,omitempty"` // the session's run id (the last one's when the brain has stopped)
}

// Controller starts, stops and reports the one brain the agent runs, ticking it on its own goroutine so that it
// never blocks the session loop. It is safe for concurrent use.
type Controller struct {
	g    Game
	cfg  Config
	root context.Context

	mu     sync.Mutex
	b      *Brain
	cancel context.CancelFunc
	done   chan struct{}
	rest   atomic.Bool
	lock   *MoveLock // shared with every brain this controller runs
}

// NewController makes a controller whose brains run under root (cancel it to end them).
func NewController(root context.Context, g Game, cfg Config) *Controller {
	return &Controller{g: g, cfg: cfg.withDefaults(), root: root, lock: NewMoveLock()}
}

func (c *Controller) running() bool {
	if c.done == nil {
		return false
	}
	select {
	case <-c.done:
		return false
	default:
		return true
	}
}

// Running reports whether a brain is running.
func (c *Controller) Running() bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.running()
}

// Start builds a brain for p and runs it. It returns ErrRunning while another runs, and the error of New for bad
// parameters.
func (c *Controller) Start(p Params) (Status, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.running() {
		return Status{}, ErrRunning
	}
	if p.RunID == "" {
		p.RunID = trace.NewRunID(c.cfg.Now())
	}
	b, err := New(c.g, c.cfg, p)
	if err != nil {
		return Status{}, err
	}
	b.restOK = func() bool { return !b.p.RestGate || c.rest.Load() }
	b.lock = c.lock
	ctx, cancel := context.WithCancel(c.root)
	done := make(chan struct{})
	c.b, c.cancel, c.done = b, cancel, done
	c.rest.Store(false)
	c.g.Emit(events.Event{Kind: events.KindBrainState, Text: "started", Data: map[string]any{"state": "started", "strategies": b.Strategies(), "role": b.p.Role, "run_id": b.p.RunID}})
	go func() {
		defer close(done)
		defer cancel()
		c.loop(ctx, b)
		c.g.Emit(events.Event{Kind: events.KindBrainState, Text: "stopped", Data: map[string]any{"state": "stopped", "strategies": b.Strategies(), "role": b.p.Role, "run_id": b.p.RunID}})
	}()
	return c.statusLocked(), nil
}

// loop ticks b every Every until ctx ends.
func (c *Controller) loop(ctx context.Context, b *Brain) {
	ticks, stop := c.cfg.NewTicker(c.cfg.Every)
	defer stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticks:
			b.Tick(ctx)
		}
	}
}

// Stop ends the brain and waits for its goroutine. Stopping an idle controller is a no-op.
func (c *Controller) Stop() Status {
	c.mu.Lock()
	cancel, done := c.cancel, c.done
	c.mu.Unlock()
	if cancel != nil {
		cancel()
		<-done
		// A step of the brain's runs on its own goroutine and ends when its walker answers the cancel. Wait for it (bounded), so that
		// a restart right after the stop does not meet a lock still taken (errStepStopping on its first walk).
		c.lock.settle(stopWait)
	}
	return c.Status()
}

// HoldMove is for the dungeon driver: call it before a walk and call the release it returns when the walk ends. While it is held the
// brain starts no walk of its own, and a step of the brain's that is under way is stopped first, so the two never walk at once.
// It works whether or not a brain runs. The error is the context's.
func (c *Controller) HoldMove(ctx context.Context) (release func(), err error) {
	return c.lock.Hold(ctx)
}

// Rest allows or forbids out-of-combat upkeep for a brain started with Params.RestGate (the dungeon driver
// calls it at its rest gates).
func (c *Controller) Rest(on bool) { c.rest.Store(on) }

// Status reports the running brain, or the last one that ran, or an idle controller.
func (c *Controller) Status() Status {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.statusLocked()
}

func (c *Controller) statusLocked() Status {
	if c.b == nil {
		return Status{Strategies: []string{}, Last: []Decision{}, Holds: []HoldInfo{}, MoveHeld: c.lock.Held()}
	}
	b := c.b
	now := b.cfg.Now()
	m := b.mem
	m.mu.Lock()
	defer m.mu.Unlock()
	st := Status{RunID: b.p.RunID, Running: c.running(), Strategies: b.Strategies(), Role: b.p.Role, Tank: b.p.Tank, RestGate: b.p.RestGate, NoMove: b.p.NoMove, MoveHeld: c.lock.Held(),
		RestOK: !b.p.RestGate || c.rest.Load(), Ticks: m.ticks, Actions: m.acted, Idle: m.idle, Error: m.lastErr,
		Last: make([]Decision, 0, len(m.recent)), Holds: []HoldInfo{}}
	for _, d := range m.recent {
		d.AgoMS = now.Sub(d.at).Milliseconds()
		st.Last = append(st.Last, d)
	}
	slices.Reverse(st.Last) // newest first
	for id, h := range m.holds {
		if left := h.until.Sub(now); left > 0 {
			st.Holds = append(st.Holds, HoldInfo{SpellID: id, Name: spells.Name(id), Reason: h.reason, LeftMS: left.Milliseconds()})
		}
	}
	sort.Slice(st.Holds, func(i, j int) bool { return st.Holds[i].SpellID < st.Holds[j].SpellID })
	return st
}

// Stats is the counters of the character's play of the brain that runs, or of the last one (zero values without one).
func (c *Controller) Stats() Stats {
	c.mu.Lock()
	b := c.b
	c.mu.Unlock()
	if b == nil {
		return Stats{Actions: map[string]int{}, CastFailed: map[string]int{}, MinPowerPct: -1}
	}
	return b.mem.stat.snapshot()
}

// ResetStats starts a new window of counters (the pull test does it before every try). It does nothing without a brain.
func (c *Controller) ResetStats() {
	c.mu.Lock()
	b := c.b
	c.mu.Unlock()
	if b != nil {
		b.mem.stat.reset()
	}
}

// Close stops the brain; call it before the process ends.
func (c *Controller) Close() { c.Stop() }
