package dungeon

import (
	"context"
	"errors"
	"fmt"
	"math"
	"slices"
	"strings"
	"time"

	"github.com/pjerra/turtle-agent/internal/events"
	"github.com/pjerra/turtle-agent/internal/objects"
	"github.com/pjerra/turtle-agent/internal/packet"
	"github.com/pjerra/turtle-agent/internal/tbm"
)

// Object steps in remote mode (issue #83, gap G2). The agent's character is not with the group, so a bot does what a player does at a
// chest, a corpse, a lever and in the bags: the carrier. It is given the TortoiseBots player orders (player-controls.md section 1a,
// "Player orders"):
//
//	loot_object     cmd open <chest> <carrier>             (free-for-all loot around it), then the loot payload / `.bot inv`
//	boss loot_item  cmd lootmethod ffa, cmd loot <corpse> <carrier> <item>, cmd lootmethod <restore>
//	use_item        cmd use <item> <carrier>               (the Staff combine), then `.bot inv`
//	use_object      cmd open <go> <carrier>                (the Keystone, with the Staff in the carrier's bags)
//
// The party waits where the lead bot holds; the carrier leaves the formation for the walk and rejoins it. The GUID of a game object is
// the spawn's, which only the agent's own object store knows: the agent's character has to be within view of the object (not in the
// group). Every order waits for its final answer (ACTION_PENDING, then ACK or ERR). An order on an object is preceded by the approach
// (remote_approach.go: the party stood at the object, the carrier near and still) and, when the carrier's order fails twice, the step can
// be taken over by the agent's own character (remote_fallback.go).

// OrderGame is the optional part of a RemoteGame that gives the per-bot player orders and sees game objects. A game without it fails
// an object step as before.
type OrderGame interface {
	// Order sends a `cmd loot|open|use|lootmethod` command and waits for its final ACK or ERR (through ACTION_PENDING).
	Order(ctx context.Context, command string) (tbm.OrderResult, error)
	// Behavior sends `.bot behavior <bot> <key> on|off`.
	Behavior(ctx context.Context, bot, key string, on bool) (tbm.Reply, error)
	// GameObjects are the game objects the agent's own character has in view, with the GUIDs of their spawns.
	GameObjects() []objects.GameObject
}

// Limits of the object steps.
const (
	objectBusyTries   = 4                // an order answered busy is sent again this many times
	objectCancelTries = 2                // ... or cancelled
	objectFarTries    = 2                // ... or too-far, with the party walked toward the object in between
	objectCombatWait  = 60 * time.Second // an order refused in-combat waits for the fight to end this long
	objectRejoinWait  = 30 * time.Second // the carrier gets this long to be back by the lead bot
	objectRejoinNear  = 25               // yards from the lead bot at which the carrier has rejoined
	objectEffectTries = 4                // looks at the bags for the effect of a cast that is over
	objectWaitSettle  = 10 * time.Second // a game object the agent cannot see is waited for this long, then taken as changed
)

// ObjectOrder is one order of an object step and how it was answered.
type ObjectOrder struct {
	Command string  `json:"command"`
	Result  string  `json:"result"` // "ack", an ERR code, or "timeout"
	Reply   string  `json:"reply"`  // the final line, or what was waited for
	Pending bool    `json:"pending,omitempty"`
	Seconds float64 `json:"seconds"`
	// Target is the object an order by entry chose, from the module's ACTION_TARGET line ("0x1F10... entry 124371").
	Target string `json:"target,omitempty"`
}

// ObjectStepReport is what the carrier did for one object step.
type ObjectStepReport struct {
	Leg     int           `json:"leg"`
	Label   string        `json:"label"`
	Kind    string        `json:"kind"` // loot_object, use_object, use_item or boss loot_item
	Carrier string        `json:"carrier"`
	Orders  []ObjectOrder `json:"orders"`
	// TargetGUID is the GUID the module chose for the step's last order by entry (ACTION_TARGET), in the form `cmd` takes it.
	TargetGUID string   `json:"target_guid,omitempty"`
	Gained     []string `json:"gained"` // "7740 x1": what the loot payloads and the bags showed
	Seconds    float64  `json:"seconds"`
	Result     string   `json:"result"` // running, done, skipped or failed
	Note       string   `json:"note,omitempty"`
	Error      string   `json:"error,omitempty"`
	// Approach is how the party was brought to the object before the order ("still at 2.1 yd after 4.0 s"; remote_approach.go).
	Approach string `json:"approach,omitempty"`
	// CarrierResult is how the carrier's last object order was answered ("ack", an ERR code or "timeout"); FallbackUsed says whether the
	// agent's own character took the step over (remote_fallback.go), Fallback is what it did.
	CarrierResult string          `json:"carrier_result,omitempty"`
	FallbackUsed  bool            `json:"fallback_used"`
	Fallback      *FallbackReport `json:"fallback,omitempty"`
	// Participants are the bots a ritual step ordered, the carrier first.
	Participants []string `json:"participants,omitempty"`
}

func cloneObjectSteps(in []ObjectStepReport) []ObjectStepReport {
	if in == nil {
		return nil
	}
	out := make([]ObjectStepReport, len(in))
	for i, s := range in {
		out[i] = s
		out[i].Orders = slices.Clone(s.Orders)
		out[i].Gained = slices.Clone(s.Gained)
		out[i].Fallback = s.Fallback.clone()
		out[i].Participants = slices.Clone(s.Participants)
	}
	return out
}

// corpseRef is where a killed unit lay and its GUID.
type corpseRef struct {
	guid packet.GUID
	at   Point
	// ageMS is the agent's own copy's position age when the corpse came from its unit store (0 for the bots' frames and kills this
	// run saw): a copy older than staleCorpseMS that the server calls an invalid target is gone.
	ageMS int64
}

