package dungeon

import "testing"

// The two Turtle bosses of Zul'Farrak (#243): Zel'jeb the Ancient (62495) gets a leg of his own north of Velratha's pool, and
// Champion Razjal the Quick (62498) and the Farraki Arena get a leg between Velratha's and her own fight: the gossip that starts
// the event (option 0, by the tank: the script puts the adds' threat on the starter), then the boss's hold done by his units
// (instance_data 0) over the arena, whose scorpids (62499) are in kill_first. The leg order is the spec's (velratha, farraki_arena,
// velratha_b, zeljeb, gahzrilla, courtyard) and the courtyard leg walks back through Velratha's spot (the way in, not the pit ring).
func TestZulfarrakTurtleBossesHaveSteps(t *testing.T) {
	p := loadPlan(t, zulfarrakPlan)

	zeljeb := zfLeg(t, p, "zeljeb")
	if zeljeb.Boss == nil || zeljeb.Boss.Entry != 62495 {
		t.Fatalf("zeljeb boss: %+v", zeljeb.Boss)
	}
	want := Point{1764.37, 1437.86, -0.1}
	if d := dist3(zeljeb.Boss.Position, want); d > 0.5 {
		t.Errorf("zeljeb boss position %v is %.2f yd from %v", zeljeb.Boss.Position, d, want)
	}
	if len(zeljeb.PullGroups) == 0 {
		t.Errorf("zeljeb has no pull groups")
	}

	arena := zfLeg(t, p, "farraki_arena")
	var gossip *Gossip
	for _, st := range arena.Steps {
		if st.Gossip != nil {
			gossip = st.Gossip
		}
	}
	if gossip == nil || gossip.NPC != 62498 || gossip.Option == nil || *gossip.Option != 0 || gossip.By != GossipByTank {
		t.Errorf("farraki_arena gossip: %+v", gossip)
	}
	if arena.Boss == nil || arena.Boss.Entry != 62498 {
		t.Fatalf("farraki_arena boss: %+v", arena.Boss)
	}
	if len(arena.Boss.First) != 2 || arena.Boss.First[0] != 62496 || arena.Boss.First[1] != 62497 {
		t.Errorf("farraki_arena boss.first: %v", arena.Boss.First)
	}
	h := arena.Boss.Hold
	if h == nil || h.Data != 0 || h.Radius != 35 || h.TimeoutS != 600 {
		t.Fatalf("farraki_arena boss hold: %+v", h)
	}
	found := false
	for _, e := range h.Entries {
		if e == 62499 {
			found = true
		}
	}
	if !found {
		t.Errorf("farraki_arena boss hold entries %v have no 62499", h.Entries)
	}

	order := []string{"velratha", "farraki_arena", "velratha_b", "zeljeb", "gahzrilla", "courtyard"}
	at := map[string]int{}
	for i, l := range p.Legs {
		at[l.Name] = i
	}
	for i := 1; i < len(order); i++ {
		if at[order[i-1]] >= at[order[i]] {
			t.Errorf("the leg order is %v: %s is not before %s", order, order[i-1], order[i])
		}
	}

	kill := false
	for _, e := range p.KillFirst {
		if e == 62499 {
			kill = true
		}
	}
	if !kill {
		t.Errorf("kill_first %v has no 62499 (the arena's scorpids)", p.KillFirst)
	}

	courtyard := zfLeg(t, p, "courtyard")
	if len(courtyard.Via) == 0 || courtyard.Via[0].Point != (Point{1650.4, 1161.4, 9.9}) {
		t.Errorf("courtyard via: %+v", courtyard.Via)
	}
}
