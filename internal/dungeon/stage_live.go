package dungeon

import (
	"context"
	"fmt"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/pjerra/turtle-agent/internal/tbm"
)

// The live sequence of a staged leg for the TortoiseBots run leader: the
// Stager puts the party at the offline-checked point, checks the spot is
// quiet, summons the whole party in one burst, watches it arrive and settle,
// and optionally starts or resumes the run leader.

// StageUnit is one unit in view for the quiet and arrival checks.
type StageUnit struct {
	Name     string
	Entry    uint32
	Player   bool
	At       Point
	Map      uint32 // the agent's map (units are only those in view)
	Hostile  bool
	Dead     bool
	Ghost    bool
	InCombat bool
	Moving   bool
}

// StageGame is what the live sequence needs of the commander game.
type StageGame interface {
	CommandWait(ctx context.Context, command string, wait time.Duration) (tbm.Reply, error)
	AccountLevel(ctx context.Context) (int, bool, error)
	AgentPlace() (Point, uint32, bool)
	Appear(ctx context.Context, player string) error
	GoXYZ(ctx context.Context, mapID uint32, at Point) error
	GPS(ctx context.Context) (string, error)
	SummonBot(ctx context.Context, name string) error
	StageUnits() []StageUnit
	RunStatus() (tbm.RunStatus, uint64, bool)
}

// StageRequest is one live staging.
type StageRequest struct {
	Plan      *Plan
	Leg       int
	Opt       StageOptions
	Nav       Navmesh
	FirstRow  func(serverSteps int) (Point, bool, string)
	DryRun    bool
	Start     bool
	Resume    bool
	Speed     float64
	QuietWait time.Duration
}

// StagePartyMember is one staged bot and where it stands.
type StagePartyMember struct {
	Name    string  `json:"name"`
	X       float64 `json:"x"`
	Y       float64 `json:"y"`
	Z       float64 `json:"z"`
	Dist    float64 `json:"dist"`
	Arrived bool    `json:"arrived"`
}

// StageRun is the start or resume the staging ended with.
type StageRun struct {
	Sent      string `json:"sent"`
	StartStep int    `json:"start_step,omitempty"`
	Steps     int    `json:"steps,omitempty"`
	Leg       string `json:"leg,omitempty"`
	Line      string `json:"line,omitempty"`
}

// StageReport is the RPC result of dungeon_stage (StageResult embedded with
// the live fields). Staged is the RPC's staged: the party stands at the
// point (step 11); OK is the offline check's answer and stays true when a
// live step refuses.
type StageReport struct {
	StageResult
	Staged      bool               `json:"staged"`
	Started     bool               `json:"started"`
	DryRun      bool               `json:"dry_run"`
	FirstSource string             `json:"first_source"`
	FloorZ      *float64           `json:"floor_z,omitempty"`
	GMCommands  []string           `json:"gm_commands"`
	Party       []StagePartyMember `json:"party"`
	Run         *StageRun          `json:"run,omitempty"`
	Seconds     float64            `json:"seconds"`
	Notes       []string           `json:"notes"`
}

// Stager runs the live sequence over a StageGame.
type Stager struct {
	G     StageGame
	Now   func() time.Time
	Sleep func(ctx context.Context, d time.Duration) error
	Poll  time.Duration
}

// stageBeginRe matches the run leader's start line: "The run starts at step X of Y (leg NAME)".
var stageBeginRe = regexp.MustCompile(`^The run starts at step (\d+) of (\d+) \(leg ([^)]+)\)`)

