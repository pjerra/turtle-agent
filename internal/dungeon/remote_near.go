package dungeon

import (
	"context"
	"fmt"
	"math"
	"slices"
)

// Keeping the agent near the party (remote mode). The object steps need the agent's own character in view of the chest, the corpse
// or the door: only its object store knows the spawn GUID the carrier's order names. The bots walk, the agent does not, so in the first
// full Uldaman run Glacienne stayed ~110 yd behind and the object steps could never have seen anything.
//
//	a GM (the account's level reaches gmCapLevels.Teleport): `.appear <lead bot>` when the agent is farther than keepNearFar;
//	not a GM: the agent walks its own character (walk_path) to a point keepNearStand yards short of the lead bot, only out of combat and
//	never towards a hostile the bots see near that point.
//
// It runs after each leg's walk and before each object step. Every `.appear` is a setup shortcut in the report (setup_shortcuts, with a
// note), not a run GM command: gm_commands stays what the enter phase made it. The agent's presence is no part of the run's result.

const (
	keepNearFar   = 40.0 // yards from the lead bot beyond which the agent is brought up
	keepNearStand = 30.0 // yards short of the lead bot a walking agent stops at
	keepNearSafe  = 25.0 // a hostile within this of the stand point (or a corner of the way) keeps a walking agent where it is
)

// Follower is the optional part of a RemoteGame that moves the agent's own character after the party.
type Follower interface {
	// AgentPlace is where the agent's character stands; ok is false when it is not in the world.
	AgentPlace() (at Point, mapID uint32, ok bool)
	// AccountLevel is the agent's account security level (as Enterer's).
	AccountLevel(ctx context.Context) (level int, known bool, err error)
	// Appear sends `.appear <player>` (GM): the agent is teleported to the player.
	Appear(ctx context.Context, player string) error
	// WalkSelf walks the agent's character along the navmesh to the point (walk_path); cancelling ctx stops the walk.
	WalkSelf(ctx context.Context, to Point) (WalkResult, error)
}

// nearState is the driver's state of the keep-near behaviour.
type nearState struct {
	probed bool
	gm     bool
	noted  map[string]bool
	tried  map[[2]int]bool // object steps an `.appear`/walk was already tried for by objectLook
}

func (n *nearState) once(key string) bool {
	if n.noted == nil {
		n.noted = map[string]bool{}
	}
	if n.noted[key] {
		return false
	}
	n.noted[key] = true
	return true
}

// agentGM probes the account once: a GM may `.appear`.
func (d *RemoteDriver) agentGM(ctx context.Context, f Follower) bool {
	if !d.near.probed {
		d.near.probed = true
		level, known, err := f.AccountLevel(ctx)
		d.near.gm = err == nil && known && capsFor(level).Teleport
		if d.near.gm {
			d.rep.note(fmt.Sprintf("keep near: the agent's account level is %d: it follows the party with .appear (setup shortcuts, not run GM commands)", level))
		} else {
			d.rep.note("keep near: the agent is not a GM (or its level is unknown): it walks its own character behind the party, out of combat")
		}
	}
	return d.near.gm
}

// agentDistance is how far the agent stands from the lead bot (+Inf on another map); ok is false when either is unknown.
func (d *RemoteDriver) agentDistance(f Follower) (dist float64, ok bool) {
	at, m, here := f.AgentPlace()
	lead, haveLead := d.leadPos()
	l, _ := d.fr.LeadRec()
	if !here || !haveLead || d.stale() {
		return 0, false
	}
	if m != l.Map {
		return math.Inf(1), true
	}
	return dist3(at, lead), true
}

// keepNear brings the agent within keepNearFar of the lead bot if it can, and reports whether it moved it. Its error is the run's
// (a binding lost, a wipe while it waited); a failed appear or walk is noted and is no error.
func (d *RemoteDriver) keepNear(ctx context.Context) (moved bool, err error) {
	f, ok := d.g.(Follower)
	if !ok {
		return false, nil
	}
	d.look()
	dist, ok := d.agentDistance(f)
	if !ok || dist <= keepNearFar {
		return false, nil
	}
	if d.agentGM(ctx, f) {
		return d.appear(ctx, f, dist)
	}
	return d.walkBehind(ctx, f, dist)
}

func (d *RemoteDriver) appear(ctx context.Context, f Follower, dist float64) (bool, error) {
	if err := f.Appear(ctx, d.opt.Lead); err != nil {
		d.rep.note(fmt.Sprintf("keep near: .appear %s failed: %v", d.opt.Lead, err))
		return false, nil
	}
	cmd := ".appear " + d.opt.Lead
	d.rep.update(func(r *Report) { r.SetupShortcuts = append(r.SetupShortcuts, cmd) })
	d.remote(func(r *RemoteReport) { r.AgentAppears++ })
	where := "on another map"
	if !math.IsInf(dist, 1) {
		where = fmt.Sprintf("%.0f yd behind", dist)
	}
	d.rep.note(fmt.Sprintf("leg %d: keep near: the agent was %s the lead bot; %s (a setup shortcut, not a run GM command)", d.curLeg, where, cmd))
	return true, d.tick(ctx, d.cfg.Poll) // the object store takes the new surroundings in over the next updates
}

