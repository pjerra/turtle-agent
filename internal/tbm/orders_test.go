package tbm

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/pjerra/turtle-agent/internal/events"
)

func TestParsePending(t *testing.T) {
	p, ok := ParsePending("TBM:ACTION_PENDING|cmd loot|bot:Healy|24|0xF130000C5E0012AB")
	if !ok || p.Intent != "cmd loot" || p.Bot != "Healy" || p.BudgetS != 24 || p.GUID != "0xF130000C5E0012AB" {
		t.Fatalf("%+v %v", p, ok)
	}
	for _, bad := range []string{"TBM:ACTION_PENDING|cmd loot|party|24|0", "TBM:ACTION_PENDING|cmd loot|bot:A|x|0", "TBM:ACTION_PENDING|cmd loot|bot:A|1", "TBM:ACTION_ACK|cmd loot|bot:A|1|-"} {
		if _, ok := ParsePending(bad); ok {
			t.Errorf("%q accepted", bad)
		}
	}
}

func TestParseLoot(t *testing.T) {
	l, ok := ParseLoot("7740:1,7741:2,copper:35")
	if !ok || len(l.Items) != 2 || !l.Has(7740) || !l.Has(7741) || l.Has(1) || l.Copper != 35 {
		t.Fatalf("%+v %v", l, ok)
	}
	if l, ok := ParseLoot("-"); !ok || len(l.Items) != 0 {
		t.Fatalf("%+v %v", l, ok)
	}
	for _, bad := range []string{"opened", "7740", "x:1", "7740:y"} {
		if _, ok := ParseLoot(bad); ok {
			t.Errorf("%q accepted", bad)
		}
	}
}

func TestCheckCommandAcceptsTheOrders(t *testing.T) {
	for _, ok := range []string{"cmd loot 0xF130000C5E0012AB Healy 7741", "cmd open 0x1 Healy", "cmd use 7740 Healy", "cmd use 7740 Healy 0xF11", "cmd lootmethod ffa", "cmd lootmethod group 2"} {
		if err := CheckCommand(ok); err != nil {
			t.Errorf("%q: %v", ok, err)
		}
	}
}

func fastCommander(l *fakeLink) *Commander {
	c := NewCommander(l)
	c.Wait, c.PendingSlack = 150*time.Millisecond, 150*time.Millisecond
	return c
}

func sys(l *fakeLink, line string) {
	l.log.Append(events.Event{Kind: events.KindSystem, Text: line, Data: map[string]any{"raw": line}})
}

func TestOrderImmediateAck(t *testing.T) {
	l := newFakeLink()
	l.reply = func(string) []string { return []string{"TBM:ACTION_ACK|cmd loot|bot:Healy|1|7741:1,copper:12"} }
	r, err := fastCommander(l).Order(context.Background(), ".bot cmd loot 0x1 Healy 7741")
	if err != nil || !r.OK || r.Kind != KindAck || r.Loot == nil || !r.Loot.Has(7741) || r.Loot.Copper != 12 || r.Pending != nil || r.Bot != "Healy" {
		t.Fatalf("%v %+v", err, r)
	}
}

func TestOrderPendingThenAck(t *testing.T) {
	l := newFakeLink()
	l.reply = func(string) []string { return []string{"TBM:ACTION_PENDING|cmd open|bot:Healy|12|0x7B"} }
	c := fastCommander(l)
	go func() {
		time.Sleep(400 * time.Millisecond) // later than Wait: only the PENDING budget keeps the order waiting
		sys(l, "TBM:ACTION_ACK|cmd open|bot:Healy|1|opened")
	}()
	r, err := c.Order(context.Background(), "cmd open 0x7B Healy")
	if err != nil || !r.OK || r.Pending == nil || r.Pending.BudgetS != 12 || !r.Opened {
		t.Fatalf("%v %+v", err, r)
	}
}

