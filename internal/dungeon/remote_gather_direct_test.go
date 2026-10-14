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
	if r.DirectGathers < 1 || r.DirectGatherSummons != 0 || r.DirectGatherWaitS < 5 {
		t.Fatalf("gather %+v", r)
	}
	if len(g.summoned) != 0 {
		t.Fatalf("summoned %v: a follower that came by itself was summoned", g.summoned)
	}
}

// A follower stuck behind (it cannot path to the lead bot) gets `cmd follow`, then `cmd summon <bot>` as the last resort, and the step goes
// after it. The summon is a bot command: no GM command is counted.
func TestDirectStepSummonsAStuckFollower(t *testing.T) {
	g := newFakeRemote(t)
	g.strictDirect = true
	lagMage(g, 34, time.Hour)
	rep, err := runRemote(t, g, viaRemotePlan(), opts(), nil)
	if err != nil {
		t.Fatalf("%v %+v", err, rep.Notes)
	}
	r := rep.Remote
	if len(g.summoned) != 1 || g.summoned[0] != "Magey" || r.DirectGatherSummons != 1 || r.DirectGatherFollows < 1 {
		t.Fatalf("summoned %v, %+v", g.summoned, r)
	}
	if r.Refused["move:too-far"] != 0 || r.GMCommands != 0 || len(directMoves(g)) != 1 {
		t.Fatalf("refused %v gm %d moves %v", r.Refused, r.GMCommands, directMoves(g))
	}
	if g.indexOf("cmd follow") > g.indexOf("cmd summon Magey") || g.indexOf("cmd summon Magey") > g.indexOf("cmd move 55.00 0.00 4.00 direct") {
		t.Fatalf("order of orders: %v", g.orders)
	}
}

// After a direct step the next one is not sent until every bot has arrived (Uldaman run 10: the second step went out one second after the
// first, with the followers still running).
func TestDirectStepWaitsAfterTheStepForTheParty(t *testing.T) {
	g := newFakeRemote(t)
	g.strictDirect = true
	lagMage(g, 43.4, time.Hour) // the mage falls behind as the first direct step ends
	rep, err := runRemote(t, g, directPlan(), opts(), nil)
	if err != nil {
		t.Fatalf("%v %+v", err, rep.Notes)
	}
	if rep.Remote.Refused["move:too-far"] != 0 || len(directMoves(g)) != 2 || len(g.summoned) != 1 {
		t.Fatalf("refused %v moves %v summoned %v", rep.Remote.Refused, directMoves(g), g.summoned)
	}
	if g.indexOf("cmd summon Magey") > g.indexOf("cmd move 55.00 0.00 4.00 direct") {
		t.Fatalf("the second step went before the mage was brought: %v", g.orders)
	}
}

// A too-far that names a bot (it fell behind after the gather) regathers that bot at once and sends the step again, once.
func TestDirectStepTooFarNamingABotRegathersIt(t *testing.T) {
	g := newFakeRemote(t)
	g.farOnce = "Magey"
	rep, err := runRemote(t, g, viaRemotePlan(), opts(), nil)
	if err != nil {
		t.Fatalf("%v %+v", err, rep.Notes)
	}
	moves := directMoves(g)
	if len(moves) != 2 || rep.Remote.Refused["move:too-far"] != 1 {
		t.Fatalf("direct moves %v, refused %v", moves, rep.Remote.Refused)
	}
	if len(g.summoned) != 1 || g.summoned[0] != "Magey" || g.indexOf("cmd summon Magey") > g.lastIndexOf("cmd move 55.00 0.00 4.00 direct") {
		t.Fatalf("summoned %v: %v", g.summoned, g.orders)
	}
	// Brought within a few seconds, not after the whole wait.
	first, second := g.orderAt[g.indexOf("cmd move 55.00 0.00 4.00 direct")], g.orderAt[g.lastIndexOf("cmd move 55.00 0.00 4.00 direct")]
	if second.Sub(first) > 8*time.Second {
		t.Fatalf("the regather took %v", second.Sub(first))
	}
}

// The wait has a ceiling: a follower that cannot be brought (the summon is refused too) does not hold the run beyond it. The step is sent
// anyway, the module refuses it, and the run ends with the refusal.
func TestDirectStepGatherHasACeiling(t *testing.T) {
	g := newFakeRemote(t)
	g.strictDirect = true
	g.cmdErr["summon"] = make([]string, 50)
	for i := range g.cmdErr["summon"] {
		g.cmdErr["summon"][i] = "busy"
	}
	lagMage(g, 34, time.Hour)
	start := g.now
	rep, err := runRemote(t, g, viaRemotePlan(), opts(), nil)
	if err == nil || !strings.Contains(err.Error(), "too-far") {
		t.Fatalf("%v", err)
	}
	if rep.Remote.DirectGatherTimeouts < 1 || len(g.summoned) != 0 {
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
