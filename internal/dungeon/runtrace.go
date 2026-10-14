package dungeon

import (
	"context"
	"fmt"
	"math"
	"slices"
	"strings"
	"time"

	"github.com/pjerra/turtle-agent/internal/events"
	"github.com/pjerra/turtle-agent/internal/objects"
	"github.com/pjerra/turtle-agent/internal/packet"
	"github.com/pjerra/turtle-agent/internal/tbm"
	"github.com/pjerra/turtle-agent/internal/trace"
	"github.com/pjerra/turtle-agent/internal/tracerun"
)

// Run traces (README "Run traces and checks"). Every run has a run id (Config.Trace.RunID, else generated) that goes into the
// report (run_id), into every dungeon event (data.run_id) and into the trace file's header. The trace itself is a JSONL file per
// run (internal/trace) that both drivers feed through these hooks; they are calls at the points where the drivers already report
// (events, orders, deaths, rest gates, walks, pulls), and do nothing when the recorder has no file.

// traceEvents maps the dungeon events that the driver sends to the agent's log onto trace event names. dungeon_done and
// dungeon_failed are not copied: the end record is their equivalent.
var traceEvents = map[string]string{
	events.KindDungeonLeg:    "leg",
	events.KindDungeonStep:   "step",
	events.KindDungeonLeader: "leader",
}

// stampEvent adds the run id to an event's data and copies the events that the trace keeps into it.
func stampEvent(tr *tracerun.Recorder, kind, text string, data map[string]any) map[string]any {
	if data == nil {
		data = map[string]any{}
	}
	data["run_id"] = tr.ID()
	if ev, ok := traceEvents[kind]; ok && tr.On() {
		cp := make(map[string]any, len(data)+1)
		for k, v := range data {
			if k != "run_id" {
				cp[k] = v
			}
		}
		if text != "" {
			cp["text"] = text
		}
		tr.Event(ev, cp)
	}
	return data
}

// outcomeName is a pull's outcome as the trace words it.
func outcomeName(o outcome) string {
	switch o {
	case won:
		return "won"
	case noEngage:
		return "no_engage"
	case evaded:
		return "evaded"
	case refused:
		return "refused"
	case retarget:
		return "retarget"
	}
	return "unknown"
}

var classNames = map[uint8]string{1: "warrior", 2: "paladin", 3: "hunter", 4: "rogue", 5: "priest", 7: "shaman", 8: "mage", 9: "warlock", 11: "druid"}

func guidText(g packet.GUID) string {
	if g == 0 {
		return ""
	}
	return fmt.Sprintf("0x%x", uint64(g))
}

// traceCounters are the run's totals for the end record and the summary.
func traceCounters(r Report) map[string]float64 {
	c := map[string]float64{"seconds": r.Seconds, "legs": float64(len(r.Legs)), "deaths": float64(len(r.Deaths)), "wipes": float64(r.Wipes),
		"gm_shortcuts": float64(len(r.Shortcuts)), "setup_shortcuts": float64(len(r.SetupShortcuts)), "leader_deaths": float64(r.LeaderDeaths),
		"resurrections": float64(r.Resurrections), "corpse_runs": float64(r.CorpseRuns), "seconds_dead": r.SecondsDead}
	var pulls, retries, aggros int
	var rest float64
	for _, l := range r.Legs {
		pulls += l.Pulls
		retries += l.PullRetries
		aggros += l.WalkAggros
		rest += l.RestSeconds
	}
	c["pulls"], c["pull_retries"], c["walk_aggros"], c["rest_seconds"] = float64(pulls), float64(retries), float64(aggros), rest
	refusals := 0
	for _, x := range r.Refusals {
		refusals += x.Count
	}
	c["refusals"] = float64(refusals)
	c["rest_gates_skipped"], c["rest_walk_passes"], c["rest_share"] = float64(r.RestGatesSkipped), float64(r.RestWalkPasses), r.RestShare
	if rr := r.Remote; rr != nil {
		c["orders_sent"], c["orders_refused"], c["orders_unanswered"] = float64(rr.OrdersSent), float64(rr.OrdersRefused), float64(rr.OrdersUnanswered)
		c["frames_received"], c["frames_missed"], c["frames_stale"] = float64(rr.FramesReceived), float64(rr.FramesMissed), float64(rr.FramesStale)
		c["feed_stalls"], c["move_stalls"], c["walk_aggros"] = float64(rr.FeedStalls), float64(rr.MoveStalls), float64(rr.WalkAggros)
		c["threat_warnings"], c["threat_overrides"] = float64(rr.ThreatWarnings), float64(rr.ThreatOverrides)
		c["refills"] = float64(len(rr.Refills))
	}
	return c
}

