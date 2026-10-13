package main

import (
	"flag"
	"fmt"
	"io"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/pjerra/turtle-agent/internal/config"
	"github.com/pjerra/turtle-agent/internal/dungeon"
	"github.com/pjerra/turtle-agent/internal/events"
	"github.com/pjerra/turtle-agent/internal/mapview"
	"github.com/pjerra/turtle-agent/internal/navmesh"
)

const mapUsage = "usage: turtle-agent map [--capture FILE] [--events FILE] [--report FILE] [--botwatch FILE [--members a,b]] [--plan dungeons/x.json]\n" +
	"                        [--out run.svg] [--html] [--force] [--config FILE | --mmaps DIR] [--map N] [--box x0,y0,x1,y1] [--from RFC3339] [--to RFC3339] [--pad YD] [--floors auto|off|z1,z2] [--background=false] [--width PX]"

// mapCmd draws a run as a top-down map: one SVG per floor, and with --html a single-file replay with a time
// slider (README, "Map images").
func mapCmd(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("map", flag.ContinueOnError)
	fs.SetOutput(stderr)
	capPath := fs.String("capture", "", "a capture (login --capture): the positions of the party and the hostile creatures, and the events")
	evPath := fs.String("events", "", "saved events (the events method, or capture events) used instead of the capture's own: the dungeon driver's events are in the live log only")
	repPath := fs.String("report", "", "a dungeon_status report (JSON)")
	bwPath := fs.String("botwatch", "", "botwatch samples (JSON lines: t, bots[name, map, x, y, z]) as extra routes")
	members := fs.String("members", "", "with --botwatch, only these bots, comma separated")
	planPath := fs.String("plan", "", "a dungeon plan (dungeons/x.json) drawn over the run")
	out := fs.String("out", "map.svg", "the SVG to write; a map of several floors writes NAME-f1.svg, NAME-f2.svg, ...")
	html := fs.Bool("html", false, "also write the single-file HTML replay, next to the SVG with .html for its extension")
	force := fs.Bool("force", false, "let --out replace an existing file (never a capture)")
	cfgPath := fs.String("config", config.DefaultPath(), "settings file; its mmaps_dir gives the navmesh background")
	mmaps := fs.String("mmaps", "", "folder with the server's mmaps (overrides mmaps_dir)")
	mapID := fs.Int("map", -1, "the map to draw (default: the one with most positions)")
	boxArg := fs.String("box", "", "draw this area only: x0,y0,x1,y1 in game coordinates")
	pad := fs.Float64("pad", mapview.DefaultPad, "yards of margin round what the run covers")
	floors := fs.String("floors", "auto", `how to split a map into floors: auto (from the navmesh), off, or heights such as "-30,-10"`)
	background := fs.Bool("background", true, "draw the navmesh floor (false: leave it out, for a smaller and quicker image)")
	from := fs.String("from", "", "only what happened at or after this time (RFC 3339), to draw one stretch of a long run")
	to := fs.String("to", "", "only what happened at or before this time (RFC 3339)")
	width := fs.Int("width", 960, "the longest side of the map in pixels")
	if err := fs.Parse(args); err != nil {
		return exitUsage
	}
	if fs.NArg() != 0 || (*capPath == "" && *bwPath == "") {
		fmt.Fprintln(stderr, mapUsage)
		fmt.Fprintln(stderr, "map: give --capture and/or --botwatch: positions come from one of them")
		return exitUsage
	}
	fail := func(err error) int {
		fmt.Fprintln(stderr, "map:", err)
		return exitUsage
	}
	cuts, auto, err := mapview.ParseCuts(*floors)
	if err != nil {
		return fail(err)
	}
	var box *mapview.Box
	if *boxArg != "" {
		b, err := parseBox(*boxArg)
		if err != nil {
			return fail(err)
		}
		box = &b
	}
	cfg := mapview.DefaultConfig()
	if *mapID >= 0 {
		cfg.Map = *mapID
	}
	for _, w := range []struct {
		flag string
		val  string
		dst  *time.Time
	}{{"--from", *from, &cfg.From}, {"--to", *to, &cfg.To}} {
		if w.val == "" {
			continue
		}
		t, err := time.Parse(time.RFC3339, w.val)
		if err != nil {
			return fail(fmt.Errorf("%s: %w", w.flag, err))
		}
		*w.dst = t
	}
	var overlay *mapview.Overlay
	var plan *dungeon.Plan
	if *planPath != "" {
		if plan, err = dungeon.Load(*planPath); err != nil {
			return fail(err)
		}
		overlay = mapview.OverlayFromPlan(plan)
		if *mapID < 0 {
			cfg.Map = int(plan.Map)
		}
	}
	b := mapview.NewBuilder(cfg)
	if plan != nil {
		b.SetBoss(mapview.BossEntries(plan)...)
	}

	var evs []events.Event
	if *capPath != "" {
		replayed, info, err := mapview.AddCapture(b, *capPath, cfg)
		if err != nil {
			return fail(err)
		}
		if info.Warning != "" {
			fmt.Fprintf(stderr, "map: warning: %s; drawing the records before it\n", info.Warning)
		}
		fmt.Fprintf(stderr, "map: %s: %d records, %d frames, character %s\n", filepath.Base(*capPath), info.Records, info.Frames, info.Character)
		evs = replayed
	}
	if *evPath != "" {
		if evs, err = mapview.ReadEvents(*evPath); err != nil {
			return fail(err)
		}
	}
	if *bwPath != "" {
		var names []string
		for _, n := range strings.Split(*members, ",") {
			if n = strings.TrimSpace(n); n != "" {
				names = append(names, n)
			}
		}
		n, err := mapview.AddBotwatch(b, *bwPath, names)
		if err != nil {
			return fail(err)
		}
		fmt.Fprintf(stderr, "map: %s: %d bot positions\n", filepath.Base(*bwPath), n)
	}
	var rep *dungeon.Report
	if *repPath != "" {
		if rep, err = mapview.ReadReport(*repPath); err != nil {
			return fail(err)
		}
	}
	run := b.Build(evs, rep)

	// The navmesh background.
	mmapsDir := *mmaps
	if mmapsDir == "" {
		if st, err := config.Load(*cfgPath); err == nil {
			mmapsDir = st.MmapsDir
		} else if *cfgPath != config.DefaultPath() {
			return fail(err) // a settings file asked for by name must load
		}
	}
	var planner *navmesh.Planner
	if mmapsDir != "" {
		planner = navmesh.NewPlanner(mmapsDir)
		defer planner.Close()
	}
	sbox := box
	if sbox == nil {
		bb, ok := run.Bounds(float32(*pad), 60, overlay.Points()...)
		if !ok {
			return fail(fmt.Errorf("no position on map %d: nothing to draw (the capture holds maps %v)", run.Map, runMaps(run)))
		}
		sbox = &bb
	}
	var polys []navmesh.Polygon
	var trunc bool
	note := "background left out (--background=false)"
	if *background {
		polys, trunc, note = mapview.LoadBackground(planner, run.Map, *sbox)
	}
	scene, err := mapview.NewScene(run, mapview.SceneOptions{Box: sbox, Cuts: cuts, Auto: auto, Polys: polys, PolysTruncated: trunc,
		BackgroundNote: note, Overlay: overlay, MaxSide: *width, Config: cfg})
	if err != nil {
		return fail(err)
	}

	// Write the files; none replaces an existing file without --force.
	svgPath := *out
	if !strings.HasSuffix(strings.ToLower(svgPath), ".svg") {
		svgPath += ".svg"
	}
	base := strings.TrimSuffix(svgPath, svgPath[len(svgPath)-4:])
	type job struct {
		path string
		data []byte
	}
	var jobs []job
	for i := 0; i < scene.FloorCount(); i++ {
		p := svgPath
		if scene.FloorCount() > 1 {
			p = fmt.Sprintf("%s-f%d.svg", base, i+1)
		}
		jobs = append(jobs, job{p, scene.SVG(i)})
	}
	if *html {
		data, err := scene.HTML()
		if err != nil {
			return fail(err)
		}
		jobs = append(jobs, job{base + ".html", data})
	}
	for _, j := range jobs {
		f, err := openEventsOut(j.path, "", *force)
		if err != nil {
			return fail(err)
		}
		_, werr := f.Write(j.data)
		if cerr := f.Close(); werr == nil {
			werr = cerr
		}
		if werr != nil {
			return fail(werr)
		}
		fmt.Fprintf(stdout, "%s: %d bytes\n", j.path, len(j.data))
	}
	fmt.Fprintf(stderr, "map: map %d, x %.0f to %.0f, y %.0f to %.0f, %d members, %d creatures, %d events, %d floors; %s\n", run.Map,
		sbox.MinX, sbox.MaxX, sbox.MinY, sbox.MaxY, len(run.Tracks), len(run.Creatures), len(run.Markers), scene.FloorCount(), strings.Join(scene.Notes, "; "))
	return exitOK
}

func runMaps(r mapview.Run) []uint32 {
	seen := map[uint32]bool{}
	var out []uint32
	for _, t := range r.Tracks {
		for _, s := range t.Samples {
			if !seen[s.Map] {
				seen[s.Map] = true
				out = append(out, s.Map)
			}
		}
	}
	return out
}

// parseBox reads x0,y0,x1,y1; the corners may come in any order.
func parseBox(s string) (mapview.Box, error) {
	f := strings.Split(s, ",")
	if len(f) != 4 {
		return mapview.Box{}, fmt.Errorf("--box needs x0,y0,x1,y1, got %q", s)
	}
	var v [4]float32
	for i, x := range f {
		n, err := strconv.ParseFloat(strings.TrimSpace(x), 32)
		if err != nil {
			return mapview.Box{}, fmt.Errorf("--box: %q is not a number", x)
		}
		v[i] = float32(n)
	}
	b := mapview.Box{MinX: min(v[0], v[2]), MinY: min(v[1], v[3]), MaxX: max(v[0], v[2]), MaxY: max(v[1], v[3])}
	if b.MaxX == b.MinX || b.MaxY == b.MinY {
		return mapview.Box{}, fmt.Errorf("--box %q has no area", s)
	}
	return b, nil
}
