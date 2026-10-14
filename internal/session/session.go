// Copyright (c) 2026 agent-wow
// SPDX-License-Identifier: MIT
// The single-owner loop shape (one select over packets, requests and timers; ping
// cadence; logout wait) is ported from github.com/agent-wow/agent-wow pkg/world/engine.go
// at 5ef4916 (see NOTICE). The login sequence, readiness rule and handlers are new.

// Package session owns one logged-in character. Run is the only goroutine that writes
// packets and changes state; other goroutines call methods that queue work for it.
package session

import (
	"context"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"strings"
	"sync"
	"time"

	"github.com/pjerra/turtle-agent/internal/events"
	"github.com/pjerra/turtle-agent/internal/game"
	"github.com/pjerra/turtle-agent/internal/objects"
	"github.com/pjerra/turtle-agent/internal/opcode"
	"github.com/pjerra/turtle-agent/internal/packet"
	"github.com/pjerra/turtle-agent/internal/spells"
	"github.com/pjerra/turtle-agent/internal/spellstate"
	"github.com/pjerra/turtle-agent/internal/worldconn"
)

type Status string

const (
	StatusConnecting   Status = "connecting"
	StatusCharSelect   Status = "char_select"
	StatusLoggingIn    Status = "logging_in"
	StatusInWorld      Status = "in_world"
	StatusLoggingOut   Status = "logging_out"
	StatusLoggedOut    Status = "logged_out"
	StatusDisconnected Status = "disconnected"
)

var (
	ErrClosed       = errors.New("session closed")
	ErrDisconnected = errors.New("disconnected by the server")
	ErrNotInWorld   = errors.New("not in world")
	// ErrClosedAtLogout ends Run when the server closed the socket after a logout request
	// without SMSG_LOGOUT_COMPLETE (issues #9, #18). The logout itself succeeded: Logout
	// answers LogoutClosedByServer.
	ErrClosedAtLogout = errors.New("the server closed the connection during the logout, without SMSG_LOGOUT_COMPLETE")
	// ErrLostAtLogout ends Logout and Run when the socket died after a logout request the
	// server had not accepted yet: the core keeps a dropped character in world for a while
	// (WorldSession.cpp, ForcePlayerLogoutDelay), so this is not a logout. Like the other
	// logout ends it does not wrap ErrDisconnected: no reconnect, no grace window.
	ErrLostAtLogout = errors.New("the connection was lost before the server accepted the logout")
	// ErrLogoutTimeout ends Logout and Run when the server neither completed the logout nor
	// closed the socket within Options.LogoutTimeout; the agent then closes the socket.
	ErrLogoutTimeout = errors.New("logout timed out without SMSG_LOGOUT_COMPLETE")
	errNoPong        = errors.New("no SMSG_PONG within the pong timeout")
)

// LogoutOutcome says how an accepted logout ended.
type LogoutOutcome string

const (
	// LogoutComplete: the server sent SMSG_LOGOUT_COMPLETE (WorldSession.cpp LogoutPlayer).
	LogoutComplete LogoutOutcome = "complete"
	// LogoutClosedByServer: the server accepted the logout (SMSG_LOGOUT_RESPONSE reason 0)
	// and then closed the socket instead.
	LogoutClosedByServer LogoutOutcome = "closed_by_server"
	// LogoutLost: the socket died before the server accepted the logout (ErrLostAtLogout).
	LogoutLost LogoutOutcome = "lost"
	// LogoutTimedOut: no answer within Options.LogoutTimeout (ErrLogoutTimeout).
	LogoutTimedOut LogoutOutcome = "timeout"
)

// LogoutError is a refused logout (MiscHandler.cpp:350-365).
type LogoutError struct{ Reason uint32 }

func (e *LogoutError) Error() string {
	reason := "unknown reason"
	switch e.Reason {
	case 1:
		reason = "in combat"
	case 2:
		reason = "frozen by a GM"
	case 3:
		reason = "jumping or falling"
	}
	return fmt.Sprintf("logout refused: %s (reason %d)", reason, e.Reason)
}

