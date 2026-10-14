package dungeon

import (
	"context"
	"fmt"
	"strings"

	"github.com/pjerra/turtle-agent/internal/tbm"
)

// Ritual steps in remote mode (TortoiseBots `cmd ritual`, player-controls.md section 1a). A summoning ritual object (game object type 18:
// Uldaman's Altar of the Keepers and Altar of Archaedas) fires when `reqParticipants` DIFFERENT players of one group have used it and stand
// still while it channels. The carrier alone cannot do that, so the step orders
//
//	cmd ritual entry:<id>@x,y,z <carrier> <bot> <bot>
//
// and waits for its one final answer: the module walks the named bots to the object, has them use it in turn, and keeps them still until
// the object is active and its cast has run (`TBM:ACTION_ACK|cmd ritual|party|3|done`), or answers an ERR (`not-enough`, `interrupted`,
// `in-combat`, `too-far`, `not-found`, `busy`, ...). The plan's following wait_for (the altar active, the door open) checks what the
// ritual did; the order's ACK says the bots did their part, not that the object's script reacted.
//
// The bots are the carrier first (it uses first), then the party's bots by how little they are needed elsewhere: DPS, then the healer, then
// the tank, the lead bot last (the party follows it, so it should keep its place). The group does not change: no bot is invited or sent away.

// ritualTries is how often the order is given when it ends `interrupted` (a participant moved or was hit): the module has let every bot go
// and stopped their channel by then, so the object is not left half used. Any other refusal stands.
const ritualTries = 2

// ritualMember is one bot of the party as the roster sees it.
type ritualMember struct {
	Name   string
	Lead   bool
	Tank   bool
	Healer bool
	Dead   bool
}

// PickRitualBots names n living bots for a ritual: the carrier first, then the rest by need (DPS, healer, tank, lead bot), each group in
// party order. It fails, naming how many there are, when the party has fewer than n living bots or the carrier is not among them.
func PickRitualBots(party []ritualMember, carrier string, n int) ([]string, error) {
	out := make([]string, 0, n)
	living := 0
	haveCarrier := false
	for _, m := range party {
		if m.Dead {
			continue
		}
		living++
		if strings.EqualFold(m.Name, carrier) {
			haveCarrier = true
		}
	}
	if !haveCarrier {
		return nil, fmt.Errorf("the carrier %s is not a living bot of the party", carrier)
	}
	if living < n {
		return nil, fmt.Errorf("the ritual needs %d bots, the party has %d living", n, living)
	}
	out = append(out, carrier)
	rank := func(m ritualMember) int {
		switch {
		case m.Lead:
			return 3
		case m.Tank:
			return 2
		case m.Healer:
			return 1
		}
		return 0
	}
	for r := 0; r <= 3 && len(out) < n; r++ {
		for _, m := range party {
			if len(out) == n {
				break
			}
			if m.Dead || strings.EqualFold(m.Name, carrier) || rank(m) != r {
				continue
			}
			out = append(out, m.Name)
		}
	}
	return out, nil
}

// ritualRoster is the feed's party as ritual members.
func (d *RemoteDriver) ritualRoster() []ritualMember {
	d.look()
	var out []ritualMember
	for _, m := range d.fr.Party {
		out = append(out, ritualMember{
			Name:   m.Name,
			Lead:   m.GUID == d.fr.Lead || strings.EqualFold(m.Name, d.opt.Lead),
			Tank:   strings.EqualFold(m.Name, d.opt.Tank) || (d.tank != 0 && m.GUID == d.tank),
			Healer: d.opt.Healer != "" && strings.EqualFold(m.Name, d.opt.Healer),
			Dead:   m.Dead,
		})
	}
	return out
}

// stepRitual is a ritual step: the approach holds the party by the object, then one `cmd ritual` order for all the bots.
func (d *RemoteDriver) stepRitual(ctx context.Context, a Action, carrier tbm.PartyRec, rec *objRec) (string, error) {
	if !d.entry.ok {
		return "", fmt.Errorf("a ritual step orders `cmd ritual entry:%d@x,y,z <bots>`: this module does not take objects by entry, or does not know the order (TortoiseBots with cmd ritual is needed; the agent's own character cannot take the step over)", a.Entry)
	}
	stayed, err := d.approachObject(ctx, rec, carrier.Name, objectPoint(a), d.standOf(a))
	if err != nil {
		return "", err
	}
	defer d.resumeFollow(ctx, stayed)
	for try := 1; ; try++ {
		names, err := PickRitualBots(d.ritualRoster(), carrier.Name, a.Participants)
		if err != nil {
			return "", err
		}
		rec.update(func(s *ObjectStepReport) { s.Participants = names })
		command := "cmd ritual " + tbm.EntryArg(a.Entry, pointArr(a.EntryAt)) + " " + strings.Join(names, " ")
		res, err := d.carry(ctx, rec, command, objectPoint(a))
		if err == nil {
			if !res.Ritual {
				return "", fmt.Errorf("%s was answered %q, not done", command, res.Describe())
			}
			d.rep.note(fmt.Sprintf("%s: %d bots used the ritual together (%s)", a.Label, res.Participants, strings.Join(names, ", ")))
			return "", nil
		}
		if res.Code() != "interrupted" || try >= ritualTries || runEnded(ctx, err) {
			return "", err
		}
		d.rep.note(fmt.Sprintf("%s: the ritual was interrupted (%s); the module let the bots go, trying once more", a.Label, res.Describe()))
		if terr := d.tick(ctx, d.cfg.Poll*3); terr != nil {
			return "", terr
		}
	}
}
