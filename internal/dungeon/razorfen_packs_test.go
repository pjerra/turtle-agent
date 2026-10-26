package dungeon

import (
	"fmt"
	"math"
	"testing"
)

// The pack fixes of the Razorfen Kraul draft (2026-10-22; ~/TortoiseBots-audit/PREP-RFK-2026-10-22.md, "Fixes applied"), made from the
// world export of map 47 and the Deadmines r12/r14 + Stockade lessons, before any live run. Each test fails when its part is taken out.
const rfkPlan = "../../dungeons/razorfen_kraul.json"

func rfkLeg(t *testing.T, p *Plan, name string) Leg {
	t.Helper()
	for _, l := range p.Legs {
		if l.Name == name {
			return l
		}
	}
	t.Fatalf("no leg %s", name)
	return Leg{}
}

// Every humanoid of the map flees at low health (EventAI command 47); the Ward Guardian (Healing Wave) and the Acolyte
// (Renew, Mana Burn) are the healers and go into cc_priority with the two CC-casters (Enveloping Winds, Earthgrab Totem).
func TestRazorfenUnitListsComeFromTheCensus(t *testing.T) {
	p := loadPlan(t, rfkPlan)
	for _, c := range []struct {
		name      string
		got, want string
	}{
		{"fleers", fmt.Sprint(p.Fleers), "[4435 4436 4437 4438 4440 4515 4516 4517 4518 4519 4520 4522 4523 4525 4530 4531 4532 4623 4842 6035]"},
		{"cc_priority", fmt.Sprint(p.CCPriority), "[4427 4515 4522 4440]"},
		{"cc_always", fmt.Sprint(p.CCAlways), "[4427]"},
		{"kill_first", fmt.Sprint(p.KillFirst), "[]"},
	} {
		if c.got != c.want {
			t.Errorf("%s %s, want %s", c.name, c.got, c.want)
		}
	}
}

// The opening stand of leg 0 (r19-19z37 live run 1, stopped at step 4): the party stands at 1994 1615, a lower-floor pocket
// with no upper polygon in its formation ring, not at 1987.5 1600 where the slot lookup found the walkway above (z 92-95) and
// the two ranged bots climbed the west ramp while idle after the fight. The five corridor pull groups all have radius 10
// (r20-19z39 run 2: group 0's radius 25 owned both Quilguards of the next group and the first fight began at the corridor
// mouth), group 4 carries the corridor regroup move at 2000 1635, and the two pulls that outran the run leader's 8 approach
// hops (39 and 42) carry stands on the hall floor within 60 yd of their packs.
func TestRazorfenOpeningStandStaysOffTheLedge(t *testing.T) {
	p := loadPlan(t, rfkPlan)
	l := rfkLeg(t, p, "roogug")
	if got := fmt.Sprintf("%.1f %.1f %.1f", l.To.X, l.To.Y, l.To.Z); got != "1994.0 1615.0 83.2" {
		t.Errorf("roogug to = %s, want 1994.0 1615.0 83.2", got)
	}
	want := []struct {
		group  int
		radius float32
	}{
		{0, 10}, // Handler 4530
		{1, 10}, // Quilguard pair
		{2, 10}, // Geomancer 4520
		{3, 10}, // second Quilguard pair
		{5, 10}, // Handler 4530 + Quilguard pair: the default was 19.3
	}
	for _, c := range want {
		if got := l.PullGroups[c.group].Radius; got != c.radius {
			t.Errorf("roogug pull group %d radius %v, want %v", c.group, got, c.radius)
		}
	}
	g := l.PullGroups[4]
	if len(g.Approach) != 1 || g.Approach[0].X != 2000 || g.Approach[0].Y != 1635 || g.Approach[0].Z != 81 {
		t.Errorf("roogug pull group 4 approach %+v, want the corridor regroup point 2000 1635 81", g.Approach)
	}
	// Pull 39's pack (run 2 stopped on it at 61.0 yd): the stand 2110 1600 82.1 is 42.0 yd from it on the hall floor (the
	// corridor the tank pulled it from); pull 42's pack (4440) has the same stand (run 3 refused the room's stand 2180 1604:
	// no walkable path) and radius 10, 73 yd from the pack because the room east of x about 2135 is another pocket.
	for _, c := range []struct {
		name    string
		group   int
		x, y, z float32
		pack    Point
		maxYd   float64
	}{
		{"step 39's 4532", 20, 2110, 1600, 82.1, l.PullGroups[20].Positions[0], 60},
		{"step 42's 4440", 23, 2110, 1600, 82.1, l.PullGroups[23].Positions[0], 80},
	} {
		g := l.PullGroups[c.group]
		if len(g.Approach) != 1 {
			t.Errorf("roogug pull group %d (%s) approach %+v, want one stand point", c.group, c.name, g.Approach)
			continue
		}
		a := g.Approach[0]
		if a.X != c.x || a.Y != c.y || a.Z != c.z {
			t.Errorf("roogug pull group %d (%s) approach %+v, want %.0f %.0f %.1f", c.group, c.name, a, c.x, c.y, c.z)
		}
		dx, dy, dz := float64(a.X-c.pack.X), float64(a.Y-c.pack.Y), float64(a.Z-c.pack.Z)
		if d := math.Sqrt(dx*dx + dy*dy + dz*dz); d > c.maxYd {
			t.Errorf("roogug pull group %d (%s): the stand is %.1f yd from the pack, want at most %.0f", c.group, c.name, d, c.maxYd)
		}
	}
	if got := l.PullGroups[23].Radius; got != 10 {
		t.Errorf("roogug pull group 24 (4440) radius %v, want the explicit 10", got)
	}
}

