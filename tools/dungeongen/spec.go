package main

// Spec mode: dungeongen -spec tools/dungeongen/plans.json -out-dir dungeons writes the draft plan of every dungeon and raid the spec
// lists, from the world exports in tools/dungeongen/testdata/<data>. The spec is the one place that says, per plan, which bosses in
// which order, which part of a shared map the plan covers (wings), the level band, the party and what the instance needs besides killing
// (events, rituals, gossip, keys). The plans are drafts: the route is the export's spawn clusters walked boss to boss, never mapped
// (AUTO-IMPROVE step 0), and the file says so in its status. docs/PLANS.md is written from the same spec (-table).

import (
	"bytes"
	"encoding/json"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"

	"github.com/pjerra/turtle-agent/internal/dungeon"
)

// SpecFile is plans.json.
type SpecFile struct {
	// VMMaps lists the maps whose navmesh files the test VM holds (the table's mmaps column).
	VMMaps []uint32   `json:"vm_mmaps"`
	Plans  []PlanSpec `json:"plans"`
	// Snaps is snaps.json next to the spec (see snap.go); not part of plans.json.
	Snaps Snaps `json:"-"`
}

// PlanSpec is one plan.
type PlanSpec struct {
	Name  string `json:"name"`
	Title string `json:"title"`
	Map   uint32 `json:"map"`
	Kind  string `json:"kind"` // dungeon or raid
	// Handwritten marks a plan that was written and live-checked by hand (uldaman, zulfarrak) or hand-mapped from the export, the navmesh and a mapping walk (ragefire_chasm, status "mapped"): the generator does not touch its file and
	// the table reads its band and status from it.
	Handwritten bool `json:"handwritten,omitempty"`
	// Data is the folder under testdata that holds the map's export (creatures.tsv, gameobjects.tsv, entrances.tsv, portals.tsv).
	Data string `json:"data,omitempty"`
	// Trigger is the areatrigger_teleport id of the entrance this plan starts at: its target is the plan's entrance, its position on the
	// outer map the plan's portal. Entrance overrides the position (UBRS has no trigger of its own).
	Trigger  uint32      `json:"trigger,omitempty"`
	Entrance *[3]float64 `json:"entrance,omitempty"`
	Bosses   []BossSpec  `json:"bosses,omitempty"`
	// Route orders the bosses by a nearest-neighbour walk from the entrance instead of as listed; Last, if set, stays last.
	Route bool   `json:"route,omitempty"`
	Last  uint32 `json:"last,omitempty"`
	// Bbox limits the packs to the boxes [x1, y1, x2, y2] (a map shared by several wings); bosses are always kept.
	Bbox [][4]float64 `json:"bbox,omitempty"`
	Skip []uint32     `json:"skip,omitempty"`
	// Band overrides the level band; absent, it is taken from the packs (median level - 2 to the 90th percentile, at least median + 3).
	Band []int `json:"band,omitempty"`
	// Size is the group size of a raid plan; the roles are a placeholder split of it.
	Size         int      `json:"size,omitempty"`
	Composition  []string `json:"composition,omitempty"`
	OutsideTele  string   `json:"outside_tele,omitempty"`
	EntranceTele string   `json:"entrance_tele,omitempty"`
	// Scripted lists bosses that are NPCs (npc_flags, faction 35) until a script or gossip makes them hostile.
	Scripted []uint32 `json:"scripted,omitempty"`
	// Notes are the plan's notes after the standard ones; Special are the steps the instance needs besides killing: they go into the notes
	// ("Needs: ...") and the table.
	Notes   []string `json:"notes,omitempty"`
	Special []string `json:"special,omitempty"`
	// LegNeeds maps a leg's boss entry to the leg's needs (ai_dungeon_leg_need rows on re-export): the key doors on the leg's route.
	LegNeeds map[uint32][]dungeon.LegNeed `json:"leg_needs,omitempty"`
}

// BossSpec is a boss entry, or a boss entry with the entries to kill before it ({"entry": 15276, "first": [15275]}).
type BossSpec struct {
	Entry uint32
	First []uint32
}

