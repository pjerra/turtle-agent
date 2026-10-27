package scenario

import (
	"context"
	"encoding/json"
	"sync"
	"time"

	"github.com/pjerra/turtle-agent/internal/events"
)

// fakeCaller is the scripted agent: a scripted status, an event list served
// through events with real since/next/more semantics, recorded calls and
// per-method results or errors. clk advances on empty long-polls so deadline
// loops terminate; script runs on each events call (without the lock held) so
// tests can inject events mid-run, like a live agent would append.
type fakeCaller struct {
	mu        sync.Mutex
	char      string
	statusErr error
	evs       []events.Event
	next      uint64
	calls     []fakeCall
	results   map[string]fakeResult
	token     string
	clk       *fakeClock
	script    func(call int)
	polls     int
}

type fakeCall struct {
	Method string
	Params json.RawMessage
}

type fakeResult struct {
	Result any
	Err    error
}

func (f *fakeCaller) Token() string { return f.token }

// add stores an event and returns its seq.
func (f *fakeCaller) add(e events.Event) uint64 {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.next++
	e.Seq = f.next
	f.evs = append(f.evs, e)
	return f.next
}

func (f *fakeCaller) Call(ctx context.Context, method string, params any, timeout time.Duration) (json.RawMessage, error) {
	raw, _ := json.Marshal(params)
	f.mu.Lock()
	f.calls = append(f.calls, fakeCall{Method: method, Params: raw})
	char, statusErr, results := f.char, f.statusErr, f.results
	f.mu.Unlock()
	switch method {
	case "status":
		if statusErr != nil {
			return nil, statusErr
		}
		f.mu.Lock()
		last := f.next
		f.mu.Unlock()
		return json.Marshal(map[string]any{"character": char, "last_seq": last})
	case "events":
		f.mu.Lock()
		f.polls++
		n := f.polls
		script := f.script
		f.mu.Unlock()
		if script != nil {
			script(n)
		}
		if f.clk != nil {
			var p struct {
				Since  uint64 `json:"since"`
				WaitMS int    `json:"wait_ms"`
			}
			_ = json.Unmarshal(raw, &p)
			if p.WaitMS > 0 {
				f.mu.Lock()
				empty := true
				for _, e := range f.evs {
					if e.Seq > p.Since {
						empty = false
						break
					}
				}
				f.mu.Unlock()
				if empty {
					f.clk.advance(time.Duration(p.WaitMS) * time.Millisecond)
				}
			}
		}
		var p struct {
			Since uint64 `json:"since"`
			Limit int    `json:"limit"`
		}
		_ = json.Unmarshal(raw, &p)
		if p.Limit <= 0 {
			p.Limit = 1000
		}
		f.mu.Lock()
		defer f.mu.Unlock()
		var out []events.Event
		for _, e := range f.evs {
			if e.Seq > p.Since {
				out = append(out, e)
			}
		}
		next := p.Since
		more := false
		if len(out) > p.Limit {
			out = out[:p.Limit]
			more = true
		}
		if len(out) > 0 {
			next = out[len(out)-1].Seq
		}
		if out == nil {
			out = []events.Event{}
		}
		return json.Marshal(events.Page{Events: out, Next: next, More: more})
	}
	if r, ok := results[method]; ok {
		if r.Err != nil {
			return nil, r.Err
		}
		return json.Marshal(r.Result)
	}
	return json.Marshal(map[string]any{})
}

func (f *fakeCaller) called(method string) []fakeCall {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []fakeCall
	for _, c := range f.calls {
		if c.Method == method {
			out = append(out, c)
		}
	}
	return out
}

func (f *fakeCaller) callCount(method string) int { return len(f.called(method)) }

// fakeDB scripts counts or errors in call order.
type fakeDB struct {
	mu      sync.Mutex
	counts  []int64
	errs    []error
	calls   int
	queries []DBQuery
}

func (f *fakeDB) Count(ctx context.Context, q DBQuery) (int64, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	i := f.calls
	f.calls++
	f.queries = append(f.queries, q)
	if i < len(f.errs) && f.errs[i] != nil {
		return 0, f.errs[i]
	}
	if i < len(f.counts) {
		return f.counts[i], nil
	}
	if len(f.counts) > 0 {
		return f.counts[len(f.counts)-1], nil
	}
	return 0, nil
}

// fakeClock is the injected clock: Sleep advances Now, and a cancelled
// context fails the sleep like an interrupt.
type fakeClock struct {
	mu     sync.Mutex
	now    time.Time
	sleeps []time.Duration
}

func newFakeClock() *fakeClock {
	return &fakeClock{now: time.Date(2026, 10, 27, 12, 0, 0, 0, time.UTC)}
}

func (c *fakeClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.now
}

func (c *fakeClock) Sleep(ctx context.Context, d time.Duration) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	c.advance(d)
	return nil
}

func (c *fakeClock) slept() time.Duration {
	c.mu.Lock()
	defer c.mu.Unlock()
	var total time.Duration
	for _, d := range c.sleeps {
		total += d
	}
	return total
}

func (c *fakeClock) advance(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.now = c.now.Add(d)
	c.sleeps = append(c.sleeps, d)
}

// bindFake wires the clock into a caller before the run.
func bindFake(c *fakeCaller, clk *fakeClock) *fakeCaller {
	c.clk = clk
	return c
}

func addonEvent(text, prefix, typ string) events.Event {
	return events.Event{
		Kind: events.KindAddon,
		Text: text,
		Data: map[string]any{"prefix": prefix, "type": typ},
		Time: newFakeClock().now,
	}
}

func parseFile(t interface {
	Fatal(...any)
	Helper()
}, data string) *File {
	t.Helper()
	f, err := Parse([]byte(data), nil)
	if err != nil {
		t.Fatal(err)
	}
	return f
}
