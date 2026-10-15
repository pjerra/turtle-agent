package dungeon

import (
	"context"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/pjerra/turtle-agent/internal/packet"
	"github.com/pjerra/turtle-agent/internal/tbm"
)

var (
	_ Runner = (*Driver)(nil)
	_ Runner = (*RemoteDriver)(nil)
)

const (
	gTank   = packet.GUID(0x2A)
	gHealer = packet.GUID(0x2B)
	gMage   = packet.GUID(0x2C)
)

type rmob struct {
	guid     packet.GUID
	entry    uint32
	pos      Point
	hp       float64
	target   packet.GUID
	engaged  bool
	pulled   bool
	pullAt   time.Time
	attacked bool
	flags    string
	// the tank-first tests: hold keeps the mob on that bot (until a `cmd attack <guid> <tank>` at it sends the tank), noTarget leaves it with no
	// target (a sheep), tankPct is the tank's share in the T record while the mob is on another bot (0: not listed).
	hold     packet.GUID
	noTarget bool
	tankPct  int
	// inert: a pull at it is acknowledged and does nothing (the mob never comes: no fight)
	inert bool
	// aggroed: it came at the party because a bot walked within the fake's aggro range of it (not by a pull)
	aggroed bool
	// reaction and kind: the record's TortoiseBots #943 fields ("" leaves them out, as an older module does)
	reaction, kind string
	// the runner tests (remote_flee_test.go): at fleeAt percent health the mob runs from the lead bot at fleeSpeed yards a second (flag
	// c) for fleeFor. Only an attack order sent at it after it started to run (hit) hurts it, and only within 12 yd of where it started
	// to run: a mob nobody is sent after escapes. hideRun takes it out of the feed 2 s after it started to run. comesBack: after fleeFor
	// it stands by the lead bot again (the tank has it) and its friends join the fight, next to the lead bot.
	fleeAt, fleeSpeed  float64
	fleeFor            time.Duration
	hideRun, comesBack bool
	friends            []*rmob
	fleeing, back, hit bool
	untouchable        bool // no order hurts it while it runs (it is out of reach or out of sight)
	fleeStart, backAt  time.Time
	fleeOrigin         Point
	// ccUntil: a `cmd cc` at it holds it (flag c, no target) until then (fakeRemote.ccLasts)
	ccUntil time.Time
}

// fakeRemote is a RemoteGame the test steers: time moves only when the driver sleeps; the lead bot walks to the point of a
// `cmd move`, a `cmd pullback` brings the mob onto the tank after a delay, `cmd attack` kills it, `cmd rest` heals the party. Every
// sleep publishes a frame, as the module does about once a second.
type fakeRemote struct {
	t     *testing.T
	now   time.Time
	n     uint64
	party []*tbm.PartyRec
	mobs  []*rmob
	// the lead bot
	moveTo *Point
	speed  float64
	mapID  uint32

	orders   []string
	bound    bool
	unbinds  int
	bindErr  string   // code the bind is refused with
	status   *tbm.Ack // the answer to `commander status` (the default: the lead bot with 3 bots)
	ends     []tbm.BindingEnd
	stats    tbm.ViewStats
	frame    tbm.ViewFrame
	have     bool
	noFrames bool // the feed never starts
	cutAt    time.Time

	ignoreMoves int      // the next n moves do nothing (the bot is stuck)
	moveErrs    []string // codes the next moves are refused with
	directErrs  []string // codes the next direct moves (`cmd move x y z direct`) are refused with
	// strictDirect: a direct move is refused too-far (naming the bot, as TortoiseBots #948 does) while a living bot stands more than 5 yd
	// from the lead bot. farOnce makes the next direct move lose that bot (it falls 10 yd behind and sticks) and answers it too-far.
	strictDirect bool
	farOnce      string
	summoned     []string // bots `cmd summon` fetched to the lead bot
	// moveFetches: a plain `cmd move` brings the lagging bots (lagUntil) to the point, as the module's party move does.
	moveFetches bool
	// strictAttack: a bot-scoped `cmd attack <guid> <bot>` is refused too-far while that bot stands more than 60 yd from the mob (the
	// module\'s sight distance); tankTooFar makes the next n such orders too-far whatever the feed shows.
	strictAttack bool
	tankTooFar   int
	noDirect     bool                // the module does not know the direct flag: a move with it is refused as invalid
	cmdErr       map[string][]string // verb -> codes the next such orders are refused with
	holdDelay    time.Duration       // the mob goes on the tank this long after the pull; negative: the mage keeps it
	engageDelay  time.Duration
	killRate     float64 // hp per second once attacked
	resting      bool
	healRate     float64
	onTick       func(g *fakeRemote)
	revived      []string
	mobsKilled   int
	hide         int  // the next n frames list no hostile (the bots lost sight of everything for a moment)
	truncating   bool // frames are marked truncated and list no hostile
	panicOn      string
	vanish       bool // after a pullback the mob leaves the world without a fight
	oldModule    bool // a bot-scoped `cmd attack` is answered party-scoped (the module ignores the bot name)
	ignorePull   bool // a bot-scoped `cmd attack` at an engaged mob changes nothing (the tank does not take it)
	orderAt      []time.Time
	sight        float64 // when > 0 the bots list only the hostiles within this many yards of the lead bot (line of sight)
	// followTo, when set, is where `cmd follow` runs the lead bot (to the commander), and a pull or an attack 20 s or more after it runs
	// it back to the mob, as the module does; nil: `cmd follow` moves nobody.
	followTo *Point
	followAt time.Time
	// the careful-walk tests (remote_gather_test.go): a bot in lagUntil stays where it stands, out of the formation, until that time; aggro
	// > 0 makes a mob that is not fighting yet come at the party when a living bot gets within that many yards of it (a walk into it).
	lagUntil map[packet.GUID]time.Time
	aggro    float64
	// minMove > 0: a `cmd move` to a point nearer the lead bot than this is acknowledged and does nothing, as the module does with a move
	// under about 6 yd. ccLasts > 0: a `cmd cc` puts the mob in crowd control (flag c, no target) for that long.
	minMove float64
	ccLasts time.Duration

	moveDelay     time.Duration // a `cmd move` takes effect this long after it is sent (the bot runs on to the old point meanwhile)
	pendingMove   *Point
	pendingAt     time.Time
	stay          *Point        // the point of the last move: the lead bot's stay spot
	returnToStay  bool          // after a fight the lead bot walks back to its stay spot, like the module's "return to stay position"
	returnDelay   time.Duration // ... this long after the fight ended
	returnAt      time.Time
	wasEngaged    bool
	trace         []Point // the lead bot's position after each advance
	moveOrderPos  []Point // the lead bot's position when each `cmd move` was sent
	moveOrderDest []Point // and the point it was sent to
	moveOrderAt   []time.Time
	killTimes     []time.Time // when each mob died

	// the enter-phase tests: memberMap puts single bots on another map than mapID; busyFor keeps the tank (the first bot) in combat that
	// long after a tank-busy refusal.
	memberMap   map[packet.GUID]uint32
	busyFor     time.Duration
	combatUntil time.Time

	// The bound party (remote_party.go): bots in left are out of the group (the feed and `commander status` do not list them) until a bind
	// names them; leaveOnBind lists the bots that drop out right after a bind (once, or after every bind when stubborn); joinErr[name] is
	// the code the bind refuses to invite that bot with.
	left        map[string]bool
	lateJoin    map[string]time.Time // a bot in left joins the group (is loaded, arrives) at that time
	leaveOnBind []string
	stubborn    bool
	joinErr     map[string]string
}

