// Package worldspawn reads one map's world export (tools/dungeongen/export.sh) and models what wakes when a party walks or
// stands somewhere: the aggro radius the core gives each creature (the run leader's rule, ai::AggroRadius in TortoiseBots'
// PullAggroEstimatePolicy.h), the creature_linking and creature_groups relations that make a pack, and the waypoint loops of
// the map's patrolling spawns. It is read-only over the export files and never touches a server.
package worldspawn

import (
	"encoding/csv"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"math"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"

	"github.com/pjerra/turtle-agent/internal/dungeon"
	"github.com/pjerra/turtle-agent/internal/packet"
)

// Creature flags the aggro model reads (src/game/Objects/Creature.h:53,68).
const (
	// NoAggro is CREATURE_FLAG_EXTRA_NO_AGGRO: the creature never wakes by proximity.
	NoAggro = 0x0002
	// NoAssist is CREATURE_FLAG_EXTRA_NO_ASSIST: the creature never joins a fight by family assistance.
	NoAssist = 0x010000
)

// creature_groups options the pack model reads (src/game/CreatureGroups.h:36-38).
const (
	// FormationMove is OPTION_FORMATION_MOVE: the member follows its leader's waypoints.
	FormationMove = 0x0001
	// AggroTogether is OPTION_AGGRO_TOGETHER: the members and their leader wake as one.
	AggroTogether = 0x0002
)

// linkAggro and linkToAggro are the creature_linking flags that carry an aggro (CreatureLinkingFlags, CreatureLinkingMgr.h):
// 0x1 wakes the slave when its master aggros, 0x2 wakes the master when the slave aggros.
const (
	linkAggro   = 0x0001
	linkToAggro = 0x0002
)

const (
	// defaultDetectionYd is Creature::m_detectionDistance's default when the template has none (TB kAggroDetectionYd).
	defaultDetectionYd = 20
	// defaultSpeedWalk is the export's speed_walk for a creature without one (the core's base walk speed).
	defaultSpeedWalk = 1
	// maxLevelDiffForAggroRange is MAX_LEVEL_DIFF_FOR_AGGRO_RANGE: the level difference floors at -25.
	maxLevelDiffForAggroRange = 25
	// minReachYd is the smallest aggro reach: min(detection, 5 yd) in Creature::GetAttackDistance.
	minReachYd = 5
	// patrolSpeedYdS is the speed patrol periods are estimated at: the run leader's plans use 2.5 yd/s (spec 5.2).
	patrolSpeedYdS = 2.5
)

// Spawn is one creature spawn of the world export (creatures.tsv, one row), plus the raid columns C0 appends.
type Spawn struct {
	GUID         uint64
	Entry        uint32
	Name         string
	LevelMax     int
	Rank         int
	Faction      int
	NPCFlags     uint32
	Pos          dungeon.Point
	Wander       float32
	MovementType int
	// DetectionYd is creature_template.detection_range; 20 yd for an export that has no such column.
	DetectionYd float32
	FlagsExtra  uint32
	SpeedWalk   float32
	// GroupLeader is the creature_groups leader's guid, the spawn's own guid when it leads a group, else 0.
	GroupLeader uint64
	// GroupFlags is the group's creature_groups flags, the option set the core builds by ORing its members' rows.
	GroupFlags uint32
	// Pool is the spawn's pool_creature.pool_entry, 0 when it is not pooled (it may not be spawned at all).
	Pool uint32
}

// Hostile is dungeongen's rule: creature faction 35 is friendly to players, and a creature with npc flags (a quest giver,
// vendor or trainer) is not a trash mob. Flags are not part of the test: a NO_AGGRO spawn is still listed and still hostile.
func (s Spawn) Hostile() bool { return s.Faction != 35 && s.NPCFlags == 0 }

// Link is one creature link: a slave and its master, with the creature_linking (or creature_linking_template) flag.
type Link struct {
	Slave, Master uint64
	Flag          uint32
}

// World is one map's spawns, links and waypoint loops.
type World struct {
	Map     uint32
	Spawns  []Spawn
	Links   []Link
	Patrols []Patrol // the DB's waypoint loops; see Patrol for why this is not patrols.Patrol
}

