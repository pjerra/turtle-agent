package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"flag"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"time"

	"github.com/pjerra/turtle-agent/internal/auth"
	"github.com/pjerra/turtle-agent/internal/capture"
	"github.com/pjerra/turtle-agent/internal/config"
	"github.com/pjerra/turtle-agent/internal/events"
	"github.com/pjerra/turtle-agent/internal/navmesh"
	"github.com/pjerra/turtle-agent/internal/netutil"
	"github.com/pjerra/turtle-agent/internal/rpc"
	"github.com/pjerra/turtle-agent/internal/session"
	"github.com/pjerra/turtle-agent/internal/worldconn"
)

// Seams for tests: dial shows which key the login path hands to the world server,
// authLogin lets an end-to-end test skip realmd's SRP and pick the ticket.
var (
	dial      = worldconn.Dial
	authLogin = auth.Login
)

// Timings, variables so the end-to-end tests can shorten them.
var (
	// logoutRetryFor bounds how long a refused logout (in combat) is retried, once every
	// logoutRetryEvery, before the agent gives up with exitLogoutRefused.
	logoutRetryFor   = 35 * time.Second
	logoutRetryEvery = 2 * time.Second
	// logoutWait bounds one logout attempt (the server's 20 s logout timer plus margin).
	logoutWait = 35 * time.Second
	// logoutTimeout is the session's wait for SMSG_LOGOUT_COMPLETE or the server's close
	// after an accepted request; past it the session ends (session.ErrLogoutTimeout).
	logoutTimeout = 30 * time.Second
	// disconnectGrace keeps the RPC up after the server drops the session, so status
	// shows the disconnect and its reason (spec: error handling, review I2).
	disconnectGrace = 30 * time.Second
)

// withPrefix labels err with where it came from, unless it already says so (the auth
// and worldconn errors carry their own "realmd:" / "world:" prefix).
func withPrefix(prefix string, err error) string {
	if strings.HasPrefix(err.Error(), prefix) {
		return err.Error()
	}
	return prefix + " " + err.Error()
}

// checkCapture runs before the throttled realmd login so a bad --capture never costs an
// attempt: the file must not exist and the secrets known up front must be long enough.
// The key is 40 bytes and checked later by capture.Create.
func checkCapture(path, account, password string) error {
	if _, err := os.Lstat(path); err == nil {
		return fmt.Errorf("capture: %s already exists (captures are never overwritten)", path)
	} else if !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("capture: %w", err)
	}
	if len(account) < 3 || len(password) < 3 {
		return fmt.Errorf("capture: %w", capture.ErrSecretTooShort)
	}
	return nil
}

// rpcAuthSetup picks the RPC bearer token: none with rpc_auth "off", the settings file's
// rpc_token, or a fresh random one written (0600) to rpc-<port>.token next to the settings
// file, where fleet and local clients read it. It reports where the token is, never the
// token. The returned func removes the generated file.
func rpcAuthSetup(st config.Settings, cfgPath string, ln net.Listener, stderr io.Writer) (token string, remove func(), err error) {
	remove = func() {}
	switch {
	case !st.AuthOn():
		fmt.Fprintln(stderr, `rpc: authentication is OFF ("rpc_auth": "off"); any local process can drive the character`)
		return "", remove, nil
	case st.RPCToken != "":
		fmt.Fprintln(stderr, `rpc: calls need the settings file's "rpc_token" (Authorization: Bearer)`)
		return st.RPCToken, remove, nil
	}
	tcp, ok := ln.Addr().(*net.TCPAddr)
	if !ok {
		return "", remove, errors.New("listener is not TCP")
	}
	if token, err = rpc.NewToken(); err != nil {
		return "", remove, err
	}
	path := rpc.TokenFile(cfgPath, tcp.Port)
	if remove, err = rpc.WriteTokenFile(path, token); err != nil {
		return "", func() {}, err
	}
	fmt.Fprintf(stderr, "rpc: calls need the token in %s (Authorization: Bearer)\n", path)
	return token, remove, nil
}

