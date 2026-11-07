package main

import (
	"context"
	"fmt"
	"time"

	"github.com/pjerra/turtle-agent/internal/events"
	"github.com/pjerra/turtle-agent/internal/packet"
	"github.com/pjerra/turtle-agent/internal/rpc"
	"github.com/pjerra/turtle-agent/internal/session"
	"github.com/pjerra/turtle-agent/internal/tbm"
)

type addonParams struct {
	Prefix  string `json:"prefix"`
	Text    string `json:"text"`
	Channel string `json:"channel"`
	To      string `json:"to,omitempty"`
}

type tbmParams struct {
	Command string `json:"command"`
	Via     string `json:"via,omitempty"`
}

type handshakeParams struct {
	Via    string `json:"via,omitempty"`
	WaitMS int    `json:"wait_ms,omitempty"`
}

type channelParams struct {
	Name     string `json:"name"`
	Password string `json:"password,omitempty"`
}

type gossipParams struct {
	GUID   packet.GUID `json:"guid"`
	Option uint32      `json:"option"`
	Code   string      `json:"code,omitempty"`
	WaitMS int         `json:"wait_ms,omitempty"`
}

// taxiParams is a flight master and the route's taxi nodes, source first.
type taxiParams struct {
	GUID  packet.GUID `json:"guid"`
	Nodes []uint32    `json:"nodes"`
}

// gossipResult is the log cursor before the request and, with wait_ms, the first gossip
// or gossip_complete event after it (nil when none arrived in time).
type gossipResult struct {
	Since uint64        `json:"since"`
	Event *events.Event `json:"event"`
}

type sinceResult struct {
	Since uint64 `json:"since"`
}

type handshakeResult struct {
	Since uint64 `json:"since"` // the log cursor before the first command
	Next  uint64 `json:"next"`  // the cursor after the last event read
	tbm.Status
}

// registerSocialMethods wires addon messages, the TBM helpers, channels and gossip to the
// session cur returns at each call (see registerMethodsFor).
func registerSocialMethods(srv *rpc.Server, cur func() *session.Session) {
	log := cur().Log()
	tr := &tbm.Tracker{}
	registerTBMClick(srv, cur, tr)
	srv.Register("addon_send", rpc.Typed(func(ctx context.Context, p addonParams) (empty, error) {
		return empty{}, rpcErr(cur().AddonSend(ctx, p.Channel, p.Prefix, p.Text, p.To))
	}))
	srv.Register("tbm", rpc.Typed(func(ctx context.Context, p tbmParams) (sinceResult, error) {
		since := log.Last()
		return sinceResult{since}, rpcErr(sendTBM(ctx, cur(), p.Via, p.Command))
	}))
	srv.Register("tbm_handshake", rpc.Typed(func(ctx context.Context, p handshakeParams) (handshakeResult, error) {
		res := handshakeResult{Since: log.Last()}
		res.Lines = []string{}
		for _, c := range tbm.Handshake {
			if err := sendTBM(ctx, cur(), p.Via, c); err != nil {
				return res, rpcErr(err)
			}
		}
		tr.Reset(cur())
		tr.NoteRoster(time.Now())
		res.Next = collectTBM(ctx, log, res.Since, waitBound(p.WaitMS, 5000), &res.Status)
		// What the module said about the transport and the bots is what a click goes by.
		key := groupKey(cur())
		for _, l := range res.Lines {
			tr.Fold(l, key, time.Now())
		}
		return res, nil
	}))
	srv.Register("join_channel", rpc.Typed(func(ctx context.Context, p channelParams) (empty, error) {
		return empty{}, rpcErr(cur().JoinChannel(ctx, p.Name, p.Password))
	}))
	srv.Register("leave_channel", rpc.Typed(func(ctx context.Context, p channelParams) (empty, error) {
		return empty{}, rpcErr(cur().LeaveChannel(ctx, p.Name))
	}))
	srv.Register("gossip_hello", rpc.Typed(func(ctx context.Context, p gossipParams) (gossipResult, error) {
		res := gossipResult{Since: log.Last()}
		if err := cur().GossipHello(ctx, p.GUID); err != nil {
			return res, rpcErr(err)
		}
		res.Event = nextGossip(ctx, log, res.Since, p.WaitMS)
		return res, nil
	}))
	srv.Register("taxi", rpc.Typed(func(ctx context.Context, p taxiParams) (sinceResult, error) {
		since := log.Last()
		return sinceResult{since}, rpcErr(cur().ActivateTaxi(ctx, p.GUID, p.Nodes))
	}))
	srv.Register("gossip_select", rpc.Typed(func(ctx context.Context, p gossipParams) (gossipResult, error) {
		res := gossipResult{Since: log.Last()}
		if err := cur().GossipSelect(ctx, p.GUID, p.Option, p.Code); err != nil {
			return res, rpcErr(err)
		}
		res.Event = nextGossip(ctx, log, res.Since, p.WaitMS)
		return res, nil
	}))
}

