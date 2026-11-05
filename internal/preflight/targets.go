package preflight

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"

	"github.com/pjerra/turtle-agent/internal/slots"
)

// target is one raid-walk invocation of the walk directory, already split for the multi-slot form: one slot, one
// plan, one holder.
type target struct {
	source string // cmd-a.txt or targets.txt:LINE
	binary string
	plan   string
	server string
	slot   string // "" when the command leases any free slot
	holder string
	dir    string
	sql    string
	from   string
	until  string
	out    string
	slots  string
	hires  string

	place         bool
	gmClear       bool
	gmClearBoss   bool
	gmClearSpawns string

	capFlag int
	capSet  bool

	// goalBoss and goalLeg come from a targets.txt line's goal (boss:<entry>, leg:<name>); zero when absent.
	goalBoss uint32
	goalLeg  string
}

// fileError is a command file that cannot be read or parsed. It is reported as a FAIL, not returned as a run error:
// the other files still get checked.
type fileError struct {
	file string
	msg  string
}

// rawCmd is one command line before the pair form is expanded.
type rawCmd struct {
	source string
	binary string
	plans  []string
	flags  []rawFlag
}

type rawFlag struct {
	name  string
	value string
}

// switchFlags are raid-walk flags that may stand alone (a bare --place turns it on).
var switchFlags = map[string]bool{
	"place": true, "reuse-instance": true, "dry-run": true, "revive-at-raid": true,
	"instance-cap-fail": true, "instance-cap-unsafe": true, "instance-cap-reset": true,
}

// readTargets reads every cmd-*.txt and targets.txt of the walk directory.
func readTargets(dir string) ([]target, []fileError) {
	var targets []target
	var errs []fileError
	names, err := os.ReadDir(dir)
	if err != nil {
		return nil, []fileError{{file: dir, msg: err.Error()}}
	}
	var files []string
	for _, e := range names {
		if e.IsDir() {
			continue
		}
		n := e.Name()
		if strings.HasPrefix(n, "cmd-") && strings.HasSuffix(n, ".txt") {
			files = append(files, n)
		}
	}
	sort.Strings(files)
	for _, n := range files {
		path := filepath.Join(dir, n)
		data, err := os.ReadFile(path)
		if err != nil {
			errs = append(errs, fileError{file: path, msg: err.Error()})
			continue
		}
		for i, line := range strings.Split(string(data), "\n") {
			line = strings.TrimSpace(line)
			if line == "" || strings.HasPrefix(line, "#") {
				continue
			}
			ts, err := parseCommand(line, n)
			if err != nil {
				errs = append(errs, fileError{file: path, msg: fmt.Sprintf("line %d: %v", i+1, err)})
				continue
			}
			targets = append(targets, ts...)
		}
	}
	tpath := filepath.Join(dir, "targets.txt")
	data, err := os.ReadFile(tpath)
	if os.IsNotExist(err) {
		tpath = filepath.Join(dir, "targets")
		data, err = os.ReadFile(tpath)
	}
	if err == nil {
		ts, errs2 := parseTargetsFile(string(data), tpath)
		targets = append(targets, ts...)
		errs = append(errs, errs2...)
	} else if !os.IsNotExist(err) {
		errs = append(errs, fileError{file: tpath, msg: err.Error()})
	}
	return targets, errs
}

// parseCommand parses one full command line ("<binary> raid-walk ...") into its targets.
func parseCommand(line, source string) ([]target, error) {
	words, err := splitWords(line)
	if err != nil {
		return nil, err
	}
	if len(words) < 2 {
		return nil, fmt.Errorf("not a command: %q", line)
	}
	if filepath.Base(words[0]) != "turtle-agent" || words[1] != "raid-walk" {
		return nil, fmt.Errorf("not a turtle-agent raid-walk command: %q", line)
	}
	cmd, err := parseRawCmd(words[2:], source)
	if err != nil {
		return nil, err
	}
	cmd.binary = words[0]
	return expandCmd(cmd)
}

// parseRawCmd reads the arguments after "raid-walk".
func parseRawCmd(args []string, source string) (*rawCmd, error) {
	cmd := &rawCmd{source: source}
	for i := 0; i < len(args); i++ {
		a := args[i]
		if a == "--" {
			cmd.plans = append(cmd.plans, args[i+1:]...)
			break
		}
		if !strings.HasPrefix(a, "--") {
			cmd.plans = append(cmd.plans, a)
			continue
		}
		name := strings.TrimPrefix(a, "--")
		val, has := "", false
		if j := strings.IndexByte(name, '='); j >= 0 {
			name, val, has = name[:j], name[j+1:], true
		}
		if !has {
			if i+1 >= len(args) || strings.HasPrefix(args[i+1], "--") {
				if switchFlags[name] {
					cmd.flags = append(cmd.flags, rawFlag{name: name, value: "on"})
					continue
				}
				return nil, fmt.Errorf("--%s needs a value", name)
			}
			i++
			val, has = args[i], true
		}
		cmd.flags = append(cmd.flags, rawFlag{name: name, value: val})
	}
	if len(cmd.plans) == 0 {
		return nil, fmt.Errorf("no plan named")
	}
	return cmd, nil
}

