package tbm

import (
	"context"
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"time"
	"unicode"

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

// botLineVerbs are the plain `.bot` commands Lines may send (they are not commander orders) with one bot name: the pre-run kit refresh
// and check, the logout at the end of a run, the login of the prepare step (`add`, the owner's own character), and `summon` (a start past
// leg 0 brings the lead bot to the commander).
var botLineVerbs = map[string]bool{"maintenance": true, "inv": true, "logout": true, "add": true, "summon": true}

// botNameRe is what a bot's name can be (1.12 names are letters).
var botNameRe = regexp.MustCompile(`^[A-Za-z]{2,12}$`)

// The lines of the hire flow (prepare party "hire"): `hire <class> <role> [level=<n>]`, `autogear <bot> blue` and `command <bot> spells
// <filter>`. Each is checked word by word, so that no other hire option (a race, a gear tier, a gender), no other autogear quality and no
// other forwarded command goes out with them.
var (
	hireClasses = map[string]bool{"warrior": true, "paladin": true, "hunter": true, "rogue": true, "priest": true, "shaman": true, "mage": true, "warlock": true, "druid": true}
	hireRoles   = map[string]bool{"tank": true, "healer": true, "dps": true}
	hireLevelRe = regexp.MustCompile(`^level=([0-9]{1,2})$`)
	// spellFilterRe is what a spell-list filter can be: letters, spaces, an apostrophe and a colon ("Power Word: Shield").
	spellFilterRe = regexp.MustCompile(`^[A-Za-z][A-Za-z' :]{1,30}$`)
)

// CheckBotLine refuses a line that is not "<maintenance|inv|logout|add> <bot name>", "roster", "command <bot name> talents|leave",
// "command <bot name> spells <filter>", "autogear <bot name> blue" or "hire <class> <tank|healer|dps> [level=<n>]". The talents read-back
// and the leave (the prepare step takes a bot out of its old group) are the only `.bot command` lines of a bot's own state the agent sends
// here besides the spell-list read-back: any other word after it (reset, auto, a build link) would change the bot's talents.
func CheckBotLine(command string) error {
	if strings.IndexFunc(command, unicode.IsControl) >= 0 {
		return fmt.Errorf("%q: a control character in a bot line", command)
	}
	f := strings.Fields(strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(command), ".bot ")))
	if len(f) == 1 && strings.EqualFold(f[0], "roster") {
		return nil
	}
	if len(f) >= 3 && strings.EqualFold(f[0], "command") {
		if !botNameRe.MatchString(f[1]) {
			return fmt.Errorf("%q: %q is not a bot name", command, f[1])
		}
		switch {
		case len(f) == 3 && (f[2] == "talents" || f[2] == "leave" || f[2] == "who"):
			return nil
		case f[2] == "spells" && len(f) >= 4 && len(f) <= 7 && spellFilterRe.MatchString(strings.Join(f[3:], " ")):
			return nil
		}
	}
	if len(f) >= 3 && len(f) <= 4 && strings.EqualFold(f[0], "hire") {
		if !hireClasses[strings.ToLower(f[1])] || !hireRoles[strings.ToLower(f[2])] || (len(f) == 4 && !hireLevelRe.MatchString(strings.ToLower(f[3]))) {
			return fmt.Errorf("%q: hire takes <class> <tank|healer|dps> and optionally level=<n>", command)
		}
		return nil
	}
	if len(f) == 3 && strings.EqualFold(f[0], "autogear") {
		if !botNameRe.MatchString(f[1]) || !strings.EqualFold(f[2], "blue") {
			return fmt.Errorf("%q: autogear takes a bot name and blue", command)
		}
		return nil
	}
	if len(f) != 2 || !botLineVerbs[strings.ToLower(f[0])] {
		return fmt.Errorf("%q: Lines sends maintenance, inv, logout, add or summon with one bot name, roster, command <bot name> talents|leave|spells <filter>, autogear <bot name> blue, or hire <class> <role> [level=<n>]", command)
	}
	if !botNameRe.MatchString(f[1]) {
		return fmt.Errorf("%q: %q is not a bot name", command, f[1])
	}
	return nil
}

