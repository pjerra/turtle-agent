package addonhost

import (
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/TortoiseBots/turtle-agent/internal/events"
)

func TestGameEvent(t *testing.T) {
	for _, c := range []struct {
		e    events.Event
		want *Event
	}{
		{events.Event{Kind: events.KindSystem, Text: "TBM:ROSTER|7|Gorm|1|ready", Data: map[string]any{"raw": "TBM:ROSTER|7|Gorm|1|ready|r"}},
			&Event{"CHAT_MSG_SYSTEM", []string{"TBM:ROSTER|7|Gorm|1|ready|r"}}},
		{events.Event{Kind: events.KindAddon, Text: "TBM:TRANSPORT|party", Source: "", Data: map[string]any{"prefix": "TBM", "type": "guild"}},
			&Event{"CHAT_MSG_ADDON", []string{"TBM", "TBM:TRANSPORT|party", "GUILD", ""}}},
		{events.Event{Kind: events.KindWhisper, Text: "hi", Source: "Gorm", Data: map[string]any{"type": "whisper"}},
			&Event{"CHAT_MSG_WHISPER", []string{"hi", "Gorm"}}},
		{events.Event{Kind: events.KindWhisper, Text: "hi", Source: "Gorm", Data: map[string]any{"type": "whisper_inform"}}, nil},
		{events.Event{Kind: events.KindSession, Data: map[string]any{"state": "new_world"}}, &Event{"PLAYER_ENTERING_WORLD", []string{}}},
		{events.Event{Kind: events.KindSession, Data: map[string]any{"state": "teleported"}}, nil},
		{events.Event{Kind: events.KindChat, Text: "hello"}, nil},
	} {
		got, ok := GameEvent(c.e)
		if c.want == nil {
			if ok {
				t.Errorf("%+v: %+v, want none", c.e, got)
			}
			continue
		}
		if !ok || !reflect.DeepEqual(got, *c.want) {
			t.Errorf("%+v: %+v, want %+v", c.e, got, *c.want)
		}
	}
	if !GroupChanged(events.Event{Kind: events.KindParty, Data: map[string]any{"event": "joined"}}) ||
		GroupChanged(events.Event{Kind: events.KindParty, Text: "invites you"}) {
		t.Fatal("GroupChanged")
	}
}

// fakeAddon is a small addon on the mock: a frame that answers CHAT_MSG_SYSTEM by sending an
// addon message, a button that says something, one that raises, a greyed-out one, and an
// OnUpdate that loops forever once the system message "spin" armed it.
const fakeAddon = `
TortoiseBots = { C = {}, NormalizeName = function(n) return n end }
local TB = TortoiseBots
local f = CreateFrame("Frame", "FakeComms")
f:RegisterEvent("CHAT_MSG_SYSTEM")
f:SetScript("OnEvent", function()
    if arg1 == "spin" then TB.spin = true return end
    SendAddonMessage("TBM", "got " .. arg1, "PARTY")
end)
TB.frame = CreateFrame("Frame", "FakeMain", UIParent)
TB.actionsFrame = CreateFrame("Frame", nil, TB.frame)
TB.tabActions = CreateFrame("Button", nil, TB.frame)
TB.tabActions:SetScript("OnClick", function() end)
local hello = CreateFrame("Button", nil, TB.actionsFrame)
hello:SetText("|cffffd200Hello|r")
hello:SetScript("OnClick", function() SendChatMessage(".bot hello"); SendChatMessage("/w", "WHISPER", nil, "Gorm") end)
local bad = CreateFrame("Button", nil, TB.actionsFrame)
bad:SetText("Broken")
bad:SetScript("OnClick", function() error("boom") end)
local off = CreateFrame("Button", nil, TB.actionsFrame)
off:SetText("Greyed")
off:SetScript("OnClick", function() end)
off:Disable()
local spin = CreateFrame("Frame", nil, UIParent)
spin:SetScript("OnUpdate", function() if TB.spin then while true do end end end)
`

func startFake(t *testing.T, sink Sink) *Host {
	t.Helper()
	h, err := loadFake(t, fakeAddon, sink)
	if err != nil {
		t.Fatal(err)
	}
	return h
}

// loadFake starts the host on an addon of one file holding src and loads it.
func loadFake(t *testing.T, src string, sink Sink) (*Host, error) {
	t.Helper()
	if _, err := exec.LookPath(DefaultLua); err != nil {
		if os.Getenv("TBM_ADDON_HOST_STRICT") == "1" {
			t.Fatal("lua5.1 not installed")
		}
		t.Skip("lua5.1 not installed")
	}
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "TortoiseBotsManager.toc"), []byte("## Version: fake\nFake.lua\n"), 0o600)
	os.WriteFile(filepath.Join(dir, "Fake.lua"), []byte(src), 0o600)
	h, err := Start(dir, sink)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { h.Close() })
	_, _, err = h.Call("load", map[string]any{"in_world": true})
	return h, err
}

