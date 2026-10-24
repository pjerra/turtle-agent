package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/pjerra/turtle-agent/internal/eluna"
	"github.com/pjerra/turtle-agent/internal/rpc"
	"github.com/pjerra/turtle-agent/internal/session"
)

// The `tt` method is one Eluna test toolkit command of the TEST server (TortoiseBots tools/eluna/test-toolkit): the agent sends
// `.tt <cmd> #<id>` and answers with the `TTJSON` replies that carry that id. A GM tool (the server refuses an account below rank 3
// itself), for hand queries and the run setup; a remote run's partway start uses the same client (dungeon eluna_place.go).

type ttParams struct {
	// Cmd is the toolkit command without the `.tt` prefix and without a request id, e.g. "q units 40" or "q floor 1 2 3".
	Cmd string `json:"cmd"`
}

type ttResult struct {
	// Replies are the reply objects as the server sent them, in order: one, or two for quiet and place (read 1, then read 2).
	Replies []json.RawMessage `json:"replies"`
}

const (
	// ttMaxLen is the longest command taken: it must fit one chat line with room for the prefix and the id.
	ttMaxLen = 200
	// ttWait is how long the raw method waits for the final reply: longer than the client's default 5 s, so that `quiet` with its
	// `w=` seconds and `place`'s verified second read (about 4 s) fit.
	ttWait = 30 * time.Second
)

func registerElunaMethods(srv *rpc.Server, cur func() *session.Session) {
	srv.Register("tt", rpc.Typed(func(ctx context.Context, p ttParams) (ttResult, error) {
		cmd := strings.TrimSpace(p.Cmd)
		switch {
		case cmd == "":
			return ttResult{}, &rpc.Error{Code: rpc.CodeInvalidParams, Message: "tt: cmd is empty"}
		case strings.ContainsAny(cmd, "\r\n#"):
			return ttResult{}, &rpc.Error{Code: rpc.CodeInvalidParams, Message: "tt: cmd must be one line and carry no # (the agent adds the request id)"}
		case len(cmd) > ttMaxLen:
			return ttResult{}, &rpc.Error{Code: rpc.CodeInvalidParams, Message: fmt.Sprintf("tt: cmd is longer than %d characters", ttMaxLen)}
		}
		c := &eluna.Client{
			Send:    func(ctx context.Context, line string) error { return cur().GM(ctx, line) },
			Log:     cur().Log(),
			Timeout: ttWait,
		}
		rs, err := c.Do(ctx, cmd, 0)
		out := ttResult{Replies: []json.RawMessage{}}
		for _, r := range rs {
			if b, err := json.Marshal(r.Body); err == nil {
				out.Replies = append(out.Replies, b)
			}
		}
		if err == nil {
			return out, nil
		}
		var e *eluna.Error
		switch {
		case errors.As(err, &e):
			// The toolkit refused the command: its code rides along, the replies (the refusing one) too.
			return out, &rpc.Error{Code: rpc.CodeGame, Message: err.Error(), Data: map[string]any{"err": e.Code, "replies": out.Replies}}
		case errors.Is(err, eluna.ErrTimeout):
			return out, &rpc.Error{Code: rpc.CodeTimeout, Message: err.Error(), Data: map[string]any{"replies": out.Replies}}
		}
		return out, rpcErr(err)
	}))
}
