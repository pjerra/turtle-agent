package dungeon

import (
	"context"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/pjerra/turtle-agent/internal/objects"
	"github.com/pjerra/turtle-agent/internal/packet"
	"github.com/pjerra/turtle-agent/internal/spells"
)

// Buff is a spell the leader (a GM) puts on each companion with ".aura <id>" on the selected target.
type Buff struct {
	ID   uint32 `json:"id"`
	Name string `json:"name"`
}

// BuffPresets are the named buff lists dungeon_run's buffs parameter accepts. Every id is in the core's
// spell_template (tw_world_spell_template.sql) with the name given, and every one applies an aura (effect 6) to a
// single friendly target at any level.
var BuffPresets = map[string][]Buff{
	// Classic world buffs.
	"world": {
		{22888, "Rallying Cry of the Dragonslayer"},
		{15366, "Songflower Serenade"},
		{24425, "Spirit of Zandalar"},
		{16609, "Warchief's Blessing"},
		{22817, "Fengus' Ferocity"},
		{22818, "Mol'dar's Moxie"},
		{22820, "Slip'kik's Savvy"},
	},
}

// MaxBuffs bounds the buff list (a bot has 32 buff slots).
const MaxBuffs = 20

// ResolveBuffs expands preset names and spell ids into one list without duplicates, in the order given. An id
// outside a preset is named from the spell table (or "spell <id>").
func ResolveBuffs(presets []string, ids []uint32) ([]Buff, error) {
	var out []Buff
	add := func(b Buff) {
		if !slices.ContainsFunc(out, func(x Buff) bool { return x.ID == b.ID }) {
			out = append(out, b)
		}
	}
	for _, p := range presets {
		l, ok := BuffPresets[strings.ToLower(strings.TrimSpace(p))]
		if !ok {
			return nil, fmt.Errorf("buffs: unknown preset %q (presets: %s)", p, strings.Join(presetNames(), ", "))
		}
		for _, b := range l {
			add(b)
		}
	}
	for _, id := range ids {
		if id == 0 {
			return nil, fmt.Errorf("buffs: spell id 0")
		}
		name := spells.Name(id)
		if name == "" {
			name = fmt.Sprintf("spell %d", id)
		}
		add(Buff{ID: id, Name: name})
	}
	if len(out) > MaxBuffs {
		return nil, fmt.Errorf("buffs: %d spells, at most %d", len(out), MaxBuffs)
	}
	return out, nil
}

func presetNames() []string {
	var n []string
	for k := range BuffPresets {
		n = append(n, k)
	}
	slices.Sort(n)
	return n
}

func (b Buff) String() string { return fmt.Sprintf("%s (%d)", b.Name, b.ID) }

func buffNames(l []Buff) []string {
	out := make([]string, len(l))
	for i, b := range l {
		out[i] = b.String()
	}
	return out
}

// memberView finds the member's unit row among the units in view; ok is false when it is not in view.
func (d *Driver) memberView(m Member) (objects.Unit, bool) {
	for _, u := range d.g.Units() {
		if u.Type == "player" && (u.GUID == m.GUID || (u.GUID == 0 && u.Name == m.Name)) {
			return u, true
		}
	}
	return objects.Unit{}, false
}

// missingBuffs lists which of want the member's unit does not carry. seen is false when the member is not in view
// (its auras cannot be told).
func (d *Driver) missingBuffs(m Member, want []Buff) (missing []Buff, seen bool) {
	u, ok := d.memberView(m)
	if !ok {
		return nil, false
	}
	for _, b := range want {
		if !slices.ContainsFunc(u.Auras, func(a objects.Aura) bool { return a.SpellID == b.ID }) {
			missing = append(missing, b)
		}
	}
	return missing, true
}

// buffOutcome is how the first ".aura" of a companion's batch went.
type buffOutcome int

const (
	buffTaken    buffOutcome = iota // the companion shows the aura (or left view, so nothing can be told)
	buffNone                        // it shows nothing, and the leader neither
	buffOnLeader                    // the aura landed on the leader: the selection was not the companion
)

