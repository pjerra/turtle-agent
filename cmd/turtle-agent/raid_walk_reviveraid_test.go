package main

import (
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// farRevive scripts the walk 29 shape on the fake: every `.revive` puts the hire reviveFar yd east of the commander
// (its ghost ran there), and returns the commander's last `.go xyz` target for the test to compare with.
const reviveFar = 400

func reviveFarSetup(f *fixture, onSummon func(name string)) {
	prev := f.agent.onGM
	f.agent.onGM = func(line string) {
		if prev != nil {
			prev(line)
		}
		switch {
		case strings.HasPrefix(line, ".revive "):
			name := strings.TrimPrefix(line, ".revive ")
			f.agent.mu.Lock()
			if f.agent.pos == nil {
				f.agent.pos = map[string][3]float32{}
			}
			g := f.agent.goTarget
			f.agent.pos[name] = [3]float32{g[0] + reviveFar, g[1], g[2]}
			f.agent.mu.Unlock()
		case strings.HasPrefix(line, ".summon ") && onSummon != nil:
			onSummon(strings.TrimPrefix(line, ".summon "))
		}
	}
}

// Walk 29: hires revived 400 yd away are brought to the revive point before the resume goes on.
func TestRaidWalkReviveAtRaidBringsFarHiresBack(t *testing.T) {
	f, party := selfRegroupWipeWith(t, func(p []string) []string { return p }, func(f *fixture) {
		f.opts.reviveAtRaid = true
		reviveFarSetup(f, nil)
	})
	gm := f.agent.gmLines()
	if got := countPrefix(gm, ".summon "); got != len(party) {
		t.Fatalf("%d summons, want %d (every hire revived %d yd away): %q", got, len(party), reviveFar, gm)
	}
	// every summon follows the recovery's own `.go xyz` and the revive of that hire
	goAt, firstSummon, lastRevive := -1, -1, -1
	for i, l := range gm {
		switch {
		case strings.HasPrefix(l, ".go xyz") && goAt < 0 && lastRevive < 0:
			goAt = i
		case strings.HasPrefix(l, ".revive "):
			lastRevive = i
		case strings.HasPrefix(l, ".summon ") && firstSummon < 0:
			firstSummon = i
		}
	}
	if goAt < 0 || firstSummon < lastRevive {
		t.Fatalf("summon at %d, last revive at %d, go at %d: the summons must follow the revives: %q", firstSummon, lastRevive, goAt, gm)
	}
	f.agent.mu.Lock()
	g := f.agent.goTarget
	for _, n := range party {
		p := f.agent.pos[n]
		if d := dist3(p, g); d > reviveArriveYd+reviveArriveZYd {
			f.agent.mu.Unlock()
			t.Fatalf("%s stands %.0f yd from the revive point, want it brought to the point", n, d)
		}
	}
	f.agent.mu.Unlock()
	raw, err := os.ReadFile(filepath.Join(f.out, "run.log"))
	if err != nil {
		t.Fatal(err)
	}
	log := string(raw)
	for _, n := range party {
		if !strings.Contains(log, "revive: "+n+" brought back from 400 yd") {
			t.Fatalf("run.log has no `revive: %s brought back from 400 yd` line:\n%s", n, log)
		}
	}
	sum := f.summary()
	if sum.ReviveBrought != len(party) || sum.ReviveUnreached != 0 || sum.ReviveGatherMissed != 0 {
		t.Fatalf("summary counts brought %d unreached %d gather_missed %d, want %d 0 0", sum.ReviveBrought, sum.ReviveUnreached, sum.ReviveGatherMissed, len(party))
	}
	if !hasNote(sum, "brought back to the revive point") || hasNote(sum, "not gathered") {
		t.Fatalf("notes %q, want the brought-back note and no gather miss", sum.Notes)
	}
	if !hasNote(sum, "the run regroups by itself") {
		t.Fatalf("notes %q, want the self-regroup recovery note", sum.Notes)
	}
}

// One hire that fights away from the raid is waited for (two summons, then 60 s), the resume goes on and the note
// names it.
func TestRaidWalkReviveAtRaidWaitsForAHireFightingAway(t *testing.T) {
	var victim string
	f, party := selfRegroupWipeWith(t, func(p []string) []string { return p }, func(f *fixture) {
		f.opts.reviveAtRaid = true
		f.agent.combatAllFn = nil
		victim = "Bot03"
		f.agent.combatBots = map[string]bool{victim: true}
		// the portal-style failure: the summon of this hire is cancelled by its combat, so it stays where it was
		reviveFarSetup(f, func(name string) {
			if name != victim {
				return
			}
			f.agent.mu.Lock()
			g := f.agent.goTarget
			f.agent.pos[name] = [3]float32{g[0] + reviveFar, g[1], g[2]}
			f.agent.mu.Unlock()
		})
	})
	gm := f.agent.gmLines()
	n := 0
	for _, l := range gm {
		if l == ".summon "+victim {
			n++
		}
	}
	if n != 2 {
		t.Fatalf("%d summons of %s, want 2 (one retry): %q", n, victim, gm)
	}
	if got := countPrefix(gm, ".summon "); got != len(party)+1 {
		t.Fatalf("%d summons, want %d (each hire once, the fighting one twice)", got, len(party)+1)
	}
	sum := f.summary()
	if !hasNote(sum, "resuming with 1 hire(s) not gathered") || !hasNote(sum, victim+" ("+"400 yd away, in combat)") {
		t.Fatalf("notes %q, want the gather note naming %s in combat 400 yd away", sum.Notes, victim)
	}
	if hasNote(sum, "Bot02 (") {
		t.Fatalf("notes %q name a hire that was gathered", sum.Notes)
	}
	if sum.ReviveUnreached != 1 || sum.ReviveGatherMissed != 1 || sum.ReviveBrought != len(party) {
		t.Fatalf("summary counts brought %d unreached %d gather_missed %d, want %d 1 1", sum.ReviveBrought, sum.ReviveUnreached, sum.ReviveGatherMissed, len(party))
	}
	if !hasNote(sum, "the run regroups by itself") {
		t.Fatalf("notes %q, want the recovery to go on to the self-regroup note", sum.Notes)
	}
}

// --revive-at-raid=false gives the old flow: revived in place, no summon, no revive-at-raid note or count.
func TestRaidWalkReviveAtRaidOffKeepsTheOldFlow(t *testing.T) {
	f, party := selfRegroupWipeWith(t, func(p []string) []string { return p }, func(f *fixture) {
		f.opts.reviveAtRaid = false
		reviveFarSetup(f, nil)
	})
	gm := f.agent.gmLines()
	if got := countPrefix(gm, ".summon "); got != 0 {
		t.Fatalf("%d summons, want none with --revive-at-raid=false: %q", got, gm)
	}
	if got := countPrefix(gm, ".revive "); got != len(party) {
		t.Fatalf("%d revives, want %d", got, len(party))
	}
	sum := f.summary()
	if hasNote(sum, "revive-at-raid") || sum.ReviveBrought != 0 || sum.ReviveGatherMissed != 0 {
		t.Fatalf("notes %q brought %d: want no revive-at-raid trace", sum.Notes, sum.ReviveBrought)
	}
	if !hasNote(sum, "self-regroup: no summons, revived in place") {
		t.Fatalf("notes %q, want the old no-summons note", sum.Notes)
	}
}

func TestRaidWalkReviveAtRaidFlag(t *testing.T) {
	o, code := parseRaidWalkArgs([]string{"onyxias_lair"}, io.Discard)
	if code != exitOK || !o.reviveAtRaid {
		t.Fatalf("default: exit %d reviveAtRaid %v, want on", code, o.reviveAtRaid)
	}
	o, code = parseRaidWalkArgs([]string{"onyxias_lair", "--revive-at-raid=false"}, io.Discard)
	if code != exitOK || o.reviveAtRaid {
		t.Fatalf("--revive-at-raid=false: exit %d reviveAtRaid %v, want off", code, o.reviveAtRaid)
	}
}
