package dungeon

import (
	"context"
	"errors"
	"fmt"

	"github.com/pjerra/turtle-agent/internal/events"
)

// Optional legs (Leg.Optional) in the group driver: the leg runs when the leader carries the item at the leg's start, else it is skipped with its
// result "skipped: no <item> ..." (the remote driver does the same with the bots' bags, remote_optional.go). A GM leader is not let through
// without the item: the lock is not the question, the leg is a test that was set up with the item.

var errOptionalMissing = errors.New("optional item missing")

// skipOptionalLeg is called at an optional leg's start and reports whether the leg was skipped.
func (d *Driver) skipOptionalLeg(ctx context.Context, i int) (bool, error) {
	o := d.plan.Legs[i].Optional
	if o == nil {
		return false, nil
	}
	item := o.Ref()
	err := d.checkItem(ctx, &item, false, func(*ItemRef, string) error { return errOptionalMissing })
	switch {
	case err == nil:
		return false, nil
	case !errors.Is(err, errOptionalMissing):
		return false, err
	}
	name := d.plan.Legs[i].Name
	result := fmt.Sprintf("skipped: no %s in the leader's bags (an optional leg: it runs only when the leader carries the item)", item)
	now := d.clk.Now()
	d.rep.update(func(r *Report) {
		r.Legs = append(r.Legs, LegReport{Leg: i, Name: name, Started: now, Result: result})
	})
	d.rep.note(fmt.Sprintf("leg %d (%s): %s", i, name, result))
	d.emit(events.KindDungeonLeg, fmt.Sprintf("leg %d %s: %s", i, name, result), map[string]any{"leg": i, "name": name, "phase": "skipped", "result": result})
	d.st.SkipLeg()
	return true, nil
}
