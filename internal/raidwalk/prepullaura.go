// prepullaura.go: the `--prepull-aura` flag of raid-walk (repeatable, test servers only): the aura lists the
// commander puts on the living party at the pre-pull rest before each boss row, so a walk tests gear and preparation
// that a real raid brings into the instance.
//
//	--prepull-aura role=<role>:<id,id,...|preset,...>
//	--prepull-aura <file.json>
//
// <role> is tank, melee, ranged, caster, healer or all (every living member), and <ids> is a comma list of spell ids
// and dungeon.BuffPresets names; "world" is the world-raid preset (the classic world buffs a Turtle raid keeps,
// Warchief's Blessing left out: it is Horde-only). A value that is not a role= list is a `tb-prepull-auras` file
// (dungeons/prepull/zg-preraid.json): one list per role under "roles", so a walk names the file instead of repeating
// six values. A role named twice is refused (from two values or from a file and a value), and so is a role whose
// list would pass dungeon.MaxBuffs (the aura slots a bot has).
package raidwalk

import (
	"encoding/json"
	"fmt"
	"os"
	"slices"
	"strconv"
	"strings"

	"github.com/pjerra/turtle-agent/internal/dungeon"
)

// The role words --prepull-aura takes: the aura role a bot plays, or "all" for every living member.
const (
	PrepullTank   = "tank"
	PrepullMelee  = "melee"
	PrepullRanged = "ranged"
	PrepullCaster = "caster"
	PrepullHealer = "healer"
	PrepullAll    = "all"
)

// PrepullRoles are the role words in the flag's own order ("all" last: it applies to everyone).
var PrepullRoles = []string{PrepullTank, PrepullMelee, PrepullRanged, PrepullCaster, PrepullHealer, PrepullAll}

// PrepullWorld is what the flag's free "world" token expands to: dungeon.BuffPresets' world-raid list (the world buffs
// a Turtle raid keeps; Warchief's Blessing is Horde-only and left out).
const PrepullWorld = "world-raid"

// PrepullAura is one --prepull-aura value: the role and the aura list, resolved through dungeon.ResolveBuffs.
type PrepullAura struct {
	Role  string         `json:"role"`
	Buffs []dungeon.Buff `json:"buffs"`
}

// PrepullAuras is the parsed flag set, in flag order.
type PrepullAuras []PrepullAura

// ParsePrepullAura reads one --prepull-aura value. A preset the dungeon presets do not know and spell id 0 are
// refused with the resolver's own words.
func ParsePrepullAura(v string) (PrepullAura, error) {
	v = strings.TrimSpace(v)
	if len(v) < len("role=") || !strings.EqualFold(v[:len("role=")], "role=") {
		return PrepullAura{}, fmt.Errorf("prepull-aura %q: want \"role=<role>:<ids>\", with role one of %s", v, strings.Join(PrepullRoles, ", "))
	}
	rest := v[len("role="):]
	role, list, ok := strings.Cut(rest, ":")
	if !ok {
		return PrepullAura{}, fmt.Errorf("prepull-aura %q: want \"role=<role>:<ids>\" after the role", v)
	}
	role = strings.ToLower(strings.TrimSpace(role))
	if !slices.Contains(PrepullRoles, role) {
		return PrepullAura{}, fmt.Errorf("prepull-aura %q: role %q is not one of %s", v, role, strings.Join(PrepullRoles, ", "))
	}
	var presets []string
	var ids []uint32
	for _, word := range strings.Split(list, ",") {
		word = strings.ToLower(strings.TrimSpace(word))
		if word == "" {
			return PrepullAura{}, fmt.Errorf("prepull-aura %q: an empty id", v)
		}
		if n, err := strconv.ParseUint(word, 10, 32); err == nil {
			ids = append(ids, uint32(n))
			continue
		}
		if word == "world" {
			word = PrepullWorld
		}
		presets = append(presets, word)
	}
	buffs, err := dungeon.ResolveBuffs(presets, ids)
	if err != nil {
		return PrepullAura{}, fmt.Errorf("prepull-aura %q: %w", v, err)
	}
	return PrepullAura{Role: role, Buffs: buffs}, nil
}

