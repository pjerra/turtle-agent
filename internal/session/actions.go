package session

import (
	"context"
	"errors"
	"fmt"
	"math"
	"time"

	"github.com/pjerra/turtle-agent/internal/events"
	"github.com/pjerra/turtle-agent/internal/game"
	"github.com/pjerra/turtle-agent/internal/objects"
	"github.com/pjerra/turtle-agent/internal/opcode"
	"github.com/pjerra/turtle-agent/internal/packet"
)

const (
	maxWalk         = 40  // yards; longer moves use Teleport
	defaultRunSpeed = 7.0 // yards per second until the server sets another
)

// Why a walk ended early. A server move, a same-map teleport and a root interrupt a
// walk_path, which waits and plans again; the others end it.
var (
	errServerMoved = errors.New("moved by the server")
	errTeleported  = errors.New("teleported by the server")
	errNewMap      = errors.New("teleported to another map")
	errRooted      = errors.New("rooted by the server; walk stopped")
	errRootedNow   = errors.New("rooted; walk_to waits until the server releases the root")
	errCancelled   = errors.New("walk cancelled")
)

// walkState is a running walk along a polyline: the start, then for a navmesh leg the
// ground samples, then the end. Progress runs along the 3D length at the run speed, so
// no beat moves further than speed*dt, height included: the anticheat allows no more z
// change than that per beat (Movement.cpp:574-595, OVERSPEED_Z), even up a 50° slope.
type walkState struct {
	pts      [][3]float32 // at least two points; the last is to
	cum      []float64    // 3D length from pts[0] to pts[i]
	to       [3]float32
	o        float32
	start    time.Time // when base yards had been walked
	base     float64
	speed    float32
	nextBeat time.Time
	done     chan error
	sentZ    float32 // height and client time of the last movement packet sent for this walk
	sentT    uint32
}

func newWalk(pts [][3]float32, speed float32, now time.Time, done chan error) *walkState {
	w := &walkState{pts: pts, cum: make([]float64, len(pts)), to: pts[len(pts)-1], start: now, speed: speed,
		nextBeat: now.Add(500 * time.Millisecond), done: done}
	for i := 1; i < len(pts); i++ {
		w.cum[i] = w.cum[i-1] + dist3f(pts[i-1], pts[i])
	}
	w.o = game.Facing(pts[0][0], pts[0][1], w.to[0], w.to[1])
	return w
}

func dist3f(a, b [3]float32) float64 {
	dx, dy, dz := float64(b[0]-a[0]), float64(b[1]-a[1]), float64(b[2]-a[2])
	return math.Sqrt(dx*dx + dy*dy + dz*dz)
}

// walked is the 3D distance covered at now, at most the whole polyline.
func (w *walkState) walked(now time.Time) float64 {
	return min(w.base+float64(w.speed)*now.Sub(w.start).Seconds(), w.cum[len(w.cum)-1])
}

func (w *walkState) arrived(now time.Time) bool {
	return w.walked(now) >= w.cum[len(w.cum)-1]-1e-6
}

// at is the walk's position at now, clamped to the polyline.
func (w *walkState) at(now time.Time) [3]float32 {
	d := w.walked(now)
	for i := 1; i < len(w.pts); i++ {
		if d <= w.cum[i] || i == len(w.pts)-1 {
			seg := w.cum[i] - w.cum[i-1]
			f := float32(1)
			if seg > 1e-9 {
				f = float32(min(max((d-w.cum[i-1])/seg, 0), 1))
			}
			a, b := w.pts[i-1], w.pts[i]
			return [3]float32{a[0] + (b[0]-a[0])*f, a[1] + (b[1]-a[1])*f, a[2] + (b[2]-a[2])*f}
		}
	}
	return w.to
}

// clampZ holds a movement packet's height within speed*dt of the last one sent: a
// backstop under the polyline timing, for a ground sample that snapped to another floor.
func (w *walkState) clampZ(z float32, ctime uint32) float32 {
	lim := w.speed * float32(ctime-w.sentT) / 1000
	z = min(max(z, w.sentZ-lim), w.sentZ+lim)
	w.sentZ, w.sentT = z, ctime
	return z
}