// Stage runs the sequence of spec 3.6. Errors are session errors
// (returned); game refusals come back as staged:false with the text.
func (s *Stager) Stage(ctx context.Context, req StageRequest) (StageReport, error) {
	t0 := s.Now()
	rep := StageReport{DryRun: req.DryRun, FirstSource: "plan"}
	rep.Leg, rep.LegName, rep.Map = req.Leg, req.Plan.Legs[req.Leg].Name, req.Plan.Map
	note := func(text string) { rep.Notes = append(rep.Notes, "stage: "+text) }
	gmLine := func(line string) { rep.GMCommands = append(rep.GMCommands, line) }
	done := func() (StageReport, error) {
		rep.Seconds = s.Now().Sub(t0).Seconds()
		return rep, nil
	}

	// 1. Run state, fail closed: refuse (no teleport, no summon) unless
	// the reply is a successful reply with exactly one valid current
	// TBM:RUN line. A timeout, an ACTION_ERR, a missing line and a
	// malformed line all refuse.
	status, err := s.G.CommandWait(ctx, "run status", 4*time.Second)
	if err != nil {
		return rep, err
	}
	var st *tbm.RunStatus
	runs := 0
	malformed := false
	for _, l := range status.Lines {
		if rs, ok := tbm.ParseRunLine(l); ok {
			runs++
			c := rs
			st = &c
			continue
		}
		if strings.HasPrefix(strings.TrimSpace(l), tbm.RunPrefix) {
			malformed = true
		}
	}
	badRun := ""
	switch {
	case status.Kind == tbm.KindTimeout:
		badRun = "a timeout (no answer)"
	case status.Err != nil:
		badRun = fmt.Sprintf("a refusal: %s (%s)", status.Err.Code, status.Err.Message)
	case status.Kind != tbm.KindAck:
		badRun = fmt.Sprintf("no successful reply (kind %s)", status.Kind)
	case runs == 0 && malformed:
		badRun = "a malformed TBM:RUN line"
	case runs == 0:
		badRun = "no TBM:RUN line"
	case runs > 1:
		badRun = fmt.Sprintf("%d TBM:RUN lines", runs)
	}
	if badRun != "" {
		rep.Refusal = fmt.Sprintf("cannot stage without a current run state: run status gave %s; nothing was sent", badRun)
		return done()
	}
	pausedRefusal := fmt.Sprintf("the run leader has a paused run (step %d/%d, leg %s): pass resume to stage and resume it, or .bot run stop it first", st.Step, st.Steps, st.Leg)
	switch {
	case st.State == tbm.RunRunning:
		rep.Refusal = fmt.Sprintf("the run leader has a run going (step %d/%d, leg %s): .bot run stop or pause it first", st.Step, st.Steps, st.Leg)
		return done()
	case st.State == tbm.RunPaused && !req.Resume:
		rep.Refusal = pausedRefusal
		return done()
	case req.Resume && st.State != tbm.RunPaused:
		rep.Refusal = "resume: the run leader has no paused run"
		return done()
	case req.Resume && (!strings.EqualFold(st.Plan, req.Plan.Name) || !stageLegMatches(req.Plan, req.Leg, st.Leg)):
		rep.Refusal = fmt.Sprintf("resume: the paused run is plan %s leg %s (step %d/%d), not %s leg %s: .bot run stop it first, then stage that leg",
			st.Plan, stageLegWord(st.Leg), st.Step, st.Steps, req.Plan.Name, req.Plan.Legs[req.Leg].Name)
		return done()
	case req.Start && st.State == tbm.RunPaused:
		rep.Refusal = pausedRefusal
		return done()
	}

	// 2. Server plan: only an ACK with no error is used. A timeout, an
	// ACTION_ERR, or any other kind is refused before parsing, since a
	// reply can carry partial lines (as observe.go:302 does).
	list, err := s.G.CommandWait(ctx, "run list", 4*time.Second)
	if err != nil {
		return rep, err
	}
	if list.Kind == tbm.KindTimeout {
		rep.Refusal = "cannot stage: the server did not answer .bot run list, so it has no run leader; nothing was sent"
		return done()
	}
	if list.Err != nil {
		rep.Refusal = fmt.Sprintf("cannot stage: the server refused .bot run list: %s (%s); nothing was sent", list.Err.Code, list.Err.Message)
		return done()
	}
	if list.Kind != tbm.KindAck {
		rep.Refusal = fmt.Sprintf("cannot stage: .bot run list gave no successful reply (kind %s); nothing was sent", list.Kind)
		return done()
	}
	planList := ""
	for _, l := range list.Lines {
		if rest, ok := strings.CutPrefix(l, "Dungeon plans: "); ok {
			planList = rest
		}
	}
	if planList == "" {
		rep.Refusal = "cannot stage: .bot run list gave no \"Dungeon plans:\" line; nothing was sent"
		return done()
	}
	steps := listedSteps(planList, req.Plan.Name)
	if steps == 0 {
		rep.Refusal = fmt.Sprintf("the server's run leader has no plan %s (it lists: %s): export it with turtle-agent plan-export, apply the migration and restart the server", req.Plan.Name, planList)
		return done()
	}
	if req.FirstRow != nil {
		if fr, ok, fnote := req.FirstRow(steps); ok {
			req.Opt.FirstRow = &fr
			rep.FirstSource = "rows"
		} else if fnote != "" {
			note(fnote)
		}
	}
	// 3. Offline.
	res, err := StageLeg(req.Plan, req.Leg, req.Nav, req.Opt)
	if err != nil {
		return rep, err
	}
	rep.StageResult = res
	if !res.OK {
		return done()
	}
	if req.DryRun {
		return done()
	}

	// 4. Party.
	party, err := readBoundParty(ctx, func(ctx context.Context, cmd string) (tbm.Reply, error) {
		return s.G.CommandWait(ctx, cmd, 4*time.Second)
	})
	if err != nil {
		return rep, err
	}
	if !party.Answered || len(party.Names) == 0 {
		rep.Refusal = "no bound party: bind it first (.bot commander bind <lead> <bots>)"
		return done()
	}
	members := []string{party.Lead}
	for _, n := range party.Names {
		dup := strings.EqualFold(n, party.Lead)
		for _, m := range members {
			dup = dup || strings.EqualFold(n, m)
		}
		if !dup {
			members = append(members, n)
		}
	}
	if len(party.Dead) > 0 {
		verb := "is"
		if len(party.Dead) > 1 {
			verb = "are"
		}
		rep.Refusal = fmt.Sprintf("%s %s dead: revive first (.bot cmd revive party), then stage again", strings.Join(party.Dead, ", "), verb)
		return done()
	}

	// 5. GM capability.
	level, known, err := s.G.AccountLevel(ctx)
	if err != nil {
		return rep, err
	}
	if !known || !capsFor(level).Teleport {
		rep.Refusal = fmt.Sprintf("staging needs a GM commander (account level %d): nothing was sent", level)
		return done()
	}

	point := res.Point
	lead := members[0]

	// 6. Into the instance.
	here, mmap, _ := s.G.AgentPlace()
	units := s.G.StageUnits()
	if mmap != req.Plan.Map || unitNamed(units, lead) == nil || hdist(here, leadPosOf(units, lead)) > keepNearFar {
		if err := s.G.Appear(ctx, lead); err != nil {
			return rep, err
		}
		gmLine(".appear " + lead)
		note(".appear " + lead + " (the commander enters the lead bot's instance)")
		if err := s.waitFor(ctx, 15*time.Second, func() bool {
			_, m, _ := s.G.AgentPlace()
			if m != req.Plan.Map {
				return false
			}
			return unitNamed(s.G.StageUnits(), lead) != nil
		}); err != nil {
			rep.Refusal = fmt.Sprintf("the commander is not in the lead bot's instance 15 s after .appear %s: is the party inside (.bot cmd enter <map>)?", lead)
			return done()
		}
	}

	// 7. Commander to the point.
	if err := s.G.GoXYZ(ctx, req.Plan.Map, point); err != nil {
		return rep, err
	}
	gmLine(fmt.Sprintf(".go xyz %.2f %.2f %.2f %d", point.X, point.Y, point.Z, req.Plan.Map))
	here, mmap, _ = s.G.AgentPlace()
	if mmap != req.Plan.Map || hdist(here, point) > startNear {
		rep.Refusal = fmt.Sprintf("the commander is not at the stage point after .go xyz (at %.0f %.0f on map %d)", here.X, here.Y, mmap)
		return done()
	}
	floor, fknown, ferr := floorCheck(ctx, s.G, req.Plan.Map, point, floorHooks{
		gm:     gmLine,
		note:   func(text string) { note(text) },
		warned: new(bool),
		wait:   s.Sleep,
	})
	if ferr != nil {
		rep.Refusal = ferr.Error()
		return done()
	}
	if fknown {
		f := floor
		rep.FloorZ = &f
	}
	// 8. Quiet check (skipped with Force, which adds a warning that the quiet check was skipped instead).
	if qref, err := s.quiet(ctx, req); err != nil {
		return rep, err
	} else if qref != "" {
		if req.Opt.Force {
			rep.Warnings = append(rep.Warnings, strings.TrimSuffix(qref, "; nothing was summoned")+"; the quiet check was skipped (force)")
		} else {
			rep.Refusal = qref
			return done()
		}
	}

	// 9. Summon burst: every member, lead first, back to back with NO
	// sleep between (a lone early tank dies; TARGETED-TEST-METHOD step 4).
	for _, m := range members {
		if err := s.G.SummonBot(ctx, m); err != nil {
			return rep, err
		}
		gmLine(".summon " + m)
	}
	note(fmt.Sprintf("summoned %d bots in one burst", len(members)))

	// 10. Arrival: poll up to 10 s; once more for each missing one; poll
	// 10 s more. party[] is filled either way.
	missing := s.arrivalMissing(req, members, rep.FloorZ)
	if len(missing) > 0 {
		_ = s.waitFor(ctx, 10*time.Second, func() bool {
			missing = s.arrivalMissing(req, members, rep.FloorZ)
			return len(missing) == 0
		})
		missing = s.arrivalMissing(req, members, rep.FloorZ)
	}
	if len(missing) > 0 {
		for _, m := range missing {
			if err := s.G.SummonBot(ctx, m.name); err != nil {
				return rep, err
			}
			gmLine(".summon " + m.name)
		}
		note("retried the summon once")
		_ = s.waitFor(ctx, 10*time.Second, func() bool {
			missing = s.arrivalMissing(req, members, rep.FloorZ)
			return len(missing) == 0
		})
		missing = s.arrivalMissing(req, members, rep.FloorZ)
	}
	rep.Party = s.partyReport(req, members, rep.FloorZ)
	if len(missing) > 0 {
		parts := make([]string, len(missing))
		for i, m := range missing {
			parts[i] = m.describe()
		}
		rep.Refusal = fmt.Sprintf("the party did not arrive at the stage point %.1f %.1f %.1f after two .summon: %s",
			point.X, point.Y, point.Z, strings.Join(parts, "; "))
		return done()
	}

	// 11. Settle: sleep 5 s, read again.
	if err := s.Sleep(ctx, 5*time.Second); err != nil {
		return rep, err
	}
	if sref := s.settleRefusal(req, members, rep.FloorZ); sref != "" {
		rep.Refusal = sref
		return done()
	}
	rep.Staged = true
	note(fmt.Sprintf("the party (%d) stands at %.1f %.1f %.1f (%.0f s, %d GM lines)",
		len(members), point.X, point.Y, point.Z, s.Now().Sub(t0).Seconds(), len(rep.GMCommands)))

	// 12. Start or resume.
	if req.Start {
		rr, refusal, err := s.startRun(ctx, req)
		if err != nil {
			return rep, err
		}
		rep.Run = rr
		if refusal != "" {
			rep.Refusal = refusal
		} else {
			rep.Started = true
		}
	} else if req.Resume {
		rr, refusal, err := s.resumeRun(ctx, req)
		if err != nil {
			return rep, err
		}
		rep.Run = rr
		if refusal != "" {
			rep.Refusal = refusal
		} else {
			rep.Started = true
		}
	}
	return done()
}

