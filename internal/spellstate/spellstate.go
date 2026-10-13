// Package spellstate is what the agent knows about its own spells: the spellbook, the
// cooldowns and the global cooldown, and the state of its cast. The session feeds it from
// the packets the server sends; a brain (and the spells and cast_state methods) read it.
//
// The 1.12 server tells the client very little about cooldowns. It sends none for a spell the
// player casts (Unit::AddSpellAndCategoryCooldowns sends SMSG_SPELL_COOLDOWN only for a pet
// or a charmed unit, Unit.cpp:11900-11907); the client works them out itself from the spell
// data when its own SMSG_SPELL_GO arrives. So does this package, from internal/spells: a
// cast's cooldown, its category cooldown and the global cooldown (GCD) are started from the
// template's numbers. What the server does send is applied on top: the cooldowns at login
// (SMSG_INITIAL_SPELLS), a school lockout after an interrupt (SMSG_SPELL_COOLDOWN),
// SMSG_COOLDOWN_EVENT (start the cooldown now) and SMSG_CLEAR_COOLDOWN.
//
// Limits, all on the side of waiting too long: talents and items that shorten a cooldown
// (spell mods) are not known, so a shortened one still counts as running until its nominal end;
// the server rounds cooldowns down to whole seconds (a spell cast at x.9 s is ready up to a
// second early); haste is not applied to the GCD; the cooldown an item sets (a potion) is
// kept in the item prototype and is not tracked. A cast the server refuses with
// SPELL_FAILED_NOT_READY is held back for a second (NotReadyHold) whatever the numbers say.
package spellstate

import (
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/pjerra/turtle-agent/internal/game"
	"github.com/pjerra/turtle-agent/internal/packet"
	"github.com/pjerra/turtle-agent/internal/spells"
)

const (
	// NotReadyHold is how long a spell the server refused as not ready (SPELL_FAILED_NOT_READY)
	// counts as not ready, whatever the local numbers say.
	NotReadyHold = time.Second
	// castGrace is how long past its end a cast still shows when its SMSG_SPELL_GO did not come
	// (as in objects.castGrace).
	castGrace = 2 * time.Second
	// infinite is how long a cooldown the server keeps for "infinity" lasts here.
	infinite = 30 * 24 * time.Hour
)

// Why names what holds a spell back.
const (
	WhyCooldown = "cooldown"       // the spell's own cooldown
	WhyCategory = "category"       // a spell of its category was cast
	WhyGCD      = "gcd"            // the global cooldown
	WhyNotReady = "server_refused" // the server refused it as not ready a moment ago
)

// Sources of a cooldown, as the cooldown_start event gives them.
const (
	SourceCast   = "cast"   // the player's own SMSG_SPELL_GO
	SourceServer = "server" // SMSG_SPELL_COOLDOWN: a school lockout, a script
	SourceEvent  = "event"  // SMSG_COOLDOWN_EVENT
	SourceLogin  = "login"  // SMSG_INITIAL_SPELLS
)

type cooldown struct {
	start, end time.Time
	source     string
}

type gcdEntry struct{ start, end time.Time }

type castInfo struct {
	spell   uint32
	start   time.Time
	end     time.Time // the cast time's end; start for an instant spell
	target  packet.GUID
	gcdCat  uint32 // the GCD category this cast started; 0 with gcdSet false when none
	gcdSet  bool
	channel bool
	done    bool // SMSG_SPELL_GO came
}

type lastCast struct {
	spell uint32
	at    time.Time
}

type lastFail struct {
	spell uint32
	code  uint8
	name  string // game.CastFailName, or "interrupted"
	at    time.Time
}

// State is safe for concurrent use: the session's loop writes it, RPC calls read it.
type State struct {
	mu        sync.Mutex
	now       func() time.Time
	known     map[uint32]bool
	cds       map[uint32]cooldown
	cats      map[uint32]time.Time
	gcd       map[uint32]gcdEntry
	notReady  map[uint32]time.Time
	cast      *castInfo
	channel   *castInfo
	last      lastCast
	fail      *lastFail
	loaded    bool // SMSG_INITIAL_SPELLS was seen
	failCount uint64
}

// New makes an empty State on the clock now (nil: time.Now).
func New(now func() time.Time) *State {
	if now == nil {
		now = time.Now
	}
	return &State{now: now, known: map[uint32]bool{}, cds: map[uint32]cooldown{}, cats: map[uint32]time.Time{},
		gcd: map[uint32]gcdEntry{}, notReady: map[uint32]time.Time{}}
}

