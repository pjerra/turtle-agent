package eluna

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"sync/atomic"
	"time"

	"github.com/pjerra/turtle-agent/internal/events"
)

// DefaultTimeout is how long a one-shot answer is awaited when the client names no timeout.
const DefaultTimeout = 5 * time.Second

var (
	// ErrTimeout is no `TTJSON` answer to this request within the client's timeout.
	ErrTimeout = errors.New("eluna: no answer to the request in time")
	// ErrWrongUnit is a `q unit` reply about another unit than the one asked for (a stale or crossed answer).
	ErrWrongUnit = errors.New("eluna: the reply is about another unit")
)

// Log is the part of the agent's event log the client reads (*events.Log fits).
type Log interface {
	Last() uint64
	Read(events.Query) events.Page
	Wait(context.Context, uint64) error
}

// Client sends `.tt` lines and reads the replies out of the event log. Zero Timeout is DefaultTimeout; nil NewID numbers the
// requests from a process-wide counter.
type Client struct {
	// Send sends one command line as the agent, the leading "." included (session.GM takes it so).
	Send    func(ctx context.Context, line string) error
	Log     Log
	NewID   func() string
	Timeout time.Duration
	// Of names the player whose group the group commands act on (`of=<name>`). A commander bound with `.bot commander
	// bind` is not a member of the bots' group, so the toolkit run as the commander must be told the bots' lead bot;
	// the toolkit's `of=` takes that player's group instead of the caller's. Empty: the caller's own group.
	Of string
}

// Error is a reply with ok:false: the toolkit's error code and message.
type Error struct {
	Code  string
	Msg   string
	Reply Reply
}

func (e *Error) Error() string {
	s := "eluna: " + e.Code
	if e.Msg != "" {
		s += ": " + e.Msg
	}
	return s
}

// idSeq numbers the requests of every client of the process.
var idSeq atomic.Uint64

func defaultID() string { return "ta" + strconv.FormatUint(idSeq.Add(1), 36) }

// Do sends `.tt <sub> #<id>` and returns the replies that carry that id, up to the one that ends the command. A reply with another id
// (an old answer, another caller's) is dropped: only this request's replies count. extra widens the wait beyond Timeout (the second
// read of quiet and place). A refused request returns the replies so far and an *Error; no answer returns ErrTimeout.
func (c *Client) Do(ctx context.Context, sub string, extra time.Duration) ([]Reply, error) {
	if c == nil || c.Log == nil || c.Send == nil {
		return nil, errors.New("eluna: the client needs a log and a sender")
	}
	newID := c.NewID
	if newID == nil {
		newID = defaultID
	}
	id := newID()
	since := c.Log.Last()
	if of := c.ofArg(sub); of != "" {
		sub += of
	}
	if err := c.Send(ctx, ".tt "+sub+" #"+id); err != nil {
		return nil, err
	}
	timeout := c.Timeout
	if timeout <= 0 {
		timeout = DefaultTimeout
	}
	wctx, cancel := context.WithTimeout(ctx, timeout+extra)
	defer cancel()
	var out []Reply
	cursor := since
	for {
		p := c.Log.Read(events.Query{Since: cursor, Kinds: []string{events.KindSystem}, Limit: events.MaxLimit})
		for _, e := range p.Events {
			text := e.Text
			if raw, ok := e.Data["raw"].(string); ok && raw != "" {
				text = raw
			}
			r, ok := ParseLine(text)
			if !ok || r.ID != id {
				continue
			}
			out = append(out, r)
			if !r.OK {
				return out, &Error{Code: r.Err, Msg: r.Msg, Reply: r}
			}
			if r.Done() {
				return out, nil
			}
		}
		cursor = p.Next
		if p.More {
			continue
		}
		if err := c.Log.Wait(wctx, cursor); err != nil {
			if ctx.Err() != nil {
				return out, ctx.Err()
			}
			return out, ErrTimeout
		}
	}
}

// FloorResult is a `q floor` answer: the floor the caller's z searches (nil: none) and the map's top surface (nil when it has none).
type FloorResult struct {
	Floor *float32
	Top   *float32
	Multi bool
}

// Unit is a unit of a `q units` / `q quiet` / `q unit` reply: what the start path reads of it.
type Unit struct {
	GUID   string  `json:"g"`
	Entry  uint32  `json:"e"`
	Name   string  `json:"n"`
	Dist   float32 `json:"d"`  // 2D distance to the queried point
	Dz     float32 `json:"dz"` // height difference to the queried point
	HP     int     `json:"hp"` // percent
	Dead   bool    `json:"dead"`
	Combat bool    `json:"cb"`
}

