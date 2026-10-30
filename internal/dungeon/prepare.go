package dungeon

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"slices"
	"strings"
	"time"

	"github.com/pjerra/turtle-agent/internal/packet"
	"github.com/pjerra/turtle-agent/internal/tbm"
)

// The prepare step of a run (issue #83, owner request 2026-10-14: "why does setup take 11 minutes?"). A tester used to spend the setup
// on about 30 one-by-one calls: `.bot add` x5, `.bot maintenance` x5, talents, `.instance unbind all` per bot, the bind, a tele, the
// summons. dungeon_run's optional `prepare` block does the part that comes before the bind in one go, and reports what each step cost:
//
//	online:   `.bot add <Name>` for each named bot the roster does not show online (the agent must be their owner or a GM), then the
//	          roster is read until every one is online, within online_wait_s.
//	fresh:    (remote mode, GM) a new instance instead of the old one. The account's hourly entry ledger is checked first. The bots and
//	          the agent are teleported to the plan's outside_tele (a bot cannot be unbound from the map it stands on). Then, from account
//	          level 4: the bots leave their old group (a group keeps its own bind) and `.instance unbind all` goes out for each bot BY
//	          NAME (it selects that bot's GUID and sends the line as one operation, never a bare line on whatever the agent has
//	          selected). Below level 4 the core unbinds the sender instead of the selection, so the bots are bound into their own group
//	          first (`commander bind`, checked to hold exactly the lead and the bots), `.instance groupunbind all` goes out with the
//	          lead bot selected (every member is unbound, the group disbanded; the reply is checked), and they are bound again, checked
//	          again. The binding stays for the run's own bind to reuse. The agent unbinds itself last.
//	gm_setup: `.gm visible off|on` lines, and nothing else.
//	give_items: (remote mode, account level 3) the run option give_items: `.additem <entry>` with the named bot selected, then the bag check
//	          (`.bot inv`). It is how a test party gets an item no honest run has (the Mallet of Zul'Farrak that rings Zul'Farrak's gong);
//	          the line is listed in setup_shortcuts as "additem <entry> on <bot> (test setup: <item name>)". It runs last, after the hire.
//	party:    "hire" (hire.go): the party is five hired bots at the instance's level instead of the named bots. Hire, check, bind; sends no
//	          GM line. The run takes the lead bot, tank and healer from it (lead_bot, bots, tank and healer stay empty).
//
// The preflight (maintenance, inventory, talents) and the enter phase then run as before, back to back. Every GM line the step sends is
// a setup shortcut of the run (setup_shortcuts, and a gm_shortcut event of kind "setup" in the trace), never hidden in gm_commands.

// PrepareSpec is dungeon_run's `prepare` block.
type PrepareSpec struct {
	Online bool `json:"online,omitempty"`
	Fresh  bool `json:"fresh,omitempty"`
	// GMSetup are the GM setup lines to send: "gm visible off" or "gm visible on" (a leading dot is allowed), nothing else.
	GMSetup []string `json:"gm_setup,omitempty"`
	// OnlineWaitS is the longest wait for the bots to show online after `.bot add`, in seconds (default 45, at most 180).
	OnlineWaitS int `json:"online_wait_s,omitempty"`
	// Party is "" (the run's named bots) or "hire" (hire.go): the commander hires its party, checks it and binds it. Hire tunes it.
	Party string    `json:"party,omitempty"`
	Hire  *HireSpec `json:"hire,omitempty"`
	// GiveItems is dungeon_run's give_items run option (the run copies it here; it is not a field of the prepare block's JSON).
	GiveItems []GiveItem `json:"-"`
}

// GiveItem is one entry of dungeon_run's give_items: a GM `.additem` on a bot, for a test run whose plan has an optional leg that needs the item
// (Leg.Optional). Bot is "lead" (the lead bot, also when empty), "carrier" (the run's carrier option, else the bot the object steps use) or a
// bot's name. Name is the item's name for the setup_shortcuts line; empty takes the plan's own name for the item.
type GiveItem struct {
	Bot  string `json:"bot,omitempty"`
	Item uint32 `json:"item"`
	Name string `json:"name,omitempty"`
}

// maxGiveItems is the most entries give_items takes.
const maxGiveItems = 4

// Active says whether the block asks for anything.
func (s PrepareSpec) Active() bool {
	return s.Online || s.Fresh || len(s.GMSetup) > 0 || s.Party != "" || len(s.GiveItems) > 0
}

// HiredParty says whether the party is hired (the run's bot names come from the prepare step).
func (s PrepareSpec) HiredParty() bool { return strings.EqualFold(s.Party, PartyHire) }

// Defaults of the prepare step.
const (
	DefaultOnlineWait = 45 * time.Second
	MaxOnlineWait     = 180 * time.Second
	prepareRosterWait = 6 * time.Second // one `.bot roster` or `.bot add` answer
	prepareLeaveWait  = 3 * time.Second // a bot's answer to `command <bot> leave`
	// prepareRosterGroupWait is how long the roster is polled for the bots to show ungrouped after the leave commands.
	prepareRosterGroupWait = 10 * time.Second
	// bindReadbackWait is how long the bound party is read back until it matches the named one (a bot mid-teleport or loading shows late).
	bindReadbackWait  = 10 * time.Second
	prepareFreshWait  = 25 * time.Second
	preparePoll       = 2 * time.Second
	maxPrepareGMSetup = 4

	// AdministratorLevel is SEC_ADMINISTRATOR: the level from which `.instance unbind` acts on the SELECTED player (Commands.cpp,
	// HandleInstanceUnbindCommand). Below it the core unbinds the sender's own character whatever is selected.
	AdministratorLevel = 4
)

