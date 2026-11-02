package dashboard

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"net/http"
	"strconv"
	"time"
)

// The live tuning (README "Controls (--control)", issue #183 slice 2, D13). One action, "tune": a fresh tune_list says whether the key exists and
// its limits, the value is checked here before anything is sent, and tune_set takes it at the next leg or pull-group start. No confirm: every
// tunable is bounded by the driver, so the page's slider and its Default button are enough.

// tuneRequest is the body of POST /api/control/<id>/tune (D15). Value is kept raw so a string ("default"), a fraction, null and a missing
// value can each be told apart and refused by name.
type tuneRequest struct {
	Key   string          `json:"key"`
	Value json.RawMessage `json:"value"`
}

// tuneListResult is the part of tune_list the handler reads (the same fields the card's tune_all carries).
type tuneListResult struct {
	Tunables []tuneRow `json:"tunables"`
}

// tuneRow is one row of tune_list as the handler reads it. The bounds are pointers so a row of an older agent, which reports no min and max,
// is told from a real 0..0 range (L6).
type tuneRow struct {
	Key  string   `json:"key"`
	Unit string   `json:"unit"`
	Min  *float64 `json:"min"`
	Max  *float64 `json:"max"`
}

// tune is the live-tuning handler (D8, D13).
func (c *Controller) tune(ac *actionContext) {
	var req tuneRequest
	if !decodeBody(ac.w, ac.r, &req) {
		return
	}
	if req.Key == "" {
		jsonError(ac.w, http.StatusBadRequest, "key is needed: the tunable's name as tune_list gives it")
		return
	}
	cl := NewControlClient(ac.agent, controlTimeout)
	ctx := ac.r.Context()
	raw, err := cl.Read(ctx, "tune_list")
	if err != nil {
		jsonError(ac.w, http.StatusConflict, "%s", redactMessage(err.Error(), cl.Token()))
		return
	}
	var list tuneListResult
	if len(raw) > 0 {
		if err := json.Unmarshal(raw, &list); err != nil {
			jsonError(ac.w, http.StatusBadGateway, "the agent's tune_list could not be read: %v", err)
			return
		}
	}
	row, ok := findTuneRow(list.Tunables, req.Key)
	if !ok {
		jsonError(ac.w, http.StatusBadRequest, "unknown tunable %q: tune_list lists the ones this run has", req.Key)
		return
	}
	value, err := checkTuneValue(row, req.Value)
	if err != nil {
		jsonError(ac.w, http.StatusBadRequest, "%s", err.Error())
		return
	}
	if err := c.log.append(controlRecord{Phase: "request", At: c.cfg.Now().UTC().Format(time.RFC3339), Who: ac.who, Via: ac.via, Peer: ac.peer,
		Agent: ac.agent.ID, Action: ac.action, Verb: req.Key, Args: map[string]any{"value": value}}); err != nil {
		jsonError(ac.w, http.StatusInternalServerError, "the control log could not be written, so nothing was sent: %v", err)
		return
	}
	// The write is detached from the request (M2): a tab that closes, or a client timeout, must not cancel a command that is already sent,
	// because only the agent knows whether it ran. Its own bound still applies.
	wctx, cancel := context.WithTimeout(context.WithoutCancel(ac.r.Context()), controlTimeout)
	defer cancel()
	out, err := cl.Write(wctx, "tune_set", map[string]any{"key": req.Key, "value": value})
	okv := err == nil
	res := controlRecord{Phase: "result", At: c.cfg.Now().UTC().Format(time.RFC3339), Who: ac.who, Via: ac.via, Peer: ac.peer,
		Agent: ac.agent.ID, Action: ac.action, Verb: req.Key, RPC: "tune_set", OK: &okv, Outcome: "ok"}
	indeterminate := errors.Is(err, ErrIndeterminate)
	if err != nil {
		res.Outcome = "error"
		res.Code = rpcCode(err)
		res.Message = redactMessage(err.Error(), cl.Token())
	}
	if indeterminate { // never OK: the command may have run, so the line says so and carries no ok
		res.Outcome, res.OK = "indeterminate", nil
	}
	// The RPC has run (or been refused) by now. A result line the log cannot take must not turn a success into a 500, which would invite a
	// second tune_set: it goes to stderr, and a success is answered as executed.
	if lerr := c.log.append(res); lerr != nil {
		logLost(res, lerr)
		if err == nil {
			writeAny(ac.w, http.StatusOK, map[string]any{"executed": true, "log_failed": true})
			return
		}
	}
	switch {
	case indeterminate:
		jsonError(ac.w, http.StatusBadGateway, "unknown: check the tunable's value")
		return
	case err != nil:
		jsonError(ac.w, http.StatusBadGateway, "%s", res.Message)
		return
	}
	scrubbed, serr := Scrub(orNull(out), cl.Token())
	if serr != nil {
		jsonError(ac.w, http.StatusInternalServerError, "the agent's answer could not be scrubbed: %v", serr)
		return
	}
	writeAny(ac.w, http.StatusOK, map[string]any{"status": "ok", "action": ac.action, "verb": req.Key, "rpc": "tune_set", "result": json.RawMessage(scrubbed)})
}

// findTuneRow is the tunable with that key, if this run has it.
func findTuneRow(rows []tuneRow, key string) (tuneRow, bool) {
	for _, r := range rows {
		if r.Key == key {
			return r, true
		}
	}
	return tuneRow{}, false
}

// checkTuneValue turns the request's value into what tune_set takes: a JSON number within the row's [min, max] (whole for pct and count), or
// the string "default". A number needs the row's bounds: a row of an older agent reports none, and that is refused by name, not as a range.
// Anything else is refused with the reason (nothing is sent).
func checkTuneValue(row tuneRow, raw json.RawMessage) (any, error) {
	if len(raw) == 0 {
		return nil, errors.New("value is needed: a number, or \"default\"")
	}
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.UseNumber()
	var v any
	if err := dec.Decode(&v); err != nil {
		return nil, errors.New("value must be a number or \"default\"")
	}
	switch x := v.(type) {
	case string:
		if x != "default" {
			return nil, fmt.Errorf("%s: value must be a number or \"default\"", row.Key)
		}
		return "default", nil
	case json.Number:
		f, err := x.Float64()
		if err != nil || math.IsNaN(f) || math.IsInf(f, 0) {
			return nil, fmt.Errorf("%s: value must be a number or \"default\"", row.Key)
		}
		if row.Min == nil || row.Max == nil { // an older agent's tune_list has no bounds: its range is unknown, not 0..0 (L6)
			return nil, fmt.Errorf("this agent does not report bounds for %s", row.Key)
		}
		if f < *row.Min || f > *row.Max {
			return nil, fmt.Errorf("%s: %v is outside %v..%v (%s)", row.Key, trimNum(f), trimNum(*row.Min), trimNum(*row.Max), row.Unit)
		}
		if row.Unit == "pct" || row.Unit == "count" {
			if f != math.Trunc(f) {
				return nil, fmt.Errorf("%s: a whole number is needed (%s)", row.Key, row.Unit)
			}
		}
		return f, nil
	}
	return nil, errors.New("value must be a number or \"default\"")
}

// trimNum prints a number the shortest way (70, not 70.000000).
func trimNum(f float64) string {
	return strconv.FormatFloat(f, 'g', -1, 64)
}
