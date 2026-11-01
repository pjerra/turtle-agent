package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/pjerra/turtle-agent/internal/dungeon"
	"github.com/pjerra/turtle-agent/internal/events"
	"github.com/pjerra/turtle-agent/internal/objects"
	"github.com/pjerra/turtle-agent/internal/packet"
	"github.com/pjerra/turtle-agent/internal/planexport"
	"github.com/pjerra/turtle-agent/internal/raidwalk"
	"github.com/pjerra/turtle-agent/internal/session"
	"github.com/pjerra/turtle-agent/internal/slots"
	"github.com/pjerra/turtle-agent/internal/tbm"
)

// The fakes below stand in for the slot agent, the dashboard, the clock and the lease, so every phase runs
// without a network or a process (spec 3.9 A3).

type fakeClock struct {
	mu sync.Mutex
	t  time.Time
}

func newFakeClock() *fakeClock {
	return &fakeClock{t: time.Date(2026, 10, 29, 20, 0, 0, 0, time.Local)}
}

func (c *fakeClock) now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.t
}

func (c *fakeClock) sleep(ctx context.Context, d time.Duration) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	c.mu.Lock()
	c.t = c.t.Add(d)
	c.mu.Unlock()
	return ctx.Err()
}

// queuedEvent is one TBM line the fake events RPC delivers; text is what a colour-stripped event carries.
type queuedEvent struct {
	raw  string
	text string
}

type timelineEntry struct {
	Text     string
	Targeted bool
}

type walkFakeAgent struct {
	runStarted bool   // the walk sent `run start` (wipe-fault tests gate on it)
	recoveryGo bool   // a `.go xyz` went out after run start (the recovery teleport)
	instMap    uint32 // the map the entered party stands on (0: Onyxia's 249)
	t          *testing.T
	clock      *fakeClock

	mu        sync.Mutex
	calls     []string
	gm        []string
	gmUnits   []int
	timeline  []timelineEntry
	party     []string
	nameSeq   int
	startArgs string

	entered  bool
	status   session.StatusInfo
	lines    []queuedEvent
	next     uint64
	viewN    int
	views    int // bot_view calls, for the fault hooks' one Tick per watch loop
	dead     map[string]bool
	targeted bool
	// pageSize serves up to this many queued event lines in one events page (0: one, as before).
	// Walk 18 r5: live TB sends several TBM:RUN status lines of the same incident in one poll.
	pageSize int

	hostilesFn func(viewN int) []tbm.HostileRec
	unitsFn    func(n int) []objects.Unit
	unitsN     int

	startReply *commanderReply
	startQueue []queuedEvent
	bindLayout string
	enterReply *commanderReply
	enterBusy  int // the first enterBusy enter orders answer ERR busy
	enterCalls int
	onGM       func(line string)
	// onResume runs when the walk sends `run resume` through the commander RPC (nil: ACK only).
	onResume func()

	// pos is where the commander's last .go put it; the quiet-check fakes place units around it.
	x, y, z float32

	// hireCap is the server's hire limit (0: raidwalk.HireCap, the 39 of onyxia-kill-plan C6).
	hireCap int

	// hireShort withholds hireShort[batch] bots on the batch's first reply (dps first, then healers,
	// then tanks). The first answer is partial; the retry hires the rest.
	hireShort   map[int]int
	hireBatches []hireBatchState
	// hireShortAlways makes every retry of a short batch answer partial again (test b).
	hireShortAlways bool
	// hireRetryReportsOrder makes a retry's reply count the requested order in its hired field while the
	// server hires only to the cap: the last seat fills but the reply says the full requested count.
	// The join wait must stay cap-aware (the capped-retry finding).
	hireRetryReportsOrder bool
	// hireReply answers batch hireReply[batch] with this line verbatim (test c); the server hires nothing.
	hireReply map[int]string

	// The fault injector's fakes (raid_walk_inject_test.go): the server's selection (target/gps), the per-bot
	ownGUID              packet.GUID // the commander's GUID when a test sets one
	ownPos               [3]float32  // the commander's own position (.go xyz updates it)
	pos                  map[string][3]float32
	selGUID              packet.GUID  // the selection the server has in force
	queuedSel            *packet.GUID // a target RPC the server applies after the next .gps (lagTargets)
	lagTargets           bool
	noSelect             bool // .gps always reports the commander (a server that never applies the select)
	statusTargetMismatch bool
	noPartyFrames        bool        // bot_view frames without a party (an injector with no bot positions)
	gmAt                 []time.Time // when each gm line was sent
	// Review-round-2 fault hooks: bot_view failure/omission (viewFn) and combat during the
	// quiet rest (combatAllFn); the dashboard fights read fails through fakeDash.fightsErr.
	viewFn      func(n int) (fail bool, omit string)
	combatAllFn func(n int) bool // frame n shows every hire in combat
	// viewHook runs inside the bot_view RPC (outside f.mu): a test's fake wall
	// time passing during the call, like the live server's RPC waits.
	viewHook func()
	// eventsHook runs when the events RPC serves a queued line (outside f.mu, before
	// the frame of the same poll): a test's scripted reaction to the walk reading a
	// line, so a status line and the frames it belongs with arrive in one poll.
	eventsHook  func(raw string)
	targetCalls []packet.GUID
	targetAt    []time.Time // when each target RPC arrived

	// The identity read-back (`.pinfo`): pinfoGUID names that unit instead of the server's selection,
	// pinfoLagTargets reports the selection one target RPC behind, prevSel is the selection before the newest
	pinfoGUID       packet.GUID
	pinfoName       string // names an untracked player instead of the selection's party record
	noPInfo         bool   // .pinfo never answers (the reply waits out its own deadline)
	pinfoLagTargets bool
	prevSel         packet.GUID
	gmFail          func(line string) error

	// The place phase's toolkit: ttFn answers one `.tt` command (nil: the test does not use the toolkit), ttCalls
	// records every command in order.
	ttFn    func(cmd string) (ttResult, error)
	ttCalls []string
	// log is every gm line, toolkit command and commander call in send order, as "gm ...", "tt ...", "cmdr ...".
	log []string

	// injectCtx is the injector's allow-list context at a gm send (nil: the walk's own gm line); gmCtx and gmCtxOK
	// hold it per line, so a test checks every line against the context it was really sent with.
	injectCtx func() (raidwalk.InjectCtx, bool)
	gmCtx     []raidwalk.InjectCtx
	gmCtxOK   []bool
}

func newFakeAgent(t *testing.T, clock *fakeClock) *walkFakeAgent {
	return &walkFakeAgent{t: t, clock: clock, dead: map[string]bool{},
		status: session.StatusInfo{State: session.StatusInWorld, Map: 1, Character: "Commander"}}
}

func (f *walkFakeAgent) Call(ctx context.Context, method string, params, out any, timeout time.Duration) error {
	raw, _ := json.Marshal(params)
	f.mu.Lock()
	f.calls = append(f.calls, method)
	f.mu.Unlock()
	var v any
	switch method {
	case "status":
		f.mu.Lock()
		v = f.status
		f.mu.Unlock()
	case "party":
		f.mu.Lock()
		members := make([]session.PartyMember, 0, len(f.party))
		for i, n := range f.party {
			members = append(members, session.PartyMember{Name: n, GUID: packet.GUID(1000 + i), Online: true})
		}
		f.mu.Unlock()
		v = session.PartyInfo{InGroup: len(members) > 0, Members: members}
	case "gm":
		var p struct {
			Text string `json:"text"`
		}
		_ = json.Unmarshal(raw, &p)
		f.mu.Lock()
		f.gm = append(f.gm, p.Text)
		f.log = append(f.log, "gm "+p.Text)
		f.gmAt = append(f.gmAt, f.clock.now())
		f.gmUnits = append(f.gmUnits, f.unitsN)
		f.timeline = append(f.timeline, timelineEntry{Text: p.Text, Targeted: f.targeted})
		if f.injectCtx != nil {
			c, ok := f.injectCtx()
			f.gmCtx = append(f.gmCtx, c)
			f.gmCtxOK = append(f.gmCtxOK, ok)
		}
		onGM := f.onGM
		fail := f.gmFail
		f.mu.Unlock()
		if fail != nil {
			if err := fail(p.Text); err != nil {
				return err
			}
		}
		f.gmSide(p.Text)
		if onGM != nil {
			onGM(p.Text)
		}
		v = struct{}{}
	case "events":
		v = f.eventsPage()
	case "bot_view":
		if f.viewHook != nil {
			f.viewHook()
		}
		v = f.viewPage()
	case "units":
		f.mu.Lock()
		f.unitsN++
		n := f.unitsN
		fn := f.unitsFn
		f.mu.Unlock()
		if fn == nil {
			v = []objects.Unit{}
		} else {
			v = fn(n)
		}
	case "target":
		var p struct {
			GUID string `json:"guid"`
		}
		_ = json.Unmarshal(raw, &p)
		g, err := packet.ParseGUID(p.GUID)
		if err != nil {
			f.t.Errorf("target %q: %v", p.GUID, err)
		}
		f.mu.Lock()
		if f.statusTargetMismatch {
			f.status.Target = 0
		} else {
			f.status.Target = g
		}
		f.prevSel = f.selGUID
		if f.lagTargets {
			queued := g
			f.queuedSel = &queued
		} else {
			f.selGUID = g
		}
		f.targetCalls = append(f.targetCalls, g)
		f.targetAt = append(f.targetAt, f.clock.now())
		f.mu.Unlock()
		v = objects.Unit{GUID: g}
	case "commander":
		var p commanderReq
		_ = json.Unmarshal(raw, &p)
		v = f.commander(&p)
	case "tt":
		var p ttParams
		_ = json.Unmarshal(raw, &p)
		f.mu.Lock()
		f.ttCalls = append(f.ttCalls, p.Cmd)
		f.log = append(f.log, "tt "+p.Cmd)
		fn := f.ttFn
		f.mu.Unlock()
		if fn == nil {
			f.t.Errorf("unexpected RPC tt (%q)", p.Cmd)
			return nil
		}
		out, err := fn(p.Cmd)
		if err != nil {
			return err
		}
		v = out
	default:
		f.t.Errorf("unexpected RPC %s", method)
	}
	if out != nil {
		b, _ := json.Marshal(v)
		if err := json.Unmarshal(b, out); err != nil {
			f.t.Errorf("%s result: %v", method, err)
		}
	}
	return nil
}

// gmSide is what the game does for a GM line.
func (f *walkFakeAgent) gmSide(line string) {
	switch {
	case strings.HasPrefix(line, ".bot hire raid ") && (len(f.hireShort) > 0 || len(f.hireReply) > 0):
		f.hireSideShort(line)
		return
	case strings.HasPrefix(line, ".bot hire raid "):
		hire := strings.TrimPrefix(line, ".bot hire raid ")
		if i := strings.Index(hire, " level="); i >= 0 {
			hire = hire[:i]
		}
		n := hireCount(hire)
		f.mu.Lock()
		cap := f.hireCap
		if cap == 0 {
			cap = raidwalk.HireCap
		}
		if room := cap - len(f.party); n > room {
			n = room // the server's hire cap: it hires what fits and no more
		}
		names := make([]string, 0, n)
		for range n {
			f.nameSeq++
			names = append(names, fmt.Sprintf("Bot%02d", f.nameSeq))
		}
		f.party = append(f.party, names...)
		f.mu.Unlock()
		f.push("TBM:HIRE_RAID|ok|"+strconv.Itoa(n)+"|"+strconv.Itoa(n)+"|0|0|0|3|0|", "")
	case strings.HasPrefix(line, ".bot gear raid "):
		f.push("TBM:GEAR|ok|39|39|3|60|-|-|-|0|-|0", "")
	case strings.HasPrefix(line, ".go xyz "):
		f.mu.Lock()
		if f.runStarted {
			f.recoveryGo = true
		}
		f.mu.Unlock()
		fields := strings.Fields(line)
		if len(fields) == 6 {
			x, _ := strconv.ParseFloat(fields[2], 32)
			y, _ := strconv.ParseFloat(fields[3], 32)
			z, _ := strconv.ParseFloat(fields[4], 32)
			m, err := strconv.Atoi(fields[5])
			f.mu.Lock()
			f.x, f.y, f.z = float32(x), float32(y), float32(z)
			f.ownPos = [3]float32{f.x, f.y, f.z}
			f.status.X, f.status.Y, f.status.Z = f.x, f.y, f.z
			if err == nil {
				f.status.Map = uint32(m)
			}
			f.mu.Unlock()
		}
	case line == ".gps":
		f.gpsSide()
	case line == ".pinfo":
		f.pinfoSide()
	case line == ".die":
		f.dieSide()
	case strings.HasPrefix(line, ".appear "):
		// The GM appears in the named player's copy: the status follows (the place phase's waitMap).
		f.mu.Lock()
		m := f.instMap
		if m == 0 {
			m = 249
		}
		f.status.Map = m
		f.mu.Unlock()
	case strings.HasPrefix(line, ".summon "):
		name := strings.TrimPrefix(line, ".summon ")
		f.mu.Lock()
		if f.pos == nil {
			f.pos = map[string][3]float32{}
		}
		f.pos[name] = [3]float32{f.x, f.y, f.z}
		f.mu.Unlock()
	case strings.HasPrefix(line, ".revive "):
		// The server's revive brings the hire back: later frames show it alive, so a recovered walk
		// does not re-fire the wipe it just revived.
		name := strings.TrimPrefix(line, ".revive ")
		f.mu.Lock()
		delete(f.dead, name)
		f.mu.Unlock()
	}
}

// gpsSide pushes the `.gps` reply for the selection the server has in force (P0: the reply lists the SELECTED unit,
// and a target RPC takes effect one command later when lagTargets is set).
func (f *walkFakeAgent) gpsSide() {
	f.mu.Lock()
	sel, own := f.selGUID, f.ownGUID
	pos := f.ownPos
	mapID := f.status.Map
	if !f.noSelect && sel != 0 && sel != own {
		for i, n := range f.party {
			if packet.GUID(1000+i) == sel {
				if p, ok := f.pos[n]; ok {
					pos = p
				}
			}
		}
	}
	if f.queuedSel != nil {
		f.selGUID = *f.queuedSel
		f.queuedSel = nil
	}
	f.mu.Unlock()
	// The reply is the next events on the wire (the queued run lines are older), so it goes in front.
	reply := []queuedEvent{
		{raw: fmt.Sprintf("X: %.6f Y: %.6f Z: %.6f Orientation: 0.000000", pos[0], pos[1], pos[2])},
		{raw: fmt.Sprintf("GroundZ: 0.000000 FloorZ: 0.000000 Have height data (Map: %d VMap: 1)", mapID)},
	}
	f.mu.Lock()
	f.lines = append(reply, f.lines...)
	f.mu.Unlock()
}

// pinfoSide pushes a `.pinfo` (no name) reply for the selected player, in the core's
// LANG_PINFO_ACCOUNT shape: the first line names the character with a player link and its
// guid counter. pinfoGUID / pinfoName model a read that names another unit (or an untracked
// player), pinfoLagTargets a read one target RPC behind, noPInfo one that never answers.
func (f *walkFakeAgent) pinfoSide() {
	f.mu.Lock()
	if f.noPInfo {
		f.mu.Unlock()
		return
	}
	sel := f.selGUID
	if f.noSelect { // the server never applied a selection: the previous one (the commander) still stands
		sel = f.ownGUID
	}
	if f.pinfoLagTargets {
		sel = f.prevSel
	}
	if f.pinfoGUID != 0 {
		sel = f.pinfoGUID
	}
	name := f.pinfoName
	if name == "" && sel != 0 && sel == f.ownGUID {
		name = f.status.Character
	} else if name == "" && sel != 0 {
		for i, n := range f.party {
			if packet.GUID(1000+i) == sel {
				name = n
			}
		}
	}
	f.mu.Unlock()
	line := "There is no such player."
	if name != "" {
		line = fmt.Sprintf("Human Warrior |cffffffff|Hplayer:%s|h[%s]|h|r (guid: %d) Account: acct (id: 1) GMLevel: 3",
			name, name, uint64(uint32(sel)))
	}
	// The reply is the next events on the wire (the queued run lines are older), so it goes in front.
	f.mu.Lock()
	f.lines = append([]queuedEvent{{raw: line}}, f.lines...)
	f.mu.Unlock()
}

// dieSide kills the unit the server has selected, as `.die` does (P0: the selected unit only).
func (f *walkFakeAgent) dieSide() {
	f.mu.Lock()
	defer f.mu.Unlock()
	for i, n := range f.party {
		if packet.GUID(1000+i) == f.selGUID {
			f.dead[n] = true
			return
		}
	}
}

// hireCount reads the bot count out of a hire grammar.
func hireCount(hire string) int {
	n := 0
	for _, tok := range strings.Fields(hire) {
		if strings.HasPrefix(tok, "t") && len(tok) == 2 {
			continue // the tier word t1/t2/t3
		}
		i := 0
		for i < len(tok) && tok[i] >= '0' && tok[i] <= '9' {
			i++
		}
		if i > 0 {
			c, _ := strconv.Atoi(tok[:i])
			n += c
		}
	}
	return n
}

func (f *walkFakeAgent) push(raw, text string) {
	f.mu.Lock()
	f.lines = append(f.lines, queuedEvent{raw: raw, text: text})
	f.mu.Unlock()
}

// hireSideShort is the game side for a hire when hireShort names short batches: the batch index counts
// the walk's new batches (retries excluded: a retry asks for fewer bots than its batch's shortfall held
// back), the first reply of a short batch withholds hireShort[batch] dps as a partial, and the retry
// hires the missing dps ok.
func (f *walkFakeAgent) hireSideShort(line string) {
	hire := strings.TrimPrefix(line, ".bot hire raid ")
	if i := strings.Index(hire, " level="); i >= 0 {
		hire = hire[:i]
	}
	wantT, wantH, wantD := 0, 0, 0
	for _, tok := range strings.Fields(hire) {
		if strings.HasPrefix(tok, "t") && len(tok) == 2 {
			continue // the tier word t1/t2/t3
		}
		i := 0
		for i < len(tok) && tok[i] >= '0' && tok[i] <= '9' {
			i++
		}
		if i == 0 {
			continue
		}
		c, _ := strconv.Atoi(tok[:i])
		switch {
		case strings.HasSuffix(tok, "t"):
			wantT += c
		case strings.HasSuffix(tok, "h"):
			wantH += c
		case strings.HasSuffix(tok, "d"):
			wantD += c
		}
	}
	f.mu.Lock()
	// The gm line is already appended (Call records it before gmSide). A retry asks for what its batch's
	// partial held back (equal or a sub-grammar of it); a new batch does not fit inside the open miss.
	batchIdx := len(f.hireBatches)
	if batchIdx > 0 {
		last := &f.hireBatches[batchIdx-1]
		if last.missed && wantT <= last.missT && wantH <= last.missH && wantD <= last.missD {
			batchIdx--
		}
	}
	for len(f.hireBatches) <= batchIdx {
		f.hireBatches = append(f.hireBatches, hireBatchState{})
	}
	b := &f.hireBatches[batchIdx]
	b.attempt++
	short := f.hireShort[batchIdx]
	cap := f.hireCap
	if cap == 0 {
		cap = raidwalk.HireCap
	}
	classN := 0
	if wantT+wantH+wantD == 0 {
		classN = hireCount(hire)
	}
	status, reason := "ok", ""
	gotT, gotH, gotD := wantT, wantH, wantD
	if _, ok := f.hireReply[batchIdx]; ok && b.attempt == 1 {
		f.mu.Unlock()
		f.push(f.hireReply[batchIdx], "")
		return
	}
	if short > 0 && (b.attempt == 1 || f.hireShortAlways) {
		left := short
		for _, grp := range []*int{&gotD, &gotH, &gotT} {
			if left <= 0 {
				break
			}
			if *grp < left {
				left -= *grp
				*grp = 0
			} else {
				*grp -= left
				left = 0
			}
		}
		status = "partial"
		reason = "No mercenary of that kind is available right now. Try again shortly."
	}
	if status == "partial" {
		b.missed = true
		b.missT, b.missH, b.missD = wantT-gotT, wantH-gotH, wantD-gotD
	} else if batchIdx == len(f.hireBatches)-1 && b.attempt > 1 {
		// The retry refilled the batch: close the miss so a later equal-sized batch opens fresh.
		// (A retry asks for exactly the miss, so it clears it.)
		b.missed = false
	}
	n := gotT + gotH + gotD + classN
	if room := cap - len(f.party); n > room {
		n = room // the server's hire cap: it hires what fits and no more
	}
	names := make([]string, 0, n)
	for range n {
		f.nameSeq++
		names = append(names, fmt.Sprintf("Bot%02d", f.nameSeq))
	}
	f.party = append(f.party, names...)
	reported := n
	if f.hireRetryReportsOrder && batchIdx == len(f.hireBatches)-1 && b.attempt > 1 && status != "partial" {
		reported = gotT + gotH + gotD + classN // the capped server counts the requested order, not the seats
	}
	planned := wantT + wantH + wantD
	f.mu.Unlock()
	f.push("TBM:HIRE_RAID|"+status+"|"+strconv.Itoa(reported)+"|"+strconv.Itoa(planned)+"|"+
		strconv.Itoa(gotT)+"|"+strconv.Itoa(gotH)+"|"+strconv.Itoa(gotD)+"|3|0|"+reason, "")
}

// hireBatchState tracks one fake hire batch: its attempts and the shortfall its last partial held back.
type hireBatchState struct {
	attempt             int
	missed              bool
	missT, missH, missD int
}

func (f *walkFakeAgent) eventsPage() events.Page {
	f.mu.Lock()
	if len(f.lines) == 0 {
		next := f.next
		f.mu.Unlock()
		return events.Page{Next: next}
	}
	n := 1
	if f.pageSize > n {
		n = f.pageSize
	}
	if n > len(f.lines) {
		n = len(f.lines)
	}
	batch := append([]queuedEvent{}, f.lines[:n]...)
	f.lines = f.lines[n:]
	evs := make([]events.Event, 0, n)
	for _, q := range batch {
		f.next++
		ev := events.Event{Seq: f.next, Kind: events.KindSystem, Text: q.raw, Data: map[string]any{"raw": q.raw}}
		if q.text != "" {
			ev.Text = q.text
		}
		evs = append(evs, ev)
	}
	next := f.next
	hook := f.eventsHook
	f.mu.Unlock()
	if hook != nil {
		for _, q := range batch {
			hook(q.raw)
		}
	}
	return events.Page{Events: evs, Next: next}
}

type viewReply struct {
	Have  bool           `json:"have"`
	N     uint64         `json:"n"`
	Frame *tbm.ViewFrame `json:"frame"`
}

func (f *walkFakeAgent) viewPage() any {
	f.mu.Lock()
	f.viewN++
	n := f.viewN
	f.views++
	m := uint32(1)
	viewHook := f.viewFn
	combatAll := f.combatAllFn
	if f.entered {
		m = 249
		if f.instMap != 0 {
			m = f.instMap
		}
	}
	party := append([]string{}, f.party...)
	dead := map[string]bool{}
	for k, v := range f.dead {
		dead[k] = v
	}
	pos := map[string][3]float32{}
	for k, v := range f.pos {
		pos[k] = v
	}
	noParty := f.noPartyFrames
	fn := f.hostilesFn
	f.mu.Unlock()
	if viewHook != nil {
		if fail, _ := viewHook(n); fail {
			return viewReply{Have: false, N: uint64(n)}
		}
	}
	frame := &tbm.ViewFrame{Seq: uint64(n)}
	for i, name := range party {
		state := "n"
		if dead[name] {
			state = "d"
		}
		if combatAll != nil && combatAll(n) {
			state = "c"
		}
		if viewHook != nil {
			if _, omit := viewHook(n); omit == name {
				continue
			}
		}
		p := pos[name]
		frame.Party = append(frame.Party, tbm.PartyRec{GUID: packet.GUID(1000 + i), Name: name, HP: 1, MaxHP: 1, Map: m, Dead: dead[name], State: state,
			X: p[0], Y: p[1], Z: p[2]})
	}
	if noParty {
		frame.Party = nil
	}
	if fn != nil {
		frame.Hostiles = fn(n)
	}
	return viewReply{Have: true, N: uint64(n), Frame: frame}
}
func (f *walkFakeAgent) commander(p *commanderReq) commanderReply {
	f.mu.Lock()
	f.log = append(f.log, strings.TrimSpace("cmdr "+p.Op+" "+p.Verb+" "+strings.Join(p.Args, " ")))
	f.mu.Unlock()
	ack := func(intent string) commanderReply {
		f.mu.Lock()
		count := len(f.party)
		f.mu.Unlock()
		return commanderReply{Sent: ".bot " + intent, Reply: tbm.Reply{Kind: tbm.KindAck, OK: true,
			Ack:   &tbm.Ack{Intent: intent, Scope: "party", Count: count, Executor: "-"},
			Line:  "TBM:ACTION_ACK|" + intent + "|party|" + strconv.Itoa(count) + "|-",
			Lines: []string{"TBM:ACTION_ACK|" + intent + "|party|" + strconv.Itoa(count) + "|-"}}}
	}
	switch p.Op {
	case "bind":
		layout := f.bindLayout
		if layout == "" {
			f.mu.Lock()
			mt := "Bot01"
			if len(f.party) > 0 {
				mt = f.party[0]
			}
			n := len(f.party)
			f.mu.Unlock()
			layout = "TBM:RAID_LAYOUT|" + strconv.Itoa(n) + "|" + mt + "|" + mt + "|Bot02|"
		}
		f.push(layout, "")
		return ack("commander bind")
	case "view":
		return ack("commander view")
	case "cmd":
		if p.Verb == "enter" {
			f.mu.Lock()
			f.enterCalls++
			busy := f.enterCalls <= f.enterBusy
			f.mu.Unlock()
			if busy {
				return commanderReply{Reply: tbm.Reply{Kind: "err", Err: &tbm.Err{Intent: "enter", Code: "busy",
					Message: "That bot is busy with an earlier order (or a pending summon)."}}}
			}
		}
		if p.Verb == "enter" && f.enterReply != nil {
			return *f.enterReply
		}
		if p.Verb == "enter" {
			f.mu.Lock()
			f.entered = true
			f.mu.Unlock()
		}
		return ack("cmd " + p.Verb)
	case "run":
		if p.Verb == "start" {
			f.mu.Lock()
			f.runStarted = true
			f.startArgs = strings.Join(p.Args, "|")
			q := f.startQueue
			f.startQueue = nil
			f.mu.Unlock()
			for _, e := range q {
				f.push(e.raw, e.text)
			}
			if f.startReply != nil {
				return *f.startReply
			}
			rep := ack("run start")
			run := "TBM:RUN|" + p.Args[0] + "|running|1/29|onyxia|move|walk|walking|until boss (step 29)"
			rep.Reply.Lines = append([]string{run}, rep.Reply.Lines...)
			return rep
		}
		if p.Verb == "status" {
			rep := ack("commander status")
			f.mu.Lock()
			names := strings.Join(f.party, ", ")
			f.mu.Unlock()
			rep.Reply.Lines = append([]string{fmt.Sprintf("You command Bot01's group: %s.", names)}, rep.Reply.Lines...)
			return rep
		}
		if p.Verb == "resume" {
			f.mu.Lock()
			hook := f.onResume
			f.mu.Unlock()
			if hook != nil {
				hook()
			}
		}
		return ack("run " + p.Verb)
	}
	return ack(p.Op)
}

