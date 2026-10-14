package dungeon

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/pjerra/turtle-agent/internal/tbm"
)

// ZF-2 run 3i and ZF-1 run 5 (2026-10-14): the live fixes of the Zul'Farrak road. A party named by lead_bot/bots (no prepare hire) had no cc
// bot, so the nine-mob pull of 1642 902 got no Polymorph; the Shadowhunters' patrol was out of view when the pull began; bots left the
// group and the run walked on with three; dungeon_stop lost the instance.

func rosterReplies(g *botFake, classes map[string]int) {
	lines := []string{"TBM:ROSTER_BEGIN|3"}
	guid := 10
	for _, n := range []string{"Tanky", "Healy", "Magey"} {
		guid++
		lines = append(lines, "TBM:ROSTER|"+itoa(guid)+"|"+n+"|"+itoa(classes[n])+"|online|1|Dun Morogh")
	}
	g.replies["roster"] = append(lines, "TBM:ROSTER_END")
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	s := ""
	for ; n > 0; n /= 10 {
		s = string(rune('0'+n%10)) + s
	}
	return s
}

func bigPackPlan(g *fakeRemote, n int) *Plan {
	entries := []uint32{}
	for i := 0; i < n; i++ {
		e := uint32(300)
		if i == 0 {
			e = 299
		}
		g.addMob(e, Point{30 + float32(i), float32(i % 2), 0})
		entries = append(entries, e)
	}
	return remotePlan(Leg{Name: "a", To: Point{0, 0, 0}, PullGroups: []PullGroup{{Entries: entries, Positions: []Point{{30, 0, 0}}}}})
}

// No cc_bot given: the mage the roster names takes the big-pack CC, the Polymorph goes out before the pull.
func TestRemoteNoCCBotTakesTheMageOfTheRoster(t *testing.T) {
	g := &botFake{fakeRemote: newFakeRemote(t), replies: map[string][]string{}}
	rosterReplies(g, map[string]int{"Tanky": 1, "Healy": 5, "Magey": 8})
	plan := bigPackPlan(g.fakeRemote, 5)
	rep, err := runRemote(t, g, plan, opts(), nil) // opts() names no cc bot
	if err != nil {
		t.Fatalf("%v %v", err, rep.Notes)
	}
	cc, pb := g.indexOf("cmd cc"), g.indexOf("cmd pullback")
	if cc < 0 || cc > pb || !strings.HasSuffix(g.orders[cc], " Magey") {
		t.Fatalf("cc %d pullback %d %v", cc, pb, g.orders)
	}
	if r := rep.Remote; r.BigPackCC != 1 || !strings.Contains(notesOf(rep), "Magey, the mage of the party (the roster), takes the big-pack and reactive CC") {
		t.Fatalf("%+v %s", r, notesOf(rep))
	}
}

// What an earlier run knew of the hires (kept binding) names the mage without a roster; a cc_bot the caller gave is not replaced.
func TestRemoteNoCCBotTakesTheMageOfTheHireMemory(t *testing.T) {
	g := newFakeRemote(t)
	plan := bigPackPlan(g, 5)
	o := opts()
	o.HireMemory = map[string]tbm.Who{"tanky": {Class: "warrior"}, "healy": {Class: "priest"}, "magey": {Class: "mage"}}
	rep, err := runRemote(t, g, plan, o, nil)
	if err != nil || rep.Remote.BigPackCC != 1 || !strings.HasSuffix(g.orders[g.indexOf("cmd cc")], " Magey") {
		t.Fatalf("%v %v %+v", err, g.orders, rep.Remote)
	}
	g2 := newFakeRemote(t)
	plan2 := bigPackPlan(g2, 5)
	o2 := o
	o2.CCBot = "Healy"
	if _, err := runRemote(t, g2, plan2, o2, nil); err != nil || !strings.HasSuffix(g2.orders[g2.indexOf("cmd cc")], " Healy") {
		t.Fatalf("%v %v", err, g2.orders)
	}
}

// A party with no mage says so; the pack is pulled as it is.
func TestRemoteNoMageInThePartyNoCC(t *testing.T) {
	g := &botFake{fakeRemote: newFakeRemote(t), replies: map[string][]string{}}
	rosterReplies(g, map[string]int{"Tanky": 1, "Healy": 5, "Magey": 9})
	rep, err := runRemote(t, g, bigPackPlan(g.fakeRemote, 5), opts(), nil)
	if err != nil || g.indexOf("cmd cc") >= 0 || !strings.Contains(notesOf(rep), "no mage is known in the party") {
		t.Fatalf("%v %v %s", err, g.orders, notesOf(rep))
	}
}

