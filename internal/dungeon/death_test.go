package dungeon

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/pjerra/turtle-agent/internal/events"
	"github.com/pjerra/turtle-agent/internal/objects"
	"github.com/pjerra/turtle-agent/internal/packet"
)

// The leader's death in a run (issue #80 step 3a), against the fake game and clock.

// killLeaderInFight kills the leader once, when the first pull is under way.
func killLeaderInFight(g *fakeGame, once *bool, healerGone ...bool) {
	prev := g.onTick
	g.onTick = func(g *fakeGame) {
		if prev != nil {
			prev(g)
		}
		if !*once && len(g.targets) >= 1 && g.units[0].InCombat && g.now.After(time.Unix(1_000_000, 0).Add(5*time.Second)) {
			*once = true
			g.kill()
			if len(healerGone) > 0 && healerGone[0] {
				g.party[1].Online = false // the priest drops out with the leader: nobody is left to resurrect it
			}
		}
	}
}

func phases(g *fakeGame) string {
	var ps []string
	for _, e := range g.events {
		if e.kind == events.KindDungeonLeader {
			ps = append(ps, fmt.Sprint(e.data["phase"]))
		}
	}
	return strings.Join(ps, ",")
}

func TestLeaderDeathAcceptsAHealersResurrection(t *testing.T) {
	g, p := runFixture(t)
	var once bool
	killLeaderInFight(g, &once)
	g.dead.offerAt = time.Unix(1_000_000, 0).Add(20 * time.Second)
	g.dead.offerLater = &Offer{Name: ""}
	d := NewDriver(p, g, Config{}, g.clock())
	if err := d.Run(context.Background(), 0); err != nil {
		t.Fatalf("%v\n%+v", err, d.Report())
	}
	rep := d.Report()
	if rep.State != "done" || rep.Wipes != 0 || rep.LeaderDeaths != 1 || rep.Resurrections != 1 || rep.CorpseRuns != 0 || rep.SecondsDead <= 0 || !rep.LeaderGM {
		t.Fatalf("%+v", rep)
	}
	if l := rep.Legs[0]; l.LeaderDeaths != 1 || l.Resurrections != 1 || l.SecondsDead <= 0 {
		t.Fatalf("%+v", l)
	}
	if g.dead.releases != 0 || g.dead.accepts != 1 {
		t.Fatalf("releases %d accepts %d", g.dead.releases, g.dead.accepts)
	}
	if got := phases(g); got != "died,resurrect_accepted,back" {
		t.Fatalf("phases %s", got)
	}
	// The party is gathered and the rest gate looked at it before the run went on.
	if g.count(".bot action follow") < 1 {
		t.Fatalf("chats %q", g.chats)
	}
	if len(rep.Deaths) != 1 || rep.Deaths[0].Name != "Lead" {
		t.Fatalf("%+v", rep.Deaths)
	}
}

func TestLeaderDeathWaitsForAHealerThenReleasesAndRunsToTheCorpse(t *testing.T) {
	g, p := runFixture(t)
	var once bool
	killLeaderInFight(g, &once)
	g.dead.graveyard, g.dead.graveMap = Point{3000, 0, 0}, 70
	g.dead.corpse = Corpse{Found: true, Map: 70, CorpseMap: 70, Pos: Point{20, 5, 0}}
	g.dead.reclaimWait = 30 * time.Second
	d := NewDriver(p, g, Config{}, g.clock())
	if err := d.Run(context.Background(), 0); err != nil {
		t.Fatalf("%v\n%+v", err, d.Report())
	}
	rep := d.Report()
	if rep.State != "done" || rep.LeaderDeaths != 1 || rep.CorpseRuns != 1 || rep.Resurrections != 0 || rep.Wipes != 0 {
		t.Fatalf("%+v", rep)
	}
	// A healer was alive, so the release waited the 60 s; the reclaim waited the 30 s delay after it.
	if rep.SecondsDead < 90 || rep.SecondsDead > 130 {
		t.Fatalf("dead %.0f s", rep.SecondsDead)
	}
	if g.dead.releases != 1 || g.dead.reclaims < 1 || g.dead.accepts != 0 || g.dead.triggers != 0 {
		t.Fatalf("%+v", g.dead)
	}
	if got := phases(g); !strings.HasPrefix(got, "died,released,corpse_run") || !strings.Contains(got, "reclaimed") || !strings.HasSuffix(got, "back") {
		t.Fatalf("phases %s", got)
	}
	// The ghost walked to the corpse.
	found := false
	for _, w := range g.walks {
		if w == (Point{20, 5, 0}) {
			found = true
		}
	}
	if !found {
		t.Fatalf("walks %v", g.walks)
	}
	// No GM command was needed.
	for _, c := range g.gms {
		if strings.HasPrefix(c, ".revive") || strings.HasPrefix(c, ".go") {
			t.Fatalf("gms %q", g.gms)
		}
	}
}