// pick returns the last value of a flag that applies to the slot: an unprefixed value or one prefixed with the
// slot's own id. prefixes maps a value's "id=" prefix to its slot id.
func pick(cmd *rawCmd, name, slot string, prefixes map[string]bool) (string, bool) {
	found, ok := "", false
	for _, f := range cmd.flags {
		if f.name != name {
			continue
		}
		v := f.value
		if i := strings.IndexByte(v, '='); i > 0 && prefixes != nil {
			pre := v[:i]
			if pre == slot {
				v = v[i+1:]
			} else if prefixes[pre] {
				continue
			}
		}
		found, ok = v, true
	}
	return found, ok
}

// switchOn reports whether a switch flag is on for the slot; a later slot=off wins over an earlier on.
func switchOn(cmd *rawCmd, name, slot string, prefixes map[string]bool) bool {
	on := false
	for _, f := range cmd.flags {
		if f.name != name {
			continue
		}
		v := f.value
		if i := strings.IndexByte(v, '='); i > 0 && prefixes != nil {
			pre := v[:i]
			if pre == slot {
				on = v[i+1:] == "on"
			} else if prefixes[pre] {
				continue
			}
			continue
		}
		on = v == "on" || v == "true" || v == "1"
	}
	return on
}

// expandCmd splits the slot list into one target per slot.
func expandCmd(cmd *rawCmd) ([]target, error) {
	slotVal, _ := pick(cmd, "slot", "", nil)
	var ids []string
	if slotVal != "" {
		for _, id := range strings.Split(slotVal, ",") {
			if id == "" {
				return nil, fmt.Errorf("--slot %q: empty slot id", slotVal)
			}
			ids = append(ids, id)
		}
	} else {
		ids = []string{""}
	}
	pair := len(ids) > 1
	plans := cmd.plans
	switch {
	case len(plans) == 1:
	case pair && len(plans) == len(ids):
	default:
		return nil, fmt.Errorf("%d plans for %d slots: give one plan or one per slot", len(plans), len(ids))
	}
	prefixes := map[string]bool{}
	for _, id := range ids {
		prefixes[id] = true
	}
	out := make([]target, 0, len(ids))
	for i, slot := range ids {
		plan := plans[0]
		if len(plans) > 1 {
			plan = plans[i]
		}
		t := target{source: cmd.source, binary: cmd.binary, plan: plan, slot: slot}
		t.server, _ = pick(cmd, "server", slot, prefixes)
		if t.server == "" {
			return nil, fmt.Errorf("--server is required")
		}
		t.holder, _ = pick(cmd, "holder", slot, prefixes)
		if t.holder == "" {
			if pair {
				t.holder = fmt.Sprintf("raid-walk-%s-s%s", plan, slot)
			} else {
				t.holder = "raid-walk-" + plan
			}
		}
		t.dir, _ = pick(cmd, "dir", slot, prefixes)
		if t.dir == "" {
			t.dir = "dungeons"
		}
		t.sql, _ = pick(cmd, "sql", slot, prefixes)
		t.from, _ = pick(cmd, "from", slot, prefixes)
		t.until, _ = pick(cmd, "until", slot, prefixes)
		t.slots, _ = pick(cmd, "slots", slot, prefixes)
		t.hires, _ = pick(cmd, "hires", slot, prefixes)
		t.gmClearSpawns, _ = pick(cmd, "gm-clear-spawns", slot, prefixes)
		t.out, _ = pick(cmd, "out", slot, prefixes)
		t.place = switchOn(cmd, "place", slot, prefixes)
		_, t.gmClear = pick(cmd, "gm-clear", slot, prefixes)
		_, t.gmClearBoss = pick(cmd, "gm-clear-boss", slot, prefixes)
		if v, ok := pick(cmd, "instance-cap-per-account", slot, prefixes); ok {
			n, err := strconv.Atoi(v)
			if err != nil {
				return nil, fmt.Errorf("--instance-cap-per-account %q: not a number", v)
			}
			t.capFlag, t.capSet = n, true
		}
		if pair {
			base := slots.ExpandTilde(t.out)
			if base == "" {
				base = "raid-walk-pair"
			}
			t.out = filepath.Join(base, fmt.Sprintf("s%s-%s", slot, plan))
		}
		out = append(out, t)
	}
	return out, nil
}

