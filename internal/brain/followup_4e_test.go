package brain

import (
	"sync"
	"testing"
	"time"

	"github.com/pjerra/turtle-agent/internal/objects"
	"github.com/pjerra/turtle-agent/internal/packet"
)

// The follow-ups of the review of #102 (druid and shaman), step 4e of #80.

// ---- a totem is recorded when its spell went off, not when it was sent ----

func TestATotemRefusedByTheServerLeavesNoRecordAndIsDroppedAgain(t *testing.T) {
	r, g := totemRig(t)
	g.slow = true // the cast is sent and the server's answer (SPELL_GO or a refusal) comes later
	d := r.tick(100 * time.Millisecond)
	if d == nil || d.Action != "drop_totem" || d.Spell != shSearing4 {
		t.Fatalf("%+v", d)
	}
	if rec := r.b.mem.sham.totem(elFire); rec.spell != 0 {
		t.Fatalf("recorded before the spell went off: %+v", rec)
	}
	g.st.CastRefused(shSearing4, codeOf(t, "no_power"))
	r.tick(100 * time.Millisecond) // the refusal is seen: the pending cast is forgotten and the spell is held for a moment
	if r.b.mem.sham.pending(elFire) {
		t.Fatal("still pending after the refusal")
	}
	// The slot is empty again: the fire totem is tried again after the refusal's hold (not the next element, as with a record in place).
	d = r.tick(3 * time.Second)
	if d == nil || d.Action != "drop_totem" || d.Spell != shSearing4 {
		t.Fatalf("the refused totem is dropped again: %+v idle %q", d, r.idle())
	}
	if rec := r.b.mem.sham.totem(elFire); rec.spell != 0 {
		t.Fatalf("%+v", rec)
	}
}

func TestATotemIsRecordedAtSpellGoWithItsOwnTime(t *testing.T) {
	r, g := totemRig(t)
	g.slow = true
	r.tick(100 * time.Millisecond)
	g.clk.advance(300 * time.Millisecond)
	g.st.CastWent(shSearing4)
	g.clk.advance(200 * time.Millisecond)
	g.slow = false
	totemUnit(g.fakeGame, searingG, "Searing Totem IV")
	d := r.tick(100 * time.Millisecond) // the pending cast turns into the record, and the next slot is filled
	if d == nil || d.Spell != shManaSpring2 {
		t.Fatalf("%+v", d)
	}
	rec := r.b.mem.sham.totem(elFire)
	if rec.spell != shSearing4 || rec.name != "Searing Totem" {
		t.Fatalf("%+v", rec)
	}
	if age := g.clk.Now().Sub(rec.at); age < 250*time.Millisecond || age > 450*time.Millisecond {
		t.Fatalf("recorded at the time of the spell going off, %v ago", age)
	}
	if r.b.mem.sham.pending(elFire) {
		t.Fatal("still pending")
	}
	// The record counts: the manager never went back to this slot.
	if got := totemCasts(g.fakeGame); !eq(got, []uint32{shSearing4, shManaSpring2}) {
		t.Fatalf("totems %v", got)
	}
}

func TestATotemCastThatNeverAnswersIsGivenUp(t *testing.T) {
	r, g := totemRig(t)
	g.slow = true
	r.tick(100 * time.Millisecond)
	if !r.b.mem.sham.pending(elFire) {
		t.Fatal("not pending")
	}
	// A pending cast holds its slot; after totemPendTTL it is forgotten and the slot is filled again.
	if d := r.tick(1 * time.Second); d != nil && d.Action == "drop_totem" && d.Spell == shSearing4 {
		t.Fatalf("a second drop into the pending slot: %+v", d)
	}
	if d := r.tick(totemPendTTL); d == nil || d.Action != "drop_totem" || d.Spell != shSearing4 {
		t.Fatalf("the slot is filled again once the pending cast is given up: %+v idle %q", d, r.idle())
	}
	if rec := r.b.mem.sham.totem(elFire); rec.spell != 0 {
		t.Fatalf("%+v", rec)
	}
}

// ---- the weapon imbue follows the weapon in the main hand ----

// mhGame is a melee fake whose main-hand weapon the test sets (brain.MainHandReader).
type mhGame struct {
	*meleeGame
	mu   sync.Mutex
	mh   MainHand
	seen bool
}

func (m *mhGame) MainHand() (MainHand, bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.mh, m.seen
}

func (m *mhGame) hold(item packet.GUID, enchant uint32) {
	m.mu.Lock()
	m.mh, m.seen = MainHand{Item: item, TempEnchant: enchant}, true
	m.mu.Unlock()
}

func mhImbueRig(t *testing.T) (*rig, *mhGame) {
	t.Helper()
	g := newFake(append(append([]uint32{}, shEnhKnown...), shShields...)...)
	shamanSelf(g)
	m := &mhGame{meleeGame: newMeleeGame(g)}
	m.hold(100, 0)
	return rigOn(t, m, g, Params{Strategies: []string{"enhancement_shaman"}}), m
}

