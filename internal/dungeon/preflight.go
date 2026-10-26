package dungeon

import (
	"context"
	"fmt"
	"regexp"
	"strings"
	"time"

	"github.com/pjerra/turtle-agent/internal/events"
	"github.com/pjerra/turtle-agent/internal/tbm"
)

// The pre-run kit check of a remote run (issue #83, remote Uldaman gap G4). Owned bots start with no drinks and no ammo: the run sends
// `.bot maintenance <bot>` for each bound bot (TortoiseBots refreshes consumables, reagents, ammo and repairs, when
// AiPlayerbot.OwnedBotMaintenanceEnabled = 1) and then reads `.bot inv <bot>` to see what the bags hold: drinks for the casters,
// ammo for the shot of a hunter's ranged weapon, a shield on the tank. It also reads each bot's spent talent points (`.bot command <bot>
// talents`): a reset 0/0/0 bot is a warning. The result is the report's `preflight`; what is missing is a
// warning, and fails the run only with RemoteOptions.Preflight "strict".

// Values of RemoteOptions.Preflight.
const (
	PreflightWarn   = "warn"   // the default: check, report, go on
	PreflightStrict = "strict" // a warning fails the run before the first step
	PreflightOff    = "off"    // send nothing
)

// What the check wants in the bags.
const (
	preflightDrinksHealer = 20  // drinks for the named healer
	preflightDrinksCaster = 10  // drinks for any other bot that uses mana
	preflightAmmo         = 100 // shots of the kind the ranged weapon takes
)

// BotLiner is the optional part of a RemoteGame that sends a plain `.bot` line (maintenance, inv, logout, `command <bot> talents`: not a
// commander order) and returns the lines the server (or the bot) answered with. done says which line ends the reply; settled is false when none did in time.
type BotLiner interface {
	BotLines(ctx context.Context, command string, wait time.Duration, done func(line string) bool) (lines []string, settled bool, err error)
}

// BotLineRequest is one command of BotLinesAll.
type BotLineRequest struct {
	Command string
	Done    func(line string) bool
}

// BotLinesAller is the optional part of a BotLiner that sends several `.bot` lines at once and reads all their answers in one wait: the
// preflight's talents check asks every bot together (the answers of a bot that does not reply cost one wait for the party, not one each).
// It returns, per request in order, the lines read and whether Done ended them.
type BotLinesAller interface {
	BotLinesAll(ctx context.Context, reqs []BotLineRequest, wait time.Duration) (lines [][]string, settled []bool, err error)
}

// preflightTalentsWait is how long the talents check waits for the whole party's answers together. A bot that answers does so within a
// second (the 2026-10-15 ZF traces: 40-80 ms); one that has not by now is not going to (the same query waited 6 s per bot, one after
// another, and ran 31 s of nothing at every start).
const preflightTalentsWait = 5 * time.Second

// PreflightBot is one bot's part of the check.
type PreflightBot struct {
	Name string `json:"name"`
	// Maintenance is what `.bot maintenance` came to: "maintained", "disabled" (AiPlayerbot.OwnedBotMaintenanceEnabled is 0),
	// "refused: ...", "no answer", or "not sent" (after a "disabled").
	Maintenance string `json:"maintenance"`
	Role        string `json:"role,omitempty"`   // "tank", "healer" or "" (the roles the run names)
	Drinks      int    `json:"drinks,omitempty"` // drinks the bags hold (known entries)
	Ammo        string `json:"ammo,omitempty"`   // a ranged weapon's shot: "arrows 200", "bullets 0", ...
	Shield      *bool  `json:"shield,omitempty"` // checked for the tank only
	// Talents is the points spent per tree from `.bot command <bot> talents` ("0/5/31"), or "no answer". Total 0 is a reset bot.
	Talents   string `json:"talents,omitempty"`
	Inventory string `json:"inventory,omitempty"`
}

// Preflight is the result of the kit check.
type Preflight struct {
	Mode     string         `json:"mode"` // warn or strict
	Bots     []PreflightBot `json:"bots"`
	Warnings []string       `json:"warnings"`
	OK       bool           `json:"ok"`
	Seconds  float64        `json:"seconds"` // what the check took
}

func (p *Preflight) clone() *Preflight {
	if p == nil {
		return nil
	}
	c := *p
	c.Bots = append([]PreflightBot(nil), p.Bots...)
	c.Warnings = append([]string(nil), p.Warnings...)
	return &c
}