func TestLeaderDeathReleasesAtOnceWhenNoHealerLives(t *testing.T) {
	g, p := runFixture(t)
	var once bool
	killLeaderInFight(g, &once, true) // the priest drops out; the tank (a warrior) is no help
	g.dead.graveyard, g.dead.graveMap = Point{50, 0, 0}, 70
	g.dead.corpse = Corpse{Found: true, Map: 70, CorpseMap: 70, Pos: Point{20, 5, 0}}
	d := NewDriver(p, g, Config{}, g.clock())
	if err := d.Run(context.Background(), 0); err != nil {
		t.Fatalf("%v\n%+v", err, d.Report())
	}
	if rep := d.Report(); rep.CorpseRuns != 1 || rep.SecondsDead > 40 {
		t.Fatalf("%.0f s dead\n%+v", rep.SecondsDead, rep)
	}
}

func TestLeaderCorpseInsideTheDungeonGoesInThroughThePortal(t *testing.T) {
	g, p := runFixture(t)
	p.EntranceTrigger = 286
	var once bool
	killLeaderInFight(g, &once, true)
	// The graveyard is outside (map 1); the corpse query names the entrance on map 1 and the dungeon's map 70.
	g.dead.graveyard, g.dead.graveMap = Point{900, 0, 0}, 1
	g.dead.corpse = Corpse{Found: true, Map: 1, CorpseMap: 70, Pos: Point{1000, 0, 0}, Inside: true}
	g.dead.portalOK, g.dead.portalTo = true, Point{0, 0, 0}
	d := NewDriver(p, g, Config{}, g.clock())
	if err := d.Run(context.Background(), 0); err != nil {
		t.Fatalf("%v\n%+v", err, d.Report())
	}
	if rep := d.Report(); rep.CorpseRuns != 1 || rep.State != "done" {
		t.Fatalf("%+v", rep)
	}
	if fmt.Sprint(g.dead.triggerIDs) != "[286]" || g.dead.reclaims != 0 {
		t.Fatalf("triggers %v reclaims %d", g.dead.triggerIDs, g.dead.reclaims)
	}
	if got := phases(g); !strings.Contains(got, "portal") {
		t.Fatalf("phases %s", got)
	}

	// A plan without the trigger cannot do it: the run fails with a clear reason.
	g, p = runFixture(t)
	once = false
	killLeaderInFight(g, &once, true)
	g.dead.graveyard, g.dead.graveMap = Point{900, 0, 0}, 1
	g.dead.corpse = Corpse{Found: true, Map: 1, CorpseMap: 70, Pos: Point{1000, 0, 0}, Inside: true}
	d = NewDriver(p, g, Config{}, g.clock())
	err := d.Run(context.Background(), 0)
	if err == nil || !strings.Contains(err.Error(), "no entrance_trigger") || d.Report().State != "failed" {
		t.Fatalf("%v", err)
	}
}

func TestCorpseRunIsBounded(t *testing.T) {
	g, p := runFixture(t)
	var once bool
	killLeaderInFight(g, &once, true)
	g.dead.graveyard, g.dead.graveMap = Point{3000, 0, 0}, 70
	g.dead.corpse = Corpse{Found: true, Map: 70, CorpseMap: 70, Pos: Point{20, 5, 0}}
	g.walkNeverMoves = true
	d := NewDriver(p, g, Config{CorpseRunTimeout: 3 * time.Minute}, g.clock())
	err := d.Run(context.Background(), 0)
	if err == nil || !strings.Contains(err.Error(), "corpse run: gave up after 3 min") || !strings.Contains(err.Error(), "the leader could not come back") {
		t.Fatalf("%v", err)
	}
	rep := d.Report()
	if rep.State != "failed" || rep.Wipes != 0 || rep.LeaderDeaths != 1 || !strings.Contains(phases(g), "gave_up") {
		t.Fatalf("%+v %s", rep, phases(g))
	}
}

