//go:build linux

package routeedit

import (
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"syscall"
)

// The editable source (--edit-source) is held the way the store holds the edits folder: opened once (O_DIRECTORY|O_NOFOLLOW) when the
// editor starts, and a plan is read with openat(source, "<plan>.json", O_NOFOLLOW) and checked with fstat. No pathname is resolved again
// when a plan is read, so a folder on the way swapped for a link, or a plan file swapped for one, cannot send the read outside the folder.
// The folder checks (CheckHeld) compare the identities (device and inode) of the held descriptors and of the folders above them, walked by
// "..", not path strings.

// Source is the folder the edits start from, held open.
type Source struct {
	path string // real path at open, for messages and the record only
	fd   int
	id   fileID
}

type fileID struct{ dev, ino uint64 }

func fdID(fd int) (fileID, error) {
	var st syscall.Stat_t
	if err := syscall.Fstat(fd, &st); err != nil {
		return fileID{}, err
	}
	return fileID{uint64(st.Dev), st.Ino}, nil //nolint:unconvert // Dev is not uint64 on every arch
}

// OpenSource opens the editable source and holds it.
func OpenSource(path string) (*Source, error) {
	abs, err := filepath.Abs(path)
	if err != nil {
		return nil, err
	}
	real, err := filepath.EvalSymlinks(abs)
	if err != nil {
		return nil, fmt.Errorf("editable source: %w", err)
	}
	fd, err := syscall.Open(real, openFlags, 0)
	if err != nil {
		if errors.Is(err, syscall.ENOTDIR) || errors.Is(err, syscall.ELOOP) {
			return nil, fmt.Errorf("%s is not a folder", path)
		}
		return nil, &fs.PathError{Op: "open", Path: real, Err: err}
	}
	id, err := fdID(fd)
	if err != nil {
		syscall.Close(fd)
		return nil, err
	}
	return &Source{path: real, fd: fd, id: id}, nil
}

// Path is the folder's real path when it was opened.
func (s *Source) Path() string { return s.path }

// Close lets go of the folder.
func (s *Source) Close() { syscall.Close(s.fd) }

// ReadPlan reads <plan>.json of the held folder: never through a link, only a regular file, at most max bytes.
func (s *Source) ReadPlan(plan string, max int64) ([]byte, error) {
	if !PlanName.MatchString(plan) {
		return nil, fmt.Errorf("%q is not a plan name", plan)
	}
	name := plan + ".json"
	// O_NONBLOCK: a FIFO put in the file's place opens at once and is then refused by the fstat below instead of hanging the request
	fd, err := syscall.Openat(s.fd, name, syscall.O_RDONLY|syscall.O_NOFOLLOW|syscall.O_NONBLOCK|syscall.O_CLOEXEC, 0)
	if err != nil {
		if errors.Is(err, syscall.ELOOP) {
			return nil, fmt.Errorf("%s is a link: refused", name)
		}
		return nil, &fs.PathError{Op: "open", Path: name, Err: err}
	}
	f := os.NewFile(uintptr(fd), name)
	defer f.Close()
	st, err := f.Stat() // fstat of the opened file
	if err != nil {
		return nil, err
	}
	if !st.Mode().IsRegular() {
		return nil, fmt.Errorf("%s is not a regular file: refused", name)
	}
	if st.Size() > max {
		return nil, fmt.Errorf("%s is larger than %d bytes", name, max)
	}
	data, err := io.ReadAll(io.LimitReader(f, max+1))
	if err != nil {
		return nil, err
	}
	if int64(len(data)) > max {
		return nil, fmt.Errorf("%s is larger than %d bytes", name, max)
	}
	return data, nil
}

// Verify fails when the source's path no longer names the folder that is held: it was moved, or a folder on the way was swapped (for a
// link, or another folder). The editor stops on that rather than go on with a folder that is not the one its path says.
func (s *Source) Verify() error { return samePath(s.path, s.id, "editable source") }

// Verify fails when the store's path no longer names the folder that is held.
func (s *Store) Verify() error {
	id, err := fdID(s.rd.fd)
	if err != nil {
		return err
	}
	return samePath(s.root, id, "edits folder")
}

