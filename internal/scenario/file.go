// Package scenario runs scenario files: repeatable live tests with built-in
// checks, PASS/FAIL/ERROR, raw plus normalised transcripts (README "Scenarios").
package scenario

import (
	"encoding/json"
	"fmt"
	"net"
	"os"
	"regexp"
	"sort"
	"strconv"
	"strings"
)

var (
	scenarioName = regexp.MustCompile(`^[a-z0-9][a-z0-9_-]{0,63}$`)
	sessionName  = regexp.MustCompile(`^[A-Za-z][A-Za-z0-9_]{0,15}$`)
	varName      = regexp.MustCompile(`^[a-z][a-z0-9_]{0,31}$`)
	hostName     = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,63}$`)
	contName     = regexp.MustCompile(`^[a-z0-9][a-z0-9_.-]{0,63}$`)
	methodName   = regexp.MustCompile(`^[a-z][a-z0-9_]{0,63}$`)
	whereKey     = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]{0,63}$`)
	whereValue   = regexp.MustCompile(`^[A-Za-z0-9 _.:-]{0,64}$`)
	charName     = regexp.MustCompile(`^[A-Za-z]{2,12}$`)
	// varRef matches a whole ${name} template: allowed wherever a var
	// expands to the value at run time (char names, where values with vars).
	// Dotted session built-ins (${A.character}) match too: refs plus the static
	// check prove the var exists, and the expanded value is re-checked.
	varRef = regexp.MustCompile(`^\$\{[A-Za-z][A-Za-z0-9_.]{0,63}\}$`)
)

var validOps = map[string]bool{"==": true, "!=": true, ">=": true, "<=": true, ">": true, "<": true}

// Session is one running agent the scenario talks to.
type Session struct {
	RPC       string `json:"rpc"`
	TokenFile string `json:"token_file"`
}

// DBTarget is where the read-only DB helper runs its counts.
type DBTarget struct {
	Host      string `json:"host"`
	Container string `json:"container"`
}

// NormRule rewrites one regexp on every norm line, after the built-in rules.
type NormRule struct {
	Match   string `json:"match"`
	Replace string `json:"replace"`
	re      *regexp.Regexp
}

// compile compiles the rule's regexp (Parse already does; tests use this).
func (r *NormRule) compile() error {
	re, err := regexp.Compile(r.Match)
	if err != nil {
		return err
	}
	r.re = re
	return nil
}

// Matcher picks transcript events out (section 2.5 of the spec).
type Matcher struct {
	Session string         `json:"session"`
	Kind    string         `json:"kind"`
	Prefix  string         `json:"prefix"`
	Channel string         `json:"channel"`
	Source  string         `json:"source"`
	Match   string         `json:"match"`
	Capture map[string]int `json:"capture"`
}

// ItemQuery is the no-free-SQL item count form of check_db.
type ItemQuery struct {
	Char  string   `json:"char"`
	Chars []string `json:"chars"`
	Entry int64    `json:"entry"`
	Flags *int64   `json:"flags"`
}

// Step is one scenario step; only the fields of its Do kind are set.
type Step struct {
	Do      string `json:"do"`
	Note    string `json:"note"`
	Name    string `json:"name"`
	OnFail  string `json:"on_fail"`
	Session string `json:"session"`

	// section
	Title string `json:"-"`

	// gm / bot
	Text    string `json:"-"`
	Command string `json:"-"`

	// addon_send
	Channel string `json:"-"`
	Prefix  string `json:"-"`
	To      string `json:"-"`

	// rpc
	Method       string            `json:"-"`
	Params       map[string]any    `json:"-"`
	TimeoutS     float64           `json:"-"`
	IgnoreError  bool              `json:"-"`
	Save         map[string]string `json:"-"`
	RecordResult bool              `json:"-"`

	// sleep / wait
	Seconds float64 `json:"-"`

	// matchers (wait, expect, expect_not, record)
	Matcher

	// expect_not / check_db / check_rpc
	WithinS float64 `json:"-"`

	// check_db
	Table string         `json:"-"`
	Where map[string]any `json:"-"`
	Rows  *int64         `json:"-"`
	Op    string         `json:"-"`
	Item  *ItemQuery     `json:"-"`
	Count *int64         `json:"-"`

	// check_rpc
	Path    string `json:"-"`
	Equals  any    `json:"-"`
	HasEq   bool   `json:"-"`
	MatchRe string `json:"-"`
	HasRe   bool   `json:"-"`
	Len     *int64 `json:"-"`

	SettleS    float64 `json:"-"`
	HasSettleS bool    `json:"-"`

	raw map[string]json.RawMessage `json:"-"`
}

