package dungeon

import (
	"context"
	"fmt"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/pjerra/turtle-agent/internal/packet"
	"github.com/pjerra/turtle-agent/internal/tbm"
)

// The hire step (hire.go, hire_check.go) against a fake commander that behaves like the server for what the step relies on: `.bot hire`
// (party limit and the raid conversion, the level argument of TB #940 or a build that refuses or ignores it), hires that arrive in the
// commander's group after a delay, `.bot logout` ending a hire, `.bot maintenance`, `.bot autogear`, `.bot inv`, the bot's whispered
// talents and spell list, pets, and `commander bind|status|unbind` with the commander outside the bound group (#935).

const (
	hDrink   = 8766  // Morning Glory Dew
	hBullet  = 11284 // Accurate Slug
	hQuality = 3
)

type fhire struct {
	name, class, role string
	level             int
	hiredAt           time.Time
	bound             bool
	dead              bool
	equip             map[int]uint32
	bag               map[uint32]int
	trees             [3]int
	spells            map[string]bool // lower-case names
	pet               bool
	inView            bool
	hidden            bool // out of the agent's view and group list (a bound hire inside an instance)
}

type hireFake struct {
	*enterFake
	hires     []*fhire
	names     []string
	nextName  int
	agentLvl  int
	maxAbove  int
	levelMode string // "": TB #940; "refuse": "Bad level"; "ignore": the argument is not read
	raid      bool
	maxHires  int
	arriveIn  time.Duration
	alts      []string // owned bots (the roster lists them)
	altGroup  bool     // an owned bot stands in the commander's group
	binding   []string // the bound names, lead first; nil: not bound
	// build switches
	keepInGroup bool // a bind that leaves the commander in the group (before #935)
	refuseHired bool // a bind that refuses the commander's own hires
	maintOff    bool
	autogearOff bool // the answer says re-geared, nothing changes
	noSupplies  bool // hires come with empty bags (maintenance fills them)
	epic        map[string]bool
	noShield    map[string]bool
	lack        map[string][]string // class -> spells the bots of the class do not know
	silentList  bool                // the bot never answers `spells <filter>`
	unreachable map[string]bool     // bots that answer no whisper (teleporting, offline in the grace)
	noTalents   map[string]bool     // bots that answer `who` and `spells` but not `talents`
	whoSilent   map[string]bool     // bots that answer everything but `who` (the hires of a kept binding)
	deaf        map[string]bool     // bots that answer no `.bot` line at all (bound hires)
	noPet       bool
	petLate     int // the pets show only on the n-th look (about n seconds after the check began)
	petLooks    map[string]int
	botCmds     []string // every `.bot` line sent to a bot
	itemSilent  bool
	hireReply   string
	items       map[uint32]ItemInfo
	events      []string
	hireLines   []string
	converted   int
	// groupAtBind says whether the commander was still in a group when the first bind went through.
	groupAtBind *bool
}

func newHireFake(t *testing.T) *hireFake {
	e := newEnterFake(t)
	e.plan.LevelBand = []int{41, 45}
	e.agentMap = outsideMap
	g := &hireFake{enterFake: e, agentLvl: 45, maxHires: 5, arriveIn: 3 * time.Second, noSupplies: true,
		names: []string{"Zamoraela", "Khaeworval", "Corkhas", "Gredel", "Zisorlo", "Alrunmel", "Thundrik", "Sevaria", "Brannoch", "Ilyandra", "Morgrim", "Tessaly", "Varkull"},
		epic:  map[string]bool{}, unreachable: map[string]bool{}, noTalents: map[string]bool{}, noShield: map[string]bool{}, lack: map[string][]string{}, items: map[uint32]ItemInfo{}}
	for s := 0; s < 19; s++ {
		g.items[uint32(90000+s)] = ItemInfo{Found: true, Name: fmt.Sprintf("Blue %d", s), Quality: hQuality, ItemLevel: 40 + s%5}
		g.items[uint32(91000+s)] = ItemInfo{Found: true, Name: fmt.Sprintf("Epic %d", s), Quality: 4, ItemLevel: 60}
	}
	g.items[shieldEntries[0]] = ItemInfo{Found: true, Name: "Troll Protector", Class: 4, SubClass: 6, Quality: hQuality, ItemLevel: 43}
	g.items[bulletWeaponEntries[0]] = ItemInfo{Found: true, Name: "Ricochet Blunderbuss", Class: 2, SubClass: 3, Quality: hQuality, ItemLevel: 44}
	return g
}

func (g *hireFake) log(format string, a ...any) {
	g.events = append(g.events, fmt.Sprintf(format, a...))
}

func (g *hireFake) count(prefix string) int {
	n := 0
	for _, l := range g.events {
		if strings.HasPrefix(l, prefix) {
			n++
		}
	}
	return n
}

func (g *hireFake) idxOf(prefix string) int {
	for i, l := range g.events {
		if strings.HasPrefix(l, prefix) {
			return i
		}
	}
	return -1
}

func (g *hireFake) byName(name string) *fhire {
	for _, h := range g.hires {
		if strings.EqualFold(h.name, name) {
			return h
		}
	}
	return nil
}

func (g *hireFake) arrived(h *fhire) bool { return !g.now.Before(h.hiredAt.Add(g.arriveIn)) }

// inGroup: the hire is in the commander's own group (arrived and not in a bound group).
func (g *hireFake) inGroup(h *fhire) bool { return g.arrived(h) && !h.bound }

var hireSpells = map[string][]string{
	"warrior": {"Battle Stance", "Defensive Stance", "Berserker Stance", "Taunt", "Sunder Armor", "Shield Block", "Charge", "Heroic Strike"},
	"priest":  {"Resurrection", "Renew", "Dispel Magic", "Heal", "Greater Heal", "Power Word: Shield"},
	"hunter":  {"Tame Beast", "Call Pet", "Mend Pet", "Revive Pet", "Multi-Shot", "Hunter's Mark"},
	"rogue":   {"Kick", "Gouge", "Sinister Strike", "Evasion"},
	"warlock": {"Summon Imp", "Summon Voidwalker", "Summon Succubus", "Shadow Bolt"},
	"mage":    {"Polymorph", "Frost Nova", "Frostbolt"},
	"paladin": {"Redemption", "Holy Light"},
	"shaman":  {"Ancestral Spirit"},
	"druid":   {"Bear Form"},
}

var hireTrees = map[string][3]int{
	"warrior:tank": {0, 0, 36}, "warrior:dps": {36, 0, 0}, "priest:healer": {36, 0, 0}, "priest:dps": {0, 0, 36},
	"hunter:dps": {0, 0, 36}, "rogue:dps": {0, 0, 36}, "warlock:dps": {36, 0, 0}, "mage:dps": {0, 36, 0},
	"paladin:tank": {0, 36, 0}, "paladin:healer": {36, 0, 0}, "paladin:dps": {0, 0, 36}, "shaman:healer": {0, 0, 36}, "shaman:dps": {36, 0, 0},
	"druid:healer": {0, 0, 36}, "druid:dps": {36, 0, 0}, "druid:tank": {0, 36, 0},
}

// newHire makes a hire as the server provisions one: blue gear in the usual slots, a shield for a tank warrior, a gun for a hunter, its
// class's spells and a premade build; bags empty unless the fake says otherwise.
func (g *hireFake) newHire(class, role string, level int) *fhire {
	h := &fhire{name: g.names[g.nextName], class: class, role: role, level: level, hiredAt: g.now, equip: map[int]uint32{}, bag: map[uint32]int{}, spells: map[string]bool{}, inView: true,
		trees: hireTrees[class+":"+role], pet: !g.noPet && (class == "hunter" || class == "warlock")}
	g.nextName++
	for _, r := range requiredSlots {
		h.equip[r.slot] = uint32(90000 + r.slot)
	}
	if class == "warrior" && role == "tank" && !g.noShield[class] {
		h.equip[tbm.SlotOffHand] = shieldEntries[0]
	}
	if class == "hunter" {
		h.equip[tbm.SlotRanged] = bulletWeaponEntries[0]
	}
	if g.epic[class] {
		h.equip[slotChest] = uint32(91000 + slotChest)
	}
	for _, s := range hireSpells[class] {
		if !slices.Contains(g.lack[class], s) {
			h.spells[strings.ToLower(s)] = true
		}
	}
	if !g.noSupplies {
		g.restock(h)
	}
	return h
}

func (g *hireFake) restock(h *fhire) {
	if usesManaClass(h.class) {
		h.bag[hDrink] = 20
	}
	if h.equip[tbm.SlotRanged] != 0 {
		h.bag[hBullet] = 200
	}
}

func (g *hireFake) BotLines(_ context.Context, command string, _ time.Duration, done func(string) bool) ([]string, bool, error) {
	g.botCmds = append(g.botCmds, command)
	if g.deafTo(command) {
		return nil, false, nil
	}
	reply, handled := g.reply(command)
	if !handled {
		return nil, false, nil
	}
	var got []string
	for _, l := range reply {
		got = append(got, l)
		if done(l) {
			return got, true, nil
		}
	}
	return got, false, nil
}