// ---- group mode ----

func (d *Driver) initTrace() {
	var err error
	d.tr, err = tracerun.New(d.cfg.Trace, "dungeon_run", "group", d.clk.Now)
	if err != nil {
		d.rep.note("trace: " + err.Error())
	}
	d.rep.r.RunID = d.tr.ID()
	d.rep.r.TraceFile = d.tr.Path()
	d.rep.onNote = func(s string) { d.tr.Event("note", map[string]any{"text": s}) }
}

// emit sends a dungeon event to the agent's log with the run id in it, and copies it into the trace.
func (d *Driver) emit(kind, text string, data map[string]any) {
	d.g.Emit(kind, text, stampEvent(d.tr, kind, text, data))
}

// chat says text in party chat; the ".bot" orders are traced.
func (d *Driver) chat(ctx context.Context, text string) error {
	err := d.g.Chat(ctx, "party", text)
	if strings.HasPrefix(text, ".bot ") {
		d.wd.ordered(text)
	}
	intent, ok := strings.CutPrefix(text, ".bot action ")
	if !ok && strings.HasPrefix(text, ".bot ") {
		intent, ok = "command", true // a ".bot command <name> u drink" order to one bot
	}
	if ok {
		ev := map[string]any{"via": "chat", "intent": intent, "text": text}
		if err != nil {
			ev["error"] = err.Error()
		}
		d.tr.Event("order", ev)
	}
	return err
}

func (d *Driver) traceBegin(fromLeg int) {
	if d.tr == nil || !d.cfg.Trace.On {
		return
	}
	units := map[packet.GUID]objects.Unit{}
	for _, u := range d.g.Units() {
		units[u.GUID] = u
	}
	member := func(name string, g packet.GUID, lead bool) trace.Member {
		m := trace.Member{G: guidText(g), Name: name, Lead: lead}
		if u, ok := units[g]; ok {
			m.Class, m.Level = classNames[u.Class], int(u.Level)
		}
		return m
	}
	self := d.g.Self()
	party := []trace.Member{member(self.Name, self.GUID, true)}
	for _, m := range d.g.Party() {
		party = append(party, member(m.Name, m.GUID, false))
	}
	opts := map[string]any{"teleport": d.cfg.Teleport, "shortcuts": d.cfg.Shortcuts, "recover_wipe": d.cfg.RecoverWipe, "wait_sickness": d.cfg.WaitSickness}
	if len(d.cfg.Setup) > 0 {
		opts["setup"] = slices.Clone(d.cfg.Setup)
	}
	if len(d.cfg.Buffs) > 0 {
		opts["buffs"] = buffNames(d.cfg.Buffs)
	}
	// The group run reads positions, facing and movement from the object store of the members in view, and hp and power from the
	// party list.
	caps := []string{trace.CapFacing, trace.CapMoving, trace.CapOrders, trace.CapWalks, trace.CapPartyList}
	d.tr.Begin(trace.Header{Map: d.plan.Map, Plan: d.plan.Name, FromLeg: fromLeg, Lead: self.Name, Brain: d.cfg.BrainName, Party: party, Options: opts, Caps: caps})
	if !d.tr.On() { // the file could not be made: the report's trace.error says why
		d.rep.update(func(r *Report) { r.TraceFile = "" })
	} else {
		d.stopSampler = d.tr.Sampler(context.Background(), d.traceSample)
		d.traceSample()
	}
}

