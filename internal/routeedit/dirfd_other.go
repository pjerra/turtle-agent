//go:build !linux

package routeedit

import (
	"errors"
	"os"
)

var errNeedsLinux = errors.New("the route editor's version store needs Linux (it works on directory descriptors)")

type rootDir struct{}

func openRoot(string) (*rootDir, error)                     { return nil, errNeedsLinux }
func (*rootDir) close()                                     {}
func (*rootDir) openPlanDir(string, bool) (*planDir, error) { return nil, errNeedsLinux }
func (*rootDir) planNames() ([]string, error)               { return nil, errNeedsLinux }

type planDir struct{}

func (*planDir) close()                          {}
func (*planDir) names() ([]string, error)        { return nil, errNeedsLinux }
func (*planDir) create(string) (*os.File, error) { return nil, errNeedsLinux }
func (*planDir) open(string) (*os.File, error)   { return nil, errNeedsLinux }
func (*planDir) remove(string)                   {}