type Options struct {
	Character     string        // character name, case-insensitive
	Create        *CreateSpec   // nil: no create; set: create this race/class when the list lacks the name
	Log           *events.Log   // nil: a new 100000-event log
	Logger        io.Writer     // diagnostics such as parse errors with hex dumps; nil: discarded
	PingInterval  time.Duration // 0: 30 s; under 27 s is test-only: the server kicks for overspeed pings (WorldSocket.cpp:413-433)
	PongTimeout   time.Duration // 0: 90 s
	LogoutTimeout time.Duration // 0: 30 s
	// LoginRetryDelay spaces the CMSG_PLAYER_LOGIN retries after a locked character; 0: 2 s.
	LoginRetryDelay time.Duration
	// NamesWait bounds how long an event waits for a source or target name before it is
	// logged with the names known so far; 0: 2 s.
	NamesWait time.Duration
	// MarkWait bounds how long mark waits for the server's MSG_RAID_TARGET_UPDATE echo; 0: 2 s.
	MarkWait time.Duration
	// TradeSettle is how long Trade waits after its last change to the window before it accepts; 0: 1.1 s, which clears
	// the core's one-second scam-prevention delay. Shorter is for tests against a scripted server.
	TradeSettle time.Duration
	Store       *objects.Store // nil: a new store
	// Redact lists account names the server may echo back, as in a .pinfo reply, and
	// RedactAnywhere passwords. They are replaced with "<redacted>" in event text, names
	// and string data (ASCII case-insensitive; account names on whole words only, so a
	// character named after its account keeps its name, passwords anywhere), and a packet
	// holding one is never hex-dumped.
	Redact         []string
	RedactAnywhere []string
	// Planner plans path_to and walk_path on the navmesh; nil: both answer ErrNoNavmesh.
	Planner Planner
}

// maxLoginRetries bounds the retries after SMSG_CHARACTER_LOGIN_FAILED code 1. With the
// 2 s delay they cover the backup worker's 5 s cycle plus the dump (World.cpp:4841-4869).
const maxLoginRetries = 5

type StatusInfo struct {
	State        Status         `json:"state"`
	Character    string         `json:"character,omitempty"`
	GUID         packet.GUID    `json:"guid,omitempty"`
	Level        uint8          `json:"level,omitempty"`
	Map          uint32         `json:"map"`
	X            float32        `json:"x"`
	Y            float32        `json:"y"`
	Z            float32        `json:"z"`
	O            float32        `json:"o"`
	Target       packet.GUID    `json:"target,omitempty"`
	Health       uint32         `json:"health"`
	MaxHealth    uint32         `json:"max_health"`
	Power        uint32         `json:"power"`
	MaxPower     uint32         `json:"max_power"`
	Falling      bool           `json:"falling"`   // in the air: a knockback, or a fall after a teleport
	ZUnknown     bool           `json:"z_unknown"` // z is a guess (GM teleport, climb, landing) until the server sends a position
	LatencyMS    uint32         `json:"latency_ms"`
	Unknown      map[string]int `json:"unknown_opcodes"`
	ParseErrors  int            `json:"parse_errors"`
	LastSeq      uint64         `json:"last_seq"`
	MonoMS       int64          `json:"mono_ms"`        // ms since base_time on the monotonic clock, as in events
	BaseTime     time.Time      `json:"base_time"`      // wall time of mono_ms 0
	WallOffsetMS int64          `json:"wall_offset_ms"` // wall clock now minus base_time + mono_ms: host clock steps and drift
	Error        string         `json:"error,omitempty"`

	// The agent's own spell state (README "Agent state").
	ComboPoints uint8       `json:"combo_points"`           // 0-5, from the player's own update fields
	ComboTarget packet.GUID `json:"combo_target,omitempty"` // the unit they are on; absent without points
	GCDMS       int64       `json:"gcd_ms"`                 // global cooldown left; 0 when none

	// Dead is health 0 with the spirit not released; Ghost is a released spirit (health 1). Both false: alive.
	Dead  bool `json:"dead"`
	Ghost bool `json:"ghost"`
}

type handler func(body []byte) error

// fatalError, returned by a handler, ends the session (login failures).
type fatalError struct{ err error }

func (f *fatalError) Error() string { return f.err.Error() }

type request struct {
	fn    func() (any, error)
	reply chan result
}

type result struct {
	v   any
	err error
}

type logoutOp struct {
	done    chan struct{}
	outcome LogoutOutcome
	err     error
}

