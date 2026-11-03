package main

import (
	"context"
	"github.com/pjerra/turtle-agent/internal/objects"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
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

// Codex HIGH on 43d1496: a living lead on another map is not "where the raid is". It is brought back like any hire
// and, when that fails, named after the gather timeout instead of dropping off the straggler list.
func TestRaidWalkReviveAtRaidLeadOnAnotherMapStaysAStraggler(t *testing.T) {
	f, party := selfRegroupWipeWith(t, func(p []string) []string { return p }, func(f *fixture) {
		f.opts.reviveAtRaid = true
		f.agent.combatAllFn = nil
		reviveFarSetup(f, nil)
		// the layout's main tank is revived on another map (it passed the enter check on the plan's)
		prev := f.agent.onGM
		f.agent.onGM = func(line string) {
			prev(line)
			if line == ".revive Bot01" {
				f.agent.mu.Lock()
				f.agent.offMapBots = map[string]bool{"Bot01": true}
				f.agent.mu.Unlock()
			}
		}
	})
	gm := f.agent.gmLines()
	n := 0
	for _, l := range gm {
		if l == ".summon Bot01" {
			n++
		}
	}
	if n != 2 {
		t.Fatalf("%d summons of the off-map lead, want 2 (the summon and one retry): %q", n, gm)
	}
	sum := f.summary()
	if !hasNote(sum, "Bot01 (on another map)") || !hasNote(sum, "not gathered") {
		t.Fatalf("notes %q, want the off-map lead named as not gathered", sum.Notes)
	}
	if hasNote(sum, "Bot02 (") {
		t.Fatalf("notes %q name a gathered hire", sum.Notes)
	}
	if sum.ReviveGatherMissed != 1 || sum.ReviveUnreached != 1 || sum.ReviveBrought != len(party) {
		t.Fatalf("counts brought %d unreached %d gather_missed %d, want %d 1 1", sum.ReviveBrought, sum.ReviveUnreached, sum.ReviveGatherMissed, len(party))
	}
}

// A near-wipe stop (someone alive): the hires it revives at their corpses 400 yd from the living ones are brought
// to them (`.appear` at a living hire, then `.summon`), and the resume goes on.
func TestRaidWalkReviveAtRaidNearWipeBringsRevivedHiresBack(t *testing.T) {
	for _, on := range []bool{true, false} {
		name := "flag off"
		if on {
			name = "flag on"
		}
		t.Run(name, func(t *testing.T) {
			f := newFixture(t)
			f.opts.reviveAtRaid = on
			f.agent.startReply = startAt("onyxias_lair", 10)
			f.agent.unitsFn = func(n int) []objects.Unit { return []objects.Unit{} }
			var resumeAt time.Time
			resumeHook(f, 11, 28, &resumeAt)
			f.agent.mu.Lock()
			f.agent.startQueue = []queuedEvent{
				{raw: "TBM:RUN|onyxias_lair|paused|10/29|onyxia|move|walk|19 dead: 3 near wipes within 10 min (54): the run stops here. Regroup (c...)"},
			}
			f.agent.mu.Unlock()
			// the raid (the lead alone, alive) stands at A; every other hire lies dead 400 yd east of it
			a := [3]float32{100, 100, 50}
			f.agent.onGM = func(line string) {
				if !strings.HasPrefix(line, ".bot hire raid ") {
					return
				}
				f.agent.mu.Lock()
				defer f.agent.mu.Unlock()
				if len(f.agent.party) < 39 {
					return
				}
				if f.agent.pos == nil {
					f.agent.pos = map[string][3]float32{}
				}
				for i, n := range f.agent.party {
					if i == 0 {
						f.agent.pos[n] = a
						continue
					}
					f.agent.dead[n] = true
					f.agent.pos[n] = [3]float32{a[0] + reviveFar, a[1], a[2]}
				}
			}
			if code := f.run(context.Background()); code != exitOK {
				t.Fatalf("exit %d, want 0: %s\n%s", code, f.stderr.String(), f.stdout.String())
			}
			f.agent.mu.Lock()
			party := append([]string{}, f.agent.party...)
			far := 0
			for _, n := range party[1:] {
				if dist3(f.agent.pos[n], a) > reviveFarYd {
					far++
				}
			}
			f.agent.mu.Unlock()
			gm := f.agent.gmLines()
			sum := f.summary()
			if !on {
				if got := countPrefix(gm, ".summon "); got != 0 || far != len(party)-1 || sum.ReviveBrought != 0 {
					t.Fatalf("flag off: %d summons, %d hires still far, brought %d; want the old revive in place", got, far, sum.ReviveBrought)
				}
				return
			}
			if got := countPrefix(gm, ".summon "); got != len(party)-1 {
				t.Fatalf("%d summons, want %d (each revived hire): %q", got, len(party)-1, gm)
			}
			if far != 0 {
				t.Fatalf("%d revived hires still far from the raid", far)
			}
			lastRevive, appearAt, firstSummon := -1, -1, -1
			for i, l := range gm {
				switch {
				case strings.HasPrefix(l, ".revive "):
					lastRevive = i
				case l == ".appear "+party[0]:
					appearAt = i
				case strings.HasPrefix(l, ".summon ") && firstSummon < 0:
					firstSummon = i
				}
			}
			if !(lastRevive < appearAt && appearAt < firstSummon) {
				t.Fatalf("order revive %d, appear %d, first summon %d: want revives, then the appear at the living lead, then the summons: %q", lastRevive, appearAt, firstSummon, gm)
			}
			if sum.ReviveBrought != len(party)-1 || sum.ReviveUnreached != 0 || sum.ReviveGatherMissed != 0 {
				t.Fatalf("counts brought %d unreached %d gather_missed %d, want %d 0 0 (%q)", sum.ReviveBrought, sum.ReviveUnreached, sum.ReviveGatherMissed, len(party)-1, sum.Notes)
			}
			raw, _ := os.ReadFile(filepath.Join(f.out, "run.log"))
			if !strings.Contains(string(raw), "revive: "+party[1]+" brought back from 400 yd") {
				t.Fatalf("run.log has no brought-back line for %s", party[1])
			}
			if resumeAt.IsZero() || sum.WipeResumes != 1 || sum.EndReason != "until" {
				t.Fatalf("resume at %v, wipe_resumes %d, end %q: want the run resumed to until", resumeAt, sum.WipeResumes, sum.EndReason)
			}
		})
	}
}

// nearWipeRevive runs the near-wipe stop of TestRaidWalkReviveAtRaidNearWipeBringsRevivedHiresBack with revive-at-raid
// on: the lead alone alive at a, every other hire dead 400 yd east of it. setup runs before the run and may wrap
// f.agent.onGM and set the view hooks. It returns the fixture and the party (the lead first).
func nearWipeRevive(t *testing.T, setup func(f *fixture)) (*fixture, []string) {
	t.Helper()
	f := newFixture(t)
	f.opts.reviveAtRaid = true
	f.agent.startReply = startAt("onyxias_lair", 10)
	f.agent.unitsFn = func(n int) []objects.Unit { return []objects.Unit{} }
	var resumeAt time.Time
	resumeHook(f, 11, 28, &resumeAt)
	f.agent.mu.Lock()
	f.agent.startQueue = []queuedEvent{
		{raw: "TBM:RUN|onyxias_lair|paused|10/29|onyxia|move|walk|19 dead: 3 near wipes within 10 min (54): the run stops here. Regroup (c...)"},
	}
	f.agent.mu.Unlock()
	a := [3]float32{100, 100, 50}
	f.agent.onGM = func(line string) {
		if !strings.HasPrefix(line, ".bot hire raid ") {
			return
		}
		f.agent.mu.Lock()
		defer f.agent.mu.Unlock()
		if len(f.agent.party) < 39 {
			return
		}
		if f.agent.pos == nil {
			f.agent.pos = map[string][3]float32{}
		}
		for i, n := range f.agent.party {
			if i == 0 {
				f.agent.pos[n] = a
				continue
			}
			f.agent.dead[n] = true
			f.agent.pos[n] = [3]float32{a[0] + reviveFar, a[1], a[2]}
		}
	}
	setup(f)
	if code := f.run(context.Background()); code != exitOK {
		t.Fatalf("exit %d, want 0: %s\n%s", code, f.stderr.String(), f.stdout.String())
	}
	f.agent.mu.Lock()
	party := append([]string{}, f.agent.party...)
	f.agent.mu.Unlock()
	return f, party
}

// Codex HIGH on e625950: a revived hire whose alive frame comes 8 s after its revive (until then the view leaves it out,
// so only its dead frame from before stands) is still brought back. The revive's frames are polled, not read once.
func TestRaidWalkReviveAtRaidNearWipeWaitsForALateAliveFrame(t *testing.T) {
	// The victim is the last hire revived, so its alive frame comes 8 s after the revive loop ends: the wait starts there,
	// and a single read 5 s later (the old fixed sleep) would still see only the dead frame.
	var victim string
	var revivedAt time.Time
	f, party := nearWipeRevive(t, func(f *fixture) {
		prev := f.agent.onGM
		f.agent.onGM = func(line string) {
			prev(line)
			if strings.HasPrefix(line, ".revive ") {
				victim = strings.TrimPrefix(line, ".revive ")
				revivedAt = f.clock.now()
			}
		}
		f.agent.viewFn = func(int) (bool, string) {
			if victim != "" && f.clock.now().Before(revivedAt.Add(8*time.Second)) {
				return false, victim
			}
			return false, ""
		}
	})
	if victim == "" {
		t.Fatal("no `.revive` line: the near-wipe path revived nobody")
	}
	gm := f.agent.gmLines()
	if got := countPrefix(gm, ".summon "); got != len(party)-1 {
		t.Fatalf("%d summons, want %d (each revived hire, %s included): %q", got, len(party)-1, victim, gm)
	}
	if got := countExact(gm, ".summon "+victim); got != 1 {
		t.Fatalf("%d summons of %s, want 1 (its late frame shows it far, not unknown): %q", got, victim, gm)
	}
	raw, err := os.ReadFile(filepath.Join(f.out, "run.log"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(raw), "revive: "+victim+" brought back from 400 yd") {
		t.Fatalf("run.log has no `revive: %s brought back from 400 yd` line:\n%s", victim, raw)
	}
	sum := f.summary()
	if hasNote(sum, "state unknown") || hasNote(sum, "shown dead") {
		t.Fatalf("notes %q name a hire not shown alive, want none: the late frame came within the wait", sum.Notes)
	}
	if sum.ReviveBrought != len(party)-1 || sum.ReviveUnreached != 0 {
		t.Fatalf("counts brought %d unreached %d, want %d 0", sum.ReviveBrought, sum.ReviveUnreached, len(party)-1)
	}
	if sum.WipeResumes != 1 || sum.EndReason != "until" {
		t.Fatalf("wipe_resumes %d, end %q: want the run resumed to until", sum.WipeResumes, sum.EndReason)
	}
}

// Codex HIGH on e625950: a view that fails through the revive's wait leaves every revived hire's state unknown. None is
// dropped: each is summoned to the point anyway, and the note names them `state unknown`.
func TestRaidWalkReviveAtRaidNearWipeSummonsAHireWithUnknownState(t *testing.T) {
	var victim string
	var revived, polled bool
	f, party := nearWipeRevive(t, func(f *fixture) {
		prev := f.agent.onGM
		f.agent.onGM = func(line string) {
			prev(line)
			switch {
			case strings.HasPrefix(line, ".revive "):
				if victim == "" {
					victim = strings.TrimPrefix(line, ".revive ")
				}
				revived = true
			case revived:
				polled = true // the first GM line after the revives: the wait for the frames is over
			}
		}
		// every bot_view fails from the first revive until the wait is over
		f.agent.viewFn = func(int) (bool, string) { return revived && !polled, "" }
	})
	if victim == "" {
		t.Fatal("no `.revive` line: the near-wipe path revived nobody")
	}
	gm := f.agent.gmLines()
	if got := countPrefix(gm, ".summon "); got != len(party)-1 {
		t.Fatalf("%d summons, want %d (every revived hire, the unknown ones too): %q", got, len(party)-1, gm)
	}
	if got := countExact(gm, ".summon "+victim); got != 1 {
		t.Fatalf("%d summons of %s, want 1: %q", got, victim, gm)
	}
	sum := f.summary()
	if !hasNote(sum, victim+" (state unknown)") {
		t.Fatalf("notes %q, want %s named as state unknown", sum.Notes, victim)
	}
	if sum.ReviveBrought != len(party)-1 || sum.ReviveUnreached != 0 {
		t.Fatalf("counts brought %d unreached %d, want %d 0 (%q)", sum.ReviveBrought, sum.ReviveUnreached, len(party)-1, sum.Notes)
	}
	if sum.WipeResumes != 1 || sum.EndReason != "until" {
		t.Fatalf("wipe_resumes %d, end %q: want the run resumed to until", sum.WipeResumes, sum.EndReason)
	}
}

// countExact counts the lines equal to line.
func countExact(lines []string, line string) int {
	n := 0
	for _, l := range lines {
		if l == line {
			n++
		}
	}
	return n
}
