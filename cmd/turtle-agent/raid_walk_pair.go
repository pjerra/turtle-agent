// raid_walk_pair.go: two raids at once, `raid-walk <plan> [<plan2>] --slot A,B` (spec raid-walk-addons 4). It splits
// --slot A,B into two ordinary walks, each with its own slot, lease, agent, dashboard client and output dir, and runs
// them in two goroutines under one context. Nothing here is a second walk implementation: both halves call
// raidWalkRun (the package var, so tests fake it) with the same raidWalkOptions the single walk parses.
package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"net/url"
	"os"
	"path/filepath"
	"reflect"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/pjerra/turtle-agent/internal/raidwalk"
	"github.com/pjerra/turtle-agent/internal/slots"
)

func init() { raidWalkPairEntry = raidWalkPairCmd }

// raidWalkPairRun is the walk each half runs; tests replace it to capture the options.
var raidWalkPairRun = raidWalkRun

// raidWalkPairEnv binds each half to its own slots.Store and its own lease (spec 4.1). Tests replace it with fakes.
var raidWalkPairEnv = defaultWalkEnv

// raidWalkPairSleep is the stagger wait between the two starts; tests replace it to record the wait.
var raidWalkPairSleep = sleepCtx

// pairUsage is the pair form's own usage line.
const pairUsage = `usage: turtle-agent raid-walk <plan> [<plan2>] --slot A,B [--stagger 30s] [--out DIR] [walk flags]
    per-walk flags take a slot prefix: --sql A=a.sql --sql B=b.sql (--sql --hires --dir --patrols --until --from
    --tier --tier-override --holder --inject); every other raid-walk flag is global`

// pairSlotRe is a slot id: the slots config's own shape.
var pairSlotRe = regexp.MustCompile(`^[A-Za-z0-9]{1,8}$`)

// pairDigitsRe tells a slot-prefix attempt from a value that merely holds '=' (an inject spec, a path).
var pairDigitsRe = regexp.MustCompile(`^[0-9]+$`)

// pairPerWalkFlags are the raid-walk flags a slot prefix may narrow to one walk (spec 4.1).
var pairPerWalkFlags = map[string]bool{
	"sql": true, "hires": true, "dir": true, "patrols": true, "until": true, "from": true,
	"tier": true, "tier-override": true, "holder": true, "inject": true,
}

// pairFlag is one flag of the pair command line, before distribution to the walks.
type pairFlag struct {
	name  string
	value string
}

// pairOptions are the parsed pair command line.
type pairOptions struct {
	walks   []*pairWalk
	out     string
	stagger time.Duration
	dryRun  bool
}

// pairWalk is one half: a slot, a plan and the raid-walk options they become.
type pairWalk struct {
	slot   string
	plan   string
	prefix string
	outDir string
	opts   raidWalkOptions
}

// linePrefixWriter labels every line of one walk with its slot and plan, writing whole lines under one mutex shared
// by both walks per underlying writer (spec 4.1).
type linePrefixWriter struct {
	w      io.Writer
	mu     *sync.Mutex // shared per underlying writer
	prefix string

	bufMu sync.Mutex
	buf   []byte
}

// pairLineMu is one mutex per underlying writer, so both walks' labelled lines never interleave.
var pairLineMu sync.Map // io.Writer -> *sync.Mutex

func prefixW(w io.Writer, prefix string) *linePrefixWriter {
	mu := &sync.Mutex{}
	if reflect.TypeOf(w) != nil && reflect.TypeOf(w).Comparable() {
		if v, ok := pairLineMu.LoadOrStore(w, mu); ok {
			mu = v.(*sync.Mutex)
		}
	}
	return &linePrefixWriter{w: w, mu: mu, prefix: prefix}
}

func (l *linePrefixWriter) Write(b []byte) (int, error) {
	l.bufMu.Lock()
	l.buf = append(l.buf, b...)
	var err error
	for {
		i := bytes.IndexByte(l.buf, '\n')
		if i < 0 {
			break
		}
		line := string(l.buf[:i])
		l.buf = append([]byte(nil), l.buf[i+1:]...)
		l.mu.Lock()
		_, err = fmt.Fprintf(l.w, "%s%s\n", l.prefix, line)
		l.mu.Unlock()
		if err != nil {
			break
		}
	}
	l.bufMu.Unlock()
	return len(b), err
}

