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
	"github.com/pjerra/turtle-agent/internal/tbdash"
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
	// kind and source make the line a bot's chat or whisper (events.KindChat, "BotName") instead of a system line.
	kind   string
	source string
	// due holds a line back until the fake clock reaches it (the module's delayed answer); zero serves at once.
	due time.Time
}

// event is the served form of a queued line.
func (q queuedEvent) event(seq uint64) events.Event {
	ev := events.Event{Seq: seq, Kind: events.KindSystem, Text: q.raw, Data: map[string]any{"raw": q.raw}}
	if q.text != "" {
		ev.Text = q.text
	}
	if q.kind != "" {
		ev.Kind, ev.Source, ev.Data = q.kind, q.source, map[string]any{}
	}
	return ev
}

// queuedSpell is one cast event the practice loop reads through the events RPC (kinds spell_start/spell_go).
type queuedSpell struct {
	at time.Time
	ev events.Event
}

type timelineEntry struct {
	Text     string
	Targeted bool
}

// fakeBind is one `.instance listbinds` row: the instance id and its time to reset.
type fakeBind struct {
	inst float64
	ttr  string
}

type walkFakeAgent struct {
	runStarted bool   // the walk sent `run start` (wipe-fault tests gate on it)
	recoveryGo bool   // a `.go xyz` went out after run start (the recovery teleport)
	instMap    uint32 // the map the entered party stands on (0: Onyxia's 249)
	t          *testing.T
	clock      *fakeClock

	mu       sync.Mutex
	calls    []string
	gm       []string
	gmUnits  []int
	timeline []timelineEntry
	party    []string
	nameSeq  int
	// uninviteLag delays the module's uninvite by that many party reads: the server answers the line on its next
	// group update, so the first read after it still holds the bot (0: the bot is gone on the next read).
	uninviteLag map[string]int
	// uninvitePending is the group update in flight: name -> party reads that must still show it.
	uninvitePending map[string]int
	// uninviteRefused names the module cannot uninvite (a real player's: the usage line, no uninvite): the bot stays.
	uninviteRefused map[string]bool
	startArgs       string
	roleLines       []string // every `.bot role` line, in order
	roleFail        bool     // the server refuses every `.bot role` ("does not use that spec")
	roleSilentFor   string   // the one bot whose `.bot role` is never answered
	roleSilent      bool     // the server never answers `.bot role`
	// gearBusy is how many `.bot gear raid` sends the module refuses with ACTION_ERR|gear|busy before it answers the
	// verdict (an earlier gear job still running).
	gearBusy int
	// gearBusyDelay holds each busy reply back that long (the module's answer arrives late); 0 answers at once.
	gearBusyDelay time.Duration
	// gearBlockerIn schedules the blocking job's own verdict that long after a busy reply, as the job it runs ends;
	// 0 schedules none.
	gearBlockerIn time.Duration
	// talentMode is how the fake game answers `.bot command <bot> talents [<path>]`: "" applies the build and reads it back,
	// "silent" never answers, "stuck" answers but leaves the build as it was (Survival), "multiple" lists several specs.
	talentMode      string
	talentReadExtra string            // a system line the game sends right behind the talent read-back (the answer to the next command)
	talentLines     []string          // every `.bot command <bot> talents ...` line, in order
	talentTrees     map[string][3]int // the build each hire has (default: the Survival premade)

	entered  bool
	status   session.StatusInfo
	lines    []queuedEvent
	history  []queuedEvent // every served line, in serve order (seq i+1); a cursor rewind re-serves from it
	since    uint64        // the `since` cursor of the in-flight events call
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

	// The --gm-clear fakes: gm_kill calls the killFn with the guid and the number of lines sent to it so far (nil: every kill works);
	// a unit it lets die shows Dead in the unit RPC (killed).
	killFn    func(g packet.GUID, tries int) bool
	killTries map[packet.GUID]int
	killed    map[packet.GUID]bool

	// instanceBinds is what `.instance listbinds` reports for the commander (the reuse bind check).
	instanceBinds []fakeBind

	startReply *commanderReply
	startQueue []queuedEvent
	// startCallErr makes the `run start` RPC itself fail (a timed-out call: the server may still have accepted the
	// start), after the fake recorded it and ran its startQueue and onStart hooks.
	startCallErr error
	// pauseCallErr makes the `run pause` RPC itself fail (a transport error, never the server's reply): even when
	// its text mentions the run not running it must not read as the already-gone answer.
	pauseCallErr error
	bindLayout   string
	enterReply   *commanderReply
	enterBusy    int // the first enterBusy enter orders answer ERR busy
	enterCalls   int
	onGM         func(line string)
	// onResume runs when the walk sends `run resume` through the commander RPC (nil: ACK only). runEnded models the
	// commander's run book: a `.bot run stop` ENDS the run and erases its slot, so a later `run resume` answers "No run
	// is going" (the practice reset must pause, never stop).
	onResume func()
	runEnded bool
	// runVerbs records every `run` commander request's verb, in order, and onSkip runs for one whose verb is skip
	// (walk 24 B: the boss-not-found skip). skipErr makes that skip answer an error (a refusal). pauseErr, when
	// non-nil, makes `run pause` fail while it points at true (a refused pause: the gm-instance-data resume test).
	// The refusal's code and message come from pauseErrCode (default "refused") and pauseErrMsg (default "the run
	// is busy"); the practice's already-gone answer sets both to the module's code and message.
	// onPause runs after an accepted pause (the fake server's TBM:RUN paused line) and nil models an ACK with no
	// status, which the practice reset must refuse.
	runVerbs []string
	onSkip   func()
	skipErr  bool
	pauseErr *bool
	// pauseErrMsg overrides the refused pause's message (the practice's already-gone run answers "The run is not
	// running" instead of the default "the run is busy").
	pauseErrMsg string
	// pauseErrCode overrides the refused pause's ACTION_ERR code. Empty keeps the legacy "refused"; a not-going
	// refusal answers the module's code ("no-run" or "state").
	pauseErrCode string
	// pauseFails caps the `run pause` refusals: the first pauseFails pauses refuse, later ones land. 0 refuses
	// everything while pauseErr points at true.
	pauseFails int
	onPause    func()
	// onStart runs after a `run start` was answered and its startQueue was pushed (a test that needs lines after a
	// later start: the boss-not-found re-run). nil: nothing.
	onStart func()
	// runStatusFn answers one `run status` RPC (the re-run's stop confirmation): nil keeps the party line's answer, and
	// a nil result falls back to it. It runs on the walk's goroutine, so it must not take f.mu.
	runStatusFn func() *commanderReply

	// pos is where the commander's last .go put it; the quiet-check fakes place units around it.
	x, y, z float32

	// hireCap is the server's hire limit (0: raidwalk.HireCap, the 39 of onyxia-kill-plan C6).
	hireCap int

	// The practice hands' game side (raid-walk --practice): each hook models what the GM command does to the test
	// scenario; nil leaves the world unchanged. spellQueue holds the cast events the practice loop's own events read
	// (kinds spell_start/spell_go) delivers once the fake clock reaches their time.
	onEvade func(g packet.GUID)
	onSetHP func(g packet.GUID)
	// setHPErr, when set, makes every gm_set_hp call answer it (a unit the client cannot find). It runs after the
	// call is logged, so a test can count the attempts.
	setHPErr   error
	onRespawn  func(g packet.GUID)
	onRevive   func(name string)
	spellQueue []queuedSpell

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
	// partyJoinAt holds the LAST hire out of the party list for that long after the last hire line: on the live
	// server the join lands seconds after the reply (walk 12), so the settle poll has to wait for it.
	partyJoinAt time.Duration
	lastHireAt  time.Time

	// The fault injector's fakes (raid_walk_inject_test.go): the server's selection (target/gps), the per-bot
	ownGUID packet.GUID // the commander's GUID when a test sets one
	ownPos  [3]float32  // the commander's own position (.go xyz updates it)
	pos     map[string][3]float32
	// goLag holds back the position of the recovery's `.go xyz` (the first one after `run start`) for that many
	// status polls, while the map id takes effect at once: the live server answers the go at once and lands the
	// character a moment later (walk 22 test2 s4, Q3: the summons left 2 ms after the go). 0: every go lands at
	// once, as the enter phase's own go does (it precedes the run start).
	goLag int
	// goLagEvery re-arms goLag for EVERY recovery go (the entrance fallback's go lags like the first), not only the first.
	goLagEvery int
	// recoveryGos counts the recovery's `.go xyz` lines (the first one after `run start` is 1). goStuckFrom makes
	// that go and every later one accepted but never landed (the status keeps the commander's old position): the
	// hole a wait on the landing can never pass, for the H1 review case.
	recoveryGos int
	goStuckFrom int
	// goUnsettled makes the status report the recovery go's destination AT ONCE but flagged ZUnknown (or Falling when
	// goUnsettledFalling) for that many polls, the live session's state until the server sends a position.
	goUnsettled        int
	goUnsettledFalling bool
	unsettledLeft      int
	lastUnsettled      bool // the last status served was flagged unsettled
	// summonUnsettled counts the summons that left right after a status flagged unsettled.
	summonUnsettled int
	// zStuck models a status whose z the session never settles: a FAR teleport (walk 23: SMSG_NEW_WORLD leaves
	// ZUnknown for good, as no floor drop follows it) or a NEAR one over a navmesh hole (meshHole). goArrive sets
	// it. A settled near go clears it.
	zStuck bool
	// teleportSeq is the server teleports the session has acked (StatusInfo.TeleportSeq): it advances when a go or
	// appear LANDS.
	teleportSeq uint64
	// meshHole keeps the status ZUnknown for good after a NEAR go, as the live session does when the navmesh has a
	// hole under the destination: dropToFloor finds no floor and the z guess never settles (walk 24 test2 r3). The
	// teleport seq still advances, so a seq-based landing rule lands.
	meshHole bool
	// commanderInst is the instance copy the commander stands in (walk 23, mangosd TTEV enter events): "hires" after a
	// `.appear <hire>` (the core binds a GM to the target's copy, Commands.cpp HandleGonameCommand), "fresh" after a
	// recovery `.go xyz` from another map (the server opens a new copy), unchanged by a near go. instAtRevive is that
	// copy at the first `.revive`; appearRefused makes `.appear` move nobody (a refused appear).
	commanderInst string
	instAtRevive  string
	appearRefused bool
	// appearLag serves that many status reads with the pre-appear state before an accepted `.appear` lands (the live
	// teleport lands a moment after the chat line); goBeforeAppear counts recovery gos sent while it had not landed.
	// statusFailNext fails that many status reads (an RPC error); ghostsOff reports dead hires on map 0 (released to
	// the graveyard, once the run has started); appearRefusedUntilRevive refuses every `.appear` until the first `.revive` went out.
	statusFailNext int
	ghostsOff      bool
	// cancelStatusAt cancels cancelStatusCtx on the n-th status read (1-based; 0: disabled): the read answers while
	// the walk is already interrupted, so a failure there latches nothing. M2 round 5: the old statusFailNext-only
	// shape cancelled before bossCorpse ran and never reached the status-cancel guard.
	cancelStatusAt  int
	cancelStatusCtx context.CancelFunc
	// ghostsOffN is a PARTIAL release: the first N dead hires show on map 0 (the rest keep the plan's map), so a
	// walk has a hire off the map and one still to appear at (the post-wait ghosts re-check, review round 1 M2).
	ghostsOffN               int
	appearRefusedUntilRevive bool
	revived                  bool
	appearLag                int
	appearPend               func()
	appearPendLeft           int
	goBeforeAppear           int
	// enterMap puts the commander on this map once `cmd enter` is acked (0: it stays where its last go put it):
	// the live walk-22 agent stood inside the instance, so its recovery go was a near one.
	enterMap   uint32
	goPend     [3]float32
	goPendOK   bool
	goTarget   [3]float32 // the last `.go xyz` target: the revive point a `.summon` must wait for
	goTargetOK bool
	// summonDist records, per `.summon`, the distance from the commander's own reported position to that target:
	// a summon before the landing shows the race the wait closes.
	summonDist []float64
	// partyExtra are the P-record fields the module sends beyond the fake's own (F3: target, spell, action, result).
	partyExtra           map[string]tbm.PartyRec
	selGUID              packet.GUID  // the selection the server has in force
	queuedSel            *packet.GUID // a target RPC the server applies after the next .gps (lagTargets)
	lagTargets           bool
	noSelect             bool // .gps always reports the commander (a server that never applies the select)
	statusTargetMismatch bool
	noPartyFrames        bool        // bot_view frames without a party (an injector with no bot positions)
	gmAt                 []time.Time // when each gm line was sent
	killAt               []time.Time // when each gm_kill call arrived (the retry gap's clock)
	// Review-round-2 fault hooks: bot_view failure/omission (viewFn) and combat during the
	// quiet rest (combatAllFn); the dashboard fights read fails through fakeDash.fightsErr.
	viewFn      func(n int) (fail bool, omit string)
	combatAllFn func(n int) bool // frame n shows every hire in combat
	combatBots  map[string]bool  // these hires show in combat in every frame (--revive-at-raid: one fights away)
	offMapBots  map[string]bool  // these hires show on map 0 in every frame (--revive-at-raid: the lead on another map)
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
	// noGMOn makes the `.gm on` probe answer "GM mode is OFF": the place phase must refuse (review round 1, item 12).
	noGMOn bool

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
	// gmCtxErr is the context error each gm line was sent under (review round 2, LOW-3: the teardown's unbind must
	// not go out on a cancelled context). gmOnSeen and gmOnKinds are the kinds the first events read after `.gm on`
	// asked for (review round 2, LOW-2).
	gmCtxErr  []error
	gmOnSeen  bool
	gmOnKinds []string
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
		if f.cancelStatusAt > 0 {
			f.cancelStatusAt--
			if f.cancelStatusAt == 0 && f.cancelStatusCtx != nil {
				cancel := f.cancelStatusCtx
				f.mu.Unlock()
				cancel()
				f.mu.Lock()
			}
		}
		if f.statusFailNext > 0 {
			f.statusFailNext--
			f.mu.Unlock()
			return errors.New("status: agent busy")
		}
		if f.goPendOK {
			f.goLag--
			if f.goLag <= 0 {
				f.goPendOK, f.goLag = false, 0
				fromMap := f.status.Map
				f.x, f.y, f.z = f.goPend[0], f.goPend[1], f.goPend[2]
				f.ownPos = [3]float32{f.x, f.y, f.z}
				f.status.X, f.status.Y, f.status.Z = f.x, f.y, f.z
				f.goArrive(fromMap)
			}
		}
		if f.appearPend != nil {
			if f.appearPendLeft > 0 {
				f.appearPendLeft--
			} else {
				f.appearPend()
				f.appearPend = nil
			}
		}
		st := f.status
		st.TeleportSeq = f.teleportSeq
		f.lastUnsettled = false
		if f.zStuck {
			st.ZUnknown = true
			f.lastUnsettled = true
		}
		if f.unsettledLeft > 0 {
			f.unsettledLeft--
			f.lastUnsettled = true
			if f.goUnsettledFalling {
				st.Falling = true
			} else {
				st.ZUnknown = true
			}
		}
		v = st
		f.mu.Unlock()
	case "party":
		f.mu.Lock()
		// The group update in flight: a delayed uninvite leaves the bot in the group for that many reads.
		for name, n := range f.uninvitePending {
			if n > 0 {
				f.uninvitePending[name] = n - 1
				continue
			}
			for i, m := range f.party {
				if m == name {
					f.party = append(f.party[:i], f.party[i+1:]...)
					break
				}
			}
			delete(f.uninvitePending, name)
		}
		members := make([]session.PartyMember, 0, len(f.party))
		for i, n := range f.party {
			if f.partyJoinAt > 0 && i == len(f.party)-1 && f.clock.now().Sub(f.lastHireAt) < f.partyJoinAt {
				continue // the last hire's join has not landed yet: the group list names it, its join does not
			}
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
		f.gmCtxErr = append(f.gmCtxErr, ctx.Err())
		if p.Text == ".gm on" {
			f.gmOnSeen = true
		}
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
		f.mu.Lock()
		if f.gmOnSeen && f.gmOnKinds == nil {
			var ep struct {
				Kinds []string `json:"kinds"`
			}
			_ = json.Unmarshal(raw, &ep)
			f.gmOnKinds = append([]string{}, ep.Kinds...)
		}
		var sp struct {
			Since uint64   `json:"since"`
			Kinds []string `json:"kinds"`
		}
		_ = json.Unmarshal(raw, &sp)
		f.since = sp.Since
		spells := false
		for _, k := range sp.Kinds {
			if k == events.KindSpellStart || k == events.KindSpellGo {
				spells = true
			}
		}
		f.mu.Unlock()
		if spells {
			v = f.spellPage(sp.Since)
		} else {
			v = f.eventsPage()
		}
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
	case "gm_kill":
		var p struct {
			GUID string `json:"guid"`
		}
		_ = json.Unmarshal(raw, &p)
		g, err := packet.ParseGUID(p.GUID)
		if err != nil {
			f.t.Errorf("gm_kill %q: %v", p.GUID, err)
		}
		f.mu.Lock()
		f.log = append(f.log, "gm_kill "+g.String())
		f.killAt = append(f.killAt, f.clock.now())
		if f.killTries == nil {
			f.killTries, f.killed = map[packet.GUID]int{}, map[packet.GUID]bool{}
		}
		f.killTries[g]++
		if f.killFn == nil || f.killFn(g, f.killTries[g]) {
			f.killed[g] = true
		}
		f.mu.Unlock()
	case "gm_evade", "gm_respawn":
		var p struct {
			GUID string `json:"guid"`
		}
		_ = json.Unmarshal(raw, &p)
		g, err := packet.ParseGUID(p.GUID)
		if err != nil {
			f.t.Errorf("%s %q: %v", method, p.GUID, err)
		}
		f.mu.Lock()
		f.log = append(f.log, method+" "+g.String())
		h := map[string]func(packet.GUID){"gm_evade": f.onEvade, "gm_respawn": f.onRespawn}[method]
		f.mu.Unlock()
		if h != nil {
			h(g)
		}
	case "gm_set_hp":
		var p struct {
			GUID string  `json:"guid"`
			HP   *uint32 `json:"hp"`
		}
		_ = json.Unmarshal(raw, &p)
		if p.HP != nil {
			// The RPC takes the guid only: any explicit hp is refused before the session reads the unit.
			return fmt.Errorf("gm_set_hp: the hp argument is refused (the unit is restored to its own max health): got %d", *p.HP)
		}
		g, err := packet.ParseGUID(p.GUID)
		if err != nil {
			f.t.Errorf("gm_set_hp %q: %v", p.GUID, err)
		}
		f.mu.Lock()
		f.log = append(f.log, "gm_set_hp "+g.String())
		h := f.onSetHP
		hpErr := f.setHPErr
		f.mu.Unlock()
		if hpErr != nil {
			// The client cannot find the unit: no restore happens, so the world is not touched either.
			return hpErr
		}
		if h != nil {
			h(g)
		}
	case "gm_revive":
		var p struct {
			Name string `json:"name"`
		}
		_ = json.Unmarshal(raw, &p)
		f.mu.Lock()
		f.log = append(f.log, "gm_revive "+p.Name)
		h := f.onRevive
		f.mu.Unlock()
		if h != nil {
			h(p.Name)
		}
	case "unit":
		var p struct {
			GUID string `json:"guid"`
		}
		_ = json.Unmarshal(raw, &p)
		g, _ := packet.ParseGUID(p.GUID)
		f.mu.Lock()
		f.log = append(f.log, "unit "+g.String())
		dead := f.killed[g]
		f.mu.Unlock()
		v = objects.Unit{GUID: g, Type: "creature", Dead: dead, Health: map[bool]uint32{true: 0, false: 100}[dead], MaxHealth: 100}
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
		if p.Op == "run" && p.Verb == "start" && f.startCallErr != nil {
			return f.startCallErr
		}
		if p.Op == "run" && p.Verb == "pause" && f.pauseCallErr != nil {
			return f.pauseCallErr
		}
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

// goArrive is the moment a teleport LANDS in the fake (the one a real session counts in TeleportSeq): the position
// is served from here on. fromMap is the commander's map when the go was sent (near vs far). A meshHole keeps the
// status ZUnknown for good after a near go, as the live navmesh hole does.
func (f *walkFakeAgent) goArrive(fromMap uint32) { // f.mu held
	f.teleportSeq++
	far := f.status.Map != fromMap
	if f.meshHole && !far {
		f.zStuck = true
		return
	}
	f.zStuck = far // a far go leaves z unknown for good (SMSG_NEW_WORLD); a near go settles it
}

// gmSide is what the game does for a GM line.
func (f *walkFakeAgent) gmSide(line string) {
	if strings.TrimSpace(line) == ".bot run stop" {
		f.mu.Lock()
		f.runEnded = true
		f.mu.Unlock()
		return
	}
	if name, ok := strings.CutPrefix(line, ".bot uninvite "); ok {
		// The module uninvites the bot from the commander's group (commands/Party.cpp: HandleUninvite). A name it
		// cannot uninvite (a real player's) answers its usage line and leaves the group as it was.
		f.mu.Lock()
		defer f.mu.Unlock()
		if f.uninviteRefused[name] {
			return
		}
		if n := f.uninviteLag[name]; n > 0 {
			if f.uninvitePending == nil {
				f.uninvitePending = map[string]int{}
			}
			f.uninvitePending[name] = n
			return
		}
		for i, n := range f.party {
			if n == name {
				f.party = append(f.party[:i], f.party[i+1:]...)
				break
			}
		}
		return
	}
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
		f.lastHireAt = f.clock.now()
		f.mu.Unlock()
		f.push("TBM:HIRE_RAID|ok|"+strconv.Itoa(n)+"|"+strconv.Itoa(n)+"|0|0|0|3|0|", "")
	case strings.HasPrefix(line, ".bot gear raid "):
		f.mu.Lock()
		busy := f.gearBusy > 0
		if busy {
			f.gearBusy--
		}
		delay, blocker := f.gearBusyDelay, f.gearBlockerIn
		f.mu.Unlock()
		if busy {
			reply := "TBM:ACTION_ERR|gear|busy|A gear job is already running"
			if delay > 0 {
				f.pushAt(reply, "", f.clock.now().Add(delay))
			} else {
				f.push(reply, "")
			}
			if blocker > 0 {
				f.pushAt("TBM:GEAR|ok|39|39|3|60|-|-|-|0|-|0", "", f.clock.now().Add(blocker))
			}
			return
		}
		f.push("TBM:GEAR|ok|39|39|3|60|-|-|-|0|-|0", "")
	case strings.HasPrefix(line, ".bot command ") && strings.Contains(line, " talents"):
		f.talentSide(line)
	case strings.HasPrefix(line, ".bot role "):
		fields := strings.Fields(line)
		f.mu.Lock()
		f.roleLines = append(f.roleLines, line)
		fail := f.roleFail
		silent := f.roleSilent || (len(fields) > 2 && fields[2] == f.roleSilentFor)
		f.mu.Unlock()
		if len(fields) == 4 && !silent {
			if fail {
				f.push(fmt.Sprintf("Bot %s does not use that spec.", fields[2]), "")
			} else {
				f.push(fmt.Sprintf("Bot %s role set to %s.", fields[2], raidwalk.SpecRole(fields[3])), "")
			}
		}
	case strings.HasPrefix(line, ".go xyz "):
		f.mu.Lock()
		started := f.runStarted
		if started {
			f.recoveryGo = true
			f.recoveryGos++
			if f.appearPend != nil {
				f.goBeforeAppear++
			}
		}
		f.mu.Unlock()
		fields := strings.Fields(line)
		if len(fields) == 6 {
			x, _ := strconv.ParseFloat(fields[2], 32)
			y, _ := strconv.ParseFloat(fields[3], 32)
			z, _ := strconv.ParseFloat(fields[4], 32)
			m, err := strconv.Atoi(fields[5])
			f.mu.Lock()
			f.goTarget, f.goTargetOK = [3]float32{float32(x), float32(y), float32(z)}, true
			if err == nil {
				fromMap := f.status.Map
				if started && uint32(m) != f.status.Map {
					f.commanderInst = "fresh"
				}
				f.status.Map = uint32(m) // the go's map id takes effect at once; only a goLag test holds the position back
				switch {
				case started && f.goStuckFrom > 0 && f.recoveryGos >= f.goStuckFrom:
					// Accepted but never landed (the H1 review case): the map id follows and the position does not,
					// so every wait on the landing times out.
				default:
					if started && f.goLagEvery > 0 {
						f.goLag = f.goLagEvery
					}
					if started && f.goUnsettled > 0 {
						f.unsettledLeft = f.goUnsettled
					}
					if started && f.goLag > 0 {
						// The recovery's go: the position lands a few status polls later (the live server's landing).
						f.goPend, f.goPendOK = f.goTarget, true
					} else {
						f.x, f.y, f.z = float32(x), float32(y), float32(z)
						f.ownPos = [3]float32{f.x, f.y, f.z}
						f.status.X, f.status.Y, f.status.Z = f.x, f.y, f.z
						// The destination is reported, and the server's near teleport has landed: the session acks
						// it (TeleportSeq) whatever the run state, so a pre-start go (the GM clear's stops) acks
						// too. While goUnsettled reports the destination for a few polls (the live Q3 window), the
						// seq has not advanced yet.
						if f.unsettledLeft == 0 {
							f.goArrive(fromMap)
						} else if uint32(m) != fromMap {
							f.zStuck = true // a far teleport before the seq is acked still never settles z
						}
					}
				}
			}
			f.mu.Unlock()
		}
	case strings.HasPrefix(line, ".appear "):
		name := strings.TrimPrefix(line, ".appear ")
		f.mu.Lock()
		known := false
		for _, n := range f.party {
			known = known || n == name
		}
		accepted := !f.appearRefused && !(f.appearRefusedUntilRevive && !f.revived)
		if accepted && !(known && f.entered) {
			// The place phase's appear (dev, #354): the GM appears in the named player's copy, and only the status's
			// map follows (its waitMap).
			m := f.instMap
			if m == 0 {
				m = 249
			}
			f.status.Map = m
		}
		if accepted && known && f.entered {
			hm := uint32(249)
			if f.instMap != 0 {
				hm = f.instMap
			}
			p := f.pos[name]
			land := func() { // f.mu held
				fromMap := f.status.Map
				f.status.Map, f.commanderInst = hm, "hires"
				f.x, f.y, f.z = p[0], p[1], p[2]+2
				f.ownPos = [3]float32{f.x, f.y, f.z}
				f.status.X, f.status.Y, f.status.Z = f.x, f.y, f.z
				f.goArrive(fromMap)
			}
			if f.appearLag > 0 {
				f.appearPend, f.appearPendLeft = land, f.appearLag
			} else {
				land()
			}
		}
		f.mu.Unlock()
	case line == ".gm on":
		// The place phase's GM-mode probe (review round 1, item 12): the server answers a notification.
		if f.noGMOn {
			f.push("GM mode is OFF", "")
		} else {
			f.push("GM mode is ON", "")
		}
	case line == ".instance listbinds":
		// The core's HandleInstanceListBindsCommand: one line per player bind, then the two counts.
		for _, b := range f.instanceBinds {
			f.push(fmt.Sprintf("map: %d (Zul'Gurub) inst: %.0f perm: no canReset: yes TTR: %s", f.instMap, b.inst, b.ttr), "")
		}
		f.push("player binds: "+strconv.Itoa(len(f.instanceBinds)), "")
		f.push("group binds: 0", "")
	case line == ".gps":
		f.gpsSide()
	case line == ".pinfo":
		f.pinfoSide()
	case strings.HasPrefix(line, ".pinfo "):
		f.pinfoNameSide(strings.TrimPrefix(line, ".pinfo "))
	case line == ".die":
		f.dieSide()
	case strings.HasPrefix(line, ".summon "):
		name := strings.TrimPrefix(line, ".summon ")
		f.mu.Lock()
		if f.pos == nil {
			f.pos = map[string][3]float32{}
		}
		f.pos[name] = [3]float32{f.x, f.y, f.z}
		if f.instMap != 0 && f.status.Map == f.instMap {
			// A summon lands the hire in the commander's map and instance (the bind resume's path).
			f.entered = true
		}
		if f.lastUnsettled {
			f.summonUnsettled++
		}
		if f.goTargetOK {
			f.summonDist = append(f.summonDist, dist3([3]float32{f.status.X, f.status.Y, f.status.Z}, f.goTarget))
		}
		f.mu.Unlock()
	case strings.HasPrefix(line, ".revive "):
		f.mu.Lock()
		if f.instAtRevive == "" {
			f.instAtRevive = f.commanderInst
		}
		f.revived = true
		f.mu.Unlock()
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

// pinfoNameSide pushes the `.pinfo <name>` reply (the instance ledger's account read). The account id is stable
// per name, so a test can read the ledger entry back; no account name is put in the reply (the redactor would scrub
// it anyway).
func (f *walkFakeAgent) pinfoNameSide(name string) {
	id := 100
	for _, c := range name {
		id += int(c)
	}
	f.mu.Lock()
	f.lines = append([]queuedEvent{{raw: fmt.Sprintf("Human Warrior |cffffffff|Hplayer:%s|h[%s]|h|r (guid: 7) Account: acct (id: %d) GMLevel: 3", name, name, id)}}, f.lines...)
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

// talentSide is the game side for `.bot command <bot> talents [<path>]` (ChangeTalentsAction): the apply says "Apply spec ..." and
// the bare form reads the build back, both in the bot's own chat.
func (f *walkFakeAgent) talentSide(line string) {
	fields := strings.Fields(line)
	if len(fields) < 4 {
		return
	}
	name := fields[2]
	queue := func(q queuedEvent) {
		f.mu.Lock()
		f.lines = append(f.lines, q)
		f.mu.Unlock()
	}
	say := func(text string) { queue(queuedEvent{text: text, kind: events.KindChat, source: name}) }
	f.mu.Lock()
	f.talentLines = append(f.talentLines, line)
	mode := f.talentMode
	extra := f.talentReadExtra
	if f.talentTrees == nil {
		f.talentTrees = map[string][3]int{}
	}
	trees, ok := f.talentTrees[name]
	if !ok {
		trees = [3]int{8, 0, 43}
	}
	// The modes that leave the build alone: stuck (TB answers, nothing changes), silent, multiple, refused.
	applies := mode == "" || mode == "tbtext" || mode == "otherbot" || mode == "systemonly"
	if len(fields) == 5 && applies {
		switch fields[4] {
		case "beastmastery":
			trees = [3]int{45, 6, 0}
		case "marksmanship":
			trees = [3]int{9, 42, 0}
		case "survival":
			trees = [3]int{8, 0, 43}
		}
		f.talentTrees[name] = trees
	}
	f.mu.Unlock()
	// The forwarding command's own acknowledgement, a system line, as TB sends it.
	queue(queuedEvent{raw: fmt.Sprintf("Forwarded command for %s: %s; action success is not guaranteed.", name, strings.Join(fields[3:], " ")), text: ""})
	if mode == "silent" {
		return
	}
	if len(fields) == 5 {
		switch mode {
		case "multiple":
			say("Found multiple specs: marksmanship, survival")
		case "refused":
			say("Apply talents [" + fields[4] + "] talent link is invalid")
		default:
			say("Apply spec |h|cffffffff" + fields[4] + " 9/42/0")
		}
		return
	}
	text := fmt.Sprintf("My current talent spec is: |h|cffffffffHunter (%d/%d/%d) Link: http://x", trees[0], trees[1], trees[2])
	switch mode {
	case "tbtext":
		// TB's own colouring (formatSpec): the numbers are coloured, the closing parenthesis carries a reset.
		text = fmt.Sprintf("My current talent spec is: |h|cffffffffBM (|h|cff00ff00%d|h|cffffffff/|h|cff00ff00%d|h|cffffffff/%d|h|cffffffff)|r Hunter Link: http://x", trees[0], trees[1], trees[2])
	case "otherbot":
		queue(queuedEvent{text: text, kind: events.KindWhisper, source: "Bot03"})
		return
	case "systemonly":
		queue(queuedEvent{raw: text})
		return
	}
	say(text)
	if extra != "" {
		queue(queuedEvent{raw: extra})
	}
}

func (f *walkFakeAgent) push(raw, text string) {
	f.mu.Lock()
	f.lines = append(f.lines, queuedEvent{raw: raw, text: text})
	f.mu.Unlock()
}

// pushAt queues a line the events RPC serves only once the fake clock reaches due.
func (f *walkFakeAgent) pushAt(raw, text string, due time.Time) {
	f.mu.Lock()
	f.lines = append(f.lines, queuedEvent{raw: raw, text: text, due: due})
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

// eventsPage pops the next page off the queue, but retains every served
// line in history: a cursor rewind to an older `since` (waitTBMMatch, the
// gm-instance-data firing) re-serves the tail from history instead of
// losing it, the way the live agent's cursor does.
func (f *walkFakeAgent) eventsPage() events.Page {
	return f.eventsPageSince(f.since)
}

func (f *walkFakeAgent) eventsPageSince(since uint64) events.Page {
	f.mu.Lock()
	if since > 0 && since < uint64(len(f.history)) {
		n := 1
		if f.pageSize > n {
			n = f.pageSize
		}
		if n > len(f.history)-int(since) {
			n = len(f.history) - int(since)
		}
		batch := append([]queuedEvent{}, f.history[since:since+uint64(n)]...)
		evs := make([]events.Event, 0, n)
		for i, q := range batch {
			evs = append(evs, q.event(since+uint64(i)+1))
		}
		next := since + uint64(n)
		// Next is the last served seq, not the high-water mark: the caller
		// re-reads forward from here, re-serving whatever history holds.
		hook := f.eventsHook
		f.mu.Unlock()
		if hook != nil {
			for _, q := range batch {
				hook(q.raw)
			}
		}
		return events.Page{Events: evs, Next: next}
	}
	if len(f.lines) == 0 {
		next := f.next
		f.mu.Unlock()
		return events.Page{Next: next}
	}
	// The head line is held back until the fake clock reaches its due time. Lines are a log, so a due line blocks
	// the ones behind it too.
	if due := f.lines[0].due; !due.IsZero() && f.clock.now().Before(due) {
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
	f.history = append(f.history, batch...)
	evs := make([]events.Event, 0, n)
	for _, q := range batch {
		f.next++
		evs = append(evs, q.event(f.next))
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

// spellPage delivers the queued cast events whose time the fake clock reached. It shares f.next with the system
// events, so both cursors move forward without ever re-reading a line.
func (f *walkFakeAgent) spellPage(since uint64) events.Page {
	f.mu.Lock()
	defer f.mu.Unlock()
	now := f.clock.now()
	var evs []events.Event
	for len(f.spellQueue) > 0 && !f.spellQueue[0].at.After(now) {
		q := f.spellQueue[0]
		f.spellQueue = f.spellQueue[1:]
		f.next++
		ev := q.ev
		ev.Seq = f.next
		evs = append(evs, ev)
	}
	next := f.next
	if next < since {
		next = since
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
	combatBots := map[string]bool{}
	for k, v := range f.combatBots {
		combatBots[k] = v
	}
	offMapBots := map[string]bool{}
	for k, v := range f.offMapBots {
		offMapBots[k] = v
	}
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
	extra := map[string]tbm.PartyRec{}
	for k, v := range f.partyExtra {
		extra[k] = v
	}
	noParty := f.noPartyFrames
	ghostsOff := f.ghostsOff && f.runStarted // released once the run is under way
	ghostsOffN := 0
	if f.runStarted {
		ghostsOffN = f.ghostsOffN // a partial release: only the first N dead hires show on map 0
	}
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
		if combatBots[name] {
			state = "c"
		}
		if viewHook != nil {
			if _, omit := viewHook(n); omit == name {
				continue
			}
		}
		p := pos[name]
		hm := m
		if (ghostsOff && dead[name]) || (ghostsOffN > 0 && dead[name] && i < ghostsOffN) {
			hm = 0
		}
		if offMapBots[name] {
			hm = 0
		}
		rec := tbm.PartyRec{GUID: packet.GUID(1000 + i), Name: name, HP: 1, MaxHP: 1, Map: hm, Dead: dead[name], State: state,
			X: p[0], Y: p[1], Z: p[2]}
		if ex, ok := extra[name]; ok { // the P-record fields the module sends beyond the fake's own
			rec.Target, rec.Spell, rec.Action, rec.Result = ex.Target, ex.Spell, ex.Action, ex.Result
			rec.Power, rec.MaxPower, rec.PowerType = ex.Power, ex.MaxPower, ex.PowerType
		}
		frame.Party = append(frame.Party, rec)
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
			if f.enterMap != 0 {
				f.status.Map = f.enterMap
			}
			f.mu.Unlock()
		}
		return ack("cmd " + p.Verb)
	case "run":
		f.mu.Lock()
		f.runVerbs = append(f.runVerbs, p.Verb)
		f.mu.Unlock()
		if p.Verb == "pause" {
			f.mu.Lock()
			fail := f.pauseErr != nil && *f.pauseErr
			n := 0
			for _, v := range f.runVerbs {
				if v == "pause" {
					n++
				}
			}
			// runVerbs already holds this pause: the first pauseFails pauses refuse, later ones land.
			if f.pauseFails > 0 && n > f.pauseFails {
				fail = false
			}
			msg := f.pauseErrMsg
			hook := f.onPause
			f.mu.Unlock()
			if fail {
				if msg == "" {
					msg = "the run is busy"
				}
				code := f.pauseErrCode
				if code == "" {
					code = "refused"
				}
				return commanderReply{Reply: tbm.Reply{Kind: "err", Err: &tbm.Err{Intent: "run pause", Code: code,
					Message: msg}}}
			}
			if hook != nil {
				hook()
			}
		}
		if p.Verb == "skip" {
			f.mu.Lock()
			hook, refuse := f.onSkip, f.skipErr
			f.mu.Unlock()
			if refuse {
				return commanderReply{Reply: tbm.Reply{Kind: "err", Err: &tbm.Err{Intent: "run skip", Code: "refused",
					Message: "the step cannot be skipped"}}}
			}
			if hook != nil {
				hook()
			}
		}
		if p.Verb == "start" {
			f.mu.Lock()
			f.runStarted = true
			f.runEnded = false
			f.startArgs = strings.Join(p.Args, "|")
			q := f.startQueue
			f.startQueue = nil
			f.mu.Unlock()
			for _, e := range q {
				f.push(e.raw, e.text)
			}
			if hook := f.onStart; hook != nil {
				hook()
			}
			if f.startReply != nil {
				return *f.startReply
			}
			rep := ack("run start")
			run := "TBM:RUN|" + p.Args[0] + "|running|1/21|onyxia|move|walk|walking|until boss (step 21)"
			rep.Reply.Lines = append([]string{run}, rep.Reply.Lines...)
			return rep
		}
		if p.Verb == "status" {
			if fn := f.runStatusFn; fn != nil {
				if rep := fn(); rep != nil {
					return *rep
				}
			}
			rep := ack("commander status")
			f.mu.Lock()
			names := strings.Join(f.party, ", ")
			f.mu.Unlock()
			rep.Reply.Lines = append([]string{fmt.Sprintf("You command Bot01's group: %s.", names)}, rep.Reply.Lines...)
			return rep
		}
		if p.Verb == "resume" {
			f.mu.Lock()
			ended := f.runEnded
			hook := f.onResume
			f.mu.Unlock()
			if ended {
				return commanderReply{Reply: tbm.Reply{Kind: "err", Err: &tbm.Err{Intent: "run resume", Code: "no-run",
					Message: "No run is going"}}}
			}
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

// roleOrder is every `.bot role` line the walk sent, in order.
func (f *walkFakeAgent) roleOrder() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string{}, f.roleLines...)
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
	// commandersRaw are the /api/v1/commanders answers in order, the last one repeating (nil: an empty list);
	// commandersFn builds one from the test's own state (a dashboard view of the wipe, F4's self-regroup test)
	// and wins over commandersRaw.
	commandersRaw []json.RawMessage
	commandersFn  func() json.RawMessage
	decisions     []tbdash.DecisionCapture
	decisionsErr  error
	decFiles      map[string]string // file name (index.json or seg-*.jsonl) to body
	decFileErr    map[string]error
	decRequested  []string // every DecisionFile file name requested, in order
	decCalls      int      // Decisions calls (the ended wait polls again)
	// fightsStallFirst is how many of the first Fights calls block until their context ends (or fightsStall passes).
	fightsStallFirst int
	fightsStall      time.Duration
	fightsCalls      int
	// fightsSeq are the /api/v1/combat/fights answers in order, the last one repeating (nil: d.fights).
	fightsSeq [][]walkFightRow
}

func (d *fakeDash) Login(ctx context.Context) error { return nil }

func (d *fakeDash) Commanders(ctx context.Context) (json.RawMessage, error) {
	d.mu.Lock()
	d.commanders++
	fn, raws := d.commandersFn, d.commandersRaw
	if len(raws) > 1 {
		d.commandersRaw = raws[1:]
	}
	d.mu.Unlock()
	if fn != nil {
		return fn(), nil
	}
	if len(raws) > 0 {
		return raws[0], nil
	}
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
	d.fightsCalls++
	stall := d.fightsStallFirst > 0
	if stall {
		d.fightsStallFirst--
	}
	d.mu.Unlock()
	if stall {
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-time.After(d.fightsStall):
		}
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.fightsErr != nil {
		return nil, d.fightsErr
	}
	fights := d.fights
	if len(d.fightsSeq) > 1 {
		fights = d.fightsSeq[0]
		d.fightsSeq = d.fightsSeq[1:]
	} else if len(d.fightsSeq) == 1 {
		fights = d.fightsSeq[0]
	}
	raw, err := json.Marshal(fights)
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

func (d *fakeDash) Decisions(ctx context.Context, commander string, since, until time.Time) ([]tbdash.DecisionCapture, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.decCalls++
	if d.decisionsErr != nil {
		return nil, d.decisionsErr
	}
	return append([]tbdash.DecisionCapture{}, d.decisions...), nil
}

func (d *fakeDash) DecisionFile(ctx context.Context, id, file string, dst io.Writer, max int64) (int64, error) {
	d.mu.Lock()
	d.decRequested = append(d.decRequested, file)
	body, hasBody := d.decFiles[file]
	ferr := d.decFileErr[file]
	d.mu.Unlock()
	if ferr != nil {
		return 0, ferr
	}
	if !hasBody {
		return 0, fmt.Errorf("no such decision file %s", file)
	}
	if int64(len(body)) > max {
		return int64(len(body)), fmt.Errorf("dashboard decision file %s/%s: over the %d-byte limit", id, file, max)
	}
	n, err := io.WriteString(dst, body)
	return int64(n), err
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
		resetWait: 5 * time.Minute, wipeResume: 1, wipeRest: 10 * time.Minute, hireSettle: hireSettleWait, out: out,
		decisions: true, decisionsMaxMiB: decisionsBudgetDefaultMiB,
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
	for step := 2; step <= 20; step++ {
		q = append(q, queuedEvent{raw: fmt.Sprintf("TBM:RUN|%s|running|%d/21|onyxia|move|walk|walking", plan, step)})
	}
	q = append(q, queuedEvent{raw: fmt.Sprintf("TBM:RUN|%s|done|20/21|onyxia|move|walk|stopped before step 21 (until boss): the party stays where it stands", plan)})
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
			fmt.Sprintf("TBM:RUN|%s|running|%d/21|onyxia|move|walk|walking|until boss (step 21)", plan, step),
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

func TestRaidWalkDecisionsDryRunLine(t *testing.T) {
	f := newFixture(t)
	f.opts.dryRun = true
	f.opts.decisions = false
	if code := f.run(context.Background()); code != exitOK {
		t.Fatalf("dry run exit %d: %s", code, f.stderr.String())
	}
	want := " 14 decisions: " + f.out + "/decisions (dashboard: off)\n"
	if !strings.Contains(f.stdout.String(), want) {
		t.Fatalf("dry-run plan lacks %q:\n%s", want, f.stdout.String())
	}
}

func TestRaidWalkDecisionsPulled(t *testing.T) {
	f := newFixture(t)
	f.opts.dashURL, f.opts.dashPassFile = "http://dash.test", "/tmp/pass"
	ended := f.clock.now()
	f.dash.decisions = []tbdash.DecisionCapture{
		{ID: "20261029T200000Z-Commander-molten_core", Commander: "Commander", Plan: "molten_core", Map: 1, Started: f.clock.now().Add(-time.Minute), Ended: &ended, Segments: []tbdash.DecisionSegment{
			{File: "seg-0000.jsonl", FirstSeq: 1, LastSeq: 5, Records: 5, Bytes: 5},
			{File: "seg-0001.jsonl", FirstSeq: 6, LastSeq: 8, Records: 3, Bytes: 3, Deleted: true},
		}},
		{ID: "20261029T200100Z-Commander-molten_core", Commander: "Commander", Plan: "molten_core", Map: 1, Started: f.clock.now().Add(-time.Minute), Ended: &ended},
	}
	f.dash.decFiles = map[string]string{
		"index.json":     `{"id":"x"}`,
		"seg-0000.jsonl": "{\"seq\":1}\n",
	}
	f.defaultRun("onyxias_lair")
	code := f.run(context.Background())
	if code != exitOK {
		t.Fatalf("exit %d, want 0: %s\n%s", code, f.stderr.String(), f.stdout.String())
	}
	sum := f.summary()
	if sum.Decisions == nil || len(sum.Decisions.Captures) != 2 {
		t.Fatalf("decisions %+v, want the 2 captures of this commander", sum.Decisions)
	}
	for _, name := range f.dash.decRequested {
		if name == "seg-0001.jsonl" {
			t.Fatalf("deleted segment was requested: %q", f.dash.decRequested)
		}
	}
	if _, err := os.Stat(filepath.Join(f.out, "decisions", "20261029T200000Z-Commander-molten_core", "seg-0000.jsonl")); err != nil {
		t.Fatalf("segment not pulled: %v (requested %q)", err, f.dash.decRequested)
	}
	raw, err := os.ReadFile(filepath.Join(f.out, "decisions", "manifest.json"))
	if err != nil {
		t.Fatalf("manifest.json: %v", err)
	}
	var man []decisionManifestCapture
	if err := json.Unmarshal(raw, &man); err != nil {
		t.Fatalf("manifest: %v", err)
	}
	if len(man) != 2 {
		t.Fatalf("manifest %d captures, want 2: %s", len(man), raw)
	}
	if leftovers, _ := filepath.Glob(filepath.Join(f.out, "decisions", "*", "*.part")); len(leftovers) != 0 {
		t.Fatalf(".part files left behind: %q", leftovers)
	}
	st, err := os.Stat(filepath.Join(f.out, "decisions", "manifest.json"))
	if err != nil {
		t.Fatal(err)
	}
	if st.Mode().Perm() != 0o600 {
		t.Fatalf("manifest mode %o, want 600", st.Mode().Perm())
	}
}

func TestRaidWalkDecisionsNoDashboard(t *testing.T) {
	f := newFixture(t)
	f.defaultRun("onyxias_lair")
	code := f.run(context.Background())
	if code != exitOK {
		t.Fatalf("exit %d, want 0: %s", code, f.stderr.String())
	}
	sum := f.summary()
	if sum.Decisions == nil || sum.Decisions.Note != "off (no dashboard)" {
		t.Fatalf("decisions %+v, want the no-dashboard note", sum.Decisions)
	}
}

func TestRaidWalkDecisionsBudgetAndFailure(t *testing.T) {
	f := newFixture(t)
	f.opts.dashURL, f.opts.dashPassFile = "http://dash.test", "/tmp/pass"
	f.opts.decisionsMaxMiB = 1
	ended := f.clock.now()
	f.dash.decisions = []tbdash.DecisionCapture{
		{ID: "20261029T200000Z-Commander-molten_core", Commander: "Commander", Ended: &ended, Started: f.clock.now()},
		{ID: "20261029T200100Z-Commander-molten_core", Commander: "Commander", Ended: &ended, Started: f.clock.now()},
	}
	f.dash.decFiles = map[string]string{"index.json": string(make([]byte, 2*1024*1024))}
	f.defaultRun("onyxias_lair")
	code := f.run(context.Background())
	if code != exitOK {
		t.Fatalf("exit %d, want 0 (the pull never changes the exit code)", code)
	}
	sum := f.summary()
	if sum.Decisions == nil || !strings.Contains(sum.Decisions.Note, "budget") {
		t.Fatalf("decisions %+v, want a budget note", sum.Decisions)
	}
}

func TestRaidWalkDecisionsServerIDRefused(t *testing.T) {
	f := newFixture(t)
	f.opts.dashURL, f.opts.dashPassFile = "http://dash.test", "/tmp/pass"
	ended := f.clock.now()
	f.dash.decisions = []tbdash.DecisionCapture{
		{ID: "../evil", Commander: "Commander", Ended: &ended, Started: f.clock.now()},
		{ID: "20261029T200000Z-Commander-molten_core", Commander: "Commander", Ended: &ended, Started: f.clock.now()},
	}
	f.dash.decFiles = map[string]string{"index.json": `{"id":"x"}`}
	f.defaultRun("onyxias_lair")
	if code := f.run(context.Background()); code != exitOK {
		t.Fatalf("exit %d, want 0", code)
	}
	sum := f.summary()
	if sum.Decisions == nil || len(sum.Decisions.Captures) != 1 {
		t.Fatalf("decisions %+v, want only the well-formed id pulled", sum.Decisions)
	}
}

func TestRaidWalkDecisionsFileFailureKeepsOthers(t *testing.T) {
	f := newFixture(t)
	f.opts.dashURL, f.opts.dashPassFile = "http://dash.test", "/tmp/pass"
	ended := f.clock.now()
	f.dash.decisions = []tbdash.DecisionCapture{
		{ID: "20261029T200000Z-Commander-molten_core", Commander: "Commander", Ended: &ended, Started: f.clock.now(), Segments: []tbdash.DecisionSegment{{File: "seg-0000.jsonl"}}},
		{ID: "20261029T200100Z-Commander-molten_core", Commander: "Commander", Ended: &ended, Started: f.clock.now()},
	}
	f.dash.decFiles = map[string]string{"index.json": `{"id":"x"}`}
	f.dash.decFileErr = map[string]error{"seg-0000.jsonl": errors.New("HTTP 500 for GET /api/v1/decisions")}
	f.defaultRun("onyxias_lair")
	code := f.run(context.Background())
	if code != exitOK {
		t.Fatalf("exit %d, want 0 (a failed file notes and the rest continue)", code)
	}
	sum := f.summary()
	if sum.Decisions == nil || len(sum.Decisions.Captures) != 1 {
		t.Fatalf("decisions %+v, want the good capture pulled and the failed one noted", sum.Decisions)
	}
	found := false
	for _, n := range sum.Notes {
		if strings.Contains(n, "20261029T200000Z-Commander-molten_core") {
			found = true
		}
	}
	if !found {
		t.Fatalf("no failed-file note in %q", sum.Notes)
	}
}

// TestRaidWalkDecisionsFailedRepullKeepsComplete: an existing complete capture survives a failed
// re-pull — the failure removes only the staging dir, never the target.
func TestRaidWalkDecisionsFailedRepullKeepsComplete(t *testing.T) {
	out := t.TempDir()
	ended := time.Now()
	id := "20261029T200000Z-Commander-molten_core"
	good := &fakeDash{
		decisions: []tbdash.DecisionCapture{
			{ID: id, Commander: "Commander", Ended: &ended, Started: ended.Add(-time.Minute)},
		},
		decFiles: map[string]string{"index.json": `{"id":"x"}`},
	}
	if _, notes := pullDecisionsInto(context.Background(), good, "Commander", time.Time{}, time.Time{}, out, 1<<20); len(notes) != 0 {
		t.Fatalf("first pull notes %q", notes)
	}
	want, err := os.ReadFile(filepath.Join(out, "decisions", id, "index.json"))
	if err != nil {
		t.Fatalf("first pull: %v", err)
	}
	bad := &fakeDash{
		decisions: []tbdash.DecisionCapture{
			{ID: id, Commander: "Commander", Ended: &ended, Started: ended.Add(-time.Minute)},
		},
		decFiles:   map[string]string{"index.json": `{"id":"x"}`},
		decFileErr: map[string]error{"index.json": errors.New("HTTP 500 for GET /api/v1/decisions")},
	}
	if _, notes := pullDecisionsInto(context.Background(), bad, "Commander", time.Time{}, time.Time{}, out, 1<<20); len(notes) == 0 {
		t.Fatal("failed re-pull: no note")
	}
	got, err := os.ReadFile(filepath.Join(out, "decisions", id, "index.json"))
	if err != nil {
		t.Fatalf("complete capture lost on failed re-pull: %v", err)
	}
	if !bytes.Equal(got, want) {
		t.Fatalf("complete capture changed on failed re-pull: %q, want %q", got, want)
	}
	if leftovers, _ := filepath.Glob(filepath.Join(out, "decisions", id+"*")); len(leftovers) != 1 {
		t.Fatalf("staging left behind or target touched: %q", leftovers)
	}
	if stag, _ := filepath.Glob(filepath.Join(out, "decisions", ".*staging*")); len(stag) != 0 {
		t.Fatalf("staging dirs left behind: %q", stag)
	}
}

// TestRaidWalkDecisionsUnknownSegmentFailsCapture: a non-deleted segment the file regex rejects
// fails that capture with a note; it is not in the success manifest.
func TestRaidWalkDecisionsUnknownSegmentFailsCapture(t *testing.T) {
	f := newFixture(t)
	f.opts.dashURL, f.opts.dashPassFile = "http://dash.test", "/tmp/pass"
	ended := f.clock.now()
	f.dash.decisions = []tbdash.DecisionCapture{
		{ID: "20261029T200000Z-Commander-molten_core", Commander: "Commander", Ended: &ended, Started: f.clock.now(), Segments: []tbdash.DecisionSegment{{File: "seg-evil!.jsonl"}}},
		{ID: "20261029T200100Z-Commander-molten_core", Commander: "Commander", Ended: &ended, Started: f.clock.now()},
	}
	f.dash.decFiles = map[string]string{"index.json": `{"id":"x"}`}
	f.defaultRun("onyxias_lair")
	if code := f.run(context.Background()); code != exitOK {
		t.Fatalf("exit %d, want 0 (the unknown segment notes, the rest continues)", code)
	}
	sum := f.summary()
	if sum.Decisions == nil || len(sum.Decisions.Captures) != 1 {
		t.Fatalf("decisions %+v, want only the good capture", sum.Decisions)
	}
	if sum.Decisions.Captures[0].ID != "20261029T200100Z-Commander-molten_core" {
		t.Fatalf("decisions %+v, want the good capture kept", sum.Decisions)
	}
	found := false
	for _, n := range sum.Notes {
		if strings.Contains(n, "seg-evil!.jsonl") {
			found = true
		}
	}
	if !found {
		t.Fatalf("no unknown-segment note in %q", sum.Notes)
	}
	raw, err := os.ReadFile(filepath.Join(f.out, "decisions", "manifest.json"))
	if err != nil {
		t.Fatalf("manifest.json: %v", err)
	}
	var man []decisionManifestCapture
	if err := json.Unmarshal(raw, &man); err != nil {
		t.Fatalf("manifest: %v", err)
	}
	if len(man) != 1 || man[0].ID != "20261029T200100Z-Commander-molten_core" {
		t.Fatalf("manifest %s, want only the good capture", raw)
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
plan ../../dungeons/onyxias_lair.json, map 249, 2 legs, 21 steps, raid 40
roster 40 bots: mt 1 warrior:tank, ot 3t, rest 10h 26d, level 60, tier 2 (table)
until boss; from -; enter auto; server m910q slot any holder raid-walk-onyxias_lair
out ` + f.out + `
phases:
  1 validate: plan, roster, until, 21 steps
  2 lease: m910q any --holder raid-walk-onyxias_lair --ttl 15m (renewed every 5m)
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
 14 decisions: ` + f.out + `/decisions (dashboard: on)
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
	if sum.EndReason != "until" || sum.StepsReached != 20 || sum.StepsTotal != 21 {
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
		if !raidwalk.AllowedLine(line, hires, nil, 249) {
			t.Fatalf("a GM line fails the allow-list: %q", line)
		}
	}
	if f.agent.startArgs != "onyxias_lair|until|boss" {
		t.Fatalf("run start args %q", f.agent.startArgs)
	}
	if !strings.Contains(f.stdout.String(), "raid-walk onyxias_lair: until; steps 20/21") {
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
	// 29-world statuses for the 29-step fixture migration (the default run is the new plan's 21).
	var q []queuedEvent
	for step := 2; step <= 28; step++ {
		q = append(q, queuedEvent{raw: fmt.Sprintf("TBM:RUN|%s|running|%d/29|onyxia|move|walk|walking", "onyxias_lair", step)})
	}
	q = append(q, queuedEvent{raw: "TBM:RUN|onyxias_lair|done|28/29|onyxia|move|walk|stopped before step 29 (until boss): the party stays where it stands"})
	f.agent.mu.Lock()
	f.agent.startQueue = q
	f.agent.mu.Unlock()
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
				"TBM:RUN|onyxias_lair|running|1/21|onyxia|move|walk|walking",
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

// startsAndSkips is a fake's `run` verbs without the `run status` asks: the re-run's stop confirmation polls the status, so the
// verbs a test counts are the ones that change the run.
func startsAndSkips(verbs []string) []string {
	out := make([]string, 0, len(verbs))
	for _, v := range verbs {
		if v != "status" {
			out = append(out, v)
		}
	}
	return out
}

func TestRaidWalkReadsRawLinesNotStrippedText(t *testing.T) {
	f := newFixture(t)
	var q []queuedEvent
	for step := 2; step <= 20; step++ {
		q = append(q, queuedEvent{raw: fmt.Sprintf("TBM:RUN|onyxias_lair|running|%d/21|onyxia|move|walk|walking", step)})
	}
	// The walk-3 lesson (spec 0.1): this event's text lost the "|r" of the plan name, the raw line did not.
	q = append(q, queuedEvent{
		raw:  "TBM:RUN|onyxias_lair|done|20/21|onyxia|move|walk|stopped before step 21 (until boss): the party stays where it stands",
		text: "TBM:RUN|onyxias_lairunning|done|20/21|onyxia|move|walk|stopped before step 21 (until boss): the party stays where it stands",
	})
	f.agent.mu.Lock()
	f.agent.startQueue = q
	f.agent.mu.Unlock()
	code := f.run(context.Background())
	if code != exitOK {
		t.Fatalf("exit %d: %s", code, f.stderr.String())
	}
	if sum := f.summary(); sum.EndReason != "until" || sum.StepsReached != 20 {
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

// retirePack0Patrol shows pack 0's entrance Warder dead at its spawn in the
// run's first view frames: the fixture runs start past it (steps 10+), so its
// patrol no longer blocks the entrance revive point. The dead frame hostile
// binds to the plan patrol (first seen at the pack spawn) and reads all-dead,
// which retires it; the quiet check reads the units feed, so the corpse never
// blocks it. The corpse leaves the frames after the enter phase: the reset
// wait needs every recently seen hostile at 100 %, so a corpse in every frame
// would hold it open, while the patrol binding keeps the stale track entry.
func retirePack0Patrol(f *fixture) {
	prev := f.agent.hostilesFn
	dead := tbm.HostileRec{GUID: packet.GUID(0x5205001), Entry: 12129,
		X: 49.8206, Y: -114.607, Z: -24.3112, HPPct: 0}
	f.agent.hostilesFn = func(n int) []tbm.HostileRec {
		var out []tbm.HostileRec
		if prev != nil {
			out = append(out, prev(n)...)
		}
		if n <= 10 {
			out = append(out, dead)
		}
		return out
	}
}

func TestRaidWalkWipeRevivesOnlyWhenQuiet(t *testing.T) {
	run := func(t *testing.T, neverQuiet bool) (*fixture, raidwalk.Summary) {
		f := newFixture(t)
		f.agent.startReply = startAt("onyxias_lair", 13)
		retirePack0Patrol(f)
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
			resumeHook(f, 14, 20, &resumeAt)
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

// placedFollower is placedIdleBlocker for a unit that follows the commander at yd out: every fallback point of the
// revive, the entrance included, is blocked too (the fallback rows sit hundreds of yards from the place point).
func placedFollower(f *fixture, yd float32, entry uint32, name string, inCombat bool) func(int) []objects.Unit {
	return func(int) []objects.Unit {
		f.agent.mu.Lock()
		x, y, z := f.agent.x, f.agent.y, f.agent.z
		f.agent.mu.Unlock()
		return []objects.Unit{{Type: "creature", Name: name, Entry: entry, Hostile: true, InCombat: inCombat,
			X: x + yd, Y: y, Z: z}}
	}
}

// wipeOnceAfterStart is wipeAfterStart for a test whose run goes on after the recovery: the raid dies once, on the
// first running line, so the running line the resume hook queues does not kill the revived party again.
func wipeOnceAfterStart(f *fixture) {
	wipeAfterStart(f)
	hook := f.agent.eventsHook
	killed := false
	f.agent.eventsHook = func(raw string) {
		if killed || !strings.Contains(raw, "|running|") {
			return
		}
		killed = true
		hook(raw)
	}
}

// resumePlacedRun answers `run resume` with the placed ZG run going on and stopping at its until: the same server
// behaviour resumeHook fakes for the Onyxia fixture, whose line format is hard-coded to onyxia.
func resumePlacedRun(f *fixture) {
	n := zgPlaceSteps(f.t)
	f.agent.onResume = func() {
		f.agent.mu.Lock()
		defer f.agent.mu.Unlock()
		f.agent.lines = append(f.agent.lines,
			queuedEvent{raw: fmt.Sprintf("TBM:RUN|zul_gurub|running|%d/%d|thekal|move|walk|resumed", n.start+1, n.total)},
			queuedEvent{raw: fmt.Sprintf("TBM:RUN|zul_gurub|done|%d/%d|thekal|move|walk|stopped before step %d (until boss): the party stays where it stands", n.start+1, n.total, n.until)})
	}
}

// walk 25, hardened by Codex HIGH (turtle-agent#360): D-A lets an idle unit
// beyond the placement's 25 yd landing reach through only at its proven home
// (a plan spawn within 3 yd). A still, calm mob standing 8 yd from its spawn
// is a paused patrol or a wanderer, so the wipe ends at 19; at its own spawn
// the wipe recovers instead.
func TestRaidWalkWipeRevivesPastIdleHomeMob(t *testing.T) {
	// Away from home: still and calm 44.6 yd out (it follows the commander, so every fallback point and the
	// entrance are blocked too), but the plan's nearest same-entry spawn is ~230 yd off (Marli's pack, leg 2), so
	// the revive is refused.
	t.Run("away from home blocks", func(t *testing.T) {
		f, _ := newPlaceFixture(t)
		f.agent.startReply = zgPlaceReply(t)
		f.agent.unitsFn = placedFollower(f, 44.6, 14532, "Razzashi Venombrood", false)
		wipeOnceAfterStart(f)
		if code := f.run(context.Background()); code != exitRaidWalkIncident {
			t.Fatalf("exit %d, want 19: %s\n%s\n%s", code, f.stderr.String(), f.stdout.String(), f.summaryText())
		}
		rr := f.summary().ReviveRefusal
		if rr == nil {
			t.Fatal("no revive_refusal with the away-from-home blocker in place")
		}
	})
	// At home: an onyxia walk whose revive point is step 1 (49.8 -114.6),
	// blocked by entry 12129 standing on the leg-0 group-1 spawn (-53.0
	// -96.9, 35 yd out), so the wipe recovers.
	t.Run("at home revives", func(t *testing.T) {
		f := newFixture(t)
		f.agent.startReply = startAt("onyxias_lair", 13)
		retirePack0Patrol(f)
		f.agent.unitsFn = func(int) []objects.Unit {
			return []objects.Unit{{Type: "creature", Name: "Onyxian Warder", Entry: 12129, Hostile: true,
				X: -52.9699, Y: -96.8813, Z: -38.6419}}
		}
		f.allDeadFromHires()
		var resumeAt time.Time
		resumeHook(f, 14, 20, &resumeAt)
		code := f.run(context.Background())
		if code != exitOK {
			t.Fatalf("exit %d, want 0: %s\n%s\n%s", code, f.stderr.String(), f.stdout.String(), f.summaryText())
		}
		sum := f.summary()
		if sum.WipeResumes != 1 || sum.EndReason != "until" {
			t.Fatalf("wipe_resumes %d end %q, want 1 and until", sum.WipeResumes, sum.EndReason)
		}
		if sum.ReviveRefusal != nil {
			t.Fatalf("the revive was refused with the at-home blocker in place: %+v", sum.ReviveRefusal)
		}
		if n := countPrefix(f.agent.gmLines(), ".summon "); n == 0 {
			t.Fatalf("no summons: the revive never reached its summon stage: %q", f.agent.gmLines())
		}
	})
}

// D-C: the 120 s check gives up after 24 reads, so a blocker that leaves later still gets the revive: the walk
// re-reads every 10 s until waitEnd.
func TestRaidWalkReviveWaitsForBlockerToLeave(t *testing.T) {
	f, _ := newPlaceFixture(t)
	f.agent.startReply = zgPlaceReply(t)
	// An in-combat unit at 30 yd: not idle (D-A does not exempt it), so the check runs its full 24 reads. It leaves
	// after 30 reads, a few of which are the D-C wait's.
	const leaves = 30
	unitsAtSummon := 0
	f.agent.unitsFn = func(reads int) []objects.Unit {
		unitsAtSummon = reads
		if reads > leaves {
			return []objects.Unit{}
		}
		f.agent.mu.Lock()
		x, y, z := f.agent.x, f.agent.y, f.agent.z
		f.agent.mu.Unlock()
		return []objects.Unit{{Type: "creature", Name: "Razzashi Skitterer", Entry: 11338, Hostile: true, InCombat: true,
			X: x + 30, Y: y, Z: z}}
	}
	wipeOnceAfterStart(f)
	resumePlacedRun(f)
	code := f.run(context.Background())
	if code != exitOK {
		t.Fatalf("exit %d, want 0: %s\n%s\n%s", code, f.stderr.String(), f.stdout.String(), f.summaryText())
	}
	f.agent.mu.Lock()
	gm := append([]string{}, f.agent.gm...)
	f.agent.mu.Unlock()
	visibleOff, firstSummon := -1, -1
	for i, line := range gm {
		if visibleOff < 0 && line == ".gm visible off" {
			visibleOff = i
		}
		if firstSummon < 0 && strings.HasPrefix(line, ".summon ") {
			firstSummon = i
		}
	}
	if visibleOff < 0 || firstSummon < 0 {
		t.Fatalf("no revive: visible off %d, first summon %d: %q", visibleOff, firstSummon, gm)
	}
	if unitsAtSummon <= leaves {
		t.Fatalf("the first summon came after %d units calls; the blocker was still there (it leaves after read %d)", unitsAtSummon, leaves)
	}
	if sum := f.summary(); sum.ReviveRefusal != nil {
		t.Fatalf("the revive was refused with the blocker gone: %+v", sum.ReviveRefusal)
	}
}

// Codex round 2: the commander is moved off the revive point's map during the D-C wait (a teleport, a graveyard
// transfer): a read from there sees nothing, but it judges surroundings the revive will not have, so no summon goes out.
func TestRaidWalkReviveQuietNeedsCommanderOnTheMap(t *testing.T) {
	f, _ := newPlaceFixture(t)
	f.agent.startReply = zgPlaceReply(t)
	rows, place := placePlanRows(t)
	// D-C starts after the revive point's 24-read check, one read per row fallback, and the entrance's 24-read check.
	moved := 24 + len(reviveFallbackRows(rows, place.Start)) + 24 + 1
	f.agent.unitsFn = func(reads int) []objects.Unit {
		if reads > moved {
			return []objects.Unit{} // the area reads empty from where the commander now stands
		}
		if reads == moved {
			f.agent.mu.Lock()
			f.agent.status.Map = 0 // moved off the map; the next read's status check sees it
			f.agent.mu.Unlock()
		}
		f.agent.mu.Lock()
		x, y, z := f.agent.x, f.agent.y, f.agent.z
		f.agent.mu.Unlock()
		return []objects.Unit{{Type: "creature", Name: "Razzashi Skitterer", Entry: 11338, Hostile: true, InCombat: true,
			X: x + 30, Y: y, Z: z}}
	}
	wipeOnceAfterStart(f)
	resumePlacedRun(f)
	f.run(context.Background())
	f.agent.mu.Lock()
	gm := append([]string{}, f.agent.gm...)
	f.agent.mu.Unlock()
	for _, line := range gm {
		if strings.HasPrefix(line, ".summon ") || strings.HasPrefix(line, ".revive ") {
			t.Fatalf("revived after the commander left the map: %q in %q", line, gm)
		}
	}
}

// D-C's waitEnd leaves the rest and the resume that follow inside the recovery deadline: the walk then ends with the
// revive refusal (exit 19, "not revived"), and not with "wipe recovery timed out".
func TestRaidWalkReviveWaitEndsBeforeRest(t *testing.T) {
	f, _ := newPlaceFixture(t)
	f.opts.wipeRest = 5 * time.Minute
	f.agent.startReply = zgPlaceReply(t)
	f.agent.unitsFn = placedFollower(f, 30, 14532, "Razzashi Venombrood", true)
	wipeAfterStart(f)
	code := f.run(context.Background())
	if code != exitRaidWalkIncident {
		t.Fatalf("exit %d, want 19: %s\n%s", code, f.stderr.String(), f.stdout.String())
	}
	sum := f.summary()
	if !strings.Contains(sum.EndReason, "not revived") {
		t.Fatalf("end reason %q, want the revive refusal itself", sum.EndReason)
	}
	rr := sum.ReviveRefusal
	if rr == nil {
		t.Fatalf("no revive_refusal with the point never quiet: %q", sum.EndReason)
	}
	// The recovery's deadline is 3 x --wipe-rest from its entry, and the wait ends rest + 30 s before it, so the wait
	// is 2 x rest - 30 s less the few seconds the revive needs before its first read.
	full := int((2*f.opts.wipeRest - 30*time.Second) / time.Second)
	if rr.WaitedS > full || rr.WaitedS < full-60 {
		t.Fatalf("waited %ds, want the D-C end at %ds less the revive's own seconds", rr.WaitedS, full)
	}
}

// D-D: a point that never goes quiet ends the walk with the refusal in the summary, its blockers and all.
func TestRaidWalkReviveRefusalSummary(t *testing.T) {
	t.Run("in combat", func(t *testing.T) {
		f, _ := newPlaceFixture(t)
		f.opts.wipeRest = 5 * time.Minute
		n := zgPlaceSteps(t)
		f.agent.startReply = zgPlaceReply(t)
		// Two blockers, both fighting: the summary and the reason name the nearest, and the run.log line lists both.
		f.agent.unitsFn = func(int) []objects.Unit {
			f.agent.mu.Lock()
			x, y, z := f.agent.x, f.agent.y, f.agent.z
			f.agent.mu.Unlock()
			return []objects.Unit{
				{Type: "creature", Name: "Razzashi Skitterer", Entry: 11338, Hostile: true, InCombat: true,
					X: x + 30, Y: y, Z: z},
				{Type: "creature", Name: "Razzashi Venombrood", Entry: 14532, Hostile: true, InCombat: true,
					X: x + 44.6, Y: y, Z: z},
			}
		}
		wipeAfterStart(f)
		if code := f.run(context.Background()); code != exitRaidWalkIncident {
			t.Fatalf("exit %d, want 19: %s\n%s", code, f.stderr.String(), f.stdout.String())
		}
		sum := f.summary()
		rr := sum.ReviveRefusal
		if rr == nil {
			t.Fatalf("no revive_refusal: end %q", sum.EndReason)
		}
		if rr.WipeStep != n.start || rr.StartStep != n.start || len(rr.TriedSteps) < 2 || rr.TriedSteps[0] != n.start {
			t.Fatalf("refusal %+v, want wipe step and start step %d and the fallback rows tried", rr, n.start)
		}
		if rr.AllIdle {
			t.Fatalf("all_idle true with in-combat blockers: %+v", rr)
		}
		if len(rr.Blockers) != 2 {
			t.Fatalf("blockers %+v, want both", rr.Blockers)
		}
		if b := rr.Blockers[0]; b.Entry != 11338 || b.Idle || !b.InCombat || b.Dist < 29 || b.Dist > 31 {
			t.Fatalf("nearest blocker %+v, want entry 11338 in combat 30 yd out", b)
		}
		if b := rr.Blockers[1]; b.Entry != 14532 || b.Name != "Razzashi Venombrood" || b.Idle || !b.InCombat {
			t.Fatalf("second blocker %+v, want entry 14532 in combat", b)
		}
		if !strings.Contains(sum.EndReason, "Razzashi Skitterer 30.0 yd from the revive point") {
			t.Fatalf("end reason %q, want the nearest blocker named", sum.EndReason)
		}
		want := fmt.Sprintf("revive refusal: wipe step %d, tried %d/", n.start, n.start)
		if txt := f.summaryText(); !strings.Contains(txt, want) || !strings.Contains(txt, "Razzashi Skitterer") {
			t.Fatalf("summary.txt:\n%s\nwant %q with the blocker", txt, want)
		}
		// D-E: the failed reads left a run.log line with both blockers' entry, name, distance and state.
		raw, err := os.ReadFile(filepath.Join(f.out, "run.log"))
		if err != nil {
			t.Fatalf("run.log: %v", err)
		}
		log := string(raw)
		if !strings.Contains(log, "quiet read at") || !strings.Contains(log, "11338") || !strings.Contains(log, "14532") ||
			!strings.Contains(log, "Razzashi Skitterer") || !strings.Contains(log, "in_combat=true") {
			t.Fatalf("run.log has no quiet-read line with both blockers:\n%s", log)
		}
	})
	t.Run("idle inside the reach", func(t *testing.T) {
		f, _ := newPlaceFixture(t)
		f.opts.wipeRest = 5 * time.Minute
		f.agent.startReply = zgPlaceReply(t)
		// Idle at 20 yd: inside the landing reach, so D-A still lets it block, and waiting cannot help.
		f.agent.unitsFn = placedFollower(f, 20, 14532, "Razzashi Venombrood", false)
		wipeAfterStart(f)
		if code := f.run(context.Background()); code != exitRaidWalkIncident {
			t.Fatalf("exit %d, want 19: %s\n%s", code, f.stderr.String(), f.stdout.String())
		}
		rr := f.summary().ReviveRefusal
		if rr == nil || !rr.AllIdle {
			t.Fatalf("refusal %+v, want an all-idle one", rr)
		}
		if b := rr.Blockers[0]; !b.Idle || b.InCombat || b.Dist < 19 || b.Dist > 21 {
			t.Fatalf("blocker %+v, want an idle one 20 yd out", b)
		}
	})
	t.Run("critter is not a blocker", func(t *testing.T) {
		f, _ := newPlaceFixture(t)
		f.opts.wipeRest = 5 * time.Minute
		f.agent.startReply = zgPlaceReply(t)
		// Walk 27 C restart 2: a moving Snake 2914 72 yd out must not join the blocker list, so the idle Mad Servant
		// 16.8 yd from the commander is the only blocker and all_idle is true (the wait is pointless, the mob is off
		// its spawn). The critter is still in the read, as it was in the evidence. The two stand relative to the
		// commander, so the point, the fallback rows and the entrance all stay refused and the refusal reaches the
		// summary (#392's entrance fallback runs before it).
		f.agent.unitsFn = func(int) []objects.Unit {
			f.agent.mu.Lock()
			x, y, z := f.agent.x, f.agent.y, f.agent.z
			f.agent.mu.Unlock()
			return []objects.Unit{
				{Type: "creature", Name: "Mad Servant", Entry: 15111, Hostile: true, Level: 61, MaxHealth: 4000,
					X: x + 16.8, Y: y, Z: z},
				{Type: "creature", Name: "Snake", Entry: 2914, Hostile: true, Level: 1, MaxHealth: 8, Moving: true,
					X: x + 72, Y: y, Z: z},
			}
		}
		wipeAfterStart(f)
		if code := f.run(context.Background()); code != exitRaidWalkIncident {
			t.Fatalf("exit %d, want 19: %s\n%s", code, f.stderr.String(), f.stdout.String())
		}
		rr := f.summary().ReviveRefusal
		if rr == nil || len(rr.Blockers) != 1 || rr.Blockers[0].Entry != 15111 || !rr.AllIdle {
			t.Fatalf("refusal %+v, want one idle blocker (15111) and all_idle", rr)
		}
		raw, err := os.ReadFile(filepath.Join(f.out, "run.log"))
		if err != nil {
			t.Fatalf("run.log: %v", err)
		}
		if strings.Contains(string(raw), "2914") || strings.Contains(string(raw), "Snake") {
			t.Fatalf("run.log names the critter as a blocker:\n%s", raw)
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
			queuedEvent{raw: "TBM:RUN|onyxias_lair|running|" + strconv.Itoa(step) + "/21|onyxia|move|walk|resumed"},
			queuedEvent{raw: "TBM:RUN|onyxias_lair|done|" + strconv.Itoa(doneStep) + "/21|onyxia|move|walk|stopped before step " + strconv.Itoa(doneStep+1) + " (until boss): the party stays where it stands"})
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
	retirePack0Patrol(f)
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
	resumeHook(f, 11, 20, &resumeAt)
	f.agent.mu.Lock()
	f.agent.startQueue = []queuedEvent{
		{raw: "TBM:RUN|onyxias_lair|paused|10/21|onyxia|move|walk|19 dead: 3 near wipes within 10 min (54): the run stops here. Regroup (c...)"},
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
	retirePack0Patrol(f)
	f.agent.unitsFn = func(n int) []objects.Unit { return []objects.Unit{} }
	var resumeAt time.Time
	resumeHook(f, 11, 20, &resumeAt)
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
			queuedEvent{raw: "TBM:RUN|onyxias_lair|running|11/21|onyxia|move|walk|resumed"})
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
	f.agent.startReply = startAt("onyxias_lair", 13)
	retirePack0Patrol(f)
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
	if sum.EndReason != "wipe at step 13" {
		t.Fatalf("end %q, want the wipe ending", sum.EndReason)
	}
}

// --wipe-end fast (the autopilot passes it for --gm-instance-data and --boss-wipe-fresh goals): a wipe at a boss row
// (or the row before it, PlaceStepAtBoss) ends the walk with exit 19 at once. The copy is thrown away by the
// autopilot's fresh-instance relaunch, so the reset wait (walk 74: 49 s median) and the revive recipe (34 s median)
// are spent on nothing; the dead hires still get a bare `.revive <name>` each, so the party is not left dead.
func TestRaidWalkWipeEndFastSkipsResetAndReviveAtABoss(t *testing.T) {
	f := newFixture(t)
	f.opts.wipeResume = 0
	f.opts.wipeEndFast = true
	f.agent.startReply = startAt("onyxias_lair", 20) // step 21 is the boss row
	retirePack0Patrol(f)
	f.agent.unitsFn = func(n int) []objects.Unit { return []objects.Unit{} }
	f.allDeadFromHires()
	if code := f.run(context.Background()); code != exitRaidWalkIncident {
		t.Fatalf("exit %d, want 19: %s\n%s", code, f.stderr.String(), f.stdout.String())
	}
	gm := f.agent.gmLines()
	for _, line := range gm {
		if strings.HasPrefix(line, ".gm visible off") || strings.HasPrefix(line, ".summon ") {
			t.Fatalf("a wipe the next launch discards still ran the revive recipe in this copy: %q", gm)
		}
	}
	hires := map[string]bool{}
	for _, n := range f.agent.party {
		hires[n] = true
	}
	revived := map[string]bool{}
	for _, line := range gm {
		if !strings.HasPrefix(line, ".revive ") {
			continue
		}
		name := strings.TrimPrefix(line, ".revive ")
		if !hires[name] {
			t.Fatalf("a fast revive named %q, not a hire: %q", name, gm)
		}
		revived[name] = true
	}
	if len(revived) != len(f.agent.party) {
		t.Fatalf("%d of the %d dead hires were revived, want all: %q", len(revived), len(f.agent.party), gm)
	}
	if n := countPrefix(gm, ".bot run stop"); n != 1 {
		t.Fatalf("%d run stops, want exactly 1", n)
	}
	sum := f.summary()
	if sum.WipeResumes != 0 || sum.EndReason != "wipe at step 20" {
		t.Fatalf("wipe_resumes %d end %q, want 0 and the wipe ending", sum.WipeResumes, sum.EndReason)
	}
	if !hasNote(sum, "--wipe-end fast") || !hasNote(sum, "the dead hires get a bare revive") {
		t.Fatalf("notes %q do not say the copy was discarded and the dead hires revived", sum.Notes)
	}
}

// Codex round 2 MEDIUM: the fast wipe revive stopped at the first failed line, leaving the rest of the party dead in
// the copy (and for the party view of the next launch). Every dead hire now gets its line, a failed one is retried
// once inside the window, and the ones still dead are named in one `wipe-end: revive incomplete` note.
func TestReviveDeadHiresKeepsGoingPastAFailure(t *testing.T) {
	newWalk := func(t *testing.T, a *walkFakeAgent) *walk {
		now := a.clock.now()
		return &walk{
			plan:  &dungeon.Plan{Map: 309},
			env:   walkEnv{now: a.clock.now, sleep: a.clock.sleep},
			agent: a,
			mt:    "Tank",
			ots:   []string{"Healer"},
			hires: map[string]bool{"Tank": true, "Healer": true},
			botAt: map[string]botPos{
				"Tank":   {dead: true, at: now},
				"Healer": {dead: true, at: now},
			},
		}
	}
	t.Run("the failed hire is named and the next is still revived", func(t *testing.T) {
		a := newFakeAgent(t, newFakeClock())
		a.gmFail = func(line string) error {
			if line == ".revive Tank" {
				return errors.New("revive refused")
			}
			return nil
		}
		w := newWalk(t, a)
		w.reviveDeadHires(context.Background())
		gm := a.gmLines()
		if n := countPrefix(gm, ".revive Tank"); n != 2 {
			t.Fatalf("%d Tank revive lines, want the line and its one retry: %q", n, gm)
		}
		if n := countPrefix(gm, ".revive Healer"); n != 1 {
			t.Fatalf("%d Healer revive lines, want 1: a failed hire must not stop the loop: %q", n, gm)
		}
		notes := strings.Join(w.notesCopy(), "\n")
		if !strings.Contains(notes, "wipe-end: revive incomplete Tank") {
			t.Fatalf("notes %q do not name the hire left dead", notes)
		}
		if strings.Contains(notes, "incomplete Tank, Healer") {
			t.Fatalf("notes %q name the revived hire as incomplete", notes)
		}
	})
	t.Run("a retry that succeeds leaves nothing incomplete", func(t *testing.T) {
		a := newFakeAgent(t, newFakeClock())
		failed := false
		a.gmFail = func(line string) error {
			if line == ".revive Tank" && !failed {
				failed = true
				return errors.New("revive refused")
			}
			return nil
		}
		w := newWalk(t, a)
		w.reviveDeadHires(context.Background())
		if n := countPrefix(a.gmLines(), ".revive Tank"); n != 2 {
			t.Fatalf("%d Tank revive lines, want the line and its retry", n)
		}
		if notes := strings.Join(w.notesCopy(), "\n"); strings.Contains(notes, "revive incomplete") {
			t.Fatalf("notes %q call a retried revive incomplete", notes)
		}
	})
}

// The same flag, but the wipe is away from the boss: the autopilot re-places this copy (--reuse-instance), so the
// reset wait and the revive stay.
func TestRaidWalkWipeEndFastKeepsTheReviveAwayFromABoss(t *testing.T) {
	f := newFixture(t)
	f.opts.wipeResume = 0
	f.opts.wipeEndFast = true
	f.agent.startReply = startAt("onyxias_lair", 13)
	retirePack0Patrol(f)
	f.agent.unitsFn = func(n int) []objects.Unit { return []objects.Unit{} }
	f.allDeadFromHires()
	if code := f.run(context.Background()); code != exitRaidWalkIncident {
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
		t.Fatalf("a wipe away from the boss lost its revive: %q", gm)
	}
	if hasNote(f.summary(), "no reset wait and no revive") {
		t.Fatalf("notes %q claim the copy was discarded", f.summary().Notes)
	}
}

// A --wipe-end value other than "fast" is a usage refusal: the flag changes what a wipe does, so a typo must not fall
// back to the reset-and-revive path silently.
func TestParseWipeEndRefusesOtherValues(t *testing.T) {
	if _, code := parseRaidWalkArgs([]string{"--wipe-end", "slow", "onyxias_lair"}, io.Discard); code != exitUsage {
		t.Fatalf("--wipe-end slow: exit %d, want %d", code, exitUsage)
	}
	o, code := parseRaidWalkArgs([]string{"--wipe-end", "fast", "onyxias_lair"}, io.Discard)
	if code != exitOK || !o.wipeEndFast {
		t.Fatalf("--wipe-end fast: exit %d wipeEndFast %v", code, o.wipeEndFast)
	}
}

// TestRaidWalkWipeResumesOnce: all hires die at step 10, then quiet. The walk revives every dead hire, rests at
// least --wipe-rest with no `.bot run stop` before the end, resumes, and the run reaches until: exit 0,
// wipe_resumes 1.
func TestRaidWalkWipeResumesOnce(t *testing.T) {
	f := newFixture(t)
	f.agent.startReply = startAt("onyxias_lair", 10)
	retirePack0Patrol(f)
	f.agent.unitsFn = func(n int) []objects.Unit { return []objects.Unit{} }
	var resumeAt time.Time
	resumeHook(f, 11, 20, &resumeAt)
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
	resumeHook(f, 11, 20, &resumeAt)
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
	retirePack0Patrol(f)
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
			queuedEvent{raw: "TBM:RUN|onyxias_lair|running|11/21|onyxia|move|walk|resumed"},
			queuedEvent{raw: "TBM:RUN|onyxias_lair|done|20/21|onyxia|move|walk|stopped before step 21 (until boss): the party stays where it stands"})
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
			retirePack0Patrol(f)
			f.agent.unitsFn = func(n int) []objects.Unit { return []objects.Unit{} }
			f.opts.dashURL, f.opts.dashPassFile = "http://dash.test", "/tmp/pass"
			f.dash.fights = []walkFightRow{{ID: "4-5", Key: "Commander\x001793219890\x004-5", Dur: 1200, Outcome: tc.outcome, BossName: "Onyxia", Leg: "onyxia", Step: 3, Deaths: 1}}
			resumes := 0
			f.agent.onResume = func() {
				resumes++
				f.agent.mu.Lock()
				defer f.agent.mu.Unlock()
				f.agent.lines = append(f.agent.lines,
					queuedEvent{raw: "TBM:RUN|onyxias_lair|running|11/21|onyxia|move|walk|resumed"},
					queuedEvent{raw: "TBM:RUN|onyxias_lair|done|20/21|onyxia|move|walk|stopped before step 21 (until boss): the party stays where it stands"})
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

// TestRaidWalkWipeResumeBossStillEngagedRecovers (walk 32): the same walk, but the boss is still in the frames when
// the wipe on its own step lands. Under an until that goes past the boss step (boss-done's clause) the run leader owns
// that fight, so the wipe follows the normal wipe-resume flow: revive, rest, resume, one recovery — never the old
// `.bot run stop`. Before walk 32's fix a boss row in the frames ended the walk here.
func TestRaidWalkWipeResumeBossStillEngagedRecovers(t *testing.T) {
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
	// The recovery's resume takes the run on: its status and the run's end arrive after the resume went out.
	f.agent.onResume = func() {
		f.agent.mu.Lock()
		defer f.agent.mu.Unlock()
		f.agent.lines = append(f.agent.lines,
			queuedEvent{raw: fmt.Sprintf("TBM:RUN|molten_core|running|%d/%d|lucifron|boss|fight|resumed", next-1, total)},
			queuedEvent{raw: fmt.Sprintf("TBM:RUN|molten_core|done|%d/%d|lucifron|move|walk|stopped before step %d (until step %d): the party stays where it stands", next, total, next, next)})
	}
	code := f.run(context.Background())
	if code != exitOK {
		t.Fatalf("exit %d, want 0: %s\n%s", code, f.stderr.String(), f.stdout.String())
	}
	sum := f.summary()
	if !hasNote(sum, "so the run leader fights it") {
		t.Fatalf("notes %q, want the boss-fight note (the engagement must latch)", sum.Notes)
	}
	if sum.WipeResumes != 1 {
		t.Fatalf("wipe_resumes %d, want 1 (the run leader fights the boss, so the wipe recovers)", sum.WipeResumes)
	}
	if hasNote(sum, "wipe resume skipped") {
		t.Fatalf("notes %q, want no skipped recovery", sum.Notes)
	}
	if n := countPrefix(f.agent.gmLines(), ".bot run stop"); n != 0 {
		t.Fatalf("%d run stops, want none (a boss-step wipe recovers)", n)
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

// TestRaidWalkWipeResumeBossDeadFrameKills: walk 26 test2-ven-door. A plan
// boss row engaged long ago (the frames hold only the kill now), then at
// 0 % when the trash wipe lands. RED on a96aaec: the latch fires the
// unknown wait ("boss state unknown ... latched ..."), exit 19,
// wipe_resumes 0. GREEN after the fix: the wipe treats it as killed (no
// unknown skip) and advances.
func TestRaidWalkWipeResumeBossDeadFrameKills(t *testing.T) {
	f, next, total := bossDoneFixture(t)
	f.agent.startReply = zgStart(total, next)
	f.agent.mu.Lock()
	// Boss-kind status: the run is on its boss step, so the watch absorbs the
	// tracker's engagement (run-leader-fights-it note) and the walk stays out
	// until the wipe, like the model.
	f.agent.startQueue = []queuedEvent{
		{raw: fmt.Sprintf("TBM:RUN|molten_core|running|%d/%d|lucifron|boss|fight|fighting", next-1, total)},
	}
	f.agent.mu.Unlock()
	f.agent.unitsFn = func(n int) []objects.Unit { return []objects.Unit{} }
	killed := false
	// Engaged until 100 s before the wipe, then the 0 % corpse stays in the
	// frames: stale past the engaged gate (> 15 s) and past the tracker's own
	// target memory, but the latch is 100 s old (inside 120 s), so only the
	// unknown wait blocks, via `bossUnknown`.
	t0 := f.clock.now()
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
		if now.Before(wipeAt.Add(-100 * time.Second)) {
			return []tbm.HostileRec{{GUID: 9, Entry: 12118, HPPct: 90, Target: packet.GUID(1000)}}
		}
		return []tbm.HostileRec{{GUID: 9, Entry: 12118, HPPct: 0, Target: 0}}
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
		t.Fatalf("wipe_resumes %d, want 1 (a boss at 0 %% in the frames is killed, not unknown)", sum.WipeResumes)
	}
	if hasNote(sum, "boss state unknown") {
		t.Fatalf("notes %q, want no boss-unknown skip (the 0 %% frame is the kill)", sum.Notes)
	}
	if !hasNote(sum, "killed") {
		t.Fatalf("notes %q, want the kill evidence", sum.Notes)
	}
}

// dashLatchWipeFixture: a molten_core boss-done walk with the dashboard
// enabled. The frames latch the boss engaged (90 % with a party target)
// until 140 s, then show after(elapsed) rows; the wipe at 240 s leaves the
// latch 100 s old, inside the 120 s window. The run resumes when the
// recovery completes. Codex HIGH: the frame verdict must never skip the
// dashboard's live-boss gate.
func dashLatchWipeFixture(t *testing.T, after func(elapsed time.Duration) []tbm.HostileRec) (*fixture, int, int) {
	t.Helper()
	f, next, total := bossDoneFixture(t)
	f.agent.startReply = zgStart(total, next)
	f.agent.mu.Lock()
	f.agent.startQueue = []queuedEvent{
		{raw: fmt.Sprintf("TBM:RUN|molten_core|running|%d/%d|lucifron|boss|fight|fighting", next-1, total)},
	}
	f.agent.mu.Unlock()
	f.agent.unitsFn = func(n int) []objects.Unit { return []objects.Unit{} }
	f.opts.dashURL, f.opts.dashPassFile = "http://dash.test", "/tmp/pass"
	killed := false
	t0 := f.clock.now()
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
		if elapsed := now.Sub(t0); elapsed < 140*time.Second {
			return []tbm.HostileRec{{GUID: 9, Entry: 12118, HPPct: 90, Target: packet.GUID(1000)}}
		} else {
			return after(elapsed)
		}
	}
	f.agent.onResume = func() {
		f.agent.mu.Lock()
		defer f.agent.mu.Unlock()
		f.agent.lines = append(f.agent.lines,
			queuedEvent{raw: fmt.Sprintf("TBM:RUN|molten_core|running|%d/%d|lucifron|boss|fight|fighting resumed", next-1, total)},
			queuedEvent{raw: fmt.Sprintf("TBM:RUN|molten_core|done|%d/%d|lucifron|move|walk|stopped before step %d (until step %d): the party stays where it stands", next, total, next, next)})
	}
	return f, next, total
}

// revivesBeforeFirstStop counts the revive/summon gm lines sent before the
// first `.bot run stop`: the recovery's own revive (a wrong resume) happens
// before the old wipe ending's stop, the ending's revive after it.
func revivesBeforeFirstStop(gm []string) int {
	stopAt, revives := -1, 0
	for i, l := range gm {
		if stopAt < 0 && strings.HasPrefix(l, ".bot run stop") {
			stopAt = i
		}
		if stopAt < 0 && (strings.HasPrefix(l, ".revive ") || strings.HasPrefix(l, ".summon ")) {
			revives++
		}
	}
	return revives
}

// TestRaidWalkWipeResumeFrameKillLiveDashboardBlocks (Codex HIGH): the frames
// show the boss killed (0 % corpse, the latch kill) while the dashboard's
// freshest fights list holds a live boss fight row. The dashboard gate runs
// first, so nothing is revived into that fight: exit 19, wipe_resumes 0, the
// live-row skip, no kill-advance note and no revive or summon before the run
// stop. With the frame verdict checked first, the walk advanced on the kill,
// revived, and only then read the live row.
func TestRaidWalkWipeResumeFrameKillLiveDashboardBlocks(t *testing.T) {
	f, _, _ := dashLatchWipeFixture(t, func(elapsed time.Duration) []tbm.HostileRec {
		return []tbm.HostileRec{{GUID: 9, Entry: 12118, HPPct: 0, Target: 0}}
	})
	f.dash.fights = []walkFightRow{{ID: "4-5", Key: "k", Live: true, BossName: "Lucifron", Outcome: "live"}}
	code := f.run(context.Background())
	if code != exitRaidWalkIncident {
		t.Fatalf("exit %d, want 19: %s\n%s", code, f.stderr.String(), f.stdout.String())
	}
	sum := f.summary()
	if sum.WipeResumes != 0 {
		t.Fatalf("wipe_resumes %d, want 0 (a live dashboard fight blocks the revive)", sum.WipeResumes)
	}
	if !hasNote(sum, "wipe resume skipped: boss state unknown (live boss fight row on the dashboard)") {
		t.Fatalf("notes %q, want the live-row skip", sum.Notes)
	}
	if hasNote(sum, "advancing, not unknown") {
		t.Fatalf("notes %q, want no frame kill advance past the dashboard gate", sum.Notes)
	}
	if n := revivesBeforeFirstStop(f.agent.gmLines()); n != 0 {
		t.Fatalf("%d revive/summon lines before the run stop, want 0 (nothing is revived into a live fight): %q", n, f.agent.gmLines())
	}
}

// TestRaidWalkWipeResumeFrameKillCleanDashboardResumes: the same frame kill
// with a fresh fights list that holds no live row (a finished fight is
// history) still resolves the latch as the kill and resumes.
func TestRaidWalkWipeResumeFrameKillCleanDashboardResumes(t *testing.T) {
	f, _, _ := dashLatchWipeFixture(t, func(elapsed time.Duration) []tbm.HostileRec {
		return []tbm.HostileRec{{GUID: 9, Entry: 12118, HPPct: 0, Target: 0}}
	})
	f.dash.fights = mcWipeFights()
	code := f.run(context.Background())
	if code != exitOK {
		t.Fatalf("exit %d, want 0: %s\n%s", code, f.stderr.String(), f.stdout.String())
	}
	sum := f.summary()
	if sum.WipeResumes != 1 {
		t.Fatalf("wipe_resumes %d, want 1 (a clean dashboard leaves the frame kill to resolve the latch)", sum.WipeResumes)
	}
	if !hasNote(sum, "killed") {
		t.Fatalf("notes %q, want the kill evidence", sum.Notes)
	}
	if hasNote(sum, "boss state unknown") {
		t.Fatalf("notes %q, want no boss-unknown skip", sum.Notes)
	}
}

// TestRaidWalkWipeResumeFrameResetErroredDashboardSkips: the frames show the
// boss reset (fresh 100 %, no target) but the dashboard fights read errors.
// An errored read is unknown boss state, exactly as before the frame verdict
// existed: exit 19, wipe_resumes 0, the read-failed skip, no reset advance.
func TestRaidWalkWipeResumeFrameResetErroredDashboardSkips(t *testing.T) {
	f, _, _ := dashLatchWipeFixture(t, func(elapsed time.Duration) []tbm.HostileRec {
		return []tbm.HostileRec{{GUID: 9, Entry: 12118, HPPct: 100, Target: 0}}
	})
	f.dash.fightsErr = errors.New("daemon down")
	code := f.run(context.Background())
	if code != exitRaidWalkIncident {
		t.Fatalf("exit %d, want 19: %s\n%s", code, f.stderr.String(), f.stdout.String())
	}
	sum := f.summary()
	if sum.WipeResumes != 0 {
		t.Fatalf("wipe_resumes %d, want 0 (an errored read is unknown boss state)", sum.WipeResumes)
	}
	if !hasNote(sum, "wipe resume skipped: boss state unknown (dashboard fights read failed: daemon down)") {
		t.Fatalf("notes %q, want the read-failed skip", sum.Notes)
	}
	if hasNote(sum, "advancing, not unknown") {
		t.Fatalf("notes %q, want no frame reset advance past the dashboard gate", sum.Notes)
	}
	if n := revivesBeforeFirstStop(f.agent.gmLines()); n != 0 {
		t.Fatalf("%d revive/summon lines before the run stop, want 0 (an unreadable dashboard is unknown): %q", n, f.agent.gmLines())
	}
}

// TestRaidWalkWipeResumeBossDeadNoAttemptNoKill: the frames hold only a
// 0 % row of the boss — a fake death, never a live row in this attempt. No
// live row means no attempt start and no boss latch, so the corpse is no
// kill evidence and no block: the wipe recovers (exit 0, wipe_resumes 1)
// with no kill note.
func TestRaidWalkWipeResumeBossDeadNoAttemptNoKill(t *testing.T) {
	f, next, total := bossDoneFixture(t)
	f.agent.startReply = zgStart(total, next)
	f.agent.mu.Lock()
	f.agent.startQueue = []queuedEvent{
		{raw: fmt.Sprintf("TBM:RUN|molten_core|running|%d/%d|lucifron|boss|fight|fighting", next-1, total)},
	}
	f.agent.mu.Unlock()
	f.agent.unitsFn = func(n int) []objects.Unit { return []objects.Unit{} }
	killed := false
	// The frames never show the boss alive in this attempt: only the 0 %
	// row, with no party target, from 100 s before the wipe on. No live row
	// means no attempt start, so the dead row is no kill evidence.
	t0 := f.clock.now()
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
		if now.Before(wipeAt.Add(-100 * time.Second)) {
			return nil
		}
		return []tbm.HostileRec{{GUID: 9, Entry: 12118, HPPct: 0}}
	}
	f.agent.onResume = func() {
		f.agent.mu.Lock()
		defer f.agent.mu.Unlock()
		f.agent.lines = append(f.agent.lines,
			queuedEvent{raw: fmt.Sprintf("TBM:RUN|molten_core|running|%d/%d|lucifron|boss|fight|fighting resumed", next-1, total)},
			queuedEvent{raw: fmt.Sprintf("TBM:RUN|molten_core|done|%d/%d|lucifron|move|walk|stopped before step %d (until step %d): the party stays where it stands", next, total, next, next)})
	}
	code := f.run(context.Background())
	sum := f.summary()
	if code != exitOK {
		t.Fatalf("exit %d, want 0: %s\n%s\nnotes %q", code, f.stderr.String(), f.stdout.String(), sum.Notes)
	}
	if sum.WipeResumes != 1 {
		t.Fatalf("wipe_resumes %d, want 1 (a dead row with no live row in this attempt is no kill and no block)", sum.WipeResumes)
	}
	if hasNote(sum, "killed") {
		t.Fatalf("notes %q, want no kill evidence (the 0 %% row never started an attempt)", sum.Notes)
	}
	if hasNote(sum, "boss state unknown") {
		t.Fatalf("notes %q, want no unknown wait (no boss row ever latched)", sum.Notes)
	}
}

// TestRaidWalkWipeBossFrameDeadNoAttemptNoKill: the wipe-resume judgement
// itself. A live row starts the attempt and the 0 % row after it is the
// kill; once the recovery clears the attempt, the same corpse is another
// attempt's and no live row came after it, so wipeBossFrame reports neither
// kill nor reset (the unknown wait, not a kill).
func TestRaidWalkWipeBossFrameDeadNoAttemptNoKill(t *testing.T) {
	now := time.Date(2026, 10, 29, 20, 0, 0, 0, time.Local)
	plan := &dungeon.Plan{Map: 230, Legs: []dungeon.Leg{{Boss: &dungeon.Boss{Entry: 12118}}}}
	w := &walk{
		plan:          plan,
		bossRule:      raidwalk.NewBossRows(plan),
		bossRows:      map[uint32]hostileState{},
		hostiles:      map[string]hostileState{},
		bossSet:       map[uint32]bool{12118: true},
		bossAdds:      map[uint32]bool{},
		bossAttemptAt: map[uint32]time.Time{},
		partyG:        map[uint64]bool{},
	}
	liveAt := now.Add(-20 * time.Second)
	w.trackFrame(liveAt, &tbm.ViewFrame{Hostiles: []tbm.HostileRec{{GUID: 9, Entry: 12118, HPPct: 60}}})
	if at := w.bossAttemptAt[12118]; !at.Equal(liveAt) {
		t.Fatalf("bossAttemptAt = %v, want %v: the live row starts the attempt", at, liveAt)
	}
	w.trackFrame(now.Add(-5*time.Second), &tbm.ViewFrame{Hostiles: []tbm.HostileRec{{GUID: 9, Entry: 12118, HPPct: 0}}})
	if kill, alive, words := w.wipeBossFrame(now); !kill || alive || !strings.Contains(words, "12118") {
		t.Fatalf("wipeBossFrame = %v %v %q, want the kill: the live row started this attempt", kill, alive, words)
	}
	clear(w.bossAttemptAt) // recoverWipe clears it: the next attempt has no live row
	if kill, alive, words := w.wipeBossFrame(now); kill || alive || words != "" {
		t.Fatalf("wipeBossFrame = %v %v %q, want no kill and no reset: no live row in this attempt", kill, alive, words)
	}
}

// TestRaidWalkWipeResumeBossDeadThenDespawnedKills: walk 26 test2-ven-door.
// Engaged, at 0 %, then gone from the frames (despawned) when the wipe
// lands. The frames' last view of that guid was dead, so the wipe treats it
// as killed (no unknown skip) and advances.
func TestRaidWalkWipeResumeBossDeadThenDespawnedKills(t *testing.T) {
	f, next, total := bossDoneFixture(t)
	f.agent.startReply = zgStart(total, next)
	f.agent.mu.Lock()
	// Boss-kind status: the run is on its boss step, so the watch absorbs the
	// tracker's engagement (run-leader-fights-it note) and the walk stays out
	// until the wipe, like the model.
	f.agent.startQueue = []queuedEvent{
		{raw: fmt.Sprintf("TBM:RUN|molten_core|running|%d/%d|lucifron|boss|fight|fighting", next-1, total)},
	}
	f.agent.mu.Unlock()
	f.agent.unitsFn = func(n int) []objects.Unit { return []objects.Unit{} }
	killed := false
	// Engaged until 100 s before the wipe, the 0 % corpse for 70 s, then
	// gone (despawned) for the last 30 s: the frames' last view was dead.
	t0 := f.clock.now()
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
		if now.Before(wipeAt.Add(-100 * time.Second)) {
			return []tbm.HostileRec{{GUID: 9, Entry: 12118, HPPct: 90, Target: packet.GUID(1000)}}
		}
		if now.Before(wipeAt.Add(-30 * time.Second)) {
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
		t.Fatalf("wipe_resumes %d, want 1 (a despawned boss after a 0 %% frame is killed, not unknown)", sum.WipeResumes)
	}
	if hasNote(sum, "boss state unknown") {
		t.Fatalf("notes %q, want no boss-unknown skip (the frames' last view was dead)", sum.Notes)
	}
	if !hasNote(sum, "killed") {
		t.Fatalf("notes %q, want the kill evidence", sum.Notes)
	}
}

// TestRaidWalkWipeResumeBossLowHpRecovers (walk 32): engaged 100 s before the wipe on the boss's own step, last frame
// at 4 % with a target. Under boss-done's until the run leader owns that fight, so the wipe recovers (one
// --wipe-resume, no `.bot run stop`); the 4 % row is not read as a kill (that rule still guards the walk-owned boss
// path, wipeBossFrame's own tests). Before walk 32's fix the latched target ended the walk here.
func TestRaidWalkWipeResumeBossLowHpRecovers(t *testing.T) {
	f, next, total := bossDoneFixture(t)
	f.agent.startReply = zgStart(total, next)
	f.agent.mu.Lock()
	f.agent.startQueue = []queuedEvent{
		{raw: fmt.Sprintf("TBM:RUN|molten_core|running|%d/%d|lucifron|boss|fight|fighting", next-1, total)},
	}
	f.agent.mu.Unlock()
	f.agent.unitsFn = func(n int) []objects.Unit { return []objects.Unit{} }
	t0 := f.clock.now()
	killed := false
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
		// Stale targeted 4 % row 100 s before the wipe: never a kill, so the
		// unknown wait still fires.
		if now.Before(wipeAt.Add(-100 * time.Second)) {
			return []tbm.HostileRec{{GUID: 9, Entry: 12118, HPPct: 90, Target: packet.GUID(1000)}}
		}
		return []tbm.HostileRec{{GUID: 9, Entry: 12118, HPPct: 4, Target: packet.GUID(1000)}}
	}
	f.agent.onResume = func() {
		f.agent.mu.Lock()
		defer f.agent.mu.Unlock()
		f.agent.lines = append(f.agent.lines,
			queuedEvent{raw: fmt.Sprintf("TBM:RUN|molten_core|running|%d/%d|lucifron|boss|fight|resumed", next-1, total)},
			queuedEvent{raw: fmt.Sprintf("TBM:RUN|molten_core|done|%d/%d|lucifron|move|walk|stopped before step %d (until step %d): the party stays where it stands", next, total, next, next)})
	}
	code := f.run(context.Background())
	if code != exitOK {
		t.Fatalf("exit %d, want 0: %s\n%s", code, f.stderr.String(), f.stdout.String())
	}
	sum := f.summary()
	if sum.WipeResumes != 1 {
		t.Fatalf("wipe_resumes %d, want 1 (the run leader fights the boss, so the wipe recovers)", sum.WipeResumes)
	}
	if hasNote(sum, "wipe resume skipped") {
		t.Fatalf("notes %q, want no skipped recovery", sum.Notes)
	}
	if n := countPrefix(f.agent.gmLines(), ".bot run stop"); n != 0 {
		t.Fatalf("%d run stops, want none (a boss-step wipe recovers)", n)
	}
}

// TestRaidWalkWipeResumeBossResetFrameResumes: the boss fought, then reset
// (full HP, no target, no live target for 30 s) before the wipe. The reset
// frames say the boss is alive, so the wipe resumes normally (wipe_resumes
// 1, no unknown skip).
func TestRaidWalkWipeResumeBossResetFrameResumes(t *testing.T) {
	f, next, total := bossDoneFixture(t)
	f.agent.startReply = zgStart(total, next)
	f.agent.mu.Lock()
	f.agent.startQueue = []queuedEvent{
		{raw: fmt.Sprintf("TBM:RUN|molten_core|running|%d/%d|lucifron|boss|fight|fighting", next-1, total)},
	}
	f.agent.mu.Unlock()
	f.agent.unitsFn = func(n int) []objects.Unit { return []objects.Unit{} }
	killed := false
	// Engaged until 100 s before the wipe, then full HP with no target in
	// the frames: the reset shape, inside the latch's 120 s window but stale
	// past the engaged gate (> 15 s) and neutral to the reset wait (hp 100).
	t0 := f.clock.now()
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
		if now.Before(wipeAt.Add(-100 * time.Second)) {
			return []tbm.HostileRec{{GUID: 9, Entry: 12118, HPPct: 90, Target: packet.GUID(1000)}}
		}
		return []tbm.HostileRec{{GUID: 9, Entry: 12118, HPPct: 100, Target: 0}}
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
		t.Fatalf("wipe_resumes %d, want 1 (a reset boss is alive, the wipe resumes)", sum.WipeResumes)
	}
	if hasNote(sum, "boss state unknown") {
		t.Fatalf("notes %q, want no boss-unknown skip (the reset frames say alive)", sum.Notes)
	}
}

// TestRaidWalkWipeResumeNoBossFrameStillSkips: engaged 100 s before the wipe,
// then no boss row in the frames at all. Only the latch speaks, so the walk
// keeps the unknown wait: exit 19, wipe_resumes 0, the skipped note.
func TestRaidWalkWipeResumeNoBossFrameStillSkips(t *testing.T) {
	f, next, total := bossDoneFixture(t)
	f.agent.startReply = zgStart(total, next)
	f.agent.mu.Lock()
	f.agent.startQueue = []queuedEvent{
		{raw: fmt.Sprintf("TBM:RUN|molten_core|running|%d/%d|lucifron|boss|fight|fighting", next-1, total)},
	}
	f.agent.mu.Unlock()
	f.agent.unitsFn = func(n int) []objects.Unit { return []objects.Unit{} }
	t0 := f.clock.now()
	killed := false
	// Engaged until 100 s before the wipe, then gone: the stale-row shape
	// with no frame evidence either way.
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
		if now.Before(wipeAt.Add(-100 * time.Second)) {
			return []tbm.HostileRec{{GUID: 9, Entry: 12118, HPPct: 90, Target: packet.GUID(1000)}}
		}
		return nil
	}
	code := f.run(context.Background())
	if code != exitRaidWalkIncident {
		t.Fatalf("exit %d, want 19: %s\n%s", code, f.stderr.String(), f.stdout.String())
	}
	sum := f.summary()
	if sum.WipeResumes != 0 {
		t.Fatalf("wipe_resumes %d, want 0 (no frame evidence keeps the unknown wait)", sum.WipeResumes)
	}
	if !hasNote(sum, "boss state unknown") {
		t.Fatalf("notes %q, want the skipped note", sum.Notes)
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
	retirePack0Patrol(f)
	f.agent.unitsFn = func(n int) []objects.Unit { return []objects.Unit{} }
	f.opts.wipeRest = 90 * time.Second
	f.allDeadFromHires()
	var resumeAt time.Time
	resumes := 0
	f.agent.onResume = func() {
		resumes++
		resumeAt = f.clock.now()
		f.agent.push("TBM:RUN|onyxias_lair|running|11/21|onyxia|move|walk|resumed", "")
		f.agent.push("TBM:RUN|onyxias_lair|done|20/21|onyxia|move|walk|stopped before step 21 (until boss): the party stays where it stands", "")
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
	retirePack0Patrol(f)
	f.agent.unitsFn = func(n int) []objects.Unit { return []objects.Unit{} }
	var resumeAt time.Time
	resumeHook(f, 11, 20, &resumeAt)
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
	retirePack0Patrol(f)
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
	retirePack0Patrol(f)
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
			queuedEvent{raw: "TBM:RUN|onyxias_lair|running|11/21|onyxia|move|walk|regrouped"},
			queuedEvent{raw: "TBM:RUN|onyxias_lair|done|20/21|onyxia|move|walk|stopped before step 21 (until boss): the party stays where it stands"})
		f.agent.mu.Unlock()
	}
	resumes := 0
	f.agent.onResume = func() {
		resumes++
		f.agent.push("TBM:RUN|onyxias_lair|running|12/21|onyxia|move|walk|resumed", "")
		f.agent.push("TBM:RUN|onyxias_lair|done|20/21|onyxia|move|walk|stopped before step 21 (until boss): the party stays where it stands", "")
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
	retirePack0Patrol(f)
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
			queuedEvent{raw: "TBM:RUN|onyxias_lair|running|10/21|onyxia|move|walk|regrouped"},
			queuedEvent{raw: "TBM:RUN|onyxias_lair|running|10/21|onyxia|move|walk|walking"},
			queuedEvent{raw: "TBM:RUN|onyxias_lair|done|20/21|onyxia|move|walk|stopped before step 21 (until boss): the party stays where it stands"})
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
	retirePack0Patrol(f)
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
			queuedEvent{raw: "TBM:RUN|onyxias_lair|running|11/21|onyxia|move|walk|walking"},
			queuedEvent{raw: "TBM:RUN|onyxias_lair|done|20/21|onyxia|move|walk|stopped before step 21 (until boss): the party stays where it stands"})
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
	resumeHook(f, 11, 20, &resumeAt)
	f.agent.mu.Lock()
	f.agent.startQueue = []queuedEvent{
		{raw: "TBM:RUN|onyxias_lair|running|10/21|onyxia|move|walk|regrouped"},
		{raw: "TBM:RUN|onyxias_lair|paused|10/21|onyxia|move|walk|19 dead: 3 near wipes within 10 min (54): the run stops here. Regroup (c...)"},
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
	retirePack0Patrol(f)
	f.agent.unitsFn = func(n int) []objects.Unit { return []objects.Unit{} }
	var resumeAt time.Time
	resumeHook(f, 11, 20, &resumeAt)
	f.agent.mu.Lock()
	f.agent.startQueue = []queuedEvent{
		{raw: "TBM:RUN|onyxias_lair|running|10/21|onyxia|move|walk|19 dead (1 wipe, 1 near wipe this run): regrouping by itself (1 of 2 at this step)"},
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
	retirePack0Patrol(f)
	f.agent.unitsFn = func(n int) []objects.Unit { return []objects.Unit{} }
	var resumeAt time.Time
	resumeHook(f, 11, 20, &resumeAt)
	note := "TBM:RUN|onyxias_lair|running|9/21|onyxia|move|walk|19 dead (1 wipe, 1 near wipe this run): regrouping by itself (1 of 2 at this step)"
	walk := "TBM:RUN|onyxias_lair|running|10/21|onyxia|move|walk|walking"
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
	retirePack0Patrol(f)
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
// every hire that joined after the bind. The refit (and the bind after it) waits, up to hireSettleWait, for every
// hire to join (waitHiresJoined).

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

// A version 2 roster sets each batch's spec with `.bot role` after its hire and before the refit, and
// logs one "roster:" line per bot (name, class, spec).
func TestRaidWalkSetsBatchSpecsBeforeTheRefit(t *testing.T) {
	f := newFixture(t)
	roster := `{"format":"tb-raid-roster","version":2,"level":60,"tier":2,
 "batches":[{"role":"mt","hire":"1 warrior:tank","spec":"protection"},
            {"role":"ot","hire":"1 paladin:tank","spec":"protection"},
            {"role":"rest","hire":"1 priest:healer","spec":"holy"},
            {"role":"rest","hire":"17d"}]}`
	path := filepath.Join(t.TempDir(), "roster.json")
	if err := os.WriteFile(path, []byte(roster), 0o600); err != nil {
		t.Fatal(err)
	}
	f.opts.hires = path
	f.defaultRun("onyxias_lair")
	if code := f.run(context.Background()); code != exitOK {
		t.Fatalf("exit %d, want 0: %s", code, f.stderr.String())
	}
	want := []string{".bot role Bot01 protection", ".bot role Bot02 protection", ".bot role Bot03 holy"}
	if got := f.agent.roleOrder(); len(got) != len(want) || got[0] != want[0] || got[1] != want[1] || got[2] != want[2] {
		t.Fatalf("role lines %q, want %q", got, want)
	}
	// Every spec goes on before the one refit, which follows the bots' own specs.
	gear := -1
	for i, line := range f.agent.gmLines() {
		if strings.HasPrefix(line, ".bot gear raid ") {
			gear = i
		}
	}
	if gear < 0 {
		t.Fatal("no refit line")
	}
	for i, line := range f.agent.gmLines() {
		if strings.HasPrefix(line, ".bot role ") && i > gear {
			t.Fatalf("the spec line %q came after the refit", line)
		}
	}
	raw, err := os.ReadFile(filepath.Join(f.out, "run.log"))
	if err != nil {
		t.Fatal(err)
	}
	for _, line := range []string{"roster: Bot01 warrior protection", "roster: Bot02 paladin protection", "roster: Bot03 priest holy"} {
		if !strings.Contains(string(raw), line) {
			t.Errorf("run.log lacks %q", line)
		}
	}
}

// A roster's commander_faction is checked against the commander's own side before the first hire: the
// module hires the commander's faction only, so the other side's roster cannot be filled (no shaman for an
// Alliance commander, no paladin for a Horde one).
func TestRaidWalkRefusesTheOtherCommanderFaction(t *testing.T) {
	f := newFixture(t)
	f.agent.status.Team = "alliance"
	f.agent.status.Character = "Glacienne"
	roster := `{"format":"tb-raid-roster","version":2,"level":60,"tier":2,"commander_faction":"horde",
 "batches":[{"role":"mt","hire":"1 warrior:tank","spec":"protection"},
            {"role":"rest","hire":"1 shaman:dps","spec":"ele"},
            {"role":"rest","hire":"18d"}]}`
	path := filepath.Join(t.TempDir(), "roster.json")
	if err := os.WriteFile(path, []byte(roster), 0o600); err != nil {
		t.Fatal(err)
	}
	f.opts.hires = path
	f.defaultRun("onyxias_lair")
	if code := f.run(context.Background()); code != exitRaidWalkFailed {
		t.Fatalf("exit %d, want 18: %s", code, f.stderr.String())
	}
	reason := f.summary().EndReason
	if !strings.Contains(reason, "wants a horde commander") || !strings.Contains(reason, "Glacienne is alliance") {
		t.Fatalf("end reason %q, want the roster's faction and the commander's", reason)
	}
	for _, line := range f.agent.gmLines() {
		if strings.HasPrefix(line, ".bot hire raid ") {
			t.Fatalf("a hire went out for the other side: %q", line)
		}
	}
}

// The roster's commander_faction and the commander's own side agree: the hire goes on, and the log says
// which side the commander is.
func TestRaidWalkAcceptsTheRostersCommanderFaction(t *testing.T) {
	f := newFixture(t)
	f.agent.status.Team = "horde"
	f.agent.status.Character = "Zfmageb"
	roster := `{"format":"tb-raid-roster","version":2,"level":60,"tier":2,"commander_faction":"horde",
 "batches":[{"role":"mt","hire":"1 warrior:tank","spec":"protection"},
            {"role":"ot","hire":"1 druid:tank","spec":"bear"},
            {"role":"rest","hire":"1 shaman:healer","spec":"restoration"},
            {"role":"rest","hire":"16d"}]}`
	path := filepath.Join(t.TempDir(), "roster.json")
	if err := os.WriteFile(path, []byte(roster), 0o600); err != nil {
		t.Fatal(err)
	}
	f.opts.hires = path
	f.defaultRun("onyxias_lair")
	if code := f.run(context.Background()); code != exitOK {
		t.Fatalf("exit %d, want 0: %s", code, f.stderr.String())
	}
	raw, err := os.ReadFile(filepath.Join(f.out, "run.log"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(raw), "roster: commander Zfmageb is horde, the roster wants horde") {
		t.Errorf("run.log lacks the commander's faction line")
	}
}

// A `.bot role` the server refuses stops the walk before the refit: the roster's spec is not optional.
func TestRaidWalkRefusedSpecStopsBeforeTheRefit(t *testing.T) {
	f := newFixture(t)
	f.agent.roleFail = true
	roster := `{"format":"tb-raid-roster","version":2,"level":60,"tier":2,
 "batches":[{"role":"mt","hire":"1 warrior:tank","spec":"protection"},
            {"role":"ot","hire":"1t"},
            {"role":"rest","hire":"18d"}]}`
	path := filepath.Join(t.TempDir(), "roster.json")
	if err := os.WriteFile(path, []byte(roster), 0o600); err != nil {
		t.Fatal(err)
	}
	f.opts.hires = path
	f.defaultRun("onyxias_lair")
	if code := f.run(context.Background()); code != exitRaidWalkFailed {
		t.Fatalf("exit %d, want 18: %s", code, f.stderr.String())
	}
	if !strings.Contains(f.summary().EndReason, "does not use that spec") {
		t.Fatalf("end reason %q, want the server's refusal", f.summary().EndReason)
	}
	for _, line := range f.agent.gmLines() {
		if strings.HasPrefix(line, ".bot gear raid ") {
			t.Fatal("the refit went out after a refused spec")
		}
	}
}

// A 40-bot roster whose last spec batch is one bot: the server's 39-bot cap fills the earlier batches,
// the last batch is skipped whole (the server would refuse it as "maximum companions"), and the walk
// goes on with the cap note.
func TestRaidWalkSkipsBatchesBeyondTheHireCap(t *testing.T) {
	f := newFixture(t)
	roster := `{"format":"tb-raid-roster","version":2,"level":60,"tier":2,
 "batches":[{"role":"mt","hire":"1 warrior:tank","spec":"protection"},
            {"role":"ot","hire":"3t"},
            {"role":"rest","hire":"10h 25d"},
            {"role":"rest","hire":"1 mage","spec":"fire"}]}`
	path := filepath.Join(t.TempDir(), "roster.json")
	if err := os.WriteFile(path, []byte(roster), 0o600); err != nil {
		t.Fatal(err)
	}
	f.opts.hires = path
	f.defaultRun("onyxias_lair")
	if code := f.run(context.Background()); code != exitOK {
		t.Fatalf("exit %d, want 0: %s", code, f.stderr.String())
	}
	if len(f.agent.party) != raidwalk.HireCap {
		t.Fatalf("party holds %d bots, want the capped %d", len(f.agent.party), raidwalk.HireCap)
	}
	if !hasNote(f.summary(), "hire cap 39: asked for 40 bots, the server hired 39") {
		t.Fatalf("summary notes %q, want the hire cap note", f.summary().Notes)
	}
	for _, line := range f.agent.gmLines() {
		if strings.HasPrefix(line, ".bot hire raid 1 mage") {
			t.Fatalf("the batch beyond the cap was sent: %q", line)
		}
		if strings.HasPrefix(line, ".bot role ") && strings.HasSuffix(line, " fire") {
			t.Fatalf("a spec of the skipped batch was set: %q", line)
		}
	}
	if got := f.agent.roleOrder(); len(got) != 1 || got[0] != ".bot role Bot01 protection" {
		t.Fatalf("role lines %q, want only the mt's", got)
	}
}

// The server never answers `.bot role`: the walk stops before the refit, so no tier kit is set on a bot
// whose spec is unknown.
func TestRaidWalkWaitsForTheSpecReplyBeforeTheRefit(t *testing.T) {
	f := newFixture(t)
	f.agent.roleSilent = true
	roster := `{"format":"tb-raid-roster","version":2,"level":60,"tier":2,
 "batches":[{"role":"mt","hire":"1 warrior:tank","spec":"protection"},
            {"role":"ot","hire":"1t"},
            {"role":"rest","hire":"18d"}]}`
	path := filepath.Join(t.TempDir(), "roster.json")
	if err := os.WriteFile(path, []byte(roster), 0o600); err != nil {
		t.Fatal(err)
	}
	f.opts.hires = path
	f.defaultRun("onyxias_lair")
	if code := f.run(context.Background()); code != exitRaidWalkFailed {
		t.Fatalf("exit %d, want 18: %s", code, f.stderr.String())
	}
	if !strings.Contains(f.summary().EndReason, "no answer to .bot role Bot01 protection") {
		t.Fatalf("end reason %q, want the unanswered role line", f.summary().EndReason)
	}
	for _, line := range f.agent.gmLines() {
		if strings.HasPrefix(line, ".bot gear raid ") {
			t.Fatal("the refit went out without the spec applied")
		}
	}
}

// The refit's busy retry (speed3's side finding: 11 of 101 Hakkar attempts): the hire batch's own gear job can still
// be running when `.bot gear raid` fires, and the module answers ACTION_ERR|gear|busy instead of a verdict; the old
// code then waited out its whole 60 s line budget and ended 18. Busy twice, with the blocking job's verdict 1 s after
// each refusal, then ok: the walk drops both verdicts, re-sends once each is read, and carries on with the accepted
// send's verdict.
func TestRaidWalkRetriesTheRefitWhileAGearJobRuns(t *testing.T) {
	f := newFixture(t)
	f.agent.gearBusy = 2
	f.agent.gearBlockerIn = time.Second
	f.defaultRun("onyxias_lair")
	if code := f.run(context.Background()); code != exitOK {
		t.Fatalf("exit %d, want 0: %s", code, f.stderr.String())
	}
	var at []time.Time
	for i, line := range f.agent.gm {
		if strings.HasPrefix(line, ".bot gear raid ") {
			at = append(at, f.agent.gmAt[i])
		}
	}
	if len(at) != 3 {
		t.Fatalf("the refit went out %d times, want 3 (busy, busy, ok)", len(at))
	}
	for i := 1; i < len(at); i++ {
		if got := at[i].Sub(at[i-1]); got != time.Second {
			t.Fatalf("send %d came %s after send %d, want 1s (the blocking job's verdict)", i+1, got, i)
		}
	}
	if span := at[2].Sub(at[0]); span != 2*time.Second {
		t.Fatalf("the two blocking waits span %s, want exactly 2s", span)
	}
}

// The blocking job's verdict is not the refit's: a job that ran before the send holds a kit the refit was never
// ordered to change. It arrives 5 s in, after the old 3 s cap: the walk must still be waiting, drop it (never count
// it), and re-send once; success comes from the second send's own verdict.
func TestRaidWalkDropsTheBlockingGearsVerdict(t *testing.T) {
	f := newFixture(t)
	f.agent.gearBusy = 1
	f.agent.gearBlockerIn = 5 * time.Second
	f.defaultRun("onyxias_lair")
	if code := f.run(context.Background()); code != exitOK {
		t.Fatalf("exit %d, want 0: %s", code, f.stderr.String())
	}
	var at []time.Time
	for i, line := range f.agent.gm {
		if strings.HasPrefix(line, ".bot gear raid ") {
			at = append(at, f.agent.gmAt[i])
		}
	}
	if len(at) != 2 {
		t.Fatalf("the refit went out %d times, want 2 (busy, then exactly one re-send)", len(at))
	}
	if got := at[1].Sub(at[0]); got != 5*time.Second {
		t.Fatalf("the re-send came %s after the refused send, want 5s: the wait runs to the blocking verdict, "+
			"not a 3 s cap", got)
	}
	if strings.Contains(f.stderr.String(), "no TBM:GEAR") {
		t.Fatalf("the walk missed the accepted send's verdict: %s", f.stderr.String())
	}
}

// A busy reply whose blocking job never reports a verdict ends the refit at the deadline: the walk waits the rest of
// the budget for the job's TBM:GEAR line and, without it, does not re-send (a re-send could be refused, or a later
// verdict taken for its own).
func TestRaidWalkFailsWhenTheBlockingJobNeverVerdicts(t *testing.T) {
	f := newFixture(t)
	f.agent.gearBusy = 1
	f.defaultRun("onyxias_lair")
	if code := f.run(context.Background()); code != exitRaidWalkFailed {
		t.Fatalf("exit %d, want 18: %s", code, f.stderr.String())
	}
	var at []time.Time
	for i, line := range f.agent.gm {
		if strings.HasPrefix(line, ".bot gear raid ") {
			at = append(at, f.agent.gmAt[i])
		}
	}
	if len(at) != 1 {
		t.Fatalf("the refit went out %d times, want 1 (no re-send without the blocking job's verdict)", len(at))
	}
	if reason := f.summary().EndReason; !strings.Contains(reason, "blocking job") {
		t.Fatalf("end reason %q, want the missing blocking verdict", reason)
	}
	if waited := f.clock.now().Sub(at[0]); waited < gearLineWait {
		t.Fatalf("the walk gave up %s after the refused send, want the full %s budget", waited, gearLineWait)
	}
}

// A busy line that lands at the line budget's deadline ends the refit there: the wait for the blocking job's verdict
// has no budget left, and no re-send follows.
func TestRaidWalkStopsAtTheBudgetWhenBusyArrivesAtTheDeadline(t *testing.T) {
	f := newFixture(t)
	f.agent.gearBusy = 1
	f.agent.gearBusyDelay = gearLineWait
	f.defaultRun("onyxias_lair")
	if code := f.run(context.Background()); code != exitRaidWalkFailed {
		t.Fatalf("exit %d, want 18: %s", code, f.stderr.String())
	}
	var at []time.Time
	for i, line := range f.agent.gm {
		if strings.HasPrefix(line, ".bot gear raid ") {
			at = append(at, f.agent.gmAt[i])
		}
	}
	if len(at) != 1 {
		t.Fatalf("the refit went out %d times, want 1 (no re-send after the deadline)", len(at))
	}
	if reason := f.summary().EndReason; !strings.Contains(reason, "blocking job") {
		t.Fatalf("end reason %q, want the missing blocking verdict", reason)
	}
}

// A busy reply at every send, each followed by the blocking job's verdict, ends the walk on the module's own line:
// at most gearBusyTries sends, their waits inside the one 60 s line budget, exit 18.
func TestRaidWalkGivesUpTheRefitAfterFiveBusyReplies(t *testing.T) {
	f := newFixture(t)
	f.agent.gearBusy = gearBusyTries
	f.agent.gearBlockerIn = time.Second
	f.defaultRun("onyxias_lair")
	if code := f.run(context.Background()); code != exitRaidWalkFailed {
		t.Fatalf("exit %d, want 18: %s", code, f.stderr.String())
	}
	if !strings.Contains(f.summary().EndReason, "gear|busy") {
		t.Fatalf("end reason %q, want the module's busy line", f.summary().EndReason)
	}
	var at []time.Time
	for i, line := range f.agent.gm {
		if strings.HasPrefix(line, ".bot gear raid ") {
			at = append(at, f.agent.gmAt[i])
		}
	}
	if len(at) != gearBusyTries {
		t.Fatalf("the refit went out %d times, want the cap %d", len(at), gearBusyTries)
	}
	if span := at[len(at)-1].Sub(at[0]); span != time.Duration(gearBusyTries-1)*time.Second {
		t.Fatalf("the %d sends span %s, want exactly %s (one blocking verdict between each)",
			len(at), span, time.Duration(gearBusyTries-1)*time.Second)
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

// The settle is a poll of the group, not a fixed sleep: the refit waits for the last hire's join and carries on as
// soon as every hire is in it. Walk 12's last join landed 4-8 s after the hire answer; a hire that joins at 15 s is
// still waited for, up to the --hire-settle ceiling.
func TestRaidWalkHireSettleWaitsForALateJoin(t *testing.T) {
	f := newFixture(t)
	f.agent.partyJoinAt = 15 * time.Second
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
	if gear.IsZero() || gear.Sub(lastHire) < 15*time.Second {
		t.Fatalf("the refit went out %s after the last hire, want at least the 15 s late join", gear.Sub(lastHire))
	}
}

// waitHiresJoined ends on the group, not on the clock: every hire joined carries on at once; a last hire that joins
// 3 s later is waited for; one that never joins is left at the ceiling, and the refit after it reports the gap.
func TestWaitHiresJoinedPollsTheGroup(t *testing.T) {
	t.Run("every hire joined", func(t *testing.T) {
		clock := newFakeClock()
		a := newFakeAgent(t, clock)
		a.party = []string{"Tank", "Bot01"}
		w := &walk{env: walkEnv{now: clock.now, sleep: clock.sleep}, agent: a, mt: "Tank", rest: []string{"Bot01"}}
		start := clock.now()
		if err := w.waitHiresJoined(context.Background(), 20*time.Second); err != nil {
			t.Fatal(err)
		}
		if got := clock.now().Sub(start); got != 0 {
			t.Fatalf("a complete group took %s, want none", got)
		}
	})
	t.Run("a late hire is waited for", func(t *testing.T) {
		clock := newFakeClock()
		a := newFakeAgent(t, clock)
		a.party = []string{"Tank", "Bot01"}
		a.lastHireAt = clock.now()
		a.partyJoinAt = 3 * time.Second
		w := &walk{env: walkEnv{now: clock.now, sleep: clock.sleep}, agent: a, mt: "Tank", rest: []string{"Bot01"}}
		start := clock.now()
		if err := w.waitHiresJoined(context.Background(), 20*time.Second); err != nil {
			t.Fatal(err)
		}
		if got := clock.now().Sub(start); got != 3*time.Second {
			t.Fatalf("a hire joining at 3 s was waited for %s, want 3 s", got)
		}
	})
	t.Run("a hire that never joins ends at the ceiling", func(t *testing.T) {
		clock := newFakeClock()
		a := newFakeAgent(t, clock)
		a.party = []string{"Tank", "Bot01"}
		a.lastHireAt = clock.now()
		a.partyJoinAt = time.Minute
		w := &walk{env: walkEnv{now: clock.now, sleep: clock.sleep}, agent: a, mt: "Tank", rest: []string{"Bot01"}}
		start := clock.now()
		if err := w.waitHiresJoined(context.Background(), 20*time.Second); err != nil {
			t.Fatal(err)
		}
		if got := clock.now().Sub(start); got != 20*time.Second {
			t.Fatalf("a hire that never joins took %s, want the 20 s ceiling", got)
		}
	})
}

// TestRaidWalkHireSettleFlag (TortoiseBots#1367 proof): --hire-settle 0 sends the refit straight after the last
// hire with no sleep, so one live walk can prove the bind keeps its late joiners; the default is the 20 s settle,
// and a negative value is a usage error.
func TestRaidWalkHireSettleFlag(t *testing.T) {
	t.Run("0 skips the wait", func(t *testing.T) {
		f := newFixture(t)
		f.opts.hireSettle = 0
		var slept []time.Duration
		f.env.sleep = func(ctx context.Context, d time.Duration) error { // the fake env records every sleep
			slept = append(slept, d)
			return f.clock.sleep(ctx, d)
		}
		f.defaultRun("onyxias_lair")
		if code := f.run(context.Background()); code != exitOK {
			t.Fatalf("exit %d, want 0: %s\n%s", code, f.stderr.String(), f.stdout.String())
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
		if gear.IsZero() || !gear.Equal(lastHire) {
			t.Fatalf("the refit went out %s after the last hire, want no wait at all", gear.Sub(lastHire))
		}
		for _, d := range slept {
			if d == hireSettleWait {
				t.Fatalf("the walk slept the %s settle with --hire-settle 0: %v", hireSettleWait, slept)
			}
		}
		log, err := os.ReadFile(filepath.Join(f.out, "run.log"))
		if err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(string(log), "no wait for the hires' join summons (--hire-settle 0)") {
			t.Fatalf("run.log carries no --hire-settle 0 line: %s", log)
		}
		// The summary marks the run, so the suite never takes it as a tier-proof baseline (LastGood).
		sum := f.summary()
		if !sum.Roster.SettleShort || sum.Roster.HireSettleS == nil || *sum.Roster.HireSettleS != 0 {
			t.Fatalf("roster settle_short %v hire_settle_s %s, want true and 0", sum.Roster.SettleShort, settleSeconds(sum.Roster.HireSettleS))
		}
	})
	t.Run("the default is not marked", func(t *testing.T) {
		f := newFixture(t)
		f.defaultRun("onyxias_lair")
		if code := f.run(context.Background()); code != exitOK {
			t.Fatalf("exit %d, want 0: %s\n%s", code, f.stderr.String(), f.stdout.String())
		}
		sum := f.summary()
		if sum.Roster.SettleShort || sum.Roster.HireSettleS == nil || *sum.Roster.HireSettleS != hireSettleWait.Seconds() {
			t.Fatalf("roster settle_short %v hire_settle_s %s, want false and %v",
				sum.Roster.SettleShort, settleSeconds(sum.Roster.HireSettleS), hireSettleWait.Seconds())
		}
	})
	t.Run("default is hireSettleWait", func(t *testing.T) {
		o, code := parseRaidWalkArgs([]string{"onyxias_lair"}, io.Discard)
		if code != exitOK {
			t.Fatalf("parse exit %d, want 0", code)
		}
		if o.hireSettle != hireSettleWait {
			t.Fatalf("--hire-settle default %s, want %s", o.hireSettle, hireSettleWait)
		}
	})
	t.Run("a negative value is a usage error", func(t *testing.T) {
		if _, code := parseRaidWalkArgs([]string{"--hire-settle", "-1s", "onyxias_lair"}, io.Discard); code != exitUsage {
			t.Fatalf("--hire-settle -1s: exit %d, want %d", code, exitUsage)
		}
	})
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
	// The dashboard starts empty (the pre-placement read), so the walk's own fight files mid-walk.
	f.dash.fightsSeq = [][]walkFightRow{{}, {{ID: "4-5", Key: "Commander\x001793219890\x004-5", Dur: 1200, Outcome: "win", BossName: "Onyxia", Leg: "onyxia", Step: 3, Deaths: 1}}}
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
	// The pre-placement read is empty; the walk's own fight is live first, then finished.
	f.dash.fightsSeq = [][]walkFightRow{{}, {{ID: "4-5", Key: "k", Live: true}}, {{ID: "4-5", Key: "k", Dur: 1200, Outcome: "win", Step: 3, Deaths: 1, Warnings: 2}}}
	f.defaultRun("onyxias_lair")
	if code := f.run(context.Background()); code != exitOK {
		t.Fatalf("exit %d: %s", code, f.stderr.String())
	}
	sum := f.summary()
	if len(sum.Fights) != 1 || sum.Fights[0].Warnings != 2 || sum.Fights[0].Detail == nil {
		t.Fatalf("fights %+v", sum.Fights)
	}
	if sum.RunLabel != "until boss (step 21)" || sum.SpeedFellBack {
		t.Fatalf("run label %q, fell back %v", sum.RunLabel, sum.SpeedFellBack)
	}
}

// TestRaidWalkRetriesAFailedFightDetail: a detail request that fails once does not lose the fight's digest; a later poll fetches it.
func TestRaidWalkRetriesAFailedFightDetail(t *testing.T) {
	f := newFixture(t)
	f.opts.dashURL, f.opts.dashPassFile = "http://dash.test", "/tmp/pass"
	f.dash.failFight = 1
	f.dash.fightsSeq = [][]walkFightRow{{}, {{ID: "4-5", Key: "k", Dur: 1200, Outcome: "win", Step: 3, Deaths: 1}}}
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
	for step := 2; step <= 20; step++ {
		q = append(q, queuedEvent{raw: fmt.Sprintf("TBM:RUN|onyxias_lair|running|%d/21|onyxia|move|walk|walking", step)})
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
		for step := 2; step <= 20; step++ {
			q = append(q, queuedEvent{raw: fmt.Sprintf("TBM:RUN|onyxias_lair|running|%d/21|onyxia|move|walk|walking", step)})
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
		f.agent.startReply = startAt("onyxias_lair", 13)
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

// joinerWalk runs a walk whose pull is step 2 of onyxias_lair (the pull row
// at 49.8, -114.6, radius 10), with the given hostiles in every frame.
func joinerWalk(t *testing.T, hostiles []tbm.HostileRec) *fixture {
	t.Helper()
	return joinerWalkNote(t, hostiles,
		"TBM:RUN|onyxias_lair|running|2/21|onyxia|move|pull|pulling 12129 0xF130002C6C00C24B (order 1; shot from where it stands); joiners not read|until boss (step 21)")
}

// joinerWalkNote is joinerWalk with the pull's own status line, so a stand
// pull ("shot from firing stand 1") is counted exactly like a plain one.
func joinerWalkNote(t *testing.T, hostiles []tbm.HostileRec, pullLine string) *fixture {
	t.Helper()
	f := newFixture(t)
	f.agent.startReply = startAt("onyxias_lair", 1)
	f.agent.startQueue = []queuedEvent{
		{raw: pullLine},
		{raw: "TBM:RUN|onyxias_lair|done|20/21|onyxia|move|walk|stopped before step 21 (until boss): the party stays where it stands|until boss (step 21)"},
	}
	polls := 0
	f.agent.viewHook = func() {
		polls++
		if polls > 3 {
			f.clock.mu.Lock()
			f.clock.t = f.clock.t.Add(11 * time.Second)
			f.clock.mu.Unlock()
		}
	}
	f.agent.hostilesFn = func(n int) []tbm.HostileRec { return hostiles }
	if code := f.run(context.Background()); code != exitOK {
		t.Fatalf("exit %d: %s\n%s", code, f.stderr.String(), f.stdout.String())
	}
	return f
}

// readPullJoiners reads the walk's per-pull joiner record.
func readPullJoiners(t *testing.T, dir string) []raidwalk.PullJoiners {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join(dir, raidwalk.PullJoinersFName))
	if err != nil {
		t.Fatal(err)
	}
	var recs []raidwalk.PullJoiners
	if err := json.Unmarshal(raw, &recs); err != nil {
		t.Fatal(err)
	}
	return recs
}

func TestRaidWalkCountsPullJoinersFromFrames(t *testing.T) {
	// The pulled unit is at the pull row's point; a mate 5 yd off is the rest
	// of the pack and never counts, while 3 engaged units of another pack
	// inside the 10 s window do: the record says 3.
	f := joinerWalk(t, []tbm.HostileRec{
		{GUID: 0xF130002C6C00C24B, Entry: 12129, X: 49.8, Y: -114.6, HPPct: 90, Target: packet.GUID(1000)},
		{GUID: 0xF130002, Entry: 12129, X: 53.0, Y: -114.6, HPPct: 100, Target: packet.GUID(1000)},
		{GUID: 0xF130101, Entry: 12130, X: 0, Y: 0, HPPct: 100, Target: packet.GUID(1001)},
		{GUID: 0xF130102, Entry: 12130, X: 0, Y: 0, HPPct: 100, Target: packet.GUID(1002)},
		{GUID: 0xF130103, Entry: 12130, X: 0, Y: 0, HPPct: 100, Target: packet.GUID(1000)},
	})
	raw, err := os.ReadFile(filepath.Join(f.out, "run.log"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(raw), "step 2: joiners 3") {
		t.Fatalf("no step-keyed joiners line in run.log:\n%s", raw)
	}
	recs := readPullJoiners(t, f.out)
	if len(recs) != 1 || recs[0].Step != 2 || recs[0].Count != 3 || len(recs[0].GUIDs) != 3 {
		t.Fatalf("pulljoiners.json %+v, want one step-2 record with 3 guids", recs)
	}
}

// A neighbouring pack with the same entry as the pulled pack still joins: the
// pack is owned by spawn guid and position, not by creature entry.
func TestRaidWalkCountsSameEntryNeighbourAsJoiner(t *testing.T) {
	f := joinerWalk(t, []tbm.HostileRec{
		{GUID: 0xF130002C6C00C24B, Entry: 12129, X: 49.8, Y: -114.6, HPPct: 90, Target: packet.GUID(1000)},
		{GUID: 0xF130002, Entry: 12129, X: 53.0, Y: -114.6, HPPct: 100, Target: packet.GUID(1000)}, // pack mate
		// The next pull group's pack, 106 yd off, same entry 12129: joiners.
		{GUID: 0xF130501, Entry: 12129, X: -52.9, Y: -96.9, HPPct: 100, Target: packet.GUID(1001)},
		{GUID: 0xF130502, Entry: 12129, X: -53.1, Y: -97.0, HPPct: 100, Target: packet.GUID(1000)},
	})
	raw, err := os.ReadFile(filepath.Join(f.out, "run.log"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(raw), "step 2: joiners 2") {
		t.Fatalf("no same-entry joiners line in run.log:\n%s", raw)
	}
	recs := readPullJoiners(t, f.out)
	if len(recs) != 1 || recs[0].Step != 2 || recs[0].Count != 2 {
		t.Fatalf("pulljoiners.json %+v, want one step-2 record with 2 guids", recs)
	}
}

// A pull shot from a firing stand counts its joiners like a plain pull: the
// pulled pack (the guid and its mate within the row) is owned, and 3 engaged
// hostiles of another pack inside the window make "joiners 3".
func TestRaidWalkCountsStandPullJoiners(t *testing.T) {
	f := joinerWalkNote(t, []tbm.HostileRec{
		{GUID: 0xF130002C6C00C24B, Entry: 12129, X: 49.8, Y: -114.6, HPPct: 90, Target: packet.GUID(1000)},
		{GUID: 0xF130002, Entry: 12129, X: 53.0, Y: -114.6, HPPct: 100, Target: packet.GUID(1000)}, // pack mate
		// The neighbouring pack, 106 yd off: the stand pull's joiners.
		{GUID: 0xF130501, Entry: 12130, X: -52.9, Y: -96.9, HPPct: 100, Target: packet.GUID(1001)},
		{GUID: 0xF130502, Entry: 12130, X: -53.1, Y: -97.0, HPPct: 100, Target: packet.GUID(1002)},
		{GUID: 0xF130503, Entry: 12130, X: -52.8, Y: -97.4, HPPct: 100, Target: packet.GUID(1000)},
	}, "TBM:RUN|onyxias_lair|running|2/21|onyxia|move|pull|pulling 12129 0xF130002C6C00C24B (order 1; shot from firing stand 1); joiners not read|until boss (step 21)")
	raw, err := os.ReadFile(filepath.Join(f.out, "run.log"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(raw), "step 2: joiners 3") {
		t.Fatalf("no stand-pull joiners line in run.log:\n%s", raw)
	}
	recs := readPullJoiners(t, f.out)
	if len(recs) != 1 || recs[0].Step != 2 || recs[0].Count != 3 || len(recs[0].GUIDs) != 3 {
		t.Fatalf("pulljoiners.json %+v, want one step-2 record with 3 guids", recs)
	}
}

// Headline: the walk 27 defect was move-step stand pulls counting their own
// pack mates (a-r2 step 5, c-r3 step 262): the run still stands on its
// approach move row when the stand pull goes out, so the pack is owned from
// the pulled unit's plan row. Here the pull of 12129 is ordered on move step 1
// while its pack stands at pull row step 2: the mate 3 yd off never counts,
// and the 3 engaged units of the far pack do.
func TestRaidWalkCountsStandPullJoinersOnMoveStep(t *testing.T) {
	f := joinerWalkNote(t, []tbm.HostileRec{
		{GUID: 0xF130002C6C00C24B, Entry: 12129, X: 49.8, Y: -114.6, HPPct: 90, Target: packet.GUID(1000)},
		{GUID: 0xF130002, Entry: 12129, X: 53.0, Y: -114.6, HPPct: 100, Target: packet.GUID(1000)}, // pack mate
		{GUID: 0xF130501, Entry: 12130, X: -52.9, Y: -96.9, HPPct: 100, Target: packet.GUID(1001)},
		{GUID: 0xF130502, Entry: 12130, X: -53.1, Y: -97.0, HPPct: 100, Target: packet.GUID(1002)},
		{GUID: 0xF130503, Entry: 12130, X: -52.8, Y: -97.4, HPPct: 100, Target: packet.GUID(1000)},
	}, "TBM:RUN|onyxias_lair|running|1/21|onyxia|move|pull|pulling 12129 0xF130002C6C00C24B (order 1; shot from firing stand 1); joiners not read|until boss (step 21)")
	raw, err := os.ReadFile(filepath.Join(f.out, "run.log"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(raw), "step 1: joiners 3") {
		t.Fatalf("no move-step stand-pull joiners line in run.log:\n%s", raw)
	}
	recs := readPullJoiners(t, f.out)
	if len(recs) != 1 || recs[0].Step != 1 || recs[0].Count != 3 || len(recs[0].GUIDs) != 3 {
		t.Fatalf("pulljoiners.json %+v, want one step-1 record with 3 guids", recs)
	}
}

// A boss pull has no "pulling <entry> <guid>" line: the run leader walks to
// the firing stand and the tank engages on arrival ("boss|walk|fighting").
// That fight start arms the same 10 s window, so a boss stand pull counts the
// hostiles outside the boss row as joiners too.
func TestRaidWalkCountsBossPullJoiners(t *testing.T) {
	f := newFixture(t)
	f.opts.until = "none"
	f.agent.startReply = &commanderReply{Reply: tbm.Reply{Kind: tbm.KindAck, OK: true,
		Ack:  &tbm.Ack{Intent: "run start", Scope: "party", Count: 1, Executor: "-"},
		Line: "TBM:ACTION_ACK|run start|party|1|-",
		Lines: []string{
			"TBM:RUN|onyxias_lair|running|20/21|onyxia|move|walk|walking|until boss (step 21)",
			"TBM:ACTION_ACK|run start|party|1|-",
		}}}
	f.agent.startQueue = []queuedEvent{
		{raw: "TBM:RUN|onyxias_lair|running|21/21|onyxia|boss|walk|fighting: the tank engages 10184 on arrival (1.0 yd)|until boss (step 21)"},
		{raw: "TBM:RUN|onyxias_lair|done|20/21|onyxia|move|walk|stopped before step 21 (until boss): the party stays where it stands|until boss (step 21)"},
	}
	polls := 0
	f.agent.viewHook = func() {
		polls++
		if polls > 3 {
			f.clock.mu.Lock()
			f.clock.t = f.clock.t.Add(11 * time.Second)
			f.clock.mu.Unlock()
		}
	}
	f.agent.hostilesFn = func(n int) []tbm.HostileRec {
		return []tbm.HostileRec{
			{GUID: 0xF13000000010, Entry: 10184, X: 0.7, Y: -212.0, HPPct: 99, Target: packet.GUID(1000)},
			// The boss row's own adds, within its radius: the boss pack.
			{GUID: 0xF13000000011, Entry: 11368, X: 5.0, Y: -212.0, HPPct: 100, Target: packet.GUID(1000)},
			{GUID: 0xF13000000012, Entry: 11368, X: -5.0, Y: -212.0, HPPct: 100, Target: packet.GUID(1001)},
			// 3 hostiles of another pack, 100 yd off: joiners.
			{GUID: 0xF13000000021, Entry: 12130, X: 100.7, Y: -212.0, HPPct: 100, Target: packet.GUID(1001)},
			{GUID: 0xF13000000022, Entry: 12130, X: 101.0, Y: -213.0, HPPct: 100, Target: packet.GUID(1002)},
			{GUID: 0xF13000000023, Entry: 12130, X: 100.0, Y: -211.0, HPPct: 100, Target: packet.GUID(1000)},
		}
	}
	if code := f.run(context.Background()); code != exitOK {
		t.Fatalf("exit %d: %s\n%s", code, f.stderr.String(), f.stdout.String())
	}
	raw, err := os.ReadFile(filepath.Join(f.out, "run.log"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(raw), "step 21: joiners 3") {
		t.Fatalf("no boss-pull joiners line in run.log:\n%s", raw)
	}
	recs := readPullJoiners(t, f.out)
	if len(recs) != 1 || recs[0].Step != 21 || recs[0].Count != 3 || len(recs[0].GUIDs) != 3 {
		t.Fatalf("pulljoiners.json %+v, want one step-21 record with 3 guids", recs)
	}
}

// A boss window marks the boss row's entries and the plan's hold/add entries
// as "boss add", not as joiners: summoned and arriving adds of those entries
// join the fight as it runs, and are no other pack joining the pull.
func TestBossAddsOfMarksRowHoldAndFirstEntries(t *testing.T) {
	w := &walk{
		plan: &dungeon.Plan{Legs: []dungeon.Leg{{
			Boss: &dungeon.Boss{Entry: 100, First: []uint32{200}, Hold: &dungeon.Hold{Entries: []uint32{300}}},
		}}},
		rows: planexport.Rows{Steps: []planexport.StepRow{
			{Kind: planexport.KindBoss, Entry: 100, Entries: []uint32{150}},
		}},
	}
	got := w.bossAddsOf(1)
	for _, e := range []uint32{150, 200, 300} {
		if !got[e] {
			t.Fatalf("bossAddsOf(1) = %v, want entry %d marked boss add", got, e)
		}
	}
	if got[999] {
		t.Fatalf("bossAddsOf(1) = %v, want no unlisted entry", got)
	}
	w.rows.Steps[0].Kind = planexport.KindPull
	if got := w.bossAddsOf(1); len(got) != 0 {
		t.Fatalf("a trash pull's bossAddsOf = %v, want nothing", got)
	}
}

// A stand pull still inside its 10 s window when the boss's first fighting line
// goes by must not block the boss window: the pull's own window ends at that
// line (its record is written then) and the boss window opens, so both records
// land in pulljoiners.json. The pull line is pumped on the first poll and the
// fight line is pushed from that poll's frame, so the next poll (same fake
// clock) sees it 0 s after the pull.
func TestRaidWalkBossWindowOpensOverOpenPullWindow(t *testing.T) {
	f := newFixture(t)
	f.opts.until = "none"
	f.agent.startReply = &commanderReply{Reply: tbm.Reply{Kind: tbm.KindAck, OK: true,
		Ack:  &tbm.Ack{Intent: "run start", Scope: "party", Count: 1, Executor: "-"},
		Line: "TBM:ACTION_ACK|run start|party|1|-",
		Lines: []string{
			"TBM:RUN|onyxias_lair|running|20/21|onyxia|move|walk|walking|until boss (step 21)",
			"TBM:ACTION_ACK|run start|party|1|-",
		}}}
	// The pull line goes through the poll loop (startQueue), where pullStarted arms the window.
	f.agent.startQueue = []queuedEvent{
		{raw: "TBM:RUN|onyxias_lair|running|2/21|onyxia|move|pull|pulling 12129 0xF130002C6C00C24B (order 1; shot from where it stands); joiners not read|until boss (step 21)"},
	}
	polls := 0
	f.agent.viewHook = func() {
		polls++
		if polls > 3 {
			f.clock.mu.Lock()
			f.clock.t = f.clock.t.Add(11 * time.Second)
			f.clock.mu.Unlock()
		}
	}
	others := []tbm.HostileRec{
		{GUID: 0xF13000000021, Entry: 12130, X: 100.7, Y: -212.0, HPPct: 100, Target: packet.GUID(1001)},
		{GUID: 0xF13000000022, Entry: 12130, X: 101.0, Y: -213.0, HPPct: 100, Target: packet.GUID(1002)},
		{GUID: 0xF13000000023, Entry: 12130, X: 100.0, Y: -211.0, HPPct: 100, Target: packet.GUID(1000)},
	}
	pulled := tbm.HostileRec{GUID: 0xF130002C6C00C24B, Entry: 12129, X: 49.8, Y: -114.6, HPPct: 90, Target: packet.GUID(1000)}
	// Views before the run starts (the hire wait) see no frames of the pull.
	afterStart := 0
	f.agent.hostilesFn = func(n int) []tbm.HostileRec {
		f.agent.mu.Lock()
		started := f.agent.runStarted
		f.agent.mu.Unlock()
		if !started {
			return others
		}
		afterStart++
		switch afterStart {
		case 1:
			// The first frame after the pull line's poll: the pulled unit is here
			// (the window seeds on it), and the fight line lands on the next poll.
			f.agent.push("TBM:RUN|onyxias_lair|running|21/21|onyxia|boss|walk|fighting: the tank engages 10184 on arrival (1.0 yd)|until boss (step 21)", "")
			return append([]tbm.HostileRec{pulled}, others...)
		case 3:
			f.agent.push("TBM:RUN|onyxias_lair|done|20/21|onyxia|move|walk|stopped before step 21 (until boss): the party stays where it stands|until boss (step 21)", "")
		}
		return others
	}
	if code := f.run(context.Background()); code != exitOK {
		t.Fatalf("exit %d: %s\n%s", code, f.stderr.String(), f.stdout.String())
	}
	raw, err := os.ReadFile(filepath.Join(f.out, "run.log"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(raw), "step 2: joiners 3") || !strings.Contains(string(raw), "step 21: joiners 3") {
		t.Fatalf("run.log lacks a joiners line for both pulls:\n%s", raw)
	}
	recs := readPullJoiners(t, f.out)
	if len(recs) != 2 {
		t.Fatalf("pulljoiners.json %+v, want the step-2 and step-21 records", recs)
	}
	byStep := map[int]raidwalk.PullJoiners{}
	for _, r := range recs {
		byStep[r.Step] = r
	}
	for _, step := range []int{2, 21} {
		if r, ok := byStep[step]; !ok || r.Count != 3 || len(r.GUIDs) != 3 {
			t.Fatalf("pulljoiners.json %+v, want a step-%d record with 3 guids", recs, step)
		}
	}
}

// No row in range: the pack is the seed's aggro-together mates seen in the
// first frame within 15 yd, and the record says "no row". Here the pull is
// ordered on move step 1 and names an entry no pull/boss row holds, so no row
// is in range: the mate 2 yd off is owned and the far neighbour joins.
func TestRaidWalkPullWithNoRowUsesSeedMates(t *testing.T) {
	f := joinerWalkNote(t, []tbm.HostileRec{
		{GUID: 0xF130002C6C00C24B, Entry: 77777, X: 49.8, Y: -114.6, HPPct: 90, Target: packet.GUID(1000)},
		{GUID: 0xF130002, Entry: 77777, X: 52.0, Y: -114.6, HPPct: 100, Target: packet.GUID(1000)}, // mate 2 yd off
		{GUID: 0xF130501, Entry: 12130, X: 0, Y: 0, HPPct: 100, Target: packet.GUID(1001)},
	}, "TBM:RUN|onyxias_lair|running|1/21|onyxia|move|pull|pulling 77777 0xF130002C6C00C24B (order 1; shot from where it stands); joiners not read|until boss (step 21)")
	raw, err := os.ReadFile(filepath.Join(f.out, "run.log"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(raw), "step 1: joiners 1") {
		t.Fatalf("no no-row joiners line in run.log:\n%s", raw)
	}
	recs := readPullJoiners(t, f.out)
	if len(recs) != 1 || recs[0].Step != 1 || recs[0].Count != 1 || !recs[0].NoRow {
		t.Fatalf("pulljoiners.json %+v, want one step-1 record with 1 guid and no_row", recs)
	}
	if !hasNote(f.summary(), "step 1: no row") {
		t.Fatalf("notes %q, want the no-row note", f.summary().Notes)
	}
}

// seeding waits for it and the mate still does not count as a joiner.
func TestRaidWalkPullSeedRetriesAfterFirstFrame(t *testing.T) {
	f := newFixture(t)
	f.agent.startReply = startAt("onyxias_lair", 1)
	f.agent.startQueue = []queuedEvent{
		{raw: "TBM:RUN|onyxias_lair|running|3/21|onyxia|move|pull|pulling 12129 0xF130002C6C00C24B (order 1; shot from where it stands); joiners not read|until boss (step 21)"},
		{raw: "TBM:RUN|onyxias_lair|done|20/21|onyxia|move|walk|stopped before step 21 (until boss): the party stays where it stands|until boss (step 21)"},
	}
	polls := 0
	f.agent.viewHook = func() {
		polls++
		if polls > 4 {
			f.clock.mu.Lock()
			f.clock.t = f.clock.t.Add(11 * time.Second)
			f.clock.mu.Unlock()
		}
	}
	mate := tbm.HostileRec{GUID: 0xF130002, Entry: 12129, X: 53.0, Y: -114.6, HPPct: 100, Target: packet.GUID(1000)}
	joiner := tbm.HostileRec{GUID: 0xF130501, Entry: 12130, X: 0, Y: 0, HPPct: 100, Target: packet.GUID(1001)}
	seed := tbm.HostileRec{GUID: 0xF130002C6C00C24B, Entry: 12129, X: 49.8, Y: -114.6, HPPct: 90, Target: packet.GUID(1000)}
	views := 0
	f.agent.hostilesFn = func(n int) []tbm.HostileRec {
		views++
		if views <= 2 {
			return []tbm.HostileRec{mate, joiner} // the seed is not in view yet
		}
		return []tbm.HostileRec{seed, mate, joiner}
	}
	if code := f.run(context.Background()); code != exitOK {
		t.Fatalf("exit %d: %s\n%s", code, f.stderr.String(), f.stdout.String())
	}
	recs := readPullJoiners(t, f.out)
	if len(recs) != 1 || recs[0].Step != 3 || recs[0].Count != 1 || len(recs[0].GUIDs) != 1 {
		t.Fatalf("pulljoiners.json %+v, want one step-3 record with 1 guid (the mate waited out the retry)", recs)
	}
}

// The "waiting for the tank to stand still before pulling" line opens no
// window: only the actual pull line does. Here the waiting line goes by first
// and the pull line never comes, so no record is written at all.
func TestRaidWalkWaitingLineOpensNoWindow(t *testing.T) {
	f := newFixture(t)
	f.agent.startReply = startAt("onyxias_lair", 1)
	f.agent.startQueue = []queuedEvent{
		{raw: "TBM:RUN|onyxias_lair|running|2/21|onyxia|move|pull|waiting for the tank to stand still before pulling 12129 0xF130002C6C00C24B|until boss (step 21)"},
		{raw: "TBM:RUN|onyxias_lair|done|20/21|onyxia|move|walk|stopped before step 21 (until boss): the party stays where it stands|until boss (step 21)"},
	}
	polls := 0
	f.agent.viewHook = func() {
		polls++
		if polls > 3 {
			f.clock.mu.Lock()
			f.clock.t = f.clock.t.Add(11 * time.Second)
			f.clock.mu.Unlock()
		}
	}
	f.agent.hostilesFn = func(n int) []tbm.HostileRec {
		return []tbm.HostileRec{
			{GUID: 0xF130002C6C00C24B, Entry: 12129, X: 49.8, Y: -114.6, HPPct: 90, Target: packet.GUID(1000)},
			{GUID: 0xF130501, Entry: 12130, X: 0, Y: 0, HPPct: 100, Target: packet.GUID(1001)},
		}
	}
	if code := f.run(context.Background()); code != exitOK {
		t.Fatalf("exit %d: %s\n%s", code, f.stderr.String(), f.stdout.String())
	}
	recs := readPullJoiners(t, f.out)
	if len(recs) != 0 {
		t.Fatalf("pulljoiners.json %+v, want no record: the waiting line opens no window", recs)
	}
}

// The boss window's frames mark summoned/arriving boss adds instead of
// counting them: a unit of a boss-add entry engaged inside the window lands in
// the record's boss-add set, while a true neighbour of another entry joins.
func TestRaidWalkPullFramedMarksBossAdds(t *testing.T) {
	now := time.Now()
	w := &walk{
		env:           walkEnv{now: func() time.Time { return now }},
		rows:          planexport.Rows{},
		hostiles:      map[string]hostileState{},
		partyG:        map[uint64]bool{1000: true},
		pullJoinsAt:   now,
		pullJoinsStep: 7,
		pullBossAdds:  map[uint32]bool{11368: true},
		pullPack:      map[packet.GUID]bool{},
		pullJoinsSeen: map[packet.GUID]bool{},
		pullAddsSeen:  map[packet.GUID]bool{},
	}
	f := &tbm.ViewFrame{
		Party: []tbm.PartyRec{{GUID: 1000, Name: "Tank"}},
		Hostiles: []tbm.HostileRec{
			{GUID: 0xF13000000011, Entry: 11368, HPPct: 100, Target: packet.GUID(1000)},
			{GUID: 0xF13000000021, Entry: 12130, HPPct: 100, Target: packet.GUID(1000)},
		},
	}
	w.pullFramed(now.Add(time.Second), f)
	if len(w.pullJoinsSeen) != 1 || !w.pullJoinsSeen[packet.GUID(0xF13000000021)] {
		t.Fatalf("joins %+v, want only the 12130 neighbour", w.pullJoinsSeen)
	}
	if len(w.pullAddsSeen) != 1 || !w.pullAddsSeen[packet.GUID(0xF13000000011)] {
		t.Fatalf("adds %+v, want the 11368 boss add marked instead", w.pullAddsSeen)
	}
	w.pullJoinersFlush(now.Add(11 * time.Second))
	if len(w.pullJoins) != 1 || w.pullJoins[0].Count != 1 || len(w.pullJoins[0].BossAdds) != 1 {
		t.Fatalf("record %+v, want 1 joiner plus 1 boss add", w.pullJoins)
	}
}

// The boss latch clears when the walk resumes at an earlier step: a step
// decrease forgets the armed steps, so the same boss fight later arms a fresh
// window and its joiners are counted.
func TestRaidWalkBossLatchClearsOnStepDecrease(t *testing.T) {
	w := &walk{
		rows: planexport.Rows{Steps: []planexport.StepRow{
			{Kind: planexport.KindBoss, Entry: 10184, X: 0, Y: -212, Radius: 30},
		}},
		bossPullArmed: map[int]bool{1: true},
		haveStatus:    true,
		lastStatus:    tbm.RunStatus{State: tbm.RunRunning, Step: 5},
	}
	now := time.Now()
	w.pullStarted(now, tbm.RunStatus{State: tbm.RunRunning, Step: 1, Kind: planexport.KindBoss, Note: "fighting: the tank engages 10184 on arrival"})
	if w.pullJoinsAt.IsZero() || w.pullJoinsStep != 1 {
		t.Fatalf("no boss window after the step decrease (armed %+v, at %v)", w.bossPullArmed, w.pullJoinsAt)
	}
}

// pollPageStub is a walkAgent that answers each events read with the next page of TBM run lines and fails any
// other RPC, so one pollOnce call reads exactly its page and no frame.
type pollPageStub struct {
	pages [][]string
	seq   uint64
}

func (s *pollPageStub) Call(ctx context.Context, method string, params, out any, timeout time.Duration) error {
	if method != "events" {
		return errors.New("unexpected RPC " + method)
	}
	page, ok := out.(*events.Page)
	if !ok {
		return errors.New("events: want *events.Page")
	}
	var next events.Page
	if len(s.pages) > 0 {
		for _, line := range s.pages[0] {
			s.seq++
			next.Events = append(next.Events, events.Event{Seq: s.seq, Kind: events.KindSystem, Text: line})
		}
		s.pages = s.pages[1:]
	}
	next.Next = s.seq
	*page = next
	return nil
}

// pollOnce hands pullStarted the status the walk held BEFORE the line it reads, so a step decrease clears the
// boss latch and the same boss's next fighting line opens a fresh joiner window. pollOnce used to store the new
// status first: the decrease then compared a status with itself and the latch stayed armed.
func TestRaidWalkPollOnceReopensBossWindowAfterStepDecrease(t *testing.T) {
	clock := time.Date(2026, 11, 2, 12, 0, 0, 0, time.UTC)
	plan := &dungeon.Plan{Name: "onyxias_lair", Map: 249, Legs: []dungeon.Leg{{Boss: &dungeon.Boss{Entry: 10184}}}}
	rows := planexport.Rows{Steps: []planexport.StepRow{
		{Kind: planexport.KindMove}, {Kind: planexport.KindMove}, {Kind: planexport.KindMove}, {Kind: planexport.KindMove},
		{Kind: planexport.KindBoss, Entry: 10184, X: 0, Y: -212, Radius: 30},
	}}
	const fight = "TBM:RUN|onyxias_lair|running|5/5|onyxia|boss|walk|fighting: the tank engages 10184 on arrival (1.0 yd)|until step 5 (step 5)"
	const back = "TBM:RUN|onyxias_lair|running|2/5|onyxia|move|walk|walking|until step 5 (step 5)"
	w := &walk{
		plan:    plan,
		rows:    rows,
		tracker: raidwalk.NewTracker(plan, nil),
		agent:   &pollPageStub{pages: [][]string{{fight}, {back}, {fight}}},
		env:     walkEnv{now: func() time.Time { return clock }},
	}
	poll := func(label string) {
		t.Helper()
		if _, err := w.pollOnce(context.Background(), false); err != nil {
			t.Fatalf("%s: %v", label, err)
		}
	}
	// The first fight opens the boss window at t0.
	poll("first fight")
	if w.pullJoinsAt.IsZero() || w.pullJoinsStep != 5 {
		t.Fatalf("no boss window on the first fight (at %v, step %d)", w.pullJoinsAt, w.pullJoinsStep)
	}
	// Eleven seconds on, the walk resumes at step 2: the decrease clears the latch and this poll's end closes the old window.
	clock = clock.Add(11 * time.Second)
	poll("step decrease")
	if len(w.pullJoins) != 1 || w.pullJoins[0].Step != 5 {
		t.Fatalf("records %+v, want the first window's step-5 record", w.pullJoins)
	}
	// The same boss fights again: a new window opens at that line.
	clock = clock.Add(time.Second)
	poll("second fight")
	if !w.pullJoinsAt.Equal(clock) || w.pullJoinsStep != 5 {
		t.Fatalf("no new boss window after the step decrease (at %v, step %d; want %v, step 5; armed %+v)",
			w.pullJoinsAt, w.pullJoinsStep, clock, w.bossPullArmed)
	}
}

func TestRaidWalkTeardownReasonNamesTheFailingStep(t *testing.T) {
	f := newFixture(t)
	f.defaultRun("onyxias_lair")
	f.env.teardown = func(args []string, stdout, stderr io.Writer) int {
		fmt.Fprint(stdout, `{"server":"m910q","slot":"2","steps":[{"step":"gone","status":"fail","detail":"still in view after 10 s: Bot01"}],"failed":2}`)
		return 14
	}
	code := f.run(context.Background())
	if code != exitOK {
		t.Fatalf("exit %d, want 0 (a failed teardown step alone keeps the walk green): %s", code, f.stderr.String())
	}
	sum := f.summary()
	if !sum.Teardown.Failed {
		t.Fatalf("teardown %+v, want failed", sum.Teardown)
	}
	if sum.Teardown.Reason != "gone: still in view after 10 s: Bot01" {
		t.Fatalf("reason %q", sum.Teardown.Reason)
	}
	if !hasNote(sum, "gone: still in view after 10 s: Bot01") {
		t.Fatalf("notes %q, want the teardown reason", sum.Notes)
	}
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

// turtle-agent#379, round 2: the revive's reset wait never counts the frame's own critter flag — a hurt 'k' row at
// 60 % still lets the reset complete; a hurt row without the flag still holds it. The band fallback is a units-read
// test (raidwalk), because the frame carries no max health for it.
func TestResetReadyIgnoresCritterFlag(t *testing.T) {
	now := newFakeClock().now()
	fresh := now.Add(-time.Second)
	w := &walk{partyG: map[uint64]bool{}, hostiles: map[string]hostileState{
		"0xF130001234000001": {entry: 2914, kind: "k", hppct: 60, at: fresh},
	}, bossRows: map[uint32]hostileState{}, botAt: map[string]botPos{}}
	for i := range 10 {
		if got := w.resetReady(now.Add(time.Duration(i) * 2 * time.Second)); got {
			t.Fatalf("reset ready at %ds with only a hurt critter in view", i*2)
		}
	}
	// Held 20 s on a critter alone: the wait completes.
	if got := w.resetReady(now.Add(20 * time.Second)); !got {
		t.Fatal("no reset after 20 s with only a hurt critter in view")
	}
	// Targeted by nothing, hurt, no flag: still a blocker.
	w = &walk{partyG: map[uint64]bool{}, hostiles: map[string]hostileState{
		"0xF130001234000001": {entry: 2914, kind: "-", hppct: 60, at: fresh},
	}, bossRows: map[uint32]hostileState{}, botAt: map[string]botPos{}}
	if got := w.resetReady(now.Add(time.Minute)); got {
		t.Fatal("reset ready with a hurt level-1-by-level row the frame did not flag")
	}
	// A level-5 critter the frame flags is still a critter: the flag is primary at any level.
	w = &walk{partyG: map[uint64]bool{}, hostiles: map[string]hostileState{
		"0xF130001234000002": {entry: 2915, kind: "k", hppct: 60, at: fresh},
	}, bossRows: map[uint32]hostileState{}, botAt: map[string]botPos{}}
	w.resetReady(now)
	if got := w.resetReady(now.Add(20 * time.Second)); !got {
		t.Fatal("no reset after 20 s with only a flagged critter in view")
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
	// 29-world statuses for the 29-step fixture migration.
	var q []queuedEvent
	for step := 2; step <= 28; step++ {
		q = append(q, queuedEvent{raw: fmt.Sprintf("TBM:RUN|%s|running|%d/29|onyxia|move|walk|walking", "onyxias_lair", step)})
	}
	q = append(q, queuedEvent{raw: "TBM:RUN|onyxias_lair|done|28/29|onyxia|move|walk|stopped before step 29 (until boss): the party stays where it stands"})
	f.agent.mu.Lock()
	f.agent.startQueue = q
	f.agent.mu.Unlock()
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
	return zgStartUntil(total, next, next)
}

// zgStartUntil is zgStart with an until label naming untilStep: the until the run is given (the
// tests that override --until pass their own step).
func zgStartUntil(total, next, untilStep int) *commanderReply {
	return &commanderReply{Reply: tbm.Reply{Kind: tbm.KindAck, OK: true,
		Ack:  &tbm.Ack{Intent: "run start", Scope: "party", Count: 1, Executor: "-"},
		Line: "TBM:ACTION_ACK|run start|party|1|-",
		Lines: []string{
			fmt.Sprintf("TBM:RUN|molten_core|running|1/%d|lucifron|move|walk|walking|until step %d (step %d)", total, untilStep, untilStep),
			"TBM:ACTION_ACK|run start|party|1|-",
		}}}
}

// zgThekalStart is the run start's answer for the Thekal early hand-over: the run at the step
// BEFORE the boss, with the until label the boss-done clause carries.
func zgThekalStart(total, boss int) *commanderReply {
	lines := []string{
		fmt.Sprintf("TBM:RUN|zul_gurub|running|%d/%d|high_priest_thekal|move|walk|walking|until step %d (step %d)", boss-1, total, boss+1, boss+1),
		"TBM:ACTION_ACK|run start|party|1|-",
	}
	return &commanderReply{Reply: tbm.Reply{Kind: tbm.KindAck, OK: true,
		Ack:  &tbm.Ack{Intent: "run start", Scope: "party", Count: 1, Executor: "-"},
		Line: lines[0], Lines: lines}}
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

// earlyEngageFixture is a Molten Core boss-done walk whose run stands on step with the boss engaged in the frames
// (a target), and whose main tank stands mtYd 2D from the boss row (east of it). The engaged row is 12118, the boss
// of the fixture's boss step; step must be in that boss's leg.
func earlyEngageFixture(t *testing.T, step int, mtYd float32) (*fixture, int, int) {
	t.Helper()
	f, next, total := bossDoneFixture(t)
	rows := mcRows(t)
	boss := next - 1
	if step < 1 || step > boss || rows.Steps[step-1].Leg != rows.Steps[boss-1].Leg {
		t.Fatalf("fixture rows: step %d is not before the boss step %d in the same leg", step, boss)
	}
	f.agent.startReply = zgStart(total, next)
	f.agent.mu.Lock()
	f.agent.startQueue = []queuedEvent{
		{raw: fmt.Sprintf("TBM:RUN|molten_core|running|%d/%d|lucifron|move|walk|fighting", step, total)},
		{raw: fmt.Sprintf("TBM:RUN|molten_core|done|%d/%d|lucifron|move|walk|stopped before step %d (until step %d): the party stays where it stands", next, total, next, next)},
	}
	f.agent.mu.Unlock()
	f.agent.pos = map[string][3]float32{"Bot01": {rows.Steps[boss-1].X + mtYd, rows.Steps[boss-1].Y, rows.Steps[boss-1].Z}}
	f.agent.hostilesFn = func(n int) []tbm.HostileRec {
		return []tbm.HostileRec{{GUID: 9, Entry: 12118, HPPct: 100, Target: packet.GUID(1000)}}
	}
	return f, next, total
}

// Walk 24 (Thekal's shape, Molten Core): the run is on the step BEFORE its boss step, in the same leg, its until
// goes past that boss step, the engaged entry belongs to the plan boss (its entry, phase entries, first adds or
// hold entries) and the main tank stands at the boss row. The run leader fights it: no run stop.
func TestRaidWalkBossDoneEarlyEngageLetsTheRunFightIt(t *testing.T) {
	rows := mcRows(t)
	_, next, _ := bossDoneFixture(t)
	boss := next - 1
	if boss < 2 || rows.Steps[boss-2].Leg != rows.Steps[boss-1].Leg {
		t.Fatalf("fixture rows: step %d and the boss step %d are not in the same leg", boss-1, boss)
	}
	fx, _, _ := earlyEngageFixture(t, boss-1, 10)
	if code := fx.run(context.Background()); code != exitOK {
		t.Fatalf("exit %d: %s\n%s", code, fx.stderr.String(), fx.stdout.String())
	}
	if n := countPrefix(fx.agent.gmLines(), ".bot run stop"); n != 0 {
		t.Fatalf("%d run stops, want 0: the run leader fights its boss one step early", n)
	}
	if sum := fx.summary(); sum.EndReason != "until" {
		t.Fatalf("summary end %q, want until", sum.EndReason)
	}
	if !hasNote(fx.summary(), "before its boss step") {
		t.Fatalf("notes %q, want the early-engage note", fx.summary().Notes)
	}
	if !hasNote(fx.summary(), "so the run leader fights it") {
		t.Fatalf("notes %q", fx.summary().Notes)
	}
}

// The hand-over reaches two steps back, the walk-24 approach case (Thekal's approach point sits inside the pull):
// the party is at the boss row, so the run leader fights it.
func TestRaidWalkBossDoneEarlyEngageTwoStepsBackLetsTheRunFightIt(t *testing.T) {
	_, next, _ := bossDoneFixture(t)
	f, _, _ := earlyEngageFixture(t, next-1-2, 10)
	if code := f.run(context.Background()); code != exitOK {
		t.Fatalf("exit %d: %s\n%s", code, f.stderr.String(), f.stdout.String())
	}
	if n := countPrefix(f.agent.gmLines(), ".bot run stop"); n != 0 {
		t.Fatalf("%d run stops, want 0: two steps back is still the boss's own pull", n)
	}
	if !hasNote(f.summary(), "before its boss step") {
		t.Fatalf("notes %q, want the early-engage note", f.summary().Notes)
	}
}

// Boss-done boss death: the party engages its boss on the approach step and the run leader fights it on the boss step
// until the boss is down. No run stop, the walk ends at its until (exit 0) and the boss is credited killed.
func TestRaidWalkBossDoneRunLeaderKillEndsAtUntil(t *testing.T) {
	_, next, _ := bossDoneFixture(t)
	f, _, total := earlyEngageFixture(t, next-1-5, 10)
	f.agent.startReply = zgStart(total, next)
	f.agent.mu.Lock()
	f.agent.startQueue = []queuedEvent{
		{raw: fmt.Sprintf("TBM:RUN|molten_core|running|%d/%d|lucifron|move|walk|fighting", next-1-5, total)},
		{raw: fmt.Sprintf("TBM:RUN|molten_core|running|%d/%d|lucifron|boss|fight|fighting", next-1, total)},
		{raw: fmt.Sprintf("TBM:RUN|molten_core|done|%d/%d|lucifron|boss|fight|stopped before step %d (until step %d): the party stays where it stands", next, total, next, next)},
	}
	f.agent.mu.Unlock()
	if code := f.run(context.Background()); code != exitOK {
		t.Fatalf("exit %d: %s\n%s", code, f.stderr.String(), f.stdout.String())
	}
	if n := countPrefix(f.agent.gmLines(), ".bot run stop"); n != 0 {
		t.Fatalf("%d run stops, want 0", n)
	}
	sum := f.summary()
	if sum.EndReason != "until" {
		t.Fatalf("summary end %q, want until", sum.EndReason)
	}
	if sum.BossKill == nil || !sum.BossKill.Killed {
		t.Fatalf("boss kill %+v, want killed by the run leader", sum.BossKill)
	}
}

// Five steps back, with the lead bot at the boss row, is still the party's own pull: the boss woke when this party
// pulled the pack next to it (walk 29c Mandokir, engaged at step 469 with the boss row at 474, the tank 41 yd from
// the boss row). Under boss-done the run leader fights it: no run stop, the run goes on to its until.
func TestRaidWalkBossDoneEarlyEngageFiveStepsBackLetsTheRunFightIt(t *testing.T) {
	_, next, _ := bossDoneFixture(t)
	f, _, _ := earlyEngageFixture(t, next-1-5, 10)
	if code := f.run(context.Background()); code != exitOK {
		t.Fatalf("exit %d: %s\n%s", code, f.stderr.String(), f.stdout.String())
	}
	if n := countPrefix(f.agent.gmLines(), ".bot run stop"); n != 0 {
		t.Fatalf("%d run stops, want 0: boss-done never stops the run on an engage of its boss", n)
	}
	if sum := f.summary(); sum.EndReason != "until" {
		t.Fatalf("summary end %q, want until", sum.EndReason)
	}
	if !hasNote(f.summary(), "before its boss step") {
		t.Fatalf("notes %q, want the early-engage note", f.summary().Notes)
	}
}

// A lead bot far from the boss row means the party is not at the pull at all: the hand-over does not fire and the
// walk stops the run as before (review round 1, item 3).
func TestRaidWalkBossDoneEarlyEngageLeadFarStops(t *testing.T) {
	_, next, _ := bossDoneFixture(t)
	f, _, _ := earlyEngageFixture(t, next-2, 1000)
	code := f.run(context.Background())
	if code != exitRaidWalkIncident {
		t.Fatalf("exit %d, want 19: %s", code, f.stderr.String())
	}
	if n := countPrefix(f.agent.gmLines(), ".bot run stop"); n != 1 {
		t.Fatalf("%d run stops, want exactly 1", n)
	}
	if hasNote(f.summary(), "before its boss step") {
		t.Fatalf("notes %q, want no early-engage note", f.summary().Notes)
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

// Another leg's boss woken from a trash step still stops: the same-leg rule keeps
// the hand-over to the boss's own leg (walk 19: tigers 11361 on Venoxis's leg).
func TestRaidWalkEarlyBossWakeOtherLegStillStops(t *testing.T) {
	// The same-leg rule: the status stands on a step whose NEXT boss step is in ANOTHER leg (the rows
	// below: the last step of a leg, then the next leg's boss), and the engaged entry belongs to that
	// boss. Dropping the same-leg check would let the hand-over fire from the earlier leg.
	f, next, total := bossDoneFixture(t)
	rows := mcRows(t)
	last, entryBoss := 0, uint32(0)
	for s := 2; s <= len(rows.Steps); s++ {
		if rows.Steps[s-1].Kind == planexport.KindBoss {
			continue
		}
		b := nextBossStep(rows, s)
		if b == 0 || rows.Steps[s-1].Leg == rows.Steps[b-1].Leg {
			continue
		}
		last, entryBoss = s, rows.Steps[b-1].Entry
		break
	}
	if last == 0 {
		t.Fatalf("the fixture rows hold no step before a boss of another leg")
	}
	f.opts.until = "step 9999" // past the boss' step, so the until alone would let the run fight
	f.agent.startReply = zgStartUntil(total, next, 9999)
	f.agent.mu.Lock()
	f.agent.startQueue = []queuedEvent{
		{raw: fmt.Sprintf("TBM:RUN|molten_core|running|%d/%d|lucifron|move|walk|walking|until step 9999 (step 9999)", last, total)},
	}
	f.agent.mu.Unlock()
	f.agent.hostilesFn = func(n int) []tbm.HostileRec {
		return []tbm.HostileRec{{GUID: 9, Entry: entryBoss, HPPct: 100, Target: packet.GUID(1000)}}
	}
	code := f.run(context.Background())
	if code != exitRaidWalkIncident {
		t.Fatalf("exit %d, want 19: stderr=%s stdout=%s", code, f.stderr.String(), f.stdout.String())
	}
	if n := countPrefix(f.agent.gmLines(), ".bot run stop"); n != 1 {
		t.Fatalf("%d run stops, want exactly 1", n)
	}
}

// The early hand-over needs the until to go PAST the boss step: --until step <the boss step>
// stops as before (the server stops BEFORE step N, so N must be > the boss step).
func TestRaidWalkEarlyEngageUntilAtTheBossStillStops(t *testing.T) {
	f, next, total := bossDoneFixture(t)
	boss := next - 1
	f.opts.until = fmt.Sprintf("step %d", boss)
	f.agent.startReply = &commanderReply{Reply: tbm.Reply{Kind: tbm.KindAck, OK: true,
		Ack:  &tbm.Ack{Intent: "run start", Scope: "party", Count: 1, Executor: "-"},
		Line: "TBM:ACTION_ACK|run start|party|1|-",
		Lines: []string{
			fmt.Sprintf("TBM:RUN|molten_core|running|1/%d|lucifron|move|walk|walking|until step %d (step %d)", total, boss, boss),
			"TBM:ACTION_ACK|run start|party|1|-",
		}}}
	f.agent.mu.Lock()
	f.agent.startQueue = []queuedEvent{
		{raw: fmt.Sprintf("TBM:RUN|molten_core|running|%d/%d|lucifron|move|walk|walking|until step %d (step %d)", boss-1, total, boss, boss)},
	}
	f.agent.mu.Unlock()
	f.agent.hostilesFn = func(n int) []tbm.HostileRec {
		return []tbm.HostileRec{{GUID: 9, Entry: 12118, HPPct: 100, Target: packet.GUID(1000)}}
	}
	code := f.run(context.Background())
	if code != exitRaidWalkIncident {
		t.Fatalf("exit %d, want 19: stderr=%s stdout=%s", code, f.stderr.String(), f.stdout.String())
	}
	if n := countPrefix(f.agent.gmLines(), ".bot run stop"); n != 1 {
		t.Fatalf("%d run stops, want exactly 1", n)
	}
}

// The early hand-over needs the engaged entry to belong to the plan boss of the next boss
// step: another leg's boss engaged there stops as before.
func TestRaidWalkEarlyEngageOtherBossStillStops(t *testing.T) {
	f, next, total := bossDoneFixture(t)
	boss := next - 1
	f.opts.until = "step 9999"
	f.agent.startReply = zgStartUntil(total, next, 9999)
	f.agent.mu.Lock()
	f.agent.startQueue = []queuedEvent{
		{raw: fmt.Sprintf("TBM:RUN|molten_core|running|%d/%d|lucifron|move|walk|walking|until step 9999 (step 9999)", boss-1, total)},
	}
	f.agent.mu.Unlock()
	f.agent.hostilesFn = func(n int) []tbm.HostileRec {
		return []tbm.HostileRec{{GUID: 9, Entry: 11982, X: 1143.14, Y: -1016.66, Z: -185.75, HPPct: 100, Target: packet.GUID(1000)}}
	}
	code := f.run(context.Background())
	if code != exitRaidWalkIncident {
		t.Fatalf("exit %d, want 19: stderr=%s stdout=%s gmlines=%q", code, f.stderr.String(), f.stdout.String(), f.agent.gmLines())
	}
	if n := countPrefix(f.agent.gmLines(), ".bot run stop"); n != 1 {
		t.Fatalf("%d run stops, want exactly 1", n)
	}
}

// Walk 24 (Thekal): the run is on the step BEFORE his boss step, in the same leg, its until
// (boss-done -> the step after the boss) goes past the boss step, and one of Thekal's `first`
// adds (11348 Zath) wakes the encounter. The run leader fights it: no run stop.
func TestRaidWalkThekalEarlyEngageFirstAddLetsTheRunFightIt(t *testing.T) {
	f := zgThekalFixture(t, "zul_gurub")
	boss := zgBossStep(t)
	total := len(zgRows(t).Steps)
	// The party stands at Thekal's spot with the first add that woke him (the hand-over needs the main tank at
	// the boss row).
	f.agent.pos = map[string][3]float32{"Bot01": {zgRows(t).Steps[boss-1].X, zgRows(t).Steps[boss-1].Y, zgRows(t).Steps[boss-1].Z}}
	f.agent.startReply = zgThekalStart(total, boss)
	f.agent.mu.Lock()
	f.agent.startQueue = []queuedEvent{
		{raw: fmt.Sprintf("TBM:RUN|zul_gurub|running|%d/%d|high_priest_thekal|move|walk|fighting", boss-1, total)},
		{raw: fmt.Sprintf("TBM:RUN|zul_gurub|running|%d/%d|high_priest_thekal|move|walk|fighting", boss-1, total)},
		{raw: fmt.Sprintf("TBM:RUN|zul_gurub|done|%d/%d|high_priest_thekal|move|walk|stopped before step %d (until step %d): the party stays where it stands", boss+1, total, boss+1, boss+1)},
	}
	f.agent.mu.Unlock()
	f.agent.hostilesFn = func(n int) []tbm.HostileRec {
		return []tbm.HostileRec{{GUID: 9, Entry: 11348, X: -11703.2, Y: -1996.8, Z: 62.1, HPPct: 100, Target: packet.GUID(1000)}}
	}
	if code := f.run(context.Background()); code != exitOK {
		t.Fatalf("exit %d: %s\n%s", code, f.stderr.String(), f.stdout.String())
	}
	if n := countPrefix(f.agent.gmLines(), ".bot run stop"); n != 0 {
		t.Fatalf("%d run stops, want 0: the run leader fights its boss's first add", n)
	}
	if !hasNote(f.summary(), "before its boss step") {
		t.Fatalf("notes %q", f.summary().Notes)
	}
}

// mcRows is the fixture's Molten Core rows through planexport.Build: the tests derive their step
// numbers from them, never hard-coding the plan's numbering.
func mcRows(t *testing.T) planexport.Rows {
	t.Helper()
	p, err := dungeon.Load("../../dungeons/molten_core.json")
	if err != nil {
		t.Fatal(err)
	}
	res, err := planexport.Build(p, planexport.Options{})
	if err != nil {
		t.Fatal(err)
	}
	return res.Rows
}

// zgRows is the fixture's Zul'Gurub rows through planexport.Build.
func zgRows(t *testing.T) planexport.Rows {
	t.Helper()
	p, err := dungeon.Load("../../dungeons/zul_gurub.json")
	if err != nil {
		t.Fatal(err)
	}
	res, err := planexport.Build(p, planexport.Options{})
	if err != nil {
		t.Fatal(err)
	}
	return res.Rows
}

// zgBossStep is the step of Zul'Gurub's Thekal boss row (the plan's boss entry 14599).
func zgBossStep(t *testing.T) int {
	t.Helper()
	for i, s := range zgRows(t).Steps {
		if s.Kind == planexport.KindBoss && s.Entry == 14599 {
			return i + 1
		}
	}
	t.Fatal("no Thekal boss row in the ZG rows")
	return 0
}

// zgThekalFixture is a Zul'Gurub walk with --until boss-done, as the ZG-placed tests do.
func zgThekalFixture(t *testing.T, plan string) *fixture {
	t.Helper()
	f := newFixture(t)
	f.opts.plan, f.opts.holder = plan, "raid-walk-"+plan
	f.opts.until = fmt.Sprintf("step %d", zgBossStep(t)+1) // the boss-done clause's step: past the boss
	p, err := dungeon.Load(filepath.Join(f.opts.dir, plan+".json"))
	if err != nil {
		t.Fatal(err)
	}
	f.agent.instMap = p.Map
	// The live commander stands in the instance by the pause (the enter phase put the raid there); without this the
	// M1 guard (corpseMoveReady) latches every walk-25 point_far test off the map before any move goes out.
	f.agent.enterMap = p.Map
	return f
}

// bossNotFoundLine is a paused status whose note is the run leader's "boss <N> not found" (walk 24 test2 r2):
// TB's phase-1 fake death leaves no 14599 corpse, so it pauses instead of marking the step done.
func bossNotFoundLine(plan string, step, total int, entry, next int) string {
	return fmt.Sprintf("TBM:RUN|%s|paused|%d/%d|lucifron|boss|begin|boss %d not found: not alive or dead within 30.0 yd of -11703.2 -1996.8 62.1 for 60 s nor anywhere in the instance (.bot run skip if he is already dead)|until step %d (step %d)",
		plan, step, total, entry, next, next)
}

// Walk 24 test2 r2 (revive r7): the boss fights (a live 60 % row: this walk's attempt start), dies, and
// the frames show his row at 0 % while the run leader never finds his corpse and pauses with
// "boss 12118 not found" on the boss's own step. The 0 % frame of the guid seen alive is the kill
// evidence: the walk sends `.bot run skip` once and the run goes on to its until (exit 0), instead of
// waiting out pausedWait and exiting 18.
func TestRaidWalkBossNotFoundAfterKillSkips(t *testing.T) {
	f, next, total := bossDoneFixture(t)
	boss := next - 1
	f.opts.pausedWait = 30 * time.Second
	f.agent.startReply = zgStart(total, next)
	// Fill some polls between the kill and the pause, so the boss's last sighting is more than 10 s old.
	var q []queuedEvent
	for range 12 {
		q = append(q, queuedEvent{raw: fmt.Sprintf("TBM:RUN|molten_core|running|%d/%d|lucifron|boss|begin|boss 12118 not at his spot: waiting for him|until step %d (step %d)", boss, total, next, next)})
	}
	q = append(q, queuedEvent{raw: bossNotFoundLine("molten_core", boss, total, 12118, next)})
	f.agent.mu.Lock()
	f.agent.startQueue = q
	f.agent.mu.Unlock()
	f.agent.hostilesFn = func(n int) []tbm.HostileRec {
		switch {
		case n == 1:
			return []tbm.HostileRec{{GUID: 9, Entry: 12118, HPPct: 60, Target: packet.GUID(1000)}} // the fight: this walk's attempt start
		case n == 2:
			return []tbm.HostileRec{{GUID: 9, Entry: 12118, HPPct: 0}} // the kill, seen alive first in this attempt
		}
		return nil
	}
	f.agent.onSkip = func() {
		f.agent.mu.Lock()
		f.agent.lines = append(f.agent.lines, queuedEvent{raw: fmt.Sprintf("TBM:RUN|molten_core|done|%d/%d|lucifron|move|walk|stopped before step %d (until step %d): the party stays where it stands", next, total, next, next)})
		f.agent.mu.Unlock()
	}
	code := f.run(context.Background())
	if code != exitOK {
		t.Fatalf("exit %d, want 0: %s\n%s", code, f.stderr.String(), f.stdout.String())
	}
	f.agent.mu.Lock()
	verbs := append([]string{}, f.agent.runVerbs...)
	f.agent.mu.Unlock()
	if countPrefix(verbs, "skip") != 1 {
		t.Fatalf("run verbs %v, want exactly one skip", verbs)
	}
	sum := f.summary()
	if sum.EndReason != "until" {
		t.Fatalf("summary end %q, want until", sum.EndReason)
	}
	if !hasNote(sum, "killed") {
		t.Fatalf("notes %q, want the kill note", sum.Notes)
	}
}

// Walk 24 test2 r2 (Zul'Gurub, Thekal's phase 2): 14599 60 -> 22 % then gone, 14509 (Thekal reborn) 99 -> 0 %
// then gone, hires alive, and the not-found pause on Thekal's boss row. The phase entry is the family evidence.
func TestRaidWalkBossNotFoundAfterPhaseKillSkips(t *testing.T) {
	f := zgThekalFixture(t, "zul_gurub")
	boss := zgBossStep(t)
	total := len(zgRows(t).Steps)
	f.opts.pausedWait = 30 * time.Second
	f.agent.startReply = zgThekalStart(total, boss)
	f.agent.mu.Lock()
	f.agent.startQueue = zgThekalPauseQueue(boss, total)
	f.agent.mu.Unlock()
	f.agent.hostilesFn = func(n int) []tbm.HostileRec {
		switch {
		case n <= 1:
			return []tbm.HostileRec{{GUID: 9, Entry: 14599, HPPct: 60, Target: packet.GUID(1000)}}
		case n <= 2:
			return []tbm.HostileRec{{GUID: 10, Entry: 14509, HPPct: 99, Target: packet.GUID(1000)}}
		case n <= 3:
			return []tbm.HostileRec{{GUID: 10, Entry: 14509, HPPct: 0, Target: packet.GUID(1000)}}
		}
		return nil
	}
	zgThekalDoneOnSkip(f, boss, total)
	if code := f.run(context.Background()); code != exitOK {
		t.Fatalf("exit %d, want 0: %s\n%s", code, f.stderr.String(), f.stdout.String())
	}
	f.agent.mu.Lock()
	verbs := append([]string{}, f.agent.runVerbs...)
	f.agent.mu.Unlock()
	if countPrefix(verbs, "skip") != 1 {
		t.Fatalf("run verbs %v, want exactly one skip", verbs)
	}
	if !hasNote(f.summary(), "killed") {
		t.Fatalf("notes %q, want the kill note", f.summary().Notes)
	}
}

// The boss evaded at 20 %: the frames' last sighting of him is low but not a corpse, and the toolkit sees no
// corpse of his entry near the boss row either. A stale low-hp sighting alone is never a kill (review round 1,
// item 1): no skip, the pause path is unchanged (wait, exit 18).
func TestRaidWalkBossNotFoundEvadeStillPauses(t *testing.T) {
	f, next, total := bossDoneFixture(t)
	boss := next - 1
	f.opts.pausedWait = 4 * time.Second
	f.agent.startReply = zgStart(total, next)
	f.agent.ttFn = ttUnitsFn(t)
	var q []queuedEvent
	for range 12 {
		q = append(q, queuedEvent{raw: fmt.Sprintf("TBM:RUN|molten_core|running|%d/%d|lucifron|boss|begin|boss 12118 not at his spot: waiting for him|until step %d (step %d)", boss, total, next, next)})
	}
	q = append(q, queuedEvent{raw: bossNotFoundLine("molten_core", boss, total, 12118, next)})
	f.agent.mu.Lock()
	f.agent.startQueue = q
	f.agent.mu.Unlock()
	f.agent.hostilesFn = func(n int) []tbm.HostileRec {
		if n <= 1 {
			return []tbm.HostileRec{{GUID: 9, Entry: 12118, HPPct: 20, Target: packet.GUID(1000)}}
		}
		return nil
	}
	code := f.run(context.Background())
	if code != exitRaidWalkFailed {
		t.Fatalf("exit %d, want 18: %s", code, f.stderr.String())
	}
	if !strings.Contains(f.summary().EndReason, "paused: boss 12118 not found") {
		t.Fatalf("end %q, want the pause", f.summary().EndReason)
	}
	f.agent.mu.Lock()
	verbs, tt := append([]string{}, f.agent.runVerbs...), append([]string{}, f.agent.ttCalls...)
	f.agent.mu.Unlock()
	if countPrefix(verbs, "skip") != 0 {
		t.Fatalf("run verbs %v, want no skip: the boss evaded at 20 %%", verbs)
	}
	if countPrefix(tt, "q units") == 0 {
		t.Fatalf("toolkit calls %q, want the corpse read", tt)
	}
}

// The party wiped: the boss's row is at 0 %, but every hire is dead, so the boss's absence is no kill.
func TestRaidWalkBossNotFoundAfterWipeStillPauses(t *testing.T) {
	f, next, total := bossDoneFixture(t)
	boss := next - 1
	f.opts.pausedWait = 4 * time.Second
	f.agent.startReply = zgStart(total, next)
	// The hires leave the frames once the run started (a wipe's aftermath without a running status line):
	// their last sighting ages out, so no hire is alive, and the tracker's wipe rule sees nobody.
	f.agent.eventsHook = func(raw string) {
		if strings.Contains(raw, "|running|") {
			f.agent.mu.Lock()
			f.agent.noPartyFrames = true
			f.agent.mu.Unlock()
		}
	}
	var q []queuedEvent
	for range 12 {
		q = append(q, queuedEvent{raw: fmt.Sprintf("TBM:RUN|molten_core|running|%d/%d|lucifron|boss|begin|boss 12118 not at his spot: waiting for him|until step %d (step %d)", boss, total, next, next)})
	}
	q = append(q, queuedEvent{raw: bossNotFoundLine("molten_core", boss, total, 12118, next)})
	f.agent.mu.Lock()
	f.agent.startQueue = q
	f.agent.mu.Unlock()
	f.agent.hostilesFn = func(n int) []tbm.HostileRec {
		switch {
		case n == 1:
			return []tbm.HostileRec{{GUID: 9, Entry: 12118, HPPct: 60, Target: packet.GUID(1000)}} // the fight: this walk's attempt start
		case n == 2:
			return []tbm.HostileRec{{GUID: 9, Entry: 12118, HPPct: 0}} // the kill, seen alive first in this attempt
		}
		return nil
	}
	code := f.run(context.Background())
	if code != exitRaidWalkFailed {
		t.Fatalf("exit %d, want 18: %s", code, f.stderr.String())
	}
	f.agent.mu.Lock()
	verbs := append([]string{}, f.agent.runVerbs...)
	f.agent.mu.Unlock()
	if countPrefix(verbs, "skip") != 0 {
		t.Fatalf("run verbs %v, want no skip: the party wiped", verbs)
	}
}

// The kill evidence holds but the run leader refuses the skip: the send is retried once (review round 1, item 2),
// then the walk waits as a pause (exit 18).
func TestRaidWalkBossNotFoundSkipRefusedStillPauses(t *testing.T) {
	f, next, total := bossDoneFixture(t)
	boss := next - 1
	f.opts.pausedWait = 4 * time.Second
	f.agent.startReply = zgStart(total, next)
	var q []queuedEvent
	for range 12 {
		q = append(q, queuedEvent{raw: fmt.Sprintf("TBM:RUN|molten_core|running|%d/%d|lucifron|boss|begin|boss 12118 not at his spot: waiting for him|until step %d (step %d)", boss, total, next, next)})
	}
	q = append(q, queuedEvent{raw: bossNotFoundLine("molten_core", boss, total, 12118, next)})
	f.agent.mu.Lock()
	f.agent.startQueue = q
	f.agent.mu.Unlock()
	f.agent.skipErr = true
	f.agent.hostilesFn = func(n int) []tbm.HostileRec {
		switch {
		case n == 1:
			return []tbm.HostileRec{{GUID: 9, Entry: 12118, HPPct: 60, Target: packet.GUID(1000)}} // the fight: this walk's attempt start
		case n == 2:
			return []tbm.HostileRec{{GUID: 9, Entry: 12118, HPPct: 0}} // the kill, seen alive first in this attempt
		}
		return nil
	}
	code := f.run(context.Background())
	if code != exitRaidWalkFailed {
		t.Fatalf("exit %d, want 18: %s", code, f.stderr.String())
	}
	if !hasNote(f.summary(), "run skip failed") {
		t.Fatalf("notes %q, want the refused-skip note", f.summary().Notes)
	}
	f.agent.mu.Lock()
	verbs := append([]string{}, f.agent.runVerbs...)
	f.agent.mu.Unlock()
	if countPrefix(verbs, "skip") != 2 {
		t.Fatalf("run verbs %v, want exactly two skips (the send and its one retry)", verbs)
	}
}

// zgBossNotFoundLine is the paused status of a Zul'Gurub Thekal walk: the run leader's "boss 14599 not found" on
// Thekal's own boss row (walk 24 test2 r2: TB's phase-1 fake death leaves no 14599 corpse).
func zgBossNotFoundLine(boss, total, next int) string {
	return fmt.Sprintf("TBM:RUN|zul_gurub|paused|%d/%d|high_priest_thekal|boss|begin|boss 14599 not found: not alive or dead within 30.0 yd of -11703.2 -1996.8 62.1 for 60 s nor anywhere in the instance (.bot run skip if he is already dead)|until step %d (step %d)",
		boss, total, next, next)
}

// zgThekalPauseQueue is the event queue of a Thekal walk that ran up to his boss step and paused there: 12 running
// lines first, so the boss's last sighting in the frames is older than 15 s when the pause arrives.
func zgThekalPauseQueue(boss, total int) []queuedEvent {
	var q []queuedEvent
	for range 12 {
		q = append(q, queuedEvent{raw: fmt.Sprintf("TBM:RUN|zul_gurub|running|%d/%d|high_priest_thekal|boss|begin|boss 14599 not at his spot: waiting for him|until step %d (step %d)", boss, total, boss+1, boss+1)})
	}
	return append(q, queuedEvent{raw: zgBossNotFoundLine(boss, total, boss+1)})
}

// zgThekalDoneOnSkip appends the run's ending after the walk's skip: the run stopped before its until step.
func zgThekalDoneOnSkip(f *fixture, boss, total int) {
	f.agent.onSkip = func() {
		f.agent.mu.Lock()
		f.agent.lines = append(f.agent.lines, queuedEvent{raw: fmt.Sprintf("TBM:RUN|zul_gurub|done|%d/%d|high_priest_thekal|move|walk|stopped before step %d (until step %d): the party stays where it stands", boss+1, total, boss+1, boss+1)})
		f.agent.mu.Unlock()
	}
}

// ttUnitsFn is a toolkit answer carrying the given units: the corpse reads of these tests (`q units ... dead e=`).
func ttUnitsFn(t *testing.T, units ...map[string]any) func(cmd string) (ttResult, error) {
	t.Helper()
	return func(cmd string) (ttResult, error) {
		return ttReplyOf(t, map[string]any{"units": units}), nil
	}
}

// The 15068 Zulian Guardians are an add that exists several at once (the encounter summons two, boss_thekal.cpp
// :41/:413), not the boss reborn: a 15068 corpse is no kill evidence, even with 14509 (the reborn Thekal) last
// seen at 60 % and nothing of 14599 left in the frames. Per-unit judging must not read the live boss's row off the
// add's corpse (review round 1, item 1): no skip, exit 18.
func TestRaidWalkBossNotFoundAddCorpseStillPauses(t *testing.T) {
	f := zgThekalFixture(t, "zul_gurub")
	boss := zgBossStep(t)
	total := len(zgRows(t).Steps)
	f.opts.pausedWait = 4 * time.Second
	f.agent.startReply = zgThekalStart(total, boss)
	f.agent.ttFn = ttUnitsFn(t) // no corpse of a boss unit at the boss row
	f.agent.mu.Lock()
	f.agent.startQueue = zgThekalPauseQueue(boss, total)
	f.agent.mu.Unlock()
	f.agent.hostilesFn = func(n int) []tbm.HostileRec {
		switch {
		case n <= 1:
			// The two guardians stand up together: 15068 is an add, and its corpses prove nothing.
			return []tbm.HostileRec{
				{GUID: 41, Entry: 15068, HPPct: 90, Target: packet.GUID(1000)},
				{GUID: 42, Entry: 15068, HPPct: 80, Target: packet.GUID(1000)},
			}
		case n <= 2:
			return []tbm.HostileRec{{GUID: 10, Entry: 14509, HPPct: 60, Target: packet.GUID(1000)}}
		case n <= 3:
			return []tbm.HostileRec{{GUID: 41, Entry: 15068, HPPct: 0}}
		}
		return nil
	}
	code := f.run(context.Background())
	if code != exitRaidWalkFailed {
		t.Fatalf("exit %d, want 18: %s\n%s", code, f.stderr.String(), f.stdout.String())
	}
	f.agent.mu.Lock()
	verbs := append([]string{}, f.agent.runVerbs...)
	f.agent.mu.Unlock()
	if countPrefix(verbs, "skip") != 0 {
		t.Fatalf("run verbs %v, want no skip: a guardian's corpse is no boss kill", verbs)
	}
}

// TB's own boss step reads the live view only: a boss killed while the party was dead or away leaves no 0 % row in
// the frames at all. The toolkit's `q units ... all dead e=` reads the map's creatures, corpses included, so a dead
// 12118 within 40 yd of the boss row is the kill evidence (review round 1, item 1) — once the r6 rule holds:
// the corpse's guid was seen alive in this attempt and a second read 15 s later still shows it dead.
func TestRaidWalkBossNotFoundToolkitCorpseSkips(t *testing.T) {
	f, next, total := bossDoneFixture(t)
	boss := next - 1
	f.opts.pausedWait = 60 * time.Second
	f.agent.startReply = zgStart(total, next)
	f.agent.ttFn = ttUnitsFn(t, map[string]any{"g": "0xF130002F56000009", "e": 12118, "dead": true, "d": 12.5, "hp": 0})
	var q []queuedEvent
	for range 24 {
		q = append(q, queuedEvent{raw: fmt.Sprintf("TBM:RUN|molten_core|running|%d/%d|lucifron|boss|begin|boss 12118 not at his spot: waiting for him|until step %d (step %d)", boss, total, next, next)})
	}
	q = append(q, queuedEvent{raw: bossNotFoundLine("molten_core", boss, total, 12118, next)})
	f.agent.mu.Lock()
	f.agent.startQueue = q
	f.agent.mu.Unlock()
	f.agent.hostilesFn = func(n int) []tbm.HostileRec {
		if n <= 1 {
			// GUID 9 with the toolkit corpse's full unit guid: the frames saw this unit alive
			// in this attempt, and the toolkit reports his corpse, 15 s apart.
			return []tbm.HostileRec{{GUID: 0xF130002F56000009, Entry: 12118, HPPct: 60, Target: packet.GUID(1000)}}
		}
		return nil
	}
	f.agent.onSkip = func() {
		f.agent.mu.Lock()
		f.agent.lines = append(f.agent.lines, queuedEvent{raw: fmt.Sprintf("TBM:RUN|molten_core|done|%d/%d|lucifron|move|walk|stopped before step %d (until step %d): the party stays where it stands", next, total, next, next)})
		f.agent.mu.Unlock()
	}
	if code := f.run(context.Background()); code != exitOK {
		t.Fatalf("exit %d, want 0: %s\n%s", code, f.stderr.String(), f.stdout.String())
	}
	f.agent.mu.Lock()
	verbs, tt := append([]string{}, f.agent.runVerbs...), append([]string{}, f.agent.ttCalls...)
	f.agent.mu.Unlock()
	if countPrefix(verbs, "skip") != 1 {
		t.Fatalf("run verbs %v, want exactly one skip", verbs)
	}
	if !hasNote(f.summary(), "the toolkit sees 12118 dead") {
		t.Fatalf("notes %q, want the toolkit's corpse evidence", f.summary().Notes)
	}
	if len(tt) < 2 || !strings.Contains(tt[0], "all dead e=12118") {
		t.Fatalf("toolkit calls %q, want the corpse read of 12118 twice (the 15 s confirmation)", tt)
	}
}

// Revive r6: the first toolkit corpse sighting alone is no evidence yet (the kill needs the second
// read 15 s later). A pause shorter than the wait never skips on the toolkit's word alone.
func TestRaidWalkBossNotFoundToolkitCorpseNeedsConfirmation(t *testing.T) {
	f, next, total := bossDoneFixture(t)
	boss := next - 1
	f.opts.pausedWait = 4 * time.Second
	f.agent.startReply = zgStart(total, next)
	f.agent.ttFn = ttUnitsFn(t, map[string]any{"g": "0xF130002F56000009", "e": 12118, "dead": true, "d": 12.5, "hp": 0})
	var q []queuedEvent
	for range 12 {
		q = append(q, queuedEvent{raw: fmt.Sprintf("TBM:RUN|molten_core|running|%d/%d|lucifron|boss|begin|boss 12118 not at his spot: waiting for him|until step %d (step %d)", boss, total, next, next)})
	}
	q = append(q, queuedEvent{raw: bossNotFoundLine("molten_core", boss, total, 12118, next)})
	f.agent.mu.Lock()
	f.agent.startQueue = q
	f.agent.mu.Unlock()
	f.agent.hostilesFn = func(n int) []tbm.HostileRec {
		if n <= 1 {
			return []tbm.HostileRec{{GUID: 0xF130002F56000009, Entry: 12118, HPPct: 60, Target: packet.GUID(1000)}}
		}
		return nil
	}
	code := f.run(context.Background())
	if code != exitRaidWalkFailed {
		t.Fatalf("exit %d, want 18: %s", code, f.stderr.String())
	}
	f.agent.mu.Lock()
	verbs := append([]string{}, f.agent.runVerbs...)
	f.agent.mu.Unlock()
	if countPrefix(verbs, "skip") != 0 {
		t.Fatalf("run verbs %v, want no skip: the corpse wait never confirmed within the pause", verbs)
	}
}

// Walk 25 F3: the toolkit's corpse read is range-limited to 150 yd, so a read from far away is refused with
// point_far. The walk moves the commander to the boss row once (with the revive's own GM sequence), re-reads the
// same entry once, and finds the corpse: exactly one `.go xyz` to the boss spot between the two reads, one
// `.bot run skip`, and the move note. Today: no move, latched, no skip.
// Codex round 1: a .go to the boss row that fails is not a move: no move note, no second read, and the reads latch
// off with the real error.
func TestRaidWalkBossNotFoundCorpseReadPointFarGoFails(t *testing.T) {
	f := zgThekalFixture(t, "zul_gurub")
	boss := zgBossStep(t)
	total := len(zgRows(t).Steps)
	f.opts.pausedWait = 30 * time.Second
	f.agent.startReply = zgThekalStart(total, boss)
	spot := zgRows(t).Steps[boss-1]
	wantGo := fmt.Sprintf(".go xyz %s %s %s 309", num32(spot.X), num32(spot.Y), num32(spot.Z+2))
	f.agent.ttFn = func(cmd string) (ttResult, error) {
		return ttResult{}, errors.New("rpc error -32003: eluna: point_far: point is 258 yd from you (max 150): .go xyz there first")
	}
	f.agent.gmFail = func(line string) error {
		if line == wantGo {
			return errors.New("command refused")
		}
		return nil
	}
	f.agent.mu.Lock()
	f.agent.startQueue = zgThekalPauseQueue(boss, total)
	f.agent.mu.Unlock()
	f.agent.hostilesFn = func(n int) []tbm.HostileRec {
		if n <= 1 {
			return []tbm.HostileRec{{GUID: 9, Entry: 14599, HPPct: 60, Target: packet.GUID(1000)}}
		}
		return nil
	}
	f.run(context.Background())
	f.agent.mu.Lock()
	tt := append([]string{}, f.agent.ttCalls...)
	f.agent.mu.Unlock()
	if len(tt) != 1 {
		t.Fatalf("toolkit calls %q, want exactly the one refused read (no re-read after a failed go)", tt)
	}
	if hasNote(f.summary(), "the commander moved to the boss row for the corpse read") {
		t.Fatalf("notes %q, want no move note after a failed go", f.summary().Notes)
	}
	if !hasNote(f.summary(), "the move to the boss row failed") {
		t.Fatalf("notes %q, want the failed-move note", f.summary().Notes)
	}
}

// Walk 25 point_far round 4 (Codex MEDIUM): the status read before the boss-row move fails. The failure latches
// the reads off with the real error: the walk waits out the pause with exactly one toolkit read in total, instead
// of repeating the corpse read and the failing status RPC on every pause tick.
func TestRaidWalkBossNotFoundCorpseReadPointFarStatusFails(t *testing.T) {
	f := zgThekalFixture(t, "zul_gurub")
	boss := zgBossStep(t)
	total := len(zgRows(t).Steps)
	f.opts.pausedWait = 30 * time.Second
	f.agent.startReply = zgThekalStart(total, boss)
	f.agent.mu.Lock()
	f.agent.startQueue = zgThekalPauseQueue(boss, total)
	f.agent.mu.Unlock()
	f.agent.ttFn = func(cmd string) (ttResult, error) {
		// Arm the failure only once the corpse read is under way: the login and watch phases need a working status.
		f.agent.mu.Lock()
		f.agent.statusFailNext = 100 // every status RPC fails from here on, on every pause tick
		f.agent.mu.Unlock()
		return ttResult{}, errors.New("rpc error -32003: eluna: point_far: point is 258 yd from you (max 150): .go xyz there first")
	}
	f.agent.hostilesFn = func(n int) []tbm.HostileRec {
		if n <= 1 {
			return []tbm.HostileRec{{GUID: 9, Entry: 14599, HPPct: 60, Target: packet.GUID(1000)}}
		}
		return nil
	}
	if code := f.run(context.Background()); code != exitRaidWalkFailed {
		t.Fatalf("exit %d, want 18: %s\n%s", code, f.stderr.String(), f.stdout.String())
	}
	f.agent.mu.Lock()
	tt := append([]string{}, f.agent.ttCalls...)
	f.agent.mu.Unlock()
	if len(tt) != 1 {
		t.Fatalf("toolkit calls %q, want exactly the one refused read (the status failure latches the reads off)", tt)
	}
	if hasNote(f.summary(), "the commander moved to the boss row for the corpse read") {
		t.Fatalf("notes %q, want no move note after a failed status read", f.summary().Notes)
	}
	if !hasNote(f.summary(), "the toolkit corpse read failed") || !hasNote(f.summary(), "agent busy") {
		t.Fatalf("notes %q, want the latch note carrying the real status error", f.summary().Notes)
	}
}

// Walk 25 point_far round 4 (Codex MEDIUM): the `.gm visible off` before the boss-row move fails. The failure
// latches the reads off with the real error: the walk waits out the pause with exactly one toolkit read in total,
// instead of repeating the corpse read and the failing GM RPC on every pause tick.
func TestRaidWalkBossNotFoundCorpseReadPointFarVisibleOffFails(t *testing.T) {
	f := zgThekalFixture(t, "zul_gurub")
	boss := zgBossStep(t)
	total := len(zgRows(t).Steps)
	f.opts.pausedWait = 30 * time.Second
	f.agent.startReply = zgThekalStart(total, boss)
	f.agent.mu.Lock()
	f.agent.startQueue = zgThekalPauseQueue(boss, total)
	f.agent.mu.Unlock()
	f.agent.ttFn = func(cmd string) (ttResult, error) {
		return ttResult{}, errors.New("rpc error -32003: eluna: point_far: point is 258 yd from you (max 150): .go xyz there first")
	}
	f.agent.gmFail = func(line string) error {
		if line == raidwalk.GMVisibleOff {
			return errors.New("command refused")
		}
		return nil
	}
	f.agent.hostilesFn = func(n int) []tbm.HostileRec {
		if n <= 1 {
			return []tbm.HostileRec{{GUID: 9, Entry: 14599, HPPct: 60, Target: packet.GUID(1000)}}
		}
		return nil
	}
	if code := f.run(context.Background()); code != exitRaidWalkFailed {
		t.Fatalf("exit %d, want 18: %s\n%s", code, f.stderr.String(), f.stdout.String())
	}
	f.agent.mu.Lock()
	tt := append([]string{}, f.agent.ttCalls...)
	f.agent.mu.Unlock()
	if len(tt) != 1 {
		t.Fatalf("toolkit calls %q, want exactly the one refused read (the visible-off failure latches the reads off)", tt)
	}
	if hasNote(f.summary(), "the commander moved to the boss row for the corpse read") {
		t.Fatalf("notes %q, want no move note after a failed visible-off", f.summary().Notes)
	}
	if !hasNote(f.summary(), "the toolkit corpse read failed") || !hasNote(f.summary(), "command refused") {
		t.Fatalf("notes %q, want the latch note carrying the real visible-off error", f.summary().Notes)
	}
}

// Walk 25 point_far round 4 (Codex MEDIUM): a cancelled context is no toolkit failure. The status read inside the
// point_far branch answers while the walk itself is interrupted (the fake cancels the context on that read), so
// bossCorpse returns without latching, like today's code. M2 round 5: the old shape cancelled before bossCorpse ran
// and never reached the status-cancel guard.
func TestRaidWalkBossNotFoundCorpseReadPointFarStatusCancelDoesNotLatch(t *testing.T) {
	f := zgThekalFixture(t, "zul_gurub")
	rows := zgRows(t).Steps
	// The guard needs the commander's map (see corpseMoveReady): stand in the instance like MovesAndRereads does.
	f.agent.status.Map = 309
	w := &walk{plan: &dungeon.Plan{Name: "zul_gurub", Map: 309}, agent: f.agent, rows: planexport.Rows{Steps: rows},
		env: walkEnv{now: f.clock.now, sleep: f.clock.sleep}, hires: map[string]bool{}}
	f.agent.ttFn = func(cmd string) (ttResult, error) {
		return ttResult{}, errors.New("rpc error -32003: eluna: point_far: point is 258 yd from you (max 150): .go xyz there first")
	}
	f.agent.statusFailNext = 1 // the point_far branch's status read fails
	ctx, cancel := context.WithCancel(context.Background())
	// Cancel inside the status handler: the status read answers while the walk is already interrupted.
	f.agent.cancelStatusAt, f.agent.cancelStatusCtx = 1, cancel
	if ok, _ := w.bossCorpse(ctx, 14599, []uint32{14599}, w.env.now()); ok {
		t.Fatalf("bossCorpse = true, want no evidence on a cancelled read")
	}
	if w.bossToolkitOff {
		t.Fatalf("bossToolkitOff latched, want no latch on a cancelled status read")
	}
	f.agent.cancelStatusAt, f.agent.cancelStatusCtx = 0, nil
}

// Walk 25 point_far M2 round 5: a cancel before the go returns without latching: the go and the landing wait run
// under an interrupted walk, so there is no verdict and no latch.
func TestRaidWalkBossNotFoundCorpseReadPointFarGoCancelDoesNotLatch(t *testing.T) {
	f := zgThekalFixture(t, "zul_gurub")
	rows := zgRows(t).Steps
	// The guard needs the commander's map (see corpseMoveReady): stand in the instance like MovesAndRereads does.
	f.agent.status.Map = 309
	w := &walk{plan: &dungeon.Plan{Name: "zul_gurub", Map: 309}, agent: f.agent, rows: planexport.Rows{Steps: rows},
		env: walkEnv{now: f.clock.now, sleep: f.clock.sleep}, hires: map[string]bool{}}
	f.agent.ttFn = func(cmd string) (ttResult, error) {
		return ttResult{}, errors.New("rpc error -32003: eluna: point_far: point is 258 yd from you (max 150): .go xyz there first")
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel() // the go and the landing wait run under an interrupted walk: no verdict, no latch
	if ok, _ := w.bossCorpse(ctx, 14599, []uint32{14599}, w.env.now()); ok {
		t.Fatalf("bossCorpse = true, want no evidence on a cancelled go")
	}
	if w.bossToolkitOff {
		t.Fatalf("bossToolkitOff latched, want no latch on a cancelled go")
	}
}

// Walk 25 point_far M2 round 5: a cancel during the landing wait returns without latching: the go went out but the
// wait sees the interrupted walk first.
func TestRaidWalkBossNotFoundCorpseReadPointFarLandingCancelDoesNotLatch(t *testing.T) {
	f := zgThekalFixture(t, "zul_gurub")
	rows := zgRows(t).Steps
	spot := zgRows(t).Steps[zgBossStep(t)-1]
	// The guard needs the commander's map: stand in the instance, away from the boss row so the wait polls.
	f.agent.status.Map = 309
	f.agent.goLagEvery = 1 << 30 // the go never lands: the wait polls until ctx ends it
	w := &walk{plan: &dungeon.Plan{Name: "zul_gurub", Map: 309}, agent: f.agent, rows: planexport.Rows{Steps: rows},
		env: walkEnv{now: f.clock.now, sleep: f.clock.sleep}, hires: map[string]bool{}}
	f.agent.ttFn = func(cmd string) (ttResult, error) {
		return ttResult{}, errors.New("rpc error -32003: eluna: point_far: point is 258 yd from you (max 150): .go xyz there first")
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel() // the landing wait runs under an interrupted walk: no verdict, no latch
	_ = spot
	if ok, _ := w.bossCorpse(ctx, 14599, []uint32{14599}, w.env.now()); ok {
		t.Fatalf("bossCorpse = true, want no evidence on a cancelled landing wait")
	}
	if w.bossToolkitOff {
		t.Fatalf("bossToolkitOff latched, want no latch on a cancelled landing wait")
	}
	f.agent.goLagEvery = 0
}

// Walk 25 point_far M1 round 5: the commander stands at the portal (off the plan's map), where a `.go xyz ... 309`
// would open a NEW copy instead of the hires' one. No move goes out; the reads latch with the not-on-map note.
func TestRaidWalkBossNotFoundCorpseReadPointFarCommanderOffMapDoesNotMove(t *testing.T) {
	f := zgThekalFixture(t, "zul_gurub")
	rows := zgRows(t).Steps
	// Off the plan's map (the fake's spawn map 1, like a commander still at the portal).
	f.agent.status.Map = 1
	w := &walk{plan: &dungeon.Plan{Name: "zul_gurub", Map: 309}, agent: f.agent, rows: planexport.Rows{Steps: rows},
		env: walkEnv{now: f.clock.now, sleep: f.clock.sleep}, hires: map[string]bool{}}
	f.agent.ttFn = func(cmd string) (ttResult, error) {
		return ttResult{}, errors.New("rpc error -32003: eluna: point_far: point is 258 yd from you (max 150): .go xyz there first")
	}
	if ok, _ := w.bossCorpse(context.Background(), 14599, []uint32{14599}, w.env.now()); ok {
		t.Fatalf("bossCorpse = true, want no evidence off the map")
	}
	if !w.bossToolkitOff {
		t.Fatalf("bossToolkitOff = false, want the latch on the off-map commander")
	}
	f.agent.mu.Lock()
	gm, tt := append([]string{}, f.agent.gm...), append([]string{}, f.agent.ttCalls...)
	f.agent.mu.Unlock()
	for _, line := range gm {
		if strings.HasPrefix(line, ".go xyz") {
			t.Fatalf("gm lines %q, want no .go xyz from off the map", gm)
		}
	}
	if len(tt) != 1 {
		t.Fatalf("toolkit calls %q, want exactly the refused read (no re-read after the latch)", tt)
	}
}

// Walk 25 point_far M1 round 5, placed walk: the commander's instance moved (status's read agrees it is on the plan's
// map, but the toolkit copy differs from w.placeInst, as placeStillJoined compares). No move goes out; the reads
// latch with the instance note.
func TestRaidWalkBossNotFoundCorpseReadPointFarPlacedInstanceMovesDoesNotMove(t *testing.T) {
	f := zgThekalFixture(t, "zul_gurub")
	rows := zgRows(t).Steps
	f.agent.status.Map = 309
	w := &walk{plan: &dungeon.Plan{Name: "zul_gurub", Map: 309}, agent: f.agent, rows: planexport.Rows{Steps: rows},
		env: walkEnv{now: f.clock.now, sleep: f.clock.sleep}, hires: map[string]bool{},
		place: &raidwalk.PlacePlan{Start: 1}, placeInst: 101}
	f.agent.ttFn = func(cmd string) (ttResult, error) {
		if strings.HasPrefix(cmd, "q inst") {
			return ttReplyOf(t, map[string]any{"map": 309, "inst": 104}), nil
		}
		return ttResult{}, errors.New("rpc error -32003: eluna: point_far: point is 258 yd from you (max 150): .go xyz there first")
	}
	if ok, _ := w.bossCorpse(context.Background(), 14599, []uint32{14599}, w.env.now()); ok {
		t.Fatalf("bossCorpse = true, want no evidence in another copy")
	}
	if !w.bossToolkitOff {
		t.Fatalf("bossToolkitOff = false, want the latch on the moved copy")
	}
	f.agent.mu.Lock()
	gm, tt := append([]string{}, f.agent.gm...), append([]string{}, f.agent.ttCalls...)
	f.agent.mu.Unlock()
	for _, line := range gm {
		if strings.HasPrefix(line, ".go xyz") {
			t.Fatalf("gm lines %q, want no .go xyz from another copy", gm)
		}
	}
	if len(tt) == 0 || !strings.Contains(tt[0], "all dead e=14599") {
		t.Fatalf("toolkit calls %q, want the refused corpse read first", tt)
	}
}

// Walk 25 point_far L4 round 5: the re-read after the move is refused again -> the re-read latches the reads off.
// The move goes out once (the move note), the re-read fails, and the latch note carries it; the walk waits out the
// pause with no skip.
func TestRaidWalkBossNotFoundCorpseReadPointFarRereadRefusedLatches(t *testing.T) {
	f := zgThekalFixture(t, "zul_gurub")
	boss := zgBossStep(t)
	total := len(zgRows(t).Steps)
	f.opts.pausedWait = 30 * time.Second
	f.agent.startReply = zgThekalStart(total, boss)
	f.agent.mu.Lock()
	f.agent.startQueue = zgThekalPauseQueue(boss, total)
	f.agent.mu.Unlock()
	f.agent.ttFn = func(cmd string) (ttResult, error) {
		return ttResult{}, errors.New("rpc error -32003: eluna: point_far: point is 258 yd from you (max 150): .go xyz there first")
	}
	f.agent.hostilesFn = func(n int) []tbm.HostileRec {
		if n <= 1 {
			return []tbm.HostileRec{{GUID: 9, Entry: 14599, HPPct: 60, Target: packet.GUID(1000)}}
		}
		return nil
	}
	if code := f.run(context.Background()); code != exitRaidWalkFailed {
		t.Fatalf("exit %d, want 18: %s\n%s", code, f.stderr.String(), f.stdout.String())
	}
	f.agent.mu.Lock()
	tt := append([]string{}, f.agent.ttCalls...)
	f.agent.mu.Unlock()
	// The move went out once and the re-read failed: the refused read plus its re-read, then the latch.
	if len(tt) != 2 {
		t.Fatalf("toolkit calls %q, want the refused read and its re-read (the re-read latches the reads off)", tt)
	}
	if !hasNote(f.summary(), "the commander moved to the boss row for the corpse read") {
		t.Fatalf("notes %q, want the move note (the move landed before the re-read)", f.summary().Notes)
	}
	if !hasNote(f.summary(), "the toolkit corpse read failed") {
		t.Fatalf("notes %q, want the latch note carrying the re-read failure", f.summary().Notes)
	}
	if n := countPrefix(f.agent.runVerbs, "skip"); n != 0 {
		t.Fatalf("run verbs %v, want no skip: a twice-refused read is no evidence", f.agent.runVerbs)
	}
}

// Walk 25 point_far L4 round 5: the re-read after the move shows no corpse -> no latch, and the next tick does not
// move again. Exactly one `.go xyz ... 309` goes out for the whole pause (the portal go does not count), and no skip
// follows a clean re-read.
func TestRaidWalkBossNotFoundCorpseReadPointFarRereadCleanMovesOnce(t *testing.T) {
	f := zgThekalFixture(t, "zul_gurub")
	boss := zgBossStep(t)
	total := len(zgRows(t).Steps)
	f.opts.pausedWait = 30 * time.Second
	f.agent.startReply = zgThekalStart(total, boss)
	f.agent.mu.Lock()
	f.agent.startQueue = zgThekalPauseQueue(boss, total)
	f.agent.mu.Unlock()
	spot := zgRows(t).Steps[boss-1]
	refused := true
	f.agent.ttFn = func(cmd string) (ttResult, error) {
		if strings.Contains(cmd, "all dead e=14599") && refused {
			// Only the FIRST read is refused (the commander stands too far away); once the move lands, the
			// re-read and later ticks read cleanly. The fake answers from the commander's position like the
			// landing-wait test does.
			f.agent.mu.Lock()
			x, y := f.agent.status.X, f.agent.status.Y
			f.agent.mu.Unlock()
			if x != spot.X || y != spot.Y {
				return ttResult{}, errors.New("rpc error -32003: eluna: point_far: point is 258 yd from you (max 150): .go xyz there first")
			}
			refused = false
		}
		return ttReplyOf(t, map[string]any{"units": []any{}}), nil
	}
	f.agent.hostilesFn = func(n int) []tbm.HostileRec {
		if n <= 1 {
			return []tbm.HostileRec{{GUID: 9, Entry: 14599, HPPct: 60, Target: packet.GUID(1000)}}
		}
		return nil
	}
	if code := f.run(context.Background()); code != exitRaidWalkFailed {
		t.Fatalf("exit %d, want 18: %s\n%s", code, f.stderr.String(), f.stdout.String())
	}
	wantGo := fmt.Sprintf(".go xyz %s %s %s 309", num32(spot.X), num32(spot.Y), num32(spot.Z+2))
	f.agent.mu.Lock()
	gm, tt := append([]string{}, f.agent.gm...), append([]string{}, f.agent.ttCalls...)
	f.agent.mu.Unlock()
	gos := 0
	for _, line := range gm {
		if line == wantGo {
			gos++
		}
	}
	// One move for the whole pause: the move note's go, and no second move on later ticks.
	if gos != 1 {
		t.Fatalf("gm lines %q, want exactly one %q for the whole pause", gm, wantGo)
	}
	// The re-read went out (and later ticks re-read cleanly without moving again); no latch either.
	if len(tt) < 2 {
		t.Fatalf("toolkit calls %q, want the refused read and at least its clean re-read", tt)
	}
	if hasNote(f.summary(), "the toolkit corpse read failed") {
		t.Fatalf("notes %q, want no latch on a clean re-read", f.summary().Notes)
	}
	if verbs := f.agent.runVerbs; countPrefix(verbs, "skip") != 0 {
		t.Fatalf("run verbs %v, want no skip: a clean re-read is no evidence", verbs)
	}
}

// Walk 25 point_far L4 round 5: `.gm visible off` is sent before the go. The send log shows gm visible-off, then the
// go, then the re-read.
func TestRaidWalkBossNotFoundCorpseReadPointFarVisibleOffBeforeGo(t *testing.T) {
	f := zgThekalFixture(t, "zul_gurub")
	rows := zgRows(t).Steps
	spot := zgRows(t).Steps[zgBossStep(t)-1]
	f.agent.status.Map = 309
	w := &walk{plan: &dungeon.Plan{Name: "zul_gurub", Map: 309}, agent: f.agent, rows: planexport.Rows{Steps: rows},
		env: walkEnv{now: f.clock.now, sleep: f.clock.sleep}, hires: map[string]bool{}}
	calls := 0
	f.agent.ttFn = func(cmd string) (ttResult, error) {
		calls++
		if calls == 1 {
			return ttResult{}, errors.New("rpc error -32003: eluna: point_far: point is 258 yd from you (max 150): .go xyz there first")
		}
		return ttReplyOf(t, map[string]any{"units": []any{}}), nil
	}
	if ok, _ := w.bossCorpse(context.Background(), 14599, []uint32{14599}, w.env.now()); ok {
		t.Fatalf("bossCorpse = true, want no evidence from a clean re-read")
	}
	if w.bossToolkitOff {
		t.Fatalf("bossToolkitOff latched, want no latch on a clean re-read")
	}
	f.agent.mu.Lock()
	log := append([]string{}, f.agent.log...)
	f.agent.mu.Unlock()
	visOff, goIdx, rereadIdx, reads := -1, -1, -1, 0
	for i, line := range log {
		switch {
		case line == "gm "+raidwalk.GMVisibleOff && visOff < 0:
			visOff = i
		case strings.HasPrefix(line, "gm .go xyz") && goIdx < 0:
			goIdx = i
		case strings.HasPrefix(line, "tt q units"):
			reads++
			if reads == 2 {
				rereadIdx = i
			}
		}
	}
	_ = spot
	if visOff < 0 || goIdx < 0 || rereadIdx < 0 || !(visOff < goIdx && goIdx < rereadIdx) {
		t.Fatalf("send log %q, want gm visible-off, then the go, then the re-read", log)
	}
}

func TestRaidWalkBossNotFoundCorpseReadPointFarMovesAndRereads(t *testing.T) {
	f := zgThekalFixture(t, "zul_gurub")
	boss := zgBossStep(t)
	total := len(zgRows(t).Steps)
	f.opts.pausedWait = 30 * time.Second
	f.agent.startReply = zgThekalStart(total, boss)
	spot := zgRows(t).Steps[boss-1]
	wantGo := fmt.Sprintf(".go xyz %s %s %s 309", num32(spot.X), num32(spot.Y), num32(spot.Z+2))
	seenGo := false
	f.agent.ttFn = func(cmd string) (ttResult, error) {
		if !strings.Contains(cmd, "all dead e=14599") {
			// The boss's reborn phase entry: no corpse in the toolkit's answer for it.
			return ttReplyOf(t, map[string]any{"units": []any{}}), nil
		}
		if !seenGo {
			return ttResult{}, errors.New("rpc error -32003: eluna: point_far: point is 258 yd from you (max 150): .go xyz there first")
		}
		return ttReplyOf(t, map[string]any{"units": []map[string]any{{"g": packet.GUID(9).String(), "e": 14599, "dead": true, "d": 12.5, "hp": 0}}}), nil
	}
	f.agent.mu.Lock()
	f.agent.startQueue = zgThekalPauseQueue(boss, total)
	f.agent.mu.Unlock()
	f.agent.onGM = func(line string) {
		if line == wantGo {
			seenGo = true
		}
	}
	f.agent.hostilesFn = func(n int) []tbm.HostileRec {
		if n <= 1 {
			return []tbm.HostileRec{{GUID: 9, Entry: 14599, HPPct: 60, Target: packet.GUID(1000)}}
		}
		return nil
	}
	zgThekalDoneOnSkip(f, boss, total)
	if code := f.run(context.Background()); code != exitOK {
		t.Fatalf("exit %d, want 0: %s\n%s", code, f.stderr.String(), f.stdout.String())
	}
	f.agent.mu.Lock()
	verbs, tt := append([]string{}, f.agent.runVerbs...), append([]string{}, f.agent.ttCalls...)
	gm := append([]string{}, f.agent.gm...)
	log := append([]string{}, f.agent.log...)
	f.agent.mu.Unlock()
	if countPrefix(verbs, "skip") != 1 {
		t.Fatalf("run verbs %v, want exactly one skip", verbs)
	}
	// The refused read, the re-read after the move that starts the corpse wait, and at least one read 15 s later:
	// the kill needs the confirmed corpse (the guid the first frame showed alive), not the re-read alone.
	if len(tt) < 3 || !strings.Contains(tt[0], "all dead e=14599") || !strings.Contains(tt[1], "all dead e=14599") {
		t.Fatalf("toolkit calls %q, want the refused read, its re-read and the confirming read of 14599", tt)
	}
	gos := 0
	for _, line := range gm {
		if line == wantGo {
			gos++
		}
	}
	if gos != 1 {
		t.Fatalf("gm lines %q, want exactly one %q between the two reads", gm, wantGo)
	}
	// The move went out between the two reads: gm, then tt, in the walk's own send log.
	goIdx, rereadIdx := -1, -1
	for i, line := range log {
		if line == "gm "+wantGo && goIdx < 0 {
			goIdx = i
		}
		if strings.HasPrefix(line, "tt q units") && strings.Contains(line, "all dead e=14599") {
			rereadIdx = i
		}
	}
	if goIdx < 0 || rereadIdx < 0 || goIdx > rereadIdx {
		t.Fatalf("send log %q, want the %q gm line before the second corpse read", log, wantGo)
	}
	if !hasNote(f.summary(), "the commander moved to the boss row for the corpse read") {
		t.Fatalf("notes %q, want the move note", f.summary().Notes)
	}
}

// Walk 25 point_far round 2: a `.go xyz` answers at once but the server lands the commander a moment later, so a
// re-read at once can still be refused with point_far and latch the reads off. The walk waits for the landing at
// the boss row with the revive's own landing wait before the move note and the re-read: the fake lands the go 3
// status reads later, and the second toolkit read goes out only after it.
func TestRaidWalkBossNotFoundCorpseReadPointFarWaitsForLanding(t *testing.T) {
	f := zgThekalFixture(t, "zul_gurub")
	boss := zgBossStep(t)
	total := len(zgRows(t).Steps)
	f.opts.pausedWait = 30 * time.Second
	f.agent.startReply = zgThekalStart(total, boss)
	spot := zgRows(t).Steps[boss-1]
	f.agent.mu.Lock()
	f.agent.startQueue = zgThekalPauseQueue(boss, total)
	f.agent.goLagEvery = 3 // the go is accepted but lands 3 status reads later
	f.agent.mu.Unlock()
	statusReads := func() int {
		f.agent.mu.Lock()
		defer f.agent.mu.Unlock()
		n := 0
		for _, c := range f.agent.calls {
			if c == "status" {
				n++
			}
		}
		return n
	}
	var atRead []int
	f.agent.ttFn = func(cmd string) (ttResult, error) {
		if !strings.Contains(cmd, "all dead e=14599") {
			// The boss's reborn phase entry: no corpse in the toolkit's answer for it.
			return ttReplyOf(t, map[string]any{"units": []any{}}), nil
		}
		f.agent.mu.Lock()
		x, y, z := f.agent.x, f.agent.y, f.agent.z
		f.agent.mu.Unlock()
		atRead = append(atRead, statusReads())
		if x != spot.X || y != spot.Y || z != spot.Z+2 {
			// Still standing far away: the read is refused, as the live toolkit is.
			return ttResult{}, errors.New("rpc error -32003: eluna: point_far: point is 258 yd from you (max 150): .go xyz there first")
		}
		return ttReplyOf(t, map[string]any{"units": []map[string]any{{"g": packet.GUID(9).String(), "e": 14599, "dead": true, "d": 12.5, "hp": 0}}}), nil
	}
	f.agent.hostilesFn = func(n int) []tbm.HostileRec {
		if n <= 1 {
			return []tbm.HostileRec{{GUID: 9, Entry: 14599, HPPct: 60, Target: packet.GUID(1000)}}
		}
		return nil
	}
	zgThekalDoneOnSkip(f, boss, total)
	if code := f.run(context.Background()); code != exitOK {
		t.Fatalf("exit %d, want 0: %s\n%s", code, f.stderr.String(), f.stdout.String())
	}
	f.agent.mu.Lock()
	verbs, tt := append([]string{}, f.agent.runVerbs...), append([]string{}, f.agent.ttCalls...)
	f.agent.mu.Unlock()
	if countPrefix(verbs, "skip") != 1 {
		t.Fatalf("run verbs %v, want exactly one skip", verbs)
	}
	// The refused read, the re-read after the landing that starts the corpse wait, and the later confirming reads.
	if len(tt) < 3 {
		t.Fatalf("toolkit calls %q, want the refused read, its re-read and the confirming reads", tt)
	}
	if len(atRead) < 2 || atRead[1]-atRead[0] < 3 {
		t.Fatalf("toolkit reads at status counts %v, want the re-read at least 3 status reads after the refused one (the landing)", atRead)
	}
	if !hasNote(f.summary(), "the commander moved to the boss row for the corpse read") {
		t.Fatalf("notes %q, want the move note", f.summary().Notes)
	}
}

// Walk 25 point_far round 2: the move's landing never comes. No re-read goes out, the reads latch with the
// failed-move note, and the walk waits out the pause as before.
func TestRaidWalkBossNotFoundCorpseReadPointFarLandingNeverComes(t *testing.T) {
	f := zgThekalFixture(t, "zul_gurub")
	boss := zgBossStep(t)
	total := len(zgRows(t).Steps)
	f.opts.pausedWait = 30 * time.Second
	f.agent.startReply = zgThekalStart(total, boss)
	f.agent.mu.Lock()
	f.agent.startQueue = zgThekalPauseQueue(boss, total)
	f.agent.mu.Unlock()
	neverLanding(f) // the go is accepted but never lands
	f.agent.ttFn = func(cmd string) (ttResult, error) {
		return ttResult{}, errors.New("rpc error -32003: eluna: point_far: point is 258 yd from you (max 150): .go xyz there first")
	}
	f.agent.hostilesFn = func(n int) []tbm.HostileRec {
		if n <= 1 {
			return []tbm.HostileRec{{GUID: 9, Entry: 14599, HPPct: 60, Target: packet.GUID(1000)}}
		}
		return nil
	}
	if code := f.run(context.Background()); code != exitRaidWalkFailed {
		t.Fatalf("exit %d, want 18: %s\n%s", code, f.stderr.String(), f.stdout.String())
	}
	f.agent.mu.Lock()
	verbs, tt := append([]string{}, f.agent.runVerbs...), append([]string{}, f.agent.ttCalls...)
	f.agent.mu.Unlock()
	if countPrefix(verbs, "skip") != 0 {
		t.Fatalf("run verbs %v, want no skip: the move never landed", verbs)
	}
	if len(tt) != 1 {
		t.Fatalf("toolkit calls %q, want exactly the refused read (no re-read without the landing)", tt)
	}
	if hasNote(f.summary(), "the commander moved to the boss row for the corpse read") {
		t.Fatalf("notes %q, want no move note without the landing", f.summary().Notes)
	}
	if !hasNote(f.summary(), "the move to the boss row did not land") {
		t.Fatalf("notes %q, want the failed-move note", f.summary().Notes)
	}
}

// A toolkit read that fails is no evidence (the walk waits as a pause) and turns the reads off for the rest of the
// walk: a server without the toolkit must not cost the watch loop a timeout on every pause tick.
func TestRaidWalkBossNotFoundToolkitFailureStillPauses(t *testing.T) {
	f, next, total := bossDoneFixture(t)
	boss := next - 1
	f.opts.pausedWait = 30 * time.Second
	f.agent.startReply = zgStart(total, next)
	f.agent.ttFn = func(cmd string) (ttResult, error) { return ttResult{}, errors.New("no such command") }
	var q []queuedEvent
	for range 12 {
		q = append(q, queuedEvent{raw: fmt.Sprintf("TBM:RUN|molten_core|running|%d/%d|lucifron|boss|begin|boss 12118 not at his spot: waiting for him|until step %d (step %d)", boss, total, next, next)})
	}
	q = append(q, queuedEvent{raw: bossNotFoundLine("molten_core", boss, total, 12118, next)})
	f.agent.mu.Lock()
	f.agent.startQueue = q
	f.agent.mu.Unlock()
	f.agent.hostilesFn = func(n int) []tbm.HostileRec {
		if n <= 1 {
			return []tbm.HostileRec{{GUID: 9, Entry: 12118, HPPct: 60, Target: packet.GUID(1000)}}
		}
		return nil
	}
	code := f.run(context.Background())
	if code != exitRaidWalkFailed {
		t.Fatalf("exit %d, want 18: %s", code, f.stderr.String())
	}
	f.agent.mu.Lock()
	verbs, tt := append([]string{}, f.agent.runVerbs...), append([]string{}, f.agent.ttCalls...)
	f.agent.mu.Unlock()
	if countPrefix(verbs, "skip") != 0 {
		t.Fatalf("run verbs %v, want no skip: a failed read is no evidence", verbs)
	}
	if len(tt) != 1 {
		t.Fatalf("toolkit calls %v, want exactly one (the read that failed)", tt)
	}
	if !hasNote(f.summary(), "the toolkit corpse read failed") {
		t.Fatalf("notes %q, want the failed-read note", f.summary().Notes)
	}
}

// The toolkit read runs into its own deadline: that is a failure, latched once (Codex, round 3).
func TestRaidWalkBossNotFoundToolkitTimeoutLatchesOnce(t *testing.T) {
	f, next, total := bossDoneFixture(t)
	boss := next - 1
	f.opts.pausedWait = 30 * time.Second
	f.agent.startReply = zgStart(total, next)
	old := bossCorpseWait
	bossCorpseWait = 100 * time.Millisecond
	t.Cleanup(func() { bossCorpseWait = old })
	// A toolkit that never answers in time: each read runs into the read's own deadline, which must latch the
	// reads off like any other failure (one read for the whole pause, not one per tick).
	f.agent.ttFn = func(cmd string) (ttResult, error) {
		time.Sleep(400 * time.Millisecond)
		return ttResult{}, errors.New("late")
	}
	var q []queuedEvent
	for range 12 {
		q = append(q, queuedEvent{raw: fmt.Sprintf("TBM:RUN|molten_core|running|%d/%d|lucifron|boss|begin|boss 12118 not at his spot: waiting for him|until step %d (step %d)", boss, total, next, next)})
	}
	q = append(q, queuedEvent{raw: bossNotFoundLine("molten_core", boss, total, 12118, next)})
	f.agent.mu.Lock()
	f.agent.startQueue = q
	f.agent.mu.Unlock()
	f.agent.hostilesFn = func(n int) []tbm.HostileRec {
		if n <= 1 {
			return []tbm.HostileRec{{GUID: 9, Entry: 12118, HPPct: 60, Target: packet.GUID(1000)}}
		}
		return nil
	}
	code := f.run(context.Background())
	if code != exitRaidWalkFailed {
		t.Fatalf("exit %d, want 18: %s", code, f.stderr.String())
	}
	f.agent.mu.Lock()
	verbs, tt := append([]string{}, f.agent.runVerbs...), append([]string{}, f.agent.ttCalls...)
	f.agent.mu.Unlock()
	if countPrefix(verbs, "skip") != 0 {
		t.Fatalf("run verbs %v, want no skip: a failed read is no evidence", verbs)
	}
	if len(tt) != 1 {
		t.Fatalf("toolkit calls %v, want exactly one (the read that timed out)", tt)
	}
	if !hasNote(f.summary(), "the toolkit corpse read failed") {
		t.Fatalf("notes %q, want the failed-read note", f.summary().Notes)
	}
}

// The pause arrives before the evidence (revive r7): the first ticks of the pause see no corpse (the frames
// hold nothing and the toolkit sees none), and a later frame shows the 14509 seen alive then dead at 0 %.
// The evidence is re-read on every tick while the pause lasts, and the skip latches only once it went out
// (review round 1, item 2): exactly one `.bot run skip`.
func TestRaidWalkBossNotFoundLateEvidenceSkips(t *testing.T) {
	f := zgThekalFixture(t, "zul_gurub")
	boss := zgBossStep(t)
	total := len(zgRows(t).Steps)
	f.opts.pausedWait = 60 * time.Second
	f.agent.startReply = zgThekalStart(total, boss)
	f.agent.ttFn = ttUnitsFn(t) // no corpse yet
	// The party stands at the boss row, so the fight's own frame (14599 at 60 %) uses up the tracker's one-shot
	// boss latch through the early hand-over instead of stopping the walk.
	f.agent.pos = map[string][3]float32{"Bot01": {zgRows(t).Steps[boss-1].X, zgRows(t).Steps[boss-1].Y, zgRows(t).Steps[boss-1].Z}}
	f.agent.mu.Lock()
	f.agent.startQueue = zgThekalPauseQueue(boss, total)
	f.agent.mu.Unlock()
	frames, pauseFrame := 0, -1
	f.agent.eventsHook = func(raw string) {
		if strings.Contains(raw, "boss 14599 not found") {
			pauseFrame = frames
		}
	}
	f.agent.hostilesFn = func(n int) []tbm.HostileRec {
		frames++
		switch {
		case pauseFrame < 0 && frames <= 12:
			return []tbm.HostileRec{{GUID: 9, Entry: 14599, HPPct: 60, Target: packet.GUID(1000)}}
		case pauseFrame >= 0 && frames == pauseFrame+2:
			return []tbm.HostileRec{{GUID: 10, Entry: 14509, HPPct: 99, Target: packet.GUID(1000)}} // reborn, alive first in this attempt
		case pauseFrame >= 0 && frames > pauseFrame+3:
			return []tbm.HostileRec{{GUID: 10, Entry: 14509, HPPct: 0}} // the kill, seen alive first in this attempt
		}
		return nil
	}
	zgThekalDoneOnSkip(f, boss, total)
	if code := f.run(context.Background()); code != exitOK {
		t.Fatalf("exit %d, want 0: %s\n%s", code, f.stderr.String(), f.stdout.String())
	}
	f.agent.mu.Lock()
	verbs := append([]string{}, f.agent.runVerbs...)
	f.agent.mu.Unlock()
	if countPrefix(verbs, "skip") != 1 {
		t.Fatalf("run verbs %v, want exactly one skip (the evidence came after the pause)", verbs)
	}
	if !hasNote(f.summary(), "no kill in the frames yet") {
		t.Fatalf("notes %q, want the first no-evidence note", f.summary().Notes)
	}
}

// Review round 3 (Codex HIGH, r7): a live row after the death voids the frame corpse. The boss fights
// (a live 60 % row: this walk's attempt start), dies (a 0 % row of the same guid), the encounter resets
// (the family seen alive at 100 % afterwards) and the party loses sight of him, so when the run leader
// pauses with "boss 12118 not found" the family corpse is voided by the newer live row: no dead row
// evidence, no toolkit read (a fresh live row decides alone), no skip, the pause stays (exit 18).
func TestRaidWalkBossNotFoundRespawnedBossStillPauses(t *testing.T) {
	f, next, total := bossDoneFixture(t)
	boss := next - 1
	f.opts.pausedWait = 4 * time.Second
	f.agent.startReply = zgStart(total, next)
	f.agent.ttFn = ttUnitsFn(t) // no corpse of a boss unit at the boss row
	var q []queuedEvent
	for range 12 {
		q = append(q, queuedEvent{raw: fmt.Sprintf("TBM:RUN|molten_core|running|%d/%d|lucifron|boss|begin|boss 12118 not at his spot: waiting for him|until step %d (step %d)", boss, total, next, next)})
	}
	q = append(q, queuedEvent{raw: bossNotFoundLine("molten_core", boss, total, 12118, next)})
	f.agent.mu.Lock()
	f.agent.startQueue = q
	f.agent.mu.Unlock()
	f.agent.hostilesFn = func(n int) []tbm.HostileRec {
		switch {
		case n <= 3:
			return []tbm.HostileRec{{GUID: 9, Entry: 12118, HPPct: 60, Target: packet.GUID(1000)}} // this walk's attempt start
		case n <= 4:
			return []tbm.HostileRec{{GUID: 9, Entry: 12118, HPPct: 0}} // the kill, seen alive first in this attempt
		default:
			return []tbm.HostileRec{{GUID: 10, Entry: 12118, HPPct: 100, Target: packet.GUID(1000)}} // the reset: the boss back alive
		}
	}
	code := f.run(context.Background())
	if code != exitRaidWalkFailed {
		t.Fatalf("exit %d, want 18: %s\n%s", code, f.stderr.String(), f.stdout.String())
	}
	if !strings.Contains(f.summary().EndReason, "paused: boss 12118 not found") {
		t.Fatalf("end %q, want the pause", f.summary().EndReason)
	}
	f.agent.mu.Lock()
	verbs, tt := append([]string{}, f.agent.runVerbs...), append([]string{}, f.agent.ttCalls...)
	f.agent.mu.Unlock()
	if countPrefix(verbs, "skip") != 0 {
		t.Fatalf("run verbs %v, want no skip: the guid was alive again after its death", verbs)
	}
	if len(tt) != 0 {
		t.Fatalf("toolkit calls %q, want no corpse read: the live boss row decides alone", tt)
	}
}

// Review round 3 (Codex HIGH, r7): the attempt bound itself. A 0 % row the frames saw before the CURRENT attempt's
// engagement is another attempt's corpse (the frames are never pruned, so a wipe recovery leaves it in
// w.hostiles), and a 0 % row inside the attempt of a guid seen alive there is evidence. Without the bound
// the stale row alone would be a kill; without the link a corpse the frames never saw alive would be one.
func TestBossKilledDeadRowBoundToTheAttempt(t *testing.T) {
	now := time.Date(2026, 10, 29, 20, 0, 0, 0, time.Local)
	start := func(deadAt time.Time) *walk {
		return &walk{
			mt:            "Tank",
			botAt:         map[string]botPos{"Tank": {at: now}},
			hostiles:      map[string]hostileState{"0x9": {entry: 12118, hppct: 0, at: deadAt}},
			bossAttemptAt: map[uint32]time.Time{12118: now.Add(-60 * time.Second)},
			bossAliveSeen: map[string]time.Time{"0x9": now.Add(-60 * time.Second)},
		}
	}
	ok, ev := start(now.Add(-90*time.Second)).bossKilled(context.Background(), 12118, now)
	if ok || ev != "no boss unit dead in the frames and no corpse at the boss row" {
		t.Fatalf("bossKilled = %v (%q), want no evidence: the 0 %% row predates the attempt", ok, ev)
	}
	ok, ev = start(now.Add(-30*time.Second)).bossKilled(context.Background(), 12118, now)
	if !ok || !strings.Contains(ev, "seen dead in the frames") {
		t.Fatalf("bossKilled = %v (%q), want the frame row inside the attempt as the evidence", ok, ev)
	}
}

// bossTTStub is a walkAgent that only answers toolkit reads (bossKilled's corpse read).
type bossTTStub struct {
	fn func(cmd string) (ttResult, error)
}

func (s *bossTTStub) Call(ctx context.Context, method string, params, out any, timeout time.Duration) error {
	if method != "tt" {
		return errors.New("unexpected RPC " + method)
	}
	raw, _ := json.Marshal(params)
	var p ttParams
	_ = json.Unmarshal(raw, &p)
	res, err := s.fn(p.Cmd)
	if err != nil {
		return err
	}
	b, _ := json.Marshal(res)
	return json.Unmarshal(b, out)
}

// bossCorpseGUID is the toolkit corpse's guid in the tests below: entry 12118 (0x2F56), counter 9.
const bossCorpseGUID = "0xF130002F56000009"

// bossCorpseWalk is a walk on a one-boss plan (entry 12118) whose toolkit always reports corpse:
// it returns the walk and the corpse guid's canonical key for the frame maps.
func bossCorpseWalk(t *testing.T, corpse map[string]any) (*walk, string) {
	t.Helper()
	g, err := packet.ParseGUID(bossCorpseGUID)
	if err != nil {
		t.Fatal(err)
	}
	f := newFixture(t)
	w := &walk{
		plan:          &dungeon.Plan{Map: 230, Legs: []dungeon.Leg{{Boss: &dungeon.Boss{Entry: 12118}}}},
		rows:          planexport.Rows{Steps: []planexport.StepRow{{Kind: planexport.KindBoss, Entry: 12118, X: 100, Y: 200, Z: 300}}},
		agent:         &bossTTStub{fn: ttUnitsFn(t, corpse)},
		env:           walkEnv{now: f.clock.now, sleep: f.clock.sleep},
		mt:            "Tank",
		hires:         map[string]bool{"Tank": true},
		hostiles:      map[string]hostileState{},
		bossAttemptAt: map[uint32]time.Time{},
	}
	setBossCorpseMaps(w)
	return w, g.String()
}

// setBossCorpseMaps inits the corpse wait maps; a helper so the call sites read plainly.
func setBossCorpseMaps(w *walk) {
	w.bossAliveSeen = map[string]time.Time{}
	w.bossCorpseSeen = map[string]time.Time{}
}

// Codex HIGH (r6): a toolkit corpse whose guid the frames never showed alive in this attempt is a
// stale or foreign corpse, not kill evidence: no skip, even when the read repeats 16 s later.
func TestBossKilledToolkitCorpseNeedsAttemptAliveGUID(t *testing.T) {
	now := time.Date(2026, 10, 29, 20, 0, 0, 0, time.Local)
	w, _ := bossCorpseWalk(t, map[string]any{"g": bossCorpseGUID, "e": 12118, "dead": true, "d": 12.5})
	t1, t2 := now.Add(16*time.Second), now.Add(32*time.Second)
	w.botAt = map[string]botPos{"Tank": {at: t2}}
	w.bossAttemptAt[12118] = now // the attempt began, but this guid was never seen alive in it
	if ok, ev := w.bossKilled(context.Background(), 12118, t1); ok {
		t.Fatalf("bossKilled = true (%q), want no skip: the corpse guid was never alive this attempt", ev)
	}
	if ok, ev := w.bossKilled(context.Background(), 12118, t2); ok {
		t.Fatalf("bossKilled = true (%q), want no skip even 16 s later: the guid link never held", ev)
	}
}

// Codex HIGH (r6): the boss is seen alive, the toolkit reports his corpse, then the SAME guid is
// alive again within 15 s (a fake death: Thekal phase 1 dies and is raised again). The
// resurrection voids the corpse wait: no skip once the alive row goes stale either.
func TestBossKilledToolkitCorpseFakeDeathStillPauses(t *testing.T) {
	now := time.Date(2026, 10, 29, 20, 0, 0, 0, time.Local)
	w, key := bossCorpseWalk(t, map[string]any{"g": bossCorpseGUID, "e": 12118, "dead": true, "d": 12.5})
	t1 := now.Add(16 * time.Second)
	w.botAt = map[string]botPos{"Tank": {at: t1.Add(21 * time.Second)}}
	w.bossAttemptAt[12118] = now
	w.bossAliveSeen[key] = now
	w.hostiles[key] = hostileState{entry: 12118, hppct: 60, target: 1000, at: now} // stale at t1 (16 s)
	if ok, ev := w.bossKilled(context.Background(), 12118, t1); ok {
		t.Fatalf("bossKilled = true (%q), want no skip on the first corpse sighting: it starts the wait", ev)
	}
	// Raised again 5 s after the first sighting: the wait is void, not confirmed.
	aliveAgain := t1.Add(5 * time.Second)
	w.hostiles[key] = hostileState{entry: 12118, hppct: 100, at: aliveAgain}
	w.bossAliveSeen[key] = aliveAgain
	if ok, ev := w.bossKilled(context.Background(), 12118, t1.Add(21*time.Second)); ok {
		t.Fatalf("bossKilled = true (%q), want no skip: the guid was alive again after the corpse", ev)
	}
}

// Codex HIGH (r6): seen alive, toolkit corpse, still dead 16 s later with no alive frame for that
// guid in between: the kill is proven, skip.
func TestBossKilledToolkitCorpseConfirmedSkips(t *testing.T) {
	now := time.Date(2026, 10, 29, 20, 0, 0, 0, time.Local)
	w, key := bossCorpseWalk(t, map[string]any{"g": bossCorpseGUID, "e": 12118, "dead": true, "d": 12.5})
	t1 := now.Add(16 * time.Second)
	w.botAt = map[string]botPos{"Tank": {at: t1.Add(16 * time.Second)}}
	w.bossAttemptAt[12118] = now
	w.bossAliveSeen[key] = now
	w.hostiles[key] = hostileState{entry: 12118, hppct: 60, target: 1000, at: now} // stale at t1 (16 s)
	if ok, ev := w.bossKilled(context.Background(), 12118, t1); ok {
		t.Fatalf("bossKilled = true (%q), want no skip on the first corpse sighting: it starts the wait", ev)
	}
	ok, ev := w.bossKilled(context.Background(), 12118, t1.Add(16*time.Second))
	if !ok || !strings.Contains(ev, "the toolkit sees 12118 dead") {
		t.Fatalf("bossKilled = %v (%q), want the confirmed toolkit corpse as the evidence", ok, ev)
	}
}

// bossFrameWalk is a walk on a one-boss plan (entry 12118) whose toolkit sees no corpse: the frame
// path alone decides (revive r7). The caller feeds frames with trackFrame and reads bossKilled.
func bossFrameWalk(t *testing.T) *walk {
	t.Helper()
	f := newFixture(t)
	w := &walk{
		plan:          &dungeon.Plan{Map: 230, Legs: []dungeon.Leg{{Boss: &dungeon.Boss{Entry: 12118}}}},
		rows:          planexport.Rows{Steps: []planexport.StepRow{{Kind: planexport.KindBoss, Entry: 12118, X: 100, Y: 200, Z: 300}}},
		agent:         &bossTTStub{fn: ttUnitsFn(t)},
		env:           walkEnv{now: f.clock.now, sleep: f.clock.sleep},
		mt:            "Tank",
		hires:         map[string]bool{"Tank": true},
		partyG:        map[uint64]bool{},
		hostiles:      map[string]hostileState{},
		botAt:         map[string]botPos{},
		bossRows:      map[uint32]hostileState{},
		bossRule:      raidwalk.NewBossRows(&dungeon.Plan{Legs: []dungeon.Leg{{Boss: &dungeon.Boss{Entry: 12118}}}}),
		bossAttemptAt: map[uint32]time.Time{},
	}
	setBossCorpseMaps(w)
	return w
}

// bossTestFrame is one frame with Tank (guid 1000) watching the given hostile rows.
func bossTestFrame(hostiles ...tbm.HostileRec) *tbm.ViewFrame {
	return &tbm.ViewFrame{
		Party:    []tbm.PartyRec{{GUID: 1000, Name: "Tank"}},
		Hostiles: hostiles,
	}
}

// Codex HIGH (r7): a dead row never starts the attempt. The first sighting of a pre-existing
// corpse (0 %, even holding a party target) records no attempt start and no live sighting, so
// bossKilled finds no evidence and the pause stays: no skip.
func TestBossAttemptCorpseFirstStartsNoAttempt(t *testing.T) {
	now := time.Date(2026, 10, 29, 20, 0, 0, 0, time.Local)
	w := bossFrameWalk(t)
	w.trackFrame(now, bossTestFrame(tbm.HostileRec{GUID: 9, Entry: 12118, HPPct: 0, Target: packet.GUID(1000)}))
	if at := w.bossAttemptAt[12118]; !at.IsZero() {
		t.Fatalf("bossAttemptAt = %v, want zero: a 0 %% row never starts the attempt", at)
	}
	if _, ok := w.bossAliveSeen[packet.GUID(9).String()]; ok {
		t.Fatalf("bossAliveSeen holds the corpse guid, want nothing: a corpse is no live sighting")
	}
	killAt := now.Add(16 * time.Second)
	w.botAt = map[string]botPos{"Tank": {at: killAt}}
	if ok, ev := w.bossKilled(context.Background(), 12118, killAt); ok {
		t.Fatalf("bossKilled = true (%q), want no skip: the corpse predates any attempt", ev)
	}
}

// Codex HIGH (r7): a live sighting from an earlier attempt vouches for nothing. The frames show 12118
// standing at full hp before any attempt began (no target, no latch), then the pull (a live 60 % row of
// another guid starts the attempt) and a 0 % row of the first guid. That corpse predates the attempt's
// live link, so it is no evidence and the empty toolkit proves nothing: no skip.
func TestBossAttemptEarlierSightingVoidsCorpse(t *testing.T) {
	now := time.Date(2026, 10, 29, 20, 0, 0, 0, time.Local)
	w := bossFrameWalk(t)
	w.trackFrame(now, bossTestFrame(tbm.HostileRec{GUID: 9, Entry: 12118, HPPct: 100}))
	if _, ok := w.bossAliveSeen[packet.GUID(9).String()]; !ok {
		t.Fatalf("bossAliveSeen = %v, want the standing boss's sighting", w.bossAliveSeen)
	}
	if at := w.bossAttemptAt[12118]; !at.IsZero() {
		t.Fatalf("bossAttemptAt = %v, want zero: a full hp row with no target starts nothing", at)
	}
	pullAt := now.Add(2 * time.Second)
	w.trackFrame(pullAt, bossTestFrame(tbm.HostileRec{GUID: 10, Entry: 12118, HPPct: 60, Target: packet.GUID(1000)}))
	if at := w.bossAttemptAt[12118]; !at.Equal(pullAt) {
		t.Fatalf("bossAttemptAt = %v, want %v: the pull starts the attempt", at, pullAt)
	}
	deadAt := now.Add(20 * time.Second)
	w.trackFrame(deadAt, bossTestFrame(tbm.HostileRec{GUID: 9, Entry: 12118, HPPct: 0}))
	w.botAt = map[string]botPos{"Tank": {at: deadAt}}
	if ok, ev := w.bossKilled(context.Background(), 12118, deadAt); ok {
		t.Fatalf("bossKilled = true (%q), want no skip: guid 9 was never seen alive in this attempt", ev)
	}
}

// Codex HIGH (r7): the attempt starts on a live engaged row, and the 0 % row of that same guid is
// the kill: seen alive before its death, inside the attempt. Skip.
func TestBossAttemptLiveThenDeathKills(t *testing.T) {
	now := time.Date(2026, 10, 29, 20, 0, 0, 0, time.Local)
	w := bossFrameWalk(t)
	w.trackFrame(now, bossTestFrame(tbm.HostileRec{GUID: 9, Entry: 12118, HPPct: 60, Target: packet.GUID(1000)}))
	if at := w.bossAttemptAt[12118]; !at.Equal(now) {
		t.Fatalf("bossAttemptAt = %v, want %v: the live engaged row starts the attempt", at, now)
	}
	deadAt := now.Add(20 * time.Second)
	w.trackFrame(deadAt, bossTestFrame(tbm.HostileRec{GUID: 9, Entry: 12118, HPPct: 0}))
	w.botAt = map[string]botPos{"Tank": {at: deadAt}}
	ok, ev := w.bossKilled(context.Background(), 12118, deadAt)
	if !ok || !strings.Contains(ev, "seen dead in the frames") {
		t.Fatalf("bossKilled = %v (%q), want the frame row inside the attempt as the evidence", ok, ev)
	}
}

// Codex HIGH (r7): a fake death voids the kill. Live, dead, then the same guid alive again (raised):
// the newest frame row is a stale live one, so no dead row is evidence and the empty toolkit proves
// nothing: no skip, the pause stays.
func TestBossAttemptFakeDeathStillPauses(t *testing.T) {
	now := time.Date(2026, 10, 29, 20, 0, 0, 0, time.Local)
	w := bossFrameWalk(t)
	w.trackFrame(now, bossTestFrame(tbm.HostileRec{GUID: 9, Entry: 12118, HPPct: 60, Target: packet.GUID(1000)}))
	deadAt := now.Add(20 * time.Second)
	w.trackFrame(deadAt, bossTestFrame(tbm.HostileRec{GUID: 9, Entry: 12118, HPPct: 0}))
	raisedAt := deadAt.Add(5 * time.Second)
	w.trackFrame(raisedAt, bossTestFrame(tbm.HostileRec{GUID: 9, Entry: 12118, HPPct: 100, Target: packet.GUID(1000)}))
	if at := w.bossAttemptAt[12118]; !at.Equal(now) {
		t.Fatalf("bossAttemptAt = %v, want %v: the attempt started on the first live row", at, now)
	}
	killAt := raisedAt.Add(16 * time.Second)
	w.botAt = map[string]botPos{"Tank": {at: killAt}}
	if ok, ev := w.bossKilled(context.Background(), 12118, killAt); ok {
		t.Fatalf("bossKilled = true (%q), want no skip: the guid was alive again after its death", ev)
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

// zgArlokkFixture is a Zul'Gurub walk whose until is the step after Arlokk's boss row, with that leg's gong row and boss row.
func zgArlokkFixture(t *testing.T) (f *fixture, openStep, bossStep int) {
	t.Helper()
	f = newFixture(t)
	f.opts.plan, f.opts.holder = "zul_gurub", "raid-walk-zul_gurub"
	rows := zgRows(t)
	for i, s := range rows.Steps {
		if s.Kind == planexport.KindBoss && s.Entry == 14515 {
			bossStep = i + 1
		}
	}
	if bossStep == 0 {
		t.Fatal("no Arlokk boss row in the ZG rows")
	}
	f.opts.until = fmt.Sprintf("step %d", bossStep+1)
	p, err := dungeon.Load(filepath.Join(f.opts.dir, "zul_gurub.json"))
	if err != nil {
		t.Fatal(err)
	}
	step, ok := raidwalk.SummonOpenStep(rows, p, bossStep)
	if !ok {
		t.Fatalf("the Arlokk boss row %d reports no summoning open step (its plan annotation)", bossStep)
	}
	f.agent.instMap = p.Map
	f.agent.enterMap = p.Map
	f.agent.ttFn = ttUnitsFn(t)
	return f, step, bossStep
}

// Walk 35 (b3/e3, Arlokk): after a wipe she despawns and no spawn row is left, so the run leader pauses "boss 14515 not found" on
// her own step. The leg's only other row is the gong (use_object 180526, kind open), which summons her: the walk restarts its run at
// that row (`.bot run stop`, then `.bot run start ... from step <gong>`) with the run's own until, so the party repeats the gong and
// meets her again, instead of waiting out pausedWait and ending the walk (which is what forced an operator restart in walk 35).
func TestRaidWalkBossNotFoundRerunsTheLegsSummoningOpenStep(t *testing.T) {
	f, openStep, bossStep := zgArlokkFixture(t)
	total := len(zgRows(t).Steps)
	f.opts.pausedWait = 30 * time.Second
	f.agent.startReply = &commanderReply{Reply: tbm.Reply{Kind: tbm.KindAck, OK: true,
		Ack:  &tbm.Ack{Intent: "run start", Scope: "party", Count: 1, Executor: "-"},
		Line: fmt.Sprintf("TBM:RUN|zul_gurub|running|1/%d|high_priestess_arlokk|move|walk|walking|until step %d (step %d)", total, bossStep+1, bossStep+1),
		Lines: []string{
			fmt.Sprintf("TBM:RUN|zul_gurub|running|1/%d|high_priestess_arlokk|move|walk|walking|until step %d (step %d)", total, bossStep+1, bossStep+1),
			"TBM:ACTION_ACK|run start|party|1|-",
		}}}
	f.agent.startQueue = []queuedEvent{{raw: fmt.Sprintf("TBM:RUN|zul_gurub|paused|%d/%d|high_priestess_arlokk|boss|begin|boss 14515 not found: not alive or dead within 30.0 yd of -11540.7 -1627.7 41.3 for 60 s nor anywhere in the instance (.bot run skip if he is already dead)|until step %d (step %d)",
		bossStep, total, bossStep+1, bossStep+1)}}
	f.agent.hostilesFn = func(int) []tbm.HostileRec { return nil } // she despawned: no unit of her entry in the frames
	// The stop's confirmation: the first `run status` still sees the old run (the stop has not landed), the next sees it done.
	asks := 0
	f.agent.runStatusFn = func() *commanderReply {
		asks++
		state := "done"
		if asks == 1 {
			state = "paused"
		}
		run := fmt.Sprintf("TBM:RUN|zul_gurub|%s|%d/%d|high_priestess_arlokk|boss|begin|boss 14515 not found", state, bossStep, total)
		return &commanderReply{Reply: tbm.Reply{Kind: tbm.KindAck, OK: true,
			Ack:   &tbm.Ack{Intent: "run status", Scope: "party", Count: 1, Executor: "-"},
			Line:  run,
			Lines: []string{run, "TBM:ACTION_ACK|run status|party|1|-"}}}
	}
	starts := 0
	f.agent.onStart = func() {
		starts++
		if starts != 2 {
			return
		}
		// The re-run's own answer: the run running at the gong row, with the walk's until. The hook runs before the
		// fake hands its reply back, so this replaces the first start's answer.
		run := fmt.Sprintf("TBM:RUN|zul_gurub|running|%d/%d|high_priestess_arlokk|open|begin|arrived|until step %d (step %d)", openStep, total, bossStep+1, bossStep+1)
		f.agent.startReply = &commanderReply{Reply: tbm.Reply{Kind: tbm.KindAck, OK: true,
			Ack:   &tbm.Ack{Intent: "run start", Scope: "party", Count: 1, Executor: "-"},
			Line:  run,
			Lines: []string{run, "TBM:ACTION_ACK|run start|party|1|-"}}}
		f.agent.push(fmt.Sprintf("TBM:RUN|zul_gurub|done|%d/%d|high_priestess_arlokk|move|walk|stopped before step %d (until step %d): the party stays where it stands", bossStep+1, total, bossStep+1, bossStep+1), "")
	}
	if code := f.run(context.Background()); code != exitOK {
		t.Fatalf("exit %d, want 0: %s\n%s", code, f.stderr.String(), f.stdout.String())
	}
	f.agent.mu.Lock()
	verbs := append([]string{}, f.agent.runVerbs...)
	f.agent.mu.Unlock()
	// The stop is a GM chat line (`.bot run stop`), not a commander run verb: without the stop confirmation's `run status`
	// asks, the verbs are the two starts.
	if got := strings.Join(startsAndSkips(verbs), ","); got != "start,start" {
		t.Fatalf("run verbs %q, want the run started again after the stop", got)
	}
	if asks < 2 {
		t.Fatalf("%d run status asks: the start must wait for a status that shows the run stopped", asks)
	}
	stopped, restarted := false, false
	for _, l := range f.agentLog() {
		switch {
		case strings.HasPrefix(l, "gm .bot run stop"):
			stopped = true
		case strings.HasPrefix(l, "cmdr run start ") && stopped:
			restarted = true
		}
	}
	if !stopped || !restarted {
		t.Fatalf("log %v: want `.bot run stop` before the re-run's start", f.agentLog())
	}
	want := fmt.Sprintf("zul_gurub|from|step|%d|until|step|%d", openStep, bossStep+1)
	if f.agent.startArgs != want {
		t.Fatalf("the re-run started with %q, want %q", f.agent.startArgs, want)
	}
	sum := f.summary()
	if sum.EndReason != "until" {
		t.Fatalf("end %q, want until", sum.EndReason)
	}
	if !hasNote(sum, fmt.Sprintf("re-running step %d", openStep)) || !hasNote(sum, fmt.Sprintf("restarted at step %d", openStep)) {
		t.Fatalf("notes %q, want the gong re-run and the restart", sum.Notes)
	}
	// No skip: the boss is not killed, she is brought back.
	if n := countPrefix(verbs, "skip"); n != 0 {
		t.Fatalf("%d skips went out", n)
	}
}

// The gong re-run must not start a second run over a live one: when `.bot run stop` is refused, no `run start` goes out and the walk
// falls back to the old pause path (exit 18 after pausedWait).
func TestRaidWalkBossNotFoundRerunNeedsTheStop(t *testing.T) {
	f, openStep, bossStep := zgArlokkFixture(t)
	total := len(zgRows(t).Steps)
	f.opts.pausedWait = 5 * time.Second
	f.agent.startReply = &commanderReply{Reply: tbm.Reply{Kind: tbm.KindAck, OK: true,
		Ack:  &tbm.Ack{Intent: "run start", Scope: "party", Count: 1, Executor: "-"},
		Line: fmt.Sprintf("TBM:RUN|zul_gurub|running|1/%d|high_priestess_arlokk|move|walk|walking|until step %d (step %d)", total, bossStep+1, bossStep+1),
		Lines: []string{
			fmt.Sprintf("TBM:RUN|zul_gurub|running|1/%d|high_priestess_arlokk|move|walk|walking|until step %d (step %d)", total, bossStep+1, bossStep+1),
			"TBM:ACTION_ACK|run start|party|1|-",
		}}}
	f.agent.startQueue = []queuedEvent{{raw: fmt.Sprintf("TBM:RUN|zul_gurub|paused|%d/%d|high_priestess_arlokk|boss|begin|boss 14515 not found: not alive or dead within 30.0 yd of -11540.7 -1627.7 41.3 for 60 s nor anywhere in the instance|until step %d (step %d)",
		bossStep, total, bossStep+1, bossStep+1)}}
	f.agent.hostilesFn = func(int) []tbm.HostileRec { return nil }
	f.agent.gmFail = func(line string) error {
		if line == ".bot run stop" {
			return fmt.Errorf("the command was refused")
		}
		return nil
	}
	if code := f.run(context.Background()); code != exitRaidWalkFailed {
		t.Fatalf("exit %d, want 18: %s", code, f.stderr.String())
	}
	f.agent.mu.Lock()
	verbs := append([]string{}, f.agent.runVerbs...)
	f.agent.mu.Unlock()
	if got := strings.Join(verbs, ","); got != "start" {
		t.Fatalf("run verbs %q: no second start may go out over a run that did not stop", got)
	}
	if !hasNote(f.summary(), fmt.Sprintf("re-running step %d", openStep)) || !hasNote(f.summary(), "the run stop before the re-run failed") {
		t.Fatalf("notes %q, want the re-run attempt and the failed stop", f.summary().Notes)
	}
}

// A `.bot run stop` that the commander takes is not the run stopping: the chat may be refused by the module, dropped, or handled
// late, and the old run then stays live. The re-run waits for the run's own status to show it stopped; a status that stays paused
// or running sends no second `run start`, leaves the cleanup armed (a stopped run is never assumed) and the pause path ends the walk.
func TestRaidWalkBossNotFoundRerunWaitsForTheRunToShowItStopped(t *testing.T) {
	for _, state := range []string{"paused", "running"} {
		t.Run(state, func(t *testing.T) {
			f, openStep, bossStep := zgArlokkFixture(t)
			total := zgArlokkPause(t, f, bossStep)
			f.opts.pausedWait = 5 * time.Second
			line := fmt.Sprintf("TBM:RUN|zul_gurub|%s|%d/%d|high_priestess_arlokk|boss|begin|boss 14515 not found", state, bossStep, total)
			f.agent.runStatusFn = func() *commanderReply {
				return &commanderReply{Reply: tbm.Reply{Kind: tbm.KindAck, OK: true,
					Ack:   &tbm.Ack{Intent: "run status", Scope: "party", Count: 1, Executor: "-"},
					Line:  line,
					Lines: []string{line, "TBM:ACTION_ACK|run status|party|1|-"}}}
			}
			if code := f.run(context.Background()); code != exitRaidWalkFailed {
				t.Fatalf("exit %d, want 18: %s\n%s", code, f.stderr.String(), f.stdout.String())
			}
			f.agent.mu.Lock()
			verbs := append([]string{}, f.agent.runVerbs...)
			f.agent.mu.Unlock()
			if got := strings.Join(startsAndSkips(verbs), ","); got != "start" {
				t.Fatalf("run verbs %q: no second start may go out over a run the status does not show stopped", got)
			}
			// The run may still be live: the walk's cleanup keeps its own stop instead of trusting the re-run's.
			if n := countPrefix(f.agent.gmLines(), ".bot run stop"); n != 2 {
				t.Fatalf("%d run stops, want the re-run's and the cleanup's", n)
			}
			sum := f.summary()
			if !hasNote(sum, "the run stop before the re-run is not confirmed") || !hasNote(sum, "the run is still "+state+" at step ") {
				t.Fatalf("notes %q, want the unconfirmed stop naming the %s run", sum.Notes, state)
			}
			if !hasNote(sum, fmt.Sprintf("re-running step %d", openStep)) {
				t.Fatalf("notes %q, want the re-run attempt", sum.Notes)
			}
		})
	}
}

// zgArlokkPause starts a Zul'Gurub walk whose run is running and then pauses at Arlokk's boss row with "boss 14515 not found",
// the pause testRaidWalkBossNotFoundRerunStopsTheRun's four cases fail their re-run from. It returns the plan's step count.
func zgArlokkPause(t *testing.T, f *fixture, bossStep int) int {
	t.Helper()
	total := len(zgRows(t).Steps)
	run := func(step int) string {
		return fmt.Sprintf("TBM:RUN|zul_gurub|running|%d/%d|high_priestess_arlokk|move|walk|walking|until step %d (step %d)", step, total, bossStep+1, bossStep+1)
	}
	f.agent.startReply = &commanderReply{Reply: tbm.Reply{Kind: tbm.KindAck, OK: true,
		Ack:   &tbm.Ack{Intent: "run start", Scope: "party", Count: 1, Executor: "-"},
		Line:  run(1),
		Lines: []string{run(1), "TBM:ACTION_ACK|run start|party|1|-"}}}
	f.agent.startQueue = []queuedEvent{{raw: fmt.Sprintf("TBM:RUN|zul_gurub|paused|%d/%d|high_priestess_arlokk|boss|begin|boss 14515 not found: not alive or dead within 30.0 yd of -11540.7 -1627.7 41.3 for 60 s nor anywhere in the instance (.bot run skip if he is already dead)|until step %d (step %d)",
		bossStep, total, bossStep+1, bossStep+1)}}
	f.agent.hostilesFn = func(int) []tbm.HostileRec { return nil } // she despawned: no unit of her entry in the frames
	f.opts.pausedWait = 60 * time.Second
	// The re-run's stop confirmation: the run's own status shows the run stopped after the `.bot run stop`.
	stopped := fmt.Sprintf("TBM:RUN|zul_gurub|done|%d/%d|high_priestess_arlokk|boss|begin|stopped (.bot run stop)", bossStep, total)
	f.agent.runStatusFn = func() *commanderReply {
		return &commanderReply{Reply: tbm.Reply{Kind: tbm.KindAck, OK: true,
			Ack:   &tbm.Ack{Intent: "run status", Scope: "party", Count: 1, Executor: "-"},
			Line:  stopped,
			Lines: []string{stopped, "TBM:ACTION_ACK|run status|party|1|-"}}}
	}
	return total
}

// The re-run's `run start` may be accepted by the server even when its own answer says nothing useful: the RPC times out, no run
// status arrives, the status is malformed, or it reports another step. The run then counts as possibly live: the walk sends
// `.bot run stop` again (its own cleanup would otherwise skip it) and falls back to the pause path.
func TestRaidWalkBossNotFoundRerunStopsTheRunWhenTheStartDoesNotCheckOut(t *testing.T) {
	for _, tc := range []struct {
		name  string
		reply func(total, openStep, bossStep int) (*commanderReply, error)
		want  string
	}{
		{"the start times out", func(total, openStep, bossStep int) (*commanderReply, error) {
			return nil, errors.New("commander run start: the call timed out after 25s")
		}, "the call timed out"},
		{"no status line", func(total, openStep, bossStep int) (*commanderReply, error) {
			return &commanderReply{Reply: tbm.Reply{Kind: tbm.KindAck, OK: true,
				Ack:  &tbm.Ack{Intent: "run start", Scope: "party", Count: 1, Executor: "-"},
				Line: "TBM:ACTION_ACK|run start|party|1|-"}}, nil
		}, "no TBM:RUN status for the new run"},
		{"a malformed status line", func(total, openStep, bossStep int) (*commanderReply, error) {
			return &commanderReply{Reply: tbm.Reply{Kind: tbm.KindAck, OK: true,
				Ack:  &tbm.Ack{Intent: "run start", Scope: "party", Count: 1, Executor: "-"},
				Line: "TBM:ACTION_ACK|run start|party|1|-"}}, nil
		}, "its status line is not a run line"},
		{"a status at another step", func(total, openStep, bossStep int) (*commanderReply, error) {
			run := fmt.Sprintf("TBM:RUN|zul_gurub|running|%d/%d|high_priestess_arlokk|open|begin|arrived|until step %d (step %d)", openStep+3, total, bossStep+1, bossStep+1)
			return &commanderReply{Reply: tbm.Reply{Kind: tbm.KindAck, OK: true,
				Ack:   &tbm.Ack{Intent: "run start", Scope: "party", Count: 1, Executor: "-"},
				Line:  run,
				Lines: []string{run, "TBM:ACTION_ACK|run start|party|1|-"}}}, nil
		}, "it started at step"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f, openStep, bossStep := zgArlokkFixture(t)
			total := zgArlokkPause(t, f, bossStep)
			starts := 0
			var rerunAt time.Time
			f.agent.onStart = func() {
				starts++
				if starts != 2 {
					return
				}
				rerunAt = f.agent.clock.now()
				rep, err := tc.reply(total, openStep, bossStep)
				if err != nil {
					f.agent.startCallErr = err
					return
				}
				f.agent.startReply = rep
				if tc.name == "a malformed status line" {
					f.agent.push("TBM:RUN|zul_gurub|running", "")
				}
			}
			if code := f.run(context.Background()); code != exitRaidWalkFailed {
				t.Fatalf("exit %d, want 18: %s\n%s", code, f.stderr.String(), f.stdout.String())
			}
			f.agent.mu.Lock()
			var stops []time.Time
			for i, l := range f.agent.gm {
				if l == ".bot run stop" {
					stops = append(stops, f.agent.gmAt[i])
				}
			}
			f.agent.mu.Unlock()
			if len(stops) != 2 {
				t.Fatalf("%d run stops %v, want the one before the re-run and the one after it", len(stops), stops)
			}
			// The second stop is the walk's own, right after the start that did not check out: the ending cleanup (60 s of pause
			// later) is no stop of its own, since the walk knows the run is stopped.
			if d := stops[1].Sub(rerunAt); d < 0 || d > 30*time.Second {
				t.Fatalf("the stop after the failed start came %v after it (stops %v, start %v): want the walk's own, not the cleanup's", d, stops, rerunAt)
			}
			sum := f.summary()
			if !hasNote(sum, fmt.Sprintf("the re-run at step %d failed", openStep)) || !hasNote(sum, tc.want) {
				t.Fatalf("notes %q, want the failed re-run naming %q", sum.Notes, tc.want)
			}
		})
	}
}

// When the stop after the failed re-run does not go out at all, the walk's ending cleanup sends another: the run counts as live from
// the moment `run start` was sent, so a stop that failed cannot leave it running with the cleanup believing it stopped.
func TestRaidWalkBossNotFoundRerunCleanupStopsARunTheRerunStopMissed(t *testing.T) {
	f, openStep, bossStep := zgArlokkFixture(t)
	zgArlokkPause(t, f, bossStep)
	starts := 0
	f.agent.onStart = func() {
		starts++
		if starts == 2 {
			f.agent.startCallErr = errors.New("commander run start: the call timed out after 25s")
		}
	}
	stops := 0
	f.agent.gmFail = func(line string) error {
		if line == ".bot run stop" {
			stops++
			if stops == 2 {
				return errors.New("the command was refused")
			}
		}
		return nil
	}
	if code := f.run(context.Background()); code != exitRaidWalkFailed {
		t.Fatalf("exit %d, want 18: %s\n%s", code, f.stderr.String(), f.stdout.String())
	}
	if n := countPrefix(f.agent.gmLines(), ".bot run stop"); n != 3 {
		t.Fatalf("%d run stops, want the one before the re-run, the refused one after it and the cleanup's", n)
	}
	sum := f.summary()
	if !hasNote(sum, "the run stop after the re-run: gm .bot run stop: the command was refused") || !hasNote(sum, "the walk's cleanup sends another") {
		t.Fatalf("notes %q, want the failed stop named", sum.Notes)
	}
	if !hasNote(sum, fmt.Sprintf("the re-run at step %d failed", openStep)) {
		t.Fatalf("notes %q, want the failed re-run", sum.Notes)
	}
}

// zgArlokkNotFoundPause is the run leader's pause at Arlokk's boss row: the frames show no unit of her entry, so the run
// leader cannot find her (walk 35's note; walk 37 b's at step 417).
func zgArlokkNotFoundPause(total, bossStep int) string {
	return fmt.Sprintf("TBM:RUN|zul_gurub|paused|%d/%d|high_priestess_arlokk|boss|begin|boss 14515 not found: not alive or dead within 30.0 yd of -11540.7 -1627.7 41.3 for 60 s nor anywhere in the instance (.bot run skip if he is already dead)|until step %d (step %d)",
		bossStep, total, bossStep+1, bossStep+1)
}

// zgArlokkRunning is the run's status at the gong row (the re-run's own start) with the walk's until.
func zgArlokkRunning(total, step, bossStep int) string {
	return fmt.Sprintf("TBM:RUN|zul_gurub|running|%d/%d|high_priestess_arlokk|open|begin|arrived|until step %d (step %d)", step, total, bossStep+1, bossStep+1)
}

// Walk 37 b: the re-run's own `.bot run stop` reaches the server after its `run start` went out, so the start's reply carries
// the OLD run's final line ("done ... stopped by .bot run stop") before the new run's first status. The walk took that line for
// the new run ("the new run is done"), sent its cleanup stop, killed the run it had just started, and the walk ended 0 (walk-super
// restarts only 18/19, so the raid sat dead). The new run's own running line at the open row decides; the old run's ending is no
// evidence about it.
func TestRaidWalkBossNotFoundRerunIgnoresTheOldRunsFinalLine(t *testing.T) {
	f, openStep, bossStep := zgArlokkFixture(t)
	total := zgArlokkPause(t, f, bossStep)
	f.opts.pausedWait = 60 * time.Second
	starts := 0
	f.agent.onStart = func() {
		starts++
		if starts != 2 {
			return
		}
		old := fmt.Sprintf("TBM:RUN|zul_gurub|done|%d/%d|high_priestess_arlokk|boss|begin|stopped by .bot run stop|until step %d (step %d)", bossStep, total, bossStep+1, bossStep+1)
		run := zgArlokkRunning(total, openStep, bossStep)
		f.agent.startReply = &commanderReply{Reply: tbm.Reply{Kind: tbm.KindAck, OK: true,
			Ack:   &tbm.Ack{Intent: "run start", Scope: "party", Count: 1, Executor: "-"},
			Line:  old,
			Lines: []string{old, run, "TBM:ACTION_ACK|run start|party|1|-"}}}
		// The stopped run's own ending is in the feed too, ahead of the new run's line; the walk must not take it for the
		// walk's end. Then the new run's status, and the walk's until.
		f.agent.push("TBM:RUN|-|none|0/0|-|-|-|no run", "")
		f.agent.push(old, "")
		f.agent.push(run, "")
		f.agent.push(fmt.Sprintf("TBM:RUN|zul_gurub|done|%d/%d|high_priestess_arlokk|move|walk|stopped before step %d (until step %d): the party stays where it stands", bossStep+1, total, bossStep+1, bossStep+1), "")
	}
	if code := f.run(context.Background()); code != exitOK {
		t.Fatalf("exit %d, want 0: %s\n%s", code, f.stderr.String(), f.stdout.String())
	}
	f.agent.mu.Lock()
	verbs := append([]string{}, f.agent.runVerbs...)
	f.agent.mu.Unlock()
	if got := strings.Join(startsAndSkips(verbs), ","); got != "start,start" {
		t.Fatalf("run verbs %q, want the run started again after the stop", got)
	}
	if n := countPrefix(f.agent.gmLines(), ".bot run stop"); n != 1 {
		t.Fatalf("%d run stops, want only the one before the re-run: the old run's ending is no failed start", n)
	}
	sum := f.summary()
	if sum.EndReason != "until" {
		t.Fatalf("end %q, want until", sum.EndReason)
	}
	if hasNote(sum, fmt.Sprintf("the re-run at step %d failed", openStep)) {
		t.Fatalf("notes %q: the old run's done line must not fail the re-run", sum.Notes)
	}
	if !hasNote(sum, fmt.Sprintf("restarted at step %d", openStep)) {
		t.Fatalf("notes %q, want the restart", sum.Notes)
	}
}

// Walk 37 b's ending, on the tool's own stop: a re-run that fails after its stop leaves the run stopped, and the tracker's own
// "stopped by .bot run stop" line then ended the walk 0. Walk-super restarts only 18/19, so the raid sat dead 67 minutes. A failed
// re-run ends the walk with the pause path's restartable code, whatever the run's own ending says.
func TestRaidWalkBossNotFoundRerunFailureEndsRestartable(t *testing.T) {
	f, openStep, bossStep := zgArlokkFixture(t)
	total := zgArlokkPause(t, f, bossStep)
	starts := 0
	f.agent.onStart = func() {
		starts++
		if starts != 2 {
			return
		}
		f.agent.startCallErr = errors.New("commander run start: the call timed out after 25s")
		// The stop after the failed start reaches the server: the run's own "done ... stopped by .bot run stop" lands in the
		// feed, where the tracker would otherwise end the walk clean.
		f.agent.push(fmt.Sprintf("TBM:RUN|zul_gurub|done|%d/%d|high_priestess_arlokk|boss|begin|stopped by .bot run stop|until step %d (step %d)", bossStep, total, bossStep+1, bossStep+1), "")
	}
	if code := f.run(context.Background()); code != exitRaidWalkFailed {
		t.Fatalf("exit %d, want 18: %s\n%s", code, f.stderr.String(), f.stdout.String())
	}
	sum := f.summary()
	if !hasNote(sum, fmt.Sprintf("the re-run at step %d failed", openStep)) {
		t.Fatalf("notes %q, want the failed re-run", sum.Notes)
	}
	if sum.EndReason == "" || strings.Contains(sum.EndReason, "stopped by") {
		t.Fatalf("end %q, want the failed re-run's own reason, not the run's stopped ending", sum.EndReason)
	}
}

// The stale-drop's other half: the new run's own ending at the paused step (a "stopped before step" until line) is not the
// stopped run's, so it ends the walk even when the start line came from the reply and the feed shows no further running line.
func TestRaidWalkBossNotFoundRerunKeepsTheNewRunsEndingAtThePausedStep(t *testing.T) {
	f, openStep, bossStep := zgArlokkFixture(t)
	total := zgArlokkPause(t, f, bossStep)
	starts := 0
	f.agent.onStart = func() {
		starts++
		if starts != 2 {
			return
		}
		run := zgArlokkRunning(total, openStep, bossStep)
		f.agent.startReply = &commanderReply{Reply: tbm.Reply{Kind: tbm.KindAck, OK: true,
			Ack:   &tbm.Ack{Intent: "run start", Scope: "party", Count: 1, Executor: "-"},
			Line:  run,
			Lines: []string{run, "TBM:ACTION_ACK|run start|party|1|-"}}}
		f.agent.push(fmt.Sprintf("TBM:RUN|zul_gurub|done|%d/%d|high_priestess_arlokk|move|walk|stopped before step %d (until step %d): the party stays where it stands", bossStep, total, bossStep+1, bossStep+1), "")
	}
	if code := f.run(context.Background()); code != exitOK {
		t.Fatalf("exit %d, want 0: %s\n%s", code, f.stderr.String(), f.stdout.String())
	}
	if sum := f.summary(); sum.EndReason != "until" {
		t.Fatalf("end %q, want until: the new run's own ending must end the walk", sum.EndReason)
	}
}

// The gong cannot be used twice in an instance whose encounter state still counts the boss started: the core's script returns
// before the goober's spell while the instance says IN_PROGRESS or DONE (boss_arlokk.cpp GOHello_go_gong_of_bethekk), and only
// the boss reaching home resets it. A re-run whose gong summons nothing (no unit of his alive in the frames since) is reported
// as such and ends the walk restartable, for a fresh instance, instead of burning the other re-runs and the pause.
func TestRaidWalkBossNotFoundRerunThatSummonsNothingEndsRestartable(t *testing.T) {
	f, openStep, bossStep := zgArlokkFixture(t)
	total := zgArlokkPause(t, f, bossStep)
	f.opts.pausedWait = 60 * time.Second
	starts := 0
	f.agent.onStart = func() {
		starts++
		if starts != 2 {
			return
		}
		run := zgArlokkRunning(total, openStep, bossStep)
		f.agent.startReply = &commanderReply{Reply: tbm.Reply{Kind: tbm.KindAck, OK: true,
			Ack:   &tbm.Ack{Intent: "run start", Scope: "party", Count: 1, Executor: "-"},
			Line:  run,
			Lines: []string{run, "TBM:ACTION_ACK|run start|party|1|-"}}}
		// The run walks on to the boss row and pauses again; the frames never show Arlokk alive (hostilesFn is nil).
		f.agent.push(zgArlokkNotFoundPause(total, bossStep), "")
	}
	if code := f.run(context.Background()); code != exitRaidWalkFailed {
		t.Fatalf("exit %d, want 18: %s\n%s", code, f.stderr.String(), f.stdout.String())
	}
	f.agent.mu.Lock()
	verbs := append([]string{}, f.agent.runVerbs...)
	f.agent.mu.Unlock()
	if got := strings.Join(startsAndSkips(verbs), ","); got != "start,start" {
		t.Fatalf("run verbs %q, want no second re-run after a gong that summoned nothing", got)
	}
	sum := f.summary()
	if !hasNote(sum, fmt.Sprintf("the re-run at step %d did not summon him again", openStep)) {
		t.Fatalf("notes %q, want the gong that summoned nothing", sum.Notes)
	}
	if !hasNote(sum, "a fresh instance is needed") {
		t.Fatalf("notes %q, want the fresh instance named", sum.Notes)
	}
}

// The other half of the same rule: when the frames DID show the boss alive after the re-run, the gong worked and the boss
// despawned again, so the next pause is another re-run (up to bossGongReruns), not the "summoned nothing" ending.
func TestRaidWalkBossNotFoundRerunThatSummonsTheBossAgainRerunsAgain(t *testing.T) {
	f, openStep, bossStep := zgArlokkFixture(t)
	total := zgArlokkPause(t, f, bossStep)
	f.opts.pausedWait = 60 * time.Second
	seen, paused := false, false
	f.agent.hostilesFn = func(int) []tbm.HostileRec {
		if !seen {
			return nil
		}
		if !paused {
			paused = true
			// The pause arrives after this frame, so the walk has the live sighting before it judges the gong.
			f.agent.push(zgArlokkNotFoundPause(total, bossStep), "")
		}
		return []tbm.HostileRec{{GUID: 0xF1300038B33D0D35, Entry: 14515, X: -11540.7, Y: -1627.7, Z: 41.3, HPPct: 100}}
	}
	starts := 0
	f.agent.onStart = func() {
		starts++
		run := zgArlokkRunning(total, openStep, bossStep)
		switch starts {
		case 2:
			seen = true
		case 3:
			// The second re-run: end the walk at its until.
			f.agent.push(fmt.Sprintf("TBM:RUN|zul_gurub|done|%d/%d|high_priestess_arlokk|move|walk|stopped before step %d (until step %d): the party stays where it stands", bossStep+1, total, bossStep+1, bossStep+1), "")
		default:
			return
		}
		f.agent.startReply = &commanderReply{Reply: tbm.Reply{Kind: tbm.KindAck, OK: true,
			Ack:   &tbm.Ack{Intent: "run start", Scope: "party", Count: 1, Executor: "-"},
			Line:  run,
			Lines: []string{run, "TBM:ACTION_ACK|run start|party|1|-"}}}
	}
	if code := f.run(context.Background()); code != exitOK {
		t.Fatalf("exit %d, want 0: %s\n%s", code, f.stderr.String(), f.stdout.String())
	}
	if starts != 3 {
		t.Fatalf("%d starts, want the first, the re-run and the second re-run", starts)
	}
	if hasNote(f.summary(), "did not summon him again") {
		t.Fatalf("notes %q: a boss seen alive after the re-run means the gong worked", f.summary().Notes)
	}
}

// With the plan's annotation gone, the same pause is no re-run: the gong row still stands right before Arlokk's boss row, but
// nothing says it summons her, so the walk waits the pause out as before (the rows' shape is no evidence).
func TestRaidWalkBossNotFoundWithoutTheAnnotationDoesNotRerunTheOpen(t *testing.T) {
	raw, err := os.ReadFile("../../dungeons/zul_gurub.json")
	if err != nil {
		t.Fatal(err)
	}
	var keep []string
	for _, l := range strings.Split(string(raw), "\n") {
		if strings.Contains(l, `"summon_open_step"`) {
			// It is the boss object's last field: the line before it loses the comma that held the object open.
			if n := len(keep); n > 0 {
				keep[n-1] = strings.TrimSuffix(keep[n-1], ",")
			}
			continue
		}
		keep = append(keep, l)
	}
	dir := t.TempDir()
	plan := filepath.Join(dir, "zul_gurub.json")
	if err := os.WriteFile(plan, []byte(strings.Join(keep, "\n")), 0o600); err != nil {
		t.Fatal(err)
	}
	p, err := dungeon.Load(plan)
	if err != nil {
		t.Fatal(err)
	}
	for i := range p.Legs {
		if b := p.Legs[i].Boss; b != nil && b.SummonOpenStep != 0 {
			t.Fatalf("the plan copy still carries summon_open_step %d on boss %d", b.SummonOpenStep, b.Entry)
		}
	}
	f := newFixture(t)
	f.opts.dir = dir
	f.opts.plan, f.opts.holder = "zul_gurub", "raid-walk-zul_gurub"
	bossStep := 0
	for i, s := range zgRows(t).Steps {
		if s.Kind == planexport.KindBoss && s.Entry == 14515 {
			bossStep = i + 1
		}
	}
	if bossStep == 0 {
		t.Fatal("no Arlokk boss row in the ZG rows")
	}
	f.opts.until = fmt.Sprintf("step %d", bossStep+1)
	f.agent.instMap = p.Map
	f.agent.enterMap = p.Map
	f.agent.ttFn = ttUnitsFn(t)
	zgArlokkPause(t, f, bossStep)
	if code := f.run(context.Background()); code != exitRaidWalkFailed {
		t.Fatalf("exit %d, want 18: %s\n%s", code, f.stderr.String(), f.stdout.String())
	}
	f.agent.mu.Lock()
	verbs := append([]string{}, f.agent.runVerbs...)
	f.agent.mu.Unlock()
	if got := strings.Join(verbs, ","); got != "start" {
		t.Fatalf("run verbs %q: an un-annotated boss must not be re-run", got)
	}
	sum := f.summary()
	if hasNote(sum, "re-running step") {
		t.Fatalf("notes %q: the walk re-ran an open row no plan field names", sum.Notes)
	}
	if !hasNote(sum, "boss 14515 not found and no kill in the frames yet") {
		t.Fatalf("notes %q, want the pause path's re-reading note", sum.Notes)
	}
}

// sinceAgent serves events by the cursor the walk asks from (the real events RPC's `since` is exclusive), so a wait that
// leaves the page's tail unread can be seen to lose it.
type sinceAgent struct {
	evs []events.Event
}

func (a *sinceAgent) Call(_ context.Context, method string, params, out any, _ time.Duration) error {
	if method != "events" {
		return errors.New("unexpected RPC " + method)
	}
	since, _ := params.(map[string]any)["since"].(uint64)
	page, ok := out.(*events.Page)
	if !ok {
		return errors.New("events: want *events.Page")
	}
	*page = events.Page{}
	for _, e := range a.evs {
		if e.Seq > since {
			page.Events = append(page.Events, e)
			page.Next = e.Seq
		}
	}
	return nil
}

// A wait that matches a line in the middle of an events page must not lose the page's tail: a run's done line right after
// its start line is the walk's end, and the re-run's wait reads the start line from the feed when the reply has none (review
// of walk 37 b). waitTBMMatch puts the cursor back after the matched line, so the next poll reads the tail.
func TestWaitTBMMatchKeepsThePageTail(t *testing.T) {
	clk := newFakeClock()
	ag := &sinceAgent{evs: []events.Event{
		{Seq: 1, Kind: events.KindSystem, Text: "TBM:RUN|zul_gurub|running|415/761|high_priestess_arlokk|open|begin|arrived|until step 418 (step 418)"},
		{Seq: 2, Kind: events.KindSystem, Text: "TBM:RUN|zul_gurub|done|418/761|high_priestess_arlokk|move|walk|stopped before step 418 (until step 418)"},
	}}
	w := &walk{agent: ag, env: walkEnv{now: clk.now, sleep: clk.sleep}}
	line, ok, err := w.waitTBMMatch(context.Background(), 5*time.Second, func(l string) bool {
		st, ok := raidwalk.ParseRun(l)
		return ok && st.State == tbm.RunRunning
	})
	if err != nil || !ok || !strings.Contains(line, "running|415") {
		t.Fatalf("wait: line %q ok %v err %v, want the running line", line, ok, err)
	}
	if w.eventsAt != 1 {
		t.Fatalf("eventsAt %d, want 1 (after the matched line)", w.eventsAt)
	}
	lines, err := w.pump(context.Background(), 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(lines) != 1 || !strings.Contains(lines[0], "done|418") {
		t.Fatalf("next pump %q, want the page's done line", lines)
	}
}

// Walk 80 (#446): a failed attempt's late joiners stay in the group, and the run start refuses while the whole party
// is not in the lead bot's instance (exit 18: attempt 2's fresh placement), which sent attempt 3 into a bind resume.
// phaseHire dismisses every group member this walk did not hire, before the batches: one `.bot uninvite` per stray,
// none for a hire of this walk, and the dismissed names are gone from the party the hires join.
func TestRaidWalkDismissesStrayGroupMembersBeforeHiring(t *testing.T) {
	f := newFixture(t)
	f.agent.mu.Lock()
	f.agent.party = []string{"Straya", "Strayb"}
	f.agent.mu.Unlock()
	f.defaultRun("onyxias_lair")
	if code := f.run(context.Background()); code != exitOK {
		t.Fatalf("exit %d, want 0: %s", code, f.stderr.String())
	}
	var dismissed []string
	for _, line := range f.agent.gmLines() {
		if name, ok := strings.CutPrefix(line, ".bot uninvite "); ok {
			dismissed = append(dismissed, name)
		}
	}
	if len(dismissed) != 2 || dismissed[0] != "Straya" || dismissed[1] != "Strayb" {
		t.Fatalf("dismissed %v, want the two strays in party order: %v", dismissed, f.agent.gmLines())
	}
	sum := f.summary()
	if !hasNote(sum, "hire: dismissed Straya, a group member this walk did not hire") {
		t.Fatalf("notes %q, want the dismissal of Straya", sum.Notes)
	}
	f.agent.mu.Lock()
	party := append([]string{}, f.agent.party...)
	f.agent.mu.Unlock()
	if len(party) != raidwalk.HireCap {
		t.Fatalf("party holds %d bots, want the full roster of %d after the strays were dismissed: %v", len(party), raidwalk.HireCap, party)
	}
	for _, n := range party {
		if n == "Straya" || n == "Strayb" {
			t.Fatalf("%s is still in the party after its dismissal: %v", n, party)
		}
	}
}

// The module answers `.bot uninvite` on the server's next group update: the first party read after the lines still
// holds the stray. phaseHire must wait that read out before it reads `before`, or the join poll's want counts a
// member who is already leaving and times out with "N of M hires joined" (the walk 80 failure, round 1).
func TestRaidWalkWaitsForTheDismissedStraysToLeave(t *testing.T) {
	f := newFixture(t)
	f.agent.mu.Lock()
	f.agent.party = []string{"Straya", "Strayb"}
	f.agent.uninviteLag = map[string]int{"Straya": 1, "Strayb": 1}
	f.agent.mu.Unlock()
	f.defaultRun("onyxias_lair")
	if code := f.run(context.Background()); code != exitOK {
		t.Fatalf("exit %d, want 0: %s\n%s", code, f.stderr.String(), f.stdout.String())
	}
	f.agent.mu.Lock()
	var lastUninvite, firstHire time.Time
	for i, l := range f.agent.gm {
		if strings.HasPrefix(l, ".bot uninvite ") {
			lastUninvite = f.agent.gmAt[i]
		}
		if strings.HasPrefix(l, ".bot hire raid ") && firstHire.IsZero() {
			firstHire = f.agent.gmAt[i]
		}
	}
	party := append([]string{}, f.agent.party...)
	f.agent.mu.Unlock()
	if lastUninvite.IsZero() || firstHire.IsZero() {
		t.Fatalf("no uninvite or no hire line: %v", f.agent.gmLines())
	}
	if d := firstHire.Sub(lastUninvite); d < time.Second {
		t.Fatalf("the first hire went out %s after the last uninvite, want the dismissal read back first", d)
	}
	if len(party) != raidwalk.HireCap {
		t.Fatalf("party holds %d bots, want the full roster of %d: %v", len(party), raidwalk.HireCap, party)
	}
	for _, n := range party {
		if n == "Straya" || n == "Strayb" {
			t.Fatalf("%s is still in the party: %v", n, party)
		}
	}
	if reason := f.summary().EndReason; strings.Contains(reason, "joined in") {
		t.Fatalf("end reason %q, want no join timeout: the wait must read the strays out before the join poll", reason)
	}
}

// A stray that outlives the 15 s wait (a name the module cannot uninvite, a real player's) stops phaseHire before a
// single hire line, with the names still in the group: the join poll cannot count a party that holds them.
func TestRaidWalkStopsWhenAStrayIsNotDismissed(t *testing.T) {
	f := newFixture(t)
	f.agent.mu.Lock()
	f.agent.party = []string{"Straya", "Stranger"}
	f.agent.uninviteRefused = map[string]bool{"Stranger": true}
	f.agent.mu.Unlock()
	f.defaultRun("onyxias_lair")
	if code := f.run(context.Background()); code != exitRaidWalkFailed {
		t.Fatalf("exit %d, want %d: %s\n%s", code, exitRaidWalkFailed, f.stderr.String(), f.stdout.String())
	}
	sum := f.summary()
	if want := "stray group members not dismissed: Stranger"; !strings.Contains(sum.EndReason, want) {
		t.Fatalf("end reason %q, want %q", sum.EndReason, want)
	}
	if hasNote(sum, "dismissed Stranger") {
		t.Fatalf("notes %q: the stray that stayed was logged as dismissed", sum.Notes)
	}
	if !hasNote(sum, "dismissed Straya") {
		t.Fatalf("notes %q, want the dismissal of Straya, which did leave", sum.Notes)
	}
	for _, l := range f.agentLog() {
		if strings.HasPrefix(l, "gm .bot hire raid ") {
			t.Fatalf("a hire line went out while a stray was still in the party: %q", l)
		}
	}
}