// sessionOptions are the session options of the first login and of every reconnect, so
// both get the same redaction (the account names the server may echo, the password) and
// the same navmesh planner. A nil planner leaves path_to and walk_path answering that
// mmaps_dir is not set.
func sessionOptions(character string, log *events.Log, stderr io.Writer, accounts []string, password string,
	planner *navmesh.Planner) session.Options {
	o := session.Options{Character: character, Log: log, Logger: stderr, Redact: accounts, RedactAnywhere: []string{password},
		LogoutTimeout: logoutTimeout}
	if planner != nil { // a nil *Planner in the interface would not read as nil
		o.Planner = planner
	}
	return o
}

// dialWorld keys the world connection with Ticket.KeyBytes(), the unpadded form the
// server hashes (39 bytes when the high byte of K is zero), never Ticket.Key[:].
func dialWorld(ctx context.Context, addr string, tk *auth.Ticket, tap worldconn.Tap, opts ...worldconn.Option) (*worldconn.Conn, error) {
	return dial(ctx, addr, tk.Account, tk.KeyBytes(), tap, opts...)
}

// fingerprintPath is the file holding the addon fingerprint for account: one per account
// (named by a hash, so the name stays out of the folder listing), because the core's
// anticheat bans by a hash of fingerprint and IP and a shared one would spread a ban to
// every account. With shared_fingerprint every account uses one file.
func fingerprintPath(st config.Settings, account string) string {
	if st.SharedFingerprint {
		return filepath.Join(st.FingerprintDir, "addon-fingerprint")
	}
	sum := sha256.Sum256([]byte(strings.ToUpper(account)))
	return filepath.Join(st.FingerprintDir, "addon-fingerprint-"+hex.EncodeToString(sum[:6]))
}

// addonOption makes the login present the same addon fingerprint every time: the one kept
// in the account's file, or, on the first login, the one the server announces, which is then
// saved there. A failed save is reported and costs only a new fingerprint next login.
func addonOption(st config.Settings, account string, stderr io.Writer) worldconn.Option {
	a := &worldconn.Addons{Names: st.Addons}
	if st.FingerprintDir == "" {
		return worldconn.WithAddons(a)
	}
	f := &worldconn.FingerprintFile{Path: fingerprintPath(st, account)}
	a.Fingerprint = f.Load()
	a.OnFingerprint = func(fp uint32) {
		if err := f.Save(fp); err != nil {
			fmt.Fprintln(stderr, "addon fingerprint not saved:", err)
		}
	}
	return worldconn.WithAddons(a)
}