// Flush writes a partial last line (a walk that ended mid-line still gets its prefix).
func (l *linePrefixWriter) Flush() {
	l.bufMu.Lock()
	defer l.bufMu.Unlock()
	if len(l.buf) == 0 {
		return
	}
	line := string(l.buf)
	l.buf = nil
	l.mu.Lock()
	fmt.Fprintf(l.w, "%s%s\n", l.prefix, line)
	l.mu.Unlock()
}

// raidWalkPairCmd is the two-raid entry (raidWalkPairEntry).
func raidWalkPairCmd(ctx context.Context, args []string, stdout, stderr io.Writer) int {
	o, code := parsePairArgs(args, stderr)
	if code != exitOK {
		return code
	}
	if o.dryRun {
		codes := make([]int, len(o.walks))
		for i, w := range o.walks {
			pre := prefixW(stdout, w.prefix)
			perr := prefixW(stderr, w.prefix)
			codes[i] = raidWalkPairRun(ctx, w.opts, walkEnv{}, walkDeps{}, pre, perr)
			pre.Flush()
			perr.Flush()
		}
		return pairWorstExit(codes...)
	}

	out := slots.ExpandTilde(o.out)
	if _, err := os.Lstat(out); err == nil {
		fmt.Fprintf(stderr, "raid-walk: --out %s exists: pick a new dir\n", out)
		return exitUsage
	} else if !os.IsNotExist(err) {
		fmt.Fprintf(stderr, "raid-walk: --out %s: %v\n", out, err)
		return exitUsage
	}
	if err := os.MkdirAll(out, 0o700); err != nil {
		fmt.Fprintf(stderr, "raid-walk: --out %s: %v\n", out, err)
		return exitUsage
	}
	_ = os.Chmod(out, 0o700)

	ctx, cancel := context.WithCancel(ctx)
	defer cancel()

	started := time.Now()
	entries := make([]*pairWalk, len(o.walks))
	results := make([]pairResult, len(o.walks))
	outW := make([]*linePrefixWriter, len(o.walks))
	errW := make([]*linePrefixWriter, len(o.walks))
	var notesMu sync.Mutex
	var notes []string
	addNote := func(format string, a ...any) {
		notesMu.Lock()
		notes = append(notes, fmt.Sprintf(format, a...))
		notesMu.Unlock()
	}
	for i, w := range o.walks {
		entries[i] = w
		w.outDir = filepath.Join(out, fmt.Sprintf("s%s-%s", w.slot, w.plan))
		w.opts.out = w.outDir
		outW[i] = prefixW(stdout, w.prefix)
		errW[i] = prefixW(stderr, w.prefix)
	}

	var wg sync.WaitGroup
	run := func(i int) {
		w := entries[i]
		var clean *pairWalkCleanup
		defer wg.Done()
		// The recover is the pair's boundary: a panic in either half becomes that half's failure, stops the other
		// walk (the shared cancel), and still runs the deferred cleanup before the report is written.
		defer func() {
			if r := recover(); r != nil {
				results[i] = pairResult{
					code:   exitRaidWalkFailed,
					reason: fmt.Sprintf("panic: %v", r),
					sum:    readPairSummary(w.outDir),
				}
				addNote("s%s %s: panic: %v", w.slot, w.plan, r)
				cancel()
				outW[i].Flush()
				errW[i].Flush()
			}
			if clean != nil {
				for _, left := range clean.cleanup(w.outDir) {
					addNote("s%s %s: %s", w.slot, w.plan, left)
				}
			}
		}()
		env, deps, err := raidWalkPairEnv(&w.opts)
		if err != nil {
			results[i].code = exitUsage
			addNote("s%s %s: %v", w.slot, w.plan, err)
			return
		}
		clean, env = newPairWalkCleanup(env)
		code := raidWalkPairRun(ctx, w.opts, env, deps, outW[i], errW[i])
		results[i] = pairResult{code: code, sum: readPairSummary(w.outDir)}
	}

	wg.Add(1)
	go run(0)
	skipSecond := false
	if o.stagger > 0 {
		if err := raidWalkPairSleep(ctx, o.stagger); err != nil {
			skipSecond = true
			results[1].code = exitRaidWalkFailed
			results[1].reason = "interrupted"
			addNote("s%s %s: not started (interrupted during the stagger)", entries[1].slot, entries[1].plan)
		}
	}
	if !skipSecond {
		wg.Add(1)
		go run(1)
	}
	wg.Wait()
	for i := range outW {
		outW[i].Flush()
		errW[i].Flush()
	}

	codes := make([]int, len(results))
	for i := range results {
		codes[i] = results[i].code
	}
	code = pairWorstExit(codes...)
	ended := time.Now()

	rep := pairReport{
		Format: "tb-raid-walk-pair", Version: 1,
		Started: started, Ended: ended, StaggerS: o.stagger.Seconds(),
		Walks:    make([]pairWalkReport, 0, len(entries)),
		ExitCode: code, Notes: append([]string{}, notes...),
	}
	for i, w := range entries {
		rep.Walks = append(rep.Walks, pairWalkReportOf(w, results[i]))
	}
	rep.OverlapS = pairOverlap(results[0].sum, results[1].sum)
	rep.DashboardBothListed = pairDashboardBoth(rep.Walks, entries)

	text := rep.Text()
	if raw, err := json.MarshalIndent(rep, "", "  "); err == nil {
		if err := os.WriteFile(filepath.Join(out, "pair.json"), append(raw, '\n'), 0o600); err != nil {
			fmt.Fprintf(stderr, "raid-walk: pair.json: %v\n", err)
		}
	}
	if err := os.WriteFile(filepath.Join(out, "pair.txt"), []byte(text), 0o600); err != nil {
		fmt.Fprintf(stderr, "raid-walk: pair.txt: %v\n", err)
	}
	fmt.Fprint(stdout, text)
	return code
}

