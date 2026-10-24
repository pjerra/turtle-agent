package config

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func writeSettings(t *testing.T, mode os.FileMode, body string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "settings.json")
	if err := os.WriteFile(p, []byte(body), mode); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(p, mode); err != nil { // WriteFile honours the umask
		t.Fatal(err)
	}
	return p
}

const good = `{"realmd":"127.0.0.1:3724","accounts":{"Harness":"s3cret-pw"},"bots":["Gorm","Lia"],
"test_spell":133,"spot":{"map":0,"x":1.5,"y":2.5,"z":3.5}}`

func TestLoadDefaultsAndLookup(t *testing.T) {
	s, err := Load(writeSettings(t, 0o600, good))
	if err != nil {
		t.Fatal(err)
	}
	if s.RPC != DefaultRPC || s.TestSpell != 133 || s.Spot == nil || s.Spot.Y != 2.5 || len(s.Bots) != 2 {
		t.Fatalf("loaded %v", s)
	}
	if pw, err := s.Password("HARNESS"); err != nil || pw != "s3cret-pw" {
		t.Fatalf("lookup: %v", err)
	}
	if _, err := s.Password("other"); err == nil || strings.Contains(err.Error(), "s3cret") || strings.Contains(err.Error(), "other") {
		t.Fatalf("missing account: %v", err)
	}
}

func TestLoadsRunMetaDefault(t *testing.T) {
	s, err := Load(writeSettings(t, 0o600, `{"realmd":"h:1","accounts":{"a":"b"},"run_meta":{"model":"haiku","effort":"max","run":"zf-ab-2-s5"}}`))
	if err != nil {
		t.Fatal(err)
	}
	if s.RunMeta != (RunMeta{Model: "haiku", Effort: "max", Run: "zf-ab-2-s5"}) {
		t.Fatalf("run_meta %+v", s.RunMeta)
	}
	s, err = Load(writeSettings(t, 0o600, `{"realmd":"h:1","accounts":{"a":"b"},"run_meta":{"model":"haiku","server_trace":true}}`))
	if err != nil || !s.RunMeta.ServerTrace {
		t.Fatalf("server_trace %+v %v", s.RunMeta, err)
	}
	// 96 joined characters pass: 32+31+31 letters plus two colons.
	full := `{"realmd":"h:1","accounts":{"a":"b"},"run_meta":{"model":"mmmmmmmmmmmmmmmmmmmmmmmmmmmmmmmm","effort":"eeeeeeeeeeeeeeeeeeeeeeeeeeeeeee","run":"rrrrrrrrrrrrrrrrrrrrrrrrrrrrrrr"}}`
	if _, err := Load(writeSettings(t, 0o600, full)); err != nil {
		t.Fatalf("96-char driver refused: %v", err)
	}
}

func TestRejectsGroupReadableFile(t *testing.T) {
	_, err := Load(writeSettings(t, 0o644, good))
	if err == nil || !strings.Contains(err.Error(), "chmod 600") {
		t.Fatalf("err = %v", err)
	}
}