// BotBurst models tbm.Commander.Burst: the reply's lines from the first one begin accepts; began is false when none does.
func (g *hireFake) BotBurst(_ context.Context, command string, _, _ time.Duration, begin func(string) bool) ([]string, bool, error) {
	if g.deafTo(command) {
		return nil, false, nil
	}
	reply, _ := g.reply(command)
	for i, l := range reply {
		if begin(l) {
			return reply[i:], true, nil
		}
	}
	return nil, false, nil
}

// deafTo says whether the line names a bot that answers no `.bot` line.
func (g *hireFake) deafTo(command string) bool {
	for _, w := range strings.Fields(command) {
		if g.deaf[w] {
			return true
		}
	}
	return false
}

// reply is what the server and the bots say to a `.bot` line, in order; handled is false for a line the fake does not know.
func (g *hireFake) reply(command string) (reply []string, handled bool) {
	f := strings.Fields(command)
	switch {
	case command == "roster":
		g.log("roster")
		reply = append(reply, fmt.Sprintf("TBM:ROSTER_BEGIN|%d", len(g.alts)))
		for i, n := range g.alts {
			reply = append(reply, fmt.Sprintf("TBM:ROSTER|%d|%s|1|online|0|Uldaman", 500+i, n))
		}
		reply = append(reply, "TBM:ROSTER_END")
	case f[0] == "hire":
		g.log("%s", command)
		g.hireLines = append(g.hireLines, command)
		reply = []string{g.hire(f)}
	case f[0] == "logout" && len(f) == 2:
		g.log("logout %s", f[1])
		if h := g.byName(f[1]); h != nil {
			g.dismiss(h)
			reply = []string{fmt.Sprintf("Hire ended for %s; its character is being deleted.", h.name)}
		} else {
			reply = []string{fmt.Sprintf("Character '%s' not found.", f[1])}
		}
	case f[0] == "maintenance" && len(f) == 2:
		g.log("maintenance %s", f[1])
		h := g.byName(f[1])
		switch {
		case g.maintOff:
			reply = []string{"Owned-bot maintenance is disabled (AiPlayerbot.OwnedBotMaintenanceEnabled = 0)."}
		case h == nil:
			reply = []string{"Usage: .bot maintenance <online bot name> (same account only)"}
		default:
			g.restock(h)
			reply = []string{fmt.Sprintf("Maintained %s (consumables, reagents, ammo, repair).", h.name)}
		}
	case f[0] == "autogear" && len(f) == 3 && f[2] == "blue":
		g.log("autogear %s", f[1])
		h := g.byName(f[1])
		if h == nil {
			reply = []string{"Usage: .bot autogear <online bot name> [quality|ilvl] (same account only)"}
			break
		}
		if !g.autogearOff {
			for _, r := range requiredSlots {
				h.equip[r.slot] = uint32(90000 + r.slot)
			}
			if h.class == "warrior" && h.role == "tank" {
				h.equip[tbm.SlotOffHand] = shieldEntries[0]
			}
			if h.class == "hunter" {
				h.equip[tbm.SlotRanged] = bulletWeaponEntries[0]
			}
		}
		reply = []string{fmt.Sprintf("Re-geared %s within the configured caps.", h.name)}
	case f[0] == "inv" && len(f) == 2:
		g.log("inv %s", f[1])
		h := g.byName(f[1])
		if h == nil {
			reply = []string{"TBM:ACTION_ERR|inv|no-bot|not a bot"}
			break
		}
		reply = append(reply, fmt.Sprintf("TBM:INV_BEGIN|%s|0", h.name))
		for s := 0; s < 19; s++ {
			if e := h.equip[s]; e != 0 {
				reply = append(reply, fmt.Sprintf("TBM:INV_EQ|%d|%d|50|50", s, e))
			}
		}
		slot := 23
		for _, e := range sortedEntries(h.bag) {
			reply = append(reply, fmt.Sprintf("TBM:INV_ITEM|255|%d|%d|%d|-", slot, e, h.bag[e]))
			slot++
		}
		reply = append(reply, fmt.Sprintf("TBM:INV_END|%s|%d|16", h.name, 16-len(h.bag)))
	case f[0] == "command" && len(f) == 3 && f[2] == "talents":
		g.log("talents %s", f[1])
		if h := g.byName(f[1]); h != nil && !g.unreachable[h.name] && !g.noTalents[h.name] {
			reply = []string{fmt.Sprintf("Forwarded command for %s: talents; action success is not guaranteed.", h.name),
				fmt.Sprintf("My current talent spec is: |hspec (%d|h/%d|h/|h%d|h) Link: 0-0-1", h.trees[0], h.trees[1], h.trees[2])}
		}
	case f[0] == "command" && len(f) == 3 && f[2] == "who":
		g.log("who %s", f[1])
		if h := g.byName(f[1]); h != nil && !g.unreachable[h.name] && !g.whoSilent[h.name] {
			// WhoAction::QuerySpec with its colour codes: "<spec> (a/b/c) <class> (<level> lvl), <gear score> GS (<quality counts>)".
			reply = []string{fmt.Sprintf("Forwarded command for %s: who; action success is not guaranteed.", h.name),
				fmt.Sprintf("|h|cffffffffspec (|h|cff00ff00%d|h|cffffffff/%d|h|cffffffff/|h|cff00ff00%d|h|cffffffff)|r %s (|h|cff00ff00%d|h|cffffffff lvl), |h|cff00ff00812|h|cffffffff GS (|h|cff8080ff5|h|cffffffff), playing with Glacienne",
					h.trees[0], h.trees[1], h.trees[2], h.class, h.level)}
		}
	case f[0] == "command" && len(f) >= 4 && f[2] == "spells":
		filter := strings.ToLower(strings.Join(f[3:], " "))
		g.log("spells %s %s", f[1], filter)
		h := g.byName(f[1])
		if h == nil || g.silentList || g.unreachable[h.name] {
			break
		}
		// ListSpellsAction::Execute: the header (also when nothing matches), then ONE spell per line, each a spell link.
		reply = []string{fmt.Sprintf("Forwarded command for %s: spells %s; action success is not guaranteed.", h.name, filter), "=== Spells ==="}
		for i, sp := range hireSpells[h.class] {
			if h.spells[strings.ToLower(sp)] && strings.Contains(strings.ToLower(sp), filter) {
				reply = append(reply, fmt.Sprintf("|cffffffff|Hspell:%d|h[%s]|h|r", 100+i, sp))
			}
		}
	case f[0] == "command" && len(f) == 3 && f[2] == "leave":
		g.log("leave %s", f[1])
		reply = []string{"ok"}
	default:
		g.log("line %s", command)
		return nil, false
	}
	return reply, true
}

func sortedEntries(m map[uint32]int) []uint32 {
	var out []uint32
	for e := range m {
		out = append(out, e)
	}
	slices.Sort(out)
	return out
}

// hire models HandleHire / HireProvisionService::Hire for the parts the step meets.
func (g *hireFake) hire(f []string) string {
	if g.hireReply != "" {
		return g.hireReply
	}
	class, role, level, arg := f[1], f[2], g.agentLvl, 0
	for _, w := range f[3:] {
		fmt.Sscanf(w, "level=%d", &arg)
	}
	if arg > 0 && g.levelMode == "refuse" {
		return fmt.Sprintf("Bad level \"%s\": use level=<1-60> or level <1-60>, for example .bot hire mage level=42.", f[len(f)-1])
	}
	if len(g.hires) >= g.maxHires {
		return "You already command the maximum number of companions."
	}
	inGroup := 0
	for _, h := range g.hires {
		if !h.bound {
			inGroup++
		}
	}
	if !g.raid && inGroup >= 4 {
		return "Your party is full. Convert to a raid to hire more companions."
	}
	suffix := ""
	if arg > 0 && g.levelMode != "ignore" {
		limit := g.agentLvl + g.maxAbove
		level = arg
		suffix = fmt.Sprintf(" Level %d.", level)
		if arg > limit {
			level = limit
			suffix = fmt.Sprintf(" Level %d. The level you asked for is above your limit (%d).", level, limit)
		}
	}
	h := g.newHire(class, role, level)
	g.hires = append(g.hires, h)
	return fmt.Sprintf("Hired %s for 75s. Your companion is mustering.%s", h.name, suffix)
}

func (g *hireFake) dismiss(h *fhire) {
	g.hires = slices.DeleteFunc(g.hires, func(x *fhire) bool { return x == h })
	if g.binding != nil && slices.Contains(g.binding, h.name) {
		g.binding = slices.DeleteFunc(slices.Clone(g.binding), func(n string) bool { return n == h.name })
		if len(g.binding) == 0 {
			g.binding = nil
			g.fakeRemote.bound = false
		}
	}
}

