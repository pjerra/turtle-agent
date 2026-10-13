package events

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"
)

func appendKinds(l *Log, kinds ...string) {
	for _, k := range kinds {
		l.Append(Event{Kind: k})
	}
}

func TestSinceIsExclusiveAndContiguous(t *testing.T) {
	l := New(100)
	appendKinds(l, KindChat, KindMelee, KindChat, KindHeal)
	p := l.Read(Query{Since: 1})
	if len(p.Events) != 3 || p.Events[0].Seq != 2 || p.Events[2].Seq != 4 || p.Next != 4 || p.Dropped || p.More {
		t.Fatalf("%+v", p)
	}
	if p := l.Read(Query{Since: 4}); len(p.Events) != 0 || p.Next != 4 {
		t.Fatalf("%+v", p)
	}
}

func TestKindsFilterAdvancesNext(t *testing.T) {
	l := New(100)
	appendKinds(l, KindMelee, KindChat, KindMelee)
	p := l.Read(Query{Kinds: []string{KindChat}})
	if len(p.Events) != 1 || p.Events[0].Seq != 2 || p.Next != 3 {
		t.Fatalf("%+v", p)
	}
}

func TestPartyOnly(t *testing.T) {
	l := New(100)
	l.Append(Event{Kind: KindMelee, Party: true})
	l.Append(Event{Kind: KindMelee})
	if p := l.Read(Query{PartyOnly: true}); len(p.Events) != 1 || p.Events[0].Seq != 1 || p.Next != 2 {
		t.Fatalf("%+v", p)
	}
}

func TestLimitAndMore(t *testing.T) {
	l := New(100)
	for i := 0; i < 10; i++ {
		l.Append(Event{Kind: KindChat})
	}
	p := l.Read(Query{Limit: 4})
	if len(p.Events) != 4 || !p.More || p.Next != 4 {
		t.Fatalf("%+v", p)
	}
	p = l.Read(Query{Since: p.Next, Limit: 100})
	if len(p.Events) != 6 || p.More || p.Next != 10 {
		t.Fatalf("%+v", p)
	}
}

func TestEvictionReportsDropped(t *testing.T) {
	l := New(10)
	for i := 0; i < 25; i++ {
		l.Append(Event{Kind: KindChat})
	}
	p := l.Read(Query{})
	if !p.Dropped || p.Oldest <= 1 || p.Events[0].Seq != p.Oldest || p.Next != 25 || len(p.Events) > 10 {
		t.Fatalf("%+v", p)
	}
	if p := l.Read(Query{Since: p.Oldest}); p.Dropped {
		t.Fatalf("cursor at Oldest must not report drops: %+v", p)
	}
}

func TestConcurrentAppend(t *testing.T) {
	l := New(100000)
	var writers, reader sync.WaitGroup
	for g := 0; g < 8; g++ {
		writers.Add(1)
		go func() {
			defer writers.Done()
			for i := 0; i < 1000; i++ {
				l.Append(Event{Kind: KindChat})
			}
		}()
	}
	done := make(chan struct{})
	reader.Add(1)
	go func() {
		defer reader.Done()
		var next uint64
		for {
			p := l.Read(Query{Since: next, Limit: MaxLimit})
			for _, e := range p.Events {
				if e.Seq != next+1 {
					t.Errorf("gap: %d after %d", e.Seq, next)
				}
				next = e.Seq
			}
			select {
			case <-done:
				if next == 8000 {
					return
				}
			default:
			}
		}
	}()
	writers.Wait()
	close(done)
	reader.Wait()
	if l.Last() != 8000 {
		t.Fatalf("last %d", l.Last())
	}
}

func TestCursorAheadOfLog(t *testing.T) {
	l := New(10)
	appendKinds(l, KindChat)
	if p := l.Read(Query{Since: 99}); p.Next != 1 || len(p.Events) != 0 {
		t.Fatalf("%+v", p)
	}
}

func TestWaitWakesOnAppendAndHonoursContext(t *testing.T) {
	l := New(10)
	go func() { time.Sleep(20 * time.Millisecond); l.Append(Event{Kind: KindChat}) }()
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if err := l.Wait(ctx, 0); err != nil {
		t.Fatal(err)
	}
	ctx2, cancel2 := context.WithTimeout(context.Background(), 30*time.Millisecond)
	defer cancel2()
	if err := l.Wait(ctx2, 1); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("err = %v", err)
	}
}

func TestEventJSONShape(t *testing.T) {
	b, _ := json.Marshal(Event{Seq: 1, Kind: KindMelee, SourceGUID: 0xF130000000001234, Amount: 12})
	s := string(b)
	if !strings.Contains(s, `"source_guid":"0xF130000000001234"`) || strings.Contains(s, "target_guid") || !strings.Contains(s, `"amount":12`) {
		t.Fatal(s)
	}
}