// staleCorpseMS is the position age (ms) from which the agent's copy of a corpse is taken for stale once the server answers invalid-target.
const staleCorpseMS = 300_000

// objState is the driver's state for the object steps.
type objState struct {
	carrier string
	corpses map[uint32]corpseRef
	waitKey [2]int
	waitAt  time.Time
	// unchecked is the game object a wait passed without seeing it ("game object 124372 near ..."); the next walk's failure names it, and
	// the next walk that succeeds clears it.
	unchecked string
}

// noteCorpse keeps the GUID of a unit a won pull killed: a boss's loot order names it.
func (d *RemoteDriver) noteCorpse(u deadAt) {
	if u.guid == 0 {
		return
	}
	if d.obj.corpses == nil {
		d.obj.corpses = map[uint32]corpseRef{}
	}
	d.obj.corpses[u.entry] = corpseRef{guid: u.guid, at: u.at}
}

func (d *RemoteDriver) orderGame() (OrderGame, bool) {
	og, ok := d.g.(OrderGame)
	return og, ok
}

// objectsInView are the agent's game objects, as the Stepper's find wants them: in reach, because the carrier does the walking.
func (d *RemoteDriver) objectsInView() []objects.GameObject {
	og, ok := d.orderGame()
	if !ok {
		return nil
	}
	var out []objects.GameObject
	for _, o := range og.GameObjects() {
		if o.Distance < 0 {
			continue // its position was never sent
		}
		o.Distance = 0
		out = append(out, o)
	}
	return out
}

// objectStepsNote is the note at the start of a run that has object steps.
func (d *RemoteDriver) objectStepsNote(n, fromLeg int) string {
	if _, ok := d.orderGame(); !ok {
		return fmt.Sprintf("remote mode: the plan has %d object steps (use_object, loot_object, use_item, game-object waits) from leg %d on; each fails the run when reached, because this game cannot give the carrier bot orders", n, fromLeg)
	}
	return fmt.Sprintf("remote mode: the plan has %d object steps from leg %d on; the carrier bot does them (cmd open|loot|use); a module that takes objects by entry (probed at the start of the run) needs no agent in view, any other needs the agent's own character within view of every game object they name", n, fromLeg)
}

// stepOf is the plan step an action belongs to.
func (d *RemoteDriver) stepOf(a Action) *Step {
	if a.Leg < 0 || a.Leg >= len(d.plan.Legs) || a.Step < 0 {
		return nil
	}
	l := d.plan.Legs[a.Leg]
	steps := l.Steps
	if strings.Contains(a.Label, "after_boss step") {
		steps = l.AfterBoss
	}
	if a.Step < len(steps) {
		return &steps[a.Step]
	}
	return nil
}

// objectLook handles what the Stepper made of a step that asked for game objects. handled is false when the action goes on as usual:
// the object is in the agent's view and the Stepper has an action for it.
func (d *RemoteDriver) objectLook(ctx context.Context, a Action) (err error, handled bool) {
	if _, ok := d.orderGame(); !ok {
		return d.objectStepError(a), true // a game that cannot give orders: the old, designed failure
	}
	switch a.Kind {
	case ActWalk, ActFailed:
		if st := d.stepOf(a); a.Kind == ActFailed && d.entry.ok && st != nil && st.WaitFor != nil && st.WaitFor.GameObject != nil && st.WaitFor.GameObject.Entry != 0 {
			return d.stateWaitError(a), true // a wait on cmd state that timed out: the Stepper's text and the module's last answer
		}
		// The Stepper found no such object in the agent's view and wanted to walk the party (or gave up): the agent cannot see it. (The
		// agent is brought up before the step is looked at, see nearBeforeObjectStep.) Only an action of an object step says so: a wait
		// that held in this very Next (the Seal seen active) is followed by the next leg's plain walk, which asked for the objects too
		// (uldaman-remote-4 ended on exactly that).
		if st := d.stepOf(a); st == nil || objectStepKind(*st) == "" {
			return nil, false
		}
		return d.objectViewError(a), true
	case ActWait:
		st := d.stepOf(a)
		if st == nil || st.WaitFor == nil || st.WaitFor.GameObject == nil {
			return nil, false
		}
		ref := &st.WaitFor.GameObject.ObjectRef
		if d.entry.ok && ref.Entry != 0 {
			d.waitOnState(ctx, a, st.WaitFor.GameObject)
			return nil, true
		}
		if _, seen := d.st.find(ref); seen {
			return nil, false // its state is in the agent's view: the usual wait
		}
		key := [2]int{a.Leg, a.Step}
		if d.obj.waitKey != key || d.obj.waitAt.IsZero() { // a timer that is not running starts now (stepFailedRecover resets it)
			d.obj.waitKey, d.obj.waitAt = key, d.clk.Now()
		}
		left := objectWaitSettle - d.clk.Now().Sub(d.obj.waitAt)
		if left <= 0 {
			// The feed (party and hostiles) carries no game-object state, so the wait cannot be checked another way. The step is passed
			// as a last resort: if the object did not change, the party's next walk past its position fails, and the run says so then.
			d.rep.note(fmt.Sprintf("%s: the agent cannot see %s%s, so its state is not checked; taken as %s after the order that opens it: the party's next walk through it is the check (a closed door fails that walk)", a.Label, describeRef(ref), d.agentFarText(st.WaitFor.GameObject.Near), st.WaitFor.GameObject.State))
			d.obj.unchecked = describeRef(ref)
			d.st.SkipStep()
			return nil, true
		}
		if err := d.tick(ctx, min(d.cfg.WaitPoll, left)); err != nil {
			d.stepFailedRecover(ctx, err)
		}
		return nil, true
	}
	return nil, false
}

