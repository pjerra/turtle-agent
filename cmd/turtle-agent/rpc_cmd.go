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
	"errors"
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
	// The file must hold a token the agent generates (rpc.NewToken: 32
	// random bytes as 43 base64url characters): anything else (quotes, a
	// backslash, markup, an inner newline, the wrong length) is refused
	// before use, without printing it. The rpcScrub calls below stay as a
	// second line of defence.
	if !rpcValidToken(token) {
		fmt.Fprintln(stderr, "rpc: the token file is not an agent token")
		return exitSlotRefused
	}
	params, err := rpcReadParams(o.params, o.paramsFromStdin)
	if err != nil {
		fmt.Fprintf(stderr, "rpc: %v\n", err)
		return exitUsage
	}
	result, rpcErr, err := rpcPostCall(manifest.Port, token, o.method, params, o.timeout)
	if err != nil {
		// A bad envelope keeps its own "bad reply" line; only transport
		// failures (agent not running, connection refused) use the
		// lease/session wording, matching teardown's own.
		var bad *rpcBadReplyError
		if errors.As(err, &bad) {
			fmt.Fprintf(stderr, "rpc: %s\n", rpcScrub(err.Error(), token))
			return exitSlotRefused
		}
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
// Anything that is not a valid 2.0 envelope for our id comes back as an
// *rpcBadReplyError, so the caller can tell a bad reply from a dead agent.
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
		return nil, nil, &rpcBadReplyError{reason: "unauthorized: token file no longer matches the agent (restarted?)"}
	}
	if resp.StatusCode != http.StatusOK {
		return nil, nil, &rpcBadReplyError{reason: fmt.Sprintf("http %d", resp.StatusCode)}
	}
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return nil, nil, err
	}
	return rpcCheckReply(raw)
}

// rpcBadReplyError is a reply that fails the JSON-RPC envelope check: a
// non-200 status, or a body that is not an object with "jsonrpc":"2.0",
// our id and exactly one of result/error. rpcCmd reports it as
// "rpc: bad reply from the agent: <reason>" (exit 15); anything else from
// rpcPostCall is a transport failure ("cannot reach").
type rpcBadReplyError struct {
	reason string
}

func (e *rpcBadReplyError) Error() string { return "bad reply from the agent: " + e.reason }

// rpcTokenLen is the length of rpc.NewToken: 32 random bytes in base64url
// without padding.
const rpcTokenLen = 43

// rpcValidToken reports whether tok has exactly the shape the agent
// generates, so a stray file is refused before it is ever sent.
func rpcValidToken(tok string) bool {
	if len(tok) != rpcTokenLen {
		return false
	}
	for i := range len(tok) {
		c := tok[i]
		if c >= '0' && c <= '9' || c >= 'A' && c <= 'Z' || c >= 'a' && c <= 'z' || c == '-' || c == '_' {
			continue
		}
		return false
	}
	return true
}

// rpcCheckReply validates the full JSON-RPC envelope of a 200 reply to our
// request (id 1): an object with "jsonrpc":"2.0", "id" 1, and exactly one
// of a present "result" or "error"; an "error" must be an object with a
// numeric code and a string message.
func rpcCheckReply(raw []byte) (json.RawMessage, *rpcError, error) {
	bad := func(format string, args ...any) (json.RawMessage, *rpcError, error) {
		return nil, nil, &rpcBadReplyError{reason: fmt.Sprintf(format, args...)}
	}
	if trimmed := bytes.TrimSpace(raw); len(trimmed) == 0 || trimmed[0] != '{' {
		return bad("not a JSON object")
	}
	var obj map[string]json.RawMessage
	if err := json.Unmarshal(raw, &obj); err != nil {
		return bad("invalid JSON: %v", err)
	}
	var version string
	if v, ok := obj["jsonrpc"]; !ok || json.Unmarshal(v, &version) != nil || version != "2.0" {
		return bad(`want "jsonrpc":"2.0"`)
	}
	var id any
	if v, ok := obj["id"]; !ok || json.Unmarshal(v, &id) != nil || id != float64(1) {
		return bad(`want "id":1`)
	}
	res, hasRes := obj["result"]
	errRaw, hasErr := obj["error"]
	if hasRes == hasErr {
		return bad(`want exactly one of "result" or "error"`)
	}
	if !hasErr {
		return res, nil, nil
	}
	var ebody struct {
		Code    any `json:"code"`
		Message any `json:"message"`
	}
	const errShape = `"error" must be an object with a numeric code and a string message`
	if json.Unmarshal(errRaw, &ebody) != nil {
		return bad(errShape)
	}
	code, ok := ebody.Code.(float64)
	if !ok {
		return bad(errShape)
	}
	msg, ok := ebody.Message.(string)
	if !ok {
		return bad(errShape)
	}
	return nil, &rpcError{Code: int(code), Message: msg}, nil
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
