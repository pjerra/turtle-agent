// Package trace is the run trace: one JSONL file per dungeon run or brain session, schema "bt-trace" version 1, plus a small
// <run_id>.summary.json beside it that a dashboard can list without parsing the trace.
//
// The format is the one the TortoiseBots server recorder writes (docs/guides/run-trace.md in TortoiseBots), so one scorer and one
// dashboard read both and the two files of a run join on the run id. Every line is a flat JSON object that starts with the same
// five fields: v (the schema version, 1), k (the record kind), run (the run id), src (the producer: "agent" here, "server" there)
// and t (milliseconds since the run started).
//
// Kinds: header (once, first: scope, agent build, start time, interval, caps, and the agent's own fields: mode, plan, options, meta),
// party (one per member, when first seen), mobs (one per hostile, the first time it is in a frame), frame (about once a second:
// bots and mobs arrays), event (ev, and a, b, s, s2 as there, plus the agent's own fields: orders, rest gates, walks, ...), end (once:
// reason and counters) and oracle (one per check, appended after the end; the server keeps its verdicts in the summary only).
//
// A reader ignores record kinds, fields and events it does not know and refuses a file whose v is higher than it reads.
package trace

import (
	"bytes"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"regexp"
	"sort"
	"time"
)

// Schema names the format; Version is its version; Producer is what src says for the agent's traces.
const (
	Schema   = "bt-trace"
	Version  = 1
	Producer = "agent"
)

// Record kinds (the k field).
const (
	KindHeader = "header"
	KindParty  = "party"
	KindMobs   = "mobs"
	KindFrame  = "frame"
	KindEvent  = "event"
	KindEnd    = "end"
	KindOracle = "oracle"
)

// Verdicts of an oracle.
const (
	Pass = "pass"
	Fail = "fail"
	NA   = "na" // the trace does not have what the check needs
)

var runIDPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,63}$`)

// ValidRunID says whether s can be a run id: it names a file, so letters, digits, dot, underscore and dash only.
func ValidRunID(s string) bool { return runIDPattern.MatchString(s) }

// NewRunID makes an id such as r-20261014T093012-a1b2: the UTC start time and four random hex digits.
func NewRunID(t time.Time) string {
	var b [2]byte
	if _, err := rand.Read(b[:]); err != nil {
		b = [2]byte{byte(t.Nanosecond() >> 8), byte(t.Nanosecond())}
	}
	return "r-" + t.UTC().Format("20060102T150405") + "-" + hex.EncodeToString(b[:])
}

// Envelope starts every record.
type Envelope struct {
	V   int    `json:"v"`
	K   string `json:"k"`
	Run string `json:"run"`
	Src string `json:"src"`
	T   int64  `json:"t"` // ms since the start of the run
}

// Capabilities a producer lists in the header (caps). A scorer answers na for a check whose capability is missing. The server's:
// facing, moving, interrupts, in_fire. The agent uses facing and moving where it can see them and adds the ones below.
const (
	CapFacing    = "facing"    // bots have fa (degrees off their hostile target)
	CapMoving    = "moving"    // bots have mv (a movement is active)
	CapThreat    = "threat"    // mobs have thr (threat lists)
	CapOrders    = "orders"    // order events
	CapWalks     = "walks"     // walk_start and walk_end events
	CapPartyList = "partylist" // the bots' hp and power come from the party list, so a list that stops updating shows
)

// Agent says which build wrote the trace.
type Agent struct {
	Version string `json:"version"`
	SHA     string `json:"sha,omitempty"`
}

// Member is a party member (a party record). Class and level are what the agent could see (a remote run sees neither). Role is only
// set where known: tank, heal or dps.
type Member struct {
	G     string `json:"g"`
	Name  string `json:"name"`
	Class string `json:"class,omitempty"`
	Role  string `json:"role,omitempty"`
	Level int    `json:"level,omitempty"`
	Lead  bool   `json:"lead,omitempty"` // the member that walks: the agent's own character (group) or the lead bot (remote)
}

// Header is the first record. The fields from Source on are the agent's own; Party is written as party records, not in the header.
type Header struct {
	Envelope
	Schema       string   `json:"schema"`
	Scope        string   `json:"scope"` // party
	ScopeName    string   `json:"scope_name,omitempty"`
	Requester    string   `json:"requester,omitempty"` // the agent's character
	AgentVersion string   `json:"agent_version,omitempty"`
	AgentSHA     string   `json:"agent_sha,omitempty"`
	StartUnix    int64    `json:"start_unix"`
	IntervalMS   int      `json:"interval_ms"`
	MaxBytes     int64    `json:"max_bytes"`
	Map          uint32   `json:"map"`
	Instance     uint32   `json:"instance"`
	Caps         []string `json:"caps"`

	Source  string            `json:"source,omitempty"` // dungeon_run, brain
	Mode    string            `json:"mode,omitempty"`   // group or remote
	Plan    string            `json:"plan,omitempty"`
	FromLeg int               `json:"from_leg,omitempty"`
	Start   time.Time         `json:"start"`
	Lead    string            `json:"lead,omitempty"` // the character that walks
	Tank    string            `json:"tank,omitempty"`
	Brain   string            `json:"brain,omitempty"`
	Options map[string]any    `json:"options,omitempty"` // the run's choices (teleport, shortcuts, recover_wipe, setup, buffs, ...)
	Meta    map[string]string `json:"meta,omitempty"`    // free-form from the caller: server build, TortoiseBots SHA, scenario, ...
	Party   []Member          `json:"-"`
}

// HasCap says whether the header lists capability c.
func (h Header) HasCap(c string) bool {
	for _, x := range h.Caps {
		if x == c {
			return true
		}
	}
	return false
}

// PartyRecord is a party record.
type PartyRecord struct {
	Envelope
	Member
}

// MobInfo is a hostile's registry entry.
type MobInfo struct {
	G     string `json:"g"`
	Entry uint32 `json:"entry,omitempty"`
	Name  string `json:"name,omitempty"`
	Level int    `json:"level,omitempty"`
	Rank  int    `json:"rank,omitempty"`
	Boss  bool   `json:"boss,omitempty"`
}

// MobRecord is a mobs record.
type MobRecord struct {
	Envelope
	MobInfo
}

// Bot is a party member in a frame, keyed by its guid (the name is in the party record).
type Bot struct {
	G      string  `json:"g"`
	Alive  bool    `json:"al"`
	Combat bool    `json:"cb"`
	State  string  `json:"st"` // engine state: c combat, n non-combat, d dead, r reaction
	HP     uint32  `json:"hp"`
	MaxHP  uint32  `json:"mhp"`
	Power  uint32  `json:"pw"`
	MaxPow uint32  `json:"mpw"`
	PType  uint8   `json:"pt"` // 0 mana, 1 rage, 2 focus, 3 energy
	X      float32 `json:"x"`
	Y      float32 `json:"y"`
	Z      float32 `json:"z"`
	Moving bool    `json:"mv"` // a movement is active (with the moving capability)
	Target string  `json:"tg,omitempty"`
	Facing *int    `json:"fa,omitempty"` // degrees, 0-180, between where it faces and the direction to its hostile target (with a target in view)
	Cast   uint32  `json:"cs,omitempty"`
	Action string  `json:"act,omitempty"` // the last action its AI ran (feed)
	NoPos  bool    `json:"np,omitempty"`  // the position is not known (x, y, z are 0)
	Lead   bool    `json:"lead,omitempty"`
}

// Threat is one entry of a mob's threat list: a guid and its share of the top entry's threat. It is written as [guid, pct].
type Threat struct {
	G   string
	Pct int
}

// MarshalJSON writes [guid, pct].
func (t Threat) MarshalJSON() ([]byte, error) { return json.Marshal([]any{t.G, t.Pct}) }

// UnmarshalJSON reads [guid, pct].
func (t *Threat) UnmarshalJSON(b []byte) error {
	var raw []json.RawMessage
	if err := json.Unmarshal(b, &raw); err != nil || len(raw) != 2 {
		return fmt.Errorf("threat entry: want [guid, pct]")
	}
	if err := json.Unmarshal(raw[0], &t.G); err != nil {
		return err
	}
	var f float64
	if err := json.Unmarshal(raw[1], &f); err != nil {
		return err
	}
	t.Pct = int(f)
	return nil
}

// Mob is a hostile in a frame. Entry, Name and Level are for the registry (mobs records), not written in the frame.
type Mob struct {
	G      string   `json:"g"`
	HPPct  int      `json:"hp"`
	X      float32  `json:"x"`
	Y      float32  `json:"y"`
	Z      float32  `json:"z"`
	Combat bool     `json:"cb"`
	Target string   `json:"tg,omitempty"`
	Cast   uint32   `json:"cs,omitempty"`
	Evade  bool     `json:"ev,omitempty"`
	CC     bool     `json:"cc,omitempty"`
	Threat []Threat `json:"thr,omitempty"` // the top of the threat list, highest first (with the threat capability)
	Entry  uint32   `json:"-"`
	Name   string   `json:"-"`
	Level  int      `json:"-"`
}

// Frame is a sample of the world. Via is "session" (the agent's own object store and party list) or "feed" (a TBM:VIEW frame).
type Frame struct {
	Envelope
	Via  string `json:"via"`
	Seq  uint64 `json:"seq,omitempty"` // the feed's frame number
	Cut  bool   `json:"cut,omitempty"` // the source marked the frame as cut short
	Bots []Bot  `json:"bots"`
	Mobs []Mob  `json:"mobs"`
}

// Event is one thing that happened: Ev names it, D holds its fields, written flat beside ev. The server's events use a and b (guids),
// s and s2 (text); the agent's use those where they fit and add fields of their own. A field named like an envelope field is
// written with an x_ prefix. The agent's events: leg, step, leader, order, refusal, pull_start, pull_end, rest, walk_start, walk_end,
// death, wipe, combat_on, combat_off, gm_shortcut, note, feed_stall, roster, stuck, unstuck, brain, brain_state, unreachable, trace_limit.
type Event struct {
	Envelope
	Ev string
	D  map[string]any
}

var reserved = map[string]bool{"v": true, "k": true, "run": true, "src": true, "t": true, "ev": true}

// MarshalJSON writes the envelope, ev and then the fields in key order.
func (e Event) MarshalJSON() ([]byte, error) {
	var b bytes.Buffer
	head, err := json.Marshal(e.Envelope)
	if err != nil {
		return nil, err
	}
	b.Write(head[:len(head)-1])
	ev, _ := json.Marshal(e.Ev)
	b.WriteString(`,"ev":`)
	b.Write(ev)
	keys := make([]string, 0, len(e.D))
	for k := range e.D {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		name := k
		if reserved[k] {
			name = "x_" + k
		}
		kb, _ := json.Marshal(name)
		vb, err := json.Marshal(e.D[k])
		if err != nil {
			return nil, err
		}
		b.WriteByte(',')
		b.Write(kb)
		b.WriteByte(':')
		b.Write(vb)
	}
	b.WriteByte('}')
	return b.Bytes(), nil
}

// UnmarshalJSON reads the envelope and ev and keeps the rest in D.
func (e *Event) UnmarshalJSON(b []byte) error {
	var m map[string]any
	if err := json.Unmarshal(b, &m); err != nil {
		return err
	}
	if err := json.Unmarshal(b, &e.Envelope); err != nil {
		return err
	}
	e.Ev, _ = m["ev"].(string)
	e.D = map[string]any{}
	for k, v := range m {
		if !reserved[k] {
			e.D[k] = v
		}
	}
	return nil
}

// End closes the run. Reason is how it ended (done, failed, stopped); Counters has the writer's counts (frames, events, party, mobs,
// records, bytes, dropped, truncated) and the run's totals.
type End struct {
	Envelope
	Reason   string         `json:"reason"`
	Error    string         `json:"error,omitempty"`
	Seconds  float64        `json:"seconds"`
	Ended    time.Time      `json:"ended"`
	Counters map[string]any `json:"counters"`
}

// Counter reads a number out of the end record's counters (0 when absent).
func (e *End) Counter(name string) float64 {
	if e == nil {
		return 0
	}
	switch n := e.Counters[name].(type) {
	case float64:
		return n
	case int:
		return float64(n)
	case int64:
		return float64(n)
	}
	return 0
}

// Check is the verdict of one oracle: what was measured against what threshold, how many times it happened, and the times (ms) that
// show it. id, name, verdict, count, first_ms and detail are the server's fields; the rest are the agent's.
type Check struct {
	ID        string  `json:"id"`
	Name      string  `json:"name"`
	Verdict   string  `json:"verdict"`
	Count     int     `json:"count"`
	FirstMS   int64   `json:"first_ms,omitempty"`
	Detail    string  `json:"detail,omitempty"`
	Value     float64 `json:"value"`
	Threshold float64 `json:"threshold"`
	Unit      string  `json:"unit,omitempty"`
	Evidence  []int64 `json:"evidence,omitempty"`
}

// Oracle is a Check as a record of the trace.
type Oracle struct {
	Envelope
	Check
}
