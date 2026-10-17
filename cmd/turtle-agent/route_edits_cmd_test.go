package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/pjerra/turtle-agent/internal/dungeon"
	"github.com/pjerra/turtle-agent/internal/routeedit"
)

const editsPlan = `{"version":1,"name":"edit1","map":209,"entrance":{"x":0,"y":0,"z":5},
 "legs":[{"name":"one","to":{"x":100,"y":0,"z":5},"via":[{"x":20,"y":0,"z":5},{"x":40,"y":0,"z":5}],"pull_groups":[{"entries":[7001],"positions":[{"x":120,"y":0,"z":5}]}]}]}`

type editsRig struct {
	t      *testing.T
	store  *routeedit.Store
	cfg    string
	base   []byte
	edit   []byte
	editSH string
}

func newEditsRig(t *testing.T) *editsRig {
	t.Helper()
	root := t.TempDir()
	r := &editsRig{t: t, cfg: filepath.Join(root, "config")}
	if err := os.MkdirAll(r.cfg, 0o755); err != nil {
		t.Fatal(err)
	}
	var err error
	if r.store, err = routeedit.NewStore(filepath.Join(root, "edits")); err != nil {
		t.Fatal(err)
	}
	p, err := dungeon.Parse("edit1.json", []byte(editsPlan))
	if err != nil {
		t.Fatal(err)
	}
	r.base, _ = dungeon.Marshal(p)
	p.Legs[0].Via[0].X = 21
	r.edit, _ = dungeon.Marshal(p)
	sum := sha256.Sum256(r.base)
	r.editSH = hex.EncodeToString(func() []byte { s := sha256.Sum256(r.edit); return s[:] }())
	save := func(status string, plan []byte) {
		m := &routeedit.Meta{Who: "owner", At: "2026-10-16T01:00:00Z", Status: status, Note: "shorter", Mmaps: "/mm/vm",
			Base:  routeedit.BaseInfo{Source: "/src/edit1.json", SHA256: hex.EncodeToString(sum[:])},
			Check: routeedit.CheckResult{Checked: true, OK: status == routeedit.StatusChecked, Points: 7}, Ops: []routeedit.Op{{Leg: 0, Kind: "move", Index: 0, X: 21, Y: 0}}}
		if _, err := r.store.Save("edit1", m, plan, "d"); err != nil {
			t.Fatal(err)
		}
	}
	save(routeedit.StatusChecked, r.edit)
	save(routeedit.StatusRefused, nil)
	return r
}

func (r *editsRig) run(args ...string) (int, string, string) {
	r.t.Helper()
	var out, errb bytes.Buffer
	code := routeEditsCmd(context.Background(), append(args, "--edit-dir", r.store.Root()), &out, &errb)
	return code, out.String(), errb.String()
}

func TestRouteEditsList(t *testing.T) {
	r := newEditsRig(t)
	code, out, _ := r.run("list")
	if code != 0 || !strings.Contains(out, "edit1 v0001 checked") || !strings.Contains(out, "edit1 v0002 refused") || !strings.Contains(out, "by owner") || !strings.Contains(out, `"shorter"`) {
		t.Fatalf("%d %s", code, out)
	}
	if code, out, _ := r.run("list", "edit1"); code != 0 || strings.Count(out, "\n") != 2 {
		t.Errorf("one plan: %d %s", code, out)
	}
	code, out, _ = r.run("list", "--json", "edit1")
	var ms []routeedit.Meta
	if code != 0 || json.Unmarshal([]byte(out), &ms) != nil || len(ms) != 2 {
		t.Errorf("json: %d %s", code, out)
	}
	if code, out, _ := r.run("list", "never-edited"); code != 0 || !strings.Contains(out, "no versions") {
		t.Errorf("no versions: %d %s", code, out)
	}
	if code, _, _ := r.run("list", "../x"); code == 0 {
		t.Error("a path")
	}
	var o, e bytes.Buffer
	if code := routeEditsCmd(context.Background(), []string{"list", "--edit-dir", filepath.Join(t.TempDir(), "none")}, &o, &e); code != exitUsage {
		t.Errorf("a missing folder: %d", code)
	}
	if code := routeEditsCmd(context.Background(), []string{"frobnicate"}, &o, &e); code != exitUsage {
		t.Errorf("unknown subcommand: %d", code)
	}
	if code := routeEditsCmd(context.Background(), nil, &o, &e); code != exitUsage {
		t.Errorf("no subcommand: %d", code)
	}
}