// preBind puts hires in the commander's group already, bound as a finished earlier run left them.
func (g *hireFake) preHire(class, role string, level int) *fhire {
	h := g.newHire(class, role, level)
	h.hiredAt = g.now.Add(-time.Hour)
	g.hires = append(g.hires, h)
	return h
}

func (g *hireFake) preBind() {
	var names []string
	for _, h := range g.hires {
		names = append(names, h.name)
		h.bound = true
	}
	g.binding = names
	g.fakeRemote.bound = true
	g.setParty(names)
}

// setParty makes the feed's party the bound names, lead first.
func (g *hireFake) setParty(names []string) {
	g.party = g.party[:0]
	for i, n := range names {
		g.party = append(g.party, &tbm.PartyRec{GUID: packet.GUID(0x40 + i), Name: n, HP: 1000, MaxHP: 1000, Power: 1000, MaxPower: 1000, PowerType: 0, State: "n"})
	}
	g.memberMap = map[packet.GUID]uint32{}
}

func (g *hireFake) Command(ctx context.Context, command string) (tbm.Reply, error) {
	f := strings.Fields(command)
	switch {
	case strings.HasPrefix(command, "commander bind"):
		g.orders = append(g.orders, command)
		g.log("cbind %s", strings.Join(f[2:], " "))
		switch {
		case g.binding != nil:
			return errReply("commander bind", "already-bound"), nil
		case g.refuseHired:
			return errReply("commander bind", "hired"), nil
		}
		for _, n := range f[2:] {
			if h := g.byName(n); h == nil || !g.arrived(h) {
				return errReply("commander bind", "no-bot"), nil
			}
		}
		g.binding = slices.Clone(f[2:])
		for _, n := range g.binding {
			g.byName(n).bound = true
		}
		g.fakeRemote.bound = true
		g.setParty(g.binding)
		if g.groupAtBind == nil {
			in := g.InGroup()
			g.groupAtBind = &in
		}
		r := ack("commander bind")
		r.Ack.Count, r.Ack.Executor = len(g.binding), g.binding[0]
		return r, nil
	case command == "commander status":
		if g.binding == nil {
			return errReply("commander status", "not-bound"), nil
		}
		r := ack("commander status")
		r.Ack.Count, r.Ack.Executor = len(g.binding), g.binding[0]
		r.Lines = []string{fmt.Sprintf("You command %s's group: %s.", g.binding[0], strings.Join(g.binding, ", "))}
		return r, nil
	case command == "commander unbind":
		g.orders = append(g.orders, command)
		g.unbinds++
		g.log("cunbind")
		if g.binding == nil {
			return errReply("commander unbind", "not-bound"), nil
		}
		for _, n := range g.binding {
			if h := g.byName(n); h != nil {
				h.bound = false
			}
		}
		g.binding = nil
		g.fakeRemote.bound = false
		return ack("commander unbind"), nil
	}
	return g.enterFake.Command(ctx, command)
}

// The HireGame the step sees.

func (g *hireFake) AgentLevel() int { return g.agentLvl }
func (g *hireFake) InGroup() bool {
	if g.keepInGroup && g.binding != nil {
		return true
	}
	if g.altGroup {
		return true
	}
	for _, h := range g.hires {
		if g.inGroup(h) {
			return true
		}
	}
	return false
}

func (g *hireFake) member(h *fhire, inGroup bool) HireMember {
	m := HireMember{Name: h.name, GUID: packet.GUID(0x900 + len(h.name) + int(h.name[0])), Level: h.level, InGroup: inGroup, Online: true, Dead: h.dead, MaxHP: 1500 + uint32(h.level)*10}
	if h.inView {
		m.Class = h.class
	}
	return m
}

func (g *hireFake) Members() []HireMember {
	var out []HireMember
	if g.altGroup && len(g.alts) > 0 {
		out = append(out, HireMember{Name: g.alts[0], GUID: 0x777, Level: 40, InGroup: true, Online: true, MaxHP: 1000})
	}
	for _, h := range g.hires {
		if g.inGroup(h) && !h.hidden {
			out = append(out, g.member(h, true))
		}
	}
	return out
}

func (g *hireFake) Player(name string) (HireMember, bool) {
	for _, m := range g.Members() {
		if strings.EqualFold(m.Name, name) {
			return m, true
		}
	}
	if h := g.byName(name); h != nil && g.arrived(h) && !h.hidden {
		return g.member(h, false), true
	}
	return HireMember{}, false
}

func (g *hireFake) PetOf(guid packet.GUID) (string, bool) {
	for _, h := range g.hires {
		if g.member(h, false).GUID == guid && h.pet {
			if g.petLate > 0 {
				if g.petLooks == nil {
					g.petLooks = map[string]int{}
				}
				if g.petLooks[h.name]++; g.petLooks[h.name] < g.petLate {
					return "", false
				}
			}
			return h.name + "'s pet", true
		}
	}
	return "", false
}

func (g *hireFake) ConvertRaid(context.Context) error {
	g.log("convert_raid")
	g.converted++
	g.raid = true
	return nil
}

func (g *hireFake) ItemInfos(_ context.Context, entries []uint32, _ time.Duration) (map[uint32]ItemInfo, error) {
	out := map[uint32]ItemInfo{}
	if g.itemSilent {
		return out, nil
	}
	for _, e := range entries {
		if it, ok := g.items[e]; ok {
			out[e] = it
		}
	}
	return out, nil
}

func (g *hireFake) AgentName() string      { return prepAgent }
func (g *hireFake) AgentGUID() packet.GUID { return prepAgentID }
func (g *hireFake) Sees(string) bool       { return true }
func (g *hireFake) GMVisible(_ context.Context, on bool) error {
	g.log("visible %v", on)
	return nil
}
func (g *hireFake) UnbindInstances(_ context.Context, who string, _ packet.GUID) error {
	g.t.Errorf("the hire step sent .instance unbind all on %s", who)
	return nil
}
func (g *hireFake) GroupUnbind(_ context.Context, who string, _ packet.GUID) (GroupUnbindResult, error) {
	g.t.Errorf("the hire step sent .instance groupunbind all on %s", who)
	return GroupNoAnswer, nil
}
func (g *hireFake) TeleName(ctx context.Context, player, loc string) error {
	g.log("tele %s %s", player, loc)
	return g.enterFake.TeleName(ctx, player, loc)
}

func (g *hireFake) run(spec PrepareSpec) (Report, error) {
	o := RemoteOptions{Prepare: spec}
	return runRemote(g.t, g, g.plan, o, nil)
}

func hireSpec() PrepareSpec { return PrepareSpec{Party: PartyHire} }

func failed(rep Report, err error, want ...string) bool {
	if err == nil || rep.State != "failed" {
		return false
	}
	for _, w := range want {
		if !strings.Contains(err.Error(), w) {
			return false
		}
	}
	return true
}

// ---- the step ----

