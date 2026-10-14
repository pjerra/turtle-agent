package dashboard

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/pjerra/turtle-agent/internal/dungeon"
)

// node runs the page's own scripts: the view math is plain JavaScript, so the test loads the same file the page does. The
// test is skipped on a machine without node.
func nodeOrSkip(t *testing.T) string {
	t.Helper()
	p, err := exec.LookPath("node")
	if err != nil {
		t.Skip("node is not installed")
	}
	return p
}

func runCheck(t *testing.T, check, script string) {
	t.Helper()
	node := nodeOrSkip(t)
	path, err := filepath.Abs(filepath.Join("static", script))
	if err != nil {
		t.Fatal(err)
	}
	out, err := exec.Command(node, filepath.Join("testdata", check), path).CombinedOutput()
	if err != nil || !strings.Contains(string(out), "ok") {
		t.Fatalf("%s: %v\n%s", check, err, out)
	}
}

// TestViewMath: bounds union, fit and aspect ratio, the transform, zoom, pan, pinch and follow.
func TestViewMath(t *testing.T) { runCheck(t, "view_check.js", "view.js") }

// TestPlainText: the player-language formatting of states, counters, orders and party rows.
func TestPlainText(t *testing.T) { runCheck(t, "text_check.js", "text.js") }

func TestPageScriptsParse(t *testing.T) {
	node := nodeOrSkip(t)
	for _, f := range []string{"app.js", "view.js", "text.js"} {
		src, err := os.ReadFile(filepath.Join("static", f))
		if err != nil {
			t.Fatal(err)
		}
		cmd := exec.Command(node, "--check", "-")
		cmd.Stdin = strings.NewReader(string(src))
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Errorf("%s: %v\n%s", f, err, out)
		}
	}
}

func TestEntryNames(t *testing.T) {
	p, err := dungeon.Parse("n.json", []byte(`{"version":1,"name":"n","map":209,"entrance":{"x":0,"y":0,"z":0},
 "notes":["Revelosh (6910) is the first boss."],
 "legs":[{"name":"a","to":{"x":10,"y":10,"z":0},
  "notes":["The Stonevault Cave Lurkers (4850) spawn stealthed.","Stonevault passage and the ramp room of the Stonevault Maulers (7320) wait.","It casts Amplify Flames (9482) and the Unused Thing (5555) is not in the plan."],
  "pull_groups":[{"entries":[4850],"positions":[{"x":5,"y":5,"z":0}]},{"entries":[7320],"positions":[{"x":6,"y":6,"z":0}]}],
  "boss":{"entry":6910,"position":{"x":10,"y":10,"z":0}}}]}`))
	if err != nil {
		t.Fatal(err)
	}
	got := EntryNames(p)
	want := map[string]string{"4850": "Stonevault Cave Lurkers", "7320": "Stonevault Maulers", "6910": "Revelosh"}
	if len(got) != len(want) {
		t.Errorf("names %v, want %v", got, want)
	}
	for k, v := range want {
		if got[k] != v {
			t.Errorf("entry %s: %q, want %q", k, got[k], v)
		}
	}
}