// Validate checks the block. remote says whether the run is in remote mode; a group run takes none (its leader is the agent's own
// character, already in the world and in its party: the caller's `setup` list names the GM lines it ran).
func (s PrepareSpec) Validate(remote bool) error {
	if !remote {
		if s.Active() || s.OnlineWaitS != 0 {
			return errors.New(`prepare is for mode "remote": a group run's leader is the agent's own character, so there is nothing to log in, reset or hide`)
		}
		return nil
	}
	if s.OnlineWaitS < 0 || s.OnlineWaitS > int(MaxOnlineWait/time.Second) {
		return fmt.Errorf("prepare.online_wait_s must be 0 to %d", int(MaxOnlineWait/time.Second))
	}
	switch {
	case s.Party == "":
		if s.Hire != nil {
			return errors.New(`prepare.hire goes with prepare.party "hire"`)
		}
	case !s.HiredParty():
		return fmt.Errorf(`prepare.party: %q is not "hire" (leave it out for the run's named bots)`, s.Party)
	case s.Online || s.Fresh:
		return errors.New(`prepare.party "hire" brings its own bots: online and fresh do not apply (new hires have no saved instance and are online; rehire gives a clean one)`)
	default:
		if err := s.Hire.Validate(); err != nil {
			return err
		}
	}
	if len(s.GMSetup) > maxPrepareGMSetup {
		return fmt.Errorf("prepare.gm_setup: at most %d lines", maxPrepareGMSetup)
	}
	for _, l := range s.GMSetup {
		if _, _, err := ParseGMSetup(l); err != nil {
			return err
		}
	}
	return ValidateGiveItems(s.GiveItems)
}

// ValidateGiveItems checks the give_items run option.
func ValidateGiveItems(gs []GiveItem) error {
	if len(gs) > maxGiveItems {
		return fmt.Errorf("give_items: at most %d entries", maxGiveItems)
	}
	for i, g := range gs {
		switch b := strings.ToLower(strings.TrimSpace(g.Bot)); {
		case g.Item == 0:
			return fmt.Errorf("give_items[%d].item: required (the item entry)", i)
		case g.Bot != strings.TrimSpace(g.Bot):
			return fmt.Errorf("give_items[%d].bot: %q has spaces around it", i, g.Bot)
		case b == "" || b == GiveLead || b == GiveCarrier || botNameRe.MatchString(g.Bot):
		default:
			return fmt.Errorf("give_items[%d].bot: %q is neither %q, %q nor a character name", i, g.Bot, GiveLead, GiveCarrier)
		}
		if len(g.Name) > 60 || strings.ContainsAny(g.Name, "\r\n") {
			return fmt.Errorf("give_items[%d].name: at most 60 characters on one line", i)
		}
	}
	return nil
}

// The two roles give_items names besides a bot's own name.
const (
	GiveLead    = "lead"
	GiveCarrier = "carrier"
)

// ParseGMSetup reads a gm_setup line. It accepts ".gm visible off" and ".gm visible on" (case, spacing and the dot are free) and refuses
// every other text: the prepare step sends no other GM setup line. text is the canonical form, on the value.
func ParseGMSetup(line string) (text string, on bool, err error) {
	f := strings.Fields(strings.ToLower(strings.TrimPrefix(strings.TrimSpace(line), ".")))
	if len(f) == 3 && f[0] == "gm" && f[1] == "visible" && (f[2] == "off" || f[2] == "on") {
		return strings.Join(f, " "), f[2] == "on", nil
	}
	return "", false, fmt.Errorf("prepare.gm_setup: %q is not allowed: only \"gm visible off\" and \"gm visible on\"", line)
}

// botNameRe is what a character name can be (1.12 names are letters).
var botNameRe = regexp.MustCompile(`^[A-Za-z]{2,12}$`)

// PrepareStep is one step of the prepare block with what it cost.
type PrepareStep struct {
	Name    string  `json:"name"` // online, fresh or gm_setup
	Seconds float64 `json:"seconds"`
	Detail  string  `json:"detail,omitempty"`
}

