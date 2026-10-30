// Command patrol-record records the patrol loops of creatures around an
// already logged-in GM slot agent and writes them in the tb-patrols format
// (spec D2, section 6.3).
//
// The command is a bystander: it leases nothing, sets up nothing and logs in
// nothing. It asks the agent for its status and nearby units, and sends only
// the stance lines of its own allow-list: .gm on, .gm visible off, one
// .go xyz teleport to the area, and .gm visible on at the end. It never
// attacks, casts, targets or moves the character.
package main

import (
	"bufio"
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/pjerra/turtle-agent/internal/navmesh"
	"github.com/pjerra/turtle-agent/internal/objects"
	"github.com/pjerra/turtle-agent/internal/packet"
	"github.com/pjerra/turtle-agent/internal/patrols"
	"github.com/pjerra/turtle-agent/internal/session"
)

// exitPatrolRecordStopped is exit 22: the recording stopped for safety
// (combat, death, lost world); the file is still written.
const exitPatrolRecordStopped = 22

const patrolRecordUsage = "usage: turtle-agent patrol-record <slot-dir> --map M --area x,y,r [--z Z] --minutes N [--sample-ms 1000] [--out FILE] [--spawns DIR] [--mmaps DIR] [--entries e1,e2]"

const (
	patrolRecordAreaMinR   = 10
	patrolRecordAreaMaxR   = 200
	patrolRecordMinMinutes = 1
	patrolRecordMaxMinutes = 60
	patrolRecordMinSample  = 500
	patrolRecordMaxSample  = 5000
	// patrolRecordArriveWait is how long the teleport may take to show the map.
	patrolRecordArriveWait = 30 * time.Second
	// patrolRecordNearYd is how far around the area the units query reaches.
	patrolRecordNearYd = 60
	// patrolRecordSpawnYd is how close a path point must come to a spawn to match it.
	patrolRecordSpawnYd = 40
)

// patrolRecordOptions is one parsed command line.
type patrolRecordOptions struct {
	dir      string
	mapID    uint32
	x, y, z  float32
	r        float32
	minutes  int
	sampleMS int
	out      string
	spawns   string
	mmaps    string
	entries  []uint32
}

// patrolRecordEnv is what the tests replace: the clock, the wait, the slot
// client and the navmesh floors.
type patrolRecordEnv struct {
	now    func() time.Time
	sleep  func(ctx context.Context, d time.Duration) error
	open   func(dir string) (*slotClient, error)
	floors func(mmaps string, mapID uint32, x, y float32) ([]float32, error)
}

// patrolRecordEnvDefault is the live environment.
func patrolRecordEnvDefault() patrolRecordEnv {
	return patrolRecordEnv{
		now:    time.Now,
		sleep:  patrolRecordSleep,
		open:   openSlotClient,
		floors: patrolRecordFloors,
	}
}

// patrolRecordSleep waits d or until ctx ends.
func patrolRecordSleep(ctx context.Context, d time.Duration) error {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-t.C:
		return nil
	}
}

// patrolRecordFloors lists the navmesh surfaces at (x, y) over the whole map.
func patrolRecordFloors(mmaps string, mapID uint32, x, y float32) ([]float32, error) {
	pl := navmesh.NewPlanner(mmaps)
	defer pl.Close()
	fl, err := pl.Floors(mapID, x, y, -2000, 2000)
	if err != nil {
		return nil, err
	}
	out := make([]float32, 0, len(fl))
	for _, f := range fl {
		out = append(out, f.Z)
	}
	return out, nil
}

// patrolRecordSpawn is one row of the world export that matters here.
type patrolRecordSpawn struct {
	guid  uint32
	entry uint32
	x, y  float32
	z     float32
}

func patrolRecordCmd(ctx context.Context, args []string, stdout, stderr io.Writer) int {
	return patrolRecordCmdEnv(ctx, args, stdout, stderr, patrolRecordEnvDefault())
}

