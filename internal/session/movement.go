package session

import (
	"fmt"
	"math"
	"time"

	"github.com/pjerra/turtle-agent/internal/events"
	"github.com/pjerra/turtle-agent/internal/game"
	"github.com/pjerra/turtle-agent/internal/opcode"
)

// maxFlight bounds a flight; one that meets no floor by then lands where it is.
const maxFlight = 10 * time.Second

// landGap is the least time between a flight's last heartbeat and its landing.
const landGap = 150 * time.Millisecond

// maxBeatGap (ms of client time) bounds how long a landing waits for the anticheat's bound
// after the last heartbeat: the core records SKIPPED_HEARTBEATS past 1000 ms while moving
// (Movement.cpp:600-604).
const maxBeatGap = 900

// lastMove is the movement the server last has for the player: our last movement packet,
// or a position the server sent, which it holds as standing still there.
type lastMove struct {
	pos    [3]float32
	ctime  uint32
	flags  uint32
	jumpXY float32
}

// stepMargin is the slack the session allows a step over the anticheat's bound: half the
// 0.5 yard the server adds (Movement.cpp:576-578), the other half kept in reserve.
const stepMargin = 0.25

// checkStep holds a walk or facing packet to the distance the anticheat allows after the
// last movement (Movement::GetMaxAllowedDist, Movement.cpp:849-890, checked at :574-595):
// nothing after standing still, the run speed for the client-time gap while running, the
// jump speed across and the terminal speed up or down in the air. A z change past the bound
// is CHEAT_TYPE_OVERSPEED_Z (:593-595).
func (s *Session) checkStep(next game.MoveInfo) error {
	l := s.last
	dt := 0.0
	if next.Time > l.ctime {
		dt = float64(next.Time-l.ctime) / 1000
	}
	var dxy, dz float64
	switch {
	case l.flags&game.MoveMaskMoving == 0:
	case l.flags&(game.MoveJumping|game.MoveFallingFar) != 0:
		term := game.TerminalVelocity
		if l.flags&game.MoveSafeFall != 0 {
			term = game.TerminalSafeFall
		}
		dxy, dz = float64(l.jumpXY)*dt, term*dt
	default:
		dxy, dz = float64(s.runSpeed)*dt, float64(s.runSpeed)*dt
	}
	if d := math.Abs(float64(next.Z - l.pos[2])); d > dz+stepMargin {
		return fmt.Errorf("a %.2f yd change of height in %.3f s is over the %.2f yd the server allows (OVERSPEED_Z)", d, dt, dz)
	}
	if d := math.Hypot(float64(next.X-l.pos[0]), float64(next.Y-l.pos[1])); d > dxy+stepMargin {
		return fmt.Errorf("a %.2f yd step in %.3f s is over the %.2f yd the server allows", d, dt, dxy)
	}
	return nil
}

// sendMove writes a packet that carries mi and records mi as what the server now has.
func (s *Session) sendMove(op uint16, mi game.MoveInfo, body []byte) error {
	if err := s.conn.WritePacket(op, body); err != nil {
		return err
	}
	s.last = lastMove{pos: [3]float32{mi.X, mi.Y, mi.Z}, ctime: mi.Time, flags: mi.Flags, jumpXY: mi.JumpXYSpeed}
	return nil
}

// sendWalkMove is sendMove for a packet a running walk makes at now (a heartbeat, a facing,
// an ack mid-walk). It also records how far along the walk the server now has the player:
// the point an aborted walk stops at (abortWalk).
func (s *Session) sendWalkMove(op uint16, mi game.MoveInfo, body []byte, now time.Time) error {
	if err := s.sendMove(op, mi, body); err != nil {
		return err
	}
	if w := s.walk; w != nil {
		w.sent = w.walked(s.walkAxis(now))
	}
	return nil
}

// serverPosition records a position the server put the player at. zKnown is false when
// the z may be in the air (a GM teleport); the next position from the server settles it.
func (s *Session) serverPosition(p [3]float32, zKnown bool) {
	s.resolveFall() // a flight still waiting for its trace announces itself first
	s.fall = nil
	s.placed++
	s.last = lastMove{pos: p, ctime: s.last.ctime}
	s.mu.Lock()
	s.pos[0], s.pos[1], s.pos[2] = p[0], p[1], p[2]
	s.falling, s.zUnknown = false, !zKnown
	s.mu.Unlock()
}

