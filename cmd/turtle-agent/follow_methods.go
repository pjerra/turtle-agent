package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/pjerra/turtle-agent/internal/brain"
	"github.com/pjerra/turtle-agent/internal/dungeon"
	"github.com/pjerra/turtle-agent/internal/objects"
	"github.com/pjerra/turtle-agent/internal/rpc"
	"github.com/pjerra/turtle-agent/internal/session"
)

// followStartParams are follow_start's parameters (spec 1a, JSON names).
type followStartParams struct {
	Plan   string   `json:"plan"`
	Lead   string   `json:"lead"`
	Near   *float64 `json:"near,omitempty"`
	Far    *float64 `json:"far,omitempty"`
	Assist bool     `json:"assist,omitempty"`
	LostS  *int     `json:"lost_s,omitempty"`
}

type followPoint struct {
	X float32 `json:"x"`
	Y float32 `json:"y"`
	Z float32 `json:"z"`
}

func followPointOf(p dungeon.Point) followPoint { return followPoint{X: p.X, Y: p.Y, Z: p.Z} }

// followStatusJSON is the follow_status result (spec 1c).
type followStatusJSON struct {
	Running       bool         `json:"running"`
	State         string       `json:"state"`
	Plan          string       `json:"plan"`
	Lead          string       `json:"lead"`
	Assist        bool         `json:"assist"`
	Near          float64      `json:"near"`
	Far           float64      `json:"far"`
	LostS         int          `json:"lost_s"`
	LeadSeen      bool         `json:"lead_seen"`
	LeadYd        float64      `json:"lead_yd"`
	LeadAt        *followPoint `json:"lead_at,omitempty"`
	SelfAt        followPoint  `json:"self_at"`
	Map           uint32       `json:"map"`
	TrailYd       float64      `json:"trail_yd"`
	Walks         int          `json:"walks"`
	WalkFails     int          `json:"walk_fails"`
	Deaths        int          `json:"deaths"`
	Resurrections int          `json:"resurrections"`
	CorpseRuns    int          `json:"corpse_runs"`
	ZoneIns       int          `json:"zone_ins"`
	Assists       int          `json:"assists"`
	MaxLeadYd     float64      `json:"max_lead_yd"`
	SecondsOver60 float64      `json:"seconds_over_60"`
	StoppedReason string       `json:"stopped_reason"`
	Notes         []string     `json:"notes"`
	Started       string       `json:"started"`
	Seconds       float64      `json:"seconds"`
}

func followStatusOf(st dungeon.FollowStatus) followStatusJSON {
	out := followStatusJSON{
		Running: st.Running, State: st.State, Plan: st.Plan, Lead: st.Lead,
		Assist: st.Assist, Near: st.Near, Far: st.Far, LostS: st.LostS,
		LeadSeen: st.LeadSeen, LeadYd: st.LeadYd, SelfAt: followPointOf(st.SelfAt), Map: st.Map,
		TrailYd: st.TrailYd, Walks: st.Walks, WalkFails: st.WalkFails, Deaths: st.Deaths,
		Resurrections: st.Resurrections, CorpseRuns: st.CorpseRuns, ZoneIns: st.ZoneIns,
		Assists: st.Assists, MaxLeadYd: st.MaxLeadYd, SecondsOver60: st.SecondsOver60,
		StoppedReason: st.StoppedReason, Notes: st.Notes, Seconds: st.Seconds,
	}
	if out.Notes == nil {
		out.Notes = []string{}
	}
	if st.LeadAt != nil {
		p := followPointOf(*st.LeadAt)
		out.LeadAt = &p
	}
	if !st.Started.IsZero() {
		out.Started = st.Started.UTC().Format("2006-01-02T15:04:05Z")
	}
	return out
}

var followZeroStatus = followStatusJSON{State: "stopped", Notes: []string{}}

// followGame adapts the agent's session to dungeon.FollowGame.
type followGame struct {
	*sessionGame
}

func (g *followGame) SelfRow() (objects.Unit, bool) {
	s := g.cur()
	st := s.Status()
	row, err := s.Unit(st.GUID, "")
	if err != nil {
		return objects.Unit{}, false
	}
	return row, true
}

func (g *followGame) Attack(ctx context.Context) error { return g.cur().Attack(ctx) }