// waitFor polls ok every Poll until it holds or max passes. A timeout is
// context.DeadlineExceeded; a sleep or context error is returned.
func (s *Stager) waitFor(ctx context.Context, max time.Duration, ok func() bool) error {
	deadline := s.Now().Add(max)
	for {
		if ok() {
			return nil
		}
		if !s.Now().Before(deadline) {
			return context.DeadlineExceeded
		}
		if err := s.Sleep(ctx, s.Poll); err != nil {
			return err
		}
		if err := ctx.Err(); err != nil {
			return err
		}
	}
}

type arrivalMiss struct {
	name string
	kind string // "missing", "dead", "far", "floor"
	at   Point
	dist float64
	dz   float64
}

func (m arrivalMiss) describe() string {
	switch m.kind {
	case "dead":
		return m.name + " dead"
	case "far":
		return fmt.Sprintf("%s %.0f yd away at %.1f %.1f %.1f", m.name, m.dist, m.at.X, m.at.Y, m.at.Z)
	case "floor":
		return fmt.Sprintf("%s %.1f yd off the floor", m.name, m.dz)
	default:
		return m.name + " not in view"
	}
}

func unitNamed(units []StageUnit, name string) *StageUnit {
	for i, u := range units {
		if strings.EqualFold(u.Name, name) {
			c := units[i]
			return &c
		}
	}
	return nil
}

