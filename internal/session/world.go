package session

import (
	"errors"
	"fmt"
	"math"
	"strings"
	"time"

	"github.com/pjerra/turtle-agent/internal/events"
	"github.com/pjerra/turtle-agent/internal/game"
	"github.com/pjerra/turtle-agent/internal/objects"
	"github.com/pjerra/turtle-agent/internal/opcode"
	"github.com/pjerra/turtle-agent/internal/packet"
)

// moveRelays are the movement packets the server relays for other units: packed guid +
// MovementInfo (MovementHandler.cpp:414-418, MovementPacketSender.cpp:166-172).
var moveRelays = []uint16{
	opcode.MSGMoveStartForward, opcode.MSGMoveStartBackward, opcode.MSGMoveStop, opcode.MSGMoveStartStrafeLeft,
	opcode.MSGMoveStartStrafeRight, opcode.MSGMoveStopStrafe, opcode.MSGMoveJump, opcode.MSGMoveStartTurnLeft,
	opcode.MSGMoveStartTurnRight, opcode.MSGMoveStopTurn, opcode.MSGMoveSetRunMode, opcode.MSGMoveSetWalkMode,
	opcode.MSGMoveFallLand, opcode.MSGMoveStartSwim, opcode.MSGMoveStopSwim, opcode.MSGMoveSetFacing,
	opcode.MSGMoveHeartbeat, opcode.MSGMoveTeleport,
}

func (s *Session) registerWorld() {
	s.handlers[opcode.SMSGUpdateObject] = func(b []byte) error { return s.onUpdate(objects.ParseUpdate(b)) }
	s.handlers[opcode.SMSGCompressedUpdateObject] = func(b []byte) error { return s.onUpdate(objects.ParseCompressedUpdate(b)) }
	s.handlers[opcode.SMSGDestroyObject] = s.onDestroy
	s.registerGameObjects()
	s.handlers[opcode.SMSGMonsterMove] = s.onMonsterMove
	s.handlers[opcode.SMSGCompressedMoves] = s.onCompressedMoves
	for _, op := range moveRelays {
		s.handlers[op] = s.onMoveRelay
	}
}

func (s *Session) Store() *objects.Store { return s.store }

func (s *Session) selfPos() (x, y, z, o float32) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.pos[0], s.pos[1], s.pos[2], s.pos[3]
}

// Units snapshots the units around the player (not the player itself), nearest first,
// with names and party membership filled in. Ruling P7: the store is asked without a
// limit, so the name filter sees past the nearest 100; the caller's limit (0: 100) comes last.
func (s *Session) Units(f objects.Filter) []objects.Unit {
	s.mu.Lock()
	x, y, z, self := s.pos[0], s.pos[1], s.pos[2], s.self.GUID
	s.mu.Unlock()
	name := strings.ToLower(f.Name)
	limit := f.Limit
	if limit <= 0 {
		limit = objects.DefaultLimit
	}
	f.Name, f.Limit = "", math.MaxInt
	var out []objects.Unit
	for _, u := range s.store.Units(f, x, y, z) {
		if u.GUID == self {
			continue
		}
		u.Name, _ = s.names.lookup(u.GUID, u.Entry)
		if name != "" && !strings.Contains(strings.ToLower(u.Name), name) {
			continue
		}
		u.InParty = s.inParty(u.GUID)
		out = append(out, u)
		if len(out) == limit {
			break
		}
	}
	return out
}

// Unit is one unit, as Units gives it, by guid (the player itself too) or by name: the
// nearest living unit whose name contains it, else the nearest such corpse; the player's
// own name gives the player, at the session's own position (status x/y/z).
func (s *Session) Unit(g packet.GUID, name string) (objects.Unit, error) {
	if err := s.inWorld(); err != nil {
		return objects.Unit{}, err
	}
	s.mu.Lock()
	self := s.self
	s.mu.Unlock()
	if g == 0 && name != "" && strings.EqualFold(name, self.Name) {
		g = self.GUID
	}
	g, err := s.pickUnit("unit", g, name)
	if err != nil {
		return objects.Unit{}, err
	}
	x, y, z, o := s.selfPos()
	u, ok := s.store.Get(g, x, y, z)
	if !ok {
		return objects.Unit{}, fmt.Errorf("unit %v is not in view", g)
	}
	if g == self.GUID {
		// The server never moves the player's own object after its create block; the
		// session knows where the player is.
		u.X, u.Y, u.Z, u.O, u.PosAgeMS, u.Moving, u.Distance, u.Distance2D = x, y, z, o, 0, false, 0, 0
		u.GCDMS = s.spellGCDMS()
	}
	u.Name, _ = s.cachedName(g)
	u.InParty = s.inParty(g)
	return u, nil
}

// onUpdate applies the blocks parsed before an error, then reports the error.
func (s *Session) onUpdate(blocks []objects.Block, err error) error {
	s.applyBlocks(blocks)
	return err
}

