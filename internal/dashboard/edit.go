package dashboard

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime"
	"net"
	"net/http"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode"

	"crypto/sha256"
	"encoding/hex"

	"github.com/pjerra/turtle-agent/internal/dungeon"
	"github.com/pjerra/turtle-agent/internal/routeedit"
)

// The route editor's write side (README "Route editor"). It is off unless the dashboard is started with --edit; this file is all of it that is
// reachable from the network, so it is kept small:
//   - the guard (server.go) lets only POST /api/edit/* through besides GET and HEAD, and only with a valid Bearer token (authorize);
//   - a request carries ops, never a plan: routeedit.Apply changes nothing but the position of a via point or a leg's `to`, adds a plain via
//     point or deletes one, on a copy of the base plan the server reads itself;
//   - the edit is checked on the navmesh (routeedit.Evaluate), and written as a new numbered version into --edit-dir with names the server
//     makes (routeedit.Store); nothing else is written, and a slot's plan folder is never touched.

// maxEditBody bounds the body of an edit request.
const maxEditBody = 64 << 10

// maxBaseFile bounds the plan file read as the base of an edit.
const maxBaseFile = 4 << 20

// maxSnapPrior bounds the ops a snap request carries as the edits before the one tested.
const maxSnapPrior = 24 << 10

// maxDiffReturned bounds the text diff sent back in a reply (the whole diff is in the store).
const maxDiffReturned = 48 << 10

// Nav is what the editor asks of the navmesh: the plan check's questions and the floors under a point. navmesh.Planner is one.
type Nav interface {
	dungeon.Navmesh
	routeedit.Floorer
}

// EditConfig is the editor's setup.
type EditConfig struct {
	Source string // the folder whose plan files are the base of an edit
	Store  *routeedit.Store
	Tokens *routeedit.Tokens
	Nav    Nav
	Mmaps  string // the navmesh folder Nav reads, recorded with every version
	// AllowDefaultDir names settings files (by file name) with an rpc but no dungeons_dir that the operator vouches read a default plan folder
	// elsewhere than the edits folder; every other such file stops the editor (SlotFolders).
	AllowDefaultDir []string
	Now             func() time.Time
}

// Editor serves /api/edit/*.
type Editor struct {
	cfg EditConfig
	p   *Poller
	mu  sync.Mutex // one edit at a time: a plan check takes a moment and versions are counted

	gitMu  sync.Mutex
	gitAt  time.Time
	gitSHA string
}

// NewEditor makes the editor. It refuses a setup whose folders touch a slot's plan folder (see routeedit.CheckFolders); the check is made
// again at every edit, since agents appear later.
func NewEditor(p *Poller, cfg EditConfig) (*Editor, error) {
	if cfg.Store == nil || cfg.Tokens == nil || cfg.Nav == nil || cfg.Source == "" || cfg.Mmaps == "" {
		return nil, errors.New("editor: source, store, tokens, navmesh and mmaps folder are all needed")
	}
	if cfg.Now == nil {
		cfg.Now = time.Now
	}
	if err := routeedit.CheckSource(cfg.Source); err != nil {
		return nil, err
	}
	e := &Editor{cfg: cfg, p: p}
	if err := e.checkFolders(); err != nil {
		return nil, err
	}
	return e, nil
}

func (e *Editor) checkFolders() error {
	if a := strings.TrimSpace(e.p.opt.Agents); a != "auto" {
		return errors.New("--edit needs --agents auto: an agent given by address has no settings file, so the folder its runs read is unknown")
	}
	dirs, err := SlotFolders(e.p.opt.ConfigDir, e.cfg.AllowDefaultDir)
	if err != nil {
		return fmt.Errorf("the slots' plan folders cannot all be told, so the edits folder cannot be checked against them: %w", err)
	}
	return routeedit.CheckFolders(e.cfg.Store.Root(), e.cfg.Source, dirs)
}

type whoKey struct{}

