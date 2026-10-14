package dungeon

import (
	"slices"

	"github.com/pjerra/turtle-agent/internal/packet"
	"github.com/pjerra/turtle-agent/internal/tbm"
)

// The kill ledger: a run-wide record of every hostile the party fought, whichever pull it came to. A pull's fight tracks the pulled
// mob and what joins it from the step's own entries; a mob of another group (or the boss) that joins a fight and dies there was not
// tracked, so its death left no trace and the boss step later found "no living [..] seen" for a boss the party had killed (remote
// Uldaman run 1, Revelosh dying inside a 4852 pull that ended "evaded").
//
// A mob enters the ledger when the feed shows it fighting the party (engaged, or hurt). It is dead when the feed lists it at 0 health,
// or when it is absent from two whole frames in a row (never counting a frame the module cut short) after being seen fighting, nearly
// dead (at most ledgerDyingPct), with a living bot near where it stood (ledgerSeeRadius), and not walking home. A wipe clears the counts.
// A mob seen alive again is alive again. The ledger answers "is this boss dead" (bossDead). It does not clear positions: the per-position evidence stays killedAt, which only a won pull fills (#115).

// ledgerMob is one hostile the ledger follows.
type ledgerMob struct {
	last   tbm.HostileRec // as the feed last showed it: its entry and where it was
	absent int            // whole frames in a row without it
	dead   bool
}

// noteLedger folds a new frame into the ledger. It runs once per new frame (look).
func (d *RemoteDriver) noteLedger() {
	if d.ledger == nil {
		d.ledger = map[packet.GUID]*ledgerMob{}
	}
	seen := make(map[packet.GUID]bool, len(d.fr.Hostiles))
	for _, h := range d.fr.Hostiles {
		if h.Entry == 0 {
			continue
		}
		seen[h.GUID] = true
		if h.HPPct > 0 {
			if d.seenHostile == nil {
				d.seenHostile = map[packet.GUID]uint32{}
			}
			d.seenHostile[h.GUID] = h.Entry
		}
		m := d.ledger[h.GUID]
		fighting := h.HPPct > 0 && (d.engagedWith(h) || h.HPPct < 100)
		if m == nil {
			if !fighting && h.HPPct > 0 {
				continue // not in the fight (yet): nothing to follow
			}
			if h.HPPct <= 0 {
				continue // a corpse the ledger never saw fight: not its kill
			}
			m = &ledgerMob{}
			d.ledger[h.GUID] = m
		}
		m.last, m.absent = h, 0
		m.dead = h.HPPct <= 0
	}
	if d.fr.Truncated {
		return // records were dropped to fit: an absence says nothing
	}
	if d.fr.AllDead() {
		d.ledgerClearAbsences() // a wipe: nobody was there to see anything
		return
	}
	for g, m := range d.ledger {
		if seen[g] || m.dead {
			continue
		}
		// An absence counts only where the bots would have seen the mob: a living bot stands near its last position. A bot chased out
		// of range, or a party that fled, says nothing about a boss it left alive.
		if !d.botNear(hostilePoint(m.last), ledgerSeeRadius) {
			m.absent = 0
			continue
		}
		// ... and only a mob that was nearly dead when it went: a healthy one that leaves view has not died.
		if m.absent++; m.absent >= 2 && d.goneMeansDead(m.last) {
			m.dead = true
		}
	}
}

const (
	ledgerSeeRadius = 25 // yards: a living bot this near a mob's last position would have seen it die
	ledgerDyingPct  = 20 // the health a mob had when last seen, at most, to be counted dead for being gone from the feed
)

// goneMeansDead says whether a mob last seen as h, now out of the feed for two whole frames, is taken as dead: nearly dead, not walking
// home, with a living bot near where it stood.
func (d *RemoteDriver) goneMeansDead(h tbm.HostileRec) bool {
	return h.HPPct <= ledgerDyingPct && !h.Evading() && d.botNear(hostilePoint(h), ledgerSeeRadius)
}

// botNear says whether a living bot of the newest frame stands within r of p.
func (d *RemoteDriver) botNear(p Point, r float64) bool {
	for _, m := range d.fr.Party {
		if !m.Dead && dist3(partyPoint(m), p) <= r {
			return true
		}
	}
	return false
}

// ledgerClearAbsences forgets the absences counted so far (a wipe).
func (d *RemoteDriver) ledgerClearAbsences() {
	for _, m := range d.ledger {
		m.absent = 0
	}
}

// ledgerDead says whether a hostile of one of the entries is on the ledger as dead.
func (d *RemoteDriver) ledgerDead(entries []uint32) bool {
	for _, m := range d.ledger {
		if m.dead && slices.Contains(entries, m.last.Entry) {
			return true
		}
	}
	return false
}

// hostileSeen says whether the party has met a unit of one of entries as a hostile: one is listed alive in the feed now, or a unit
// (by GUID: an entry may have several spawns) was listed alive earlier in the run and is not known dead since (killed in a won pull, or
// dead in the kill ledger). The feed lists only what is hostile to the party, so this is the data behind the team gate (HostileWorld):
// a part gated to the other team that shows up here is fought. A mob that was never listed (friendly, or never in sight) is not "seen
// hostile"; the driver looks for it before the gate skips the part (gatedLook).
func (d *RemoteDriver) hostileSeen(entries []uint32) bool {
	for _, h := range d.fr.Hostiles {
		if h.HPPct > 0 && h.Entry != 0 && slices.Contains(entries, h.Entry) {
			return true
		}
	}
	for g, e := range d.seenHostile {
		if !slices.Contains(entries, e) || d.killedGUID[g] {
			continue
		}
		if m := d.ledger[g]; m != nil && m.dead {
			continue
		}
		return true
	}
	return false
}