// AggroRadius is Creature::GetAttackDistance for one spawn against a party of partyLevel: the detection range minus the level
// difference (party minus creature, floored at -25 levels), never below min(detection, 5 yd), times the aggro rate. It is the
// run leader's estimate too (TortoiseBots ai::AggroRadius), so the two tools agree. A NO_AGGRO creature or one with no
// detection range never wakes: 0.
func AggroRadius(s Spawn, partyLevel int, rate float32) float32 {
	if s.FlagsExtra&NoAggro != 0 || s.DetectionYd < 1 {
		return 0
	}
	diff := partyLevel - s.LevelMax
	if diff < -maxLevelDiffForAggroRange {
		diff = -maxLevelDiffForAggroRange
	}
	reach := s.DetectionYd - float32(diff)
	floor := s.DetectionYd
	if floor > minReachYd {
		floor = minReachYd
	}
	if reach < floor {
		reach = floor
	}
	return reach * rate
}

// Load reads the export in dir (the files tools/dungeongen/export.sh writes for one map) and returns the map's world. The
// notes say what the export could not give (an old export's missing columns and files, unresolved entry links, patrolling
// spawns without waypoints); they are informational, never errors.
func Load(dir string, mapID uint32) (*World, []string, error) {
	var notes []string
	creatures, err := readTSV(filepath.Join(dir, "creatures.tsv"))
	if err != nil {
		return nil, nil, err
	}
	if creatures == nil {
		return nil, nil, fmt.Errorf("%s: no creatures.tsv (tools/dungeongen/export.sh writes the export)", dir)
	}
	spawns, err := loadSpawns(creatures, &notes)
	if err != nil {
		return nil, nil, err
	}
	sort.Slice(spawns, func(i, j int) bool { return spawns[i].GUID < spawns[j].GUID })
	w := &World{Map: mapID, Spawns: spawns}

	groups, err := readTSV(filepath.Join(dir, "groups.tsv"))
	if err != nil {
		return nil, nil, err
	}
	if err := applyGroups(w, groups, &notes); err != nil {
		return nil, nil, err
	}
	if err := loadLinks(w, dir, &notes); err != nil {
		return nil, nil, err
	}
	if err := loadPatrols(w, dir, &notes); err != nil {
		return nil, nil, err
	}
	return w, notes, nil
}

// loadSpawns reads creatures.tsv. The five raid columns are appended by C0's export.sh; an older file gets their defaults.
func loadSpawns(t *tsvFile, notes *[]string) ([]Spawn, error) {
	if err := t.require("guid", "entry", "name", "level_max", "rank", "faction", "npc_flags", "x", "y", "z", "wander",
		"movement_type", "group_leader"); err != nil {
		return nil, err
	}
	var missing []string
	for _, col := range []string{"detection_range", "flags_extra", "speed_walk", "group_flags", "pool"} {
		if !t.has(col) {
			missing = append(missing, col)
		}
	}
	if len(missing) > 0 {
		*notes = append(*notes, fmt.Sprintf("creatures.tsv: old export (no %s columns): detection %d yd, flags_extra 0, speed_walk %d, group_flags 0, pool 0 assumed",
			strings.Join(missing, ", "), defaultDetectionYd, defaultSpeedWalk))
	}
	out := make([]Spawn, 0, len(t.rows))
	for _, row := range t.rows {
		r := rowReader{t: t, row: row}
		s := Spawn{
			GUID:         uint64(r.num("guid")),
			Entry:        uint32(r.num("entry")),
			Name:         r.text("name"),
			LevelMax:     int(r.num("level_max")),
			Rank:         int(r.num("rank")),
			Faction:      int(r.num("faction")),
			NPCFlags:     uint32(r.num("npc_flags")),
			Pos:          dungeon.Point{X: float32(r.num("x")), Y: float32(r.num("y")), Z: float32(r.num("z"))},
			Wander:       float32(r.num("wander")),
			MovementType: int(r.num("movement_type")),
			DetectionYd:  float32(r.optional("detection_range", defaultDetectionYd)),
			FlagsExtra:   uint32(r.optional("flags_extra", 0)),
			SpeedWalk:    float32(r.optional("speed_walk", defaultSpeedWalk)),
			GroupLeader:  uint64(r.num("group_leader")),
			GroupFlags:   uint32(r.optional("group_flags", 0)),
			Pool:         uint32(r.optional("pool", 0)),
		}
		if r.err != nil {
			return nil, r.err
		}
		out = append(out, s)
	}
	return out, nil
}

