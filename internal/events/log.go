// Package events is the ordered event log the session appends to and RPC pages through.
package events

import (
	"context"
	"sort"
	"sync"
	"time"

	"github.com/pjerra/turtle-agent/internal/packet"
)

const (
	KindSession     = "session"      // state changes: in_world, logout_pending, logged_out, disconnected, teleported, falling, knocked_back, landed, reconnected
	KindChat        = "chat"         // say, party, yell, emote, channel, monster speech
	KindWhisper     = "whisper"      // whispers in and out
	KindSystem      = "system"       // system messages, GM command output, notifications
	KindParty       = "party"        // invites, command results, joins and leaves
	KindPartyStats  = "party_stats"  // SMSG_PARTY_MEMBER_STATS
	KindMelee       = "melee"        // SMSG_ATTACKERSTATEUPDATE
	KindSpellDamage = "spell_damage" // SMSG_SPELLNONMELEEDAMAGELOG
	KindPeriodic    = "periodic"     // SMSG_PERIODICAURALOG
	KindHeal        = "heal"         // SMSG_SPELLHEALLOG
	KindEnergize    = "energize"     // SMSG_SPELLENERGIZELOG
	KindSpellStart  = "spell_start"
	KindSpellGo     = "spell_go"
	KindSpellFail   = "spell_fail"
	KindSpellMiss   = "spell_miss"
	KindAuraApplied = "aura_applied"
	KindAuraRemoved = "aura_removed"
	KindDeath       = "death" // health reached zero
	KindKill        = "kill"  // SMSG_PARTYKILLLOG
	KindAttackStart = "attack_start"
	KindAttackStop  = "attack_stop"
	KindAttackError = "attack_error"
	KindParseError  = "parse_error"
	KindRaidMark    = "raid_mark" // MSG_RAID_TARGET_UPDATE: a raid icon set on or cleared from a unit

	// Resurrection and the corpse run (README "Death and the corpse run").
	KindResurrectRequest   = "resurrect_request"    // SMSG_RESURRECT_REQUEST: a spell offers to resurrect the player; source the caster, data.caster, name, sickness, timer
	KindCorpseReclaimDelay = "corpse_reclaim_delay" // SMSG_CORPSE_RECLAIM_DELAY after the release: data.ms until the corpse may be reclaimed

	// Group loot rolls, tied together by data.roll_id.
	KindLootStart     = "loot_roll_start" // SMSG_LOOT_START_ROLL: an item is up for rolls
	KindLootRoll      = "loot_roll"       // SMSG_LOOT_ROLL: a member's need/greed/pass, or their dice
	KindLootWon       = "loot_roll_won"   // SMSG_LOOT_ROLL_WON
	KindLootAllPassed = "loot_all_passed" // SMSG_LOOT_ALL_PASSED
	// KindInventoryError is SMSG_INVENTORY_CHANGE_FAILURE: an equip or move the server refused.
	KindInventoryError = "inventory_error"
	KindAddon          = "addon" // LANG_ADDON chat: prefix, text and sender (CHAT_MSG_ADDON in the client)
	// KindChannelNotify is SMSG_CHANNEL_NOTIFY: joined, left, kicked, wrong password, …
	KindChannelNotify  = "channel_notify"
	KindGossip         = "gossip"          // SMSG_GOSSIP_MESSAGE: an NPC's menu
	KindGossipComplete = "gossip_complete" // SMSG_GOSSIP_COMPLETE: the menu closed
	// The loot window of a corpse, its guid in source.
	KindLootOpen       = "loot_open"        // SMSG_LOOT_RESPONSE: the window with its money and items
	KindLootError      = "loot_error"       // SMSG_LOOT_RESPONSE refused: data.reason
	KindLootTaken      = "loot_taken"       // SMSG_LOOT_REMOVED: a slot left the window
	KindLootMoney      = "loot_money"       // SMSG_LOOT_CLEAR_MONEY: the window's money was taken
	KindLootMoneyShare = "loot_money_share" // SMSG_LOOT_MONEY_NOTIFY: this player's share of split money
	KindLootClosed     = "loot_closed"      // SMSG_LOOT_RELEASE_RESPONSE
	// KindGameObject is a game object changing: data.change is state, flags, dyn_flags, animation, reset,
	// spawn or destroyed (see the README, "Game objects"); the object is the source.
	KindGameObject = "gameobject"
	// Self state changes seen in the player's own update fields.
	KindMount       = "mount"        // UNIT_FIELD_MOUNTDISPLAYID went to or from 0: data.mounted
	KindMountResult = "mount_result" // SMSG_MOUNTRESULT and SMSG_DISMOUNTRESULT: data.result
	KindStandState  = "stand_state"  // the stand state changed (sit while eating or drinking): data.state
	// Vendors: a list, and the result of each buy and sell.
	KindVendorList       = "vendor_list"        // SMSG_LIST_INVENTORY
	KindVendorBuy        = "vendor_buy"         // SMSG_BUY_ITEM: the purchase went through
	KindVendorBuyFailed  = "vendor_buy_failed"  // SMSG_BUY_FAILED
	KindVendorSell       = "vendor_sell"        // the sold item left the bags (the core sends no packet for a sale)
	KindVendorSellFailed = "vendor_sell_failed" // SMSG_SELL_ITEM
	// Trainers: a list, and the result of each training.
	KindTrainerList    = "trainer_list"    // SMSG_TRAINER_LIST
	KindTrainerTrained = "trainer_trained" // SMSG_TRAINER_BUY_SUCCEEDED
	KindTrainerFailed  = "trainer_failed"  // SMSG_TRAINER_BUY_FAILED
	KindSpellLearned   = "spell_learned"   // SMSG_LEARNED_SPELL: a spell the server taught the player, spell_id
	// The agent's own spells (README "Agent state"): the spellbook, cooldowns, channels and combo points.
	KindSpellRemoved    = "spell_removed"    // SMSG_REMOVED_SPELL: a spell left the spellbook, spell_id
	KindSpellSuperseded = "spell_superseded" // SMSG_SUPERCEDED_SPELL: a higher rank replaced spell_id, data.by
	KindCooldownStart   = "cooldown_start"   // a spell's cooldown began: data.ms, category, source (cast, server, event)
	KindCooldownEnd     = "cooldown_end"     // a spell's own cooldown ran out
	KindCooldownClear   = "cooldown_clear"   // SMSG_CLEAR_COOLDOWN or SMSG_COOLDOWN_CHEAT ended it early; spell_id 0 with data.all is every cooldown
	KindChannelStart    = "channel_start"    // MSG_CHANNEL_START: the player channels spell_id for data.ms
	KindChannelEnd      = "channel_end"      // MSG_CHANNEL_UPDATE with 0 left: the channel ended or was broken
	KindComboPoints     = "combo_points"     // the player's combo points changed: data.points, was, target
	// Quests: the panels an NPC opens, and the result of each accept, hand-in and abandon.
	KindQuestList         = "quest_list"          // SMSG_QUESTGIVER_QUEST_LIST
	KindQuestDetails      = "quest_details"       // SMSG_QUESTGIVER_QUEST_DETAILS
	KindQuestRequestItems = "quest_request_items" // SMSG_QUESTGIVER_REQUEST_ITEMS
	KindQuestOfferReward  = "quest_offer_reward"  // SMSG_QUESTGIVER_OFFER_REWARD
	KindQuestAccepted     = "quest_accepted"      // the quest appeared in the quest log
	KindQuestRewarded     = "quest_rewarded"      // SMSG_QUESTGIVER_QUEST_COMPLETE
	KindQuestAbandoned    = "quest_abandoned"     // the quest left the quest log
	KindQuestError        = "quest_error"         // SMSG_QUESTGIVER_QUEST_INVALID, _FAILED or SMSG_QUESTLOG_FULL
	KindQuestUpdate       = "quest_update"        // SMSG_QUESTUPDATE_*: progress, completion or failure
	// The dungeon driver (dungeon_run): data holds the leg, step, seconds and, at the end, the run report's totals.
	KindDungeonLeg    = "dungeon_leg"    // a leg started or ended: data.phase start|end, data.result, seconds, deaths
	KindDungeonStep   = "dungeon_step"   // an action of the plan finished (or a wait began): data.action, ok, seconds, error
	KindDungeonFailed = "dungeon_failed" // the run ended without finishing: data.error ("wipe", a failed step, "stopped")
	KindDungeonDone   = "dungeon_done"   // the plan is finished
	// KindDungeonLeader is the leader's death and its way back during a run: data.phase died, resurrected, released,
	// corpse_run, reclaimed, back or gave_up (data.leg, seconds dead, how).
	KindDungeonLeader = "dungeon_leader"
	// KindPlayerFollow is the player follow mode (follow_start): data.phase started, walk, ahead, lost, zoning, zoned, dead, back,
	// stopped. Its death recovery sends KindDungeonLeader events with data.mode "follow".
	KindPlayerFollow = "player_follow"
	// The bot brain (brain_start, README "Bot brain"): what it decided each tick, and why it did nothing.
	KindBrainDecision = "brain_decision" // data.trigger, action, relevance, result (ok, not_possible, vetoed, error), via; spell_id and target_guid set
	KindBrainIdle     = "brain_idle"     // the brain had nothing to do (throttled): data.reason
	// KindBotViewFrame is a TBM:VIEW frame of the remote commander's perception feed (README "Remote leader"), throttled: data.seq,
	// lead, party (name, hp_pct, power_pct, dead, state), hostiles, engaged, threats, and the feed's counters.
	KindBotViewFrame = "bot_view_frame"
	KindBrainState   = "brain_state" // the brain started or stopped: data.state, strategies, role

	// KindTradeStatus is SMSG_TRADE_STATUS: a step of a trade window (README "Trading"). data.status names it (begin, open, accepted,
	// back_to_trade, complete, cancelled, too_far, busy, ...) and data.code is the TradeStatus number; the other player is the source.
	KindTradeStatus = "trade_status"

	// The player's pet (README "Pets", issue #80 step 4d). The pet's guid is source.
	KindPetSummoned  = "pet_summoned"  // UNIT_FIELD_SUMMON of the player names a pet: data.entry, data.dead; target the owner
	KindPetDied      = "pet_died"      // the pet's health reached 0 (a hunter's pet stays as a corpse until revived; a demon is gone in 15 s)
	KindPetRevived   = "pet_revived"   // the pet, dead before, has health again
	KindPetDismissed = "pet_dismissed" // the field was cleared: the pet was dismissed, replaced, unsummoned or despawned; data.was_dead
	KindPetError     = "pet_error"     // SMSG_PET_CAST_FAILED (data.spell_id, data.reason) or SMSG_PET_ACTION_FEEDBACK (data.feedback): the pet's refusal
)

