package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"strings"

	"github.com/pjerra/turtle-agent/internal/dashboard"
	"github.com/pjerra/turtle-agent/internal/netutil"
	"github.com/pjerra/turtle-agent/internal/rpc"
)

const dashboardUsage = "usage: turtle-agent dashboard [--listen 127.0.0.1:8790] [--agents auto|<rpc,...>] [--tests-dir DIR] [--traces-dir DIR] [--plans-dir DIR[,DIR]] [--embed-from ORIGIN[,ORIGIN]] [--config-dir DIR]\n" +
	"  serves a read-only live page of the running agents (README \"Live dashboard\"); it never sends a command"

// expandHome turns a leading ~/ into the home folder.
func expandHome(p string) string {
	if p == "~" || strings.HasPrefix(p, "~/") {
		if home, err := os.UserHomeDir(); err == nil {
			return filepath.Join(home, p[1:])
		}
	}
	return p
}

// dashboardCmd serves the live dashboard until ctx ends.
func dashboardCmd(ctx context.Context, args []string, stdout, stderr io.Writer) int {
	home, _ := os.UserHomeDir()
	fs := flag.NewFlagSet("dashboard", flag.ContinueOnError)
	fs.SetOutput(stderr)
	listen := fs.String("listen", "127.0.0.1:8790", "address to serve the page on (loopback unless you say otherwise)")
	agents := fs.String("agents", "auto", `agents to watch: "auto" (the rpc field of the settings files) or a comma list of RPC ports or addresses`)
	tests := fs.String("tests-dir", filepath.Join(home, "TortoiseBots-audit", "tests"), "the only folder whose files the page serves (run traces and reports)")
	traces := fs.String("traces-dir", filepath.Join(home, "turtle-agent", "traces"), "where the agents write run traces (read for orders and the binary SHA, never served)")
	plans := fs.String("plans-dir", filepath.Join(home, "turtle-agent", "dungeons"), "folder(s) with the dungeon plan files, comma separated")
	embed := fs.String("embed-from", "", "extra origins (http://host:port, comma separated) that may show this page in a frame; loopback pages always may")
	cfgDir := fs.String("config-dir", dashboard.DefaultConfigDir(), "folder with settings*.json and the rpc-<port>.token files")
	if err := fs.Parse(args); err != nil {
		return exitUsage
	}
	if fs.NArg() != 0 {
		fmt.Fprintln(stderr, dashboardUsage)
		return exitUsage
	}
	host, _, err := net.SplitHostPort(*listen)
	if err != nil {
		fmt.Fprintln(stderr, "dashboard: --listen:", err)
		return exitUsage
	}
	if !netutil.IsLoopback(host) {
		fmt.Fprintf(stderr, "dashboard: warning: %s is not loopback; anyone who can reach it sees the runs and the files under --tests-dir\n", *listen)
	}
	if _, err := dashboard.DiscoverAgents(expandHome(*cfgDir), *agents); err != nil {
		fmt.Fprintln(stderr, "dashboard: --agents:", err)
		return exitUsage
	}
	var embedFrom []string
	for _, o := range strings.Split(*embed, ",") {
		if o = strings.TrimSpace(o); o != "" {
			if err := dashboard.CheckEmbedOrigin(o); err != nil {
				fmt.Fprintln(stderr, "dashboard: --embed-from:", err)
				return exitUsage
			}
			embedFrom = append(embedFrom, o)
		}
	}
	var planDirs []string
	for _, d := range strings.Split(*plans, ",") {
		if d = strings.TrimSpace(d); d != "" {
			planDirs = append(planDirs, expandHome(d))
		}
	}
	testsDir := expandHome(*tests)
	if st, err := os.Stat(testsDir); err != nil || !st.IsDir() {
		fmt.Fprintf(stderr, "dashboard: --tests-dir %s is not a folder: no files will be linked\n", testsDir)
		testsDir = ""
	}
	p := dashboard.NewPoller(dashboard.Options{ConfigDir: expandHome(*cfgDir), Agents: *agents, TestsDir: testsDir, TracesDir: expandHome(*traces), PlansDirs: planDirs})
	ln, err := (&net.ListenConfig{}).Listen(ctx, "tcp", *listen)
	if err != nil {
		fmt.Fprintln(stderr, "dashboard:", err)
		return exitUsage
	}
	pctx, stop := context.WithCancel(ctx)
	defer stop()
	go p.Run(pctx)
	fmt.Fprintf(stdout, "turtle-agent dashboard %s on http://%s/ (read-only; agents: %s)\n", version, ln.Addr(), *agents)
	err = rpc.Serve(ctx, ln, dashboard.Handler(p, testsDir, embedFrom...))
	if err != nil && !errors.Is(err, context.Canceled) {
		fmt.Fprintln(stderr, "dashboard:", err)
		return 1
	}
	return exitOK
}
