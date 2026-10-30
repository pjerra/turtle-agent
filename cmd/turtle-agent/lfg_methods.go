package main

import (
	"context"
	"fmt"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/pjerra/turtle-agent/internal/events"
	"github.com/pjerra/turtle-agent/internal/lft"
	"github.com/pjerra/turtle-agent/internal/packet"
	"github.com/pjerra/turtle-agent/internal/rpc"
	"github.com/pjerra/turtle-agent/internal/session"
)

// The LFG methods drive Turtle's group finder the way the LFG addon does: addon messages
// prefixed TW_LFG on the guild channel, and answers read back from the event log, folded
// into lft.State. The core answers as the player himself (Player::SendAddonMessage), so a
// line only counts when its sender is the player: a guild-mate's forged S2C_* line has his
// GUID.

type lfgJoinParams struct {
	Instances []string `json:"instances"`
	Roles     string   `json:"roles"`
	Force     bool     `json:"force,omitempty"`
	WaitMS    int      `json:"wait_ms,omitempty"`
}

type lfgRoleAnswerParams struct {
	Roles  string `json:"roles"`
	Force  bool   `json:"force,omitempty"`
	WaitMS int    `json:"wait_ms,omitempty"`
}

type lfgProposalParams struct {
	Accept bool `json:"accept"`
	Force  bool `json:"force,omitempty"`
	WaitMS int  `json:"wait_ms,omitempty"`
}

type lfgWaitParams struct {
	WaitMS int `json:"wait_ms,omitempty"`
}

type lfgStatusParams struct {
	Refresh bool `json:"refresh,omitempty"`
	WaitMS  int  `json:"wait_ms,omitempty"`
}

type lfgAutoParams struct {
	Roles   string `json:"roles,omitempty"`
	Accept  *bool  `json:"accept,omitempty"`
	DelayMS *int   `json:"delay_ms,omitempty"`
	Off     bool   `json:"off,omitempty"`
}

type lfgListingsParams struct {
	Refresh bool   `json:"refresh,omitempty"`
	ID      uint32 `json:"id,omitempty"`
	WaitMS  int    `json:"wait_ms,omitempty"`
}

type lfgListingRoleParams struct {
	Role   string `json:"role"`
	WaitMS int    `json:"wait_ms,omitempty"`
}

// lfgResult is the answer every LFG method gives: the log cursor before the request (the
// point to read events from afterwards), the text sent, and the state the accepted lines
// folded to.
type lfgResult struct {
	Since uint64    `json:"since"`
	Sent  string    `json:"sent"`
	State lft.State `json:"state"`
}

// lfgAutoSettings is what lfg_auto has running, or on=false after off (and after a
// session change, which stops it).
type lfgAutoSettings struct {
	On      bool   `json:"on"`
	Roles   string `json:"roles,omitempty"`
	Accept  *bool  `json:"accept,omitempty"`
	DelayMS int    `json:"delay_ms"`
}

type lfgAutoResult struct {
	lfgResult
	Auto lfgAutoSettings `json:"auto"`
}

// lfgAuto is one running auto answerer: a goroutine tailing the event log.
type lfgAuto struct {
	roles  string
	accept *bool // nil: offers are left alone
	delay  time.Duration
	stop   chan struct{}
	done   chan struct{}
}

// lfgService owns the folded state and the auto answerer behind the lfg_* methods. The
// state is folded from the event log on demand; cursor belongs to the session it was
// folded from, so a reconnect starts empty.
type lfgService struct {
	cur func() *session.Session
	log *events.Log

	mu     sync.Mutex
	sess   *session.Session
	cursor uint64
	state  lft.State
	auto   *lfgAuto
}