func (f *walkFakeAgent) gmLines() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string{}, f.gm...)
}

// gmContexts is the injector's allow-list context per gm line (gmCtxOK false: none was available).
func (f *walkFakeAgent) gmContexts() ([]raidwalk.InjectCtx, []bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]raidwalk.InjectCtx{}, f.gmCtx...), append([]bool{}, f.gmCtxOK...)
}

// fakeDash is the dashboard client.
type fakeDash struct {
	mu        sync.Mutex
	fights    []walkFightRow
	fightKeys []string
	failFight int // the first n Fight calls fail
	fightsErr error
	// commanders counts the Commanders reads: a recovery's returned watch loop polls it again.
	commanders int
}

func (d *fakeDash) Login(ctx context.Context) error { return nil }

func (d *fakeDash) Commanders(ctx context.Context) (json.RawMessage, error) {
	d.mu.Lock()
	d.commanders++
	d.mu.Unlock()
	return json.RawMessage(`{"commanders":[]}`), nil
}

// commandersCalls is how many dashboard Commanders reads the walk made.
func (d *fakeDash) commandersCalls() int {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.commanders
}

func (d *fakeDash) Fights(ctx context.Context, commander string) (json.RawMessage, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.fightsErr != nil {
		return nil, d.fightsErr
	}
	raw, err := json.Marshal(d.fights)
	return raw, err
}

func (d *fakeDash) Fight(ctx context.Context, key string) (json.RawMessage, error) {
	d.mu.Lock()
	d.fightKeys = append(d.fightKeys, key)
	fail := d.failFight > 0
	if fail {
		d.failFight--
	}
	d.mu.Unlock()
	if fail {
		return nil, errors.New("dashboard timeout")
	}
	return json.RawMessage(`{"fight":{"id":"4-5"}}`), nil
}

// envRecord is what the fake environment saw.
type envRecord struct {
	acquires  int
	releases  int
	setupArgs []string
	teardown  []string
	started   int
}

// fixture is one raid walk with fakes.
type fixture struct {
	t      *testing.T
	clock  *fakeClock
	agent  *walkFakeAgent
	dash   *fakeDash
	rec    *envRecord
	env    walkEnv
	deps   walkDeps
	opts   raidWalkOptions
	out    string
	stdout bytes.Buffer
	stderr bytes.Buffer
}

func newFixture(t *testing.T) *fixture {
	t.Helper()
	t.Setenv("TBDASH_URL", "")
	t.Setenv("TBDASH_PASS_FILE", "")
	t.Setenv("TBDASH_USER", "")
	clock := newFakeClock()
	agent := newFakeAgent(t, clock)
	rec := &envRecord{}
	out := filepath.Join(t.TempDir(), "out")
	f := &fixture{t: t, clock: clock, agent: agent, dash: &fakeDash{}, rec: rec, out: out}
	f.opts = raidWalkOptions{
		plan: "onyxias_lair", server: "m910q", holder: "raid-walk-onyxias_lair", dir: "../../dungeons",
		until: "boss", speed: 1.0, enter: "auto", maxRunMinutes: 90, pausedWait: 10 * time.Minute,
		resetWait: 5 * time.Minute, wipeResume: 1, wipeRest: 10 * time.Minute, out: out,
	}
	f.env = walkEnv{
		now:   clock.now,
		sleep: clock.sleep,
		startAgent: func(m teardownSlotManifest) (int, error) {
			rec.started++
			return 4242, nil
		},
		acquire: func(server, slot string, any bool, holder string, ttl time.Duration) (slots.Acquired, error) {
			rec.acquires++
			if slot == "" {
				slot = "2"
			}
			now := clock.now()
			return slots.Acquired{Lease: slots.Lease{Server: server, Slot: slot, Holder: holder, LeaseID: "lease-1", Acquired: now, Expires: now.Add(ttl)},
				Slot: slots.Slot{ID: slot}}, nil
		},
		renew: func(server, slot, holder, leaseID string, ttl time.Duration) (slots.Lease, error) {
			return slots.Lease{}, nil
		},
		release: func(server, slot, holder, leaseID string, force bool) (slots.Lease, error) {
			rec.releases++
			return slots.Lease{}, nil
		},
		setup: func(args []string, stdout, stderr io.Writer) int {
			rec.setupArgs = append([]string{}, args...)
			dir := argValue(args, "--dir")
			if err := os.MkdirAll(dir, 0o700); err != nil {
				f.t.Fatal(err)
			}
			m := teardownSlotManifest{Server: args[0], Slot: args[1], Holder: argValue(args, "--holder"), Character: "Commander",
				Port: 1234, Dir: dir, Binary: filepath.Join(dir, "turtle-agent"), Settings: filepath.Join(dir, "settings.json"),
				PIDFile: filepath.Join(dir, "agent.pid"), TokenFile: filepath.Join(dir, "rpc-1234.token")}
			raw, _ := json.Marshal(m)
			if err := os.WriteFile(filepath.Join(dir, "slot.json"), raw, 0o600); err != nil {
				f.t.Fatal(err)
			}
			fmt.Fprintf(stdout, "slot %s/%s ready in %s\n", args[0], args[1], dir)
			return 0
		},
		teardown: func(args []string, stdout, stderr io.Writer) int {
			rec.teardown = append([]string{}, args...)
			fmt.Fprint(stdout, `{"server":"m910q","slot":"2","steps":[],"failed":0}`)
			return 0
		},
	}
	f.deps = walkDeps{
		openAgent: func(dir string) (walkAgent, teardownSlotManifest, error) {
			m, err := readWalkManifest(dir)
			return agent, m, err
		},
		openDash: func(url, user, passFile string) (walkDash, error) { return f.dash, nil },
	}
	return f
}

func argValue(args []string, flag string) string {
	for i := 0; i < len(args)-1; i++ {
		if args[i] == flag {
			return args[i+1]
		}
	}
	return ""
}

// run performs the walk.
func (f *fixture) run(ctx context.Context) int {
	return raidWalkRun(ctx, f.opts, f.env, f.deps, &f.stdout, &f.stderr)
}

// summary reads <out>/summary.json.
func (f *fixture) summary() raidwalk.Summary {
	f.t.Helper()
	raw, err := os.ReadFile(filepath.Join(f.out, "summary.json"))
	if err != nil {
		f.t.Fatalf("summary.json: %v (stdout %q, stderr %q)", err, f.stdout.String(), f.stderr.String())
	}
	var s raidwalk.Summary
	if err := json.Unmarshal(raw, &s); err != nil {
		f.t.Fatal(err)
	}
	return s
}

// defaultRun queues the run lines of a walk that stops before Onyxia (2/29 .. 28/29, then the until done); they
// arrive once the run start has been answered, as on the server.
func (f *fixture) defaultRun(plan string) {
	var q []queuedEvent
	for step := 2; step <= 28; step++ {
		q = append(q, queuedEvent{raw: fmt.Sprintf("TBM:RUN|%s|running|%d/29|onyxia|move|walk|walking", plan, step)})
	}
	q = append(q, queuedEvent{raw: fmt.Sprintf("TBM:RUN|%s|done|28/29|onyxia|move|walk|stopped before step 29 (until boss): the party stays where it stands", plan)})
	f.agent.mu.Lock()
	f.agent.startQueue = q
	f.agent.mu.Unlock()
}

// startAt is a start reply whose first RUN line is at step with the until label.
func startAt(plan string, step int) *commanderReply {
	return &commanderReply{Reply: tbm.Reply{Kind: tbm.KindAck, OK: true,
		Ack:  &tbm.Ack{Intent: "run start", Scope: "party", Count: 1, Executor: "-"},
		Line: "TBM:ACTION_ACK|run start|party|1|-",
		Lines: []string{
			fmt.Sprintf("TBM:RUN|%s|running|%d/29|onyxia|move|walk|walking|until boss (step 29)", plan, step),
			"TBM:ACTION_ACK|run start|party|1|-",
		}}}
}

func TestRaidWalkRefusesSlot3(t *testing.T) {
	var stdout, stderr bytes.Buffer
	code := raidWalkCmd(context.Background(), []string{"--slot", "3", "onyxias_lair"}, &stdout, &stderr)
	if code != exitSlotRefused {
		t.Fatalf("--slot 3: exit %d, want %d (stderr %q)", code, exitSlotRefused, stderr.String())
	}
	if !strings.Contains(stderr.String(), "never uses slot 3") {
		t.Fatalf("the refusal does not name the rule: %q", stderr.String())
	}
}

func TestRaidWalkDryRunPrintsThePhasePlan(t *testing.T) {
	f := newFixture(t)
	f.opts.dryRun = true
	if code := f.run(context.Background()); code != exitOK {
		t.Fatalf("dry run exit %d: %s", code, f.stderr.String())
	}
	if f.rec.acquires != 0 || f.rec.setupArgs != nil {
		t.Fatalf("the dry run touched the lease or the slot: %+v", f.rec)
	}
	if _, err := os.Stat(f.out); !os.IsNotExist(err) {
		t.Fatalf("the dry run created the out dir")
	}
	got := f.stdout.String()
	want := `raid-walk onyxias_lair: dry run, nothing is sent
plan ../../dungeons/onyxias_lair.json, map 249, 1 legs, 7 steps, raid 40
roster 40 bots: mt 1 warrior:tank, ot 3t, rest 10h 26d, level 60, tier 2 (table)
until boss; from -; enter auto; server m910q slot any holder raid-walk-onyxias_lair
out ` + f.out + `
phases:
  1 validate: plan, roster, until, 7 steps
  2 lease: m910q any --holder raid-walk-onyxias_lair --ttl 120m
  3 slot setup: slot setup m910q <slot> --holder raid-walk-onyxias_lair --dir ` + f.out + `/slot --binary <bin> --dungeons-from ../../dungeons
  4 login: ` + f.out + `/slot/turtle-agent login --config ` + f.out + `/slot/settings.json --pid-file ` + f.out + `/slot/agent.pid <commander>
  5 gm: .go xyz -4768.61 -3752.11 53.4317 1
  6 gm: .bot hire raid 1 warrior:tank level=60 t2
  6 gm: .bot hire raid 3t level=60 t2
  6 gm: .bot hire raid 10h 26d level=60 t2
  6 party: before and after every batch (the new names are the mt, ot and rest)
  6 hire cap: 39 (the server hires 39 of the 40 asked; the summary notes it)
  6 gm: .bot gear raid <commander> tier 2
  7 commander: bind <hire> raid
  7 commander: view on
  8 commander: cmd enter near
  9 commander: run start onyxias_lair until boss
 10 watch: events 5s, bot_view 2s, dashboard off (no --dash-url/--dash-pass-file), ceiling 90m, paused wait 10m0s, auto-resume 0, wipe-resume 1, wipe rest 10m0s
 11 hold: 0m
 12 commander: status; teardown --json ` + f.out + `/slot
 13 summary: ` + f.out + `/summary.json, ` + f.out + `/summary.txt
`
	if got != want {
		t.Fatalf("dry-run plan:\n%s\nwant:\n%s", got, want)
	}
}

