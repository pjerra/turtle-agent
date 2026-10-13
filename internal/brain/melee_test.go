package brain

import (
	"context"
	"math"
	"testing"
	"time"

	"github.com/pjerra/turtle-agent/internal/objects"
)

// behind reports whether a point is at least rearMin round from the way the mob faces, and within the melee spot's rings.
func behind(m objects.Unit, p Vec3) bool {
	return math.Abs(angleDiff(float32(bearing(Vec3{m.X, m.Y, m.Z}, p)), m.O)) > rearMin && dist2(p, Vec3{m.X, m.Y, m.Z}) <= 4.7
}

func TestMeleeWalksBehindTheMobThatFacesIt(t *testing.T) {
	r, mf := newWarriorRig(t, "arms_warrior", stanceBattle, Params{})
	tankFights(r.g, 8, 4, math.Pi) // the mob at 4 yards faces west, at the warrior: a cleave, a breath, a tail swipe
	mf.setSwing(mobA)
	d := r.tick(100 * time.Millisecond)
	if d == nil || d.Action != "melee_reposition" || d.Trigger != "bad_melee_position" || d.Rel != RelMove {
		t.Fatalf("%+v idle %q", d, r.idle())
	}
	w := mf.walkList()
	m := r.g.units[1]
	if m.GUID != mobA {
		m = r.g.units[0]
	}
	if len(w) != 1 || !behind(m, w[0]) {
		t.Fatalf("walked to %v, which is not behind the mob at (%.0f,%.0f) facing %.2f", w, m.X, m.Y, m.O)
	}
	// Behind it, the warrior stays and fights: no second walk.
	setRage(r.g, 600)
	for i := 0; i < 4; i++ {
		r.tick(1600 * time.Millisecond)
	}
	if len(mf.walkList()) != 1 {
		t.Fatalf("walks %v", mf.walkList())
	}
	if got := casts(r.g); len(got) == 0 || got[0] != spMortal {
		t.Fatalf("it never struck from behind: %v", got)
	}
}

func TestMeleeLeavesAFlankAndABackAlone(t *testing.T) {
	for _, o := range []float32{0, math.Pi / 2, -math.Pi / 2, math.Pi / 3 * 2} { // the warrior stands west of the mob
		r, mf := newWarriorRig(t, "arms_warrior", stanceBattle, Params{})
		tankFights(r.g, 8, 4, o)
		mf.setSwing(mobA)
		setRage(r.g, 600)
		r.tick(100 * time.Millisecond)
		open(r)
		if len(mf.walkList()) != 0 {
			t.Errorf("mob facing %.2f: walked %v", o, mf.walkList())
		}
	}
}

func TestMeleeRunsInToAMobOutOfReachAndStopsBehindIt(t *testing.T) {
	r, mf := newWarriorRig(t, "arms_warrior", stanceBattle, Params{}, spBattleSt, spBattleShout, spMortal) // no Charge yet
	tankFights(r.g, 14, 12, 0)
	mf.setSwing(mobA)
	d := r.tick(100 * time.Millisecond)
	if d == nil || d.Action != "melee_reposition" {
		t.Fatalf("%+v idle %q", d, r.idle())
	}
	w := mf.walkList()
	m := r.g.units[1]
	if m.GUID != mobA {
		m = r.g.units[0]
	}
	if len(w) != 1 || !behind(m, w[0]) || w[0][0] >= m.X {
		t.Fatalf("walked to %v for a mob at (%.0f,%.0f) facing east", w, m.X, m.Y)
	}
}

func TestMeleeDoesNotWalkWhereItMayNotAndStillFights(t *testing.T) {
	r, mf := newWarriorRig(t, "arms_warrior", stanceBattle, Params{NoMove: true})
	tankFights(r.g, 8, 4, math.Pi)
	mf.setSwing(mobA)
	setRage(r.g, 600)
	r.tick(100 * time.Millisecond)
	d := open(r)
	if len(mf.walkList()) != 0 || d == nil || d.Action != "mortal_strike" {
		t.Fatalf("walks %v %+v", mf.walkList(), d)
	}
	// The dungeon driver holds the move lock: the same.
	r, mf = newWarriorRig(t, "arms_warrior", stanceBattle, Params{})
	rel, err := r.b.lock.Hold(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer rel()
	tankFights(r.g, 8, 4, math.Pi)
	mf.setSwing(mobA)
	r.tick(100 * time.Millisecond)
	open(r)
	if len(mf.walkList()) != 0 {
		t.Fatalf("walked under the driver's lock: %v", mf.walkList())
	}
}

func TestMeleeHoldsItsGroundWhenTheMobIsOnIt(t *testing.T) {
	r, mf := newWarriorRig(t, "arms_warrior", stanceBattle, Params{})
	m := mob(mobA, selfG)
	m.O = math.Pi
	r.g.addUnit(m, 4, 0)
	mf.setSwing(mobA)
	setRage(r.g, 600)
	r.g.setSelf(func(u *objects.Unit) { u.InCombat = true })
	d := r.tick(100 * time.Millisecond) // a mob that attacks the warrior is fought at once
	if d == nil || d.Action != "mortal_strike" || len(mf.walkList()) != 0 {
		t.Fatalf("%+v walks %v", d, mf.walkList())
	}
}

// A game that cannot walk or see the mesh: the melee fighter swings from where it stands, and repeats the attack order only now
// and then.
func TestMeleeWithoutAWalkerOrAMeleeLog(t *testing.T) {
	g := newFake(warriorSpells...)
	g.self.Class, g.self.Level, g.self.PowerType, g.self.MaxPower = ClassWarrior, 40, 1, 1000
	g.self.Auras = []objects.Aura{stanceAura("Battle Shout", spBattleShout), stanceAura("Battle Stance", spBattleSt)}
	tankFights(g, 8, 4, math.Pi)
	r := newRig(t, g, Params{Strategies: []string{"arms_warrior"}})
	r.tick(100 * time.Millisecond)
	if d := open(r); d == nil || d.Action != "auto_attack" || g.attacks != 1 {
		t.Fatalf("%+v attacks %d", d, g.attacks)
	}
	for i := 0; i < 5; i++ { // 5 s since the order
		r.tick(time.Second)
	}
	if g.attacks != 1 {
		t.Fatalf("the attack order was repeated after 5 s: %d", g.attacks)
	}
	r.tick(2 * time.Second) // 7 s since the order
	if g.attacks != 2 {
		t.Fatalf("the attack order was not repeated after the refresh time: %d", g.attacks)
	}
}
