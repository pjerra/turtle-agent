package preflight

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"

	"github.com/pjerra/turtle-agent/internal/raidwalk"
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

	// goalBoss and goalLeg come from a targets.txt line's goal, parsed by raidwalk.ParseAutoGoal: a boss or a
	// practice goal names the boss the walk fights, a leg goal names where it stops. Zero when absent.
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

// readTargets reads every cmd-*.txt and targets.txt of the walk directory. binary is the turtle-agent a target that
// names none is checked as.
func readTargets(dir, binary string) ([]target, []fileError) {
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
	for i := range targets {
		// A command's own path stays (it is a literal invocation); the bare default name is --binary's, as the
		// autopilot replaces its own launcher and nothing else.
		if targets[i].binary == "" || targets[i].binary == "turtle-agent" {
			targets[i].binary = binary
		}
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
			switch {
			case name == "gm-instance-data":
				// The value holds spaces: `<leg>:<a> <b>[;<a> <b>...]`. The autopilot reads it the same way (the words
				// after the flag, up to the next flag) and hands the walk one argv, so a quoted or an unquoted line
				// reads alike.
				var words []string
				for i+1 < len(args) && !strings.HasPrefix(args[i+1], "--") {
					i++
					words = append(words, args[i])
				}
				if val = strings.Join(words, " "); val == "" {
					return nil, fmt.Errorf("--%s needs a value", name)
				}
			case name == "from" && i+2 < len(args) && (args[i+1] == "step" || args[i+1] == "leg") && !strings.HasPrefix(args[i+2], "--"):
				// raid-walk's --from takes two words (`leg L` or `step N`), as the autopilot parses it.
				val = args[i+1] + " " + args[i+2]
				i += 2
			case i+1 >= len(args) || strings.HasPrefix(args[i+1], "--"):
				if switchFlags[name] {
					cmd.flags = append(cmd.flags, rawFlag{name: name, value: "on"})
					continue
				}
				return nil, fmt.Errorf("--%s needs a value", name)
			default:
				i++
				val = args[i]
			}
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
// The lines are parsed by raidwalk.ParseAutoTargetsLenient, the autopilot's own reader, so the preflight cannot drift
// from the launch path: the goal grammar (practice:<ENTRY>:<CYCLES> included), the two-word `--from leg L`, an
// unquoted `--gm-instance-data <value with spaces>` and the managed flags all read here exactly as they do there. A
// full "<binary> raid-walk ..." line is taken as a command instead (cmd-*.txt form), so both files can name a walk.
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
	}
	ats, issues, err := raidwalk.ParseAutoTargetsLenient(strings.NewReader(data), nil)
	if err != nil && len(issues) == 0 {
		// A file with no valid line left (or an unreadable stream): the issues, when there are any, carry the reason.
		errs = append(errs, fileError{file: path, msg: err.Error()})
	}
	for _, is := range issues {
		errs = append(errs, fileError{file: path, msg: is.Err.Error()})
	}
	for _, at := range ats {
		ts, err := targetsFromAuto(at, fmt.Sprintf("%s:%d", filepath.Base(path), at.Line))
		if err != nil {
			errs = append(errs, fileError{file: path, msg: fmt.Sprintf("line %d: %v", at.Line, err)})
			continue
		}
		out = append(out, ts...)
	}
	return out, errs
}

// targetsFromAuto turns one autopilot target into the preflight's targets: its raid-walk args are read the same way
// as a command line's, with the fields the autopilot manages filled back in from the parsed target.
func targetsFromAuto(at raidwalk.AutoTarget, source string) ([]target, error) {
	cmd, err := parseRawCmd(at.Args, source)
	if err != nil {
		return nil, err
	}
	cmd.binary = "turtle-agent"
	cmd.flags = append(cmd.flags,
		rawFlag{name: "server", value: at.Server},
		rawFlag{name: "slot", value: at.Slot},
		rawFlag{name: "holder", value: at.Holder},
		// The autopilot launches every target with --place (a restart asks for the goal's own start), so the
		// placement check reads it as on.
		rawFlag{name: "place", value: "on"})
	var boss uint32
	leg := ""
	switch at.Goal.Kind {
	case "boss", "practice":
		boss = at.Goal.Boss
	case "leg":
		leg = at.Goal.Leg
	}
	ts, err := expandCmd(cmd)
	if err != nil {
		return nil, err
	}
	for i := range ts {
		ts[i].from = at.Start
		ts[i].goalBoss, ts[i].goalLeg = boss, leg
	}
	return ts, nil
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
