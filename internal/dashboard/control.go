package dashboard

import (
	"context"
	"encoding/json"
	"errors"
	"mime"
	"net"
	"net/http"
	"net/netip"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/pjerra/turtle-agent/internal/netutil"
	"github.com/pjerra/turtle-agent/internal/routeedit"
)

// The run controls (README "Controls (--control)", issue #183). Off unless the dashboard is started with --control; then, and only then, does
// the guard let POST /api/control/* through, and only with a control token (never the route-edit one) or, when --control-trust-local is on, from
// a trusted peer. Every control first asks the agent a fresh dungeon_status, a live check; the page is a convenience, the server is the gate.
//
// Slice 1 of #183 has the run controls only (pause, resume, skip, stop). The live tuning and the party orders are added later: a new handler in
// c.actions is the whole of it (control_tune.go, control_order.go).

// maxControlBody bounds the body of a control request (D5).
const maxControlBody = 4 << 10

// controlTimeout bounds one control's RPC. dungeon_pause and dungeon_stop wait up to 10 s for a run to end, so the bound leaves room for them.
const controlTimeout = 30 * time.Second

// ControlConfig is the controls' setup.
type ControlConfig struct {
	Tokens       *routeedit.Tokens // the control token file (`label token`), never the route-editor one
	LogPath      string            // where the control log is appended (its folder is made 0700, the file 0600)
	Now          func() time.Time
	MaxPerMinute int // controls per token label per minute (0 = 30)
}

// Controller serves /api/control/*.
type Controller struct {
	p       *Poller
	cfg     ControlConfig
	log     *controlLog
	trust   *localTrust // --control-trust-local; nil: every control needs a token
	actions map[string]actionFunc

	mu     sync.Mutex
	recent map[string][]time.Time // under mu: the times of each label's controls in the last minute
	locks  map[string]*sync.Mutex // under mu: one control at a time per agent
}

// actionFunc handles one control action (run, and later tune and order).
type actionFunc func(ac *actionContext)

// actionContext is what a handler needs: the reply writer, the request, who sent it and which agent it is for.
type actionContext struct {
	w      http.ResponseWriter
	r      *http.Request
	who    string // the token label (or the trusted label)
	via    string // token or trusted
	peer   string // the caller's address, host only
	agent  Agent
	action string
}

// NewController makes the controller, opening the log (fail closed: a log that cannot be opened is an error).
func NewController(p *Poller, cfg ControlConfig) (*Controller, error) {
	if cfg.Tokens == nil {
		return nil, errors.New("controls: the control token file is needed")
	}
	if cfg.Now == nil {
		cfg.Now = time.Now
	}
	if cfg.MaxPerMinute <= 0 {
		cfg.MaxPerMinute = defaultMaxPerMinute
	}
	log, err := openControlLog(cfg.LogPath)
	if err != nil {
		return nil, err
	}
	c := &Controller{p: p, cfg: cfg, log: log, recent: map[string][]time.Time{}, locks: map[string]*sync.Mutex{}}
	c.actions = map[string]actionFunc{"run": c.run, "tune": c.tune}
	return c, nil
}

// Close closes the log file.
func (c *Controller) Close() { c.log.Close() }

// routes registers the controls' paths under /api/control/.
func (c *Controller) routes(mux *http.ServeMux) {
	mux.HandleFunc("/api/control/", c.serve)
}

// authorize is the gate for a control request: a control token (or a trusted peer under --control-trust-local), a same-origin request and a
// small JSON body. On failure it has answered and returns false. The label and how it was trusted are put in the request's context.
func (c *Controller) authorize(w http.ResponseWriter, r *http.Request) (*http.Request, bool) {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		host = r.RemoteAddr
	}
	var label, via string
	if len(r.Header.Values("Authorization")) == 0 && c.trust != nil && c.trust.withNonce(r, controlNonceHeader) {
		label, via = c.trust.label, "trusted" // a request that sends any token is judged by it alone
	} else {
		via = "token"
		label, err = c.cfg.Tokens.Authenticate(r.Header.Get("Authorization"), host, c.cfg.Now())
	}
	switch {
	case errors.Is(err, routeedit.ErrTooMany):
		jsonError(w, http.StatusTooManyRequests, "%s", err.Error())
		return r, false
	case err != nil:
		w.Header().Set("WWW-Authenticate", `Bearer realm="dashboard controls"`)
		jsonError(w, http.StatusUnauthorized, "a control needs the control token (Authorization: Bearer ...)")
		return r, false
	}
	if !csrfOK(r) {
		jsonError(w, http.StatusForbidden, "a control must be same-origin: Origin, if present, must name the Host, and Sec-Fetch-Site must be same-origin or none")
		return r, false
	}
	if mt, _, err := mime.ParseMediaType(r.Header.Get("Content-Type")); err != nil || mt != "application/json" {
		jsonError(w, http.StatusUnsupportedMediaType, "Content-Type must be application/json")
		return r, false
	}
	r.Body = http.MaxBytesReader(w, r.Body, maxControlBody)
	ctx := context.WithValue(r.Context(), whoKey{}, label)
	ctx = context.WithValue(ctx, viaKey{}, via)
	return r.WithContext(ctx), true
}

