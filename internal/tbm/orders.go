package tbm

import (
	"context"
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/pjerra/turtle-agent/internal/events"
	"github.com/pjerra/turtle-agent/internal/packet"
)

// The per-bot player orders of the remote commander (docs/guides/player-controls.md section 1a of TortoiseBots, "Player orders"):
// `.bot cmd loot <guid> <bot> [itemId]`, `cmd open <guid> <bot>`, `cmd use <itemId> <bot> [target-guid]` and `cmd lootmethod`, plus the read-only `cmd state <guid|entry:<id>[@x,y,z]>`; loot, open and use also take `entry:<id>[@x,y,z]` for a GUID. They
// are asynchronous: a refusal is one ERR line; an accepted order that has to walk answers TBM:ACTION_PENDING first and its final ACK
// or ERR later. Order sends one and waits for the final line.

// PendingPrefix starts the line of an order that is running.
const PendingPrefix = "TBM:ACTION_PENDING|"

// Pending is a TBM:ACTION_PENDING|cmd <verb>|bot:<Name>|<budget seconds>|<object guid or 0> line: the order is accepted and runs; the
// final ACK or ERR of the same intent and bot follows within the budget.
type Pending struct {
	Intent  string `json:"intent"`
	Bot     string `json:"bot"`
	BudgetS int    `json:"budget_s"`
	GUID    string `json:"guid"`
}

// ParsePending reads an ACTION_PENDING line.
func ParsePending(line string) (Pending, bool) {
	rest, ok := strings.CutPrefix(line, PendingPrefix)
	if !ok {
		return Pending{}, false
	}
	f := strings.Split(rest, "|")
	if len(f) != 4 || strings.TrimSpace(f[0]) == "" || !strings.HasPrefix(f[1], "bot:") || len(f[1]) == 4 {
		return Pending{}, false
	}
	n, err := strconv.Atoi(f[2])
	if err != nil || n < 0 {
		return Pending{}, false
	}
	return Pending{Intent: f[0], Bot: f[1][4:], BudgetS: n, GUID: f[3]}, true
}

// TargetPrefix starts the line that names the object an order by entry chose.
const TargetPrefix = "TBM:ACTION_TARGET|"

// Target is a TBM:ACTION_TARGET|cmd <verb>|bot:<Name>|<guid decimal>|<entry> line: the object that an `entry:<id>[@x,y,z]` order picked
// (after the range checks, before ACTION_PENDING and the final answer), so the client can log it and use the GUID later.
type Target struct {
	Intent string      `json:"intent"`
	Bot    string      `json:"bot"`
	GUID   packet.GUID `json:"guid"`
	Entry  uint32      `json:"entry"`
}

// ParseTarget reads an ACTION_TARGET line: four fields after the prefix, a bot: scope, and a decimal GUID and entry.
func ParseTarget(line string) (Target, bool) {
	rest, ok := strings.CutPrefix(line, TargetPrefix)
	if !ok {
		return Target{}, false
	}
	f := strings.Split(rest, "|")
	if len(f) != 4 || strings.TrimSpace(f[0]) == "" || !strings.HasPrefix(f[1], "bot:") || len(f[1]) == 4 {
		return Target{}, false
	}
	g, err := strconv.ParseUint(f[2], 10, 64)
	if err != nil {
		return Target{}, false
	}
	e, err := strconv.ParseUint(f[3], 10, 32)
	if err != nil {
		return Target{}, false
	}
	return Target{Intent: f[0], Bot: f[1][4:], GUID: packet.GUID(g), Entry: uint32(e)}, true
}

// GOState is what `cmd state` answers: guid:<dec>,entry:<n>,type:<n>,state:<active|ready|alternative>,loot:<...>,flags:<...>.
type GOState struct {
	GUID  packet.GUID `json:"guid"`
	Entry uint32      `json:"entry"`
	Type  uint32      `json:"type"`
	State string      `json:"state"` // active (a door open, a lever pulled), ready (at rest) or alternative
	Loot  string      `json:"loot,omitempty"`
	Flags string      `json:"flags,omitempty"`
}

