package brain

import (
	"bytes"
	"context"
	"crypto/sha256"
	"embed"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strings"
	"sync"

	"github.com/pjerra/turtle-agent/internal/objects"
	"github.com/pjerra/turtle-agent/internal/spells"
)

// Rotations as data (DESIGN-DUNGEON-MODES P2, TA-D). A rotation file is the priority list of one class and spec for the agent's own
// character: spells by name (the brain casts the highest rank the spellbook has), each with the conditions it waits for. The list is
// read top to bottom: an earlier entry beats a later one of the same band. A file REPLACES the class rotation of a preset (the
// strategies its "replaces" names), so the same preset plays the Go rotation or the file, and two files can be switched between by name
// (`rotation` of dungeon_run and brain_start, `--rotation` of tools/run-uldaman.sh): that is the A/B arm of a pull test.
//
// The files are reimplemented from understanding the behaviour of mod-playerbots' class strategies (GPL-2.0+; ideas and thresholds
// only, no code, see each file's "sources") and checked against the Turtle core's spell data: every spell name must exist, the level
// of the first rank must match (`turtle-agent rotation check`), and `rotation show <name> --level N` lists the rank cast at level N.
//
// Format, version 1 (unknown fields are refused):
//
//	{"version": 1, "name": "frost_mage", "class": "mage", "spec": "frost",
//	 "replaces": ["mage_frost"],               strategies of the preset this takes the place of
//	 "sources": ["mod-playerbots src/Ai/Class/Mage/Strategy/FrostMageStrategy.cpp"],
//	 "priorities": [                           highest first
//	   {"id": "frost_nova", "spell": "Frost Nova", "level": 10, "target": "self", "band": "high", "threat": "aoe",
//	    "when": {"melee_mob": true, "no_mob_aura": ["Frost Nova"]}},
//	   {"id": "frostbolt", "spell": "Frostbolt", "level": 4, "target": "fight"}]}
//
// Entry: id (the action's name in brain_decision), kind (cast, the default; attack: the auto attack at the fight's mob; next_swing: a
// spell that replaces the next swing, sent once per swing; stance: return to a home stance, with stances), spell or spell_id, level (the
// character level of the first rank; talent true: a talent, any level), target (fight, melee_mob, self), band (low 5, normal 10, high
// 20, move 30, emergency 43; default normal), threat (single, aoe, none), harmful (default: the target is an enemy), alt (ids tried when
// this one cannot run), when (conditions, all must hold; see Conditions).
//
// The conditions are a closed set, each a function of what the agent sees: so a file cannot do more than a player's eyes can.

// RotationVersion is the format this build reads.
const RotationVersion = 1

//go:embed rotations/*.json
var builtinRotationFiles embed.FS

// Conditions of a priority entry. All that are set must hold.
type Conditions struct {
	State string `json:"state,omitempty"` // combat (the default for an enemy target: there is a mob to fight), rest (out of combat, upkeep allowed), any (the default for self)

	MeleeMob    bool     `json:"melee_mob,omitempty"`         // a mob that attacks the player stands within 8 yd (the "mob" of the mob_* conditions is that one)
	MobInReach  bool     `json:"mob_in_reach,omitempty"`      // the fight's mob is within melee reach
	MobsInReach int      `json:"mobs_in_reach_min,omitempty"` // at least this many mobs in combat within melee reach
	MobsInCone  int      `json:"mobs_in_cone_min,omitempty"`  // at least this many mobs in combat in the 90 degree cone in front within 10 yd
	MobDistMin  float32  `json:"mob_distance_min,omitempty"`  // the fight's mob is at least this far
	MobDistMax  float32  `json:"mob_distance_max,omitempty"`  // and at most this far
	MobOnMe     bool     `json:"mob_on_me,omitempty"`         // a mob in combat has the player as its target
	TankHolds   bool     `json:"tank_holds,omitempty"`        // a tank holds the fight (or a mob has the player): a gap closer does not run into an un-tanked pull
	Feared      bool     `json:"feared,omitempty"`            // the player carries a fear or a horror
	MobFleeing  bool     `json:"mob_fleeing,omitempty"`       // the mob runs at low health within a hamstring's reach
	Moving      *bool    `json:"moving,omitempty"`            // the player moves (true) or stands (false)
	InCombat    *bool    `json:"in_combat,omitempty"`         // the player's combat flag
	After       string   `json:"after,omitempty"`             // dodge: the mob dodged a moment ago; avoided: the player dodged, parried or blocked
	ComboMin    int      `json:"combo_points_min,omitempty"`  // combo points on the fight's mob
	Stance      []string `json:"stance,omitempty"`            // one of these stance auras is on the player

	SelfHPBelow *int `json:"self_hp_below,omitempty"` // percent
	SelfHPAbove *int `json:"self_hp_above,omitempty"`
	PowerBelow  *int `json:"power_below,omitempty"` // percent of the player's power (mana, rage, energy)
	PowerAbove  *int `json:"power_above,omitempty"`
	MobHPBelow  *int `json:"mob_hp_below,omitempty"` // percent of the mob's health
	MobHPAbove  *int `json:"mob_hp_above,omitempty"`

	SelfAura        []string `json:"self_aura,omitempty"`         // any of these on the player
	NoSelfAura      []string `json:"no_self_aura,omitempty"`      // none of these on the player
	SelfAuraMissing []string `json:"self_aura_missing,omitempty"` // none of these on the player, or one with under a minute left (a buff to keep up)
	MobAura         []string `json:"mob_aura,omitempty"`
	NoMobAura       []string `json:"no_mob_aura,omitempty"`
	MobStacksBelow  *Stacks  `json:"mob_stacks_below,omitempty"`  // the mob carries fewer stacks of the aura
	MobsLacking     string   `json:"mobs_lacking_aura,omitempty"` // a mob in melee reach lacks the aura
}

