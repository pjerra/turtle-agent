package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/TortoiseBots/turtle-agent/internal/auth"
	"github.com/TortoiseBots/turtle-agent/internal/opcode"
	"github.com/TortoiseBots/turtle-agent/internal/packet"
	"github.com/TortoiseBots/turtle-agent/internal/worldconn/worldtest"
)

// setReconnectTimings shortens the reconnect backoff and the realmd throttle window.
func setReconnectTimings(t *testing.T, backoff, window time.Duration) {
	oldB, oldMax, oldW := reconnectBackoff, reconnectMaxBackoff, realmdThrottleWindow
	reconnectBackoff, reconnectMaxBackoff, realmdThrottleWindow = backoff, 4*backoff, window
	t.Cleanup(func() { reconnectBackoff, reconnectMaxBackoff, realmdThrottleWindow = oldB, oldMax, oldW })
}

// authScript stubs realmd: answers[i] is the result of the i-th login (nil: the ticket);
// past the end every login succeeds. It returns the number of logins so far.
func authScript(t *testing.T, tk *auth.Ticket, answers ...error) func() int {
	var mu sync.Mutex
	n := 0
	old := authLogin
	t.Cleanup(func() { authLogin = old })
	authLogin = func(context.Context, string, string, string) (*auth.Ticket, error) {
		mu.Lock()
		defer mu.Unlock()
		n++
		if n <= len(answers) && answers[n-1] != nil {
			return nil, answers[n-1]
		}
		return tk, nil
	}
	return func() int { mu.Lock(); defer mu.Unlock(); return n }
}

// worldSessions serves one scripted world session per script, in order, each logged in to
// the world first; it returns the config file and the scripts' joined result.
func worldSessions(t *testing.T, tk *auth.Ticket, scripts ...func(srv *worldtest.Server) error) (string, <-chan error) {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { ln.Close() })
	res := make(chan error, 1)
	go func() {
		var errs []error
		for _, script := range scripts {
			errs = append(errs, func() error {
				nc, err := ln.Accept()
				if err != nil {
					return err
				}
				defer nc.Close()
				srv, err := worldtest.Accept(nc, tk.KeyBytes(), worldtest.Options{})
				if err != nil {
					return err
				}
				defer srv.Close()
				step := func(op uint16) error { _, err := srv.Expect(op, 5*time.Second); return err }
				if err := step(opcode.CMSGCharEnum); err != nil {
					return err
				}
				srv.Send(opcode.SMSGCharEnum, e2eChars("Harnessb"))
				if err := step(opcode.CMSGPlayerLogin); err != nil {
					return err
				}
				srv.Send(opcode.SMSGLoginVerifyWorld, packet.NewWriter().U32(0).F32(-8913).F32(-136).F32(82).F32(1.5).Bytes())
				if err := step(opcode.CMSGSetActiveMover); err != nil {
					return err
				}
				return script(srv)
			}())
		}
		res <- errors.Join(errs...)
	}()
	return settingsFile(t, `{"realmd":"127.0.0.1:1","world_addr":"`+ln.Addr().String()+`","rpc":"127.0.0.1:0","rpc_token":"`+testRPCToken+`","accounts":{"harness":"`+safetyPassword+`"}}`), res
}

func reconnectTicket() *auth.Ticket {
	tk := &auth.Ticket{Account: "HARNESS", Realms: []auth.Realm{{Name: "Tortoise", Address: "127.0.0.1:1"}}}
	for i := range tk.Key {
		tk.Key[i] = byte(0x41 + i)
	}
	return tk
}

func dropAfter(d time.Duration) func(*worldtest.Server) error {
	return func(*worldtest.Server) error { time.Sleep(d); return nil } // the deferred Close drops the socket
}

func logoutClean(srv *worldtest.Server) error {
	_, err := answerLogouts(srv, func(int) uint32 { return 0 })
	return err
}

// rpcAddr waits for the "rpc: listening" line.
func rpcAddr(t *testing.T, stderr *syncBuffer) string {
	t.Helper()
	for deadline := time.Now().Add(5 * time.Second); time.Now().Before(deadline); time.Sleep(20 * time.Millisecond) {
		if _, rest, ok := strings.Cut(stderr.String(), "rpc: listening on http://"); ok {
			addr, _, _ := strings.Cut(rest, "/rpc")
			return addr
		}
	}
	t.Fatalf("no rpc address: %q", stderr.String())
	return ""
}

