// quietstart.go: the offline start choice of `raid-walk --place` (walk 33). Before a fresh instance is entered, a
// requested start step is walked back along the plan's move rows to the nearest row whose point stands clear of the
// aggro of every static spawn of the packs the run has not pulled yet. No network, no session: the command applies it
// in validate(), with the world export the placement lints and --gm-clear read.
package raidwalk

import (
	"fmt"
	"math"

	"github.com/pjerra/turtle-agent/internal/dungeon"
	"github.com/pjerra/turtle-agent/internal/planexport"
	"github.com/pjerra/turtle-agent/internal/worldspawn"
)

const (
	// PlaceQuietGap is how far beyond a spawn's aggro radius a placement step must stand: the placement's own spread,
	// the toolkit's landing ring and the time before the run leader pulls (walk 33: a start 3-6.5 yd from a not-yet
	// pulled pack burned a fresh instance on the quiet check).
	PlaceQuietGap = 8
	// placeQuietPullReach and placeQuietBossReach are the row reaches the pull estimator uses to say a row owns a pack
	// (trashcheck targetReach: radius+5, else 10 for a pull and 30 for a boss).
	placeQuietPullReach = 10
	placeQuietBossReach = 30
	// placeQuietOwnSlack is added to a row's own radius for that ownership test.
	placeQuietOwnSlack = 5
)

// placeQuietHazard is one static spawn of a not-yet-pulled pack, the row that will pull it, and the distance the
// placement must keep from it.
type placeQuietHazard struct {
	spawn    worldspawn.Spawn
	fromStep int
	// plainReach, when above zero, is the aggro radius of a boss the world export holds no spawn of: the default
	// detection range, whatever the party's level (a spawn with no stats cannot be judged by level).
	plainReach float64
}

// placeQuietBossDetectYd is the detection range a boss row's hazard gets when the world holds no spawn of the boss
// (creature_template.detection_range's default, the same 20 yd a pack member without the column gets).
const placeQuietBossDetectYd = 20

// placeQuietBossSpawnYd is how far from the boss row a world spawn of the boss's entry may stand and still be the boss
// (the row's position is the plan's boss.position, not always the exact spawn point).
const placeQuietBossSpawnYd = 30

// placeQuietRowReach is how near a creature row may stand to a member of a pack it pulls (trashcheck's targetReach).
func placeQuietRowReach(s planexport.StepRow) float64 {
	if s.Radius > 0 {
		return float64(s.Radius) + placeQuietOwnSlack
	}
	if s.Kind == planexport.KindBoss {
		return placeQuietBossReach
	}
	return placeQuietPullReach
}

// placeQuietOwned says a row pulls a pack: one of the pack's member spawns carries an entry of the row and stands
// within the row's reach. It is trashcheck's packClearedBy rule, without the patrol loops (a static start choice does
// not judge a pack's loop).
func placeQuietOwned(p worldspawn.Pack, s planexport.StepRow, reach float64, spawns map[uint64]worldspawn.Spawn) bool {
	for _, g := range p.Members {
		m, ok := spawns[g]
		if !ok || !holdsEntry(s.Entries, m.Entry) {
			continue
		}
		dx, dy, dz := float64(m.Pos.X-s.X), float64(m.Pos.Y-s.Y), float64(m.Pos.Z-s.Z)
		if math.Sqrt(dx*dx+dy*dy+dz*dz) <= reach {
			return true
		}
	}
	return false
}