// Stacks is an aura and a number of stacks.
type Stacks struct {
	Aura   string `json:"aura"`
	Stacks int    `json:"stacks"`
}

// Priority is one entry of a rotation.
type Priority struct {
	ID      string     `json:"id"`
	Kind    string     `json:"kind,omitempty"`
	Spell   string     `json:"spell,omitempty"`
	SpellID uint32     `json:"spell_id,omitempty"`
	Level   int        `json:"level,omitempty"`
	Talent  bool       `json:"talent,omitempty"`
	Target  string     `json:"target,omitempty"`
	Band    string     `json:"band,omitempty"`
	Threat  string     `json:"threat,omitempty"`
	Harmful *bool      `json:"harmful,omitempty"`
	Alt     []string   `json:"alt,omitempty"`
	Stances []string   `json:"stances,omitempty"`
	When    Conditions `json:"when"`
	Note    string     `json:"note,omitempty"`
}

// Rotation is a rotation file.
type Rotation struct {
	Version    int        `json:"version"`
	Name       string     `json:"name"`
	Class      string     `json:"class"`
	Spec       string     `json:"spec,omitempty"`
	Replaces   []string   `json:"replaces"`
	Notes      string     `json:"notes,omitempty"`
	Sources    []string   `json:"sources,omitempty"`
	Priorities []Priority `json:"priorities"`

	sha string // of the file's bytes, for the report
}

// Rotation limits.
const maxPriorities = 40

var bands = map[string]float32{"low": RelDefault, "normal": RelNormal, "high": RelHigh, "move": RelMove, "emergency": relEmergency}

var rotationName = func(s string) bool {
	if s == "" || len(s) > 40 {
		return false
	}
	for _, r := range s {
		if !(r >= 'a' && r <= 'z' || r >= '0' && r <= '9' || r == '_') {
			return false
		}
	}
	return true
}

// ParseRotation reads a rotation file's bytes and checks it (Validate).
func ParseRotation(data []byte) (*Rotation, error) {
	var r Rotation
	d := json.NewDecoder(bytes.NewReader(data))
	d.DisallowUnknownFields()
	if err := d.Decode(&r); err != nil {
		return nil, fmt.Errorf("rotation file: %w", err)
	}
	if d.More() {
		return nil, errors.New("rotation file: more than one value")
	}
	if err := r.Validate(); err != nil {
		return nil, err
	}
	r.sha = shortSHA(data)
	return &r, nil
}

