package scenario

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/pjerra/turtle-agent/internal/events"
)

// drain fetches new events for every open session until no page reports more,
// appends them to the buffer (cap 20,000, oldest dropped) and writes the
// recorded ones after the step.
func (rr *run) drain(ctx context.Context, step int) error {
	for _, name := range rr.order {
		st := rr.sess[name]
		if st.closed {
			continue
		}
		for {
			raw, err := st.caller.Call(ctx, "events", map[string]any{"since": st.cursor, "limit": 1000}, 20*time.Second)
			if err != nil {
				return fmt.Errorf("session %s: drain: %v", name, err)
			}
			var page events.Page
			if err := json.Unmarshal(raw, &page); err != nil {
				return fmt.Errorf("session %s: drain: %v", name, err)
			}
			if page.Dropped {
				rr.emit(Record{T: "note", Step: step, Session: name,
					Raw:  fmt.Sprintf("  note: events of session %s were dropped by the agent's ring: a check may miss lines", name),
					Norm: fmt.Sprintf("  note: events of session %s were dropped by the agent's ring: a check may miss lines", name)})
			}
			for _, e := range page.Events {
				st.buf = append(st.buf, e)
			}
			if len(st.buf) > 20000 {
				st.buf = append([]events.Event(nil), st.buf[len(st.buf)-20000:]...)
			}
			st.cursor = page.Next
			if !page.More {
				break
			}
		}
	}
	rr.writeRecorded(step)
	return nil
}

// matchable reports whether a matcher applies to the event for recording.
func matchable(m Matcher, e events.Event) bool {
	if m.Session != "" && m.Session != sessOf(e) {
		return false
	}
	c, err := m.compile(nil)
	if err != nil {
		return false
	}
	// A record matcher's match is optional; an empty regexp matches all.
	if m.Match == "" {
		kindOK := m.Kind == "" || e.Kind == m.Kind
		if !kindOK {
			return false
		}
		if m.Prefix != "" {
			p, _ := e.Data["prefix"].(string)
			if e.Kind != events.KindAddon || p != m.Prefix {
				return false
			}
		}
		if m.Channel != "" {
			typ, _ := e.Data["type"].(string)
			if typ != m.Channel {
				return false
			}
		}
		if m.Source != "" && !strings.EqualFold(e.Source, m.Source) {
			return false
		}
		return true
	}
	return c.matches(e)
}

func sessOf(e events.Event) string { return "" }

// writeRecorded writes every new event matching any record matcher (or an
// expect just fulfilled — callers pass those explicitly), in sorted session
// order then seq.
func (rr *run) writeRecorded(step int) {
	rr.writeRecordedExtra(step, nil)
}

func (rr *run) writeRecordedExtra(step int, extra map[uint64]bool) {
	for _, name := range rr.order {
		st := rr.sess[name]
		for i := range st.buf {
			e := &st.buf[i]
			if e.Seq <= st.recorded {
				continue
			}
			matched := extra != nil && extra[e.Seq]
			if !matched {
				for _, m := range rr.f.Record {
					mm := m
					if mm.Session == "" || mm.Session == name {
						if matchable(mm, *e) {
							matched = true
							break
						}
					}
				}
			}
			if !matched {
				continue
			}
			rr.emit(eventRecord(step, name, e))
			if e.Seq > st.recorded {
				st.recorded = e.Seq
			}
		}
		// Advance recorded past unmatchable events so they are not rescanned.
		for _, e := range st.buf {
			if e.Seq > st.recorded {
				// keep recorded at the max written, not the max seen:
				// a later expect may still need an older unrecorded event.
				_ = e
			}
		}
	}
}