// Roamer is a unit quiet saw moving or arriving.
type Roamer struct {
	GUID  string  `json:"g"`
	Entry uint32  `json:"e"`
	Why   string  `json:"why"` // moved or arrived
	Moved float32 `json:"moved"`
	Dist  float32 `json:"d"`
}

// QuietResult is the second read of `quiet`: whether the area stayed clear, what stands near, who roams.
type QuietResult struct {
	Quiet   bool
	Near    []Unit
	Roamers []Roamer
}

// Placed is one member of a `place` answer's second read.
type Placed struct {
	GUID   string    `json:"g"`
	Name   string    `json:"n"`
	Why    string    `json:"why"` // set when the member could not be found (gone)
	OK     bool      `json:"ok"`
	Pos    []float32 `json:"p"`
	Dz     float32   `json:"dz"`
	Dist   float32   `json:"d"`
	HP     int       `json:"hp"`
	Combat bool      `json:"cb"`
	Alive  bool      `json:"alive"`
}

// PlaceResult is the second read of `place`: whether the party stands on the floor it was put on.
type PlaceResult struct {
	Placed  bool
	Members []Placed
}

// PlaceDzTolerance is how far a placed member's z may be from the floor it was put on and still count as placed. The
// toolkit's own `placed` also requires each member within 5 yd of its ring spot, but the bots walk a few yards after the
// teleport while still on the floor, so distance alone is not a failed placement.
const PlaceDzTolerance = 1.5

// OnFloor reports whether every member of the placement stands on the floor it was put on: each one found and its |dz|
// within PlaceDzTolerance. Distance drift (`d`) is not judged here: a bot that stepped a few yards away is still placed.
func (p PlaceResult) OnFloor() bool {
	if len(p.Members) == 0 {
		return false
	}
	for _, m := range p.Members {
		if m.Why != "" || absF(m.Dz) > PlaceDzTolerance {
			return false
		}
	}
	return true
}

// ofArg is the `of=` argument Do appends to a group command when Of is set: " of=<name>", else "".
func (c *Client) ofArg(sub string) string {
	if c.Of == "" || !groupCommand(sub) {
		return ""
	}
	return " of=" + c.Of
}

// groupCommand reports whether a toolkit command acts on a group (TortoiseBots tools/eluna/test-toolkit, tt_commands.lua):
// place, reset and q group. Only those take `of=`; the player's name would be an error on any other command.
func groupCommand(sub string) bool {
	f := strings.Fields(sub)
	if len(f) == 0 {
		return false
	}
	switch f[0] {
	case "place", "reset":
		return true
	case "q":
		return len(f) > 1 && f[1] == "group"
	}
	return false
}

// absF is |v|.
func absF(v float32) float32 {
	if v < 0 {
		return -v
	}
	return v
}

// Floor asks the z-aware floor at a point.
func (c *Client) Floor(ctx context.Context, x, y, z float32) (FloorResult, error) {
	rs, err := c.Do(ctx, "q floor "+f2(x)+" "+f2(y)+" "+f2(z), 0)
	if err != nil {
		return FloorResult{}, err
	}
	r, err := final(rs)
	if err != nil {
		return FloorResult{}, err
	}
	var out FloorResult
	if err := decodeInto(r.Body, "floor", &out.Floor); err != nil {
		return FloorResult{}, err
	}
	if err := decodeInto(r.Body, "top", &out.Top); err != nil {
		return FloorResult{}, err
	}
	out.Multi = fieldBool(r.Body, "multi")
	return out, nil
}

// QuietOpts narrow what `quiet` counts (the toolkit's dz= and skip=). The zero value sends neither, so the toolkit's own defaults
// apply (dz 8 yd, no skipped entries).
type QuietOpts struct {
	// Dz is the vertical window in yards: units farther above or below the point do not count. Nil: the toolkit's default (8);
	// a pointer to 0 sends dz=0, which means no vertical limit (the old 2D rule).
	Dz *float32
	// Skip lists entries that never count (e.g. a friendly NPC the test needs near the point). At most MaxSkip are sent.
	Skip []uint32
}

// MaxSkip is the most entries the toolkit reads from one skip= list (its idList cap).
const MaxSkip = 16

