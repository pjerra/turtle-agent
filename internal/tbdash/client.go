// Package tbdash is turtle-agent's client for the TortoiseBots dashboard daemon's JSON API.
// It logs in with a password read from a private file and keeps the daemon's session cookie in
// a jar; the raid tools use it to save the commander snapshots and combat fights of a walk.
//
// The password is only ever read from the file path given to New. It never appears in an error
// text, and the buffers holding it are zeroed once the login request has been sent.
package tbdash

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/cookiejar"
	"net/url"
	"strings"
	"sync"

	"github.com/pjerra/turtle-agent/internal/privfile"
)

const (
	loginPath        = "/api/v1/auth/login"
	maxPasswordBytes = 4096
)

// HTTPError is a non-200 answer from the daemon other than the 401/302 that renews the session.
// It carries no response body: the daemon echoes request values in some of its errors.
type HTTPError struct {
	Code   int
	Method string
	Path   string
}

func (e *HTTPError) Error() string {
	return fmt.Sprintf("HTTP %d for %s %s", e.Code, e.Method, e.Path)
}

// Client talks to one dashboard daemon as one user. Base is the daemon's root URL, User the
// login name and passFile the path of the file holding the password.
type Client struct {
	Base     *url.URL
	User     string
	passFile string
	hc       *http.Client
	loginMu  sync.Mutex // one login at a time when several reads meet a 401 together
}

// New validates base (http or https, with a host) and builds a client whose cookie jar keeps the
// session the daemon hands out at login. It does not read passFile; Login does.
func New(base, user, passFile string) (*Client, error) {
	u, err := url.Parse(strings.TrimSpace(base))
	if err != nil {
		return nil, fmt.Errorf("dashboard URL %q: %w", base, err)
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return nil, fmt.Errorf("dashboard URL %q: the scheme must be http or https", base)
	}
	if u.Host == "" {
		return nil, fmt.Errorf("dashboard URL %q: no host", base)
	}
	if u.RawQuery != "" || u.Fragment != "" {
		return nil, fmt.Errorf("dashboard URL %q: a base URL has no query or fragment", base)
	}
	u.Path = strings.TrimSuffix(u.Path, "/")
	u.RawPath = strings.TrimSuffix(u.RawPath, "/")
	jar, err := cookiejar.New(nil)
	if err != nil {
		return nil, fmt.Errorf("dashboard cookie jar: %w", err)
	}
	hc := &http.Client{
		Jar: jar,
		// The daemon answers an unauthenticated API read with 401, but sends browsers to the
		// login page with a 302. Both must come back here so GetJSON can renew the session once.
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	}
	return &Client{Base: u, User: user, passFile: passFile, hc: hc}, nil
}

// Login reads the password file, POSTs the credentials and keeps the session cookie. The file
// must be one only its owner can access (privfile.Read refuses a symlink and a loose mode) and
// its one trailing newline is dropped. The password never reaches an error text.
func (c *Client) Login(ctx context.Context) error {
	c.loginMu.Lock()
	defer c.loginMu.Unlock()

	raw, err := privfile.Read(c.passFile, maxPasswordBytes)
	if err != nil {
		return fmt.Errorf("dashboard password file: %w", err)
	}
	raw = trimOneNewline(raw)
	defer zero(raw)
	password := string(raw)

	body, err := json.Marshal(struct {
		Username string `json:"username"`
		Password string `json:"password"`
	}{Username: c.User, Password: password})
	if err != nil {
		return scrub(fmt.Errorf("dashboard login: %w", err), password)
	}
	defer zero(body)

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.requestURL(loginPath, nil), bytes.NewReader(body))
	if err != nil {
		return scrub(fmt.Errorf("dashboard login: %w", err), password)
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := c.hc.Do(req)
	if err != nil {
		return scrub(fmt.Errorf("dashboard login: %w", err), password)
	}
	defer resp.Body.Close()
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 64<<10))
	if resp.StatusCode != http.StatusOK {
		return scrub(fmt.Errorf("dashboard login: %w", &HTTPError{Code: resp.StatusCode, Method: http.MethodPost, Path: loginPath}), password)
	}
	return nil
}

// GetJSON GETs path (an escaped path under Base) with query and returns the daemon's JSON body.
// A 401 or a 302 renews the session once with Login and retries; any other non-200, and a retry
// that still fails, is an *HTTPError.
func (c *Client) GetJSON(ctx context.Context, path string, query url.Values) (json.RawMessage, error) {
	if !strings.HasPrefix(path, "/") {
		path = "/" + path
	}
	body, code, err := c.get(ctx, path, query)
	if err != nil {
		return nil, err
	}
	if code == http.StatusUnauthorized || code == http.StatusFound {
		if err := c.Login(ctx); err != nil {
			return nil, err
		}
		if body, code, err = c.get(ctx, path, query); err != nil {
			return nil, err
		}
	}
	if code != http.StatusOK {
		return nil, &HTTPError{Code: code, Method: http.MethodGet, Path: path}
	}
	return json.RawMessage(body), nil
}

// Commanders reads GET /api/v1/commanders.
func (c *Client) Commanders(ctx context.Context) (json.RawMessage, error) {
	return c.GetJSON(ctx, "/api/v1/commanders", nil)
}

// Fights reads GET /api/v1/combat/fights, filtered to commander when it is not empty.
func (c *Client) Fights(ctx context.Context, commander string) (json.RawMessage, error) {
	var query url.Values
	if commander != "" {
		query = url.Values{"commander": {commander}}
	}
	return c.GetJSON(ctx, "/api/v1/combat/fights", query)
}

// Fight reads GET /api/v1/combat/fight/<key>. Keys hold NUL separators, so the key is escaped as
// one path segment.
func (c *Client) Fight(ctx context.Context, key string) (json.RawMessage, error) {
	return c.GetJSON(ctx, "/api/v1/combat/fight/"+url.PathEscape(key), nil)
}

func (c *Client) get(ctx context.Context, path string, query url.Values) ([]byte, int, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.requestURL(path, query), nil)
	if err != nil {
		return nil, 0, fmt.Errorf("dashboard GET %s: %w", path, err)
	}
	resp, err := c.hc.Do(req)
	if err != nil {
		return nil, 0, fmt.Errorf("dashboard GET %s: %w", path, err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, 0, fmt.Errorf("dashboard GET %s: reading the response: %w", path, err)
	}
	return body, resp.StatusCode, nil
}

// requestURL joins Base with an already-escaped path, keeping the escaping (a fight key's NUL
// bytes) intact instead of re-escaping the percent signs.
func (c *Client) requestURL(escapedPath string, query url.Values) string {
	var b strings.Builder
	b.WriteString(c.Base.Scheme)
	b.WriteString("://")
	b.WriteString(c.Base.Host)
	b.WriteString(strings.TrimSuffix(c.Base.EscapedPath(), "/"))
	b.WriteString(escapedPath)
	if len(query) > 0 {
		b.WriteByte('?')
		b.WriteString(query.Encode())
	}
	return b.String()
}

// scrub replaces the password in an error's text. Nothing else may carry it to the caller.
func scrub(err error, password string) error {
	if err == nil || password == "" {
		return err
	}
	return errors.New(strings.ReplaceAll(err.Error(), password, "***"))
}

func trimOneNewline(b []byte) []byte {
	if n := len(b); n > 0 && b[n-1] == '\n' {
		b = b[:n-1]
		if n = len(b); n > 0 && b[n-1] == '\r' {
			b = b[:n-1]
		}
	}
	return b
}

func zero(b []byte) {
	for i := range b {
		b[i] = 0
	}
}
