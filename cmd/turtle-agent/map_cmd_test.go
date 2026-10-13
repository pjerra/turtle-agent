package main

import (
	"bytes"
	"encoding/json"
	"encoding/xml"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

const mapFixture = "../../testdata/captures/m7-gameobjects.twcap"

// runMap runs `turtle-agent map` and returns its exit code and output.
func runMap(t *testing.T, args ...string) (code int, stdout, stderr string) {
	t.Helper()
	var out, errb bytes.Buffer
	code = run(t.Context(), append([]string{"map", "--background=false"}, args...), &out, &errb)
	return code, out.String(), errb.String()
}

func xmlOK(t *testing.T, path string) string {
	t.Helper()
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	dec := xml.NewDecoder(bytes.NewReader(raw))
	for {
		if _, err := dec.Token(); err != nil {
			if err == io.EOF {
				return string(raw)
			}
			t.Fatalf("%s: %v", path, err)
		}
	}
}

func TestMapCmdNeedsAPositionSource(t *testing.T) {
	for _, args := range [][]string{{}, {"--out", "x.svg"}, {"--plan", "x.json"}, {"extra", "--capture", mapFixture}} {
		if code, _, stderr := runMap(t, args...); code != exitUsage || !strings.Contains(stderr, "usage: turtle-agent map") {
			t.Errorf("%v: exit %d, %q", args, code, stderr)
		}
	}
}

func TestMapCmdDrawsACaptureAndHTMLAndRefusesToOverwrite(t *testing.T) {
	dir := t.TempDir()
	out := filepath.Join(dir, "run.svg")
	code, stdout, stderr := runMap(t, "--capture", mapFixture, "--out", out, "--html")
	if code != exitOK || !strings.Contains(stdout, "run.svg") || !strings.Contains(stdout, "run.html") {
		t.Fatalf("exit %d\n%s\n%s", code, stdout, stderr)
	}
	svg := xmlOK(t, out)
	if !strings.Contains(svg, "Harnessc") && !strings.Contains(svg, "map 1") {
		t.Fatalf("the image does not name the run:\n%.300s", svg)
	}
	html, err := os.ReadFile(filepath.Join(dir, "run.html"))
	if err != nil || !bytes.Contains(html, []byte(`id="slider"`)) {
		t.Fatalf("%v", err)
	}
	if info, _ := os.Stat(out); info.Mode().Perm() != 0o600 {
		t.Errorf("mode %v", info.Mode())
	}
	if code, _, stderr := runMap(t, "--capture", mapFixture, "--out", out); code != exitUsage || !strings.Contains(stderr, "exists (--force replaces it)") {
		t.Errorf("an existing file was not protected: %d %q", code, stderr)
	}
	if code, _, _ := runMap(t, "--capture", mapFixture, "--out", out, "--force"); code != exitOK {
		t.Errorf("--force: %d", code)
	}
	// A capture is never overwritten, not even with --force.
	cap2 := filepath.Join(dir, "c.svg") // a capture under an image's name
	raw, _ := os.ReadFile(mapFixture)
	os.WriteFile(cap2, raw, 0o600)
	if code, _, stderr := runMap(t, "--capture", cap2, "--out", cap2, "--force"); code != exitUsage || !strings.Contains(stderr, "capture") {
		t.Errorf("the capture was a target: %d %q", code, stderr)
	}
	if got, _ := os.ReadFile(cap2); !bytes.Equal(got, raw) {
		t.Error("the capture changed")
	}
	// The extension is added when missing.
	if code, stdout, _ := runMap(t, "--capture", mapFixture, "--out", filepath.Join(dir, "noext")); code != exitOK || !strings.Contains(stdout, "noext.svg") {
		t.Errorf("%d %q", code, stdout)
	}
}

func TestMapCmdBadInputsAreUsageErrors(t *testing.T) {
	dir := t.TempDir()
	junk := filepath.Join(dir, "junk")
	os.WriteFile(junk, []byte("not what it says"), 0o600)
	for name, args := range map[string][]string{
		"missing capture":  {"--capture", filepath.Join(dir, "nope.twcap")},
		"junk capture":     {"--capture", junk},
		"bad box":          {"--capture", mapFixture, "--box", "1,2,3"},
		"flat box":         {"--capture", mapFixture, "--box", "1,2,1,9"},
		"bad floors":       {"--capture", mapFixture, "--floors", "five"},
		"missing plan":     {"--capture", mapFixture, "--plan", filepath.Join(dir, "nope.json")},
		"junk events":      {"--capture", mapFixture, "--events", junk},
		"junk report":      {"--capture", mapFixture, "--report", junk},
		"missing botwatch": {"--botwatch", filepath.Join(dir, "nope.jsonl")},
		"bad from":         {"--capture", mapFixture, "--from", "yesterday"},
		"bad to":           {"--capture", mapFixture, "--to", "2026-13-45"},
		"empty window":     {"--capture", mapFixture, "--from", "2001-01-01T00:00:00Z", "--to", "2001-01-02T00:00:00Z"},
		"unknown flag":     {"--capture", mapFixture, "--frobnicate"},
		"missing config":   {"--capture", mapFixture, "--config", filepath.Join(dir, "nope.json")},
		"empty map":        {"--capture", mapFixture, "--map", "999"},
	} {
		if code, _, stderr := runMap(t, append(args, "--out", filepath.Join(dir, "x.svg"), "--force")...); code != exitUsage {
			t.Errorf("%s: exit %d, %q", name, code, stderr)
		}
	}
	if _, err := os.Stat(filepath.Join(dir, "x.svg")); err == nil {
		t.Error("an image was written for a failed run")
	}
}

// The plan, the report, the events and botwatch samples together: routes of two bots, the plan over them, a wipe
// and a GM teleport from the report, a door from the events.
func TestMapCmdMergesPlanReportEventsAndBotwatch(t *testing.T) {
	dir := t.TempDir()
	write := func(name, data string) string {
		p := filepath.Join(dir, name)
		if err := os.WriteFile(p, []byte(data), 0o600); err != nil {
			t.Fatal(err)
		}
		return p
	}
	base := time.Date(2026, 10, 13, 12, 0, 0, 0, time.UTC)
	var bw strings.Builder
	for i := 0; i < 40; i++ {
		rec := map[string]any{"t": float64(base.Add(time.Duration(i)*5*time.Second).UnixNano()) / 1e9, "bots": []map[string]any{
			{"name": "Cod", "map": 5, "x": 10 + float64(i), "y": 10 + float64(i)/2, "z": 0},
			{"name": "Wish", "map": 5, "x": 8 + float64(i), "y": 11 + float64(i)/2, "z": 0},
			{"name": "Other", "map": 5, "x": 100, "y": 100, "z": 0}}}
		line, _ := json.Marshal(rec)
		bw.Write(line)
		bw.WriteByte('\n')
	}
	bwPath := write("bw.jsonl", bw.String())
	plan := write("plan.json", `{"version":1,"name":"hall","map":5,"entrance":{"x":2,"y":2,"z":0},"legs":[
	 {"name":"far","to":{"x":40,"y":25,"z":0},"pull_groups":[{"entries":[100],"positions":[{"x":20,"y":15,"z":0}]}],"boss":{"entry":200,"position":{"x":45,"y":28,"z":0}}}]}`)
	report := write("report.json", `{"report":{"plan":"hall","map":5,"state":"failed","started":"2026-10-13T12:00:00Z","wipes":1,"gm_shortcuts":[
	 {"kind":"teleport_to_leg_start","leg":0,"detail":"go","at":"2026-10-13T12:01:00Z"}],"deaths":[{"name":"Wish","leg":0,"at":"2026-10-13T12:02:00Z"}]}}`)
	events := write("events.json", `[{"seq":1,"time":"2026-10-13T12:02:30Z","mono_ms":1,"kind":"dungeon_failed","text":"dungeon hall failed: wipe","data":{"error":"wipe"}}]`)
	out := filepath.Join(dir, "m.svg")
	code, _, stderr := runMap(t, "--botwatch", bwPath, "--members", "Cod,Wish", "--plan", plan, "--report", report, "--events", events, "--out", out, "--html")
	if code != exitOK {
		t.Fatalf("exit %d: %s", code, stderr)
	}
	svg := xmlOK(t, out)
	for _, want := range []string{"hall (map 5)", "Cod", "Wish", "entrance", "planned boss 200", "death", "wipe: 1", "GM shortcut", "2 members"} {
		if !strings.Contains(svg, want) {
			t.Errorf("no %q in the image", want)
		}
	}
	if strings.Contains(svg, "Other") {
		t.Error("a bot outside --members was drawn")
	}
	if !strings.Contains(stderr, "bw.jsonl: 80 bot positions") {
		t.Errorf("stderr: %s", stderr)
	}
	// --box crops, --map picks the map: a box off the run still draws (empty) and a map with no positions fails.
	if code, _, _ := runMap(t, "--botwatch", bwPath, "--members", "Cod", "--box", "0,0,30,30", "--out", filepath.Join(dir, "b.svg")); code != exitOK {
		t.Errorf("--box: %d", code)
	}
	if code, _, stderr := runMap(t, "--botwatch", bwPath, "--map", "6", "--out", filepath.Join(dir, "c.svg")); code != exitUsage || !strings.Contains(stderr, "map 6") {
		t.Errorf("--map 6: %d %s", code, stderr)
	}
}
