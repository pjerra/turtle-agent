package spellstate

import (
	"sync"
	"testing"
	"time"

	"github.com/pjerra/turtle-agent/internal/game"
)

// The spells below, with their template numbers (internal/spells/meta_test.go):
//
//	116/205/837 Frostbolt ranks 1-3        2136 Fire Blast: 8 s category 19, GCD 1.5 s
//	133 Fireball: 1.5 s cast, GCD 1.5 s    78 Heroic Strike: no GCD, no cooldown
//	2687 Bloodrage: 60 s own cooldown       6552 Pummel and 2139 Counterspell share category 88
const (
	frostbolt1, frostbolt2, frostbolt3 = 116, 205, 837
	fireBlast, fireball                = 2136, 133
	heroicStrike, bloodrage            = 78, 2687
	pummel, counterspell               = 6552, 2139
)

type clock struct{ t time.Time }

func (c *clock) now() time.Time          { return c.t }
func (c *clock) advance(d time.Duration) { c.t = c.t.Add(d) }

func newState(known ...uint32) (*State, *clock) {
	c := &clock{t: time.Unix(1_700_000_000, 0)}
	st := New(c.now)
	st.SetInitial(game.InitialSpells{Spells: known})
	return st, c
}

func TestSpellbookAndRanks(t *testing.T) {
	st, _ := newState(frostbolt1, heroicStrike)
	if !st.Loaded() || !st.Known(frostbolt1) || st.Known(frostbolt2) {
		t.Fatal("the initial spellbook is wrong")
	}
	// Learning a rank: the trainer's SMSG_LEARNED_SPELL, then the server supersedes the old rank.
	if !st.Learn(frostbolt2) || st.Learn(frostbolt2) {
		t.Error("Learn must report a new spell once")
	}
	st.Supersede(frostbolt1, frostbolt2)
	if st.Known(frostbolt1) || !st.Known(frostbolt2) {
		t.Error("the superseded rank must leave the spellbook")
	}
	st.Learn(frostbolt1) // a lower rank next to a higher one: Highest still picks by rank
	st.Learn(frostbolt3)
	if id, ok := st.Highest("frostbolt"); !ok || id != frostbolt3 {
		t.Errorf("Highest = %d %v, want %d", id, ok, frostbolt3)
	}
	if id, ok := st.Highest("  FROSTBOLT "); !ok || id != frostbolt3 {
		t.Errorf("Highest is not case-insensitive: %d %v", id, ok)
	}
	if r := st.Ranks("Frostbolt"); len(r) != 3 || r[0] != frostbolt1 || r[1] != frostbolt2 || r[2] != frostbolt3 {
		t.Errorf("Ranks = %v", r)
	}
	if _, ok := st.Highest("Pyroblast"); ok {
		t.Error("Highest found a spell that is not known")
	}
	if !st.Remove(frostbolt3) || st.Remove(frostbolt3) {
		t.Error("Remove must report a removed spell once")
	}
	if id, _ := st.Highest("Frostbolt"); id != frostbolt2 {
		t.Errorf("after the removal Highest = %d", id)
	}
	if ids := st.KnownIDs(); len(ids) != 3 || ids[0] != heroicStrike {
		t.Errorf("KnownIDs = %v", ids)
	}
}

