package dungeon

import (
	"context"
	"strings"
	"testing"
	"time"
)

// rezFakeBrain is a leader's brain that can resurrect while can is set (the healer presets, step 4e).
type rezFakeBrain struct {
	*fakeBrain
	can   bool
	asked int
}

func (b *rezFakeBrain) CanRez() bool { b.asked++; return b.can }

func deadHealerRun(t *testing.T, cfg Config, can bool, rezAfter time.Duration) (*Driver, *fakeGame, *rezFakeBrain, error) {
	t.Helper()
	g, p := runFixture(t)
	g.party[1].Dead, g.party[1].Health = true, 0 // dead before the run: not a death of the run
	if rezAfter > 0 {
		g.revAt = map[string]time.Time{"Heal": g.now.Add(rezAfter)} // the brain's resurrection lands (the member accepts) this long into the gate
	}
	br := &rezFakeBrain{fakeBrain: &fakeBrain{}, can: can}
	cfg.Brain, cfg.BrainName = br, "holy_paladin"
	d := NewDriver(p, g, cfg, g.clock())
	err := d.Run(context.Background(), 0)
	return d, g, br, err
}

func TestRestGateWaitsForTheBrainsResurrectionInsteadOfAGMRevive(t *testing.T) {
	d, g, br, err := deadHealerRun(t, Config{Shortcuts: true}, true, 9*time.Second)
	if err != nil {
		t.Fatalf("%v", err)
	}
	rep := d.Report()
	if len(rep.Shortcuts) != 0 || strings.Contains(strings.Join(g.gms, "|"), ".revive") {
		t.Fatalf("the brain's resurrection was preferred, yet a GM revive went out: %+v %q", rep.Shortcuts, g.gms)
	}
	if !strings.Contains(strings.Join(rep.Notes, "|"), "waiting up to 120 s for the leader's brain to resurrect") || br.asked == 0 {
		t.Fatalf("notes %q asked %d", rep.Notes, br.asked)
	}
	if g.party[1].Dead {
		t.Fatal("the member is still dead")
	}
}

func TestRestGateWithoutShortcutsOrWipeRecoveryRunsOnAfterTheBrainsResurrection(t *testing.T) {
	// Without it the gate stops the run: "Heal is dead and GM shortcuts are off".
	if _, _, _, err := deadHealerRun(t, Config{}, false, 9*time.Second); err == nil || !strings.Contains(err.Error(), "GM shortcuts are off") {
		t.Fatalf("%v", err)
	}
	if _, _, _, err := deadHealerRun(t, Config{}, true, 9*time.Second); err != nil {
		t.Fatalf("%v", err)
	}
}

func TestRestGateBoundsTheWaitForTheBrain(t *testing.T) {
	// The brain says it can, but nothing lands: after BrainRezWait the old answer (the GM revive) takes over.
	d, g, _, err := deadHealerRun(t, Config{Shortcuts: true, BrainRezWait: 20 * time.Second}, true, 0)
	if err != nil {
		t.Fatalf("%v", err)
	}
	rep := d.Report()
	if len(rep.Shortcuts) != 1 || rep.Shortcuts[0].Kind != "revive" || !strings.Contains(strings.Join(g.gms, "|"), ".revive Heal") {
		t.Fatalf("%+v %q", rep.Shortcuts, g.gms)
	}
}

func TestRestGateDoesNotWaitForABrainThatCannotResurrect(t *testing.T) {
	d, g, br, err := deadHealerRun(t, Config{Shortcuts: true}, false, 0)
	if err != nil {
		t.Fatalf("%v", err)
	}
	if len(d.Report().Shortcuts) != 1 || !strings.Contains(strings.Join(g.gms, "|"), ".revive Heal") || br.asked == 0 {
		t.Fatalf("%+v %q asked %d", d.Report().Shortcuts, g.gms, br.asked)
	}
	if strings.Contains(strings.Join(d.Report().Notes, "|"), "waiting up to") {
		t.Fatalf("%q", d.Report().Notes)
	}
}
