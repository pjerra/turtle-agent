package dungeon

import (
	"fmt"
	"testing"
)

// The two Turtle Wardens of Crescent Grove (#243) that the plan only pulled as trash: Warden Treeshade (92129) and Warden
// Liferoot (92128) each get a leg of their own (the boss promotion recipe of the spec, §2.1). The route and the pulls do not
// change, and Engryss keeps the two Elders as his boss.first.
func TestCrescentGroveTurtleBossesHaveSteps(t *testing.T) {
	p := loadPlan(t, "../../dungeons/crescent_grove.json")

	bosses := map[uint32]string{}
	var order []string
	for _, l := range p.Legs {
		order = append(order, l.Name)
		if l.Boss != nil {
			bosses[l.Boss.Entry] = l.Name
		}
	}

	for _, want := range []struct {
		entry uint32
		name  string
		pos   Point
	}{
		{92129, "warden_treeshade", Point{-50.6621, 27.2743, 297.397}},
		{92128, "warden_liferoot", Point{-6.16984, -95.9617, 293.077}},
	} {
		if got, ok := bosses[want.entry]; !ok {
			t.Errorf("no leg has boss %d", want.entry)
		} else if got != want.name {
			t.Errorf("boss %d is the boss of leg %q, want %q", want.entry, got, want.name)
		}
		l := zfLeg(t, p, want.name)
		if l.Boss != nil {
			if d := dist3(l.Boss.Position, want.pos); d > 0.5 {
				t.Errorf("%s boss position %v is %.2f yd from %v", want.name, l.Boss.Position, d, want.pos)
			}
		}
	}

	engryss := zfLeg(t, p, "grovetender_engryss")
	if engryss.Boss == nil || engryss.Boss.Entry != 92107 || fmt.Sprint(engryss.Boss.First) != "[92105 92106]" {
		t.Errorf("Engryss: %+v", engryss.Boss)
	}
	if bosses[92109] != "keeper_ranathos" {
		t.Errorf("Ranathos is on leg %q", bosses[92109])
	}

	if len(p.Legs) != 7 {
		t.Errorf("%d legs, want 7", len(p.Legs))
	}
	wantOrder := "[grovetender_engryss warden_treeshade warden_liferoot keeper_ranathos high_priestess_a'lathea fenektis_the_deceiver master_raxxieth]"
	if fmt.Sprint(order) != wantOrder {
		t.Errorf("leg order %v, want %s", order, wantOrder)
	}
}
