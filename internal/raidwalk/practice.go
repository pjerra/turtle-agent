// practice.go: the boss-mechanic practice table of `raid-walk --practice`. A practice run keeps one boss fight alive
// and resets the boss with the test server's GM tools between attempts, so one raid collects many mechanic cycles
// instead of losing the raid after two or three. The timings are the core's own (each cite names the file and line):
//
//   - Zul'Gurub, Hakkar 14834 (scripts/dungeons/zulgurub/boss_hakkar.cpp): Reset() sets BloodSiphon_Timer = 90000
//     (line ~100), so the first Blood Siphon 24324 comes 90 s after the pull and every 90 s after that (the
//     BloodSiphon branch, ~177); Berserk_Timer = 600000 (line ~105) enrages him at 10 min. One attempt therefore holds
//     at most 5-6 siphons, and 10 cycles need two attempts.
//   - His Sons 11357 are static spawns (zulgurub_trash.cpp ~298 npc_fils_hakkar), not summons: a killed Son stays
//     dead until respawned, so a siphon without living Sons is not the real mechanic.
//
// The GM mechanics the reset relies on, and their checks:
//
//   - `.npc evade` (Chat.cpp ~473, Commands.cpp HandleNpcEvadeCommand ~10607): the selected creature leaves combat,
//     walks home and resets (full HP, timers).
//   - `.modify hp <hp> <max>` (Chat.cpp ~362, Commands.cpp HandleModifyHPCommand ~7373): sets max then current
//     health; pass the unit's own max for both, never a smaller max.
//   - `.respawn` (Commands.cpp HandleRespawnCommand ~4907): with a selected dead creature it respawns that one;
//     WITHOUT a selection it respawns every dead creature in range, which would bring back cleared trash. Only the
//     selected form is used.
//   - `.revive <name>` (Commands.cpp HandleReviveCommand ~3024): ResurrectPlayer(1.f), full health.
//
// This file is pure: no clock, no network, no files. The walk's practice loop (cmd/turtle-agent/raid_walk_practice.go)
// owns the timers and the GM calls.
package raidwalk

import (
	"fmt"
	"time"
)

// PracticeMechanic is one boss's mechanic timing, as the practice loop needs it.
type PracticeMechanic struct {
	Entry   uint32 // the boss's creature entry
	Name    string // the boss's name, for the log
	Spell   string // the mechanic's spell name, for the log
	SpellID uint32
	// FirstAtS is the first cast after the pull, in seconds; EverySec the period after it.
	FirstAtS int
	EverySec int
	// EvalAfterS is how long after a cast the cycle's state is read (the GM sustain's decision point).
	EvalAfterS int
	// BerserkS is the boss's enrage timer in seconds (0: none). An attempt ends BerserkS-60 s in, before the enrage.
	BerserkS int
	// CyclesPerAttempt is the most mechanic cycles one attempt holds before the reset (the core's own limit).
	CyclesPerAttempt int
	// RespawnEntries are the static units the mechanic needs alive (Hakkar's Sons): the reset respawns the dead ones
	// of the set the client held at the attempt start.
	RespawnEntries []uint32
}

// IsWindow says the mechanic is an unknown entry in --practice-window mode: one "cycle" is one window of EverySec
// seconds, there is no spell detection and no respawn set.
func (m PracticeMechanic) IsWindow() bool { return m.SpellID == 0 && m.Spell == "" }

// practiceMechanics is the table. Only entries with sourced timings go here; every other entry is window mode.
var practiceMechanics = map[uint32]PracticeMechanic{
	// Hakkar: Blood Siphon 24324 at 90 s then every 90 s, enrage at 600 s, Sons 11357 static.
	14834: {Entry: 14834, Name: "Hakkar", Spell: "Blood Siphon", SpellID: 24324,
		FirstAtS: 90, EverySec: 90, EvalAfterS: 20, BerserkS: 600, CyclesPerAttempt: 5, RespawnEntries: []uint32{11357}},
}

// PracticeMechanics is the table, by entry.
func PracticeMechanics() map[uint32]PracticeMechanic {
	out := make(map[uint32]PracticeMechanic, len(practiceMechanics))
	for k, v := range practiceMechanics {
		out[k] = v
	}
	return out
}

// PracticeMechanicFor is the table's mechanic for entry. ok is false for an unknown entry: the caller uses
// PracticeWindowMechanic then.
func PracticeMechanicFor(entry uint32) (PracticeMechanic, bool) {
	m, ok := practiceMechanics[entry]
	return m, ok
}

// PracticeWindowMechanic is the unknown entry's window mode: one cycle per window, 3 cycles per attempt, no spell
// detection, no enrage, no respawn set. The window also sets the reset's cycle budget and the log's name.
func PracticeWindowMechanic(entry uint32, window time.Duration) PracticeMechanic {
	sec := int(window / time.Second)
	if sec < 1 {
		sec = 1
	}
	return PracticeMechanic{
		Entry: entry, Name: fmt.Sprintf("entry %d", entry), Spell: "", SpellID: 0,
		FirstAtS: sec, EverySec: sec, EvalAfterS: 0, BerserkS: 0, CyclesPerAttempt: 3,
	}
}