func TestFilteredPagingCoversEveryEventOnce(t *testing.T) {
	l := New(100)
	for i := 0; i < 30; i++ {
		k := KindMelee
		if i%3 == 0 {
			k = KindChat
		}
		l.Append(Event{Kind: k})
	}
	var got []uint64
	var next uint64
	for i := 0; i < 20; i++ {
		p := l.Read(Query{Since: next, Kinds: []string{KindChat}, Limit: 3})
		for _, e := range p.Events {
			got = append(got, e.Seq)
		}
		next = p.Next
		if !p.More {
			break
		}
	}
	if len(got) != 10 || got[0] != 1 || got[9] != 28 || next != 28 && next != 30 {
		t.Fatalf("got %v next %d", got, next)
	}
}

func TestWaitReturnsImmediatelyWhenBehindAndLimitClamps(t *testing.T) {
	l := New(10)
	appendKinds(l, KindChat, KindChat)
	if err := l.Wait(context.Background(), 1); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 3; i++ {
		l.Append(Event{Kind: KindChat})
	}
	if p := l.Read(Query{Limit: -5}); len(p.Events) != 5 {
		t.Fatalf("%+v", p)
	}
}

func TestStampKeepsWallTimeAndAddsMono(t *testing.T) {
	saved, savedBase := now, base
	defer func() { now, base = saved, savedBase }()
	wall := time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC)
	now = func() time.Time { return wall }
	base = wall
	l := New(10)
	a := l.Append(Event{Kind: KindChat})
	wall = wall.Add(1500 * time.Millisecond)
	b := l.Append(Event{Kind: KindChat})
	if a.MonoMS != 0 || b.MonoMS != 1500 || !b.Time.Equal(wall) {
		t.Fatalf("%+v %+v", a, b)
	}
	// An event that brings its own time keeps it and still gets the mono_ms of its arrival.
	c := l.Append(Event{Kind: KindChat, Time: base.Add(-time.Hour)})
	if c.MonoMS != 1500 || !c.Time.Equal(base.Add(-time.Hour)) {
		t.Fatalf("preset time: %+v", c)
	}
	var m map[string]any
	raw, _ := json.Marshal(b)
	json.Unmarshal(raw, &m)
	if m["mono_ms"] != float64(1500) {
		t.Fatalf("json: %s", raw)
	}
}

func TestWallOffsetShowsAClockStep(t *testing.T) {
	saved := base
	defer func() { base = saved }()
	base = time.Now()
	elapsed := time.Second
	if got := wallOffset(base.Round(0).Add(elapsed), elapsed); got != 0 {
		t.Fatalf("no step: %v", got)
	}
	// The host clock stepped back 2 s while 1 s of monotonic time passed.
	if got := wallOffset(base.Round(0).Add(elapsed-2*time.Second), elapsed); got != -2*time.Second {
		t.Fatalf("step: %v", got)
	}
	_ = WallOffsetMS()
}

func TestMonoMSNeverGoesBack(t *testing.T) {
	l := New(100)
	prev := int64(-1)
	for i := 0; i < 1000; i++ {
		e := l.Append(Event{Kind: KindChat})
		if e.MonoMS < prev {
			t.Fatalf("mono_ms went back: %d after %d", e.MonoMS, prev)
		}
		prev = e.MonoMS
	}
}

func TestLogIntercept(t *testing.T) {
	l := New(100)
	var seen []string
	l.SetIntercept(func(e Event) bool {
		seen = append(seen, e.Text)
		if e.Text == "eat me" {
			l.Append(Event{Kind: KindSystem, Text: "from inside"}) // an interceptor may append other events
			return true
		}
		return false
	})
	l.Append(Event{Kind: KindSystem, Text: "keep"})
	got := l.Append(Event{Kind: KindSystem, Text: "eat me"})
	l.Append(Event{Kind: KindSystem, Text: "keep too"})
	if got.Seq != 0 || got.MonoMS == 0 && got.Time.IsZero() {
		t.Fatalf("consumed event: %+v", got)
	}
	p := l.Read(Query{})
	var texts []string
	for _, e := range p.Events {
		texts = append(texts, e.Text)
	}
	if len(texts) != 3 || texts[0] != "keep" || texts[1] != "from inside" || texts[2] != "keep too" || l.Last() != 3 {
		t.Fatalf("%v last %d", texts, l.Last())
	}
	l.SetIntercept(nil)
	if e := l.Append(Event{Kind: KindSystem, Text: "eat me"}); e.Seq != 4 {
		t.Fatalf("%+v", e)
	}
}
