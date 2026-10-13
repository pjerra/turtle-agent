package session

import (
	"math"
	"time"

	"github.com/pjerra/turtle-agent/internal/game"
)

// The client time in movement packets has to run at the server's pace: the anticheat syncs
// the two clocks at the first movement and records CLOCK_DESYNC whenever the client has
// counted more than max(5 s, 500 ms a minute) beyond the server since then (Movement.cpp:434-467).
// The monotonic clock alone does not do: on a WSL2 host it runs about 3-4% fast, and only the
// wall clock is disciplined (stepped back about a second every half minute), so a session
// counting monotonic time drifted 73 s ahead in 40 minutes. moveClock counts monotonic time,
// which never steps, and slews it towards the wall clock, which keeps the long-run pace.
const (
	// clockSlewIdle is how much slower or faster than the monotonic clock the client time may
	// run while the player stands still: the server bounds no step by it then.
	clockSlewIdle = 0.5
	// clockSlewMoving is the same while a walk, fall or movement is under way. The anticheat
	// allows a step the run speed times the client-time gap, so a slower client time shortens
	// what a walk timed on the monotonic clock may cover: 2% of a 900 ms heartbeat gap at 11 yd/s
	// is 0.2 yd, inside stepMargin.
	clockSlewMoving = 0.02
	// clockRebase is how far the wall clock may stand from the client time before it counts as
	// set (by hand, or a resume from sleep) rather than drifting: the client time then keeps
	// its own pace and the wall clock is followed from where it now is.
	clockRebase = time.Minute
)

// moveClock is the movement client time, in milliseconds since the session's start.
type moveClock struct {
	mono    time.Duration // monotonic time since the start at the last reading
	ms      float64       // the client time at that reading
	wallOff time.Duration // added to the wall time since the start; moved by a rebase
}

// at is the clock at a reading taken mono (monotonic) and wall (wall-clock) after the start.
// It never runs back, and between two readings it advances the monotonic gap give or take
// slew of it, the part that closes on the wall clock.
func (c moveClock) at(mono, wall time.Duration, slew float64) moveClock {
	if mono <= c.mono {
		return c
	}
	dm := float64(mono-c.mono) / float64(time.Millisecond)
	v := c.ms + dm
	off := c.wallOff
	ahead := v - float64(wall+off)/float64(time.Millisecond)
	if math.Abs(ahead) > float64(clockRebase/time.Millisecond) {
		off += time.Duration(ahead * float64(time.Millisecond))
		ahead = 0
	}
	lim := dm * slew
	v -= min(max(ahead, -lim), lim)
	return moveClock{mono: mono, ms: v, wallOff: off}
}

// clockReading is now as monotonic and wall time since the session's start. A time without a
// monotonic reading (a replay's, a test's) gives the wall time for both.
func (s *Session) clockReading(now time.Time) (mono, wall time.Duration, slew float64) {
	slew = clockSlewIdle
	if s.walk != nil || s.fall != nil || s.last.flags&game.MoveMaskMoving != 0 {
		slew = clockSlewMoving
	}
	return now.Sub(s.start), now.Round(0).Sub(s.start.Round(0)), slew
}