// traceSample takes a frame of the party and the hostiles from the session's object store and party list, when one is due.
func (d *Driver) traceSample() {
	if !d.tr.Due() {
		return
	}
	byG := map[packet.GUID]objects.Unit{}
	units := d.g.Units()
	for _, u := range units {
		byG[u.GUID] = u
	}
	self := d.g.Self()
	pos := d.g.Position()
	mobPos := map[string]trace.Mob{}
	var mobs []trace.Mob
	for _, u := range units {
		if u.Type == "player" || !u.Hostile || u.Dead || len(mobs) >= 40 {
			continue
		}
		if !u.InCombat && (u.Distance < 0 || u.Distance > 60) {
			continue
		}
		m := trace.Mob{G: guidText(u.GUID), HPPct: pct(u.Health, u.MaxHealth), X: u.X, Y: u.Y, Z: u.Z, Combat: u.InCombat, Target: guidText(u.Target),
			Evade: false, Entry: u.Entry, Name: u.Name, Level: int(u.Level)}
		mobs = append(mobs, m)
		mobPos[m.G] = m
	}
	// A member's row in the object store gives position, combat, target, facing and movement; one that is not in view has none.
	bot := func(g packet.GUID, hp, mhp, pw, mpw uint32, ptype uint8, dead bool, lead bool) trace.Bot {
		b := trace.Bot{G: guidText(g), Alive: !dead, HP: hp, MaxHP: mhp, Power: pw, MaxPow: mpw, PType: ptype, Lead: lead, State: "n"}
		u, ok := byG[g]
		switch {
		case !ok:
			b.NoPos = true
		default:
			b.X, b.Y, b.Z, b.Combat, b.Target, b.Moving = u.X, u.Y, u.Z, u.InCombat, guidText(u.Target), u.Moving
			if mpw == 0 && u.MaxPower > 0 {
				b.Power, b.MaxPow, b.PType = u.Power, u.MaxPower, u.PowerType
			}
			if t, ok := mobPos[b.Target]; ok {
				b.Facing = facingDegrees(u.O, u.X, u.Y, t.X, t.Y)
			}
		}
		switch {
		case dead:
			b.State = "d"
		case b.Combat:
			b.State = "c"
		}
		return b
	}
	lead := bot(self.GUID, self.Health, self.MaxHealth, 0, 0, 0, self.Dead(), true)
	lead.X, lead.Y, lead.Z, lead.NoPos = pos.X, pos.Y, pos.Z, false // the session's own position is the newest
	bots := []trace.Bot{lead}
	for _, m := range d.g.Party() {
		bots = append(bots, bot(m.GUID, m.Health, m.MaxHealth, m.Power, m.MaxPower, m.PowerType, m.Dead, false))
		d.tr.Party(trace.Member{G: guidText(m.GUID), Name: m.Name})
	}
	d.tr.Party(trace.Member{G: guidText(self.GUID), Name: self.Name, Lead: true})
	d.tr.Frame(trace.Frame{Via: "session", Bots: bots, Mobs: mobs})
}

// facingDegrees is the angle, 0 to 180, between where a unit faces (orientation o, radians, 0 towards +x) and the direction to a point.
func facingDegrees(o, x, y, tx, ty float32) *int {
	diff := math.Abs(math.Remainder(float64(o)-math.Atan2(float64(ty-y), float64(tx-x)), 2*math.Pi)) * 180 / math.Pi
	d := int(math.Round(diff))
	return &d
}

func pct(v, max uint32) int {
	if max == 0 {
		return 100
	}
	return int(uint64(v) * 100 / uint64(max))
}

// traceEnd writes the end of the trace, scores it and puts the outcome in the report. It runs before the report publishes its end.
func (d *Driver) traceEnd(state, errText string, at time.Time) {
	if d.stopSampler != nil {
		d.stopSampler()
		d.stopSampler = nil
	}
	rep := d.rep.snapshot()
	endReport(&rep, state, errText, at)
	if info := d.tr.Finish(state, errText, traceCounters(rep)); info != nil {
		d.rep.update(func(r *Report) { r.Trace = info })
	}
}

// tracedWalk is doWalk for a walk of the plan, with its start and end in the trace.
func (d *Driver) tracedWalk(ctx context.Context, p Point) error {
	began := d.clk.Now()
	d.tr.Event("walk_start", map[string]any{"leg": d.curLeg, "x": p.X, "y": p.Y, "z": p.Z})
	err := d.doWalk(ctx, p)
	end := map[string]any{"leg": d.curLeg, "ok": err == nil, "seconds": d.clk.Now().Sub(began).Seconds()}
	if err != nil {
		end["error"] = err.Error()
	}
	d.tr.Event("walk_end", end)
	return err
}

// tracedFight is fight with the pull's start and end in the trace.
func (d *Driver) tracedFight(ctx context.Context, t objects.Unit, sp fightSpec) (fightResult, error) {
	began := d.clk.Now()
	d.tr.Event("pull_start", map[string]any{"leg": d.curLeg, "target": t.Name, "entry": t.Entry, "guid": guidText(t.GUID), "x": t.X, "y": t.Y, "z": t.Z})
	d.wd.softProgress("pull started: " + t.Name)
	d.wd.fightBegin()
	d.wdRepick.clear() // a request left by an earlier fight is not this fight's
	leave := d.wd.enter(wdFightPhase, t.Name, func(ctx context.Context) (string, error) {
		d.wdRepick.set()
		err := d.chat(ctx, ".bot action follow")
		return "told the party to follow and ended the fight to pick the target again", err
	})
	res, err := d.fight(ctx, t, sp)
	leave()
	d.wd.softProgress("pull ended: " + outcomeName(res.out))
	end := map[string]any{"leg": d.curLeg, "outcome": outcomeName(res.out), "kills": res.kills, "seconds": d.clk.Now().Sub(began).Seconds()}
	if err != nil {
		end["outcome"], end["error"] = "error", err.Error()
	}
	d.tr.Event("pull_end", end)
	return res, err
}