// objectViewError is the failure of an object step whose game object the agent's character does not see.
func (d *RemoteDriver) objectViewError(a Action) error {
	what, far := "the game object", ""
	if st := d.stepOf(a); st != nil {
		if ref, at := objectViewOf(st); ref != nil {
			what, far = describeRef(ref), d.agentFarText(at)
			if far != "" {
				far = ", " + strings.Trim(strings.TrimSpace(far), "()") + fmt.Sprintf(", after looking for it %.0f s and moving toward it where it was safe", objectViewWait.Seconds())
			}
		}
	}
	return fmt.Errorf("%s: the agent must be within view of %s%s: only the agent's own object store knows the spawn GUID the carrier's order needs, and it has none "+
		"(remote mode needs the agent's character in view of object steps, not in the group)", a.Label, what, far)
}

// ---- the carrier ----

// carrierBot is the bot that does the object steps: the run's carrier option, else the first living bot that is neither the lead bot,
// nor the tank, nor the healer (a DPS), else the first living one that is not the lead bot.
func (d *RemoteDriver) carrierBot() (tbm.PartyRec, error) {
	d.look()
	if name := strings.TrimSpace(d.opt.Carrier); name != "" {
		m, ok := d.memberByName(name)
		switch {
		case !ok:
			return m, fmt.Errorf("carrier %s is not in the party of the feed", name)
		case m.Dead:
			return m, fmt.Errorf("carrier %s is dead", name)
		}
		return m, nil
	}
	if m, ok := d.memberByName(d.obj.carrier); ok && !m.Dead && d.obj.carrier != "" {
		return m, nil
	}
	var fallback *tbm.PartyRec
	for i := range d.fr.Party {
		m := d.fr.Party[i]
		if m.Dead || m.GUID == d.fr.Lead || strings.EqualFold(m.Name, d.opt.Lead) {
			continue
		}
		tank := strings.EqualFold(m.Name, d.opt.Tank) || (d.tank != 0 && m.GUID == d.tank)
		if !tank && !strings.EqualFold(m.Name, d.opt.Healer) {
			d.obj.carrier = m.Name
			return m, nil
		}
		if fallback == nil && !tank {
			fallback = &d.fr.Party[i]
		}
	}
	if fallback != nil {
		d.obj.carrier = fallback.Name
		return *fallback, nil
	}
	if l, ok := d.fr.LeadRec(); ok && !l.Dead {
		d.obj.carrier = l.Name
		return l, nil
	}
	return tbm.PartyRec{}, errors.New("no living bot to carry out the object step")
}

// ---- the step report ----

type objRec struct {
	d      *RemoteDriver
	idx    int
	at     time.Time
	target packet.GUID // the object the module chose for the step's last order by entry (ACTION_TARGET)
}

func (o *objRec) targetGUID() packet.GUID { return o.target }

func (d *RemoteDriver) newObjRec(leg int, label, kind, carrier string) *objRec {
	var idx int
	d.remote(func(r *RemoteReport) {
		r.Carrier = carrier
		r.ObjectSteps = append(r.ObjectSteps, ObjectStepReport{Leg: leg, Label: label, Kind: kind, Carrier: carrier, Orders: []ObjectOrder{}, Gained: []string{}, Result: "running"})
		idx = len(r.ObjectSteps) - 1
	})
	return &objRec{d: d, idx: idx, at: d.clk.Now()}
}

func (o *objRec) update(f func(*ObjectStepReport)) {
	o.d.remote(func(r *RemoteReport) {
		if o.idx < len(r.ObjectSteps) {
			f(&r.ObjectSteps[o.idx])
		}
	})
}

func (o *objRec) order(res tbm.OrderResult, began time.Time) {
	oo := ObjectOrder{Command: res.Command, Result: "ack", Reply: res.Final, Pending: res.Pending != nil, Seconds: o.d.clk.Now().Sub(began).Seconds()}
	if c := res.Code(); c != "" {
		oo.Result = c
	}
	if oo.Reply == "" {
		oo.Reply = res.Describe()
	}
	if t := res.Target; t != nil {
		o.target = t.GUID
		oo.Target = fmt.Sprintf("%s entry %d", tbm.GUIDArg(t.GUID), t.Entry)
	}
	o.update(func(s *ObjectStepReport) {
		s.Orders = append(s.Orders, oo)
		if res.Target != nil {
			s.TargetGUID = tbm.GUIDArg(res.Target.GUID)
		}
	})
}

func (o *objRec) gained(items ...string) {
	o.update(func(s *ObjectStepReport) {
		for _, it := range items {
			if !slices.Contains(s.Gained, it) {
				s.Gained = append(s.Gained, it)
			}
		}
	})
}

func (o *objRec) loot(l *tbm.Loot) {
	if l == nil {
		return
	}
	for _, it := range l.Items {
		o.gained(fmt.Sprintf("%d x%d", it.Entry, it.Count))
	}
	if l.Copper > 0 {
		o.gained(fmt.Sprintf("copper %d", l.Copper))
	}
}

func (o *objRec) finish(result string, err error, note string) {
	secs := o.d.clk.Now().Sub(o.at).Seconds()
	o.update(func(s *ObjectStepReport) {
		s.Seconds, s.Result, s.Note = secs, result, note
		if err != nil {
			s.Result, s.Error = "failed", err.Error()
		}
	})
}

// ---- orders ----

func (d *RemoteDriver) countOrder(verb string, res tbm.OrderResult) {
	d.wd.ordered(res.Command + " -> " + res.Describe())
	if res.Kind == tbm.KindAck {
		d.wd.acked(res.Command)
	}
	d.remote(func(rr *RemoteReport) {
		rr.OrdersSent++
		rr.Orders[verb]++
		switch {
		case res.Kind == tbm.KindErr && res.Err != nil:
			rr.OrdersRefused++
			rr.Refused[verb+":"+res.Err.Code]++
		case res.Kind == tbm.KindTimeout:
			rr.OrdersUnanswered++
		}
	})
}

