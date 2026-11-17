// Copyright (c) 2026 agent-wow
// SPDX-License-Identifier: MIT
// Ported from github.com/agent-wow/agent-wow pkg/worldrpc/server.go and router.go at
// 5ef4916 (see NOTICE): loopback-only Listen, the local-process request checks and the
// JSON-RPC 2.0 envelope are kept; dispatch is a method registry instead of gRPC modules.

// Package rpc serves local JSON-RPC 2.0 over HTTP POST /rpc.
package rpc

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"io"
	"mime"
	"net"
	"net/http"
	"sort"
	"strconv"
	"sync"
	"time"

	"github.com/TortoiseBots/turtle-agent/internal/netutil"
)

const (
	CodeParse          = -32700
	CodeInvalidRequest = -32600
	CodeMethodNotFound = -32601
	CodeInvalidParams  = -32602
	CodeInternal       = -32000
	CodeNotInWorld     = -32001
	CodeDisconnected   = -32002
	CodeGame           = -32003
	CodeTimeout        = -32004
)

// Error is a JSON-RPC error; methods return it to choose the code.
type Error struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
	Data    any    `json:"data,omitempty"`
}

func (e *Error) Error() string { return e.Message }

type Method func(ctx context.Context, params json.RawMessage) (any, error)

// Typed decodes object params into P (unknown fields rejected) and calls fn.
func Typed[P, R any](fn func(ctx context.Context, p P) (R, error)) Method {
	return func(ctx context.Context, raw json.RawMessage) (any, error) {
		var p P
		if t := bytes.TrimSpace(raw); len(t) > 0 && string(t) != "null" {
			dec := json.NewDecoder(bytes.NewReader(t))
			dec.DisallowUnknownFields()
			if err := dec.Decode(&p); err != nil {
				return nil, &Error{Code: CodeInvalidParams, Message: "invalid params: " + err.Error()}
			}
		}
		return fn(ctx, p)
	}
}

// Server routes calls. It fails closed: until SetToken or DisableAuth is called, Handler
// answers every request with 401.
type Server struct {
	mu      sync.RWMutex
	methods map[string]Method
	auth    authMode
	token   [sha256.Size]byte // SHA-256 of the bearer token (authToken)
}

type authMode uint8

const (
	authClosed authMode = iota // nothing configured: refuse everything
	authToken
	authOff
)

func NewServer() *Server { return &Server{methods: map[string]Method{}} }

// SetToken requires "Authorization: Bearer <token>" on every call.
func (s *Server) SetToken(token string) {
	s.mu.Lock()
	s.token, s.auth = sha256.Sum256([]byte(token)), authToken
	s.mu.Unlock()
}

// DisableAuth lets every request through that passes the loopback, Host and Origin checks
// (settings "rpc_auth": "off").
func (s *Server) DisableAuth() {
	s.mu.Lock()
	s.auth = authOff
	s.mu.Unlock()
}

func (s *Server) authorized(r *http.Request) bool {
	s.mu.RLock()
	mode, tok := s.auth, s.token
	s.mu.RUnlock()
	switch mode {
	case authOff:
		return true
	case authToken:
		return tokenMatch(r.Header.Get("Authorization"), tok)
	}
	return false
}

func (s *Server) Register(name string, m Method) {
	s.mu.Lock()
	s.methods[name] = m
	s.mu.Unlock()
}

// Methods lists the registered names, sorted.
func (s *Server) Methods() []string {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]string, 0, len(s.methods))
	for n := range s.methods {
		out = append(out, n)
	}
	sort.Strings(out)
	return out
}

// Listen binds a loopback address only (localhost means 127.0.0.1); the port may be 0.
func Listen(ctx context.Context, address string) (net.Listener, error) {
	host, port, err := netutil.CheckLoopbackAddr(address)
	if err != nil {
		return nil, errors.New("RPC listen address " + err.Error())
	}
	return (&net.ListenConfig{}).Listen(ctx, "tcp", net.JoinHostPort(host, strconv.Itoa(port)))
}

// shutdownGrace is how long calls in flight run on after Serve's ctx ends before their
// contexts are cancelled, which ends long-polls with the page they have.
const shutdownGrace = time.Second

