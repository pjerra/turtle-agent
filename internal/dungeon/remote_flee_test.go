package dungeon

import (
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/pjerra/turtle-agent/internal/packet"
	"github.com/pjerra/turtle-agent/internal/tbm"
)

// Runners and bodies in remote mode (remote_flee.go): a mob that runs off at low health is killed before it is out of reach, one that
// got away is waited for before the next pull, and a dead bot is not raised in place next to live hostiles.

func fleePlan(fleers ...uint32) *Plan {
	p := remotePlan(Leg{Name: "a", To: Point{0, 0, 0}, PullGroups: []PullGroup{
		{Entries: []uint32{299}, Positions: []Point{{40, 0, 0}}},
		{Entries: []uint32{300}, Positions: []Point{{90, 0, 0}}},
	}})
	p.Fleers = fleers
	return p
}

func mobAlive(g *fakeRemote, ms ...*rmob) bool {
	for _, m := range ms {
		if slices.Contains(g.mobs, m) {
			return true
		}
	}
	return false
}

// The runner is hit by a damage dealer ordered at it while it is still within reach: it dies, it is no runner that got away, and the
// pull of the next group is not held. With the plan naming the entry (the feed's c flag at low health) or without (low health and moving
// away from the party, which takes a second frame).
func TestRemoteRunnerIsKilledBeforeItIsOutOfReach(t *testing.T) {
	for name, fleers := range map[string][]uint32{"known": {299}, "unknown": nil} {
		t.Run(name, func(t *testing.T) {
			g := newFakeRemote(t)
			g.sight = 24
			a := g.addMob(299, Point{40, 0, 0})
			a.fleeAt, a.fleeSpeed, a.fleeFor = 15, 4, time.Minute
			b := g.addMob(300, Point{90, 0, 0})
			g.killRate = 10
			rep, err := runRemote(t, g, fleePlan(fleers...), opts(), nil)
			if err != nil {
				t.Fatalf("%v\n%s", err, notesOf(rep))
			}
			r := rep.Remote
			if mobAlive(g, a, b) || r.Fled != 0 || r.FleeWaits != 0 || r.FleeOrders < 1 {
				t.Fatalf("a alive %v b alive %v: %+v\n%s", mobAlive(g, a), mobAlive(g, b), r, notesOf(rep))
			}
			// The damage dealers go after it one by one, the tank keeps the mobs it holds.
			if g.indexOf("cmd attack "+tbm.GUIDArg(a.guid)+" Magey") < 0 || g.indexOf("cmd attack "+tbm.GUIDArg(a.guid)+" Tanky") >= 0 {
				t.Fatalf("orders %v", g.orders)
			}
		})
	}
}

// A mob at low health that does not move away is nobody's runner: a stunned or rooted one gets no special orders.
func TestRemoteLowHealthMobThatStaysIsNoRunner(t *testing.T) {
	g := newFakeRemote(t)
	g.sight = 24
	a := g.addMob(299, Point{40, 0, 0})
	a.fleeAt, a.fleeSpeed, a.fleeFor = 15, 0, 0 // flag c at 15 % and it stays where it stands
	b := g.addMob(300, Point{90, 0, 0})
	g.killRate = 10
	// Dealt with as before: the flag is a stun, the party fights it (the fake does not hurt a "fleeing" mob without the hit flag).
	g.onTick = func(g *fakeRemote) {
		if a.fleeing {
			a.fleeing, a.back, a.flags = false, true, "c"
		}
	}
	rep, err := runRemote(t, g, fleePlan(), opts(), nil)
	if err != nil || mobAlive(g, a, b) || rep.Remote.FleeOrders != 0 || rep.Remote.Fled != 0 {
		t.Fatalf("%v %+v\n%s", err, rep.Remote, notesOf(rep))
	}
}