// Started is a cooldown that began, as the cooldown_start event reports it.
type Started struct {
	Spell      uint32
	MS         uint32
	Category   uint32
	CategoryMS uint32
	Source     string
}

// ---- the spellbook ----

// SetInitial replaces the spellbook and the cooldowns with SMSG_INITIAL_SPELLS.
func (st *State) SetInitial(in game.InitialSpells) {
	st.mu.Lock()
	defer st.mu.Unlock()
	now := st.now()
	st.known = map[uint32]bool{}
	for _, id := range in.Spells {
		st.known[id] = true
	}
	st.cds, st.cats = map[uint32]cooldown{}, map[uint32]time.Time{}
	for _, c := range in.Cooldowns {
		own, cat := ms(c.Cooldown), ms(c.CategoryCooldown)
		if c.CategoryCooldown == game.InfiniteCooldown {
			cat = infinite
			if c.Cooldown <= 1 {
				own = infinite
			}
		}
		if c.Category != 0 && cat > 0 {
			st.cats[c.Category] = now.Add(cat)
		}
		if own == 0 {
			own = cat
		}
		if own > 0 && c.Item == 0 { // an item's spell is not one of the spellbook
			st.cds[c.Spell] = cooldown{start: now, end: now.Add(own), source: SourceLogin}
		}
	}
	st.loaded = true
}

// Loaded reports whether the spellbook came yet.
func (st *State) Loaded() bool {
	st.mu.Lock()
	defer st.mu.Unlock()
	return st.loaded
}

// Learn adds a spell to the spellbook (SMSG_LEARNED_SPELL); false when it was there already.
func (st *State) Learn(id uint32) bool {
	st.mu.Lock()
	defer st.mu.Unlock()
	had := st.known[id]
	st.known[id] = true
	return !had
}

// Remove drops a spell (SMSG_REMOVED_SPELL); false when it was not there.
func (st *State) Remove(id uint32) bool {
	st.mu.Lock()
	defer st.mu.Unlock()
	had := st.known[id]
	delete(st.known, id)
	return had
}

// Supersede replaces old by its higher rank (SMSG_SUPERCEDED_SPELL): the spellbook shows
// the highest rank alone, so the lower one leaves the known spells.
func (st *State) Supersede(old, replacement uint32) {
	st.mu.Lock()
	defer st.mu.Unlock()
	if old != replacement {
		delete(st.known, old)
	}
	st.known[replacement] = true
}

// Known reports whether id is in the spellbook.
func (st *State) Known(id uint32) bool {
	st.mu.Lock()
	defer st.mu.Unlock()
	return st.known[id]
}

// KnownIDs lists the spellbook in id order.
func (st *State) KnownIDs() []uint32 {
	st.mu.Lock()
	defer st.mu.Unlock()
	return st.knownIDs()
}

func (st *State) knownIDs() []uint32 {
	ids := make([]uint32, 0, len(st.known))
	for id := range st.known {
		ids = append(ids, id)
	}
	sort.Slice(ids, func(i, j int) bool { return ids[i] < ids[j] })
	return ids
}

func sameName(id uint32, name string) bool {
	n := spells.Name(id)
	return n != "" && strings.EqualFold(n, strings.TrimSpace(name))
}

// Ranks lists the known spells named name (case-insensitive), the lowest rank first.
func (st *State) Ranks(name string) []uint32 {
	st.mu.Lock()
	defer st.mu.Unlock()
	return st.ranks(name)
}

func (st *State) ranks(name string) []uint32 {
	var ids []uint32
	for id := range st.known {
		if sameName(id, name) {
			ids = append(ids, id)
		}
	}
	rank := func(id uint32) uint8 {
		m, _ := spells.Lookup(id)
		return m.Rank
	}
	sort.Slice(ids, func(i, j int) bool {
		if ri, rj := rank(ids[i]), rank(ids[j]); ri != rj {
			return ri < rj
		}
		return ids[i] < ids[j]
	})
	return ids
}

// Highest is the highest known rank of the spell named name (case-insensitive), by the rank
// in its subtext and then its id. Never hard-code a rank's id: the highest known one changes
// as the character levels.
func (st *State) Highest(name string) (uint32, bool) {
	r := st.Ranks(name)
	if len(r) == 0 {
		return 0, false
	}
	return r[len(r)-1], true
}

// ---- cooldowns and the global cooldown ----

func ms(v uint32) time.Duration { return time.Duration(v) * time.Millisecond }