func leadPosOf(units []StageUnit, lead string) Point {
	if u := unitNamed(units, lead); u != nil {
		return u.At
	}
	return Point{}
}

// arrivalMissing lists the members that have not arrived: in view by name
// (case-insensitive, Player), !Dead && !Ghost, 2D within 10 yd of the
// commander, and |z - floor| <= 2 (floor_z when known, else the commander's
// z).
func (s *Stager) arrivalMissing(req StageRequest, members []string, floor *float64) []arrivalMiss {
	here, _, _ := s.G.AgentPlace()
	fz := float64(here.Z)
	if floor != nil {
		fz = *floor
	}
	var out []arrivalMiss
	for _, m := range members {
		u := unitNamed(s.G.StageUnits(), m)
		if u == nil || !u.Player {
			out = append(out, arrivalMiss{name: m, kind: "missing"})
			continue
		}
		if u.Dead || u.Ghost {
			out = append(out, arrivalMiss{name: m, kind: "dead"})
			continue
		}
		if hdist(u.At, here) > 10 {
			out = append(out, arrivalMiss{name: m, kind: "far", at: u.At, dist: hdist(u.At, here)})
			continue
		}
		dz := float64(u.At.Z) - fz
		if dz < 0 {
			dz = -dz
		}
		if dz > 2 {
			out = append(out, arrivalMiss{name: m, kind: "floor", dz: dz})
		}
	}
	return out
}

