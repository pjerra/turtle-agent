package brain

import (
	"context"
	"errors"
	"math"
	"sync"
	"time"

	"github.com/pjerra/turtle-agent/internal/objects"
	"github.com/pjerra/turtle-agent/internal/packet"
)

// Melee fighting (issue #80 step 3c): the part of the brain that every melee class shares. A melee fighter swings (auto attack),
// stands where the mob cannot cleave it, and closes the distance when the mob is out of reach. Behaviour after mod-playerbots'
// SetBehindTargetAction and TankFaceAction (GPL-2.0+; ideas and numbers only, no code).

// Melee is the optional half of the brain's Game that knows how the player's swings went. It is asked by type assertion, like Sight:
// a game without it never sees a dodge (no Overpower) or an avoided attack (no Revenge) and starts its auto attack from its own
// memory. The session's implementation reads the event log (cmd/turtle-agent/brain_melee.go).
type Melee interface {
	// LastDodge is when the unit last dodged an attack of the player's, the zero time if the brain has not seen one.
	LastDodge(target packet.GUID) time.Time
	// LastAvoided is when the player last dodged, parried or blocked an attack, the zero time if never.
	LastAvoided() time.Time
	// Swinging is the unit the player's auto attack is on, 0 when it is not attacking.
	Swinging() packet.GUID
}

func meleeOf(c *Ctx) Melee { m, _ := c.G.(Melee); return m }

// melMem is what the melee strategies remember between ticks.
type melMem struct {
	mu          sync.Mutex
	swingTarget packet.GUID // the unit the last attack order named (a game without Melee)
	swingAt     time.Time
	stanceAt    time.Time // the last stance change the brain made
	faces       map[packet.GUID]faceTry
}

// faceTry is how often, and when last, the tank walked to turn a mob away from the group.
type faceTry struct {
	n  int
	at time.Time
}

const (
	// meleeFar is how far (2D, yards) from the mob a melee fighter lets itself get before it walks in: the abilities reach 5 yards
	// plus the server's slack, so 6 leaves a margin.
	meleeFar = 6.0
	// meleeReach is the 2D distance within which the mob counts as in reach for an attack order.
	meleeReach = 8.0
	// swingRefresh is how often a game without Melee has the attack order repeated (CMSG_ATTACKSWING on a unit already attacked is
	// ignored by the server).
	swingRefresh = 6 * time.Second
	// rearMin is the least angle between the mob's facing and the way to a spot for the spot to count as behind the mob.
	rearMin = 100 * math.Pi / 180
	// tankFacingMax is how close to the mob's facing the group may stand before the tank turns the mob away from it.
	tankFacingMax = 60 * math.Pi / 180
	// tankAwayMin is how far round from the group's side of the mob the tank's spot lies: the mob then faces away from the group.
	tankAwayMin = 110 * math.Pi / 180
	// groupRange limits the party members counted for the tank's idea of where the group is.
	groupRange = 40.0
	// meleeKeepAway keeps a melee spot this far from mobs that have not noticed the party; fighting mobs near the target are not in
	// the way, a melee fighter stands among them.
	meleeKeepAway = 10.0
	// A tank turns a mob away from the group at most faceTries times, faceGap apart: the mob's facing in the update fields may not
	// follow it, and a tank that re-walks every 700 ms never gets to Sunder or Revenge.
	faceTries = 2
	faceGap   = 6 * time.Second
	faceKeep  = time.Minute // a mob not turned for this long is forgotten
)

// faceAllowed says the tank may walk again to turn the mob away from the group.
func faceAllowed(c *Ctx, m *objects.Unit) bool {
	c.mem.mel.mu.Lock()
	defer c.mem.mel.mu.Unlock()
	t, ok := c.mem.mel.faces[m.GUID]
	return !ok || (t.n < faceTries && c.Now.Sub(t.at) >= faceGap)
}

// faceNoted records a walk to turn the mob away.
func faceNoted(c *Ctx, m *objects.Unit) {
	c.mem.mel.mu.Lock()
	defer c.mem.mel.mu.Unlock()
	if c.mem.mel.faces == nil {
		c.mem.mel.faces = map[packet.GUID]faceTry{}
	}
	for g, t := range c.mem.mel.faces {
		if c.Now.Sub(t.at) > faceKeep {
			delete(c.mem.mel.faces, g)
		}
	}
	t := c.mem.mel.faces[m.GUID]
	c.mem.mel.faces[m.GUID] = faceTry{n: t.n + 1, at: c.Now}
}

