package dungeon

import (
	"strings"
	"testing"
	"time"

	"github.com/pjerra/turtle-agent/internal/packet"
)

// directPlan is a lip walk and two direct steps, as the Uldaman pit crossing.
func directPlan() *Plan {
	return remotePlan(Leg{Name: "gap", Via: []Waypoint{{Point: Point{40, 0, 0}}, {Point: Point{43.5, 0, 1}, Direct: true}, {Point: Point{55, 0, 4}, Direct: true}}, To: Point{55, 0, 4},
		Steps: []Step{{WaitFor: &WaitFor{HostilesGone: &HostilesGone{Radius: 30}, TimeoutS: 5}}}})
}

// lagMage drops the mage 6 yd behind the lead bot the first time the lead bot is at or past x, for the given time (an hour: stuck).
func lagMage(g *fakeRemote, x float32, lag time.Duration) {
	done := false
	g.onTick = func(g *fakeRemote) {
		if done || g.lead().X < x {
			return
		}
		done = true
		m := g.party[2]
		m.X, m.Y, m.Z = g.lead().X-6, g.lead().Y, g.lead().Z
		if g.lagUntil == nil {
			g.lagUntil = map[packet.GUID]time.Time{}
		}
		g.lagUntil[m.GUID] = g.now.Add(lag)
	}
}

func directMoves(g *fakeRemote) []string {
	var out []string
	for _, m := range g.ordersOf("cmd move") {
		if strings.HasSuffix(m, "direct") {
			out = append(out, m)
		}
	}
	return out
}

// A follower that lags behind the lead bot when a direct step is due is waited for: the step goes out once it has come up, and the
// module never has to refuse it.
func TestDirectStepWaitsForALaggingFollower(t *testing.T) {
	g := newFakeRemote(t)
	g.strictDirect = true
	lagMage(g, 34, 6*time.Second)
	rep, err := runRemote(t, g, viaRemotePlan(), opts(), nil)
	if err != nil {
		t.Fatalf("%v %+v", err, rep.Notes)
	}
	r := rep.Remote
	if len(directMoves(g)) != 1 || r.Refused["move:too-far"] != 0 {
		t.Fatalf("direct moves %v, refused %v", directMoves(g), r.Refused)
	}
	if r.DirectGathers < 1 || r.DirectGatherWaitS < 5 {
		t.Fatalf("gather %+v", r)
	}
	if len(g.summoned) != 0 {
		t.Fatalf("summoned %v: a follower that came by itself was summoned", g.summoned)
	}
}

// leadSpotMoves counts the `cmd move` orders sent to the very spot the lead bot stood on (a gather's move to the lead bot).
func leadSpotMoves(g *fakeRemote) int {
	n := 0
	for i := range g.moveOrderDest {
		if dist3(g.moveOrderDest[i], g.moveOrderPos[i]) < 0.05 {
			n++
		}
	}
	return n
}

// A follower 6 yd behind the lead bot (where `cmd follow` holds the bots) is more than the module's 5 yd: the gather sends `cmd move` to
// the lead bot's spot, never `cmd summon` (a summon set the bots' follow to the commander, outside the instance, and the party was ejected
// to Northshire: Uldaman hires-3 run 1). The party is held there with `cmd stay`, and the step goes after it. No GM command is counted.
func TestDirectStepMovesAStuckFollowerToTheLeadBotNeverSummons(t *testing.T) {
	g := newFakeRemote(t)
	g.strictDirect = true
	g.moveFetches = true
	lagMage(g, 34, time.Hour)
	rep, err := runRemote(t, g, viaRemotePlan(), opts(), nil)
	if err != nil {
		t.Fatalf("%v %+v", err, rep.Notes)
	}
	r := rep.Remote
	if len(g.summoned) != 0 || len(g.ordersOf("cmd summon")) != 0 {
		t.Fatalf("summoned %v: %v", g.summoned, g.orders)
	}
	if r.DirectGatherMoves < 1 || r.DirectGatherStays != 1 || leadSpotMoves(g) < 1 {
		t.Fatalf("moves %d stays %d lead-spot moves %d", r.DirectGatherMoves, r.DirectGatherStays, leadSpotMoves(g))
	}
	if r.Refused["move:too-far"] != 0 || r.GMCommands != 0 || len(directMoves(g)) != 1 {
		t.Fatalf("refused %v gm %d moves %v", r.Refused, r.GMCommands, directMoves(g))
	}
	direct := g.indexOf("cmd move 55.00 0.00 4.00 direct")
	if st := g.indexOf("cmd stay"); st < 0 || st > direct {
		t.Fatalf("no cmd stay between the gather and the step: %v", g.orders)
	}
}