// PrepullFileFormat is the format string a --prepull-aura file names ("tb-prepull-auras": dungeons/prepull/
// zg-preraid.json).
const PrepullFileFormat = "tb-prepull-auras"

// prepullAuraFileDoc is the file form's JSON: the format, the version and one {ids} list per aura role. The sample
// file's other keys ("what", "note", "verified", "raid_buff_strip") are documentation and are not read.
type prepullAuraFileDoc struct {
	Format  string                    `json:"format"`
	Version int                       `json:"version"`
	Roles   map[string]prepullAuraIDs `json:"roles"`
}

// prepullAuraIDs is one role's list in a --prepull-aura file.
type prepullAuraIDs struct {
	IDs []uint32 `json:"ids"`
}

// PlanPrepullAuras parses every flag value, refuses a role named twice (it would be ambiguous which list wins), and
// checks that no role's merged list passes the aura slots a bot has. A value that is not a role= list names a
// tb-prepull-auras file, whose roles join the set in PrepullRoles order.
func PlanPrepullAuras(values []string) (PrepullAuras, error) {
	var out PrepullAuras
	named := map[string]bool{}
	add := func(list []PrepullAura) error {
		for _, a := range list {
			if named[a.Role] {
				return fmt.Errorf("prepull-aura: role %q is named twice: one list per role", a.Role)
			}
			named[a.Role] = true
			out = append(out, a)
		}
		return nil
	}
	for _, v := range values {
		if isPrepullAuraValue(v) {
			a, err := ParsePrepullAura(v)
			if err != nil {
				return nil, err
			}
			if err := add([]PrepullAura{a}); err != nil {
				return nil, err
			}
			continue
		}
		path := strings.TrimSpace(v)
		data, err := os.ReadFile(path)
		if err != nil {
			return nil, fmt.Errorf("prepull-aura %q: %w (want \"role=<role>:<ids>\" or a %s file)", v, err, PrepullFileFormat)
		}
		list, err := ParsePrepullAuraFile(path, data)
		if err != nil {
			return nil, err
		}
		if err := add(list); err != nil {
			return nil, err
		}
	}
	for _, role := range PrepullRoles {
		if n := len(out.For(role)); n > dungeon.MaxBuffs {
			return nil, fmt.Errorf("prepull-aura: a %s bot would carry %d auras, at most %d (its aura slots)", role, n, dungeon.MaxBuffs)
		}
	}
	return out, nil
}

// isPrepullAuraValue tells a role= list from a file path: only the role= form starts with "role=".
func isPrepullAuraValue(v string) bool {
	v = strings.TrimSpace(v)
	return len(v) >= len("role=") && strings.EqualFold(v[:len("role=")], "role=")
}

// ParsePrepullAuraFile reads a tb-prepull-auras file's lists: one PrepullAura per role the file names, in
// PrepullRoles order, so the same file always resolves to the same flag set. The format and version are checked, an
// unknown role word, a role with no ids and an id ResolveBuffs refuses are errors: a file the walk cannot read whole
// is no preparation.
func ParsePrepullAuraFile(name string, data []byte) ([]PrepullAura, error) {
	var doc prepullAuraFileDoc
	if err := json.Unmarshal(data, &doc); err != nil {
		return nil, fmt.Errorf("prepull-aura %s: %v", name, err)
	}
	if doc.Format != PrepullFileFormat {
		return nil, fmt.Errorf("prepull-aura %s: format %q, want %q", name, doc.Format, PrepullFileFormat)
	}
	if doc.Version != 1 {
		return nil, fmt.Errorf("prepull-aura %s: version %d, want 1", name, doc.Version)
	}
	for role := range doc.Roles {
		if !slices.Contains(PrepullRoles, role) {
			return nil, fmt.Errorf("prepull-aura %s: role %q is not one of %s", name, role, strings.Join(PrepullRoles, ", "))
		}
	}
	var out []PrepullAura
	for _, role := range PrepullRoles {
		r, ok := doc.Roles[role]
		if !ok {
			continue
		}
		if len(r.IDs) == 0 {
			return nil, fmt.Errorf("prepull-aura %s: role %q names no id", name, role)
		}
		buffs, err := dungeon.ResolveBuffs(nil, r.IDs)
		if err != nil {
			return nil, fmt.Errorf("prepull-aura %s: role %q: %w", name, role, err)
		}
		out = append(out, PrepullAura{Role: role, Buffs: buffs})
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("prepull-aura %s: the file names no role (want one of %s)", name, strings.Join(PrepullRoles, ", "))
	}
	return out, nil
}