// The packs that stand with a boss are the boss row's first, not pulls of their own: Roogug's two Defenders and two Adepts (8 yd
// from him, a pull of radius 23.6 took the boss in), Jargba's Acolyte and Groundshaker (6-9 yd), the two Death's Head Priests
// under Charlga's den (the boss is 2 yd from the priest pull, 20 yd up). The two Spearhides 5-6 yd from Ramtusk are his first.
func TestRazorfenBossPacksAreTheBossFight(t *testing.T) {
	p := loadPlan(t, rfkPlan)
	for _, c := range []struct {
		leg   string
		boss  uint32
		first string
		pulls int
	}{
		{"roogug", 6168, "[4516 4442]", 31},
		{"aggem_thorncurse", 4424, "[]", 20},
		{"death_speaker_jargba", 4428, "[4515 4523]", 8},
		{"overlord_ramtusk", 4420, "[4438]", 14},
		{"rotthorn", 62503, "[]", 12},
		{"agathelos_the_raging", 4422, "[]", 10},
		// r35 slot7b split the old charlga leg in three: the den packs, the pit (a leg of its own because only a leg's via chain
		// can row the 1200-yd walk into the pit), and the boss with the pit-rim, upper-ledge and corridor packs.
		{"charlga_den", 0, "", 6},
		{"charlga_pit", 0, "", 17},
		{"charlga_razorflank", 4421, "[4517]", 4},
	} {
		l := rfkLeg(t, p, c.leg)
		if c.boss == 0 {
			if l.Boss != nil {
				t.Errorf("leg %s: boss %+v, want none (the packs of a leg with no boss, the walk is its via chain)", c.leg, l.Boss)
			}
		} else if l.Boss == nil || l.Boss.Entry != c.boss {
			t.Errorf("leg %s: boss %+v, want %d", c.leg, l.Boss, c.boss)
			continue
		}
		if c.boss != 0 {
			if got := fmt.Sprint(l.Boss.First); got != c.first {
				t.Errorf("leg %s: boss first %s, want %s", c.leg, got, c.first)
			}
		}
		if len(l.PullGroups) != c.pulls {
			t.Errorf("leg %s: %d pull groups, want %d", c.leg, len(l.PullGroups), c.pulls)
		}
	}
}