func newFakeRemote(t *testing.T) *fakeRemote {
	g := &fakeRemote{t: t, now: time.Unix(1_000_000, 0), speed: 10, engageDelay: 2 * time.Second, holdDelay: 0, killRate: 25, healRate: 20, cmdErr: map[string][]string{}}
	mk := func(guid packet.GUID, name string, ptype uint8, hp uint32) *tbm.PartyRec {
		return &tbm.PartyRec{GUID: guid, Name: name, HP: hp, MaxHP: 1000, Power: 1000, MaxPower: 1000, PowerType: ptype, State: "n"}
	}
	g.party = []*tbm.PartyRec{mk(gTank, "Tanky", 1, 1000), mk(gHealer, "Healy", 0, 1000), mk(gMage, "Magey", 0, 1000)}
	return g
}

func (g *fakeRemote) lead() *tbm.PartyRec { return g.party[0] }

// leadDist is how far the mob stands from the lead bot.
func (g *fakeRemote) leadDist(m *rmob) float64 {
	dx, dy := float64(m.pos.X-g.lead().X), float64(m.pos.Y-g.lead().Y)
	return sqrt(dx*dx + dy*dy)
}

func (g *fakeRemote) clock() Clock {
	return Clock{Now: func() time.Time { return g.now }, Sleep: func(ctx context.Context, d time.Duration) error {
		if err := ctx.Err(); err != nil {
			return err
		}
		g.advance(d)
		return nil
	}}
}

func (g *fakeRemote) addMob(entry uint32, at Point) *rmob {
	m := &rmob{guid: packet.GUID(0xF130000000000000 | uint64(entry)<<24 | uint64(len(g.mobs)+1)), entry: entry, pos: at, hp: 100, flags: "-"}
	g.mobs = append(g.mobs, m)
	return m
}

func (g *fakeRemote) advance(d time.Duration) {
	g.now = g.now.Add(d)
	for n, at := range g.lateJoin {
		if !g.now.Before(at) {
			delete(g.left, n)
			delete(g.lateJoin, n)
		}
	}
	sec := d.Seconds()
	if g.pendingMove != nil && !g.now.Before(g.pendingAt) {
		g.moveTo, g.pendingMove = g.pendingMove, nil
	}
	// the lead bot walks
	if g.moveTo != nil {
		l := g.lead()
		dx, dy := float64(g.moveTo.X-l.X), float64(g.moveTo.Y-l.Y)
		dist := dx*dx + dy*dy
		step := g.speed * sec
		if dist <= step*step {
			l.X, l.Y, l.Z = g.moveTo.X, g.moveTo.Y, g.moveTo.Z
			g.moveTo = nil
		} else {
			f := step / sqrt(dist)
			l.X, l.Y = l.X+float32(dx*f), l.Y+float32(dy*f)
		}
	}
	for _, p := range g.party[1:] {
		if until, ok := g.lagUntil[p.GUID]; ok && g.now.Before(until) {
			continue
		}
		p.X, p.Y, p.Z = g.lead().X, g.lead().Y, g.lead().Z
	}
	if g.aggro > 0 {
		for _, m := range g.mobs {
			if m.pulled || m.engaged {
				continue
			}
			for _, p := range g.party {
				if dx, dy := float64(m.pos.X-p.X), float64(m.pos.Y-p.Y); !p.Dead && sqrt(dx*dx+dy*dy) <= g.aggro {
					m.pulled, m.pullAt, m.aggroed = true, g.now.Add(-g.engageDelay), true
					break
				}
			}
		}
	}
	// the fight
	live := g.mobs[:0]
	anyEngaged := false
	for _, m := range g.mobs {
		if !m.ccUntil.IsZero() && !g.now.Before(m.ccUntil) {
			m.ccUntil, m.flags, m.noTarget = time.Time{}, "-", false // the sheep runs out
		}
		if m.pulled && !m.engaged && g.now.Sub(m.pullAt) >= g.engageDelay {
			m.engaged = true
		}
		if m.engaged {
			m.target = gTank
			if g.holdDelay < 0 || g.now.Sub(m.pullAt) < g.holdDelay {
				m.target = gMage
			}
			if m.hold != 0 {
				m.target = m.hold
			}
			if m.noTarget {
				m.target = 0
			}
			anyEngaged = anyEngaged || !m.fleeing
			if m.fleeAt > 0 && !m.back {
				if !m.fleeing && m.hp <= m.fleeAt {
					m.fleeing, m.fleeStart, m.flags, m.hit, m.fleeOrigin = true, g.now, "c", false, m.pos
				}
				if m.fleeing && g.now.Sub(m.fleeStart) < m.fleeFor {
					l := g.lead()
					dx, dy := float64(m.pos.X-l.X), float64(m.pos.Y-l.Y)
					if n := sqrt(dx*dx + dy*dy); n > 0 {
						m.pos.X += float32(dx / n * m.fleeSpeed * sec)
						m.pos.Y += float32(dy / n * m.fleeSpeed * sec)
					}
				} else if m.fleeing && m.comesBack {
					l := g.lead()
					m.fleeing, m.back, m.flags, m.backAt = false, true, "-", g.now
					m.pos = Point{l.X + 6, l.Y, l.Z}
					for _, f := range m.friends {
						f.pos = Point{l.X + 8, l.Y + 3, l.Z}
						f.pulled, f.pullAt, f.engaged = true, g.now, true
					}
				}
			}
			if m.attacked && (!m.fleeing || m.hit && !m.untouchable && dist3(m.pos, m.fleeOrigin) <= 12) {
				m.hp -= g.killRate * sec
			}
		}
		if m.hp > 0 {
			live = append(live, m)
		} else {
			g.mobsKilled++
			g.killTimes = append(g.killTimes, g.now)
		}
	}
	g.mobs = live
	for _, p := range g.party {
		if p.Dead {
			continue
		}
		switch {
		case anyEngaged:
			p.State = "c"
		default:
			p.State = "n"
			if g.resting {
				p.HP = min(p.MaxHP, p.HP+uint32(g.healRate*10*sec))
			}
		}
	}
	if g.now.Before(g.combatUntil) && !g.party[0].Dead {
		g.party[0].State = "c"
	}
	if !anyEngaged && g.allRested() {
		g.resting = false
	}
	if g.wasEngaged && !anyEngaged && g.returnToStay {
		g.returnAt = g.now.Add(g.returnDelay)
	}
	if !g.returnAt.IsZero() && !g.now.Before(g.returnAt) {
		g.returnAt = time.Time{}
		if g.stay != nil && g.moveTo == nil && g.pendingMove == nil {
			g.moveTo = &Point{g.stay.X, g.stay.Y, g.stay.Z}
		}
	}
	g.wasEngaged = anyEngaged
	g.trace = append(g.trace, Point{g.lead().X, g.lead().Y, g.lead().Z})
	if g.onTick != nil {
		g.onTick(g)
	}
	if g.noFrames || (!g.cutAt.IsZero() && !g.now.Before(g.cutAt)) {
		return
	}
	g.publish()
}

func sqrt(v float64) float64 {
	x := v
	for i := 0; i < 40 && x > 0; i++ {
		x = (x + v/x) / 2
	}
	return x
}

func (g *fakeRemote) allRested() bool {
	for _, p := range g.party {
		if p.HP < p.MaxHP {
			return false
		}
	}
	return true
}

