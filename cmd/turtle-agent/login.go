package main

import (
	"context"
	"encoding/hex"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"strings"
	"time"

	"github.com/pjerra/turtle-agent/internal/auth"
	"github.com/pjerra/turtle-agent/internal/capture"
	"github.com/pjerra/turtle-agent/internal/config"
	"github.com/pjerra/turtle-agent/internal/events"
	"github.com/pjerra/turtle-agent/internal/navmesh"
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

// dialWorld keys the world connection with Ticket.KeyBytes(), the unpadded form the
// server hashes (39 bytes when the high byte of K is zero), never Ticket.Key[:].
func dialWorld(ctx context.Context, addr string, tk *auth.Ticket, tap worldconn.Tap) (*worldconn.Conn, error) {
	return dial(ctx, addr, tk.Account, tk.KeyBytes(), tap)
}

func loginCmd(ctx context.Context, args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("login", flag.ContinueOnError)
	fs.SetOutput(stderr)
	cfgPath := fs.String("config", config.DefaultPath(), "settings file (realmd address, accounts)")
	capPath := fs.String("capture", "", "record decrypted world packets to this new file")
	stay := fs.Duration("stay", 0, "log out after this long in world (0: until interrupted or the logout RPC)")
	rpcAddr := fs.String("rpc", "", "JSON-RPC listen address (default: the settings file's rpc)")
	if err := fs.Parse(args); err != nil {
		return exitUsage
	}
	if fs.NArg() != 1 && fs.NArg() != 2 {
		fmt.Fprintln(stderr, "usage: turtle-agent login [flags] [<account>] <character>")
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

	if *capPath != "" {
		if err := checkCapture(*capPath, account, password); err != nil {
			fmt.Fprintln(stderr, err)
			return exitUsage
		}
	}

	actx, cancel := context.WithTimeout(ctx, 30*time.Second)
	ticket, err := authLogin(actx, st.Realmd, account, password)
	cancel()
	if err != nil {
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
	conn, err := dialWorld(wctx, addr, ticket, tap)
	cancel()
	if err != nil {
		fmt.Fprintln(stderr, withPrefix("world:", err))
		return exitWorld
	}
	fmt.Fprintf(stderr, "world: authenticated on %s (%s)\n", realm.Name, addr)

	log := events.New(100000)
	// The server can echo the account name back (a .pinfo reply): events and the stderr
	// log never carry it, nor the password (ruling P10, review I3).
	opts := session.Options{Character: character, Log: log, Logger: stderr,
		Redact: []string{ticket.Account, account}, RedactAnywhere: []string{password}}
	if st.MmapsDir != "" {
		planner := navmesh.NewPlanner(st.MmapsDir)
		defer planner.Close()
		opts.Planner = planner
		fmt.Fprintf(stderr, "navmesh: tiles from %s\n", st.MmapsDir)
	}
	s := session.New(conn, opts)
	runErr := make(chan error, 1)
	go func() { runErr <- s.Run(context.Background()) }()
	select {
	case <-s.Ready():
		info := s.Status()
		fmt.Fprintf(stderr, "in world: %s guid=%s map=%d x=%.1f y=%.1f z=%.1f\n", info.Character, info.GUID, info.Map, info.X, info.Y, info.Z)
	case err := <-runErr:
		// Ready never closes when login fails; Run's result says why.
		return sessionExit(s, err, stderr)
	case <-ctx.Done():
		conn.Close()
		<-runErr
		return exitWorld
	}

	srv := rpc.NewServer()
	registerMethods(srv, s)
	rctx, stopRPC := context.WithCancel(context.Background())
	served := make(chan error, 1)
	go func() { served <- rpc.Serve(rctx, ln, srv.Handler()) }()
	defer func() {
		stopRPC()
		if served != nil {
			<-served
		}
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
			if err == nil || served == nil {
				return code // a logout through the RPC: done at once
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
		err := s.Logout(ctx)
		cancel()
		var le *session.LogoutError
		if !errors.As(err, &le) {
			if err != nil {
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
	// Logout returned nil after SMSG_LOGOUT_COMPLETE, and Run then ends at once; after an
	// error (a lost connection, no SMSG_LOGOUT_COMPLETE in time) it may not.
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
	if err == nil {
		fmt.Fprintln(stderr, "logout: complete")
		return exitOK
	}
	fmt.Fprintln(stderr, "session:", s.Redact(err.Error()))
	if errors.Is(err, session.ErrDisconnected) {
		return exitDisconnected
	}
	return exitWorld
}