func TestHireDefaultPartyIsHiredCheckedAndBound(t *testing.T) {
	g := newHireFake(t)
	rep, err := g.run(hireSpec())
	if err != nil || rep.State != "done" {
		t.Fatalf("%v %s\n%s", err, rep.State, notesOf(rep))
	}
	// Five hires in the order tank, healer, three damage dealers; the level is the middle of the plan's band (43), not the commander's
	// (45); the 5th goes after the conversion to a raid the server asked for.
	want := []string{"hire warrior tank level=43", "hire priest healer level=43", "hire hunter dps level=43", "hire rogue dps level=43", "hire warlock dps level=43", "hire warlock dps level=43"}
	if strings.Join(g.hireLines, "|") != strings.Join(want, "|") || g.converted != 1 || g.idxOf("convert_raid") < 0 {
		t.Fatalf("hire lines %v, raid conversions %d", g.hireLines, g.converted)
	}
	if i := g.idxOf("convert_raid"); i < 0 || g.events[i-1] != "hire warlock dps level=43" || g.events[i+1] != "hire warlock dps level=43" {
		t.Fatalf("the 5th hire is refused, the party converted, the 5th hired: %v", g.events)
	}
	r := rep.Ready
	if r == nil || !r.Ready || r.Level != 43 || len(r.Bots) != 5 || len(r.Hired) != 5 || len(r.Reused) != 0 || len(r.Dismissed) != 0 {
		t.Fatalf("%+v", r)
	}
	for _, b := range r.Bots {
		if !b.Ready || b.Level != 43 || b.Quality != "rare" || b.ItemLevel < 40 || b.Source != "hired" || b.MaxHP == 0 || len(b.Problems) != 0 {
			t.Errorf("%+v", b)
		}
		if len(b.Fixed) == 0 && b.Class != "rogue" && b.Class != "warrior" {
			t.Errorf("%s came with empty bags and the step fixed nothing: %+v", b.Name, b)
		}
	}
	tank, healer, hunter := r.Bots[0], r.Bots[1], r.Bots[2]
	if tank.Class != "warrior" || tank.Role != "tank" || tank.Shield == nil || !*tank.Shield || tank.Talents != "0/0/36" {
		t.Errorf("tank %+v", tank)
	}
	if healer.Drinks != 20 || !strings.Contains(strings.Join(healer.Fixed, "|"), "maintenance: drinks 0") {
		t.Errorf("healer %+v", healer)
	}
	if hunter.Pet == "" || hunter.Ammo != "bullets 200" || !slices.Contains(hunter.Spells, "Tame Beast") || !slices.Contains(tank.Spells, "Taunt") || !slices.Contains(tank.Spells, "Sunder Armor") || !slices.Contains(tank.Spells, "Defensive Stance") {
		t.Errorf("hunter %+v tank %+v", hunter, tank)
	}
	// The bind: the tank leads, the commander stands outside, and the run's own bind found the binding and used it.
	if g.idxOf("cbind Zamoraela Khaeworval Corkhas Gredel Zisorlo") < 0 || g.groupAtBind == nil || *g.groupAtBind {
		t.Fatalf("bind/outside: %v", g.events)
	}
	if rep.Remote.LeadBot != "Zamoraela" || rep.Remote.Tank != "Zamoraela" || strings.Join(rep.Remote.Bots, ",") != "Khaeworval,Corkhas,Gredel,Zisorlo" {
		t.Fatalf("%+v", rep.Remote)
	}
	if !strings.Contains(notesOf(rep), "already bound to Zamoraela with 5 bots, the same group; reusing the binding") {
		t.Fatalf("the run did not adopt the binding\n%s", notesOf(rep))
	}
	// Honest counts: the hire step sends no GM line; the run's own tele (the enter phase) is its only one.
	p := rep.Prepare
	if p == nil || len(p.Steps) != 1 || p.Steps[0].Name != "hire" || p.GMCommands != 0 || rep.Remote.GMCommands != 1 || len(rep.SetupShortcuts) != 0 {
		t.Fatalf("%+v setup %v gm %d", p, rep.SetupShortcuts, rep.Remote.GMCommands)
	}
	if r.Seconds <= 0 || r.Seconds > 120 || rep.SetupS < r.Seconds || r.SetupS != rep.SetupS {
		t.Fatalf("seconds %.1f setup_s %.1f ready.setup_s %.1f", r.Seconds, rep.SetupS, r.SetupS)
	}
	if g.count("leave") != 0 {
		t.Fatalf("a hire was told to leave (that ends it): %v", g.events)
	}
}

func TestHireAtTheCommandersLevelSendsNoLevelArgument(t *testing.T) {
	g := newHireFake(t)
	g.agentLvl = 43
	spec := hireSpec()
	spec.Hire = &HireSpec{Composition: []string{"warrior", "priest", "mage"}}
	rep, err := g.run(spec)
	if err != nil || rep.State != "done" {
		t.Fatalf("%v %s\n%s", err, rep.State, notesOf(rep))
	}
	if strings.Join(g.hireLines, "|") != "hire warrior tank|hire priest healer|hire mage dps" || len(rep.Ready.Bots) != 3 {
		t.Fatalf("%v", g.hireLines)
	}
}

func TestHireServerThatRefusesTheLevelFallsBackToTheCommandersLevel(t *testing.T) {
	g := newHireFake(t)
	g.levelMode = "refuse"
	spec := hireSpec()
	spec.Hire = &HireSpec{Composition: []string{"warrior", "priest", "mage"}}
	rep, err := g.run(spec)
	if err != nil || rep.State != "done" {
		t.Fatalf("%v %s\n%s", err, rep.State, notesOf(rep))
	}
	// The first line is refused, the second is sent without the level, and so are the rest: no more refusals.
	if strings.Join(g.hireLines, "|") != "hire warrior tank level=43|hire warrior tank|hire priest healer|hire mage dps" {
		t.Fatalf("%v", g.hireLines)
	}
	r := rep.Ready
	if r.Level != 43 || !strings.Contains(r.LevelNote, "refused level=<n>") || r.Bots[0].Level != 45 {
		t.Fatalf("%+v", r)
	}
}

func TestHireServerThatIgnoresTheLevelIsFoundOutFromTheAnswer(t *testing.T) {
	g := newHireFake(t)
	g.levelMode = "ignore"
	spec := hireSpec()
	spec.Hire = &HireSpec{Composition: []string{"warrior", "priest"}}
	rep, err := g.run(spec)
	if err != nil || rep.State != "done" || !strings.Contains(rep.Ready.LevelNote, "ignores level=<n>") || rep.Ready.Bots[1].Level != 45 {
		t.Fatalf("%v %s %+v\n%s", err, rep.State, rep.Ready, notesOf(rep))
	}
	if g.hireLines[1] != "hire priest healer" {
		t.Fatalf("after the first answer the level is not sent again: %v", g.hireLines)
	}
}

func TestHireFallbackOutsideTheBandFails(t *testing.T) {
	g := newHireFake(t)
	g.levelMode = "ignore"
	g.agentLvl = 50
	spec := hireSpec()
	spec.Hire = &HireSpec{Composition: []string{"warrior", "priest"}}
	rep, err := g.run(spec)
	if !failed(rep, err, "level 50 is outside the plan's band 41-45") {
		t.Fatalf("%v %s", err, rep.State)
	}
	if g.count("cbind") != 0 {
		t.Fatalf("bound after a failed check: %v", g.events)
	}
	if rep.Ready == nil || rep.Ready.Ready || len(rep.Ready.Bots) != 2 || rep.Ready.Bots[0].Ready {
		t.Fatalf("the failed block is in the report: %+v", rep.Ready)
	}
}

func TestHireLevelLoweredToTheCommandersLimit(t *testing.T) {
	g := newHireFake(t)
	g.agentLvl = 42 // can hire up to 42 (HireMaxLevelAboveHirer 0); the band is 41-45, so 42 is inside
	spec := hireSpec()
	spec.Hire = &HireSpec{Level: 44, Composition: []string{"warrior", "priest"}}
	rep, err := g.run(spec)
	if err != nil || rep.State != "done" || rep.Ready.Bots[0].Level != 42 || !strings.Contains(rep.Ready.LevelNote, "lowered the level 44 to the commander's limit 42") {
		t.Fatalf("%v %s %+v", err, rep.State, rep.Ready)
	}
	// A level other than asked for, without a refusal to explain it, fails.
	g2 := newHireFake(t)
	g2.hires = nil
	g2.agentLvl = 45
	h := g2.preHire("warrior", "tank", 44)
	_ = h
	spec.Hire = &HireSpec{Level: 43, Composition: []string{"warrior", "priest"}}
	rep, err = g2.run(spec)
	if err != nil || rep.State != "done" || len(rep.Ready.Dismissed) != 1 || rep.Ready.Bots[0].Level != 43 {
		t.Fatalf("a level 44 hire is not a level 43 hire: %v %s %+v", err, rep.State, rep.Ready)
	}
}

func TestHireRehireReleasesTheBindingAndDismissesTheOldHires(t *testing.T) {
	g := newHireFake(t)
	for _, c := range [][2]string{{"warrior", "tank"}, {"priest", "healer"}, {"hunter", "dps"}, {"rogue", "dps"}, {"warlock", "dps"}} {
		g.preHire(c[0], c[1], 43)
	}
	g.nextName = 5
	g.raid = true
	g.preBind()
	old := []string{"Zamoraela", "Khaeworval", "Corkhas", "Gredel", "Zisorlo"}
	spec := hireSpec()
	spec.Hire = &HireSpec{Rehire: true}
	rep, err := g.run(spec)
	if err != nil || rep.State != "done" {
		t.Fatalf("%v %s\n%s", err, rep.State, notesOf(rep))
	}
	if g.idxOf("cunbind") < 0 || g.idxOf("cunbind") > g.idxOf("logout Zamoraela") || g.count("logout") != 5 || g.idxOf("hire warrior") < g.idxOf("logout Zisorlo") {
		t.Fatalf("order: %v", g.events)
	}
	r := rep.Ready
	if d := slices.Sorted(slices.Values(r.Dismissed)); strings.Join(d, ",") != strings.Join(slices.Sorted(slices.Values(old)), ",") || len(r.Hired) != 5 || len(r.Reused) != 0 {
		t.Fatalf("%+v", r)
	}
	for _, b := range r.Bots {
		if slices.Contains(old, b.Name) {
			t.Errorf("%s is an old hire", b.Name)
		}
	}
	if g.count("leave") != 0 {
		t.Fatalf("leave: %v", g.events)
	}
}

