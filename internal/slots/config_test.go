//go:build linux

package slots

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// 4. A loose slots file is refused: it decides which account a client gets.
func TestLoadRefusesALooseMode(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "slots.json")
	if err := os.WriteFile(path, []byte(`{"version":1,"servers":[]}`), 0o644); err != nil {
		t.Fatal(err)
	}
	_, err := Load(path)
	if err == nil || !strings.Contains(err.Error(), "chmod 600") {
		t.Fatalf("got %v, want a chmod 600 refusal", err)
	}
	// Item 7 (R2): a loose folder names chmod 700 with the folder, not 600.
	loose := t.TempDir()
	if err := os.Chmod(loose, 0o775); err != nil {
		t.Fatal(err)
	}
	loosePath := filepath.Join(loose, "slots.json")
	if err := os.WriteFile(loosePath, []byte(`{"version":1,"servers":[]}`), 0o600); err != nil {
		t.Fatal(err)
	}
	_, err = Load(loosePath)
	if err == nil || !strings.Contains(err.Error(), "chmod 700 "+loose) {
		t.Fatalf("got %v, want a chmod 700 %s refusal", err, loose)
	}
}

// 1. A "password" key is refused, and the refusal never carries the value.
func TestLoadRefusesAPasswordField(t *testing.T) {
	path := writeSlots(t, `{"version":1,"servers":[{"name":"m910q","realmd":"10.0.0.1:3724","slots":[
		{"id":"1","account":"TAGENT1","character":"Gbmage","port":8771,"settings_from":"/s.json","password":"pw-not-real"}]}]}`)
	_, err := Load(path)
	if err == nil || !strings.Contains(err.Error(), `unknown field "password"`) {
		t.Fatalf("got %v, want an unknown-field refusal", err)
	}
	if strings.Contains(err.Error(), "pw-not-real") {
		t.Fatalf("refusal leaks the value: %v", err)
	}
}

// 2. One laptop, one port: duplicates across servers are refused.
func TestLoadRefusesDuplicatePortsAcrossServers(t *testing.T) {
	path := writeSlots(t, `{"version":1,"servers":[
		{"name":"m910q","realmd":"10.0.0.1:3724","slots":[{"id":"1","account":"A","character":"Gbmage","port":8771,"settings_from":"/s.json"}]},
		{"name":"test2","realmd":"10.0.0.2:3724","slots":[{"id":"1","account":"A","character":"Gbmage","port":8771,"settings_from":"/s.json"}]}]}`)
	_, err := Load(path)
	if err == nil || !strings.Contains(err.Error(), "ports are per laptop") {
		t.Fatalf("got %v, want a per-laptop-ports refusal", err)
	}
}

// 3. m910q/3 is never leasable even when the file lists it as a normal slot;
// per the brief's 9.1 the same holds for tortoise-test2/3.
func TestM910qSlot3IsAlwaysReserved(t *testing.T) {
	path := writeSlots(t, `{"version":1,"servers":[
		{"name":"m910q","realmd":"10.0.0.1:3724","slots":[{"id":"3","account":"TAGENT3","character":"Tac","port":8773,"settings_from":"/s.json"}]},
		{"name":"tortoise-test2","realmd":"10.0.0.2:3724","slots":[]}]}`)
	cfg, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	_, slot, err := cfg.Find("m910q", "3")
	if err != nil {
		t.Fatal(err)
	}
	if slot.Reserved != "owner rule: never slot 3 on m910q" {
		t.Fatalf("m910q/3 Reserved = %q", slot.Reserved)
	}
	// Per the brief's 9.1 tortoise-test2/3 is hard-reserved too, even unlisted.
	_, slot3, err := cfg.Find("tortoise-test2", "3")
	if err != nil {
		t.Fatal(err)
	}
	if slot3.Reserved != "owner rule: never slot 3 on tortoise-test2" {
		t.Fatalf("tortoise-test2/3 Reserved = %q", slot3.Reserved)
	}
}

func writeSlots(t *testing.T, body string) string {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "slots.json")
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

// A server's instance_cap_per_account (the core's MAX_INSTANCE_PER_ACCOUNT_PER_HOUR patch) is read for the raid
// commands; unset is 0 (the stock 5) and a negative value is refused.
func TestServerInstanceCap(t *testing.T) {
	path := writeSlots(t, `{"version":1,"servers":[
		{"name":"m910q","realmd":"10.0.0.1:3724","instance_cap_per_account":30,"slots":[
			{"id":"1","account":"A","character":"Gbmage","port":8771,"settings_from":"/s.json"}]},
		{"name":"test2","realmd":"10.0.0.2:3724","slots":[
			{"id":"1","account":"A","character":"Gbmage","port":8772,"settings_from":"/s.json"}]}]}`)
	cfg, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if got := cfg.ServerInstanceCap("m910q"); got != 30 {
		t.Fatalf("m910q cap = %d, want 30", got)
	}
	if got := cfg.ServerInstanceCap("test2"); got != 0 {
		t.Fatalf("unset cap = %d, want 0 (the stock 5)", got)
	}
	if got := cfg.ServerInstanceCap("nowhere"); got != 0 {
		t.Fatalf("unknown server cap = %d, want 0", got)
	}
	bad := writeSlots(t, `{"version":1,"servers":[
		{"name":"m910q","realmd":"10.0.0.1:3724","instance_cap_per_account":-1,"slots":[]}]}`)
	if _, err := Load(bad); err == nil || !strings.Contains(err.Error(), "instance_cap_per_account") {
		t.Fatalf("negative cap: %v, want a refusal", err)
	}
}
