package main

import (
	"context"
	"crypto/rand"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"sort"
	"strings"
	"sync"
	"time"
)

// loginBudget is the fleet's one share of realmd's login limit. realmd answers DB_BUSY once
// 14 other accounts have logged in from one address within 300 s (the 15th is refused), and
// every child process used to count only its own logins. The supervisor holds this budget:
// it books a slot for each child it starts, and a reconnecting child books its next login
// here over loopback. Booked slots may lie in the future, so the budget never grants a login
// that, together with the ones already booked, would be the 15th within a window.
type loginBudget struct {
	mu     sync.Mutex
	limit  int           // logins allowed in one window
	window time.Duration // realmd's window
	// margin is slack on top of the window. A slot is booked before the child starts, and
	// realmd stamps last_login a few seconds after that (process start, SRP, queueing).
	margin time.Duration
	slots  []time.Time // booked login times, sorted
	// busyUntil: realmd answered busy; no login before this.
	busyUntil time.Time
}

const (
	// maxBudgetSlots bounds the bookings a client can make through the port. A real fleet
	// holds one live booking per child.
	maxBudgetSlots = 512
	// maxBudgetAfter bounds the earliest time a client may ask for (the reconnect backoff
	// is at most two minutes); it also keeps the arithmetic far from overflow.
	maxBudgetAfter = time.Hour
	// budgetTokenEnv carries the budget's bearer token to a child. The environment, not
	// argv: ps shows the command line to every user.
	budgetTokenEnv = "TURTLE_AGENT_LOGIN_BUDGET_TOKEN"
)

func newLoginBudget(limit int, window time.Duration) *loginBudget {
	return &loginBudget{limit: limit, window: window, margin: window / 30} // 10 s on 300 s
}

// reserve books the first login time at or after earliest that keeps the budget, and
// returns it.
func (b *loginBudget) reserve(earliest time.Time) time.Time {
	t, _ := b.book(earliest, 0, nil)
	return t
}

// reserveAdjusted is reserve with a final adjustment of the time: adjust maps a login time the budget would allow to the time the
// login will really happen (realmd's hour-start hold plus jitter, login_hold.go). Under the budget's lock it finds the earliest
// allowed time, applies adjust, and looks again from the adjusted time until adjust leaves the time alone, so the stored slot is
// the time of the login itself and expires 300 s after that, not after the time before the adjustment.
func (b *loginBudget) reserveAdjusted(earliest time.Time, adjust func(time.Time) time.Time) time.Time {
	t, _ := b.book(earliest, 0, adjust)
	return t
}

// tryReserve is reserve for a client of the port: false when maxBudgetSlots are booked.
func (b *loginBudget) tryReserve(earliest time.Time) (time.Time, bool) {
	return b.book(earliest, maxBudgetSlots, nil)
}

// tryReserveAdjusted is reserveAdjusted for a client of the port.
func (b *loginBudget) tryReserveAdjusted(earliest time.Time, adjust func(time.Time) time.Time) (time.Time, bool) {
	return b.book(earliest, maxBudgetSlots, adjust)
}

// maxAdjustRounds bounds the adjust-and-look-again loop of book.
const maxAdjustRounds = 8

func (b *loginBudget) book(earliest time.Time, maxSlots int, adjust func(time.Time) time.Time) (time.Time, bool) {
	b.mu.Lock()
	defer b.mu.Unlock()
	span := b.window + b.margin
	keep := b.slots[:0]
	for _, s := range b.slots {
		if earliest.Sub(s) < span { // an older slot cannot share a window with any later login
			keep = append(keep, s)
		}
	}
	b.slots = keep
	if maxSlots > 0 && len(b.slots) >= maxSlots {
		return time.Time{}, false
	}

	t := earliest
	if b.busyUntil.After(t) {
		t = b.busyUntil
	}
	t = b.firstFit(t, span)
	for i := 0; adjust != nil && i < maxAdjustRounds; i++ {
		a := adjust(t)
		if !a.After(t) {
			break
		}
		t = b.firstFit(a, span)
	}
	i := sort.Search(len(b.slots), func(i int) bool { return b.slots[i].After(t) })
	b.slots = append(b.slots, time.Time{})
	copy(b.slots[i+1:], b.slots[i:])
	b.slots[i] = t
	return t, true
}

// firstFit is the first login time at or after t that keeps the budget. The only times worth trying are t and the moments an old
// slot leaves the window.
func (b *loginBudget) firstFit(t time.Time, span time.Duration) time.Time {
	cands := []time.Time{t}
	for _, s := range b.slots {
		if c := s.Add(span); c.After(t) {
			cands = append(cands, c)
		}
	}
	sort.Slice(cands, func(i, j int) bool { return cands[i].Before(cands[j]) })
	for _, c := range cands {
		if b.fits(c, span) {
			return c
		}
	}
	return t
}

// fits says whether a login at t leaves every span-long stretch with at most limit logins.
func (b *loginBudget) fits(t time.Time, span time.Duration) bool {
	i := sort.Search(len(b.slots), func(i int) bool { return b.slots[i].After(t) })
	all := make([]time.Time, 0, len(b.slots)+1)
	all = append(append(append(all, b.slots[:i]...), t), b.slots[i:]...)
	for i := 0; i+b.limit < len(all); i++ {
		if all[i+b.limit].Sub(all[i]) < span {
			return false
		}
	}
	return true
}

