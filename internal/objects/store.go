package objects

import (
	"math"
	"sort"
	"sync"
	"time"

	"github.com/pjerra/turtle-agent/internal/game"
	"github.com/pjerra/turtle-agent/internal/packet"
	"github.com/pjerra/turtle-agent/internal/spells"
)

type ChangeKind uint8

const (
	ChangeCreated ChangeKind = iota + 1
	ChangeHealth
	ChangeAura
	ChangeMount // UNIT_FIELD_MOUNTDISPLAYID changed: Old and New are the display ids
	ChangeStand // the stand state (byte 0 of UNIT_FIELD_BYTES_1) changed: Old and New are the states
	ChangeCombo // the combo points (byte 1 of PLAYER_FIELD_BYTES) changed: Old and New are the points
	// Game object changes on an object already known (a create block reports only ChangeCreated): Old and New are
	// the GOState, GAMEOBJECT_FLAGS and GAMEOBJECT_DYN_FLAGS values.
	ChangeGOState
	ChangeGOFlags
	ChangeGODynFlags
)

// DefaultLimit is the Units limit when the filter sets none.
const DefaultLimit = 100

// Change is a state change the session turns into events. A create block reports only
// ChangeCreated, so a login burst does not flood the log with auras. Units leaving range
// and combat flag flips are not reported: no event consumes them.
type Change struct {
	GUID     packet.GUID
	Kind     ChangeKind
	Slot     int // aura slot, ChangeAura only
	Old, New uint32
}

// Unit is a player or creature snapshot. When the server's ShowHealthValues option is
// off, units the client may not see the health of arrive as a percent: Health 1..100
// (0 only when dead) and MaxHealth 100 (Object.cpp:727-748). The store passes those
// values through as sent, so Dead and ChangeHealth deltas then work on percents.
type Unit struct {
	GUID  packet.GUID `json:"guid"`
	Type  string      `json:"type"` // "player" or "creature"
	Entry uint32      `json:"entry,omitempty"`
	Name  string      `json:"name"`
	X     float32     `json:"x"`
	Y     float32     `json:"y"`
	Z     float32     `json:"z"`
	O     float32     `json:"o"`
	// PosAgeMS is how long ago the server last said where the unit is (a create or
	// movement block, a movement relay or a spline); -1 when it never did, and the
	// distances are then -1 too. While Moving, x/y/z are the spline's current point.
	PosAgeMS   int64       `json:"pos_age_ms"`
	Moving     bool        `json:"moving"`
	Distance   float32     `json:"distance"`    // 3D, from the player
	Distance2D float32     `json:"distance_2d"` // ignoring height
	Health     uint32      `json:"health"`
	MaxHealth  uint32      `json:"max_health"`
	PowerType  uint8       `json:"power_type"`
	Power      uint32      `json:"power"`
	MaxPower   uint32      `json:"max_power"`
	Level      uint32      `json:"level"`
	Class      uint8       `json:"class,omitempty"` // class id 1-11 from UNIT_FIELD_BYTES_0 (5 priest); creatures have one too
	Faction    uint32      `json:"faction"`
	Target     packet.GUID `json:"target,omitempty"`
	Flags      uint32      `json:"flags"`
	NPCFlags   uint32      `json:"npc_flags"`
	InCombat   bool        `json:"in_combat"`
	Dead       bool        `json:"dead"`
	Hostile    bool        `json:"hostile"`
	InParty    bool        `json:"in_party"`
	Ghost      bool        `json:"ghost,omitempty"` // a player who released the spirit (PLAYER_FLAGS_GHOST): its health is 1, so Dead is false
	// StandState names byte 0 of UNIT_FIELD_BYTES_1 as the stand_state event does.
	StandState string `json:"stand_state"`
	// ComboPoints and ComboTarget are the player's own combo points (0-5) and the unit they are on,
	// from PLAYER_FIELD_BYTES and PLAYER_FIELD_COMBO_TARGET. The core writes them on the player's own object
	// (Player::SetComboPoints), so the player's own row is the one to read. The target is left out when there
	// are no points: the core leaves the old guid in the field.
	ComboPoints uint8       `json:"combo_points,omitempty"`
	ComboTarget packet.GUID `json:"combo_target,omitempty"`
	// GCDMS is the global cooldown left, set by the session on the player's own row only.
	GCDMS   int64  `json:"gcd_ms,omitempty"`
	Auras   []Aura `json:"auras"`   // in slot order, never null
	Casting *Cast  `json:"casting"` // null when not casting
	// Mana and MaxMana are the mana pool of a unit that plays on another power: a druid in Cat Form (energy) or Bear Form (rage) keeps
	// its mana in UNIT_FIELD_POWER1, which Power then does not show. Left out when the unit plays on mana (Power is the mana) or has none.
	Mana    uint32 `json:"mana,omitempty"`
	MaxMana uint32 `json:"max_mana,omitempty"`
}

