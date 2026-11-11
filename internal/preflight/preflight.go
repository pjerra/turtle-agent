// Package preflight checks a raid-walk directory before any walk starts: the binaries, the slot table, the
// instance-cap file, the leases, the placement flags, the plan/export pair, the server config keys and the rosters.
// Every check is read-only: no lease, ledger or config is written, and the server side is read over ssh.
package preflight

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"time"

	"github.com/pjerra/turtle-agent/internal/dungeon"
	"github.com/pjerra/turtle-agent/internal/planexport"
	"github.com/pjerra/turtle-agent/internal/raidwalk"
	"github.com/pjerra/turtle-agent/internal/slots"
)

// Check names, numbered as the brief's checks.
const (
	CheckBinary   = "1 binary"
	CheckSchema   = "2 slots schema"
	CheckSlots    = "3 slots path"
	CheckCap      = "4 instance cap"
	CheckLease    = "5 lease"
	CheckPlace    = "6 gm-clear"
	CheckPlan     = "7 plan"
	CheckKeys     = "8 server keys"
	CheckRoster   = "9 roster"
	statusOKName  = "OK"
	statusWarnNam = "WARN"
	statusFailNam = "FAIL"
)

// Status is the outcome of one check.
type Status string

// The three outcomes.
const (
	OK   Status = statusOKName
	Warn Status = statusWarnNam
	Fail Status = statusFailNam
)

// Result is one line of the report: the check, its outcome, the thing checked, what was seen and how to fix a
// warning or a failure.
type Result struct {
	Check   string `json:"check"`
	Status  Status `json:"status"`
	Subject string `json:"subject,omitempty"`
	Detail  string `json:"detail"`
	Fix     string `json:"fix,omitempty"`
}

// Report is the whole run.
type Report struct {
	Dir     string   `json:"dir"`
	Results []Result `json:"results"`
}

// Counts returns the number of warnings and failures.
func (r *Report) Counts() (warns, fails int) {
	for _, res := range r.Results {
		switch res.Status {
		case Warn:
			warns++
		case Fail:
			fails++
		}
	}
	return warns, fails
}

// ExitCode is the contract: 0 all OK, 1 warnings only, 2 any FAIL.
func (r *Report) ExitCode() int {
	warns, fails := r.Counts()
	switch {
	case fails > 0:
		return 2
	case warns > 0:
		return 1
	}
	return 0
}

// WriteJSON writes the report as JSON.
func (r *Report) WriteJSON(w io.Writer) error {
	data, err := json.MarshalIndent(r, "", "  ")
	if err != nil {
		return err
	}
	data = append(data, '\n')
	_, err = w.Write(data)
	return err
}

// Runner runs a binary and returns its combined output and exit code. An error means the binary did not start.
type Runner func(ctx context.Context, path string, args ...string) (output string, code int, err error)

// ConfReader reads a server's effective config keys over ssh. Tests replace it, so no test touches the network.
type ConfReader interface {
	EffectiveKeys(ctx context.Context, server string) (map[string]string, error)
}

// Deps are the effects the checks use.
type Deps struct {
	Probe slots.Probe
	Conf  ConfReader
	Run   Runner
	Now   func() time.Time
}

// Options configure one run.
type Options struct {
	// Dir is the walk directory that holds cmd-*.txt and targets.txt.
	Dir string
	// Servers, when not empty, limits the checks to the commands of these servers.
	Servers []string
	// SlotsPath is the slot table (default slots.DefaultPath()).
	SlotsPath string
	// KeysPath is the required-keys file (default ~/.config/turtle-agent/preflight-keys.json).
	KeysPath string
	// Binary is the turtle-agent a target that names none is checked as (a targets.txt line, or a command whose
	// binary is the bare `turtle-agent` name). Default: this executable, exactly what the autopilot launches without
	// its own --binary.
	Binary string
	Deps   Deps
}

// DefaultKeysPath is the required server keys file.
func DefaultKeysPath() string {
	home, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	return filepath.Join(home, ".config", "turtle-agent", "preflight-keys.json")
}

type check struct {
	opts    Options
	deps    Deps
	targets []target
	report  *Report

	// caches shared by the checks
	plans      map[string]*dungeon.Plan
	planErrs   map[string]error
	migrations map[string][]planexport.Rows
	migErrs    map[string]error
	rosters    map[string]*raidwalk.Roster
	rosterErrs map[string]error
	capsByDir  map[string]map[string]int
	capsErr    map[string]error
	slotCfg    *slots.Config
	slotErr    error
	slotLoaded bool
	binaries   map[string]*binInfo
}

type binInfo struct {
	help string
	ok   bool
}