// applyBlocks updates the store and turns its changes into events: health reaching 0 is
// a death, an aura slot losing or gaining a spell an aura_removed or aura_applied.
func (s *Session) applyBlocks(blocks []objects.Block) {
	s.seedRunSpeed(blocks)
	changes := s.store.Apply(blocks)
	self := s.selfGUID()
	var selfAuras []uint32 // positive auras the player gained in this update: a mount's is among them
	for _, c := range changes {
		if c.GUID == self && c.Kind == objects.ChangeAura && c.New != 0 && c.Slot < objects.PositiveAuraSlots {
			selfAuras = append(selfAuras, c.New)
		}
	}
	for _, c := range changes {
		if c.GUID == self {
			s.selfChanges(c, selfAuras)
		}
		switch c.Kind {
		case objects.ChangeCreated:
			if c.GUID.IsPlayer() || c.GUID.IsCreature() || c.GUID.IsGameObject() {
				s.nameOf(c.GUID) // warm the name cache
			}
		case objects.ChangeGOState, objects.ChangeGOFlags, objects.ChangeGODynFlags:
			s.emitGameObjectChange(c)
		case objects.ChangeHealth:
			if c.Old > 0 && c.New == 0 {
				s.emit(events.Event{Kind: events.KindDeath, SourceGUID: c.GUID})
			}
		case objects.ChangeAura:
			// Slots 32-47 hold the negative auras (SpellAuraDefines.h:25-26).
			harmful := c.Slot >= objects.PositiveAuraSlots
			if c.Old != 0 {
				s.emit(events.Event{Kind: events.KindAuraRemoved, TargetGUID: c.GUID, SpellID: c.Old,
					Data: map[string]any{"slot": c.Slot, "harmful": harmful}})
			}
			if c.New != 0 {
				s.emit(events.Event{Kind: events.KindAuraApplied, TargetGUID: c.GUID, SpellID: c.New,
					Data: map[string]any{"slot": c.Slot, "harmful": harmful}})
			}
		}
	}
}

func (s *Session) onDestroy(body []byte) error {
	r := packet.NewReader(body)
	g := r.GUID() // Object.cpp:410-411
	if err := r.Err(); err != nil {
		return err
	}
	if s.store.IsGameObject(g) {
		s.emitGameObject(g, "destroyed", nil)
	}
	s.store.Destroy(g)
	return nil
}

// onMonsterMove puts the unit at the spline's destination. A spline on the player itself
// (fear, charm, a scripted move) moves the player's own position too, so the next walk or
// facing starts where the server has it, and walk_to waits until the spline has run.
func (s *Session) onMonsterMove(body []byte) error {
	m, err := objects.ParseMonsterMove(body)
	if err != nil {
		return err
	}
	s.store.MoveSpline(m)
	if m.GUID == s.selfGUID() {
		s.serverMoved(m.Dest)
		s.splineUntil = time.Time{}
		if !m.Stop {
			s.splineUntil = s.now().Add(time.Duration(m.Duration) * time.Millisecond)
		}
	}
	return nil
}

// serverMoved puts the player where the server moved it, ending a running walk without a
// stop packet and a knockback flight without a landing: the server owns the movement now,
// and its position settles a z the agent could only guess. Live, that is a spline on the
// player (fear, charge) or the rare heartbeat the server sends the player itself
// (Unit::SendHeartBeat(true), Unit.cpp:537-549, which carries back the agent's own last z, so
// it settles nothing new); the core never sends the player its own movement block, nor relays
// its own MSG_MOVE_* back. The position is stored before the walk ends, so a walk_to that
// returns "moved by the server" already sees it in status.
func (s *Session) serverMoved(p [3]float32) {
	s.serverPosition(p, true)
	s.stopWalk(errServerMoved, false)
}

// onCompressedMoves runs each bundled packet through the handler table.
func (s *Session) onCompressedMoves(body []byte) error {
	subs, err := objects.SplitCompressedMoves(body)
	var errs []error
	if err != nil {
		errs = append(errs, err)
	}
	for _, sp := range subs {
		h, ok := s.handlers[sp.Op]
		if !ok {
			s.mu.Lock()
			s.unknown[sp.Op]++
			s.mu.Unlock()
			continue
		}
		if err := h(sp.Body); err != nil {
			errs = append(errs, err)
		}
	}
	return errors.Join(errs...)
}

func (s *Session) onMoveRelay(body []byte) error {
	r := packet.NewReader(body)
	g := r.PackedGUID()
	mi := game.ReadMoveInfo(r)
	if err := r.Err(); err != nil {
		return err
	}
	s.store.MoveTo(g, mi.X, mi.Y, mi.Z, mi.O)
	if g == s.selfGUID() {
		s.serverMoved([3]float32{mi.X, mi.Y, mi.Z})
	}
	return nil
}
