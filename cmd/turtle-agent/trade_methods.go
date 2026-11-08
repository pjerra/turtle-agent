package main

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/pjerra/turtle-agent/internal/packet"
	"github.com/pjerra/turtle-agent/internal/rpc"
	"github.com/pjerra/turtle-agent/internal/session"
)

type tradeParams struct {
	To         string                 `json:"to"`                     // a player's name, or a guid ("0x0000000000001234")
	Items      []session.TradeItemReq `json:"items,omitempty"`        // [{"item": entry, "count": n}]; count defaults to 1
	Gold       uint32                 `json:"gold,omitempty"`         // copper
	WaitMS     int                    `json:"wait_ms,omitempty"`      // for the other side to accept; 12 s when omitted
	OpenWaitMS int                    `json:"open_wait_ms,omitempty"` // for the window to open; 8 s when omitted
}

// playerRef reads the "to" of trade: a guid when it is "0x..." or all digits (a character name has no digits),
// else a name.
func playerRef(to string) (packet.GUID, string, error) { return playerRefField("to", to) }

// playerRefField is playerRef for the parameter called field ("to" of trade and trade_open, "from" of trade_expect).
func playerRefField(field, to string) (packet.GUID, string, error) {
	to = strings.TrimSpace(to)
	if to == "" {
		return 0, "", fmt.Errorf("trade needs %q: a player's name or guid", field)
	}
	if strings.HasPrefix(strings.ToLower(to), "0x") || strings.Trim(to, "0123456789") == "" {
		g, err := packet.ParseGUID(to)
		return g, "", err
	}
	return 0, to, nil
}

// tradeRequest turns the RPC parameters into a session request; a bad "to" is an error.
func tradeRequest(p tradeParams) (session.TradeRequest, error) {
	g, name, err := playerRef(p.To)
	if err != nil {
		return session.TradeRequest{}, err
	}
	return session.TradeRequest{To: g, Name: name, Items: p.Items, Gold: p.Gold,
		Wait: time.Duration(p.WaitMS) * time.Millisecond, OpenWait: time.Duration(p.OpenWaitMS) * time.Millisecond}, nil
}

// registerTradeMethods wires trade, which hands items and gold to a player in trade range.
func registerTradeMethods(srv *rpc.Server, cur func() *session.Session) {
	srv.Register("trade", rpc.Typed(func(ctx context.Context, p tradeParams) (session.TradeResult, error) {
		req, err := tradeRequest(p)
		if err != nil {
			return session.TradeResult{}, rpcErr(err)
		}
		r, err := cur().Trade(ctx, req)
		return r, rpcErr(err)
	}))
	registerTradeStepMethods(srv, cur)
}

type tradeOpenParams struct {
	To         string `json:"to"`                     // a player's name in view, or a guid
	OpenWaitMS int    `json:"open_wait_ms,omitempty"` // for the window to open; 8 s when omitted
}

type tradeExpectParams struct {
	From   string `json:"from"`              // a player's name, or a guid
	WaitMS int    `json:"wait_ms,omitempty"` // for the request to come and the window to open; 15 s when omitted
}

type tradeSetItemParams struct {
	TradeSlot *uint8 `json:"trade_slot"` // 0-6; 6 is the slot that is not traded
	ItemEntry uint32 `json:"item_entry,omitempty"`
	Bag       *uint8 `json:"bag,omitempty"`
	Slot      *uint8 `json:"slot,omitempty"`
}

type tradeAcceptParams struct {
	WaitMS int `json:"wait_ms,omitempty"` // for the other side to accept; 12 s when omitted
}

// registerTradeStepMethods wires the step trade: trade_open and trade_expect open a window that stays open, trade_set_item,
// cast (with trade_slot) and trade_accept drive it, trade_status shows it and trade_cancel closes it.
func registerTradeStepMethods(srv *rpc.Server, cur func() *session.Session) {
	srv.Register("trade_open", rpc.Typed(func(ctx context.Context, p tradeOpenParams) (session.TradeStepState, error) {
		g, name, err := playerRefField("to", p.To)
		if err != nil {
			return session.TradeStepState{}, rpcErr(err)
		}
		st, err := cur().TradeOpen(ctx, g, name, time.Duration(p.OpenWaitMS)*time.Millisecond)
		return st, rpcErr(err)
	}))
	srv.Register("trade_expect", rpc.Typed(func(ctx context.Context, p tradeExpectParams) (session.TradeStepState, error) {
		g, name, err := playerRefField("from", p.From)
		if err != nil {
			return session.TradeStepState{}, rpcErr(err)
		}
		st, err := cur().TradeExpect(ctx, g, name, time.Duration(p.WaitMS)*time.Millisecond)
		return st, rpcErr(err)
	}))
	srv.Register("trade_set_item", rpc.Typed(func(ctx context.Context, p tradeSetItemParams) (session.TradeSetResult, error) {
		if p.TradeSlot == nil || *p.TradeSlot > 6 {
			return session.TradeSetResult{}, &rpc.Error{Code: rpc.CodeInvalidParams, Message: "invalid params: trade_set_item needs trade_slot 0-6"}
		}
		r, err := cur().TradeSetItem(ctx, *p.TradeSlot, session.EquipRef{Entry: p.ItemEntry, Bag: p.Bag, Slot: p.Slot})
		return r, rpcErr(err)
	}))
	srv.Register("trade_accept", rpc.Typed(func(ctx context.Context, p tradeAcceptParams) (session.TradeResult, error) {
		r, err := cur().TradeAccept(ctx, time.Duration(p.WaitMS)*time.Millisecond)
		return r, rpcErr(err)
	}))
	srv.Register("trade_cancel", rpc.Typed(func(ctx context.Context, _ empty) (map[string]string, error) {
		st, err := cur().TradeCancel(ctx)
		return map[string]string{"status": st}, rpcErr(err)
	}))
	srv.Register("trade_status", rpc.Typed(func(ctx context.Context, _ empty) (session.TradeStepState, error) {
		return cur().TradeStatus(), nil
	}))
}
