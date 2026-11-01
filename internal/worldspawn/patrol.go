package worldspawn

import (
	"fmt"
	"path/filepath"
	"sort"

	"github.com/pjerra/turtle-agent/internal/dungeon"
)

// Patrol is one waypoint loop of the world export: a spawn's creature_movement rows, or its entry's
// creature_movement_template rows. The fields are those of patrols.Patrol (internal/patrols, spec 6.1) that an export can
// fill, so a reader gets a DB patrol and a recorded one the same way; the recording-only fields (cycles seen, phase, sample
// count) have no DB source. internal/patrols is D1's branch, not this branch's base, so the type cannot be imported yet: its
// field names and meaning are kept identical for the merge.
type Patrol struct {
	ID             string          // the leader spawn's guid as a string (trash-check's key, the dashboard's wake.json id)
	GUIDs          []string        // the members' world GUIDs, leader first
	SpawnGUIDs     []uint64        // the members' spawn guids, leader first
	Entries        []uint32        // the members' entries, leader first
	Name           string          // the leader's name
	Path           []dungeon.Point // the loop's waypoints, in point order
	WaitsMS        []int           // the wait at each path point, ms
	PeriodS        float32         // one round: the loop at 2.5*speed_walk yd/s plus the waits
	PeriodEstimate bool            // always true for a DB loop: the phase was never recorded
	Source         string          // "creature_movement <guid>" or "creature_movement_template <entry>"
}

// waypoint is one creature_movement row.
type waypoint struct {
	point int
	pos   dungeon.Point
	wait  int
}

// loadPatrols reads the map's waypoint loops: movement.tsv per spawn guid, movement_template.tsv per entry. A patrolling
// spawn (movement_type 2) with no rows of either kind is a note: its walk cannot be modelled.
func loadPatrols(w *World, dir string, notes *[]string) error {
	byGUID, err := readMovement(dir, "movement.tsv", "guid", notes, "movement.tsv: no waypoints (an export before the raid columns): no patrols by guid")
	if err != nil {
		return err
	}
	byEntry, err := readMovement(dir, "movement_template.tsv", "entry", notes, "movement_template.tsv: no waypoint templates (an export before the raid columns): no patrols by entry")
	if err != nil {
		return err
	}

	var patrols []Patrol
	patrolOf := map[uint64]int{} // spawn guid -> index in patrols
	entryPatrol := map[uint32]int{}
	for _, s := range w.Spawns {
		if s.MovementType != 2 {
			continue
		}
		if points, ok := byGUID[s.GUID]; ok && len(points) >= 2 {
			patrols = append(patrols, newPatrol(s, points, fmt.Sprintf("creature_movement %d", s.GUID)))
			patrolOf[s.GUID] = len(patrols) - 1
			continue
		}
		if points, ok := byEntry[uint64(s.Entry)]; ok && len(points) >= 2 {
			if i, done := entryPatrol[s.Entry]; done {
				p := &patrols[i]
				p.SpawnGUIDs = append(p.SpawnGUIDs, s.GUID)
				p.GUIDs = append(p.GUIDs, guidHex(s.Entry, s.GUID))
				p.Entries = appendEntry(p.Entries, s.Entry)
				patrolOf[s.GUID] = i
				continue
			}
			patrols = append(patrols, newPatrol(s, points, fmt.Sprintf("creature_movement_template %d", s.Entry)))
			entryPatrol[s.Entry] = len(patrols) - 1
			patrolOf[s.GUID] = len(patrols) - 1
			continue
		}
		*notes = append(*notes, fmt.Sprintf("spawn %d (entry %d): movement_type 2 but fewer than 2 waypoints: no patrol", s.GUID, s.Entry))
	}

	// Formation followers walk their leader's loop without a loop of their own (creature_groups OPTION_FORMATION_MOVE).
	for _, s := range w.Spawns {
		if s.GroupFlags&FormationMove == 0 || s.GroupLeader == 0 || s.GroupLeader == s.GUID {
			continue
		}
		i, ok := patrolOf[s.GroupLeader]
		if !ok {
			continue
		}
		p := &patrols[i]
		if spawnIn(p.SpawnGUIDs, s.GUID) {
			continue
		}
		p.SpawnGUIDs = append(p.SpawnGUIDs, s.GUID)
		p.GUIDs = append(p.GUIDs, guidHex(s.Entry, s.GUID))
		p.Entries = appendEntry(p.Entries, s.Entry)
		patrolOf[s.GUID] = i
	}
	sort.Slice(patrols, func(i, j int) bool { return patrols[i].SpawnGUIDs[0] < patrols[j].SpawnGUIDs[0] })
	w.Patrols = patrols
	return nil
}

// readMovement parses movement.tsv (key guid) or movement_template.tsv (key entry) into sorted waypoint lists.
func readMovement(dir, name, key string, notes *[]string, missing string) (map[uint64][]waypoint, error) {
	t, err := readTSV(filepath.Join(dir, name))
	if err != nil {
		return nil, err
	}
	if t == nil {
		*notes = append(*notes, missing)
		return nil, nil
	}
	if err := t.require(key, "point", "x", "y", "z", "waittime"); err != nil {
		return nil, err
	}
	out := map[uint64][]waypoint{}
	for _, row := range t.rows {
		r := rowReader{t: t, row: row}
		wp := waypoint{
			point: int(r.num("point")),
			pos:   dungeon.Point{X: float32(r.num("x")), Y: float32(r.num("y")), Z: float32(r.num("z"))},
			wait:  int(r.num("waittime")),
		}
		k := uint64(r.num(key))
		if r.err != nil {
			return nil, r.err
		}
		out[k] = append(out[k], wp)
	}
	for k := range out {
		sort.Slice(out[k], func(i, j int) bool { return out[k][i].point < out[k][j].point })
	}
	return out, nil
}

// newPatrol makes the loop of one spawn from its waypoints: the path in point order, the waits, and the period the run
// leader's plans assume (the closed loop at 2.5*speed_walk yd/s plus the waits).
func newPatrol(s Spawn, points []waypoint, source string) Patrol {
	path := make([]dungeon.Point, len(points))
	waits := make([]int, len(points))
	loop := float64(0)
	totalWait := 0
	for i, wp := range points {
		path[i] = wp.pos
		waits[i] = wp.wait
		totalWait += wp.wait
		loop += float64(dist3(wp.pos, points[(i+1)%len(points)].pos))
	}
	speed := float64(s.SpeedWalk)
	if speed <= 0 {
		speed = defaultSpeedWalk
	}
	return Patrol{
		ID:             fmt.Sprint(s.GUID),
		GUIDs:          []string{guidHex(s.Entry, s.GUID)},
		SpawnGUIDs:     []uint64{s.GUID},
		Entries:        []uint32{s.Entry},
		Name:           s.Name,
		Path:           path,
		WaitsMS:        waits,
		PeriodS:        float32(loop/(patrolSpeedYdS*speed)) + float32(totalWait)/1000,
		PeriodEstimate: true,
		Source:         source,
	}
}

// appendEntry adds an entry once, in the order the entries were met.
func appendEntry(entries []uint32, e uint32) []uint32 {
	for _, have := range entries {
		if have == e {
			return entries
		}
	}
	return append(entries, e)
}

// spawnIn reports whether guid is in guids.
func spawnIn(guids []uint64, guid uint64) bool {
	for _, g := range guids {
		if g == guid {
			return true
		}
	}
	return false
}
