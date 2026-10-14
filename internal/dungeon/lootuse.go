package dungeon

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/pjerra/turtle-agent/internal/events"
	"github.com/pjerra/turtle-agent/internal/packet"
)

// Looted is what an open-and-take brought out of an object or a corpse: the items now in the bags, the copper, and the slots the
// server kept (a full bag, a running roll).
type Looted struct {
	Items []ItemCount
	Money uint32
	Left  []ItemCount
}

// ItemCount is an item entry and how many.
type ItemCount struct {
	Entry uint32
	Count uint32
}

// ObjectActor is the optional part of a Game that opens and loots game objects and corpses and uses items: the loot_object and
// use_item steps and Boss.LootItem need it, and fail with a message without it. LootObject casts the Opening spell at a chest or node
// and takes everything (session.LootObject), LootCorpse loots a dead creature (session.LootCorpse), UseItem uses an item from the
// bags by entry (CMSG_USE_ITEM on the leader itself).
type ObjectActor interface {
	LootObject(ctx context.Context, guid packet.GUID) (Looted, error)
	LootCorpse(ctx context.Context, guid packet.GUID) (Looted, error)
	UseItem(ctx context.Context, entry uint32) error
}

// TeamSource is the optional part of a Game that tells the leader's team, "alliance" or "horde" ("" when it cannot): the team gate
// (Boss.Team, PullGroup.Team) reads it. A Game without it, or one that cannot tell, runs everything.
type TeamSource interface {
	Team() string
}

// Team makes the Driver's stepWorld a TeamWorld.
func (w stepWorld) Team() string {
	if ts, ok := w.d.g.(TeamSource); ok {
		return ts.Team()
	}
	return ""
}

// LootRules is the group's loot setting: the method by name ("free_for_all", "round_robin", "master_loot", "group_loot",
// "need_before_greed"), the master looter (master_loot), the quality threshold, and whether the run's leader leads the group (only
// the group leader may change the setting).
type LootRules struct {
	Method    string
	Master    packet.GUID
	Threshold uint8
	Leader    bool
}

// LootFreeForAll is the method under which every member sees every item of a corpse.
const LootFreeForAll = "free_for_all"

// LootRuler is the optional part of a Game that reads and sets the group's loot rules (the party's SMSG_GROUP_LIST and
// CMSG_LOOT_METHOD). Boss.LootItem needs the leader to see the item in the corpse's window: under round_robin, group_loot and
// need_before_greed an item below the group's threshold (a white quest item like the Shaft of Tsol, quality 1 under the default
// uncommon threshold) is shown only to the member whose turn it was at the kill, until that member opens and closes the corpse
// (LootMgr.cpp:922-946, LootHandler.cpp:668-684). With the companions' looting off that member never does, and live run 4 found
// the Shaft hidden from the leader. The window's permission is taken from the method when the corpse is opened
// (Player::SendLoot, Player.cpp:9566-9590), so the driver switches the group to free_for_all just for the leader's loot and puts
// the old method back after. A Game without it loots under whatever the group has.
type LootRuler interface {
	LootRules() (LootRules, bool)
	SetLootRules(ctx context.Context, r LootRules) error
}

// seesBossLoot reports whether the rules show every item of a corpse to the leader: free_for_all, or master_loot with the leader
// as the master looter.
func seesBossLoot(r LootRules, self packet.GUID) bool {
	return r.Method == LootFreeForAll || (r.Method == "master_loot" && r.Master != 0 && r.Master == self)
}

// openBossLoot makes the corpse's items visible to the leader for its loot: a group leader under another method switches to
// free_for_all and gets back a restore that puts the old method back. A leader that does not lead the group cannot; the loot is
// tried as it is (the item shows when the turn was the leader's) and the note says why it may be missing.
func (d *Driver) openBossLoot(ctx context.Context, label string) func() {
	noop := func() {}
	lr, ok := d.g.(LootRuler)
	if !ok {
		return noop
	}
	old, known := lr.LootRules()
	if !known || seesBossLoot(old, d.g.Self().GUID) {
		return noop
	}
	if !old.Leader {
		d.rep.note(fmt.Sprintf("%s: the group's loot method is %s and the leader does not lead the group: an item below the threshold shows only to the member whose turn it was", label, old.Method))
		return noop
	}
	want := LootRules{Method: LootFreeForAll, Threshold: old.Threshold}
	if err := lr.SetLootRules(ctx, want); err != nil {
		d.rep.note(fmt.Sprintf("%s: could not set the group's loot to %s for the leader's loot (%v); looting under %s", label, LootFreeForAll, err, old.Method))
		return noop
	}
	d.rep.note(fmt.Sprintf("%s: the group's loot is %s for the leader's loot (was %s)", label, LootFreeForAll, old.Method))
	return func() {
		if err := lr.SetLootRules(context.WithoutCancel(ctx), old); err != nil {
			d.rep.note(fmt.Sprintf("%s: could not put the group's loot back to %s: %v", label, old.Method, err))
		}
	}
}

