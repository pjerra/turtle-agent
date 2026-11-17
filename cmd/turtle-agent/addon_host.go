package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/TortoiseBots/turtle-agent/internal/addonhost"
	"github.com/TortoiseBots/turtle-agent/internal/events"
	"github.com/TortoiseBots/turtle-agent/internal/packet"
	"github.com/TortoiseBots/turtle-agent/internal/rpc"
	"github.com/TortoiseBots/turtle-agent/internal/session"
	"github.com/TortoiseBots/turtle-agent/internal/tbm"
)

// The addon host (README "Addon host"): the real TortoiseBotsManager addon runs in a lua5.1
// sidecar (internal/addonhost) next to the session. It reads the session only through the
// event log and its read-only views (status, party, unit), and acts only through the send
// paths every other method uses (chat, addon message, target), so the session loop stays the
// single owner of the connection.

// addonStepMS is how often the real clock ticks the addon (OnUpdate) and feeds it new events.
const addonStepMS = 100

// addonStartParams: the interpreter (lua5.1 from PATH) and the addon checkout (settings
// tbm_addon_dir, or the pinned cache) are not the caller's to choose.
type addonStartParams struct {
	Clock string `json:"clock,omitempty"` // real (default): ticks with the wall clock; manual: only addon_tick advances it
}

type addonStartResult struct {
	Dir     string      `json:"dir"`
	Version string      `json:"version"` // the .toc version
	Files   []string    `json:"files"`
	Clock   string      `json:"clock"`
	Sent    []addonSent `json:"sent"` // what the addon sent while loading (its handshake)
}

type addonClickParams struct {
	Button string   `json:"button,omitempty"` // a tbm_buttons name ("Fight.AoE") or label ("AoE")
	Tab    string   `json:"tab,omitempty"`    // or a widget by tab and label: Fight, Camp, Party, Roster, Guild, Raid, Log, BotPanel
	Label  string   `json:"label,omitempty"`
	Bot    string   `json:"bot,omitempty"`   // BotPanel: whose panel; Party: the row
	Name   string   `json:"name,omitempty"`  // Roster/Guild: the row to tick
	Names  []string `json:"names,omitempty"` // Roster/Guild: the rows to tick
	Mark   string   `json:"mark,omitempty"`  // CC marks panel: the row (moon, skull, ...)
	Row    string   `json:"row,omitempty"`   // only inside the row that shows this text
	View   string   `json:"view,omitempty"`  // BotPanel: Gear, Bags or Behaviour first
	Before string   `json:"before,omitempty"`
	Bag    *int     `json:"bag,omitempty"` // BotPanel items
	Slot   *int     `json:"slot,omitempty"`
	Force  bool     `json:"force,omitempty"`   // click a greyed-out button anyway
	WaitMS *int     `json:"wait_ms,omitempty"` // real clock: wait this long for the reply; default 8000
}

type addonTickParams struct {
	MS int `json:"ms"`
}

// addonSent is one thing the addon sent through the agent.
type addonSent struct {
	MonoMS  int64  `json:"mono_ms"`
	Kind    string `json:"kind"`              // addon, chat or target
	Channel string `json:"channel,omitempty"` // addon: PARTY, ...; chat: SAY, ...
	Prefix  string `json:"prefix,omitempty"`
	Text    string `json:"text,omitempty"`
	To      string `json:"to,omitempty"`
	Error   string `json:"error,omitempty"`
}

type addonClickResult struct {
	Clicked  []string        `json:"clicked"` // every widget pressed, in order, and /tbm commands typed
	Sent     []addonSent     `json:"sent"`    // what the addon sent from the click to the end of the wait
	Reply    []string        `json:"reply"`   // server lines the addon received in that time
	TimedOut bool            `json:"timed_out,omitempty"`
	State    json.RawMessage `json:"state"`
}

type addonTickResult struct {
	Clock float64     `json:"clock"` // the addon's GetTime()
	Sent  []addonSent `json:"sent"`
}

