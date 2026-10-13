package session

import (
	"context"
	"path/filepath"
	"reflect"
	"testing"
	"time"

	"github.com/pjerra/turtle-agent/internal/capture"
	"github.com/pjerra/turtle-agent/internal/events"
	"github.com/pjerra/turtle-agent/internal/game"
	"github.com/pjerra/turtle-agent/internal/game/gametest"
	"github.com/pjerra/turtle-agent/internal/objects"
	"github.com/pjerra/turtle-agent/internal/opcode"
	"github.com/pjerra/turtle-agent/internal/packet"
)

func allEvents(l *events.Log) []events.Event {
	return l.Read(events.Query{Limit: events.MaxLimit}).Events
}

// liveScenario drives a session through combat with names (one of them never answered),
// vendor and quest requests whose events are inferred from the agent's own packets, a
// knockback with its landing, and a logout, all recorded to a capture.
func liveScenario(t *testing.T, path string) []events.Event {
	t.Helper()
	w, err := capture.Create(path, []byte("SECRETPW"))
	if err != nil {
		t.Fatal(err)
	}
	h := startTapped(t, Options{NamesWait: 100 * time.Millisecond}, testKey, testKey, nil, w)
	h.enterWorld()
	ctx := context.Background()
	stranger := packet.GUID(0x40)

	h.send(opcode.SMSGGroupList, gametest.GroupList(me.GUID, gorm))
	wolfName(h)
	h.send(opcode.SMSGAttackerstateupdate, gametest.AttackerStateUpdate(game.HitInfoCritical, gorm.GUID, wolfGUID, 24, 0, 0, 0, 1, 0))
	h.send(opcode.SMSGSpellStart, gametest.SpellStart(me.GUID, 133, 3000, wolfGUID))
	h.send(opcode.SMSGSpellGo, gametest.SpellGo(me.GUID, 133, []packet.GUID{wolfGUID}, nil, nil, wolfGUID))
	h.send(opcode.SMSGSpellnonmeleedamagelog, gametest.SpellNonMeleeDamage(wolfGUID, stranger, 133, 30, 2, 0, 0, false, 0, 0))
	h.send(opcode.SMSGPartykilllog, gametest.PartyKill(gorm.GUID, wolfGUID))
	time.Sleep(250 * time.Millisecond) // the stranger's name query is never answered

	// The sale, the abandon and the accept are inferred from the agent's requests and the
	// update fields that follow.
	h.send(opcode.SMSGUpdateObject, ownInventory(map[uint8]packet.GUID{23: swordGUID}, map[packet.GUID][2]uint32{swordGUID: {2589, 1}}))
	waitFor(t, func() bool { return len(h.s.Inventory()) == 1 })
	questLog(h, 2, 100, 0)
	waitFor(t, func() bool { return len(h.s.QuestLog()) == 1 })
	if _, err := h.s.Sell(ctx, vendorGUID, EquipRef{Entry: 2589}, 0); err != nil {
		t.Fatal(err)
	}
	h.expect(opcode.CMSGSellItem)
	if err := h.s.QuestAbandon(ctx, 100); err != nil {
		t.Fatal(err)
	}
	h.expect(opcode.CMSGQuestlogRemoveQuest)
	if err := h.s.QuestAccept(ctx, giverGUID, 101); err != nil {
		t.Fatal(err)
	}
	h.expect(opcode.CMSGQuestgiverAcceptQuest)
	h.send(opcode.SMSGUpdateObject, ownInventory(nil, nil))
	waitFor(t, func() bool { return len(ofKind(h.s.Log(), events.KindQuestAbandoned)) == 1 })
	questLog(h, 3, 101, 0)
	coinage(h, 80)
	waitFor(t, func() bool {
		return len(ofKind(h.s.Log(), events.KindVendorSell)) == 1 && len(ofKind(h.s.Log(), events.KindQuestAccepted)) == 1
	})

	// A purchase and a quest hand-in: the buy's item and the reward's NPC come from the
	// agent's own requests, which the capture holds. A Warden packet is stored without its body.
	h.send(opcode.SMSGWardenData, []byte("not for the log"))
	if err := h.s.Buy(ctx, vendorGUID, BuyRef{Entry: 2512, Count: 2}); err != nil {
		t.Fatal(err)
	}
	h.expect(opcode.CMSGBuyItem)
	h.send(opcode.SMSGBuyItem, gametest.BuyItem(vendorGUID, 2, 18, 2))
	if err := h.s.QuestComplete(ctx, giverGUID, 101); err != nil {
		t.Fatal(err)
	}
	h.expect(opcode.CMSGQuestgiverCompleteQuest)
	h.send(opcode.SMSGQuestgiverRequestItems, gametest.QuestGiverRequestItems(giverGUID, 101, "More Rats", "Bring tails.", 0, nil, true))
	h.expect(opcode.CMSGQuestgiverRequestReward)
	h.send(opcode.SMSGQuestgiverOfferReward, gametest.QuestGiverOfferReward(giverGUID, 101, "More Rats", "Thanks.", nil, nil, 0, 0))
	if err := h.s.QuestReward(ctx, giverGUID, 101, nil); err != nil {
		t.Fatal(err)
	}
	h.expect(opcode.CMSGQuestgiverChooseReward)
	h.send(opcode.SMSGQuestgiverQuestComplete, gametest.QuestGiverQuestComplete(101, 450, 250, gametest.QuestReward{Entry: 118, Count: 2}))
	waitFor(t, func() bool { return len(ofKind(h.s.Log(), events.KindQuestRewarded)) == 1 })

	// A knockback, flown by the session's own simulation until it lands.
	h.send(opcode.SMSGMoveKnockBack, gametest.KnockBack(me.GUID, 5, 0, 1, 8, -9.5))
	h.expect(opcode.CMSGMoveKnockBackAck)
	waitFor(t, func() bool {
		for _, e := range ofKind(h.s.Log(), events.KindSession) {
			if e.Data["state"] == "landed" {
				return true
			}
		}
		return false
	})
	h.logout()
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	return allEvents(h.s.Log())
}