func (op *logoutOp) finish(outcome LogoutOutcome, err error) {
	op.outcome, op.err = outcome, err
	close(op.done)
}

type Session struct {
	conn      *worldconn.Conn
	opts      Options
	log       *events.Log
	store     *objects.Store
	logger    io.Writer
	redact    redactor
	handlers  map[uint16]handler
	reqs      chan request
	done      chan struct{}
	ready     chan struct{}
	start     time.Time
	clockFn   func() time.Time // nil: the wall clock; a replay sets the time of the record it handles
	afterTake func()           // tests only: runs in takeAll between the take and the close

	mu        sync.Mutex // guards these fields for readers outside the loop
	state     Status
	self      Character
	mapID     uint32
	pos       [4]float32
	walkDist  float64 // yards along its path the last walk got to when it stopped, arrived or was aborted; read by walk_path
	latency   uint32
	unknown   map[uint16]int
	parseErrs int
	lastErr   string
	party     PartyInfo
	offer     *resurrectOffer                 // the open resurrection offer, death.go
	reclaimAt time.Time                       // the corpse may be reclaimed from here, death.go
	marks     [game.RaidIconCount]packet.GUID // the group's raid icons
	target    packet.GUID
	falling   bool // s.fall != nil, for Status
	zUnknown  bool

	// owner-loop only
	finished        bool
	nextPing        time.Time
	pingSent        time.Time
	pingSeq         uint32
	pongWait        bool
	logout          *logoutOp
	logoutDeadline  time.Time
	logoutAccepted  bool // SMSG_LOGOUT_RESPONSE reason 0 for the pending logout
	loginRetries    int
	createSent      bool // CMSG_CHAR_CREATE sent once (Options.Create)
	loginRetryAt    time.Time
	names           *nameCache
	walk            *walkState
	runSpeed        float32
	rooted          bool
	splineUntil     time.Time // a server spline moves the player until then
	teleportWaiters []chan struct{}
	corpseWaiters   []chan game.Corpse // calls waiting for MSG_CORPSE_QUERY (death.go)
	markWaiters     []markWaiter
	rolls           map[rollKey]*openRoll
	lastRollID      uint64
	loot            lootState        // the open loot window (lootwindow.go)
	mount           mountState       // the player's mount (useitem.go)
	gossip          *game.GossipMenu // the open gossip menu, nil once closed
	npc             npcState         // vendor, trainer and quest requests awaiting their results
	fall            *fallState
	traceSeq        uint64    // the last floor search sent off the loop (fall.go)
	placed          uint64    // counts the positions the server put the player at (serverPosition)
	last            lastMove  // what the server has for the player
	lastClock       uint32    // the last MovementInfo client time sent
	mclock          moveClock // the movement client time, in step with real time (clock.go)
	toggles         uint32    // water walking, hover, feather fall as the server applied them

	// spellState is the agent's spellbook, cooldowns and cast state (internal/spellstate); it
	// locks itself, so RPC calls read it directly.
	spellState *spellstate.State

	trade tradeState // the player's trade window (trade.go)

	pet petTracker // the player's pet: its action bar and what was announced of it (pet.go)
}

func New(conn *worldconn.Conn, opts Options) *Session {
	if opts.Log == nil {
		opts.Log = events.New(100000)
	}
	if opts.Logger == nil {
		opts.Logger = io.Discard
	}
	if opts.PingInterval == 0 {
		opts.PingInterval = 30 * time.Second
	}
	if opts.PongTimeout == 0 {
		opts.PongTimeout = 90 * time.Second
	}
	if opts.LogoutTimeout == 0 {
		opts.LogoutTimeout = 30 * time.Second
	}
	if opts.LoginRetryDelay == 0 {
		opts.LoginRetryDelay = 2 * time.Second
	}
	if opts.NamesWait == 0 {
		opts.NamesWait = 2 * time.Second
	}
	if opts.MarkWait == 0 {
		opts.MarkWait = 2 * time.Second
	}
	if opts.TradeSettle == 0 {
		opts.TradeSettle = tradeSettle
	}
	if opts.Store == nil {
		opts.Store = objects.NewStore()
	}
	s := &Session{
		conn: conn, opts: opts, log: opts.Log, logger: opts.Logger,
		handlers: map[uint16]handler{}, reqs: make(chan request), done: make(chan struct{}),
		ready: make(chan struct{}), start: time.Now(), state: StatusConnecting, unknown: map[uint16]int{},
		names: newNameCache(), store: opts.Store, rolls: map[rollKey]*openRoll{}, npc: newNPCState(), runSpeed: defaultRunSpeed, redact: newRedactor(opts.Redact, opts.RedactAnywhere),
	}
	s.spellState = spellstate.New(s.now)
	go spells.Warm() // the tables load once per process; keep the first lookup out of the loop
	s.register()
	return s
}

