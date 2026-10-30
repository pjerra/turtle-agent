package scenario

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os/exec"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"
)

// allowedTables is the read-only allowlist (spec S2): a scenario cannot widen
// it; a change is a code change with review. Nothing from tw_logon (account
// hashes), tw_logs, mysql, information_schema, performance_schema or sys.
var allowedTables = map[string]bool{
	"tw_world.lft_user_groups":      true,
	"tw_world.ai_dungeon_plan":      true,
	"tw_world.ai_dungeon_leg":       true,
	"tw_world.ai_dungeon_step":      true,
	"tw_world.ai_boss_tactic":       true,
	"tw_world.migrations":           true,
	"tw_char.characters":            true,
	"tw_char.character_inventory":   true,
	"tw_char.item_instance":         true,
	"tw_char.ai_run_given_item":     true,
	"tw_char.groups":                true,
	"tw_char.group_member":          true,
	"tw_char.character_queststatus": true,
	"tw_char.mail":                  true,
	"tw_char.mail_items":            true,
}

func allowedTable(table string) bool { return allowedTables[table] }

// DBQuery is one read-only count: either a table row count or an item count.
// Built by the runner from a check_db step, rendered by BuildSQL below.
type DBQuery struct {
	Table string
	Where map[string]any
	Op    string
	Want  int64
	Item  *ItemQuery
}

