package tbm

import (
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/TortoiseBots/turtle-agent/internal/events"
)

// Ack is a TBM:ACTION_ACK|intent|scope|count|executor reply (Comms.lua setActionAck).
type Ack struct {
	Intent string `json:"intent"`
	Scope  string `json:"scope"` // "party" or "bot:<name>"
	Count  int    `json:"count"`
	// Executor is the bot that acted, "-" for none, or the detail an intent reports (the
	// resulting "on"/"off"/"mixed" of a switch, an item id).
	Executor string `json:"executor"`
}

// Err is a TBM:ACTION_ERR|intent|code|message reply (Comms.lua setActionError).
type Err struct {
	Intent  string `json:"intent"`
	Code    string `json:"code"`
	Message string `json:"message"`
}

// ParseAck reads an ACTION_ACK line the way the addon does: four fields, a scope of party or
// bot:<name>, a numeric count and a non-empty executor.
func ParseAck(line string) (Ack, bool) {
	rest, ok := strings.CutPrefix(line, "TBM:ACTION_ACK|")
	if !ok {
		return Ack{}, false
	}
	f := strings.Split(rest, "|")
	if len(f) != 4 || strings.TrimSpace(f[0]) == "" || strings.TrimSpace(f[3]) == "" {
		return Ack{}, false
	}
	if f[1] != "party" && !(strings.HasPrefix(f[1], "bot:") && strings.TrimSpace(f[1][4:]) != "") {
		return Ack{}, false
	}
	n, err := strconv.Atoi(f[2])
	if err != nil || n < 0 {
		return Ack{}, false
	}
	return Ack{Intent: f[0], Scope: f[1], Count: n, Executor: f[3]}, true
}

// ParseErr reads an ACTION_ERR line: three fields, intent and code non-empty.
func ParseErr(line string) (Err, bool) {
	rest, ok := strings.CutPrefix(line, "TBM:ACTION_ERR|")
	if !ok {
		return Err{}, false
	}
	f := strings.Split(rest, "|")
	if len(f) != 3 || strings.TrimSpace(f[0]) == "" || strings.TrimSpace(f[1]) == "" {
		return Err{}, false
	}
	return Err{Intent: f[0], Code: f[1], Message: f[2]}, true
}

// Transports a command can take.
const (
	TransportChat  = "chat"  // ".bot <command>" as say chat, answered by system messages
	TransportAddon = "addon" // "<command>" as an addon message with prefix TBM on PARTY, answered by TBM addon messages
)

// Carried returns the line an event carries on transport: replies arrive on the transport that
// carried the request, system messages for chat and TBM addon messages for the addon channel
// (Comms.lua header, BotAddonAdapter.cpp). A TBM: line is read raw (colour stripping eats
// "|r" from fields), a plain reply line stripped.
func Carried(e events.Event, transport string) (string, bool) {
	switch {
	case transport == TransportChat && e.Kind == events.KindSystem:
		if raw, ok := e.Data["raw"].(string); ok && strings.HasPrefix(raw, "TBM:") {
			return raw, true
		}
		return e.Text, e.Text != ""
	case transport == TransportAddon && e.Kind == events.KindAddon && e.Data["prefix"] == Prefix:
		return e.Text, e.Text != ""
	}
	return "", false
}

// Reply kinds a Matcher settles on.
const (
	KindAck       = "ack"
	KindErr       = "err"
	KindText      = "text"
	KindInventory = "inventory"
	KindSnapshot  = "snapshot"
	KindTimeout   = "timeout"
)

// Reply is what the request got back.
type Reply struct {
	Kind string `json:"kind"`
	// Line is the line that settled the reply (empty on a timeout).
	Line string `json:"line,omitempty"`
	// OK: the addon would count this as success (an ACK, a success text, a snapshot).
	OK    bool     `json:"ok"`
	Ack   *Ack     `json:"ack,omitempty"`  // the first ACK
	Acks  []Ack    `json:"acks,omitempty"` // every ACK of a Multi button
	Err   *Err     `json:"err,omitempty"`
	Lines []string `json:"lines"` // every line the transport carried back, in order
}

// Matcher decides which reply lines answer one command. The request is matched on what the
// addon matches on: the ACTION_ACK/ERR intent, the INV_END of the bot, the roster trailer, or
// a text pattern; lines for other requests are kept in Lines but never settle the reply.
type Matcher struct {
	cmd      Command
	ok       []*regexp.Regexp
	fail     []*regexp.Regexp
	any      bool
	status   Status
	reply    Reply
	done     bool
	skip     int // replies of this intent still owed to earlier, timed-out requests
	skipped  int
	firstAck string
}

// MultiQuiet is how long a Multi reply stays open after its last ACK.
const MultiQuiet = 600 * time.Millisecond

// compile builds a text-reply pattern: case-insensitive, with {name} and {bot} replaced by the
// request's argument (any word when absent), so a reply names the bot it is about.
func (c Command) compile(pattern string) (*regexp.Regexp, error) {
	for _, k := range []string{"name", "bot"} {
		v := `\S+`
		if a := strings.TrimSpace(c.Args[k]); a != "" {
			v = regexp.QuoteMeta(a)
		}
		pattern = strings.ReplaceAll(pattern, "{"+k+"}", v)
	}
	return regexp.Compile("(?i)" + pattern)
}

