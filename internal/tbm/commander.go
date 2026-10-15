package tbm

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/pjerra/turtle-agent/internal/events"
	"github.com/pjerra/turtle-agent/internal/packet"
)

// The remote commander (TortoiseBots issue #884, docs/guides/player-controls.md section 1a): a player leads a bot-only party
// without being in it. `.bot commander bind|unbind|status|view` manage the binding, `.bot cmd <verb> ...` give the orders. The
// replies are the usual TBM:ACTION_ACK / TBM:ACTION_ERR lines with the intent `commander <verb>` or `cmd <verb>`.

// Intents of the commander commands (the first two words of the command).
var (
	CommanderVerbs = []string{"bind", "unbind", "status", "view"}
	CmdVerbs       = []string{"attack", "pull", "pullback", "cc", "focus", "move", "stay", "follow", "rest", "revive", "summon", "enter", "loot", "open", "use", "ritual", "lootmethod", "state"}
)

// CCMarks are the raid marks `.bot cmd cc` takes.
var CCMarks = []string{"skull", "cross", "square", "moon", "triangle", "diamond", "circle", "star"}

// argRe is what one argument of a command may be: names, numbers, a GUID, a mark, an object by entry (entry:<id>@x,y,z). No spaces, no control characters, no way to
// put a second command on the line.
var argRe = regexp.MustCompile(`^[A-Za-z0-9_,:+\-][A-Za-z0-9_.,:+@\-]{0,63}$`)

// Link is the commander's way to the game: the say chat the commands go out on, and the event log the replies come back in.
type Link interface {
	// Say sends the line as say chat (".bot ..." runs as a command).
	Say(ctx context.Context, text string) error
	Log() *events.Log
}

// DefaultOrderWait is how long an order waits for its ACK or ERR.
const DefaultOrderWait = 4 * time.Second

// Commander sends the commander commands and reads their replies. One command is in flight at a time.
type Commander struct {
	link Link
	// Wait is how long a command waits for its reply (DefaultOrderWait when 0).
	Wait time.Duration
	// PendingSlack is what an order that reports itself running (ACTION_PENDING) may take beyond its budget (DefaultPendingSlack when 0).
	PendingSlack time.Duration

	mu sync.Mutex
}

// NewCommander returns a commander over link.
func NewCommander(link Link) *Commander { return &Commander{link: link} }

// Intent is the reply intent of a command line: its first two words ("cmd move", "commander bind"), lower-cased.
func Intent(command string) string {
	f := strings.Fields(strings.TrimPrefix(strings.TrimSpace(command), ".bot "))
	if len(f) < 2 {
		return ""
	}
	return strings.ToLower(f[0] + " " + f[1])
}

// CheckCommand refuses a command that is not a commander command (commander, cmd or run) or carries an unsafe argument. command is the line without
// ".bot " ("cmd move 1 2 3").
func CheckCommand(command string) error {
	for _, r := range command {
		if r < 0x20 || r == 0x7f {
			return errors.New("a command is one line: no control characters")
		}
	}
	f := strings.Fields(command)
	if len(f) < 2 {
		return errors.New(`a commander command is "commander <bind|unbind|status|view> ..." or "cmd <verb> ..."`)
	}
	group, verb := strings.ToLower(f[0]), strings.ToLower(f[1])
	var verbs []string
	switch group {
	case "commander":
		verbs = CommanderVerbs
	case "cmd":
		verbs = CmdVerbs
	case "run":
		verbs = RunVerbs
	default:
		return fmt.Errorf("%q is not a commander command: use commander, cmd or run", f[0])
	}
	found := false
	for _, v := range verbs {
		found = found || v == verb
	}
	if !found {
		return fmt.Errorf("%s %s: unknown verb (%s)", group, verb, strings.Join(verbs, ", "))
	}
	for _, a := range f[2:] {
		if !argRe.MatchString(a) {
			return fmt.Errorf("argument %q: letters, digits and _ . , : + - only, not starting with a dot", a)
		}
	}
	return nil
}

// Do sends ".bot <command>" and waits for the ACK or ERR of its intent. command is "cmd move 1 2 3" or "commander bind Tanky".
// A reply that does not come is Kind timeout, not an error: the error is for a command that could not be sent.
func (c *Commander) Do(ctx context.Context, command string) (Reply, error) {
	return c.DoWait(ctx, command, c.Wait)
}

