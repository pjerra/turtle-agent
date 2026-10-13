package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"time"

	"github.com/pjerra/turtle-agent/internal/auth"
	"github.com/pjerra/turtle-agent/internal/config"
	"github.com/pjerra/turtle-agent/internal/events"
	"github.com/pjerra/turtle-agent/internal/navmesh"
	"github.com/pjerra/turtle-agent/internal/session"
	"github.com/pjerra/turtle-agent/internal/worldconn"
)

// Reconnect timings, variables so the tests can shorten them.
var (
	reconnectBackoff    = 5 * time.Second // before the first attempt; doubles for each later one
	reconnectMaxBackoff = 2 * time.Minute
	// realmdThrottleWindow is realmd's per-IP login window: it answers WOW_FAIL_DB_BUSY once
	// 15 distinct accounts have logged in from one IP within 300 s (AuthSocket.cpp:439-456).
	realmdThrottleWindow = 300 * time.Second
	// reconnectReadyWait bounds one attempt from the world connection to in world.
	reconnectReadyWait = time.Minute
)

const realmdThrottleLogins = 15

// backoff is the wait before reconnect attempt n (from 1).
func backoff(n int) time.Duration {
	d := reconnectBackoff
	for i := 1; i < n && d < reconnectMaxBackoff; i++ {
		d *= 2
	}
	return min(d, reconnectMaxBackoff)
}

// loginWindow keeps this process's realmd logins under realmd's rate: at most
// realmdThrottleLogins-1 in any realmdThrottleWindow (realmd counts the login it is
// answering as one more), and none for a whole window after realmd answered busy.
type loginWindow struct {
	times     []time.Time
	busyUntil time.Time
}

func (w *loginWindow) note(t time.Time) { w.times = append(w.times, t) }

func (w *loginWindow) busy(t time.Time) { w.busyUntil = t.Add(realmdThrottleWindow) }

// wait is how long the next login must wait at now.
func (w *loginWindow) wait(now time.Time) time.Duration {
	keep := w.times[:0]
	for _, t := range w.times {
		if now.Sub(t) < realmdThrottleWindow {
			keep = append(keep, t)
		}
	}
	w.times = keep
	var d time.Duration
	if n := realmdThrottleLogins - 1; len(w.times) >= n {
		d = w.times[len(w.times)-n].Add(realmdThrottleWindow).Sub(now)
	}
	return max(d, w.busyUntil.Sub(now), 0)
}

// retryableLogin says whether a failed login may succeed later. Unknown accounts, wrong
// passwords, bans and build errors never do, and every failed proof counts towards
// realmd's failed-login autoban (AuthSocket.cpp:1042-1054), so those stop the reconnects.
func retryableLogin(err error) bool {
	var ae *auth.AuthError
	if errors.As(err, &ae) {
		return ae.Code == auth.ResultDBBusy || ae.Code == auth.ResultAlreadyOnline
	}
	return !errors.Is(err, auth.ErrPINRequired)
}

// worldSession is one logged-in session and its Run result.
type worldSession struct {
	s      *session.Session
	conn   *worldconn.Conn
	runErr chan error
}

// reconnector logs the character in again after an unplanned disconnect (--reconnect N).
// The single-owner rule holds: each attempt builds a new session with its own Run
// goroutine; the old one has already returned.
type reconnector struct {
	total, used int
	window      *loginWindow
	budget      *budgetClient // the fleet's shared login budget; nil outside a fleet
	st          config.Settings
	account     string
	password    string
	character   string
	create      *session.CreateSpec // carried into every attempt, like the first login
	log         *events.Log
	stderr      io.Writer
	redact      func(string) string
	planner     *navmesh.Planner // shared with the first session, its loaded tiles kept
}