func TestLeaderResurrectionOfferedToAGhostOnTheWay(t *testing.T) {
	g, p := runFixture(t)
	var once bool
	killLeaderInFight(g, &once, true)
	g.dead.graveyard, g.dead.graveMap = Point{3000, 0, 0}, 70
	g.dead.corpse = Corpse{Found: true, Map: 70, CorpseMap: 70, Pos: Point{20, 5, 0}}
	g.dead.reclaimWait = 5 * time.Minute // the corpse could not be reclaimed soon
	d := NewDriver(p, g, Config{}, g.clock())
	// The offer comes while the ghost waits out the delay.
	prev := g.onTick
	g.onTick = func(g *fakeGame) {
		prev(g)
		if g.self.Ghost && g.dead.offer == nil && g.now.After(g.dead.releaseAt.Add(40*time.Second)) {
			g.dead.offer = &Offer{}
		}
	}
	if err := d.Run(context.Background(), 0); err != nil {
		t.Fatalf("%v\n%+v", err, d.Report())
	}
	if rep := d.Report(); rep.Resurrections != 1 || rep.CorpseRuns != 0 || g.dead.reclaims != 0 {
		t.Fatalf("%+v", rep)
	}
}

func TestLeaderDeathsAreBounded(t *testing.T) {
	g, p := runFixture(t)
	// The leader dies again half a minute after every resurrection; each time a resurrection waits.
	var lastKill time.Time
	g.onTick = func(g *fakeGame) {
		if len(g.targets) >= 1 && !g.self.Dead() && g.now.Sub(lastKill) > 30*time.Second {
			lastKill = g.now
			g.kill()
			g.dead.offer = &Offer{}
		}
	}
	d := NewDriver(p, g, Config{MaxLeaderDeaths: 3}, g.clock())
	err := d.Run(context.Background(), 0)
	if err == nil || !strings.Contains(err.Error(), "the leader has died 4 times") || d.Report().LeaderDeaths != 3 {
		t.Fatalf("%v %+v", err, d.Report())
	}
}

func TestOffersThatDoNotReviveAreGivenUpOn(t *testing.T) {
	g, p := runFixture(t)
	var once bool
	killLeaderInFight(g, &once, true)
	g.dead.offer = &Offer{}
	g.dead.acceptErr = errors.New("the offer ran out")
	g.dead.graveyard, g.dead.graveMap = Point{50, 0, 0}, 70
	g.dead.corpse = Corpse{Found: true, Map: 70, CorpseMap: 70, Pos: Point{20, 5, 0}}
	d := NewDriver(p, g, Config{}, g.clock())
	if err := d.Run(context.Background(), 0); err != nil {
		t.Fatalf("%v\n%+v", err, d.Report())
	}
	// Three answers in the wait for a resurrection, three more on the corpse run; then the corpse was reclaimed.
	if rep := d.Report(); rep.CorpseRuns != 1 || g.dead.accepts < 3 || g.dead.accepts > 6 {
		t.Fatalf("accepts %d %+v", g.dead.accepts, rep)
	}
}

func TestLeaderDyingOnAWalkDoesNotUseUpATry(t *testing.T) {
	g, p := runFixture(t)
	first := true
	g.dead.offer = &Offer{}
	g.walkHook = func(ctx context.Context, g *fakeGame, to Point) (WalkResult, error) {
		if first {
			first = false
			g.kill()
			return WalkResult{Result: "partial", Reason: "died", DistLeft: 5}, nil
		}
		g.pos = to
		return WalkResult{Result: "arrived"}, nil
	}
	d := NewDriver(p, g, Config{WalkAttempts: 1}, g.clock())
	if err := d.Run(context.Background(), 0); err != nil {
		t.Fatalf("%v\n%+v", err, d.Report())
	}
	if rep := d.Report(); rep.LeaderDeaths != 1 || rep.Resurrections != 1 {
		t.Fatalf("%+v", rep)
	}
	if g.walks[0] != g.walks[1] {
		t.Fatalf("the walk is made again: %v", g.walks)
	}
}

