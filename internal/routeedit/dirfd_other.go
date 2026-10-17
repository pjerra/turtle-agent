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

// Source needs Linux too (see source_linux.go).
type Source struct{}

func OpenSource(string) (*Source, error)               { return nil, errNeedsLinux }
func (*Source) Path() string                           { return "" }
func (*Source) Close()                                 {}
func (*Source) ReadPlan(string, int64) ([]byte, error) { return nil, errNeedsLinux }
func (*Source) Verify() error                          { return errNeedsLinux }
func (*Store) Verify() error                           { return errNeedsLinux }
func CheckHeld(*Store, *Source, []string) error        { return errNeedsLinux }
func (*Store) FreeBytes() (uint64, error)              { return 0, errNeedsLinux }
