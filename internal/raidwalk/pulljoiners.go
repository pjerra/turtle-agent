package raidwalk

import (
	"encoding/json"
	"fmt"
	"math"
	"sort"
	"time"

	"github.com/pjerra/turtle-agent/internal/packet"
	"github.com/pjerra/turtle-agent/internal/planexport"
)

// PullJoiners is one pull attempt's joiner record: the pull's step and time,
// how many engaged hostiles outside the pulled pack joined within the window,
// and which guids. The walk writes one per attempt when the pull window
// closes; the report reads all of them from the walk folder (pulljoiners.json)
// and hangs each on the fight of its step whose window holds At.
type PullJoiners struct {
	Step  int           `json:"step"`
	At    time.Time     `json:"at"`
	Count int           `json:"count"`
	GUIDs []packet.GUID `json:"guids"`
}

// PullJoinersFName is the structured pull-joiner record the walk writes and the report reads.
const PullJoinersFName = "pulljoiners.json"

// PackRow is the plan pack of a pull step: its centroid and radius, within
// which the pulled guid's pack mates stand (planexport's pull row).
type PackRow struct {
	// HasRow is false when the step has no pull/boss row: nothing is owned by position.
	HasRow bool
	CX, CY float32
	Radius float32
}

// PullJoinersLine writes one pull's joiners as the run.log line:
// "step <n>: joiners <count> (<guids...>)". Empty when nobody joined.
func PullJoinersLine(step int, guids []packet.GUID) string {
	if n := JoinersNote(SortJoinerGUIDs(guids)); n != "" {
		return fmt.Sprintf("step %d: %s", step, n)
	}
	return ""
}

// ParsePullJoins reads the walk's per-pull joiner records (pulljoiners.json):
// every attempt keeps its record, with the pull's time, so a retried pull at
// one step does not replace the attempt before it.
func ParsePullJoins(raw []byte) []PullJoiners {
	var list []PullJoiners
	if err := json.Unmarshal(raw, &list); err != nil {
		return nil
	}
	var out []PullJoiners
	for _, j := range list {
		if j.Step > 0 {
			out = append(out, j)
		}
	}
	return out
}

// SortJoinerGUIDs orders joiner guids for stable records and notes.
func SortJoinerGUIDs(guids []packet.GUID) []packet.GUID {
	out := append([]packet.GUID(nil), guids...)
	sort.Slice(out, func(i, j int) bool { return out[i] < out[j] })
	return out
}

// PullPackMembers owns the pulled pack by spawn guid: the returned guids are
// the pack's other members. seed is the pulled guid; row is the plan pack of
// the pull step; pos holds the freshest known frame position per guid. A guid
// is a member when its position stands within the row's radius of the pack
// centroid, as JoinersOfPull's pack map does. Units outside the radius belong
// to a neighbouring pack, even when they share the entry. Without a row
// nothing is owned by position: only the pulled guid is the pack.
func PullPackMembers(seed packet.GUID, row PackRow, pos map[packet.GUID][3]float32) []packet.GUID {
	if !row.HasRow {
		return nil
	}
	var members []packet.GUID
	for g, p := range pos {
		if g == seed {
			continue
		}
		dx, dy := float64(p[0])-float64(row.CX), float64(p[1])-float64(row.CY)
		if math.Hypot(dx, dy) <= float64(row.Radius) {
			members = append(members, g)
		}
	}
	return SortJoinerGUIDs(members)
}

// RowRadius is the module's rule (TortoiseBots origin): a pull row's radius of
// 0 means DefaultPullYd, a boss row's DefaultBossRad.
func RowRadius(kind string, radius float32) float32 {
	if radius > 0 {
		return radius
	}
	if kind == planexport.KindBoss {
		return planexport.DefaultBossRad
	}
	return planexport.DefaultPullYd
}
