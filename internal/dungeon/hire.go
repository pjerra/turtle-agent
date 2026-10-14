package dungeon

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/pjerra/turtle-agent/internal/packet"
	"github.com/pjerra/turtle-agent/internal/tbm"
)

// The hired party of the prepare step (prepare.party "hire"; owner rule 2026-10-14: every commander group is five HIRED bots at the
// instance's level, the commander outside the group, TB #935). The step does, in one go and within a couple of minutes:
//
//  1. looks at the commander's current hires (the group members and the bound listing that the roster does not list: a hire is a pool
//     character, not an owned one). `rehire` ends the old binding and dismisses them all; otherwise the ones that match the wanted
//     class, level and role are kept and the others are dismissed.
//  2. hires what is missing: `.bot hire <class> <role> level=<n>` (the 5th after a raid conversion, which the server asks for in its
//     reply). A server without TB #940 ignores or refuses level=<n>: the hire is then made at the commander's level, and accepted only
//     inside the plan's level band.
//  3. checks every bot ready: level and class, gear (worn items are asked for with CMSG_ITEM_QUERY_SINGLE: no epic, the usual slots
//     filled, a shield on the tank, a ranged weapon on the hunter; `.bot autogear <bot> blue` when not), supplies (`.bot maintenance`,
//     then the bags), the key spells of the class and role (the bot's own `spells <filter>` answer), the talents, and the pet of a hunter
//     or warlock. What a `.bot` command can fix is fixed once and written down; the rest fails the step with the bot and the reason.
//  4. binds (`commander bind <tank> <others>`), checks that the bound group is exactly the hires and that the commander is outside it.
//  5. writes the ready block of the report: per bot the class, level, item level and quality, what was fixed, the spells and supplies seen.
//
// The step sends no GM command (hiring, autogear and maintenance are player commands of the commander's own account), so gm_commands stays
// what the run itself sends; nothing in it uses `.learn` or `.levelup`: a bot that lacks a spell fails the step.

// HireSpec is the `prepare.hire` block.
type HireSpec struct {
	// Composition is the party, "class" or "class:role" each (role tank, healer or dps): a warrior is the tank and a priest the healer
	// unless said otherwise; paladin, shaman and druid need a role. Exactly one tank and one healer, two to five bots. Empty: a warrior
	// tank, a priest healer and three damage dealers from hunter, rogue, warlock, mage.
	Composition []string `json:"composition,omitempty"`
	// Level of the hires. 0: the middle of the plan's level_band.
	Level int `json:"level,omitempty"`
	// Rehire ends the old binding and dismisses the commander's current hires; without it the ones that match are kept (they keep
	// their saved instances, so a fresh instance needs rehire).
	Rehire bool `json:"rehire,omitempty"`
	// NoFix reports what is wrong and fails instead of trying `.bot autogear` and `.bot maintenance` again.
	NoFix bool `json:"no_fix,omitempty"`
	// WaitS is the longest wait for a hire to show up in the group, in seconds (default 30, at most 120).
	WaitS int `json:"wait_s,omitempty"`
}

// Limits and defaults of the hire step.
const (
	PartyHire        = "hire"
	DefaultHireWait  = 30 * time.Second
	MaxHireWait      = 120 * time.Second
	hireReplyWait    = 8 * time.Second  // one `.bot hire` answer
	hireDismissWait  = 12 * time.Second // the dismissed hires leave the group and the binding
	hireStepCeiling  = 240 * time.Second
	hireBotLineWait  = 6 * time.Second // maintenance, autogear, talents
	hireInvWait      = 8 * time.Second
	hireSpellWait    = 3 * time.Second        // the first line of one `spells <filter>` answer
	hireSpellQuiet   = 400 * time.Millisecond // its burst of lines ends when none came for this long
	hireItemWait     = 3 * time.Second
	hirePetWait      = 40 * time.Second // a regeared warlock re-summons its demon some 10 to 30 s later (hires-2 live: 10 s was too short)
	hireOutOfGroup   = 10 * time.Second // the commander leaves the group after a bind
	maxHireComposite = 5
)

var hireDpsPreference = []string{"hunter", "rogue", "warlock", "mage"}

// hireSlot is one wanted bot.
type hireSlot struct{ Class, Role string }

func (s hireSlot) String() string { return s.Class + ":" + s.Role }

// DefaultHireComposition is the 5-man party: a tank, a healer and three damage dealers.
func DefaultHireComposition() []string {
	out := []string{"warrior:tank", "priest:healer"}
	for _, c := range hireDpsPreference[:3] {
		out = append(out, c+":dps")
	}
	return out
}