func loginCmd(ctx context.Context, args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("login", flag.ContinueOnError)
	fs.SetOutput(stderr)
	cfgPath := fs.String("config", config.DefaultPath(), "settings file (realmd address, accounts)")
	capPath := fs.String("capture", "", "record decrypted world packets to this new file")
	stay := fs.Duration("stay", 0, "log out after this long in world (0: until interrupted or the logout RPC)")
	rpcAddr := fs.String("rpc", "", "JSON-RPC listen address (default: the settings file's rpc)")
	reconnects := fs.Int("reconnect", 0, "after an unplanned disconnect, log in again up to N times (0: off)")
	budgetAddr := fs.String("login-budget", "", "loopback address of the fleet's shared login budget (set by `fleet`; its token is in the environment)")
	if err := fs.Parse(args); err != nil {
		return exitUsage
	}
	if fs.NArg() != 1 && fs.NArg() != 2 {
		fmt.Fprintln(stderr, "usage: turtle-agent login [flags] [<account>] <character>")
		return exitUsage
	}
	if *reconnects < 0 {
		fmt.Fprintln(stderr, "login: --reconnect must be 0 (off) or more")
		return exitUsage
	}
	var budget *budgetClient // the fleet's login budget, if a fleet started this process
	if *budgetAddr != "" {
		if _, port, err := netutil.CheckLoopbackAddr(*budgetAddr); err != nil || port == 0 {
			fmt.Fprintln(stderr, "login: --login-budget must be a loopback address with a port")
			return exitUsage
		}
		tok := os.Getenv(budgetTokenEnv)
		if tok == "" {
			fmt.Fprintf(stderr, "login: --login-budget needs the token in %s\n", budgetTokenEnv)
			return exitUsage
		}
		budget = newBudgetClient(*budgetAddr, tok)
	}
	if *reconnects > 0 && *capPath != "" {
		// A reconnect brings a new session key that the capture's redaction list lacks.
		fmt.Fprintln(stderr, "login: --reconnect cannot be combined with --capture")
		return exitUsage
	}
	st, err := config.Load(*cfgPath)
	if err != nil {
		fmt.Fprintln(stderr, "config:", err)
		return exitUsage
	}
	// With one argument the account is the settings file's only one, which keeps the name
	// out of ps and shell history (ruling P10).
	var account, character string
	if fs.NArg() == 2 {
		account, character = fs.Arg(0), fs.Arg(1)
	} else {
		character = fs.Arg(0)
		if account, err = st.OnlyAccount(); err != nil {
			fmt.Fprintln(stderr, "config:", err)
			return exitUsage
		}
	}
	password, err := st.Password(account)
	if err != nil {
		fmt.Fprintf(stderr, "config: account %q: %v\n", account, err)
		return exitUsage
	}

	// The RPC port is bound before realmd, so a bad, non-loopback or busy address never
	// costs a login attempt; the server starts on it once the character is in world.
	addrRPC := st.RPC
	if *rpcAddr != "" {
		addrRPC = *rpcAddr
	}
	ln, err := rpc.Listen(ctx, addrRPC)
	if err != nil {
		fmt.Fprintln(stderr, "rpc:", err)
		return exitUsage
	}
	defer ln.Close() // every early return; Serve closes it too once it runs
	rpcToken, removeToken, err := rpcAuthSetup(st, *cfgPath, ln, stderr)
	if err != nil {
		fmt.Fprintln(stderr, "rpc:", err)
		return exitUsage
	}
	defer removeToken()

	if *capPath != "" {
		if err := checkCapture(*capPath, account, password); err != nil {
			fmt.Fprintln(stderr, err)
			return exitUsage
		}
	}

	window := &loginWindow{}
	window.note(time.Now())
	actx, cancel := context.WithTimeout(ctx, 30*time.Second)
	ticket, err := authLogin(actx, st.Realmd, account, password)
	cancel()
	if err != nil {
		// A busy answer closes realmd's window for the whole fleet, not just this child.
		var ae *auth.AuthError
		if budget != nil && errors.As(err, &ae) && ae.Code == auth.ResultDBBusy {
			budget.busy(context.Background())
		}
		fmt.Fprintln(stderr, withPrefix("realmd:", err))
		return exitAuth
	}
	// The account name is sensitive (ruling P10): the progress line does not repeat it.
	fmt.Fprintf(stderr, "realmd: authenticated, %d realm(s)\n", len(ticket.Realms))
	realm, err := ticket.Pick(st.Realm)
	if err != nil {
		fmt.Fprintln(stderr, withPrefix("realmd:", err))
		return exitRealm
	}
	addr := realm.Address
	if st.WorldAddr != "" {
		addr = st.WorldAddr
	}

	var tap worldconn.Tap
	if *capPath != "" {
		key := ticket.KeyBytes()
		hexKey := hex.EncodeToString(key)
		// Create expands each secret into its variants (raw, reversed, hex, case).
		w, err := capture.Create(*capPath, []byte(ticket.Account), []byte(password), []byte(strings.ToUpper(password)),
			ticket.Key[:], key, []byte(hexKey), []byte(strings.ToUpper(hexKey)))
		if err != nil {
			fmt.Fprintln(stderr, err)
			return exitUsage
		}
		defer func() {
			n, redacted := w.Counts()
			if err := w.Close(); err != nil {
				fmt.Fprintln(stderr, "capture:", err)
			}
			fmt.Fprintf(stderr, "capture: %d records, %d redacted, written to %s\n", n, redacted, *capPath)
		}()
		tap = w
	}

	wctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	conn, err := dialWorld(wctx, addr, ticket, tap, addonOption(st, ticket.Account, stderr))
	cancel()
	if err != nil {
		fmt.Fprintln(stderr, withPrefix("world:", err))
		return exitWorld
	}
	fmt.Fprintf(stderr, "world: authenticated on %s (%s)\n", realm.Name, addr)

	log := events.New(100000)
	// The server can echo the account name back (a .pinfo reply): events and the stderr
	// log never carry it, nor the password (ruling P10, review I3).
	var planner *navmesh.Planner
	if st.MmapsDir != "" {
		planner = navmesh.NewPlanner(st.MmapsDir)
		defer planner.Close()
		fmt.Fprintf(stderr, "navmesh: tiles from %s\n", st.MmapsDir)
	}
	s := session.New(conn, sessionOptions(character, log, stderr, []string{ticket.Account, account}, password, planner))
	runErr := make(chan error, 1)
	go func() { runErr <- s.Run(context.Background()) }()
	select {
	case <-s.Ready():
	case err := <-runErr:
		select {
		case <-s.Ready():
			// In world, then dropped before this select ran (it picks at random between
			// ready cases). Run sends once and runErr is buffered, so the result goes back
			// for the loop below, which gives the disconnect its grace window.
			runErr <- err
		default:
			// Ready never closes when login fails; Run's result says why.
			return sessionExit(s, err, stderr)
		}
	case <-ctx.Done():
		conn.Close()
		<-runErr
		return exitWorld
	}
	info := s.Status()
	fmt.Fprintf(stderr, "in world: %s guid=%s map=%d x=%.1f y=%.1f z=%.1f\n", info.Character, info.GUID, info.Map, info.X, info.Y, info.Z)

	// The RPC acts on the current session: a reconnect swaps the new one in behind the same
	// address, and the shared event log keeps the cursors valid.
	var cur atomic.Pointer[session.Session]
	cur.Store(s)
	var rc *reconnector
	if *reconnects > 0 {
		rc = &reconnector{total: *reconnects, window: window, st: st, account: account, password: password,
			character: character, log: log, stderr: stderr, redact: s.Redact, planner: planner}
		rc.budget = budget
	}
	srv := rpc.NewServer()
	if rpcToken == "" {
		srv.DisableAuth()
	} else {
		srv.SetToken(rpcToken)
	}
	closeMethods := registerMethodsFor(srv, cur.Load, methodOptions{AddonDir: st.TBMAddonDir, DungeonsDir: st.DungeonsDir})
	rctx, stopRPC := context.WithCancel(context.Background())
	served := make(chan error, 1)
	go func() { served <- rpc.Serve(rctx, ln, srv.Handler()) }()
	defer func() {
		stopRPC()
		if served != nil {
			<-served
		}
		closeMethods() // after the last call: the addon host and its sidecar
	}()
	fmt.Fprintf(stderr, "rpc: listening on http://%s/rpc\n", ln.Addr())

	var stayC <-chan time.Time
	if *stay > 0 {
		stayC = time.After(*stay)
	}
	for {
		select {
		case <-stayC:
			return logoutAndWait(s, conn, runErr, stderr)
		case <-ctx.Done():
			return logoutAndWait(s, conn, runErr, stderr)
		case err := <-runErr:
			code := sessionExit(s, err, stderr)
			if err == nil || errors.Is(err, session.ErrClosedAtLogout) || errors.Is(err, session.ErrLostAtLogout) ||
				errors.Is(err, session.ErrLogoutTimeout) {
				// A logout through the RPC, however it ended: done at once. The deferred RPC
				// shutdown lets the logout call in flight write its reply first.
				return code
			}
			// --reconnect: an unplanned disconnect of a session that was in world logs in again.
			if rc != nil && errors.Is(err, session.ErrDisconnected) {
				if ws, ok := rc.next(ctx); ok {
					s, conn, runErr = ws.s, ws.conn, ws.runErr
					cur.Store(s)
					continue
				}
			}
			if served == nil {
				return code
			}
			// Unplanned: keep the RPC up, read-only in effect (actions answer -32002), so
			// status and events show the disconnect and why. A Ctrl-C ends the wait.
			fmt.Fprintf(stderr, "rpc: serving status for %s after the disconnect (Ctrl-C to quit now)\n", disconnectGrace)
			grace := time.NewTimer(disconnectGrace)
			defer grace.Stop()
			select {
			case <-grace.C:
			case <-ctx.Done():
			case <-served:
				served = nil
			}
			return code
		case err := <-served:
			// The character stays in world (--stay and Ctrl-C still log out); only the
			// RPC is gone. The error names the listener address, never a secret.
			fmt.Fprintln(stderr, "rpc: server stopped:", err)
			served = nil
		}
	}
}

