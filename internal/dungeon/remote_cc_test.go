package dungeon

import (
	"strings"
	"testing"
	"time"

	"github.com/pjerra/turtle-agent/internal/tbm"
)

// bigPack runs one pull group of n mobs (entries 299, then 300 for the rest) with the cc bot given.
func bigPack(t *testing.T, n int, ccBot string, prefer []uint32) (*fakeRemote, []*rmob, Report) {
	t.Helper()
	g := newFakeRemote(t)
	var mobs []*rmob
	entries := []uint32{}
	for i := 0; i < n; i++ {
		e := uint32(300)
		if i == 0 {
			e = 299
		}
		mobs = append(mobs, g.addMob(e, Point{30 + float32(i), float32(i % 2), 0}))
		entries = append(entries, e)
	}
	plan := remotePlan(Leg{Name: "a", To: Point{0, 0, 0}, PullGroups: []PullGroup{{Entries: entries, Positions: []Point{{30, 0, 0}}}}})
	plan.CCPriority = prefer
	o := opts()
	o.CCBot = ccBot
	rep, err := runRemote(t, g, plan, o, nil)
	if err != nil {
		t.Fatalf("%v %v", err, rep.Notes)
	}
	return g, mobs, rep
}

func TestRemoteBigPackGetsACCBeforeThePull(t *testing.T) {
	g, mobs, rep := bigPack(t, 5, "Magey", nil)
	cc, pb := g.indexOf("cmd cc"), g.indexOf("cmd pullback")
	if cc < 0 || cc > pb || !strings.HasPrefix(g.orders[cc], "cmd cc moon ") || !strings.HasSuffix(g.orders[cc], " Magey") {
		t.Fatalf("cc %d pullback %d %v", cc, pb, g.orders)
	}
	// Never the pull target; with no cc_priority, the member farthest from it (the last one, at x 34).
	target := strings.Fields(g.orders[pb])[2]
	if strings.Contains(g.orders[cc], target) || !strings.Contains(g.orders[cc], tbm.GUIDArg(mobs[4].guid)) {
		t.Fatalf("cc %q, pull %q", g.orders[cc], g.orders[pb])
	}
	if r := rep.Remote; r.BigPacks != 1 || r.BigPackCC != 1 || r.CCOrders != 1 {
		t.Fatalf("%+v", r)
	}
}

func TestRemoteBigPackCCTakesThePreferredEntry(t *testing.T) {
	g, mobs, _ := bigPack(t, 6, "Magey", []uint32{300})
	cc := g.indexOf("cmd cc")
	if cc < 0 || strings.Contains(g.orders[cc], tbm.GUIDArg(mobs[0].guid)) {
		t.Fatalf("%v", g.orders)
	}
}

func TestRemoteSmallPackOrNoCCBotGetsNoCC(t *testing.T) {
	if g, _, rep := bigPack(t, 4, "Magey", nil); g.indexOf("cmd cc") >= 0 || rep.Remote.BigPacks != 0 {
		t.Fatalf("4 mobs: %v %+v", g.orders, rep.Remote)
	}
	g, _, rep := bigPack(t, 5, "", nil)
	if g.indexOf("cmd cc") >= 0 || rep.Remote.BigPacks != 1 || rep.Remote.BigPackCC != 0 {
		t.Fatalf("no cc bot: %v %+v", g.orders, rep.Remote)
	}
}

// Owner rule 2026-10-14: a Shadowhunter (cc_always) is sheeped in any pack, below big_pack too, and is not the pull target while another
// member is there; in a big pack it goes before the cc_priority entries.
func TestRemoteCCAlwaysEntryIsSheepedInASmallPack(t *testing.T) {
	g := newFakeRemote(t)
	g.addMob(299, Point{30, 0, 0})
	hex := g.addMob(300, Point{31, 1, 0})
	plan := remotePlan(Leg{Name: "a", To: Point{0, 0, 0}, PullGroups: []PullGroup{{Entries: []uint32{300, 299}, Positions: []Point{{30, 0, 0}}}}})
	plan.CCAlways = []uint32{300}
	o := opts()
	o.CCBot = "Magey"
	rep, err := runRemote(t, g, plan, o, nil)
	if err != nil {
		t.Fatalf("%v %v", err, rep.Notes)
	}
	cc, pb := g.indexOf("cmd cc"), g.indexOf("cmd pullback")
	if cc < 0 || cc > pb || g.orders[cc] != "cmd cc moon "+tbm.GUIDArg(hex.guid)+" Magey" || strings.Contains(g.orders[pb], tbm.GUIDArg(hex.guid)) {
		t.Fatalf("cc %d pullback %d %v", cc, pb, g.orders)
	}
	if r := rep.Remote; r.BigPacks != 0 || r.BigPackCC != 1 {
		t.Fatalf("%+v", r)
	}
}

