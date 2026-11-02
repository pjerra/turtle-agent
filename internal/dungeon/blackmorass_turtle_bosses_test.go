package dungeon

import (
	"fmt"
	"testing"
)

// The four Turtle bosses of Black Morass (#243) that the plan only pulled as trash: Drifting Avatar of Sand (61316), Epidamu
// (61575), Rotmaw (65122) and Mossheart (65124) each get a leg of their own (the boss promotion recipe of the spec, §2.1).
// The route and the pulls do not change; the four bosses the plan already had keep their legs.
func TestBlackMorassTurtleBossesHaveSteps(t *testing.T) {
	p := loadPlan(t, "../../dungeons/black_morass.json")

	bosses := map[uint32]string{}
	var order []string
	for _, l := range p.Legs {
		order = append(order, l.Name)
		if l.Boss != nil {
			bosses[l.Boss.Entry] = l.Name
		}
	}

	leg := func(name string) Leg {
		t.Helper()
		for _, l := range p.Legs {
			if l.Name == name {
				return l
			}
		}
		t.Fatalf("the plan has no leg %q", name)
		return Leg{}
	}

	for _, want := range []struct {
		entry uint32
		name  string
		pos   Point
	}{
		{61316, "drifting_avatar_of_sand", Point{-1339.93, 6921.69, -32.675}},
		{61575, "epidamu", Point{-1405.35, 7004.3, -85.968}},
		{65122, "rotmaw", Point{-2095.36, 7410.7, 24.6254}},
		{65124, "mossheart", Point{-1880.32, 6904.04, 26.2261}},
	} {
		if got, ok := bosses[want.entry]; !ok {
			t.Errorf("no leg has boss %d", want.entry)
		} else if got != want.name {
			t.Errorf("boss %d is the boss of leg %q, want %q", want.entry, got, want.name)
		}
		l := leg(want.name)
		if l.Boss != nil {
			if d := dist3(l.Boss.Position, want.pos); d > 0.5 {
				t.Errorf("%s boss position %v is %.2f yd from %v", want.name, l.Boss.Position, d, want.pos)
			}
		}
	}

	// The four old bosses keep their legs.
	for entry, name := range map[uint32]string{65125: "antnormi", 50105: "medivh", 65116: "time-lord_epochronos", 65113: "chronar"} {
		if bosses[entry] != name {
			t.Errorf("boss %d is on leg %q, want %q", entry, bosses[entry], name)
		}
	}

	if len(p.Legs) != 8 {
		t.Errorf("%d legs, want 8", len(p.Legs))
	}
	wantOrder := "[mossheart antnormi rotmaw medivh epidamu drifting_avatar_of_sand time-lord_epochronos chronar]"
	if fmt.Sprint(order) != wantOrder {
		t.Errorf("leg order %v, want %s", order, wantOrder)
	}
}