// pairResult is one half's outcome.
type pairResult struct {
	code   int
	reason string // a fallback end reason when the walk wrote no summary
	sum    *raidwalk.Summary
}

// pairWalkCleanup is one half's panic-safe cleanup. It wraps the half's environment so the lease the walk
// acquired is known to the pair. A step is marked done only after it confirmed success (release returned nil;
// teardown exited 0 with a --json report that parses), so a panic or a failure leaves it incomplete and the
// deferred cleanup retries it once. A panic in one step never skips the other.
type pairWalkCleanup struct {
	env      walkEnv
	release  func(server, slot, holder, leaseID string, force bool) (slots.Lease, error)
	teardown func(args []string, stdout, stderr io.Writer) int
	lease    slots.Lease
	released bool // confirmed: teardown's report says the lease went back, or release returned nil
	tornDown bool // confirmed: teardown exited 0 and wrote a report that parses
}

// pairTeardownResult is one teardown attempt: its exit code, and the --json report when it was asked for.
type pairTeardownResult struct {
	code   int
	report teardownJSON
	ok     bool   // the --json report parsed
	detail string // the first stderr line, for the report
}

// confirmed is teardown's success: exit 0 and, when --json was asked for, a report that parses.
func (r pairTeardownResult) confirmed() bool { return r.code == exitOK && r.ok }

// released is true when a confirmed teardown's own lease step says the lease was given back.
func (r pairTeardownResult) released() bool {
	for _, s := range r.report.Steps {
		if s.Step == "lease" {
			return s.Status == "ok"
		}
	}
	return false
}

// failureText says why a teardown did not confirm, for the pair report.
func (r pairTeardownResult) failureText() string {
	if r.code == exitOK {
		return "teardown wrote no valid --json report"
	}
	detail := r.detail
	if detail == "" && r.ok {
		var fails []string
		for _, s := range r.report.Steps {
			if s.Status == "fail" || s.Status == "ending" {
				fails = append(fails, s.Step+": "+s.Detail)
			}
		}
		detail = strings.Join(fails, "; ")
	}
	if detail == "" {
		return fmt.Sprintf("teardown failed (exit %d)", r.code)
	}
	return fmt.Sprintf("teardown failed (exit %d): %s", r.code, detail)
}