func (s *Stager) partyReport(req StageRequest, members []string, floor *float64) []StagePartyMember {
	here, _, _ := s.G.AgentPlace()
	missing := map[string]bool{}
	for _, m := range s.arrivalMissing(req, members, floor) {
		missing[strings.ToLower(m.name)] = true
	}
	out := make([]StagePartyMember, len(members))
	for i, m := range members {
		e := StagePartyMember{Name: m, Arrived: !missing[strings.ToLower(m)]}
		if u := unitNamed(s.G.StageUnits(), m); u != nil {
			e.X, e.Y, e.Z = float64(u.At.X), float64(u.At.Y), float64(u.At.Z)
			e.Dist = dist3(u.At, here)
		}
		out[i] = e
	}
	return out
}

// quiet reads StageUnits twice QuietWait apart (once when QuietWait is 0)
// and judges the spot.
func (s *Stager) quiet(ctx context.Context, req StageRequest) (string, error) {
	first := s.G.StageUnits()
	second := first
	if req.QuietWait > 0 {
		if err := s.Sleep(ctx, req.QuietWait); err != nil {
			return "", err
		}
		second = s.G.StageUnits()
	}
	return s.judgeQuiet(req, first, second), nil
}

// judgeQuiet refuses the spot when any hostile breaks the quiet rule:
// L1 a hostile within StageOwnYd (20) 3D of the commander; L2 a hostile
// within StageSkippedYd (30) 3D that is not a member of an own pack; L3 a
// roamer within 80 yd 2D (Moving at either read, or moved >= 3 yd 2D
// between the reads, matched by name+entry, nearest), except the staged
// leg's own boss and the units of the staged leg's own packs (and later
// legs' packs, as L2 already exempts within 30 yd), wherever they walk.
func (s *Stager) judgeQuiet(req StageRequest, first, second []StageUnit) string {
	here, _, _ := s.G.AgentPlace()
	hostile := func(u StageUnit) bool { return !u.Player && u.Hostile && !u.Dead }
	var bad []string
	seen := map[string]bool{}
	flag := func(u StageUnit, d float64, moving bool) {
		k := strings.ToLower(u.Name) + "\x00" + strconv.Itoa(int(u.Entry))
		if seen[k] {
			return
		}
		seen[k] = true
		if len(bad) >= 5 {
			return
		}
		t := fmt.Sprintf("%s (entry %d) %.0f yd", u.Name, u.Entry, d)
		if moving {
			t += " moving"
		}
		bad = append(bad, t)
	}
	// L1/L2 over both reads.
	for _, units := range [][]StageUnit{first, second} {
		for _, u := range units {
			if !hostile(u) {
				continue
			}
			if d := dist3(u.At, here); d <= StageOwnYd {
				flag(u, d, u.Moving)
			} else if d <= StageSkippedYd && !s.ownMember(req, u) {
				flag(u, d, u.Moving)
			}
		}
	}
	// L3 roamers, matched by name+entry.
	byKey := func(units []StageUnit) map[string][]StageUnit {
		m := map[string][]StageUnit{}
		for _, u := range units {
			if !hostile(u) {
				continue
			}
			m[strings.ToLower(u.Name)+"\x00"+strconv.Itoa(int(u.Entry))] = append(m[strings.ToLower(u.Name)+"\x00"+strconv.Itoa(int(u.Entry))], u)
		}
		return m
	}
	secondByKey := byKey(second)
	firstByKey := byKey(first)
	nearest := func(us []StageUnit, at Point) *StageUnit {
		var best *StageUnit
		for i, u := range us {
			if best == nil || hdist(u.At, at) < hdist(best.At, at) {
				best = &us[i]
			}
		}
		return best
	}
	checkRoam := func(u StageUnit, d2 float64, other *StageUnit) {
		if d2 > 80 {
			return
		}
		if s.ownUnit(req, u) {
			return // the staged leg's own boss and own packs pace: not a patrol to refuse
		}
		if u.Moving || (other != nil && other.Moving) {
			flag(u, d2, true)
			return
		}
		if other != nil && hdist(u.At, other.At) >= 3 {
			flag(u, d2, u.Moving || other.Moving)
		}
	}
	for _, u := range first {
		if !hostile(u) {
			continue
		}
		k := strings.ToLower(u.Name) + "\x00" + strconv.Itoa(int(u.Entry))
		checkRoam(u, hdist(u.At, here), nearest(secondByKey[k], u.At))
	}
	for _, u := range second {
		if !hostile(u) {
			continue
		}
		k := strings.ToLower(u.Name) + "\x00" + strconv.Itoa(int(u.Entry))
		checkRoam(u, hdist(u.At, here), nearest(firstByKey[k], u.At))
	}
	if len(bad) == 0 {
		return ""
	}
	return fmt.Sprintf("the stage point is not quiet: %s (rule: no living hostile within 20 yd, none within 30 yd that is not a pack of leg %d or later, no roamer within 80 yd except the staged leg's own boss and own packs); nothing was summoned",
		strings.Join(bad, ", "), req.Leg)
}

