// Command plandrift compares the dungeon plans in turtle-agent's dungeons/ folder with the newest export of each plan
// in TortoiseBots' world migrations (tb/data/sql/world/*.sql).
//
// The plan file is the source: plan-export writes the ai_dungeon_plan/leg/step rows. This tool builds the rows the
// current exporter writes and finds them (by plan) in the newest migration that exports them. Per plan it reports:
//
//   - in sync: the plan row's map and unit lists, the leg names and every step row are equal;
//   - differs: the row counts and the first differing step;
//   - not exported upstream: no migration exports the plan;
//   - current export refused: plan-export cannot map the plan (it names the reason).
//
// Source and comment columns are not compared: the source names the exporting commit and so changes on every export,
// and the comments carry export-time notes. What the run leader reads (map, unit lists, legs, steps) is compared.
//
// The exit status is 0 (report-only) unless --fail is given: then 1 when an exported plan differs. A parse or load
// error is 2.
package main

import (
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"

	"github.com/pjerra/turtle-agent/internal/dungeon"
	"github.com/pjerra/turtle-agent/internal/planexport"
)

const (
	stateInSync      = "in sync"
	stateDiffers     = "differs"
	stateNotExported = "not exported upstream"
	stateRefused     = "current export refused"
)

// migrationName is a TortoiseBots world migration: YYYYMMDDHHMMSS_world.sql.
var migrationName = regexp.MustCompile(`^\d{14}_world\.sql$`)

// Export is the newest migration that exports a plan, and its rows.
type Export struct {
	File string
	Rows planexport.Rows
}

// Result is one plan's comparison.
type Result struct {
	Plan         string `json:"plan"`
	Status       string `json:"status"`
	Upstream     string `json:"upstream,omitempty"`
	State        string `json:"state"`
	UpstreamLegs int    `json:"upstream_legs,omitempty"`
	UpstreamStep int    `json:"upstream_steps,omitempty"`
	CurrentLegs  int    `json:"current_legs,omitempty"`
	CurrentSteps int    `json:"current_steps,omitempty"`
	FirstDiff    string `json:"first_diff,omitempty"`
	FirstStep    string `json:"first_step_diff,omitempty"`
	Error        string `json:"error,omitempty"`
}

// Report is the whole run.
type Report struct {
	Plans  []Result `json:"plans"`
	SQLDir string   `json:"sql_dir"`
}

func main() {
	plans := flag.String("plans", "dungeons", "folder with the plan JSON files")
	sqlDir := flag.String("sql", "tb/data/sql/world", "folder with TortoiseBots' world migrations")
	fail := flag.Bool("fail", false, "exit 1 when an exported plan differs (the first CI version is report-only)")
	asJSON := flag.Bool("json", false, "print the report as JSON")
	flag.Parse()
	rep, err := Run(*plans, *sqlDir)
	if err != nil {
		fmt.Fprintln(os.Stderr, "plandrift:", err)
		os.Exit(2)
	}
	if *asJSON {
		if err := WriteJSON(os.Stdout, rep); err != nil {
			fmt.Fprintln(os.Stderr, "plandrift:", err)
			os.Exit(2)
		}
	} else {
		WriteMarkdown(os.Stdout, rep)
	}
	if *fail && len(Differences(rep)) > 0 {
		os.Exit(1)
	}
}