// viaKey is how a control was authorized (token or trusted), for the log.
type viaKey struct{}

func (c *Controller) serve(w http.ResponseWriter, r *http.Request) {
	seg := strings.Split(strings.TrimPrefix(r.URL.Path, "/api/control/"), "/")
	switch {
	case r.Method == http.MethodGet && len(seg) == 1 && seg[0] == "status":
		c.status(w, r)
	case r.Method == http.MethodGet && len(seg) == 2 && seg[1] == "log":
		c.serveLog(w, r, seg[0])
	case r.Method == http.MethodPost && len(seg) == 2:
		c.control(w, r, seg[0], seg[1])
	default:
		jsonError(w, http.StatusNotFound, "no such control path")
	}
}

// status answers GET /api/control/status: the page shows its controls only when this says enabled.
func (c *Controller) status(w http.ResponseWriter, r *http.Request) {
	acts := make([]string, 0, len(c.actions))
	for k := range c.actions {
		acts = append(acts, k)
	}
	sort.Strings(acts)
	st := map[string]any{"enabled": true, "local_trusted": false, "actions": acts}
	if c.trust != nil && c.trust.request(r) {
		st["local_trusted"] = true
		st["trust_nonce"] = c.trust.nonce // the page's capability; not a token, and useless without a trusted peer
	}
	writeAny(w, http.StatusOK, st)
}

// serveLog answers GET /api/control/<id>/log?n=20: the last result lines of that agent, newest last.
func (c *Controller) serveLog(w http.ResponseWriter, r *http.Request, id string) {
	if !validAgentID(id) {
		jsonError(w, http.StatusNotFound, "no such agent")
		return
	}
	n := 20
	if s := r.URL.Query().Get("n"); s != "" {
		if v, err := strconv.Atoi(s); err == nil {
			n = v
		}
	}
	if n < 1 {
		n = 1
	}
	if n > controlMaxLines {
		n = controlMaxLines
	}
	lines, err := c.log.tail(id, n)
	if err != nil {
		jsonError(w, http.StatusInternalServerError, "the control log cannot be read: %v", err)
		return
	}
	if lines == nil {
		lines = []controlRecord{}
	}
	writeAny(w, http.StatusOK, map[string]any{"lines": lines})
}

// control is the shared part of every POST: the action must exist, the agent must be one the dashboard watches (D6), one control per agent
// at a time (D11) and at most MaxPerMinute per label per minute.
func (c *Controller) control(w http.ResponseWriter, r *http.Request, id, action string) {
	fn := c.actions[action]
	if fn == nil {
		jsonError(w, http.StatusNotFound, "no such control action")
		return
	}
	if !validAgentID(id) {
		jsonError(w, http.StatusNotFound, "no such agent")
		return
	}
	agent, ok := c.p.agentByID(id)
	if !ok {
		jsonError(w, http.StatusNotFound, "no such agent")
		return
	}
	who, _ := r.Context().Value(whoKey{}).(string)
	via, _ := r.Context().Value(viaKey{}).(string)
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		host = r.RemoteAddr
	}
	l := c.agentLock(id)
	if !l.TryLock() {
		jsonError(w, http.StatusConflict, "another control for this agent is still running")
		return
	}
	defer l.Unlock()
	if !c.allow(who) {
		jsonError(w, http.StatusTooManyRequests, "at most %d controls a minute per token: wait a moment", c.cfg.MaxPerMinute)
		return
	}
	fn(&actionContext{w: w, r: r, who: who, via: via, peer: host, agent: agent, action: action})
}

func (c *Controller) agentLock(id string) *sync.Mutex {
	c.mu.Lock()
	defer c.mu.Unlock()
	l := c.locks[id]
	if l == nil {
		l = &sync.Mutex{}
		c.locks[id] = l
	}
	return l
}

// allow counts one control against the label's per-minute bound. It reports false (429) when the bound is reached.
func (c *Controller) allow(who string) bool {
	now := c.cfg.Now()
	c.mu.Lock()
	defer c.mu.Unlock()
	var kept []time.Time
	for _, t := range c.recent[who] {
		if now.Sub(t) < time.Minute {
			kept = append(kept, t)
		}
	}
	if len(kept) >= c.cfg.MaxPerMinute {
		c.recent[who] = kept
		return false
	}
	c.recent[who] = append(kept, now)
	return true
}

// runRequest is the body of POST /api/control/<id>/run (D15).
type runRequest struct {
	Verb    string `json:"verb"`
	Confirm bool   `json:"confirm"`
}