// The decoded capture is the event log the live session built: same events, same order,
// same sequence numbers and names, with times that follow the capture.
func TestReplayEqualsLiveEvents(t *testing.T) {
	path := filepath.Join(t.TempDir(), "agent.cap")
	live := liveScenario(t, path)
	recs, err := capture.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	log, rep, err := Replay(recs, Options{NamesWait: 100 * time.Millisecond})
	if err != nil {
		t.Fatal(err)
	}
	got := allEvents(log)
	if rep.Character != "Harnessb" || rep.Ended != "" || rep.Redacted != 1 {
		t.Fatalf("report %+v", rep)
	}
	kinds := map[string]bool{}
	for _, e := range live {
		kinds[e.Kind] = true
	}
	for _, k := range []string{events.KindMelee, events.KindSpellStart, events.KindSpellGo, events.KindSpellDamage, events.KindKill,
		events.KindVendorSell, events.KindQuestAbandoned, events.KindQuestAccepted, events.KindVendorBuy, events.KindQuestRewarded, events.KindSession} {
		if !kinds[k] {
			t.Fatalf("the scenario has no %s event", k)
		}
	}
	if len(got) != len(live) {
		t.Fatalf("%d decoded events, %d live\nlive:    %v\ndecoded: %v", len(got), len(live), kindList(live), kindList(got))
	}
	for i := range live {
		l, g := live[i], got[i]
		// The only field that legitimately differs: the live time is when the session
		// handled the packet, the capture's when the socket read it.
		if d := l.Time.Sub(g.Time); d < -time.Second || d > time.Second {
			t.Errorf("event %d (%s): time %v live, %v decoded", l.Seq, l.Kind, l.Time, g.Time)
		}
		// mono_ms in a replay counts from the capture's first record; live it counts from the
		// process start. It must follow the capture's time and move as the live one did.
		if want := g.Time.Sub(recs[0].Time).Milliseconds(); g.MonoMS < want-1 || g.MonoMS > want+1 {
			t.Errorf("event %d (%s): mono_ms %d, %d ms after the first record", g.Seq, g.Kind, g.MonoMS, want)
		}
		if d := (l.MonoMS - live[0].MonoMS) - (g.MonoMS - got[0].MonoMS); d < -200 || d > 200 {
			t.Errorf("event %d (%s): mono_ms moved %d ms live, %d ms decoded", l.Seq, l.Kind, l.MonoMS-live[0].MonoMS, g.MonoMS-got[0].MonoMS)
		}
		l.Time, g.Time, l.MonoMS, g.MonoMS = time.Time{}, time.Time{}, 0, 0
		if !reflect.DeepEqual(l, g) {
			t.Errorf("event %d differs\nlive:    %+v\ndecoded: %+v", l.Seq, l, g)
		}
	}
}