func TestRaidWalkHappyPathOnyxia(t *testing.T) {
	f := newFixture(t)
	f.defaultRun("onyxias_lair")
	code := f.run(context.Background())
	if code != exitOK {
		t.Fatalf("exit %d: %s\n%s", code, f.stderr.String(), f.stdout.String())
	}
	sum := f.summary()
	if sum.EndReason != "until" || sum.StepsReached != 28 || sum.StepsTotal != 29 {
		t.Fatalf("summary end %q steps %d/%d", sum.EndReason, sum.StepsReached, sum.StepsTotal)
	}
	if sum.ExitCode != exitOK || sum.Teardown.Failed {
		t.Fatalf("summary exit %d teardown %+v", sum.ExitCode, sum.Teardown)
	}
	if len(f.rec.teardown) != 2 || f.rec.teardown[0] != "--json" || f.rec.teardown[1] != filepath.Join(f.out, "slot") {
		t.Fatalf("teardown args %q", f.rec.teardown)
	}
	gm := f.agent.gmLines()
	if len(gm) < 5 || !strings.HasPrefix(gm[0], ".go xyz ") || !strings.HasPrefix(gm[1], ".bot hire raid 1 warrior:tank") ||
		!strings.HasPrefix(gm[4], ".bot gear raid Commander tier 2") {
		t.Fatalf("GM lines out of order: %q", gm)
	}
	hires := map[string]bool{}
	f.agent.mu.Lock()
	for _, n := range f.agent.party {
		hires[n] = true
	}
	f.agent.mu.Unlock()
	for _, line := range gm {
		if !raidwalk.AllowedLine(line, hires, 249) {
			t.Fatalf("a GM line fails the allow-list: %q", line)
		}
	}
	if f.agent.startArgs != "onyxias_lair|until|boss" {
		t.Fatalf("run start args %q", f.agent.startArgs)
	}
	if !strings.Contains(f.stdout.String(), "raid-walk onyxias_lair: until; steps 28/29") {
		t.Fatalf("one-line result: %q", f.stdout.String())
	}
	// The tier of the committed table, in the summary and in every dump's meta (amendment G1).
	if sum.Roster.Tier != 2 || sum.Roster.TierSource != raidwalk.TierSourceTable || sum.Roster.TierComparison {
		t.Fatalf("summary roster tier %d %q comparison %v, want 2 table false",
			sum.Roster.Tier, sum.Roster.TierSource, sum.Roster.TierComparison)
	}
	if !hasNote(sum, "hire cap 39: asked for 40 bots, the server hired 39") {
		t.Fatalf("summary notes %q, want the hire cap note", sum.Notes)
	}
	raw, err := os.ReadFile(filepath.Join(f.out, "frames.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	var frame struct {
		Tier int `json:"tier"`
	}
	if err := json.Unmarshal(bytes.SplitN(raw, []byte("\n"), 2)[0], &frame); err != nil || frame.Tier != 2 {
		t.Fatalf("frames.jsonl tier = %d (%v), want 2", frame.Tier, err)
	}
	text, err := os.ReadFile(filepath.Join(f.out, "summary.txt"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(text), "tier 2 (table)\n") {
		t.Fatalf("summary.txt has no tier line: %q", text)
	}
}

// hasNote says whether the summary carries a note holding text.
func hasNote(s raidwalk.Summary, text string) bool {
	for _, n := range s.Notes {
		if strings.Contains(n, text) {
			return true
		}
	}
	return false
}

// TestRaidWalkReadsDeployedOnyxiaMigration is the smoke of the deployed migrations (B1's lenient reader): the
// ai_dungeon_boss block beside the rows is skipped with a note, and the rows are the walk's (1 leg, 29 steps).
func TestRaidWalkReadsDeployedOnyxiaMigration(t *testing.T) {
	for _, c := range []struct {
		file string
		z26  float32
	}{
		{"onyxias_lair-20261029120000.sql", -74.62397},
		{"onyxias_lair-20261029130000.sql", -66.7},
	} {
		path := filepath.Join("../../internal/planexport/testdata", c.file)
		rows, notes, err := readRaidMigration(path, "onyxias_lair")
		if err != nil {
			t.Fatalf("%s: %v", c.file, err)
		}
		if len(rows.Legs) != 1 || len(rows.Steps) != 29 {
			t.Fatalf("%s: %d legs, %d steps, want 1 and 29", c.file, len(rows.Legs), len(rows.Steps))
		}
		if z := rows.Steps[25].Z; z != c.z26 {
			t.Fatalf("%s: step 26 z %v, want %v", c.file, z, c.z26)
		}
		if len(notes) != 1 || !strings.HasPrefix(notes[0], "skipped ai_dungeon_boss:") {
			t.Fatalf("%s: notes %q, want the skipped boss block", c.file, notes)
		}
		if _, _, err := readRaidMigration(path, "molten_core"); err == nil || !strings.Contains(err.Error(), "no plan") {
			t.Fatalf("%s as molten_core: %v, want the no-plan refusal", c.file, err)
		}
	}
}

// TestRaidWalkUsesTheDeployedMigration runs the walk with --sql and carries the reader's notes into the summary.
func TestRaidWalkUsesTheDeployedMigration(t *testing.T) {
	f := newFixture(t)
	f.opts.sql = "../../internal/planexport/testdata/onyxias_lair-20261029130000.sql"
	f.defaultRun("onyxias_lair")
	if code := f.run(context.Background()); code != exitOK {
		t.Fatalf("exit %d: %s", code, f.stderr.String())
	}
	sum := f.summary()
	if sum.StepsTotal != 29 {
		t.Fatalf("steps total %d, want 29 from the migration", sum.StepsTotal)
	}
	if !hasNote(sum, "skipped ai_dungeon_boss") {
		t.Fatalf("summary notes %q, want the skipped-table note", sum.Notes)
	}
	if hasNote(sum, "step points from the plan JSON") {
		t.Fatalf("a walk with --sql used the plan JSON: %q", sum.Notes)
	}
}

// TestRaidWalkReadsTbPatrolsFile reads a real D1 file (numeric spawn_guids, period, waits): the fallback reader of
// the patrols format cannot, internal/patrols can.
func TestRaidWalkReadsTbPatrolsFile(t *testing.T) {
	doc := `{"format":"tb-patrols","version":1,"map":249,"source":"record","created":"2026-10-29T10:00:00Z",
 "area":{"x":-100,"y":-150,"r":120},"minutes":15,"sample_ms":1000,
 "patrols":[{"id":"52050","guids":["0xF130002F61000ABC"],"spawn_guids":[52050],"entries":[12129],"name":"Onyxian Warder",
   "path":[{"x":49.8,"y":-114.6,"z":-24.3},{"x":30,"y":-114.6,"z":-24.3}],"waits_ms":[0,2000],"period_s":33.4,
   "period_estimate":false,"cycles_seen":3,"phase_epoch_ms":1793223608123,"complete":true,"samples":900,
   "source":"record m910q 2026-10-29"}],
 "static":[],"wanderers":[],"stopped":""}`
	path := filepath.Join(t.TempDir(), "patrols-249.json")
	if err := os.WriteFile(path, []byte(doc), 0o600); err != nil {
		t.Fatal(err)
	}
	patrols, guids, mapID, err := readPatrolsFile(path)
	if err != nil {
		t.Fatalf("a real tb-patrols file: %v", err)
	}
	if mapID != 249 || len(patrols) != 1 || len(patrols[0].Path) != 2 || patrols[0].ID != "52050" {
		t.Fatalf("patrols %+v map %d", patrols, mapID)
	}
	for _, key := range []string{"0XF130002F61000ABC", "52050", "0X000000000000CB52"} {
		if !guids[0][key] {
			t.Errorf("the guid set does not hold %q: %v", key, guids[0])
		}
	}

	// The reader's own refusals reach the walk (a patrol path with one point).
	short := `{"format":"tb-patrols","version":1,"map":249,"patrols":[{"id":"52050",
 "path":[{"x":49.8,"y":-114.6,"z":-24.3}],"period_s":33.4}]}`
	shortPath := filepath.Join(t.TempDir(), "short.json")
	if err := os.WriteFile(shortPath, []byte(short), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, _, _, err := readPatrolsFile(shortPath); err == nil || !strings.Contains(err.Error(), "need 2") {
		t.Fatalf("a one-point path: %v, want the reader's refusal", err)
	}

	f := newFixture(t)
	f.opts.dryRun = true
	f.opts.patrols = path
	if code := f.run(context.Background()); code != exitOK {
		t.Fatalf("dry run with --patrols: exit %d, %s", code, f.stderr.String())
	}

	// A file of another map is a usage error (the plan's map check stays in the walk).
	other := strings.Replace(doc, `"map":249`, `"map":1`, 1)
	if err := os.WriteFile(path, []byte(other), 0o600); err != nil {
		t.Fatal(err)
	}
	if code := f.run(context.Background()); code != exitUsage {
		t.Fatalf("--patrols of map 1: exit %d, want %d", code, exitUsage)
	}
}

// TestRaidWalkTierFlags: --tier fills a raid the table leaves unknown, --tier-override marks a comparison, and both
// are refused where they do not apply.
func TestRaidWalkTierFlags(t *testing.T) {
	t.Run("override", func(t *testing.T) {
		f := newFixture(t)
		f.opts.tierOverride = 3
		f.defaultRun("onyxias_lair")
		if code := f.run(context.Background()); code != exitOK {
			t.Fatalf("exit %d: %s", code, f.stderr.String())
		}
		sum := f.summary()
		if sum.Roster.Tier != 3 || sum.Roster.TierSource != raidwalk.TierSourceOverride || !sum.Roster.TierComparison {
			t.Fatalf("roster %+v, want tier 3 by --tier-override and the comparison mark", sum.Roster)
		}
		if !hasNote(sum, "comparison, not a tier proof") {
			t.Fatalf("notes %q, want the comparison mark", sum.Notes)
		}
		want := ".bot gear raid Commander tier 3"
		if !strings.Contains(strings.Join(f.agent.gmLines(), "\n"), want) {
			t.Fatalf("no refit %q in %q", want, f.agent.gmLines())
		}
	})
	t.Run("unknown raid without a tier", func(t *testing.T) {
		f := newFixture(t)
		f.opts.plan, f.opts.holder, f.opts.dryRun = "molten_core", "raid-walk-molten_core", true
		if code := f.run(context.Background()); code != exitUsage {
			t.Fatalf("exit %d, want 2 (stderr %q)", code, f.stderr.String())
		}
		if !strings.Contains(f.stderr.String(), "--tier") {
			t.Fatalf("the refusal does not name --tier: %q", f.stderr.String())
		}
	})
	t.Run("unknown raid with --tier", func(t *testing.T) {
		f := newFixture(t)
		f.opts.plan, f.opts.holder, f.opts.dryRun = "molten_core", "raid-walk-molten_core", true
		f.opts.tier = 1
		if code := f.run(context.Background()); code != exitOK {
			t.Fatalf("exit %d: %s", code, f.stderr.String())
		}
		if !strings.Contains(f.stdout.String(), "tier 1 (--tier)") {
			t.Fatalf("the dry run does not name the flag tier: %q", f.stdout.String())
		}
	})
	t.Run("--tier on a known raid", func(t *testing.T) {
		f := newFixture(t)
		f.opts.tier, f.opts.dryRun = 3, true
		if code := f.run(context.Background()); code != exitUsage {
			t.Fatalf("exit %d, want 2", code)
		}
		if !strings.Contains(f.stderr.String(), "--tier-override") {
			t.Fatalf("the refusal does not offer --tier-override: %q", f.stderr.String())
		}
	})
	t.Run("both flags", func(t *testing.T) {
		var stdout, stderr bytes.Buffer
		code := raidWalkCmd(context.Background(), []string{"--tier", "1", "--tier-override", "2", "onyxias_lair"}, &stdout, &stderr)
		if code != exitUsage || !strings.Contains(stderr.String(), "not both") {
			t.Fatalf("exit %d, stderr %q", code, stderr.String())
		}
	})
	t.Run("out of range", func(t *testing.T) {
		var stdout, stderr bytes.Buffer
		code := raidWalkCmd(context.Background(), []string{"--tier", "4", "molten_core"}, &stdout, &stderr)
		if code != exitUsage || !strings.Contains(stderr.String(), "1, 2 or 3") {
			t.Fatalf("exit %d, stderr %q", code, stderr.String())
		}
	})
}

// TestRaidWalkShortHireIsAnError: below the server's cap a short hire is a failed walk, not a note.
func TestRaidWalkShortHireIsAnError(t *testing.T) {
	f := newFixture(t)
	f.agent.hireCap = 30
	code := f.run(context.Background())
	if code != exitRaidWalkFailed {
		t.Fatalf("exit %d, want 18: %s", code, f.stderr.String())
	}
	if sum := f.summary(); !strings.Contains(sum.EndReason, "asked for 40 bots, the party has 30") {
		t.Fatalf("summary end %q", sum.EndReason)
	}
	if hasNote(f.summary(), "hire cap 39") {
		t.Fatalf("a short hire below the cap was noted away: %q", f.summary().Notes)
	}
}

func TestRaidWalkRefusesAServerWithoutUntil(t *testing.T) {
	t.Run("err", func(t *testing.T) {
		f := newFixture(t)
		f.agent.startReply = &commanderReply{Reply: tbm.Reply{Kind: tbm.KindErr, OK: false,
			Err: &tbm.Err{Intent: "run start", Code: "invalid", Message: "unknown word until"}, Line: "TBM:ACTION_ERR|run start|invalid|unknown word until"}}
		code := f.run(context.Background())
		if code != exitRaidWalkFailed {
			t.Fatalf("exit %d, want 18", code)
		}
		if n := countPrefix(f.agent.gmLines(), ".bot run stop"); n != 0 {
			t.Fatalf("%d run stops after a refused start", n)
		}
		if !strings.Contains(f.summary().EndReason, "has no `until`") {
			t.Fatalf("the refusal does not name run-until: %q", f.summary().EndReason)
		}
	})
	t.Run("no label", func(t *testing.T) {
		f := newFixture(t)
		f.agent.startReply = &commanderReply{Reply: tbm.Reply{Kind: tbm.KindAck, OK: true,
			Ack:  &tbm.Ack{Intent: "run start", Scope: "party", Count: 1, Executor: "-"},
			Line: "TBM:ACTION_ACK|run start|party|1|-",
			Lines: []string{
				"TBM:RUN|onyxias_lair|running|1/29|onyxia|move|walk|walking",
				"TBM:ACTION_ACK|run start|party|1|-",
			}}}
		code := f.run(context.Background())
		if code != exitRaidWalkFailed {
			t.Fatalf("exit %d, want 18", code)
		}
		if n := countPrefix(f.agent.gmLines(), ".bot run stop"); n != 1 {
			t.Fatalf("%d run stops, want exactly 1", n)
		}
		if !strings.Contains(f.summary().EndReason, "has no `until`") {
			t.Fatalf("summary reason %q", f.summary().EndReason)
		}
	})
}

func countPrefix(lines []string, prefix string) int {
	n := 0
	for _, l := range lines {
		if strings.HasPrefix(l, prefix) {
			n++
		}
	}
	return n
}

func TestRaidWalkReadsRawLinesNotStrippedText(t *testing.T) {
	f := newFixture(t)
	var q []queuedEvent
	for step := 2; step <= 28; step++ {
		q = append(q, queuedEvent{raw: fmt.Sprintf("TBM:RUN|onyxias_lair|running|%d/29|onyxia|move|walk|walking", step)})
	}
	// The walk-3 lesson (spec 0.1): this event's text lost the "|r" of the plan name, the raw line did not.
	q = append(q, queuedEvent{
		raw:  "TBM:RUN|onyxias_lair|done|28/29|onyxia|move|walk|stopped before step 29 (until boss): the party stays where it stands",
		text: "TBM:RUN|onyxias_lairunning|done|28/29|onyxia|move|walk|stopped before step 29 (until boss): the party stays where it stands",
	})
	f.agent.mu.Lock()
	f.agent.startQueue = q
	f.agent.mu.Unlock()
	code := f.run(context.Background())
	if code != exitOK {
		t.Fatalf("exit %d: %s", code, f.stderr.String())
	}
	if sum := f.summary(); sum.EndReason != "until" || sum.StepsReached != 28 {
		t.Fatalf("the walk did not read the raw done line: end %q steps %d", sum.EndReason, sum.StepsReached)
	}
}

// allDeadFromHires makes every hire dead once the roster has been hired.
func (f *fixture) allDeadFromHires() {
	f.agent.onGM = func(line string) {
		if !strings.HasPrefix(line, ".bot hire raid ") {
			return
		}
		f.agent.mu.Lock()
		if len(f.agent.party) >= 39 {
			for _, n := range f.agent.party {
				f.agent.dead[n] = true
			}
		}
		f.agent.mu.Unlock()
	}
}

func TestRaidWalkWipeRevivesOnlyWhenQuiet(t *testing.T) {
	run := func(t *testing.T, neverQuiet bool) (*fixture, raidwalk.Summary) {
		f := newFixture(t)
		f.agent.startReply = startAt("onyxias_lair", 23)
		f.agent.unitsFn = func(n int) []objects.Unit {
			if neverQuiet || n <= 3 {
				f.agent.mu.Lock()
				x, y, z := f.agent.x, f.agent.y, f.agent.z
				f.agent.mu.Unlock()
				return []objects.Unit{{Type: "creature", Name: "Onyxian Warder", Hostile: true, Moving: true, X: x + 60, Y: y, Z: z}}
			}
			return []objects.Unit{}
		}
		f.allDeadFromHires()
		if !neverQuiet {
			// The wipe recovers (--wipe-resume 1): the run goes on, so the server must show it going.
			var resumeAt time.Time
			resumeHook(f, 24, 28, &resumeAt)
		}
		code := f.run(context.Background())
		if neverQuiet {
			if code != exitRaidWalkIncident {
				t.Fatalf("exit %d, want 19: %s\n%s", code, f.stderr.String(), f.stdout.String())
			}
		} else if code != exitOK {
			t.Fatalf("exit %d, want 0: %s\n%s", code, f.stderr.String(), f.stdout.String())
		}
		return f, f.summary()
	}
	t.Run("quiet after three checks", func(t *testing.T) {
		f, sum := run(t, false)
		gm := f.agent.gmLines()
		firstSummon, firstRevive := -1, -1
		for i, line := range gm {
			if firstSummon < 0 && strings.HasPrefix(line, ".summon ") {
				firstSummon = i
			}
			if firstRevive < 0 && strings.HasPrefix(line, ".revive ") {
				firstRevive = i
			}
		}
		if firstSummon < 0 || firstRevive < 0 || firstSummon > firstRevive {
			t.Fatalf("no summon/revive in order: %q", gm)
		}
		f.agent.mu.Lock()
		unitsAtSummon := f.agent.gmUnits[firstSummon]
		f.agent.mu.Unlock()
		if unitsAtSummon < 3 {
			t.Fatalf("the first summon came after %d units calls, before the warder left", unitsAtSummon)
		}
		if sum.EndReason != "until" || sum.ExitCode != exitOK || sum.WipeResumes != 1 {
			t.Fatalf("summary end %q exit %d wipe_resumes %d, want until 0 1", sum.EndReason, sum.ExitCode, sum.WipeResumes)
		}
	})
	t.Run("never quiet", func(t *testing.T) {
		f, sum := run(t, true)
		if n := countPrefix(f.agent.gmLines(), ".summon "); n != 0 {
			t.Fatalf("%d summons while the warder never left", n)
		}
		if !strings.Contains(sum.EndReason, "not revived") {
			t.Fatalf("summary end %q", sum.EndReason)
		}
	})
}

// resumeHook re-queues one running line at step once the walk sends `run resume` through the commander
// RPC: the server shows the run going again, so resumeRun's 60 s wait sees it. doneStep appends the until done
// line right after, so the walk ends at its goal once the watch loop polls again. resumeAt records the fake
// clock when the resume went out, for the rest-length assert.
func resumeHook(f *fixture, step, doneStep int, resumeAt *time.Time) {
	f.agent.onResume = func() {
		*resumeAt = f.clock.now()
		f.agent.mu.Lock()
		defer f.agent.mu.Unlock()
		f.agent.lines = append(f.agent.lines,
			queuedEvent{raw: "TBM:RUN|onyxias_lair|running|" + strconv.Itoa(step) + "/29|onyxia|move|walk|resumed"},
			queuedEvent{raw: "TBM:RUN|onyxias_lair|done|" + strconv.Itoa(doneStep) + "/29|onyxia|move|walk|stopped before step " + strconv.Itoa(doneStep+1) + " (until boss): the party stays where it stands"})
	}
}

func TestRaidWalkNeverRevivesWhileTheBossHasATarget(t *testing.T) {
	f := newFixture(t)
	f.agent.startReply = startAt("onyxias_lair", 10)
	f.agent.hostilesFn = func(n int) []tbm.HostileRec {
		targeted := n < 8
		f.agent.mu.Lock()
		f.agent.targeted = targeted
		f.agent.mu.Unlock()
		if targeted {
			return []tbm.HostileRec{{GUID: 9, Entry: 10184, HPPct: 100, Target: packet.GUID(1000)}}
		}
		return []tbm.HostileRec{{GUID: 9, Entry: 10184, HPPct: 100}}
	}
	f.allDeadFromHires()
	code := f.run(context.Background())
	if code != exitRaidWalkIncident {
		t.Fatalf("exit %d, want 19: %s", code, f.stderr.String())
	}
	f.agent.mu.Lock()
	timeline := append([]timelineEntry{}, f.agent.timeline...)
	f.agent.mu.Unlock()
	revives := 0
	for _, e := range timeline {
		if strings.HasPrefix(e.Text, ".revive ") {
			revives++
			if e.Targeted {
				t.Fatalf("a .revive went out while the boss row still had a target")
			}
		}
	}
	if revives == 0 {
		t.Fatalf("the wipe at the boss was never revived after the reset")
	}
}

// TestRaidWalkNearWipePauseResumes: the walk-13 paused line (a near-wipe stop outside a boss fight) recovers the
// same way: revive, rest, resume, and the run reaches until with wipe_resumes 1 and no run stop.
func TestRaidWalkNearWipePauseResumes(t *testing.T) {
	f := newFixture(t)
	f.agent.startReply = startAt("onyxias_lair", 10)
	f.agent.unitsFn = func(n int) []objects.Unit { return []objects.Unit{} }
	var resumeAt time.Time
	resumeHook(f, 11, 28, &resumeAt)
	f.agent.mu.Lock()
	f.agent.startQueue = []queuedEvent{
		{raw: "TBM:RUN|onyxias_lair|paused|10/29|onyxia|move|walk|19 dead: 3 near wipes within 10 min (54): the run stops here. Regroup (c...)"},
	}
	f.agent.mu.Unlock()
	code := f.run(context.Background())
	if code != exitOK {
		t.Fatalf("exit %d, want 0: %s\n%s", code, f.stderr.String(), f.stdout.String())
	}
	if n := countPrefix(f.agent.gmLines(), ".bot run stop"); n != 0 {
		t.Fatalf("%d run stops before the end, want none (the walk resumes, it never stops)", n)
	}
	if resumeAt.IsZero() {
		t.Fatal("no run resume went out")
	}
	sum := f.summary()
	if sum.WipeResumes != 1 {
		t.Fatalf("wipe_resumes %d, want 1", sum.WipeResumes)
	}
	if !hasNote(sum, "wipe-resume 1/1 at step 10: revived") || !hasNote(sum, "resumed") {
		t.Fatalf("notes %q, want the recovery note", sum.Notes)
	}
	if sum.EndReason != "until" {
		t.Fatalf("end %q, want until (the run went on after the recovery)", sum.EndReason)
	}
}

// TestRaidWalkSecondWipeEndsTheWalk: K=1 and two wipes. The first recovers; the second ends as today (exit 19,
// run stop sent).
func TestRaidWalkSecondWipeEndsTheWalk(t *testing.T) {
	f := newFixture(t)
	f.agent.startReply = startAt("onyxias_lair", 10)
	f.agent.unitsFn = func(n int) []objects.Unit { return []objects.Unit{} }
	var resumeAt time.Time
	resumeHook(f, 11, 28, &resumeAt)
	f.allDeadFromHires()
	// The second wipe: after the resume succeeds, every hire dies again two frames later, so the resumed
	// watch (not the resume wait) sees it and ends the walk as today.
	f.agent.onResume = func() {
		resumeAt = f.clock.now()
		f.agent.mu.Lock()
		killAt := f.agent.views + 2
		f.agent.mu.Unlock()
		f.agent.hostilesFn = func(n int) []tbm.HostileRec {
			if n >= killAt {
				f.agent.mu.Lock()
				defer f.agent.mu.Unlock()
				for _, name := range f.agent.party {
					f.agent.dead[name] = true
				}
			}
			return nil
		}
		f.agent.mu.Lock()
		defer f.agent.mu.Unlock()
		f.agent.lines = append(f.agent.lines,
			queuedEvent{raw: "TBM:RUN|onyxias_lair|running|11/29|onyxia|move|walk|resumed"})
	}
	code := f.run(context.Background())
	if code != exitRaidWalkIncident {
		t.Fatalf("exit %d, want 19: %s\n%s", code, f.stderr.String(), f.stdout.String())
	}
	if n := countPrefix(f.agent.gmLines(), ".bot run stop"); n != 1 {
		t.Fatalf("%d run stops, want exactly 1 (the second wipe stops the run)", n)
	}
	sum := f.summary()
	if sum.WipeResumes != 1 {
		t.Fatalf("wipe_resumes %d, want 1 (only the first wipe recovered)", sum.WipeResumes)
	}
	if !strings.Contains(sum.EndReason, "wipe at step") {
		t.Fatalf("end %q, want the second wipe's ending", sum.EndReason)
	}
}

// TestRaidWalkWipeResumeZeroKeepsOldBehaviour: K=0 recovers nothing — a wipe ends exactly as today (exit 19,
// revive only when quiet, run stop sent).
func TestRaidWalkWipeResumeZeroKeepsOldBehaviour(t *testing.T) {
	f := newFixture(t)
	f.opts.wipeResume = 0
	f.agent.startReply = startAt("onyxias_lair", 23)
	f.agent.unitsFn = func(n int) []objects.Unit { return []objects.Unit{} }
	f.allDeadFromHires()
	code := f.run(context.Background())
	if code != exitRaidWalkIncident {
		t.Fatalf("exit %d, want 19: %s\n%s", code, f.stderr.String(), f.stdout.String())
	}
	gm := f.agent.gmLines()
	var summons, revives int
	for _, line := range gm {
		if strings.HasPrefix(line, ".summon ") {
			summons++
		}
		if strings.HasPrefix(line, ".revive ") {
			revives++
		}
	}
	if summons == 0 || revives == 0 {
		t.Fatalf("no revive with K=0: %q", gm)
	}
	if n := countPrefix(gm, ".bot run stop"); n != 1 {
		t.Fatalf("%d run stops, want exactly 1 (the wipe stops the run as today)", n)
	}
	sum := f.summary()
	if sum.WipeResumes != 0 {
		t.Fatalf("wipe_resumes %d, want 0", sum.WipeResumes)
	}
	if sum.EndReason != "wipe at step 23" {
		t.Fatalf("end %q, want the wipe ending", sum.EndReason)
	}
}

// TestRaidWalkWipeResumesOnce: all hires die at step 10, then quiet. The walk revives every dead hire, rests at
// least --wipe-rest with no `.bot run stop` before the end, resumes, and the run reaches until: exit 0,
// wipe_resumes 1.
func TestRaidWalkWipeResumesOnce(t *testing.T) {
	f := newFixture(t)
	f.agent.startReply = startAt("onyxias_lair", 10)
	f.agent.unitsFn = func(n int) []objects.Unit { return []objects.Unit{} }
	var resumeAt time.Time
	resumeHook(f, 11, 28, &resumeAt)
	f.allDeadFromHires()
	code := f.run(context.Background())
	if code != exitOK {
		t.Fatalf("exit %d, want 0: %s\n%s", code, f.stderr.String(), f.stdout.String())
	}
	gm := f.agent.gmLines()
	var summons, revives int
	for _, line := range gm {
		if strings.HasPrefix(line, ".summon ") {
			summons++
		}
		if strings.HasPrefix(line, ".revive ") {
			revives++
		}
	}
	if summons != len(f.agent.party) || revives != len(f.agent.party) {
		t.Fatalf("summoned %d revived %d of %d hires: %q", summons, revives, len(f.agent.party), gm)
	}
	if n := countPrefix(gm, ".bot run stop"); n != 0 {
		t.Fatalf("%d run stops before the end, want none (the walk resumes, it never stops)", n)
	}
	// The rest is at least --wipe-rest: the resume goes out only after the revived raid rested out of combat.
	var lastRevive time.Time
	for i, line := range gm {
		if strings.HasPrefix(line, ".revive ") {
			lastRevive = f.agent.gmAt[i]
		}
	}
	if resumeAt.IsZero() {
		t.Fatal("no run resume went out")
	}
	if resumeAt.Sub(lastRevive) < f.opts.wipeRest {
		t.Fatalf("the resume went out %s after the last revive, want at least %s", resumeAt.Sub(lastRevive), f.opts.wipeRest)
	}
	sum := f.summary()
	if sum.WipeResumes != 1 {
		t.Fatalf("wipe_resumes %d, want 1", sum.WipeResumes)
	}
	if !hasNote(sum, "wipe-resume 1/1 at step 10: revived") || !hasNote(sum, "resumed") {
		t.Fatalf("notes %q, want the recovery note", sum.Notes)
	}
	if sum.EndReason != "until" {
		t.Fatalf("end %q, want until (the run went on after the recovery)", sum.EndReason)
	}
}

// TestRaidWalkWipeResumeBossUnknownEndsOld (review 1): a dashboard read that errors at wipe time
// is unknown boss state, not "no boss". The walk must not recover: exit 19, wipe_resumes 0,
// and the skipped note.
func TestRaidWalkWipeResumeBossUnknownEndsOld(t *testing.T) {
	f := newFixture(t)
	f.agent.startReply = startAt("onyxias_lair", 10)
	f.agent.unitsFn = func(n int) []objects.Unit { return []objects.Unit{} }
	f.opts.dashURL, f.opts.dashPassFile = "http://dash.test", "/tmp/pass"
	f.dash.fightsErr = errors.New("daemon down")
	f.allDeadFromHires()
	// If the walk wrongly recovered, the resume must be able to complete, so the test
	// visibly flips (exit 0, wipe_resumes 1) instead of failing later in the resume wait.
	var resumeAt time.Time
	resumeHook(f, 11, 28, &resumeAt)
	code := f.run(context.Background())
	if code != exitRaidWalkIncident {
		t.Fatalf("exit %d, want 19: %s\n%s", code, f.stderr.String(), f.stdout.String())
	}
	sum := f.summary()
	if sum.WipeResumes != 0 {
		t.Fatalf("wipe_resumes %d, want 0 (an unknown boss is never recovered)", sum.WipeResumes)
	}
	if !hasNote(sum, "wipe resume skipped: boss state unknown") {
		t.Fatalf("notes %q, want the skipped note", sum.Notes)
	}
	if n := countPrefix(f.agent.gmLines(), ".bot run stop"); n != 1 {
		t.Fatalf("%d run stops, want exactly 1 (the old wipe ending stops the run)", n)
	}
}

// TestRaidWalkWipeResumeWithDashboardRecovers (review 3 HIGH): a dashboard that answers
// (no engaged boss) keeps the boss evidence fresh through the normal poll, so a wipe
// recovers: exactly one `run resume`, wipe_resumes 1, and the run reaches until.
func TestRaidWalkWipeResumeWithDashboardRecovers(t *testing.T) {
	f := newFixture(t)
	f.agent.startReply = startAt("onyxias_lair", 10)
	f.agent.unitsFn = func(n int) []objects.Unit { return []objects.Unit{} }
	f.opts.dashURL, f.opts.dashPassFile = "http://dash.test", "/tmp/pass"
	resumes := 0
	var resumeAt time.Time
	f.agent.onResume = func() {
		resumes++
		resumeAt = f.clock.now()
		f.agent.mu.Lock()
		defer f.agent.mu.Unlock()
		f.agent.lines = append(f.agent.lines,
			queuedEvent{raw: "TBM:RUN|onyxias_lair|running|11/29|onyxia|move|walk|resumed"},
			queuedEvent{raw: "TBM:RUN|onyxias_lair|done|28/29|onyxia|move|walk|stopped before step 29 (until boss): the party stays where it stands"})
	}
	f.allDeadFromHires()
	code := f.run(context.Background())
	if code != exitOK {
		t.Fatalf("exit %d, want 0: %s\n%s", code, f.stderr.String(), f.stdout.String())
	}
	if resumes != 1 {
		t.Fatalf("%d run resumes, want exactly 1", resumes)
	}
	if resumeAt.IsZero() {
		t.Fatal("no run resume went out")
	}
	if n := countPrefix(f.agent.gmLines(), ".bot run stop"); n != 0 {
		t.Fatalf("%d run stops before the end, want none (the walk resumes, it never stops)", n)
	}
	sum := f.summary()
	if sum.WipeResumes != 1 {
		t.Fatalf("wipe_resumes %d, want 1", sum.WipeResumes)
	}
	if sum.EndReason != "until" {
		t.Fatalf("end %q, want until (the run went on after the recovery)", sum.EndReason)
	}
}

// TestRaidWalkWipeResumeAfterBossWinRecovers (final review): the fights list is the run's
// history, so a boss fight already won (outcome "win") must not block recovery from a later
// trash wipe; a boss row still "live" must.
func TestRaidWalkWipeResumeAfterBossWinRecovers(t *testing.T) {
	for _, tc := range []struct {
		name    string
		outcome string
		resumes int
	}{
		{"won boss in the history recovers", "win", 1},
		{"live boss fight blocks recovery", "live", 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newFixture(t)
			f.agent.startReply = startAt("onyxias_lair", 10)
			f.agent.unitsFn = func(n int) []objects.Unit { return []objects.Unit{} }
			f.opts.dashURL, f.opts.dashPassFile = "http://dash.test", "/tmp/pass"
			f.dash.fights = []walkFightRow{{ID: "4-5", Key: "Commander\x001793219890\x004-5", Dur: 1200, Outcome: tc.outcome, BossName: "Onyxia", Leg: "onyxia", Step: 3, Deaths: 1}}
			resumes := 0
			f.agent.onResume = func() {
				resumes++
				f.agent.mu.Lock()
				defer f.agent.mu.Unlock()
				f.agent.lines = append(f.agent.lines,
					queuedEvent{raw: "TBM:RUN|onyxias_lair|running|11/29|onyxia|move|walk|resumed"},
					queuedEvent{raw: "TBM:RUN|onyxias_lair|done|28/29|onyxia|move|walk|stopped before step 29 (until boss): the party stays where it stands"})
			}
			f.allDeadFromHires()
			f.run(context.Background())
			if resumes != tc.resumes {
				t.Fatalf("%d run resumes, want %d (outcome %q)", resumes, tc.resumes, tc.outcome)
			}
			if tc.resumes == 0 {
				// The live boss must block the recovery before anything is revived: the old
				// ending stops the run first, and only then revives after the reset wait.
				stopAt, reviveAt := -1, -1
				for i, l := range f.agent.gmLines() {
					if stopAt < 0 && strings.HasPrefix(l, ".bot run stop") {
						stopAt = i
					}
					if reviveAt < 0 && (strings.HasPrefix(l, ".revive") || strings.HasPrefix(l, ".summon")) {
						reviveAt = i
					}
				}
				if reviveAt >= 0 && (stopAt < 0 || reviveAt < stopAt) {
					t.Fatalf("revive/summon at gm line %d before the run stop (%d): a live boss fight was revived into", reviveAt, stopAt)
				}
			}
		})
	}
}

// TestRaidWalkWipeResumeAfterEarlierBossKillRecovers (wipe-resume latch): the
// frames engage the boss at an earlier step (the tracker latches the first
// boss it sees and never forgets), the dashboard fight row says the boss died
// ("kill"), and a trash wipe more than 120 s after the last boss frame
// recovers: wipe_resumes 1 and no "boss state unknown" note. Before the fix
// the tracker's latch blocked this recovery.
func TestRaidWalkWipeResumeAfterEarlierBossKillRecovers(t *testing.T) {
	f, next, total := bossDoneFixture(t)
	f.agent.startReply = zgStart(total, next)
	f.agent.mu.Lock()
	f.agent.startQueue = []queuedEvent{
		{raw: fmt.Sprintf("TBM:RUN|molten_core|running|%d/%d|lucifron|boss|fight|fighting", next-1, total)},
	}
	f.agent.mu.Unlock()
	f.agent.unitsFn = func(n int) []objects.Unit { return []objects.Unit{} }
	f.opts.dashURL, f.opts.dashPassFile = "http://dash.test", "/tmp/pass"
	f.dash.fights = []walkFightRow{{ID: "4-5", Key: "Commander\x001793219890\x004-5", Dur: 1200, Outcome: "kill", BossName: "Lucifron", Leg: "lucifron", Step: 3, Deaths: 1}}
	// The boss shows in the frames only at the start of the walk, then the
	// raid wipes on trash 200 s of fake time later, past the 120 s
	// bossLatchAt window. The kill runs inside the frame call, so the wipe
	// lands late in the walk.
	t0 := f.clock.now()
	killed := false
	f.agent.hostilesFn = func(n int) []tbm.HostileRec {
		now := f.clock.now()
		if !killed && now.Sub(t0) > 200*time.Second {
			killed = true
			f.agent.mu.Lock()
			for _, name := range f.agent.party {
				f.agent.dead[name] = true
			}
			f.agent.mu.Unlock()
		}
		if now.Sub(t0) < 45*time.Second {
			return []tbm.HostileRec{{GUID: 9, Entry: 12118, HPPct: 90, Target: packet.GUID(1000)}}
		}
		return nil
	}
	f.agent.onResume = func() {
		f.agent.mu.Lock()
		defer f.agent.mu.Unlock()
		f.agent.lines = append(f.agent.lines,
			queuedEvent{raw: fmt.Sprintf("TBM:RUN|molten_core|running|%d/%d|lucifron|move|walk|resumed", next-1, total)},
			queuedEvent{raw: fmt.Sprintf("TBM:RUN|molten_core|done|%d/%d|lucifron|move|walk|stopped before step %d (until step %d): the party stays where it stands", next, total, next, next)})
	}
	code := f.run(context.Background())
	if code != exitOK {
		t.Fatalf("exit %d, want 0: %s\n%s", code, f.stderr.String(), f.stdout.String())
	}
	sum := f.summary()
	if !hasNote(sum, "so the run leader fights it") {
		t.Fatalf("notes %q, want the boss-fight note (the early engagement must latch)", sum.Notes)
	}
	if sum.WipeResumes != 1 {
		t.Fatalf("wipe_resumes %d, want 1 (a boss killed long before the wipe does not block recovery)", sum.WipeResumes)
	}
	if hasNote(sum, "boss state unknown") {
		t.Fatalf("notes %q, want no boss-unknown skip", sum.Notes)
	}
	if sum.EndReason != "until" {
		t.Fatalf("end %q, want until (the run went on after the recovery)", sum.EndReason)
	}
}

// TestRaidWalkWipeResumeBossStillEngagedSkips: the same walk, but the boss is
// still in the frames when the late trash wipe lands. The wipe still ends the
// walk the old way (handleWipe's bossEngagedNow check fires before any
// recovery: exit 19, wipe_resumes 0, no resume).
func TestRaidWalkWipeResumeBossStillEngagedSkips(t *testing.T) {
	f, next, total := bossDoneFixture(t)
	f.agent.startReply = zgStart(total, next)
	f.agent.mu.Lock()
	f.agent.startQueue = []queuedEvent{
		{raw: fmt.Sprintf("TBM:RUN|molten_core|running|%d/%d|lucifron|boss|fight|fighting", next-1, total)},
	}
	f.agent.mu.Unlock()
	f.agent.unitsFn = func(n int) []objects.Unit { return []objects.Unit{} }
	f.opts.dashURL, f.opts.dashPassFile = "http://dash.test", "/tmp/pass"
	f.dash.fights = []walkFightRow{{ID: "4-5", Key: "Commander\x001793219890\x004-5", Dur: 1200, Outcome: "kill", BossName: "Lucifron", Leg: "lucifron", Step: 3, Deaths: 1}}
	t0 := f.clock.now()
	killed := false
	f.agent.hostilesFn = func(n int) []tbm.HostileRec {
		if now := f.clock.now(); !killed && now.Sub(t0) > 200*time.Second {
			killed = true
			f.agent.mu.Lock()
			for _, name := range f.agent.party {
				f.agent.dead[name] = true
			}
			f.agent.mu.Unlock()
		}
		return []tbm.HostileRec{{GUID: 9, Entry: 12118, HPPct: 90, Target: packet.GUID(1000)}}
	}
	code := f.run(context.Background())
	if code != exitRaidWalkIncident {
		t.Fatalf("exit %d, want 19: %s\n%s", code, f.stderr.String(), f.stdout.String())
	}
	sum := f.summary()
	if !hasNote(sum, "so the run leader fights it") {
		t.Fatalf("notes %q, want the boss-fight note (the engagement must latch)", sum.Notes)
	}
	if sum.WipeResumes != 0 {
		t.Fatalf("wipe_resumes %d, want 0 (a boss still in the frames blocks recovery)", sum.WipeResumes)
	}
	if hasNote(sum, "wipe-resume ") {
		t.Fatalf("notes %q, want no recovery note (the wipe ended as before, with no resume)", sum.Notes)
	}
}

// TestRaidWalkWipeResumeVisibleBossAfterKillRecovers (walk 17 s4 replay): an
// engaged boss early (the latch), killed and gone, then a plan `first` add in
// view at full health with no target (standing as trash) while the raid wipes
// on trash more than 200 s later. A row merely in view must not latch, so the
// wipe recovers: exit 0, wipe_resumes 1, no boss-unknown skip, end until.
func TestRaidWalkWipeResumeVisibleBossAfterKillRecovers(t *testing.T) {
	f, next, total := bossDoneFixture(t)
	f.agent.startReply = zgStart(total, next)
	f.agent.mu.Lock()
	f.agent.startQueue = []queuedEvent{
		{raw: fmt.Sprintf("TBM:RUN|molten_core|running|%d/%d|lucifron|boss|fight|fighting", next-1, total)},
		{raw: "TBM:RUN|molten_core|running|35/235|lucifron|move|walk|walking"},
	}
	f.agent.mu.Unlock()
	f.agent.unitsFn = func(n int) []objects.Unit { return []objects.Unit{} }
	f.opts.dashURL, f.opts.dashPassFile = "http://dash.test", "/tmp/pass"
	f.dash.fights = []walkFightRow{{ID: "4-5", Key: "Commander\x001793219890\x004-5", Dur: 1200, Outcome: "kill", BossName: "Lucifron", Leg: "lucifron", Step: 3, Deaths: 1}}
	t0 := f.clock.now()
	killed := false
	// Wipe at 240 s: like the walk-17 s4 shape, but late enough that the
	// run left the boss step for trash (so the watch never routes the
	// visible rows to handleBoss).
	wipeAt := t0.Add(240 * time.Second)
	f.agent.hostilesFn = func(n int) []tbm.HostileRec {
		now := f.clock.now()
		if !killed && !now.Before(wipeAt) {
			killed = true
			f.agent.mu.Lock()
			for _, name := range f.agent.party {
				f.agent.dead[name] = true
			}
			f.agent.mu.Unlock()
		}
		if now.Sub(t0) < 45*time.Second {
			// The boss row holds a target: engaged, latches, and notes the fight.
			return []tbm.HostileRec{{GUID: 9, Entry: 12118, HPPct: 90, Target: packet.GUID(1000)}}
		}
		// The boss is killed and gone; from 150 s on a plan `first` add
		// (Flamewaker Protector) stands as trash in view — alive, full
		// health, no target — through the wipe (as in s4, where the row was
		// last saved ~25 s before it).
		if elapsed := now.Sub(t0); elapsed >= 150*time.Second {
			return []tbm.HostileRec{{GUID: 11, Entry: 12119, HPPct: 100, Target: 0}}
		}
		return nil
	}
	f.agent.onResume = func() {
		f.agent.mu.Lock()
		defer f.agent.mu.Unlock()
		f.agent.lines = append(f.agent.lines,
			queuedEvent{raw: fmt.Sprintf("TBM:RUN|molten_core|running|%d/%d|lucifron|boss|fight|fighting resumed", next-1, total)},
			queuedEvent{raw: fmt.Sprintf("TBM:RUN|molten_core|done|%d/%d|lucifron|move|walk|stopped before step %d (until step %d): the party stays where it stands", next, total, next, next)})
	}
	code := f.run(context.Background())
	if code != exitOK {
		t.Fatalf("exit %d, want 0: %s\n%s", code, f.stderr.String(), f.stdout.String())
	}
	sum := f.summary()
	if !hasNote(sum, "so the run leader fights it") {
		t.Fatalf("notes %q, want the boss-fight note (the early engagement must latch)", sum.Notes)
	}
	if sum.WipeResumes != 1 {
		t.Fatalf("wipe_resumes %d, want 1 (a merely visible plan add does not block recovery)", sum.WipeResumes)
	}
	if hasNote(sum, "boss state unknown") {
		t.Fatalf("notes %q, want no boss-unknown skip", sum.Notes)
	}
	if sum.EndReason != "until" {
		t.Fatalf("end %q, want until (the run went on after the recovery)", sum.EndReason)
	}
}

// TestRaidWalkWipeResumeVisibleBossNeverTargetedRecovers: no plan boss row
// ever holds a target; the boss shows at 100 % with no target (a plan boss
// merely in view, never fought) while the raid wipes on trash more than
// 200 s later. Nothing was ever engaged, so the wipe recovers — and the walk
// must not route the visible row to handleBoss.
func TestRaidWalkWipeResumeVisibleBossNeverTargetedRecovers(t *testing.T) {
	f, next, total := bossDoneFixture(t)
	f.agent.startReply = zgStart(total, next)
	f.agent.mu.Lock()
	f.agent.startQueue = []queuedEvent{
		{raw: fmt.Sprintf("TBM:RUN|molten_core|running|%d/%d|lucifron|move|walk|walking", next-2, total)},
		{raw: "TBM:RUN|molten_core|running|35/235|lucifron|move|walk|walking"},
	}
	f.agent.mu.Unlock()
	t0 := f.clock.now()
	killed := false
	// Wipe at 240 s: like the walk-17 s4 shape, but late enough that the
	// run left the boss step for trash (so the watch never routes the
	// visible row to handleBoss).
	wipeAt := t0.Add(240 * time.Second)
	f.agent.hostilesFn = func(n int) []tbm.HostileRec {
		now := f.clock.now()
		if !killed && !now.Before(wipeAt) {
			killed = true
			f.agent.mu.Lock()
			for _, name := range f.agent.party {
				f.agent.dead[name] = true
			}
			f.agent.mu.Unlock()
		}
		// Every frame holds the boss in view at full health with no target:
		// never engaged, latching nothing. (Kept through the wipe: before
		// the fix the fresh 100 %/no-target row counts as engaged.)
		return []tbm.HostileRec{{GUID: 9, Entry: 12118, HPPct: 100, Target: 0}}
	}
	f.agent.onResume = func() {
		f.agent.mu.Lock()
		defer f.agent.mu.Unlock()
		f.agent.lines = append(f.agent.lines,
			queuedEvent{raw: fmt.Sprintf("TBM:RUN|molten_core|running|%d/%d|lucifron|move|walk|resumed", next, total)},
			queuedEvent{raw: fmt.Sprintf("TBM:RUN|molten_core|done|%d/%d|lucifron|move|walk|stopped before step %d (until step %d): the party stays where it stands", next, total, next, next)})
	}
	code := f.run(context.Background())
	if code != exitOK {
		t.Fatalf("exit %d, want 0: %s\n%s", code, f.stderr.String(), f.stdout.String())
	}
	sum := f.summary()
	if sum.WipeResumes != 1 {
		t.Fatalf("wipe_resumes %d, want 1 (a never-targeted visible row is no fight)", sum.WipeResumes)
	}
	if hasNote(sum, "boss state unknown") {
		t.Fatalf("notes %q, want no boss-unknown skip", sum.Notes)
	}
	if hasNote(sum, "boss 12118 engaged") {
		t.Fatalf("notes %q, want no handleBoss routing (nothing was ever engaged)", sum.Notes)
	}
	if !strings.Contains(sum.EndReason, "until") {
		t.Fatalf("end %q, want until (the run went on after the recovery)", sum.EndReason)
	}
}

// TestRaidWalkWipeResumeDashOffRecentBossSkips: the dash-off fail-safe. A
// plan boss row engaged into the trash wipe (a fight in progress) ends the
// wipe the old way and never recovers: exit 19, wipe_resumes 0, no recovery
// note, the run stop before the reset-wait revive. Both an engaged shape the
// HP-only predicate would miss (100 % with a target: the `target` case) and
// one the target-only predicate would miss (hurt with no target: the `hurt`
// case) end here; with either predicate arm dropped, the row no longer latches
// and the wipe recovers instead (red).
func TestRaidWalkWipeResumeDashOffRecentBossSkips(t *testing.T) {
	for _, tc := range []struct {
		name string
		row  tbm.HostileRec
	}{
		{"target", tbm.HostileRec{GUID: 9, Entry: 12118, HPPct: 100, Target: packet.GUID(1000)}},
		{"hurt", tbm.HostileRec{GUID: 9, Entry: 12118, HPPct: 60, Target: 0}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f, next, total := bossDoneFixture(t)
			f.agent.startReply = zgStart(total, next)
			f.agent.mu.Lock()
			// Boss-kind status: the run is on its boss step, so the watch
			// absorbs the tracker's engagement (run-leader-fights-it note)
			// and the walk stays out until the trash wipe, like the model.
			f.agent.startQueue = []queuedEvent{
				{raw: fmt.Sprintf("TBM:RUN|molten_core|running|%d/%d|lucifron|boss|fight|fighting", next-1, total)},
			}
			f.agent.mu.Unlock()
			f.agent.unitsFn = func(n int) []objects.Unit { return []objects.Unit{} }
			killed := false
			row := tc.row
			// Wipe at 240 s: like the walk-17 s4 shape, the engaged row
			// stays in the frames until 40 s before the wipe, then is gone
			// (past the stale-row gate while the latch is inside its 120 s
			// window), and stays gone through the recovery: only the latch
			// blocks, via `bossUnknown`.
			wipeAt := f.clock.now().Add(240 * time.Second)
			f.agent.hostilesFn = func(n int) []tbm.HostileRec {
				now := f.clock.now()
				if !killed && !now.Before(wipeAt) {
					killed = true
					f.agent.mu.Lock()
					for _, name := range f.agent.party {
						f.agent.dead[name] = true
					}
					f.agent.mu.Unlock()
				}
				if now.Before(wipeAt.Add(-40 * time.Second)) {
					return []tbm.HostileRec{row}
				}
				return nil
			}
			code := f.run(context.Background())
			if code != exitRaidWalkIncident {
				t.Fatalf("exit %d, want 19: %s\n%s", code, f.stderr.String(), f.stdout.String())
			}
			sum := f.summary()
			if sum.WipeResumes != 0 {
				t.Fatalf("wipe_resumes %d, want 0 (an engaged boss ends the wipe the old way)", sum.WipeResumes)
			}
			if hasNote(sum, "wipe-resume ") {
				t.Fatalf("notes %q, want no recovery note (the wipe ended as before, with no resume)", sum.Notes)
			}
			stopAt, reviveAt := -1, -1
			for i, l := range f.agent.gmLines() {
				if stopAt < 0 && strings.HasPrefix(l, ".bot run stop") {
					stopAt = i
				}
				if reviveAt < 0 && (strings.HasPrefix(l, ".revive ") || strings.HasPrefix(l, ".summon ")) {
					reviveAt = i
				}
			}
			if reviveAt >= 0 && (stopAt < 0 || reviveAt < stopAt) {
				t.Fatalf("revive/summon at gm line %d before the run stop (%d): an unknown boss was revived into", reviveAt, stopAt)
			}
		})
	}
}

// TestRaidWalkWipeResumeBossCorpseRecovers: like the walk-17 s4 replay, but
// the frames from 150 s on hold the boss's corpse (0 %). A corpse never
// latches, so the wipe recovers: wipe_resumes 1, no boss-unknown skip.
func TestRaidWalkWipeResumeBossCorpseRecovers(t *testing.T) {
	f, next, total := bossDoneFixture(t)
	f.agent.startReply = zgStart(total, next)
	f.agent.mu.Lock()
	f.agent.startQueue = []queuedEvent{
		{raw: fmt.Sprintf("TBM:RUN|molten_core|running|%d/%d|lucifron|boss|fight|fighting", next-1, total)},
	}
	f.agent.mu.Unlock()
	f.agent.unitsFn = func(n int) []objects.Unit { return []objects.Unit{} }
	f.opts.dashURL, f.opts.dashPassFile = "http://dash.test", "/tmp/pass"
	f.dash.fights = []walkFightRow{{ID: "4-5", Key: "Commander\x001793219890\x004-5", Dur: 1200, Outcome: "kill", BossName: "Lucifron", Leg: "lucifron", Step: 3, Deaths: 1}}
	t0 := f.clock.now()
	killed := false
	// Wipe at 240 s: like the walk-17 s4 shape, but late enough that the
	// run left the boss step for trash (so the watch never routes the
	// engaged-then-dead rows to handleBoss).
	wipeAt := t0.Add(240 * time.Second)
	f.agent.hostilesFn = func(n int) []tbm.HostileRec {
		now := f.clock.now()
		if !killed && !now.Before(wipeAt) {
			killed = true
			f.agent.mu.Lock()
			for _, name := range f.agent.party {
				f.agent.dead[name] = true
			}
			f.agent.mu.Unlock()
		}
		if now.Sub(t0) < 45*time.Second {
			return []tbm.HostileRec{{GUID: 9, Entry: 12118, HPPct: 90, Target: packet.GUID(1000)}}
		}
		// The boss is killed (corpse in view) from 150 s on, through the wipe:
		// a 0 % row never latches, so the wipe can recover.
		if elapsed := now.Sub(t0); elapsed >= 150*time.Second {
			return []tbm.HostileRec{{GUID: 9, Entry: 12118, HPPct: 0, Target: 0}}
		}
		return nil
	}
	f.agent.onResume = func() {
		f.agent.mu.Lock()
		defer f.agent.mu.Unlock()
		f.agent.lines = append(f.agent.lines,
			queuedEvent{raw: fmt.Sprintf("TBM:RUN|molten_core|running|%d/%d|lucifron|boss|fight|fighting resumed", next-1, total)},
			queuedEvent{raw: fmt.Sprintf("TBM:RUN|molten_core|done|%d/%d|lucifron|move|walk|stopped before step %d (until step %d): the party stays where it stands", next, total, next, next)})
	}
	code := f.run(context.Background())
	if code != exitOK {
		t.Fatalf("exit %d, want 0: %s\n%s", code, f.stderr.String(), f.stdout.String())
	}
	sum := f.summary()
	if sum.WipeResumes != 1 {
		t.Fatalf("wipe_resumes %d, want 1 (a boss corpse does not block recovery)", sum.WipeResumes)
	}
	if hasNote(sum, "boss state unknown") {
		t.Fatalf("notes %q, want no boss-unknown skip", sum.Notes)
	}
}

// TestRaidWalkWipeResumeRoutesNewBossToHandleBoss (review 2 HIGH): boss A is
// engaged early (the tracker latches it for life) and the dashboard files it
// as outcome "kill", so a trash wipe more than 120 s later recovers. Boss B
// wakes during the resume wait: the walk must route it to handleBoss (exit 19
// with the boss-B note and stop reason), not report a clean wipe-resume while
// a boss fight is live. Before the fix the lifetime latch made recoveredBoss
// always see hadBoss and skip the routing.
func TestRaidWalkWipeResumeRoutesNewBossToHandleBoss(t *testing.T) {
	f, next, total := bossDoneFixture(t)
	f.agent.startReply = zgStart(total, next)
	f.agent.mu.Lock()
	f.agent.startQueue = []queuedEvent{
		{raw: fmt.Sprintf("TBM:RUN|molten_core|running|%d/%d|lucifron|boss|fight|fighting", next-1, total)},
	}
	f.agent.mu.Unlock()
	f.agent.unitsFn = func(n int) []objects.Unit { return []objects.Unit{} }
	f.opts.dashURL, f.opts.dashPassFile = "http://dash.test", "/tmp/pass"
	f.dash.fights = []walkFightRow{{ID: "4-5", Key: "Commander\x001793219890\x004-5", Dur: 1200, Outcome: "kill", BossName: "Lucifron", Leg: "lucifron", Step: 3, Deaths: 1}}
	t0 := f.clock.now()
	killed := false
	resumed := false
	f.agent.hostilesFn = func(n int) []tbm.HostileRec {
		now := f.clock.now()
		if !killed && now.Sub(t0) > 200*time.Second {
			killed = true
			f.agent.mu.Lock()
			for _, name := range f.agent.party {
				f.agent.dead[name] = true
			}
			f.agent.mu.Unlock()
		}
		if now.Sub(t0) < 45*time.Second {
			return []tbm.HostileRec{{GUID: 9, Entry: 12118, HPPct: 90, Target: packet.GUID(1000)}}
		}
		if resumed {
			return []tbm.HostileRec{{GUID: 9, Entry: 11982, HPPct: 90, Target: packet.GUID(1000)}}
		}
		return nil
	}
	f.agent.onResume = func() {
		resumed = true
		f.agent.mu.Lock()
		defer f.agent.mu.Unlock()
		f.agent.lines = append(f.agent.lines,
			queuedEvent{raw: fmt.Sprintf("TBM:RUN|molten_core|running|%d/%d|lucifron|move|walk|resumed", next-1, total)},
			queuedEvent{raw: fmt.Sprintf("TBM:RUN|molten_core|done|%d/%d|lucifron|move|walk|stopped before step %d (until step %d): the party stays where it stands", next, total, next, next)})
	}
	code := f.run(context.Background())
	if code != exitRaidWalkIncident {
		t.Fatalf("exit %d, want 19: %s\n%s", code, f.stderr.String(), f.stdout.String())
	}
	sum := f.summary()
	if !hasNote(sum, "so the run leader fights it") {
		t.Fatalf("notes %q, want the boss-fight note (the early engagement must latch)", sum.Notes)
	}
	if sum.WipeResumes != 0 {
		t.Fatalf("wipe_resumes %d, want 0 (a boss engaged during the resume wait is not a recovery)", sum.WipeResumes)
	}
	if !hasNote(sum, "boss 11982 engaged") {
		t.Fatalf("notes %q, want the handleBoss note for the new boss", sum.Notes)
	}
	if !strings.Contains(sum.EndReason, "boss 11982 engaged") {
		t.Fatalf("end %q, want the boss stop reason for the new boss", sum.EndReason)
	}
	if hasNote(sum, "wipe-resume ") {
		t.Fatalf("notes %q, want no recovery note (the walk routed to the boss path)", sum.Notes)
	}
}

// TestRaidWalkWipeResumeStaleBossStatusRoutesNewBossToHandleBoss (review 3
// HIGH): --until "step N" past the boss, and the retained status is an earlier
// boss-kind line. A trash wipe recovers with NO fresh post-resume run line,
// and a new boss is engaged in the current frames. The walk must route it to
// handleBoss (exit 19 with the new boss's note and stop reason): the stale
// pre-resume boss-kind status must not count as the run fighting its boss.
// Before the fix recoveredBoss applied the runFightsBoss exception on the
// retained status and reported a clean wipe-resume.
func TestRaidWalkWipeResumeStaleBossStatusRoutesNewBossToHandleBoss(t *testing.T) {
	f, next, total := bossDoneFixture(t)
	f.agent.startReply = zgStart(total, next)
	f.agent.mu.Lock()
	f.agent.startQueue = []queuedEvent{
		{raw: fmt.Sprintf("TBM:RUN|molten_core|running|%d/%d|lucifron|boss|fight|fighting", next-1, total)},
	}
	f.agent.mu.Unlock()
	f.agent.unitsFn = func(n int) []objects.Unit { return []objects.Unit{} }
	f.opts.dashURL, f.opts.dashPassFile = "http://dash.test", "/tmp/pass"
	f.dash.fights = []walkFightRow{{ID: "4-5", Key: "Commander\x001793219890\x004-5", Dur: 1200, Outcome: "kill", BossName: "Lucifron", Leg: "lucifron", Step: 3, Deaths: 1}}
	t0 := f.clock.now()
	killed := false
	resumed := false
	f.agent.hostilesFn = func(n int) []tbm.HostileRec {
		now := f.clock.now()
		if !killed && now.Sub(t0) > 200*time.Second {
			killed = true
			f.agent.mu.Lock()
			for _, name := range f.agent.party {
				f.agent.dead[name] = true
			}
			f.agent.mu.Unlock()
		}
		if now.Sub(t0) < 45*time.Second {
			return []tbm.HostileRec{{GUID: 9, Entry: 12118, HPPct: 90, Target: packet.GUID(1000)}}
		}
		if resumed {
			return []tbm.HostileRec{{GUID: 9, Entry: 11982, HPPct: 90, Target: packet.GUID(1000)}}
		}
		return nil
	}
	// No fresh post-resume run line: pollRunRunning passes on the retained
	// pre-resume boss-kind status, so recoveredBoss must still route the
	// frames' new boss to handleBoss.
	f.agent.onResume = func() {
		resumed = true
	}
	code := f.run(context.Background())
	if code != exitRaidWalkIncident {
		t.Fatalf("exit %d, want 19: %s\n%s", code, f.stderr.String(), f.stdout.String())
	}
	sum := f.summary()
	if !hasNote(sum, "so the run leader fights it") {
		t.Fatalf("notes %q, want the boss-fight note (the early engagement must latch)", sum.Notes)
	}
	if sum.WipeResumes != 0 {
		t.Fatalf("wipe_resumes %d, want 0 (a boss engaged after the resume is not a recovery)", sum.WipeResumes)
	}
	bossRouted := false
	for _, n := range sum.Notes {
		if strings.HasPrefix(n, "boss 11982 engaged at step") && !strings.Contains(n, "so the run leader fights it") {
			bossRouted = true
		}
	}
	if !bossRouted {
		t.Fatalf("notes %q, want the handleBoss note for the new boss (not the run-leader exception)", sum.Notes)
	}
	if !strings.Contains(sum.EndReason, "boss 11982 engaged") {
		t.Fatalf("end %q, want the boss stop reason for the new boss", sum.EndReason)
	}
	if hasNote(sum, "wipe-resume ") {
		t.Fatalf("notes %q, want no recovery note (the walk routed to the boss path)", sum.Notes)
	}
}

// TestRaidWalkWipeResumeFreshBossStatusKeepsTheFight (review 3): the same
// walk, but a fresh post-resume run line says the run is on its boss step. The
// run leader keeps the fight: no handleBoss run stop, the recovery completes
// (wipe_resumes 1) and the walk ends at its until.
func TestRaidWalkWipeResumeFreshBossStatusKeepsTheFight(t *testing.T) {
	f, next, total := bossDoneFixture(t)
	f.agent.startReply = zgStart(total, next)
	f.agent.mu.Lock()
	f.agent.startQueue = []queuedEvent{
		{raw: fmt.Sprintf("TBM:RUN|molten_core|running|%d/%d|lucifron|boss|fight|fighting", next-1, total)},
	}
	f.agent.mu.Unlock()
	f.agent.unitsFn = func(n int) []objects.Unit { return []objects.Unit{} }
	f.opts.dashURL, f.opts.dashPassFile = "http://dash.test", "/tmp/pass"
	f.dash.fights = []walkFightRow{{ID: "4-5", Key: "Commander\x001793219890\x004-5", Dur: 1200, Outcome: "kill", BossName: "Lucifron", Leg: "lucifron", Step: 3, Deaths: 1}}
	t0 := f.clock.now()
	killed := false
	resumed := false
	f.agent.hostilesFn = func(n int) []tbm.HostileRec {
		now := f.clock.now()
		if !killed && now.Sub(t0) > 200*time.Second {
			killed = true
			f.agent.mu.Lock()
			for _, name := range f.agent.party {
				f.agent.dead[name] = true
			}
			f.agent.mu.Unlock()
		}
		if now.Sub(t0) < 45*time.Second {
			return []tbm.HostileRec{{GUID: 9, Entry: 12118, HPPct: 90, Target: packet.GUID(1000)}}
		}
		if resumed {
			return []tbm.HostileRec{{GUID: 9, Entry: 11982, HPPct: 90, Target: packet.GUID(1000)}}
		}
		return nil
	}
	f.agent.onResume = func() {
		resumed = true
		f.agent.mu.Lock()
		defer f.agent.mu.Unlock()
		f.agent.lines = append(f.agent.lines,
			queuedEvent{raw: fmt.Sprintf("TBM:RUN|molten_core|running|%d/%d|lucifron|boss|fight|fighting post-resume", next-1, total)},
			queuedEvent{raw: fmt.Sprintf("TBM:RUN|molten_core|done|%d/%d|lucifron|move|walk|stopped before step %d (until step %d): the party stays where it stands", next, total, next, next)})
	}
	code := f.run(context.Background())
	if code != exitOK {
		t.Fatalf("exit %d, want 0: %s\n%s", code, f.stderr.String(), f.stdout.String())
	}
	if n := countPrefix(f.agent.gmLines(), ".bot run stop"); n != 0 {
		t.Fatalf("%d run stops, want 0: the run leader fights its boss step (no handleBoss)", n)
	}
	sum := f.summary()
	if !hasNote(sum, "so the run leader fights it") {
		t.Fatalf("notes %q, want the boss-fight note (the fresh post-resume status keeps the fight)", sum.Notes)
	}
	if sum.WipeResumes != 1 {
		t.Fatalf("wipe_resumes %d, want 1 (the fresh post-resume boss status keeps the fight)", sum.WipeResumes)
	}
	if !hasNote(sum, "wipe-resume ") {
		t.Fatalf("notes %q, want the recovery note (the walk resumed, it never routed to the boss path)", sum.Notes)
	}
	if sum.EndReason != "until" {
		t.Fatalf("end %q, want until (the run went on after the recovery)", sum.EndReason)
	}
}

// TestRaidWalkWipeResumeStaleViewRestartsQuiet (review 2): bot_view fails and drops a hire for
// some polls of the rest. No `.bot run resume` may go out until a full window of complete views
// has passed after the last bad poll.
func TestRaidWalkWipeResumeStaleViewRestartsQuiet(t *testing.T) {
	f := newFixture(t)
	f.agent.startReply = startAt("onyxias_lair", 10)
	f.agent.unitsFn = func(n int) []objects.Unit { return []objects.Unit{} }
	f.opts.wipeRest = 90 * time.Second
	f.allDeadFromHires()
	var resumeAt time.Time
	resumes := 0
	f.agent.onResume = func() {
		resumes++
		resumeAt = f.clock.now()
		f.agent.push("TBM:RUN|onyxias_lair|running|11/29|onyxia|move|walk|resumed", "")
		f.agent.push("TBM:RUN|onyxias_lair|done|28/29|onyxia|move|walk|stopped before step 29 (until boss): the party stays where it stands", "")
	}
	// Bad polls cover the first ~60 s of the rest (viewN+30 at 2 polls per rest poll... the
	// rest polls bot_view every 5 s of fake time, one view call per poll plus watch-loop polls;
	// badUntil in view calls is generous). Odd bad polls fail, even ones drop a hire; the
	// last bad poll's fake time is recorded so the test can require a full 90 s of complete
	// views AFTER it.
	badUntil := -1
	var badEndT time.Time
	// The dropped hire is the roster's first name, but the party only exists after the hire
	// phase: resolve it per poll, not once up front.
	// The fault starts only once the wipe is on (every hire dead): the enter phase's own
	// bot_view reads stay clean, so the walk reaches the watch before the bad polls begin.
	f.agent.viewFn = func(n int) (bool, string) {
		f.agent.mu.Lock()
		wiping := len(f.agent.party) >= 39
		if wiping {
			for _, name := range f.agent.party {
				if !f.agent.dead[name] {
					wiping = false
					break
				}
			}
		}
		rec := f.agent.recoveryGo
		if badUntil < 0 && rec {
			badUntil = n + 60
		}
		f.agent.mu.Unlock()
		if badUntil >= 0 && n <= badUntil {
			badEndT = f.clock.now()
			rel := badUntil - n
			if rel >= 20 {
				return true, ""
			}
			f.agent.mu.Lock()
			dropped := ""
			if len(f.agent.party) > 0 {
				dropped = f.agent.party[0]
			}
			f.agent.mu.Unlock()
			return false, dropped
		}
		return false, ""
	}
	code := f.run(context.Background())
	if code != exitOK {
		t.Fatalf("exit %d, want 0: %s\n%s", code, f.stderr.String(), f.stdout.String())
	}
	if resumeAt.IsZero() {
		t.Fatal("no run resume went out")
	}
	if resumes != 1 {
		t.Fatalf("%d run resumes, want exactly 1 (no resume during the bad-view window)", resumes)
	}
	badEnd := time.Time{}
	f.agent.mu.Lock()
	for i, line := range f.agent.gm {
		if strings.HasPrefix(line, ".revive ") {
			badEnd = f.agent.gmAt[i]
		}
	}
	f.agent.mu.Unlock()
	gap := resumeAt.Sub(badEnd)
	t.Logf("revive->resume gap %s; bad polls ended at %s, resume at %s", gap, badEndT.Format("15:04:05"), resumeAt.Format("15:04:05"))
	if gap < 90*time.Second {
		t.Fatalf("the resume went out %s after the revive, want a full 90 s quiet window after the bad polls", gap)
	}
	// The window must start AFTER the last bad poll: the resume goes out at least 90 s
	// after the bad window ended. A mutant that counts failed views as quiet builds its
	// window during the bad period and resumes too early.
	if resumeAt.Sub(badEndT) < 90*time.Second {
		t.Fatalf("the resume went out %s after the last bad poll, want a full 90 s of complete views after it", resumeAt.Sub(badEndT))
	}
	if sum := f.summary(); sum.WipeResumes != 1 {
		t.Fatalf("wipe_resumes %d, want 1", sum.WipeResumes)
	}
}

// TestRaidWalkWipeRestCountsWallTime: the rest measures its quiet window on
// the clock, not in poll turns. The fake clock advances only in sleep, so a
// bot_view hook advancing each call by 2.5 s (≈ 10 s per rest turn: pollOnce
// view + quiet view + 5 s sleep, like the live run) doubles every turn's wall
// time. The resume must go out between wipeRest and wipeRest + 30 s after the
// last revive; before the fix the 5 s-per-turn count needed 120 turns ≈ 20 min.
func TestRaidWalkWipeRestCountsWallTime(t *testing.T) {
	f := newFixture(t)
	f.agent.startReply = startAt("onyxias_lair", 10)
	f.agent.unitsFn = func(n int) []objects.Unit { return []objects.Unit{} }
	var resumeAt time.Time
	resumeHook(f, 11, 28, &resumeAt)
	f.allDeadFromHires()
	f.agent.viewHook = func() {
		f.clock.mu.Lock()
		f.clock.t = f.clock.t.Add(2500 * time.Millisecond)
		f.clock.mu.Unlock()
	}
	code := f.run(context.Background())
	if code != exitOK {
		t.Fatalf("exit %d, want 0: %s\n%s", code, f.stderr.String(), f.stdout.String())
	}
	gm := f.agent.gmLines()
	var lastRevive time.Time
	for i, line := range gm {
		if strings.HasPrefix(line, ".revive ") {
			lastRevive = f.agent.gmAt[i]
		}
	}
	if resumeAt.IsZero() {
		t.Fatal("no run resume went out")
	}
	if lastRevive.IsZero() {
		t.Fatal("no revive went out")
	}
	gap := resumeAt.Sub(lastRevive)
	if gap < 10*time.Minute || gap > 10*time.Minute+30*time.Second {
		t.Fatalf("resume %s after the last revive, want between 10m and 10m30s", gap)
	}
	if sum := f.summary(); sum.WipeResumes != 1 {
		t.Fatalf("wipe_resumes %d, want 1", sum.WipeResumes)
	}
}

// selfRegroupArm arms a status line whose delivery kills every hire in that same poll, so the
// poll that reads the status line also reads the all-dead frames — exactly as the live server
// reports a run that is regrouping by itself after a wipe (walk 17b m910q s4, test2 s2: the
// status line and the deaths arrive together). queue also puts the line in the events stream.
func selfRegroupArm(f *fixture, raw string, queue bool) {
	f.agent.mu.Lock()
	if queue {
		f.agent.startQueue = append(f.agent.startQueue, queuedEvent{raw: raw})
	}
	prev := f.agent.eventsHook
	f.agent.eventsHook = func(served string) {
		if prev != nil {
			prev(served)
		}
		if served != raw {
			return
		}
		f.agent.mu.Lock()
		for _, name := range f.agent.party {
			f.agent.dead[name] = true
		}
		f.agent.mu.Unlock()
	}
	f.agent.mu.Unlock()
}

// selfRegroupKill is selfRegroupArm for a line queued right away.
func selfRegroupKill(f *fixture, raw string) { selfRegroupArm(f, raw, true) }

// selfRegroupPair queues two status lines of the same step in ONE events page (pageSize 2) and kills
// every hire when the second line is served: exactly the live order, where "regrouping by itself" is
// followed by "regroup: waiting for X (dead), ... reviving" in the same poll (walk 18 r5).
func selfRegroupPair(f *fixture, first, second string) {
	f.agent.pageSize = 2
	f.agent.mu.Lock()
	f.agent.startQueue = append(f.agent.startQueue, queuedEvent{raw: first}, queuedEvent{raw: second})
	prev := f.agent.eventsHook
	f.agent.eventsHook = func(served string) {
		if prev != nil {
			prev(served)
		}
		if served != second {
			return
		}
		f.agent.mu.Lock()
		for _, name := range f.agent.party {
			f.agent.dead[name] = true
		}
		f.agent.mu.Unlock()
	}
	f.agent.mu.Unlock()
}

// mcWipeFights is the dashboard fights read a wipe lands on: a boss already killed, nothing live.
func mcWipeFights() []walkFightRow {
	return []walkFightRow{{ID: "4-5", Key: "Commander\x001793219890\x004-5", Dur: 1200, Outcome: "kill", BossName: "Lucifron", Leg: "lucifron", Step: 3, Deaths: 1}}
}

// TestRaidWalkWipeSelfRegroupRecoversWithDashboard (walk 17b m910q s4 replay): the run is still
// running when the wipe lands and regroups by itself ("regrouping by itself (1 of 2 at this
// step)"). The walk revives the raid and lets the run go on: no out-of-combat rest, no `run
// resume`, one --wipe-resume spent, and the watch loop runs again. Before the fix the walk sits
// in restOutOfCombat while the run fights on (never 10 min of quiet) and the recovery ends at
// 3 x --wipe-rest as a timeout.
func TestRaidWalkWipeSelfRegroupRecoversWithDashboard(t *testing.T) {
	t.Run("running regroups by itself", func(t *testing.T) {
		f, next, total := bossDoneFixture(t)
		f.opts.wipeResume = 2
		f.agent.startReply = zgStart(total, next)
		step := next - 2
		regroup := fmt.Sprintf("TBM:RUN|molten_core|running|%d/%d|lucifron|move|begin|19 dead (1 wipe, 1 near wipe this run): regrouping by itself (1 of 2 at this step)", step, total)
		f.agent.unitsFn = func(n int) []objects.Unit { return []objects.Unit{} }
		f.opts.dashURL, f.opts.dashPassFile = "http://dash.test", "/tmp/pass"
		f.dash.fights = mcWipeFights()
		selfRegroupKill(f, regroup)
		var reviveAt time.Time
		commandersAtRevive := 0
		f.agent.onGM = func(line string) {
			if !strings.HasPrefix(line, ".revive ") {
				return
			}
			reviveAt = f.clock.now()
			commandersAtRevive = f.dash.commandersCalls()
		}
		resumes := 0
		f.agent.onResume = func() { resumes++ }
		// After its own regroup the run marches on with the raid in combat every second frame:
		// never 10 min of quiet. The done line lands 15 min after the revive.
		f.agent.combatAllFn = func(n int) bool { return !reviveAt.IsZero() && n%2 == 0 }
		doneQueued := false
		f.agent.viewHook = func() {
			if reviveAt.IsZero() || doneQueued || f.clock.now().Sub(reviveAt) < 15*time.Minute {
				return
			}
			doneQueued = true
			f.agent.mu.Lock()
			f.agent.lines = append(f.agent.lines,
				queuedEvent{raw: fmt.Sprintf("TBM:RUN|molten_core|done|%d/%d|lucifron|move|walk|stopped before step %d (until step %d): the party stays where it stands", next, total, next, next)})
			f.agent.mu.Unlock()
		}
		code := f.run(context.Background())
		if code != exitOK {
			t.Fatalf("exit %d, want 0: %s\n%s", code, f.stderr.String(), f.stdout.String())
		}
		sum := f.summary()
		if sum.WipeResumes != 1 {
			t.Fatalf("wipe_resumes %d, want 1 (a self-regroup recovery counts once)", sum.WipeResumes)
		}
		if !hasNote(sum, "the run regroups by itself") {
			t.Fatalf("notes %q, want the self-regroup recovery note", sum.Notes)
		}
		if resumes != 0 {
			t.Fatalf("%d run resumes, want none (the run regroups on its own)", resumes)
		}
		if n := countPrefix(f.agent.gmLines(), ".bot run stop"); n != 0 {
			t.Fatalf("%d run stops, want none (the recovery never stops the run)", n)
		}
		if sum.EndReason != "until" {
			t.Fatalf("end %q, want until (the run went on after the recovery)", sum.EndReason)
		}
		if got := f.dash.commandersCalls(); got <= commandersAtRevive {
			t.Fatalf("dashboard Commanders reads %d, want more than the %d at the last revive (the watch loop runs again)", got, commandersAtRevive)
		}
	})
	// Mutation guard (M14): the self-regroup decision needs the run's state. The same note on a
	// PAUSED status is the old stopped-run shape and keeps the old rest-and-resume recovery.
	t.Run("paused keeps the old rest path", func(t *testing.T) {
		f, next, total := bossDoneFixture(t)
		f.opts.wipeResume = 2
		f.agent.startReply = zgStart(total, next)
		step := next - 2
		regroup := fmt.Sprintf("TBM:RUN|molten_core|paused|%d/%d|lucifron|move|begin|19 dead (1 wipe, 1 near wipe this run): regrouping by itself (1 of 2 at this step)", step, total)
		f.agent.unitsFn = func(n int) []objects.Unit { return []objects.Unit{} }
		f.opts.dashURL, f.opts.dashPassFile = "http://dash.test", "/tmp/pass"
		f.dash.fights = mcWipeFights()
		selfRegroupKill(f, regroup)
		var resumeAt time.Time
		f.agent.onResume = func() {
			resumeAt = f.clock.now()
			f.agent.mu.Lock()
			f.agent.lines = append(f.agent.lines,
				queuedEvent{raw: fmt.Sprintf("TBM:RUN|molten_core|running|%d/%d|lucifron|move|walk|resumed", step+1, total)},
				queuedEvent{raw: fmt.Sprintf("TBM:RUN|molten_core|done|%d/%d|lucifron|move|walk|stopped before step %d (until step %d): the party stays where it stands", next, total, next, next)})
			f.agent.mu.Unlock()
		}
		code := f.run(context.Background())
		if code != exitOK {
			t.Fatalf("exit %d, want 0: %s\n%s", code, f.stderr.String(), f.stdout.String())
		}
		sum := f.summary()
		if sum.WipeResumes != 1 {
			t.Fatalf("wipe_resumes %d, want 1 (the old path recovers too)", sum.WipeResumes)
		}
		if hasNote(sum, "the run regroups by itself") || hasNote(sum, "the run went on by itself") {
			t.Fatalf("notes %q, want the old rest-and-resume recovery (a paused run is no self-regroup)", sum.Notes)
		}
		if resumeAt.IsZero() {
			t.Fatal("no run resume went out: a paused run keeps the old rest and resume")
		}
		if !hasNote(sum, "resumed") {
			t.Fatalf("notes %q, want the rest-and-resume recovery note", sum.Notes)
		}
	})
}

// TestRaidWalkWipeSelfRegroupOrderedLines (walk 18 r5, live replay): TB sends the self-regroup note
// and then "regroup: waiting for X (dead), ... reviving" for the SAME step in one events page, so the
// wipe's last status line is the waiting one, not the note. The walk must still take the self-regroup
// path (revive the raid, no rest, no `run resume`), not the fallback rest-and-resume which sits in the
// rest while the run fights on and times out at 3 x --wipe-rest.
func TestRaidWalkWipeSelfRegroupOrderedLines(t *testing.T) {
	f, next, total := bossDoneFixture(t)
	f.opts.wipeResume = 2
	f.agent.startReply = zgStart(total, next)
	step := next - 2
	regroup := fmt.Sprintf("TBM:RUN|molten_core|running|%d/%d|lucifron|move|begin|19 dead (1 wipe, 1 near wipe this run): regrouping by itself (1 of 2 at this step)", step, total)
	waiting := fmt.Sprintf("TBM:RUN|molten_core|running|%d/%d|lucifron|move|begin|regroup: waiting for Smashy (dead), Gorebrand (dead), reviving", step, total)
	f.agent.unitsFn = func(n int) []objects.Unit { return []objects.Unit{} }
	f.opts.dashURL, f.opts.dashPassFile = "http://dash.test", "/tmp/pass"
	f.dash.fights = mcWipeFights()
	selfRegroupPair(f, regroup, waiting)
	var reviveAt time.Time
	commandersAtRevive := 0
	f.agent.onGM = func(line string) {
		if !strings.HasPrefix(line, ".revive ") {
			return
		}
		reviveAt = f.clock.now()
		commandersAtRevive = f.dash.commandersCalls()
	}
	resumes := 0
	f.agent.onResume = func() { resumes++ }
	// After its own regroup the run marches on with the raid in combat every second frame, so the
	// rest never gets 10 min of quiet; the done line lands 15 min after the revive.
	f.agent.combatAllFn = func(n int) bool { return !reviveAt.IsZero() && n%2 == 0 }
	doneQueued := false
	f.agent.viewHook = func() {
		if reviveAt.IsZero() || doneQueued || f.clock.now().Sub(reviveAt) < 15*time.Minute {
			return
		}
		doneQueued = true
		f.agent.mu.Lock()
		f.agent.lines = append(f.agent.lines,
			queuedEvent{raw: fmt.Sprintf("TBM:RUN|molten_core|done|%d/%d|lucifron|move|walk|stopped before step %d (until step %d): the party stays where it stands", next, total, next, next)})
		f.agent.mu.Unlock()
	}
	code := f.run(context.Background())
	if code != exitOK {
		t.Fatalf("exit %d, want 0: %s\n%s", code, f.stderr.String(), f.stdout.String())
	}
	sum := f.summary()
	if sum.WipeResumes != 1 {
		t.Fatalf("wipe_resumes %d, want 1 (a self-regroup recovery counts once)", sum.WipeResumes)
	}
	if !hasNote(sum, "the run regroups by itself") {
		t.Fatalf("notes %q, want the self-regroup recovery note (the note line is not the last of the poll)", sum.Notes)
	}
	if hasNote(sum, "the run went on by itself") {
		t.Fatalf("notes %q, the fallback rest path ran instead of the self-regroup path", sum.Notes)
	}
	if resumes != 0 {
		t.Fatalf("%d run resumes, want none (the run regroups on its own)", resumes)
	}
	if n := countPrefix(f.agent.gmLines(), ".bot run stop"); n != 0 {
		t.Fatalf("%d run stops, want none (the recovery never stops the run)", n)
	}
	if sum.EndReason != "until" {
		t.Fatalf("end %q, want until (the run went on after the recovery)", sum.EndReason)
	}
	if got := f.dash.commandersCalls(); got <= commandersAtRevive {
		t.Fatalf("dashboard Commanders reads %d, want more than the %d at the last revive (the watch loop runs again)", got, commandersAtRevive)
	}
}

// TestRaidWalkWipeSelfRegroupSameStepRegrouped (walk 19 r6): the wipe's page carries the self-regroup
// note and then the run's own "regrouped" for the SAME step, so the note is not the last status line.
// The self-regroup path must still be taken: revive, no rest, no `run resume`, one --wipe-resume.
func TestRaidWalkWipeSelfRegroupSameStepRegrouped(t *testing.T) {
	wipeSelfRegroupPage(t, func(step, total int) string {
		return fmt.Sprintf("TBM:RUN|molten_core|running|%d/%d|lucifron|move|begin|regrouped", step, total)
	})
}

// TestRaidWalkWipeSelfRegroupAdvancedStep (walk 19 r6): the wipe's page carries the self-regroup note
// and then a running line of the NEXT step — the run going on by itself, which also moves the step the
// tracker fires the wipe on. The note still explains the wipe: the self-regroup path must be taken.
// Before the fix selfRegroup was gated on lastStatus.Step == step, so this page's note was lost,
// runLines was cleared and the walk sat out the fallback rest's 3 x --wipe-rest timeout.
func TestRaidWalkWipeSelfRegroupAdvancedStep(t *testing.T) {
	wipeSelfRegroupPage(t, func(step, total int) string {
		return fmt.Sprintf("TBM:RUN|molten_core|running|%d/%d|lucifron|move|begin|walking", step+1, total)
	})
}

// wipeSelfRegroupPage runs the self-regroup page scenario of the two tests above: a two-line events
// page whose first line is the self-regroup note at the wipe's step and whose second is second(step,
// total), with every hire dying on the second line's delivery. The run then marches on in combat (never
// 10 min of quiet) and a done line lands 15 min after the revive: only the self-regroup path (no rest,
// no `run resume`) can reach the until ending. second's own step decides what the tracker makes of the
// wipe (the run's step when it is the note's, the next one when the line moved it).
func wipeSelfRegroupPage(t *testing.T, second func(step, total int) string) {
	t.Helper()
	f, next, total := bossDoneFixture(t)
	f.opts.wipeResume = 2
	f.agent.startReply = zgStart(total, next)
	step := next - 2
	regroup := fmt.Sprintf("TBM:RUN|molten_core|running|%d/%d|lucifron|move|begin|19 dead (1 wipe, 1 near wipe this run): regrouping by itself (1 of 2 at this step)", step, total)
	f.agent.unitsFn = func(n int) []objects.Unit { return []objects.Unit{} }
	f.opts.dashURL, f.opts.dashPassFile = "http://dash.test", "/tmp/pass"
	f.dash.fights = mcWipeFights()
	selfRegroupPair(f, regroup, second(step, total))
	var reviveAt time.Time
	commandersAtRevive := 0
	f.agent.onGM = func(line string) {
		if !strings.HasPrefix(line, ".revive ") {
			return
		}
		reviveAt = f.clock.now()
		commandersAtRevive = f.dash.commandersCalls()
	}
	resumes := 0
	f.agent.onResume = func() { resumes++ }
	// After its own regroup the run marches on with the raid in combat every second frame: never
	// 10 min of quiet. The done line lands 15 min after the revive.
	f.agent.combatAllFn = func(n int) bool { return !reviveAt.IsZero() && n%2 == 0 }
	doneQueued := false
	f.agent.viewHook = func() {
		if reviveAt.IsZero() || doneQueued || f.clock.now().Sub(reviveAt) < 15*time.Minute {
			return
		}
		doneQueued = true
		f.agent.mu.Lock()
		f.agent.lines = append(f.agent.lines,
			queuedEvent{raw: fmt.Sprintf("TBM:RUN|molten_core|done|%d/%d|lucifron|move|walk|stopped before step %d (until step %d): the party stays where it stands", next, total, next, next)})
		f.agent.mu.Unlock()
	}
	code := f.run(context.Background())
	if code != exitOK {
		t.Fatalf("exit %d, want 0: %s\n%s", code, f.stderr.String(), f.stdout.String())
	}
	sum := f.summary()
	if sum.WipeResumes != 1 {
		t.Fatalf("wipe_resumes %d, want 1 (a self-regroup recovery counts once)", sum.WipeResumes)
	}
	if !hasNote(sum, "the run regroups by itself") {
		t.Fatalf("notes %q, want the self-regroup recovery note (the note line is not the last of the page)", sum.Notes)
	}
	if hasNote(sum, "the run went on by itself") {
		t.Fatalf("notes %q, the fallback rest path ran instead of the self-regroup path", sum.Notes)
	}
	if resumes != 0 {
		t.Fatalf("%d run resumes, want none (the run regroups on its own)", resumes)
	}
	if n := countPrefix(f.agent.gmLines(), ".bot run stop"); n != 0 {
		t.Fatalf("%d run stops, want none (the recovery never stops the run)", n)
	}
	if sum.EndReason != "until" {
		t.Fatalf("end %q, want until (the run went on after the recovery)", sum.EndReason)
	}
	if got := f.dash.commandersCalls(); got <= commandersAtRevive {
		t.Fatalf("dashboard Commanders reads %d, want more than the %d at the last revive (the watch loop runs again)", got, commandersAtRevive)
	}
}

// TestRaidWalkWipeSelfRegroupBudget: a self-regroup recovery counts against --wipe-resume like
// every other one. With two recoveries left, two self-regroup wipes are both recovered; with one
// left the second wipe ends the walk the old way and says why.
//
// The second wipe is scripted only once the first recovery's revives are done and one more poll
// has shown the raid alive again: the tracker's wipe rule latches until it sees the party up, so
// a wipe scripted while the raid is still down would never fire.
func TestRaidWalkWipeSelfRegroupBudget(t *testing.T) {
	for _, tc := range []struct {
		name       string
		wipeResume int
		want       int
	}{
		{"two", 2, 2},
		{"one", 1, 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f, next, total := bossDoneFixture(t)
			f.opts.wipeResume = tc.wipeResume
			f.agent.startReply = zgStart(total, next)
			step := next - 2
			f.agent.unitsFn = func(n int) []objects.Unit { return []objects.Unit{} }
			f.opts.dashURL, f.opts.dashPassFile = "http://dash.test", "/tmp/pass"
			f.dash.fights = mcWipeFights()
			second := fmt.Sprintf("TBM:RUN|molten_core|running|%d/%d|lucifron|move|begin|19 dead (2 wipes, 2 near wipes this run): regrouping by itself (2 of 2 at this step)", step+2, total)
			done := fmt.Sprintf("TBM:RUN|molten_core|done|%d/%d|lucifron|move|walk|stopped before step %d (until step %d): the party stays where it stands", next, total, next, next)
			selfRegroupKill(f, fmt.Sprintf("TBM:RUN|molten_core|running|%d/%d|lucifron|move|begin|19 dead (1 wipe, 1 near wipe this run): regrouping by itself (1 of 2 at this step)", step, total))
			// The second wipe's line is queued once the first recovery is done; its delivery kills.
			selfRegroupArm(f, second, false)
			// Recoveries whose revives have finished (each revives every hire once).
			var revives, recoveries, sinceRecovery, queued int
			var partyN int
			f.agent.onGM = func(line string) {
				if !strings.HasPrefix(line, ".revive ") {
					return
				}
				if partyN == 0 {
					f.agent.mu.Lock()
					partyN = len(f.agent.party)
					f.agent.mu.Unlock()
				}
				if partyN == 0 {
					return
				}
				revives++
				if revives == recoveries*partyN+partyN {
					recoveries++
				}
			}
			f.agent.viewHook = func() {
				if recoveries <= queued {
					return
				}
				// One alive poll after the revives, then the next script line: the tracker must
				// see the party up again before the next wipe (its wipe rule latches otherwise).
				sinceRecovery++
				if sinceRecovery < 2 {
					return
				}
				sinceRecovery = 0
				queued++
				line := done
				if queued == 1 {
					line = second
				}
				f.agent.mu.Lock()
				f.agent.lines = append(f.agent.lines, queuedEvent{raw: line})
				f.agent.mu.Unlock()
			}
			resumes := 0
			f.agent.onResume = func() { resumes++ }
			code := f.run(context.Background())
			sum := f.summary()
			if sum.WipeResumes != tc.want {
				t.Fatalf("wipe_resumes %d, want %d (notes %q, exit %d)", sum.WipeResumes, tc.want, sum.Notes, code)
			}
			if resumes != 0 {
				t.Fatalf("%d run resumes, want none (self-regroup recoveries never resume)", resumes)
			}
			if tc.want == 2 {
				if code != exitOK {
					t.Fatalf("exit %d, want 0: %s\n%s", code, f.stderr.String(), f.stdout.String())
				}
				if sum.EndReason != "until" {
					t.Fatalf("end %q, want until", sum.EndReason)
				}
				if n := countPrefix(f.agent.gmLines(), ".bot run stop"); n != 0 {
					t.Fatalf("%d run stops, want none (both wipes recovered)", n)
				}
				return
			}
			if code != exitRaidWalkIncident {
				t.Fatalf("exit %d, want 19: %s\n%s", code, f.stderr.String(), f.stdout.String())
			}
			if !hasNote(sum, "wipe resume skipped: no recoveries left (1/1)") {
				t.Fatalf("notes %q, want the no-recoveries-left skip", sum.Notes)
			}
			if !hasNote(sum, fmt.Sprintf("wipe at step %d", step+2)) {
				t.Fatalf("notes %q, want the second wipe's ending", sum.Notes)
			}
			if n := countPrefix(f.agent.gmLines(), ".bot run stop"); n != 1 {
				t.Fatalf("%d run stops, want exactly 1 (the second wipe's old ending)", n)
			}
		})
	}
}

