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
	// An order for the whole party (cmd ritual) says party where the bot would be.
	if p, ok := ParsePending("TBM:ACTION_PENDING|cmd ritual|party|41|1234"); !ok || p.Intent != "cmd ritual" || p.Bot != "" || p.BudgetS != 41 || p.GUID != "1234" {
		t.Fatalf("%+v %v", p, ok)
	}
	for _, bad := range []string{"TBM:ACTION_PENDING|cmd loot|party|x|0", "TBM:ACTION_PENDING|cmd loot|bot:|24|0", "TBM:ACTION_PENDING|cmd loot|bot:A|x|0", "TBM:ACTION_PENDING|cmd loot|bot:A|1", "TBM:ACTION_ACK|cmd loot|bot:A|1|-"} {
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

func TestEntryArgsAreCommands(t *testing.T) {
	at := [3]float32{-235, 262.5, -48}
	for cmd, want := range map[string]string{
		"cmd open " + EntryArg(124371, &at) + " Healy":   "cmd open entry:124371@-235.00,262.50,-48.00 Healy",
		"cmd loot " + EntryArg(124371, nil) + " Healy 7": "cmd loot entry:124371 Healy 7",
		"cmd state " + EntryArg(124372, &at):             "cmd state entry:124372@-235.00,262.50,-48.00",
		"cmd use 7740 Healy " + EntryArg(5, &at):         "cmd use 7740 Healy entry:5@-235.00,262.50,-48.00",
	} {
		if cmd != want {
			t.Errorf("%q != %q", cmd, want)
		}
		if err := CheckCommand(cmd); err != nil {
			t.Errorf("%q: %v", cmd, err)
		}
	}
	for _, bad := range []string{"cmd open entry:1@0,0,0;say hi Healy", "cmd open @entry:1 Healy", "cmd state entry:1@0,0,0;say"} {
		if err := CheckCommand(bad); err == nil {
			t.Errorf("%q accepted", bad)
		}
	}
}

func TestParseTarget(t *testing.T) {
	tg, ok := ParseTarget("TBM:ACTION_TARGET|cmd open|bot:Healy|8246337208325079209|124371")
	if !ok || tg.Intent != "cmd open" || tg.Bot != "Healy" || uint64(tg.GUID) != 8246337208325079209 || tg.Entry != 124371 {
		t.Fatalf("%+v %v", tg, ok)
	}
	for _, bad := range []string{"TBM:ACTION_TARGET|cmd open|party|1|2", "TBM:ACTION_TARGET|cmd open|bot:A|x|2", "TBM:ACTION_TARGET|cmd open|bot:A|1", "TBM:ACTION_PENDING|cmd open|bot:A|1|2"} {
		if _, ok := ParseTarget(bad); ok {
			t.Errorf("%q accepted", bad)
		}
	}
}

func TestParseGOState(t *testing.T) {
	s, ok := ParseGOState("guid:123,entry:124372,type:0,state:active,loot:ready,flags:in-use+locked")
	if !ok || s.GUID != 123 || s.Entry != 124372 || s.State != "active" || s.Loot != "ready" || s.Flags != "in-use+locked" {
		t.Fatalf("%+v %v", s, ok)
	}
	for _, bad := range []string{"-", "guid:1,entry:2,type:3", "guid:x,entry:2,state:active", "opened"} {
		if _, ok := ParseGOState(bad); ok {
			t.Errorf("%q accepted", bad)
		}
	}
}

func TestOrderTargetPendingAck(t *testing.T) {
	l := newFakeLink()
	l.reply = func(string) []string {
		return []string{"TBM:ACTION_TARGET|cmd open|bot:Healy|123|124371", "TBM:ACTION_PENDING|cmd open|bot:Healy|12|0x7B", "TBM:ACTION_ACK|cmd open|bot:Healy|1|opened"}
	}
	r, err := fastCommander(l).Order(context.Background(), "cmd open entry:124371@1,2,3 Healy")
	if err != nil || !r.OK || !r.Opened || r.Target == nil || r.Target.GUID != 123 || r.Target.Entry != 124371 || r.Pending == nil {
		t.Fatalf("%v %+v", err, r)
	}
	// A target line of another bot's order is not this order's.
	l.reply = func(string) []string {
		return []string{"TBM:ACTION_TARGET|cmd open|bot:Magey|9|1", "TBM:ACTION_ERR|cmd open|not-found|nothing there"}
	}
	r, _ = fastCommander(l).Order(context.Background(), "cmd open entry:1 Healy")
	if r.Target != nil || r.Code() != "not-found" {
		t.Fatalf("%+v", r)
	}
}

func TestOrderState(t *testing.T) {
	l := newFakeLink()
	l.reply = func(string) []string {
		return []string{"TBM:ACTION_ACK|cmd state|party|1|guid:77,entry:124372,type:0,state:ready,loot:not-ready,flags:none"}
	}
	r, err := fastCommander(l).Order(context.Background(), "cmd state entry:124372@-234,294,-47")
	if err != nil || !r.OK || r.State == nil || r.State.State != "ready" || r.State.GUID != 77 || r.Bot != "" {
		t.Fatalf("%v %+v", err, r)
	}
	l.reply = func(string) []string { return []string{"TBM:ACTION_ERR|cmd state|not-found|No spawned game object"} }
	r, _ = fastCommander(l).Order(context.Background(), "cmd state entry:124372")
	if r.Code() != "not-found" || r.State != nil {
		t.Fatalf("%+v", r)
	}
}

// cmd ritual speaks for the whole party: its PENDING and ACK say party, not bot:<Name>, and the ACK's detail is "done".
func TestOrderRitual(t *testing.T) {
	l := newFakeLink()
	l.reply = func(string) []string {
		return []string{"TBM:ACTION_TARGET|cmd ritual|bot:Magey|123|130511", "TBM:ACTION_PENDING|cmd ritual|party|41|123", "TBM:ACTION_ACK|cmd ritual|party|3|done"}
	}
	cmd := "cmd ritual entry:130511@104.80,272.50,-26.50 Magey Dpsy Rogy"
	if err := CheckCommand(cmd); err != nil {
		t.Fatal(err)
	}
	r, err := fastCommander(l).Order(context.Background(), cmd)
	if err != nil || !r.OK || !r.Ritual || r.Participants != 3 || r.Pending == nil || r.Pending.BudgetS != 41 || r.Bot != "" || r.Target == nil || r.Target.Entry != 130511 {
		t.Fatalf("%v %+v", err, r)
	}
	l.reply = func(string) []string {
		return []string{"TBM:ACTION_PENDING|cmd ritual|party|41|123", "TBM:ACTION_ERR|cmd ritual|interrupted|a participant stopped"}
	}
	r, _ = fastCommander(l).Order(context.Background(), cmd)
	if r.OK || r.Ritual || r.Code() != "interrupted" || r.Pending == nil {
		t.Fatalf("%+v", r)
	}
}
