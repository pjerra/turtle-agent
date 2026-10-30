package mapview

import (
	"fmt"
	"math"
	"slices"
	"sort"
	"strings"
	"time"

	"github.com/pjerra/turtle-agent/internal/dungeon"
	"github.com/pjerra/turtle-agent/internal/events"
)

// MemberFrame is where a party member stood in a Frame. A unit is told apart by its GUID, so two units that share a
// name are two tracks; a member without a GUID (a botwatch sample) is told apart by its name.
type MemberFrame struct {
	Name string
	GUID uint64
	Pos
	Self bool // the player the agent plays: the leader
}

// UnitFrame is a hostile creature in a Frame.
type UnitFrame struct {
	GUID      uint64
	Entry     uint32
	Name      string
	Level     uint32
	Health    uint32
	MaxHealth uint32
	InCombat  bool
	Dead      bool
	Pos
}

// ObjectFrame is a game object in a Frame.
type ObjectFrame struct {
	GUID  uint64
	Entry uint32
	Name  string
	Type  string // door, button, goober, chest, ...
	Pos
}

// Frame is what the agent saw at one moment: the party, the hostile creatures and the game objects in view.
type Frame struct {
	T        time.Time
	Map      uint32
	Members  []MemberFrame
	Hostiles []UnitFrame
	Objects  []ObjectFrame
}

// Config tunes how a Builder reads its input. The zero value is not usable: start from DefaultConfig.
type Config struct {
	Map         int           // the map to draw; AutoMap: the one the most samples are on
	Title       string        // the image's title; empty: the plan name, else "run on map N"
	GapAfter    time.Duration // a member unseen for longer than this leaves a gap in the route
	PullGap     time.Duration // quiet time that ends a fight: the next combat event is a new pull
	MinMove     float32       // yards a member must move before a new sample is kept (else only every KeepEvery)
	KeepEvery   time.Duration // how often a standing member still gets a sample
	PackRadius  float64       // yards within which hostile creatures count as one pack
	TeleportMin float64       // yards a member must jump within GapAfter for the route to show a teleport
	// From and To keep only what happened between them (zero: no limit), to draw one leg of a long run.
	From, To time.Time
	// FrameStep is how far apart the frames of a replay are (a source's choice; kept here for its callers).
	FrameStep time.Duration
	// Boss lists creature entries that are bosses besides the ones a creature's health gives away (a plan's).
	Boss map[uint32]bool
}

// AutoMap lets the builder pick the map with the most samples.
const AutoMap = -1

// DefaultConfig is the configuration the CLI and the RPC use.
func DefaultConfig() Config {
	return Config{
		Map: AutoMap, GapAfter: 30 * time.Second, PullGap: 15 * time.Second, MinMove: 1.5, KeepEvery: 10 * time.Second,
		PackRadius: 10, TeleportMin: 60, FrameStep: time.Second,
	}
}

// memberKey is the track a member's samples go to: its GUID, or with none (a botwatch sample) its name.
type memberKey struct {
	guid uint64
	name string // only when guid is zero
}

type memberAcc struct {
	name    string
	leader  bool
	samples []rawSample
}

type rawSample struct {
	t   time.Time
	m   uint32
	pos Pos
}

type creatureAcc struct {
	c         Creature
	seen      time.Time
	lastSeen  time.Time
	maxHealth uint32
	combatAt  time.Time // first time seen in combat
	calmAt    time.Time // first time seen alive and out of combat after combatAt+calmAfter
	lastCombt time.Time
	deadAt    time.Time
}

// calmAfter is how long past its last combat sample a creature must be seen calm and alive to count as evaded.
const calmAfter = 4 * time.Second

type objectAcc struct {
	entry uint32
	name  string
	typ   string
	m     uint32
	pos   Pos
}

// Builder gathers frames, then turns them with the events and the report into a Run. It is not safe for
// concurrent use.
type Builder struct {
	cfg       Config
	members   map[memberKey]*memberAcc
	order     []memberKey
	creatures map[uint64]*creatureAcc
	objects   map[uint64]*objectAcc
	first     time.Time
	last      time.Time
	mapVotes  map[uint32]int
}

// NewBuilder returns an empty Builder.
func NewBuilder(cfg Config) *Builder {
	return &Builder{cfg: cfg, members: map[memberKey]*memberAcc{}, creatures: map[uint64]*creatureAcc{},
		objects: map[uint64]*objectAcc{}, mapVotes: map[uint32]int{}}
}

