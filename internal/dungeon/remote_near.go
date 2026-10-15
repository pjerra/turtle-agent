package dungeon

import (
	"context"
	"fmt"
	"math"
	"slices"
	"strings"
	"time"

	"github.com/pjerra/turtle-agent/internal/objects"
)

// Keeping the agent near the party (remote mode). The object steps need the agent's own character in view of the chest, the corpse
// or the door: only its object store knows the spawn GUID the carrier's order names. The bots walk, the agent does not, so in the first
// full Uldaman run Glacienne stayed ~110 yd behind and the object steps could never have seen anything.
//
//	a GM (the account's level reaches gmCapLevels.Teleport): `.appear <lead bot>` when the agent is farther than keepNearFar;
//	not a GM: the agent walks its own character (walk_path) to a point keepNearStand yards short of the lead bot, only out of combat and
//	never towards a hostile the bots see near that point.
//
// It runs after each leg's walk and before each object step. Every `.appear` is a GM assist during the run: it is counted in run_gm_assists, noted, and
// listed in setup_shortcuts, and gm_commands stays what the enter phase made it (so "0 GM commands" has to read run_gm_assists too). The agent's presence is no part of the run's result.

const (
	keepNearFar   = 40.0             // yards from the lead bot beyond which the agent is brought up
	keepNearStand = 30.0             // yards short of the lead bot a walking agent stops at
	keepNearSafe  = 25.0             // a hostile within this of the stand point (or of the way, sampled every wayStep) keeps a walking agent where it is
	wayStep       = 10.0             // yards between the points of a way that are checked against the hostiles
	keepNearWalk  = 60 * time.Second // ceiling of the walk behind the party
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
			d.rep.note(fmt.Sprintf("keep near: the agent's account level is %d: it follows the party with .appear: GM assists during the run, counted in run_gm_assists and not in gm_commands", level))
		} else {
			d.rep.note("keep near: the agent is not a GM (or its level is unknown): it walks its own character behind the party, out of combat")
		}
	}
	return d.near.gm
}

// setupHas says whether the caller's setup list (the GM commands it ran before the run) holds the command, ignoring case, a leading
// dot and extra spaces.
func (d *RemoteDriver) setupHas(cmd string) bool {
	for _, c := range d.cfg.Setup {
		if strings.Join(strings.Fields(strings.ToLower(strings.TrimPrefix(strings.TrimSpace(c), "."))), " ") == cmd {
			return true
		}
	}
	return false
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
	return d.keepNearOpt(ctx, false)
}

// keepNearOpt is keepNear; force overrides the entry-targets switch-off (remote_entry.go: no object step needs the agent in view) for
// the one thing that still does, the evidence of a boss killed before this run.
func (d *RemoteDriver) keepNearOpt(ctx context.Context, force bool) (moved bool, err error) {
	f, ok := d.g.(Follower)
	if !ok || (d.entry.only && !force) {
		return false, nil
	}
	d.look()
	dist, ok := d.agentDistance(f)
	if !ok || dist <= keepNearFar {
		return false, nil
	}
	if d.agentGM(ctx, f) {
		// A GM assist during the run: never in a fight (the agent would land in it), and only for a GM made invisible by the setup, so
		// that the mobs do not take the arriving agent for a target.
		if d.inCombat() {
			return false, nil
		}
		if !d.setupHas("gm visible off") {
			if d.near.once("visible") {
				d.rep.note(`keep near: no .appear: the setup does not list "gm visible off" (pass it in dungeon_run setup after running it), so a GM agent arriving could be seen by the mobs`)
			}
			return false, nil
		}
		return d.appear(ctx, f, d.opt.Lead, dist)
	}
	return d.walkBehind(ctx, f, dist)
}

func (d *RemoteDriver) appear(ctx context.Context, f Follower, player string, dist float64) (bool, error) {
	if err := f.Appear(ctx, player); err != nil {
		d.rep.note(fmt.Sprintf("keep near: .appear %s failed: %v", player, err))
		return false, nil
	}
	cmd := ".appear " + player
	d.rep.update(func(r *Report) { r.SetupShortcuts = append(r.SetupShortcuts, cmd+" (GM assist during the run)") })
	d.remote(func(r *RemoteReport) { r.RunGMAssists++ })
	where := "on another map"
	if !math.IsInf(dist, 1) {
		where = fmt.Sprintf("%.0f yd behind", dist)
	}
	who := "the lead bot"
	if !strings.EqualFold(player, d.opt.Lead) {
		who = player
	}
	d.rep.note(fmt.Sprintf("leg %d: keep near: the agent was %s %s; %s a GM assist during the run (run_gm_assists; not counted in gm_commands)", d.curLeg, where, who, cmd))
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
	if !ok || !d.agentWayClear(path, stand) {
		return false, nil
	}
	d.remote(func(r *RemoteReport) { r.AgentWalks++ })
	d.rep.note(fmt.Sprintf("leg %d: keep near: the agent is %.0f yd behind the lead bot; walking to %.0f %.0f %.0f", d.curLeg, dist, stand.X, stand.Y, stand.Z))
	return d.agentWalk(ctx, f, stand, keepNearWalk)
}