// placeQuietHazards are the static spawns of every pack a creature row at or after start pulls, with the first row
// that pulls it. A patrolling member (movement type 2) is not a static spawn and never counts: its own loop is the
// placement lints' business, and it does not stand at its spawn point.
func placeQuietHazards(world *worldspawn.World, rows planexport.Rows, start int) []placeQuietHazard {
	spawns := make(map[uint64]worldspawn.Spawn, len(world.Spawns))
	for _, s := range world.Spawns {
		spawns[s.GUID] = s
	}
	packs := world.Packs(worldspawn.PackOptions{})
	seen := map[uint64]bool{}
	var out []placeQuietHazard
	for i := start - 1; i < len(rows.Steps); i++ {
		s := rows.Steps[i]
		if !creatureRow(s) || !placedRow(s) {
			continue
		}
		reach := placeQuietRowReach(s)
		for _, p := range packs {
			if !placeQuietOwned(p, s, reach, spawns) {
				continue
			}
			for _, g := range p.Members {
				m, ok := spawns[g]
				if !ok || m.MovementType == 2 || seen[g] {
					continue
				}
				seen[g] = true
				out = append(out, placeQuietHazard{spawn: m, fromStep: i + 1})
			}
		}
	}
	// A boss stands alone: no pack owns it, so the pack loop above never sees it (walk 46 Jin'do: the start 25.2 yd from
	// his spawn pulled him). Each boss row at or after start is a hazard at the plan's boss position, with the stats
	// of the export's spawn of that entry when one stands near, else the default detection range.
	for i := start - 1; i < len(rows.Steps); i++ {
		s := rows.Steps[i]
		if s.Kind != planexport.KindBoss || !placedRow(s) {
			continue
		}
		out = append(out, placeQuietBossHazard(world, s, i+1))
	}
	return out
}

// placeQuietBossHazard is the hazard of one boss row, standing at the row's position.
func placeQuietBossHazard(world *worldspawn.World, s planexport.StepRow, step int) placeQuietHazard {
	pos := dungeon.Point{X: s.X, Y: s.Y, Z: s.Z}
	var best worldspawn.Spawn
	bestD, found := math.Inf(1), false
	for _, sp := range world.Spawns {
		if sp.Entry != s.Entry {
			continue
		}
		dx, dy, dz := float64(sp.Pos.X-pos.X), float64(sp.Pos.Y-pos.Y), float64(sp.Pos.Z-pos.Z)
		if d := math.Sqrt(dx*dx + dy*dy + dz*dz); d <= placeQuietBossSpawnYd && d < bestD {
			best, bestD, found = sp, d, true
		}
	}
	if !found {
		return placeQuietHazard{
			spawn:      worldspawn.Spawn{Entry: s.Entry, Name: "boss", Pos: pos},
			fromStep:   step,
			plainReach: placeQuietBossDetectYd,
		}
	}
	best.Pos = pos
	return placeQuietHazard{spawn: best, fromStep: step}
}

// placeQuietNeed is the distance a point must keep from a hazard at the party's level.
func placeQuietNeed(h placeQuietHazard, partyLevel int) float64 {
	if h.plainReach > 0 {
		return h.plainReach + PlaceQuietGap
	}
	level := partyLevel
	if level <= 0 {
		level = h.spawn.LevelMax
	}
	return float64(worldspawn.AggroRadius(h.spawn, level, 1)) + PlaceQuietGap
}

// placeQuietBlocked is the hazard that leaves the point's clearance lowest (distance minus the distance it needs),
// and whether any hazard is closer than it may be.
func placeQuietBlocked(hz []placeQuietHazard, p dungeon.Point, partyLevel int) (worst placeQuietHazard, dist, need float64, blocked bool) {
	worst = placeQuietHazard{}
	margin := math.Inf(1)
	for _, h := range hz {
		dx, dy, dz := float64(h.spawn.Pos.X-p.X), float64(h.spawn.Pos.Y-p.Y), float64(h.spawn.Pos.Z-p.Z)
		d := math.Sqrt(dx*dx + dy*dy + dz*dz)
		n := placeQuietNeed(h, partyLevel)
		if m := d - n; m < margin {
			margin, worst, dist, need = m, h, d, n
		}
	}
	return worst, dist, need, margin < 0
}

