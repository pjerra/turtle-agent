package dungeon

import (
	"context"
	"fmt"
	"slices"
	"strings"

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

// sendBuffs has the leader put each of list on the member with ".aura <id>", every application flagged in the report
// as a "buff" shortcut. Each command goes with the selection of the member as ONE session operation (Game.GMOn): the
// core runs .aura on whatever is selected when it arrives, and the target RPC or the addon host could change the
// selection between a separate Target and GM call (the world buffs would land on a boss).
func (d *Driver) sendBuffs(ctx context.Context, m Member, list []Buff) error {
	for i, b := range list {
		if i > 0 {
			if err := d.sleep(ctx, d.cfg.BuffGap); err != nil {
				return err
			}
		}
		d.shortcut("buff", max(d.curLeg, 0), b.Name+" on "+m.Name)
		if err := d.g.GMOn(ctx, m.GUID, fmt.Sprintf(".aura %d", b.ID)); err != nil {
			return fmt.Errorf("buffs: .aura %d on %s: %w", b.ID, m.Name, err)
		}
	}
	d.buffedAt[m.GUID] = d.curLeg
	return nil
}

// checkBuffs looks for list in the member's auras. done is false when the member is not in view (nothing can be
// told). A buff missing from a member in view is an error with strict, else a note.
func (d *Driver) checkBuffs(m Member, list []Buff, strict bool) (done bool, err error) {
	missing, seen := d.missingBuffs(m, list)
	switch {
	case !seen:
		return false, nil
	case len(missing) > 0 && strict:
		return true, fmt.Errorf("buffs: GM .aura on %s had no effect (%s still missing %.1f s after): the leader must be a GM with developer security, "+
			"or the server refused the command (below that level .aura puts the aura on the leader itself); a companion with all 32 buff slots "+
			"full cannot take more either, so use fewer spells",
			m.Name, strings.Join(buffNames(missing), ", "), d.cfg.UseSettle.Seconds())
	case len(missing) > 0:
		d.rep.note(fmt.Sprintf("leg %d: buffs still missing on %s after the GM .aura: %s", d.curLeg, m.Name, strings.Join(buffNames(missing), ", ")))
	}
	return true, nil
}

// giveBuffs sends list to the member and checks it a moment later, without failing the run: a revive or a leg top-up
// that does not stick is noted.
func (d *Driver) giveBuffs(ctx context.Context, m Member, list []Buff) error {
	if len(list) == 0 {
		return nil
	}
	if err := d.sendBuffs(ctx, m, list); err != nil {
		return err
	}
	if err := d.sleep(ctx, d.cfg.UseSettle); err != nil {
		return err
	}
	if done, _ := d.checkBuffs(m, list, false); !done {
		d.rep.note(fmt.Sprintf("buffs on %s could not be checked: not in view", m.Name))
	}
	return nil
}

// startBuffs puts every buff on every living online member as the run begins, then checks the first effect of the
// command (strict). Companions out of view (right after a teleport) are looked at again for up to BuffViewWait; if
// none of them could be checked in that time the run fails, since nothing proves the command worked.
func (d *Driver) startBuffs(ctx context.Context, fromLeg int) error {
	if len(d.cfg.Buffs) == 0 {
		return nil
	}
	d.curLeg = fromLeg // so the flags carry the leg the run starts at
	defer func() { d.curLeg = -1 }()
	var pending []Member
	for _, m := range d.g.Party() {
		if !m.Online || m.Dead {
			continue
		}
		if err := d.sendBuffs(ctx, m, d.cfg.Buffs); err != nil {
			return err
		}
		pending = append(pending, m)
	}
	if len(pending) == 0 {
		return nil
	}
	total := len(pending)
	deadline := d.clk.Now().Add(d.cfg.BuffViewWait)
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
		if len(pending) == 0 || !d.clk.Now().Before(deadline) {
			break
		}
		if err := d.sleep(ctx, d.cfg.Poll); err != nil {
			return err
		}
	}
	var names []string
	for _, m := range pending {
		names = append(names, m.Name)
	}
	switch {
	case len(pending) == total:
		return fmt.Errorf("buffs: could not check the GM .aura: %s not in view within %.0f s of the command, so nothing shows it worked "+
			"(the leader needs developer security, and the companions must stand near it)", strings.Join(names, ", "), d.cfg.BuffViewWait.Seconds())
	case len(pending) > 0:
		d.rep.note("buffs on " + strings.Join(names, ", ") + " could not be checked: not in view")
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
