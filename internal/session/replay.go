package session

import (
	"errors"
	"fmt"
	"math"
	"time"

	"github.com/pjerra/turtle-agent/internal/capture"
	"github.com/pjerra/turtle-agent/internal/events"
	"github.com/pjerra/turtle-agent/internal/opcode"
	"github.com/pjerra/turtle-agent/internal/packet"
	"github.com/pjerra/turtle-agent/internal/worldconn"
)

// replayStep is the spacing of the ticks a replay runs while something time-driven is
// pending, as the live loop's own ticker does.
const replayStep = 100 * time.Millisecond

// ReplayReport says what a replay saw besides the events.
type ReplayReport struct {
	Character string         // the character the capture logged in
	Records   int            // records read
	Redacted  int            // server packets stored without a body, which cannot be decoded
	Handled   int            // server packets given to a handler
	Unknown   map[string]int // server packets without a handler, by opcode name
	Ended     string         // why the replay stopped before the last record, "" if it did not
	First     time.Time      // time of the first and last record
	Last      time.Time
}

// Replay runs the server packets of a capture through a session with no socket and returns
// the event log that session built. The packets go through the same handlers as live ones,
// in order, under a clock that follows the capture's record times, so names, timeouts and
// event times come out as they did live. The capture holds decrypted packets, so the header
// cipher plays no part.
//
// Requests the agent sent (CMSG_SELL_ITEM, the quest accepts and so on) are noted as the
// RPC methods noted them, since some events (vendor_sell, quest_accepted) are inferred from
// them. Not reproduced: events from text the capture redacted, a final "disconnected" event
// (a capture does not record how the socket ended), and flights the agent simulated after
// walking by RPC (a knockback restarts from the last position the replay knows).
//
// opts.Character empty: the character of the first CMSG_PLAYER_LOGIN. opts.Log nil: an
// unbounded log.
func Replay(recs []capture.Record, opts Options) (*events.Log, ReplayReport, error) {
	return ReplayWith(recs, opts, nil)
}

// ReplayWith is Replay that calls observe after each record has been handled, with the session as the replay has
// it at that record's time (a client packet is only noted, never applied: the session's own position does not
// follow the agent's walking, so a reader that wants it parses the client movement packets itself). observe must
// only read the session. A record whose body was redacted reaches it too, unhandled.
func ReplayWith(recs []capture.Record, opts Options, observe func(rec capture.Record, s *Session)) (*events.Log, ReplayReport, error) {
	rep := ReplayReport{Records: len(recs), Unknown: map[string]int{}}
	if opts.Character == "" {
		name, err := loggedInCharacter(recs)
		if err != nil {
			return nil, rep, err
		}
		opts.Character = name
	}
	rep.Character = opts.Character
	if opts.Log == nil {
		opts.Log = events.New(math.MaxInt)
	}
	// No navmesh: a replay has no loop to hand an off-loop floor search back to (traceFall,
	// dropToFloor), and the capture does not hold the mesh the live run had.
	opts.Planner = nil
	s := New(worldconn.NewOffline(), opts)
	var clock time.Time
	s.clockFn = func() time.Time { return clock }
	if opts.Store == nil { // a store of the replay's own follows the capture's clock, so a moving unit is where it was then
		s.store.SetClock(func() time.Time { return clock })
	}
	if len(recs) > 0 {
		// The session's epoch is the capture's start: client times and mono_ms count from it,
		// and no duration is negative.
		clock, s.start = recs[0].Time, recs[0].Time
	}
	var prev time.Time
	for i, rec := range recs {
		if i == 0 {
			rep.First = rec.Time
			clock = rec.Time
		}
		rep.Last = rec.Time
		// Run the ticks a live session would have run since the last record, while there
		// is something for them to do.
		if !prev.IsZero() {
			for t := prev.Add(replayStep); t.Before(rec.Time) && s.tickPending(); t = t.Add(replayStep) {
				clock = t
				s.replayTick()
			}
		}
		prev, clock = rec.Time, rec.Time
		s.replayTick()
		switch rec.Dir {
		case worldconn.ClientToServer:
			s.observeClient(rec.Op, rec.Body)
			if observe != nil {
				observe(rec, s)
			}
			continue
		case worldconn.ServerToClient:
		default:
			continue
		}
		if rec.Redacted {
			rep.Redacted++
			if observe != nil {
				observe(rec, s)
			}
			continue
		}
		if _, ok := s.handlers[rec.Op]; ok {
			rep.Handled++
		} else {
			rep.Unknown[opcode.Name(rec.Op)]++
		}
		stop, err := s.handle(worldconn.Packet{Op: rec.Op, Body: rec.Body})
		if observe != nil {
			observe(rec, s)
		}
		if stop {
			if err != nil {
				rep.Ended = err.Error()
			}
			break
		}
	}
	s.flushPending()
	return opts.Log, rep, nil
}

