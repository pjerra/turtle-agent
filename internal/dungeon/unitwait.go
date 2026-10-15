package dungeon

import (
	"slices"

	"github.com/pjerra/turtle-agent/internal/objects"
	"github.com/pjerra/turtle-agent/internal/packet"
)

// The unit waits (UnitWait: wait_for unit, the expect of an area_trigger) judge a list of sightings, whatever the driver they come from.

// unitSight is one creature as a driver sees it: the bots' feed (a hostile, alive or dead) or the agent's own object store.
type unitSight struct {
	guid    packet.GUID
	entry   uint32
	at      Point
	dead    bool
	hostile bool
}

// holds says whether the wait's state is true of the sightings. known is the driver's other evidence that a unit of the entries died (the
// run's kill ledger: the feed lists no corpse for long).
//
//	seen     a living unit of the entries is among them
//	hostile  a living unit of the entries is among them and hostile
//	dead     a unit of the entries is dead (a corpse among them, or known) and none is alive
//	gone     no living unit of the entries is among them
func (u *UnitWait) holds(sights []unitSight, known bool) bool {
	var alive, hostile, dead bool
	for _, s := range sights {
		if !slices.Contains(u.Entries, s.entry) {
			continue
		}
		if u.Near != nil && dist3(s.at, *u.Near) > float64(u.radius()) {
			continue
		}
		if s.dead {
			dead = true
			continue
		}
		alive = true
		hostile = hostile || s.hostile
	}
	switch u.State {
	case UnitSeen:
		return alive
	case UnitHostile:
		return hostile
	case UnitDead:
		return (dead || known) && !alive
	case UnitGone:
		return !alive
	}
	return false
}

// storeSights turns the creatures of an object store into sightings, leaving out the GUIDs already seen (the feed is the fresher record) and
// the units with no position yet.
func storeSights(us []objects.Unit, have map[packet.GUID]bool) []unitSight {
	var out []unitSight
	for _, u := range us {
		if u.Type != "creature" || u.PosAgeMS < 0 || have[u.GUID] {
			continue
		}
		out = append(out, unitSight{guid: u.GUID, entry: u.Entry, at: Point{u.X, u.Y, u.Z}, dead: u.Dead || (u.MaxHealth > 0 && u.Health == 0), hostile: u.Hostile})
	}
	return out
}
