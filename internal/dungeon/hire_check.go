package dungeon

import (
	"context"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/pjerra/turtle-agent/internal/tbm"
)

// The readiness check of one hired bot (hire.go step 3; TEST-BRIEF "Party readiness standard"). A problem fails the step with the bot's
// name and the reason; a warning goes into the ready block and nothing more.

func (b *hireBot) fail(format string, a ...any) {
	b.rb.Problems = append(b.rb.Problems, fmt.Sprintf(format, a...))
}
func (b *hireBot) warn(format string, a ...any) {
	b.rb.Warnings = append(b.rb.Warnings, fmt.Sprintf(format, a...))
}

func (f *hireFlow) checkBot(ctx context.Context, b *hireBot) {
	b.rb = ReadyBot{Name: b.name, Class: b.slot.Class, Role: b.slot.Role, Source: "hired"}
	if b.reused {
		b.rb.Source = "reused"
	}
	m, ok := f.member(b)
	if !ok || m.Level == 0 || m.Class == "" {
		// Not in the agent's group or view (a bound hire inside an instance, one still teleporting): what the bot says of itself decides.
		w, why := f.readWho(ctx, b.name)
		if why != "" {
			b.fail("no answer from %s to `who` (%s) and the agent does not see it: its level and class are not known", b.name, why)
			return
		}
		m.Name, m.Online = b.name, true
		m.Level, m.Class = w.Level, w.Class
		if !ok {
			b.warn("not in the agent's group or view: level and class are from its own `who` answer; its health and pet are not checked")
		}
	}
	b.rb.Level, b.rb.MaxHP = m.Level, m.MaxHP
	switch {
	case m.Dead:
		b.fail("dead")
		return
	case m.Class != "" && m.Class != b.slot.Class:
		b.fail("is a %s, wanted a %s", m.Class, b.slot.Class)
		return
	}
	if p := f.levelProblem(m.Level); p != "" {
		b.fail("%s", p)
	}
	f.gearAndSupplies(ctx, b)
	f.checkSpells(ctx, b, m)
	f.checkTalents(ctx, b)
	f.checkPet(ctx, b, m)
}

// ---- gear and supplies ----

// Equipment slots a bot must have filled (the core's EquipmentSlots, 1.12).
const (
	slotHead  = 0
	slotChest = 4
	slotLegs  = 6
	slotFeet  = 7
	slotShirt = 3
	slotTabrd = 18
)

var requiredSlots = []struct {
	slot int
	name string
}{{slotHead, "head"}, {slotChest, "chest"}, {slotLegs, "legs"}, {slotFeet, "feet"}, {tbm.SlotMainHand, "main hand"}}

var qualityNames = []string{"poor", "common", "uncommon", "rare", "epic", "legendary"}

func qualityName(q int) string {
	if q >= 0 && q < len(qualityNames) {
		return qualityNames[q]
	}
	return fmt.Sprintf("quality %d", q)
}

// gearRead is a bot's bags and worn gear with the prototypes of the worn items.
type gearRead struct {
	inv  tbm.Inventory
	info map[uint32]ItemInfo
}

// readGear reads `.bot inv` and asks the server for the worn items.
func (f *hireFlow) readGear(ctx context.Context, name string) (*gearRead, error) {
	lines, settled, err := f.e.g.BotLines(ctx, "inv "+name, hireInvWait, func(l string) bool {
		return strings.HasPrefix(l, "TBM:INV_END|") || strings.HasPrefix(l, "TBM:ACTION_ERR|inv|")
	})
	inv := tbm.ParseInventory(lines)
	switch {
	case err != nil:
		return nil, err
	case inv.Err != nil:
		return nil, fmt.Errorf("refused: %s", inv.Err.Code)
	case !settled || !inv.Complete:
		return nil, fmt.Errorf("no answer in %.0f s", hireInvWait.Seconds())
	}
	g := &gearRead{inv: inv, info: map[uint32]ItemInfo{}}
	var entries []uint32
	for _, e := range inv.Equipped {
		if e != 0 {
			entries = append(entries, e)
		}
	}
	info, err := f.hg.ItemInfos(ctx, entries, hireItemWait)
	if err != nil {
		return g, nil // the bags are read; the quality stays unknown (a warning)
	}
	g.info = info
	return g, nil
}

// worn lists the worn items with their slot.
func (g *gearRead) wornSlots() []int {
	var out []int
	for s, e := range g.inv.Equipped {
		if e != 0 {
			out = append(out, s)
		}
	}
	slices.Sort(out)
	return out
}