// Earthcaller Halmgar 4842 (a rank-2 caster the leg 3 pull of radius 10 dragged in from 2 yd) is a member of that pull group and
// no longer a skipped unit of the leg; the trio east of him has a radius that stops short of him (his 13 yd) and of its own mate
// pack (16 yd). The Blind Hunter 4425 (rank 2, standing on the last bat pull's own position) is a member of that group.
func TestRazorfenRaresAreOwnedNotSkipped(t *testing.T) {
	p := loadPlan(t, rfkPlan)
	r := rfkLeg(t, p, "overlord_ramtusk")
	if len(r.Skip) != 0 {
		t.Errorf("overlord_ramtusk still skips %v: both units are owned now", r.Skip)
	}
	n := 0
	for i, g := range r.PullGroups {
		for _, e := range g.Entries {
			if e == 4842 {
				n++
				if len(g.Entries) != 2 || len(g.Positions) != 2 || g.Radius != 10 {
					t.Errorf("overlord_ramtusk pull group %d with Halmgar: entries %v radius %v, want 2 entries, 2 positions, radius 10", i, g.Entries, g.Radius)
				}
			}
		}
	}
	if n != 1 {
		t.Errorf("4842 is in %d pull groups of overlord_ramtusk, want 1", n)
	}
	// the Earthbreaker + Stalker + Sage trio: radius 12 keeps the mate Earthbreaker (16 yd) and Halmgar (14 yd) out
	g := r.PullGroups[len(r.PullGroups)-8]
	if g.Entries[0] != 4525 || len(g.Entries) != 3 || g.Radius != 12 {
		t.Errorf("overlord_ramtusk trio: entries %v radius %v, want [4525 6035 4518] radius 12", g.Entries, g.Radius)
	}
	c := rfkLeg(t, p, "charlga_razorflank")
	n = 0
	for i, g := range c.PullGroups {
		for _, e := range g.Entries {
			if e == 4425 {
				n++
				if len(g.Entries) != 2 || len(g.Positions) != 2 || g.Radius != 10 {
					t.Errorf("charlga_razorflank pull group %d with the Blind Hunter: entries %v radius %v, want 2 entries, 2 positions, radius 10", i, g.Entries, g.Radius)
				}
			}
		}
	}
	if n != 1 {
		t.Errorf("4425 is in %d pull groups of charlga_razorflank, want 1", n)
	}
}

// The overlapping clusters of the rotthorn leg are one pull each, not two pulls that take each other's mates as uncounted
// joiners (the Blood of Agamaggan and its Agam'ar stand 4 yd apart, each thicket pair 2-4 yd), and the plateau packs' radii do
// not reach across the 28 yd of floor between them.
func TestRazorfenBrambleClustersAreSinglePulls(t *testing.T) {
	p := loadPlan(t, rfkPlan)
	r := rfkLeg(t, p, "rotthorn")
	pit := rfkLeg(t, p, "charlga_pit")
	// the two thicket merges share entries, positions and radius: two groups of [62502 62501 62501] at radius 10
	n := 0
	for _, g := range r.PullGroups {
		if fmt.Sprint(g.Entries) == "[62502 62501 62501]" && g.Radius == 10 {
			n++
			if len(g.Positions) != 3 {
				t.Errorf("rotthorn thicket group: %d positions, want 3", len(g.Positions))
			}
		}
	}
	if n != 2 {
		t.Errorf("rotthorn: %d thicket pulls of [62502 62501 62501] radius 10, want 2", n)
	}
	for _, c := range []struct {
		leg     *Leg
		legName string
		entries string
		pos     int
		radius  float32
	}{
		{&r, "rotthorn", "[62502 62501]", 2, 12},
		{&r, "rotthorn", "[4512 4514 4541]", 3, 12},
		// the two-Spearhide pair left the rotthorn leg for the pit floor (pf-rfk-stands)
		{&pit, "charlga_pit", "[4514 4541]", 2, 10},
	} {
		n := 0
		for _, g := range c.leg.PullGroups {
			if fmt.Sprint(g.Entries) == c.entries {
				n++
				if len(g.Positions) != c.pos || g.Radius != c.radius {
					t.Errorf("%s group %v: %d positions radius %v, want %d and %v", c.legName, g.Entries, len(g.Positions), g.Radius, c.pos, c.radius)
				}
			}
		}
		if n != 1 {
			t.Errorf("%s: %v is %d pull groups, want 1", c.legName, c.entries, n)
		}
	}
}