// args is the " dz=.. skip=.." tail of a quiet line.
func (o QuietOpts) args() string {
	var s string
	if o.Dz != nil {
		s += " dz=" + strconv.FormatFloat(float64(*o.Dz), 'f', -1, 32)
	}
	if n := len(o.Skip); n > 0 {
		if n > MaxSkip {
			n = MaxSkip
		}
		ids := make([]string, n)
		for i := 0; i < n; i++ {
			ids[i] = strconv.FormatUint(uint64(o.Skip[i]), 10)
		}
		s += " skip=" + strings.Join(ids, ",")
	}
	return s
}

// Quiet holds (or asks) whether a radius around a point stays clear for wait, and answers after it: the second read of the command.
func (c *Client) Quiet(ctx context.Context, x, y, z, radius float32, wait time.Duration) (QuietResult, error) {
	return c.QuietWith(ctx, x, y, z, radius, wait, QuietOpts{})
}

// QuietWith is Quiet with the toolkit's vertical window and entry filter.
func (c *Client) QuietWith(ctx context.Context, x, y, z, radius float32, wait time.Duration, o QuietOpts) (QuietResult, error) {
	if wait < 0 {
		wait = 0
	}
	sub := "quiet " + f2(radius) + " " + f2(x) + " " + f2(y) + " " + f2(z) + " w=" + strconv.FormatFloat(wait.Seconds(), 'f', -1, 64) + o.args()
	rs, err := c.Do(ctx, sub, wait+10*time.Second)
	if err != nil {
		return QuietResult{}, err
	}
	r, err := final(rs)
	if err != nil {
		return QuietResult{}, err
	}
	out := QuietResult{Quiet: fieldBool(r.Body, "quiet")}
	if err := decodeInto(r.Body, "near", &out.Near); err != nil {
		return QuietResult{}, err
	}
	if err := decodeInto(r.Body, "roamers", &out.Roamers); err != nil {
		return QuietResult{}, err
	}
	return out, nil
}

// Place puts the party in a ring of spread yards around a point and answers after the verification: the second read of the command.
func (c *Client) Place(ctx context.Context, x, y, z, spread float32) (PlaceResult, error) {
	sub := "place " + f2(x) + " " + f2(y) + " " + f2(z) + " spread=" + strconv.FormatFloat(float64(spread), 'f', -1, 32)
	rs, err := c.Do(ctx, sub, 10*time.Second)
	if err != nil {
		return PlaceResult{}, err
	}
	r, err := final(rs)
	if err != nil {
		return PlaceResult{}, err
	}
	out := PlaceResult{Placed: fieldBool(r.Body, "placed")}
	if err := decodeInto(r.Body, "members", &out.Members); err != nil {
		return PlaceResult{}, err
	}
	return out, nil
}

// Unit reads one unit by guid, with the auras of the given spells looked up. A reply about another guid is ErrWrongUnit.
func (c *Client) Unit(ctx context.Context, g uint64, auras []uint32) (Unit, error) {
	sub := "q unit " + HexGUID(g)
	if len(auras) > 0 {
		ids := make([]string, len(auras))
		for i, a := range auras {
			ids[i] = strconv.FormatUint(uint64(a), 10)
		}
		sub += " au=" + strings.Join(ids, ",")
	}
	rs, err := c.Do(ctx, sub, 0)
	if err != nil {
		return Unit{}, err
	}
	r, err := final(rs)
	if err != nil {
		return Unit{}, err
	}
	var out Unit
	if err := decodeInto(r.Body, "unit", &out); err != nil {
		return Unit{}, err
	}
	got, err := ParseHexGUID(out.GUID)
	if err != nil || got != g {
		return Unit{}, ErrWrongUnit
	}
	return out, nil
}

// final is the reply that ended the command (the last one: the second read for quiet and place).
func final(rs []Reply) (Reply, error) {
	if len(rs) == 0 {
		return Reply{}, ErrTimeout
	}
	return rs[len(rs)-1], nil
}

// decodeInto decodes one field of a reply body into v; an absent field leaves v alone.
func decodeInto(b map[string]json.RawMessage, key string, v any) error {
	raw, ok := b[key]
	if !ok {
		return nil
	}
	if err := json.Unmarshal(raw, v); err != nil {
		return fmt.Errorf("eluna: reply field %q: %w", key, err)
	}
	return nil
}

// f2 formats a coordinate the way the toolkit reads it.
func f2(v float32) string { return strconv.FormatFloat(float64(v), 'f', 2, 32) }
