package main

import (
	"context"
	"errors"
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
func playerRef(to string) (packet.GUID, string, error) {
	to = strings.TrimSpace(to)
	if to == "" {
		return 0, "", errors.New("trade needs \"to\": a player's name or guid")
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
}