// agentWayClear says whether no living hostile stands within keepNearSafe of the way: the path sampled about every wayStep yards along
// every segment (a mob in the middle of a long straight is no corner) plus the stand point. The hostiles are those the bots see
// (the feed) and those in the agent's own store (UnitSource): the feed lists only what the bots see, and the agent walks where they do not.
// A walking agent stays out of pulls; the first hostile that blocks it is noted once.
func (d *RemoteDriver) agentWayClear(path []Point, stand Point) bool {
	type foe struct {
		at   Point
		name string
		key  string
	}
	var foes []foe
	for _, h := range d.fr.Hostiles {
		if h.Entry != 0 && h.HPPct > 0 {
			foes = append(foes, foe{hostilePoint(h), d.hostileName(h), h.GUID.String()})
		}
	}
	if us, ok := d.g.(UnitSource); ok {
		for _, u := range us.Units() {
			if u.Type == "creature" && u.Hostile && !u.Dead && u.PosAgeMS >= 0 && (u.MaxHealth == 0 || u.Health > 0) {
				name := u.Name
				if name == "" {
					name = fmt.Sprintf("creature %d", u.Entry)
				}
				foes = append(foes, foe{Point{u.X, u.Y, u.Z}, name + " (in the agent's view)", u.GUID.String()})
			}
		}
	}
	if len(foes) == 0 {
		return true
	}
	pts := []Point{stand}
	for i := 0; i < len(path); i++ {
		pts = append(pts, path[i])
		if i == 0 {
			continue
		}
		a, b := path[i-1], path[i]
		n := int(dist3(a, b) / wayStep)
		for k := 1; k <= n; k++ {
			t := float32(k) / float32(n+1)
			pts = append(pts, Point{a.X + (b.X-a.X)*t, a.Y + (b.Y-a.Y)*t, a.Z + (b.Z-a.Z)*t})
		}
	}
	for _, p := range pts {
		for _, h := range foes {
			if dist3(h.at, p) <= keepNearSafe {
				if d.near.once(h.key) {
					d.rep.note(fmt.Sprintf("keep near: the agent stays where it is: %s stands near its way", h.name))
				}
				return false
			}
		}
	}
	return true
}

// agentWalk walks the agent's character to stand and keeps the run going meanwhile; a fight stops the walk, and so does limit (when it
// is not 0). It reports whether the walk ended well; the error is the run's.
func (d *RemoteDriver) agentWalk(ctx context.Context, f Follower, stand Point, limit time.Duration) (bool, error) {
	return d.agentWalkOpt(ctx, f, stand, limit, true)
}

// agentWalkOpt is agentWalk; stopOnFight false keeps the walk going through a fight (the agent's walk out of a zone it has just taken a
// trigger in: the party is about to fight there).
func (d *RemoteDriver) agentWalkOpt(ctx context.Context, f Follower, stand Point, limit time.Duration, stopOnFight bool) (bool, error) {
	wctx, cancel := context.WithCancel(ctx)
	defer cancel()
	type result struct {
		r   WalkResult
		err error
	}
	done := make(chan result, 1)
	started := make(chan struct{})
	go func() {
		close(started)
		r, err := f.WalkSelf(wctx, stand)
		done <- result{r, err}
	}()
	<-started
	began := d.clk.Now() // the walk's time counts from here
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
		if stopOnFight && d.inCombat() {
			cancel()
			<-done
			d.rep.note("keep near: a fight began: the agent stopped walking")
			return false, nil
		}
		if limit > 0 && d.clk.Now().Sub(began) >= limit {
			cancel()
			<-done
			d.rep.note(fmt.Sprintf("keep near: the agent's walk took longer than %.0f s: stopped", limit.Seconds()))
			return false, nil
		}
	}
}

// inCombat says whether the party fights (or a hostile fights it) in the newest frame.
func (d *RemoteDriver) inCombat() bool {
	return d.haveFr && (d.fr.PartyInCombat() || len(d.fr.EngagedHostiles()) > 0)
}