// speedAcks maps each forced speed change to its ack (Opcodes_1_12_1.h:229-238, :733-738).
var speedAcks = map[uint16]uint16{
	opcode.SMSGForceRunSpeedChange:      opcode.CMSGForceRunSpeedChangeAck,
	opcode.SMSGForceRunBackSpeedChange:  opcode.CMSGForceRunBackSpeedChangeAck,
	opcode.SMSGForceSwimSpeedChange:     opcode.CMSGForceSwimSpeedChangeAck,
	opcode.SMSGForceWalkSpeedChange:     opcode.CMSGForceWalkSpeedChangeAck,
	opcode.SMSGForceSwimBackSpeedChange: opcode.CMSGForceSwimBackSpeedChangeAck,
	opcode.SMSGForceTurnRateChange:      opcode.CMSGForceTurnRateChangeAck,
}

var swingErrors = map[uint16]string{
	opcode.SMSGAttackswingNotinrange:  "not_in_range",
	opcode.SMSGAttackswingBadfacing:   "bad_facing",
	opcode.SMSGAttackswingNotstanding: "not_standing",
	opcode.SMSGAttackswingDeadtarget:  "dead_target",
	opcode.SMSGAttackswingCantAttack:  "cant_attack",
}

func (s *Session) registerActions() {
	s.handlers[opcode.MSGMoveTeleportAck] = s.onTeleportAck
	s.handlers[opcode.SMSGTransferPending] = s.onTransferPending
	s.handlers[opcode.SMSGNewWorld] = s.onNewWorld
	for op, ack := range speedAcks {
		s.handlers[op] = s.speedHandler(op, ack)
	}
	s.handlers[opcode.SMSGForceMoveRoot] = s.rootHandler(opcode.CMSGForceMoveRootAck, true)
	s.handlers[opcode.SMSGForceMoveUnroot] = s.rootHandler(opcode.CMSGForceMoveUnrootAck, false)
	s.handlers[opcode.SMSGAttackstart] = s.onAttackStart
	s.handlers[opcode.SMSGAttackstop] = s.onAttackStop
	for op, reason := range swingErrors {
		s.handlers[op] = func([]byte) error {
			s.emit(events.Event{Kind: events.KindAttackError, Text: "attack swing failed: " + reason, Data: map[string]any{"reason": reason}})
			return nil
		}
	}
}

func (s *Session) inWorld() error {
	if st := s.State(); st != StatusInWorld {
		return fmt.Errorf("%w (state %s)", ErrNotInWorld, st)
	}
	return nil
}

// clock is the MovementInfo client time: milliseconds on the session's monotonic clock,
// plus one, because the anticheat flags a zero time and a clock that runs backwards or
// more than 5 s ahead of the server's (Movement.cpp:377, :434-475).
func (s *Session) clock() uint32 { return uint32(time.Since(s.start).Milliseconds()) + 1 }

// moveInfo is the player's current MovementInfo; the caller holds s.mu.
func (s *Session) moveInfo(flags uint32) game.MoveInfo {
	return game.MoveInfo{Flags: flags, Time: s.clock(), X: s.pos[0], Y: s.pos[1], Z: s.pos[2], O: s.pos[3]}
}

// currentFlags are the movement flags the player has now. MoveRoot stays on while rooted,
// or the anticheat records ROOT_MOVE (Movement.cpp:186-204).
func (s *Session) currentFlags() uint32 {
	var f uint32
	if s.walk != nil {
		f |= game.MoveForward
	}
	if s.rooted {
		f |= game.MoveRoot
	}
	return f
}

// syncWalk moves s.pos to where the running walk is at now; a movement packet follows.
func (s *Session) syncWalk(now time.Time) {
	if s.walk == nil {
		return
	}
	p := s.walk.at(now)
	p[2] = s.walk.clampZ(p[2], s.clock())
	s.mu.Lock()
	s.pos[0], s.pos[1], s.pos[2] = p[0], p[1], p[2]
	s.mu.Unlock()
}

