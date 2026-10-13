package brain

import (
	"context"
	"strings"
	"time"
)

// Sitting. A player who eats or drinks sits (the Food and Drink auras), and one who has finished stays seated until it moves. The core
// refuses every cast of a seated player with SPELL_FAILED_NOT_STANDING (Spell.cpp CheckCast) and does not stand the player by itself, so
// a cast sent while seated is lost, and sent again on every tick. The engine therefore looks at it for every cast:
//   - a player still eating or drinking is left alone unless the cast is worth breaking the meal for (the player is in combat, or the
//     action is an emergency: relevance of a low-health heal and above); the cast waits, as for any other condition;
//   - otherwise, and when the player sits with no meal to finish, it stands up first (CMSG_STANDSTATECHANGE, through the optional Stander
//     half of the game) and the cast follows on the next tick.
// Eating and drinking themselves are item uses, not casts, and are not affected. A refusal that gets through (the state changed under the
// tick) backs the spell off for a second (holdFor).

// Stander is the optional half of the Game that can stand the player up. A game without it never stands: a seated player's casts then
// wait for it to stand by itself.
type Stander interface {
	Stand(ctx context.Context) error
}

const (
	standWorthRel = RelHigh + 8 // the relevance from which a meal is broken outside a fight
	standHold     = 1500 * time.Millisecond
)

// eating says the player is eating or drinking (the aura is up).
func eating(c *Ctx) bool { return HasAura(&c.Self, "Drink", "Food") }

// seated says the player sits: at a meal, or still down after it.
func seated(c *Ctx) bool { return eating(c) || strings.HasPrefix(c.Self.StandState, "sit") }

// standOrWait decides for a cast of relevance rel while the player is seated: it returns (true, nil) when the cast may go on, a Decision
// when the player was stood up instead (the tick's action), and (false, nil) when the cast waits.
func (b *Brain) standOrWait(ctx context.Context, c *Ctx, a *Action, trigger string, rel float32) (proceed bool, done *Decision) {
	if !a.isCast() || !c.SelfOK || !seated(c) {
		return true, nil
	}
	if eating(c) && !inCombat(c) && rel < standWorthRel {
		b.traceNotPossible(c, a, trigger, rel, "eating")
		return false, nil
	}
	st, ok := c.G.(Stander)
	if !ok {
		b.traceNotPossible(c, a, trigger, rel, "seated")
		return false, nil
	}
	if why, held := b.mem.actionHeld("stand_up", c.Now); held {
		b.traceNotPossible(c, a, trigger, rel, "standing:"+why)
		return false, nil
	}
	d := Decision{Trigger: trigger, Action: "stand_up", Rel: rel, at: c.Now}
	cctx, cancel := context.WithTimeout(ctx, b.cfg.DoTimeout)
	defer cancel()
	if err := st.Stand(cctx); err != nil {
		d.Result, d.Error = ResultError, err.Error()
		b.mem.holdAction("stand_up", c.Now.Add(b.cfg.ErrorHold), "error")
		return false, &d
	}
	d.Result = ResultOK
	b.mem.holdAction("stand_up", c.Now.Add(standHold), "settle")
	return false, &d
}

func (b *Brain) traceNotPossible(c *Ctx, a *Action, trigger string, rel float32, why string) {
	c.trace = append(c.trace, Decision{Trigger: trigger, Action: a.Name, Rel: rel, Result: ResultNotPossible, Why: why, at: c.Now})
}