func TestHireKeepsTheHiresThatMatchAndTheStandingBinding(t *testing.T) {
	g := newHireFake(t)
	for _, c := range [][2]string{{"warrior", "tank"}, {"priest", "healer"}, {"hunter", "dps"}, {"rogue", "dps"}, {"warlock", "dps"}} {
		g.preHire(c[0], c[1], 43)
	}
	g.nextName = 5
	g.raid = true
	g.preBind()
	rep, err := g.run(hireSpec())
	if err != nil || rep.State != "done" {
		t.Fatalf("%v %s\n%s", err, rep.State, notesOf(rep))
	}
	if len(g.hireLines) != 0 || g.count("logout") != 0 || g.count("cunbind") != 1 || g.count("cbind") != 1 {
		// the one bind is the run's own (refused as already-bound and adopted), the one unbind its end
		t.Fatalf("a matching party was touched: %v", g.events)
	}
	r := rep.Ready
	if len(r.Reused) != 5 || len(r.Hired) != 0 || !r.Ready {
		t.Fatalf("%+v", r)
	}
	for _, b := range r.Bots {
		if b.Source != "reused" || len(b.Fixed) == 0 || !strings.HasPrefix(b.Fixed[len(b.Fixed)-1], "maintenance:") {
			t.Errorf("a kept hire is maintained (repair and restock): %+v", b)
		}
	}
	if !strings.Contains(notesOf(rep), "the binding of Zamoraela's group stands and holds the wanted hires: kept") {
		t.Fatalf("\n%s", notesOf(rep))
	}
}

func TestHireKeepsTheMatchingOnesAndReplacesTheWrongOnes(t *testing.T) {
	g := newHireFake(t)
	g.preHire("warrior", "dps", 43) // an arms warrior: no tank
	g.preHire("priest", "healer", 43)
	g.preHire("hunter", "dps", 43)
	g.preHire("rogue", "dps", 41) // the wrong level
	g.preHire("warlock", "dps", 43)
	g.nextName = 5
	g.raid = true
	g.preBind()
	rep, err := g.run(hireSpec())
	if err != nil || rep.State != "done" {
		t.Fatalf("%v %s\n%s", err, rep.State, notesOf(rep))
	}
	r := rep.Ready
	if strings.Join(r.Reused, ",") != "Khaeworval,Corkhas,Zisorlo" || strings.Join(slices.Sorted(slices.Values(r.Dismissed)), ",") != "Gredel,Zamoraela" || len(r.Hired) != 2 || g.hireLines[0] != "hire warrior tank level=43" || g.hireLines[1] != "hire rogue dps level=43" {
		t.Fatalf("%+v %v", r, g.hireLines)
	}
	if g.idxOf("cunbind") < 0 || g.idxOf("cunbind") > g.idxOf("logout") || g.idxOf("cbind") < g.idxOf("hire rogue") {
		t.Fatalf("a changed party is unbound, then rebound: %v", g.events)
	}
}

// A hire that has no empty slot of its kind is surplus, not wrong: nothing is deleted for it, the step stops and names it.
func TestHireExtraHireIsNeverDismissed(t *testing.T) {
	g := newHireFake(t)
	for _, c := range [][2]string{{"warrior", "tank"}, {"priest", "healer"}, {"hunter", "dps"}, {"rogue", "dps"}, {"warlock", "dps"}, {"mage", "dps"}} {
		g.preHire(c[0], c[1], 43)
	}
	g.nextName, g.maxHires, g.raid = 6, 6, true
	g.preBind()
	rep, err := g.run(hireSpec())
	if !failed(rep, err, "Alrunmel", "not dismissed", "rehire") || g.count("logout") != 0 || g.count("cunbind") != 0 || len(g.hireLines) != 0 {
		t.Fatalf("%v %s %v", err, rep.State, g.events)
	}
}

// Bound hires inside an instance are not in the agent's view: the module's own `who` answer says what they are, they are kept, and the
// binding stands.
func TestHireBoundHiresOutOfViewAreKept(t *testing.T) {
	g := newHireFake(t)
	for _, c := range [][2]string{{"warrior", "tank"}, {"priest", "healer"}, {"hunter", "dps"}, {"rogue", "dps"}, {"warlock", "dps"}} {
		g.preHire(c[0], c[1], 43).hidden = true
	}
	g.nextName, g.raid = 5, true
	g.preBind()
	rep, err := g.run(hireSpec())
	if err != nil || rep.State != "done" {
		t.Fatalf("%v %s\n%s", err, rep.State, notesOf(rep))
	}
	if len(g.hireLines) != 0 || g.count("logout") != 0 || len(rep.Ready.Reused) != 5 || len(rep.Ready.Dismissed) != 0 {
		t.Fatalf("hires out of view were touched: %v %+v", g.events, rep.Ready)
	}
	if w := rep.Ready.Bots[0].Warnings; len(w) == 0 || !strings.Contains(w[0], "not in the agent's group or view") {
		t.Fatalf("%+v", rep.Ready.Bots[0])
	}
	if g.count("who ") != 5 {
		t.Fatalf("one who per hire: %v", g.events)
	}
}

// A hire that does not answer (teleporting, offline in its logout grace) is not placed and not dismissed: the step stops and names it.
func TestHireHireThatDoesNotAnswerIsKept(t *testing.T) {
	g := newHireFake(t)
	for _, c := range [][2]string{{"warrior", "tank"}, {"priest", "healer"}, {"hunter", "dps"}, {"rogue", "dps"}, {"warlock", "dps"}} {
		g.preHire(c[0], c[1], 43)
	}
	g.nextName, g.raid = 5, true
	g.preBind()
	g.unreachable["Corkhas"] = true
	rep, err := g.run(hireSpec())
	if !failed(rep, err, "no answer from the commander's hire Corkhas", "not dismissed") || g.count("logout") != 0 || g.count("cunbind") != 0 || len(g.hireLines) != 0 {
		t.Fatalf("%v %s %v", err, rep.State, g.events)
	}
	// rehire is the caller's word: those hires go whatever they answer, and none is asked who it is.
	g.unreachable["Corkhas"] = false
	g.unreachable["Gredel"] = true
	spec := hireSpec()
	spec.Hire = &HireSpec{Rehire: true}
	g.events, g.hireLines = nil, nil
	rep, err = g.run(spec)
	if err != nil || rep.State != "done" || g.count("logout") != 5 || g.count("who ") != 0 {
		t.Fatalf("%v %s %v", err, rep.State, g.events)
	}
}

// A talents whisper that times out is a failed check of that bot, never a reason to delete it.
func TestHireTimedOutTalentsReadKeepsTheHire(t *testing.T) {
	g := newHireFake(t)
	for _, c := range [][2]string{{"warrior", "tank"}, {"priest", "healer"}} {
		g.preHire(c[0], c[1], 43)
	}
	g.nextName, g.raid = 2, true
	g.preBind()
	g.noTalents["Khaeworval"] = true
	spec := hireSpec()
	spec.Hire = &HireSpec{Composition: []string{"warrior", "priest"}}
	rep, err := g.run(spec)
	if !failed(rep, err, "Khaeworval", "talents: no answer") || g.count("logout") != 0 || len(g.hireLines) != 0 || len(rep.Ready.Dismissed) != 0 {
		t.Fatalf("%v %s %v", err, rep.State, g.events)
	}
}

// The old binding of an alt party (owned bots, bound from outside) is ended, and the alts are left alone.
func TestHireEndsAnOldBindingOfAltsAndLeavesThemAlone(t *testing.T) {
	g := newHireFake(t)
	g.alts = []string{"Lcwarra", "Lcpriest"}
	g.binding = []string{"Lcwarra", "Lcpriest"}
	g.fakeRemote.bound = true
	spec := hireSpec()
	spec.Hire = &HireSpec{Composition: []string{"warrior", "priest"}}
	rep, err := g.run(spec)
	if err != nil || rep.State != "done" {
		t.Fatalf("%v %s\n%s", err, rep.State, notesOf(rep))
	}
	if g.idxOf("cunbind") < 0 || g.idxOf("cunbind") > g.idxOf("hire warrior") || g.count("logout") != 0 || g.count("leave") != 0 {
		t.Fatalf("%v", g.events)
	}
	if len(rep.Ready.Dismissed) != 0 || len(rep.Ready.Hired) != 2 {
		t.Fatalf("%+v", rep.Ready)
	}
}

func TestHireOwnedBotInTheGroupIsInTheWay(t *testing.T) {
	g := newHireFake(t)
	g.alts, g.altGroup = []string{"Lcwarra"}, true
	rep, err := g.run(hireSpec())
	if !failed(rep, err, "Lcwarra", "owned bot") || len(g.hireLines) != 0 {
		t.Fatalf("%v %s %v", err, rep.State, g.hireLines)
	}
}

// ---- the checks ----

func TestHireEpicGearAndAMissingShieldAreFixedWithAutogear(t *testing.T) {
	g := newHireFake(t)
	g.epic["warlock"] = true
	g.noShield["warrior"] = true
	rep, err := g.run(hireSpec())
	if err != nil || rep.State != "done" {
		t.Fatalf("%v %s\n%s", err, rep.State, notesOf(rep))
	}
	if g.count("autogear") != 2 {
		t.Fatalf("autogear lines: %v", g.events)
	}
	tank, lock := rep.Ready.Bots[0], rep.Ready.Bots[4]
	if len(tank.Fixed) == 0 || !strings.HasPrefix(tank.Fixed[0], "autogear blue: shield") || tank.Shield == nil || !*tank.Shield {
		t.Errorf("tank %+v", tank)
	}
	if len(lock.Fixed) == 0 || !strings.HasPrefix(lock.Fixed[0], "autogear blue: epic Epic 4") || lock.Quality != "rare" {
		t.Errorf("warlock %+v", lock)
	}
}