// Validate checks the rotation against the formats rules and the spell data.
func (r *Rotation) Validate() error {
	bad := func(path, format string, a ...any) error {
		return fmt.Errorf("rotation %q: %s: %s", r.Name, path, fmt.Sprintf(format, a...))
	}
	if r.Version != RotationVersion {
		return fmt.Errorf("rotation %q: version %d, this build reads %d", r.Name, r.Version, RotationVersion)
	}
	if !rotationName(r.Name) {
		return fmt.Errorf("rotation name %q: use a-z, 0-9 and _ (at most 40)", r.Name)
	}
	if r.Class != "" && !knownClassName(r.Class) {
		return bad("class", "%q is not a class", r.Class)
	}
	if len(r.Replaces) == 0 {
		return bad("replaces", "name the strategies of the preset this takes the place of")
	}
	for _, s := range r.Replaces {
		if _, ok := strategyBuilders[s]; !ok || strings.HasPrefix(s, "rot:") {
			return bad("replaces", "%q is not a strategy", s)
		}
	}
	if n := len(r.Priorities); n == 0 || n > maxPriorities {
		return bad("priorities", "%d entries, use 1-%d", n, maxPriorities)
	}
	ids := map[string]bool{}
	for i, p := range r.Priorities {
		path := fmt.Sprintf("priorities[%d] (%s)", i, p.ID)
		if !rotationName(p.ID) {
			return bad(path, "id %q: use a-z, 0-9 and _", p.ID)
		}
		if ids[p.ID] {
			return bad(path, "id used twice")
		}
		ids[p.ID] = true
		if err := p.validate(r.Class); err != nil {
			return bad(path, "%v", err)
		}
	}
	for i, p := range r.Priorities {
		for _, a := range p.Alt {
			if !ids[a] || a == p.ID {
				return bad(fmt.Sprintf("priorities[%d] (%s)", i, p.ID), "alt %q is not another id of this file", a)
			}
		}
	}
	return nil
}

func (p *Priority) validate(class string) error {
	switch p.Kind {
	case "", "cast", "next_swing":
		if (p.Spell == "") == (p.SpellID == 0) {
			return errors.New("name the spell by name or by spell_id")
		}
	case "attack":
		if p.Spell != "" || p.SpellID != 0 {
			return errors.New("attack is the auto attack: no spell")
		}
	case "stance":
		if len(p.Stances) == 0 {
			return errors.New("stance needs stances (the home stances, the first the spellbook has)")
		}
		for _, s := range p.Stances {
			if _, ok := stanceSpells[s]; !ok {
				return fmt.Errorf("stance %q is not a warrior stance", s)
			}
		}
	default:
		return fmt.Errorf("kind %q: use cast, attack, next_swing or stance", p.Kind)
	}
	switch p.Target {
	case "", "fight", "melee_mob", "self":
	default:
		return fmt.Errorf("target %q: use fight, melee_mob or self", p.Target)
	}
	if p.Band != "" {
		if _, ok := bands[p.Band]; !ok {
			return fmt.Errorf("band %q: use low, normal, high, move or emergency", p.Band)
		}
	}
	switch p.Threat {
	case "", "single", "aoe", "none":
	default:
		return fmt.Errorf("threat %q: use single, aoe or none", p.Threat)
	}
	name := p.Spell
	if p.SpellID != 0 {
		if name = spells.Name(p.SpellID); name == "" {
			return fmt.Errorf("spell_id %d is not in the spell data", p.SpellID)
		}
	}
	if name != "" {
		ids := spells.IDsByName(name)
		if len(ids) == 0 {
			return fmt.Errorf("no spell is named %q", name)
		}
		if first := FirstRankLevels(name); p.Level > 0 && !p.Talent && !slices.Contains(first, p.Level) {
			return fmt.Errorf("%s: the first rank is learned at level %v in the spell data, the file says %d", name, first, p.Level)
		}
		if p.Level == 0 && !p.Talent && p.SpellID == 0 {
			return fmt.Errorf("%s: say the level of its first rank (level), or talent true", name)
		}
	}
	return p.When.validate()
}

// FirstRankLevels are the character levels at which the first rank of a spell is learned: the levels of the spells of that name with
// "Rank 1" in the spell data, or, for a name with no such spell (a talent, a spell without ranks), the levels of the rankless ones. The
// data holds creatures' copies of the same name too, which is why there can be more than one; a file's level must be one of them.
func FirstRankLevels(name string) []int {
	var ranked, rankless []int
	add := func(l *[]int, v int) {
		if !slices.Contains(*l, v) {
			*l = append(*l, v)
		}
	}
	for _, id := range spells.IDsByName(name) {
		m, ok := spellMeta(id)
		switch {
		case !ok || m.Level == 0:
		case m.Rank == 1:
			add(&ranked, int(m.Level))
		case m.Rank == 0:
			add(&rankless, int(m.Level))
		}
	}
	out := ranked
	if len(out) == 0 {
		out = rankless
	}
	sort.Ints(out)
	return out
}

