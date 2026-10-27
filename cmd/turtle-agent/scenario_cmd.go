package main

import (
	"bytes"
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/user"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/pjerra/turtle-agent/internal/rpc"
	"github.com/pjerra/turtle-agent/internal/scenario"
)

// exitScenarioFail (14) and exitScenarioAbort (15): the run's checks failed
// (14, no error), or the run errored (15, wins over 14). 11, 12 and 13 are
// taken by plan-check, route-oracle and stage-check; 3-10 are login codes.
const (
	exitScenarioFail  = 14
	exitScenarioAbort = 15
)

const scenarioUsage = `usage:
  turtle-agent scenario check [--var NAME=VALUE]... <file>
  turtle-agent scenario run [--out DIR] [--force] [--quiet] [--var NAME=VALUE]... [--session NAME=ADDR[@TOKENFILE]]... <file>
  turtle-agent scenario norm <file> <dir>`

const scenarioCheckUsage = "usage: turtle-agent scenario check [--var NAME=VALUE]... <file>"
const scenarioRunUsage = "usage: turtle-agent scenario run [--out DIR] [--force] [--quiet] [--var NAME=VALUE]... [--session NAME=ADDR[@TOKENFILE]]... <file>"
const scenarioNormUsage = "usage: turtle-agent scenario norm <file> <dir>"

type varFlag map[string]string

func (v varFlag) String() string { return fmt.Sprint(map[string]string(v)) }
func (v varFlag) Set(s string) error {
	k, val, ok := strings.Cut(s, "=")
	if !ok {
		return fmt.Errorf("want NAME=VALUE, got %q", s)
	}
	v[k] = val
	return nil
}

type sessionFlag map[string]string

func (v sessionFlag) String() string { return fmt.Sprint(map[string]string(v)) }
func (v sessionFlag) Set(s string) error {
	k, val, ok := strings.Cut(s, "=")
	if !ok {
		return fmt.Errorf("want NAME=ADDR[@TOKENFILE], got %q", s)
	}
	v[k] = val
	return nil
}

// scenarioCmd is the scenario subcommand: check, run or norm a scenario file.
func scenarioCmd(ctx context.Context, args []string, stdout, stderr io.Writer) int {
	if len(args) == 0 {
		fmt.Fprintln(stderr, scenarioUsage)
		return exitUsage
	}
	switch args[0] {
	case "check":
		return scenarioCheckCmd(args[1:], stdout, stderr)
	case "run":
		return scenarioRunCmd(ctx, args[1:], stdout, stderr)
	case "norm":
		return scenarioNormCmd(args[1:], stdout, stderr)
	default:
		fmt.Fprintln(stderr, scenarioUsage)
		return exitUsage
	}
}

func scenarioCheckCmd(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("scenario check", flag.ContinueOnError)
	fs.SetOutput(stderr)
	vars := varFlag{}
	fs.Var(vars, "var", "override a file var (repeatable)")
	if err := fs.Parse(args); err != nil {
		return exitUsage
	}
	if fs.NArg() != 1 {
		fmt.Fprintln(stderr, scenarioCheckUsage)
		return exitUsage
	}
	f, err := scenario.Load(fs.Arg(0), vars)
	if err != nil {
		fmt.Fprintf(stderr, "scenario: %v\n", err)
		return exitUsage
	}
	n := 0
	for _, s := range f.Steps {
		n++
		fmt.Fprintf(stdout, "%d %s %s\n", n, s.Do, scenarioShort(s))
	}
	return 0
}

func scenarioShort(s *scenario.Step) string {
	switch s.Do {
	case "section":
		return s.Title
	case "gm":
		return s.Text
	case "bot":
		return s.Command
	case "addon_send":
		return s.Channel + " " + s.Text
	case "rpc", "check_rpc":
		raw, _ := json.Marshal(s.Params)
		return s.Method + " " + string(raw)
	case "sleep":
		return fmt.Sprintf("%g s", s.Seconds)
	case "wait", "expect", "expect_not":
		return s.Session + " /" + s.Match + "/"
	case "check_db":
		if s.Table != "" {
			return s.Table
		}
		return "item"
	default:
		return ""
	}
}