// Cap on what a long-lived Builder (the live RPC) keeps, so a day-long agent stays small.
const (
	maxSamplesPerMember = 40000
	maxCreatures        = 6000
)

// SetMap fixes the map the run is drawn on.
func (b *Builder) SetMap(m uint32) { b.cfg.Map = int(m) }

// SetTitle sets the image's title.
func (b *Builder) SetTitle(t string) { b.cfg.Title = t }

// SetBoss adds creature entries to the bosses of every Run the builder makes from now on. A builder that outlives one
// run (the RPC's recorder) passes that run's bosses to BuildWithBosses instead.
func (b *Builder) SetBoss(entries ...uint32) {
	if b.cfg.Boss == nil {
		b.cfg.Boss = map[uint32]bool{}
	}
	for _, e := range entries {
		b.cfg.Boss[e] = true
	}
}

// within says whether a time falls in the window the config asks for.
func (b *Builder) within(t time.Time) bool {
	return (b.cfg.From.IsZero() || !t.Before(b.cfg.From)) && (b.cfg.To.IsZero() || !t.After(b.cfg.To))
}

func (b *Builder) note(t time.Time) {
	if t.IsZero() {
		return
	}
	if b.first.IsZero() || t.Before(b.first) {
		b.first = t
	}
	if t.After(b.last) {
		b.last = t
	}
}

// AddFrame takes one frame. Frames may come in any order.
func (b *Builder) AddFrame(f Frame) {
	if !b.within(f.T) {
		return
	}
	b.note(f.T)
	for _, m := range f.Members {
		k := memberKey{guid: m.GUID}
		if m.GUID == 0 {
			k.name = m.Name
		}
		a := b.members[k]
		if a == nil {
			a = &memberAcc{name: m.Name}
			b.members[k] = a
			b.order = append(b.order, k)
		} else if a.name == "" {
			a.name = m.Name
		}
		if m.Self {
			a.leader = true
		}
		b.addSample(a, rawSample{t: f.T, m: f.Map, pos: m.Pos})
	}
	for _, u := range f.Hostiles {
		b.addUnit(f, u)
	}
	for _, o := range f.Objects {
		if _, ok := b.objects[o.GUID]; !ok && len(b.objects) < maxCreatures {
			b.objects[o.GUID] = &objectAcc{entry: o.Entry, name: o.Name, typ: o.Type, m: f.Map, pos: o.Pos}
		} else if ok && o.Name != "" {
			b.objects[o.GUID].name = o.Name
		}
	}
}

func (b *Builder) addSample(a *memberAcc, s rawSample) {
	if n := len(a.samples); n > 0 {
		last := a.samples[n-1]
		if s.t.Before(last.t) { // out of order (merged sources): insert in place
			i := sort.Search(n, func(i int) bool { return a.samples[i].t.After(s.t) })
			a.samples = append(a.samples, rawSample{})
			copy(a.samples[i+1:], a.samples[i:])
			a.samples[i] = s
			b.mapVotes[s.m]++
			return
		}
		if last.m == s.m && s.t.Sub(last.t) < b.cfg.KeepEvery && dist2D(last.pos, s.pos) < float64(b.cfg.MinMove) &&
			math.Abs(float64(last.pos.Z-s.pos.Z)) < 2 {
			return
		}
	}
	a.samples = append(a.samples, s)
	b.mapVotes[s.m]++
	if len(a.samples) > maxSamplesPerMember { // thin the older half to every second sample
		half := len(a.samples) / 2
		kept := a.samples[:0:0]
		for i, x := range a.samples[:half] {
			if i%2 == 0 {
				kept = append(kept, x)
			}
		}
		a.samples = append(kept, a.samples[half:]...)
	}
}