// ParseGOState reads the payload of a `cmd state` ACK. ok is false without a numeric guid and entry and a state.
func ParseGOState(payload string) (GOState, bool) {
	var s GOState
	var haveGUID, haveEntry bool
	for _, part := range strings.Split(strings.TrimSpace(payload), ",") {
		k, v, found := strings.Cut(part, ":")
		if !found {
			return GOState{}, false
		}
		switch strings.ToLower(k) {
		case "guid":
			n, err := strconv.ParseUint(v, 10, 64)
			if err != nil {
				return GOState{}, false
			}
			s.GUID, haveGUID = packet.GUID(n), true
		case "entry":
			n, err := strconv.ParseUint(v, 10, 32)
			if err != nil {
				return GOState{}, false
			}
			s.Entry, haveEntry = uint32(n), true
		case "type":
			n, err := strconv.ParseUint(v, 10, 32)
			if err != nil {
				return GOState{}, false
			}
			s.Type = uint32(n)
		case "state":
			s.State = strings.ToLower(v)
		case "loot":
			s.Loot = v
		case "flags":
			s.Flags = v
		}
	}
	return s, haveGUID && haveEntry && s.State != ""
}

// ItemCount is one entry of a loot payload: an item and how many came.
type ItemCount struct {
	Entry uint32 `json:"entry"`
	Count int    `json:"count"`
}

// Loot is what a loot or chest ACK carries: `<itemId:count,...>[,copper:<n>]`, or `-` for nothing.
type Loot struct {
	Items  []ItemCount `json:"items"`
	Copper uint64      `json:"copper,omitempty"`
}

// Has says whether the loot holds the item.
func (l Loot) Has(entry uint32) bool {
	for _, it := range l.Items {
		if it.Entry == entry && it.Count > 0 {
			return true
		}
	}
	return false
}

// ParseLoot reads a loot payload. ok is false when it is neither `-` nor a list of itemId:count and copper:n entries.
func ParseLoot(payload string) (Loot, bool) {
	l := Loot{Items: []ItemCount{}}
	payload = strings.TrimSpace(payload)
	if payload == "-" {
		return l, true
	}
	for _, part := range strings.Split(payload, ",") {
		k, v, found := strings.Cut(strings.TrimSpace(part), ":")
		if !found {
			return Loot{}, false
		}
		n, err := strconv.ParseUint(v, 10, 64)
		if err != nil {
			return Loot{}, false
		}
		if strings.EqualFold(k, "copper") {
			l.Copper += n
			continue
		}
		id, err := strconv.ParseUint(k, 10, 32)
		if err != nil {
			return Loot{}, false
		}
		l.Items = append(l.Items, ItemCount{Entry: uint32(id), Count: int(n)})
	}
	return l, true
}

// OrderResult is the typed outcome of one player order.
type OrderResult struct {
	Command string `json:"command"` // the line sent, without ".bot "
	Intent  string `json:"intent"`  // "cmd loot"
	Bot     string `json:"bot,omitempty"`
	// Kind is KindAck, KindErr or KindTimeout (no final line within the wait).
	Kind    string   `json:"kind"`
	OK      bool     `json:"ok"`
	Pending *Pending `json:"pending,omitempty"` // set when the order reported it was running
	Ack     *Ack     `json:"ack,omitempty"`
	Err     *Err     `json:"err,omitempty"`
	// The ACK payload, by verb: Loot for loot (and for open on a chest), Opened for open on anything else, Spell for use (0: none),
	// Method and Threshold for lootmethod.
	Loot      *Loot  `json:"loot,omitempty"`
	Opened    bool   `json:"opened,omitempty"`
	Spell     uint32 `json:"spell,omitempty"`
	Method    string `json:"method,omitempty"`
	Threshold string `json:"threshold,omitempty"`
	// Target is the object an order by entry chose (the ACTION_TARGET line); nil for an order by GUID or one refused before the choice.
	Target *Target `json:"target,omitempty"`
	// State is the answer of `cmd state`.
	State *GOState `json:"state,omitempty"`
	// Waited is how long the order took, in real time.
	Waited time.Duration `json:"waited_ns"`
	// Final is the ACK or ERR line that ended the order ("" on a timeout).
	Final string   `json:"final,omitempty"`
	Lines []string `json:"lines"`
}

