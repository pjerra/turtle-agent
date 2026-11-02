package raidwalk

// Joiners from frames: after a pull starts, the hostile units in combat with
// the party that are NOT in the pulled pack, within the first seconds.
//
// The server's own pull note carries the aggro estimate ("N joiners expected"
// or "joiners not read"; TortoiseBots origin/dev commands/Commander.cpp, the
// estimate read by ReadPullJoiners). When the note says "not read" the walk
// still sees what joined: the frames show every hostile with a party target.
// This counts that, so the pull note names the actual joiners instead of
// leaving the estimate's gap open.

import (
	"sort"
	"time"

	"github.com/pjerra/turtle-agent/internal/packet"
	"github.com/pjerra/turtle-agent/internal/tbm"
)

// JoinWindow is how long after the pull starts a hostile may join and still count.
const JoinWindow = 10 * time.Second

// JoinersOfPull counts the joiners of one pull from a frame stream: hostile,
// alive, in combat with the party (a party target, or threat on one — the same
// rule as ViewFrame.EngagedHostiles) within window of the pull start, that are
// not in the pulled pack. pack holds the pulled pack's guids (spawn/guid
// ownership from the plan). The count stops at the first frame past the window;
// later arrivals are the fight's own adds, not joiners of the pull.
func JoinersOfPull(pullAt time.Time, pack map[packet.GUID]bool, frames []tbm.ViewFrame, window time.Duration) []packet.GUID {
	if window <= 0 {
		window = JoinWindow
	}
	seen := map[packet.GUID]bool{}
	var out []packet.GUID
	for _, f := range frames {
		if f.At.Before(pullAt) || f.At.Sub(pullAt) > window {
			continue
		}
		for _, h := range f.EngagedHostiles() {
			if pack[h.GUID] || seen[h.GUID] {
				continue
			}
			seen[h.GUID] = true
			out = append(out, h.GUID)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i] < out[j] })
	return out
}

// JoinersNote formats the pull note tail: "joiners N (guids...)", or "" when none joined.
func JoinersNote(guids []packet.GUID) string {
	if len(guids) == 0 {
		return ""
	}
	s := "joiners " + itoa(len(guids)) + " ("
	for i, g := range guids {
		if i > 0 {
			s += " "
		}
		s += g.String()
	}
	return s + ")"
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	var b [20]byte
	i := len(b)
	for n > 0 {
		i--
		b[i] = byte('0' + n%10)
		n /= 10
	}
	return string(b[i:])
}
