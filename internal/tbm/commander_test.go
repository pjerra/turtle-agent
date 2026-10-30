package tbm

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/pjerra/turtle-agent/internal/events"
)

// fakeLink answers every command through reply (the module): the lines it returns are logged as system messages.
type fakeLink struct {
	log   *events.Log
	mu    sync.Mutex
	sent  []string
	reply func(text string) []string
	// sayHook, when set, runs after each line sent: it appends whatever events the test wants.
	sayHook func(text string)
}

func newFakeLink() *fakeLink { return &fakeLink{log: events.New(1000)} }

func (l *fakeLink) Log() *events.Log { return l.log }

func (l *fakeLink) Say(ctx context.Context, text string) error {
	l.mu.Lock()
	l.sent = append(l.sent, text)
	l.mu.Unlock()
	if l.sayHook != nil {
		l.sayHook(text)
	}
	if l.reply != nil {
		for _, line := range l.reply(text) {
			l.log.Append(events.Event{Kind: events.KindSystem, Text: line, Data: map[string]any{"raw": line}})
		}
	}
	return nil
}

func TestCheckCommand(t *testing.T) {
	for _, ok := range []string{"commander bind Tanky Healy", "cmd move 1.5 -2 3", "cmd attack 0xF130000C5E0012AB", "cmd cc moon 0x1 Mage", "commander view on", "CMD REST"} {
		if err := CheckCommand(ok); err != nil {
			t.Errorf("%q: %v", ok, err)
		}
	}
	for _, bad := range []string{"", "cmd", "action attack", "cmd dance", "commander bind Tanky\nsay hi", "cmd attack 0x1 .gm on", "cmd move 1 2 $3", "cmd attack " + strings.Repeat("a", 65)} {
		if err := CheckCommand(bad); err == nil {
			t.Errorf("%q accepted", bad)
		}
	}
}

// dungeon_stage appends speed= to the run start it sends: the one `=` word
// the run grammar takes (1.0 to 3.0, one decimal). No other `=` word passes,
// and resume takes none.
func TestCheckCommandRunStartSpeed(t *testing.T) {
	for _, ok := range []string{
		"run start zulfarrak from leg gahzrilla speed=3.0",
		"run start zulfarrak from leg gahzrilla speed=2",
	} {
		if err := CheckCommand(ok); err != nil {
			t.Errorf("%q: %v", ok, err)
		}
	}
	for _, bad := range []string{
		"run start zulfarrak from leg gahzrilla speed=9.0",
		"run start zulfarrak from leg gahzrilla speed=3.0x",
		"run resume zulfarrak speed=3.0",
		"run start zulfarrak foo=1",
	} {
		if err := CheckCommand(bad); err == nil {
			t.Errorf("%q accepted", bad)
		}
	}
}

// The server's run recorder is the one `.bot` group outside commander/cmd/run that a run sends (run_meta.server_trace): exactly its
// start, its stop and the status the stop's retries read. Every other `.bot` line is still refused.
func TestCheckCommandTrace(t *testing.T) {
	// The driver word carries 1-96 characters after driver= (the server stores the value, at most 96: dungeon.RunMeta.Check); a run id is
	// the commander's own argument.
	long := strings.Repeat("a", 96)
	for _, ok := range []string{"trace start r-20261020T095109-ab12 driver=deepseek:v4.1-flash:wc-s2", "trace stop", "trace status", "TRACE Stop", "trace start r driver=" + long} {
		if err := CheckCommand(ok); err != nil {
			t.Errorf("%q: %v", ok, err)
		}
	}
	for _, bad := range []string{"trace", "trace foo", "trace start", "trace start r;say hi", "trace stop now", "trace stop driver=x", "maintenance Tanky", "autogear Tanky blue", "inv Tanky", "logout Tanky", "trace start r driver=a b", "trace start r driver=" + long + "a", "trace start r notdriver=1", "trace start driver=x", "trace start r driver=bad/path"} {
		if err := CheckCommand(bad); err == nil {
			t.Errorf("%q accepted", bad)
		}
	}
}

func TestIntent(t *testing.T) {
	for in, want := range map[string]string{"cmd move 1 2 3": "cmd move", ".bot commander bind A B": "commander bind", "cmd": "", "CMD Rest": "cmd rest"} {
		if got := Intent(in); got != want {
			t.Errorf("%q: %q", in, got)
		}
	}
}