// authorize is the guard's gate for an edit request: a valid Bearer token, a JSON body of at most maxEditBody bytes. On failure it has
// answered and returns false. The label of the token is put in the request's context.
func (e *Editor) authorize(w http.ResponseWriter, r *http.Request) (*http.Request, bool) {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		host = r.RemoteAddr
	}
	label, err := e.cfg.Tokens.Authenticate(r.Header.Get("Authorization"), host, e.cfg.Now())
	switch {
	case errors.Is(err, routeedit.ErrTooMany):
		http.Error(w, err.Error(), http.StatusTooManyRequests)
		return r, false
	case err != nil:
		w.Header().Set("WWW-Authenticate", `Bearer realm="route editor"`)
		http.Error(w, "an edit needs the editing token (Authorization: Bearer ...)", http.StatusUnauthorized)
		return r, false
	}
	if mt, _, err := mime.ParseMediaType(r.Header.Get("Content-Type")); err != nil || mt != "application/json" {
		http.Error(w, "Content-Type must be application/json", http.StatusUnsupportedMediaType)
		return r, false
	}
	r.Body = http.MaxBytesReader(w, r.Body, maxEditBody)
	return r.WithContext(context.WithValue(r.Context(), whoKey{}, label)), true
}

// routes registers the editor's paths under /api/edit/.
func (e *Editor) routes(mux *http.ServeMux) {
	mux.HandleFunc("/api/edit/", e.serve)
}

func jsonError(w http.ResponseWriter, code int, format string, a ...any) {
	b, _ := json.Marshal(map[string]any{"status": "error", "message": fmt.Sprintf(format, a...)})
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(code)
	_, _ = w.Write(b)
}

