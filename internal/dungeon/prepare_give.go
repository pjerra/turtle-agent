package dungeon

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/pjerra/turtle-agent/internal/packet"
)

// give_items (prepare.go): `.additem <entry>` on a bot, for a test run. The line acts on the SELECTED player, so the bot is selected by GUID
// and the line goes out as one session operation (ItemGiver); the bag check (`.bot inv`) proves the item arrived. The step refuses
// without a GM account of level DeveloperLevel (`.additem` is SEC_DEVELOPER), because a player's `.additem` is no command at all and would be
// said aloud in chat.

// ItemGiver is the optional part of a Preparer that sends `.additem <entry>` to a selected player: as one operation, with the GUID selected and
// confirmed first, like UnbindInstances. who is the name, for errors.
type ItemGiver interface {
	GiveItem(ctx context.Context, who string, guid packet.GUID, entry uint32) error
}

// giveBagWait is how long the bag is read until the item shows after the `.additem`.
const giveBagWait = 8 * time.Second

// defaultCarrier is the bot the object steps use when no carrier is named: the first bot that is neither the lead, the tank nor the healer, else
// the first that is not the lead or the tank, else the lead (RemoteDriver.carrierBot, without the look at who is alive).
func defaultCarrier(lead string, bots []string, tank, healer string) string {
	var fallback string
	for _, b := range bots {
		if strings.EqualFold(b, lead) {
			continue
		}
		isTank := strings.EqualFold(b, tank)
		if !isTank && !strings.EqualFold(b, healer) {
			return b
		}
		if fallback == "" && !isTank {
			fallback = b
		}
	}
	if fallback != "" {
		return fallback
	}
	return lead
}

// giveTarget names the bot a give_items entry means.
func (e *prepEnv) giveTarget(g GiveItem) (string, error) {
	lead, tank, healer := "", e.tank, e.healer
	bots := slices.Clone(e.bots)
	if e.hired != nil {
		lead, tank, healer = e.hired.Lead, e.hired.Tank, e.hired.Healer
		bots = append([]string{e.hired.Lead}, e.hired.Bots...)
	} else if len(e.bots) > 0 {
		lead, bots = e.bots[0], e.bots
	}
	switch role := strings.ToLower(strings.TrimSpace(g.Bot)); role {
	case "", GiveLead:
		if lead == "" {
			return "", errors.New("the run has no lead bot")
		}
		return lead, nil
	case GiveCarrier:
		if e.carrier != "" {
			return e.carrier, nil
		}
		if c := defaultCarrier(lead, bots, tank, healer); c != "" {
			return c, nil
		}
		return "", errors.New("the run has no bot to carry")
	}
	for _, b := range bots {
		if strings.EqualFold(b, g.Bot) {
			return b, nil
		}
	}
	return "", fmt.Errorf("%q is not a bot of the run (%s)", g.Bot, strings.Join(bots, ", "))
}

// giveName is the item's name for the setup line: the entry's own, else the plan's name for it, else "item <entry>".
func (e *prepEnv) giveName(g GiveItem) string {
	if g.Name != "" {
		return g.Name
	}
	if e.plan != nil {
		for _, l := range e.plan.Legs {
			if l.Optional != nil && l.Optional.Item == g.Item && l.Optional.Name != "" {
				return l.Optional.Name
			}
			for _, st := range append(slices.Clone(l.Steps), l.AfterBoss...) {
				if u := st.UseObject; u != nil && u.RequiresItem != nil && u.RequiresItem.Entry == g.Item && u.RequiresItem.Name != "" {
					return u.RequiresItem.Name
				}
			}
		}
	}
	return fmt.Sprintf("item %d", g.Item)
}

// guidOf is a bot's GUID: from the agent's own group or view (the hires, before the bind), else from the roster.
func (e *prepEnv) guidOf(ctx context.Context, name string) (packet.GUID, error) {
	if hg, ok := e.g.(HireGame); ok {
		if m, ok := hg.Player(name); ok && m.GUID != 0 {
			return m.GUID, nil
		}
	}
	if len(e.rows) == 0 {
		if err := e.readRoster(ctx); err != nil {
			return 0, err
		}
	}
	if r, ok := e.rowOf(name); ok && r.GUID != 0 {
		return packet.GUID(r.GUID), nil
	}
	return 0, fmt.Errorf("no GUID of %s: it is neither in the agent's group or view nor on the roster", name)
}