// UnmarshalJSON reads a number or an object.
func (b *BossSpec) UnmarshalJSON(data []byte) error {
	var n uint32
	if err := json.Unmarshal(data, &n); err == nil {
		b.Entry, b.First = n, nil
		return nil
	}
	var o struct {
		Entry uint32   `json:"entry"`
		First []uint32 `json:"first"`
	}
	d := json.NewDecoder(bytes.NewReader(data))
	d.DisallowUnknownFields()
	if err := d.Decode(&o); err != nil {
		return fmt.Errorf("a boss is an entry or {\"entry\": .., \"first\": [..]}: %w", err)
	}
	b.Entry, b.First = o.Entry, o.First
	return nil
}

// MarshalJSON writes a boss with no first entries as a number.
func (b BossSpec) MarshalJSON() ([]byte, error) {
	if len(b.First) == 0 {
		return json.Marshal(b.Entry)
	}
	return json.Marshal(struct {
		Entry uint32   `json:"entry"`
		First []uint32 `json:"first"`
	}{b.Entry, b.First})
}

// LoadSpec reads and checks plans.json.
func LoadSpec(path string) (*SpecFile, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var sf SpecFile
	d := json.NewDecoder(bytes.NewReader(data))
	d.DisallowUnknownFields()
	if err := d.Decode(&sf); err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	seen := map[string]bool{}
	for i, ps := range sf.Plans {
		at := fmt.Sprintf("%s: plans[%d] (%s)", path, i, ps.Name)
		switch {
		case ps.Name == "" || ps.Map == 0 || ps.Title == "":
			return nil, fmt.Errorf("%s: name, title and map are required", at)
		case seen[ps.Name]:
			return nil, fmt.Errorf("%s: the name is used twice", at)
		case ps.Kind != dungeon.KindDungeon && ps.Kind != dungeon.KindRaid:
			return nil, fmt.Errorf("%s: kind is dungeon or raid", at)
		case ps.Kind == dungeon.KindRaid && ps.Size == 0:
			return nil, fmt.Errorf("%s: a raid needs a size", at)
		case !ps.Handwritten && (ps.Data == "" || len(ps.Bosses) == 0):
			return nil, fmt.Errorf("%s: data and bosses are required", at)
		}
		seen[ps.Name] = true
	}
	if sf.Snaps, err = loadSnaps(filepath.Join(filepath.Dir(path), "snaps.json")); err != nil {
		return nil, err
	}
	return &sf, nil
}

// raidSplit is the placeholder role split of a raid of that size.
func raidSplit(size int) (dungeon.RaidSpec, error) {
	switch size {
	case 10:
		return dungeon.RaidSpec{Size: 10, Tanks: 2, Healers: 3, DPS: 5}, nil
	case 20:
		return dungeon.RaidSpec{Size: 20, Tanks: 3, Healers: 5, DPS: 12}, nil
	case 40:
		return dungeon.RaidSpec{Size: 40, Tanks: 4, Healers: 10, DPS: 26}, nil
	}
	return dungeon.RaidSpec{}, fmt.Errorf("raid size %d: use 10, 20 or 40", size)
}

// defaultComposition is the 5-man party of a draft: a tank, a healer, a mage for crowd control, a rogue (locks, sap) and a hunter.
func defaultComposition() []string {
	return []string{"warrior:tank", "priest:healer", "mage:dps", "rogue:dps", "hunter:dps"}
}