// loggedInCharacter names the character the capture picked: the GUID of the first
// CMSG_PLAYER_LOGIN, looked up in the SMSG_CHAR_ENUM before it.
func loggedInCharacter(recs []capture.Record) (string, error) {
	var chars []Character
	for _, rec := range recs {
		switch {
		case rec.Op == opcode.SMSGCharEnum && rec.Dir == worldconn.ServerToClient && !rec.Redacted:
			c, err := ParseCharEnum(rec.Body)
			if err != nil {
				return "", fmt.Errorf("replay: %w", err)
			}
			chars = c
		case rec.Op == opcode.CMSGPlayerLogin && rec.Dir == worldconn.ClientToServer:
			r := packet.NewReader(rec.Body)
			g := r.GUID()
			if err := r.Err(); err != nil {
				return "", fmt.Errorf("replay: CMSG_PLAYER_LOGIN: %w", err)
			}
			for _, c := range chars {
				if c.GUID == g {
					return c.Name, nil
				}
			}
			return "", errors.New("replay: the character of CMSG_PLAYER_LOGIN is not in the capture's character list")
		}
	}
	return "", errors.New("replay: the capture has no CMSG_PLAYER_LOGIN, so no character logged in (pass a character)")
}

// tickPending reports whether a tick would do anything now: events waiting for names, a
// flight to land, or a cooldown to end.
func (s *Session) tickPending() bool {
	return len(s.names.waiting) > 0 || s.fall != nil || s.spellState.Pending()
}

// replayTick is the part of onTick that changes events: cooldowns ending, name timeouts, expiring
// requests and the fall simulation. Pings, logout deadlines and walks have no say in a replay.
func (s *Session) replayTick() {
	s.tickSpells()
	s.flushNames(s.now())
	s.prunePending(s.now())
	_ = s.tickFall() // the offline connection takes every write
}

// observeClient notes the requests the RPC methods note (see vendor.go and quests.go) from
// the client packets a capture holds. A packet that does not parse is ignored.
func (s *Session) observeClient(op uint16, body []byte) {
	r := packet.NewReader(body)
	switch op {
	case opcode.CMSGBuyItem, opcode.CMSGBuyItemInSlot:
		vendor, entry := r.GUID(), r.U32()
		var count uint8
		if op == opcode.CMSGBuyItem {
			count = r.U8()
		} else {
			r.GUID()
			r.U8()
			count = r.U8()
		}
		if r.Err() == nil {
			s.noteBuy(vendor, entry, count)
		}
	case opcode.CMSGSellItem:
		vendor, item, count := r.GUID(), r.GUID(), r.U8()
		if r.Err() != nil {
			return
		}
		for _, it := range s.Items() {
			if it.GUID == item {
				s.noteSell(vendor, it, uint32(count))
				return
			}
		}
	case opcode.CMSGQuestgiverAcceptQuest:
		if npc, quest := r.GUID(), r.U32(); r.Err() == nil {
			s.noteAccept(npc, quest)
		}
	case opcode.CMSGQuestgiverCompleteQuest:
		if npc, quest := r.GUID(), r.U32(); r.Err() == nil {
			s.noteComplete(npc, quest)
		}
	case opcode.CMSGQuestgiverChooseReward:
		if npc, quest := r.GUID(), r.U32(); r.Err() == nil {
			s.noteReward(npc, quest)
		}
	case opcode.CMSGQuestlogRemoveQuest:
		slot := r.U8()
		if r.Err() != nil {
			return
		}
		for _, q := range s.QuestLog() {
			if q.Slot == slot {
				s.noteAbandon(q.ID)
				return
			}
		}
	}
}