// The whole party dead: a wipe ends the run, unless recover_wipe lets everybody run back to their corpses.
func wipeFixture(t *testing.T) (*fakeGame, *Plan) {
	g, p := runFixture(t)
	g.dead.graveyard, g.dead.graveMap = Point{3000, 0, 0}, 70
	g.dead.corpse = Corpse{Found: true, Map: 70, CorpseMap: 70, Pos: Point{20, 5, 0}}
	wiped := false
	g.onTick = func(g *fakeGame) {
		if !wiped && len(g.targets) >= 1 && g.units[0].InCombat && g.now.After(time.Unix(1_000_000, 0).Add(5*time.Second)) {
			wiped = true
			g.kill()
			g.party[0].Dead, g.party[1].Dead = true, true
		}
	}
	g.onChat = func(g *fakeGame, text string) {
		if text == ".bot action corpse run" {
			if g.revAt == nil {
				g.revAt = map[string]time.Time{}
			}
			for _, m := range g.party {
				if m.Dead {
					g.revAt[m.Name] = g.now.Add(25 * time.Second)
				}
			}
		}
	}
	return g, p
}

func TestWipeEndsTheRunByDefault(t *testing.T) {
	g, p := wipeFixture(t)
	d := NewDriver(p, g, Config{}, g.clock())
	err := d.Run(context.Background(), 0)
	if err == nil || err.Error() != "wipe" || d.Report().Wipes != 1 || g.dead.releases != 0 {
		t.Fatalf("%v %+v", err, d.Report())
	}
}

func TestRecoverWipeRunsEverybodyBackAndTheRunGoesOn(t *testing.T) {
	g, p := wipeFixture(t)
	d := NewDriver(p, g, Config{RecoverWipe: true, GM: GMNone}, g.clock())
	if err := d.Run(context.Background(), 0); err != nil {
		t.Fatalf("%v\n%+v\n%q", err, d.Report(), g.chats)
	}
	rep := d.Report()
	if rep.State != "done" || rep.Wipes != 1 || rep.LeaderDeaths != 1 || rep.CorpseRuns != 1 || rep.LeaderGM || len(rep.Shortcuts) != 0 {
		t.Fatalf("%+v", rep)
	}
	if g.count(".bot action release") != 1 || g.count(".bot action corpse run") < 1 || len(g.gms) != 0 {
		t.Fatalf("chats %q gms %q", g.chats, g.gms)
	}
	// Orders in the right sequence: release before the corpse run.
	ri, ci := -1, -1
	for i, c := range g.chats {
		if c == ".bot action release" && ri < 0 {
			ri = i
		}
		if c == ".bot action corpse run" && ci < 0 {
			ci = i
		}
	}
	if ri < 0 || ci < ri {
		t.Fatalf("order %q", g.chats)
	}
	// The run resumed: the pack was fought again to the end.
	if rep.Legs[0].Result != "done" || rep.Legs[1].Result != "done" {
		t.Fatalf("%+v", rep.Legs)
	}
}

func TestRecoverWipeIsBounded(t *testing.T) {
	g, p := wipeFixture(t)
	// The first pull wipes the party (wipeFixture) and the second one does again.
	wiped := false
	g.onTick = func(g *fakeGame) {
		trigger := (!wiped && g.units[0].InCombat) || (wiped && g.units[2].InCombat && !g.self.Dead())
		if len(g.targets) >= 1 && trigger && g.now.After(time.Unix(1_000_000, 0).Add(5*time.Second)) {
			wiped = true
			g.kill()
			g.party[0].Dead, g.party[1].Dead = true, true
		}
	}
	d := NewDriver(p, g, Config{RecoverWipe: true, MaxWipeRecoveries: 1}, g.clock())
	err := d.Run(context.Background(), 0)
	if err == nil || err.Error() != "wipe" || !strings.Contains(strings.Join(d.Report().Notes, "|"), "after 1 recoveries") || d.Report().Wipes != 2 {
		t.Fatalf("%v %+v", err, d.Report())
	}
}

