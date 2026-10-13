package main

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"text/tabwriter"
	"time"

	"github.com/pjerra/turtle-agent/internal/config"
	"github.com/pjerra/turtle-agent/internal/netutil"
	"github.com/pjerra/turtle-agent/internal/rpc"
	"github.com/pjerra/turtle-agent/internal/session"
)

// exitFleet: a child failed or the fleet did not reach the world in time.
const exitFleet = 9

// fleetMember is one entry of the fleet file. The account is a key into the settings
// file, which holds the password; the fleet file never carries a secret.
type fleetMember struct {
	Account   string `json:"account"`
	Character string `json:"character"`
	RPC       string `json:"rpc"`
	// Reconnect overrides the fleet's --reconnect for this member (0 turns it off).
	Reconnect *int `json:"reconnect,omitempty"`
	// Create is the login --create spec ("race,class[,gender]") for this member.
	Create string `json:"create,omitempty"`
}

type fleetFile struct {
	Members []fleetMember `json:"members"`
}

// loadFleet reads and validates the fleet file: every entry needs an account, a
// character and a loopback RPC address with a fixed port; characters
// and RPC ports must be distinct. If settings is non-nil each account must exist
// in it (the error does not name the account).
func loadFleet(path string, settings *config.Settings) ([]fleetMember, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("fleet %s: %w", path, err)
	}
	var f fleetFile
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&f); err != nil {
		detail := ""
		if msg := err.Error(); strings.HasPrefix(msg, "json: unknown field ") {
			detail = ": " + strings.TrimPrefix(msg, "json: ")
		}
		return nil, fmt.Errorf("fleet %s is not valid fleet JSON%s", path, detail)
	}
	if len(f.Members) == 0 {
		return nil, errors.New(`fleet: "members" must list at least one entry`)
	}
	chars := map[string]bool{}
	ports := map[int]bool{}
	for i := range f.Members {
		m := &f.Members[i]
		n := i + 1
		if m.Account == "" || m.Character == "" || m.RPC == "" {
			return nil, fmt.Errorf("fleet: member %d needs account, character and rpc", n)
		}
		host, port, err := netutil.CheckLoopbackAddr(m.RPC)
		if err != nil {
			return nil, fmt.Errorf("fleet: member %d rpc %w", n, err)
		}
		if port == 0 {
			return nil, fmt.Errorf("fleet: member %d rpc needs a fixed port (the supervisor polls it)", n)
		}
		// Ports must differ outright: 127.0.0.1:N and ::1:N are different sockets, but a
		// fleet that mixes them is almost surely a typo.
		if ports[port] {
			return nil, fmt.Errorf("fleet: member %d rpc port %d is used twice", n, port)
		}
		ports[port] = true
		m.RPC = net.JoinHostPort(host, strconv.Itoa(port))
		if m.Reconnect != nil && *m.Reconnect < 0 {
			return nil, fmt.Errorf("fleet: member %d reconnect must be 0 (off) or more", n)
		}
		if m.Create != "" {
			if _, err := session.ParseCreateSpec(m.Create); err != nil {
				return nil, fmt.Errorf("fleet: member %d (%s): %w", n, m.Character, err)
			}
		}
		key := strings.ToLower(m.Character)
		if chars[key] {
			return nil, fmt.Errorf("fleet: character %q is listed twice", m.Character)
		}
		chars[key] = true
		if settings != nil {
			if _, err := settings.Password(m.Account); err != nil {
				return nil, fmt.Errorf("fleet: member %d (%s): %w", n, m.Character, err)
			}
		}
	}
	return f.Members, nil
}

// fleetChild is a running login process (or a fake in tests).
type fleetChild interface {
	// Stop asks the child to log out cleanly (an interrupt).
	Stop()
	// Kill ends it at once.
	Kill()
	// Done closes when the child has exited; Code is valid after that.
	Done() <-chan struct{}
	Code() int
}

type fleetDeps struct {
	start func(m fleetMember, stderr io.Writer) (fleetChild, error)
	// status returns the session state ("in_world", ...) of the agent on addr.
	status func(ctx context.Context, addr string) (string, error)
}

type fleetOpts struct {
	stagger, poll, wait, stopGrace time.Duration
	// stopOnFailure restores the old all-or-nothing fleet: the first child that fails
	// stops the rest. By default a failed child is reported and the others keep running.
	stopOnFailure bool
	// budget paces the children's logins under realmd's per-IP limit; nil: a new one.
	budget *loginBudget
}