func writeAny(w http.ResponseWriter, code int, v any) {
	b, err := json.Marshal(v)
	if err != nil {
		jsonError(w, http.StatusInternalServerError, "%v", err)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(code)
	_, _ = w.Write(b)
}

// serve routes /api/edit/<rest>: GET status, <plan>/versions, <plan>/snap, <plan>/vNNNN, <plan>/vNNNN/diff, and POST <plan>.
func (e *Editor) serve(w http.ResponseWriter, r *http.Request) {
	seg := strings.Split(strings.TrimPrefix(r.URL.Path, "/api/edit/"), "/")
	switch {
	case r.Method == http.MethodPost && len(seg) == 1 && routeedit.PlanName.MatchString(seg[0]):
		e.submit(w, r, seg[0])
	case r.Method == http.MethodPost:
		jsonError(w, http.StatusNotFound, "no such edit path")
	case len(seg) == 1 && seg[0] == "status":
		writeAny(w, 200, map[string]any{"enabled": true, "source": e.cfg.Source, "edit_dir": e.cfg.Store.Root(), "mmaps": e.cfg.Mmaps})
	case len(seg) == 2 && routeedit.PlanName.MatchString(seg[0]) && seg[1] == "versions":
		e.versions(w, seg[0])
	case len(seg) == 2 && routeedit.PlanName.MatchString(seg[0]) && seg[1] == "snap":
		e.snap(w, r, seg[0])
	case len(seg) >= 2 && len(seg) <= 3 && routeedit.PlanName.MatchString(seg[0]):
		v, ok := routeedit.ParseVersion(seg[1])
		if !ok || (len(seg) == 3 && seg[2] != "diff") {
			jsonError(w, http.StatusNotFound, "no such version")
			return
		}
		if len(seg) == 3 {
			d, err := e.cfg.Store.Diff(seg[0], v)
			if err != nil {
				jsonError(w, http.StatusNotFound, "no such version")
				return
			}
			w.Header().Set("Content-Type", "text/plain; charset=utf-8")
			w.Header().Set("Cache-Control", "no-store")
			_, _ = io.WriteString(w, d)
			return
		}
		m, err := e.cfg.Store.Meta(seg[0], v)
		if err != nil {
			jsonError(w, http.StatusNotFound, "no such version")
			return
		}
		writeAny(w, 200, m)
	default:
		jsonError(w, http.StatusNotFound, "no such edit path")
	}
}

// baseOf reads the base plan of an edit: the file in the source folder (no link out of it), its bytes' sha256 and the parsed plan.
func (e *Editor) baseOf(plan string) (*dungeon.Plan, routeedit.BaseInfo, error) {
	full, err := SafeJoin(e.cfg.Source, plan+".json")
	if err != nil {
		return nil, routeedit.BaseInfo{}, fmt.Errorf("no plan file %s.json in the editable source", plan)
	}
	st, err := os.Stat(full)
	if err != nil || st.Size() > maxBaseFile {
		return nil, routeedit.BaseInfo{}, fmt.Errorf("the plan file %s.json cannot be read", plan)
	}
	data, err := os.ReadFile(full)
	if err != nil {
		return nil, routeedit.BaseInfo{}, err
	}
	p, err := dungeon.Parse(full, data)
	if err != nil {
		return nil, routeedit.BaseInfo{}, fmt.Errorf("the plan file does not load: %s", firstLine(err.Error()))
	}
	sum := sha256.Sum256(data)
	return p, routeedit.BaseInfo{Source: full, SHA256: hex.EncodeToString(sum[:]), GitSHA: e.gitHead()}, nil
}

// gitHead is HEAD of the repo the source folder is in, "" when it is in none; asked at most every 30 s.
func (e *Editor) gitHead() string {
	e.gitMu.Lock()
	defer e.gitMu.Unlock()
	if e.cfg.Now().Sub(e.gitAt) < 30*time.Second {
		return e.gitSHA
	}
	e.gitAt, e.gitSHA = e.cfg.Now(), gitHeadOf(e.cfg.Source)
	return e.gitSHA
}

// gitHeadOf runs git on the operator's own --edit-source folder.
func gitHeadOf(dir string) string {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	out, err := exec.CommandContext(ctx, "git", "-C", dir, "rev-parse", "HEAD").Output()
	if err != nil {
		return ""
	}
	s := strings.TrimSpace(string(out))
	if len(s) < 7 || len(s) > 64 || strings.Trim(s, "0123456789abcdef") != "" {
		return ""
	}
	return s
}

func (e *Editor) versions(w http.ResponseWriter, plan string) {
	ms, err := e.cfg.Store.List(plan)
	if err != nil {
		jsonError(w, http.StatusInternalServerError, "%v", err)
		return
	}
	out := map[string]any{"plan": plan, "versions": ms}
	if _, base, err := e.baseOf(plan); err == nil {
		out["base"] = base
	} else {
		out["base_error"] = err.Error()
	}
	writeAny(w, 200, out)
}

// snap tries one op on the base plan and says where the point would land: the floor picked, and whether there was a choice. It writes nothing.
func (e *Editor) snap(w http.ResponseWriter, r *http.Request, plan string) {
	q := r.URL.Query()
	num := func(k string) (float64, bool) {
		f, err := strconv.ParseFloat(q.Get(k), 64)
		return f, err == nil
	}
	leg, ok1 := num("leg")
	idx, ok2 := num("index")
	x, ok3 := num("x")
	y, ok4 := num("y")
	kind := q.Get("kind")
	if kind == "" {
		kind = routeedit.KindMove
	}
	if !ok1 || !ok2 || !ok3 || !ok4 || kind == routeedit.KindDelete {
		jsonError(w, http.StatusBadRequest, "snap takes kind (move or insert), leg, index, x and y")
		return
	}
	base, _, err := e.baseOf(plan)
	if err != nil {
		jsonError(w, http.StatusNotFound, "%v", err)
		return
	}
	// the ops already made come first (prior): an op names a place in the leg as the ops before it left it
	var ops []routeedit.Op
	if prior := q.Get("prior"); prior != "" {
		if len(prior) > maxSnapPrior || json.Unmarshal([]byte(prior), &ops) != nil || len(ops) >= routeedit.MaxOps {
			jsonError(w, http.StatusBadRequest, "prior must be a JSON list of at most %d ops in at most %d bytes", routeedit.MaxOps-1, maxSnapPrior)
			return
		}
	}
	ops = append(ops, routeedit.Op{Leg: int(leg), Kind: kind, Index: int(idx), X: x, Y: y})
	_, ch, err := routeedit.Apply(base, ops, e.cfg.Nav)
	if err != nil {
		writeAny(w, 200, map[string]any{"ok": false, "message": err.Error()})
		return
	}
	writeAny(w, 200, map[string]any{"ok": true, "change": ch[len(ch)-1]})
}

// EditRequest is the body of POST /api/edit/<plan>.
type EditRequest struct {
	BaseSHA256 string         `json:"base_sha256"`
	Note       string         `json:"note"`
	Ops        []routeedit.Op `json:"ops"`
}

// EditReply is the answer to an edit: the version written (checked, unchecked, or a refused record) and why.
type EditReply struct {
	Status  string                `json:"status"` // checked, unchecked or refused
	Version int                   `json:"version"`
	Name    string                `json:"name"`
	Message string                `json:"message,omitempty"`
	Who     string                `json:"who"`
	Check   routeedit.CheckResult `json:"check"`
	Changes []routeedit.Change    `json:"changes"`
	Diff    string                `json:"diff"`
}

// cleanNote keeps the owner's note short and printable.
func cleanNote(s string) string {
	var b strings.Builder
	n := 0
	for _, r := range s {
		if n >= 500 {
			break
		}
		if unicode.IsControl(r) {
			r = ' '
		}
		b.WriteRune(r)
		n++
	}
	return strings.TrimSpace(b.String())
}

func (e *Editor) submit(w http.ResponseWriter, r *http.Request, plan string) {
	who, _ := r.Context().Value(whoKey{}).(string)
	var req EditRequest
	dec := json.NewDecoder(r.Body)
	dec.DisallowUnknownFields()
	if err := dec.Decode(&req); err != nil {
		var tooBig *http.MaxBytesError
		if errors.As(err, &tooBig) {
			jsonError(w, http.StatusRequestEntityTooLarge, "the request is larger than %d bytes", maxEditBody)
			return
		}
		jsonError(w, http.StatusBadRequest, "the request is not the expected JSON: %v", err)
		return
	}
	if dec.More() {
		jsonError(w, http.StatusBadRequest, "one JSON object, nothing after it")
		return
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	if err := e.checkFolders(); err != nil {
		jsonError(w, http.StatusServiceUnavailable, "%v", err)
		return
	}
	base, info, err := e.baseOf(plan)
	if err != nil {
		jsonError(w, http.StatusNotFound, "%v", err)
		return
	}
	if req.BaseSHA256 == "" {
		jsonError(w, http.StatusBadRequest, "base_sha256 is needed: the sha256 of the plan file the edit started from")
		return
	}
	if req.BaseSHA256 != info.SHA256 {
		jsonError(w, http.StatusConflict, "the plan changed under you (the file is %s, the edit started from %s): reload the plan and edit again", info.SHA256[:8], shortSHA(req.BaseSHA256))
		return
	}
	edited, changes, err := routeedit.Apply(base, req.Ops, e.cfg.Nav)
	if err != nil {
		jsonError(w, http.StatusBadRequest, "%v", err)
		return
	}
	baseJSON, _ := dungeon.Marshal(base)
	editedJSON, err := dungeon.Marshal(edited)
	if err != nil {
		jsonError(w, http.StatusInternalServerError, "%v", err)
		return
	}
	diff := routeedit.UnifiedDiff(plan+".json (base "+info.SHA256[:8]+")", plan+".json (edit)", string(baseJSON), string(editedJSON))
	if diff == "" {
		jsonError(w, http.StatusBadRequest, "the ops change nothing")
		return
	}
	check := routeedit.Evaluate(base, edited, e.cfg.Nav)
	meta := &routeedit.Meta{Who: who, At: e.cfg.Now().UTC().Format(time.RFC3339), Note: cleanNote(req.Note), Base: info, Mmaps: e.cfg.Mmaps,
		Check: check, Ops: req.Ops, Changes: changes, PlanDiff: dungeon.DiffPlans(base, edited)}
	reply := EditReply{Who: who, Check: check, Changes: changes, Diff: diff}
	if len(reply.Diff) > maxDiffReturned {
		reply.Diff = reply.Diff[:maxDiffReturned] + "\n... (cut: the whole diff is in the version's .diff file)\n"
	}
	code := http.StatusOK
	var planJSON []byte
	switch {
	case !check.Checked || !check.Accepted:
		meta.Status, reply.Status, reply.Message = routeedit.StatusRefused, routeedit.StatusRefused, check.Refusal()
		code = http.StatusUnprocessableEntity
	case check.OK:
		meta.Status, reply.Status, planJSON = routeedit.StatusChecked, routeedit.StatusChecked, editedJSON
	default: // no new issue, but the plan had issues of its own: kept, never "checked"
		meta.Status, reply.Status, reply.Message, planJSON = routeedit.StatusUnchecked, routeedit.StatusUnchecked, check.Unchecked(), editedJSON
	}
	v, err := e.cfg.Store.Save(plan, meta, planJSON, diff)
	if err != nil {
		jsonError(w, http.StatusInternalServerError, "the version could not be written: %v", err)
		return
	}
	reply.Version, reply.Name = v, routeedit.Name(v)
	writeAny(w, code, reply)
}
