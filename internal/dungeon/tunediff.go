package dungeon

import (
	"encoding/json"
	"fmt"
	"sort"

	"github.com/pjerra/turtle-agent/internal/trace"
)

// TuneDiffResult is what a run's live tuning comes to, as patches for the files it would have been better to start with.
type TuneDiffResult struct {
	Run  string `json:"run"`
	Plan string `json:"plan"`
	// TunePatch is a JSON patch (RFC 6902) for the tunables file: one op per key whose value at the end of the run is not the one it
	// began with ("add" sets it, "remove" puts it back to the driver's own).
	TunePatch []PatchOp `json:"tune_patch"`
	// PlanPatch is a JSON patch for the plan file as the run began with it: the reloads' operations, in order.
	PlanPatch []PatchOp `json:"plan_patch"`
	// Refused lists the files and requests the run turned down, with why.
	Refused []string `json:"refused,omitempty"`
	// Changes is the number of tune_change and plan_reload events behind the patches.
	Changes int `json:"changes"`
}

// TuneDiff reads the tune_change and plan_reload events of a trace.
func TuneDiff(f *trace.File) TuneDiffResult {
	res := TuneDiffResult{Run: f.Header.Run, Plan: f.Header.Plan, TunePatch: []PatchOp{}, PlanPatch: []PatchOp{}}
	type net struct {
		first, last float64
		src         string
	}
	keys := map[string]*net{}
	var order []string
	for _, e := range f.Events {
		ok, _ := e.D["ok"].(bool)
		switch e.Ev {
		case "tune_change":
			if !ok {
				res.Refused = append(res.Refused, fmt.Sprintf("t=%.1fs %v: %v", float64(e.T)/1000, e.D["source"], e.D["error"]))
				continue
			}
			k, _ := e.D["key"].(string)
			o, _ := e.D["old"].(float64)
			n, _ := e.D["new"].(float64)
			src, _ := e.D["source"].(string)
			res.Changes++
			if x := keys[k]; x != nil {
				x.last, x.src = n, src
			} else {
				keys[k] = &net{first: o, last: n, src: src}
				order = append(order, k)
			}
		case "plan_reload":
			if !ok {
				res.Refused = append(res.Refused, fmt.Sprintf("t=%.1fs plan: %v", float64(e.T)/1000, firstLine(fmt.Sprint(e.D["error"]))))
				continue
			}
			raw, _ := json.Marshal(e.D["ops"])
			var ops []PatchOp
			_ = json.Unmarshal(raw, &ops)
			res.Changes++
			for _, op := range ops {
				op.Old = nil
				res.PlanPatch = append(res.PlanPatch, op)
			}
		}
	}
	sort.Strings(order)
	for _, k := range order {
		x := keys[k]
		if x.first == x.last {
			continue
		}
		if x.src == "reset" {
			res.TunePatch = append(res.TunePatch, PatchOp{Op: "remove", Path: jsonPtr(k)})
			continue
		}
		res.TunePatch = append(res.TunePatch, PatchOp{Op: "add", Path: jsonPtr(k), Value: x.last})
	}
	return res
}
