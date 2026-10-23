package eluna

import (
	"context"
	"encoding/json"
	"errors"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/pjerra/turtle-agent/internal/events"
)

// fakeLog is an in-memory events.Log: the client's Read/Wait/Last over lines the test appends.
type fakeLog struct {
	mu   sync.Mutex
	evs  []events.Event
	last uint64
	ch   chan struct{}
}

func newFakeLog() *fakeLog { return &fakeLog{ch: make(chan struct{})} }

func (l *fakeLog) add(text string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.last++
	l.evs = append(l.evs, events.Event{Seq: l.last, Kind: events.KindSystem, Text: text})
	close(l.ch)
	l.ch = make(chan struct{})
}

func (l *fakeLog) Last() uint64 {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.last
}

func (l *fakeLog) Read(q events.Query) events.Page {
	l.mu.Lock()
	defer l.mu.Unlock()
	limit := q.Limit
	if limit <= 0 {
		limit = events.DefaultLimit
	}
	p := events.Page{Events: []events.Event{}, Next: q.Since, Oldest: 1}
	for _, e := range l.evs {
		if e.Seq <= q.Since {
			continue
		}
		p.Next = e.Seq
		if len(p.Events) < limit {
			p.Events = append(p.Events, e)
		} else {
			p.More = true
		}
	}
	return p
}

func (l *fakeLog) Wait(ctx context.Context, since uint64) error {
	for {
		l.mu.Lock()
		if l.last > since {
			l.mu.Unlock()
			return nil
		}
		ch := l.ch
		l.mu.Unlock()
		select {
		case <-ch:
		case <-ctx.Done():
			return ctx.Err()
		}
	}
}

// testClient is a client with a fixed request id and a sender the test can script into the log.
func testClient(t *testing.T) (*Client, *fakeLog, *[]string) {
	t.Helper()
	l := newFakeLog()
	sent := &[]string{}
	c := &Client{
		Log:     l,
		Timeout: 100 * time.Millisecond,
		NewID:   func() string { return "t1" },
		Send: func(ctx context.Context, line string) error {
			*sent = append(*sent, line)
			return nil
		},
	}
	return c, l, sent
}