// Without a GM: no GM command at all, and the options that need one are refused.
func TestNonGMRunSendsNoGMCommand(t *testing.T) {
	g, p := runFixture(t)
	d := NewDriver(p, g, Config{GM: GMNone, Shortcuts: true}, g.clock())
	if err := d.Run(context.Background(), 0); err != nil {
		t.Fatalf("%v\n%+v", err, d.Report())
	}
	rep := d.Report()
	if len(g.gms) != 0 || len(g.teleports) != 0 || rep.LeaderGM || len(rep.Shortcuts) != 0 || rep.State != "done" {
		t.Fatalf("gms %q %+v", g.gms, rep)
	}

	for name, cfg := range map[string]Config{
		"teleport": {GM: GMNone, Teleport: true},
		"buffs":    {GM: GMNone, Buffs: []Buff{{22888, "Rallying Cry of the Dragonslayer"}}},
	} {
		g, p = runFixture(t)
		err := NewDriver(p, g, cfg, g.clock()).Run(context.Background(), 0)
		if err == nil || !strings.Contains(err.Error(), "GM account") || len(g.gms) != 0 || len(g.chats) != 0 {
			t.Fatalf("%s: %v gms %q chats %q", name, err, g.gms, g.chats)
		}
	}

	// A dead member at a rest gate is not revived by GM: the run says so, and names recover_wipe.
	g, p = runFixture(t)
	g.party[1].Dead = true
	g.afterGate = nil
	err := NewDriver(p, g, Config{GM: GMNone, Shortcuts: true}, g.clock()).Run(context.Background(), 0)
	if err == nil || !strings.Contains(err.Error(), "Heal is dead and GM shortcuts are off") || len(g.gms) != 0 {
		t.Fatalf("%v %q", err, g.gms)
	}
}

func TestNonGMDeadMemberRecoversWithRecoverWipe(t *testing.T) {
	g, p := runFixture(t)
	g.party[1].Dead = true
	g.onChat = func(g *fakeGame, text string) {
		if text == ".bot action corpse run" {
			g.revAt = map[string]time.Time{"Heal": g.now.Add(20 * time.Second)}
		}
	}
	d := NewDriver(p, g, Config{GM: GMNone, RecoverWipe: true}, g.clock())
	if err := d.Run(context.Background(), 0); err != nil {
		t.Fatalf("%v\n%+v", err, d.Report())
	}
	if len(g.gms) != 0 || g.count(".bot action release") != 1 || g.count(".bot action corpse run") < 1 {
		t.Fatalf("gms %q chats %q", g.gms, g.chats)
	}
}