func TestHireGearThatAutogearDoesNotFixFailsNamingTheBot(t *testing.T) {
	g := newHireFake(t)
	g.epic["priest"] = true
	g.autogearOff = true
	rep, err := g.run(hireSpec())
	if !failed(rep, err, "1 of 5 hires are not ready", "Khaeworval", "gear still wrong after autogear blue: epic Epic 4") {
		t.Fatalf("%v %s", err, rep.State)
	}
	if g.count("cbind") != 0 {
		t.Fatalf("bound a party that is not ready: %v", g.events)
	}
	if rep.Ready.Ready || rep.Ready.Bots[1].Ready || !rep.Ready.Bots[0].Ready {
		t.Fatalf("%+v", rep.Ready)
	}
}

func TestHireNoFixReportsAndSendsNoAutogear(t *testing.T) {
	g := newHireFake(t)
	g.epic["priest"] = true
	spec := hireSpec()
	spec.Hire = &HireSpec{NoFix: true}
	rep, err := g.run(spec)
	if !failed(rep, err, "Khaeworval", "gear: epic Epic 4", "no_fix") || g.count("autogear") != 0 {
		t.Fatalf("%v %s %v", err, rep.State, g.events)
	}
}

func TestHireMissingSpellFailsAndTheStepLearnsNothing(t *testing.T) {
	g := newHireFake(t)
	g.lack["warrior"] = []string{"Taunt"}
	rep, err := g.run(hireSpec())
	if !failed(rep, err, "Zamoraela", "lacks Taunt", "GM command") {
		t.Fatalf("%v %s", err, rep.State)
	}
	if g.count("tele") != 0 || rep.Prepare.GMCommands != 0 {
		t.Fatalf("a GM line went out: %v", g.events)
	}
}

// A bot that never answers `spells <filter>` is "no answer", not a bot that lacks the spell.
func TestHireSpellQuestionThatIsNotAnsweredFailsAsNoAnswer(t *testing.T) {
	g := newHireFake(t)
	g.silentList = true
	rep, err := g.run(hireSpec())
	if !failed(rep, err, "Zamoraela", "no answer from Zamoraela to `spells stance`") || strings.Contains(err.Error(), "lacks") {
		t.Fatalf("%v %s", err, rep.State)
	}
}

// The real bot sends the header and then ONE spell per line (ListSpellsAction.cpp): a family with two spells (stance, pet) is read whole.
func TestHireSpellFamilyWithTwoSpellsIsReadFromAllItsLines(t *testing.T) {
	g := newHireFake(t)
	rep, err := g.run(hireSpec())
	if err != nil || rep.State != "done" {
		t.Fatalf("%v %s\n%s", err, rep.State, notesOf(rep))
	}
	tank, hunter := rep.Ready.Bots[0], rep.Ready.Bots[2]
	if !slices.Contains(tank.Spells, "Battle Stance") || !slices.Contains(tank.Spells, "Defensive Stance") || !slices.Contains(hunter.Spells, "Call Pet") || !slices.Contains(hunter.Spells, "Mend Pet") {
		t.Fatalf("%+v %+v", tank, hunter)
	}
	// A family of which the bot lacks one spell: the other is seen, the missing one is named.
	g = newHireFake(t)
	g.lack["hunter"] = []string{"Mend Pet"}
	rep, err = g.run(hireSpec())
	if !failed(rep, err, "Corkhas", "lacks Mend Pet") || strings.Contains(err.Error(), "Call Pet,") {
		t.Fatalf("%v %s", err, rep.State)
	}
}

func TestHireWrongTalentsFailWithTheRole(t *testing.T) {
	g := newHireFake(t)
	hireTrees["warrior:tank"] = [3]int{36, 0, 0} // an arms build for the tank
	defer func() { hireTrees["warrior:tank"] = [3]int{0, 0, 36} }()
	rep, err := g.run(hireSpec())
	if !failed(rep, err, "Zamoraela", "talents 36/0/0 are for dps, wanted tank") {
		t.Fatalf("%v %s", err, rep.State)
	}
}

func TestHireSuppliesThatMaintenanceCannotGiveFail(t *testing.T) {
	g := newHireFake(t)
	g.maintOff = true
	rep, err := g.run(hireSpec())
	if !failed(rep, err, "Khaeworval", "0 drinks, 20 wanted") {
		t.Fatalf("%v %s", err, rep.State)
	}
	if w := rep.Ready.Bots[1].Warnings; len(w) == 0 || !strings.Contains(w[0], "maintenance is off") {
		t.Fatalf("%+v", rep.Ready.Bots[1])
	}
}

func TestHireMissingPetFailsWithinItsCeiling(t *testing.T) {
	g := newHireFake(t)
	g.noPet = true
	began := g.now
	rep, err := g.run(hireSpec())
	if !failed(rep, err, "Corkhas", "no pet out") || !strings.Contains(err.Error(), "Zisorlo") {
		t.Fatalf("%v %s", err, rep.State)
	}
	if took := g.now.Sub(began); took > 150*time.Second { // the hunter and the warlock wait 60 s each
		t.Fatalf("took %v", took)
	}
}

// A pet that shows 50 s after the check began is waited for (the ceiling is 60 s), and a bot with no pet at 20 s is told once to summon one;
// a pet that never comes still fails.
func TestHireWaitsSixtySecondsForAPetAndNudgesOnce(t *testing.T) {
	g := newHireFake(t)
	g.petLate = 50
	rep, err := g.run(hireSpec())
	if err != nil || rep.State == "failed" {
		t.Fatalf("%v %s", err, rep.State)
	}
	nudges := map[string]int{}
	for _, c := range g.botCmds {
		if strings.Contains(c, " summon voidwalker") || strings.Contains(c, " call pet") {
			nudges[c]++
		}
	}
	if len(nudges) == 0 {
		t.Fatalf("no summon-pet order: %v", g.botCmds)
	}
	for c, n := range nudges {
		if n != 1 {
			t.Errorf("%q sent %d times", c, n)
		}
	}
	if hirePetWait != 60*time.Second || hirePetNudge != 20*time.Second {
		t.Errorf("%v %v", hirePetWait, hirePetNudge)
	}
}

func TestHireItemQualityThatTheServerDoesNotDescribeIsAWarning(t *testing.T) {
	g := newHireFake(t)
	g.itemSilent = true
	spec := hireSpec()
	spec.Hire = &HireSpec{Composition: []string{"warrior", "priest"}}
	rep, err := g.run(spec)
	if err != nil || rep.State != "done" {
		t.Fatalf("%v %s\n%s", err, rep.State, notesOf(rep))
	}
	if w := rep.Ready.Bots[0].Warnings; len(w) == 0 || !strings.Contains(strings.Join(w, "|"), "quality is not verified") {
		t.Fatalf("%+v", rep.Ready.Bots[0])
	}
}

// ---- waits and the bind ----

func TestHireArrivalWaitHasACeiling(t *testing.T) {
	g := newHireFake(t)
	g.arriveIn = time.Hour
	began := g.now
	spec := hireSpec()
	spec.Hire = &HireSpec{WaitS: 20, Composition: []string{"warrior", "priest"}}
	rep, err := g.run(spec)
	if !failed(rep, err, "not in the commander's group", "within 20 s") {
		t.Fatalf("%v %s", err, rep.State)
	}
	if took := g.now.Sub(began); took > 40*time.Second {
		t.Fatalf("a 20 s wait took %v", took)
	}
}

func TestHireTheServerRefusesAHire(t *testing.T) {
	for reply, want := range map[string]string{
		"Companions are hired at a <Mercenary Hire> recruiter. Look for one in an inn.": "recruiter",
		"You cannot afford that companion (cost 2g).":                                   "cannot afford",
		"You already command the maximum number of companions.":                         "maximum number",
	} {
		g := newHireFake(t)
		g.hireReply = reply
		rep, err := g.run(hireSpec())
		if !failed(rep, err, "refused", want) || g.count("hire") != 1 {
			t.Errorf("%q: %v %s %v", reply, err, rep.State, g.hireLines)
		}
	}
}

func TestHireBindRefusedAsHiredNamesTheMissingServerSupport(t *testing.T) {
	g := newHireFake(t)
	g.refuseHired = true
	rep, err := g.run(hireSpec())
	if !failed(rep, err, "commander bind", "hired", "#935") {
		t.Fatalf("%v %s", err, rep.State)
	}
	if g.count("leave") != 0 {
		t.Fatalf("the hires were told to leave (that would end them): %v", g.events)
	}
}

