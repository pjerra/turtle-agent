package raidwalk

import (
	"testing"
	"time"

	"github.com/pjerra/turtle-agent/internal/packet"
	"github.com/pjerra/turtle-agent/internal/tbm"
)

// frameAt builds one frame at t with the given hostiles; party holds two bots
// (guids 1000, 1001) so a hostile targeting either counts as engaged.
func frameAt(t time.Time, hs ...tbm.HostileRec) tbm.ViewFrame {
	p := []tbm.PartyRec{{GUID: 1000, Name: "Tank"}, {GUID: 1001, Name: "Healer"}}
	f := tbm.ViewFrame{At: t, Party: p, Hostiles: hs}
	return f
}

func hostile(g packet.GUID, target packet.GUID) tbm.HostileRec {
	return tbm.HostileRec{GUID: g, Entry: 11368, HPPct: 100, Target: target}
}

// A pull of pack A where 3 units of pack B join: the note says "joiners 3".
func TestJoinersOfPullCountsPackB(t *testing.T) {
	t0 := time.Date(2026, 11, 2, 1, 0, 0, 0, time.UTC)
	pack := map[packet.GUID]bool{0xF130001: true, 0xF130002: true} // the pulled pack A
	frames := []tbm.ViewFrame{
		frameAt(t0, hostile(0xF130001, 1000)),                    // the pulled unit, in combat
		frameAt(t0.Add(2*time.Second), hostile(0xF130001, 1000)), // still only A
		frameAt(t0.Add(4*time.Second), hostile(0xF130001, 1000), hostile(0xF130101, 1000), hostile(0xF130102, 1001)),
		frameAt(t0.Add(8*time.Second), hostile(0xF130101, 1000), hostile(0xF130102, 1001), hostile(0xF130103, 1000)),
		frameAt(t0.Add(12*time.Second), hostile(0xF130104, 1000)), // past the window: not a joiner
	}
	got := JoinersOfPull(t0, pack, frames, JoinWindow)
	if len(got) != 3 {
		t.Fatalf("joiners %v, want 3", got)
	}
	if got[0] != 0xF130101 || got[1] != 0xF130102 || got[2] != 0xF130103 {
		t.Fatalf("joiner guids %v", got)
	}
	if note := JoinersNote(got); note != "joiners 3 (0x000000000F130101 0x000000000F130102 0x000000000F130103)" {
		t.Fatalf("note %q", note)
	}
}

// The pulled pack itself never counts, nor do bystanders, corpses or untargeted rows.
func TestJoinersOfPullIgnoresPackAndBystanders(t *testing.T) {
	t0 := time.Date(2026, 11, 2, 1, 0, 0, 0, time.UTC)
	pack := map[packet.GUID]bool{0xF130001: true}
	frames := []tbm.ViewFrame{
		frameAt(t0,
			hostile(0xF130001, 1000),                               // pulled: not a joiner
			tbm.HostileRec{GUID: 0xF130201, HPPct: 0},              // corpse
			tbm.HostileRec{GUID: 0xF130202, HPPct: 100},            // in view, no target
			tbm.HostileRec{GUID: 0xF130203, HPPct: 100, Kind: "k"}, // critter
		),
	}
	if got := JoinersOfPull(t0, pack, frames, JoinWindow); len(got) != 0 {
		t.Fatalf("joiners %v, want none", got)
	}
	if JoinersNote(nil) != "" {
		t.Fatalf("empty joiners must give no note")
	}
}

// A hostile already fighting when the pull starts is the old fight, not a joiner of this pull.
func TestJoinersOfPullIgnoresPrefight(t *testing.T) {
	t0 := time.Date(2026, 11, 2, 1, 0, 0, 0, time.UTC)
	pack := map[packet.GUID]bool{0xF130001: true}
	old := frameAt(t0.Add(-time.Minute), hostile(0xF130301, 1000))
	if got := JoinersOfPull(t0, pack, []tbm.ViewFrame{old}, JoinWindow); len(got) != 0 {
		t.Fatalf("joiners %v, want none (before the pull)", got)
	}
}