func (h *HireSpec) slots() ([]hireSlot, error) {
	comp := h.Composition
	if len(comp) == 0 {
		comp = DefaultHireComposition()
	}
	if len(comp) < 2 || len(comp) > maxHireComposite {
		return nil, fmt.Errorf("prepare.hire.composition: %d bots; use 2 to %d (a party holds five)", len(comp), maxHireComposite)
	}
	var out []hireSlot
	tanks, healers := 0, 0
	for _, c := range comp {
		f := strings.Split(strings.ToLower(strings.TrimSpace(c)), ":")
		if len(f) > 2 || !hireClassOK(f[0]) {
			return nil, fmt.Errorf("prepare.hire.composition: %q is not \"class\" or \"class:role\" (classes %s)", c, strings.Join(hireClassList(), ", "))
		}
		s := hireSlot{Class: f[0]}
		if len(f) == 2 {
			s.Role = f[1]
		} else {
			switch s.Class {
			case "warrior":
				s.Role = "tank"
			case "priest":
				s.Role = "healer"
			case "paladin", "shaman", "druid":
				return nil, fmt.Errorf("prepare.hire.composition: %q needs a role (%s:tank, :healer or :dps)", c, s.Class)
			default:
				s.Role = "dps"
			}
		}
		if !slices.Contains([]string{"tank", "healer", "dps"}, s.Role) {
			return nil, fmt.Errorf("prepare.hire.composition: %q: the role is tank, healer or dps", c)
		}
		if !classCanBe(s.Class, s.Role) {
			return nil, fmt.Errorf("prepare.hire.composition: a %s cannot be %s", s.Class, s.Role)
		}
		switch s.Role {
		case "tank":
			tanks++
		case "healer":
			healers++
		}
		out = append(out, s)
	}
	if tanks != 1 || healers != 1 {
		return nil, fmt.Errorf("prepare.hire.composition: %d tanks and %d healers; the run needs exactly one of each", tanks, healers)
	}
	return out, nil
}

// classCanBe says whether the class has a build for the role (the hire command refuses "That class cannot fill that role").
func classCanBe(class, role string) bool {
	switch role {
	case "tank":
		return slices.Contains([]string{"warrior", "paladin", "druid"}, class)
	case "healer":
		return slices.Contains([]string{"priest", "paladin", "shaman", "druid"}, class)
	}
	return true
}

func hireClassOK(c string) bool { return slices.Contains(hireClassList(), c) }

func hireClassList() []string {
	return []string{"warrior", "paladin", "hunter", "rogue", "priest", "shaman", "mage", "warlock", "druid"}
}

// Validate checks the block; the level is checked against the plan when the step runs.
func (h *HireSpec) Validate() error {
	if h == nil {
		return nil
	}
	if h.Level < 0 || h.Level > 60 {
		return errors.New("prepare.hire.level must be 0 (the middle of the plan's level band) or 1 to 60")
	}
	if h.WaitS < 0 || h.WaitS > int(MaxHireWait/time.Second) {
		return fmt.Errorf("prepare.hire.wait_s must be 0 to %d", int(MaxHireWait/time.Second))
	}
	_, err := h.slots()
	return err
}

// ---- what the game gives the step ----

// HireMember is a player the agent knows by name: a member of its own group (the hires, before the bind) or a player in its view.
type HireMember struct {
	Name    string
	GUID    packet.GUID
	Level   int
	Class   string // "warrior", "priest", ... ; "" while the unit is not in the agent's view
	InGroup bool   // a member of the agent's own group
	Online  bool
	Dead    bool
	MaxHP   uint32
}

// ItemInfo is an item prototype as the server describes it (CMSG_ITEM_QUERY_SINGLE).
type ItemInfo struct {
	Found         bool
	Name          string
	Class         int
	SubClass      int
	Quality       int // 3 is rare (blue), 4 epic
	InventoryType int
	ItemLevel     int
	RequiredLevel int
}

// HireGame is what the hire step needs of the game beyond a Preparer.
type HireGame interface {
	// AgentLevel is the commander's own level (0: unknown).
	AgentLevel() int
	// InGroup says whether the commander is in a group.
	InGroup() bool
	// Members are the other members of the commander's group, with what the agent knows of them.
	Members() []HireMember
	// Player looks a player up by name among the group and the units in view.
	Player(name string) (HireMember, bool)
	// PetOf says whether the player has a pet out (a hunter's or warlock's), by its name.
	PetOf(guid packet.GUID) (string, bool)
	// BotBurst sends a plain `.bot` line whose answer is a run of lines (tbm.Commander.Burst): from the first line begin accepts until the
	// lines stop coming for quiet.
	BotBurst(ctx context.Context, command string, wait, quiet time.Duration, begin func(line string) bool) (lines []string, began bool, err error)
	// ConvertRaid turns the commander's party into a raid.
	ConvertRaid(ctx context.Context) error
	// ItemInfos asks the server for the prototypes (those it answers within wait are returned).
	ItemInfos(ctx context.Context, entries []uint32, wait time.Duration) (map[uint32]ItemInfo, error)
}

// ---- the report ----