// patrolRecordCmdEnv parses the command line, settles the z question, then
// records. Exit: 0 recorded, 22 stopped for safety, 15 slot refused or the
// agent is gone, 16 an RPC error, 2 usage.
func patrolRecordCmdEnv(ctx context.Context, args []string, stdout, stderr io.Writer, env patrolRecordEnv) int {
	fs := flag.NewFlagSet("patrol-record", flag.ContinueOnError)
	fs.SetOutput(stderr)
	mapFlag := fs.String("map", "", "map id (numeric); required")
	areaFlag := fs.String("area", "", "the circle to watch, x,y,r; required")
	zFlag := fs.String("z", "", "the floor's z; needed when the navmesh has several floors at (x, y)")
	minutesFlag := fs.Int("minutes", 0, "recording length in minutes (1-60); required")
	sampleMS := fs.Int("sample-ms", 1000, "milliseconds between samples (500-5000)")
	outFlag := fs.String("out", "", "output file (default ./patrols-<map>-<stamp>.json); refused if it exists")
	spawnsFlag := fs.String("spawns", "", "world export folder with creatures.tsv, to name the patrols' spawns")
	mmapsFlag := fs.String("mmaps", "", "navmesh folder, to find z when --z is absent")
	entriesFlag := fs.String("entries", "", "comma-separated creature entries to record (default: every creature)")
	pos, rest := splitPositionals(fs, args, 1)
	if err := fs.Parse(rest); err != nil {
		return exitUsage
	}
	if len(pos) != 1 || fs.NArg() != 0 {
		fmt.Fprintln(stderr, patrolRecordUsage)
		return exitUsage
	}
	o := patrolRecordOptions{
		dir:      pos[0],
		minutes:  *minutesFlag,
		sampleMS: *sampleMS,
		out:      *outFlag,
		spawns:   *spawnsFlag,
		mmaps:    *mmapsFlag,
	}
	var err error
	if o.mapID, err = patrolRecordParseMap(*mapFlag); err != nil {
		fmt.Fprintf(stderr, "patrol-record: %v\n%s\n", err, patrolRecordUsage)
		return exitUsage
	}
	if o.x, o.y, o.r, err = patrolRecordParseArea(*areaFlag); err != nil {
		fmt.Fprintf(stderr, "patrol-record: %v\n%s\n", err, patrolRecordUsage)
		return exitUsage
	}
	if o.minutes < patrolRecordMinMinutes || o.minutes > patrolRecordMaxMinutes {
		fmt.Fprintf(stderr, "patrol-record: --minutes must be %d-%d, not %d\n", patrolRecordMinMinutes, patrolRecordMaxMinutes, o.minutes)
		return exitUsage
	}
	if o.sampleMS < patrolRecordMinSample || o.sampleMS > patrolRecordMaxSample {
		fmt.Fprintf(stderr, "patrol-record: --sample-ms must be %d-%d, not %d\n", patrolRecordMinSample, patrolRecordMaxSample, o.sampleMS)
		return exitUsage
	}
	if o.entries, err = patrolRecordParseEntries(*entriesFlag); err != nil {
		fmt.Fprintf(stderr, "patrol-record: %v\n", err)
		return exitUsage
	}
	if *zFlag != "" {
		z, err := strconv.ParseFloat(*zFlag, 32)
		if err != nil {
			fmt.Fprintf(stderr, "patrol-record: --z %q is not a number\n", *zFlag)
			return exitUsage
		}
		o.z = float32(z)
	} else {
		if o.mmaps == "" {
			fmt.Fprintln(stderr, "patrol-record: --z is required without --mmaps (several floors may sit at one x,y)")
			return exitUsage
		}
		zs, err := env.floors(o.mmaps, o.mapID, o.x, o.y)
		if err != nil {
			fmt.Fprintf(stderr, "patrol-record: navmesh floors at %.1f,%.1f: %v\n", o.x, o.y, err)
			return exitUsage
		}
		switch len(zs) {
		case 0:
			fmt.Fprintf(stderr, "patrol-record: no navmesh floor at %.1f,%.1f; pass --z\n", o.x, o.y)
			return exitUsage
		case 1:
			o.z = zs[0]
		default:
			names := make([]string, len(zs))
			for i, z := range zs {
				names[i] = fmt.Sprintf("z=%.1f", z)
			}
			fmt.Fprintf(stderr, "patrol-record: %d floors at %.1f,%.1f: %s; multi-floor maps need --z\n",
				len(zs), o.x, o.y, strings.Join(names, ", "))
			return exitUsage
		}
	}
	if o.out == "" {
		o.out = fmt.Sprintf("./patrols-%d-%s.json", o.mapID, env.now().Format("20060102-150405"))
	}
	if _, err := os.Stat(o.out); err == nil {
		fmt.Fprintf(stderr, "patrol-record: %s exists; refusing to overwrite a recording\n", o.out)
		return exitUsage
	} else if !errors.Is(err, os.ErrNotExist) {
		fmt.Fprintf(stderr, "patrol-record: %s: %v\n", o.out, err)
		return exitUsage
	}
	var spawns []patrolRecordSpawn
	if o.spawns != "" {
		if spawns, err = patrolRecordLoadSpawns(o.spawns); err != nil {
			fmt.Fprintf(stderr, "patrol-record: %v\n", err)
			return exitUsage
		}
	}

	client, err := env.open(o.dir)
	if err != nil {
		var refused *slotOpenError
		if errors.As(err, &refused) {
			fmt.Fprintln(stderr, refused.msg)
			return refused.code
		}
		fmt.Fprintf(stderr, "patrol-record: %v\n", err)
		return exitSlotRefused
	}
	// Never record inside a group: the teleport would land in a bound raid's instance.
	var party session.PartyInfo
	if err := client.Call(ctx, "party", nil, &party, 10*time.Second); err != nil {
		fmt.Fprintf(stderr, "patrol-record: party: %v\n", err)
		return patrolRecordErrCode(err)
	}
	if party.InGroup {
		fmt.Fprintln(stderr, "patrol-record: the slot agent is in a group; a recording goes into the GM's own instance, so leave the group first")
		return exitSlotRefused
	}
	var st session.StatusInfo
	if err := client.Call(ctx, "status", nil, &st, 10*time.Second); err != nil {
		fmt.Fprintf(stderr, "patrol-record: status: %v\n", err)
		return patrolRecordErrCode(err)
	}
	if st.State != session.StatusInWorld || st.Dead || st.Ghost {
		fmt.Fprintf(stderr, "patrol-record: the slot agent is not a live character in the world (state %q, dead %v, ghost %v)\n",
			st.State, st.Dead, st.Ghost)
		return exitSlotRefused
	}

	// Stance and teleport: the allow-list's three lines.
	for _, line := range []string{
		".gm on",
		".gm visible off",
		fmt.Sprintf(".go xyz %g %g %g %d", o.x, o.y, o.z, o.mapID),
	} {
		if err := patrolRecordGM(ctx, client, line); err != nil {
			fmt.Fprintf(stderr, "patrol-record: %s: %v\n", line, err)
			return patrolRecordErrCode(err)
		}
	}
	// The safety rule stops on a map mismatch, so wait for the teleport to land first.
	if code := patrolRecordWaitForMap(ctx, client, env, o, stderr); code != exitOK {
		return code
	}

	started := env.now()
	samples, stop := patrolRecordLoop(ctx, client, env, o)
	// Visibility is restored whatever ended the recording.
	if err := patrolRecordGM(ctx, client, ".gm visible on"); err != nil {
		fmt.Fprintf(stderr, "patrol-record: .gm visible on: %v\n", err)
	}

	measured, static, wanderers, _ := patrols.Analyze(samples, patrols.Options{})
	source := fmt.Sprintf("record %s %s", client.Manifest().Server, started.Format("2006-01-02"))
	for i := range measured {
		measured[i].Source = source
	}
	measured = patrolRecordMatchSpawns(measured, spawns)
	file := &patrols.File{
		Format:    patrols.Format,
		Version:   patrols.Version,
		Map:       o.mapID,
		Source:    "record",
		Created:   started,
		Area:      patrols.Area{X: o.x, Y: o.y, R: o.r},
		Minutes:   o.minutes,
		SampleMS:  o.sampleMS,
		Patrols:   measured,
		Static:    static,
		Wanderers: wanderers,
		Stopped:   stop,
	}
	if file.Patrols == nil {
		file.Patrols = []patrols.Patrol{}
	}
	if file.Static == nil {
		file.Static = []patrols.Static{}
	}
	if file.Wanderers == nil {
		file.Wanderers = []patrols.Wanderer{}
	}
	if err := patrols.Write(o.out, file); err != nil {
		fmt.Fprintf(stderr, "patrol-record: write %s: %v\n", o.out, err)
		return exitUsage
	}
	for _, p := range file.Patrols {
		spawns := "no spawn match"
		if len(p.SpawnGUIDs) > 0 {
			spawns = fmt.Sprintf("matches spawn %d", p.SpawnGUIDs[0])
		}
		fmt.Fprintf(stdout, "%s %s: period %.1f s (%d cycles), %d points, %s\n",
			p.ID, p.Name, p.PeriodS, p.CyclesSeen, len(p.Path), spawns)
	}
	if stop != "" {
		fmt.Fprintf(stderr, "patrol-record: stopped: %s; wrote %s\n", stop, o.out)
		return exitPatrolRecordStopped
	}
	return exitOK
}

