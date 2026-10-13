package session

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/pjerra/turtle-agent/internal/game"
	"github.com/pjerra/turtle-agent/internal/game/gametest"
	"github.com/pjerra/turtle-agent/internal/opcode"
	"github.com/pjerra/turtle-agent/internal/packet"
)

// Core cites in this file are tortoise-wow upstream/1181dev 5784fb0.

const (
	liaGUID  packet.GUID = 0x31
	brakGUID packet.GUID = 0x32
)

// member builds a list entry in 0-based subgroup sub.
func member(name string, g packet.GUID, sub uint8, assistant bool) game.GroupMember {
	f := sub
	if assistant {
		f |= game.GroupAssistant
	}
	return game.GroupMember{Name: name, GUID: g, Status: game.MemberOnline, Flags: f}
}

// groupList sends the list Group::SendUpdate builds for the player (Group.cpp:1411-1443).
func groupList(h *harness, typ, own uint8, leader packet.GUID, ms ...game.GroupMember) {
	h.t.Helper()
	h.send(opcode.SMSGGroupList, gametest.GroupListAs(typ, own, leader, ms...))
}

// asyncErr runs fn in the background, so the test can answer as the server.
func asyncErr(fn func() error) chan error {
	done := make(chan error, 1)
	go func() { done <- fn() }()
	return done
}

func finish(t *testing.T, done chan error) {
	t.Helper()
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("call did not return")
	}
}