func (w *Conditions) validate() error {
	switch w.State {
	case "", "combat", "rest", "any":
	default:
		return fmt.Errorf("when.state %q: use combat, rest or any", w.State)
	}
	switch w.After {
	case "", "dodge", "avoided":
	default:
		return fmt.Errorf("when.after %q: use dodge or avoided", w.After)
	}
	for _, s := range w.Stance {
		if _, ok := stanceSpells[s]; !ok {
			return fmt.Errorf("when.stance %q is not a warrior stance", s)
		}
	}
	auras := [][]string{w.SelfAura, w.NoSelfAura, w.SelfAuraMissing, w.MobAura, w.NoMobAura}
	if w.MobStacksBelow != nil {
		auras = append(auras, []string{w.MobStacksBelow.Aura})
		if w.MobStacksBelow.Stacks < 1 || w.MobStacksBelow.Stacks > 20 {
			return errors.New("when.mob_stacks_below.stacks: use 1-20")
		}
	}
	if w.MobsLacking != "" {
		auras = append(auras, []string{w.MobsLacking})
	}
	for _, l := range auras {
		for _, a := range l {
			if !spells.HasName(a) {
				return fmt.Errorf("when: no aura is named %q", a)
			}
		}
	}
	for name, v := range map[string]*int{"self_hp_below": w.SelfHPBelow, "self_hp_above": w.SelfHPAbove, "power_below": w.PowerBelow,
		"power_above": w.PowerAbove, "mob_hp_below": w.MobHPBelow, "mob_hp_above": w.MobHPAbove} {
		if v != nil && (*v < 0 || *v > 100) {
			return fmt.Errorf("when.%s: a percent, 0-100", name)
		}
	}
	if w.MobsInReach < 0 || w.MobsInCone < 0 || w.ComboMin < 0 || w.ComboMin > 5 || w.MobDistMin < 0 || w.MobDistMax < 0 {
		return errors.New("when: a count or a distance below zero (combo points go to 5)")
	}
	return nil
}

func shortSHA(data []byte) string {
	h := sha256.Sum256(data)
	return hex.EncodeToString(h[:])[:12]
}

func spellMeta(id uint32) (spells.Meta, bool) { return spells.Lookup(id) }

func knownClassName(n string) bool {
	for _, ci := range classTable {
		if ci.Name == n {
			return true
		}
	}
	return false
}

// ---- the registry ----

var (
	rotMu    sync.Mutex
	rotReg   = map[string]*Rotation{}
	rotBuilt sync.Once
)

// builtinRotations loads the files embedded in the binary, once.
func builtinRotations() {
	rotBuilt.Do(func() {
		ents, err := fs.ReadDir(builtinRotationFiles, "rotations")
		if err != nil {
			panic(err)
		}
		for _, e := range ents {
			data, err := builtinRotationFiles.ReadFile("rotations/" + e.Name())
			if err != nil {
				panic(err)
			}
			r, err := ParseRotation(data)
			if err != nil {
				panic(fmt.Sprintf("built-in rotation %s: %v", e.Name(), err))
			}
			rotReg[r.Name] = r
		}
	})
}

// RegisterRotation adds a rotation under its name; a name already taken (a built-in one too) is refused, so that "frost_mage" always means
// the same list.
func RegisterRotation(r *Rotation) error {
	builtinRotations()
	rotMu.Lock()
	defer rotMu.Unlock()
	if _, ok := rotReg[r.Name]; ok {
		return fmt.Errorf("rotation %q exists already: give the file another name", r.Name)
	}
	rotReg[r.Name] = r
	return nil
}

// LoadRotationFile reads, checks and registers one file.
func LoadRotationFile(path string) (*Rotation, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	r, err := ParseRotation(data)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	return r, RegisterRotation(r)
}

// LoadRotationDir registers every *.json file of a folder (settings rotations_dir); the first bad file stops it with its name.
func LoadRotationDir(dir string) ([]string, error) {
	ents, err := os.ReadDir(dir)
	if err != nil {
		return nil, err
	}
	var names []string
	for _, e := range ents {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".json") {
			continue
		}
		r, err := LoadRotationFile(filepath.Join(dir, e.Name()))
		if err != nil {
			return names, err
		}
		names = append(names, r.Name)
	}
	return names, nil
}