// Code is the ERR code, "timeout" for no answer, "" for an ACK.
func (r OrderResult) Code() string {
	switch {
	case r.Err != nil:
		return r.Err.Code
	case r.Kind == KindTimeout:
		return "timeout"
	}
	return ""
}

// Describe is one short line for logs and messages: the ERR code and message, "no answer", or the ACK payload.
func (r OrderResult) Describe() string {
	switch {
	case r.Err != nil:
		return r.Err.Code + " (" + r.Err.Message + ")"
	case r.Kind == KindTimeout && r.Pending != nil:
		return fmt.Sprintf("no final answer %d s after it reported running", r.Pending.BudgetS)
	case r.Kind == KindTimeout:
		return "no answer"
	case r.Ack != nil:
		return r.Ack.Executor
	}
	return ""
}

// DefaultPendingSlack is the time added to an order's PENDING budget before the wait gives up.
const DefaultPendingSlack = 5 * time.Second

// MaxPendingWait caps the wait that an ACTION_PENDING line can ask for: the budget is the server's word, and a pending order that
// claims an hour must not hold a run for an hour (the run watchdog cannot see into a blocked order).
const MaxPendingWait = 90 * time.Second

// pendingWait is how long to wait for the final answer of an order that reported a budget of budgetS seconds.
func pendingWait(budgetS int, slack time.Duration) time.Duration {
	return min(time.Duration(max(budgetS, 0))*time.Second+slack, MaxPendingWait)
}

// orderBot is the bot an order names: loot <guid> <bot>, open <guid> <bot>, use <item> <bot>.
func orderBot(command string) string {
	f := strings.Fields(command)
	if len(f) >= 4 && strings.EqualFold(f[0], "cmd") {
		switch strings.ToLower(f[1]) {
		case "loot", "open", "use":
			return f[3]
		}
	}
	return ""
}

// Order sends a `cmd` command and waits for its final ACK or ERR. The first line has Wait (DefaultOrderWait) to come; an
// ACTION_PENDING line extends the wait to its budget plus PendingSlack. A final line for another bot is passed over. The payload of an
// ACK is parsed by verb into the result. The error is for a command that could not be sent; an order that never answers is Kind
// timeout.
func (c *Commander) Order(ctx context.Context, command string) (OrderResult, error) {
	return c.OrderWait(ctx, command, c.Wait)
}