// ReadyBot is one bot of the ready block.
type ReadyBot struct {
	Name   string `json:"name"`
	Class  string `json:"class"`
	Role   string `json:"role"`
	Level  int    `json:"level"`
	Source string `json:"source"` // hired or reused
	MaxHP  uint32 `json:"max_hp,omitempty"`
	// ItemLevel is the average item level of the worn gear (shirt and tabard left out), Quality the best quality worn ("rare" is blue).
	ItemLevel float64 `json:"ilvl,omitempty"`
	Quality   string  `json:"quality,omitempty"`
	Talents   string  `json:"talents,omitempty"`
	Drinks    int     `json:"drinks,omitempty"`
	Ammo      string  `json:"ammo,omitempty"`
	Shield    *bool   `json:"shield,omitempty"`
	Pet       string  `json:"pet,omitempty"`
	// Spells are the key spells the bot's own list showed.
	Spells []string `json:"spells,omitempty"`
	// Fixed is what a `.bot` command fixed ("autogear blue: epic ...", "maintenance").
	Fixed    []string `json:"fixed,omitempty"`
	Warnings []string `json:"warnings,omitempty"`
	Problems []string `json:"problems,omitempty"`
	Ready    bool     `json:"ready"`
}

// ReadyReport is the report's `ready` block of a hired party.
type ReadyReport struct {
	Party     string     `json:"party"`
	Level     int        `json:"level"`
	LevelNote string     `json:"level_note,omitempty"`
	Hired     []string   `json:"hired,omitempty"`
	Reused    []string   `json:"reused,omitempty"`
	Dismissed []string   `json:"dismissed,omitempty"`
	Bots      []ReadyBot `json:"bots"`
	// Seconds is what the hire step took; SetupS the whole setup of the run (prepare, bind, enter phase and preflight), set when it ends.
	Seconds  float64  `json:"seconds"`
	SetupS   float64  `json:"setup_s"`
	Ready    bool     `json:"ready"`
	Warnings []string `json:"warnings,omitempty"`
}

func (r *ReadyReport) clone() *ReadyReport {
	if r == nil {
		return nil
	}
	c := *r
	c.Hired, c.Reused, c.Dismissed = slices.Clone(r.Hired), slices.Clone(r.Reused), slices.Clone(r.Dismissed)
	c.Warnings = slices.Clone(r.Warnings)
	c.Bots = slices.Clone(r.Bots)
	for i := range c.Bots {
		b := &c.Bots[i]
		b.Spells, b.Fixed, b.Warnings, b.Problems = slices.Clone(b.Spells), slices.Clone(b.Fixed), slices.Clone(b.Warnings), slices.Clone(b.Problems)
		if b.Shield != nil {
			v := *b.Shield
			b.Shield = &v
		}
	}
	return &c
}

// HiredParty is the party the step left bound: the run takes its lead bot, tank and healer from it.
type HiredParty struct {
	Lead   string
	Bots   []string // the others, lead left out
	Tank   string
	Healer string
}

// ---- the hire command's answers ----

// hireReply is what one reply line of `.bot hire` said.
type hireReply struct {
	Name    string
	Level   int // "Level 43." when the line says it
	Limit   int // the hirer's limit when the level was lowered to it
	Refusal string
	Code    string // raid-full, level-arg, cap, other
}

var (
	hiredRe    = regexp.MustCompile(`(?i)\bhired\s+([A-Za-z]{2,12})\s+for\b`)
	hireLevRe  = regexp.MustCompile(`\bLevel (\d{1,2})\.`)
	hireLimRe  = regexp.MustCompile(`above your limit \((\d{1,2})\)`)
	refusalSub = []struct{ sub, code string }{
		{"convert to a raid", "raid-full"},
		{"bad level", "level-arg"}, {"unclear number", "level-arg"}, {"give only one level", "level-arg"},
		{"maximum number of companions", "cap"}, {"raid already holds the maximum", "cap"},
		{"hiring is disabled", "other"}, {"may not hire", "other"}, {"hired at a <mercenary hire> recruiter", "other"}, {"at a <mercenary hire> recruiter", "other"},
		{"choose a class", "other"}, {"cannot be that class", "other"}, {"fights for the other faction", "other"}, {"cannot fill that role", "other"},
		{"cannot afford", "other"}, {"no mercenary of that kind", "other"}, {"already mustering", "other"}, {"could not record", "other"},
		{"not unlocked yet", "other"}, {"must be in-game", "other"}, {"no race of your faction", "other"}, {"usage: .bot hire", "level-arg"},
	}
)

// parseHireReply reads one line of the answer to `.bot hire`; done says the line ends the reply.
func parseHireReply(line string) (r hireReply, done bool) {
	if m := hiredRe.FindStringSubmatch(line); m != nil {
		r.Name = m[1]
		if l := hireLevRe.FindStringSubmatch(line); l != nil {
			r.Level, _ = strconv.Atoi(l[1])
		}
		if l := hireLimRe.FindStringSubmatch(line); l != nil {
			r.Limit, _ = strconv.Atoi(l[1])
		}
		return r, true
	}
	low := strings.ToLower(line)
	for _, s := range refusalSub {
		if strings.Contains(low, s.sub) {
			r.Refusal, r.Code = strings.TrimSpace(line), s.code
			return r, true
		}
	}
	return r, false
}

