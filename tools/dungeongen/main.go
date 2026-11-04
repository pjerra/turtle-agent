// Command dungeongen writes a dungeon plan skeleton (see internal/dungeon) from the spawns of one map.
//
// The spawns come from three TSV files that export.sh writes from the world database (creatures.tsv,
// gameobjects.tsv, entrances.tsv); dungeongen itself reads files only and touches no database. It is re-runnable: the same
// files and flags give the same skeleton, byte for byte.
//
//	dungeongen -dir testdata/uldaman -name uldaman -map 70 -bosses 6906,6910,7228 -out /tmp/uldaman.skeleton.json
//	dungeongen -format dungeons/uldaman.json            # check a plan and print it in the form the files are kept in
//	dungeongen -spec tools/dungeongen/plans.json -out-dir dungeons -table docs/PLANS.md   # every draft dungeon and raid plan, and their table
//
// What the skeleton is: one leg per boss, in the order of -bosses; each pack of hostile creatures (spawns within -cluster
// yards of each other, or in one creature_groups group) goes into the leg of the boss nearest to it, packs ordered by a
// nearest-neighbour walk from the previous leg's end; the doors, levers and altars near a leg are listed in its notes. It does
// not know walls, doors or the order a real run takes, so it is a starting point: the hand-written plan (dungeons/uldaman.json)
// reorders the packs, adds the door steps and checks every waypoint with path_to.
package main

import (
	"encoding/csv"
	"errors"
	"flag"
	"fmt"
	"io"
	"math"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"

	"github.com/pjerra/turtle-agent/internal/dungeon"
)

type creature struct {
	GUID        uint64
	Entry       uint32
	Name        string
	Rank        int
	LevelMax    int
	Faction     int
	NPCFlags    uint32
	Pos         dungeon.Point
	GroupLeader uint64
	Patrol      bool
	Auras       []uint32 // spell ids of the auras the creature spawns with (template and spawn row)
}

type object struct {
	GUID  uint64
	Entry uint32
	Name  string
	Type  int
	Pos   dungeon.Point
	// KeyItem and KeyName are the item a non-GM needs to open the object (its lock's key in Lock.dbc), from the optional
	// key_item and key_name columns of gameobjects.tsv. export.sh does not write them: the key is in Lock.dbc, not in the
	// world database, so they are filled in by hand (or by a tool that reads the DBC); without them nothing is noted.
	KeyItem uint32
	KeyName string
}

// Options are the flags.
type Options struct {
	Name           string
	Map            uint32
	Bosses         []uint32
	Entrance       *dungeon.Point  // default: the first row of entrances.tsv
	Cluster        float64         // yards between spawns of one pack
	IgnoreFactions map[int]bool    // factions that are friendly to the players (35 by default)
	Skip           []uint32        // entries that go into every leg's skip list, besides the rares found
	NoteRange      float64         // yards around a leg within which doors and altars are noted
	Friendly       map[int]bool    // factions known to be friendly to the leader's team (-friendly-factions): a boss of one is warned about
	Exclude        map[uint32]bool // entries that are no pack of their own (a boss's "first" entries): dropped from the trash
	Scripted       map[uint32]bool // bosses that are NPCs until a script or gossip makes them hostile: no npc_flags warning in the leg notes
}

