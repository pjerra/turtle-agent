// Package eluna speaks the Eluna test toolkit's `.tt` commands (a TEST server only: TortoiseBots
// tools/eluna/test-toolkit). A GM line `.tt <sub> #<id>` is answered with system chat lines of the form
//
//	TTJSON {"v":1,"cmd":"q.units","id":"<id>","ok":true,...}
//
// and some commands answer twice (read 1, final false; read 2, final true). The package has no session dependency: the caller
// gives it the event log to read and the way to send a line.
package eluna

import (
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
)

// Prefix starts a reply line.
const Prefix = "TTJSON "

// Reply is one `TTJSON` line: the fields this client names, plus the whole object (Body) for everything else.
type Reply struct {
	V     int
	Cmd   string
	ID    string
	OK    bool
	Err   string
	Msg   string
	Read  int
	Final bool
	// Body is the whole reply object, as the server sent it.
	Body map[string]json.RawMessage
}

// ParseLine reads a reply line. ok is false for anything that is not one `TTJSON` object: other chat (a guild line, a GM error that
// is not a reply), a broken object, a line with two objects.
func ParseLine(line string) (Reply, bool) {
	rest, found := strings.CutPrefix(strings.TrimSpace(line), Prefix)
	if !found {
		return Reply{}, false
	}
	var body map[string]json.RawMessage
	if err := json.Unmarshal([]byte(strings.TrimSpace(rest)), &body); err != nil || body == nil {
		return Reply{}, false
	}
	r := Reply{Body: body}
	r.V = fieldInt(body, "v")
	r.Cmd = fieldString(body, "cmd")
	r.ID = fieldString(body, "id")
	r.OK = fieldBool(body, "ok")
	r.Err = fieldString(body, "err")
	r.Msg = fieldString(body, "msg")
	r.Read = fieldInt(body, "read")
	r.Final = fieldBool(body, "final")
	return r, true
}

// Done reports whether this reply ends the command: a one-shot reply (no read announced), or the final read of a two-read command.
func (r Reply) Done() bool { return r.Read == 0 || r.Final }

// HexGUID is the toolkit's spelling of a raw 64-bit guid: "0xF130EEEEEECCCCCC".
func HexGUID(g uint64) string { return fmt.Sprintf("0x%016X", g) }

// ParseHexGUID reads a guid in the toolkit's spelling, with or without the "0x" (either case).
func ParseHexGUID(s string) (uint64, error) {
	t := s
	if len(t) >= 2 && t[0] == '0' && (t[1] == 'x' || t[1] == 'X') {
		t = t[2:]
	}
	if t == "" {
		return 0, fmt.Errorf("%q is not a guid", s)
	}
	g, err := strconv.ParseUint(t, 16, 64)
	if err != nil {
		return 0, fmt.Errorf("%q is not a guid", s)
	}
	return g, nil
}

// fieldString is a reply field as a string ("" when absent or not a string).
func fieldString(b map[string]json.RawMessage, key string) string {
	var s string
	if raw, ok := b[key]; ok {
		_ = json.Unmarshal(raw, &s)
	}
	return s
}

// fieldInt is a reply field as an int (0 when absent or not a number).
func fieldInt(b map[string]json.RawMessage, key string) int {
	var n int
	if raw, ok := b[key]; ok {
		_ = json.Unmarshal(raw, &n)
	}
	return n
}

// fieldBool is a reply field as a bool (false when absent or not a bool).
func fieldBool(b map[string]json.RawMessage, key string) bool {
	var v bool
	if raw, ok := b[key]; ok {
		_ = json.Unmarshal(raw, &v)
	}
	return v
}
