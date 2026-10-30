// Package patrols reads and writes the recorded patrol file (tb-patrols) and turns a recording of unit positions into patrols:
// loops with their measured period and path, static units and wanderers.
package patrols

import (
	"encoding/json"
	"fmt"
	"os"
	"time"

	"github.com/pjerra/turtle-agent/internal/dungeon"
)

const (
	// Format is the file format's name, and Version the version this package reads and writes.
	Format  = "tb-patrols"
	Version = 1
	// maxPeriodS bounds a patrol's round, as dungeon's maxPatrolPeriod does: the plan schema refuses more.
	maxPeriodS = 900
)

// File is a tb-patrols document: what a recording found on one map, plus where and how it was recorded.
type File struct {
	Format    string     `json:"format"`
	Version   int        `json:"version"`
	Map       uint32     `json:"map"`
	Source    string     `json:"source"` // "record" or "db": how the file was made
	Created   time.Time  `json:"created"`
	Area      Area       `json:"area"`
	Minutes   int        `json:"minutes"`
	SampleMS  int        `json:"sample_ms"`
	Patrols   []Patrol   `json:"patrols"`
	Static    []Static   `json:"static"`
	Wanderers []Wanderer `json:"wanderers"`
	// Stopped says why a recording ended early (combat, death, lost world); empty for a complete one.
	Stopped string `json:"stopped"`
}

// Area is the circle a recording watched, on one floor.
type Area struct {
	X float32 `json:"x"`
	Y float32 `json:"y"`
	R float32 `json:"r"`
}

// Patrol is one group of units that walks the same loop: the lead member's measured round, and every member's guid.
type Patrol struct {
	ID         string          `json:"id"`          // the matched spawn guid, else the lead's hex guid
	GUIDs      []string        `json:"guids"`       // every member's hex guid, lead first
	SpawnGUIDs []uint32        `json:"spawn_guids"` // the world export's spawn guids, when matched
	Entries    []uint32        `json:"entries"`
	Name       string          `json:"name"`
	Path       []dungeon.Point `json:"path"`
	WaitsMS    []int           `json:"waits_ms"` // time spent at each point of Path, 0 under 2 s
	PeriodS    float32         `json:"period_s"`
	// PeriodEstimate says PeriodS is the recording's span because no loop was seen, or a loop seen fewer than twice.
	PeriodEstimate bool `json:"period_estimate"`
	CyclesSeen     int  `json:"cycles_seen"`
	// PhaseEpochMS is the wall time (unix ms) of the latest sighting at Path[0], so a reader can predict the patrol's place.
	PhaseEpochMS int64 `json:"phase_epoch_ms"`
	Complete     bool  `json:"complete"` // a loop was found within the recording
	// Samples is how many sightings of the lead member the analysis used.
	Samples int    `json:"samples"`
	Source  string `json:"source"`
}

// ToDungeon makes the plan schema's patrol block from the measured round.
func (p Patrol) ToDungeon() dungeon.Patrol {
	return dungeon.Patrol{
		Entries:        p.Entries,
		Path:           p.Path,
		PeriodS:        p.PeriodS,
		PeriodEstimate: p.PeriodEstimate,
		Source:         p.Source,
	}
}

// Static is a unit that stayed in one place.
type Static struct {
	GUID  string  `json:"guid"`
	Entry uint32  `json:"entry"`
	X     float32 `json:"x"`
	Y     float32 `json:"y"`
	Z     float32 `json:"z"`
}

// Wanderer is a unit that moved around a small area without a loop (random movement, a short patrol).
type Wanderer struct {
	GUID   string        `json:"guid"`
	Entry  uint32        `json:"entry"`
	Center dungeon.Point `json:"center"`
	Radius float32       `json:"radius"`
}

// Read parses a tb-patrols file and refuses another format or version, a patrol path with fewer than two points, and a round
// outside 1-900 s (what the plan schema accepts).
func Read(path string) (*File, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var f File
	if err := json.Unmarshal(data, &f); err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	if f.Format != Format {
		return nil, fmt.Errorf("%s: format %q, not %q", path, f.Format, Format)
	}
	if f.Version != Version {
		return nil, fmt.Errorf("%s: version %d, not %d", path, f.Version, Version)
	}
	for i, p := range f.Patrols {
		if len(p.Path) < 2 {
			return nil, fmt.Errorf("%s: patrol %d (%s): %d path points, need 2", path, i, p.ID, len(p.Path))
		}
		if p.PeriodS <= 0 || p.PeriodS > maxPeriodS {
			return nil, fmt.Errorf("%s: patrol %d (%s): period %g s, need 1-%d", path, i, p.ID, p.PeriodS, maxPeriodS)
		}
	}
	return &f, nil
}

// Write creates path with the file's JSON, mode 0600. It refuses to overwrite an existing file.
func Write(path string, f *File) error {
	data, err := json.MarshalIndent(f, "", "  ")
	if err != nil {
		return err
	}
	data = append(data, '\n')
	out, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return err
	}
	if _, err := out.Write(data); err != nil {
		out.Close()
		os.Remove(path)
		return err
	}
	if err := out.Close(); err != nil {
		os.Remove(path)
		return err
	}
	return nil
}