// PrepareReport is the report's `prepare`.
type PrepareReport struct {
	Steps   []PrepareStep `json:"steps"`
	Seconds float64       `json:"seconds"` // all steps together
	// Online: the bots that were online already, and those the step logged in with `.bot add`.
	AlreadyOnline []string `json:"already_online,omitempty"`
	Added         []string `json:"added,omitempty"`
	// Fresh: who was teleported out, who left its group, and for whom `.instance unbind all` went out (the reply goes to the unbound
	// player, so it is sent, not confirmed).
	TeleportedOut []string `json:"teleported_out,omitempty"`
	LeftGroup     []string `json:"left_group,omitempty"`
	// Unbound: `.instance unbind all` was sent with that player selected (account level 4 and up for the bots; the agent unbinds itself).
	// GroupUnbound: the reply confirmed `.instance groupunbind all` on that bot: its whole group was unbound and disbanded (level 3).
	// NotGrouped: the reply said the bot is in no group, so that command did nothing for it.
	Unbound      []string `json:"unbound,omitempty"`
	GroupUnbound []string `json:"group_unbound,omitempty"`
	NotGrouped   []string `json:"not_grouped,omitempty"`
	GMSetup      []string `json:"gm_setup,omitempty"`
	// GMCommands counts the GM lines the step sent (tele, unbind, gm visible): setup shortcuts, not part of the run's own gm_commands.
	GMCommands int      `json:"gm_commands"`
	Warnings   []string `json:"warnings,omitempty"`
	// Ready is the hire step's ready block (the run's report carries it as `ready`).
	Ready *ReadyReport `json:"-"`
}

func (p *PrepareReport) clone() *PrepareReport {
	if p == nil {
		return nil
	}
	c := *p
	c.Steps = slices.Clone(p.Steps)
	c.AlreadyOnline = slices.Clone(p.AlreadyOnline)
	c.Added = slices.Clone(p.Added)
	c.TeleportedOut = slices.Clone(p.TeleportedOut)
	c.LeftGroup = slices.Clone(p.LeftGroup)
	c.Unbound = slices.Clone(p.Unbound)
	c.GroupUnbound = slices.Clone(p.GroupUnbound)
	c.NotGrouped = slices.Clone(p.NotGrouped)
	c.GMSetup = slices.Clone(p.GMSetup)
	c.Warnings = slices.Clone(p.Warnings)
	c.Ready = p.Ready.clone()
	return &c
}

// Preparer is what the prepare step needs of the game: plain `.bot` lines (roster, add, command <bot> leave), the account level, the
// GM lines it may send and a look at the agent's own surroundings. The agent implements it over the session (cmd/turtle-agent).
type Preparer interface {
	BotLiner
	// AccountLevel is the agent's account security level (the group driver's `.account` probe).
	AccountLevel(ctx context.Context) (level int, known bool, err error)
	// TeleName sends `.tele name <player> <location>`.
	TeleName(ctx context.Context, player, location string) error
	// AgentName and AgentGUID are the agent's own character; AgentMap the map it stands on.
	AgentName() string
	AgentGUID() packet.GUID
	AgentMap() uint32
	// Sees says whether the agent's own character has the player in view (its object store lists it).
	Sees(name string) bool
	// GMVisible sends `.gm visible on|off`.
	GMVisible(ctx context.Context, on bool) error
	// UnbindInstances selects the player with the GUID and sends `.instance unbind all`, as one operation. who is the name, for errors.
	// The core runs it on the selected player only from account level 4; below, on the sender. Its reply goes to the unbound player.
	UnbindInstances(ctx context.Context, who string, guid packet.GUID) error
	// GroupUnbind selects the player with the GUID and sends `.instance groupunbind all`, as one operation, and reads the reply the
	// sender gets: "Group unbound. Disbanding." (every in-world member of the player's group is unbound and the group disbanded) or
	// "<name> is not in a group." It works from account level 3.
	// The line acts on the SELECTED player, and a wrong one is another player's group: the implementation selects the GUID, waits until the
	// server shows that selection (an error when it does not, with nothing sent), and classifies the reply: a "not in a group" naming the
	// agent is GroupWrongTarget, one naming a third player GroupThirdPlayer.
	GroupUnbind(ctx context.Context, who string, guid packet.GUID) (GroupUnbindResult, error)
}

// GroupUnbindResult is what the sender's reply to `.instance groupunbind all` said.
type GroupUnbindResult string

const (
	GroupUnbound   GroupUnbindResult = "unbound"     // "Group unbound. Disbanding."
	GroupNotGroups GroupUnbindResult = "not-grouped" // "<name> is not in a group."
	GroupNoAnswer  GroupUnbindResult = "no-answer"   // neither came in time
	// GroupWrongTarget: "<name> is not in a group." named somebody else than the player that was selected: the selection did not reach
	// the server before the line (the core queues the two packets apart), so the line ran on the sender.
	GroupWrongTarget GroupUnbindResult = "wrong-target"
	// GroupThirdPlayer: "<name> is not in a group." named a player that is neither the lead bot nor the agent: the line ran on somebody
	// else. Nothing is sent again.
	GroupThirdPlayer GroupUnbindResult = "third-player"
)