func kindList(evs []events.Event) []string {
	out := make([]string, len(evs))
	for i, e := range evs {
		out[i] = e.Kind
	}
	return out
}

// A capture cut off mid-session, as a killed agent leaves it, still decodes up to the cut.
func TestReplayStopsWhereTheCaptureEnds(t *testing.T) {
	path := filepath.Join(t.TempDir(), "agent.cap")
	live := liveScenario(t, path)
	recs, err := capture.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	half := recs[:len(recs)/2]
	log, _, err := Replay(half, Options{NamesWait: 100 * time.Millisecond})
	if err != nil {
		t.Fatal(err)
	}
	got := allEvents(log)
	if len(got) == 0 || len(got) >= len(live) {
		t.Fatalf("%d events from half the records, %d live", len(got), len(live))
	}
	for i, e := range got {
		if e.Kind != live[i].Kind || e.Seq != live[i].Seq {
			t.Fatalf("event %d is %s #%d, live %s #%d", i, e.Kind, e.Seq, live[i].Kind, live[i].Seq)
		}
	}
}

func TestReplayNeedsALogin(t *testing.T) {
	if _, _, err := Replay(nil, Options{}); err == nil {
		t.Fatal("an empty capture replayed")
	}
}

// ReplayWith hands every record to the observer, in order, after it was handled; the units it sees stand where the
// capture's clock puts them, so two replays of one capture see the same positions.
func TestReplayWithObservesEveryRecordUnderTheCapturesClock(t *testing.T) {
	recs := fixture(t, "m6-fight.twcap")
	positions := func() (int, map[packet.GUID][3]float32) {
		calls, last := 0, map[packet.GUID][3]float32{}
		_, _, err := ReplayWith(recs, Options{}, func(_ capture.Record, s *Session) {
			calls++
			if calls == len(recs) {
				for _, u := range s.Units(objects.Filter{Limit: 5000}) {
					last[u.GUID] = [3]float32{u.X, u.Y, u.Z}
				}
			}
		})
		if err != nil {
			t.Fatal(err)
		}
		return calls, last
	}
	n1, p1 := positions()
	n2, p2 := positions()
	if n1 != len(recs) || n2 != n1 || len(p1) < 10 || !reflect.DeepEqual(p1, p2) {
		t.Fatalf("%d and %d calls for %d records; %d and %d units; equal %v", n1, n2, len(recs), len(p1), len(p2), reflect.DeepEqual(p1, p2))
	}
	// Replay is ReplayWith with no observer.
	a, _, _ := Replay(recs, Options{})
	b, _, _ := ReplayWith(recs, Options{}, nil)
	// Not event for event: a group list that drops two members at once emits their `left` events in map order.
	count := func(l *events.Log) map[string]int {
		m := map[string]int{}
		for _, e := range allEvents(l) {
			m[e.Kind]++
		}
		return m
	}
	if ca, cb := count(a), count(b); !reflect.DeepEqual(ca, cb) || len(ca) < 5 {
		t.Fatalf("Replay and ReplayWith(nil) differ: %v and %v", ca, cb)
	}
}
