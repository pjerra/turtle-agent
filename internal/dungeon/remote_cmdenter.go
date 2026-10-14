package dungeon

import (
	"context"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/pjerra/turtle-agent/internal/tbm"
)

// Entering by `cmd enter` (TortoiseBots #939). A bot never sends CMSG_AREATRIGGER by itself and the module takes a trigger only from
// the master, so a lead bot that stands in the portal is not taken in. `.bot cmd enter <trigger id>` makes the commanded party go
// through the area trigger itself, as one order for the whole party:
//
//	TBM:ACTION_PENDING|cmd enter|party|<budget s>|<trigger id>     the order runs
//	TBM:ACTION_ACK|cmd enter|party|<n>|entered                      n bots went through; the party is inside
//	TBM:ACTION_ERR|cmd enter|<code>|<Bot>: <msg> [(n of m got in)]  not-found too-far requirement instance-limit failed busy in-combat
//	                                                                unavailable unreachable timeout cancelled denied no-bot
//
// The module ends the order itself with a timeout ERR about 6 s after the budget, so the wait for the final line is bounded by what the
// module says (tbm.MaxPendingWait at the most). The driver walks the lead bot to the portal as for the walk (the others follow it), waits
// a moment for them, and sends the order; every retry has its own count, and the whole phase a ceiling (rule 3). A module without the
// verb (no ACTION_PENDING, or an unknown-verb refusal) leaves the walk behaviour: the lead bot stands in the portal and the run fails
// saying why.
//
// The agent's own character is not part of the entry. It stays outside and ungrouped: a player who walked into the trigger alone would
// enter a private instance of its own, not the bots' (the instance belongs to the bots' group), so there is nothing to follow with.

// EnterCmd is the value of RemoteOptions.Enter that forces `cmd enter` (auto picks it for a plan with an entrance_trigger when the
// account cannot teleport).
const EnterCmd = "cmd"

// Limits of the `cmd enter` phase.
const (
	cmdEnterBusyTries   = 3                // an order answered busy is sent again this many times
	cmdEnterCancelTries = 2                // ... or cancelled
	cmdEnterFarTries    = 2                // ... or too-far, after the party had another moment to come to the door
	cmdEnterCombatWait  = 45 * time.Second // an order refused in-combat waits for the fight to end this long
	cmdEnterMax         = 4 * time.Minute  // the whole of the walk and the order: past it the phase fails
	cmdEnterGather      = 30               // yards from the portal at which a bot counts as at the door
)

// CmdEnterReport is what the `cmd enter` order did, in the enter report.
type CmdEnterReport struct {
	Trigger  uint32   `json:"trigger"`
	Orders   int      `json:"orders"`             // `cmd enter` lines sent
	Answers  []string `json:"answers,omitempty"`  // the final line (or "no answer") of each
	Entered  int      `json:"entered"`            // bots the ACK counted
	Fallback string   `json:"fallback,omitempty"` // why the walk behaviour was taken instead (an older module)
	Seconds  float64  `json:"seconds"`
}

// cmdEnterHints say what a refusal usually means.
var cmdEnterHints = map[string]string{
	"not-found":      "no area trigger of that id within reach of the party: is the plan's entrance_trigger right, and did the party reach the door?",
	"requirement":    "a bot does not meet the entry requirement (level, item or quest) of the dungeon",
	"instance-limit": "the account entered too many new instances this hour (TRANSFER_ABORT_TOO_MANY_INSTANCES)",
	"unreachable":    "a bot cannot walk to the trigger",
	"unavailable":    "a bot is dead, on a taxi or otherwise cannot enter",
	"denied":         "the agent's account is not the bots' owner",
	"no-bot":         "the party is not the commanded one",
	"failed":         "the teleport into the dungeon was refused",
	"timeout":        "the bots did not get through the trigger within the module's budget",
}

// unknownVerb says whether a refusal means the module has no such order (an older TortoiseBots).
func unknownVerb(code string) bool {
	switch code {
	case "unknown", "unknown-verb", "unknown-cmd", "invalid-verb", "usage":
		return true
	}
	return false
}

func (d *RemoteDriver) hasOrderGame() bool {
	_, ok := d.orderGame()
	return ok
}