// patrolRecordErrCode maps an agent failure: a JSON-RPC answer is an RPC
// error (16), anything else means the agent cannot be reached (15, the slot
// refusal S0 uses).
func patrolRecordErrCode(err error) int {
	var rpcErr *slotRPCError
	if errors.As(err, &rpcErr) {
		return exitRPCError
	}
	return exitSlotRefused
}

// patrolRecordGM sends one stance line, and only a line of the allow-list.
func patrolRecordGM(ctx context.Context, client *slotClient, line string) error {
	if !patrolRecordAllowedLine(line) {
		return fmt.Errorf("line outside the allow-list: %q", line)
	}
	return client.Call(ctx, "gm", gmParams{Text: line}, nil, 10*time.Second)
}

// patrolRecordAllowedLine is the stance allow-list: .gm on, .gm visible
// off/on, and .go xyz x y z map.
func patrolRecordAllowedLine(line string) bool {
	switch line {
	case ".gm on", ".gm visible off", ".gm visible on":
		return true
	}
	rest, ok := strings.CutPrefix(line, ".go xyz ")
	if !ok {
		return false
	}
	fields := strings.Fields(rest)
	if len(fields) != 4 {
		return false
	}
	for _, f := range fields[:3] {
		if _, err := strconv.ParseFloat(f, 32); err != nil {
			return false
		}
	}
	if _, err := strconv.ParseUint(fields[3], 10, 32); err != nil {
		return false
	}
	return true
}

