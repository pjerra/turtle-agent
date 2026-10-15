package routeedit

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"

	"github.com/pjerra/turtle-agent/internal/dungeon"
)

// PlanName is the rule for a plan's name everywhere in the editor (the dashboard's and dungeon_run's): it is the only thing of the request
// that becomes part of a path, together with the version number the server counts.
var PlanName = regexp.MustCompile(`^[a-z0-9][a-z0-9_-]{0,39}$`)

var versionName = regexp.MustCompile(`^v(\d{4})$`)

// The statuses of a version.
const (
	StatusChecked   = "checked"   // the plan check found no issue in the edited plan at all
	StatusUnchecked = "unchecked" // the edit made no new issue, but the plan has plan check issues of its own: kept, never picked for a run
	StatusRefused   = "refused"   // the plan check failed (or could not run): a record only, there is no plan file
	StatusAccepted  = "accepted"  // a run walked it and passed (set by whoever promotes it)
)

// MaxVersions is the highest version number: the name is four digits.
const MaxVersions = 9999

// BaseInfo says what an edit started from.
type BaseInfo struct {
	Source string `json:"source"` // the plan file edited
	SHA256 string `json:"sha256"`
	GitSHA string `json:"git_sha,omitempty"` // HEAD of the repo the source folder is in, when it is one
}

// Meta is the record of a version (vNNNN.meta.json).
type Meta struct {
	Version  int               `json:"version"`
	Plan     string            `json:"plan"`
	Who      string            `json:"who"`
	At       string            `json:"at"` // server clock, UTC, RFC 3339
	Status   string            `json:"status"`
	Note     string            `json:"note,omitempty"`
	Base     BaseInfo          `json:"base"`
	Mmaps    string            `json:"mmaps"`            // the navmesh folder the check ran on
	SHA256   string            `json:"sha256,omitempty"` // of vNNNN.json
	Check    CheckResult       `json:"check"`
	Ops      []Op              `json:"ops"`
	Changes  []Change          `json:"changes"`
	PlanDiff []dungeon.PatchOp `json:"plan_diff"`
}

// Name is the version as it is written in file names: v0007.
func Name(v int) string { return fmt.Sprintf("v%04d", v) }

// ParseVersion reads "v0007".
func ParseVersion(s string) (int, bool) {
	m := versionName.FindStringSubmatch(s)
	if m == nil {
		return 0, false
	}
	n, _ := strconv.Atoi(m[1])
	return n, n > 0
}

// Store keeps the versions of every plan under one folder: <root>/<plan>/vNNNN.json (the whole plan, dungeon.Marshal),
// vNNNN.meta.json (Meta) and vNNNN.diff (a unified text diff against the base). A refused edit has the last two only. Versions are
// never overwritten or removed. The only file names it creates are built here, from a validated plan name and a number it counts, and every
// path step is made relative to a directory descriptor opened without following links (dirfd_linux.go).
type Store struct {
	root string
	rd   *rootDir
	mu   sync.Mutex
}

func newStore(root string, create bool) (*Store, error) {
	root, err := filepath.Abs(root)
	if err != nil {
		return nil, err
	}
	if create {
		if err := os.MkdirAll(root, 0o750); err != nil {
			return nil, err
		}
	}
	real, err := filepath.EvalSymlinks(root)
	if err != nil {
		return nil, fmt.Errorf("edits folder: %w", err)
	}
	if st, err := os.Stat(real); err != nil || !st.IsDir() {
		return nil, fmt.Errorf("%s is not a folder", root)
	}
	rd, err := openRoot(real)
	if err != nil {
		return nil, err
	}
	return &Store{root: real, rd: rd}, nil
}

// NewStore makes the folder (private to the user) if it is not there, resolves its real path and holds it open, so that a link put in its
// place later is not followed.
func NewStore(root string) (*Store, error) { return newStore(root, true) }

// OpenStore opens an existing store folder without making it (for reading: list, export, promote).
func OpenStore(root string) (*Store, error) { return newStore(root, false) }

// Close lets go of the folder.
func (s *Store) Close() { s.rd.close() }

// Root is the folder, symlinks resolved.
func (s *Store) Root() string { return s.root }

// dir opens the folder of a plan; with create it is made. A plan folder that is a link, or a file, is refused.
func (s *Store) dir(plan string, create bool) (*planDir, error) {
	if !PlanName.MatchString(plan) {
		return nil, fmt.Errorf("%q is not a plan name", plan)
	}
	return s.rd.openPlanDir(plan, create)
}

