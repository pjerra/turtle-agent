package session

import (
	"errors"
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

// onUpdate applies the blocks parsed before an error, then reports the error.
func (s *Session) onUpdate(blocks []objects.Block, err error) error {
	s.applyBlocks(blocks)
	return err
}

// applyBlocks updates the store and turns its changes into events: health reaching 0 is
// a death, an aura slot losing or gaining a spell an aura_removed or aura_applied.
func (s *Session) applyBlocks(blocks []objects.Block) {
	s.seedRunSpeed(blocks)
	for _, c := range s.store.Apply(blocks) {
		switch c.Kind {
		case objects.ChangeCreated:
			if c.GUID.IsPlayer() || c.GUID.IsCreature() {
				s.nameOf(c.GUID) // warm the name cache
			}
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
	s.store.MoveSpline(m.GUID, m.Dest[0], m.Dest[1], m.Dest[2], m.Stop)
	if m.GUID == s.selfGUID() {
		s.serverMoved(m.Dest)
		s.splineUntil = time.Time{}
		if !m.Stop {
			s.splineUntil = time.Now().Add(time.Duration(m.Duration) * time.Millisecond)
		}
	}
	return nil
}

// serverMoved puts the player where the server moved it, ending a running walk without a
// stop packet: the server owns the movement now.
func (s *Session) serverMoved(p [3]float32) {
	s.stopWalk(errServerMoved, false)
	s.mu.Lock()
	s.pos[0], s.pos[1], s.pos[2] = p[0], p[1], p[2]
	s.mu.Unlock()
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
