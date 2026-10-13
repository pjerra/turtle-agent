package dungeon

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/pjerra/turtle-agent/internal/objects"
	"github.com/pjerra/turtle-agent/internal/packet"
)

// Fixes from the second live Uldaman run (turtle-agent #73).

const onePackPlan = `{"version":1,"name":"t","map":70,"entrance":{"x":0,"y":0,"z":0},"legs":[
 {"name":"a","to":{"x":10,"y":0,"z":0},
  "pull_groups":[{"entries":[1,2],"positions":[{"x":30,"y":0,"z":0},{"x":32,"y":0,"z":0}]}]}]}`

// The plan's own pack is dead (an earlier walk-aggro killed it); a living mob of the same entry stands in the next pack,
// 28 yd from the anchors. The group counts as cleared: nobody is sent at the distant mob.
func TestAGroupWhosePackIsDeadDoesNotPullADistantMobOfTheSameEntry(t *testing.T) {
	g := newFake()
	for _, u := range []*fakeUnit{g.add(101, 1, Point{30, 0, 0}), g.add(102, 2, Point{32, 0, 0})} {
		u.Dead, u.Health = true, 0
	}
	g.add(109, 1, Point{63, 0, 0}) // another pack
	d := NewDriver(mustPlan(t, onePackPlan), g, Config{}, g.clock())
	if err := d.Run(context.Background(), 0); err != nil {
		t.Fatalf("%v\n%+v", err, d.Report())
	}
	if len(g.targets) != 0 || g.count(".bot action pullback") != 0 {
		t.Fatalf("targets %v pullbacks %d: the distant mob was pulled", g.targets, g.count(".bot action pullback"))
	}
	if !strings.Contains(notes(d), "taken as already dead") {
		t.Fatalf("notes %q", notes(d))
	}
}

// A pulled mob has left its spawn to come at the tank: it is still the group's, wherever it stands.
func TestAMobFightingThePartyIsStillTheGroups(t *testing.T) {
	g := newFake()
	g.add(101, 1, Point{70, 0, 0})
	g.add(102, 2, Point{32, 0, 0}).Dead = true
	g.units[0].InCombat, g.units[0].Target = true, tank
	d := NewDriver(mustPlan(t, onePackPlan), g, Config{}, g.clock())
	d.party[tank] = true
	sp := fightSpec{pick: []uint32{1, 2}, anchors: []Point{{30, 0, 0}, {32, 0, 0}}}
	if cs := d.candidates(sp, sp.pick); len(cs) != 1 || cs[0].GUID != 101 {
		t.Fatalf("%+v", cs)
	}
	g.units[0].InCombat = false
	if cs := d.candidates(sp, sp.pick); len(cs) != 0 {
		t.Fatalf("an idle mob 40 yd from the anchors is no member: %+v", cs)
	}
}

func refuse(intent, code, msg string) Refusal {
	return Refusal{Intent: intent, Code: code, Message: msg}
}

// The tank cannot reach the first mob: the refusal is read, the mob is skipped, the next member is pulled, and the
// refusal is in the report.
func TestAnUnreachableMobIsSkippedAndTheNextMemberPulled(t *testing.T) {
	g := newFake()
	g.add(101, 1, Point{30, 0, 0}).noJoin = true
	g.add(102, 2, Point{32, 0, 0})
	g.refuseTargets = map[packet.GUID][]Refusal{101: {refuse("pullback", "unreachable", "The tank could not start moving to a firing position for the pull.")}}
	d := NewDriver(mustPlan(t, onePackPlan), g, Config{}, g.clock())
	if err := d.Run(context.Background(), 0); err != nil {
		t.Fatalf("%v\n%+v", err, d.Report())
	}
	rep := d.Report()
	if fmt.Sprint(g.targets) != fmt.Sprint([]packet.GUID{101, 102}) {
		t.Fatalf("targets %v", g.targets)
	}
	if len(rep.Refusals) != 1 || rep.Refusals[0].Code != "unreachable" || rep.Refusals[0].Intent != "pullback" || rep.Refusals[0].Count != 1 || rep.Refusals[0].Leg != 0 {
		t.Fatalf("refusals %+v", rep.Refusals)
	}
	if !strings.Contains(notes(d), "was refused (unreachable") {
		t.Fatalf("notes %q", notes(d))
	}
	// The refused mob is not asked about again by a later look.
	if _, gone := d.skipped[101]; !gone {
		t.Fatal("101 is not on the skip list")
	}
}

