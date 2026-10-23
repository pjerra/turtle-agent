package dashboard

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
)

// The history of a dungeon is what earlier runs left in the tests folder (and the agents' traces folder): run traces
// (tracerun files, r-<stamp>-<id>.jsonl). They are only read. From them the page draws the best earlier run as a ghost and the
// places where parties died in the last runs of the dungeon. Everything is bounded and cached: the folders are listed at most every
// historyRescan, a trace is parsed once per change of the file, and the cache holds historyKeep digests.
const (
	historyRescan     = 90 * time.Second
	historyMaxFiles   = 600   // trace files looked at
	historyMaxEntries = 40000 // directory entries looked at per root
	historyDepth      = 6     // folder levels under a root
	historyMinSize    = 4 << 10
	historyMaxSize    = 48 << 20
	historyKeep       = 28
	// HeatRuns is how many of the latest runs of a dungeon the death map reads.
	HeatRuns = 20
	// ghostCandidates is how many partial runs are looked at when no run of the plan is complete.
	ghostCandidates = 8
	ghostMinSeconds = 60
	ghostMaxPoints  = 1200
	heatMaxPoints   = 3000
)

// runFile is a trace file as the index knows it, from its first and last lines.
type runFile struct {
	path    string
	folder  string // the first folder under the root (the capture folder)
	run     string
	plan    string
	mode    string
	start   int64 // unix seconds
	fromLeg int   // the section the run started at (dungeon_run from_leg); 0 for a whole run
	seconds float64
	reason  string // how it ended ("" when no end record: a trace that was cut)
	size    int64
	mod     time.Time
}

type digestEntry struct {
	size int64
	mod  time.Time
	d    *RunDigest
	used time.Time
}

// History indexes and digests the traces of the tests and traces folders.
type History struct {
	roots []string
	now   func() time.Time

	mu      sync.Mutex
	scanned time.Time
	files   []runFile
	heads   map[string]runFile // path -> its header/tail read, valid for size+mod
	digests map[string]*digestEntry
	ghosts  map[string]cachedJSON // plan|exclude -> answer
	heats   map[string]cachedJSON
}

type cachedJSON struct {
	at   time.Time
	scan time.Time
	body []byte
}

// NewHistory makes an index over roots (empty names are left out).
func NewHistory(now func() time.Time, roots ...string) *History {
	h := &History{now: now, heads: map[string]runFile{}, digests: map[string]*digestEntry{}, ghosts: map[string]cachedJSON{}, heats: map[string]cachedJSON{}}
	for _, r := range roots {
		if r != "" {
			h.roots = append(h.roots, r)
		}
	}
	if h.now == nil {
		h.now = time.Now
	}
	return h
}

// scan lists the trace files, at most every historyRescan. The caller holds h.mu.
func (h *History) scan() {
	if !h.scanned.IsZero() && h.now().Sub(h.scanned) < historyRescan {
		return
	}
	h.scanned = h.now()
	var out []runFile
	seen := map[string]bool{}
	for _, root := range h.roots {
		rootReal, err := filepath.EvalSymlinks(root)
		if err != nil {
			continue
		}
		visited := 0
		_ = filepath.WalkDir(rootReal, func(p string, d fs.DirEntry, err error) error {
			if err != nil {
				return nil
			}
			if visited++; visited > historyMaxEntries {
				return fs.SkipAll
			}
			rel, _ := filepath.Rel(rootReal, p)
			if d.IsDir() {
				if strings.Count(rel, string(filepath.Separator)) >= historyDepth || d.Name() == ".git" || d.Name() == "node_modules" {
					return fs.SkipDir
				}
				return nil
			}
			n := d.Name()
			if !strings.HasPrefix(n, "r-") || !strings.HasSuffix(n, ".jsonl") || len(out) >= historyMaxFiles {
				return nil
			}
			info, err := d.Info()
			if err != nil || !info.Mode().IsRegular() || info.Size() < historyMinSize || info.Size() > historyMaxSize {
				return nil
			}
			rf, ok := h.head(p, info)
			if !ok || seen[rf.run] {
				return nil
			}
			seen[rf.run] = true
			rf.folder = "traces" // a trace straight under a root has no capture folder
			if parts := strings.Split(filepath.ToSlash(rel), "/"); len(parts) > 1 {
				rf.folder = parts[0]
			}
			out = append(out, rf)
			return nil
		})
	}
	// forget the heads of files that are gone
	live := map[string]bool{}
	for _, f := range out {
		live[f.path] = true
	}
	for p := range h.heads {
		if !live[p] {
			delete(h.heads, p)
		}
	}
	h.files = out
}

