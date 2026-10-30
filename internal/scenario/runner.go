package scenario

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"sort"
	"time"

	"github.com/pjerra/turtle-agent/internal/events"
)

// RPCError is a JSON-RPC error an agent answered (as dashboard's RPCError).
type RPCError struct {
	Code    int
	Message string
}

func (e *RPCError) Error() string { return fmt.Sprintf("error %d: %s", e.Code, e.Message) }

// Caller is one session: a JSON-RPC client of a running agent.
type Caller interface {
	Call(ctx context.Context, method string, params any, timeout time.Duration) (json.RawMessage, error)
}

// Counter is the DB helper: exactly one integer comes back, never rows.
type Counter interface {
	Count(ctx context.Context, q DBQuery) (int64, error)
}

// tokenReader is implemented by callers that remember their last bearer
// token, so a reply echoing it can be redacted from the transcript.
type tokenReader interface {
	Token() string
}

// Runner holds the clients and clocks a run needs. Now and Sleep are injected
// so tests run without real-time sleeps: Sleep advances Now.
type Runner struct {
	Sessions map[string]Caller
	DB       Counter // nil when the file has no db
	Now      func() time.Time
	Sleep    func(ctx context.Context, d time.Duration) error
	Out      io.Writer // live raw lines (stdout); may be io.Discard
	Quiet    bool
}

// Result is what a run leaves: the transcript records, the check outcomes,
// the counts and the exit code (0 pass, 14 fail, 15 abort).
type Result struct {
	Scenario string
	File     string
	Started  time.Time
	Finished time.Time
	Seconds  float64
	Exit     int
	Passed   int
	Failed   int
	Errors   int
	Skipped  int
	Checks   []CheckResult
	Records  []Record
}

// sessState is the runner's per-session cursor triple plus the buffer.
type sessState struct {
	name      string
	caller    Caller
	addr      string
	character string
	cursor    uint64 // last seq fetched
	window    uint64 // seq at the last section/mark
	from      uint64 // match cursor: expects only see seq > from
	buf       []events.Event
	recorded  uint64 // max seq written to the transcript
	closed    bool
	closedAt  int
}

type run struct {
	r        *Runner
	f        *File
	vars     map[string]string
	sess     map[string]*sessState
	order    []string
	records  []Record
	checks   []CheckResult
	passed   int
	failed   int
	errs     int
	skipped  int
	sections int
	sleep    func(ctx context.Context, d time.Duration) error
	now      func() time.Time
	runCtx   context.Context
	deadline time.Time
}

func sortedKeys(m map[string]Session) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

func (r *Runner) Run(ctx context.Context, f *File) (*Result, error) {
	now := r.Now
	if now == nil {
		now = time.Now
	}
	sleep := r.Sleep
	if sleep == nil {
		sleep = func(ctx context.Context, d time.Duration) error {
			t := time.NewTimer(d)
			defer t.Stop()
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-t.C:
				return nil
			}
		}
	}
	rr := &run{r: r, f: f, vars: map[string]string{}, sess: map[string]*sessState{}, sleep: sleep, now: now}
	for k, v := range f.Vars {
		rr.vars[k] = v
	}
	res := &Result{Scenario: f.Scenario, File: f.path, Started: now()}
	runCtx, cancel := context.WithTimeout(ctx, time.Duration(f.MaxMinutes*float64(time.Minute)))
	defer cancel()
	rr.runCtx = runCtx
	rr.deadline = res.Started.Add(time.Duration(f.MaxMinutes * float64(time.Minute)))

	// 3.1 Start: status probe every session, sorted by name.
	rr.order = sortedKeys(f.Sessions)
	for _, name := range rr.order {
		sd := f.Sessions[name]
		st := &sessState{name: name, caller: r.Sessions[name], addr: sd.RPC}
		rr.sess[name] = st
		if st.caller == nil {
			rr.abort(0, fmt.Sprintf("session %s", name), fmt.Errorf("session %s: no caller (a bug in the test)", name))
			break
		}
		raw, err := st.caller.Call(ctx, "status", map[string]any{}, 10*time.Second)
		if err != nil {
			rr.errorf(0, fmt.Sprintf("session %s", name), "session %s: no answer at %s (%v): start the agent first", name, sd.RPC, err)
			res = rr.finish(res, 15)
			return res, nil
		}
		var stt struct {
			Character string `json:"character"`
			LastSeq   uint64 `json:"last_seq"`
		}
		if err := json.Unmarshal(raw, &stt); err != nil {
			rr.errorf(0, fmt.Sprintf("session %s", name), "session %s: bad status reply (%v)", name, err)
			res = rr.finish(res, 15)
			return res, nil
		}
		if stt.Character == "" {
			rr.errorf(0, fmt.Sprintf("session %s", name), "session %s: not in the world (status has no character)", name)
			res = rr.finish(res, 15)
			return res, nil
		}
		st.character = stt.Character
		st.cursor, st.window, st.from, st.recorded = stt.LastSeq, stt.LastSeq, stt.LastSeq, stt.LastSeq
		rr.vars[name+".character"] = stt.Character
	}
	if rr.errs > 0 {
		return res, nil
	}
	rr.header()
	aborted := false
	for i, s := range f.Steps {
		if ctx.Err() == nil && (runCtx.Err() != nil || !rr.now().Before(rr.deadline)) {
			rr.errorf(i+1, fmt.Sprintf("step %d (%s)", i+1, s.Do), "max_minutes passed")
			rr.skipRest(i + 1)
			aborted = true
			break
		}
		if ctx.Err() != nil {
			rr.errorf(i+1, fmt.Sprintf("step %d (%s)", i+1, s.Do), "interrupted")
			rr.skipRest(i + 1)
			aborted = true
			break
		}
		if !rr.step(runCtx, s, i+1) {
			aborted = true
			break
		}
	}
	_ = aborted
	// finally runs in every case except a failed start, under its own 60 s context.
	fctx, fcancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer fcancel()
	if len(f.Finally) > 0 {
		rr.emit(Record{T: "finally", Raw: "=== FINALLY", Norm: "=== FINALLY"})
		for i, s := range f.Finally {
			rr.finalStep(fctx, s, 1000+i)
		}
	}
	exit := 0
	switch {
	case rr.errs > 0:
		exit = 15
	case rr.failed > 0:
		exit = 14
	}
	res = rr.finish(res, exit)
	return res, nil
}

