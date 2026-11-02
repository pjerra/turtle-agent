package dungeon

import (
	"strings"
	"testing"

	"github.com/pjerra/turtle-agent/internal/tbm"
)

// The plan's floor is set at the band's middle level; the check scales it to the hired tank's own level with the test runs' curve (#266).
func TestScaledTankFloor(t *testing.T) {
	for _, c := range []struct {
		floor uint32
		mid   int
		level int
		want  uint32
	}{
		{3700, 47, 45, 3350}, // Maraudon Orange, a level-45 hire
		{3700, 47, 47, 3700}, // the band's middle: the floor as set
		{3700, 47, 50, 4200}, // the top of the band
		{800, 15, 13, 650},   // Ragefire Chasm below its middle
		{0, 47, 45, 0},       // no floor stays none
	} {
		if got := scaledTankFloor(c.floor, c.mid, c.level); got != c.want {
			t.Errorf("scaledTankFloor(%d, %d, %d) = %d, want %d", c.floor, c.mid, c.level, got, c.want)
		}
	}
}

// The curve's shape from the 2026-10 runs: health rises with level, levels 58 and 59 take 57's value, the gaps interpolate.
func TestTankCurve(t *testing.T) {
	if tankCurve(58) != tankCurve(57) || tankCurve(59) != tankCurve(57) {
		t.Errorf("levels 58 and 59: %v %v, level 57 %v", tankCurve(58), tankCurve(59), tankCurve(57))
	}
	if tankCurve(60) <= tankCurve(57) || tankCurve(47) <= tankCurve(31) || tankCurve(16) <= tankCurve(1) {
		t.Errorf("the curve does not rise: %v", tankCurve(60))
	}
	if got, want := tankCurve(44), tankCurve(42)+(tankCurve(45)-tankCurve(42))*2.0/3.0; got != want {
		t.Errorf("level 44 interpolates: %v, want %v", got, want)
	}
}

// A Maraudon Orange hire at level 45 with the test runs' median health (3450) is not under the scaled floor; a clearly weak one is.
func TestPreflightTankFloorScalesToTheHiredLevel(t *testing.T) {
	p := &Plan{Name: "maraudon_orange", LevelBand: []int{45, 50}, TankMinHP: 3700}
	d := &RemoteDriver{plan: p, opt: RemoteOptions{Tank: "Tanky"}}
	d.fr.Party = []tbm.PartyRec{{Name: "Tanky", MaxHP: 3450}}
	d.rep.update(func(r *Report) { r.Ready = &ReadyReport{Bots: []ReadyBot{{Name: "Tanky", Role: "tank", Level: 45}}} })
	pf := &Preflight{OK: true}
	d.checkPartyStrength(pf)
	if all := strings.Join(pf.Warnings, "\n"); strings.Contains(all, "max health is") {
		t.Fatalf("a level-45 tank at the test median was warned about (floor not scaled):\n%s", all)
	}

	// The same level with 3000: below the scaled floor (3350), a warning.
	d.fr.Party[0].MaxHP = 3000
	pf = &Preflight{OK: true}
	d.checkPartyStrength(pf)
	if all := strings.Join(pf.Warnings, "\n"); !strings.Contains(all, "the tank's max health is 3000, the plan wants at least 3350") {
		t.Fatalf("a weak level-45 tank was not warned about:\n%s", all)
	}
}

// A level-60 plan's floor assumes tier gear: the warning says so. The curve itself is untouched (a level-60 floor still scales).
func TestPreflightTankFloorAt60NamesTierGear(t *testing.T) {
	p := &Plan{Name: "onyxias_lair", LevelBand: []int{60, 60}, TankMinHP: 8500}
	d := &RemoteDriver{plan: p, opt: RemoteOptions{Tank: "Tanky"}}
	d.fr.Party = []tbm.PartyRec{{Name: "Tanky", MaxHP: 8000}}
	pf := &Preflight{OK: true}
	d.checkPartyStrength(pf)
	if all := strings.Join(pf.Warnings, "\n"); !strings.Contains(all, "the 60 floor assumes tier gear") {
		t.Fatalf("a level-60 warning names no tier gear:\n%s", all)
	}

	p.LevelBand = []int{55, 60}
	pf = &Preflight{OK: true}
	d.checkPartyStrength(pf)
	if all := strings.Join(pf.Warnings, "\n"); strings.Contains(all, "tier gear") {
		t.Fatalf("a non-60 band named tier gear:\n%s", all)
	}
}