// addonRunner is the one addon host of an agent.
type addonRunner struct {
	cur      func() *session.Session
	addonDir string // from the settings; empty: the pinned cache

	mu       sync.Mutex
	h        *addonhost.Host
	dir      string
	manual   bool
	cursor   uint64
	world    string // the last world pushed, JSON
	lastTick time.Time
	sent     []addonSent // the last maxAddonSent sends
	stopPump context.CancelFunc
	pumpDone chan struct{}
	err      error // why the host stopped on its own
	dropped  int   // times the log had already dropped events the addon had not seen
	// during a click: the sends and lines since it
	collect     bool
	clickSent   []addonSent
	clickLines  []string
	lastLineAt  time.Time
	firstSendAt time.Time
}

const maxAddonSent = 50

// registerAddonHost registers the addon_* methods and returns the func that stops the host.
func registerAddonHost(srv *rpc.Server, cur func() *session.Session, dir string) func() {
	r := &addonRunner{cur: cur, addonDir: dir}
	srv.Register("addon_start", rpc.Typed(func(ctx context.Context, p addonStartParams) (addonStartResult, error) {
		return r.start(p)
	}))
	srv.Register("addon_stop", rpc.Typed(func(ctx context.Context, _ empty) (empty, error) {
		return empty{}, r.stop()
	}))
	srv.Register("addon_state", rpc.Typed(func(ctx context.Context, _ empty) (json.RawMessage, error) {
		return r.state()
	}))
	srv.Register("addon_tick", rpc.Typed(func(ctx context.Context, p addonTickParams) (addonTickResult, error) {
		return r.tick(p.MS)
	}))
	srv.Register("addon_click", rpc.Typed(func(ctx context.Context, p addonClickParams) (addonClickResult, error) {
		return r.click(ctx, p)
	}))
	return func() { r.stop() }
}

// addonDir is the addon checkout: the settings' tbm_addon_dir, else the copy
// tools/tbmcontract/fetch.sh keeps of the commit the button table was made from.
func addonDir(dir string) (string, error) {
	if dir == "" {
		dir = filepath.Join(".cache", "tbmcontract", "addon-"+tbm.AddonCommit)
		if _, err := os.Stat(filepath.Join(dir, "TortoiseBotsManager.toc")); err != nil {
			return "", fmt.Errorf("no addon: set tbm_addon_dir in the settings, or run tools/tbmcontract/fetch.sh in the agent's working directory (looked for %s)", dir)
		}
	}
	return filepath.Abs(dir)
}

func (r *addonRunner) start(p addonStartParams) (addonStartResult, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.h != nil {
		return addonStartResult{}, errors.New("the addon host is already running (addon_stop first)")
	}
	manual := false
	switch p.Clock {
	case "", "real":
	case "manual":
		manual = true
	default:
		return addonStartResult{}, fmt.Errorf("clock %q: use real or manual", p.Clock)
	}
	dir, err := addonDir(r.addonDir)
	if err != nil {
		return addonStartResult{}, err
	}
	h, err := addonhost.Start(dir, r.perform)
	if err != nil {
		return addonStartResult{}, err
	}
	s := r.cur()
	log := s.Log()
	r.h, r.dir, r.manual, r.err, r.dropped = h, dir, manual, nil, 0
	r.cursor, r.sent, r.lastTick = log.Last(), nil, time.Now()
	w := r.worldJSON(s)
	r.world = w
	res, outs, err := h.Call("load", map[string]any{"world": json.RawMessage(w), "in_world": s.State() == session.StatusInWorld})
	if err != nil {
		h.Close()
		r.h = nil
		return addonStartResult{}, fmt.Errorf("loading the addon: %w", err)
	}
	var loaded struct {
		Version string   `json:"version"`
		Files   []string `json:"files"`
	}
	json.Unmarshal(res, &loaded)
	out := addonStartResult{Dir: dir, Version: loaded.Version, Files: loaded.Files, Clock: "real", Sent: r.note(outs)}
	if manual {
		out.Clock = "manual"
	} else {
		ctx, cancel := context.WithCancel(context.Background())
		r.stopPump, r.pumpDone = cancel, make(chan struct{})
		go r.pump(ctx, r.pumpDone)
	}
	return out, nil
}