// GenerateSpec builds the draft plan of one spec entry from the exports under dataRoot.
func GenerateSpec(ps PlanSpec, dataRoot string) (*dungeon.Plan, error) {
	dir := filepath.Join(dataRoot, ps.Data)
	all, err := readCreatures(filepath.Join(dir, "creatures.tsv"))
	if err != nil {
		return nil, err
	}
	objs, err := readObjects(filepath.Join(dir, "gameobjects.tsv"))
	if err != nil {
		return nil, err
	}
	entrance, portal, err := specEntrance(ps, dir)
	if err != nil {
		return nil, err
	}
	bossEntry := map[uint32]bool{}
	firsts := map[uint32]bool{}
	var entries []uint32
	for _, b := range ps.Bosses {
		bossEntry[b.Entry] = true
		entries = append(entries, b.Entry)
		for _, f := range b.First {
			bossEntry[f] = true
			firsts[f] = true
		}
	}
	var cs []creature
	for _, c := range all {
		if bossEntry[c.Entry] || inBoxes(c.Pos, ps.Bbox) {
			cs = append(cs, c)
		}
	}
	if ps.Route {
		if entries, err = routeOrder(entries, cs, entrance, ps.Last); err != nil {
			return nil, err
		}
	}
	o := Options{Name: ps.Name, Map: ps.Map, Bosses: entries, Entrance: &entrance, Cluster: 12, NoteRange: 20,
		IgnoreFactions: map[int]bool{35: true}, Skip: ps.Skip, Exclude: firsts, Scripted: toSet(ps.Scripted)}
	band := ps.Band
	if len(band) == 0 {
		band = autoBand(cs, o.IgnoreFactions)
	}
	p, err := Generate(cs, objs, o)
	if err != nil {
		return nil, err
	}
	first := map[uint32][]uint32{}
	for _, b := range ps.Bosses {
		first[b.Entry] = b.First
	}
	for i := range p.Legs {
		p.Legs[i].Boss.First = first[p.Legs[i].Boss.Entry]
		if needs, ok := ps.LegNeeds[p.Legs[i].Boss.Entry]; ok {
			p.Legs[i].Needs = needs
		}
	}
	p.Kind = ps.Kind
	p.Status = dungeon.DraftStatus
	p.LevelBand = band
	p.EntranceTrigger = ps.Trigger
	p.EntranceTele, p.OutsideTele = ps.EntranceTele, ps.OutsideTele
	p.Portal = portal
	if ps.Kind == dungeon.KindRaid {
		r, err := raidSplit(ps.Size)
		if err != nil {
			return nil, err
		}
		p.Raid = &r
	} else {
		p.HireComposition = ps.Composition
		if len(p.HireComposition) == 0 {
			p.HireComposition = defaultComposition()
		}
	}
	p.Notes = []string{
		fmt.Sprintf("DRAFT: a skeleton from the world database export of map %d (tools/dungeongen, spec tools/dungeongen/plans.json). The boss order is the usual clear order, the packs are the spawn clusters nearest each boss; walls, doors, stairs and events are unknown to it. Not mapped, not live-checked: do the GM-invisible mapping walk and correct the route before the first run (AUTO-IMPROVE step 0); dungeon_run refuses it without allow_draft.", ps.Map),
		"A wait_for on a door a lever or key opens needs timeout_s 60 (the default): a linked door can open 30 s after the use.",
	}
	if ps.Kind == dungeon.KindRaid {
		p.Notes = append(p.Notes, fmt.Sprintf("Raid plan: %d players, the tank/healer/dps split is a placeholder. dungeon_run has no raid driver yet; the file is data for the raid leader.", ps.Size))
	}
	for _, s := range ps.Special {
		p.Notes = append(p.Notes, "Needs: "+s)
	}
	p.Notes = append(p.Notes, ps.Notes...)
	return p, nil
}

func toSet(es []uint32) map[uint32]bool {
	m := map[uint32]bool{}
	for _, e := range es {
		m[e] = true
	}
	return m
}

func inBoxes(p dungeon.Point, boxes [][4]float64) bool {
	if len(boxes) == 0 {
		return true
	}
	for _, b := range boxes {
		if float64(p.X) >= math.Min(b[0], b[2]) && float64(p.X) <= math.Max(b[0], b[2]) && float64(p.Y) >= math.Min(b[1], b[3]) && float64(p.Y) <= math.Max(b[1], b[3]) {
			return true
		}
	}
	return false
}

// routeOrder walks the bosses nearest-first from the entrance; last, when set, goes at the end.
func routeOrder(entries []uint32, cs []creature, from dungeon.Point, last uint32) ([]uint32, error) {
	at := map[uint32]dungeon.Point{}
	for _, c := range cs { // the first spawn row of an entry, as Generate takes it
		if _, ok := at[c.Entry]; !ok {
			at[c.Entry] = c.Pos
		}
	}
	var rest []uint32
	for _, e := range entries {
		if _, ok := at[e]; !ok {
			return nil, fmt.Errorf("boss %d has no spawn in creatures.tsv (wrong map or entry?)", e)
		}
		if e != last {
			rest = append(rest, e)
		}
	}
	var out []uint32
	for len(rest) > 0 {
		best, bd := 0, math.MaxFloat64
		for i, e := range rest {
			if d := dist(from, at[e]); d < bd {
				best, bd = i, d
			}
		}
		out = append(out, rest[best])
		from = at[rest[best]]
		rest = append(rest[:best], rest[best+1:]...)
	}
	if last != 0 {
		out = append(out, last)
	}
	return out, nil
}