// A follower within 5 yd of the lead bot is on it: nothing is sent to gather.
func TestDirectStepAcceptsAFollowerWithinFiveYards(t *testing.T) {
	g := newFakeRemote(t)
	g.strictDirect = true
	g.moveFetches = true
	g.onTick = func(g *fakeRemote) {
		if g.lead().X < 34 {
			return
		}
		m := g.party[2]
		m.X, m.Y, m.Z = g.lead().X-4.5, g.lead().Y, g.lead().Z // always 4.5 yd behind
		g.lagUntil = map[packet.GUID]time.Time{m.GUID: g.now.Add(time.Hour)}
	}
	rep, err := runRemote(t, g, viaRemotePlan(), opts(), nil)
	if err != nil {
		t.Fatalf("%v %+v", err, rep.Notes)
	}
	if leadSpotMoves(g) != 0 || rep.Remote.DirectGatherMoves != 0 || rep.Remote.DirectGatherStays != 0 || len(g.ordersOf("cmd summon")) != 0 {
		t.Fatalf("gather orders sent for a bot at 4.5 yd: %v", g.orders)
	}
}

// After a direct step the next one is not sent until every bot has arrived (Uldaman run 10: the second step went out one second after the
// first, with the followers still running).
func TestDirectStepWaitsAfterTheStepForTheParty(t *testing.T) {
	g := newFakeRemote(t)
	g.strictDirect = true
	g.moveFetches = true
	lagMage(g, 43.4, time.Hour) // the mage falls behind as the first direct step ends
	rep, err := runRemote(t, g, directPlan(), opts(), nil)
	if err != nil {
		t.Fatalf("%v %+v", err, rep.Notes)
	}
	if rep.Remote.Refused["move:too-far"] != 0 || len(directMoves(g)) != 2 || len(g.summoned) != 0 {
		t.Fatalf("refused %v moves %v summoned %v", rep.Remote.Refused, directMoves(g), g.summoned)
	}
	if leadSpotMoves(g) < 1 || g.indexOf("cmd stay") < 0 || g.indexOf("cmd stay") > g.indexOf("cmd move 55.00 0.00 4.00 direct") {
		t.Fatalf("the second step went before the mage was brought: %v", g.orders)
	}
}

// A too-far that names a bot (it fell behind after the gather) regathers that bot at once and sends the step again, once.
func TestDirectStepTooFarNamingABotRegathersIt(t *testing.T) {
	g := newFakeRemote(t)
	g.farOnce = "Magey"
	g.moveFetches = true
	rep, err := runRemote(t, g, viaRemotePlan(), opts(), nil)
	if err != nil {
		t.Fatalf("%v %+v", err, rep.Notes)
	}
	moves := directMoves(g)
	if len(moves) != 2 || rep.Remote.Refused["move:too-far"] != 1 {
		t.Fatalf("direct moves %v, refused %v", moves, rep.Remote.Refused)
	}
	if len(g.summoned) != 0 || len(g.ordersOf("cmd summon")) != 0 || leadSpotMoves(g) < 1 {
		t.Fatalf("summoned %v, lead-spot moves %d: %v", g.summoned, leadSpotMoves(g), g.orders)
	}
	// Brought within a few seconds, not after the whole wait.
	first, second := g.orderAt[g.indexOf("cmd move 55.00 0.00 4.00 direct")], g.orderAt[g.lastIndexOf("cmd move 55.00 0.00 4.00 direct")]
	if second.Sub(first) > 8*time.Second {
		t.Fatalf("the regather took %v", second.Sub(first))
	}
}

// The wait has a ceiling: a follower that cannot be brought (the move does not fetch it, and no summon is sent) does not hold the run beyond it. The step is sent
// anyway, the module refuses it, and the run ends with the refusal.
func TestDirectStepGatherHasACeiling(t *testing.T) {
	g := newFakeRemote(t)
	g.strictDirect = true
	lagMage(g, 34, time.Hour)
	start := g.now
	rep, err := runRemote(t, g, viaRemotePlan(), opts(), nil)
	if err == nil || !strings.Contains(err.Error(), "too-far") {
		t.Fatalf("%v", err)
	}
	if rep.Remote.DirectGatherTimeouts < 1 || len(g.ordersOf("cmd summon")) != 0 {
		t.Fatalf("%+v summoned %v", rep.Remote, g.summoned)
	}
	if el := g.now.Sub(start); el > 6*time.Minute {
		t.Fatalf("the run took %v", el)
	}
	var noted bool
	for _, n := range rep.Notes {
		noted = noted || strings.Contains(n, "did not stand on the lead bot")
	}
	if !noted {
		t.Fatalf("no note: %v", rep.Notes)
	}
}

func TestFarBotName(t *testing.T) {
	for in, want := range map[string]string{
		"Hega is not with Jialebaeth (no navmesh path of at most 40 yd to its spot).": "Hega",
		"the lead bot is out of the step's limits":                                    "",
		"": "",
	} {
		if got := farBotName(in); got != want {
			t.Errorf("%q: %q, want %q", in, got, want)
		}
	}
}