// traceRest is deferred at the top of a rest gate: it records the time the gate took.
func (d *Driver) traceRest(began time.Time) {
	d.wd.softProgress("rest gate finished")
	d.tr.Event("rest", map[string]any{"leg": d.curLeg, "seconds": d.clk.Now().Sub(began).Seconds()})
}

// ---- remote mode ----

func (d *RemoteDriver) initTrace() {
	var err error
	d.tr, err = tracerun.New(d.cfg.Trace, "dungeon_run", "remote", d.clk.Now)
	if err != nil {
		d.rep.note("trace: " + err.Error())
	}
	d.rep.r.RunID = d.tr.ID()
	d.rep.r.TraceFile = d.tr.Path()
	d.rep.onNote = func(s string) { d.tr.Event("note", map[string]any{"text": s}) }
}

func (d *RemoteDriver) emit(kind, text string, data map[string]any) {
	d.g.Emit(kind, text, stampEvent(d.tr, kind, text, data))
}

func (d *RemoteDriver) traceBegin(fromLeg int) {
	if d.tr == nil || !d.cfg.Trace.On {
		return
	}
	// The roster of a remote run comes from the feed (the first frame); the header lists the bots by name for now.
	opts := map[string]any{"recover_wipe": d.cfg.RecoverWipe, "lead_bot": d.opt.Lead, "bots": slices.Clone(d.opt.Bots), "cc_rules": len(d.opt.CC)}
	if len(d.cfg.Setup) > 0 {
		opts["setup"] = slices.Clone(d.cfg.Setup)
	}
	if g := d.startGroup(); g >= 0 {
		opts["from_pull_group"] = g
	}
	if pt := d.opt.Pulltest; pt != nil {
		opts["pulltest"] = map[string]any{"leg": pt.Leg, "pull_group": pt.PullGroup, "tries": pt.Tries, "reset": pt.Reset}
	}
	// The feed has facing and the bot's target but no movement flag or class, and it has threat lists.
	caps := []string{trace.CapFacing, trace.CapThreat, trace.CapOrders, trace.CapWalks, trace.CapPartyList}
	d.tr.Begin(trace.Header{Map: d.plan.Map, Plan: d.plan.Name, FromLeg: fromLeg, Lead: d.opt.Lead, Tank: d.opt.Tank, Options: opts, Caps: caps})
	if !d.tr.On() {
		d.rep.update(func(r *Report) { r.TraceFile = "" })
	}
}

func (d *RemoteDriver) traceEnd(state, errText string, at time.Time) {
	rep := d.rep.snapshot()
	endReport(&rep, state, errText, at)
	if info := d.tr.Finish(state, errText, traceCounters(rep)); info != nil {
		d.rep.update(func(r *Report) { r.Trace = info })
	}
}

// traceLook is deferred by look: a new feed frame goes into the trace (the recorder keeps about one a second), and the feed going
// quiet is an event.
func (d *RemoteDriver) traceLook() {
	if d.haveFr && d.n != d.trSeen {
		d.trSeen = d.n
		if !d.trRoster {
			d.trRoster = true
			names := make([]string, 0, len(d.fr.Party))
			for _, p := range d.fr.Party {
				names = append(names, p.Name)
			}
			d.tr.Event("roster", map[string]any{"party": names})
			for _, p := range d.fr.Party {
				d.tr.Party(trace.Member{G: guidText(p.GUID), Name: p.Name, Lead: p.GUID == d.fr.Lead && d.fr.Lead != 0, Role: d.roleOf(p.Name)})
			}
		}
		if d.tr.Due() {
			d.tr.Frame(feedFrame(d.fr))
		}
	}
	if d.stalled && !d.trStalled {
		d.tr.Event("feed_stall", map[string]any{"frames": d.n})
	}
	d.trStalled = d.stalled
}

// roleOf is "tank" for the bot the run was told to hold threat, else empty (the feed does not say).
func (d *RemoteDriver) roleOf(name string) string {
	if d.opt.Tank != "" && strings.EqualFold(d.opt.Tank, name) {
		return "tank"
	}
	return ""
}