// Save writes the next version of plan. meta.Version is set here; planJSON is the whole plan (nil for a refused edit, which keeps no plan).
// It returns the version number.
func (s *Store) Save(plan string, meta *Meta, planJSON []byte, diff string) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	d, err := s.dir(plan, true)
	if err != nil {
		return 0, err
	}
	defer d.close()
	have, err := versionsOf(d)
	if err != nil {
		return 0, err
	}
	n := 1
	if len(have) > 0 {
		n = have[len(have)-1] + 1
	}
	for ; n <= MaxVersions; n++ {
		// the record is the claim on the number: two writers cannot both create it
		mf, err := d.create(Name(n) + ".meta.json")
		if errors.Is(err, os.ErrExist) {
			continue
		}
		if err != nil {
			return 0, err
		}
		written := []string{Name(n) + ".meta.json"}
		fail := func(err error) (int, error) {
			mf.Close()
			for _, f := range written {
				d.remove(f) // only files this call made
			}
			return 0, err
		}
		meta.Version, meta.Plan = n, plan
		if planJSON != nil {
			sum := sha256.Sum256(planJSON)
			meta.SHA256 = hex.EncodeToString(sum[:])
			if err := writeNew(d, Name(n)+".json", planJSON, &written); err != nil {
				return fail(err)
			}
		}
		if err := writeNew(d, Name(n)+".diff", []byte(diff), &written); err != nil {
			return fail(err)
		}
		mb, err := json.MarshalIndent(meta, "", "  ")
		if err != nil {
			return fail(err)
		}
		if _, err := mf.Write(append(mb, '\n')); err != nil {
			return fail(err)
		}
		return n, mf.Close()
	}
	return 0, fmt.Errorf("%s has %d versions: the numbers are used up", plan, MaxVersions)
}

func writeNew(d *planDir, name string, data []byte, written *[]string) error {
	f, err := d.create(name)
	if err != nil {
		return err
	}
	*written = append(*written, name)
	if _, err := f.Write(data); err != nil {
		f.Close()
		return err
	}
	return f.Close()
}

// versionsOf lists the version numbers that have a record in the plan folder, ascending.
func versionsOf(d *planDir) ([]int, error) {
	names, err := d.names()
	if err != nil {
		return nil, err
	}
	var out []int
	for _, name := range names {
		if stem, ok := strings.CutSuffix(name, ".meta.json"); ok {
			if n, ok := ParseVersion(stem); ok {
				out = append(out, n)
			}
		}
	}
	sort.Ints(out)
	return out, nil
}

// List returns the records of a plan, oldest first. A record that does not read (a write that was cut off) is left out.
func (s *Store) List(plan string) ([]Meta, error) {
	d, err := s.dir(plan, false)
	if errors.Is(err, os.ErrNotExist) {
		return []Meta{}, nil
	}
	if err != nil {
		return nil, err
	}
	ns, err := versionsOf(d)
	d.close()
	if err != nil {
		return nil, err
	}
	out := []Meta{}
	for _, n := range ns {
		if m, err := s.Meta(plan, n); err == nil {
			out = append(out, *m)
		}
	}
	return out, nil
}

// Plans lists the plans that have versions.
func (s *Store) Plans() ([]string, error) {
	names, err := s.rd.planNames()
	if err != nil {
		return nil, err
	}
	out := []string{}
	for _, n := range names {
		if PlanName.MatchString(n) {
			out = append(out, n)
		}
	}
	return out, nil
}

// maxFile bounds what is read back from the store.
const maxFile = 8 << 20

func (s *Store) read(plan string, v int, suffix string) ([]byte, error) {
	if v <= 0 || v > MaxVersions {
		return nil, fmt.Errorf("version %d", v)
	}
	d, err := s.dir(plan, false)
	if err != nil {
		return nil, err
	}
	defer d.close()
	f, err := d.open(Name(v) + suffix)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	return io.ReadAll(io.LimitReader(f, maxFile))
}

// Meta reads the record of a version.
func (s *Store) Meta(plan string, v int) (*Meta, error) {
	b, err := s.read(plan, v, ".meta.json")
	if err != nil {
		return nil, err
	}
	var m Meta
	if err := json.Unmarshal(b, &m); err != nil {
		return nil, err
	}
	return &m, nil
}

// PlanFile reads the plan of a version (not there for a refused edit).
func (s *Store) PlanFile(plan string, v int) ([]byte, error) { return s.read(plan, v, ".json") }

// Diff reads the text diff of a version.
func (s *Store) Diff(plan string, v int) (string, error) {
	b, err := s.read(plan, v, ".diff")
	return string(b), err
}

// Latest is the newest version that is checked (and not refused), or 0.
func (s *Store) Latest(plan string) (int, error) {
	ms, err := s.List(plan)
	if err != nil {
		return 0, err
	}
	for i := len(ms) - 1; i >= 0; i-- {
		if ms[i].Status == StatusChecked || ms[i].Status == StatusAccepted {
			return ms[i].Version, nil
		}
	}
	return 0, nil
}