// --reconnect N: after the server drops the session, the agent logs in again, the RPC keeps
// its address and its event log (cursors stay valid), a reconnected event marks the new
// session, and --stay then logs out cleanly.
func TestReconnectAfterDrop(t *testing.T) {
	setReconnectTimings(t, 50*time.Millisecond, 300*time.Second)
	tk := reconnectTicket()
	logins := authScript(t, tk)
	cfg, done := worldSessions(t, tk, dropAfter(200*time.Millisecond), logoutClean)
	var stderr syncBuffer
	codeC := make(chan int, 1)
	go func() {
		codeC <- run(context.Background(), []string{"login", "--config", cfg, "--reconnect", "2", "--stay", "2s", "harness", "Harnessb"}, &bytes.Buffer{}, &stderr)
	}()
	addr := rpcAddr(t, &stderr)
	var page map[string]any
	for deadline := time.Now().Add(5 * time.Second); time.Now().Before(deadline); time.Sleep(20 * time.Millisecond) {
		out := rpcCall(t, addr, "events", `{"since":0,"kinds":["session"]}`)
		if res, ok := out["result"].(map[string]any); ok {
			if raw, _ := json.Marshal(res); strings.Contains(string(raw), `"state":"reconnected"`) {
				page = res
				break
			}
		}
	}
	if page == nil {
		t.Fatalf("no reconnected event (stderr %q)", stderr.String())
	}
	var states []string
	for _, e := range page["events"].([]any) {
		states = append(states, e.(map[string]any)["data"].(map[string]any)["state"].(string))
	}
	if got := strings.Join(states, ","); got != "in_world,disconnected,in_world,reconnected" {
		t.Fatalf("session states %s", got)
	}
	st := rpcCall(t, addr, "status", "{}")["result"].(map[string]any)
	if st["state"] != "in_world" {
		t.Fatalf("status after reconnect %v", st)
	}
	if code := <-codeC; code != exitOK {
		t.Fatalf("code %d, stderr %q", code, stderr.String())
	}
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	if n := logins(); n != 2 {
		t.Fatalf("%d realmd logins, want 2", n)
	}
	out := stderr.String()
	if !strings.Contains(out, "reconnect: attempt 1 of 2") || !strings.Contains(out, "reconnect: in world again") {
		t.Fatalf("stderr %q", out)
	}
	for _, secret := range []string{"HARNESS", "harness", safetyPassword} {
		if strings.Contains(out, secret) {
			t.Errorf("%q leaked: %q", secret, out)
		}
	}
}

// The budget counts login attempts: failed ones use it up, and the agent then falls back
// to the disconnect grace and exit 6. A realmd answer that cannot change (wrong password,
// banned) stops at once, so it never walks into realmd's failed-login autoban
// (AuthSocket.cpp:1042-1054).
func TestReconnectBudgetAndHardFailures(t *testing.T) {
	setTimings(t, 35*time.Second, 2*time.Second, 100*time.Millisecond)
	setReconnectTimings(t, 20*time.Millisecond, 300*time.Second)
	t.Run("budget", func(t *testing.T) {
		tk := reconnectTicket()
		netErr := errors.New("realmd: connect 127.0.0.1:1: connection refused")
		logins := authScript(t, tk, nil, netErr, netErr)
		cfg, done := worldSessions(t, tk, dropAfter(100*time.Millisecond))
		code, _, errOut := runCLI("login", "--config", cfg, "--reconnect", "2", "harness", "Harnessb")
		if code != exitDisconnected || !strings.Contains(errOut, "reconnect: no attempts left") {
			t.Fatalf("code %d stderr %q", code, errOut)
		}
		if err := <-done; err != nil {
			t.Fatal(err)
		}
		if n := logins(); n != 3 {
			t.Fatalf("%d realmd logins, want 1 + 2", n)
		}
	})
	t.Run("hard", func(t *testing.T) {
		tk := reconnectTicket()
		logins := authScript(t, tk, nil, &auth.AuthError{Stage: "proof", Code: auth.ResultUnknownAccount})
		cfg, done := worldSessions(t, tk, dropAfter(100*time.Millisecond))
		code, _, errOut := runCLI("login", "--config", cfg, "--reconnect", "3", "harness", "Harnessb")
		if code != exitDisconnected || !strings.Contains(errOut, "reconnect: giving up") {
			t.Fatalf("code %d stderr %q", code, errOut)
		}
		if err := <-done; err != nil {
			t.Fatal(err)
		}
		if n := logins(); n != 2 {
			t.Fatalf("%d realmd logins, want 2", n)
		}
	})
	t.Run("off by default", func(t *testing.T) {
		tk := reconnectTicket()
		logins := authScript(t, tk)
		cfg, done := worldSessions(t, tk, dropAfter(100*time.Millisecond))
		if code, _, errOut := runCLI("login", "--config", cfg, "harness", "Harnessb"); code != exitDisconnected || strings.Contains(errOut, "reconnect") {
			t.Fatalf("code %d stderr %q", code, errOut)
		}
		if err := <-done; err != nil {
			t.Fatal(err)
		}
		if n := logins(); n != 1 {
			t.Fatalf("%d realmd logins without --reconnect", n)
		}
	})
}

