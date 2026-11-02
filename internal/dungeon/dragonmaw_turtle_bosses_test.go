package dungeon

import (
	"fmt"
	"testing"
)

// The six named Turtle bosses of Dragonmaw Retreat (#243) that the plan only pulled as trash get a leg of their own (the boss
// promotion recipe of the spec, §2.1): Overlord Blackheart (62070), Elder Hollowblood (62038), Slagfist Destroyer (62068),
// Garlok Flamekeeper (62071), Web Master Torkon (62067) and Cavernweb Broodmother (62066). The route and the pulls do not
// change; Searistrasz keeps his boss and the groups after his promotion.
func TestDragonmawTurtleBossesHaveSteps(t *testing.T) {
	p := loadPlan(t, "../../dungeons/dragonmaw_retreat.json")

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
		first string
	}{
		{62070, "overlord_blackheart", Point{-6305.66, -4316.22, 266.168}, "[62051]"},
		{62038, "elder_hollowblood", Point{-6170.29, -4353.98, 266.168}, "[]"},
		{62068, "slagfist_destroyer", Point{-6539.19, -4206.34, 231.847}, "[]"},
		{62071, "garlok_flamekeeper", Point{-6293.57, -4095.59, 264.827}, "[]"},
		{62067, "web_master_torkon", Point{-6292.16, -4069.22, 72.5998}, "[62074]"},
		{62066, "cavernweb_broodmother", Point{-6074.1, -3977.02, 72.5993}, "[]"},
	} {
		if got, ok := bosses[want.entry]; !ok {
			t.Errorf("no leg has boss %d", want.entry)
		} else if got != want.name {
			t.Errorf("boss %d is the boss of leg %q, want %q", want.entry, got, want.name)
		}
		l := zfLeg(t, p, want.name)
		if l.Boss == nil {
			continue
		}
		if d := dist3(l.Boss.Position, want.pos); d > 0.5 {
			t.Errorf("%s boss position %v is %.2f yd from %v", want.name, l.Boss.Position, d, want.pos)
		}
		if fmt.Sprint(l.Boss.First) != want.first {
			t.Errorf("%s boss.first %v, want %s", want.name, l.Boss.First, want.first)
		}
	}

	// Searistrasz keeps his boss and the groups after the split; the promoted group is gone from his leg.
	s := zfLeg(t, p, "searistrasz")
	if s.Boss == nil || s.Boss.Entry != 62072 {
		t.Fatalf("searistrasz boss: %+v", s.Boss)
	}
	for _, g := range s.PullGroups {
		for _, e := range g.Entries {
			if e == 62038 {
				t.Errorf("the searistrasz leg still pulls Hollowblood")
			}
		}
	}
	if bosses[62037] != "zuluhed_the_whacked" || bosses[62069] != "halgan_redbrand" || bosses[62057] != "gowlfang" || bosses[62056] != "bogpaw_truthsay" {
		t.Errorf("an old boss lost its leg: %v", bosses)
	}

	if len(p.Legs) != 17 {
		t.Errorf("%d legs, want 17", len(p.Legs))
	}
	wantOrder := "[bogpaw_truthsay gowlfang overlord_blackheart hall_clear elder_hollowblood searistrasz searistrasz_line halgan_approach slagfist_destroyer halgan_redbrand garlok_flamekeeper zuluhed_approach zuluhed_mid cavernweb_broodmother web_master_torkon zuluhed_floor zuluhed_the_whacked]"
	if fmt.Sprint(order) != wantOrder {
		t.Errorf("leg order %v, want %s", order, wantOrder)
	}
}