// newPairWalkCleanup wraps env and returns the cleanup with the environment the walk must use.
func newPairWalkCleanup(env walkEnv) (*pairWalkCleanup, walkEnv) {
	c := &pairWalkCleanup{env: env, release: env.release, teardown: env.teardown}
	if acquire := env.acquire; acquire != nil {
		c.env.acquire = func(server, slot string, any bool, holder string, ttl time.Duration) (slots.Acquired, error) {
			acq, err := acquire(server, slot, any, holder, ttl)
			if err == nil {
				c.lease = acq.Lease
			}
			return acq, err
		}
	}
	if release := c.release; release != nil {
		c.env.release = func(server, slot, holder, leaseID string, force bool) (slots.Lease, error) {
			l, err := release(server, slot, holder, leaseID, force)
			// Confirmed success only: a panic or an error leaves released false, so cleanup retries it once.
			if err == nil {
				c.released = true
			}
			return l, err
		}
	}
	if teardown := c.teardown; teardown != nil {
		c.env.teardown = func(args []string, stdout, stderr io.Writer) int {
			res := pairCallTeardown(teardown, args, stdout, stderr)
			if res.confirmed() {
				c.tornDown = true
				if res.released() {
					c.released = true
				}
			}
			return res.code
		}
	}
	return c, c.env
}

// pairArgsHaveJSON reports whether a teardown call asked for its JSON report.
func pairArgsHaveJSON(args []string) bool {
	for _, a := range args {
		if a == "--json" {
			return true
		}
	}
	return false
}

// pairCallTeardown runs teardown, capturing the report it writes when --json was asked for. A panic is not
// recovered here: it must reach the walk (or cleanup's own guard) with the completion flags still unset.
func pairCallTeardown(teardown func(args []string, stdout, stderr io.Writer) int, args []string, stdout, stderr io.Writer) pairTeardownResult {
	var report bytes.Buffer
	asJSON := pairArgsHaveJSON(args)
	if asJSON {
		stdout = io.MultiWriter(stdout, &report)
	}
	res := pairTeardownResult{code: teardown(args, stdout, stderr)}
	if asJSON {
		res.report, res.ok = parseTeardownJSON(report.Bytes())
	}
	return res
}

// cleanup ends what a panicked or failed walk left behind: its slot dir (agent, files) and, when teardown could
// not reach the lease, the lease itself. It runs at most once more here, so a step that fails again is reported
// rather than retried forever; every step is guarded, so one panic never skips the other. What is still held
// comes back as one report line.
func (c *pairWalkCleanup) cleanup(outDir string) []string {
	if c.lease.Slot == "" {
		return nil
	}
	var problems []string
	if !c.tornDown && c.teardown != nil {
		res, panicked := pairSafeTeardown(c.teardown, []string{"--json", filepath.Join(outDir, "slot")})
		switch {
		case panicked != "":
			problems = append(problems, "teardown panicked: "+panicked)
		case res.confirmed():
			c.tornDown = true
			if res.released() {
				c.released = true
			}
		default:
			problems = append(problems, res.failureText())
		}
	}
	if !c.released && c.release != nil {
		err, panicked := pairSafeRelease(c.release, c.lease)
		switch {
		case panicked != "":
			problems = append(problems, "release panicked: "+panicked)
		case err != nil:
			problems = append(problems, "release failed: "+err.Error())
		default:
			c.released = true
		}
	}
	if c.released {
		return nil
	}
	note := fmt.Sprintf("lease %s/%s %s (holder %s) was left held", c.lease.Server, c.lease.Slot, c.lease.LeaseID, c.lease.Holder)
	if len(problems) > 0 {
		note += ": " + strings.Join(problems, "; ")
	}
	return []string{note}
}

// pairSafeTeardown is one guarded teardown attempt for cleanup: a panic becomes a message.
func pairSafeTeardown(teardown func(args []string, stdout, stderr io.Writer) int, args []string) (res pairTeardownResult, panicked string) {
	var out, errb bytes.Buffer
	defer func() {
		if r := recover(); r != nil {
			panicked = fmt.Sprint(r)
		}
	}()
	res = pairCallTeardown(teardown, args, &out, &errb)
	if res.code != exitOK {
		if d := strings.TrimSpace(errb.String()); d != "" {
			res.detail = firstLine(d)
		}
	}
	return res, ""
}