// A reply that carries another request's id (an older answer, another caller's) is not ours: only ours counts.
func TestDoDropsOtherIDs(t *testing.T) {
	c, l, sent := testClient(t)
	c.Send = func(ctx context.Context, line string) error {
		*sent = append(*sent, line)
		l.add(`TTJSON {"v":1,"cmd":"help","id":"old","ok":true,"final":true}`)
		l.add(`TTJSON {"v":1,"cmd":"help","id":"t1","ok":true,"final":true}`)
		l.add(`TTJSON {"v":1,"cmd":"help","id":"next","ok":true,"final":true}`)
		return nil
	}
	rs, err := c.Do(context.Background(), "help", 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(rs) != 1 || rs[0].ID != "t1" || rs[0].Cmd != "help" {
		t.Fatalf("replies %+v", rs)
	}
	if (*sent)[0] != ".tt help #t1" {
		t.Fatalf("sent %q", *sent)
	}
}

func TestDoTimeout(t *testing.T) {
	c, _, sent := testClient(t)
	c.Timeout = 30 * time.Millisecond
	start := time.Now()
	rs, err := c.Do(context.Background(), "q floor 1 2 3", 0)
	if !errors.Is(err, ErrTimeout) {
		t.Fatalf("err %v", err)
	}
	if len(rs) != 0 {
		t.Fatalf("replies %+v", rs)
	}
	if len(*sent) != 1 || (*sent)[0] != ".tt q floor 1 2 3 #t1" {
		t.Fatalf("sent %v", *sent)
	}
	if time.Since(start) > 5*time.Second {
		t.Fatal("the wait was not bounded")
	}
}

// The extra time widens the wait: quiet's second read may come after the default timeout.
func TestDoExtraTimeWidensTheWait(t *testing.T) {
	c, l, _ := testClient(t)
	c.Timeout = 20 * time.Millisecond
	c.Send = func(ctx context.Context, line string) error {
		go func() {
			time.Sleep(60 * time.Millisecond)
			l.add(`TTJSON {"v":1,"cmd":"quiet","id":"t1","ok":true,"read":2,"final":true,"quiet":true}`)
		}()
		return nil
	}
	rs, err := c.Do(context.Background(), "quiet 50 0 0 0 w=1", 2*time.Second)
	if err != nil || len(rs) != 1 || !rs[0].Final {
		t.Fatalf("%+v %v", rs, err)
	}
}

func TestDoErrorReply(t *testing.T) {
	c, l, _ := testClient(t)
	c.Send = func(ctx context.Context, line string) error {
		l.add(`TTJSON {"v":1,"cmd":"q.unit","id":"t1","ok":false,"err":"not_allowed","msg":"rank 3 needed"}`)
		return nil
	}
	rs, err := c.Do(context.Background(), "q unit 0x1", 0)
	var e *Error
	if !errors.As(err, &e) {
		t.Fatalf("err %v", err)
	}
	if e.Code != "not_allowed" || e.Msg != "rank 3 needed" || e.Reply.Cmd != "q.unit" {
		t.Fatalf("%+v", e)
	}
	if len(rs) != 1 {
		t.Fatalf("the refusing reply is kept: %+v", rs)
	}
	if !strings.Contains(err.Error(), "not_allowed") {
		t.Fatalf("err %v", err)
	}
}

func TestQuietWaitsForReadTwo(t *testing.T) {
	c, l, sent := testClient(t)
	c.Send = func(ctx context.Context, line string) error {
		*sent = append(*sent, line)
		l.add(`TTJSON {"v":1,"cmd":"quiet","id":"t1","ok":true,"read":1,"final":false,"quiet_now":false,"n_near":1,"near":[{"g":"0xF130000000001622","e":5650,"n":"Witch Doctor Zum'rah","d":8.2}]}`)
		go func() {
			time.Sleep(10 * time.Millisecond)
			l.add(`TTJSON {"v":1,"cmd":"quiet","id":"t1","ok":true,"read":2,"final":true,"quiet":true,"near":[{"g":"0xF130000000001622","e":5650,"n":"Witch Doctor Zum'rah","d":8.2}],"roamers":[]}`)
		}()
		return nil
	}
	q, err := c.Quiet(context.Background(), 1862.6, 1054.9, 9, 50, 20*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	if !q.Quiet || len(q.Near) != 1 || q.Near[0].Entry != 5650 || q.Near[0].Name == "" || len(q.Roamers) != 0 {
		t.Fatalf("%+v", q)
	}
	want := ".tt quiet 50.00 1862.60 1054.90 9.00 w=20 #t1"
	if (*sent)[0] != want {
		t.Fatalf("sent %q, want %q", (*sent)[0], want)
	}
}

func TestPlaceNotPlaced(t *testing.T) {
	c, l, sent := testClient(t)
	c.Send = func(ctx context.Context, line string) error {
		*sent = append(*sent, line)
		l.add(`TTJSON {"v":1,"cmd":"place","id":"t1","ok":true,"read":1,"final":false,"center":[10,20,30],"members":[{"g":"0x1","n":"A","to":[10,20,30]}]}`)
		l.add(`TTJSON {"v":1,"cmd":"place","id":"t1","ok":true,"read":2,"final":true,"placed":false,"members":[{"g":"0x1","ok":false,"dz":4.5}]}`)
		return nil
	}
	p, err := c.Place(context.Background(), 10, 20, 30, 3)
	if err != nil {
		t.Fatal(err)
	}
	if p.Placed || len(p.Members) != 1 || p.Members[0].OK || p.Members[0].Dz != 4.5 {
		t.Fatalf("%+v", p)
	}
	if (*sent)[0] != ".tt place 10.00 20.00 30.00 spread=3 #t1" {
		t.Fatalf("sent %q", (*sent)[0])
	}
}

// With Of set, the group commands carry `of=<name>`: a bound commander is not in the bots' group, so place, q group and
// reset would act on the commander's empty group.
func TestGroupCommandsCarryOf(t *testing.T) {
	c, l, sent := testClient(t)
	c.Of = "Leadbot"
	c.Send = func(ctx context.Context, line string) error {
		*sent = append(*sent, line)
		l.add(`TTJSON {"v":1,"cmd":"x","id":"t1","ok":true}`)
		return nil
	}
	for _, sub := range []string{"place 10 20 30 spread=3", "q group", "reset 33"} {
		if _, err := c.Do(context.Background(), sub, 0); err != nil {
			t.Fatalf("%s: %v", sub, err)
		}
	}
	want := []string{
		".tt place 10 20 30 spread=3 of=Leadbot #t1",
		".tt q group of=Leadbot #t1",
		".tt reset 33 of=Leadbot #t1",
	}
	if !slices.Equal(*sent, want) {
		t.Fatalf("sent %q, want %q", *sent, want)
	}
	// Every other command keeps the caller's group (of= is not a toolkit argument there).
	if _, err := c.Do(context.Background(), "q units 10", 0); err != nil {
		t.Fatal(err)
	}
	if got := (*sent)[len(*sent)-1]; got != ".tt q units 10 #t1" {
		t.Fatalf("sent %q", got)
	}
}

// Without Of (nothing bound, or the caller's own group) no `of=` is sent.
func TestGroupCommandsWithoutOf(t *testing.T) {
	c, l, sent := testClient(t)
	c.Send = func(ctx context.Context, line string) error {
		*sent = append(*sent, line)
		l.add(`TTJSON {"v":1,"cmd":"x","id":"t1","ok":true}`)
		return nil
	}
	if _, err := c.Do(context.Background(), "place 10 20 30 spread=3", 0); err != nil {
		t.Fatal(err)
	}
	if _, err := c.Do(context.Background(), "q group", 0); err != nil {
		t.Fatal(err)
	}
	want := []string{".tt place 10 20 30 spread=3 #t1", ".tt q group #t1"}
	if !slices.Equal(*sent, want) {
		t.Fatalf("sent %q, want %q", *sent, want)
	}
}

// A placement is judged by the floor, not by the distance: a bot that drifted 7 yd from its ring spot but stands 0.3 yd
// from the floor is placed (the toolkit's own `placed` says no), while a bot 3 yd off the floor is not.
func TestPlaceOnFloorJudgesDzNotDistance(t *testing.T) {
	c, l, _ := testClient(t)
	c.Send = func(ctx context.Context, line string) error {
		l.add(`TTJSON {"v":1,"cmd":"place","id":"t1","ok":true,"read":1,"final":false,"members":[{"g":"0x1","n":"A"}]}`)
		l.add(`TTJSON {"v":1,"cmd":"place","id":"t1","ok":true,"read":2,"final":true,"placed":false,"members":[{"g":"0x1","n":"A","ok":false,"d":7,"dz":0.3,"alive":true}]}`)
		return nil
	}
	p, err := c.Place(context.Background(), 10, 20, 30, 3)
	if err != nil {
		t.Fatal(err)
	}
	if p.Placed || !p.OnFloor() {
		t.Fatalf("d=7 dz=0.3: placed %v, on floor %v", p.Placed, p.OnFloor())
	}

	c2, l2, _ := testClient(t)
	c2.Send = func(ctx context.Context, line string) error {
		l2.add(`TTJSON {"v":1,"cmd":"place","id":"t1","ok":true,"read":2,"final":true,"placed":false,"members":[{"g":"0x1","n":"A","ok":false,"d":1,"dz":3}]}`)
		return nil
	}
	p2, err := c2.Place(context.Background(), 10, 20, 30, 3)
	if err != nil {
		t.Fatal(err)
	}
	if p2.OnFloor() {
		t.Fatalf("dz=3 counted as on the floor: %+v", p2)
	}

	// -0.3 is as close as 0.3 (the height difference is signed).
	c3, l3, _ := testClient(t)
	c3.Send = func(ctx context.Context, line string) error {
		l3.add(`TTJSON {"v":1,"cmd":"place","id":"t1","ok":true,"read":2,"final":true,"placed":true,"members":[{"g":"0x1","n":"A","dz":-0.3}]}`)
		return nil
	}
	if p3, err := c3.Place(context.Background(), 10, 20, 30, 3); err != nil || !p3.OnFloor() {
		t.Fatalf("%+v %v", p3, err)
	}
}

// A member the toolkit could not find (why "gone") is not on the floor, and a placement with no members at all is not either:
// an empty read must not pass as a placed party.
func TestPlaceOnFloorNeedsEveryMember(t *testing.T) {
	if (PlaceResult{}).OnFloor() {
		t.Fatal("an empty placement is on the floor")
	}
	gone := PlaceResult{Members: []Placed{{GUID: "0x1", Why: "gone"}, {GUID: "0x2", Dz: 0.2}}}
	if gone.OnFloor() {
		t.Fatalf("a gone member passed: %+v", gone)
	}
	one := PlaceResult{Members: []Placed{{GUID: "0x1", Dz: 0.2}, {GUID: "0x2", Dz: -1.4}}}
	if !one.OnFloor() {
		t.Fatalf("members on the floor were refused: %+v", one)
	}
	if edge := (PlaceResult{Members: []Placed{{GUID: "0x1", Dz: 1.5}}}); !edge.OnFloor() {
		t.Fatal("1.5 yd is the tolerance's edge and must pass")
	}
}

func TestUnitWrongGUID(t *testing.T) {
	c, l, _ := testClient(t)
	const asked = uint64(0xF130000E450000FF)
	c.Send = func(ctx context.Context, line string) error {
		l.add(`TTJSON {"v":1,"cmd":"q.unit","id":"t1","ok":true,"asked":"0xF130000E450000FF","unit":{"g":"0xF130000000000001","e":1,"n":"Other"}}`)
		return nil
	}
	if _, err := c.Unit(context.Background(), asked, nil); !errors.Is(err, ErrWrongUnit) {
		t.Fatalf("err %v", err)
	}

	c2, l2, sent2 := testClient(t)
	c2.Send = func(ctx context.Context, line string) error {
		*sent2 = append(*sent2, line)
		l2.add(`TTJSON {"v":1,"cmd":"q.unit","id":"t1","ok":true,"asked":"0xF130000E450000FF","unit":{"g":"0xF130000E450000FF","e":41592,"n":"Frostmane Hailar"}}`)
		return nil
	}
	u, err := c2.Unit(context.Background(), asked, []uint32{41592, 41593})
	if err != nil || u.GUID != "0xF130000E450000FF" || u.Name != "Frostmane Hailar" || u.Entry != 41592 {
		t.Fatalf("%+v %v", u, err)
	}
	if (*sent2)[0] != ".tt q unit 0xF130000E450000FF au=41592,41593 #t1" {
		t.Fatalf("sent %q", (*sent2)[0])
	}
}

func TestFloor(t *testing.T) {
	c, l, sent := testClient(t)
	c.Send = func(ctx context.Context, line string) error {
		*sent = append(*sent, line)
		l.add(`TTJSON {"v":1,"cmd":"q.floor","id":"t1","ok":true,"floor":17.03,"floor_xy":17.03,"top":161.8,"multi":true}`)
		return nil
	}
	f, err := c.Floor(context.Background(), -731.6, -2218.4, 18.5)
	if err != nil {
		t.Fatal(err)
	}
	if f.Floor == nil || *f.Floor != 17.03 || f.Top == nil || *f.Top != 161.8 || !f.Multi {
		t.Fatalf("%+v", f)
	}
	if (*sent)[0] != ".tt q floor -731.60 -2218.40 18.50 #t1" {
		t.Fatalf("sent %q", (*sent)[0])
	}

	// No floor: a null field is no value, not zero.
	c2, l2, _ := testClient(t)
	c2.Send = func(ctx context.Context, line string) error {
		l2.add(`TTJSON {"v":1,"cmd":"q.floor","id":"t1","ok":true,"floor":null,"top":null,"multi":false}`)
		return nil
	}
	f, err = c2.Floor(context.Background(), 0, 0, 0)
	if err != nil || f.Floor != nil || f.Top != nil || f.Multi {
		t.Fatalf("%+v %v", f, err)
	}
}

// The client reads the raw line when the log kept one (the colours-stripped text and the raw differ).
func TestDoReadsRawText(t *testing.T) {
	c, l, _ := testClient(t)
	c.Send = func(ctx context.Context, line string) error {
		l.mu.Lock()
		l.last++
		l.evs = append(l.evs, events.Event{Seq: l.last, Kind: events.KindSystem, Text: "TTJSON {", Data: map[string]any{"raw": `TTJSON {"v":1,"cmd":"help","id":"t1","ok":true,"final":true}`}})
		l.mu.Unlock()
		return nil
	}
	rs, err := c.Do(context.Background(), "help", 0)
	if err != nil || len(rs) != 1 || rs[0].Cmd != "help" {
		t.Fatalf("%+v %v", rs, err)
	}
}

// A reply body is handed to the caller as the server's own JSON values.
func TestTypedDecodersSeeRawFields(t *testing.T) {
	r, ok := ParseLine(`TTJSON {"v":1,"cmd":"q.floor","id":"t1","ok":true,"floor":91.67}`)
	if !ok {
		t.Fatal("not parsed")
	}
	var f float64
	if err := json.Unmarshal(r.Body["floor"], &f); err != nil || f != 91.67 {
		t.Fatalf("%v %v", f, err)
	}
}

// quiet sends the toolkit's dz= window and skip= list only when the run asks for them; dz=0 (no vertical limit) is sent as 0.
func TestQuietWithSendsWindowAndSkip(t *testing.T) {
	f := func(v float32) *float32 { return &v }
	for _, c := range []struct {
		o    QuietOpts
		want string
	}{
		{QuietOpts{}, ".tt quiet 50.00 1.00 2.00 3.00 w=0 #t1"},
		{QuietOpts{Dz: f(8)}, ".tt quiet 50.00 1.00 2.00 3.00 w=0 dz=8 #t1"},
		{QuietOpts{Dz: f(0)}, ".tt quiet 50.00 1.00 2.00 3.00 w=0 dz=0 #t1"},
		{QuietOpts{Dz: f(6.5), Skip: []uint32{3849, 3850}}, ".tt quiet 50.00 1.00 2.00 3.00 w=0 dz=6.5 skip=3849,3850 #t1"},
		{QuietOpts{Skip: []uint32{3849}}, ".tt quiet 50.00 1.00 2.00 3.00 w=0 skip=3849 #t1"},
	} {
		c2, l, sent := testClient(t)
		c2.Send = func(ctx context.Context, line string) error {
			*sent = append(*sent, line)
			l.add(`TTJSON {"v":1,"cmd":"quiet","id":"t1","ok":true,"read":1,"final":true,"quiet":true,"near":[],"roamers":[]}`)
			return nil
		}
		q, err := c2.QuietWith(context.Background(), 1, 2, 3, 50, 0, c.o)
		if err != nil || !q.Quiet {
			t.Fatalf("%+v: %+v %v", c.o, q, err)
		}
		if (*sent)[0] != c.want {
			t.Errorf("sent %q, want %q", (*sent)[0], c.want)
		}
	}
	// at most the toolkit's 16 entries go
	var many []uint32
	for i := uint32(1); i <= 20; i++ {
		many = append(many, i)
	}
	if got := strings.Count(QuietOpts{Skip: many}.args(), ",") + 1; got != MaxSkip {
		t.Errorf("%d entries sent, want %d", got, MaxSkip)
	}
}