// Target selects a unit by guid, or by name, and returns it. A name picks the nearest
// living match; a corpse (the last run's kill) only when no living one is in view, and the
// returned unit then says dead. A guid selects exactly that unit, dead or alive.
func (s *Session) Target(ctx context.Context, g packet.GUID, name string) (objects.Unit, error) {
	if g == 0 {
		if name == "" {
			return objects.Unit{}, errors.New("target needs a guid or a name")
		}
		us := s.Units(objects.Filter{Name: name, Limit: math.MaxInt})
		if len(us) == 0 {
			return objects.Unit{}, fmt.Errorf("no unit named %q nearby", name)
		}
		g = us[0].GUID
		for _, u := range us {
			if !u.Dead {
				g = u.GUID
				break
			}
		}
	}
	_, err := s.do(ctx, func() (any, error) {
		if err := s.inWorld(); err != nil {
			return nil, err
		}
		if err := s.conn.WritePacket(opcode.CMSGSetSelection, game.BuildSetSelection(g)); err != nil {
			return nil, err
		}
		s.mu.Lock()
		s.target = g
		s.mu.Unlock()
		return nil, nil
	})
	if err != nil {
		return objects.Unit{}, err
	}
	x, y, z, _ := s.selfPos()
	u, _ := s.store.Get(g, x, y, z)
	u.GUID = g
	u.Name, _ = s.cachedName(g)
	u.InParty = s.inParty(g)
	return u, nil
}

func (s *Session) Attack(ctx context.Context) error {
	_, err := s.do(ctx, func() (any, error) {
		if err := s.inWorld(); err != nil {
			return nil, err
		}
		s.mu.Lock()
		t := s.target
		s.mu.Unlock()
		if t == 0 {
			return nil, errors.New("no target selected; call target first")
		}
		return nil, s.conn.WritePacket(opcode.CMSGAttackswing, game.BuildAttackSwing(t))
	})
	return err
}

func (s *Session) StopAttack(ctx context.Context) error {
	return s.send(ctx, opcode.CMSGAttackstop, nil)
}

// Cast casts spell on target, or on the current target when target is 0, or on the
// player when nothing is selected.
func (s *Session) Cast(ctx context.Context, spell uint32, target packet.GUID) error {
	if spell == 0 {
		return errors.New("cast needs a spell id")
	}
	_, err := s.do(ctx, func() (any, error) {
		if err := s.inWorld(); err != nil {
			return nil, err
		}
		t := target
		if t == 0 {
			s.mu.Lock()
			t = s.target
			s.mu.Unlock()
		}
		return nil, s.conn.WritePacket(opcode.CMSGCastSpell, game.BuildCastSpell(spell, t))
	})
	return err
}

// Face turns the player towards a known unit (MSG_MOVE_SET_FACING).
func (s *Session) Face(ctx context.Context, g packet.GUID) error {
	x, y, z, _ := s.selfPos()
	u, ok := s.store.Get(g, x, y, z)
	if !ok {
		return fmt.Errorf("unit %s is not in view", g)
	}
	o := game.Facing(x, y, u.X, u.Y)
	_, err := s.do(ctx, func() (any, error) {
		if err := s.inWorld(); err != nil {
			return nil, err
		}
		if s.walk != nil {
			return nil, errors.New("walking; wait for walk_to to finish")
		}
		s.mu.Lock()
		s.pos[3] = o
		mi := s.moveInfo(s.currentFlags())
		s.mu.Unlock()
		return nil, s.conn.WritePacket(opcode.MSGMoveSetFacing, game.BuildMovement(mi))
	})
	return err
}

// WalkTo walks in a straight line to a point at most maxWalk yards away and returns on arrival.
func (s *Session) WalkTo(ctx context.Context, x, y, z float32) error {
	return s.walkStraight(ctx, [3]float32{x, y, z}, maxWalk, nil)
}