// DoWait is Do with its own wait for the reply (DefaultOrderWait when 0).
func (c *Commander) DoWait(ctx context.Context, command string, wait time.Duration) (Reply, error) {
	command = strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(command), ".bot "))
	if err := CheckCommand(command); err != nil {
		return Reply{Kind: KindErr, Lines: []string{}}, err
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	log := c.link.Log()
	since := log.Last()
	if err := c.link.Say(ctx, Chat(command)); err != nil {
		return Reply{Lines: []string{}}, err
	}
	if wait <= 0 {
		wait = DefaultOrderWait
	}
	return ReadReply(ctx, log, since, Intent(command), wait), nil
}

// ReadReply waits up to wait for the ACK or ERR of intent in the log after since. Every TBM: line and system line read on the way
// is kept in the reply's Lines (at most 60).
func ReadReply(ctx context.Context, log *events.Log, since uint64, intent string, wait time.Duration) Reply {
	wctx, cancel := context.WithTimeout(ctx, wait)
	defer cancel()
	r := Reply{Lines: []string{}}
	cursor := since
	for {
		p := log.Read(events.Query{Since: cursor, Kinds: []string{events.KindSystem, events.KindAddon}, Limit: events.MaxLimit})
		for _, e := range p.Events {
			line, ok := LineOf(e)
			if !ok {
				continue
			}
			if strings.HasPrefix(line, ViewPrefix) {
				continue // the feed is not a reply, and it would drown the lines
			}
			if len(r.Lines) < 60 {
				r.Lines = append(r.Lines, line)
			}
			if a, ok := ParseAck(line); ok && strings.EqualFold(a.Intent, intent) {
				r.Kind, r.OK, r.Line, r.Ack = KindAck, true, line, &a
				return r
			}
			if a, ok := parseLooseAck(line); ok && strings.EqualFold(a.Intent, intent) {
				r.Kind, r.OK, r.Line, r.Ack = KindAck, true, line, &a
				return r
			}
			if er, ok := ParseErr(line); ok && strings.EqualFold(er.Intent, intent) {
				r.Kind, r.Line, r.Err = KindErr, line, &er
				return r
			}
		}
		cursor = p.Next
		if p.More {
			continue
		}
		if log.Wait(wctx, cursor) != nil {
			r.Kind = KindTimeout
			return r
		}
	}
}

// parseLooseAck reads an ACTION_ACK whose scope is neither party nor bot:<name> (`.bot run list` answers with the scope "plans"): the
// addon would drop it, but the line is still the answer to its command.
func parseLooseAck(line string) (Ack, bool) {
	rest, ok := strings.CutPrefix(line, "TBM:ACTION_ACK|")
	if !ok {
		return Ack{}, false
	}
	f := strings.Split(rest, "|")
	if len(f) < 3 || len(f) > 4 || strings.TrimSpace(f[0]) == "" || strings.TrimSpace(f[1]) == "" {
		return Ack{}, false
	}
	n, err := strconv.Atoi(f[2])
	if err != nil || n < 0 {
		return Ack{}, false
	}
	a := Ack{Intent: f[0], Scope: f[1], Count: n, Executor: "-"}
	if len(f) == 4 && strings.TrimSpace(f[3]) != "" {
		a.Executor = f[3]
	}
	return a, true
}

// LineOf is the TBM: line or system line an event carries. A system message that is no TBM line is returned as its text (the
// listing of `commander status`); the module's addon channel carries its lines without the "TBM:".
func LineOf(e events.Event) (string, bool) {
	if l, ok := Line(e); ok {
		return l, true
	}
	switch {
	case e.Kind == events.KindAddon && e.Data["prefix"] == Prefix && e.Text != "":
		return "TBM:" + e.Text, true
	case e.Kind == events.KindSystem && e.Text != "":
		return e.Text, true
	}
	return "", false
}

// Bind binds lead (and the named bots) as the commanded party.
func (c *Commander) Bind(ctx context.Context, lead string, bots ...string) (Reply, error) {
	return c.Do(ctx, strings.Join(append([]string{"commander", "bind", lead}, bots...), " "))
}

// Unbind ends the binding.
func (c *Commander) Unbind(ctx context.Context) (Reply, error) { return c.Do(ctx, "commander unbind") }

// Status asks for the commanded party.
func (c *Commander) Status(ctx context.Context) (Reply, error) { return c.Do(ctx, "commander status") }

// View switches the perception feed of the current binding.
func (c *Commander) View(ctx context.Context, on bool) (Reply, error) {
	if on {
		return c.Do(ctx, "commander view on")
	}
	return c.Do(ctx, "commander view off")
}