// runVerbs maps a run verb to its RPC method. The page may send these and nothing else.
var runVerbs = map[string]string{"pause": "dungeon_pause", "resume": "dungeon_resume", "skip": "dungeon_skip", "stop": "dungeon_stop"}

// run is the run-control handler (D8, D9): a fresh dungeon_status proves the agent is reachable, every pause and stop needs a confirm, and
// the RPC is logged before it is sent.
func (c *Controller) run(ac *actionContext) {
	var req runRequest
	if !decodeBody(ac.w, ac.r, &req) {
		return
	}
	method, ok := runVerbs[req.Verb]
	if !ok {
		jsonError(ac.w, http.StatusBadRequest, "verb must be one of pause, resume, skip, stop")
		return
	}
	cl := NewControlClient(ac.agent, controlTimeout)
	ctx := ac.r.Context()
	// D8: ask for a fresh status, never the poller's cache. It is a live check: the confirm is decided by the verb alone (D9).
	if _, err := cl.Read(ctx, "dungeon_status"); err != nil {
		jsonError(ac.w, http.StatusBadGateway, "the agent's dungeon_status could not be read: %s", redactMessage(err.Error(), cl.Token()))
		return
	}
	// D9: a pause may end or interrupt a run, and the status read above can be stale by the time the RPC lands. dungeon_pause carries no
	// expected state to guard with, so every pause and every stop needs a confirm.
	if !req.Confirm && (req.Verb == "pause" || req.Verb == "stop") {
		jsonError(ac.w, http.StatusConflict, "%s needs \"confirm\": true", req.Verb)
		return
	}
	if err := c.log.append(controlRecord{Phase: "request", At: c.cfg.Now().UTC().Format(time.RFC3339), Who: ac.who, Via: ac.via, Peer: ac.peer,
		Agent: ac.agent.ID, Action: ac.action, Verb: req.Verb, Args: map[string]bool{"confirm": req.Confirm}}); err != nil {
		jsonError(ac.w, http.StatusInternalServerError, "the control log could not be written, so nothing was sent: %v", err)
		return
	}
	out, err := cl.Write(ctx, method, nil)
	okv := err == nil
	res := controlRecord{Phase: "result", At: c.cfg.Now().UTC().Format(time.RFC3339), Who: ac.who, Via: ac.via, Peer: ac.peer,
		Agent: ac.agent.ID, Action: ac.action, Verb: req.Verb, RPC: method, OK: &okv}
	if err != nil {
		res.Code = rpcCode(err)
		res.Message = redactMessage(err.Error(), cl.Token())
	}
	if lerr := c.log.append(res); lerr != nil {
		jsonError(ac.w, http.StatusInternalServerError, "the control was sent but the log could not be written: %v", lerr)
		return
	}
	if err != nil {
		jsonError(ac.w, http.StatusBadGateway, "%s", res.Message)
		return
	}
	scrubbed, serr := Scrub(orNull(out), cl.Token())
	if serr != nil {
		jsonError(ac.w, http.StatusInternalServerError, "the agent's answer could not be scrubbed: %v", serr)
		return
	}
	writeAny(ac.w, http.StatusOK, map[string]any{"status": "ok", "action": ac.action, "verb": req.Verb, "rpc": method, "result": json.RawMessage(scrubbed)})
}

// decodeBody reads one strict JSON object from the request (unknown fields are refused, a too-large body is 413).
func decodeBody(w http.ResponseWriter, r *http.Request, v any) bool {
	dec := json.NewDecoder(r.Body)
	dec.DisallowUnknownFields()
	if err := dec.Decode(v); err != nil {
		var tooBig *http.MaxBytesError
		if errors.As(err, &tooBig) {
			jsonError(w, http.StatusRequestEntityTooLarge, "the request is larger than %d bytes", maxControlBody)
			return false
		}
		jsonError(w, http.StatusBadRequest, "the request is not the expected JSON: %v", err)
		return false
	}
	if dec.More() {
		jsonError(w, http.StatusBadRequest, "one JSON object, nothing after it")
		return false
	}
	return true
}

// rpcCode is the agent's JSON-RPC error code, 0 for a transport error.
func rpcCode(err error) int {
	var re *RPCError
	if errors.As(err, &re) {
		return re.Code
	}
	return 0
}

// redactMessage blanks the agent's token in a message and cuts it to a length the log keeps (D10).
func redactMessage(s, token string) string {
	if len(token) >= minRedact {
		s = strings.ReplaceAll(s, token, redacted)
	}
	if len(s) > 300 {
		s = s[:300]
	}
	return s
}

// IsTailnetListen reports whether host (no port) may be a --listen address with --control: a loopback name or literal, or a literal Tailscale
// address (D4: a bearer token must never cross a LAN in plain HTTP).
func IsTailnetListen(host string) bool {
	if netutil.IsLoopback(host) {
		return true
	}
	a, err := netip.ParseAddr(host)
	return err == nil && IsTailnetAddr(a)
}
