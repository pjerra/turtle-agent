// Command spellnamegen writes internal/spells/names.tsv.gz, mounts.txt.gz and meta.tsv.gz from
// the core's sql/base/tw_world_spell_template.sql, with the later REPLACE INTO and UPDATE
// statements of sql/database_updates/world applied on top (-updates). names.tsv.gz has one
// "id<TAB>name" line per spell, in id order. mounts.txt.gz has the id of each spell that applies
// SPELL_AURA_MOUNTED in one of its effects, one per line in id order. meta.tsv.gz has the
// numbers a brain needs per spell (see meta.go); it also reads three DBCs (-dbc), since the
// template holds only indices into SpellCastTimes, SpellRange and SpellDuration.
package main

import (
	"bytes"
	"compress/gzip"
	"crypto/sha256"
	"errors"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
)

func main() {
	in := flag.String("in", "", "tw_world_spell_template.sql")
	updates := flag.String("updates", "", "directory of the core's database_updates/world: its spell_template statements are applied in file-name order (optional)")
	out := flag.String("out", "", "names.tsv.gz to write")
	mounts := flag.String("mounts", "", "mounts.txt.gz to write")
	meta := flag.String("meta", "", "meta.tsv.gz to write (needs -dbc)")
	dbc := flag.String("dbc", "", "directory holding SpellCastTimes.dbc, SpellRange.dbc and SpellDuration.dbc")
	flag.Parse()
	if *in == "" || *out == "" || *mounts == "" || (*meta != "") != (*dbc != "") {
		fmt.Fprintln(os.Stderr, "usage: spellnamegen -in tw_world_spell_template.sql [-updates database_updates/world] -out names.tsv.gz -mounts mounts.txt.gz [-meta meta.tsv.gz -dbc dir]")
		os.Exit(2)
	}
	if err := run(*in, *updates, *out, *mounts, *meta, *dbc); err != nil {
		fmt.Fprintln(os.Stderr, "spellnamegen:", err)
		os.Exit(1)
	}
}

func run(in, updates, out, mounts, meta, dbc string) error {
	src, err := os.ReadFile(in)
	if err != nil {
		return err
	}
	var ups [][]byte
	if updates != "" {
		if ups, err = readUpdates(updates); err != nil {
			return err
		}
	}
	t, err := load(src, ups...)
	if err != nil {
		return err
	}
	names, err := t.names(src, len(ups))
	if err != nil {
		return err
	}
	list, err := t.mountList(src, len(ups))
	if err != nil {
		return err
	}
	if err := os.WriteFile(out, names, 0o644); err != nil {
		return err
	}
	if err := os.WriteFile(mounts, list, 0o644); err != nil {
		return err
	}
	if meta == "" {
		return nil
	}
	d, err := readDBCs(dbc)
	if err != nil {
		return err
	}
	m, err := t.meta(src, len(ups), d)
	if err != nil {
		return err
	}
	return os.WriteFile(meta, m, 0o644)
}

// readUpdates reads, in file-name order (their timestamps), the *.sql files that mention
// spell_template.
func readUpdates(dir string) ([][]byte, error) {
	files, err := filepath.Glob(filepath.Join(dir, "*.sql"))
	if err != nil {
		return nil, err
	}
	sort.Strings(files)
	var out [][]byte
	for _, f := range files {
		b, err := os.ReadFile(f)
		if err != nil {
			return nil, err
		}
		if bytes.Contains(b, []byte("`spell_template`")) {
			out = append(out, b)
		}
	}
	return out, nil
}

// stmt finds the head of an INSERT or REPLACE of rows into spell_template; the column list
// may span lines (the core's updates write it that way).
var stmt = regexp.MustCompile("(?:INSERT|REPLACE) INTO `spell_template`\\s*\\(([^)]*)\\)\\s*VALUES")

// update finds an UPDATE of spell_template up to its ending semicolon.
var update = regexp.MustCompile("(?s)UPDATE `spell_template` SET (.*?);")

// where is the tail of an UPDATE this tool can apply: WHERE `entry` = N or IN (N, N, ...).
var where = regexp.MustCompile("(?s)^(.*?)\\s+WHERE\\s+`entry`\\s*(?:=\\s*(\\d+)|IN\\s*\\(([\\d,\\s]*)\\))\\s*$")

