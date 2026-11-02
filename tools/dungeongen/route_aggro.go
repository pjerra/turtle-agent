// RouteIntoLaterPacks reports the route segments that pass within aggro of a pack a later row pulls (see Check and
// PackOverlaps). The run leader walks from row to row; when the straight line between two consecutive rows crosses a
// later pack's aggro, the party pulls it early (Zul'Gurub walk 25: the move to group 12's approach crossed group 15's
// spawns). planexport.Build gives the rows; ownership says which row pulls each spawn (its entry in the row's entries,
// within its radius of the row's point, expanded to the spawn's whole formation by group_leader). Every spawn closer
// (3D) to a sampled point than its detection_range (20 when 0) is a finding, one line per (row pair, guid). Sampling
// every 2 yd on the straight segment is the approximation: the run leader's own navmesh hops are out of scope. So is
// the firing-stand walk of a plain pull row: its party point is the row's own point, not the firing stand the run
// leader walks out to (a stand-pull row instead carries its stand, and the check walks from there).
package main

import (
	"fmt"
	"math"
	"sort"

	"github.com/pjerra/turtle-agent/internal/dungeon"
	"github.com/pjerra/turtle-agent/internal/planexport"
)

// defaultDetectionYd is the yards at which a spawn engages when the export carries no detection_range (draft exports
// predate the column).
const defaultDetectionYd = 20

// routeAggroStep is how finely a row pair's segment is sampled (yards).
const routeAggroStep = 2

// routePoint is one row that puts the party somewhere (a move, or the fight point of a pull or boss row). Each row
// carries two positions: the OWNERSHIP point (x, y, z: the row's own point, used to own spawns in routeOwnerOf) and
// the PARTY point (px, py, pz: where the party stands for that row, used to draw the walked segments). A stand-pull
// pull or boss row (FlagStandPull) is fought from its stand, the move row right before it in the same leg, so its
// party point is the stand's point while its ownership point stays the row's point. A plain pull row keeps the row's
// point as its party point.
type routePoint struct {
	plan       string
	leg        int
	seq        int // the export's step number (1-based within the leg)
	x, y, z    float32
	px, py, pz float32
	// entries and radius own the spawns this row pulls; idx is the row's absolute index in the route. ownerIdx is
	// the same absolute index when the row pulls, -1 for a move row, which owns nothing: the segment check
	// compares the owner's absolute index against the segment end's idx.
	entries  []uint32
	radius   float32
	idx      int
	ownerIdx int
}

// routePoints builds the rows with planexport (Options{}: the check reads the committed plans as exported). When the
// package cannot import planexport the order is derived from the legs instead: walk the legs' to points and stands
// (see planexport.Builder.leg). Today the import works; the fallback is not needed.
func routePoints(p *dungeon.Plan) ([]routePoint, error) {
	res, err := planexport.Build(p, planexport.Options{})
	if err != nil {
		return nil, err
	}
	var out []routePoint
	for si, s := range res.Rows.Steps {
		switch s.Kind {
		case planexport.KindMove, planexport.KindMoveDirect, planexport.KindMoveHopExact,
			planexport.KindPull, planexport.KindBoss, planexport.KindHold:
		default:
			continue
		}
		rp := routePoint{plan: s.Plan, leg: s.Leg, seq: s.Seq,
			x: s.X, y: s.Y, z: s.Z, px: s.X, py: s.Y, pz: s.Z,
			entries: s.Entries, radius: s.Radius, idx: len(out), ownerIdx: -1}
		if (s.Kind == planexport.KindPull || s.Kind == planexport.KindBoss) && s.Flags&planexport.FlagStandPull != 0 {
			// The party never walks to the row's point (the pack centroid): it fights from its stand. The export
			// writes the stand as the move row right before the pull row in the same leg (see FlagStandPull), so
			// the party point is that move's point; anything else (a plan the export refused) keeps the row point.
			if si > 0 {
				if prev := res.Rows.Steps[si-1]; prev.Plan == s.Plan && prev.Leg == s.Leg &&
					(prev.Kind == planexport.KindMove || prev.Kind == planexport.KindMoveDirect || prev.Kind == planexport.KindMoveHopExact) {
					rp.px, rp.py, rp.pz = prev.X, prev.Y, prev.Z
				}
			}
		}
		out = append(out, rp)
	}
	for i := range out {
		if len(out[i].entries) != 0 {
			out[i].ownerIdx = i
		}
	}
	return out, nil
}