// eventRecord renders one recorded event (raw with seq/time, norm without).
func eventRecord(step int, session string, e *events.Event) Record {
	kind := e.Kind
	if t, _ := e.Data["type"].(string); t != "" && (e.Kind == "addon" || e.Kind == "chat") {
		kind += "/" + t
	}
	extra := ""
	if p, _ := e.Data["prefix"].(string); e.Kind == "addon" && p != "" {
		extra = " " + p
	}
	line := eventLine(*e)
	raw := fmt.Sprintf("    [%s] %d\t%s\t%s%s\t%s", session, e.Seq, e.Time.Format(time.RFC3339Nano), kind, extra, line)
	norm := fmt.Sprintf("    [%s] %s%s\t%s", session, kind, extra, line)
	return Record{T: "event", Step: step, Session: session, Raw: raw, Norm: norm, Event: e}
}

// doMark drains and moves every window and match cursor to the newest event.
func (rr *run) doMark(s *Step, n int) {
	_ = rr.drain(rr.runCtx, n)
	for _, name := range rr.order {
		st := rr.sess[name]
		if st.closed {
			continue
		}
		st.window, st.from = st.cursor, st.cursor
	}
}

// doSection prints the header, marks, and then behaves as mark.
func (rr *run) doSection(s *Step, n int) bool {
	rr.sections++
	line := fmt.Sprintf("=== STEP %d: %s", rr.sections, s.Title)
	rr.emit(Record{T: "section", Step: n, Raw: "\n" + line, Norm: "\n" + line})
	rr.doMark(s, n)
	return true
}

// settle sleeps settle_s (step value, else top-level), then drains.
func (rr *run) settle(ctx context.Context, s *Step, n int) error {
	d := rr.f.SettleS
	if s.HasSettleS {
		d = s.SettleS
	}
	if d > 0 {
		if err := rr.sleep(ctx, time.Duration(d*float64(time.Second))); err != nil {
			rr.errorf(n, fmt.Sprintf("step %d (%s)", n, s.Do), "interrupted")
			rr.skipRest(n + 1)
			return err
		}
	}
	if err := rr.drain(ctx, n); err != nil {
		rr.errorf(n, fmt.Sprintf("step %d (%s)", n, s.Do), "%v", err)
		rr.skipRest(n + 1)
		return err
	}
	return nil
}

func (rr *run) openSession(s *Step, n int) (*sessState, bool) {
	st, ok := rr.sess[s.Session]
	if !ok {
		rr.errorf(n, fmt.Sprintf("step %d (%s)", n, s.Do), "unknown session %q", s.Session)
		rr.skipRest(n + 1)
		return nil, false
	}
	if st.closed {
		rr.errorf(n, fmt.Sprintf("step %d (%s)", n, s.Do), "session %s is closed (it logged out at step %d)", s.Session, st.closedAt)
		rr.skipRest(n + 1)
		return nil, false
	}
	return st, true
}

// callTimeout resolves a step's RPC timeout: step value, else 60 s.
func callTimeout(s *Step) time.Duration {
	if s.TimeoutS > 0 {
		return time.Duration(s.TimeoutS * float64(time.Second))
	}
	return 60 * time.Second
}

func (rr *run) doGM(ctx context.Context, s *Step, n int, inFinally bool) bool {
	if s.Note == "" {
		// note already printed by step()
	}
	st, ok := rr.openSession(s, n)
	if !ok {
		return false
	}
	text, err := expand(s.Text, rr.vars, false)
	if err != nil {
		return rr.checkErr(n, s, err)
	}
	if gmDenied(text) {
		rr.errorf(n, fmt.Sprintf("step %d (%s)", n, s.Do), "GM command %q changes the world for everyone (AGENTS.md): refused", text)
		if !inFinally {
			rr.skipRest(n + 1)
		}
		return false
	}
	raw := fmt.Sprintf("  gm[%s] %s", s.Session, text)
	norm := fmt.Sprintf("  gm[%s] %s", s.Session, s.Text)
	rr.emit(Record{T: "action", Step: n, Session: s.Session, Raw: raw, Norm: norm})
	if _, err := st.caller.Call(ctx, "gm", map[string]any{"text": text}, callTimeout(s)); err != nil {
		rr.errorf(n, fmt.Sprintf("step %d (%s)", n, s.Do), "gm: %v", rpcText(err))
		if !inFinally {
			rr.skipRest(n + 1)
		}
		return false
	}
	_ = st
	if inFinally {
		_ = rr.drain(ctx, n)
		return true
	}
	if err := rr.settle(ctx, s, n); err != nil {
		return false
	}
	return true
}

