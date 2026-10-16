//go:build linux

package routeedit

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"syscall"
)

// The store works on directory file descriptors: the edits folder is opened once (O_DIRECTORY|O_NOFOLLOW), each plan folder is opened
// relative to it, and every file is made or read relative to the plan folder's descriptor, each step with O_NOFOLLOW. A folder swapped for a
// link after it was opened cannot redirect a write, and a link put in a folder's place is refused when it is opened, not checked beforehand.

const openFlags = syscall.O_RDONLY | syscall.O_DIRECTORY | syscall.O_NOFOLLOW | syscall.O_CLOEXEC

type rootDir struct{ fd int }

// openRoot opens the edits folder; real must already be a path without links in its last component.
func openRoot(real string) (*rootDir, error) {
	fd, err := syscall.Open(real, openFlags, 0)
	if err != nil {
		return nil, &fs.PathError{Op: "open", Path: real, Err: err}
	}
	return &rootDir{fd}, nil
}

func (r *rootDir) close() { syscall.Close(r.fd) }

type planDir struct{ fd int }

func (p *planDir) close() { syscall.Close(p.fd) }

// openPlanDir opens the folder of a plan under the root, making it when create is set. A link or a file in its place is refused.
func (r *rootDir) openPlanDir(plan string, create bool) (*planDir, error) {
	fd, err := syscall.Openat(r.fd, plan, openFlags, 0)
	if errors.Is(err, syscall.ENOENT) && create {
		if merr := syscall.Mkdirat(r.fd, plan, 0o750); merr != nil && !errors.Is(merr, syscall.EEXIST) {
			return nil, &fs.PathError{Op: "mkdir", Path: plan, Err: merr}
		}
		fd, err = syscall.Openat(r.fd, plan, openFlags, 0)
	}
	switch {
	case err == nil:
		return &planDir{fd}, nil
	case errors.Is(err, syscall.ELOOP), errors.Is(err, syscall.ENOTDIR):
		return nil, fmt.Errorf("%s is not a folder (a link or a file): refused", plan)
	}
	return nil, &fs.PathError{Op: "open", Path: plan, Err: err}
}

// planNames lists the folders (not links) of the root.
func (r *rootDir) planNames() ([]string, error) {
	ents, err := readDirAt(r.fd)
	if err != nil {
		return nil, err
	}
	var out []string
	for _, e := range ents {
		if e.IsDir() {
			out = append(out, e.Name())
		}
	}
	return out, nil
}

func readDirAt(dirfd int) ([]fs.DirEntry, error) {
	fd, err := syscall.Openat(dirfd, ".", openFlags, 0)
	if err != nil {
		return nil, err
	}
	f := os.NewFile(uintptr(fd), "dir")
	defer f.Close()
	return f.ReadDir(-1)
}

func (p *planDir) names() ([]string, error) {
	ents, err := readDirAt(p.fd)
	if err != nil {
		return nil, err
	}
	var out []string
	for _, e := range ents {
		out = append(out, e.Name())
	}
	return out, nil
}

// create makes a new file in the folder: it must not exist, and is never a link.
func (p *planDir) create(name string) (*os.File, error) {
	fd, err := syscall.Openat(p.fd, name, syscall.O_WRONLY|syscall.O_CREAT|syscall.O_EXCL|syscall.O_NOFOLLOW|syscall.O_CLOEXEC, 0o640)
	if err != nil {
		return nil, &fs.PathError{Op: "create", Path: name, Err: err}
	}
	return os.NewFile(uintptr(fd), name), nil
}

// open reads a file of the folder; a link is refused.
func (p *planDir) open(name string) (*os.File, error) {
	fd, err := syscall.Openat(p.fd, name, syscall.O_RDONLY|syscall.O_NOFOLLOW|syscall.O_CLOEXEC, 0)
	if err != nil {
		return nil, &fs.PathError{Op: "open", Path: name, Err: err}
	}
	return os.NewFile(uintptr(fd), name), nil
}

// remove deletes a file of the folder (a file this process made and is undoing).
func (p *planDir) remove(name string) { _ = syscall.Unlinkat(p.fd, name) }