// r35 slot7b (2026-10-24) stopped at the first pit stand of the charlga leg (step 655): the whole party walked from the den
// floor (2157 1941 z65) up the z76 dead-end ledge over the pit, 28.1 yd from and 25 yd above the stand, because the plan asked
// for one 49.1 yd move whose only walkable route is the 1200-yd loop round the instance (the server's own path ends on the
// ledge, and the offline route the exporter would have cut runs 1347 yd). A leg's via chain is the plan field that can row
// that walk: a pull group's approach holds at most 4 points, and every move must stay under the run leader's 150 yd
// ReactDistance. charlga_den keeps the den corridor packs, charlga_pit walks the loop down and pulls the pit, and
// charlga_razorflank walks it back up to the ledge corner for the pit-rim, upper-ledge and corridor packs and Charlga.
func TestRazorfenCharlgaLegsWalkThePitLoop(t *testing.T) {
	p := loadPlan(t, rfkPlan)
	den := rfkLeg(t, p, "charlga_den")
	if got := fmt.Sprintf("%.1f %.1f %.4f", den.To.X, den.To.Y, den.To.Z); got != "2135.6 1978.4 60.1695" {
		t.Errorf("charlga_den to = %s, want the den corridor point 2135.6 1978.4 60.1695", got)
	}
	if len(den.Via) != 0 {
		t.Errorf("charlga_den has %d via points: the den packs are walked from the corridor point", len(den.Via))
	}
	if den.Boss != nil {
		t.Errorf("charlga_den holds the boss %+v: Charlga is charlga_razorflank's", den.Boss)
	}
	pit := rfkLeg(t, p, "charlga_pit")
	if got := fmt.Sprintf("%.1f %.1f %.1f", pit.To.X, pit.To.Y, pit.To.Z); got != "2095.0 1845.0 50.8" {
		t.Errorf("charlga_pit to = %s, want the pit stand 2095.0 1845.0 50.8", got)
	}
	if len(pit.Via) != 31 {
		t.Errorf("charlga_pit has %d via points, want the 31 of the loop walk", len(pit.Via))
	}
	if got := fmt.Sprintf("%.1f %.1f %.1f", pit.Via[0].X, pit.Via[0].Y, pit.Via[0].Z); got != "2112.0 1877.3 72.8" {
		t.Errorf("charlga_pit via 0 = %s, want the spiral ramp foot 2112.0 1877.3 72.8", got)
	}
	if got := pit.Via[len(pit.Via)-1]; got.X != 2126.9 || got.Y != 1824 || got.Z != 47.8 {
		t.Errorf("charlga_pit last via = %+.1f %+.1f %+.1f, want the pit mouth 2126.9 1824 47.8", got.X, got.Y, got.Z)
	}
	// the pit's first pack is pulled from the leg's own stand: no approach row, so the party does not walk to the pack
	if a := pit.PullGroups[0].Approach; len(a) != 0 {
		t.Errorf("charlga_pit pull group 0 has the approach %+v: the leg's 'to' is its stand", a)
	}
	up := rfkLeg(t, p, "charlga_razorflank")
	if got := fmt.Sprintf("%.1f %.1f %.1f", up.To.X, up.To.Y, up.To.Z); got != "2093.1 1767.5 79.8" {
		t.Errorf("charlga_razorflank to = %s, want route corner 2093.1 1767.5 79.8", got)
	}
	if len(up.Via) != 26 {
		t.Errorf("charlga_razorflank has %d via points, want the 26 of the loop walk back", len(up.Via))
	}
	if got := fmt.Sprintf("%.1f %.1f %.1f", up.Via[0].X, up.Via[0].Y, up.Via[0].Z); got != "2117.1 1807.5 48.0" {
		t.Errorf("charlga_razorflank via 0 = %s, want the pit mouth point 2117.1 1807.5 48.0", got)
	}
	if got := up.Via[len(up.Via)-1]; got.X != 2102.7 || got.Y != 1728 || got.Z != 78.2 {
		t.Errorf("charlga_razorflank last via = %+.1f %+.1f %+.1f, want the ledge point 2102.7 1728 78.2", got.X, got.Y, got.Z)
	}
	if a := up.PullGroups[0].Approach; len(a) != 0 {
		t.Errorf("charlga_razorflank pull group 0 has the approach %+v: the leg's 'to' is its stand", a)
	}
	// every hop of both walks stays under the run leader's 150 yd ReactDistance (a longer cmd move is refused too-far), and
	// under the exporter's 60 yd cut (a longer move would be cut again into interpolated points that leave the mesh).
	// The leg start is the previous leg's 'to' (plan check) while the exporter anchors on the last row before the leg (a pull
	// row); both are covered: the first via within the 150 yd ReactDistance of the leg start, every other hop within the
	// exporter's 60 yd cut.
	legs := []Leg{pit, up}
	starts := []Point{den.To, pit.To}
	for i, l := range legs {
		if d := dist3v(starts[i], l.Via[0].Point); d > 150 {
			t.Errorf("leg %s: via 0 is %.1f yd from the leg start (ReactDistance 150)", l.Name, d)
		}
		prev := l.Via[0].Point
		for j, w := range l.Via[1:] {
			if d := dist3v(prev, w.Point); d > 60 {
				t.Errorf("leg %s via %d: %.1f yd from the point before it (the exporter would cut it)", l.Name, j+1, d)
			}
			prev = w.Point
		}
		if d := dist3v(prev, l.To); d > 60 {
			t.Errorf("leg %s: 'to' is %.1f yd from the last via (the exporter would cut it)", l.Name, d)
		}
	}
}

