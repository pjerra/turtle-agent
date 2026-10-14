package dashboard

import (
	"context"
	"encoding/json"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
)

// Options configure the dashboard.
type Options struct {
	ConfigDir   string        // where settings*.json and the RPC token files are
	Agents      string        // "auto", or a comma list of RPC addresses (also "auto,8770")
	TestsDir    string        // the only folder whose files are served
	TracesDir   string        // where the agents write run traces (read, never served)
	PlansDirs   []string      // folders with dungeon plan files
	MinimapDir  string        // images from `turtle-agent minimap-extract` (<map>.png + <map>.json), drawn under the plan; "" for none
	Interval    time.Duration // poll period
	CallTimeout time.Duration // bound on one agent's polls
	Now         func() time.Time
}

// Poller polls the agents on a fixed period and keeps the latest answer as ready-made JSON, so that a request does no work.
type Poller struct {
	opt    Options
	plans  *planCache
	tailer *Tailer

	mu      sync.RWMutex
	state   []byte
	raw     map[string][]byte
	clients map[string]*Client
	alive   map[string]bool // agents that have answered since the start
	trails  map[string]*Trail
	agents  []Agent
	lastDis time.Time
}

// NewPoller applies the defaults.
func NewPoller(o Options) *Poller {
	if o.Interval <= 0 {
		o.Interval = 1500 * time.Millisecond
	}
	if o.CallTimeout <= 0 {
		o.CallTimeout = 2 * time.Second
	}
	if o.Now == nil {
		o.Now = time.Now
	}
	if o.ConfigDir == "" {
		o.ConfigDir = DefaultConfigDir()
	}
	if o.Agents == "" {
		o.Agents = "auto"
	}
	p := &Poller{opt: o, plans: newPlanCache(o.PlansDirs), tailer: NewTailer(), raw: map[string][]byte{}, clients: map[string]*Client{}, alive: map[string]bool{}, trails: map[string]*Trail{}}
	p.state = []byte(`{"agents":[],"offline":[]}`)
	return p
}

// Run polls until ctx ends.
func (p *Poller) Run(ctx context.Context) {
	p.Tick(ctx)
	t := time.NewTicker(p.opt.Interval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			p.Tick(ctx)
		}
	}
}

// rediscover is how often the agent list is read again, so that an agent started later shows up.
const rediscover = 10 * time.Second

// Tick polls every agent once, in parallel, and publishes the result.
func (p *Poller) Tick(ctx context.Context) {
	now := p.opt.Now()
	if p.agents == nil || now.Sub(p.lastDis) >= rediscover {
		if as, err := DiscoverAgents(p.opt.ConfigDir, p.opt.Agents); err == nil {
			p.agents, p.lastDis = as, now
		}
	}
	agents := p.agents
	type result struct {
		card Card
		raw  []byte
	}
	res := make([]result, len(agents))
	var wg sync.WaitGroup
	for i, a := range agents {
		wg.Add(1)
		go func() {
			defer wg.Done()
			cl := p.client(a)
			c, raw := p.pollOne(ctx, cl, now)
			res[i] = result{c, raw}
		}()
	}
	wg.Wait()
	state := struct {
		Time    time.Time `json:"time"`
		Agents  []Card    `json:"agents"`
		Offline []string  `json:"offline"`
		Plans   []string  `json:"plans"`
	}{Time: now, Agents: []Card{}, Offline: []string{}}
	raws := map[string][]byte{}
	plans := map[string]bool{}
	p.mu.Lock()
	for i, a := range agents {
		c := res[i].card
		if c.Online {
			p.alive[a.ID] = true
			p.trackTrail(a.ID, &c)
		}
		switch {
		case c.Online:
			state.Agents = append(state.Agents, c)
			raws[a.ID] = res[i].raw
			if c.Label.Plan != "" {
				plans[c.Label.Plan] = true
			}
		case p.alive[a.ID]:
			state.Agents = append(state.Agents, c) // seen before and gone now: shown as offline
		default:
			state.Offline = append(state.Offline, a.ID)
		}
	}
	p.mu.Unlock()
	for n := range plans {
		state.Plans = append(state.Plans, n)
	}
	sort.Strings(state.Plans)
	body, err := json.Marshal(state)
	if err == nil {
		body, err = Scrub(body, p.secrets()...)
	}
	if err != nil {
		return
	}
	p.mu.Lock()
	p.state, p.raw = body, raws
	p.mu.Unlock()
}

// trackTrail adds the lead's position to the agent's trail while a run goes and puts the trail on the card; an ended run keeps its trail
// on show until the next one starts. The caller holds p.mu.
func (p *Poller) trackTrail(id string, c *Card) {
	t := p.trails[id]
	if t == nil {
		t = &Trail{}
		p.trails[id] = t
	}
	if c.Run.State == "running" {
		if x, y, ok := c.LeadPosition(); ok {
			run := c.Label.RunID
			if run == "" {
				run = c.Label.Plan
			}
			t.Add(run, x, y)
		}
	}
	c.Trail = t.Points()
}

func (p *Poller) client(a Agent) *Client {
	p.mu.Lock()
	defer p.mu.Unlock()
	c := p.clients[a.ID]
	if c == nil || c.Agent != a {
		c = NewClient(a, p.opt.CallTimeout)
		p.clients[a.ID] = c
	}
	return c
}

