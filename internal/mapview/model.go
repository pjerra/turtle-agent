// Package mapview draws a run as a top-down map (issue #79): the navmesh floor as the background, the route of
// each party member, the hostile creatures with what became of them, and the events that matter (pulls, deaths,
// wipes, doors, GM teleports), as an SVG per floor and as a single-file HTML replay with a time slider.
//
// Everything is drawn from data the agent already records: the frames a session or a capture replay yields (where
// the party and the hostile units were), the event log, the dungeon_status report, botwatch samples, a dungeon
// plan and the server's mmaps. The package does no networking and has no dependency outside the standard library
// and this repository.
//
// Coordinates are the game's: x runs north, y runs west. The image has north up, so west is on the left.
package mapview

import (
	"math"
	"sort"
	"time"
)

// Pos is a position in the map's coordinates.
type Pos struct {
	X float32 `json:"x"`
	Y float32 `json:"y"`
	Z float32 `json:"z"`
}

// Sample is where a member was, seconds after the run's start.
type Sample struct {
	T float64 `json:"t"`
	Pos
	Map uint32 `json:"map"`
}

// Track is one party member's positions over time.
type Track struct {
	Name    string   `json:"name"`
	Leader  bool     `json:"leader,omitempty"`
	Samples []Sample `json:"samples"`
}

// Fate is what became of a hostile creature the party saw.
type Fate string

const (
	FateKilled  Fate = "killed"  // it died
	FateSkipped Fate = "skipped" // seen, never fought
	FateEvaded  Fate = "evaded"  // fought, then seen again alive and out of combat (it reset)
	FateAlive   Fate = "alive"   // fought and last seen alive and still fighting (a wipe, or out of view)
)

// Creature is a hostile creature, at the place it was first seen.
type Creature struct {
	GUID  uint64  `json:"guid"`
	Entry uint32  `json:"entry"`
	Name  string  `json:"name"`
	Level uint32  `json:"level,omitempty"`
	Boss  bool    `json:"boss,omitempty"`
	Pack  int     `json:"pack,omitempty"` // 0: alone; else the number of the pack (creatures within PackRadius of each other)
	Fate  Fate    `json:"fate"`
	Seen  float64 `json:"seen"`             // seconds after the start it was first seen
	FateT float64 `json:"fate_t,omitempty"` // and when its fate was settled (a kill; for others the last time it was seen)
	Map   uint32  `json:"map"`
	Pos
}

// Marker kinds.
const (
	MarkPull     = "pull"     // the first combat after a quiet spell
	MarkDeath    = "death"    // a party member died
	MarkWipe     = "wipe"     // the run ended in a wipe
	MarkDoor     = "door"     // a door, lever or button changed state
	MarkTeleport = "teleport" // the player was put somewhere else (GM teleport when Flag says so)
	MarkLeg      = "leg"      // a leg of the dungeon plan began
	MarkGM       = "gm"       // another GM shortcut of the dungeon report (revive, buff)
	MarkBossKill = "boss_kill"
)

// Marker is one event placed on the map.
type Marker struct {
	Kind  string  `json:"kind"`
	T     float64 `json:"t"`
	Label string  `json:"label,omitempty"`
	Flag  bool    `json:"flag,omitempty"` // a GM teleport: the dungeon report listed it as a shortcut
	From  *Pos    `json:"from,omitempty"` // a teleport's origin
	Map   uint32  `json:"map"`
	Pos
}

// Run is everything the map shows, with time in seconds after Start.
type Run struct {
	Title     string     `json:"title"`
	Map       uint32     `json:"map"`
	Start     time.Time  `json:"start"`
	Seconds   float64    `json:"seconds"`
	Tracks    []Track    `json:"tracks"`
	Creatures []Creature `json:"creatures"`
	Markers   []Marker   `json:"markers"`
	// Gaps are the spans (in seconds) with no position for a member longer than the gap limit.
	Gaps  int      `json:"gaps"`
	Notes []string `json:"notes,omitempty"`
}

// Box is an area of the map in game coordinates.
type Box struct{ MinX, MinY, MaxX, MaxY float32 }

func (b Box) Width() float32  { return b.MaxY - b.MinY } // across the image: y
func (b Box) Height() float32 { return b.MaxX - b.MinX } // up the image: x