// File is a parsed and validated scenario file.
type File struct {
	Scenario    string             `json:"scenario"`
	Description string             `json:"description"`
	Sessions    map[string]Session `json:"sessions"`
	DB          *DBTarget          `json:"db"`
	Vars        map[string]string  `json:"vars"`
	AddonPrefix string             `json:"addon_prefix"`
	SettleS     float64            `json:"settle_s"`
	TimeoutS    float64            `json:"timeout_s"`
	OnFail      string             `json:"on_fail"`
	MaxMinutes  float64            `json:"max_minutes"`
	Record      []Matcher          `json:"record"`
	Normalise   []NormRule         `json:"normalise"`
	Steps       []*Step            `json:"steps"`
	Finally     []*Step            `json:"finally"`
	path        string
}

// Load reads and validates a scenario file, applying --var overrides.
func Load(path string, vars map[string]string) (*File, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	f, err := Parse(data, vars)
	if err != nil {
		return nil, err
	}
	f.path = path
	return f, nil
}

// Parse decodes and validates a scenario file. vars holds --var overrides:
// every name must exist in the file's vars.
func Parse(data []byte, vars map[string]string) (*File, error) {
	dec := json.NewDecoder(strings.NewReader(string(data)))
	dec.DisallowUnknownFields()
	var raw struct {
		Scenario    *string            `json:"scenario"`
		Description *string            `json:"description"`
		Sessions    map[string]Session `json:"sessions"`
		DB          *DBTarget          `json:"db"`
		Vars        map[string]string  `json:"vars"`
		AddonPrefix *string            `json:"addon_prefix"`
		SettleS     *float64           `json:"settle_s"`
		TimeoutS    *float64           `json:"timeout_s"`
		OnFail      *string            `json:"on_fail"`
		MaxMinutes  *float64           `json:"max_minutes"`
		Record      []json.RawMessage  `json:"record"`
		Normalise   []NormRule         `json:"normalise"`
		Steps       []json.RawMessage  `json:"steps"`
		Finally     []json.RawMessage  `json:"finally"`
	}
	if err := dec.Decode(&raw); err != nil {
		return nil, fmt.Errorf("scenario: %v", err)
	}
	f := &File{Sessions: raw.Sessions, DB: raw.DB, Vars: map[string]string{}, Record: []Matcher{}}
	if raw.Scenario == nil || !scenarioName.MatchString(*raw.Scenario) {
		return nil, fmt.Errorf("scenario: bad scenario name %q", strOr(raw.Scenario))
	}
	f.Scenario = *raw.Scenario
	if raw.Description != nil {
		f.Description = *raw.Description
	}
	if len(f.Sessions) == 0 || len(f.Sessions) > 8 {
		return nil, fmt.Errorf("scenario: need 1-8 sessions, have %d", len(f.Sessions))
	}
	for name, s := range f.Sessions {
		if !sessionName.MatchString(name) {
			return nil, fmt.Errorf("scenario: bad session name %q", name)
		}
		if err := CheckAddr(name, s.RPC); err != nil {
			return nil, err
		}
	}
	if raw.Vars != nil {
		for k, v := range raw.Vars {
			if !varName.MatchString(k) {
				return nil, fmt.Errorf("scenario: bad var name %q", k)
			}
			f.Vars[k] = v
		}
	}
	for k := range vars {
		if _, ok := f.Vars[k]; !ok {
			return nil, fmt.Errorf("scenario: --var %s: the scenario has no var %s", k, k)
		}
	}
	for k, v := range vars {
		f.Vars[k] = v
	}
	if raw.AddonPrefix != nil {
		f.AddonPrefix = *raw.AddonPrefix
	}
	f.SettleS = 1
	if raw.SettleS != nil {
		f.SettleS = *raw.SettleS
	}
	if f.SettleS < 0 || f.SettleS > 60 {
		return nil, fmt.Errorf("scenario: settle_s %g out of range 0-60", f.SettleS)
	}
	f.TimeoutS = 10
	if raw.TimeoutS != nil {
		f.TimeoutS = *raw.TimeoutS
	}
	if f.TimeoutS < 1 || f.TimeoutS > 3600 {
		return nil, fmt.Errorf("scenario: timeout_s %g out of range 1-3600", f.TimeoutS)
	}
	f.OnFail = "continue"
	if raw.OnFail != nil {
		f.OnFail = *raw.OnFail
	}
	if f.OnFail != "continue" && f.OnFail != "stop" {
		return nil, fmt.Errorf("scenario: bad on_fail %q", f.OnFail)
	}
	f.MaxMinutes = 30
	if raw.MaxMinutes != nil {
		f.MaxMinutes = *raw.MaxMinutes
	}
	if f.MaxMinutes < 1 || f.MaxMinutes > 360 {
		return nil, fmt.Errorf("scenario: max_minutes %g out of range 1-360", f.MaxMinutes)
	}
	for i, r := range raw.Record {
		m, err := parseMatcher(r, false)
		if err != nil {
			return nil, fmt.Errorf("scenario: record %d: %v", i, err)
		}
		f.Record = append(f.Record, m)
	}
	if len(raw.Normalise) > 50 {
		return nil, fmt.Errorf("scenario: more than 50 normalise rules")
	}
	for i := range raw.Normalise {
		re, err := regexp.Compile(raw.Normalise[i].Match)
		if err != nil {
			return nil, fmt.Errorf("scenario: normalise %d: bad match regexp: %v", i, err)
		}
		raw.Normalise[i].re = re
	}
	f.Normalise = raw.Normalise
	if len(raw.Steps) == 0 || len(raw.Steps) > 500 {
		return nil, fmt.Errorf("scenario: need 1-500 steps, have %d", len(raw.Steps))
	}
	for i, r := range raw.Steps {
		s, err := parseStep(r, i+1, false)
		if err != nil {
			return nil, err
		}
		f.Steps = append(f.Steps, s)
	}
	if len(raw.Finally) > 50 {
		return nil, fmt.Errorf("scenario: more than 50 finally steps")
	}
	for i, r := range raw.Finally {
		s, err := parseStep(r, i+1, true)
		if err != nil {
			if strings.HasPrefix(err.Error(), "step ") {
				err = fmt.Errorf("finally step %d%s", i+1, strings.TrimPrefix(err.Error(), fmt.Sprintf("step %d", i+1)))
			}
			return nil, err
		}
		f.Finally = append(f.Finally, s)
	}
	if err := f.validate(); err != nil {
		return nil, err
	}
	return f, nil
}

