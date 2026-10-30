package main

// Tests for the shared slot RPC client (spec S0): openSlotClient is
// rpcCmd's manifest/token check as a value, and Call is one JSON-RPC call
// whose every error text passes rpcScrub.
//
// The token files here hold fake values made by the test; no real token
// is read or printed.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// A token file that is not an agent token is refused, with the fixed line,
// and never echoed: the quotes stay out of the error text.
// Mutation: skipping rpcValidToken in openSlotClient sends the file content
// to the server (the fake post below records it) or prints it.
func TestSlotClientRefusesNonAgentToken(t *testing.T) {
	dir := rpcCLITestDir(t, 41234, rpcCLITestToken(t))
	quoted := `"0123456789012345678901234567890123456789ab"`
	tokenPath := filepath.Join(dir, "rpc-41234.token")
	if err := os.WriteFile(tokenPath, []byte(quoted+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	c, err := openSlotClient(dir)
	if err == nil {
		t.Fatalf("opened the client with a non-agent token: %#v", c)
	}
	if strings.Contains(err.Error(), quoted) || strings.Contains(err.Error(), strings.Repeat("0", 10)) {
		t.Fatalf("error echoes the token file: %q", err)
	}
	var oe *slotOpenError
	if !errors.As(err, &oe) {
		t.Fatalf("error %v is not a *slotOpenError", err)
	}
	if oe.code != exitSlotRefused {
		t.Fatalf("code %d, want %d", oe.code, exitSlotRefused)
	}
	if !strings.Contains(err.Error(), "not an agent token") {
		t.Fatalf("error %q has no not-an-agent-token line", err)
	}
}

// An agent error whose message contains the token comes back as a
// *slotRPCError with the message scrubbed (the <token> marker in place),
// and Error() scrubbed too.
// Mutation: dropping the rpcScrub in Call leaves the token in both.
func TestSlotClientScrubsTokenFromRPCError(t *testing.T) {
	token := rpcCLITestToken(t)
	c := &slotClient{token: token}
	c.post = func(port int, gotToken, method string, params json.RawMessage, timeout time.Duration) (json.RawMessage, *rpcError, error) {
		if gotToken != token {
			t.Fatalf("post token %q, want the client's", gotToken)
		}
		return nil, &rpcError{Code: -32000, Message: "boom " + token}, nil
	}
	err := c.Call(context.Background(), "fail", nil, nil, time.Second)
	if err == nil {
		t.Fatal("no error for a JSON-RPC error reply")
	}
	if strings.Contains(err.Error(), token) {
		t.Fatalf("token in error %q", err)
	}
	var se *slotRPCError
	if !errors.As(err, &se) {
		t.Fatalf("error %T %v is not a *slotRPCError", err, err)
	}
	if se.Code != -32000 {
		t.Fatalf("code %d", se.Code)
	}
	if !strings.Contains(se.Message, "<token>") {
		t.Fatalf("message %q has no redaction marker", se.Message)
	}
	if !strings.Contains(err.Error(), "<token>") {
		t.Fatalf("Error() %q has no redaction marker", err.Error())
	}
}

// Call marshals params (nil sends none), decodes the result into out, and
// tolerates a nil out.
// Mutation: sending "params":null for nil fails the nil-params check;
// dropping the unmarshal leaves out zero.
func TestSlotClientDecodesResultAndRPCError(t *testing.T) {
	c := &slotClient{manifest: teardownSlotManifest{Port: 41234}, token: rpcCLITestToken(t)}
	var calls []struct {
		method string
		params json.RawMessage
	}
	c.post = func(port int, token, method string, params json.RawMessage, timeout time.Duration) (json.RawMessage, *rpcError, error) {
		if port != 41234 {
			t.Fatalf("port %d", port)
		}
		calls = append(calls, struct {
			method string
			params json.RawMessage
		}{method, params})
		switch method {
		case "status":
			return json.RawMessage(`{"state":"in_world","map":249}`), nil, nil
		case "empty":
			return nil, nil, nil
		default:
			return nil, nil, fmt.Errorf("no such method %s", method)
		}
	}
	var got struct {
		State string `json:"state"`
		Map   uint32 `json:"map"`
	}
	if err := c.Call(context.Background(), "status", map[string]any{"within": 80}, &got, time.Second); err != nil {
		t.Fatalf("status: %v", err)
	}
	if got.State != "in_world" || got.Map != 249 {
		t.Fatalf("decoded %+v", got)
	}
	if len(calls) != 1 || calls[0].method != "status" {
		t.Fatalf("calls %+v", calls)
	}
	var params map[string]any
	if err := json.Unmarshal(calls[0].params, &params); err != nil || params["within"] != float64(80) {
		t.Fatalf("params %s (%v)", calls[0].params, err)
	}
	if err := c.Call(context.Background(), "empty", nil, nil, time.Second); err != nil {
		t.Fatalf("empty: %v", err)
	}
	if calls[1].params != nil {
		t.Fatalf("nil params sent as %s", calls[1].params)
	}
	err := c.Call(context.Background(), "missing", nil, nil, time.Second)
	if err == nil || !strings.Contains(err.Error(), "no such method") {
		t.Fatalf("transport error %v", err)
	}
}