// autoBand is the level band of the packs: the median level - 2 up to the 90th percentile (at least median + 3), within 1-60. Only
// ordinary mobs count (hostile, no npc_flags, not a rare or a boss).
func autoBand(cs []creature, ignore map[int]bool) []int {
	var lv []int
	for _, c := range cs {
		if ignore[c.Faction] || c.NPCFlags != 0 || c.Rank > 1 || c.LevelMax < 1 {
			continue
		}
		lv = append(lv, c.LevelMax)
	}
	if len(lv) == 0 {
		return nil
	}
	sort.Ints(lv)
	med, p90 := lv[len(lv)/2], lv[len(lv)*9/10]
	lo, hi := max(med-2, 1), min(max(p90, med+3), 60)
	if lo > hi {
		lo = hi
	}
	return []int{lo, hi}
}

// specEntrance is the plan's entrance and the portal on the outer map. Rows come from entrances.tsv (the target of the area trigger) and
// portals.tsv (the trigger's own position); without a trigger the first entrance row is taken and there is no portal.
func specEntrance(ps PlanSpec, dir string) (dungeon.Point, *dungeon.Portal, error) {
	var portal *dungeon.Portal
	if ps.Trigger != 0 {
		pr, err := rows(filepath.Join(dir, "portals.tsv"), "id", "outer_map", "x", "y", "z")
		if err != nil {
			return dungeon.Point{}, nil, err
		}
		for _, m := range pr {
			if m["id"] == strconv.Itoa(int(ps.Trigger)) && m["outer_map"] != "NULL" && m["outer_map"] != "" {
				r := &reader{path: filepath.Join(dir, "portals.tsv")}
				om := uint32(r.num(m, "outer_map"))
				portal = &dungeon.Portal{Map: om, Point: r.pos(m)}
				if r.err != nil {
					return dungeon.Point{}, nil, r.err
				}
			}
		}
	}
	if ps.Entrance != nil {
		return dungeon.Point{X: float32(ps.Entrance[0]), Y: float32(ps.Entrance[1]), Z: float32(ps.Entrance[2])}, portal, nil
	}
	er, err := rows(filepath.Join(dir, "entrances.tsv"), "id", "x", "y", "z")
	if err != nil {
		return dungeon.Point{}, nil, err
	}
	for _, m := range er {
		if ps.Trigger == 0 || m["id"] == strconv.Itoa(int(ps.Trigger)) {
			r := &reader{path: filepath.Join(dir, "entrances.tsv")}
			p := r.pos(m)
			return p, portal, r.err
		}
	}
	return dungeon.Point{}, nil, fmt.Errorf("%s: no entrance row for trigger %d", filepath.Join(dir, "entrances.tsv"), ps.Trigger)
}

// GenerateAll writes every generated plan of the spec to outDir (one file each, in Marshal's form) and returns the file names.
func GenerateAll(sf *SpecFile, dataRoot, outDir string) ([]string, error) {
	var written []string
	for _, ps := range sf.Plans {
		if ps.Handwritten {
			continue
		}
		p, err := GenerateSpec(ps, dataRoot)
		if err != nil {
			return written, fmt.Errorf("%s: %w", ps.Name, err)
		}
		ApplySnaps(p, sf.Snaps[ps.Name])
		b, err := dungeon.Marshal(p)
		if err != nil {
			return written, fmt.Errorf("%s: %w", ps.Name, err)
		}
		f := filepath.Join(outDir, ps.Name+".json")
		if err := os.WriteFile(f, b, 0o644); err != nil {
			return written, err
		}
		written = append(written, f)
	}
	return written, nil
}

// runSpec is the -spec command.
func runSpec(specPath, dataRoot, outDir, table, plansDir, snapRoot string) int {
	sf, err := LoadSpec(specPath)
	if err == nil && snapRoot != "" {
		var sn Snaps
		var report []string
		if sn, report, err = UpdateSnaps(sf, dataRoot, snapRoot); err == nil {
			fmt.Println(strings.Join(report, "\n"))
			sf.Snaps = sn
			err = sn.write(filepath.Join(filepath.Dir(specPath), "snaps.json"))
		}
	}
	if err == nil && outDir != "" {
		var files []string
		files, err = GenerateAll(sf, dataRoot, outDir)
		if err == nil {
			fmt.Printf("wrote %d plans to %s\n", len(files), outDir)
		}
	}
	if err == nil && table != "" {
		var b []byte
		if b, err = Table(sf, plansDir); err == nil {
			err = os.WriteFile(table, b, 0o644)
		}
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, "dungeongen:", err)
		return 1
	}
	return 0
}

