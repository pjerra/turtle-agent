package scenario

import (
	"strings"
	"testing"
)

// TestCheckDBQueryExpandsCharVar pins that an item check may name its
// character with a var (e.g. ${A.character} from the status probe): the
// template expands first and the value is then validated. A value with bad
// chars is still rejected.
func TestCheckDBQueryExpandsCharVar(t *testing.T) {
	f, err := Parse([]byte(`{"scenario":"t","sessions":{"A":{"rpc":"127.0.0.1:8774"}},
		"db":{"host":"m910q","container":"tortoise-db"},
		"steps":[
			{"do":"check_db","item":{"char":"${A.character}","entry":9240},"count":0},
			{"do":"check_db","item":{"chars":["${A.character}"],"entry":9240},"count":0}
		]}`), nil)
	if err != nil {
		t.Fatal(err)
	}
	rr := &run{vars: map[string]string{"A.character": "Tacmdaron"}}
	q, err := rr.checkDBQuery(f.Steps[0])
	if err != nil {
		t.Fatal(err)
	}
	if q.Item == nil || q.Item.Char != "Tacmdaron" {
		t.Fatalf("%+v", q.Item)
	}
	q2, err := rr.checkDBQuery(f.Steps[1])
	if err != nil {
		t.Fatal(err)
	}
	if q2.Item == nil || len(q2.Item.Chars) != 1 || q2.Item.Chars[0] != "Tacmdaron" {
		t.Fatalf("%+v", q2.Item)
	}
	bad := &run{vars: map[string]string{"A.character": "Bob'--"}}
	if _, err := bad.checkDBQuery(f.Steps[0]); err == nil || !strings.Contains(err.Error(), "bad char name") {
		t.Fatalf("char: got %v", err)
	}
	if _, err := bad.checkDBQuery(f.Steps[1]); err == nil || !strings.Contains(err.Error(), "bad char name") {
		t.Fatalf("chars: got %v", err)
	}
}