func (r *addonRunner) stop() error {
	r.mu.Lock()
	h, cancel, done := r.h, r.stopPump, r.pumpDone
	r.h, r.stopPump, r.pumpDone = nil, nil, nil
	r.mu.Unlock()
	if h == nil {
		return errors.New("the addon host is not running")
	}
	if cancel != nil && done != nil {
		cancel()
		<-done
	}
	return h.Close()
}

// pump drives the real clock: every addonStepMS it feeds new events and ticks OnUpdate by
// the time that passed.
func (r *addonRunner) pump(ctx context.Context, done chan struct{}) {
	defer close(done)
	t := time.NewTicker(addonStepMS * time.Millisecond)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
		r.mu.Lock()
		if r.h != nil {
			r.realStep()
		}
		r.mu.Unlock()
	}
}

// realStep feeds events and ticks by the wall time since the last tick. The caller holds r.mu.
func (r *addonRunner) realStep() {
	now := time.Now()
	ms := int(now.Sub(r.lastTick) / time.Millisecond)
	if err := r.step(ms); err != nil {
		return
	}
	r.lastTick = r.lastTick.Add(time.Duration(ms) * time.Millisecond)
}

// ready returns the running host, or why there is none. The caller holds r.mu.
func (r *addonRunner) ready() error {
	if r.h != nil {
		return nil
	}
	if r.err != nil {
		return fmt.Errorf("the addon host stopped: %w (addon_stop, then addon_start)", r.err)
	}
	return errors.New("the addon host is not running (addon_start)")
}

// call runs one op and records what the addon sent. A host that died is closed and its
// error kept. The caller holds r.mu.
func (r *addonRunner) call(op string, params any) (json.RawMessage, error) {
	if err := r.ready(); err != nil {
		return nil, err
	}
	res, outs, err := r.h.Call(op, params)
	r.note(outs)
	if errors.Is(err, addonhost.ErrStopped) {
		r.err = err
		r.h.Close()
		r.h = nil
		if r.stopPump != nil {
			r.stopPump() // the pump exits on its next step; stop() is not needed
			r.stopPump, r.pumpDone = nil, nil
		}
	}
	return res, err
}

// note records the addon's sends. The caller holds r.mu.
func (r *addonRunner) note(outs []addonhost.Out) []addonSent {
	list := []addonSent{}
	for _, o := range outs {
		s := addonSent{MonoMS: events.MonoMS(), Kind: o.Out, Prefix: o.Prefix, Text: o.Text, To: o.To, Error: o.Error}
		switch o.Out {
		case "addon":
			s.Channel = o.Channel
		case "chat":
			s.Channel = o.Type
		case "target":
			s.Text = o.Name
			if o.GUID != "" {
				s.Text = strings.TrimSpace(o.Name + " " + o.GUID)
			}
		}
		list = append(list, s)
		r.sent = append(r.sent, s)
		if r.collect {
			r.clickSent = append(r.clickSent, s)
			if r.firstSendAt.IsZero() {
				r.firstSendAt = time.Now()
			}
		}
	}
	if len(r.sent) > maxAddonSent {
		r.sent = append([]addonSent(nil), r.sent[len(r.sent)-maxAddonSent:]...)
	}
	return list
}

