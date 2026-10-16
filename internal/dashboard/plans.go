package dashboard

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/pjerra/turtle-agent/dungeons"
	"github.com/pjerra/turtle-agent/internal/dungeon"
)

// planName limits a plan name to what a plan file name can be (the same rule dungeon_run applies).
var planName = regexp.MustCompile(`^[a-z0-9][a-z0-9_-]{0,39}$`)

// PlanSource says which file a plan was read from: the copy the dashboard draws is not always the copy a run walks.
type PlanSource struct {
	// Role is "dashboard" (a folder of --plans-dir), "slot" (a slot's dungeons_dir, the folder its runs read) or "build" (the copy carried
	// by this binary, used when no folder has the plan).
	Role     string `json:"role"`
	Dir      string `json:"dir,omitempty"`
	Path     string `json:"path"`
	SHA256   string `json:"sha256,omitempty"`
	Modified string `json:"modified,omitempty"` // RFC 3339, UTC
}

// Short is the first 8 hex digits of the file's sha256, the way the page names a copy.
func (s PlanSource) Short() string {
	if len(s.SHA256) >= 8 {
		return s.SHA256[:8]
	}
	return s.SHA256
}

type planEntry struct {
	mod  time.Time
	size int64
	plan *dungeon.Plan
	g    Geometry
	err  string
	path string
	src  PlanSource
}

// planCache loads plan files and their drawings, and loads a file again when it changes (a plan reload on the spot edits the
// file the run reads). Entries are kept by file path, so the dashboard's copy of a plan and a slot's copy of it are two entries.
type planCache struct {
	dirs []string
	mu   sync.Mutex
	m    map[string]*planEntry
}

func newPlanCache(dirs []string) *planCache {
	return &planCache{dirs: dirs, m: map[string]*planEntry{}}
}

// get returns the plan called name from the dashboard's folders (or the build), its drawing as JSON, and the load error if there is one.
func (c *planCache) get(name string) (*dungeon.Plan, []byte, string) {
	plan, g, _, errText := c.lookup(c.dirs, name, "dashboard")
	if plan == nil {
		return nil, nil, errText
	}
	b, err := json.Marshal(g)
	if err != nil {
		return nil, nil, err.Error()
	}
	return plan, b, errText
}

