package planexport

import (
	"crypto/sha256"
	"fmt"
	"path/filepath"
	"testing"

	"github.com/pjerra/turtle-agent/internal/dungeon"
)

// testPlan is one leg with two vias, a pull group with a stand and a boss: every emitter of Origins runs.
func testPlan() *dungeon.Plan {
	return &dungeon.Plan{
		Version:  1,
		Name:     "origins_test",
		Kind:     "raid",
		Map:      1,
		Entrance: dungeon.Point{X: 0, Y: 0, Z: 0},
		Legs: []dungeon.Leg{{
			Name: "one",
			Via:  []dungeon.Waypoint{{Point: dungeon.Point{X: 5, Y: 0, Z: 0}}, {Point: dungeon.Point{X: 8, Y: 0, Z: 0}}},
			To:   dungeon.Point{X: 10, Y: 0, Z: 0},
			PullGroups: []dungeon.PullGroup{{
				Entries:   []uint32{100},
				Positions: []dungeon.Point{{X: 20, Y: 0, Z: 0}},
				Approach:  []dungeon.Point{{X: 15, Y: 0, Z: 0}, {X: 18, Y: 0, Z: 0}},
				StandPull: true,
			}},
			Boss: &dungeon.Boss{Entry: 200, Position: dungeon.Point{X: 30, Y: 0, Z: 0}},
		}},
	}
}

func TestOriginsCoverEveryRow(t *testing.T) {
	p := testPlan()
	res, err := Build(p, Options{})
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Origins) != len(res.Rows.Steps) {
		t.Fatalf("%d origins for %d rows", len(res.Origins), len(res.Rows.Steps))
	}
	want := []RowOrigin{
		{Leg: 0, Part: "via", Group: -1, Index: 0},
		{Leg: 0, Part: "via", Group: -1, Index: 1},
		{Leg: 0, Part: "to", Group: -1, Index: 0},
		{Leg: 0, Part: "approach", Group: 0, Index: 0},
		{Leg: 0, Part: "approach", Group: 0, Index: 1},
		{Leg: 0, Part: "pull", Group: 0, Index: 0},
		{Leg: 0, Part: "boss", Group: -1, Index: 0},
	}
	if len(res.Origins) != len(want) {
		t.Fatalf("%d origins, want %d: %+v", len(res.Origins), len(want), res.Origins)
	}
	for i := range want {
		if res.Origins[i] != want[i] {
			t.Errorf("row %d origin %+v, want %+v", i+1, res.Origins[i], want[i])
		}
	}
	// A non-cut row's point is the plan element it names.
	pt := func(o RowOrigin) dungeon.Point {
		l := p.Legs[o.Leg]
		switch o.Part {
		case "via":
			return l.Via[o.Index].Point
		case "to":
			return l.To
		case "approach":
			return l.PullGroups[o.Group].Approach[o.Index]
		case "pull":
			// The row point is the group's centre: the single position.
			return l.PullGroups[o.Group].Positions[0]
		case "boss":
			return l.Boss.Position
		}
		t.Fatalf("unexpected part %q", o.Part)
		return dungeon.Point{}
	}
	for i, o := range res.Origins {
		s := res.Rows.Steps[i]
		w := pt(o)
		if s.X != w.X || s.Y != w.Y || s.Z != w.Z {
			t.Errorf("row %d (%s) at %.1f %.1f %.1f, origin point %.1f %.1f %.1f", i+1, o.Part, s.X, s.Y, s.Z, w.X, w.Y, w.Z)
		}
	}
}

