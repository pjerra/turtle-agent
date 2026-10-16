package dashboard

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/pjerra/turtle-agent/internal/navmesh"
	"github.com/pjerra/turtle-agent/internal/navmesh/navfixture"
	"github.com/pjerra/turtle-agent/internal/routeedit"
)

const (
	editToken = "t0k3n-for-the-tests-only-0123456789abcdef"
	editPlan  = `{"version":1,"name":"edit1","map":209,"entrance":{"x":0,"y":0,"z":5},
 "legs":[
  {"name":"one","to":{"x":100,"y":0,"z":5},
   "via":[{"x":20,"y":0,"z":5},{"x":40,"y":0,"z":5},{"x":60,"y":0,"z":5,"direct":true}],
   "pull_groups":[{"entries":[7001],"positions":[{"x":120,"y":0,"z":5}]}]},
  {"name":"two","to":{"x":200,"y":0,"z":5},"boss":{"entry":6001,"position":{"x":220,"y":0,"z":5}}}]}`
)

// testNav is a flat mesh at height 5 with holes (a point in one is off the mesh).
type testNav struct{ holes [][4]float32 }

func (n testNav) in(x, y float32) bool {
	for _, h := range n.holes {
		if x >= h[0] && x <= h[2] && y >= h[1] && y <= h[3] {
			return true
		}
	}
	return false
}
func (n testNav) Plan(_ uint32, from, to navmesh.Vec3) (navmesh.Path, error) {
	return navmesh.Path{Status: navmesh.Complete, Points: []navmesh.Vec3{from, to}, Length: 1}, nil
}
func (n testNav) Raycast(uint32, navmesh.Vec3, navmesh.Vec3) (navmesh.Ray, error) {
	return navmesh.Ray{Known: true, Clear: true}, nil
}
func (n testNav) Nearest(_ uint32, p navmesh.Vec3) (navmesh.Vec3, bool, error) {
	if n.in(p[0], p[1]) {
		return navmesh.Vec3{}, false, nil
	}
	return navmesh.Vec3{p[0], p[1], 5}, true, nil
}
func (n testNav) Floors(_ uint32, x, y, lo, hi float32) ([]navmesh.Floor, error) {
	if n.in(x, y) || lo > 5 || hi < 5 {
		return nil, nil
	}
	return []navmesh.Floor{{Z: 5, Flags: navmesh.NavGround}}, nil
}

type editRig struct {
	t      *testing.T
	src    string
	edits  string
	cfg    string
	srv    *httptest.Server
	ed     *Editor
	p      *Poller
	base   string // sha256 of the plan file
	tokens string
}

func newEditRig(t *testing.T, nav Nav) *editRig {
	t.Helper()
	root := t.TempDir()
	r := &editRig{t: t, src: filepath.Join(root, "source"), edits: filepath.Join(root, "edits"), cfg: filepath.Join(root, "config")}
	for _, d := range []string{r.src, r.cfg} {
		must(t, os.MkdirAll(d, 0o755))
	}
	must(t, os.WriteFile(filepath.Join(r.src, "edit1.json"), []byte(editPlan), 0o644))
	r.tokens = filepath.Join(root, "tokens")
	must(t, os.WriteFile(r.tokens, []byte("owner "+editToken+"\nhypeer "+strings.Repeat("h", 40)+"\n"), 0o600))
	tk, err := routeedit.LoadTokens(r.tokens)
	must(t, err)
	store, err := routeedit.NewStore(r.edits)
	must(t, err)
	r.p = NewPoller(Options{ConfigDir: r.cfg, PlansDirs: []string{r.src}, Agents: "auto"})
	if nav == nil {
		nav = testNav{}
	}
	r.ed, err = NewEditor(r.p, EditConfig{Source: r.src, Store: store, Tokens: tk, Nav: nav, Mmaps: "/mmaps/test"})
	must(t, err)
	r.srv = httptest.NewServer(HandlerWithEditor(r.p, "", nil, nil, r.ed))
	t.Cleanup(r.srv.Close)
	sum := sha256.Sum256([]byte(editPlan))
	r.base = hex.EncodeToString(sum[:])
	return r
}

type reply struct {
	code int
	body string
}

func (r *editRig) do(method, path string, body []byte, hdr map[string]string) reply {
	r.t.Helper()
	req, err := http.NewRequest(method, r.srv.URL+path, bytes.NewReader(body))
	must(r.t, err)
	for k, v := range hdr {
		req.Header.Set(k, v)
	}
	resp, err := http.DefaultClient.Do(req)
	must(r.t, err)
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	return reply{resp.StatusCode, string(b)}
}

func (r *editRig) post(plan string, req map[string]any, token string) reply {
	r.t.Helper()
	b, _ := json.Marshal(req)
	h := map[string]string{"Content-Type": "application/json"}
	if token != "" {
		h["Authorization"] = "Bearer " + token
	}
	return r.do(http.MethodPost, "/api/edit/"+plan, b, h)
}

func (r *editRig) edit(ops ...map[string]any) map[string]any {
	return map[string]any{"base_sha256": r.base, "note": "test", "ops": ops}
}

func move(leg, idx int, x, y float64) map[string]any {
	return map[string]any{"leg": leg, "kind": "move", "index": idx, "x": x, "y": y}
}

// tree lists every file under dir with its content hash, to show that nothing changed.
func tree(t *testing.T, dir string) string {
	t.Helper()
	var out []string
	_ = filepath.Walk(dir, func(p string, info os.FileInfo, err error) error {
		if err == nil && !info.IsDir() {
			b, _ := os.ReadFile(p)
			sum := sha256.Sum256(b)
			rel, _ := filepath.Rel(dir, p)
			out = append(out, rel+":"+hex.EncodeToString(sum[:4]))
		}
		return nil
	})
	sort.Strings(out)
	return strings.Join(out, " ")
}

