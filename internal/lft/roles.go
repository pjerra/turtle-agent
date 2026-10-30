package lft

// Class ids as UNIT_FIELD_BYTES_0 has them (the 1.12 ChrClasses ids; 6 and 10 do not exist).
const (
	classWarrior uint8 = 1
	classPaladin uint8 = 2
	classHunter  uint8 = 3
	classRogue   uint8 = 4
	classPriest  uint8 = 5
	classShaman  uint8 = 7
	classMage    uint8 = 8
	classWarlock uint8 = 9
	classDruid   uint8 = 11
)

// Role bits of LFTRoles (LFTMgr.h:17-22).
const (
	RoleTank   uint8 = 0x01
	RoleHealer uint8 = 0x02
	RoleDamage uint8 = 0x04
)

// AllowedRoles is the core's AllowedRoleMask for a class, as the roles text it accepts
// (LFTQeueue.cpp:717-736): druid, paladin and shaman may tank, heal and damage; a warrior
// tanks and damages; a priest heals and damages; hunter, mage, rogue and warlock damage.
// An empty string means no role is allowed (the core's default).
func AllowedRoles(class uint8) string {
	switch class {
	case classDruid, classPaladin, classShaman:
		return "thd"
	case classWarrior:
		return "td"
	case classPriest:
		return "hd"
	case classHunter, classMage, classRogue, classWarlock:
		return "d"
	}
	return ""
}

// Mask is the core's ParseRoleMask: t, h and d in either case, anything else ignored.
func Mask(roles string) uint8 {
	var mask uint8
	for i := range roles {
		switch roles[i] {
		case 't', 'T':
			mask |= RoleTank
		case 'h', 'H':
			mask |= RoleHealer
		case 'd', 'D':
			mask |= RoleDamage
		}
	}
	return mask
}
