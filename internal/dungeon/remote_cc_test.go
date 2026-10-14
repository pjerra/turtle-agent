package dungeon

import (
	"strings"
	"testing"

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
