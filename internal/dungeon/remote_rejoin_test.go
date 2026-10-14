package dungeon

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/pjerra/turtle-agent/internal/packet"
	"github.com/pjerra/turtle-agent/internal/tbm"
)

// The commander brings a far bot back to the lead bot with `cmd summon` (remote_rejoin.go).

// rejoinRun runs plan with a GM commander (a nearGame at level 3) that stands at the party's start, without "gm visible off" in the setup,
// so that keep-near sends no .appear: run_gm_assists then counts the summons alone.
func rejoinRun(t *testing.T, g *nearGame, plan *Plan, mod func(d *RemoteDriver)) (Report, error) {
	t.Helper()
	g.agent = Point{0, 0, 0}
	cfg := DefaultConfig()
	cfg.PostFight, cfg.AddsWait = 2*time.Second, 4*time.Second
	d := NewRemoteDriver(plan, g, cfg, objOpts(), g.clock())
	if mod != nil {
		mod(d)
	}
	err := d.Run(context.Background(), 0)
	rep, ended := d.Status()
	if !ended {
		t.Fatal("not ended")
	}
	return rep, err
}

// farMage keeps Magey where it is (x yd on the x axis) until a summon lands: the fake's bots otherwise follow the lead bot at once.
func farMage(g *fakeRemote, x float32) {
	g.party[2].X = x
	g.lagUntil = map[packet.GUID]time.Time{gMage: g.now.Add(24 * time.Hour)}
}

func rejoinPlan() *Plan { return remotePlan(Leg{Name: "a", To: Point{150, 0, 0}}) }

func TestRejoinFarBotIsSummonedToTheLeadBot(t *testing.T) {
	g := newNearGame(t, 3)
	farMage(g.fakeRemote, -100) // Magey stands 100 yd behind (a corpse run's end at the entrance)
	rep, err := rejoinRun(t, g, rejoinPlan(), nil)
	if err != nil || rep.State != "done" {
		t.Fatalf("%v %s\n%s", err, rep.State, notesOf(rep))
	}
	if strings.Join(g.summoned, ",") != "Magey" || len(g.ordersOf("cmd summon")) != 1 {
		t.Fatalf("summoned %v orders %v", g.summoned, g.orders)
	}
	r := rep.Remote
	if r.RejoinTPs != 1 || r.RunGMAssists != 1 || r.GMCommands != 0 || r.Orders["summon"] != 1 {
		t.Fatalf("%+v", r)
	}
	if got := rep.SetupShortcuts; len(got) != 1 || got[0] != "cmd summon Magey (GM assist during the run)" {
		t.Fatalf("setup %v", got)
	}
	// The summon puts the bot on follow of the commander: the party is put back on the lead bot after it landed.
	if i, j := g.indexOf("cmd summon"), g.indexOf("cmd follow"); i < 0 || j < i {
		t.Fatalf("no cmd follow after the summon: %v", g.orders)
	}
	if !strings.Contains(notesOf(rep), "rejoin: Magey is back at the lead bot") {
		t.Fatalf("notes\n%s", notesOf(rep))
	}
	// The summon went out before the lead bot left the entrance: the run did not walk on with the bot far.
	if i := g.indexOf("cmd summon"); i > g.indexOf("cmd move") {
		t.Fatalf("a move went out before the summon: %v", g.orders)
	}
}

func TestRejoinBotOnAnotherMapIsSummoned(t *testing.T) {
	g := newNearGame(t, 3)
	farMage(g.fakeRemote, 0)
	// Magey is on the outer map from the second leg on (a bot that died there and came up at the graveyard).
	g.onTick = func(f *fakeRemote) {
		if f.lead().X >= 30 && f.memberMap == nil {
			f.memberMap = map[packet.GUID]uint32{gMage: 99}
		}
	}
	rep, err := rejoinRun(t, g, remotePlan(Leg{Name: "a", To: Point{30, 0, 0}}, Leg{Name: "b", To: Point{150, 0, 0}}), nil)
	if err != nil || rep.State != "done" {
		t.Fatalf("%v %s\n%s", err, rep.State, notesOf(rep))
	}
	if strings.Join(g.summoned, ",") != "Magey" || rep.Remote.RejoinTPs != 1 || !strings.Contains(notesOf(rep), "Magey on another map") {
		t.Fatalf("summoned %v %+v\n%s", g.summoned, rep.Remote, notesOf(rep))
	}
}

