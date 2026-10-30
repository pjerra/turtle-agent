package scenario

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/pjerra/turtle-agent/internal/events"
)

// matcherDesc is the norm-form <desc> of a matcher step.
func matcherDesc(verb string, s *Step, secs float64) string {
	var b strings.Builder
	b.WriteString(verb + "[" + s.Session + "] ")
	if s.Kind != "" {
		b.WriteString(s.Kind)
	} else {
		b.WriteString("any")
	}
	if s.Channel != "" {
		b.WriteString("/" + s.Channel)
	}
	if s.Prefix != "" {
		b.WriteString(" " + s.Prefix)
	}
	b.WriteString(" /" + s.Match + "/")
	if verb == "expect_not" {
		fmt.Fprintf(&b, " for %g s", secs)
	} else {
		fmt.Fprintf(&b, " within %g s", secs)
	}
	return b.String()
}

func (rr *run) timeoutOf(s *Step) float64 {
	if s.TimeoutS > 0 {
		return s.TimeoutS
	}
	return rr.f.TimeoutS
}

// compileStep compiles a step's matcher with vars expanded (match
// regex-quoted) and the addon default prefix applied.
func (rr *run) compileStep(s *Step) (*matcher, error) {
	m := s.Matcher
	if m.Prefix == "" && m.Kind == "addon" {
		m.Prefix = rr.f.AddonPrefix
	}
	return m.compile(rr.vars)
}

// find scans the session buffer for the first event with seq > from matching m.
func (st *sessState) find(m *matcher, from uint64) *events.Event {
	for i := range st.buf {
		if st.buf[i].Seq > from && m.matches(st.buf[i]) {
			return &st.buf[i]
		}
	}
	return nil
}

// fetch pulls one events page, long-polling wait_ms (the HTTP timeout is
// wait_ms + 10 s). Empty pages advance the fake clock in tests.
func (rr *run) fetch(ctx context.Context, st *sessState, waitMS int) error {
	if st.closed {
		return nil
	}
	if waitMS > 1000 {
		waitMS = 1000
	}
	if waitMS < 1 {
		waitMS = 1
	}
	raw, err := st.caller.Call(ctx, "events",
		map[string]any{"since": st.cursor, "limit": 1000, "wait_ms": waitMS},
		time.Duration(waitMS)*time.Millisecond+10*time.Second)
	if err != nil {
		return err
	}
	var page events.Page
	if err := json.Unmarshal(raw, &page); err != nil {
		return err
	}
	st.buf = append(st.buf, page.Events...)
	if len(st.buf) > 20000 {
		st.buf = append([]events.Event(nil), st.buf[len(st.buf)-20000:]...)
	}
	st.cursor = page.Next
	return nil
}

// waitMSLeft clamps the remaining time to a 1-1000 ms fetch wait.
func waitMSLeft(remaining time.Duration) int {
	ms := int(remaining.Milliseconds())
	if ms > 1000 {
		ms = 1000
	}
	if ms < 1 {
		ms = 1
	}
	return ms
}

func (rr *run) doExpect(ctx context.Context, s *Step, n int) bool {
	timeout := rr.timeoutOf(s)
	desc := rr.descOf(s, matcherDesc("expect", s, timeout))
	m, err := rr.compileStep(s)
	if err != nil {
		rr.errorf(n, desc, "%v", err)
		rr.skipRest(n + 1)
		return false
	}
	st := rr.sess[s.Session]
	start := rr.now()
	deadline := start.Add(time.Duration(timeout * float64(time.Second)))
	for {
		if e := st.find(m, st.from); e != nil {
			dur := rr.now().Sub(start).Seconds()
			st.from = e.Seq
			rr.applyCapture(s, e)
			line := fmt.Sprintf("  PASS %s (seq %d, %.1f s)", desc, e.Seq, dur)
			norm := fmt.Sprintf("  PASS %s", desc)
			if len(s.Capture) > 0 {
				keys := make([]string, 0, len(s.Capture))
				for k := range s.Capture {
					keys = append(keys, k)
				}
				sort.Strings(keys)
				for _, k := range keys {
					line += fmt.Sprintf(" -> %s=%s", k, rr.vars[k])
					norm += fmt.Sprintf(" -> %s", k)
				}
			}
			rr.emit(Record{T: "check", Step: n, Session: s.Session, Raw: line, Norm: norm, Status: "PASS"})
			rr.checks = append(rr.checks, CheckResult{Step: n, Desc: desc, Status: "PASS"})
			rr.passed++
			rr.writeRecordedExtra(n, map[uint64]bool{e.Seq: true})
			return true
		}
		if !rr.now().Before(deadline) {
			reason := fmt.Sprintf("no matching line within %g s", timeout)
			if !rr.failf(n, desc, reason, rr.onFailOf(s)) {
				rr.skipRest(n + 1)
				return false
			}
			return true
		}
		if ferr := rr.fetch(ctx, st, waitMSLeft(deadline.Sub(rr.now()))); ferr != nil {
			rr.errorf(n, desc, "%v", ferr)
			rr.skipRest(n + 1)
			return false
		}
		if ctx.Err() != nil {
			rr.errorf(n, desc, "interrupted")
			rr.skipRest(n + 1)
			return false
		}
	}
}