// head reads the header (first line) and the end record (in the last bytes) of a trace.
func (h *History) head(path string, info os.FileInfo) (runFile, bool) {
	if c, ok := h.heads[path]; ok && c.size == info.Size() && c.mod.Equal(info.ModTime()) {
		return c, c.run != ""
	}
	rf := runFile{path: path, size: info.Size(), mod: info.ModTime()}
	defer func() { h.heads[path] = rf }()
	fh, err := os.Open(path)
	if err != nil {
		return rf, false
	}
	defer fh.Close()
	br := bufio.NewReaderSize(fh, 64<<10)
	first, err := br.ReadBytes('\n')
	if err != nil && len(first) == 0 {
		return rf, false
	}
	var hd struct {
		K         string `json:"k"`
		Run       string `json:"run"`
		Plan      string `json:"plan"`
		Mode      string `json:"mode"`
		StartUnix int64  `json:"start_unix"`
		FromLeg   int    `json:"from_leg"`
	}
	if json.Unmarshal(first, &hd) != nil || hd.K != "header" || hd.Run == "" || hd.Plan == "" {
		return rf, false
	}
	rf.run, rf.plan, rf.mode, rf.start, rf.fromLeg = hd.Run, hd.Plan, hd.Mode, hd.StartUnix, hd.FromLeg
	tail := int64(16 << 10)
	off := info.Size() - tail
	if off < 0 {
		off = 0
	}
	if _, err := fh.Seek(off, io.SeekStart); err == nil {
		data, _ := io.ReadAll(fh)
		for _, line := range strings.Split(string(data), "\n") {
			if strings.HasPrefix(line, `{"v":1,"k":"end"`) {
				var e struct {
					Reason  string  `json:"reason"`
					Seconds float64 `json:"seconds"`
				}
				if json.Unmarshal([]byte(line), &e) == nil {
					rf.reason, rf.seconds = e.Reason, e.Seconds
				}
			}
		}
	}
	return rf, true
}

// digest returns the digest of a trace, parsed once per change of the file. The caller holds h.mu.
func (h *History) digest(f runFile) *RunDigest {
	if e := h.digests[f.path]; e != nil && e.size == f.size && e.mod.Equal(f.mod) {
		e.used = h.now()
		return e.d
	}
	data, err := os.ReadFile(f.path)
	if err != nil || int64(len(data)) > historyMaxSize {
		return nil
	}
	d := digestFile(data).Snapshot(0)
	h.digests[f.path] = &digestEntry{size: f.size, mod: f.mod, d: d, used: h.now()}
	if len(h.digests) > historyKeep {
		type kv struct {
			p string
			t time.Time
		}
		var all []kv
		for p, e := range h.digests {
			all = append(all, kv{p, e.used})
		}
		sort.Slice(all, func(i, j int) bool { return all[i].t.Before(all[j].t) })
		for _, x := range all[:len(all)-historyKeep] {
			delete(h.digests, x.p)
		}
	}
	return d
}

// runsOf are the indexed runs of a plan, newest first.
func (h *History) runsOf(plan string) []runFile {
	var out []runFile
	for _, f := range h.files {
		if f.plan == plan {
			out = append(out, f)
		}
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].start > out[j].start })
	return out
}

// GhostAnswer is the best earlier run of a plan, ready for the page.
type GhostAnswer struct {
	Found    bool         `json:"found"`
	Why      string       `json:"why,omitempty"` // when not found
	Plan     string       `json:"plan"`
	Run      string       `json:"run,omitempty"`
	Folder   string       `json:"folder,omitempty"`
	Seconds  float64      `json:"seconds,omitempty"`
	Complete bool         `json:"complete,omitempty"` // the run cleared the whole dungeon (done with no packs left)
	Left     int          `json:"left,omitempty"`     // packs the run leader left alive
	LegsDone int          `json:"legs_done,omitempty"`
	PlanLegs int          `json:"plan_legs,omitempty"` // the plan's sections, when the page said
	Legs     []DLeg       `json:"legs,omitempty"`
	Path     [][3]float64 `json:"path,omitempty"` // [x, y, seconds since the run began] of the lead
	Runs     int          `json:"runs"`           // runs of the plan looked at
}