func TestHostEventsOutsAndClicks(t *testing.T) {
	var done []Out
	h := startFake(t, func(o Out) error {
		done = append(done, o)
		if o.Type == "WHISPER" {
			return errors.New("no such player")
		}
		return nil
	})
	_, outs, err := h.Call("events", map[string]any{"events": []Event{{"CHAT_MSG_SYSTEM", []string{"a|b"}}, {"CHAT_MSG_SYSTEM", []string{"c"}}}})
	if err != nil || len(outs) != 2 || outs[0].Text != "got a|b" || outs[1].Channel != "PARTY" || outs[1].Prefix != "TBM" {
		t.Fatalf("%v %+v", err, outs)
	}
	res, outs, err := h.Call("click", map[string]any{"tab": "actions", "label": "hello"})
	if err != nil || len(outs) != 2 || outs[0].Text != ".bot hello" || outs[0].Type != "SAY" || outs[1].To != "Gorm" || outs[1].Error != "no such player" {
		t.Fatalf("%v %+v", err, outs)
	}
	var c struct{ Clicked []string }
	json.Unmarshal(res, &c)
	if !reflect.DeepEqual(c.Clicked, []string{"Hello"}) || len(done) != 4 {
		t.Fatalf("%s %+v", res, done)
	}
	if _, _, err := h.Call("click", map[string]any{"tab": "actions", "label": "Broken"}); err == nil || !strings.Contains(err.Error(), "boom") {
		t.Fatalf("raise: %v", err)
	}
	if _, _, err := h.Call("click", map[string]any{"tab": "actions", "label": "Greyed"}); err == nil || !strings.Contains(err.Error(), "greyed out") {
		t.Fatalf("disabled: %v", err)
	}
	if _, _, err := h.Call("click", map[string]any{"tab": "actions", "label": "Greyed", "force": true}); err != nil {
		t.Fatalf("forced: %v", err)
	}
	if _, _, err := h.Call("nope", nil); err == nil || !strings.Contains(err.Error(), "unknown op") {
		t.Fatalf("op: %v", err)
	}
}

// An addon stuck in a loop is killed after CallTimeout, and later calls say so.
func TestHostKillsAStuckAddon(t *testing.T) {
	old := CallTimeout
	CallTimeout = 300 * time.Millisecond
	t.Cleanup(func() { CallTimeout = old })
	h := startFake(t, nil)
	if _, _, err := h.Call("tick", map[string]any{"ms": 100}); err != nil {
		t.Fatal(err)
	}
	// an error inside the addon keeps the host alive
	if _, _, err := h.Call("click", map[string]any{"tab": "actions", "label": "missing"}); err == nil || errors.Is(err, ErrStopped) {
		t.Fatalf("missing: %v", err)
	}
	if _, _, err := h.Call("events", map[string]any{"events": []Event{{"CHAT_MSG_SYSTEM", []string{"spin"}}}}); err != nil {
		t.Fatal(err)
	}
	_, _, err := h.Call("tick", map[string]any{"ms": 100})
	if !errors.Is(err, ErrStopped) || !strings.Contains(err.Error(), "killed") {
		t.Fatalf("stuck: %v", err)
	}
	if _, _, err := h.Call("state", nil); !errors.Is(err, ErrStopped) {
		t.Fatalf("after: %v", err)
	}
}

// The addon runs in a sandbox: nothing outside the WoW API and safe Lua reaches the host.
func TestHostSandbox(t *testing.T) {
	marker := filepath.Join(t.TempDir(), "pwned")
	for name, src := range map[string]string{
		"os.execute":  `os.execute("touch ` + marker + `")`,
		"io.open":     `io.open("` + marker + `", "w"):write("x")`,
		"loadfile":    `loadfile("/etc/hostname")()`,
		"require":     `require("os")`,
		"dofile":      `dofile("/etc/hostname")`,
		"loadstring":  `loadstring("return 1")()`,
		"getfenv":     `getfenv(0).os.execute("touch ` + marker + `")`,
		"string.dump": `string.dump(print)`,
		"_G escape":   `_G.os.execute("touch ` + marker + `")`,
	} {
		_, err := loadFake(t, "TortoiseBots = {}\n"+src, nil)
		if err == nil || !strings.Contains(err.Error(), "raised while loading") {
			t.Errorf("%s: %v, want a load error", name, err)
		}
		if _, statErr := os.Stat(marker); statErr == nil {
			t.Fatalf("%s: the addon wrote %s", name, marker)
		}
	}
	// What the addon may use works: the WoW short names, date, and print (to the chat frame).
	h, err := loadFake(t, `TortoiseBots = { stamp = date("%H:%M"), n = getn({1, 2}), at = strfind("abc", "b") }
print("hello", 1)
io = nil`, nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := h.Call("tick", map[string]any{"ms": 10}); err != nil {
		t.Fatalf("host after print: %v", err)
	}
}

// Lua writing to stdout cannot pass for a frame: the protocol is on its own descriptor.
func TestHostStdoutIsNotTheProtocol(t *testing.T) {
	h, err := loadFake(t, `TortoiseBots = {}
local f = CreateFrame("Frame", "Out")
f:RegisterEvent("CHAT_MSG_SYSTEM")
f:SetScript("OnEvent", function() print('{"id":99,"result":{}}'); SendAddonMessage("TBM", arg1, "PARTY") end)`, nil)
	if err != nil {
		t.Fatal(err)
	}
	_, outs, err := h.Call("events", map[string]any{"events": []Event{{"CHAT_MSG_SYSTEM", []string{"x"}}}})
	if err != nil || len(outs) != 1 || outs[0].Text != "x" {
		t.Fatalf("%v %+v", err, outs)
	}
}