// For is the aura list a bot of aura role role gets: every "all" list and the role's own, without duplicates, in flag
// order. A bot whose role is unknown ("") gets the "all" lists only.
func (a PrepullAuras) For(role string) []dungeon.Buff {
	var out []dungeon.Buff
	for _, v := range a {
		if v.Role != PrepullAll && (role == "" || v.Role != role) {
			continue
		}
		for _, b := range v.Buffs {
			if !slices.ContainsFunc(out, func(x dungeon.Buff) bool { return x.ID == b.ID }) {
				out = append(out, b)
			}
		}
	}
	return out
}

// classNames is the class name of the class ids UNIT_FIELD_BYTES_0 carries (1 warrior ... 11 druid); 6 (the death
// knight) and 10 (unused) have none, as in the aura role table below.
var classNames = map[uint8]string{
	1: "warrior", 2: "paladin", 3: "hunter", 4: "rogue", 5: "priest",
	7: "shaman", 8: "mage", 9: "warlock", 11: "druid",
}

// ClassName is the class name of a class id, "" for an id no class has.
func ClassName(class uint8) string { return classNames[class] }

// hybridSpecRoles is the aura role a dps spec of a class that can be more than one of melee, caster and healer gives
// it: a feral druid (cat, bear) and an enhancement shaman are melee, a balance or boomkin druid and an elemental shaman
// are casters. A spec absent here (restoration, or none) gives no role: the fire fails closed rather than guess.
var hybridSpecRoles = map[string]map[string]string{
	"druid":  {"cat": PrepullMelee, "bear": PrepullMelee, "balance": PrepullCaster, "boomkin": PrepullCaster},
	"shaman": {"enh": PrepullMelee, "ele": PrepullCaster},
}

// HireAuraRole is the aura role of one of the walk's own hires, from the hire data the walk holds rather than the
// class id alone (walk 100's cold review: the class table put a ret paladin, a feral druid, an enhancement shaman and a
// shadow priest on the HEALER list).
//
//	batchRole — the roster batch's role: mt and ot are tanks whatever the class.
//	tokenRole — the role the batch's hire grammar asked of the class (Batch.TokenRole: tank, healer or dps).
//	class     — the class name (ClassName), from the class id the fire reads.
//	spec      — the class-spec id a version-2 roster set on the batch ("" when it names none).
//
// A tank is a tank and a healer is a healer whatever its class. A dps is melee (warrior, rogue, paladin), ranged
// (hunter) or caster (mage, warlock, priest); a dps druid or shaman needs its spec, and "" comes back whenever the
// walk cannot tell the role — an unknown token role or a hybrid spec that is not named. The caller fails closed on
// "", never as an "all"-only bot.
func HireAuraRole(batchRole, tokenRole, class, spec string) string {
	if batchRole == RoleMT || batchRole == RoleOT {
		return PrepullTank
	}
	switch tokenRole {
	case "tank":
		return PrepullTank
	case "healer":
		return PrepullHealer
	case "dps":
	default:
		return ""
	}
	switch class {
	case "warrior", "rogue", "paladin":
		return PrepullMelee
	case "hunter":
		return PrepullRanged
	case "mage", "warlock", "priest":
		return PrepullCaster
	}
	return hybridSpecRoles[class][spec]
}

// PrepullIDs is the ids of an aura list, in order (the report's form).
func PrepullIDs(list []dungeon.Buff) []uint32 {
	out := make([]uint32, len(list))
	for i, b := range list {
		out[i] = b.ID
	}
	return out
}
