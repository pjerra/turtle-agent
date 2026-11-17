package tbm

import (
	"reflect"
	"testing"

	"github.com/TortoiseBots/turtle-agent/internal/events"
)

func TestParseAckAndErr(t *testing.T) {
	a, ok := ParseAck("TBM:ACTION_ACK|aoe|bot:Gorm|1|off")
	if !ok || a != (Ack{Intent: "aoe", Scope: "bot:Gorm", Count: 1, Executor: "off"}) {
		t.Fatalf("%+v %v", a, ok)
	}
	for _, bad := range []string{
		"TBM:ACTION_ACK|aoe|party|1", "TBM:ACTION_ACK|aoe|raid|1|-", "TBM:ACTION_ACK|aoe|bot:|1|-",
		"TBM:ACTION_ACK|aoe|party|x|-", "TBM:ACTION_ACK||party|1|-", "TBM:ACTION_ACK|aoe|party|1|", "TBM:ACTION_ERR|a|b|c",
	} {
		if _, ok := ParseAck(bad); ok {
			t.Errorf("%q parsed", bad)
		}
	}
	e, ok := ParseErr("TBM:ACTION_ERR|attack|no-target|Select a live non-bot target first.")
	if !ok || e.Code != "no-target" || e.Message != "Select a live non-bot target first." {
		t.Fatalf("%+v", e)
	}
	if _, ok := ParseErr("TBM:ACTION_ERR||x|y"); ok {
		t.Error("empty intent parsed")
	}
}

func cmd(t *testing.T, name string, args map[string]string) Command {
	t.Helper()
	c, err := Build(mustFind(t, name), Context{Args: args})
	if err != nil {
		t.Fatal(err)
	}
	return c
}

func feed(t *testing.T, m *Matcher, lines ...string) bool {
	t.Helper()
	done := false
	for _, l := range lines {
		done = m.Feed(l)
	}
	return done
}

// A reply belongs to the click whose intent it carries; other bots' and other buttons'
// lines are kept but do not settle it.
func TestMatcherAckByIntent(t *testing.T) {
	m, err := NewMatcher(cmd(t, "Fight.AoE", map[string]string{"state": "off"}))
	if err != nil {
		t.Fatal(err)
	}
	if feed(t, m, "TBM:ACTION_ACK|stay|party|2|-", "TBM:ACTION_ERR|attack|no-target|x", "TBM:BOTSTATE|Gorm|aoe=off", "Some chat line") {
		t.Fatal("settled on another intent")
	}
	if !feed(t, m, "TBM:ACTION_ACK|AoE|party|2|off") {
		t.Fatal("did not settle on the ACK")
	}
	r := m.Reply()
	if r.Kind != KindAck || !r.OK || r.Ack.Executor != "off" || len(r.Lines) != 5 {
		t.Fatalf("%+v", r)
	}
	if !m.Feed("TBM:ACTION_ERR|aoe|failed|late") || m.Reply().Kind != KindAck {
		t.Fatal("a settled reply changed")
	}
}

func TestMatcherErr(t *testing.T) {
	m, _ := NewMatcher(cmd(t, "Fight.Attack", nil))
	if !feed(t, m, "TBM:ACTION_ERR|attack|no-target|Select a live non-bot target first.") {
		t.Fatal("not settled")
	}
	if r := m.Reply(); r.Kind != KindErr || r.OK || r.Err.Code != "no-target" {
		t.Fatalf("%+v", r)
	}
}

func TestMatcherTimeout(t *testing.T) {
	m, _ := NewMatcher(cmd(t, "Camp.Rest", nil))
	feed(t, m, "TBM:ACTION_ACK|sell|party|1|-")
	if r := m.Reply(); r.Kind != KindTimeout || r.OK || !reflect.DeepEqual(r.Lines, []string{"TBM:ACTION_ACK|sell|party|1|-"}) {
		t.Fatalf("%+v", r)
	}
	if r := (&Matcher{}).Reply(); r.Lines == nil {
		t.Fatal("lines must not be nil")
	}
}

func TestMatcherTextReplies(t *testing.T) {
	m, _ := NewMatcher(cmd(t, "Roster.Summon", map[string]string{"name": "Gorm"}))
	if !feed(t, m, "Summoning Gorm to a safe position near you (3s); it will follow on arrival.") || !m.Reply().OK {
		t.Fatalf("%+v", m.Reply())
	}
	m, _ = NewMatcher(cmd(t, "Roster.Login", map[string]string{"name": "Nobody"}))
	if !feed(t, m, "Character 'Nobody' not found") || m.Reply().OK || m.Reply().Kind != KindText {
		t.Fatalf("%+v", m.Reply())
	}
	m, _ = NewMatcher(cmd(t, "Camp.Formation.Line", nil))
	if !feed(t, m, "Party formation set to line (2 bots updated).") || !m.Reply().OK {
		t.Fatalf("%+v", m.Reply())
	}
}

func TestMatcherInventoryAndSnapshot(t *testing.T) {
	m, _ := NewMatcher(cmd(t, "BotPanel.Inventory", map[string]string{"bot": "Gorm"}))
	if feed(t, m, "TBM:INV_BEGIN|Gorm|100", "TBM:INV_EQ|0|1|1|1", "TBM:INV_END|Lia|1|2") {
		t.Fatal("another bot's inventory settled it")
	}
	if !feed(t, m, "TBM:INV_END|gorm|3|16") || m.Reply().Kind != KindInventory {
		t.Fatalf("%+v", m.Reply())
	}
	m, _ = NewMatcher(cmd(t, "Roster.Refresh", nil))
	if feed(t, m, "TBM:ROSTER_BEGIN|0", "TBM:ROSTER_END") || !feed(t, m, "TBM:TRANSPORT|none") || m.Reply().Kind != KindSnapshot {
		t.Fatalf("%+v", m.Reply())
	}
	m, _ = NewMatcher(cmd(t, "Roster.Refresh", nil))
	if !feed(t, m, "TBM:ROSTER_ERROR|not-in-game|x") || m.Reply().OK {
		t.Fatalf("%+v", m.Reply())
	}
}

