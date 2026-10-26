package planexport

import (
	"bufio"
	"fmt"
	"strconv"
	"strings"
	"time"
)

// StampFormat is the layout of a migration's file name: YYYYMMDDHHMMSS_world.sql.
const StampFormat = "20060102150405"

// FileName is the migration file name for a moment.
func FileName(t time.Time) string { return t.UTC().Format(StampFormat) + "_world.sql" }

func quote(s string) string {
	s = strings.ReplaceAll(s, `\`, `\\`)
	s = strings.ReplaceAll(s, `'`, `''`)
	s = strings.NewReplacer("\n", " ", "\r", " ", "\x00", "").Replace(s)
	return "'" + s + "'"
}

func num(v float32) string { return strconv.FormatFloat(float64(v), 'f', -1, 32) }

func entriesText(es []uint32) string {
	parts := make([]string, len(es))
	for i, e := range es {
		parts[i] = strconv.FormatUint(uint64(e), 10)
	}
	return strings.Join(parts, ",")
}

// Header is the comment at the top of a migration.
const Header = `-- Dungeon plans for the run leader (.bot run), exported by turtle-agent (plan-export). Plan format v1; the mapping is
-- docs/guides/dungeon-run-leader.md ("From plan v1 to rows"). The plan file in turtle-agent is the single source: edit it
-- there and export again, never these rows by hand. The plan row carries the unit lists cc_priority, cc_always and kill_first
-- (columns from 20261015200000_world.sql). Never edit this file once applied; export a new one.
--
-- Idempotent: each plan's rows are deleted, then inserted. The tables come from 20261014150000_world.sql.
`

// WriteSQL renders the migration for the plans, in the order given.
func WriteSQL(plans []Rows) string {
	var b strings.Builder
	b.WriteString(Header)
	for _, r := range plans {
		p := r.Plan
		fmt.Fprintf(&b, "\n-- %s: map %d, %d legs, %d steps\n", p.Plan, p.Map, len(r.Legs), len(r.Steps))
		fmt.Fprintf(&b, "DELETE FROM `ai_dungeon_step` WHERE `plan` = %s;\n", quote(p.Plan))
		fmt.Fprintf(&b, "DELETE FROM `ai_dungeon_leg` WHERE `plan` = %s;\n", quote(p.Plan))
		fmt.Fprintf(&b, "DELETE FROM `ai_dungeon_leg_need` WHERE `plan` = %s;\n", quote(p.Plan))
		fmt.Fprintf(&b, "DELETE FROM `ai_dungeon_plan` WHERE `plan` = %s;\n", quote(p.Plan))
		fmt.Fprintf(&b, "INSERT INTO `ai_dungeon_plan` (`plan`, `map`, `cc_priority`, `cc_always`, `kill_first`, `source`, `comment`) VALUES\n(%s, %d, %s, %s, %s, %s, %s);\n", quote(p.Plan), p.Map,
			quote(entriesText(p.CCPriority)), quote(entriesText(p.CCAlways)), quote(entriesText(p.KillFirst)), quote(p.Source), quote(p.Comment))
		b.WriteString("INSERT INTO `ai_dungeon_leg` (`plan`, `leg`, `name`, `comment`) VALUES\n")
		for i, l := range r.Legs {
			sep := ",\n"
			if i == len(r.Legs)-1 {
				sep = ";\n"
			}
			fmt.Fprintf(&b, "(%s, %d, %s, %s)%s", quote(l.Plan), l.Leg, quote(l.Name), quote(l.Comment), sep)
		}
		if len(r.Needs) > 0 {
			b.WriteString("INSERT INTO `ai_dungeon_leg_need` (`plan`, `leg`, `item`, `mode`, `pick`, `object`, `name`, `how`) VALUES\n")
			for i, n := range r.Needs {
				sep := ",\n"
				if i == len(r.Needs)-1 {
					sep = ";\n"
				}
				fmt.Fprintf(&b, "(%s, %d, %d, %d, %d, %d, %s, %s)%s", quote(n.Plan), n.Leg, n.Item, n.Mode, n.Pick, n.Object, quote(n.Name), quote(n.How), sep)
			}
		}
		b.WriteString("INSERT INTO `ai_dungeon_step` (`plan`, `leg`, `seq`, `kind`, `entry`, `x`, `y`, `z`, `radius`, `item`, `param`, `flags`, `timeout_s`, `entries`, `comment`) VALUES\n")
		for i, s := range r.Steps {
			sep := ",\n"
			if i == len(r.Steps)-1 {
				sep = ";\n"
			}
			fmt.Fprintf(&b, "(%s, %d, %d, %s, %d, %s, %s, %s, %s, %d, %d, %d, %d, %s, %s)%s", quote(s.Plan), s.Leg, s.Seq, quote(s.Kind), s.Entry,
				num(s.X), num(s.Y), num(s.Z), num(s.Radius), s.Item, s.Param, s.Flags, s.TimeoutS, quote(entriesText(s.Entries)), quote(s.Comment), sep)
		}
	}
	return b.String()
}

// Parse reads a migration WriteSQL wrote back into rows, one Rows per plan in the order of the file. It is the round-trip check of
// the exporter's tests and of `plan-export --check`; it reads only the statements WriteSQL writes.
func Parse(sql string) ([]Rows, error) {
	var out []Rows
	byPlan := map[string]int{}
	table := ""
	sc := bufio.NewScanner(strings.NewReader(sql))
	sc.Buffer(make([]byte, 1<<20), 1<<20)
	line := 0
	cur := func(plan string) (*Rows, error) {
		i, ok := byPlan[plan]
		if !ok {
			return nil, fmt.Errorf("line %d: rows of plan %q before its ai_dungeon_plan row", line, plan)
		}
		return &out[i], nil
	}
	for sc.Scan() {
		line++
		t := strings.TrimSpace(sc.Text())
		switch {
		case t == "" || strings.HasPrefix(t, "--") || strings.HasPrefix(t, "DELETE FROM"):
			continue
		case strings.HasPrefix(t, "INSERT INTO `"):
			name, _, _ := strings.Cut(strings.TrimPrefix(t, "INSERT INTO `"), "`")
			table = name
			continue
		case strings.HasPrefix(t, "("):
		default:
			return nil, fmt.Errorf("line %d: unexpected %q", line, clip(t, 40))
		}
		f, err := splitTuple(t)
		if err != nil {
			return nil, fmt.Errorf("line %d: %w", line, err)
		}
		switch table {
		case "ai_dungeon_plan":
			if len(f) != 7 {
				return nil, fmt.Errorf("line %d: %d columns in ai_dungeon_plan", line, len(f))
			}
			m, err := strconv.ParseUint(f[1], 10, 32)
			if err != nil {
				return nil, fmt.Errorf("line %d: map: %w", line, err)
			}
			var lists [3][]uint32
			for i := range lists {
				if lists[i], err = parseEntries(f[2+i]); err != nil {
					return nil, fmt.Errorf("line %d: %w", line, err)
				}
			}
			byPlan[f[0]] = len(out)
			out = append(out, Rows{Plan: PlanRow{Plan: f[0], Map: uint32(m), CCPriority: lists[0], CCAlways: lists[1], KillFirst: lists[2], Source: f[5], Comment: f[6]}})
		case "ai_dungeon_leg":
			if len(f) != 4 {
				return nil, fmt.Errorf("line %d: %d columns in ai_dungeon_leg", line, len(f))
			}
			r, err := cur(f[0])
			if err != nil {
				return nil, err
			}
			n, err := strconv.Atoi(f[1])
			if err != nil {
				return nil, fmt.Errorf("line %d: leg: %w", line, err)
			}
			r.Legs = append(r.Legs, LegRow{Plan: f[0], Leg: n, Name: f[2], Comment: f[3]})
		case "ai_dungeon_leg_need":
			if len(f) != 8 {
				return nil, fmt.Errorf("line %d: %d columns in ai_dungeon_leg_need", line, len(f))
			}
			r, err := cur(f[0])
			if err != nil {
				return nil, err
			}
			n := NeedRow{Plan: f[0], Name: f[6], How: f[7]}
			var e1 error
			ints := func(i int) int {
				v, err := strconv.Atoi(f[i])
				if err != nil && e1 == nil {
					e1 = fmt.Errorf("column %d: %w", i, err)
				}
				return v
			}
			n.Leg, n.Item = ints(1), uint32(ints(2))
			n.Mode, n.Pick, n.Object = uint8(ints(3)), uint16(ints(4)), uint32(ints(5))
			if e1 != nil {
				return nil, fmt.Errorf("line %d: %w", line, e1)
			}
			r.Needs = append(r.Needs, n)
		case "ai_dungeon_step":
			if len(f) != 15 {
				return nil, fmt.Errorf("line %d: %d columns in ai_dungeon_step", line, len(f))
			}
			r, err := cur(f[0])
			if err != nil {
				return nil, err
			}
			s := StepRow{Plan: f[0], Kind: f[3], Comment: f[14]}
			var e1 error
			ints := func(i int) int {
				v, err := strconv.Atoi(f[i])
				if err != nil && e1 == nil {
					e1 = fmt.Errorf("column %d: %w", i, err)
				}
				return v
			}
			floats := func(i int) float32 {
				v, err := strconv.ParseFloat(f[i], 32)
				if err != nil && e1 == nil {
					e1 = fmt.Errorf("column %d: %w", i, err)
				}
				return float32(v)
			}
			s.Leg, s.Seq, s.Entry = ints(1), ints(2), uint32(ints(4))
			s.X, s.Y, s.Z, s.Radius = floats(5), floats(6), floats(7), floats(8)
			s.Item, s.Param, s.Flags, s.TimeoutS = uint32(ints(9)), uint32(ints(10)), uint32(ints(11)), ints(12)
			var eerr error
			if s.Entries, eerr = parseEntries(f[13]); eerr != nil && e1 == nil {
				e1 = eerr
			}
			if e1 != nil {
				return nil, fmt.Errorf("line %d: %w", line, e1)
			}
			r.Steps = append(r.Steps, s)
		default:
			return nil, fmt.Errorf("line %d: a row before any INSERT", line)
		}
	}
	return out, sc.Err()
}

// parseEntries reads a comma list of creature entries back (” is none).
func parseEntries(text string) ([]uint32, error) {
	if text == "" {
		return nil, nil
	}
	var out []uint32
	for _, e := range strings.Split(text, ",") {
		v, err := strconv.ParseUint(e, 10, 32)
		if err != nil {
			return nil, fmt.Errorf("entries: %w", err)
		}
		out = append(out, uint32(v))
	}
	return out, nil
}

// splitTuple splits "(a, 'b', 3)," into its values; strings are unquoted.
func splitTuple(t string) ([]string, error) {
	t = strings.TrimSuffix(strings.TrimSuffix(t, ";"), ",")
	if !strings.HasPrefix(t, "(") || !strings.HasSuffix(t, ")") {
		return nil, fmt.Errorf("not a tuple: %q", clip(t, 40))
	}
	t = t[1 : len(t)-1]
	var out []string
	for i := 0; i < len(t); {
		for i < len(t) && t[i] == ' ' {
			i++
		}
		var v strings.Builder
		if i < len(t) && t[i] == '\'' {
			i++
			for {
				if i >= len(t) {
					return nil, fmt.Errorf("unterminated string")
				}
				c := t[i]
				switch {
				case c == '\\' && i+1 < len(t):
					v.WriteByte(t[i+1])
					i += 2
				case c == '\'' && i+1 < len(t) && t[i+1] == '\'':
					v.WriteByte('\'')
					i += 2
				case c == '\'':
					i++
					goto done
				default:
					v.WriteByte(c)
					i++
				}
			}
		} else {
			for i < len(t) && t[i] != ',' {
				v.WriteByte(t[i])
				i++
			}
		}
	done:
		out = append(out, strings.TrimSpace(v.String()))
		for i < len(t) && t[i] == ' ' {
			i++
		}
		if i < len(t) {
			if t[i] != ',' {
				return nil, fmt.Errorf("expected a comma at %d", i)
			}
			i++
		}
	}
	return out, nil
}

// Equal compares the rows that were built with the rows read back, and names the first difference.
func Equal(want, got []Rows) error {
	if len(want) != len(got) {
		return fmt.Errorf("%d plans, read back %d", len(want), len(got))
	}
	for i := range want {
		w, g := want[i], got[i]
		wp, gp := w.Plan, g.Plan
		for _, l := range [][2]*[]uint32{{&wp.CCPriority, &gp.CCPriority}, {&wp.CCAlways, &gp.CCAlways}, {&wp.KillFirst, &gp.KillFirst}} {
			if entriesText(*l[0]) != entriesText(*l[1]) {
				return fmt.Errorf("plan %s: row %+v, read back %+v", w.Plan.Plan, w.Plan, g.Plan)
			}
			*l[0], *l[1] = nil, nil
		}
		if wp.Plan != gp.Plan || wp.Map != gp.Map || wp.Source != gp.Source || wp.Comment != gp.Comment {
			return fmt.Errorf("plan %s: row %+v, read back %+v", w.Plan.Plan, w.Plan, g.Plan)
		}
		if len(w.Legs) != len(g.Legs) {
			return fmt.Errorf("plan %s: %d legs, read back %d", w.Plan.Plan, len(w.Legs), len(g.Legs))
		}
		for j := range w.Legs {
			if w.Legs[j] != g.Legs[j] {
				return fmt.Errorf("plan %s leg %d: %+v, read back %+v", w.Plan.Plan, j, w.Legs[j], g.Legs[j])
			}
		}
		if len(w.Needs) != len(g.Needs) {
			return fmt.Errorf("plan %s: %d need rows, read back %d", w.Plan.Plan, len(w.Needs), len(g.Needs))
		}
		for j := range w.Needs {
			if w.Needs[j] != g.Needs[j] {
				return fmt.Errorf("plan %s need %d: %+v, read back %+v", w.Plan.Plan, j, w.Needs[j], g.Needs[j])
			}
		}
		if len(w.Steps) != len(g.Steps) {
			return fmt.Errorf("plan %s: %d steps, read back %d", w.Plan.Plan, len(w.Steps), len(g.Steps))
		}
		for j := range w.Steps {
			a, b := w.Steps[j], g.Steps[j]
			if entriesText(a.Entries) != entriesText(b.Entries) {
				return fmt.Errorf("plan %s step %d: entries %v, read back %v", w.Plan.Plan, j, a.Entries, b.Entries)
			}
			a.Entries, b.Entries = nil, nil
			if fmt.Sprintf("%+v", a) != fmt.Sprintf("%+v", b) {
				return fmt.Errorf("plan %s step %d: %+v, read back %+v", w.Plan.Plan, j, a, b)
			}
		}
	}
	return nil
}
