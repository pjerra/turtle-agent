// Package tbm speaks the protocol between the TortoiseBots server module and its
// TortoiseBotsManager (TBM) addon: the commands the addon sends and the "TBM:" lines the
// module answers with. Sources: TortoiseBots 18d4d14 (commands/BotCommands.cpp,
// host/BotAddonAdapter.cpp) and TortoiseBotsManager 3384e15 (Core.lua, Comms.lua).
package tbm

import (
	"strconv"
	"strings"
	"unicode"

	"github.com/TortoiseBots/turtle-agent/internal/events"
)

// Prefix is the addon message prefix both sides use (BotAddonAdapter.cpp:18, Constants.lua:276).
const Prefix = "TBM"

// Handshake is what the addon sends on PLAYER_ENTERING_WORLD while its transport is still
// unknown: "help" for the server capabilities, then "roster" (Core.lua:505-509, :551-560,
// :659-662). Both go out as ".bot" chat (Core.lua:253-266); a roster response ends with
// TBM:TRANSPORT, and only "party" moves later commands to the addon channel (Comms.lua:101-126).
var Handshake = []string{"help", "roster"}

// Chat is a command as the chat transport sends it (Core.lua:258).
func Chat(command string) string { return ".bot " + command }

// RosterRow is one TBM:ROSTER line: guid low, name, class id, state, grouped, location
// (BotCommands.cpp:1950-1953), and the CC mark the addon also reads as a 7th field
// (Roster.lua:342-346).
type RosterRow struct {
	GUID     uint32 `json:"guid"`
	Name     string `json:"name"`
	Class    uint32 `json:"class"`
	State    string `json:"state"`
	Grouped  bool   `json:"grouped"`
	Location string `json:"location"`
	CCMark   string `json:"cc_mark,omitempty"`
}

// Status is what a roster response says about the module (BotCommands.cpp:1880-2024).
type Status struct {
	Version   string            `json:"version,omitempty"`   // TBM:VERSION
	Caps      []string          `json:"caps,omitempty"`      // TBM:CAPS
	Transport string            `json:"transport,omitempty"` // TBM:TRANSPORT: "party" or "none"
	Roster    []RosterRow       `json:"roster,omitempty"`
	BotState  map[string]string `json:"bot_state,omitempty"` // TBM:BOTSTATE name -> "move=…,loot=…"
	Errors    []string          `json:"errors,omitempty"`    // TBM:ROSTER_ERROR and TBM:ACTION_ERR lines
	Lines     []string          `json:"lines"`               // every TBM: line, in order
	// Complete: the TBM:TRANSPORT trailer or a TBM:ROSTER_ERROR arrived, so the roster
	// response is over.
	Complete bool `json:"complete"`
}

// Add folds one TBM: line into st and reports whether the roster response is complete.
func (st *Status) Add(line string) bool {
	st.Lines = append(st.Lines, line)
	verb, rest, _ := strings.Cut(strings.TrimPrefix(line, "TBM:"), "|")
	f := strings.Split(rest, "|")
	switch verb {
	case "VERSION":
		st.Version = rest
	case "CAPS": // split like the addon: [^,%s|]+ (Comms.lua:623)
		st.Caps = strings.FieldsFunc(rest, func(r rune) bool { return r == ',' || r == '|' || unicode.IsSpace(r) })
	case "TRANSPORT":
		st.Transport, st.Complete = rest, true
	case "ROSTER_BEGIN":
		st.Roster = nil
	case "ROSTER":
		if len(f) >= 6 {
			guid, _ := strconv.ParseUint(f[0], 10, 32)
			class, _ := strconv.ParseUint(f[2], 10, 32)
			row := RosterRow{GUID: uint32(guid), Name: f[1], Class: uint32(class), State: f[3], Grouped: f[4] == "1", Location: f[5]}
			if len(f) >= 7 {
				row.CCMark = f[6]
			}
			st.Roster = append(st.Roster, row)
		}
	case "BOTSTATE_BEGIN":
		st.BotState = nil
	case "BOTSTATE":
		if len(f) == 2 {
			if st.BotState == nil {
				st.BotState = map[string]string{}
			}
			st.BotState[f[0]] = f[1]
		}
	case "ROSTER_ERROR":
		st.Errors, st.Complete = append(st.Errors, line), true
	case "ACTION_ERR":
		st.Errors = append(st.Errors, line)
	}
	return st.Complete
}

// Line returns the TBM: protocol line an event carries: a system message on the chat
// transport, or an addon message with prefix TBM on the addon transport (the module redirects
// its chat replies there, BotAddonAdapter.cpp:20-56). A system line is read raw, because
// colour stripping eats "|r" out of fields such as "|removing".
func Line(e events.Event) (string, bool) {
	var s string
	switch e.Kind {
	case events.KindSystem:
		s = e.Text
		if raw, ok := e.Data["raw"].(string); ok {
			s = raw
		}
	case events.KindAddon:
		if e.Data["prefix"] != Prefix {
			return "", false
		}
		s = e.Text
	default:
		return "", false
	}
	if !strings.HasPrefix(s, "TBM:") {
		return "", false
	}
	return s, true
}