// pairSafeRelease is one guarded release attempt for cleanup: a panic becomes a message.
func pairSafeRelease(release func(server, slot, holder, leaseID string, force bool) (slots.Lease, error), l slots.Lease) (err error, panicked string) {
	defer func() {
		if r := recover(); r != nil {
			panicked = fmt.Sprint(r)
		}
	}()
	_, err = release(l.Server, l.Slot, l.Holder, l.LeaseID, false)
	return err, ""
}

// parsePairArgs reads the pair flags and turns them into two raid-walk option sets.
func parsePairArgs(args []string, stderr io.Writer) (pairOptions, int) {
	usage := func(format string, a ...any) (pairOptions, int) {
		fmt.Fprintf(stderr, "raid-walk: "+format+"\n", a...)
		fmt.Fprintln(stderr, pairUsage)
		return pairOptions{}, exitUsage
	}

	var flags []pairFlag
	var positionals []string
	var slotVal, outVal, staggerVal string
	haveSlot, haveOut, haveStagger := false, false, false
	dryRun := false
	for i := 0; i < len(args); i++ {
		a := args[i]
		if a == "--" {
			positionals = append(positionals, args[i+1:]...)
			break
		}
		if !strings.HasPrefix(a, "--") {
			positionals = append(positionals, a)
			continue
		}
		name := strings.TrimPrefix(a, "--")
		val := ""
		has := false
		if j := strings.IndexByte(name, '='); j >= 0 {
			name, val, has = name[:j], name[j+1:], true
		}
		if name == "dry-run" {
			if has {
				return usage("--dry-run takes no value")
			}
			dryRun = true
			continue
		}
		if !has {
			if i+1 >= len(args) || strings.HasPrefix(args[i+1], "--") {
				return usage("--%s needs a value", name)
			}
			i++
			val, has = args[i], true
		}
		switch name {
		case "slot":
			slotVal, haveSlot = val, true
		case "out":
			outVal, haveOut = val, true
		case "stagger":
			staggerVal, haveStagger = val, true
		default:
			flags = append(flags, pairFlag{name: name, value: val})
		}
	}
	if !haveSlot {
		return usage("the pair form needs --slot A,B")
	}
	oa := pairOptions{out: outVal, dryRun: dryRun, stagger: 30 * time.Second}
	if !haveOut {
		oa.out = fmt.Sprintf("raid-walk-pair-%s", time.Now().Format("20060102-150405"))
	}
	parts := strings.Split(slotVal, ",")
	if len(parts) != 2 || parts[0] == "" || parts[1] == "" || parts[0] == parts[1] ||
		!pairSlotRe.MatchString(parts[0]) || !pairSlotRe.MatchString(parts[1]) {
		return usage("--slot wants exactly two distinct slot ids, not %q", slotVal)
	}
	slotA, slotB := parts[0], parts[1]
	// The owner rule, before anything else (raid-walk never uses slot 3).
	if slotA == "3" || slotB == "3" {
		fmt.Fprintln(stderr, "raid-walk never uses slot 3 (owner rule)")
		return pairOptions{}, exitSlotRefused
	}
	if haveStagger {
		d, err := time.ParseDuration(staggerVal)
		if err != nil || d < 0 || d > 600*time.Second {
			return usage("--stagger wants 0 to 600s, not %q", staggerVal)
		}
		oa.stagger = d
	}
	if len(positionals) < 1 || len(positionals) > 2 {
		return usage("the pair form takes one plan (both slots) or two plans, not %d", len(positionals))
	}
	plans := []string{positionals[0], positionals[0]}
	if len(positionals) == 2 {
		plans[1] = positionals[1]
	}

	// Distribute the flags: a slot prefix narrows a per-walk flag to that walk, an unprefixed one applies to both.
	perWalk := [2][]pairFlag{}
	var global []pairFlag
	for _, f := range flags {
		value := f.value
		prefix := ""
		if i := strings.IndexByte(value, '='); i > 0 {
			cand := value[:i]
			if cand == slotA || cand == slotB {
				prefix = cand
				value = value[i+1:]
			} else if pairDigitsRe.MatchString(cand) {
				return usage("--%s carries an unknown slot prefix %q (the slots are %s and %s)", f.name, cand, slotA, slotB)
			}
		}
		if !pairPerWalkFlags[f.name] {
			if prefix != "" {
				return usage("--%s is global and takes no slot prefix", f.name)
			}
			global = append(global, pairFlag{name: f.name, value: value})
			continue
		}
		g := pairFlag{name: f.name, value: value}
		if prefix == "" || prefix == slotA {
			perWalk[0] = append(perWalk[0], g)
		}
		if prefix == "" || prefix == slotB {
			perWalk[1] = append(perWalk[1], g)
		}
	}

	oa.walks = make([]*pairWalk, 2)
	for i, slot := range []string{slotA, slotB} {
		plan := plans[i]
		walkArgs := []string{"--slot", slot, "--out", filepath.Join(oa.out, fmt.Sprintf("s%s-%s", slot, plan))}
		if dryRun {
			walkArgs = append(walkArgs, "--dry-run")
		}
		for _, f := range global {
			walkArgs = append(walkArgs, "--"+f.name, f.value)
		}
		for _, f := range perWalk[i] {
			walkArgs = append(walkArgs, "--"+f.name, f.value)
		}
		walkArgs = append(walkArgs, plan)
		opts, code := parseRaidWalkArgs(walkArgs, stderr)
		if code != exitOK {
			return pairOptions{}, code
		}
		if !pairHasFlag(perWalk[i], "holder") {
			opts.holder = fmt.Sprintf("raid-walk-%s-s%s", plan, slot)
		}
		oa.walks[i] = &pairWalk{slot: slot, plan: plan, prefix: fmt.Sprintf("[s%s %s] ", slot, plan), opts: opts}
	}
	if oa.walks[0].opts.holder == oa.walks[1].opts.holder {
		return usage("both walks would hold the lease as %q: give --holder A=... and --holder B=...", oa.walks[0].opts.holder)
	}
	return oa, exitOK
}

