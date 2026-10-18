package dungeon

import (
	"fmt"
)

// RunMeta names the AI driver of a run (model, effort, run label). Every run writes it into its trace header and run
// report. Only with ServerTrace (opt-in, off by default) does a remote run (driven or observed) also send it to the
// server's run record (`.bot trace start <run_id> ... driver=<model>:<effort>:<run>`, TortoiseBots #986) and stop that
// trace at the end; a group run never sends it (party chat carries no answer: the report notes it). The server's stop
// is not scoped to a run id, so ServerTrace is for boxes where nobody starts a trace by hand (README).
type RunMeta struct {
	Model       string `json:"model,omitempty"`
	Effort      string `json:"effort,omitempty"`
	Run         string `json:"run,omitempty"`
	ServerTrace bool   `json:"server_trace,omitempty"`
}

// Set says whether a driver field (model, effort, run) is set; ServerTrace alone is not a driver.
func (m RunMeta) Set() bool { return m.Model != "" || m.Effort != "" || m.Run != "" }

// Driver is the value of the server's driver= field: model:effort:run, with "-" for an empty field.
func (m RunMeta) Driver() string {
	dash := func(s string) string {
		if s == "" {
			return "-"
		}
		return s
	}
	return dash(m.Model) + ":" + dash(m.Effort) + ":" + dash(m.Run)
}

// Check says why the meta is not usable, or "" when it is. Each field takes letters, digits and . _ - + (no colon: it
// joins the driver value), and the joined driver=<model>:<effort>:<run> the server stores is at most 96 characters.
func (m RunMeta) Check() string {
	if !m.Set() {
		if m.ServerTrace {
			return "run_meta.server_trace needs run_meta's model, effort or run: the server trace sends the driver"
		}
		return ""
	}
	for _, f := range []struct {
		name, v string
	}{{"model", m.Model}, {"effort", m.Effort}, {"run", m.Run}} {
		if len(f.v) > 64 {
			return fmt.Sprintf("run_meta.%s: %q is longer than 64 characters", f.name, f.v)
		}
		for _, c := range f.v {
			ok := c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' ||
				c == '.' || c == '_' || c == '-' || c == '+'
			if !ok {
				return fmt.Sprintf("run_meta.%s: %q takes letters, digits and . _ - + only (no colon: it joins the driver value)", f.name, f.v)
			}
		}
	}
	if len(m.Driver()) > 96 {
		return fmt.Sprintf("run_meta: the driver value %q is longer than 96 characters (the server's limit)", m.Driver())
	}
	return ""
}