func strOr(s *string) string {
	if s == nil {
		return ""
	}
	return *s
}

// CheckAddr pins the RPC address to loopback: a bearer token must never
// leave the machine. The cmd layer re-runs it on every --session override.
func CheckAddr(session, addr string) error {
	host, port, err := splitAddr(addr)
	if err != nil {
		return fmt.Errorf("session %s: rpc %q is not a loopback address (the agent only listens on loopback; a token must never leave the machine)", session, addr)
	}
	if host != "127.0.0.1" && host != "localhost" && host != "::1" {
		return fmt.Errorf("session %s: rpc %q is not a loopback address (the agent only listens on loopback; a token must never leave the machine)", session, addr)
	}
	if port < 1 || port > 65535 {
		return fmt.Errorf("session %s: rpc %q is not a loopback address (the agent only listens on loopback; a token must never leave the machine)", session, addr)
	}
	return nil
}

func splitAddr(addr string) (string, int, error) {
	host, portS, err := net.SplitHostPort(addr)
	if err != nil {
		return "", 0, err
	}
	if portS == "" {
		return "", 0, fmt.Errorf("no port")
	}
	for _, c := range []byte(portS) {
		if c < '0' || c > '9' {
			return "", 0, fmt.Errorf("bad port")
		}
	}
	port, err := strconv.Atoi(portS)
	if err != nil {
		return "", 0, err
	}
	return host, port, nil
}

// actionKinds are the step kinds allowed in finally.
var actionKinds = map[string]bool{"section": true, "mark": true, "gm": true, "bot": true, "addon_send": true, "rpc": true, "sleep": true}

// checkKinds are the step kinds that take name/on_fail.
var checkKinds = map[string]bool{"expect": true, "expect_not": true, "check_db": true, "check_rpc": true}

var stepFields = map[string]map[string]bool{
	"section":    {"do": true, "note": true, "title": true},
	"mark":       {"do": true, "note": true},
	"gm":         {"do": true, "note": true, "session": true, "text": true, "settle_s": true},
	"bot":        {"do": true, "note": true, "session": true, "command": true, "settle_s": true},
	"addon_send": {"do": true, "note": true, "session": true, "text": true, "channel": true, "prefix": true, "to": true, "settle_s": true},
	"rpc":        {"do": true, "note": true, "session": true, "method": true, "params": true, "timeout_s": true, "ignore_error": true, "save": true, "record_result": true, "settle_s": true},
	"sleep":      {"do": true, "note": true, "seconds": true},
	"wait":       {"do": true, "note": true, "session": true, "kind": true, "prefix": true, "channel": true, "source": true, "match": true, "timeout_s": true, "capture": true},
	"expect":     {"do": true, "note": true, "name": true, "on_fail": true, "session": true, "kind": true, "prefix": true, "channel": true, "source": true, "match": true, "timeout_s": true, "capture": true},
	"expect_not": {"do": true, "note": true, "name": true, "on_fail": true, "session": true, "kind": true, "prefix": true, "channel": true, "source": true, "match": true, "within_s": true},
	"check_db":   {"do": true, "note": true, "name": true, "on_fail": true, "within_s": true, "op": true, "table": true, "where": true, "rows": true, "item": true, "count": true},
	"check_rpc":  {"do": true, "note": true, "name": true, "on_fail": true, "session": true, "method": true, "params": true, "path": true, "equals": true, "match": true, "len": true, "within_s": true, "timeout_s": true},
}