func TestImbueIsRenewedWhenTheWeaponIsAnotherOneOrTheImbueIsGone(t *testing.T) {
	r, m := mhImbueRig(t)
	if d := r.tick(100 * time.Millisecond); d == nil || d.Action != "imbue" || len(m.imbueList()) != 1 {
		t.Fatalf("%+v", d)
	}
	m.hold(100, 5000) // the item's update shows the enchantment
	if d := r.tick(30 * time.Second); d != nil {
		t.Fatalf("imbued weapon: %+v", d)
	}
	// A weapon swap: the new one is not imbued, the timer has half an hour to go.
	m.hold(200, 0)
	if d := r.tick(2 * time.Second); d == nil || d.Action != "imbue" || len(m.imbueList()) != 2 {
		t.Fatalf("after a weapon swap: %+v idle %q", d, r.idle())
	}
	// The same weapon and the enchantment gone (it ran out, or was lost): again, but not before the item's update has had time to show.
	m.hold(200, 5000)
	r.tick(30 * time.Second)
	m.hold(200, 0)
	if d := r.tick(2 * time.Second); d == nil || d.Action != "imbue" || len(m.imbueList()) != 3 {
		t.Fatalf("a lost imbue: %+v idle %q", d, r.idle())
	}
	m.hold(200, 0) // the update has not come yet
	if d := r.tick(3 * time.Second); d != nil {
		t.Fatalf("again within the time the update takes: %+v", d)
	}
	// And the swap back to the first weapon.
	m.hold(100, 5000)
	if d := r.tick(30 * time.Second); d == nil || d.Action != "imbue" || len(m.imbueList()) != 4 {
		t.Fatalf("%+v", d)
	}
}

func TestImbueFallsBackToTheTimerWhenTheServerNeverShowsTheEnchantment(t *testing.T) {
	r, m := mhImbueRig(t) // the field reads 0 whatever is done
	r.tick(100 * time.Millisecond)
	if d := r.tick(5 * time.Minute); d != nil {
		t.Fatalf("an imbue every few seconds because the field is blind: %+v", d)
	}
	if len(m.imbueList()) != 1 {
		t.Fatalf("imbues %v", m.imbueList())
	}
	// A weapon swap is still seen.
	m.hold(300, 0)
	if d := r.tick(2 * time.Second); d == nil || d.Action != "imbue" || len(m.imbueList()) != 2 {
		t.Fatalf("%+v", d)
	}
	// The timer renews it as before.
	if d := r.tick(56 * time.Minute); d == nil || d.Action != "imbue" || len(m.imbueList()) != 3 {
		t.Fatalf("%+v", d)
	}
	// An empty hand: only the timer.
	r2, m2 := mhImbueRig(t)
	m2.mu.Lock()
	m2.seen = false
	m2.mu.Unlock()
	r2.tick(100 * time.Millisecond)
	if d := r2.tick(5 * time.Minute); d != nil || len(m2.imbueList()) != 1 {
		t.Fatalf("%+v %v", d, m2.imbueList())
	}
}

// ---- Mark of the Wild on a member it keeps failing on ----

func TestMarkOfTheWildSkipsAMemberItFailedOnTwiceFor60Seconds(t *testing.T) {
	r, g := restoRig(t)
	g.self.InCombat = false
	g.member(tankG, "Tankard", 1, 1000, 1000, 6, 0)
	g.mutate(tankG, func(u *objects.Unit) {
		u.InCombat = false
		u.Auras = []objects.Aura{{Name: "Thorns", SpellID: drThorns4}}
	})
	d := r.tick(100 * time.Millisecond)
	if d == nil || d.Action != "mark_of_the_wild" || d.Target != tankG {
		t.Fatalf("%+v idle %q", d, r.idle())
	}
	g.st.CastRefused(drMotw5, codeOf(t, "out_of_range"))
	// The first refusal: the spell is held for the refusal's three seconds and then tried again.
	if d := r.tick(2 * time.Second); d != nil && d.Action == "mark_of_the_wild" {
		t.Fatalf("%+v", d)
	}
	if d := r.tick(2 * time.Second); d == nil || d.Action != "mark_of_the_wild" || d.Target != tankG {
		t.Fatalf("one failure does not skip the member: %+v idle %q", d, r.idle())
	}
	g.st.CastRefused(drMotw5, codeOf(t, "out_of_range"))
	r.tick(2 * time.Second)
	// The second: the member is left alone, so the druid has nothing to buff and keeps its form.
	for i := 0; i < 5; i++ {
		if d := r.tick(5 * time.Second); d != nil && d.Action == "mark_of_the_wild" {
			t.Fatalf("tick %d: %+v", i, d)
		}
	}
	if motwPending(r.b.newCtx(g.clk.Now())) {
		t.Fatal("Mark of the Wild still pending: a feral druid would shift out of its form for it")
	}
	// After a minute it tries again.
	if d := r.tick(40 * time.Second); d == nil || d.Action != "mark_of_the_wild" || d.Target != tankG {
		t.Fatalf("%+v idle %q", d, r.idle())
	}
	// A member who got the buff has no count left.
	g.addAura(tankG, objects.Aura{Name: "Mark of the Wild", SpellID: drMotw5})
	r.tick(2 * time.Second)
	if n := len(r.b.mem.dru.fails); n != 0 {
		t.Fatalf("%d counts kept", n)
	}
}
