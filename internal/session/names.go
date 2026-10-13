package session

import (
	"fmt"
	"sync"
	"time"

	"github.com/pjerra/turtle-agent/internal/events"
	"github.com/pjerra/turtle-agent/internal/game"
	"github.com/pjerra/turtle-agent/internal/opcode"
	"github.com/pjerra/turtle-agent/internal/packet"
)

// nameQueryInterval spaces repeated name or creature queries for one GUID or entry.
const nameQueryInterval = 5 * time.Second

// goKey tells a game object's entry from a creature's in the asked and silent maps, which key
// by entry: entries stay under 2^24, so bit 31 is free.
const goKey uint32 = 1 << 31

// nameCache maps player GUIDs, creature entries and game object entries to names. A creature's
// entry comes from the object store (OBJECT_FIELD_ENTRY), never from a pet's GUID bits, and a
// creature or game object whose entry is unknown (0) stays nameless and unasked until its values
// arrive. mu guards the three maps, which RPC goroutines read; the rest belongs to the owner loop.
type nameCache struct {
	mu         sync.RWMutex
	players    map[packet.GUID]string
	creatures  map[uint32]string
	objects    map[uint32]string // game object entries
	askedGUID  map[packet.GUID]time.Time
	askedEntry map[uint32]time.Time
	// silentGUID and silentEntry hold queries whose wait ran out unanswered: until the
	// time stored, the GUID or entry counts as nameless instead of stalling the queue again.
	silentGUID  map[packet.GUID]time.Time
	silentEntry map[uint32]time.Time
	waiting     []pendingEvent
}

type pendingEvent struct {
	e     events.Event
	until time.Time
	party bool // fixed when the event was emitted, not when its names arrive
}

func newNameCache() *nameCache {
	return &nameCache{
		players: map[packet.GUID]string{}, creatures: map[uint32]string{}, objects: map[uint32]string{},
		askedGUID: map[packet.GUID]time.Time{}, askedEntry: map[uint32]time.Time{},
		silentGUID: map[packet.GUID]time.Time{}, silentEntry: map[uint32]time.Time{},
	}
}

func (c *nameCache) setPlayer(g packet.GUID, name string) {
	c.mu.Lock()
	c.players[g] = name
	c.mu.Unlock()
}

func (c *nameCache) setCreature(entry uint32, name string) {
	c.mu.Lock()
	c.creatures[entry] = name
	c.mu.Unlock()
}

func (c *nameCache) setObject(entry uint32, name string) {
	c.mu.Lock()
	c.objects[entry] = name
	c.mu.Unlock()
}

// lookup returns the cached name, using entry for a creature or game object; ok is false while
// a query is owed or, for those, while the entry is unknown.
func (c *nameCache) lookup(g packet.GUID, entry uint32) (string, bool) {
	c.mu.RLock()
	defer c.mu.RUnlock()
	switch {
	case g == 0:
		return "", true
	case g.IsPlayer():
		n, ok := c.players[g]
		return n, ok
	case g.IsCreature():
		n, ok := c.creatures[entry]
		return n, ok && entry != 0
	case g.IsGameObject():
		n, ok := c.objects[entry]
		return n, ok && entry != 0
	}
	return "", true // corpses and transports have no name query in v1
}

// nameOf returns g's name and sends a name or creature query when it is unknown. A GUID
// whose last query went unanswered for a whole wait counts as known and nameless until
// that query's throttle lapses.
func (s *Session) nameOf(g packet.GUID) (string, bool) {
	n, ok := s.cachedName(g)
	if ok {
		return n, true
	}
	if s.silent(g, s.now()) {
		return "", true
	}
	s.askName(g)
	return "", false
}

// cachedName is lookup with the creature entry taken from the store.
func (s *Session) cachedName(g packet.GUID) (string, bool) {
	var entry uint32
	if g.IsCreature() || g.IsGameObject() {
		entry = s.store.Entry(g)
	}
	return s.names.lookup(g, entry)
}

// silent reports whether g is in the short negative cache, dropping a lapsed entry.
func (s *Session) silent(g packet.GUID, now time.Time) bool {
	if g.IsPlayer() {
		until, ok := s.names.silentGUID[g]
		if ok && !now.Before(until) {
			delete(s.names.silentGUID, g)
			return false
		}
		return ok
	}
	key := s.entryKey(g)
	until, ok := s.names.silentEntry[key]
	if ok && !now.Before(until) {
		delete(s.names.silentEntry, key)
		return false
	}
	return ok
}