// ZF-2 run 3i: the patrol is out of view when the pull begins. The party watches for it (patrol_locates) before the pull; a patrol that
// walks into view is then waited for as before.
func TestRemoteLooksForAPatrolOutOfView(t *testing.T) {
	g := newFakeRemote(t)
	g.addMob(299, Point{60, 0, 0})
	p := g.addMob(301, Point{300, 0, 0})
	g.sight = 120 // the bots see 120 yd round the lead bot: the patrol at 300 is out of view
	g.onTick = walker(p, 80, 300, 5, -1)
	pat := &Patrol{Entries: []uint32{301}, Path: []Point{{300, 0, 0}, {80, 0, 0}}, PeriodS: 88}
	plan := remotePlan(Leg{Name: "a", To: Point{0, 0, 0}, PullGroups: []PullGroup{{Entries: []uint32{299}, Positions: []Point{{60, 0, 0}}, Patrol: pat}}})
	rep, err := runRemote(t, g, plan, opts(), nil)
	if err != nil {
		t.Fatalf("%v %v", err, rep.Notes)
	}
	r := rep.Remote
	if r.PatrolLocates != 1 || r.PatrolLocateS < 20 || r.PatrolWaits != 1 {
		t.Fatalf("%+v\n%s", r, notesOf(rep))
	}
}

// A patrol that never shows is looked for once, for its round plus 10 s at most, then the pull goes; patrol_locate_s 0 turns the look off.
func TestRemotePatrolNeverInViewCostsOneRound(t *testing.T) {
	run := func(mod func(d *RemoteDriver)) (Report, error) {
		g := newFakeRemote(t)
		g.addMob(299, Point{60, 0, 0})
		pat := &Patrol{Entries: []uint32{301}, Path: []Point{{300, 0, 0}, {80, 0, 0}}, PeriodS: 88}
		plan := remotePlan(Leg{Name: "a", To: Point{0, 0, 0}, PullGroups: []PullGroup{{Entries: []uint32{299}, Positions: []Point{{60, 0, 0}}, Patrol: pat}}})
		return runRemoteTx(t, g, plan, opts(), mod)
	}
	rep, err := run(nil)
	if err != nil || rep.Remote.PatrolLocates != 1 || rep.Remote.PatrolLocateS < 95 || rep.Remote.PatrolLocateS > 110 || !strings.Contains(notesOf(rep), "did not come into view in 98 s") {
		t.Fatalf("%v %+v\n%s", err, rep.Remote, notesOf(rep))
	}
	rep, err = run(func(d *RemoteDriver) { d.tx.PatrolLocate = 0 })
	if err != nil || rep.Remote.PatrolLocates != 0 {
		t.Fatalf("%v %+v", err, rep.Remote)
	}
}

// ZF-2 run 3b: the module removes a bot from the group; the run stops at the next calm moment instead of walking on with fewer.
func TestRemoteStopsWhenABotLeavesTheGroup(t *testing.T) {
	g := newFakeRemote(t)
	g.addMob(299, Point{30, 0, 0})
	g.addMob(299, Point{150, 0, 0})
	g.left = map[string]bool{}
	g.onTick = func(g *fakeRemote) {
		if g.mobsKilled >= 1 {
			g.left["Magey"] = true
		}
	}
	plan := remotePlan(Leg{Name: "a", To: Point{0, 0, 0}, PullGroups: []PullGroup{{Entries: []uint32{299}, Positions: []Point{{30, 0, 0}}}, {Entries: []uint32{299}, Positions: []Point{{150, 0, 0}}}}})
	rep, err := runRemote(t, g, plan, opts(), nil)
	if err == nil || rep.State != "failed" || !strings.Contains(err.Error(), "party short: the party has 2 of 3 bots, Magey left the group") {
		t.Fatalf("%v %s", err, rep.State)
	}
	if g.mobsKilled != 1 {
		t.Fatalf("the run went on after the bot left: %d kills", g.mobsKilled)
	}
}

// The checks on their own: a short party is tolerated for partyShortHold and in a fight; a dead bot stops a walk or a pull when out of combat.
func TestPartyIntactAndRequireLiving(t *testing.T) {
	g := newFakeRemote(t)
	d := NewRemoteDriver(remotePlan(Leg{Name: "a", To: Point{0, 0, 0}}), g, DefaultConfig(), opts(), g.clock())
	d.curLeg, d.haveFr, d.wantParty = 0, true, 3
	d.opt.Lead, d.opt.Bots = "Tanky", []string{"Healy", "Magey"}
	full := []tbm.PartyRec{{Name: "Tanky", State: "n"}, {Name: "Healy", State: "n"}, {Name: "Magey", State: "n"}}
	d.fr.Party = full[:2]
	if err := d.partyIntact(); err != nil {
		t.Fatalf("first look: %v", err)
	}
	g.now = g.now.Add(partyShortHold + time.Second)
	d.fr.At = g.now
	d.fr.Party = []tbm.PartyRec{{Name: "Tanky", State: "c"}, {Name: "Healy", State: "c"}}
	if err := d.partyIntact(); err != nil {
		t.Fatalf("in combat: %v", err)
	}
	d.fr.Party = full[:2]
	if err := d.partyIntact(); !errors.Is(err, errPartyShort) || !strings.Contains(err.Error(), "Magey left the group") {
		t.Fatalf("calm: %v", err)
	}
	d.fr.Party = full
	if err := d.partyIntact(); err != nil {
		t.Fatalf("whole again: %v", err)
	}
	d.fr.Party = []tbm.PartyRec{{Name: "Tanky", State: "n"}, {Name: "Healy", State: "n", Dead: true}, {Name: "Magey", State: "n"}}
	if err := d.requireLiving(); !errors.Is(err, errPartyShort) || !strings.Contains(err.Error(), "1 of 3 bots are dead (Healy)") {
		t.Fatalf("dead: %v", err)
	}
	d.fr.Party[0].State, d.fr.Party[2].State = "c", "c"
	if err := d.requireLiving(); err != nil {
		t.Fatalf("a death in a fight is the fight's: %v", err)
	}
}