func TestEditIsOffUnlessTheEditorIsOn(t *testing.T) {
	srv := httptest.NewServer(Handler(NewPoller(Options{ConfigDir: t.TempDir()}), "", nil))
	defer srv.Close()
	for _, path := range []string{"/api/edit/edit1", "/api/edit/status", "/api/state", "/api/plans"} {
		req, _ := http.NewRequest(http.MethodPost, srv.URL+path, strings.NewReader(`{}`))
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Authorization", "Bearer "+editToken)
		resp, err := http.DefaultClient.Do(req)
		must(t, err)
		resp.Body.Close()
		if resp.StatusCode != http.StatusMethodNotAllowed {
			t.Errorf("POST %s with the editor off: %d", path, resp.StatusCode)
		}
	}
	resp, err := http.Get(srv.URL + "/api/edit/status")
	must(t, err)
	resp.Body.Close()
	if resp.StatusCode != http.StatusNotFound {
		t.Errorf("GET /api/edit/status with the editor off: %d", resp.StatusCode)
	}
}

func TestEditNeedsTheTokenAndAJSONBody(t *testing.T) {
	r := newEditRig(t, nil)
	before := tree(t, r.edits) + "|" + tree(t, r.src)
	good := r.edit(move(0, 0, 21, 1))
	b, _ := json.Marshal(good)
	for name, h := range map[string]map[string]string{
		"no token":         {"Content-Type": "application/json"},
		"wrong token":      {"Content-Type": "application/json", "Authorization": "Bearer " + strings.Repeat("x", 40)},
		"basic":            {"Content-Type": "application/json", "Authorization": "Basic " + editToken},
		"token as bare":    {"Content-Type": "application/json", "Authorization": editToken},
		"token in a query": {"Content-Type": "application/json"},
	} {
		path := "/api/edit/edit1"
		if name == "token in a query" {
			path += "?token=" + editToken
		}
		if got := r.do(http.MethodPost, path, b, h); got.code != http.StatusUnauthorized {
			t.Errorf("%s: %d %s", name, got.code, got.body)
		}
	}
	for name, ct := range map[string]string{"form": "application/x-www-form-urlencoded", "text": "text/plain", "none": ""} {
		h := map[string]string{"Authorization": "Bearer " + editToken}
		if ct != "" {
			h["Content-Type"] = ct
		}
		if got := r.do(http.MethodPost, "/api/edit/edit1", b, h); got.code != http.StatusUnsupportedMediaType {
			t.Errorf("content type %s: %d", name, got.code)
		}
	}
	// the right token on a Host the guard does not know is refused before the token is looked at
	req, _ := http.NewRequest(http.MethodPost, r.srv.URL+"/api/edit/edit1", bytes.NewReader(b))
	req.Host = "evil.example.com"
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+editToken)
	resp, err := http.DefaultClient.Do(req)
	must(t, err)
	resp.Body.Close()
	if resp.StatusCode != http.StatusForbidden {
		t.Errorf("foreign Host: %d", resp.StatusCode)
	}
	if after := tree(t, r.edits) + "|" + tree(t, r.src); after != before {
		t.Errorf("a refused request wrote something:\n%s\n%s", before, after)
	}
	// the other methods stay refused, token or not
	for _, m := range []string{http.MethodPut, http.MethodDelete, http.MethodPatch} {
		if got := r.do(m, "/api/edit/edit1", b, map[string]string{"Authorization": "Bearer " + editToken, "Content-Type": "application/json"}); got.code != http.StatusMethodNotAllowed {
			t.Errorf("%s: %d", m, got.code)
		}
	}
	// POST outside /api/edit/ stays refused with the right token
	if got := r.do(http.MethodPost, "/api/state", b, map[string]string{"Authorization": "Bearer " + editToken, "Content-Type": "application/json"}); got.code != http.StatusMethodNotAllowed {
		t.Errorf("POST /api/state: %d", got.code)
	}
}

func TestFailedTokensAreLimited(t *testing.T) {
	r := newEditRig(t, nil)
	for i := 0; i < 10; i++ {
		if got := r.post("edit1", r.edit(move(0, 0, 21, 1)), "wrong-"+strings.Repeat("x", 40)); got.code != http.StatusUnauthorized {
			t.Fatalf("try %d: %d", i, got.code)
		}
	}
	if got := r.post("edit1", r.edit(move(0, 0, 21, 1)), editToken); got.code != http.StatusTooManyRequests {
		t.Errorf("the right token after ten failures: %d", got.code)
	}
}

func TestEditBodyLimitsAndShape(t *testing.T) {
	r := newEditRig(t, nil)
	h := map[string]string{"Authorization": "Bearer " + editToken, "Content-Type": "application/json"}
	big := `{"note":"` + strings.Repeat("a", maxEditBody) + `"}`
	if got := r.do(http.MethodPost, "/api/edit/edit1", []byte(big), h); got.code != http.StatusRequestEntityTooLarge {
		t.Errorf("oversized: %d %.80s", got.code, got.body)
	}
	for name, body := range map[string]string{
		"not json":       `not json`,
		"unknown field":  `{"base_sha256":"` + r.base + `","ops":[],"plan":{"name":"x"}}`,
		"a whole plan":   `{"version":1,"name":"edit1","map":209,"legs":[]}`,
		"two documents":  `{"base_sha256":"` + r.base + `","ops":[]}{"x":1}`,
		"ops not a list": `{"base_sha256":"` + r.base + `","ops":{"leg":0}}`,
		"empty":          ``,
	} {
		if got := r.do(http.MethodPost, "/api/edit/edit1", []byte(body), h); got.code != http.StatusBadRequest {
			t.Errorf("%s: %d %.100s", name, got.code, got.body)
		}
	}
	if got := r.post("edit1", map[string]any{"base_sha256": r.base, "ops": []any{}}, editToken); got.code != http.StatusBadRequest {
		t.Errorf("no ops: %d", got.code)
	}
	if names := tree(t, r.edits); names != "" {
		t.Errorf("bad requests wrote: %s", names)
	}
}