// enterByCmd walks the party to the portal and orders it through the area trigger.
func (d *RemoteDriver) enterByCmd(ctx context.Context, began time.Time) error {
	if d.leadInside() {
		d.enterNote("the lead bot is already inside")
		return nil
	}
	pt := d.plan.Portal
	trig := d.plan.EntranceTrigger
	d.enterReport(func(r *EnterReport) { r.CmdEnter = &CmdEnterReport{Trigger: trig} })
	d.enterNote(fmt.Sprintf("walking the lead bot to the door at %.1f %.1f %.1f on map %d, then cmd enter %d", pt.X, pt.Y, pt.Z, pt.Map, trig))
	d.enterCmd = true
	defer func() { d.enterCmd = false }()
	return d.walkToPortal(ctx, pt, began)
}

// gatherAtDoor waits up to EnterGrace for every named bot to stand within cmdEnterGather yards of the portal: the order takes the whole
// party through, and a bot far from the door answers too-far.
func (d *RemoteDriver) gatherAtDoor(ctx context.Context) error {
	pt := d.plan.Portal
	deadline := d.clk.Now().Add(d.opt.EnterGrace)
	for {
		d.look()
		var far []string
		for _, n := range append([]string{d.opt.Lead}, d.namedBots()...) {
			m, ok := d.memberByName(n)
			if !ok || m.Map != pt.Map || dist3(partyPoint(m), pt.Point) > cmdEnterGather {
				far = append(far, n)
			}
		}
		if len(far) == 0 {
			return nil
		}
		if !d.clk.Now().Before(deadline) {
			d.enterNote(fmt.Sprintf("%s not within %d yd of the door after %.0f s: sending the order anyway", strings.Join(far, ", "), cmdEnterGather, d.opt.EnterGrace.Seconds()))
			return nil
		}
		if err := d.tick(ctx, d.cfg.Poll); err != nil {
			return err
		}
	}
}

// cmdEnterAtDoor sends `cmd enter <trigger>` from the moment the lead bot is at the portal until the party is in, or the phase fails.
func (d *RemoteDriver) cmdEnterAtDoor(ctx context.Context, began time.Time) error {
	og, _ := d.orderGame()
	trig := d.plan.EntranceTrigger
	command := "cmd enter " + strconv.FormatUint(uint64(trig), 10)
	if err := d.gatherAtDoor(ctx); err != nil {
		return err
	}
	phaseEnd := d.clk.Now().Add(cmdEnterMax)
	busy, cancelled, far := 0, 0, 0
	combatUntil := d.clk.Now().Add(cmdEnterCombatWait)
	for {
		if !d.clk.Now().Before(phaseEnd) {
			return fmt.Errorf("enter: cmd enter %d did not get the party in within %.0f s", trig, cmdEnterMax.Seconds())
		}
		sent := d.clk.Now()
		res, err := og.Order(ctx, command)
		if err != nil {
			return fmt.Errorf("enter: %s: %w", command, err)
		}
		d.countOrder("enter", res)
		d.cmdEnterNote(res, sent)
		if err := d.check(); err != nil {
			return err
		}
		if res.Err != nil {
			d.recordRefusal(Refusal{Intent: res.Intent, Code: res.Err.Code, Message: res.Err.Message})
		}
		code := res.Code()
		switch {
		case res.Kind == tbm.KindAck:
			n := 0
			if res.Ack != nil {
				n = res.Ack.Count
			}
			d.enterReport(func(r *EnterReport) {
				if r.CmdEnter != nil {
					r.CmdEnter.Entered = n
				}
			})
			if !res.Entered {
				d.enterNote(fmt.Sprintf("cmd enter answered %q, not \"entered\": looking for the lead bot inside", answerOf(res)))
			}
			if err := d.waitLead(ctx, began, d.opt.EnterGrace); err != nil {
				return fmt.Errorf("enter: cmd enter %d was acknowledged (%s) but the lead bot is not on map %d: %w", trig, answerOf(res), d.plan.Map, err)
			}
			return nil
		case code == "not-bound" || code == "disabled":
			return fmt.Errorf("%w: %s answered %s", errBindingLost, res.Intent, code)
		case code == "busy" && busy < cmdEnterBusyTries:
			busy++
			err = d.tick(ctx, d.cfg.Poll*3)
		case code == "cancelled" && cancelled < cmdEnterCancelTries:
			cancelled++
			err = d.tick(ctx, d.cfg.Poll)
		case code == "in-combat" && d.clk.Now().Before(combatUntil):
			err = d.tick(ctx, d.cfg.Poll*2)
		case code == "too-far" && far < cmdEnterFarTries:
			far++
			d.enterNote("cmd enter: a bot is too far from the door; waiting for the party to come")
			err = d.tick(ctx, d.opt.EnterGrace)
		case unknownVerb(code) || (res.Kind == tbm.KindTimeout && res.Pending == nil):
			return d.cmdEnterFallback(ctx, began, res)
		case res.Kind == tbm.KindTimeout:
			// It reported running and then no final line came in budget plus slack: the bots may be through anyway.
			if lerr := d.waitLead(ctx, began, d.opt.EnterGrace); lerr == nil {
				d.enterNote("cmd enter gave no final line, but the lead bot is inside")
				return nil
			}
			return fmt.Errorf("enter: cmd enter %d reported running (budget %d s) and gave no final answer", trig, res.Pending.BudgetS)
		case res.Err != nil:
			if got, of, ok := tbm.ParseGotIn(res.Err.Message); ok && got > 0 {
				// Part of the party went through: the group is locked, the others are summoned to the lead bot if it is inside.
				if lerr := d.waitLead(ctx, began, d.opt.EnterGrace); lerr == nil {
					d.enterNote(fmt.Sprintf("cmd enter: %d of %d got in (%s: %s); the rest are summoned", got, of, res.Err.Code, res.Err.Message))
					return nil
				}
			}
			return d.cmdEnterError(res)
		default:
			return fmt.Errorf("enter: %s: no ACK (%s)", command, res.Describe())
		}
		if err != nil {
			return err
		}
	}
}