// giveUp puts g in the negative cache until its last query's throttle lapses.
func (s *Session) giveUp(g packet.GUID) {
	if g == 0 {
		return
	}
	if _, ok := s.cachedName(g); ok {
		return
	}
	if g.IsPlayer() {
		if t, ok := s.names.askedGUID[g]; ok {
			s.names.silentGUID[g] = t.Add(nameQueryInterval)
		}
		return
	}
	key := s.entryKey(g)
	if t, ok := s.names.askedEntry[key]; ok {
		s.names.silentEntry[key] = t.Add(nameQueryInterval)
	}
}

// entryKey is the key of g's entry in the asked and silent maps: the entry, with goKey set for a
// game object.
func (s *Session) entryKey(g packet.GUID) uint32 {
	if g.IsGameObject() {
		return s.store.Entry(g) | goKey
	}
	return s.store.Entry(g)
}

// askName sends CMSG_NAME_QUERY, CMSG_CREATURE_QUERY or CMSG_GAMEOBJECT_QUERY, at most once
// per nameQueryInterval for a GUID or entry. A creature or game object whose entry is not known yet is not
// asked: its create block brings the entry and asks again. A write error surfaces on the
// next send.
func (s *Session) askName(g packet.GUID) {
	now := s.now()
	if g.IsPlayer() {
		if t, ok := s.names.askedGUID[g]; ok && now.Sub(t) < nameQueryInterval {
			return
		}
		s.names.askedGUID[g] = now
		_ = s.conn.WritePacket(opcode.CMSGNameQuery, game.BuildNameQuery(g))
		return
	}
	entry := s.store.Entry(g)
	if entry == 0 {
		return
	}
	key := s.entryKey(g)
	if t, ok := s.names.askedEntry[key]; ok && now.Sub(t) < nameQueryInterval {
		return
	}
	s.names.askedEntry[key] = now
	if g.IsGameObject() {
		_ = s.conn.WritePacket(opcode.CMSGGameobjectQuery, game.BuildGameObjectQuery(entry, g))
		return
	}
	_ = s.conn.WritePacket(opcode.CMSGCreatureQuery, game.BuildCreatureQuery(entry, g))
}

// fillNames sets the source and target names it can and reports whether both are known.
func (s *Session) fillNames(e *events.Event) bool {
	ok := true
	if e.Source == "" && e.SourceGUID != 0 {
		n, known := s.nameOf(e.SourceGUID)
		e.Source, ok = n, ok && known
	}
	if e.Target == "" && e.TargetGUID != 0 {
		n, known := s.nameOf(e.TargetGUID)
		e.Target, ok = n, ok && known
	}
	return ok
}

// flushNames appends waiting events in arrival order, stopping at the first one whose
// names are unknown and whose wait has not run out. An event whose wait ran out is
// appended with the names known so far, and its unanswered GUIDs go to the negative cache.
func (s *Session) flushNames(now time.Time) {
	i := 0
	for ; i < len(s.names.waiting); i++ {
		p := &s.names.waiting[i]
		if !s.fillNames(&p.e) {
			if now.Before(p.until) {
				break
			}
			s.giveUp(p.e.SourceGUID)
			s.giveUp(p.e.TargetGUID)
		}
		s.appendEvent(p.e, p.party)
	}
	if i == len(s.names.waiting) {
		s.names.waiting = nil
	} else {
		s.names.waiting = s.names.waiting[i:]
	}
}

func (s *Session) onNameQueryResponse(body []byte) error {
	info, err := game.ParseNameQueryResponse(body)
	if err != nil {
		return err
	}
	s.names.setPlayer(info.GUID, info.Name)
	s.flushNames(s.now())
	return nil
}

func (s *Session) onCreatureQueryResponse(body []byte) error {
	info, err := game.ParseCreatureQueryResponse(body)
	if err != nil {
		return err
	}
	name := info.Name
	if !info.Found {
		name = fmt.Sprintf("creature %d", info.Entry)
	}
	s.names.setCreature(info.Entry, name)
	s.flushNames(s.now())
	return nil
}

func (s *Session) onGameObjectQueryResponse(body []byte) error {
	info, err := game.ParseGameObjectQueryResponse(body)
	if err != nil {
		return err
	}
	name := info.Name
	if !info.Found {
		name = fmt.Sprintf("gameobject %d", info.Entry)
	}
	s.names.setObject(info.Entry, name)
	s.flushNames(s.now())
	return nil
}