// walkStraight is one straight walk to `to`, refused when it is over limit yards; walk_to
// and each segment of walk_path go through it. via are the ground points on the way (a
// navmesh leg's samples), walked through in order. It returns on arrival, or with the
// reason the walk ended early.
func (s *Session) walkStraight(ctx context.Context, to [3]float32, limit float64, via [][3]float32) error {
	x, y, z := to[0], to[1], to[2]
	v, err := s.do(ctx, func() (any, error) {
		if err := s.inWorld(); err != nil {
			return nil, err
		}
		if s.walk != nil {
			return nil, errors.New("already walking")
		}
		if s.rooted {
			return nil, errRootedNow
		}
		if left := time.Until(s.splineUntil); left > 0 {
			return nil, fmt.Errorf("%w (fear, charm or a scripted move); walk_to waits %.1f s until it ends", errServerMoved, left.Seconds())
		}
		s.mu.Lock()
		from := [3]float32{s.pos[0], s.pos[1], s.pos[2]}
		s.mu.Unlock()
		dx, dy, dz := float64(x-from[0]), float64(y-from[1]), float64(z-from[2])
		dist := math.Sqrt(dx*dx + dy*dy + dz*dz)
		if dist > limit {
			return nil, fmt.Errorf("walk_to is for short moves: %.1f yards is over %g, use walk_path or teleport", dist, limit)
		}
		done := make(chan error, 1)
		if dist < 0.1 {
			done <- nil
			return done, nil
		}
		now := time.Now()
		pts := append(append([][3]float32{from}, via...), [3]float32{x, y, z})
		w := newWalk(pts, s.runSpeed, now, done)
		s.mu.Lock()
		s.pos[3] = w.o
		mi := s.moveInfo(game.MoveForward)
		s.mu.Unlock()
		w.sentZ, w.sentT = mi.Z, mi.Time
		if err := s.conn.WritePacket(opcode.MSGMoveStartForward, game.BuildMovement(mi)); err != nil {
			return nil, err
		}
		s.walk = w
		return done, nil
	})
	if err != nil {
		return err
	}
	done := v.(chan error)
	select {
	case err := <-done:
		return err
	case <-ctx.Done():
		_, _ = s.do(context.Background(), func() (any, error) {
			if s.walk != nil && s.walk.done == done { // not a walk started after this one ended
				s.stopWalk(errCancelled, true)
			}
			return nil, nil
		})
		return ctx.Err()
	case <-s.done:
		return ErrClosed
	}
}

// tickWalk advances a walk: heartbeat every 500 ms, stop at the destination. It reads the
// clock once, not the tick's send time: a tick read late from a busy loop would place the
// heartbeat behind the time it carries, and the next step would exceed the run speed.
func (s *Session) tickWalk() error {
	w := s.walk
	if w == nil {
		return nil
	}
	now := time.Now()
	if w.arrived(now) {
		s.walk = nil
		z := w.clampZ(w.to[2], s.clock())
		s.mu.Lock()
		s.pos = [4]float32{w.to[0], w.to[1], z, w.o}
		mi := s.moveInfo(s.currentFlags())
		s.mu.Unlock()
		err := s.conn.WritePacket(opcode.MSGMoveStop, game.BuildMovement(mi))
		w.done <- err
		return err
	}
	if now.Before(w.nextBeat) {
		return nil
	}
	w.nextBeat = now.Add(500 * time.Millisecond)
	s.syncWalk(now)
	s.mu.Lock()
	mi := s.moveInfo(s.currentFlags())
	s.mu.Unlock()
	return s.conn.WritePacket(opcode.MSGMoveHeartbeat, game.BuildMovement(mi))
}