// RotationNames lists the registered rotations, sorted.
func RotationNames() []string {
	builtinRotations()
	rotMu.Lock()
	defer rotMu.Unlock()
	out := make([]string, 0, len(rotReg))
	for n := range rotReg {
		out = append(out, n)
	}
	sort.Strings(out)
	return out
}

// RotationNamed is the registered rotation of that name.
func RotationNamed(name string) (*Rotation, error) { return rotationNamed(name) }

func rotationNamed(name string) (*Rotation, error) {
	builtinRotations()
	name = strings.TrimPrefix(strings.ToLower(strings.TrimSpace(name)), "rot:")
	rotMu.Lock()
	r, ok := rotReg[name]
	rotMu.Unlock()
	if !ok {
		return nil, fmt.Errorf("rotation %q is unknown (known: %s)", name, strings.Join(RotationNames(), ", "))
	}
	return r, nil
}

// SHA is a short digest of the rotation file's bytes: two arms of a test name the same rotation only when the file is the same.
func (r *Rotation) SHA() string { return r.sha }

// apply takes the strategies the rotation replaces out of the list and puts its own in the place of the first of them. Another rotation
// file of the preset that replaces the same strategies goes the same way (fire_mage is a preset with a rotation file in it, and
// `rotation: frost_mage` takes its place). A rotation that replaces nothing of the preset is refused: it would play next to the
// preset's own class rotation.
func (r *Rotation) apply(flat []string) ([]string, error) {
	out := make([]string, 0, len(flat)+1)
	at := -1
	for _, s := range flat {
		replaced := slices.Contains(r.Replaces, s)
		if o, err := rotationNamed(s); strings.HasPrefix(s, "rot:") && err == nil {
			replaced = slices.ContainsFunc(o.Replaces, func(x string) bool { return slices.Contains(r.Replaces, x) })
		}
		if replaced {
			if at < 0 {
				at = len(out)
			}
			continue
		}
		out = append(out, s)
	}
	if at < 0 {
		return nil, fmt.Errorf("rotation %q replaces %s, and the strategies %v have none of them: it is for another class or spec", r.Name, strings.Join(r.Replaces, ", "), flat)
	}
	return slices.Insert(out, at, "rot:"+r.Name), nil
}

// ---- the strategy ----

// strategy builds the rotation's strategy.
func (r *Rotation) strategy() *Strategy {
	s := &Strategy{Name: "rot:" + r.Name}
	var swings []string
	for _, p := range r.Priorities {
		if p.Kind == "next_swing" {
			swings = append(swings, p.ID)
		}
	}
	n := len(r.Priorities)
	for i := range r.Priorities {
		p := r.Priorities[i]
		rel := bands[orDefault(p.Band, "normal")] + float32(n-i)*0.01
		s.Actions = append(s.Actions, p.action(swings))
		s.Rules = append(s.Rules, Rule{Trigger: Trigger{Name: p.ID, Active: p.trigger()}, Do: []Next{{p.ID, rel}}})
	}
	return s
}

func orDefault(s, d string) string {
	if s == "" {
		return d
	}
	return s
}

func (p *Priority) selfTarget() bool { return p.Target == "self" }

// action builds the entry's action.
func (p *Priority) action(swings []string) *Action {
	harmful := !p.selfTarget()
	if p.Harmful != nil {
		harmful = *p.Harmful
	}
	threat := ThreatNone
	switch p.Threat {
	case "single":
		threat = ThreatSingle
	case "aoe":
		threat = ThreatAoE
	case "":
		if harmful {
			threat = ThreatSingle
		}
	}
	switch p.Kind {
	case "attack":
		return swingAction(p.ID, fightMob)
	case "stance":
		return homeStanceAction(p.ID, p.Stances...)
	}
	a := &Action{Name: p.ID, Spell: p.Spell, Harmful: harmful, Threat: threat, Alt: p.Alt}
	if p.SpellID != 0 {
		id := p.SpellID
		a.Resolve = func(c *Ctx) (uint32, bool) { return id, c.Spells.Known(id) }
	}
	switch p.Target {
	case "", "fight":
		a.Target = fightTarget
	case "melee_mob":
		a.Target = meleeMobGUID
	}
	if !p.selfTarget() && harmful {
		a.Pre = []string{"face_target"}
	}
	if p.Kind == "next_swing" {
		id := p.ID
		a.Do = func(ctx context.Context, c *Ctx) error {
			sid, ok := a.spell(c)
			if !ok {
				return nil
			}
			// A strike that replaces the next swing is sent once per swing; the others of its kind replace each other on the queue.
			for _, n := range swings {
				c.mem.holdAction(n, c.Now.Add(hsHold), "queued")
			}
			c.mem.holdAction(id, c.Now.Add(hsHold), "queued")
			return c.cast(ctx, sid, fightTarget(c))
		}
	}
	return a
}

