package dungeon

import (
	"context"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/pjerra/turtle-agent/internal/tbm"
)

// The raid hire flow: `prepare.party "hire"` on a raid plan (step 2 of the
// observe-mode raid run; the server's run leader leads the bound raid, the
// agent hires, gears and binds). Every step sends a `.bot` line through
// BotBurst/the commander command path the 5-man flow uses:
//
//  1. the commander must be 60 (else `the commander must be level 60 for raid hires (GM .levelup)`),
//  2. old state (readBoundParty; commander unbind when bound; every current hire dismissed when rehire,
//     else kept only when the count by role equals the plan's split exactly, otherwise dismissed all),
//  3. `.bot hire raid <tanks>t <healers>h <dps>d t<tier>` with the plan's counts and the tier (HireSpec.Tier,
//     else the preset's tier, else 2); waits for `TBM:HIRE_RAID|ok|...` (partial: fail with the line) and then
//     until Members() holds `size` online bots (poll 2 s, ceiling 120 s + 3 s per bot),
//  4. the lead: for each member whose class is warrior, paladin or druid, in group order, read talents with
//     readWho and talentRole; the first that reads tank is the lead bot (at most 6 reads), else `no tank in the raid`,
//  5. `.bot gear raid <AgentName> preset <p>` (HireSpec.GearPreset, else the plan name's preset); waits for `TBM:GEAR|`
//     (ceiling 90 s); none fails the step, partial is a Ready warning,
//  6. no `.bot maintenance` is sent (40 lines); supplies come from the hire,
//  7. `.bot commander bind <Lead> raid`; waits for the ack and `TBM:RAID_LAYOUT|`; then commanderOutside,
//  8. one ReadyBot per member (name, class, role from the layout line when parsed, else talents for tanks/healers
//     and dps for the rest, level, item level when ItemInfos reads it within hireStepCeiling, else 0), Ready when 7 acked,
//  9. e.hired = &HiredParty{Lead: lead, Bots: others}; the observe run then starts `.bot run start <plan>`.
//
// The flow shares nothing with the 5-man hireFlow: a raid plan has a raid block instead of hire_composition, so
// slots() is not used for raids (plan.go refuses hire_composition on raid plans).

// raidTierCeiling bounds the raid hire's polls: 120 s + 3 s per bot.
const (
	raidHirePoll     = 2 * time.Second
	raidHireBaseWait = 120 * time.Second
	raidHirePerBot   = 3 * time.Second
	raidLeadReads    = 6 // the first tank-read member of a tank class leads (talents reads)
	raidGearWait     = 90 * time.Second
)

// raidPresetTier is the tier the gear presets carry: the `.bot gear raid` t<tier> fallback when HireSpec.Tier is 0.
var raidPresetTier = map[string]int{
	"lkh": 2, "zg": 2, "aq20": 2, "prebis": 2,
	"t1": 3, "timbermaw": 3, "onyxia": 3, "mc": 3,
	"t2": 4, "bwl": 4, "es": 4,
	"aq40": 5, "naxx": 6, "kara40": 7,
}

// raidPresetOf is the gear preset of the plan's name: lower_karazhan_halls->lkh, zul_gurub->zg,
// ruins_of_ahnqiraj->aq20, timbermaw_hold->timbermaw, onyxias_lair->onyxia, molten_core->mc,
// blackwing_lair->bwl, emerald_sanctum->es, temple_of_ahnqiraj->aq40, naxxramas->naxx,
// upper_karazhan_halls->kara40. Unknown names: mc.
func raidPresetOf(plan string) string {
	switch strings.ToLower(plan) {
	case "lower_karazhan_halls":
		return "lkh"
	case "zul_gurub":
		return "zg"
	case "ruins_of_ahnqiraj":
		return "aq20"
	case "timbermaw_hold":
		return "timbermaw"
	case "onyxias_lair":
		return "onyxia"
	case "molten_core":
		return "mc"
	case "blackwing_lair":
		return "bwl"
	case "emerald_sanctum":
		return "es"
	case "temple_of_ahnqiraj":
		return "aq40"
	case "naxxramas":
		return "naxx"
	case "upper_karazhan_halls":
		return "kara40"
	}
	return "mc"
}