// epicAllowance is how far above the bot's level an epic's item level may be: "they can have epic as long as it's for their level"
// (owner, 2026-10-14). An epic is for the level when its required level is at most the bot's and its item level at most the bot's plus this.
const epicAllowance = 5

// epicForLevel says whether an epic item suits a bot of the level: required level <= level and item level <= level + epicAllowance.
func epicForLevel(it ItemInfo, level int) bool {
	return it.RequiredLevel <= level && it.ItemLevel <= level+epicAllowance
}

// summary is the best quality worn, the average item level (shirt and tabard left out), the epic items that are not for the bot's level
// (level 0: every epic), and how many worn items the server did not describe. An epic for the level (epicForLevel) is not listed.
func (g *gearRead) summary(level int) (best int, ilvl float64, epics []string, unknown int) {
	best = -1
	sum, n := 0, 0
	for _, s := range g.wornSlots() {
		it, ok := g.info[g.inv.Equipped[s]]
		if !ok || !it.Found {
			unknown++
			continue
		}
		best = max(best, it.Quality)
		if it.Quality >= 4 && (level <= 0 || !epicForLevel(it, level)) {
			epics = append(epics, fmt.Sprintf("%s (required level %d, item level %d)", it.Name, it.RequiredLevel, it.ItemLevel))
		}
		if s != slotShirt && s != slotTabrd {
			sum += it.ItemLevel
			n++
		}
	}
	if n > 0 {
		ilvl = float64(sum) / float64(n)
	}
	return best, ilvl, epics, unknown
}

// hasShield says whether the off hand holds a shield (an armor of the shield subclass, else the shield table).
func (g *gearRead) hasShield() bool {
	e := g.inv.Equipped[tbm.SlotOffHand]
	if e == 0 {
		return false
	}
	if it, ok := g.info[e]; ok && it.Found {
		return it.Class == 4 && it.SubClass == 6
	}
	return shieldSet[e]
}

// missingGear names the slots a bot of the class and role must have filled and has not.
func (g *gearRead) missingGear(class, role string) []string {
	var out []string
	for _, r := range requiredSlots {
		if g.inv.Equipped[r.slot] == 0 {
			out = append(out, r.name)
		}
	}
	if class == "hunter" && g.inv.Equipped[tbm.SlotRanged] == 0 {
		out = append(out, "ranged weapon")
	}
	if role == "tank" && (class == "warrior" || class == "paladin") && !g.hasShield() {
		out = append(out, "shield")
	}
	return out
}

// softSlots are the armor slots autogear may leave empty at a low level: at level 16 it has no blue head piece for plate, leather or mail
// (the first Ragefire start: warrior, rogue and hunter "not ready: gear still wrong after autogear blue: head"). After autogear an empty one
// is a note, not a failure; the main hand, a hunter's ranged weapon and a tank's shield still fail.
var softSlots = map[string]bool{"head": true, "chest": true, "legs": true, "feet": true}

// splitMissing sorts missingGear's names into the ones that fail the check (main hand, ranged weapon, shield) and the soft armor slots.
func splitMissing(miss []string) (hard, soft []string) {
	for _, m := range miss {
		if softSlots[m] {
			soft = append(soft, m)
		} else {
			hard = append(hard, m)
		}
	}
	return hard, soft
}

// usesMana: the classes whose bots drink.
func usesManaClass(class string) bool {
	switch class {
	case "warrior", "rogue":
		return false
	}
	return true
}

// supplies reads the drinks and the shot of the worn ranged weapon from the bags. short names what is under the wanted amounts.
func (g *gearRead) supplies(class, role string) (drinks int, ammo string, short []string) {
	if usesManaClass(class) {
		drinks = invCount(g.inv, drinkSet)
		need := preflightDrinksCaster
		if role == "healer" {
			need = preflightDrinksHealer
		}
		if drinks < need {
			short = append(short, fmt.Sprintf("%d drinks, %d wanted", drinks, need))
		}
	}
	if w, ok := g.inv.Equipped[tbm.SlotRanged]; ok && w != 0 {
		shot, set := "", map[uint32]bool(nil)
		switch it, known := g.info[w]; {
		case arrowWeaponSet[w]:
			shot, set = "arrows", arrowSet
		case bulletWeaponSet[w]:
			shot, set = "bullets", bulletSet
		case known && it.Found && it.Class == 2 && (it.SubClass == 2 || it.SubClass == 18):
			shot, set = "arrows", arrowSet
		case known && it.Found && it.Class == 2 && it.SubClass == 3:
			shot, set = "bullets", bulletSet
		}
		if set != nil {
			have := invCount(g.inv, set)
			ammo = fmt.Sprintf("%s %d", shot, have)
			if have < preflightAmmo {
				short = append(short, fmt.Sprintf("%d %s, %d wanted", have, shot, preflightAmmo))
			}
		}
	}
	return drinks, ammo, short
}