func TestOrderErrCodes(t *testing.T) {
	for _, code := range []string{"busy", "too-far", "no-item", "cancelled", "unavailable", "in-combat", "locked", "failed"} {
		l := newFakeLink()
		l.reply = func(string) []string {
			return []string{"TBM:ACTION_PENDING|cmd use|bot:Healy|5|0", "TBM:ACTION_ERR|cmd use|" + code + "|because"}
		}
		r, err := fastCommander(l).Order(context.Background(), "cmd use 7740 Healy")
		if err != nil || r.OK || r.Kind != KindErr || r.Code() != code || !strings.Contains(r.Describe(), "because") {
			t.Fatalf("%s: %v %+v", code, err, r)
		}
	}
}

func TestOrderTimeouts(t *testing.T) {
	l := newFakeLink()
	r, err := fastCommander(l).Order(context.Background(), "cmd use 7740 Healy")
	if err != nil || r.Kind != KindTimeout || r.Code() != "timeout" || r.Pending != nil {
		t.Fatalf("%v %+v", err, r)
	}
	l.reply = func(string) []string { return []string{"TBM:ACTION_PENDING|cmd use|bot:Healy|0|0"} }
	r, err = fastCommander(l).Order(context.Background(), "cmd use 7740 Healy")
	if err != nil || r.Kind != KindTimeout || r.Pending == nil || !strings.Contains(r.Describe(), "reported running") {
		t.Fatalf("%v %+v", err, r)
	}
}

func TestOrderIgnoresOtherBots(t *testing.T) {
	l := newFakeLink()
	l.reply = func(string) []string {
		return []string{"TBM:ACTION_ACK|cmd use|bot:Magey|1|spell:5", "TBM:ACTION_PENDING|cmd use|bot:Magey|9|0", "TBM:ACTION_ACK|cmd use|bot:Healy|1|spell:9594"}
	}
	r, err := fastCommander(l).Order(context.Background(), "cmd use 7740 Healy")
	if err != nil || !r.OK || r.Spell != 9594 || r.Pending != nil {
		t.Fatalf("%v %+v", err, r)
	}
}

func TestOrderLootMethodAndUseWithoutSpell(t *testing.T) {
	l := newFakeLink()
	l.reply = func(string) []string { return []string{"TBM:ACTION_ACK|cmd lootmethod|party|5|ffa:2"} }
	r, _ := fastCommander(l).Order(context.Background(), "cmd lootmethod ffa")
	if !r.OK || r.Method != "ffa" || r.Threshold != "2" {
		t.Fatalf("%+v", r)
	}
	l.reply = func(string) []string { return []string{"TBM:ACTION_ACK|cmd use|bot:Healy|1|-"} }
	r, _ = fastCommander(l).Order(context.Background(), "cmd use 1 Healy")
	if !r.OK || r.Spell != 0 {
		t.Fatalf("%+v", r)
	}
}

func TestOrderContextEnds(t *testing.T) {
	l := newFakeLink()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Millisecond)
	defer cancel()
	c := NewCommander(l)
	c.Wait = 5 * time.Second
	if _, err := c.Order(ctx, "cmd open 0x1 Healy"); err == nil {
		t.Fatal("a stopped context is an error")
	}
}

func TestOrderRefusesBadCommand(t *testing.T) {
	l := newFakeLink()
	if _, err := fastCommander(l).Order(context.Background(), "cmd loot 0x1 Healy .gm on"); err == nil || len(l.sent) != 0 {
		t.Fatalf("%v %v", err, l.sent)
	}
}

func TestBehavior(t *testing.T) {
	l := newFakeLink()
	l.reply = func(s string) []string {
		return []string{"TBM:ACTION_ACK|behavior loot|bot:Healy|1|off"}
	}
	r, err := fastCommander(l).Behavior(context.Background(), "Healy", "loot", false)
	if err != nil || r.Kind != KindAck || l.sent[0] != ".bot behavior Healy loot off" {
		t.Fatalf("%v %+v %v", err, r, l.sent)
	}
	if _, err := fastCommander(l).Behavior(context.Background(), "He ly", "loot", false); err == nil {
		t.Fatal("bad bot")
	}
}