// applyGroups fills GroupLeader and GroupFlags from groups.tsv (the creature_groups rows). The core ORs every member row's
// flags into its group and ignores a row whose member is the leader (CreatureGroups.cpp:AddMember), so a group's option set
// is the OR of its non-leader rows. Without the file (an export from before the raid columns) the creatures.tsv columns are
// all there is.
func applyGroups(w *World, t *tsvFile, notes *[]string) error {
	if t == nil {
		*notes = append(*notes, "groups.tsv: no creature_groups rows (an export before the raid columns): only creature_linking rows join packs")
		return nil
	}
	if err := t.require("leader_guid", "member_guid", "dist", "angle", "flags"); err != nil {
		return err
	}
	leaderOf := map[uint64]uint64{}
	flags := map[uint64]uint32{}
	for _, row := range t.rows {
		r := rowReader{t: t, row: row}
		leader, member, f := uint64(r.num("leader_guid")), uint64(r.num("member_guid")), uint32(r.num("flags"))
		if r.err != nil {
			return r.err
		}
		leaderOf[member] = leader
		if member != leader {
			flags[leader] |= f
		}
	}
	for leader := range flags {
		if _, ok := leaderOf[leader]; !ok {
			leaderOf[leader] = leader
		}
	}
	for i := range w.Spawns {
		s := &w.Spawns[i]
		if leader, ok := leaderOf[s.GUID]; ok {
			s.GroupLeader = leader
		}
		if s.GroupLeader != 0 {
			s.GroupFlags = flags[s.GroupLeader]
		}
	}
	*notes = append(*notes, fmt.Sprintf("groups.tsv: %d groups, %d members", len(flags), len(leaderOf)))
	return nil
}

// loadLinks reads creature_linking rows (links.tsv) and resolves creature_linking_template rows (link_templates.tsv) to the
// spawn guids they link on this map.
func loadLinks(w *World, dir string, notes *[]string) error {
	guidLinks, err := readTSV(filepath.Join(dir, "links.tsv"))
	if err != nil {
		return err
	}
	byGUID := map[uint64]bool{}
	if guidLinks == nil {
		*notes = append(*notes, "links.tsv: no creature_linking rows (an export before the raid columns): no link edges")
	} else if err := guidLinks.require("guid", "master_guid", "flag"); err != nil {
		return err
	} else {
		for _, row := range guidLinks.rows {
			r := rowReader{t: guidLinks, row: row}
			l := Link{Slave: uint64(r.num("guid")), Master: uint64(r.num("master_guid")), Flag: uint32(r.num("flag"))}
			if r.err != nil {
				return r.err
			}
			w.Links = append(w.Links, l)
			byGUID[l.Slave] = true
		}
		*notes = append(*notes, fmt.Sprintf("links.tsv: %d creature_linking rows", len(guidLinks.rows)))
	}

	templates, err := readTSV(filepath.Join(dir, "link_templates.tsv"))
	if err != nil {
		return err
	}
	if templates == nil {
		*notes = append(*notes, "link_templates.tsv: no creature_linking_template rows (an export before the raid columns): no entry links")
		return nil
	}
	if err := templates.require("entry", "map", "master_entry", "flag", "search_range"); err != nil {
		return err
	}
	byEntry := map[uint32][]Spawn{}
	for _, s := range w.Spawns {
		byEntry[s.Entry] = append(byEntry[s.Entry], s)
	}
	linked, unresolved, outOfRange := 0, 0, 0
	for _, row := range templates.rows {
		r := rowReader{t: templates, row: row}
		entry, mapID := uint32(r.num("entry")), uint32(r.num("map"))
		masterEntry, flag := uint32(r.num("master_entry")), uint32(r.num("flag"))
		searchRange := float32(r.num("search_range"))
		if r.err != nil {
			return r.err
		}
		if mapID != w.Map {
			continue
		}
		slaves, masters := byEntry[entry], byEntry[masterEntry]
		if len(slaves) == 0 || len(masters) == 0 {
			unresolved++
			continue
		}
		for _, slave := range slaves {
			if byGUID[slave.GUID] {
				continue // the core's GetLinkedTriggerInformation prefers a guid row over a template row
			}
			master, ok := nearestMaster(masters, slave.Pos, searchRange)
			if !ok {
				outOfRange++
				continue
			}
			w.Links = append(w.Links, Link{Slave: slave.GUID, Master: master.GUID, Flag: flag})
			linked++
		}
	}
	note := fmt.Sprintf("link_templates.tsv: %d rows, %d spawn links", len(templates.rows), linked)
	if unresolved > 0 {
		note += fmt.Sprintf("; %d rows unresolved (no spawn of the slave or master entry on the map)", unresolved)
	}
	if outOfRange > 0 {
		note += fmt.Sprintf("; %d spawns had no master within search_range", outOfRange)
	}
	*notes = append(*notes, note)
	return nil
}