func scenarioRunCmd(ctx context.Context, args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("scenario run", flag.ContinueOnError)
	fs.SetOutput(stderr)
	out := fs.String("out", "", "output directory (default scenario-out/<scenario>-<UTC yyyymmddThhmmss>)")
	force := fs.Bool("force", false, "write into a non-empty --out")
	quiet := fs.Bool("quiet", false, "print only PASS/FAIL/ERROR/SKIP lines and the summary")
	vars := varFlag{}
	fs.Var(vars, "var", "override a file var (repeatable)")
	sess := sessionFlag{}
	fs.Var(sess, "session", "override a session's address and token file (repeatable)")
	if err := fs.Parse(args); err != nil {
		return exitUsage
	}
	if fs.NArg() != 1 {
		fmt.Fprintln(stderr, scenarioRunUsage)
		return exitUsage
	}
	file := fs.Arg(0)
	f, err := scenario.Load(file, vars)
	if err != nil {
		fmt.Fprintf(stderr, "scenario: %v\n", err)
		return exitUsage
	}
	// --session overrides: NAME=ADDR[@TOKENFILE]; the name must exist.
	for name, val := range sess {
		sd, ok := f.Sessions[name]
		if !ok {
			fmt.Fprintf(stderr, "scenario: --session %s: the scenario has no session %s\n", name, name)
			return exitUsage
		}
		addr, tokFile, _ := strings.Cut(val, "@")
		sd.RPC = addr
		if tokFile != "" {
			sd.TokenFile = tokFile
		}
		f.Sessions[name] = sd
	}
	outDir := *out
	if outDir == "" {
		outDir = filepath.Join("scenario-out", f.Scenario+"-"+time.Now().UTC().Format("20060102T150405"))
	}
	if err := os.MkdirAll(outDir, 0o755); err != nil {
		fmt.Fprintf(stderr, "scenario: %v\n", err)
		return exitUsage
	}
	entries, err := os.ReadDir(outDir)
	if err != nil {
		fmt.Fprintf(stderr, "scenario: %v\n", err)
		return exitUsage
	}
	if len(entries) > 0 && !*force {
		fmt.Fprintf(stderr, "scenario: %s exists and is not empty (pass --force to write into it)\n", outDir)
		return exitUsage
	}
	r := &scenario.Runner{Sessions: map[string]scenario.Caller{}, Out: stdout, Quiet: *quiet}
	if f.DB != nil {
		r.DB = scenario.SSHDB{Host: f.DB.Host, Container: f.DB.Container}
	}
	names := make([]string, 0, len(f.Sessions))
	for name := range f.Sessions {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		sd := f.Sessions[name]
		tokFile := sd.TokenFile
		if tokFile == "" {
			tokFile = defaultScenarioTokenFile(sd.RPC)
		} else if strings.HasPrefix(tokFile, "~/") {
			if u, err := user.Current(); err == nil {
				tokFile = filepath.Join(u.HomeDir, strings.TrimPrefix(tokFile, "~/"))
			} else if home := os.Getenv("HOME"); home != "" {
				tokFile = filepath.Join(home, strings.TrimPrefix(tokFile, "~/"))
			}
		}
		r.Sessions[name] = &scenario.HTTPCaller{Addr: sd.RPC, TokenFile: tokFile, HTTP: &http.Client{Transport: &http.Transport{DisableKeepAlives: true}}}
	}
	res, err := r.Run(ctx, f)
	if err != nil {
		fmt.Fprintf(stderr, "scenario: %v\n", err)
		return exitScenarioAbort
	}
	writeScenarioOut(outDir, f, res)
	switch res.Exit {
	case 0:
		return 0
	case 14:
		return exitScenarioFail
	default:
		return exitScenarioAbort
	}
}

