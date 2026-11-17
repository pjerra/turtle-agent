package main

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/TortoiseBots/turtle-agent/internal/config"
	"github.com/TortoiseBots/turtle-agent/internal/rpc"
)

type fakeChild struct {
	mu      sync.Mutex
	done    chan struct{}
	code    int
	stopped bool
	killed  bool
	closed  bool
	onStop  int // exit code used when Stop is called; -1: ignore Stop
}

func newFake() *fakeChild { return &fakeChild{done: make(chan struct{})} }

func (f *fakeChild) exit(code int) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if !f.closed {
		f.code = code
		f.closed = true
		close(f.done)
	}
}
func (f *fakeChild) Stop() {
	f.mu.Lock()
	f.stopped = true
	ignore := f.onStop < 0
	f.mu.Unlock()
	if !ignore {
		f.exit(f.onStop)
	}
}
func (f *fakeChild) Kill()                 { f.mu.Lock(); f.killed = true; f.mu.Unlock(); f.exit(1) }
func (f *fakeChild) Done() <-chan struct{} { return f.done }
func (f *fakeChild) Code() int             { f.mu.Lock(); defer f.mu.Unlock(); return f.code }

type fakeFleet struct {
	mu       sync.Mutex
	children map[string]*fakeChild // by rpc address
	states   map[string]string
	started  []string
}

func newFakeFleet() *fakeFleet {
	return &fakeFleet{children: map[string]*fakeChild{}, states: map[string]string{}}
}

func (ff *fakeFleet) deps() fleetDeps {
	return fleetDeps{
		start: func(m fleetMember, w io.Writer) (fleetChild, error) {
			ff.mu.Lock()
			defer ff.mu.Unlock()
			c := newFake()
			ff.children[m.RPC] = c
			ff.started = append(ff.started, m.Character)
			io.WriteString(w, "world: authenticated\npartial")
			return c, nil
		},
		status: func(_ context.Context, addr string) (string, error) {
			ff.mu.Lock()
			defer ff.mu.Unlock()
			if s, ok := ff.states[addr]; ok {
				return s, nil
			}
			return "", io.EOF
		},
	}
}

func (ff *fakeFleet) setState(addr, s string) { ff.mu.Lock(); ff.states[addr] = s; ff.mu.Unlock() }
func (ff *fakeFleet) child(addr string) *fakeChild {
	ff.mu.Lock()
	defer ff.mu.Unlock()
	return ff.children[addr]
}

var party = []fleetMember{
	{Account: "ACC1", Character: "Tankone", RPC: "127.0.0.1:9001"},
	{Account: "ACC2", Character: "Healtwo", RPC: "127.0.0.1:9002"},
	{Account: "ACC3", Character: "Dpsthree", RPC: "127.0.0.1:9003"},
}

var fastOpts = fleetOpts{poll: 5 * time.Millisecond, wait: 2 * time.Second, stopGrace: time.Second}

// stopOpts is fastOpts with the old all-or-nothing behaviour (--stop-on-failure).
var stopOpts = func() fleetOpts { o := fastOpts; o.stopOnFailure = true; return o }()

// syncBuf lets the test read output while runFleet still writes.
type syncBuf struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (s *syncBuf) Write(p []byte) (int, error) { s.mu.Lock(); defer s.mu.Unlock(); return s.b.Write(p) }
func (s *syncBuf) String() string              { s.mu.Lock(); defer s.mu.Unlock(); return s.b.String() }