func (g *fakeRemote) publish() {
	g.n++
	f := tbm.ViewFrame{Version: "v1", Seq: g.n, Lead: g.lead().GUID, Party: []tbm.PartyRec{}, Hostiles: []tbm.HostileRec{}, Threats: []tbm.ThreatRec{}, At: g.now}
	for _, p := range g.party {
		if g.left[p.Name] {
			continue
		}
		c := *p
		c.Map = g.mapID
		if m, ok := g.memberMap[p.GUID]; ok {
			c.Map = m
		}
		f.Party = append(f.Party, c)
	}
	for _, m := range g.mobs {
		if dx, dy := float64(m.pos.X-g.lead().X), float64(m.pos.Y-g.lead().Y); g.sight > 0 && !m.engaged && sqrt(dx*dx+dy*dy) > g.sight {
			continue
		}
		if m.fleeing && m.hideRun && g.now.Sub(m.fleeStart) > 2*time.Second {
			continue
		}
		h := tbm.HostileRec{GUID: m.guid, Entry: m.entry, X: m.pos.X, Y: m.pos.Y, Z: m.pos.Z, HPPct: int(m.hp), Target: m.target, Flags: m.flags, Reaction: m.reaction, Kind: m.kind}
		f.Hostiles = append(f.Hostiles, h)
		if m.engaged {
			top := []tbm.ThreatEntry{{GUID: m.target, Pct: 100}, {GUID: gHealer, Pct: 30}}
			if m.noTarget {
				top = []tbm.ThreatEntry{{GUID: gMage, Pct: 100}}
			} else if m.target != gTank && m.tankPct > 0 {
				top = []tbm.ThreatEntry{{GUID: m.target, Pct: 100}, {GUID: gTank, Pct: m.tankPct}}
			}
			f.Threats = append(f.Threats, tbm.ThreatRec{Hostile: m.guid, Top: top})
		}
	}
	if g.hide > 0 {
		g.hide--
		f.Hostiles, f.Threats = []tbm.HostileRec{}, []tbm.ThreatRec{}
	}
	if g.truncating {
		f.Truncated = true
		f.Hostiles, f.Threats = []tbm.HostileRec{}, []tbm.ThreatRec{}
	}
	g.frame, g.have = f, true
	g.stats.Frames++
	g.stats.LastSeq = g.n
}

func ack(intent string) tbm.Reply {
	a := tbm.Ack{Intent: intent, Scope: "party", Count: 3, Executor: "Tanky"}
	return tbm.Reply{Kind: tbm.KindAck, OK: true, Ack: &a, Lines: []string{}}
}

func errReply(intent, code string) tbm.Reply {
	e := tbm.Err{Intent: intent, Code: code, Message: code + " message"}
	return tbm.Reply{Kind: tbm.KindErr, Err: &e, Lines: []string{}}
}

// tooFarReply is the module's answer to a direct step with a follower away from the lead bot (TortoiseBots #948).
func tooFarReply(intent, bot, lead string) tbm.Reply {
	e := tbm.Err{Intent: intent, Code: "too-far", Message: bot + " is not with " + lead + " (no navmesh path of at most 40 yd to its spot, and not within 5 yd of it on its floor in its sight). A direct step takes the lead bot's line: gather the party on it first."}
	return tbm.Reply{Kind: tbm.KindErr, Err: &e, Lines: []string{}}
}

func (g *fakeRemote) Command(ctx context.Context, command string) (tbm.Reply, error) {
	g.orders = append(g.orders, command)
	g.orderAt = append(g.orderAt, g.now)
	if g.panicOn != "" && strings.HasPrefix(command, g.panicOn) {
		g.panicOn = ""
		panic("boom")
	}
	f := strings.Fields(command)
	intent := f[0] + " " + f[1]
	if codes := g.cmdErr[f[1]]; f[0] == "cmd" && len(codes) > 0 {
		g.cmdErr[f[1]] = codes[1:]
		if codes[0] == "tank-busy" {
			g.combatUntil = g.now.Add(g.busyFor)
		}
		return errReply(intent, codes[0]), nil
	}
	switch intent {
	case "commander bind":
		if g.bindErr != "" {
			return errReply(intent, g.bindErr), nil
		}
		if g.bound {
			return errReply(intent, "already-bound"), nil
		}
		for _, n := range f[3:] {
			if code := g.joinErr[n]; code != "" && g.left[n] {
				return errReply(intent, code), nil
			}
		}
		for _, n := range f[3:] {
			delete(g.left, n)
		}
		g.bound = true
		r := ack(intent)
		r.Ack.Count = len(g.party) // the ACK counts the party as it was bound; a bot may leave a moment later
		for _, n := range g.leaveOnBind {
			if g.left == nil {
				g.left = map[string]bool{}
			}
			g.left[n] = true
		}
		if !g.stubborn {
			g.leaveOnBind = nil
		}
		return r, nil
	case "commander view":
		return ack(intent), nil
	case "commander status":
		if g.status != nil {
			return tbm.Reply{Kind: tbm.KindAck, OK: true, Ack: g.status, Lines: []string{}}, nil
		}
		if !g.bound {
			return errReply(intent, "not-bound"), nil
		}
		r := ack(intent)
		var names []string
		for _, p := range g.party {
			if !g.left[p.Name] {
				names = append(names, p.Name)
			}
		}
		r.Ack.Count = len(names)
		r.Lines = []string{fmt.Sprintf("You command %s's group: %s.", g.lead().Name, strings.Join(names, ", "))}
		return r, nil
	case "commander unbind":
		g.unbinds++
		if !g.bound {
			return errReply(intent, "not-bound"), nil
		}
		g.bound = false
		return ack(intent), nil
	case "cmd move":
		if len(f) > 5 && f[5] == "direct" {
			if g.noDirect {
				return errReply(intent, "invalid"), nil
			}
			if g.farOnce != "" {
				name := g.farOnce
				g.farOnce = ""
				for _, p := range g.party {
					if p.Name == name {
						p.X, p.Y, p.Z = g.lead().X-10, g.lead().Y, g.lead().Z
						if g.lagUntil == nil {
							g.lagUntil = map[packet.GUID]time.Time{}
						}
						g.lagUntil[p.GUID] = g.now.Add(time.Hour)
					}
				}
				return tooFarReply(intent, name, g.lead().Name), nil
			}
			if g.strictDirect {
				for _, p := range g.party {
					if !p.Dead && !g.left[p.Name] && p != g.lead() && dist3(Point{p.X, p.Y, p.Z}, Point{g.lead().X, g.lead().Y, g.lead().Z}) > 5 {
						return tooFarReply(intent, p.Name, g.lead().Name), nil
					}
				}
			}
			if len(g.directErrs) > 0 {
				c := g.directErrs[0]
				g.directErrs = g.directErrs[1:]
				return errReply(intent, c), nil
			}
		} else if len(f) != 5 {
			return errReply(intent, "invalid"), nil
		}
		if len(g.moveErrs) > 0 {
			c := g.moveErrs[0]
			g.moveErrs = g.moveErrs[1:]
			return errReply(intent, c), nil
		}
		x, _ := strconv.ParseFloat(f[2], 32)
		y, _ := strconv.ParseFloat(f[3], 32)
		z, _ := strconv.ParseFloat(f[4], 32)
		l := g.lead()
		if g.moveFetches && len(f) == 5 {
			for _, p := range g.party[1:] {
				if _, lag := g.lagUntil[p.GUID]; lag {
					p.X, p.Y, p.Z = float32(x), float32(y), float32(z)
					delete(g.lagUntil, p.GUID)
				}
			}
		}
		if dx, dy := x-float64(l.X), y-float64(l.Y); sqrt(dx*dx+dy*dy) > 150 {
			return errReply(intent, "too-far"), nil
		}
		if dx, dy := x-float64(l.X), y-float64(l.Y); g.minMove > 0 && sqrt(dx*dx+dy*dy) < g.minMove {
			g.moveOrderDest = append(g.moveOrderDest, Point{float32(x), float32(y), float32(z)})
			return ack(intent), nil // too short: the module does not move
		}
		g.moveOrderPos = append(g.moveOrderPos, Point{l.X, l.Y, l.Z})
		g.moveOrderDest = append(g.moveOrderDest, Point{float32(x), float32(y), float32(z)})
		g.moveOrderAt = append(g.moveOrderAt, g.now)
		if g.ignoreMoves > 0 {
			g.ignoreMoves--
			return ack(intent), nil
		}
		to := &Point{float32(x), float32(y), float32(z)}
		g.stay = to
		if g.moveDelay > 0 {
			g.pendingMove, g.pendingAt = to, g.now.Add(g.moveDelay)
			return ack(intent), nil
		}
		g.moveTo = to
		return ack(intent), nil
	case "cmd pullback", "cmd pull":
		m := g.mobByArg(f[2])
		if m == nil {
			return errReply(intent, "invalid-target"), nil
		}
		if m.inert {
			return ack(intent), nil
		}
		m.pulled, m.pullAt = true, g.now
		g.runToMob(m)
		if intent == "cmd pull" && m.engaged && !g.ignorePull {
			m.hold = 0 // the tank takes the mob
		}
		if g.vanish {
			g.mobs = nil
		}
		return ack(intent), nil
	case "cmd attack":
		m := g.mobByArg(f[2])
		if m == nil {
			return errReply(intent, "dead-target"), nil
		}
		if m.fleeing && !(len(f) > 3 && strings.EqualFold(f[3], g.party[0].Name)) {
			m.hit = true // a damage dealer is sent after the runner
		}
		if len(f) > 3 {
			if g.tankTooFar > 0 {
				g.tankTooFar--
				return tooFarReply(intent, f[3], "the mob"), nil
			}
			if g.strictAttack {
				for _, p := range g.party {
					if strings.EqualFold(p.Name, f[3]) {
						if dx, dy := float64(p.X-m.pos.X), float64(p.Y-m.pos.Y); sqrt(dx*dx+dy*dy) > 60 {
							return tooFarReply(intent, p.Name, "the mob"), nil
						}
					}
				}
			}
			// bot-scoped (#928): only that bot goes; the tank takes the mob, the party does not join
			if m.engaged && !g.ignorePull && strings.EqualFold(f[3], g.party[0].Name) {
				m.hold = 0
			}
			r := ack(intent)
			if !g.oldModule {
				r.Ack.Scope = "bot:" + f[3]
			}
			return r, nil
		}
		m.attacked = true
		g.runToMob(m)
		return ack(intent), nil
	case "cmd cc":
		if len(f) < 4 {
			return ack(intent), nil
		}
		if m := g.mobByArg(f[3]); m != nil && g.ccLasts > 0 {
			m.ccUntil, m.flags, m.noTarget = g.now.Add(g.ccLasts), "c", true
		}
		return ack(intent), nil
	case "cmd follow":
		if g.followTo != nil {
			to := *g.followTo
			g.moveTo, g.pendingMove, g.followAt = &to, nil, g.now
		}
		return ack(intent), nil
	case "cmd summon":
		for _, p := range g.party {
			if strings.EqualFold(p.Name, f[2]) {
				p.X, p.Y, p.Z = g.lead().X, g.lead().Y, g.lead().Z
				delete(g.lagUntil, p.GUID)
				delete(g.memberMap, p.GUID)
				g.summoned = append(g.summoned, p.Name)
			}
		}
		return ack(intent), nil
	case "cmd rest":
		g.resting = true
		return ack(intent), nil
	case "cmd revive":
		for _, p := range g.party {
			if strings.EqualFold(p.Name, f[2]) && p.Dead {
				p.Dead, p.HP, p.State = false, p.MaxHP/2, "n"
				g.revived = append(g.revived, p.Name)
			}
		}
		return ack(intent), nil
	}
	return ack(intent), nil
}