// PlaceQuietStart is the offline start choice of a placed walk: from start it walks back along the plan's positioned
// move rows to the nearest row whose point stands at least each static spawn's aggro radius + PlaceQuietGap from
// every static spawn of the packs a creature row at or after start pulls. The requested row itself is tested first.
// A world with no such spawn, or a nil world, leaves the start as given. The note names the chosen row and the reason
// (the nearest hazard and what it needs), and says when no earlier move row is quiet either.
func PlaceQuietStart(rows planexport.Rows, world *worldspawn.World, start, partyLevel int) (int, string) {
	if world == nil || start < 1 || start > len(rows.Steps) {
		return start, ""
	}
	hz := placeQuietHazards(world, rows, start)
	if len(hz) == 0 {
		return start, fmt.Sprintf("start step %d: no static spawn of a pack the run has not pulled yet (nothing to judge)", start)
	}
	point := rows.Steps[start-1]
	p := dungeon.Point{X: point.X, Y: point.Y, Z: point.Z}
	worst, dist, need, blocked := placeQuietBlocked(hz, p, partyLevel)
	if !blocked {
		return start, fmt.Sprintf("start step %d is clear: the nearest static spawn of a pack ahead is %d %s at %.1f yd (needs %.1f)",
			start, worst.spawn.Entry, worst.spawn.Name, dist, need)
	}
	why := fmt.Sprintf("start step %d stands %.1f yd from %d %s (spawn %d, pulled by step %d; needs %.1f)",
		start, dist, worst.spawn.Entry, worst.spawn.Name, worst.spawn.GUID, worst.fromStep, need)
	var best int
	var bestDist, bestNeed float64
	found := false
	for i := start - 2; i >= 0; i-- {
		s := rows.Steps[i]
		if !moveRow(s) || !placedRow(s) {
			continue
		}
		if _, ok := eventStep(rows, i); ok {
			continue
		}
		q := dungeon.Point{X: s.X, Y: s.Y, Z: s.Z}
		_, d, n, no := placeQuietBlocked(hz, q, partyLevel)
		if no {
			continue
		}
		best, bestDist, bestNeed = i+1, d, n
		found = true
		break
	}
	if !found {
		return start, why + "; no quiet move row stands before it"
	}
	return best, fmt.Sprintf("%s; the nearest quiet move row is step %d, %.1f yd from it (needs %.1f)", why, best, bestDist, bestNeed)
}

// PlaceLegName is the name of the leg a step belongs to (the step reader's own helper, exported for the caller that
// moved the start back to another leg).
func PlaceLegName(rows planexport.Rows, step int) string {
	if step < 1 || step > len(rows.Steps) {
		return ""
	}
	return startLegName(rows, rows.Steps[step-1].Leg)
}

// PlaceBack is the previous positioned move row before step (the restart walk-super takes, one move row at a time).
// false when no move row stands before it. The in-instance quiet retreat takes PlaceBackInLeg instead: a start placed
// at a leg must not step back out of it.
func PlaceBack(rows planexport.Rows, step int) (int, bool) {
	for i := step - 2; i >= 0; i-- {
		s := rows.Steps[i]
		if moveRow(s) && placedRow(s) {
			return i + 1, true
		}
	}
	return 0, false
}

// PlaceBackInLeg is the retreat a placed start may take (the in-instance quiet retry): the previous positioned move
// row before step, in the same leg as `leg` and with no boss or hold row between. false when there is none: a retreat
// across a boss or hold row, or out of the leg, would put the run before that row and the raid would re-fight it
// (walk 36 b: a start placed at leg high_priestess_arlokk refused the quiet circle at step 273, stepped back over
// Thekal's boss row to his approach at step 271, and the run started there and fought Thekal again; the leg lookup
// was right, the retreat was not).
func PlaceBackInLeg(rows planexport.Rows, step int, leg string) (int, bool) {
	if step < 1 || step > len(rows.Steps) {
		return 0, false
	}
	if k := rows.Steps[step-1].Kind; k == planexport.KindBoss || k == planexport.KindHold {
		return 0, false // a start on a boss or hold row already re-fights it; no retreat makes that worse
	}
	if leg == "" {
		// An explicit-step start names no leg: the retreat stays in the leg the row itself belongs to.
		leg = startLegName(rows, rows.Steps[step-1].Leg)
	}
	for i := step - 2; i >= 0; i-- {
		s := rows.Steps[i]
		if s.Kind == planexport.KindBoss || s.Kind == planexport.KindHold {
			return 0, false
		}
		if !moveRow(s) || !placedRow(s) {
			continue
		}
		if startLegName(rows, s.Leg) != leg {
			return 0, false
		}
		return i + 1, true
	}
	return 0, false
}