// defaultScenarioTokenFile is rpc.TokenFile's layout: rpc-<port>.token next
// to the settings file, defaulting to ~/.config/turtle-agent.
func defaultScenarioTokenFile(addr string) string {
	_, port, err := net.SplitHostPort(addr)
	if err != nil || port == "" {
		return ""
	}
	home := os.Getenv("HOME")
	if u, err := user.Current(); err == nil {
		home = u.HomeDir
	}
	return filepath.Join(home, ".config", "turtle-agent", "rpc-"+port+".token")
}

// writeScenarioOut writes raw.txt, norm.txt, transcript.jsonl and result.json.
func writeScenarioOut(dir string, f *scenario.File, res *scenario.Result) {
	raw := scenario.RenderRaw(res.Records)
	norm := scenario.RenderNorm(res.Records, f.Normalise)
	_ = os.WriteFile(filepath.Join(dir, "raw.txt"), []byte(strings.Join(raw, "\n")+"\n"), 0o644)
	_ = os.WriteFile(filepath.Join(dir, "norm.txt"), []byte(strings.Join(norm, "\n")+"\n"), 0o644)
	var jl bytes.Buffer
	for _, rec := range res.Records {
		raw, _ := json.Marshal(rec)
		jl.Write(raw)
		jl.WriteByte('\n')
	}
	_ = os.WriteFile(filepath.Join(dir, "transcript.jsonl"), jl.Bytes(), 0o644)
	checks := []map[string]any{}
	for _, c := range res.Checks {
		m := map[string]any{"step": c.Step, "desc": c.Desc, "status": c.Status}
		if len(c.Got) > 0 {
			m["got"] = json.RawMessage(c.Got)
		}
		if c.Reason != "" {
			m["reason"] = c.Reason
		}
		checks = append(checks, m)
	}
	result := map[string]any{
		"scenario": f.Scenario, "file": res.File,
		"started": res.Started.Format(time.RFC3339), "finished": res.Finished.Format(time.RFC3339),
		"seconds": res.Seconds, "exit": res.Exit, "passed": res.Passed, "failed": res.Failed,
		"errors": res.Errors, "skipped": res.Skipped, "checks": checks,
	}
	out, _ := json.MarshalIndent(result, "", "  ")
	_ = os.WriteFile(filepath.Join(dir, "result.json"), append(out, '\n'), 0o644)
}

func scenarioNormCmd(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("scenario norm", flag.ContinueOnError)
	fs.SetOutput(stderr)
	if err := fs.Parse(args); err != nil {
		return exitUsage
	}
	if fs.NArg() != 2 {
		fmt.Fprintln(stderr, scenarioNormUsage)
		return exitUsage
	}
	f, err := scenario.Load(fs.Arg(0), nil)
	if err != nil {
		fmt.Fprintf(stderr, "scenario: %v\n", err)
		return exitUsage
	}
	dir := fs.Arg(1)
	raw, err := os.ReadFile(filepath.Join(dir, "transcript.jsonl"))
	if err != nil {
		fmt.Fprintf(stderr, "scenario: %v\n", err)
		return exitUsage
	}
	recs := []scenario.Record{}
	for _, line := range strings.Split(string(raw), "\n") {
		if strings.TrimSpace(line) == "" {
			continue
		}
		var rec scenario.Record
		if err := json.Unmarshal([]byte(line), &rec); err != nil {
			fmt.Fprintf(stderr, "scenario: %v\n", err)
			return exitUsage
		}
		// Re-apply the file's (possibly changed) rules to the stored norms.
		rec.Norm = scenario.NormaliseOne(rec.Norm, f.Normalise)
		recs = append(recs, rec)
	}
	norm := scenario.RenderNorm(recs, f.Normalise)
	// RenderNorm would normalise twice; the records already carry the fresh
	// single-pass norms, so join their Norm fields directly.
	lines := make([]string, 0, len(recs))
	for _, r := range recs {
		if r.Norm == "" {
			continue
		}
		lines = append(lines, r.Norm)
	}
	_ = norm
	_ = os.WriteFile(filepath.Join(dir, "norm.txt"), []byte(strings.Join(lines, "\n")+"\n"), 0o644)
	fmt.Fprintln(stdout, filepath.Join(dir, "norm.txt"))
	return 0
}

var _ = rpc.ReadTokenFile