// BuildSQL renders a DBQuery as exactly one of the two allowed SELECT forms.
// Integer values render decimal; strings (already restricted by the loader's
// value regex: no quotes, backslashes or semicolons) go in single quotes.
// Where keys sort so the text is stable.
func BuildSQL(q DBQuery) (string, error) {
	if q.Item != nil {
		return buildItemSQL(q)
	}
	if !allowedTable(q.Table) {
		return "", fmt.Errorf("table %s is not on the read-only allowlist (internal/scenario/db.go)", q.Table)
	}
	var b strings.Builder
	fmt.Fprintf(&b, "SELECT COUNT(*) FROM %s", q.Table)
	if len(q.Where) > 0 {
		keys := make([]string, 0, len(q.Where))
		for k := range q.Where {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		b.WriteString(" WHERE ")
		for i, k := range keys {
			if i > 0 {
				b.WriteString(" AND ")
			}
			switch v := q.Where[k].(type) {
			case string:
				fmt.Fprintf(&b, "%s = '%s'", k, v)
			case float64:
				fmt.Fprintf(&b, "%s = %d", k, int64(v))
			case int64:
				fmt.Fprintf(&b, "%s = %d", k, v)
			case int:
				fmt.Fprintf(&b, "%s = %d", k, v)
			default:
				return "", fmt.Errorf("bad where value for %q", k)
			}
		}
	}
	sql := b.String()
	if err := checkSelectOnly(sql); err != nil {
		return "", err
	}
	return sql, nil
}

// buildItemSQL renders the item-count form: the stack sum of one entry over
// the named characters' rows (bags, bank, keyring, mail). Names sort so the
// text is stable.
func buildItemSQL(q DBQuery) (string, error) {
	names := []string{}
	if q.Item.Char != "" {
		names = append(names, q.Item.Char)
	}
	names = append(names, q.Item.Chars...)
	sort.Strings(names)
	var b strings.Builder
	b.WriteString("SELECT COALESCE(SUM(ii.count), 0) FROM tw_char.item_instance ii JOIN tw_char.characters c ON c.guid = ii.owner_guid WHERE c.name IN (")
	for i, n := range names {
		if i > 0 {
			b.WriteString(", ")
		}
		fmt.Fprintf(&b, "'%s'", n)
	}
	fmt.Fprintf(&b, ") AND ii.itemEntry = %d", q.Item.Entry)
	if q.Item.Flags != nil {
		fmt.Fprintf(&b, " AND ii.flags = %d", *q.Item.Flags)
	}
	sql := b.String()
	if err := checkSelectOnly(sql); err != nil {
		return "", err
	}
	return sql, nil
}

// forbiddenWords are refused outside single-quoted literals (case-insensitive,
// whole word): anything that writes, locks, loads files or sleeps.
var forbiddenWords = []string{
	"INTO", "OUTFILE", "DUMPFILE", "LOAD_FILE", "INSERT", "UPDATE", "DELETE",
	"REPLACE", "DROP", "ALTER", "CREATE", "TRUNCATE", "GRANT", "REVOKE", "SET",
	"CALL", "LOCK", "UNLOCK", "HANDLER", "DO", "SLEEP", "BENCHMARK",
	"GET_LOCK", "RELEASE_LOCK", "SHUTDOWN", "KILL",
}

var wordRe = regexp.MustCompile(`[A-Za-z0-9_]+`)

// checkSelectOnly is the defence-in-depth guard on the final string (S3):
// one SELECT, no statement stacking or escapes, no writes, every FROM/JOIN
// target on the allowlist. The error names the rule, never the content.
func checkSelectOnly(sql string) error {
	if !strings.HasPrefix(sql, "SELECT ") {
		return fmt.Errorf("scenario db: refused non-read-only SQL: not a SELECT")
	}
	// Strip single-quoted literals; the loader's value regex keeps quotes
	// out of values, so a quote here only comes from a template.
	var bare strings.Builder
	inLit := false
	for i := 0; i < len(sql); i++ {
		c := sql[i]
		if c == '\'' {
			inLit = !inLit
			continue
		}
		if c == '\\' {
			return fmt.Errorf("scenario db: refused non-read-only SQL: backslash")
		}
		if !inLit {
			bare.WriteByte(c)
		}
	}
	if inLit {
		return fmt.Errorf("scenario db: refused non-read-only SQL: unterminated literal")
	}
	b := bare.String()
	for _, c := range []string{";", "`", `"`, "#", "--", "/*"} {
		if strings.Contains(b, c) {
			return fmt.Errorf("scenario db: refused non-read-only SQL: %q", c)
		}
	}
	upper := strings.ToUpper(b)
	for _, w := range forbiddenWords {
		for _, m := range wordRe.FindAllString(upper, -1) {
			if m == w {
				return fmt.Errorf("scenario db: refused non-read-only SQL: %s", w)
			}
		}
	}
	// Every table after FROM or JOIN must be schema.table on the allowlist.
	toks := strings.Fields(b)
	for i, t := range toks {
		if strings.ToUpper(t) == "FROM" || strings.ToUpper(t) == "JOIN" {
			if i+1 >= len(toks) {
				return fmt.Errorf("scenario db: refused non-read-only SQL: dangling %s", strings.ToUpper(t))
			}
			tab := strings.Trim(toks[i+1], "(),;")
			parts := strings.Split(tab, ".")
			if len(parts) != 2 || !allowedTable(tab) {
				offender := tab
				if len(parts) != 2 {
					offender = toks[i+1]
				}
				return fmt.Errorf("scenario db: refused non-read-only SQL: table %s", offender)
			}
		}
	}
	return nil
}

// dbRemote is the exact remote command (S5): docker exec into the DB
// container, reading the root password from the container env. The single
// quotes keep $MARIADB_ROOT_PASSWORD unexpanded on the ssh host.
const dbRemote = `docker exec -i %s sh -c 'export MYSQL_PWD="$MARIADB_ROOT_PASSWORD"; exec mysql -uroot --batch --skip-column-names --no-auto-rehash'`

// dbWrap wraps the SELECT in the read-only session (S4): a READ ONLY
// transaction (MariaDB refuses writes there with error 1792), plus a
// statement timeout. MyISAM tables ignore transactions, so S1-S3 stay the
// real guard.
func dbWrap(sql string) []byte {
	var b strings.Builder
	b.WriteString("SET SESSION max_statement_time=10;\n")
	b.WriteString("SET SESSION TRANSACTION READ ONLY;\n")
	b.WriteString("START TRANSACTION READ ONLY;\n")
	b.WriteString(sql)
	b.WriteString(";\n")
	b.WriteString("ROLLBACK;\n")
	return []byte(b.String())
}

// SSHDB runs the built SELECT over ssh on the DB host (S5-S7). Host and
// container come from the validated file; the password never leaves the
// container. Exec defaults to exec.CommandContext; tests replace it.
// Never run against a real server from a test (brief rule): unit tests use
// a fake runner (Counter), never SSHDB.
type SSHDB struct {
	Host      string
	Container string
	Exec      func(ctx context.Context, name string, args []string, stdin []byte) (stdout, stderr []byte, err error)
}

var outLine = regexp.MustCompile(`^\d+$`)

// Count renders the query, runs it (one 30 s ssh call) and returns the one
// integer. Anything else is an error that never echoes content (S6).
func (d SSHDB) Count(ctx context.Context, q DBQuery) (int64, error) {
	sql, err := BuildSQL(q)
	if err != nil {
		return 0, err
	}
	execFn := d.Exec
	if execFn == nil {
		execFn = func(ctx context.Context, name string, args []string, stdin []byte) ([]byte, []byte, error) {
			ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
			defer cancel()
			cmd := exec.CommandContext(ctx, name, args...)
			cmd.Stdin = bytes.NewReader(stdin)
			var out, errOut bytes.Buffer
			cmd.Stdout = &out
			cmd.Stderr = &errOut
			err := cmd.Run()
			if out.Len() > 64<<10 {
				out.Truncate(64 << 10)
			}
			if errOut.Len() > 64<<10 {
				errOut.Truncate(64 << 10)
			}
			return out.Bytes(), errOut.Bytes(), err
		}
	}
	remote := fmt.Sprintf(dbRemote, d.Container)
	stdout, stderr, err := execFn(ctx, "ssh", []string{"-o", "BatchMode=yes", "-o", "ConnectTimeout=10", d.Host, remote}, dbWrap(sql))
	if err != nil {
		return 0, fmt.Errorf("scenario db: ssh/mysql exited %d: %s", exitCode(err), firstErrorLine(stderr))
	}
	out := stdout
	if len(out) > 64<<10 {
		out = out[:64<<10]
	}
	trimmed := strings.TrimSuffix(string(out), "\n")
	if strings.Contains(trimmed, "\n") || !outLine.MatchString(trimmed) {
		lines := 1
		if len(trimmed) > 0 {
			lines = strings.Count(trimmed, "\n") + 1
		}
		return 0, fmt.Errorf("scenario db: unexpected output from mysql (%d lines, %d bytes)", lines, len(out))
	}
	n, _ := strconv.ParseInt(trimmed, 10, 64)
	return n, nil
}

// firstErrorLine shows only the first mysql ERROR line's first 200 chars, or
// a bare exit note that never carries secrets.
func firstErrorLine(stderr []byte) string {
	if len(stderr) > 64<<10 {
		stderr = stderr[:64<<10]
	}
	for line := range strings.Lines(strings.TrimSuffix(string(stderr), "\n")) {
		if m := errLine.FindString(line); m != "" {
			if len(line) > 200 {
				line = line[:200]
			}
			return line
		}
	}
	return "ssh/mysql failed"
}

var errLine = regexp.MustCompile(`^ERROR \d+ \([0-9A-Z]{5}\)`)

func exitCode(err error) int {
	var ee interface{ ExitCode() int }
	if errors.As(err, &ee) {
		return ee.ExitCode()
	}
	return 1
}