// Aura is a spell in one of a unit's 48 aura slots (UNIT_FIELD_AURA).
type Aura struct {
	Slot    int    `json:"slot"`
	SpellID uint32 `json:"spell_id"`
	Name    string `json:"name,omitempty"`
	Stacks  int    `json:"stacks"`  // proc charges x stacks when the aura has charges (SpellAuras.cpp:7565)
	Harmful bool   `json:"harmful"` // slots 32-47 (SpellAuraDefines.h:25-26)
	// RemainingMS is the time left as the server last said (SMSG_UPDATE_AURA_DURATION,
	// which it sends only for the player's own auras); omitted when unknown or endless.
	RemainingMS *int64 `json:"remaining_ms,omitempty"`
}

// Cast is the spell a unit is casting: from SMSG_SPELL_START with a cast time until its
// SMSG_SPELL_GO or a failure, or the channel in UNIT_CHANNEL_SPELL.
type Cast struct {
	SpellID uint32 `json:"spell_id"`
	Name    string `json:"name,omitempty"`
	Channel bool   `json:"channel"`
	// Target is the unit the cast is aimed at, as SMSG_SPELL_START names it (0: none, or an area).
	Target packet.GUID `json:"target,omitempty"`
	// EndsInMS is the time to the cast's end (negative a little past it, while the spell
	// goes off); a channel's end is not sent to other clients, so it has none.
	EndsInMS *int64 `json:"ends_in_ms,omitempty"`
}

type Filter struct {
	Near    float32 // 3D yards; 0: any distance (units with no known position never match)
	Entry   uint32
	Hostile *bool
	Name    string // applied by the session after names are filled in
	Limit   int    // 0: 100
}

type object struct {
	guid       packet.GUID
	typ        uint8
	values     map[uint16]uint32
	x, y, z, o float32
	posAt      time.Time // zero: no position yet
	track      *track    // the server spline the unit follows, if any
	cast       *casting
	auraEnds   map[int]time.Time // aura slot -> when it runs out, the player's own auras only
	// pendingAura holds a duration the server sent for a slot, until the slot's spell changes or
	// the slot empties (see AuraDuration).
	pendingAura map[int]pendingDuration
}

// pendingDuration is an aura duration that waits for the spell it belongs to.
type pendingDuration struct {
	end     time.Time // when the aura runs out
	arrived time.Time // when the duration came, for pendingAuraWindow
}

// pendingAuraWindow is how long a duration waits for the spell that takes its slot. The server
// sends the duration at once, straight to the player (SpellAuras.cpp:6909, 7576-7588), but the
// object update that puts the spell in the slot goes out at the end of the map tick (Map.cpp:903).
// Past MapUpdate.ObjectsUpdate.Timeout (default 100 ms, Map.cpp:2797) the rest of that update waits
// for a later tick, so under load the spell can trail its duration by more than a second (inferred
// from the code, not measured). The window is 5 s to cover that. A duration older than it is dropped.
// Residual: a slot that changes to a spell with no duration packet of its own takes a pending time,
// which may be a refresh of the spell it replaced. The core sends a packet for every non-passive
// aura it puts in a slot, so this is narrow, and the packet has no spell id to tell it apart from a
// rank swap.
const pendingAuraWindow = 5 * time.Second