func (rr *run) applyCapture(s *Step, e *events.Event) {
	if len(s.Capture) == 0 {
		return
	}
	m := s.Matcher
	if m.Prefix == "" && m.Kind == "addon" {
		m.Prefix = rr.f.AddonPrefix
	}
	pat, err := expand(m.Match, rr.vars, true)
	if err != nil {
		return
	}
	re, err := compileRegexp(pat)
	if err != nil {
		return
	}
	subs := re.FindStringSubmatch(eventLine(*e))
	for k, grp := range s.Capture {
		if grp < len(subs) {
			rr.vars[k] = subs[grp]
		}
	}
}

func (rr *run) doWait(ctx context.Context, s *Step, n int) bool {
	timeout := rr.timeoutOf(s)
	desc := matcherDesc("wait", s, timeout)
	m, err := rr.compileStep(s)
	if err != nil {
		rr.errorf(n, desc, "%v", err)
		rr.skipRest(n + 1)
		return false
	}
	st := rr.sess[s.Session]
	deadline := rr.now().Add(time.Duration(timeout * float64(time.Second)))
	for {
		if e := st.find(m, st.from); e != nil {
			dur := rr.now().Sub(deadline.Add(-time.Duration(timeout * float64(time.Second)))).Seconds()
			st.from = e.Seq
			rr.applyCapture(s, e)
			raw := fmt.Sprintf("  wait[%s] %s: ok (seq %d, %.1f s)", s.Session, s.Match, e.Seq, dur)
			norm := fmt.Sprintf("  wait[%s] %s: ok", s.Session, s.Match)
			rr.emit(Record{T: "action", Step: n, Session: s.Session, Raw: raw, Norm: norm})
			rr.writeRecordedExtra(n, map[uint64]bool{e.Seq: true})
			return true
		}
		if !rr.now().Before(deadline) {
			rr.errorf(n, desc, "no matching line within %g s", timeout)
			rr.skipRest(n + 1)
			return false
		}
		if ferr := rr.fetch(ctx, st, waitMSLeft(deadline.Sub(rr.now()))); ferr != nil {
			rr.errorf(n, desc, "%v", ferr)
			rr.skipRest(n + 1)
			return false
		}
		if ctx.Err() != nil {
			rr.errorf(n, desc, "interrupted")
			rr.skipRest(n + 1)
			return false
		}
	}
}