func TestHireCommanderLeftInTheGroupFailsAfterItsCeiling(t *testing.T) {
	g := newHireFake(t)
	g.keepInGroup = true
	began := g.now
	spec := hireSpec()
	spec.Hire = &HireSpec{Composition: []string{"warrior", "priest"}}
	rep, err := g.run(spec)
	if !failed(rep, err, "commander is still in a group", "#935") {
		t.Fatalf("%v %s", err, rep.State)
	}
	if took := g.now.Sub(began); took > 150*time.Second { // the hunter and the warlock wait 60 s each
		t.Fatalf("took %v", took)
	}
	// The failed step took the binding down again.
	if g.binding != nil {
		t.Fatalf("a binding is left after a failed prepare: %v", g.binding)
	}
}

// ---- the pieces ----

func TestHireSpecValidate(t *testing.T) {
	ok := func(h *HireSpec) PrepareSpec { return PrepareSpec{Party: "hire", Hire: h} }
	for _, c := range []struct {
		name string
		spec PrepareSpec
		want string // "" valid
	}{
		{"default", ok(nil), ""},
		{"empty block", ok(&HireSpec{}), ""},
		{"composition", ok(&HireSpec{Composition: []string{"warrior", "priest", "mage", "Rogue", "hunter:dps"}, Level: 43, WaitS: 60}), ""},
		{"paladin tank", ok(&HireSpec{Composition: []string{"paladin:tank", "priest"}}), ""},
		{"online with hire", PrepareSpec{Party: "hire", Online: true}, "online and fresh do not apply"},
		{"fresh with hire", PrepareSpec{Party: "hire", Fresh: true}, "online and fresh do not apply"},
		{"unknown party", PrepareSpec{Party: "alts"}, `"alts" is not "hire"`},
		{"hire without party", PrepareSpec{Hire: &HireSpec{}}, `goes with prepare.party "hire"`},
		{"level 61", ok(&HireSpec{Level: 61}), "prepare.hire.level"},
		{"wait", ok(&HireSpec{WaitS: 500}), "wait_s"},
		{"one bot", ok(&HireSpec{Composition: []string{"warrior"}}), "use 2 to 5"},
		{"six bots", ok(&HireSpec{Composition: []string{"warrior", "priest", "mage", "mage", "mage", "mage"}}), "use 2 to 5"},
		{"two tanks", ok(&HireSpec{Composition: []string{"warrior", "warrior", "priest"}}), "2 tanks and 1 healers"},
		{"no healer", ok(&HireSpec{Composition: []string{"warrior", "mage"}}), "1 tanks and 0 healers"},
		{"paladin needs a role", ok(&HireSpec{Composition: []string{"paladin", "warrior", "priest"}}), "needs a role"},
		{"mage tank", ok(&HireSpec{Composition: []string{"mage:tank", "priest"}}), "cannot be tank"},
		{"unknown class", ok(&HireSpec{Composition: []string{"monk", "priest"}}), "not \"class\" or"},
		{"unknown role", ok(&HireSpec{Composition: []string{"warrior:boss", "priest"}}), "tank, healer or dps"},
	} {
		err := c.spec.Validate(true)
		switch {
		case c.want == "" && err != nil:
			t.Errorf("%s: %v", c.name, err)
		case c.want != "" && (err == nil || !strings.Contains(err.Error(), c.want)):
			t.Errorf("%s: %v, want %q", c.name, err, c.want)
		}
	}
	if err := (PrepareSpec{Party: "hire"}).Validate(false); err == nil {
		t.Error("a group run takes no prepare block")
	}
	if got := DefaultHireComposition(); strings.Join(got, ",") != "warrior:tank,priest:healer,hunter:dps,rogue:dps,warlock:dps" {
		t.Errorf("%v", got)
	}
}

func TestHireLevelNeedsTheBandOrTheSpec(t *testing.T) {
	g := newHireFake(t)
	g.plan.LevelBand = nil
	rep, err := g.run(hireSpec())
	if !failed(rep, err, "no level_band", "prepare.hire.level") {
		t.Fatalf("%v %s", err, rep.State)
	}
	spec := hireSpec()
	spec.Hire = &HireSpec{Level: 45, Composition: []string{"warrior", "priest"}}
	g = newHireFake(t)
	g.plan.LevelBand = nil
	if rep, err = g.run(spec); err != nil || rep.State != "done" {
		t.Fatalf("%v %s", err, rep.State)
	}
}

func TestParseHireReply(t *testing.T) {
	for _, c := range []struct {
		line string
		want hireReply
		done bool
	}{
		{"Hired Zisorlo for 75s. Your companion is mustering.", hireReply{Name: "Zisorlo"}, true},
		{"Hired Corkhas for 1g 20s 3c. Your companion is mustering. Level 43.", hireReply{Name: "Corkhas", Level: 43}, true},
		{"Hired Corkhas for 75s. Your companion is mustering. Level 45. The level you asked for is above your limit (45).", hireReply{Name: "Corkhas", Level: 45, Limit: 45}, true},
		{"Your party is full. Convert to a raid to hire more companions.", hireReply{Refusal: "Your party is full. Convert to a raid to hire more companions.", Code: "raid-full"}, true},
		{"Bad level \"level=0\": use level=<1-60> or level <1-60>, for example .bot hire mage level=42.", hireReply{Refusal: "Bad level \"level=0\": use level=<1-60> or level <1-60>, for example .bot hire mage level=42.", Code: "level-arg"}, true},
		{"Give only one level.", hireReply{Refusal: "Give only one level.", Code: "level-arg"}, true},
		{"You cannot afford that companion (1g).", hireReply{Refusal: "You cannot afford that companion (1g).", Code: "other"}, true},
		{"GM Zfmagea just logged in.", hireReply{}, false},
		{"Forwarded command for X: talents; action success is not guaranteed.", hireReply{}, false},
	} {
		got, done := parseHireReply(c.line)
		if got != c.want || done != c.done {
			t.Errorf("%q: %+v %v, want %+v %v", c.line, got, done, c.want, c.done)
		}
	}
}

func TestTalentRole(t *testing.T) {
	for _, c := range []struct {
		class string
		trees [3]int
		want  string
	}{
		{"warrior", [3]int{0, 0, 36}, "tank"}, {"warrior", [3]int{31, 5, 0}, "dps"}, {"warrior", [3]int{5, 31, 0}, "dps"},
		{"priest", [3]int{36, 0, 0}, "healer"}, {"priest", [3]int{0, 36, 0}, "healer"}, {"priest", [3]int{0, 0, 36}, "dps"},
		{"paladin", [3]int{0, 36, 0}, "tank"}, {"paladin", [3]int{36, 0, 0}, "healer"}, {"paladin", [3]int{0, 0, 36}, "dps"},
		{"druid", [3]int{0, 36, 0}, "feral"}, {"druid", [3]int{0, 0, 36}, "healer"}, {"shaman", [3]int{0, 0, 31}, "healer"},
		{"mage", [3]int{0, 36, 0}, "dps"}, {"hunter", [3]int{0, 0, 36}, "dps"}, {"warlock", [3]int{36, 0, 0}, "dps"},
		{"warrior", [3]int{0, 0, 0}, ""},
	} {
		if got := talentRole(c.class, c.trees); got != c.want {
			t.Errorf("%s %v: %q, want %q", c.class, c.trees, got, c.want)
		}
	}
}

func TestKeySpellsFollowClassRoleAndLevel(t *testing.T) {
	names := func(cs []spellCheck) string {
		var out []string
		for _, c := range cs {
			for _, w := range c.want {
				out = append(out, w.name)
			}
		}
		return strings.Join(out, ",")
	}
	if got := names(keySpells("warrior", "tank", 43)); got != "Battle Stance,Defensive Stance,Taunt,Sunder Armor,Shield Block" {
		t.Errorf("tank %s", got)
	}
	if got := names(keySpells("warrior", "dps", 43)); got != "Battle Stance" {
		t.Errorf("warrior dps %s", got)
	}
	if got := names(keySpells("priest", "healer", 43)); got != "Resurrection,Renew,Dispel Magic" {
		t.Errorf("healer %s", got)
	}
	if got := names(keySpells("hunter", "dps", 43)); got != "Tame Beast,Call Pet,Mend Pet" {
		t.Errorf("hunter %s", got)
	}
	if got := names(keySpells("warlock", "dps", 43)); got != "Summon Imp,Summon Voidwalker" {
		t.Errorf("warlock %s", got)
	}
	if got := names(keySpells("warrior", "tank", 5)); got != "Battle Stance" {
		t.Errorf("a level 5 warrior has no taunt yet: %s", got)
	}
	f, m := spellsIn("[Taunt] |cff71d5ff|Hspell:355|h[Sunder Armor]|h|r", []spellReq{{"Taunt", 10}, {"Sunder Armor", 10}, {"Shield Block", 12}})
	if strings.Join(f, ",") != "Taunt,Sunder Armor" || strings.Join(m, ",") != "Shield Block" {
		t.Errorf("%v %v", f, m)
	}
}