func TestDoAckErrTimeout(t *testing.T) {
	l := newFakeLink()
	l.reply = func(text string) []string {
		switch {
		case strings.Contains(text, "commander bind"):
			return []string{"Commander bound.", "TBM:ACTION_ACK|commander bind|party|5|Tanky"}
		case strings.Contains(text, "cmd pullback"):
			return []string{"TBM:ACTION_ERR|cmd pullback|no-tank|No tank in the party"}
		case strings.Contains(text, "cmd move"):
			// An ack of another intent does not answer this command.
			return []string{"TBM:ACTION_ACK|cmd stay|party|5|Tanky"}
		}
		return nil
	}
	c := NewCommander(l)
	c.Wait = 50 * time.Millisecond
	r, err := c.Bind(context.Background(), "Tanky", "Healy", "Mage")
	if err != nil || r.Kind != KindAck || r.Ack == nil || r.Ack.Count != 5 || r.Ack.Executor != "Tanky" || !r.OK {
		t.Fatalf("%+v %v", r, err)
	}
	if got := l.sent[0]; got != ".bot commander bind Tanky Healy Mage" {
		t.Fatalf("sent %q", got)
	}
	if len(r.Lines) != 2 || r.Lines[0] != "Commander bound." {
		t.Fatalf("lines %v", r.Lines)
	}
	r, err = c.Cmd(context.Background(), "pullback", GUIDArg(0xF130000C5E0012AB), "10")
	if err != nil || r.Kind != KindErr || r.Err == nil || r.Err.Code != "no-tank" || r.OK {
		t.Fatalf("%+v %v", r, err)
	}
	if got := l.sent[1]; got != ".bot cmd pullback 0xF130000C5E0012AB 10" {
		t.Fatalf("sent %q", got)
	}
	r, err = c.Cmd(context.Background(), "move", CoordArg(1), CoordArg(2), CoordArg(3.456))
	if err != nil || r.Kind != KindTimeout || r.OK {
		t.Fatalf("%+v %v", r, err)
	}
	if got := l.sent[2]; got != ".bot cmd move 1.00 2.00 3.46" {
		t.Fatalf("sent %q", got)
	}
	if _, err := c.Do(context.Background(), "cmd attack 0x1; .gm on"); err == nil || len(l.sent) != 3 {
		t.Fatalf("an unsafe command was sent: %v %v", err, l.sent)
	}
}

func TestDoIgnoresRepliesBeforeTheSend(t *testing.T) {
	l := newFakeLink()
	l.log.Append(events.Event{Kind: events.KindSystem, Text: "TBM:ACTION_ACK|cmd rest|party|5|Tanky"}) // an old reply
	c := NewCommander(l)
	c.Wait = 30 * time.Millisecond
	if r, _ := c.Cmd(context.Background(), "rest"); r.Kind != KindTimeout {
		t.Fatalf("an old ACK answered a new order: %+v", r)
	}
}

func TestDoViaAddonChannel(t *testing.T) {
	l := newFakeLink()
	l.reply = func(string) []string { return nil }
	c := NewCommander(l)
	c.Wait = time.Second
	go func() {
		time.Sleep(20 * time.Millisecond)
		l.log.Append(events.Event{Kind: events.KindAddon, Text: "ACTION_ACK|cmd stay|party|5|Tanky", Data: map[string]any{"prefix": "TBM"}})
	}()
	if r, _ := c.Cmd(context.Background(), "stay"); r.Kind != KindAck {
		t.Fatalf("%+v", r)
	}
}

func viewEvents(lines []string) []events.Event {
	var out []events.Event
	for _, l := range lines {
		out = append(out, events.Event{Kind: events.KindSystem, Text: l, Data: map[string]any{"raw": l}})
	}
	return out
}

func TestFeedPublishesAndEmitsThrottled(t *testing.T) {
	log := events.New(1000)
	var mu sync.Mutex
	var evs []map[string]any
	now := time.Unix(1000, 0)
	feed := NewViewFeed(log, func(kind, text string, data map[string]any) {
		mu.Lock()
		defer mu.Unlock()
		if kind != events.KindBotViewFrame {
			t.Errorf("kind %s", kind)
		}
		evs = append(evs, data)
	}, 5*time.Second)
	feed.SetClock(func() time.Time { return now })
	feed.Process(events.Event{Kind: events.KindSystem, Text: "not a view"})
	push := func(seq uint64, hostiles []string) {
		for _, l := range frameLines(seq, []string{tankRec}, hostiles, nil) {
			feed.Process(viewEvents([]string{l})[0])
		}
	}
	push(1, nil)
	if _, n, ok := feed.Latest(); !ok || n != 1 || len(evs) != 1 {
		t.Fatalf("first frame: n=%d events=%d", n, len(evs))
	}
	// Quiet frames within the interval: no event.
	for s := uint64(2); s <= 4; s++ {
		now = now.Add(time.Second)
		push(s, nil)
	}
	if len(evs) != 1 {
		t.Fatalf("events %d", len(evs))
	}
	// Hostiles appear: an event even inside the interval (after the minimum gap).
	now = now.Add(time.Second)
	push(5, []string{mobRec})
	if len(evs) != 2 || evs[1]["engaged"].(int) != 1 {
		t.Fatalf("events %d %v", len(evs), evs)
	}
	// Then the interval.
	now = now.Add(6 * time.Second)
	push(6, []string{mobRec})
	if len(evs) != 3 {
		t.Fatalf("events %d", len(evs))
	}
	fr, n, ok := feed.Latest()
	if !ok || n != 6 || fr.Seq != 6 {
		t.Fatalf("latest %+v %d", fr, n)
	}
	feed.Reset()
	if _, _, ok := feed.Latest(); ok {
		t.Fatal("frame after Reset")
	}
}

