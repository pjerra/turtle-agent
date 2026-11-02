// RouteIntoLaterPacks reports the route segments that pass within aggro of a pack a later row pulls (see Check and
// PackOverlaps). The run leader walks from row to row; when the straight line between two consecutive rows crosses a
// later pack's aggro, the party pulls it early (Zul'Gurub walk 25: the move to group 12's approach crossed group 15's
// spawns). planexport.Build gives the rows, with the live-export options plandrift uses (DropCompanionsLoot and
// CarrierChests, plus GMCrossDirect where the plan needs it); ownership says which row pulls each spawn (its entry in
// the row's entries, within its radius of the row's point, expanded to the spawn's whole formation by group_leader).
// The reach is the repo's aggro rule: worldspawn.AggroRadius at the plan's party level (level_band[0]), plus the
// party's spread (10 yd for a raid, 3 else, as trash-check) and the spawn's wander. One line per (row pair, guid).
// Sampling every 2 yd on the straight segment is the approximation: the run leader's own navmesh hops are out of scope
// (known limit L1), as are patrol loops, which stand where the export put them (known limit L2). So is the
// firing-stand walk of a plain pull row: its party point is the row's own point, not the firing stand the run leader
// walks out to (a stand-pull row instead carries its stand, and the check walks from there).
package main

import (
	"fmt"
	"math"
	"sort"

	"github.com/pjerra/turtle-agent/internal/aggroest"
	"github.com/pjerra/turtle-agent/internal/dungeon"
	"github.com/pjerra/turtle-agent/internal/planexport"
	"github.com/pjerra/turtle-agent/internal/worldspawn"
)

const (
	// campShotYd is how far the tag spot may stand from the nearest unit of
	// its own pack: the tank's shot (28.5 yd) less the 5 yd margin the run
	// leader wants (spec 3.7).
	campShotYd = 23.5
	// defaultDetectionYd is the yards at which a spawn engages when the export carries no detection_range (draft exports
	// predate the column).
	defaultDetectionYd = 20
	// routeAggroStep is how finely a row pair's segment is sampled (yards).
	routeAggroStep = 2
	// routeAggroRate is the aggro rate scale (the core's Rate.Creature.Aggro; trash-check's --aggro-rate, default 1).
	routeAggroRate = 1
)

// routeAggroBuild carries the options the live rows need: DropCompanionsLoot plus CarrierChests (Zul'Farrak's zumrah
// leg has companions_loot false and no chest stand-free form, so the plan is refused without them), plus GMCrossDirect
// for the plan whose gap the run leader walks straight (upper Karazhan's outland_landing gm_cross via), plus CampPull
// (a camp group's rows are the camp chain: the party fights at the camp, the tank walks the tag rows alone).
func routeAggroBuild(p *dungeon.Plan) (planexport.Result, error) {
	return planexport.Build(p, planexport.Options{DropCompanionsLoot: true, CarrierChests: true, GMCrossDirect: true, CampPull: true})
}

// routePoint is one row that puts the party somewhere (a move, or the fight point of a pull or boss row). Each row
// carries two positions: the OWNERSHIP point (x, y, z: the row's own point, used to own spawns in routeOwnerOf) and
// the PARTY point (px, py, pz: where the party stands for that row, used to draw the walked segments). A stand-pull
// pull or boss row (FlagStandPull) is fought from its stand, the move row right before it in the same leg, so its
// party point is the stand's point while its ownership point stays the row's point. A plain pull row keeps the row's
// point as its party point.
type routePoint struct {
	plan       string
	leg        int
	seq        int // the export's step number (0-based within the leg)
	kind       string
	standPull  bool
	campPull   bool
	camp       bool // the camp move row itself (FlagCampPoint): the party walks there, the tank walks on alone
	x, y, z    float32
	px, py, pz float32
	// entries and radius own the spawns this row pulls; idx is the row's absolute index in the route. ownerIdx is
	// the same absolute index when the row pulls, -1 for a move row, which owns nothing: the segment check
	// compares the owner's absolute index against the segment end's idx. item is the row's item column (a pull
	// row's max_bodies, 0 when the plan sizes nothing).
	entries  []uint32
	radius   float32
	item     uint32
	idx      int
	ownerIdx int
}

