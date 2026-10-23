package session

import (
	"github.com/pjerra/turtle-agent/internal/objects"
	"github.com/pjerra/turtle-agent/internal/packet"
)

// The selection log ties the server's echo of the selection (UNIT_FIELD_TARGET of the player's own object) to the CMSG_SET_SELECTION packets
// the session sent, so that a stale echo cannot confirm a selection that is still on its way (issue #296: an echo showing the lead from an
// earlier select was taken for the new one while a stranger's select, sent in between, was what the server had in force when the GM line ran).
//
// What the session relies on: the core applies the selections of one session in the order they were sent, and an update that carries the
// target field shows the selection in force after some prefix of them (several applied in one map update give one echo, and a selection that
// does not change the field gives none). The core sets a player's selection only from CMSG_SET_SELECTION, except when it replaces the whole
// object (a create block), which is handled as an echo of a value it may have set itself.
//
// selLog[0] is the selection known to be in force at a lower bound of the server's progress through the sent selections (selBaseKnown says
// whether it is known at all: not before the first create block of the player, nor after a far teleport); selLog[1:] are the selections sent
// since, any of which may still be pending. An echo of v moves the lower bound to the FIRST entry that is v (the oldest send it can answer),
// and the selection g is confirmed only when every entry from there on is g: whichever of them the server has reached, g is in force, and
// stays in force until another selection is sent. A selection sent again without change is confirmed at once, with no echo needed.

// selSentLocked records a CMSG_SET_SELECTION of g written to the server. The caller holds s.mu.
func (s *Session) selSentLocked(g packet.GUID) {
	if len(s.selLog) == 0 {
		s.selLog = []packet.GUID{0}
		s.selBaseKnown = false
	}
	if n := len(s.selLog); s.selLog[n-1] == g && (n > 1 || s.selBaseKnown) {
		return // the same unit again: an echo of it answers the older send as well, so the log need not grow
	}
	s.selLog = append(s.selLog, g)
}

// selObservedLocked records that the server shows v as the player's selection. The caller holds s.mu.
func (s *Session) selObservedLocked(v packet.GUID) {
	if len(s.selLog) == 0 {
		s.selLog, s.selBaseKnown = []packet.GUID{v}, true
		return
	}
	if !s.selBaseKnown {
		// The value at the lower bound was unknown, so the echo may be it: nothing sent since is known to be applied.
		s.selLog[0], s.selBaseKnown = v, true
		return
	}
	for i, x := range s.selLog {
		if x == v {
			s.selLog = s.selLog[i:]
			return
		}
	}
	// A value the session never sent: the server set it (a create block). It is in force now; what was sent since the lower bound may
	// still be pending.
	s.selLog[0] = v
}

// selUnknownLocked forgets the selection in force (a far teleport replaces the player's object); the selections sent stay pending. The
// caller holds s.mu.
func (s *Session) selUnknownLocked() {
	if len(s.selLog) == 0 {
		s.selLog = []packet.GUID{0}
	}
	s.selBaseKnown = false
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

// SelectionConfirmed says whether the server is known to have the unit g in force as the player's selection: it showed g in an echo that
// can only answer a selection of g sent no earlier than the last selection of anything else. A stale echo (one showing g from before another
// unit was selected) does not confirm it.
func (s *Session) SelectionConfirmed(g packet.GUID) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.selConfirmedLocked(g)
}

// SelectionAmbiguous says whether an echo of a new selection of g could not be told from an older one: g was sent, and something else after
// it, and the server has not shown how far it got. Selecting another unit first (SelectionNeutral) and waiting for it to be confirmed
// removes the doubt; without that the core, which may apply several selections in one update and echo only the last, could leave g
// unconfirmable.
func (s *Session) SelectionAmbiguous(g packet.GUID) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
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

// SelectionNeutral is a selection that no entry of the log holds, so its echo answers that send alone: no selection (0) or else the player
// itself. ok is false when the log holds both.
func (s *Session) SelectionNeutral() (g packet.GUID, ok bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, c := range []packet.GUID{0, s.self.GUID} {
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

// observeSelection records the selection the server shows when one of blocks replaced the player's object or carried its target field.
// Run after the store applied them; the store's value is the last of them.
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