// A bot inside rejoin_tp_dist is the gathers' business: nothing is sent.
func TestRejoinNearBotIsLeftAlone(t *testing.T) {
	g := newNearGame(t, 3)
	farMage(g.fakeRemote, -20)
	rep, err := rejoinRun(t, g, remotePlan(Leg{Name: "a", To: Point{30, 0, 0}}), nil) // 50 yd at the end of the walk
	if err != nil || rep.State != "done" {
		t.Fatalf("%v %s\n%s", err, rep.State, notesOf(rep))
	}
	if len(g.summoned) != 0 || rep.Remote.RejoinTPs != 0 || rep.Remote.RunGMAssists != 0 || g.indexOf("cmd follow") >= 0 && strings.Contains(notesOf(rep), "rejoin") {
		t.Fatalf("summoned %v %+v", g.summoned, rep.Remote)
	}
}

func TestRejoinOffAtZero(t *testing.T) {
	g := newNearGame(t, 3)
	farMage(g.fakeRemote, -100)
	rep, err := rejoinRun(t, g, rejoinPlan(), func(d *RemoteDriver) { d.tx.RejoinTPDist = 0 })
	if err != nil || rep.State != "done" {
		t.Fatalf("%v %s\n%s", err, rep.State, notesOf(rep))
	}
	if len(g.summoned) != 0 || rep.Remote.RejoinTPs != 0 || rep.Remote.RunGMAssists != 0 {
		t.Fatalf("summoned %v %+v", g.summoned, rep.Remote)
	}
}

// The distance is a tunable: a bot 100 yd back is not far at 150.
func TestRejoinDistanceIsTheTunable(t *testing.T) {
	g := newNearGame(t, 3)
	farMage(g.fakeRemote, -100)
	rep, err := rejoinRun(t, g, remotePlan(Leg{Name: "a", To: Point{30, 0, 0}}), func(d *RemoteDriver) { d.tx.RejoinTPDist = 150 }) // 130 yd at the end
	if err != nil || len(g.summoned) != 0 || rep.Remote.RejoinTPs != 0 {
		t.Fatalf("%v summoned %v", err, g.summoned)
	}
}

// Only a GM account sends the summon: any other account leaves the bot to the gathers, with one note.
func TestRejoinNeedsAGMAccount(t *testing.T) {
	g := newNearGame(t, 0)
	farMage(g.fakeRemote, -100)
	rep, err := rejoinRun(t, g, rejoinPlan(), nil)
	if err != nil || rep.State != "done" {
		t.Fatalf("%v %s\n%s", err, rep.State, notesOf(rep))
	}
	if len(g.summoned) != 0 || rep.Remote.RejoinTPs != 0 || strings.Count(notesOf(rep), "only a GM account brings it back") != 1 {
		t.Fatalf("summoned %v %+v\n%s", g.summoned, rep.Remote, notesOf(rep))
	}
}

// A game that is no Follower cannot tell the account level: no summon.
func TestRejoinNeedsAFollowerGame(t *testing.T) {
	g := newFakeRemote(t)
	farMage(g, -100)
	rep, err := runRemote(t, g, rejoinPlan(), opts(), nil)
	if err != nil || len(g.summoned) != 0 || rep.Remote.RejoinTPs != 0 {
		t.Fatalf("%v summoned %v", err, g.summoned)
	}
}

// In combat the party fights first; the summon waits for rejoinCalm of calm after it (the landing bot would join the fight).
func TestRejoinWaitsForTheFightAndForCalm(t *testing.T) {
	g := newNearGame(t, 3)
	farMage(g.fakeRemote, -100)
	until := g.now.Add(10 * time.Second)
	g.onTick = func(f *fakeRemote) {
		if f.now.Before(until) {
			f.party[1].State = "c" // the healer fights something the feed does not list
		}
	}
	rep, err := rejoinRun(t, g, rejoinPlan(), nil)
	if err != nil || rep.State != "done" {
		t.Fatalf("%v %s\n%s", err, rep.State, notesOf(rep))
	}
	i := g.indexOf("cmd summon")
	if i < 0 {
		t.Fatalf("no summon: %v\n%s", g.orders, notesOf(rep))
	}
	if at := g.orderAt[i]; at.Before(until.Add(rejoinCalm)) {
		t.Fatalf("the summon went out %v before the fight ended and the calm passed", until.Add(rejoinCalm).Sub(at))
	}
}

