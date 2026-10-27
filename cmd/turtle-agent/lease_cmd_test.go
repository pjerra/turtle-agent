package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/pjerra/turtle-agent/internal/config"
	"github.com/pjerra/turtle-agent/internal/slots"
)

func leaseTestSlots(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	src := filepath.Join(dir, "src.json")
	body := `{"realmd":"127.0.0.1:3724","rpc":"127.0.0.1:0","accounts":{"TAGENT1":"pw-src-1","TAGENT2":"pw-src-2","HARNESSC":"pw-src-3"}}`
	if err := os.WriteFile(src, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	table := `{"version":1,"servers":[{"name":"m910q","realmd":"100.78.24.50:3724","slots":[
		{"id":"1","account":"TAGENT1","character":"Gbmage","port":18771,"settings_from":"SRC"},
		{"id":"2","account":"HARNESSC","character":"Glacienne","port":18769,"settings_from":"SRC"},
		{"id":"3","reserved":"owner rule: never slot 3 on m910q"},
		{"id":"4","account":"TAGENT2","character":"Tacmdaron","port":18774,"settings_from":"SRC"}]}]}`
	table = strings.ReplaceAll(table, "SRC", src)
	path := filepath.Join(dir, "slots.json")
	if err := os.WriteFile(path, []byte(table), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestLeaseAcquirePrintsTheExactLine(t *testing.T) {
	slotsPath := leaseTestSlots(t)
	code, out, errOut := runCLI("lease", "acquire", "m910q", "--slot", "4", "--holder", "w-brd", "--slots", slotsPath)
	if code != exitOK {
		t.Fatalf("code %d stderr %q", code, errOut)
	}
	if !strings.HasPrefix(out, "leased m910q/4 to w-brd until ") {
		t.Fatalf("stdout %q", out)
	}
	if !strings.Contains(out, ": account TAGENT2, character Tacmdaron, port 18774\n") {
		t.Fatalf("stdout %q", out)
	}
	if strings.Contains(out+errOut, "pw-src-2") {
		t.Fatalf("leak in output")
	}
}

func TestLeaseAcquireNeedsSlotOrAny(t *testing.T) {
	slotsPath := leaseTestSlots(t)
	if code, _, errOut := runCLI("lease", "acquire", "m910q", "--holder", "w-a", "--slots", slotsPath); code != exitUsage || !strings.Contains(errOut, "lease acquire: pass --slot ID or --any") {
		t.Fatalf("code %d stderr %q", code, errOut)
	}
	if code, _, _ := runCLI("lease", "acquire", "m910q", "--slot", "1", "--any", "--holder", "w-a", "--slots", slotsPath); code != exitUsage {
		t.Fatalf("code %d, want usage", code)
	}
}

func TestLeaseRefusalExits15(t *testing.T) {
	slotsPath := leaseTestSlots(t)
	if code, _, _ := runCLI("lease", "acquire", "m910q", "--slot", "1", "--holder", "w-a", "--slots", slotsPath); code != exitOK {
		t.Fatalf("acquire a: %d", code)
	}
	code, _, errOut := runCLI("lease", "acquire", "m910q", "--slot", "1", "--holder", "w-b", "--slots", slotsPath)
	if code != exitSlotRefused || !strings.HasPrefix(strings.TrimSpace(errOut), "lease: ") {
		t.Fatalf("code %d stderr %q", code, errOut)
	}
	if code, _, errOut := runCLI("lease", "acquire", "m910q", "--slot", "3", "--holder", "w-b", "--slots", slotsPath); code != exitSlotRefused {
		t.Fatalf("reserved: code %d stderr %q", code, errOut)
	}
}

func TestLeaseRenewReleaseRoundTrip(t *testing.T) {
	slotsPath := leaseTestSlots(t)
	if code, _, errOut := runCLI("lease", "acquire", "m910q", "--slot", "1", "--holder", "w-a", "--slots", slotsPath); code != exitOK {
		t.Fatalf("acquire: %d %q", code, errOut)
	}
	code, out, errOut := runCLI("lease", "renew", "m910q", "1", "--holder", "w-a", "--slots", slotsPath)
	if code != exitOK || !strings.HasPrefix(out, "renewed m910q/1 for w-a until ") {
		t.Fatalf("code %d stdout %q stderr %q", code, out, errOut)
	}
	code, out, errOut = runCLI("lease", "release", "m910q", "1", "--holder", "w-a", "--force", "--slots", slotsPath)
	if code != exitOK || strings.TrimSpace(out) != "released m910q/1 (was w-a) (forced)" {
		t.Fatalf("code %d stdout %q stderr %q", code, out, errOut)
	}
}

func TestLeaseTTLAndHolderRefused(t *testing.T) {
	slotsPath := leaseTestSlots(t)
	if code, _, errOut := runCLI("lease", "acquire", "m910q", "--slot", "1", "--holder", "w-a", "--ttl", "0", "--slots", slotsPath); code != exitUsage || !strings.Contains(errOut, "lease: --ttl 0: 1 to 720 minutes") {
		t.Fatalf("code %d stderr %q", code, errOut)
	}
	if code, _, errOut := runCLI("lease", "acquire", "m910q", "--slot", "1", "--holder", "bad holder!", "--slots", slotsPath); code != exitUsage || !strings.Contains(errOut, "lease: --holder must be 1-64 letters, digits, . _ -") {
		t.Fatalf("code %d stderr %q", code, errOut)
	}
}

func TestLeaseListFormatOmitsAccounts(t *testing.T) {
	slotsPath := leaseTestSlots(t)
	if code, _, errOut := runCLI("lease", "acquire", "m910q", "--slot", "1", "--holder", "w-a", "--slots", slotsPath); code != exitOK {
		t.Fatalf("acquire: %d %q", code, errOut)
	}
	code, out, errOut := runCLI("lease", "list", "m910q", "--slots", slotsPath)
	if code != exitOK {
		t.Fatalf("code %d stderr %q", code, errOut)
	}
	for _, acct := range []string{"TAGENT1", "TAGENT2", "HARNESSC"} {
		if strings.Contains(out, acct) {
			t.Fatalf("list names the account %s: %q", acct, out)
		}
	}
	lines := strings.Split(strings.TrimSpace(out), "\n")
	if len(lines) != 4 {
		t.Fatalf("want 4 lines, got %q", out)
	}
	if !strings.Contains(lines[0], "held by w-a") {
		t.Fatalf("line 1: %q", lines[0])
	}
	if !strings.Contains(lines[2], "reserved: owner rule: never slot 3 on m910q") {
		t.Fatalf("line 3: %q", lines[2])
	}
	if !strings.HasSuffix(strings.TrimSpace(lines[3]), "free") {
		t.Fatalf("line 4: %q", lines[3])
	}
	code, out, _ = runCLI("lease", "list", "m910q", "--json", "--slots", slotsPath)
	if code != exitOK {
		t.Fatalf("json list: %d", code)
	}
	var states []map[string]any
	if err := json.Unmarshal([]byte(out), &states); err != nil {
		t.Fatalf("json: %v", err)
	}
	if strings.Contains(out, "TAGENT1") {
		t.Fatalf("json names accounts: %q", out)
	}
	var _ = config.Settings{}
	var _ = slots.Lease{}
}