func TestEditPlanNamesAndPathsNeverReachTheDisk(t *testing.T) {
	r := newEditRig(t, nil)
	outside := filepath.Join(filepath.Dir(r.src), "secret.json")
	must(t, os.WriteFile(outside, []byte(editPlan), 0o644))
	before := tree(t, filepath.Dir(r.src))
	for _, path := range []string{"Edit1", "../source/edit1", "..%2Fsource%2Fedit1", "%2e%2e/secret", "edit1/../../secret", "edit1%00", "a b", "edit1.json", "nope", strings.Repeat("a", 41)} {
		got := r.do(http.MethodPost, "/api/edit/"+path, []byte(`{"base_sha256":"`+r.base+`","ops":[{"leg":0,"kind":"move","index":0,"x":21,"y":1}]}`),
			map[string]string{"Authorization": "Bearer " + editToken, "Content-Type": "application/json"})
		if got.code == http.StatusOK || got.code == http.StatusUnprocessableEntity {
			t.Errorf("%q: %d", path, got.code)
		}
	}
	if after := tree(t, filepath.Dir(r.src)); after != before {
		t.Errorf("a path reached the disk:\n%s\n%s", before, after)
	}
	// a plan file in the source that is a link out of it is not edited
	if err := os.Symlink(outside, filepath.Join(r.src, "linked.json")); err == nil {
		if got := r.post("linked", map[string]any{"base_sha256": r.base, "ops": []any{move(0, 0, 21, 1)}}, editToken); got.code != http.StatusNotFound {
			t.Errorf("linked plan: %d", got.code)
		}
	}
}

func TestACheckedEditIsKeptAsAVersionAndTheSourceIsUntouched(t *testing.T) {
	r := newEditRig(t, nil)
	srcBefore := tree(t, r.src)
	got := r.post("edit1", map[string]any{"base_sha256": r.base, "note": "line1\nline2\x00" + strings.Repeat("n", 600), "ops": []any{
		move(0, 0, 21, 1), map[string]any{"leg": 1, "kind": "insert", "index": 0, "x": 210, "y": 5}}}, editToken)
	if got.code != http.StatusOK {
		t.Fatalf("%d %s", got.code, got.body)
	}
	var rep EditReply
	must(t, json.Unmarshal([]byte(got.body), &rep))
	if rep.Status != "checked" || rep.Version != 1 || rep.Name != "v0001" || rep.Who != "owner" || !rep.Check.OK || len(rep.Changes) != 2 || !strings.Contains(rep.Diff, "@@") {
		t.Fatalf("%+v", rep)
	}
	// the source file is as it was; the edits folder holds the three server-named files
	if srcBefore != tree(t, r.src) {
		t.Error("the source folder changed")
	}
	ents, _ := os.ReadDir(filepath.Join(r.ed.cfg.Store.Root(), "edit1"))
	var names []string
	for _, e := range ents {
		names = append(names, e.Name())
	}
	if strings.Join(names, ",") != "v0001.diff,v0001.json,v0001.meta.json" {
		t.Fatalf("files: %v", names)
	}
	// the version is the whole plan, written with dungeon.Marshal: it loads, and differs from the base in the two points only
	var m routeedit.Meta
	b, _ := os.ReadFile(filepath.Join(r.ed.cfg.Store.Root(), "edit1", "v0001.meta.json"))
	must(t, json.Unmarshal(b, &m))
	if m.Who != "owner" || m.Status != "checked" || m.Base.SHA256 != r.base || m.Mmaps != "/mmaps/test" || len(m.Ops) != 2 || len(m.PlanDiff) == 0 || m.SHA256 == "" || !m.Check.Checked || len(m.Note) > 500 || strings.ContainsAny(m.Note, "\n\x00") || m.At == "" {
		t.Errorf("%+v", m)
	}
	pb, _ := os.ReadFile(filepath.Join(r.ed.cfg.Store.Root(), "edit1", "v0001.json"))
	if sum := sha256.Sum256(pb); hex.EncodeToString(sum[:]) != m.SHA256 {
		t.Error("the recorded sha256 is not the file's")
	}
	if !strings.Contains(string(pb), `"x": 21, "y": 1, "z": 5`) || !strings.HasSuffix(string(pb), "}\n") {
		t.Errorf("not the canonical file form:\n%.400s", pb)
	}
	// it can be read back
	if got := r.do(http.MethodGet, "/api/edit/edit1/versions", nil, nil); got.code != 200 || !strings.Contains(got.body, `"who":"owner"`) || !strings.Contains(got.body, r.base) {
		t.Errorf("versions: %d %s", got.code, got.body)
	}
	if got := r.do(http.MethodGet, "/api/edit/edit1/v0001/diff", nil, nil); got.code != 200 || !strings.Contains(got.body, "+") || !strings.HasPrefix(got.body, "--- edit1.json") {
		t.Errorf("diff: %d %.200s", got.code, got.body)
	}
	if got := r.do(http.MethodGet, "/api/edit/edit1/v0001", nil, nil); got.code != 200 || !strings.Contains(got.body, `"status":"checked"`) {
		t.Errorf("meta: %d", got.code)
	}
	for _, bad := range []string{"/api/edit/edit1/v0009", "/api/edit/edit1/v1", "/api/edit/edit1/v0001/json", "/api/edit/edit1/v0001/diff/x", "/api/edit/Edit1/versions", "/api/edit/edit1/..%2Fx"} {
		if got := r.do(http.MethodGet, bad, nil, nil); got.code == 200 {
			t.Errorf("GET %s: 200", bad)
		}
	}
}

