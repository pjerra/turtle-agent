package dungeon

import "github.com/pjerra/turtle-agent/internal/objects"

// StealthSpells are the auras that hide a unit from the party's companions: they never attack what they cannot see,
// so a pull of such a unit draws no fight. 5916 is Shadowstalker Stealth, which the Uldaman Cave Lurkers (4850) carry
// from their spawn (creature_addon.auras); 22766 is Sneak, which their creature AI casts when they spawn.
var StealthSpells = map[uint32]bool{
	1784: true, 1785: true, 1786: true, 1787: true, // Stealth
	8822: true, 5916: true, 22766: true, // Stealth (NPC), Shadowstalker Stealth, Sneak
	5215: true, 6783: true, 9913: true, // Prowl
	20580: true, // Shadowmeld
}

// StealthedAuras reports whether any spell id of auras is a stealth aura.
func StealthedAuras(ids []uint32) bool {
	for _, id := range ids {
		if StealthSpells[id] {
			return true
		}
	}
	return false
}

// Stealthed reports a unit that carries one of StealthSpells. It goes by spell id only: names would also catch
// "Stealth Detection" (8279, 18950 ...), an aura many ordinary spawns have, and "Sneaky".
func Stealthed(u objects.Unit) bool {
	for _, a := range u.Auras {
		if StealthSpells[a.SpellID] {
			return true
		}
	}
	return false
}
