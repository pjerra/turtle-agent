package main

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// fakeScenarioAgent is an httptest stand-in for a running agent: status, events and
// addon_send with canned replies.
type fakeScenarioAgent struct {
	t      *testing.T
	tok    string
	events []map[string]any
	seq    uint64
}

func (f *fakeScenarioAgent) handler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer "+f.tok {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		var req struct {
			Method string          `json:"method"`
			Params json.RawMessage `json:"params"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		enc := func(v any) { _ = json.NewEncoder(w).Encode(map[string]any{"jsonrpc": "2.0", "id": 1, "result": v}) }
		switch req.Method {
		case "status":
			enc(map[string]any{"character": "Tacmdaron", "last_seq": f.seq})
		case "events":
			var p struct {
				Since uint64 `json:"since"`
			}
			_ = json.Unmarshal(req.Params, &p)
			var out []map[string]any
			next := p.Since
			for _, e := range f.events {
				if e["seq"].(uint64) > p.Since {
					out = append(out, e)
					next = e["seq"].(uint64)
				}
			}
			if out == nil {
				out = []map[string]any{}
			}
			enc(map[string]any{"events": out, "next": next})
		case "addon_send":
			enc(map[string]any{})
		default:
			enc(map[string]any{})
		}
	})
}

func writeFakeFile(t *testing.T, dir, name, text string) string {
	t.Helper()
	p := filepath.Join(dir, name)
	if err := os.WriteFile(p, []byte(text), 0o644); err != nil {
		t.Fatal(err)
	}
	return p
}

// TestScenarioCmdCheck pins check: a valid file exits 0 with one line per
// step, an invalid one exits 2.
func TestScenarioCmdCheck(t *testing.T) {
	dir := t.TempDir()
	ok := writeFakeFile(t, dir, "ok.json", `{"scenario":"ok","sessions":{"A":{"rpc":"127.0.0.1:8774"}},
		"steps":[{"do":"mark"},{"do":"sleep","seconds":1}]}`)
	var out, errOut bytes.Buffer
	if code := scenarioCmd(t.Context(), []string{"check", ok}, &out, &errOut); code != 0 {
		t.Fatalf("exit %d (%s)", code, errOut.String())
	}
	if lines := strings.Count(strings.TrimSpace(out.String()), "\n") + 1; lines != 2 {
		t.Fatalf("want 2 lines, got %q", out.String())
	}
	bad := writeFakeFile(t, dir, "bad.json", `{"scenario":"ok","sessions":{"A":{"rpc":"127.0.0.1:8774"}},
		"steps":[{"do":"gm","session":"A","text":"x","match":"y"}]}`)
	var out2, err2 bytes.Buffer
	if code := scenarioCmd(t.Context(), []string{"check", bad}, &out2, &err2); code != exitUsage {
		t.Fatalf("exit %d", code)
	}
}

// TestScenarioCmdRunAgainstAFakeAgent pins run: against the fake agent it
// exits 0 and writes the four files, with no seq in norm.txt; an unmatchable
// expect exits 14; a non-empty out dir without --force exits 2.
func TestScenarioCmdRunAgainstAFakeAgent(t *testing.T) {
	dir := t.TempDir()
	tok := "fake-token-" + strings.Repeat("x", 32)
	tokPath := filepath.Join(dir, "rpc-8774.token")
	if err := os.WriteFile(tokPath, []byte(tok+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	fa := &fakeScenarioAgent{t: t, tok: tok}
	srv := httptest.NewServer(fa.handler())
	defer srv.Close()
	addr := strings.TrimPrefix(srv.URL, "http://")
	file := writeFakeFile(t, dir, "run.json", `{"scenario":"cmdrun","sessions":{"A":{"rpc":"`+addr+`","token_file":"`+tokPath+`"}},
		"addon_prefix":"TW_LFG",
		"steps":[{"do":"addon_send","session":"A","channel":"guild","text":"C2S_GET_GROUPS_LIST"}]}`)
	outDir := filepath.Join(dir, "out")
	var out, errOut bytes.Buffer
	if code := scenarioCmd(t.Context(), []string{"run", "--out", outDir, file}, &out, &errOut); code != 0 {
		t.Fatalf("exit %d (%s)", code, errOut.String())
	}
	for _, name := range []string{"raw.txt", "norm.txt", "transcript.jsonl", "result.json"} {
		if _, err := os.Stat(filepath.Join(outDir, name)); err != nil {
			t.Fatalf("%s: %v", name, err)
		}
	}
	norm, _ := os.ReadFile(filepath.Join(outDir, "norm.txt"))
	if strings.Contains(string(norm), "\t1\t") {
		t.Fatalf("norm has seq: %q", norm)
	}
	var res struct {
		Exit int `json:"exit"`
	}
	raw, _ := os.ReadFile(filepath.Join(outDir, "result.json"))
	if err := json.Unmarshal(raw, &res); err != nil || res.Exit != 0 {
		t.Fatalf("%s %v", raw, err)
	}

	// An expect that cannot match exits 14.
	file2 := writeFakeFile(t, dir, "run2.json", `{"scenario":"cmdrun2","sessions":{"A":{"rpc":"`+addr+`","token_file":"`+tokPath+`"}},
		"steps":[{"do":"expect","session":"A","match":"^NOPE$","timeout_s":1}]}`)
	var out2, err2 bytes.Buffer
	if code := scenarioCmd(t.Context(), []string{"run", "--out", filepath.Join(dir, "out2"), file2}, &out2, &err2); code != 14 {
		t.Fatalf("exit %d (%s)", code, err2.String())
	}

	// A non-empty out dir without --force exits 2.
	var out3, err3 bytes.Buffer
	if code := scenarioCmd(t.Context(), []string{"run", "--out", outDir, file}, &out3, &err3); code != exitUsage {
		t.Fatalf("exit %d", code)
	}
}

// TestScenarioCmdNormRerenders pins norm: after editing the rules, scenario
// norm rewrites norm.txt while raw.txt stays untouched.
func TestScenarioCmdNormRerenders(t *testing.T) {
	dir := t.TempDir()
	tokPath := filepath.Join(dir, "rpc-8774.token")
	if err := os.WriteFile(tokPath, []byte("tok-"+strings.Repeat("y", 32)+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	fa := &fakeScenarioAgent{t: t, tok: "tok-" + strings.Repeat("y", 32)}
	srv := httptest.NewServer(fa.handler())
	defer srv.Close()
	addr := strings.TrimPrefix(srv.URL, "http://")
	file := writeFakeFile(t, dir, "n.json", `{"scenario":"nr","sessions":{"A":{"rpc":"`+addr+`","token_file":"`+tokPath+`"}},
		"steps":[{"do":"gm","session":"A","text":".go xyz 1 2 3"}]}`)
	outDir := filepath.Join(dir, "out")
	var out, errOut bytes.Buffer
	if code := scenarioCmd(t.Context(), []string{"run", "--out", outDir, file}, &out, &errOut); code != 0 {
		t.Fatalf("exit %d (%s)", code, errOut.String())
	}
	before, _ := os.ReadFile(filepath.Join(outDir, "raw.txt"))
	beforeNorm, _ := os.ReadFile(filepath.Join(outDir, "norm.txt"))
	// Rewrite the file with a normalise rule hiding the coordinates.
	file2 := writeFakeFile(t, dir, "n.json", `{"scenario":"nr","sessions":{"A":{"rpc":"`+addr+`","token_file":"`+tokPath+`"}},
		"normalise":[{"match":"xyz 1 2 3","replace":"XYZ"}],
		"steps":[{"do":"gm","session":"A","text":".go xyz 1 2 3"}]}`)
	var out2, err2 bytes.Buffer
	if code := scenarioCmd(t.Context(), []string{"norm", file2, outDir}, &out2, &err2); code != 0 {
		t.Fatalf("exit %d (%s)", code, err2.String())
	}
	after, _ := os.ReadFile(filepath.Join(outDir, "raw.txt"))
	afterNorm, _ := os.ReadFile(filepath.Join(outDir, "norm.txt"))
	if string(after) != string(before) {
		t.Fatal("raw.txt changed")
	}
	if string(afterNorm) == string(beforeNorm) || !strings.Contains(string(afterNorm), "XYZ") {
		t.Fatalf("norm.txt not rerendered: %q", afterNorm)
	}
}

// TestScenarioCmdVarAndSessionFlags pins --var (unknown refused) on check
// and the --session override parse on run (a bad value is a usage error).
func TestScenarioCmdVarAndSessionFlags(t *testing.T) {
	dir := t.TempDir()
	file := writeFakeFile(t, dir, "v.json", `{"scenario":"v","sessions":{"A":{"rpc":"127.0.0.1:8774"}},
		"vars":{"x":"1"},"steps":[{"do":"sleep","seconds":1}]}`)
	var out, errOut bytes.Buffer
	if code := scenarioCmd(t.Context(), []string{"check", "--var", "unknown=1", file}, &out, &errOut); code != exitUsage {
		t.Fatalf("exit %d", code)
	}
	var out2, err2 bytes.Buffer
	if code := scenarioCmd(t.Context(), []string{"check", "--var", "x=2", file}, &out2, &err2); code != 0 {
		t.Fatalf("exit %d (%s)", code, err2.String())
	}
	var out3, err3 bytes.Buffer
	if code := scenarioCmd(t.Context(), []string{"run", "--session", "ZZZ=127.0.0.1:9@/x", "--out", filepath.Join(dir, "o"), file}, &out3, &err3); code != exitUsage {
		t.Fatalf("exit %d", code)
	}
	// A --session override to a non-loopback address is refused before any dial.
	var out4, err4 bytes.Buffer
	if code := scenarioCmd(t.Context(), []string{"run", "--session", "A=10.0.0.5:8774", "--out", filepath.Join(dir, "o2"), file}, &out4, &err4); code != exitUsage {
		t.Fatalf("exit %d", code)
	}
	if !strings.Contains(err4.String(), "is not a loopback address") {
		t.Fatalf("stderr %q", err4.String())
	}
}

// TestScenarioCmdRunArtifactFailure pins that an unwritable out dir aborts
// with exit 15 and an explicit message.
func TestScenarioCmdRunArtifactFailure(t *testing.T) {
	dir := t.TempDir()
	tokPath := filepath.Join(dir, "rpc-8774.token")
	if err := os.WriteFile(tokPath, []byte("tok-"+strings.Repeat("z", 32)+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	fa := &fakeScenarioAgent{t: t, tok: "tok-" + strings.Repeat("z", 32)}
	srv := httptest.NewServer(fa.handler())
	defer srv.Close()
	addr := strings.TrimPrefix(srv.URL, "http://")
	file := writeFakeFile(t, dir, "run.json", `{"scenario":"cmdrun","sessions":{"A":{"rpc":"`+addr+`","token_file":"`+tokPath+`"}},
		"steps":[{"do":"addon_send","session":"A","channel":"guild","text":"C2S_GET_GROUPS_LIST"}]}`)
	outDir := filepath.Join(dir, "out")
	if err := os.MkdirAll(outDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(outDir, "raw.txt"), []byte("x"), 0o444); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(outDir, 0o555); err != nil {
		t.Fatal(err)
	}
	defer os.Chmod(outDir, 0o755)
	var out, errOut bytes.Buffer
	if code := scenarioCmd(t.Context(), []string{"run", "--force", "--out", outDir, file}, &out, &errOut); code != exitScenarioAbort {
		t.Fatalf("exit %d (%s)", code, errOut.String())
	}
	if !strings.Contains(errOut.String(), "cannot write") {
		t.Fatalf("stderr %q", errOut.String())
	}
}