// Cmd sends `.bot cmd <verb> <args...>`.
func (c *Commander) Cmd(ctx context.Context, verb string, args ...string) (Reply, error) {
	return c.Do(ctx, strings.Join(append([]string{"cmd", verb}, args...), " "))
}

// GUIDArg is a GUID as `.bot cmd` takes it.
func GUIDArg(g packet.GUID) string { return fmt.Sprintf("0x%X", uint64(g)) }

// EntryArg names a game object (or, for `cmd loot`, a corpse) by its entry as `.bot cmd open|loot|use|state` takes it: `entry:<id>`, or
// `entry:<id>@x,y,z` with the point to search around (the server picks the nearest of that entry within 30 yd of it). The point is
// written with commas and no spaces, which the module's parser wants.
func EntryArg(entry uint32, at *[3]float32) string {
	if at == nil {
		return fmt.Sprintf("entry:%d", entry)
	}
	return fmt.Sprintf("entry:%d@%s,%s,%s", entry, CoordArg(at[0]), CoordArg(at[1]), CoordArg(at[2]))
}

// CoordArg formats a coordinate for `.bot cmd move`.
func CoordArg(v float32) string { return fmt.Sprintf("%.2f", v) }

// BindingEnd is an automatic end of the binding the module announced: TBM:ACTION_ACK|commander unbind|party|<n>|<reason>.
type BindingEnd struct {
	Reason string    `json:"reason"`
	Count  int       `json:"count"`
	At     time.Time `json:"at"`
}

// FeedEvent receives the throttled bot_view_frame events.
type FeedEvent func(kind, text string, data map[string]any)

// ViewFeed follows an event log for TBM:VIEW lines, assembles them into frames and keeps the newest. It also notes when the
// module ends the binding. Run it in a goroutine; read it from any.
type ViewFeed struct {
	log *events.Log
	now func() time.Time

	mu      sync.Mutex
	asm     *ViewAssembler
	run     RunStatus // the newest TBM:RUN line
	runN    uint64    // TBM:RUN lines read
	latest  ViewFrame
	have    bool
	n       uint64 // frames published since the feed started (a Reset does not zero it)
	ends    []BindingEnd
	changed chan struct{}

	emit        FeedEvent
	emitEvery   time.Duration
	lastEmit    time.Time
	lastEmitSig string
}

// DefaultEventEvery is how often at most a bot_view_frame event is logged when nothing important changed.
const DefaultEventEvery = 5 * time.Second

// minEventGap is the shortest gap between two bot_view_frame events, whatever changed.
const minEventGap = time.Second

// NewViewFeed returns a feed over log. emit may be nil (no events); every is the quiet interval of the events (DefaultEventEvery
// when 0).
func NewViewFeed(log *events.Log, emit FeedEvent, every time.Duration) *ViewFeed {
	if every <= 0 {
		every = DefaultEventEvery
	}
	return &ViewFeed{log: log, now: time.Now, asm: NewViewAssembler(), changed: make(chan struct{}), emit: emit, emitEvery: every}
}

// SetClock replaces the feed's clock (tests).
func (f *ViewFeed) SetClock(now func() time.Time) { f.now = now }

// Run follows the log from its end until ctx ends.
func (f *ViewFeed) Run(ctx context.Context) {
	cursor := f.log.Last()
	for {
		if f.log.Wait(ctx, cursor) != nil {
			return
		}
		for {
			p := f.log.Read(events.Query{Since: cursor, Kinds: []string{events.KindSystem, events.KindAddon}, Limit: events.MaxLimit})
			for _, e := range p.Events {
				f.Process(e)
			}
			cursor = p.Next
			if !p.More {
				break
			}
		}
	}
}

// Process folds one event into the feed.
func (f *ViewFeed) Process(e events.Event) {
	line, ok := Line(e)
	if !ok && e.Kind == events.KindAddon && e.Data["prefix"] == Prefix {
		line, ok = "TBM:"+e.Text, true
	}
	if !ok {
		return
	}
	f.ProcessLine(line)
}

// ProcessLine folds one TBM: line into the feed.
func (f *ViewFeed) ProcessLine(line string) {
	if a, ok := ParseAck(line); ok && strings.EqualFold(a.Intent, "commander unbind") {
		f.mu.Lock()
		f.ends = append(f.ends, BindingEnd{Reason: a.Executor, Count: a.Count, At: f.now()})
		f.wake()
		f.mu.Unlock()
		return
	}
	if st, ok := ParseRunLine(line); ok {
		f.mu.Lock()
		f.run, f.runN = st, f.runN+1
		f.wake()
		f.mu.Unlock()
		return
	}
	if !strings.HasPrefix(line, ViewPrefix) {
		return
	}
	f.mu.Lock()
	fr, done := f.asm.Feed(line)
	var ev func()
	if done {
		fr.At = f.now()
		f.latest, f.have = *fr, true
		f.n++
		ev = f.eventFor(*fr)
		f.wake()
	}
	f.mu.Unlock()
	if ev != nil {
		ev()
	}
}

