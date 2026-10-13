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
	// maxWalkTime keeps a slow walk inside the 35 s an RPC client waits for walk_to
	// (wow-play-harness HeadlessClient); a 40-yard walk at the 7 yd/s run speed takes 5.7 s.
	maxWalkTime = 30 * time.Second
)

// Why a walk ended early. A server move, a same-map teleport, a knockback and a root
// interrupt a walk_path, which waits and plans again; the others end it.
var (
	errServerMoved = errors.New("moved by the server")
	errTeleported  = errors.New("teleported by the server")
	errNewMap      = errors.New("teleported to another map")
	errRooted      = errors.New("rooted by the server; walk stopped")
	errRootedNow   = errors.New("rooted; walk_to waits until the server releases the root")
	errKnockedBack = errors.New("knocked back by the server; walk stopped")
	errAirborne    = errors.New("in the air (a knockback or a fall)")
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
	sent     float64 // walked when the last walk packet was sent: where an aborted walk stops
	nextBeat time.Time
	done     chan error
	onMesh   bool  // the end is a navmesh floor point: arriving there settles z
	ends     []int // index in pts where each leg (corner to corner) ends; the last is len(pts)-1
	leg      int   // the leg w.o faces along
}

func newWalk(pts [][3]float32, speed float32, now time.Time, done chan error) *walkState {
	w := &walkState{pts: pts, cum: make([]float64, len(pts)), to: pts[len(pts)-1], start: now, speed: speed,
		nextBeat: now.Add(500 * time.Millisecond), done: done}
	for i := 1; i < len(pts); i++ {
		w.cum[i] = w.cum[i-1] + dist3f(pts[i-1], pts[i])
	}
	w.setCorners(nil)
	return w
}

func dist3f(a, b [3]float32) float64 {
	dx, dy, dz := float64(b[0]-a[0]), float64(b[1]-a[1]), float64(b[2]-a[2])
	return math.Sqrt(dx*dx + dy*dy + dz*dz)
}

// length is the polyline's 3D length, duration the time it takes at speed.
func (w *walkState) length() float64 { return w.cum[len(w.cum)-1] }

func (w *walkState) duration() time.Duration {
	return time.Duration(w.length() / float64(w.speed) * float64(time.Second))
}

// walked is the 3D distance covered at now, at most the whole polyline.
func (w *walkState) walked(now time.Time) float64 {
	return min(w.base+float64(w.speed)*now.Sub(w.start).Seconds(), w.length())
}

func (w *walkState) arrived(now time.Time) bool { return w.walked(now) >= w.length()-1e-6 }

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