// Once per bot per rejoin_tp_gap_s: a bot that is far again soon after is left to the gathers.
func TestRejoinOncePerBotPerGap(t *testing.T) {
	g := newNearGame(t, 3)
	farMage(g.fakeRemote, -100)
	pulled := false
	g.onTick = func(f *fakeRemote) {
		if len(f.summoned) == 1 && !pulled {
			pulled = true
			f.party[2].X = f.lead().X - 100 // sent back to the entrance right after
		}
	}
	rep, err := rejoinRun(t, g, rejoinPlan(), nil)
	if err != nil || rep.State != "done" {
		t.Fatalf("%v %s\n%s", err, rep.State, notesOf(rep))
	}
	if len(g.summoned) != 1 || rep.Remote.RejoinTPs != 1 {
		t.Fatalf("summoned %v %+v", g.summoned, rep.Remote)
	}
}

// A refusal (a bot in combat, dead...) is no summon: nothing is counted and the run goes on, the bot not asked again inside the gap.
func TestRejoinRefusedSummonIsNotCounted(t *testing.T) {
	g := newNearGame(t, 3)
	farMage(g.fakeRemote, -100)
	g.cmdErr["summon"] = []string{"busy", "busy", "busy"}
	rep, err := rejoinRun(t, g, rejoinPlan(), nil)
	if err != nil || rep.State != "done" {
		t.Fatalf("%v %s\n%s", err, rep.State, notesOf(rep))
	}
	if len(g.summoned) != 0 || rep.Remote.RejoinTPs != 0 || rep.Remote.RunGMAssists != 0 || len(rep.SetupShortcuts) != 0 || !strings.Contains(notesOf(rep), "was refused: busy") {
		t.Fatalf("summoned %v %+v\n%s", g.summoned, rep.Remote, notesOf(rep))
	}
}

// A far bot at a rest gate is brought as well (the pull's rest gate), and a dead one is not summoned (it is revived first).
func TestRejoinAtARestGateButNeverADeadBot(t *testing.T) {
	g := newNearGame(t, 3)
	farMage(g.fakeRemote, -100)
	g.party[2].Dead, g.party[2].HP, g.party[2].State = true, 0, "d"
	m := g.addMob(299, Point{40, 0, 0})
	plan := remotePlan(Leg{Name: "first", To: Point{0, 0, 0}, PullGroups: []PullGroup{{Entries: []uint32{299}, Positions: []Point{m.pos}}}})
	rep, err := rejoinRun(t, g, plan, nil)
	if err != nil || rep.State != "done" {
		t.Fatalf("%v %s\n%s", err, rep.State, notesOf(rep))
	}
	if i, j := g.indexOf("cmd summon"), g.indexOf("cmd revive"); j < 0 || i >= 0 && i < j {
		t.Fatalf("a dead bot was summoned (or not revived): %v", g.orders)
	}
}

// Only the run's own bots are ever named: a party member the run did not name, the lead bot and a dead bot are never far bots.
func TestRejoinFarBotsNameOnlyTheRunsBots(t *testing.T) {
	g := newFakeRemote(t)
	d := NewRemoteDriver(rejoinPlan(), g, DefaultConfig(), opts(), g.clock())
	rec := func(guid uint64, name string, x float32, mod func(*tbm.PartyRec)) tbm.PartyRec {
		p := tbm.PartyRec{GUID: packet.GUID(guid), Name: name, X: x, HP: 10, MaxHP: 10, State: "n"}
		if mod != nil {
			mod(&p)
		}
		return p
	}
	d.fr = tbm.ViewFrame{Lead: gTank, Party: []tbm.PartyRec{
		rec(uint64(gTank), "Tanky", 0, nil),
		rec(uint64(gHealer), "Healy", 200, nil),
		rec(0x99, "Stranger", 500, nil),                                           // not a bot of the run
		rec(uint64(gMage), "Magey", 200, func(p *tbm.PartyRec) { p.Dead = true }), // dead
	}}
	far := d.rejoinFarBots(60)
	if len(far) != 1 || far[0].name != "Healy" {
		t.Fatalf("%+v", far)
	}
	// Without named bots the bound party (the feed's) is the run's.
	d.opt.Bots = nil
	if far = d.rejoinFarBots(60); len(far) != 2 {
		t.Fatalf("%+v", far)
	}
}