type casting struct {
	spell  uint32
	end    time.Time
	target packet.GUID
}

// castGrace is how long past its end a cast still shows when its SMSG_SPELL_GO did not
// come (the server sends none for a caster that went out of range meanwhile).
const castGrace = 2 * time.Second

type Store struct {
	mu   sync.RWMutex
	objs map[packet.GUID]*object
	now  func() time.Time
}

func NewStore() *Store { return &Store{objs: map[packet.GUID]*object{}, now: time.Now} }

// SetClock replaces time.Now for position ages and spline progress (tests).
func (s *Store) SetClock(now func() time.Time) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.now = now
}

func (s *Store) get(g packet.GUID) (*object, bool) {
	o, ok := s.objs[g]
	if !ok {
		o = &object{guid: g, values: map[uint16]uint32{}}
		s.objs[g] = o
	}
	return o, ok
}

// Apply applies blocks in order and returns the resulting changes.
func (s *Store) Apply(blocks []Block) []Change {
	s.mu.Lock()
	defer s.mu.Unlock()
	var ch []Change
	now := s.now()
	for _, b := range blocks {
		switch b.Type {
		case BlockCreate, BlockCreate2:
			// A create block carries the whole object, so one for a GUID already held (a
			// respawn, a unit back in range) starts it afresh: no stale auras or target.
			o := &object{guid: b.GUID, typ: b.ObjType, values: map[uint16]uint32{}}
			s.objs[b.GUID] = o
			ch = append(ch, Change{GUID: b.GUID, Kind: ChangeCreated})
			s.moveObject(o, b.Move)
			ch = setValues(o, b.Values, ch, false, now)
		case BlockValues:
			o, existed := s.get(b.GUID) // values before a create we missed are kept
			ch = setValues(o, b.Values, ch, existed, now)
		case BlockMovement:
			if o, ok := s.objs[b.GUID]; ok {
				s.moveObject(o, b.Move)
			}
		case BlockOutOfRange:
			for _, g := range b.GUIDs {
				delete(s.objs, g)
			}
		}
	}
	return ch
}

// moveObject takes the position of a create or movement block. A unit on a spline
// carries on along it from the block's time passed (the core sends the whole route and
// how far along it the unit is, packet_builder.cpp:154-180); the block's position is
// where the unit stood when the server wrote it.
func (s *Store) moveObject(o *object, m *Movement) {
	if m == nil || !m.HasPosition {
		return
	}
	now := s.now()
	o.x, o.y, o.z, o.o, o.posAt, o.track = m.X, m.Y, m.Z, m.O, now, nil
	if sp := m.Spline; sp != nil && sp.Flags&splineDone == 0 && len(sp.Points) >= 3 {
		// The first and last points of the path are Spline's virtual points
		// (SplineBase::InitCatmullRom, spline.cpp:240-271), linear paths included: the
		// initializers table maps ModeLinear to InitCatmullRom (spline.cpp:49-55), and
		// every create-block spline in the captures has them (TestCapturedCreateSplines).
		pts := sp.Points[1 : len(sp.Points)-1]
		start := now.Add(-time.Duration(sp.TimePassed) * time.Millisecond)
		o.track = newTrack(pts, start, time.Duration(sp.Duration)*time.Millisecond, sp.Flags&splineCyclic != 0)
		if o.track != nil && sp.Flags&splineFinalAngle != 0 {
			o.track.finalO, o.track.hasFinalO = sp.FinalAngle, true
		}
	}
}