// autogearVerdict reads a reply line of `.bot autogear <bot> blue`.
func autogearVerdict(line string) (verdict string, done bool) {
	low := strings.ToLower(strings.TrimSpace(line))
	switch {
	case strings.HasPrefix(low, "re-geared "):
		return "regeared", true
	case strings.Contains(low, "autogear is disabled"):
		return "disabled", true
	case strings.Contains(low, "pool bot"):
		return "refused: pool bot", true
	case strings.Contains(low, "must be alive"):
		return "refused: the bot is dead", true
	case strings.HasPrefix(low, "usage:"), strings.Contains(low, "unknown autogear option"):
		return "refused: " + strings.TrimSpace(line), true
	}
	return "", false
}

// ---- the flow ----

type hireBot struct {
	slot   hireSlot
	name   string
	reused bool
	rb     ReadyBot
}

type hireFlow struct {
	e       *prepEnv
	hg      HireGame
	spec    HireSpec
	slots   []hireSlot
	level   int
	began   time.Time
	rdy     ReadyReport
	noLevel bool // the server refused level=<n>, or ignored it: the hires come at the commander's level
	lowered bool // the server lowered a level to the commander's limit
	bots    []*hireBot
	dropped []string
	problem []string
	who     map[string]tbm.Who // the bots' own `who` answers
}

func (f *hireFlow) over() error {
	if f.e.now().Sub(f.began) > hireStepCeiling {
		return fmt.Errorf("the hire step ran over its %.0f s ceiling", hireStepCeiling.Seconds())
	}
	return nil
}

func (f *hireFlow) note(format string, a ...any) {
	f.e.note("prepare hire: " + fmt.Sprintf(format, a...))
}

// hire is the prepare step `party: hire`.
func (e *prepEnv) hire(ctx context.Context) (string, error) {
	hg, ok := e.g.(HireGame)
	if !ok {
		return "", errors.New("this game cannot hire: it has no group view or item queries")
	}
	if e.command == nil {
		return "", errors.New("this game cannot send commander commands (the bind of the hires)")
	}
	spec := HireSpec{}
	if e.spec.Hire != nil {
		spec = *e.spec.Hire
	}
	if len(spec.Composition) == 0 {
		spec.Composition = e.plan.HireComposition // the plan's own party, if it names one
	}
	slots, err := spec.slots()
	if err != nil {
		return "", err
	}
	f := &hireFlow{e: e, hg: hg, spec: spec, slots: slots, began: e.now()}
	if f.level, err = f.pickLevel(); err != nil {
		return "", err
	}
	f.rdy = ReadyReport{Party: PartyHire, Level: f.level}
	defer func() { // the partial block stays in the report when the step fails
		f.rdy.Seconds = e.now().Sub(f.began).Seconds()
		e.rep.Ready = f.rdy.clone()
	}()
	return f.run(ctx)
}

// pickLevel is the hires' level: the spec's, else the middle of the plan's level band.
func (f *hireFlow) pickLevel() (int, error) {
	if f.spec.Level > 0 {
		return f.spec.Level, nil
	}
	lo, hi := f.e.plan.Band()
	if lo == 0 || hi == 0 {
		return 0, fmt.Errorf("plan %s has no level_band: give prepare.hire.level", f.e.plan.Name)
	}
	return (lo + hi) / 2, nil
}

