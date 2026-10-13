package tbm

import (
	"errors"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/pjerra/turtle-agent/internal/packet"
)

// The perception feed of the remote commander (TortoiseBots issue #884 phase 2, docs/guides/player-controls.md
// "Perception feed"): while a commander is bound and its view is on, the module sends one frame about every
// AiPlayerbot.RemoteCommanderViewMs as system lines
//
//	TBM:VIEW|v1|<seq>|B|<lead>,<party>,<hostiles>,<threats>
//	TBM:VIEW|v1|<seq>|P|<party record>[;<party record>...]
//	TBM:VIEW|v1|<seq>|H|<hostile record>[;...]
//	TBM:VIEW|v1|<seq>|T|<threat record>[;...]
//	TBM:VIEW|v1|<seq>|E|<lines>
//
// B opens a frame and E closes it; E counts every line of the frame, B and E included. P, H and T come between, in that
// order, each only when it has records, and a part may span several lines.

// ViewVersion is the only grammar version the parser reads. A frame of another version is counted, never guessed at.
const ViewVersion = "v1"

// ViewPrefix starts every line of the feed.
const ViewPrefix = "TBM:VIEW|"

// Field counts of the records (a longer record is accepted: a later version may append fields).
const (
	partyFields   = 18
	hostileFields = 9
)

// maxPendingFrames bounds the frames the assembler holds open at once (lines of frames that never complete are dropped).
const maxPendingFrames = 4

// staleSeqWindow is how far behind the newest published frame a complete frame may be and still be taken for an
// out-of-order late arrival (dropped); farther behind it can only be a new binding, whose seq starts again at 1.
const staleSeqWindow = 3

// PartyRec is one bot of the commanded party (a P record).
type PartyRec struct {
	GUID      packet.GUID `json:"guid"`
	Name      string      `json:"name"`
	HP        uint32      `json:"hp"`
	MaxHP     uint32      `json:"max_hp"`
	Power     uint32      `json:"power"`
	MaxPower  uint32      `json:"max_power"`
	PowerType uint8       `json:"power_type"` // 0 mana, 1 rage, 2 focus, 3 energy
	X         float32     `json:"x"`
	Y         float32     `json:"y"`
	Z         float32     `json:"z"`
	O         float32     `json:"o"`
	Map       uint32      `json:"map"`
	Instance  uint32      `json:"instance"`
	Target    packet.GUID `json:"target,omitempty"` // the bot's target, else its victim
	Spell     uint32      `json:"spell,omitempty"`  // the spell it casts or channels
	Dead      bool        `json:"dead"`
	State     string      `json:"state"`            // c combat, n non-combat, d dead, r reaction
	Action    string      `json:"action,omitempty"` // the last action its AI ran
}

// HPPct is the bot's health in percent (100 when the maximum is unknown).
func (p PartyRec) HPPct() int { return pctOf(p.HP, p.MaxHP) }

// PowerPct is the bot's power in percent (100 when it has none).
func (p PartyRec) PowerPct() int { return pctOf(p.Power, p.MaxPower) }

// UsesMana says whether the bot's power is mana and it has any.
func (p PartyRec) UsesMana() bool { return p.PowerType == 0 && p.MaxPower > 0 }

// InCombat says whether the bot's AI is in a combat or reaction state.
func (p PartyRec) InCombat() bool { return !p.Dead && (p.State == "c" || p.State == "r") }

func pctOf(v, max uint32) int {
	if max == 0 {
		return 100
	}
	return int(uint64(v) * 100 / uint64(max))
}

// HostileRec is one hostile the bots fight or see (an H record).
type HostileRec struct {
	GUID   packet.GUID `json:"guid"`
	Entry  uint32      `json:"entry"` // 0 for a player
	X      float32     `json:"x"`
	Y      float32     `json:"y"`
	Z      float32     `json:"z"`
	HPPct  int         `json:"hp_pct"`
	Target packet.GUID `json:"target,omitempty"`
	Spell  uint32      `json:"spell,omitempty"`
	Flags  string      `json:"flags"` // s stealthed, e evading, c crowd-controlled, "-" none
}

