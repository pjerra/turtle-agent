package session

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/TortoiseBots/turtle-agent/internal/events"
	"github.com/TortoiseBots/turtle-agent/internal/game"
	"github.com/TortoiseBots/turtle-agent/internal/game/gametest"
	"github.com/TortoiseBots/turtle-agent/internal/opcode"
	"github.com/TortoiseBots/turtle-agent/internal/packet"
)

// inGroup puts the player in a group it leads, with Gorm.
func inGroup(h *harness) {
	h.t.Helper()
	h.send(opcode.SMSGGroupList, gametest.GroupList(me.GUID, gorm))
	h.expect(opcode.CMSGRequestPartyMemberStats)
	waitFor(h.t, func() bool { return h.s.Party().InGroup })
}

// markAsync runs Mark in the background, so the test can answer as the server.
func markAsync(h *harness, g packet.GUID, name, icon string) chan error {
	done := make(chan error, 1)
	go func() {
		_, err := h.s.Mark(context.Background(), g, name, icon)
		done <- err
	}()
	return done
}

func marksOf(l *events.Log) string {
	var got []string
	for _, e := range ofKind(l, events.KindRaidMark) {
		got = append(got, e.Data["action"].(string)+":"+e.Data["icon"].(string)+":"+e.Target)
	}
	return strings.Join(got, ",")
}

func TestMarkSetsAndClearsAnIcon(t *testing.T) {
	h := start(t, Options{})
	h.enterWorld()
	wolfNearby(h)
	if _, err := h.s.Mark(context.Background(), wolfGUID, "", "skull"); err == nil || !strings.Contains(err.Error(), "group") {
		t.Fatalf("mark outside a group: %v", err)
	}
	inGroup(h)
	if _, err := h.s.Mark(context.Background(), wolfGUID, "", "bone"); err == nil || !strings.Contains(err.Error(), "skull") {
		t.Fatalf("bad icon: %v", err)
	}

	done := markAsync(h, 0, "young wolf", "skull")
	if p := h.expect(opcode.MSGRaidTargetUpdate); string(p.Body) != string(game.BuildRaidTargetUpdate(7, wolfGUID)) {
		t.Fatalf("set % X", p.Body)
	}
	h.send(opcode.MSGRaidTargetUpdate, gametest.RaidTargetDelta(7, wolfGUID))
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	if m := h.s.Party().Marks; len(m) != 1 || m["skull"] != wolfGUID {
		t.Fatalf("marks %v", m)
	}

	// Clearing sends the icon the unit holds with a zero guid (Group.cpp:1296-1314).
	done = markAsync(h, wolfGUID, "", "clear")
	if p := h.expect(opcode.MSGRaidTargetUpdate); string(p.Body) != string(game.BuildRaidTargetUpdate(7, 0)) {
		t.Fatalf("clear % X", p.Body)
	}
	h.send(opcode.MSGRaidTargetUpdate, gametest.RaidTargetDelta(7, 0))
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	if m := h.s.Party().Marks; len(m) != 0 {
		t.Fatalf("marks after clear %v", m)
	}
	if _, err := h.s.Mark(context.Background(), wolfGUID, "", "clear"); err == nil || !strings.Contains(err.Error(), "no raid icon") {
		t.Fatalf("clear an unmarked unit: %v", err)
	}
	h.logout()
	if got := marksOf(h.s.Log()); got != "set:skull:Young Wolf,cleared:skull:Young Wolf" {
		t.Fatalf("raid_mark events %s", got)
	}
}

// A mark the server does not echo (the player is neither leader nor assistant,
// GroupHandler.cpp:494-498) fails after the wait instead of reporting success.
func TestMarkWithoutEchoTimesOut(t *testing.T) {
	h := start(t, Options{MarkWait: 100 * time.Millisecond})
	h.enterWorld()
	wolfNearby(h)
	inGroup(h)
	done := markAsync(h, wolfGUID, "", "moon")
	h.expect(opcode.MSGRaidTargetUpdate)
	if err := <-done; err == nil || !strings.Contains(err.Error(), "leader or an assistant") {
		t.Fatalf("err = %v", err)
	}
}