func (f *hireFlow) run(ctx context.Context) (string, error) {
	e := f.e
	if err := e.readRoster(ctx); err != nil {
		return "", fmt.Errorf("%w (the roster tells the commander's own bots from its hires)", err)
	}
	held, err := f.currentHires(ctx)
	if err != nil {
		return "", err
	}
	bound, err := readBoundParty(ctx, e.command)
	if err != nil {
		return "", err
	}
	// 1. Keep what matches. A hire is dismissed (its character is deleted) only when the bot's own answer shows it is not what the slot
	// wants, or when the caller said rehire; one that does not answer, or that has no slot, stops the step.
	var keep map[int]*heldHire
	var drop []*heldHire
	if f.spec.Rehire {
		drop = held
	} else {
		var extras []*heldHire
		if keep, drop, extras = f.match(held); len(extras) > 0 {
			var why []string
			for _, h := range extras {
				why = append(why, h.name+" ("+h.reason+")")
			}
			return "", fmt.Errorf("the commander's hires %s are not dismissed: %s. Nothing is deleted without a read mismatch; rehire dismisses every current hire on purpose (hires outside the group and the binding still count against the hire cap)", strings.Join(slices.Sorted(slices.Values(namesOf(extras))), ", "), strings.Join(why, "; "))
		}
	}
	standing := f.bindingStands(bound, keep, drop)
	if bound.Answered && !standing {
		if err := e.unbindCommander(ctx); err != nil {
			return "", err
		}
		f.note("ended the old binding of %s's group", bound.Lead)
	}
	if err := f.dismiss(ctx, drop); err != nil {
		return "", err
	}
	// 2. Hire the missing ones.
	f.bots = make([]*hireBot, len(f.slots))
	for i := range f.slots { // in slot order: the report reads the same on every run
		if h, ok := keep[i]; ok {
			f.bots[i] = &hireBot{slot: f.slots[i], name: h.name, reused: true}
			f.rdy.Reused = append(f.rdy.Reused, h.name)
		}
	}
	if err := f.hireMissing(ctx); err != nil {
		return "", err
	}
	// 3. Wait for them, then check them all.
	if err := f.arrive(ctx); err != nil {
		return "", err
	}
	for _, b := range f.bots {
		if err := f.over(); err != nil {
			return "", err
		}
		f.checkBot(ctx, b)
		f.rdy.Bots = append(f.rdy.Bots, b.rb)
		f.rdy.Bots[len(f.rdy.Bots)-1].Ready = len(b.rb.Problems) == 0
		if len(b.rb.Problems) > 0 {
			f.problem = append(f.problem, b.name+": "+strings.Join(b.rb.Problems, "; "))
		}
		if e.progress != nil {
			e.progress("prepare hire: " + b.name)
		}
	}
	if len(f.problem) > 0 {
		return "", fmt.Errorf("%d of %d hires are not ready: %s", len(f.problem), len(f.bots), strings.Join(f.problem, " | "))
	}
	// 4. Bind and check.
	names := make([]string, len(f.bots))
	for i, b := range f.bots {
		names[i] = b.name
	}
	tank, healer := "", ""
	for _, b := range f.bots {
		switch b.slot.Role {
		case "tank":
			tank = b.name
		case "healer":
			healer = b.name
		}
	}
	e.bots = append([]string{tank}, slices.DeleteFunc(slices.Clone(names), func(n string) bool { return n == tank })...)
	if standing && bound.Answered {
		f.note("the binding of %s's group stands and holds the wanted hires: kept", bound.Lead)
		e.bound = true
	} else if err := e.bindBots(ctx, "of the hires"); err != nil {
		return "", err
	}
	if err := f.commanderOutside(ctx); err != nil {
		return "", err
	}
	e.hired = &HiredParty{Lead: e.bots[0], Bots: slices.Clone(e.bots[1:]), Tank: tank, Healer: healer}
	f.rdy.Ready = true
	return fmt.Sprintf("%d hires at level %d bound (%d hired, %d kept, %d dismissed), commander outside the group", len(f.bots), f.level, len(f.rdy.Hired), len(f.rdy.Reused), len(f.rdy.Dismissed)), nil
}

// ---- the commander's current hires ----

type heldHire struct {
	name     string
	class    string
	level    int
	trees    [3]int
	answered bool
	reason   string // why a hire without a slot stays: filled in by match
}

// currentHires are the commander's hires now: the members of its group and of the bound group that the roster does not list (a hire is a
// pool character, not one of the account's). An owned bot in the commander's own group is in the way of a hired party and fails the step;
// an owned bot in a bound group is not (the old binding of an alt party is ended and the bots are left as they are).
func (f *hireFlow) currentHires(ctx context.Context) ([]*heldHire, error) {
	e := f.e
	names := map[string]string{}
	inGroup := map[string]bool{}
	add := func(n string, own bool) {
		if n != "" && !strings.EqualFold(n, e.g.AgentName()) {
			names[strings.ToLower(n)] = n
			inGroup[strings.ToLower(n)] = inGroup[strings.ToLower(n)] || own
		}
	}
	for _, m := range f.hg.Members() {
		add(m.Name, true)
	}
	bp, err := readBoundParty(ctx, e.command)
	if err != nil {
		return nil, err
	}
	for _, n := range bp.Names {
		add(n, false)
	}
	var owned []string
	var out []*heldHire
	for _, n := range sortedValues(names) {
		if _, ok := e.rowOf(n); ok {
			if inGroup[strings.ToLower(n)] {
				owned = append(owned, n)
			}
			continue
		}
		out = append(out, &heldHire{name: n})
	}
	if len(owned) > 0 {
		return nil, fmt.Errorf("the commander's group holds %s, an owned bot of the account (not a hire): it takes a place of the hired party and would be bound with it; take it out of the group first", strings.Join(owned, ", "))
	}
	if f.spec.Rehire {
		return out, nil // dismissed on purpose, whatever they are
	}
	// What each hire is comes from the bot's own `who` answer, not from the agent's view: a bound hire inside an instance is not in it.
	var silent []string
	inBound := map[string]bool{}
	for _, n := range bp.Names {
		inBound[strings.ToLower(n)] = true
	}
	for _, h := range out {
		// A hire of a kept binding does not answer `who`: what the run that left it read is used for the bots still in it.
		if w, ok := e.memory[strings.ToLower(h.name)]; ok && inBound[strings.ToLower(h.name)] {
			if f.who == nil {
				f.who = map[string]tbm.Who{}
			}
			f.who[strings.ToLower(h.name)] = w
			f.note("%s: a bound hire of a kept binding does not answer who; taken as level %d %s from the run that left it", h.name, w.Level, w.Class)
		}
		w, why := f.readWho(ctx, h.name)
		if why != "" {
			silent = append(silent, fmt.Sprintf("%s (%s)", h.name, why))
			continue
		}
		h.class, h.level, h.trees, h.answered = w.Class, w.Level, w.Trees, true
	}
	if len(silent) > 0 {
		return nil, fmt.Errorf("no answer from the commander's hire %s to `who` (offline, teleporting or in its logout grace?): it is not dismissed, and the step cannot place it. Try again when it is back, or end it on purpose with rehire", strings.Join(silent, ", "))
	}
	return out, nil
}

