package dungeon

import (
	"context"
	"errors"
	"fmt"

	"github.com/pjerra/turtle-agent/internal/events"
)

// itemCount is how many of the entry the leader carries in the last inventory read (0 when unknown).
func (d *Driver) itemCount(entry uint32) int {
	src, ok := d.g.(ItemSource)
	if !ok {
		return 0
	}
	n := 0
	for _, it := range src.Items() {
		if it.Entry == entry {
			n += int(it.Count)
		}
	}
	return n
}

// skipNeedText names the first missing need with the held and wanted counts: "no 5x Zulian Mudskunk (19975)
// in the bags of X (has 2)" for a count need, "no <item> ..." for a single item.
func skipNeedText(need *ItemNeed, held int) string {
	if need.Count <= 1 {
		return fmt.Sprintf("no %s in the bags of the leader", need.Bare())
	}
	return fmt.Sprintf("no %s in the bags of the leader (has %d)", need.Bare(), held)
}

// Optional legs (Leg.Optional) in the group driver: the leg runs when the leader carries the item at the leg's start, else it is skipped with its
// result "skipped: no <item> ..." (the remote driver does the same with the bots' bags, remote_optional.go). A GM leader is not let through
// without the item: the lock is not the question, the leg is a test that was set up with the item.

var errOptionalMissing = errors.New("optional item missing")

// skipOptionalLeg is called at an optional leg's start and reports whether the leg was skipped. The leader's
// inventory read carries counts, so every need is checked at its count: the leg runs only when one read shows
// every need at its count.
func (d *Driver) skipOptionalLeg(ctx context.Context, i int) (bool, error) {
	o := d.plan.Legs[i].Optional
	if o == nil {
		return false, nil
	}
	if len(o.Also) == 0 {
		// Legacy shape: one item, presence only. Today's behaviour exactly when Also is absent.
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
	// checkItem checks presence only, so wait through it for a readable list (an unknown list proves nothing),
	// then count from that same inventory: every need at its count in one read.
	miss := func(it *ItemRef, where string) error { return errOptionalMissing }
	var first *ItemNeed
	var held int
	for _, nd := range o.Needs() {
		nd := nd
		it := ItemRef{Entry: nd.Item, Name: nd.Name}
		if err := d.checkItem(ctx, &it, d.itemsExempt(), miss); err != nil {
			if !errors.Is(err, errOptionalMissing) {
				return false, err
			}
			first, held = &nd, d.itemCount(nd.Item)
			break
		}
		if n := d.itemCount(nd.Item); n < nd.Count {
			first, held = &nd, n
			break
		}
	}
	if first == nil {
		return false, nil
	}
	name := d.plan.Legs[i].Name
	result := fmt.Sprintf("skipped: %s (an optional leg: it runs only when the leader carries the item)", skipNeedText(first, held))
	now := d.clk.Now()
	d.rep.update(func(r *Report) {
		r.Legs = append(r.Legs, LegReport{Leg: i, Name: name, Started: now, Result: result})
	})
	d.rep.note(fmt.Sprintf("leg %d (%s): %s", i, name, result))
	d.emit(events.KindDungeonLeg, fmt.Sprintf("leg %d %s: %s", i, name, result), map[string]any{"leg": i, "name": name, "phase": "skipped", "result": result})
	d.st.SkipLeg()
	return true, nil
}