// awaitView waits, until deadline, for the member to be in the leader's object store within BuffMaxDist: a unit the
// leader does not see (just summoned, not yet created for it) may not be selectable, and the GM command then acts on the
// leader itself (live run 2: the 4th companion's first aura landed on the leader). ok is false when it never showed.
func (d *Driver) awaitView(ctx context.Context, m Member, deadline time.Time) (ok bool, err error) {
	for {
		if u, seen := d.memberView(m); seen && (u.Distance < 0 || u.Distance <= d.cfg.BuffMaxDist) {
			return true, nil
		}
		if !d.clk.Now().Before(deadline) {
			return false, nil
		}
		if err := d.sleep(ctx, min(d.cfg.Poll, max(deadline.Sub(d.clk.Now()), time.Millisecond))); err != nil {
			return false, err
		}
	}
}

// hasBuff says whether the member's unit carries the buff; seen is false when it is not in view.
func (d *Driver) hasBuff(m Member, b Buff) (has, seen bool) {
	missing, seen := d.missingBuffs(m, []Buff{b})
	return seen && len(missing) == 0, seen
}

// leaderAuraIDs are the spell ids the leader itself carries now (nil when that cannot be told).
func (d *Driver) leaderAuraIDs() map[uint32]bool {
	src, ok := d.g.(SelfAuraSource)
	if !ok {
		return nil
	}
	auras, ok := src.SelfAuras()
	if !ok {
		return nil
	}
	ids := make(map[uint32]bool, len(auras))
	for _, a := range auras {
		ids[a.SpellID] = true
	}
	return ids
}

// leaderHas says whether the leader carries one of the buffs that it did not carry before the batch began
// (d.leaderBase): an aura the leader already had, say a Rallying Cry of its own, is no sign of a misfire. False when
// the leader's auras cannot be told.
func (d *Driver) leaderHas(list []Buff) bool {
	now := d.leaderAuraIDs()
	return slices.ContainsFunc(list, func(b Buff) bool { return now[b.ID] && !d.leaderBase[b.ID] })
}

// sendBuffs has the leader put each of list on the member with ".aura <id>", every application flagged in the report
// as a "buff" shortcut. Each command goes only once the server has confirmed the selection of the member (Game.GMOn): the
// core runs .aura on whatever is selected when it arrives, and the target RPC or the addon host could change the
// selection between a separate Target and GM call (the world buffs would land on a boss).
//
// The first command of the batch is checked: the member must show the aura, within two UseSettle. When it does not
// (the aura sits on the leader, or nowhere) the selection is made again and the command sent once more; a second miss
// stops the batch (the rest would land on the leader too) and is the outcome. An aura on the leader is never a success.
func (d *Driver) sendBuffs(ctx context.Context, m Member, list []Buff) (buffOutcome, error) {
	d.leaderBase = d.leaderAuraIDs() // what the leader carries before this batch
	for i, b := range list {
		if i > 0 {
			if err := d.sleep(ctx, d.cfg.BuffGap); err != nil {
				return buffNone, err
			}
		}
		if i == 0 {
			if oc, err := d.sendFirst(ctx, m, b, list); err != nil || oc != buffTaken {
				return oc, err
			}
			continue
		}
		if err := d.sendBuff(ctx, m, b); err != nil {
			return buffNone, err
		}
	}
	d.buffedAt[m.GUID] = d.curLeg
	return buffTaken, nil
}

func (d *Driver) sendBuff(ctx context.Context, m Member, b Buff) error {
	d.shortcut("buff", max(d.curLeg, 0), b.Name+" on "+m.Name)
	if err := d.g.GMOn(ctx, m.GUID, fmt.Sprintf(".aura %d", b.ID)); err != nil {
		return fmt.Errorf("buffs: .aura %d on %s: %w", b.ID, m.Name, err)
	}
	return nil
}