// Marks set by others arrive as deltas and as the full list the server sends after a group
// list; the log reports what changed, and leaving the group forgets the icons.
func TestMarksFromTheServer(t *testing.T) {
	h := start(t, Options{})
	h.enterWorld()
	wolfNearby(h)
	inGroup(h)
	h.send(opcode.MSGRaidTargetUpdate, gametest.RaidTargetList([8]packet.GUID{7: wolfGUID, 4: gorm.GUID}))
	waitFor(t, func() bool { return len(h.s.Party().Marks) == 2 })
	// The same list again (after the next group list) changes nothing.
	h.send(opcode.MSGRaidTargetUpdate, gametest.RaidTargetList([8]packet.GUID{7: wolfGUID, 4: gorm.GUID}))
	// Cross moves onto the wolf: the server first clears skull from it.
	h.send(opcode.MSGRaidTargetUpdate, gametest.RaidTargetDelta(7, 0))
	h.send(opcode.MSGRaidTargetUpdate, gametest.RaidTargetDelta(6, wolfGUID))
	// Moon passes from Gorm to the wolf in one delta.
	h.send(opcode.MSGRaidTargetUpdate, gametest.RaidTargetDelta(4, wolfGUID))
	waitFor(t, func() bool { return h.s.Party().Marks["moon"] == wolfGUID })
	if m := h.s.Party().Marks; len(m) != 2 || m["cross"] != wolfGUID {
		t.Fatalf("marks %v", m)
	}
	h.send(opcode.SMSGGroupDestroyed, nil)
	waitFor(t, func() bool { return !h.s.Party().InGroup })
	if m := h.s.Party().Marks; len(m) != 0 {
		t.Fatalf("marks after disband %v", m)
	}
	h.logout()
	want := "set:moon:Gorm,set:skull:Young Wolf,cleared:skull:Young Wolf,set:cross:Young Wolf,cleared:moon:Gorm,set:moon:Young Wolf"
	if got := marksOf(h.s.Log()); got != want {
		t.Fatalf("raid_mark events\n got %s\nwant %s", got, want)
	}
	if e := ofKind(h.s.Log(), events.KindRaidMark); !e[0].Party || e[0].Data["icon_id"] != uint8(4) {
		t.Fatalf("first event %+v", e[0])
	}
}

// A raid gets no icon list after its group lists (Group.cpp:1385-1403 resends it only to
// parties), so the session asks once (0xFF) when it joins a raid or its party converts.
func TestRaidAsksForTheIconListOnce(t *testing.T) {
	h := start(t, Options{})
	h.enterWorld()
	wolfNearby(h)
	inGroup(h) // a party: no request
	h.send(opcode.SMSGGroupList, gametest.GroupListAs(game.GroupRaid, 0, me.GUID, gorm))
	if p := h.expect(opcode.MSGRaidTargetUpdate); string(p.Body) != string(game.BuildRaidTargetRequest()) {
		t.Fatalf("request % X", p.Body)
	}
	h.send(opcode.MSGRaidTargetUpdate, gametest.RaidTargetList([8]packet.GUID{7: wolfGUID}))
	h.send(opcode.SMSGGroupList, gametest.GroupListAs(game.GroupRaid, 0, me.GUID, gorm)) // no second request
	waitFor(t, func() bool { return h.s.Party().Marks["skull"] == wolfGUID })
	if !h.s.Party().Raid {
		t.Fatal("party.raid")
	}
	// The next MSG_RAID_TARGET_UPDATE the client sends is the mark, not another request.
	done := markAsync(h, wolfGUID, "", "cross")
	if p := h.expect(opcode.MSGRaidTargetUpdate); string(p.Body) != string(game.BuildRaidTargetUpdate(6, wolfGUID)) {
		t.Fatalf("mark % X", p.Body)
	}
	h.send(opcode.MSGRaidTargetUpdate, gametest.RaidTargetDelta(7, 0))
	h.send(opcode.MSGRaidTargetUpdate, gametest.RaidTargetDelta(6, wolfGUID))
	if err := <-done; err != nil {
		t.Fatal(err)
	}
}

// Only the leader or an assistant may mark (GroupHandler.cpp:494-496): anyone else fails at
// once, without a packet; an assistant (own flags 0x80) marks.
func TestMarkNeedsLeaderOrAssistant(t *testing.T) {
	h := start(t, Options{MarkWait: 10 * time.Second})
	h.enterWorld()
	wolfNearby(h)
	h.send(opcode.SMSGGroupList, gametest.GroupList(gorm.GUID, gorm))
	h.expect(opcode.CMSGRequestPartyMemberStats)
	waitFor(t, func() bool { return h.s.Party().InGroup })
	began := time.Now()
	if _, err := h.s.Mark(context.Background(), wolfGUID, "", "skull"); err == nil || !strings.Contains(err.Error(), "leader or an assistant") {
		t.Fatalf("err = %v", err)
	}
	if time.Since(began) > time.Second {
		t.Fatal("waited for an echo that cannot come")
	}
	if p, err := h.srv.Expect(opcode.MSGRaidTargetUpdate, 200*time.Millisecond); err == nil {
		t.Fatalf("sent % X", p.Body)
	}
	// Promoted to assistant in a raid: the list request, then the mark.
	h.send(opcode.SMSGGroupList, gametest.GroupListAs(game.GroupRaid, 0x80, gorm.GUID, gorm))
	if p := h.expect(opcode.MSGRaidTargetUpdate); string(p.Body) != string(game.BuildRaidTargetRequest()) {
		t.Fatalf("request % X", p.Body)
	}
	waitFor(t, func() bool { return h.s.Party().Assistant })
	done := markAsync(h, wolfGUID, "", "skull")
	if p := h.expect(opcode.MSGRaidTargetUpdate); string(p.Body) != string(game.BuildRaidTargetUpdate(7, wolfGUID)) {
		t.Fatalf("mark % X", p.Body)
	}
	h.send(opcode.MSGRaidTargetUpdate, gametest.RaidTargetDelta(7, wolfGUID))
	if err := <-done; err != nil {
		t.Fatal(err)
	}
}
