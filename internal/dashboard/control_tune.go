package dashboard

import (
	"bytes"
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
	Tunables []TuneRow `json:"tunables"`
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
	out, err := cl.Write(ctx, "tune_set", map[string]any{"key": req.Key, "value": value})
	okv := err == nil
	res := controlRecord{Phase: "result", At: c.cfg.Now().UTC().Format(time.RFC3339), Who: ac.who, Via: ac.via, Peer: ac.peer,
		Agent: ac.agent.ID, Action: ac.action, Verb: req.Key, RPC: "tune_set", OK: &okv}
	if err != nil {
		res.Code = rpcCode(err)
		res.Message = redactMessage(err.Error(), cl.Token())
	}
	if lerr := c.log.append(res); lerr != nil {
		jsonError(ac.w, http.StatusInternalServerError, "the control was sent but the log could not be written: %v", lerr)
		return
	}
	if err != nil {
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
func findTuneRow(rows []TuneRow, key string) (TuneRow, bool) {
	for _, r := range rows {
		if r.Key == key {
			return r, true
		}
	}
	return TuneRow{}, false
}

// checkTuneValue turns the request's value into what tune_set takes: a JSON number within the row's [min, max] (whole for pct and count), or
// the string "default". Anything else is refused with the reason (nothing is sent).
func checkTuneValue(row TuneRow, raw json.RawMessage) (any, error) {
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
		if f < row.Min || f > row.Max {
			return nil, fmt.Errorf("%s: %v is outside %v..%v (%s)", row.Key, trimNum(f), trimNum(row.Min), trimNum(row.Max), row.Unit)
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