// maintainBot sends `.bot maintenance name` and reads the verdict.
func maintainBot(ctx context.Context, bl BotLiner, name string) string {
	verdict := ""
	lines, settled, err := bl.BotLines(ctx, "maintenance "+name, hireBotLineWait, func(line string) bool {
		v, done := maintenanceVerdict(line)
		if done {
			verdict = v
		}
		return done
	})
	switch {
	case err != nil:
		return "error: " + err.Error()
	case !settled:
		if len(lines) > 0 {
			return "no answer (" + lines[len(lines)-1] + ")"
		}
		return "no answer"
	}
	return verdict
}

// gearAndSupplies is the gear and supplies part of the check: read the bags and the worn items, fix gear with `.bot autogear <bot> blue`
// (an epic, an empty main slot, no shield on the tank, no ranged weapon on the hunter), top the supplies up with `.bot maintenance` (always
// for a kept hire, whose gear has worn since), and read again. What is still wrong fails.
func (f *hireFlow) gearAndSupplies(ctx context.Context, b *hireBot) {
	g, err := f.readGear(ctx, b.name)
	if err != nil {
		b.fail("inventory: %v", err)
		return
	}
	class, role := b.slot.Class, b.slot.Role
	_, _, epics, _ := g.summary(b.rb.Level)
	var gear []string
	for _, e := range epics {
		gear = append(gear, "epic "+e)
	}
	hard0, soft0 := splitMissing(g.missingGear(class, role))
	gear = append(append(gear, hard0...), soft0...)
	if len(gear) > 0 {
		if f.spec.NoFix {
			if len(gear) == len(soft0) { // only soft armor slots empty: not re-geared, a note
				b.warn("%s empty at level %d (no_fix: not re-geared)", strings.Join(soft0, ", "), b.rb.Level)
			} else {
				b.fail("gear: %s (no_fix: not re-geared)", strings.Join(gear, ", "))
			}
		} else if v := f.autogear(ctx, b.name); v != "regeared" {
			b.fail("gear: %s, and `.bot autogear %s blue` gave: %s", strings.Join(gear, ", "), b.name, v)
		} else {
			b.rb.Fixed = append(b.rb.Fixed, "autogear blue: "+strings.Join(gear, ", "))
			f.note("%s: autogear blue (%s)", b.name, strings.Join(gear, ", "))
			if g, err = f.readGear(ctx, b.name); err != nil {
				b.fail("inventory after autogear: %v", err)
				return
			}
			_, _, epics, _ = g.summary(b.rb.Level)
			var left []string
			for _, e := range epics {
				left = append(left, "epic "+e)
			}
			hard, soft := splitMissing(g.missingGear(class, role))
			left = append(left, hard...)
			if len(left) > 0 {
				b.fail("gear still wrong after autogear blue: %s", strings.Join(left, ", "))
			}
			if len(soft) > 0 {
				b.warn("%s empty after autogear at level %d", strings.Join(soft, ", "), b.rb.Level)
			}
		}
	}
	drinks, ammo, short := g.supplies(class, role)
	if b.reused || (len(short) > 0 && !f.spec.NoFix) {
		before := fmt.Sprintf("drinks %d", drinks)
		if ammo != "" {
			before += ", " + ammo
		}
		switch v := maintainBot(ctx, f.e.g, b.name); v {
		case "maintained":
			if g2, err := f.readGear(ctx, b.name); err != nil {
				b.fail("inventory after maintenance: %v", err)
				return
			} else {
				g = g2
			}
			drinks, ammo, short = g.supplies(class, role)
			after := fmt.Sprintf("drinks %d", drinks)
			if ammo != "" {
				after += ", " + ammo
			}
			if before == after {
				b.rb.Fixed = append(b.rb.Fixed, "maintenance: repaired, nothing to restock ("+after+")")
			} else {
				b.rb.Fixed = append(b.rb.Fixed, fmt.Sprintf("maintenance: %s -> %s", before, after))
			}
		case "disabled":
			b.warn("maintenance is off on the server (AiPlayerbot.OwnedBotMaintenanceEnabled = 0): supplies are as the hire came")
		default:
			b.warn("maintenance: %s", v)
		}
	}
	if len(short) > 0 {
		b.fail("supplies: %s", strings.Join(short, ", "))
	}
	b.rb.Drinks, b.rb.Ammo = drinks, ammo
	if role == "tank" && (class == "warrior" || class == "paladin") {
		has := g.hasShield()
		b.rb.Shield = &has
	}
	best2, ilvl, epics2, unknown := g.summary(b.rb.Level)
	b.rb.ItemLevel = float64(int(ilvl*10+0.5)) / 10
	if best2 >= 0 {
		b.rb.Quality = qualityName(best2)
	}
	if len(epics2) > 0 {
		b.fail("epic gear worn: %s", strings.Join(epics2, ", "))
	}
	if unknown > 0 {
		b.warn("the server described no prototype for %d worn items: their quality is not verified", unknown)
	}
}