func main() {
	dir := flag.String("dir", "", "folder holding creatures.tsv, gameobjects.tsv and entrances.tsv (written by export.sh)")
	out := flag.String("out", "", "plan file to write (default: standard output)")
	name := flag.String("name", "", "plan name (the file is dungeons/<name>.json)")
	mapID := flag.Uint("map", 0, "instance map id")
	bosses := flag.String("bosses", "", "boss entries in kill order, comma separated")
	entrance := flag.String("entrance", "", "x,y,z of the entrance (default: the first row of entrances.tsv)")
	cluster := flag.Float64("cluster", 12, "yards between the spawns of one pack")
	ignore := flag.String("ignore-factions", "35", "comma separated factions that are not hostile (35: friendly to all)")
	skip := flag.String("skip", "", "entries to add to every leg's skip list, comma separated")
	check := flag.String("check", "", "plan file to lint against -dir's creatures.tsv: warn for a boss or pack member with npc_flags or a faction friendly to -team (no generation)")
	team := flag.String("team", "", "with -check: the leader's team, alliance or horde; what the plan gates to the other team is not looked at")
	strict := flag.Bool("strict", false, "with -check: exit 1 when there is any warning (for CI)")
	friendly := flag.String("friendly-factions", "", "comma separated factions known to be friendly to the leader's team (FactionTemplate.dbc is not in the export, so dungeongen cannot tell; 35 always is)")
	awakened := flag.String("awakened", "", "with -check: comma separated creature entries that spawn friendly (faction 35, stoned) and that the encounter's script turns hostile, so they are no warning (Uldaman: 4857 Stone Keeper, 2748 Archaedas)")
	spec := flag.String("spec", "", "plan spec (tools/dungeongen/plans.json): with -out-dir writes every draft plan it lists, with -table docs/PLANS.md")
	dataRoot := flag.String("data-root", "tools/dungeongen/testdata", "with -spec: the folder holding the export folders the spec names")
	outDir := flag.String("out-dir", "", "with -spec: the folder to write the plan files to (dungeons)")
	table := flag.String("table", "", "with -spec: write the plan table (docs/PLANS.md) to this file")
	plansDir := flag.String("plans-dir", "dungeons", "with -spec -table: the folder the plan files are read from")
	scripted := flag.String("scripted", "", "with -check: comma separated creature entries that are NPCs (npc_flags, faction 35) until a script or dialogue makes them hostile; no warning for them")
	format := flag.String("format", "", "plan file to validate and print in canonical form (to -out, default standard output); no generation")
	snapRoot := flag.String("snap-update", "", "with -spec: rebuild snaps.json from the navmesh folders <dir>/mmaps-turtle<map> (points off the mesh move to the nearest floor, or get needs_walk), then generate as asked")
	flag.Parse()
	if *spec != "" {
		os.Exit(runSpec(*spec, *dataRoot, *outDir, *table, *plansDir, *snapRoot))
	}
	if *format != "" {
		os.Exit(formatFile(*format, *out))
	}
	if *check != "" {
		os.Exit(checkFile(*check, *dir, *team, *friendly, *awakened, *scripted, *strict))
	}
	if *dir == "" || *name == "" || *mapID == 0 || *bosses == "" {
		fmt.Fprintln(os.Stderr, "usage: dungeongen -dir DIR -name NAME -map ID -bosses E1,E2,... [-entrance x,y,z] [-cluster 12] [-ignore-factions 35] [-skip E,...] [-out FILE]")
		os.Exit(2)
	}
	o := Options{Name: *name, Map: uint32(*mapID), Cluster: *cluster, NoteRange: 20, IgnoreFactions: map[int]bool{}}
	var err error
	if o.Bosses, err = parseEntries(*bosses); err == nil {
		o.Skip, err = parseEntries(*skip)
	}
	if err == nil {
		var fs []uint32
		if fs, err = parseEntries(*ignore); err == nil {
			for _, f := range fs {
				o.IgnoreFactions[int(f)] = true
			}
		}
	}
	if err == nil {
		o.Friendly, err = parseFactions(*friendly)
	}
	if err == nil && *entrance != "" {
		var p dungeon.Point
		if p, err = parsePoint(*entrance); err == nil {
			o.Entrance = &p
		}
	}
	var p *dungeon.Plan
	if err == nil {
		p, err = run(*dir, o)
	}
	var b []byte
	if err == nil {
		b, err = dungeon.Marshal(p)
	}
	if err == nil {
		if *out == "" {
			_, err = os.Stdout.Write(b)
		} else {
			err = os.WriteFile(*out, b, 0o644)
		}
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, "dungeongen:", err)
		os.Exit(1)
	}
}

// formatFile validates a plan and writes it back in the canonical form (dungeon.Marshal).
func formatFile(path, out string) int {
	p, err := dungeon.Load(path)
	var b []byte
	if err == nil {
		b, err = dungeon.Marshal(p)
	}
	if err == nil {
		if out == "" {
			_, err = os.Stdout.Write(b)
		} else {
			err = os.WriteFile(out, b, 0o644)
		}
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, "dungeongen:", err)
		return 1
	}
	return 0
}