// ownUnit says whether the hostile is the staged leg's own boss (its boss
// entry, including First) or a unit of the staged leg's own packs (or later
// legs' packs): the entry alone decides, wherever the unit walks. The L3
// roamer rule exempts those; L1 (within 20 yd) and L2 still see them.
func (s *Stager) ownUnit(req StageRequest, u StageUnit) bool {
	if l := req.Plan.Legs[req.Leg]; l.Boss != nil && (u.Entry == l.Boss.Entry || slices.Contains(l.Boss.First, u.Entry)) {
		return true
	}
	for li := req.Leg; li < len(req.Plan.Legs); li++ {
		if li <= req.Opt.ClearedThrough {
			continue
		}
		for _, g := range req.Plan.Legs[li].PullGroups {
			if slices.Contains(g.Entries, u.Entry) {
				return true
			}
		}
	}
	return false
}

// ownMember says whether the hostile is a member of an own pull group: a
// group of a leg at or past the staged leg (not cleared) whose Entries
// holds its entry and one of whose Positions is within Radius (0 → 25) of
// it.
func (s *Stager) ownMember(req StageRequest, u StageUnit) bool {
	for li := req.Leg; li < len(req.Plan.Legs); li++ {
		if li <= req.Opt.ClearedThrough {
			continue
		}
		for _, g := range req.Plan.Legs[li].PullGroups {
			found := false
			for _, e := range g.Entries {
				found = found || e == u.Entry
			}
			if !found {
				continue
			}
			rad := float64(g.Radius)
			if rad == 0 {
				rad = 25
			}
			for _, at := range g.Positions {
				if dist3(at, u.At) <= rad {
					return true
				}
			}
		}
	}
	return false
}