// ChatKinds is what the chat_log method returns.
var ChatKinds = []string{KindChat, KindWhisper, KindSystem}

// now is the clock; tests replace it.
var now = time.Now

// base is taken once per process. Its monotonic reading is the zero of mono_ms.
var base = now()

// BaseTime is the wall time mono_ms counts from.
func BaseTime() time.Time { return base }

// MonoMS is the milliseconds elapsed since BaseTime on the monotonic clock.
func MonoMS() int64 { return int64(now().Sub(base) / time.Millisecond) }

// WallOffsetMS is the wall clock now minus BaseTime plus the monotonic elapsed time, in ms: how
// far the host clock has been stepped or has drifted from the monotonic clock since start.
func WallOffsetMS() int64 {
	n := now()
	return wallOffset(n.Round(0), n.Sub(base)).Milliseconds()
}

func wallOffset(wall time.Time, elapsed time.Duration) time.Duration {
	return wall.Sub(base.Round(0).Add(elapsed))
}

// Stamp sets Time (the wall clock) unless it is already set, and MonoMS (the steady axis) unless
// it is already set. An event that arrives with a Time still gets the mono_ms of its arrival.
func Stamp(e *Event) {
	n := now()
	if e.Time.IsZero() {
		e.Time = n
	}
	if e.MonoMS == 0 {
		e.MonoMS = int64(n.Sub(base) / time.Millisecond)
	}
}