// parseFactions reads a comma separated list of faction ids into a set.
func parseFactions(s string) (map[int]bool, error) {
	fs, err := parseEntries(s)
	m := map[int]bool{}
	for _, f := range fs {
		m[int(f)] = true
	}
	return m, err
}

// checkFile lints a plan against dir's creatures.tsv and prints the warnings; the exit status is 1 for a plan that does not load or
// an export that cannot be read, 0 otherwise (warnings are for a person to read), and 1 with strict when there is a warning.
func checkFile(path, dir, team, friendly, awakened, scripted string, strict bool) int {
	fail := func(err error) int {
		fmt.Fprintln(os.Stderr, "dungeongen:", err)
		return 1
	}
	if dir == "" {
		return fail(errors.New("-check needs -dir"))
	}
	if team != "" && team != dungeon.TeamAlliance && team != dungeon.TeamHorde {
		return fail(fmt.Errorf("-team %q is neither alliance nor horde", team))
	}
	p, err := dungeon.Load(path)
	if err != nil {
		return fail(err)
	}
	cs, err := readCreatures(filepath.Join(dir, "creatures.tsv"))
	if err != nil {
		return fail(err)
	}
	fm, err := parseFactions(friendly)
	if err != nil {
		return fail(err)
	}
	aw, err := parseEntries(awakened)
	if err != nil {
		return fail(err)
	}
	am := map[uint32]bool{}
	for _, e := range aw {
		am[e] = true
	}
	sc, err := parseEntries(scripted)
	if err != nil {
		return fail(err)
	}
	ws := Check(p, cs, CheckOptions{Team: team, Friendly: fm, Awakened: am, Scripted: toSet(sc)})
	for _, w := range ws {
		fmt.Println("warning:", w)
	}
	fmt.Printf("%d warning(s) for %s as team %q\n", len(ws), path, team)
	if strict && len(ws) > 0 {
		return 1
	}
	return 0
}

func parseEntries(s string) ([]uint32, error) {
	var out []uint32
	for _, f := range strings.Split(s, ",") {
		f = strings.TrimSpace(f)
		if f == "" {
			continue
		}
		n, err := strconv.ParseUint(f, 10, 32)
		if err != nil || n == 0 {
			return nil, fmt.Errorf("%q is not a creature entry", f)
		}
		out = append(out, uint32(n))
	}
	return out, nil
}

func parsePoint(s string) (dungeon.Point, error) {
	f := strings.Split(s, ",")
	var v [3]float64
	if len(f) != 3 {
		return dungeon.Point{}, fmt.Errorf("%q is not x,y,z", s)
	}
	for i := range f {
		var err error
		if v[i], err = strconv.ParseFloat(strings.TrimSpace(f[i]), 32); err != nil {
			return dungeon.Point{}, fmt.Errorf("%q is not x,y,z: %w", s, err)
		}
	}
	return dungeon.Point{X: float32(v[0]), Y: float32(v[1]), Z: float32(v[2])}, nil
}

func run(dir string, o Options) (*dungeon.Plan, error) {
	cs, err := readCreatures(filepath.Join(dir, "creatures.tsv"))
	if err != nil {
		return nil, err
	}
	os_, err := readObjects(filepath.Join(dir, "gameobjects.tsv"))
	if err != nil {
		return nil, err
	}
	if o.Entrance == nil {
		e, err := readEntrance(filepath.Join(dir, "entrances.tsv"))
		if err != nil {
			return nil, err
		}
		o.Entrance = &e
	}
	return Generate(cs, os_, o)
}