func TestRouteEditsExportWritesTheNewestCheckedVersionIntoAFreshFolder(t *testing.T) {
	r := newEditsRig(t)
	dest := filepath.Join(t.TempDir(), "ta-new", "dungeons")
	code, out, errs := r.run("export", "--config-dir", r.cfg, "--to", dest, "edit1")
	if code != 0 {
		t.Fatalf("%d %s", code, errs)
	}
	if out != "route_edit=edit1@v0001 sha256="+r.editSH+"\n" {
		t.Errorf("the tag line: %q", out)
	}
	if b, _ := os.ReadFile(filepath.Join(dest, "edit1.json")); string(b) != string(r.edit) {
		t.Error("not the version's bytes")
	}
	if !strings.Contains(errs, "trace_meta") {
		t.Errorf("the hint for the run: %s", errs)
	}
	// the folder is used up; the refused version cannot be exported; flags may follow the arguments
	if code, _, errs := r.run("export", "--config-dir", r.cfg, "--to", dest, "edit1"); code == 0 || !strings.Contains(errs, "not empty") {
		t.Errorf("second export: %d %s", code, errs)
	}
	if code, _, errs := r.run("export", "edit1", "v0002", "--config-dir", r.cfg, "--to", filepath.Join(t.TempDir(), "x")); code == 0 || !strings.Contains(errs, "refused") {
		t.Errorf("refused: %d %s", code, errs)
	}
	if code, out, _ := r.run("export", "edit1", "1", "--config-dir", r.cfg, "--to", filepath.Join(t.TempDir(), "y")); code != 0 || !strings.Contains(out, "@v0001") {
		t.Errorf("version 1: %d %s", code, out)
	}
	for _, args := range [][]string{{"export", "edit1"}, {"export", "--to", "x"}, {"export", "--to", "x", "edit1", "vX"}, {"export", "--to", "x", "a", "b", "c"}} {
		if code, _, _ := r.run(args...); code != exitUsage {
			t.Errorf("%v: %d", args, code)
		}
	}
}

// slot makes a settings file for an agent on a port, reading the plan folder dir, with mmaps.
func (r *editsRig) slot(name string, port int, dir, mmaps string) {
	r.t.Helper()
	b, _ := json.Marshal(map[string]string{"rpc": "127.0.0.1:" + strconv.Itoa(port), "dungeons_dir": dir, "mmaps_dir": mmaps})
	if err := os.WriteFile(filepath.Join(r.cfg, name), b, 0o600); err != nil {
		r.t.Fatal(err)
	}
}

// fakeAgent answers dungeon_status as an agent whose run is on (or not).
func fakeAgent(t *testing.T, running bool) int {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		var in struct {
			Method string `json:"method"`
		}
		_ = json.NewDecoder(req.Body).Decode(&in)
		if in.Method != "dungeon_status" {
			http.Error(w, "no", http.StatusBadRequest)
			return
		}
		_, _ = w.Write([]byte(`{"jsonrpc":"2.0","id":1,"result":{"running":` + strconv.FormatBool(running) + `}}`))
	}))
	t.Cleanup(srv.Close)
	_, p, _ := net.SplitHostPort(srv.Listener.Addr().String())
	port, _ := strconv.Atoi(p)
	return port
}