// runToMob runs the lead bot back to a mob it pulls or attacks, when followTo is set and the follow is 20 s old.
func (g *fakeRemote) runToMob(m *rmob) {
	if g.followTo != nil && g.now.Sub(g.followAt) >= 20*time.Second {
		to := m.pos
		g.moveTo, g.pendingMove = &to, nil
	}
}

func (g *fakeRemote) mobByArg(a string) *rmob {
	v, err := packet.ParseGUID(a)
	if err != nil {
		return nil
	}
	for _, m := range g.mobs {
		if m.guid == v {
			return m
		}
	}
	return nil
}

func (g *fakeRemote) Frame() (tbm.ViewFrame, uint64, bool)        { return g.frame, g.n, g.have }
func (g *fakeRemote) ResetView()                                  { g.have = false }
func (g *fakeRemote) BindingEnds() []tbm.BindingEnd               { return g.ends }
func (g *fakeRemote) ViewStats() tbm.ViewStats                    { return g.stats }
func (g *fakeRemote) Emit(kind, text string, data map[string]any) {}

// ordersOf are the orders that start with prefix, in order.
func (g *fakeRemote) ordersOf(prefix string) []string {
	var out []string
	for _, o := range g.orders {
		if strings.HasPrefix(o, prefix) {
			out = append(out, o)
		}
	}
	return out
}

func (g *fakeRemote) lastIndexOf(prefix string) int {
	for i := len(g.orders) - 1; i >= 0; i-- {
		if strings.HasPrefix(g.orders[i], prefix) {
			return i
		}
	}
	return -1
}

func (g *fakeRemote) indexOf(prefix string) int {
	for i, o := range g.orders {
		if strings.HasPrefix(o, prefix) {
			return i
		}
	}
	return -1
}

func remotePlan(legs ...Leg) *Plan {
	return &Plan{Version: 1, Name: "rt", Map: 0, Legs: legs}
}

func opts() RemoteOptions { return RemoteOptions{Lead: "Tanky", Bots: []string{"Healy", "Magey"}} }

func runRemote(t *testing.T, g interface {
	RemoteGame
	clock() Clock
}, plan *Plan, o RemoteOptions, mod func(*Config)) (Report, error) {
	t.Helper()
	cfg := DefaultConfig()
	cfg.PostFight = 2 * time.Second
	cfg.AddsWait = 4 * time.Second
	if mod != nil {
		mod(&cfg)
	}
	d := NewRemoteDriver(plan, g, cfg, o, g.clock())
	err := d.Run(context.Background(), 0)
	rep, ended := d.Status()
	if !ended {
		t.Fatal("the report does not say the run ended")
	}
	return rep, err
}

