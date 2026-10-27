package scenario

import (
	"context"
	"strings"
	"testing"
)

// TestBuildSQLTableRows pins the exact table-rows SELECT.
func TestBuildSQLTableRows(t *testing.T) {
	q := DBQuery{Table: "tw_world.lft_user_groups", Where: map[string]any{"creatorName": "Tacmdaron", "category": 2}, Op: "==", Want: 1}
	got, err := BuildSQL(q)
	if err != nil {
		t.Fatal(err)
	}
	want := "SELECT COUNT(*) FROM tw_world.lft_user_groups WHERE category = 2 AND creatorName = 'Tacmdaron'"
	if got != want {
		t.Fatalf("got  %q\nwant %q", got, want)
	}
	_ = q
}

// TestBuildSQLItem pins the exact item-count SELECT.
func TestBuildSQLItem(t *testing.T) {
	fl := int64(65)
	q := DBQuery{Item: &ItemQuery{Chars: []string{"Ragnar", "Bo"}, Entry: 9240, Flags: &fl}, Op: "==", Want: 1}
	got, err := BuildSQL(q)
	if err != nil {
		t.Fatal(err)
	}
	want := "SELECT COALESCE(SUM(ii.count), 0) FROM tw_char.item_instance ii JOIN tw_char.characters c ON c.guid = ii.owner_guid WHERE c.name IN ('Bo', 'Ragnar') AND ii.itemEntry = 9240 AND ii.flags = 65"
	if got != want {
		t.Fatalf("got  %q\nwant %q", got, want)
	}
}

// TestSelectOnlyGuard pins the read-only guard: it accepts both built forms
// and refuses everything else, without echoing content.
func TestSelectOnlyGuard(t *testing.T) {
	table, _ := BuildSQL(DBQuery{Table: "tw_world.lft_user_groups", Where: map[string]any{"category": 2}, Op: "=="})
	fl := int64(65)
	item, _ := BuildSQL(DBQuery{Item: &ItemQuery{Chars: []string{"Ragnar"}, Entry: 9240, Flags: &fl}, Op: "=="})
	for _, ok := range []string{table, item, "SELECT COUNT(*) FROM tw_world.lft_user_groups WHERE title = 'UPDATE'"} {
		if err := checkSelectOnly(ok); err != nil {
			t.Fatalf("%q: %v", ok, err)
		}
	}
	for _, bad := range []string{
		"SELECT 1; DROP TABLE x",
		"SELECT 1 INTO OUTFILE '/tmp/x'",
		"UPDATE tw_world.x SET a = 1",
		"SELECT 1 -- x",
		"SELECT 1 /* */",
		"SELECT 1 # x",
		"SELECT `a` FROM tw_world.lft_user_groups",
		"SELECT SLEEP(5)",
		"SELECT COUNT(*) FROM tw_logon.account",
		"SELECT COUNT(*) FROM characters",
		"select COUNT(*) FROM tw_world.lft_user_groups",
	} {
		if err := checkSelectOnly(bad); err == nil {
			t.Fatalf("%q: accepted", bad)
		}
	}
}