// pairHasFlag returns the flag (nil when absent).
func pairHasFlag(flags []pairFlag, name string) bool {
	for _, f := range flags {
		if f.name == name {
			return true
		}
	}
	return false
}

// pairWorstExit is the pair's exit: the first that occurs in this order over the walks' codes (spec 4.1).
func pairWorstExit(codes ...int) int {
	for _, p := range []int{exitUsage, exitSlotRefused, exitRaidWalkFailed, exitRaidWalkIncident, exitRaidWalkInjectFailed, exitOK} {
		for _, c := range codes {
			if c == p {
				return p
			}
		}
	}
	for _, c := range codes {
		if c != exitOK {
			return c
		}
	}
	return exitOK
}

// pairWalkReport is one walk of pair.json. Slot is the slot id as given (a slot id is alphanumeric).
type pairWalkReport struct {
	Slot         string `json:"slot"`
	Plan         string `json:"plan"`
	Character    string `json:"character"`
	Out          string `json:"out"`
	ExitCode     int    `json:"exit_code"`
	EndReason    string `json:"end_reason"`
	StepsReached int    `json:"steps_reached"`
	StepsTotal   int    `json:"steps_total"`
	Deaths       int    `json:"deaths"`
	Fights       int    `json:"fights"`
	Pauses       int    `json:"pauses"`
	Tier         int    `json:"tier"`
	Tab          string `json:"tab"`
}

// pairReport is pair.json (format tb-raid-walk-pair v1).
type pairReport struct {
	Format              string           `json:"format"`
	Version             int              `json:"version"`
	Started             time.Time        `json:"started"`
	Ended               time.Time        `json:"ended"`
	StaggerS            float64          `json:"stagger_s"`
	Walks               []pairWalkReport `json:"walks"`
	OverlapS            float64          `json:"overlap_s"`
	DashboardBothListed *bool            `json:"dashboard_both_listed"`
	ExitCode            int              `json:"exit_code"`
	Notes               []string         `json:"notes"`
}