// feedFrame turns a TBM:VIEW frame into a trace frame.
func feedFrame(fr tbm.ViewFrame) trace.Frame {
	out := trace.Frame{Via: "feed", Seq: fr.Seq, Cut: fr.Truncated}
	hostile := map[packet.GUID]tbm.HostileRec{}
	for _, h := range fr.Hostiles {
		hostile[h.GUID] = h
	}
	for _, p := range fr.Party {
		b := trace.Bot{G: guidText(p.GUID), Alive: !p.Dead, Combat: p.InCombat(), State: p.State, HP: p.HP, MaxHP: p.MaxHP, Power: p.Power, MaxPow: p.MaxPower,
			PType: p.PowerType, X: p.X, Y: p.Y, Z: p.Z, Target: guidText(p.Target), Cast: p.Spell, Action: p.Action, Lead: p.GUID == fr.Lead && fr.Lead != 0}
		if h, ok := hostile[p.Target]; ok {
			b.Facing = facingDegrees(p.O, p.X, p.Y, h.X, h.Y)
		}
		out.Bots = append(out.Bots, b)
	}
	for _, h := range fr.Hostiles {
		m := trace.Mob{G: guidText(h.GUID), HPPct: h.HPPct, X: h.X, Y: h.Y, Z: h.Z, Combat: false, Target: guidText(h.Target), Cast: h.Spell,
			Evade: h.Evading(), CC: h.CrowdControlled(), Entry: h.Entry}
		m.Combat = h.Target != 0
		if t, ok := fr.Threat(h.GUID); ok {
			for _, e := range t.Top {
				m.Threat = append(m.Threat, trace.Threat{G: guidText(e.GUID), Pct: e.Pct})
			}
		}
		out.Mobs = append(out.Mobs, m)
	}
	return out
}

// traceOrder records an order of the remote run with the module's answer.
func (d *RemoteDriver) traceOrder(verb, cmd string, r tbm.Reply, ms int64) {
	ev := map[string]any{"via": "cmd", "verb": verb, "text": cmd, "reply": r.Kind, "ms": ms}
	if r.Err != nil {
		ev["code"], ev["message"] = r.Err.Code, r.Err.Message
	}
	d.tr.Event("order", ev)
}

func (d *RemoteDriver) tracedWalk(ctx context.Context, p Point) error {
	return d.tracedWalkAs(ctx, p, false)
}

// tracedWalkAs is tracedWalk, as a direct step (straight line, `cmd move ... direct`) when direct is set.
func (d *RemoteDriver) tracedWalkAs(ctx context.Context, p Point, direct bool) error {
	began := d.clk.Now()
	start := map[string]any{"leg": d.curLeg, "x": p.X, "y": p.Y, "z": p.Z}
	if direct {
		start["direct"] = true
	}
	d.tr.Event("walk_start", start)
	err := d.walk(ctx, p, direct)
	end := map[string]any{"leg": d.curLeg, "ok": err == nil, "seconds": d.clk.Now().Sub(began).Seconds()}
	if err != nil {
		end["error"] = err.Error()
	}
	d.tr.Event("walk_end", end)
	return err
}

func (d *RemoteDriver) tracedFight(ctx context.Context, t tbm.HostileRec, sp fightSpec) (fightResult, error) {
	began := d.clk.Now()
	d.tr.Event("pull_start", map[string]any{"leg": d.curLeg, "target": d.hostileName(t), "entry": t.Entry, "guid": guidText(t.GUID), "x": t.X, "y": t.Y, "z": t.Z})
	d.wd.softProgress("pull started: " + d.hostileName(t))
	d.wd.fightBegin()
	d.wdRepick.clear() // a request left by an earlier fight is not this fight's
	leave := d.wd.enter(wdFightPhase, d.hostileName(t), func(ctx context.Context) (string, error) {
		d.wdRepick.set()
		_, err := d.order(ctx, "follow")
		return "sent cmd follow and ended the fight to pick the target again", err
	})
	res, err := d.fight(ctx, t, sp)
	leave()
	d.wd.softProgress("pull ended: " + outcomeName(res.out))
	end := map[string]any{"leg": d.curLeg, "outcome": outcomeName(res.out), "kills": res.kills, "seconds": d.clk.Now().Sub(began).Seconds()}
	if err != nil {
		end["outcome"], end["error"] = "error", err.Error()
	}
	d.tr.Event("pull_end", end)
	return res, err
}

func (d *RemoteDriver) traceRest(began time.Time) {
	d.wd.softProgress("rest gate finished")
	d.tr.Event("rest", map[string]any{"leg": d.curLeg, "seconds": d.clk.Now().Sub(began).Seconds()})
}

func facingOf(o float32) *float32 { return &o }