func TestCastStartsGCDAndCooldown(t *testing.T) {
	st, c := newState(fireBlast, fireball, heroicStrike)
	if !st.Ready(fireBlast) || !st.Ready(fireball) {
		t.Fatal("a fresh spellbook must be ready")
	}
	// SMSG_SPELL_START of an instant spell: the GCD of category 133 starts.
	cat, ms := st.CastStarted(fireBlast, 0, 0)
	if cat != 133 || ms != 1500 {
		t.Fatalf("GCD %d/%d", cat, ms)
	}
	left, why := st.Remaining(fireball)
	if left != 1500*time.Millisecond || why != WhyGCD {
		t.Errorf("Fireball during the GCD: %v %s", left, why)
	}
	// Heroic Strike has no GCD category, so the GCD does not hold it back.
	if !st.Ready(heroicStrike) {
		t.Error("Heroic Strike must ignore the GCD")
	}
	if total, left := st.GCD(); total != 1500*time.Millisecond || left != 1500*time.Millisecond {
		t.Errorf("GCD() = %v %v", total, left)
	}
	// SMSG_SPELL_GO: the cooldown starts, from the category (Fire Blast has none of its own).
	started, ok := st.CastWent(fireBlast)
	if !ok || started.Spell != fireBlast || started.MS != 8000 || started.Category != 19 || started.Source != SourceCast {
		t.Fatalf("started %+v %v", started, ok)
	}
	c.advance(1500 * time.Millisecond)
	if !st.Ready(fireball) {
		t.Error("Fireball must be ready when the GCD ended")
	}
	if left, why := st.Remaining(fireBlast); left != 6500*time.Millisecond || why != WhyCooldown {
		t.Errorf("Fire Blast after 1.5 s: %v %s", left, why)
	}
	if got := st.Expired(); len(got) != 0 {
		t.Errorf("Expired too early: %v", got)
	}
	c.advance(6500 * time.Millisecond)
	if got := st.Expired(); len(got) != 1 || got[0] != fireBlast {
		t.Errorf("Expired = %v", got)
	}
	if !st.Ready(fireBlast) || len(st.Expired()) != 0 {
		t.Error("an expired cooldown must be gone, and reported once")
	}
}

func TestOwnCooldownAndUnknownSpells(t *testing.T) {
	st, c := newState(bloodrage)
	st.CastStarted(bloodrage, 0, 0)
	if _, left := st.GCD(); left != 0 {
		t.Error("Bloodrage starts no GCD")
	}
	if s, ok := st.CastWent(bloodrage); !ok || s.MS != 60000 || s.Category != 0 {
		t.Errorf("%+v %v", s, ok)
	}
	c.advance(59 * time.Second)
	if left, why := st.Remaining(bloodrage); left != time.Second || why != WhyCooldown {
		t.Errorf("%v %s", left, why)
	}
	// A spell that is not in the spellbook (an item's, a proc) starts no cooldown here.
	if _, ok := st.CastWent(pummel); ok {
		t.Error("an unknown spell started a cooldown")
	}
	// A spell with nothing in the table does nothing, and is always ready.
	if _, ok := st.StartCooldown(3999999, SourceEvent); ok || !st.Ready(3999999) {
		t.Error("a spell the table does not hold must not hold anything")
	}
}

func TestCategoryIsShared(t *testing.T) {
	st, c := newState(pummel, counterspell)
	st.CastStarted(pummel, 0, 0)
	st.CastWent(pummel)
	c.advance(2 * time.Second)
	left, why := st.Remaining(counterspell)
	if why != WhyCategory || left != 8*time.Second {
		t.Errorf("Counterspell after Pummel: %v %s", left, why)
	}
	st.Clear(pummel) // SMSG_CLEAR_COOLDOWN drops the lock the cast held
	if !st.Ready(counterspell) || !st.Ready(pummel) {
		t.Error("Clear must release the category")
	}
}

func TestNotReadyHold(t *testing.T) {
	st, c := newState(fireball)
	if reason := st.CastRefused(fireball, game.CastFailedNotReady); reason != "not_ready" {
		t.Errorf("reason %q", reason)
	}
	if left, why := st.Remaining(fireball); left != NotReadyHold || why != WhyNotReady {
		t.Errorf("%v %s", left, why)
	}
	c.advance(NotReadyHold)
	if !st.Ready(fireball) {
		t.Error("the hold must end")
	}
	st.CastRefused(fireball, 89)
	if !st.Ready(fireball) {
		t.Error("only a not-ready refusal holds a spell back")
	}
}

