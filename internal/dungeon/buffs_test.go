package dungeon

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/pjerra/turtle-agent/internal/packet"
	"github.com/pjerra/turtle-agent/internal/spells"
)

func auraCmds(g *fakeGame) []string {
	var out []string
	for _, c := range g.gms {
		if strings.HasPrefix(c, ".aura ") {
			out = append(out, c)
		}
	}
	return out
}

func buffShortcuts(r Report) (out []string) {
	for _, s := range r.Shortcuts {
		if s.Kind == "buff" {
			out = append(out, s.Detail)
		}
	}
	return out
}

func worldBuffs(t *testing.T) []Buff {
	t.Helper()
	b, err := ResolveBuffs([]string{"world"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

// Every buff goes on every companion, each application is flagged, and a run with no loss tops nobody up.
func TestBuffsAreAppliedToEachCompanionAndFlagged(t *testing.T) {
	g, p := runFixture(t)
	buffs := worldBuffs(t)
	d := NewDriver(p, g, Config{Buffs: buffs}, g.clock())
	if err := d.Run(context.Background(), 0); err != nil {
		t.Fatalf("%v\n%+v", err, d.Report())
	}
	rep := d.Report()
	if got := auraCmds(g); len(got) != 14 || got[0] != ".aura 22888" || got[6] != ".aura 22820" || got[7] != ".aura 22888" {
		t.Fatalf("aura commands %q", got)
	}
	if len(g.gmOnGUIDs) != 14 || g.gmOnGUIDs[0] != tank || g.gmOnGUIDs[6] != tank || g.gmOnGUIDs[7] != healer || g.gmOnGUIDs[13] != healer {
		t.Fatalf("selections %v: each .aura goes with the selection of its companion", g.gmOnGUIDs)
	}
	flags := buffShortcuts(rep)
	if len(flags) != 14 || flags[0] != "Rallying Cry of the Dragonslayer on Tank" || flags[3] != "Warchief's Blessing on Tank" || flags[7] != "Rallying Cry of the Dragonslayer on Heal" {
		t.Fatalf("flags %q", flags)
	}
	for _, s := range rep.Shortcuts {
		if s.Kind != "buff" || s.Leg != 0 || s.At.IsZero() {
			t.Fatalf("%+v", s)
		}
	}
	if len(rep.Buffs) != 7 || rep.Buffs[2].ID != 24425 {
		t.Fatalf("report buffs %+v", rep.Buffs)
	}
	var sum map[string]any
	for _, e := range g.events {
		if e.kind == "dungeon_done" {
			sum = e.data
		}
	}
	if names, _ := sum["buffs"].([]string); len(names) != 7 || names[1] != "Songflower Serenade (15366)" || sum["gm_shortcuts"] != 14 {
		t.Fatalf("summary %v", sum)
	}
}

// Without the parameter nothing is sent or flagged, and the report says so.
func TestNoBuffsSendsNothing(t *testing.T) {
	g, p := runFixture(t)
	d := NewDriver(p, g, Config{}, g.clock())
	if err := d.Run(context.Background(), 0); err != nil {
		t.Fatalf("%v", err)
	}
	rep := d.Report()
	if len(auraCmds(g)) != 0 || len(rep.Shortcuts) != 0 || rep.Buffs == nil || len(rep.Buffs) != 0 {
		t.Fatalf("gms %q report %+v", g.gms, rep)
	}
	if names, ok := d.rep.summary()["buffs"].([]string); !ok || len(names) != 0 {
		t.Fatalf("%v", d.rep.summary())
	}
}

// A server that ignores .aura (no developer level) shows in the auras: the run fails at once with a clear error.
func TestRefusedBuffCommandFailsClearly(t *testing.T) {
	g, p := runFixture(t)
	g.auraRefused = true
	d := NewDriver(p, g, Config{Buffs: worldBuffs(t)[:1]}, g.clock())
	err := d.Run(context.Background(), 0)
	if err == nil || !strings.Contains(err.Error(), "GM .aura on Tank had no effect") || !strings.Contains(err.Error(), "developer security") ||
		!strings.Contains(err.Error(), "Rallying Cry of the Dragonslayer (22888)") {
		t.Fatalf("%v", err)
	}
	if rep := d.Report(); rep.State != "failed" || rep.Error != err.Error() || len(g.walks) != 0 {
		t.Fatalf("%+v walks %v", rep, g.walks)
	}

	// A command the session could not send is named too.
	g, p = runFixture(t)
	g.auraErr = errors.New("not in world")
	d = NewDriver(p, g, Config{Buffs: worldBuffs(t)[:1]}, g.clock())
	if err := d.Run(context.Background(), 0); err == nil || !strings.Contains(err.Error(), "buffs: .aura 22888 on Tank: not in world") {
		t.Fatalf("%v", err)
	}
}

// A companion revived by GM gets the buffs again (death strips them), after the revive and flagged.
func TestBuffsComeBackAfterAGMRevive(t *testing.T) {
	g, p := runFixture(t)
	g.party[1].Dead = true
	d := NewDriver(p, g, Config{Shortcuts: true, Buffs: worldBuffs(t)[:1]}, g.clock())
	if err := d.Run(context.Background(), 0); err != nil {
		t.Fatalf("%v\n%+v", err, d.Report())
	}
	var kinds []string
	for _, s := range d.Report().Shortcuts {
		kinds = append(kinds, s.Kind+":"+s.Detail)
	}
	want := "[buff:Rallying Cry of the Dragonslayer on Tank revive:GM .revive Heal buff:Rallying Cry of the Dragonslayer on Heal]"
	if fmt.Sprint(kinds) != want {
		t.Fatalf("%v", kinds)
	}
	if len(g.auras[healer]) != 1 {
		t.Fatalf("auras %v", g.auras)
	}
}

// A leg that finds a buff gone gives it again once; a member that does not keep it is not chased, and the run goes on.
func TestBuffTopUpIsOncePerLegPerMember(t *testing.T) {
	g, p := runFixture(t)
	first := true
	g.onWalk = func(g *fakeGame, to Point) {
		if first {
			first = false
			delete(g.auras, tank)
			g.auraRefused = true // the top-up will not stick
		}
	}
	d := NewDriver(p, g, Config{Buffs: worldBuffs(t)[:1]}, g.clock())
	if err := d.Run(context.Background(), 0); err != nil {
		t.Fatalf("%v\n%+v", err, d.Report())
	}
	if n := countOf(g.gms, ".aura 22888"); n != 3 { // two at the start, one top-up of Tank at leg 1
		t.Fatalf("aura commands %q", auraCmds(g))
	}
	if !strings.Contains(notes(d), "buffs still missing on Tank") {
		t.Fatalf("notes %q", notes(d))
	}
	if fl := buffShortcuts(d.Report()); len(fl) != 3 || fl[2] != "Rallying Cry of the Dragonslayer on Tank" {
		t.Fatalf("%q", fl)
	}
}

func TestResolveBuffs(t *testing.T) {
	w, err := ResolveBuffs([]string{" World "}, nil)
	if err != nil || len(w) != 7 {
		t.Fatalf("%v %v", w, err)
	}
	// Every id of a preset carries the name the core's spell table has for it.
	for name, l := range BuffPresets {
		for _, b := range l {
			if spells.Name(b.ID) != b.Name {
				t.Errorf("%s: %d is %q in the spell table, preset says %q", name, b.ID, spells.Name(b.ID), b.Name)
			}
		}
	}
	got, err := ResolveBuffs([]string{"world"}, []uint32{22888, 1, 999999999})
	if err != nil || len(got) != 9 || got[7].ID != 1 || got[7].Name != spells.Name(1) || got[8].Name != "spell 999999999" {
		t.Fatalf("%v %v", got, err)
	}
	if _, err := ResolveBuffs([]string{"worldly"}, nil); err == nil || !strings.Contains(err.Error(), `unknown preset "worldly" (presets: world)`) {
		t.Fatalf("%v", err)
	}
	if _, err := ResolveBuffs(nil, []uint32{0}); err == nil {
		t.Fatal("id 0")
	}
	many := make([]uint32, MaxBuffs+1)
	for i := range many {
		many[i] = uint32(100 + i)
	}
	if _, err := ResolveBuffs(nil, many); err == nil || !strings.Contains(err.Error(), "at most") {
		t.Fatalf("%v", err)
	}
	if none, err := ResolveBuffs(nil, nil); err != nil || len(none) != 0 {
		t.Fatalf("%v %v", none, err)
	}
}

// Somebody else (the target RPC, the addon host) changing the selection between two commands must not move a buff
// onto another unit: every .aura carries its own selection, and the other unit gets nothing.
func TestBuffsFollowTheirOwnSelectionNotTheCurrentOne(t *testing.T) {
	const boss packet.GUID = 107
	g, p := runFixture(t)
	g.onTick = func(g *fakeGame) { // the selection moves to the boss whenever time passes in the buff phase (the 300 ms gaps)
		if len(g.gmOnGUIDs) < 14 {
			g.targeted = boss
		}
	}
	d := NewDriver(p, g, Config{Buffs: worldBuffs(t)}, g.clock())
	if err := d.Run(context.Background(), 0); err != nil {
		t.Fatalf("%v\n%+v", err, d.Report())
	}
	if len(g.auras[boss]) != 0 || len(g.auras[tank]) != 7 || len(g.auras[healer]) != 7 {
		t.Fatalf("auras: boss %d tank %d healer %d", len(g.auras[boss]), len(g.auras[tank]), len(g.auras[healer]))
	}
	for i, id := range g.gmOnGUIDs {
		if id != tank && id != healer {
			t.Fatalf("command %d selected %v", i, id)
		}
	}
}

// Companions out of view when the buffs go on (right after a teleport) are looked at again; one that shows up
// within the wait is checked, one that never does is noted, and none at all fails the run.
func TestBuffCheckWaitsForCompanionsToComeIntoView(t *testing.T) {
	g, p := runFixture(t)
	t0 := g.now
	g.hidden = map[packet.GUID]bool{tank: true, healer: true}
	g.onTick = func(g *fakeGame) {
		if g.now.Sub(t0) >= 4*time.Second {
			g.hidden = nil
		}
	}
	d := NewDriver(p, g, Config{Buffs: worldBuffs(t)[:1]}, g.clock())
	if err := d.Run(context.Background(), 0); err != nil {
		t.Fatalf("%v\n%+v", err, d.Report())
	}

	// One shows up, one does not within the wait: noted, the run goes on.
	g, p = runFixture(t)
	t0 = g.now
	g.hidden = map[packet.GUID]bool{healer: true}
	g.onTick = func(g *fakeGame) {
		if g.now.Sub(t0) >= 12*time.Second {
			g.hidden = nil
		}
	}
	d = NewDriver(p, g, Config{Buffs: worldBuffs(t)[:1]}, g.clock())
	if err := d.Run(context.Background(), 0); err != nil {
		t.Fatalf("%v\n%+v", err, d.Report())
	}
	if !strings.Contains(notes(d), "buffs on Heal could not be checked: not in view") {
		t.Fatalf("notes %q", notes(d))
	}

	// Nobody in view in time: nothing shows the command worked, the run fails and says so.
	g, p = runFixture(t)
	t0 = g.now
	g.hidden = map[packet.GUID]bool{tank: true, healer: true}
	d = NewDriver(p, g, Config{Buffs: worldBuffs(t)[:1]}, g.clock())
	err := d.Run(context.Background(), 0)
	if err == nil || !strings.Contains(err.Error(), "could not check the GM .aura: Tank, Heal not in view within 10 s") {
		t.Fatalf("%v", err)
	}
	if el := g.now.Sub(t0); el < 10*time.Second || el > 20*time.Second || len(g.walks) != 0 {
		t.Fatalf("waited %v, walks %v", el, g.walks)
	}
}