func (d *Driver) sendFirst(ctx context.Context, m Member, b Buff, list []Buff) (buffOutcome, error) {
	began := d.clk.Now()
	missed := func() buffOutcome {
		if d.leaderHas([]Buff{b}) {
			return buffOnLeader
		}
		return buffNone
	}
	for try := 1; try <= 2; try++ {
		if try == 2 {
			// Selected again by the command itself; make sure the member is still in the leader's view first.
			if ok, err := d.awaitView(ctx, m, d.clk.Now().Add(d.cfg.BuffViewWait)); err != nil || !ok {
				return buffNone, err
			}
		}
		if err := d.sendBuff(ctx, m, b); err != nil {
			return buffNone, err
		}
		if try == 1 {
			// The first look is at 2 x UseSettle; a miss then is answered with the second command.
			for look := 0; look < 2; look++ {
				if err := d.sleep(ctx, d.cfg.UseSettle); err != nil {
					return buffNone, err
				}
				if has, seen := d.hasBuff(m, b); has || !seen {
					return buffTaken, nil
				}
			}
			where := "on nobody"
			if missed() == buffOnLeader {
				where = "on the leader"
			}
			d.rep.note(fmt.Sprintf("leg %d: the first .aura (%s) for %s landed %s; selecting %s again and sending it once more", max(d.curLeg, 0), b.Name, m.Name, where, m.Name))
		}
	}
	// The second command is given the rest of BuffViewWait, counted from the first, to show: the run is not failed
	// before then.
	for {
		if err := d.sleep(ctx, min(d.cfg.UseSettle, max(began.Add(d.cfg.BuffViewWait).Sub(d.clk.Now()), time.Millisecond))); err != nil {
			return buffNone, err
		}
		if has, seen := d.hasBuff(m, b); has || !seen {
			return buffTaken, nil
		}
		if !d.clk.Now().Before(began.Add(d.cfg.BuffViewWait)) {
			return missed(), nil
		}
	}
}

// noEffectError is the failure of a batch whose first command did not take.
func (d *Driver) noEffectError(m Member, missing []Buff, oc buffOutcome) error {
	where := ""
	if oc == buffOnLeader {
		where = "; the aura landed on the leader itself, so the selection of " + m.Name + " did not hold"
	}
	return fmt.Errorf("buffs: GM .aura on %s had no effect (%s still missing %.1f s after%s): the leader must be a GM with developer security, "+
		"or the server refused the command (below that level .aura puts the aura on the leader itself); a companion with all 32 buff slots "+
		"full cannot take more either, so use fewer spells",
		m.Name, strings.Join(buffNames(missing), ", "), d.cfg.UseSettle.Seconds(), where)
}

// checkBuffs looks for list in the member's auras. done is false when the member is not in view (nothing can be
// told). A buff missing from a member in view is an error with strict, else a note.
func (d *Driver) checkBuffs(m Member, list []Buff, strict bool) (done bool, err error) {
	missing, seen := d.missingBuffs(m, list)
	switch {
	case !seen:
		return false, nil
	case len(missing) > 0 && strict:
		oc := buffNone
		if d.leaderHas(missing) {
			oc = buffOnLeader
		}
		return true, d.noEffectError(m, missing, oc)
	case len(missing) > 0:
		d.rep.note(fmt.Sprintf("leg %d: buffs still missing on %s after the GM .aura: %s", d.curLeg, m.Name, strings.Join(buffNames(missing), ", ")))
	}
	return true, nil
}

// giveBuffs sends list to the member and checks it a moment later, without failing the run: a revive or a leg top-up
// that does not stick (or finds the member out of view) is noted.
func (d *Driver) giveBuffs(ctx context.Context, m Member, list []Buff) error {
	if len(list) == 0 {
		return nil
	}
	seen, err := d.awaitView(ctx, m, d.clk.Now().Add(d.cfg.BuffViewWait))
	if err != nil {
		return err
	}
	if !seen {
		d.rep.note(fmt.Sprintf("buffs on %s not given: not in the leader's view within %.0f s", m.Name, d.cfg.BuffViewWait.Seconds()))
		return nil
	}
	oc, err := d.sendBuffs(ctx, m, list)
	if err != nil {
		return err
	}
	if oc != buffTaken {
		d.rep.note(fmt.Sprintf("leg %d: buffs on %s did not take (%s landed %s)", d.curLeg, m.Name, list[0].Name, map[buffOutcome]string{buffOnLeader: "on the leader", buffNone: "nowhere"}[oc]))
		return nil
	}
	if err := d.sleep(ctx, d.cfg.UseSettle); err != nil {
		return err
	}
	if done, _ := d.checkBuffs(m, list, false); !done {
		d.rep.note(fmt.Sprintf("buffs on %s could not be checked: not in view", m.Name))
	}
	return nil
}