// Every member refused: the group counts as cleared after the bounded attempts, noted; the run goes on.
func TestAGroupWhoseMembersAreAllRefusedCountsAsCleared(t *testing.T) {
	g := newFake()
	g.add(101, 1, Point{30, 0, 0})
	g.add(102, 2, Point{32, 0, 0})
	ev := refuse("pullback", "unreachable", "x")
	g.refuseTargets = map[packet.GUID][]Refusal{101: {ev, ev, ev}, 102: {ev, ev, ev}}
	d := NewDriver(mustPlan(t, onePackPlan), g, Config{}, g.clock())
	if err := d.Run(context.Background(), 0); err != nil {
		t.Fatalf("%v\n%+v", err, d.Report())
	}
	if !strings.Contains(notes(d), "counted as cleared") || !strings.Contains(notes(d), "mob1 at 30 0: unreachable: x") {
		t.Fatalf("notes %q", notes(d))
	}
	if r := d.Report().Refusals; len(r) != 1 || r[0].Count != 2 {
		t.Fatalf("refusals %+v", r)
	}
}

// A mob that evades is refused the same way ("is evading").
func TestAnEvadingMobIsSkipped(t *testing.T) {
	g := newFake()
	g.add(101, 1, Point{30, 0, 0}).noJoin = true
	g.add(102, 2, Point{32, 0, 0})
	g.refuseTargets = map[packet.GUID][]Refusal{101: {refuse("pullback", "evading", "mob1 is evading")}}
	d := NewDriver(mustPlan(t, onePackPlan), g, Config{}, g.clock())
	if err := d.Run(context.Background(), 0); err != nil {
		t.Fatalf("%v\n%+v", err, d.Report())
	}
	if fmt.Sprint(g.targets) != fmt.Sprint([]packet.GUID{101, 102}) {
		t.Fatalf("targets %v", g.targets)
	}
}

// dead-target and no-target: the target is picked again, and that does not use up a pull attempt.
func TestADeadOrLostTargetIsPickedAgain(t *testing.T) {
	g := newFake()
	g.add(101, 1, Point{30, 0, 0})
	g.add(102, 2, Point{32, 0, 0})
	g.refuseTargets = map[packet.GUID][]Refusal{101: {
		refuse("pullback", "dead-target", "The target is dead."),
		refuse("pullback", "no-target", "Select a live non-bot target first."),
	}}
	d := NewDriver(mustPlan(t, onePackPlan), g, Config{PullAttempts: 1}, g.clock())
	if err := d.Run(context.Background(), 0); err != nil {
		t.Fatalf("%v\n%+v", err, d.Report())
	}
	if fmt.Sprint(g.targets) != fmt.Sprint([]packet.GUID{101, 101, 101}) || len(d.Report().Refusals) != 2 {
		t.Fatalf("targets %v refusals %+v", g.targets, d.Report().Refusals)
	}
}

// A refused assist order (the mob evades, the target died) is recorded and sent again at once.
func TestARefusedAssistOrderIsRecordedAndSentAgain(t *testing.T) {
	g := newFake()
	g.add(101, 1, Point{30, 0, 0}).noJoin = true
	g.add(102, 2, Point{32, 0, 0})
	g.dieIn = 30 * time.Second
	sent := false
	g.onChat = func(g *fakeGame, text string) {
		if text == ".bot action attack" && !sent {
			sent = true
			g.refusals = append(g.refusals, refuse("attack", "no-target", "Select a live non-bot target first."))
		}
	}
	d := NewDriver(mustPlan(t, onePackPlan), g, Config{}, g.clock())
	if err := d.Run(context.Background(), 0); err != nil {
		t.Fatalf("%v\n%+v", err, d.Report())
	}
	if r := d.Report().Refusals; len(r) != 1 || r[0].Intent != "attack" || r[0].Code != "no-target" {
		t.Fatalf("refusals %+v", r)
	}
	if g.count(".bot action attack") < 2 {
		t.Fatalf("chats %q", g.chats)
	}
}

