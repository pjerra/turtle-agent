package dungeon

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"time"

	"github.com/pjerra/turtle-agent/internal/tbm"
)

// The bound party of a remote run must be exactly the named lead bot and bots before anything enters the dungeon (issue #83, run 6b):
// the bind ACKed five bots and its listing named all five, and one second later the hunter was out of the group again (an earlier
// `command <bot> leave` is only forwarded, "action success is not guaranteed", and can land after the bind), ungrouped on the outer map,
// following the agent. The enter phase then never summoned it, because it summoned only the bots of the feed. So after each bind the
// party is read back after a settle, from `commander status` (lead, count, the listing) and from the feed's party; a missing bot is
// invited again by one unbind + bind, and a bot that is still missing fails the run before the entry.

// DefaultBindSettle is how long a bind is given before the party is read back: a late `leave` or a refused invite shows by then.
const DefaultBindSettle = 3 * time.Second

// bindCommand is the bind line of a run: the lead bot first, then the others.
func bindCommand(lead string, bots []string) string {
	return strings.Join(append([]string{"commander", "bind", lead}, bots...), " ")
}

// listingRe is the system line of `commander bind|status`: "You command Lcwarra's group: Lcwarra, Lchunter (dead), Lcpriest."
var listingRe = regexp.MustCompile(`(?i)you command .*? group: (.+?)\.?\s*$`)

// boundParty is what `commander status` said.
type boundParty struct {
	Answered bool     // an ACK came
	Lead     string   // the ACK's lead bot
	Count    int      // the ACK's number of bots
	Names    []string // the listing's names ((dead) marks dropped); empty when the reply carried no listing
	Dead     []string // the listing's names marked (dead)
}

// readBoundParty sends `commander status` through command and reads the answer.
func readBoundParty(ctx context.Context, command func(context.Context, string) (tbm.Reply, error)) (boundParty, error) {
	r, err := command(ctx, "commander status")
	if err != nil {
		return boundParty{}, fmt.Errorf("commander status: %w", err)
	}
	if r.Kind != tbm.KindAck || r.Ack == nil {
		return boundParty{}, nil
	}
	b := boundParty{Answered: true, Lead: strings.TrimSpace(r.Ack.Executor), Count: r.Ack.Count}
	for _, l := range r.Lines {
		m := listingRe.FindStringSubmatch(l)
		if m == nil {
			continue
		}
		for _, n := range strings.Split(m[1], ",") {
			dead := strings.HasSuffix(strings.TrimSpace(n), "(dead)")
			n = strings.TrimSpace(strings.TrimSuffix(strings.TrimSpace(n), "(dead)"))
			if n != "" {
				b.Names = append(b.Names, n)
				if dead {
					b.Dead = append(b.Dead, n)
				}
			}
		}
		break
	}
	return b, nil
}

// partyProblems compares the bound party with the named one. status is `commander status` (zero value: not asked); feed are the party
// names of the newest feed frame (haveFeed false: no feed yet). missing are the named bots a source does not list, extra the listed
// ones the run did not name, and why says how each missing bot shows.
func partyProblems(lead string, wanted []string, st boundParty, feed []string, haveFeed bool) (missing, extra []string, why []string) {
	in := func(list []string, n string) bool {
		for _, x := range list {
			if strings.EqualFold(x, n) {
				return true
			}
		}
		return false
	}
	for _, n := range wanted {
		var reasons []string
		if len(st.Names) > 0 && !in(st.Names, n) {
			reasons = append(reasons, "not in commander status's listing")
		}
		if haveFeed && !in(feed, n) {
			reasons = append(reasons, "not in the feed's party")
		}
		if len(reasons) > 0 {
			missing = append(missing, n)
			why = append(why, n+" "+strings.Join(reasons, " and "))
		}
	}
	for _, list := range [][]string{st.Names, feed} {
		for _, n := range list {
			if !in(wanted, n) && !in(extra, n) {
				extra = append(extra, n)
			}
		}
	}
	if st.Answered && !strings.EqualFold(st.Lead, lead) {
		why = append(why, fmt.Sprintf("commander status names lead %s, not %s", st.Lead, lead))
		if len(missing) == 0 {
			missing = append(missing, lead)
		}
	}
	// Without a listing and a feed only the count tells: fewer bots than named.
	if len(missing) == 0 && st.Answered && len(st.Names) == 0 && !haveFeed && st.Count < len(wanted) {
		missing = append(missing, fmt.Sprintf("(%d of %d bots)", st.Count, len(wanted)))
		why = append(why, fmt.Sprintf("commander status counts %d bots, the run named %d", st.Count, len(wanted)))
	}
	return missing, extra, why
}