func (rr *run) doBot(ctx context.Context, s *Step, n int, inFinally bool) bool {
	st, ok := rr.openSession(s, n)
	if !ok {
		return false
	}
	cmd, err := expand(s.Command, rr.vars, false)
	if err != nil {
		return rr.checkErr(n, s, err)
	}
	raw := fmt.Sprintf("  gm[%s] .bot %s", s.Session, cmd)
	norm := fmt.Sprintf("  gm[%s] .bot %s", s.Session, s.Command)
	_ = st
	rr.emit(Record{T: "action", Step: n, Session: s.Session, Raw: raw, Norm: norm})
	st2, ok := rr.openSession(s, n)
	if !ok {
		return false
	}
	if _, err := st2.caller.Call(ctx, "gm", map[string]any{"text": ".bot " + cmd}, callTimeout(s)); err != nil {
		rr.errorf(n, fmt.Sprintf("step %d (%s)", n, s.Do), "gm: %v", rpcText(err))
		if !inFinally {
			rr.skipRest(n + 1)
		}
		return false
	}
	if inFinally {
		_ = rr.drain(ctx, n)
		return true
	}
	if err := rr.settle(ctx, s, n); err != nil {
		return false
	}
	return true
}

func (rr *run) doAddonSend(ctx context.Context, s *Step, n int, inFinally bool) bool {
	st, ok := rr.openSession(s, n)
	if !ok {
		return false
	}
	text, err := expand(s.Text, rr.vars, false)
	if err != nil {
		return rr.checkErr(n, s, err)
	}
	to, err := expand(s.To, rr.vars, false)
	if err != nil {
		return rr.checkErr(n, s, err)
	}
	prefix := s.Prefix
	if prefix == "" {
		prefix = rr.f.AddonPrefix
	}
	raw := fmt.Sprintf("  send[%s] %s ch=%s %s", s.Session, prefix, s.Channel, text)
	norm := fmt.Sprintf("  send[%s] %s ch=%s %s", s.Session, prefix, s.Channel, s.Text)
	rr.emit(Record{T: "action", Step: n, Session: s.Session, Raw: raw, Norm: norm})
	params := map[string]any{"prefix": prefix, "text": text, "channel": s.Channel}
	if to != "" {
		params["to"] = to
	}
	if _, err := st.caller.Call(ctx, "addon_send", params, callTimeout(s)); err != nil {
		rr.errorf(n, fmt.Sprintf("step %d (%s)", n, s.Do), "addon_send: %v", rpcText(err))
		if !inFinally {
			rr.skipRest(n + 1)
		}
		return false
	}
	if inFinally {
		_ = rr.drain(ctx, n)
		return true
	}
	if err := rr.settle(ctx, s, n); err != nil {
		return false
	}
	return true
}