// prepEnv is what the prepare step needs of the run around it: its clock, its notes and its shortcut book.
type prepEnv struct {
	g    Preparer
	plan *Plan
	spec PrepareSpec
	bots []string // the characters of the run, lead first
	// memory is what an earlier run read of the hires of a kept binding (RemoteOptions.HireMemory).
	memory map[string]tbm.Who
	// remote: the run is in remote mode (the only mode that prepares).
	remote bool
	// maintain: the hire check may send `.bot maintenance` (observe mode: only with observe.maintenance, #253).
	maintain bool
	now      func() time.Time
	sleep    func(ctx context.Context, d time.Duration) error
	note     func(string)
	// shortcut books a GM line the step sent (setup_shortcuts and the trace).
	shortcut func(line string)
	// entryOK is the ledger check before a fresh entry (nil: no ledger).
	entryOK func() error
	// progress tells the run's watchdog that the step moved on.
	progress func(string)
	// command sends a commander command (`commander bind|unbind|status`) and returns its reply. Level 3 needs it: the group unbind is
	// preceded and followed by a bind.
	command func(ctx context.Context, line string) (tbm.Reply, error)
	// hired is set by the hire step: the party it bound.
	hired *HiredParty
	// bound is set when the step left a binding standing (the run's own bind then reuses it); settle is the wait after a bind or a
	// `leave` before it is checked (0: DefaultBindSettle).
	bound  bool
	settle time.Duration

	// tank, healer and carrier are the run's named roles (the hire step's own win): give_items resolves "carrier" the way the object steps do.
	tank, healer, carrier string

	onlineWait time.Duration // 0: DefaultOnlineWait
	freshWait  time.Duration // 0: prepareFreshWait
	poll       time.Duration // 0: preparePoll

	rep    PrepareReport
	rows   []tbm.RosterRow // the newest roster
	level  int
	levelK bool
	probed bool
}

// runPrepare does the steps in order: online, fresh, gm_setup. The partial report comes back with an error.
func runPrepare(ctx context.Context, e *prepEnv) (*PrepareReport, error) {
	began := e.now()
	e.rep = PrepareReport{Steps: []PrepareStep{}}
	if e.onlineWait <= 0 {
		e.onlineWait = DefaultOnlineWait
	}
	if e.spec.OnlineWaitS > 0 {
		e.onlineWait = min(time.Duration(e.spec.OnlineWaitS)*time.Second, MaxOnlineWait)
	}
	if e.freshWait <= 0 {
		e.freshWait = prepareFreshWait
	}
	if e.poll <= 0 {
		e.poll = preparePoll
	}
	finish := func(err error) (*PrepareReport, error) {
		e.rep.Seconds = e.now().Sub(began).Seconds()
		return &e.rep, err
	}
	if err := e.spec.Validate(e.remote); err != nil {
		return finish(err)
	}
	type step struct {
		name string
		on   bool
		run  func(context.Context) (string, error)
	}
	for _, s := range []step{
		{"hire", e.spec.HiredParty() && (e.plan == nil || !e.plan.IsRaid()), e.hire},
		{"hire", e.spec.HiredParty() && e.plan != nil && e.plan.IsRaid(), e.raidHire},
		{"online", e.spec.Online, e.online},
		{"fresh", e.spec.Fresh, e.fresh},
		{"gm_setup", len(e.spec.GMSetup) > 0, e.gmSetup},
		{"give_items", len(e.spec.GiveItems) > 0, e.giveItems},
	} {
		if !s.on {
			continue
		}
		if err := ctx.Err(); err != nil {
			return finish(err)
		}
		at := e.now()
		detail, err := s.run(ctx)
		e.rep.Steps = append(e.rep.Steps, PrepareStep{Name: s.name, Seconds: e.now().Sub(at).Seconds(), Detail: detail})
		if err != nil {
			return finish(fmt.Errorf("prepare %s: %w", s.name, err))
		}
		e.note(fmt.Sprintf("prepare %s: %s (%.1f s)", s.name, detail, e.now().Sub(at).Seconds()))
		if e.progress != nil {
			e.progress("prepare " + s.name)
		}
	}
	return finish(nil)
}

// gmLevel probes the account level once.
func (e *prepEnv) gmLevel(ctx context.Context) (int, error) {
	if !e.probed {
		lv, known, err := e.g.AccountLevel(ctx)
		if err != nil {
			return 0, fmt.Errorf("the account level could not be read: %w", err)
		}
		e.probed, e.level, e.levelK = true, lv, known
	}
	if !e.levelK {
		return 0, errors.New("the server did not answer .account: the account level is unknown")
	}
	return e.level, nil
}

// ---- online ----

// readRoster sends `.bot roster` and keeps the rows.
func (e *prepEnv) readRoster(ctx context.Context) error {
	var st tbm.Status
	_, settled, err := e.g.BotLines(ctx, "roster", prepareRosterWait, func(line string) bool {
		if !strings.HasPrefix(line, "TBM:") {
			return false
		}
		st.Add(line)
		return strings.HasPrefix(line, "TBM:ROSTER_END") || strings.HasPrefix(line, "TBM:ROSTER_ERROR")
	})
	switch {
	case err != nil:
		return fmt.Errorf(".bot roster: %w", err)
	case !settled:
		return errors.New(".bot roster: no answer (does the server run a TortoiseBots build with the roster?)")
	case len(st.Errors) > 0:
		return fmt.Errorf(".bot roster: %s", strings.Join(st.Errors, "; "))
	}
	e.rows = st.Roster
	return nil
}

func (e *prepEnv) rowOf(name string) (tbm.RosterRow, bool) {
	for _, r := range e.rows {
		if strings.EqualFold(r.Name, name) {
			return r, true
		}
	}
	return tbm.RosterRow{}, false
}

// stateOf is the roster state of a bot: online, starting, removing or offline (also for one the roster does not list).
func (e *prepEnv) stateOf(name string) string {
	if r, ok := e.rowOf(name); ok {
		return strings.ToLower(r.State)
	}
	return "offline"
}