// OrderWait is Order with its own wait for the first line (DefaultOrderWait when 0).
func (c *Commander) OrderWait(ctx context.Context, command string, wait time.Duration) (OrderResult, error) {
	command = strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(command), ".bot "))
	res := OrderResult{Command: command, Intent: Intent(command), Bot: orderBot(command), Lines: []string{}}
	if err := CheckCommand(command); err != nil {
		res.Kind = KindErr
		return res, err
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	log := c.link.Log()
	since := log.Last()
	start := time.Now()
	if err := c.link.Say(ctx, Chat(command)); err != nil {
		return res, err
	}
	if wait <= 0 {
		wait = DefaultOrderWait
	}
	slack := c.PendingSlack
	if slack <= 0 {
		slack = DefaultPendingSlack
	}
	deadline := start.Add(wait)
	cursor := since
	for {
		p := log.Read(events.Query{Since: cursor, Kinds: []string{events.KindSystem, events.KindAddon}, Limit: events.MaxLimit})
		for _, e := range p.Events {
			line, ok := LineOf(e)
			if !ok || strings.HasPrefix(line, ViewPrefix) {
				continue
			}
			if len(res.Lines) < 60 {
				res.Lines = append(res.Lines, line)
			}
			if tg, ok := ParseTarget(line); ok && strings.EqualFold(tg.Intent, res.Intent) && (res.Bot == "" || strings.EqualFold(tg.Bot, res.Bot)) {
				res.Target = &tg
				continue
			}
			if pd, ok := ParsePending(line); ok && strings.EqualFold(pd.Intent, res.Intent) && (res.Bot == "" || strings.EqualFold(pd.Bot, res.Bot)) {
				res.Pending = &pd
				deadline = time.Now().Add(pendingWait(pd.BudgetS, slack))
				continue
			}
			if a, ok := ParseAck(line); ok && strings.EqualFold(a.Intent, res.Intent) {
				if res.Bot != "" && strings.HasPrefix(a.Scope, "bot:") && !strings.EqualFold(a.Scope[4:], res.Bot) {
					continue
				}
				res.Kind, res.OK, res.Ack, res.Final = KindAck, true, &a, line
				res.Waited = time.Since(start)
				interpretAck(&res, a)
				return res, nil
			}
			if er, ok := ParseErr(line); ok && strings.EqualFold(er.Intent, res.Intent) {
				res.Kind, res.Err, res.Final = KindErr, &er, line
				res.Waited = time.Since(start)
				return res, nil
			}
		}
		cursor = p.Next
		if p.More {
			continue
		}
		wctx, cancel := context.WithDeadline(ctx, deadline)
		err := log.Wait(wctx, cursor)
		cancel()
		if err != nil {
			if ctx.Err() != nil {
				res.Kind, res.Waited = KindTimeout, time.Since(start)
				return res, ctx.Err()
			}
			res.Kind, res.Waited = KindTimeout, time.Since(start)
			return res, nil
		}
	}
}

// interpretAck reads the ACK payload of the order's verb.
func interpretAck(r *OrderResult, a Ack) {
	f := strings.Fields(r.Intent)
	if len(f) < 2 {
		return
	}
	switch f[1] {
	case "loot":
		if l, ok := ParseLoot(a.Executor); ok {
			r.Loot = &l
		}
	case "open":
		if strings.EqualFold(a.Executor, "opened") {
			r.Opened = true
		} else if l, ok := ParseLoot(a.Executor); ok {
			r.Loot = &l // a chest answers like loot
		}
	case "use":
		if id, ok := strings.CutPrefix(a.Executor, "spell:"); ok {
			if n, err := strconv.ParseUint(id, 10, 32); err == nil {
				r.Spell = uint32(n)
			}
		}
	case "lootmethod":
		r.Method, r.Threshold, _ = strings.Cut(a.Executor, ":")
	case "state":
		if st, ok := ParseGOState(a.Executor); ok {
			r.State = &st
		}
	}
}

// Behavior sends `.bot behavior <bot> <key> on|off` and waits for its ACK or ERR (intent "behavior <key>").
func (c *Commander) Behavior(ctx context.Context, bot, key string, on bool) (Reply, error) {
	state := "off"
	if on {
		state = "on"
	}
	if !botNameRe.MatchString(bot) || !regexp.MustCompile(`^[a-z]{2,12}$`).MatchString(key) {
		return Reply{Kind: KindErr, Lines: []string{}}, fmt.Errorf("behavior: %q %q: a bot name and a lower-case key", bot, key)
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	log := c.link.Log()
	since := log.Last()
	if err := c.link.Say(ctx, Chat("behavior "+bot+" "+key+" "+state)); err != nil {
		return Reply{Lines: []string{}}, err
	}
	wait := c.Wait
	if wait <= 0 {
		wait = DefaultOrderWait
	}
	return ReadReply(ctx, log, since, "behavior "+key, wait), nil
}