func TestRemoteRunHappyPath(t *testing.T) {
	g := newFakeRemote(t)
	g.addMob(299, Point{150, 5, 0})
	plan := remotePlan(Leg{Name: "first", To: Point{100, 0, 0}, PullGroups: []PullGroup{{Entries: []uint32{299}, Positions: []Point{{150, 5, 0}}}}},
		Leg{Name: "second", To: Point{200, 0, 0}})
	rep, err := runRemote(t, g, plan, opts(), nil)
	if err != nil || rep.State != "done" {
		t.Fatalf("%v %s %v", err, rep.State, rep.Error)
	}
	// Bind first (the lead and the others), view on, then orders; unbind last.
	if g.orders[0] != "commander bind Tanky Healy Magey" || g.orders[1] != "commander view on" || g.orders[len(g.orders)-1] != "commander unbind" {
		t.Fatalf("orders: %v", g.orders)
	}
	if g.unbinds != 1 || g.bound {
		t.Fatalf("unbinds %d bound %v", g.unbinds, g.bound)
	}
	// Walk = cmd move; pull = cmd pullback at the mob's guid; the party attack only after the pull.
	if len(g.ordersOf("cmd move")) < 2 {
		t.Fatalf("moves: %v", g.ordersOf("cmd move"))
	}
	pb, at := g.indexOf("cmd pullback"), g.indexOf("cmd attack")
	if pb < 0 || at < pb {
		t.Fatalf("pullback %d attack %d: %v", pb, at, g.orders)
	}
	if !strings.Contains(g.orders[pb], "0xF130") {
		t.Fatalf("the pull has no guid: %s", g.orders[pb])
	}
	// The report flags remote mode, the binding, the feed and the orders.
	r := rep.Remote
	if r == nil || r.Mode != "remote" || r.LeadBot != "Tanky" || !r.Bound || !r.Unbound || r.UnbindVia != "ack" || r.GMCommands != 0 {
		t.Fatalf("remote %+v", r)
	}
	if r.FramesReceived == 0 || r.OrdersSent < 5 || r.Orders["pullback"] != 1 || r.Orders["move"] < 2 || r.OrdersRefused != 0 {
		t.Fatalf("remote %+v", r)
	}
	if rep.LeaderGM || len(rep.Shortcuts) != 0 || rep.Wipes != 0 {
		t.Fatalf("a remote run used GM: %+v", rep)
	}
	if len(rep.Legs) != 2 || rep.Legs[0].Pulls != 1 || rep.Legs[0].Result != "done" {
		t.Fatalf("legs %+v", rep.Legs)
	}
	if r.ThreatHolds != 1 || r.Tank != "Tanky" {
		t.Fatalf("threat %+v", r)
	}
	if _, err := json.Marshal(rep); err != nil {
		t.Fatal(err)
	}
}

func TestRemoteWalkInLegs(t *testing.T) {
	g := newFakeRemote(t)
	plan := remotePlan(Leg{Name: "far", To: Point{400, 0, 0}})
	rep, err := runRemote(t, g, plan, opts(), nil)
	if err != nil {
		t.Fatalf("%v %+v", err, rep.Notes)
	}
	moves := g.ordersOf("cmd move")
	if len(moves) < 4 {
		t.Fatalf("a 400 yd walk took %d moves: %v", len(moves), moves)
	}
	if g.lead().X < 395 {
		t.Fatalf("lead at %v", g.lead().X)
	}
	for _, m := range g.orders {
		if strings.HasPrefix(m, "cmd move") && g.orders != nil {
			// no move was refused as too-far
		}
	}
	if rep.Remote.Refused["move:too-far"] != 0 {
		t.Fatalf("refused: %v", rep.Remote.Refused)
	}
}

func TestRemoteWalkReissuesAStalledMove(t *testing.T) {
	g := newFakeRemote(t)
	g.ignoreMoves = 1 // the first move does nothing
	plan := remotePlan(Leg{Name: "a", To: Point{60, 0, 0}})
	o := opts()
	o.CarefulHop = 1000 // one leg straight to the point, not the careful walk's hops
	rep, err := runRemote(t, g, plan, o, nil)
	if err != nil {
		t.Fatalf("%v", err)
	}
	if rep.Remote.MoveStalls != 1 || len(g.ordersOf("cmd move")) != 2 {
		t.Fatalf("stalls %d moves %v", rep.Remote.MoveStalls, g.ordersOf("cmd move"))
	}
}

func TestRemoteWalkGivesUpOnAStuckLead(t *testing.T) {
	g := newFakeRemote(t)
	g.ignoreMoves = 100
	plan := remotePlan(Leg{Name: "a", To: Point{60, 0, 0}})
	rep, err := runRemote(t, g, plan, opts(), nil)
	if err == nil || !strings.Contains(err.Error(), "gained no ground") || rep.State != "failed" {
		t.Fatalf("%v %s", err, rep.State)
	}
	if g.unbinds != 1 {
		t.Fatalf("unbinds %d", g.unbinds)
	}
}

func TestRemoteWalkRefusedMoveGetsAShorterLeg(t *testing.T) {
	g := newFakeRemote(t)
	g.moveErrs = []string{"off-mesh", "unreachable"}
	plan := remotePlan(Leg{Name: "a", To: Point{100, 0, 0}})
	rep, err := runRemote(t, g, plan, opts(), nil)
	if err != nil {
		t.Fatalf("%v", err)
	}
	if rep.Remote.OrdersRefused != 2 || rep.Remote.Refused["move:off-mesh"] != 1 || len(rep.Refusals) != 2 || rep.Refusals[0].Intent != "cmd move" {
		t.Fatalf("%+v %+v", rep.Remote, rep.Refusals)
	}
	// A move that is always refused fails the walk with the reason.
	g2 := newFakeRemote(t)
	g2.moveErrs = []string{"off-mesh", "off-mesh", "off-mesh", "off-mesh", "off-mesh", "off-mesh", "off-mesh", "off-mesh", "off-mesh", "off-mesh"}
	_, err = runRemote(t, g2, plan, opts(), nil)
	if err == nil || !strings.Contains(err.Error(), "off-mesh") || g2.unbinds != 1 {
		t.Fatalf("%v unbinds %d", err, g2.unbinds)
	}
}

func TestRemoteRestGate(t *testing.T) {
	g := newFakeRemote(t)
	for _, p := range g.party {
		p.HP = 400
	}
	plan := remotePlan(Leg{Name: "a", To: Point{20, 0, 0}})
	rep, err := runRemote(t, g, plan, opts(), nil)
	if err != nil {
		t.Fatalf("%v", err)
	}
	if len(g.ordersOf("cmd rest")) != 1 || rep.Legs[0].RestSeconds <= 0 {
		t.Fatalf("rest orders %v, rest seconds %v", g.ordersOf("cmd rest"), rep.Legs[0].RestSeconds)
	}
	for _, p := range g.party {
		if p.HPPct() < 90 {
			t.Fatalf("%s at %d%%", p.Name, p.HPPct())
		}
	}
}

func TestRemoteRestGateWaitsForMana(t *testing.T) {
	g := newFakeRemote(t)
	g.party[1].Power = 100 // the healer is at 10% and does not drink
	plan := remotePlan(Leg{Name: "a", To: Point{20, 0, 0}})
	rep, err := runRemote(t, g, plan, opts(), func(c *Config) { c.RestTimeout = 20 * time.Second })
	// Below HealerManaMin the gate is waited out; everyone is healthy, so the run goes on after the timeout.
	if err != nil {
		t.Fatalf("%v", err)
	}
	found := false
	for _, n := range rep.Notes {
		found = found || strings.Contains(n, "rest gate gave up")
	}
	if !found {
		t.Fatalf("notes %v", rep.Notes)
	}
}