// TestCutRowsCarryTheViaIndexThecutPrecedes: a cut inserts move rows before the row whose hop it cuts; their origin names the
// plan element the cut row precedes (for `to`: the via count).
func TestCutRowsCarryTheViaIndexTheCutPrecedes(t *testing.T) {
	p := testPlan()
	p.Legs[0].To = dungeon.Point{X: 100, Y: 0, Z: 0}
	cut := func(mapID uint32, from, to dungeon.Point) ([]dungeon.Point, []CutNote, error) {
		return []dungeon.Point{{X: 30, Y: 0, Z: 0}, {X: 60, Y: 0, Z: 0}}, nil, nil
	}
	res, err := Build(p, Options{Cut: cut})
	if err != nil {
		t.Fatal(err)
	}
	var beforeTo []RowOrigin
	for i, o := range res.Origins {
		if o.Part != "cut" {
			continue
		}
		if res.Rows.Steps[i].Comment != "path" {
			t.Errorf("row %d: comment %q", i+1, res.Rows.Steps[i].Comment)
		}
		if o.Leg != 0 || o.Group != -1 {
			t.Errorf("cut origin %+v, want leg 0 and group -1", o)
		}
		if o.Index == 2 {
			beforeTo = append(beforeTo, o)
		}
	}
	if len(beforeTo) != 2 {
		t.Fatalf("%d cut rows before `to`, want 2: %+v", len(beforeTo), res.Origins)
	}
	// The cut rows with index 2 are the ones right before the `to` row (the via count).
	for i, o := range res.Origins {
		if o.Part == "cut" && o.Index == 2 && res.Rows.Steps[i].Comment != "path" {
			t.Errorf("row %d: cut origin before `to`", i+1)
		}
	}
}