func (rr *run) doRPC(ctx context.Context, s *Step, n int, inFinally bool) bool {
	st, ok := rr.openSession(s, n)
	if !ok {
		return false
	}
	params, err := expandAny(s.Params, rr.vars)
	if err != nil {
		return rr.checkErr(n, s, err)
	}
	pm, _ := params.(map[string]any)
	if pm == nil {
		pm = map[string]any{}
	}
	if s.Method == "gm" {
		if t, _ := pm["text"].(string); t != "" && gmDenied(t) {
			rr.errorf(n, fmt.Sprintf("step %d (%s)", n, s.Do), "GM command %q changes the world for everyone (AGENTS.md): refused", t)
			if !inFinally {
				rr.skipRest(n + 1)
			}
			return false
		}
	}
	tplParams, _ := json.Marshal(s.Params)
	rawParams, _ := json.Marshal(pm)
	maxParams := 300
	rp, tp := string(rawParams), string(tplParams)
	rr.emit(Record{T: "action", Step: n, Session: s.Session,
		Raw:  fmt.Sprintf("  rpc[%s] %s %s", s.Session, s.Method, cut(rp, maxParams)),
		Norm: fmt.Sprintf("  rpc[%s] %s %s", s.Session, s.Method, cut(tp, maxParams))})
	res, err := st.caller.Call(ctx, s.Method, pm, callTimeout(s))
	if s.Method == "logout" {
		st.closed, st.closedAt = true, n
	}
	if err != nil {
		rr.emit(Record{T: "result", Step: n, Session: s.Session,
			Raw: fmt.Sprintf("    -> error %s", rpcText(err)), Norm: resultNorm(s, fmt.Sprintf("    -> error %s", rpcText(err)))})
		if s.IgnoreError {
			if inFinally {
				_ = rr.drain(ctx, n)
				return true
			}
			if serr := rr.settle(ctx, s, n); serr != nil {
				return false
			}
			return true
		}
		rr.errorf(n, fmt.Sprintf("step %d (%s)", n, s.Do), "rpc %s: %s", s.Method, rpcText(err))
		if !inFinally {
			rr.skipRest(n + 1)
		}
		return false
	}
	line := "    -> " + cut(string(res), 300)
	rr.emit(Record{T: "result", Step: n, Session: s.Session, Raw: line, Norm: resultNorm(s, line), Got: res})
	if len(s.Save) > 0 {
		for k, path := range s.Save {
			v, ok := pathValue(res, path)
			if !ok {
				rr.errorf(n, fmt.Sprintf("step %d (%s)", n, s.Do), "path %s not in the result", path)
				if !inFinally {
					rr.skipRest(n + 1)
				}
				return false
			}
			rr.vars[k] = render(v)
		}
	}
	if inFinally {
		_ = rr.drain(ctx, n)
		return true
	}
	if err := rr.settle(ctx, s, n); err != nil {
		return false
	}
	return true
}

func resultNorm(s *Step, line string) string {
	if s.RecordResult {
		return line
	}
	return ""
}

func (rr *run) doSleep(ctx context.Context, s *Step, n int) bool {
	line := fmt.Sprintf("  sleep %g s", s.Seconds)
	rr.emit(Record{T: "action", Step: n, Raw: line, Norm: line})
	if err := rr.sleep(ctx, time.Duration(s.Seconds*float64(time.Second))); err != nil {
		if ctx.Err() != nil {
			rr.errorf(n, fmt.Sprintf("step %d (%s)", n, s.Do), "interrupted")
		} else {
			rr.errorf(n, fmt.Sprintf("step %d (%s)", n, s.Do), "%v", err)
		}
		rr.skipRest(n + 1)
		return false
	}
	if err := rr.drain(ctx, n); err != nil {
		rr.errorf(n, fmt.Sprintf("step %d (%s)", n, s.Do), "%v", err)
		rr.skipRest(n + 1)
		return false
	}
	return true
}

// checkErr maps an unset var at run time to an ERROR.
func (rr *run) checkErr(n int, s *Step, err error) bool {
	if strings.Contains(err.Error(), "is not set") {
		for _, r := range refs(s.Text + s.Command + s.Match) {
			if _, ok := rr.vars[r]; !ok {
				rr.errorf(n, fmt.Sprintf("step %d (%s)", n, s.Do), "${%s} is not set", r)
				rr.skipRest(n + 1)
				return false
			}
		}
		rr.errorf(n, fmt.Sprintf("step %d (%s)", n, s.Do), "%v", err)
		rr.skipRest(n + 1)
		return false
	}
	rr.errorf(n, fmt.Sprintf("step %d (%s)", n, s.Do), "%v", err)
	rr.skipRest(n + 1)
	return false
}

func rpcText(err error) string {
	if re, ok := err.(*RPCError); ok {
		return re.Error()
	}
	return err.Error()
}

func cut(s string, n int) string {
	if len(s) > n {
		return s[:n]
	}
	return s
}
