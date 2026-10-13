package main

import (
	"context"
	"path/filepath"
	"strconv"
	"sync"
	"time"

	"github.com/pjerra/turtle-agent/internal/dungeon"
	"github.com/pjerra/turtle-agent/internal/events"
	"github.com/pjerra/turtle-agent/internal/mapview"
	"github.com/pjerra/turtle-agent/internal/navmesh"
	"github.com/pjerra/turtle-agent/internal/rpc"
	"github.com/pjerra/turtle-agent/internal/session"
)

// mapSampleEvery is how often the recorder looks at the session.
const mapSampleEvery = 2 * time.Second

type mapParams struct {
	Floor int `json:"floor,omitempty"` // the floor to draw, from 0 (a map of one floor has only 0)
	// Plan names a plan in the dungeons folder to draw over the run (the same name dungeon_run takes).
	Plan string `json:"plan,omitempty"`
	// Floors is "auto" (the default: split by height where the navmesh stacks floors), "off", or heights "-30,-10".
	Floors string `json:"floors,omitempty"`
	// Background false leaves the navmesh out (a smaller, faster image).
	Background *bool   `json:"background,omitempty"`
	HTML       bool    `json:"html,omitempty"` // also return the single-file replay
	Pad        float32 `json:"pad,omitempty"`
}

type mapResult struct {
	Map        uint32   `json:"map"`
	Floor      int      `json:"floor"`
	Floors     []string `json:"floors"` // the name of each floor
	Seconds    float64  `json:"seconds"`
	Members    int      `json:"members"`
	Creatures  int      `json:"creatures"`
	Events     int      `json:"events"`
	Background bool     `json:"background"` // the navmesh is drawn
	Notes      []string `json:"notes"`
	SVG        string   `json:"svg"`
	HTML       string   `json:"html,omitempty"`
}

// mapRecorder keeps the positions of the party and the hostile creatures the session shows, every few seconds,
// so a snapshot can draw the run so far. It starts with the agent and costs one pass over the object store per
// sample; its memory is bounded (mapview.Builder).
type mapRecorder struct {
	cur func() *session.Session
	mu  sync.Mutex
	b   *mapview.Builder
	end func()
}

func newMapRecorder(cur func() *session.Session) *mapRecorder {
	cfg := mapview.DefaultConfig()
	cfg.FrameStep = mapSampleEvery
	r := &mapRecorder{cur: cur, b: mapview.NewBuilder(cfg)}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	r.end = func() { cancel(); <-done }
	go func() {
		defer close(done)
		t := time.NewTicker(mapSampleEvery)
		defer t.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case now := <-t.C:
				r.sample(now)
			}
		}
	}()
	return r
}

func (r *mapRecorder) sample(now time.Time) {
	f, ok := mapview.SessionFrame(r.cur(), now, nil)
	if !ok {
		return
	}
	r.mu.Lock()
	r.b.AddFrame(f)
	r.b.SetMap(f.Map) // the map the leader is on now
	r.mu.Unlock()
}

// registerMapMethods wires map_snapshot. The returned func stops the recorder; call it before the process ends.
// planner and dr may be nil (no mmaps_dir: no background; no dungeon runner: no report).
func registerMapMethods(srv *rpc.Server, cur func() *session.Session, planner *navmesh.Planner, dungeonsDir string, dr *dungeonRunner) func() {
	if dungeonsDir == "" {
		dungeonsDir = "dungeons"
	}
	rec := newMapRecorder(cur)
	srv.Register("map_snapshot", rpc.Typed(func(ctx context.Context, p mapParams) (mapResult, error) {
		cuts, auto, err := mapview.ParseCuts(p.Floors)
		if err != nil {
			return mapResult{}, &rpc.Error{Code: rpc.CodeInvalidParams, Message: err.Error()}
		}
		var overlay *mapview.Overlay
		var plan *dungeon.Plan
		if p.Plan != "" {
			if !planName.MatchString(p.Plan) {
				return mapResult{}, &rpc.Error{Code: rpc.CodeInvalidParams, Message: `plan must be a plan file name such as "uldaman" (a-z, 0-9, _ and -)`}
			}
			if plan, err = dungeon.Load(filepath.Join(dungeonsDir, p.Plan+".json")); err != nil {
				return mapResult{}, &rpc.Error{Code: rpc.CodeInvalidParams, Message: "dungeon plan: " + err.Error()}
			}
			overlay = mapview.OverlayFromPlan(plan)
		}
		// Take one frame now so the picture is as of this call, then build from a copy of what was recorded.
		rec.sample(time.Now())
		// The plan's bosses mark this image only: the recorder goes on after the call.
		var bosses []uint32
		if plan != nil {
			bosses = mapview.BossEntries(plan)
		}
		rec.mu.Lock()
		var rep *dungeon.Report
		if dr != nil {
			if st := dr.status(); st.Report != nil {
				rep = st.Report
			}
		}
		run := rec.b.BuildWithBosses(allEvents(cur().Log()), rep, bosses...)
		rec.mu.Unlock()

		cfg := mapview.DefaultConfig()
		pad := p.Pad
		if pad <= 0 {
			pad = mapview.DefaultPad
		}
		box, ok := run.Bounds(pad, 60, overlay.Points()...)
		if !ok {
			return mapResult{}, &rpc.Error{Code: rpc.CodeGame, Message: "nothing to draw yet: the recorder has no position on the map (is the character in the world?)"}
		}
		var polys []navmesh.Polygon
		var trunc bool
		note := "background left out"
		if p.Background == nil || *p.Background {
			// The box is read into a planner of its own, closed when the call returns: the tiles a live plan holds
			// stay where they are, and nothing this call loaded outlives it.
			var draw *navmesh.Planner
			if planner != nil {
				draw = planner.Scratch()
				defer draw.Close()
			}
			polys, trunc, note = mapview.LoadBackground(draw, run.Map, box)
		}
		scene, err := mapview.NewScene(run, mapview.SceneOptions{Box: &box, Cuts: cuts, Auto: auto, Polys: polys, PolysTruncated: trunc,
			BackgroundNote: note, Overlay: overlay, Config: cfg})
		if err != nil {
			return mapResult{}, &rpc.Error{Code: rpc.CodeGame, Message: err.Error()}
		}
		if p.Floor < 0 || p.Floor >= scene.FloorCount() {
			return mapResult{}, &rpc.Error{Code: rpc.CodeInvalidParams, Message: "floor: this map has floors 0 to " + strconv.Itoa(scene.FloorCount()-1)}
		}
		res := mapResult{Map: run.Map, Floor: p.Floor, Seconds: run.Seconds, Members: len(run.Tracks), Creatures: len(run.Creatures),
			Events: len(run.Markers), Background: scene.Terrain, Notes: scene.Notes, SVG: string(scene.SVG(p.Floor))}
		for _, f := range scene.Floors {
			res.Floors = append(res.Floors, f.Name)
		}
		if p.HTML {
			h, err := scene.HTML()
			if err != nil {
				return mapResult{}, &rpc.Error{Code: rpc.CodeInternal, Message: err.Error()}
			}
			res.HTML = string(h)
		}
		return res, nil
	}))
	return rec.end
}

// allEvents reads the whole log, page by page.
func allEvents(l *events.Log) []events.Event {
	var out []events.Event
	for since := uint64(0); ; {
		page := l.Read(events.Query{Since: since, Limit: events.MaxLimit})
		out = append(out, page.Events...)
		since = page.Next
		if !page.More {
			return out
		}
	}
}
