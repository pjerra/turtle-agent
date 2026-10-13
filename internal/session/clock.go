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
	// clockSlewMoving is the same while a knockback or fall is under way: a flight is timed on the
	// monotonic clock (fall.go), and the anticheat allows a step the speed times the client-time gap,
	// so a slower client time shortens what a flight may cover: 2% of a 900 ms heartbeat gap at
	// 11 yd/s is 0.2 yd, inside stepMargin. A walk is not held to it: it is timed on the client time
	// itself (walkAxis), so its steps keep the bound whatever the clock does, and the clock may
	// follow real time as fast as it does standing still. Timed on the monotonic clock, a 6-minute
	// walk on a 3.8% fast host gained 1.8% x 360 s = 6.5 s on the server, past the 5 s the
	// anticheat allows (live run 3 still had walks of that length).
	clockSlewMoving = 0.02
	// clockSlewWalkFast is how much faster than the monotonic clock the client time may run while a walk
	// is under way. The walk's heartbeats come every 500 ms of real time, and the anticheat records
	// SKIPPED_HEARTBEATS when two moving packets are more than 1000 ms of client time apart
	// (Movement.cpp:600-604): a client time that could run 1.5 times real time would turn a loop stall
	// of 700 ms into one, and would make the leader walk at 1.5 times its speed. So a walk can catch up
	// with a wall clock that is ahead (a forward step, a suspend, a slow monotonic clock) by 2% only;
	// slowing down, when the client time is ahead of the wall clock (a fast monotonic clock), keeps the
	// idle 0.5.
	clockSlewWalkFast = 0.02
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

// slewLimits bound how far a reading may pull the client time off the monotonic gap since the
// last one, as fractions of that gap: slow, how much of it may be withheld (the client time is
// ahead of the wall clock), fast, how much may be added (it is behind).
type slewLimits struct{ slow, fast float64 }

var (
	slewIdle    = slewLimits{clockSlewIdle, clockSlewIdle}
	slewWalking = slewLimits{clockSlewIdle, clockSlewWalkFast}
	slewFlight  = slewLimits{clockSlewMoving, clockSlewMoving}
)

// at is the clock at a reading taken mono (monotonic) and wall (wall-clock) after the start.
// It never runs back, and between two readings it advances the monotonic gap give or take
// the part that closes on the wall clock, within sl.
func (c moveClock) at(mono, wall time.Duration, sl slewLimits) moveClock {
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
	v -= min(max(ahead, -dm*sl.fast), dm*sl.slow)
	return moveClock{mono: mono, ms: v, wallOff: off}
}

// clockReading is now as monotonic and wall time since the session's start. A time without a
// monotonic reading (a replay's, a test's) gives the wall time for both.
func (s *Session) clockReading(now time.Time) (mono, wall time.Duration, sl slewLimits) {
	return now.Sub(s.start), now.Round(0).Sub(s.start.Round(0)), s.slewNow()
}

// slewNow is how far the client time may be pulled towards the wall clock now. A flight (a fall or
// knockback, timed on the monotonic clock) takes the small slew both ways. A walk is timed on the
// client time, so it may slow down as freely as standing still, but speeds up by 2% at most (see
// clockSlewWalkFast): it counts from the walk's start to its stop packet, which still carries the
// moving flags.
func (s *Session) slewNow() slewLimits {
	switch {
	case s.fall != nil || s.last.flags&(game.MoveJumping|game.MoveFallingFar) != 0:
		return slewFlight
	case s.walk != nil || s.last.flags&game.MoveMaskMoving != 0:
		return slewWalking
	}
	return slewIdle
}

// walkTime is the time axis of a walk: the session's start plus the client time c reads, in
// place of the monotonic clock the walk used to count on. A walk covers speed x (its axis gap)
// between two readings, and the packets carry that same client time, so no step is longer than
// the run speed times the client-time gap whichever way the host's clock drifts.
func walkTime(start time.Time, c moveClock) time.Time {
	return start.Add(time.Duration(c.ms * float64(time.Millisecond)))
}

// walkAxis is the walk axis at now (see walkTime). Owner loop only.
func (s *Session) walkAxis(now time.Time) time.Time {
	return walkTime(s.start, s.mclock.at(s.clockReading(now)))
}