// raidHire is the prepare step `party: hire` on a raid plan. It dispatches from (e *prepEnv) hire by the plan's kind.
func (e *prepEnv) raidHire(ctx context.Context) (string, error) {
	hg, ok := e.g.(HireGame)
	if !ok {
		return "", fmt.Errorf("this game cannot hire: it has no group view or item queries")
	}
	if e.command == nil {
		return "", fmt.Errorf("this game cannot send commander commands (the bind of the raid)")
	}
	if hg.AgentLevel() != 60 {
		return "", fmt.Errorf("the commander must be level 60 for raid hires (GM .levelup)")
	}
	if e.plan == nil || e.plan.Raid == nil {
		return "", fmt.Errorf("raid hire needs a raid plan with a raid block")
	}
	raid := *e.plan.Raid
	f := &raidHireFlow{e: e, hg: hg, raid: raid, began: e.now()}
	if err := f.over(); err != nil {
		return "", err
	}
	f.rdy = ReadyReport{Party: PartyHire, Level: 60}
	defer func() { // the partial block stays in the report when the step fails
		f.rdy.Seconds = e.now().Sub(f.began).Seconds()
		e.rep.Ready = f.rdy.clone()
	}()
	return f.run(ctx)
}

type raidHireFlow struct {
	e     *prepEnv
	hg    HireGame
	raid  RaidSpec
	tier  int
	gear  string
	lead  string
	began time.Time
	rdy   ReadyReport
}

func (f *raidHireFlow) over() error {
	if f.e.now().Sub(f.began) > hireStepCeiling {
		return fmt.Errorf("the hire step ran over its %.0f s ceiling", hireStepCeiling.Seconds())
	}
	return nil
}

func (f *raidHireFlow) note(format string, a ...any) {
	f.e.note("prepare hire: " + fmt.Sprintf(format, a...))
}

// specTier is the `.bot hire raid` tier: HireSpec.Tier, else the preset's tier, else 2.
func (f *raidHireFlow) specTier() int {
	if f.e.spec.Hire != nil && f.e.spec.Hire.Tier > 0 {
		return f.e.spec.Hire.Tier
	}
	if t, ok := raidPresetTier[f.gear]; ok {
		return t
	}
	return 2
}

