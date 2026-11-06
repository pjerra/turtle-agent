// instdata.go: the --gm-instance-data test tool (opt-in, test servers only).
// In ZG walks Hakkar's pull gate needs the five priests marked done, which
// the walk-39 worker did with a shell loop (hkgmall.sh): once a raid's
// run.log showed the Hakkar leg, it sent `.debug instancedata 1 3`,
// `.debug instancedata 6 3`, `.debug instancedata 0 0` as say chat over the
// slot's rpc, 3 s apart. A glob bug in that loop cost ~4 h of Hakkar attempts
// in walk 39. Raid-walk owns it now: one GMInstanceSpec per named leg, sent
// through the walk's own gm path (OrderedLine allow-listed), in order, 3 s
// apart, once per launch: a placed start inside the leg fires BEFORE the
// run starts, a mid-run reach fires under a run pause.
package raidwalk

import (
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/pjerra/turtle-agent/internal/planexport"
)

// GMInstanceSpec is one --gm-instance-data value: the leg that fires it and
// the `.debug instancedata <a> <b>` commands, in send order.
type GMInstanceSpec struct {
	Leg  string
	Cmds [][2]int
}

// ParseGMInstanceData reads one --gm-instance-data value,
// "<leg>:<a> <b>[;<a> <b>...]". The leg's existence is checked at validate
// against the rows (unknown leg: refusal); the shape is checked here.
func ParseGMInstanceData(v string) (GMInstanceSpec, error) {
	leg, cmds, ok := strings.Cut(v, ":")
	leg, cmds = strings.TrimSpace(leg), strings.TrimSpace(cmds)
	if !ok || leg == "" || cmds == "" {
		return GMInstanceSpec{}, fmt.Errorf("--gm-instance-data %q: want \"<leg>:<a> <b>[;<a> <b>...]\" (a leg name and instance-data commands of two integers)", v)
	}
	var out GMInstanceSpec
	out.Leg = leg
	for _, c := range strings.Split(cmds, ";") {
		f := strings.Fields(c)
		if len(f) != 2 {
			return GMInstanceSpec{}, fmt.Errorf("--gm-instance-data %q: %q is not two integers", v, strings.TrimSpace(c))
		}
		var cmd [2]int
		for i, s := range f {
			n, err := strconv.Atoi(s)
			if err != nil {
				return GMInstanceSpec{}, fmt.Errorf("--gm-instance-data %q: %q is not two integers", v, strings.TrimSpace(c))
			}
			cmd[i] = n
		}
		out.Cmds = append(out.Cmds, cmd)
	}
	return out, nil
}

// GMInstanceDataLine is the `.debug instancedata` line of one command.
func GMInstanceDataLine(cmd [2]int) string {
	return fmt.Sprintf(".debug instancedata %d %d", cmd[0], cmd[1])
}

// GMInstanceDataLogLine is the run.log line of one sent command.
func GMInstanceDataLogLine(leg string, cmd [2]int, reply string) string {
	return fmt.Sprintf("gm-instance-data: %s: %s -> %s", leg, GMInstanceDataLine(cmd), reply)
}

// LegMatchesStatus says whether the run-status leg label starts the named
// leg: the server sends the leg's full plan name (high_priest_thekal) and the
// statuses under test shorten it (thekal), so the walk compares the run's own
// lines, not operator words. Unknown leg names never validate; a leg prefix
// is not its leg (hakkar vs hakkar_outer).
func LegMatchesStatus(leg, label string) bool {
	return strings.EqualFold(leg, label)
}

// PlanGMInstanceData resolves the specs against the rows: an unknown leg is
// refused (exit 2 at validate), and two specs for one leg are one firing in
// flag order.
func PlanGMInstanceData(rows planexport.Rows, specs []GMInstanceSpec) ([]GMInstanceSpec, error) {
	var out []GMInstanceSpec
	seen := map[string]bool{}
	for _, sp := range specs {
		if _, name, err := findLeg(rows, sp.Leg); err != nil {
			return nil, fmt.Errorf("--gm-instance-data: %v", err)
		} else if sp.Leg != name {
			sp.Leg = name
		}
		if seen[sp.Leg] {
			return nil, fmt.Errorf("--gm-instance-data: leg %s is named twice", sp.Leg)
		}
		seen[sp.Leg] = true
		out = append(out, sp)
	}
	return out, nil
}

// InstanceMarks remembers which legs already fired in an instance: one
// marker file per instance and leg in the walk's out dir, one line per sent
// command. A --reuse-instance restart resumes a different out dir per fresh
// attempt, but a retry in the same instance reuses the same instance id and
// never refires a leg the marker calls done.
type InstanceMarks struct {
	dir string
}

// NewInstanceMarks opens the markers of an out dir.
func NewInstanceMarks(dir string) InstanceMarks {
	return InstanceMarks{dir: dir}
}

// markName is the marker's file name: the instance id sanitized for a file
// name plus the leg.
func markName(inst, leg string) string {
	safe := strings.Map(func(r rune) rune {
		if r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || r == '_' || r == '-' {
			return r
		}
		return '_'
	}, inst)
	return fmt.Sprintf("gm-instance-data-%s-%s", safe, leg)
}

// LegDone says the marker already holds every command of the spec.
func (m InstanceMarks) LegDone(inst, leg string, spec GMInstanceSpec) bool {
	raw, err := os.ReadFile(filepath.Join(m.dir, markName(inst, leg)))
	if err != nil {
		return false
	}
	lines := strings.Split(strings.TrimSpace(string(raw)), "\n")
	if len(lines) != len(spec.Cmds) {
		return false
	}
	for i, cmd := range spec.Cmds {
		if strings.TrimSpace(lines[i]) != GMInstanceDataLine(cmd) {
			return false
		}
	}
	return true
}

// Record appends one sent command's line to the leg's marker. The marker
// stores the bare command lines; the run.log line (with the reply) is the
// walk's own record.
func (m InstanceMarks) Record(inst, leg string, cmd [2]int) error {
	if err := os.MkdirAll(m.dir, 0o700); err != nil {
		return err
	}
	f, err := os.OpenFile(filepath.Join(m.dir, markName(inst, leg)), os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	defer f.Close()
	_, err = fmt.Fprintln(f, GMInstanceDataLine(cmd))
	return err
}

// Write rewrites the leg's marker with exactly cmds, in order (an empty
// list removes the marker). The walk rewrites the confirmed commands every
// pass, so a refire never duplicates lines and LegDone reads back once
// every command of the spec is confirmed.
func (m InstanceMarks) Write(inst, leg string, cmds [][2]int) error {
	if err := os.MkdirAll(m.dir, 0o700); err != nil {
		return err
	}
	p := filepath.Join(m.dir, markName(inst, leg))
	if len(cmds) == 0 {
		if err := os.Remove(p); err != nil && !os.IsNotExist(err) {
			return err
		}
		return nil
	}
	var b strings.Builder
	for _, cmd := range cmds {
		b.WriteString(GMInstanceDataLine(cmd))
		b.WriteByte('\n')
	}
	return os.WriteFile(p, []byte(b.String()), 0o600)
}

// LegLog returns the leg marker's lines ("": none yet).
func (m InstanceMarks) LegLog(inst, leg string) ([]string, error) {
	raw, err := os.ReadFile(filepath.Join(m.dir, markName(inst, leg)))
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	var out []string
	for _, l := range strings.Split(strings.TrimSpace(string(raw)), "\n") {
		if strings.TrimSpace(l) != "" {
			out = append(out, l)
		}
	}
	return out, nil
}
