package main

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/pjerra/turtle-agent/internal/events"
	"github.com/pjerra/turtle-agent/internal/objects"
	"github.com/pjerra/turtle-agent/internal/packet"
	"github.com/pjerra/turtle-agent/internal/rpc"
	"github.com/pjerra/turtle-agent/internal/session"
)

// goTypeParam is a game object type given as a name ("door", "spell_focus") or a number.
type goTypeParam uint8

func (t *goTypeParam) UnmarshalJSON(b []byte) error {
	var name string
	if err := json.Unmarshal(b, &name); err != nil {
		var n uint8
		if err := json.Unmarshal(b, &n); err != nil {
			return fmt.Errorf("type must be a game object type name or number: %w", err)
		}
		name = fmt.Sprint(n)
	}
	id, ok := objects.GOTypeByName(name)
	if !ok {
		return fmt.Errorf("unknown game object type %q: use a name such as door, button, chest, goober or its number 0-30", name)
	}
	*t = goTypeParam(id)
	return nil
}

type gameObjectsParams struct {
	Within float32      `json:"within,omitempty"` // yards, 3D
	Radius float32      `json:"radius,omitempty"` // the same as within
	Entry  uint32       `json:"entry,omitempty"`
	Type   *goTypeParam `json:"type,omitempty"`
	Name   string       `json:"name,omitempty"`
	Limit  int          `json:"limit,omitempty"`
}

type useObjectParams struct {
	GUID   packet.GUID `json:"guid"`
	WaitMS int         `json:"wait_ms,omitempty"`
}

// useObjectResult is the object as it stood when the use was sent, the log cursor before it,
// and the first thing the server did about it within wait_ms.
type useObjectResult struct {
	Since  uint64             `json:"since"`
	Object objects.GameObject `json:"object"`
	// Outcome is the kind of the first answering event (loot_open, loot_error, gossip, a quest
	// panel, vendor_list, trainer_list, gameobject, spell_start, spell_go), or "none".
	Outcome string        `json:"outcome"`
	Event   *events.Event `json:"event"`
	// Hint says why nothing came back, when the object's state or distance makes a reason likely.
	Hint string `json:"hint,omitempty"`
}

// useObjectKinds are the events that answer a use of a game object. The ones about the object
// (or a panel it opens) carry its guid as source; a spell is the player's cast at the object or the
// object's own. inventory_error and spell_fail name no object, so they are not answers.
var useObjectKinds = []string{events.KindLootOpen, events.KindLootError, events.KindGossip, events.KindGossipComplete,
	events.KindQuestList, events.KindQuestDetails, events.KindQuestRequestItems, events.KindQuestOfferReward,
	events.KindVendorList, events.KindTrainerList, events.KindGameObject, events.KindSpellStart, events.KindSpellGo}

// useAnswers reports whether e is an answer to a use of object, by the player self. A spell counts
// when the object casts it, or when the player casts it with the object as target (spell_start and
// spell_go carry the target guid, an object included), so an unrelated buff or heal landing in
// the window does not.
func useAnswers(e events.Event, object, self packet.GUID) bool {
	switch e.Kind {
	case events.KindSpellStart, events.KindSpellGo:
		return e.SourceGUID == object || (e.SourceGUID == self && e.TargetGUID == object)
	}
	return e.SourceGUID == object
}

// registerGameObjectMethods wires the game object list and use to the session cur returns at each
// call (see registerMethodsFor).
func registerGameObjectMethods(srv *rpc.Server, cur func() *session.Session) {
	log := cur().Log()
	srv.Register("gameobjects", rpc.Typed(func(ctx context.Context, p gameObjectsParams) ([]objects.GameObject, error) {
		if p.Within == 0 {
			p.Within = p.Radius
		}
		f := objects.GOFilter{Near: p.Within, Entry: p.Entry, Name: p.Name, Limit: p.Limit}
		if p.Type != nil {
			t := uint8(*p.Type)
			f.Type = &t
		}
		gs := cur().GameObjects(f)
		if gs == nil {
			gs = []objects.GameObject{}
		}
		return gs, nil
	}))
	srv.Register("use_object", rpc.Typed(func(ctx context.Context, p useObjectParams) (useObjectResult, error) {
		res := useObjectResult{Since: log.Last(), Outcome: "none"}
		s := cur()
		o, err := s.UseObject(ctx, p.GUID)
		if err != nil {
			return res, rpcErr(err)
		}
		res.Object = o
		self := s.Status().GUID
		res.Event = answerEvent(ctx, log, res.Since, p.WaitMS, func(e events.Event) bool { return useAnswers(e, p.GUID, self) })
		if res.Event != nil {
			res.Outcome = res.Event.Kind
		} else {
			res.Hint = noUseAnswerHint(o)
		}
		return res, nil
	}))
}

// useObjectWait is use_object's default wait for an answer.
const useObjectWait = 3000

// answerEvent waits up to waitMS (default useObjectWait, at most 25 s) for the first event of
// useObjectKinds after since that match accepts, or returns nil.
func answerEvent(ctx context.Context, log *events.Log, since uint64, waitMS int, match func(events.Event) bool) *events.Event {
	if waitMS <= 0 {
		waitMS = useObjectWait
	}
	deadline := time.Now().Add(time.Duration(min(waitMS, 25000)) * time.Millisecond)
	for {
		left := time.Until(deadline)
		if left <= 0 {
			// One last look without waiting, so a zero-length remainder still sees what arrived.
			left = time.Millisecond
		}
		page := readEvents(ctx, log, events.Query{Since: since, Kinds: useObjectKinds, Limit: 50}, int(left/time.Millisecond))
		for i := range page.Events {
			if match(page.Events[i]) {
				return &page.Events[i]
			}
		}
		if ctx.Err() != nil || !time.Now().Before(deadline) {
			return nil
		}
		since = page.Next
	}
}

// noUseAnswerHint names the likely reasons for an unanswered use, from what the update fields say.
func noUseAnswerHint(o objects.GameObject) string {
	var why []string
	if o.Distance < 0 {
		why = append(why, "its position is unknown")
	} else if o.Distance > 5 {
		why = append(why, fmt.Sprintf("it is %.1f yards away; the core ignores a use out of reach, walk next to it first", o.Distance))
	}
	if !o.Usable {
		why = append(why, "it is flagged unusable (generic, no-interact, in use or with an interact condition)")
	}
	if o.Locked {
		why = append(why, "it is locked")
	}
	if len(why) == 0 {
		return "the server sent no answer: it ignores a use that the object's own conditions refuse"
	}
	return "the server sent no answer: " + strings.Join(why, "; ")
}