func (rr *run) finish(res *Result, exit int) *Result {
	res.Finished = rr.now()
	res.Seconds = res.Finished.Sub(res.Started).Seconds()
	res.Exit = exit
	res.Passed, res.Failed, res.Errors, res.Skipped = rr.passed, rr.failed, rr.errs, rr.skipped
	failedDescs := []string{}
	for _, c := range rr.checks {
		if c.Status == "FAIL" || c.Status == "ERROR" {
			failedDescs = append(failedDescs, c.Desc)
		}
	}
	sum := fmt.Sprintf("# summary: %d passed, %d failed, %d errors, %d skipped; exit %d",
		rr.passed, rr.failed, rr.errs, rr.skipped, exit)
	rr.emit(Record{T: "summary", Raw: "", Norm: ""})
	rr.emit(Record{T: "summary", Raw: sum, Norm: sum})
	for _, d := range failedDescs {
		line := "# failed: " + d
		rr.emit(Record{T: "summary", Raw: line, Norm: line})
	}
	fin := fmt.Sprintf("# finished %s (%.1f s)", res.Finished.Format(time.RFC3339), res.Seconds)
	rr.emit(Record{T: "summary", Raw: fin, Norm: ""})
	res.Checks = rr.checks
	res.Records = rr.records
	return res
}

func (rr *run) abort(step int, desc string, err error) {
	rr.errorf(step, desc, "%v", err)
}

// errorf records an ERROR check entry, prints it and counts it. The reason
// is redacted before it is stored: only the scrubbed Record used to reach
// result.json, so a server-controlled string (rpcText of a JSON-RPC error)
// echoing the bearer token must never land there unredacted.
func (rr *run) errorf(step int, desc, format string, args ...any) {
	reason := redactAll(fmt.Sprintf(format, args...), rr.tokens())
	rr.checks = append(rr.checks, CheckResult{Step: step, Desc: desc, Status: "ERROR", Reason: reason})
	rr.errs++
	rr.emit(Record{T: "check", Step: step, Raw: "  ERROR " + desc + ": " + reason, Norm: "  ERROR " + desc + ": " + reason, Status: "ERROR"})
}

// failf records a FAIL check entry, prints it, counts it and honours on_fail.
// Like errorf it redacts the reason before storing it in the CheckResult.
func (rr *run) failf(step int, desc, reason string, onFail string) bool {
	reason = redactAll(reason, rr.tokens())
	rr.checks = append(rr.checks, CheckResult{Step: step, Desc: desc, Status: "FAIL", Reason: reason})
	rr.failed++
	rr.emit(Record{T: "check", Step: step, Raw: "  FAIL " + desc + ": " + reason, Norm: "  FAIL " + desc + ": " + reason, Status: "FAIL"})
	return onFail != "stop"
}

