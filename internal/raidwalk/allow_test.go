package raidwalk

import "testing"

func TestAllowedLine(t *testing.T) {
	hires := map[string]bool{"Tankbot": true, "Healbot": true}
	ok := []string{
		".go xyz 1 2 3 249",
		".go xyz -4768.61 -3752.11 53.43 1",
		".go xyz -11915.8 -1236.43 96.1502 0",
		".bot hire raid 1 warrior:tank level=60 t3",
		".bot hire raid 10h 26d level=60",
		".bot hire raid 3t 10h 26d level=60",
		".bot gear raid Glacienne tier 2",
		".tele name Tankbot onyxia",
		".summon Tankbot",
		".revive Tankbot",
		".bot run stop",
		".bot run resume",
	}
	for _, line := range ok {
		if !AllowedLine(line, hires) {
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
		".tele name NotAHire onyxia",
		".tele name Tankbot bad word",
		".summon Tankbot extra",
		".bot run stop now",
		".bot run stop time limit reached (raid-walk 0m)",
		".bot run resume now",
		".bot run resume x",
	}
	for _, line := range bad {
		if AllowedLine(line, hires) {
			t.Errorf("accepted a forbidden line %q", line)
		}
	}
}