// rows reads a TSV file and returns its rows keyed by the header names.
func rows(path string, need ...string) ([]map[string]string, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	r := csv.NewReader(f)
	r.Comma = '\t'
	r.LazyQuotes = true
	r.FieldsPerRecord = -1
	head, err := r.Read()
	if err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	for _, n := range need {
		found := false
		for _, h := range head {
			found = found || h == n
		}
		if !found {
			return nil, fmt.Errorf("%s: no %q column (is it from this version of export.sh?)", path, n)
		}
	}
	var out []map[string]string
	for line := 2; ; line++ {
		rec, err := r.Read()
		if errors.Is(err, io.EOF) {
			return out, nil
		}
		if err != nil {
			return nil, fmt.Errorf("%s:%d: %w", path, line, err)
		}
		if len(rec) != len(head) {
			return nil, fmt.Errorf("%s:%d: %d fields, the header has %d", path, line, len(rec), len(head))
		}
		m := map[string]string{}
		for i, h := range head {
			m[h] = rec[i]
		}
		out = append(out, m)
	}
}

type reader struct {
	path string
	err  error
}

func (r *reader) num(m map[string]string, k string) float64 {
	v, err := strconv.ParseFloat(m[k], 64)
	if err != nil && r.err == nil {
		r.err = fmt.Errorf("%s: %q in column %s is not a number", r.path, m[k], k)
	}
	return v
}

func (r *reader) pos(m map[string]string) dungeon.Point {
	return dungeon.Point{X: float32(r.num(m, "x")), Y: float32(r.num(m, "y")), Z: float32(r.num(m, "z"))}
}

func readCreatures(path string) ([]creature, error) {
	rs, err := rows(path, "guid", "entry", "name", "rank", "faction", "npc_flags", "x", "y", "z", "group_leader", "patrol")
	if err != nil {
		return nil, err
	}
	r := &reader{path: path}
	var out []creature
	for _, m := range rs {
		c := creature{GUID: uint64(r.num(m, "guid")), Entry: uint32(r.num(m, "entry")), Name: m["name"], Rank: int(r.num(m, "rank")),
			Faction: int(r.num(m, "faction")), NPCFlags: uint32(r.num(m, "npc_flags")), Pos: r.pos(m), GroupLeader: uint64(r.num(m, "group_leader")),
			Patrol: m["patrol"] == "1", Auras: parseAuras(m["auras"])}
		if l, err := strconv.Atoi(m["level_max"]); err == nil { // optional: only the band of a spec plan needs it
			c.LevelMax = l
		}
		out = append(out, c)
	}
	return out, r.err
}

// parseAuras reads the auras column of creatures.tsv: spell ids separated by spaces ("6408 5916"). A file written
// before the column existed has none, and NULL or empty is no aura.
func parseAuras(s string) []uint32 {
	var out []uint32
	for _, f := range strings.Fields(s) {
		if n, err := strconv.ParseUint(f, 10, 32); err == nil && n != 0 {
			out = append(out, uint32(n))
		}
	}
	return out
}

func readObjects(path string) ([]object, error) {
	rs, err := rows(path, "guid", "entry", "name", "type", "x", "y", "z")
	if err != nil {
		return nil, err
	}
	r := &reader{path: path}
	var out []object
	for _, m := range rs {
		ob := object{GUID: uint64(r.num(m, "guid")), Entry: uint32(r.num(m, "entry")), Name: m["name"], Type: int(r.num(m, "type")), Pos: r.pos(m), KeyName: m["key_name"]}
		if k := m["key_item"]; k != "" {
			n, err := strconv.ParseUint(k, 10, 32)
			if err != nil && r.err == nil {
				r.err = fmt.Errorf("%s: %q in column key_item is not an item entry", path, k)
			}
			ob.KeyItem = uint32(n)
		}
		out = append(out, ob)
	}
	return out, r.err
}

func readEntrance(path string) (dungeon.Point, error) {
	rs, err := rows(path, "x", "y", "z")
	if err != nil {
		return dungeon.Point{}, err
	}
	if len(rs) == 0 {
		return dungeon.Point{}, fmt.Errorf("%s has no row: give -entrance x,y,z", path)
	}
	r := &reader{path: path}
	p := r.pos(rs[0])
	return p, r.err
}

// pack is spawns that pull together.
type pack struct {
	members []creature
}

func (p pack) center() dungeon.Point {
	var c dungeon.Point
	for _, m := range p.members {
		c.X += m.Pos.X
		c.Y += m.Pos.Y
		c.Z += m.Pos.Z
	}
	n := float32(len(p.members))
	return dungeon.Point{X: c.X / n, Y: c.Y / n, Z: c.Z / n}
}