// Serve runs HTTP on ln until ctx ends, then shuts down, letting running calls finish (5 s).
// Request contexts are cancelled shutdownGrace into the shutdown, so a long-poll does not
// hold it up while a quick call (a logout reply) still answers normally.
func Serve(ctx context.Context, ln net.Listener, h http.Handler) error {
	base, cancelBase := context.WithCancel(context.Background())
	defer cancelBase()
	srv := &http.Server{Handler: h, ReadHeaderTimeout: 5 * time.Second,
		BaseContext: func(net.Listener) context.Context { return base }}
	errc := make(chan error, 1)
	go func() { errc <- srv.Serve(ln) }()
	select {
	case err := <-errc:
		return err
	case <-ctx.Done():
		sctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		grace := time.AfterFunc(shutdownGrace, cancelBase)
		defer grace.Stop()
		_ = srv.Shutdown(sctx)
		if err := <-errc; err != nil && !errors.Is(err, http.ErrServerClosed) {
			return err
		}
		return nil
	}
}

type request struct {
	Version string          `json:"jsonrpc"`
	Method  string          `json:"method"`
	Params  json.RawMessage `json:"params"`
	ID      json.RawMessage `json:"id"`
}

type response struct {
	Version string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id"`
	Result  json.RawMessage `json:"result,omitempty"`
	Error   *Error          `json:"error,omitempty"`
}

// Handler serves POST /rpc for local processes; browser-origin requests are refused and
// every call needs the bearer token (SetToken) unless DisableAuth was called.
// One request object per POST: JSON-RPC batches (arrays) are not supported and get
// Invalid Request.
func (s *Server) Handler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/rpc" {
			http.NotFound(w, r)
			return
		}
		if r.Method != http.MethodPost {
			w.Header().Set("Allow", "POST")
			http.Error(w, "POST required", http.StatusMethodNotAllowed)
			return
		}
		_, origin := r.Header["Origin"]
		_, fetch := r.Header["Sec-Fetch-Site"]
		host, _, err := net.SplitHostPort(r.Host)
		if err != nil {
			host = r.Host
		}
		if origin || fetch || !netutil.IsLoopback(host) {
			http.Error(w, "local process requests only", http.StatusForbidden)
			return
		}
		if !s.authorized(r) {
			// No detail: not which part was wrong, not that a token exists.
			w.Header().Set("WWW-Authenticate", "Bearer")
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		if media, _, err := mime.ParseMediaType(r.Header.Get("Content-Type")); err != nil || media != "application/json" {
			http.Error(w, "application/json required", http.StatusUnsupportedMediaType)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("Cache-Control", "no-store")
		w.Header().Set("X-Content-Type-Options", "nosniff")
		body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, 64<<10))
		if err != nil || !json.Valid(body) {
			writeJSON(w, response{Version: "2.0", ID: json.RawMessage("null"), Error: &Error{Code: CodeParse, Message: "Parse error"}})
			return
		}
		var req request
		trimmed := bytes.TrimSpace(body)
		if err := json.Unmarshal(body, &req); err != nil || trimmed[0] != '{' || req.Version != "2.0" || req.Method == "" || !validID(req.ID) {
			writeJSON(w, response{Version: "2.0", ID: json.RawMessage("null"), Error: &Error{Code: CodeInvalidRequest, Message: "Invalid Request"}})
			return
		}
		resp := response{Version: "2.0", ID: req.ID}
		s.mu.RLock()
		m, ok := s.methods[req.Method]
		s.mu.RUnlock()
		if !ok {
			resp.Error = &Error{Code: CodeMethodNotFound, Message: "Method not found: " + req.Method}
		} else if v, err := m(r.Context(), req.Params); err != nil {
			var re *Error
			if errors.As(err, &re) {
				resp.Error = re
			} else {
				resp.Error = &Error{Code: CodeInternal, Message: err.Error()}
			}
		} else if raw, err := json.Marshal(v); err != nil {
			resp.Error = &Error{Code: CodeInternal, Message: "encode result: " + err.Error()}
		} else {
			resp.Result = raw
		}
		if req.ID == nil {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		writeJSON(w, resp)
	})
}

func writeJSON(w http.ResponseWriter, v any) { _ = json.NewEncoder(w).Encode(v) }

func validID(id json.RawMessage) bool {
	if id == nil {
		return true
	}
	var v any
	d := json.NewDecoder(bytes.NewReader(id))
	d.UseNumber()
	if d.Decode(&v) != nil {
		return false
	}
	switch v.(type) {
	case nil, string, json.Number:
		return true
	}
	return false
}