// now is the session's clock: the wall clock, or in a replay the time of the packet being
// handled, so timeouts and event times follow the capture instead of the replay's speed.
func (s *Session) now() time.Time {
	if s.clockFn != nil {
		return s.clockFn()
	}
	return time.Now()
}

// register installs every packet handler; each feature adds its own register call here.
func (s *Session) register() {
	s.registerCore()
	s.registerChat()
	s.registerParty()
	s.registerWorld()
	s.registerActions()
	s.registerCombat()
	s.registerRaid()
	s.registerLoot()
	s.registerItems()
	s.registerLootWindow()
	s.registerUseItem()
	s.registerSocial()
	s.registerNPC()
	s.registerSpells()
	s.registerDeath()
	s.registerPet()
	s.registerTrade()
}

func (s *Session) registerCore() {
	s.handlers[opcode.SMSGCharEnum] = s.onCharEnum
	s.handlers[opcode.SMSGCharCreate] = s.onCharCreate
	s.handlers[opcode.SMSGCharacterLoginFailed] = s.onLoginFailed
	s.handlers[opcode.SMSGLoginVerifyWorld] = s.onVerifyWorld
	s.handlers[opcode.SMSGPong] = s.onPong
	s.handlers[opcode.SMSGLogoutResponse] = s.onLogoutResponse
	s.handlers[opcode.SMSGLogoutComplete] = s.onLogoutComplete
	// Warden is never answered; see README "What the server sees" (Warden.cpp:494-500).
	s.handlers[opcode.SMSGWardenData] = func([]byte) error { return nil }
	s.handlers[opcode.SMSGAddonInfo] = func([]byte) error { return nil }
}

// Ready closes once SMSG_LOGIN_VERIFY_WORLD arrives. It never closes when login fails
// (missing character, SMSG_CHARACTER_LOGIN_FAILED, disconnect), so callers also select
// on Done.
func (s *Session) Ready() <-chan struct{} { return s.ready }

// Done closes when Run returns.
func (s *Session) Done() <-chan struct{} { return s.done }

func (s *Session) Log() *events.Log { return s.log }

// Redact scrubs the session's secrets (Options.Redact, Options.RedactAnywhere) out of text.
func (s *Session) Redact(text string) string { return s.redact.scrub(text) }

func (s *Session) State() Status {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.state
}

func (s *Session) setState(st Status) {
	s.mu.Lock()
	s.state = st
	s.mu.Unlock()
}

func (s *Session) Status() StatusInfo {
	s.mu.Lock()
	defer s.mu.Unlock()
	unknown := make(map[string]int, len(s.unknown))
	for op, n := range s.unknown {
		unknown[opcode.Name(op)] = n
	}
	info := StatusInfo{
		State: s.state, Character: s.self.Name, GUID: s.self.GUID, Level: s.self.Level, Map: s.mapID,
		X: s.pos[0], Y: s.pos[1], Z: s.pos[2], O: s.pos[3], Target: s.target, LatencyMS: s.latency,
		Falling: s.falling, ZUnknown: s.zUnknown,
		Unknown: unknown, ParseErrors: s.parseErrs, LastSeq: s.log.Last(), MonoMS: events.MonoMS(), BaseTime: events.BaseTime(), WallOffsetMS: events.WallOffsetMS(), Error: s.lastErr,
	}
	// The player's own values from the store (zero until its create block arrives).
	if u, ok := s.store.Get(s.self.GUID, s.pos[0], s.pos[1], s.pos[2]); ok {
		info.Health, info.MaxHealth, info.Power, info.MaxPower = u.Health, u.MaxHealth, u.Power, u.MaxPower
		info.ComboPoints, info.ComboTarget = u.ComboPoints, u.ComboTarget
		info.Dead, info.Ghost = u.Dead, u.Ghost
		if u.Level > 0 { // the live level, not the one from the character list
			info.Level = uint8(min(u.Level, 255))
		}
	}
	info.GCDMS = s.spellGCDMS()
	return info
}

