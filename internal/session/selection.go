package session

import (
	"context"
	"fmt"
	"time"

	"github.com/pjerra/turtle-agent/internal/game"
	"github.com/pjerra/turtle-agent/internal/objects"
	"github.com/pjerra/turtle-agent/internal/opcode"
	"github.com/pjerra/turtle-agent/internal/packet"
)

// The selection log ties the server's echo of the selection (UNIT_FIELD_TARGET of the player's own object) to the CMSG_SET_SELECTION packets
// the session sent, so that a stale echo cannot confirm a selection that is still on its way (issue #296: an echo showing the lead from an
// earlier select was taken for the new one while a stranger's select, sent in between, was what the server had in force when the GM line ran).
//
// What a GM command acts on is the player's selection GUID (Player::GetSelectionGuid, Chat.cpp:2334), which only CMSG_SET_SELECTION sets. The
// field the session can see is UNIT_FIELD_TARGET: the core writes it on that packet too, but ALSO without touching the selection GUID when
// the player starts or stops attacking (Unit::Attack Unit.cpp:5189 writes the victim, Unit::AttackStop :5257 writes 0, also through
// RemoveAllAttackers on the victim's death) and around a stun (SpellAuras.cpp:3359 writes 0, :3412 the victim again). So the field is evidence
// of the selection only where it answers a select the session sent:
//   - the core applies the selections of one session in the order they were sent, and an update that carries the field shows it after some
//     prefix of them (several applied in one map update give one echo, and a select that does not change the field gives none);
//   - an echo of a value the session never sent (a combat or stun rewrite, a create block) leaves the selection in force UNKNOWN; the
//     sends after the lower bound stay pending.
// What is left: a combat rewrite to exactly the unit of a pending select reads as that select's echo.
//
// selLog[0] is the selection known to be in force at a lower bound of the server's progress through the sent selections, known only when an
// echo answered one of the session's own sends (selBaseKnown); selLog[1:] are the selections sent since, any of which may still be pending.
// An echo of v moves the lower bound to the FIRST pending entry that is v (the oldest send it can answer), and the selection g is confirmed
// only when every entry from there on is g: whichever of them the server has reached, g is in force, and stays in force until another
// selection is sent. Selecting g again while g is confirmed is confirmed at once, with no echo needed (the core sends none). Where an echo
// of a new select could not be told from an older one, or would not come at all because the field already shows the unit, SelectConfirmed
// first makes and confirms a neutral selection.

// selCap bounds the log: the selections sent with no echo between them. Past it the oldest are dropped, the selection in force becomes
// unknown, and an echo of a dropped unit confirms nothing until an echo of a later send was seen.
const selCap = 32

// selSentLocked records a CMSG_SET_SELECTION of g written to the server. The caller holds s.mu.
func (s *Session) selSentLocked(g packet.GUID) {
	if len(s.selLog) == 0 {
		s.selLog, s.selBaseKnown = []packet.GUID{0}, false
	}
	if n := len(s.selLog); s.selLog[n-1] == g && (n > 1 || s.selBaseKnown) {
		return // the same unit again: an echo of it answers the older send as well, so the log need not grow
	}
	s.selLog = append(s.selLog, g)
	if len(s.selLog) > selCap {
		drop := len(s.selLog) - (selCap - 1) // entries leaving the log; the new base is unknown
		if s.selDropped == nil {
			s.selDropped = map[packet.GUID]bool{}
		}
		for i, x := range s.selLog[:drop] {
			if i > 0 || s.selBaseKnown {
				s.selDropped[x] = true
			}
		}
		kept := append([]packet.GUID{0}, s.selLog[drop:]...)
		s.selLog, s.selBaseKnown = kept, false
	}
}

// selObservedLocked records that the server shows v in the player's target field. The caller holds s.mu.
func (s *Session) selObservedLocked(v packet.GUID) {
	s.selShown, s.selShownOK = v, true
	if len(s.selLog) == 0 {
		s.selLog, s.selBaseKnown = []packet.GUID{0}, false // nothing sent: the field says nothing about the selection
		return
	}
	start := 1
	if s.selBaseKnown {
		start = 0
	}
	for i := start; i < len(s.selLog); i++ {
		if s.selLog[i] != v {
			continue
		}
		if s.selDropped[v] {
			// It may answer a dropped send, with everything after it pending.
			s.selBaseKnown = false
			return
		}
		s.selLog, s.selBaseKnown = s.selLog[i:], true
		if i > 0 {
			s.selDropped = nil // a kept send was applied, so every dropped one was too
		}
		return
	}
	// A value no pending send holds: the core rewrote the field itself (combat, a stun, a create block). The selection in force is unknown.
	s.selBaseKnown = false
}

// selUnknownLocked forgets the selection in force (a far teleport replaces the player's object); the selections sent stay pending. The
// caller holds s.mu.
func (s *Session) selUnknownLocked() {
	if len(s.selLog) == 0 {
		s.selLog = []packet.GUID{0}
	}
	s.selBaseKnown, s.selShownOK = false, false
}

