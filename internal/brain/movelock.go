package brain

import (
	"context"
	"errors"
	"sync"
	"time"
)

// MoveLock says who moves the player. The dungeon driver walks the player along its plan; the brain steps the player to a better
// place in a fight. They must never walk at once (the session refuses a second walk, and two goals would fight over the
// position), so each takes the lock for its walk. The driver's hold wins: Hold stops a step of the brain's that is under way
// and, while it lasts, the brain's movement actions are not possible (casting goes on). A nil *MoveLock is a lock nobody
// holds, so code that has none (the driver of a test, a brain started without a controller) needs no special case.
type MoveLock struct {
	mu      sync.Mutex
	drivers int
	step    *brainStep
}

type brainStep struct {
	cancel context.CancelFunc
	done   chan struct{}
}

// NewMoveLock makes a free lock.
func NewMoveLock() *MoveLock { return &MoveLock{} }

// Held reports whether the driver holds the lock now.
func (l *MoveLock) Held() bool {
	if l == nil {
		return false
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.drivers > 0
}

// Hold takes the lock for the driver's walk and returns what lets it go; call that when the walk ends (it is safe to call twice).
// A step of the brain's that is under way is cancelled and waited for first, so the driver never meets a walk in progress. Holds
// nest: the lock is free once every hold is let go. The error is the context's, when it ended while waiting.
func (l *MoveLock) Hold(ctx context.Context) (release func(), err error) {
	if l == nil {
		return func() {}, nil
	}
	l.mu.Lock()
	l.drivers++
	st := l.step
	l.mu.Unlock()
	release = sync.OnceFunc(func() {
		l.mu.Lock()
		l.drivers--
		l.mu.Unlock()
	})
	if st != nil {
		st.cancel()
		select {
		case <-st.done:
		case <-ctx.Done():
			release()
			return func() {}, ctx.Err()
		}
	}
	return release, nil
}

// begin takes the lock for one step of the brain's. It fails while the driver holds the lock or another step runs. The returned
// context ends when the driver takes the lock; done lets the lock go.
func (l *MoveLock) begin(parent context.Context) (context.Context, func(), bool) {
	if l == nil {
		return parent, func() {}, true
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.drivers > 0 || l.step != nil {
		return nil, nil, false
	}
	ctx, cancel := context.WithCancel(parent)
	st := &brainStep{cancel: cancel, done: make(chan struct{})}
	l.step = st
	return ctx, func() {
		cancel()
		l.mu.Lock()
		l.step = nil
		l.mu.Unlock()
		close(st.done)
	}, true
}

// stepTimeout bounds one step of the brain's walk; a longer way is walked in several steps, the brain deciding again between them.
const stepTimeout = 2500 * time.Millisecond

var (
	errMoveLocked = errors.New("the dungeon driver is walking")
	errNoMover    = errors.New("the game cannot walk")
)

// walk steps the player to a point through the game's walker, under the move lock. A walk cut short (by stepTimeout, or by the
// driver taking the lock) is not a failure: the brain decides again on the next tick.
func (c *Ctx) walk(ctx context.Context, to Vec3) error {
	m, ok := c.G.(Mover)
	if !ok {
		return errNoMover
	}
	sctx, done, ok := c.lock.begin(ctx)
	if !ok {
		return errMoveLocked
	}
	defer done()
	tctx, cancel := context.WithTimeout(sctx, stepTimeout)
	defer cancel()
	err := m.WalkTo(tctx, to[0], to[1], to[2])
	if err != nil && tctx.Err() != nil {
		return nil
	}
	return err
}