// Ghost picks the best earlier run of plan, not counting the run called exclude: the one that got through the most sections, and of
// equals a confirmed clean clear before any other run, then the quicker. A run that ended "done", started at the first section and left no packs alive counts as a clear of the whole dungeon,
// or, when planLegs says how many sections the plan has, a run with that many done and none left. The answer is cached until the
// folders are listed again.
func (h *History) Ghost(plan, exclude string, planLegs int) []byte {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.scan()
	key := fmt.Sprintf("%s|%s|%d", plan, exclude, planLegs)
	if c, ok := h.ghosts[key]; ok && c.scan.Equal(h.scanned) {
		return c.body
	}
	ans := GhostAnswer{Plan: plan, PlanLegs: planLegs}
	var runs []runFile
	for _, f := range h.runsOf(plan) {
		if f.run != exclude && f.seconds >= ghostMinSeconds {
			runs = append(runs, f)
		}
	}
	ans.Runs = len(runs)
	// the longest runs are the likeliest to have got far; the quick finished ones are looked at too
	sort.SliceStable(runs, func(i, j int) bool { return runs[i].seconds > runs[j].seconds })
	var picked []runFile
	for _, f := range runs {
		if len(picked) < ghostCandidates || f.reason == "done" {
			picked = append(picked, f)
		}
	}
	var best *RunDigest
	var bestF runFile
	for _, f := range picked {
		d := h.digest(f)
		if d == nil {
			continue
		}
		if best == nil || d.LegsDone > best.LegsDone || (d.LegsDone == best.LegsDone && ghostBetter(f, d, bestF, best)) {
			best, bestF = d, f
		}
	}

	if best == nil {
		ans.Why = "no earlier run of " + plan + " in the tests folder"
		return h.keepGhost(key, ans)
	}
	ans.Found, ans.Run, ans.Folder, ans.Seconds, ans.LegsDone, ans.Left, ans.Legs = true, best.Run, bestF.folder, best.Seconds, best.LegsDone, best.Left, best.Legs
	if planLegs > 0 {
		ans.Complete = best.LegsDone >= planLegs && best.Left == 0
	} else {
		ans.Complete = confirmedClear(bestF, best)
	}
	for _, p := range best.Paths {
		if p.Lead {
			ans.Path = thinPath(p, ghostMaxPoints)
			break
		}
	}
	if len(ans.Path) == 0 && len(best.Paths) > 0 {
		ans.Path = thinPath(best.Paths[0], ghostMaxPoints)
	}
	return h.keepGhost(key, ans)
}

// confirmedClear is a run that cleared the whole dungeon for sure: it ended
// "done", started at the first section and left no packs alive. An
// interrupted run never reported a final count, so its Left of 0 proves
// nothing. Complete and the ghost tie-break share this predicate.
func confirmedClear(f runFile, d *RunDigest) bool {
	return f.reason == "done" && f.fromLeg == 0 && d.Left == 0
}

// ghostBetter says whether d is the better ghost of two runs that finished the same sections: a confirmed clean clear beats any other run, and of two alike runs the quicker wins.
func ghostBetter(df runFile, d *RunDigest, bestF runFile, best *RunDigest) bool {
	dClean, bestClean := confirmedClear(df, d), confirmedClear(bestF, best)
	if dClean != bestClean {
		return dClean
	}
	return d.Seconds < best.Seconds
}

func (h *History) keepGhost(key string, ans GhostAnswer) []byte {
	b, _ := json.Marshal(ans)
	h.ghosts[key] = cachedJSON{scan: h.scanned, body: b}
	return b
}

func thinPath(p DPath, limit int) [][3]float64 {
	step := 1
	if len(p.P) > limit {
		step = (len(p.P) + limit - 1) / limit
	}
	var out [][3]float64
	for i := 0; i < len(p.P); i += step {
		out = append(out, [3]float64{p.P[i][0], p.P[i][1], p.T[i]})
	}
	if last := len(p.P) - 1; last >= 0 && (len(out) == 0 || out[len(out)-1][2] != p.T[last]) {
		out = append(out, [3]float64{p.P[last][0], p.P[last][1], p.T[last]})
	}
	return out
}

// HeatAnswer is where parties died in the last HeatRuns runs of a plan: [x, y, leg].
type HeatAnswer struct {
	Plan   string       `json:"plan"`
	Runs   int          `json:"runs"`
	Deaths int          `json:"deaths"`
	Points [][3]float64 `json:"points"`
	Why    string       `json:"why,omitempty"`
}

// Heat collects the death places of the latest runs of plan.
func (h *History) Heat(plan string) []byte {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.scan()
	if c, ok := h.heats[plan]; ok && c.scan.Equal(h.scanned) {
		return c.body
	}
	ans := HeatAnswer{Plan: plan, Points: [][3]float64{}}
	runs := h.runsOf(plan)
	if len(runs) > HeatRuns {
		runs = runs[:HeatRuns]
	}
	for _, f := range runs {
		d := h.digest(f)
		if d == nil {
			continue
		}
		ans.Runs++
		for _, m := range d.Deaths {
			if len(ans.Points) < heatMaxPoints {
				ans.Points = append(ans.Points, [3]float64{m.X, m.Y, float64(m.Leg)})
			}
			ans.Deaths++
		}
	}
	if ans.Runs == 0 {
		ans.Why = "no earlier run of " + plan + " in the tests folder"
	}
	b, _ := json.Marshal(ans)
	h.heats[plan] = cachedJSON{scan: h.scanned, body: b}
	return b
}