// nearBeforeObjectStep is keepNear when the Stepper is about to look at an object step (a chest, a door, an item to use, a game-object
// wait): the Stepper fails a step whose object the agent does not see, so the agent has to be up before Next. Once per step. The agent
// is first brought toward the lead bot (keepNear), and when the step's own object is still not in its store, toward the object
// (viewObject): the lead bot can stand more than the view range from the door or chest of the step.
func (d *RemoteDriver) nearBeforeObjectStep(ctx context.Context) error {
	key, ok := d.objectStepNext()
	if !ok || d.entry.only {
		return nil
	}
	if d.near.tried == nil {
		d.near.tried = map[[2]int]bool{}
	}
	if d.near.tried[key] {
		return nil
	}
	d.near.tried[key] = true
	moved, err := d.keepNear(ctx)
	if err != nil {
		return err
	}
	return d.viewObject(ctx, key, moved)
}

// The agent brought into view of an object step's game object. Only its own object store knows the spawn GUID the carrier's order
// names, and the store holds what the server sent for the agent's position (uldaman-remote-4: the lead bot stood 41 yd from the Seal
// the next step waited for). The server's view range is not read anywhere, so the object is simply looked for.
const (
	objectViewWait    = 12 * time.Second // the object is looked for this long, from the start and again after the walk (the store takes new surroundings in over a few updates)
	objectViewSettle  = 4 * time.Second  // ... before the agent is walked toward it (after a move toward the lead bot: this long to see)
	objectViewStand   = 20.0             // yards short of the object a walking agent stops at
	objectViewWalkMax = 40 * time.Second // ceiling of that walk
)

// objectViewOf is the game object an object step needs the agent to see and where it stands (nil without a position in the plan); the
// ref is nil for a step that needs none (an item to use).
func objectViewOf(st *Step) (*ObjectRef, *Point) {
	var ref *ObjectRef
	var stand *Point
	switch {
	case st == nil:
		return nil, nil
	case st.UseObject != nil:
		ref, stand = &st.UseObject.ObjectRef, st.UseObject.Stand
	case st.LootObject != nil:
		ref, stand = &st.LootObject.ObjectRef, st.LootObject.Stand
	case st.Ritual != nil:
		at := st.Ritual.Position
		ref, stand = &ObjectRef{Entry: st.Ritual.Entry, Near: &at}, st.Ritual.Stand
	case st.WaitFor != nil && st.WaitFor.GameObject != nil:
		ref = &st.WaitFor.GameObject.ObjectRef
	default:
		return nil, nil
	}
	if ref.Near != nil {
		return ref, ref.Near
	}
	return ref, stand
}

// stepByKey is the step objectStepNext's key names.
func (d *RemoteDriver) stepByKey(key [2]int) *Step {
	if key[0] < 0 || key[0] >= len(d.plan.Legs) {
		return nil
	}
	l := d.plan.Legs[key[0]]
	if key[1] < 0 {
		if i := -1 - key[1]; i < len(l.AfterBoss) {
			return &l.AfterBoss[i]
		}
		return nil
	}
	if key[1] < len(l.Steps) {
		return &l.Steps[key[1]]
	}
	return nil
}

// viewObject looks for the step's game object in the agent's store, and moves the agent toward it when it is not there: a short wait
// (when keepNear just moved the agent), then the agent walks to objectViewStand yards short of the object (out of combat, never past a
// hostile the bots see), then a wait again; the look ends objectViewWait after the start, or after the walk when there was one. It gives up quietly: the Stepper then fails the step
// naming how far the agent stood (objectViewError), or a game-object wait goes on as unchecked. The error is the run's.
func (d *RemoteDriver) viewObject(ctx context.Context, key [2]int, moved bool) error {
	f, ok := d.g.(Follower)
	ref, at := objectViewOf(d.stepByKey(key))
	if !ok || ref == nil || d.objectSeen(ref) {
		return nil
	}
	began := d.clk.Now()
	until := began.Add(objectViewWait) // the walk's own time does not count: the store gets objectViewWait after it
	walked := false
	for {
		if d.objectSeen(ref) {
			d.rep.note(fmt.Sprintf("leg %d: keep near: %s is in the agent's view after %.0f s", d.curLeg, describeRef(ref), d.clk.Now().Sub(began).Seconds()))
			return nil
		}
		if !d.clk.Now().Before(until) {
			d.rep.note(fmt.Sprintf("leg %d: keep near: %s is not in the agent's view after %.0f s%s", d.curLeg, describeRef(ref), objectViewWait.Seconds(), d.agentFarText(at)))
			return nil
		}
		if !walked && at != nil && (!moved || d.clk.Now().Sub(began) >= objectViewSettle) && !d.inCombat() {
			walked = true
			if err := d.walkToObject(ctx, f, *at, ref); err != nil {
				return err
			}
			until = d.clk.Now().Add(objectViewWait)
			continue
		}
		if err := d.tick(ctx, d.cfg.Poll); err != nil {
			return err
		}
	}
}

// objectSeen says whether the agent's store holds the object the way the Stepper finds it.
func (d *RemoteDriver) objectSeen(ref *ObjectRef) bool {
	_, ok := d.st.find(ref)
	return ok
}

