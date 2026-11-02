package dashboard

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"sync"
	"time"

	"github.com/pjerra/turtle-agent/internal/rpc"
)

// The write client of the run controls (README "Controls (--control)"). It is separate from Client (agents.go), whose Call is read-only
// by construction: the controls are the only thing that sends a command, and this client may call only what the Run controls need. Every other
// method name is refused before any HTTP request is made.

// controlReadMethods are the reads a control uses to decide from a fresh answer (D8), never the poller's cache.
var controlReadMethods = map[string]bool{"dungeon_status": true, "tune_list": true, "bot_view": true}

// controlWriteMethods are the writes the controls may make. The run controls take no parameter; tune_set takes {key, value}. The
// party-order entry (commander) belongs to slice 3 of #183 and is added with its handler.
var controlWriteMethods = map[string]bool{"dungeon_pause": true, "dungeon_resume": true, "dungeon_skip": true, "dungeon_stop": true,
	"tune_set": true}

// controlParamMethods are the writes that carry parameters: only these may be given them.
var controlParamMethods = map[string]bool{"tune_set": true}

// ErrIndeterminate is returned when the agent answered but not with a JSON-RPC 2.0 reply to this request (no envelope, another id, no
// result). For a write the command may already have run, so the caller must neither call it OK nor a plain refusal.
var ErrIndeterminate = errors.New("indeterminate")

// rpcID is the id every control request carries; a reply must echo it.
const rpcID = 1

// ControlClient calls the write methods of one agent. It holds no cache: the run handler makes one per control.
type ControlClient struct {
	Agent Agent
	HTTP  *http.Client
	mu    sync.Mutex
	token string // the token of the last call, so a reply that echoes it can be redacted
}

// NewControlClient makes a client with its own bounded transport. The timeout must leave room for dungeon_pause/stop, which wait up to 10 s
// for a run to end.
func NewControlClient(a Agent, timeout time.Duration) *ControlClient {
	return &ControlClient{Agent: a, HTTP: &http.Client{Timeout: timeout, Transport: &http.Transport{DisableKeepAlives: true}}}
}

// Token is the bearer token of the last call ("" before the first, or when the agent has none).
func (c *ControlClient) Token() string {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.token
}

// Read runs a read method (dungeon_status, tune_list, bot_view) with no parameters and returns the result.
func (c *ControlClient) Read(ctx context.Context, method string) (json.RawMessage, error) {
	if !controlReadMethods[method] {
		return nil, ErrNotAllowed
	}
	return c.call(ctx, method, nil)
}

// Write runs a write method with its parameters. The run controls take none, so a parameter is refused; tune_set takes them.
func (c *ControlClient) Write(ctx context.Context, method string, params any) (json.RawMessage, error) {
	if !controlWriteMethods[method] {
		return nil, ErrNotAllowed
	}
	if params != nil && !controlParamMethods[method] {
		return nil, ErrNotAllowed
	}
	return c.call(ctx, method, params)
}

// call sends one JSON-RPC request. A reply must be a JSON-RPC 2.0 envelope that echoes the request id and carries a result or an error;
// anything else is ErrIndeterminate, never a success.
func (c *ControlClient) call(ctx context.Context, method string, params any) (json.RawMessage, error) {
	req0 := map[string]any{"jsonrpc": "2.0", "id": rpcID, "method": method}
	if params != nil {
		req0["params"] = params
	}
	body, _ := json.Marshal(req0)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, "http://"+c.Agent.Addr+"/rpc", bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	if tok, err := rpc.ReadTokenFile(c.Agent.TokenPath); err == nil {
		req.Header.Set("Authorization", "Bearer "+tok)
		c.mu.Lock()
		c.token = tok
		c.mu.Unlock()
	}
	resp, err := c.HTTP.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusUnauthorized {
		return nil, errors.New("unauthorized: no readable " + tokenFileName(c.Agent) + " next to the settings file")
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("http %d", resp.StatusCode)
	}
	data, err := io.ReadAll(io.LimitReader(resp.Body, maxReply))
	if err != nil {
		return nil, err
	}
	var env struct {
		JSONRPC string          `json:"jsonrpc"`
		ID      any             `json:"id"`
		Result  json.RawMessage `json:"result"`
		Error   *struct {
			Code    int    `json:"code"`
			Message string `json:"message"`
		} `json:"error"`
	}
	if err := json.Unmarshal(data, &env); err != nil {
		return nil, fmt.Errorf("%w: the reply is not JSON (%v)", ErrIndeterminate, err)
	}
	if env.JSONRPC != "2.0" {
		return nil, fmt.Errorf("%w: the reply has no jsonrpc 2.0 envelope", ErrIndeterminate)
	}
	if id, ok := env.ID.(float64); !ok || id != rpcID {
		return nil, fmt.Errorf("%w: the reply's id is not the request's id", ErrIndeterminate)
	}
	if env.Error != nil {
		return nil, &RPCError{Code: env.Error.Code, Message: env.Error.Message}
	}
	if len(env.Result) == 0 { // "result": null is a result; no member at all is not
		return nil, fmt.Errorf("%w: the reply has no result", ErrIndeterminate)
	}
	return env.Result, nil
}