var (
	drinkSet        = entrySet(drinkEntries)
	arrowSet        = entrySet(arrowEntries)
	bulletSet       = entrySet(bulletEntries)
	shieldSet       = entrySet(shieldEntries)
	arrowWeaponSet  = entrySet(arrowWeaponEntries)
	bulletWeaponSet = entrySet(bulletWeaponEntries)
)

func entrySet(l []uint32) map[uint32]bool {
	m := make(map[uint32]bool, len(l))
	for _, e := range l {
		m[e] = true
	}
	return m
}

// countOf sums the bot's items that are in set.
func invCount(inv tbm.Inventory, set map[uint32]bool) int {
	n := 0
	for e, c := range inv.Counts {
		if set[e] {
			n += c
		}
	}
	return n
}

// maintenanceVerdict reads one reply line of `.bot maintenance <bot>`; done is true for a line that ends the reply.
func maintenanceVerdict(line string) (verdict string, done bool) {
	l := strings.TrimSpace(line)
	low := strings.ToLower(l)
	switch {
	case strings.HasPrefix(low, "maintained "):
		return "maintained", true
	case strings.Contains(low, "maintenance is disabled"):
		return "disabled", true
	case strings.Contains(low, "pool bot"):
		return "refused: pool bot", true
	case strings.Contains(low, "must be alive"):
		return "refused: the bot is dead", true
	case strings.HasPrefix(low, "usage:"):
		return "refused: not an online bot of this account", true
	}
	return "", false
}