func fleetCmd(ctx context.Context, args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("fleet", flag.ContinueOnError)
	fs.SetOutput(stderr)
	fleetPath := fs.String("config", "", "fleet file: {\"members\":[{\"account\",\"character\",\"rpc\"}]} (required)")
	settingsPath := fs.String("settings", config.DefaultPath(), "settings file (realmd address, accounts)")
	stagger := fs.Duration("stagger", 2*time.Second, "pause between starting children (realmd throttles logins)")
	wait := fs.Duration("wait", 2*time.Minute, "give up if not every character is in world after this long")
	reconnects := fs.Int("reconnect", 0, "each child logs in again up to N times after an unplanned disconnect (0: off; a member's \"reconnect\" overrides)")
	stopOnFailure := fs.Bool("stop-on-failure", false, "stop every child when one fails (default: report it and keep the others running)")
	if err := fs.Parse(args); err != nil {
		return exitUsage
	}
	if *fleetPath == "" || fs.NArg() != 0 {
		fmt.Fprintln(stderr, "usage: turtle-agent fleet --config FILE [--settings FILE] [--stagger D] [--wait D] [--reconnect N] [--stop-on-failure]")
		return exitUsage
	}
	if *reconnects < 0 {
		fmt.Fprintln(stderr, "fleet: --reconnect must be 0 (off) or more")
		return exitUsage
	}
	st, err := config.Load(*settingsPath)
	if err != nil {
		fmt.Fprintln(stderr, "config:", err)
		return exitUsage
	}
	members, err := loadFleet(*fleetPath, &st)
	if err != nil {
		fmt.Fprintln(stderr, err)
		return exitUsage
	}
	exe, err := os.Executable()
	if err != nil {
		fmt.Fprintln(stderr, "fleet:", err)
		return exitUsage
	}
	// Every child shares the supervisor's login budget (starts, reconnects and busy answers),
	// so the fleet as a whole stays under realmd's limit. Its port needs a token, which the
	// children get in their environment.
	budget := newLoginBudget(realmdThrottleLogins-1, realmdThrottleWindow)
	budgetToken, err := newBudgetToken()
	if err != nil {
		fmt.Fprintln(stderr, "fleet: login budget:", err)
		return exitUsage
	}
	budgetAddr, stopBudget, err := serveLoginBudget(ctx, budget, budgetToken)
	if err != nil {
		fmt.Fprintln(stderr, "fleet: login budget:", err)
		return exitUsage
	}
	defer stopBudget()
	// Pdeathsig (see detachProcessGroup) fires when the thread that started a child exits,
	// so the supervisor stays on one OS thread.
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()
	deps := fleetDeps{
		start: func(m fleetMember, w io.Writer) (fleetChild, error) {
			return startProcess(exe, childArgs(*settingsPath, m, *reconnects, budgetAddr), []string{budgetTokenEnv + "=" + budgetToken}, w)
		},
		status: func(ctx context.Context, addr string) (string, error) {
			return rpcStatus(ctx, addr, fleetToken(st, *settingsPath, addr))
		},
	}
	return runFleet(ctx, members, deps, fleetOpts{stagger: *stagger, poll: 500 * time.Millisecond, wait: *wait, stopGrace: 60 * time.Second,
		stopOnFailure: *stopOnFailure, budget: budget}, stdout, stderr)
}

// exitReason says in words what a child's exit code means (the README's exit-code table).
func exitReason(code int) string {
	switch code {
	case exitOK:
		return "logged out"
	case exitUsage:
		return "bad flags, arguments or settings"
	case exitAuth:
		return "realmd rejected the login"
	case exitRealm:
		return "realm missing or offline"
	case exitWorld:
		return "world server rejected the session or the character"
	case exitDisconnected:
		return "the server closed the connection"
	case exitLogoutRefused:
		return "the server kept refusing the logout"
	case exitLogoutIncomplete:
		return "the logout did not complete"
	}
	return ""
}

// goneChild stands in for a child that could not be started: already exited, failed.
type goneChild struct{ done chan struct{} }

func newGoneChild() goneChild {
	c := goneChild{done: make(chan struct{})}
	close(c.done)
	return c
}
func (goneChild) Stop()                   {}
func (goneChild) Kill()                   {}
func (c goneChild) Done() <-chan struct{} { return c.done }
func (goneChild) Code() int               { return 1 }