// Run drives the character from the character list to logout. It returns nil after a
// clean logout, ErrClosedAtLogout, ErrLostAtLogout or ErrLogoutTimeout after a logout that
// ended another way, and an error wrapping ErrDisconnected when the server closes the socket otherwise.
func (s *Session) Run(ctx context.Context) error {
	defer close(s.done)
	defer s.conn.Close()
	packets := make(chan worldconn.Packet, 256)
	readErr := make(chan error, 1)
	go func() {
		for {
			p, err := s.conn.ReadPacket()
			if err != nil {
				readErr <- err
				return
			}
			select {
			case packets <- p:
			case <-s.done:
				return
			}
		}
	}()
	if err := s.conn.WritePacket(opcode.CMSGCharEnum, nil); err != nil { // core: WorldSession::HandleCharEnumOpcode
		return s.connLost(err)
	}
	s.setState(StatusCharSelect)
	tick := time.NewTicker(100 * time.Millisecond)
	defer tick.Stop()
	for {
		select {
		case <-ctx.Done():
			return s.end(ctx.Err())
		case err := <-readErr:
			// The reader queued every packet it read before the error, so a final
			// SMSG_LOGOUT_COMPLETE or system message sent just before the close is still handled.
			for len(packets) > 0 {
				if stop, err := s.handle(<-packets); stop {
					return err
				}
			}
			return s.connLost(err)
		case p := <-packets:
			if stop, err := s.handle(p); stop {
				return err
			}
		case r := <-s.reqs:
			v, err := r.fn()
			r.reply <- result{v, err}
		case now := <-tick.C:
			if err := s.onTick(now); err != nil {
				return s.connLost(err)
			}
		}
	}
}

// handle dispatches one packet; stop reports that Run must return err (nil after logout).
func (s *Session) handle(p worldconn.Packet) (stop bool, err error) {
	if err := s.dispatch(p); err != nil {
		return true, s.end(err)
	}
	return s.finished, nil
}

