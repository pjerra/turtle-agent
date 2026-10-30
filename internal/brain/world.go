package brain

import (
	"context"
	"math"
	"sort"
	"sync"
	"time"

	"github.com/pjerra/turtle-agent/internal/game"
	"github.com/pjerra/turtle-agent/internal/objects"
	"github.com/pjerra/turtle-agent/internal/packet"
)

// Vec3 is a WoW position (x, y, z), as navmesh.Vec3.
type Vec3 = [3]float32

// The Game of the brain is the small core every test fake has. What positioning needs is optional, asked of the game by type
// assertion: a game without a Mover cannot walk, one without Sight has no line of sight and no standing spots, one without Ground
// sees no ground effects. The brain then does without that part, and its triggers stay quiet.

// Mover walks the player to a point along the navmesh and returns on arrival (the session's walk_path). Cancelling the
// context stops the walk where it is: that is the brain's "stop".
type Mover interface {
	WalkTo(ctx context.Context, x, y, z float32) error
}

// Sight answers navmesh queries (the session over navmesh.Planner).
type Sight interface {
	// LineClear tells whether the straight line between two points stays on walkable ground of the navmesh: line of sight as
	// far as the agent can tell (it has no vmaps), and a straight step being possible. known is false when the mesh cannot
	// say (no navmesh, no tile, the start off the mesh); then the answer must not be acted on.
	LineClear(a, b Vec3) (clear, known bool)
	// SnapToMesh is the walkable point nearest to p within a few yards.
	SnapToMesh(p Vec3) (Vec3, bool)
}

// Ground lists the ground effects (dynamic objects) in view.
type Ground interface {
	DynamicObjects() []objects.DynamicObject
}

func sightOf(c *Ctx) Sight   { s, _ := c.G.(Sight); return s }
func groundOf(c *Ctx) Ground { g, _ := c.G.(Ground); return g }

// canMove says the brain may walk the player now: the game can walk and see the mesh, the run did not switch movement off
// (Params.NoMove, until the dungeon driver holds the move lock around its walks), and the driver is not walking.
func canMove(c *Ctx) bool {
	if c.P.NoMove || c.lock.Held() {
		return false
	}
	_, ok := c.G.(Mover)
	return ok && sightOf(c) != nil
}

func selfPos(c *Ctx) Vec3 { return Vec3{c.Self.X, c.Self.Y, c.Self.Z} }
func unitPos(u *objects.Unit) Vec3 {
	return Vec3{u.X, u.Y, u.Z}
}

func dist2(a, b Vec3) float64 { return math.Hypot(float64(a[0]-b[0]), float64(a[1]-b[1])) }

// bearing is the direction from a to b, as game.Facing.
func bearing(a, b Vec3) float64 { return float64(game.Facing(a[0], a[1], b[0], b[1])) }

// ---- what the brain remembers for positioning, interrupts and items ----

// memExt is the memory of the parts of the brain that came with step 3b; it has its own lock so that its users never nest
// inside memory.mu.
type memExt struct {
	mu          sync.Mutex
	los         map[packet.GUID]losEntry
	spot        spotMemo
	interrupted map[interruptKey]time.Time
	prevHealth  uint32
	lastDamage  time.Time
	lastOnMe    time.Time // a mob last had the player as its target
	bandage     *pendingBandage
}

type interruptKey struct {
	caster packet.GUID
	spell  uint32
}

type pendingBandage struct {
	entry uint32
	at    time.Time
}

// ---- line of sight ----

// losTTL is how long a line-of-sight answer for a target stands, if neither end has moved more than losMoved. A server refusal
// (markLOSBlocked) stands for losBlockFor, as long as the player has not walked more than losBlockMoved from where it was refused.
const (
	losTTL        = 250 * time.Millisecond
	losMoved      = 1.0
	losBlockFor   = 5 * time.Second
	losBlockMoved = 3.0
)

type losEntry struct {
	at       time.Time
	from, to Vec3
	clear    bool
	known    bool
	// A "line_of_sight" refusal by the server overrides the mesh, which cannot see walls it has no vmaps for: until blockedUntil the
	// target counts as known blocked, while the player stays near blockedFrom and the target near blockedTo.
	blockedUntil time.Time
	blockedFrom  Vec3
	blockedTo    Vec3
}

// blockedNow says the entry is a standing server refusal for these positions.
func (e losEntry) blockedNow(now time.Time, from, to Vec3) bool {
	return now.Before(e.blockedUntil) && dist2(e.blockedFrom, from) < losBlockMoved && dist2(e.blockedTo, to) < losBlockMoved
}