func TestRemoteThreatGate(t *testing.T) {
	// The mob goes onto the tank 4 s after the pull: the party is held back until then, then sent in.
	g := newFakeRemote(t)
	g.holdDelay = 4 * time.Second
	g.addMob(299, Point{30, 0, 0})
	plan := remotePlan(Leg{Name: "a", To: Point{0, 0, 0}, PullGroups: []PullGroup{{Entries: []uint32{299}, Positions: []Point{{30, 0, 0}}}}})
	o1 := opts()
	o1.Tank = "Tanky"
	rep, err := runRemote(t, g, plan, o1, func(c *Config) { c.RestTimeout = 5 * time.Second })
	if err != nil {
		t.Fatalf("%v", err)
	}
	if rep.Remote.ThreatWaits != 1 || rep.Remote.ThreatHolds != 1 || rep.Remote.ThreatOverrides != 0 {
		t.Fatalf("%+v", rep.Remote)
	}
	// Never on the tank: the party goes in after ThreatWait.
	g2 := newFakeRemote(t)
	g2.holdDelay = -1
	g2.addMob(299, Point{30, 0, 0})
	rep, err = runRemote(t, g2, plan, opts(), func(c *Config) { c.RestTimeout = 5 * time.Second })
	if err != nil {
		t.Fatalf("%v", err)
	}
	if rep.Remote.ThreatOverrides != 1 || g2.indexOf("cmd attack") < 0 {
		t.Fatalf("%+v %v", rep.Remote, g2.orders)
	}
	// A named tank is the one that has to hold: the mage holding the mob does not count.
	g3 := newFakeRemote(t)
	g3.holdDelay = -1
	g3.addMob(299, Point{30, 0, 0})
	o := opts()
	o.Tank = "Tanky"
	rep, err = runRemote(t, g3, plan, o, func(c *Config) { c.RestTimeout = 5 * time.Second })
	if err != nil || rep.Remote.ThreatHolds != 0 || rep.Remote.ThreatOverrides != 1 {
		t.Fatalf("%v %+v", err, rep.Remote)
	}
}

func TestRemotePullRefusals(t *testing.T) {
	plan := remotePlan(Leg{Name: "a", To: Point{0, 0, 0}, PullGroups: []PullGroup{{Entries: []uint32{299}, Positions: []Point{{30, 0, 0}}}}})
	// unreachable: the party is walked nearer and the mob is pulled again from there (the group is not cleared with the mob alive).
	g := newFakeRemote(t)
	g.addMob(299, Point{30, 0, 0})
	g.cmdErr["pullback"] = []string{"unreachable"}
	rep, err := runRemote(t, g, plan, opts(), nil)
	if err != nil {
		t.Fatalf("%v", err)
	}
	if rep.Remote.OrdersRefused != 1 || len(rep.Refusals) != 1 || rep.Refusals[0].Code != "unreachable" || rep.Refusals[0].Intent != "cmd pullback" {
		t.Fatalf("%+v %+v", rep.Remote, rep.Refusals)
	}
	if len(g.ordersOf("cmd pullback")) != 2 || g.mobsKilled != 1 || rep.Remote.Unreachable != 1 {
		t.Fatalf("the refused mob was not pulled again: %v %+v", g.orders, rep.Remote)
	}
	// no-tank: the party is sent in with an attack order.
	g = newFakeRemote(t)
	m := g.addMob(299, Point{30, 0, 0})
	g.cmdErr["pullback"] = []string{"no-tank"}
	m.pulled, m.pullAt = false, g.now
	rep, err = runRemote(t, g, plan, opts(), func(c *Config) { c.EngageTimeout = 10 * time.Second })
	_ = err
	if rep.Remote.PullFallbacks != 1 || g.indexOf("cmd attack") < 0 {
		t.Fatalf("%+v %v", rep.Remote, g.orders)
	}
	// too-far: the lead bot walks closer and the pull is sent again.
	g = newFakeRemote(t)
	g.addMob(299, Point{30, 0, 0})
	g.cmdErr["pullback"] = []string{"too-far"}
	if _, err = runRemote(t, g, plan, opts(), nil); err != nil || len(g.ordersOf("cmd pullback")) != 2 {
		t.Fatalf("%v %v", err, g.orders)
	}
	// dead-target: picked again (the mob is still there for the fake): the second pull goes.
	g = newFakeRemote(t)
	g.addMob(299, Point{30, 0, 0})
	g.cmdErr["pullback"] = []string{"dead-target"}
	if _, err = runRemote(t, g, plan, opts(), nil); err != nil || len(g.ordersOf("cmd pullback")) != 2 {
		t.Fatalf("%v %v", err, g.orders)
	}
}

func TestRemoteBindRefused(t *testing.T) {
	for code, want := range map[string]string{"disabled": "RemoteCommander is off", "no-bot": "not an online bot"} {
		g := newFakeRemote(t)
		g.bindErr = code
		rep, err := runRemote(t, g, remotePlan(Leg{Name: "a", To: Point{10, 0, 0}}), opts(), nil)
		if err == nil || !strings.Contains(err.Error(), want) || rep.State != "failed" {
			t.Fatalf("%s: %v", code, err)
		}
		// A refused bind is no binding: nothing to unbind.
		if g.unbinds != 0 || len(g.ordersOf("cmd")) != 0 || rep.Remote.Bound {
			t.Fatalf("%s: unbinds %d orders %v", code, g.unbinds, g.orders)
		}
	}
}

func TestRemoteNoFeed(t *testing.T) {
	g := newFakeRemote(t)
	g.noFrames = true
	rep, err := runRemote(t, g, remotePlan(Leg{Name: "a", To: Point{10, 0, 0}}), opts(), nil)
	if err == nil || !strings.Contains(err.Error(), "no TBM:VIEW frame") || g.unbinds != 1 || rep.State != "failed" {
		t.Fatalf("%v unbinds %d", err, g.unbinds)
	}
}

func TestRemoteFeedLostMidRun(t *testing.T) {
	g := newFakeRemote(t)
	g.cutAt = g.now.Add(30 * time.Second)
	plan := remotePlan(Leg{Name: "a", To: Point{3000, 0, 0}})
	_, err := runRemote(t, g, plan, opts(), nil)
	if err == nil || !strings.Contains(err.Error(), "view feed stopped") || g.unbinds != 1 {
		t.Fatalf("%v unbinds %d", err, g.unbinds)
	}
}

func TestRemoteWrongMap(t *testing.T) {
	g := newFakeRemote(t)
	g.mapID = 70
	_, err := runRemote(t, g, remotePlan(Leg{Name: "a", To: Point{10, 0, 0}}), opts(), nil)
	if err == nil || !strings.Contains(err.Error(), "map 70") || g.unbinds != 1 {
		t.Fatalf("%v unbinds %d", err, g.unbinds)
	}
}

func TestRemoteObjectStepsFailClearly(t *testing.T) {
	g := newFakeRemote(t)
	plan := remotePlan(Leg{Name: "a", To: Point{20, 0, 0}, Steps: []Step{{UseObject: &UseObject{ObjectRef: ObjectRef{Entry: 124371}}}}})
	rep, err := runRemote(t, g, plan, opts(), nil)
	if err == nil || !strings.Contains(err.Error(), "remote mode: object step needs the agent nearby") || !strings.Contains(err.Error(), "use_object") {
		t.Fatalf("%v", err)
	}
	if g.unbinds != 1 || rep.State != "failed" || rep.Remote.ObjectStepsAhead != 1 {
		t.Fatalf("unbinds %d %s %+v", g.unbinds, rep.State, rep.Remote)
	}
	// No order was sent for the step itself.
	if len(g.ordersOf("cmd move")) == 0 || g.indexOf("cmd use") >= 0 {
		t.Fatalf("%v", g.orders)
	}
}

func TestRemoteHostilesGoneWait(t *testing.T) {
	g := newFakeRemote(t)
	g.addMob(299, Point{0, 0, 0})
	g.onTick = func(g *fakeRemote) {
		if g.now.After(time.Unix(1_000_000, 0).Add(8 * time.Second)) {
			g.mobs = nil
		}
	}
	plan := remotePlan(Leg{Name: "a", To: Point{0, 0, 0}, Steps: []Step{{WaitFor: &WaitFor{HostilesGone: &HostilesGone{Radius: 30}, TimeoutS: 60}}}})
	// The hostile is in the feed, not near the agent: the wait looks at the feed.
	if _, err := runRemote(t, g, plan, opts(), nil); err != nil {
		t.Fatalf("%v", err)
	}
}