// autogear sends `.bot autogear <bot> blue` and reads the verdict.
func (f *hireFlow) autogear(ctx context.Context, name string) string {
	verdict := ""
	lines, settled, err := f.e.g.BotLines(ctx, "autogear "+name+" blue", hireBotLineWait, func(line string) bool {
		v, done := autogearVerdict(line)
		if done {
			verdict = v
		}
		return done
	})
	switch {
	case err != nil:
		return "error: " + err.Error()
	case !settled:
		if len(lines) > 0 {
			return "no answer (" + lines[len(lines)-1] + ")"
		}
		return "no answer"
	}
	return verdict
}

// ---- spells ----

type spellReq struct {
	name string
	min  int // the level from which the class has it
}

// spellCheck is one `spells <filter>` question: the filter and the spells its answer must list (any one of them with any).
type spellCheck struct {
	filter string
	want   []spellReq
	any    bool
}

// keySpells are what a bot of the class and role must know at the level (TEST-BRIEF standard item 3; warrior Taunt / Sunder Armor /
// Defensive Stance, a priest's Resurrection, a hunter's pet spells, a warlock's summons). Nothing is learned for it: the hires come with
// their spells.
func keySpells(class, role string, level int) []spellCheck {
	var all []spellCheck
	switch class {
	case "warrior":
		stance := spellCheck{filter: "stance", want: []spellReq{{"Battle Stance", 1}}}
		if role == "tank" {
			stance.want = append(stance.want, spellReq{"Defensive Stance", 10})
			all = append(all, stance,
				spellCheck{filter: "taunt", want: []spellReq{{"Taunt", 10}}},
				spellCheck{filter: "sunder", want: []spellReq{{"Sunder Armor", 10}}},
				spellCheck{filter: "shield block", want: []spellReq{{"Shield Block", 12}}})
		} else {
			all = append(all, stance)
		}
	case "priest":
		all = append(all, spellCheck{filter: "resurrection", want: []spellReq{{"Resurrection", 10}}})
		if role == "healer" {
			all = append(all, spellCheck{filter: "renew", want: []spellReq{{"Renew", 8}}}, spellCheck{filter: "dispel", want: []spellReq{{"Dispel Magic", 18}}})
		}
	case "hunter":
		all = append(all, spellCheck{filter: "tame", want: []spellReq{{"Tame Beast", 10}}},
			spellCheck{filter: "pet", want: []spellReq{{"Call Pet", 10}, {"Mend Pet", 12}}})
	case "warlock":
		all = append(all, spellCheck{filter: "summon", want: []spellReq{{"Summon Imp", 1}, {"Summon Voidwalker", 10}}, any: true})
	case "mage":
		all = append(all, spellCheck{filter: "polymorph", want: []spellReq{{"Polymorph", 8}}})
	case "paladin":
		all = append(all, spellCheck{filter: "redemption", want: []spellReq{{"Redemption", 12}}})
	case "shaman":
		all = append(all, spellCheck{filter: "ancestral", want: []spellReq{{"Ancestral Spirit", 12}}})
	}
	var out []spellCheck
	for _, c := range all {
		var w []spellReq
		for _, r := range c.want {
			if level >= r.min {
				w = append(w, r)
			}
		}
		if len(w) > 0 {
			c.want = w
			out = append(out, c)
		}
	}
	return out
}