func dist(a, b dungeon.Point) float64 {
	return math.Sqrt(math.Pow(float64(a.X-b.X), 2) + math.Pow(float64(a.Y-b.Y), 2) + math.Pow(float64(a.Z-b.Z), 2))
}

// Generate builds the skeleton. It is deterministic: ties break on creature guid.
func Generate(cs []creature, objs []object, o Options) (*dungeon.Plan, error) {
	if len(o.Bosses) == 0 {
		return nil, errors.New("no bosses given")
	}
	bossAt := map[uint32]creature{}
	isBoss := map[uint32]bool{}
	for _, b := range o.Bosses {
		isBoss[b] = true
	}
	sort.Slice(cs, func(i, j int) bool { return cs[i].GUID < cs[j].GUID })
	var trash []creature
	skipSet := map[uint32]bool{}
	for _, c := range cs {
		switch {
		case isBoss[c.Entry]:
			if _, dup := bossAt[c.Entry]; !dup {
				bossAt[c.Entry] = c
			}
		case o.Exclude[c.Entry]:
			// part of a boss encounter (boss.first): not a pack
		case o.IgnoreFactions[c.Faction] || c.NPCFlags != 0:
			// friendly, a vendor, quest giver or the like: nothing to pull (and nothing to wait for)
		case c.Rank == 2 || c.Rank == 4:
			skipSet[c.Entry] = true // rare: never wait for it
		default:
			trash = append(trash, c)
		}
	}
	for _, e := range o.Skip {
		skipSet[e] = true
	}
	for _, b := range o.Bosses {
		if _, ok := bossAt[b]; !ok {
			return nil, fmt.Errorf("boss %d has no spawn in creatures.tsv (wrong map or entry?)", b)
		}
	}
	var skip []uint32
	for e := range skipSet {
		skip = append(skip, e)
	}
	sort.Slice(skip, func(i, j int) bool { return skip[i] < skip[j] })

	packs := cluster(trash, o.Cluster)
	// A pack goes with the boss nearest to its centre.
	byBoss := make([][]pack, len(o.Bosses))
	for _, p := range packs {
		best, bd := 0, math.MaxFloat64
		for i, b := range o.Bosses {
			if d := dist(p.center(), bossAt[b].Pos); d < bd {
				best, bd = i, d
			}
		}
		byBoss[best] = append(byBoss[best], p)
	}

	plan := &dungeon.Plan{Version: dungeon.Version, Name: o.Name, Map: o.Map, Entrance: *o.Entrance,
		Notes: []string{"Skeleton from dungeongen: packs sit in the leg of the nearest boss, ordered by a nearest-neighbour walk. Walls and doors are unknown to it; reorder by hand and check every waypoint with path_to. A wait_for on a door a lever or key opens needs timeout_s 60 (the default): a linked door can open 30 s after the use."}}
	from := *o.Entrance
	for i, b := range o.Bosses {
		boss := bossAt[b]
		ps := order(byBoss[i], from)
		leg := dungeon.Leg{Name: strings.ReplaceAll(strings.ToLower(boss.Name), " ", "_"), Skip: skip}
		for _, p := range ps {
			leg.PullGroups = append(leg.PullGroups, packGroup(p, from))
			from = p.members[len(p.members)-1].Pos
		}
		if len(leg.PullGroups) > 0 {
			leg.To = leg.PullGroups[0].Positions[0]
		} else {
			leg.To = boss.Pos
		}
		leg.Boss = &dungeon.Boss{Entry: b, Position: boss.Pos}
		var warns []string
		if !o.Scripted[b] {
			warns = bossWarnings(boss, o.Friendly)
		}
		leg.Notes = append(warns, objectNotes(objs, leg, o.NoteRange)...)
		plan.Legs = append(plan.Legs, leg)
		from = boss.Pos
	}
	return plan, nil
}