// TestRaidWalkWipeSelfRegroupBossEngagedNotes (walk 17b m910q s2 replay): the frames still hold an
// engaged plan boss (a party target, hp below full) when the self-regroup wipe lands, so the walk
// ends the wipe the old way — and says why.
func TestRaidWalkWipeSelfRegroupBossEngagedNotes(t *testing.T) {
	f, next, total := bossDoneFixture(t)
	f.opts.wipeResume = 2
	f.agent.startReply = zgStart(total, next)
	step := next - 2
	// The run is on its boss step and its kind is boss: the tracker's own engagement is absorbed
	// by the run-leader exception, so the wipe is the incident that ends the walk.
	regroup := fmt.Sprintf("TBM:RUN|molten_core|running|%d/%d|lucifron|boss|begin|19 dead (2 wipes, 2 near wipes this run): regrouping by itself (1 of 2 at this step)", step, total)
	f.agent.unitsFn = func(n int) []objects.Unit { return []objects.Unit{} }
	f.opts.dashURL, f.opts.dashPassFile = "http://dash.test", "/tmp/pass"
	f.dash.fights = mcWipeFights()
	f.agent.hostilesFn = func(n int) []tbm.HostileRec {
		return []tbm.HostileRec{{GUID: 9, Entry: 12118, HPPct: 80, Target: packet.GUID(1000)}}
	}
	selfRegroupKill(f, regroup)
	code := f.run(context.Background())
	if code != exitRaidWalkIncident {
		t.Fatalf("exit %d, want 19: %s\n%s", code, f.stderr.String(), f.stdout.String())
	}
	sum := f.summary()
	if sum.WipeResumes != 0 {
		t.Fatalf("wipe_resumes %d, want 0 (a boss engaged into the wipe ends it)", sum.WipeResumes)
	}
	if !hasNote(sum, "wipe resume skipped: boss 12118 engaged") {
		t.Fatalf("notes %q, want the engaged-boss skip note", sum.Notes)
	}
	if !hasNote(sum, fmt.Sprintf("wipe at step %d", step)) {
		t.Fatalf("notes %q, want the wipe ending", sum.Notes)
	}
	stopAt, summonAt := -1, -1
	for i, l := range f.agent.gmLines() {
		if stopAt < 0 && strings.HasPrefix(l, ".bot run stop") {
			stopAt = i
		}
		if summonAt < 0 && (strings.HasPrefix(l, ".revive ") || strings.HasPrefix(l, ".summon ")) {
			summonAt = i
		}
	}
	if stopAt < 0 {
		t.Fatal("no run stop went out")
	}
	if summonAt >= 0 && summonAt < stopAt {
		t.Fatalf("revive/summon at gm line %d before the run stop (%d): the unknown boss was revived into", summonAt, stopAt)
	}
}