// orderHints say what a refusal of a player order usually means.
var orderHints = map[string]string{
	"no-item":        "the carrier has no key or item for it (check .bot inv)",
	"locked":         "the carrier lacks the skill to open it",
	"denied":         "the agent's account is not the bot's owner",
	"no-bot":         "the carrier is not in the commanded party",
	"not-enough":     "the ritual needs more different bots than were named",
	"interrupted":    "a participant moved, was hit or stunned while the ritual channeled",
	"unavailable":    "the carrier is dead or left the party",
	"invalid-target": "no such corpse or object on the carrier's map",
	"not-found":      "no object of that entry is within the bots' reach of the point",
	"unreachable":    "the carrier found no way to it",
	"timeout":        "the walk took longer than its budget",
	"too-far":        "the object is farther than ReactDistance (150 yd by default) from the carrier and the party could not be brought nearer",
	"in-combat":      "a fight did not end",
	"busy":           "the carrier stayed busy with another order",
	"cancelled":      "another order took the carrier over",
	"failed":         "see the message",
}

func orderError(res tbm.OrderResult) error {
	switch {
	case res.Err != nil:
		if h := orderHints[res.Err.Code]; h != "" {
			return fmt.Errorf("%s refused: %s (%s): %s", res.Intent, res.Err.Code, res.Err.Message, h)
		}
		return fmt.Errorf("%s refused: %s (%s)", res.Intent, res.Err.Code, res.Err.Message)
	case res.Kind == tbm.KindTimeout:
		return fmt.Errorf("%s: %s", res.Intent, res.Describe())
	}
	return fmt.Errorf("%s: no ACK", res.Intent)
}

// carry sends one player order to the carrier and waits for its final answer, sending it again where that helps: busy (the carrier's
// last order is still running), cancelled (a later order took it), in-combat (until the fight ends) and too-far (the party is walked
// toward near, then the order goes out again). A refusal that stays, or an order that never answers, is the error.
func (d *RemoteDriver) carry(ctx context.Context, rec *objRec, command string, near *Point) (tbm.OrderResult, error) {
	og, _ := d.orderGame()
	verb := strings.Fields(command)[1]
	busy, cancelled, far := 0, 0, 0
	_, _, byEntry := entryFormOf(command)
	farTries := objectFarTries
	if byEntry {
		farTries = 1 // by entry the server says too-far before any search: the party is walked nearer in legs once, then the order is retried
	}
	combatUntil := d.clk.Now().Add(objectCombatWait)
	leadUntil := d.clk.Now().Add(d.opt.LeadWait)
	for {
		began := d.clk.Now()
		res, err := og.Order(ctx, command)
		if err != nil {
			return res, fmt.Errorf("cmd %s: %w", verb, err)
		}
		rec.order(res, began)
		d.countOrder(verb, res)
		if err := d.check(); err != nil {
			return res, err
		}
		if res.Kind == tbm.KindAck {
			return res, nil
		}
		if res.Err != nil {
			d.recordRefusal(Refusal{Intent: res.Intent, Code: res.Err.Code, Message: res.Err.Message})
		}
		if d.wdObject.take() { // the watchdog cancelled the retries: the refusal stands, and the step's retry or fallback takes over
			return res, orderError(res)
		}
		code := res.Code()
		switch {
		case code == "not-bound" || code == "disabled":
			return res, fmt.Errorf("%w: %s answered %s", errBindingLost, res.Intent, code)
		case code == "busy" && busy < objectBusyTries:
			busy++
			err = d.tick(ctx, d.cfg.Poll*3)
		case code == "cancelled" && cancelled < objectCancelTries:
			cancelled++
			err = d.tick(ctx, d.cfg.Poll)
		case code == "in-combat" && verb != "ritual" && d.clk.Now().Before(combatUntil): // a ritual is never re-sent blindly: stepRitual asks the altar first
			err = d.tick(ctx, d.cfg.Poll*2)
		case code == "lead-unavailable" && d.clk.Now().Before(leadUntil):
			err = d.tick(ctx, d.cfg.Poll*2)
		case code == "too-far" && near != nil && far < farTries:
			far++
			d.rep.note(fmt.Sprintf("%s: the object is out of the carrier's reach; walking the party toward it", command))
			err = d.doWalk(ctx, *near)
		case byEntry && code == "not-found":
			return res, entryNotFound(command, res)
		default:
			return res, orderError(res)
		}
		if err != nil {
			return res, err
		}
	}
}

// carrierInv is `.bot inv <bot>`: what the bot carries.
func (d *RemoteDriver) carrierInv(ctx context.Context, bot string) (tbm.Inventory, error) {
	bl, ok := d.g.(BotLiner)
	if !ok {
		return tbm.Inventory{}, errors.New("this game cannot send .bot inv")
	}
	lines, settled, err := bl.BotLines(ctx, "inv "+bot, 8*time.Second, func(line string) bool {
		return strings.HasPrefix(line, "TBM:INV_END|") || strings.HasPrefix(line, "TBM:ACTION_ERR|inv|")
	})
	inv := tbm.ParseInventory(lines)
	switch {
	case err != nil:
		return inv, fmt.Errorf(".bot inv %s: %w", bot, err)
	case inv.Err != nil:
		return inv, fmt.Errorf(".bot inv %s refused: %s (%s)", bot, inv.Err.Code, inv.Err.Message)
	case !settled || !inv.Complete:
		return inv, fmt.Errorf(".bot inv %s: no snapshot came", bot)
	}
	return inv, nil
}

