// slotClient calls one slot agent's JSON-RPC like `rpc` does, so commands
// that orchestrate a live slot (raid-walk, patrol-record) never read its
// token file themselves (spec S0).
//
// The dir checks are rpcCmd's, moved here unchanged: slot.json is read with
// privfile.Read and asserted by teardownCheckManifest, the token comes from
// the derived rpc-<port>.token file and must have the agent's token shape.
// The token is never printed: every error text passes rpcScrub.
package main

import (
	"context"
	"encoding/json"
	"fmt"
	"path/filepath"
	"time"

	"github.com/pjerra/turtle-agent/internal/privfile"
	"github.com/pjerra/turtle-agent/internal/rpc"
)

// slotClient calls one slot agent's JSON-RPC like `rpc` does: slot.json
// checked (teardownCheckManifest), token read from rpc-<port>.token and
// validated (rpcValidToken), never printed; every error text passes
// rpcScrub.
type slotClient struct {
	dir      string
	manifest teardownSlotManifest
	token    string // unexported, never logged
	post     func(port int, token, method string, params json.RawMessage, timeout time.Duration) (json.RawMessage, *rpcError, error)
}

// slotOpenError is a refused slot dir: rpc prints msg verbatim and exits
// with code, which is what rpcCmd did inline before the extraction.
type slotOpenError struct {
	code int
	msg  string
}

func (e *slotOpenError) Error() string { return e.msg }

// slotRPCError is one JSON-RPC error answer with its message scrubbed.
type slotRPCError struct {
	Code    int
	Message string
}

func (e *slotRPCError) Error() string { return fmt.Sprintf("rpc error %d: %s", e.Code, e.Message) }

// scrubError keeps the cause's type for errors.As but scrubs the token out
// of its text, so a caller can still tell a bad reply from a dead agent
// without the token reaching stdout or stderr.
type scrubError struct {
	err error
	msg string
}

func (e *scrubError) Error() string { return e.msg }
func (e *scrubError) Unwrap() error { return e.err }

// openSlotClient runs the rpcCmd checks on dir and returns a client that
// posts through rpcPostCall. A refusal is a *slotOpenError carrying the
// exact line rpc prints and its exit code.
func openSlotClient(dir string) (*slotClient, error) {
	absDir, err := filepath.Abs(dir)
	if err != nil {
		return nil, &slotOpenError{code: exitUsage, msg: fmt.Sprintf("rpc: %v", err)}
	}
	dir = absDir
	unreadable := fmt.Sprintf("rpc: %s/slot.json missing or unreadable: rpc works on a dir made by slot setup", dir)
	raw, err := privfile.Read(filepath.Join(dir, "slot.json"), 64*1024)
	if err != nil {
		return nil, &slotOpenError{code: exitUsage, msg: unreadable}
	}
	var manifest teardownSlotManifest
	if err := json.Unmarshal(raw, &manifest); err != nil ||
		manifest.Server == "" || manifest.Slot == "" || manifest.Holder == "" || manifest.Port == 0 {
		return nil, &slotOpenError{code: exitUsage, msg: unreadable}
	}
	if err := teardownCheckManifest(dir, &manifest); err != nil {
		return nil, &slotOpenError{code: exitUsage, msg: err.Error()}
	}
	// The file must hold a token the agent generates (rpc.NewToken: 32
	// random bytes as 43 base64url characters): anything else (quotes, a
	// backslash, markup, an inner newline, the wrong length) is refused
	// before use, without printing it.
	token, err := rpc.ReadTokenFile(filepath.Join(dir, fmt.Sprintf("rpc-%d.token", manifest.Port)))
	if err != nil {
		return nil, &slotOpenError{code: exitSlotRefused, msg: "rpc: token file: " + rpcScrub(err.Error(), "")}
	}
	if !rpcValidToken(token) {
		return nil, &slotOpenError{code: exitSlotRefused, msg: "rpc: the token file is not an agent token"}
	}
	return &slotClient{dir: dir, manifest: manifest, token: token, post: rpcPostCall}, nil
}

// Manifest is the slot.json the client was opened with.
func (c *slotClient) Manifest() teardownSlotManifest { return c.manifest }

// Call marshals params (nil -> no params), posts, and unmarshals the result
// into out (out may be nil). A JSON-RPC error becomes *slotRPCError with a
// scrubbed message; transport failures keep their type (so errors.As still
// finds *rpcBadReplyError) with scrubbed text. ctx cancel aborts the wait.
func (c *slotClient) Call(ctx context.Context, method string, params, out any, timeout time.Duration) error {
	if ctx == nil {
		ctx = context.Background()
	}
	var raw json.RawMessage
	switch p := params.(type) {
	case nil:
	case json.RawMessage:
		raw = p
	default:
		b, err := json.Marshal(p)
		if err != nil {
			return &scrubError{err: err, msg: rpcScrub(fmt.Sprintf("%s: params: %v", method, err), c.token)}
		}
		raw = b
	}
	type reply struct {
		result json.RawMessage
		rpcErr *rpcError
		err    error
	}
	done := make(chan reply, 1)
	post := c.post
	go func() {
		result, rpcErr, err := post(c.manifest.Port, c.token, method, raw, timeout)
		done <- reply{result: result, rpcErr: rpcErr, err: err}
	}()
	var r reply
	select {
	case r = <-done:
	case <-ctx.Done():
		return ctx.Err()
	}
	if r.err != nil {
		return &scrubError{err: r.err, msg: rpcScrub(r.err.Error(), c.token)}
	}
	if r.rpcErr != nil {
		return &slotRPCError{Code: r.rpcErr.Code, Message: rpcScrub(r.rpcErr.Message, c.token)}
	}
	if out != nil && r.result != nil {
		if err := json.Unmarshal(r.result, out); err != nil {
			return &scrubError{err: err, msg: rpcScrub(err.Error(), c.token)}
		}
	}
	return nil
}