// canWalk says the brain may walk the player now: the game can walk, the run did not switch movement off and the driver is not
// walking. Unlike canMove it does not ask for the navmesh: a melee fighter walks to its mob along the walker's own route.
func canWalk(c *Ctx) bool {
	if c.P.NoMove || c.lock.Held() {
		return false
	}
	_, ok := c.G.(Mover)
	return ok
}

// ---- values ----

// mobHealthPct is a mob's health in percent of its maximum (100 when unknown). A 1.12 client sees a mob's health as a percentage.
func mobHealthPct(u *objects.Unit) int {
	if u == nil || u.MaxHealth == 0 {
		return 100
	}
	return int(uint64(u.Health) * 100 / uint64(u.MaxHealth))
}

// ValMeleeMobs counts the living hostile mobs in combat within meleeReach of the player.
var ValMeleeMobs = &Value[int]{Name: "melee_mobs", Calc: func(c *Ctx) int {
	n := 0
	for i := range c.Units {
		u := &c.Units[i]
		if hostileMob(u) && u.InCombat && u.Distance >= 0 && float64(u.Distance2D) <= meleeReach {
			n++
		}
	}
	return n
}}

// groupCenter is the middle of the party members in view (alive, online, within groupRange), the player excluded.
func groupCenter(c *Ctx) (Vec3, bool) {
	var sx, sy, sz float64
	n := 0
	for _, m := range c.Party {
		if !m.Online || m.Dead || m.GUID == c.Self.GUID {
			continue
		}
		u, ok := c.Unit(m.GUID)
		if !ok || u.Dead || u.Distance < 0 || float64(u.Distance2D) > groupRange {
			continue
		}
		sx, sy, sz = sx+float64(u.X), sy+float64(u.Y), sz+float64(u.Z)
		n++
	}
	if n == 0 {
		return Vec3{}, false
	}
	return Vec3{float32(sx / float64(n)), float32(sy / float64(n)), float32(sz / float64(n))}, true
}

// ---- positioning ----

// Melee position issues.
const (
	issueFacesGroup = "mob_faces_group"
)

// meleeIssue says what is wrong with where the melee fighter stands for the fight's mob, "" when nothing is. A damage dealer stays
// within reach and out of the mob's front (a mob facing the tank cleaves, breathes and tail-swipes the way it faces); a mob that
// has picked the player is the tank's business. A tanking player stays within reach and turns the mob away from the group.
func meleeIssue(c *Ctx) string {
	m := fightMob(c)
	if m == nil || !m.InCombat || m.Distance < 0 {
		return ""
	}
	d := float64(m.Distance2D)
	if c.P.Role == RoleTank {
		if d > meleeFar {
			return issueTooFar
		}
		if m.Target == c.Self.GUID {
			if g, ok := groupCenter(c); ok && math.Abs(angleDiff(m.O, float32(bearing(unitPos(m), g)))) < tankFacingMax && faceAllowed(c, m) {
				return issueFacesGroup
			}
		}
		return ""
	}
	if d > meleeFar { // a mob on the player that stays away (a caster, an archer) is walked up to like any other
		return issueTooFar
	}
	if ValMobOnMe.Get(c) != nil { // within reach, a mob on the player is the tank's business: no stepping round it
		return ""
	}
	if inFront(m, selfPos(c)) {
		return issueFrontal
	}
	return ""
}