// next waits out the backoff and the realmd window and logs in again, until an attempt
// reaches the world, the attempts run out, a failure cannot change, or ctx ends.
func (r *reconnector) next(ctx context.Context) (*worldSession, bool) {
	for r.used < r.total {
		r.used++
		wait := r.wait(ctx)
		fmt.Fprintf(r.stderr, "reconnect: attempt %d of %d in %s\n", r.used, r.total, wait.Round(time.Millisecond))
		select {
		case <-time.After(wait):
		case <-ctx.Done():
			fmt.Fprintln(r.stderr, "reconnect: interrupted")
			return nil, false
		}
		r.window.note(time.Now())
		ws, err := r.login(ctx)
		if err == nil {
			r.log.Append(events.Event{Kind: events.KindSession, Data: map[string]any{"state": "reconnected", "attempt": r.used, "of": r.total}})
			info := ws.s.Status()
			fmt.Fprintf(r.stderr, "reconnect: in world again (attempt %d of %d): %s map=%d x=%.1f y=%.1f z=%.1f\n",
				r.used, r.total, info.Character, info.Map, info.X, info.Y, info.Z)
			return ws, true
		}
		fmt.Fprintln(r.stderr, "reconnect: attempt failed:", r.redact(err.Error()))
		var ae *auth.AuthError
		if errors.As(err, &ae) && ae.Code == auth.ResultDBBusy {
			r.window.busy(time.Now())
			if r.budget != nil {
				r.budget.busy(context.Background())
			}
		}
		if !retryableLogin(err) || ctx.Err() != nil {
			fmt.Fprintln(r.stderr, "reconnect: giving up; that answer will not change")
			return nil, false
		}
	}
	fmt.Fprintln(r.stderr, "reconnect: no attempts left")
	return nil, false
}

// wait is how long to wait before this attempt: the backoff, and what the realmd limit
// asks on top. In a fleet the supervisor books the login against every child's logins; if
// it cannot be reached this process's own window is used.
func (r *reconnector) wait(ctx context.Context) time.Duration {
	wait := backoff(r.used)
	if r.budget != nil {
		d, err := r.budget.reserve(ctx, wait)
		if err == nil {
			if d > wait {
				fmt.Fprintf(r.stderr, "reconnect: waiting %s for the fleet's share of realmd's login limit (%d logins per %s for all characters)\n",
					d.Round(time.Second), realmdThrottleLogins-1, realmdThrottleWindow.Round(time.Second))
			}
			return max(wait, d)
		}
		fmt.Fprintln(r.stderr, "reconnect: fleet login budget unavailable, using this process's own window:", err)
	}
	return max(wait, r.window.wait(time.Now()))
}

// login is one attempt: realmd, the world server, then in world. It has no capture tap
// (--reconnect and --capture do not combine: the new session key is not in the capture's
// redaction list).
func (r *reconnector) login(ctx context.Context) (*worldSession, error) {
	actx, cancel := context.WithTimeout(ctx, 30*time.Second)
	ticket, err := authLogin(actx, r.st.Realmd, r.account, r.password)
	cancel()
	if err != nil {
		return nil, err
	}
	realm, err := ticket.Pick(r.st.Realm)
	if err != nil {
		return nil, err
	}
	addr := realm.Address
	if r.st.WorldAddr != "" {
		addr = r.st.WorldAddr
	}
	wctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	conn, err := dialWorld(wctx, addr, ticket, nil, addonOption(r.st, ticket.Account, r.stderr))
	cancel()
	if err != nil {
		return nil, err
	}
	s := session.New(conn, sessionOptions(r.character, r.log, r.stderr, []string{ticket.Account, r.account}, r.password, r.planner, r.create))
	runErr := make(chan error, 1)
	go func() { runErr <- s.Run(context.Background()) }()
	timeout := time.NewTimer(reconnectReadyWait)
	defer timeout.Stop()
	select {
	case <-s.Ready():
		return &worldSession{s: s, conn: conn, runErr: runErr}, nil
	case err := <-runErr:
		if err == nil {
			err = errors.New("session ended before reaching the world")
		}
		return nil, err
	case <-timeout.C:
		conn.Close()
		<-runErr
		return nil, fmt.Errorf("world: not in world after %s", reconnectReadyWait)
	case <-ctx.Done():
		conn.Close()
		<-runErr
		return nil, ctx.Err()
	}
}
