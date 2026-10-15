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
	"github.com/pjerra/turtle-agent/internal/navmesh"
	"github.com/pjerra/turtle-agent/internal/netutil"
	"github.com/pjerra/turtle-agent/internal/routeedit"
	"github.com/pjerra/turtle-agent/internal/rpc"
)

const dashboardUsage = "usage: turtle-agent dashboard [--listen 127.0.0.1:8790] [--allow-host HOST[,HOST]] [--agents auto|<rpc,...>] [--tests-dir DIR] [--traces-dir DIR] [--plans-dir DIR[,DIR]] [--minimap-dir DIR] [--embed-from ORIGIN[,ORIGIN]] [--config-dir DIR]\n" +
	"                          [--edit --mmaps-dir DIR [--edit-dir DIR] [--edit-source DIR] [--edit-tokens FILE]]\n" +
	"  serves a read-only live page of the running agents (README \"Live dashboard\"); it never sends a command.\n" +
	"  --edit turns on the route editor (README \"Route editor\"): the only thing the page can then write is a checked version of a plan's route"

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
	allowHosts := fs.String("allow-host", "", "host names or IP addresses, comma separated, the page accepts in its Host header besides loopback (the name you open it by, such as a Tailscale MagicDNS name); the --listen IP is always accepted")
	agents := fs.String("agents", "auto", `agents to watch: "auto" (the rpc field of the settings files) or a comma list of RPC ports or addresses`)
	tests := fs.String("tests-dir", filepath.Join(home, "TortoiseBots-audit", "tests"), "the only folder whose files the page serves (run traces and reports)")
	traces := fs.String("traces-dir", filepath.Join(home, "turtle-agent", "traces"), "where the agents write run traces (read for orders and the binary SHA, never served)")
	plans := fs.String("plans-dir", filepath.Join(home, "turtle-agent", "dungeons"), "folder(s) with the dungeon plan files, comma separated")
	minimaps := fs.String("minimap-dir", filepath.Join(home, "turtle-agent-minimaps"), "folder with the map images `turtle-agent minimap-extract` wrote (<map>.png, <map>.json); the run map draws over the image of its map id when there is one")
	embed := fs.String("embed-from", "", "extra origins (http://host:port, comma separated) that may show this page in a frame; loopback pages always may")
	edit := fs.Bool("edit", false, "turn on the route editor: POST /api/edit/<plan> with the editing token writes a navmesh-checked version of a plan's route into --edit-dir (off: the page is GET/HEAD only)")
	editDir := fs.String("edit-dir", filepath.Join(home, "turtle-agent-route-edits"), "with --edit: where the versions are written (<plan>/vNNNN.json, .meta.json, .diff); must not be, contain or lie inside any slot's dungeons_dir")
	editSource := fs.String("edit-source", "", "with --edit: the folder whose plan files an edit starts from (default: the first --plans-dir); must not lie inside any slot's dungeons_dir")
	editTokens := fs.String("edit-tokens", filepath.Join(home, ".config", "turtle-agent", "route-edit-tokens"), "with --edit: file of `label token` lines, mode 0600; the label is recorded as who made a version")
	editMmaps := fs.String("mmaps-dir", "", "with --edit: the navmesh folder of the server the plans are run on (a check on another server's mesh proves nothing); required")
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
	allow, err := dashboard.AllowedHosts(host, *allowHosts)
	if err != nil {
		fmt.Fprintln(stderr, "dashboard: --allow-host:", err)
		return exitUsage
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
	p := dashboard.NewPoller(dashboard.Options{ConfigDir: expandHome(*cfgDir), Agents: *agents, TestsDir: testsDir, TracesDir: expandHome(*traces), PlansDirs: planDirs, MinimapDir: minimapDir(expandHome(*minimaps), stderr)})
	var editor *dashboard.Editor
	if *edit {
		src := expandHome(*editSource)
		if src == "" && len(planDirs) > 0 {
			src = planDirs[0]
		}
		ed, planner, err := newEditor(p, src, expandHome(*editDir), expandHome(*editTokens), expandHome(*editMmaps))
		if err != nil {
			fmt.Fprintln(stderr, "dashboard: --edit:", err)
			return exitUsage
		}
		defer planner.Close()
		editor = ed
		fmt.Fprintf(stderr, "dashboard: route editing is ON: edits start from %s, versions go to %s, checked on the mesh in %s\n", src, expandHome(*editDir), expandHome(*editMmaps))
	}
	ln, err := (&net.ListenConfig{}).Listen(ctx, "tcp", *listen)
	if err != nil {
		fmt.Fprintln(stderr, "dashboard:", err)
		return exitUsage
	}
	pctx, stop := context.WithCancel(ctx)
	defer stop()
	go p.Run(pctx)
	mode := "read-only"
	if editor != nil {
		mode = "route editing ON"
	}
	fmt.Fprintf(stdout, "turtle-agent dashboard %s on http://%s/ (%s; agents: %s)\n", version, ln.Addr(), mode, *agents)
	err = rpc.Serve(ctx, ln, dashboard.HandlerWithEditor(p, testsDir, allow, embedFrom, editor))
	if err != nil && !errors.Is(err, context.Canceled) {
		fmt.Fprintln(stderr, "dashboard:", err)
		return 1
	}
	return exitOK
}

// newEditor sets up the route editor: the token file, the versions folder, the navmesh the plans are checked on, and the check that the folders
// stay clear of every slot's plan folder. The planner is the caller's to close.
func newEditor(p *dashboard.Poller, source, editDir, tokens, mmaps string) (*dashboard.Editor, *navmesh.Planner, error) {
	if mmaps == "" {
		return nil, nil, errors.New("--mmaps-dir is needed: the navmesh folder of the server the plans are run on")
	}
	if st, err := os.Stat(mmaps); err != nil || !st.IsDir() {
		return nil, nil, fmt.Errorf("--mmaps-dir %s is not a folder", mmaps)
	}
	if source == "" {
		return nil, nil, errors.New("--edit-source (or a --plans-dir) is needed")
	}
	tk, err := routeedit.LoadTokens(tokens)
	if err != nil {
		return nil, nil, err
	}
	store, err := routeedit.NewStore(editDir)
	if err != nil {
		return nil, nil, err
	}
	planner := navmesh.NewPlanner(mmaps)
	ed, err := dashboard.NewEditor(p, dashboard.EditConfig{Source: source, Store: store, Tokens: tk, Nav: planner, Mmaps: mmaps})
	if err != nil {
		planner.Close()
		return nil, nil, err
	}
	return ed, planner, nil
}

// minimapDir is dir when it is a folder, else "" (the page then draws no background).
func minimapDir(dir string, stderr io.Writer) string {
	if st, err := os.Stat(dir); err != nil || !st.IsDir() {
		fmt.Fprintf(stderr, "dashboard: --minimap-dir %s is not a folder: the map has no background image (run turtle-agent minimap-extract)\n", dir)
		return ""
	}
	return dir
}