// TestRaidWalkWipeResumeSecondWipeDuringRestNotes: a stopped run (no self-regroup note), so the
// recovery rests; halfway through the rest every hire dies again. The walk ends 19 and the second
// wipe leaves a note instead of the bare stop.
func TestRaidWalkWipeResumeSecondWipeDuringRestNotes(t *testing.T) {
	f := newFixture(t)
	f.agent.startReply = startAt("onyxias_lair", 10)
	f.agent.unitsFn = func(n int) []objects.Unit { return []objects.Unit{} }
	f.allDeadFromHires()
	var reviveAt time.Time
	hireHook := f.agent.onGM
	f.agent.onGM = func(line string) {
		if hireHook != nil {
			hireHook(line)
		}
		if strings.HasPrefix(line, ".revive ") {
			reviveAt = f.clock.now()
		}
	}
	killed := false
	f.agent.viewHook = func() {
		if killed || reviveAt.IsZero() || f.clock.now().Sub(reviveAt) < 5*time.Minute {
			return
		}
		killed = true
		f.agent.mu.Lock()
		for _, name := range f.agent.party {
			f.agent.dead[name] = true
		}
		f.agent.mu.Unlock()
	}
	code := f.run(context.Background())
	if code != exitRaidWalkIncident {
		t.Fatalf("exit %d, want 19: %s\n%s", code, f.stderr.String(), f.stdout.String())
	}
	if sum := f.summary(); !hasNote(sum, "second wipe at step 10 during the wipe-resume of step 10") {
		t.Fatalf("notes %q, want the second-wipe note (the rest's second wipe)", sum.Notes)
	}
}