func TestAnyMatcher(t *testing.T) {
	m := NewAnyMatcher()
	if feed(t, m, "hello") || !feed(t, m, "TBM:ACTION_ERR|x|bad|y") || m.Reply().Kind != KindErr {
		t.Fatalf("%+v", m.Reply())
	}
}

// Replies arrive on the transport that carried the request.
func TestCarriedFollowsTheTransport(t *testing.T) {
	sys := events.Event{Kind: events.KindSystem, Text: "TBM:TRANSPORT|party"}
	add := events.Event{Kind: events.KindAddon, Text: "TBM:ACTION_ACK|stay|party|1|-", Data: map[string]any{"prefix": "TBM"}}
	other := events.Event{Kind: events.KindAddon, Text: "TBM:X", Data: map[string]any{"prefix": "BigWigs"}}
	for _, tc := range []struct {
		e         events.Event
		transport string
		ok        bool
	}{
		{sys, TransportChat, true}, {sys, TransportAddon, false},
		{add, TransportAddon, true}, {add, TransportChat, false},
		{other, TransportAddon, false},
	} {
		if _, ok := Carried(tc.e, tc.transport); ok != tc.ok {
			t.Errorf("%v on %s: %v", tc.e.Kind, tc.transport, ok)
		}
	}
	raw := events.Event{Kind: events.KindSystem, Text: "TBM:ROSTER|7|Gorm|1|online|1|x", Data: map[string]any{"raw": "TBM:ROSTER|7|Gorm|1|online|1|x|r"}}
	if l, _ := Carried(raw, TransportChat); l != "TBM:ROSTER|7|Gorm|1|online|1|x|r" {
		t.Errorf("TBM line must be read raw: %q", l)
	}
	plain := events.Event{Kind: events.KindSystem, Text: "Bot Gorm role set to Tank.", Data: map[string]any{"raw": "|cffffffffBot Gorm role set to Tank.|r"}}
	if l, _ := Carried(plain, TransportChat); l != "Bot Gorm role set to Tank." {
		t.Errorf("plain line must be stripped: %q", l)
	}
}

// Text patterns are anchored and name the bot: "Your role set to" is not "Bot X role set to",
// and another bot's reply does not settle this click.
func TestMatcherTextRepliesAreAnchored(t *testing.T) {
	m, _ := NewMatcher(cmd(t, "Party.RoleTank", map[string]string{"bot": "Gorm"}))
	if feed(t, m, "Your role set to Tank.", "Bot Lia role set to Tank.", "Say: Bot Gorm role set to Tank.") {
		t.Fatalf("settled on a foreign line: %+v", m.Reply())
	}
	if !feed(t, m, "Bot gorm role set to Tank.") || !m.Reply().OK {
		t.Fatalf("%+v", m.Reply())
	}
	m, _ = NewMatcher(cmd(t, "Party.RoleSelf", map[string]string{"role": "dps"}))
	if feed(t, m, "Bot Gorm role set to Tank.") || !feed(t, m, "Your role set to DPS.") {
		t.Fatalf("%+v", m.Reply())
	}
	m, _ = NewMatcher(cmd(t, "Roster.Summon", map[string]string{"name": "Gorm"}))
	if feed(t, m, "Summoning Lia to a safe position near you (3s); it will follow on arrival.") {
		t.Fatal("another bot's summon settled it")
	}
}

// Replies owed to earlier requests that timed out are passed over, one per owed request.
func TestMatcherSkipsOwedReplies(t *testing.T) {
	m, _ := NewMatcher(cmd(t, "Camp.Rest", nil))
	m.Skip(1)
	if feed(t, m, "TBM:ACTION_ACK|rest|party|1|-") || m.Skipped() != 1 {
		t.Fatalf("%+v", m.Reply())
	}
	if !feed(t, m, "TBM:ACTION_ACK|rest|party|2|-") || m.Reply().Ack.Count != 2 {
		t.Fatalf("%+v", m.Reply())
	}
}

// One ACK per bot and no end marker: the reply closes on a quiet gap, with every ACK.
func TestMatcherMultiCollectsAcks(t *testing.T) {
	m, _ := NewMatcher(cmd(t, "Raid.CheckBots", nil))
	if m.Quiet() != 0 {
		t.Fatal("quiet gap before the first ACK")
	}
	for _, l := range []string{"TBM:ACTION_ACK|raid status|bot:Gorm|1|molten core", "TBM:ACTION_ACK|raid status|bot:Lia|1|outdoor"} {
		if m.Feed(l) {
			t.Fatal("a Multi reply settled on one ACK")
		}
	}
	if m.Quiet() != MultiQuiet {
		t.Fatal("no quiet gap after an ACK")
	}
	r := m.Reply()
	if r.Kind != KindAck || !r.OK || len(r.Acks) != 2 || r.Ack.Scope != "bot:Gorm" || r.Line != "TBM:ACTION_ACK|raid status|bot:Gorm|1|molten core" || len(r.Lines) != 2 {
		t.Fatalf("%+v", r)
	}
}