// Stealthed says whether the unit is stealthed or invisible to the party at large (only a detecting bot lists it).
func (h HostileRec) Stealthed() bool { return strings.Contains(h.Flags, "s") }

// Evading says whether the unit is walking back to its spawn.
func (h HostileRec) Evading() bool { return strings.Contains(h.Flags, "e") }

// CrowdControlled says whether the unit is stunned, incapacitated, polymorphed, feared or rooted.
func (h HostileRec) CrowdControlled() bool { return strings.Contains(h.Flags, "c") }

// ThreatEntry is one entry of a hostile's threat list: Pct is the share of the top entry's threat (the top is 100).
type ThreatEntry struct {
	GUID packet.GUID `json:"guid"`
	Pct  int         `json:"pct"`
}

// ThreatRec is the top (up to three) of one hostile's threat list, highest first.
type ThreatRec struct {
	Hostile packet.GUID   `json:"hostile"`
	Top     []ThreatEntry `json:"top"`
}

// ViewFrame is one assembled frame: the commander's world model of the commanded party.
type ViewFrame struct {
	Version  string       `json:"version"`
	Seq      uint64       `json:"seq"`
	Lead     packet.GUID  `json:"lead"`
	Party    []PartyRec   `json:"party"`
	Hostiles []HostileRec `json:"hostiles"`
	Threats  []ThreatRec  `json:"threats"`
	// Truncated: the module dropped records to fit the frame (the optional fifth field of the B line, TortoiseBots #901).
	Truncated bool      `json:"truncated,omitempty"`
	Lines     int       `json:"lines"` // the lines the frame came in, B and E included
	At        time.Time `json:"at"`    // when the frame completed (set by the feed)
}

// LeadRec is the lead bot's party record.
func (f ViewFrame) LeadRec() (PartyRec, bool) {
	for _, p := range f.Party {
		if p.GUID == f.Lead && f.Lead != 0 {
			return p, true
		}
	}
	return PartyRec{}, false
}

// Member finds a party bot by GUID.
func (f ViewFrame) Member(g packet.GUID) (PartyRec, bool) {
	for _, p := range f.Party {
		if p.GUID == g {
			return p, true
		}
	}
	return PartyRec{}, false
}

// IsMember says whether g is a bot of the party.
func (f ViewFrame) IsMember(g packet.GUID) bool {
	_, ok := f.Member(g)
	return ok
}

// Hostile finds a hostile by GUID.
func (f ViewFrame) Hostile(g packet.GUID) (HostileRec, bool) {
	for _, h := range f.Hostiles {
		if h.GUID == g {
			return h, true
		}
	}
	return HostileRec{}, false
}

// Threat finds the threat record of a hostile.
func (f ViewFrame) Threat(hostile packet.GUID) (ThreatRec, bool) {
	for _, t := range f.Threats {
		if t.Hostile == hostile {
			return t, true
		}
	}
	return ThreatRec{}, false
}

// ThreatHolder is the bot at the top of the hostile's threat list (the one the unit should be hitting).
func (f ViewFrame) ThreatHolder(hostile packet.GUID) (packet.GUID, bool) {
	t, ok := f.Threat(hostile)
	if !ok || len(t.Top) == 0 {
		return 0, false
	}
	return t.Top[0].GUID, true
}

// ThreatOf is the share of the top entry's threat that who has on the hostile, and whether the list shows who at all (the
// record keeps only the top three: a bot not listed is below them all).
func (f ViewFrame) ThreatOf(hostile, who packet.GUID) (pct int, listed bool) {
	t, ok := f.Threat(hostile)
	if !ok {
		return 0, false
	}
	for _, e := range t.Top {
		if e.GUID == who {
			return e.Pct, true
		}
	}
	return 0, false
}

// EngagedHostiles are the hostiles fighting the party: they target a bot, or carry threat on one.
func (f ViewFrame) EngagedHostiles() []HostileRec {
	var out []HostileRec
	for _, h := range f.Hostiles {
		if h.HPPct <= 0 {
			continue
		}
		if f.IsMember(h.Target) {
			out = append(out, h)
			continue
		}
		if t, ok := f.Threat(h.GUID); ok && len(t.Top) > 0 {
			out = append(out, h)
		}
	}
	return out
}