// perform carries out what the addon asked for, on the current session.
func (r *addonRunner) perform(o addonhost.Out) error {
	s := r.cur()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	switch o.Out {
	case "addon":
		return s.AddonSend(ctx, strings.ToLower(o.Channel), o.Prefix, o.Text, o.To)
	case "chat":
		// The addon's own bytes: a ".bot" line goes out as say chat, which the server runs.
		return s.Chat(ctx, strings.ToLower(o.Type), o.Text, o.To)
	case "target":
		var g uint64
		if o.GUID != "" {
			var err error
			if g, err = strconv.ParseUint(o.GUID, 10, 64); err != nil {
				return fmt.Errorf("target guid %q: %w", o.GUID, err)
			}
		}
		name := o.Name
		if g != 0 {
			name = ""
		}
		_, err := s.Target(ctx, packet.GUID(g), name)
		return err
	}
	return fmt.Errorf("unknown request %q from the addon", o.Out)
}

// step feeds the events logged since the cursor (a group change pushes the group at its
// place in the stream), pushes the world when it changed, then ticks ms. The caller holds
// r.mu.
func (r *addonRunner) step(ms int) error {
	s := r.cur()
	log := s.Log()
	var batch []addonhost.Event
	var batchEnd uint64 // the seq of the last event in batch
	// flush feeds the batch; the cursor moves past it only once the addon has it.
	flush := func() error {
		if len(batch) == 0 {
			return nil
		}
		if _, err := r.call("events", map[string]any{"events": batch}); err != nil {
			return err
		}
		batch = nil
		r.cursor = batchEnd
		return nil
	}
	for {
		p := log.Read(events.Query{Since: r.cursor, Limit: events.MaxLimit})
		if p.Dropped {
			r.dropped++
		}
		for _, e := range p.Events {
			if addonhost.GroupChanged(e) {
				if err := flush(); err != nil {
					return err
				}
				if err := r.pushWorld(s, true); err != nil {
					return err
				}
				r.cursor = e.Seq
				continue
			}
			ev, ok := addonhost.GameEvent(e)
			if !ok {
				continue
			}
			batch, batchEnd = append(batch, ev), e.Seq
			if r.collect && (ev.Event == "CHAT_MSG_SYSTEM" || ev.Event == "CHAT_MSG_ADDON") {
				line := ev.Args[0]
				if ev.Event == "CHAT_MSG_ADDON" {
					line = ev.Args[1]
				}
				r.clickLines = append(r.clickLines, line)
				r.lastLineAt = time.Now()
			}
		}
		if err := flush(); err != nil {
			return err
		}
		r.cursor = p.Next // the events after the last fed one were none of the addon's
		if !p.More {
			break
		}
	}
	if err := r.pushWorld(s, false); err != nil {
		return err
	}
	if ms > 0 {
		_, err := r.call("tick", map[string]any{"ms": ms})
		return err
	}
	return nil
}

// pushWorld sends the units the addon can ask about when they changed (always with force).
func (r *addonRunner) pushWorld(s *session.Session, force bool) error {
	w := r.worldJSON(s)
	if w == r.world && !force {
		return nil
	}
	r.world = w
	_, err := r.call("world", map[string]any{"world": json.RawMessage(w)})
	return err
}

// addonUnit is a unit as the addon's UnitName, UnitIsDead, ... see it.
type addonUnit struct {
	Name   string `json:"name,omitempty"`
	GUID   string `json:"guid,omitempty"`
	Level  int    `json:"level,omitempty"`
	Dead   bool   `json:"dead,omitempty"`
	Online bool   `json:"online"`
	Enemy  bool   `json:"enemy,omitempty"`
	Player bool   `json:"player,omitempty"`
}

type addonWorld struct {
	Player addonUnit   `json:"player"`
	Target *addonUnit  `json:"target,omitempty"`
	Party  []addonUnit `json:"party"`
	Raid   []addonUnit `json:"raid"`
}

