package scenario

import (
	"testing"

	"github.com/pjerra/turtle-agent/internal/events"
)

// TestMatcherReadsTheRawSystemLine pins the G1 gotcha: a system event's text
// is colour-stripped and loses |r, so matchers read data.raw instead.
func TestMatcherReadsTheRawSystemLine(t *testing.T) {
	e := events.Event{
		Kind: events.KindSystem,
		Text: "TBM:RUN|zulfarrakunning|76/247|use_on|done",
		Data: map[string]any{"raw": "TBM:RUN|zulfarrak|running|76/247|use_on|done"},
	}
	if got := eventLine(e); got != "TBM:RUN|zulfarrak|running|76/247|use_on|done" {
		t.Fatalf("line %q", got)
	}
	m, err := Matcher{Kind: "system", Match: `^TBM:RUN\|zulfarrak\|running\|`}.compile(nil)
	if err != nil {
		t.Fatal(err)
	}
	if !m.matches(e) {
		t.Fatal("raw system line does not match")
	}
	plain := events.Event{Kind: events.KindSystem, Text: "hello"}
	if got := eventLine(plain); got != "hello" {
		t.Fatalf("line %q", got)
	}
}

// TestMatcherAddonPrefixAndChannel pins addon matching on prefix and channel.
func TestMatcherAddonPrefixAndChannel(t *testing.T) {
	e := addonEvent("S2C_GROUPS_LIST_UPDATE;start", "TW_LFG", "guild")
	yes, err := Matcher{Kind: "addon", Prefix: "TW_LFG", Channel: "guild", Match: "."}.compile(nil)
	if err != nil {
		t.Fatal(err)
	}
	if !yes.matches(e) {
		t.Fatal("TW_LFG/guild does not match")
	}
	for _, m := range []Matcher{
		{Kind: "addon", Prefix: "TBM", Match: "."},
		{Kind: "addon", Prefix: "TW_LFG", Channel: "party", Match: "."},
		{Kind: "system", Prefix: "TW_LFG", Match: "."},
	} {
		c, err := m.compile(nil)
		if err != nil {
			t.Fatal(err)
		}
		if c.matches(e) {
			t.Fatalf("%+v matches", m)
		}
	}
}