// addVerdict reads one reply line of `.bot add <Name>` (BotCommands.cpp HandleAdd); done is true for a line that ends the reply.
func addVerdict(line string) (verdict string, done bool) {
	low := strings.ToLower(strings.TrimSpace(line))
	switch {
	case strings.Contains(low, "queued for login"):
		return "queued", true
	case strings.Contains(low, "already online"):
		return "already online", true
	case strings.Contains(low, "not found"), strings.Contains(low, "may only control"), strings.Contains(low, "owned by another"),
		strings.Contains(low, "failed to add"), strings.Contains(low, "failed to persist"), strings.HasPrefix(low, "usage:"),
		strings.Contains(low, "invalid character name"), strings.Contains(low, "must be in-game"):
		return "refused: " + strings.TrimSpace(line), true
	}
	return "", false
}

// online logs in the bots the roster does not show online and waits, within the limit, for all of them.
func (e *prepEnv) online(ctx context.Context) (string, error) {
	if len(e.bots) == 0 {
		return "", errors.New("no bot names to bring online")
	}
	if err := e.readRoster(ctx); err != nil {
		return "", err
	}
	var todo []string
	for _, n := range e.bots {
		switch e.stateOf(n) {
		case "online":
			e.rep.AlreadyOnline = append(e.rep.AlreadyOnline, n)
		case "starting":
			// logging in already: only the wait below
		default:
			todo = append(todo, n)
		}
	}
	// A bot the server says is online but the roster does not list (a hired bot: `.bot roster` lists the account's own bots only, ZF-1
	// 2026-10-14) is taken as online on the server's word; waiting for the roster would wait for nothing.
	serverOnline := map[string]bool{}
	for _, n := range todo {
		var verdict string
		_, settled, err := e.g.BotLines(ctx, "add "+n, prepareRosterWait, func(line string) bool {
			v, done := addVerdict(line)
			if done {
				verdict = v
			}
			return done
		})
		switch {
		case err != nil:
			return "", fmt.Errorf(".bot add %s: %w", n, err)
		case !settled:
			e.rep.Warnings = append(e.rep.Warnings, ".bot add "+n+": no answer; waiting for it on the roster")
		case strings.HasPrefix(verdict, "refused"):
			return "", fmt.Errorf(".bot add %s %s (the agent must own the character or be a GM)", n, verdict)
		case verdict == "already online":
			if _, listed := e.rowOf(n); !listed {
				serverOnline[n] = true
				e.rep.AlreadyOnline = append(e.rep.AlreadyOnline, n)
				e.rep.Warnings = append(e.rep.Warnings, ".bot add "+n+": the server says it is already online and the roster does not list it (a hired bot, or one of another owner): taken as online")
				continue
			}
			e.rep.Warnings = append(e.rep.Warnings, ".bot add "+n+": the server says it is already online; waiting for the roster to show it")
		}
		e.rep.Added = append(e.rep.Added, n)
	}
	deadline := e.now().Add(e.onlineWait)
	for {
		if err := e.readRoster(ctx); err != nil {
			return "", err
		}
		var pending []string
		for _, n := range e.bots {
			if st := e.stateOf(n); st != "online" && !serverOnline[n] {
				pending = append(pending, n+" ("+st+")")
			}
		}
		if len(pending) == 0 {
			break
		}
		if !e.now().Before(deadline) {
			return "", fmt.Errorf("%s not online %.0f s after the login (online_wait_s %d)", strings.Join(pending, ", "), e.onlineWait.Seconds(), int(e.onlineWait.Seconds()))
		}
		if err := e.sleep(ctx, e.poll); err != nil {
			return "", err
		}
	}
	return fmt.Sprintf("%d online (%d already, %d logged in)", len(e.bots), len(e.rep.AlreadyOnline), len(e.rep.Added)), nil
}

// ---- fresh ----

// gmLine books a GM line the step sent.
func (e *prepEnv) gmLine(line string) {
	e.rep.GMCommands++
	e.shortcut(line)
}