// secrets are the tokens seen on the last calls; a reply that echoes one is redacted.
func (p *Poller) secrets() []string {
	p.mu.RLock()
	defer p.mu.RUnlock()
	var out []string
	for _, c := range p.clients {
		if c.Token() != "" {
			out = append(out, c.Token())
		}
	}
	return out
}

// pollOne asks one agent its read-only questions and builds its card and its raw panel.
func (p *Poller) pollOne(ctx context.Context, cl *Client, now time.Time) (Card, []byte) {
	ctx, cancel := context.WithTimeout(ctx, p.opt.CallTimeout)
	defer cancel()
	in := Inputs{Agent: cl.Agent, Now: now}
	status, err := cl.Call(ctx, "status")
	if err != nil {
		in.StatusErr = err.Error()
		return BuildCard(in), nil
	}
	in.Status = status
	var wg sync.WaitGroup
	call := func(method string, dst *json.RawMessage) {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if r, err := cl.Call(ctx, method); err == nil {
				*dst = r
			}
		}()
	}
	call("dungeon_status", &in.Dungeon)
	call("tune_list", &in.Tune)
	call("bot_view", &in.View)
	wg.Wait()
	var ds struct {
		Report *struct {
			Plan      string `json:"plan"`
			TraceFile string `json:"trace_file"`
		} `json:"report"`
	}
	if json.Unmarshal(in.Dungeon, &ds) == nil && ds.Report != nil {
		if ds.Report.Plan != "" {
			in.Plan, _, in.PlanErr = p.plans.get(ds.Report.Plan)
		}
		if path, link, folder, ok := p.resolveTrace(ds.Report.TraceFile); ok {
			if snap, err := p.tailer.Read(path, now); err == nil {
				in.Trace = &snap
				in.Folder = folder
				in.Links = p.links(link)
			}
		}
	}
	card := BuildCard(in)
	raw, _ := json.Marshal(map[string]json.RawMessage{"status": orNull(in.Status), "dungeon_status": orNull(in.Dungeon), "tune_list": orNull(in.Tune)})
	raw, err = Scrub(raw, cl.Token())
	if err != nil {
		raw = nil
	}
	return card, raw
}

func orNull(r json.RawMessage) json.RawMessage {
	if len(r) == 0 {
		return json.RawMessage("null")
	}
	return r
}

// resolveTrace maps the trace path an agent reported to a file the dashboard may read. The path may be relative to the agent's
// working folder (the folder above the traces folder) or absolute; it must end in .jsonl and, after symlinks, lie in the tests
// folder or the traces folder. link is the path under the tests folder (empty for a trace outside it), folder the capture
// folder it belongs to.
func (p *Poller) resolveTrace(reported string) (path, link, folder string, ok bool) {
	if reported == "" || !strings.HasSuffix(reported, ".jsonl") {
		return "", "", "", false
	}
	var cands []string
	if filepath.IsAbs(reported) {
		cands = []string{filepath.Clean(reported)}
	} else if p.opt.TracesDir != "" {
		cands = []string{filepath.Join(filepath.Dir(p.opt.TracesDir), reported), filepath.Join(p.opt.TracesDir, filepath.Base(reported))}
	}
	for _, c := range cands {
		for _, root := range []string{p.opt.TestsDir, p.opt.TracesDir} {
			if root == "" {
				continue
			}
			rootReal, err := filepath.EvalSymlinks(root)
			if err != nil {
				continue
			}
			rel, err := filepath.Rel(root, c)
			if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
				continue
			}
			full, err := SafeJoin(rootReal, filepath.ToSlash(rel))
			if err != nil {
				continue
			}
			if root == p.opt.TestsDir {
				return full, filepath.ToSlash(rel), strings.Split(filepath.ToSlash(rel), "/")[0], true
			}
			return full, "", "traces", true
		}
	}
	return "", "", "", false
}

// State is the latest published state as JSON.
func (p *Poller) State() []byte {
	p.mu.RLock()
	defer p.mu.RUnlock()
	return p.state
}

// Raw is the latest scrubbed replies of an agent.
func (p *Poller) Raw(id string) ([]byte, bool) {
	p.mu.RLock()
	defer p.mu.RUnlock()
	b, ok := p.raw[id]
	return b, ok
}

// Geometry is the drawing of a plan as JSON.
func (p *Poller) Geometry(name string) ([]byte, string) {
	_, geo, errText := p.plans.get(name)
	return geo, errText
}

// links are the files of a run the page can link to: the trace, its summary and the capture folder's report, as far as they exist
// under the tests folder.
func (p *Poller) links(trace string) []Link {
	if trace == "" || p.opt.TestsDir == "" {
		return nil
	}
	var out []Link
	add := func(name, rel string) {
		if _, err := SafeJoin(p.opt.TestsDir, rel); err == nil {
			out = append(out, Link{Name: name, Path: rel})
		}
	}
	add("trace", trace)
	add("summary", strings.TrimSuffix(trace, ".jsonl")+".summary.json")
	if i := strings.IndexByte(trace, '/'); i > 0 {
		add("report", trace[:i]+"/REPORT.md")
	}
	return out
}
