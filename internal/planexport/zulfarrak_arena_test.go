package planexport

// The Farraki Arena leg of Zul'Farrak (#243) as rows: the walk to the arena, the gossip that starts the event (option 0 -> param 1),
// the boss's hold done by his units with reset_to_gossip (entry 0, param 0, flags 16384, the boss first and the scorpids behind him),
// and the Zel'jeb leg's boss row. Exported with the options the TortoiseBots file uses: drop-companions-loot, carrier-chests,
// with-optional gahzrilla.

import (
	"fmt"
	"strings"
	"testing"

	"github.com/pjerra/turtle-agent/internal/dungeon"
)

func TestZulfarrakArenaRows(t *testing.T) {
	p, err := dungeon.Load("../../dungeons/zulfarrak.json")
	if err != nil {
		t.Fatal(err)
	}
	res, err := Build(p, Options{
		Source:             "test",
		DropCompanionsLoot: true,
		CarrierChests:      true,
		WithOptional:       []string{"gahzrilla"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if res.Cut != "" {
		t.Fatalf("cut: %s", res.Cut)
	}
	if len(res.Rows.Legs) != 20 {
		t.Fatalf("leg rows: %d, want 20", len(res.Rows.Legs))
	}

	var arena []string
	for _, s := range zulfarrakLegRows(t, res.Rows, "farraki_arena") {
		arena = append(arena, fmt.Sprintf("%s %d %g %g %g r%g p%d f%d t%d %v", s.Kind, s.Entry, s.X, s.Y, s.Z, s.Radius, s.Param, s.Flags, s.TimeoutS, s.Entries))
	}
	joined := strings.Join(arena, "\n")
	want := []string{
		"gossip 62498 1483.08 1011.05 12.15",
		"hold 0 1510 1015 12.1 r35 p0 f16384 t600 [62498 62496 62497 62499]",
	}
	for _, w := range want {
		if !strings.Contains(joined, w) {
			t.Errorf("the farraki_arena rows have no %q:\n%s", w, joined)
		}
	}
	// The gossip row must come straight before the units hold row.
	gi, hi := -1, -1
	for i, s := range arena {
		if strings.HasPrefix(s, "gossip ") {
			gi = i
		}
		if strings.HasPrefix(s, "hold ") {
			hi = i
		}
	}
	if gi < 0 || hi < 0 || hi != gi+1 {
		t.Errorf("the gossip row (%d) and the hold row (%d) are not adjacent:\n%s", gi, hi, joined)
	}

	zeljeb := zulfarrakLegRows(t, res.Rows, "zeljeb")
	last := zeljeb[len(zeljeb)-1]
	if last.Kind != "boss" || last.Entry != 62495 {
		t.Errorf("the zeljeb leg does not end with the boss row 62495: %s %d", last.Kind, last.Entry)
	}
}