// registerLFGMethods wires the group-finder methods to the session cur returns at each
// call. The returned func stops the auto answerer; call it before the process ends.
func registerLFGMethods(srv *rpc.Server, cur func() *session.Session) func() {
	v := &lfgService{cur: cur, log: cur().Log()}
	v.mu.Lock()
	v.sess = cur()
	v.cursor = v.log.Last()
	v.state = lft.State{Phase: lft.PhaseIdle}
	v.mu.Unlock()

	srv.Register("lfg_join", rpc.Typed(func(ctx context.Context, p lfgJoinParams) (lfgResult, error) {
		text, err := lft.QueueJoin(p.Instances, p.Roles)
		if err != nil {
			return lfgResult{}, lfgParamError(err)
		}
		s := cur()
		if err := v.checkRoles(s, p.Roles, p.Force); err != nil {
			return lfgResult{}, err
		}
		since := v.begin(s)
		if err := s.AddonSend(ctx, "guild", lft.Prefix, text, ""); err != nil {
			return lfgResult{Since: since}, rpcErr(err)
		}
		v.wait(ctx, s, since, waitBound(p.WaitMS, 5000), func(m lft.Msg) bool {
			switch m.Kind {
			case "S2C_ROLECHECK_START", "S2C_QUEUE_ERROR", "S2C_QUEUE_JOINED":
				return true
			}
			return false
		})
		return v.result(since, text), nil
	}))

	srv.Register("lfg_role_check_answer", rpc.Typed(func(ctx context.Context, p lfgRoleAnswerParams) (lfgResult, error) {
		s := cur()
		since := v.begin(s)
		if !p.Force {
			if err := lfgRequirePhase(v.snapshot(), lft.PhaseRolecheck, "answer a role check"); err != nil {
				return lfgResult{Since: since}, err
			}
		}
		if err := v.checkRoles(s, p.Roles, p.Force); err != nil {
			return lfgResult{Since: since}, err
		}
		text := lft.RolecheckResponse(p.Roles)
		if err := s.AddonSend(ctx, "guild", lft.Prefix, text, ""); err != nil {
			return lfgResult{Since: since}, rpcErr(err)
		}
		name := s.Status().Character
		v.wait(ctx, s, since, waitBound(p.WaitMS, 5000), func(m lft.Msg) bool {
			switch m.Kind {
			case "S2C_QUEUE_JOINED":
				return true
			case "S2C_ROLECHECK_INFO":
				return len(m.Fields) > 0 && strings.EqualFold(m.Fields[0], name)
			}
			return false
		})
		return v.result(since, text), nil
	}))

	srv.Register("lfg_proposal_answer", rpc.Typed(func(ctx context.Context, p lfgProposalParams) (lfgResult, error) {
		s := cur()
		since := v.begin(s)
		if !p.Force {
			if err := lfgRequirePhase(v.snapshot(), lft.PhaseOffer, "answer the proposal"); err != nil {
				return lfgResult{Since: since}, err
			}
		}
		text := lft.OfferAccept()
		if !p.Accept {
			// The protocol has no decline: leaving the queue is the decline (decision 5).
			text = lft.QueueLeave()
		}
		if err := s.AddonSend(ctx, "guild", lft.Prefix, text, ""); err != nil {
			return lfgResult{Since: since}, rpcErr(err)
		}
		v.wait(ctx, s, since, waitBound(p.WaitMS, 5000), func(m lft.Msg) bool {
			switch m.Kind {
			case "S2C_OFFER_UPDATE_COUNT", "S2C_OFFER_COMPLETE", "S2C_QUEUE_LEFT":
				return true
			}
			return false
		})
		return v.result(since, text), nil
	}))

	srv.Register("lfg_leave", rpc.Typed(func(ctx context.Context, p lfgWaitParams) (lfgResult, error) {
		s := cur()
		since := v.begin(s)
		text := lft.QueueLeave()
		if err := s.AddonSend(ctx, "guild", lft.Prefix, text, ""); err != nil {
			return lfgResult{Since: since}, rpcErr(err)
		}
		v.wait(ctx, s, since, waitBound(p.WaitMS, 5000), func(m lft.Msg) bool {
			return m.Kind == "S2C_QUEUE_LEFT"
		})
		return v.result(since, text), nil
	}))

	srv.Register("lfg_status", rpc.Typed(func(ctx context.Context, p lfgStatusParams) (lfgResult, error) {
		s := cur()
		since := v.begin(s)
		if !p.Refresh {
			return v.result(since, ""), nil
		}
		text := lft.QueueStatus()
		if err := s.AddonSend(ctx, "guild", lft.Prefix, text, ""); err != nil {
			return lfgResult{Since: since}, rpcErr(err)
		}
		v.wait(ctx, s, since, waitBound(p.WaitMS, 5000), func(m lft.Msg) bool {
			return m.Kind == "S2C_UPDATE_QUEUE_STATUS"
		})
		return v.result(since, text), nil
	}))

	srv.Register("lfg_auto", rpc.Typed(func(ctx context.Context, p lfgAutoParams) (lfgAutoResult, error) {
		s := cur()
		since := v.begin(s)
		if p.Off {
			v.stopAuto()
			return lfgAutoResult{lfgResult: v.result(since, "")}, nil
		}
		delay := 1500
		if p.DelayMS != nil {
			delay = *p.DelayMS
		}
		if delay < 0 || delay > 30000 {
			return lfgAutoResult{}, lfgParamError(fmt.Errorf("delay_ms %d is outside 0-30000", delay))
		}
		if p.Roles == "" {
			return lfgAutoResult{}, lfgParamError(fmt.Errorf("roles are empty"))
		}
		if err := v.checkRoles(s, p.Roles, false); err != nil {
			return lfgAutoResult{}, err
		}
		var accept *bool
		if p.Accept != nil {
			x := *p.Accept
			accept = &x
		}
		a := &lfgAuto{roles: p.Roles, accept: accept, delay: time.Duration(delay) * time.Millisecond, stop: make(chan struct{}), done: make(chan struct{})}
		v.mu.Lock()
		old := v.auto
		v.auto = a
		v.mu.Unlock()
		if old != nil {
			stopAuto(old)
		}
		go v.runAuto(a, s, s.Log(), since)
		auto := lfgAutoSettings{On: true, Roles: p.Roles, Accept: accept, DelayMS: delay}
		return lfgAutoResult{lfgResult: v.result(since, ""), Auto: auto}, nil
	}))

	srv.Register("lfg_listings", rpc.Typed(func(ctx context.Context, p lfgListingsParams) (lfgResult, error) {
		s := cur()
		since := v.begin(s)
		sent := ""
		if p.Refresh {
			text := lft.GroupsList()
			if err := s.AddonSend(ctx, "guild", lft.Prefix, text, ""); err != nil {
				return lfgResult{Since: since}, rpcErr(err)
			}
			sent = text
			if p.ID == 0 {
				v.wait(ctx, s, since, waitBound(p.WaitMS, 5000), func(m lft.Msg) bool {
					return m.Kind == "S2C_GROUPS_LIST_UPDATE" && len(m.Fields) > 0 && m.Fields[0] == "end"
				})
				return v.result(since, sent), nil
			}
		}
		if p.ID != 0 {
			text := lft.GroupDetails(p.ID)
			if err := s.AddonSend(ctx, "guild", lft.Prefix, text, ""); err != nil {
				return lfgResult{Since: since}, rpcErr(err)
			}
			sent = text
			id := strconv.FormatUint(uint64(p.ID), 10)
			v.wait(ctx, s, since, waitBound(p.WaitMS, 5000), func(m lft.Msg) bool {
				return m.Kind == "S2C_UPDATE_GROUP" && len(m.Fields) >= 2 && m.Fields[0] == id && m.Fields[1] == "end"
			})
		}
		return v.result(since, sent), nil
	}))

	srv.Register("lfg_listing_role", rpc.Typed(func(ctx context.Context, p lfgListingRoleParams) (lfgResult, error) {
		s := cur()
		since := v.begin(s)
		text := "C2C_ROLECHECK_RESPONSE;" + lft.ListingRole(p.Role)
		// The addon confirms a listed group's role on the group channel, not the guild one.
		if err := s.AddonSend(ctx, "party", lft.Prefix, text, ""); err != nil {
			return lfgResult{Since: since}, rpcErr(err)
		}
		v.wait(ctx, s, since, waitBound(p.WaitMS, 5000), func(m lft.Msg) bool {
			return m.Kind == "S2C_GROUPS_LIST_UPDATE"
		})
		return v.result(since, text), nil
	}))

	return v.close
}

