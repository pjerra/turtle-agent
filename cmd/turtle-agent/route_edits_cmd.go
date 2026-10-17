package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/pjerra/turtle-agent/internal/dashboard"
	"github.com/pjerra/turtle-agent/internal/routeedit"
)

const routeEditsUsage = `usage:
  turtle-agent route-edits list [--edit-dir DIR] [--json] [<plan>]
  turtle-agent route-edits export [--edit-dir DIR] [--config-dir DIR] --to DIR <plan> [vN|latest]
  turtle-agent route-edits promote [--edit-dir DIR] [--message-file FILE] --worktree PATH <plan> vN
  the versions the dashboard's route editor wrote (README "Route editor"); export copies the newest checked version (or vN) into a fresh folder
  as <plan>.json for a stopped slot's dungeons_dir and prints the run tag; promote writes it over dungeons/<plan>.json of a worktree for a PR`

// routeEditsCmd is `turtle-agent route-edits list|export|promote`.
func routeEditsCmd(ctx context.Context, args []string, stdout, stderr io.Writer) int {
	if len(args) == 0 {
		fmt.Fprintln(stderr, routeEditsUsage)
		return exitUsage
	}
	home, _ := os.UserHomeDir()
	fs := flag.NewFlagSet("route-edits "+args[0], flag.ContinueOnError)
	fs.SetOutput(stderr)
	editDir := fs.String("edit-dir", filepath.Join(home, "turtle-agent-route-edits"), "where the route editor wrote its versions")
	var asJSON *bool
	var cfgDir, to, worktree, msgFile *string
	switch args[0] {
	case "list":
		asJSON = fs.Bool("json", false, "print the records as JSON")
	case "export":
		cfgDir = fs.String("config-dir", dashboard.DefaultConfigDir(), "folder with the settings*.json files of the slots (which folders are a slot's plan folder, and whether that agent runs)")
		to = fs.String("to", "", "the fresh folder to write <plan>.json into (it must not exist or be empty)")
	case "promote":
		worktree = fs.String("worktree", "", "the turtle-agent worktree whose dungeons/<plan>.json is replaced")
		msgFile = fs.String("message-file", "", "also write the commit message to this file")
	default:
		fmt.Fprintln(stderr, routeEditsUsage)
		return exitUsage
	}
	pos, err := parseInterspersed(fs, args[1:])
	if err != nil {
		return exitUsage
	}
	store, err := routeedit.OpenStore(expandHome(*editDir))
	if err != nil {
		fmt.Fprintln(stderr, "route-edits:", err)
		return exitUsage
	}
	switch args[0] {
	case "list":
		if len(pos) > 1 {
			fmt.Fprintln(stderr, routeEditsUsage)
			return exitUsage
		}
		return routeEditsList(store, pos, *asJSON, stdout, stderr)
	case "export":
		if len(pos) < 1 || len(pos) > 2 || *to == "" {
			fmt.Fprintln(stderr, routeEditsUsage)
			return exitUsage
		}
		return routeEditsExport(ctx, store, pos, expandHome(*to), expandHome(*cfgDir), stdout, stderr)
	default:
		if len(pos) != 2 || *worktree == "" {
			fmt.Fprintln(stderr, routeEditsUsage)
			return exitUsage
		}
		return routeEditsPromote(store, pos, expandHome(*worktree), expandHome(*msgFile), stdout, stderr)
	}
}

// parseInterspersed parses flags that may come before, between or after the arguments, and returns the arguments.
func parseInterspersed(fs *flag.FlagSet, args []string) ([]string, error) {
	var pos []string
	for {
		if err := fs.Parse(args); err != nil {
			return nil, err
		}
		args = fs.Args()
		if len(args) == 0 {
			return pos, nil
		}
		pos = append(pos, args[0])
		args = args[1:]
	}
}

// versionArg reads "v0007", "v7", "7" or "latest" (0).
func versionArg(s string) (int, error) {
	if s == "latest" {
		return 0, nil
	}
	n, err := strconv.Atoi(strings.TrimPrefix(s, "v"))
	if err != nil || n <= 0 || n > routeedit.MaxVersions {
		return 0, fmt.Errorf("%q is not a version (v0007, 7 or latest)", s)
	}
	return n, nil
}

func routeEditsList(store *routeedit.Store, pos []string, asJSON bool, stdout, stderr io.Writer) int {
	plans := pos
	if len(plans) == 0 {
		var err error
		if plans, err = store.Plans(); err != nil {
			fmt.Fprintln(stderr, "route-edits:", err)
			return 1
		}
	}
	var all []routeedit.Meta
	for _, p := range plans {
		ms, err := store.List(p)
		if err != nil {
			fmt.Fprintln(stderr, "route-edits:", err)
			return exitUsage
		}
		all = append(all, ms...)
	}
	if asJSON {
		if all == nil {
			all = []routeedit.Meta{}
		}
		_ = json.NewEncoder(stdout).Encode(all)
		return exitOK
	}
	for _, m := range all {
		check := "not checked"
		if m.Check.Checked {
			check = fmt.Sprintf("%d points, %d new issues, %d already there", m.Check.Points, m.Check.NewIssues, m.Check.BaseIssues)
		}
		fmt.Fprintf(stdout, "%s %s %-8s %s by %s: %d ops, %s, base %s", m.Plan, routeedit.Name(m.Version), m.Status, m.At, m.Who, len(m.Ops), check, short8(m.Base.SHA256))
		if m.Note != "" {
			fmt.Fprintf(stdout, ", %q", m.Note)
		}
		fmt.Fprintln(stdout)
	}
	if len(all) == 0 {
		fmt.Fprintln(stdout, "no versions")
	}
	return exitOK
}