func TestARefusedEditKeepsARecordAndNoPlan(t *testing.T) {
	r := newEditRig(t, testNav{holes: [][4]float32{{10, 40, 30, 60}}})
	got := r.post("edit1", r.edit(move(0, 0, 25, 30)), editToken) // floor under it: (25, 30) is outside the hole; then the next hop is fine
	if got.code != 200 {
		t.Fatalf("a good move: %d %s", got.code, got.body)
	}
	// a move into the hole has no floor at all: the op itself is refused, nothing recorded
	if got := r.post("edit1", r.edit(move(0, 1, 20, 50)), editToken); got.code != http.StatusBadRequest || !strings.Contains(got.body, "off the mesh") {
		t.Errorf("no floor: %d %s", got.code, got.body)
	}
	// a point with a floor that the plan check cannot walk to: refused with a record
	r2 := newEditRig(t, blockedNav{})
	got = r2.post("edit1", r2.edit(move(0, 0, 21, 1)), editToken)
	if got.code != http.StatusUnprocessableEntity {
		t.Fatalf("%d %s", got.code, got.body)
	}
	var rep EditReply
	must(t, json.Unmarshal([]byte(got.body), &rep))
	if rep.Status != "refused" || rep.Version != 1 || rep.Check.OK || rep.Check.NewIssues == 0 || rep.Message == "" {
		t.Fatalf("%+v", rep)
	}
	ents, _ := os.ReadDir(filepath.Join(r2.ed.cfg.Store.Root(), "edit1"))
	var names []string
	for _, e := range ents {
		names = append(names, e.Name())
	}
	if strings.Join(names, ",") != "v0001.diff,v0001.meta.json" {
		t.Errorf("a refused edit keeps a record and a diff, no plan: %v", names)
	}
}

// blockedNav has floors everywhere but no path to a point whose x is 21: the plan check fails there, the floor lookup does not.
type blockedNav struct{ testNav }

func (blockedNav) Plan(_ uint32, from, to navmesh.Vec3) (navmesh.Path, error) {
	if to[0] == 21 {
		return navmesh.Path{Status: navmesh.NoPath, Reason: "no way"}, nil
	}
	return navmesh.Path{Status: navmesh.Complete, Points: []navmesh.Vec3{from, to}, Length: 1}, nil
}

func TestEditRefusesAChangedBaseAndEmptyChanges(t *testing.T) {
	r := newEditRig(t, nil)
	stale := r.edit(move(0, 0, 21, 1))
	stale["base_sha256"] = strings.Repeat("0", 64)
	if got := r.post("edit1", stale, editToken); got.code != http.StatusConflict || !strings.Contains(got.body, "changed under you") {
		t.Errorf("stale base: %d %s", got.code, got.body)
	}
	missing := r.edit(move(0, 0, 21, 1))
	delete(missing, "base_sha256")
	if got := r.post("edit1", missing, editToken); got.code != http.StatusBadRequest {
		t.Errorf("no base: %d", got.code)
	}
	// the same position again changes nothing
	if got := r.post("edit1", r.edit(move(0, 0, 20, 0)), editToken); got.code != http.StatusBadRequest || !strings.Contains(got.body, "change nothing") {
		t.Errorf("no change: %d %s", got.code, got.body)
	}
	// a locked point, a delete of `to`, a bad leg
	for name, op := range map[string]map[string]any{
		"direct":    move(0, 2, 61, 0),
		"delete to": {"leg": 0, "kind": "delete", "index": 3},
		"bad leg":   move(9, 0, 1, 1),
		"far away":  move(0, 0, 9000, 0),
	} {
		if got := r.post("edit1", r.edit(op), editToken); got.code != http.StatusBadRequest {
			t.Errorf("%s: %d %s", name, got.code, got.body)
		}
	}
	if names := tree(t, r.edits); names != "" {
		t.Errorf("refusals before the check wrote: %s", names)
	}
	// the file changes under the editor: the next edit from the old base is refused
	must(t, os.WriteFile(filepath.Join(r.src, "edit1.json"), []byte(strings.Replace(editPlan, `"x":20,`, `"x":22,`, 1)), 0o644))
	if got := r.post("edit1", r.edit(move(0, 0, 21, 1)), editToken); got.code != http.StatusConflict {
		t.Errorf("the file changed: %d", got.code)
	}
}

func TestTheEditorWillNotRunNextToASlotsPlanFolder(t *testing.T) {
	r := newEditRig(t, nil)
	slot := func(dir string) {
		must(t, os.WriteFile(filepath.Join(r.cfg, "settings-9.json"), []byte(`{"rpc":"127.0.0.1:8769","dungeons_dir":`+string(mustJSON(t, dir))+`}`), 0o600))
	}
	// a slot whose plan folder holds the edits folder, is it, or lies inside it: refused at the moment of the edit, nothing written
	for name, dir := range map[string]string{"is it": r.ed.cfg.Store.Root(), "contains it": filepath.Dir(r.ed.cfg.Store.Root()), "inside it": filepath.Join(r.ed.cfg.Store.Root(), "edit1")} {
		slot(dir)
		before := tree(t, filepath.Dir(r.edits))
		if got := r.post("edit1", r.edit(move(0, 0, 21, 1)), editToken); got.code != http.StatusServiceUnavailable {
			t.Errorf("%s: %d %s", name, got.code, got.body)
		}
		if tree(t, filepath.Dir(r.edits)) != before {
			t.Errorf("%s: wrote", name)
		}
	}
	// and the editor will not even start with a source that lies inside a slot's plan folder
	slot(filepath.Dir(r.src))
	tk, _ := routeedit.LoadTokens(r.tokens)
	store, _ := routeedit.NewStore(filepath.Join(t.TempDir(), "e"))
	if _, err := NewEditor(r.p, EditConfig{Source: r.src, Store: store, Tokens: tk, Nav: testNav{}, Mmaps: "/m"}); err == nil {
		t.Error("started with the source inside a slot's plan folder")
	}
	// a slot elsewhere is no hindrance
	slot(t.TempDir())
	if got := r.post("edit1", r.edit(move(0, 0, 21, 1)), editToken); got.code != http.StatusOK {
		t.Errorf("a slot elsewhere: %d %s", got.code, got.body)
	}
}