// A mob that runs out of reach and out of the feed is not a kill. The next pull waits where the party stands; the mob comes back with a
// friend, both are fought there, and only then the party pulls on.
func TestRemotePullWaitsForTheMobThatRanOff(t *testing.T) {
	g := newFakeRemote(t)
	g.sight = 24
	a := g.addMob(299, Point{40, 0, 0})
	friend := g.addMob(302, Point{300, 300, 0}) // far away: it comes with the mob that ran off
	b := g.addMob(300, Point{90, 0, 0})
	a.fleeAt, a.fleeSpeed, a.fleeFor, a.hideRun, a.comesBack, a.friends = 15, 5, 12*time.Second, true, true, []*rmob{friend}
	a.untouchable = true
	g.killRate = 10
	var aliveAtPull, pulled bool
	g.onTick = func(g *fakeRemote) {
		if b.pulled && !pulled {
			pulled = true
			aliveAtPull = mobAlive(g, a, friend)
		}
	}
	rep, err := runRemote(t, g, fleePlan(299), opts(), nil)
	if err != nil {
		t.Fatalf("%v\n%s", err, notesOf(rep))
	}
	r := rep.Remote
	if !pulled || aliveAtPull || mobAlive(g, a, friend, b) {
		t.Fatalf("pulled %v, a/friend alive at the pull %v, alive now a %v friend %v b %v\n%s", pulled, aliveAtPull, mobAlive(g, a), mobAlive(g, friend), mobAlive(g, b), notesOf(rep))
	}
	if r.Fled != 1 || r.FleeWaits != 1 || r.FleeWaitS < 5 || r.FleeWaitS > 45 {
		t.Fatalf("%+v\n%s", r, notesOf(rep))
	}
	if !strings.Contains(notesOf(rep), "ran off at 1") {
		t.Fatalf("no note of the runner:\n%s", notesOf(rep))
	}
}

// The wait is bounded: a mob that never comes back holds the next pull for FleeWait and no longer.
func TestRemoteWaitForTheMobThatRanOffIsBounded(t *testing.T) {
	g := newFakeRemote(t)
	g.sight = 24
	a := g.addMob(299, Point{40, 0, 0})
	b := g.addMob(300, Point{90, 0, 0})
	a.fleeAt, a.fleeSpeed, a.fleeFor, a.hideRun, a.untouchable = 15, 5, time.Hour, true, true
	g.killRate = 10
	var lostAt, pulledAt time.Time
	g.onTick = func(g *fakeRemote) {
		if lostAt.IsZero() && a.fleeing && g.now.Sub(a.fleeStart) > 2*time.Second {
			lostAt = g.now
		}
		if pulledAt.IsZero() && b.pulled {
			pulledAt = g.now
		}
	}
	rep, err := runRemote(t, g, fleePlan(299), opts(), nil)
	if err != nil {
		t.Fatalf("%v\n%s", err, notesOf(rep))
	}
	r := rep.Remote
	if r.Fled != 1 || r.FleeWaits != 1 || pulledAt.IsZero() || pulledAt.Sub(lostAt) < defaultFleeWait-5*time.Second || pulledAt.Sub(lostAt) > defaultFleeWait+25*time.Second {
		t.Fatalf("lost at %v pulled at %v (+%v): %+v\n%s", lostAt, pulledAt, pulledAt.Sub(lostAt), r, notesOf(rep))
	}
}

const defaultFleeWait = fleeWait

// A runner last seen far from the party is not worth a wait.
func TestRemoteRunnerFarAwayIsNotWaitedFor(t *testing.T) {
	g := newFakeRemote(t)
	g.sight = 24
	a := g.addMob(299, Point{40, 0, 0})
	b := g.addMob(300, Point{90, 0, 0})
	a.fleeAt, a.fleeSpeed, a.fleeFor = 15, 30, time.Hour // out of sight and well beyond FleeNearYd within a poll or two
	a.hideRun, a.untouchable = true, true
	g.killRate = 40
	rep, err := runRemote(t, g, fleePlan(299), opts(), func(c *Config) {})
	if err != nil {
		t.Fatalf("%v\n%s", err, notesOf(rep))
	}
	if mobAlive(g, b) || rep.Remote.FleeWaits != 0 {
		t.Fatalf("%+v\n%s", rep.Remote, notesOf(rep))
	}
}

// ---- bodies ----