// stopWalk ends a walk early; sendStop is false when the server moved the player. The
// stop is sent from where the walk has got to and carries MoveRoot while rooted.
func (s *Session) stopWalk(reason error, sendStop bool) {
	w := s.walk
	if w == nil {
		return
	}
	if sendStop {
		s.syncWalk(time.Now())
	}
	s.walk = nil
	if sendStop {
		s.mu.Lock()
		mi := s.moveInfo(s.currentFlags())
		s.mu.Unlock()
		_ = s.conn.WritePacket(opcode.MSGMoveStop, game.BuildMovement(mi))
	}
	w.done <- reason
}

// Teleport sends ".go xyz x y z [map]" (Commands.cpp:8347-8367) and waits up to 10 s for
// the server's teleport (MSG_MOVE_TELEPORT_ACK or SMSG_NEW_WORLD).
func (s *Session) Teleport(ctx context.Context, x, y, z float32, mapID int) error {
	cmd := fmt.Sprintf(".go xyz %.2f %.2f %.2f", x, y, z)
	if mapID >= 0 {
		cmd += fmt.Sprintf(" %d", mapID)
	}
	body, err := game.BuildMessageChat(game.ChatSay, "", cmd)
	if err != nil {
		return err
	}
	v, err := s.do(ctx, func() (any, error) {
		if err := s.inWorld(); err != nil {
			return nil, err
		}
		ch := make(chan struct{})
		s.teleportWaiters = append(s.teleportWaiters, ch)
		return ch, s.conn.WritePacket(opcode.CMSGMessagechat, body)
	})
	if err != nil {
		return err
	}
	tctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	select {
	case <-v.(chan struct{}):
		return nil
	case <-tctx.Done():
		s.dropTeleportWaiter(v.(chan struct{}))
		return fmt.Errorf("teleport: no MSG_MOVE_TELEPORT_ACK or SMSG_NEW_WORLD within 10 s (does the account have GM rights?): %w", tctx.Err())
	case <-s.done:
		return ErrClosed
	}
}

// dropTeleportWaiter forgets a waiter whose Teleport gave up, so waiters do not pile up
// until the next teleport. Best effort: the session may be closing.
func (s *Session) dropTeleportWaiter(ch chan struct{}) {
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	s.do(ctx, func() (any, error) {
		for i, w := range s.teleportWaiters {
			if w == ch {
				s.teleportWaiters = append(s.teleportWaiters[:i], s.teleportWaiters[i+1:]...)
				break
			}
		}
		return nil, nil
	})
}

func (s *Session) teleported() {
	for _, ch := range s.teleportWaiters {
		close(ch)
	}
	s.teleportWaiters = nil
}

func (s *Session) selfGUID() packet.GUID {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.self.GUID
}

func (s *Session) onTeleportAck(body []byte) error {
	a, err := game.ParseTeleportAck(body)
	if err != nil {
		return err
	}
	self := s.selfGUID()
	if a.GUID != self {
		return nil
	}
	s.stopWalk(errTeleported, false)
	s.mu.Lock()
	s.pos = [4]float32{a.Move.X, a.Move.Y, a.Move.Z, a.Move.O}
	s.mu.Unlock()
	s.store.MoveTo(self, a.Move.X, a.Move.Y, a.Move.Z, a.Move.O)
	if err := s.conn.WritePacket(opcode.MSGMoveTeleportAck, game.BuildTeleportAck(self, a.Counter, s.clock())); err != nil {
		return &fatalError{err}
	}
	s.emit(events.Event{Kind: events.KindSession, Data: map[string]any{"state": "teleported", "x": a.Move.X, "y": a.Move.Y, "z": a.Move.Z}})
	s.teleported()
	return nil
}

func (s *Session) onTransferPending(body []byte) error {
	m, err := game.ParseTransferPending(body)
	if err != nil {
		return err
	}
	s.emit(events.Event{Kind: events.KindSession, Data: map[string]any{"state": "transfer_pending", "map": m}})
	return nil
}