// vmHas says whether the test VM holds the map's navmesh files.
func (sf *SpecFile) vmHas(m uint32) bool {
	for _, v := range sf.VMMaps {
		if v == m {
			return true
		}
	}
	return false
}

// Table writes docs/PLANS.md: one row per plan, from the spec and the plan files in plansDir.
func Table(sf *SpecFile, plansDir string) ([]byte, error) {
	var b bytes.Buffer
	b.WriteString(tableHead)
	for _, kind := range []string{dungeon.KindDungeon, dungeon.KindRaid} {
		if kind == dungeon.KindDungeon {
			b.WriteString("\n## Dungeons (level order)\n\n")
		} else {
			b.WriteString("\n## Raids\n\n")
		}
		b.WriteString("| Map | Plan | Level band | Legs | Status | mmaps on the VM | Needs besides killing |\n| --- | --- | --- | --- | --- | --- | --- |\n")
		type row struct {
			lo, hi int
			text   string
		}
		var rs []row
		for _, ps := range sf.Plans {
			if ps.Kind != kind {
				continue
			}
			p, err := dungeon.Load(filepath.Join(plansDir, ps.Name+".json"))
			if err != nil {
				return nil, err
			}
			who := fmt.Sprintf("`%s`", ps.Name)
			if kind == dungeon.KindRaid {
				who += fmt.Sprintf(" (%d-man)", p.Raid.Size)
			}
			lo, hi := p.Band()
			status := p.Status
			if word, _, ok := strings.Cut(status, " "); ok {
				status = word
			}
			vm := "no"
			if sf.vmHas(ps.Map) {
				vm = "yes"
			}
			special := strings.Join(ps.Special, "; ")
			if special == "" {
				special = "none known"
			}
			rs = append(rs, row{lo, hi, fmt.Sprintf("| %d | %s: %s | %d-%d | %d | %s | %s | %s |\n", ps.Map, who, ps.Title, lo, hi, len(p.Legs), status, vm, strings.ReplaceAll(special, "|", "/"))})
		}
		if kind == dungeon.KindDungeon { // level order; the spec order breaks ties
			sort.SliceStable(rs, func(i, j int) bool { return rs[i].lo < rs[j].lo || (rs[i].lo == rs[j].lo && rs[i].hi < rs[j].hi) })
		}
		for _, r := range rs {
			b.WriteString(r.text)
		}
	}
	if err := needsWalkSection(&b, sf, plansDir); err != nil {
		return nil, err
	}
	b.WriteString(tableTail)
	return b.Bytes(), nil
}

// needsWalkSection lists the plans with points the mesh has no floor for (needs_walk): how many of each kind.
func needsWalkSection(b *bytes.Buffer, sf *SpecFile, plansDir string) error {
	var lines []string
	for _, ps := range sf.Plans {
		p, err := dungeon.Load(filepath.Join(plansDir, ps.Name+".json"))
		if err != nil {
			return err
		}
		to, groups, bosses := 0, 0, 0
		for _, l := range p.Legs {
			if l.NeedsWalk {
				to++
			}
			for _, g := range l.PullGroups {
				if g.NeedsWalk {
					groups++
				}
			}
			if l.Boss != nil && l.Boss.NeedsWalk {
				bosses++
			}
		}
		if to+groups+bosses > 0 {
			lines = append(lines, fmt.Sprintf("| `%s` | %d | %d | %d |\n", ps.Name, groups, bosses, to))
		}
	}
	if len(lines) == 0 {
		return nil
	}
	b.WriteString("\n## Points that need a walk\n\nPoints with no navmesh floor within reach (see mmaps above); each is marked `needs_walk` in its plan.\n\n| Plan | Pull groups | Bosses | Leg ends |\n| --- | --- | --- | --- |\n")
	for _, l := range lines {
		b.WriteString(l)
	}
	return nil
}