// Run reads the walk directory and runs every check.
func Run(ctx context.Context, opts Options) (*Report, error) {
	dir, err := filepath.Abs(opts.Dir)
	if err != nil {
		return nil, err
	}
	if fi, err := os.Stat(dir); err != nil {
		return nil, err
	} else if !fi.IsDir() {
		return nil, fmt.Errorf("%s is not a directory", opts.Dir)
	}
	opts.Dir = dir
	if opts.SlotsPath == "" {
		opts.SlotsPath = slots.DefaultPath()
	}
	if opts.KeysPath == "" {
		opts.KeysPath = DefaultKeysPath()
	}
	if opts.Binary == "" {
		exe, err := os.Executable()
		if err != nil {
			return nil, fmt.Errorf("no --binary and this executable cannot be resolved: %w", err)
		}
		opts.Binary = exe
	}
	c := &check{
		opts:       opts,
		deps:       opts.Deps,
		report:     &Report{Dir: dir},
		plans:      map[string]*dungeon.Plan{},
		planErrs:   map[string]error{},
		migrations: map[string][]planexport.Rows{},
		migErrs:    map[string]error{},
		rosters:    map[string]*raidwalk.Roster{},
		rosterErrs: map[string]error{},
		capsByDir:  map[string]map[string]int{},
		capsErr:    map[string]error{},
		binaries:   map[string]*binInfo{},
	}
	if c.deps.Probe == nil {
		c.deps.Probe = slots.LinuxProbe{}
	}
	if c.deps.Conf == nil {
		c.deps.Conf = SSHConf{}
	}
	if c.deps.Run == nil {
		c.deps.Run = execRunner
	}
	if c.deps.Now == nil {
		c.deps.Now = time.Now
	}

	targets, errs := readTargets(dir, opts.Binary)
	for _, e := range errs {
		c.add(Fail, CheckBinary, filepath.Base(e.file), e.msg, "fix the command file")
	}
	if len(targets) == 0 {
		if len(errs) == 0 {
			c.add(Fail, CheckBinary, dir, "no cmd-*.txt or targets.txt in the walk directory", "write the walk's commands first")
		}
		return c.report, nil
	}
	if len(opts.Servers) > 0 {
		want := map[string]bool{}
		for _, s := range opts.Servers {
			want[s] = true
		}
		var kept []target
		for _, t := range targets {
			if want[t.server] {
				kept = append(kept, t)
			}
		}
		have := map[string]bool{}
		for _, t := range kept {
			have[t.server] = true
		}
		for _, s := range opts.Servers {
			if !have[s] {
				c.add(Fail, CheckBinary, s, "no command in "+dir+" names server "+s, "check --server and the walk's cmd files")
			}
		}
		targets = kept
		if len(targets) == 0 {
			return c.report, nil
		}
	}
	c.targets = targets

	c.checkBinaries(ctx)
	c.checkSchema(ctx)
	c.checkSlotsPath()
	c.checkInstanceCaps()
	c.checkLeases()
	c.checkPlacement()
	c.checkPlans()
	c.checkServerKeys(ctx)
	c.checkRosters()
	return c.report, nil
}

// execRunner is the real Runner: a binary's combined output and exit code.
func execRunner(ctx context.Context, path string, args ...string) (string, int, error) {
	cmd := exec.CommandContext(ctx, path, args...)
	out, err := cmd.CombinedOutput()
	if err == nil {
		return string(out), 0, nil
	}
	var ee *exec.ExitError
	if errors.As(err, &ee) {
		return string(out), ee.ExitCode(), nil
	}
	return string(out), 0, err
}

func (c *check) add(st Status, check, subject, detail, fix string) {
	c.report.Results = append(c.report.Results, Result{Check: check, Status: st, Subject: subject, Detail: detail, Fix: fix})
}

// expandPath resolves a path from a command line: ~ expands, a relative path is relative to the walk directory.
func (c *check) expandPath(p string) string {
	p = slots.ExpandTilde(p)
	if p == "" {
		return ""
	}
	if !filepath.IsAbs(p) {
		p = filepath.Join(c.opts.Dir, p)
	}
	return filepath.Clean(p)
}

// planOf loads and caches the plan JSON the walk will load: <--dir>/<plan>.json (--dir default "dungeons"). raid-walk
// joins --dir as given (it expands no ~ there), so this does too; only a relative dir is taken from the walk directory.
func (c *check) planOf(t target) (*dungeon.Plan, string, error) {
	dir := t.dir
	if !filepath.IsAbs(dir) {
		dir = filepath.Join(c.opts.Dir, dir)
	}
	path := filepath.Clean(filepath.Join(dir, t.plan+".json"))
	if p, ok := c.plans[path]; ok {
		return p, path, c.planErrs[path]
	}
	p, err := dungeon.Load(path)
	c.plans[path] = p
	c.planErrs[path] = err
	return p, path, err
}

// migrationOf parses and caches the --sql export.
func (c *check) migrationOf(path string) ([]planexport.Rows, error) {
	if rows, ok := c.migrations[path]; ok {
		return rows, c.migErrs[path]
	}
	raw, err := os.ReadFile(path)
	if err == nil {
		var rows []planexport.Rows
		rows, _, err = planexport.ParseMigration(string(raw))
		c.migrations[path] = rows
	}
	c.migErrs[path] = err
	return c.migrations[path], err
}

func (c *check) slotsConfig() (*slots.Config, error) {
	if !c.slotLoaded {
		c.slotCfg, c.slotErr = slots.Load(c.opts.SlotsPath)
		c.slotLoaded = true
	}
	return c.slotCfg, c.slotErr
}

// capsFor reads instance-cap-per-account.json beside the slot table the targets use.
func (c *check) capsFor(slotsPath string) (map[string]int, string, error) {
	dir := filepath.Dir(slotsPath)
	if caps, ok := c.capsByDir[dir]; ok {
		return caps, filepath.Join(dir, slots.InstanceCapPerAccountFile), c.capsErr[dir]
	}
	path := filepath.Join(dir, slots.InstanceCapPerAccountFile)
	caps, err := slots.LoadInstanceCaps(path)
	c.capsByDir[dir] = caps
	c.capsErr[dir] = err
	return caps, path, err
}

// binaryFor runs the checks of one binary once and caches the answers.
func (c *check) binaryFor(path string) *binInfo {
	if b, ok := c.binaries[path]; ok {
		return b
	}
	b := &binInfo{}
	c.binaries[path] = b
	return b
}

// Number formatting helpers used by the checks.
func pl(n int) string {
	if n == 1 {
		return "1 leg"
	}
	return strconv.Itoa(n) + " legs"
}

func sortedKeys(m map[string]bool) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}