func (s *Session) selConfirmedLocked(g packet.GUID) bool {
	if !s.selBaseKnown || len(s.selLog) == 0 {
		return false
	}
	for _, x := range s.selLog {
		if x != g {
			return false
		}
	}
	return true
}

// selNeedsNeutralLocked says whether a new select of g could stay unconfirmed: its echo could not be told from an older one (g was sent,
// then something else, and the server has not shown how far it got), or no echo would come (the selection in force is unknown and the field
// already shows g, or the field is not known).
func (s *Session) selNeedsNeutralLocked(g packet.GUID) bool {
	if s.selConfirmedLocked(g) {
		return false
	}
	if !s.selBaseKnown && (!s.selShownOK || s.selShown == g) {
		return true
	}
	seen := false
	for i, x := range s.selLog {
		if i == 0 && !s.selBaseKnown {
			continue
		}
		if x == g {
			seen = true
		} else if seen {
			return true
		}
	}
	return false
}

// selNeutralLocked is a selection whose echo answers its own send alone: one no entry of the log (or a dropped one) holds and the field does
// not show, so the field must change. No selection (0) or else the player itself; ok is false when neither will do.
func (s *Session) selNeutralLocked() (packet.GUID, bool) {
	for _, c := range []packet.GUID{0, s.self.GUID} {
		if s.selDropped[c] || (s.selShownOK && s.selShown == c) {
			continue
		}
		in := false
		for _, x := range s.selLog {
			if x == c {
				in = true
				break
			}
		}
		if !in {
			return c, true
		}
	}
	return 0, false
}

// SelectionConfirmed says whether the server is known to have the unit g (0: none) in force as the player's selection: an echo that can only
// answer one of the session's own selects of g, sent no earlier than its last select of anything else, showed it. A stale echo, or the
// field rewritten by combat or a stun, does not confirm it.
func (s *Session) SelectionConfirmed(g packet.GUID) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.selConfirmedLocked(g)
}

// SelectionAmbiguous says whether SelectConfirmed would make a neutral selection before selecting g.
func (s *Session) SelectionAmbiguous(g packet.GUID) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.selNeedsNeutralLocked(g)
}

// sendSelection writes CMSG_SET_SELECTION of g (0: none) from the owner goroutine and records it.
func (s *Session) sendSelection(ctx context.Context, g packet.GUID) error {
	_, err := s.do(ctx, func() (any, error) {
		if err := s.inWorld(); err != nil {
			return nil, err
		}
		if err := s.conn.WritePacket(opcode.CMSGSetSelection, game.BuildSetSelection(g)); err != nil {
			return nil, err
		}
		s.mu.Lock()
		s.target = g
		s.selSentLocked(g)
		s.mu.Unlock()
		return nil, nil
	})
	return err
}

// SelectConfirmed selects the unit g (0: none) and waits, up to wait in all, until the server is known to have it in force
// (SelectionConfirmed). When a new select of g could stay unconfirmed (SelectionAmbiguous), a neutral selection (none, or the player itself)
// is made and confirmed first. An error when the confirmation does not come in time; a line that acts on the selection must not be sent then.
func (s *Session) SelectConfirmed(ctx context.Context, g packet.GUID, wait time.Duration) error {
	timer := time.NewTimer(wait)
	defer timer.Stop()
	s.mu.Lock()
	need := s.selNeedsNeutralLocked(g)
	n, ok := s.selNeutralLocked()
	s.mu.Unlock()
	if need && ok {
		if err := s.sendSelection(ctx, n); err != nil {
			return err
		}
		if err := s.awaitSelection(ctx, n, wait, timer.C); err != nil {
			return fmt.Errorf("%w (the neutral selection made before %v)", err, g)
		}
	}
	if err := s.sendSelection(ctx, g); err != nil {
		return err
	}
	return s.awaitSelection(ctx, g, wait, timer.C)
}

func (s *Session) awaitSelection(ctx context.Context, g packet.GUID, wait time.Duration, expired <-chan time.Time) error {
	tick := time.NewTicker(10 * time.Millisecond)
	defer tick.Stop()
	for {
		if s.SelectionConfirmed(g) {
			return nil
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-expired:
			sel, _ := s.ServerSelection()
			return fmt.Errorf("the server did not show %v as the agent's selection within %s (it shows %v): not sending a line that acts on the selection", g, wait, sel)
		case <-tick.C:
		}
	}
}

// observeSelection records the target field the server shows when one of blocks replaced the player's object or carried the field. Run
// after the store applied them; the store's value is the last of them.
func (s *Session) observeSelection(blocks []objects.Block, self packet.GUID) {
	touched := false
	for _, b := range blocks {
		if b.GUID != self || self == 0 {
			continue
		}
		switch b.Type {
		case objects.BlockCreate, objects.BlockCreate2:
			touched = true
		case objects.BlockValues:
			_, lo := b.Values[objects.UnitTarget]
			_, hi := b.Values[objects.UnitTarget+1]
			touched = touched || lo || hi
		}
	}
	if !touched {
		return
	}
	u, ok := s.store.Get(self, 0, 0, 0)
	if !ok {
		return
	}
	s.mu.Lock()
	s.selObservedLocked(u.Target)
	s.mu.Unlock()
}