func TestFailedCastCancelsItsGCD(t *testing.T) {
	st, _ := newState(fireball, fireBlast)
	st.CastStarted(fireball, 1500, 0x77)
	if st.Ready(fireBlast) {
		t.Fatal("the cast must have started the GCD")
	}
	// A failure of another spell leaves the running cast and its GCD alone.
	st.CastRefused(fireBlast, 89)
	if st.Ready(fireBlast) || st.CastState().Cast.State != "casting" {
		t.Error("another spell's failure ended the cast")
	}
	st.CastBroken(fireball)
	if !st.Ready(fireBlast) {
		t.Error("an interrupted cast must give the GCD back")
	}
	if cs := st.CastState(); cs.Cast.State != "idle" || cs.LastFailure == nil || cs.LastFailure.ReasonName != "interrupted" || cs.Failures != 2 {
		t.Errorf("%+v", cs)
	}
}

func TestCastState(t *testing.T) {
	st, c := newState(fireball)
	if cs := st.CastState(); cs.Cast.State != "idle" || cs.LastCast != nil || cs.LastFailure != nil {
		t.Fatalf("%+v", cs)
	}
	st.CastStarted(fireball, 3000, 0x77)
	c.advance(1000 * time.Millisecond)
	cs := st.CastState()
	if cs.Cast.State != "casting" || cs.Cast.SpellID != fireball || cs.Cast.Name != "Fireball" || cs.Cast.Target != 0x77 ||
		*cs.Cast.EndsInMS != 2000 || cs.Cast.ElapsedMS != 1000 || cs.Cast.CastMS != 3000 {
		t.Fatalf("%+v", cs.Cast)
	}
	st.CastDelayed(500) // pushback
	if cs := st.CastState(); *cs.Cast.EndsInMS != 2500 {
		t.Errorf("pushback: %d", *cs.Cast.EndsInMS)
	}
	st.CastWent(fireball)
	cs = st.CastState()
	if cs.Cast.State != "idle" || cs.LastCast == nil || cs.LastCast.SpellID != fireball || cs.LastCast.AgoMS != 0 {
		t.Errorf("after the go: %+v", cs)
	}
	c.advance(time.Second)
	if cs := st.CastState(); cs.LastCast.AgoMS != 1000 {
		t.Errorf("ago %d", cs.LastCast.AgoMS)
	}
	// An instant spell is never "casting".
	st.CastStarted(heroicStrike, 0, 0)
	if st.Casting() {
		t.Error("an instant spell is not a cast")
	}
	// A cast whose go never came is idle again 2 s past its end.
	st.CastStarted(fireball, 1000, 0)
	c.advance(2900 * time.Millisecond)
	if !st.Casting() {
		t.Error("the grace period ended early")
	}
	c.advance(200 * time.Millisecond)
	if st.Casting() {
		t.Error("a cast without a go must time out")
	}
}

func TestChannel(t *testing.T) {
	st, c := newState()
	if ended, _ := st.ChannelUpdated(0); ended {
		t.Error("no channel was running")
	}
	st.ChannelStarted(5143, 5000) // Arcane Missiles
	c.advance(time.Second)
	cs := st.CastState()
	if cs.Cast.State != "channelling" || cs.Cast.SpellID != 5143 || *cs.Cast.EndsInMS != 4000 || cs.Cast.CastMS != 5000 {
		t.Fatalf("%+v", cs.Cast)
	}
	if ended, _ := st.ChannelUpdated(2500); ended {
		t.Error("a channel with time left has not ended")
	}
	if cs := st.CastState(); *cs.Cast.EndsInMS != 2500 {
		t.Errorf("update: %d", *cs.Cast.EndsInMS)
	}
	if ended, spell := st.ChannelUpdated(0); !ended || spell != 5143 {
		t.Errorf("end: %v %d", ended, spell)
	}
	if st.Casting() {
		t.Error("still channelling after the end")
	}
	// A broken channel: the interrupt names the channel's spell.
	st.ChannelStarted(5143, 5000)
	st.CastBroken(5143)
	if st.Casting() {
		t.Error("an interrupted channel is over")
	}
}

