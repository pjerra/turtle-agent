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

// giveBuffs has the leader select the member and put each of list on it (".aura <id>"), every application flagged
// in the report as a "buff" shortcut. The server answers a refused command (or one below developer level, where
// ".aura" lands on the leader itself) only in chat, so the effect is checked afterwards in the member's auras: with
// strict, a buff that is still missing from a member in view ends the run with a clear error, else it is a note.
func (d *Driver) giveBuffs(ctx context.Context, m Member, list []Buff, strict bool) error {
	if len(list) == 0 {
		return nil
	}
	if err := d.g.Target(ctx, m.GUID); err != nil {
		return fmt.Errorf("buffs: select %s: %w", m.Name, err)
	}
	for i, b := range list {
		if i > 0 {
			if err := d.sleep(ctx, d.cfg.BuffGap); err != nil {
				return err
			}
		}
		d.shortcut("buff", max(d.curLeg, 0), b.Name+" on "+m.Name)
		if err := d.g.GM(ctx, fmt.Sprintf(".aura %d", b.ID)); err != nil {
			return fmt.Errorf("buffs: .aura %d on %s: %w", b.ID, m.Name, err)
		}
	}
	if err := d.sleep(ctx, d.cfg.UseSettle); err != nil {
		return err
	}
	missing, seen := d.missingBuffs(m, list)
	switch {
	case !seen:
		d.rep.note(fmt.Sprintf("buffs on %s could not be checked: not in view", m.Name))
	case len(missing) > 0 && strict:
		return fmt.Errorf("buffs: GM .aura on %s had no effect (%s still missing %.1f s after): the leader must be a GM with developer security, "+
			"or the server refused the command (below that level .aura puts the aura on the leader itself)",
			m.Name, strings.Join(buffNames(missing), ", "), d.cfg.UseSettle.Seconds())
	case len(missing) > 0:
		d.rep.note(fmt.Sprintf("leg %d: buffs still missing on %s after the GM .aura: %s", d.curLeg, m.Name, strings.Join(buffNames(missing), ", ")))
	}
	d.buffedAt[m.GUID] = d.curLeg
	return nil
}

// startBuffs puts every buff on every living online member, as the run begins (strict: the first check of the
// command).
func (d *Driver) startBuffs(ctx context.Context, fromLeg int) error {
	if len(d.cfg.Buffs) == 0 {
		return nil
	}
	d.curLeg = fromLeg // so the flags carry the leg the run starts at
	defer func() { d.curLeg = -1 }()
	for _, m := range d.g.Party() {
		if !m.Online || m.Dead {
			continue
		}
		if err := d.giveBuffs(ctx, m, d.cfg.Buffs, true); err != nil {
			return err
		}
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
		if err := d.giveBuffs(ctx, m, missing, false); err != nil {
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
			return d.giveBuffs(ctx, m, d.cfg.Buffs, false)
		}
	}
	return nil
}