// fresh gets the party a new instance: out of the old one, out of the old group, unbound (see the top of this file).
func (e *prepEnv) fresh(ctx context.Context) (string, error) {
	p := e.plan
	if !e.remote {
		return "", errors.New("fresh is for remote mode")
	}
	if p.OutsideTele == "" {
		return "", fmt.Errorf("plan %s has no outside_tele (the game_tele name of its entrance on the outer map): bring the party out by hand", p.Name)
	}
	level, err := e.gmLevel(ctx)
	if err != nil {
		return "", fmt.Errorf("%w: fresh needs a GM account", err)
	}
	if level < DeveloperLevel {
		return "", fmt.Errorf("the account is level %d: fresh teleports the bots (.tele name) and unbinds instances, which need a developer-level GM (%d). Bring the group out and reset the instance by hand, or run without fresh", level, DeveloperLevel)
	}
	if e.entryOK != nil {
		if err := e.entryOK(); err != nil {
			return "", err
		}
	}
	// The bots must be online (a tele needs them in the world) and the roster has their GUIDs.
	if len(e.rows) == 0 || !e.spec.Online {
		if err := e.readRoster(ctx); err != nil {
			return "", err
		}
	}
	guids := map[string]packet.GUID{}
	var off []string
	for _, n := range e.bots {
		r, ok := e.rowOf(n)
		if !ok || strings.ToLower(r.State) != "online" || r.GUID == 0 {
			off = append(off, n+" ("+e.stateOf(n)+")")
			continue
		}
		guids[n] = packet.GUID(r.GUID)
	}
	if len(off) > 0 {
		return "", fmt.Errorf("%s not online: fresh needs the bots in the world (prepare online true)", strings.Join(off, ", "))
	}

	// 1. Out of the old instance: the bots, then the agent. `.instance unbind` leaves the map a player stands on alone.
	agent := e.g.AgentName()
	for _, n := range append(slices.Clone(e.bots), agent) {
		if err := e.g.TeleName(ctx, n, p.OutsideTele); err != nil {
			return "", fmt.Errorf(".tele name %s %s: %w", n, p.OutsideTele, err)
		}
		e.gmLine(fmt.Sprintf(".tele name %s %s (prepare fresh)", n, p.OutsideTele))
		e.rep.TeleportedOut = append(e.rep.TeleportedOut, n)
	}
	if err := e.waitOutside(ctx); err != nil {
		return "", err
	}
	// 2. The bots' binds and group. From account level 4 `.instance unbind all` runs on the selected bot, so each bot is unbound by name
	// and leaves its group. Below it the core unbinds the SENDER whatever is selected (Commands.cpp, HandleInstanceUnbindCommand), which
	// would clear the agent five times and no bot: there `.instance groupunbind all` goes out with the lead bot selected (it unbinds
	// every in-world member of its group, disbands the group, and tells the sender so). Only a confirmed reply is reported.
	if level >= AdministratorLevel {
		if err := e.leaveGroups(ctx, e.bots); err != nil {
			return "", err
		}
		for _, n := range e.bots {
			if err := e.g.UnbindInstances(ctx, n, guids[n]); err != nil {
				return "", fmt.Errorf(".instance unbind all on %s: %w", n, err)
			}
			e.gmLine(".instance unbind all on " + n + " (prepare fresh)")
			e.rep.Unbound = append(e.rep.Unbound, n)
		}
	} else if err := e.groupReset(ctx, guids); err != nil {
		return "", err
	}
	if err := e.g.UnbindInstances(ctx, agent, e.g.AgentGUID()); err != nil {
		return "", fmt.Errorf(".instance unbind all on %s: %w", agent, err)
	}
	e.gmLine(".instance unbind all on " + agent + " (prepare fresh)")
	e.rep.Unbound = append(e.rep.Unbound, agent)
	unbound := fmt.Sprintf("%s unbound", strings.Join(e.rep.Unbound, ", "))
	if len(e.rep.GroupUnbound) > 0 {
		unbound = fmt.Sprintf("the group of %s unbound and disbanded (confirmed by the reply), %s", strings.Join(e.rep.GroupUnbound, ", "), unbound)
	}
	return fmt.Sprintf("%d bots and the agent teleported to %s; %s", len(e.bots), p.OutsideTele, unbound), nil
}

// leaveGroups sends `command <bot> leave` for each bot (a bot in no group is harmless). The module only forwards the command ("action
// success is not guaranteed") and answers at once, so the bots are given leaveSettle to act on it: a leave that lands after the next
// bind takes the bot out of the new group (run 6b).
func (e *prepEnv) leaveGroups(ctx context.Context, bots []string) error {
	for _, n := range bots {
		_, settled, err := e.g.BotLines(ctx, "command "+n+" leave", prepareLeaveWait, func(string) bool { return true })
		switch {
		case err != nil:
			e.rep.Warnings = append(e.rep.Warnings, "command "+n+" leave: "+err.Error())
		case !settled:
			e.note("prepare fresh: " + n + " did not answer `leave` (it may have been in no group)")
		}
		e.rep.LeftGroup = append(e.rep.LeftGroup, n)
	}
	return e.waitUngrouped(ctx, bots)
}

// waitUngrouped waits, within prepareRosterGroupWait, until `.bot roster` shows none of the bots grouped: only then is a bind safe from a
// late `leave`. A bot still grouped at the end is a warning (the bind's own check catches a bot that went missing from the group).
func (e *prepEnv) waitUngrouped(ctx context.Context, bots []string) error {
	deadline := e.now().Add(prepareRosterGroupWait)
	for {
		if err := e.readRoster(ctx); err != nil {
			e.rep.Warnings = append(e.rep.Warnings, "roster after the leave commands: "+err.Error())
			return nil
		}
		var grouped []string
		for _, n := range bots {
			if r, ok := e.rowOf(n); ok && r.Grouped {
				grouped = append(grouped, n)
			}
		}
		if len(grouped) == 0 {
			return nil
		}
		if !e.now().Before(deadline) {
			w := fmt.Sprintf("%s still grouped %.0f s after `leave`", strings.Join(grouped, ", "), prepareRosterGroupWait.Seconds())
			e.rep.Warnings = append(e.rep.Warnings, w)
			e.note("prepare fresh: warning: " + w)
			return nil
		}
		if err := e.sleep(ctx, time.Second); err != nil {
			return err
		}
	}
}