// pf-rfk-stands (refast razorfen_kraul slotD, 2026-10-25): the r39 run logged "no firing stand found" at steps 10 and 25
// (packs 4511/4512 of the Roogug leg) and 315 (the rotthorn leg's 4511, with the pair above it never killed: "1 units out of
// reach after 3 pulls that killed nothing", 2 wipes and the run's only pause). The cause is a floor mismatch, not range: those
// packs stand on lower floors the plan pulled from the upper one. The lower pocket off the ramp (z 62-65) is now walked down
// before its first pull, and its four packs are pulled in the order the party meets them (the ramp foot is at the south end),
// so each next pack is in range from the one before. The five rotthorn packs in the pit north of the spiral are unreachable
// from the spiral top (the server's path from 2102.67 1728 78.19 to the 4511 at 2046.61 1765.31 59.46 is the 454 yd incomplete
// one that ends on the ledge 23 yd above it; the pit stand 2095 1845 50.8 reaches it in 147.4 yd), and so is the Roogug leg's
// [4512 4511] pair at 2069.06 1701.43 57.604 (its 183 yd route loops through the Ramtusk leg's packs). All six moved to
// charlga_pit, which stands on their floor, with a one-point approach stand each (15-24.4 yd from its pack, a complete server
// path from the stand before, on the pack's floor).
func TestRazorfenLowerFloorPacksArePulledFromTheirOwnFloor(t *testing.T) {
	p := loadPlan(t, rfkPlan)
	rog := rfkLeg(t, p, "roogug")
	if len(rog.PullGroups) != 31 {
		t.Fatalf("roogug: %d pull groups, want 31 (the [4512 4511] pair left for charlga_pit)", len(rog.PullGroups))
	}
	// the lower pocket, in walk order from the ramp foot: 4514, the [4511 4514] pair, 4511, 4541
	want := []struct {
		entries string
		x, y, z float32
	}{
		{"[4514]", 2067.24, 1599.55, 62.8485},
		{"[4511 4514]", 2059.58, 1617.29, 62.9914},
		{"[4511]", 2065.89, 1633.15, 64.296},
		{"[4541]", 2061.45, 1646.19, 61.9304},
	}
	for i, w := range want {
		g := rog.PullGroups[6+i]
		pos := g.Positions[0]
		if fmt.Sprint(g.Entries) != w.entries || pos.X != w.x || pos.Y != w.y || pos.Z != w.z {
			t.Errorf("roogug group %d: %v at %.2f %.2f %.2f, want %s at %.2f %.2f %.2f", 6+i, g.Entries, pos.X, pos.Y, pos.Z, w.entries, w.x, w.y, w.z)
		}
	}
	// the descent is a pull group's four approach points: the upper corridor, the ramp head, the ramp foot, the pocket stand
	down := rog.PullGroups[6].Approach
	if len(down) != 4 || down[0].X != 2016 || down[0].Y != 1554 || down[3].X != 2059 || down[3].Y != 1582 {
		t.Errorf("roogug group 6 approach %+v, want the 4-point walk down to the lower pocket (2016 1554 ... 2059 1582)", down)
	}
	for i := 1; i < len(down); i++ {
		if d := dist3v(down[i-1], down[i]); d > 60 {
			t.Errorf("roogug group 6 approach point %d: %.1f yd from the one before it (the exporter would cut it)", i, d)
		}
	}
	for _, g := range rog.PullGroups {
		if fmt.Sprint(g.Entries) == "[4512 4511]" && len(g.Positions) == 2 && g.Positions[0].X == 2069.06 {
			t.Errorf("roogug still pulls the 2069.06 1701.43 pair: its floor is the pit's, not the upper walkway's")
		}
	}
	// the rotthorn leg keeps only the packs the spiral reaches; the five pit packs are gone
	rot := rfkLeg(t, p, "rotthorn")
	if len(rot.PullGroups) != 12 {
		t.Fatalf("rotthorn: %d pull groups, want 12", len(rot.PullGroups))
	}
	for _, g := range rot.PullGroups {
		for _, pos := range g.Positions {
			if (math.Abs(float64(pos.X-2080.08)) < 0.5 && math.Abs(float64(pos.Y-1784.59)) < 0.5) ||
				(math.Abs(float64(pos.X-2046.61)) < 0.5 && math.Abs(float64(pos.Y-1765.31)) < 0.5) ||
				(math.Abs(float64(pos.X-2032.57)) < 0.5 && math.Abs(float64(pos.Y-1727.98)) < 0.5) ||
				(math.Abs(float64(pos.X-2044.24)) < 0.5 && math.Abs(float64(pos.Y-1709.91)) < 0.5) {
				t.Errorf("rotthorn still pulls a pit pack at %.2f %.2f: the spiral top cannot reach it", pos.X, pos.Y)
			}
		}
	}
	pit := rfkLeg(t, p, "charlga_pit")
	if len(pit.PullGroups) != 17 {
		t.Fatalf("charlga_pit: %d pull groups, want 17", len(pit.PullGroups))
	}
	moved := pit.PullGroups[11:]
	wantPit := []string{"[4514]", "[4511]", "[4514 4541]", "[4514]", "[4514]", "[4512 4511]"}
	for i, g := range moved {
		if fmt.Sprint(g.Entries) != wantPit[i] {
			t.Errorf("charlga_pit group %d: %v, want %s", 11+i, g.Entries, wantPit[i])
		}
		if len(g.Approach) != 1 {
			t.Errorf("charlga_pit group %d (%v): %d approach points, want the one stand", 11+i, g.Entries, len(g.Approach))
			continue
		}
		a := g.Approach[0]
		d := dist3v(a, g.Positions[0])
		if d < 15 || d > 24.5 {
			t.Errorf("charlga_pit group %d (%v): the stand is %.2f yd from the pack, want 15-24.5", 11+i, g.Entries, d)
		}
		if a.Z < 50 || a.Z > 61 {
			t.Errorf("charlga_pit group %d (%v): the stand z %.2f is not on the pit floor the pack stands on", 11+i, g.Entries, a.Z)
		}
	}
	// one stand each, and the chain is joined: every approach is a short complete walk from the one before
	for i := 1; i < len(moved); i++ {
		if d := dist3v(moved[i-1].Approach[0], moved[i].Approach[0]); d > 40 {
			t.Errorf("charlga_pit moved group %d: %.1f yd from the stand before it", 11+i, d)
		}
	}
}

