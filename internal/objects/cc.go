package objects

import (
	"strings"

	"github.com/pjerra/turtle-agent/internal/spells"
)

// ccMechanics are the spell mechanics (SpellDefines.h Mechanics) of the crowd control that damage breaks and that a party
// leaves alone: sleep (10), freeze (13, Freezing Trap), knockout (14, Sap), polymorph (17), banish (18), shackle (20) and turn (23; 22 is persuade). Stun, fear and root are
// left out: they do not need the mob spared.
var ccMechanics = map[uint8]bool{10: true, 13: true, 14: true, 17: true, 18: true, 20: true, 23: true}

// ccAuraNames are the names of the same auras (also with a rank or a variant after a space or colon), for a spell the table does not carry or gives no mechanic.
var ccAuraNames = []string{"Polymorph", "Sap", "Shackle Undead", "Hibernate", "Banish", "Freezing Trap Effect", "Turn Undead", "Wyvern Sting", "Seduction"}

// notCC are spells that share a mechanic with crowd control but are no sheep: Gouge is a knockout (mechanic 14) of 4 s that the rogue
// opens a fight with and that the party should keep hitting.
var notCC = map[string]bool{"Gouge": true}

// CrowdControlled says whether the unit carries an aura of a party's crowd control (polymorph, sap, trap, shackle, sleep,
// banish, turn undead): the mob is meant to stay out of the fight until the party breaks it.
func (u Unit) CrowdControlled() bool {
	for _, a := range u.Auras {
		name := a.Name
		if name == "" {
			name = spells.Name(a.SpellID)
		}
		if notCC[name] {
			continue
		}
		if m, ok := spells.Lookup(a.SpellID); ok && ccMechanics[m.Mechanic] {
			return true
		}
		for _, n := range ccAuraNames {
			if name == n || strings.HasPrefix(name, n+" ") || strings.HasPrefix(name, n+":") {
				return true
			}
		}
	}
	return false
}