// dungeon_pause: a run stopped after Pause keeps the binding (no unbind, the report says why); a plain stop unbinds.
func TestRemotePausedRunKeepsTheBinding(t *testing.T) {
	for _, pause := range []bool{false, true} {
		g := newFakeRemote(t)
		plan := remotePlan(Leg{Name: "a", To: Point{0, 0, 0}, Steps: []Step{{WaitFor: &WaitFor{HostilesGone: &HostilesGone{Radius: 30}, TimeoutS: 600}}}})
		g.addMob(299, Point{10, 0, 0}).inert = true
		ctx, cancel := context.WithCancel(context.Background())
		d := NewRemoteDriver(plan, g, DefaultConfig(), opts(), g.clock())
		start := g.now
		g.onTick = func(g *fakeRemote) {
			if g.now.Sub(start) > 40*time.Second {
				if pause {
					d.Pause()
				}
				cancel()
			}
		}
		err := d.Run(ctx, 0)
		rep, _ := d.Status()
		if err == nil || rep.State != "stopped" {
			t.Fatalf("pause %v: %v %s", pause, err, rep.State)
		}
		if pause && (g.unbinds != 0 || !strings.HasPrefix(rep.Remote.UnbindVia, "kept (the run was paused")) {
			t.Fatalf("paused: unbinds %d via %q", g.unbinds, rep.Remote.UnbindVia)
		}
		if !pause && g.unbinds != 1 {
			t.Fatalf("stopped: unbinds %d", g.unbinds)
		}
	}
}

// ZF-2 run 4: `.bot roster` lists none of the bound hires (ROSTER_BEGIN|0) and the failed prepare run left no hire memory, so seven resumes
// had no cc bot. The class the agent learned of the hires in any earlier run (ClassMemory) names the mage.
func TestRemoteNoCCBotTakesTheMageOfTheClassMemoryWhenTheRosterIsEmpty(t *testing.T) {
	g := &botFake{fakeRemote: newFakeRemote(t), replies: map[string][]string{"roster": {"TBM:ROSTER_BEGIN|0", "TBM:ROSTER_END"}}}
	plan := bigPackPlan(g.fakeRemote, 5)
	o := opts()
	o.ClassMemory = map[string]string{"tanky": "warrior", "healy": "priest", "magey": "mage"}
	rep, err := runRemote(t, g, plan, o, nil)
	if err != nil || rep.Remote.BigPackCC != 1 || !strings.HasSuffix(g.orders[g.indexOf("cmd cc")], " Magey") {
		t.Fatalf("%v %v %+v\n%s", err, g.orders, rep.Remote, notesOf(rep))
	}
}

// With no memory and no roster at all, the spells the bots cast name the mage: the first big pack after it has cast gets its CC. The
// run starts without one (noted), and the cc bot is taken at the pack once the feed's last action of a bot is a mage spell.
func TestRemoteNoCCBotTakesTheMageFromItsSpells(t *testing.T) {
	g := newFakeRemote(t)
	g.party[2].Action = "frostbolt"
	plan := bigPackPlan(g, 5)
	rep, err := runRemote(t, g, plan, opts(), nil)
	if err != nil || rep.Remote.BigPackCC != 1 || !strings.HasSuffix(g.orders[g.indexOf("cmd cc")], " Magey") {
		t.Fatalf("%v %v %+v\n%s", err, g.orders, rep.Remote, notesOf(rep))
	}
	// A hunter's arcane shot and a priest's prayer are no mage spells; a 16-character cut of a mage spell is.
	for a, want := range map[string]bool{"arcane shot": false, "prayer of healin": false, "shadowburn": false, "scorch": true, "Frostbolt": true, "arcane explosion": true, "arcane explosio": true, "": false} {
		if isMageAction(a) != want {
			t.Errorf("isMageAction(%q) = %v", a, !want)
		}
	}
}