// PartyInCombat says whether any living bot of the party is in combat, or any hostile is engaged with it.
func (f ViewFrame) PartyInCombat() bool {
	for _, p := range f.Party {
		if p.InCombat() {
			return true
		}
	}
	return len(f.EngagedHostiles()) > 0
}

// AllDead says whether the frame has a party and every bot of it is dead.
func (f ViewFrame) AllDead() bool {
	if len(f.Party) == 0 {
		return false
	}
	for _, p := range f.Party {
		if !p.Dead {
			return false
		}
	}
	return true
}

// ViewLine is one TBM:VIEW line taken apart.
type ViewLine struct {
	Version string
	Seq     uint64
	Part    byte // 'B', 'P', 'H', 'T' or 'E'
	Body    string
}

// Errors a line or a frame can be refused with. Version is not an error of the stream: the line is counted and skipped.
var (
	ErrNotView    = errors.New("not a TBM:VIEW line")
	ErrViewFormat = errors.New("malformed TBM:VIEW line")
	ErrViewSeq    = errors.New("TBM:VIEW sequence number out of range")
)

// ParseViewLine splits a TBM:VIEW line into its version, seq, part and body. The body is not read here. An unknown version
// is returned (ok) so that the caller can count it.
func ParseViewLine(line string) (ViewLine, error) {
	rest, ok := strings.CutPrefix(line, ViewPrefix)
	if !ok {
		return ViewLine{}, ErrNotView
	}
	f := strings.SplitN(rest, "|", 4)
	if len(f) != 4 || len(f[0]) == 0 || len(f[2]) != 1 {
		return ViewLine{}, ErrViewFormat
	}
	seq, err := strconv.ParseUint(f[1], 10, 63)
	if err != nil {
		return ViewLine{}, fmt.Errorf("%w: %q", ErrViewSeq, f[1])
	}
	switch f[2][0] {
	case 'B', 'P', 'H', 'T', 'E':
	default:
		return ViewLine{}, fmt.Errorf("%w: part %q", ErrViewFormat, f[2])
	}
	return ViewLine{Version: f[0], Seq: seq, Part: f[2][0], Body: f[3]}, nil
}

// ParseGUIDField reads a GUID of the feed: "0x" + hex, or decimal, or "0" for none.
func ParseGUIDField(s string) (packet.GUID, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return 0, ErrViewFormat
	}
	var v uint64
	var err error
	if h, ok := strings.CutPrefix(strings.ToLower(s), "0x"); ok {
		v, err = strconv.ParseUint(h, 16, 64)
	} else {
		v, err = strconv.ParseUint(s, 10, 64)
	}
	if err != nil {
		return 0, fmt.Errorf("%w: guid %q", ErrViewFormat, s)
	}
	return packet.GUID(v), nil
}

// fieldReader reads typed fields of a record and keeps the first error.
type fieldReader struct {
	f   []string
	err error
}

func (r *fieldReader) fail(i int, what string) {
	if r.err == nil {
		r.err = fmt.Errorf("%w: field %d (%s) %q", ErrViewFormat, i+1, what, r.f[i])
	}
}

func (r *fieldReader) guid(i int, what string) packet.GUID {
	g, err := ParseGUIDField(r.f[i])
	if err != nil {
		r.fail(i, what)
	}
	return g
}

func (r *fieldReader) u32(i int, what string) uint32 {
	v, err := strconv.ParseUint(r.f[i], 10, 32)
	if err != nil {
		r.fail(i, what)
	}
	return uint32(v)
}

func (r *fieldReader) f32(i int, what string) float32 {
	v, err := strconv.ParseFloat(r.f[i], 32)
	if err != nil || v != v || v > 1e9 || v < -1e9 {
		r.fail(i, what)
		return 0
	}
	return float32(v)
}

func (r *fieldReader) pct(i int, what string) int {
	v, err := strconv.Atoi(r.f[i])
	if err != nil || v < 0 || v > 1000 {
		r.fail(i, what)
	}
	return v
}