func (b *Builder) addUnit(f Frame, u UnitFrame) {
	a := b.creatures[u.GUID]
	if a == nil {
		if len(b.creatures) >= maxCreatures {
			return
		}
		a = &creatureAcc{seen: f.T}
		a.c = Creature{GUID: u.GUID, Entry: u.Entry, Name: u.Name, Level: u.Level, Map: f.Map, Pos: u.Pos}
		b.creatures[u.GUID] = a
	}
	if u.Name != "" {
		a.c.Name = u.Name
	}
	if u.Level != 0 {
		a.c.Level = u.Level
	}
	a.lastSeen = f.T
	if u.MaxHealth > 100 && u.MaxHealth > a.maxHealth { // real values, not the percent the server sends when it hides them
		a.maxHealth = u.MaxHealth
	}
	switch {
	case u.Dead:
		if a.deadAt.IsZero() {
			a.deadAt = f.T
		}
	case u.InCombat:
		if a.combatAt.IsZero() {
			a.combatAt = f.T
		}
		a.lastCombt, a.calmAt = f.T, time.Time{}
	case !a.combatAt.IsZero() && a.calmAt.IsZero() && f.T.Sub(a.lastCombt) >= calmAfter:
		a.calmAt = f.T
	}
}

// Samples is how many positions the builder holds, for tests and the RPC's status line.
func (b *Builder) Samples() (n int) {
	for _, a := range b.members {
		n += len(a.samples)
	}
	return n
}

// Build turns what was added, with the events and (optionally) the dungeon report, into a Run.
func (b *Builder) Build(evs []events.Event, rep *dungeon.Report) Run {
	return b.BuildWithBosses(evs, rep)
}

// BuildWithBosses is Build with bosses for this Run only, besides the builder's own (SetBoss): the entries are bosses
// in this Run and nowhere else, so a plan's bosses drawn over a recorder that outlives the run stay with that run.
func (b *Builder) BuildWithBosses(evs []events.Event, rep *dungeon.Report, bosses ...uint32) Run {
	kept := make([]events.Event, 0, len(evs))
	for _, e := range evs {
		if b.within(e.Time) {
			kept = append(kept, e)
			b.note(e.Time)
		}
	}
	evs = kept
	if rep != nil && b.within(rep.Started) {
		b.note(rep.Started)
	}
	run := Run{Title: b.cfg.Title, Start: b.first}
	if !b.first.IsZero() {
		run.Seconds = b.last.Sub(b.first).Seconds()
	}
	if b.cfg.Map >= 0 {
		run.Map = uint32(b.cfg.Map)
	} else {
		run.Map = b.dominantMap()
	}
	if run.Title == "" {
		run.Title = fmt.Sprintf("run on map %d", run.Map)
		if rep != nil && rep.Plan != "" {
			run.Title = fmt.Sprintf("%s (map %d)", rep.Plan, run.Map)
		}
	}
	sec := func(t time.Time) float64 { return t.Sub(b.first).Seconds() }

	run.Tracks = b.tracks(sec)
	run.Gaps = countGaps(run.Tracks, run.Map, b.cfg.GapAfter.Seconds())

	b.buildCreatures(&run, evs, sec, bosses)
	b.buildMarkers(&run, evs, rep, sec)
	if run.Gaps > 0 {
		run.Notes = append(run.Notes, fmt.Sprintf("%d %s of a route with no position for over %.0f s (dotted)", run.Gaps, plural(run.Gaps, "stretch", "stretches"), b.cfg.GapAfter.Seconds()))
	}
	run.Notes = append(run.Notes, "only what the agent saw is drawn: units outside its view distance are missing")
	return run
}

func (b *Builder) dominantMap() uint32 {
	var best uint32
	n := -1
	for m, v := range b.mapVotes {
		if v > n || (v == n && m < best) {
			best, n = m, v
		}
	}
	return best
}

// tracks makes one track per member, leader first. A sample without a GUID (a botwatch bot) joins the one member with a
// GUID of its name when there is only one, so a bot that a capture and a botwatch file both saw is one track; with two
// such units, or none, it is a track of its own. The accumulators are read, not changed: a later Build sees the same
// samples.
func (b *Builder) tracks(sec func(time.Time) float64) []Track {
	guidOf := map[string]memberKey{} // the member with a GUID of each name
	named := map[string]int{}        // how many members with a GUID have each name
	for _, k := range b.order {
		if k.guid != 0 {
			name := b.members[k].name
			guidOf[name] = k
			named[name]++
		}
	}
	var targets []memberKey
	parts := map[memberKey][]*memberAcc{}
	for _, k := range b.order {
		t := k
		if k.guid == 0 && named[k.name] == 1 {
			t = guidOf[k.name]
		}
		if _, ok := parts[t]; !ok {
			targets = append(targets, t)
		}
		parts[t] = append(parts[t], b.members[k])
	}
	out := make([]Track, 0, len(targets))
	for _, t := range targets {
		tr := Track{Name: b.members[t].name}
		samples := parts[t][0].samples
		for _, a := range parts[t] {
			tr.Leader = tr.Leader || a.leader
		}
		if len(parts[t]) > 1 {
			samples = nil
			for _, a := range parts[t] {
				samples = append(samples, a.samples...)
			}
			sort.SliceStable(samples, func(i, j int) bool { return samples[i].t.Before(samples[j].t) })
		}
		for _, s := range samples {
			tr.Samples = append(tr.Samples, Sample{T: sec(s.t), Pos: s.pos, Map: s.m})
		}
		out = append(out, tr)
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].Leader && !out[j].Leader })
	return out
}

