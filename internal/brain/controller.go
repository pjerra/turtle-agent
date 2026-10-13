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
	RestOK     bool       `json:"rest_ok"`
	Ticks      uint64     `json:"ticks"`
	Actions    uint64     `json:"actions"`        // ticks on which an action ran
	Idle       string     `json:"idle,omitempty"` // why the newest tick did nothing; empty when it did something
	Last       []Decision `json:"last_decisions"`
	Holds      []HoldInfo `json:"holds"`
	Error      string     `json:"error,omitempty"`
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
}

// NewController makes a controller whose brains run under root (cancel it to end them).
func NewController(root context.Context, g Game, cfg Config) *Controller {
	return &Controller{g: g, cfg: cfg.withDefaults(), root: root}
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
	b, err := New(c.g, c.cfg, p)
	if err != nil {
		return Status{}, err
	}
	b.restOK = func() bool { return !b.p.RestGate || c.rest.Load() }
	ctx, cancel := context.WithCancel(c.root)
	done := make(chan struct{})
	c.b, c.cancel, c.done = b, cancel, done
	c.rest.Store(false)
	c.g.Emit(events.Event{Kind: events.KindBrainState, Text: "started", Data: map[string]any{"state": "started", "strategies": b.Strategies(), "role": b.p.Role}})
	go func() {
		defer close(done)
		defer cancel()
		c.loop(ctx, b)
		c.g.Emit(events.Event{Kind: events.KindBrainState, Text: "stopped", Data: map[string]any{"state": "stopped", "strategies": b.Strategies(), "role": b.p.Role}})
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
	}
	return c.Status()
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
		return Status{Strategies: []string{}, Last: []Decision{}, Holds: []HoldInfo{}}
	}
	b := c.b
	now := b.cfg.Now()
	m := b.mem
	m.mu.Lock()
	defer m.mu.Unlock()
	st := Status{Running: c.running(), Strategies: b.Strategies(), Role: b.p.Role, Tank: b.p.Tank, RestGate: b.p.RestGate,
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

// Close stops the brain; call it before the process ends.
func (c *Controller) Close() { c.Stop() }
