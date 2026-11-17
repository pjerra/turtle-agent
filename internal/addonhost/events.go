package addonhost

import (
	"strings"

	"github.com/TortoiseBots/turtle-agent/internal/events"
)

// Event is a 1.12 game event as the addon's OnEvent handlers read it: the event name and
// arg1..argN.
type Event struct {
	Event string   `json:"event"`
	Args  []string `json:"args"`
}

// GameEvent translates a logged event into the client event a real 1.12 client raises for
// it, among those the addon registers for (Comms.lua InitComms, Core.lua, Roster.lua): system
// messages, addon messages, whispers to the player and the end of a loading screen.
func GameEvent(e events.Event) (Event, bool) {
	switch e.Kind {
	case events.KindSystem:
		// The raw text, colour codes and all: StripColors eats "|r" out of protocol fields.
		return Event{Event: "CHAT_MSG_SYSTEM", Args: []string{raw(e)}}, true
	case events.KindAddon:
		prefix, _ := e.Data["prefix"].(string)
		typ, _ := e.Data["type"].(string)
		return Event{Event: "CHAT_MSG_ADDON", Args: []string{prefix, e.Text, strings.ToUpper(typ), e.Source}}, true
	case events.KindWhisper:
		if typ, _ := e.Data["type"].(string); typ != "whisper" {
			return Event{}, false // whisper_inform is the player's own whisper
		}
		return Event{Event: "CHAT_MSG_WHISPER", Args: []string{raw(e), e.Source}}, true
	case events.KindSession:
		// in_world after the login or a reconnect, new_world after a far teleport: both end a
		// loading screen.
		if st, _ := e.Data["state"].(string); st == "in_world" || st == "new_world" {
			return Event{Event: "PLAYER_ENTERING_WORLD", Args: []string{}}, true
		}
	}
	return Event{}, false
}

// GroupChanged says whether e is a change of the player's group: the agent then pushes its
// group to the addon at that point of the stream, so PARTY_MEMBERS_CHANGED comes before the
// lines the server sent after it, as in a real client.
func GroupChanged(e events.Event) bool {
	if e.Kind != events.KindParty {
		return false
	}
	switch e.Data["event"] {
	case "joined", "left", "disbanded", "uninvited", "leader":
		return true
	}
	return false
}

func raw(e events.Event) string {
	if s, ok := e.Data["raw"].(string); ok {
		return s
	}
	return e.Text
}