// readWho asks the bot who it is (`command <bot> who`): class, level and the points in each tree. why says what went wrong when it could
// not be read. The answer is kept for the step.
func (f *hireFlow) readWho(ctx context.Context, name string) (tbm.Who, string) {
	if w, ok := f.who[strings.ToLower(name)]; ok {
		return w, ""
	}
	var got tbm.Who
	_, settled, err := f.e.g.BotLines(ctx, "command "+name+" who", hireBotLineWait, func(line string) bool {
		w, ok := tbm.ParseWho([]string{line})
		if ok {
			got = w
		}
		return ok
	})
	switch {
	case err != nil:
		return got, err.Error()
	case !settled:
		return got, "no answer in " + hireBotLineWait.String()
	}
	if f.who == nil {
		f.who = map[string]tbm.Who{}
	}
	f.who[strings.ToLower(name)] = got
	return got, ""
}

func namesOf(hs []*heldHire) []string {
	var out []string
	for _, h := range hs {
		out = append(out, h.name)
	}
	return out
}

func sortedValues(m map[string]string) []string {
	var out []string
	for _, v := range m {
		out = append(out, v)
	}
	slices.Sort(out)
	return out
}

// match gives each slot a hire of the same class and level whose build gives the role. A leftover hire is dismissed only when it is
// read to be the wrong version of a slot that is still empty (the same class, another level or a build for another role); any other
// leftover (no empty slot of its class, a build that cannot be told) is returned as an extra and stops the step: nothing is deleted
// for being surplus or unclear.
func (f *hireFlow) match(held []*heldHire) (keep map[int]*heldHire, drop, extras []*heldHire) {
	keep = map[int]*heldHire{}
	used := map[*heldHire]bool{}
	for i, s := range f.slots {
		for _, h := range held {
			if !used[h] && h.class == s.Class && h.level == f.level && talentRole(h.class, h.trees) == s.Role {
				keep[i], used[h] = h, true
				break
			}
		}
	}
	claimed := map[int]bool{}
	for _, h := range held {
		if used[h] {
			continue
		}
		role := talentRole(h.class, h.trees)
		replaced := false
		for i, s := range f.slots {
			if keep[i] != nil || claimed[i] || s.Class != h.class {
				continue
			}
			switch {
			case h.level != f.level:
				h.reason = fmt.Sprintf("level %d, the party is level %d", h.level, f.level)
			case role != "" && role != "feral" && role != s.Role:
				h.reason = fmt.Sprintf("a %s build, the slot is %s", role, s.Role)
			default:
				continue
			}
			claimed[i], replaced = true, true
			f.note("%s is a level %d %s: %s; replaced", h.name, h.level, h.class, h.reason)
			break
		}
		switch {
		case replaced:
			drop = append(drop, h)
		case h.reason == "":
			h.reason = fmt.Sprintf("level %d %s with no empty slot of its kind or a build that cannot be told", h.level, h.class)
			extras = append(extras, h)
		default:
			extras = append(extras, h)
		}
	}
	return keep, drop, extras
}

// talentRole is the role the dominant tree gives (1.12 tree order): "", tank, healer, dps or feral (bear or cat).
func talentRole(class string, trees [3]int) string {
	best, at := -1, 0
	for i, p := range trees {
		if p > best {
			best, at = p, i
		}
	}
	if best <= 0 {
		return ""
	}
	switch class {
	case "warrior":
		if at == 2 {
			return "tank"
		}
		return "dps"
	case "paladin":
		return []string{"healer", "tank", "dps"}[at]
	case "priest":
		return []string{"healer", "healer", "dps"}[at]
	case "shaman":
		return []string{"dps", "dps", "healer"}[at]
	case "druid":
		return []string{"dps", "feral", "healer"}[at]
	case "hunter", "rogue", "mage", "warlock":
		return "dps"
	}
	return ""
}

