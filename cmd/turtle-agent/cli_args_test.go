package main

import (
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// Every documented flag order for teardown, lease and slot setup parses to
// the same options: flags work before, between or after the positionals,
// values come from `--flag value` or `--flag=value`, and boolean flags take
// no value. Restoring the old leading-only splitPositionals breaks every
// flags-first row below.
func checkParse[T comparable](t *testing.T, name string, got, want T, code, wantCode int) {
	t.Helper()
	if code != wantCode {
		t.Fatalf("%s: exit %d, want %d", name, code, wantCode)
	}
	if code == exitOK && got != want {
		t.Fatalf("%s: options %+v, want %+v", name, got, want)
	}
}

func TestTeardownFlagOrderSameOptions(t *testing.T) {
	full := teardownOptions{dir: "somedir", keepLease: true, releaseAnyway: true, gm: "keep", dryRun: true, asJSON: true, slotsPath: "somefile"}
	dry := teardownOptions{dir: "somedir", gm: "off", dryRun: true}
	for _, r := range []struct {
		name string
		args []string
		want teardownOptions
		code int
	}{
		{"flags after dir", []string{"somedir", "--keep-lease", "--release-anyway", "--gm", "keep", "--dry-run", "--json", "--slots", "somefile"}, full, exitOK},
		{"flags before dir", []string{"--keep-lease", "--release-anyway", "--gm", "keep", "--dry-run", "--json", "--slots", "somefile", "somedir"}, full, exitOK},
		{"flags around dir", []string{"--keep-lease", "somedir", "--release-anyway", "--gm", "keep", "--dry-run", "--json", "--slots", "somefile"}, full, exitOK},
		{"equals form", []string{"--keep-lease", "--release-anyway", "--gm=keep", "--dry-run", "--json", "--slots=somefile", "somedir"}, full, exitOK},
		{"bool equals form", []string{"--dry-run=true", "--json=true", "--keep-lease=true", "--release-anyway=true", "--gm", "keep", "--slots", "somefile", "somedir"}, full, exitOK},
		// The exact reported failures: each must parse, not print usage.
		{"dry-run before dir", []string{"--dry-run", "somedir"}, dry, exitOK},
		{"dry-run json before dir", []string{"--dry-run", "--json", "somedir"}, teardownOptions{dir: "somedir", gm: "off", dryRun: true, asJSON: true}, exitOK},
		{"dry-run slots before dir", []string{"--dry-run", "--slots", "somefile", "somedir"}, teardownOptions{dir: "somedir", gm: "off", dryRun: true, slotsPath: "somefile"}, exitOK},
		{"dry-run equals before dir", []string{"--dry-run=true", "somedir"}, dry, exitOK},
		{"dry-run after dir", []string{"somedir", "--dry-run"}, dry, exitOK},
		{"no dir", []string{"--dry-run"}, teardownOptions{}, exitUsage},
		{"two dirs", []string{"a", "b", "--dry-run"}, teardownOptions{}, exitUsage},
		{"bad gm", []string{"--gm", "sometimes", "somedir"}, teardownOptions{}, exitUsage},
	} {
		got, code := parseTeardownArgs(r.args, io.Discard)
		checkParse(t, r.name, got, r.want, code, r.code)
	}
}

func TestLeaseAcquireFlagOrderSameOptions(t *testing.T) {
	full := leaseAcquireOptions{server: "m910q", slotID: "4", holder: "w-a", ttlMin: 5, slotsPath: "somefile"}
	for _, r := range []struct {
		name string
		args []string
		want leaseAcquireOptions
		code int
	}{
		{"server first", []string{"m910q", "--slot", "4", "--holder", "w-a", "--ttl", "5", "--slots", "somefile"}, full, exitOK},
		{"server last", []string{"--slot", "4", "--holder", "w-a", "--ttl", "5", "--slots", "somefile", "m910q"}, full, exitOK},
		{"server between", []string{"--slot", "4", "m910q", "--holder", "w-a", "--ttl", "5", "--slots", "somefile"}, full, exitOK},
		{"equals form", []string{"--slot=4", "--holder=w-a", "--ttl=5", "--slots=somefile", "m910q"}, full, exitOK},
		{"any first", []string{"--any", "--holder", "w-a", "m910q"}, leaseAcquireOptions{server: "m910q", any: true, holder: "w-a", ttlMin: 180}, exitOK},
		{"no server", []string{"--slot", "4", "--holder", "w-a"}, leaseAcquireOptions{}, exitUsage},
	} {
		got, code := parseLeaseAcquireArgs(r.args, io.Discard)
		checkParse(t, r.name, got, r.want, code, r.code)
	}
}

func TestLeaseRenewFlagOrderSameOptions(t *testing.T) {
	full := leaseRenewOptions{server: "m910q", slotID: "1", holder: "w-a", leaseID: "lid-9", ttlMin: 7, slotsPath: "somefile"}
	for _, r := range []struct {
		name string
		args []string
		want leaseRenewOptions
		code int
	}{
		{"positionals first", []string{"m910q", "1", "--holder", "w-a", "--lease-id", "lid-9", "--ttl", "7", "--slots", "somefile"}, full, exitOK},
		{"positionals last", []string{"--holder", "w-a", "--lease-id", "lid-9", "--ttl", "7", "--slots", "somefile", "m910q", "1"}, full, exitOK},
		{"positionals split", []string{"m910q", "--holder", "w-a", "--lease-id", "lid-9", "1", "--ttl", "7", "--slots", "somefile"}, full, exitOK},
		{"equals form", []string{"--holder=w-a", "--lease-id=lid-9", "--ttl=7", "--slots=somefile", "m910q", "1"}, full, exitOK},
		{"one positional", []string{"m910q", "--holder", "w-a", "--lease-id", "lid-9"}, leaseRenewOptions{}, exitUsage},
	} {
		got, code := parseLeaseRenewArgs(r.args, io.Discard)
		checkParse(t, r.name, got, r.want, code, r.code)
	}
}

func TestLeaseReleaseFlagOrderSameOptions(t *testing.T) {
	plain := leaseReleaseOptions{server: "m910q", slotID: "1", holder: "w-a", leaseID: "lid-9", slotsPath: "somefile"}
	forced := leaseReleaseOptions{server: "m910q", slotID: "1", holder: "w-a", force: true, slotsPath: "somefile"}
	for _, r := range []struct {
		name string
		args []string
		want leaseReleaseOptions
		code int
	}{
		{"positionals first", []string{"m910q", "1", "--holder", "w-a", "--lease-id", "lid-9", "--slots", "somefile"}, plain, exitOK},
		{"positionals last", []string{"--holder", "w-a", "--lease-id", "lid-9", "--slots", "somefile", "m910q", "1"}, plain, exitOK},
		{"positionals split", []string{"m910q", "--holder", "w-a", "1", "--lease-id", "lid-9", "--slots", "somefile"}, plain, exitOK},
		{"equals form", []string{"--holder=w-a", "--lease-id=lid-9", "--slots=somefile", "m910q", "1"}, plain, exitOK},
		{"force before positionals", []string{"--force", "--holder", "w-a", "--slots", "somefile", "m910q", "1"}, forced, exitOK},
		{"force equals before positionals", []string{"--force=true", "--holder=w-a", "--slots=somefile", "m910q", "1"}, forced, exitOK},
	} {
		got, code := parseLeaseReleaseArgs(r.args, io.Discard)
		checkParse(t, r.name, got, r.want, code, r.code)
	}
}

func TestLeaseListFlagOrderSameOptions(t *testing.T) {
	full := leaseListOptions{server: "m910q", asJSON: true, slotsPath: "somefile"}
	for _, r := range []struct {
		name string
		args []string
		want leaseListOptions
		code int
	}{
		{"server first", []string{"m910q", "--json", "--slots", "somefile"}, full, exitOK},
		{"server last", []string{"--json", "--slots", "somefile", "m910q"}, full, exitOK},
		{"equals form", []string{"--json=true", "--slots=somefile", "m910q"}, full, exitOK},
		{"no server", []string{"--json", "--slots", "somefile"}, leaseListOptions{asJSON: true, slotsPath: "somefile"}, exitOK},
		{"bare", []string{}, leaseListOptions{}, exitOK},
		{"two servers", []string{"a", "b"}, leaseListOptions{}, exitUsage},
	} {
		got, code := parseLeaseListArgs(r.args, io.Discard)
		checkParse(t, r.name, got, r.want, code, r.code)
	}
}

func TestSlotSetupFlagOrderSameOptions(t *testing.T) {
	full := slotSetupOptions{server: "m910q", slot: "1", holder: "w-a", dir: "D", binary: "B", dungeonsDir: "G", character: "C", slotsPath: "F"}
	for _, r := range []struct {
		name string
		args []string
		want slotSetupOptions
		code int
	}{
		{"positionals first", []string{"m910q", "1", "--holder", "w-a", "--dir", "D", "--binary", "B", "--dungeons-dir", "G", "--character", "C", "--slots", "F"}, full, exitOK},
		{"positionals last", []string{"--holder", "w-a", "--dir", "D", "--binary", "B", "--dungeons-dir", "G", "--character", "C", "--slots", "F", "m910q", "1"}, full, exitOK},
		{"positionals split", []string{"m910q", "--holder", "w-a", "1", "--dir", "D", "--binary", "B", "--dungeons-dir", "G", "--character", "C", "--slots", "F"}, full, exitOK},
		{"equals form", []string{"--holder=w-a", "--dir=D", "--binary=B", "--dungeons-dir=G", "--character=C", "--slots=F", "m910q", "1"}, full, exitOK},
		{"no holder", []string{"m910q", "1", "--dir", "D"}, slotSetupOptions{}, exitUsage},
	} {
		got, code := parseSlotSetupArgs(r.args, io.Discard)
		checkParse(t, r.name, got, r.want, code, r.code)
	}
}

// Flags-first forms end to end: the reported `teardown --dry-run <dir>`
// usage failure, plus the same fix in lease and slot setup.
func TestCLIFlagsBeforePositionalsEndToEnd(t *testing.T) {
	t.Run("teardown dry-run", func(t *testing.T) {
		steps := []teardownCLIStep{{Step: "logout", Status: "ok", Detail: "logged out"}}
		dir, slotsPath, _, _, restore := teardownCLIFixture(t, steps, false)
		defer restore()
		for _, args := range [][]string{
			{"teardown", "--dry-run", dir, "--slots", slotsPath},
			{"teardown", "--dry-run", "--json", dir, "--slots", slotsPath},
			{"teardown", "--dry-run", "--slots", slotsPath, dir},
			{"teardown", "--dry-run=true", dir, "--slots", slotsPath},
		} {
			code, out, errOut := runCLI(args...)
			if code != exitOK {
				t.Fatalf("%v: exit %d stderr %q", args, code, errOut)
			}
			if !strings.Contains(out, "plan") {
				t.Fatalf("%v: no plan line: %q", args, out)
			}
		}
	})
	t.Run("lease", func(t *testing.T) {
		slotsPath := leaseTestSlots(t)
		code, out, errOut := runCLI("lease", "acquire", "--slot", "4", "--holder", "w-e2e", "--slots", slotsPath, "m910q")
		if code != exitOK {
			t.Fatalf("acquire server-last: %d %q", code, errOut)
		}
		if !strings.HasPrefix(out, "leased m910q/4 to w-e2e until ") {
			t.Fatalf("stdout %q", out)
		}
		code, out, errOut = runCLI("lease", "list", "--slots", slotsPath, "m910q")
		if code != exitOK || !strings.Contains(out, "held by w-e2e") {
			t.Fatalf("list server-last: %d %q %q", code, out, errOut)
		}
	})
	t.Run("slot setup", func(t *testing.T) {
		slotsPath, _, _ := slotSetupTestEnv(t)
		slotSetupAcquire(t, slotsPath, "w-a")
		bin := filepath.Join(t.TempDir(), "turtle-agent")
		if err := os.WriteFile(bin, []byte("x"), 0o755); err != nil {
			t.Fatal(err)
		}
		dir := filepath.Join(t.TempDir(), "client")
		code, _, errOut := runCLI("slot", "setup", "--holder", "w-a", "--dir", dir, "--binary", bin, "--slots", slotsPath, "m910q", "1")
		if code != exitOK {
			t.Fatalf("setup positionals-last: %d %q", code, errOut)
		}
	})
}