func TestNewEditorNeedsItsParts(t *testing.T) {
	p := NewPoller(Options{ConfigDir: t.TempDir()})
	if _, err := NewEditor(p, EditConfig{}); err == nil {
		t.Error("an empty setup")
	}
	if _, err := NewEditor(p, EditConfig{Source: "/nonexistent-dir-xyz", Mmaps: "/m"}); err == nil {
		t.Error("a missing source")
	}
}

func TestSnapAndStatusWriteNothing(t *testing.T) {
	r := newEditRig(t, testNav{holes: [][4]float32{{10, 40, 30, 60}}})
	before := tree(t, filepath.Dir(r.edits))
	got := r.do(http.MethodGet, "/api/edit/edit1/snap?kind=move&leg=0&index=1&x=41&y=2", nil, nil)
	var s struct {
		OK     bool             `json:"ok"`
		Change routeedit.Change `json:"change"`
	}
	must(t, json.Unmarshal([]byte(got.body), &s))
	if got.code != 200 || !s.OK || s.Change.To == nil || s.Change.To.Z != 5 {
		t.Errorf("snap: %d %s", got.code, got.body)
	}
	got = r.do(http.MethodGet, "/api/edit/edit1/snap?kind=move&leg=0&index=1&x=20&y=50", nil, nil)
	if got.code != 200 || !strings.Contains(got.body, `"ok":false`) || !strings.Contains(got.body, "off the mesh") {
		t.Errorf("snap into the hole: %d %s", got.code, got.body)
	}
	for _, q := range []string{"leg=x&index=1&x=1&y=1", "kind=delete&leg=0&index=0&x=1&y=1", "leg=0&index=0&x=NaN&y=1"} {
		got = r.do(http.MethodGet, "/api/edit/edit1/snap?"+q, nil, nil)
		if got.code == 200 && strings.Contains(got.body, `"ok":true`) {
			t.Errorf("snap %s: %s", q, got.body)
		}
	}
	if got := r.do(http.MethodGet, "/api/edit/status", nil, nil); got.code != 200 || !strings.Contains(got.body, `"enabled":true`) || strings.Contains(got.body, editToken) {
		t.Errorf("status: %d %s", got.code, got.body)
	}
	if tree(t, filepath.Dir(r.edits)) != before {
		t.Error("snap or status wrote")
	}
}

// The whole path on a real navmesh (a corridor made by navfixture): a move along it passes, a move onto an island of mesh the corridor does
// not reach is refused by the plan check with a record, and a move off the mesh is refused as an op.
func TestEditOnARealNavmesh(t *testing.T) {
	polys := append(navfixture.LCorridor(0), navfixture.Square(60, 60, 64, 64, 0))
	planner := navmesh.NewPlanner(navfixture.WriteDir(t, 209, polys))
	defer planner.Close()
	if _, err := planner.Floors(209, 12, 12, -5, 5); errors.Is(err, navmesh.ErrNoEngine) {
		t.Skip("built without the navmesh engine")
	}
	const meshPlan = `{"version":1,"name":"edit1","map":209,"entrance":{"x":12,"y":12,"z":0},
 "legs":[{"name":"corridor","to":{"x":40,"y":12,"z":0},"via":[{"x":20,"y":12,"z":0},{"x":30,"y":12,"z":0}],
  "pull_groups":[{"entries":[7001],"positions":[{"x":48,"y":30,"z":0}]}],"boss":{"entry":6001,"position":{"x":48,"y":46,"z":0}}}]}`
	r := newEditRig(t, planner)
	must(t, os.WriteFile(filepath.Join(r.src, "edit1.json"), []byte(meshPlan), 0o644))
	sum := sha256.Sum256([]byte(meshPlan))
	r.base = hex.EncodeToString(sum[:])
	if got := r.post("edit1", r.edit(move(0, 1, 32, 13)), editToken); got.code != 200 {
		t.Fatalf("along the corridor: %d %s", got.code, got.body)
	}
	if got := r.post("edit1", r.edit(move(0, 1, 30, 40)), editToken); got.code != http.StatusBadRequest || !strings.Contains(got.body, "off the mesh") {
		t.Errorf("off the mesh: %d %s", got.code, got.body)
	}
	got := r.post("edit1", r.edit(move(0, 1, 62, 62)), editToken)
	var rep EditReply
	must(t, json.Unmarshal([]byte(got.body), &rep))
	if got.code != http.StatusUnprocessableEntity || rep.Status != "refused" || rep.Check.NewIssues == 0 {
		t.Fatalf("an island: %d %s", got.code, got.body)
	}
	hasReason := false
	for _, is := range rep.Check.Issues {
		hasReason = hasReason || (is.New && is.Reason != "")
	}
	if !hasReason {
		t.Errorf("the refusal names no reason: %+v", rep.Check.Issues)
	}
	if _, err := os.Stat(filepath.Join(r.ed.cfg.Store.Root(), "edit1", routeedit.Name(rep.Version)+".json")); err == nil {
		t.Error("a refused edit wrote a plan file")
	}
}

