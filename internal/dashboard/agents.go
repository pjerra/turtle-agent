package dashboard

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/pjerra/turtle-agent/internal/netutil"
	"github.com/pjerra/turtle-agent/internal/rpc"
)

// Agent is one running turtle-agent the dashboard watches.
type Agent struct {
	ID        string // the RPC port, as text
	Addr      string // host:port, loopback only
	TokenPath string // rpc-<port>.token next to the settings file
	// DungeonsDir is the settings file's dungeons_dir (a leading ~/ made the home folder): the folder the agent's runs read their plan files
	// from. "" when the settings name none (the agent then reads dungeons/ under its working folder, which the dashboard cannot see).
	DungeonsDir string
}

// DefaultConfigDir is where the agent keeps its settings files and RPC token files.
func DefaultConfigDir() string {
	home, err := os.UserHomeDir()
	if err != nil {
		return ".config/turtle-agent"
	}
	return filepath.Join(home, ".config", "turtle-agent")
}

// settingsFields are the two fields of a settings file the dashboard reads: where the agent listens and which folder its runs read
// plans from. The accounts, the passwords and every other field of the file are skipped by the decoder and never held.
type settingsFields struct {
	RPC         string `json:"rpc"`
	DungeonsDir string `json:"dungeons_dir"`
}

// expandHome turns a leading ~/ into the home folder, as the agent's settings loader does.
func expandHome(p string) string {
	if p == "~" || strings.HasPrefix(p, "~/") {
		if home, err := os.UserHomeDir(); err == nil {
			return filepath.Join(home, p[1:])
		}
	}
	return p
}

// SlotDungeonDirs lists the dungeons_dir of every settings*.json in configDir (even a file with no rpc field): the folders the commander
// runs of this machine read their plans from. A leading ~/ is made the home folder; a file that does not read is skipped, and an error is
// returned for a configDir that cannot be listed.
func SlotDungeonDirs(configDir string) ([]string, error) {
	files, err := filepath.Glob(filepath.Join(configDir, "settings*.json"))
	if err != nil {
		return nil, err
	}
	var out []string
	for _, f := range files {
		data, err := os.ReadFile(f)
		if err != nil {
			continue
		}
		var s settingsFields
		if json.Unmarshal(data, &s) == nil && s.DungeonsDir != "" {
			out = append(out, expandHome(s.DungeonsDir))
		}
	}
	return out, nil
}

// DiscoverAgents lists the agents to watch. spec is "auto" (the "rpc" field of configDir/settings*.json), a comma list of
// RPC addresses ("8765" or "127.0.0.1:8766"), or both ("auto,8770"). Every address must be loopback.
func DiscoverAgents(configDir, spec string) ([]Agent, error) {
	var out []Agent
	seen := map[string]bool{}
	add := func(addr, tokenDir, dungeonsDir string) error {
		host, port, err := netutil.CheckLoopbackAddr(addr)
		if err != nil {
			return fmt.Errorf("agent %q: %w", addr, err)
		}
		if port == 0 {
			return fmt.Errorf("agent %q: port 0", addr)
		}
		a := host + ":" + strconv.Itoa(port)
		if seen[a] {
			return nil
		}
		seen[a] = true
		out = append(out, Agent{ID: strconv.Itoa(port), Addr: a, TokenPath: filepath.Join(tokenDir, "rpc-"+strconv.Itoa(port)+".token"), DungeonsDir: dungeonsDir})
		return nil
	}
	for _, part := range strings.Split(spec, ",") {
		part = strings.TrimSpace(part)
		switch {
		case part == "":
		case part == "auto":
			files, _ := filepath.Glob(filepath.Join(configDir, "settings*.json"))
			sort.Strings(files)
			for _, f := range files {
				data, err := os.ReadFile(f)
				if err != nil {
					continue
				}
				var s settingsFields
				if json.Unmarshal(data, &s) != nil || s.RPC == "" {
					continue
				}
				_ = add(s.RPC, filepath.Dir(f), expandHome(s.DungeonsDir)) // a settings file with a bad rpc value is skipped, not fatal
			}
		default:
			if !strings.Contains(part, ":") {
				part = "127.0.0.1:" + part
			}
			if err := add(part, configDir, ""); err != nil {
				return nil, err
			}
		}
	}
	sort.Slice(out, func(i, j int) bool { return portOf(out[i]) < portOf(out[j]) })
	return out, nil
}

func portOf(a Agent) int { n, _ := strconv.Atoi(a.ID); return n }

// readOnlyMethods are the only RPC methods the dashboard calls. None of them changes anything in the agent or the game
// (bot_view is called without parameters: its log_every switch is never sent).
var readOnlyMethods = map[string]bool{"status": true, "dungeon_status": true, "tune_list": true, "bot_view": true}

// maxReply bounds what is read from one reply.
const maxReply = 4 << 20

// Client calls the read-only methods of one agent.
type Client struct {
	Agent Agent
	HTTP  *http.Client
	mu    sync.Mutex
	token string // the token of the last call, so a reply that echoes it can be redacted
}

// Token is the bearer token of the last call ("" before the first, or when the agent has none).
func (c *Client) Token() string {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.token
}

// NewClient makes a client with its own bounded transport.
func NewClient(a Agent, timeout time.Duration) *Client {
	return &Client{Agent: a, HTTP: &http.Client{Timeout: timeout, Transport: &http.Transport{DisableKeepAlives: true}}}
}

// ErrNotAllowed is returned for a method outside the read-only list.
var ErrNotAllowed = errors.New("dashboard: method is not read-only")

// RPCError is a JSON-RPC error an agent answered.
type RPCError struct {
	Code    int
	Message string
}

func (e *RPCError) Error() string { return fmt.Sprintf("rpc error %d: %s", e.Code, e.Message) }

// Call runs method (which must be on the read-only list) with no parameters and returns the result.
func (c *Client) Call(ctx context.Context, method string) (json.RawMessage, error) {
	if !readOnlyMethods[method] {
		return nil, ErrNotAllowed
	}
	body, _ := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": 1, "method": method})
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
		return nil, errors.New("unauthorized: no readable rpc-" + c.Agent.ID + ".token next to the settings file")
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