// fallState is a flight through the air: a knockback's ballistic arc from where the server
// threw the player, or a fall from rest where a GM teleport left it. It lands on the navmesh
// floor the arc meets (trace) when the agent has the server's mmaps, else back at the height
// it started at, a guess. A feather-fall order mid-flight restarts the arc from where it is
// (restart).
type fallState struct {
	kb       game.KnockBack // direction, xy speed, and the jump values every packet echoes
	began    time.Time      // the knockback or the fall; the MovementInfo fall time counts from here
	startZ   float32        // the height the flight began at: the landing height without a floor
	floor    floorFunc      // the navmesh floors (nil: none, land at startZ)
	from     [3]float32     // where the current arc starts
	start    time.Time      // and when
	v0, term float64        // its downward speed then (negative: up) and the speed cap
	stopXY   float64        // seconds into the current arc when it hit a wall and stopped going across
	bumpT    float64        // and when it hit a ceiling and dropped from rest from there
	land     time.Time      // on the ground
	landPos  [3]float32     // where
	onFloor  bool           // landPos is on a navmesh floor
	water    bool           // and that floor is water, where the client would swim
	frozen   bool           // the current arc began against a wall
	nextBeat time.Time
	tracing  uint64     // the trace in flight off the loop (0: none); see Session.traceFall
	traceBy  time.Time  // when the flight stops waiting for it and keeps the guess
	late     bool       // a trace came too late: the landing is a guess
	announce string     // the event state ("knocked_back") emitted once the landing is known
	annFrom  [3]float32 // and its start position
	annAt    time.Time  // and time
}

// arc starts the current arc at from and start with downward speed v0 under the cap term,
// and finds its landing (begin, then traced). frozen keeps it from going across (it already
// hit a wall).
func (f *fallState) arc(from [3]float32, start time.Time, v0, term float64, frozen bool) {
	f.begin(from, start, v0, term, frozen)
	f.traced()
}

// begin starts the current arc and lands it, for now, back at startZ: a guess, which traced
// replaces with the navmesh floor the arc meets. The speed is held to the cap both ways:
// with MoveSafeFall the server bounds a step up or down by 7 yd/s (Movement.cpp:874-881).
func (f *fallState) begin(from [3]float32, start time.Time, v0, term float64, frozen bool) {
	f.from, f.start, f.term = from, start, term
	f.v0 = max(min(v0, term), -term)
	f.frozen = frozen
	f.guess()
}

// guess lands the current arc at startZ, off the navmesh.
func (f *fallState) guess() {
	f.stopXY, f.bumpT = math.Inf(1), math.Inf(1)
	if f.frozen {
		f.stopXY = 0
	}
	f.onFloor, f.water = false, false
	t := min(game.FallTimeTo(float64(f.from[2]-f.startZ), f.v0, f.term), max(f.limit(), 0))
	f.land = f.start.Add(time.Duration(t * float64(time.Second)))
	f.landPos = f.pos(t)
}

// limit is how long the current arc may fly: up to maxFlight after the flight began.
func (f *fallState) limit() float64 { return f.began.Add(maxFlight).Sub(f.start).Seconds() }

// traced lands the current arc on the navmesh floor it meets (trace). It may read tiles and
// wait for the planner, so the session runs it on a copy off its loop (Session.traceFall).
// A floor query that fails, or no floor met in time, keeps the guess.
func (f *fallState) traced() {
	if f.floor == nil {
		return
	}
	if t, ok := f.trace(f.limit()); ok {
		f.land = f.start.Add(time.Duration(t * float64(time.Second)))
		return
	}
	f.guess() // undo what the failed trace found
}

// restart continues the flight from where it is at now, at the speed it has, under a new
// cap: the fall a feather-fall order changes mid-air. Its landing is a guess until traced.
func (f *fallState) restart(now time.Time, term float64) {
	t := min(max(now.Sub(f.start), 0), f.land.Sub(f.start)).Seconds()
	f.begin(f.at(now), now, f.speed(t), term, f.stopXY <= t)
}

// pos is the current arc t seconds after it started, not stopped at the ground; after a
// ceiling bump it drops from rest from where the bump was.
func (f *fallState) pos(t float64) [3]float32 {
	if t > f.bumpT {
		b := f.pos(f.bumpT)
		b[2] -= float32(game.FallDrop(t-f.bumpT, 0, f.term))
		return b
	}
	xy := float64(f.kb.XYSpeed) * min(t, f.stopXY)
	return [3]float32{f.from[0] + f.kb.Cos*float32(xy), f.from[1] + f.kb.Sin*float32(xy),
		f.from[2] - float32(game.FallDrop(t, f.v0, f.term))}
}