// setValues applies vals to o at now, and returns ch with the changes it reports when report is set.
func setValues(o *object, vals map[uint16]uint32, ch []Change, report bool, now time.Time) []Change {
	for k, v := range vals {
		if k >= UnitAura && k < UnitAura+AuraSlots && v != o.values[k] {
			// A new spell in the slot, or none: the time the old spell had goes with it. A duration
			// still pending is the new spell's if the spell comes within pendingAuraWindow of it; it
			// is dropped if the slot empties, and once older than the window.
			slot := int(k - UnitAura)
			delete(o.auraEnds, slot)
			if p, waiting := o.pendingAura[slot]; waiting && v != 0 && now.Sub(p.arrived) <= pendingAuraWindow {
				if o.auraEnds == nil {
					o.auraEnds = map[int]time.Time{}
				}
				o.auraEnds[slot] = p.end
			}
			delete(o.pendingAura, slot)
		}
	}
	idx := make([]int, 0, len(vals))
	for i := range vals {
		idx = append(idx, int(i))
	}
	sort.Ints(idx)
	for _, i := range idx {
		k, v := uint16(i), vals[uint16(i)]
		old := o.values[k]
		o.values[k] = v
		if !report || old == v {
			continue
		}
		if isGameObject(o) { // its fields reuse the unit indices, so the unit cases below must not see them
			switch k {
			case GOState:
				ch = append(ch, Change{GUID: o.guid, Kind: ChangeGOState, Old: old, New: v})
			case GOFlags:
				ch = append(ch, Change{GUID: o.guid, Kind: ChangeGOFlags, Old: old, New: v})
			case GODynFlags:
				ch = append(ch, Change{GUID: o.guid, Kind: ChangeGODynFlags, Old: old, New: v})
			}
			continue
		}
		switch {
		case k == UnitHealth:
			ch = append(ch, Change{GUID: o.guid, Kind: ChangeHealth, Old: old, New: v})
		case k >= UnitAura && k < UnitAura+AuraSlots:
			ch = append(ch, Change{GUID: o.guid, Kind: ChangeAura, Slot: int(k - UnitAura), Old: old, New: v})
		case k == UnitMountDisplay:
			ch = append(ch, Change{GUID: o.guid, Kind: ChangeMount, Old: old, New: v})
		case k == UnitBytes1 && old&0xFF != v&0xFF:
			ch = append(ch, Change{GUID: o.guid, Kind: ChangeStand, Old: old & 0xFF, New: v & 0xFF})
		case k == PlayerFieldBytes && old>>8&0xFF != v>>8&0xFF:
			ch = append(ch, Change{GUID: o.guid, Kind: ChangeCombo, Old: old >> 8 & 0xFF, New: v >> 8 & 0xFF})
		}
	}
	return ch
}

func (s *Store) Destroy(g packet.GUID) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	_, ok := s.objs[g]
	delete(s.objs, g)
	return ok
}

// MoveTo puts g where a movement relay says it is, ending any spline it was on.
func (s *Store) MoveTo(g packet.GUID, x, y, z, o float32) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if ob, ok := s.objs[g]; ok {
		ob.x, ob.y, ob.z, ob.o, ob.posAt, ob.track = x, y, z, o, s.now(), nil
	}
}

// MoveSpline starts g on a spline from SMSG_MONSTER_MOVE. Until the spline's duration has
// run, g is at the spline's current point, facing along it; then at its destination,
// facing the spline's final angle or the way it last travelled. A stop puts g at the
// stop's point; a stop, or a move with no horizontal travel, keeps the facing it had.
func (s *Store) MoveSpline(m MonsterMove) {
	s.mu.Lock()
	defer s.mu.Unlock()
	ob, ok := s.objs[m.GUID]
	if !ok {
		return
	}
	now := s.now()
	_, ob.o, _ = ob.pos(now) // the facing it has now, partway along an earlier spline
	path := m.Path
	if len(path) < 2 {
		path = [][3]float32{m.Start, m.Dest}
	}
	// A linear path of more than Movement.MaxPointsPerPacket (80) points goes out in
	// parts: each later SMSG_MONSTER_MOVE of the same spline repeats the first point, then
	// the points after those sent, with the time left to its last point (Unit.cpp:11437-11442,
	// packet_builder.cpp:78-150). The unit carries on from where it is, through the points
	// of the earlier part it has not reached yet.
	if t := ob.track; t != nil && !m.Stop && m.ID != 0 && t.id == m.ID {
		path = append(t.rest(now), path[1:]...)
	}
	ob.x, ob.y, ob.z, ob.posAt, ob.track = m.Dest[0], m.Dest[1], m.Dest[2], now, nil
	if m.Stop {
		return
	}
	if ob.track = newTrack(path, now, time.Duration(m.Duration)*time.Millisecond, m.Flags&splineCyclic != 0); ob.track != nil {
		ob.track.id, ob.track.finalO, ob.track.hasFinalO = m.ID, m.FinalAngle, m.HasFinalAngle
		return
	}
	// No time to take or no way to go: the unit is at the destination at once.
	if dx, dy := float64(m.Dest[0]-m.Start[0]), float64(m.Dest[1]-m.Start[1]); dx != 0 || dy != 0 {
		ob.o = angle(dx, dy)
	}
	if m.HasFinalAngle {
		ob.o = m.FinalAngle
	}
}

