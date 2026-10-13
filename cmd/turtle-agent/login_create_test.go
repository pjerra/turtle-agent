package main

import (
	"strings"
	"testing"
)

// A bad --create never spends a realmd attempt: the spec is parsed before the
// login, next to --reconnect's own validation.
func TestLoginBadCreateNeverSpendsARealmdAttempt(t *testing.T) {
	var calls int
	stubLogin(t, nil, &calls)
	for _, spec := range []string{"human", "1", "1,2,3,4"} {
		code, _, errOut := runCLI("login", "--config", settingsFile(t, unreachable), "--create", spec, "harness", "Harnessb")
		if code != exitUsage || !strings.Contains(errOut, "--create") {
			t.Fatalf("spec %q: code %d stderr %q", spec, code, errOut)
		}
	}
	if calls != 0 {
		t.Fatalf("realmd was hit %d times", calls)
	}
}

func TestChildArgsPassCreate(t *testing.T) {
	m := fleetMember{Account: "ACC", Character: "Fresh", RPC: "127.0.0.1:9001", Create: "1,2"}
	want := "login --config /s.json --rpc 127.0.0.1:9001 --create 1,2 ACC Fresh"
	if got := strings.Join(childArgs("/s.json", m, 0, ""), " "); got != want {
		t.Fatalf("got  %s\nwant %s", got, want)
	}
}

func TestLoadFleetCreateField(t *testing.T) {
	ms, err := loadFleet(writeFile(t, "f.json", `{"members":[{"account":"A","character":"A","rpc":"127.0.0.1:9001","create":"1,2,1"}]}`), nil)
	if err != nil || ms[0].Create != "1,2,1" {
		t.Fatalf("%v %+v", err, ms)
	}
	_, err = loadFleet(writeFile(t, "f.json", `{"members":[{"account":"A","character":"A","rpc":"127.0.0.1:9001","create":"human"}]}`), nil)
	if err == nil || !strings.Contains(err.Error(), "--create") {
		t.Fatalf("bad create spec: %v", err)
	}
}
