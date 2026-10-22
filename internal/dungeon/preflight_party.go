package dungeon

import (
	"fmt"
	"strings"

	"github.com/pjerra/turtle-agent/internal/packet"
)

// The party-strength half of the pre-run check (issue #266): the tank below the plan's tank_min_hp, and a hired bot of the other team than
// the party's. Both are warnings; a warning fails the run only with RemoteOptions.Preflight "strict". Neither sees what stands around the
// hire spot or the portal (the observer's duty): the check reads only the first frame's party and the units in the agent's object store.

// BotTeamer is the optional part of a RemoteGame that tells a bot's team from its race: "alliance", "horde", or "" when the unit is not in
// view. The feed carries no race; the agent's object store has each visible unit's race byte (UNIT_FIELD_BYTES_0).
type BotTeamer interface {
	BotTeam(guid packet.GUID) string
}

// checkPartyStrength adds the party warnings the plan and the visible units allow before the first step.
func (d *RemoteDriver) checkPartyStrength(pf *Preflight) {
	name := d.opt.Tank
	if name == "" {
		name = d.opt.Lead
	}
	if m, ok := d.memberByName(name); ok && d.plan.TankMinHP > 0 && m.MaxHP > 0 && m.MaxHP < d.plan.TankMinHP {
		pf.Warnings = append(pf.Warnings, fmt.Sprintf("%s: the tank's max health is %d, the plan wants at least %d for its level band (weak gear: autogear again, or hire another tank)", m.Name, m.MaxHP, d.plan.TankMinHP))
	}
	bt, ok := d.g.(BotTeamer)
	if !ok {
		return
	}
	team := d.opt.Team
	if team == "" {
		if ts, ok := d.g.(TeamSource); ok {
			team = ts.Team()
		}
	}
	if team == "" {
		return
	}
	for _, m := range d.fr.Party {
		botTeam := bt.BotTeam(m.GUID)
		if botTeam != "" && !strings.EqualFold(botTeam, team) {
			pf.Warnings = append(pf.Warnings, fmt.Sprintf("%s: is %s but the party's team is %s (a hire of the other faction: keep it out of enemy towns, hire and enter at the dungeon door)", m.Name, botTeam, team))
		}
	}
}
