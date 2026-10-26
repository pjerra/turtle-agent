package planexport

import (
	"strings"
	"testing"

	"github.com/pjerra/turtle-agent/internal/dungeon"
)

// Spec section E + F.26: by default the export writes optional legs into the
// rows (as --with-optional writes them today) plus one ai_dungeon_leg_need row
// with mode 1 (optional) for the leg's item.
func TestOptionalLegWrittenWithNeedRow(t *testing.T) {
	p, err := dungeon.Load("../../dungeons/zulfarrak.json")
	if err != nil {
		t.Fatal(err)
	}
	res, err := Build(p, Options{Source: "test", DropCompanionsLoot: true})
	if err != nil {
		t.Fatal(err)
	}
	if res.Cut != "" {
		t.Fatalf("cut: %s", res.Cut)
	}
	legs := res.Rows.Legs
	if len(legs) != 18 || legs[9].Name != "gahzrilla" || legs[10].Name != "courtyard" {
		t.Fatalf("default export leaves the optional leg out: %+v", legs)
	}
	if len(res.Rows.Needs) != 1 {
		t.Fatalf("%d need rows, want 1: %+v", len(res.Rows.Needs), res.Rows.Needs)
	}
	n := res.Rows.Needs[0]
	if n.Plan != "zulfarrak" || n.Leg != 9 || n.Item != 9240 || n.Mode != 1 || n.Pick != 0 || n.Name != "Mallet of Zul'Farrak" {
		t.Fatalf("need row %+v", n)
	}
	sql := WriteSQL([]Rows{res.Rows})
	if !strings.Contains(sql, "DELETE FROM `ai_dungeon_leg_need` WHERE `plan` = 'zulfarrak';") {
		t.Fatal("the migration has no ai_dungeon_leg_need DELETE")
	}
	if !strings.Contains(sql, "INSERT INTO `ai_dungeon_leg_need` (`plan`, `leg`, `item`, `mode`, `pick`, `object`, `name`, `how`) VALUES\n('zulfarrak', 9, 9240, 1, 0, 0, 'Mallet of Zul''Farrak', '');") {
		t.Fatalf("the migration has no mode-1 need row for the gahzrilla leg:\n%s", sql)
	}
	back, err := Parse(sql)
	if err != nil {
		t.Fatal(err)
	}
	if err := Equal([]Rows{res.Rows}, back); err != nil {
		t.Fatal(err)
	}
}

// Spec section E + F.27: --without-optional restores the old leave-out (with
// the warning text kept).
func TestWithoutOptionalLeavesItOut(t *testing.T) {
	p, err := dungeon.Load("../../dungeons/zulfarrak.json")
	if err != nil {
		t.Fatal(err)
	}
	res, err := Build(p, Options{Source: "test", DropCompanionsLoot: true, WithoutOptional: true})
	if err != nil {
		t.Fatal(err)
	}
	for _, l := range res.Rows.Legs {
		if l.Name == "gahzrilla" {
			t.Fatalf("gahzrilla is in the rows with WithoutOptional: %+v", res.Rows.Legs)
		}
	}
	if len(res.Rows.Legs) != 17 || res.Rows.Legs[9].Name != "courtyard" {
		t.Fatalf("legs %+v", res.Rows.Legs)
	}
	if len(res.Rows.Needs) != 0 {
		t.Fatalf("%d need rows, want none: %+v", len(res.Rows.Needs), res.Rows.Needs)
	}
	found := false
	for _, w := range res.Warnings {
		if strings.Contains(w, "leg 9 (gahzrilla): optional (needs Mallet of Zul'Farrak (item 9240), which the run leader's bots do not carry): left out of the export") {
			found = true
		}
	}
	if !found {
		t.Fatalf("the leave-out warning is gone: %q", res.Warnings)
	}
}