// ParsePartyRec reads one P record.
func ParsePartyRec(s string) (PartyRec, error) {
	f := strings.Split(s, ",")
	if len(f) < partyFields {
		return PartyRec{}, fmt.Errorf("%w: party record has %d fields, needs %d", ErrViewFormat, len(f), partyFields)
	}
	r := &fieldReader{f: f}
	p := PartyRec{
		GUID: r.guid(0, "guid"), Name: f[1], HP: r.u32(2, "hp"), MaxHP: r.u32(3, "maxhp"), Power: r.u32(4, "power"), MaxPower: r.u32(5, "maxpower"),
		X: r.f32(7, "x"), Y: r.f32(8, "y"), Z: r.f32(9, "z"), O: r.f32(10, "o"), Map: r.u32(11, "map"), Instance: r.u32(12, "instance"),
		Target: r.guid(13, "target"), Spell: r.u32(14, "spell"), State: f[16], Action: f[17],
	}
	p.PowerType = uint8(min(r.u32(6, "powertype"), 255))
	p.Dead = r.u32(15, "dead") != 0
	if p.Action == "-" {
		p.Action = ""
	}
	if r.err != nil {
		return PartyRec{}, r.err
	}
	return p, nil
}

// ParseHostileRec reads one H record.
func ParseHostileRec(s string) (HostileRec, error) {
	f := strings.Split(s, ",")
	if len(f) < hostileFields {
		return HostileRec{}, fmt.Errorf("%w: hostile record has %d fields, needs %d", ErrViewFormat, len(f), hostileFields)
	}
	r := &fieldReader{f: f}
	h := HostileRec{GUID: r.guid(0, "guid"), Entry: r.u32(1, "entry"), X: r.f32(2, "x"), Y: r.f32(3, "y"), Z: r.f32(4, "z"),
		HPPct: r.pct(5, "hp%"), Target: r.guid(6, "target"), Spell: r.u32(7, "spell"), Flags: f[8]}
	if r.err != nil {
		return HostileRec{}, r.err
	}
	return h, nil
}

// ParseThreatRec reads one T record: the hostile, then guid:pct entries.
func ParseThreatRec(s string) (ThreatRec, error) {
	f := strings.Split(s, ",")
	if len(f) < 2 {
		return ThreatRec{}, fmt.Errorf("%w: threat record has no entries", ErrViewFormat)
	}
	h, err := ParseGUIDField(f[0])
	if err != nil {
		return ThreatRec{}, err
	}
	t := ThreatRec{Hostile: h}
	for _, e := range f[1:] {
		g, pct, ok := strings.Cut(e, ":")
		if !ok {
			return ThreatRec{}, fmt.Errorf("%w: threat entry %q", ErrViewFormat, e)
		}
		gg, err := ParseGUIDField(g)
		if err != nil {
			return ThreatRec{}, err
		}
		p, err := strconv.Atoi(pct)
		if err != nil || p < 0 || p > 100000 {
			return ThreatRec{}, fmt.Errorf("%w: threat share %q", ErrViewFormat, pct)
		}
		t.Top = append(t.Top, ThreatEntry{GUID: gg, Pct: p})
	}
	return t, nil
}

// beginRec is the body of a B line.
type beginRec struct {
	lead                       packet.GUID
	party, hostiles, threatCnt int
	truncated                  bool
}

func parseBegin(s string) (beginRec, error) {
	f := strings.Split(s, ",")
	if len(f) < 4 || len(f) > 5 {
		return beginRec{}, fmt.Errorf("%w: B line has %d fields, needs 4 or 5", ErrViewFormat, len(f))
	}
	lead, err := ParseGUIDField(f[0])
	if err != nil {
		return beginRec{}, err
	}
	var n [3]int
	for i := range n {
		v, err := strconv.Atoi(f[i+1])
		if err != nil || v < 0 || v > 1000 {
			return beginRec{}, fmt.Errorf("%w: B count %q", ErrViewFormat, f[i+1])
		}
		n[i] = v
	}
	b := beginRec{lead: lead, party: n[0], hostiles: n[1], threatCnt: n[2]}
	if len(f) == 5 {
		switch f[4] {
		case "0":
		case "1":
			b.truncated = true
		default:
			return beginRec{}, fmt.Errorf("%w: B truncated flag %q", ErrViewFormat, f[4])
		}
	}
	return b, nil
}