// startCooldown starts spell's own and category cooldowns from its template, as
// Unit::AddSpellAndCategoryCooldowns does: the spell's own time, or its category's when it
// has none of its own. It reports whether any started. Called with the lock held.
func (st *State) startCooldown(id uint32, m spells.Meta, source string, now time.Time) (Started, bool) {
	own, cat := ms(m.CooldownMS), ms(m.CategoryCDMS)
	if own == 0 && cat == 0 {
		return Started{}, false
	}
	if own == 0 {
		own = cat
	}
	if m.Category != 0 && cat > 0 {
		st.cats[m.Category] = now.Add(cat)
	}
	st.cds[id] = cooldown{start: now, end: now.Add(own), source: source}
	return Started{Spell: id, MS: uint32(own / time.Millisecond), Category: m.Category, CategoryMS: m.CategoryCDMS, Source: source}, true
}

// StartCooldown starts the cooldowns of spell id from its template, as SMSG_COOLDOWN_EVENT
// does. It reports false for a spell with no cooldown or an unknown template.
func (st *State) StartCooldown(id uint32, source string) (Started, bool) {
	m, ok := spells.Lookup(id)
	if !ok {
		return Started{}, false
	}
	st.mu.Lock()
	defer st.mu.Unlock()
	return st.startCooldown(id, m, source, st.now())
}

// ServerCooldown sets the cooldown of spell to ms milliseconds from now (SMSG_SPELL_COOLDOWN).
func (st *State) ServerCooldown(id uint32, durMS uint32) Started {
	st.mu.Lock()
	defer st.mu.Unlock()
	now := st.now()
	st.cds[id] = cooldown{start: now, end: now.Add(ms(durMS)), source: SourceServer}
	return Started{Spell: id, MS: durMS, Source: SourceServer}
}

// Clear ends the cooldown of spell id and, with it, its category's (SMSG_CLEAR_COOLDOWN
// removes the spell's entry, which holds both, Unit.cpp:11727-11736). It reports whether
// anything was running.
func (st *State) Clear(id uint32) bool {
	st.mu.Lock()
	defer st.mu.Unlock()
	now := st.now()
	_, running := st.cds[id]
	delete(st.cds, id)
	delete(st.notReady, id)
	if m, ok := spells.Lookup(id); ok && m.Category != 0 {
		if end, ok := st.cats[m.Category]; ok && end.After(now) {
			running = true
		}
		delete(st.cats, m.Category)
	}
	return running
}

// ClearAll ends every cooldown, the GCD included (SMSG_COOLDOWN_CHEAT).
func (st *State) ClearAll() []uint32 {
	st.mu.Lock()
	defer st.mu.Unlock()
	ids := make([]uint32, 0, len(st.cds))
	for id := range st.cds {
		ids = append(ids, id)
	}
	sort.Slice(ids, func(i, j int) bool { return ids[i] < ids[j] })
	st.cds, st.cats = map[uint32]cooldown{}, map[uint32]time.Time{}
	st.gcd, st.notReady = map[uint32]gcdEntry{}, map[uint32]time.Time{}
	return ids
}

// Pending reports whether a cooldown is running that will end: the session's replay keeps
// ticking while one does, so cooldown_end events come at the right time. A cooldown the server
// keeps for "infinity" does not count.
func (st *State) Pending() bool {
	st.mu.Lock()
	defer st.mu.Unlock()
	for _, c := range st.cds {
		if c.end.Sub(c.start) < infinite/2 {
			return true
		}
	}
	return false
}

// Expired returns, in id order, the spells whose own cooldown ended since the last call, and
// forgets them. The session turns them into cooldown_end events.
func (st *State) Expired() []uint32 {
	st.mu.Lock()
	defer st.mu.Unlock()
	now := st.now()
	var ids []uint32
	for id, c := range st.cds {
		if !c.end.After(now) {
			ids = append(ids, id)
			delete(st.cds, id)
		}
	}
	for cat, end := range st.cats {
		if !end.After(now) {
			delete(st.cats, cat)
		}
	}
	for cat, g := range st.gcd {
		if !g.end.After(now) {
			delete(st.gcd, cat)
		}
	}
	for id, until := range st.notReady {
		if !until.After(now) {
			delete(st.notReady, id)
		}
	}
	sort.Slice(ids, func(i, j int) bool { return ids[i] < ids[j] })
	return ids
}

// Remaining says how long until spell id can be cast, 0 when it can, and why not. It checks
// what Spell::CheckCast does: the global cooldown of the spell's group (HasGlobalCooldown,
// Unit.cpp:90-94), the spell's own cooldown and its category's.
func (st *State) Remaining(id uint32) (time.Duration, string) {
	st.mu.Lock()
	defer st.mu.Unlock()
	return st.remaining(id, st.now())
}