// do runs fn on the owner goroutine and returns its result.
func (s *Session) do(ctx context.Context, fn func() (any, error)) (any, error) {
	r := request{fn: fn, reply: make(chan result, 1)}
	select {
	case s.reqs <- r:
	case <-s.done:
		return nil, ErrClosed
	case <-ctx.Done():
		return nil, ctx.Err()
	}
	select {
	case res := <-r.reply:
		return res.v, res.err
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

// send writes one packet from the owner goroutine once the character is in world.
func (s *Session) send(ctx context.Context, op uint16, body []byte) error {
	_, err := s.do(ctx, func() (any, error) {
		if err := s.inWorld(); err != nil {
			return nil, err
		}
		return nil, s.conn.WritePacket(op, body)
	})
	return err
}

// emit queues e behind events still waiting for names and appends whatever is ready.
// The names of e are asked for at once, so its wait overlaps the waits ahead of it.
func (s *Session) emit(e events.Event) {
	if s.clockFn != nil { // a replay: time and mono_ms follow the capture, mono_ms from its first record
		if e.Time.IsZero() {
			e.Time = s.now()
		}
		if e.MonoMS == 0 {
			e.MonoMS = max(1, s.now().Sub(s.start).Milliseconds()) // 0 would be stamped with the real clock
		}
	}
	events.Stamp(&e)
	s.fillNames(&e)
	party := s.partyInvolved(e.SourceGUID) || s.partyInvolved(e.TargetGUID)
	s.names.waiting = append(s.names.waiting, pendingEvent{e: e, until: s.now().Add(s.opts.NamesWait), party: party})
	s.flushNames(s.now())
}

// appendEvent stores a ready event, flagging it (party, decided at emit) when the player, a party member or one of
// their pets is involved. Secrets are scrubbed here, once the names are final, so nothing
// the server echoes reaches the log, RPC replies or MCP output (ruling P10).
func (s *Session) appendEvent(e events.Event, party bool) {
	s.redact.scrubEvent(&e)
	e.Party = party
	s.log.Append(e)
}

// flushPending appends everything still waiting, with the names known so far.
func (s *Session) flushPending() { s.flushNames(s.now().Add(time.Hour)) }

func (s *Session) dispatch(p worldconn.Packet) error {
	h, ok := s.handlers[p.Op]
	if !ok {
		s.mu.Lock()
		s.unknown[p.Op]++
		s.mu.Unlock()
		return nil
	}
	err := h(p.Body)
	s.checkNPC()
	if err == nil {
		return nil
	}
	var fatal *fatalError
	if errors.As(err, &fatal) {
		return fatal.err
	}
	s.mu.Lock()
	s.parseErrs++
	s.mu.Unlock()
	msg := s.redact.scrub(err.Error())
	if s.redact.contains(p.Body) {
		// The stderr log is kept as captures/m*.log: no dump of a body holding a secret.
		fmt.Fprintf(s.logger, "parse error in %s (0x%03X, %d bytes): %s (body holds a redacted secret; no dump)\n", opcode.Name(p.Op), p.Op, len(p.Body), msg)
	} else {
		dump := p.Body
		if len(dump) > 256 {
			dump = dump[:256]
		}
		fmt.Fprintf(s.logger, "parse error in %s (0x%03X, %d bytes): %s\n%s", opcode.Name(p.Op), p.Op, len(p.Body), msg, hex.Dump(dump))
	}
	s.emit(events.Event{Kind: events.KindParseError, Text: err.Error(),
		Data: map[string]any{"opcode": opcode.Name(p.Op), "length": len(p.Body)}})
	return nil
}

func (s *Session) onTick(now time.Time) error {
	s.tickSpells()
	s.flushNames(now)
	s.prunePending(now)
	if err := s.tickWalk(); err != nil {
		return err
	}
	if err := s.tickFall(); err != nil {
		return err
	}
	if !s.loginRetryAt.IsZero() && !now.Before(s.loginRetryAt) {
		s.loginRetryAt = time.Time{}
		if err := s.sendPlayerLogin(); err != nil {
			return err
		}
	}
	if st := s.State(); st == StatusInWorld || st == StatusLoggingOut {
		if s.pongWait && now.Sub(s.pingSent) > s.opts.PongTimeout {
			return errNoPong
		}
		if !s.nextPing.IsZero() && !now.Before(s.nextPing) {
			s.pingSeq++
			s.mu.Lock()
			latency := s.latency
			s.mu.Unlock()
			if err := s.conn.WritePacket(opcode.CMSGPing, packet.NewWriter().U32(s.pingSeq).U32(latency).Bytes()); err != nil {
				return err
			}
			// Schedule from the send, not from the tick: a tick read late after a loop stall
			// would put the next ping under the server's 27 s floor (WorldSocket.cpp:413-433).
			sent := s.now()
			if !s.pongWait { // the timeout runs from the oldest unanswered ping
				s.pingSent, s.pongWait = sent, true
			}
			s.nextPing = sent.Add(s.opts.PingInterval)
		}
	}
	if s.logout != nil && now.After(s.logoutDeadline) {
		// No answer at all: end the session (Run closes the socket) rather than leave the
		// caller with a character that may or may not still be logging out.
		return ErrLogoutTimeout
	}
	return nil
}

// end records why the session stopped, emits it and wakes a waiting Logout.
func (s *Session) end(err error) error { return s.endLogout(err, "") }

// endLogout is end for a pending logout that ended without a logout: the session event
// and the waiting Logout carry outcome.
func (s *Session) endLogout(err error, outcome LogoutOutcome) error {
	data := map[string]any{"state": string(StatusDisconnected)}
	if outcome != "" {
		data["outcome"] = string(outcome)
	}
	// The event first, with every event still waiting for a name before it, then the state: a reader that sees the session
	// disconnected (Status) finds the events that say why in the log.
	s.emit(events.Event{Kind: events.KindSession, Text: err.Error(), Data: data})
	s.flushPending()
	s.mu.Lock()
	s.state = StatusDisconnected
	s.lastErr = s.redact.scrub(err.Error())
	s.mu.Unlock()
	if s.logout != nil {
		s.logout.finish(outcome, err)
		s.logout = nil
	}
	return err
}

// connLost ends the session after a socket error or a failed tick. A socket that dies
// after the server accepted a pending logout is the server ending the logout its own way
// (issues #9, #18): the logout succeeds as LogoutClosedByServer and Run returns
// ErrClosedAtLogout. Before the acceptance it is LogoutLost (ErrLostAtLogout). Neither
// wraps ErrDisconnected, so neither --reconnect nor the disconnect grace window applies.
func (s *Session) connLost(err error) error {
	switch st := s.State(); {
	case st == StatusLoggedOut:
		return nil
	case errors.Is(err, ErrLogoutTimeout):
		return s.endLogout(err, LogoutTimedOut)
	case st == StatusLoggingOut && !errors.Is(err, errNoPong):
		if s.logoutAccepted {
			return s.closedAtLogout(err)
		}
		return s.endLogout(fmt.Errorf("%w: %v", ErrLostAtLogout, err), LogoutLost)
	}
	return s.end(fmt.Errorf("%w: %v", ErrDisconnected, err))
}

// closedAtLogout records a socket closed during a pending logout as the logout's end.
func (s *Session) closedAtLogout(cause error) error {
	s.emit(events.Event{Kind: events.KindSession, Text: ErrClosedAtLogout.Error(),
		Data: map[string]any{"state": string(StatusLoggedOut), "outcome": string(LogoutClosedByServer)}})
	s.flushPending()
	s.mu.Lock()
	s.state = StatusLoggedOut
	s.lastErr = s.redact.scrub(fmt.Sprintf("%v: %v", ErrClosedAtLogout, cause))
	s.mu.Unlock()
	if s.logout != nil {
		s.logout.finish(LogoutClosedByServer, nil)
		s.logout = nil
	}
	return ErrClosedAtLogout
}

func (s *Session) onCharEnum(body []byte) error {
	chars, err := ParseCharEnum(body)
	if err != nil {
		return &fatalError{err}
	}
	var names []string
	for _, c := range chars {
		if strings.EqualFold(c.Name, s.opts.Character) {
			s.mu.Lock()
			s.self, s.state = c, StatusLoggingIn
			s.mu.Unlock()
			if err := s.sendPlayerLogin(); err != nil {
				return &fatalError{err}
			}
			return nil
		}
		names = append(names, c.Name)
	}
	if s.opts.Create != nil && !s.createSent {
		s.createSent = true
		if err := s.conn.WritePacket(opcode.CMSGCharCreate, s.opts.Create.createBody(s.opts.Character)); err != nil {
			return &fatalError{err}
		}
		return nil
	}
	return &fatalError{fmt.Errorf("character %q is not on this account (characters: %s)", s.opts.Character, strings.Join(names, ", "))}
}

func (s *Session) sendPlayerLogin() error {
	return s.conn.WritePacket(opcode.CMSGPlayerLogin, packet.NewWriter().GUID(s.self.GUID).Bytes())
}

// onLoginFailed retries code 1 a few times: the server also sends it while the character
// is locked for an automatic backup, which the first character list of a session from a
// new IP schedules for characters above AutoPDump.MinCharLevel (core: WorldSocket::HandleAuthSession
// sets the flag, WorldSession::HandleCharEnum schedules the dump, WorldSession::HandlePlayerLoginOpcode
// refuses while World::IsCharacterLocked, and World::AutoPDumpWorker unlocks).
func (s *Session) onLoginFailed(body []byte) error {
	code := -1
	if len(body) > 0 {
		code = int(body[0])
	}
	if code == 1 && s.State() == StatusLoggingIn && s.loginRetries < maxLoginRetries {
		s.loginRetries++
		s.loginRetryAt = s.now().Add(s.opts.LoginRetryDelay)
		fmt.Fprintf(s.logger, "world: SMSG_CHARACTER_LOGIN_FAILED (code 1), character locked (backup?); retrying in %s (%d/%d)\n",
			s.opts.LoginRetryDelay, s.loginRetries, maxLoginRetries)
		return nil
	}
	return &fatalError{fmt.Errorf("SMSG_CHARACTER_LOGIN_FAILED (code %d): character locked or already loading", code)}
}

func (s *Session) onVerifyWorld(body []byte) error {
	r := packet.NewReader(body)
	m, x, y, z, o := r.U32(), r.F32(), r.F32(), r.F32(), r.F32()
	if err := r.Err(); err != nil {
		return &fatalError{fmt.Errorf("SMSG_LOGIN_VERIFY_WORLD: %w", err)}
	}
	s.mu.Lock()
	s.mapID, s.pos, s.state = m, [4]float32{x, y, z, o}, StatusInWorld
	self := s.self
	s.mu.Unlock()
	s.last = lastMove{pos: [3]float32{x, y, z}}
	s.names.setPlayer(self.GUID, self.Name)
	s.nextPing = s.now().Add(s.opts.PingInterval)
	if err := s.conn.WritePacket(opcode.CMSGSetActiveMover, packet.NewWriter().GUID(self.GUID).Bytes()); err != nil {
		return &fatalError{err}
	}
	s.emit(events.Event{Kind: events.KindSession, Source: self.Name, SourceGUID: self.GUID,
		Data: map[string]any{"state": string(StatusInWorld), "map": m}})
	select {
	case <-s.ready:
	default:
		close(s.ready)
	}
	return nil
}

func (s *Session) onPong(body []byte) error {
	r := packet.NewReader(body)
	seq := r.U32()
	if err := r.Err(); err != nil {
		return err
	}
	if s.pongWait && seq <= s.pingSeq {
		s.pongWait = false
		s.mu.Lock()
		s.latency = uint32(time.Since(s.pingSent).Milliseconds())
		s.mu.Unlock()
	}
	return nil
}

// Logout asks for a logout and waits for its end: SMSG_LOGOUT_COMPLETE (LogoutComplete) or
// the server closing the socket after accepting it (LogoutClosedByServer). A refusal is a
// *LogoutError and the session lives on. A socket lost before the acceptance (LogoutLost,
// ErrLostAtLogout) and no answer within Options.LogoutTimeout (LogoutTimedOut,
// ErrLogoutTimeout) end the session and return their outcome with the error.
func (s *Session) Logout(ctx context.Context) (LogoutOutcome, error) {
	v, err := s.do(ctx, func() (any, error) {
		if s.logout != nil {
			return s.logout, nil
		}
		if err := s.inWorld(); err != nil {
			return nil, err
		}
		if err := s.conn.WritePacket(opcode.CMSGLogoutRequest, nil); err != nil {
			return nil, err
		}
		s.logout = &logoutOp{done: make(chan struct{})}
		s.logoutDeadline = s.now().Add(s.opts.LogoutTimeout)
		s.logoutAccepted = false
		s.setState(StatusLoggingOut)
		return s.logout, nil
	})
	if err != nil {
		return "", err
	}
	op := v.(*logoutOp)
	select {
	case <-op.done:
		return op.outcome, op.err
	case <-s.done:
		select {
		case <-op.done:
			return op.outcome, op.err
		default:
			return "", ErrClosed
		}
	case <-ctx.Done():
		return "", ctx.Err()
	}
}

func (s *Session) onLogoutResponse(body []byte) error {
	r := packet.NewReader(body)
	reason, instant := r.U32(), r.U8()
	if err := r.Err(); err != nil {
		return err
	}
	if reason != 0 {
		s.emit(events.Event{Kind: events.KindSession, Data: map[string]any{"state": "logout_refused", "reason": reason}})
		s.setState(StatusInWorld)
		if s.logout != nil {
			s.logout.finish("", &LogoutError{Reason: reason})
			s.logout = nil
		}
		return nil
	}
	s.logoutAccepted = s.logout != nil
	s.emit(events.Event{Kind: events.KindSession, Data: map[string]any{"state": "logout_pending", "instant": instant == 1}})
	return nil
}

func (s *Session) onLogoutComplete([]byte) error {
	s.emit(events.Event{Kind: events.KindSession, Data: map[string]any{"state": string(StatusLoggedOut), "outcome": string(LogoutComplete)}})
	s.flushPending()
	s.setState(StatusLoggedOut)
	if s.logout != nil {
		s.logout.finish(LogoutComplete, nil)
		s.logout = nil
	}
	s.finished = true
	return nil
}