// ViewStats counts what the assembler saw, for the report and bot_view.
type ViewStats struct {
	Lines       uint64 `json:"lines"`       // TBM:VIEW lines read
	Frames      uint64 `json:"frames"`      // frames published
	Missed      uint64 `json:"missed"`      // seq numbers skipped between two published frames
	Stale       uint64 `json:"stale"`       // complete frames older than the newest one (out of order, dropped)
	Invalid     uint64 `json:"invalid"`     // frames refused: bad record, count or line total that did not match
	Incomplete  uint64 `json:"incomplete"`  // frames dropped without their E line (a newer frame came first)
	Orphans     uint64 `json:"orphans"`     // lines of a frame whose B never came
	Unsupported uint64 `json:"unsupported"` // lines of another grammar version
	Malformed   uint64 `json:"malformed"`   // TBM:VIEW lines that did not parse
	LastSeq     uint64 `json:"last_seq"`    // seq of the newest published frame
	Restarts    uint64 `json:"restarts"`    // times seq went back (a new binding)
	Truncated   uint64 `json:"truncated"`   // published frames the module marked as cut short
	LastInvalid string `json:"last_invalid,omitempty"`
}

// pending is a frame being assembled.
type pending struct {
	seq      uint64
	begin    *beginRec
	haveEnd  bool
	endLines int
	lines    int
	party    []PartyRec
	hostiles []HostileRec
	threats  []ThreatRec
	bad      error
}

// ViewAssembler builds frames out of TBM:VIEW lines, tolerating lost, late and reordered lines and frames. It is not safe for
// concurrent use; the ViewFeed serializes access.
type ViewAssembler struct {
	pend  map[uint64]*pending
	last  uint64 // seq of the newest published frame, 0 before the first
	stats ViewStats
}

// NewViewAssembler returns an empty assembler.
func NewViewAssembler() *ViewAssembler { return &ViewAssembler{pend: map[uint64]*pending{}} }

// Stats are the counters so far.
func (a *ViewAssembler) Stats() ViewStats { return a.stats }

// Reset forgets the frames held and the newest seq (a new binding starts at seq 1); the counters stay.
func (a *ViewAssembler) Reset() {
	a.pend = map[uint64]*pending{}
	a.last = 0
	a.stats.LastSeq = 0
}

// Feed reads one line and returns the frame it completed, if any. A line that is not a TBM:VIEW line is ignored. The frame's At
// is left for the caller.
func (a *ViewAssembler) Feed(line string) (*ViewFrame, bool) {
	vl, err := ParseViewLine(line)
	if err != nil {
		if !errors.Is(err, ErrNotView) {
			a.stats.Lines++
			a.stats.Malformed++
		}
		return nil, false
	}
	a.stats.Lines++
	if vl.Version != ViewVersion {
		a.stats.Unsupported++
		return nil, false
	}
	p := a.pend[vl.Seq]
	if p == nil {
		// A frame older than the newest published one cannot be published anymore: its lines are late (unless seq went back).
		if a.last > 0 && vl.Seq <= a.last && a.last-vl.Seq <= staleSeqWindow {
			if vl.Part == 'E' {
				a.stats.Stale++
			}
			return nil, false
		}
		if vl.Part != 'B' {
			// The B line was lost, or this is the tail of a frame dropped before. Keep the lines: B may still come (reordered).
			a.stats.Orphans++
		}
		p = a.open(vl.Seq)
	}
	p.lines++
	switch vl.Part {
	case 'B':
		b, err := parseBegin(vl.Body)
		if err != nil {
			p.bad = err
		} else {
			p.begin = &b
		}
	case 'P':
		p.party, p.bad = appendRecs(p.party, vl.Body, ParsePartyRec, p.bad)
	case 'H':
		p.hostiles, p.bad = appendRecs(p.hostiles, vl.Body, ParseHostileRec, p.bad)
	case 'T':
		p.threats, p.bad = appendRecs(p.threats, vl.Body, ParseThreatRec, p.bad)
	case 'E':
		n, err := strconv.Atoi(vl.Body)
		if err != nil || n < 2 {
			p.bad = fmt.Errorf("%w: E line count %q", ErrViewFormat, vl.Body)
		}
		p.haveEnd, p.endLines = true, n
	}
	return a.settle(p)
}