// countGaps counts the stretches of a track on the map with a hole longer than limit seconds.
func countGaps(tracks []Track, mapID uint32, limit float64) (n int) {
	for _, t := range tracks {
		for i := 1; i < len(t.Samples); i++ {
			a, c := t.Samples[i-1], t.Samples[i]
			if a.Map == mapID && c.Map == mapID && c.T-a.T > limit {
				n++
			}
		}
	}
	return n
}

func (b *Builder) buildCreatures(run *Run, evs []events.Event, sec func(time.Time) float64, bosses []uint32) {
	// Deaths the event log names beat the frames' (a corpse seen later).
	killedAt := map[uint64]time.Time{}
	for _, e := range evs {
		if e.Kind == events.KindDeath && e.SourceGUID != 0 {
			if _, ok := killedAt[uint64(e.SourceGUID)]; !ok {
				killedAt[uint64(e.SourceGUID)] = e.Time
			}
		}
		if e.Kind == events.KindKill && e.TargetGUID != 0 {
			if t, ok := killedAt[uint64(e.TargetGUID)]; !ok || e.Time.Before(t) {
				killedAt[uint64(e.TargetGUID)] = e.Time
			}
		}
	}
	var hp []float64
	for _, a := range b.creatures {
		if a.maxHealth > 0 {
			hp = append(hp, float64(a.maxHealth))
		}
	}
	sort.Float64s(hp)
	median := 0.0
	if len(hp) > 0 {
		median = hp[len(hp)/2]
	}
	guids := make([]uint64, 0, len(b.creatures))
	for g := range b.creatures {
		guids = append(guids, g)
	}
	sort.Slice(guids, func(i, j int) bool {
		a, c := b.creatures[guids[i]], b.creatures[guids[j]]
		if !a.seen.Equal(c.seen) {
			return a.seen.Before(c.seen)
		}
		return guids[i] < guids[j]
	})
	var cs []Creature
	for _, g := range guids {
		a := b.creatures[g]
		if a.c.Map != run.Map {
			continue
		}
		c := a.c
		c.Seen = sec(a.seen)
		c.FateT = sec(a.lastSeen)
		dead := a.deadAt
		if t, ok := killedAt[g]; ok && (dead.IsZero() || t.Before(dead)) {
			dead = t
		}
		switch {
		case !dead.IsZero():
			c.Fate, c.FateT = FateKilled, sec(dead)
		case !a.calmAt.IsZero():
			c.Fate, c.FateT = FateEvaded, sec(a.calmAt)
		case !a.combatAt.IsZero():
			c.Fate = FateAlive
		default:
			c.Fate = FateSkipped
		}
		c.Boss = b.cfg.Boss[c.Entry] || slices.Contains(bosses, c.Entry) ||
			(median > 0 && a.maxHealth >= 8000 && float64(a.maxHealth) >= 5*median)
		cs = append(cs, c)
	}
	groupPacks(cs, b.cfg.PackRadius)
	run.Creatures = cs
}

// groupPacks numbers the packs: creatures within radius yards of each other (chained), two or more to a pack.
// Bosses stand alone. The numbers follow the order of the slice.
func groupPacks(cs []Creature, radius float64) {
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
	for i := range cs {
		if cs[i].Boss {
			continue
		}
		for j := i + 1; j < len(cs); j++ {
			if cs[j].Boss || cs[i].Map != cs[j].Map || math.Abs(float64(cs[i].Z-cs[j].Z)) > 8 {
				continue
			}
			if dist2D(cs[i].Pos, cs[j].Pos) <= radius {
				parent[find(i)] = find(j)
			}
		}
	}
	size := map[int]int{}
	for i := range cs {
		size[find(i)]++
	}
	num := map[int]int{}
	for i := range cs {
		r := find(i)
		if size[r] < 2 {
			continue
		}
		if _, ok := num[r]; !ok {
			num[r] = len(num) + 1
		}
		cs[i].Pack = num[r]
	}
}

