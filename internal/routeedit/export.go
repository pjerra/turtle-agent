package routeedit

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"syscall"

	"github.com/pjerra/turtle-agent/internal/dungeon"
)

// Pick reads a version of a plan for use: version 0 is the newest one that is checked (or accepted). A refused version has no plan file and is
// never picked. The plan file is read back, must be the file the record's sha256 names, and must load as the plan called plan.
func Pick(s *Store, plan string, version int) (*Meta, []byte, error) {
	if !PlanName.MatchString(plan) {
		return nil, nil, fmt.Errorf("%q is not a plan name", plan)
	}
	if version == 0 {
		v, err := s.Latest(plan)
		if err != nil {
			return nil, nil, err
		}
		if v == 0 {
			return nil, nil, fmt.Errorf("%s has no checked version in %s", plan, s.Root())
		}
		version = v
	}
	m, err := s.Meta(plan, version)
	if err != nil {
		return nil, nil, fmt.Errorf("%s %s: no such version", plan, Name(version))
	}
	if m.Status == StatusRefused {
		return nil, nil, fmt.Errorf("%s %s was refused by the plan check (%s): there is no plan file to use", plan, Name(version), m.Check.Refusal())
	}
	data, err := s.PlanFile(plan, version)
	if err != nil {
		return nil, nil, fmt.Errorf("%s %s: %w", plan, Name(version), err)
	}
	if sum := sha256.Sum256(data); hex.EncodeToString(sum[:]) != m.SHA256 {
		return nil, nil, fmt.Errorf("%s %s: the plan file is not the one its record names (sha256 differs): it was changed after it was written", plan, Name(version))
	}
	p, err := dungeon.Parse(plan+".json", data)
	if err != nil {
		return nil, nil, fmt.Errorf("%s %s does not load: %w", plan, Name(version), err)
	}
	if p.Name != plan {
		return nil, nil, fmt.Errorf("%s %s is a plan called %q", plan, Name(version), p.Name)
	}
	return m, data, nil
}

// Exported is the result of Export.
type Exported struct {
	Plan    string
	Version int
	Name    string // v0007
	SHA256  string
	Path    string // the file written
	Meta    *Meta
}

// Tag is the run tag: `trace_meta.route_edit` of dungeon_run, and the line the export prints.
func (e Exported) Tag() string { return fmt.Sprintf("%s@%s sha256=%s", e.Plan, e.Name, e.SHA256) }

// Export copies a version into the fresh folder dir as <plan>.json, for a slot's dungeons_dir. dir must not exist or must be empty, the file is
// created new (never over another), and guard (may be nil) is asked about the real path of dir first: it refuses a folder a running commander
// reads.
func Export(s *Store, plan string, version int, dir string, guard func(realDir string) error) (*Exported, error) {
	m, data, err := Pick(s, plan, version)
	if err != nil {
		return nil, err
	}
	real := realPath(dir)
	if guard != nil {
		if err := guard(real); err != nil {
			return nil, err
		}
	}
	st, err := os.Lstat(real)
	switch {
	case err == nil && !st.IsDir():
		return nil, fmt.Errorf("%s exists and is not a folder", real)
	case err == nil:
		ents, err := os.ReadDir(real)
		if err != nil {
			return nil, err
		}
		if len(ents) > 0 {
			return nil, fmt.Errorf("%s is not empty: export goes into a fresh folder, so that nothing a run reads is overwritten", real)
		}
	case errors.Is(err, os.ErrNotExist):
		if err := os.MkdirAll(real, 0o755); err != nil {
			return nil, err
		}
	default:
		return nil, err
	}
	path := filepath.Join(real, plan+".json")
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL|syscall.O_NOFOLLOW, 0o644)
	if err != nil {
		return nil, err
	}
	if _, err := f.Write(data); err != nil {
		f.Close()
		os.Remove(path)
		return nil, err
	}
	if err := f.Close(); err != nil {
		os.Remove(path)
		return nil, err
	}
	return &Exported{Plan: plan, Version: m.Version, Name: Name(m.Version), SHA256: m.SHA256, Path: path, Meta: m}, nil
}

// Promoted is the result of Promote.
type Promoted struct {
	Path    string
	Message string
	Meta    *Meta
}

