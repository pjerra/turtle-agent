package main

import (
	"context"
	"fmt"
	"time"

	"github.com/pjerra/turtle-agent/internal/dungeon"
	"github.com/pjerra/turtle-agent/internal/objects"
	"github.com/pjerra/turtle-agent/internal/packet"
	"github.com/pjerra/turtle-agent/internal/session"
)

// The agent's own hands in a remote run (dungeon.AgentHands): what an object step falls back on when the carrier bot's order fails twice.
// It opens and loots with the same session calls as a group run, and hands the item to the carrier with the trade RPC.

func (g remoteGame) hands() *sessionGame {
	return &sessionGame{cur: g.c.cur, probeWindow: g.c.probeWindow}
}

func (g remoteGame) LootObject(ctx context.Context, guid packet.GUID) (dungeon.Looted, error) {
	return g.hands().LootObject(ctx, guid)
}

func (g remoteGame) LootCorpse(ctx context.Context, guid packet.GUID) (dungeon.Looted, error) {
	return g.hands().LootCorpse(ctx, guid)
}

func (g remoteGame) UseItem(ctx context.Context, entry uint32) error {
	return g.hands().UseItem(ctx, entry)
}

func (g remoteGame) UseObject(ctx context.Context, guid packet.GUID) error {
	return g.hands().UseObject(ctx, guid)
}

// Items is what the agent's own character carries.
func (g remoteGame) Items() []objects.Item { return g.c.cur().Items() }

// GM sends one of the two visibility lines of a hand-over (a GM that the run's setup made invisible is visible for the trade only).
func (g remoteGame) GM(ctx context.Context, text string) error {
	switch text {
	case ".gm visible on", ".gm visible off":
		return g.c.cur().GM(ctx, text)
	}
	return fmt.Errorf("gm: %q is not a line a remote run sends", text)
}

// Trade hands the items to the bot `to` (the trade RPC's session call); the bot has to be in view and in trade range.
func (g remoteGame) Trade(ctx context.Context, to string, items []dungeon.ItemCount) (dungeon.TradeOutcome, error) {
	if !botName.MatchString(to) {
		return dungeon.TradeOutcome{}, fmt.Errorf("trade: %q is not a bot name", to)
	}
	req := session.TradeRequest{Name: to, OpenWait: 8 * time.Second, Wait: 12 * time.Second}
	for _, it := range items {
		req.Items = append(req.Items, session.TradeItemReq{Entry: it.Entry, Count: it.Count})
	}
	r, err := g.c.cur().Trade(ctx, req)
	return dungeon.TradeOutcome{Result: r.Result, Verified: r.Verified, Detail: r.Detail}, err
}

var _ dungeon.AgentHands = remoteGame{}

// AreaTrigger sends CMSG_AREATRIGGER for the agent's own character (dungeon.AreaTriggerGame): the area_trigger step's fallback.
func (g remoteGame) AreaTrigger(ctx context.Context, id uint32) error {
	return g.c.cur().AreaTrigger(ctx, id)
}