// memberAt is the position and map of a named member at t, the leader's when the name is unknown.
func memberAt(run *Run, name string, t float64) (Pos, uint32, bool) {
	var leader *Track
	for i := range run.Tracks {
		if run.Tracks[i].Name == name {
			p, ok := run.Tracks[i].At(t)
			return p, run.Tracks[i].MapAt(t), ok
		}
		if run.Tracks[i].Leader {
			leader = &run.Tracks[i]
		}
	}
	if leader == nil && len(run.Tracks) > 0 {
		leader = &run.Tracks[0]
	}
	if leader == nil {
		return Pos{}, 0, false
	}
	p, ok := leader.At(t)
	return p, leader.MapAt(t), ok
}

func (b *Builder) buildMarkers(run *Run, evs []events.Event, rep *dungeon.Report, sec func(time.Time) float64) {
	members := map[string]bool{}
	for _, t := range run.Tracks {
		members[t.Name] = true
	}
	creatureByGUID := map[uint64]*Creature{}
	for i := range run.Creatures {
		creatureByGUID[run.Creatures[i].GUID] = &run.Creatures[i]
	}
	add := func(m Marker) { run.Markers = append(run.Markers, m) }
	atMember := func(name string, t float64) (Pos, bool) { p, _, ok := memberAt(run, name, t); return p, ok }
	mapOf := func(name string, t float64) uint32 { _, m, _ := memberAt(run, name, t); return m }
	leaderName := ""
	for _, t := range run.Tracks {
		if t.Leader {
			leaderName = t.Name
		}
	}
	sorted := append([]events.Event(nil), evs...)
	sort.SliceStable(sorted, func(i, j int) bool { return sorted[i].Time.Before(sorted[j].Time) })

	// Shortcuts of the report, by time, to flag the teleports among them.
	var shortcuts []dungeon.Shortcut
	if rep != nil {
		shortcuts = rep.Shortcuts
	}
	usedShortcut := make([]bool, len(shortcuts))

	var lastCombat time.Time
	pulls := 0
	for _, e := range sorted {
		t := sec(e.Time)
		switch e.Kind {
		case events.KindAttackStart, events.KindMelee, events.KindSpellDamage, events.KindPeriodic:
			if !e.Party {
				continue
			}
			if !lastCombat.IsZero() && e.Time.Sub(lastCombat) < b.cfg.PullGap {
				lastCombat = e.Time
				continue
			}
			lastCombat = e.Time
			pos, ok, pmap := Pos{}, false, uint32(0)
			if c := creatureByGUID[uint64(e.TargetGUID)]; c != nil && !members[e.Target] {
				pos, ok, pmap = c.Pos, true, c.Map
			} else if c := creatureByGUID[uint64(e.SourceGUID)]; c != nil && !members[e.Source] {
				pos, ok, pmap = c.Pos, true, c.Map
			}
			if !ok {
				pos, ok = atMember(leaderName, t)
				pmap = mapOf(leaderName, t)
			}
			if !ok {
				continue
			}
			pulls++
			label := fmt.Sprintf("pull %d", pulls)
			if e.Target != "" && !members[e.Target] {
				label += ": " + e.Target
			} else if e.Source != "" && !members[e.Source] {
				label += ": " + e.Source
			}
			add(Marker{Kind: MarkPull, T: t, Label: label, Map: pmap, Pos: pos})
		case events.KindDeath:
			if !members[e.Source] {
				continue
			}
			if pos, ok := atMember(e.Source, t); ok {
				add(Marker{Kind: MarkDeath, T: t, Label: e.Source, Map: mapOf(e.Source, t), Pos: pos})
			}
		case events.KindKill:
			if c := creatureByGUID[uint64(e.TargetGUID)]; c != nil && c.Boss {
				add(Marker{Kind: MarkBossKill, T: t, Label: c.Name + " killed", Map: c.Map, Pos: c.Pos})
			}
		case events.KindDungeonFailed:
			errText, _ := e.Data["error"].(string)
			if errText == "wipe" || strings.HasPrefix(errText, "wipe") || strings.Contains(e.Text, "wipe") {
				if pos, ok := atMember(leaderName, t); ok {
					add(Marker{Kind: MarkWipe, T: t, Label: "wipe", Map: mapOf(leaderName, t), Pos: pos})
				}
			}
		case events.KindDungeonLeg:
			if phase, _ := e.Data["phase"].(string); phase == "start" {
				name, _ := e.Data["name"].(string)
				leg := num(e.Data["leg"])
				if pos, ok := atMember(leaderName, t); ok {
					add(Marker{Kind: MarkLeg, T: t, Label: fmt.Sprintf("leg %d %s", int(leg), name), Map: mapOf(leaderName, t), Pos: pos})
				}
			}
		case events.KindGameObject:
			ch, _ := e.Data["change"].(string)
			if ch != "state" && ch != "animation" {
				continue
			}
			o := b.objects[uint64(e.SourceGUID)]
			if o == nil || o.m != run.Map || !doorType(o.typ) {
				continue
			}
			label := o.name
			if label == "" {
				label = fmt.Sprintf("%s %d", o.typ, o.entry)
			}
			if ch == "state" {
				label += fmt.Sprintf(": %v to %v", e.Data["old"], e.Data["new"])
			}
			add(Marker{Kind: MarkDoor, T: t, Label: label + " [" + o.typ + "]", Map: o.m, Pos: o.pos})
		case events.KindSession:
			if st, _ := e.Data["state"].(string); st != "teleported" {
				continue
			}
			x, y, z := num(e.Data["x"]), num(e.Data["y"]), num(e.Data["z"])
			m := Marker{Kind: MarkTeleport, T: t, Label: "teleport", Map: mapOf(leaderName, t), Pos: Pos{X: x, Y: y, Z: z}}
			if from, ok := atMember(leaderName, t-0.5); ok {
				m.From = &from
			}
			for i, s := range shortcuts {
				if usedShortcut[i] || !strings.HasPrefix(s.Kind, "teleport") || math.Abs(sec(s.At)-t) > 15 {
					continue
				}
				usedShortcut[i], m.Flag, m.Label = true, true, "GM teleport ("+s.Kind+")"
				break
			}
			add(m)
		}
	}
	for i, s := range shortcuts {
		if usedShortcut[i] {
			continue
		}
		t := sec(s.At)
		pos, ok := atMember(leaderName, t)
		if !ok {
			continue
		}
		detail := s.Kind
		if s.Detail != "" {
			detail += ": " + s.Detail
		}
		kind := MarkGM
		m := Marker{Kind: kind, T: t, Label: "GM " + detail, Flag: true, Map: mapOf(leaderName, t), Pos: pos}
		if strings.HasPrefix(s.Kind, "teleport") {
			m.Kind, m.Label = MarkTeleport, "GM teleport ("+s.Kind+")"
		}
		add(m)
	}
	if rep != nil {
		for _, d := range rep.Deaths {
			t := sec(d.At)
			if hasMarker(run.Markers, MarkDeath, d.Name, t, 3) {
				continue
			}
			if pos, ok := atMember(d.Name, t); ok {
				add(Marker{Kind: MarkDeath, T: t, Label: d.Name, Map: mapOf(d.Name, t), Pos: pos})
			}
		}
	}
	// What happened on another map, or that the report dates outside the span of the run (a window was asked for), is not part of this image.
	inside := run.Markers[:0]
	for _, m := range run.Markers {
		if m.Map == run.Map && m.T >= 0 && m.T <= run.Seconds+1 {
			inside = append(inside, m)
		}
	}
	run.Markers = inside
	sort.SliceStable(run.Markers, func(i, j int) bool { return run.Markers[i].T < run.Markers[j].T })
}

func hasMarker(ms []Marker, kind, label string, t, within float64) bool {
	for _, m := range ms {
		if m.Kind == kind && m.Label == label && math.Abs(m.T-t) <= within {
			return true
		}
	}
	return false
}

// doorType says whether a game object type is one whose use the map shows: doors, levers and buttons, and the
// goobers (altars, keystones) a dungeon plan uses.
func doorType(t string) bool {
	switch t {
	case "door", "button", "goober":
		return true
	}
	return false
}

func num(v any) float32 {
	switch x := v.(type) {
	case float64:
		return float32(x)
	case float32:
		return x
	case int:
		return float32(x)
	case int64:
		return float32(x)
	case uint32:
		return float32(x)
	}
	return 0
}

func plural(n int, one, many string) string {
	if n == 1 {
		return one
	}
	return many
}