// waitForItem looks at the carrier's bags until the item is there: a cast that is over has put its effect in the bags a moment later.
func (d *RemoteDriver) waitForItem(ctx context.Context, bot string, it *ItemRef) (bool, error) {
	for try := 0; try < objectEffectTries; try++ {
		inv, err := d.carrierInv(ctx, bot)
		if err != nil {
			return false, err
		}
		if inv.Count(it.Entry) > 0 {
			return true, nil
		}
		if err := d.tick(ctx, d.cfg.Poll*2); err != nil {
			return false, err
		}
	}
	return false, nil
}

// withFFA runs fn with the party's loot method set to free-for-all, then puts it back (RemoteOptions.LootRestore, "group" by default).
// A quest item rolled or handed to another member would not reach the carrier. The loot method goes back whatever the ffa order was
// answered: an ERR, a timeout or a cancel while it waited may still have changed it.
func (d *RemoteDriver) withFFA(ctx context.Context, rec *objRec, fn func() error) error {
	og, _ := d.orderGame()
	began := d.clk.Now()
	res, err := og.Order(ctx, "cmd lootmethod ffa")
	if err == nil {
		rec.order(res, began)
		d.countOrder("lootmethod", res)
	}
	switch {
	case err != nil:
		err = fmt.Errorf("cmd lootmethod ffa: %w", err)
	case res.Kind != tbm.KindAck:
		err = fmt.Errorf("could not set free-for-all loot: %w", orderError(res))
	case !strings.EqualFold(res.Method, "ffa"):
		err = fmt.Errorf("could not set free-for-all loot: the party's method is %q now", res.Method)
	default:
		err = fn()
	}
	restore := strings.ToLower(strings.TrimSpace(d.opt.LootRestore))
	if restore == "" {
		restore = "group"
	}
	rctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 15*time.Second)
	defer cancel()
	began = d.clk.Now()
	rres, rerr := og.Order(rctx, "cmd lootmethod "+restore)
	if rerr == nil {
		rec.order(rres, began)
		d.countOrder("lootmethod", rres)
	}
	if rerr != nil || rres.Kind != tbm.KindAck {
		d.rep.note(fmt.Sprintf("%s: could not put the loot method back to %s: %v", rec.label(), restore, errors.Join(rerr, orderErrorOrNil(rres))))
	}
	return err
}

func orderErrorOrNil(res tbm.OrderResult) error {
	if res.Kind == tbm.KindAck || res.Command == "" {
		return nil
	}
	return orderError(res)
}

func (o *objRec) label() string {
	var l string
	o.d.remote(func(r *RemoteReport) {
		if o.idx < len(r.ObjectSteps) {
			l = r.ObjectSteps[o.idx].Label
		}
	})
	return l
}

// waitRejoin waits for the carrier to be back by the lead bot after its walk. The module sends a follower back by itself; a carrier that
// is still away after a while is sent `cmd follow`.
func (d *RemoteDriver) waitRejoin(ctx context.Context, carrier string) error {
	if strings.EqualFold(carrier, d.opt.Lead) {
		return nil
	}
	began := d.clk.Now()
	sentFollow := false
	for {
		d.look()
		m, ok := d.memberByName(carrier)
		lead, haveLead := d.leadPos()
		if !ok || m.Dead || !haveLead || dist3(partyPoint(m), lead) <= objectRejoinNear {
			return nil
		}
		el := d.clk.Now().Sub(began)
		if el >= objectRejoinWait {
			d.rep.note(fmt.Sprintf("carrier %s is still %.0f yd from the lead bot %.0f s after its order", carrier, dist3(partyPoint(m), lead), el.Seconds()))
			return nil
		}
		if !sentFollow && el >= objectRejoinWait/3 {
			sentFollow = true
			if _, err := d.order(ctx, "follow"); err != nil {
				return err
			}
		}
		if err := d.tick(ctx, d.cfg.Poll*2); err != nil {
			return err
		}
	}
}

// ---- the steps ----

func objectPoint(a Action) *Point {
	if a.Object == nil {
		return a.EntryAt // an object named by entry that the agent does not see: the plan's point
	}
	return &Point{a.Object.X, a.Object.Y, a.Object.Z}
}

// doObjectStep does a loot_object, use_object or use_item step through the carrier.
func (d *RemoteDriver) doObjectStep(ctx context.Context, a Action) error {
	if _, ok := d.orderGame(); !ok {
		return d.objectStepError(a)
	}
	d.wdObject.clear()
	leave := d.wd.enter("object step", a.Label, func(context.Context) (string, error) {
		d.wdObject.set()
		return "cancelled the carrier order's retries: the step retries it once, then takes the fallback", nil
	})
	defer func() {
		leave()
		d.wd.progress("object step result: " + a.Label)
	}()
	if a.Kind == ActLoot && a.SpawnGUID != 0 {
		// Whatever the party is still fighting (the last grave's mobs, a pack that came by) is fought out before the carrier walks to the next chest.
		if err := d.fightOut(ctx, a.Label); err != nil {
			return err
		}
	}
	if _, err := d.keepNear(ctx); err != nil {
		return err
	}
	c, err := d.carrierBot()
	if err != nil {
		return fmt.Errorf("%s: %w", a.Label, err)
	}
	kind := "object step"
	if st := d.stepOf(a); st != nil && objectStepKind(*st) != "" {
		kind = objectStepKind(*st)
	}
	rec := d.newObjRec(a.Leg, a.Label, kind, c.Name)
	var note string
	switch a.Kind {
	case ActLoot:
		note, err = d.stepLoot(ctx, a, c.Name, rec)
		if err == nil && note == "" && a.SpawnGUID != 0 {
			err = d.fightOut(ctx, a.Label) // what the chest brought (a grave's Zombie and Dead Hero) is fought out before the next step
		}
	case ActUse:
		note, err = d.stepOpen(ctx, a, c.Name, rec)
	case ActItem:
		note, err = d.stepItem(ctx, a, c.Name, rec)
	case ActRitual:
		note, err = d.stepRitual(ctx, a, c, rec)
	}
	if err == nil && note != "carries it already" {
		err = d.waitRejoin(ctx, c.Name)
	}
	if err != nil {
		rec.finish("failed", err, note)
		d.emit(events.KindDungeonStep, a.Label+": "+err.Error(), map[string]any{"leg": a.Leg, "carrier": c.Name, "ok": false, "mode": "remote"})
		return fmt.Errorf("%s: %w", a.Label, err)
	}
	result := "done"
	if note != "" {
		result = "skipped"
	}
	rec.finish(result, nil, note)
	return nil
}

