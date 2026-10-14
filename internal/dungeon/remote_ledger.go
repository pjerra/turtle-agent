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
// or when it is absent from two whole frames in a row (never counting a frame the module cut short) after being seen fighting and
// not walking home. A mob seen alive again is alive again. The ledger answers "is this boss dead" (bossDead). It does not clear
// positions: the per-position evidence stays killedAt, which only a won pull fills (#115).

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
	for g, m := range d.ledger {
		if seen[g] || m.dead {
			continue
		}
		if m.absent++; m.absent >= 2 && !m.last.Evading() {
			m.dead = true
		}
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
