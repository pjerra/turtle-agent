package scenario

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"strings"
	"time"

	"github.com/pjerra/turtle-agent/internal/rpc"
)

// maxReply caps one RPC reply at 4 MiB.
const maxReply = 4 << 20

// HTTPCaller is a JSON-RPC client of one running agent: unlike the
// dashboard's read-only client it may call any method, since a scenario holds
// the agent's token anyway. The loopback check runs at every call so a token
// never leaves the machine.
type HTTPCaller struct {
	Addr      string
	TokenFile string
	HTTP      *http.Client
	token     string
}

// Token is the bearer token of the last call ("" before the first).
func (c *HTTPCaller) Token() string { return c.token }

// Call posts one JSON-RPC request and returns the result, or an *RPCError.
// The token file is read at every call (a restarted agent has a new token)
// and never appears in an error.
func (c *HTTPCaller) Call(ctx context.Context, method string, params any, timeout time.Duration) (json.RawMessage, error) {
	host, _, err := net.SplitHostPort(c.Addr)
	if err != nil {
		return nil, fmt.Errorf("scenario: rpc %q is not a loopback address (the agent only listens on loopback; a token must never leave the machine)", c.Addr)
	}
	if host != "127.0.0.1" && host != "localhost" && host != "[::1]" && host != "::1" {
		return nil, fmt.Errorf("scenario: rpc %q is not a loopback address (the agent only listens on loopback; a token must never leave the machine)", c.Addr)
	}
	id := 1
	body, _ := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": id, "method": method, "params": params})
	tctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	req, err := http.NewRequestWithContext(tctx, http.MethodPost, "http://"+c.Addr+"/rpc", bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	tok, err := rpc.ReadTokenFile(c.TokenFile)
	if err != nil {
		return nil, fmt.Errorf("scenario: token file: %v", err)
	}
	req.Header.Set("Authorization", "Bearer "+tok)
	c.token = tok
	hc := c.HTTP
	if hc == nil {
		hc = &http.Client{Timeout: timeout, Transport: &http.Transport{DisableKeepAlives: true}}
	}
	resp, err := hc.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusUnauthorized {
		return nil, fmt.Errorf("scenario: unauthorized: unreadable token file for %s", c.Addr)
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("scenario: rpc %s: http %d", method, resp.StatusCode)
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
		return nil, fmt.Errorf("scenario: rpc %s: bad reply", method)
	}
	if env.Error != nil {
		return nil, &RPCError{Code: env.Error.Code, Message: env.Error.Message}
	}
	return env.Result, nil
}

// tokenPathFor returns the default token file for an RPC address: the
// rpc-<port>.token layout next to the settings file, under ~/.config.
// os.UserHomeDir expands the leading ~/.
func defaultTokenFile(addr string) string {
	_, port, err := net.SplitHostPort(addr)
	if err != nil || port == "" {
		return ""
	}
	return "~/.config/turtle-agent/rpc-" + strings.TrimSpace(port) + ".token"
}

var _ = defaultTokenFile
