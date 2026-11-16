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

// TestSQLUnchangedByOrigins pins the SQL the exporter writes for the two plans the origins work touched: row origins are a
// side table and never change a row.
func TestSQLUnchangedByOrigins(t *testing.T) {
	for _, c := range []struct {
		plan string
		sha  string
	}{
		{"zul_gurub", "c5a7a2f7114f45b9125c1119c7763f62069cfbf3e01ecc993251e004345afbdb"},    // walk 104's six ZG route fixes in one plan (2026-11-15, T25/T28/T29/T31/T34/T35): 689 -> 708 steps and 699 -> 718 rows (floor-aware, approach-hops, the server's mmaps). T28: leg 7 drops the two 15043 packs on the lake islet (no dry floor reaches them; the run leader's one water hop stalled the lead bot at step 553) and the lower-ring pack walks a dry west-rim approach. T31: leg 1's merged Mad Servant camp stand moves -11919.2 -1843.6 58.8 -> -11899.3 -1849.1 61.3, 23.3 yd from its nearest member (inside the run leader's 28.5 yd shot; the old stand was 43.0 yd out and the server read no line of sight from it). T25: the Arlokk ledge's packs pull from stands outside their own wake (the z 60 landing, the ledge west, the ring). T29: the Jin'do hall's west packs are stand pulls from 21-28 yd out, group 6's stand 9.5 yd west (-11619.5 -1306, 41.2 yd from the 1173082 four). T34: the Mandokir leg pulls the Vilebranch Speaker 11391 last so the boss wakes alone, and the boss row gains the arena-floor stand -12188 -1956 130.6. T35: step 36 merges the 49155/49189 bat pull and re-adds formation 49189's own stand pull as a fallback (stand -12108 -1479, its 6-point loop). Earlier: walk 107/108 T35 round 3 (2026-11-15): formation 49189 joins the F6 bat row (the 31.5 yd stand sat inside its live patrol wake, 33 yd reach) and the merged row walks to one stand, -12114.5 -1446.8 131.32, with radius 6.5 and max_bodies 11: leg 0 50 -> 47 steps, 148 -> 147 pulls (690 -> 687 steps, 700 -> 697 rows with the live flags). Earlier: walk 107/108 T35 round 2 (2026-11-15): leg 0 step 36's stand moves -12130 -1495.7 133 -> -12114 -1491 131.63, outside the next pull group 49189's wake reach (round 1's stand sat 24.8 and 27.3 yd from its bats 49151/49153 and 30.9 yd from its loop) while inside the tank's shot of F6 49155's loop (25.3 yd from its east waypoint, 19.9 yd of the loop in shot); same rows: 690 steps and 700 rows. Earlier: walk 107/108 T35 round 1 (2026-11-15): the stand moved -12093 -1464 131.5 -> -12130 -1495.7 133, an extra approach row (689 -> 690 steps, 699 -> 700 rows), and pull group 10 gained F6's 49155 loop (8 points, 74 s) so the gate waits for the patrol. Earlier: arlokk gong (2026-11-14, walk 100b T13): the gong step's stand moves -11502.9 -1609.1 49.625404 -> -11566.5 -1626.5 41.33 (the goober's own floor, 3.03 yd from it; the old stand stood 69.4 yd away and 8.3 yd above it) and the Arlokk boss approach drops its two ramp vias -11492.3 -1634.9 49.525 and -11491.2 -1621.3 41.325 (the party is on the arena floor after the gong), so 691 -> 689 steps and 701 -> 699 rows. Earlier: walk 71 fast (2026-11-10, t1106k): leg 8's entry drops the second via (-11776.531 -1502.4786 29.246) and `to` moves -11754.643 -1494.6427 37.765 -> -11786.999 -1564.2667 20.563 (the placement row the placed walk already starts on; export 694 -> 691 rows). Earlier: t1105e (2026-11-06): the Hakkar boss approach moved onto his platform (plan hAKkar-anchor-t1105e Part B): the ramp lip -11807.651 -1691.3049 52.111286 and the stand -11790 -1662 53.11 replace -11802.733 -1679.4271 52.943153, and leg 8 loses the path row the lip replaces (81 -> 80 rows, 695 -> 694 steps). Its plan row's cc_priority is cut 19 -> 16 entries (the module's kPlanMaxEntries, DungeonPlanPolicy.h:197: a longer list drops all four lists, so ZG ran without kill_first and CC since t1105c); the dropped tail is 11370, 11365, 11361. Earlier: t1105c (2026-11-06): merged origin/dev (WALK 39 trash CC lists: 19-entry cc_priority, kill_first without 11353) and Jin'do kill_first ward-first (14987 before 15112); steps unchanged. Earlier: walk 41 (2026-11-05): Jeklik's last boss approach moved from -12241.56 -1399.14 to the walked floor point -12269.3 -1389.4 131.5 (one move row, no row count change). Combined 2026-11-05: walk 38's Jin'do hall staging (49363 stand, 1173082 plain centroid, the guard and north camp stands, the lone mistresses with west approaches), its review round 2 (the fold is undone: the guard, the north camp and the two lone mistresses are their own pull groups again, the corridor's 49714 row keeps max_bodies 9) and Jeklik's 49131/49135 patrol split; the Arlokk leg's loop order, the ledge's stands, the one-ascent temple walk and the arena descent in the gong's rows; walk 37's dropped -11603 -1229 pull and the boss stand at -11536.8 -1261.5 stay Then walk 107 T34 (2026-11-15): the bloodlord_mandokir leg pulls the Vilebranch Speaker 11391 LAST (group 17 -> 19), after the 49270 camp and the 51453 raptors, because its death is Mandokir's wake; its stand moves -12216 -1930 132.62 -> -12188 -1972 132.1, the raptors' stand -12180 -1966 130.84 -> -12177 -1978 133.5 (the walk from the camp stays 32.7 yd from the live speaker) and the boss row gains the stand -12188 -1956 130.6 (the arena floor, 42.0 yd from his platform); the exporter's tail cut loses one row there, so leg 5 is 78 rows and the plan 688 steps (698 rows) against 79 and 689 (699). Earlier: arlokk gong (2026-11-14, walk 100b T13): the gong step's stand moves -11502.9 -1609.1 49.625404 -> -11566.5 -1626.5 41.33 (the goober's own floor, 3.03 yd from it; the old stand stood 69.4 yd away and 8.3 yd above it) and the Arlokk boss approach drops its two ramp vias -11492.3 -1634.9 49.525 and -11491.2 -1621.3 41.325 (the party is on the arena floor after the gong), so 691 -> 689 steps and 701 -> 699 rows. Earlier: walk 71 fast (2026-11-10, t1106k): leg 8's entry drops the second via (-11776.531 -1502.4786 29.246) and `to` moves -11754.643 -1494.6427 37.765 -> -11786.999 -1564.2667 20.563 (the placement row the placed walk already starts on; export 694 -> 691 rows). Earlier: t1105e (2026-11-06): the Hakkar boss approach moved onto his platform (plan hAKkar-anchor-t1105e Part B): the ramp lip -11807.651 -1691.3049 52.111286 and the stand -11790 -1662 53.11 replace -11802.733 -1679.4271 52.943153, and leg 8 loses the path row the lip replaces (81 -> 80 rows, 695 -> 694 steps). Its plan row's cc_priority is cut 19 -> 16 entries (the module's kPlanMaxEntries, DungeonPlanPolicy.h:197: a longer list drops all four lists, so ZG ran without kill_first and CC since t1105c); the dropped tail is 11370, 11365, 11361. Earlier: t1105c (2026-11-06): merged origin/dev (WALK 39 trash CC lists: 19-entry cc_priority, kill_first without 11353) and Jin'do kill_first ward-first (14987 before 15112); steps unchanged. Earlier: walk 41 (2026-11-05): Jeklik's last boss approach moved from -12241.56 -1399.14 to the walked floor point -12269.3 -1389.4 131.5 (one move row, no row count change). Combined 2026-11-05: walk 38's Jin'do hall staging (49363 stand, 1173082 plain centroid, the guard and north camp stands, the lone mistresses with west approaches), its review round 2 (the fold is undone: the guard, the north camp and the two lone mistresses are their own pull groups again, the corridor's 49714 row keeps max_bodies 9) and Jeklik's 49131/49135 patrol split; the Arlokk leg's loop order, the ledge's stands, the one-ascent temple walk and the arena descent in the gong's rows; walk 37's dropped -11603 -1229 pull and the boss stand at -11536.8 -1261.5 stay Then review round 3 (2026-11-15, T29): leg 6 pull group 5's approach -11610 -1306 79.2 -> -11619.5 -1306 79.17 (9.5 yd west: 41.2 yd from the later 1173082 four, its reach plus 8; 414 steps and 424 rows unchanged). Earlier: walk 104 (2026-11-15, T29): the Jin'do hall's west packs are stand pulls from walked floor points -- leg 6 pull group 5's approach -11614.4 -1282.1 79.4 -> -11610 -1306 79.2 and its row a stand pull, group 6's stand -11581 -1334.8 81.1 -> -11590 -1329.2 77.75, group 7's approach -11577.5 -1293.6 77.8 -> -11599 -1307 77.7 and its row a stand pull; 414 steps and 424 rows unchanged. Earlier: arlokk gong (2026-11-14, walk 100b T13): the gong step's stand moves -11502.9 -1609.1 49.625404 -> -11566.5 -1626.5 41.33 (the goober's own floor, 3.03 yd from it; the old stand stood 69.4 yd away and 8.3 yd above it) and the Arlokk boss approach drops its two ramp vias -11492.3 -1634.9 49.525 and -11491.2 -1621.3 41.325 (the party is on the arena floor after the gong), so 691 -> 689 steps and 701 -> 699 rows. Earlier: walk 71 fast (2026-11-10, t1106k): leg 8's entry drops the second via (-11776.531 -1502.4786 29.246) and `to` moves -11754.643 -1494.6427 37.765 -> -11786.999 -1564.2667 20.563 (the placement row the placed walk already starts on; export 694 -> 691 rows). Earlier: t1105e (2026-11-06): the Hakkar boss approach moved onto his platform (plan hAKkar-anchor-t1105e Part B): the ramp lip -11807.651 -1691.3049 52.111286 and the stand -11790 -1662 53.11 replace -11802.733 -1679.4271 52.943153, and leg 8 loses the path row the lip replaces (81 -> 80 rows, 695 -> 694 steps). Its plan row's cc_priority is cut 19 -> 16 entries (the module's kPlanMaxEntries, DungeonPlanPolicy.h:197: a longer list drops all four lists, so ZG ran without kill_first and CC since t1105c); the dropped tail is 11370, 11365, 11361. Earlier: t1105c (2026-11-06): merged origin/dev (WALK 39 trash CC lists: 19-entry cc_priority, kill_first without 11353) and Jin'do kill_first ward-first (14987 before 15112); steps unchanged. Earlier: walk 41 (2026-11-05): Jeklik's last boss approach moved from -12241.56 -1399.14 to the walked floor point -12269.3 -1389.4 131.5 (one move row, no row count change). Combined 2026-11-05: walk 38's Jin'do hall staging (49363 stand, 1173082 plain centroid, the guard and north camp stands, the lone mistresses with west approaches), its review round 2 (the fold is undone: the guard, the north camp and the two lone mistresses are their own pull groups again, the corridor's 49714 row keeps max_bodies 9) and Jeklik's 49131/49135 patrol split; the Arlokk leg's loop order, the ledge's stands, the one-ascent temple walk and the arena descent in the gong's rows; walk 37's dropped -11603 -1229 pull and the boss stand at -11536.8 -1261.5 stay
		{"onyxias_lair", "b87f12c40e8681f5152732614900570cf7e000f573574d69f58616885e18f3cf"}, // walk 2d pack 0 radius 1 -> 28
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