func lfgParamError(err error) error {
	return &rpc.Error{Code: rpc.CodeInvalidParams, Message: err.Error()}
}

// lfgRequirePhase refuses a method the conversation is not ready for unless force is set.
func lfgRequirePhase(st lft.State, want lft.Phase, what string) error {
	if st.Phase == want {
		return nil
	}
	return &rpc.Error{Code: rpc.CodeGame, Message: fmt.Sprintf("phase is %s, not %s: cannot %s (force overrides)",
		st.Phase, want, what)}
}

// begin folds everything the log holds for the current session and returns the cursor
// position before the request.
func (v *lfgService) begin(s *session.Session) uint64 {
	v.mu.Lock()
	defer v.mu.Unlock()
	v.syncLocked(s)
	v.foldLocked(0, nil)
	return v.log.Last()
}

// syncLocked starts a new state when cur() hands out another session: the old
// conversation and its cursor belong to the old session, and the auto answerer stops.
func (v *lfgService) syncLocked(s *session.Session) bool {
	if v.sess == s {
		return false
	}
	if a := v.detachAutoLocked(); a != nil {
		close(a.stop)
	}
	v.sess = s
	v.log = s.Log()
	v.cursor = v.log.Last()
	v.state = lft.State{Phase: lft.PhaseIdle}
	return true
}