func TestServerCooldownsAndLogin(t *testing.T) {
	st, c := newState(fireball)
	// A school lockout after an interrupt.
	if s := st.ServerCooldown(fireball, 4000); s.Source != SourceServer || s.MS != 4000 {
		t.Errorf("%+v", s)
	}
	if left, why := st.Remaining(fireball); left != 4*time.Second || why != WhyCooldown {
		t.Errorf("%v %s", left, why)
	}
	c.advance(4 * time.Second)
	if got := st.Expired(); len(got) != 1 {
		t.Errorf("Expired = %v", got)
	}

	// SMSG_INITIAL_SPELLS: a running cooldown, a category lock, an infinite one, and an item's.
	st.SetInitial(game.InitialSpells{Spells: []uint32{fireBlast, bloodrage, pummel, counterspell}, Cooldowns: []game.InitialCooldown{
		{Spell: fireBlast, Category: 19, Cooldown: 5000, CategoryCooldown: 5000},
		{Spell: pummel, Category: 88, Cooldown: 0, CategoryCooldown: 7000},
		{Spell: bloodrage, Cooldown: 1, CategoryCooldown: game.InfiniteCooldown},
		{Spell: 5, Item: 6948, Cooldown: 30000},
	}})
	if left, _ := st.Remaining(fireBlast); left != 5*time.Second {
		t.Errorf("Fire Blast %v", left)
	}
	if left, why := st.Remaining(counterspell); left != 7*time.Second || why != WhyCategory {
		t.Errorf("Counterspell %v %s", left, why)
	}
	if left, _ := st.Remaining(bloodrage); left < 24*time.Hour {
		t.Errorf("an infinite cooldown ran out: %v", left)
	}
	if !st.Ready(5) {
		t.Error("an item's cooldown is not tracked")
	}
	// SMSG_COOLDOWN_CHEAT.
	if ids := st.ClearAll(); len(ids) != 3 {
		t.Errorf("ClearAll = %v", ids)
	}
	if !st.Ready(fireBlast) || !st.Ready(bloodrage) || !st.Ready(counterspell) {
		t.Error("ClearAll left a cooldown")
	}
}

func TestStartCooldownFromEvent(t *testing.T) {
	st, _ := newState(bloodrage)
	s, ok := st.StartCooldown(bloodrage, SourceEvent)
	if !ok || s.MS != 60000 || s.Source != SourceEvent {
		t.Fatalf("%+v %v", s, ok)
	}
	if !st.Clear(bloodrage) || st.Clear(bloodrage) {
		t.Error("Clear must say whether a cooldown was running")
	}
}

// The state is written by the session's loop and read by RPC calls.
func TestConcurrentUse(t *testing.T) {
	st, _ := newState(fireball, fireBlast, frostbolt1)
	var wg sync.WaitGroup
	for i := 0; i < 4; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < 200; j++ {
				st.CastStarted(fireBlast, 0, 0)
				st.CastWent(fireBlast)
				st.CastRefused(fireball, 89)
				st.ChannelStarted(5143, 100)
			}
		}()
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < 200; j++ {
				st.Ready(fireBlast)
				st.CastState()
				st.GCD()
				st.Highest("Frostbolt")
				st.Expired()
			}
		}()
	}
	wg.Wait()
}

