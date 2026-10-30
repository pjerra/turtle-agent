//go:build unix

package main

import (
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

func TestPIDFileRefusesALiveOwner(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "agent.pid")
	if err := os.WriteFile(path, []byte(strconv.Itoa(os.Getpid())+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := writePIDFile(path); err == nil || !strings.Contains(err.Error(), "names a running process") || !strings.Contains(err.Error(), strconv.Itoa(os.Getpid())) {
		t.Fatalf("err %v", err)
	}
}

func TestPIDFileRemovedOnlyWhileOurs(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "agent.pid")
	remove, err := writePIDFile(path)
	if err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(path)
	if err != nil || strings.TrimSpace(string(raw)) != strconv.Itoa(os.Getpid()) {
		t.Fatalf("pid file %q %v", raw, err)
	}
	// A replaced file (another writer's pid) is kept.
	if err := os.WriteFile(path, []byte("999999999\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	remove()
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("replaced file removed: %v", err)
	}
	// Our own file is removed.
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	remove2, err := writePIDFile(path)
	if err != nil {
		t.Fatal(err)
	}
	remove2()
	if _, err := os.Stat(path); err == nil {
		t.Fatal("own file kept")
	}
}