func TestRouteEditsExportNeverWritesWhereARunningAgentReads(t *testing.T) {
	r := newEditsRig(t)
	slotDir := filepath.Join(t.TempDir(), "ta-r4-s1", "dungeons")
	if err := os.MkdirAll(slotDir, 0o755); err != nil {
		t.Fatal(err)
	}
	r.slot("settings-1.json", fakeAgent(t, true), slotDir, "/mm/vm")
	for name, to := range map[string]string{"the folder itself": slotDir, "inside it": filepath.Join(slotDir, "new"), "the folder above it": filepath.Dir(slotDir)} {
		code, _, errs := r.run("export", "--config-dir", r.cfg, "--to", to, "edit1")
		if code == 0 || !strings.Contains(errs, "running a dungeon") {
			t.Errorf("%s: %d %s", name, code, errs)
		}
	}
	if ents, _ := os.ReadDir(slotDir); len(ents) != 0 {
		t.Errorf("something was written in a running slot's folder: %v", ents)
	}
	// the same slot, stopped: the empty folder may be filled, with a note; and a different mesh is warned about
	r2 := newEditsRig(t)
	slot2 := filepath.Join(t.TempDir(), "ta-r4-s2", "dungeons")
	os.MkdirAll(slot2, 0o755)
	r2.slot("settings-2.json", fakeAgent(t, false), slot2, "/mm/other")
	code, _, errs := r2.run("export", "--config-dir", r2.cfg, "--to", slot2, "edit1")
	if code != 0 || !strings.Contains(errs, "not running") || !strings.Contains(errs, "another server's mesh") {
		t.Errorf("stopped slot: %d %s", code, errs)
	}
	// an agent that is not there at all is not running
	r3 := newEditsRig(t)
	slot3 := filepath.Join(t.TempDir(), "dungeons")
	l, _ := net.Listen("tcp", "127.0.0.1:0")
	_, p, _ := net.SplitHostPort(l.Addr().String())
	l.Close()
	port, _ := strconv.Atoi(p)
	r3.slot("settings-3.json", port, slot3, "/mm/vm")
	if code, _, errs := r3.run("export", "--config-dir", r3.cfg, "--to", slot3, "edit1"); code != 0 {
		t.Errorf("offline agent: %d %s", code, errs)
	} else if strings.Contains(errs, "another server's mesh") {
		t.Errorf("the same mesh was warned about: %s", errs)
	}
	// an agent that answers something unreadable: the state is unknown, nothing is written
	r4 := newEditsRig(t)
	slot4 := filepath.Join(t.TempDir(), "dungeons")
	bad := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { http.Error(w, "boom", 500) }))
	defer bad.Close()
	_, p4, _ := net.SplitHostPort(bad.Listener.Addr().String())
	port4, _ := strconv.Atoi(p4)
	r4.slot("settings-4.json", port4, slot4, "/mm/vm")
	if code, _, errs := r4.run("export", "--config-dir", r4.cfg, "--to", slot4, "edit1"); code == 0 || !strings.Contains(errs, "state is unknown") {
		t.Errorf("unknown state: %d %s", code, errs)
	}
	if _, err := os.Stat(slot4); err == nil {
		t.Error("wrote where an agent of unknown state reads")
	}
}

func TestRouteEditsPromote(t *testing.T) {
	r := newEditsRig(t)
	wt := t.TempDir()
	os.Mkdir(filepath.Join(wt, "dungeons"), 0o755)
	os.WriteFile(filepath.Join(wt, ".git"), []byte("gitdir: x"), 0o644)
	os.WriteFile(filepath.Join(wt, "dungeons", "edit1.json"), r.base, 0o644)
	msg := filepath.Join(t.TempDir(), "msg")
	code, out, errs := r.run("promote", "--worktree", wt, "--message-file", msg, "edit1", "v0001")
	if code != 0 {
		t.Fatalf("%d %s", code, errs)
	}
	if b, _ := os.ReadFile(filepath.Join(wt, "dungeons", "edit1.json")); string(b) != string(r.edit) {
		t.Error("not the canonical bytes of the version")
	}
	if b, _ := os.ReadFile(msg); string(b) != out || !strings.HasPrefix(out, "dungeons: edit1 route edit v0001 (owner,") {
		t.Errorf("message: %q", out)
	}
	// again: the worktree is no longer on the edit's base
	if code, _, errs := r.run("promote", "--worktree", wt, "edit1", "v0001"); code == 0 || !strings.Contains(errs, "started from") {
		t.Errorf("second promote: %d %s", code, errs)
	}
	for _, args := range [][]string{{"promote", "--worktree", wt, "edit1"}, {"promote", "edit1", "v0001"}, {"promote", "--worktree", wt, "edit1", "latest"}, {"promote", "--worktree", wt, "edit1", "v0002"}} {
		if code, _, _ := r.run(args...); code == 0 {
			t.Errorf("%v: accepted", args)
		}
	}
}