// Run compares the plans in plansDir with the newest exports in sqlDir.
func Run(plansDir, sqlDir string) (Report, error) {
	exports, err := LoadExports(sqlDir)
	if err != nil {
		return Report{}, err
	}
	files, err := filepath.Glob(filepath.Join(plansDir, "*.json"))
	if err != nil {
		return Report{}, err
	}
	sort.Strings(files)
	rep := Report{SQLDir: sqlDir}
	for _, file := range files {
		base := strings.TrimSuffix(filepath.Base(file), ".json")
		if strings.HasSuffix(base, ".tune") {
			continue // a tunables file, not a plan
		}
		p, err := dungeon.Load(file)
		if err != nil {
			return Report{}, fmt.Errorf("%s: %w", file, err)
		}
		name := p.Name
		if name == "" {
			name = base
		}
		res := Result{Plan: name, Status: p.Status, State: stateNotExported}
		up, ok := exports[name]
		if !ok {
			up, ok = exports[base]
		}
		if ok {
			res.Upstream = up.File
			res.UpstreamLegs, res.UpstreamStep = len(up.Rows.Legs), len(up.Rows.Steps)
		}
		cur, err := planexport.Build(p, planexport.Options{DropCompanionsLoot: true, CarrierChests: true})
		if err != nil {
			res.State = stateRefused
			res.Error = err.Error()
			rep.Plans = append(rep.Plans, res)
			continue
		}
		res.CurrentLegs, res.CurrentSteps = len(cur.Rows.Legs), len(cur.Rows.Steps)
		if !ok {
			rep.Plans = append(rep.Plans, res)
			continue
		}
		first, firstStep := CompareRows(up.Rows, cur.Rows)
		if first != "" {
			res.State = stateDiffers
			res.FirstDiff = first
			res.FirstStep = firstStep
		} else {
			res.State = stateInSync
		}
		rep.Plans = append(rep.Plans, res)
	}
	return rep, nil
}

// Differences returns the plans that differ or whose current export is refused.
func Differences(rep Report) []Result {
	var out []Result
	for _, r := range rep.Plans {
		if r.State == stateDiffers || r.State == stateRefused {
			out = append(out, r)
		}
	}
	return out
}

// LoadExports reads the newest migration that exports each plan from sqlDir. Files are read in name order (the
// timestamp is the name), so a later migration replaces an earlier export of the same plan.
func LoadExports(sqlDir string) (map[string]Export, error) {
	entries, err := os.ReadDir(sqlDir)
	if err != nil {
		return nil, err
	}
	var files []string
	for _, e := range entries {
		if !e.IsDir() && migrationName.MatchString(e.Name()) {
			files = append(files, e.Name())
		}
	}
	sort.Strings(files)
	out := map[string]Export{}
	for _, name := range files {
		data, err := os.ReadFile(filepath.Join(sqlDir, name))
		if err != nil {
			return nil, err
		}
		plans, err := ParseMigration(string(data))
		if err != nil {
			return nil, fmt.Errorf("%s: %w", name, err)
		}
		for plan, rows := range plans {
			out[plan] = Export{File: name, Rows: rows}
		}
	}
	return out, nil
}

// ParseMigration reads the ai_dungeon_plan, ai_dungeon_leg and ai_dungeon_step rows of a migration. Other tables and
// statements (schema, ai_dungeon_leg_need, ai_dungeon_boss, comments) are ignored, so a world migration with any
// number of unrelated blocks can be read. Column names are read from the INSERT, not assumed in one order.
func ParseMigration(sql string) (map[string]planexport.Rows, error) {
	out := map[string]planexport.Rows{}
	exported := map[string]bool{}
	for _, stmt := range splitStatements(stripLineComments(sql)) {
		table, cols, body, ok := insertParts(stmt)
		if !ok {
			continue
		}
		switch table {
		case "ai_dungeon_plan", "ai_dungeon_leg", "ai_dungeon_step":
		default:
			continue
		}
		tuples, err := parseTuples(body)
		if err != nil {
			return nil, fmt.Errorf("INSERT INTO %s: %w", table, err)
		}
		for _, vals := range tuples {
			if len(vals) != len(cols) {
				return nil, fmt.Errorf("INSERT INTO %s: %d columns, %d values", table, len(cols), len(vals))
			}
			row := make(map[string]string, len(cols))
			for i, c := range cols {
				row[c] = vals[i]
			}
			if table == "ai_dungeon_plan" {
				exported[row["plan"]] = true
			}
			if err := addRow(out, table, row); err != nil {
				return nil, err
			}
		}
	}
	// A migration that only replaces one leg's steps is an update, not an export of the plan; the newest export is the
	// newest file with the plan's ai_dungeon_plan row.
	for plan := range out {
		if !exported[plan] {
			delete(out, plan)
		}
	}
	return out, nil
}