func (st *State) remaining(id uint32, now time.Time) (time.Duration, string) {
	var best time.Duration
	var why string
	hold := func(end time.Time, w string) {
		if d := end.Sub(now); d > best {
			best, why = d, w
		}
	}
	if c, ok := st.cds[id]; ok {
		hold(c.end, WhyCooldown)
	}
	if until, ok := st.notReady[id]; ok {
		hold(until, WhyNotReady)
	}
	if m, ok := spells.Lookup(id); ok {
		if m.Category != 0 {
			if end, ok := st.cats[m.Category]; ok {
				hold(end, WhyCategory)
			}
		}
		if g, ok := st.gcd[m.GCDCategory]; ok {
			hold(g.end, WhyGCD)
		}
	}
	return best, why
}

// Ready reports whether spell id is off cooldown and the GCD.
func (st *State) Ready(id uint32) bool {
	d, _ := st.Remaining(id)
	return d <= 0
}

// GCD is the longest global cooldown running: its length and what is left, 0 when none.
func (st *State) GCD() (total, left time.Duration) {
	st.mu.Lock()
	defer st.mu.Unlock()
	now := st.now()
	for _, g := range st.gcd {
		if d := g.end.Sub(now); d > left {
			total, left = g.end.Sub(g.start), d
		}
	}
	return total, left
}

// ---- the cast ----

// CastStarted records the player's own SMSG_SPELL_START: the spell, how long it casts (0 for
// an instant spell), its target, and whether it came from an item. The server starts the
// global cooldown of the spell's group now (Spell::TriggerGlobalCooldown, Spell.cpp:8411-8441):
// the GCD it started is returned (zero when the spell starts none).
func (st *State) CastStarted(spell, castMS uint32, target packet.GUID) (gcdCat uint32, gcdMS uint32) {
	st.mu.Lock()
	defer st.mu.Unlock()
	now := st.now()
	c := &castInfo{spell: spell, start: now, end: now.Add(ms(castMS)), target: target}
	if m, ok := spells.Lookup(spell); ok && m.GCDMS > 0 {
		st.gcd[m.GCDCategory] = gcdEntry{start: now, end: now.Add(ms(m.GCDMS))}
		c.gcdCat, c.gcdSet = m.GCDCategory, true
		gcdCat, gcdMS = m.GCDCategory, m.GCDMS
	}
	st.cast = c
	return gcdCat, gcdMS
}

// CastWent records the player's own SMSG_SPELL_GO: the cast is over and its cooldowns start
// (Spell::SendSpellCooldown runs before the packet, Spell.cpp:3888, :4046). Only a spell of the
// spellbook starts one: a triggered spell or an item's has its cooldown set elsewhere.
func (st *State) CastWent(spell uint32) (Started, bool) {
	st.mu.Lock()
	defer st.mu.Unlock()
	now := st.now()
	if st.cast != nil && st.cast.spell == spell {
		st.cast.done = true
		st.cast.end = now
	}
	st.last = lastCast{spell: spell, at: now}
	if !st.known[spell] {
		return Started{}, false
	}
	m, ok := spells.Lookup(spell)
	if !ok || m.Flags.Has(spells.FlagPassive) || m.Flags.Has(spells.FlagCooldownOnFade) {
		return Started{}, false
	}
	return st.startCooldown(spell, m, SourceCast, now)
}

// ChannelStarted records MSG_CHANNEL_START: the spell channels for durMS.
func (st *State) ChannelStarted(spell, durMS uint32) {
	st.mu.Lock()
	defer st.mu.Unlock()
	now := st.now()
	st.channel = &castInfo{spell: spell, start: now, end: now.Add(ms(durMS)), channel: true}
}

// ChannelUpdated records MSG_CHANNEL_UPDATE: leftMS is the time left, 0 when the channel ended.
// It reports whether a channel was running and has now ended.
func (st *State) ChannelUpdated(leftMS uint32) (ended bool, spell uint32) {
	st.mu.Lock()
	defer st.mu.Unlock()
	if st.channel == nil {
		return false, 0
	}
	spell = st.channel.spell
	if leftMS == 0 {
		st.channel = nil
		return true, spell
	}
	st.channel.end = st.now().Add(ms(leftMS))
	return false, spell
}