// runFleet starts every member, waits until all are in world, prints the table, then
// supervises until ctx ends (Ctrl-C: every child is stopped and logs out) or every child
// has exited. A child that fails is reported and the others keep running; with
// o.stopOnFailure the rest are stopped too. It returns 0 only if every child exited 0.
// Starts are paced by o.budget so the fleet stays under realmd's per-IP login limit.
func runFleet(ctx context.Context, members []fleetMember, deps fleetDeps, o fleetOpts, stdout, stderr io.Writer) int {
	var mu sync.Mutex // serialises writes to stderr from the children's prefixers
	children := make([]fleetChild, len(members))
	states := make([]string, len(members))
	reported := make([]bool, len(members))
	for i := range states {
		states[i] = "starting"
	}
	if o.budget == nil {
		o.budget = newLoginBudget(realmdThrottleLogins-1, realmdThrottleWindow)
	}

	stopAll := func() {
		for _, c := range children {
			if c != nil {
				c.Stop()
			}
		}
	}
	// report says once how a finished child exited; a clean exit is said only if loud.
	report := func(i int, loud bool) {
		c := children[i]
		if reported[i] || (c.Code() == exitOK && !loud) {
			return
		}
		reported[i] = true
		if c.Code() == exitOK {
			fmt.Fprintf(stderr, "fleet: %s logged out (exit 0)\n", members[i].Character)
		} else if why := exitReason(c.Code()); why != "" {
			fmt.Fprintf(stderr, "fleet: %s exited with code %d (%s)\n", members[i].Character, c.Code(), why)
		} else {
			fmt.Fprintf(stderr, "fleet: %s exited with code %d\n", members[i].Character, c.Code())
		}
	}
	isDone := func(i int) bool {
		select {
		case <-children[i].Done():
			return true
		default:
			return false
		}
	}
	// finish stops every child, waits for them (killing after stopGrace) and returns the exit code.
	finish := func(failed bool) int {
		stopAll()
		timer := time.NewTimer(o.stopGrace)
		defer timer.Stop()
		for i, c := range children {
			if c == nil {
				continue
			}
			select {
			case <-c.Done():
			case <-timer.C:
				fmt.Fprintf(stderr, "fleet: %s did not log out in %s; killing it\n", members[i].Character, o.stopGrace)
				c.Kill()
				<-c.Done()
				failed = true
			}
		}
		bad := 0
		for i, c := range children {
			if c != nil && c.Code() != exitOK {
				report(i, false)
				failed = true
				bad++
			}
		}
		if bad > 0 {
			fmt.Fprintf(stderr, "fleet: %d of %d character(s) exited with a failure\n", bad, len(members))
		}
		if failed {
			return exitFleet
		}
		return exitOK
	}

	// startedFailure is a started child that failed, for --stop-on-failure while the start
	// loop is asleep (exit 10, an incomplete logout, never stops the rest).
	startedFailure := func() int {
		for i, c := range children {
			if c == nil {
				continue
			}
			select {
			case <-c.Done():
				if c.Code() != exitOK && c.Code() != exitLogoutIncomplete {
					return i
				}
			default:
			}
		}
		return -1
	}
	// pause sleeps for d, but wakes for Ctrl-C and, with --stop-on-failure, for a child that
	// failed meanwhile (its index, else -1).
	pause := func(d time.Duration) int {
		timer := time.NewTimer(d)
		defer timer.Stop()
		tk := time.NewTicker(max(o.poll, time.Millisecond))
		defer tk.Stop()
		for {
			select {
			case <-timer.C:
				return -1
			case <-ctx.Done():
				return -1
			case <-tk.C:
				if o.stopOnFailure {
					if i := startedFailure(); i >= 0 {
						return i
					}
				}
			}
		}
	}
	for i, m := range members {
		if i > 0 && o.stagger > 0 {
			if f := pause(o.stagger); f >= 0 {
				report(f, true)
				fmt.Fprintf(stderr, "fleet: %s failed; stopping the rest\n", members[f].Character)
				return finish(true)
			}
		}
		// One shared budget: realmd refuses the 15th account from an IP within 300 s, so
		// a login that would be the 15th waits until the oldest has left the window.
		if wait := time.Until(o.budget.reserve(time.Now())); wait > 0 && ctx.Err() == nil {
			fmt.Fprintf(stderr, "fleet: waiting %s to log in %s: realmd allows %d logins per %s from one address and the fleet has used them\n",
				wait.Round(time.Second), m.Character, o.budget.limit, o.budget.window.Round(time.Second))
			if f := pause(wait); f >= 0 {
				report(f, true)
				fmt.Fprintf(stderr, "fleet: %s failed; stopping the rest\n", members[f].Character)
				return finish(true)
			}
		}
		if ctx.Err() != nil {
			return finish(false)
		}
		c, err := deps.start(m, &prefixWriter{w: stderr, mu: &mu, prefix: "[" + m.Character + "] "})
		if err != nil {
			fmt.Fprintf(stderr, "fleet: starting %s: %v\n", m.Character, err)
			if o.stopOnFailure {
				return finish(true)
			}
			children[i], reported[i] = newGoneChild(), true
			continue
		}
		children[i] = c
	}

	// anyExit reports an exited child and whether it failed. A failed child wins over
	// an earlier clean exit, so a clean logout never hides a later failure. A logout that
	// did not complete (exitLogoutIncomplete, from that child's own RPC logout) is not a
	// failure here: the rest keep running, and finish still reports its code.
	anyExit := func() (int, bool) {
		first := -1
		for i, c := range children {
			select {
			case <-c.Done():
				if c.Code() != exitOK && c.Code() != exitLogoutIncomplete {
					return i, true
				}
				if first < 0 {
					first = i
				}
			default:
			}
		}
		return first, false
	}

	deadline := time.NewTimer(o.wait)
	defer deadline.Stop()
	tick := time.NewTicker(o.poll)
	defer tick.Stop()
	for ready := false; !ready; {
		ready = true
		live := 0
		for i, m := range members {
			if isDone(i) {
				states[i] = fmt.Sprintf("exited (%d)", children[i].Code())
				continue
			}
			live++
			s, err := deps.status(ctx, m.RPC)
			if err != nil {
				s = "starting"
			}
			states[i] = s
			if s != string(session.StatusInWorld) {
				ready = false
			}
		}
		if o.stopOnFailure {
			if i, failed := anyExit(); i >= 0 {
				printFleetTable(stdout, members, states)
				report(i, true)
				if failed {
					fmt.Fprintf(stderr, "fleet: %s failed before the fleet was in world\n", members[i].Character)
				} else {
					fmt.Fprintf(stderr, "fleet: %s logged out before the fleet was in world\n", members[i].Character)
				}
				return finish(true)
			}
		}
		for i := range children {
			if isDone(i) {
				report(i, true)
			}
		}
		if live == 0 {
			printFleetTable(stdout, members, states)
			fmt.Fprintln(stderr, "fleet: every character exited before the fleet was in world")
			return finish(true)
		}
		if ready {
			break
		}
		select {
		case <-tick.C:
		case <-deadline.C:
			printFleetTable(stdout, members, states)
			fmt.Fprintf(stderr, "fleet: not every character was in world after %s\n", o.wait)
			return finish(true)
		case <-ctx.Done():
			return finish(false)
		}
	}
	printFleetTable(stdout, members, states)
	inWorld := 0
	for _, s := range states {
		if s == string(session.StatusInWorld) {
			inWorld++
		}
	}
	fmt.Fprintf(stderr, "fleet: %d character(s) in world; Ctrl-C logs them all out\n", inWorld)

	// Supervise: report each child as it exits and keep the rest running, until Ctrl-C
	// or until every child has exited (a logout through its own RPC included).
	exited := make(chan struct{}, len(children))
	for _, c := range children {
		go func(c fleetChild) { <-c.Done(); exited <- struct{}{} }(c)
	}
	for left := len(children); left > 0; left-- {
		select {
		case <-ctx.Done():
			fmt.Fprintln(stderr, "fleet: interrupt, logging every character out")
			return finish(false)
		case <-exited:
			for i := range children {
				if isDone(i) {
					report(i, true)
				}
			}
			if !o.stopOnFailure {
				continue
			}
			if i, failed := anyExit(); failed {
				fmt.Fprintf(stderr, "fleet: %s failed; stopping the rest\n", members[i].Character)
				return finish(true)
			}
		}
	}
	return finish(false)
}