func parseStep(raw json.RawMessage, n int, inFinally bool) (*Step, error) {
	var kv map[string]json.RawMessage
	dec := json.NewDecoder(strings.NewReader(string(raw)))
	dec.DisallowUnknownFields()
	_ = dec
	if err := json.Unmarshal(raw, &kv); err != nil {
		return nil, fmt.Errorf("step %d: %v", n, err)
	}
	var do struct {
		Do string `json:"do"`
	}
	if err := json.Unmarshal(raw, &do); err != nil || do.Do == "" {
		return nil, fmt.Errorf("step %d: missing do", n)
	}
	allowed, ok := stepFields[do.Do]
	if !ok {
		return nil, fmt.Errorf("step %d: bad do %q", n, do.Do)
	}
	if inFinally && !actionKinds[do.Do] {
		return nil, fmt.Errorf("finally step %d (%s): checks are not allowed in finally", n, do.Do)
	}
	for k := range kv {
		if !allowed[k] {
			return nil, fmt.Errorf("step %d (%s): unknown field %q", n, do.Do, k)
		}
	}
	s := &Step{Do: do.Do, raw: kv}
	g := func(key string, dst any) bool {
		v, ok := kv[key]
		if !ok {
			return false
		}
		if err := json.Unmarshal(v, dst); err != nil {
			return false
		}
		return true
	}
	var note, name, onFail, sess, kind, prefix, channel, source, match, title, text, cmd, to, method, path string
	var tm, ch, src, mre string
	_ = tm
	_ = ch
	_ = src
	_ = mre
	g("note", &note)
	s.Note = note
	if checkKinds[do.Do] {
		g("name", &name)
		s.Name = name
		g("on_fail", &onFail)
		s.OnFail = onFail
		if onFail != "" && onFail != "continue" && onFail != "stop" {
			return nil, fmt.Errorf("step %d (%s): bad on_fail %q", n, do.Do, onFail)
		}
	}
	needSession := map[string]bool{"gm": true, "bot": true, "addon_send": true, "rpc": true, "wait": true, "expect": true, "expect_not": true, "check_rpc": true}
	if needSession[do.Do] {
		g("session", &sess)
		if sess == "" {
			return nil, fmt.Errorf("step %d (%s): missing session", n, do.Do)
		}
		s.Session = sess
	}
	switch do.Do {
	case "section":
		g("title", &title)
		if title == "" {
			return nil, fmt.Errorf("step %d (section): missing title", n)
		}
		s.Title = title
	case "gm":
		g("text", &text)
		if text == "" {
			return nil, fmt.Errorf("step %d (gm): missing text", n)
		}
		s.Text = text
		if err := gmLoadCheck(n, text); err != nil {
			return nil, err
		}
	case "bot":
		g("command", &cmd)
		if cmd == "" {
			return nil, fmt.Errorf("step %d (bot): missing command", n)
		}
		s.Command = cmd
	case "addon_send":
		g("text", &text)
		g("channel", &channel)
		if text == "" || channel == "" {
			return nil, fmt.Errorf("step %d (addon_send): missing text or channel", n)
		}
		s.Text = text
		s.Channel = channel
		g("prefix", &prefix)
		s.Prefix = prefix
		g("to", &to)
		s.To = to
	case "rpc":
		g("method", &method)
		if method == "" || !methodName.MatchString(method) {
			return nil, fmt.Errorf("step %d (rpc): bad method %q", n, method)
		}
		s.Method = method
		var params map[string]any
		if v, ok := kv["params"]; ok {
			if err := json.Unmarshal(v, &params); err != nil {
				return nil, fmt.Errorf("step %d (rpc): bad params: %v", n, err)
			}
		}
		if params == nil {
			params = map[string]any{}
		}
		s.Params = params
		var ts float64
		if g("timeout_s", &ts) {
			if ts < 1 || ts > 600 {
				return nil, fmt.Errorf("step %d (rpc): timeout_s %g out of range 1-600", n, ts)
			}
			s.TimeoutS = ts
		}
		var ie bool
		if g("ignore_error", &ie) {
			s.IgnoreError = ie
		}
		var save map[string]string
		if v, ok := kv["save"]; ok {
			if err := json.Unmarshal(v, &save); err != nil {
				return nil, fmt.Errorf("step %d (rpc): bad save: %v", n, err)
			}
			for k, p := range save {
				if !varName.MatchString(k) || p == "" {
					return nil, fmt.Errorf("step %d (rpc): bad save %q", n, k)
				}
			}
			s.Save = save
		}
		var rr bool
		if g("record_result", &rr) {
			s.RecordResult = rr
		}
		if s.Method == "gm" {
			var pt struct {
				Text string `json:"text"`
			}
			if v, ok := kv["params"]; ok {
				_ = json.Unmarshal(v, &pt)
			}
			if pt.Text != "" {
				if err := gmLoadCheck(n, pt.Text); err != nil {
					return nil, err
				}
			}
		}
	case "sleep":
		var secs float64
		if !g("seconds", &secs) || secs <= 0 || secs > 3600 {
			return nil, fmt.Errorf("step %d (sleep): bad seconds", n)
		}
		s.Seconds = secs
	case "wait", "expect", "expect_not":
		s.Matcher = Matcher{Session: sess}
		g("kind", &kind)
		s.Kind = kind
		g("prefix", &prefix)
		s.Prefix = prefix
		g("channel", &channel)
		s.Channel = channel
		g("source", &source)
		s.Source = source
		g("match", &match)
		if match == "" && do.Do != "expect_not" {
			// record matchers may omit match; steps require it
		}
		if do.Do != "expect_not" && match == "" {
			if do.Do == "wait" || do.Do == "expect" {
				return nil, fmt.Errorf("step %d (%s): missing match", n, do.Do)
			}
		}
		s.Match = match
		if match != "" {
			if _, err := regexp.Compile(match); err != nil {
				return nil, fmt.Errorf("step %d (%s): bad match regexp: %v", n, do.Do, err)
			}
		}
		if do.Do == "expect_not" && match == "" {
			return nil, fmt.Errorf("step %d (expect_not): missing match", n)
		}
		if v, ok := kv["capture"]; ok {
			var cap map[string]int
			if err := json.Unmarshal(v, &cap); err != nil {
				return nil, fmt.Errorf("step %d (%s): bad capture: %v", n, do.Do, err)
			}
			re := regexp.MustCompile(match)
			for k, grp := range cap {
				if !varName.MatchString(k) {
					return nil, fmt.Errorf("step %d (%s): bad capture var %q", n, do.Do, k)
				}
				if grp < 1 || grp > re.NumSubexp() {
					return nil, fmt.Errorf("step %d (%s): capture group %d out of range", n, do.Do, grp)
				}
			}
			s.Capture = cap
		}
		if do.Do == "expect_not" {
			var w float64
			if !g("within_s", &w) || w < 0 || w > 600 {
				return nil, fmt.Errorf("step %d (expect_not): bad within_s", n)
			}
			s.WithinS = w
		} else if do.Do == "expect" || do.Do == "wait" {
			var ts float64
			if g("timeout_s", &ts) {
				if ts < 1 || ts > 3600 {
					return nil, fmt.Errorf("step %d (%s): timeout_s %g out of range", n, do.Do, ts)
				}
				s.TimeoutS = ts
			}
		}
	case "check_db":
		var w float64
		if g("within_s", &w) {
			if w < 0 || w > 600 {
				return nil, fmt.Errorf("step %d (check_db): bad within_s", n)
			}
			s.WithinS = w
		}
		var op string
		if g("op", &op) {
			if !validOps[op] {
				return nil, fmt.Errorf("step %d (check_db): bad op %q", n, op)
			}
			s.Op = op
		} else {
			s.Op = "=="
		}
		_, hasTable := kv["table"]
		_, hasItem := kv["item"]
		if hasTable == hasItem {
			return nil, fmt.Errorf("step %d (check_db): exactly one of table or item", n)
		}
		if hasTable {
			var table string
			g("table", &table)
			if !allowedTable(table) {
				return nil, fmt.Errorf("step %d (check_db): table %s is not on the read-only allowlist (internal/scenario/db.go)", n, table)
			}
			s.Table = table
			var where map[string]any
			if v, ok := kv["where"]; ok {
				if err := json.Unmarshal(v, &where); err != nil {
					return nil, fmt.Errorf("step %d (check_db): bad where: %v", n, err)
				}
				if len(where) > 8 {
					return nil, fmt.Errorf("step %d (check_db): more than 8 where keys", n)
				}
				for k, v := range where {
					if !whereKey.MatchString(k) {
						return nil, fmt.Errorf("step %d (check_db): bad where key %q", n, k)
					}
					switch t := v.(type) {
					case string:
						// A whole ${var} template expands at run time (and
						// is re-checked there); literals must match now.
						if !whereValue.MatchString(t) && !varRef.MatchString(t) {
							return nil, fmt.Errorf("step %d (check_db): bad where value %q", n, t)
						}
					case float64:
						if t != float64(int64(t)) {
							return nil, fmt.Errorf("step %d (check_db): bad where value for %q", n, k)
						}
					default:
						return nil, fmt.Errorf("step %d (check_db): bad where value for %q", n, k)
					}
				}
			}
			s.Where = where
			var rows float64
			if !g("rows", &rows) || rows < 0 || rows != float64(int64(rows)) {
				return nil, fmt.Errorf("step %d (check_db): bad rows", n)
			}
			r := int64(rows)
			s.Rows = &r
		} else {
			var iq struct {
				Char  *string  `json:"char"`
				Chars []string `json:"chars"`
				Entry *float64 `json:"entry"`
				Flags *float64 `json:"flags"`
			}
			if v, ok := kv["item"]; ok {
				dec := json.NewDecoder(strings.NewReader(string(v)))
				dec.DisallowUnknownFields()
				var rawItem map[string]json.RawMessage
				if err := json.Unmarshal(v, &rawItem); err != nil {
					return nil, fmt.Errorf("step %d (check_db): bad item: %v", n, err)
				}
				for k := range rawItem {
					if k != "char" && k != "chars" && k != "entry" && k != "flags" {
						return nil, fmt.Errorf("step %d (check_db): unknown item field %q", n, k)
					}
				}
				if err := json.Unmarshal(v, &iq); err != nil {
					return nil, fmt.Errorf("step %d (check_db): bad item: %v", n, err)
				}
			}
			if (iq.Char == nil) == (iq.Chars == nil) {
				return nil, fmt.Errorf("step %d (check_db): exactly one of char or chars", n)
			}
			q := &ItemQuery{}
			// Names may hold ${vars} (expanded and re-checked at run
			// time); otherwise they must already match.
			okName := func(name string) bool {
				if charName.MatchString(name) {
					return true
				}
				return varRef.MatchString(name)
			}
			if iq.Char != nil {
				if !okName(*iq.Char) {
					return nil, fmt.Errorf("step %d (check_db): bad char name %q", n, *iq.Char)
				}
				q.Char = *iq.Char
			} else {
				if len(iq.Chars) == 0 || len(iq.Chars) > 10 {
					return nil, fmt.Errorf("step %d (check_db): bad chars", n)
				}
				for _, c := range iq.Chars {
					if !okName(c) {
						return nil, fmt.Errorf("step %d (check_db): bad char name %q", n, c)
					}
				}
				q.Chars = iq.Chars
			}
			if iq.Entry == nil || *iq.Entry < 1 || *iq.Entry > 16777215 || *iq.Entry != float64(int64(*iq.Entry)) {
				return nil, fmt.Errorf("step %d (check_db): bad entry", n)
			}
			q.Entry = int64(*iq.Entry)
			if iq.Flags != nil {
				if *iq.Flags < 0 || *iq.Flags != float64(int64(*iq.Flags)) {
					return nil, fmt.Errorf("step %d (check_db): bad flags", n)
				}
				fl := int64(*iq.Flags)
				q.Flags = &fl
			}
			s.Item = q
			var count float64
			if !g("count", &count) || count < 0 || count != float64(int64(count)) {
				return nil, fmt.Errorf("step %d (check_db): bad count", n)
			}
			c := int64(count)
			s.Count = &c
		}
	case "check_rpc":
		g("method", &method)
		if method == "" || !methodName.MatchString(method) {
			return nil, fmt.Errorf("step %d (check_rpc): bad method %q", n, method)
		}
		s.Method = method
		var params map[string]any
		if v, ok := kv["params"]; ok {
			if err := json.Unmarshal(v, &params); err != nil {
				return nil, fmt.Errorf("step %d (check_rpc): bad params: %v", n, err)
			}
		}
		if params == nil {
			params = map[string]any{}
		}
		s.Params = params
		g("path", &path)
		if path == "" {
			return nil, fmt.Errorf("step %d (check_rpc): missing path", n)
		}
		s.Path = path
		_, hasEq := kv["equals"]
		_, hasRe := kv["match"]
		_, hasLen := kv["len"]
		if (hasEq && hasRe) || (hasEq && hasLen) || (hasRe && hasLen) || (!hasEq && !hasRe && !hasLen) {
			return nil, fmt.Errorf("step %d (check_rpc): exactly one of equals, match, len", n)
		}
		if hasEq {
			var eq any
			if err := json.Unmarshal(kv["equals"], &eq); err != nil {
				return nil, fmt.Errorf("step %d (check_rpc): bad equals: %v", n, err)
			}
			s.Equals = eq
			s.HasEq = true
		}
		if hasRe {
			g("match", &mre)
			if _, err := regexp.Compile(mre); err != nil {
				return nil, fmt.Errorf("step %d (check_rpc): bad match regexp: %v", n, err)
			}
			s.MatchRe = mre
			s.HasRe = true
		}
		if hasLen {
			var l float64
			if !g("len", &l) || l < 0 || l != float64(int64(l)) {
				return nil, fmt.Errorf("step %d (check_rpc): bad len", n)
			}
			li := int64(l)
			s.Len = &li
		}
		var w float64
		if g("within_s", &w) {
			if w < 0 || w > 600 {
				return nil, fmt.Errorf("step %d (check_rpc): bad within_s", n)
			}
			s.WithinS = w
		}
		var ts float64
		if g("timeout_s", &ts) {
			if ts < 1 || ts > 600 {
				return nil, fmt.Errorf("step %d (check_rpc): timeout_s %g out of range", n, ts)
			}
			s.TimeoutS = ts
		}
	}
	if v, ok := kv["settle_s"]; ok && (do.Do == "gm" || do.Do == "bot" || do.Do == "addon_send" || do.Do == "rpc") {
		var ss float64
		if err := json.Unmarshal(v, &ss); err != nil || ss < 0 || ss > 60 {
			return nil, fmt.Errorf("step %d (%s): bad settle_s", n, do.Do)
		}
		s.SettleS = ss
		s.HasSettleS = true
	}
	return s, nil
}

