package dashboard

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"regexp"
	"sync"
	"time"

	"github.com/pjerra/turtle-agent/dungeons"
	"github.com/pjerra/turtle-agent/internal/dungeon"
)

// planName limits a plan name to what a plan file name can be (the same rule dungeon_run applies).
var planName = regexp.MustCompile(`^[a-z0-9][a-z0-9_-]{0,39}$`)

type planEntry struct {
	mod  time.Time
	size int64
	plan *dungeon.Plan
	geo  []byte
	err  string
	path string
}

// planCache loads plan files and their drawings, and loads a file again when it changes (a plan reload on the spot edits the
// file the run reads).
type planCache struct {
	dirs []string
	mu   sync.Mutex
	m    map[string]*planEntry
}

func newPlanCache(dirs []string) *planCache {
	return &planCache{dirs: dirs, m: map[string]*planEntry{}}
}

// get returns the plan called name, its drawing as JSON, and the load error if there is one.
func (c *planCache) get(name string) (*dungeon.Plan, []byte, string) {
	if !planName.MatchString(name) {
		return nil, nil, "not a plan name"
	}
	path := ""
	var st os.FileInfo
	for _, d := range c.dirs {
		p := filepath.Join(d, name+".json")
		if s, err := os.Stat(p); err == nil && s.Mode().IsRegular() {
			path, st = p, s
			break
		}
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if path == "" {
		return c.builtin(name)
	}
	if e := c.m[name]; e != nil && e.path == path && e.mod.Equal(st.ModTime()) && e.size == st.Size() {
		return e.plan, e.geo, e.err
	}
	e := &planEntry{mod: st.ModTime(), size: st.Size(), path: path}
	p, err := dungeon.Load(path)
	if err != nil {
		e.err = firstLine(err.Error())
	} else {
		e.plan = p
		e.geo, err = json.Marshal(BuildGeometry(p))
		if err != nil {
			e.err, e.plan = err.Error(), nil
		}
	}
	c.m[name] = e
	return e.plan, e.geo, e.err
}

// builtin draws the plan file this build carries, for a plan no plans folder has.
func (c *planCache) builtin(name string) (*dungeon.Plan, []byte, string) {
	key := "builtin:" + name
	if e := c.m[key]; e != nil {
		return e.plan, e.geo, e.err
	}
	data, err := dungeons.FS.ReadFile(name + ".json")
	if err != nil {
		return nil, nil, "no plan file " + name + ".json in the plans folders or in this build"
	}
	e := &planEntry{}
	if p, err := dungeon.Parse(name+".json", data); err != nil {
		e.err = firstLine(err.Error())
	} else if e.geo, err = json.Marshal(BuildGeometry(p)); err != nil {
		e.err = err.Error()
	} else {
		e.plan = p
	}
	c.m[key] = e
	return e.plan, e.geo, e.err
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