// busy notes that realmd just answered busy: nobody logs in for a whole window.
func (b *loginBudget) busy(now time.Time) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if u := now.Add(b.window + b.margin); u.After(b.busyUntil) {
		b.busyUntil = u
	}
}

// budgetRequest is the body of POST /login: the child's own backoff, as the earliest
// it would log in.
type budgetRequest struct {
	AfterMS int64 `json:"after_ms"`
}

type budgetReply struct {
	WaitMS int64 `json:"wait_ms"`
}

// newBudgetToken is a random bearer token for one fleet run.
func newBudgetToken() (string, error) {
	var raw [24]byte
	if _, err := rand.Read(raw[:]); err != nil {
		return "", err
	}
	return hex.EncodeToString(raw[:]), nil
}

// serveLoginBudget serves b on a loopback port for the fleet's children and returns the
// address. POST /login books a login (the reply is how long to wait), POST /busy reports
// realmd's busy answer. Both need the bearer token (which only the children's environment
// holds); a browser page, with an Origin header or another Host, is refused, so neither a
// local process nor a web page can freeze or flood the fleet's logins. Stop it with the
// returned func or when ctx ends.
func serveLoginBudget(ctx context.Context, b *loginBudget, token string) (string, func(), error) {
	if len(token) < 16 {
		return "", nil, errors.New("login budget token too short")
	}
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return "", nil, err
	}
	want := []byte("Bearer " + token)
	host := ln.Addr().String()
	// guard admits only a POST from a child: right Host, no Origin, right token.
	guard := func(h http.HandlerFunc) http.HandlerFunc {
		return func(w http.ResponseWriter, r *http.Request) {
			switch {
			case r.Host != host || r.Header.Get("Origin") != "":
				http.Error(w, "forbidden", http.StatusForbidden)
			case subtle.ConstantTimeCompare([]byte(r.Header.Get("Authorization")), want) != 1:
				http.Error(w, "unauthorized", http.StatusUnauthorized)
			case r.Method != http.MethodPost:
				http.Error(w, "bad request", http.StatusBadRequest)
			default:
				h(w, r)
			}
		}
	}
	mux := http.NewServeMux()
	mux.HandleFunc("/login", guard(func(w http.ResponseWriter, r *http.Request) {
		var req budgetRequest
		if json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<10)).Decode(&req) != nil || req.AfterMS < 0 || req.AfterMS > maxBudgetAfter.Milliseconds() {
			http.Error(w, "bad request", http.StatusBadRequest)
			return
		}
		now := loginHoldNow()
		// The hour-start hold and jitter are applied inside the booking, so the stored slot is the time the child will log in.
		at, ok := b.tryReserveAdjusted(now.Add(time.Duration(req.AfterMS)*time.Millisecond), holdAdjuster())
		if !ok {
			http.Error(w, "too many bookings", http.StatusTooManyRequests)
			return
		}
		_ = json.NewEncoder(w).Encode(budgetReply{WaitMS: max(at.Sub(now), 0).Milliseconds()})
	}))
	mux.HandleFunc("/busy", guard(func(w http.ResponseWriter, r *http.Request) {
		b.busy(time.Now())
	}))
	srv := &http.Server{Handler: mux, ReadHeaderTimeout: 5 * time.Second}
	go func() { _ = srv.Serve(ln) }()
	stop := func() { _ = srv.Close() }
	go func() { <-ctx.Done(); stop() }()
	return host, stop, nil
}

// budgetClient is a child's view of the fleet's login budget.
type budgetClient struct {
	addr, token string
	http        *http.Client
}

func newBudgetClient(addr, token string) *budgetClient {
	return &budgetClient{addr: addr, token: token, http: &http.Client{Timeout: 5 * time.Second}}
}

// reserve books a login no sooner than after from now and returns how long to wait.
func (c *budgetClient) reserve(ctx context.Context, after time.Duration) (time.Duration, error) {
	body, _ := json.Marshal(budgetRequest{AfterMS: min(max(after, 0), maxBudgetAfter).Milliseconds()})
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, "http://"+c.addr+"/login", strings.NewReader(string(body)))
	if err != nil {
		return 0, err
	}
	req.Header.Set("Authorization", "Bearer "+c.token)
	resp, err := c.http.Do(req)
	if err != nil {
		return 0, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return 0, fmt.Errorf("login budget answered %s", resp.Status)
	}
	var out budgetReply
	if err := json.NewDecoder(io.LimitReader(resp.Body, 1<<10)).Decode(&out); err != nil {
		return 0, err
	}
	if out.WaitMS < 0 {
		return 0, errors.New("login budget answered a negative wait")
	}
	return time.Duration(out.WaitMS) * time.Millisecond, nil
}

// busy tells the fleet that realmd answered busy. Best effort: the child waits anyway.
func (c *budgetClient) busy(ctx context.Context) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, "http://"+c.addr+"/busy", nil)
	if err != nil {
		return
	}
	req.Header.Set("Authorization", "Bearer "+c.token)
	if resp, err := c.http.Do(req); err == nil {
		resp.Body.Close()
	}
}