// spellsIn says which of the wanted spells a reply lists: a spell shows as "[Name]" in the bot's whisper.
func spellsIn(text string, want []spellReq) (found, missing []string) {
	low := strings.ToLower(text)
	for _, w := range want {
		if strings.Contains(low, "["+strings.ToLower(w.name)+"]") {
			found = append(found, w.name)
		} else {
			missing = append(missing, w.name)
		}
	}
	return found, missing
}

// spellsHeader is the first line of the bot's answer to `spells`: "=== Spells ===", then one spell per line, then "N more..." past 50
// (ListSpellsAction.cpp Execute). The header comes even when nothing matches the filter, so a header with no spell is an answer.
const spellsHeader = "=== Spells ==="

// checkSpells asks the bot's own spell list (`command <bot> spells <filter>`) for each key spell family and reads the whole burst of lines.
// A question the bot never answered (no header) fails the bot as "no answer", not as a missing spell; one it answered without the spell
// fails as lacking it.
func (f *hireFlow) checkSpells(ctx context.Context, b *hireBot, m HireMember) {
	var lacking, silent []string
	for _, c := range keySpells(b.slot.Class, b.slot.Role, m.Level) {
		lines, began, err := f.hg.BotBurst(ctx, "command "+b.name+" spells "+c.filter, hireSpellWait, hireSpellQuiet, func(l string) bool {
			return strings.Contains(l, spellsHeader)
		})
		if err != nil {
			b.fail("spells %s: %v", c.filter, err)
			return
		}
		if !began {
			silent = append(silent, c.filter)
			continue
		}
		found, missing := spellsIn(strings.Join(lines, "\n"), c.want)
		b.rb.Spells = append(b.rb.Spells, found...)
		if c.any && len(found) > 0 {
			continue
		}
		lacking = append(lacking, missing...)
	}
	if len(silent) > 0 {
		b.fail("no answer from %s to `spells %s` within %.0f s: its key spells are not confirmed", b.name, strings.Join(silent, "`, `spells "), hireSpellWait.Seconds())
	}
	if len(lacking) > 0 {
		b.fail("lacks %s (its own spell list does not show them; the step learns nothing, .learn is a GM command)", strings.Join(lacking, ", "))
	}
}

// ---- talents ----

// readTalents reads the bot's spent points (`command <bot> talents`); why says what went wrong when it could not.
func (f *hireFlow) readTalents(ctx context.Context, name string) (tbm.Talents, string) {
	var got tbm.Talents
	_, settled, err := f.e.g.BotLines(ctx, "command "+name+" talents", hireBotLineWait, func(line string) bool {
		t, ok := tbm.ParseTalents([]string{line})
		if ok {
			got = t
		}
		return ok
	})
	switch {
	case err != nil:
		return got, err.Error()
	case !settled:
		return got, "no answer"
	}
	return got, ""
}

func (f *hireFlow) checkTalents(ctx context.Context, b *hireBot) {
	t, why := f.readTalents(ctx, b.name)
	if why != "" {
		b.fail("talents: %s", why)
		return
	}
	b.rb.Talents = fmt.Sprintf("%d/%d/%d", t.Trees[0], t.Trees[1], t.Trees[2])
	role := talentRole(b.slot.Class, t.Trees)
	switch {
	case t.Total == 0:
		b.fail("no talent points spent (%s)", b.rb.Talents)
	case role == "feral":
		b.warn("feral talents %s: bear or cat is not told by the points", b.rb.Talents)
	case role != b.slot.Role:
		b.fail("talents %s are for %s, wanted %s", b.rb.Talents, role, b.slot.Role)
	}
}

// ---- pets ----

// checkPet waits, within hirePetWait, for the pet of a hunter or warlock to be out. A bot the agent does not see (its class is unknown)
// cannot be judged: a warning.
func (f *hireFlow) checkPet(ctx context.Context, b *hireBot, m HireMember) {
	if b.slot.Class != "hunter" && b.slot.Class != "warlock" {
		return
	}
	deadline := f.e.now().Add(hirePetWait)
	for {
		cur, ok := f.hg.Player(b.name)
		if !ok || cur.Class == "" {
			b.warn("not in the agent's view: its pet is not checked")
			return
		}
		if name, ok := f.hg.PetOf(cur.GUID); ok {
			b.rb.Pet = name
			return
		}
		if !f.e.now().Before(deadline) {
			b.fail("no pet out %.0f s after it showed", hirePetWait.Seconds())
			return
		}
		if err := f.e.sleep(ctx, time.Second); err != nil {
			return
		}
	}
}