// Skip makes the matcher pass over the first n replies that carry its intent: they answer
// earlier requests that timed out and whose ACK arrives late (there is no request id).
func (m *Matcher) Skip(n int) { m.skip = n }

// Skipped is how many replies Skip passed over.
func (m *Matcher) Skipped() int { return m.skipped }

// Quiet is the gap after which a Multi reply is complete, or 0 when the reply ends at a line.
func (m *Matcher) Quiet() time.Duration {
	if !m.any && m.cmd.Button.Multi && len(m.reply.Acks) > 0 && !m.done {
		return MultiQuiet
	}
	return 0
}

// NewMatcher builds the matcher for a resolved button command.
func NewMatcher(c Command) (*Matcher, error) {
	m := &Matcher{cmd: c, reply: Reply{Lines: []string{}}}
	for _, p := range c.Button.Ok {
		re, err := c.compile(p)
		if err != nil {
			return nil, err
		}
		m.ok = append(m.ok, re)
	}
	for _, p := range c.Button.Fail {
		re, err := c.compile(p)
		if err != nil {
			return nil, err
		}
		m.fail = append(m.fail, re)
	}
	return m, nil
}

// NewAnyMatcher settles on the first ACTION_ACK or ACTION_ERR, or on a complete roster
// response: for a raw command whose button is unknown.
func NewAnyMatcher() *Matcher {
	return &Matcher{any: true, reply: Reply{Lines: []string{}}}
}

// Feed offers one carried line and reports whether the reply is settled. After that it only
// records lines.
func (m *Matcher) Feed(line string) bool {
	m.reply.Lines = append(m.reply.Lines, line)
	if m.done {
		return true
	}
	if m.any {
		return m.feedAny(line)
	}
	b := m.cmd.Button
	switch b.Reply {
	case ReplyAck:
		return m.feedAck(line, m.cmd.Intent)
	case ReplyInventory:
		if rest, ok := strings.CutPrefix(line, "TBM:INV_END|"); ok {
			bot, _, _ := strings.Cut(rest, "|")
			if strings.EqualFold(bot, m.cmd.Args["bot"]) {
				return m.settle(KindInventory, line, true)
			}
			return false
		}
		return m.feedAck(line, m.cmd.Intent)
	case ReplySnapshot:
		if m.status.Add(line) {
			return m.settle(KindSnapshot, line, len(m.status.Errors) == 0)
		}
	case ReplyText:
		for _, re := range m.fail {
			if re.MatchString(line) {
				return m.settle(KindText, line, false)
			}
		}
		for _, re := range m.ok {
			if re.MatchString(line) {
				return m.settle(KindText, line, true)
			}
		}
		if e, ok := ParseErr(line); ok && strings.EqualFold(e.Intent, m.cmd.Intent) && m.cmd.Intent != "" {
			m.reply.Err = &e
			return m.settle(KindErr, line, false)
		}
	}
	return false
}

func (m *Matcher) feedAck(line, intent string) bool {
	if a, ok := ParseAck(line); ok && strings.EqualFold(a.Intent, intent) {
		if m.take() {
			return false
		}
		if m.cmd.Button.Multi { // one ACK per bot: Reply closes it after the quiet gap
			if len(m.reply.Acks) == 0 {
				m.firstAck = line
			}
			m.reply.Acks = append(m.reply.Acks, a)
			return false
		}
		m.reply.Ack = &a
		return m.settle(KindAck, line, true)
	}
	if e, ok := ParseErr(line); ok && strings.EqualFold(e.Intent, intent) {
		if m.take() {
			return false
		}
		m.reply.Err = &e
		return m.settle(KindErr, line, false)
	}
	return false
}

// take spends one owed reply of an earlier request.
func (m *Matcher) take() bool {
	if m.skip > m.skipped {
		m.skipped++
		return true
	}
	return false
}

func (m *Matcher) feedAny(line string) bool {
	if a, ok := ParseAck(line); ok {
		m.reply.Ack = &a
		return m.settle(KindAck, line, true)
	}
	if e, ok := ParseErr(line); ok {
		m.reply.Err = &e
		return m.settle(KindErr, line, false)
	}
	if m.status.Add(line) {
		return m.settle(KindSnapshot, line, len(m.status.Errors) == 0)
	}
	return false
}

func (m *Matcher) settle(kind, line string, ok bool) bool {
	m.reply.Kind, m.reply.Line, m.reply.OK, m.done = kind, line, ok, true
	return true
}

// Reply is the settled reply, or a timeout reply carrying the lines seen so far.
func (m *Matcher) Reply() Reply {
	r := m.reply
	switch {
	case m.done:
	case len(r.Acks) > 0: // a Multi reply ends by quiet gap or timeout, with what it has
		r.Acks = append([]Ack{}, r.Acks...)
		first := r.Acks[0]
		r.Kind, r.OK, r.Ack, r.Line = KindAck, true, &first, m.firstAck
	default:
		r.Kind = KindTimeout
	}
	r.Lines = append([]string{}, m.reply.Lines...)
	return r
}