// ensureWholeParty makes sure the bound party is exactly the lead bot and the named bots before the entry, and fails otherwise. A run
// that names no bots takes the lead bot's party as it is and is not checked. A missing bot is invited by one unbind and bind again; a
// bot the second bind does not hold, or a refusal of it, fails the run, naming the bot and the module's reason.
func (d *RemoteDriver) ensureWholeParty(ctx context.Context) error {
	if len(d.opt.Bots) == 0 {
		return nil
	}
	wanted := append([]string{d.opt.Lead}, d.opt.Bots...)
	for attempt := 0; ; attempt++ {
		if err := d.settleBind(ctx); err != nil {
			return err
		}
		// Read it back until it matches, within bindReadbackWait: a bot that is loading or teleporting joins a moment late.
		var missing, extra, why []string
		for polled := time.Duration(0); ; polled += d.cfg.Poll {
			var err error
			if missing, extra, why, err = d.readParty(ctx, wanted); err != nil {
				return err
			}
			if (len(missing) == 0 && len(extra) == 0) || polled >= bindReadbackWait {
				break
			}
			if err := d.tick(ctx, d.cfg.Poll); err != nil {
				return err
			}
		}
		if len(missing) == 0 && len(extra) == 0 {
			d.rep.note(fmt.Sprintf("bound party checked: %s with %d bots, as named (commander status and the feed)", d.opt.Lead, len(d.opt.Bots)))
			return nil
		}
		if len(extra) > 0 {
			return fmt.Errorf("the bound group holds %s, which the run did not name (lead bot %s with %s); the group is not changed to fix it: unbind it and name every bot", strings.Join(extra, ", "), d.opt.Lead, strings.Join(d.opt.Bots, ", "))
		}
		if attempt >= 1 {
			return fmt.Errorf("%s not in the bound group after binding again (%s): not entering with a partial party. The bind ACK/ERR lines name a refusal (hired, no-bot, member-grouped, bot-bound, party-unnamed, ...); a bot also drops out when an earlier `command <bot> leave` lands after the bind", strings.Join(missing, ", "), strings.Join(why, "; "))
		}
		d.rep.note(fmt.Sprintf("bound party: %s; unbinding and binding again to invite %s", strings.Join(why, "; "), strings.Join(missing, ", ")))
		if err := d.rebind(ctx, missing); err != nil {
			return err
		}
	}
}

// settleBind waits DefaultBindSettle (RemoteOptions.BindSettle) so that a bot that is about to drop out has dropped.
func (d *RemoteDriver) settleBind(ctx context.Context) error {
	for waited := time.Duration(0); waited < d.opt.BindSettle; waited += d.cfg.Poll {
		if err := d.tick(ctx, d.cfg.Poll); err != nil {
			return err
		}
	}
	return nil
}

// readParty reads `commander status` and the feed's party and compares them with the named party.
func (d *RemoteDriver) readParty(ctx context.Context, wanted []string) (missing, extra, why []string, err error) {
	st, err := readBoundParty(ctx, d.g.Command)
	if err != nil {
		return nil, nil, nil, err
	}
	d.look()
	var feed []string
	for _, m := range d.fr.Party {
		feed = append(feed, m.Name)
	}
	missing, extra, why = partyProblems(d.opt.Lead, wanted, st, feed, d.haveFr)
	return missing, extra, why, nil
}