// foldLocked folds every accepted addon line after the cursor. When match is set it
// reports whether one of the lines newer than after answers the request.
func (v *lfgService) foldLocked(after uint64, match func(lft.Msg) bool) bool {
	self := v.sess.Status().GUID
	matched := false
	for {
		p := v.log.Read(events.Query{Since: v.cursor, Kinds: []string{events.KindAddon}, Limit: events.MaxLimit})
		for _, e := range p.Events {
			m, ok := lfgLine(e, self)
			if !ok {
				continue
			}
			v.state.Fold(m, e.Time)
			if match != nil && e.Seq > after && match(m) {
				matched = true
			}
		}
		v.cursor = p.Next
		if !p.More {
			break
		}
	}
	return matched
}

// lfgLine is the spoof guard: an addon line on the guild channel, prefix TW_LFG, sent by
// the player himself, naming an S2C_* kind.
func lfgLine(e events.Event, self packet.GUID) (lft.Msg, bool) {
	if self == 0 || e.SourceGUID != self {
		return lft.Msg{}, false
	}
	if typ, _ := e.Data["type"].(string); typ != "guild" {
		return lft.Msg{}, false
	}
	if prefix, _ := e.Data["prefix"].(string); prefix != lft.Prefix {
		return lft.Msg{}, false
	}
	m := lft.Parse(e.Text)
	if !strings.HasPrefix(m.Kind, "S2C_") {
		return lft.Msg{}, false
	}
	return m, true
}

// wait folds accepted lines until one answers the request, wait runs out, or the session
// changes.
func (v *lfgService) wait(ctx context.Context, s *session.Session, since uint64, wait time.Duration, match func(lft.Msg) bool) {
	if wait <= 0 {
		return
	}
	deadline := time.Now().Add(wait)
	for {
		v.mu.Lock()
		if v.syncLocked(s) {
			v.mu.Unlock()
			return
		}
		matched := v.foldLocked(since, match)
		cursor, log := v.cursor, v.log
		v.mu.Unlock()
		if matched {
			return
		}
		remain := time.Until(deadline)
		if remain <= 0 {
			return
		}
		wctx, cancel := context.WithTimeout(ctx, remain)
		err := log.Wait(wctx, cursor)
		cancel()
		if err != nil {
			return
		}
	}
}

// snapshot is a deep copy, safe to marshal after the lock is released.
func (v *lfgService) snapshot() lft.State {
	v.mu.Lock()
	defer v.mu.Unlock()
	return cloneLFTState(v.state)
}

func (v *lfgService) result(since uint64, sent string) lfgResult {
	v.mu.Lock()
	defer v.mu.Unlock()
	return lfgResult{Since: since, Sent: sent, State: cloneLFTState(v.state)}
}

// checkRoles mirrors the core's AllowedRoleMask (LFTQeueue.cpp:717-736): an answer naming a
// role the class cannot take would be silently ignored and the role check would time out.
func (v *lfgService) checkRoles(s *session.Session, roles string, force bool) error {
	mask := lft.Mask(roles)
	if mask == 0 {
		return lfgParamError(fmt.Errorf("roles %q name none of t, h, d", roles))
	}
	if force {
		return nil
	}
	class := lfgSelfClass(s)
	if class == 0 {
		return &rpc.Error{Code: rpc.CodeGame, Message: "the character's class is not known yet"}
	}
	allowed := lft.AllowedRoles(class)
	if mask&^lft.Mask(allowed) != 0 {
		return &rpc.Error{Code: rpc.CodeGame, Message: fmt.Sprintf("roles %q are not allowed for a %s (allowed: %s)",
			roleText(mask), lfgClassName(class), allowed)}
	}
	return nil
}