func printFleetTable(w io.Writer, members []fleetMember, states []string) {
	tw := tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)
	fmt.Fprintln(tw, "CHARACTER\tRPC\tSTATE")
	for i, m := range members {
		fmt.Fprintf(tw, "%s\t%s\t%s\n", m.Character, m.RPC, states[i])
	}
	tw.Flush()
}

// prefixWriter labels each line of a child's stderr with its character.
type prefixWriter struct {
	w      io.Writer
	mu     *sync.Mutex
	prefix string
	buf    []byte
}

func (p *prefixWriter) Write(b []byte) (int, error) {
	p.buf = append(p.buf, b...)
	for {
		i := bytes.IndexByte(p.buf, '\n')
		if i < 0 {
			break
		}
		p.mu.Lock()
		fmt.Fprintf(p.w, "%s%s\n", p.prefix, p.buf[:i])
		p.mu.Unlock()
		p.buf = p.buf[i+1:]
	}
	return len(b), nil
}

// procChild is a real `turtle-agent login` process. The password is never passed on its
// command line or environment: the child reads it from the settings file by account key.
type procChild struct {
	cmd  *exec.Cmd
	done chan struct{}
	code int
}

// memberReconnect is the reconnect count of m: its own, else the fleet's.
func memberReconnect(m fleetMember, fleetWide int) int {
	if m.Reconnect != nil {
		return *m.Reconnect
	}
	return fleetWide
}