// routeOwnerOf is the row that pulls the spawn: the first pull/boss/hold row (in route order) whose entries hold its
// entry and whose radius reaches it from the row's point. The formation's spawns share the row: ownership expands by
// group_leader (see PackOverlaps' pack rule, tools/dungeongen/spec.go). ok is false when no row pulls it.
func routeOwnerOf(owners []routePoint, byLeader map[uint64][]creature, c creature) (routePoint, bool) {
	var group []creature
	if c.GroupLeader != 0 {
		group = byLeader[c.GroupLeader]
	}
	if len(group) == 0 {
		group = []creature{c}
	}
	for _, o := range owners {
		want := false
		for _, e := range o.entries {
			if e == c.Entry {
				want = true
				break
			}
		}
		if !want {
			continue
		}
		r := float64(o.radius)
		if r == 0 {
			r = defaultPackRadius
		}
		for _, m := range group {
			dx, dy, dz := float64(o.x-m.Pos.X), float64(o.y-m.Pos.Y), float64(o.z-m.Pos.Z)
			if math.Sqrt(dx*dx+dy*dy+dz*dz) <= r {
				return o, true
			}
		}
	}
	return routePoint{}, false
}

// aggroOf is the yards at which the spawn engages: its detection_range, or 20 when the export carries none.
func aggroOf(c creature) float64 {
	if c.DetectionRange > 0 {
		return c.DetectionRange
	}
	return defaultDetectionYd
}

// RouteIntoLaterPacks lists every spawn within aggro of a straight route segment that a later row pulls (or that no
// row pulls). For each pair of consecutive point rows the straight segment between their PARTY points is sampled
// every 2 yd (a stand-pull row's party point is its stand, the move row right before it; every other row's is its
// own point), and every spawn closer (3D) than its detection_range (20 when 0) that is owned by a later row or by no
// row is one finding. Critters, friendly factions and triggers use the same filter as PackOverlaps (reuse it). One
// line per (row pair, guid):
func RouteIntoLaterPacks(p *dungeon.Plan, cs []creature, ignore map[int]bool) []string {
	pts, err := routePoints(p)
	if err != nil {
		return []string{fmt.Sprintf("plan %s: %v", p.Name, err)}
	}
	bossOf := map[uint32]bool{}
	legSkip := map[int]map[uint32]bool{}
	for li, l := range p.Legs {
		if l.Boss != nil {
			bossOf[l.Boss.Entry] = true
			for _, e := range l.Boss.First {
				bossOf[e] = true
			}
		}
		if len(l.Skip) > 0 {
			m := map[uint32]bool{}
			for _, e := range l.Skip {
				m[e] = true
			}
			legSkip[li] = m
		}
	}
	var owners []routePoint
	for _, q := range pts {
		if q.ownerIdx >= 0 {
			owners = append(owners, q)
		}
	}
	byLeader := map[uint64][]creature{}
	for _, c := range cs {
		if c.GroupLeader != 0 {
			byLeader[c.GroupLeader] = append(byLeader[c.GroupLeader], c)
		}
	}
	ownerOf := map[uint64]routePoint{}
	for _, c := range cs {
		if o, ok := routeOwnerOf(owners, byLeader, c); ok {
			ownerOf[c.GUID] = o
		}
	}
	var out []string
	for i := 0; i+1 < len(pts); i++ {
		a, b := pts[i], pts[i+1]
		dx, dy, dz := float64(b.px-a.px), float64(b.py-a.py), float64(b.pz-a.pz)
		n := int(math.Ceil(math.Sqrt(dx*dx+dy*dy+dz*dz) / routeAggroStep))
		if n < 1 {
			n = 1
		}
		for _, c := range cs {
			if c.NPCFlags != 0 || ignore[c.Faction] || c.LevelMax <= 1 {
				continue
			}
			if bossOf[c.Entry] {
				continue
			}
			if legSkip[a.leg][c.Entry] {
				continue
			}
			o, hasOwner := ownerOf[c.GUID]
			if hasOwner && o.ownerIdx <= b.idx {
				continue
			}
			best := math.MaxFloat64
			for k := 0; k <= n; k++ {
				t := float64(k) / float64(n)
				sx, sy, sz := float64(a.px)+t*dx, float64(a.py)+t*dy, float64(a.pz)+t*dz
				ddx, ddy, ddz := float64(c.Pos.X)-sx, float64(c.Pos.Y)-sy, float64(c.Pos.Z)-sz
				if d := math.Sqrt(ddx*ddx + ddy*ddy + ddz*ddz); d < best {
					best = d
				}
			}
			if best >= aggroOf(c) {
				continue
			}
			own := "no row"
			if hasOwner {
				own = fmt.Sprintf("row %d", o.seq)
			}
			out = append(out, fmt.Sprintf("%s leg %d rows %d->%d: passes %.1f yd from %s (guid %d, entry %d) owned by %s",
				p.Name, a.leg, a.seq, b.seq, best, c.Name, c.GUID, c.Entry, own))
		}
	}
	sort.Strings(out)
	return out
}
