package session

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/pjerra/turtle-agent/internal/events"
	"github.com/pjerra/turtle-agent/internal/game"
	"github.com/pjerra/turtle-agent/internal/opcode"
)

// Death, resurrection and the corpse run (README "Death and the corpse run"). The packets are the ones a 1.12 client
// sends; core cites are in internal/game/death.go.

// resurrectOfferTTL is how long an offer stays acceptable: the client's dialog runs 60 s. The server keeps the request
// until it is answered, replaced or the player lives, and ignores an answer to one it no longer holds.
const resurrectOfferTTL = 60 * time.Second

// corpseQueryWait bounds the wait for the answer to MSG_CORPSE_QUERY.
const corpseQueryWait = 5 * time.Second

type resurrectOffer struct {
	game.ResurrectRequest
	at time.Time
}

// DeathInfo is the player's death state: alive, dead (health 0, before the spirit was released) or ghost.
type DeathInfo struct {
	Dead  bool `json:"dead"`  // health 0: the body lies where the player died
	Ghost bool `json:"ghost"` // the spirit was released (PLAYER_FLAGS_GHOST); health is 1
	// Resurrect is the offer of a resurrection spell cast at the player that is still open (SMSG_RESURRECT_REQUEST),
	// nil when none; accept_resurrect answers it.
	Resurrect *game.ResurrectRequest `json:"resurrect,omitempty"`
	// ReclaimInMS is how long until the corpse may be reclaimed (SMSG_CORPSE_RECLAIM_DELAY after the release), 0 when it
	// may be, or when the server named no delay.
	ReclaimInMS int64 `json:"reclaim_in_ms"`
}

func (s *Session) registerDeath() {
	s.handlers[opcode.SMSGResurrectRequest] = s.onResurrectRequest
	s.handlers[opcode.SMSGCorpseReclaimDelay] = s.onCorpseReclaimDelay
	s.handlers[opcode.MSGCorpseQuery] = s.onCorpseQuery
}

// selfDown reports the player's own death state from the object store.
func (s *Session) selfDown() (dead, ghost bool) {
	st := s.Status()
	u, ok := s.store.Get(st.GUID, st.X, st.Y, st.Z)
	if !ok {
		return false, false
	}
	return u.Dead, u.Ghost
}

// Death returns the player's death state and any open resurrection offer.
func (s *Session) Death() DeathInfo {
	dead, ghost := s.selfDown()
	s.mu.Lock()
	defer s.mu.Unlock()
	info := DeathInfo{Dead: dead, Ghost: ghost}
	if !dead && !ghost { // alive again: an old offer and delay mean nothing
		s.offer, s.reclaimAt = nil, time.Time{}
		return info
	}
	now := s.now()
	if o := s.offer; o != nil && now.Sub(o.at) < resurrectOfferTTL {
		r := o.ResurrectRequest
		info.Resurrect = &r
	}
	if left := s.reclaimAt.Sub(now); left > 0 {
		info.ReclaimInMS = left.Milliseconds()
	}
	return info
}

func (s *Session) onResurrectRequest(body []byte) error {
	q, err := game.ParseResurrectRequest(body)
	if err != nil {
		return err
	}
	s.mu.Lock()
	s.offer = &resurrectOffer{ResurrectRequest: q, at: s.now()}
	s.mu.Unlock()
	data := map[string]any{"caster": q.Caster.String(), "sickness": q.Sickness, "timer": q.Timer}
	if q.Name != "" {
		data["name"] = q.Name
	}
	s.emit(events.Event{Kind: events.KindResurrectRequest, SourceGUID: q.Caster, TargetGUID: s.selfGUID(), Data: data})
	return nil
}

func (s *Session) onCorpseReclaimDelay(body []byte) error {
	ms, err := game.ParseCorpseReclaimDelay(body)
	if err != nil {
		return err
	}
	s.mu.Lock()
	s.reclaimAt = s.now().Add(time.Duration(ms) * time.Millisecond)
	s.mu.Unlock()
	s.emit(events.Event{Kind: events.KindCorpseReclaimDelay, SourceGUID: s.selfGUID(), Data: map[string]any{"ms": ms}})
	return nil
}

func (s *Session) onCorpseQuery(body []byte) error {
	c, err := game.ParseCorpseQuery(body)
	if err != nil {
		return err
	}
	// The server also sends a "no corpse" when the corpse turns to bones; a waiter gets whatever arrives first.
	for _, ch := range s.corpseWaiters {
		ch <- c
	}
	s.corpseWaiters = nil
	return nil
}