// skipRest prints SKIP for every remaining main step.
func (rr *run) skipRest(from int) {
	for i := from; i <= len(rr.f.Steps); i++ {
		s := rr.f.Steps[i-1]
		line := fmt.Sprintf("  SKIP step %d (%s)", i, s.Do)
		rr.emit(Record{T: "skip", Step: i, Raw: line, Norm: line})
		rr.skipped++
	}
}

func (rr *run) header() {
	started := rr.now()
	head := fmt.Sprintf("# scenario %s (%s), started %s", rr.f.Scenario, rr.f.path, started.Format(time.RFC3339))
	rr.emit(Record{T: "header", Raw: head, Norm: "# scenario " + rr.f.Scenario})
	if rr.f.Description != "" {
		rr.emit(Record{T: "header", Raw: "# " + rr.f.Description, Norm: "# " + rr.f.Description})
	}
	for _, name := range rr.order {
		st := rr.sess[name]
		raw := fmt.Sprintf("# session %s: %s rpc %s", name, st.character, st.addr)
		rr.emit(Record{T: "session", Session: name, Raw: raw, Norm: fmt.Sprintf("# session %s: %s", name, st.character)})
	}
}

// tokens collects the current bearer token of every session for redaction.
func (rr *run) tokens() []string {
	tokens := []string{}
	for _, st := range rr.sess {
		if tr, ok := st.caller.(tokenReader); ok {
			if tok := tr.Token(); tok != "" {
				tokens = append(tokens, tok)
			}
		}
	}
	return tokens
}

// emit stores a record (redacting tokens) and prints its raw line live.
func (rr *run) emit(rec Record) {
	tokens := rr.tokens()
	rec.Raw = redactAll(rec.Raw, tokens)
	rec.Norm = redactAll(rec.Norm, tokens)
	if len(rec.Got) > 0 {
		rec.Got = redactJSON(rec.Got, tokens)
	}
	if rec.Event != nil {
		cp := *rec.Event
		redactEvent(&cp, tokens)
		rec.Event = &cp
	}
	rr.records = append(rr.records, rec)
	if rr.r.Quiet {
		switch rec.T {
		case "check", "skip", "summary":
		default:
			return
		}
	}
	if rr.r.Out != nil && rec.Raw != "" {
		fmt.Fprintln(rr.r.Out, rec.Raw)
	} else if rr.r.Out != nil {
		fmt.Fprintln(rr.r.Out)
	}
}

// step runs one main step. It reports whether the run continues: false means
// abort (ERROR, or FAIL with on_fail stop; the rest is SKIP).
func (rr *run) step(ctx context.Context, s *Step, n int) bool {
	if s.Note != "" {
		rr.emit(Record{T: "note", Step: n, Raw: "  # " + s.Note, Norm: "  # " + s.Note})
	}
	switch s.Do {
	case "section":
		if err := rr.doSection(ctx, s, n); err != nil {
			rr.errorf(n, fmt.Sprintf("step %d (%s)", n, s.Do), "%v", err)
			rr.skipRest(n + 1)
			return false
		}
		return true
	case "mark":
		if err := rr.doMark(ctx, s, n); err != nil {
			rr.errorf(n, fmt.Sprintf("step %d (%s)", n, s.Do), "%v", err)
			rr.skipRest(n + 1)
			return false
		}
		return true
	case "gm":
		return rr.doGM(ctx, s, n, false)
	case "bot":
		return rr.doBot(ctx, s, n, false)
	case "addon_send":
		return rr.doAddonSend(ctx, s, n, false)
	case "rpc":
		return rr.doRPC(ctx, s, n, false)
	case "sleep":
		return rr.doSleep(ctx, s, n)
	case "wait":
		return rr.doWait(ctx, s, n)
	case "expect":
		return rr.doExpect(ctx, s, n)
	case "expect_not":
		return rr.doExpectNot(ctx, s, n)
	case "check_db":
		return rr.doCheckDB(ctx, s, n)
	case "check_rpc":
		return rr.doCheckRPC(ctx, s, n)
	default:
		rr.errorf(n, fmt.Sprintf("step %d (%s)", n, s.Do), "bad do %q", s.Do)
		rr.skipRest(n + 1)
		return false
	}
}

// onFailOf resolves a check's effective on_fail.
func (rr *run) onFailOf(s *Step) string {
	if s.OnFail != "" {
		return s.OnFail
	}
	return rr.f.OnFail
}

// descOf builds a check's norm-form description with its [name] prefix.
func (rr *run) descOf(s *Step, body string) string {
	if s.Name != "" {
		return "[" + s.Name + "] " + body
	}
	return body
}