// A refusal of a new attempt comes from a fresh Spell object and does not touch the cast that runs: the cast
// and its GCD stay (Spell.cpp:3545-3551, :8451).
func TestRefusalOfANewAttemptKeepsTheRunningCast(t *testing.T) {
	st, c := newState(fireball, fireBlast)
	st.CastStarted(fireball, 3000, 0x77)
	c.advance(500 * time.Millisecond)
	// The same spell sent again while it casts: SPELL_FAILED_SPELL_IN_PROGRESS.
	st.CastRefused(fireball, game.CastFailedInProgress)
	cs := st.CastState()
	if cs.Cast.State != "casting" || cs.Cast.SpellID != fireball || cs.LastFailure.ReasonName != "spell_in_progress" {
		t.Fatalf("%+v", cs)
	}
	if left, why := st.Remaining(fireBlast); left != 1000*time.Millisecond || why != WhyGCD {
		t.Errorf("the GCD of the running cast was given back: %v %s", left, why)
	}
	// Every other refusal of an attempt (out of range, no power, moving) is the same.
	st.CastRefused(fireball, 89)
	st.CastRefused(fireball, 77)
	if !st.Casting() {
		t.Error("a refusal ended the running cast")
	}
	// An instant spell sent again between its start and its go: not ready, and its GCD stays.
	st2, _ := newState(fireBlast, fireball)
	st2.CastStarted(fireBlast, 0, 0)
	st2.CastRefused(fireBlast, game.CastFailedNotReady)
	if left, why := st2.Remaining(fireball); left != 1500*time.Millisecond || why != WhyGCD {
		t.Errorf("the GCD went: %v %s", left, why)
	}
	if s, ok := st2.CastWent(fireBlast); !ok || s.MS != 8000 {
		t.Errorf("the go still starts the cooldown: %+v %v", s, ok)
	}
	// Only SMSG_SPELL_FAILED_OTHER ends the cast, and the pair of an interrupt counts once.
	st.CastBroken(fireball)
	st.CastRefused(fireball, game.CastFailedInterrupted)
	cs = st.CastState()
	if cs.Cast.State != "idle" || !st.Ready(fireBlast) || cs.LastFailure.ReasonName != "interrupted" {
		t.Errorf("%+v", cs)
	}
	if cs.Failures != 4 { // in progress, out of range, no power, and the interrupt pair
		t.Errorf("%d failures", cs.Failures)
	}
}

// A spell held back while its aura is up (SPELL_ATTR_DISABLED_WHILE_ACTIVE) reads not ready from its go
// until SMSG_COOLDOWN_EVENT starts the real cooldown; Cold Blood has a cooldown of its own, Stealth a category's.
func TestCooldownOnFadeSpells(t *testing.T) {
	const coldBlood, stealth = 14177, 1784
	st, c := newState(coldBlood, stealth)
	for _, id := range []uint32{coldBlood, stealth} {
		st.CastStarted(id, 0, 0)
		if s, ok := st.CastWent(id); ok {
			t.Errorf("%d: the cast started a timed cooldown: %+v", id, s)
		}
		if left, why := st.Remaining(id); left < 24*time.Hour || why != WhyCooldown {
			t.Errorf("%d: ready while its aura is up: %v %s", id, left, why)
		}
	}
	if st.Pending() {
		t.Error("an aura-held cooldown must not keep the replay ticking")
	}
	c.advance(time.Hour)
	if len(st.Expired()) != 0 || st.Ready(coldBlood) {
		t.Error("the aura-held cooldown ended by itself")
	}
	// The aura fades: the server says so, and the real cooldown starts.
	if s, ok := st.StartCooldown(coldBlood, SourceEvent); !ok || s.MS != 180000 {
		t.Fatalf("%+v %v", s, ok)
	}
	if left, _ := st.Remaining(coldBlood); left != 180*time.Second {
		t.Errorf("Cold Blood %v", left)
	}
	if s, ok := st.StartCooldown(stealth, SourceEvent); !ok || s.MS != 10000 {
		t.Fatalf("%+v %v", s, ok)
	}
	if left, why := st.Remaining(stealth); left != 10*time.Second || why != WhyCooldown {
		t.Errorf("Stealth %v %s", left, why)
	}
}