// "checked" means the edited plan has no plan check issue at all. An edit that adds none to a plan that has issues of its own is kept under
// another status, "unchecked", which the newest-checked lookup (export, promote) never picks.
func TestAnEditOfABrokenPlanIsKeptAsUncheckedNotChecked(t *testing.T) {
	r := newEditRig(t, testNav{holes: [][4]float32{{35, -5, 45, 5}}}) // swallows via 1 (40, 0): the plan fails there already
	got := r.post("edit1", r.edit(move(1, 0, 201, 1)), editToken)
	var rep EditReply
	must(t, json.Unmarshal([]byte(got.body), &rep))
	if got.code != http.StatusOK || rep.Status != "unchecked" || rep.Check.OK || !rep.Check.Accepted || rep.Check.NewIssues != 0 || len(rep.Check.Issues) == 0 || !strings.Contains(rep.Message, "own") {
		t.Fatalf("%d %s", got.code, got.body)
	}
	if v, _ := r.ed.cfg.Store.Latest("edit1"); v != 0 {
		t.Errorf("Latest picked version %d of a plan with issues", v)
	}
	if _, err := os.Stat(filepath.Join(r.ed.cfg.Store.Root(), "edit1", "v0001.json")); err != nil {
		t.Errorf("an unchecked edit keeps its plan, to go on from: %v", err)
	}
	m, _ := r.ed.cfg.Store.Meta("edit1", 1)
	if m.Status != "unchecked" {
		t.Errorf("status %q", m.Status)
	}
	// an edit that fixes the plan is checked
	got = r.post("edit1", r.edit(move(0, 1, 40, 8)), editToken)
	must(t, json.Unmarshal([]byte(got.body), &rep))
	if got.code != http.StatusOK || rep.Status != "checked" || !rep.Check.OK || len(rep.Check.Issues) != 0 {
		t.Fatalf("%d %s", got.code, got.body)
	}
	if v, _ := r.ed.cfg.Store.Latest("edit1"); v != 2 {
		t.Errorf("latest %d", v)
	}
}

// The slot-folder guard fails closed: a settings file that cannot be read or decoded, or a slot whose plan folder cannot be told, stops edits.
func TestSlotFoldersFailClosed(t *testing.T) {
	dir := t.TempDir()
	write := func(name, body string, mode os.FileMode) {
		must(t, os.WriteFile(filepath.Join(dir, name), []byte(body), mode))
	}
	abs := t.TempDir()
	write("settings-1.json", `{"rpc":"127.0.0.1:8765","dungeons_dir":`+string(mustJSON(t, abs))+`}`, 0o600)
	write("settings.json", `{"mmaps_dir":"/x"}`, 0o600) // no rpc: not an agent that can be commanded
	got, err := SlotFolders(dir, nil)
	if err != nil || len(got) != 1 || got[0] != abs {
		t.Fatalf("a good config: %v %v", got, err)
	}
	cases := map[string]struct{ name, body string }{
		"malformed":          {"settings-2.json", `{"rpc": `},
		"empty dungeons_dir": {"settings-2.json", `{"rpc":"127.0.0.1:8766"}`},
		"relative":           {"settings-2.json", `{"rpc":"127.0.0.1:8766","dungeons_dir":"dungeons"}`},
		"not an object":      {"settings-2.json", `[1,2]`},
	}
	for name, c := range cases {
		write(c.name, c.body, 0o600)
		if got, err := SlotFolders(dir, nil); err == nil || !strings.Contains(err.Error(), c.name) {
			t.Errorf("%s: %v %v", name, got, err)
		}
		os.Remove(filepath.Join(dir, c.name))
	}
	// unreadable
	write("settings-3.json", `{"rpc":"127.0.0.1:8767","dungeons_dir":"/x"}`, 0o000)
	if os.Getuid() != 0 {
		if _, err := SlotFolders(dir, nil); err == nil || !strings.Contains(err.Error(), "settings-3.json cannot be read") {
			t.Errorf("unreadable: %v", err)
		}
	}
	os.Remove(filepath.Join(dir, "settings-3.json"))
	// the operator vouches for a file with a default folder; the vouching is by file name and covers only that file
	write("settings-4.json", `{"rpc":"127.0.0.1:8768"}`, 0o600)
	if _, err := SlotFolders(dir, []string{"settings-5.json"}); err == nil {
		t.Error("vouched for another file")
	}
	if got, err := SlotFolders(dir, []string{"settings-4.json"}); err != nil || len(got) != 1 {
		t.Errorf("vouched: %v %v", got, err)
	}
	// ~ is the home folder; a missing config folder is an error
	write("settings-6.json", `{"rpc":"127.0.0.1:8769","dungeons_dir":"~/ta-x/dungeons"}`, 0o600)
	home, _ := os.UserHomeDir()
	if got, _ := SlotFolders(dir, []string{"settings-4.json"}); !containsStr(got, filepath.Join(home, "ta-x", "dungeons")) {
		t.Errorf("~ not expanded: %v", got)
	}
	if _, err := SlotFolders(filepath.Join(dir, "nope"), nil); err == nil {
		t.Error("a missing config folder")
	}
}

func containsStr(l []string, s string) bool {
	for _, x := range l {
		if x == s {
			return true
		}
	}
	return false
}

func TestEditsAreRefusedWhenASlotsFolderCannotBeTold(t *testing.T) {
	for name, body := range map[string]string{
		"malformed settings":    `{"rpc": `,
		"empty dungeons_dir":    `{"rpc":"127.0.0.1:8766"}`,
		"relative dungeons_dir": `{"rpc":"127.0.0.1:8766","dungeons_dir":"dungeons"}`,
	} {
		r := newEditRig(t, nil)
		must(t, os.WriteFile(filepath.Join(r.cfg, "settings-2.json"), []byte(body), 0o600))
		before := tree(t, filepath.Dir(r.edits))
		got := r.post("edit1", r.edit(move(0, 0, 21, 1)), editToken)
		if got.code != http.StatusServiceUnavailable || !strings.Contains(got.body, "settings-2.json") {
			t.Errorf("%s: %d %s", name, got.code, got.body)
		}
		if tree(t, filepath.Dir(r.edits)) != before {
			t.Errorf("%s: wrote", name)
		}
		// and the editor does not start on it
		tk, _ := routeedit.LoadTokens(r.tokens)
		store, _ := routeedit.NewStore(filepath.Join(t.TempDir(), "e"))
		if _, err := NewEditor(r.p, EditConfig{Source: r.src, Store: store, Tokens: tk, Nav: testNav{}, Mmaps: "/m"}); err == nil {
			t.Errorf("%s: started", name)
		}
		// vouching for that file lets it go on
		if name == "empty dungeons_dir" {
			if _, err := NewEditor(r.p, EditConfig{Source: r.src, Store: store, Tokens: tk, Nav: testNav{}, Mmaps: "/m", AllowDefaultDir: []string{"settings-2.json"}}); err != nil {
				t.Errorf("vouched: %v", err)
			}
		}
	}
	// agents given by address have no settings file
	r := newEditRig(t, nil)
	r.p.opt.Agents = "auto,8770"
	if got := r.post("edit1", r.edit(move(0, 0, 21, 1)), editToken); got.code != http.StatusServiceUnavailable || !strings.Contains(got.body, "--agents auto") {
		t.Errorf("explicit agents: %d %s", got.code, got.body)
	}
}