// nearestMaster picks the master a creature_linking_template row gives a slave. search_range 0 is no limit in the core
// (IsSlaveInRangeOfMaster), and then every master qualifies: the core takes the first one it loaded, which the export can only
// approximate as the lowest guid. Otherwise it is the nearest master within search_range.
func nearestMaster(masters []Spawn, from dungeon.Point, searchRange float32) (Spawn, bool) {
	if searchRange <= 0 {
		return masters[0], true
	}
	best, bestD := -1, float32(0)
	for i, m := range masters {
		d := dist3(from, m.Pos)
		if d <= searchRange && (best < 0 || d < bestD) {
			best, bestD = i, d
		}
	}
	if best < 0 {
		return Spawn{}, false
	}
	return masters[best], true
}

// readTSV reads one export file. A missing file is (nil, nil): the caller notes what it could not read.
func readTSV(path string) (*tsvFile, error) {
	f, err := os.Open(path)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	defer f.Close()
	r := csv.NewReader(f)
	r.Comma = '\t'
	r.LazyQuotes = true
	r.FieldsPerRecord = -1
	head, err := r.Read()
	if err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	t := &tsvFile{path: path, cols: map[string]bool{}}
	for _, h := range head {
		t.cols[h] = true
	}
	for line := 2; ; line++ {
		rec, err := r.Read()
		if errors.Is(err, io.EOF) {
			return t, nil
		}
		if err != nil {
			return nil, fmt.Errorf("%s:%d: %w", path, line, err)
		}
		if len(rec) != len(head) {
			return nil, fmt.Errorf("%s:%d: %d fields, the header has %d", path, line, len(rec), len(head))
		}
		m := make(map[string]string, len(head))
		for i, h := range head {
			m[h] = rec[i]
		}
		t.rows = append(t.rows, tsvRow{line: line, m: m})
	}
}

// tsvFile is one parsed export file.
type tsvFile struct {
	path string
	cols map[string]bool
	rows []tsvRow
}

// tsvRow is one data row with its file line, for error messages.
type tsvRow struct {
	line int
	m    map[string]string
}

func (t *tsvFile) has(col string) bool { return t.cols[col] }

// require checks the columns the model needs; a missing one names the file, as a reader of another export version would.
func (t *tsvFile) require(cols ...string) error {
	for _, col := range cols {
		if !t.cols[col] {
			return fmt.Errorf("%s: no %q column (is it from this version of tools/dungeongen/export.sh?)", t.path, col)
		}
	}
	return nil
}

// rowReader reads one row's columns, stopping at the first parse error.
type rowReader struct {
	t   *tsvFile
	row tsvRow
	err error
}

func (r *rowReader) text(col string) string { return r.row.m[col] }

func (r *rowReader) num(col string) float64 {
	if r.err != nil {
		return 0
	}
	n, err := strconv.ParseFloat(strings.TrimSpace(r.row.m[col]), 64)
	if err != nil {
		r.err = fmt.Errorf("%s:%d: column %q: %v", r.t.path, r.row.line, col, err)
		return 0
	}
	return n
}

// optional reads a column an older export has not: an absent or empty cell is the default.
func (r *rowReader) optional(col string, def float64) float64 {
	if !r.t.has(col) || strings.TrimSpace(r.row.m[col]) == "" {
		return def
	}
	return r.num(col)
}

// dist3 is the plain 3D distance, as the core measures every aggro and link range.
func dist3(a, b dungeon.Point) float32 {
	dx, dy, dz := a.X-b.X, a.Y-b.Y, a.Z-b.Z
	return float32(math.Sqrt(float64(dx*dx + dy*dy + dz*dz)))
}

// guidHex renders a creature spawn's world GUID (ObjectGuid.h: high unit, the entry, then the spawn guid).
func guidHex(entry uint32, guid uint64) string {
	return packet.GUID(uint64(packet.HighUnit)<<48 | uint64(entry)<<24 | guid&0x00FFFFFF).String()
}