func TestProbeSettlesTheModeFromTheAccountLevel(t *testing.T) {
	all := GMCaps{Visible: true, Teleport: true, Revive: true, Buffs: true}
	cases := []struct {
		name     string
		level    int
		known    bool
		err      error
		wantGM   bool
		wantCaps GMCaps
		wantNote string
	}{
		{"a developer", 3, true, nil, true, all, "account level is 3"},
		{"an administrator", 4, true, nil, true, all, "account level is 4"},
		{"a moderator", 2, true, nil, true, GMCaps{Visible: true, Teleport: true}, "account level is 2"},
		{"an observer", 1, true, nil, true, GMCaps{Teleport: true}, "needs account level 2; the leader's is 1: not sent"},
		{"a player", 0, true, nil, false, GMCaps{}, "account level is 0 (a player)"},
		// No answer (or a broken probe) is a player's run, never a GM's: the note says how to override.
		{"no answer", 0, false, nil, false, GMCaps{}, "did not answer .account; the leader is taken for a player and the run sends no GM command (pass gm: true"},
		{"probe error", 0, false, errors.New("not in world"), false, GMCaps{}, "could not tell the leader's account level (not in world); the leader is taken for a player"},
	}
	for _, c := range cases {
		g, p := runFixture(t)
		g.dead.probeLevel, g.dead.probeKnown, g.dead.probeErr = c.level, c.known, c.err
		d := NewDriver(p, g, Config{GM: GMProbe, Shortcuts: true}, g.clock())
		if err := d.Run(context.Background(), 0); err != nil {
			t.Fatalf("%s: %v", c.name, err)
		}
		rep := d.Report()
		if rep.LeaderGM != c.wantGM || rep.GMCaps != c.wantCaps || g.dead.probes != 1 || !strings.Contains(strings.Join(rep.Notes, "|"), c.wantNote) {
			t.Fatalf("%s: %+v", c.name, rep)
		}
		if c.known && (rep.AccountLevel == nil || *rep.AccountLevel != c.level) || !c.known && rep.AccountLevel != nil {
			t.Fatalf("%s: level %v", c.name, rep.AccountLevel)
		}
		if got := len(g.gms) == 1 && g.gms[0] == ".gm visible on"; got != c.wantCaps.Visible || (!c.wantCaps.Visible && len(g.gms) != 0) {
			t.Fatalf("%s: gms %q", c.name, g.gms)
		}
	}

	// Teleport (.go xyz) needs level 1: refused for a player, with the override named when the level is unknown; an
	// observer teleports.
	for _, c := range []struct {
		name         string
		level        int
		known        bool
		wantErr      string
		wantOverride bool
	}{
		{"player", 0, true, "teleport needs a GM account (.go xyz, level 1)", false},
		{"unknown", 0, false, "teleport needs a GM account (.go xyz, level 1)", true},
		{"observer", 1, true, "", false},
	} {
		g, p := runFixture(t)
		g.mapID = 1
		g.dead.probeLevel, g.dead.probeKnown = c.level, c.known
		err := NewDriver(p, g, Config{GM: GMProbe, Teleport: true}, g.clock()).Run(context.Background(), 1)
		if c.wantErr == "" {
			if err != nil || len(g.teleports) != 1 {
				t.Fatalf("%s: %v %v", c.name, err, g.teleports)
			}
			continue
		}
		if err == nil || !strings.Contains(err.Error(), c.wantErr) || strings.Contains(err.Error(), "pass gm: true") != c.wantOverride || len(g.teleports) != 0 || len(g.gms) != 0 {
			t.Fatalf("%s: %v", c.name, err)
		}
	}

	// .aura needs developer level: buffs are refused below it, before anything is sent but .gm visible on.
	for level, wantErr := range map[int]string{0: "buffs need a GM account (.aura, level 3)", 1: "buffs need account level 3 (.aura); the leader's account level is 1",
		2: "buffs need account level 3 (.aura); the leader's account level is 2", 3: ""} {
		g, p := runFixture(t)
		g.dead.probeLevel, g.dead.probeKnown = level, true
		err := NewDriver(p, g, Config{GM: GMProbe, Buffs: []Buff{{22888, "Rallying Cry of the Dragonslayer"}}}, g.clock()).Run(context.Background(), 0)
		if wantErr != "" && (err == nil || !strings.Contains(err.Error(), wantErr) || gmsWith(g, ".aura") != 0) {
			t.Fatalf("level %d: %v", level, err)
		}
		if wantErr == "" && err != nil {
			t.Fatalf("level %d: %v", level, err)
		}
	}
}

// .revive needs developer level: a moderator (probed or asserted) does not revive by GM; its dead members go the
// non-GM way (recover_wipe), and without recover_wipe the run says so instead of sending .revive.
func TestReviveNeedsDeveloperLevel(t *testing.T) {
	for _, cfg := range []Config{{GM: GMProbe, Shortcuts: true}, {GM: GMAssumed, GMLevel: 2, Shortcuts: true}} {
		g, p := runFixture(t)
		g.dead.probeLevel, g.dead.probeKnown = 2, true
		g.party[1].Dead = true
		g.afterGate = nil
		d := NewDriver(p, g, cfg, g.clock())
		err := d.Run(context.Background(), 0)
		if err == nil || !strings.Contains(err.Error(), "Heal is dead and GM shortcuts are off") || gmsWith(g, ".revive") != 0 ||
			!strings.Contains(strings.Join(d.Report().Notes, "|"), "GM revives need account level 3") || d.Report().GMCaps.Revive {
			t.Fatalf("%+v: %v %q", cfg, err, g.gms)
		}

		g, p = runFixture(t)
		g.dead.probeLevel, g.dead.probeKnown = 2, true
		g.party[1].Dead = true
		g.onChat = func(g *fakeGame, text string) {
			if text == ".bot action corpse run" {
				g.revAt = map[string]time.Time{"Heal": g.now.Add(20 * time.Second)}
			}
		}
		cfg.RecoverWipe = true
		d = NewDriver(p, g, cfg, g.clock())
		if err := d.Run(context.Background(), 0); err != nil || gmsWith(g, ".revive") != 0 || g.count(".bot action corpse run") < 1 {
			t.Fatalf("%+v: %v gms %q", cfg, err, g.gms)
		}
	}
	// An asserted GM with no gm_level is a developer: it revives.
	g, p := runFixture(t)
	g.party[1].Dead = true
	d := NewDriver(p, g, Config{Shortcuts: true}, g.clock())
	if err := d.Run(context.Background(), 0); err != nil || gmsWith(g, ".revive Heal") != 1 || d.Report().GMCaps != (GMCaps{Visible: true, Teleport: true, Revive: true, Buffs: true}) {
		t.Fatalf("%v %q %+v", err, g.gms, d.Report().GMCaps)
	}
}