func TestReconnectFlagChecks(t *testing.T) {
	var calls int
	stubLogin(t, &auth.Ticket{}, &calls)
	cfg := settingsFile(t, unreachable)
	if code, _, errOut := runCLI("login", "--config", cfg, "--reconnect", "-1", "harness", "Harnessb"); code != exitUsage || !strings.Contains(errOut, "--reconnect") {
		t.Fatalf("negative: %d %q", code, errOut)
	}
	if code, _, errOut := runCLI("login", "--config", cfg, "--reconnect", "1", "--capture", t.TempDir()+"/c.twcap", "harness", "Harnessb"); code != exitUsage || !strings.Contains(errOut, "--capture") {
		t.Fatalf("with capture: %d %q", code, errOut)
	}
	if calls != 0 {
		t.Fatalf("realmd login ran %d times", calls)
	}
}

// realmd refuses a 15th distinct account from one IP within 300 s with WOW_FAIL_DB_BUSY
// (AuthSocket.cpp:439-456). The agent keeps its own logins under that rate, and after a
// busy answer waits the whole window before the next try.
func TestLoginWindow(t *testing.T) {
	setReconnectTimings(t, time.Second, 300*time.Second)
	now := time.Unix(1000, 0)
	var w loginWindow
	for i := 0; i < realmdThrottleLogins-1; i++ {
		if d := w.wait(now); d != 0 {
			t.Fatalf("login %d waits %s", i+1, d)
		}
		w.note(now.Add(time.Duration(i) * time.Second))
	}
	if d := w.wait(now.Add(20 * time.Second)); d != 280*time.Second {
		t.Fatalf("login %d waits %s, want until the first leaves the window", realmdThrottleLogins, d)
	}
	if d := w.wait(now.Add(301 * time.Second)); d != 0 {
		t.Fatalf("after the window: %s", d)
	}
	var b loginWindow
	b.note(now)
	b.busy(now.Add(time.Second))
	if d := b.wait(now.Add(2 * time.Second)); d != 299*time.Second {
		t.Fatalf("after a busy answer: %s", d)
	}
	for attempt, want := range map[int]time.Duration{1: time.Second, 2: 2 * time.Second, 3: 4 * time.Second, 9: 4 * time.Second} {
		if d := backoff(attempt); d != want {
			t.Fatalf("backoff(%d) = %s, want %s", attempt, d, want)
		}
	}
}