// A dead bot with a caster alive is not left to a resurrection in place when a live hostile stands near the bodies: it is released at once.
func TestRemoteNoResurrectionInPlaceNextToLiveMobs(t *testing.T) {
	g := newFakeRemote(t)
	g.addMob(299, Point{20, 0, 0})
	g.party[2].Dead, g.party[2].HP, g.party[2].State = true, 0, "d"
	began := g.now
	var releasedAt time.Time
	g.onTick = func(g *fakeRemote) {
		if releasedAt.IsZero() && len(g.revived) > 0 {
			releasedAt = g.now
		}
	}
	o := opts()
	o.Healer = "Healy"
	rep, err := runRemote(t, g, remotePlan(Leg{Name: "a", To: Point{0, 0, 0}, PullGroups: []PullGroup{{Entries: []uint32{299}, Positions: []Point{{20, 0, 0}}}}}), o, nil)
	if err != nil {
		t.Fatalf("%v\n%s", err, notesOf(rep))
	}
	if len(g.ordersOf("cmd revive")) != 1 || rep.Remote.UnsafeRezSkips != 1 || rep.Remote.RezWaits != 0 || releasedAt.Sub(began) > 8*time.Second {
		t.Fatalf("revived at +%v: %v %+v\n%s", releasedAt.Sub(began), g.orders, rep.Remote, notesOf(rep))
	}
	if !strings.Contains(notesOf(rep), "no resurrection in place") {
		t.Fatalf("no note:\n%s", notesOf(rep))
	}
}

// ... and with the mob just outside the radius the caster gets its wait, as before.
func TestRemoteResurrectionInPlaceWhenTheMobsAreFarFromTheBodies(t *testing.T) {
	g := newFakeRemote(t)
	g.addMob(299, Point{35, 0, 0})
	g.party[2].Dead, g.party[2].HP, g.party[2].State = true, 0, "d"
	raised := false
	g.onTick = func(g *fakeRemote) {
		if !raised && g.now.Sub(time.Unix(1_000_000, 0)) > 12*time.Second {
			raised = true
			p := g.party[2]
			p.Dead, p.HP, p.State = false, p.MaxHP/2, "n"
		}
	}
	o := opts()
	o.Healer = "Healy"
	rep, err := runRemote(t, g, remotePlan(Leg{Name: "a", To: Point{0, 0, 0}, PullGroups: []PullGroup{{Entries: []uint32{299}, Positions: []Point{{35, 0, 0}}}}}), o, nil)
	if err != nil || len(g.ordersOf("cmd revive")) != 0 || rep.Remote.UnsafeRezSkips != 0 || rep.Remote.RezWaits != 1 || rep.Remote.Rezzed != 1 {
		t.Fatalf("%v %v %+v\n%s", err, g.orders, rep.Remote, notesOf(rep))
	}
}

// A critter, a guard or a civilian near the bodies is no danger to a raised bot.
func TestRemoteBystandersAreNoDangerToABody(t *testing.T) {
	g := newFakeRemote(t)
	m := g.addMob(299, Point{20, 0, 0})
	m.kind = "k"
	g.party[2].Dead, g.party[2].HP, g.party[2].State = true, 0, "d"
	g.publish()
	d := NewRemoteDriver(fleePlan(), g, DefaultConfig(), opts(), g.clock())
	d.fr, d.haveFr = g.frame, true
	if got := d.hostilesNearCorpses(); len(got) != 0 {
		t.Fatalf("%v", got)
	}
	m.kind, m.reaction = "", "h"
	g.publish()
	d.fr = g.frame
	if got := d.hostilesNearCorpses(); len(got) != 1 {
		t.Fatalf("%v", got)
	}
}

// ---- runners that die between two frames ----

// flf is one feed frame for the runner tests: the mage's target and the runner (nil: not in the frame). skip leaves out that many feed
// frames before it (the driver missed them).
type flf struct {
	mageOn packet.GUID
	mob    *tbm.HostileRec
	skip   uint64
}