// settleRefusal checks the party 5 s after the summon: any member InCombat,
// dead, or over 10 yd refuses; hostiles within 30 yd are listed as in the
// quiet check, "none" when there are none.
func (s *Stager) settleRefusal(req StageRequest, members []string, floor *float64) string {
	here, _, _ := s.G.AgentPlace()
	var bad []string
	for _, m := range members {
		u := unitNamed(s.G.StageUnits(), m)
		switch {
		case u == nil:
			bad = append(bad, m+" not in view")
		case u.Dead || u.Ghost:
			bad = append(bad, m+" dead")
		case u.InCombat:
			bad = append(bad, m+" in combat")
		case hdist(u.At, here) > 10:
			bad = append(bad, fmt.Sprintf("%s %.0f yd away", m, hdist(u.At, here)))
		}
	}
	if len(bad) == 0 {
		return ""
	}
	var hosts []string
	for _, u := range s.G.StageUnits() {
		if u.Player || !u.Hostile || u.Dead {
			continue
		}
		if d := dist3(u.At, here); d <= 30 {
			t := fmt.Sprintf("%s (entry %d) %.0f yd", u.Name, u.Entry, d)
			if u.Moving {
				t += " moving"
			}
			hosts = append(hosts, t)
		}
	}
	hostText := "none"
	if len(hosts) > 0 {
		hostText = strings.Join(hosts, ", ")
	}
	return fmt.Sprintf("the party was attacked at the stage point within 5 s: %s; hostiles within 30 yd: %s",
		strings.Join(bad, ", "), hostText)
}

// startRun sends `run start <plan> [from leg <name>|from <rowLeg>][ speed=x]`
// and waits for the begin line and the running line.
func (s *Stager) startRun(ctx context.Context, req StageRequest) (*StageRun, string, error) {
	cmd := "run start " + req.Plan.Name
	if words := stageStartWords(req.Plan, req.Leg); words != "" {
		cmd += " " + words
	}
	if req.Speed > 0 {
		cmd += fmt.Sprintf(" speed=%.1f", req.Speed)
	}
	_, n0, _ := s.G.RunStatus()
	r, err := s.G.CommandWait(ctx, cmd, 6*time.Second)
	if err != nil {
		return nil, "", err
	}
	rr := &StageRun{Sent: ".bot " + cmd}
	if r.Err != nil {
		return rr, fmt.Sprintf("the run leader refused the start: %s (%s)", r.Err.Code, r.Err.Message), nil
	}
	for _, l := range r.Lines {
		if m := stageBeginRe.FindStringSubmatch(l); m != nil {
			rr.StartStep, _ = strconv.Atoi(m[1])
			rr.Steps, _ = strconv.Atoi(m[2])
			rr.Leg = m[3]
			break
		}
	}
	var got *tbm.RunStatus
	if terr := s.waitFor(ctx, 20*time.Second, func() bool {
		st, n, ok := s.G.RunStatus()
		if ok && n > n0 && st.State == tbm.RunRunning {
			c := st
			got = &c
			return true
		}
		return false
	}); terr != nil || got == nil {
		if terr != nil && terr != context.DeadlineExceeded {
			return rr, "", terr
		}
		return rr, "no TBM:RUN running line within 20 s after the start ACK", nil
	}
	want := req.Plan.Legs[req.Leg].Name
	have := got.Leg
	if have == "" {
		have = rr.Leg
	}
	if !stageLegMatches(req.Plan, req.Leg, have) {
		stop, err := s.G.CommandWait(ctx, "run stop", 4*time.Second)
		if err != nil {
			return rr, "", err
		}
		reply := fmt.Sprintf("the run leader started in leg %s, not %s: the server's plan differs from the plan file; the run was stopped (.bot run stop)", stageLegWord(have), want)
		if stop.Err != nil || stop.Kind != tbm.KindAck {
			reply = fmt.Sprintf("the run leader started in leg %s, not %s: the server's plan differs from the plan file; a run stop was sent but not confirmed: %s", stageLegWord(have), want, stopDescribe(stop))
		}
		return rr, reply, nil
	}
	rr.Line = got.Line
	return rr, "", nil
}