// groupReset is fresh below account level 4, where `.instance unbind` acts on the sender and only `.instance groupunbind all` reaches a
// bot (it unbinds every in-world member of the selected bot's group and disbands the group). So the group must exist first, with every
// named bot in it, and be formed again after:
//
//	commander bind (the bots-only group with the lead, checked) -> groupunbind on the lead (the reply is checked) -> commander bind again.
//
// The bots are never sent `leave` on this path: a bot the bind has to take from an old group of its own is told to leave only when the
// bind refuses it (member-grouped), and then the bind is made again.
func (e *prepEnv) groupReset(ctx context.Context, guids map[string]packet.GUID) error {
	if e.command == nil {
		return errors.New("below account level 4 fresh binds the bots, unbinds their group and binds again, and this game cannot send commander commands")
	}
	lead := e.bots[0]
	if err := e.bindBots(ctx, "before the group unbind"); err != nil {
		return err
	}
	// The implementation sends the line only once the server shows the lead bot as the agent's selection. A reply that names the AGENT
	// means the line still ran on the sender: it is sent again, at most three times in all (each send is a GM line of the setup). A reply
	// that names any third player stops the step at once, and nothing is sent again.
	var res GroupUnbindResult
	for try := 0; try < 3; try++ {
		var err error
		if res, err = e.g.GroupUnbind(ctx, lead, guids[lead]); err != nil {
			return fmt.Errorf(".instance groupunbind all on %s: %w", lead, err)
		}
		e.gmLine(".instance groupunbind all on " + lead + " (prepare fresh)")
		if res != GroupWrongTarget {
			break
		}
		e.note(fmt.Sprintf("prepare fresh: .instance groupunbind all on %s ran on the agent itself; selecting %s again and sending it again", lead, lead))
	}
	switch res {
	case GroupUnbound:
		// The reply does not say whose group it was. The lead bot's group is the one just bound, so disbanding it ends the binding: the
		// module says so on the next `commander status`. A binding that still stands means the line disbanded somebody else's group.
		if err := e.bindingEnded(ctx); err != nil {
			return err
		}
		e.bound = false
		e.rep.GroupUnbound = append(e.rep.GroupUnbound, lead)
		e.note(fmt.Sprintf("prepare fresh: .instance groupunbind all on %s: \"Group unbound. Disbanding.\" (the group held %s)", lead, strings.Join(e.bots, ", ")))
	case GroupNotGroups:
		e.rep.NotGrouped = append(e.rep.NotGrouped, lead)
		return fmt.Errorf(".instance groupunbind all on %s: \"is not in a group\" right after the bind formed its group: the binds of the bots are not cleared", lead)
	case GroupWrongTarget:
		return fmt.Errorf(".instance groupunbind all on %s: the reply named the agent itself in all three tries, so the selection of %s did not take: the binds of the bots are not cleared", lead, lead)
	case GroupThirdPlayer:
		return fmt.Errorf("the unbind hit another target: the reply to .instance groupunbind all named a player who is neither %s nor the agent, so the line ran on somebody else's selection and may have unbound and disbanded that player's group. Stopping; nothing was sent again", lead)
	default:
		return fmt.Errorf(".instance groupunbind all on %s: no reply (\"Group unbound. Disbanding.\" or \"is not in a group\" expected): the unbind is not confirmed", lead)
	}
	return e.bindBots(ctx, "after the group unbind")
}

// bindingEnded checks that the binding is gone after a "Group unbound. Disbanding." reply: `commander status` answers not-bound. Anything
// else stops the run, because the reply carries no name and the line may have disbanded another group.
func (e *prepEnv) bindingEnded(ctx context.Context) error {
	r, err := e.command(ctx, "commander status")
	if err != nil {
		return fmt.Errorf("commander status after the group unbind: %w", err)
	}
	switch {
	case r.Kind == tbm.KindErr && r.Err != nil && r.Err.Code == "not-bound":
		return nil
	case r.Kind == tbm.KindAck:
		return fmt.Errorf("the unbind hit another target: \"Group unbound. Disbanding.\" came, but commander status still shows %s's group bound, so the line disbanded a group that is not the bots'. Stopping", e.bots[0])
	}
	return fmt.Errorf("the unbind is not confirmed: \"Group unbound. Disbanding.\" came, but commander status gave no not-bound answer (%s), so it is not known whose group was disbanded. Stopping", r.Kind)
}

