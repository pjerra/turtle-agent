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
//
// Deadlock note: Hold (HoldMove on the controller) waits for a step of the brain's to end, and a step ends only when the game's walker
// answers its cancel. So Hold must not be called from inside the session's do loop (the goroutine that runs the session's handlers
// and the walker's ticks): the walker would never see the cancel, and Hold and the loop would wait for each other. Call it from the
// driver's own goroutine, as the dungeon driver does.
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

// stepping reports whether a step of the brain's is under way.
func (l *MoveLock) stepping() bool {
	if l == nil {
		return false
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.step != nil
}

// preempt cancels the step of the brain's that is under way, if one is, and waits up to wait for its walker to stop: a more
// relevant action (an interrupt, an emergency) then runs in the same tick instead of after the walk.
func (l *MoveLock) preempt(wait time.Duration) {
	if l == nil {
		return
	}
	l.mu.Lock()
	st := l.step
	l.mu.Unlock()
	if st == nil {
		return
	}
	st.cancel()
	t := time.NewTimer(wait)
	defer t.Stop()
	select {
	case <-st.done:
	case <-t.C:
	}
}

// settle waits up to wait for the step of the brain's that is under way to end; false when it was still there.
func (l *MoveLock) settle(wait time.Duration) bool {
	if l == nil {
		return true
	}
	l.mu.Lock()
	st := l.step
	l.mu.Unlock()
	if st == nil {
		return true
	}
	t := time.NewTimer(wait)
	defer t.Stop()
	select {
	case <-st.done:
		return true
	case <-t.C:
		return false
	}
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

// preemptWait is how long a more relevant action waits for the step it cancels to stop.
const preemptWait = 400 * time.Millisecond

// stopWait bounds how long Controller.Stop waits for the step that was under way when the brain was stopped.
const stopWait = 2 * time.Second

var (
	errMoveLocked = errors.New("the dungeon driver is walking")
	errNoMove     = errors.New("movement is off for this run")
	errNoMover    = errors.New("the game cannot walk")
	// errStepStopping: an earlier step of the brain's was cancelled but its walker has not answered yet (a slow cancel), so the
	// player cannot be sent a new walk. It is a matter of a moment, not a failure: the action is not held back (see Brain.run) and
	// the next tick tries again.
	errStepStopping = errors.New("the brain's earlier step is still stopping")
)

// walk starts a step of the player to a point through the game's walker, under the move lock, and returns at once: the step runs
// on its own goroutine, so the brain goes on ticking while the player walks. While a step runs, only an action at RelInterrupt
// or above runs (the tick cancels the step first, see Brain.try); every other action waits, so the player neither casts on the
// move nor starts a second walk. A step cut short (by stepTimeout, by a more relevant action, or by the driver taking the lock)
// is not a failure; a walk the game refuses is: the action is held back for ErrorHold and the failure is reported as a decision.
// The checks of canMove are repeated here as a second guard, so an action that forgot them still never walks the driver's player.
func (c *Ctx) walk(ctx context.Context, to Vec3) error {
	if c.P.NoMove {
		return errNoMove
	}
	if c.lock.Held() {
		return errMoveLocked
	}
	m, ok := c.G.(Mover)
	if !ok {
		return errNoMover
	}
	// A tick sets c.run, always. A Ctx built by hand (a test) has none: the action's own ctx would end the step when the action
	// returns, so the step hangs on a context that outlives it; stepTimeout still bounds the step.
	run := c.run
	if run == nil {
		run = context.WithoutCancel(ctx)
	}
	sctx, done, ok := c.lock.begin(run)
	if !ok {
		if c.lock.Held() { // the driver took the lock between the check above and here
			return errMoveLocked
		}
		return errStepStopping
	}
	b, name := c.b, c.act
	go func() {
		defer done()
		tctx, cancel := context.WithTimeout(sctx, stepTimeout)
		defer cancel()
		if err := m.WalkTo(tctx, to[0], to[1], to[2]); err != nil && tctx.Err() == nil && b != nil {
			b.walkFailed(name, err)
		}
	}()
	return nil
}