// patrolRecordWaitForMap polls status until the teleport shows map M; the
// recording's safety check treats any other map as a stop.
func patrolRecordWaitForMap(ctx context.Context, client *slotClient, env patrolRecordEnv, o patrolRecordOptions, stderr io.Writer) int {
	deadline := env.now().Add(patrolRecordArriveWait)
	for {
		var st session.StatusInfo
		if err := client.Call(ctx, "status", nil, &st, 10*time.Second); err != nil {
			fmt.Fprintf(stderr, "patrol-record: status: %v\n", err)
			return patrolRecordErrCode(err)
		}
		if st.State != session.StatusInWorld || st.Dead || st.Ghost {
			fmt.Fprintf(stderr, "patrol-record: the slot agent left the world (state %q, dead %v, ghost %v)\n",
				st.State, st.Dead, st.Ghost)
			return exitSlotRefused
		}
		if st.Map == o.mapID {
			return exitOK
		}
		if !env.now().Before(deadline) {
			fmt.Fprintf(stderr, "patrol-record: the agent did not reach map %d (it is on map %d) within %s\n",
				o.mapID, st.Map, patrolRecordArriveWait)
			return exitSlotRefused
		}
		if err := env.sleep(ctx, time.Second); err != nil {
			fmt.Fprintf(stderr, "patrol-record: interrupted while waiting for map %d\n", o.mapID)
			return exitSlotRefused
		}
	}
}

