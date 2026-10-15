package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/pjerra/turtle-agent/internal/dungeon"
	"github.com/pjerra/turtle-agent/internal/rpc"
	"github.com/pjerra/turtle-agent/internal/trace"
)

// tuner is the part of a dungeon driver that tunes live (dungeon.RemoteDriver).
type tuner interface {
	TuneSet(key string, value any) (dungeon.TuneAck, error)
	TuneList() []dungeon.TuneInfo
}

type tuneSetParams struct {
	Key string `json:"key"`
	// Value is a number, or "default" to put the key back to the tunables file's value (or the driver's own).
	Value json.RawMessage `json:"value"`
}

type tuneListResult struct {
	Tunables []dungeon.TuneInfo `json:"tunables"`
}

// currentTuner is the running remote driver, or an error that says why there is none.
func (r *dungeonRunner) currentTuner() (tuner, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.drv == nil || !r.running() {
		return nil, &rpc.Error{Code: rpc.CodeGame, Message: "no dungeon run is going"}
	}
	t, ok := r.drv.(tuner)
	if !ok {
		return nil, &rpc.Error{Code: rpc.CodeGame, Message: `live tuning is for runs in mode "remote"`}
	}
	return t, nil
}

// registerTuneMethods wires tune_set and tune_list: a one-off change of a tunable from outside the run, with the limits and the events of
// a tunables file, taken at the next leg start or pull-group start (README "Live tuning").
func registerTuneMethods(srv *rpc.Server, r *dungeonRunner) {
	srv.Register("tune_set", rpc.Typed(func(ctx context.Context, p tuneSetParams) (dungeon.TuneAck, error) {
		t, err := r.currentTuner()
		if err != nil {
			return dungeon.TuneAck{}, err
		}
		var v any
		if len(p.Value) == 0 || json.Unmarshal(p.Value, &v) != nil {
			return dungeon.TuneAck{}, &rpc.Error{Code: rpc.CodeInvalidParams, Message: "value must be a number or \"default\""}
		}
		ack, err := t.TuneSet(p.Key, v)
		var te *dungeon.TuneError
		if errors.As(err, &te) {
			return ack, &rpc.Error{Code: rpc.CodeInvalidParams, Message: te.Msg}
		}
		return ack, err
	}))
	srv.Register("tune_list", rpc.Typed(func(ctx context.Context, _ empty) (tuneListResult, error) {
		t, err := r.currentTuner()
		if err != nil {
			return tuneListResult{}, err
		}
		return tuneListResult{Tunables: t.TuneList()}, nil
	}))
}

// tuneFile is the tunables file of a remote run: the run's own tune parameter (which must exist), else the login's --tune, else
// <plan>.tune.json beside the plan file when the run reloads (absent is fine; the driver looks again at each boundary).
func (r *dungeonRunner) tuneFile(p dungeonRunParams, planPath string) (string, error) {
	path := p.Tune
	if path == "" {
		path = r.tunePath
	}
	if path == "" {
		return "", nil // the driver's default: beside the plan
	}
	if strings.HasPrefix(path, "~/") {
		if home, err := os.UserHomeDir(); err == nil {
			path = filepath.Join(home, path[2:])
		}
	}
	// A file the call names must exist and be good: a typo is an error here, not a note in the middle of the run. The login's --tune may
	// be created later, but when it is there it is checked as well.
	data, err := os.ReadFile(path)
	switch {
	case err != nil && p.Tune != "":
		return "", &rpc.Error{Code: rpc.CodeInvalidParams, Message: "tune: " + err.Error()}
	case err == nil:
		if verr := dungeon.ValidateTuneFile(data); verr != nil {
			return "", &rpc.Error{Code: rpc.CodeInvalidParams, Message: fmt.Sprintf("tune: %s: %v", path, verr)}
		}
	}
	return path, nil
}

const tuneDiffUsage = "usage: turtle-agent tune-diff <trace.jsonl>\n" +
	"  prints what the live tuning of a run changed as JSON patches for the tunables file and the plan file (README \"Live tuning\")"

// tuneDiffCmd reads a run trace and prints its tune_change and plan_reload events as a JSON patch for the tunables file and one for the
// plan file, so that a fix made during a run can be made in the files afterwards. It reads nothing but the trace.
func tuneDiffCmd(args []string, stdout, stderr io.Writer) int {
	if len(args) != 1 || strings.HasPrefix(args[0], "-") {
		fmt.Fprintln(stderr, tuneDiffUsage)
		return exitUsage
	}
	f, err := trace.Read(args[0])
	if err != nil {
		fmt.Fprintln(stderr, "tune-diff:", err)
		return exitUsage
	}
	enc := json.NewEncoder(stdout)
	enc.SetIndent("", "  ")
	if err := enc.Encode(dungeon.TuneDiff(f)); err != nil {
		fmt.Fprintln(stderr, "tune-diff:", err)
		return exitUsage
	}
	return exitOK
}
