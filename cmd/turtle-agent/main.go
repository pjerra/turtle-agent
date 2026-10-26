// Command turtle-agent is a headless WoW 1.12 / Tortoise 1.18.1 client for bot tests.
package main

import (
	"context"
	"fmt"
	"io"
	"os"
	"os/signal"
	"syscall"
)

const version = "0.1.0"

// Exit codes; the Python wrapper and shell scripts branch on them.
const (
	exitOK           = 0
	exitUsage        = 2 // bad flags, arguments or settings file
	exitAuth         = 3 // realmd rejected the login
	exitRealm        = 4 // realm missing or offline
	exitWorld        = 5 // world server rejected the session or the character
	exitDisconnected = 6 // the server closed the connection
	exitScrub        = 7 // scrub-check found a secret in a capture
	// exitLogoutRefused: the server still refused the logout (in combat) when the retries
	// ran out; the agent closed the socket and the character may stay in world.
	exitLogoutRefused = 8
	// exitFleet (fleet.go) is 9: a fleet child failed or the fleet never got into world.
	// exitLogoutIncomplete: a logout ended without one: the server never answered, or the
	// socket died before the server accepted it. The character may stay in world for a while.
	exitLogoutIncomplete = 10
)

func main() {
	code := run(signalContext(), os.Args[1:], os.Stdout, os.Stderr)
	os.Exit(code)
}

// signalContext ends on the first Ctrl-C, SIGTERM or SIGHUP and then hands both signals back to
// the default action, so a second Ctrl-C ends the process at once, even while a logout
// is being retried (review I1). The handler is stopped before the context ends.
func signalContext() context.Context {
	ctx, cancel := context.WithCancel(context.Background())
	c := make(chan os.Signal, 1)
	sigs := []os.Signal{os.Interrupt, syscall.SIGTERM}
	// SIGHUP: the terminal closed. Under nohup it is ignored on purpose, and Notify would
	// undo that.
	if !signal.Ignored(syscall.SIGHUP) {
		sigs = append(sigs, syscall.SIGHUP)
	}
	signal.Notify(c, sigs...)
	go func() {
		<-c
		signal.Stop(c)
		cancel()
	}()
	return ctx
}

func run(ctx context.Context, args []string, stdout, stderr io.Writer) int {
	if len(args) == 0 {
		usage(stderr)
		return exitUsage
	}
	switch args[0] {
	case "login":
		return loginCmd(ctx, args[1:], stdout, stderr)
	case "fleet":
		return fleetCmd(ctx, args[1:], stdout, stderr)
	case "scrub-check":
		return scrubCmd(args[1:], stdout, stderr)
	case "capture":
		return captureCmd(args[1:], stdout, stderr)
	case "map":
		return mapCmd(args[1:], stdout, stderr)
	case "tune-diff":
		return tuneDiffCmd(args[1:], stdout, stderr)
	case "capture-stats":
		return statsCmd(args[1:], stdout, stderr)
	case "trace-score":
		return traceScoreCmd(args[1:], stdout, stderr)
	case "pulltest":
		return pulltestCmd(args[1:], stdout, stderr)
	case "rotation":
		return rotationCmd(args[1:], stdout, stderr)
	case "dashboard":
		return dashboardCmd(ctx, args[1:], stdout, stderr)
	case "minimap-extract":
		return minimapCmd(args[1:], stdout, stderr)
	case "plan-export":
		return planExportCmd(args[1:], stdout, stderr)
	case "plan-check":
		return planCheckCmd(args[1:], stdout, stderr)
	case "path-check":
		return pathCheckCmd(args[1:], stdout, stderr)
	case "route-oracle":
		return routeOracleCmd(args[1:], stdout, stderr)
	case "route-edits":
		return routeEditsCmd(ctx, args[1:], stdout, stderr)
	case "version":
		fmt.Fprintln(stdout, "turtle-agent", version)
		return exitOK
	default:
		usage(stderr)
		return exitUsage
	}
}

func usage(w io.Writer) {
	fmt.Fprintln(w, `usage:
  turtle-agent login [--config FILE] [--capture FILE] [--stay DURATION] [--rpc ADDR] [--reconnect N] [--tune FILE] [<account>] <character>
  turtle-agent fleet --config FILE [--settings FILE] [--stagger DURATION] [--wait DURATION] [--reconnect N] [--stop-on-failure]
  turtle-agent scrub-check [--config FILE] <capture>...
  turtle-agent capture-stats <capture>
  turtle-agent map [--capture FILE] [--events FILE] [--report FILE] [--botwatch FILE] [--plan FILE] [--out run.svg] [--html] [--force] [--mmaps DIR] [--map N] [--box x0,y0,x1,y1] [--from RFC3339] [--to RFC3339] [--floors auto|off|z,z]
  turtle-agent capture events [--from RFC3339] [--to RFC3339] [--kinds a,b] [--out FILE [--force]] <capture>
  turtle-agent tune-diff <trace.jsonl>
  turtle-agent trace-score [--json] [--summary] [--set check=threshold]... <trace.jsonl>...
  turtle-agent pulltest [--json] <result.json> | --compare <a.json> <b.json>
  turtle-agent rotation list | show [--level N] <name|file> | check <file>...
  turtle-agent dashboard [--listen 127.0.0.1:8790] [--agents auto|<rpc,...>] [--tests-dir DIR] [--traces-dir DIR] [--plans-dir DIR[,DIR]] [--edit --mmaps-dir DIR]
  turtle-agent minimap-extract --client DIR --out DIR [--maps 70,209|all-instances] [--mmaps DIR] [--list]
  turtle-agent plan-export [--dir dungeons] [--allow-draft] [--drop-companions-loot] [--carrier-chests] [--skip-legs NAME,...] [--with-optional NAME,...] [--mmaps DIR] [--rev SHA] [--stamp YYYYMMDDHHMMSS] [--force] --out FILE|DIR (<plan>... | --all-live)
  turtle-agent plan-check [--dir dungeons] --mmaps DIR [--from-leg N [--from-pull-group N]] [--json] <plan>...
  turtle-agent path-check --mmaps DIR --map N x y z x y z [--json]
  turtle-agent route-oracle --plans DIR --mmaps DIR --out DIR [--plan NAME]...
  turtle-agent route-edits list [--edit-dir DIR] [--json] [<plan>] | export [--edit-dir DIR] [--config-dir DIR] --to DIR <plan> [vN|latest] | promote [--edit-dir DIR] --worktree PATH <plan> vN
  turtle-agent version`)
}