// childArgs is the `login` command line of one member (after the executable). The password
// is not on it; the account name is, as before.
func childArgs(settings string, m fleetMember, fleetReconnect int, budgetAddr string) []string {
	args := []string{"login", "--config", settings, "--rpc", m.RPC}
	if n := memberReconnect(m, fleetReconnect); n > 0 {
		args = append(args, "--reconnect", strconv.Itoa(n))
	}
	if budgetAddr != "" { // also without --reconnect: a busy answer at the first login is reported
		args = append(args, "--login-budget", budgetAddr)
	}
	if m.Create != "" { // validated by loadFleet, so the child parses the same spec
		args = append(args, "--create", m.Create)
	}
	return append(args, m.Account, m.Character)
}

// startProcess runs exe with args and extra environment entries (the budget token).
func startProcess(exe string, args, env []string, stderr io.Writer) (fleetChild, error) {
	cmd := exec.Command(exe, args...)
	if len(env) > 0 {
		cmd.Env = append(os.Environ(), env...)
	}
	// Own process group (unix): a terminal Ctrl-C reaches only the supervisor, which
	// forwards exactly one interrupt; a second one would end the child's logout retry.
	detachProcessGroup(cmd)
	pr, pw := io.Pipe()
	cmd.Stderr = pw
	if err := cmd.Start(); err != nil {
		pr.Close()
		pw.Close()
		return nil, err
	}
	c := &procChild{cmd: cmd, done: make(chan struct{})}
	copied := make(chan struct{})
	go func() {
		sc := bufio.NewScanner(pr)
		sc.Buffer(make([]byte, 64*1024), 1<<20)
		for sc.Scan() {
			fmt.Fprintln(stderr, sc.Text())
		}
		// Scan stops at a line over the buffer limit; keep draining so the child never
		// blocks writing to a pipe nobody reads.
		_, _ = io.Copy(io.Discard, pr)
		close(copied)
	}()
	go func() {
		err := cmd.Wait()
		pw.Close()
		<-copied
		var ee *exec.ExitError
		switch {
		case err == nil:
		case errors.As(err, &ee):
			c.code = ee.ExitCode()
			if c.code < 0 {
				c.code = 1 // killed by a signal
			}
		default:
			c.code = 1
		}
		close(c.done)
	}()
	return c, nil
}

func (c *procChild) Stop()                 { _ = c.cmd.Process.Signal(os.Interrupt) }
func (c *procChild) Kill()                 { _ = c.cmd.Process.Kill() }
func (c *procChild) Done() <-chan struct{} { return c.done }
func (c *procChild) Code() int             { return c.code }

// fleetToken is the bearer token for the child on addr: none with rpc_auth "off", the
// settings file's rpc_token (shared by every child), or else the one that child generated
// into rpc-<port>.token next to the settings file. A child that has not written its file
// yet gives "", its 401 reads as "starting", and the next poll tries again.
func fleetToken(st config.Settings, settingsPath, addr string) string {
	if !st.AuthOn() {
		return ""
	}
	if st.RPCToken != "" {
		return st.RPCToken
	}
	_, port, err := net.SplitHostPort(addr)
	if err != nil {
		return ""
	}
	n, err := strconv.Atoi(port)
	if err != nil {
		return ""
	}
	tok, _ := rpc.ReadTokenFile(rpc.TokenFile(settingsPath, n))
	return tok
}

// rpcStatus calls the agent's JSON-RPC status method and returns the session state.
// A non-empty token goes out as the bearer token.
func rpcStatus(ctx context.Context, addr, token string) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, "http://"+addr+"/rpc",
		strings.NewReader(`{"jsonrpc":"2.0","id":1,"method":"status"}`))
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", "application/json")
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	var out struct {
		Result struct {
			State string `json:"state"`
		} `json:"result"`
		Error *struct {
			Message string `json:"message"`
		} `json:"error"`
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(&out); err != nil {
		return "", err
	}
	if out.Error != nil {
		return "", errors.New("rpc error")
	}
	return out.Result.State, nil
}
