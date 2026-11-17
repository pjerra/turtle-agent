package main

import (
	"bytes"
	"context"
	"strings"
	"testing"
	"time"

	"github.com/TortoiseBots/turtle-agent/internal/opcode"
	"github.com/TortoiseBots/turtle-agent/internal/packet"
	"github.com/TortoiseBots/turtle-agent/internal/rpc"
	"github.com/TortoiseBots/turtle-agent/internal/worldconn/worldtest"
)

// Issues #9 and #18: a logout over the RPC answers the call with how the logout ended and
// then ends the process at once, whatever the server does: completes it, closes the
// socket after the response or instead of it, or never answers.
func TestRPCLogoutEndsProcess(t *testing.T) {
	accepted := packet.NewWriter().U32(0).U8(0).Bytes()
	for _, tc := range []struct {
		name    string
		server  func(srv *worldtest.Server)
		outcome string // result.outcome, or error.data.outcome with errCode
		errCode int    // 0: a result
		code    int
		stderr  string
	}{
		{"response and complete", func(srv *worldtest.Server) {
			srv.Send(opcode.SMSGLogoutResponse, accepted)
			srv.Send(opcode.SMSGLogoutComplete, nil)
		}, "complete", 0, exitOK, "logout: complete"},
		{"close after response", func(srv *worldtest.Server) {
			srv.Send(opcode.SMSGLogoutResponse, accepted)
			srv.Close()
		}, "closed_by_server", 0, exitOK, "logout: the server closed the connection"},
		{"close without response", func(srv *worldtest.Server) { srv.Close() },
			"lost", rpc.CodeDisconnected, exitLogoutIncomplete, "before the server accepted the logout"},
		{"never answers", func(srv *worldtest.Server) {
			srv.Expect(0xFFFF, 10*time.Second) // hold the socket until the agent closes it
		}, "timeout", rpc.CodeTimeout, exitLogoutIncomplete, "logout: timed out"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			old := logoutTimeout
			logoutTimeout = 500 * time.Millisecond
			t.Cleanup(func() { logoutTimeout = old })
			cfg, done := worldScript(t, func(srv *worldtest.Server) error {
				if _, err := srv.Expect(opcode.CMSGLogoutRequest, 10*time.Second); err != nil {
					return err
				}
				tc.server(srv)
				return nil
			})
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			var stderr syncBuffer
			codeC, exited := make(chan int, 1), make(chan struct{})
			go func() {
				defer close(exited)
				codeC <- run(ctx, []string{"login", "--config", cfg, "harness", "Harnessb"}, &bytes.Buffer{}, &stderr)
			}()
			t.Cleanup(func() { cancel(); <-exited })
			addr := rpcAddr(t, &stderr)
			out := rpcCall(t, addr, "logout", "{}")
			if tc.errCode == 0 {
				res, _ := out["result"].(map[string]any)
				if out["error"] != nil || res["outcome"] != tc.outcome {
					t.Fatalf("logout reply %v", out)
				}
			} else if e, _ := out["error"].(map[string]any); errCode(out) != tc.errCode || e["data"].(map[string]any)["outcome"] != tc.outcome {
				t.Fatalf("logout reply %v", out)
			}
			select {
			case code := <-codeC:
				if code != tc.code {
					t.Fatalf("code %d, stderr %q", code, stderr.String())
				}
			case <-time.After(3 * time.Second):
				t.Fatalf("process still up after the logout, stderr %q", stderr.String())
			}
			if err := <-done; err != nil {
				t.Fatal(err)
			}
			if s := stderr.String(); !strings.Contains(s, tc.stderr) || strings.Contains(s, "serving status") {
				t.Fatalf("stderr %q", s)
			}
		})
	}
}