// trigger builds the condition function of the entry.
func (p *Priority) trigger() func(*Ctx) bool {
	w := p.When
	state := w.State
	if state == "" {
		state = "combat"
		if p.selfTarget() {
			state = "any"
		}
	}
	if p.Kind == "stance" {
		off := offHome(p.Stances...)
		return func(c *Ctx) bool { return off(c) && w.holds(c) }
	}
	melee := p.Kind == "attack" || p.Kind == "next_swing" || p.Target == "melee_mob"
	return func(c *Ctx) bool {
		if !c.SelfOK {
			return false
		}
		switch state {
		case "combat":
			if fightMob(c) == nil && !(p.Target == "melee_mob" && ValMeleeMob.Get(c) != nil) {
				return false
			}
		case "rest":
			if !restNow(c) {
				return false
			}
		}
		if melee && p.Target != "melee_mob" && !mobInReach(c) {
			return false
		}
		return w.holds(c)
	}
}

// mobOf is the mob the mob_* conditions look at: the one on the player when melee_mob is set, else the fight's.
func (w *Conditions) mobOf(c *Ctx) *objects.Unit {
	if w.MeleeMob {
		return ValMeleeMob.Get(c)
	}
	return fightMob(c)
}

func (w *Conditions) holds(c *Ctx) bool {
	mob := w.mobOf(c)
	if w.MeleeMob && mob == nil {
		return false
	}
	if w.MobInReach && !mobInReach(c) {
		return false
	}
	if w.MobsInReach > 0 && ValMeleeMobs.Get(c) < w.MobsInReach {
		return false
	}
	if w.MobsInCone > 0 && ValConeMobs.Get(c) < w.MobsInCone {
		return false
	}
	if w.MobDistMin > 0 || w.MobDistMax > 0 {
		m := fightMob(c)
		if m == nil || m.Distance < 0 || (w.MobDistMin > 0 && m.Distance < w.MobDistMin) || (w.MobDistMax > 0 && m.Distance > w.MobDistMax) {
			return false
		}
	}
	if w.MobOnMe && ValMobOnMe.Get(c) == nil {
		return false
	}
	if w.TankHolds && c.P.Role != RoleTank {
		if f := ValFight.Get(c); f.Tank == nil && f.Source != "self" {
			return false
		}
	}
	if w.Feared && !feared(c) {
		return false
	}
	if w.MobFleeing && !fleeing(fightMob(c)) {
		return false
	}
	if w.Moving != nil && c.Self.Moving != *w.Moving {
		return false
	}
	if w.InCombat != nil && c.Self.InCombat != *w.InCombat {
		return false
	}
	switch w.After {
	case "dodge":
		if !dodgedRecently(c) {
			return false
		}
	case "avoided":
		if !avoidedRecently(c) {
			return false
		}
	}
	if w.ComboMin > 0 && comboOnMob(c) < w.ComboMin {
		return false
	}
	if len(w.Stance) > 0 && !InStance(&c.Self, w.Stance...) {
		return false
	}
	pct := func(v *int, got int, below bool) bool {
		return v == nil || (below && got < *v) || (!below && got > *v)
	}
	if !pct(w.SelfHPBelow, healthPct(c), true) || !pct(w.SelfHPAbove, healthPct(c), false) ||
		!pct(w.PowerBelow, PowerPct(c), true) || !pct(w.PowerAbove, PowerPct(c), false) {
		return false
	}
	if w.MobHPBelow != nil || w.MobHPAbove != nil {
		if mob == nil || !pct(w.MobHPBelow, mobHealthPct(mob), true) || !pct(w.MobHPAbove, mobHealthPct(mob), false) {
			return false
		}
	}
	if len(w.SelfAura) > 0 && seenAura(&c.Self, w.SelfAura...) == "" {
		return false
	}
	if len(w.NoSelfAura) > 0 && seenAura(&c.Self, w.NoSelfAura...) != "" {
		return false
	}
	if len(w.SelfAuraMissing) > 0 && !auraMissing(&c.Self, w.SelfAuraMissing...) {
		return false
	}
	if len(w.MobAura) > 0 && (mob == nil || seenAura(mob, w.MobAura...) == "") {
		return false
	}
	if len(w.NoMobAura) > 0 && (mob == nil || seenAura(mob, w.NoMobAura...) != "") {
		return false
	}
	if s := w.MobStacksBelow; s != nil {
		if mob == nil || stacksOn(mob, s.Aura) >= s.Stacks {
			return false
		}
	}
	if w.MobsLacking != "" && mobsLacking(c, w.MobsLacking) == 0 {
		return false
	}
	return true
}