// meleeSpot is where the melee fighter should stand for the mob: behind it (a damage dealer), or on the far side of it from the group
// (a tank), within reach. The spot is searched on rings of a few yards round the mob, over the navmesh when the game has one. A mob
// too far for a search is approached in a straight line.
func meleeSpot(c *Ctx, m *objects.Unit, issue string) (Vec3, bool) {
	kind := "melee"
	if issue == issueFacesGroup {
		kind = "melee_face"
	}
	return memoSpot(c, kind, m.GUID, func() (Vec3, bool) {
		mp, me := unitPos(m), selfPos(c)
		radii := []float64{3.2, 4.0, 4.6}
		base := float64(m.O) + math.Pi // behind the mob
		extra := func(p Vec3) bool { return math.Abs(angleDiff(float32(bearing(mp, p)), m.O)) > rearMin }
		if c.P.Role == RoleTank {
			base = bearing(mp, me) // the side the tank comes from
			extra = func(Vec3) bool { return true }
			if g, ok := groupCenter(c); ok && issue == issueFacesGroup {
				toGroup := bearing(mp, g)
				base = toGroup + math.Pi // across the mob from the group
				radii = []float64{2.6, 3.2, 4.0}
				extra = func(p Vec3) bool { return math.Abs(angleDiff(float32(bearing(mp, p)), float32(toGroup))) > tankAwayMin }
			}
		}
		inner := extra
		extra = func(p Vec3) bool { return inner(p) && !inGroundHazard(c, p, 2) }
		if sightOf(c) != nil {
			sp := spotSpec{kind: kind, center: mp, radii: radii, base: base, target: m, extra: extra, keepAway: meleeKeepAway, busyAway: -1}
			if p, ok := findSpot(c, sp); ok {
				return p, true
			}
		}
		if issue == issueTooFar { // too far for a search (or nothing found): run at the mob
			f := radii[0] / dist2(mp, me)
			return Vec3{mp[0] + (me[0]-mp[0])*float32(f), mp[1] + (me[1]-mp[1])*float32(f), mp[2]}, true
		}
		if sightOf(c) == nil { // no mesh to search: the point on the ring at the wanted bearing
			p := Vec3{mp[0] + float32(radii[0]*math.Cos(base)), mp[1] + float32(radii[0]*math.Sin(base)), mp[2]}
			return p, extra(p)
		}
		return Vec3{}, false
	})
}

// closerPending says a gap closer is about to fire, so the fighter should not walk the distance on foot: the tank has the mob, the
// opening delay is not over, and Charge (out of combat) or Intercept (in combat) is ready for a mob 8 to 25 yards off.
func closerPending(c *Ctx) bool {
	f := ValFight.Get(c)
	if f.Mob == nil || f.Source != "tank" || f.Since >= c.P.OpeningDelay || f.Mob.Distance < 8 || f.Mob.Distance > 25 {
		return false
	}
	return gapCloserReady(c, "Charge", false, stanceBattle) || gapCloserReady(c, "Intercept", true, stanceBerserker)
}

// gapCloserReady reports whether the named spell can be cast now in the stance the player is in: known, ready, paid for, and the
// combat state it needs.
func gapCloserReady(c *Ctx, spell string, inCombat bool, stance string) bool {
	return c.Self.InCombat == inCombat && InStance(&c.Self, stance) && canCast(c, spell)
}

// meleePosition keeps a melee fighter within reach of the mob and out of its front; a tank, on the far side of it from the group.
// All of it is a walk, so it is off while the run switched movement off and while the dungeon driver holds the move lock.
func meleePosition() *Strategy {
	return &Strategy{
		Name: "melee_position",
		Actions: []*Action{
			{Name: "melee_reposition", Settle: 700 * time.Millisecond,
				Possible: func(c *Ctx) bool {
					m := fightMob(c)
					if m == nil || !canWalk(c) || closerPending(c) {
						return false
					}
					_, ok := meleeSpot(c, m, meleeIssue(c))
					return ok
				},
				Useful: func(c *Ctx) bool { return meleeIssue(c) != "" },
				Do: func(ctx context.Context, c *Ctx) error {
					m := fightMob(c)
					if m == nil {
						return nil
					}
					issue := meleeIssue(c)
					p, ok := meleeSpot(c, m, issue)
					if !ok {
						return errors.New("no spot")
					}
					if issue == issueFacesGroup {
						faceNoted(c, m)
					}
					forgetSpot(c)
					return c.walk(ctx, p)
				}},
		},
		Rules: []Rule{
			{Trigger: Trigger{Name: "bad_melee_position", Active: func(c *Ctx) bool { return canWalk(c) && meleeIssue(c) != "" }},
				Do: []Next{{"melee_reposition", RelMove}}},
		},
	}
}
