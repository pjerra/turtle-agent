// raid_walk_decisions.go: the walk's decision-capture pull (spec section 10.2). After the
// teardown step and before finish, on every exit path that reached the dashboard login, the walk
// lists the dashboard's decision captures for its character in the walk window, waits for each to
// show ended, downloads index.json and every non-deleted segment into <out>/decisions/<id>/, and
// writes <out>/decisions/manifest.json. A 5-minute context bounds the pull. It never changes the
// exit code: every failure lands in the summary's decisions note.
//
// pullDecisionsInto is the shared worker: the walk calls it through pullDecisionsIn, and
// `turtle-agent decisions pull` calls it directly, so both pull the same files.
package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"time"

	"github.com/pjerra/turtle-agent/internal/raidwalk"
	"github.com/pjerra/turtle-agent/internal/tbdash"
)

// decisionsBudgetDefaultMiB is --decisions-max-mib's default: the pull stops with a note past it.
const decisionsBudgetDefaultMiB = 1024

// walkDecisionsPollEvery is the ended wait's poll step; walkDecisionsWaitMax bounds it.
const (
	walkDecisionsPollEvery = 3 * time.Second
	walkDecisionsWaitMax   = 15 * time.Second
)

// decisionManifestFile is one file row of <out>/decisions/manifest.json.
type decisionManifestFile struct {
	Name   string `json:"name"`
	Bytes  int64  `json:"bytes"`
	SHA256 string `json:"sha256"`
}

// decisionManifestCapture is one capture row of the manifest.
type decisionManifestCapture struct {
	ID    string                 `json:"id"`
	Files []decisionManifestFile `json:"files"`
}

// decisionsPuller is what pullDecisionsInto reads and streams from: the walk's walkDash and the
// standalone command's *tbdash.Client both satisfy it.
type decisionsPuller interface {
	Login(ctx context.Context) error
	Decisions(ctx context.Context, commander string, since, until time.Time) ([]tbdash.DecisionCapture, error)
	DecisionFile(ctx context.Context, id, file string, dst io.Writer, max int64) (int64, error)
}

// pullDecisions pulls the walk's decision captures into <out>/decisions. Call it once after the
// teardown step and before finish, on every exit path that reached the dashboard login, with its
// own 5-minute context. w.dash == nil (login never reached) records "off (no dashboard)". The
// summary's Decisions field carries the pull: a note plus one row per pulled capture.
func (w *walk) pullDecisions(ctx context.Context) {
	pull, cancel := context.WithTimeout(ctx, 5*time.Minute)
	defer cancel()
	w.pullDecisionsIn(pull)
}

// pullDecisionsIn is pullDecisions with the caller's context (tests hand one in directly).
func (w *walk) pullDecisionsIn(ctx context.Context) {
	if w.dash == nil {
		w.decisions = &raidwalk.DecisionsSummary{Note: "off (no dashboard)", Captures: []raidwalk.DecisionsCaptureSummary{}}
		return
	}
	if !w.opts.decisions {
		w.decisions = &raidwalk.DecisionsSummary{Note: "off", Captures: []raidwalk.DecisionsCaptureSummary{}}
		return
	}
	end := w.ended
	if end.IsZero() {
		end = w.env.now()
	}
	budget := int64(w.opts.decisionsMaxMiB) * 1024 * 1024
	budgetMiB := w.opts.decisionsMaxMiB
	if budgetMiB <= 0 {
		budgetMiB = decisionsBudgetDefaultMiB
		budget = int64(budgetMiB) * 1024 * 1024
	}
	res, notes := pullDecisionsInto(ctx, w.dash, w.manifest.Character, w.started.Add(-10*time.Second), end, w.outDir, budget)
	for _, n := range notes {
		w.note("decisions: %s", n)
	}
	if res.BudgetHit {
		if len(res.Captures) == 0 {
			res.Note = fmt.Sprintf("budget stopped the pull at %d MiB: nothing pulled", budgetMiB)
		} else {
			res.Note = fmt.Sprintf("budget stopped the pull at %d MiB: pulled %d captures (%d bytes)", budgetMiB, len(res.Captures), res.Bytes)
		}
	} else {
		res.Note = fmt.Sprintf("pulled %d captures (%d bytes)", len(res.Captures), res.Bytes)
	}
	w.decisions = &raidwalk.DecisionsSummary{Note: res.Note, Captures: res.Captures, Bytes: res.Bytes}
}

// decisionsPullResult is pullDecisionsInto's summary: one row per fully pulled capture.
type decisionsPullResult struct {
	Captures  []raidwalk.DecisionsCaptureSummary
	Bytes     int64
	Note      string
	BudgetHit bool
}