// grow makes the box hold p.
func (b *Box) grow(x, y float32, first bool) {
	if first {
		*b = Box{x, y, x, y}
		return
	}
	b.MinX, b.MaxX = min(b.MinX, x), max(b.MaxX, x)
	b.MinY, b.MaxY = min(b.MinY, y), max(b.MaxY, y)
}

func (r *Run) hasCreatureOnMap() bool {
	for _, c := range r.Creatures {
		if c.Map == r.Map {
			return true
		}
	}
	return false
}

// How far past the party's own extent a creature, or a point of the plan, still counts for the image's box.
const (
	creatureReach = 30
	planReach     = 60
)

// Bounds is the box holding every track and event of the run on its map, pad yards wider on each side (and at least
// minSide yards each way), and the creatures and plan points (extra) near enough to them to matter: a creature seen
// from afar, or a stretch of the plan the run never came near, does not stretch the image. ok is false for a run
// with nothing on the map.
func (r *Run) Bounds(pad, minSide float32, extra ...Pos) (Box, bool) {
	var b Box
	first := true
	add := func(m uint32, x, y float32) {
		if m != r.Map {
			return
		}
		b.grow(x, y, first)
		first = false
	}
	for _, t := range r.Tracks {
		for _, s := range t.Samples {
			add(s.Map, s.X, s.Y)
		}
	}
	if first && !r.hasCreatureOnMap() { // nothing was seen on this map, whatever the markers say
		return Box{}, false
	}
	for _, m := range r.Markers {
		add(m.Map, m.X, m.Y)
		if m.From != nil {
			add(m.Map, m.From.X, m.From.Y)
		}
	}
	if first {
		// No route or event: the box holds the creatures and the plan.
		for _, c := range r.Creatures {
			add(c.Map, c.X, c.Y)
		}
		for _, p := range extra {
			add(r.Map, p.X, p.Y)
		}
	} else {
		reach := b
		near := func(x, y, by float32) bool {
			return x >= reach.MinX-by && x <= reach.MaxX+by && y >= reach.MinY-by && y <= reach.MaxY+by
		}
		for _, c := range r.Creatures {
			if c.Map == r.Map && near(c.X, c.Y, creatureReach) {
				b.grow(c.X, c.Y, false)
			}
		}
		for _, p := range extra {
			if near(p.X, p.Y, planReach) {
				b.grow(p.X, p.Y, false)
			}
		}
	}
	b.MinX, b.MinY, b.MaxX, b.MaxY = b.MinX-pad, b.MinY-pad, b.MaxX+pad, b.MaxY+pad
	for _, side := range []struct{ lo, hi *float32 }{{&b.MinX, &b.MaxX}, {&b.MinY, &b.MaxY}} {
		if d := *side.hi - *side.lo; d < minSide {
			mid := (*side.hi + *side.lo) / 2
			*side.lo, *side.hi = mid-minSide/2, mid+minSide/2
		}
	}
	return b, true
}

// dist2D is the distance in yards ignoring height.
func dist2D(a, b Pos) float64 { return math.Hypot(float64(a.X-b.X), float64(a.Y-b.Y)) }

// At is the member's position at time t (seconds after the start): interpolated between the samples around it,
// else the nearest sample. ok is false for a track with no sample.
func (t *Track) At(sec float64) (Pos, bool) {
	s := t.Samples
	if len(s) == 0 {
		return Pos{}, false
	}
	lo, hi := 0, len(s)
	for lo < hi { // the first sample after sec
		mid := (lo + hi) / 2
		if s[mid].T > sec {
			hi = mid
		} else {
			lo = mid + 1
		}
	}
	switch {
	case lo == 0:
		return s[0].Pos, true
	case lo == len(s):
		return s[len(s)-1].Pos, true
	}
	a, b := s[lo-1], s[lo]
	if b.T-a.T <= 0 || a.Map != b.Map {
		return a.Pos, true
	}
	f := float32((sec - a.T) / (b.T - a.T))
	return Pos{a.X + (b.X-a.X)*f, a.Y + (b.Y-a.Y)*f, a.Z + (b.Z-a.Z)*f}, true
}

// MapAt is the map of the member's last sample at or before t (the first sample's when t is earlier).
func (t *Track) MapAt(sec float64) uint32 {
	s := t.Samples
	if len(s) == 0 {
		return 0
	}
	i := sort.Search(len(s), func(i int) bool { return s[i].T > sec })
	if i == 0 {
		return s[0].Map
	}
	return s[i-1].Map
}