// stepLoot is a loot_object step: the carrier opens the chest and the loot is checked for the step's expect_item.
func (d *RemoteDriver) stepLoot(ctx context.Context, a Action, carrier string, rec *objRec) (string, error) {
	exp := a.Expect
	if exp != nil {
		inv, err := d.carrierInv(ctx, carrier)
		if err != nil {
			return "", err
		}
		if inv.Count(exp.Entry) > 0 {
			d.rep.note(fmt.Sprintf("%s: %s carries %s already; the chest is left alone", a.Label, carrier, exp))
			return "carries it already", nil
		}
	}
	// A chest of an exact spawn that is opened already, or gone, is skipped (a resumed run goes on at the first unopened grave).
	skip, why, sure := d.spawnOpened(ctx, a)
	if skip {
		d.rep.note(fmt.Sprintf("%s: %s: skipped", a.Label, why))
		return why, nil
	}
	stayed, err := d.approachObject(ctx, rec, carrier, objectPoint(a), d.standOf(a))
	if err != nil {
		return "", err
	}
	defer d.resumeFollow(ctx, stayed)
	if !sure { // the first look was out of the bots' reach: look again where the carrier stands now
		if skip, why, _ := d.spawnOpened(ctx, a); skip {
			d.rep.note(fmt.Sprintf("%s: %s: skipped", a.Label, why))
			return why, nil
		}
	}
	var res tbm.OrderResult
	var failed bool
	err = d.withFFA(ctx, rec, func() error {
		var err error
		res, failed, err = d.carryObject(ctx, rec, a, func(arg string) string { return "cmd open " + arg + " " + carrier })
		return err
	})
	if err != nil {
		if failed {
			return "", d.fallbackLoot(ctx, a, carrier, rec, err)
		}
		return "", err
	}
	rec.loot(res.Loot)
	if exp == nil {
		return "", nil
	}
	if res.Loot != nil && res.Loot.Has(exp.Entry) {
		return "", nil
	}
	if ok, err := d.waitForItem(ctx, carrier, exp); err != nil {
		return "", err
	} else if !ok {
		return "", fmt.Errorf("%s opened the chest (%s) but %s is not in its bags", carrier, res.Describe(), exp)
	}
	rec.gained(fmt.Sprintf("%d x1", exp.Entry))
	return "", nil
}

// stepOpen is a use_object step: the carrier opens the object (the Keystone, with the Staff of Prehistoria in its bags).
func (d *RemoteDriver) stepOpen(ctx context.Context, a Action, carrier string, rec *objRec) (string, error) {
	if req := a.Requires; req != nil {
		inv, err := d.carrierInv(ctx, carrier)
		if err != nil {
			return "", err
		}
		if inv.Count(req.Entry) == 0 {
			if d.agentHolds(req) && !d.laterNeeds(a, req.Entry) {
				// The agent made (or kept) the key itself: it opens the object.
				return "", d.fallbackOpen(ctx, a, carrier, rec, nil)
			}
			return "", fmt.Errorf("%s does not carry %s, which the object needs (.bot inv %s)", carrier, req, carrier)
		}
	}
	stayed, err := d.approachObject(ctx, rec, carrier, objectPoint(a), d.standOf(a))
	if err != nil {
		return "", err
	}
	defer d.resumeFollow(ctx, stayed)
	res, failed, err := d.carryObject(ctx, rec, a, func(arg string) string { return "cmd open " + arg + " " + carrier })
	if err != nil {
		if failed && a.Requires != nil && d.agentHolds(a.Requires) && !d.laterNeeds(a, a.Requires.Entry) {
			return "", d.fallbackOpen(ctx, a, carrier, rec, err)
		}
		if failed {
			err = fmt.Errorf("%w (the agent does not take this step over: it holds no %s, or a later step needs a bot to hold it)", err, a.Requires)
		}
		return "", err
	}
	rec.loot(res.Loot)
	return "", nil
}

// stepItem is a use_item step: the carrier uses the item (the Medallion, which combines with the Shaft of Tsol into the Staff).
func (d *RemoteDriver) stepItem(ctx context.Context, a Action, carrier string, rec *objRec) (string, error) {
	use := a.Item
	inv, err := d.carrierInv(ctx, carrier)
	if err != nil {
		return "", err
	}
	if a.Expect != nil && inv.Count(a.Expect.Entry) > 0 {
		d.rep.note(fmt.Sprintf("%s: %s carries %s already; nothing to use", a.Label, carrier, a.Expect))
		return "carries it already", nil
	}
	if inv.Count(use.Entry) == 0 || (a.Requires != nil && inv.Count(a.Requires.Entry) == 0) {
		if d.agentHolds(use, a.Requires) {
			// The agent holds both parts (it kept them): it combines them.
			return "", d.fallbackCombine(ctx, a, carrier, rec, nil)
		}
		if inv.Count(use.Entry) == 0 {
			return "", fmt.Errorf("%s does not carry %s to use (.bot inv %s)", carrier, use, carrier)
		}
		return "", fmt.Errorf("%s does not carry %s, which using %s consumes (.bot inv %s)", carrier, a.Requires, use, carrier)
	}
	res, failed, err := d.carryOrFallback(ctx, rec, fmt.Sprintf("cmd use %d %s", use.Entry, carrier), nil)
	if err != nil {
		if failed && d.agentHolds(use, a.Requires) {
			return "", d.fallbackCombine(ctx, a, carrier, rec, err)
		}
		if failed {
			err = fmt.Errorf("%w (the agent does not take this step over: its own bags do not hold %s%s)", err, use, andRequires(a.Requires))
		}
		return "", err
	}
	if res.Spell != 0 {
		d.rep.note(fmt.Sprintf("%s: %s cast spell %d", a.Label, carrier, res.Spell))
	}
	if a.Expect == nil {
		return "", nil
	}
	ok, err := d.waitForItem(ctx, carrier, a.Expect)
	if err != nil {
		return "", err
	}
	if !ok {
		return "", fmt.Errorf("%s used %s (%s) but %s is not in its bags", carrier, use, res.Describe(), a.Expect)
	}
	rec.gained(fmt.Sprintf("%d x1", a.Expect.Entry))
	return "", nil
}