func (f *ViewFeed) wake() {
	close(f.changed)
	f.changed = make(chan struct{})
}

// Latest is the newest frame and how many frames the feed has published; ok is false before the first one (or after a Reset).
func (f *ViewFeed) Latest() (fr ViewFrame, n uint64, ok bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.latest, f.n, f.have
}

// RunLatest is the newest TBM:RUN line and how many the feed has read; ok is false before the first.
func (f *ViewFeed) RunLatest() (st RunStatus, n uint64, ok bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.run, f.runN, f.runN > 0
}

// Stats are the assembler's counters.
func (f *ViewFeed) Stats() ViewStats {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.asm.Stats()
}

// Ends are the automatic ends of the binding seen so far.
func (f *ViewFeed) Ends() []BindingEnd {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]BindingEnd{}, f.ends...)
}

// Reset forgets the frame held and the seq counting: a new binding starts at seq 1.
func (f *ViewFeed) Reset() {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.asm.Reset()
	f.have = false
	f.lastEmit, f.lastEmitSig = time.Time{}, ""
	f.wake()
}

// WaitNew waits until the feed has published more than after frames (n of Latest) and returns the newest, or until ctx ends.
func (f *ViewFeed) WaitNew(ctx context.Context, after uint64) (ViewFrame, uint64, error) {
	for {
		f.mu.Lock()
		if f.n > after && f.have {
			fr, n := f.latest, f.n
			f.mu.Unlock()
			return fr, n, nil
		}
		ch := f.changed
		f.mu.Unlock()
		select {
		case <-ch:
		case <-ctx.Done():
			return ViewFrame{}, 0, ctx.Err()
		}
	}
}

// eventFor returns the event to log for the frame, or nil when the throttle holds it back. Called with the lock held.
func (f *ViewFeed) eventFor(fr ViewFrame) func() {
	if f.emit == nil {
		return nil
	}
	now := f.now()
	sig := frameSignature(fr)
	since := now.Sub(f.lastEmit)
	if !f.lastEmit.IsZero() && (since < minEventGap || (sig == f.lastEmitSig && since < f.emitEvery)) {
		return nil
	}
	f.lastEmit, f.lastEmitSig = now, sig
	data := FrameSummary(fr)
	st := f.asm.Stats()
	data["frames"], data["missed"], data["invalid"] = st.Frames, st.Missed, st.Invalid
	text := fmt.Sprintf("view %d: %d bots, %d hostiles (%d engaged)", fr.Seq, len(fr.Party), len(fr.Hostiles), len(fr.EngagedHostiles()))
	emit := f.emit
	return func() { emit(events.KindBotViewFrame, text, data) }
}

// frameSignature changes when something worth an event happens: a bot dies or is raised, the party enters or leaves combat, or
// hostiles appear or all go.
func frameSignature(fr ViewFrame) string {
	dead := 0
	for _, p := range fr.Party {
		if p.Dead {
			dead++
		}
	}
	return fmt.Sprintf("%d/%d/%v/%v", len(fr.Party), dead, fr.PartyInCombat(), len(fr.Hostiles) > 0)
}

// FrameSummary is the compact form of a frame for events: the party with health and power, the hostiles and their threat holder.
func FrameSummary(fr ViewFrame) map[string]any {
	party := make([]map[string]any, 0, len(fr.Party))
	for _, p := range fr.Party {
		party = append(party, map[string]any{"name": p.Name, "guid": p.GUID, "hp_pct": p.HPPct(), "power_pct": p.PowerPct(), "dead": p.Dead, "state": p.State})
	}
	hostiles := make([]map[string]any, 0, len(fr.Hostiles))
	for _, h := range fr.Hostiles {
		m := map[string]any{"guid": h.GUID, "entry": h.Entry, "hp_pct": h.HPPct, "flags": h.Flags}
		if g, ok := fr.ThreatHolder(h.GUID); ok {
			m["threat_holder"] = g
		}
		hostiles = append(hostiles, m)
	}
	return map[string]any{"seq": fr.Seq, "lead": fr.Lead, "party": party, "hostiles": hostiles, "engaged": len(fr.EngagedHostiles()), "in_combat": fr.PartyInCombat()}
}
