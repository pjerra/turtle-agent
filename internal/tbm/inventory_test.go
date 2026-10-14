package tbm

import (
	"context"
	"strings"
	"testing"
	"time"
)

func TestParseInventory(t *testing.T) {
	inv := ParseInventory([]string{
		"TBM:INV_BEGIN|Tanky|12345",
		"TBM:INV_EQ|16|2000|40|40",
		"TBM:INV_EQ|17|2504|30|30",
		"TBM:INV_ITEM|255|23|5350|20|-",
		"TBM:INV_ITEM|19|1|5350|15|e",
		"TBM:INV_ITEM|19|2|2512|200|-",
		"some system line",
		"TBM:INV_END|Tanky|10|16",
	})
	if !inv.Complete || inv.Bot != "Tanky" || inv.Copper != 12345 || inv.Free != 10 || inv.Total != 16 {
		t.Fatalf("%+v", inv)
	}
	if inv.Equipped[SlotOffHand] != 2000 || inv.Equipped[SlotRanged] != 2504 || len(inv.Items) != 3 {
		t.Fatalf("%+v", inv)
	}
	if inv.Count(5350) != 35 || inv.Count(2512) != 200 || inv.Count(2000) != 1 || inv.Count(1) != 0 {
		t.Fatalf("%v", inv.Counts)
	}
	if inv.Items[1].Flags != "e" {
		t.Fatalf("%+v", inv.Items)
	}
	// An error reply, and a snapshot that never ended.
	if e := ParseInventory([]string{"TBM:ACTION_ERR|inv|no-bot|Usage"}); e.Err == nil || e.Err.Code != "no-bot" || e.Complete {
		t.Fatalf("%+v", e)
	}
	if c := ParseInventory([]string{"TBM:INV_BEGIN|Tanky|0", "TBM:INV_ITEM|255|23|x|20|-"}); c.Complete || len(c.Items) != 0 {
		t.Fatalf("%+v", c)
	}
}

func TestCheckBotLine(t *testing.T) {
	for _, ok := range []string{"maintenance Tanky", "inv Healy", ".bot logout Magey", "INV Healy"} {
		if err := CheckBotLine(ok); err != nil {
			t.Errorf("%q: %v", ok, err)
		}
	}
	for _, bad := range []string{"", "inv", "maintenance", "maintenance Tanky Healy", "autogear Tanky", "inv Tank;y", "logout Tanky\n.gm on", "inv 1234", "gm on"} {
		if err := CheckBotLine(bad); err == nil {
			t.Errorf("%q accepted", bad)
		}
	}
}

func TestLinesCollectsUntilDone(t *testing.T) {
	l := newFakeLink()
	l.reply = func(text string) []string {
		if text == ".bot inv Tanky" {
			return []string{"TBM:INV_BEGIN|Tanky|0", "TBM:VIEW|v1|1|E|3", "TBM:INV_ITEM|255|23|5350|20|-", "TBM:INV_END|Tanky|1|2", "late line"}
		}
		return nil
	}
	c := NewCommander(l)
	lines, settled, err := c.Lines(context.Background(), "inv Tanky", time.Second, func(s string) bool { return strings.HasPrefix(s, "TBM:INV_END|") })
	if err != nil || !settled || len(lines) != 3 || lines[2] != "TBM:INV_END|Tanky|1|2" {
		t.Fatalf("%v %v %v", lines, settled, err)
	}
	if len(l.sent) != 1 || l.sent[0] != ".bot inv Tanky" {
		t.Fatalf("%v", l.sent)
	}
	// No answer: not settled, no error.
	lines, settled, err = c.Lines(context.Background(), "maintenance Tanky", 50*time.Millisecond, func(string) bool { return true })
	if err != nil || settled || len(lines) != 0 {
		t.Fatalf("%v %v %v", lines, settled, err)
	}
	// Something that is not a bot line is refused and sends nothing.
	if _, _, err := c.Lines(context.Background(), "autogear Tanky", time.Second, func(string) bool { return true }); err == nil || len(l.sent) != 2 {
		t.Fatalf("%v %v", err, l.sent)
	}
}