func parseMatcher(raw json.RawMessage, _ bool) (Matcher, error) {
	var kv map[string]json.RawMessage
	if err := json.Unmarshal(raw, &kv); err != nil {
		return Matcher{}, err
	}
	for k := range kv {
		switch k {
		case "session", "kind", "prefix", "channel", "source", "match":
		default:
			return Matcher{}, fmt.Errorf("unknown field %q", k)
		}
	}
	var m Matcher
	for k, v := range kv {
		var s string
		if err := json.Unmarshal(v, &s); err != nil {
			return Matcher{}, fmt.Errorf("bad %s", k)
		}
		switch k {
		case "session":
			m.Session = s
		case "kind":
			m.Kind = s
		case "prefix":
			m.Prefix = s
		case "channel":
			m.Channel = s
		case "source":
			m.Source = s
		case "match":
			m.Match = s
		}
	}
	if m.Match != "" {
		if _, err := regexp.Compile(m.Match); err != nil {
			return Matcher{}, fmt.Errorf("bad match regexp: %v", err)
		}
	}
	return m, nil
}

// gmDenied reports whether expanded GM text changes the world for everyone
// (AGENTS.md): reload, server, account, ban/unban, npc add/delete/move/set,
// gobject add/delete.
func gmDenied(text string) bool {
	t := strings.ToLower(strings.TrimSpace(text))
	t = strings.TrimPrefix(t, ".")
	fields := strings.Fields(t)
	if len(fields) == 0 {
		return false
	}
	switch fields[0] {
	case "reload", "server", "account", "ban", "unban":
		return true
	}
	if len(fields) >= 2 {
		if fields[0] == "npc" {
			switch fields[1] {
			case "add", "delete", "move", "set":
				return true
			}
		}
		if fields[0] == "gobject" {
			switch fields[1] {
			case "add", "delete":
				return true
			}
		}
	}
	return false
}