// checkLootRules fails a run whose legs from fromLeg take an item from a boss's corpse (Boss.LootItem) when the leader can neither
// see every item under the group's loot method nor change it (it does not lead the group): the drop would show only when the turn is
// the leader's. The leader must lead the group or the group must use free_for_all.
func (d *Driver) checkLootRules(fromLeg int) error {
	lr, ok := d.g.(LootRuler)
	if !ok {
		return nil
	}
	r, known := lr.LootRules()
	if !known || r.Leader || seesBossLoot(r, d.g.Self().GUID) {
		return nil
	}
	for i := fromLeg; i < len(d.plan.Legs); i++ {
		if b := d.plan.Legs[i].Boss; b != nil && b.LootItem != nil {
			return fmt.Errorf("leg %d %q takes %s from the corpse of boss %d, but the group's loot method is %s and the leader does not lead the group: "+
				"an item below the loot threshold would show only to the member whose turn it is. Make the leader the group leader (promote) or set the loot method to free_for_all first",
				i, d.plan.Legs[i].Name, b.LootItem, b.Entry, r.Method)
		}
	}
	return nil
}

// Waits of the loot and item steps. They are not Config fields: the cast of an Opening spell and the cast of an item's spell are
// short and fixed (the Opening spells 3365 and 6477 take 5 s in the spell table, spell 9594 takes 2 s).
const (
	lootOpenWait  = 15 * time.Second // the cast of the Opening spell and the server's window
	lootTakeWait  = 5 * time.Second  // the server's removal of the taken slots
	itemArriveMax = 8 * time.Second  // how long an item the step expects may take to show in the bags after the action
	useItemSettle = 3 * time.Second  // the wait after a use_item with no expect_item
	corpseReach   = 4.0              // yards: the leader walks this close to a corpse before it loots it
)

func (d *Driver) objectActor(what string) (ObjectActor, error) {
	a, ok := d.g.(ObjectActor)
	if !ok {
		return nil, fmt.Errorf("%s: this game cannot open objects, loot corpses or use items", what)
	}
	return a, nil
}

// noteSkipped reports what the stepper's team gate passed over since the last look, in the report and as dungeon_step events.
func (d *Driver) noteSkipped() {
	for _, text := range d.st.Skipped() {
		d.rep.note(text)
		d.emit(events.KindDungeonStep, text, map[string]any{"action": "skip_team", "ok": true})
	}
}

// carries reports whether the leader's bags certainly hold the item.
func (d *Driver) carries(it *ItemRef) bool {
	has, _, _ := d.itemState(it.Entry)
	return has
}

// awaitItem waits up to itemArriveMax for the item to show in the bags (the server's update fields arrive a moment after the
// loot or the cast), and fails with what the step did when it does not. A Game that cannot list the bags cannot be asked.
func (d *Driver) awaitItem(ctx context.Context, it *ItemRef, what string) error {
	if _, ok := d.g.(ItemSource); !ok {
		return fmt.Errorf("%s: cannot check for %s, this game does not list the bags", what, it)
	}
	for waited := time.Duration(0); ; waited += d.cfg.Poll {
		if d.carries(it) {
			return nil
		}
		if waited >= itemArriveMax {
			return fmt.Errorf("%s, but %s is not in the leader's bags after %.0f s (see the loot_*, spell_fail and inventory_error events)", what, it, itemArriveMax.Seconds())
		}
		if err := d.sleep(ctx, d.cfg.Poll); err != nil {
			return err
		}
	}
}

// describeLooted lists what was taken for the report.
func describeLooted(l Looted) string {
	var parts []string
	for _, it := range l.Items {
		parts = append(parts, fmt.Sprintf("%d x item %d", it.Count, it.Entry))
	}
	if l.Money > 0 {
		parts = append(parts, fmt.Sprintf("%d copper", l.Money))
	}
	if len(parts) == 0 {
		parts = append(parts, "nothing")
	}
	s := strings.Join(parts, ", ")
	if len(l.Left) > 0 {
		var left []string
		for _, it := range l.Left {
			left = append(left, fmt.Sprintf("%d x item %d", it.Count, it.Entry))
		}
		sort.Strings(left)
		s += "; left in the window: " + strings.Join(left, ", ")
	}
	return s
}