// cmdEnterNote records an answer in the report and the notes.
func (d *RemoteDriver) cmdEnterNote(res tbm.OrderResult, sent time.Time) {
	answer := res.Final
	if answer == "" {
		answer = "no answer"
		if res.Pending != nil {
			answer = fmt.Sprintf("running (budget %d s), no final line", res.Pending.BudgetS)
		}
	}
	d.enterReport(func(r *EnterReport) {
		if r.CmdEnter != nil {
			r.CmdEnter.Orders++
			r.CmdEnter.Answers = append(r.CmdEnter.Answers, answer)
		}
	})
	d.enterNote(fmt.Sprintf("%s -> %s (%.0f s)", res.Command, answer, d.clk.Now().Sub(sent).Seconds()))
}

func (d *RemoteDriver) cmdEnterError(res tbm.OrderResult) error {
	hint := cmdEnterHints[res.Err.Code]
	if hint != "" {
		hint = ": " + hint
	}
	return fmt.Errorf("enter: %s refused: %s (%s)%s", res.Intent, res.Err.Code, res.Err.Message, hint)
}

// cmdEnterFallback is the walk behaviour for a module that does not know `cmd enter`: the lead bot stands in the portal and gets a moment
// to be taken in; if it is not, the run fails and says what is missing.
func (d *RemoteDriver) cmdEnterFallback(ctx context.Context, began time.Time, res tbm.OrderResult) error {
	why := "no ACTION_PENDING and no answer"
	if res.Err != nil {
		why = res.Err.Code + " (" + res.Err.Message + ")"
	}
	d.enterReport(func(r *EnterReport) {
		if r.CmdEnter != nil {
			r.CmdEnter.Fallback = why
		}
	})
	d.enterNote("the module does not know cmd enter (" + why + "; it needs TortoiseBots #939): waiting for the lead bot to be taken in as on a walk")
	if err := d.waitLead(ctx, began, d.opt.EnterGrace); err != nil {
		return fmt.Errorf("enter: the lead bot stood at the portal and did not enter, and the module has no `cmd enter` order (%s; needs TortoiseBots #939): %w", why, err)
	}
	return nil
}