func samePath(path string, id fileID, what string) error {
	var st syscall.Stat_t
	if err := syscall.Stat(path, &st); err != nil || (fileID{uint64(st.Dev), st.Ino}) != id { //nolint:unconvert
		return fmt.Errorf("the %s %s is no longer the folder the editor opened (moved, or a folder on the way replaced): refused, restart the editor", what, path)
	}
	return nil
}

// maxDepth bounds the walk up a folder tree.
const maxDepth = 4096

// above lists the identities of the folder fd and of every folder above it up to /, walked by ".." from the descriptor (no path is resolved).
func above(fd int) (map[fileID]bool, error) {
	out := map[fileID]bool{}
	cur, err := syscall.Openat(fd, ".", openFlags, 0)
	if err != nil {
		return nil, err
	}
	for i := 0; i < maxDepth; i++ {
		id, err := fdID(cur)
		if err != nil {
			syscall.Close(cur)
			return nil, err
		}
		if out[id] { // / is its own parent
			syscall.Close(cur)
			return out, nil
		}
		out[id] = true
		up, err := syscall.Openat(cur, "..", openFlags, 0)
		syscall.Close(cur)
		if err != nil {
			return nil, err
		}
		cur = up
	}
	syscall.Close(cur)
	return nil, errors.New("the folder tree is too deep")
}

// openSlot opens a slot's plan folder by its configured path (the agent follows links in it too). When it is not there, the nearest folder
// above it that is, with exists false.
func openSlot(path string) (fd int, exists bool, err error) {
	p := filepath.Clean(path)
	for exists = true; ; exists = false {
		fd, err = syscall.Open(p, syscall.O_RDONLY|syscall.O_DIRECTORY|syscall.O_CLOEXEC, 0)
		if err == nil {
			return fd, exists, nil
		}
		if !errors.Is(err, syscall.ENOENT) {
			return -1, false, &fs.PathError{Op: "open", Path: p, Err: err}
		}
		if filepath.Dir(p) == p {
			return -1, false, &fs.PathError{Op: "open", Path: path, Err: err}
		}
		p = filepath.Dir(p)
	}
}

// CheckHeld is CheckFolders on the held folders: the edits folder and the source by the identities of their descriptors, each slot's plan
// folder by that of the folder its path names now (or, when it is not made yet, of the nearest folder above it). It first checks that the
// paths of the held folders still name them. Any folder it cannot open or walk is a refusal.
func CheckHeld(store *Store, src *Source, slotDirs []string) error {
	if err := store.Verify(); err != nil {
		return err
	}
	if err := src.Verify(); err != nil {
		return err
	}
	edID, err := fdID(store.rd.fd)
	if err != nil {
		return err
	}
	edUp, err := above(store.rd.fd)
	if err != nil {
		return fmt.Errorf("the edits folder cannot be walked: %w", err)
	}
	srcUp, err := above(src.fd)
	if err != nil {
		return fmt.Errorf("the editable source cannot be walked: %w", err)
	}
	for _, s := range slotDirs {
		if s == "" {
			continue
		}
		fd, exists, err := openSlot(s)
		if err != nil {
			return fmt.Errorf("the plan folder %s of a slot cannot be opened: %w", s, err)
		}
		sdID, err := fdID(fd)
		var sdUp map[fileID]bool
		if err == nil {
			sdUp, err = above(fd)
		}
		syscall.Close(fd)
		if err != nil {
			return fmt.Errorf("the plan folder %s of a slot cannot be walked: %w", s, err)
		}
		switch {
		case exists && edUp[sdID]:
			return fmt.Errorf("the edits folder %s lies inside the plan folder %s of a slot: a running commander would take what is written there", store.root, s)
		case sdUp[edID]:
			return fmt.Errorf("the plan folder %s of a slot lies inside the edits folder %s", s, store.root)
		case exists && srcUp[sdID]:
			return fmt.Errorf("the editable source %s lies inside the plan folder %s of a slot", src.path, s)
		}
	}
	return nil
}