// logoutAndWait logs out for --stay or the first Ctrl-C. A refused logout (in combat) is
// said once and retried every logoutRetryEvery for logoutRetryFor; still refused, the agent
// closes the socket and exits with exitLogoutRefused (review I1). A second Ctrl-C ends the
// process at once: main hands the signal back after the first.
func logoutAndWait(s *session.Session, conn *worldconn.Conn, runErr <-chan error, stderr io.Writer) int {
	until := time.Now().Add(logoutRetryFor)
	var refused *session.LogoutError
	for {
		ctx, cancel := context.WithTimeout(context.Background(), logoutWait)
		_, err := s.Logout(ctx)
		cancel()
		var le *session.LogoutError
		if !errors.As(err, &le) {
			if err != nil && !errors.Is(err, session.ErrLogoutTimeout) && !errors.Is(err, session.ErrLostAtLogout) { // sessionExit says those
				fmt.Fprintln(stderr, "logout:", s.Redact(err.Error()))
			}
			break
		}
		if refused == nil || refused.Reason != le.Reason {
			fmt.Fprintf(stderr, "logout: %v; retrying every %s for up to %s (a second Ctrl-C quits at once)\n", le, logoutRetryEvery, logoutRetryFor)
		}
		refused = le
		if !time.Now().Add(logoutRetryEvery).Before(until) {
			fmt.Fprintf(stderr, "logout: still refused after %s; closing the connection, the character may stay in world\n", logoutRetryFor)
			return closeAndExit(conn, runErr, exitLogoutRefused)
		}
		select {
		case err := <-runErr:
			return sessionExit(s, err, stderr)
		case <-time.After(logoutRetryEvery):
		}
	}
	// Run ends at once after any logout end (complete, closed by the server, timed out);
	// after another error (the caller's context, a failed write) it may not.
	select {
	case err := <-runErr:
		return sessionExit(s, err, stderr)
	case <-time.After(5 * time.Second):
		fmt.Fprintln(stderr, "logout: timed out")
		return closeAndExit(conn, runErr, exitDisconnected)
	}
}