func waitFor(t *testing.T, what string, f func() bool) {
	t.Helper()
	for i := 0; i < 400; i++ {
		if f() {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %s", what)
}

func TestFleetAllInWorldThenCtrlCLogsOutEveryone(t *testing.T) {
	ff := newFakeFleet()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	var out, errOut syncBuf
	code := make(chan int, 1)
	go func() { code <- runFleet(ctx, party, ff.deps(), fastOpts, &out, &errOut) }()

	// Two are in world, one is still logging in: no table yet.
	ff.setState("127.0.0.1:9001", "in_world")
	ff.setState("127.0.0.1:9002", "in_world")
	ff.setState("127.0.0.1:9003", "logging_in")
	time.Sleep(50 * time.Millisecond)
	if out.String() != "" {
		t.Fatalf("table printed early: %q", out.String())
	}
	ff.setState("127.0.0.1:9003", "in_world")
	waitFor(t, "table", func() bool { return strings.Contains(out.String(), "Dpsthree") })
	for _, want := range []string{"CHARACTER", "RPC", "STATE", "Tankone", "127.0.0.1:9002", "in_world"} {
		if !strings.Contains(out.String(), want) {
			t.Errorf("table missing %q:\n%s", want, out.String())
		}
	}
	cancel() // Ctrl-C
	if c := <-code; c != exitOK {
		t.Fatalf("exit %d, stderr:\n%s", c, errOut.String())
	}
	for _, m := range party {
		if !ff.child(m.RPC).stopped {
			t.Errorf("%s was not told to log out", m.Character)
		}
	}
	// Child output is prefixed by character, one line at a time.
	if !strings.Contains(errOut.String(), "[Tankone] world: authenticated") {
		t.Errorf("child log not prefixed:\n%s", errOut.String())
	}
}

func TestFleetStopOnFailureStopsTheRestAndFails(t *testing.T) {
	ff := newFakeFleet()
	for _, m := range party {
		ff.setState(m.RPC, "in_world")
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	var out, errOut syncBuf
	code := make(chan int, 1)
	go func() { code <- runFleet(ctx, party, ff.deps(), stopOpts, &out, &errOut) }()
	waitFor(t, "table", func() bool { return strings.Contains(out.String(), "Dpsthree") })
	ff.child("127.0.0.1:9002").exit(exitDisconnected)
	if c := <-code; c != exitFleet {
		t.Fatalf("exit %d, want %d", c, exitFleet)
	}
	if !ff.child("127.0.0.1:9001").stopped || !ff.child("127.0.0.1:9003").stopped {
		t.Error("survivors were not stopped")
	}
	if !strings.Contains(errOut.String(), "Healtwo exited with code 6") {
		t.Errorf("stderr:\n%s", errOut.String())
	}
}

// A child whose own RPC logout did not complete (exit 10) does not stop the rest; the
// fleet still reports it in its exit code once everyone is done.
func TestFleetIncompleteLogoutDoesNotStopTheRest(t *testing.T) {
	ff := newFakeFleet()
	for _, m := range party {
		ff.setState(m.RPC, "in_world")
	}
	var out, errOut syncBuf
	code := make(chan int, 1)
	go func() { code <- runFleet(context.Background(), party, ff.deps(), fastOpts, &out, &errOut) }()
	waitFor(t, "table", func() bool { return strings.Contains(out.String(), "Dpsthree") })
	ff.child("127.0.0.1:9002").exit(exitLogoutIncomplete)
	time.Sleep(100 * time.Millisecond)
	select {
	case c := <-code:
		t.Fatalf("fleet ended with %d after one incomplete logout", c)
	default:
	}
	if ff.child("127.0.0.1:9001").stopped || ff.child("127.0.0.1:9003").stopped {
		t.Fatal("survivors stopped")
	}
	ff.child("127.0.0.1:9001").exit(0)
	ff.child("127.0.0.1:9003").exit(0)
	if c := <-code; c != exitFleet || !strings.Contains(errOut.String(), "Healtwo exited with code 10") {
		t.Fatalf("exit %d, stderr:\n%s", c, errOut.String())
	}
}

func TestFleetStopOnFailureChildFailsBeforeWorld(t *testing.T) {
	ff := newFakeFleet()
	var out, errOut syncBuf
	code := make(chan int, 1)
	go func() { code <- runFleet(context.Background(), party, ff.deps(), stopOpts, &out, &errOut) }()
	waitFor(t, "start", func() bool { return ff.child("127.0.0.1:9003") != nil })
	ff.child("127.0.0.1:9001").exit(exitAuth)
	if c := <-code; c != exitFleet {
		t.Fatalf("exit %d", c)
	}
	if !strings.Contains(out.String(), "exited") || !strings.Contains(errOut.String(), "Tankone failed before") {
		t.Errorf("out:\n%s\nerr:\n%s", out.String(), errOut.String())
	}
}

func TestFleetTimeoutFails(t *testing.T) {
	ff := newFakeFleet() // no status ever answers
	o := fastOpts
	o.wait = 30 * time.Millisecond
	var out, errOut bytes.Buffer
	if c := runFleet(context.Background(), party, ff.deps(), o, &out, &errOut); c != exitFleet {
		t.Fatalf("exit %d", c)
	}
	if !strings.Contains(out.String(), "starting") || !strings.Contains(errOut.String(), "not every character") {
		t.Errorf("out:\n%s\nerr:\n%s", out.String(), errOut.String())
	}
	for _, m := range party {
		if !ff.child(m.RPC).stopped {
			t.Errorf("%s left running", m.Character)
		}
	}
}

func TestFleetKillsAChildThatIgnoresStop(t *testing.T) {
	ff := newFakeFleet()
	for _, m := range party {
		ff.setState(m.RPC, "in_world")
	}
	d := ff.deps()
	inner := d.start
	d.start = func(m fleetMember, w io.Writer) (fleetChild, error) {
		c, err := inner(m, w)
		if m.Character == "Healtwo" {
			c.(*fakeChild).onStop = -1
		}
		return c, err
	}
	ctx, cancel := context.WithCancel(context.Background())
	o := fastOpts
	o.stopGrace = 30 * time.Millisecond
	var out, errOut syncBuf
	code := make(chan int, 1)
	go func() { code <- runFleet(ctx, party, d, o, &out, &errOut) }()
	waitFor(t, "table", func() bool { return strings.Contains(out.String(), "Dpsthree") })
	cancel()
	if c := <-code; c != exitFleet {
		t.Fatalf("exit %d", c)
	}
	if !ff.child("127.0.0.1:9002").killed {
		t.Error("stuck child not killed")
	}
}

func TestFleetCleanLogoutOfEveryoneExitsZero(t *testing.T) {
	ff := newFakeFleet()
	for _, m := range party {
		ff.setState(m.RPC, "in_world")
	}
	var out, errOut syncBuf
	code := make(chan int, 1)
	go func() { code <- runFleet(context.Background(), party, ff.deps(), fastOpts, &out, &errOut) }()
	waitFor(t, "table", func() bool { return strings.Contains(out.String(), "Dpsthree") })
	for _, m := range party {
		ff.child(m.RPC).exit(0) // each logged out through its own RPC
	}
	if c := <-code; c != exitOK {
		t.Fatalf("exit %d, stderr:\n%s", c, errOut.String())
	}
}

func writeFile(t *testing.T, name, body string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), name)
	if err := os.WriteFile(p, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	return p
}

func TestLoadFleetValidation(t *testing.T) {
	st := config.Settings{Accounts: map[string]string{"alpha": "pw-one", "BETA": "pw-two"}}
	ok := `{"members":[{"account":"ALPHA","character":"A","rpc":"localhost:9001"},{"account":"beta","character":"B","rpc":"[::1]:9002"}]}`
	ms, err := loadFleet(writeFile(t, "f.json", ok), &st)
	if err != nil {
		t.Fatal(err)
	}
	if ms[0].RPC != "127.0.0.1:9001" || ms[1].RPC != "[::1]:9002" {
		t.Fatalf("addresses not normalised: %+v", ms)
	}
	cases := map[string]string{
		"empty":         `{"members":[]}`,
		"unknown field": `{"members":[{"account":"alpha","character":"A","rpc":"127.0.0.1:9001","password":"x"}]}`,
		"missing":       `{"members":[{"account":"alpha","rpc":"127.0.0.1:9001"}]}`,
		"non-loopback":  `{"members":[{"account":"alpha","character":"A","rpc":"0.0.0.0:9001"}]}`,
		"port zero":     `{"members":[{"account":"alpha","character":"A","rpc":"127.0.0.1:0"}]}`,
		"dup port":      `{"members":[{"account":"alpha","character":"A","rpc":"127.0.0.1:9001"},{"account":"beta","character":"B","rpc":"localhost:9001"}]}`,
		"dup character": `{"members":[{"account":"alpha","character":"A","rpc":"127.0.0.1:9001"},{"account":"beta","character":"a","rpc":"127.0.0.1:9002"}]}`,
		"no account":    `{"members":[{"account":"gamma","character":"A","rpc":"127.0.0.1:9001"}]}`,
	}
	for name, body := range cases {
		_, err := loadFleet(writeFile(t, "f.json", body), &st)
		if err == nil {
			t.Errorf("%s: accepted", name)
			continue
		}
		if strings.Contains(err.Error(), "pw-") || strings.Contains(err.Error(), "gamma") {
			t.Errorf("%s: error leaks a secret or account: %v", name, err)
		}
	}
}

func TestFleetCmdUsageAndSecrets(t *testing.T) {
	var out, errOut bytes.Buffer
	if c := run(context.Background(), []string{"fleet"}, &out, &errOut); c != exitUsage {
		t.Fatalf("exit %d", c)
	}
	settings := writeFile(t, "settings.json", `{"realmd":"127.0.0.1:3724","accounts":{"ACC":"hunter2-pw"}}`)
	fl := writeFile(t, "fleet.json", `{"members":[{"account":"nope","character":"A","rpc":"127.0.0.1:9001"}]}`)
	errOut.Reset()
	if c := run(context.Background(), []string{"fleet", "--settings", settings, "--config", fl}, &out, &errOut); c != exitUsage {
		t.Fatalf("exit %d", c)
	}
	if s := out.String() + errOut.String(); strings.Contains(s, "hunter2-pw") || strings.Contains(s, "nope") {
		t.Fatalf("secret in output: %q", s)
	}
}

func TestPrefixWriterSplitsLines(t *testing.T) {
	var b bytes.Buffer
	pw := &prefixWriter{w: &b, mu: &sync.Mutex{}, prefix: "[X] "}
	io.WriteString(pw, "one\ntw")
	io.WriteString(pw, "o\n")
	if b.String() != "[X] one\n[X] two\n" {
		t.Fatalf("%q", b.String())
	}
}

func TestFleetStopOnFailureLaterFailureNotHiddenByEarlierCleanExit(t *testing.T) {
	ff := newFakeFleet()
	for _, m := range party {
		ff.setState(m.RPC, "in_world")
	}
	var out, errOut syncBuf
	code := make(chan int, 1)
	go func() { code <- runFleet(context.Background(), party, ff.deps(), stopOpts, &out, &errOut) }()
	waitFor(t, "table", func() bool { return strings.Contains(out.String(), "Dpsthree") })
	ff.child("127.0.0.1:9001").exit(0)
	time.Sleep(30 * time.Millisecond)
	ff.child("127.0.0.1:9002").exit(exitDisconnected)
	select {
	case c := <-code:
		if c != exitFleet {
			t.Fatalf("exit %d, want %d", c, exitFleet)
		}
	case <-time.After(time.Second):
		t.Fatal("fleet kept running after a failure")
	}
	if !ff.child("127.0.0.1:9003").stopped {
		t.Error("remaining child not stopped")
	}
	if !strings.Contains(errOut.String(), "Healtwo failed") {
		t.Errorf("stderr:\n%s", errOut.String())
	}
}

// By default one failed child is reported and the others keep running; the fleet exit
// code still says that a child failed.
func TestFleetFailedChildDoesNotStopTheRest(t *testing.T) {
	ff := newFakeFleet()
	for _, m := range party {
		ff.setState(m.RPC, "in_world")
	}
	var out, errOut syncBuf
	code := make(chan int, 1)
	go func() { code <- runFleet(context.Background(), party, ff.deps(), fastOpts, &out, &errOut) }()
	waitFor(t, "table", func() bool { return strings.Contains(out.String(), "Dpsthree") })
	ff.child("127.0.0.1:9002").exit(exitDisconnected)
	waitFor(t, "report", func() bool {
		return strings.Contains(errOut.String(), "Healtwo exited with code 6 (the server closed the connection)")
	})
	time.Sleep(50 * time.Millisecond)
	select {
	case c := <-code:
		t.Fatalf("fleet ended with %d after one failure", c)
	default:
	}
	if ff.child("127.0.0.1:9001").stopped || ff.child("127.0.0.1:9003").stopped {
		t.Fatal("survivors were stopped")
	}
	// The survivors log out cleanly later; the failure still decides the exit code.
	ff.child("127.0.0.1:9001").exit(0)
	ff.child("127.0.0.1:9003").exit(0)
	if c := <-code; c != exitFleet {
		t.Fatalf("exit %d, want %d", c, exitFleet)
	}
	if !strings.Contains(errOut.String(), "Tankone logged out") || !strings.Contains(errOut.String(), "1 of 3 character(s) exited with a failure") {
		t.Errorf("stderr:\n%s", errOut.String())
	}
	if strings.Count(errOut.String(), "Healtwo exited") != 1 {
		t.Errorf("failure reported more than once:\n%s", errOut.String())
	}
}

// A child that fails while the fleet is still logging in leaves the others to finish.
func TestFleetChildFailsBeforeWorldTheRestCarryOn(t *testing.T) {
	ff := newFakeFleet()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	var out, errOut syncBuf
	code := make(chan int, 1)
	go func() { code <- runFleet(ctx, party, ff.deps(), fastOpts, &out, &errOut) }()
	waitFor(t, "start", func() bool { return ff.child("127.0.0.1:9003") != nil })
	ff.child("127.0.0.1:9001").exit(exitAuth)
	ff.setState("127.0.0.1:9002", "in_world")
	ff.setState("127.0.0.1:9003", "in_world")
	waitFor(t, "table", func() bool { return strings.Contains(out.String(), "Dpsthree") })
	if !strings.Contains(out.String(), "exited (3)") || !strings.Contains(errOut.String(), "2 character(s) in world") ||
		!strings.Contains(errOut.String(), "Tankone exited with code 3 (realmd rejected the login)") {
		t.Errorf("out:\n%s\nerr:\n%s", out.String(), errOut.String())
	}
	cancel()
	if c := <-code; c != exitFleet {
		t.Fatalf("exit %d, want %d", c, exitFleet)
	}
	if !ff.child("127.0.0.1:9002").stopped {
		t.Error("Ctrl-C did not reach the survivors")
	}
}

func TestFleetStartErrorIsIsolated(t *testing.T) {
	ff := newFakeFleet()
	for _, m := range party {
		ff.setState(m.RPC, "in_world")
	}
	d := ff.deps()
	inner := d.start
	d.start = func(m fleetMember, w io.Writer) (fleetChild, error) {
		if m.Character == "Healtwo" {
			return nil, errors.New("exec: no such file")
		}
		return inner(m, w)
	}
	ctx, cancel := context.WithCancel(context.Background())
	var out, errOut syncBuf
	code := make(chan int, 1)
	go func() { code <- runFleet(ctx, party, d, fastOpts, &out, &errOut) }()
	waitFor(t, "table", func() bool { return strings.Contains(out.String(), "Dpsthree") })
	cancel()
	if c := <-code; c != exitFleet {
		t.Fatalf("exit %d", c)
	}
	if !strings.Contains(errOut.String(), "starting Healtwo: exec: no such file") || !strings.Contains(errOut.String(), "2 character(s) in world") {
		t.Errorf("stderr:\n%s", errOut.String())
	}
	if ff.child("127.0.0.1:9003") == nil || !ff.child("127.0.0.1:9003").stopped {
		t.Error("members after the failed start were not run, or not stopped")
	}
	// The same failure with --stop-on-failure ends the fleet before the next member starts.
	ff2 := newFakeFleet()
	d2 := ff2.deps()
	inner2 := d2.start
	d2.start = func(m fleetMember, w io.Writer) (fleetChild, error) {
		if m.Character == "Healtwo" {
			return nil, errors.New("exec: no such file")
		}
		return inner2(m, w)
	}
	if c := runFleet(context.Background(), party, d2, stopOpts, io.Discard, io.Discard); c != exitFleet {
		t.Fatalf("stop-on-failure exit %d", c)
	}
	if ff2.child("127.0.0.1:9003") != nil {
		t.Error("a member started after the failed one")
	}
}

func TestFleetEveryChildFailingEndsTheFleet(t *testing.T) {
	ff := newFakeFleet()
	var out, errOut syncBuf
	code := make(chan int, 1)
	go func() { code <- runFleet(context.Background(), party, ff.deps(), fastOpts, &out, &errOut) }()
	waitFor(t, "start", func() bool { return ff.child("127.0.0.1:9003") != nil })
	for _, m := range party {
		ff.child(m.RPC).exit(exitWorld)
	}
	if c := <-code; c != exitFleet || !strings.Contains(errOut.String(), "every character exited") {
		t.Fatalf("exit %d, stderr:\n%s", c, errOut.String())
	}
}

// The fleet paces its starts under realmd's login limit: with 2 logins allowed per window
// the third member waits for the first to leave it, and says so.
func TestFleetStartsWaitForTheLoginBudget(t *testing.T) {
	ff := newFakeFleet()
	for _, m := range party {
		ff.setState(m.RPC, "in_world")
	}
	o := fastOpts
	o.budget = newLoginBudget(2, 300*time.Millisecond)
	var out, errOut syncBuf
	begin := time.Now()
	ctx, cancel := context.WithCancel(context.Background())
	code := make(chan int, 1)
	go func() { code <- runFleet(ctx, party, ff.deps(), o, &out, &errOut) }()
	waitFor(t, "third start", func() bool { return ff.child("127.0.0.1:9003") != nil })
	if d := time.Since(begin); d < 290*time.Millisecond {
		t.Errorf("third member started after %s, before the first left the window", d)
	}
	if !strings.Contains(errOut.String(), "waiting") || !strings.Contains(errOut.String(), "to log in Dpsthree") {
		t.Errorf("no budget line:\n%s", errOut.String())
	}
	if strings.Contains(errOut.String(), "to log in Healtwo") {
		t.Errorf("second member waited:\n%s", errOut.String())
	}
	cancel()
	<-code
}

func TestFleetCtrlCEndsTheBudgetWait(t *testing.T) {
	ff := newFakeFleet()
	o := fastOpts
	o.budget = newLoginBudget(1, time.Hour)
	ctx, cancel := context.WithCancel(context.Background())
	var errOut syncBuf
	code := make(chan int, 1)
	go func() { code <- runFleet(ctx, party, ff.deps(), o, io.Discard, &errOut) }()
	waitFor(t, "budget wait", func() bool { return strings.Contains(errOut.String(), "waiting") })
	cancel()
	select {
	case <-code:
	case <-time.After(2 * time.Second):
		t.Fatal("fleet kept waiting for the budget after Ctrl-C")
	}
	if ff.child("127.0.0.1:9001") == nil || !ff.child("127.0.0.1:9001").stopped {
		t.Error("the started member was not stopped")
	}
}

func TestChildArgsAndReconnectOverride(t *testing.T) {
	two, zero := 2, 0
	plain := fleetMember{Account: "ACC", Character: "Plain", RPC: "127.0.0.1:9001"}
	over := fleetMember{Account: "ACC2", Character: "Over", RPC: "127.0.0.1:9002", Reconnect: &two}
	off := fleetMember{Account: "ACC3", Character: "Off", RPC: "127.0.0.1:9003", Reconnect: &zero}
	for _, c := range []struct {
		m     fleetMember
		fleet int
		addr  string
		want  string
	}{
		{plain, 0, "", "login --config /s.json --rpc 127.0.0.1:9001 ACC Plain"},
		{plain, 0, "127.0.0.1:5000", "login --config /s.json --rpc 127.0.0.1:9001 --login-budget 127.0.0.1:5000 ACC Plain"},
		{plain, 3, "127.0.0.1:5000", "login --config /s.json --rpc 127.0.0.1:9001 --reconnect 3 --login-budget 127.0.0.1:5000 ACC Plain"},
		{over, 0, "127.0.0.1:5000", "login --config /s.json --rpc 127.0.0.1:9002 --reconnect 2 --login-budget 127.0.0.1:5000 ACC2 Over"},
		{off, 5, "127.0.0.1:5000", "login --config /s.json --rpc 127.0.0.1:9003 --login-budget 127.0.0.1:5000 ACC3 Off"},
		{over, 0, "", "login --config /s.json --rpc 127.0.0.1:9002 --reconnect 2 ACC2 Over"},
	} {
		if got := strings.Join(childArgs("/s.json", c.m, c.fleet, c.addr), " "); got != c.want {
			t.Errorf("got  %s\nwant %s", got, c.want)
		}
	}
}

func TestLoadFleetReconnectField(t *testing.T) {
	ms, err := loadFleet(writeFile(t, "f.json", `{"members":[{"account":"A","character":"A","rpc":"127.0.0.1:9001","reconnect":4},{"account":"B","character":"B","rpc":"127.0.0.1:9002"}]}`), nil)
	if err != nil || ms[0].Reconnect == nil || *ms[0].Reconnect != 4 || ms[1].Reconnect != nil {
		t.Fatalf("%v %+v", err, ms)
	}
	_, err = loadFleet(writeFile(t, "f.json", `{"members":[{"account":"A","character":"A","rpc":"127.0.0.1:9001","reconnect":-1}]}`), nil)
	if err == nil || !strings.Contains(err.Error(), "reconnect") {
		t.Fatalf("negative reconnect: %v", err)
	}
}

func TestFleetCmdRejectsNegativeReconnect(t *testing.T) {
	var out, errOut bytes.Buffer
	if c := fleetCmd(context.Background(), []string{"--config", "x.json", "--reconnect", "-1"}, &out, &errOut); c != exitUsage || !strings.Contains(errOut.String(), "--reconnect") {
		t.Fatalf("%d %q", c, errOut.String())
	}
}

func TestFleetTokenAndStatusHeader(t *testing.T) {
	dir := t.TempDir()
	settings := filepath.Join(dir, "settings.json")
	if tok := fleetToken(config.Settings{}, settings, "127.0.0.1:8771"); tok != "" {
		t.Fatalf("no file yet: %q", tok)
	}
	if _, err := rpc.WriteTokenFile(rpc.TokenFile(settings, 8771), "child-one-token-0123"); err != nil {
		t.Fatal(err)
	}
	if _, err := rpc.WriteTokenFile(rpc.TokenFile(settings, 8772), "child-two-token-0123"); err != nil {
		t.Fatal(err)
	}
	if a, b := fleetToken(config.Settings{}, settings, "127.0.0.1:8771"), fleetToken(config.Settings{}, settings, "[::1]:8772"); a != "child-one-token-0123" || b != "child-two-token-0123" {
		t.Fatalf("one token per child: %q %q", a, b)
	}
	if tok := fleetToken(config.Settings{RPCToken: "shared-token-0123456"}, settings, "127.0.0.1:8771"); tok != "shared-token-0123456" {
		t.Fatalf("shared token: %q", tok)
	}
	if tok := fleetToken(config.Settings{RPCAuth: "off"}, settings, "127.0.0.1:8771"); tok != "" {
		t.Fatalf("auth off: %q", tok)
	}

	srv := rpc.NewServer()
	srv.SetToken("child-one-token-0123")
	srv.Register("status", rpc.Typed(func(ctx context.Context, _ struct{}) (map[string]string, error) {
		return map[string]string{"state": "in_world"}, nil
	}))
	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()
	addr := strings.TrimPrefix(ts.URL, "http://")
	if st, err := rpcStatus(context.Background(), addr, "child-one-token-0123"); err != nil || st != "in_world" {
		t.Fatalf("with token: %q %v", st, err)
	}
	if _, err := rpcStatus(context.Background(), addr, ""); err == nil {
		t.Fatal("status answered without a token")
	}
}

// With --stop-on-failure a child that fails while the start loop waits for the login
// budget stops the fleet at once, and the member waiting for its slot never starts.
func TestFleetStopOnFailureActsDuringTheBudgetWait(t *testing.T) {
	ff := newFakeFleet()
	o := stopOpts
	o.budget = newLoginBudget(1, time.Hour)
	var errOut syncBuf
	code := make(chan int, 1)
	go func() { code <- runFleet(context.Background(), party, ff.deps(), o, io.Discard, &errOut) }()
	waitFor(t, "budget wait", func() bool { return strings.Contains(errOut.String(), "waiting") })
	ff.child("127.0.0.1:9001").exit(exitDisconnected)
	select {
	case c := <-code:
		if c != exitFleet {
			t.Fatalf("exit %d", c)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("fleet kept waiting for the budget after a child failed")
	}
	if ff.child("127.0.0.1:9002") != nil {
		t.Error("the waiting member started")
	}
	if !strings.Contains(errOut.String(), "Tankone failed; stopping the rest") {
		t.Errorf("stderr:\n%s", errOut.String())
	}
}