func (rr *run) doExpectNot(ctx context.Context, s *Step, n int) bool {
	desc := rr.descOf(s, matcherDesc("expect_not", s, s.WithinS))
	m, err := rr.compileStep(s)
	if err != nil {
		rr.errorf(n, desc, "%v", err)
		rr.skipRest(n + 1)
		return false
	}
	st := rr.sess[s.Session]
	if err := rr.drain(ctx, n); err != nil {
		rr.errorf(n, desc, "%v", err)
		rr.skipRest(n + 1)
		return false
	}
	deadline := rr.now().Add(time.Duration(s.WithinS * float64(time.Second)))
	for {
		if e := st.find(m, st.window); e != nil {
			// Redact before the cut: an event echoing the token straddling
			// the 200-char edge must not leave a fragment failf cannot match.
			line := redactAll(eventLine(*e), rr.tokens())
			if len(line) > 200 {
				line = line[:200]
			}
			reason := fmt.Sprintf("seen at seq %d: %s", e.Seq, line)
			if !rr.failf(n, desc, reason, rr.onFailOf(s)) {
				rr.skipRest(n + 1)
				return false
			}
			return true
		}
		if !rr.now().Before(deadline) {
			line := fmt.Sprintf("  PASS %s", desc)
			rr.emit(Record{T: "check", Step: n, Session: s.Session, Raw: line, Norm: line, Status: "PASS"})
			rr.checks = append(rr.checks, CheckResult{Step: n, Desc: desc, Status: "PASS"})
			rr.passed++
			return true
		}
		if ferr := rr.fetch(ctx, st, waitMSLeft(deadline.Sub(rr.now()))); ferr != nil {
			rr.errorf(n, desc, "%v", ferr)
			rr.skipRest(n + 1)
			return false
		}
		if ctx.Err() != nil {
			rr.errorf(n, desc, "interrupted")
			rr.skipRest(n + 1)
			return false
		}
	}
}

// dbDesc is the norm-form description of a check_db step (2.8).
func dbDesc(s *Step) string {
	var b strings.Builder
	b.WriteString("db ")
	if s.Table != "" {
		b.WriteString(s.Table)
		keys := make([]string, 0, len(s.Where))
		for k := range s.Where {
			keys = append(keys, k)
		}
		sortStrings(keys)
		for _, k := range keys {
			fmt.Fprintf(&b, " %s=%v", k, s.Where[k])
		}
		fmt.Fprintf(&b, " rows %s %d", s.Op, *s.Rows)
		return b.String()
	}
	names := s.Item.Char
	if len(s.Item.Chars) > 0 {
		names = strings.Join(s.Item.Chars, ",")
	}
	fmt.Fprintf(&b, "item %s %d", names, s.Item.Entry)
	if s.Item.Flags != nil {
		fmt.Fprintf(&b, " flags %d", *s.Item.Flags)
	}
	fmt.Fprintf(&b, " count %s %d", s.Op, *s.Count)
	return b.String()
}

// checkDBQuery builds the DBQuery from a check_db step with vars expanded.
func (rr *run) checkDBQuery(s *Step) (DBQuery, error) {
	q := DBQuery{Op: s.Op}
	if s.Table != "" {
		q.Table = s.Table
		q.Want = *s.Rows
		if len(s.Where) > 0 {
			q.Where = map[string]any{}
			for k, v := range s.Where {
				switch t := v.(type) {
				case string:
					x, err := expand(t, rr.vars, false)
					if err != nil {
						return q, err
					}
					if !whereValue.MatchString(x) {
						return q, fmt.Errorf("step (check_db): bad where value %q", x)
					}
					q.Where[k] = x
				default:
					q.Where[k] = v
				}
			}
		}
		return q, nil
	}
	q.Item = &ItemQuery{Entry: s.Item.Entry, Flags: s.Item.Flags}
	q.Want = *s.Count
	if s.Item.Char != "" {
		x, err := expand(s.Item.Char, rr.vars, false)
		if err != nil {
			return q, err
		}
		if !charName.MatchString(x) {
			return q, fmt.Errorf("bad char name %q", x)
		}
		q.Item.Char = x
	} else {
		for _, c := range s.Item.Chars {
			x, err := expand(c, rr.vars, false)
			if err != nil {
				return q, err
			}
			if !charName.MatchString(x) {
				return q, fmt.Errorf("bad char name %q", x)
			}
			q.Item.Chars = append(q.Item.Chars, x)
		}
	}
	return q, nil
}

func cmpOp(op string, got, want int64) bool {
	switch op {
	case "==":
		return got == want
	case "!=":
		return got != want
	case ">=":
		return got >= want
	case "<=":
		return got <= want
	case ">":
		return got > want
	case "<":
		return got < want
	}
	return false
}