// patrolRecordLoop samples until the recording ends or a safety rule stops
// it. It returns the samples and the stop reason ("" for a complete run).
func patrolRecordLoop(ctx context.Context, client *slotClient, env patrolRecordEnv, o patrolRecordOptions) ([]patrols.Sample, string) {
	total := o.minutes * 60_000 / o.sampleMS
	if total < 1 {
		total = 1
	}
	tracked := map[packet.GUID]bool{}
	var samples []patrols.Sample
	for i := range total {
		var st session.StatusInfo
		if err := client.Call(ctx, "status", nil, &st, 10*time.Second); err != nil {
			return samples, "lost the agent: " + err.Error()
		}
		switch {
		case st.State != session.StatusInWorld:
			return samples, fmt.Sprintf("lost world (%s)", st.State)
		case st.Dead:
			return samples, "dead"
		case st.Ghost:
			return samples, "ghost"
		case st.Map != o.mapID:
			return samples, fmt.Sprintf("left map %d (now on map %d)", o.mapID, st.Map)
		}
		var own objects.Unit
		if err := client.Call(ctx, "unit", targetParams{GUID: st.GUID}, &own, 10*time.Second); err != nil {
			return samples, "lost the agent: " + err.Error()
		}
		if own.InCombat {
			return samples, "in combat"
		}
		var units []objects.Unit
		if err := client.Call(ctx, "units", unitsParams{Within: o.r + patrolRecordNearYd}, &units, 10*time.Second); err != nil {
			return samples, "lost the agent: " + err.Error()
		}
		for _, u := range units {
			if u.GUID != st.GUID && u.Target == st.GUID {
				return samples, "targeted by " + patrolRecordUnitName(u)
			}
		}
		now := env.now()
		for _, u := range units {
			if u.Type != "creature" || u.Dead {
				continue
			}
			if len(o.entries) > 0 && !patrolRecordHasEntry(o.entries, u.Entry) {
				continue
			}
			if !tracked[u.GUID] {
				if patrolRecordDist2DSq(u.X, u.Y, o.x, o.y) > float64(o.r)*float64(o.r) {
					continue
				}
				tracked[u.GUID] = true
			}
			samples = append(samples, patrols.Sample{
				T: now, GUID: u.GUID, Entry: u.Entry, Name: u.Name,
				X: u.X, Y: u.Y, Z: u.Z, Moving: u.Moving,
			})
		}
		if i+1 < total {
			if err := env.sleep(ctx, time.Duration(o.sampleMS)*time.Millisecond); err != nil {
				return samples, "interrupted"
			}
		}
	}
	return samples, ""
}

// patrolRecordUnitName is the unit's name for a stop reason, its entry when
// no name is known.
func patrolRecordUnitName(u objects.Unit) string {
	if u.Name != "" {
		return u.Name
	}
	return fmt.Sprintf("entry %d", u.Entry)
}

// patrolRecordParseMap parses --map.
func patrolRecordParseMap(s string) (uint32, error) {
	if s == "" {
		return 0, errors.New("--map is required")
	}
	n, err := strconv.ParseUint(s, 10, 32)
	if err != nil {
		return 0, fmt.Errorf("--map %q is not a map id", s)
	}
	return uint32(n), nil
}

// patrolRecordParseArea parses --area x,y,r and checks the radius.
func patrolRecordParseArea(s string) (x, y, r float32, err error) {
	if s == "" {
		return 0, 0, 0, errors.New("--area is required")
	}
	parts := strings.Split(s, ",")
	if len(parts) != 3 {
		return 0, 0, 0, fmt.Errorf("--area %q is not x,y,r", s)
	}
	var v [3]float64
	for i, p := range parts {
		f, perr := strconv.ParseFloat(strings.TrimSpace(p), 32)
		if perr != nil {
			return 0, 0, 0, fmt.Errorf("--area %q: %q is not a number", s, p)
		}
		v[i] = f
	}
	if v[2] < patrolRecordAreaMinR || v[2] > patrolRecordAreaMaxR {
		return 0, 0, 0, fmt.Errorf("--area radius must be %d-%d yd, not %g", patrolRecordAreaMinR, patrolRecordAreaMaxR, v[2])
	}
	return float32(v[0]), float32(v[1]), float32(v[2]), nil
}

// patrolRecordParseEntries parses --entries e1,e2.
func patrolRecordParseEntries(s string) ([]uint32, error) {
	if s == "" {
		return nil, nil
	}
	var out []uint32
	for _, p := range strings.Split(s, ",") {
		n, err := strconv.ParseUint(strings.TrimSpace(p), 10, 32)
		if err != nil {
			return nil, fmt.Errorf("--entries %q: %q is not an entry", s, p)
		}
		out = append(out, uint32(n))
	}
	return out, nil
}