// Event is one log entry. Names are resolved by the session before Append.
type Event struct {
	Seq        uint64         `json:"seq"`
	Time       time.Time      `json:"time"`
	MonoMS     int64          `json:"mono_ms"` // ms since BaseTime on the monotonic clock; unaffected by wall-clock steps (time is the wall clock)
	Kind       string         `json:"kind"`
	Source     string         `json:"source,omitempty"`
	SourceGUID packet.GUID    `json:"source_guid,omitempty"`
	Target     string         `json:"target,omitempty"`
	TargetGUID packet.GUID    `json:"target_guid,omitempty"`
	SpellID    uint32         `json:"spell_id,omitempty"`
	Amount     int64          `json:"amount,omitempty"`
	Crit       bool           `json:"crit,omitempty"`
	Party      bool           `json:"party,omitempty"` // source or target is the player or a party member
	Text       string         `json:"text,omitempty"`
	Data       map[string]any `json:"data,omitempty"`
}

const (
	DefaultLimit = 500
	MaxLimit     = 5000
)

type Query struct {
	Since     uint64
	Kinds     []string
	PartyOnly bool
	Limit     int
}

type Page struct {
	Events  []Event `json:"events"`
	Next    uint64  `json:"next"`
	Oldest  uint64  `json:"oldest"`
	Dropped bool    `json:"dropped"`
	More    bool    `json:"more"`
}