// parseTargetsFile reads the autopilot's targets file:
//
//	id server slot holder goal -- <plan> [raid-walk args...]
//
// and also accepts a full "<binary> raid-walk ..." line. Blank lines and # lines are skipped.
func parseTargetsFile(data, path string) ([]target, []fileError) {
	var out []target
	var errs []fileError
	for n, line := range strings.Split(data, "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		words, err := splitWords(line)
		if err != nil {
			errs = append(errs, fileError{file: path, msg: fmt.Sprintf("line %d: %v", n+1, err)})
			continue
		}
		if len(words) >= 2 && filepath.Base(words[0]) == "turtle-agent" && words[1] == "raid-walk" {
			ts, err := parseCommand(line, fmt.Sprintf("%s:%d", filepath.Base(path), n+1))
			if err != nil {
				errs = append(errs, fileError{file: path, msg: fmt.Sprintf("line %d: %v", n+1, err)})
				continue
			}
			out = append(out, ts...)
			continue
		}
		sep := -1
		for i, w := range words {
			if w == "--" {
				sep = i
				break
			}
		}
		if sep != 5 {
			errs = append(errs, fileError{file: path, msg: fmt.Sprintf("line %d: want `id server slot holder goal -- <plan> [args]`", n+1)})
			continue
		}
		id, server, slot, holder, goal := words[0], words[1], words[2], words[3], words[4]
		if id == "" {
			errs = append(errs, fileError{file: path, msg: fmt.Sprintf("line %d: no raid id", n+1)})
			continue
		}
		if slot == "" {
			errs = append(errs, fileError{file: path, msg: fmt.Sprintf("line %d: no slot", n+1)})
			continue
		}
		args := words[sep+1:]
		cmd, err := parseRawCmd(args, fmt.Sprintf("%s:%d", filepath.Base(path), n+1))
		if err != nil {
			errs = append(errs, fileError{file: path, msg: fmt.Sprintf("line %d: %v", n+1, err)})
			continue
		}
		cmd.flags = append(cmd.flags, rawFlag{name: "server", value: server}, rawFlag{name: "slot", value: slot}, rawFlag{name: "holder", value: holder})
		var boss uint32
		leg := ""
		switch {
		case goal == "full":
		case strings.HasPrefix(goal, "boss:"):
			nb, err := strconv.ParseUint(strings.TrimPrefix(goal, "boss:"), 10, 32)
			if err != nil {
				errs = append(errs, fileError{file: path, msg: fmt.Sprintf("line %d: bad goal %q", n+1, goal)})
				continue
			}
			boss = uint32(nb)
		case strings.HasPrefix(goal, "leg:"):
			leg = strings.TrimPrefix(goal, "leg:")
			if leg == "" {
				errs = append(errs, fileError{file: path, msg: fmt.Sprintf("line %d: bad goal %q", n+1, goal)})
				continue
			}
		default:
			errs = append(errs, fileError{file: path, msg: fmt.Sprintf("line %d: goal %q (want full, leg:<L> or boss:<ENTRY>)", n+1, goal)})
			continue
		}
		cmd.binary = "turtle-agent"
		ts, err := expandCmd(cmd)
		if err != nil {
			errs = append(errs, fileError{file: path, msg: fmt.Sprintf("line %d: %v", n+1, err)})
			continue
		}
		for i := range ts {
			ts[i].goalBoss, ts[i].goalLeg = boss, leg
		}
		out = append(out, ts...)
	}
	return out, errs
}

// splitWords splits a command line into words: white space separates, single and double quotes group, a backslash
// escapes the next byte outside single quotes. No expansion is done: the line is data.
func splitWords(s string) ([]string, error) {
	var out []string
	var b strings.Builder
	inWord := false
	quote := byte(0)
	for i := 0; i < len(s); i++ {
		ch := s[i]
		if quote == 0 {
			switch ch {
			case ' ', '\t', '\r', '\n':
				if inWord {
					out = append(out, b.String())
					b.Reset()
					inWord = false
				}
			case '\'', '"':
				quote, inWord = ch, true
			case '\\':
				if i+1 >= len(s) {
					return nil, fmt.Errorf("trailing backslash")
				}
				i++
				b.WriteByte(s[i])
				inWord = true
			default:
				b.WriteByte(ch)
				inWord = true
			}
			continue
		}
		if ch == quote {
			quote = 0
			continue
		}
		if ch == '\\' && quote == '"' && i+1 < len(s) {
			i++
			b.WriteByte(s[i])
			continue
		}
		b.WriteByte(ch)
	}
	if quote != 0 {
		return nil, fmt.Errorf("unclosed %c quote", quote)
	}
	if inWord {
		out = append(out, b.String())
	}
	return out, nil
}