// stacksOn is how many stacks of the aura the unit carries.
func stacksOn(u *objects.Unit, aura string) int {
	for _, a := range u.Auras {
		if strings.EqualFold(a.Name, aura) {
			return max(a.Stacks, 1)
		}
	}
	return 0
}

// ---- strategy lookup for "rot:<name>" ----

// rotationStrategy builds the strategy of a registered rotation named in a strategy list ("rot:frost_mage").
func rotationStrategy(name string) (*Strategy, error) {
	r, err := rotationNamed(name)
	if err != nil {
		return nil, err
	}
	return r.strategy(), nil
}

// checkRotationActions refuses a rotation whose action ids another active strategy defines too: the engine keys actions by name, so
// one would silently replace the other.
func checkRotationActions(strats []*Strategy) error {
	owner := map[string]string{}
	for _, s := range strats {
		if strings.HasPrefix(s.Name, "rot:") {
			continue
		}
		for _, a := range s.Actions {
			owner[a.Name] = s.Name
		}
	}
	for _, s := range strats {
		if !strings.HasPrefix(s.Name, "rot:") {
			continue
		}
		for _, a := range s.Actions {
			if o, ok := owner[a.Name]; ok {
				return fmt.Errorf("%s: action id %q is also an action of strategy %s: rename it in the file", s.Name, a.Name, o)
			}
		}
	}
	return nil
}

// ---- spell data for show ----

// RankAt is the spell id of the rank of a spell name that a character of that level casts: the highest rank whose level is at most
// the character's, 0 when none is. Spells that share the name but have no rank (passives, talents' own effects) are passed over.
func RankAt(name string, level int) uint32 {
	var best, firstRankless uint32
	bestRank, ranked := -1, false
	for _, id := range spells.IDsByName(name) { // lowest id first
		m, ok := spellMeta(id)
		if !ok || m.Level == 0 {
			continue
		}
		if m.Rank == 0 {
			if firstRankless == 0 && int(m.Level) <= level {
				firstRankless = id
			}
			continue
		}
		ranked = true
		if int(m.Level) <= level && int(m.Rank) > bestRank {
			best, bestRank = id, int(m.Rank)
		}
	}
	if !ranked {
		return firstRankless // a spell without ranks: the data's first copy of the name
	}
	return best
}

// Describe lists the rotation, highest first, with the spell cast at the level (0: the entry alone).
func (r *Rotation) Describe(level int) string {
	var b strings.Builder
	fmt.Fprintf(&b, "rotation %s (%s %s) sha %s, replaces %s\n", r.Name, r.Class, r.Spec, r.sha, strings.Join(r.Replaces, ", "))
	for i, p := range r.Priorities {
		spell := p.Spell
		if p.SpellID != 0 {
			spell = fmt.Sprintf("#%d %s", p.SpellID, spells.Name(p.SpellID))
		}
		if spell == "" {
			spell = "(" + orDefault(p.Kind, "cast") + ")"
		}
		fmt.Fprintf(&b, "%2d. %-22s %-24s band %-9s target %-9s", i+1, p.ID, spell, orDefault(p.Band, "normal"), orDefault(p.Target, "fight"))
		if level > 0 && p.Spell != "" {
			switch id := RankAt(p.Spell, level); {
			case id != 0:
				m, _ := spellMeta(id)
				fmt.Fprintf(&b, " level %d: id %d rank %d", level, id, m.Rank)
			case p.Talent:
				fmt.Fprintf(&b, " level %d: talent", level)
			default:
				fmt.Fprintf(&b, " level %d: not learned yet (level %d)", level, p.Level)
			}
		}
		b.WriteByte('\n')
	}
	return b.String()
}