// lookup finds the plan called name in dirs (the first folder that has the file) and, only when role is "dashboard", falls back to the
// copy built into this binary. The drawing carries the source it was read from.
func (c *planCache) lookup(dirs []string, name, role string) (*dungeon.Plan, Geometry, PlanSource, string) {
	if !planName.MatchString(name) {
		return nil, Geometry{}, PlanSource{}, "not a plan name"
	}
	path, dir := "", ""
	var st os.FileInfo
	for _, d := range dirs {
		if d == "" {
			continue
		}
		p := filepath.Join(d, name+".json")
		if s, err := os.Stat(p); err == nil && s.Mode().IsRegular() {
			path, dir, st = p, d, s
			break
		}
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if path == "" {
		if role != "dashboard" {
			return nil, Geometry{}, PlanSource{}, "no plan file " + name + ".json in " + strings.Join(dirs, ", ")
		}
		return c.builtin(name)
	}
	key := path + "|" + role
	if e := c.m[key]; e != nil && e.mod.Equal(st.ModTime()) && e.size == st.Size() {
		return e.plan, e.g, e.src, e.err
	}
	e := &planEntry{mod: st.ModTime(), size: st.Size(), path: path}
	e.src = PlanSource{Role: role, Dir: dir, Path: path, Modified: st.ModTime().UTC().Format(time.RFC3339)}
	data, err := os.ReadFile(path)
	if err != nil {
		e.err = firstLine(err.Error())
	} else {
		sum := sha256.Sum256(data)
		e.src.SHA256 = hex.EncodeToString(sum[:])
		p, err := dungeon.Parse(path, data)
		if err != nil {
			e.err = firstLine(err.Error())
		} else {
			e.plan, e.g = p, BuildGeometry(p)
			e.g.Source = &e.src
		}
	}
	c.m[key] = e
	return e.plan, e.g, e.src, e.err
}

// builtin draws the plan file this build carries, for a plan no plans folder has.
func (c *planCache) builtin(name string) (*dungeon.Plan, Geometry, PlanSource, string) {
	key := "builtin:" + name
	if e := c.m[key]; e != nil {
		return e.plan, e.g, e.src, e.err
	}
	data, err := dungeons.FS.ReadFile(name + ".json")
	if err != nil {
		return nil, Geometry{}, PlanSource{}, "no plan file " + name + ".json in the plans folders or in this build"
	}
	sum := sha256.Sum256(data)
	e := &planEntry{src: PlanSource{Role: "build", Path: "(built into this turtle-agent) " + name + ".json", SHA256: hex.EncodeToString(sum[:])}}
	if p, err := dungeon.Parse(name+".json", data); err != nil {
		e.err = firstLine(err.Error())
	} else {
		e.plan, e.g = p, BuildGeometry(p)
		e.g.Source = &e.src
	}
	c.m[key] = e
	return e.plan, e.g, e.src, e.err
}

// PlanCopy says which file a run card draws and which file the run reads. The page shows Text as the card's plan label.
type PlanCopy struct {
	// Drawn is the file the card's map and the card's group mapping come from: the run's own file when it is there, else the dashboard's.
	Drawn PlanSource `json:"drawn"`
	// Run is the run's file; nil when it could not be found. The run reports the path it was loaded from (Reported); without that the
	// slot's settings are used (dungeons_dir + the plan name).
	Run *PlanSource `json:"run,omitempty"`
	// Reported: Run's path is the one the run itself reported (report.remote.plan_file), not a guess from the settings.
	Reported bool `json:"reported,omitempty"`
	// RunSHA256 is the sha256 the run reports for the last version of its file it read; Changed: the file on disk is another version by now
	// (the run takes the change at its next leg, or never when live_reload is off).
	RunSHA256 string `json:"run_sha256,omitempty"`
	Changed   bool   `json:"changed,omitempty"`
	// Dash is the dashboard's own copy (--plans-dir, else the build's); nil when it has none.
	Dash *PlanSource `json:"dash,omitempty"`
	// Same: the run's copy and the dashboard's are the same bytes. Meaningful when both are there.
	Same bool   `json:"same"`
	Text string `json:"text"`
}

// reportedPlan is what a run says about its plan file (dungeon_status report.remote.plan_file and plan_sha256).
type reportedPlan struct {
	Plan, File, SHA string
}

// copyFor works out PlanCopy of agent a for the plan called name, and returns the plan to draw (the run's file when it loads, else the
// dashboard's) with the load error of the one drawn. rp is what the agent's run reports about its file (zero when it reports none).
func (c *planCache) copyFor(a Agent, name string, rp reportedPlan) (PlanCopy, *dungeon.Plan, Geometry, string) {
	dplan, dgeo, dsrc, derr := c.lookup(c.dirs, name, "dashboard")
	cp := PlanCopy{}
	if dsrc.Path != "" {
		d := dsrc
		cp.Dash = &d
	}
	plan, geo, errText, drawn := dplan, dgeo, derr, dsrc
	// the run's folder: the file it reports when that is a plan file of this name, else the dungeons_dir of the slot's settings
	runDir, reported := a.DungeonsDir, false
	if rp.Plan == name && rp.File != "" && filepath.IsAbs(rp.File) && filepath.Base(rp.File) == name+".json" {
		runDir, reported = filepath.Dir(rp.File), true
		cp.RunSHA256 = rp.SHA
	}
	slotErr := ""
	if runDir != "" {
		splan, sgeo, ssrc, serr := c.lookup([]string{runDir}, name, "slot")
		switch {
		case splan != nil:
			s := ssrc
			cp.Run, cp.Reported = &s, reported
			plan, geo, errText, drawn = splan, sgeo, "", ssrc
			cp.Changed = reported && rp.SHA != "" && rp.SHA != ssrc.SHA256
		case ssrc.Path != "":
			slotErr = serr // the file is there and does not load
		}
	}
	cp.Drawn = drawn
	cp.Same = cp.Run != nil && cp.Dash != nil && cp.Run.SHA256 == cp.Dash.SHA256
	cp.Text = copyText(a, name, cp, runDir, slotErr)
	return cp, plan, geo, errText
}

// copyText is the card's label in words: the drawn file, the file the run reads, and whether they are the same.
func copyText(a Agent, name string, cp PlanCopy, runDir, slotErr string) string {
	dash := "no copy in the dashboard's plans folder or the build"
	if cp.Dash != nil {
		dash = "the dashboard's copy " + cp.Dash.Path + " (" + cp.Dash.Short() + ")"
	}
	switch {
	case cp.Run != nil:
		who := "the run's own copy " + cp.Run.Path + " (" + cp.Run.Short() + "), which is what the run reads (from its settings' dungeons_dir)"
		if cp.Reported {
			who = "the file the run reports it was loaded from, " + cp.Run.Path + " (" + cp.Run.Short() + ")"
		}
		t := "Drawn: " + who + ". "
		if cp.Same {
			t += "The dashboard's copy is the same file content."
		} else {
			t += "It differs from " + dash + "."
		}
		if cp.Changed {
			t += " The file has changed since the run last read it (it read " + cp.RunSHA256[:min(8, len(cp.RunSHA256))] + "); a run with live reload takes the change at its next leg."
		}
		return t
	case slotErr != "":
		return "The run's copy " + filepath.Join(runDir, name+".json") + " does not load (" + slotErr + "), so this map shows " + dash + ", not what the run reads."
	case runDir != "":
		return "The run's folder " + runDir + " has no " + name + ".json, so this map shows " + dash + ". The run reads another file than the one drawn."
	default:
		return "This agent's settings name no dungeons_dir and its run reports no plan file: it reads dungeons/ under the agent's own working folder, which the dashboard cannot see. This map shows " + dash + "."
	}
}

// PlanInfo is one line of the plan list.
type PlanInfo struct {
	Name   string     `json:"name"`
	Map    uint32     `json:"map,omitempty"`
	Legs   int        `json:"legs,omitempty"`
	Status string     `json:"status,omitempty"`
	Source PlanSource `json:"source"`
	Error  string     `json:"error,omitempty"`
}

// list names every plan the dashboard can draw: the files of its folders (the first folder that has a name wins) and the plans of the build
// that no folder has.
func (c *planCache) list() []PlanInfo {
	seen := map[string]bool{}
	var names []string
	add := func(n string) {
		if planName.MatchString(n) && !seen[n] {
			seen[n] = true
			names = append(names, n)
		}
	}
	for _, d := range c.dirs {
		files, _ := filepath.Glob(filepath.Join(d, "*.json"))
		for _, f := range files {
			add(strings.TrimSuffix(filepath.Base(f), ".json"))
		}
	}
	if ents, err := dungeons.FS.ReadDir("."); err == nil {
		for _, e := range ents {
			add(strings.TrimSuffix(e.Name(), ".json"))
		}
	}
	sort.Strings(names)
	out := make([]PlanInfo, 0, len(names))
	for _, n := range names {
		plan, _, src, errText := c.lookup(c.dirs, n, "dashboard")
		pi := PlanInfo{Name: n, Source: src, Error: errText}
		if plan != nil {
			pi.Map, pi.Legs, pi.Status = plan.Map, len(plan.Legs), plan.Status
		}
		out = append(out, pi)
	}
	return out
}

var errOutside = errors.New("path is outside the folder")

// SafeJoin joins rel to root and refuses anything that leaves root: dot-dot segments, absolute paths, backslashes, NUL bytes
// and symlinks that point out. The result is a regular file or an error.
func SafeJoin(root, rel string) (string, error) {
	if rel == "" || containsAny(rel, "\x00\\") || filepath.IsAbs(rel) {
		return "", errOutside
	}
	for _, seg := range splitSlash(rel) {
		if seg == ".." {
			return "", errOutside
		}
	}
	rootReal, err := filepath.EvalSymlinks(root)
	if err != nil {
		return "", err
	}
	full, err := filepath.EvalSymlinks(filepath.Join(rootReal, filepath.FromSlash(rel)))
	if err != nil {
		return "", err
	}
	back, err := filepath.Rel(rootReal, full)
	if err != nil || back == ".." || len(back) >= 3 && back[:3] == ".."+string(filepath.Separator) {
		return "", errOutside
	}
	st, err := os.Stat(full)
	if err != nil {
		return "", err
	}
	if !st.Mode().IsRegular() {
		return "", errors.New("not a file")
	}
	return full, nil
}

func containsAny(s, chars string) bool {
	for _, r := range s {
		for _, c := range chars {
			if r == c {
				return true
			}
		}
	}
	return false
}

func splitSlash(s string) []string {
	var out []string
	start := 0
	for i := 0; i <= len(s); i++ {
		if i == len(s) || s[i] == '/' {
			out = append(out, s[start:i])
			start = i + 1
		}
	}
	return out
}