// doLoot is a loot_object step: the leader is within UseRange of the chest. It casts the Opening spell and takes the loot (twice at
// most: a cast that something interrupted), then checks the expected item arrived. A leader that carries the expected item already
// has nothing to fetch.
func (d *Driver) doLoot(ctx context.Context, a Action) error {
	act, err := d.objectActor("loot_object")
	if err != nil {
		return err
	}
	if a.Expect != nil && d.carries(a.Expect) {
		d.rep.note(fmt.Sprintf("%s: the leader carries %s already; the object is left alone", a.Label, a.Expect))
		return nil
	}
	var got Looted
	lerr := d.holdingMove(ctx, func() error {
		var err error
		for try := 0; try < 2; try++ {
			if got, err = act.LootObject(ctx, a.GUID); err == nil {
				return nil
			}
			if cerr := ctx.Err(); cerr != nil {
				return cerr
			}
			if serr := d.sleepHeld(ctx, time.Second); serr != nil {
				return serr
			}
		}
		return err
	})
	if err := d.check(ctx); err != nil {
		return err
	}
	if lerr != nil {
		if errors.Is(lerr, context.Canceled) || errors.Is(lerr, context.DeadlineExceeded) {
			return lerr
		}
		return fmt.Errorf("loot game object %s: %w", a.GUID, lerr)
	}
	d.rep.note(fmt.Sprintf("%s: took %s", a.Label, describeLooted(got)))
	if a.Expect != nil {
		return d.awaitItem(ctx, a.Expect, fmt.Sprintf("looted game object %s (%s)", a.GUID, describeLooted(got)))
	}
	return nil
}

// doUseItem is a use_item step. The reagent (Requires) is checked first so that a missing one is named; then the item is used and
// the expected item awaited. A leader that carries the expected item already has no use for the item.
func (d *Driver) doUseItem(ctx context.Context, a Action) error {
	act, err := d.objectActor("use_item")
	if err != nil {
		return err
	}
	if a.Expect != nil && d.carries(a.Expect) {
		d.rep.note(fmt.Sprintf("%s: the leader carries %s already; the item is not used", a.Label, a.Expect))
		return nil
	}
	need := func(it *ItemRef, where string) error { return missingForUse(it, where, *a.Item) }
	if err := d.checkItem(ctx, a.Item, false, need); err != nil {
		return err
	}
	if err := d.checkItem(ctx, a.Requires, false, need); err != nil {
		return err
	}
	uerr := d.holdingMove(ctx, func() error {
		if err := act.UseItem(ctx, a.Item.Entry); err != nil {
			return fmt.Errorf("use %s: %w", a.Item, err)
		}
		return d.sleepHeld(ctx, d.cfg.UseSettle)
	})
	if err := d.check(ctx); err != nil {
		return err
	}
	if uerr != nil {
		return uerr
	}
	if a.Expect == nil {
		return d.sleep(ctx, useItemSettle)
	}
	return d.awaitItem(ctx, a.Expect, fmt.Sprintf("used %s", a.Item))
}

// doKillAndLoot is the boss action: the kill, then the loot of the boss's corpse when the plan names an item to take from it. With
// Boss.CompanionsLoot false the party's corpse looting is off from before the pull until the leader has looted (also when the fight or the
// loot fails), so that a companion cannot take the drop the plan needs. The restore is a blanket "on" for every party bot: TortoiseBots
// does not hand the driver each bot's own setting, so a bot the player had turned off comes back on (README).
func (d *Driver) doKillAndLoot(ctx context.Context, a Action) error {
	b := a.Boss
	if b.CompanionsLoot != nil && !*b.CompanionsLoot {
		if err := d.lootOrder(ctx, "off"); err != nil {
			return fmt.Errorf("%s: %w", a.Label, err)
		}
		d.rep.note(fmt.Sprintf("%s: the companions' corpse looting is off for the fight and the leader's loot", a.Label))
		defer d.restoreLoot(context.WithoutCancel(ctx), a.Label)
	}
	if err := d.doKill(ctx, a); err != nil {
		return err
	}
	if b.LootItem != nil {
		return d.lootBoss(ctx, a)
	}
	return nil
}

// lootAnswerWait is how long the driver waits for the answer to a loot order (a refusal whisper) before it reads the refusals.
const lootAnswerWait = time.Second

// lootOrder says ".bot action loot <state>" in party chat. The bot module scopes the order to the one bot the leader has selected, when it
// has one (ResolveDynamicScope), so the leader targets itself first: a target that is not a live bot leaves the order to every party bot.
// The earlier selection is not put back afterwards.
func (d *Driver) lootOrder(ctx context.Context, state string) error {
	self := d.g.Self().GUID
	if self == 0 {
		return fmt.Errorf("cannot clear the selection before the loot %s order: the leader's guid is not known", state)
	}
	if err := d.g.Target(ctx, self); err != nil {
		return fmt.Errorf("cannot clear the selection before the loot %s order: %w", state, err)
	}
	return d.order(ctx, "loot "+state)
}

