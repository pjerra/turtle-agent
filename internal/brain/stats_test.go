package brain

import (
	"context"
	"testing"
	"time"

	"github.com/pjerra/turtle-agent/internal/objects"
)

func TestStatsCountActionsRefusalsIdleAndMobsOnTheLeader(t *testing.T) {
	r := frost(t)
	r.g.addUnit(mob(mobA, tankG), 28, 0)
	r.g.setSelf(func(u *objects.Unit) { u.InCombat = true })
	r.tick(100 * time.Millisecond)
	r.tick(2200 * time.Millisecond) // frostbolt
	r.tick(1600 * time.Millisecond) // frostbolt
	// A refusal of the server: out of range.
	r.g.st.CastRefused(spFrostbolt, codeOf(t, "out_of_range"))
	r.tick(100 * time.Millisecond)
	// A mob has the leader; nothing to cast at it in melee range for a while (the cooldowns): idle seconds.
	r.g.mutate(mobA, func(u *objects.Unit) { u.Target = selfG })
	for i := 0; i < 5; i++ {
		r.tick(500 * time.Millisecond)
	}
	s := r.b.mem.stat.snapshot()
	if s.Actions["frostbolt"] < 2 || s.ActionsTotal() < 2 {
		t.Fatalf("%+v", s)
	}
	if s.CastFailed["out_of_range"] != 1 || s.CastFailedTotal() != 1 {
		t.Fatalf("%+v", s)
	}
	if s.CombatS < 3 || s.MobsOnMe != 1 || s.MobOnMeS <= 0 || s.MobOnMeS > s.CombatS {
		t.Fatalf("%+v", s)
	}
	if s.IdleCombatS > s.CombatS || s.MinPowerPct != 100 {
		t.Fatalf("%+v", s)
	}
	r.b.mem.stat.reset()
	if z := r.b.mem.stat.snapshot(); z.ActionsTotal() != 0 || z.CombatS != 0 || z.MobsOnMe != 0 || z.MinPowerPct != -1 {
		t.Fatalf("%+v", z)
	}
}

func TestStatsCountPowerSpentAndIdleInCombat(t *testing.T) {
	r := frost(t)
	r.g.addUnit(mob(mobA, tankG), 45, 0) // out of range: the mage waits
	r.g.setSelf(func(u *objects.Unit) { u.InCombat = true })
	r.tick(100 * time.Millisecond)
	for i := 0; i < 4; i++ {
		r.tick(time.Second)
	}
	s := r.b.mem.stat.snapshot()
	if s.IdleCombatS < 3 {
		t.Fatalf("a mage in combat with nothing to cast at idles: %+v", s)
	}
	r.g.setSelf(func(u *objects.Unit) { u.Power -= 100 })
	r.tick(time.Second)
	r.g.setSelf(func(u *objects.Unit) { u.Power += 300 }) // regeneration or a drink is not spending
	r.tick(time.Second)
	if s := r.b.mem.stat.snapshot(); s.PowerSpent != 100 {
		t.Fatalf("%+v", s)
	}
}

func TestControllerStatsWithoutABrainAreZero(t *testing.T) {
	ctl := NewController(context.Background(), newFake(), Config{})
	if s := ctl.Stats(); s.ActionsTotal() != 0 || s.MinPowerPct != -1 || s.Actions == nil {
		t.Fatalf("%+v", s)
	}
	ctl.ResetStats() // no brain: nothing to do
}