// TestSSHArgvAndStdin pins the S5 argv shape and S4 stdin wrapper: no element
// but the last holds the password or the SELECT.
func TestSSHArgvAndStdin(t *testing.T) {
	var gotName string
	var gotArgs []string
	var gotStdin []byte
	db := SSHDB{Host: "m910q", Container: "tortoise-db", Exec: func(ctx context.Context, name string, args []string, stdin []byte) (stdout, stderr []byte, err error) {
		gotName, gotArgs, gotStdin = name, args, stdin
		return []byte("3\n"), nil, nil
	}}
	n, err := db.Count(context.Background(), DBQuery{Table: "tw_world.lft_user_groups", Op: "=="})
	if err != nil || n != 3 {
		t.Fatalf("%d %v", n, err)
	}
	if gotName != "ssh" {
		t.Fatalf("name %q", gotName)
	}
	want := []string{"ssh", "-o", "BatchMode=yes", "-o", "ConnectTimeout=10", "m910q"}
	if len(gotArgs) < len(want) {
		t.Fatalf("argv %q", gotArgs)
	}
	for i, w := range want {
		if gotArgs[i] != w {
			t.Fatalf("argv %q", gotArgs)
		}
	}
	last := gotArgs[len(gotArgs)-1]
	if !strings.HasPrefix(last, "docker exec -i tortoise-db sh -c '") || !strings.HasSuffix(last, "'") {
		t.Fatalf("remote %q", last)
	}
	inner := strings.TrimSuffix(strings.TrimPrefix(last, "docker exec -i tortoise-db sh -c '"), "'")
	if !strings.Contains(inner, "MYSQL_PWD") || !strings.Contains(inner, "$MARIADB_ROOT_PASSWORD") {
		t.Fatalf("no env password: %q", inner)
	}
	for _, a := range gotArgs[:len(gotArgs)-1] {
		if strings.Contains(a, "SELECT") || strings.Contains(a, "MARIADB_ROOT_PASSWORD") {
			t.Fatalf("secret outside remote: %q", a)
		}
	}
	body := string(gotStdin)
	for _, want := range []string{
		"SET SESSION max_statement_time=10;",
		"SET SESSION TRANSACTION READ ONLY;",
		"START TRANSACTION READ ONLY;",
		"SELECT COUNT(*) FROM tw_world.lft_user_groups;",
		"ROLLBACK;",
	} {
		if !strings.Contains(body, want) {
			t.Fatalf("stdin %q lacks %q", body, want)
		}
	}
}

// TestDBOutputIsOnlyANumber pins the S6 output rule: `5` parses, anything
// else errors without echoing content; only an ERROR line from stderr shows.
func TestDBOutputIsOnlyANumber(t *testing.T) {
	mk := func(out, errOut string, code int) SSHDB {
		return SSHDB{Host: "m910q", Container: "tortoise-db", Exec: func(ctx context.Context, name string, args []string, stdin []byte) ([]byte, []byte, error) {
			if code != 0 {
				return []byte(out), []byte(errOut), &exitError{code: code}
			}
			return []byte(out), []byte(errOut), nil
		}}
	}
	n, err := mk("5\n", "", 0).Count(context.Background(), DBQuery{Table: "tw_world.lft_user_groups", Op: "=="})
	if err != nil || n != 5 {
		t.Fatalf("%d %v", n, err)
	}
	for _, tc := range []struct {
		name string
		out  string
		err  string
		code int
	}{
		{"two columns", "secret\tvalue\n", "", 0},
		{"words", "nope\n", "", 0},
		{"two lines", "5\n6\n", "", 0},
		{"empty", "", "", 0},
	} {
		_, err := mk(tc.out, tc.err, tc.code).Count(context.Background(), DBQuery{Table: "tw_world.lft_user_groups", Op: "=="})
		if err == nil {
			t.Fatalf("%s: accepted", tc.name)
		}
		if strings.Contains(err.Error(), "secret") || strings.Contains(err.Error(), "value") {
			t.Fatalf("%s: echoed content: %v", tc.name, err)
		}
	}
	// An ssh failure carrying a password warning must not surface the secret.
	_, err = mk("", "Warning: pw=hunter2\n", 1).Count(context.Background(), DBQuery{Table: "tw_world.lft_user_groups", Op: "=="})
	if err == nil || strings.Contains(err.Error(), "hunter2") {
		t.Fatalf("leaked: %v", err)
	}
	// A mysql ERROR line shows, cut to its first line.
	_, err = mk("", "ERROR 1146 (42S02) at line 4: Table 'tw_char.x' doesn't exist\nmore\n", 1).Count(context.Background(), DBQuery{Table: "tw_world.lft_user_groups", Op: "=="})
	if err == nil || !strings.Contains(err.Error(), "ERROR 1146 (42S02)") || strings.Contains(err.Error(), "more") {
		t.Fatalf("stderr: %v", err)
	}
}

// exitError fakes an exec exit status for the stderr tests.
type exitError struct {
	code int
}

func (e *exitError) Error() string { return "exit status 1" }
