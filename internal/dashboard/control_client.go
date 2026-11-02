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

// controlWriteMethods are the writes the controls may make. None of them takes a parameter. The live-tuning (tune_set) and party-order
// (commander) entries belong to slices 2 and 3 of #183 and are added with their handlers.
var controlWriteMethods = map[string]bool{"dungeon_pause": true, "dungeon_resume": true, "dungeon_skip": true, "dungeon_stop": true}

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

// Write runs a write method with its parameters: the four run controls take none, so any parameter is refused (a later slice adds a
// parameter-taking write here with its own check).
func (c *ControlClient) Write(ctx context.Context, method string, params any) (json.RawMessage, error) {
	if !controlWriteMethods[method] {
		return nil, ErrNotAllowed
	}
	if params != nil {
		return nil, ErrNotAllowed
	}
	return c.call(ctx, method, nil)
}

// call sends one JSON-RPC request. The envelope parsing is the read client's (agents.go), with the parameters a control may carry.
func (c *ControlClient) call(ctx context.Context, method string, params any) (json.RawMessage, error) {
	req0 := map[string]any{"jsonrpc": "2.0", "id": 1, "method": method}
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
		Result json.RawMessage `json:"result"`
		Error  *struct {
			Code    int    `json:"code"`
			Message string `json:"message"`
		} `json:"error"`
	}
	if err := json.Unmarshal(data, &env); err != nil {
		return nil, err
	}
	if env.Error != nil {
		return nil, &RPCError{Code: env.Error.Code, Message: env.Error.Message}
	}
	return env.Result, nil
}