// resumeRun sends `run resume` and waits for the running line, then
// checks the first fresh running status's plan and leg: on mismatch it
// sends `run stop` (checked like the start's), and refuses. The paused
// run itself was checked for plan and leg before anyone moved (step 1).
func (s *Stager) resumeRun(ctx context.Context, req StageRequest) (*StageRun, string, error) {
	_, n0, _ := s.G.RunStatus()
	r, err := s.G.CommandWait(ctx, "run resume", 6*time.Second)
	if err != nil {
		return nil, "", err
	}
	rr := &StageRun{Sent: ".bot run resume"}
	if r.Err != nil {
		return rr, fmt.Sprintf("the run leader refused the resume: %s (%s)", r.Err.Code, r.Err.Message), nil
	}
	var got *tbm.RunStatus
	if terr := s.waitFor(ctx, 20*time.Second, func() bool {
		st, n, ok := s.G.RunStatus()
		if ok && n > n0 && st.State == tbm.RunRunning {
			c := st
			got = &c
			return true
		}
		return false
	}); terr != nil || got == nil {
		if terr != nil && terr != context.DeadlineExceeded {
			return rr, "", terr
		}
		return rr, "no TBM:RUN running line within 20 s after the resume ACK", nil
	}
	want := req.Plan.Legs[req.Leg].Name
	if !strings.EqualFold(got.Plan, req.Plan.Name) || !stageLegMatches(req.Plan, req.Leg, got.Leg) {
		stop, err := s.G.CommandWait(ctx, "run stop", 4*time.Second)
		if err != nil {
			return rr, "", err
		}
		reply := fmt.Sprintf("the resumed run is plan %s leg %s (step %d/%d), not %s leg %s: the run was stopped (.bot run stop)",
			got.Plan, stageLegWord(got.Leg), got.Step, got.Steps, req.Plan.Name, want)
		if stop.Err != nil || stop.Kind != tbm.KindAck {
			reply = fmt.Sprintf("the resumed run is plan %s leg %s (step %d/%d), not %s leg %s: a run stop was sent but not confirmed: %s",
				got.Plan, stageLegWord(got.Leg), got.Step, got.Steps, req.Plan.Name, want, stopDescribe(stop))
		}
		return rr, reply, nil
	}
	rr.Line = got.Line
	rr.Steps = got.Steps
	return rr, "", nil
}

// stopDescribe is the short reply text for the "stop was sent but not
// confirmed" refusals: the ACTION_ERR, the kind on a timeout, or the
// first line.
func stopDescribe(r tbm.Reply) string {
	if r.Err != nil {
		return fmt.Sprintf("%s (%s)", r.Err.Code, r.Err.Message)
	}
	if len(r.Lines) == 0 {
		return "no answer (kind " + r.Kind + ")"
	}
	return r.Lines[0]
}

// stageStartWords is the start's `from` words: `from leg <name>` when the
// name is safe for the command line (legNameForCommand in observe_start.go:
// one word, short, unique), else the leg's row number (planexport.LegNumber;
// leg 0 starts at the first step: no `from`).
func stageStartWords(p *Plan, leg int) string {
	if name, ok := stageLegNameForCommand(p, leg); ok {
		return "from leg " + name
	}
	if row, ok := stageRowLeg(p, leg); ok && row > 0 {
		return "from " + strconv.Itoa(row)
	}
	return ""
}

func stageLegNameForCommand(p *Plan, leg int) (string, bool) {
	name := p.Legs[leg].Name
	// planexport.MaxLegName (32): the exporter keeps this many characters.
	if name == "" || len(name) > 32 || len(strings.Fields(name)) != 1 || tbm.BadOrderWords(name) != "" {
		return "", false
	}
	n := 0
	for _, l := range p.Legs {
		if strings.EqualFold(l.Name, name) {
			n++
		}
	}
	return name, n == 1
}

// stageRowLeg is planexport.LegNumber: the leg's number in the exported
// rows (optional legs take none).
func stageRowLeg(p *Plan, leg int) (int, bool) {
	if leg < 0 || leg >= len(p.Legs) || p.Legs[leg].Optional != nil {
		return 0, false
	}
	row := 0
	for i := 0; i < leg; i++ {
		if p.Legs[i].Optional == nil {
			row++
		}
	}
	return row, true
}

// stageLegMatches says whether the run leader's leg word names the
// requested leg: its plan name (case-insensitive) or its index. The
// status line carries the name, or its number.
func stageLegMatches(p *Plan, leg int, have string) bool {
	if have == "" {
		return false
	}
	if strings.EqualFold(have, p.Legs[leg].Name) {
		return true
	}
	return have == strconv.Itoa(leg)
}

// stageLegWord is the leg word for a refusal: the raw word, "-" when empty.
func stageLegWord(have string) string {
	if have == "" {
		return "-"
	}
	return have
}