// preflight runs the kit check against the bots of the first frame. A game that cannot send bot lines skips it with a note.
func (d *RemoteDriver) preflight(ctx context.Context) error {
	mode := d.opt.Preflight
	if mode == PreflightOff {
		return nil
	}
	bl, ok := d.g.(BotLiner)
	if !ok {
		d.rep.note("preflight: skipped, this game cannot send .bot maintenance or .bot inv")
		return nil
	}
	began := d.clk.Now()
	pf := &Preflight{Mode: mode, OK: true}
	maintenanceOn := d.preflightMaintains()
	maintenanceNote := "not sent"
	if !maintenanceOn {
		// An observed run's preflight, by default: `.bot maintenance` walks each bot to a vendor, and hires autogeared a moment ago
		// scatter over the maps, which pauses the run leader ("the lead bot is not on the plan's map", #253). The inventory and
		// talent reads below still run; observe.maintenance brings maintenance back.
		maintenanceNote = "not sent (observe mode: observe.maintenance is off)"
		d.rep.note("preflight: .bot maintenance skipped (observe mode: off by default, it scatters the party to vendors); pass observe.maintenance to send it")
	}
	// Before the maintenance orders, not after: `.bot maintenance` walks the hires to vendors, they leave the object store, and the faction
	// check would read no race and skip its warning (#266). The check needs only the first frame's party and what is in view now.
	d.checkPartyStrength(pf)
	for _, m := range d.fr.Party {
		b := PreflightBot{Name: m.Name, Maintenance: maintenanceNote}
		if maintenanceOn {
			b.Maintenance = d.maintain(ctx, bl, m.Name)
			if b.Maintenance == "disabled" {
				maintenanceOn = false
				d.kit.off = true // the rest gates do not try it again
				pf.Warnings = append(pf.Warnings, "maintenance is off on the server (AiPlayerbot.OwnedBotMaintenanceEnabled = 0): drinks and ammo are as the bots have them")
			} else if b.Maintenance != "maintained" {
				pf.Warnings = append(pf.Warnings, fmt.Sprintf("%s: maintenance: %s", m.Name, b.Maintenance))
			}
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		pf.Bots = append(pf.Bots, b)
	}
	d.checkAllTalents(ctx, bl, pf)
	for i, m := range d.fr.Party {
		d.checkKit(ctx, bl, pf, &pf.Bots[i], m)
		if err := ctx.Err(); err != nil {
			return err
		}
	}
	pf.OK = len(pf.Warnings) == 0
	pf.Seconds = d.clk.Now().Sub(began).Seconds()
	d.remote(func(r *RemoteReport) { r.Preflight = pf.clone() })
	text := "preflight: ok"
	if !pf.OK {
		text = "preflight: " + strings.Join(pf.Warnings, "; ")
	}
	d.rep.note(text)
	d.emit(events.KindDungeonStep, text, map[string]any{"action": "preflight", "ok": pf.OK, "warnings": pf.Warnings, "mode": "remote"})
	if !pf.OK && mode == PreflightStrict {
		return fmt.Errorf("preflight (strict): %s", strings.Join(pf.Warnings, "; "))
	}
	return nil
}

// preflightMaintains says whether the preflight sends `.bot maintenance`: a driven run always does; an observed run only with
// observe.maintenance (Off by default: the run leader's party must not wander off to vendors, #253).
func (d *RemoteDriver) preflightMaintains() bool {
	if d.observing() {
		return d.opt.Observe.withDefaults(d.plan).Maintenance
	}
	return true
}

// maintain sends `.bot maintenance name` and reads the verdict.
func (d *RemoteDriver) maintain(ctx context.Context, bl BotLiner, name string) string {
	verdict := ""
	lines, settled, err := bl.BotLines(ctx, "maintenance "+name, 6*time.Second, func(line string) bool {
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

// talentsRe reads a "a/b/c" talents string (the hire report's and memory's form).
var treesRe = regexp.MustCompile(`^(\d+)/(\d+)/(\d+)$`)

// parseTrees reads "a/b/c" spent points; ok is false for anything else ("no answer", "error: ...").
func parseTrees(s string) (tbm.Talents, bool) {
	m := treesRe.FindStringSubmatch(s)
	if m == nil {
		return tbm.Talents{}, false
	}
	var t tbm.Talents
	for i := range t.Trees {
		n := 0
		for _, c := range m[i+1] {
			n = n*10 + int(c-'0')
		}
		t.Trees[i], t.Total = n, t.Total+n
	}
	return t, true
}

// knownTalents are a bot's spent points when the hire already read them: first the ready block's entry for a name
// matching case-insensitively (a hire of this run), else the hire memory of a kept binding (name in lower case). An
// unknown ready entry ("no answer", malformed, 0/0/0) falls through to the memory; unknown in both places, the bot is
// asked as before.
func knownTalents(ready *ReadyReport, memory map[string]tbm.Who, name string) (string, bool) {
	if ready != nil {
		for _, b := range ready.Bots {
			if !strings.EqualFold(b.Name, name) {
				continue
			}
			if t, ok := parseTrees(b.Talents); ok && t.Total > 0 {
				return b.Talents, true
			}
			break
		}
	}
	if w, ok := memory[strings.ToLower(name)]; ok {
		if s := w.Trees[0] + w.Trees[1] + w.Trees[2]; s > 0 {
			return fmt.Sprintf("%d/%d/%d", w.Trees[0], w.Trees[1], w.Trees[2]), true
		}
	}
	return "", false
}

// hireTalents fills in the talents the hire already read: for every bot with known talents it sets "a/b/c (hire)" and
// runs the zero-points rule, and it says which bots still need asking. It sends no request itself.
func (d *RemoteDriver) hireTalents(pf *Preflight) (ask []int) {
	ready := d.rep.snapshot().Ready
	for i, m := range d.fr.Party {
		t, ok := knownTalents(ready, d.opt.HireMemory, m.Name)
		if !ok {
			ask = append(ask, i)
			continue
		}
		b := &pf.Bots[i]
		b.Talents = t + " (hire)"
		if got, ok := parseTrees(t); ok && got.Total == 0 {
			pf.Warnings = append(pf.Warnings, fmt.Sprintf("%s: no talent points spent (%s): a reset spec?", m.Name, b.Talents))
		}
	}
	return ask
}

// checkAllTalents fills in the talents the hire already read and asks the rest at once, in one wait of
// preflightTalentsWait. A game that cannot send several lines at once is asked bot by bot, as before. Bots with no
// known talents keep today's ask and today's "no answer" warning.
func (d *RemoteDriver) checkAllTalents(ctx context.Context, bl BotLiner, pf *Preflight) {
	ask := d.hireTalents(pf)
	if len(ask) == 0 {
		return
	}
	multi, ok := bl.(BotLinesAller)
	if !ok {
		for _, i := range ask {
			d.checkTalents(ctx, bl, pf, &pf.Bots[i], d.fr.Party[i])
		}
		return
	}
	got := make([]tbm.Talents, len(ask))
	reqs := make([]BotLineRequest, len(ask))
	for j, i := range ask {
		m := d.fr.Party[i]
		reqs[j] = BotLineRequest{Command: "command " + m.Name + " talents", Done: func(line string) bool {
			t, ok := tbm.ParseTalents([]string{line})
			if ok {
				got[j] = t
			}
			return ok
		}}
	}
	_, settled, err := multi.BotLinesAll(ctx, reqs, preflightTalentsWait)
	for j, i := range ask {
		m, b := d.fr.Party[i], &pf.Bots[i]
		switch {
		case err != nil:
			b.Talents = "error: " + err.Error()
		case j >= len(settled) || !settled[j]:
			b.Talents = "no answer"
		default:
			d.talentsVerdict(pf, b, m, got[j])
			continue
		}
		pf.Warnings = append(pf.Warnings, fmt.Sprintf("%s: talents: %s", m.Name, b.Talents))
	}
}

// talentsVerdict records the points an answer gave and flags a bot with none spent.
func (d *RemoteDriver) talentsVerdict(pf *Preflight, b *PreflightBot, m tbm.PartyRec, got tbm.Talents) {
	b.Talents = fmt.Sprintf("%d/%d/%d", got.Trees[0], got.Trees[1], got.Trees[2])
	if got.Total == 0 {
		pf.Warnings = append(pf.Warnings, fmt.Sprintf("%s: no talent points spent (%s): a reset spec?", m.Name, b.Talents))
	}
}

// checkTalents reads the bot's spent talent points (`.bot command <bot> talents`, whose answer is the bot's whisper "My current talent
// spec is: ... (a/b/c)") and flags a bot with none spent: a respec that was reset and never applied again leaves a 0/0/0 tank.
func (d *RemoteDriver) checkTalents(ctx context.Context, bl BotLiner, pf *Preflight, b *PreflightBot, m tbm.PartyRec) {
	var got tbm.Talents
	_, settled, err := bl.BotLines(ctx, "command "+m.Name+" talents", 6*time.Second, func(line string) bool {
		t, ok := tbm.ParseTalents([]string{line})
		if ok {
			got = t
		}
		return ok
	})
	switch {
	case err != nil:
		b.Talents = "error: " + err.Error()
	case !settled:
		b.Talents = "no answer"
	default:
		d.talentsVerdict(pf, b, m, got)
		return
	}
	pf.Warnings = append(pf.Warnings, fmt.Sprintf("%s: talents: %s", m.Name, b.Talents))
}

// checkKit reads the bot's bags and judges them for its role.
func (d *RemoteDriver) checkKit(ctx context.Context, bl BotLiner, pf *Preflight, b *PreflightBot, m tbm.PartyRec) {
	lines, settled, err := bl.BotLines(ctx, "inv "+m.Name, 8*time.Second, func(line string) bool {
		return strings.HasPrefix(line, "TBM:INV_END|") || strings.HasPrefix(line, "TBM:ACTION_ERR|inv|")
	})
	inv := tbm.ParseInventory(lines)
	switch {
	case err != nil:
		b.Inventory = "error: " + err.Error()
	case inv.Err != nil:
		b.Inventory = "refused: " + inv.Err.Code
	case !settled || !inv.Complete:
		b.Inventory = "no answer"
	default:
		b.Inventory = "read"
	}
	if b.Inventory != "read" {
		pf.Warnings = append(pf.Warnings, fmt.Sprintf("%s: inventory: %s", m.Name, b.Inventory))
		return
	}
	named := func(n string) bool { return n != "" && strings.EqualFold(n, m.Name) }
	switch {
	case named(d.opt.Tank):
		b.Role = "tank"
	case named(d.opt.Healer):
		b.Role = "healer"
	}
	// Drinks for whoever uses mana: the named healer needs more.
	if m.UsesMana() {
		b.Drinks = invCount(inv, drinkSet)
		d.kit.set(m.Name, b.Drinks, -1)
		need := preflightDrinksCaster
		if b.Role == "healer" {
			need = preflightDrinksHealer
		}
		if b.Drinks < need {
			pf.Warnings = append(pf.Warnings, fmt.Sprintf("%s: %d drinks in the bags, %d wanted", m.Name, b.Drinks, need))
		}
	}
	// Ammo for the shot of the ranged weapon it wears.
	if w, ok := inv.Equipped[tbm.SlotRanged]; ok {
		shot, set, have := "", map[uint32]bool(nil), 0
		switch {
		case arrowWeaponSet[w]:
			shot, set = "arrows", arrowSet
		case bulletWeaponSet[w]:
			shot, set = "bullets", bulletSet
		}
		if set != nil {
			have = invCount(inv, set)
			b.Ammo = fmt.Sprintf("%s %d", shot, have)
			if have < preflightAmmo {
				otherSet := bulletSet
				if shot == "bullets" {
					otherSet = arrowSet
				}
				other := invCount(inv, otherSet)
				switch {
				case have == 0 && other > 0:
					pf.Warnings = append(pf.Warnings, fmt.Sprintf("%s: its ranged weapon takes %s and the bags hold the other kind (%d)", m.Name, shot, other))
				case have == 0:
					pf.Warnings = append(pf.Warnings, fmt.Sprintf("%s: its ranged weapon takes %s and the bags hold none", m.Name, shot))
				default:
					pf.Warnings = append(pf.Warnings, fmt.Sprintf("%s: %d %s, %d wanted", m.Name, have, shot, preflightAmmo))
				}
			}
		}
	}
	// A shield on the tank.
	if b.Role == "tank" {
		has := shieldSet[inv.Equipped[tbm.SlotOffHand]]
		b.Shield = &has
		if !has {
			pf.Warnings = append(pf.Warnings, fmt.Sprintf("%s: the tank wears no shield", m.Name))
		}
	}
}

// endStay parks the bots before the binding ends: `cmd stay`, so that they do not follow the far-away agent once the commander lets go.
// It runs with its own context (the run's may be cancelled) and records the answer.
func (d *RemoteDriver) endStay(ctx context.Context) {
	r, err := d.g.Command(ctx, "cmd stay")
	via := ""
	switch {
	case err != nil:
		via = "error: " + err.Error()
	case r.Kind == tbm.KindAck:
		via = "ack"
	case r.Kind == tbm.KindErr && r.Err != nil:
		via = "refused: " + r.Err.Code
	default:
		via = "no answer"
	}
	d.remote(func(rr *RemoteReport) {
		rr.OrdersSent++
		rr.Orders["stay"]++
		rr.EndStay = via
	})
	d.rep.note("end of run: cmd stay: " + via)
}

// endLogout logs the bots out (`.bot logout name`) after the binding has ended, and records which went.
func (d *RemoteDriver) endLogout(ctx context.Context) {
	bl, ok := d.g.(BotLiner)
	if !ok {
		d.rep.note("end of run: on_end logout skipped, this game cannot send .bot logout")
		return
	}
	var gone []string
	for _, name := range d.partyNames() {
		var verdict string
		_, settled, err := bl.BotLines(ctx, "logout "+name, 4*time.Second, func(line string) bool {
			low := strings.ToLower(line)
			switch {
			case strings.Contains(low, "logout requested") || strings.Contains(low, "hire ended"):
				verdict = "ok"
			case strings.Contains(low, "could not be logged out") || strings.Contains(low, "not found") || strings.HasPrefix(low, "usage:") ||
				strings.Contains(low, "may only control") || strings.Contains(low, "has no ai"):
				verdict = "refused: " + line
			default:
				return false
			}
			return true
		})
		switch {
		case err != nil:
			d.rep.note(fmt.Sprintf("end of run: logout %s: %v", name, err))
		case !settled:
			d.rep.note(fmt.Sprintf("end of run: logout %s: no answer", name))
		case verdict == "ok":
			gone = append(gone, name)
		default:
			d.rep.note(fmt.Sprintf("end of run: logout %s: %s", name, verdict))
		}
	}
	d.remote(func(rr *RemoteReport) { rr.LoggedOut = gone })
	d.rep.note(fmt.Sprintf("end of run: logged out %v", gone))
}

// partyNames are the bots of the run: the party of the newest frame, else the names the run was given.
func (d *RemoteDriver) partyNames() []string {
	var out []string
	for _, p := range d.fr.Party {
		out = append(out, p.Name)
	}
	if len(out) == 0 {
		out = append([]string{d.opt.Lead}, d.opt.Bots...)
	}
	return out
}