// The vouching (--edit-allow-default-dir) covers a settings file with NO dungeons_dir, never a relative one, at start and at every edit.
func TestVouchingNeverCoversARelativeDungeonsDir(t *testing.T) {
	dir := t.TempDir()
	write := func(body string) { must(t, os.WriteFile(filepath.Join(dir, "settings-3.json"), []byte(body), 0o600)) }
	vouch := []string{"settings-3.json"}
	for _, rel := range []string{"dungeons", "./dungeons", "../x/dungeons", "ta/dungeons"} {
		write(`{"rpc":"127.0.0.1:8767","dungeons_dir":` + string(mustJSON(t, rel)) + `}`)
		if got, err := SlotFolders(dir, vouch); err == nil || !strings.Contains(err.Error(), "relative") {
			t.Errorf("%q vouched: %v %v", rel, got, err)
		}
	}
	write(`{"rpc":"127.0.0.1:8767"}`)
	if _, err := SlotFolders(dir, vouch); err != nil {
		t.Errorf("an empty dungeons_dir, vouched: %v", err)
	}

	// at start: the editor does not come up on a vouched file with a relative dir
	r := newEditRig(t, nil)
	tk, _ := routeedit.LoadTokens(r.tokens)
	store, _ := routeedit.NewStore(filepath.Join(t.TempDir(), "e"))
	cfg := EditConfig{Source: r.src, Store: store, Tokens: tk, Nav: testNav{}, Mmaps: "/m", AllowDefaultDir: vouch}
	settings := filepath.Join(r.cfg, "settings-3.json")
	must(t, os.WriteFile(settings, []byte(`{"rpc":"127.0.0.1:8767","dungeons_dir":"dungeons"}`), 0o600))
	if _, err := NewEditor(r.p, cfg); err == nil {
		t.Fatal("started with a vouched file whose dungeons_dir is relative")
	}
	// the vouched file with no dungeons_dir starts; then it changes to a relative dir: the next edit is refused and nothing is written
	must(t, os.WriteFile(settings, []byte(`{"rpc":"127.0.0.1:8767"}`), 0o600))
	ed, err := NewEditor(r.p, cfg)
	if err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(HandlerWithEditor(r.p, "", nil, nil, ed))
	defer srv.Close()
	r.srv = srv
	r.ed = ed
	if got := r.post("edit1", r.edit(move(0, 0, 21, 1)), editToken); got.code != http.StatusOK {
		t.Fatalf("before the change: %d %s", got.code, got.body)
	}
	before := tree(t, store.Root())
	must(t, os.WriteFile(settings, []byte(`{"rpc":"127.0.0.1:8767","dungeons_dir":"dungeons"}`), 0o600))
	got := r.post("edit1", r.edit(move(0, 0, 22, 1)), editToken)
	if got.code != http.StatusServiceUnavailable || !strings.Contains(got.body, "relative") {
		t.Errorf("after the change: %d %s", got.code, got.body)
	}
	if tree(t, store.Root()) != before {
		t.Error("an edit was written after the slot's folder became unknown")
	}
}

// A plan file, or a folder on the way to the source, swapped for a link to a folder outside after the editor checked its folders and
// before it reads the plan: the read stays in the held folder or is refused, and nothing is written.
func TestSourceSwappedForALinkBetweenCheckAndRead(t *testing.T) {
	outside := t.TempDir()
	must(t, os.MkdirAll(filepath.Join(outside, "source"), 0o755))
	must(t, os.WriteFile(filepath.Join(outside, "source", "edit1.json"), []byte(editPlan), 0o644)) // same bytes: only the place differs

	t.Run("plan file", func(t *testing.T) {
		r := newEditRig(t, nil)
		r.ed.afterCheck = func() {
			must(t, os.Rename(filepath.Join(r.src, "edit1.json"), filepath.Join(r.src, "edit1.json.old")))
			must(t, os.Symlink(filepath.Join(outside, "source", "edit1.json"), filepath.Join(r.src, "edit1.json")))
		}
		before := tree(t, r.edits)
		got := r.post("edit1", r.edit(move(0, 0, 21, 1)), editToken)
		if got.code == http.StatusOK || !strings.Contains(got.body, "link") {
			t.Errorf("a plan file swapped for a link was read: %d %s", got.code, got.body)
		}
		if tree(t, r.edits) != before {
			t.Error("wrote")
		}
	})

	t.Run("folder on the way", func(t *testing.T) {
		r := newEditRig(t, nil)
		root := t.TempDir()
		mid := filepath.Join(root, "mid")
		src := filepath.Join(mid, "source")
		must(t, os.MkdirAll(src, 0o755))
		must(t, os.WriteFile(filepath.Join(src, "edit1.json"), []byte(editPlan), 0o644))
		tk, _ := routeedit.LoadTokens(r.tokens)
		ed, err := NewEditor(r.p, EditConfig{Source: src, Store: r.ed.cfg.Store, Tokens: tk, Nav: testNav{}, Mmaps: "/m"})
		must(t, err)
		t.Cleanup(ed.Close)
		srv := httptest.NewServer(HandlerWithEditor(r.p, "", nil, nil, ed))
		t.Cleanup(srv.Close)
		r.srv, r.ed = srv, ed
		swapped := false
		ed.afterCheck = func() {
			if swapped {
				return
			}
			swapped = true
			must(t, os.Rename(mid, filepath.Join(root, "mid.old")))
			must(t, os.Symlink(outside, mid))
		}
		before := tree(t, r.edits)
		got := r.post("edit1", r.edit(move(0, 0, 21, 1)), editToken)
		if got.code == http.StatusOK || !strings.Contains(got.body, "no longer the folder") {
			t.Errorf("a source reached through a swapped folder was used: %d %s", got.code, got.body)
		}
		if tree(t, r.edits) != before {
			t.Error("wrote")
		}
		// and it stays refused: the folder checks see the swap too
		if got := r.post("edit1", r.edit(move(0, 0, 21, 1)), editToken); got.code != http.StatusServiceUnavailable {
			t.Errorf("after the swap: %d %s", got.code, got.body)
		}
	})
}