func addRow(out map[string]planexport.Rows, table string, row map[string]string) error {
	plan := row["plan"]
	if plan == "" {
		return fmt.Errorf("%s row without a plan", table)
	}
	r, ok := out[plan]
	if !ok {
		out[plan] = planexport.Rows{Plan: planexport.PlanRow{Plan: plan}}
		r = out[plan]
	}
	switch table {
	case "ai_dungeon_plan":
		ccp, err := parseEntries(row["cc_priority"])
		if err != nil {
			return fmt.Errorf("plan %s: cc_priority: %w", plan, err)
		}
		cca, err := parseEntries(row["cc_always"])
		if err != nil {
			return fmt.Errorf("plan %s: cc_always: %w", plan, err)
		}
		kf, err := parseEntries(row["kill_first"])
		if err != nil {
			return fmt.Errorf("plan %s: kill_first: %w", plan, err)
		}
		m, err := strconv.ParseUint(strings.TrimSpace(row["map"]), 10, 32)
		if err != nil {
			return fmt.Errorf("plan %s: map: %w", plan, err)
		}
		r.Plan = planexport.PlanRow{Plan: plan, Map: uint32(m), CCPriority: ccp, CCAlways: cca, KillFirst: kf, Source: row["source"], Comment: row["comment"]}
	case "ai_dungeon_leg":
		leg, err := strconv.Atoi(strings.TrimSpace(row["leg"]))
		if err != nil {
			return fmt.Errorf("plan %s: leg: %w", plan, err)
		}
		r.Legs = append(r.Legs, planexport.LegRow{Plan: plan, Leg: leg, Name: row["name"], Comment: row["comment"]})
	case "ai_dungeon_step":
		s, err := stepFromRow(plan, row)
		if err != nil {
			return err
		}
		r.Steps = append(r.Steps, s)
	}
	out[plan] = r
	return nil
}

func stepFromRow(plan string, row map[string]string) (planexport.StepRow, error) {
	s := planexport.StepRow{Plan: plan, Kind: row["kind"], Comment: row["comment"]}
	var err error
	if s.Leg, err = strconv.Atoi(strings.TrimSpace(row["leg"])); err != nil {
		return s, fmt.Errorf("plan %s: step leg: %w", plan, err)
	}
	if s.Seq, err = strconv.Atoi(strings.TrimSpace(row["seq"])); err != nil {
		return s, fmt.Errorf("plan %s: step seq: %w", plan, err)
	}
	ints := []struct {
		name string
		dst  *int
	}{{"timeout_s", &s.TimeoutS}}
	for _, v := range ints {
		if *v.dst, err = strconv.Atoi(strings.TrimSpace(row[v.name])); err != nil {
			return s, fmt.Errorf("plan %s leg %d seq %d: %s: %w", plan, s.Leg, s.Seq, v.name, err)
		}
	}
	u32 := []struct {
		name string
		dst  *uint32
	}{{"entry", &s.Entry}, {"item", &s.Item}, {"param", &s.Param}, {"flags", &s.Flags}}
	for _, v := range u32 {
		n, err := strconv.ParseUint(strings.TrimSpace(row[v.name]), 10, 32)
		if err != nil {
			return s, fmt.Errorf("plan %s leg %d seq %d: %s: %w", plan, s.Leg, s.Seq, v.name, err)
		}
		*v.dst = uint32(n)
	}
	f32 := []struct {
		name string
		dst  *float32
	}{{"x", &s.X}, {"y", &s.Y}, {"z", &s.Z}, {"radius", &s.Radius}}
	for _, v := range f32 {
		f, err := strconv.ParseFloat(strings.TrimSpace(row[v.name]), 32)
		if err != nil {
			return s, fmt.Errorf("plan %s leg %d seq %d: %s: %w", plan, s.Leg, s.Seq, v.name, err)
		}
		*v.dst = float32(f)
	}
	if s.Entries, err = parseEntries(row["entries"]); err != nil {
		return s, fmt.Errorf("plan %s leg %d seq %d: entries: %w", plan, s.Leg, s.Seq, err)
	}
	return s, nil
}

