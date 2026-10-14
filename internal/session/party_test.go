package session

import (
	"bytes"
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/pjerra/turtle-agent/internal/events"
	"github.com/pjerra/turtle-agent/internal/game"
	"github.com/pjerra/turtle-agent/internal/game/gametest"
	"github.com/pjerra/turtle-agent/internal/objects"
	"github.com/pjerra/turtle-agent/internal/opcode"
	"github.com/pjerra/turtle-agent/internal/packet"
)

func fullStats(g packet.GUID, hp, maxHP uint16) []byte {
	mask := game.GroupUpdateStatus | game.GroupUpdateCurHP | game.GroupUpdateMaxHP | game.GroupUpdateLevel
	return packet.NewWriter().PackedGUID(g).U32(mask).U8(game.MemberOnline).U16(hp).U16(maxHP).U16(60).Bytes()
}

func waitFor(t *testing.T, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatal("condition not reached")
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func partyEvents(l *events.Log) string {
	var got []string
	for _, e := range ofKind(l, events.KindParty) {
		got = append(got, e.Data["event"].(string)+":"+e.Source)
	}
	return strings.Join(got, ",")
}

var gorm = game.GroupMember{Name: "Gorm", GUID: 0x30, Status: game.MemberOnline}

func TestInviteGroupListAndStats(t *testing.T) {
	h := start(t, Options{})
	h.enterWorld()
	if err := h.s.Invite(context.Background(), "Gorm"); err != nil {
		t.Fatal(err)
	}
	if p := h.expect(opcode.CMSGGroupInvite); !bytes.Equal(p.Body, []byte("Gorm\x00")) {
		t.Fatalf("% X", p.Body)
	}
	h.send(opcode.SMSGPartyCommandResult, packet.NewWriter().U32(0).CString("Gorm").U32(0).Bytes())
	h.send(opcode.SMSGGroupList, gametest.GroupList(me.GUID, gorm))
	if p := h.expect(opcode.CMSGRequestPartyMemberStats); packet.NewReader(p.Body).GUID() != 0x30 {
		t.Fatalf("stats request % X", p.Body)
	}
	h.send(opcode.SMSGPartyMemberStatsFull, fullStats(0x30, 500, 600))
	waitFor(t, func() bool { p := h.s.Party(); return len(p.Members) == 1 && p.Members[0].Health == 500 })
	p := h.s.Party()
	if !p.InGroup || p.Leader != me.GUID || p.Members[0].Name != "Gorm" || !p.Members[0].Online || p.Members[0].MaxHealth != 600 || p.Members[0].Level != 60 {
		t.Fatalf("%+v", p)
	}
	if !h.s.inParty(0x30) || !h.s.inParty(me.GUID) || h.s.inParty(0x31) {
		t.Fatal("inParty")
	}
	h.logout()
	if got := partyEvents(h.s.Log()); got != "result:,joined:Gorm" {
		t.Fatalf("party events %v", got)
	}
	if st := ofKind(h.s.Log(), events.KindPartyStats); len(st) != 1 || st[0].Source != "Gorm" {
		t.Fatalf("stats events %+v", st)
	}
}

func TestAcceptInvite(t *testing.T) {
	h := start(t, Options{})
	h.enterWorld()
	h.send(opcode.SMSGGroupInvite, []byte("Lia\x00"))
	waitFor(t, func() bool { return h.s.Party().PendingInvite == "Lia" })
	if err := h.s.AcceptInvite(context.Background()); err != nil {
		t.Fatal(err)
	}
	h.expect(opcode.CMSGGroupAccept)
	if err := h.s.AcceptInvite(context.Background()); err == nil || !strings.Contains(err.Error(), "no pending") {
		t.Fatalf("second accept: %v", err)
	}
	h.logout()
}

func TestLeaveGroupAndDisband(t *testing.T) {
	h := start(t, Options{})
	h.enterWorld()
	if err := h.s.LeaveGroup(context.Background()); err == nil {
		t.Fatal("left a group we are not in")
	}
	h.send(opcode.SMSGGroupList, gametest.GroupList(0x30, gorm))
	waitFor(t, func() bool { return h.s.Party().InGroup })
	if err := h.s.LeaveGroup(context.Background()); err != nil {
		t.Fatal(err)
	}
	h.expect(opcode.CMSGGroupDisband)
	h.send(opcode.SMSGGroupDestroyed, nil)
	waitFor(t, func() bool { return !h.s.Party().InGroup })
	h.logout()
}

// Ruling P4: a partial update reports the member's merged state, not zeros for the
// fields its mask leaves out.
func TestPartialStatsReportMergedState(t *testing.T) {
	h := start(t, Options{})
	h.enterWorld()
	h.send(opcode.SMSGGroupList, gametest.GroupList(me.GUID, gorm))
	h.send(opcode.SMSGPartyMemberStatsFull, fullStats(0x30, 500, 600))
	h.send(opcode.SMSGPartyMemberStats, packet.NewWriter().PackedGUID(0x30).U32(game.GroupUpdateCurHP).U16(0).Bytes())
	h.send(opcode.SMSGPartyMemberStats, packet.NewWriter().PackedGUID(0x30).U32(game.GroupUpdateStatus).U8(game.MemberOnline|game.MemberDead).Bytes())
	waitFor(t, func() bool { return len(ofKind(h.s.Log(), events.KindPartyStats)) == 3 })
	st := ofKind(h.s.Log(), events.KindPartyStats)
	hp := st[1].Data
	if hp["health"] != uint16(0) || hp["max_health"] != uint16(600) || hp["online"] != true || hp["dead"] != false {
		t.Fatalf("hp update %v", hp)
	}
	if d := st[2].Data; d["health"] != uint16(0) || d["max_health"] != uint16(600) || d["online"] != true || d["dead"] != true {
		t.Fatalf("status update %v", d)
	}
	if m := h.s.Party().Members[0]; m.Health != 0 || m.MaxHealth != 600 || !m.Dead || m.Level != 60 {
		t.Fatalf("%+v", m)
	}
	h.logout()
}

func TestGroupListLeftLeaderAndUninvite(t *testing.T) {
	h := start(t, Options{})
	h.enterWorld()
	lia := game.GroupMember{Name: "Lia", GUID: 0x31, Status: game.MemberOnline | game.MemberGhost}
	h.send(opcode.SMSGGroupList, gametest.GroupList(me.GUID, gorm, lia))
	waitFor(t, func() bool { return len(h.s.Party().Members) == 2 })
	if p := h.s.Party(); !p.Members[1].Dead || p.Members[0].Leader {
		t.Fatalf("%+v", p)
	}
	h.send(opcode.SMSGGroupList, gametest.GroupList(me.GUID, gorm))
	h.send(opcode.SMSGGroupSetLeader, []byte("Gorm\x00"))
	waitFor(t, func() bool { return h.s.Party().Leader == 0x30 })
	if p := h.s.Party(); len(p.Members) != 1 || !p.Members[0].Leader || h.s.inParty(0x31) {
		t.Fatalf("%+v", p)
	}
	h.send(opcode.SMSGGroupUninvite, nil)
	waitFor(t, func() bool { return !h.s.Party().InGroup })
	if p := h.s.Party(); p.Members == nil || len(p.Members) != 0 || p.Leader != 0 || h.s.inParty(0x30) {
		t.Fatalf("%+v", p)
	}
	h.logout()
	if got := partyEvents(h.s.Log()); got != "joined:Gorm,joined:Lia,left:Lia,leader:,uninvited:" {
		t.Fatalf("party events %v", got)
	}
}

func TestPartyNeedsWorld(t *testing.T) {
	h := start(t, Options{})
	h.expect(opcode.CMSGCharEnum)
	for name, err := range map[string]error{
		"invite": h.s.Invite(context.Background(), "Gorm"),
		"accept": h.s.AcceptInvite(context.Background()),
		"leave":  h.s.LeaveGroup(context.Background()),
	} {
		if !errors.Is(err, ErrNotInWorld) {
			t.Fatalf("%s before world: %v", name, err)
		}
	}
	if err := h.s.Invite(context.Background(), " "); err == nil {
		t.Fatal("blank invite accepted")
	}
}

// The server sends member stats only to members out of range (Player.cpp:21101), so a
// member the store sees reports the store's health, max health and level; one it does not
// see, or no longer sees, keeps the stats packet's values.
func TestPartyOverlaysVisibleMembers(t *testing.T) {
	h := start(t, Options{})
	h.enterWorld()
	brak := game.GroupMember{Name: "Brak", GUID: 0x31, Status: game.MemberOnline}
	h.send(opcode.SMSGGroupList, gametest.GroupList(me.GUID, gorm, brak))
	h.send(opcode.SMSGPartyMemberStatsFull, fullStats(0x30, 500, 600))
	h.send(opcode.SMSGPartyMemberStatsFull, fullStats(0x31, 300, 400))
	h.send(opcode.SMSGUpdateObject, unitCreate(0x30, objects.TypePlayer, me.X+3, me.Y, me.Z,
		map[uint16]uint32{objects.UnitHealth: 450, objects.UnitMaxHealth: 700, objects.UnitLevel: 61}))
	waitFor(t, func() bool {
		p := h.s.Party()
		return len(p.Members) == 2 && p.Members[0].Health == 450 && p.Members[1].Health == 300
	})
	p := h.s.Party()
	if g, b := p.Members[0], p.Members[1]; g.MaxHealth != 700 || g.Level != 61 || b.MaxHealth != 400 || b.Level != 60 {
		t.Fatalf("overlay %+v", p.Members)
	}
	h.send(opcode.SMSGDestroyObject, packet.NewWriter().GUID(0x30).Bytes())
	waitFor(t, func() bool { return h.s.Party().Members[0].Health == 500 })
	if g := h.s.Party().Members[0]; g.MaxHealth != 600 || g.Level != 60 {
		t.Fatalf("fallback %+v", g)
	}
	h.logout()
}

// Mana comes from the unit fields too: the stats packet is the only other source and the server stops sending it to a member
// in range (live Uldaman run 4: a priest read 29% mana for 14 rest gates while it drank to full). The stats stop here after the
// first packet; the unit fields change twice and the party list follows.
func TestPartyPowerFollowsTheUnitFields(t *testing.T) {
	h := start(t, Options{})
	h.enterWorld()
	h.send(opcode.SMSGGroupList, gametest.GroupList(me.GUID, gorm))
	mask := game.GroupUpdateStatus | game.GroupUpdateCurHP | game.GroupUpdateMaxHP | game.GroupUpdatePowerType | game.GroupUpdateCurPower | game.GroupUpdateMaxPower
	h.send(opcode.SMSGPartyMemberStats, packet.NewWriter().PackedGUID(0x30).U32(mask).U8(game.MemberOnline).U16(500).U16(600).U8(0).U16(1060).U16(3534).Bytes())
	waitFor(t, func() bool {
		m := h.s.Party().Members
		return len(m) == 1 && m[0].Power == 1060 && m[0].MaxPower == 3534
	})
	// The member comes into view: mana 1060 then, after a drink, 3534; max health 600 as before. No further stats packet.
	vals := func(power uint32) map[uint16]uint32 {
		return map[uint16]uint32{objects.UnitHealth: 500, objects.UnitMaxHealth: 600, objects.UnitBytes0: 5 << 8, objects.UnitPower1: power, objects.UnitMaxPower1: 3534}
	}
	h.send(opcode.SMSGUpdateObject, unitCreate(0x30, objects.TypePlayer, me.X+3, me.Y, me.Z, vals(1500)))
	waitFor(t, func() bool { return h.s.Party().Members[0].Power == 1500 })
	h.send(opcode.SMSGUpdateObject, valuesUpdate(0x30, map[uint16]uint32{objects.UnitPower1: 3534}))
	waitFor(t, func() bool { return h.s.Party().Members[0].Power == 3534 })
	if m := h.s.Party().Members[0]; m.MaxPower != 3534 || m.PowerType != 0 {
		t.Fatalf("%+v", m)
	}
	// Out of sight again: the last stats values.
	h.send(opcode.SMSGDestroyObject, packet.NewWriter().GUID(0x30).Bytes())
	waitFor(t, func() bool { return h.s.Party().Members[0].Power == 1060 })
	h.logout()
}

func TestFit16(t *testing.T) {
	if p, m := fit16(3534, 3534); p != 3534 || m != 3534 {
		t.Fatal(p, m)
	}
	if p, m := fit16(100000, 200000); p != 32767 || m != 65535 {
		t.Fatal(p, m)
	}
	if p, _ := fit16(900, 600); p != 600 {
		t.Fatal(p)
	}
}

// The Party flag is decided when the event is emitted, not when its name wait ends: a
// member who leaves inside the wait still flags the event.
func TestPartyFlagIsFixedAtEmit(t *testing.T) {
	h := start(t, Options{NamesWait: 300 * time.Millisecond})
	h.enterWorld()
	h.send(opcode.SMSGGroupList, gametest.GroupList(me.GUID, gorm))
	waitFor(t, func() bool { return h.s.Party().InGroup })
	h.s.names.mu.Lock()
	h.s.names.players[0x30] = "Gorm"
	h.s.names.mu.Unlock()
	_, err := h.s.do(context.Background(), func() (any, error) {
		h.s.emit(events.Event{Kind: events.KindDeath, SourceGUID: 0x30, TargetGUID: 0x99})
		return nil, nil
	})
	if err != nil {
		t.Fatal(err)
	}
	h.send(opcode.SMSGGroupDestroyed, nil)
	waitFor(t, func() bool { return !h.s.Party().InGroup })
	waitFor(t, func() bool { return len(ofKind(h.s.Log(), events.KindDeath)) == 1 })
	if d := ofKind(h.s.Log(), events.KindDeath)[0]; !d.Party {
		t.Fatalf("the member left inside the wait and the flag flipped: %+v", d)
	}
	h.logout()
}