// gossipKinds are the answers to a gossip hello or select: the menu, its end, or what an
// option opens (a vendor or trainer list, or a quest panel).
var gossipKinds = []string{events.KindGossip, events.KindGossipComplete, events.KindVendorList, events.KindTrainerList,
	events.KindQuestList, events.KindQuestDetails, events.KindQuestRequestItems, events.KindQuestOfferReward}

// nextGossip waits up to waitMS for the first answer event after since; waitMS 0 does not
// look at all.
func nextGossip(ctx context.Context, log *events.Log, since uint64, waitMS int) *events.Event {
	return firstEvent(ctx, log, since, gossipKinds, waitMS)
}

// firstEvent waits up to waitMS for the first event of one of kinds after since, or returns
// nil when none came; waitMS 0 does not look at all.
func firstEvent(ctx context.Context, log *events.Log, since uint64, kinds []string, waitMS int) *events.Event {
	if waitMS <= 0 {
		return nil
	}
	q := events.Query{Since: since, Kinds: kinds, Limit: 1}
	if p := readEvents(ctx, log, q, waitMS); len(p.Events) > 0 {
		return &p.Events[0]
	}
	return nil
}

// sendTBM sends one TBM command on the addon's chat transport (".bot <command>" as say)
// or on its addon transport (prefix TBM on PARTY).
func sendTBM(ctx context.Context, s *session.Session, via, command string) error {
	switch via {
	case "", "chat":
		return s.GM(ctx, tbm.Chat(command))
	case "addon":
		return s.AddonSend(ctx, "party", tbm.Prefix, command, "")
	}
	return fmt.Errorf("via %q: use chat or addon", via)
}

// waitBound is waitMS, or def when unset, capped at 25 s like events.
func waitBound(waitMS, def int) time.Duration {
	if waitMS <= 0 {
		waitMS = def
	}
	if waitMS > 25000 {
		waitMS = 25000
	}
	return time.Duration(waitMS) * time.Millisecond
}

// collectTBM folds the TBM: lines logged after since into st until the roster response is
// complete or wait runs out, and returns the cursor after the last event read.
func collectTBM(ctx context.Context, log *events.Log, since uint64, wait time.Duration, st *tbm.Status) uint64 {
	wctx, cancel := context.WithTimeout(ctx, wait)
	defer cancel()
	cursor := since
	for {
		p := log.Read(events.Query{Since: cursor, Kinds: []string{events.KindSystem, events.KindAddon}, Limit: events.MaxLimit})
		for _, e := range p.Events {
			if line, ok := tbm.Line(e); ok && st.Add(line) {
				return e.Seq
			}
		}
		cursor = p.Next
		if p.More {
			continue
		}
		if log.Wait(wctx, cursor) != nil {
			return cursor
		}
	}
}
