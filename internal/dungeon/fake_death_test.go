package dungeon

import (
	"context"
	"errors"
	"time"

	"github.com/pjerra/turtle-agent/internal/objects"
)

// fakeDeath scripts the leader's death for fakeGame: a resurrection offer, the release to a graveyard, the corpse query,
// the reclaim and the dungeon portal, as the core answers them (README "Death and the corpse run").
type fakeDeath struct {
	offer       *Offer        // an open resurrection offer
	offerAt     time.Time     // the offer appears at this time (zero: when the test sets offer)
	offerLater  *Offer        // appears at offerAt
	graveyard   Point         // where the release puts the ghost
	graveMap    uint32        // and on which map
	corpse      Corpse        // MSG_CORPSE_QUERY's answer
	reclaimAt   time.Time     // the reclaim delay ends then
	releaseAt   time.Time     // when the spirit was released
	reclaimWait time.Duration // the delay after the release
	portalOK    bool          // the area trigger takes the ghost in and revives it
	portalTo    Point         // where the ghost arrives inside
	noRelease   bool          // the release is ignored

	releases, accepts, reclaims, triggers, queries int
	triggerIDs                                     []uint32
	probeLevel                                     int
	probeKnown                                     bool
	probeErr                                       error
	probes                                         int
	acceptErr                                      error
}

func (g *fakeGame) revive() {
	g.self.Ghost = false
	g.self.Health = g.self.MaxHealth / 2
	g.dead.offer = nil
}

func (g *fakeGame) kill() { g.self.Health, g.self.Ghost = 0, false }

func (g *fakeGame) Death() DeathState {
	d := &g.dead
	if d.offerLater != nil && !g.now.Before(d.offerAt) && g.self.Dead() {
		d.offer, d.offerLater = d.offerLater, nil
	}
	out := DeathState{Dead: g.self.Dead() && !g.self.Ghost, Ghost: g.self.Ghost}
	if g.self.Dead() {
		out.Offer = d.offer
		if left := d.reclaimAt.Sub(g.now); left > 0 && g.self.Ghost {
			out.ReclaimIn = left
		}
	}
	return out
}

func (g *fakeGame) AcceptResurrect(ctx context.Context) error {
	g.dead.accepts++
	if g.dead.acceptErr != nil {
		return g.dead.acceptErr
	}
	if g.dead.offer == nil {
		return errors.New("no resurrection is on offer")
	}
	g.revive()
	return nil
}

func (g *fakeGame) ReleaseSpirit(ctx context.Context) error {
	g.moved("release")
	g.dead.releases++
	if g.dead.noRelease {
		return nil
	}
	if g.self.Ghost || !g.self.Dead() {
		return errors.New("the spirit cannot be released")
	}
	g.self.Ghost, g.self.Health = true, 1
	g.dead.releaseAt = g.now
	g.dead.reclaimAt = g.now.Add(g.dead.reclaimWait)
	g.pos, g.mapID = g.dead.graveyard, g.dead.graveMap
	return nil
}

func (g *fakeGame) CorpseQuery(ctx context.Context) (Corpse, error) {
	g.dead.queries++
	return g.dead.corpse, nil
}

func (g *fakeGame) ReclaimCorpse(ctx context.Context) error {
	g.dead.reclaims++
	if !g.self.Ghost {
		return errors.New("not a ghost")
	}
	if g.now.Before(g.dead.reclaimAt) {
		return errors.New("the reclaim delay is not over")
	}
	if g.mapID != g.dead.corpse.CorpseMap || dist3(g.pos, g.dead.corpse.Pos) > 39 {
		return nil // the server ignores it
	}
	g.revive()
	return nil
}

func (g *fakeGame) AreaTrigger(ctx context.Context, id uint32) error {
	g.moved("portal")
	g.dead.triggers++
	g.dead.triggerIDs = append(g.dead.triggerIDs, id)
	if g.dead.portalOK && g.self.Ghost {
		g.mapID, g.pos = g.dead.corpse.CorpseMap, g.dead.portalTo
		g.revive()
	}
	return nil
}

func (g *fakeGame) ProbeAccount(ctx context.Context) (int, bool, error) {
	g.dead.probes++
	return g.dead.probeLevel, g.dead.probeKnown, g.dead.probeErr
}

func auraNamed(name string, remainingMS *int64) []objects.Aura {
	return []objects.Aura{{SpellID: 15007, Name: name, Harmful: true, RemainingMS: remainingMS}}
}
