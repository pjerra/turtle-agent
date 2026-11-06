package main

import (
	"context"
	"fmt"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/pjerra/turtle-agent/internal/events"
	"github.com/pjerra/turtle-agent/internal/rpc"
	"github.com/pjerra/turtle-agent/internal/session"
	"github.com/pjerra/turtle-agent/internal/tbm"
)

type clickParams struct {
	Button    string         `json:"button"`
	Args      map[string]any `json:"args,omitempty"`
	Transport string         `json:"transport,omitempty"` // auto (default), chat or addon
	WaitMS    int            `json:"wait_ms,omitempty"`   // how long to wait for the reply; default 8000, the addon's ACTION_TIMEOUT
	Force     bool           `json:"force,omitempty"`     // send even where the addon greys the button out
	FollowUp  *bool          `json:"follow_up,omitempty"` // poll the roster after the click as the addon does; default per button
}

type sendParams struct {
	Command   string `json:"command"`
	Transport string `json:"transport,omitempty"`
	WaitMS    int    `json:"wait_ms,omitempty"`
}

type buttonsParams struct {
	Tab string `json:"tab,omitempty"`
}

type buttonsResult struct {
	AddonCommit     string       `json:"addon_commit"`
	SendDelayMS     int          `json:"send_delay_ms"`
	PollAfterCmdMS  int          `json:"poll_after_cmd_ms"`
	ActionTimeoutMS int          `json:"action_timeout_ms"`
	Buttons         []tbm.Button `json:"buttons"`
}

// sentInfo is exactly what went on the wire: the .bot chat line, or the addon message.
type sentInfo struct {
	Transport string `json:"transport"`
	Text      string `json:"text"`
	Channel   string `json:"channel,omitempty"` // addon transport: the addon channel
	Prefix    string `json:"prefix,omitempty"`  // addon transport: the addon message prefix
}

type clickResult struct {
	Since     uint64      `json:"since"` // the log cursor before the first send
	Next      uint64      `json:"next"`  // the cursor after the last event read
	Button    string      `json:"button,omitempty"`
	Scope     string      `json:"scope,omitempty"` // party, or bot:<name> while an owned bot is targeted
	Sent      sentInfo    `json:"sent"`
	Why       string      `json:"transport_reason"`
	Refreshed bool        `json:"refreshed,omitempty"` // a roster poll ran first, as the addon's poll would have
	Reply     tbm.Reply   `json:"reply"`
	FollowUp  *tbm.Status `json:"follow_up,omitempty"`
}

// tbmClient clicks addon buttons for one agent. Its tracker holds the transport verdict and
// the module's last roster response between calls, as the addon does.
type tbmClient struct {
	cur func() *session.Session
	tr  *tbm.Tracker
	mu  sync.Mutex // one click or send at a time
}

func registerTBMClick(srv *rpc.Server, cur func() *session.Session, tr *tbm.Tracker) {
	c := &tbmClient{cur: cur, tr: tr}
	srv.Register("tbm_buttons", rpc.Typed(func(ctx context.Context, p buttonsParams) (buttonsResult, error) {
		res := buttonsResult{AddonCommit: tbm.AddonCommit, SendDelayMS: tbm.SendDelayMS, PollAfterCmdMS: tbm.PollAfterCmdMS,
			ActionTimeoutMS: tbm.ActionTimeoutMS, Buttons: []tbm.Button{}}
		for _, b := range tbm.Buttons {
			if p.Tab == "" || strings.EqualFold(b.Tab, p.Tab) {
				res.Buttons = append(res.Buttons, b)
			}
		}
		return res, nil
	}))
	srv.Register("tbm_click", rpc.Typed(func(ctx context.Context, p clickParams) (clickResult, error) {
		res, err := c.click(ctx, p)
		return res, rpcErr(err)
	}))
	srv.Register("tbm_send", rpc.Typed(func(ctx context.Context, p sendParams) (clickResult, error) {
		res, err := c.sendRaw(ctx, p)
		return res, rpcErr(err)
	}))
}

// groupKey is the player's group as the transport verdict sees it.
func groupKey(s *session.Session) string {
	p := s.Party()
	guids := make([]uint64, len(p.Members))
	for i, m := range p.Members {
		guids[i] = uint64(m.GUID)
	}
	return tbm.GroupKey(p.InGroup, p.Raid, guids)
}

// argStrings turns the JSON arguments into the strings the command templates take.
func argStrings(in map[string]any) (map[string]string, error) {
	out := make(map[string]string, len(in))
	for k, v := range in {
		switch x := v.(type) {
		case string:
			out[k] = x
		case float64:
			out[k] = strconv.FormatFloat(x, 'f', -1, 64)
		case bool:
			out[k] = strconv.FormatBool(x)
		case nil:
		default:
			return nil, fmt.Errorf("argument %q: use a string or a number", k)
		}
	}
	return out, nil
}