func (rr *run) doCheckDB(ctx context.Context, s *Step, n int) bool {
	desc := rr.descOf(s, dbDesc(s))
	if rr.r.DB == nil {
		rr.errorf(n, desc, "the scenario has no db target")
		rr.skipRest(n + 1)
		return false
	}
	q, err := rr.checkDBQuery(s)
	if err != nil {
		rr.errorf(n, desc, "%v", err)
		rr.skipRest(n + 1)
		return false
	}
	deadline := rr.now().Add(time.Duration(s.WithinS * float64(time.Second)))
	for {
		got, err := rr.r.DB.Count(ctx, q)
		if err != nil {
			rr.errorf(n, desc, "%v", err)
			rr.skipRest(n + 1)
			return false
		}
		if cmpOp(s.Op, got, q.Want) {
			line := fmt.Sprintf("  PASS %s (got %d)", desc, got)
			rr.emit(Record{T: "check", Step: n, Raw: line, Norm: line, Status: "PASS"})
			rr.checks = append(rr.checks, CheckResult{Step: n, Desc: desc, Status: "PASS"})
			rr.passed++
			return true
		}
		if !rr.now().Before(deadline) {
			reason := fmt.Sprintf("got %d", got)
			if !rr.failf(n, desc, reason, rr.onFailOf(s)) {
				rr.skipRest(n + 1)
				return false
			}
			return true
		}
		if serr := rr.sleep(ctx, 5*time.Second); serr != nil {
			if ctx.Err() != nil {
				rr.errorf(n, desc, "interrupted")
			} else {
				rr.errorf(n, desc, "%v", serr)
			}
			rr.skipRest(n + 1)
			return false
		}
	}
}

// checkRPCDesc is the norm-form description of a check_rpc step.
func checkRPCDesc(s *Step) string {
	base := fmt.Sprintf("rpc[%s] %s %s", s.Session, s.Method, s.Path)
	switch {
	case s.HasEq:
		raw, _ := json.Marshal(s.Equals)
		return base + " == " + string(raw)
	case s.HasRe:
		return base + " =~ /" + s.MatchRe + "/"
	default:
		return fmt.Sprintf("%s len == %d", base, *s.Len)
	}
}

func (rr *run) doCheckRPC(ctx context.Context, s *Step, n int) bool {
	desc := rr.descOf(s, checkRPCDesc(s))
	st, ok := rr.sess[s.Session]
	if !ok {
		rr.errorf(n, desc, "unknown session %q", s.Session)
		rr.skipRest(n + 1)
		return false
	}
	if st.closed {
		rr.errorf(n, desc, "session %s is closed (it logged out at step %d)", s.Session, st.closedAt)
		rr.skipRest(n + 1)
		return false
	}
	params, err := expandAny(s.Params, rr.vars)
	if err != nil {
		rr.errorf(n, desc, "%v", err)
		rr.skipRest(n + 1)
		return false
	}
	pm, _ := params.(map[string]any)
	if pm == nil {
		pm = map[string]any{}
	}
	deadline := rr.now().Add(time.Duration(s.WithinS * float64(time.Second)))
	for {
		raw, err := st.caller.Call(ctx, s.Method, pm, callTimeout(s))
		if err != nil {
			rr.errorf(n, desc, "rpc %s: %s", s.Method, rpcText(err))
			rr.skipRest(n + 1)
			return false
		}
		v, found := pathValue(raw, s.Path)
		if !found {
			if s.WithinS == 0 || !rr.now().Before(deadline) {
				reason := fmt.Sprintf("path %s not in the result", s.Path)
				if !rr.failf(n, desc, reason, rr.onFailOf(s)) {
					rr.skipRest(n + 1)
					return false
				}
				return true
			}
			if serr := rr.sleep(ctx, time.Second); serr != nil {
				rr.errorf(n, desc, "interrupted")
				rr.skipRest(n + 1)
				return false
			}
			continue
		}
		var reason string
		pass := false
		switch {
		case s.HasEq:
			wantAny, _ := expandAny(s.Equals, rr.vars)
			// Deep equal after both sides go through json.Unmarshal into any
			// (numbers are float64).
			wb, _ := json.Marshal(wantAny)
			gb, _ := json.Marshal(v)
			var wa, ga any
			_ = json.Unmarshal(wb, &wa)
			_ = json.Unmarshal(gb, &ga)
			gb2, _ := json.Marshal(ga)
			wb2, _ := json.Marshal(wa)
			pass = string(gb2) == string(wb2)
			reason = fmt.Sprintf("got %s", cut(string(redactJSON(gb, rr.tokens())), 200))
		case s.HasRe:
			re, _ := compileRegexp(s.MatchRe)
			str := render(v)
			pass = re.MatchString(str)
			reason = fmt.Sprintf("got %s", cut(redactAll(str, rr.tokens()), 200))
		default:
			arr, ok := v.([]any)
			if !ok {
				reason = fmt.Sprintf("got %s", cut(redactAll(render(v), rr.tokens()), 200))
				break
			}
			pass = int64(len(arr)) == *s.Len
			reason = fmt.Sprintf("got %d", len(arr))
		}
		if pass {
			gotRaw, _ := json.Marshal(v)
			gotRaw = redactJSON(gotRaw, rr.tokens())
			line := fmt.Sprintf("  PASS %s (got %s)", desc, cut(string(gotRaw), 200))
			rr.emit(Record{T: "check", Step: n, Session: s.Session, Raw: line, Norm: line, Status: "PASS"})
			rr.checks = append(rr.checks, CheckResult{Step: n, Desc: desc, Status: "PASS", Got: gotRaw})
			rr.passed++
			return true
		}
		if s.WithinS == 0 || !rr.now().Before(deadline) {
			if !rr.failf(n, desc, reason, rr.onFailOf(s)) {
				rr.skipRest(n + 1)
				return false
			}
			return true
		}
		if serr := rr.sleep(ctx, time.Second); serr != nil {
			rr.errorf(n, desc, "interrupted")
			rr.skipRest(n + 1)
			return false
		}
	}
}

