package dungeon

import "math"

// The plan's tank_min_hp is set at the middle of the plan's level band from the tank's max health in the 2026-10 test runs. A hire may
// stand at any level inside the band, and a level-45 tank cannot carry the health of a level-47 one; the floor is scaled with the same
// per-level curve the floors were derived from, so it means "what a tank of this level had in the test runs".
//
// tankHealthAtLevel is the median max health of the tank at each level in tanks.tsv, over the levels with at least three runs of any
// plan (the derivation in the audit's ta-266-data/floors2.py). Level 58 and 59 use level 57's value: the runs there are nearly all
// level-60 tanks and a median of one or two would invent a dip. A level between two table rows takes the curve between them; below 16
// it scales down from 16's value, above 60 it stays at 60's.
var tankHealthAtLevel = []struct {
	level int
	hp    float64
}{
	{16, 916}, {18, 1183}, {20, 1359}, {21, 1396}, {22, 1524}, {25, 1774}, {27, 2189}, {28, 2283},
	{31, 2621}, {33, 2903}, {34, 3103}, {38, 3531}, {40, 3524}, {42, 3755}, {45, 3736}, {46, 3631},
	{47, 4110}, {51, 4851}, {52, 5125}, {55, 5849}, {56, 5651}, {57, 5915}, {60, 8489},
}

// tankCurve is the curve's value at a level.
func tankCurve(level int) float64 {
	if level <= 0 {
		return 0
	}
	if level > 57 && level < 60 {
		level = 57
	}
	if level <= tankHealthAtLevel[0].level {
		return tankHealthAtLevel[0].hp * float64(level) / float64(tankHealthAtLevel[0].level)
	}
	for i := 0; i < len(tankHealthAtLevel); i++ {
		row := tankHealthAtLevel[i]
		if level == row.level {
			return row.hp
		}
		if !(level < row.level) {
			continue
		}
		prev := tankHealthAtLevel[i-1]
		return prev.hp + (row.hp-prev.hp)*float64(level-prev.level)/float64(row.level-prev.level)
	}
	return tankHealthAtLevel[len(tankHealthAtLevel)-1].hp
}

// scaledTankFloor is the plan's floor for a tank at level: the floor set at the band's middle level times the curve at the tank's level
// over the curve at the middle, rounded down to 50 (a floor is never a harder bar than the run it came from).
func scaledTankFloor(floor uint32, mid, level int) uint32 {
	if floor == 0 || mid <= 0 || level <= 0 {
		return floor
	}
	c, cm := tankCurve(level), tankCurve(mid)
	if c <= 0 || cm <= 0 {
		return floor
	}
	v := float64(floor) * c / cm
	return uint32(math.Floor(v/50)) * 50
}
