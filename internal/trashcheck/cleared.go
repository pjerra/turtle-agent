package trashcheck

import (
	"github.com/pjerra/turtle-agent/internal/dungeon"
	"github.com/pjerra/turtle-agent/internal/planexport"
	"github.com/pjerra/turtle-agent/internal/worldspawn"
)

// ClearedPacks are the packs a pull, boss or hold row clears: the same rule Check uses to say which row clears a pack
// (model.targets: the row's entries hold one of the pack's member entries and the row stands within its reach of that member's
// spawn), over the same pack model (worldspawn Packs: creature_groups with AGGRO_TOGETHER and both directions of every aggro
// link). A pack is returned whole, so a linked unit that stands away from the row is in it. Only the spawn positions count: a
// patrol's loop is not read here. The packs come in guid order of their smallest member.
func ClearedPacks(w *worldspawn.World, row planexport.StepRow) []worldspawn.Pack {
	if w == nil {
		return nil
	}
	s := step{
		kind:       row.Kind,
		p:          dungeon.Point{X: row.X, Y: row.Y, Z: row.Z},
		positioned: positioned(row.X, row.Y, row.Z),
		radius:     row.Radius,
		entry:      row.Entry,
		entries:    row.Entries,
	}
	if !s.positioned {
		return nil
	}
	es := targetEntries(s)
	if len(es) == 0 {
		return nil
	}
	m := newModel(w.Spawns, w, Options{})
	reach := targetReach(s)
	var out []worldspawn.Pack
	for i := range m.packs {
		if m.packClearedBy(i, es, s.p, reach) {
			out = append(out, m.packs[i])
		}
	}
	return out
}