// restoreLoot is the defer of a boss's loot-off: it orders the companions' looting back on. The refusals the fight left are read first, so
// that they are not blamed on this order; the answer to this order is read within lootAnswerWait, while it is still the last order given
// (a refusal whisper names no intent, and drainRefusals files it under the last order).
func (d *Driver) restoreLoot(ctx context.Context, label string) {
	d.drainRefusals()
	if err := d.lootOrder(ctx, "on"); err != nil {
		d.rep.note(fmt.Sprintf("%s: could not order the companions' looting back on: %v", label, err))
		return
	}
	if d.clk.Sleep(ctx, lootAnswerWait) == nil {
		d.drainRefusals()
	}
}

// lootRaceHint is what a refused or empty loot of the boss's corpse may mean: a companion took the drop first. Under round-robin and group loot
// the corpse is open to the player whose turn it is, or to whoever the item was rolled to (Player.cpp:17280-17293), and the server closes the
// window of the others.
const lootRaceHint = "a companion may have looted it first (round-robin or group loot lets one player take it; set the boss's companions_loot to false), or the loot method hid it (an item below the group's threshold shows only to the member whose turn it was; see the loot method note)"

// downstream is the item a later use_item step makes out of item, when the plan has one: the Shaft of Tsol becomes the Staff of
// Prehistoria through the Medallion's use. A run resumed with the product in the bags has no use for the corpse.
func (d *Driver) downstream(leg int, item uint32) *ItemRef {
	for li := leg; li < len(d.plan.Legs); li++ {
		l := d.plan.Legs[li]
		steps := append(append([]Step{}, l.Steps...), l.AfterBoss...)
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

// lootBoss walks to the boss's corpse, loots it and checks that Boss.LootItem arrived. The bots loot their kills as well, and may take
// the item first; the error says so.
func (d *Driver) lootBoss(ctx context.Context, a Action) error {
	b := a.Boss
	act, err := d.objectActor("boss loot")
	if err != nil {
		return err
	}
	if d.carries(b.LootItem) {
		d.rep.note(fmt.Sprintf("%s: the leader carries %s already; the corpse is left alone", a.Label, b.LootItem))
		return nil
	}
	if p := d.downstream(a.Leg, b.LootItem.Entry); p != nil && d.carries(p) {
		d.rep.note(fmt.Sprintf("%s: the leader carries %s, which %s is only for; the corpse is left alone", a.Label, p, b.LootItem))
		return nil
	}
	var corpse packet.GUID
	var at Point
	best := 1e9
	for _, u := range d.g.Units() {
		if u.Entry == b.Entry && u.Dead && u.Distance >= 0 && float64(u.Distance) < best {
			corpse, at, best = u.GUID, Point{u.X, u.Y, u.Z}, float64(u.Distance)
		}
	}
	if corpse == 0 {
		return fmt.Errorf("%s: the corpse of boss %d is not in view to take %s from", a.Label, b.Entry, b.LootItem)
	}
	if best > corpseReach {
		if err := d.doWalk(ctx, at); err != nil {
			return err
		}
	}
	restore := d.openBossLoot(ctx, a.Label)
	defer restore()
	var got Looted
	lerr := d.holdingMove(ctx, func() error {
		var err error
		for try := 0; try < 2; try++ {
			if got, err = act.LootCorpse(ctx, corpse); err == nil {
				return nil
			}
			if cerr := ctx.Err(); cerr != nil {
				return cerr
			}
			if serr := d.sleepHeld(ctx, time.Second); serr != nil {
				return serr
			}
		}
		return err
	})
	if err := d.check(ctx); err != nil {
		return err
	}
	if lerr != nil {
		if errors.Is(lerr, context.Canceled) || errors.Is(lerr, context.DeadlineExceeded) {
			return lerr
		}
		return fmt.Errorf("%s: loot the corpse of boss %d: %w; %s", a.Label, b.Entry, lerr, lootRaceHint)
	}
	d.rep.note(fmt.Sprintf("%s: took %s from the corpse of boss %d", a.Label, describeLooted(got), b.Entry))
	if err := d.awaitItem(ctx, b.LootItem, fmt.Sprintf("looted the corpse of boss %d (%s)", b.Entry, describeLooted(got))); err != nil {
		return fmt.Errorf("%w; %s", err, lootRaceHint)
	}
	return nil
}