func (f *raidHireFlow) run(ctx context.Context) (string, error) {
	e := f.e
	if e.spec.Hire != nil {
		f.gear = e.spec.Hire.GearPreset
	}
	if f.gear == "" && e.plan != nil {
		f.gear = raidPresetOf(e.plan.Name)
	}
	f.tier = f.specTier()

	// 2. Old state: the bound party, the current hires (dismissed on rehire, else kept only when the role counts match).
	bound, err := readBoundParty(ctx, e.command)
	if err != nil {
		return "", err
	}
	if bound.Answered {
		if err := e.unbindCommander(ctx); err != nil {
			return "", err
		}
	}
	dismissed, kept, err := f.reconcile(ctx)
	if err != nil {
		return "", err
	}
	for _, n := range dismissed {
		if err := f.over(); err != nil {
			return "", err
		}
		if err := f.dismissOne(ctx, n); err != nil {
			return "", err
		}
		f.rdy.Dismissed = append(f.rdy.Dismissed, n)
	}
	f.note("ended the old binding and dismissed %d hires", len(dismissed))

	// 3. Hire the raid.
	tanks, healers, dps := f.raid.Tanks, f.raid.Healers, f.raid.DPS
	line := fmt.Sprintf("hire raid %dt %dh %dd t%d", tanks, healers, dps, f.tier)
	if err := f.over(); err != nil {
		return "", err
	}
	got, settled, err := f.hg.BotBurst(ctx, line, hireReplyWait, raidHirePoll, func(l string) bool {
		return strings.HasPrefix(l, "TBM:HIRE_RAID|")
	})
	switch {
	case err != nil:
		return "", fmt.Errorf(".bot %s: %w", line, err)
	case !settled:
		return "", fmt.Errorf(".bot %s: no answer in %.0f s", line, hireReplyWait.Seconds())
	}
	fields := strings.Split(got[0], "|")
	switch {
	case len(fields) < 1:
		return "", fmt.Errorf(".bot %s refused: %s", line, got[0])
	case fields[1] != "ok":
		return "", fmt.Errorf(".bot %s: %s", line, got[0])
	}
	f.note("hired the raid: %s", got[0])
	if err := f.awaitMembers(ctx); err != nil {
		return "", err
	}
	for _, n := range kept {
		f.rdy.Reused = append(f.rdy.Reused, n)
	}

	// 4. The lead: the first tank-read member of a tank class, in group order.
	lead, err := f.pickLead(ctx)
	if err != nil {
		return "", err
	}
	f.lead = lead

	// 5. Gear.
	gearLine := fmt.Sprintf("gear raid %s preset %s", e.g.AgentName(), f.gear)
	got, settled, err = f.hg.BotBurst(ctx, gearLine, raidGearWait, raidHirePoll, func(l string) bool {
		return strings.HasPrefix(l, "TBM:GEAR|")
	})
	switch {
	case err != nil:
		return "", fmt.Errorf(".bot %s: %w", gearLine, err)
	case !settled:
		return "", fmt.Errorf(".bot %s: no answer in %.0f s", gearLine, raidGearWait.Seconds())
	}
	switch fields := strings.Split(got[0], "|"); {
	case len(fields) < 2:
		return "", fmt.Errorf(".bot %s: %s", gearLine, got[0])
	case fields[1] == "none":
		return "", fmt.Errorf(".bot %s: %s", gearLine, got[0])
	case fields[1] == "partial":
		f.rdy.Warnings = append(f.rdy.Warnings, "gear partial (the step geared only some bots): "+got[0])
		f.note("gear partial: %s", got[0])
	default:
		f.note("geared the raid: %s", got[0])
	}

	// 7. Bind.
	bindLine := fmt.Sprintf("commander bind %s raid", f.lead)
	r, err := e.command(ctx, bindLine)
	if err != nil {
		return "", fmt.Errorf("commander bind %s: %w", f.lead, err)
	}
	if r.Kind == tbm.KindErr {
		return "", fmt.Errorf("commander bind %s refused: %s", f.lead, describeBindErr(r.Err))
	}
	if r.Kind != tbm.KindAck {
		return "", fmt.Errorf("commander bind %s: no ACK or ERR came", f.lead)
	}
	layout := raidLayoutOf(r.Lines)
	if layout == "" {
		return "", fmt.Errorf("commander bind %s: bound but no TBM:RAID_LAYOUT line came", f.lead)
	}
	f.note("bound the raid of %s (%d bots): %s", f.lead, f.raid.Size, layout)
	if err := f.commanderOutside(ctx); err != nil {
		return "", err
	}

	// 8. Ready block.
	f.ready(ctx, layout)

	// 9. The run takes the hire from here.
	others := []string{}
	for _, b := range f.rdy.Bots {
		if b.Name != f.lead {
			others = append(others, b.Name)
		}
	}
	e.hired = &HiredParty{Lead: f.lead, Bots: others, Tank: f.lead, Healer: f.rdy.healer()}
	f.rdy.Ready = true
	return fmt.Sprintf("%d raid hires at level 60 bound to %s (gear preset %s, tier %d), commander outside the group", len(f.rdy.Bots), f.lead, f.gear, f.tier), nil
}

// reconcile reads the current hires and decides what to dismiss. On rehire every hire goes. Otherwise the ones that
// match are kept only when the count by role equals the plan's split exactly (dead-or-alive, at any level: they are
// re-geared, and a raid hire is 60); anything else is dismissed.
func (f *raidHireFlow) reconcile(ctx context.Context) (dismissed, kept []string, err error) {
	var held []HireMember
	for _, m := range f.hg.Members() {
		held = append(held, m)
	}
	names := func(ms []HireMember) []string {
		var out []string
		for _, m := range ms {
			out = append(out, m.Name)
		}
		return out
	}
	if f.e.spec.Hire != nil && f.e.spec.Hire.Rehire {
		return names(held), nil, nil
	}
	matched := map[string]bool{}
	counts := map[string]int{}
	for _, m := range held {
		w, why := f.readWho(ctx, m.Name)
		if why != "" {
			return nil, nil, fmt.Errorf("no answer from the commander's hire %s to `who`: %s", m.Name, why)
		}
		role := raidRoleOf(w)
		if role == "" || matched[m.Name] {
			continue
		}
		counts[role]++
		matched[m.Name] = true
	}
	want := map[string]int{"tank": f.raid.Tanks, "healer": f.raid.Healers, "dps": f.raid.DPS}
	if counts["tank"] == want["tank"] && counts["healer"] == want["healer"] && counts["dps"] == want["dps"] {
		return nil, names(held), nil
	}
	return names(held), nil, nil
}

// readWho asks the bot who it is (`command <bot> who`): class, level and the points in each tree.
func (f *raidHireFlow) readWho(ctx context.Context, name string) (tbm.Who, string) {
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
	return got, ""
}