func andRequires(it *ItemRef) string {
	if it == nil {
		return ""
	}
	return " and " + it.String()
}

// ---- the boss ----

// doBoss kills a boss. A boss with a loot_item, or with companions_loot false, also gets the carrier: the other bots' corpse looting is
// off for the fight and the carrier takes the item from the corpse.
func (d *RemoteDriver) doBoss(ctx context.Context, a Action) error {
	if skip, err := d.gatedLook(ctx, a); skip || err != nil {
		return err
	}
	b := a.Boss
	og, ok := d.orderGame()
	// A leg that has its looting off (Leg.CompanionsLoot false) has the bots muted already: the boss's own mute would turn it back on early.
	off := b.CompanionsLoot != nil && !*b.CompanionsLoot && len(d.legMuted) == 0
	if !ok || (b.LootItem == nil && !off) {
		return d.doKill(ctx, a)
	}
	c, err := d.carrierBot()
	if err != nil {
		return fmt.Errorf("%s: %w", a.Label, err)
	}
	if off {
		var muted []string
		for _, m := range d.fr.Party {
			if strings.EqualFold(m.Name, c.Name) {
				continue
			}
			if r, err := og.Behavior(ctx, m.Name, "loot", false); err != nil || r.Kind != tbm.KindAck {
				d.rep.note(fmt.Sprintf("%s: could not turn %s's corpse looting off: %s", a.Label, m.Name, behaviorWhy(r, err)))
				continue
			}
			muted = append(muted, m.Name)
		}
		d.rep.note(fmt.Sprintf("%s: corpse looting is off for %v (the carrier is %s)", a.Label, muted, c.Name))
		defer func() {
			rctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 15*time.Second)
			defer cancel()
			for _, n := range muted {
				if r, err := og.Behavior(rctx, n, "loot", true); err != nil || r.Kind != tbm.KindAck {
					d.rep.note(fmt.Sprintf("%s: could not turn %s's corpse looting back on: %s", a.Label, n, behaviorWhy(r, err)))
				}
			}
		}()
	}
	if err := d.doKill(ctx, a); err != nil {
		return err
	}
	if b.LootItem == nil {
		return nil
	}
	rec := d.newObjRec(a.Leg, a.Label, "boss loot_item", c.Name)
	note, err := d.lootBossItem(ctx, a, c.Name, rec)
	if err == nil && note == "" {
		err = d.waitRejoin(ctx, c.Name)
	}
	result := "done"
	if note != "" {
		result = "skipped"
	}
	if err != nil {
		rec.finish("failed", err, note)
		return fmt.Errorf("%s: %w", a.Label, err)
	}
	rec.finish(result, nil, note)
	return nil
}

func behaviorWhy(r tbm.Reply, err error) string {
	switch {
	case err != nil:
		return err.Error()
	case r.Err != nil:
		return r.Err.Code + " (" + r.Err.Message + ")"
	}
	return "no answer"
}

// lootOnAtStart turns corpse looting on for every bound bot when a remote run starts. A run that ended between a boss's `loot off` and
// its deferred `loot on` (a crash, say) leaves the bots loot-off, so every run sets it back. A refusal is noted; the run goes on.
func (d *RemoteDriver) lootOnAtStart(ctx context.Context) {
	og, ok := d.orderGame()
	if !ok {
		return
	}
	var on []string
	for _, m := range d.fr.Party {
		r, err := og.Behavior(ctx, m.Name, "loot", true)
		if err != nil || r.Kind != tbm.KindAck {
			d.rep.note(fmt.Sprintf("remote mode: could not turn %s's corpse looting on at the start of the run: %s", m.Name, behaviorWhy(r, err)))
			continue
		}
		on = append(on, m.Name)
	}
	d.rep.note(fmt.Sprintf("remote mode: corpse looting turned on for %v at the start of the run", on))
}

// lootBossHint is what a failed boss loot may mean.
const lootBossHint = "another bot may have looted it first (set the boss's companions_loot to false), or the loot rules hid it"

// downstreamItem is the item a later use_item step makes out of item (the Shaft of Tsol becomes the Staff of Prehistoria).
func (d *RemoteDriver) downstreamItem(leg int, item uint32) *ItemRef {
	for li := leg; li < len(d.plan.Legs); li++ {
		l := d.plan.Legs[li]
		steps := append(slices.Clone(l.Steps), l.AfterBoss...)
		if li == leg {
			steps = l.AfterBoss
		}
		for _, st := range steps {
			if u := st.UseItem; u != nil && u.RequiresItem != nil && u.RequiresItem.Entry == item && u.ExpectItem != nil {
				return u.ExpectItem
			}
		}
	}
	return nil
}