func parseEntries(text string) ([]uint32, error) {
	if text == "" {
		return nil, nil
	}
	var out []uint32
	for _, e := range strings.Split(text, ",") {
		v, err := strconv.ParseUint(strings.TrimSpace(e), 10, 32)
		if err != nil {
			return nil, err
		}
		out = append(out, uint32(v))
	}
	return out, nil
}

// CompareRows returns the first difference and the first differing step ("" when equal). The plan row's Source and
// Comment are not part of the comparison (see the package comment).
func CompareRows(up, cur planexport.Rows) (first, firstStep string) {
	firstStep = firstStepDiff(up.Steps, cur.Steps)
	if up.Plan.Map != cur.Plan.Map {
		return fmt.Sprintf("plan map: upstream %d, current %d", up.Plan.Map, cur.Plan.Map), firstStep
	}
	for _, l := range []struct {
		name string
		a, b []uint32
	}{{"cc_priority", up.Plan.CCPriority, cur.Plan.CCPriority}, {"cc_always", up.Plan.CCAlways, cur.Plan.CCAlways}, {"kill_first", up.Plan.KillFirst, cur.Plan.KillFirst}} {
		if !equalEntries(l.a, l.b) {
			return fmt.Sprintf("plan %s: upstream %q, current %q", l.name, entriesText(l.a), entriesText(l.b)), firstStep
		}
	}
	if len(up.Legs) != len(cur.Legs) {
		return fmt.Sprintf("legs: upstream %d, current %d", len(up.Legs), len(cur.Legs)), firstStep
	}
	ul := append([]planexport.LegRow(nil), up.Legs...)
	cl := append([]planexport.LegRow(nil), cur.Legs...)
	sort.Slice(ul, func(i, j int) bool { return ul[i].Leg < ul[j].Leg })
	sort.Slice(cl, func(i, j int) bool { return cl[i].Leg < cl[j].Leg })
	for i := range ul {
		if ul[i].Leg != cl[i].Leg {
			return fmt.Sprintf("legs: upstream leg %d, current leg %d", ul[i].Leg, cl[i].Leg), firstStep
		}
		if ul[i].Name != cl[i].Name {
			return fmt.Sprintf("leg %d name: upstream %q, current %q", ul[i].Leg, ul[i].Name, cl[i].Name), firstStep
		}
	}
	return firstStep, firstStep
}

func firstStepDiff(up, cur []planexport.StepRow) string {
	type key struct{ leg, seq int }
	um := map[key]planexport.StepRow{}
	cm := map[key]planexport.StepRow{}
	for _, s := range up {
		um[key{s.Leg, s.Seq}] = s
	}
	for _, s := range cur {
		cm[key{s.Leg, s.Seq}] = s
	}
	keys := make([]key, 0, len(um)+len(cm))
	for k := range um {
		keys = append(keys, k)
	}
	for k := range cm {
		if _, ok := um[k]; !ok {
			keys = append(keys, k)
		}
	}
	sort.Slice(keys, func(i, j int) bool {
		if keys[i].leg != keys[j].leg {
			return keys[i].leg < keys[j].leg
		}
		return keys[i].seq < keys[j].seq
	})
	for _, k := range keys {
		a, aok := um[k]
		b, bok := cm[k]
		switch {
		case !aok:
			return fmt.Sprintf("leg %d seq %d: current has %s %q, upstream has no step", k.leg, k.seq, b.Kind, b.Comment)
		case !bok:
			return fmt.Sprintf("leg %d seq %d: upstream has %s %q, current has no step", k.leg, k.seq, a.Kind, a.Comment)
		}
		if f := diffStep(a, b); f != "" {
			return fmt.Sprintf("leg %d seq %d: %s", k.leg, k.seq, f)
		}
	}
	return ""
}