// pos is where o is at now, its facing, and whether it is still moving on a spline. A
// spline with no horizontal travel leaves the facing the unit had before it.
func (o *object) pos(now time.Time) (p [3]float32, face float32, moving bool) {
	if o.track == nil {
		return [3]float32{o.x, o.y, o.z}, o.o, false
	}
	p, face, ok, moving := o.track.at(now)
	if !ok {
		face = o.o
	}
	return p, face, moving
}

// StartCast records SMSG_SPELL_START: g casts spell for castMS. An instant spell (cast
// time 0) is not a cast; its SMSG_SPELL_GO follows at once.
func (s *Store) StartCast(g packet.GUID, spell, castMS uint32) { s.StartCastAt(g, spell, castMS, 0) }

// StartCastAt is StartCast with the unit the cast is aimed at (the target of SMSG_SPELL_START), 0 for none.
func (s *Store) StartCastAt(g packet.GUID, spell, castMS uint32, target packet.GUID) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if o, ok := s.objs[g]; ok && castMS > 0 {
		o.cast = &casting{spell: spell, end: s.now().Add(time.Duration(castMS) * time.Millisecond), target: target}
	}
}

// EndCast ends g's cast of spell (SMSG_SPELL_GO, a failure); spell 0 ends any cast.
func (s *Store) EndCast(g packet.GUID, spell uint32) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if o, ok := s.objs[g]; ok && o.cast != nil && (spell == 0 || o.cast.spell == spell) {
		o.cast = nil
	}
}

// DelayCast moves g's cast end back by ms (SMSG_SPELL_DELAYED, pushback, sent to the
// caster only).
func (s *Store) DelayCast(g packet.GUID, ms uint32) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if o, ok := s.objs[g]; ok && o.cast != nil {
		o.cast.end = o.cast.end.Add(time.Duration(ms) * time.Millisecond)
	}
}

// AuraDuration records SMSG_UPDATE_AURA_DURATION for g's aura slot: ms left, or a
// negative value for an aura without end. The server sends it when it applies or refreshes
// an aura, before the object update that puts the spell in the slot (SpellAuras.cpp:6896-6909),
// so the packet can come first. The packet has no spell id: a duration for a slot that holds
// a spell is that spell's, and one for an empty slot waits for the spell that fills it. Either
// way it stays pending for pendingAuraWindow: a different spell that takes the slot in that
// time (a rank swap, whose duration the server sends ahead of the object update) takes it.
func (s *Store) AuraDuration(g packet.GUID, slot int, ms int32) {
	s.mu.Lock()
	defer s.mu.Unlock()
	o, ok := s.objs[g]
	if !ok || slot < 0 || slot >= AuraSlots {
		return
	}
	if ms < 0 {
		delete(o.auraEnds, slot)
		delete(o.pendingAura, slot)
		return
	}
	now := s.now()
	end := now.Add(time.Duration(ms) * time.Millisecond)
	if o.pendingAura == nil {
		o.pendingAura = map[int]pendingDuration{}
	}
	o.pendingAura[slot] = pendingDuration{end: end, arrived: now}
	if o.values[UnitAura+uint16(slot)] == 0 {
		return
	}
	if o.auraEnds == nil {
		o.auraEnds = map[int]time.Time{}
	}
	o.auraEnds[slot] = end
}

// Clear forgets every object (SMSG_NEW_WORLD).
func (s *Store) Clear() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.objs = map[packet.GUID]*object{}
}