func short8(s string) string {
	if len(s) > 8 {
		return s[:8]
	}
	return s
}

func routeEditsExport(ctx context.Context, store *routeedit.Store, pos []string, to, cfgDir string, stdout, stderr io.Writer) int {
	version := 0
	if len(pos) == 2 {
		var err error
		if version, err = versionArg(pos[1]); err != nil {
			fmt.Fprintln(stderr, "route-edits:", err)
			return exitUsage
		}
	}
	agents, err := dashboard.DiscoverAgents(cfgDir, "auto")
	if err != nil {
		fmt.Fprintln(stderr, "route-edits: --config-dir:", err)
		return exitUsage
	}
	// A commander run reloads its plan file at every leg: a folder a running agent reads is not written to.
	guard := func(real string) error {
		for _, a := range agents {
			if a.DungeonsDir == "" || !routeedit.Overlaps(real, a.DungeonsDir) {
				continue
			}
			running, err := agentRuns(ctx, a)
			switch {
			case err != nil:
				return fmt.Errorf("%s is (or holds) the plan folder %s of the agent on rpc %s, and its state is unknown (%v): not written", real, a.DungeonsDir, a.Addr, err)
			case running:
				return fmt.Errorf("%s is (or holds) the plan folder %s of the agent on rpc %s, which is running a dungeon: its plan reload would take the file at the next leg unchecked. Export into a fresh folder and point a stopped slot at it", real, a.DungeonsDir, a.Addr)
			}
			fmt.Fprintf(stderr, "route-edits: note: %s is the plan folder of the agent on rpc %s (not running)\n", a.DungeonsDir, a.Addr)
		}
		return nil
	}
	ex, err := routeedit.Export(store, pos[0], version, to, guard)
	if err != nil {
		fmt.Fprintln(stderr, "route-edits:", err)
		return 1
	}
	for _, w := range mmapsMismatch(cfgDir, to, ex.Meta.Mmaps) {
		fmt.Fprintln(stderr, "route-edits: warning:", w)
	}
	fmt.Fprintf(stdout, "route_edit=%s\n", ex.Tag())
	fmt.Fprintf(stderr, "route-edits: wrote %s; pass trace_meta {\"route_edit\": %q} to dungeon_run\n", ex.Path, ex.Tag())
	return exitOK
}

// agentRuns asks an agent whether a dungeon run is on. An agent that does not answer because nothing listens is not running.
func agentRuns(ctx context.Context, a dashboard.Agent) (bool, error) {
	cctx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	res, err := dashboard.NewClient(a, 3*time.Second).Call(cctx, "dungeon_status")
	if err != nil {
		if errors.Is(err, syscall.ECONNREFUSED) {
			return false, nil
		}
		return false, err
	}
	var st struct {
		Running bool `json:"running"`
	}
	if err := json.Unmarshal(res, &st); err != nil {
		return false, err
	}
	return st.Running, nil
}

// mmapsMismatch names the slots whose plan folder is dir (or holds it) and whose mmaps_dir is not the navmesh the version was checked on.
func mmapsMismatch(cfgDir, dir, checkedOn string) []string {
	files, _ := filepath.Glob(filepath.Join(cfgDir, "settings*.json"))
	var out []string
	for _, f := range files {
		data, err := os.ReadFile(f)
		if err != nil {
			continue
		}
		var s struct {
			RPC         string `json:"rpc"`
			DungeonsDir string `json:"dungeons_dir"`
			MmapsDir    string `json:"mmaps_dir"`
		}
		if json.Unmarshal(data, &s) != nil || s.DungeonsDir == "" || !routeedit.Overlaps(dir, expandHome(s.DungeonsDir)) {
			continue
		}
		if m := expandHome(s.MmapsDir); filepath.Clean(m) != filepath.Clean(checkedOn) {
			out = append(out, fmt.Sprintf("the edit was checked on the navmesh in %s, but %s (the slot on rpc %s) uses %q: a check on another server's mesh proves nothing", checkedOn, filepath.Base(f), s.RPC, m))
		}
	}
	return out
}

func routeEditsPromote(store *routeedit.Store, pos []string, worktree, msgFile string, stdout, stderr io.Writer) int {
	version, err := versionArg(pos[1])
	if err != nil || version == 0 {
		fmt.Fprintln(stderr, "route-edits: promote needs a version like v0007")
		return exitUsage
	}
	pr, err := routeedit.Promote(store, pos[0], version, worktree)
	if err != nil {
		fmt.Fprintln(stderr, "route-edits:", err)
		return 1
	}
	if msgFile != "" {
		if err := os.WriteFile(msgFile, []byte(pr.Message), 0o644); err != nil {
			fmt.Fprintln(stderr, "route-edits:", err)
			return 1
		}
	}
	fmt.Fprintf(stderr, "route-edits: wrote %s\n", pr.Path)
	fmt.Fprint(stdout, pr.Message)
	return exitOK
}