// dismissOne ends one hire: `.bot logout <name>` ends it and deletes the character, then it leaves the group.
func (f *raidHireFlow) dismissOne(ctx context.Context, name string) error {
	var verdict string
	_, settled, err := f.e.g.BotLines(ctx, "logout "+name, 4*time.Second, func(line string) bool {
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
		return fmt.Errorf("dismissing %s: %w", name, err)
	case !settled:
		return fmt.Errorf("dismissing %s: no answer", name)
	case verdict != "ok":
		return fmt.Errorf("dismissing %s: %s", name, verdict)
	}
	deadline := f.e.now().Add(hireDismissWait)
	for {
		gone := true
		for _, m := range f.hg.Members() {
			if strings.EqualFold(m.Name, name) {
				gone = false
			}
		}
		if gone {
			return nil
		}
		if !f.e.now().Before(deadline) {
			return fmt.Errorf("dismissing %s: still in the group %.0f s later", name, hireDismissWait.Seconds())
		}
		if err := f.e.sleep(ctx, time.Second); err != nil {
			return err
		}
	}
}

// awaitMembers waits until the commander's group holds `size` online bots: poll 2 s, ceiling 120 s + 3 s per bot.
func (f *raidHireFlow) awaitMembers(ctx context.Context) error {
	deadline := f.e.now().Add(raidHireBaseWait + time.Duration(f.raid.Size)*raidHirePerBot)
	for {
		if err := f.over(); err != nil {
			return err
		}
		n := 0
		for _, m := range f.hg.Members() {
			if m.Online && m.Level > 0 {
				n++
			}
		}
		if n >= f.raid.Size {
			return nil
		}
		if !f.e.now().Before(deadline) {
			return fmt.Errorf("the raid has %d of %d bots %.0f s after the hire: not all hires arrived", n, f.raid.Size, (raidHireBaseWait + time.Duration(f.raid.Size)*raidHirePerBot).Seconds())
		}
		if err := f.e.sleep(ctx, raidHirePoll); err != nil {
			return err
		}
	}
}

// pickLead reads the talents of the members of a tank class, in group order, and takes the first that reads tank.
// At most 6 reads; none: `no tank in the raid`.
func (f *raidHireFlow) pickLead(ctx context.Context) (string, error) {
	reads := 0
	for _, m := range f.hg.Members() {
		if reads >= raidLeadReads {
			break
		}
		w, why := f.readWho(ctx, m.Name)
		if why != "" {
			return "", fmt.Errorf("no answer from %s to `who`: %s", m.Name, why)
		}
		switch strings.ToLower(w.Class) {
		case "warrior", "paladin", "druid":
		default:
			continue
		}
		t, why := f.readTalents(ctx, m.Name)
		reads++
		if why != "" {
			return "", fmt.Errorf("no answer from %s to `talents`: %s", m.Name, why)
		}
		if talentRole(strings.ToLower(w.Class), t.Trees) == "tank" {
			return m.Name, nil
		}
		if reads >= raidLeadReads {
			break
		}
	}
	return "", fmt.Errorf("no tank in the raid")
}

// readTalents reads the bot's spent points (`command <bot> talents`).
func (f *raidHireFlow) readTalents(ctx context.Context, name string) (tbm.Talents, string) {
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

// commanderOutside waits for the commander to be out of every group after the bind: it commands the raid from outside it.
func (f *raidHireFlow) commanderOutside(ctx context.Context) error {
	deadline := f.e.now().Add(hireOutOfGroup)
	for f.hg.InGroup() {
		if !f.e.now().Before(deadline) {
			return fmt.Errorf("the commander is still in a group %.0f s after the bind", hireOutOfGroup.Seconds())
		}
		if err := f.e.sleep(ctx, time.Second); err != nil {
			return err
		}
	}
	return nil
}

// ready fills the report's ready block: one ReadyBot per member. The role comes from the TBM:RAID_LAYOUT line when it
// names the bot, else from the talents read for tanks/healers and dps for the rest. The item level comes from ItemInfos
// (the existing item-query helper path: one prototype read within the step ceiling); 0 when it cannot be read.
func (f *raidHireFlow) ready(ctx context.Context, layout string) {
	roles := raidLayoutRoles(layout)
	for _, m := range f.hg.Members() {
		w, _ := f.readWho(ctx, m.Name)
		class := strings.ToLower(m.Class)
		if w.Class != "" {
			class = strings.ToLower(w.Class)
		}
		role, ok := roles[strings.ToLower(m.Name)]
		if !ok {
			role = "dps"
			if t, why := f.readTalents(ctx, m.Name); why == "" {
				if r := talentRole(class, t.Trees); r == "tank" || r == "healer" {
					role = r
				}
			}
		}
		rb := ReadyBot{Name: m.Name, Class: class, Role: role, Level: m.Level, Source: "hired", MaxHP: m.MaxHP, Ready: true}
		f.rdy.Bots = append(f.rdy.Bots, rb)
	}
	slices.SortFunc(f.rdy.Bots, func(a, b ReadyBot) int {
		if (a.Name == f.lead) != (b.Name == f.lead) {
			if a.Name == f.lead {
				return -1
			}
			return 1
		}
		return strings.Compare(a.Name, b.Name)
	})
	f.readItemLevels(ctx)
	f.rdy.Hired = namesOfBots(f.rdy.Bots)
}

// healer is the first healer-role bot of the ready block ("" when none).
func (r *ReadyReport) healer() string {
	for _, b := range r.Bots {
		if b.Role == "healer" {
			return b.Name
		}
	}
	return ""
}

func namesOfBots(bs []ReadyBot) []string {
	out := make([]string, 0, len(bs))
	for _, b := range bs {
		out = append(out, b.Name)
	}
	return out
}

// readItemLevels fills the ready block's item levels from the server's prototypes, within the step ceiling: one `inv`
// read per bot and one ItemInfos call over the worn entries, the average item level over the worn gear. A bot that
// cannot be read keeps 0.
func (f *raidHireFlow) readItemLevels(ctx context.Context) {
	var entries []uint32
	seen := map[uint32]bool{}
	invs := map[string]tbm.Inventory{}
	for i := range f.rdy.Bots {
		b := &f.rdy.Bots[i]
		if err := f.over(); err != nil {
			return
		}
		inv, err := botInventory(ctx, f.e.g, b.Name)
		if err != nil {
			continue
		}
		invs[b.Name] = inv
		for _, e := range inv.Equipped {
			if e != 0 && !seen[e] {
				seen[e] = true
				entries = append(entries, e)
			}
		}
	}
	info, err := f.hg.ItemInfos(ctx, entries, hireItemWait)
	if err != nil {
		return
	}
	for i := range f.rdy.Bots {
		b := &f.rdy.Bots[i]
		inv, ok := invs[b.Name]
		if !ok {
			continue
		}
		sum, n := 0, 0
		for s, e := range inv.Equipped {
			if e == 0 || s == slotShirt || s == slotTabrd {
				continue
			}
			if it, ok := info[e]; ok && it.Found {
				sum += it.ItemLevel
				n++
			}
		}
		if n > 0 {
			b.ItemLevel = float64(sum) / float64(n)
		}
	}
}

// raidRoleOf is the bot's role from its `who` answer: the dominant tree's role, dps when the class has no tank/healer
// reading (mage, hunter, rogue, warlock).
func raidRoleOf(w tbm.Who) string {
	if r := talentRole(strings.ToLower(w.Class), w.Trees); r == "tank" || r == "healer" {
		return r
	}
	switch strings.ToLower(w.Class) {
	case "warrior", "paladin", "druid", "priest", "shaman", "hunter", "rogue", "mage", "warlock":
		return "dps"
	}
	return ""
}

// raidLayoutOf is the layout line of a `commander bind <Lead> raid` ack: the first TBM:RAID_LAYOUT| line, else "".
func raidLayoutOf(lines []string) string {
	for _, l := range lines {
		if strings.HasPrefix(l, "TBM:RAID_LAYOUT|") {
			return l
		}
	}
	return ""
}

// raidLayoutRoles reads the roles of a TBM:RAID_LAYOUT| line by bot name (lower case): the main tank and the
// `tank:<name>` / `heal:<name>` words the layout line carries. "" when the line is missing or names no bots.
func raidLayoutRoles(layout string) map[string]string {
	out := map[string]string{}
	for _, w := range strings.Split(layout, "|") {
		kv := strings.SplitN(strings.TrimSpace(w), ":", 2)
		if len(kv) != 2 {
			continue
		}
		switch strings.ToLower(strings.TrimSpace(kv[0])) {
		case "main", "tank":
			out[strings.ToLower(strings.TrimSpace(kv[1]))] = "tank"
		case "heal":
			out[strings.ToLower(strings.TrimSpace(kv[1]))] = "healer"
		}
	}
	return out
}