// prepare chooses the transport (the addon's rule, unless the caller forces one) and runs the
// roster poll the addon would have made by now: a grouped player without a verdict for the
// current group, or a click that reads bot states, polls first. The addon's own poll respects
// its 5 s roster throttle (PollList), so a poll is skipped when the last one is younger.
func (c *tbmClient) prepare(ctx context.Context, s *session.Session, want, key string, refresh bool) (transport, why string, polled bool, err error) {
	forced := ""
	switch want {
	case "", "auto":
	case tbm.TransportChat, tbm.TransportAddon:
		forced = want
	default:
		return "", "", false, fmt.Errorf("transport %q: use auto, chat or addon", want)
	}
	pick := func() (string, string) {
		if forced != "" {
			return forced, "forced by the caller"
		}
		return c.tr.Pick(key)
	}
	need := refresh || (forced == "" && key != "" && !c.tr.VerdictCurrent(key))
	throttled := false
	if need {
		if c.tr.RosterDue(time.Now()) {
			t, _ := pick()
			if err := c.pollRoster(ctx, s, t, key, 3*time.Second); err != nil {
				return "", "", false, err
			}
			polled = true
		} else {
			throttled = true
		}
	}
	transport, why = pick()
	if throttled {
		why += " (no roster poll: the last one is under the addon's 5 s throttle)"
	}
	return transport, why, polled, nil
}

// pollRoster sends "roster" on transport and folds the response, up to its trailer, into the
// tracker.
func (c *tbmClient) pollRoster(ctx context.Context, s *session.Session, transport, key string, wait time.Duration) error {
	var st tbm.Status
	if err := c.sendSlot(ctx); err != nil {
		return err
	}
	_, err := c.exchange(ctx, s, transport, "roster", key, s.Log().Last(), wait, func(line string) bool {
		return strings.HasPrefix(line, "TBM:") && st.Add(line)
	}, nil)
	return err
}

// exchanged are the log cursors around one request and when it left.
type exchanged struct {
	since, next uint64
	sentAt      time.Time
}