// pairWalkReportOf fills one walk's line from its summary (or its fallback when the walk wrote none).
func pairWalkReportOf(w *pairWalk, r pairResult) pairWalkReport {
	rep := pairWalkReport{
		Slot: w.slot, Plan: w.plan, Out: w.outDir, ExitCode: r.code, EndReason: r.reason,
	}
	if r.sum == nil {
		return rep
	}
	s := r.sum
	rep.Plan = s.Plan
	rep.Character = s.Character
	rep.EndReason = s.EndReason
	rep.StepsReached, rep.StepsTotal = s.StepsReached, s.StepsTotal
	rep.Deaths, rep.Fights, rep.Pauses = s.Deaths.Total, len(s.Fights), len(s.Pauses)
	rep.Tier = s.Roster.Tier
	if s.Character != "" {
		rep.Tab = "#/commanders/" + url.PathEscape(s.Character)
	}
	return rep
}

// pairOverlap is how long both walks were between their start and their end, from the two summaries.
func pairOverlap(a, b *raidwalk.Summary) float64 {
	if a == nil || b == nil {
		return 0
	}
	start := a.Started
	if b.Started.After(start) {
		start = b.Started
	}
	end := a.Ended
	if b.Ended.Before(end) {
		end = b.Ended
	}
	if !end.After(start) {
		return 0
	}
	return math.Round(end.Sub(start).Seconds()*10) / 10
}

// pairDashboardBothListed is true when one commanders dump of either walk names both characters; null with the
// dashboard off (spec 4.1).
func pairDashboardBoth(walks []pairWalkReport, entries []*pairWalk) *bool {
	on := false
	for _, w := range entries {
		if s := readPairSummary(w.outDir); s != nil && !strings.HasPrefix(s.Dashboard, "off") {
			on = true
		}
	}
	if !on {
		return nil
	}
	names := []string{}
	for _, w := range walks {
		if w.Character != "" {
			names = append(names, w.Character)
		}
	}
	for _, w := range walks {
		if len(names) == 2 && pairDumpNamesBoth(w.Out, names[0], names[1]) {
			yes := true
			return &yes
		}
	}
	no := false
	return &no
}

// pairDumpNamesBoth reports whether one commanders-*.json dump of dir names both characters.
func pairDumpNamesBoth(dir, a, b string) bool {
	files, err := filepath.Glob(filepath.Join(dir, "commanders-*.json"))
	if err != nil {
		return false
	}
	sort.Strings(files)
	for _, path := range files {
		raw, err := os.ReadFile(path)
		if err != nil {
			continue
		}
		var v struct {
			Commanders []struct {
				Commander string `json:"commander"`
			} `json:"commanders"`
		}
		if json.Unmarshal(raw, &v) != nil {
			continue
		}
		haveA, haveB := false, false
		for _, c := range v.Commanders {
			if strings.EqualFold(c.Commander, a) {
				haveA = true
			}
			if strings.EqualFold(c.Commander, b) {
				haveB = true
			}
		}
		if haveA && haveB {
			return true
		}
	}
	return false
}

// readPairSummary reads <walk out>/summary.json (nil when the walk wrote none).
func readPairSummary(dir string) *raidwalk.Summary {
	raw, err := os.ReadFile(filepath.Join(dir, "summary.json"))
	if err != nil {
		return nil
	}
	var s raidwalk.Summary
	if json.Unmarshal(raw, &s) != nil {
		return nil
	}
	return &s
}

// Text is pair.txt: one line per walk and the pair's exit.
func (r pairReport) Text() string {
	var b strings.Builder
	for _, w := range r.Walks {
		reason := w.EndReason
		if reason == "" {
			reason = "?"
		}
		fmt.Fprintf(&b, "s%s %s: %s; steps %d/%d; deaths %d; fights %d; pauses %d; out %s; exit %d\n",
			w.Slot, w.Plan, reason, w.StepsReached, w.StepsTotal, w.Deaths, w.Fights, w.Pauses, w.Out, w.ExitCode)
	}
	for _, n := range r.Notes {
		fmt.Fprintf(&b, "note: %s\n", n)
	}
	fmt.Fprintf(&b, "pair: exit %d\n", r.ExitCode)
	return b.String()
}