// LineOfSight is whether the player has sight of the unit, by the navmesh ray (see Sight.LineClear), cached per target for
// 250 ms while neither end moves a yard. known false means the brain cannot tell, which callers treat as clear. A target the
// server refused a cast at for line of sight is known blocked for a few seconds, whatever the mesh says (markLOSBlocked).
func (c *Ctx) LineOfSight(u *objects.Unit) (clear, known bool) {
	s := sightOf(c)
	if s == nil || u == nil || !c.SelfOK || u.Distance < 0 {
		return true, false
	}
	from, to := selfPos(c), unitPos(u)
	e := &c.mem.ext
	e.mu.Lock()
	prev, had := e.los[u.GUID]
	if had && prev.blockedNow(c.Now, from, to) {
		e.mu.Unlock()
		return false, true
	}
	if had && c.Now.Sub(prev.at) < losTTL && dist2(prev.from, from) < losMoved && dist2(prev.to, to) < losMoved {
		e.mu.Unlock()
		return prev.clear, prev.known
	}
	e.mu.Unlock()
	clear, known = s.LineClear(from, to)
	e.mu.Lock()
	if e.los == nil {
		e.los = map[packet.GUID]losEntry{}
	}
	ne := losEntry{at: c.Now, from: from, to: to, clear: clear, known: known}
	if old, ok := e.los[u.GUID]; ok && old.blockedNow(c.Now, from, to) {
		ne.blockedUntil, ne.blockedFrom, ne.blockedTo = old.blockedUntil, old.blockedFrom, old.blockedTo
	}
	e.los[u.GUID] = ne
	for g, old := range e.los { // forget targets not asked about for a while
		if c.Now.Sub(old.at) > 10*time.Second && !c.Now.Before(old.blockedUntil) {
			delete(e.los, g)
		}
	}
	e.mu.Unlock()
	return clear, known
}

// markLOSBlocked records that the server refused a cast at the unit for line of sight: it is known blocked for losBlockFor from
// where the player and the unit stand now. A repeated refusal extends it. The remembered standing spot is dropped, so that the
// next search looks again (and, with the block standing, for a place away from here).
func markLOSBlocked(c *Ctx, u *objects.Unit) {
	if u == nil || !c.SelfOK {
		return
	}
	from, to := selfPos(c), unitPos(u)
	e := &c.mem.ext
	e.mu.Lock()
	if e.los == nil {
		e.los = map[packet.GUID]losEntry{}
	}
	le := e.los[u.GUID]
	le.at, le.from, le.to, le.clear, le.known = c.Now, from, to, false, true
	le.blockedUntil, le.blockedFrom, le.blockedTo = c.Now.Add(losBlockFor), from, to
	e.los[u.GUID] = le
	e.spot = spotMemo{}
	e.mu.Unlock()
}

// losBlocked says the server's refusal stands for this unit from where the player is.
func losBlocked(c *Ctx, u *objects.Unit) bool {
	if u == nil || !c.SelfOK {
		return false
	}
	e := &c.mem.ext
	e.mu.Lock()
	defer e.mu.Unlock()
	le, ok := e.los[u.GUID]
	return ok && le.blockedNow(c.Now, selfPos(c), unitPos(u))
}

// ---- standing spots ----

const (
	rangedMin   = 20.0 // the ranged band, yards from the target; Frostbolt reaches 30, so the top leaves room for the server's reach
	rangedMax   = 28.0
	bandSlack   = 1.0 // the band is left only past this much, so a spot on its edge is not left again at once
	maxStep     = 24.0
	spotChecks  = 48
	spotPosTTL  = 600 * time.Millisecond
	spotNegTTL  = 1500 * time.Millisecond
	spotMoved   = 2.0
	mobKeepAway = 15.0 // a spot stays this far from a mob that has not noticed the party
	mobBusyAway = 8.0  // and this far from one that is fighting (other than the target)
)

type spotMemo struct {
	kind   string
	target packet.GUID
	at     time.Time
	from   Vec3
	spot   Vec3
	ok     bool
}

// spotSpec describes a search for a place to stand: candidate points sit on rings round a centre and fan out from a bearing;
// the nearest one that the mesh can reach in a straight line and that meets the rest is taken.
type spotSpec struct {
	kind   string
	center Vec3
	radii  []float64
	base   float64       // bearing of the first candidate
	target *objects.Unit // when set: the spot has sight of it, and stays within the ranged band of it
	extra  func(p Vec3) bool
	// keepAway and busyAway override mobKeepAway and mobBusyAway (0: the default). A negative busyAway lets the spot be anywhere
	// near mobs that are fighting, as a melee fighter's is.
	keepAway, busyAway float64
}