// speed is the downward speed t seconds into the current arc.
func (f *fallState) speed(t float64) float64 {
	if t > f.bumpT {
		return game.FallSpeed(t-f.bumpT, 0, f.term)
	}
	return game.FallSpeed(t, f.v0, f.term)
}

// at is where the flight is at now: on the arc, or on the ground once landed.
func (f *fallState) at(now time.Time) [3]float32 {
	if !now.Before(f.land) {
		return f.landPos
	}
	return f.pos(max(now.Sub(f.start), 0).Seconds())
}

// descending says the flight is coming down at now, past its start: the packets then carry
// MoveFallingFar besides MoveJumping (see moveInfoAt).
func (f *fallState) descending(now time.Time) bool {
	return now.After(f.start) && f.speed(now.Sub(f.start).Seconds()) > 0
}

func fallTerm(toggles uint32) float64 {
	if toggles&game.MoveSafeFall != 0 {
		return game.TerminalSafeFall
	}
	return game.TerminalVelocity
}

// syncFall moves s.pos to where the flight is at now.
func (s *Session) syncFall(now time.Time) {
	if s.fall == nil {
		return
	}
	p := s.fall.at(now)
	s.mu.Lock()
	s.pos[0], s.pos[1], s.pos[2] = p[0], p[1], p[2]
	s.mu.Unlock()
}

// onKnockBack acks SMSG_MOVE_KNOCK_BACK and starts the flight. The server throws the player
// from the position it last had (Unit.cpp:10662-10682, Movement.cpp:103-115): a walk's last
// heartbeat, not where the walk has got to since. The ack carries MoveJumping and the
// order's values (Unit.cpp:7479-7495). The server sends no knockback while the player is
// rooted or stunned (:10664).
func (s *Session) onKnockBack(body []byte) error {
	k, err := game.ParseKnockBack(body)
	if err != nil {
		return err
	}
	if k.GUID != s.selfGUID() {
		return nil
	}
	now := s.now()
	s.stopWalk(errKnockedBack, false)
	from := s.last.pos
	// The ack only needs the start; it goes out before the flight is traced against the
	// navmesh (which may read a tile first), as the server drops a knockback whose ack has
	// not come within Movement.PendingAckResponseTime (2 s, World.cpp:1348, Unit.cpp:7264).
	term := fallTerm(s.toggles)
	s.resolveFall() // a flight still waiting for its trace announces itself first
	s.fall = &fallState{kb: k, began: now, startZ: from[2], floor: s.floorFunc(), from: from, start: now,
		v0: max(min(float64(k.ZSpeed), term), -term), term: term, land: now.Add(maxFlight), nextBeat: now.Add(500 * time.Millisecond)}
	s.mu.Lock()
	s.pos[0], s.pos[1], s.pos[2] = from[0], from[1], from[2]
	s.falling = true
	mi := s.moveInfoAt(s.currentFlags(), now)
	s.mu.Unlock()
	if err := s.sendMove(opcode.CMSGMoveKnockBackAck, mi, game.BuildKnockBackAck(k, mi)); err != nil {
		return &fatalError{err}
	}
	s.fall.begin(from, now, float64(k.ZSpeed), term, false)
	s.fall.announce, s.fall.annFrom, s.fall.annAt = "knocked_back", from, now
	s.traceFall(now)
	return nil
}