// cluster groups spawns that are within radius yards of one another (single linkage) or in one creature group.
func cluster(cs []creature, radius float64) []pack {
	parent := make([]int, len(cs))
	for i := range parent {
		parent[i] = i
	}
	find := func(i int) int {
		for parent[i] != i {
			parent[i] = parent[parent[i]]
			i = parent[i]
		}
		return i
	}
	union := func(a, b int) {
		ra, rb := find(a), find(b)
		if ra != rb {
			parent[max(ra, rb)] = min(ra, rb)
		}
	}
	for i := range cs {
		for j := i + 1; j < len(cs); j++ {
			sameGroup := cs[i].GroupLeader != 0 && cs[i].GroupLeader == cs[j].GroupLeader
			// A patrol walks away from its spawn point, so it is not a pack with the spawns it passes.
			if sameGroup || (!cs[i].Patrol && !cs[j].Patrol && dist(cs[i].Pos, cs[j].Pos) <= radius && math.Abs(float64(cs[i].Pos.Z-cs[j].Pos.Z)) <= 8) {
				union(i, j)
			}
		}
	}
	by := map[int]*pack{}
	var roots []int
	for i := range cs {
		r := find(i)
		if by[r] == nil {
			by[r] = &pack{}
			roots = append(roots, r)
		}
		by[r].members = append(by[r].members, cs[i])
	}
	out := make([]pack, 0, len(roots))
	for _, r := range roots {
		out = append(out, *by[r])
	}
	return out
}

// order walks the packs nearest-first from a point.
func order(ps []pack, from dungeon.Point) []pack {
	var out []pack
	rest := append([]pack(nil), ps...)
	for len(rest) > 0 {
		best, bd := 0, math.MaxFloat64
		for i, p := range rest {
			if d := dist(from, p.center()); d < bd {
				best, bd = i, d
			}
		}
		p := rest[best]
		rest = append(rest[:best], rest[best+1:]...)
		out = append(out, p)
		from = p.center()
	}
	return out
}

// packGroup lists a pack's members nearest to from first (the one to pull), one entry and one position each, and
// marks the group ambush when all of them spawn stealthed (the party cannot pull those).
func packGroup(p pack, from dungeon.Point) dungeon.PullGroup {
	ms := append([]creature(nil), p.members...)
	sort.SliceStable(ms, func(i, j int) bool { return dist(from, ms[i].Pos) < dist(from, ms[j].Pos) })
	var g dungeon.PullGroup
	g.Ambush = len(ms) > 0
	for _, m := range ms {
		g.Entries = append(g.Entries, m.Entry)
		g.Positions = append(g.Positions, m.Pos)
		// A pack is an ambush only when every member is stealthed; with one that can be pulled the driver pulls that one.
		g.Ambush = g.Ambush && dungeon.StealthedAuras(m.Auras)
	}
	return g
}

// Game object types a plan may want to use: door, button, goober, spell focus is left out (the campfires).
var interesting = map[int]string{0: "door", 1: "button", 10: "goober"}

// objectNotes lists the doors, buttons and goobers within r yards of the leg's packs or boss.
func objectNotes(objs []object, l dungeon.Leg, r float64) []string {
	pts := []dungeon.Point{l.Boss.Position}
	for _, g := range l.PullGroups {
		pts = append(pts, g.Positions...)
	}
	var out []string
	for _, ob := range objs {
		kind, ok := interesting[ob.Type]
		if !ok {
			continue
		}
		for _, p := range pts {
			if dist(p, ob.Pos) <= r {
				note := fmt.Sprintf("%s %q entry %d at %.1f %.1f %.1f", kind, ob.Name, ob.Entry, ob.Pos.X, ob.Pos.Y, ob.Pos.Z)
				if ob.KeyItem != 0 {
					// The plan's use_object step for it needs "requires_item" (see internal/dungeon), or the core ignores the use.
					note += fmt.Sprintf(", locked: a non-GM needs item %d%s (set requires_item on its use_object step)", ob.KeyItem, nameSuffix(ob.KeyName))
				}
				out = append(out, note)
				break
			}
		}
	}
	sort.Strings(out)
	return out
}

func nameSuffix(n string) string {
	if n == "" {
		return ""
	}
	return fmt.Sprintf(" %q", n)
}
