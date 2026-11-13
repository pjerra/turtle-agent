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
	// The world buffs a raid keeps on Turtle (raid-walk --prepull-aura's "world" token): the list above without
	// Warchief's Blessing, which only a Horde player can have and which a buffed Alliance raid would never show.
	"world-raid": {
		{22888, "Rallying Cry of the Dragonslayer"},
		{15366, "Songflower Serenade"},
		{24425, "Spirit of Zandalar"},
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

// AuraTarget is one unit auras go on: its guid and the name the report and the core know it by.
type AuraTarget struct {
	GUID packet.GUID
	Name string
}

// AuraOutcome is how the first ".aura" of a target's batch went.
type AuraOutcome int

const (
	AuraTaken  AuraOutcome = iota // the target shows the aura (or left view, so nothing can be told)
	AuraNone                      // it shows nothing, and the caster neither
	AuraOnSelf                    // the aura landed on the caster itself (dungeon_run's leader, raid-walk's commander): the selection was not the target
)

// AuraPort is what applying a list of auras needs from its caller: the GM select-and-send, the target's unit row, the
// caster's own auras, the clock and a note sink. dungeon_run's Driver (startBuffs and its top-ups) and raid-walk's
// --prepull-aura both apply auras through it, so the ".aura" behaviour (the first-aura check, one retry, an aura on
// the caster is a miss) has one implementation.
type AuraPort interface {
	// SendAura selects t and sends the GM line that puts b on it, only while that selection stands.
	SendAura(ctx context.Context, t AuraTarget, b Buff) error
	// ViewUnit returns t's unit row as the client holds it; false when it cannot be seen.
	ViewUnit(ctx context.Context, t AuraTarget) (objects.Unit, bool)
	// CasterAuras lists the caster's own auras; false when that cannot be told.
	CasterAuras(ctx context.Context) ([]objects.Aura, bool)
	Sleep(ctx context.Context, d time.Duration) error
	// Note reports one line in the caller's own words.
	Note(msg string)
}

// AuraCfg is one application's timings and reach.
type AuraCfg struct {
	Gap      time.Duration // between two auras of one target's list
	Settle   time.Duration // the first aura's look window is 2 x Settle
	ViewWait time.Duration // the longest wait for the target to show, and for the first aura to land
	MaxDist  float32       // the farthest the target may be; <= 0 (or an unknown distance, < 0): no rule
}

// missingBuffs lists which of want the member's unit does not carry. seen is false when the member is not in view
// (its auras cannot be told).
func (d *Driver) missingBuffs(ctx context.Context, m Member, want []Buff) (missing []Buff, seen bool) {
	return AurasMissing(ctx, driverAuras{d}, AuraTarget{GUID: m.GUID, Name: m.Name}, want)
}

// AurasMissing lists which of want the target does not carry. seen is false when the target cannot be seen (its auras
// cannot be told).
func AurasMissing(ctx context.Context, p AuraPort, t AuraTarget, want []Buff) (missing []Buff, seen bool) {
	u, ok := p.ViewUnit(ctx, t)
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

// auraPresent says whether the target carries b; seen is false when it cannot be told.
func auraPresent(ctx context.Context, p AuraPort, t AuraTarget, b Buff) (has, seen bool) {
	missing, seen := AurasMissing(ctx, p, t, []Buff{b})
	return seen && len(missing) == 0, seen
}

// awaitVisible waits, until deadline, for the target to be in view within cfg.MaxDist: a unit the caster does not see
// (just summoned, not yet created for it) may not be selectable, and the GM command then acts on the caster itself
// (live run 2: the 4th companion's first aura landed on the leader). ok is false when it never showed.
func awaitVisible(ctx context.Context, p AuraPort, t AuraTarget, deadline time.Time, cfg AuraCfg, now func() time.Time) (ok bool, err error) {
	for {
		if u, seen := p.ViewUnit(ctx, t); seen && (cfg.MaxDist <= 0 || u.Distance < 0 || u.Distance <= cfg.MaxDist) {
			return true, nil
		}
		if !now().Before(deadline) {
			return false, nil
		}
		poll := cfg.Settle
		if poll <= 0 {
			poll = 200 * time.Millisecond
		}
		if err := p.Sleep(ctx, min(poll, max(deadline.Sub(now()), time.Millisecond))); err != nil {
			return false, err
		}
	}
}

// leaderAuraIDs are the spell ids the leader itself carries now (nil when that cannot be told).
func (d *Driver) leaderAuraIDs(ctx context.Context) map[uint32]bool {
	return casterIDs(ctx, driverAuras{d})
}

// casterIDs are the caster's own aura ids now (nil when that cannot be told).
func casterIDs(ctx context.Context, p AuraPort) map[uint32]bool {
	auras, ok := p.CasterAuras(ctx)
	if !ok {
		return nil
	}
	ids := make(map[uint32]bool, len(auras))
	for _, a := range auras {
		ids[a.SpellID] = true
	}
	return ids
}

// casterHasNew says whether the caster carries one of list that it did not carry before the batch began (base): an
// aura the caster already had, say a Rallying Cry of its own, is no sign of a misfire. False when the caster's auras
// cannot be told.
func casterHasNew(ctx context.Context, p AuraPort, base map[uint32]bool, list []Buff) bool {
	now := casterIDs(ctx, p)
	return slices.ContainsFunc(list, func(b Buff) bool { return now != nil && now[b.ID] && !base[b.ID] })
}

// leaderHas says whether the leader carries one of the buffs that it did not carry before the batch began
// (d.leaderBase): an aura the leader already had, say a Rallying Cry of its own, is no sign of a misfire. False when
// the leader's auras cannot be told.
func (d *Driver) leaderHas(ctx context.Context, list []Buff) bool {
	return casterHasNew(ctx, driverAuras{d}, d.leaderBase, list)
}

// awaitView waits, until deadline, for the member to be in the leader's object store within BuffMaxDist.
func (d *Driver) awaitView(ctx context.Context, m Member, deadline time.Time) (ok bool, err error) {
	return awaitVisible(ctx, driverAuras{d}, AuraTarget{GUID: m.GUID, Name: m.Name}, deadline, d.auraCfg(), d.clk.Now)
}

// ApplyAuras puts each of list on t with ".aura <id>", every application the caller flagged as its own shortcut.
// Each command goes only once the server has confirmed the selection of the target (AuraPort.SendAura): the core runs
// .aura on whatever is selected when it arrives, and another request could change the selection between a separate
// Target and GM call (the world buffs would land on a boss).
//
// The first command of the batch is checked: the target must show the aura, within two cfg.Settle. When it does not
// (the aura sits on the caster, or nowhere) the selection is made again and the command sent once more; a second miss
// stops the batch (the rest would land on the caster too) and is the outcome. An aura on the caster is never a success.
func ApplyAuras(ctx context.Context, p AuraPort, t AuraTarget, list []Buff, cfg AuraCfg, now func() time.Time) (AuraOutcome, error) {
	base := casterIDs(ctx, p) // what the caster carries before this batch
	for i, b := range list {
		if i > 0 {
			if err := p.Sleep(ctx, cfg.Gap); err != nil {
				return AuraNone, err
			}
		}
		if i == 0 {
			if oc, err := applyFirstAura(ctx, p, t, b, cfg, base, now); err != nil || oc != AuraTaken {
				return oc, err
			}
			continue
		}
		if err := p.SendAura(ctx, t, b); err != nil {
			return AuraNone, err
		}
	}
	return AuraTaken, nil
}

// applyFirstAura sends the batch's first aura and checks it landed on the target: a first miss is retried once (the
// selection made again), and the retry is given the rest of cfg.ViewWait to show.
func applyFirstAura(ctx context.Context, p AuraPort, t AuraTarget, b Buff, cfg AuraCfg, base map[uint32]bool, now func() time.Time) (AuraOutcome, error) {
	began := now()
	missed := func() AuraOutcome {
		if casterHasNew(ctx, p, base, []Buff{b}) {
			return AuraOnSelf
		}
		return AuraNone
	}
	for try := 1; try <= 2; try++ {
		if try == 2 {
			// Selected again by the command itself; make sure the target is still in the caster's view first.
			if ok, err := awaitVisible(ctx, p, t, now().Add(cfg.ViewWait), cfg, now); err != nil || !ok {
				return AuraNone, err
			}
		}
		if err := p.SendAura(ctx, t, b); err != nil {
			return AuraNone, err
		}
		if try == 1 {
			// The first look is at 2 x Settle; a miss then is answered with the second command.
			for look := 0; look < 2; look++ {
				if err := p.Sleep(ctx, cfg.Settle); err != nil {
					return AuraNone, err
				}
				if has, seen := auraPresent(ctx, p, t, b); has || !seen {
					return AuraTaken, nil
				}
			}
			where := "on nobody"
			if missed() == AuraOnSelf {
				where = "on the leader"
			}
			p.Note(fmt.Sprintf("the first .aura (%s) for %s landed %s; selecting %s again and sending it once more", b.Name, t.Name, where, t.Name))
		}
	}
	// The second command is given the rest of ViewWait, counted from the first, to show: nothing is failed before then.
	for {
		if err := p.Sleep(ctx, min(cfg.Settle, max(began.Add(cfg.ViewWait).Sub(now()), time.Millisecond))); err != nil {
			return AuraNone, err
		}
		if has, seen := auraPresent(ctx, p, t, b); has || !seen {
			return AuraTaken, nil
		}
		if !now().Before(began.Add(cfg.ViewWait)) {
			return missed(), nil
		}
	}
}

// sendBuffs has the leader put each of list on the member, every application flagged in the report as a "buff"
// shortcut, and records the member as buffed at the current leg.
func (d *Driver) sendBuffs(ctx context.Context, m Member, list []Buff) (AuraOutcome, error) {
	d.leaderBase = d.leaderAuraIDs(ctx) // what the leader carries before this batch
	oc, err := ApplyAuras(ctx, driverAuras{d}, AuraTarget{GUID: m.GUID, Name: m.Name}, list, d.auraCfg(), d.clk.Now)
	if err == nil && oc == AuraTaken {
		d.buffedAt[m.GUID] = d.curLeg
	}
	return oc, err
}

// auraCfg is the Driver's timings and reach for the shared aura application.
func (d *Driver) auraCfg() AuraCfg {
	return AuraCfg{Gap: d.cfg.BuffGap, Settle: d.cfg.UseSettle, ViewWait: d.cfg.BuffViewWait, MaxDist: d.cfg.BuffMaxDist}
}

// driverAuras is the Driver's AuraPort: its game's GM select-and-send, its unit store, the leader's own auras, its
// clock and its report.
type driverAuras struct{ d *Driver }

func (a driverAuras) SendAura(ctx context.Context, t AuraTarget, b Buff) error {
	return a.d.sendBuff(ctx, Member{GUID: t.GUID, Name: t.Name}, b)
}

func (a driverAuras) ViewUnit(_ context.Context, t AuraTarget) (objects.Unit, bool) {
	return a.d.memberView(Member{GUID: t.GUID, Name: t.Name})
}

func (a driverAuras) CasterAuras(context.Context) ([]objects.Aura, bool) {
	src, ok := a.d.g.(SelfAuraSource)
	if !ok {
		return nil, false
	}
	return src.SelfAuras()
}

func (a driverAuras) Sleep(ctx context.Context, d time.Duration) error { return a.d.sleep(ctx, d) }

func (a driverAuras) Note(msg string) {
	a.d.rep.note(fmt.Sprintf("leg %d: %s", max(a.d.curLeg, 0), msg))
}

// sendBuff puts one buff on the member with ".aura <id>" through the game's GM select-and-send, flagged in the report
// as the "buff" shortcut.
func (d *Driver) sendBuff(ctx context.Context, m Member, b Buff) error {
	d.shortcut("buff", max(d.curLeg, 0), b.Name+" on "+m.Name)
	if err := d.g.GMOn(ctx, m.GUID, fmt.Sprintf(".aura %d", b.ID)); err != nil {
		return fmt.Errorf("buffs: .aura %d on %s: %w", b.ID, m.Name, err)
	}
	return nil
}

// noEffectError is the failure of a batch whose first command did not take.
func (d *Driver) noEffectError(ctx context.Context, m Member, missing []Buff, oc AuraOutcome) error {
	where := ""
	if oc == AuraOnSelf {
		where = "; the aura landed on the leader itself, so the selection of " + m.Name + " did not hold"
	}
	return fmt.Errorf("buffs: GM .aura on %s had no effect (%s still missing %.1f s after%s): the leader must be a GM with developer security, "+
		"or the server refused the command (below that level .aura puts the aura on the leader itself); a companion with all 32 buff slots "+
		"full cannot take more either, so use fewer spells",
		m.Name, strings.Join(buffNames(missing), ", "), d.cfg.UseSettle.Seconds(), where)
}

// checkBuffs looks for list in the member's auras. done is false when the member is not in view (nothing can be
// told). A buff missing from a member in view is an error with strict, else a note.
func (d *Driver) checkBuffs(ctx context.Context, m Member, list []Buff, strict bool) (done bool, err error) {
	missing, seen := d.missingBuffs(ctx, m, list)
	switch {
	case !seen:
		return false, nil
	case len(missing) > 0 && strict:
		oc := AuraNone
		if d.leaderHas(ctx, missing) {
			oc = AuraOnSelf
		}
		return true, d.noEffectError(ctx, m, missing, oc)
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
	if oc != AuraTaken {
		d.rep.note(fmt.Sprintf("leg %d: buffs on %s did not take (%s landed %s)", d.curLeg, m.Name, list[0].Name, map[AuraOutcome]string{AuraOnSelf: "on the leader", AuraNone: "nowhere"}[oc]))
		return nil
	}
	if err := d.sleep(ctx, d.cfg.UseSettle); err != nil {
		return err
	}
	if done, _ := d.checkBuffs(ctx, m, list, false); !done {
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
		if oc != AuraTaken {
			return d.noEffectError(ctx, m, d.cfg.Buffs[:1], oc)
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
			done, err := d.checkBuffs(ctx, m, d.cfg.Buffs, true)
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
		missing, seen := d.missingBuffs(ctx, m, d.cfg.Buffs)
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