func (g *followGame) StopAttack(ctx context.Context) error { return g.cur().StopAttack(ctx) }

// followBrain adapts the brain controller to dungeon.Brain: only the move lock
// is held (Start/Stop/Rest are no-ops).
type followBrain struct {
	ctl *brain.Controller
}

func (b followBrain) Start() error { return nil }

func (b followBrain) Stop() {}

func (b followBrain) Rest(on bool) {}

func (b followBrain) HoldMove(ctx context.Context) (func(), error) {
	if b.ctl == nil {
		return func() {}, nil
	}
	return b.ctl.HoldMove(ctx)
}

// checkFollowFlags validates login's --follow flags (spec 1d); nil when they are usable.
func checkFollowFlags(lead, plan string, assist bool) error {
	if lead == "" {
		if plan != "" || assist {
			return fmt.Errorf("login: --follow-plan and --follow-assist need --follow")
		}
		return nil
	}
	if plan == "" {
		return fmt.Errorf("login: --follow needs --follow-plan (the plan gives the map, the portal and the entrance trigger)")
	}
	if !planName.MatchString(plan) {
		return fmt.Errorf("login: --follow-plan must be a plan file name such as \"zulfarrak\"")
	}
	return nil
}

// followService holds the one player follow of the agent at a time.
type followService struct {
	mu     sync.Mutex
	cur    func() *session.Session
	dir    string
	nav    bool
	brain  *brain.Controller
	runner *dungeonRunner

	f      *dungeon.PlayerFollower
	cancel context.CancelFunc
	done   chan struct{}
	last   dungeon.FollowStatus
	root   context.Context
}

func (s *followService) Running() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.f != nil
}

func (s *followService) statusLocked() followStatusJSON {
	if s.f != nil {
		return followStatusOf(s.f.Status())
	}
	if s.last.Plan != "" {
		return followStatusOf(s.last)
	}
	return followZeroStatus
}

// Start begins following, or returns the status when already following (spec 1a).
func (s *followService) Start(ctx context.Context, p followStartParams) (followStatusJSON, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if st := s.cur().Status(); st.State != session.StatusInWorld {
		return followStatusJSON{}, &rpc.Error{Code: rpc.CodeNotInWorld, Message: "follow_start needs the character in the world (state " + string(st.State) + ")"}
	}
	if !planName.MatchString(p.Plan) {
		return followStatusJSON{}, &rpc.Error{Code: rpc.CodeInvalidParams, Message: `plan must be a plan file name such as "zulfarrak" (a-z, 0-9, _ and -)`}
	}
	plan, err := dungeon.Load(filepath.Join(s.dir, p.Plan+".json"))
	if err != nil {
		return followStatusJSON{}, &rpc.Error{Code: rpc.CodeInvalidParams, Message: "dungeon plan: " + err.Error()}
	}
	if strings.TrimSpace(p.Lead) == "" {
		return followStatusJSON{}, &rpc.Error{Code: rpc.CodeInvalidParams, Message: "lead: name the lead bot (the run leader's tank)"}
	}
	if st := s.cur().Status(); strings.EqualFold(p.Lead, st.Character) {
		return followStatusJSON{}, &rpc.Error{Code: rpc.CodeInvalidParams, Message: "lead: the lead bot cannot be the player itself"}
	}
	near, far := dungeon.FollowNear, dungeon.FollowFar
	if p.Near != nil {
		near = *p.Near
	}
	if p.Far != nil {
		far = *p.Far
	}
	if near < 3 || near > 30 {
		return followStatusJSON{}, &rpc.Error{Code: rpc.CodeInvalidParams, Message: "near must be 3-30 yd"}
	}
	if far < near+2 || far > 45 {
		return followStatusJSON{}, &rpc.Error{Code: rpc.CodeInvalidParams, Message: "far must be from near+2 to 45 yd"}
	}
	lost := 30 * time.Second
	if p.LostS != nil {
		if *p.LostS < 10 || *p.LostS > 120 {
			return followStatusJSON{}, &rpc.Error{Code: rpc.CodeInvalidParams, Message: "lost_s must be 10-120"}
		}
		lost = time.Duration(*p.LostS) * time.Second
	}
	if s.runner != nil && s.runner.running() {
		return followStatusJSON{}, &rpc.Error{Code: rpc.CodeGame, Message: "a dungeon run is going: it moves the character itself (dungeon_stop it first)"}
	}
	if !s.nav {
		return followStatusJSON{}, &rpc.Error{Code: rpc.CodeGame, Message: "no navmesh planner (set \"mmaps_dir\" in the settings file): the follow mode walks with walk_path"}
	}
	if s.f != nil {
		if plan.Name == s.last.Plan && strings.EqualFold(p.Lead, s.last.Lead) {
			return s.statusLocked(), nil
		}
		if plan.Name != s.last.Plan {
			return followStatusJSON{}, &rpc.Error{Code: rpc.CodeGame, Message: "following with plan " + s.last.Plan + ": follow_stop first"}
		}
		s.f.SetLead(p.Lead)
		return s.statusLocked(), nil
	}
	cfg := dungeon.FollowConfig{Plan: plan, Lead: p.Lead, Near: near, Far: far, Assist: p.Assist, LostAfter: lost, Brain: followBrain{ctl: s.brain}}
	if s.brain != nil {
		cfg.BrainRunning = s.brain.Running
	}
	f := dungeon.NewFollower(&followGame{sessionGame: &sessionGame{cur: s.cur}}, cfg, dungeon.RealClock())
	ctx, cancel := context.WithCancel(s.root)
	done := make(chan struct{})
	s.f, s.cancel, s.done = f, cancel, done
	go func() {
		defer close(done)
		defer cancel()
		f.Run(ctx)
	}()
	s.last = f.Status()
	return s.statusLocked(), nil
}

