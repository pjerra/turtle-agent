package raidwalk

import (
	"strconv"
	"strings"
	"testing"
)

func TestAllowedLine(t *testing.T) {
	hires := map[string]bool{"Tankbot": true, "Healbot": true}
	party := map[string]bool{"Tankbot": true, "Healbot": true, "StrayBot": true}
	ok := []string{
		".go xyz 1 2 3 249",
		".go xyz -4768.61 -3752.11 53.43 1",
		".go xyz -11915.8 -1236.43 96.1502 0",
		".bot hire raid 1 warrior:tank level=60 t3",
		".bot hire raid 10h 26d level=60",
		".bot hire raid 3t 10h 26d level=60",
		".bot gear raid Glacienne tier 2",
		".bot role Tankbot protection",
		".bot role Healbot holy",
		".bot role Tankbot bear",
		// phaseHire dismisses a group member this walk did not hire: the module's own uninvite line (walk 80).
		".bot uninvite StrayBot",
		".tele name Tankbot onyxia",
		".summon Tankbot",
		".revive Tankbot",
		".appear Tankbot",
		".gm visible off",
		".bot run stop",
		".bot run resume",
	}
	for _, line := range ok {
		if !AllowedLine(line, hires, party, 309) {
			t.Errorf("refused an allowed line %q", line)
		}
	}
	bad := []string{
		".die Tankbot",
		".npc delete",
		".summon NotAHire",
		".kick X",
		".go xyz 1 2",
		".go xyz 1 2 3",
		".go xyz 1 2 3 -1",
		".go xyz NaN 2 3 249",
		".go xyz 1 2 3 249 extra",
		".bot hire raid onyxia level=60",
		".bot hire raid 3t 10h 26d",
		".bot hire raid 3t 10h 26d level=0",
		".bot hire raid 3t 10h 26d level=61",
		".bot hire raid 3t 10h 26d level=60 t4",
		".bot hire raid 1 bard level=60",
		".bot gear raid Glacienne tier",
		".bot gear raid Glacienne tier 0",
		".bot gear raid Glacienne tier 4",
		".bot gear raid Glacienne tier x",
		".bot gear raid Glacienne tier 2 extra",
		".bot gear raid Glacienne preset onyxia",
		".bot role NotAHire protection",
		".bot role Tankbot prot",
		".bot role Tankbot tank",
		".bot role Tankbot",
		".bot role Tankbot protection extra",
		".bot uninvite",
		".bot uninvite StrayBot extra",
		".bot uninvite 9lives",
		".bot uninvite x",
		// Only a current group member this walk did not hire: a hire of this walk and a name outside the party fail.
		".bot uninvite Tankbot",
		".bot uninvite Stranger",
		".tele name NotAHire onyxia",
		".tele name Tankbot bad word",
		".summon Tankbot extra",
		".appear NotAHire",
		".appear Tankbot extra",
		".appear",
		".gm visible on",
		".gm off",
		".gm visible off now",
		".gm visible off Tankbot",
		" .gm visible off",
		".gm  visible off",
		".bot run stop now",
		".bot run stop time limit reached (raid-walk 0m)",
		".bot run resume now",
		".bot run resume x",
	}
	for _, line := range bad {
		if AllowedLine(line, hires, party, 309) {
			t.Errorf("accepted a forbidden line %q", line)
		}
	}
}

// The place phase's two added GM lines (spec 7): .appear of a hire, and .instance unbind of the walk's own map.
func TestAllowedLineAppearUnbind(t *testing.T) {
	hires := map[string]bool{"Tankbot": true, "Healbot": true}
	for _, line := range []string{".appear Tankbot", ".appear Healbot", ".instance unbind 309", ".gm on", ".instance listbinds"} {
		if !AllowedLine(line, hires, nil, 309) {
			t.Errorf("refused an allowed line %q", line)
		}
	}
	for _, line := range []string{".appear Stranger", ".appear", ".appear Tankbot extra", ".instance unbind all",
		".instance unbind 409", ".instance unbind", ".instance listbinds 309", ".instance unbind 0309",
		".gm", ".gm off", ".gm on extra", ".gm visible on"} { // `.gm visible off` is the revive's one exact line (TestAllowedLine)
		if AllowedLine(line, hires, nil, 309) {
			t.Errorf("accepted a forbidden line %q", line)
		}
	}
}

