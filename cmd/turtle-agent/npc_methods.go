package main

import (
	"context"
	"errors"
	"time"

	"github.com/TortoiseBots/turtle-agent/internal/events"
	"github.com/TortoiseBots/turtle-agent/internal/objects"
	"github.com/TortoiseBots/turtle-agent/internal/packet"
	"github.com/TortoiseBots/turtle-agent/internal/rpc"
	"github.com/TortoiseBots/turtle-agent/internal/session"
)

// npcParams serves the vendor, trainer and quest methods; each reads the fields it needs.
type npcParams struct {
	GUID      packet.GUID `json:"guid"`
	ItemEntry uint32      `json:"item_entry,omitempty"`
	Count     uint32      `json:"count,omitempty"`
	Bag       *uint8      `json:"bag,omitempty"`
	Slot      *uint8      `json:"slot,omitempty"`
	SpellID   uint32      `json:"spell_id,omitempty"`
	QuestID   uint32      `json:"quest_id,omitempty"`
	Choice    *uint32     `json:"choice,omitempty"`
	WaitMS    int         `json:"wait_ms,omitempty"`
}

// npcResult is the log cursor before the request and, with wait_ms, the first event that
// answers it (nil when none arrived in time). Item is the item a sell picked.
type npcResult struct {
	Since uint64        `json:"since"`
	Item  *objects.Item `json:"item,omitempty"`
	Event *events.Event `json:"event"`
}

var (
	buyKinds       = []string{events.KindVendorBuy, events.KindVendorBuyFailed, events.KindInventoryError}
	sellKinds      = []string{events.KindVendorSell, events.KindVendorSellFailed}
	trainKinds     = []string{events.KindTrainerTrained, events.KindTrainerFailed}
	helloKinds     = gossipKinds
	queryKinds     = []string{events.KindQuestDetails, events.KindQuestError}
	acceptKinds    = []string{events.KindQuestAccepted, events.KindQuestError}
	completeKinds  = []string{events.KindQuestRequestItems, events.KindQuestOfferReward, events.KindQuestError}
	rewardKinds    = []string{events.KindQuestRewarded, events.KindQuestError, events.KindQuestOfferReward}
	abandonedKinds = []string{events.KindQuestAbandoned}
)

type questLogResult struct {
	Quests []objects.QuestSlot `json:"quests"`
}

// registerNPCMethods wires vendors, trainers and quest givers to the session cur returns at
// each call. Each request returns at once with the log cursor; with wait_ms it also waits for
// the first event that answers it.
func registerNPCMethods(srv *rpc.Server, cur func() *session.Session) {
	log := cur().Log()
	// call runs do, then waits for the answer among kinds.
	call := func(ctx context.Context, p npcParams, kinds []string, do func(*session.Session) error) (npcResult, error) {
		res := npcResult{Since: log.Last()}
		if err := do(cur()); err != nil {
			return res, rpcErr(err)
		}
		res.Event = firstEvent(ctx, log, res.Since, kinds, p.WaitMS)
		return res, nil
	}
	srv.Register("vendor_list", rpc.Typed(func(ctx context.Context, p npcParams) (npcResult, error) {
		return call(ctx, p, []string{events.KindVendorList, events.KindVendorSellFailed}, func(s *session.Session) error { return s.VendorList(ctx, p.GUID) })
	}))
	srv.Register("buy", rpc.Typed(func(ctx context.Context, p npcParams) (npcResult, error) {
		return call(ctx, p, buyKinds, func(s *session.Session) error {
			if p.Count > 255 {
				return errors.New("buy: count is at most 255 per call")
			}
			return s.Buy(ctx, p.GUID, session.BuyRef{Entry: p.ItemEntry, Count: uint8(p.Count), Bag: p.Bag, Slot: p.Slot})
		})
	}))
	srv.Register("sell", rpc.Typed(func(ctx context.Context, p npcParams) (npcResult, error) {
		var item objects.Item
		res, err := call(ctx, p, sellKinds, func(s *session.Session) (err error) {
			// A slot with no bag is the backpack, as for equip.
			item, err = s.Sell(ctx, p.GUID, session.EquipRef{Entry: p.ItemEntry, Bag: p.Bag, Slot: p.Slot}, p.Count)
			return err
		})
		if err == nil {
			res.Item = &item
		}
		return res, err
	}))
	srv.Register("trainer_list", rpc.Typed(func(ctx context.Context, p npcParams) (npcResult, error) {
		return call(ctx, p, []string{events.KindTrainerList}, func(s *session.Session) error { return s.TrainerList(ctx, p.GUID) })
	}))
	srv.Register("train", rpc.Typed(func(ctx context.Context, p npcParams) (npcResult, error) {
		return call(ctx, p, trainKinds, func(s *session.Session) error { return s.Train(ctx, p.GUID, p.SpellID) })
	}))
	srv.Register("quest_hello", rpc.Typed(func(ctx context.Context, p npcParams) (npcResult, error) {
		return call(ctx, p, helloKinds, func(s *session.Session) error { return s.QuestHello(ctx, p.GUID) })
	}))
	srv.Register("quest_query", rpc.Typed(func(ctx context.Context, p npcParams) (npcResult, error) {
		return call(ctx, p, queryKinds, func(s *session.Session) error { return s.QuestQuery(ctx, p.GUID, p.QuestID) })
	}))
	srv.Register("quest_accept", rpc.Typed(func(ctx context.Context, p npcParams) (npcResult, error) {
		return call(ctx, p, acceptKinds, func(s *session.Session) error { return s.QuestAccept(ctx, p.GUID, p.QuestID) })
	}))
	srv.Register("quest_complete", rpc.Typed(func(ctx context.Context, p npcParams) (npcResult, error) {
		start := time.Now()
		res, err := call(ctx, p, completeKinds, func(s *session.Session) error { return s.QuestComplete(ctx, p.GUID, p.QuestID) })
		// A request_items panel with the items in hand is followed by the offer: report that,
		// within the wait_ms the caller gave in all.
		if err == nil && res.Event != nil && res.Event.Kind == events.KindQuestRequestItems && res.Event.Data["completable"] == true {
			left := p.WaitMS - int(time.Since(start).Milliseconds())
			if next := firstEvent(ctx, log, res.Event.Seq, []string{events.KindQuestOfferReward, events.KindQuestError}, max(left, 1)); next != nil {
				res.Event = next
			}
		}
		return res, err
	}))
	srv.Register("quest_reward", rpc.Typed(func(ctx context.Context, p npcParams) (npcResult, error) {
		return call(ctx, p, rewardKinds, func(s *session.Session) error { return s.QuestReward(ctx, p.GUID, p.QuestID, p.Choice) })
	}))
	srv.Register("quest_abandon", rpc.Typed(func(ctx context.Context, p npcParams) (npcResult, error) {
		return call(ctx, p, abandonedKinds, func(s *session.Session) error { return s.QuestAbandon(ctx, p.QuestID) })
	}))
	srv.Register("quest_log", rpc.Typed(func(ctx context.Context, _ empty) (questLogResult, error) {
		q := cur().QuestLog()
		if q == nil {
			q = []objects.QuestSlot{}
		}
		return questLogResult{q}, nil
	}))
}