func (s *Store) Get(g packet.GUID, ox, oy, oz float32) (Unit, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	o, ok := s.objs[g]
	if !ok {
		return Unit{}, false
	}
	return toUnit(o, s.now(), ox, oy, oz), true
}

// OwnCorpse is the guid of the corpse object that belongs to the player, 0 when the store holds none (the client sends it in
// CMSG_RECLAIM_CORPSE; the server ignores it).
func (s *Store) OwnCorpse(owner packet.GUID) packet.GUID {
	s.mu.RLock()
	defer s.mu.RUnlock()
	for g, o := range s.objs {
		if o.typ == TypeCorpse && guidAt(o.values, CorpseOwner) == owner {
			return g
		}
	}
	return 0
}

// Entry is g's creature entry: its OBJECT_FIELD_ENTRY when the store holds that, else
// what the GUID encodes (packet.GUID.Entry), which is 0 for a pet whose values are unknown.
func (s *Store) Entry(g packet.GUID) uint32 {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if o, ok := s.objs[g]; ok && o.values[FieldEntry] != 0 {
		return o.values[FieldEntry]
	}
	return g.Entry()
}

// Field is the update field idx of g as last sent, 0 when g or the field is unknown.
func (s *Store) Field(g packet.GUID, idx uint16) uint32 {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if o, ok := s.objs[g]; ok {
		return o.values[idx]
	}
	return 0
}

// Owners returns the charmer, summoner and creator of g (zero when unset or unknown), so
// a pet, totem or charmed unit can be traced to its player in one lookup.
func (s *Store) Owners(g packet.GUID) [3]packet.GUID {
	s.mu.RLock()
	defer s.mu.RUnlock()
	o, ok := s.objs[g]
	if !ok {
		return [3]packet.GUID{}
	}
	return [3]packet.GUID{guidAt(o.values, UnitCharmedBy), guidAt(o.values, UnitSummonedBy), guidAt(o.values, UnitCreatedBy)}
}

// SummonOf is the pet an owner's UNIT_FIELD_SUMMON names, 0 when it names none or the owner is unknown. A player's own field is sent to the
// player and to everyone in view (Player::InitVisibleBits, Objects/Player.cpp:5107-5108).
func (s *Store) SummonOf(owner packet.GUID) packet.GUID {
	s.mu.RLock()
	defer s.mu.RUnlock()
	o, ok := s.objs[owner]
	if !ok {
		return 0
	}
	return guidAt(o.values, UnitSummon)
}

