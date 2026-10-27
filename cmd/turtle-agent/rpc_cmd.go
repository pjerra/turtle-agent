// Command rpc sends one JSON-RPC call to the agent of a slot dir, so
// live-test workers never read its token file (owner decision 2026-10-28).
//
// The slot.json paths are assertions, not inputs: teardownCheckManifest is
// reused verbatim, so the dir must be a canonical slot dir, the port is the
// validated one, and the token file is the one derived inside the dir. The
// request goes to 127.0.0.1:<port> only; nothing on the command line can
// change the host.
package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/pjerra/turtle-agent/internal/privfile"
	"github.com/pjerra/turtle-agent/internal/rpc"
)

// exitRPCError is the exit code when the agent answers with a JSON-RPC
// error. 16 is the next free code above the table: 11/12/13 are
// trace-score, route-oracle and stage-check, 14 is teardown/scenario checks,
// 15 is lease/slot refusals (which doubles as rpc's cannot-reach code, see
// below).
const exitRPCError = 16

const rpcUsage = "usage: turtle-agent rpc [--timeout DUR] [--raw] <dir> <method> [<params-json>|-]"

// rpcStdin is the params source when params-json is "-"; tests replace it.
var rpcStdin io.Reader = os.Stdin

// rpcTimeout parses --timeout values: Go durations ("5s", "1m30s").
func rpcCmd(args []string, stdout, stderr io.Writer) int {
	if !slotsSupported() {
		return slotsUnsupported("rpc", stderr)
	}
	o, code := parseRPCArgs(args, stderr)
	if code != exitOK {
		return code
	}
	// Slot manifest loading follows teardownCmd: read slot.json, assert
	// the paths, use only the derived token file, never the strings.
	absDir, err := filepath.Abs(o.dir)
	if err != nil {
		fmt.Fprintf(stderr, "rpc: %v\n", err)
		return exitUsage
	}
	dir := absDir
	raw, err := privfile.Read(filepath.Join(dir, "slot.json"), 64*1024)
	if err != nil {
		fmt.Fprintf(stderr, "rpc: %s/slot.json missing or unreadable: rpc works on a dir made by slot setup\n", dir)
		return exitUsage
	}
	var manifest teardownSlotManifest
	if err := json.Unmarshal(raw, &manifest); err != nil ||
		manifest.Server == "" || manifest.Slot == "" || manifest.Holder == "" || manifest.Port == 0 {
		fmt.Fprintf(stderr, "rpc: %s/slot.json missing or unreadable: rpc works on a dir made by slot setup\n", dir)
		return exitUsage
	}
	if err := teardownCheckManifest(dir, &manifest); err != nil {
		fmt.Fprintf(stderr, "%v\n", err)
		return exitUsage
	}
	token, err := rpc.ReadTokenFile(filepath.Join(dir, fmt.Sprintf("rpc-%d.token", manifest.Port)))
	if err != nil {
		fmt.Fprintf(stderr, "rpc: token file: %v\n", rpcScrub(err.Error(), ""))
		return exitSlotRefused
	}
	params, err := rpcReadParams(o.params, o.paramsFromStdin)
	if err != nil {
		fmt.Fprintf(stderr, "rpc: %v\n", err)
		return exitUsage
	}
	result, rpcErr, err := rpcPostCall(manifest.Port, token, o.method, params, o.timeout)
	if err != nil {
		// Transport failure (agent not running, connection refused):
		// the lease/session wording matches teardown's own.
		fmt.Fprintf(stderr, "rpc: cannot reach the agent at 127.0.0.1:%d: %s\n", manifest.Port, rpcScrub(err.Error(), token))
		return exitSlotRefused
	}
	if rpcErr != nil {
		fmt.Fprintf(stderr, "error: %d %s\n", rpcErr.Code, rpcScrub(rpcErr.Message, token))
		return exitRPCError
	}
	var out []byte
	if o.raw {
		out, err = json.Marshal(result)
	} else {
		out, err = json.MarshalIndent(result, "", "  ")
	}
	if err != nil {
		fmt.Fprintf(stderr, "rpc: %v\n", rpcScrub(err.Error(), token))
		return exitUsage
	}
	out = []byte(rpcScrub(string(out), token))
	fmt.Fprintln(stdout, string(out))
	return exitOK
}

// rpcReadParams decodes the params JSON: an object or an array, "-" from
// stdin, empty when omitted.
func rpcReadParams(params string, fromStdin bool) (json.RawMessage, error) {
	var raw []byte
	if fromStdin {
		var err error
		raw, err = io.ReadAll(io.LimitReader(rpcStdin, 1<<20))
		if err != nil {
			return nil, err
		}
		if len(bytes.TrimSpace(raw)) == 0 {
			return nil, nil
		}
	} else if params == "" {
		return nil, nil
	} else {
		raw = []byte(params)
	}
	trimmed := bytes.TrimSpace(raw)
	if len(trimmed) == 0 {
		return nil, nil
	}
	if trimmed[0] != '{' && trimmed[0] != '[' {
		return nil, fmt.Errorf("params must be a JSON object or array")
	}
	var v any
	if err := json.Unmarshal(trimmed, &v); err != nil {
		return nil, fmt.Errorf("bad params JSON: %v", err)
	}
	return json.RawMessage(trimmed), nil
}

// rpcError is one JSON-RPC error from the agent.
type rpcError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

// rpcCall posts one JSON-RPC 2.0 request to 127.0.0.1:port and returns the
// result (nil for notifications/empty), or the error the agent answered.
func rpcPostCall(port int, token, method string, params json.RawMessage, timeout time.Duration) (json.RawMessage, *rpcError, error) {
	reqBody := map[string]any{
		"jsonrpc": "2.0",
		"id":      1,
		"method":  method,
	}
	if params != nil {
		reqBody["params"] = params
	}
	body, err := json.Marshal(reqBody)
	if err != nil {
		return nil, nil, err
	}
	req, err := http.NewRequest("POST", fmt.Sprintf("http://127.0.0.1:%d/rpc", port), bytes.NewReader(body))
	if err != nil {
		return nil, nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+token)
	client := &http.Client{Timeout: timeout}
	resp, err := client.Do(req)
	if err != nil {
		return nil, nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusUnauthorized {
		return nil, nil, fmt.Errorf("unauthorized: token file no longer matches the agent (restarted?)")
	}
	if resp.StatusCode != http.StatusOK {
		return nil, nil, fmt.Errorf("http %d", resp.StatusCode)
	}
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return nil, nil, err
	}
	var env struct {
		Result json.RawMessage `json:"result"`
		Error  *rpcError       `json:"error"`
	}
	if err := json.Unmarshal(raw, &env); err != nil {
		return nil, nil, fmt.Errorf("bad reply: %v", err)
	}
	if env.Error != nil {
		return nil, env.Error, nil
	}
	return env.Result, nil, nil
}

// rpcScrub replaces the token with <token> so it never reaches stdout or
// stderr: not in errors, not with --raw, not in --help (the help text holds
// no token by construction). An empty token scrubs nothing.
func rpcScrub(s, token string) string {
	if token == "" {
		return s
	}
	return strings.ReplaceAll(s, token, "<token>")
}