// bindingStands: the binding of the account is the wanted party already (nothing to dismiss, every slot kept, the group is exactly the
// kept hires, led by the tank), so neither an unbind nor a bind is needed.
func (f *hireFlow) bindingStands(bound boundParty, keep map[int]*heldHire, drop []*heldHire) bool {
	if !bound.Answered || len(drop) > 0 || len(keep) != len(f.slots) || len(bound.Names) != len(keep) {
		return false
	}
	tank := ""
	for i, h := range keep {
		if f.slots[i].Role == "tank" {
			tank = h.name
		}
		if !slices.ContainsFunc(bound.Names, func(n string) bool { return strings.EqualFold(n, h.name) }) {
			return false
		}
	}
	return strings.EqualFold(bound.Lead, tank)
}

// dismiss ends the hires that are not wanted: `.bot logout <name>` on a hire ends it and deletes the character.
func (f *hireFlow) dismiss(ctx context.Context, drop []*heldHire) error {
	if len(drop) == 0 {
		return nil
	}
	bl := f.e.g
	for _, h := range drop {
		var verdict string
		_, settled, err := bl.BotLines(ctx, "logout "+h.name, 4*time.Second, func(line string) bool {
			low := strings.ToLower(line)
			switch {
			case strings.Contains(low, "hire ended"), strings.Contains(low, "logout requested"):
				verdict = "ok"
			case strings.Contains(low, "could not be logged out"), strings.Contains(low, "not found"), strings.HasPrefix(low, "usage:"), strings.Contains(low, "may only control"):
				verdict = "refused: " + line
			default:
				return false
			}
			return true
		})
		switch {
		case err != nil:
			return fmt.Errorf("dismissing %s: %w", h.name, err)
		case !settled:
			f.e.rep.Warnings = append(f.e.rep.Warnings, "logout "+h.name+": no answer")
		case verdict != "ok":
			return fmt.Errorf("dismissing %s: %s", h.name, verdict)
		}
		f.rdy.Dismissed = append(f.rdy.Dismissed, h.name)
		f.note("dismissed %s (%s %d)", h.name, orDash(h.class), h.level)
	}
	// They leave the group; a new hire needs the places.
	deadline := f.e.now().Add(hireDismissWait)
	for {
		gone := true
		for _, m := range f.hg.Members() {
			if slices.ContainsFunc(drop, func(h *heldHire) bool { return strings.EqualFold(h.name, m.Name) }) {
				gone = false
			}
		}
		if gone {
			return nil
		}
		if !f.e.now().Before(deadline) {
			f.rdy.Warnings = append(f.rdy.Warnings, fmt.Sprintf("a dismissed hire still shows in the group %.0f s later", hireDismissWait.Seconds()))
			return nil
		}
		if err := f.e.sleep(ctx, time.Second); err != nil {
			return err
		}
	}
}

func orDash(s string) string {
	if s == "" {
		return "?"
	}
	return s
}

// ---- hiring ----

func (f *hireFlow) hireMissing(ctx context.Context) error {
	for i, b := range f.bots {
		if b != nil {
			continue
		}
		if err := f.over(); err != nil {
			return err
		}
		name, err := f.hireOne(ctx, f.slots[i])
		if err != nil {
			return err
		}
		f.bots[i] = &hireBot{slot: f.slots[i], name: name}
		f.rdy.Hired = append(f.rdy.Hired, name)
		f.note("hired %s (%s, level %d)", name, f.slots[i], f.level)
	}
	return nil
}

// hireLine builds the `.bot hire` line. The level goes only when it differs from the commander's own: the hire is at the commander's level
// without it, on every build.
func (f *hireFlow) hireLine(s hireSlot) string {
	line := fmt.Sprintf("hire %s %s", s.Class, s.Role)
	if !f.noLevel && f.level != f.hg.AgentLevel() {
		line += fmt.Sprintf(" level=%d", f.level)
	}
	return line
}