func TestRemoteWipe(t *testing.T) {
	plan := remotePlan(Leg{Name: "a", To: Point{0, 0, 0}, PullGroups: []PullGroup{{Entries: []uint32{299}, Positions: []Point{{30, 0, 0}}}}})
	kill := func(g *fakeRemote) {
		for _, m := range g.mobs {
			if m.engaged && g.now.After(m.pullAt.Add(5*time.Second)) {
				for _, p := range g.party {
					p.Dead, p.HP = true, 0
				}
			}
		}
	}
	g := newFakeRemote(t)
	g.addMob(299, Point{30, 0, 0})
	g.killRate = 0
	g.onTick = kill
	rep, err := runRemote(t, g, plan, opts(), nil)
	if err == nil || err.Error() != "wipe" || rep.Wipes != 1 || len(rep.Deaths) != 3 || g.unbinds != 1 {
		t.Fatalf("%v wipes %d deaths %d unbinds %d", err, rep.Wipes, len(rep.Deaths), g.unbinds)
	}
	// With recover_wipe the party is revived by order and the pull is tried again.
	g = newFakeRemote(t)
	g.addMob(299, Point{30, 0, 0})
	g.killRate = 25
	first := true
	g.onTick = func(g *fakeRemote) {
		if first {
			for _, m := range g.mobs {
				if m.engaged {
					first = false
					for _, p := range g.party {
						p.Dead, p.HP = true, 0
					}
					m.hp, m.engaged, m.pulled, m.attacked, m.target = 100, false, false, false, 0
				}
			}
		}
	}
	rep, err = runRemote(t, g, plan, opts(), func(c *Config) { c.RecoverWipe = true })
	if err != nil || rep.Wipes != 1 || len(g.revived) != 3 || rep.Remote.Revives < 3 {
		t.Fatalf("%v wipes %d revived %v %+v", err, rep.Wipes, g.revived, rep.Remote)
	}
}

func TestRemoteDeadMemberAtRestGateIsRevived(t *testing.T) {
	g := newFakeRemote(t)
	g.party[2].Dead, g.party[2].HP = true, 0
	rep, err := runRemote(t, g, remotePlan(Leg{Name: "a", To: Point{20, 0, 0}}), opts(), nil)
	if err != nil || len(g.revived) != 1 || g.revived[0] != "Magey" || rep.Remote.Revives != 1 || len(rep.Shortcuts) != 0 {
		t.Fatalf("%v %v %+v", err, g.revived, rep.Remote)
	}
	// A revive that is refused fails the run, and the run unbinds.
	g = newFakeRemote(t)
	g.party[2].Dead, g.party[2].HP = true, 0
	g.cmdErr["revive"] = []string{"not-owner"}
	_, err = runRemote(t, g, remotePlan(Leg{Name: "a", To: Point{20, 0, 0}}), opts(), nil)
	if err == nil || !strings.Contains(err.Error(), "revive order was refused") || g.unbinds != 1 {
		t.Fatalf("%v", err)
	}
}

func TestRemoteBindingEndedByModule(t *testing.T) {
	g := newFakeRemote(t)
	g.onTick = func(g *fakeRemote) {
		if g.now.After(time.Unix(1_000_000, 0).Add(15*time.Second)) && len(g.ends) == 0 {
			g.ends = append(g.ends, tbm.BindingEnd{Reason: "the lead bot left the party", Count: 3, At: g.now})
		}
	}
	rep, err := runRemote(t, g, remotePlan(Leg{Name: "a", To: Point{3000, 0, 0}}), opts(), nil)
	if err == nil || !strings.Contains(err.Error(), "binding ended") || !strings.Contains(err.Error(), "left the party") || rep.Remote.EndedByModule == "" {
		t.Fatalf("%v %+v", err, rep.Remote)
	}
	if g.unbinds != 1 { // sent anyway; the module answers not-bound
		t.Fatalf("unbinds %d", g.unbinds)
	}
}

func TestRemoteNotBoundAnswerEndsTheRun(t *testing.T) {
	g := newFakeRemote(t)
	g.cmdErr["move"] = []string{"not-bound"}
	_, err := runRemote(t, g, remotePlan(Leg{Name: "a", To: Point{50, 0, 0}}), opts(), nil)
	if err == nil || !strings.Contains(err.Error(), "binding ended") {
		t.Fatalf("%v", err)
	}
}

func TestRemoteLeadUnavailableIsWaitedOut(t *testing.T) {
	g := newFakeRemote(t)
	g.cmdErr["move"] = []string{"lead-unavailable", "lead-unavailable"}
	rep, err := runRemote(t, g, remotePlan(Leg{Name: "a", To: Point{50, 0, 0}}), opts(), nil)
	if err != nil || rep.Remote.Refused["move:lead-unavailable"] != 2 {
		t.Fatalf("%v %+v", err, rep.Remote)
	}
	g = newFakeRemote(t)
	g.cmdErr["move"] = []string{"lead-unavailable", "lead-unavailable", "lead-unavailable", "lead-unavailable", "lead-unavailable", "lead-unavailable", "lead-unavailable", "lead-unavailable", "lead-unavailable", "lead-unavailable", "lead-unavailable", "lead-unavailable", "lead-unavailable", "lead-unavailable", "lead-unavailable", "lead-unavailable", "lead-unavailable", "lead-unavailable"}
	if _, err := runRemote(t, g, remotePlan(Leg{Name: "a", To: Point{50, 0, 0}}), opts(), nil); err == nil || !strings.Contains(err.Error(), "lead bot was unavailable") {
		t.Fatalf("%v", err)
	}
}

func TestRemoteUnbindOnStop(t *testing.T) {
	g := newFakeRemote(t)
	ctx, cancel := context.WithCancel(context.Background())
	g.onTick = func(g *fakeRemote) {
		if g.now.After(time.Unix(1_000_000, 0).Add(10 * time.Second)) {
			cancel()
		}
	}
	d := NewRemoteDriver(remotePlan(Leg{Name: "a", To: Point{3000, 0, 0}}), g, DefaultConfig(), opts(), g.clock())
	err := d.Run(ctx, 0)
	rep := d.Report()
	if err == nil || rep.State != "stopped" || g.unbinds != 1 || !rep.Remote.Unbound {
		t.Fatalf("%v %s unbinds %d %+v", err, rep.State, g.unbinds, rep.Remote)
	}
}

func TestRemoteCC(t *testing.T) {
	g := newFakeRemote(t)
	g.addMob(299, Point{30, 0, 0})
	sheep := g.addMob(300, Point{32, 0, 0})
	plan := remotePlan(Leg{Name: "a", To: Point{0, 0, 0}, PullGroups: []PullGroup{{Entries: []uint32{299, 300}, Positions: []Point{{30, 0, 0}}}}})
	o := opts()
	o.CC = []CCRule{{Entry: 300, Mark: "moon", Bot: "Magey"}}
	rep, err := runRemote(t, g, plan, o, nil)
	if err != nil {
		t.Fatalf("%v %+v", err, rep.Notes)
	}
	cc, pb := g.indexOf("cmd cc"), g.indexOf("cmd pullback")
	if cc < 0 || cc > pb || g.orders[cc] != "cmd cc moon "+tbm.GUIDArg(sheep.guid)+" Magey" || rep.Remote.CCOrders != 1 {
		t.Fatalf("cc %d pullback %d %v", cc, pb, g.orders)
	}
	// The sheep is not the pull target while another candidate is.
	if strings.Contains(g.orders[pb], tbm.GUIDArg(sheep.guid)) {
		t.Fatalf("the CC mob was pulled: %s", g.orders[pb])
	}
}