// bindBots binds the lead and the bots of the run and checks the bound group holds exactly them (`commander status`: lead, count and
// listing), after a settle. A refusal that a step can fix is fixed once: an old binding of the account is ended (already-bound), a bot in
// some other group is told to leave (member-grouped, bot-bound); a bot the bind left out is invited by one unbind and bind again. Any
// other refusal, or a bot still missing, is an error that names it: the run does not go on with a partial party.
func (e *prepEnv) bindBots(ctx context.Context, when string) error {
	lead, others := e.bots[0], e.bots[1:]
	line := bindCommand(lead, others)
	retried, rebound := false, false
	for {
		r, err := e.command(ctx, line)
		if err != nil {
			return fmt.Errorf("commander bind %s: %w", when, err)
		}
		switch r.Kind {
		case tbm.KindErr:
			code := ""
			if r.Err != nil {
				code = r.Err.Code
			}
			// A hire told to `leave` its party is dismissed (its character is deleted): for hires only an old binding is fixed here.
			if retried || (code != "already-bound" && code != "member-grouped" && code != "bot-bound") || (e.spec.HiredParty() && code != "already-bound") {
				return fmt.Errorf("commander bind %s refused: %s", when, describeBindErr(r.Err))
			}
			retried = true
			e.note(fmt.Sprintf("%s: commander bind %s refused (%s): fixing it and binding again", e.tag(), when, code))
			if code == "already-bound" {
				if err := e.unbindCommander(ctx); err != nil {
					return err
				}
			} else if err := e.leaveGroups(ctx, e.bots); err != nil {
				return err
			}
			continue
		case tbm.KindTimeout:
			return fmt.Errorf("commander bind %s: no ACK or ERR came (does the server run a TortoiseBots build with the remote commander?)", when)
		}
		e.bound = true
		if err := e.sleep(ctx, e.bindSettle()); err != nil {
			return err
		}
		// Read the party back until it matches, within bindReadbackWait: a bot that is loading or teleporting joins a moment late.
		var st boundParty
		var missing, extra, why []string
		for polled := time.Duration(0); ; polled += time.Second {
			if st, err = readBoundParty(ctx, e.command); err != nil {
				return err
			}
			missing, extra, why = partyProblems(lead, e.bots, st, nil, false)
			if (len(missing) == 0 && len(extra) == 0) || polled >= bindReadbackWait {
				break
			}
			if err := e.sleep(ctx, time.Second); err != nil {
				return err
			}
		}
		if len(missing) == 0 && len(extra) == 0 {
			e.note(fmt.Sprintf("%s: commander bind %s: %s with %d bots, exactly as named (commander status)", e.tag(), when, lead, len(others)))
			return nil
		}
		if len(extra) > 0 {
			return fmt.Errorf("commander bind %s: the group holds %s, which the run did not name", when, strings.Join(extra, ", "))
		}
		if rebound {
			return fmt.Errorf("commander bind %s: %s not in the bound group after binding again (%s; the bind ACKed %d bots)", when, strings.Join(missing, ", "), strings.Join(why, "; "), r.Ack.Count)
		}
		rebound = true
		e.note(fmt.Sprintf("%s: commander bind %s: %s; unbinding and binding again to invite %s", e.tag(), when, strings.Join(why, "; "), strings.Join(missing, ", ")))
		if err := e.unbindCommander(ctx); err != nil {
			return err
		}
	}
}

// unbindCommander ends the account's binding (not-bound is fine).
func (e *prepEnv) unbindCommander(ctx context.Context) error {
	r, err := e.command(ctx, "commander unbind")
	if err != nil {
		return fmt.Errorf("commander unbind: %w", err)
	}
	if r.Kind == tbm.KindErr && (r.Err == nil || r.Err.Code != "not-bound") {
		return fmt.Errorf("commander unbind refused: %s", describeBindErr(r.Err))
	}
	e.bound = false
	return nil
}

// tag names the step in the notes of the shared bind code.
func (e *prepEnv) tag() string {
	if e.spec.HiredParty() {
		return "prepare hire"
	}
	return "prepare fresh"
}

func (e *prepEnv) bindSettle() time.Duration {
	if e.settle > 0 {
		return e.settle
	}
	return DefaultBindSettle
}

// waitOutside waits, within freshWait, for the agent to stand outside the plan's map and to have every bot in view: the teleports put
// them all at the same spot.
func (e *prepEnv) waitOutside(ctx context.Context) error {
	deadline := e.now().Add(e.freshWait)
	for {
		var missing []string
		if e.g.AgentMap() == e.plan.Map {
			missing = append(missing, "the agent (still on map "+fmt.Sprint(e.plan.Map)+")")
		}
		for _, n := range e.bots {
			if !e.g.Sees(n) {
				missing = append(missing, n)
			}
		}
		if len(missing) == 0 {
			return nil
		}
		if !e.now().Before(deadline) {
			return fmt.Errorf("after the teleport to %s %s not outside, in the agent's view, within %.0f s", e.plan.OutsideTele, strings.Join(missing, ", "), e.freshWait.Seconds())
		}
		if err := e.sleep(ctx, time.Second); err != nil {
			return err
		}
	}
}

// ---- gm_setup ----

func (e *prepEnv) gmSetup(ctx context.Context) (string, error) {
	level, err := e.gmLevel(ctx)
	if err != nil {
		return "", fmt.Errorf("%w: gm_setup needs a GM account", err)
	}
	if level < ModeratorLevel {
		return "", fmt.Errorf("the account is level %d: .gm visible needs %d", level, ModeratorLevel)
	}
	for _, l := range e.spec.GMSetup {
		text, on, err := ParseGMSetup(l)
		if err != nil {
			return "", err
		}
		if err := e.g.GMVisible(ctx, on); err != nil {
			return "", fmt.Errorf("%s: %w", text, err)
		}
		e.gmLine(text)
		e.rep.GMSetup = append(e.rep.GMSetup, text)
	}
	return strings.Join(e.rep.GMSetup, ", "), nil
}
