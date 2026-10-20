package main

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
)

// The report files are written under --out and nowhere else, whatever the folder already
// holds. Two things guard that, and a lexical check is neither:
//
//   - safeOutDir refuses a --out that is, or has a component that is, a symlink, so the
//     folder the reports land in is the real one the user named;
//   - writeFilesIn refuses a destination that already exists as anything but a regular file
//     (a symlink, even a dangling one, a directory), checks both before writing either, and
//     writes each file to a new temporary file of its own (O_EXCL) in a directory handle it
//     opened (os.Root: no component may leave the folder) and renames it over the target.
//     A rename replaces whatever is at the target, a symlink included, without following it,
//     so a link planted between the check and the write still overwrites nothing outside.

// safeOutDir makes dir (and the folders above it that are missing) and returns its absolute
// path, or says which component is a symlink. Nothing is created through a symlink: the
// existing components are checked before anything is made, and all of them again after.
func safeOutDir(dir string) (string, error) {
	abs, err := filepath.Abs(dir)
	if err != nil {
		return "", err
	}
	if err := noSymlinkIn(abs); err != nil {
		return "", err
	}
	if err := os.MkdirAll(abs, 0o755); err != nil {
		return "", err
	}
	if err := noSymlinkIn(abs); err != nil {
		return "", err
	}
	fi, err := os.Lstat(abs)
	if err != nil {
		return "", err
	}
	if !fi.IsDir() {
		return "", fmt.Errorf("--out %s is not a folder", abs)
	}
	return abs, nil
}

// noSymlinkIn walks the absolute path from the root down and refuses a component that is a
// symlink; the walk stops at the first component that does not exist.
func noSymlinkIn(abs string) error {
	vol := filepath.VolumeName(abs)
	rest := strings.TrimPrefix(abs, vol)
	cur := vol + string(filepath.Separator)
	for _, part := range strings.Split(rest, string(filepath.Separator)) {
		if part == "" {
			continue
		}
		cur = filepath.Join(cur, part)
		fi, err := os.Lstat(cur)
		if errors.Is(err, fs.ErrNotExist) {
			return nil
		}
		if err != nil {
			return err
		}
		if fi.Mode()&fs.ModeSymlink != 0 {
			return fmt.Errorf("--out %s contains a symlink (%s): name the real folder", abs, cur)
		}
	}
	return nil
}

// writeFilesIn writes the named files (base name -> content) into dir without following a
// symlink, and writes nothing when any destination is not a plain file or absent.
func writeFilesIn(dir string, files map[string][]byte) error {
	root, err := os.OpenRoot(dir)
	if err != nil {
		return err
	}
	defer root.Close()
	for name := range files {
		if name != filepath.Base(name) || name == "." || name == ".." || strings.ContainsAny(name, `/\`) {
			return fmt.Errorf("report file name %q is not a plain file name", name)
		}
		fi, err := root.Lstat(name)
		switch {
		case errors.Is(err, fs.ErrNotExist):
		case err != nil:
			return err
		case !fi.Mode().IsRegular():
			return fmt.Errorf("report %s exists in %s and is not a regular file (%s): refusing to write through it", name, dir, fi.Mode().Type())
		}
	}
	for name, content := range files {
		if err := writeThroughTemp(root, name, content); err != nil {
			return err
		}
	}
	return nil
}

// writeThroughTemp writes content to a fresh temporary file in root and renames it to name.
func writeThroughTemp(root *os.Root, name string, content []byte) (err error) {
	var tmp string
	var f *os.File
	for i := 0; ; i++ {
		tmp = fmt.Sprintf(".%s.%d.%d.tmp", name, os.Getpid(), i)
		f, err = root.OpenFile(tmp, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o644)
		if err == nil {
			break
		}
		if !errors.Is(err, fs.ErrExist) || i > 100 {
			return err
		}
	}
	defer func() {
		if err != nil {
			f.Close()
			root.Remove(tmp)
		}
	}()
	if _, err = f.Write(content); err != nil {
		return err
	}
	if err = f.Close(); err != nil {
		return err
	}
	return root.Rename(tmp, name)
}