// auraNames are the aura type columns of the three effects. SPELL_AURA_MOUNTED is 78
// (SpellAuraDefines.h:216).
var auraNames = []string{"effectApplyAuraName1", "effectApplyAuraName2", "effectApplyAuraName3"}

const auraMounted = "78"

// want lists the template columns the tool keeps, in the order a row holds them. A column an
// INSERT does not name reads as empty (0).
var want = []string{
	"entry", "name", "nameSubtext",
	"effectApplyAuraName1", "effectApplyAuraName2", "effectApplyAuraName3",
	"school", "category", "dispel", "mechanic", "attributes", "attributesEx", "attributesEx2",
	"interruptFlags", "channelInterruptFlags", "castingTimeIndex", "recoveryTime", "categoryRecoveryTime",
	"baseLevel", "maxLevel", "spellLevel", "durationIndex", "powerType", "manaCost", "manaCostPercentage", "rangeIndex",
	"startRecoveryCategory", "startRecoveryTime",
	"reagent1", "reagent2", "reagent3", "reagent4", "reagent5", "reagent6", "reagent7", "reagent8",
	"reagentCount1", "reagentCount2", "reagentCount3", "reagentCount4", "reagentCount5", "reagentCount6", "reagentCount7", "reagentCount8",
	"effect1", "effect2", "effect3",
	"effectBasePoints1", "effectBasePoints2", "effectBasePoints3",
	"effectBaseDice1", "effectBaseDice2", "effectBaseDice3",
	"effectDieSides1", "effectDieSides2", "effectDieSides3",
	"effectRealPointsPerLevel1", "effectRealPointsPerLevel2", "effectRealPointsPerLevel3",
	"effectPointsPerComboPoint1", "effectPointsPerComboPoint2", "effectPointsPerComboPoint3",
}

// col is the index of a wanted column in a row.
var col = func() map[string]int {
	m := map[string]int{}
	for i, c := range want {
		m[c] = i
	}
	return m
}()

// requiredCols must be in every INSERT: without them a spell has no id, name or mount flag.
var requiredCols = append([]string{"entry", "name"}, auraNames...)

// table is what the dump says about each spell: the wanted columns of its latest row. A later
// row for an id replaces an earlier one, as REPLACE INTO does.
type table struct{ rows map[uint64][]string }

// load applies the base dump and then each update, in order.
func load(src []byte, updates ...[]byte) (table, error) {
	t := table{rows: map[uint64][]string{}}
	if !stmt.Match(src) {
		return t, errors.New("no INSERT INTO `spell_template`")
	}
	if err := t.apply(src); err != nil {
		return t, err
	}
	for _, u := range updates {
		if err := t.apply(u); err != nil {
			return t, err
		}
	}
	return t, nil
}

// apply reads the INSERT and REPLACE statements of rows into spell_template and the UPDATEs of a
// wanted column, in the order they stand in the file: an UPDATE changes the rows written before it
// and a later REPLACE overwrites it. Each INSERT names its columns, so the entry, name and aura
// columns are looked up per statement.
func (t table) apply(src []byte) error {
	type step struct {
		at     int
		insert []int  // stmt submatch indices
		update string // the UPDATE's text after SET
	}
	var steps []step
	for _, loc := range stmt.FindAllSubmatchIndex(src, -1) {
		steps = append(steps, step{at: loc[0], insert: loc})
	}
	for _, m := range update.FindAllSubmatchIndex(src, -1) {
		steps = append(steps, step{at: m[0], update: string(src[m[2]:m[3]])})
	}
	sort.Slice(steps, func(i, j int) bool { return steps[i].at < steps[j].at })
	for _, st := range steps {
		var err error
		if st.insert != nil {
			err = t.insertAt(src, st.insert)
		} else {
			err = t.updateOne(st.update)
		}
		if err != nil {
			return err
		}
	}
	return nil
}