// In a fleet the reconnect books its login with the supervisor's budget, which counts
// every child's logins: here a slot taken by another child pushes the reconnect back, and
// the log says why it waits.
func TestReconnectWaitsForTheFleetBudget(t *testing.T) {
	setReconnectTimings(t, 20*time.Millisecond, 300*time.Second)
	budget := newLoginBudget(1, 700*time.Millisecond)
	budget.reserve(time.Now()) // another child's login, just now
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	baddr, stop, err := serveLoginBudget(ctx, budget, testBudgetToken)
	if err != nil {
		t.Fatal(err)
	}
	defer stop()
	t.Setenv(budgetTokenEnv, testBudgetToken)
	tk := reconnectTicket()
	logins := authScript(t, tk)
	cfg, done := worldSessions(t, tk, dropAfter(100*time.Millisecond), logoutClean)
	code, _, errOut := runCLI("login", "--config", cfg, "--reconnect", "1", "--login-budget", baddr, "--stay", "2s", "harness", "Harnessb")
	if code != exitOK {
		t.Fatalf("code %d stderr %q", code, errOut)
	}
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	if n := logins(); n != 2 {
		t.Fatalf("%d realmd logins, want 2", n)
	}
	if !strings.Contains(errOut, "fleet's share of realmd's login limit") || !strings.Contains(errOut, "reconnect: in world again") {
		t.Fatalf("stderr %q", errOut)
	}
}

// If the supervisor's budget cannot be reached the child uses its own window.
func TestReconnectFallsBackWithoutTheFleetBudget(t *testing.T) {
	setReconnectTimings(t, 20*time.Millisecond, 300*time.Second)
	t.Setenv(budgetTokenEnv, testBudgetToken)
	tk := reconnectTicket()
	logins := authScript(t, tk)
	cfg, done := worldSessions(t, tk, dropAfter(100*time.Millisecond), logoutClean)
	code, _, errOut := runCLI("login", "--config", cfg, "--reconnect", "1", "--login-budget", "127.0.0.1:1", "--stay", "2s", "harness", "Harnessb")
	if code != exitOK || !strings.Contains(errOut, "fleet login budget unavailable") {
		t.Fatalf("code %d stderr %q", code, errOut)
	}
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	if n := logins(); n != 2 {
		t.Fatalf("%d realmd logins, want 2", n)
	}
}

func TestLoginBudgetFlagCheck(t *testing.T) {
	var calls int
	stubLogin(t, &auth.Ticket{}, &calls)
	cfg := settingsFile(t, unreachable)
	t.Setenv(budgetTokenEnv, "")
	if code, _, errOut := runCLI("login", "--config", cfg, "--login-budget", "127.0.0.1:5000", "harness", "Harnessb"); code != exitUsage || !strings.Contains(errOut, budgetTokenEnv) {
		t.Fatalf("no token: %d %q", code, errOut)
	}
	t.Setenv(budgetTokenEnv, testBudgetToken)
	for _, bad := range []string{"not-an-address", "10.0.0.1:5000", "127.0.0.1:0"} {
		if code, _, errOut := runCLI("login", "--config", cfg, "--reconnect", "1", "--login-budget", bad, "harness", "Harnessb"); code != exitUsage || !strings.Contains(errOut, "--login-budget") {
			t.Fatalf("%s: %d %q", bad, code, errOut)
		}
	}
	if calls != 0 {
		t.Fatalf("realmd login ran %d times", calls)
	}
}

// realmd's busy answer at the very first login closes the window for the whole fleet,
// not only for the child that got it.
func TestFirstLoginBusyIsReportedToTheFleetBudget(t *testing.T) {
	budget := newLoginBudget(14, 300*time.Second)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	baddr, stop, err := serveLoginBudget(ctx, budget, testBudgetToken)
	if err != nil {
		t.Fatal(err)
	}
	defer stop()
	t.Setenv(budgetTokenEnv, testBudgetToken)
	tk := reconnectTicket()
	authScript(t, tk, &auth.AuthError{Stage: "challenge", Code: auth.ResultDBBusy})
	cfg, _ := worldSessions(t, tk)
	code, _, errOut := runCLI("login", "--config", cfg, "--login-budget", baddr, "harness", "Harnessb")
	if code != exitAuth {
		t.Fatalf("code %d stderr %q", code, errOut)
	}
	budget.mu.Lock()
	until := budget.busyUntil
	budget.mu.Unlock()
	if time.Until(until) < 290*time.Second {
		t.Fatalf("busy until %s from now", time.Until(until))
	}
	if next := budget.reserve(time.Now()); time.Until(next) < 290*time.Second {
		t.Fatalf("next fleet login in %s", time.Until(next))
	}
}