// Stop ends the follow (spec 1b).
func (s *followService) Stop() followStatusJSON {
	s.mu.Lock()
	f, cancel, done := s.f, s.cancel, s.done
	s.mu.Unlock()
	if f == nil {
		s.mu.Lock()
		defer s.mu.Unlock()
		return s.statusLocked()
	}
	cancel()
	select {
	case <-done:
	case <-time.After(10 * time.Second):
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	st := f.Status()
	if st.StoppedReason == "" {
		st.StoppedReason = "stopped by follow_stop"
	}
	s.last = st
	s.f, s.cancel, s.done = nil, nil, nil
	return followStatusOf(st)
}

// Status answers the follow_status (spec 1c).
func (s *followService) Status() followStatusJSON {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.statusLocked()
}

// registerFollowMethods wires follow_start, follow_stop and follow_status. The
// returned func stops a follow still going; call it before the process ends.
// When opts.Follow is set the follow starts at once (login --follow): success
// goes to opts.Stderr as "follow: ...", a failure too, and the agent keeps running.
func registerFollowMethods(srv *rpc.Server, cur func() *session.Session, dir string, nav bool, brainCtl *brain.Controller, runner *dungeonRunner, opts followOptions) (*followService, func()) {
	if dir == "" {
		dir = "dungeons"
	}
	svc := &followService{cur: cur, dir: dir, nav: nav, brain: brainCtl, runner: runner, root: context.Background()}
	srv.Register("follow_start", rpc.Typed(func(ctx context.Context, p followStartParams) (followStatusJSON, error) {
		return svc.Start(ctx, p)
	}))
	srv.Register("follow_stop", rpc.Typed(func(ctx context.Context, _ empty) (followStatusJSON, error) {
		return svc.Stop(), nil
	}))
	srv.Register("follow_status", rpc.Typed(func(ctx context.Context, _ empty) (followStatusJSON, error) {
		return svc.Status(), nil
	}))
	if opts.Follow != nil {
		stderr := opts.Stderr
		if stderr == nil {
			stderr = io.Discard
		}
		if _, err := svc.Start(context.Background(), *opts.Follow); err != nil {
			fmt.Fprintln(stderr, "follow:", errMessage(err))
		} else {
			fmt.Fprintf(stderr, "follow: following %s (plan %s)\n", opts.Follow.Lead, opts.Follow.Plan)
		}
	}
	return svc, func() {
		svc.Stop()
	}
}

func errMessage(err error) string {
	var re *rpc.Error
	if errors.As(err, &re) {
		return re.Message
	}
	return err.Error()
}

// followOptions are the login follow flags for registerMethodsFor.
type followOptions struct {
	Follow *followStartParams
	Stderr io.Writer
}