// clampZ holds a height within speed*dt of the last movement packet sent (s.last), dt
// counted to ctime, the client time the packet will carry: a backstop under the polyline
// timing, for a ground sample that snapped to another floor. checkStep still judges the
// packet.
func clampZ(z float32, last lastMove, ctime uint32, speed float32) float32 {
	dt := float32(0)
	if ctime > last.ctime {
		dt = float32(ctime-last.ctime) / 1000
	}
	lim := speed * dt
	return min(max(z, last.pos[2]-lim), last.pos[2]+lim)
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
	s.handlers[opcode.SMSGMoveKnockBack] = s.onKnockBack
	for op, o := range toggleOrders {
		s.handlers[op] = s.toggleHandler(o)
	}
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

// clockAt is the MovementInfo client time at now: moveClock's milliseconds (real time, slewed
// to the wall clock so it keeps the server's pace), plus one, because the anticheat flags a
// zero time and a clock that runs backwards or ahead of the server's (Movement.cpp:377,
// :434-475). It never goes back, even for a now read before the last packet's. Owner loop only.
func (s *Session) clockAt(now time.Time) uint32 {
	s.mclock = s.mclock.at(s.clockReading(now))
	c := max(uint32(s.mclock.ms)+1, s.lastClock)
	s.lastClock = c
	return c
}

func (s *Session) clock() uint32 { return s.clockAt(s.now()) }

// peekClock is the client time clockAt(now) will give, without taking it.
func (s *Session) peekClock(now time.Time) uint32 {
	return max(uint32(s.mclock.at(s.clockReading(now)).ms)+1, s.lastClock)
}

// moveInfoAt is the player's MovementInfo at now, its client time read from the same
// instant as the position, so a step never covers more ground than its time gap allows. In
// the air it carries the knockback's jump values and the time fallen, and on the way down
// MoveFallingFar too. The caller holds s.mu.
func (s *Session) moveInfoAt(flags uint32, now time.Time) game.MoveInfo {
	m := game.MoveInfo{Flags: flags, Time: s.clockAt(now), X: s.pos[0], Y: s.pos[1], Z: s.pos[2], O: s.pos[3]}
	if f := s.fall; f != nil && flags&game.MoveJumping != 0 {
		if f.descending(now) {
			m.Flags |= game.MoveFallingFar
		}
		m.FallTime = uint32(max(now.Sub(f.began), 0).Milliseconds())
		m.JumpZSpeed, m.JumpCos, m.JumpSin, m.JumpXYSpeed = f.kb.ZSpeed, f.kb.Cos, f.kb.Sin, f.kb.XYSpeed
	}
	return m
}

// currentFlags are the movement flags the player has now. MoveRoot stays on while rooted,
// or the anticheat records ROOT_MOVE (Movement.cpp:186-204); water walking, hover and
// feather fall stay on while the server has them applied.
func (s *Session) currentFlags() uint32 {
	f := s.toggles
	if s.walk != nil {
		f |= game.MoveForward
	}
	if s.fall != nil {
		f |= game.MoveJumping
	}
	if s.rooted {
		f |= game.MoveRoot
	}
	return f
}

// syncWalk moves s.pos to where the running walk or flight is at now.
func (s *Session) syncWalk(now time.Time) {
	s.syncFall(now)
	if s.walk == nil {
		return
	}
	p := s.walk.at(now)
	p[2] = clampZ(p[2], s.last, s.peekClock(now), s.walk.speed)
	s.mu.Lock()
	s.pos[0], s.pos[1], s.pos[2] = p[0], p[1], p[2]
	s.mu.Unlock()
}

// airborne refuses an action the player cannot take in the air.
func (s *Session) airborne(what string) error {
	if f := s.fall; f != nil {
		return fmt.Errorf("in the air (a knockback or a fall); %s waits for the landing in %.1f s", what, time.Until(f.land).Seconds())
	}
	return nil
}

// Target selects a unit by guid, or by name, and returns it. A name picks the nearest
// living match; a corpse (the last run's kill) only when no living one is in view, and the
// returned unit then says dead. A guid selects exactly that unit, dead or alive.
func (s *Session) Target(ctx context.Context, g packet.GUID, name string) (objects.Unit, error) {
	g, err := s.pickUnit("target", g, name)
	if err != nil {
		return objects.Unit{}, err
	}
	_, err = s.do(ctx, func() (any, error) {
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

// pickUnit returns g, or for a name the nearest living unit whose name contains it, or
// the nearest such corpse when none lives. verb names the caller in errors.
func (s *Session) pickUnit(verb string, g packet.GUID, name string) (packet.GUID, error) {
	if g != 0 {
		return g, nil
	}
	if name == "" {
		return 0, fmt.Errorf("%s needs a guid or a name", verb)
	}
	us := s.Units(objects.Filter{Name: name, Limit: math.MaxInt})
	if len(us) == 0 {
		return 0, fmt.Errorf("no unit named %q nearby", name)
	}
	for _, u := range us {
		if !u.Dead {
			return u.GUID, nil
		}
	}
	return us[0].GUID, nil
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

// Stand stands the player up (CMSG_STANDSTATECHANGE, state 0), as a real client does when a sitting player clicks or casts: the core
// refuses a cast from a seated player (SPELL_FAILED_NOT_STANDING) and does not stand the player by itself.
func (s *Session) Stand(ctx context.Context) error {
	return s.send(ctx, opcode.CMSGStandstatechange, game.BuildStandStateChange(0))
}

func (s *Session) StopAttack(ctx context.Context) error {
	return s.send(ctx, opcode.CMSGAttackstop, nil)
}

// CancelAutoRepeat stops the player's auto-repeat spell (Auto Shot, Shoot): CMSG_CANCEL_AUTO_REPEAT_SPELL, an empty body, which the core
// answers with InterruptSpell(CURRENT_AUTOREPEAT_SPELL) (SpellHandler.cpp HandleCancelAutoRepeatSpellOpcode). A real client sends it when
// the player toggles Auto Shot off.
func (s *Session) CancelAutoRepeat(ctx context.Context) error {
	return s.send(ctx, opcode.CMSGCancelAutoRepeatSpell, nil)
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

// CancelAura removes one of the player's own auras, as a right-click on the buff does (CMSG_CANCEL_AURA, SpellHandler.cpp
// HandleCancelAuraOpcode): a druid leaves its shapeshift form with it, a mount aura is the same packet (Dismount). The server refuses
// an aura that cannot be cancelled and a harmful one without a word.
func (s *Session) CancelAura(ctx context.Context, spell uint32) error {
	if spell == 0 {
		return errors.New("cancel_aura needs a spell id")
	}
	return s.send(ctx, opcode.CMSGCancelAura, game.BuildCancelAura(spell))
}

// mainHandSlot is the inventory slot of the main-hand weapon (EQUIPMENT_SLOT_MAINHAND, 1.12).
const mainHandSlot = 15

// CastOnMainHand casts an enchanting spell (a shaman's weapon imbue) at the weapon in the main hand: CMSG_CAST_SPELL with
// TARGET_FLAG_ITEM and the weapon's guid, as the client sends it for a spell that needs an item. The server answers with the
// enchantment or a spell failure (item gone) when the hand is empty.
func (s *Session) CastOnMainHand(ctx context.Context, spell uint32) error {
	if spell == 0 {
		return errors.New("cast needs a spell id")
	}
	_, err := s.do(ctx, func() (any, error) {
		if err := s.inWorld(); err != nil {
			return nil, err
		}
		it, ok := s.store.ItemAt(s.selfGUID(), objects.BackpackBag, mainHandSlot)
		if !ok {
			return nil, errors.New("no weapon in the main hand")
		}
		return nil, s.conn.WritePacket(opcode.CMSGCastSpell, game.BuildCastSpellOnItem(spell, it.GUID))
	})
	return err
}

// CancelCast cancels the cast, channel or auto-repeat of spell, or of any spell when spell
// is 0 (CMSG_CANCEL_CAST, SpellHandler.cpp:411-426, Object.cpp:5428-5440).
func (s *Session) CancelCast(ctx context.Context, spell uint32) error {
	return s.send(ctx, opcode.CMSGCancelCast, game.BuildCancelCast(spell))
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
		if err := s.airborne("face"); err != nil {
			return nil, err
		}
		s.mu.Lock()
		s.pos[3] = o
		mi := s.moveInfoAt(s.currentFlags(), s.now())
		s.mu.Unlock()
		if err := s.checkStep(mi); err != nil {
			return nil, fmt.Errorf("face refused: %w", err)
		}
		return nil, s.sendMove(opcode.MSGMoveSetFacing, mi, game.BuildMovement(mi))
	})
	return err
}

// WalkTo walks in a straight line to a point at most maxWalk yards away and returns on
// arrival. The walk is timed on the 3D distance, so it climbs or drops no faster than the
// run speed, the bound the anticheat holds a z change to (Movement.cpp:885-887, OVERSPEED_Z
// at :593-595); every step is checked against that bound before it is sent.
func (s *Session) WalkTo(ctx context.Context, x, y, z float32) error {
	return s.walkStraight(ctx, [3]float32{x, y, z}, walkOpts{limit: maxWalk, maxTime: maxWalkTime})
}

// walkOpts shape one straight walk: limit (yards) and maxTime refuse a walk too long for
// its caller (0: no bound); via are the ground points on the way (a navmesh leg's samples),
// walked through in order; corners are the indexes in via where the walk turns (a
// walk_path's corners; see tickTurn); onMesh says the end is a navmesh floor point.
type walkOpts struct {
	limit   float64
	maxTime time.Duration
	via     [][3]float32
	corners []int
	onMesh  bool
}

// sentinelErr reads as err and also matches is with errors.Is.
type sentinelErr struct{ err, is error }

func (e sentinelErr) Error() string   { return e.err.Error() }
func (e sentinelErr) Unwrap() []error { return []error{e.err, e.is} }

// walkStraight is one walk to `to`: a straight line for walk_to, a whole planned path,
// corners and all, for walk_path. It returns on arrival, or with the reason the walk ended
// early.
func (s *Session) walkStraight(ctx context.Context, to [3]float32, o walkOpts) error {
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
		if err := s.airborne("walk_to"); err != nil {
			return nil, sentinelErr{err, errAirborne}
		}
		s.mu.Lock()
		from := [3]float32{s.pos[0], s.pos[1], s.pos[2]}
		s.mu.Unlock()
		dx, dy, dz := float64(x-from[0]), float64(y-from[1]), float64(z-from[2])
		dist := math.Sqrt(dx*dx + dy*dy + dz*dz)
		if o.limit > 0 && dist > o.limit {
			return nil, fmt.Errorf("walk_to is for short moves: %.1f yards is over %g, use walk_path or teleport", dist, o.limit)
		}
		done := make(chan error, 1)
		now := s.now()
		pts := append(append([][3]float32{from}, o.via...), [3]float32{x, y, z})
		w := newWalk(pts, s.runSpeed, now, done)
		if w.length() < 0.1 {
			done <- nil
			return done, nil
		}
		w.onMesh = o.onMesh
		w.setCorners(o.corners)
		if dur := w.duration(); o.maxTime > 0 && dur > o.maxTime {
			return nil, fmt.Errorf("walk_to would take %.0f s at %.1f yd/s, over %s; walk a shorter way or teleport", dur.Seconds(), s.runSpeed, o.maxTime)
		}
		s.mu.Lock()
		s.pos[3] = w.o
		mi := s.moveInfoAt(s.currentFlags()|game.MoveForward, now)
		s.mu.Unlock()
		if err := s.checkStep(mi); err != nil {
			return nil, fmt.Errorf("walk_to refused: the start is not where the server has the player: %w", err)
		}
		if err := s.sendMove(opcode.MSGMoveStartForward, mi, game.BuildMovement(mi)); err != nil {
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

// tickWalk advances a walk: heartbeat every 500 ms, a facing at each corner, stop at the
// destination. It reads the
// clock once, not the tick's send time: a tick read late from a busy loop would place the
// heartbeat behind the time it carries, and the next step would exceed the run speed.
func (s *Session) tickWalk() error {
	w := s.walk
	if w == nil {
		return nil
	}
	now := s.now()
	if turned, err := s.tickTurn(w, now); turned {
		return err
	}
	if w.arrived(now) {
		s.walk = nil
		z := clampZ(w.to[2], s.last, s.peekClock(now), w.speed)
		s.mu.Lock()
		s.pos = [4]float32{w.to[0], w.to[1], z, w.o}
		// A straight line that changed height may end above or below the ground; a navmesh
		// leg ends on the mesh floor, which settles it.
		if w.onMesh && z == w.to[2] {
			s.zUnknown = false
		} else {
			s.zUnknown = s.zUnknown || math.Abs(float64(w.to[2]-w.pts[0][2])) > 0.5
		}
		mi := s.moveInfoAt(s.currentFlags(), now)
		s.mu.Unlock()
		if err := s.checkStep(mi); err != nil {
			return s.abortWalk(w, err)
		}
		s.mu.Lock()
		s.walkDist = w.walked(now) // the whole path, as walk_path counts an arrival, once checkStep passes the stop
		s.mu.Unlock()
		err := s.sendMove(opcode.MSGMoveStop, mi, game.BuildMovement(mi))
		w.done <- err
		return err
	}
	if now.Before(w.nextBeat) {
		return nil
	}
	w.nextBeat = now.Add(500 * time.Millisecond)
	s.syncWalk(now)
	s.mu.Lock()
	mi := s.moveInfoAt(s.currentFlags(), now)
	s.mu.Unlock()
	if err := s.checkStep(mi); err != nil {
		s.walk = nil
		return s.abortWalk(w, err)
	}
	return s.sendWalkMove(opcode.MSGMoveHeartbeat, mi, game.BuildMovement(mi), now)
}

// abortWalk ends a walk whose next step would break the anticheat's bound: it stops where
// the server last had the player, a step of zero length, and reports why. walkDist is how far
// along the path that was: the refused step and the rest of the path are not counted. s.walk is nil.
func (s *Session) abortWalk(w *walkState, reason error) error {
	s.mu.Lock()
	s.pos[0], s.pos[1], s.pos[2] = s.last.pos[0], s.last.pos[1], s.last.pos[2]
	s.walkDist = w.sent
	mi := s.moveInfoAt(s.currentFlags(), s.now())
	s.mu.Unlock()
	err := s.sendMove(opcode.MSGMoveStop, mi, game.BuildMovement(mi))
	w.done <- fmt.Errorf("walk stopped: %w", reason)
	return err
}

// stopWalk ends a walk early; sendStop is false when the server moved the player. The
// stop is sent from where the walk has got to and carries MoveRoot while rooted. walkDist
// records how far along the path the walk got, for walk_path.
func (s *Session) stopWalk(reason error, sendStop bool) {
	w := s.walk
	if w == nil {
		return
	}
	now := s.now()
	if sendStop {
		s.syncWalk(now)
	}
	s.mu.Lock()
	s.walkDist = w.walked(now)
	s.mu.Unlock()
	s.walk = nil
	if sendStop {
		s.mu.Lock()
		mi := s.moveInfoAt(s.currentFlags(), now)
		s.mu.Unlock()
		if s.checkStep(mi) != nil {
			_ = s.abortWalk(w, reason)
			return
		}
		_ = s.sendMove(opcode.MSGMoveStop, mi, game.BuildMovement(mi))
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
	ch := v.(chan struct{})
	select {
	case <-ch:
		return nil
	case <-tctx.Done():
		s.dropTeleportWaiter(ch)
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
	s.gossip = nil // the client closes an NPC window when the player is moved away
	// A .go xyz puts the player at the given z, perhaps in the air: dropToFloor below
	// falls to the navmesh floor, else the next position from the server settles it
	// (review M13).
	s.serverPosition([3]float32{a.Move.X, a.Move.Y, a.Move.Z}, false)
	s.mu.Lock()
	s.pos[3] = a.Move.O
	s.mu.Unlock()
	s.store.MoveTo(self, a.Move.X, a.Move.Y, a.Move.Z, a.Move.O)
	s.stopWalk(errTeleported, false) // after the position, so a walk_path woken by it reads the new one
	if err := s.conn.WritePacket(opcode.MSGMoveTeleportAck, game.BuildTeleportAck(self, a.Counter, s.clock())); err != nil {
		return &fatalError{err}
	}
	s.emit(events.Event{Kind: events.KindSession, Data: map[string]any{"state": "teleported", "x": a.Move.X, "y": a.Move.Y, "z": a.Move.Z}})
	s.teleported()
	s.dropToFloor()
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
	s.gossip = nil
	s.serverPosition([3]float32{nw.X, nw.Y, nw.Z}, false)
	s.mu.Lock()
	s.mapID, s.pos = nw.Map, [4]float32{nw.X, nw.Y, nw.Z, nw.O}
	s.target = 0
	self := s.self.GUID
	s.mu.Unlock()
	s.stopWalk(errNewMap, false) // after the position, so a walk_path woken by it reads the new one
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
		now := s.now()
		s.syncWalk(now)
		if op == opcode.SMSGForceRunSpeedChange {
			s.setRunSpeed(c.Speed, now)
		}
		s.mu.Lock()
		mi := s.moveInfoAt(s.currentFlags(), now)
		s.mu.Unlock()
		speed := c.Speed
		return s.sendWalkMove(ack, mi, game.BuildMovementChangeAck(self, c.Counter, mi, &speed), now)
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

// toggleFlags are the movement flags the water-walk, hover and feather-fall orders switch.
const toggleFlags = game.MoveWaterWalking | game.MoveHover | game.MoveSafeFall

// seedRunSpeed takes the run speed (Speeds[1]) and the water-walk, hover and feather-fall
// flags from the player's own create block (Object.cpp:436-441), so movement right after
// login or a far teleport matches what the server already has, mounts and auras included.
func (s *Session) seedRunSpeed(blocks []objects.Block) {
	self := s.selfGUID()
	for _, b := range blocks {
		if (b.Type == objects.BlockCreate || b.Type == objects.BlockCreate2) && b.GUID == self && b.Move != nil && b.Move.Living {
			s.setRunSpeed(b.Move.Speeds[1], s.now())
			s.toggles = b.Move.Info.Flags & toggleFlags
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
		now := s.now()
		s.syncWalk(now)
		s.mu.Lock()
		mi := s.moveInfoAt(s.currentFlags(), now)
		s.mu.Unlock()
		return s.sendMove(ack, mi, game.BuildMovementChangeAck(self, c.Counter, mi, nil))
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
