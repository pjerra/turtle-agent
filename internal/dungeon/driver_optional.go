package dungeon

import (
	"context"
	"errors"
	"fmt"
	"time"

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

// skipOptionalLeg is called at an optional leg's start and reports whether the leg was skipped. The leg runs
// only when one inventory read shows every need at its count. Bags that cannot be read yet (no ItemSource, or
// an item still loading) prove nothing: the leg runs, and a short count seen next to a loading slot waits for
// the list to settle instead of skipping on it. An empty list waits out ItemsWait in checkItem, then skips the
// leg — exactly the single-item path.
func (d *Driver) skipOptionalLeg(ctx context.Context, i int) (bool, error) {
	o := d.plan.Legs[i].Optional
	if o == nil {
		return false, nil
	}
	legacy := len(o.Also) == 0
	// The single-item path waits through an unknown list for a readable one with checkItem (its miss fires only
	// on a read list), then reads the count from that same inventory: every need at its count in one read. A list
	// that cannot be read yet (no ItemSource, or an item still loading) proves nothing: the leg runs. An empty
	// list waits out ItemsWait in checkItem, then skips.
	//
	// settle waits up to ItemsWait for a partly read list (a slot still loading) to finish, so a short count next to
	// a loading slot is not taken for short: the rest of the stack may be in that slot. It reports whether the list
	// became readable; a list still unread after the wait proves nothing either, and the leg runs.
	settle := func(nd ItemNeed) (bool, error) {
		for waited := time.Duration(0); ; waited += d.cfg.Poll {
			if _, known, empty := d.itemState(nd.Item); known || empty {
				return true, nil
			}
			if waited >= d.cfg.ItemsWait {
				return false, nil
			}
			if err := d.sleep(ctx, d.cfg.Poll); err != nil {
				return false, err
			}
		}
	}
	miss := func(it *ItemRef, where string) error { return errOptionalMissing }
	var first *ItemNeed
	var firstRef ItemRef
	var held int
	for _, nd := range o.Needs() {
		nd := nd
		it := ItemRef{Entry: nd.Item, Name: nd.Name}
		// Like the single-item path before it, a GM leader is no exemption: the lock is not the question,
		// the leg is a test set up with the item.
		has, known, empty := d.itemState(nd.Item)
		if !known && !empty {
			if !has {
				return false, nil
			}
			if d.itemCount(nd.Item) >= nd.Count {
				continue // the count is already at the need: a loading slot can only add to it
			}
			readable, err := settle(nd)
			if err != nil {
				return false, err
			}
			if !readable {
				return false, nil
			}
		}
		if err := d.checkItem(ctx, &it, false, miss); err != nil {
			if !errors.Is(err, errOptionalMissing) {
				return false, err
			}
			first, firstRef, held = &nd, it, d.itemCount(nd.Item)
			break
		}
		if n := d.itemCount(nd.Item); n < nd.Count {
			first, firstRef, held = &nd, it, n
			break
		}
	}
	if first == nil {
		return false, nil
	}
	detail := skipNeedText(first, held)
	if legacy {
		detail = fmt.Sprintf("no %s in the leader's bags", firstRef)
	}
	name := d.plan.Legs[i].Name
	result := fmt.Sprintf("skipped: %s (an optional leg: it runs only when the leader carries the item)", detail)
	now := d.clk.Now()
	d.rep.update(func(r *Report) {
		r.Legs = append(r.Legs, LegReport{Leg: i, Name: name, Started: now, Result: result})
	})
	d.rep.note(fmt.Sprintf("leg %d (%s): %s", i, name, result))
	d.emit(events.KindDungeonLeg, fmt.Sprintf("leg %d %s: %s", i, name, result), map[string]any{"leg": i, "name": name, "phase": "skipped", "result": result})
	d.st.SkipLeg()
	return true, nil
}