// finalStep runs one finally step: actions only, errors become exit 15 but
// never stop the remaining finally steps.
func (rr *run) finalStep(ctx context.Context, s *Step, n int) {
	if s.Note != "" {
		rr.emit(Record{T: "note", Step: n, Raw: "  # " + s.Note, Norm: "  # " + s.Note})
	}
	checksBefore := len(rr.checks)
	errsBefore := rr.errs
	var ok bool
	switch s.Do {
	case "section":
		rr.sections++
		line := fmt.Sprintf("=== STEP %d: %s", rr.sections, s.Title)
		rr.emit(Record{T: "section", Step: n, Raw: "\n" + line, Norm: "\n" + line})
		if err := rr.doMark(ctx, s, n); err != nil {
			rr.errorf(n, fmt.Sprintf("finally step %d (%s)", n-1000+1, s.Do), "%v", err)
		}
		return
	case "mark":
		if err := rr.doMark(ctx, s, n); err != nil {
			rr.errorf(n, fmt.Sprintf("finally step %d (%s)", n-1000+1, s.Do), "%v", err)
		}
		return
	case "gm":
		ok = rr.doGM(ctx, s, n, true)
	case "bot":
		ok = rr.doBot(ctx, s, n, true)
	case "addon_send":
		ok = rr.doAddonSend(ctx, s, n, true)
	case "rpc":
		ok = rr.doRPC(ctx, s, n, true)
	case "sleep":
		ok = rr.doSleep(ctx, s, n)
	default:
		rr.errorf(n, fmt.Sprintf("finally step %d (%s)", n-1000+1, s.Do), "bad do %q", s.Do)
		return
	}
	if ok {
		return
	}
	// Helpers already called errorf exactly once on failure: rename that one
	// record to finally numbering, keeping the helper's ": <reason>" suffix.
	if len(rr.checks) == checksBefore+1 && rr.errs == errsBefore+1 {
		oldDesc := rr.checks[len(rr.checks)-1].Desc
		newDesc := fmt.Sprintf("finally step %d (%s)", n-1000+1, s.Do)
		rr.checks[len(rr.checks)-1].Desc = newDesc
		for j := len(rr.records) - 1; j >= 0; j-- {
			if rr.records[j].T == "check" && rr.records[j].Step == n {
				rr.records[j].Raw = strings.Replace(rr.records[j].Raw, "  ERROR "+oldDesc, "  ERROR "+newDesc, 1)
				rr.records[j].Norm = strings.Replace(rr.records[j].Norm, "  ERROR "+oldDesc, "  ERROR "+newDesc, 1)
				break
			}
		}
		return
	}
	rr.errorf(n, fmt.Sprintf("finally step %d (%s)", n-1000+1, s.Do), "failed")
}