// tickFall sends the flight's heartbeats every 500 ms and MSG_MOVE_FALL_LAND, with the time
// fallen, at the landing. A heartbeat due less than landGap before the landing is left out,
// so the landing step, which may climb onto a floor a little above the arc, has the time
// to stay inside the anticheat's bound; should it still not, the landing waits until it
// does, at most until maxBeatGap after the last packet. The landing settles z when it is on a navmesh floor (not water: the agent does not
// swim); without one the landing z is the starting z, a guess, until the server sends a
// position (Status.ZUnknown).
func (s *Session) tickFall() error {
	f := s.fall
	if f == nil {
		return nil
	}
	now := s.now()
	if f.tracing != 0 {
		if now.Before(f.traceBy) {
			return nil // no beat, nor landing, on an arc the trace may still bend
		}
		// The planner is busy: keep a guess, and one that comes straight back down where
		// the arc began, on ground the server knows, rather than the arc's own guess, which
		// up a slope lands under the terrain (#31). No beat went out meanwhile to say otherwise.
		f.late, f.frozen = true, true
		f.guess()
		s.resolveFall()
	}
	if !now.Before(f.land) {
		p := f.landPos
		s.mu.Lock()
		old := s.pos
		s.pos[0], s.pos[1], s.pos[2] = p[0], p[1], p[2]
		fall := s.fall
		s.fall = nil
		mi := s.moveInfoAt(s.currentFlags(), now)
		if err := s.checkStep(mi); err != nil && mi.Time-s.last.ctime < maxBeatGap {
			s.fall, s.pos = fall, old // not yet
			s.mu.Unlock()
			return nil
		}
		zUnknown := !f.onFloor || f.water
		s.falling, s.zUnknown = false, zUnknown
		s.mu.Unlock()
		mi.FallTime = uint32(f.land.Sub(f.began).Milliseconds())
		if err := s.sendMove(opcode.MSGMoveFallLand, mi, game.BuildMovement(mi)); err != nil {
			return err
		}
		s.emit(events.Event{Kind: events.KindSession, Data: map[string]any{"state": "landed", "x": p[0], "y": p[1], "z": p[2],
			"z_unknown": zUnknown, "floor": f.floorKind(), "fall_ms": mi.FallTime, "floor_late": f.late}})
		return nil
	}
	if f.land.Sub(now) < landGap {
		return nil
	}
	if now.Before(f.nextBeat) {
		return nil
	}
	f.nextBeat = now.Add(500 * time.Millisecond)
	s.syncFall(now)
	s.mu.Lock()
	mi := s.moveInfoAt(s.currentFlags(), now)
	s.mu.Unlock()
	return s.sendMove(opcode.MSGMoveHeartbeat, mi, game.BuildMovement(mi))
}

// toggleOrder is one water-walk, hover or feather-fall order (MovementPacketSender.cpp:259-276)
// and its ack (Opcodes_1_12_1.h:225-226, :245-249, :722-723).
type toggleOrder struct {
	ack   uint16
	flag  uint32
	apply bool
}

var toggleOrders = map[uint16]toggleOrder{
	opcode.SMSGMoveWaterWalk:   {opcode.CMSGMoveWaterWalkAck, game.MoveWaterWalking, true},
	opcode.SMSGMoveLandWalk:    {opcode.CMSGMoveWaterWalkAck, game.MoveWaterWalking, false},
	opcode.SMSGMoveSetHover:    {opcode.CMSGMoveHoverAck, game.MoveHover, true},
	opcode.SMSGMoveUnsetHover:  {opcode.CMSGMoveHoverAck, game.MoveHover, false},
	opcode.SMSGMoveFeatherFall: {opcode.CMSGMoveFeatherFallAck, game.MoveSafeFall, true},
	opcode.SMSGMoveNormalFall:  {opcode.CMSGMoveFeatherFallAck, game.MoveSafeFall, false},
}

// toggleHandler acks a toggle order with its counter, the MovementInfo with the flag as
// now applied, and the apply u32 the server matches against the order
// (MovementHandler.cpp:543-558, Unit.cpp:7437-7449). The flag stays in later movement, as
// a real client keeps it. Feather fall applied or removed mid-flight restarts the arc under
// the new speed cap, which the server applies from this ack on (Movement.cpp:874-881).
func (s *Session) toggleHandler(o toggleOrder) handler {
	return func(body []byte) error {
		c, err := game.ParseMovementChange(body, false)
		if err != nil {
			return err
		}
		self := s.selfGUID()
		if c.GUID != self {
			return nil
		}
		if o.apply {
			s.toggles |= o.flag
		} else {
			s.toggles &^= o.flag
		}
		now := s.now()
		s.syncWalk(now)
		s.mu.Lock()
		mi := s.moveInfoAt(s.currentFlags(), now)
		s.mu.Unlock()
		if err := s.sendWalkMove(o.ack, mi, game.BuildMovementToggleAck(self, c.Counter, mi, o.apply), now); err != nil {
			return &fatalError{err}
		}
		if f := s.fall; f != nil && o.flag == game.MoveSafeFall { // traced after the ack, as a knockback
			f.restart(now, fallTerm(s.toggles))
			s.traceFall(now)
		}
		return nil
	}
}
