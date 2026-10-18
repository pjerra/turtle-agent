package dungeon

import (
	"context"
	"fmt"
	"strings"

	"github.com/pjerra/turtle-agent/internal/events"
	"github.com/pjerra/turtle-agent/internal/tbm"
)

// Optional legs (Leg.Optional) in remote mode: a leg that needs an item no honest run carries (Zul'Farrak's gong needs the Mallet of
// Zul'Farrak) is run only when the lead bot or the carrier has the item in its bags at the leg's start; otherwise the whole leg is skipped, with
// its result "skipped: no <item> ...", and the run goes on at the next leg. A skipped optional leg is never a failure. The bags are read
// with `.bot inv`; a read that fails is an error (the run does not skip a leg on a guess).

// optionalHolders are the bots whose bags decide an optional leg and carry its items: the carrier, then the lead bot (each once).
func (d *RemoteDriver) optionalHolders() []string {
	var names []string
	if c, err := d.carrierBot(); err == nil {
		names = append(names, c.Name)
	}
	if lead := strings.TrimSpace(d.opt.Lead); lead != "" {
		dup := false
		for _, n := range names {
			dup = dup || strings.EqualFold(n, lead)
		}
		if !dup {
			names = append(names, lead)
		}
	}
	return names
}

// optionalLegCheck says whether the optional leg i has its item: holder is the bot that carries it, else why is the skip text. A failed read is
// an error.
func (d *RemoteDriver) optionalLegCheck(ctx context.Context, i int) (holder, why string, err error) {
	o := d.plan.Legs[i].Optional
	if o == nil {
		return "", "", nil
	}
	item := o.Ref()
	names := d.optionalHolders()
	if len(names) == 0 {
		return "", "", fmt.Errorf("leg %d (%s): optional, but there is no bot whose bags could hold %s", i, d.plan.Legs[i].Name, item)
	}
	for _, n := range names {
		inv, err := d.carrierInv(ctx, n)
		if err != nil {
			return "", "", fmt.Errorf("leg %d (%s) needs %s: %w", i, d.plan.Legs[i].Name, item, err)
		}
		if inv.Count(item.Entry) > 0 {
			return n, "", nil
		}
	}
	return "", fmt.Sprintf("no %s in the bags of %s (an optional leg: it runs only when the lead bot or the carrier carries the item; give_items puts it there for a test)", item, strings.Join(names, " or ")), nil
}

// skipOptionalLeg is called at an optional leg's start. It reports whether the leg was skipped: its result is "skipped: <why>", the stepper goes
// on at the next leg and the run notes why.
func (d *RemoteDriver) skipOptionalLeg(ctx context.Context, i int) (bool, error) {
	if d.plan.Legs[i].Optional == nil {
		return false, nil
	}
	holder, why, err := d.optionalLegCheck(ctx, i)
	if err != nil {
		return false, err
	}
	if why == "" {
		d.rep.note(fmt.Sprintf("leg %d (%s): optional; %s carries %s: the leg runs", i, d.plan.Legs[i].Name, holder, d.plan.Legs[i].Optional.Ref()))
		return false, nil
	}
	name := d.plan.Legs[i].Name
	result := "skipped: " + why
	now := d.clk.Now()
	d.rep.update(func(r *Report) {
		r.Legs = append(r.Legs, LegReport{Leg: i, Name: name, Started: now, Result: result})
	})
	d.rep.note(fmt.Sprintf("leg %d (%s): %s", i, name, result))
	d.emit(events.KindDungeonLeg, fmt.Sprintf("leg %d %s: %s", i, name, result), map[string]any{"leg": i, "name": name, "phase": "skipped", "result": result, "mode": "remote"})
	d.st.SkipLeg()
	return true, nil
}

// keyHolder is the bot that does a use_object step whose lock needs the item req: the carrier c when it carries it, else the lead bot when
// that does, else c (the step then fails naming the item). A read of the lead bot's bags that fails leaves c.
func (d *RemoteDriver) keyHolder(ctx context.Context, c tbm.PartyRec, req *ItemRef) tbm.PartyRec {
	if req == nil {
		return c
	}
	if inv, err := d.carrierInv(ctx, c.Name); err != nil || inv.Count(req.Entry) > 0 {
		return c
	}
	d.look()
	lead, ok := d.fr.LeadRec()
	if !ok || lead.Dead || strings.EqualFold(lead.Name, c.Name) {
		return c
	}
	if inv, err := d.carrierInv(ctx, lead.Name); err == nil && inv.Count(req.Entry) > 0 {
		d.rep.note(fmt.Sprintf("the carrier %s does not carry %s; the lead bot %s does, and uses the object", c.Name, req, lead.Name))
		return lead
	}
	return c
}