func TestRejectsBadSettings(t *testing.T) {
	for name, body := range map[string]string{
		"unknown field":      `{"realmd":"h:1","accounts":{"a":"b"},"pasword":"x"}`,
		"no port":            `{"realmd":"192.0.2.10","accounts":{"a":"b"}}`,
		"public rpc":         `{"realmd":"h:1","accounts":{"a":"b"},"rpc":"0.0.0.0:8765"}`,
		"no accounts":        `{"realmd":"h:1","accounts":{}}`,
		"empty pw":           `{"realmd":"h:1","accounts":{"a":""}}`,
		"bad rpc_auth":       `{"realmd":"h:1","accounts":{"a":"b"},"rpc_auth":"yes"}`,
		"short token":        `{"realmd":"h:1","accounts":{"a":"b"},"rpc_token":"short"}`,
		"space token":        `{"realmd":"h:1","accounts":{"a":"b"},"rpc_token":"has a space in it, long"}`,
		"token and off":      `{"realmd":"h:1","accounts":{"a":"b"},"rpc_auth":"off","rpc_token":"0123456789abcdef"}`,
		"bad run_meta key":   `{"realmd":"h:1","accounts":{"a":"b"},"run_meta":{"driver":"x"}}`,
		"lone server_trace":  `{"realmd":"h:1","accounts":{"a":"b"},"run_meta":{"server_trace":true}}`,
		"bad run_meta char":  `{"realmd":"h:1","accounts":{"a":"b"},"run_meta":{"model":"a b"}}`,
		"bad run_meta colon": `{"realmd":"h:1","accounts":{"a":"b"},"run_meta":{"run":"a:b"}}`,
		"long run_meta":      `{"realmd":"h:1","accounts":{"a":"b"},"run_meta":{"model":"mmmmmmmmmmmmmmmmmmmmmmmmmmmmmmmm","effort":"eeeeeeeeeeeeeeeeeeeeeeeeeeeeeeee","run":"rrrrrrrrrrrrrrrrrrrrrrrrrrrrrrr"}}`,
	} {
		if _, err := Load(writeSettings(t, 0o600, body)); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
}

func TestPasswordsNeverFormatted(t *testing.T) {
	s, err := Load(writeSettings(t, 0o600, good))
	if err != nil {
		t.Fatal(err)
	}
	for _, out := range []string{fmt.Sprintf("%v", s), fmt.Sprintf("%+v", s), fmt.Sprintf("%#v", s), s.String()} {
		if strings.Contains(out, "s3cret") || strings.Contains(out, "Harness") {
			t.Fatalf("password or account name leaked: %s", out)
		}
	}
}

func TestParseErrorDoesNotEchoContent(t *testing.T) {
	_, err := Load(writeSettings(t, 0o600, `{"realmd":"h:1","accounts":{"a":s3cret-pw}}`))
	if err == nil || strings.Contains(err.Error(), "s3cret") {
		t.Fatalf("err = %v", err)
	}
}

func TestOnlyAccount(t *testing.T) {
	one := Settings{Accounts: map[string]string{"solo": "pw"}}
	if name, err := one.OnlyAccount(); err != nil || name != "solo" {
		t.Fatalf("one account: %q %v", name, err)
	}
	two := Settings{Accounts: map[string]string{"a": "pw", "b": "pw"}}
	if _, err := two.OnlyAccount(); err == nil || strings.Contains(err.Error(), "a") && strings.Contains(err.Error(), "b\"") {
		t.Fatalf("two accounts: %v", err)
	}
}

func TestDungeonsDir(t *testing.T) {
	s, err := Load(writeSettings(t, 0o600, `{"realmd":"h:1","accounts":{"a":"b"},"dungeons_dir":"~/plans"}`))
	home, _ := os.UserHomeDir()
	if err != nil || s.DungeonsDir != filepath.Join(home, "plans") {
		t.Fatalf("%q %v", s.DungeonsDir, err)
	}
	if s, err = Load(writeSettings(t, 0o600, `{"realmd":"h:1","accounts":{"a":"b"}}`)); err != nil || s.DungeonsDir != "" {
		t.Fatalf("%q %v", s.DungeonsDir, err)
	}
}

func TestRotationsDir(t *testing.T) {
	s, err := Load(writeSettings(t, 0o600, `{"realmd":"h:1","accounts":{"a":"b"},"rotations_dir":"~/rot"}`))
	home, _ := os.UserHomeDir()
	if err != nil || s.RotationsDir != filepath.Join(home, "rot") {
		t.Fatalf("%q %v", s.RotationsDir, err)
	}
}

func TestMmapsDir(t *testing.T) {
	dir := t.TempDir()
	s, err := Load(writeSettings(t, 0o600, fmt.Sprintf(`{"realmd":"h:1","accounts":{"a":"b"},"mmaps_dir":%q}`, dir)))
	if err != nil || s.MmapsDir != dir {
		t.Fatalf("%q %v", s.MmapsDir, err)
	}
	home := t.TempDir()
	t.Setenv("HOME", home)
	if err := os.Mkdir(filepath.Join(home, "mmaps"), 0o755); err != nil {
		t.Fatal(err)
	}
	s, err = Load(writeSettings(t, 0o600, `{"realmd":"h:1","accounts":{"a":"b"},"mmaps_dir":"~/mmaps"}`))
	if err != nil || s.MmapsDir != filepath.Join(home, "mmaps") {
		t.Fatalf("~/: %q %v", s.MmapsDir, err)
	}
	_, err = Load(writeSettings(t, 0o600, fmt.Sprintf(`{"realmd":"h:1","accounts":{"a":"b"},"mmaps_dir":%q}`, filepath.Join(dir, "none"))))
	if err == nil || !strings.Contains(err.Error(), "mmaps_dir") {
		t.Fatalf("missing folder: %v", err)
	}
}

func TestTBMAddonDir(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	s, err := Load(writeSettings(t, 0o600, `{"realmd":"h:1","accounts":{"a":"b"},"tbm_addon_dir":"~/tbm"}`))
	if err != nil || s.TBMAddonDir != filepath.Join(home, "tbm") {
		t.Fatalf("%q %v", s.TBMAddonDir, err)
	}
}

func TestRPCAuthSettings(t *testing.T) {
	for body, want := range map[string]bool{
		`{"realmd":"h:1","accounts":{"a":"b"}}`:                                true,
		`{"realmd":"h:1","accounts":{"a":"b"},"rpc_auth":"on"}`:                true,
		`{"realmd":"h:1","accounts":{"a":"b"},"rpc_auth":"off"}`:               false,
		`{"realmd":"h:1","accounts":{"a":"b"},"rpc_token":"0123456789abcdef"}`: true,
	} {
		s, err := Load(writeSettings(t, 0o600, body))
		if err != nil || s.AuthOn() != want {
			t.Errorf("%s: on=%v err=%v, want on=%v", body, s.AuthOn(), err, want)
		}
	}
	s, _ := Load(writeSettings(t, 0o600, `{"realmd":"h:1","accounts":{"a":"b"},"rpc_token":"0123456789abcdef"}`))
	for _, out := range []string{fmt.Sprintf("%v", s), fmt.Sprintf("%#v", s)} {
		if strings.Contains(out, "0123456789abcdef") {
			t.Fatalf("token leaked: %s", out)
		}
	}
}

func TestAddonsOverride(t *testing.T) {
	list := `["Blizzard_BindingUI","Blizzard_InspectUI","Blizzard_MacroUI","Blizzard_RaidUI","Blizzard_TalentUI"]`
	p := writeSettings(t, 0o600, `{"realmd":"h:1","accounts":{"a":"b"},"addons":`+list+`}`)
	s, err := Load(p)
	if err != nil || len(s.Addons) != 5 {
		t.Fatalf("addons %v err %v", s.Addons, err)
	}
	if want := filepath.Dir(p); s.FingerprintDir != want {
		t.Fatalf("fingerprint dir %q, want %q", s.FingerprintDir, want)
	}
	s, err = Load(writeSettings(t, 0o600, good))
	if err != nil || s.Addons != nil {
		t.Fatalf("default addons %v err %v", s.Addons, err)
	}
	if _, err := Load(writeSettings(t, 0o600, `{"realmd":"h:1","accounts":{"a":"b"},"addons":["Blizzard_TalentUI"]}`)); err == nil {
		t.Fatal(`an "addons" list without the fingerprint addons was accepted`)
	}
}

// The Eluna test toolkit is off unless the settings turn it on: a test-server feature, never a default.
func TestElunaToolkitSetting(t *testing.T) {
	s, err := Load(writeSettings(t, 0o600, `{"realmd":"h:1","accounts":{"a":"b"}}`))
	if err != nil || s.ElunaToolkit {
		t.Fatalf("absent: %v %v", s.ElunaToolkit, err)
	}
	s, err = Load(writeSettings(t, 0o600, `{"realmd":"h:1","accounts":{"a":"b"},"eluna_toolkit":true}`))
	if err != nil || !s.ElunaToolkit {
		t.Fatalf("set: %v %v", s.ElunaToolkit, err)
	}
	s, err = Load(writeSettings(t, 0o600, `{"realmd":"h:1","accounts":{"a":"b"},"eluna_toolkit":false}`))
	if err != nil || s.ElunaToolkit {
		t.Fatalf("off: %v %v", s.ElunaToolkit, err)
	}
}