func TestAllowedTT(t *testing.T) {
	hires := map[string]bool{"Tankbot": true, "Healbot": true}
	ok := []string{
		"q inst",
		"q floor -11920 -1857.6 61.34",
		"q group of=Tankbot",
		"q units 30 -11882.36 -1866.66 63.88 all",
		"q units 41 -11882.36 -1866.66 63.88 all",
		"q units 80 -11919.999 -1857.6 61.340786 all",
		"q units 40 -11703.2 -1996.8 62.13 all dead e=14509",
		"q units 40 -11703.2 -1996.8 62.13 all dead e=12118",
		"q units 40 -11703.2 -1996.8 62.13 all e=14509",
		"quiet 50 -11920 -1857.6 61.34 w=20 dz=8",
		"quiet 50 -11920 -1857.6 61.34 w=20 dz=8 skip=14758,15091",
		"quiet 50 -11920 -1857.6 61.34 w=0 dz=15 skip=962",
		"place -11920 -1857.6 61.34 spread=5 of=Tankbot",
		"place -11920 -1857.6 61.34 spread=0 of=Healbot",
		"clearto 309 -11945.7 -1841.13 56.11 8 dz=8 all=1",
		"clearto 309 -11945.7 -1841.13 56.11 30 dz=8 all=1 skip=14758,15091",
		"clearto 309 -11882.36 -1866.66 63.88 3 dz=8 all=1",
	}
	for _, cmd := range ok {
		if !AllowedTT(cmd, hires, 309) {
			t.Errorf("refused an allowed command %q", cmd)
		}
	}
	bad := []string{
		"",
		"clearto 309 -11945.7 -1841.13 56.11 8 dz=8 all=1 kill",
		"clearto 309 -11945.7 -1841.13 56.11 8 dz=8 all=1 boss",
		"clearto 309 -11945.7 -1841.13 56.11 8 all=1",
		"clearto 309 -11945.7 -1841.13 56.11 31 dz=8 all=1",
		"clearto 409 -11945.7 -1841.13 56.11 8 dz=8 all=1",
		"clearto 309 -11945.7 -1841.13 56.11 8 dz=0 all=1",
		"clearto 309 -11945.7 -1841.13 56.11 8 dz=16 all=1",
		"clearto 309 -11945.7 -1841.13 56.11 8 dz=8",
		"clearto 309 -11945.7 -1841.13 56.11 8 dz=8 all=0",
		"clearto 309 -11945.7 -1841.13 56.11 8 dz=8 all=1 skip=x",
		"place here spread=5 of=Tankbot",
		"place -11920 -1857.6 61.34 spread=5 of=Stranger",
		"place -11920 -1857.6 61.34 spread=9 of=Tankbot",
		"place -11920 -1857.6 61.34 spread=5",
		"place -11920 -1857.6 61.34 spread=5 of=Tankbot force",
		"place -11920 -1857.6 61.34 spread=5 of=Tankbot me",
		"q group of=Stranger",
		"q group",
		"q units 30 -11882.36 -1866.66 63.88",
		"q units 81 -11882.36 -1866.66 63.88 all",
		"q units 40 -11703.2 -1996.8 62.13 all dead e=0",
		"q units 40 -11703.2 -1996.8 62.13 all dead e=10000000",
		"q units 40 -11703.2 -1996.8 62.13 all dead e=x",
		"q units 40 -11703.2 -1996.8 62.13 all dead e=14509 kill",
		"q units 40 -11703.2 -1996.8 62.13 dead all",
		"q units 40 -11703.2 -1996.8 62.13 all boss",
		"q unit 0xF130000000000001",
		"q link 0xF130000000000001",
		"quiet 51 -11920 -1857.6 61.34 w=20 dz=8",
		"quiet 50 -11920 -1857.6 61.34 w=31 dz=8",
		"quiet 50 -11920 -1857.6 61.34 w=20 dz=0",
		"quiet 50 -11920 -1857.6 61.34 w=20",
		"reset 309",
		"q floor 1 2 3 #abc",
		"q inst #abc",
		"kill 1",
		"boss",
		"force",
		"me",
		"here",
		"q inst\r\nplace 1 2 3 spread=0 of=Tankbot",
	}
	for _, cmd := range bad {
		if AllowedTT(cmd, hires, 309) {
			t.Errorf("accepted a forbidden command %q", cmd)
		}
	}
	// the length cap
	long := "q floor " + strings.Repeat("1", 200)
	if AllowedTT(long, hires, 309) {
		t.Error("accepted an over-long command")
	}
	// the skip cap: MaxSkipEntries entries pass, one more is refused (review round 1, item 11).
	ids := make([]string, 0, MaxSkipEntries+1)
	for i := range MaxSkipEntries + 1 {
		ids = append(ids, strconv.Itoa(90000+i))
	}
	at := "clearto 309 1 2 3 8 dz=8 all=1 skip=" + strings.Join(ids[:MaxSkipEntries], ",")
	over := "clearto 309 1 2 3 8 dz=8 all=1 skip=" + strings.Join(ids, ",")
	if !AllowedTT(at, hires, 309) {
		t.Errorf("refused %d skip entries", MaxSkipEntries)
	}
	if AllowedTT(over, hires, 309) {
		t.Errorf("accepted %d skip entries", MaxSkipEntries+1)
	}
}

// TestAllowedPInfo pins the instance ledger's account read: read-only, the walk's own hires only.
func TestAllowedPInfo(t *testing.T) {
	hires := map[string]bool{"Tankbot": true, "Healbot": true}
	for _, line := range []string{".pinfo", ".pinfo Tankbot", ".pinfo Healbot"} {
		if !AllowedLine(line, hires, nil, 309) {
			t.Errorf("refused %q", line)
		}
	}
	for _, line := range []string{".pinfo Stranger", ".pinfo Tankbot extra", ".pinfoo", ".pinfo Tankbot\\n.die"} {
		if AllowedLine(line, hires, nil, 309) {
			t.Errorf("allowed %q", line)
		}
	}
}
