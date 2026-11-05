package worldspawn

import "testing"

// The script ranges the spec pins (§8): Moam 60, Ossirian 45, Ayamiss 40 yd.
// FIRST: the table must carry them, and nothing else.
func TestScriptAggroYd(t *testing.T) {
	for entry, want := range map[uint32]float32{15340: 60, 15339: 45, 15369: 40} {
		got, ok := ScriptAggroYd(entry)
		if !ok || got != want {
			t.Errorf("ScriptAggroYd(%d) = %g, %v, want %g, true", entry, got, ok, want)
		}
	}
	for _, entry := range []uint32{0, 15348, 15370, 15324, 11} {
		if got, ok := ScriptAggroYd(entry); ok {
			t.Errorf("ScriptAggroYd(%d) = %g, true, want false", entry, got)
		}
	}
}

// The template reach wins unless the script wakes farther (Kurinnaxx's 50 yd
// template gives 53 against a level-60 raid, so no script row is needed).
func TestBossAggroYdTakesTheLarger(t *testing.T) {
	moam := Spawn{Entry: 15340, DetectionYd: 20, LevelMax: 63}
	if got := BossAggroYd(moam, 60, 1); got != 60 {
		t.Errorf("BossAggroYd(moam) = %g, want 60", got)
	}
	kux := Spawn{Entry: 15348, DetectionYd: 50, LevelMax: 63}
	if got := BossAggroYd(kux, 60, 1); got != 53 {
		t.Errorf("BossAggroYd(kurinnaxx) = %g, want 53", got)
	}
	plain := Spawn{Entry: 11, DetectionYd: 20, LevelMax: 60}
	if got := BossAggroYd(plain, 60, 1); got != 20 {
		t.Errorf("BossAggroYd(plain) = %g, want 20", got)
	}
	// A NO_AGGRO script boss still never wakes.
	if got := BossAggroYd(Spawn{Entry: 15340, DetectionYd: 20, LevelMax: 63, FlagsExtra: NoAggro}, 60, 1); got != 0 {
		t.Errorf("BossAggroYd(no-aggro moam) = %g, want 0", got)
	}
}