// findSpot searches for a standing spot. It asks the mesh at most spotChecks times.
func findSpot(c *Ctx, sp spotSpec) (Vec3, bool) {
	s := sightOf(c)
	if s == nil || !c.SelfOK {
		return Vec3{}, false
	}
	me := selfPos(c)
	type cand struct {
		p Vec3
		d float64
	}
	var cs []cand
	const step = 20 * math.Pi / 180
	for _, r := range sp.radii {
		for k := 0; k < 19; k++ {
			off := float64((k+1)/2) * step
			if k%2 == 0 {
				off = -off
			}
			a := sp.base + off
			p := Vec3{sp.center[0] + float32(r*math.Cos(a)), sp.center[1] + float32(r*math.Sin(a)), me[2]}
			if d := dist2(p, me); d >= 1.5 && d <= maxStep {
				cs = append(cs, cand{p, d})
			}
		}
	}
	sort.SliceStable(cs, func(i, j int) bool { return cs[i].d < cs[j].d })
	checked := 0
	for _, cd := range cs {
		if checked >= spotChecks {
			break
		}
		if !spotSafeAway(c, cd.p, sp.target, sp.keepAway, sp.busyAway) || (sp.extra != nil && !sp.extra(cd.p)) {
			continue
		}
		checked++
		snap, ok := s.SnapToMesh(cd.p)
		if !ok || dist2(snap, cd.p) > 2.5 {
			continue
		}
		if clear, known := s.LineClear(me, snap); !clear || !known {
			continue
		}
		if sp.target != nil {
			eye := snap
			if clear, known := s.LineClear(eye, unitPos(sp.target)); !clear || !known {
				continue
			}
		}
		return snap, true
	}
	return Vec3{}, false
}

// spotSafeAway keeps a candidate away from mobs that are not the target: from sleeping ones (keep yards; 0: mobKeepAway) so that
// standing there does not pull them, from fighting ones (busy yards; 0: mobBusyAway, below 0: no limit) so that standing there is
// not in their way.
func spotSafeAway(c *Ctx, p Vec3, target *objects.Unit, keep, busy float64) bool {
	if keep <= 0 {
		keep = mobKeepAway
	}
	if busy == 0 {
		busy = mobBusyAway
	}
	for i := range c.Units {
		u := &c.Units[i]
		if !hostileMob(u) || u.Distance < 0 || (target != nil && u.GUID == target.GUID) {
			continue
		}
		d := dist2(p, unitPos(u))
		if (!u.InCombat && d < keep) || (u.InCombat && d < busy) {
			return false
		}
	}
	return true
}

// memoSpot runs a spot search at most every spotPosTTL (spotNegTTL after a miss) for a kind and target, and again at once when
// the player has moved more than spotMoved yards since.
func memoSpot(c *Ctx, kind string, target packet.GUID, find func() (Vec3, bool)) (Vec3, bool) {
	e := &c.mem.ext
	e.mu.Lock()
	m := e.spot
	e.mu.Unlock()
	me := selfPos(c)
	ttl := spotPosTTL
	if !m.ok {
		ttl = spotNegTTL
	}
	if m.kind == kind && m.target == target && c.Now.Sub(m.at) < ttl && dist2(m.from, me) < spotMoved {
		return m.spot, m.ok
	}
	p, ok := find()
	e.mu.Lock()
	e.spot = spotMemo{kind: kind, target: target, at: c.Now, from: me, spot: p, ok: ok}
	e.mu.Unlock()
	return p, ok
}

// forgetSpot drops the remembered spot, after a walk used it.
func forgetSpot(c *Ctx) {
	c.mem.ext.mu.Lock()
	c.mem.ext.spot = spotMemo{}
	c.mem.ext.mu.Unlock()
}

// bandSpot is a standing place in the ranged band of the unit with sight of it, out of its front, near the player.
func bandSpot(c *Ctx, kind string, u *objects.Unit) (Vec3, bool) {
	blocked := losBlocked(c, u)
	return memoSpot(c, kind, u.GUID, func() (Vec3, bool) {
		lo, hi := rangedBand(c)
		mid := (lo + hi) / 2
		sp := spotSpec{kind: kind, center: unitPos(u), radii: []float64{lo + 2, mid, hi - 1}, target: u,
			base: bearing(unitPos(u), selfPos(c)),
			extra: func(p Vec3) bool {
				// Sight the mesh cannot deny but the server did: the spot must be somewhere else than here.
				if blocked && dist2(p, selfPos(c)) < losBlockMoved+1 {
					return false
				}
				return !inFront(u, p) && !inGroundHazard(c, p, 2)
			}}
		return findSpot(c, sp)
	})
}

// frontalHalf is the half angle of the front of a mob that the brain keeps out of: 45 degrees, so 90 in all.
const frontalHalf = math.Pi / 4

// inFront reports whether a point is in the frontal cone of a unit.
func inFront(u *objects.Unit, p Vec3) bool {
	return math.Abs(angleDiff(u.O, float32(bearing(unitPos(u), p)))) < frontalHalf
}