// Stationary mobs that do not link come one by one. Each pull that kills one is progress and does not use up a try: six
// of them used to fail the group after three evaded pulls.
func TestProgressDoesNotUseUpPullAttempts(t *testing.T) {
	g := newFake()
	const plan = `{"version":1,"name":"t","map":70,"entrance":{"x":0,"y":0,"z":0},"legs":[
 {"name":"a","to":{"x":10,"y":0,"z":0},
  "pull_groups":[{"entries":[5,5,5,5,5,5],"positions":[{"x":30,"y":0,"z":0},{"x":31,"y":0,"z":0},{"x":32,"y":0,"z":0},{"x":33,"y":0,"z":0},{"x":34,"y":0,"z":0},{"x":35,"y":0,"z":0}]}]}]}`
	for i := 0; i < 6; i++ {
		g.add(packet.GUID(200+i), 5, Point{30 + float32(i), 0, 0}).noJoin = true
	}
	d := NewDriver(mustPlan(t, plan), g, Config{}, g.clock())
	if err := d.Run(context.Background(), 0); err != nil {
		t.Fatalf("%v\n%+v", err, d.Report())
	}
	if n := g.count(".bot action pullback"); n != 6 {
		t.Fatalf("%d pulls, want one per mob", n)
	}
	if l := d.Report().Legs[0]; l.Pulls != 6 || l.PullRetries != 0 {
		t.Fatalf("%+v", l)
	}
}

// The last mob never takes the pull (live run 2: one Ambusher drew no fight twice). The group made progress, so it counts
// as cleared with a note; with no progress at all the run still fails.
func TestAStuckLastMobAfterProgressCountsAsCleared(t *testing.T) {
	const plan = `{"version":1,"name":"t","map":70,"entrance":{"x":0,"y":0,"z":0},"legs":[
 {"name":"a","to":{"x":10,"y":0,"z":0},
  "pull_groups":[{"entries":[5,5,5],"positions":[{"x":30,"y":0,"z":0},{"x":31,"y":0,"z":0},{"x":32,"y":0,"z":0}]}]}]}`
	build := func(stuck ...int) *fakeGame {
		g := newFake()
		for i := 0; i < 3; i++ {
			u := g.add(packet.GUID(200+i), 5, Point{30 + float32(i), 0, 0})
			u.noJoin = true
			for _, s := range stuck {
				u.stuck = u.stuck || s == i
			}
		}
		return g
	}
	g := build(2)
	d := NewDriver(mustPlan(t, plan), g, Config{}, g.clock())
	if err := d.Run(context.Background(), 0); err != nil {
		t.Fatalf("%v\n%+v", err, d.Report())
	}
	if !strings.Contains(notes(d), "with 2 kills") || !strings.Contains(notes(d), "counted as cleared") {
		t.Fatalf("notes %q", notes(d))
	}

	g = build(0, 1, 2)
	d = NewDriver(mustPlan(t, plan), g, Config{}, g.clock())
	if err := d.Run(context.Background(), 0); err == nil || !strings.Contains(err.Error(), "3 pulls of [5 5 5] failed") {
		t.Fatalf("%v", err)
	}
}

// from_leg without teleport says what it assumes.
func TestFromLegWithoutTeleportNotesItsAssumption(t *testing.T) {
	g, p := runFixture(t)
	d := NewDriver(p, g, Config{}, g.clock())
	if err := d.Run(context.Background(), 1); err != nil {
		t.Fatalf("%v\n%+v", err, d.Report())
	}
	if !strings.Contains(notes(d), "from_leg 1 without teleport") || !strings.Contains(notes(d), "assumes the legs before it are cleared") {
		t.Fatalf("notes %q", notes(d))
	}
	g, p = runFixture(t)
	d = NewDriver(p, g, Config{}, g.clock())
	if err := d.Run(context.Background(), 0); err != nil || strings.Contains(notes(d), "without teleport") {
		t.Fatalf("%v %q", err, notes(d))
	}
}

// --- buffs ---

func leaderAuras(g *fakeGame) int { return len(g.auras[leader]) }