// onNewWorld finishes a far teleport. The units of the old map are gone, the selection
// with them.
func (s *Session) onNewWorld(body []byte) error {
	nw, err := game.ParseNewWorld(body)
	if err != nil {
		return err
	}
	s.stopWalk(errNewMap, false)
	s.mu.Lock()
	s.mapID, s.pos = nw.Map, [4]float32{nw.X, nw.Y, nw.Z, nw.O}
	s.target = 0
	self := s.self.GUID
	s.mu.Unlock()
	s.store.Clear()
	if err := s.conn.WritePacket(opcode.MSGMoveWorldportAck, nil); err != nil { // MovementHandler.cpp:43-49
		return &fatalError{err}
	}
	if err := s.conn.WritePacket(opcode.CMSGSetActiveMover, packet.NewWriter().GUID(self).Bytes()); err != nil {
		return &fatalError{err}
	}
	s.emit(events.Event{Kind: events.KindSession, Data: map[string]any{"state": "new_world", "map": nw.Map, "x": nw.X, "y": nw.Y, "z": nw.Z}})
	s.teleported()
	return nil
}

// speedHandler acknowledges a forced speed change with its own counter and the speed it
// carried. A new run speed re-times a running walk from where it has got to.
func (s *Session) speedHandler(op, ack uint16) handler {
	return func(body []byte) error {
		c, err := game.ParseMovementChange(body, true)
		if err != nil {
			return err
		}
		self := s.selfGUID()
		if c.GUID != self {
			return nil
		}
		now := time.Now()
		s.syncWalk(now)
		if op == opcode.SMSGForceRunSpeedChange {
			s.setRunSpeed(c.Speed, now)
		}
		s.mu.Lock()
		mi := s.moveInfo(s.currentFlags())
		s.mu.Unlock()
		speed := c.Speed
		return s.conn.WritePacket(ack, game.BuildMovementChangeAck(self, c.Counter, mi, &speed))
	}
}

// setRunSpeed sets the run speed; a running walk keeps its course from where it has got
// to and takes the rest at the new speed.
func (s *Session) setRunSpeed(speed float32, now time.Time) {
	if speed <= 0 {
		return
	}
	s.runSpeed = speed
	if w := s.walk; w != nil {
		w.base, w.start, w.speed = w.walked(now), now, speed
	}
}

// seedRunSpeed takes the run speed from the player's own create block (Speeds[1],
// Object.cpp:436-441), so a walk right after login or a far teleport uses the speed the
// server already has, mounts and auras included.
func (s *Session) seedRunSpeed(blocks []objects.Block) {
	self := s.selfGUID()
	for _, b := range blocks {
		if (b.Type == objects.BlockCreate || b.Type == objects.BlockCreate2) && b.GUID == self && b.Move != nil && b.Move.Living {
			s.setRunSpeed(b.Move.Speeds[1], time.Now())
		}
	}
}

// rootHandler acknowledges SMSG_FORCE_MOVE_(UN)ROOT. A root stops a running walk first,
// with a MSG_MOVE_STOP that already carries MoveRoot (ruling T23).
func (s *Session) rootHandler(ack uint16, apply bool) handler {
	return func(body []byte) error {
		c, err := game.ParseMovementChange(body, false)
		if err != nil {
			return err
		}
		self := s.selfGUID()
		if c.GUID != self {
			return nil
		}
		s.rooted = apply
		if apply {
			s.stopWalk(errRooted, true)
		}
		s.mu.Lock()
		mi := s.moveInfo(s.currentFlags())
		s.mu.Unlock()
		return s.conn.WritePacket(ack, game.BuildMovementChangeAck(self, c.Counter, mi, nil))
	}
}

func (s *Session) onAttackStart(body []byte) error {
	a, err := game.ParseAttackStart(body)
	if err != nil {
		return err
	}
	s.emit(events.Event{Kind: events.KindAttackStart, SourceGUID: a.Attacker, TargetGUID: a.Victim})
	return nil
}

func (s *Session) onAttackStop(body []byte) error {
	a, err := game.ParseAttackStop(body)
	if err != nil {
		return err
	}
	s.emit(events.Event{Kind: events.KindAttackStop, SourceGUID: a.Attacker, TargetGUID: a.Victim})
	return nil
}