// fleeFrames puts frames in the driver as look() does (ledger first, then the runners): the tank at the origin and the mage at 2 yd.
func fleeFrames(d *RemoteDriver, frames ...flf) {
	for _, f := range frames {
		d.n += 1 + f.skip
		var hs []tbm.HostileRec
		if f.mob != nil {
			hs = append(hs, *f.mob)
		}
		d.fr = tbm.ViewFrame{Seq: d.n, Lead: gTank, Party: []tbm.PartyRec{
			{GUID: gTank, Name: "Tanky", HP: 100, MaxHP: 100},
			{GUID: gMage, Name: "Magey", HP: 100, MaxHP: 100, X: 2, Target: f.mageOn},
		}, Hostiles: hs}
		d.haveFr = true
		d.noteLedger()
		d.noteFleeing()
	}
}

// Uldaman run 3 (19z12, observe mode): Stonevault Geomancers and Shadowforge casters ran at 6-16 % with bots on them, 1-24 yd from the
// party, and were gone from the feed between two frames. The feed lists only living units and every bot's target, so such a mob died:
// it is not a fled mob, and the ledger counts it dead. A runner that no bot targeted in its last frame (the target dropped first), one
// healthier than fleeHeldDiedPct, one far from the party, or one that went while the driver missed a frame, still counts as fled.
func TestRemoteRunnerGoneWhileABotHitsItDied(t *testing.T) {
	const g = packet.GUID(0xF1300012F0006B61)
	mob := func(x float32, hp int, flags string) *tbm.HostileRec {
		return &tbm.HostileRec{GUID: g, Entry: 4848, X: x, HPPct: hp, Target: gTank, Flags: flags}
	}
	// at hp: 26 % at 3 yd, then hp and moving away (a runner), last seen at x.
	run := func(on packet.GUID, x float32, hp int) []flf {
		return []flf{{on, mob(3, 26, "-"), 0}, {on, mob(x-2, hp, "c"), 0}, {on, mob(x, hp, "c"), 0}}
	}
	cases := []struct {
		name   string
		fleers bool
		frames []flf
		fled   bool
	}{
		{"targeted, near: died", false, append(run(g, 8, 16), flf{}, flf{}), false},
		{"targeted, 24 yd off: died", false, append(run(g, 26, 16), flf{}, flf{}), false},
		{"untargeted: ran off", false, append(run(0, 8, 16), flf{}, flf{}), true},
		{"targeted, 25 yd off, target dropped before it went: ran off", false, append(run(g, 27, 16), flf{0, mob(29, 16, "c"), 0}, flf{}, flf{}), true},
		{"targeted, out of chase range: ran off", false, append(run(g, 60, 16), flf{}, flf{}), true},
		{"targeted, a frame missed: ran off", false, append(run(g, 8, 16), flf{0, nil, 1}, flf{}), true},
		{"targeted known fleer at 33 %: ran off", true, append(run(g, 8, 33), flf{}, flf{}), true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			d := ledgerDriver(t)
			if c.fleers {
				d.plan.Fleers = []uint32{4848}
			}
			fleeFrames(d, c.frames[:3]...)
			if !d.isRunning(g) {
				t.Fatalf("not taken for a runner")
			}
			fleeFrames(d, c.frames[3:]...)
			if d.wasFled(g) != c.fled {
				t.Fatalf("fled %v, want %v\n%s", d.wasFled(g), c.fled, strings.Join(d.rep.r.Notes, "\n"))
			}
			if c.fled && d.ledgerDead([]uint32{4848}) {
				t.Fatal("a mob that ran off is on the ledger as dead")
			}
			if !c.fled && !d.ledgerDead([]uint32{4848}) {
				t.Fatal("a runner that died in the bots' hands is not on the ledger")
			}
		})
	}
}

// A mob at half health that a bot targets and that leaves the feed is no runner and no kill.
func TestRemoteTargetedHealthyMobGoneIsNotDead(t *testing.T) {
	const g = packet.GUID(0xF1300012F0006B62)
	m := &tbm.HostileRec{GUID: g, Entry: 4848, X: 8, HPPct: 50, Target: gTank, Flags: "-"}
	m2 := *m
	m2.X = 12
	d := ledgerDriver(t)
	fleeFrames(d, flf{g, m, 0}, flf{g, &m2, 0}, flf{}, flf{}, flf{})
	if d.wasFled(g) || d.ledgerDead([]uint32{4848}) {
		t.Fatalf("fled %v, ledger dead %v", d.wasFled(g), d.ledgerDead([]uint32{4848}))
	}
}
