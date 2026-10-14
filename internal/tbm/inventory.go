package tbm

import (
	"context"
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/pjerra/turtle-agent/internal/events"
)

// Equipment slots of the TBM:INV_EQ lines (the core's EquipmentSlots, 1.12): the ones a pre-run check looks at.
const (
	SlotMainHand = 15
	SlotOffHand  = 16
	SlotRanged   = 17
)

// InvItem is one TBM:INV_ITEM line: an item in a bag.
type InvItem struct {
	Bag   int    `json:"bag"`
	Slot  int    `json:"slot"`
	Entry uint32 `json:"entry"`
	Count int    `json:"count"`
	Flags string `json:"flags,omitempty"`
}

// Inventory is a bot's `.bot inv <bot>` snapshot: TBM:INV_BEGIN, INV_EQ, INV_ITEM and INV_END (TortoiseBots BotCommands.cpp
// SendInventorySnapshot).
type Inventory struct {
	Bot      string         `json:"bot"`
	Copper   uint32         `json:"copper"`
	Equipped map[int]uint32 `json:"equipped"` // equipment slot -> item entry
	Items    []InvItem      `json:"items"`
	Free     int            `json:"free_slots"`
	Total    int            `json:"total_slots"`
	Complete bool           `json:"complete"` // the INV_END line came
	Err      *Err           `json:"err,omitempty"`
	Counts   map[uint32]int `json:"-"` // entry -> how many the bot carries or wears
}

// Count is how many of the item the bot carries in its bags and wears.
func (v Inventory) Count(entry uint32) int { return v.Counts[entry] }

// ParseInventory reads the lines of one `.bot inv` reply. Lines that are not inventory lines are ignored.
func ParseInventory(lines []string) Inventory {
	v := Inventory{Equipped: map[int]uint32{}, Counts: map[uint32]int{}}
	num := func(s string) (int, bool) {
		n, err := strconv.Atoi(strings.TrimSpace(s))
		return n, err == nil
	}
	for _, l := range lines {
		rest, ok := strings.CutPrefix(l, "TBM:")
		if !ok {
			continue
		}
		verb, args, _ := strings.Cut(rest, "|")
		f := strings.Split(args, "|")
		switch verb {
		case "INV_BEGIN":
			if len(f) >= 1 {
				v.Bot = f[0]
			}
			if len(f) >= 2 {
				if n, err := strconv.ParseUint(f[1], 10, 32); err == nil {
					v.Copper = uint32(n)
				}
			}
		case "INV_EQ":
			if len(f) >= 2 {
				slot, ok1 := num(f[0])
				entry, ok2 := num(f[1])
				if ok1 && ok2 {
					v.Equipped[slot] = uint32(entry)
					v.Counts[uint32(entry)]++
				}
			}
		case "INV_ITEM":
			if len(f) >= 4 {
				bag, ok1 := num(f[0])
				slot, ok2 := num(f[1])
				entry, ok3 := num(f[2])
				count, ok4 := num(f[3])
				if ok1 && ok2 && ok3 && ok4 {
					it := InvItem{Bag: bag, Slot: slot, Entry: uint32(entry), Count: count}
					if len(f) >= 5 {
						it.Flags = f[4]
					}
					v.Items = append(v.Items, it)
					v.Counts[it.Entry] += count
				}
			}
		case "INV_END":
			if len(f) >= 1 && v.Bot == "" {
				v.Bot = f[0]
			}
			if len(f) >= 3 {
				v.Free, _ = num(f[1])
				v.Total, _ = num(f[2])
			}
			v.Complete = true
		case "ACTION_ERR":
			if e, ok := ParseErr(l); ok && strings.EqualFold(e.Intent, "inv") {
				v.Err = &e
			}
		}
	}
	return v
}

// botLineVerbs are the plain `.bot` commands Lines may send (they are not commander orders): the pre-run kit refresh and check,
// and the logout at the end of a run.
var botLineVerbs = map[string]bool{"maintenance": true, "inv": true, "logout": true}

// botNameRe is what a bot's name can be (1.12 names are letters).
var botNameRe = regexp.MustCompile(`^[A-Za-z]{2,12}$`)

// CheckBotLine refuses a line that is not "<maintenance|inv|logout> <bot name>".
func CheckBotLine(command string) error {
	f := strings.Fields(strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(command), ".bot ")))
	if len(f) != 2 || !botLineVerbs[strings.ToLower(f[0])] {
		return fmt.Errorf("%q: Lines sends maintenance, inv or logout with one bot name", command)
	}
	if !botNameRe.MatchString(f[1]) {
		return fmt.Errorf("%q: %q is not a bot name", command, f[1])
	}
	return nil
}

// maxLines bounds the lines one Lines call keeps.
const maxLines = 400

// Lines sends ".bot <command>" for a command that has no ACK (maintenance, inv, logout) and collects the lines the server answers
// with, until done reports a line that ends the reply or wait runs out. settled is false when done never said so. The feed's
// TBM:VIEW lines are not part of the reply. One command is in flight at a time, like Do.
func (c *Commander) Lines(ctx context.Context, command string, wait time.Duration, done func(line string) bool) (lines []string, settled bool, err error) {
	command = strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(command), ".bot "))
	if err := CheckBotLine(command); err != nil {
		return nil, false, err
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	log := c.link.Log()
	since := log.Last()
	if err := c.link.Say(ctx, Chat(command)); err != nil {
		return nil, false, err
	}
	if wait <= 0 {
		wait = DefaultOrderWait
	}
	wctx, cancel := context.WithTimeout(ctx, wait)
	defer cancel()
	lines = []string{}
	cursor := since
	for {
		p := log.Read(events.Query{Since: cursor, Kinds: []string{events.KindSystem, events.KindAddon}, Limit: events.MaxLimit})
		for _, e := range p.Events {
			line, ok := LineOf(e)
			if !ok || strings.HasPrefix(line, ViewPrefix) {
				continue
			}
			if len(lines) < maxLines {
				lines = append(lines, line)
			}
			if done(line) {
				return lines, true, nil
			}
		}
		cursor = p.Next
		if p.More {
			continue
		}
		if log.Wait(wctx, cursor) != nil {
			if err := ctx.Err(); err != nil {
				return lines, false, err
			}
			return lines, false, nil
		}
	}
}