// pullDecisionsInto lists the commander's captures in [since, until], waits up to 15s polling
// every 3s for each to show ended, downloads index.json and every non-deleted segment into
// <out>/decisions/<id>/ (0700 dirs, 0600 files via .part + rename), and writes
// <out>/decisions/manifest.json. It stops at maxBytes with BudgetHit; a failed file notes and the
// rest continue. A server-sent id that fails the capture regex is refused client-side.
func pullDecisionsInto(ctx context.Context, dash decisionsPuller, commander string, since, until time.Time, out string, maxBytes int64) (decisionsPullResult, []string) {
	var res decisionsPullResult
	res.Captures = []raidwalk.DecisionsCaptureSummary{}
	var notes []string
	caps, err := dash.Decisions(ctx, commander, since, until)
	if err != nil {
		return res, []string{err.Error()}
	}
	var manifest []decisionManifestCapture
	var total int64
	for _, c := range caps {
		if ctx.Err() != nil {
			break
		}
		if !tbdash.DecisionCaptureRe.MatchString(c.ID) {
			continue
		}
		if c.Ended == nil {
			c.Ended = waitDecisionEnded(ctx, dash, commander, since, until, c)
			if c.Ended == nil {
				continue
			}
		}
		files := []string{"index.json"}
		for _, s := range c.Segments {
			if s.Deleted {
				continue
			}
			files = append(files, s.File)
		}
		var rows []decisionManifestFile
		var capBytes int64
		ok := true
		for _, f := range files {
			if !tbdash.DecisionFileRe.MatchString(f) {
				continue
			}
			if total >= maxBytes {
				res.BudgetHit = true
				ok = false
				break
			}
			n, hash, ferr := pullOneDecisionFile(ctx, dash, out, c.ID, f, maxBytes-total)
			if ferr != nil {
				if isDecisionBudgetError(ferr) {
					res.BudgetHit = true
					ok = false
					break
				}
				notes = append(notes, fmt.Sprintf("%s/%s: %v", c.ID, f, ferr))
				ok = false
				break
			}
			total += n
			capBytes += n
			rows = append(rows, decisionManifestFile{Name: f, Bytes: n, SHA256: hash})
		}
		if !ok {
			_ = os.RemoveAll(filepath.Join(out, "decisions", c.ID))
			if res.BudgetHit {
				break
			}
			continue
		}
		if rows == nil {
			rows = []decisionManifestFile{}
		}
		manifest = append(manifest, decisionManifestCapture{ID: c.ID, Files: rows})
		res.Captures = append(res.Captures, raidwalk.DecisionsCaptureSummary{ID: c.ID, Files: len(rows), Bytes: capBytes})
		res.Bytes += capBytes
	}
	if manifest == nil {
		manifest = []decisionManifestCapture{}
	}
	for i := range manifest {
		if manifest[i].Files == nil {
			manifest[i].Files = []decisionManifestFile{}
		}
	}
	if err := writeDecisionManifest(out, manifest); err != nil {
		notes = append(notes, fmt.Sprintf("manifest: %v", err))
	}
	return res, notes
}

// waitDecisionEnded polls up to 15s, every 3s, for one capture to show ended. Nil means it never
// did (the walk skips it) or the context ended.
func waitDecisionEnded(ctx context.Context, dash decisionsPuller, commander string, since, until time.Time, c tbdash.DecisionCapture) *time.Time {
	deadline := time.Now().Add(walkDecisionsWaitMax)
	for {
		if c.Ended != nil {
			return c.Ended
		}
		if time.Now().After(deadline) {
			return nil
		}
		caps, err := dash.Decisions(ctx, commander, since, until)
		if err != nil {
			return nil
		}
		found := false
		for _, n := range caps {
			if n.ID == c.ID {
				found = true
				if n.Ended != nil {
					return n.Ended
				}
				break
			}
		}
		if !found {
			return nil
		}
		timer := time.NewTimer(walkDecisionsPollEvery)
		select {
		case <-ctx.Done():
			timer.Stop()
			return nil
		case <-timer.C:
		}
	}
}

// pullOneDecisionFile streams one capture file into <out>/decisions/<id>/ via <file>.part +
// rename (0700 dirs, 0600 files) and returns the bytes and sha256 hex. max bounds this file.
func pullOneDecisionFile(ctx context.Context, dash decisionsPuller, out, id, file string, max int64) (int64, string, error) {
	dir := filepath.Join(out, "decisions", id)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return 0, "", err
	}
	part := filepath.Join(dir, file+".part")
	dst, err := os.OpenFile(part, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o600)
	if err != nil {
		return 0, "", err
	}
	h := sha256.New()
	n, derr := dash.DecisionFile(ctx, id, file, &hashWriter{dst: dst, h: h}, max)
	cerr := dst.Close()
	if derr != nil {
		_ = os.Remove(part)
		return n, "", derr
	}
	if cerr != nil {
		_ = os.Remove(part)
		return 0, "", cerr
	}
	if err := os.Chmod(part, 0o600); err != nil {
		_ = os.Remove(part)
		return 0, "", err
	}
	if err := os.Rename(part, filepath.Join(dir, file)); err != nil {
		_ = os.Remove(part)
		return 0, "", err
	}
	return n, hex.EncodeToString(h.Sum(nil)), nil
}

// writeDecisionManifest writes <out>/decisions/manifest.json (always a list).
func writeDecisionManifest(out string, manifest []decisionManifestCapture) error {
	dir := filepath.Join(out, "decisions")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	raw, err := json.MarshalIndent(manifest, "", "  ")
	if err != nil {
		return err
	}
	part := filepath.Join(dir, "manifest.json.part")
	if err := os.WriteFile(part, append(raw, '\n'), 0o600); err != nil {
		return err
	}
	return os.Rename(part, filepath.Join(dir, "manifest.json"))
}

func isDecisionBudgetError(err error) bool {
	if err == nil {
		return false
	}
	s := err.Error()
	for _, sub := range []string{"over the", "budget", "limit"} {
	outer:
		for i := 0; i+len(sub) <= len(s); i++ {
			for j := 0; j < len(sub); j++ {
				if s[i+j] != sub[j] {
					continue outer
				}
			}
			return true
		}
	}
	return false
}

// hashWriter tees writes into a sha256 hash.
type hashWriter struct {
	dst *os.File
	h   interface {
		Write([]byte) (int, error)
	}
}

func (hw *hashWriter) Write(b []byte) (int, error) {
	n, err := hw.dst.Write(b)
	if n > 0 {
		_, _ = hw.h.Write(b[:n])
	}
	return n, err
}