// closeAndExit closes a session that is still in world, so no packet is handled or sent
// after the agent has given up, and returns code.
func closeAndExit(conn *worldconn.Conn, runErr <-chan error, code int) int {
	conn.Close()
	<-runErr
	return code
}

// sessionExit maps Run's result to an exit code. The error is scrubbed like the session's
// own lastErr before it reaches the stderr log.
func sessionExit(s *session.Session, err error, stderr io.Writer) int {
	switch {
	case err == nil:
		fmt.Fprintln(stderr, "logout: complete")
		return exitOK
	case errors.Is(err, session.ErrClosedAtLogout):
		// The server ended the logout by closing the socket (issues #9, #18): the logout
		// was asked for and the session is over, so this is a clean exit.
		fmt.Fprintln(stderr, "logout: the server closed the connection (no SMSG_LOGOUT_COMPLETE)")
		return exitOK
	case errors.Is(err, session.ErrLogoutTimeout):
		fmt.Fprintln(stderr, "logout: timed out without SMSG_LOGOUT_COMPLETE; connection closed, the character may stay in world")
		return exitLogoutIncomplete
	case errors.Is(err, session.ErrLostAtLogout):
		fmt.Fprintln(stderr, "logout:", s.Redact(err.Error())+"; the character may stay in world for a while")
		return exitLogoutIncomplete
	}
	fmt.Fprintln(stderr, "session:", s.Redact(err.Error()))
	if errors.Is(err, session.ErrDisconnected) {
		return exitDisconnected
	}
	return exitWorld
}