// The first .aura of a companion's batch lands on the leader (live run 2, the 4th companion): it is noticed, the
// selection is made again, the command is sent once more, and the self-aura is not taken for success.
func TestABuffThatLandsOnTheLeaderIsSentAgain(t *testing.T) {
	g, p := runFixture(t)
	g.failGMOn = map[packet.GUID]int{healer: 1}
	d := NewDriver(p, g, Config{Buffs: worldBuffs(t)}, g.clock())
	if err := d.Run(context.Background(), 0); err != nil {
		t.Fatalf("%v\n%+v", err, d.Report())
	}
	if len(g.auras[healer]) != 7 || len(g.auras[tank]) != 7 {
		t.Fatalf("tank %d healer %d", len(g.auras[tank]), len(g.auras[healer]))
	}
	if leaderAuras(g) != 1 {
		t.Fatalf("the leader has %d auras", leaderAuras(g))
	}
	if !strings.Contains(notes(d), "landed on the leader; selecting Heal again") {
		t.Fatalf("notes %q", notes(d))
	}
	// Every application is flagged, the lost one too: 14 + 1.
	if n := len(buffShortcuts(d.Report())); n != 15 {
		t.Fatalf("%d buff flags", n)
	}
}

// Twice on the leader: the batch stops (the rest would land on the leader as well) and the run says so.
func TestABuffThatKeepsLandingOnTheLeaderFailsTheRun(t *testing.T) {
	g, p := runFixture(t)
	g.failGMOn = map[packet.GUID]int{tank: 5}
	d := NewDriver(p, g, Config{Buffs: worldBuffs(t)}, g.clock())
	err := d.Run(context.Background(), 0)
	if err == nil || !strings.Contains(err.Error(), "GM .aura on Tank had no effect") || !strings.Contains(err.Error(), "landed on the leader itself") {
		t.Fatalf("%v", err)
	}
	if n := len(auraCmds(g)); n != 2 {
		t.Fatalf("%d aura commands: the batch must stop after the second miss", n)
	}
	if len(g.walks) != 0 {
		t.Fatalf("walks %v", g.walks)
	}
}

// The first aura of a companion that is not in the leader's view (or too far) is not sent: it would select nothing.
func TestNoBuffIsSentToACompanionOutOfView(t *testing.T) {
	g, p := runFixture(t)
	t0 := g.now
	g.hidden = map[packet.GUID]bool{healer: true}
	sentWhileHidden := 0
	g.onTick = func(g *fakeGame) {
		if g.now.Sub(t0) >= 5*time.Second {
			g.hidden = nil
			return
		}
		for _, id := range g.gmOnGUIDs {
			if id == healer {
				sentWhileHidden++
			}
		}
	}
	d := NewDriver(p, g, Config{Buffs: worldBuffs(t)[:2]}, g.clock())
	if err := d.Run(context.Background(), 0); err != nil {
		t.Fatalf("%v\n%+v", err, d.Report())
	}
	if sentWhileHidden != 0 || len(g.auras[healer]) != 2 || len(g.auras[tank]) != 2 {
		t.Fatalf("sent while hidden %d, healer %d tank %d", sentWhileHidden, len(g.auras[healer]), len(g.auras[tank]))
	}

	g, p = runFixture(t)
	g.partyInViewNearMax = 150 // in the store, but 150 yd away
	d = NewDriver(p, g, Config{Buffs: worldBuffs(t)[:1]}, g.clock())
	if err := d.Run(context.Background(), 0); err == nil || !strings.Contains(err.Error(), "not in view within 10 s") {
		t.Fatalf("%v", err)
	}
	if len(auraCmds(g)) != 0 {
		t.Fatalf("sent %q", auraCmds(g))
	}
}

// --- review round 1 ---

const overlappingGroupsPlan = `{"version":1,"name":"t","map":70,"entrance":{"x":0,"y":0,"z":0},"legs":[
 {"name":"a","to":{"x":10,"y":0,"z":0},
  "pull_groups":[
   {"entries":[5,5],"positions":[{"x":30,"y":0,"z":0},{"x":31,"y":0,"z":0}]},
   {"entries":[5],"positions":[{"x":40,"y":0,"z":0}]}]}]}`