// worldJSON is what the session knows about the units a 1.12 client exposes to addons:
// player, target, party1..4 (the player's subgroup in a raid) and raid1..40. Classes are not
// known for other units (the agent does not read UNIT_FIELD_BYTES_0), and a ghost is reported
// dead.
func (r *addonRunner) worldJSON(s *session.Session) string {
	st := s.Status()
	w := addonWorld{Player: addonUnit{Name: st.Character, GUID: guidString(st.GUID), Level: int(st.Level), Online: true, Player: true,
		Dead: st.MaxHealth > 0 && st.Health == 0}, Party: []addonUnit{}, Raid: []addonUnit{}}
	if st.Target != 0 {
		t := addonUnit{GUID: guidString(st.Target), Online: true}
		if u, err := s.Unit(st.Target, ""); err == nil {
			t.Name, t.Level, t.Dead, t.Enemy, t.Player = u.Name, int(u.Level), u.Dead, u.Hostile, u.Type == "player"
		}
		w.Target = &t
	}
	p := s.Party()
	if p.InGroup {
		if p.Raid {
			w.Raid = append(w.Raid, w.Player)
		}
		for _, m := range p.Members {
			u := addonUnit{Name: m.Name, GUID: guidString(m.GUID), Level: int(m.Level), Dead: m.Dead, Online: m.Online, Player: true}
			if p.Raid {
				w.Raid = append(w.Raid, u)
				if m.Subgroup != p.Subgroup {
					continue
				}
			}
			if len(w.Party) < 4 {
				w.Party = append(w.Party, u)
			}
		}
	}
	b, _ := json.Marshal(w)
	return string(b)
}

func guidString(g packet.GUID) string {
	if g == 0 {
		return ""
	}
	return strconv.FormatUint(uint64(g), 10)
}

// sync feeds what happened since the last step, without moving the addon's clock.
func (r *addonRunner) sync() error {
	if err := r.ready(); err != nil {
		return err
	}
	if !r.manual {
		r.realStep()
		return r.ready()
	}
	return r.step(0)
}

func (r *addonRunner) state() (json.RawMessage, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if err := r.sync(); err != nil {
		return nil, err
	}
	return r.stateLocked()
}

// stateLocked is the addon's state plus what the agent knows of the host. The caller holds r.mu.
func (r *addonRunner) stateLocked() (json.RawMessage, error) {
	res, err := r.call("state", nil)
	if err != nil {
		return nil, err
	}
	var m map[string]json.RawMessage
	if err := json.Unmarshal(res, &m); err != nil {
		return nil, err
	}
	clock := "real"
	if r.manual {
		clock = "manual"
	}
	// dropped: how often the event log had already dropped events the addon never got (the
	// log is a bounded ring); the addon's view may then miss lines.
	m["host"], _ = json.Marshal(map[string]any{"dir": r.dir, "clock": clock, "cursor": r.cursor, "dropped": r.dropped})
	sent := r.sent
	if sent == nil {
		sent = []addonSent{}
	}
	m["sent"], _ = json.Marshal(sent)
	return json.Marshal(m)
}