// TestRaidWalkWipeResumeRunWentOnDuringRest: a stopped run, so the recovery rests, but a status
// the rest reads says the run regrouped and went on. The walk skips the resume, counts the
// recovery and lets the run be.
func TestRaidWalkWipeResumeRunWentOnDuringRest(t *testing.T) {
	f := newFixture(t)
	f.agent.startReply = startAt("onyxias_lair", 10)
	f.agent.unitsFn = func(n int) []objects.Unit { return []objects.Unit{} }
	f.allDeadFromHires()
	var reviveAt time.Time
	hireHook := f.agent.onGM
	f.agent.onGM = func(line string) {
		if hireHook != nil {
			hireHook(line)
		}
		if strings.HasPrefix(line, ".revive ") {
			reviveAt = f.clock.now()
		}
	}
	// Two rest turns after the revive the run's own regroup is done and it walks on.
	views, queued := 0, false
	f.agent.viewHook = func() {
		if reviveAt.IsZero() || queued {
			return
		}
		views++
		if views < 4 {
			return
		}
		queued = true
		f.agent.mu.Lock()
		f.agent.lines = append(f.agent.lines,
			queuedEvent{raw: "TBM:RUN|onyxias_lair|running|11/29|onyxia|move|walk|regrouped"},
			queuedEvent{raw: "TBM:RUN|onyxias_lair|done|28/29|onyxia|move|walk|stopped before step 29 (until boss): the party stays where it stands"})
		f.agent.mu.Unlock()
	}
	resumes := 0
	f.agent.onResume = func() {
		resumes++
		f.agent.push("TBM:RUN|onyxias_lair|running|12/29|onyxia|move|walk|resumed", "")
		f.agent.push("TBM:RUN|onyxias_lair|done|28/29|onyxia|move|walk|stopped before step 29 (until boss): the party stays where it stands", "")
	}
	code := f.run(context.Background())
	if code != exitOK {
		t.Fatalf("exit %d, want 0: %s\n%s", code, f.stderr.String(), f.stdout.String())
	}
	sum := f.summary()
	if sum.WipeResumes != 1 {
		t.Fatalf("wipe_resumes %d, want 1 (the run going on still counts as recovered)", sum.WipeResumes)
	}
	if !hasNote(sum, "the run went on by itself") {
		t.Fatalf("notes %q, want the went-on recovery note", sum.Notes)
	}
	if resumes != 0 {
		t.Fatalf("%d run resumes, want none (the run resumed itself)", resumes)
	}
	if sum.EndReason != "until" {
		t.Fatalf("end %q, want until", sum.EndReason)
	}
}

// TestRaidWalkWipeResumeRunWentOnMultipleLines (walk 18 r5): the run's own regroup finishes with a
// "regrouped" status that is NOT the last line of its events page (a later line of the same step
// follows). The rest must still see it and stop: the recovery counts and no `run resume` goes out.
// Before the fix only the page's last line was read, so the rest ran on to the resume.
func TestRaidWalkWipeResumeRunWentOnMultipleLines(t *testing.T) {
	f := newFixture(t)
	f.agent.startReply = startAt("onyxias_lair", 10)
	f.agent.unitsFn = func(n int) []objects.Unit { return []objects.Unit{} }
	f.allDeadFromHires()
	var reviveAt time.Time
	hireHook := f.agent.onGM
	f.agent.onGM = func(line string) {
		if hireHook != nil {
			hireHook(line)
		}
		if strings.HasPrefix(line, ".revive ") {
			reviveAt = f.clock.now()
		}
	}
	f.agent.pageSize = 2
	views, queued := 0, false
	f.agent.viewHook = func() {
		if reviveAt.IsZero() || queued {
			return
		}
		views++
		if views < 4 {
			return
		}
		queued = true
		f.agent.mu.Lock()
		f.agent.lines = append(f.agent.lines,
			queuedEvent{raw: "TBM:RUN|onyxias_lair|running|10/29|onyxia|move|walk|regrouped"},
			queuedEvent{raw: "TBM:RUN|onyxias_lair|running|10/29|onyxia|move|walk|walking"},
			queuedEvent{raw: "TBM:RUN|onyxias_lair|done|28/29|onyxia|move|walk|stopped before step 29 (until boss): the party stays where it stands"})
		f.agent.mu.Unlock()
	}
	resumes := 0
	f.agent.onResume = func() { resumes++ }
	code := f.run(context.Background())
	if code != exitOK {
		t.Fatalf("exit %d, want 0: %s\n%s", code, f.stderr.String(), f.stdout.String())
	}
	sum := f.summary()
	if sum.WipeResumes != 1 {
		t.Fatalf("wipe_resumes %d, want 1", sum.WipeResumes)
	}
	if !hasNote(sum, "the run went on by itself") {
		t.Fatalf("notes %q, want the went-on recovery note (the page's last line is no regroup line)", sum.Notes)
	}
	if resumes != 0 {
		t.Fatalf("%d run resumes, want none (the run resumed itself)", resumes)
	}
	if sum.EndReason != "until" {
		t.Fatalf("end %q, want until", sum.EndReason)
	}
}

// TestRaidWalkWipeResumeRunWentOnLaterStep (walk 18 r5): the rest reads a running status on a LATER
// step whose note does not start with "regrouped". The rest still ends and the recovery counts; the
// `Step > step` half of the guard is what fires here.
func TestRaidWalkWipeResumeRunWentOnLaterStep(t *testing.T) {
	f := newFixture(t)
	f.agent.startReply = startAt("onyxias_lair", 10)
	f.agent.unitsFn = func(n int) []objects.Unit { return []objects.Unit{} }
	f.allDeadFromHires()
	var reviveAt time.Time
	hireHook := f.agent.onGM
	f.agent.onGM = func(line string) {
		if hireHook != nil {
			hireHook(line)
		}
		if strings.HasPrefix(line, ".revive ") {
			reviveAt = f.clock.now()
		}
	}
	views, queued := 0, false
	f.agent.viewHook = func() {
		if reviveAt.IsZero() || queued {
			return
		}
		views++
		if views < 4 {
			return
		}
		queued = true
		f.agent.mu.Lock()
		f.agent.lines = append(f.agent.lines,
			queuedEvent{raw: "TBM:RUN|onyxias_lair|running|11/29|onyxia|move|walk|walking"},
			queuedEvent{raw: "TBM:RUN|onyxias_lair|done|28/29|onyxia|move|walk|stopped before step 29 (until boss): the party stays where it stands"})
		f.agent.mu.Unlock()
	}
	resumes := 0
	f.agent.onResume = func() { resumes++ }
	code := f.run(context.Background())
	if code != exitOK {
		t.Fatalf("exit %d, want 0: %s\n%s", code, f.stderr.String(), f.stdout.String())
	}
	sum := f.summary()
	if sum.WipeResumes != 1 {
		t.Fatalf("wipe_resumes %d, want 1", sum.WipeResumes)
	}
	if !hasNote(sum, "the run went on by itself") {
		t.Fatalf("notes %q, want the went-on recovery note (a later step with no regroup note)", sum.Notes)
	}
	if resumes != 0 {
		t.Fatalf("%d run resumes, want none (the run went on by itself)", resumes)
	}
	if sum.EndReason != "until" {
		t.Fatalf("end %q, want until", sum.EndReason)
	}
}

// TestRaidWalkWipeResumeRunWentOnBeforeNearWipe (walk 19 r6): the run's own "regrouped" line was read
// BEFORE the near-wipe pause, so the rest never saw it (the guard reads only lines received after the
// rest began, seq0). A pre-rest line must not stop the rest: the near-wipe recovery rests and resumes
// normally. Pins runWentOnAfter's `seq <= seq0` stop.
func TestRaidWalkWipeResumeRunWentOnBeforeNearWipe(t *testing.T) {
	f := newFixture(t)
	f.agent.startReply = startAt("onyxias_lair", 10)
	f.agent.unitsFn = func(n int) []objects.Unit { return []objects.Unit{} }
	var resumeAt time.Time
	resumeHook(f, 11, 28, &resumeAt)
	f.agent.mu.Lock()
	f.agent.startQueue = []queuedEvent{
		{raw: "TBM:RUN|onyxias_lair|running|10/29|onyxia|move|walk|regrouped"},
		{raw: "TBM:RUN|onyxias_lair|paused|10/29|onyxia|move|walk|19 dead: 3 near wipes within 10 min (54): the run stops here. Regroup (c...)"},
	}
	f.agent.mu.Unlock()
	code := f.run(context.Background())
	if code != exitOK {
		t.Fatalf("exit %d, want 0: %s\n%s", code, f.stderr.String(), f.stdout.String())
	}
	if resumeAt.IsZero() {
		t.Fatal("no run resume went out: a pre-rest regroup line must not stop the rest")
	}
	sum := f.summary()
	if sum.WipeResumes != 1 {
		t.Fatalf("wipe_resumes %d, want 1", sum.WipeResumes)
	}
	if hasNote(sum, "the run went on by itself") {
		t.Fatalf("notes %q, the rest saw a regroup line read before it began", sum.Notes)
	}
	if !hasNote(sum, "resumed") {
		t.Fatalf("notes %q, want the rest-and-resume recovery note", sum.Notes)
	}
	if n := countPrefix(f.agent.gmLines(), ".bot run stop"); n != 0 {
		t.Fatalf("%d run stops, want none (the near-wipe recovery never stops the run)", n)
	}
}

// TestRaidWalkWipeResumeStaleSelfRegroupNote (walk 19 r6): a self-regroup note at the wipe's own step
// that is older than selfRegroupWindow (30 s) must not explain the wipe, even when it is the last
// status line: the run stopped regrouping long ago, so the recovery rests and resumes as usual. Before
// the fix selfRegroupNote(lastStatus.Note) ignored the window, so this wipe took the self-regroup path
// and no resume ever went out.
func TestRaidWalkWipeResumeStaleSelfRegroupNote(t *testing.T) {
	f := newFixture(t)
	f.agent.startReply = startAt("onyxias_lair", 10)
	f.agent.unitsFn = func(n int) []objects.Unit { return []objects.Unit{} }
	var resumeAt time.Time
	resumeHook(f, 11, 28, &resumeAt)
	f.agent.mu.Lock()
	f.agent.startQueue = []queuedEvent{
		{raw: "TBM:RUN|onyxias_lair|running|10/29|onyxia|move|walk|19 dead (1 wipe, 1 near wipe this run): regrouping by itself (1 of 2 at this step)"},
	}
	f.agent.mu.Unlock()
	// The wipe lands 45 s after the note: the run's frames die once the window has passed (once: the
	// recovery's own revives must bring the raid back for the rest).
	var noteAt time.Time
	killed := false
	f.agent.eventsHook = func(served string) {
		if strings.Contains(served, "regrouping by itself") {
			noteAt = f.clock.now()
		}
	}
	f.agent.viewHook = func() {
		if killed || noteAt.IsZero() || f.clock.now().Sub(noteAt) < 45*time.Second {
			return
		}
		killed = true
		f.agent.mu.Lock()
		for _, name := range f.agent.party {
			f.agent.dead[name] = true
		}
		f.agent.mu.Unlock()
	}
	code := f.run(context.Background())
	if code != exitOK {
		t.Fatalf("exit %d, want 0: %s\n%s", code, f.stderr.String(), f.stdout.String())
	}
	if resumeAt.IsZero() {
		t.Fatal("no run resume went out (a note older than the window is no self-regroup)")
	}
	sum := f.summary()
	if sum.WipeResumes != 1 {
		t.Fatalf("wipe_resumes %d, want 1", sum.WipeResumes)
	}
	if hasNote(sum, "the run regroups by itself") {
		t.Fatalf("notes %q, a > 30 s old note decided the wipe", sum.Notes)
	}
	if !hasNote(sum, "resumed") {
		t.Fatalf("notes %q, want the rest-and-resume recovery note", sum.Notes)
	}
}

// TestRaidWalkWipeResumeNoteAtPreviousStep (walk 19 r6): the self-regroup note belongs to the step
// BEFORE the wipe (a page of its own) and is still fresh, but the run stands on the wipe's step now:
// the note does not explain this wipe, so the recovery rests and resumes. This is the step half of the
// self-regroup match — a note whose own page carried the run on to the wipe's step IS a self-regroup
// (TestRaidWalkWipeSelfRegroupAdvancedStep).
func TestRaidWalkWipeResumeNoteAtPreviousStep(t *testing.T) {
	f := newFixture(t)
	f.agent.startReply = startAt("onyxias_lair", 9)
	f.agent.unitsFn = func(n int) []objects.Unit { return []objects.Unit{} }
	var resumeAt time.Time
	resumeHook(f, 11, 28, &resumeAt)
	note := "TBM:RUN|onyxias_lair|running|9/29|onyxia|move|walk|19 dead (1 wipe, 1 near wipe this run): regrouping by itself (1 of 2 at this step)"
	walk := "TBM:RUN|onyxias_lair|running|10/29|onyxia|move|walk|walking"
	f.agent.mu.Lock()
	f.agent.startQueue = append(f.agent.startQueue, queuedEvent{raw: note}, queuedEvent{raw: walk})
	// One line per events page (pageSize 0), so the note's page is not the wipe's page. The wipe lands
	// on the page that reads the next step's line, within the window.
	prev := f.agent.eventsHook
	f.agent.eventsHook = func(served string) {
		if prev != nil {
			prev(served)
		}
		if served != walk {
			return
		}
		f.agent.mu.Lock()
		for _, name := range f.agent.party {
			f.agent.dead[name] = true
		}
		f.agent.mu.Unlock()
	}
	f.agent.mu.Unlock()
	code := f.run(context.Background())
	if code != exitOK {
		t.Fatalf("exit %d, want 0: %s\n%s", code, f.stderr.String(), f.stdout.String())
	}
	if resumeAt.IsZero() {
		t.Fatal("no run resume went out (a note of the previous step is no self-regroup)")
	}
	sum := f.summary()
	if sum.WipeResumes != 1 {
		t.Fatalf("wipe_resumes %d, want 1", sum.WipeResumes)
	}
	if hasNote(sum, "the run regroups by itself") {
		t.Fatalf("notes %q, a note of the previous step decided the wipe", sum.Notes)
	}
	if !hasNote(sum, "resumed") {
		t.Fatalf("notes %q, want the rest-and-resume recovery note", sum.Notes)
	}
}

