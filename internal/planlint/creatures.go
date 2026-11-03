package planlint

import (
	"encoding/csv"
	"errors"
	"fmt"
	"io"
	"os"
	"strconv"
	"strings"

	"github.com/pjerra/turtle-agent/internal/dungeon"
)

// Creature is one row of the creature export (creatures.tsv, written by
// export.sh): a spawn of the map the plan runs on.
type Creature struct {
	GUID        uint64
	Entry       uint32
	Name        string
	Rank        int
	LevelMax    int
	Faction     int
	NPCFlags    uint32
	Pos         dungeon.Point
	GroupLeader uint64
	Patrol      bool
	Auras       []uint32 // spell ids of the auras the creature spawns with (template and spawn row)
	// DetectionRange is the creature_template detection_range (the yards at which a spawn of the entry engages, 0 when the
	// export has none: draft exports predate the column). RouteIntoLaterPacks defaults 0 to 20 yd, then applies the core
	// aggro rule (party level, spread, wander).
	DetectionRange float64
	// Wander is the spawn's wander_distance: how far it walks from its spot, added to the aggro reach (as the trash check does).
	Wander float32
	// FlagsExtra is the creature_template flags_extra: RouteIntoLaterPacks skips NO_AGGRO spawns outright (0 when the
	// export has none: draft exports predate the column).
	FlagsExtra uint32
	// PatrolPoint marks a copy of a patrolling spawn at one waypoint of its loop (the plan tests expand a lone
	// patroller's path into the pack): it demands the patrol clearance, not the plain spawn one.
	PatrolPoint bool
}

// String names the spawn for the walk-ready report.
func (c Creature) String() string { return c.Name }

// rows reads a TSV file and returns its rows keyed by the header names.
func rows(path string, need ...string) ([]map[string]string, error) {
	f, err := os.Open(path)
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
	for _, n := range need {
		found := false
		for _, h := range head {
			found = found || h == n
		}
		if !found {
			return nil, fmt.Errorf("%s: no %q column (is it from this version of export.sh?)", path, n)
		}
	}
	var out []map[string]string
	for line := 2; ; line++ {
		rec, err := r.Read()
		if errors.Is(err, io.EOF) {
			return out, nil
		}
		if err != nil {
			return nil, fmt.Errorf("%s:%d: %w", path, line, err)
		}
		if len(rec) != len(head) {
			return nil, fmt.Errorf("%s:%d: %d fields, the header has %d", path, line, len(rec), len(head))
		}
		m := map[string]string{}
		for i, h := range head {
			m[h] = rec[i]
		}
		out = append(out, m)
	}
}

type reader struct {
	path string
	err  error
}

func (r *reader) num(m map[string]string, k string) float64 {
	v, err := strconv.ParseFloat(m[k], 64)
	if err != nil && r.err == nil {
		r.err = fmt.Errorf("%s: %q in column %s is not a number", r.path, m[k], k)
	}
	return v
}

func (r *reader) pos(m map[string]string) dungeon.Point {
	return dungeon.Point{X: float32(r.num(m, "x")), Y: float32(r.num(m, "y")), Z: float32(r.num(m, "z"))}
}

// ReadCreatures reads the creature export (creatures.tsv) of a map.
func ReadCreatures(path string) ([]Creature, error) {
	rs, err := rows(path, "guid", "entry", "name", "rank", "faction", "npc_flags", "x", "y", "z", "group_leader", "patrol")
	if err != nil {
		return nil, err
	}
	r := &reader{path: path}
	var out []Creature
	for _, m := range rs {
		c := Creature{GUID: uint64(r.num(m, "guid")), Entry: uint32(r.num(m, "entry")), Name: m["name"], Rank: int(r.num(m, "rank")),
			Faction: int(r.num(m, "faction")), NPCFlags: uint32(r.num(m, "npc_flags")), Pos: r.pos(m), GroupLeader: uint64(r.num(m, "group_leader")),
			Patrol: m["patrol"] == "1", Auras: ParseAuras(m["auras"])}
		if l, err := strconv.Atoi(m["level_max"]); err == nil { // optional: only the band of a spec plan needs it
			c.LevelMax = l
		}
		if s, ok := m["detection_range"]; ok && s != "" { // optional: draft exports predate the column
			if v, err := strconv.ParseFloat(s, 64); err == nil {
				c.DetectionRange = v
			}
		}
		if s, ok := m["wander"]; ok && s != "" { // optional: draft exports predate the column
			if v, err := strconv.ParseFloat(s, 32); err == nil {
				c.Wander = float32(v)
			}
		}
		if s, ok := m["flags_extra"]; ok && s != "" { // optional: draft exports predate the column
			if v, err := strconv.ParseUint(s, 10, 32); err == nil {
				c.FlagsExtra = uint32(v)
			}
		}
		out = append(out, c)
	}
	return out, r.err
}

// ParseAuras reads the auras column of creatures.tsv: spell ids separated by spaces ("6408 5916"). A file written
// before the column existed has none, and NULL or empty is no aura.
func ParseAuras(s string) []uint32 {
	var out []uint32
	for _, f := range strings.Fields(s) {
		if n, err := strconv.ParseUint(f, 10, 32); err == nil && n != 0 {
			out = append(out, uint32(n))
		}
	}
	return out
}