func gmLoadCheck(n int, text string) error {
	expanded, err := expand(text, map[string]string{}, false)
	if err != nil {
		// Unset vars stay for the static check below; only test the
		// literal part here.
		expanded = text
	}
	if gmDenied(expanded) {
		short := expanded
		if len(short) > 60 {
			short = short[:60]
		}
		return fmt.Errorf("step %d: GM command %q changes the world for everyone (AGENTS.md): refused", n, short)
	}
	return nil
}

// validate runs the cross-step rules: session names exist, addon prefix
// defaults, DB presence, and the static var use-before-set check.
func (f *File) validate() error {
	for i, s := range f.Steps {
		n := i + 1
		if s.Session != "" {
			if _, ok := f.Sessions[s.Session]; !ok && s.Do != "check_db" {
				return fmt.Errorf("step %d (%s): unknown session %q", n, s.Do, s.Session)
			}
		}
		if s.Do == "check_db" && f.DB == nil {
			return fmt.Errorf("step %d (check_db): the scenario has no db target", n)
		}
		if f.DB != nil {
			if !hostName.MatchString(f.DB.Host) || strings.Contains(f.DB.Host, "@") || strings.HasPrefix(f.DB.Host, "-") {
				return fmt.Errorf("scenario: bad db host %q", f.DB.Host)
			}
			if !contName.MatchString(f.DB.Container) {
				return fmt.Errorf("scenario: bad db container %q", f.DB.Container)
			}
		}
	}
	if f.DB != nil {
		if !hostName.MatchString(f.DB.Host) {
			return fmt.Errorf("scenario: bad db host %q", f.DB.Host)
		}
		if !contName.MatchString(f.DB.Container) {
			return fmt.Errorf("scenario: bad db container %q", f.DB.Container)
		}
	}
	// Static var check: walk the steps in order; every ${x} must be a
	// built-in, a file var, or set by an earlier capture/save.
	have := map[string]bool{}
	for k := range f.Vars {
		have[k] = true
	}
	for name := range f.Sessions {
		have[name+".character"] = true
	}
	checkRefs := func(prefix, s string) error {
		for _, r := range refs(s) {
			if !have[r] {
				return fmt.Errorf("%s: ${%s} is never set before this step", prefix, r)
			}
		}
		return nil
	}
	for i, s := range f.Steps {
		n := i + 1
		var tmpl []string
		switch s.Do {
		case "gm":
			tmpl = append(tmpl, s.Text)
		case "bot":
			tmpl = append(tmpl, s.Command)
		case "addon_send":
			tmpl = append(tmpl, s.Text, s.To)
		case "rpc":
			tmpl = append(tmpl, s.Method)
			tmpl = append(tmpl, jsonStrings(s.Params)...)
			if s.Method == "gm" {
				if t, _ := s.Params["text"].(string); t != "" {
					tmpl = append(tmpl, t)
				}
			}
		case "expect", "wait", "expect_not":
			tmpl = append(tmpl, s.Match)
			tmpl = append(tmpl, s.Source)
			tmpl = append(tmpl, s.Prefix)
			tmpl = append(tmpl, s.Channel)
		case "check_db":
			for _, v := range s.Where {
				if str, ok := v.(string); ok {
					tmpl = append(tmpl, str)
				}
			}
			if s.Item != nil {
				if s.Item.Char != "" {
					tmpl = append(tmpl, s.Item.Char)
				}
				tmpl = append(tmpl, s.Item.Chars...)
			}
		case "check_rpc":
			tmpl = append(tmpl, jsonStrings(s.Params)...)
			tmpl = append(tmpl, jsonStrings(s.Equals)...)
			tmpl = append(tmpl, s.Path)
		}
		for _, t := range tmpl {
			if err := checkRefs(fmt.Sprintf("step %d (%s)", n, s.Do), t); err != nil {
				return err
			}
		}
		for k := range s.Capture {
			have[k] = true
		}
		for k := range s.Save {
			have[k] = true
		}
	}
	for i, m := range f.Record {
		for _, t := range []string{m.Match, m.Source, m.Prefix, m.Channel} {
			if err := checkRefs(fmt.Sprintf("record %d", i), t); err != nil {
				return err
			}
		}
	}
	// finally sees every var any main step can set.
	for i, s := range f.Finally {
		if s.Session != "" {
			if _, ok := f.Sessions[s.Session]; !ok {
				return fmt.Errorf("finally step %d (%s): unknown session %q", i+1, s.Do, s.Session)
			}
		}
		var tmpl []string
		switch s.Do {
		case "gm":
			tmpl = append(tmpl, s.Text)
		case "bot":
			tmpl = append(tmpl, s.Command)
		case "addon_send":
			tmpl = append(tmpl, s.Text, s.To)
		case "rpc":
			tmpl = append(tmpl, s.Method)
			tmpl = append(tmpl, jsonStrings(s.Params)...)
			if s.Method == "gm" {
				if t, _ := s.Params["text"].(string); t != "" {
					tmpl = append(tmpl, t)
				}
			}
		}
		for _, t := range tmpl {
			if err := checkRefs(fmt.Sprintf("finally step %d (%s)", i+1, s.Do), t); err != nil {
				return err
			}
		}
	}
	return nil
}

func jsonStrings(v any) []string {
	var out []string
	var walk func(x any)
	walk = func(x any) {
		switch t := x.(type) {
		case string:
			out = append(out, t)
		case map[string]any:
			keys := make([]string, 0, len(t))
			for k := range t {
				keys = append(keys, k)
			}
			sort.Strings(keys)
			for _, k := range keys {
				walk(t[k])
			}
		case []any:
			for _, e := range t {
				walk(e)
			}
		}
	}
	walk(v)
	return out
}