func TestRemoteCCAlwaysGoesBeforeThePriorityList(t *testing.T) {
	g := newFakeRemote(t)
	var entries []uint32
	var hex *rmob
	for i := 0; i < 5; i++ {
		e := uint32(299)
		if i == 2 {
			e = 301
		} else if i > 0 {
			e = 300
		}
		m := g.addMob(e, Point{30 + float32(i), 0, 0})
		if e == 301 {
			hex = m
		}
		entries = append(entries, e)
	}
	plan := remotePlan(Leg{Name: "a", To: Point{0, 0, 0}, PullGroups: []PullGroup{{Entries: entries, Positions: []Point{{30, 0, 0}}}}})
	plan.CCPriority, plan.CCAlways = []uint32{301, 300}, []uint32{301}
	o := opts()
	o.CCBot = "Magey"
	if rep, err := runRemote(t, g, plan, o, nil); err != nil {
		t.Fatalf("%v %v", err, rep.Notes)
	}
	if cc := g.indexOf("cmd cc"); cc < 0 || g.orders[cc] != "cmd cc moon "+tbm.GUIDArg(hex.guid)+" Magey" {
		t.Fatalf("%v", g.orders)
	}
}

// aheadPack runs a leg whose walk passes n mobs of entry 77 (no pull group lists them) with the cc bot given.
func aheadPack(t *testing.T, n int, ccBot string) (*fakeRemote, []*rmob, Report) {
	t.Helper()
	g := newFakeRemote(t)
	g.aggro = 12
	var mobs []*rmob
	for i := 0; i < n; i++ {
		mobs = append(mobs, g.addMob(77, Point{60 + float32(i%3), 8 + float32(i/3), 0}))
	}
	plan := remotePlan(Leg{Name: "first", To: Point{120, 0, 0}})
	o := opts()
	o.CCBot = ccBot
	rep, err := runRemote(t, g, plan, o, nil)
	if err != nil || rep.State != "done" {
		t.Fatalf("%v %s\n%s", err, rep.State, notesOf(rep))
	}
	return g, mobs, rep
}

// Uldaman hires-3: a pull of a pack the plan does not list woke six mobs with no CC. An ahead pull counts what would come with its
// target like a planned pull does: from big_pack mobs on, it is a big pull, gathered for and CCed before the pullback.
func TestRemoteAheadPullOfABigPackGetsGatherAndCC(t *testing.T) {
	g, mobs, rep := aheadPack(t, 6, "Magey")
	cc, pb := g.indexOf("cmd cc"), g.indexOf("cmd pullback")
	if cc < 0 || pb < 0 || cc > pb || !strings.HasPrefix(g.orders[cc], "cmd cc moon ") || !strings.HasSuffix(g.orders[cc], " Magey") {
		t.Fatalf("cc %d pullback %d %v", cc, pb, g.orders)
	}
	target := strings.Fields(g.orders[pb])[2]
	if strings.Contains(g.orders[cc], target) {
		t.Fatalf("the pull target was sheeped: %q, pull %q", g.orders[cc], g.orders[pb])
	}
	for _, m := range mobs {
		if m.aggroed {
			t.Fatalf("the party walked into the pack: %v", g.orders)
		}
	}
	if r := rep.Remote; r.AheadPulls < 1 || r.AheadBigPulls < 1 || r.BigPackCC < 1 || r.WalkAggros != 0 {
		t.Fatalf("%+v\n%s", r, notesOf(rep))
	}
	if !strings.Contains(notesOf(rep), "is a big pull (6 mobs") {
		t.Fatalf("no big-pull note:\n%s", notesOf(rep))
	}
}

// A small pack on the way is pulled as before: no CC, not a big pull.
func TestRemoteAheadPullOfASmallPackIsUnchanged(t *testing.T) {
	g, _, rep := aheadPack(t, 3, "Magey")
	if g.indexOf("cmd cc") >= 0 || g.indexOf("cmd pullback") < 0 {
		t.Fatalf("%v", g.orders)
	}
	if r := rep.Remote; r.AheadPulls < 1 || r.AheadBigPulls != 0 || r.BigPackCC != 0 {
		t.Fatalf("%+v", r)
	}
}

// A pack whose first CC ran out (Polymorph lasts 20 to 50 s) gets a new one when it is pulled again; one that was ordered a moment ago,
// or that the feed shows crowd-controlled, still holds. The party's own mark (tankFirst.ccHeld) proves nothing about the aura.
func TestRemoteBigPackCCLapses(t *testing.T) {
	g := newFakeRemote(t)
	d := NewRemoteDriver(remotePlan(Leg{Name: "a", To: Point{1, 0, 0}}), g, DefaultConfig(), opts(), g.clock())
	h := tbm.HostileRec{GUID: 0x501, Entry: 77, HPPct: 100}
	d.tf.init()
	d.tf.ccHeld[h.GUID] = true
	if d.ccStillOn(h) {
		t.Fatal("a held mark without an order or the aura is not a CC")
	}
	d.ccOrdered[h.GUID] = g.now
	if !d.ccStillOn(h) {
		t.Fatal("an order a moment ago holds")
	}
	g.advance(ccFresh + time.Second)
	if d.ccStillOn(h) {
		t.Fatal("an order from long ago, with no aura in the feed, has lapsed")
	}
	h.Flags = "c"
	if !d.ccStillOn(h) {
		t.Fatal("the feed shows it crowd-controlled")
	}
}
