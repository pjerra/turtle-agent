package dashboard

import (
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
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
	for _, f := range []string{"app.js", "view.js", "text.js", "layers.js", "paths.js", "pathdraw.js", "unitinfo.js"} {
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

// The label plate of a marker is a <rect> too: a rule that styles every rect under a marker paints it as a big box (a white box
// beside every door). Only the icon (rect.ic) may be styled.
func TestMarkerCSSDoesNotStyleLabelPlates(t *testing.T) {
	css, err := os.ReadFile(filepath.Join("static", "app.css"))
	if err != nil {
		t.Fatal(err)
	}
	re := regexp.MustCompile(`svg\s+\.(obj|boss|entrance|pin|legname|mk)[^{,]*\srect\s*[{,]`)
	if m := re.Find(css); m != nil {
		t.Errorf("css styles every rect under a marker: %q", m)
	}
	if !strings.Contains(string(css), "rect.ic") {
		t.Error("the door icon rect must be styled by its own class")
	}
}

// TestMapLayers: the layer defaults, the one global saved choice, availability and the geometry of the newer layers.
func TestMapLayers(t *testing.T) { runCheck(t, "layers_check.js", "layers.js") }

// TestUnitInfo: what a character is doing, the hover box, status icons, threat colours, the event feed and the run progress.
// TestPathMath: trails, planned vs walked, pull-group states, objects, aggro rings, patrols, combat lines, the ghost and the heat bins.
func TestPathMath(t *testing.T) { runCheck(t, "paths_check.js", "paths.js") }

// TestPathDraw: the path layers on a fake SVG.
func TestPathDraw(t *testing.T) { runCheck(t, "pathdraw_check.js", "pathdraw.js") }

func TestUnitInfo(t *testing.T) { runCheck(t, "unitinfo_check.js", "unitinfo.js") }

// The page loads the script that holds the new marks, and every layer has a hide rule or a drawing condition in the stylesheet.
func TestPageLoadsUnitInfoAndStylesNewLayers(t *testing.T) {
	html, err := os.ReadFile(filepath.Join("static", "index.html"))
	if err != nil || !strings.Contains(string(html), `src="unitinfo.js"`) {
		t.Fatalf("index.html must load unitinfo.js: %v", err)
	}
	css, err := os.ReadFile(filepath.Join("static", "app.css"))
	if err != nil {
		t.Fatal(err)
	}
	for _, k := range []string{"bossmark", "hpbars", "statusicons", "threatcolors", "events", "hostilenames"} {
		if !strings.Contains(string(css), "hide-"+k) {
			t.Errorf("app.css has no rule for layer %s", k)
		}
	}
}
