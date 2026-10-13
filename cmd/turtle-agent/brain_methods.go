package main

import (
	"context"
	"errors"
	"sync"

	"github.com/pjerra/turtle-agent/internal/brain"
	"github.com/pjerra/turtle-agent/internal/events"
	"github.com/pjerra/turtle-agent/internal/objects"
	"github.com/pjerra/turtle-agent/internal/packet"
	"github.com/pjerra/turtle-agent/internal/rpc"
	"github.com/pjerra/turtle-agent/internal/session"
	"github.com/pjerra/turtle-agent/internal/spellstate"
)

type brainStartParams struct {
	Strategies []string `json:"strategies"`
	Role       string   `json:"role,omitempty"`
	Tank       string   `json:"tank,omitempty"`
	NoMove     bool     `json:"no_move,omitempty"`
}

// registerBrainMethods wires brain_start, brain_stop and brain_status to the session cur returns at each call. The
// brain is off until brain_start (or dungeon_run's brain option) turns it on. The returned controller is for the
// dungeon runner; the returned func stops the brain and must be called before the process ends.
func registerBrainMethods(srv *rpc.Server, cur func() *session.Session) (*brain.Controller, *brainLease, func()) {
	root, cancel := context.WithCancel(context.Background())
	ctl := brain.NewController(root, &sessionBrainGame{cur: cur}, brain.Config{})
	lease := &brainLease{}
	srv.Register("brain_start", rpc.Typed(func(ctx context.Context, p brainStartParams) (brain.Status, error) {
		if st := cur().Status().State; st != session.StatusInWorld {
			return brain.Status{}, &rpc.Error{Code: rpc.CodeNotInWorld, Message: "brain_start needs the character in the world (state " + string(st) + ")"}
		}
		if err := lease.check("brain_start"); err != nil {
			return brain.Status{}, err
		}
		st, err := ctl.Start(brain.Params{Strategies: p.Strategies, Role: p.Role, Tank: p.Tank, NoMove: p.NoMove})
		return st, brainErr(err)
	}))
	srv.Register("brain_stop", rpc.Typed(func(ctx context.Context, _ empty) (brain.Status, error) {
		if err := lease.check("brain_stop"); err != nil {
			return brain.Status{}, err
		}
		return ctl.Stop(), nil
	}))
	srv.Register("brain_status", rpc.Typed(func(ctx context.Context, _ empty) (brain.Status, error) {
		return ctl.Status(), nil
	}))
	return ctl, lease, func() {
		cancel()
		ctl.Close()
	}
}

// brainLease says that a dungeon run owns the brain: brain_start and brain_stop are refused meanwhile, so that the
// run's idea of whether the brain runs cannot drift from the real one. brain_status is always answered.
type brainLease struct {
	mu   sync.Mutex
	held bool
}

// acquire takes the lease; false when a run holds it.
func (l *brainLease) acquire() bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.held {
		return false
	}
	l.held = true
	return true
}

func (l *brainLease) release() {
	l.mu.Lock()
	l.held = false
	l.mu.Unlock()
}

func (l *brainLease) check(method string) error {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.held {
		return &rpc.Error{Code: rpc.CodeGame, Message: method + ": a dungeon run owns the brain (dungeon_stop it first)"}
	}
	return nil
}

func brainErr(err error) error {
	switch {
	case err == nil:
		return nil
	case errors.Is(err, brain.ErrRunning):
		return &rpc.Error{Code: rpc.CodeGame, Message: err.Error()}
	}
	return &rpc.Error{Code: rpc.CodeInvalidParams, Message: err.Error()}
}

// driverBrain is the dungeon Driver's hold on the brain: it starts the brain for a run and stops only what it started,
// so a brain the caller started by hand is not turned off by a leg ending.
type driverBrain struct {
	ctl        *brain.Controller
	strategies []string

	started bool
}

func (b *driverBrain) Start() error {
	if b.started {
		return nil
	}
	if b.ctl.Running() {
		return errors.New("a brain was started by hand: the run leaves it alone")
	}
	// Movement is on: the brain positions the leader in fights, and the driver holds the move lock (HoldMove) around every walk it sends.
	if _, err := b.ctl.Start(brain.Params{Strategies: b.strategies, RestGate: true}); err != nil {
		return err
	}
	b.started = true
	return nil
}

func (b *driverBrain) Stop() {
	if b.started {
		b.ctl.Stop()
		b.started = false
	}
}

// HoldMove is what the driver calls around every movement of the leader: it stops a step of the brain's and keeps the brain from
// walking until the returned release is called. It works whether or not the brain runs.
func (b *driverBrain) HoldMove(ctx context.Context) (release func(), err error) {
	return b.ctl.HoldMove(ctx)
}

func (b *driverBrain) Rest(on bool) {
	if b.started {
		b.ctl.Rest(on)
	}
}

// sessionBrainGame is the brain's Game over the agent's session. It asks cur at each call, so a reconnect that swaps
// the session does not strand the brain.
type sessionBrainGame struct {
	cur   func() *session.Session
	melee meleeWatch
}

func (g *sessionBrainGame) Self() (objects.Unit, bool) {
	s := g.cur()
	u, err := s.Unit(s.Status().GUID, "")
	return u, err == nil
}

func (g *sessionBrainGame) Units() []objects.Unit { return g.cur().Units(objects.Filter{Limit: 2000}) }

func (g *sessionBrainGame) Party() []brain.Member {
	var out []brain.Member
	for _, m := range g.cur().Party().Members {
		out = append(out, brain.Member{Name: m.Name, GUID: m.GUID, Online: m.Online, Dead: m.Dead})
	}
	return out
}

func (g *sessionBrainGame) Selected() packet.GUID { return g.cur().Status().Target }

func (g *sessionBrainGame) Spells() *spellstate.State { return g.cur().SpellState() }

func (g *sessionBrainGame) Items() []objects.Item { return g.cur().Items() }

func (g *sessionBrainGame) Cast(ctx context.Context, spell uint32, target packet.GUID) error {
	return g.cur().Cast(ctx, spell, target)
}

func (g *sessionBrainGame) Face(ctx context.Context, t packet.GUID) error {
	return g.cur().Face(ctx, t)
}

func (g *sessionBrainGame) Target(ctx context.Context, t packet.GUID) error {
	_, err := g.cur().Target(ctx, t, "")
	return err
}

func (g *sessionBrainGame) Attack(ctx context.Context) error { return g.cur().Attack(ctx) }

func (g *sessionBrainGame) Stand(ctx context.Context) error { return g.cur().Stand(ctx) }

func (g *sessionBrainGame) StopAttack(ctx context.Context) error { return g.cur().StopAttack(ctx) }

func (g *sessionBrainGame) UseItem(ctx context.Context, entry uint32) error {
	_, err := g.cur().UseItem(ctx, session.EquipRef{Entry: entry}, 0)
	return err
}

func (g *sessionBrainGame) Emit(e events.Event) { g.cur().Log().Append(e) }