func TestFeedRunAndWaitNew(t *testing.T) {
	log := events.New(1000)
	feed := NewViewFeed(log, nil, 0)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go feed.Run(ctx)
	time.Sleep(10 * time.Millisecond)
	go func() {
		for _, l := range frameLines(1, []string{tankRec}, nil, nil) {
			log.Append(events.Event{Kind: events.KindSystem, Text: l, Data: map[string]any{"raw": l}})
		}
		for _, l := range frameLines(2, []string{tankRec}, nil, nil) {
			// the addon channel carries the line without its "TBM:"
			log.Append(events.Event{Kind: events.KindAddon, Text: strings.TrimPrefix(l, "TBM:"), Data: map[string]any{"prefix": "TBM"}})
		}
	}()
	wctx, wcancel := context.WithTimeout(ctx, 3*time.Second)
	defer wcancel()
	fr, n, err := feed.WaitNew(wctx, 0)
	if err != nil || n < 1 {
		t.Fatalf("%v %d", err, n)
	}
	for n < 2 {
		if fr, n, err = feed.WaitNew(wctx, n); err != nil {
			t.Fatal(err)
		}
	}
	if fr.Seq != 2 {
		t.Fatalf("seq %d", fr.Seq)
	}
	if s := feed.Stats(); s.Frames != 2 {
		t.Fatalf("%+v", s)
	}
	cancel()
	if _, _, err := feed.WaitNew(ctx, 99); err == nil {
		t.Fatal("WaitNew returned with no frame after the context ended")
	}
}

func TestFeedNotesBindingEnd(t *testing.T) {
	feed := NewViewFeed(events.New(100), nil, 0)
	feed.ProcessLine("TBM:ACTION_ACK|commander bind|party|5|Tanky") // not an end
	if len(feed.Ends()) != 0 {
		t.Fatal("bind counted as an end")
	}
	feed.ProcessLine("TBM:ACTION_ACK|commander unbind|party|5|lead bot logged out")
	e := feed.Ends()
	if len(e) != 1 || e[0].Reason != "lead bot logged out" || e[0].Count != 5 {
		t.Fatalf("%+v", e)
	}
}

func TestFrameSummary(t *testing.T) {
	a := NewViewAssembler()
	fr := feedAll(a, frameLines(4, []string{tankRec, healRec}, []string{mobRec}, []string{threatRc}))[0]
	s := FrameSummary(*fr)
	if s["seq"].(uint64) != 4 || len(s["party"].([]map[string]any)) != 2 || s["engaged"].(int) != 1 {
		t.Fatalf("%v", s)
	}
	h := s["hostiles"].([]map[string]any)[0]
	if fmt.Sprint(h["threat_holder"]) != "0x000000000000002A" {
		t.Fatalf("%v", h)
	}
}

// `cmd gossip <npc entry> <bot> <option>` (TortoiseBots #1012): the verb is known, and an option text is accepted only as words that are
// commander arguments.
func TestCheckCommandGossip(t *testing.T) {
	for _, ok := range []string{"cmd gossip 7607 Magey 0", "cmd gossip 7604 Tanky blow the door", "cmd gossip 7604 Tanky Weegli,_blow-it"} {
		if err := CheckCommand(ok); err != nil {
			t.Errorf("%q: %v", ok, err)
		}
	}
	if err := CheckCommand("cmd gossip 7604 Tanky Where's Bly?"); err == nil {
		t.Error("an apostrophe was accepted in an argument")
	}
	if BadOrderWords("blow the door") != "" || BadOrderWords("Where's Bly?") == "" || BadOrderWords("  ") == "" {
		t.Errorf("%q %q", BadOrderWords("Where's Bly?"), BadOrderWords("  "))
	}
}