// CastDelayed pushes the end of the cast back by delayMS (SMSG_SPELL_DELAYED).
func (st *State) CastDelayed(delayMS uint32) {
	st.mu.Lock()
	defer st.mu.Unlock()
	if st.cast != nil && !st.cast.done {
		st.cast.end = st.cast.end.Add(ms(delayMS))
	}
}

// CastFailed records that the server refused or broke the player's cast of spell: code is a
// SpellCastResult, or game.CastFailedInterrupted for the interrupt broadcast. A cast in
// progress ends and the GCD it started is cancelled (Spell::CancelGlobalCooldown,
// Spell.cpp:8443-8458). A refusal as not ready holds the spell back for NotReadyHold.
// It returns the failure as stored.
func (st *State) CastFailed(spell uint32, code uint8) (reason string) {
	st.mu.Lock()
	defer st.mu.Unlock()
	now := st.now()
	reason = game.CastFailName(code)
	if st.cast != nil && st.cast.spell == spell && !st.cast.done {
		if st.cast.gcdSet {
			delete(st.gcd, st.cast.gcdCat)
		}
		st.cast = nil
	}
	if st.channel != nil && st.channel.spell == spell {
		st.channel = nil
	}
	if code == game.CastFailedNotReady {
		st.notReady[spell] = now.Add(NotReadyHold)
	}
	st.failCount++
	st.fail = &lastFail{spell: spell, code: code, name: reason, at: now}
	return reason
}

// Cast is the state of the player's cast.
type Cast struct {
	State     string      `json:"state"` // "idle", "casting" or "channelling"
	SpellID   uint32      `json:"spell_id,omitempty"`
	Name      string      `json:"name,omitempty"`
	Target    packet.GUID `json:"target,omitempty"`
	CastMS    uint32      `json:"cast_ms,omitempty"`    // the cast time or channel length the server gave
	ElapsedMS int64       `json:"elapsed_ms,omitempty"` // since it started
	EndsInMS  *int64      `json:"ends_in_ms,omitempty"` // to its end; negative a little past it, while the spell goes off
}

// Failure is the last cast the server refused or broke.
type Failure struct {
	SpellID    uint32 `json:"spell_id"`
	Name       string `json:"name,omitempty"`
	Reason     uint8  `json:"reason"`      // SpellCastResult
	ReasonName string `json:"reason_name"` // out_of_range, no_power, interrupted, ...
	AgoMS      int64  `json:"ago_ms"`
}

// Done is the last cast that went off.
type Done struct {
	SpellID uint32 `json:"spell_id"`
	Name    string `json:"name,omitempty"`
	AgoMS   int64  `json:"ago_ms"`
}

// CastState is the player's cast, its last success and its last failure.
type CastState struct {
	Cast        Cast     `json:"cast"`
	LastCast    *Done    `json:"last_cast"`
	LastFailure *Failure `json:"last_failure"`
	Failures    uint64   `json:"failures"` // how many casts failed since login
}

// CastState reads the cast state. A cast whose end passed more than 2 s ago, with no SMSG_SPELL_GO
// or failure, is idle again (the server sends none for a caster that went out of range meanwhile).
func (st *State) CastState() CastState {
	st.mu.Lock()
	defer st.mu.Unlock()
	now := st.now()
	out := CastState{Cast: Cast{State: "idle"}, Failures: st.failCount}
	view := func(c *castInfo, state string) {
		ends := c.end.Sub(now).Milliseconds()
		out.Cast = Cast{State: state, SpellID: c.spell, Name: spells.Name(c.spell), Target: c.target,
			CastMS: uint32(c.end.Sub(c.start) / time.Millisecond), ElapsedMS: now.Sub(c.start).Milliseconds(), EndsInMS: &ends}
	}
	switch {
	case st.channel != nil && now.Before(st.channel.end.Add(castGrace)):
		view(st.channel, "channelling")
	case st.cast != nil && !st.cast.done && st.cast.end.After(st.cast.start) && now.Before(st.cast.end.Add(castGrace)):
		view(st.cast, "casting")
	}
	if st.last.spell != 0 {
		out.LastCast = &Done{SpellID: st.last.spell, Name: spells.Name(st.last.spell), AgoMS: now.Sub(st.last.at).Milliseconds()}
	}
	if f := st.fail; f != nil {
		out.LastFailure = &Failure{SpellID: f.spell, Name: spells.Name(f.spell), Reason: f.code, ReasonName: f.name, AgoMS: now.Sub(f.at).Milliseconds()}
	}
	return out
}

// Casting reports whether the player is casting or channelling.
func (st *State) Casting() bool { return st.CastState().Cast.State != "idle" }
