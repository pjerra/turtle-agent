package main

import (
	"context"
	"strings"
	"testing"

	"github.com/pjerra/turtle-agent/internal/dungeon"
	"github.com/pjerra/turtle-agent/internal/rpc"
)

// ctlDriver is a RemoteDriver as the control methods see it: Pause and RunControl on every remote run (as the real one has both),
// Observing telling the two modes apart.
type ctlDriver struct {
	observe bool
	paused  bool
	verbs   []string
}

func (d *ctlDriver) Run(context.Context, int) error { return nil }
func (d *ctlDriver) Report() dungeon.Report         { r, _ := d.Status(); return r }
func (d *ctlDriver) Status() (dungeon.Report, bool) { return dungeon.Report{State: "running"}, false }
func (d *ctlDriver) Pause()                         { d.paused = true }
func (d *ctlDriver) Observing() bool                { return d.observe }
func (d *ctlDriver) RunControl(verb string) error {
	if !d.observe {
		return &rpc.Error{Code: rpc.CodeInvalidParams, Message: "the run is not an observed one (mode observe)"}
	}
	d.verbs = append(d.verbs, verb)
	return nil
}

// dungeon_pause, dungeon_resume and dungeon_skip act on the active run by its mode: an observed run passes the verb to the run leader, a
// commander run pauses with its binding kept, resume and skip are refused for it, and with no run going only the pause is a no-op.
func TestDungeonControlByRunMode(t *testing.T) {
	running := func(d *ctlDriver) *dungeonRunner {
		return &dungeonRunner{drv: d, done: make(chan struct{})}
	}
	msg := func(err error) string {
		if err == nil {
			return ""
		}
		return err.Error()
	}

	obs := &ctlDriver{observe: true}
	r := running(obs)
	for _, v := range []string{"pause", "resume", "skip"} {
		if _, err := r.control(v); err != nil {
			t.Fatalf("observed %s: %v", v, err)
		}
	}
	if strings.Join(obs.verbs, ",") != "pause,resume,skip" || obs.paused {
		t.Errorf("observed run: verbs %v, commander pause %v (the verbs go to the run leader, the commander pause stays out)", obs.verbs, obs.paused)
	}

	cmd := &ctlDriver{}
	r = running(cmd)
	done := r.done
	r.cancel = func() { close(done) } // the run ends when pause cancels it
	for _, v := range []string{"resume", "skip"} {
		_, err := r.control(v)
		if e, ok := err.(*rpc.Error); !ok || e.Code != rpc.CodeInvalidParams || !strings.Contains(e.Message, "mode observe") || len(cmd.verbs) != 0 {
			t.Errorf("commander %s: %v (verbs %v)", v, err, cmd.verbs)
		}
	}
	if _, err := r.control("pause"); err != nil || !cmd.paused {
		t.Errorf("commander pause: %v, paused %v", err, cmd.paused)
	}

	idle := &dungeonRunner{}
	if _, err := idle.control("pause"); err != nil {
		t.Errorf("pause with no run: %v", err)
	}
	for _, v := range []string{"resume", "skip"} {
		if _, err := idle.control(v); !strings.Contains(msg(err), "no dungeon run is going") {
			t.Errorf("%s with no run: %v", v, err)
		}
	}
}