// TestRaidWalkWipeResumeSecondWipeSameStep (walk 19 r6): a second wipe at the SAME step right after the
// first self-regroup recovery — with no status line of its own — must not inherit the first wipe's
// note: handleWipe clears runLines, so a note explains one wipe only. Without that clear the second
// wipe looks like a self-regroup, no resume ever goes out and the walk runs to its ceiling.
func TestRaidWalkWipeResumeSecondWipeSameStep(t *testing.T) {
	// A ten-man raid (the smallest roster a raid plan has) keeps the recovery's per-hire waits small.
	f := newFixture(t)
	f.opts.plan, f.opts.holder = "lower_karazhan_halls", "raid-walk-lower_karazhan_halls"
	p, err := dungeon.Load(filepath.Join(f.opts.dir, "lower_karazhan_halls.json"))
	if err != nil {
		t.Fatal(err)
	}
	f.agent.instMap = p.Map
	f.opts.tier = 1
	f.opts.wipeResume = 2
	// The recovery's fixed settle sleeps (10 s after the summons, 15 s after the revives) would put the
	// second wipe past the 30 s window a self-regroup note can explain: even a ten-man recovery is 27 s,
	// and the second wipe needs the drive's two polls past it (31 s). Freeze those two for this test;
	// the 2 s polls, the per-hire waits, the rest's quiet window and the 30 s window stay untouched.
	prevSleep := f.env.sleep
	f.env.sleep = func(ctx context.Context, d time.Duration) error {
		if d == 10*time.Second || d == 15*time.Second {
			d = 0
		}
		return prevSleep(ctx, d)
	}
	const step, total = 3, 6
	f.agent.startReply = startAt("lower_karazhan_halls", step)
	f.agent.unitsFn = func(n int) []objects.Unit { return []objects.Unit{} }
	f.opts.dashURL, f.opts.dashPassFile = "http://dash.test", "/tmp/pass"
	f.dash.fights = mcWipeFights()
	selfRegroupKill(f, fmt.Sprintf("TBM:RUN|lower_karazhan_halls|running|%d/%d|rend|move|begin|19 dead (1 wipe, 1 near wipe this run): regrouping by itself (1 of 2 at this step)", step, total))
	partyN := 0
	revived := 0
	f.agent.onGM = func(line string) {
		if !strings.HasPrefix(line, ".revive ") {
			return
		}
		if partyN == 0 {
			f.agent.mu.Lock()
			partyN = len(f.agent.party)
			f.agent.mu.Unlock()
		}
		revived++
	}
	var resumeAt time.Time
	f.agent.onResume = func() {
		resumeAt = f.clock.now()
		f.agent.mu.Lock()
		f.agent.lines = append(f.agent.lines,
			queuedEvent{raw: fmt.Sprintf("TBM:RUN|lower_karazhan_halls|running|%d/%d|rend|move|walk|resumed", step+1, total)},
			queuedEvent{raw: fmt.Sprintf("TBM:RUN|lower_karazhan_halls|done|%d/%d|rend|move|walk|stopped before step %d (until step %d): the party stays where it stands", step+1, total, step+1, step+1)})
		f.agent.mu.Unlock()
	}
	// The frames after the last .revive come in this order: the recovery's own post-revive view, then
	// the watch loop's first poll (the alive frame that clears the tracker's wipe latch), then the poll
	// whose frames die — the second wipe, still inside the first note's 30 s window.
	views := 0
	f.agent.viewHook = func() {
		if partyN == 0 || revived < partyN {
			return
		}
		views++
		if views != 3 {
			return
		}
		f.agent.mu.Lock()
		for _, name := range f.agent.party {
			f.agent.dead[name] = true
		}
		f.agent.mu.Unlock()
	}
	code := f.run(context.Background())
	if code != exitOK {
		t.Fatalf("exit %d, want 0: %s\n%s\nnotes %q", code, f.stderr.String(), f.stdout.String(), f.summary().Notes)
	}
	if resumeAt.IsZero() {
		t.Fatal("no run resume went out (the second wipe must not reuse the first wipe's note)")
	}
	sum := f.summary()
	if sum.WipeResumes != 2 {
		t.Fatalf("wipe_resumes %d, want 2 (notes %q)", sum.WipeResumes, sum.Notes)
	}
	if !hasNote(sum, "the run regroups by itself") {
		t.Fatalf("notes %q, want the first wipe's self-regroup recovery note", sum.Notes)
	}
	if !hasNote(sum, "resumed") {
		t.Fatalf("notes %q, want the second wipe's rest-and-resume recovery note", sum.Notes)
	}
}

// TestRaidWalkWipeResumeDeadline (review 3): combat recurs every 4 min with --wipe-rest 5m, so
// the deadline (recovery start + 15m) hits before the rest ever completes. The walk ends with
// the wipe's exit 19 and the timed-out note, before the walk ceiling.
func TestRaidWalkWipeResumeDeadline(t *testing.T) {
	f := newFixture(t)
	f.agent.startReply = startAt("onyxias_lair", 10)
	f.agent.unitsFn = func(n int) []objects.Unit { return []objects.Unit{} }
	f.opts.wipeRest = 5 * time.Minute
	f.allDeadFromHires()
	f.agent.combatAllFn = func(n int) bool {
		return n >= 30 && (n-30)%120 < 3
	}
	code := f.run(context.Background())
	if code != exitRaidWalkIncident {
		t.Fatalf("exit %d, want 19: %s\n%s", code, f.stderr.String(), f.stdout.String())
	}
	sum := f.summary()
	if sum.WipeResumes != 0 {
		t.Fatalf("wipe_resumes %d, want 0 (the deadline hit before any resume)", sum.WipeResumes)
	}
	if !hasNote(sum, "wipe recovery timed out after 15m") {
		t.Fatalf("notes %q, want the timed-out note", sum.Notes)
	}
	if !strings.Contains(sum.EndReason, "wipe at step") {
		t.Fatalf("end %q, want the original wipe's ending", sum.EndReason)
	}
}

// TestRaidWalkWipeResumeDeadlineDuringRevive (review 3 MEDIUM): revive's quiet check never
// gets quiet with --wipe-rest 1m, so the ENTRY deadline (start + 3m) bounds the revive
// itself. The walk ends 19 with the timed-out note and never sends `run resume`.
func TestRaidWalkWipeResumeDeadlineDuringRevive(t *testing.T) {
	f := newFixture(t)
	f.agent.startReply = startAt("onyxias_lair", 10)
	f.agent.unitsFn = func(n int) []objects.Unit {
		f.agent.mu.Lock()
		x, y, z := f.agent.x, f.agent.y, f.agent.z
		f.agent.mu.Unlock()
		return []objects.Unit{{Type: "creature", Name: "Onyxian Warder", Hostile: true, Moving: true, X: x + 60, Y: y, Z: z}}
	}
	f.opts.wipeRest = time.Minute
	f.allDeadFromHires()
	// The recovery starts at its first `.go xyz` after run start (the revive
	// point, before the quiet check burns fake time): the deadline is entry + 3m.
	var recStart time.Time
	hireHook := f.agent.onGM
	f.agent.onGM = func(line string) {
		if hireHook != nil {
			hireHook(line)
		}
		if recStart.IsZero() && strings.HasPrefix(line, ".go xyz") {
			f.agent.mu.Lock()
			started := f.agent.runStarted
			f.agent.mu.Unlock()
			if started {
				recStart = f.clock.now()
			}
		}
	}
	resumes := 0
	f.agent.onResume = func() { resumes++ }
	code := f.run(context.Background())
	if code != exitRaidWalkIncident {
		t.Fatalf("exit %d, want 19: %s\n%s", code, f.stderr.String(), f.stdout.String())
	}
	if resumes != 0 {
		t.Fatalf("%d run resumes, want none (the deadline hit during the revive)", resumes)
	}
	if recStart.IsZero() {
		t.Fatal("the recovery never teleported to the revive point")
	}
	if elapsed := f.clock.now().Sub(recStart); elapsed > 3*time.Minute {
		t.Fatalf("the recovery ran %s past entry, past the entry+3m deadline", elapsed)
	}
	sum := f.summary()
	if sum.WipeResumes != 0 {
		t.Fatalf("wipe_resumes %d, want 0 (the deadline hit before any resume)", sum.WipeResumes)
	}
	if !hasNote(sum, "wipe recovery timed out after 3m") {
		t.Fatalf("notes %q, want the timed-out note", sum.Notes)
	}
	if !strings.Contains(sum.EndReason, "wipe at step") {
		t.Fatalf("end %q, want the original wipe's ending", sum.EndReason)
	}
}

func TestRaidWalkLayoutMainTankMismatchFails(t *testing.T) {
	f := newFixture(t)
	f.agent.bindLayout = "TBM:RAID_LAYOUT|39|Somebody,Bot02|Bot01|Bot02|"
	code := f.run(context.Background())
	if code != exitRaidWalkFailed {
		t.Fatalf("exit %d, want 18: %s", code, f.stderr.String())
	}
	if !strings.Contains(f.summary().EndReason, "main tank is Somebody") {
		t.Fatalf("summary reason %q", f.summary().EndReason)
	}
	if len(f.rec.teardown) == 0 {
		t.Fatal("no teardown after the layout refusal")
	}
}

// Walk 12: the enter order came while late joiners were still on their join summon and the walk stopped.
func TestRaidWalkEnterRetriesABusyHire(t *testing.T) {
	f := newFixture(t)
	f.defaultRun("onyxias_lair")
	f.agent.enterBusy = 2
	if code := f.run(context.Background()); code != exitOK {
		t.Fatalf("exit %d: %s", code, f.stderr.String())
	}
	if f.agent.enterCalls != 3 {
		t.Fatalf("enter sent %d times, want 3", f.agent.enterCalls)
	}
}

func TestRaidWalkEnterGivesUpOnAHireBusyTooLong(t *testing.T) {
	f := newFixture(t)
	f.agent.enterBusy = 100
	code := f.run(context.Background())
	if code != exitRaidWalkFailed {
		t.Fatalf("exit %d, want 18: %s", code, f.stderr.String())
	}
	if f.agent.enterCalls != enterBusyTries || !strings.Contains(f.summary().EndReason, "busy") {
		t.Fatalf("enter sent %d times, reason %q", f.agent.enterCalls, f.summary().EndReason)
	}
}

// Walk 12: the walk bound the raid while the last hires were still on their join summon, and the server removed
// every hire that joined after the bind. The refit (and the bind after it) waits hireSettleWait after the last hire.

// Walk 12 s4: the rest batch's first reply is partial (dps short by 8); the retry hires them and the walk
// exits 0 with the full roster. The retry asks for exactly the shortfall ("8d").
func TestRaidWalkRetriesAPartialHire(t *testing.T) {
	f := newFixture(t)
	f.agent.hireShort = map[int]int{2: 8}
	f.defaultRun("onyxias_lair")
	if code := f.run(context.Background()); code != exitOK {
		t.Fatalf("exit %d, want 0: %s", code, f.stderr.String())
	}
	sum := f.summary()
	// The roster totals 40 and the server cap owns the 40th seat (amendment G2): the retry refills the
	// short batch, and the party holds 39 with the cap note.
	if len(f.agent.party) != raidwalk.HireCap {
		t.Fatalf("party holds %d bots, want the capped roster of %d", len(f.agent.party), raidwalk.HireCap)
	}
	if !hasNote(sum, "hire cap 39: asked for 40 bots, the server hired 39") {
		t.Fatalf("summary notes %q, want the hire cap note", sum.Notes)
	}
	var hires []string
	for _, line := range f.agent.gmLines() {
		if strings.HasPrefix(line, ".bot hire raid ") {
			hires = append(hires, line)
		}
	}
	if len(hires) != 4 {
		t.Fatalf("hire lines %q, want 4 (3 batches + 1 retry)", hires)
	}
	if !strings.HasPrefix(hires[3], ".bot hire raid 8d level=60 t2") {
		t.Fatalf("retry line %q, want it to ask for \"8d\"", hires[3])
	}
	if !hasNote(sum, "hire 10h 26d: 28 of 36 hired (partial); retry 1 in 30 s for 8d") {
		t.Fatalf("summary notes %q, want the retry note", sum.Notes)
	}
}

// The rest batch's first reply is partial; the retry's reply counts the requested order while the server
// hires only to the cap: the last seat fills, the join wait stays cap-aware, and the walk exits 0 with
// the 40th seat's cap note and 39 hires.
func TestRaidWalkCappedRetryReportsTheOrder(t *testing.T) {
	f := newFixture(t)
	f.agent.hireShort = map[int]int{2: 8}
	f.agent.hireRetryReportsOrder = true
	f.defaultRun("onyxias_lair")
	if code := f.run(context.Background()); code != exitOK {
		t.Fatalf("exit %d, want 0: %s", code, f.stderr.String())
	}
	sum := f.summary()
	if len(f.agent.party) != raidwalk.HireCap {
		t.Fatalf("party holds %d bots, want the capped roster of %d", len(f.agent.party), raidwalk.HireCap)
	}
	if !hasNote(sum, "hire cap 39: asked for 40 bots, the server hired 39") {
		t.Fatalf("summary notes %q, want the hire cap note", sum.Notes)
	}
}

// The retry comes up short three times: exit 18 with "after 3 retries" and exactly 4 hire lines for the batch.
func TestRaidWalkPartialHireGivesUpAfter3Retries(t *testing.T) {
	f := newFixture(t)
	f.agent.hireShort = map[int]int{1: 1}
	f.agent.hireShortAlways = true
	f.defaultRun("onyxias_lair")
	code := f.run(context.Background())
	if code != exitRaidWalkFailed {
		t.Fatalf("exit %d, want 18: %s", code, f.stderr.String())
	}
	if !strings.Contains(f.summary().EndReason, "after 3 retries") {
		t.Fatalf("end reason %q, want it to name the retries", f.summary().EndReason)
	}
	n := 0
	for _, line := range f.agent.gmLines() {
		if strings.HasPrefix(line, ".bot hire raid 1t") || strings.HasPrefix(line, ".bot hire raid 3t") {
			n++
		}
	}
	if n != 4 {
		t.Fatalf("%d hire lines for the short batch, want 4 (1 + 3 retries)", n)
	}
}

// An mt batch ("1 warrior:tank") that answers partial or refused is not retried: exit 18, 1 hire line.
func TestRaidWalkNeverRetriesAClassBatch(t *testing.T) {
	for _, reply := range []string{
		"TBM:HIRE_RAID|partial|0|1|0|0|0|2|0|No mercenary of that kind is available right now. Try again shortly.",
		"TBM:HIRE_RAID|refused|0|1|0|0|0|2|0|No mercenary of that kind is available right now. Try again shortly.",
	} {
		f := newFixture(t)
		f.defaultRun("onyxias_lair")
		f.agent.hireReply = map[int]string{0: reply}
		code := f.run(context.Background())
		if code != exitRaidWalkFailed {
			t.Fatalf("reply %q: exit %d, want 18", reply, code)
		}
		n := 0
		for _, line := range f.agent.gmLines() {
			if strings.HasPrefix(line, ".bot hire raid ") {
				n++
			}
		}
		if n != 1 {
			t.Fatalf("reply %q: %d hire lines, want 1 (no retry)", reply, n)
		}
	}
}

// parseHireReply reads the walk 12 s4 line field by field.
func TestParseHireReplyWalk12(t *testing.T) {
	r := parseHireReply("TBM:HIRE_RAID|partial|9|17|0|5|4|1|0|No mercenary of that kind is available right now. Try again shortly.")
	if r.status != "partial" || r.hired != 9 || r.planned != 17 || r.tank != 0 || r.healer != 5 || r.dps != 4 {
		t.Fatalf("parsed %+v, want partial 9 17 0 5 4", r)
	}
	if r.reason != "No mercenary of that kind is available right now. Try again shortly." {
		t.Fatalf("reason %q", r.reason)
	}
}

func TestRaidWalkWaitsForHiresToSettle(t *testing.T) {
	f := newFixture(t)
	f.defaultRun("onyxias_lair")
	if code := f.run(context.Background()); code != exitOK {
		t.Fatalf("exit %d: %s", code, f.stderr.String())
	}
	var lastHire, gear time.Time
	for i, line := range f.agent.gm {
		switch {
		case strings.HasPrefix(line, ".bot hire raid "):
			lastHire = f.agent.gmAt[i]
		case strings.HasPrefix(line, ".bot gear raid "):
			gear = f.agent.gmAt[i]
		}
	}
	if gear.IsZero() || gear.Sub(lastHire) < hireSettleWait {
		t.Fatalf("the refit went out %s after the last hire, want at least %s", gear.Sub(lastHire), hireSettleWait)
	}
}

func TestRaidWalkDashboardOffStillWalks(t *testing.T) {
	f := newFixture(t)
	f.defaultRun("onyxias_lair")
	if code := f.run(context.Background()); code != exitOK {
		t.Fatalf("exit %d: %s", code, f.stderr.String())
	}
	if sum := f.summary(); sum.Dashboard != "off (no --dash-url/--dash-pass-file)" {
		t.Fatalf("dashboard %q", sum.Dashboard)
	}
}

func TestRaidWalkSavesFightsOncePerKey(t *testing.T) {
	f := newFixture(t)
	f.opts.dashURL, f.opts.dashPassFile = "http://dash.test", "/tmp/pass"
	f.dash.fights = []walkFightRow{{ID: "4-5", Key: "Commander\x001793219890\x004-5", Dur: 1200, Outcome: "win", BossName: "Onyxia", Leg: "onyxia", Step: 3, Deaths: 1}}
	f.defaultRun("onyxias_lair")
	if code := f.run(context.Background()); code != exitOK {
		t.Fatalf("exit %d: %s", code, f.stderr.String())
	}
	if sum := f.summary(); sum.Dashboard != "on" || len(sum.Fights) != 1 {
		t.Fatalf("dashboard %q fights %+v", sum.Dashboard, sum.Fights)
	}
	for _, name := range []string{"combat-fights.json", "combat-fight-4-5.json"} {
		if _, err := os.Stat(filepath.Join(f.out, name)); err != nil {
			t.Fatalf("%s: %v", name, err)
		}
	}
	if len(f.dash.fightKeys) != 1 || f.dash.fightKeys[0] != "Commander\x001793219890\x004-5" {
		t.Fatalf("fight details fetched %q", f.dash.fightKeys)
	}
	matches, _ := filepath.Glob(filepath.Join(f.out, "commanders-0001-walk.json"))
	if len(matches) != 1 {
		t.Fatalf("no commanders snapshot: %v", matches)
	}
}

// TestRaidWalkSkipsLiveFightRows: a fight still running (live row, no detail yet) is not taken as seen, so the finished
// fight under the same id is fetched and digested once it is filed.
func TestRaidWalkSkipsLiveFightRows(t *testing.T) {
	f := newFixture(t)
	f.opts.dashURL, f.opts.dashPassFile = "http://dash.test", "/tmp/pass"
	f.dash.fights = []walkFightRow{{ID: "4-5", Key: "k", Live: true}}
	f.defaultRun("onyxias_lair")
	if code := f.run(context.Background()); code != exitOK {
		t.Fatalf("exit %d: %s", code, f.stderr.String())
	}
	if sum := f.summary(); len(sum.Fights) != 0 || len(f.dash.fightKeys) != 0 {
		t.Fatalf("a live row was taken: fights %+v keys %q", sum.Fights, f.dash.fightKeys)
	}
	f = newFixture(t)
	f.opts.dashURL, f.opts.dashPassFile = "http://dash.test", "/tmp/pass"
	f.dash.fights = []walkFightRow{{ID: "4-5", Key: "k", Dur: 1200, Outcome: "win", Step: 3, Deaths: 1, Warnings: 2}}
	f.defaultRun("onyxias_lair")
	if code := f.run(context.Background()); code != exitOK {
		t.Fatalf("exit %d: %s", code, f.stderr.String())
	}
	sum := f.summary()
	if len(sum.Fights) != 1 || sum.Fights[0].Warnings != 2 || sum.Fights[0].Detail == nil {
		t.Fatalf("fights %+v", sum.Fights)
	}
	if sum.RunLabel != "until boss (step 29)" || sum.SpeedFellBack {
		t.Fatalf("run label %q, fell back %v", sum.RunLabel, sum.SpeedFellBack)
	}
}

// TestRaidWalkRetriesAFailedFightDetail: a detail request that fails once does not lose the fight's digest; a later poll fetches it.
func TestRaidWalkRetriesAFailedFightDetail(t *testing.T) {
	f := newFixture(t)
	f.opts.dashURL, f.opts.dashPassFile = "http://dash.test", "/tmp/pass"
	f.dash.failFight = 1
	f.dash.fights = []walkFightRow{{ID: "4-5", Key: "k", Dur: 1200, Outcome: "win", Step: 3, Deaths: 1}}
	f.defaultRun("onyxias_lair")
	if code := f.run(context.Background()); code != exitOK {
		t.Fatalf("exit %d: %s", code, f.stderr.String())
	}
	sum := f.summary()
	if len(sum.Fights) != 1 || sum.Fights[0].Detail == nil || len(f.dash.fightKeys) < 2 {
		t.Fatalf("fights %+v, detail requests %d", sum.Fights, len(f.dash.fightKeys))
	}
}

func TestRaidWalkInterruptStopsRunAndTearsDown(t *testing.T) {
	f := newFixture(t)
	var q []queuedEvent
	for step := 2; step <= 28; step++ {
		q = append(q, queuedEvent{raw: fmt.Sprintf("TBM:RUN|onyxias_lair|running|%d/29|onyxia|move|walk|walking", step)})
	}
	f.agent.mu.Lock()
	f.agent.startQueue = q
	f.agent.mu.Unlock()
	ctx, cancel := context.WithCancel(context.Background())
	var once sync.Once
	f.agent.hostilesFn = func(n int) []tbm.HostileRec {
		if n >= 3 {
			once.Do(cancel)
		}
		return nil
	}
	code := f.run(ctx)
	if code != exitRaidWalkFailed {
		t.Fatalf("exit %d, want 18", code)
	}
	if n := countPrefix(f.agent.gmLines(), ".bot run stop"); n != 1 {
		t.Fatalf("%d run stops on an interrupt", n)
	}
	if len(f.rec.teardown) == 0 {
		t.Fatal("no teardown after the interrupt")
	}
	if sum := f.summary(); sum.EndReason != "interrupted" {
		t.Fatalf("summary end %q", sum.EndReason)
	}
}

// --- fault injection hooks and the two-raid entry (task T0, spec raid-walk-addons 1.7) ---

// fakeInjector records the hooks' calls so their order can be checked.
type fakeInjector struct {
	w     *walk
	specs []string
	fail  bool

	prepares   int
	ticks      int
	cleanups   int
	preparedAt uint64 // w.afterN at Prepare: the watch's own views come after it
	afterNs    []uint64
	notes      []string
	prepared   bool // the run had started when Prepare ran
	cleaned    bool
	stopped    bool // w.runStopped at Cleanup: false when Cleanup ran before stopRun
}

func (f *fakeInjector) Prepare(ctx context.Context) error {
	f.prepares++
	f.prepared = f.w.runStarted
	f.preparedAt = f.w.afterN
	return nil
}

func (f *fakeInjector) Tick(ctx context.Context, now time.Time) error {
	f.ticks++
	f.afterNs = append(f.afterNs, f.w.afterN)
	f.notes = append(f.notes, f.w.newNotes...)
	f.w.newNotes = nil // the injector drains them, as I2 will
	if testFakeInjectorPanicOnTick {
		panic("fake injector tick")
	}
	return nil
}

func (f *fakeInjector) Cleanup(ctx context.Context) {
	f.cleanups++
	f.cleaned = true
	f.stopped = f.w.runStopped
}

func (f *fakeInjector) Results() ([]raidwalk.InjectSummary, int) {
	return []raidwalk.InjectSummary{{Kind: "kill-mt", Verdict: "pass", Step: 7}}, 1
}

func (f *fakeInjector) Failed() bool { return f.fail }

// testFakeInjectorPanicOnTick makes the fake Tick panic, for the panic exit path's cleanup test.
var testFakeInjectorPanicOnTick bool

func (f *fakeInjector) Incidents() []raidwalk.InjectIncident { return nil }

func (f *fakeInjector) Blocking() bool { return false }

func (f *fakeInjector) PrintPlan(out io.Writer) {
	fmt.Fprintln(out, "faults: 1 (kill-mt)")
}