// AcceptResurrect answers the open resurrection offer with yes (CMSG_RESURRECT_RESPONSE). It fails without an offer or
// when the player is alive. The server revives the player when the answer is valid; the caller sees it in Death.
func (s *Session) AcceptResurrect(ctx context.Context) (game.ResurrectRequest, error) {
	v, err := s.do(ctx, func() (any, error) {
		if err := s.inWorld(); err != nil {
			return nil, err
		}
		dead, ghost := s.selfDown()
		if !dead && !ghost {
			return nil, errors.New("accept_resurrect: the player is alive")
		}
		s.mu.Lock()
		o := s.offer
		s.mu.Unlock()
		if o == nil || s.now().Sub(o.at) >= resurrectOfferTTL {
			return nil, errors.New("accept_resurrect: no resurrection is on offer (SMSG_RESURRECT_REQUEST)")
		}
		if err := s.conn.WritePacket(opcode.CMSGResurrectResponse, game.BuildResurrectResponse(o.Caster, true)); err != nil {
			return nil, err
		}
		s.mu.Lock()
		s.offer = nil
		s.mu.Unlock()
		return o.ResurrectRequest, nil
	})
	if err != nil {
		return game.ResurrectRequest{}, err
	}
	return v.(game.ResurrectRequest), nil
}

// ReleaseSpirit sends CMSG_REPOP_REQUEST: the spirit leaves the body and the player is moved to the graveyard as a ghost.
// The server ignores it for a player who is alive or already a ghost, so both are refused here.
func (s *Session) ReleaseSpirit(ctx context.Context) error {
	_, err := s.do(ctx, func() (any, error) {
		if err := s.inWorld(); err != nil {
			return nil, err
		}
		dead, ghost := s.selfDown()
		switch {
		case ghost:
			return nil, errors.New("release_spirit: the spirit is already released")
		case !dead:
			return nil, errors.New("release_spirit: the player is alive")
		}
		return nil, s.conn.WritePacket(opcode.CMSGRepopRequest, game.BuildRepopRequest())
	})
	return err
}

// CorpseQuery asks where the corpse is (MSG_CORPSE_QUERY) and waits for the answer.
func (s *Session) CorpseQuery(ctx context.Context) (game.Corpse, error) {
	v, err := s.do(ctx, func() (any, error) {
		if err := s.inWorld(); err != nil {
			return nil, err
		}
		ch := make(chan game.Corpse, 1)
		s.corpseWaiters = append(s.corpseWaiters, ch)
		if err := s.conn.WritePacket(opcode.MSGCorpseQuery, game.BuildCorpseQuery()); err != nil {
			return nil, err
		}
		return ch, nil
	})
	if err != nil {
		return game.Corpse{}, err
	}
	ch := v.(chan game.Corpse)
	t := time.NewTimer(corpseQueryWait)
	defer t.Stop()
	select {
	case c := <-ch:
		return c, nil
	case <-t.C:
		return game.Corpse{}, fmt.Errorf("corpse_query: no MSG_CORPSE_QUERY answer within %s", corpseQueryWait)
	case <-s.done:
		return game.Corpse{}, ErrClosed
	case <-ctx.Done():
		return game.Corpse{}, ctx.Err()
	}
}

// ReclaimCorpse sends CMSG_RECLAIM_CORPSE. The ghost must be within game.CorpseReclaimRadius of the corpse and past the
// reclaim delay, or the server ignores the packet; the delay is checked here (it is known), the distance is the caller's
// (corpse_query gives the position).
func (s *Session) ReclaimCorpse(ctx context.Context) error {
	_, err := s.do(ctx, func() (any, error) {
		if err := s.inWorld(); err != nil {
			return nil, err
		}
		dead, ghost := s.selfDown()
		if !ghost {
			if dead {
				return nil, errors.New("reclaim_corpse: the spirit is not released yet")
			}
			return nil, errors.New("reclaim_corpse: the player is alive")
		}
		s.mu.Lock()
		left := s.reclaimAt.Sub(s.now())
		s.mu.Unlock()
		if left > 0 {
			return nil, fmt.Errorf("reclaim_corpse: the reclaim delay has %.0f s left", left.Seconds())
		}
		return nil, s.conn.WritePacket(opcode.CMSGReclaimCorpse, game.BuildReclaimCorpse(s.store.OwnCorpse(s.selfGUID())))
	})
	return err
}

// AreaTrigger sends CMSG_AREATRIGGER for trigger id, as the client does when the player steps into its volume. The server
// checks the position itself (within 5 yd of the volume) and teleports the player for a portal trigger.
func (s *Session) AreaTrigger(ctx context.Context, id uint32) error {
	if id == 0 {
		return errors.New("area_trigger: needs a trigger id")
	}
	return s.send(ctx, opcode.CMSGAreatrigger, game.BuildAreaTrigger(id))
}
