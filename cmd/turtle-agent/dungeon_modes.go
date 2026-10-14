package main

import (
	"fmt"
	"strings"

	"github.com/pjerra/turtle-agent/internal/brain"
	"github.com/pjerra/turtle-agent/internal/dungeon"
	"github.com/pjerra/turtle-agent/internal/rpc"
)

// Run modes (DESIGN-DUNGEON-MODES, phase P2): one option the owner picks, `dungeon_run {"mode": ...}`.
//
//	commander  the agent leads a bot group from outside it through the TortoiseBots remote commander (the old "remote").
//	leader     the agent's character is in the group, leads it and plays: the bot brain is on (the old "group" with a brain).
//	observe    a TortoiseBots run leader clears the dungeon and the agent watches and scores it: needs TortoiseBots' phase P3.
const (
	modeCommander = "commander"
	modeLeader    = "leader"
	modeObserve   = "observe"
)

// observeRefusal is what observe mode answers until the TortoiseBots run leader (phase P3) exists.
const observeRefusal = `mode "observe" needs the TortoiseBots run leader (phase P3 of the dungeon-modes design): a party of bots that clears the dungeon by itself, with the agent watching. It does not exist yet: use "commander" (the agent leads bots from outside) or "leader" (the agent's character leads and plays)`

// runMode names the mode of the params: "remote"/"commander" and "group"/"leader" are the same two modes; an empty mode is the older group
// mode (the brain is optional there, it is not in leader mode). observe is its own.
func runMode(mode string) (canonical string, explicit bool, err error) {
	switch strings.ToLower(strings.TrimSpace(mode)) {
	case "":
		return modeLeader, false, nil
	case "group":
		return modeLeader, false, nil
	case modeLeader:
		return modeLeader, true, nil
	case "remote", modeCommander:
		return modeCommander, true, nil
	case modeObserve:
		return modeObserve, true, &rpc.Error{Code: rpc.CodeInvalidParams, Message: observeRefusal}
	}
	return "", false, &rpc.Error{Code: rpc.CodeInvalidParams, Message: `mode must be "commander", "observe" or "leader" ("remote" and "group" are the older names of commander and leader)`}
}

// leaderPresets is the preset the agent's character plays in leader mode when the run names none: one per class and role. The role is
// "dps" unless the run says otherwise; a class without a preset for the role is refused with the list.
var leaderPresets = map[uint8]map[string]string{
	brain.ClassWarrior: {"dps": "arms_warrior", "tank": "tank_warrior"},
	brain.ClassPaladin: {"dps": "ret_paladin", "healer": "holy_paladin"},
	brain.ClassHunter:  {"dps": "marksman_hunter"},
	brain.ClassRogue:   {"dps": "combat_rogue"},
	brain.ClassPriest:  {"dps": "shadow_priest", "healer": "holy_priest"},
	brain.ClassShaman:  {"dps": "enhancement_shaman", "healer": "resto_shaman"},
	brain.ClassMage:    {"dps": "frost_mage"},
	brain.ClassWarlock: {"dps": "affliction_warlock"},
	brain.ClassDruid:   {"dps": "feral_cat_druid", "tank": "bear_tank_druid", "healer": "resto_druid"},
}

// leaderPreset is the default preset of a class and role.
func leaderPreset(class uint8, role string) (string, error) {
	role = strings.ToLower(strings.TrimSpace(role))
	if role == "" {
		role = "dps"
	}
	rows, ok := leaderPresets[class]
	if !ok {
		return "", fmt.Errorf("leader mode: the character's class (%d) is not known to the brain: pass brain", class)
	}
	if p, ok := rows[role]; ok {
		return p, nil
	}
	var have []string
	for r := range rows {
		have = append(have, r)
	}
	return "", fmt.Errorf("leader mode: %s has no default preset for the role %q (it has %s): pass brain", brain.ClassOf(class).Name, role, strings.Join(sortedStrings(have), ", "))
}

func sortedStrings(s []string) []string {
	out := append([]string{}, s...)
	for i := 1; i < len(out); i++ {
		for j := i; j > 0 && out[j] < out[j-1]; j-- {
			out[j], out[j-1] = out[j-1], out[j]
		}
	}
	return out
}

// planEncounters are the plan's bosses that carry tactics, as the brain needs them: the boss entry and the units killed before it
// switch an encounter on.
func planEncounters(plan *dungeon.Plan) []brain.Encounter {
	var out []brain.Encounter
	for _, l := range plan.Legs {
		b := l.Boss
		if b == nil || b.Tactics.Empty() {
			continue
		}
		e := brain.Encounter{Name: plan.Name + "/" + l.Name, Entries: append([]uint32{b.Entry}, b.First...), Tactics: *b.Tactics}
		out = append(out, e)
	}
	return out
}

// selfClass is the agent's character's class id, 0 when its unit row is not known yet.
func (r *dungeonRunner) selfClass() uint8 {
	s := r.cur()
	u, err := s.Unit(s.Status().GUID, "")
	if err != nil {
		return 0
	}
	return u.Class
}

// brainLabel is how the report and the trace name what played the leader: the preset, and the rotation with its file's digest.
func brainLabel(preset, rotation string) (string, error) {
	if rotation == "" {
		return preset, nil
	}
	r, err := brain.RotationNamed(rotation)
	if err != nil {
		return "", err
	}
	return preset + "+rot:" + r.Name + "@" + r.SHA(), nil
}