func TestResurrectionSicknessIsNotedOrWaitedOut(t *testing.T) {
	rem := int64(120000)
	for _, wait := range []bool{false, true} {
		g, p := runFixture(t)
		var once bool
		killLeaderInFight(g, &once, true)
		g.dead.graveyard, g.dead.graveMap = Point{50, 0, 0}, 70
		g.dead.corpse = Corpse{Found: true, Map: 70, CorpseMap: 70, Pos: Point{20, 5, 0}}
		sick := true
		prev := g.onTick
		g.onTick = func(g *fakeGame) {
			prev(g)
			if sick && !g.self.Dead() && g.dead.reclaims > 0 {
				g.auras = map[packet.GUID][]objects.Aura{leader: auraNamed("Resurrection Sickness", &rem)}
			}
			if g.auras[leader] != nil && g.now.After(g.dead.reclaimAt.Add(100*time.Second)) {
				g.auras, sick = nil, false
			}
		}
		d := NewDriver(p, g, Config{WaitSickness: wait}, g.clock())
		if err := d.Run(context.Background(), 0); err != nil {
			t.Fatalf("wait %v: %v\n%+v", wait, err, d.Report())
		}
		notes := strings.Join(d.Report().Notes, "|")
		if !wait && !strings.Contains(notes, "resurrection sickness (120 s left); not waited out") {
			t.Fatalf("notes %s", notes)
		}
		if wait && !strings.Contains(notes, "resurrection sickness waited out") {
			t.Fatalf("notes %s", notes)
		}
	}
}

// The leader's brain is off while the leader is dead or a ghost (a ghost has 1 health, so the brain cannot tell by itself in
// every build), and comes back at the rest gate after.
func TestTheBrainIsStoppedForTheCorpseRun(t *testing.T) {
	g, p := runFixture(t)
	var once bool
	killLeaderInFight(g, &once, true)
	g.dead.graveyard, g.dead.graveMap = Point{50, 0, 0}, 70
	g.dead.corpse = Corpse{Found: true, Map: 70, CorpseMap: 70, Pos: Point{20, 5, 0}}
	br := &fakeBrain{}
	ranDead := false
	var seenDead time.Time
	prev := g.onTick
	g.onTick = func(g *fakeGame) {
		prev(g)
		if !g.self.Dead() {
			seenDead = time.Time{}
			return
		}
		if seenDead.IsZero() {
			seenDead = g.now // the tick the leader dies in: the driver has not looked yet
			return
		}
		if br.running {
			ranDead = true
		}
	}
	d := NewDriver(p, g, Config{Brain: br, BrainName: "frost_mage"}, g.clock())
	if err := d.Run(context.Background(), 0); err != nil {
		t.Fatalf("%v\n%+v", err, d.Report())
	}
	if ranDead {
		t.Fatalf("the brain ran while the leader was dead or a ghost: %v", br.log)
	}
	if got := strings.Join(br.log, ","); !strings.Contains(got, "stop,rest=true") && !strings.Contains(got, "start,rest=true") {
		t.Fatalf("%s", got)
	}
}

// gmsWith counts the GM commands sent that start with prefix.
func gmsWith(g *fakeGame, prefix string) int {
	n := 0
	for _, c := range g.gms {
		if strings.HasPrefix(c, prefix) {
			n++
		}
	}
	return n
}
