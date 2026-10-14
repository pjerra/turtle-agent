package tbm

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/pjerra/turtle-agent/internal/events"
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
	for _, ok := range []string{"maintenance Tanky", "inv Healy", ".bot logout Magey", "INV Healy", "command Tanky talents", ".bot command Tanky talents", "COMMAND Healy talents",
		"roster", ".bot roster", "add Tanky", ".bot add Tanky", "command Tanky leave", ".bot command Healy leave",
		"hire warrior tank", "hire priest healer level=43", "HIRE Hunter DPS level=45", ".bot hire mage dps", "autogear Tanky blue", ".bot autogear Healy BLUE",
		"command Tanky spells taunt", "command Healy spells power word: shield", "command Magey spells hunter's mark"} {
		if err := CheckBotLine(ok); err != nil {
			t.Errorf("%q: %v", ok, err)
		}
	}
	for _, bad := range []string{"", "inv", "maintenance", "maintenance Tanky Healy", "autogear Tanky", "inv Tank;y", "logout Tanky\n.gm on", "inv 1234", "gm on",
		// the talents read-back is the only `.bot command` line: nothing else may follow the word, and no other command goes through
		"command Tanky", "command Tanky talents reset", "command Tanky talents auto", "command Tanky talents 05-31", "command Tanky reset", "command Tanky talents shift 1", "command Tank;y talents", "command 12 talents", "command Tanky Talents", "command Tanky\n.gm on talents", "roster all", "add", "add Tanky Healy", "add 12", "add Tank;y", "command Tanky Leave", "command Tanky leave now", "command Tanky leave\n.gm on",
		// the hire flow: a class, a role and a level, and nothing else (no race, gender or gear tier); autogear takes blue only; spells a plain filter
		"hire", "hire warrior", "hire warrior tank orc", "hire warrior tank level=", "hire warrior tank level=100", "hire warrior tank level=4x", "hire warrior tank t3", "hire monk dps",
		"hire mage frost", "hire warrior tank level=43 level=44", "autogear Tanky purple", "autogear Tanky", "autogear Tanky blue now", "autogear Tank;y blue", "autogear 12 blue",
		"command Tanky spells", "command Tanky spells x", "command Tanky spells taunt;.gm on", "command Tanky spells taunt\n.gm on", "command Tanky spells a b c d e f", "command Tanky spells 123",
		"hire warrior tank\n.gm on", "inv\nTanky"} {
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

func TestParseTalents(t *testing.T) {
	for _, c := range []struct {
		line  string
		trees [3]int
		total int
		ok    bool
	}{
		{"My current talent spec is: Protection (0/5/31) Link: 05-31", [3]int{0, 5, 31}, 36, true},
		{"My current talent spec is: Warrior (0/0/0) Link: 0", [3]int{}, 0, true},
		{"My current talent spec is: |h|cffffffffMitigation (|h|cff00ff005|h|cffffffff/|h|cffffffff0|h|cffffffff/|h|cff00ff0031|h|cffffffff) Link: 5-0-31", [3]int{5, 0, 31}, 36, true},
		{"Forwarded command for Tanky: talents; action success is not guaranteed.", [3]int{}, 0, false},
		{"", [3]int{}, 0, false},
	} {
		got, ok := ParseTalents([]string{"unrelated line", c.line})
		if ok != c.ok || got.Trees != c.trees || got.Total != c.total {
			t.Errorf("%q: %+v %v", c.line, got, ok)
		}
	}
}

// `command <bot> talents` is answered by the bot's own whisper (or party/say line), not a system message: Lines reads that bot's words
// and nobody else's.
func TestLinesReadsTheBotsWhisperForCommand(t *testing.T) {
	l := newFakeLink()
	l.reply = nil
	c := NewCommander(l)
	say := func(kind, from, text string, data map[string]any) {
		l.log.Append(events.Event{Kind: kind, Source: from, Text: text, Data: data})
	}
	l.sayHook = func(text string) {
		say(events.KindSystem, "", "Forwarded command for Tanky: talents; action success is not guaranteed.", nil)
		say(events.KindWhisper, "Healy", "My current talent spec is: Holy (31/20/0) Link: x", map[string]any{"type": "whisper"})
		say(events.KindWhisper, "Tanky", "My current talent spec is: Protection (0/5/31) Link: x", map[string]any{"type": "whisper_inform"})
		say(events.KindWhisper, "Tanky", "My current talent spec is: Protection (0/5/31) Link: 05-31", map[string]any{"type": "whisper"})
	}
	lines, settled, err := c.Lines(context.Background(), "command Tanky talents", time.Second, func(s string) bool {
		_, ok := ParseTalents([]string{s})
		return ok
	})
	if err != nil || !settled || len(lines) != 2 || !strings.HasSuffix(lines[1], "Link: 05-31") {
		t.Fatalf("%v %v %v", lines, settled, err)
	}
	if len(l.sent) != 1 || l.sent[0] != ".bot command Tanky talents" {
		t.Fatalf("%v", l.sent)
	}
	// A plain bot line does not read chat at all.
	l.sayHook = func(string) { say(events.KindWhisper, "Tanky", "TBM:INV_END|Tanky|1|2", nil) }
	if _, settled, _ := c.Lines(context.Background(), "inv Tanky", 50*time.Millisecond, func(s string) bool { return true }); settled {
		t.Fatal("a whisper answered an inv line")
	}
}