// Promote writes a version over dungeons/<plan>.json of a TA worktree, for a PR. The worktree's file must still be the one the edit started from
// (its sha256 is the record's base): else the plan changed meanwhile and the edit is made again on the new base. The write is a new file renamed
// over the old one; the plan file must be a regular file inside a folder that is not a link.
func Promote(s *Store, plan string, version int, worktree string) (*Promoted, error) {
	if version == 0 {
		return nil, errors.New("promote needs a version (vN): a PR carries a version someone chose")
	}
	m, data, err := Pick(s, plan, version)
	if err != nil {
		return nil, err
	}
	wt, err := filepath.EvalSymlinks(worktree)
	if err != nil {
		return nil, fmt.Errorf("worktree: %w", err)
	}
	if _, err := os.Lstat(filepath.Join(wt, ".git")); err != nil {
		return nil, fmt.Errorf("%s is not a git worktree (no .git)", wt)
	}
	dir := filepath.Join(wt, "dungeons")
	if st, err := os.Lstat(dir); err != nil || !st.IsDir() {
		return nil, fmt.Errorf("%s has no dungeons folder", wt)
	}
	path := filepath.Join(dir, plan+".json")
	if st, err := os.Lstat(path); err != nil || !st.Mode().IsRegular() {
		return nil, fmt.Errorf("%s is not a plan file of the worktree", path)
	}
	cur, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	if sum := sha256.Sum256(cur); hex.EncodeToString(sum[:]) != m.Base.SHA256 {
		return nil, fmt.Errorf("the worktree's %s (sha256 %s) is not the file this edit started from (%s): the plan changed meanwhile, so edit it again on the new base", plan+".json", hex.EncodeToString(sum[:])[:8], short(m.Base.SHA256))
	}
	tmp := filepath.Join(dir, "."+plan+".json.route-edit")
	f, err := os.OpenFile(tmp, os.O_WRONLY|os.O_CREATE|os.O_EXCL|syscall.O_NOFOLLOW, 0o644)
	if err != nil {
		return nil, err
	}
	if _, err := f.Write(data); err != nil {
		f.Close()
		os.Remove(tmp)
		return nil, err
	}
	if err := f.Close(); err != nil {
		os.Remove(tmp)
		return nil, err
	}
	if err := os.Rename(tmp, path); err != nil {
		os.Remove(tmp)
		return nil, err
	}
	return &Promoted{Path: path, Message: CommitMessage(m), Meta: m}, nil
}

func short(s string) string {
	if len(s) > 8 {
		return s[:8]
	}
	return s
}

// CommitMessage is the commit message for a promoted version: who, when, what changed, and what it was checked on.
func CommitMessage(m *Meta) string {
	var b strings.Builder
	fmt.Fprintf(&b, "dungeons: %s route edit %s (%s, %s)\n\n", m.Plan, Name(m.Version), m.Who, m.At)
	fmt.Fprintf(&b, "Edited on the dashboard route editor and checked with the plan check on the navmesh in %s", m.Mmaps)
	if m.Check.Checked {
		fmt.Fprintf(&b, " (%d points, %d new issues).\n", m.Check.Points, m.Check.NewIssues)
	} else {
		b.WriteString(".\n")
	}
	if m.Note != "" {
		fmt.Fprintf(&b, "Note: %s\n", m.Note)
	}
	b.WriteString("\n")
	for _, c := range m.Changes {
		fmt.Fprintf(&b, "- leg %d", c.Leg+1)
		if c.LegName != "" {
			fmt.Fprintf(&b, " (%s)", c.LegName)
		}
		fmt.Fprintf(&b, ": %s %s", c.Part, map[string]string{KindMove: "moved", KindInsert: "added", KindDelete: "removed"}[c.Op])
		if c.Op == KindMove && c.Moved > 0 {
			fmt.Fprintf(&b, " %.1f yd", c.Moved)
		}
		if c.Picked {
			fmt.Fprintf(&b, " (floor picked, %d floors in range)", c.Floors)
		}
		b.WriteString("\n")
	}
	fmt.Fprintf(&b, "\nBase %s sha256 %s", m.Base.Source, m.Base.SHA256)
	if m.Base.GitSHA != "" {
		fmt.Fprintf(&b, " (git %s)", m.Base.GitSHA)
	}
	fmt.Fprintf(&b, "\nEdit %s sha256 %s\n", Name(m.Version), m.SHA256)
	return b.String()
}