func TestRemoteWalkAggro(t *testing.T) {
	g := newFakeRemote(t)
	m := g.addMob(299, Point{200, 0, 0})
	// A pack that aggros the party on the walk: engaged without a pull.
	g.onTick = func(g *fakeRemote) {
		if g.lead().X > 90 && !m.engaged && m.hp > 0 {
			m.pulled, m.pullAt, m.engaged = true, g.now, true
		}
	}
	plan := remotePlan(Leg{Name: "a", To: Point{300, 0, 0}})
	rep, err := runRemote(t, g, plan, opts(), nil)
	if err != nil {
		t.Fatalf("%v %v", err, rep.Notes)
	}
	if rep.Remote.WalkAggros != 1 || rep.Legs[0].WalkAggros != 1 || g.indexOf("cmd attack") < 0 {
		t.Fatalf("%+v %v", rep.Remote, g.orders)
	}
	if g.lead().X < 295 {
		t.Fatalf("the walk did not go on: %v", g.lead().X)
	}
}

func TestRemoteBossKill(t *testing.T) {
	g := newFakeRemote(t)
	boss := g.addMob(777, Point{50, 0, 0})
	boss.hp = 100
	plan := remotePlan(Leg{Name: "b", To: Point{20, 0, 0}, Boss: &Boss{Entry: 777, Position: Point{50, 0, 0}}})
	rep, err := runRemote(t, g, plan, opts(), nil)
	if err != nil || len(g.mobs) != 0 || rep.Remote.Orders["pullback"] != 1 {
		t.Fatalf("%v mobs %d %+v", err, len(g.mobs), rep.Remote)
	}
}

func TestRemoteFeedStatsAndTruncation(t *testing.T) {
	g := newFakeRemote(t)
	g.stats.Frames, g.stats.Missed = 100, 5 // counters from before the run
	g.onTick = func(g *fakeRemote) { g.stats.Missed++; g.stats.Truncated++ }
	rep, err := runRemote(t, g, remotePlan(Leg{Name: "a", To: Point{50, 0, 0}}), opts(), nil)
	if err != nil {
		t.Fatal(err)
	}
	r := rep.Remote
	if r.FramesReceived == 0 || r.FramesReceived > 60 || r.FramesMissed == 0 || r.FramesMissed > 1000 || r.FramesTruncated == 0 {
		t.Fatalf("%+v", r)
	}
	if fmt.Sprint(rep.Remote.Bots) != "[Healy Magey]" {
		t.Fatalf("%v", rep.Remote.Bots)
	}
}

func TestRemoteRefusesWithoutLead(t *testing.T) {
	g := newFakeRemote(t)
	_, err := runRemote(t, g, remotePlan(Leg{Name: "a", To: Point{10, 0, 0}}), RemoteOptions{}, nil)
	if err == nil || !strings.Contains(err.Error(), "lead_bot") || g.unbinds != 0 || len(g.orders) != 0 {
		t.Fatalf("%v %v", err, g.orders)
	}
}

func pullPlan() *Plan {
	return remotePlan(Leg{Name: "a", To: Point{0, 0, 0}, PullGroups: []PullGroup{{Entries: []uint32{299}, Positions: []Point{{30, 0, 0}}}}})
}

// A pulled mob that is missing from the first frame after the pull (the bots lost sight of it for a moment, or it rotated out of
// the 24-hostile cap) is not dead: the fight goes on and the mob is killed.
func TestRemoteFightSurvivesAMobMissingFromAFrame(t *testing.T) {
	g := newFakeRemote(t)
	g.addMob(299, Point{30, 0, 0})
	pulled := false
	g.onTick = func(g *fakeRemote) {
		if !pulled && len(g.ordersOf("cmd pullback")) == 1 {
			pulled, g.hide = true, 1
		}
	}
	rep, err := runRemote(t, g, pullPlan(), opts(), nil)
	if err != nil || len(g.mobs) != 0 || !pulled {
		t.Fatalf("%v mobs %d notes %v", err, len(g.mobs), rep.Notes)
	}
	if rep.Remote.Orders["pullback"] != 1 || rep.Legs[0].Pulls != 1 || rep.Legs[0].PullRetries != 0 {
		t.Fatalf("%+v %+v", rep.Remote, rep.Legs[0])
	}
}

// A mob absent from two frames in a row is gone; one absent only from truncated frames never is, however many come.
func TestRemoteFightIgnoresTruncatedFrames(t *testing.T) {
	g := newFakeRemote(t)
	g.addMob(299, Point{30, 0, 0})
	g.killRate = 5 // a long fight
	var cutFrom time.Time
	g.onTick = func(g *fakeRemote) {
		m := g.mobs
		switch {
		case len(m) == 1 && m[0].engaged && cutFrom.IsZero():
			cutFrom, g.truncating = g.now, true
		case !cutFrom.IsZero() && g.now.Sub(cutFrom) > 8*time.Second:
			g.truncating = false
		}
	}
	rep, err := runRemote(t, g, pullPlan(), opts(), nil)
	if err != nil || cutFrom.IsZero() {
		t.Fatalf("%v %v", err, rep.Notes)
	}
	// The run went on after the cut only because the mob came back: it was killed, not taken for dead.
	if len(g.mobs) != 0 || g.mobsKilled == 0 {
		t.Fatalf("mobs %d killed %d: the fight ended early", len(g.mobs), g.mobsKilled)
	}
}

// Absent from two plain frames after fighting: dead.
func TestRemoteFightCountsAMobGoneForTwoFramesAsKilled(t *testing.T) {
	g := newFakeRemote(t)
	g.addMob(299, Point{30, 0, 0})
	g.killRate = 0
	g.onTick = func(g *fakeRemote) {
		for _, m := range g.mobs {
			if m.engaged && g.now.After(m.pullAt.Add(6*time.Second)) {
				g.mobs = nil // gone from the world, never at 0 %
				return
			}
		}
	}
	rep, err := runRemote(t, g, pullPlan(), opts(), nil)
	if err != nil || rep.Legs[0].Pulls != 1 || rep.Legs[0].PullRetries != 0 {
		t.Fatalf("%v %+v", err, rep.Legs)
	}
}

// A mob that leaves the feed after the pull and was never seen fighting is no kill: the pull drew no fight.
func TestRemoteFightMobNeverSeenFightingIsNoEngage(t *testing.T) {
	g := newFakeRemote(t)
	g.addMob(299, Point{30, 0, 0})
	g.vanish = true
	rep, err := runRemote(t, g, pullPlan(), opts(), nil)
	if err != nil {
		t.Fatalf("%v", err)
	}
	found := false
	for _, n := range rep.Notes {
		found = found || strings.Contains(n, "drew no fight")
	}
	if !found {
		t.Fatalf("a mob that never fought was counted as won: notes %v", rep.Notes)
	}
}

func TestRemoteRunRecoversAPanic(t *testing.T) {
	g := newFakeRemote(t)
	g.panicOn = "cmd move"
	rep, err := runRemote(t, g, remotePlan(Leg{Name: "a", To: Point{50, 0, 0}}), opts(), nil)
	if err == nil || !strings.Contains(err.Error(), "internal error: boom") || rep.State != "failed" {
		t.Fatalf("%v %s", err, rep.State)
	}
	if g.unbinds != 1 || !rep.Remote.Unbound {
		t.Fatalf("unbinds %d %+v", g.unbinds, rep.Remote)
	}
}