// installFakeInjector puts a recording injector behind the walk's hook; the getter returns it once the walk made it.
func installFakeInjector(t *testing.T, fail bool) func() *fakeInjector {
	t.Helper()
	var mu sync.Mutex
	var fi *fakeInjector
	old := newWalkInjector
	newWalkInjector = func(w *walk, specs []string) (walkInjector, error) {
		mu.Lock()
		defer mu.Unlock()
		fi = &fakeInjector{w: w, specs: append([]string{}, specs...), fail: fail}
		return fi, nil
	}
	t.Cleanup(func() { newWalkInjector = old })
	return func() *fakeInjector {
		mu.Lock()
		defer mu.Unlock()
		return fi
	}
}

func TestRaidWalkInjectFlagWithoutHookIsUsage(t *testing.T) {
	// I2's init() installs the real injector; this pins T0's nil path for a build without it.
	old := newWalkInjector
	newWalkInjector = nil
	t.Cleanup(func() { newWalkInjector = old })
	var stdout, stderr bytes.Buffer
	code := raidWalkCmd(context.Background(), []string{"--dry-run", "--dir", "../../dungeons", "--inject", "kill-mt", "onyxias_lair"}, &stdout, &stderr)
	if code != exitUsage {
		t.Fatalf("--inject without a hook: exit %d, want %d (stderr %q)", code, exitUsage, stderr.String())
	}
	if !strings.Contains(stderr.String(), "this build has no fault injection (--inject)") {
		t.Fatalf("the refusal does not name the missing hook: %q", stderr.String())
	}
}

func TestRaidWalkInjectorHookOrder(t *testing.T) {
	t.Run("happy path", func(t *testing.T) {
		inj := installFakeInjector(t, false)
		f := newFixture(t)
		f.opts.injects = stringList{"kill-mt"}
		f.defaultRun("onyxias_lair")
		teardownSawCleanup := false
		real := f.env.teardown
		f.env.teardown = func(args []string, stdout, stderr io.Writer) int {
			teardownSawCleanup = inj().cleaned
			return real(args, stdout, stderr)
		}
		if code := f.run(context.Background()); code != exitOK {
			t.Fatalf("exit %d: %s", code, f.stderr.String())
		}
		fake := inj()
		if fake == nil {
			t.Fatal("the walk did not create an injector")
		}
		if fake.prepares != 1 || !fake.prepared {
			t.Fatalf("Prepare ran %d times (run started at Prepare: %v), want once after the start line", fake.prepares, fake.prepared)
		}
		if fake.ticks == 0 {
			t.Fatal("no Tick in the watch")
		}
		f.agent.mu.Lock()
		views := f.agent.views
		f.agent.mu.Unlock()
		if want := views - int(fake.preparedAt); fake.ticks != want {
			t.Fatalf("%d Ticks for %d watch loops", fake.ticks, want)
		}
		for i := 1; i < len(fake.afterNs); i++ {
			if fake.afterNs[i] <= fake.afterNs[i-1] {
				t.Fatalf("tick %d saw no fresh frame (afterN %d -> %d)", i, fake.afterNs[i-1], fake.afterNs[i])
			}
		}
		if len(fake.notes) != fake.ticks {
			t.Fatalf("the injector saw %d notes in %d ticks: newNotes were not drained", len(fake.notes), fake.ticks)
		}
		if fake.cleanups != 1 || !teardownSawCleanup {
			t.Fatalf("Cleanup ran %d times (cleaned before teardown: %v), want once before teardown", fake.cleanups, teardownSawCleanup)
		}
		if len(fake.specs) != 1 || fake.specs[0] != "kill-mt" {
			t.Fatalf("the hook got %q, want the --inject spec", fake.specs)
		}
		if sum := f.summary(); len(sum.Injections) != 1 || sum.Injections[0].Kind != "kill-mt" || sum.Deaths.Injected != 1 {
			t.Fatalf("summary injections %+v, injected %d", sum.Injections, sum.Deaths.Injected)
		}
	})
	t.Run("interrupt", func(t *testing.T) {
		inj := installFakeInjector(t, false)
		f := newFixture(t)
		f.opts.injects = stringList{"lag"}
		var q []queuedEvent
		for step := 2; step <= 28; step++ {
			q = append(q, queuedEvent{raw: fmt.Sprintf("TBM:RUN|onyxias_lair|running|%d/29|onyxia|move|walk|walking", step)})
		}
		f.agent.mu.Lock()
		f.agent.startQueue = q
		f.agent.mu.Unlock()
		ctx, cancel := context.WithCancel(context.Background())
		var once sync.Once
		f.agent.hostilesFn = func(n int) []tbm.HostileRec {
			if n >= 3 {
				once.Do(cancel)
			}
			return nil
		}
		teardownSawCleanup := false
		real := f.env.teardown
		f.env.teardown = func(args []string, stdout, stderr io.Writer) int {
			teardownSawCleanup = inj().cleaned
			return real(args, stdout, stderr)
		}
		if code := f.run(ctx); code != exitRaidWalkFailed {
			t.Fatalf("exit %d, want 18", code)
		}
		fake := inj()
		if fake == nil {
			t.Fatal("the walk did not create an injector")
		}
		if fake.cleanups != 1 || !teardownSawCleanup {
			t.Fatalf("Cleanup ran %d times (before teardown: %v), want once", fake.cleanups, teardownSawCleanup)
		}
		if fake.stopped {
			t.Fatal("Cleanup ran after .bot run stop, want before stopRun")
		}
	})
	t.Run("wipe keeps exit 19", func(t *testing.T) {
		inj := installFakeInjector(t, true)
		f := newFixture(t)
		// No recoveries left: the wipe ends the walk as before (with --wipe-resume it would recover).
		f.opts.wipeResume = 0
		f.opts.injects = stringList{"kill-mt"}
		f.agent.startReply = startAt("onyxias_lair", 23)
		f.allDeadFromHires()
		teardownSawCleanup := false
		real := f.env.teardown
		f.env.teardown = func(args []string, stdout, stderr io.Writer) int {
			teardownSawCleanup = inj().cleaned
			return real(args, stdout, stderr)
		}
		if code := f.run(context.Background()); code != exitRaidWalkIncident {
			t.Fatalf("exit %d, want 19: %s", code, f.stderr.String())
		}
		fake := inj()
		if fake == nil {
			t.Fatal("the walk did not create an injector")
		}
		if fake.cleanups != 1 || !teardownSawCleanup {
			t.Fatalf("Cleanup ran %d times (before teardown: %v), want once", fake.cleanups, teardownSawCleanup)
		}
		if sum := f.summary(); sum.ExitCode != exitRaidWalkIncident {
			t.Fatalf("a failed fault overrode the wipe: exit %d", sum.ExitCode)
		}
	})
	t.Run("failed fault is exit 23", func(t *testing.T) {
		installFakeInjector(t, true)
		f := newFixture(t)
		f.opts.injects = stringList{"kill-mt"}
		f.defaultRun("onyxias_lair")
		if code := f.run(context.Background()); code != exitRaidWalkInjectFailed {
			t.Fatalf("exit %d, want %d (stdout %q stderr %q)", code, exitRaidWalkInjectFailed, f.stdout.String(), f.stderr.String())
		}
		if sum := f.summary(); sum.ExitCode != exitRaidWalkInjectFailed {
			t.Fatalf("summary exit %d, want %d", sum.ExitCode, exitRaidWalkInjectFailed)
		}
	})
	t.Run("no injects, no injector", func(t *testing.T) {
		inj := installFakeInjector(t, false)
		f := newFixture(t)
		f.defaultRun("onyxias_lair")
		if code := f.run(context.Background()); code != exitOK {
			t.Fatalf("exit %d: %s", code, f.stderr.String())
		}
		if fake := inj(); fake != nil {
			t.Fatalf("a walk without --inject made an injector: %+v", fake)
		}
	})
}

func TestRaidWalkSlotListRoutesToPair(t *testing.T) {
	t.Run("no hook", func(t *testing.T) {
		// P1's init installs the hook; this subtest is about the build without one.
		old := raidWalkPairEntry
		raidWalkPairEntry = nil
		t.Cleanup(func() { raidWalkPairEntry = old })
		var stdout, stderr bytes.Buffer
		code := raidWalkCmd(context.Background(), []string{"onyxias_lair", "--slot", "1,2"}, &stdout, &stderr)
		if code != exitUsage {
			t.Fatalf("--slot 1,2: exit %d, want %d", code, exitUsage)
		}
		if !strings.Contains(stderr.String(), "this build has no two-raid mode (--slot A,B)") {
			t.Fatalf("the refusal does not name the pair form: %q", stderr.String())
		}
	})
	t.Run("hook gets the args", func(t *testing.T) {
		var got []string
		old := raidWalkPairEntry
		raidWalkPairEntry = func(ctx context.Context, args []string, stdout, stderr io.Writer) int {
			got = append([]string{}, args...)
			return 7
		}
		t.Cleanup(func() { raidWalkPairEntry = old })
		for _, args := range [][]string{
			{"onyxias_lair", "--slot", "1,2"},
			{"onyxias_lair", "--slot=1,2"},
		} {
			got = nil
			var stdout, stderr bytes.Buffer
			if code := raidWalkCmd(context.Background(), args, &stdout, &stderr); code != 7 {
				t.Fatalf("%q: exit %d, want the hook's 7 (stderr %q)", args, code, stderr.String())
			}
			if strings.Join(got, " ") != strings.Join(args, " ") {
				t.Fatalf("the hook got %q, want %q", got, args)
			}
		}
	})
	t.Run("slot 3 alone still refused", func(t *testing.T) {
		called := false
		old := raidWalkPairEntry
		raidWalkPairEntry = func(ctx context.Context, args []string, stdout, stderr io.Writer) int {
			called = true
			return exitOK
		}
		t.Cleanup(func() { raidWalkPairEntry = old })
		var stdout, stderr bytes.Buffer
		if code := raidWalkCmd(context.Background(), []string{"--slot", "3", "onyxias_lair"}, &stdout, &stderr); code != exitSlotRefused {
			t.Fatalf("--slot 3: exit %d, want %d (stderr %q)", code, exitSlotRefused, stderr.String())
		}
		if called {
			t.Fatal("a single slot went to the pair entry")
		}
	})
}

func TestTrackFrameKeepsPositionsAndKinds(t *testing.T) {
	now := newFakeClock().now()
	w := &walk{partyG: map[uint64]bool{}, hostiles: map[string]hostileState{}, bossSet: map[uint32]bool{10184: true},
		bossRule: raidwalk.NewBossRows(&dungeon.Plan{Legs: []dungeon.Leg{{Boss: &dungeon.Boss{Entry: 10184}}}}),
		bossRows: map[uint32]hostileState{}, botAt: map[string]botPos{}}
	frame := &tbm.ViewFrame{
		Party: []tbm.PartyRec{
			{GUID: 7, Name: "Mt", X: 1.5, Y: 2.25, Z: -3.75, Map: 249, State: "c"},
			{GUID: 8, Name: "Dead", X: 4, Y: 5, Z: 6, Map: 249, State: "d", Dead: true},
		},
		Hostiles: []tbm.HostileRec{
			{GUID: 9, Entry: 10184, X: -4, Y: -5, Z: -6, HPPct: 55, Target: 7, Kind: "u", Reaction: "h", Flags: "ce"},
		},
	}
	w.trackFrame(now, frame)
	mt, ok := w.botAt["Mt"]
	if !ok || mt.guid != 7 || mt.x != 1.5 || mt.y != 2.25 || mt.z != -3.75 || !mt.inCombat || mt.mapID != 249 {
		t.Fatalf("bot Mt %+v, want its guid, position, map and combat state", mt)
	}
	if dead := w.botAt["Dead"]; dead.guid != 8 || dead.inCombat || !dead.dead {
		t.Fatalf("bot Dead %+v, want guid 8, dead and not in combat", dead)
	}
	h, ok := w.hostiles[packet.GUID(9).String()]
	if !ok || h.entry != 10184 || h.x != -4 || h.y != -5 || h.z != -6 || h.kind != "u" || h.reaction != "h" || h.flags != "ce" {
		t.Fatalf("hostile %+v, want its entry, position, kind, reaction and flags", h)
	}
	if h.hppct != 55 || h.target != 7 || !h.at.Equal(now) {
		t.Fatalf("hostile %+v, want hp 55, target 7 and the frame's time", h)
	}
}

func TestSummaryCarriesRowsShaAndUntilStep(t *testing.T) {
	f := newFixture(t)
	f.opts.sql = "../../internal/planexport/testdata/onyxias_lair-20261029130000.sql"
	// A note holding "(step 5)" before the until label: only the label's own step number counts.
	f.agent.startReply = &commanderReply{Reply: tbm.Reply{Kind: tbm.KindAck, OK: true,
		Ack:  &tbm.Ack{Intent: "run start", Scope: "party", Count: 1, Executor: "-"},
		Line: "TBM:ACTION_ACK|run start|party|1|-",
		Lines: []string{
			"TBM:RUN|onyxias_lair|running|1/29|onyxia|move|walk|regrouped at (step 5); until boss (step 29)",
			"TBM:ACTION_ACK|run start|party|1|-",
		}}}
	f.defaultRun("onyxias_lair")
	if code := f.run(context.Background()); code != exitOK {
		t.Fatalf("exit %d: %s", code, f.stderr.String())
	}
	rows, _, err := readRaidMigration(f.opts.sql, "onyxias_lair")
	if err != nil {
		t.Fatal(err)
	}
	h := sha256.Sum256([]byte(planexport.WriteSQL([]planexport.Rows{rows})))
	sum := f.summary()
	if want := hex.EncodeToString(h[:]); sum.RowsSHA256 != want {
		t.Fatalf("rows_sha256 %q, want %q", sum.RowsSHA256, want)
	}
	if sum.UntilStep != 29 {
		t.Fatalf("until_step %d, want 29", sum.UntilStep)
	}
	if sum.Injections == nil || len(sum.Injections) != 0 {
		t.Fatalf("injections %+v, want an empty list", sum.Injections)
	}
}

// bossDoneFixture is a walk of the Molten Core plan JSON with --until boss-done, with the clause it must send and the plan's step count.
func bossDoneFixture(t *testing.T) (*fixture, int, int) {
	t.Helper()
	f := newFixture(t)
	f.opts.plan, f.opts.holder = "molten_core", "raid-walk-molten_core"
	f.opts.until, f.opts.tier = "boss-done", 1
	p, err := dungeon.Load(filepath.Join(f.opts.dir, "molten_core.json"))
	if err != nil {
		t.Fatal(err)
	}
	f.agent.instMap = p.Map
	res, err := planexport.Build(p, planexport.Options{})
	if err != nil {
		t.Fatal(err)
	}
	clause, boss, err := raidwalk.BossDoneUntil(res.Rows, 1)
	if err != nil || boss < 1 || !strings.HasPrefix(clause, "step ") {
		t.Fatalf("clause %q boss %d: %v", clause, boss, err)
	}
	n, _ := strconv.Atoi(strings.TrimPrefix(clause, "step "))
	return f, n, len(res.Rows.Steps)
}

func zgStart(total, next int) *commanderReply {
	return &commanderReply{Reply: tbm.Reply{Kind: tbm.KindAck, OK: true,
		Ack:  &tbm.Ack{Intent: "run start", Scope: "party", Count: 1, Executor: "-"},
		Line: "TBM:ACTION_ACK|run start|party|1|-",
		Lines: []string{
			fmt.Sprintf("TBM:RUN|molten_core|running|1/%d|lucifron|move|walk|walking|until step %d (step %d)", total, next, next),
			"TBM:ACTION_ACK|run start|party|1|-",
		}}}
}

// --until boss-done sends "until step <the step after the boss>" and the run ends with the boss step done.
func TestRaidWalkUntilBossDone(t *testing.T) {
	f, next, total := bossDoneFixture(t)
	f.agent.startReply = zgStart(total, next)
	f.agent.mu.Lock()
	f.agent.startQueue = []queuedEvent{
		{raw: fmt.Sprintf("TBM:RUN|molten_core|running|%d/%d|lucifron|boss|fight|fighting", next-1, total)},
		{raw: fmt.Sprintf("TBM:RUN|molten_core|done|%d/%d|lucifron|move|walk|stopped before step %d (until step %d): the party stays where it stands", next, total, next, next)},
	}
	f.agent.mu.Unlock()
	if code := f.run(context.Background()); code != exitOK {
		t.Fatalf("exit %d: %s\n%s", code, f.stderr.String(), f.stdout.String())
	}
	if want := fmt.Sprintf("molten_core|until|step|%d", next); f.agent.startArgs != want {
		t.Fatalf("run start args %q, want %q", f.agent.startArgs, want)
	}
	sum := f.summary()
	if sum.EndReason != "until" || sum.Until != "boss-done" || sum.UntilStep != next {
		t.Fatalf("summary end %q until %q until_step %d", sum.EndReason, sum.Until, sum.UntilStep)
	}
	if !hasNote(sum, "--until boss-done: the boss is step") {
		t.Fatalf("notes %q", sum.Notes)
	}
}

// Walk 12: --until "step 41" reached step 40 (Jeklik) and raid-walk sent `.bot
// run stop` the moment the boss was engaged, so the fight ran with no run
// leader. The run stays on its boss step and its until goes past it, so the
// run leader fights it: no run stop, the walk ends at its until.
func TestRaidWalkUntilPastBossLetsTheRunFightIt(t *testing.T) {
	f, next, total := bossDoneFixture(t)
	f.agent.startReply = zgStart(total, next)
	f.agent.mu.Lock()
	f.agent.startQueue = []queuedEvent{
		{raw: fmt.Sprintf("TBM:RUN|molten_core|running|%d/%d|lucifron|boss|fight|fighting", next-1, total)},
		{raw: fmt.Sprintf("TBM:RUN|molten_core|done|%d/%d|lucifron|move|walk|stopped before step %d (until step %d): the party stays where it stands", next, total, next, next)},
	}
	f.agent.mu.Unlock()
	f.agent.hostilesFn = func(n int) []tbm.HostileRec {
		return []tbm.HostileRec{{GUID: 9, Entry: 12118, HPPct: 100, Target: packet.GUID(1000)}}
	}
	if code := f.run(context.Background()); code != exitOK {
		t.Fatalf("exit %d: %s\n%s", code, f.stderr.String(), f.stdout.String())
	}
	if n := countPrefix(f.agent.gmLines(), ".bot run stop"); n != 0 {
		t.Fatalf("%d run stops, want 0: the run leader fights its boss step", n)
	}
	if sum := f.summary(); sum.EndReason != "until" {
		t.Fatalf("summary end %q, want until", sum.EndReason)
	}
	if !hasNote(f.summary(), "so the run leader fights it") {
		t.Fatalf("notes %q", f.summary().Notes)
	}
}

// The default --until boss still stops at a boss: a trash walk that wakes a
// boss is not a run the leader fights.
func TestRaidWalkDefaultUntilStillStopsAtABoss(t *testing.T) {
	f := newFixture(t)
	f.agent.startReply = startAt("onyxias_lair", 10)
	f.agent.hostilesFn = func(n int) []tbm.HostileRec {
		return []tbm.HostileRec{{GUID: 9, Entry: 10184, HPPct: 100, Target: packet.GUID(1000)}}
	}
	f.allDeadFromHires()
	code := f.run(context.Background())
	if code != exitRaidWalkIncident {
		t.Fatalf("exit %d, want 19: %s", code, f.stderr.String())
	}
	if n := countPrefix(f.agent.gmLines(), ".bot run stop"); n != 1 {
		t.Fatalf("%d run stops, want exactly 1", n)
	}
}

// An early boss wake during a trash step still stops: the run is not on its
// boss step even though its until goes past the boss.
func TestRaidWalkEarlyBossWakeStillStops(t *testing.T) {
	f, next, total := bossDoneFixture(t)
	f.opts.until = fmt.Sprintf("step %d", next)
	f.agent.startReply = zgStart(total, next)
	f.agent.mu.Lock()
	f.agent.startQueue = []queuedEvent{
		{raw: fmt.Sprintf("TBM:RUN|molten_core|running|%d/%d|lucifron|move|walk|walking", next-1, total)},
	}
	f.agent.mu.Unlock()
	f.agent.hostilesFn = func(n int) []tbm.HostileRec {
		return []tbm.HostileRec{{GUID: 9, Entry: 12118, HPPct: 100, Target: packet.GUID(1000)}}
	}
	code := f.run(context.Background())
	if code != exitRaidWalkIncident {
		t.Fatalf("exit %d, want 19: %s", code, f.stderr.String())
	}
	if n := countPrefix(f.agent.gmLines(), ".bot run stop"); n != 1 {
		t.Fatalf("%d run stops, want exactly 1", n)
	}
}

// Rows that are not the server's (a plan of another step count) refuse the walk and stop the run: the step after the boss would be
// another step.
func TestRaidWalkBossDoneRefusesOtherRows(t *testing.T) {
	f, next, total := bossDoneFixture(t)
	f.agent.startReply = zgStart(total+40, next)
	if code := f.run(context.Background()); code != exitRaidWalkFailed {
		t.Fatalf("exit %d, want 18: %s", code, f.stderr.String())
	}
	if n := countPrefix(f.agent.gmLines(), ".bot run stop"); n != 1 {
		t.Fatalf("%d run stops, want exactly 1", n)
	}
	if !strings.Contains(f.summary().EndReason, fmt.Sprintf("boss-done: the server's plan has %d steps", total+40)) {
		t.Fatalf("summary reason %q", f.summary().EndReason)
	}
}

func TestRaidWalkBossDoneDryRun(t *testing.T) {
	f, next, _ := bossDoneFixture(t)
	f.opts.dryRun = true
	if code := f.run(context.Background()); code != exitOK {
		t.Fatalf("exit %d: %s", code, f.stderr.String())
	}
	if want := fmt.Sprintf("run start molten_core until step %d", next); !strings.Contains(f.stdout.String(), want) {
		t.Fatalf("dry run lacks %q:\n%s", want, f.stdout.String())
	}
}

// Walks 12/13: the single-slot form takes the plan before its flags, as the README documents.
func TestRaidWalkPlanFirstSingleSlot(t *testing.T) {
	flagsFirst := []string{"--server", "m910q", "--slot", "6", "--dry-run", "--until", "step 41", "zul_gurub"}
	planFirst := []string{"zul_gurub", "--server", "m910q", "--slot", "6", "--dry-run", "--until", "step 41"}
	var discard bytes.Buffer
	want, code := parseRaidWalkArgs(flagsFirst, &discard)
	if code != exitOK {
		t.Fatalf("flags-first: exit %d, want %d (stderr %q)", code, exitOK, discard.String())
	}
	var stderr bytes.Buffer
	got, code := parseRaidWalkArgs(planFirst, &stderr)
	if code != exitOK {
		t.Fatalf("plan-first: exit %d, want %d (stderr %q)", code, exitOK, stderr.String())
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("plan-first parsed to %+v, want %+v", got, want)
	}
}

func TestRaidWalkPlanFirstWithSql(t *testing.T) {
	var stderr bytes.Buffer
	o, code := parseRaidWalkArgs([]string{"zul_gurub", "--server", "m910q", "--slot", "6", "--sql", "walk.sql"}, &stderr)
	if code != exitOK {
		t.Fatalf("plan-first with --sql: exit %d, want %d (stderr %q)", code, exitOK, stderr.String())
	}
	if o.plan != "zul_gurub" || o.sql != "walk.sql" {
		t.Fatalf("plan-first with --sql parsed to plan %q sql %q", o.plan, o.sql)
	}
}

func TestRaidWalkExtraArgumentNamesIt(t *testing.T) {
	var stderr bytes.Buffer
	_, code := parseRaidWalkArgs([]string{"zul_gurub", "extra", "--slot", "6"}, &stderr)
	if code != exitUsage {
		t.Fatalf("extra argument: exit %d, want %d", code, exitUsage)
	}
	if !strings.Contains(stderr.String(), `unexpected argument "extra"`) {
		t.Fatalf("stderr does not name the extra argument: %q", stderr.String())
	}
}

// TestRaidWalkWipeRestFloor: the rest's quiet window counts from the last non-quiet poll, so a very
// short --wipe-rest can already be met by the FIRST quiet poll — the rest would close without ever
// seeing sustained quiet. The flag refuses anything below 30 s (walk 18 r5).
func TestRaidWalkWipeRestFloor(t *testing.T) {
	var stderr bytes.Buffer
	if _, code := parseRaidWalkArgs([]string{"zul_gurub", "--wipe-rest", "20s"}, &stderr); code != exitUsage {
		t.Fatalf("--wipe-rest 20s: exit %d, want %d", code, exitUsage)
	}
	if !strings.Contains(stderr.String(), "--wipe-rest must be 30s or more") {
		t.Fatalf("stderr does not name the floor: %q", stderr.String())
	}
	var ok bytes.Buffer
	if _, code := parseRaidWalkArgs([]string{"zul_gurub", "--wipe-rest", "30s"}, &ok); code != exitOK {
		t.Fatalf("--wipe-rest 30s: exit %d, want %d (%q)", code, exitOK, ok.String())
	}
}