func diffStep(a, b planexport.StepRow) string {
	switch {
	case a.Kind != b.Kind:
		return fmt.Sprintf("kind upstream %q, current %q", a.Kind, b.Kind)
	case a.Entry != b.Entry:
		return fmt.Sprintf("entry upstream %d, current %d", a.Entry, b.Entry)
	case a.X != b.X || a.Y != b.Y || a.Z != b.Z:
		return fmt.Sprintf("point upstream %.4g %.4g %.4g, current %.4g %.4g %.4g", a.X, a.Y, a.Z, b.X, b.Y, b.Z)
	case a.Radius != b.Radius:
		return fmt.Sprintf("radius upstream %g, current %g", a.Radius, b.Radius)
	case a.Item != b.Item:
		return fmt.Sprintf("item upstream %d, current %d", a.Item, b.Item)
	case a.Param != b.Param:
		return fmt.Sprintf("param upstream %d, current %d", a.Param, b.Param)
	case a.Flags != b.Flags:
		return fmt.Sprintf("flags upstream %d, current %d", a.Flags, b.Flags)
	case a.TimeoutS != b.TimeoutS:
		return fmt.Sprintf("timeout_s upstream %d, current %d", a.TimeoutS, b.TimeoutS)
	case !equalEntries(a.Entries, b.Entries):
		return fmt.Sprintf("entries upstream %q, current %q", entriesText(a.Entries), entriesText(b.Entries))
	case a.Comment != b.Comment:
		return fmt.Sprintf("comment upstream %q, current %q", a.Comment, b.Comment)
	}
	return ""
}

func equalEntries(a, b []uint32) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func entriesText(es []uint32) string {
	parts := make([]string, len(es))
	for i, e := range es {
		parts[i] = strconv.FormatUint(uint64(e), 10)
	}
	return strings.Join(parts, ",")
}

// stripLineComments drops full-line SQL comments. The exporter never puts a comment after code on the same line.
func stripLineComments(sql string) string {
	var b strings.Builder
	for _, line := range strings.Split(sql, "\n") {
		if strings.HasPrefix(strings.TrimSpace(line), "--") {
			continue
		}
		b.WriteString(line)
		b.WriteByte('\n')
	}
	return b.String()
}

// splitStatements splits on semicolons outside single-quoted strings.
func splitStatements(sql string) []string {
	var out []string
	start := 0
	quote := false
	for i := 0; i < len(sql); i++ {
		c := sql[i]
		if quote {
			switch {
			case c == '\\' && i+1 < len(sql):
				i++
			case c == '\'' && i+1 < len(sql) && sql[i+1] == '\'':
				i++
			case c == '\'':
				quote = false
			}
			continue
		}
		switch c {
		case '\'':
			quote = true
		case ';':
			out = append(out, sql[start:i])
			start = i + 1
		}
	}
	if s := strings.TrimSpace(sql[start:]); s != "" {
		out = append(out, s)
	}
	return out
}

// insertParts splits an INSERT INTO `table` (`cols`) VALUES (rows) statement.
func insertParts(stmt string) (table string, cols []string, body string, ok bool) {
	stmt = strings.TrimSpace(stmt)
	upper := strings.ToUpper(stmt)
	if !strings.HasPrefix(upper, "INSERT INTO") {
		return "", nil, "", false
	}
	rest := strings.TrimSpace(stmt[len("INSERT INTO"):])
	if !strings.HasPrefix(rest, "`") {
		return "", nil, "", false
	}
	end := strings.Index(rest[1:], "`")
	if end < 0 {
		return "", nil, "", false
	}
	table = rest[1 : 1+end]
	rest = rest[end+2:]
	open := strings.Index(rest, "(")
	if open < 0 {
		return "", nil, "", false
	}
	close := matchingParen(rest, open)
	if close < 0 {
		return "", nil, "", false
	}
	for _, c := range strings.Split(rest[open+1:close], ",") {
		cols = append(cols, strings.Trim(strings.TrimSpace(c), "`"))
	}
	vi := strings.Index(strings.ToUpper(rest[close+1:]), "VALUES")
	if vi < 0 {
		return "", nil, "", false
	}
	return table, cols, rest[close+1+vi+len("VALUES"):], true
}

func matchingParen(s string, open int) int {
	depth := 0
	quote := false
	for i := open; i < len(s); i++ {
		c := s[i]
		if quote {
			switch {
			case c == '\\' && i+1 < len(s):
				i++
			case c == '\'' && i+1 < len(s) && s[i+1] == '\'':
				i++
			case c == '\'':
				quote = false
			}
			continue
		}
		switch c {
		case '\'':
			quote = true
		case '(':
			depth++
		case ')':
			depth--
			if depth == 0 {
				return i
			}
		}
	}
	return -1
}