// agentFarText is ", the agent stands N yd from it" for the object's position; empty when either is unknown.
func (d *RemoteDriver) agentFarText(at *Point) string {
	f, ok := d.g.(Follower)
	if !ok || at == nil {
		return ""
	}
	here, _, ok := f.AgentPlace()
	if !ok {
		return ""
	}
	return fmt.Sprintf(" (the agent stands %.0f yd from it)", dist3(here, *at))
}

// walkToObject walks the agent's character to objectViewStand yards short of the object's position, unless it is that close already
// or a hostile stands near its way. The walk is counted in agent_walks and ends after objectViewWalkMax.
func (d *RemoteDriver) walkToObject(ctx context.Context, f Follower, at Point, ref *ObjectRef) error {
	here, m, ok := f.AgentPlace()
	if l, _ := d.fr.LeadRec(); !ok || m != l.Map || dist3(here, at) <= objectViewStand+5 {
		return nil // another map, or already close: walking does not bring it in
	}
	path := []Point{here, at}
	if pl, ok := d.g.(PathPlanner); ok {
		if pts, err := pl.PlanPath(ctx, d.plan.Map, here, at); err == nil && len(pts) >= 2 {
			path = pts
		}
	}
	stand, ok := StandoffPoint(path, objectViewStand)
	if !ok || !d.agentWayClear(path, stand) {
		return nil
	}
	d.remote(func(r *RemoteReport) { r.AgentWalks++ })
	d.rep.note(fmt.Sprintf("leg %d: keep near: %s is not in the agent's view; the agent is %.0f yd from it and walks to %.0f %.0f %.0f", d.curLeg, describeRef(ref), dist3(here, at), stand.X, stand.Y, stand.Z))
	_, err := d.agentWalk(ctx, f, stand, objectViewWalkMax)
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

// UnitSource is the optional part of a RemoteGame that lists the units in the agent's own view (its object store). A boss killed
// before this driver started (a resumed run) is in no ledger and no frame, but its corpse is in the store of an agent standing near it.
type UnitSource interface {
	Units() []objects.Unit
}

// bossCorpseSeen says whether the agent's own view holds a corpse of one of the entries (health 0 or the dead flag) in the boss's area:
// within the larger of the group radius and 80 yd of a position of the step. A step without positions accepts none.
func (d *RemoteDriver) bossCorpseSeen(sp fightSpec) bool {
	us, ok := d.g.(UnitSource)
	if !ok || len(sp.anchors) == 0 {
		return false // a corpse proves nothing about a boss without the area it stood in
	}
	for _, u := range us.Units() {
		if u.Type != "creature" || !slices.Contains(sp.pick, u.Entry) || u.PosAgeMS < 0 || !(u.Dead || (u.MaxHealth > 0 && u.Health == 0)) {
			continue
		}
		for _, a := range sp.anchors {
			if dist3(Point{u.X, u.Y, u.Z}, a) <= max(float64(d.groupRadius(sp)), 80) {
				return true
			}
		}
	}
	return false
}

// nearForBoss brings the agent up before a boss step is judged (once per leg and boss), so that a corpse the feed does not list is in its
// view; it waits a few looks for the object store to take the new surroundings in.
func (d *RemoteDriver) nearForBoss(ctx context.Context, sp fightSpec) error {
	if _, ok := d.g.(UnitSource); !ok {
		return nil
	}
	if d.near.tried == nil {
		d.near.tried = map[[2]int]bool{}
	}
	key := [2]int{d.curLeg, -1000 - int(sp.pick[0])}
	if d.near.tried[key] {
		return nil
	}
	d.near.tried[key] = true
	// With entry targets keep-near is off, but a boss killed before this driver ran (a resume) is in no kill record and no ledger, and the
	// feed drops corpses fast: only the agent's view can show it dead. So the agent is still brought up for a boss, and only when nothing
	// else is evidence for it (the appear is a counted GM assist, run_gm_assists). A read-only check by the module would need a `cmd state`
	// for creature entries (follow-up in TortoiseBots).
	force := false
	if d.entry.only {
		if d.bossEvidence(sp) {
			return nil
		}
		force = true
	}
	moved, err := d.keepNearOpt(ctx, force)
	for i := 0; err == nil && moved && i < 3 && !d.bossCorpseSeen(sp); i++ {
		err = d.tick(ctx, d.cfg.Poll)
	}
	return err
}

// bossEvidence says whether this run already knows the boss dead without the agent's view: a kill at a position, the ledger, or a corpse
// the agent happens to see.
func (d *RemoteDriver) bossEvidence(sp fightSpec) bool {
	for _, e := range sp.pick {
		if len(d.killedAt[e]) > 0 {
			return true
		}
	}
	return d.ledgerDead(sp.pick) || d.bossCorpseSeen(sp)
}