// rebind ends the binding (the bots stay grouped, the lead bot holds) and binds the named party again: the lead bot adopts its group as
// it is and invites the named bots outside it. The feed starts again with the new binding.
func (d *RemoteDriver) rebind(ctx context.Context, missing []string) error {
	if r, err := d.g.Command(ctx, "commander unbind"); err != nil {
		return fmt.Errorf("commander unbind (to bind again for %s): %w", strings.Join(missing, ", "), err)
	} else if r.Kind == tbm.KindErr && (r.Err == nil || r.Err.Code != "not-bound") {
		return fmt.Errorf("commander unbind (to bind again for %s) refused: %s", strings.Join(missing, ", "), describeBindErr(r.Err))
	}
	d.endsAtBind = len(d.g.BindingEnds()) // that unbind is ours, not the module's
	d.g.ResetView()
	d.fr, d.haveFr, d.n = tbm.ViewFrame{}, false, 0
	r, err := d.g.Command(ctx, bindCommand(d.opt.Lead, d.opt.Bots))
	if err != nil {
		return fmt.Errorf("commander bind (again, for %s): %w", strings.Join(missing, ", "), err)
	}
	switch r.Kind {
	case tbm.KindErr:
		return fmt.Errorf("commander bind (again, for %s) refused: %s: %s is not in the group and cannot be invited; not entering with a partial party", strings.Join(missing, ", "), describeBindErr(r.Err), strings.Join(missing, ", "))
	case tbm.KindTimeout:
		return fmt.Errorf("commander bind (again, for %s): no ACK or ERR came; not entering with a partial party", strings.Join(missing, ", "))
	}
	d.rep.note(fmt.Sprintf("commander bind again: ACK for %d bots", r.Ack.Count))
	if vr, err := d.g.Command(ctx, "commander view on"); err != nil {
		d.rep.note("commander view on: " + err.Error())
	} else if vr.Kind == tbm.KindErr {
		d.rep.note("commander view on refused: " + describeBindErr(vr.Err))
	}
	return d.waitFirstFrame(ctx)
}

// The party must stay whole (ZF-2 run 3b: the module dismissed the mage and the hunter from the group, "dismissed (removed from group)",
// and the run walked on with three bots until the three-dead stop). A bot that leaves the group is gone from the feed's party: when the
// feed shows fewer bots than the run started with for partyShortHold, the run stops there with the names, at the next calm moment (a
// fight in progress is fought out first). And no step that moves the party or pulls goes on with a bot dead.

// errPartyShort ends the leg: the party has fewer bots than the run started with, or one is dead where the run would walk or pull.
var errPartyShort = errors.New("party short")

// partyShortHold is how long the feed must show the party short before the run stops (a frame that lost a record for a moment is no
// departure).
const partyShortHold = 5 * time.Second

// partyIntact checks, once per look, that the feed still shows the bots the run started with. It returns errPartyShort when the party
// has been short for partyShortHold and is out of combat; in a fight it only says so, once.
func (d *RemoteDriver) partyIntact() error {
	if d.curLeg < 0 || !d.haveFr || d.wantParty <= 1 {
		return nil
	}
	have := len(d.fr.Party)
	if have >= d.wantParty {
		d.shortSince, d.shortSaid = time.Time{}, false
		return nil
	}
	now := d.clk.Now()
	if d.shortSince.IsZero() {
		d.shortSince = now
	}
	if now.Sub(d.shortSince) < partyShortHold {
		return nil
	}
	if d.fr.PartyInCombat() {
		if !d.shortSaid {
			d.shortSaid = true
			d.rep.note(fmt.Sprintf("leg %d: the party has %d of %d bots (%s); the run stops when the fight is over", d.curLeg, have, d.wantParty, d.missingNames()))
		}
		return nil
	}
	return fmt.Errorf("%w: the party has %d of %d bots, %s left the group; the run stops here instead of walking on (the binding is kept with keep_on_fail: bring the bot back and resume)", errPartyShort, have, d.wantParty, d.missingNames())
}

// missingNames names the bots of the run that the feed's party does not list.
func (d *RemoteDriver) missingNames() string {
	var out []string
	for _, n := range append([]string{d.opt.Lead}, d.opt.Bots...) {
		if n == "" {
			continue
		}
		if _, ok := d.memberByName(n); !ok {
			out = append(out, n)
		}
	}
	if len(out) == 0 {
		return "a bot"
	}
	return strings.Join(out, ", ")
}

// requireLiving refuses a step that walks or pulls while a bot of the party is dead: the rest gate revives the dead before a pull, so a
// bot still dead here is one the run could not bring back, and the party must not go on without it.
func (d *RemoteDriver) requireLiving() error {
	if d.curLeg < 0 || !d.haveFr || d.wantParty <= 1 {
		return nil
	}
	if d.fr.PartyInCombat() {
		return nil
	}
	dead := d.deadMembers()
	if len(dead) == 0 {
		return nil
	}
	var names []string
	for _, m := range dead {
		names = append(names, m.Name)
	}
	return fmt.Errorf("%w: %d of %d bots are dead (%s); the run stops here instead of walking on without them", errPartyShort, len(dead), len(d.fr.Party), strings.Join(names, ", "))
}