// giveItems sends `.additem` for every entry and checks the bag (see the top of this file).
func (e *prepEnv) giveItems(ctx context.Context) (string, error) {
	level, err := e.gmLevel(ctx)
	if err != nil {
		return "", fmt.Errorf("%w: give_items needs a GM account", err)
	}
	if level < DeveloperLevel {
		return "", fmt.Errorf("the account is level %d: give_items sends .additem, which needs a GM account of level %d", level, DeveloperLevel)
	}
	ig, ok := e.g.(ItemGiver)
	if !ok {
		return "", errors.New("this game cannot send .additem")
	}
	bl, ok := e.g.(BotLiner)
	if !ok {
		return "", errors.New("this game cannot send .bot inv (the bag check)")
	}
	var done []string
	for _, g := range e.spec.GiveItems {
		who, err := e.giveTarget(g)
		if err != nil {
			return "", fmt.Errorf("give_items item %d: %w", g.Item, err)
		}
		name := e.giveName(g)
		inv, err := botInventory(ctx, bl, who)
		if err != nil {
			return "", fmt.Errorf("give_items: %w", err)
		}
		if inv.Count(g.Item) > 0 {
			e.note(fmt.Sprintf("prepare give_items: %s carries %s already; no .additem sent", who, name))
			done = append(done, fmt.Sprintf("%s has %s", who, name))
			continue
		}
		guid, err := e.guidOf(ctx, who)
		if err != nil {
			return "", fmt.Errorf("give_items: %w", err)
		}
		if err := ig.GiveItem(ctx, who, guid, g.Item); err != nil {
			return "", fmt.Errorf(".additem %d on %s: %w", g.Item, who, err)
		}
		e.gmLine(fmt.Sprintf("additem %d on %s (test setup: %s)", g.Item, who, name))
		if err := e.waitBag(ctx, bl, who, g.Item); err != nil {
			return "", fmt.Errorf("give_items: .additem %d was sent for %s but %w", g.Item, who, err)
		}
		done = append(done, fmt.Sprintf("%s got %s", who, name))
	}
	return strings.Join(done, ", "), nil
}

// waitBag reads the bot's bags until the item shows, within giveBagWait.
func (e *prepEnv) waitBag(ctx context.Context, bl BotLiner, who string, item uint32) error {
	deadline := e.now().Add(giveBagWait)
	for {
		inv, err := botInventory(ctx, bl, who)
		if err != nil {
			return err
		}
		if inv.Count(item) > 0 {
			return nil
		}
		if !e.now().Before(deadline) {
			return fmt.Errorf("%s's bags (.bot inv) do not show item %d after %.0f s: the GM line may have run on another player, or the bot's bags are full", who, item, giveBagWait.Seconds())
		}
		if err := e.sleep(ctx, e.poll); err != nil {
			return err
		}
	}
}

// RemoteAbilities checks, before anything is paid for (a hire, a login, a teleport), that the game has every optional ability the run's options
// need. The remote driver finds these by asserting interfaces on its game (d.g.(X)), and a game without one fails a step late: a hired
// party is already bought when prepare's give_items finds out it cannot send `.additem`. g is the game the driver will get.
func RemoteAbilities(g any, opt RemoteOptions, plan *Plan) error {
	var missing []string
	need := func(ok bool, what, why string) {
		if !ok {
			missing = append(missing, what+" ("+why+")")
		}
	}
	prep := len(opt.GiveItems) > 0 || opt.Prepare.Active()
	_, isPrep := g.(Preparer)
	_, isLiner := g.(BotLiner)
	_, isGiver := g.(ItemGiver)
	_, isHire := g.(HireGame)
	if prep {
		need(isPrep, "Preparer", "the prepare step: bots online, fresh, gm_setup, give_items")
	}
	if opt.Prepare.HiredParty() {
		need(isHire, "HireGame", `prepare party "hire"`)
	}
	if len(opt.GiveItems) > 0 {
		need(isGiver, "ItemGiver", "give_items sends .additem")
		need(isLiner, "BotLiner", "give_items checks the bags with .bot inv")
	}
	if plan != nil {
		for i, l := range plan.Legs {
			if l.Optional != nil {
				need(isLiner, "BotLiner", fmt.Sprintf("leg %d (%s) is optional: its item is looked for with .bot inv", i, l.Name))
				break
			}
		}
	}
	if len(missing) > 0 {
		return fmt.Errorf("this game cannot do what the run's options need: %s (nothing was hired or sent)", strings.Join(missing, "; "))
	}
	return nil
}