// corpseOf is the corpse of the boss: the GUID a won pull saw die, else a unit of the entry the feed lists dead, else a dead creature of
// the entry in the agent's own unit store within 80 yd of near (the boss's position; any when nil): the bots' frames list no corpses for
// long, and a boss killed in another fight, or by a party that moved on, is only in the store of an agent that stood near.
func (d *RemoteDriver) corpseOf(entry uint32, near *Point) (corpseRef, bool) {
	if c, ok := d.obj.corpses[entry]; ok {
		return c, true
	}
	for _, h := range d.fr.Hostiles {
		if h.Entry == entry && h.HPPct <= 0 {
			return corpseRef{guid: h.GUID, at: hostilePoint(h)}, true
		}
	}
	us, ok := d.g.(UnitSource)
	if !ok {
		return corpseRef{}, false
	}
	var best corpseRef
	bestD, found := math.MaxFloat64, false
	for _, u := range us.Units() {
		if u.Type != "creature" || u.Entry != entry || u.PosAgeMS < 0 || !(u.Dead || (u.MaxHealth > 0 && u.Health == 0)) {
			continue
		}
		at := Point{u.X, u.Y, u.Z}
		dist := 0.0
		if near != nil {
			if dist = dist3(at, *near); dist > 80 {
				continue
			}
		}
		if dist < bestD {
			best, bestD, found = corpseRef{guid: u.GUID, at: at, ageMS: u.PosAgeMS}, dist, true
		}
	}
	return best, found
}

// lootBossItem has the carrier take Boss.LootItem from the corpse: free-for-all loot, `cmd loot <corpse> <carrier> <item>`, the old loot
// method back.
func (d *RemoteDriver) lootBossItem(ctx context.Context, a Action, carrier string, rec *objRec) (string, error) {
	b := a.Boss
	it := b.LootItem
	inv, err := d.carrierInv(ctx, carrier)
	if err != nil {
		return "", err
	}
	if inv.Count(it.Entry) > 0 {
		d.rep.note(fmt.Sprintf("%s: %s carries %s already; the corpse is left alone", a.Label, carrier, it))
		return "carries it already", nil
	}
	if p := d.downstreamItem(a.Leg, it.Entry); p != nil && inv.Count(p.Entry) > 0 {
		d.rep.note(fmt.Sprintf("%s: %s carries %s, which %s is only for; the corpse is left alone", a.Label, carrier, p, it))
		return "carries the product already", nil
	}
	corpse, ok := d.corpseOf(b.Entry, &b.Position)
	byEntry := d.entry.ok
	if !ok && !byEntry {
		// A boss step that passed on a corpse seen in the agent's view must not pass here without a GUID to loot from: say what happened.
		return "", fmt.Errorf("boss %d died out of sight (the bots did not see it die and no corpse of it is in the agent's view); its loot can't be reached: %s is not obtainable from it in this run", b.Entry, it)
	}
	if !ok {
		corpse = corpseRef{at: b.Position} // by entry the server finds the corpse around the boss's place; the GUID, if there is one, is the retry
	}
	stayed, err := d.approachObject(ctx, rec, carrier, &corpse.at, nil)
	if err != nil {
		return "", err
	}
	defer d.resumeFollow(ctx, stayed)
	var res tbm.OrderResult
	attempt := func() error {
		build := func(arg string) string { return fmt.Sprintf("cmd loot %s %s %d", arg, carrier, it.Entry) }
		var err error
		if !byEntry {
			res, err = d.carry(ctx, rec, build(tbm.GUIDArg(corpse.guid)), &corpse.at)
			return err
		}
		res, err = d.carry(ctx, rec, build(tbm.EntryArg(b.Entry, pointArr(&corpse.at))), &corpse.at)
		if err != nil && corpse.guid != 0 && res.Code() == "not-found" && !runEnded(ctx, err) {
			d.rep.note(fmt.Sprintf("%s: nothing by entry %d; trying the GUID %s of the corpse", rec.label(), b.Entry, tbm.GUIDArg(corpse.guid)))
			res, err = d.carry(ctx, rec, build(tbm.GUIDArg(corpse.guid)), &corpse.at)
		}
		return err
	}
	err = d.withFFA(ctx, rec, attempt)
	if err != nil && res.Code() == "timeout" && !runEnded(ctx, err) {
		// No result came back in time (live, uldaman hires-2: the carrier was drinking after the fight when the order came). The
		// loot may have happened all the same, or the bot may have been too busy to start: look in its bags, and send the order once more.
		d.rep.note(fmt.Sprintf("%s: the loot order timed out; checking the bags of %s and sending it once more", rec.label(), carrier))
		if inv, ierr := d.carrierInv(ctx, carrier); ierr == nil && inv.Count(it.Entry) > 0 {
			rec.gained(fmt.Sprintf("%d x1", it.Entry))
			return "", nil
		}
		res = tbm.OrderResult{}
		err = d.withFFA(ctx, rec, attempt)
	}
	if err != nil {
		if res.Code() == "invalid-target" {
			if corpse.ageMS > staleCorpseMS {
				return "", fmt.Errorf("boss %d's corpse %s is gone on the server (invalid-target) and the agent's copy of it is stale (position %d s old); its loot can't be reached: %w", b.Entry, corpse.guid, corpse.ageMS/1000, err)
			}
			return "", fmt.Errorf("boss %d's corpse %s is gone on the server (invalid-target); its loot can't be reached: %w", b.Entry, corpse.guid, err)
		}
		return "", fmt.Errorf("%w; %s", err, lootBossHint)
	}
	rec.loot(res.Loot)
	if res.Loot != nil && res.Loot.Has(it.Entry) {
		return "", nil
	}
	if ok, err := d.waitForItem(ctx, carrier, it); err != nil {
		return "", err
	} else if !ok {
		return "", fmt.Errorf("%s looted the corpse of boss %d (%s) but %s is not in its bags; %s", carrier, b.Entry, res.Describe(), it, lootBossHint)
	}
	rec.gained(fmt.Sprintf("%d x1", it.Entry))
	return "", nil
}