func parseTuples(body string) ([][]string, error) {
	var out [][]string
	for i := 0; i < len(body); {
		for i < len(body) && (body[i] == ',' || body[i] == ' ' || body[i] == '\n' || body[i] == '\r' || body[i] == '\t') {
			i++
		}
		if i >= len(body) || body[i] == ';' {
			break
		}
		if body[i] != '(' {
			return nil, fmt.Errorf("expected a row at %q", clip(body[i:], 30))
		}
		end := matchingParen(body, i)
		if end < 0 {
			return nil, errors.New("unterminated row")
		}
		vals, err := splitTuple(body[i : end+1])
		if err != nil {
			return nil, err
		}
		out = append(out, vals)
		i = end + 1
	}
	return out, nil
}

func splitTuple(t string) ([]string, error) {
	if !strings.HasPrefix(t, "(") || !strings.HasSuffix(t, ")") {
		return nil, fmt.Errorf("not a row: %q", clip(t, 40))
	}
	t = t[1 : len(t)-1]
	var out []string
	for i := 0; i < len(t); {
		for i < len(t) && (t[i] == ' ' || t[i] == '\n' || t[i] == '\r' || t[i] == '\t') {
			i++
		}
		var v strings.Builder
		if i < len(t) && t[i] == '\'' {
			i++
			for {
				if i >= len(t) {
					return nil, errors.New("unterminated string")
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
		for i < len(t) && (t[i] == ' ' || t[i] == '\n' || t[i] == '\r' || t[i] == '\t') {
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

func clip(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "..."
}

// WriteMarkdown writes the job summary.
func WriteMarkdown(w io.Writer, rep Report) {
	total, synced, different, missing, refused := len(rep.Plans), 0, 0, 0, 0
	for _, r := range rep.Plans {
		switch r.State {
		case stateInSync:
			synced++
		case stateDiffers:
			different++
		case stateNotExported:
			missing++
		case stateRefused:
			refused++
		}
	}
	fmt.Fprintf(w, "## Dungeon plans vs TortoiseBots dev exports\n\n")
	fmt.Fprintf(w, "Checked %d plan(s) against the newest export in `%s`: **%d in sync**, **%d differ**, **%d not exported upstream**, **%d whose current export is refused**.\n\n", total, rep.SQLDir, synced, different, missing, refused)
	fmt.Fprintln(w, "| Plan | Status | Newest export | Result |")
	fmt.Fprintln(w, "| :--- | :--- | :--- | :--- |")
	for _, r := range rep.Plans {
		up := r.Upstream
		if up == "" {
			up = "—"
		}
		detail := r.State
		if r.State == stateDiffers {
			detail = fmt.Sprintf("%s: upstream %d legs/%d steps, current %d/%d", r.State, r.UpstreamLegs, r.UpstreamStep, r.CurrentLegs, r.CurrentSteps)
		}
		if r.State == stateRefused {
			detail = "current export refused"
		}
		fmt.Fprintf(w, "| `%s` | %s | %s | %s |\n", r.Plan, r.Status, up, detail)
	}
	if d := Differences(rep); len(d) > 0 {
		fmt.Fprint(w, "\n### Differences\n\n")
		for _, r := range d {
			switch r.State {
			case stateDiffers:
				fmt.Fprintf(w, "- `%s`: %s (upstream `%s`: %d legs, %d steps; current: %d legs, %d steps)\n", r.Plan, r.FirstDiff, r.Upstream, r.UpstreamLegs, r.UpstreamStep, r.CurrentLegs, r.CurrentSteps)
				if r.FirstStep != "" && r.FirstStep != r.FirstDiff {
					fmt.Fprintf(w, "  - first differing step: %s\n", r.FirstStep)
				}
			case stateRefused:
				fmt.Fprintf(w, "- `%s`: the current export is refused: %s\n", r.Plan, r.Error)
			}
		}
	}
}

// WriteJSON writes the machine report.
func WriteJSON(w io.Writer, rep Report) error {
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	return enc.Encode(rep)
}