// A mob that drew no fight in one group is passed over by that group only: it stands in the area of the next group too, and
// that group pulls it (a run-wide skip would have left it alive and counted that group as "already dead").
func TestAPassedOverMobIsStillTheNextGroupsToPull(t *testing.T) {
	g := newFake()
	for i, x := range []float32{30, 31, 40} {
		u := g.add(packet.GUID(301+i), 5, Point{x, 0, 0})
		u.noJoin = true
	}
	g.units[0].stuckPulls = 1
	d := NewDriver(mustPlan(t, overlappingGroupsPlan), g, Config{}, g.clock())
	if err := d.Run(context.Background(), 0); err != nil {
		t.Fatalf("%v\n%+v", err, d.Report())
	}
	if fmt.Sprint(g.targets) != fmt.Sprint([]packet.GUID{301, 302, 303, 301}) {
		t.Fatalf("targets %v: the second group must pull the mob the first passed over", g.targets)
	}
	if strings.Contains(notes(d), "already dead") {
		t.Fatalf("notes %q", notes(d))
	}
	// A refusal, in contrast, is the run's.
	g = newFake()
	for i, x := range []float32{30, 31, 40} {
		g.add(packet.GUID(301+i), 5, Point{x, 0, 0}).noJoin = true
	}
	g.refuseTargets = map[packet.GUID][]Refusal{301: {refuse("pullback", "unreachable", "x")}}
	d = NewDriver(mustPlan(t, overlappingGroupsPlan), g, Config{}, g.clock())
	if err := d.Run(context.Background(), 0); err != nil {
		t.Fatalf("%v\n%+v", err, d.Report())
	}
	for _, tg := range g.targets[1:] {
		if tg == 301 {
			t.Fatalf("targets %v: a refused mob is left alone by every group", g.targets)
		}
	}
}

// A group's radius keeps its members apart from a pack of the same entry next to it.
func TestAGroupRadiusSeparatesTwoRows(t *testing.T) {
	g := newFake()
	g.add(401, 5, Point{30, 0, 0})
	g.add(402, 5, Point{44, 0, 0})
	d := NewDriver(mustPlan(t, overlappingGroupsPlan), g, Config{}, g.clock())
	sp := fightSpec{pick: []uint32{5}, anchors: []Point{{30, 0, 0}}}
	if cs := d.candidates(sp, sp.pick); len(cs) != 2 {
		t.Fatalf("default radius: %d members", len(cs))
	}
	sp.radius = 10
	if cs := d.candidates(sp, sp.pick); len(cs) != 1 || cs[0].GUID != 401 {
		t.Fatalf("radius 10: %+v", cs)
	}

	p := mustPlan(t, `{"version":1,"name":"t","map":70,"entrance":{"x":0,"y":0,"z":0},"legs":[
 {"name":"a","to":{"x":10,"y":0,"z":0},"pull_groups":[{"entries":[5],"positions":[{"x":30,"y":0,"z":0}],"radius":10}]}]}`)
	if r := p.Legs[0].PullGroups[0].Radius; r != 10 {
		t.Fatalf("radius %v", r)
	}
	if _, err := Parse("t.json", []byte(`{"version":1,"name":"t","map":70,"entrance":{"x":0,"y":0,"z":0},"legs":[
 {"name":"a","to":{"x":10,"y":0,"z":0},"pull_groups":[{"entries":[5],"positions":[{"x":30,"y":0,"z":0}],"radius":-3}]}]}`)); err == nil || !strings.Contains(err.Error(), "radius") {
		t.Fatalf("%v", err)
	}
}

// The two Uldaman rows of Ambushers do not share members.
func TestUldamanAmbusherRowsAreSeparate(t *testing.T) {
	p := loadUldaman(t)
	var rows []PullGroup
	for _, g := range p.Legs[2].PullGroups {
		if len(g.Entries) > 0 && g.Entries[0] == 7175 {
			rows = append(rows, g)
		}
	}
	if len(rows) != 2 || rows[0].Radius == 0 || rows[1].Radius == 0 {
		t.Fatalf("%+v", rows)
	}
	for _, a := range rows[0].Positions {
		for _, b := range rows[1].Positions {
			if d := dist3(a, b); d <= float64(max(rows[0].Radius, rows[1].Radius)) {
				t.Errorf("%v and %v are %.1f yd apart, radii %v and %v", a, b, d, rows[0].Radius, rows[1].Radius)
			}
		}
	}
}