// dist3v is the 3-D distance of two points (the plan's dist3 is unexported to this package's tests? it is here).
func dist3v(a, b Point) float64 {
	dx, dy, dz := float64(a.X-b.X), float64(a.Y-b.Y), float64(a.Z-b.Z)
	return math.Sqrt(dx*dx + dy*dy + dz*dz)
}

// r35 slot7b wiped three times at step 313 (the old rotthorn leg's 'to'): it stood on the first Quilguard Champion's own
// spawn (4.1 yd), so the party walked into the 2-Champion pack and the Stalker 6035, 25.9 yd away, joined. The 'to' is the
// spiral's own corner point 2102.67 1728 78.19 now, 25.7 yd from the pack and 20+ yd from every spawn. The two Quilguard
// Champions 87430/87431 (4 yd apart, the only patrolling spawns of the spiral: a 176 yd round trip up to the pit rim and back)
// were split across the rotthorn leg (87431) and the charlga leg (87430); pulling one while the other stands 4 yd away is the
// joiner the census warns of, so the pair is one pull of two and the charlga leg no longer books 87430 again.
func TestRazorfenRotthornSpiralStandAndPatrol(t *testing.T) {
	p := loadPlan(t, rfkPlan)
	r := rfkLeg(t, p, "rotthorn")
	if got := fmt.Sprintf("%.2f %.1f %.2f", r.To.X, r.To.Y, r.To.Z); got != "2102.67 1728.0 78.19" {
		t.Errorf("rotthorn to = %s, want the spiral corner 2102.67 1728.0 78.19", got)
	}
	if d := dist3v(r.To, r.PullGroups[0].Positions[0]); d < 15 || d > 30 {
		t.Errorf("rotthorn to is %.1f yd from its first pack: want a stand 15-30 yd off it", d)
	}
	g := r.PullGroups[1]
	if got := fmt.Sprint(g.Entries); got != "[4623 4623]" {
		t.Errorf("rotthorn pull group 1 entries %s, want the patrolling pair [4623 4623]", got)
	}
	if len(g.Positions) != 2 || g.Radius != 6 {
		t.Errorf("rotthorn pull group 1: %d positions radius %v, want 2 and 6", len(g.Positions), g.Radius)
	}
	if g.Patrol == nil || fmt.Sprint(g.Patrol.Entries) != "[4623]" || len(g.Patrol.Path) != 6 || g.Patrol.PeriodS != 70 || !g.Patrol.PeriodEstimate {
		t.Errorf("rotthorn pull group 1 patrol %+v, want entries [4623], the 6 creature_movement points, period 70 (estimate)", g.Patrol)
	}
	c := rfkLeg(t, p, "charlga_razorflank")
	n := 0
	for _, g := range c.PullGroups {
		holds := false
		for _, e := range g.Entries {
			holds = holds || e == 4623
		}
		if !holds {
			continue
		}
		n++
		if got := fmt.Sprintf("%.1f %.1f", g.Positions[0].X, g.Positions[0].Y); got != "2105.5 1822.5" {
			t.Errorf("charlga_razorflank pull group at %s holds a 4623: the pit-rim pair is the only one", got)
		}
	}
	if n != 1 {
		t.Errorf("charlga_razorflank books 4623 in %d pull groups, want the pit-rim pair only", n)
	}
	for _, g := range c.PullGroups {
		for _, pos := range g.Positions {
			if math.Abs(float64(pos.X-2094.39)) < 0.5 && math.Abs(float64(pos.Y-1773.56)) < 0.5 {
				t.Errorf("charlga_razorflank still pulls 87430 at 2094.39 1773.56: it is rotthorn pull group 1's pair")
			}
		}
	}
}