// sendSlot waits out the addon's send throttle: SEND_DELAY between requests, whichever
// transport they take. The caller holds c.mu.
func (c *tbmClient) sendSlot(ctx context.Context) error {
	if d := c.tr.Throttle(time.Now()); d > 0 {
		select {
		case <-time.After(d):
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	return nil
}

// exchange sends command on transport and feeds the lines that come back on that transport to
// feed until it returns true or wait runs out; with quiet, a quiet gap of that length also ends
// it. Every TBM: line also goes into the tracker. It reads from the log cursor since, which lets
// a request see a late reply that arrived before it was sent; the caller has waited out the
// send throttle and holds c.mu.
func (c *tbmClient) exchange(ctx context.Context, s *session.Session, transport, command, key string, since uint64, wait time.Duration,
	feed func(line string) bool, quiet func() time.Duration) (exchanged, error) {
	log := s.Log()
	x := exchanged{since: since}
	x.next = x.since
	if verb, _, _ := strings.Cut(command, " "); verb == "roster" || verb == "list" {
		c.tr.NoteRoster(time.Now())
	}
	if err := sendTBM(ctx, s, transport, command); err != nil {
		return x, err
	}
	x.sentAt = time.Now()
	x.next = c.await(ctx, log, x.since, wait, transport, key, feed, quiet)
	c.tr.SetCursor(x.next)
	return x, nil
}

// drain takes in what arrived since the last exchange ended: late lines of earlier requests.
// TBM: lines update the tracker, and an ACK/ERR owed to a timed-out request is crossed off, so
// the reply to the next request is not mistaken for it. The caller holds c.mu.
func (c *tbmClient) drain(s *session.Session, key string) {
	log := s.Log()
	cursor := c.tr.Cursor()
	if cursor == 0 { // a new session: nothing before now is owed
		c.tr.SetCursor(log.Last())
		return
	}
	for {
		p := log.Read(events.Query{Since: cursor, Kinds: []string{events.KindSystem, events.KindAddon}, Limit: events.MaxLimit})
		for _, e := range p.Events {
			for _, t := range []string{tbm.TransportChat, tbm.TransportAddon} {
				line, ok := tbm.Carried(e, t)
				if !ok || !strings.HasPrefix(line, "TBM:") {
					continue
				}
				c.tr.Fold(line, key, time.Now())
				if a, ok := tbm.ParseAck(line); ok {
					c.tr.Settle(a.Intent, 1)
				} else if er, ok := tbm.ParseErr(line); ok {
					c.tr.Settle(er.Intent, 1)
				}
			}
		}
		cursor = p.Next
		if !p.More {
			break
		}
	}
	c.tr.SetCursor(cursor)
}

// await reads the lines transport carried since the cursor, folds the TBM: ones into the
// tracker and calls feed with each until it returns true, or wait runs out. It returns the
// cursor after the last event read.
func (c *tbmClient) await(ctx context.Context, log *events.Log, since uint64, wait time.Duration, transport, key string,
	feed func(line string) bool, quiet func() time.Duration) uint64 {
	wctx, cancel := context.WithTimeout(ctx, wait)
	defer cancel()
	cursor := since
	for {
		p := log.Read(events.Query{Since: cursor, Kinds: []string{events.KindSystem, events.KindAddon}, Limit: events.MaxLimit})
		for _, e := range p.Events {
			line, ok := tbm.Carried(e, transport)
			if !ok {
				continue
			}
			c.tr.Fold(line, key, time.Now())
			if feed(line) {
				return e.Seq
			}
		}
		cursor = p.Next
		if p.More {
			continue
		}
		waitCtx, stop := wctx, func() {}
		if quiet != nil {
			if q := quiet(); q > 0 {
				waitCtx, stop = context.WithTimeout(wctx, q)
			}
		}
		err := log.Wait(waitCtx, cursor)
		stop()
		if err != nil {
			return cursor
		}
	}
}

func sentFor(transport, command string) sentInfo {
	if transport == tbm.TransportAddon {
		return sentInfo{Transport: transport, Text: command, Channel: "PARTY", Prefix: tbm.Prefix}
	}
	return sentInfo{Transport: transport, Text: tbm.Chat(command)}
}

// targetInfo is what the addon looks at for scope: the targeted unit, whether it is one of
// the player's online bots, and whether it is a live enemy (UI.lua targetScope,
// hasValidEnemyTarget).
type targetInfo struct {
	exists, player, dead bool
	name                 string
}

func (c *tbmClient) target(s *session.Session) targetInfo {
	g := s.Status().Target
	if g == 0 {
		return targetInfo{}
	}
	u, err := s.Unit(g, "")
	if err != nil { // targeted but not in view: the addon can still see it exists
		return targetInfo{exists: true}
	}
	return targetInfo{exists: true, player: u.Type == "player", dead: u.Dead, name: u.Name}
}

// clickBeforeDrain is a test seam: it runs inside a click right after its cursor read and before its drain, the window
// where a late ACK of an earlier request can land.
var clickBeforeDrain func()

func (c *tbmClient) click(ctx context.Context, p clickParams) (clickResult, error) {
	btn, err := tbm.Find(p.Button)
	if err != nil {
		return clickResult{}, err
	}
	args, err := argStrings(p.Args)
	if err != nil {
		return clickResult{}, err
	}
	// One request at a time: replies carry no request id, so they are matched by intent and a
	// second request in flight could be answered by the first one's reply.
	c.mu.Lock()
	defer c.mu.Unlock()
	s := c.cur()
	c.tr.Reset(s)
	wait := waitBound(p.WaitMS, tbm.ActionTimeoutMS)
	key := groupKey(s)
	// Where the drain stopped: an ACK that lands while prepare runs (its roster exchange, the send throttle) answers an
	// earlier request, and this click owes it, so the exchange below must read it from here to pass over it instead of
	// its own reply. Anything already in the log belongs to the earlier requests and the drain has settled it; reading
	// from before the drain fed those lines to this click's matcher again (a late ACK counted as this click's reply).
	if clickBeforeDrain != nil {
		clickBeforeDrain()
	}
	c.drain(s, key)
	since := c.tr.Cursor()
	tgt := c.target(s)

	// What the addon would know before a click: a fresh roster for bot names, switch states and
	// capabilities, and the transport verdict for the group.
	refresh := (btn.Toggle != "" && c.tr.Age(time.Now()) > tbm.PanelPollMS*time.Millisecond) ||
		((tgt.exists && tgt.player || btn.RequiresCap != "") && !c.tr.HasRoster())
	transport, why, polled, err := c.prepare(ctx, s, p.Transport, key, refresh)
	if err != nil {
		return clickResult{}, err
	}
	if btn.RequiresCap != "" && !c.tr.HasCap(btn.RequiresCap) && !p.Force {
		return clickResult{}, fmt.Errorf("%s: the addon greys this button out until the module advertises the %q capability in TBM:CAPS (force:true sends it anyway)", btn.Name, btn.RequiresCap)
	}

	targetBot := ""
	if tgt.player && c.tr.IsBot(tgt.name) {
		targetBot = tgt.name
	}
	cmd, err := tbm.Build(btn, tbm.Context{Args: args, TargetBot: targetBot, BotStates: c.tr.BotStates(), HasPull: c.tr.HasCap("pull-seconds")})
	if err != nil {
		return clickResult{}, err
	}
	enemy := tgt.exists && !tgt.dead && targetBot == ""
	if btn.NeedsEnemy && !enemy && !p.Force {
		return clickResult{}, fmt.Errorf("%s: the addon greys this button out without a live enemy target (force:true sends it anyway)", btn.Name)
	}
	m, err := tbm.NewMatcher(cmd)
	if err != nil {
		return clickResult{}, err
	}
	if btn.Reply == tbm.ReplyAck {
		m.Skip(c.tr.Owed(cmd.Intent, time.Now()))
	}

	res := clickResult{Button: btn.Name, Why: why, Refreshed: polled, Sent: sentFor(transport, cmd.Text), Scope: scopeOf(btn, cmd, targetBot)}
	if err := c.sendSlot(ctx); err != nil {
		res.Since, res.Next = since, since
		return res, err
	}
	x, err := c.exchange(ctx, s, transport, cmd.Text, key, since, wait, m.Feed, m.Quiet)
	res.Since, res.Next = x.since, x.next
	if err != nil {
		return res, err
	}
	res.Reply = m.Reply()
	if k := m.Skipped(); k > 0 {
		c.tr.Settle(cmd.Intent, k)
	}
	if res.Reply.Kind == tbm.KindTimeout && btn.Reply == tbm.ReplyAck {
		c.tr.AddOrphan(cmd.Intent, time.Now())
	}
	if btn.Toggle != "" && res.Reply.Kind == tbm.KindAck {
		c.applyToggle(btn, res.Reply.Ack)
	}

	follow := btn.FollowUp
	if p.FollowUp != nil {
		follow = *p.FollowUp
	}
	if follow && res.Reply.Kind != tbm.KindErr {
		// The addon polls the roster POLL_AFTER_CMD after the command, forced (Comms.lua
		// OnCommandSent, Core.lua RequestPollSoon).
		if d := tbm.PollAfterCmdMS*time.Millisecond - time.Since(x.sentAt); d > 0 {
			select {
			case <-time.After(d):
			case <-ctx.Done():
				return res, ctx.Err()
			}
		}
		var st tbm.Status
		if err := c.sendSlot(ctx); err != nil {
			return res, err
		}
		fx, err := c.exchange(ctx, s, transport, "roster", key, s.Log().Last(), wait, func(line string) bool {
			return strings.HasPrefix(line, "TBM:") && st.Add(line)
		}, nil)
		if err != nil {
			return res, err
		}
		res.FollowUp, res.Next = &st, fx.next
	}
	return res, nil
}

// applyToggle takes the state a switch ACK reports (the executor field) into the tracker, as
// the addon does with its toggle fallback, so the next click flips from it. "mixed" says
// nothing about single bots, so the next click that needs the states polls.
func (c *tbmClient) applyToggle(b tbm.Button, a *tbm.Ack) {
	if a.Executor != "on" && a.Executor != "off" {
		c.tr.Stale()
		return
	}
	bot, _ := strings.CutPrefix(a.Scope, "bot:")
	if a.Scope == "party" {
		bot = ""
	}
	c.tr.SetBotKey(bot, b.Toggle, a.Executor)
}

// scopeOf says where the click lands: the server resolves ScopeTarget from the player's
// target; the other scopes are in the command text.
func scopeOf(b tbm.Button, c tbm.Command, targetBot string) string {
	switch b.Scope {
	case tbm.ScopeBot:
		return "bot:" + c.Args["bot"]
	case tbm.ScopeNone:
		return ""
	}
	if targetBot != "" {
		return "bot:" + targetBot
	}
	return "party"
}

// sendRaw sends any addon command with the addon's transport rule and returns the first
// ACK/ERR or roster response it gets.
func (c *tbmClient) sendRaw(ctx context.Context, p sendParams) (clickResult, error) {
	command := strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(p.Command), ".bot "))
	if command == "" {
		return clickResult{}, fmt.Errorf("command is empty")
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	s := c.cur()
	c.tr.Reset(s)
	key := groupKey(s)
	c.drain(s, key)
	transport, why, polled, err := c.prepare(ctx, s, p.Transport, key, false)
	if err != nil {
		return clickResult{}, err
	}
	m := tbm.NewAnyMatcher()
	res := clickResult{Why: why, Refreshed: polled, Sent: sentFor(transport, command)}
	// From after the throttle: the any-reply matcher cannot pass over a late ACK, so it must
	// not be offered one.
	if err := c.sendSlot(ctx); err != nil {
		return res, err
	}
	x, err := c.exchange(ctx, s, transport, command, key, s.Log().Last(), waitBound(p.WaitMS, tbm.ActionTimeoutMS), m.Feed, nil)
	res.Since, res.Next = x.since, x.next
	res.Reply = m.Reply()
	return res, err
}