// hireOne sends one hire and returns the new bot's name. A reply that asks for a raid is answered with the conversion and one more try;
// a reply that refuses level=<n> with one more try without it.
func (f *hireFlow) hireOne(ctx context.Context, s hireSlot) (string, error) {
	converted := false
	for try := 0; try < 3; try++ {
		line := f.hireLine(s)
		var got hireReply
		_, settled, err := f.e.g.BotLines(ctx, line, hireReplyWait, func(l string) bool {
			r, done := parseHireReply(l)
			if done {
				got = r
			}
			return done
		})
		switch {
		case err != nil:
			return "", fmt.Errorf(".bot %s: %w", line, err)
		case !settled:
			return "", fmt.Errorf(".bot %s: no answer in %.0f s (does the server run a TortoiseBots build with hiring on?)", line, hireReplyWait.Seconds())
		case got.Name != "":
			f.readLevelAnswer(line, got)
			return got.Name, nil
		case got.Code == "raid-full" && !converted:
			converted = true
			if err := f.hg.ConvertRaid(ctx); err != nil {
				return "", fmt.Errorf("the 5th hire needs a raid and the conversion failed: %w", err)
			}
			f.note("converted the commander's party to a raid for the %s", s)
			continue
		case got.Code == "level-arg" && !f.noLevel && strings.Contains(line, "level="):
			f.noLevel = true
			f.rdy.LevelNote = "the server refused level=<n> (" + got.Refusal + "): hired at the commander's level"
			f.note("%s", f.rdy.LevelNote)
			continue
		}
		hint := ""
		if got.Code == "cap" {
			hint = " (hires left outside the group and the binding still count against the cap; rehire dismisses the commander's hires on purpose)"
		}
		return "", fmt.Errorf(".bot %s refused: %s%s", line, got.Refusal, hint)
	}
	return "", fmt.Errorf(".bot hire %s %s: still refused after the retries", s.Class, s.Role)
}

// readLevelAnswer notes what the reply said of the level: a reply that holds no "Level n." to a line that asked for one comes from a
// server that ignores level=<n> (it hired at the commander's level).
func (f *hireFlow) readLevelAnswer(line string, r hireReply) {
	if !strings.Contains(line, "level=") {
		return
	}
	switch {
	case r.Level == 0:
		if !f.noLevel {
			f.noLevel = true
			f.rdy.LevelNote = "the server's answer carries no level: it ignores level=<n>, the hires come at the commander's level"
			f.note("%s", f.rdy.LevelNote)
		}
	case r.Limit > 0:
		f.lowered = true
		f.rdy.LevelNote = fmt.Sprintf("the server lowered the level %d to the commander's limit %d", f.level, r.Limit)
		f.note("%s", f.rdy.LevelNote)
	}
}

// arrive waits, within the wait, for each new hire to show in the commander's group with its level and class, and reads them.
func (f *hireFlow) arrive(ctx context.Context) error {
	wait := DefaultHireWait
	if f.spec.WaitS > 0 {
		wait = time.Duration(f.spec.WaitS) * time.Second
	}
	deadline := f.e.now().Add(wait)
	for {
		var late []string
		for _, b := range f.bots {
			if b.reused {
				continue
			}
			if !f.shows(b) {
				late = append(late, b.name)
			}
		}
		if len(late) == 0 {
			return nil
		}
		if err := f.over(); err != nil {
			return err
		}
		if !f.e.now().Before(deadline) {
			return fmt.Errorf("%s not in the commander's group with a level within %.0f s of the hire (wait_s %d); are the hires blocked by a full party, or the server slow?", strings.Join(late, ", "), wait.Seconds(), int(wait.Seconds()))
		}
		if err := f.e.sleep(ctx, time.Second); err != nil {
			return err
		}
	}
}

// shows says whether the hire is in the commander's group, online, alive-or-dead, with a level and health.
func (f *hireFlow) shows(b *hireBot) bool {
	for _, m := range f.hg.Members() {
		if strings.EqualFold(m.Name, b.name) && m.Online && m.Level > 0 && m.MaxHP > 0 {
			return true
		}
	}
	return false
}

// member is the bot's player as the game knows it now.
func (f *hireFlow) member(b *hireBot) (HireMember, bool) {
	if m, ok := f.hg.Player(b.name); ok {
		return m, true
	}
	return HireMember{}, false
}

// levelProblem judges the hire's level against the wanted one. A level other than wanted is accepted only when the server could not give
// it (it refused, ignored or lowered level=<n>) and the commander's level lies inside the plan's band.
func (f *hireFlow) levelProblem(level int) string {
	if level == f.level {
		return ""
	}
	if !(f.noLevel || f.lowered) {
		return fmt.Sprintf("level %d, hired for %d", level, f.level)
	}
	lo, hi := f.e.plan.Band()
	if lo != 0 && (level < lo || level > hi) {
		return fmt.Sprintf("level %d is outside the plan's band %d-%d (the server did not give level %d: %s)", level, lo, hi, f.level, f.rdy.LevelNote)
	}
	return ""
}

// commanderOutside waits for the commander to be out of every group after the bind (TB #935), the hires are all it commands.
func (f *hireFlow) commanderOutside(ctx context.Context) error {
	deadline := f.e.now().Add(hireOutOfGroup)
	for f.hg.InGroup() {
		if !f.e.now().Before(deadline) {
			return fmt.Errorf("the commander is still in a group %.0f s after the bind: this server's commander bind keeps it in the hires' party (TortoiseBots before #935 binds only bots outside the commander's group)", hireOutOfGroup.Seconds())
		}
		if err := f.e.sleep(ctx, time.Second); err != nil {
			return err
		}
	}
	return nil
}