// TestSQLUnchangedByOrigins pins the SQL the exporter writes for the plans the origins work touched: row origins are a
// side table and never change a row.
func TestSQLUnchangedByOrigins(t *testing.T) {
	for _, c := range []struct {
		plan string
		sha  string
	}{
		{"zul_gurub", "fc7ca0b1be102fb180623df43c6524925197bd9042a5d7eb3e5fa11e81eb6b53"},             // arlokk gong (2026-11-14, walk 100b T13): the gong step's stand moves -11502.9 -1609.1 49.625404 -> -11566.5 -1626.5 41.33 (the goober's own floor, 3.03 yd from it; the old stand stood 69.4 yd away and 8.3 yd above it) and the Arlokk boss approach drops its two ramp vias -11492.3 -1634.9 49.525 and -11491.2 -1621.3 41.325 (the party is on the arena floor after the gong), so 691 -> 689 steps and 701 -> 699 rows. Earlier: walk 71 fast (2026-11-10, t1106k): leg 8's entry drops the second via (-11776.531 -1502.4786 29.246) and `to` moves -11754.643 -1494.6427 37.765 -> -11786.999 -1564.2667 20.563 (the placement row the placed walk already starts on; export 694 -> 691 rows). Earlier: t1105e (2026-11-06): the Hakkar boss approach moved onto his platform (plan hAKkar-anchor-t1105e Part B): the ramp lip -11807.651 -1691.3049 52.111286 and the stand -11790 -1662 53.11 replace -11802.733 -1679.4271 52.943153, and leg 8 loses the path row the lip replaces (81 -> 80 rows, 695 -> 694 steps). Its plan row's cc_priority is cut 19 -> 16 entries (the module's kPlanMaxEntries, DungeonPlanPolicy.h:197: a longer list drops all four lists, so ZG ran without kill_first and CC since t1105c); the dropped tail is 11370, 11365, 11361. Earlier: t1105c (2026-11-06): merged origin/dev (WALK 39 trash CC lists: 19-entry cc_priority, kill_first without 11353) and Jin'do kill_first ward-first (14987 before 15112); steps unchanged. Earlier: walk 41 (2026-11-05): Jeklik's last boss approach moved from -12241.56 -1399.14 to the walked floor point -12269.3 -1389.4 131.5 (one move row, no row count change). Combined 2026-11-05: walk 38's Jin'do hall staging (49363 stand, 1173082 plain centroid, the guard and north camp stands, the lone mistresses with west approaches), its review round 2 (the fold is undone: the guard, the north camp and the two lone mistresses are their own pull groups again, the corridor's 49714 row keeps max_bodies 9) and Jeklik's 49131/49135 patrol split; the Arlokk leg's loop order, the ledge's stands, the one-ascent temple walk and the arena descent in the gong's rows; walk 37's dropped -11603 -1229 pull and the boss stand at -11536.8 -1261.5 stay
		{"onyxias_lair", "b87f12c40e8681f5152732614900570cf7e000f573574d69f58616885e18f3cf"},          // walk 2d pack 0 radius 1 -> 28
		{"crescent_grove", "f860a83f8f5a11598ca9582122200bae861348390a26802f08f0fcbd79aa40fa"},        // b6a (2026-11-14, route audit B6a): four dry stands on the leg-1 pond pulls (48, 72, 73, 86), the leg-2 group-49 approach[1] snapped to its floor, and the leg-0/leg-4 boss approaches that split the 199.9 yd hop and end on the 343 floor
		{"hateforge_quarry", "62e0b89af9d28957f942a13365ad6d88c809927e829583c2faee9306c2531da0"},      // b6a (2026-11-14): the two off-floor corners snapped (-8257.4 -3500.0 174.7, -8226.0 -3473.9 140.9, -8188.0 -3482.4 121.5), the leg-4 group-19 stand moved out of Figgles' aggro and the two long hops split
		{"black_morass", "931de663057606a4a0ccf3206afd615e986e03cf51f6241653be5fa611332137"},          // b6a (2026-11-14): the two vias out of Chronar's and Epochronos' aggro (plus the west via that keeps the walk clear), four dry stands on the leg-0 pond packs, and the leg-2 walk after Medivh split on the bridge floor
		{"blackrock_depths", "3889c83811393a749e658e686af192da60ff503f17579f37c312bc6240203038"},      // b6a (2026-11-14): the leg-0 group-29 stand out of Gerstahn's 12 yd aggro, the leg-1 and leg-8 stands snapped to their floors, and the leg-8 group-57 204.6 yd hop split
		{"lower_blackrock_spire", "1f8fe3a3b4455d9f45850daa3412ac83ba83d21bacfdcb47313ae4f873748011"}, // route order (2026-11-14, issue #1542) + dr41 rounds 2-3: 126 steps (7 move, 112 pull, 7 boss): drops off-walk leg 0 pg 20, moves 6-spider pack to leg 3, keeps Vosh'gajin front pair [9240, 9239] and folds the two back platform pairs into leg 1.boss (radius 24, first [9240, 9239, 9265, 9269]).
		{"razorfen_kraul", "67e950e27c80a3eb1b22fcd627b73823461dfd8826376e6bca82fcb64eacc790"},        // route order fix (2026-11-14, Sagiroth/TortoiseBots#1542, the Gnomeregan method of 2026-11-12): every pack moves to the leg whose walk passes it (122 -> 116 pulls, 6 dropped), each leg's `to` becomes its first pack, leg 7 keeps one via (2043.2 1479.3 68.9, dev via 17) and leg 8 the boss's climb vias + its dev `to` 2093.1 1767.5 79.8, and leg 4's skip loses 4842 (its new Halmgar pack names him). 682 -> 336 rows.
		{"maraudon_purple", "d09fce9d6fdc55dfb3164a331f9d41989944d6d955ae1b7e8691fd544e54b883"},       // route order 2026-11-14 (issue #1542): 106 packs instead of 141, 287 rows instead of 1,273, 9,371 yd instead of 36,333 (round 3 adds back the five ledge packs the deck above wakes)
	} {
		p, err := dungeon.Load(filepath.Join("..", "..", "dungeons", c.plan+".json"))
		if err != nil {
			t.Fatal(err)
		}
		res, err := Build(p, Options{})
		if err != nil {
			t.Fatal(err)
		}
		got := fmt.Sprintf("%x", sha256.Sum256([]byte(WriteSQL([]Rows{res.Rows}))))
		if got != c.sha {
			t.Errorf("%s: SQL sha256 %s, want %s", c.plan, got, c.sha)
		}
	}
}

// TestStepOriginsNameTheirList: a step row's origin names the list the step is in (steps or after_boss) and its index there.
func TestStepOriginsNameTheirList(t *testing.T) {
	p := testPlan()
	p.Legs[0].Steps = []dungeon.Step{{AreaTrigger: &dungeon.AreaTrigger{ID: 1, Stand: dungeon.Point{X: 12}}}}
	p.Legs[0].AfterBoss = []dungeon.Step{{AreaTrigger: &dungeon.AreaTrigger{ID: 2, Stand: dungeon.Point{X: 40}}}}
	res, err := Build(p, Options{})
	if err != nil {
		t.Fatal(err)
	}
	var got []RowOrigin
	for _, o := range res.Origins {
		if o.Part == "step" {
			got = append(got, o)
		}
	}
	want := []RowOrigin{{Leg: 0, Part: "step", Group: -1, Index: 0, List: "steps"},
		{Leg: 0, Part: "step", Group: -1, Index: 0, List: "after_boss"}}
	if len(got) != len(want) {
		t.Fatalf("step origins %+v, want %+v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("step origin %d %+v, want %+v", i, got[i], want[i])
		}
	}
}