func TestGearReadJudgesQualityShieldAndAmmo(t *testing.T) {
	inv := tbm.ParseInventory([]string{
		"TBM:INV_BEGIN|X|0", "TBM:INV_EQ|0|90000|1|1", "TBM:INV_EQ|3|90003|1|1", "TBM:INV_EQ|4|91004|1|1", "TBM:INV_EQ|6|90006|1|1", "TBM:INV_EQ|7|90007|1|1",
		fmt.Sprintf("TBM:INV_EQ|15|%d|1|1", 90015), fmt.Sprintf("TBM:INV_EQ|16|%d|1|1", shieldEntries[0]), fmt.Sprintf("TBM:INV_EQ|17|%d|1|1", bulletWeaponEntries[0]),
		fmt.Sprintf("TBM:INV_ITEM|255|23|%d|20|-", hDrink), fmt.Sprintf("TBM:INV_ITEM|255|24|%d|50|-", hBullet), "TBM:INV_END|X|10|16"})
	g := &gearRead{inv: inv, info: map[uint32]ItemInfo{
		90000: {Found: true, Name: "A", Quality: 3, ItemLevel: 40}, 90003: {Found: true, Name: "Shirt", Quality: 1, ItemLevel: 1},
		91004: {Found: true, Name: "Epic Chest", Quality: 4, ItemLevel: 60}, 90006: {Found: true, Name: "C", Quality: 3, ItemLevel: 44},
		90015:                  {Found: true, Name: "Hammer", Quality: 2, ItemLevel: 36},
		shieldEntries[0]:       {Found: true, Name: "Shield", Class: 4, SubClass: 6, Quality: 3, ItemLevel: 43},
		bulletWeaponEntries[0]: {Found: true, Name: "Gun", Class: 2, SubClass: 3, Quality: 3, ItemLevel: 44}}}
	best, ilvl, epics, unknown := g.summary(45)
	if best != 4 || len(epics) != 1 || !strings.HasPrefix(epics[0], "Epic Chest") || unknown != 1 /* feet 90007 has no prototype */ {
		t.Fatalf("%d %.1f %v %d", best, ilvl, epics, unknown)
	}
	if want := (40.0 + 60 + 44 + 36 + 43 + 44) / 6; ilvl < want-0.01 || ilvl > want+0.01 {
		t.Fatalf("ilvl %.2f, want %.2f (shirt left out, unknown left out)", ilvl, want)
	}
	if miss := g.missingGear("warrior", "tank"); len(miss) != 0 {
		t.Fatalf("%v", miss)
	}
	delete(g.inv.Equipped, tbm.SlotOffHand)
	if miss := g.missingGear("warrior", "tank"); strings.Join(miss, ",") != "shield" {
		t.Fatalf("%v", miss)
	}
	if miss := g.missingGear("mage", "dps"); len(miss) != 0 {
		t.Fatalf("a mage needs no shield: %v", miss)
	}
	delete(g.inv.Equipped, slotHead)
	if miss := g.missingGear("hunter", "dps"); strings.Join(miss, ",") != "head" {
		t.Fatalf("%v", miss)
	}
	drinks, ammo, short := g.supplies("hunter", "dps")
	if drinks != 20 || ammo != "bullets 50" || len(short) != 1 || short[0] != "50 bullets, 100 wanted" {
		t.Fatalf("%d %q %v", drinks, ammo, short)
	}
}

// A hire of a kept binding does not answer `who`: the class and level a run that kept the binding read are used for the bots still bound.
func TestHireKeptBindingTakesWhoFromMemory(t *testing.T) {
	g := newHireFake(t)
	g.whoSilent = map[string]bool{}
	mem := map[string]tbm.Who{}
	for _, c := range [][2]string{{"warrior", "tank"}, {"priest", "healer"}, {"hunter", "dps"}, {"rogue", "dps"}, {"warlock", "dps"}} {
		h := g.preHire(c[0], c[1], 43)
		g.whoSilent[h.name] = true
		mem[strings.ToLower(h.name)] = tbm.Who{Class: h.class, Level: h.level, Trees: h.trees}
	}
	g.nextName, g.raid = 5, true
	g.preBind()
	// without the memory the silent hires stop the step; with it they are placed, kept and not asked
	rep, err := g.run(hireSpec())
	if !failed(rep, err, "to `who`") {
		t.Fatalf("%v %s", err, rep.State)
	}
	g.events, g.hireLines = nil, nil
	rep, err = runRemote(t, g, g.plan, RemoteOptions{Prepare: hireSpec(), HireMemory: mem}, nil)
	if err != nil || rep.State != "done" || g.count("logout") != 0 || len(g.hireLines) != 0 || len(rep.Ready.Reused) != 5 {
		t.Fatalf("%v %s %v\n%s", err, rep.State, g.events, notesOf(rep))
	}
}

// A resume with prepare party: hire into a kept binding: the bound hires answer no `.bot` line, so their gear, spells, talents and pet are not
// read again (a warning each); the party is reused as it is and the run goes on.
func TestHireKeptBindingIsNotCheckedAgain(t *testing.T) {
	g := newHireFake(t)
	g.whoSilent, g.deaf = map[string]bool{}, map[string]bool{}
	mem := map[string]tbm.Who{}
	for _, c := range [][2]string{{"warrior", "tank"}, {"priest", "healer"}, {"hunter", "dps"}, {"rogue", "dps"}, {"warlock", "dps"}} {
		h := g.preHire(c[0], c[1], 43)
		g.whoSilent[h.name], g.deaf[h.name] = true, true
		mem[strings.ToLower(h.name)] = tbm.Who{Class: h.class, Level: h.level, Trees: h.trees}
	}
	g.nextName, g.raid = 5, true
	g.preBind()
	rep, err := runRemote(t, g, g.plan, RemoteOptions{Prepare: hireSpec(), HireMemory: mem}, nil)
	if err != nil || rep.State != "done" || len(rep.Ready.Reused) != 5 {
		t.Fatalf("%v %s\n%s", err, rep.State, notesOf(rep))
	}
	for _, b := range rep.Ready.Bots {
		if len(b.Problems) != 0 || len(b.Warnings) == 0 || !strings.Contains(b.Warnings[len(b.Warnings)-1], "a bound hire of a kept binding answers no .bot line") {
			t.Errorf("%+v", b)
		}
	}
}

// "They can have epic as long as it's for their level" (owner, 2026-10-14): an epic whose required level is at most the bot's and whose
// item level is at most the bot's plus 5 is allowed; a higher required level or item level is out of band and still listed.
func TestSummaryAllowsEpicForTheLevel(t *testing.T) {
	inv := tbm.ParseInventory([]string{"TBM:INV_BEGIN|X|0", "TBM:INV_EQ|0|90000|1|1", "TBM:INV_EQ|4|90004|1|1", "TBM:INV_EQ|6|90006|1|1", "TBM:INV_EQ|7|90007|1|1", "TBM:INV_END|X|10|16"})
	g := &gearRead{inv: inv, info: map[uint32]ItemInfo{
		90000: {Found: true, Name: "In band", Quality: 4, ItemLevel: 50, RequiredLevel: 45},
		90004: {Found: true, Name: "Too high ilvl", Quality: 4, ItemLevel: 51, RequiredLevel: 45},
		90006: {Found: true, Name: "Too high req", Quality: 4, ItemLevel: 46, RequiredLevel: 46},
		90007: {Found: true, Name: "Blue", Quality: 3, ItemLevel: 40, RequiredLevel: 40}}}
	best, _, epics, _ := g.summary(45)
	if best != 4 || len(epics) != 2 || !strings.HasPrefix(epics[0], "Too high ilvl") || !strings.HasPrefix(epics[1], "Too high req") {
		t.Fatalf("%d %v", best, epics)
	}
	if _, _, epics, _ = g.summary(0); len(epics) != 3 {
		t.Fatalf("level unknown: every epic counts: %v", epics)
	}
}

// The epic rule judges each bot against the level it actually has (its own read), not the level asked for: a server that lowers level=44 to the
// commander's limit 42 must not let an epic for level 44 through.
func TestHireEpicIsJudgedAgainstTheBotsOwnLevel(t *testing.T) {
	run := func(agentLvl int) (Report, error) {
		g := newHireFake(t)
		g.agentLvl = agentLvl
		g.epic["priest"] = true
		g.autogearOff = true
		g.items[uint32(91000+slotChest)] = ItemInfo{Found: true, Name: "Epic 4", Quality: 4, ItemLevel: 48, RequiredLevel: 44}
		spec := hireSpec()
		spec.Hire = &HireSpec{Level: 44, Composition: []string{"warrior", "priest"}}
		return g.run(spec)
	}
	if rep, err := run(44); err != nil || rep.State != "done" {
		t.Fatalf("an epic for the bot's level (44) was refused: %v %s", err, rep.State)
	}
	rep, err := run(42)
	if !failed(rep, err, "Khaeworval", "epic Epic 4 (required level 44, item level 48)") || rep.Ready == nil || rep.Ready.Bots[1].Level != 42 {
		t.Fatalf("an epic for level 44 passed on a bot of level 42: %v %s", err, rep.State)
	}
}
