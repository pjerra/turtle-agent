package main

import (
	"time"

	"github.com/pjerra/turtle-agent/internal/objects"
	"github.com/pjerra/turtle-agent/internal/packet"
	"github.com/pjerra/turtle-agent/internal/tbm"
)

// boundFrameMaxAge is how old the newest TBM:VIEW frame may be for its party to mark units bound: the module sends one
// every RemoteCommanderViewMs (default 1-2 s) while a binding stands and the view is on.
const boundFrameMaxAge = 60 * time.Second

// unitRow is one `units` answer row: the unit and whether it is a bot of the commander's binding.
//
// A raid bind (`.bot commander bind <Tank> raid`) takes the commander out of the raid, so in_party is false for every
// bound bot from the bind on; `bound` reads the binding from the view feed instead (ZG walk 4, 2026-10-30: a buff
// script that kept the in_party units found none). It needs `.bot commander view on`, and like every row it lists
// only what the agent's own client sees: bots on another map (inside the instance while the commander waits outside)
// are not in `units` at all; `bot_view` lists them from the module's frame.
type unitRow struct {
	objects.Unit
	Bound bool `json:"bound,omitempty"`
}

// boundGUIDs is the party of the newest view frame, or nil when there is none fresh enough.
func boundGUIDs(fr tbm.ViewFrame, ok bool, now time.Time) map[packet.GUID]bool {
	if !ok || len(fr.Party) == 0 || (!fr.At.IsZero() && now.Sub(fr.At) > boundFrameMaxAge) {
		return nil
	}
	out := make(map[packet.GUID]bool, len(fr.Party))
	for _, p := range fr.Party {
		if p.GUID != 0 {
			out[p.GUID] = true
		}
	}
	return out
}

// unitRows marks the bound units; with onlyBound it keeps only those.
func unitRows(us []objects.Unit, bound map[packet.GUID]bool, onlyBound bool) []unitRow {
	out := make([]unitRow, 0, len(us))
	for _, u := range us {
		b := bound[u.GUID]
		if onlyBound && !b {
			continue
		}
		out = append(out, unitRow{Unit: u, Bound: b})
	}
	return out
}