func failWith(t *testing.T, done chan error, sub string) {
	t.Helper()
	select {
	case err := <-done:
		if err == nil || !strings.Contains(err.Error(), sub) {
			t.Fatalf("error %v, want %q", err, sub)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("call did not return")
	}
}

// noSetLeader fails when the session sent a promote the call should have skipped.
func noSetLeader(t *testing.T, h *harness) {
	t.Helper()
	if p, err := h.srv.Expect(opcode.CMSGGroupSetLeader, 50*time.Millisecond); err == nil {
		t.Fatalf("unexpected packet % X", p.Body)
	}
}

func TestPartyShowsRaidSubgroupsAndAssistants(t *testing.T) {
	h := start(t, Options{})
	h.enterWorld()
	groupList(h, game.GroupRaid, game.GroupAssistant|1, 0x30,
		member("Gorm", 0x30, 0, false), member("Lia", liaGUID, 1, true), member("Brak", brakGUID, 7, false))
	waitFor(t, func() bool { return h.s.Party().Raid })
	p := h.s.Party()
	if !p.Assistant || p.Subgroup != 2 || len(p.Members) != 3 {
		t.Fatalf("self %+v", p)
	}
	got := [][2]int{}
	for _, m := range p.Members {
		a := 0
		if m.Assistant {
			a = 1
		}
		got = append(got, [2]int{m.Subgroup, a})
	}
	if want := [][2]int{{1, 0}, {2, 1}, {8, 0}}; len(got) != 3 || got[0] != want[0] || got[1] != want[1] || got[2] != want[2] {
		t.Fatalf("members (subgroup, assistant) %v, want %v", got, want)
	}
	// Converting back to a party list drops them: a party's flags are all subgroup 0.
	groupList(h, game.GroupParty, 0, 0x30, member("Gorm", 0x30, 0, false))
	waitFor(t, func() bool { return !h.s.Party().Raid })
	if p := h.s.Party(); p.Assistant || p.Subgroup != 0 || p.Members[0].Subgroup != 0 {
		t.Fatalf("party %+v", p)
	}
	h.logout()
}

func TestPromote(t *testing.T) {
	h := start(t, Options{})
	h.enterWorld()
	inGroup(h)
	done := asyncErr(func() error { return h.s.Promote(context.Background(), 0, "gorm") })
	if p := h.expect(opcode.CMSGGroupSetLeader); string(p.Body) != string(game.BuildGroupSetLeader(0x30)) {
		t.Fatalf("% X", p.Body)
	}
	h.send(opcode.SMSGGroupSetLeader, []byte("Gorm\x00"))
	groupList(h, game.GroupParty, 0, 0x30, member("Gorm", 0x30, 0, false))
	finish(t, done)
	if p := h.s.Party(); p.Leader != 0x30 || !p.Members[0].Leader {
		t.Fatalf("%+v", p)
	}
	// Now Gorm leads: the player may not promote, and a stranger is no candidate.
	failWith(t, asyncErr(func() error { return h.s.Promote(context.Background(), me.GUID, "") }), "group leader")
	h.logout()
}

func TestPromoteRefusals(t *testing.T) {
	h := start(t, Options{})
	h.enterWorld()
	failWith(t, asyncErr(func() error { return h.s.Promote(context.Background(), 0, "Gorm") }), "needs a group")
	inGroup(h)
	failWith(t, asyncErr(func() error { return h.s.Promote(context.Background(), 0, "Nobody") }), "not in your group")
	failWith(t, asyncErr(func() error { return h.s.Promote(context.Background(), 0, "") }), "guid or name")
	if err := h.s.Promote(context.Background(), me.GUID, ""); err != nil { // already the leader
		t.Fatal(err)
	}
	noSetLeader(t, h)
	// The core ignores a promote of an offline member (GroupHandler.cpp:343-345): refused before sending.
	groupList(h, game.GroupParty, 0, me.GUID, member("Gorm", 0x30, 0, false), game.GroupMember{Name: "Lia", GUID: liaGUID})
	waitFor(t, func() bool { return len(h.s.Party().Members) == 2 })
	failWith(t, asyncErr(func() error { return h.s.Promote(context.Background(), 0, "Lia") }), "Lia is offline")
	noSetLeader(t, h)
	h.logout()
}

// The server drops a refused command without a word, so the call times out saying so.
func TestPromoteIgnoredByServer(t *testing.T) {
	h := start(t, Options{MarkWait: 100 * time.Millisecond})
	h.enterWorld()
	inGroup(h)
	done := asyncErr(func() error { return h.s.Promote(context.Background(), 0x30, "") })
	h.expect(opcode.CMSGGroupSetLeader)
	failWith(t, done, "no SMSG_GROUP_LIST")
	h.logout()
}

func TestConvertRaid(t *testing.T) {
	h := start(t, Options{})
	h.enterWorld()
	inGroup(h)
	done := asyncErr(func() error { return h.s.ConvertRaid(context.Background()) })
	if p := h.expect(opcode.CMSGGroupRaidConvert); len(p.Body) != 0 {
		t.Fatalf("% X", p.Body)
	}
	groupList(h, game.GroupRaid, 0, me.GUID, member("Gorm", 0x30, 0, false))
	finish(t, done)
	h.expect(opcode.MSGRaidTargetUpdate) // the icon list a raid joins ask for
	if !h.s.Party().Raid {
		t.Fatal("not a raid")
	}
	if err := h.s.ConvertRaid(context.Background()); err != nil { // already one
		t.Fatal(err)
	}
	h.logout()
}

func TestAssistantSetAndClear(t *testing.T) {
	h := start(t, Options{})
	h.enterWorld()
	groupList(h, game.GroupParty, 0, me.GUID, member("Gorm", 0x30, 0, false))
	h.expect(opcode.CMSGRequestPartyMemberStats)
	waitFor(t, func() bool { return h.s.Party().InGroup })
	failWith(t, asyncErr(func() error { return h.s.Assistant(context.Background(), 0, "Gorm", false) }), "needs a raid")

	groupList(h, game.GroupRaid, 0, me.GUID, member("Gorm", 0x30, 0, false))
	h.expect(opcode.MSGRaidTargetUpdate)
	waitFor(t, func() bool { return h.s.Party().Raid })
	done := asyncErr(func() error { return h.s.Assistant(context.Background(), 0x30, "", false) })
	if p := h.expect(opcode.CMSGGroupAssistantLeader); string(p.Body) != string(game.BuildGroupAssistant(0x30, true)) {
		t.Fatalf("set % X", p.Body)
	}
	groupList(h, game.GroupRaid, 0, me.GUID, member("Gorm", 0x30, 0, true))
	finish(t, done)
	if !h.s.Party().Members[0].Assistant {
		t.Fatal("Gorm is not an assistant")
	}

	done = asyncErr(func() error { return h.s.Assistant(context.Background(), 0, "GORM", true) })
	if p := h.expect(opcode.CMSGGroupAssistantLeader); string(p.Body) != string(game.BuildGroupAssistant(0x30, false)) {
		t.Fatalf("clear % X", p.Body)
	}
	groupList(h, game.GroupRaid, 0, me.GUID, member("Gorm", 0x30, 0, false))
	finish(t, done)

	// The leader may flag itself; its own flag arrives in the list's own flags.
	done = asyncErr(func() error { return h.s.Assistant(context.Background(), 0, me.Name, false) })
	if p := h.expect(opcode.CMSGGroupAssistantLeader); string(p.Body) != string(game.BuildGroupAssistant(me.GUID, true)) {
		t.Fatalf("self % X", p.Body)
	}
	groupList(h, game.GroupRaid, game.GroupAssistant, me.GUID, member("Gorm", 0x30, 0, false))
	finish(t, done)
	h.logout()
}

func TestAssistantNeedsTheLeader(t *testing.T) {
	h := start(t, Options{})
	h.enterWorld()
	groupList(h, game.GroupRaid, game.GroupAssistant, 0x30, member("Gorm", 0x30, 0, false), member("Lia", liaGUID, 0, false))
	waitFor(t, func() bool { return h.s.Party().Raid })
	// An assistant may not flag others (GroupHandler.cpp:608-610).
	failWith(t, asyncErr(func() error { return h.s.Assistant(context.Background(), liaGUID, "", false) }), "group leader")
	h.logout()
}

func TestRaidMove(t *testing.T) {
	h := start(t, Options{})
	h.enterWorld()
	groupList(h, game.GroupRaid, 0, me.GUID, member("Gorm", 0x30, 0, false), member("Lia", liaGUID, 0, false))
	h.expect(opcode.MSGRaidTargetUpdate)
	waitFor(t, func() bool { return h.s.Party().Raid })
	failWith(t, asyncErr(func() error { return h.s.RaidMove(context.Background(), 0, "Gorm", 9, "") }), "1 to 8")
	failWith(t, asyncErr(func() error { return h.s.RaidMove(context.Background(), 0, "Gorm", 0, "") }), "1 to 8")

	// Subgroup 2 on the wire is 1 (GroupHandler.cpp:524-535).
	done := asyncErr(func() error { return h.s.RaidMove(context.Background(), 0, "gorm", 2, "") })
	if p := h.expect(opcode.CMSGGroupChangeSubGroup); string(p.Body) != "Gorm\x00\x01" {
		t.Fatalf("% X", p.Body)
	}
	groupList(h, game.GroupRaid, 0, me.GUID, member("Gorm", 0x30, 1, false), member("Lia", liaGUID, 0, false))
	finish(t, done)
	if m := h.s.Party().Members; m[0].Subgroup != 2 || m[1].Subgroup != 1 {
		t.Fatalf("%+v", m)
	}
	if err := h.s.RaidMove(context.Background(), 0x30, "", 2, ""); err != nil { // already there
		t.Fatal(err)
	}
	h.logout()
}

func TestRaidMoveFullSubgroupAndSwap(t *testing.T) {
	h := start(t, Options{})
	h.enterWorld()
	// Subgroup 1 holds the player and Gorm, Lia, Brak, Dane: five. Echo waits in subgroup 2.
	full := []game.GroupMember{member("Gorm", 0x30, 0, false), member("Lia", liaGUID, 0, false), member("Brak", brakGUID, 0, false),
		member("Dane", 0x33, 0, false), member("Echo", 0x34, 1, false)}
	groupList(h, game.GroupRaid, 0, me.GUID, full...)
	h.expect(opcode.MSGRaidTargetUpdate)
	waitFor(t, func() bool { return len(h.s.Party().Members) == 5 })
	failWith(t, asyncErr(func() error { return h.s.RaidMove(context.Background(), 0x34, "", 1, "") }), "is full")
	failWith(t, asyncErr(func() error { return h.s.RaidMove(context.Background(), 0x34, "", 1, "Echo") }), "itself")
	failWith(t, asyncErr(func() error { return h.s.RaidMove(context.Background(), 0x34, "", 1, "Nobody") }), "not in your group")
	failWith(t, asyncErr(func() error { return h.s.RaidMove(context.Background(), 0x34, "", 3, "Lia") }), "swap_with must name")

	// Swapping trades places: Echo goes to subgroup 1, Lia to 2 (GroupHandler.cpp:559-596).
	done := asyncErr(func() error { return h.s.RaidMove(context.Background(), 0x34, "", 1, "Lia") })
	if p := h.expect(opcode.CMSGGroupSwapSubGroup); string(p.Body) != "Echo\x00Lia\x00" {
		t.Fatalf("% X", p.Body)
	}
	swapped := append([]game.GroupMember{}, full...)
	swapped[1], swapped[4] = member("Lia", liaGUID, 1, false), member("Echo", 0x34, 0, false)
	groupList(h, game.GroupRaid, 0, me.GUID, swapped...)
	finish(t, done)
	h.logout()
}

func TestRaidMoveNeedsRank(t *testing.T) {
	h := start(t, Options{})
	h.enterWorld()
	groupList(h, game.GroupRaid, 0, 0x30, member("Gorm", 0x30, 0, false))
	waitFor(t, func() bool { return h.s.Party().Raid })
	failWith(t, asyncErr(func() error { return h.s.RaidMove(context.Background(), 0x30, "", 2, "") }), "leader or an assistant")
	// An assistant may move members (GroupHandler.cpp:537-543).
	groupList(h, game.GroupRaid, game.GroupAssistant, 0x30, member("Gorm", 0x30, 0, false))
	waitFor(t, func() bool { return h.s.Party().Assistant })
	done := asyncErr(func() error { return h.s.RaidMove(context.Background(), me.GUID, "", 3, "") })
	if p := h.expect(opcode.CMSGGroupChangeSubGroup); string(p.Body) != me.Name+"\x00\x02" {
		t.Fatalf("% X", p.Body)
	}
	groupList(h, game.GroupRaid, game.GroupAssistant|2, 0x30, member("Gorm", 0x30, 0, false))
	finish(t, done)
	h.logout()
}

func TestDisband(t *testing.T) {
	h := start(t, Options{})
	h.enterWorld()
	failWith(t, asyncErr(func() error { return h.s.Disband(context.Background()) }), "needs a group")
	groupList(h, game.GroupParty, 0, 0x30, member("Gorm", 0x30, 0, false))
	waitFor(t, func() bool { return h.s.Party().InGroup })
	failWith(t, asyncErr(func() error { return h.s.Disband(context.Background()) }), "group leader")

	groupList(h, game.GroupRaid, 0, me.GUID, member("Gorm", 0x30, 0, false), member("Lia", liaGUID, 1, false))
	h.expect(opcode.MSGRaidTargetUpdate)
	waitFor(t, func() bool { return len(h.s.Party().Members) == 2 })
	done := asyncErr(func() error { return h.s.Disband(context.Background()) })
	for _, g := range []packet.GUID{0x30, liaGUID} {
		if p := h.expect(opcode.CMSGGroupUninviteGuid); string(p.Body) != string(game.BuildGroupUninviteGUID(g)) {
			t.Fatalf("uninvite % X, want %s", p.Body, g)
		}
	}
	h.send(opcode.SMSGGroupList, make([]byte, 24)) // the empty list Group::Disband and RemoveMember send
	finish(t, done)
	if h.s.Party().InGroup {
		t.Fatal("still in a group")
	}
	h.logout()
}

func TestGroupControlNeedsWorld(t *testing.T) {
	h := start(t, Options{})
	h.expect(opcode.CMSGCharEnum)
	for name, err := range map[string]error{
		"promote":  h.s.Promote(context.Background(), 0x30, ""),
		"assist":   h.s.Assistant(context.Background(), 0x30, "", false),
		"convert":  h.s.ConvertRaid(context.Background()),
		"raidmove": h.s.RaidMove(context.Background(), 0x30, "", 2, ""),
		"disband":  h.s.Disband(context.Background()),
	} {
		if !errors.Is(err, ErrNotInWorld) {
			t.Fatalf("%s before world: %v", name, err)
		}
	}
}

// loot_method: master_loot names the player by default; a group already on the wanted rules sends nothing; only the leader may.
func TestSetLootMethod(t *testing.T) {
	h := start(t, Options{})
	h.enterWorld()
	failWith(t, asyncErr(func() error { return h.s.SetLootMethod(context.Background(), "free_for_all", 0, "", 0) }), "needs a group")
	inGroup(h)
	failWith(t, asyncErr(func() error { return h.s.SetLootMethod(context.Background(), "everyone", 0, "", 0) }), "unknown method")
	done := asyncErr(func() error { return h.s.SetLootMethod(context.Background(), "master_loot", 0, "", 0) })
	if p := h.expect(opcode.CMSGLootMethod); string(p.Body) != string(game.BuildLootMethod(game.LootMaster, me.GUID, 2)) {
		t.Fatalf("% X", p.Body)
	}
	h.send(opcode.SMSGGroupList, gametest.GroupListLoot(me.GUID, game.LootMaster, me.GUID, 2, gorm))
	finish(t, done)
	if p := h.s.Party(); p.LootMethod != "master_loot" || p.LootMaster != me.GUID || p.LootThreshold != 2 {
		t.Fatalf("%+v", p)
	}
	if err := h.s.SetLootMethod(context.Background(), "master_loot", 0, "", 2); err != nil {
		t.Fatal(err)
	}
	if p, err := h.srv.Expect(opcode.CMSGLootMethod, 50*time.Millisecond); err == nil {
		t.Fatalf("unexpected packet % X", p.Body)
	}
	h.send(opcode.SMSGGroupList, gametest.GroupList(0x30, gorm))
	waitFor(t, func() bool { return h.s.Party().Leader == 0x30 })
	failWith(t, asyncErr(func() error { return h.s.SetLootMethod(context.Background(), "free_for_all", 0, "", 0) }), "group leader")
	h.logout()
}