// Log is a bounded ring of events; when full it drops the oldest tenth.
type Log struct {
	mu      sync.Mutex
	limit   int
	buf     []Event
	last    uint64
	changed chan struct{}
	// intercept sees every event before it is stored and may consume it (SetIntercept).
	intercept func(Event) bool
}

func New(capacity int) *Log {
	if capacity < 10 {
		capacity = 10
	}
	return &Log{limit: capacity, changed: make(chan struct{})}
}

// SetIntercept installs fn, which Append calls with every event before storing it. When fn returns true the event is consumed: it is
// not stored, gets no sequence number and wakes nobody. The remote commander's feed uses it to take the TBM:VIEW lines (about five a
// second while bound) out of the log, so they do not push chat, dungeon and brain events out of the ring. fn runs on the appender's
// goroutine, outside the log's lock, and may itself call Append for other events. nil removes it.
func (l *Log) SetIntercept(fn func(Event) bool) {
	l.mu.Lock()
	l.intercept = fn
	l.mu.Unlock()
}

// Append stores e with the next sequence number, stamps Time if unset and wakes waiters. A consumed event (SetIntercept) is returned
// with Seq 0.
func (l *Log) Append(e Event) Event {
	l.mu.Lock()
	fn := l.intercept
	l.mu.Unlock()
	if fn != nil {
		Stamp(&e)
		if fn(e) {
			return e
		}
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	l.last++
	e.Seq = l.last
	Stamp(&e)
	if len(l.buf) >= l.limit {
		n := copy(l.buf, l.buf[l.limit/10:])
		clear(l.buf[n:])
		l.buf = l.buf[:n]
	}
	l.buf = append(l.buf, e)
	close(l.changed)
	l.changed = make(chan struct{})
	return e
}

func (l *Log) Last() uint64 {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.last
}

func (l *Log) Read(q Query) Page {
	l.mu.Lock()
	defer l.mu.Unlock()
	limit := q.Limit
	if limit <= 0 {
		limit = DefaultLimit
	}
	if limit > MaxLimit {
		limit = MaxLimit
	}
	p := Page{Events: []Event{}, Next: q.Since, Oldest: l.last + 1}
	if len(l.buf) > 0 {
		p.Oldest = l.buf[0].Seq
	}
	if q.Since > l.last {
		p.Next = l.last
		return p
	}
	p.Dropped = q.Since+1 < p.Oldest
	kinds := make(map[string]bool, len(q.Kinds))
	for _, k := range q.Kinds {
		kinds[k] = true
	}
	for i := sort.Search(len(l.buf), func(i int) bool { return l.buf[i].Seq > q.Since }); i < len(l.buf); i++ {
		if len(p.Events) == limit {
			p.More = true
			break
		}
		e := l.buf[i]
		p.Next = e.Seq
		if (len(kinds) > 0 && !kinds[e.Kind]) || (q.PartyOnly && !e.Party) {
			continue
		}
		p.Events = append(p.Events, e)
	}
	return p
}

// Wait returns once an event with Seq > since exists, or when ctx ends.
func (l *Log) Wait(ctx context.Context, since uint64) error {
	for {
		l.mu.Lock()
		if l.last > since {
			l.mu.Unlock()
			return nil
		}
		ch := l.changed
		l.mu.Unlock()
		select {
		case <-ch:
		case <-ctx.Done():
			return ctx.Err()
		}
	}
}