// open starts a pending frame, dropping the oldest ones when too many are open.
func (a *ViewAssembler) open(seq uint64) *pending {
	for len(a.pend) >= maxPendingFrames {
		oldest := uint64(0)
		for s := range a.pend {
			if oldest == 0 || s < oldest {
				oldest = s
			}
		}
		delete(a.pend, oldest)
		a.stats.Incomplete++
	}
	p := &pending{seq: seq}
	a.pend[seq] = p
	return p
}

func appendRecs[T any](dst []T, body string, parse func(string) (T, error), bad error) ([]T, error) {
	for _, rec := range strings.Split(body, ";") {
		if rec == "" {
			continue
		}
		r, err := parse(rec)
		if err != nil {
			if bad == nil {
				bad = err
			}
			continue
		}
		dst = append(dst, r)
	}
	return dst, bad
}

// settle publishes the frame when it holds B and E and the line count is right; it refuses a frame whose counts do not match.
func (a *ViewAssembler) settle(p *pending) (*ViewFrame, bool) {
	if p.begin == nil || !p.haveEnd {
		return nil, false
	}
	// Lines of a frame arrive in order, so E is last; a frame whose E came before some of its lines (reordered) is held until
	// the line total matches or a newer frame pushes it out.
	if p.lines < p.endLines && p.bad == nil {
		return nil, false
	}
	delete(a.pend, p.seq)
	switch {
	case p.bad != nil:
		a.invalid(p, p.bad.Error())
		return nil, false
	case p.lines != p.endLines:
		a.invalid(p, fmt.Sprintf("E says %d lines, the frame had %d", p.endLines, p.lines))
		return nil, false
	case len(p.party) != p.begin.party || len(p.hostiles) != p.begin.hostiles || len(p.threats) != p.begin.threatCnt:
		a.invalid(p, fmt.Sprintf("B says %d/%d/%d records, the frame had %d/%d/%d", p.begin.party, p.begin.hostiles, p.begin.threatCnt, len(p.party), len(p.hostiles), len(p.threats)))
		return nil, false
	}
	switch {
	case a.last > 0 && p.seq <= a.last && a.last-p.seq <= staleSeqWindow:
		a.stats.Stale++
		return nil, false
	case a.last > 0 && p.seq < a.last:
		a.stats.Restarts++
		a.stats.Incomplete += uint64(len(a.pend))
		a.pend = map[uint64]*pending{}
	case a.last > 0 && p.seq > a.last+1:
		a.stats.Missed += p.seq - a.last - 1
	}
	a.last = p.seq
	a.stats.Frames++
	a.stats.LastSeq = p.seq
	// Frames still open and older than this one will never be published: they are counted incomplete.
	a.dropOlder(p.seq)
	f := &ViewFrame{Version: ViewVersion, Seq: p.seq, Lead: p.begin.lead, Party: orEmpty(p.party), Hostiles: orEmpty(p.hostiles), Threats: orEmpty(p.threats), Lines: p.lines, Truncated: p.begin.truncated}
	if f.Truncated {
		a.stats.Truncated++
	}
	return f, true
}

func (a *ViewAssembler) dropOlder(seq uint64) {
	var gone []uint64
	for s := range a.pend {
		if s < seq {
			gone = append(gone, s)
		}
	}
	sort.Slice(gone, func(i, j int) bool { return gone[i] < gone[j] })
	for _, s := range gone {
		delete(a.pend, s)
		a.stats.Incomplete++
	}
}

func (a *ViewAssembler) invalid(p *pending, why string) {
	a.stats.Invalid++
	a.stats.LastInvalid = fmt.Sprintf("seq %d: %s", p.seq, why)
}

func orEmpty[T any](s []T) []T {
	if s == nil {
		return []T{}
	}
	return s
}