// insertAt applies one INSERT or REPLACE statement found by stmt.
func (t table) insertAt(src []byte, loc []int) error {
	cols := strings.Split(strings.ReplaceAll(string(src[loc[2]:loc[3]]), "`", ""), ",")
	at := map[string]int{}
	for i, c := range cols {
		at[strings.TrimSpace(c)] = i
	}
	for _, c := range requiredCols {
		if _, ok := at[c]; !ok {
			return errors.New("an INSERT without entry, name or effectApplyAuraName1-3")
		}
	}
	pos := make([]int, len(want)) // column of the statement for each wanted column, -1 when absent
	for i, w := range want {
		p, ok := at[w]
		if !ok {
			p = -1
		}
		pos[i] = p
	}
	rows, err := tuples(src[loc[1]:])
	if err != nil {
		return err
	}
	for _, r := range rows {
		if len(r) != len(cols) {
			return fmt.Errorf("a row of %d values for %d columns", len(r), len(cols))
		}
		id, err := strconv.ParseUint(r[at["entry"]], 10, 32)
		if err != nil {
			return fmt.Errorf("entry %q: %w", r[at["entry"]], err)
		}
		row := make([]string, len(want))
		for i, p := range pos {
			if p >= 0 {
				row[i] = strings.TrimSpace(r[p])
			}
		}
		t.rows[id] = row
	}
	return nil
}

// updateOne applies one UPDATE if it sets a wanted column. One that sets only other columns
// (script_name, the bonus coefficients) is skipped; one this tool cannot read is an error.
func (t table) updateOne(body string) error {
	w := where.FindStringSubmatch(body)
	if w == nil {
		if setsWanted(body) {
			return fmt.Errorf("an UPDATE of spell_template this tool cannot read: %.80q", body)
		}
		return nil
	}
	set, err := assignments(w[1])
	if err != nil {
		return err
	}
	touched := false
	for c := range set {
		if _, ok := col[c]; ok {
			touched = true
		}
	}
	if !touched {
		return nil
	}
	var ids []uint64
	for _, f := range strings.FieldsFunc(w[2]+","+w[3], func(r rune) bool { return r == ',' || r == ' ' || r == '\n' || r == '\r' || r == '\t' }) {
		id, err := strconv.ParseUint(f, 10, 32)
		if err != nil {
			return fmt.Errorf("UPDATE entry %q: %w", f, err)
		}
		ids = append(ids, id)
	}
	for _, id := range ids {
		row, ok := t.rows[id]
		if !ok {
			continue // an update of a spell the dump does not hold changes nothing
		}
		for c, v := range set {
			if i, ok := col[c]; ok {
				row[i] = v
			}
		}
	}
	return nil
}

// setsWanted reports whether an UPDATE body assigns one of the wanted columns (its SET part,
// not its WHERE).
func setsWanted(body string) bool {
	if i := strings.Index(body, " WHERE "); i >= 0 {
		body = body[:i]
	}
	for _, c := range want {
		if strings.Contains(body, "`"+c+"`") {
			return true
		}
	}
	return false
}

// assignments reads "`a` = 1, `b` = 'x'" into a map, values as tuples reads them.
func assignments(s string) (map[string]string, error) {
	out := map[string]string{}
	var name, cur strings.Builder
	inStr, inName, haveEq := false, false, false
	flush := func() error {
		if name.Len() == 0 {
			return errors.New("an UPDATE assignment without a column")
		}
		out[name.String()] = value(strings.TrimSpace(cur.String()), cur.Len() > 0)
		name.Reset()
		cur.Reset()
		haveEq = false
		return nil
	}
	for i := 0; i < len(s); i++ {
		c := s[i]
		switch {
		case inStr:
			switch c {
			case '\\':
				if i+1 < len(s) {
					i++
					cur.WriteByte(unescape(s[i]))
				}
			case '\'':
				if i+1 < len(s) && s[i+1] == '\'' {
					cur.WriteByte('\'')
					i++
				} else {
					inStr = false
				}
			default:
				cur.WriteByte(c)
			}
		case c == '`':
			inName = !inName
		case inName:
			name.WriteByte(c)
		case c == '=' && !haveEq:
			haveEq = true
		case c == '\'':
			inStr = true
			cur.WriteByte(0) // marks a string value; trimmed below
		case c == ',' && haveEq:
			if err := flush(); err != nil {
				return nil, err
			}
		case haveEq:
			cur.WriteByte(c)
		}
	}
	if inStr {
		return nil, errors.New("an UPDATE with an unterminated string")
	}
	if haveEq {
		if err := flush(); err != nil {
			return nil, err
		}
	}
	for k, v := range out {
		out[k] = strings.Trim(v, "\x00 ")
	}
	return out, nil
}

// Generate returns names.tsv.gz: a header, then the "id<TAB>name" lines in id order, gzipped.
func Generate(src []byte) ([]byte, error) {
	t, err := load(src)
	if err != nil {
		return nil, err
	}
	return t.names(src, 0)
}

