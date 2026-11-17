package session

import (
	"context"
	"fmt"
	"strings"
	"testing"

	"github.com/TortoiseBots/turtle-agent/internal/events"
	"github.com/TortoiseBots/turtle-agent/internal/game"
	"github.com/TortoiseBots/turtle-agent/internal/game/gametest"
	"github.com/TortoiseBots/turtle-agent/internal/opcode"
	"github.com/TortoiseBots/turtle-agent/internal/packet"
)

func slot(n uint32) *uint32 { return &n }

// lootLine renders a loot event as who:choice[:dice] #roll_id item.
func lootLine(e events.Event) string {
	s := e.Source + ":" + fmt.Sprint(e.Data["choice"])
	if n, ok := e.Data["roll"]; ok {
		s += fmt.Sprintf(":%v", n)
	}
	return fmt.Sprintf("%s #%v %v", s, e.Data["roll_id"], e.Data["item_entry"])
}

func lootLines(l *events.Log, kind string) string {
	var got []string
	for _, e := range ofKind(l, kind) {
		got = append(got, lootLine(e))
	}
	return strings.Join(got, ",")
}

func TestLootRollsAndVotes(t *testing.T) {
	ctx := context.Background()
	h := start(t, Options{})
	h.enterWorld()
	wolfNearby(h)
	inGroup(h)
	if _, err := h.s.Roll(ctx, RollRef{Slot: slot(1)}, "greed"); err == nil || !strings.Contains(err.Error(), "no open roll") {
		t.Fatalf("roll before any start: %v", err)
	}
	h.send(opcode.SMSGLootStartRoll, gametest.LootStartRoll(wolfGUID, 1, 2589, 0, 60000))
	waitFor(t, func() bool { return len(ofKind(h.s.Log(), events.KindLootStart)) == 1 })
	if _, err := h.s.Roll(ctx, RollRef{Slot: slot(1)}, "disenchant"); err == nil || !strings.Contains(err.Error(), "need, greed or pass") {
		t.Fatalf("bad choice: %v", err)
	}
	if _, err := h.s.Roll(ctx, RollRef{}, "greed"); err == nil || !strings.Contains(err.Error(), "roll_id or an item_slot") {
		t.Fatalf("no reference: %v", err)
	}
	info, err := h.s.Roll(ctx, RollRef{Slot: slot(1)}, "Greed")
	if err != nil || info != (RollInfo{ID: 1, Looted: wolfGUID, Slot: 1, Item: 2589, Choice: "greed"}) {
		t.Fatalf("%+v %v", info, err)
	}
	if p := h.expect(opcode.CMSGLootRoll); string(p.Body) != string(game.BuildLootRoll(wolfGUID, 1, game.RollGreed)) {
		t.Fatalf("% X", p.Body)
	}
	// The server reports each vote, then the need dice and the winner (Group.cpp:940-987, :1150-1169).
	h.send(opcode.SMSGLootRoll, gametest.LootRoll(wolfGUID, 1, me.GUID, 2589, 0, 128, 2))
	h.send(opcode.SMSGLootRoll, gametest.LootRoll(wolfGUID, 1, gorm.GUID, 2589, 0, 0, 0))
	h.send(opcode.SMSGLootRoll, gametest.LootRoll(wolfGUID, 1, gorm.GUID, 2589, 0, 57, 1))
	h.send(opcode.SMSGLootRollWon, gametest.LootRollWon(wolfGUID, 1, 2589, 0, gorm.GUID, 57, 1))
	waitFor(t, func() bool { return len(ofKind(h.s.Log(), events.KindLootWon)) == 1 })
	if _, err := h.s.Roll(ctx, RollRef{ID: 1}, "need"); err == nil || !strings.Contains(err.Error(), "no open roll") {
		t.Fatalf("roll after the win: %v", err)
	}

	// Two corpses with an item in slot 0: the slot alone is ambiguous.
	other := packet.GUID(0xF13000012B000002)
	h.send(opcode.SMSGLootStartRoll, gametest.LootStartRoll(wolfGUID, 0, 4537, 0, 60000))
	h.send(opcode.SMSGLootStartRoll, gametest.LootStartRoll(other, 0, 4538, 0, 60000))
	waitFor(t, func() bool { return len(ofKind(h.s.Log(), events.KindLootStart)) == 3 })
	if _, err := h.s.Roll(ctx, RollRef{Slot: slot(0)}, "pass"); err == nil || !strings.Contains(err.Error(), "roll_id") {
		t.Fatalf("ambiguous slot: %v", err)
	}
	if info, err := h.s.Roll(ctx, RollRef{Slot: slot(0), Looted: other}, "pass"); err != nil || info.ID != 3 || info.Item != 4538 {
		t.Fatalf("%+v %v", info, err)
	}
	if p := h.expect(opcode.CMSGLootRoll); string(p.Body) != string(game.BuildLootRoll(other, 0, game.RollPass)) {
		t.Fatalf("% X", p.Body)
	}
	if info, err := h.s.Roll(ctx, RollRef{ID: 2}, "need"); err != nil || info.Looted != wolfGUID || info.Slot != 0 {
		t.Fatalf("%+v %v", info, err)
	}
	h.expect(opcode.CMSGLootRoll)
	h.send(opcode.SMSGLootAllPassed, gametest.LootAllPassed(other, 0, 4538, 0))
	h.logout()

	l := h.s.Log()
	if got := lootLines(l, events.KindLootStart); got != "Young Wolf:<nil> #1 2589,Young Wolf:<nil> #2 4537,Young Wolf:<nil> #3 4538" {
		t.Fatalf("starts %s", got)
	}
	if got := lootLines(l, events.KindLootRoll); got != "Harnessb:greed #1 2589,Gorm:need #1 2589,Gorm:need:57 #1 2589" {
		t.Fatalf("rolls %s", got)
	}
	if got := lootLines(l, events.KindLootWon); got != "Gorm:need:57 #1 2589" {
		t.Fatalf("won %s", got)
	}
	if got := lootLines(l, events.KindLootAllPassed); got != ":<nil> #3 4538" {
		t.Fatalf("all passed %s", got)
	}
	start := ofKind(l, events.KindLootStart)[0]
	if start.Data["countdown_ms"] != uint32(60000) || start.Data["item_slot"] != uint32(1) || start.Data["looted"] != wolfGUID || start.Party {
		t.Fatalf("start %+v", start)
	}
	if r := ofKind(l, events.KindLootRoll); !r[1].Party || r[1].Data["looted"] != wolfGUID {
		t.Fatalf("gorm's vote %+v", r[1])
	}
}