func patrolRecordHasEntry(entries []uint32, entry uint32) bool {
	for _, e := range entries {
		if e == entry {
			return true
		}
	}
	return false
}

func patrolRecordDist2DSq(ax, ay, bx, by float32) float64 {
	dx, dy := float64(ax-bx), float64(ay-by)
	return dx*dx + dy*dy
}

func patrolRecordDist3DSq(ax, ay, az, bx, by, bz float32) float64 {
	dx, dy, dz := float64(ax-bx), float64(ay-by), float64(az-bz)
	return dx*dx + dy*dy + dz*dz
}

// patrolRecordLoadSpawns reads the guid, entry, x, y, z columns of a world
// export's creatures.tsv by header name, so C0's appended columns change
// nothing here.
func patrolRecordLoadSpawns(dir string) ([]patrolRecordSpawn, error) {
	path := filepath.Join(dir, "creatures.tsv")
	f, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("%s: %w (tools/dungeongen/export.sh writes it)", path, err)
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 0, 64*1024), 1<<20)
	if !sc.Scan() {
		return nil, fmt.Errorf("%s: empty", path)
	}
	header := strings.Split(strings.TrimSuffix(sc.Text(), "\r"), "\t")
	idx := make(map[string]int, len(header))
	for i, h := range header {
		idx[h] = i
	}
	for _, col := range []string{"guid", "entry", "x", "y", "z"} {
		if _, ok := idx[col]; !ok {
			return nil, fmt.Errorf("%s: no %s column", path, col)
		}
	}
	var out []patrolRecordSpawn
	for sc.Scan() {
		line := strings.TrimSuffix(sc.Text(), "\r")
		if strings.TrimSpace(line) == "" {
			continue
		}
		fields := strings.Split(line, "\t")
		if len(fields) < len(header) {
			return nil, fmt.Errorf("%s: %d columns, header has %d", path, len(fields), len(header))
		}
		guid, err := strconv.ParseUint(fields[idx["guid"]], 10, 32)
		if err != nil {
			return nil, fmt.Errorf("%s: guid %q: %w", path, fields[idx["guid"]], err)
		}
		entry, err := strconv.ParseUint(fields[idx["entry"]], 10, 32)
		if err != nil {
			return nil, fmt.Errorf("%s: entry %q: %w", path, fields[idx["entry"]], err)
		}
		var pos [3]float32
		for i, col := range []string{"x", "y", "z"} {
			v, err := strconv.ParseFloat(fields[idx[col]], 32)
			if err != nil {
				return nil, fmt.Errorf("%s: %s %q: %w", path, col, fields[idx[col]], err)
			}
			pos[i] = float32(v)
		}
		out = append(out, patrolRecordSpawn{guid: uint32(guid), entry: uint32(entry), x: pos[0], y: pos[1], z: pos[2]})
	}
	if err := sc.Err(); err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	return out, nil
}

// patrolRecordMatchSpawns names each patrol after the nearest spawn of one of
// its entries within patrolRecordSpawnYd of any path point; unmatched patrols
// keep the lead's hex guid.
func patrolRecordMatchSpawns(ps []patrols.Patrol, spawns []patrolRecordSpawn) []patrols.Patrol {
	for i := range ps {
		p := &ps[i]
		best := 0.0
		var bestGUID uint32
		found := false
		for _, s := range spawns {
			if !patrolRecordHasEntry(p.Entries, s.entry) {
				continue
			}
			for _, pt := range p.Path {
				d := patrolRecordDist3DSq(s.x, s.y, s.z, pt.X, pt.Y, pt.Z)
				if !found || d < best {
					found, best, bestGUID = true, d, s.guid
				}
			}
		}
		if found && best <= patrolRecordSpawnYd*patrolRecordSpawnYd {
			p.SpawnGUIDs = []uint32{bestGUID}
			p.ID = strconv.FormatUint(uint64(bestGUID), 10)
		}
	}
	return ps
}