// PetOf is the pet of an owner: the unit its UNIT_FIELD_SUMMON names, else a creature of the pet guid class (packet.HighPet) in view that
// names the owner as its summoner, creator or charmer (Owners), the lowest guid first. 0 when there is none.
func (s *Store) PetOf(owner packet.GUID) packet.GUID {
	if g := s.SummonOf(owner); g != 0 {
		return g
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	var best packet.GUID
	for g, o := range s.objs {
		if g.High() != packet.HighPet || !isUnit(o) {
			continue
		}
		for _, w := range []uint16{UnitSummonedBy, UnitCreatedBy, UnitCharmedBy} {
			if guidAt(o.values, w) == owner && (best == 0 || g < best) {
				best = g
			}
		}
	}
	return best
}

func guidAt(v map[uint16]uint32, i uint16) packet.GUID {
	return packet.GUID(uint64(v[i]) | uint64(v[i+1])<<32)
}

// Units returns players and creatures matching f, nearest first.
func (s *Store) Units(f Filter, ox, oy, oz float32) []Unit {
	s.mu.RLock()
	var out []Unit
	now := s.now()
	for _, o := range s.objs {
		if !isUnit(o) {
			continue
		}
		u := toUnit(o, now, ox, oy, oz)
		if (f.Near > 0 && (u.Distance < 0 || u.Distance > f.Near)) || (f.Entry != 0 && u.Entry != f.Entry) || (f.Hostile != nil && u.Hostile != *f.Hostile) {
			continue
		}
		out = append(out, u)
	}
	s.mu.RUnlock()
	sort.Slice(out, func(i, j int) bool {
		if ui, uj := out[i].Distance < 0, out[j].Distance < 0; ui != uj {
			return uj // units with no known position last
		}
		if out[i].Distance != out[j].Distance {
			return out[i].Distance < out[j].Distance
		}
		return out[i].GUID < out[j].GUID
	})
	limit := f.Limit
	if limit <= 0 {
		limit = DefaultLimit
	}
	if len(out) > limit {
		out = out[:limit]
	}
	return out
}

func isUnit(o *object) bool {
	switch o.typ {
	case TypeUnit, TypePlayer:
		return true
	case 0: // values seen before a create block
		return o.guid.IsPlayer() || o.guid.IsCreature()
	}
	return false
}

func toUnit(o *object, now time.Time, ox, oy, oz float32) Unit {
	v := o.values
	u := Unit{
		GUID: o.guid, Type: "creature", Entry: v[FieldEntry],
		Health: v[UnitHealth], MaxHealth: v[UnitMaxHealth], PowerType: uint8(v[UnitBytes0] >> 24), Class: uint8(v[UnitBytes0] >> 8),
		Level: v[UnitLevel], Faction: v[UnitFaction], Flags: v[UnitFlags], NPCFlags: v[UnitNPCFlags],
		Target: guidAt(v, UnitTarget),
	}
	if o.typ == TypePlayer || o.guid.IsPlayer() {
		u.Type = "player"
	}
	if u.Entry == 0 && o.guid.IsCreature() {
		u.Entry = o.guid.Entry()
	}
	if u.PowerType < 5 {
		u.Power, u.MaxPower = v[UnitPower1+uint16(u.PowerType)], v[UnitMaxPower1+uint16(u.PowerType)]
		if u.PowerType != 0 {
			u.Mana, u.MaxMana = v[UnitPower1], v[UnitMaxPower1]
		}
	}
	u.InCombat = u.Flags&UnitFlagInCombat != 0
	u.Dead = u.MaxHealth > 0 && u.Health == 0
	u.Ghost = u.Type == "player" && v[PlayerFlags]&PlayerFlagGhost != 0
	u.Hostile = u.Type == "creature" && !u.Dead && u.NPCFlags == 0 &&
		u.Flags&(UnitFlagSpawning|UnitFlagPlayerControlled|UnitFlagImmuneToPlayer|UnitFlagNotSelectable) == 0
	u.Auras = []Aura{}
	for i := uint16(0); i < AuraSlots; i++ {
		id := v[UnitAura+i]
		if id == 0 {
			continue
		}
		a := Aura{Slot: int(i), SpellID: id, Name: spells.Name(id), Harmful: i >= PositiveAuraSlots,
			Stacks: int(v[UnitAuraApps+i/4]>>(8*(i%4))&0xFF) + 1}
		if end, ok := o.auraEnds[int(i)]; ok {
			ms := max(end.Sub(now).Milliseconds(), 0)
			a.RemainingMS = &ms
		}
		u.Auras = append(u.Auras, a)
	}
	u.StandState = game.StandStateName(v[UnitBytes1] & 0xFF)
	if o.typ == TypePlayer || o.guid.IsPlayer() {
		if u.ComboPoints = uint8(v[PlayerFieldBytes] >> 8 & 0xFF); u.ComboPoints > 0 {
			u.ComboTarget = guidAt(v, PlayerComboTarget)
		}
	}
	if c := o.cast; c != nil && now.Before(c.end.Add(castGrace)) {
		ms := c.end.Sub(now).Milliseconds()
		u.Casting = &Cast{SpellID: c.spell, Name: spells.Name(c.spell), EndsInMS: &ms, Target: c.target}
	} else if ch := v[UnitChannelSpell]; ch != 0 {
		u.Casting = &Cast{SpellID: ch, Name: spells.Name(ch), Channel: true}
	}
	if o.posAt.IsZero() {
		u.PosAgeMS, u.Distance, u.Distance2D = -1, -1, -1
		return u
	}
	p, face, moving := o.pos(now)
	u.X, u.Y, u.Z, u.O, u.Moving = p[0], p[1], p[2], face, moving
	u.PosAgeMS = max(now.Sub(o.posAt).Milliseconds(), 0)
	dx, dy, dz := float64(p[0]-ox), float64(p[1]-oy), float64(p[2]-oz)
	u.Distance = float32(math.Sqrt(dx*dx + dy*dy + dz*dz))
	u.Distance2D = float32(math.Hypot(dx, dy))
	return u
}

// Item is an item the player carries, at bag/slot as CMSG_AUTOEQUIP_ITEM addresses it.
type Item struct {
	Bag   uint8       `json:"bag"`
	Slot  uint8       `json:"slot"`
	GUID  packet.GUID `json:"guid"`
	Entry uint32      `json:"item_entry"` // 0, like Count, until the item's create block arrives
	Count uint32      `json:"count"`
}

// Backpack lists the items in player's backpack, in slot order, from the player's own
// inventory fields and each item's create block (Player::BuildCreateUpdateBlockForPlayer
// sends the items to their owner, Player.cpp:5224-5254).
func (s *Store) Backpack(player packet.GUID) []Item {
	s.mu.RLock()
	defer s.mu.RUnlock()
	p, ok := s.objs[player]
	if !ok {
		return nil
	}
	var out []Item
	for slot := BackpackFirst; slot < BackpackEnd; slot++ {
		if it, ok := s.item(BackpackBag, slot, guidAt(p.values, PlayerInvSlotHead+2*uint16(slot))); ok {
			out = append(out, it)
		}
	}
	return out
}

// Bags lists the items inside the player's four bags (inventory slots 19-22), bag by bag
// in slot order, from each bag's CONTAINER_FIELD_SLOT_1 fields. A bag whose create block
// has not arrived holds nothing yet.
func (s *Store) Bags(player packet.GUID) []Item {
	s.mu.RLock()
	defer s.mu.RUnlock()
	p, ok := s.objs[player]
	if !ok {
		return nil
	}
	var out []Item
	for bag := BagFirst; bag < BagEnd; bag++ {
		c, ok := s.objs[guidAt(p.values, PlayerInvSlotHead+2*uint16(bag))]
		if !ok {
			continue
		}
		for slot := uint32(0); slot < c.values[ContainerNumSlots] && slot < maxBagSlots; slot++ {
			if it, ok := s.item(bag, uint8(slot), guidAt(c.values, ContainerSlot1+2*uint16(slot))); ok {
				out = append(out, it)
			}
		}
	}
	return out
}

// maxBagSlots bounds a bag's slot count: a container holds at most 36 items.
const maxBagSlots = 36

// ItemAt is the item at bag/slot as CMSG_AUTOEQUIP_ITEM addresses it: bag 255 with an
// inventory slot up to the backpack's last (38), or a bag slot 19-22 with a slot inside
// that bag (its CONTAINER_FIELD_SLOT_1 fields). ok is false for an empty or unknown slot.
func (s *Store) ItemAt(player packet.GUID, bag, slot uint8) (Item, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	p, ok := s.objs[player]
	if !ok {
		return Item{}, false
	}
	switch {
	case bag == BackpackBag && slot < BackpackEnd:
		return s.item(bag, slot, guidAt(p.values, PlayerInvSlotHead+2*uint16(slot)))
	case bag >= BagFirst && bag < BagEnd:
		c, ok := s.objs[guidAt(p.values, PlayerInvSlotHead+2*uint16(bag))]
		if !ok || uint32(slot) >= c.values[ContainerNumSlots] {
			return Item{}, false
		}
		return s.item(bag, slot, guidAt(c.values, ContainerSlot1+2*uint16(slot)))
	}
	return Item{}, false
}

// item describes the item g at bag/slot; the caller holds s.mu.
func (s *Store) item(bag, slot uint8, g packet.GUID) (Item, bool) {
	if g == 0 {
		return Item{}, false
	}
	it := Item{Bag: bag, Slot: slot, GUID: g}
	if o, ok := s.objs[g]; ok {
		it.Entry, it.Count = o.values[FieldEntry], o.values[ItemStackCount]
	}
	return it, true
}