// routePoints builds the rows with planexport (routeAggroBuild: the live-export options), falling back down the
// option ladder when the plan refuses an option it does not need (a plan with no companions_loot-false leg builds
// without CarrierChests).
func routePoints(p *dungeon.Plan) ([]routePoint, error) {
	res, err := routeAggroBuild(p)
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
		rp := routePoint{plan: s.Plan, leg: s.Leg, seq: s.Seq, kind: s.Kind,
			standPull: (s.Kind == planexport.KindPull || s.Kind == planexport.KindBoss) && s.Flags&planexport.FlagStandPull != 0,
			campPull:  s.Kind == planexport.KindPull && s.Flags&planexport.FlagCampPull != 0,
			camp:      s.Kind == planexport.KindMove && s.Flags&planexport.FlagCampPoint != 0,
			x:         s.X, y: s.Y, z: s.Z, px: s.X, py: s.Y, pz: s.Z,
			entries: s.Entries, radius: s.Radius, item: s.Item, idx: len(out), ownerIdx: -1}
		if rp.standPull {
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
		if rp.campPull {
			// A camp pull is fought at its camp (the FlagCampPoint move row of the same leg): the party point is the
			// camp's point, while the ownership point stays the row's point (the pack centre).
			for j := si - 1; j >= 0; j-- {
				if prev := res.Rows.Steps[j]; prev.Plan == s.Plan && prev.Leg == s.Leg &&
					prev.Kind == planexport.KindMove && prev.Flags&planexport.FlagCampPoint != 0 {
					rp.px, rp.py, rp.pz = prev.X, prev.Y, prev.Z
					break
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

// routePartyLevel is what the trash check uses for the plan: the party's lowest level (level_band[0]), the widest
// aggro radius, as the run leader assumes. 0 when the plan gives no band (wailing_caverns-extra).
func routePartyLevel(p *dungeon.Plan) int {
	if len(p.LevelBand) == 2 {
		return p.LevelBand[0]
	}
	return 0
}

// routePartySpread is the party's spread from the route: 10 yd for a raid, 3 else (trash-check's default).
func routePartySpread(p *dungeon.Plan) float64 {
	if p.IsRaid() {
		return 10
	}
	return 3
}

// routeReach is a spawn's proximity wake reach: the same rule the trash check uses (worldspawn.AggroRadius, built on
// detection_range with the party-minus-creature level difference, NO_AGGRO and the spread), plus the spawn's wander.
// A spawn the export carries no range for wakes at the 20 yd default; a NO_AGGRO spawn never wakes.
func routeReach(p *dungeon.Plan, c creature) float64 {
	det := c.DetectionRange
	if det <= 0 {
		det = defaultDetectionYd
	}
	s := worldspawn.Spawn{LevelMax: c.LevelMax, DetectionYd: float32(det), FlagsExtra: c.FlagsExtra, Wander: c.Wander}
	return float64(worldspawn.AggroRadius(s, routePartyLevel(p), routeAggroRate)) + routePartySpread(p) + float64(c.Wander)
}

// RouteIntoLaterPacks lists every spawn within aggro of a straight route segment that a later row pulls (or that no
// row pulls). For each pair of consecutive point rows the straight segment between their PARTY points is sampled
// every 2 yd (a stand-pull row's party point is its stand, the move row right before it; every other row's is its
// own point). A zero-length stand->stand segment (a leftover stand-pull pair) is not walked and skipped. A plain
// pull's own approach is not a later pack: a segment ending at a move row whose next row's point owns the spawn is
// skipped (the party is walking out to pull it there), unless the next row is a stand pull (its stand inside the
// pack's aggro is the defect). Every spawn closer (3D) than its reach (AggroRadius at the plan's party level, plus
// spread and wander) that is still owned by a later row or by no row is one finding. NO_AGGRO spawns, critters,
// friendly factions and triggers use the same filter as PackOverlaps (reuse it). One line per (row pair, guid):
func RouteIntoLaterPacks(p *dungeon.Plan, cs []creature, ignore map[int]bool) ([]string, error) {
	pts, err := routePoints(p)
	if err != nil {
		// A refused export is not a route finding: the caller reports it as the test's own error.
		return nil, err
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
	// legSkip is keyed by the plan leg index, but the route carries export legs: optional (or skipped) legs before it
	// take no export number, so the legs after close up. Map each export leg back to its plan leg.
	planOfExport := map[int]int{}
	for li := range p.Legs {
		if n, ok := planexport.LegNumber(p, li); ok {
			planOfExport[n] = li
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
		// A zero-length segment is not walked: a leftover stand->stand pair after the party-point fix.
		if a.px == b.px && a.py == b.py && a.pz == b.pz {
			continue
		}
		dx, dy, dz := float64(b.px-a.px), float64(b.py-a.py), float64(b.pz-a.pz)
		n := int(math.Ceil(math.Sqrt(dx*dx+dy*dy+dz*dz) / routeAggroStep))
		if n < 1 {
			n = 1
		}
		for _, c := range cs {
			if c.NPCFlags != 0 || ignore[c.Faction] || c.LevelMax <= 1 {
				continue
			}
			if c.FlagsExtra&worldspawn.NoAggro != 0 {
				// A NO_AGGRO spawn never wakes by proximity: skipped outright (its AggroRadius is 0, but
				// the spread and wander added on top would otherwise still report it).
				continue
			}
			if pli, ok := planOfExport[a.leg]; ok {
				if legSkip[pli][c.Entry] {
					continue
				}
			}
			o, hasOwner := ownerOf[c.GUID]
			if hasOwner && o.ownerIdx <= b.idx {
				continue
			}
			// A plain pull's own approach is not a later pack: the segment ends at a move row and the next row's
			// point owns the spawn. Still reported when that next row is a stand pull: its stand would sit inside
			// the pack's aggro, which is the defect.
			if i+2 < len(pts) && b.ownerIdx < 0 && b.kind == planexport.KindMove &&
				a.leg == b.leg && b.leg == pts[i+2].leg && !pts[i+2].standPull {
				if next, ok := ownerOf[c.GUID]; ok && next.idx == pts[i+2].idx {
					continue
				}
			}
			reach := routeReach(p, c)
			if reach <= 0 {
				// A NO_AGGRO spawn (or one with no detection range) never wakes: AggroRadius is 0 for it.
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
			if best >= reach {
				continue
			}
			own := "no row"
			if hasOwner {
				own = fmt.Sprintf("leg %d row %d", o.leg, o.seq)
			}
			endLeg := b.leg
			out = append(out, fmt.Sprintf("%s leg %d rows %d->%d (end leg %d): passes %.1f yd from %s (guid %d, entry %d) owned by %s",
				p.Name, a.leg, a.seq, b.seq, endLeg, best, c.Name, c.GUID, c.Entry, own))
		}
	}
	sort.Strings(out)
	return out, nil
}

// campChain returns the tag rows (the tank's walk-out, last = tag spot) and
// the camp of the camp-pull chain the pull row at pts[i] ends. Empty when the
// chain cannot be read (a plan the export refused): the caller then keeps the
// plain geometry.
func campChain(pts []routePoint, i int) (tag []dungeon.Point, camp dungeon.Point) {
	leg := pts[i].leg
	for j := i - 1; j >= 0 && pts[j].leg == leg; j-- {
		q := pts[j]
		if q.kind != planexport.KindMove {
			break
		}
		if q.camp {
			return tag, dungeon.Point{X: q.px, Y: q.py, Z: q.pz}
		}
		tag = append([]dungeon.Point{{X: q.px, Y: q.py, Z: q.pz}}, tag...)
	}
	return nil, dungeon.Point{}
}

// campTankReach is a spawn's wake reach for the tank-only walk: the repo's
// aggro rule with the tank spread (1.5 yd) instead of the party's, plus the
// spawn's wander. No NO_AGGRO spawn wakes.
func campTankReach(p *dungeon.Plan, c creature) float64 {
	det := c.DetectionRange
	if det <= 0 {
		det = defaultDetectionYd
	}
	s := worldspawn.Spawn{LevelMax: c.LevelMax, DetectionYd: float32(det), FlagsExtra: c.FlagsExtra, Wander: c.Wander}
	return float64(worldspawn.AggroRadius(s, routePartyLevel(p), routeAggroRate)) + aggroest.TankSpreadYd + float64(c.Wander)
}

// CampPullFindings lints every camp chain of the plan's export rows (spec
// 3.7): the tag spot must stand outside every own-pack unit's aggro reach
// (no body pull of the tagged pack) and within shot (campShotYd) of the
// nearest one; the tank's walk-out (the tag rows) must stand clear of every
// spawn the pull does not own; the camp must stand on the cleared route (an
// earlier row's party point, or within 10 yd of the polyline between two
// earlier rows). One line per violation; a refused export is the caller's
// error, never a finding.
func CampPullFindings(p *dungeon.Plan, cs []creature, ignore map[int]bool) ([]string, error) {
	pts, err := routePoints(p)
	if err != nil {
		return nil, err
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
	eligible := func(c creature) bool {
		if c.NPCFlags != 0 || ignore[c.Faction] || c.LevelMax <= 1 {
			return false
		}
		return c.FlagsExtra&worldspawn.NoAggro == 0
	}
	var out []string
	pullNum := map[int]int{}
	for i, rp := range pts {
		if rp.kind != planexport.KindPull {
			continue
		}
		pullNum[rp.leg]++
		if !rp.campPull {
			continue
		}
		n := pullNum[rp.leg]
		tag, camp := campChain(pts, i)
		if len(tag) == 0 {
			continue
		}
		spot := tag[len(tag)-1]
		nearest := math.MaxFloat64
		for _, c := range cs {
			if !eligible(c) {
				continue
			}
			o, ok := ownerOf[c.GUID]
			if !ok || o.idx != rp.idx {
				continue
			}
			d := dist(c.Pos, spot)
			if d < nearest {
				nearest = d
			}
			if reach := campTankReach(p, c); d < reach+1 {
				out = append(out, fmt.Sprintf("%s leg %d pull %d: tag spot %.1f yd from %s (guid %d, entry %d, reach %.1f): it body-pulls its own pack",
					p.Name, rp.leg, n, d, c.Name, c.GUID, c.Entry, reach))
			}
		}
		if nearest == math.MaxFloat64 {
			out = append(out, fmt.Sprintf("%s leg %d pull %d: tag spot owns no spawn of its pull", p.Name, rp.leg, n))
		} else if nearest > campShotYd {
			out = append(out, fmt.Sprintf("%s leg %d pull %d: tag spot %.1f yd from its nearest pack unit (over the %g yd shot)", p.Name, rp.leg, n, nearest, campShotYd))
		}
		for _, c := range cs {
			if !eligible(c) {
				continue
			}
			if o, ok := ownerOf[c.GUID]; ok && o.idx == rp.idx {
				continue
			}
			best := math.MaxFloat64
			for k := range tag {
				a := camp
				if k > 0 {
					a = tag[k-1]
				} else {
					// The walk-out starts at the camp: the first tag row is walked from there.
					a = camp
				}
				b := tag[k]
				dx, dy, dz := float64(b.X-a.X), float64(b.Y-a.Y), float64(b.Z-a.Z)
				seg := int(math.Ceil(math.Sqrt(dx*dx+dy*dy+dz*dz) / routeAggroStep))
				if seg < 1 {
					seg = 1
				}
				for s := 0; s <= seg; s++ {
					t := float64(s) / float64(seg)
					sx, sy, sz := float64(a.X)+t*dx, float64(a.Y)+t*dy, float64(a.Z)+t*dz
					ddx, ddy, ddz := float64(c.Pos.X)-sx, float64(c.Pos.Y)-sy, float64(c.Pos.Z)-sz
					if d := math.Sqrt(ddx*ddx + ddy*ddy + ddz*ddz); d < best {
						best = d
					}
				}
			}
			if reach := campTankReach(p, c); best < reach {
				out = append(out, fmt.Sprintf("%s leg %d pull %d: tank walk passes %.1f yd from %s (guid %d, entry %d, reach %.1f): it wakes another pack",
					p.Name, rp.leg, n, best, c.Name, c.GUID, c.Entry, reach))
			}
		}
		cleared := false
		campIdx := -1
		for j := 0; j < i; j++ {
			if pts[j].camp && pts[j].leg == rp.leg {
				campIdx = j
				break
			}
		}
		// The camp's own row, its incoming walk and its tag rows are not
		// the cleared route: the camp must lie on ground the route walked
		// before the chain. Row points before the chain count; segments
		// count only between two such rows.
		for j := 0; j < campIdx && !cleared; j++ {
			q := pts[j]
			switch q.kind {
			case planexport.KindMove, planexport.KindMoveDirect, planexport.KindMoveHopExact,
				planexport.KindPull, planexport.KindBoss, planexport.KindHold:
			default:
				continue
			}
			if dist(camp, dungeon.Point{X: q.px, Y: q.py, Z: q.pz}) <= 10 {
				cleared = true
				break
			}
			if j+1 < campIdx {
				r := pts[j+1]
				dx, dy, dz := float64(r.px-q.px), float64(r.py-q.py), float64(r.pz-q.pz)
				seg := int(math.Ceil(math.Sqrt(dx*dx+dy*dy+dz*dz) / routeAggroStep))
				if seg < 1 {
					seg = 1
				}
				for s := 0; s <= seg; s++ {
					t := float64(s) / float64(seg)
					sx, sy, sz := float64(q.px)+t*dx, float64(q.py)+t*dy, float64(q.pz)+t*dz
					ddx, ddy, ddz := float64(camp.X)-sx, float64(camp.Y)-sy, float64(camp.Z)-sz
					if math.Sqrt(ddx*ddx+ddy*ddy+ddz*ddz) <= 10 {
						cleared = true
						break
					}
				}
			}
		}
		if !cleared {
			out = append(out, fmt.Sprintf("%s leg %d pull %d: camp is not on the cleared route (over 10 yd from every earlier row)", p.Name, rp.leg, n))
		}
	}
	sort.Strings(out)
	return out, nil
}