// rigWith makes a rig whose editor has the given bounds.
func rigWith(t *testing.T, set func(*EditConfig)) *editRig {
	r := newEditRig(t, nil)
	tk, _ := routeedit.LoadTokens(r.tokens)
	cfg := EditConfig{Source: r.src, Store: r.ed.cfg.Store, Tokens: tk, Nav: testNav{}, Mmaps: "/m"}
	set(&cfg)
	ed, err := NewEditor(r.p, cfg)
	must(t, err)
	t.Cleanup(ed.Close)
	srv := httptest.NewServer(HandlerWithEditor(r.p, "", nil, nil, ed))
	t.Cleanup(srv.Close)
	r.srv, r.ed = srv, ed
	return r
}

// The same base and ops as the latest version return that version: no new number, nothing written.
func TestResubmitReturnsTheLatestVersion(t *testing.T) {
	r := newEditRig(t, nil)
	first := r.post("edit1", r.edit(move(0, 0, 21, 1)), editToken)
	if first.code != http.StatusOK {
		t.Fatalf("%d %s", first.code, first.body)
	}
	before := tree(t, r.edits)
	again := r.post("edit1", r.edit(move(0, 0, 21, 1)), editToken)
	var rep EditReply
	must(t, json.Unmarshal([]byte(again.body), &rep))
	if again.code != http.StatusOK || !rep.Unchanged || rep.Version != 1 || rep.Status != routeedit.StatusChecked || rep.Diff == "" {
		t.Errorf("resubmit: %d %s", again.code, again.body)
	}
	if tree(t, r.edits) != before {
		t.Error("a resubmit wrote")
	}
	// other ops are a new version
	if got := r.post("edit1", r.edit(move(0, 0, 22, 1)), editToken); got.code != http.StatusOK || strings.Contains(got.body, `"unchanged"`) || !strings.Contains(got.body, `"version":2`) {
		t.Errorf("new ops: %d %s", got.code, got.body)
	}
}

func TestSavesAreBounded(t *testing.T) {
	t.Run("per token per minute", func(t *testing.T) {
		now := time.Date(2026, 10, 16, 12, 0, 0, 0, time.UTC)
		r := rigWith(t, func(c *EditConfig) { c.MaxPerMinute = 3; c.Now = func() time.Time { return now } })
		for i := 0; i < 3; i++ {
			if got := r.post("edit1", r.edit(move(0, 0, float64(21+i), 1)), editToken); got.code != http.StatusOK {
				t.Fatalf("save %d: %d %s", i, got.code, got.body)
			}
		}
		before := tree(t, r.edits)
		if got := r.post("edit1", r.edit(move(0, 0, 30, 1)), editToken); got.code != http.StatusTooManyRequests {
			t.Errorf("over the minute: %d %s", got.code, got.body)
		}
		if tree(t, r.edits) != before {
			t.Error("wrote over the bound")
		}
		now = now.Add(61 * time.Second)
		if got := r.post("edit1", r.edit(move(0, 0, 30, 1)), editToken); got.code != http.StatusOK {
			t.Errorf("a minute later: %d %s", got.code, got.body)
		}
	})
	t.Run("per plan per day", func(t *testing.T) {
		now := time.Date(2026, 10, 16, 12, 0, 0, 0, time.UTC)
		r := rigWith(t, func(c *EditConfig) { c.MaxPerDay = 2; c.Now = func() time.Time { return now } })
		for i := 0; i < 2; i++ {
			now = now.Add(2 * time.Minute)
			if got := r.post("edit1", r.edit(move(0, 0, float64(21+i), 1)), editToken); got.code != http.StatusOK {
				t.Fatalf("save %d: %d %s", i, got.code, got.body)
			}
		}
		now = now.Add(2 * time.Minute)
		if got := r.post("edit1", r.edit(move(0, 0, 30, 1)), editToken); got.code != http.StatusTooManyRequests || !strings.Contains(got.body, "24 hours") {
			t.Errorf("over the day: %d %s", got.code, got.body)
		}
		now = now.Add(24 * time.Hour)
		if got := r.post("edit1", r.edit(move(0, 0, 30, 1)), editToken); got.code != http.StatusOK {
			t.Errorf("a day later: %d %s", got.code, got.body)
		}
	})
	t.Run("disk floor", func(t *testing.T) {
		r := rigWith(t, func(c *EditConfig) { c.MinFree = 1 << 62 })
		before := tree(t, r.edits)
		if got := r.post("edit1", r.edit(move(0, 0, 21, 1)), editToken); got.code != http.StatusInsufficientStorage {
			t.Errorf("under the floor: %d %s", got.code, got.body)
		}
		if tree(t, r.edits) != before {
			t.Error("wrote under the floor")
		}
	})
}