// startBuffs puts every buff on every living online member as the run begins, then checks the effect of the commands
// (strict). A companion has to be in the leader's view, within BuffMaxDist, before its batch goes (right after a
// teleport it may not be yet): all of them are waited for up to BuffViewWait together. One that never shows is
// noted and gets nothing; if none shows the run fails, since nothing proves the command works.
func (d *Driver) startBuffs(ctx context.Context, fromLeg int) error {
	if len(d.cfg.Buffs) == 0 {
		return nil
	}
	d.curLeg = fromLeg // so the flags carry the leg the run starts at
	defer func() { d.curLeg = -1 }()
	var pending, unseen []Member
	deadline := d.clk.Now().Add(d.cfg.BuffViewWait)
	for _, m := range d.g.Party() {
		if !m.Online || m.Dead {
			continue
		}
		seen, err := d.awaitView(ctx, m, deadline)
		if err != nil {
			return err
		}
		if !seen {
			unseen = append(unseen, m)
			continue
		}
		oc, err := d.sendBuffs(ctx, m, d.cfg.Buffs)
		if err != nil {
			return err
		}
		if oc != buffTaken {
			return d.noEffectError(m, d.cfg.Buffs[:1], oc)
		}
		pending = append(pending, m)
	}
	var names []string
	for _, m := range unseen {
		names = append(names, m.Name)
	}
	if len(pending) == 0 {
		if len(unseen) == 0 {
			return nil
		}
		return fmt.Errorf("buffs: could not check the GM .aura: %s not in view within %.0f s of the command, so nothing shows it worked "+
			"(the leader needs developer security, and the companions must stand near it)", strings.Join(names, ", "), d.cfg.BuffViewWait.Seconds())
	}
	if len(unseen) > 0 {
		d.rep.note("buffs on " + strings.Join(names, ", ") + " not given: not in view within " + d.cfg.BuffViewWait.String())
	}
	recheck := d.clk.Now().Add(d.cfg.BuffViewWait)
	if err := d.sleep(ctx, d.cfg.UseSettle); err != nil {
		return err
	}
	for {
		var still []Member
		for _, m := range pending {
			done, err := d.checkBuffs(m, d.cfg.Buffs, true)
			if err != nil {
				return err
			}
			if !done {
				still = append(still, m)
			}
		}
		pending = still
		if len(pending) == 0 || !d.clk.Now().Before(recheck) {
			break
		}
		if err := d.sleep(ctx, d.cfg.Poll); err != nil {
			return err
		}
	}
	if len(pending) > 0 {
		var left []string
		for _, m := range pending {
			left = append(left, m.Name)
		}
		d.rep.note("buffs on " + strings.Join(left, ", ") + " could not be checked: not in view")
	}
	return nil
}

// topUpBuffs runs as a leg starts: a living member in view that lost some buff gets it again, once per leg.
func (d *Driver) topUpBuffs(ctx context.Context) error {
	if len(d.cfg.Buffs) == 0 {
		return nil
	}
	for _, m := range d.g.Party() {
		if !m.Online || m.Dead {
			continue
		}
		if at, ok := d.buffedAt[m.GUID]; ok && at == d.curLeg {
			continue
		}
		missing, seen := d.missingBuffs(m, d.cfg.Buffs)
		if !seen || len(missing) == 0 {
			continue
		}
		if err := d.giveBuffs(ctx, m, missing); err != nil {
			return err
		}
	}
	return nil
}

// rebuff gives a revived member all the buffs again (death strips them).
func (d *Driver) rebuff(ctx context.Context, g packet.GUID) error {
	if len(d.cfg.Buffs) == 0 {
		return nil
	}
	for _, m := range d.g.Party() {
		if m.GUID == g && m.Online && !m.Dead {
			return d.giveBuffs(ctx, m, d.cfg.Buffs)
		}
	}
	return nil
}