// GenerateMounts returns mounts.txt.gz: a header, then the id of each spell that applies
// SPELL_AURA_MOUNTED in one of its effects, one per line in id order, gzipped.
func GenerateMounts(src []byte) ([]byte, error) {
	t, err := load(src)
	if err != nil {
		return nil, err
	}
	return t.mountList(src, 0)
}

// sortedIDs lists the ids of the table in order.
func (t table) sortedIDs() []uint64 {
	ids := make([]uint64, 0, len(t.rows))
	for id := range t.rows {
		ids = append(ids, id)
	}
	sort.Slice(ids, func(i, j int) bool { return ids[i] < ids[j] })
	return ids
}

// origin is the header's account of where the table came from.
func origin(src []byte, updates int) string {
	if updates == 0 {
		return fmt.Sprintf("tw_world_spell_template.sql (sha256 %x)", sha256.Sum256(src))
	}
	return fmt.Sprintf("tw_world_spell_template.sql (sha256 %x) and %d database_updates files", sha256.Sum256(src), updates)
}

func (t table) names(src []byte, updates int) ([]byte, error) {
	var tsv bytes.Buffer
	fmt.Fprintf(&tsv, "# generated by tools/spellnamegen from %s; DO NOT EDIT\n", origin(src, updates))
	for _, id := range t.sortedIDs() {
		if n := t.rows[id][col["name"]]; n != "" {
			fmt.Fprintf(&tsv, "%d\t%s\n", id, strings.NewReplacer("\t", " ", "\n", " ", "\r", " ").Replace(n))
		}
	}
	return gzipped(tsv.Bytes())
}

func (t table) mountList(src []byte, updates int) ([]byte, error) {
	var txt bytes.Buffer
	fmt.Fprintf(&txt, "# generated by tools/spellnamegen from %s: spells with SPELL_AURA_MOUNTED (78) in an effect; DO NOT EDIT\n", origin(src, updates))
	for _, id := range t.sortedIDs() {
		for _, a := range auraNames {
			if t.rows[id][col[a]] == auraMounted {
				fmt.Fprintf(&txt, "%d\n", id)
				break
			}
		}
	}
	return gzipped(txt.Bytes())
}

func gzipped(b []byte) ([]byte, error) {
	var gz bytes.Buffer
	w, _ := gzip.NewWriterLevel(&gz, gzip.BestCompression)
	if _, err := w.Write(b); err != nil {
		return nil, err
	}
	if err := w.Close(); err != nil {
		return nil, err
	}
	return gz.Bytes(), nil
}

// tuples reads "(v, 'text', ...), (...);" up to the semicolon that ends the statement:
// numbers, NULL and single-quoted strings with MySQL backslash escapes.
func tuples(b []byte) ([][]string, error) {
	var rows [][]string
	var row []string
	var cur strings.Builder
	inRow, inStr, have := false, false, false
	for i := 0; i < len(b); i++ {
		c := b[i]
		switch {
		case inStr:
			switch c {
			case '\\':
				if i+1 < len(b) {
					i++
					cur.WriteByte(unescape(b[i]))
				}
			case '\'':
				if i+1 < len(b) && b[i+1] == '\'' {
					cur.WriteByte('\'')
					i++
				} else {
					inStr = false
				}
			default:
				cur.WriteByte(c)
			}
		case c == '\'':
			inStr, have = true, true
		case c == '(' && !inRow:
			inRow, row = true, nil
		case c == ',' && inRow:
			row = append(row, value(cur.String(), have))
			cur.Reset()
			have = false
		case c == ')' && inRow:
			row = append(row, value(cur.String(), have))
			cur.Reset()
			have, inRow = false, false
			rows = append(rows, row)
		case c == ';' && !inRow:
			return rows, nil
		case inRow && c != ' ' && c != '\n' && c != '\r' && c != '\t':
			cur.WriteByte(c)
			have = true
		}
	}
	return nil, errors.New("statement not ended by ;")
}

// unescape is the character a MySQL backslash escape stands for.
func unescape(c byte) byte {
	switch c {
	case 'n':
		return '\n'
	case 't':
		return '\t'
	case 'r':
		return '\r'
	case '0':
		return 0
	}
	return c
}

func value(s string, have bool) string {
	if !have || s == "NULL" {
		return ""
	}
	return s
}