// A corpse of the entry that lies elsewhere (an earlier walk aggro) is no sign that this group is dead: the party walks to the
// group's positions first.
func TestACorpseElsewhereDoesNotMakeAGroupAlreadyDead(t *testing.T) {
	const plan = `{"version":1,"name":"t","map":70,"entrance":{"x":0,"y":0,"z":0},"legs":[
 {"name":"a","to":{"x":10,"y":0,"z":0},"pull_groups":[{"entries":[1],"positions":[{"x":200,"y":0,"z":0}]}]}]}`
	g := newFake()
	c := g.add(501, 1, Point{20, 0, 0})
	c.Dead, c.Health = true, 0
	g.add(502, 1, Point{200, 0, 0})
	d := NewDriver(mustPlan(t, plan), g, Config{}, g.clock())
	if err := d.Run(context.Background(), 0); err != nil {
		t.Fatalf("%v\n%+v", err, d.Report())
	}
	if fmt.Sprint(g.targets) != fmt.Sprint([]packet.GUID{502}) || strings.Contains(notes(d), "already dead") {
		t.Fatalf("targets %v notes %q", g.targets, notes(d))
	}
}

// A whisper names no intent: it answers the last order, so "X is evading" to a pullback skips the mob.
func TestAnEvadingWhisperAnswersThePullback(t *testing.T) {
	g := newFake()
	g.add(101, 1, Point{30, 0, 0}).noJoin = true
	g.add(102, 2, Point{32, 0, 0})
	g.refuseTargets = map[packet.GUID][]Refusal{101: {{Code: "evading", Message: "mob1 is evading"}}}
	d := NewDriver(mustPlan(t, onePackPlan), g, Config{}, g.clock())
	if err := d.Run(context.Background(), 0); err != nil {
		t.Fatalf("%v\n%+v", err, d.Report())
	}
	if fmt.Sprint(g.targets) != fmt.Sprint([]packet.GUID{101, 102}) {
		t.Fatalf("targets %v", g.targets)
	}
	if r := d.Report().Refusals; len(r) != 1 || r[0].Intent != "pullback" || r[0].Code != "evading" {
		t.Fatalf("refusals %+v", r)
	}
}

// A bot that refuses every assist order is not asked every poll: two immediate resends, then the 10 s cadence.
func TestARefusingBotIsNotAskedEveryPoll(t *testing.T) {
	g := newFake()
	g.add(101, 1, Point{30, 0, 0})
	g.add(102, 2, Point{32, 0, 0})
	g.dieIn = 60 * time.Second
	g.onChat = func(g *fakeGame, text string) {
		if text == ".bot action attack" {
			g.refusals = append(g.refusals, refuse("attack", "no-target", "Select a live non-bot target first."))
		}
	}
	d := NewDriver(mustPlan(t, onePackPlan), g, Config{}, g.clock())
	if err := d.Run(context.Background(), 0); err != nil {
		t.Fatalf("%v\n%+v", err, d.Report())
	}
	if n := g.count(".bot action attack"); n > 14 {
		t.Fatalf("%d attack orders in a 60 s fight", n)
	}
}

// An aura the leader already carried is not a misfire: the first aura that lands nowhere is "on nobody", not "on the leader".
func TestALeadersOwnAuraIsNotAMisfire(t *testing.T) {
	g, p := runFixture(t)
	g.auras = map[packet.GUID][]objects.Aura{leader: {{SpellID: 22888}}}
	g.failGMOn = map[packet.GUID]int{tank: 1}
	d := NewDriver(p, g, Config{Buffs: worldBuffs(t)[:1]}, g.clock())
	if err := d.Run(context.Background(), 0); err != nil {
		t.Fatalf("%v\n%+v", err, d.Report())
	}
	if !strings.Contains(notes(d), "landed on nobody") || strings.Contains(notes(d), "landed on the leader") {
		t.Fatalf("notes %q", notes(d))
	}
}

// A slow aura gets the whole BuffViewWait before the run is failed, counted from the first command.
func TestABuffGetsTheWholeViewWaitToShow(t *testing.T) {
	g, p := runFixture(t)
	g.auraDelay = 8 * time.Second
	d := NewDriver(p, g, Config{Buffs: worldBuffs(t)[:1]}, g.clock())
	if err := d.Run(context.Background(), 0); err != nil {
		t.Fatalf("%v\n%+v", err, d.Report())
	}

	g, p = runFixture(t)
	g.auraRefused = true
	t0 := g.now
	d = NewDriver(p, g, Config{Buffs: worldBuffs(t)[:1]}, g.clock())
	if err := d.Run(context.Background(), 0); err == nil || !strings.Contains(err.Error(), "had no effect") {
		t.Fatalf("%v", err)
	}
	if el := g.now.Sub(t0); el < 10*time.Second {
		t.Fatalf("failed after %v, before the 10 s wait", el)
	}
}