// replyFrom is the bot whose chat reply a line sent to the bot is read with: the name of "command <bot> ...". The bot answers a
// forwarded command in its own words (a whisper to the sender), not with a system line.
func replyFrom(command string) string {
	f := strings.Fields(command)
	if len(f) >= 2 && strings.EqualFold(f[0], "command") {
		return f[1]
	}
	return ""
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
	from := replyFrom(command)
	kinds := []string{events.KindSystem, events.KindAddon}
	if from != "" {
		kinds = append(kinds, events.KindWhisper, events.KindChat)
	}
	for {
		p := log.Read(events.Query{Since: cursor, Kinds: kinds, Limit: events.MaxLimit})
		for _, e := range p.Events {
			line, ok := replyLine(e, from)
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

// LineRequest is one command of LinesAll and the test of its reply's last line.
type LineRequest struct {
	Command string
	Done    func(line string) bool
}

// LineResult is what LinesAll read for one request: the lines of its reply, and whether Done said they ended.
type LineResult struct {
	Lines   []string
	Settled bool
}

// LinesAll is Lines for several commands at once: it sends every ".bot <command>" first, then reads the log once until each request's
// Done has said so or wait (for all of them together) has run out. `command <bot> ...` requests read only that bot's own words, so
// the replies of different bots do not mix; a system line (the "Forwarded command" echo) is offered to every request that is still
// open. It exists for a check over a whole party (the preflight talents): N sequential Lines calls cost N times wait when the bots
// do not answer. Results are in the order of reqs; an error stops at the first command that cannot be sent.
func (c *Commander) LinesAll(ctx context.Context, reqs []LineRequest, wait time.Duration) ([]LineResult, error) {
	cmds := make([]string, len(reqs))
	for i, r := range reqs {
		cmds[i] = strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(r.Command), ".bot "))
		if err := CheckBotLine(cmds[i]); err != nil {
			return nil, err
		}
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	log := c.link.Log()
	since := log.Last()
	for _, cmd := range cmds {
		if err := c.link.Say(ctx, Chat(cmd)); err != nil {
			return nil, err
		}
	}
	if wait <= 0 {
		wait = DefaultOrderWait
	}
	wctx, cancel := context.WithTimeout(ctx, wait)
	defer cancel()
	out := make([]LineResult, len(reqs))
	for i := range out {
		out[i].Lines = []string{}
	}
	open := len(reqs)
	cursor := since
	for open > 0 {
		p := log.Read(events.Query{Since: cursor, Kinds: []string{events.KindSystem, events.KindAddon, events.KindWhisper, events.KindChat}, Limit: events.MaxLimit})
		for _, e := range p.Events {
			for i, r := range reqs {
				if out[i].Settled {
					continue
				}
				line, ok := replyLine(e, replyFrom(cmds[i]))
				if !ok || strings.HasPrefix(line, ViewPrefix) {
					continue
				}
				if len(out[i].Lines) < maxLines {
					out[i].Lines = append(out[i].Lines, line)
				}
				if r.Done(line) {
					out[i].Settled = true
					open--
				}
			}
		}
		cursor = p.Next
		if p.More {
			continue
		}
		if open == 0 {
			break
		}
		if log.Wait(wctx, cursor) != nil {
			if err := ctx.Err(); err != nil {
				return out, err
			}
			break
		}
	}
	return out, nil
}

// replyLine is the text of an event that can be part of a reply: a system or addon line, or (with from set) the named bot's own words. Only
// the named bot's chat counts, not a whisper inform of ours and not another player's chat.
func replyLine(e events.Event, from string) (string, bool) {
	if e.Kind == events.KindWhisper || e.Kind == events.KindChat {
		return e.Text, from != "" && strings.EqualFold(e.Source, from) && e.Text != "" && e.Data["type"] != "whisper_inform"
	}
	return LineOf(e)
}

// MaxBurst bounds how long Burst keeps reading after a reply began, however fast the lines keep coming.
const MaxBurst = 3 * time.Second

// Burst sends ".bot <command>" for a command whose answer is a run of lines that has no last-line marker (a bot's `spells <filter>`: a
// header, then one spell per line). It waits up to wait for a line that begin accepts; the reply then goes on while lines keep coming,
// and ends when none came for quiet (at most MaxBurst after it began). The lines from the first one on are returned, began says whether
// the reply started at all. One command is in flight at a time, like Lines.
func (c *Commander) Burst(ctx context.Context, command string, wait, quiet time.Duration, begin func(line string) bool) (lines []string, began bool, err error) {
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
	if quiet <= 0 {
		quiet = 300 * time.Millisecond
	}
	lines = []string{}
	cursor := since
	from := replyFrom(command)
	kinds := []string{events.KindSystem, events.KindAddon}
	if from != "" {
		kinds = append(kinds, events.KindWhisper, events.KindChat)
	}
	deadline := time.Now().Add(wait)
	var limit time.Time
	for {
		p := log.Read(events.Query{Since: cursor, Kinds: kinds, Limit: events.MaxLimit})
		for _, e := range p.Events {
			line, ok := replyLine(e, from)
			if !ok || strings.HasPrefix(line, ViewPrefix) {
				continue
			}
			if !began {
				if !begin(line) {
					continue
				}
				began, limit = true, time.Now().Add(MaxBurst)
			}
			if len(lines) < maxLines {
				lines = append(lines, line)
			}
			deadline = time.Now().Add(quiet)
			if deadline.After(limit) {
				deadline = limit
			}
		}
		cursor = p.Next
		if p.More {
			continue
		}
		wctx, cancel := context.WithDeadline(ctx, deadline)
		werr := log.Wait(wctx, cursor)
		cancel()
		if werr != nil {
			if err := ctx.Err(); err != nil {
				return lines, began, err
			}
			return lines, began, nil
		}
	}
}

// Who is what a bot answered to `.bot command <bot> who`: "protection (0/0/36) warrior (43 lvl), 1021 GS (5/2), (Area), playing with X".
type Who struct {
	Class string // lower case, as the module names it
	Level int
	Trees [3]int
}

var whoRe = regexp.MustCompile(`(?i)\((\d+)/(\d+)/(\d+)\)\s+([a-z]+)\s+\((\d+) lvl\)`)

// ParseWho reads the bot's `who` answer out of its reply lines; ok is false when none of them is one.
func ParseWho(lines []string) (w Who, ok bool) {
	for _, l := range lines {
		m := whoRe.FindStringSubmatch(markup.ReplaceAllString(l, ""))
		if m == nil {
			continue
		}
		for i := range w.Trees {
			w.Trees[i], _ = strconv.Atoi(m[i+1])
		}
		w.Class = strings.ToLower(m[4])
		w.Level, _ = strconv.Atoi(m[5])
		return w, true
	}
	return Who{}, false
}

// Talents is what a bot answered to `.bot command <bot> talents`: "My current talent spec is: Protection (0/5/31) Link: ...".
type Talents struct {
	Trees [3]int // the points spent in each tree
	Total int
}

var (
	talentsRe = regexp.MustCompile(`(?i)talent spec is:.*?\((\d+)/(\d+)/(\d+)\)`)
	// markup is the colour and link codes the module's text carries; a stray "|h" survives the session's colour strip.
	markup = regexp.MustCompile(`(?i)\|c[0-9a-f]{8}|\|[hr]`)
)

// ParseTalents reads the spent points per tree out of the bot's reply lines; ok is false when none of them is the spec line.
func ParseTalents(lines []string) (t Talents, ok bool) {
	for _, l := range lines {
		m := talentsRe.FindStringSubmatch(markup.ReplaceAllString(l, ""))
		if m == nil {
			continue
		}
		for i := range t.Trees {
			t.Trees[i], _ = strconv.Atoi(m[i+1])
			t.Total += t.Trees[i]
		}
		return t, true
	}
	return Talents{}, false
}