// lfgSelfClass is the character's class id from its own unit row, 0 when the row is not
// in the store yet.
func lfgSelfClass(s *session.Session) uint8 {
	u, err := s.Unit(s.Status().GUID, "")
	if err != nil {
		return 0
	}
	return u.Class
}

func lfgClassName(class uint8) string {
	if name, ok := classNames[class]; ok {
		return name
	}
	return fmt.Sprintf("class %d", class)
}

func roleText(mask uint8) string {
	var b strings.Builder
	if mask&1 != 0 {
		b.WriteByte('t')
	}
	if mask&2 != 0 {
		b.WriteByte('h')
	}
	if mask&4 != 0 {
		b.WriteByte('d')
	}
	return b.String()
}

func cloneLFTState(in lft.State) lft.State {
	out := in
	out.Instances = append([]string(nil), in.Instances...)
	if in.Responses != nil {
		out.Responses = make(map[string]string, len(in.Responses))
		for k, v := range in.Responses {
			out.Responses[k] = v
		}
	}
	if in.Offer != nil {
		offer := *in.Offer
		out.Offer = &offer
	}
	if in.Listings != nil {
		out.Listings = make(map[uint32]lft.Listing, len(in.Listings))
		for id, listing := range in.Listings {
			listing.Signups = append([]lft.Signup(nil), listing.Signups...)
			out.Listings[id] = listing
		}
	}
	out.History = append([]lft.Line(nil), in.History...)
	return out
}

// stopAuto detaches the running auto answerer and waits for its goroutine to end.
func (v *lfgService) stopAuto() {
	v.mu.Lock()
	a := v.detachAutoLocked()
	v.mu.Unlock()
	if a != nil {
		stopAuto(a)
	}
}

func (v *lfgService) detachAutoLocked() *lfgAuto {
	a := v.auto
	v.auto = nil
	return a
}

func stopAuto(a *lfgAuto) {
	close(a.stop)
	select {
	case <-a.done:
	case <-time.After(2 * time.Second):
	}
}

func (v *lfgService) close() {
	v.stopAuto()
}

// runAuto tails the event log and answers as a player would, after delay. Its sends put a
// line in the state's History, since the log keeps only what the server said.
func (v *lfgService) runAuto(a *lfgAuto, s *session.Session, log *events.Log, cursor uint64) {
	defer close(a.done)
	tick := time.NewTicker(20 * time.Millisecond)
	defer tick.Stop()
	for {
		select {
		case <-a.stop:
			return
		case <-s.Done():
			return
		case <-tick.C:
		}
		if v.cur() != s || s.State() != session.StatusInWorld {
			return
		}
		self := s.Status().GUID
		for {
			p := log.Read(events.Query{Since: cursor, Kinds: []string{events.KindAddon}, Limit: events.MaxLimit})
			for _, e := range p.Events {
				m, ok := lfgLine(e, self)
				if !ok {
					continue
				}
				var text string
				switch m.Kind {
				case "S2C_ROLECHECK_START":
					if a.roles == "" {
						continue
					}
					text = lft.RolecheckResponse(a.roles)
				case "S2C_OFFER_NEW":
					if a.accept == nil {
						continue
					}
					if *a.accept {
						text = lft.OfferAccept()
					} else {
						text = lft.QueueLeave()
					}
				default:
					continue
				}
				if !a.afterDelay() {
					return
				}
				ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
				err := s.AddonSend(ctx, "guild", lft.Prefix, text, "")
				cancel()
				if err != nil {
					continue
				}
				v.noteAuto(s, text)
			}
			cursor = p.Next
			if !p.More {
				break
			}
		}
	}
}

// afterDelay is the human-like pause before an auto answer; false when the answerer was
// stopped meanwhile.
func (a *lfgAuto) afterDelay() bool {
	if a.delay <= 0 {
		select {
		case <-a.stop:
			return false
		default:
			return true
		}
	}
	t := time.NewTimer(a.delay)
	defer t.Stop()
	select {
	case <-a.stop:
		return false
	case <-t.C:
		return true
	}
}

func (v *lfgService) noteAuto(s *session.Session, text string) {
	v.mu.Lock()
	defer v.mu.Unlock()
	if v.sess != s {
		return
	}
	v.state.History = append(v.state.History, lft.Line{At: time.Now(), Text: "auto: " + text})
	if len(v.state.History) > 50 {
		v.state.History = v.state.History[len(v.state.History)-50:]
	}
}
