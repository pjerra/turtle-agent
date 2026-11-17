package objects

import "github.com/TortoiseBots/turtle-agent/internal/packet"

// PlayerCoinage is PLAYER_FIELD_COINAGE (Objects/UpdateFields.h:238): the owner's copper.
// Its index, 1176, was read off the enum by compiling the header.
const PlayerCoinage uint16 = 1176

// Money is player's copper from its own update fields; ok is false until the player's
// object is in the store.
func (s *Store) Money(player packet.GUID) (copper uint32, ok bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	p, ok := s.objs[player]
	if !ok {
		return 0, false
	}
	return p.values[PlayerCoinage], true
}