func (r *addonRunner) tick(ms int) (addonTickResult, error) {
	if ms <= 0 || ms > 120000 {
		return addonTickResult{}, fmt.Errorf("ms %d: use 1 to 120000", ms)
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if err := r.ready(); err != nil {
		return addonTickResult{}, err
	}
	before := len(r.sent)
	var clock float64
	for left := ms; left > 0; left -= addonStepMS {
		if err := r.step(min(left, addonStepMS)); err != nil {
			return addonTickResult{}, err
		}
	}
	res, err := r.call("tick", map[string]any{"ms": 0})
	if err != nil {
		return addonTickResult{}, err
	}
	var c struct {
		Clock float64 `json:"clock"`
	}
	json.Unmarshal(res, &c)
	clock = c.Clock
	out := addonTickResult{Clock: clock, Sent: []addonSent{}}
	if len(r.sent) >= before {
		out.Sent = append(out.Sent, r.sent[min(before, len(r.sent)):]...)
	}
	return out, nil
}

// replyDone says whether lines hold the end of the reply a button of kind waits for.
func replyDone(kind string, lines []string) bool {
	for _, l := range lines {
		verb, _, _ := strings.Cut(strings.TrimPrefix(l, "TBM:"), "|")
		tbmLine := strings.HasPrefix(l, "TBM:")
		switch kind {
		case tbm.ReplyAck:
			if tbmLine && (verb == "ACTION_ACK" || verb == "ACTION_ERR") {
				return true
			}
		case tbm.ReplyInventory:
			if tbmLine && (verb == "INV_END" || verb == "ACTION_ERR") {
				return true
			}
		case tbm.ReplySnapshot:
			if tbmLine && (verb == "TRANSPORT" || verb == "ROSTER_ERROR") {
				return true
			}
		default: // text replies and widgets outside the button table: the first line
			return true
		}
	}
	return false
}

func (r *addonRunner) click(ctx context.Context, p addonClickParams) (addonClickResult, error) {
	plan, kind, err := planAddonClick(p)
	if err != nil {
		return addonClickResult{}, err
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if err := r.sync(); err != nil {
		return addonClickResult{}, err
	}
	r.collect, r.clickSent, r.clickLines, r.firstSendAt = true, nil, nil, time.Time{}
	defer func() { r.collect = false }()
	res, err := r.call("click", plan)
	if err != nil {
		return addonClickResult{}, err
	}
	var c struct {
		Clicked []string `json:"clicked"`
	}
	json.Unmarshal(res, &c)
	out := addonClickResult{Clicked: c.Clicked}

	var wait time.Duration
	if !r.manual {
		wait = waitBound(derefInt(p.WaitMS, tbm.ActionTimeoutMS), tbm.ActionTimeoutMS)
		if p.WaitMS != nil && *p.WaitMS <= 0 {
			wait = 0
		}
	}
	start := time.Now()
	for wait > 0 {
		if r.firstSendAt.IsZero() && time.Since(start) > time.Second {
			break // nothing left the client: the click only changed the window
		}
		if !r.firstSendAt.IsZero() && replyDone(kind, r.clickLines) {
			break
		}
		if time.Since(start) >= wait {
			out.TimedOut = true
			break
		}
		r.mu.Unlock()
		select {
		case <-time.After(addonStepMS * time.Millisecond):
		case <-ctx.Done():
		}
		r.mu.Lock()
		if ctx.Err() != nil {
			return out, ctx.Err()
		}
		if err := r.ready(); err != nil {
			return out, err
		}
		r.realStep()
	}
	if err := r.ready(); err != nil {
		return out, err
	}
	if r.manual {
		if err := r.step(0); err != nil {
			return out, err
		}
	}
	out.Sent, out.Reply = r.clickSent, r.clickLines
	if out.Sent == nil {
		out.Sent = []addonSent{}
	}
	if out.Reply == nil {
		out.Reply = []string{}
	}
	out.State, err = r.stateLocked()
	return out, err
}

func derefInt(p *int, def int) int {
	if p == nil {
		return def
	}
	return *p
}

// addonClickPlan is a click as host.lua's click op takes it.
type addonClickPlan struct {
	Tab    string    `json:"tab"`
	Label  string    `json:"label,omitempty"`
	Row    string    `json:"row,omitempty"`
	Select *[]string `json:"select,omitempty"`
	Bot    string    `json:"bot,omitempty"`
	View   string    `json:"view,omitempty"`
	Item   *struct {
		Bag  int `json:"bag"`
		Slot int `json:"slot"`
	} `json:"item,omitempty"`
	Before string `json:"before,omitempty"`
	Force  bool   `json:"force,omitempty"`
}

// addonTabs maps the button table's tab names to the addon's own (UI.lua showTab).
var addonTabs = map[string]string{"fight": "actions", "actions": "actions", "camp": "camp", "party": "party",
	"roster": "roster", "guild": "guild", "raid": "raid", "log": "log", "botpanel": "botpanel"}

// ccMarkLabels are the marks panel's row labels (Constants.lua CC_MARKS).
var ccMarkLabels = map[string]string{"moon": "Moon", "circle": "Circle", "square": "Square", "cross": "Cross",
	"triangle": "Triangle", "diamond": "Diamond", "star": "Star", "skull": "Skull"}

// planAddonClick turns a click request into the widget steps a player takes, and the reply
// kind to wait for.
func planAddonClick(p addonClickParams) (addonClickPlan, string, error) {
	var plan addonClickPlan
	plan.Force, plan.Row, plan.View, plan.Before = p.Force, p.Row, p.View, p.Before
	names := p.Names
	if p.Name != "" {
		names = append([]string{p.Name}, names...)
	}
	kind := ""
	label, tab := p.Label, p.Tab
	if p.Button != "" {
		b, err := tbm.Find(p.Button)
		if err != nil {
			return plan, "", err
		}
		kind, tab = b.Reply, b.Tab
		if label == "" {
			label = b.Label
		}
		mark := ""
		if p.Mark != "" {
			if mark = ccMarkLabels[strings.ToLower(p.Mark)]; mark == "" {
				return plan, "", fmt.Errorf("mark %q: use moon, circle, square, cross, triangle, diamond, star or skull", p.Mark)
			}
		}
		switch b.Name {
		case "Fight.CCAssign", "Fight.CCClear":
			if mark == "" {
				return plan, "", fmt.Errorf("%s: name the mark's row (mark: moon, skull, ...); Next gives it to the next online bot, X clears it", b.Name)
			}
			plan.Before, plan.Row = "CC Mark", mark
			label = map[string]string{"Fight.CCAssign": "Next", "Fight.CCClear": "X"}[b.Name]
		case "Fight.CCClearAll":
			plan.Before = "CC Mark"
		case "BotPanel.Inventory":
			label = "" // opening the panel asks for the inventory
		case "BotPanel.Item.Equip", "BotPanel.Item.Unequip", "BotPanel.Item.Give":
			if p.Bag == nil || p.Slot == nil {
				return plan, "", fmt.Errorf("%s needs bag and slot", b.Name)
			}
			plan.Item = &struct {
				Bag  int `json:"bag"`
				Slot int `json:"slot"`
			}{*p.Bag, *p.Slot}
			if plan.View == "" {
				plan.View = "Bags"
				if *p.Bag == 255 { // the equipped items (BotPanel.lua EQUIPPED_BAG)
					plan.View = "Gear"
				}
			}
		}
		if strings.HasPrefix(b.Name, "BotPanel.Behavior.") && plan.View == "" {
			plan.View = "Behaviour"
		}
		if (b.Tab == "Roster" || b.Tab == "Guild") && b.Name != "Roster.Refresh" && len(names) == 0 {
			return plan, "", fmt.Errorf("%s: name the rows to tick (name or names)", b.Name)
		}
	}
	if tab == "" {
		return plan, "", errors.New("give a button, or a tab and a label")
	}
	plan.Tab = addonTabs[strings.ToLower(strings.ReplaceAll(tab, " ", ""))]
	if plan.Tab == "" {
		return plan, "", fmt.Errorf("tab %q: use Fight, Camp, Party, Roster, Guild, Raid, Log or BotPanel", tab)
	}
	if plan.Tab == "botpanel" {
		if p.Bot == "" {
			return plan, "", errors.New("the bot panel needs bot")
		}
		plan.Bot = p.Bot
	} else if plan.Tab == "party" && p.Bot != "" && plan.Row == "" {
		plan.Row = p.Bot
	}
	if p.Mark != "" && plan.Row == "" {
		plan.Row = ccMarkLabels[strings.ToLower(p.Mark)]
	}
	if len(names) > 0 {
		plan.Select = &names
	}
	if label == "" && plan.Tab != "botpanel" && plan.Select == nil {
		return plan, "", errors.New("give the label of the button to click")
	}
	plan.Label = label
	return plan, kind, nil
}