// walkBehind walks the agent's character to keepNearStand yards short of the lead bot.
func (d *RemoteDriver) walkBehind(ctx context.Context, f Follower, dist float64) (bool, error) {
	if math.IsInf(dist, 1) {
		if d.near.once("map") {
			d.rep.note("keep near: the agent is not on the party's map and is no GM: it cannot follow the party in")
		}
		return false, nil
	}
	if d.inCombat() {
		return false, nil // the next look after a fight tries again
	}
	at, _, _ := f.AgentPlace()
	lead, _ := d.leadPos()
	path := []Point{at, lead}
	if pl, ok := d.g.(PathPlanner); ok {
		if pts, err := pl.PlanPath(ctx, d.plan.Map, at, lead); err == nil && len(pts) >= 2 {
			path = pts
		}
	}
	stand, ok := StandoffPoint(path, keepNearStand)
	if !ok {
		return false, nil
	}
	for _, p := range append(slices.Clone(path), stand) {
		for _, h := range d.fr.Hostiles {
			if h.Entry != 0 && h.HPPct > 0 && dist3(hostilePoint(h), p) <= keepNearSafe {
				if d.near.once(h.GUID.String()) {
					d.rep.note(fmt.Sprintf("keep near: the agent stays where it is: %s stands near its way", d.hostileName(h)))
				}
				return false, nil
			}
		}
	}
	d.remote(func(r *RemoteReport) { r.AgentWalks++ })
	d.rep.note(fmt.Sprintf("leg %d: keep near: the agent is %.0f yd behind the lead bot; walking to %.0f %.0f %.0f", d.curLeg, dist, stand.X, stand.Y, stand.Z))
	wctx, cancel := context.WithCancel(ctx)
	defer cancel()
	type result struct {
		r   WalkResult
		err error
	}
	done := make(chan result, 1)
	go func() {
		r, err := f.WalkSelf(wctx, stand)
		done <- result{r, err}
	}()
	for {
		select {
		case res := <-done:
			if res.err != nil && ctx.Err() == nil {
				d.rep.note("keep near: the agent's walk failed: " + res.err.Error())
			}
			return res.err == nil, ctx.Err()
		default:
		}
		if err := d.tick(ctx, d.cfg.Poll); err != nil {
			cancel()
			<-done
			return false, err
		}
		if d.inCombat() {
			cancel()
			<-done
			d.rep.note("keep near: a fight began: the agent stopped walking")
			return false, nil
		}
	}
}

// inCombat says whether the party fights (or a hostile fights it) in the newest frame.
func (d *RemoteDriver) inCombat() bool {
	return d.haveFr && (d.fr.PartyInCombat() || len(d.fr.EngagedHostiles()) > 0)
}

// nearBeforeObjectStep is keepNear when the Stepper is about to look at an object step (a chest, a door, an item to use, a game-object
// wait): the Stepper fails a step whose object the agent does not see, so the agent has to be up before Next. Once per step.
func (d *RemoteDriver) nearBeforeObjectStep(ctx context.Context) error {
	key, ok := d.objectStepNext()
	if !ok {
		return nil
	}
	if d.near.tried == nil {
		d.near.tried = map[[2]int]bool{}
	}
	if d.near.tried[key] {
		return nil
	}
	d.near.tried[key] = true
	_, err := d.keepNear(ctx)
	return err
}

// objectStepNext says whether the next thing the Stepper looks at is an object step, and which (leg, index; negative for the steps after
// the boss). The Stepper passes a finished walk or pull list inside one Next, so those phases count when they are over.
func (d *RemoteDriver) objectStepNext() ([2]int, bool) {
	leg, phase, idx := d.st.Position()
	if leg < 0 || leg >= len(d.plan.Legs) {
		return [2]int{}, false
	}
	l := d.plan.Legs[leg]
	switch phase {
	case "walk":
		if at, ok := d.leadPos(); !ok || dist3(at, l.To) > ArriveRadius || len(l.PullGroups) > 0 {
			return [2]int{}, false
		}
		idx = 0
	case "pull_groups":
		if idx < len(l.PullGroups) {
			return [2]int{}, false
		}
		idx = 0
	case "steps":
	case "after_boss":
		if idx < len(l.AfterBoss) && objectStepKind(l.AfterBoss[idx]) != "" {
			return [2]int{leg, -1 - idx}, true
		}
		return [2]int{}, false
	default:
		return [2]int{}, false
	}
	if idx < len(l.Steps) && objectStepKind(l.Steps[idx]) != "" {
		return [2]int{leg, idx}, true
	}
	return [2]int{}, false
}
