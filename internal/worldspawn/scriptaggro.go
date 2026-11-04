// The script aggro ranges of bosses whose core script wakes farther than their
// creature_template detection_range: Moam 60 yd (boss_moam.cpp:93),
// Ossirian 45 yd (boss_ossirian.cpp:164), Ayamiss 40 yd (boss_ayamiss.cpp:178).
// Kurinnaxx's template already gives 53 yd against a level-60 raid
// (detection_range 50), so he needs no row here.
package worldspawn

// ScriptAggroYd is the core script's aggro range of one boss entry, when it
// wakes farther than its template detection_range does. False for any other
// entry.
func ScriptAggroYd(entry uint32) (float32, bool) {
	switch entry {
	case 15340: // Moam, boss_moam.cpp:93
		return 60, true
	case 15339: // Ossirian the Unscarred, boss_ossirian.cpp:164
		return 45, true
	case 15369: // Ayamiss the Hunter, boss_ayamiss.cpp:178
		return 40, true
	}
	return 0, false
}

// BossAggroYd is the aggro range the lints judge a boss's rows against: the
// larger of AggroRadius and the boss's script range (ScriptAggroYd). A spawn
// that never wakes (NO_AGGRO, no detection range) still never wakes.
func BossAggroYd(s Spawn, partyLevel int, rate float32) float32 {
	if s.FlagsExtra&NoAggro != 0 || s.DetectionYd < 1 {
		return 0
	}
	r := AggroRadius(s, partyLevel, rate)
	if script, ok := ScriptAggroYd(s.Entry); ok && script*rate > r {
		r = script * rate
	}
	return r
}
